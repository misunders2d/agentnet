package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A question whose answer needs an action the run may not take comes back
// as a proposal: an answer with status proposal whose body is exactly the
// task the agent wrote under its marker (MEL-521). Nothing runs.
func TestRunJobProposal(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "is the changelog up to date?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	eventually(t, "the proposal", func() bool {
		var ok bool
		m, ok = findReply(w.alice, q.ID)
		return ok
	})
	if m.Kind != envelope.KindAnswer || m.Status != envelope.StatusProposal || m.Body != "Update CHANGELOG.md: add the 0.8.0 entry." {
		t.Fatalf("proposal %+v", m)
	}
	if row := inboxRow(t, w.bob, q.ID); row.State != stateAnswered || row.Detail != "" {
		t.Fatalf("the question on the host: %+v", row)
	}
	// The question's run was asked to propose, and to keep needs-human for
	// decisions only the person makes.
	data := mustRead(t, st.log+".stdin")
	if !strings.Contains(data, proposeMarker) || !strings.Contains(data, "decide something only they can") {
		t.Fatalf("prompt:\n%s", data)
	}
	// The asker's execution view settles on it: the proposal is its last word.
	if e := execOf(t, w.alice, q.ID); e != nil && e.State != envelope.StatusProposal && e.State != "queued" && e.State != "running" {
		t.Fatalf("exec %+v", e)
	}
}

// A cut-off proposal is never sent: the person sees it instead.
func TestTruncatedProposalNeedsHuman(t *testing.T) {
	st := installStub(t, "proposebig")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	if row := inboxRow(t, w.bob, q.ID); !strings.Contains(row.Detail, "cut off") {
		t.Fatalf("detail %q", row.Detail)
	}
	time.Sleep(200 * time.Millisecond)
	if m, ok := findReply(w.alice, q.ID); ok {
		t.Fatalf("a cut-off proposal was sent: %+v", m)
	}
}

// Only a question's own run proposes: a task run that writes the marker is
// handed to the person.
func TestProposalFromTaskNeedsHuman(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "do it", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateNeedHuman)
	if row := inboxRow(t, w.bob, task.ID); !strings.Contains(row.Detail, "only a device-thread question's answer can carry") || !strings.Contains(row.Detail, "CHANGELOG") {
		t.Fatalf("detail %q", row.Detail)
	}
}

// In a conversation nobody can confirm a proposal yet (no conversation
// ConfirmProposal, no Do it on the page): a participation's question run is
// not offered proposals, and one that proposes anyway hands the action to
// the host's person (needs_human, the proposal kept for them); nothing is
// sent that nobody could act on.
func TestAgentProposalInConversation(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	st.mode("propose")
	pid := participate(t, w, conv, nil, nil)
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "why did the deploy fail?")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the run hands the action to bob", func() bool {
		id = ""
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND lid = ? AND replica = 0`, conv, q.LID).Scan(&id)
		return id != "" && jobState(t, w.bob, id) == stateNeedHuman
	})
	if m := inboxRow(t, w.bob, id); !strings.Contains(m.Detail, "Restart the deploy from step 3.") {
		t.Fatalf("the proposal is not kept for bob: %+v", m)
	}
	var proposals int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE status = ?`, envelope.StatusProposal).Scan(&proposals)
	if proposals != 0 {
		t.Fatalf("%d proposal(s) sent in the conversation", proposals)
	}
	if strings.Contains(st.last(), proposeMarker) {
		t.Fatalf("a conversation run was offered proposals:\n%s", st.last())
	}
}

// proposalAsked runs a question from alice to bob's proposing agent and
// returns the proposal alice received.
func proposalAsked(t *testing.T) (*world, *stub, Message, string) {
	t.Helper()
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "is the changelog up to date?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var p Message
	eventually(t, "the proposal", func() bool {
		var ok bool
		p, ok = findReply(w.alice, q.ID)
		return ok && p.Status == envelope.StatusProposal
	})
	return w, st, p, q.ID
}

