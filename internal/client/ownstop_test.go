package client

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOwnHumanStopRunningProcess(t *testing.T) {
	st := installStub(t, "sleep")
	w := newWorld(t, "")
	if _, err := w.alice.CreatePerson(tctx(t), "Owner"); err != nil {
		t.Fatal(err)
	}
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	link := func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) }
	phone := linkedVia(t, w.alice, "phone", link)
	tablet := linkedVia(t, w.alice, "tablet", link)
	agentHost := linkedVia(t, w.alice, "worker", w.alice.ApproveAgentLink)
	q, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "isolated long request"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, q.ID, stateRunning)
	eventually(t, "child started", func() bool { _, e := os.Stat(st.log + ".child"); return e == nil })
	for range 3 {
		if _, err = w.alice.deviceHistoryPage(tablet.Self()); err != nil {
			t.Fatal(err)
		}
	}
	deliverDeviceHistory(t, w.alice, tablet, envelope.SubDeviceHistory)
	view := &ExecView{Host: w.alice.Address, State: stateRunning, Attempt: 1}
	descriptor, err := tablet.StopFor(q.ID, "", view)
	if err != nil || descriptor == nil || descriptor.Key != phone.Self().Fingerprint() {
		t.Fatalf("linked stop descriptor %+v %v", descriptor, err)
	}
	decision := func(sender *Agent, id, key string, attempt int64) (envelope.Envelope, envelope.Inner) {
		t.Helper()
		body, _ := json.Marshal(envelope.Decision{Action: "cancel", Expect: stateRunning, Attempt: attempt})
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: sender.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDecision, Body: string(body), Ref: &envelope.Ref{ID: id, Fingerprint: key}}
		r, _ := w.alice.Self().Recipient()
		env, e := envelope.Seal(in, sender.id.Sign, r)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.alice.admitDecision(tctx(t), env, in, sender.Self()); e != nil {
			t.Fatal(e)
		}
		return env, in
	}
	for _, tc := range []struct {
		sender  *Agent
		id, key string
		attempt int64
	}{
		{w.bob, q.ID, descriptor.Key, 1}, {agentHost, q.ID, descriptor.Key, 1}, {tablet, q.ID, w.bob.Self().Fingerprint(), 1}, {tablet, q.ID, descriptor.Key, 2}, {tablet, protocol.NewID(), descriptor.Key, 1},
	} {
		decision(tc.sender, tc.id, tc.key, tc.attempt)
		if jobState(t, w.alice, q.ID) != stateRunning {
			t.Fatal("wrong authority or attempt stopped process")
		}
	}
	// Use the real queued sender, stream and existing safe process termination.
	token := protocol.NewID()
	if _, err = tablet.Decide(WithQueuedSend(tctx(t), token), descriptor.Host, descriptor.ID, descriptor.Key, "cancel", stateRunning, 1, "", ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, q.ID, stateCancelled)
	pidText, _ := os.ReadFile(st.log + ".child")
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	eventually(t, "only bound process exited", func() bool { p, e := os.FindProcess(pid); return e != nil || p.Signal(syscall.Signal(0)) != nil })
	if st.count() != 1 {
		t.Fatal("request rerun")
	}
	if _, ok := findReply(phone, q.ID); ok {
		t.Fatal("cancelled request emitted answer")
	}
	// A completed job and a new attempt cannot be affected by an old decision.
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=2 WHERE id=?`, stateAnswered, q.ID); err != nil {
		t.Fatal(err)
	}
	decision(tablet, q.ID, descriptor.Key, 1)
	if jobState(t, w.alice, q.ID) != stateAnswered {
		t.Fatal("late cancellation changed completed outcome")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=2 WHERE id=?`, stateRunning, q.ID); err != nil {
		t.Fatal(err)
	}
	env, in := decision(tablet, q.ID, descriptor.Key, 1)
	if err = w.alice.admitDecision(tctx(t), env, in, tablet.Self()); err != nil {
		t.Fatal(err)
	}
	if jobState(t, w.alice, q.ID) != stateRunning {
		t.Fatal("replay reached another attempt")
	}
	if err = w.alice.RemoveDevice(tctx(t), tablet.Address); err != nil {
		t.Fatal(err)
	}
	decision(tablet, q.ID, descriptor.Key, 2)
	if jobState(t, w.alice, q.ID) != stateRunning {
		t.Fatal("removed human stopped request")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, q.ID); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := statusOf(stateCancelReq); state != "running" {
		t.Fatal("requested cancellation claims process stopped")
	}
}

