package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Real signed SendMessage, daemon admission and synthetic harness execution.
// No sender approval, manual accept or inbox injection makes a self send run.
func TestDirectSelfDefaultAndNamedAgent(t *testing.T) {
	st := sessionStub(t)
	for _, named := range []bool{false, true} {
		label := "default"
		if named {
			label = "named"
		}
		t.Run(label, func(t *testing.T) {
			w := newWorld(t, "")
			a := w.alice
			fakeNotify(a)
			persons(t, a)
			setResponder(t, a, "stub", st.dir, time.Minute)
			agentID, harness := "", "stub"
			if named {
				record, err := a.CreateLocalAgent("Selected local", Responder{Harness: "cstyle", Dir: st.dir, Timeout: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				agentID, harness = record.ID, "cstyle"
			}
			baseline := st.count()
			runAgent(t, a)
			waitNamedAgentCaps(t, a)
			var previous string
			for turn := 0; turn < 2; turn++ {
				out := Outgoing{To: a.Address, Kind: envelope.KindQuestion, Body: "local direct request", ReplyTo: previous}
				if named {
					out.Target = &envelope.Target{Address: a.Address, Fingerprint: a.Self().Fingerprint(), AgentID: agentID}
				}
				sent, err := a.SendMessage(tctx(t), out)
				if err != nil {
					t.Fatal(err)
				}
				eventually(t, "self request stored", func() bool { return inboxCount(t, a, "id=?", sent.ID) == 1 })
				state, err := a.store.jobState(sent.ID)
				if err != nil {
					t.Fatal(err)
				}
				if state == stateHeld {
					t.Fatalf("self direct %s request held as an unapproved foreign sender; no permission altered", label)
				}
				var reply Message
				eventually(t, "self selected agent reply", func() bool { var found bool; reply, found = findReply(a, sent.ID); return found })
				if reply.Kind != envelope.KindAnswer || reply.Status != envelope.StatusDone || reply.AgentID != agentID {
					t.Fatalf("reply selected executor: %+v", reply)
				}
				var encoded string
				if err = a.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, sent.ID).Scan(&encoded); err != nil {
					t.Fatal(err)
				}
				var stamp ExecutorStamp
				if err = json.Unmarshal([]byte(encoded), &stamp); err != nil || stamp.AgentID != agentID || stamp.Responder.Harness != harness {
					t.Fatalf("executor %s: %v", encoded, err)
				}
				view, err := a.Conversation(sent.ID, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				requests, answers := 0, 0
				for _, m := range view.Messages {
					if m.ID == sent.ID {
						requests++
					}
					if m.ID == reply.ID {
						answers++
					}
				}
				if requests != 1 || answers != 1 {
					t.Fatalf("visible self request/reply duplicated or absent: %d/%d", requests, answers)
				}
				previous = reply.ID
			}
			if runs := st.count() - baseline; runs != 2 {
				t.Fatalf("self/followup ran %d times", runs)
			}
			if count(t, a, "approvals") != 0 || count(t, a, "person_grants") != 0 {
				t.Fatal("self chat created persistent permissions")
			}
			if r, err := a.Responder(); err != nil || r == nil || r.Harness != "stub" {
				t.Fatalf("named selection changed device default: %+v %v", r, err)
			}
		})
	}
}

func TestDirectSelfExactProvenanceAndReplay(t *testing.T) {
	st := installStub(t, "answer")
	for _, scenario := range []string{"exact", "no local outbox", "different signed envelope", "different saved recipient key", "foreign sender", "held replay", "task"} {
		t.Run(scenario, func(t *testing.T) {
			w := newWorld(t, "")
			a, sender := w.alice, w.alice
			setResponder(t, a, "stub", st.dir, time.Minute)
			if scenario == "foreign sender" {
				sender = w.bob
			}
			in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: sender.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "exact local question"}
			if scenario == "task" {
				in.Kind = envelope.KindTask
			}
			recipient, err := a.Self().Recipient()
			if err != nil {
				t.Fatal(err)
			}
			env, err := envelope.Seal(in, sender.id.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			stash := func(fp string) {
				t.Helper()
				if err := a.store.addOutbox(env, in, "", nil, boundOutgoing{fingerprint: fp}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "no local outbox" && scenario != "held replay" {
				fp := a.Self().Fingerprint()
				if scenario == "different saved recipient key" {
					fp = w.bob.Self().Fingerprint()
				}
				stash(fp)
			}
			if scenario == "different signed envelope" {
				// Same ID, metadata and plaintext, but a different valid ciphertext
				// and signature: header matching alone cannot authorize this copy.
				env, err = envelope.Seal(in, sender.id.Sign, recipient)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := st.count()
			if err := a.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			if scenario == "exact" {
				if state, err := a.store.jobState(in.ID); err != nil || state != stateAccepted {
					t.Fatalf("exact local request not accepted: %s %v", state, err)
				}
				if !a.runNext(tctx(t), nil) {
					t.Fatal("exact local request not claimed")
				}
				if state, err := a.store.jobState(in.ID); err != nil || state != stateAnswered {
					t.Fatalf("exact request not answered: %s %v", state, err)
				}
				if err := a.verifyAndStore(tctx(t), env); err != nil {
					t.Fatal(err)
				}
				if state, _ := a.store.jobState(in.ID); state != stateAnswered || a.runNext(tctx(t), nil) || st.count()-before != 1 {
					t.Fatal("exact replay changed completion or ran again")
				}
			} else {
				want := stateHeld
				if scenario == "task" {
					want = stateAwaiting
				}
				if scenario == "held replay" {
					stash(a.Self().Fingerprint())
					if err := a.verifyAndStore(tctx(t), env); err != nil {
						t.Fatal(err)
					}
				}
				if state, err := a.store.jobState(in.ID); err != nil || state != want {
					t.Fatalf("invalid provenance became eligible: %s %v", state, err)
				}
				if a.runNext(tctx(t), nil) || st.count() != before {
					t.Fatal("invalid provenance ran a worker")
				}
			}
			if count(t, a, "approvals") != 0 || count(t, a, "person_grants") != 0 {
				t.Fatal("local provenance mutated permissions")
			}
		})
	}
}