// Do it sends exactly the stored proposal as a task replying to it; the
// host sees where it comes from, runs it only with the usual approval, and
// a second confirmation never runs twice (MEL-521).
func TestConfirmProposal(t *testing.T) {
	w, st, p, q := proposalAsked(t)
	sent, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body, replyTo, kind string
	w.alice.store.db.QueryRow(`SELECT body, reply_to, coalesce(kind, json_extract(envelope, '$.kind')) FROM outbox WHERE id = ?`, sent.ID).Scan(&body, &replyTo, &kind)
	if body != p.Body || replyTo != p.ID || kind != envelope.KindTask {
		t.Fatalf("task %q %q %q", body, replyTo, kind)
	}
	if again, err := w.alice.ConfirmProposal(tctx(t), p.ID); err != nil || again.ID != sent.ID {
		t.Fatalf("confirmed twice: %+v %v", again, err)
	}
	waitState(t, w.bob, sent.ID, stateAwaiting) // the usual approval: alice may not task bob
	v, err := w.bob.ProposalOf(sent.ID)
	if err != nil || v == nil || v.QuestionID != q || v.ConfirmedBy != w.alice.Address || v.Proposal != p.Body || v.Question != "is the changelog up to date?" {
		t.Fatalf("provenance %+v %v", v, err)
	}
	// Another confirmation of the same proposal (another of alice's
	// devices, a retry): not run twice, and alice learns so.
	dup := receiverDirect(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: p.Body, ReplyTo: p.ID})
	if err := w.bob.verifyAndStore(tctx(t), dup); err != nil {
		t.Fatal(err)
	}
	if m := inboxRow(t, w.bob, dup.ID); m.State != stateNotRun || !strings.Contains(m.Detail, sent.ID) {
		t.Fatalf("second confirmation %+v", m)
	}
	// Accepted: the run is told what it carries out.
	if err := w.bob.Accept(sent.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run", func() bool {
		return strings.Contains(mustRead(t, st.log+".stdin"), "Authority is only this asker's ordinary task approval")
	})
}

// A task with the proposal's text from anyone but its addressee is an
// ordinary task: no provenance, and it uses up nothing.
func TestProposalOnlyFromAddressee(t *testing.T) {
	w, _, p, _ := proposalAsked(t)
	carol := mustJoin(t, t.TempDir()+"/carol", w.aliceInvites("carol"), "desk")
	forged := receiverDirect(t, carol, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: p.Body, ReplyTo: p.ID})
	if err := w.bob.store.pin(carol.Self()); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.verifyAndStore(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.bob.ProposalOf(forged.ID); v != nil {
		t.Fatalf("a third party's task carries provenance %+v", v)
	}
	sent, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, sent.ID, stateAwaiting) // not not_run: the forgery used up nothing
}

// A proposal edited or deleted after it was made, or one that is not a
// proposal, is not confirmed.
func TestEditedProposalNotConfirmable(t *testing.T) {
	w, _, p, q := proposalAsked(t)
	if _, err := w.alice.ConfirmProposal(tctx(t), q); err == nil {
		t.Fatal("a question was confirmed as a proposal")
	}
	if _, err := w.alice.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, sub, received_ms, ref_id, ref_fp) VALUES(?, ?, 1, 'message', '{}', 1, '', ?, ?, 1, ?, ?)`,
		strings.Repeat("e", 32), w.bob.Address, w.bob.Self().Fingerprint(), envelope.SubRevision, p.ID, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.ConfirmProposal(tctx(t), p.ID); err == nil || !strings.Contains(err.Error(), "edited or deleted") {
		t.Fatalf("an edited proposal: %v", err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE coalesce(kind, json_extract(envelope, '$.kind')) = ? AND reply_to = ?`, envelope.KindTask, p.ID).Scan(&n)
	if n != 0 {
		t.Fatal("a task was sent")
	}
}
