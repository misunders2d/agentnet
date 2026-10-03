package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// takeAndAccept takes the selected session's next input and records its
// native acceptance; it must be want.
func takeAndAccept(t *testing.T, a *Agent, owner ReplySessionOwner, call ReplySessionCall, want, request string) ReplySessionCall {
	t.Helper()
	d, err := a.TakeReplyReceiverInput(call)
	if err != nil || d == nil || d.InputID != want || d.RequestBody != request {
		t.Fatalf("selected take at %s: %+v %v", a.Address, d, err)
	}
	marker := map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": owner.Handle}}
	nativeWrite(t, call.File, call.SessionID, marker, nativeEntry(d, "accepted", "marker"))
	ack := ReplyReceiverAck{ReplySessionCall: call, BindingID: d.BindingID, InputID: d.InputID, ClaimID: d.ClaimID, InputToken: d.InputToken}
	ack.Leaf = "accepted"
	for range 2 {
		if ok, err := a.AckReplyReceiverInput(ack); err != nil || !ok {
			t.Fatalf("native acceptance at %s: %v %v", a.Address, ok, err)
		}
	}
	call.Leaf = "accepted"
	if next, err := a.TakeReplyReceiverInput(call); err != nil || next != nil {
		t.Fatalf("accepted input offered again at %s: %+v %v", a.Address, next, err)
	}
	return call
}

