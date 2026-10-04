package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// threadOf is the summary of the device thread holding message id at a.
func threadOf(t *testing.T, a *Agent, id string) ThreadSummary {
	t.Helper()
	ts, err := a.Threads()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ts {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no thread %s at %s: %+v", id, a.Address, ts)
	return ThreadSummary{}
}

// A device thread names its agent when one is known, for the page to show
// who it is with: the latest agent a request names as its target, or an
// answer, result or progress names as its author (both as their senders
// wrote and admission bound them), on either side. A received request's
// local executor stamp is not the thread's word; a thread with no named
// agent names none.
func TestThreadSummaryNamesItsAgent(t *testing.T) {
	w := newWorld(t, "")
	agentA, agentB := protocol.NewID(), protocol.NewID()
	q := namedExecutorRequest(t, w, agentA, envelope.KindQuestion, "")
	if s := threadOf(t, w.alice, q); s.AgentID != agentA {
		t.Fatalf("the asker's thread: %+v", s)
	}
	if s := threadOf(t, w.bob, q); s.AgentID != agentA {
		t.Fatalf("the host's thread: %+v", s)
	}
	// A local executor stamp on a received request is not the thread's.
	w.bob.store.db.Exec(`UPDATE inbox SET agent_id = ? WHERE id = ?`, protocol.NewID(), q)
	if s := threadOf(t, w.bob, q); s.AgentID != agentA {
		t.Fatalf("the host's thread named its local stamp: %+v", s)
	}
	// The host's answer, by another of its agents, is the latest named.
	in := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "green",
		ReplyTo: q, AgentID: agentB, Status: envelope.StatusDone}
	if err := w.bob.store.addOutbox(envelope.Envelope{ID: in.ID, From: in.From, To: in.To}, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if s := threadOf(t, w.alice, q); s.AgentID != agentB {
		t.Fatalf("the asker's thread after the answer: %+v", s)
	}
	if s := threadOf(t, w.bob, q); s.AgentID != agentB {
		t.Fatalf("the host's thread after the answer: %+v", s)
	}
	plain := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "hello"}
	if err := w.alice.store.addOutbox(envelope.Envelope{ID: plain.ID, From: plain.From, To: plain.To}, plain, "", nil); err != nil {
		t.Fatal(err)
	}
	if s := threadOf(t, w.alice, plain.ID); s.AgentID != "" {
		t.Fatalf("a thread with no agent: %+v", s)
	}
}
