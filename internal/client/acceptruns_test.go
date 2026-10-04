package client

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// BUG-23: accept refuses, saying why, when nothing here would run what it
// accepts (no responder chosen, or answering by hand chosen) instead of
// reporting success and leaving the task "accepted" for good; the task
// stays in review as it was. With a responder, accept runs it.
func TestAcceptRefusesWhenNothingRuns(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "wrap the pallets", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	stays := func(how string) {
		t.Helper()
		if s, _ := w.bob.store.jobState(task.ID); s != stateAwaiting {
			t.Fatalf("%s: the task is now %s", how, s)
		}
		if review, _ := w.bob.Review(); !slices.ContainsFunc(review, func(m Message) bool { return m.ID == task.ID }) {
			t.Fatalf("%s: the task left review", how)
		}
	}
	if err := w.bob.Accept(task.ID); !errors.Is(err, ErrNothingRuns) || !strings.Contains(err.Error(), "no responder is chosen here") {
		t.Fatalf("accept with no responder: %v", err)
	}
	stays("accept with no responder")
	if _, _, err := w.bob.AcceptAlways(task.ID); !errors.Is(err, ErrNothingRuns) {
		t.Fatalf("accept --always with no responder: %v", err)
	}
	stays("accept --always with no responder")
	if err := w.bob.SetResponder(nil); err != nil { // answering by hand, chosen
		t.Fatal(err)
	}
	if err := w.bob.Accept(task.ID); !errors.Is(err, ErrNothingRuns) || !strings.Contains(err.Error(), "answer by hand") {
		t.Fatalf("accept when answering by hand: %v", err)
	}
	stays("accept when answering by hand")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatalf("accept with a responder: %v", err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
}

// BUG-23: accept refuses a DM request whose agent participation ended,
// with that reason, instead of reporting success for a request the worker
// then only closes (not_run).
func TestAcceptRefusesEndedParticipation(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, nil, nil)
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "shrink-wrap pallets 5-8"); err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the task waiting at bob", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, "shrink-wrap pallets 5-8", stateAwaiting).Scan(&id)
		return id != ""
	})
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the participation dismissed", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	if err := w.bob.Accept(id); !errors.Is(err, ErrNothingRuns) || !strings.Contains(err.Error(), "participation is dismissed") {
		t.Fatalf("accept of an ended participation's request: %v", err)
	}
	if s, _ := w.bob.store.jobState(id); s != stateAwaiting || stub.runs() != 0 {
		t.Fatalf("refused accept: state %s, runs %d", s, stub.runs())
	}
}

// A request taken in under a reply session is run by that session's own
// worker (claimReplyReceiverJob), not the default responder: with none
// chosen here, accepting one that was interrupted is not refused as if
// nothing would run it (review finding 9).
func TestAcceptReplySessionInputWithoutResponder(t *testing.T) {
	w := newWorld(t, "") // bob chose no responder
	id, binding := protocol.NewID(), protocol.NewID()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state) VALUES(?, ?, 1, ?, 'which region?', 1, ?)`, []any{id, w.alice.Address, envelope.KindQuestion, stateInterrupt}},
		{`INSERT INTO reply_receivers(id, conv, request_ref, receiver, created_at) VALUES(?, ?, ?, '{"kind":"managed_agent"}', 1)`, []any{binding, protocol.NewID(), protocol.NewID()}},
		{`INSERT INTO reply_receiver_inputs(binding, inbox_id) VALUES(?, ?)`, []any{binding, id}},
	} {
		if _, err := w.bob.store.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.bob.Accept(id); err != nil {
		t.Fatalf("accept of an interrupted reply-session request: %v", err)
	}
	if s, _ := w.bob.store.jobState(id); s != stateAccepted {
		t.Fatalf("accepted, it is %s", s)
	}
}
