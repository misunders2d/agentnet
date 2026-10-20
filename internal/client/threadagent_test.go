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

// A device thread keeps its original host/key/agent destination on either
// side. Matching output joins it; retained output from another named agent
// remains separate. A received request's local executor stamp cannot rebind
// it, and an ordinary message names no agent.
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
	// Output from the exact requested agent joins the original exchange.
	recipient, err := w.alice.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	matching := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "matching green",
		ReplyTo: q, AgentID: agentA, Status: envelope.StatusDone}
	matchingEnvelope, err := envelope.Seal(matching, w.bob.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.addOutbox(matchingEnvelope, matching, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addInbox(matching, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	// An older stored answer naming another agent cannot retarget the request.
	in := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "green",
		ReplyTo: q, AgentID: agentB, Status: envelope.StatusDone}
	otherEnvelope, err := envelope.Seal(in, w.bob.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.addOutbox(otherEnvelope, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	for _, host := range []*Agent{w.alice, w.bob} {
		if s := threadOf(t, host, q); s.AgentID != agentA || s.AgentHost != w.bob.Address || s.AgentKey != w.bob.Self().Fingerprint() || s.AgentLegacy || s.Count != 2 {
			t.Fatalf("original exact exchange at %s: %+v", host.Address, s)
		}
		if s := threadOf(t, host, in.ID); s.AgentID != agentB || s.AgentHost != w.bob.Address || s.AgentKey != w.bob.Self().Fingerprint() || s.AgentLegacy || s.Count != 1 {
			t.Fatalf("distinct historical author at %s: %+v", host.Address, s)
		}
		if s, err := host.TopicOf(matching.ID); err != nil || s.ID != q {
			t.Fatalf("matching answer left its original topic at %s: %+v %v", host.Address, s, err)
		}
		view, err := host.DisplayConversation(in.ID, 0, 0)
		wantDir := "in"
		if host == w.bob {
			wantDir = "out"
		}
		if err != nil || len(view.Messages) != 1 || view.Messages[0].ID != in.ID || view.Messages[0].AgentID != agentB || view.Messages[0].From != w.bob.Address || view.Messages[0].FromKey != w.bob.Self().Fingerprint() || view.Messages[0].Dir != wantDir {
			t.Fatalf("distinct output lost exact authorship/direction at %s: %+v %v", host.Address, view, err)
		}
	}
	plain := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "hello"}
	if err := w.alice.store.addOutbox(envelope.Envelope{ID: plain.ID, From: plain.From, To: plain.To}, plain, "", nil); err != nil {
		t.Fatal(err)
	}
	if s := threadOf(t, w.alice, plain.ID); s.AgentID != "" {
		t.Fatalf("a thread with no agent: %+v", s)
	}
}
