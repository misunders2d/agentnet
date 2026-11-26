package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestEditQueuedRequestUsesNewestSignedTextOnce(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	if err := a.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	key := w.alice.Self().Fingerprint()
	original := envelope.Inner{
		V: envelope.Version, ID: protocol.NewID(), From: w.alice.Address,
		To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "Original instructions",
	}
	if err := a.store.addInbox(original, key); err != nil {
		t.Fatal(err)
	}
	edit := func(rev int64, text string) envelope.Inner {
		t.Helper()
		body, err := json.Marshal(envelope.Revision{Rev: rev, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return envelope.Inner{
			V: envelope.Version3, ID: protocol.NewID(), From: original.From, To: original.To,
			TS: original.TS, Kind: envelope.KindMessage, Sub: envelope.SubRevision, Body: string(body),
			Ref: &envelope.Ref{ID: original.ID, Fingerprint: key},
		}
	}
	latest := edit(2, "Most recent instructions")
	older := edit(1, "Earlier instructions")
	for _, update := range []envelope.Inner{latest, older} {
		if err := a.store.addControlInbox(update, key, false); err != nil {
			t.Fatal(err)
		}
	}
	started, found, err := a.store.claimJob("stub")
	if err != nil || !found || started.ID != original.ID || started.Body != "Most recent instructions" {
		t.Fatalf("first claim did not use the latest edit: %+v %t %v", started, found, err)
	}
	if err := a.store.addControlInbox(latest, key, false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.addControlInbox(edit(1, "Out of order"), key, false); err != nil {
		t.Fatal(err)
	}
	var originalText string
	var corrections int
	if err := a.store.db.QueryRow(`SELECT body,(SELECT count(*) FROM inbox WHERE sub=?) FROM inbox WHERE id=?`,
		requestEditJob, original.ID).Scan(&originalText, &corrections); err != nil || originalText != original.Body || corrections != 0 {
		t.Fatalf("original changed or an already used edit was scheduled twice: %q %d %v", originalText, corrections, err)
	}
}

func TestEditRemoteGroupRequestSteersOrFollowsUp(t *testing.T) {
	for _, mode := range []string{"native", "finished", "finished-before-handover", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			stub := installAgentStub(t)
			w, _, packet, _ := groupTurnsFixture(t)
			a := w.bob
			p := p6Member(t, w.alice, a, packet.State.Conv)
			if err := a.Approve(w.alice.Address); err != nil {
				t.Fatal(err)
			}
			id := protocol.NewID()
			sent, retained, err := w.alice.AskAgentInTopicQueued(tctx(t), id, p.PID, envelope.KindQuestion, "Compare the two colors", "", nil, protocol.NewID(), 0)
			if err != nil || retained != nil || sent.LID != id {
				t.Fatalf("send original ask: %+v %+v %v", sent, retained, err)
			}
			waitState(t, a, id, stateAgentWaiting)
			parent, found, _, _, err := a.store.claimAgentPage("agentstub", a.Address, a.Self().Fingerprint(), 0, agentPage)
			if err != nil || !found || parent.ID != id {
				t.Fatalf("claim original: %+v %t %v", parent, found, err)
			}
			parent.nativeAttempt = 1
			responder := &Responder{Harness: "agentstub", Dir: stub.dir, Timeout: time.Minute}
			wantRuns := 1
			if mode == "finished" {
				stub.mode("echo")
				t.Setenv("AGENT_ANSWER", "Earlier result: blue had the clearer contrast")
				a.runJob(tctx(t), parent, responder, nil)
				waitState(t, a, id, stateAnswered)
				if stub.runs() != 1 {
					t.Fatal("original did not finish through the worker")
				}
				wantRuns = 2
			} else if mode == "stopped" {
				if err := a.endJob(id, stateCancelled, "", 1); err != nil {
					t.Fatal(err)
				}
			}
			ref := ControlRef{Conv: packet.State.Conv, ID: id, Fingerprint: w.alice.Self().Fingerprint()}
			if _, err = w.alice.Revise(tctx(t), ref, "Write the comparison in English"); err != nil {
				t.Fatal(err)
			}
			eventually(t, "signed edit admitted on the agent host", func() bool {
				ctrl, err := a.controlsOf(ref)
				return err == nil && ctrl.Edited
			})
			if mode == "stopped" {
				var count int
				if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub=? AND reply_to=?`, requestEditJob, id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("an edit restarted stopped work: %d %v", count, err)
				}
				return
			}
			if mode == "native" {
				pi, decoder, _ := piBridgeFixture(t)
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); a.watchNativeFollowups(ctx, parent, pi, nil) }()
				defer func() { cancel(); <-done }()
				input := make(chan map[string]any, 1)
				go func() {
					var request map[string]any
					_ = decoder.Decode(&request)
					input <- request
				}()
				select {
				case request := <-input:
					body, _ := request["message"].(string)
					if strings.Count(body, "Write the comparison in English") != 1 {
						t.Fatal("wrong native correction", request)
					}
					piEvent(pi, map[string]any{"type": "response", "id": request["id"], "command": "steer", "success": true, "data": map[string]string{"disposition": "queued"}})
					piEvent(pi, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": body}})
				case <-time.After(3 * time.Second):
					t.Fatal("edit did not reach native input")
				}
			} else {
				if mode == "finished-before-handover" {
					correction, ok, err := a.claimNativeFollowup(parent)
					if err != nil || !ok || correction.editOf != id {
						t.Fatalf("running edit did not reserve the handover: %+v %t %v", correction, ok, err)
					}
					if err := a.endJob(id, stateAnswered, "", 1); err != nil {
						t.Fatal(err)
					}
					if a.nativeFollowupCurrent(parent, correction.job) || !a.queueEditAfterCompletion(correction) || a.queueEditAfterCompletion(correction) {
						t.Fatal("unsent handover should move to the ordinary queue exactly once")
					}
				}
				child, found, _, _, err := a.store.claimAgentPage("agentstub", a.Address, a.Self().Fingerprint(), 0, agentPage)
				if err != nil || !found || child.editOf != id || child.Body != "Write the comparison in English" {
					t.Fatalf("follow-up was not bound to the original run: %+v %t %v", child, found, err)
				}
				a.runJob(tctx(t), child, responder, nil)
				if stub.runs() != wantRuns {
					var state, detail string
					queryErr := a.store.db.QueryRow(`SELECT state,coalesce(detail,'') FROM inbox WHERE id=?`, child.ID).Scan(&state, &detail)
					t.Fatalf("follow-up ran %d times, state %q, detail %q, lookup %v", stub.runs(), state, detail, queryErr)
				}
				if mode == "finished" && !strings.Contains(stub.last(), "Earlier result: blue had the clearer contrast") {
					t.Fatal("follow-up lost the original result")
				}
			}
			want := "answered"
			if mode == "native" {
				want = "steered"
			}
			eventually(t, "requester sees the exact edit outcome", func() bool {
				controls, err := w.alice.controlsOf(ref)
				return err == nil && controls.EditExecution == want
			})
		})
	}
}
