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
	if row := inboxRow(t, w.bob, task.ID); !strings.Contains(row.Detail, "only a question's answer can carry") || !strings.Contains(row.Detail, "CHANGELOG") {
		t.Fatalf("detail %q", row.Detail)
	}
}

// In a conversation, a participation's question run proposes the same way:
// the proposal is its answer turn, the emotion trailer not part of the task.
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
	res := replyAt(t, w.alice, conv, q.ID)
	if res.Kind != envelope.KindAnswer || res.status != envelope.StatusProposal || res.Body != "Restart the deploy from step 3." || res.Emotion != "calm" {
		t.Fatalf("conversation proposal %+v (status %q)", res, res.status)
	}
	if !strings.Contains(st.last(), proposeMarker) {
		t.Fatalf("prompt:\n%s", st.last())
	}
}
