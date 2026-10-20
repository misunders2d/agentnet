package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A decision response's Attempt echoes the submitted CAS, even when its
// state describes a newer attempt. Only a fresh ordinary execution status
// can reconcile that state with its actual current attempt.
func TestStopRefusalRefreshesAuthoritativeExecution(t *testing.T) {
	w := newWorld(t, "")
	host := w.alice
	defer func() {
		if host != w.alice {
			host.Close()
		}
	}()
	if _, err := host.CreatePerson(tctx(t), "Owner"); err != nil {
		t.Fatal(err)
	}
	stopHost := runAgent(t, host)
	link := func(name string) *Agent {
		dev, awaited, _ := linkPhone(t, host, name)
		if err := host.DecideLink(tctx(t), pendingLink(t, host).ID, true); err != nil {
			t.Fatal(err)
		}
		if out := <-awaited; out.err != nil {
			t.Fatal(out.err)
		}
		return dev
	}
	phone, tablet := link("phone"), link("tablet")
	owner, _, err := host.Person()
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range []*Agent{phone, tablet} {
		if _, err := dev.refreshPerson(tctx(t), owner.Person, false); err != nil {
			t.Fatal(err)
		}
	}
	stopPhone := runPublished(t, phone)
	stopTablet := runPublished(t, tablet)
	eventually(t, "requester can read signed statuses", func() bool {
		features, err := host.relayFeatures(tctx(t))
		ok, _ := host.capSupport(tctx(t), phone.Address, phone.Self(), features, protocol.CapHeadless)
		return err == nil && ok
	})
	stopPhone()
	stopTablet()
	stopHost()
	phone.stopBackgroundPosts()
	host.stopBackgroundPosts()
	type expected struct {
		id, state string
		attempt   int64
	}
	var saved []expected
	for _, tc := range []struct {
		state   string
		attempt int64
	}{{stateAnswered, 1}, {stateCancelled, 1}, {stateResolved, 1}, {stateAnswered, 2}, {stateRunning, 2}} {
		t.Run(tc.state+string(rune('0'+tc.attempt)), func(t *testing.T) {
			q, err := phone.SendMessage(WithQueuedSend(tctx(t), protocol.NewID()), Outgoing{To: host.Address, Kind: envelope.KindTask, Body: "Retained exact task"})
			if err != nil {
				t.Fatal(err)
			}
			original, err := phone.store.outboxEnvelope(q.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.verifyAndStore(tctx(t), original); err != nil {
				t.Fatal(err)
			}
			if _, err = host.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,status_due=1 WHERE id=?`, stateRunning, q.ID); err != nil {
				t.Fatal(err)
			}
			host.tellStatuses(tctx(t))
			deliverDeviceHistory(t, host, phone, envelope.SubStatus)
			before := execOf(t, phone, q.ID)
			if before == nil || before.State != stateRunning || before.Attempt != 1 {
				t.Fatalf("initial signed running status missing: %+v", before)
			}
			if _, err = host.store.db.Exec(`UPDATE inbox SET state=?,attempts=?,status_due=0 WHERE id=?`, tc.state, tc.attempt, q.ID); err != nil {
				t.Fatal(err)
			}
			token := protocol.NewID()
			if _, err = phone.Decide(WithQueuedSend(tctx(t), token), host.Address, q.ID, phone.Self().Fingerprint(), "cancel", stateRunning, 1, "", ""); err != nil {
				t.Fatal(err)
			}
			pending, err := phone.StopFor(q.ID, phone.Self().Fingerprint(), before)
			if err != nil || pending == nil || !pending.Pending || pending.Result != nil || execOf(t, phone, q.ID).State != stateRunning {
				t.Fatalf("queued offline Stop claimed completion: %+v %v", pending, err)
			}
			decision, err := phone.store.outboxEnvelope(token)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.verifyAndStore(tctx(t), decision); err != nil {
				t.Fatal(err)
			}
			eventually(t, "correlated host refusal is durable", func() bool {
				var count int
				err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND json_extract(body,'$.decision')=?`, envelope.SubStatus, token).Scan(&count)
				return err == nil && count == 1
			})
			if tc.state == stateCancelled && tc.attempt == 1 {
				var due int
				if err = host.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, q.ID).Scan(&due); err != nil || due == 0 {
					t.Fatalf("refusal did not durably queue current status: due=%d err=%v", due, err)
				}
				if err = host.Close(); err != nil {
					t.Fatal(err)
				}
				host, err = Open(host.home)
				if err != nil {
					t.Fatal(err)
				}
				host.stopBackgroundPosts()
				if err = host.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, q.ID).Scan(&due); err != nil || due == 0 {
					t.Fatalf("restart lost owed current status: due=%d err=%v", due, err)
				}
			}
			host.tellStatuses(tctx(t))
			var normal int
			if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND ref_id=? AND coalesce(json_extract(body,'$.decision'),'')='' AND json_extract(body,'$.state')=? AND json_extract(body,'$.attempt')=?`, envelope.SubStatus, q.ID, tc.state, tc.attempt).Scan(&normal); err != nil || normal == 0 {
				t.Fatalf("CAS refusal failed to publish actual current state/attempt: %s/%d count=%d err=%v", tc.state, tc.attempt, normal, err)
			}
			deliverDeviceHistory(t, host, phone, envelope.SubStatus)
			after := execOf(t, phone, q.ID)
			if after == nil || after.State != tc.state || after.Attempt != tc.attempt {
				t.Fatalf("signed normal status failed to reconcile: %+v", after)
			}
			result, err := phone.StopFor(q.ID, phone.Self().Fingerprint(), before)
			if err != nil || result == nil || result.Pending || result.Result == nil || result.Result.Refused == "" || result.Result.State != tc.state {
				t.Fatalf("refusal reason or host snapshot lost: %+v %v", result, err)
			}
			var body string
			if err = host.store.db.QueryRow(`SELECT body FROM outbox WHERE sub=? AND json_extract(body,'$.decision')=?`, envelope.SubStatus, token).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var snapshot envelope.Status
			if err = json.Unmarshal([]byte(body), &snapshot); err != nil || snapshot.Attempt != 1 {
				t.Fatal("decision correlation changed", body, err)
			}
			for range 3 {
				if _, err = phone.deviceHistoryPage(tablet.Self()); err != nil {
					t.Fatal(err)
				}
			}
			deliverDeviceHistory(t, phone, tablet, envelope.SubDeviceHistory)
			linked := execOf(t, tablet, q.ID)
			if linked == nil || linked.State != tc.state || linked.Attempt != tc.attempt {
				t.Fatalf("linked history lost current attempt: %+v", linked)
			}
			saved = append(saved, expected{q.ID, tc.state, tc.attempt})
		})
	}
	if err := phone.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(phone.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, want := range saved {
		got := execOf(t, reopened, want.id)
		if got == nil || got.State != want.state || got.Attempt != want.attempt {
			t.Fatalf("restart lost authoritative execution: %+v want=%+v", got, want)
		}
	}
}

func TestGroupStopRefusalNamesLogicalRequest(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	host := w.alice
	part := p6Member(t, host, host, packet.State.Conv)
	phone, awaited, _ := linkPhone(t, host, "stop-phone")
	if err := host.DecideLink(tctx(t), pendingLink(t, host).ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	stopPhone := runPublished(t, phone)
	eventually(t, "other group member sees the agent", func() bool {
		p, err := w.bob.Participation(part.PID)
		return err == nil && p.Claimable()
	})
	q, err := w.bob.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), part.PID, envelope.KindTask, "Retained group request")
	if err != nil {
		t.Fatal(err)
	}
	stopPhone()
	for _, stop := range stops {
		stop()
	}
	host.stopBackgroundPosts()
	phone.stopBackgroundPosts()
	// Current producers make the executable copy's ID equal its LID. Older
	// signed requests can retain a distinct physical ID, which admission and
	// the linked history/Stop route still support. Keep the producer's exact
	// current request and authority, signed in that older physical format.
	var raw string
	if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND lid=? AND kind=?`, host.Address, q.LID, envelope.KindTask).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	in, err := envelope.Open(original, host.id, host.Address, w.bob.Self())
	if err != nil {
		t.Fatal(err)
	}
	in.ID = protocol.NewID()
	recipient, err := host.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := envelope.Seal(in, w.bob.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.verifyAndStore(tctx(t), legacy); err != nil {
		t.Fatal(err)
	}
	var actual string
	if err = host.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND replica=0`, q.LID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual == q.LID {
		t.Fatalf("fixture needs a physical job distinct from its logical turn: physical=%s logical=%s", actual, q.LID)
	}
	if _, err = host.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	groupHistoryDeliverCommittedFixture(t, host, phone, packet.State.Seq, true)
	if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND lid=? AND kind=?`, phone.Address, q.LID, envelope.KindTask).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	if err = phone.verifyAndStore(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	view := &ExecView{Host: host.Address, State: stateRunning, Attempt: 1}
	action, err := phone.StopFor(q.LID, w.bob.Self().Fingerprint(), view)
	if err != nil || action == nil || action.ID != q.LID {
		t.Fatalf("logical group request cannot reach the Stop decision route: %+v %v", action, err)
	}
	if _, err = host.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,status_due=0 WHERE id=?`, stateAnswered, actual); err != nil {
		t.Fatal(err)
	}
	token := protocol.NewID()
	if _, err = phone.Decide(WithQueuedSend(tctx(t), token), action.Host, action.ID, action.Key, "cancel", stateRunning, action.Attempt, "", ""); err != nil {
		t.Fatal(err)
	}
	decision, err := phone.store.outboxEnvelope(token)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.verifyAndStore(tctx(t), decision); err != nil {
		t.Fatal(err)
	}
	var snapshot envelope.Status
	eventually(t, "logical Stop gets its correlated refusal", func() bool {
		var body string
		err := host.store.db.QueryRow(`SELECT body FROM outbox WHERE sub=? AND json_extract(body,'$.decision')=?`, envelope.SubStatus, token).Scan(&body)
		return err == nil && json.Unmarshal([]byte(body), &snapshot) == nil
	})
	if snapshot.State != stateAnswered || snapshot.Refused == "" || snapshot.Attempt != 1 {
		t.Fatalf("logical request refusal reported a different job: %+v", snapshot)
	}
	if state, err := host.store.jobState(actual); err != nil || state != stateAnswered {
		t.Fatalf("stale logical Stop changed the completed job: %s %v", state, err)
	}
}
