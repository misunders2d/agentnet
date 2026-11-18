package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// B2: a page is stored whole or not at all. Failing before its commit
// stores nothing and the Hub's redelivery stores each turn once; a page
// committed but not acknowledged, pushed again, stores nothing new, queues
// no second alert and asks for its receipts again.
func TestReceivePageReplayStoresOnce(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	stopBob := runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opener"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the DM on bob", func() bool {
		var got int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND body=?`, conv, "opener").Scan(&got)
		return got == 1
	})
	stopBob()
	var envs []envelope.Envelope
	for i := range 6 {
		sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: fmt.Sprintf("page %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, w.bob.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	stored := func() (rows, unacked, alerts int) {
		t.Helper()
		if err := w.bob.store.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE acked=0) FROM inbox WHERE conv=? AND body LIKE 'page %'`, conv).Scan(&rows, &unacked); err != nil {
			t.Fatal(err)
		}
		w.bob.store.db.QueryRow(`SELECT coalesce(sum(count),0) FROM alerts WHERE conv=?`, conv).Scan(&alerts)
		return
	}
	if _, err := w.bob.store.db.Exec(`INSERT INTO config(k,v) VALUES('alerts','on') ON CONFLICT(k) DO UPDATE SET v='on'`); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crashed before the commit")
	pageFault = func() error { return crash }
	err := w.bob.receivePage(tctx(t), envs)
	pageFault = nil
	if !errors.Is(err, crash) {
		t.Fatalf("page error %v, want the crash", err)
	}
	if rows, _, _ := stored(); rows != 0 {
		t.Fatalf("%d turns stored by a page that never committed", rows)
	}
	if err := w.bob.receivePage(tctx(t), envs); err != nil {
		t.Fatal(err)
	}
	rows, unacked, alerts := stored()
	if rows != len(envs) || unacked != len(envs) || alerts != len(envs) {
		t.Fatalf("redelivered page stored %d (%d unacknowledged, %d alerts), want %d", rows, unacked, alerts, len(envs))
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET acked=1 WHERE conv=?`, conv); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.receivePage(tctx(t), envs); err != nil { // committed, its acknowledgement lost
		t.Fatal(err)
	}
	again, unacked, alertsAgain := stored()
	if again != rows || alertsAgain != alerts || unacked != len(envs) {
		t.Fatalf("pushed again: %d turns (was %d), %d alerts (was %d), %d receipts asked again, want %d", again, rows, alertsAgain, alerts, unacked, len(envs))
	}
}

// A signed DM topic rename staged in a page whose sender is revoked before
// the page is written is held, not applied, and earns no delivered receipt.
func TestReceivePageRechecksRevokedSender(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	stopBob := runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	first, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opens a topic", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	topic := convMsgByID(t, w.alice, conv, first.ID).Topic
	eventually(t, "the topic on bob", func() bool {
		var got int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND topic=?`, conv, topic).Scan(&got)
		return got == 1
	})
	stopBob()
	sealed := func(lid string) envelope.Envelope {
		t.Helper()
		var raw string
		if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=?`, lid, w.bob.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	plain, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "in custody already"})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.RenameSharedTopic(tctx(t), conv, topic, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	ordinary, env := sealed(plain.LID), sealed(sent.LID)
	p := &receivePage{names: map[string]bool{}}
	pctx := context.WithValue(tctx(t), receivePageKey{}, p)
	for _, e := range []envelope.Envelope{ordinary, env} {
		in, sender, ok := w.bob.classify(e)
		if !ok || !p.pageTurn(in) {
			t.Fatalf("%s not a page turn", e.ID)
		}
		p.cur = e
		if err = w.bob.storeReceived(context.WithValue(pctx, openedKey{}, opened{e.ID, sender, in}), e); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.rows) != 2 {
		t.Fatalf("%d staged, want 2", len(p.rows))
	}
	// The rest of the page is prepared meanwhile; alice's device is revoked.
	if err = w.bob.store.endRecipient(w.alice.Address, w.alice.Self().Fingerprint(), true); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.storePage(tctx(t), p); err != nil {
		t.Fatal(err)
	}
	if r, found, e := w.bob.SharedTopicTitle(conv, topic); e != nil || found {
		t.Fatalf("rename applied after revocation: %+v %v", r, e)
	}
	var admitted, held int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, env.ID).Scan(&admitted)
	w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, env.ID).Scan(&held)
	if admitted != 0 || held != 1 {
		t.Fatalf("revoked sender's rename: %d admitted, %d held", admitted, held)
	}
	// An ordinary message the relay already holds in custody still comes in.
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, ordinary.ID).Scan(&admitted)
	if admitted != 1 {
		t.Fatalf("ordinary message in custody: %d admitted", admitted)
	}
}

// A staged turn whose page write fails transiently keeps its count across
// restaging: after receiveAttempts in a row it is held, and the turns after
// it come in, instead of ending the stream at attempt one forever.
func TestReceivePageCountsTransientFailures(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	stopBob := runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opener"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the DM on bob", func() bool {
		var got int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND body=?`, conv, "opener").Scan(&got)
		return got == 1
	})
	stopBob()
	var envs []envelope.Envelope
	for i := range 2 {
		sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: fmt.Sprintf("turn %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, w.bob.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	stuck := envs[0].ID
	pageRowFault = func(env envelope.Envelope) error {
		if env.ID == stuck {
			return context.DeadlineExceeded // transient, every time
		}
		return nil
	}
	defer func() { pageRowFault = nil }()
	count := func(table, id string) (n int) {
		w.bob.store.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE id=?`, id).Scan(&n)
		return
	}
	for attempt := 1; attempt < receiveAttempts; attempt++ {
		if err := w.bob.receivePage(tctx(t), envs); err == nil {
			t.Fatalf("attempt %d: no error for a transient failure", attempt)
		}
	}
	if err := w.bob.receivePage(tctx(t), envs); err != nil {
		t.Fatalf("attempt %d still ends the stream: %v", receiveAttempts, err)
	}
	if count("quarantine", stuck) != 1 || count("inbox", stuck) != 0 || count("inbox", envs[1].ID) != 1 {
		t.Fatalf("after %d attempts: held %d, admitted %d, next admitted %d", receiveAttempts, count("quarantine", stuck), count("inbox", stuck), count("inbox", envs[1].ID))
	}
}
