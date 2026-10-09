package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRequestFollowupGuard(t *testing.T) {
	w := newWorld(t, "")
	store := func(in envelope.Inner) string {
		t.Helper()
		if in.ID == "" {
			in.ID = protocol.NewID()
		}
		in.V, in.From, in.To, in.TS = envelope.Version, w.alice.Address, w.bob.Address, time.Now().Unix()
		r, _ := w.bob.Self().Recipient()
		e, err := envelope.Seal(in, w.alice.id.Sign, r)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.bob.verifyAndStore(tctx(t), e); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	original := store(envelope.Inner{Kind: envelope.KindQuestion, Body: "Create contribution-margin.md in Russian"})
	ref := &envelope.Ref{ID: original, Fingerprint: w.alice.Self().Fingerprint()}
	id := store(envelope.Inner{Kind: envelope.KindQuestion, Body: "Use English instead", ReplyTo: original, Followup: ref})
	j := job{ID: id, From: w.alice.Address, Key: ref.Fingerprint, Kind: envelope.KindQuestion, Body: "Use English instead", ReplyTo: original}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, original); err != nil {
		t.Fatal(err)
	}
	prompt, err := w.bob.requestFollowupPrompt(j)
	if err != nil || !strings.Contains(prompt, "Russian") || !strings.Contains(prompt, "queued, not accepted as live steering") || !strings.Contains(prompt, "Do not rerun completed effects") {
		t.Fatal(prompt, err)
	}
	for _, state := range []string{stateCancelled, "interrupted", stateJobFailed, stateNeedHuman, statePending} {
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, state, original); err != nil {
			t.Fatal(err)
		}
		if _, err = w.bob.requestFollowupPrompt(j); err == nil {
			t.Fatalf("%s original silently launched its correction", state)
		}
	}
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, original); err != nil {
		t.Fatal(err)
	}
	wrongKind := j
	wrongKind.Kind = envelope.KindTask
	if _, err = w.bob.requestFollowupPrompt(wrongKind); err == nil {
		t.Fatal("question became a task")
	}
	wrongKey := j
	wrongKey.Key = w.bob.Self().Fingerprint()
	if _, err = w.bob.requestFollowupPrompt(wrongKey); err == nil {
		t.Fatal("different author gained correction authority")
	}
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET replica=1 WHERE id=?`, original); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.requestFollowupPrompt(j); err == nil {
		t.Fatal("inert history became an original execution claim")
	}
	plain := store(envelope.Inner{Kind: envelope.KindQuestion, Body: "Ordinary quotation", ReplyTo: original, Quote: original})
	j.ID = plain
	if got, err := w.bob.requestFollowupPrompt(j); err != nil || got != "" {
		t.Fatal("ordinary quote acquired correction semantics", got, err)
	}
}

func TestRequestFollowupWorkerQueue(t *testing.T) {
	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	runAgent(t, w.alice)
	original, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "Write contribution-margin.md in Russian"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, original.ID, stateRunning)
	file := filepath.Join(t.TempDir(), "terms.md")
	if err = os.WriteFile(file, []byte("English terminology"), 0600); err != nil {
		t.Fatal(err)
	}
	x := RequestFollowup{Ref: envelope.Ref{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}, ID: protocol.NewID(), Body: "Use English instead", Files: []OutgoingFile{{Path: file}}}
	note, err := w.alice.QueueRequestFollowup(tctx(t), x)
	if err != nil || !strings.Contains(note, "queued") || !strings.Contains(note, "not proven native acceptance") {
		t.Fatal(note, err)
	}
	waitState(t, w.bob, x.ID, stateAnswered)
	if st.count() != 2 {
		t.Fatalf("original and one correction should run once each, got %d", st.count())
	}
	prompt, err := os.ReadFile(st.log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Russian", "Use English instead", "Explicit queued follow-up", "terms.md", "Do not rerun completed effects"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("missing %q in follow-up prompt", want)
		}
	}
	if _, err = w.alice.QueueRequestFollowup(tctx(t), x); err != nil {
		t.Fatal("same exact retry", err)
	}
	if st.count() != 2 {
		t.Fatal("retry launched another request")
	}
	x.Body = "Different work"
	if _, err = w.alice.QueueRequestFollowup(tctx(t), x); err == nil {
		t.Fatal("send identity reused for different correction")
	}
}

func TestRequestFollowupArrivalOrder(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	original := protocol.NewID()
	ref := &envelope.Ref{ID: original, Fingerprint: w.alice.Self().Fingerprint()}
	first, last := strings.Repeat("f", 32), strings.Repeat("a", 32)
	for _, id := range []string{original, first, last} {
		in := envelope.Inner{V: 1, ID: id, From: w.alice.Address, To: w.bob.Address, TS: 1, Kind: envelope.KindQuestion, Body: id}
		if id != original {
			in.ReplyTo = original
			in.Followup = ref
		}
		r, _ := w.bob.Self().Recipient()
		e, err := envelope.Seal(in, w.alice.id.Sign, r)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.bob.verifyAndStore(tctx(t), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET received_at=1,state=CASE WHEN id=? THEN ? ELSE ? END`, original, stateAnswered, stateAccepted); err != nil {
		t.Fatal(err)
	}
	j, ok, err := w.bob.store.claimJob("stub")
	if err != nil || !ok || j.ID != first {
		t.Fatalf("same-second corrections reversed durable arrival order: got %s, want %s, ok=%t err=%v", j.ID, first, ok, err)
	}
	if _, ok, err = w.bob.store.claimJob("stub"); err != nil || ok {
		t.Fatal("later correction passed its still-running predecessor", ok, err)
	}
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, first); err != nil {
		t.Fatal(err)
	}
	j, ok, err = w.bob.store.claimJob("stub")
	if err != nil || !ok || j.ID != last {
		t.Fatal("completion failed to unblock the next correction", j.ID, ok, err)
	}
}

func TestRequestFollowupParticipation(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, nil, nil)
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "Write the contribution margin document")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	x := RequestFollowup{Conv: conv, Ref: envelope.Ref{ID: q.LID, Fingerprint: w.alice.Self().Fingerprint()}, ID: protocol.NewID(), Body: "Use English in the same document"}
	if _, err = w.alice.QueueRequestFollowup(tctx(t), x); err != nil {
		t.Fatal(err)
	}
	var jobID string
	eventually(t, "follow-up stays in exact participation", func() bool {
		return w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND pid=? AND replica=0`, conv, x.ID, pid).Scan(&jobID) == nil
	})
	waitState(t, w.bob, jobID, stateAnswered)
	if st.runs() != 2 || !strings.Contains(st.last(), "Write the contribution margin document") || !strings.Contains(st.last(), "Explicit queued follow-up") {
		t.Fatal("participation lost exact original context", st.runs(), st.last())
	}
	if _, err = w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	x.ID = protocol.NewID()
	if _, err = w.alice.QueueRequestFollowup(tctx(t), x); err == nil {
		t.Fatal("dismissed original participation was silently reinvited")
	}
}
