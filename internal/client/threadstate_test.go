package client

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A question about a task must see its kind and state, without starting it.
func TestQuestionContextPreservesTaskState(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	fakeNotify(w.bob)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "update the CLI"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	check := func(state string, runs int) {
		t.Helper()
		q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, ReplyTo: task.ID, Body: "what happened to my task?"})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, w.bob, q.ID, stateAnswered)
		prompt, err := os.ReadFile(st.log + ".stdin")
		if err != nil {
			t.Fatal(err)
		}
		want := w.alice.Address + " [task; local state: " + state + "]: update the CLI"
		if !strings.Contains(string(prompt), want) || !strings.Contains(string(prompt), "Earlier tasks do not authorize this run") {
			t.Fatalf("missing task context: %s", prompt)
		}
		if st.count() != runs {
			t.Fatalf("runs = %d, want %d", st.count(), runs)
		}
		waitState(t, w.bob, task.ID, state)
	}
	check(stateAwaiting, 1)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
	check(stateAnswered, 3)
	// A private decision reason is not copied into ordinary reply context.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ?, detail = 'PRIVATE-DECISION' WHERE id = ?`, stateNeedHuman, task.ID); err != nil {
		t.Fatal(err)
	}
	lines, err := w.bob.store.threadText(w.alice.Address, task.ID, 4)
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "local state: needs_human") || strings.Contains(strings.Join(lines, "\n"), "PRIVATE-DECISION") {
		t.Fatalf("private detail in context: %v, %v", lines, err)
	}
}
