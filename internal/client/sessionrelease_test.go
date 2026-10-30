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
// it (MEL-537); an answer never becomes the person's OK item. A question or
// task sent back in reply goes to the person's Review instead, claimed by
// the session or not, and nothing runs it (owner, October 29, 2026;
// MEL-580): bound to the ended session, its topic said Waiting for good.
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
	// A question bob sends back in reply: bound too while the session lives.
	back := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, Body: "which branch?", ReplyTo: sent.ID})
	if err = w.alice.verifyAndStore(tctx(t), back); err != nil {
		t.Fatal(err)
	}
	if boundInputs(t, w.alice, back.ID) != 1 {
		t.Fatal("the question in reply is not bound to the active session")
	}
	before := arrivalOf(t, w.alice, pending)
	// The one-receiver rule holds while the session is active.
	if items, _ := w.alice.store.arrivalsAfter(0, 50); slices.ContainsFunc(items, func(it arrivalItem) bool { return it.id == pending }) {
		t.Fatal("an active session's input announced to every session")
	}
	if e := w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	if boundInputs(t, w.alice, pending) != 0 || boundInputs(t, w.alice, claimed) != 0 || boundInputs(t, w.alice, back.ID) != 0 {
		t.Fatal("inputs left bound to the ended session")
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
	inReview := func(id string) bool {
		return slices.ContainsFunc(mustReview(t, w.alice), func(m Message) bool { return m.ID == id })
	}
	if inReview(pending) {
		t.Fatal("an answer to the ended session became an OK item")
	}
	for _, id := range []string{back.ID, claimed} {
		if !inReview(id) || jobDetail(t, w.alice, id) != endedSessionRequestDetail {
			t.Fatalf("question %s for the ended session is not in Review with its reason: %q", id, jobDetail(t, w.alice, id))
		}
	}
	// One arriving after the end goes to Review the same way; nothing runs it.
	late := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: "push the fix", ReplyTo: sent.ID})
	if err = w.alice.verifyAndStore(tctx(t), late); err != nil {
		t.Fatal(err)
	}
	if boundInputs(t, w.alice, late.ID) != 0 || !inReview(late.ID) {
		t.Fatal("a task in reply to an ended session's request is not in Review")
	}
	if s, _ := w.alice.store.jobState(late.ID); s != stateNeedHuman {
		t.Fatalf("a task for an ended session: %s", s)
	}
}

func jobDetail(t *testing.T, a *Agent, id string) string {
	t.Helper()
	var d string
	if err := a.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Questions and tasks an earlier program left bound to a session that had
// already ended go to Review when the daemon starts (MEL-580).
func TestDaemonStartReviewsEndedSessionRequests(t *testing.T) {
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	_, back := nativeInput(t, w.alice, w.bob, r.Handle)
	if e := w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	// As an earlier program left it: still bound, waiting.
	var binding string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM reply_receivers WHERE json_extract(receiver,'$.session_handle')=?`, r.Handle).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.store.db.Exec(`INSERT INTO reply_receiver_inputs(binding,inbox_id) VALUES(?,?)`, binding, back); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state='pending', detail=NULL WHERE id=?`, back); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.reviewEndedSessionRequests(); err != nil {
		t.Fatal(err)
	}
	if boundInputs(t, w.alice, back) != 0 || jobDetail(t, w.alice, back) != endedSessionRequestDetail {
		t.Fatal("a question for an already ended session stayed bound")
	}
	if s, _ := w.alice.store.jobState(back); s != stateNeedHuman {
		t.Fatalf("state %s", s)
	}
	// It never ran: no attempt has a clarification to answer, so Reply is not
	// offered (ContinueRequest refuses attempt 0); Mark as handled closes it.
	if c, err := w.alice.ContinuationFor(back, "", nil); err != nil || c != nil {
		t.Fatalf("never-run request offers a continuation: %+v %v", c, err)
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

// nativeAnswer has b answer a question a asked from the session handle; the
// answer is bound to that session.
func nativeAnswer(t *testing.T, a, b *Agent, handle string) string {
	t.Helper()
	r := ReplyReceiver{Kind: "live_session", SessionHandle: handle}
	sent, err := a.SendMessage(tctx(t), Outgoing{To: b.Address, Kind: envelope.KindQuestion, Body: "asked from the session", ReplyReceiver: &r})
	if err != nil {
		t.Fatal(err)
	}
	reply := receiverDirect(t, b, a, envelope.Inner{Kind: envelope.KindAnswer, Status: envelope.StatusDone, Body: "an answer", ReplyTo: sent.ID})
	if err = a.verifyAndStore(tctx(t), reply); err != nil {
		t.Fatal(err)
	}
	if boundInputs(t, a, reply.ID) != 1 {
		t.Fatal("the answer is not bound to the session")
	}
	return reply.ID
}

// A Claude session's end releases the same way; a new generation of the
// same session (resume, clear) does too.
func TestClaudeSessionEndReleasesInputs(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	input := nativeAnswer(t, w.alice, w.bob, owner.Handle)
	if _, e := w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	if boundInputs(t, w.alice, input) != 0 {
		t.Fatal("a new generation kept the old one's input hidden")
	}
	if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route); e != nil {
		t.Fatal(e)
	}
	second := nativeAnswer(t, w.alice, w.bob, owner.Handle)
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
