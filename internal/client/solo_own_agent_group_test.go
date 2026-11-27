package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Ruslan's report (2026-11-27): one person, one device, a group of their
// own with their own agent on that same device. Nobody else needs a copy;
// the group must still talk and the own agent must still be reachable.
func TestSoloOwnAgentGroupOnOneDevice(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	conv := g.Root.ID()
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "note to self"}); err != nil {
		t.Errorf("plain message in a solo group: %v", err)
	}
	// A harness that never starts: this test is about sending, on every OS,
	// and must not launch a real agent program.
	Harnesses["solonone"] = harness{bin: filepath.Join(t.TempDir(), "missing"), question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Cleanup(func() { delete(Harnesses, "solonone") })
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "solonone", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatalf("invite own agent into a solo group: %v", err)
	}
	if !p.Claimable() {
		t.Fatalf("own agent not active: %+v", p)
	}
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "hello"); err != nil {
		t.Errorf("ask own agent in a solo group: %v", err)
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "after the agent joined"}); err != nil {
		t.Errorf("plain message with own agent present: %v", err)
	}
}

// MEL-840 end to end on one device: the own agent's job runs here and its
// answer shows in the group; a file goes with the ask. Then, as Ruslan did,
// the agent is removed and "Chat with Pi" (AgentChat.tsx: a topic of "My
// agents", ownAgentTopic's invitation, then askAgent) brings it back: the
// first message reaches the local agent again.
func TestSoloOwnAgentGroupAnswersAndRejoins(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("emotion")
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	g, err := w.alice.CreateGroup(tctx(t), "My agents")
	if err != nil {
		t.Fatal(err)
	}
	conv := g.Root.ID()
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("invite own agent: %+v %v", p, err)
	}
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err = os.WriteFile(file, []byte("solo notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "hello", OutgoingFile{Path: file, Name: "notes.txt"})
	if err != nil {
		t.Fatalf("ask with a file: %v", err)
	}
	if r := replyAt(t, w.alice, conv, ask.ID); !strings.Contains(r.Body, "the deploy failed at step 3") {
		t.Fatalf("own agent's answer: %+v", r)
	}
	if !strings.Contains(stub.last(), "hello") || !strings.Contains(stub.last(), "solo notes") {
		t.Fatalf("the local agent was not asked: %q", stub.last())
	}
	if m, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == ask.LID && m.Kind == envelope.KindQuestion }); n != 1 || len(m.Attachments) != 1 || m.Attachments[0].Name != "notes.txt" {
		t.Fatalf("the ask and its file in the group: %d %+v", n, m.Attachments)
	} else if m.Job == "" {
		t.Fatalf("the ask shows no local job: %+v", m)
	}
	var local, posted int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=? AND state=?`, ask.LID, w.alice.Address, protocol.StateDelivered).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND state!=?`, ask.LID, protocol.StateDelivered).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	if local != 1 || posted != 0 {
		t.Fatalf("the ask's copies: %d local-only, %d to post", local, posted)
	}
	if _, err = w.alice.DismissParticipation(WithQueuedSend(tctx(t), ""), p.PID); err != nil {
		t.Fatalf("remove own agent: %v", err)
	}
	eventually(t, "the agent removed", func() bool { return !stateAt(t, w.alice, p.PID).Claimable() })

	// "Chat with Pi": a new topic, the invitation the UI makes, the ask.
	topic := protocol.NewID()
	q, err := w.alice.InviteAgentInScope(WithQueuedSend(tctx(t), ""), conv, w.alice.Address, named.ID, nil, []string{w.alice.Self().Fingerprint()}, "", &topic)
	if err != nil {
		t.Fatalf("Chat with: invite own agent again: %v", err)
	}
	eventually(t, "own agent active in its topic", func() bool { return stateAt(t, w.alice, q.PID).Claimable() })
	id := protocol.NewID()
	sent, _, err := w.alice.AskAgentInTopicQueued(tctx(t), id, q.PID, envelope.KindQuestion, "Хай", topic, nil, "", 0)
	if err != nil {
		t.Fatalf("Chat with: first message: %v", err)
	}
	if r := replyAt(t, w.alice, conv, sent.ID); !strings.Contains(r.Body, "the deploy failed at step 3") {
		t.Fatalf("own agent's answer in its topic: %+v", r)
	}
	if !strings.Contains(stub.last(), "Хай") {
		t.Fatalf("the local agent was not asked in its topic: %q", stub.last())
	}
}
