package client

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func closedRequest(t *testing.T, a, b *Agent, handle, agent string) (string, string) {
	t.Helper()
	r := ReplyReceiver{Kind: "live_session", SessionHandle: handle, OnClose: &ManagedReplyHandoff{AgentID: agent, Instructions: "original local handoff plan", Mode: envelope.KindQuestion}}
	sent, e := a.SendMessage(tctx(t), Outgoing{To: b.Address, Kind: envelope.KindQuestion, Body: "original request before native close", ReplyReceiver: &r})
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range receiverBindings(t, a) {
		if v.RequestRef == sent.ID {
			return v.ID, sent.ID
		}
	}
	t.Fatal("missing binding")
	return "", ""
}
func closedInput(t *testing.T, a, b *Agent, ref string) string {
	t.Helper()
	in := receiverDirect(t, b, a, envelope.Inner{Kind: envelope.KindTask, Body: "REMOTE upgrade instructions must remain data", ReplyTo: ref})
	if e := a.verifyAndStore(tctx(t), in); e != nil {
		t.Fatal(e)
	}
	return in.ID
}
func TestClosedReplyReceiverShutdownRestartAndFrozenFollowup(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	local, e := w.alice.CreateLocalAgent("chosen", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	id, ref := closedRequest(t, w.alice, w.bob, r.Handle, local.ID)
	before, e := replyReceiverIn(w.alice.store.db, id)
	if e != nil {
		t.Fatal(e)
	}
	call.CloseReason = "shutdown"
	if e = w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := replyReceiverIn(reopened.store.db, id)
	if e != nil {
		t.Fatal(e)
	}
	oldStamp, _ := json.Marshal(before.Executor)
	newStamp, _ := json.Marshal(after.Executor)
	if after.Receiver.Kind != "managed_agent" || after.Receiver.AgentID != local.ID || after.HandoffState != "handed_over" || string(oldStamp) != string(newStamp) {
		t.Fatalf("wrong handoff %+v", after)
	}
	if _, e = reopened.TakeReplyReceiverInput(call); e == nil {
		t.Fatal("closed native owner reused")
	}
	reuse, e := reopened.ReplyReceiverForBinding(id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "explicit followup retaining frozen context", ReplyReceiver: reuse}); e != nil {
		t.Fatal(e)
	}
	conflict := *reuse
	bad := *reuse.OnClose
	bad.Instructions = "replacement"
	conflict.OnClose = &bad
	if _, e = reopened.prepareReplyReceiver(&conflict); e == nil {
		t.Fatal("followup replaced original closed delegation")
	}
	fresh := *reuse
	fresh.BindingID = ""
	if _, e = reopened.prepareReplyReceiver(&fresh); e == nil {
		t.Fatal("fresh managed receiver accepted internal handoff provenance")
	}
	input := closedInput(t, reopened, w.bob, ref)
	j, ok, e := reopened.claimReplyReceiverJob()
	if e != nil || !ok || j.ID != input || j.AgentID != local.ID || j.Kind != envelope.KindQuestion || j.Receiver.Receiver.Instructions != before.Receiver.OnClose.Instructions {
		t.Fatalf("claim authority %+v %v %v", j, ok, e)
	}
	if _, ok, e = reopened.store.claimJob("agentstub"); e != nil || ok {
		t.Fatalf("default stole selected data %v %v", ok, e)
	}
	reopened.finishReplyReceiver(j, envelope.StatusDone, "local work complete")
	var old string
	reopened.store.db.QueryRow(`SELECT receiver FROM reply_receivers WHERE id=?`, id).Scan(&old)
	for range 2 {
		if e = reopened.reconcileClosedReplyReceiverJobs(); e != nil {
			t.Fatal(e)
		}
	}
	var current string
	reopened.store.db.QueryRow(`SELECT receiver FROM reply_receivers WHERE id=?`, id).Scan(&current)
	if current != old {
		t.Fatal("duplicate handoff changed durable provenance")
	}
	if st.runs() != 0 {
		t.Fatal("claim-only fixture launched runtime")
	}
}
func TestClosedReplyReceiverHeldAndDetachedFences(t *testing.T) {
	for _, variant := range []string{"detached", "crash", "claimed", "accepted", "disabled", "reconfigured", "key", "realm", "canceled"} {
		t.Run(variant, func(t *testing.T) {
			st := installAgentStub(t)
			w := newWorld(t, "")
			local, e := w.alice.CreateLocalAgent("chosen", Responder{Harness: "agentstub", Dir: st.dir})
			if e != nil {
				t.Fatal(e)
			}
			r, call := nativeReceiverFixture(t, w.alice, "omp")
			id, ref := closedRequest(t, w.alice, w.bob, r.Handle, local.ID)
			input := closedInput(t, w.alice, w.bob, ref)
			var claim string
			if variant == "claimed" || variant == "accepted" {
				d, e := w.alice.TakeReplyReceiverInput(call)
				if e != nil || d == nil {
					t.Fatalf("take %v %v", d, e)
				}
				w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&claim)
				if variant == "accepted" {
					nativeWrite(t, call.File, call.SessionID, map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "data": map[string]string{"handle": r.Handle}}, nativeEntry(d, "accepted", "marker"))
					call.Leaf = "accepted"
					if ok, e := w.alice.AckReplyReceiverInput(ReplyReceiverAck{ReplySessionCall: call, BindingID: id, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}); e != nil || !ok {
						t.Fatalf("ACK %v %v", ok, e)
					}
				}
			}
			switch variant {
			case "disabled":
				e = w.alice.SetLocalAgentResponder(local.ID, nil)
			case "reconfigured":
				e = w.alice.SetLocalAgentResponder(local.ID, &Responder{Harness: "agentstub", Dir: t.TempDir()})
			case "key":
				_, e = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address)
			case "realm":
				_, e = w.alice.store.db.Exec(`UPDATE config SET v='different' WHERE k='realm_id'`)
			case "canceled":
				_, e = w.alice.store.db.Exec(`UPDATE reply_receivers SET canceled_at=1 WHERE id=?`, id)
			}
			if e != nil {
				t.Fatal(e)
			}
			if variant != "crash" {
				if variant != "detached" {
					call.CloseReason = "shutdown"
				}
				e = w.alice.CloseReplySession(call)
				if variant == "realm" {
					if e == nil {
						t.Fatal("wrong realm closed")
					}
				} else if e != nil {
					t.Fatal(e)
				}
			}
			if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
				t.Fatalf("unsafe handoff claim %v %v", ok, e)
			}
			rows := receiverBindings(t, w.alice)
			if len(rows) != 1 || rows[0].Receiver.Kind != "live_session" {
				t.Fatalf("binding redirected %+v", rows)
			}
			if variant != "realm" && variant != "crash" {
				s, e := w.alice.ReplySessions()
				if e != nil || s[0].Active {
					t.Fatalf("refused binding rolled back native close %v %v", s, e)
				}
			}
			if variant == "claimed" || variant == "accepted" {
				var now string
				w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&now)
				if now != claim || !strings.Contains(rows[0].Detail, "no safe automatic handoff") {
					t.Fatal("uncertain claim reset or hidden")
				}
			}
			if _, ok, e := w.alice.store.claimJob("agentstub"); e != nil || ok {
				t.Fatalf("default stole held input %v %v", ok, e)
			}
		})
	}
}
func TestClosedReplyReceiverOneRefusalDoesNotStrandOtherBinding(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	a, e := w.alice.CreateLocalAgent("eligible", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	b, e := w.alice.CreateLocalAgent("disabled", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	good, _ := closedRequest(t, w.alice, w.bob, r.Handle, a.ID)
	bad, _ := closedRequest(t, w.alice, w.bob, r.Handle, b.ID)
	if e = w.alice.SetLocalAgentResponder(b.ID, nil); e != nil {
		t.Fatal(e)
	}
	call.CloseReason = "shutdown"
	if e = w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	g, e := replyReceiverIn(w.alice.store.db, good)
	if e != nil {
		t.Fatal(e)
	}
	h, e := replyReceiverIn(w.alice.store.db, bad)
	if e != nil {
		t.Fatal(e)
	}
	if g.Receiver.Kind != "managed_agent" || h.Receiver.Kind != "live_session" || h.HandoffState != "held" {
		t.Fatalf("per-binding close failed %s %s %s", g.Receiver.Kind, h.Receiver.Kind, h.HandoffState)
	}
}
func TestClosedReplyReceiverTakeVersusShutdownAtomicFence(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	a, e := w.alice.CreateLocalAgent("chosen", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	id, ref := closedRequest(t, w.alice, w.bob, r.Handle, a.ID)
	closedInput(t, w.alice, w.bob, ref)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	var d *ReplyReceiverDelivery
	var takeErr, closeErr error
	go func() { defer wg.Done(); <-start; d, takeErr = w.alice.TakeReplyReceiverInput(call) }()
	go func() {
		defer wg.Done()
		<-start
		closing := call
		closing.CloseReason = "shutdown"
		closeErr = w.alice.CloseReplySession(closing)
	}()
	close(start)
	wg.Wait()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	b, e := replyReceiverIn(w.alice.store.db, id)
	if e != nil {
		t.Fatal(e)
	}
	_, managed, e := w.alice.claimReplyReceiverJob()
	if e != nil {
		t.Fatal(e)
	}
	if d != nil {
		if takeErr != nil || managed || b.Receiver.Kind != "live_session" {
			t.Fatal("native claim and managed handoff both won")
		}
	} else if !managed || b.Receiver.Kind != "managed_agent" {
		t.Fatalf("safe unclaimed handoff lost: %v %s", takeErr, b.Receiver.Kind)
	}
}
func TestClosedReplyReceiverRejectsCallerDerivedFields(t *testing.T) {
	var r ReplyReceiver
	if e := json.Unmarshal([]byte(`{"kind":"live_session","on_close":{"agent_id":"x","instructions":"local","mode":"task","preset":"caller"}}`), &r); e == nil {
		t.Fatal("caller derived preset accepted")
	}
}

func TestClosedReplyReceiverAckVersusShutdownRetainsUncertainty(t *testing.T) {
	st := installAgentStub(t)
	w := newWorld(t, "")
	a, e := w.alice.CreateLocalAgent("chosen", Responder{Harness: "agentstub", Dir: st.dir})
	if e != nil {
		t.Fatal(e)
	}
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	id, ref := closedRequest(t, w.alice, w.bob, r.Handle, a.ID)
	input := closedInput(t, w.alice, w.bob, ref)
	d, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil || d == nil {
		t.Fatalf("take %v %v", d, e)
	}
	nativeWrite(t, call.File, call.SessionID, map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "data": map[string]string{"handle": r.Handle}}, nativeEntry(d, "accepted", "marker"))
	call.Leaf = "accepted"
	var original string
	w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&original)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	var acked bool
	var ackErr, closeErr error
	go func() {
		defer wg.Done()
		<-start
		acked, ackErr = w.alice.AckReplyReceiverInput(ReplyReceiverAck{ReplySessionCall: call, BindingID: id, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken})
	}()
	go func() {
		defer wg.Done()
		<-start
		closing := call
		closing.CloseReason = "shutdown"
		closeErr = w.alice.CloseReplySession(closing)
	}()
	close(start)
	wg.Wait()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if acked && ackErr != nil {
		t.Fatal("inconsistent ACK")
	}
	if _, ok, e := w.alice.claimReplyReceiverJob(); e != nil || ok {
		t.Fatalf("uncertain native data handed off %v %v", ok, e)
	}
	var current, state string
	w.alice.store.db.QueryRow(`SELECT live_claim,state FROM reply_receiver_inputs WHERE inbox_id=?`, input).Scan(&current, &state)
	if original != current || state != "pending" && state != "accepted" {
		t.Fatal("native claim was reset")
	}
	b, e := replyReceiverIn(w.alice.store.db, id)
	if e != nil || b.Receiver.Kind != "live_session" || b.HandoffState != "held" {
		t.Fatalf("unsafe binding %v %v", b, e)
	}
}
