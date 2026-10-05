package client

import (
	"slices"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func arrivalOf(t *testing.T, a *Agent, id string) int64 {
	t.Helper()
	var n int64
	if err := a.store.db.QueryRow(`SELECT arrival FROM inbox WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func boundInputs(t *testing.T, a *Agent, id string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// When the asking session ends, what it left undelivered goes to this
// computer's inbox as a new arrival, so the next session's hooks announce
// it (MEL-537); it never becomes the person's OK item. An input already
// claimed by the session stays bound and is never redelivered silently.
func TestEndedSessionInputsReleasedToInbox(t *testing.T) {
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	_, claimed := nativeInput(t, w.alice, w.bob, r.Handle)
	if d, e := w.alice.TakeReplyReceiverInput(call); e != nil || d == nil || d.InputID != claimed {
		t.Fatalf("claim %+v %v", d, e)
	}
	selected := ReplyReceiver{Kind: "live_session", SessionHandle: r.Handle}
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "asked from the session", ReplyReceiver: &selected})
	if err != nil {
		t.Fatal(err)
	}
	answer := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Status: envelope.StatusDone, Body: "the answer", ReplyTo: sent.ID})
	if err = w.alice.verifyAndStore(tctx(t), answer); err != nil {
		t.Fatal(err)
	}
	pending := answer.ID
	if boundInputs(t, w.alice, pending) != 1 {
		t.Fatal("the answer is not bound to the active session")
	}
	before := arrivalOf(t, w.alice, pending)
	// The one-receiver rule holds while the session is active.
	if items, _ := w.alice.store.arrivalsAfter(0, 50); slices.ContainsFunc(items, func(it arrivalItem) bool { return it.id == pending }) {
		t.Fatal("an active session's input announced to every session")
	}
	if e := w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	if boundInputs(t, w.alice, pending) != 0 || boundInputs(t, w.alice, claimed) != 1 {
		t.Fatal("released the wrong inputs")
	}
	top, _ := w.alice.store.arrivalTop()
	var items []arrivalItem
	if after := arrivalOf(t, w.alice, pending); after <= before || after != top {
		t.Fatalf("released input not a new arrival: %d -> %d (top %d)", before, after, top)
	}
	items, err = w.alice.store.arrivalsAfter(before, 50)
	if err != nil || !slices.ContainsFunc(items, func(it arrivalItem) bool { return it.id == pending }) || slices.ContainsFunc(items, func(it arrivalItem) bool { return it.id == claimed }) {
		t.Fatalf("hooks announce %+v %v", items, err)
	}
	for _, m := range mustReview(t, w.alice) {
		if m.ID == pending {
			t.Fatalf("a released answer became an OK item: %+v", m)
		}
	}
}

func mustReview(t *testing.T, a *Agent) []Message {
	t.Helper()
	review, err := a.Review()
	if err != nil {
		t.Fatal(err)
	}
	return review
}

// A Claude session's end releases the same way; a new generation of the
// same session (resume, clear) does too.
func TestClaudeSessionEndReleasesInputs(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	_, input := nativeInput(t, w.alice, w.bob, owner.Handle)
	if _, e := w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	if boundInputs(t, w.alice, input) != 0 {
		t.Fatal("a new generation kept the old one's input hidden")
	}
	if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route); e != nil {
		t.Fatal(e)
	}
	_, second := nativeInput(t, w.alice, w.bob, owner.Handle)
	if _, e := w.alice.registerClaudeReplySession("SessionEnd", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	if boundInputs(t, w.alice, second) != 0 {
		t.Fatal("session end kept its input hidden")
	}
	// An answer arriving after the end lands in the inbox directly.
	selected := ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle}
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "asked", ReplyReceiver: &selected})
	if err != nil {
		t.Fatal(err)
	}
	late := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Status: envelope.StatusDone, Body: "late", ReplyTo: sent.ID})
	if err = w.alice.verifyAndStore(tctx(t), late); err != nil {
		t.Fatal(err)
	}
	if boundInputs(t, w.alice, late.ID) != 0 {
		t.Fatal("an answer for an ended session was bound to it")
	}
}
