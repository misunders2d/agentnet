package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A real signed backlog is already durable. A gesture admitted while its
// first carrier is handed over must take the next turn, not wait for the
// flush's stale snapshot. The phone is offline so relay custody is real.
func TestHistoryBacklogLateInteractiveTurn(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 500)
	a := w.alice
	// Advertise the reader once, then take it offline; queued copies remain
	// eligible for real relay custody rather than waiting on capabilities.
	stopPhone := runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	stopPhone()
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// Drive this fixture synchronously before replacing its HTTP transport.
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	a.convWork.take()

	rows, err := a.historySourceRows(a.store.db, "dir='in'", "conv,ms,id", 500)
	if err != nil {
		t.Fatal(err)
	}
	var copies []outCopy
	for _, row := range rows {
		copy, err := a.prepareHistorySource(phone.Self(), row)
		if err != nil {
			t.Fatal(err)
		}
		if copy != nil {
			copies = append(copies, *copy)
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = insertCopies(tx, copies); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(copies) < 500 {
		t.Fatalf("backlog too small: %d", len(copies))
	}
	late := sealTo(t, a, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "late interactive gesture"})
	second := sealTo(t, a, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "next readable turn"})
	base := a.hub.http.Transport
	var posted []string
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/messages" {
			body, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			var env envelope.Envelope
			err = json.NewDecoder(body).Decode(&env)
			body.Close()
			if err != nil {
				return nil, err
			}
			posted = append(posted, env.ID)
			if len(posted) == 1 {
				for _, env := range []envelope.Envelope{late, second} {
					in, err := envelope.Open(env, w.bob.id, w.bob.Address, a.Self())
					if err != nil {
						return nil, err
					}
					if err = a.store.addOutbox(env, in, "", nil); err != nil {
						return nil, err
					}
				}
				// Reproduce the original early-yield trigger: admitted file
				// work arrives during the first history handover. The late
				// live turns must still pass before yielding to that work.
				a.convWork.bits.Or(convServe)
			}
		}
		return base.RoundTrip(r)
	})
	defer func() { a.hub.http.Transport = base }()
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 3 || posted[1] != late.ID || posted[2] != second.ID {
		t.Fatalf("late readable FIFO did not get next delivery turns: first=%v", posted[:min(5, len(posted))])
	}
	var remaining, handedOver int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history' AND state=?`, phone.Address, stateQueued).Scan(&remaining); err != nil || remaining != 499 {
		t.Fatalf("history backlog was lost instead of yielding: %d %v", remaining, err)
	}
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id IN (?,?) AND state IN ('custody','delivered')`, late.ID, second.ID).Scan(&handedOver); err != nil || handedOver != 2 {
		t.Fatalf("live turns were not durably handed over: %d %v", handedOver, err)
	}
}

func TestHistoryWindowOfflineReceiptRestartAndCompletion(t *testing.T) {
	w, phone, _, ids := historyCatchupFixture(t, 125)
	a := w.alice
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	var first int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&first); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if first != historyPage || count != first {
		t.Fatalf("offline producer grew beyond its outstanding page: %d -> %d", first, count)
	}
	if _, err := a.store.db.Exec(`UPDATE outbox SET state='custody' WHERE recipient=? AND sub='history'`, phone.Address); err != nil {
		t.Fatal(err)
	}
	home := a.home
	a.Close()
	var err error
	a, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if len(historyCopiedItems(t, a, phone)) != first {
		t.Fatal("restart forgot custody window")
	}
	// A terminal receipt reopens production using the existing push wake.
	var id string
	if err = a.store.db.QueryRow(`SELECT id FROM outbox WHERE recipient=? AND sub='history' LIMIT 1`, phone.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	a.convWork.take()
	receipt, _ := json.Marshal(protocol.ReceiptEvent{Seq: 1, ID: id, State: protocol.StateQuarantined})
	if err = a.dispatch(tctx(t), "receipt", string(receipt)); err != nil {
		t.Fatal(err)
	}
	if a.convWork.bits.Load()&convHistory == 0 {
		t.Fatal("terminal history receipt did not reopen producer")
	}
	if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if len(historyCopiedItems(t, a, phone)) <= first {
		t.Fatal("partial window did not resume")
	}
	// Drain actual signed carriers into the phone and acknowledge proven storage;
	// each subsequent bounded producer pass must eventually cover all originals.
	seq := int64(1)
	for rounds := 0; rounds < 12; rounds++ {
		envs, err := a.store.queued()
		if err != nil {
			t.Fatal(err)
		}
		rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history' AND state='custody'`, phone.Address)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			var env envelope.Envelope
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(raw), &env); err != nil {
				t.Fatal(err)
			}
			envs = append(envs, env)
		}
		rows.Close()
		for _, env := range envs {
			if env.To != phone.Address {
				continue
			}
			if err = phone.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			state, err := phone.store.disposition(env.ID)
			if err != nil || state != protocol.StateDelivered {
				t.Fatalf("not admitted: %s %v", state, err)
			}
			seq++
			raw, _ := json.Marshal(protocol.ReceiptEvent{Seq: seq, ID: env.ID, State: state})
			if err = a.dispatch(tctx(t), "receipt", string(raw)); err != nil {
				t.Fatal(err)
			}
		}
		more, err := a.historyCatchupPage(tctx(t), phone.Self())
		if err != nil {
			t.Fatal(err)
		}
		if !more && len(historyCopiedItems(t, a, phone)) == len(ids) {
			return
		}
	}
	t.Fatalf("catchup incomplete: %d/%d", len(historyCopiedItems(t, a, phone)), len(ids))
}