// Exercise the actual serialized decision and durable projection. Report is
// omitted for an owner Stop, so json_extract returns NULL, not an empty string.
func TestOwnHumanStopSurvivesReopen(t *testing.T) {
	for _, hostState := range []string{stateRunning, stateAnswered} {
		t.Run(hostState, func(t *testing.T) {
			w := newWorld(t, "")
			if _, err := w.alice.CreatePerson(tctx(t), "Owner"); err != nil {
				t.Fatal(err)
			}
			stop := runAgent(t, w.alice)
			phone, awaited, _ := linkPhone(t, w.alice, "phone")
			if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
				t.Fatal(err)
			}
			if out := <-awaited; out.err != nil {
				t.Fatal(out.err)
			}
			// The link's older session must not stand in for the daemon
			// being stopped: its own signed record must exist first.
			stopPhone := runPublished(t, phone)
			eventually(t, "phone advertises decision status support", func() bool {
				features, err := w.alice.relayFeatures(tctx(t))
				if err != nil {
					return false
				}
				ok, _ := w.alice.capSupport(tctx(t), phone.Address, phone.Self(), features, protocol.CapHeadless)
				return ok
			})
			stopPhone()
			q, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "request to stop"})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "original request stored", func() bool { return inboxHas(t, w.alice, q.ID) })
			stop()
			if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1 WHERE id=?`, hostState, q.ID); err != nil {
				t.Fatal(err)
			}
			view := &ExecView{Host: w.alice.Address, State: stateRunning, Attempt: 1}
			key, token := phone.Self().Fingerprint(), protocol.NewID()
			queue := func() {
				t.Helper()
				if _, err := phone.Decide(WithQueuedSend(tctx(t), token), w.alice.Address, q.ID, key, "cancel", stateRunning, 1, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			pending := func() {
				t.Helper()
				action, err := phone.StopFor(q.ID, key, view)
				if err != nil || action == nil || !action.Pending || action.Result != nil {
					t.Fatalf("queued Stop lost: %+v %v", action, err)
				}
			}
			queue()
			pending()
			home := phone.home
			phone.Close()
			phone, err = Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer phone.Close()
			pending()
			queue() // The same send ID remains one control after a restart/retry.
			var n int
			if err = phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND ref_id=?`, envelope.SubDecision, q.ID).Scan(&n); err != nil || n != 1 {
				t.Fatalf("duplicate Stop controls: %d %v", n, err)
			}
			deliverDeviceHistory(t, phone, w.alice, envelope.SubDecision)
			eventually(t, "authenticated host decision response", func() bool {
				var n int
				err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND json_extract(body,'$.decision')=?`, envelope.SubStatus, token).Scan(&n)
				return err == nil && n == 1
			})
			deliverDeviceHistory(t, w.alice, phone, envelope.SubStatus)
			action, err := phone.StopFor(q.ID, key, view)
			if err != nil || action == nil || action.Pending || action.Result == nil || action.Result.Decision != token {
				t.Fatalf("host result missing: %+v %v", action, err)
			}
			if hostState == stateRunning {
				if action.Result.State != "running" || action.Result.Refused != "" {
					t.Fatalf("queued cancellation claimed terminal or refused: %+v", action.Result)
				}
			} else if action.Result.Refused == "" || jobState(t, w.alice, q.ID) != stateAnswered {
				t.Fatalf("completed-request refusal lost: %+v", action.Result)
			}
		})
	}
}