// With guests present, a member or guest asks an added assistant from its
// own registered session: the request still reaches the whole captured
// audience as a person's turn, only the host's exact copy is bound, and the
// assistant's answer returns to that session once. Another key, another
// session, the default responder or a replay never consume it; a
// cross-device receiver is refused; asking with a receiver grants the guest
// no permission at the host.
func TestHumanRequestLocalReplyReceiver(t *testing.T) {
	w, carol, conv, _, stub, ap, hp := guestAssistant(t)
	owner, call := nativeReceiverFixture(t, w.alice, "pi")
	const asked = "member asks from a selected session"
	sent, err := w.alice.AskAgentWithReceiver(tctx(t), ap.PID, envelope.KindQuestion, asked, &ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle})
	if err != nil {
		t.Fatal(err)
	}
	var bound, humans int
	var boundTo, boundFP string
	w.alice.store.db.QueryRow(`SELECT count(*), coalesce(max(recipient),''), coalesce(max(recipient_fp),'') FROM outbox WHERE lid=? AND reply_receiver IS NOT NULL`, sent.LID).Scan(&bound, &boundTo, &boundFP)
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND human IS NOT NULL`, sent.LID).Scan(&humans)
	if bound != 1 || boundTo != w.bob.Address || boundFP != w.bob.Self().Fingerprint() || humans < 2 {
		t.Fatalf("bound copies %d (%s %s), audience copies %d", bound, boundTo, boundFP, humans)
	}
	eventually(t, "request seen by the guest", func() bool { return humanBodyCount(t, carol, conv, asked) == 1 })
	var origin string
	carol.store.db.QueryRow(`SELECT coalesce(origin,'') FROM inbox WHERE lid=?`, sent.LID).Scan(&origin)
	if origin != envelope.OriginUI {
		t.Fatalf("request origin at the guest %q", origin)
	}
	answer := replyAt(t, w.alice, conv, sent.LID)
	replyAt(t, carol, conv, sent.LID)
	var answerHuman string // the selected input is the stored answer, its captured audience kept
	w.alice.store.db.QueryRow(`SELECT coalesce(human,'') FROM inbox WHERE id=?`, answer.ID).Scan(&answerHuman)
	if !strings.Contains(answerHuman, hp.PID) {
		t.Fatalf("selected input's captured audience %q", answerHuman)
	}
	if b := groupReceiverBound(t, w.alice, sent.LID); len(b.Inputs) != 1 || b.Inputs[0].ID != answer.ID {
		t.Fatalf("answer not bound once: %+v", b)
	}
	// Another session of the same device is offered nothing.
	_, other := nativeReceiverFixture(t, w.alice, "pi")
	if d, err := w.alice.TakeReplyReceiverInput(other); err != nil || d != nil {
		t.Fatalf("another session took the input: %+v %v", d, err)
	}
	call = takeAndAccept(t, w.alice, owner, call, answer.ID, asked)
	if _, ok, err := w.alice.store.claimJob("agentstub"); err != nil || ok {
		t.Fatalf("default responder took the selected answer: %v %v", ok, err)
	}

	// A replay of the host's answer, or an answer under another key (the
	// guest's), adds no input.
	var raw, human string
	if err := w.bob.store.db.QueryRow(`SELECT envelope, human FROM outbox WHERE recipient=? AND reply_to=? AND kind=?`, w.alice.Address, sent.LID, envelope.KindAnswer).Scan(&raw, &human); err != nil {
		t.Fatal(err)
	}
	var genuine envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &genuine); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.accept(tctx(t), genuine); err != nil {
		t.Fatal(err)
	}
	var h envelope.HumanTurn
	if err := json.Unmarshal([]byte(human), &h); err != nil {
		t.Fatal(err)
	}
	_, root, _, _ := w.alice.store.conversation(conv)
	recipient, _ := w.alice.Self().Recipient()
	forged := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: carol.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer,
		Body: "forged answer", ReplyTo: sent.LID, Conv: conv, LID: protocol.NewID(), Root: root, PID: ap.PID, Human: &h, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}
	env, err := envelope.Seal(forged, carol.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, w.alice, "id = ?", forged.ID) != 0 {
		t.Fatal("answer under the guest's key stored")
	}
	if b := groupReceiverBound(t, w.alice, sent.LID); len(b.Inputs) != 1 {
		t.Fatalf("replay or another key added input: %+v", b)
	}
	if d, err := w.alice.TakeReplyReceiverInput(call); err != nil || d != nil {
		t.Fatalf("replayed input offered: %+v %v", d, err)
	}

	// A cross-device receiver is refused; nothing is stored.
	h2, err := w.alice.humanPlan(tctx(t), conv, "")
	if err != nil || h2 == nil {
		t.Fatalf("audience %+v %v", h2, err)
	}
	r, rawRoot, _, _ := w.alice.store.conversation(conv)
	remote := &replyBinding{receiver: ReplyReceiver{Kind: "human", Host: &ReplyReceiverHost{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}}}
	target := &envelope.Target{Address: ap.Host.Address, Fingerprint: ap.Host.Fingerprint, AgentID: ap.AgentID}
	if _, err := w.alice.sendHumanTurn(tctx(t), r, rawRoot, ConvOutgoing{Kind: envelope.KindQuestion, Body: "cross-device receiver", PID: ap.PID, Target: target}, h2, remote); err == nil || !strings.Contains(err.Error(), "own only") {
		t.Fatalf("cross-device receiver: %v", err)
	}
	if n := humanBodyCount(t, w.alice, conv, "cross-device receiver"); n != 0 {
		t.Fatal("refused request stored")
	}

	// The guest asks from her own session: Bob's permissions decide, the
	// answer returns to her session once, never to Alice's.
	gOwner, gCall := nativeReceiverFixture(t, carol, "pi")
	const guestAsked = "guest asks from a selected session"
	runs := stub.runs()
	g, err := carol.AskAgentWithReceiver(tctx(t), ap.PID, envelope.KindQuestion, guestAsked, &ReplyReceiver{Kind: "live_session", SessionHandle: gOwner.Handle})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest question awaits Bob", func() bool { return jobState(t, w.bob, g.LID) == stateAwaiting })
	if stub.runs() != runs {
		t.Fatal("a guest's request with a receiver ran without Bob's decision")
	}
	if err := w.bob.Accept(g.LID); err != nil {
		t.Fatal(err)
	}
	gAnswer := replyAt(t, carol, conv, g.LID)
	replyAt(t, w.alice, conv, g.LID)
	if b := groupReceiverBound(t, carol, g.LID); len(b.Inputs) != 1 || b.Inputs[0].ID != gAnswer.ID {
		t.Fatalf("guest answer not bound once: %+v", b)
	}
	takeAndAccept(t, carol, gOwner, gCall, gAnswer.ID, guestAsked)
	if _, ok, err := carol.store.claimJob("agentstub"); err != nil || ok {
		t.Fatalf("guest default responder took the selected answer: %v %v", ok, err)
	}
	if d, err := w.alice.TakeReplyReceiverInput(call); err != nil || d != nil {
		t.Fatalf("member session took the guest's answer: %+v %v", d, err)
	}
}
