package client

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNamedDeviceAgentBindingAndStorage(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	self, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	host, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	peer := host.Public("host/worker")
	a := &Agent{Address: "person/laptop", id: self, store: s}
	request := envelope.Inner{ID: protocol.NewID(), From: a.Address, To: peer.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "question", Target: &envelope.Target{Address: peer.Address, Fingerprint: peer.Fingerprint(), AgentID: protocol.NewID()}}
	if err := s.addOutbox(envelope.Envelope{ID: request.ID, From: request.From, To: request.To}, request, "", nil); err != nil {
		t.Fatal(err)
	}
	answer := envelope.Inner{ID: protocol.NewID(), From: peer.Address, To: a.Address, Kind: envelope.KindAnswer, ReplyTo: request.ID, AgentID: request.Target.AgentID, Body: "answer"}
	if err := a.checkDeviceAgent(answer, peer); err != nil {
		t.Fatal(err)
	}
	if err := s.addInbox(answer, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT agent_id FROM inbox WHERE id=?`, answer.ID).Scan(&stored); err != nil || stored != answer.AgentID {
		t.Fatalf("author not retained: %q, %v", stored, err)
	}
	for _, bad := range []envelope.Inner{
		{AgentID: protocol.NewID(), ReplyTo: request.ID},
		{AgentID: answer.AgentID, ReplyTo: protocol.NewID()},
		{Target: &envelope.Target{Address: a.Address, Fingerprint: peer.Fingerprint(), AgentID: answer.AgentID}},
	} {
		if a.checkDeviceAgent(bad, peer) == nil {
			t.Fatal("foreign or unbound identity accepted")
		}
	}
	otherKey, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a.checkDeviceAgent(answer, otherKey.Public(peer.Address)) == nil {
		t.Fatal("changed host key accepted")
	}
	// Incoming requests retain their exact selected target for the authority claim.
	request.ID, request.From, request.To = protocol.NewID(), peer.Address, a.Address
	request.Target = &envelope.Target{Address: a.Address, Fingerprint: a.Self().Fingerprint(), AgentID: protocol.NewID()}
	if err := a.checkDeviceAgent(request, peer); err != nil {
		t.Fatal(err)
	}
	if err := s.addInbox(request, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT target FROM inbox WHERE id=?`, request.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var target envelope.Target
	if json.Unmarshal([]byte(stored), &target) != nil || target != *request.Target {
		t.Fatalf("target not retained: %s", stored)
	}
}

func TestNamedAgentContentHashBindsAuthor(t *testing.T) {
	in := envelope.Inner{Conv: "conversation", LID: "turn", Kind: envelope.KindAnswer, Body: "answer", PID: "participation"}
	base := contentHash(in)
	in.AgentID = protocol.NewID()
	first := contentHash(in)
	if first == base {
		t.Fatal("agent author missing from logical content hash")
	}
	in.AgentID = protocol.NewID()
	if contentHash(in) == first {
		t.Fatal("distinct agent authors alias")
	}
}

func TestNamedAgentRefusesUnsupportedPeerBeforeSend(t *testing.T) {
	w := newWorld(t, "")
	// No agi1 session: a catalog ID must not silently become a default-agent job.
	_, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "question",
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: protocol.NewID()}})
	if err == nil || !strings.Contains(err.Error(), "cannot read named agents") {
		t.Fatalf("unsupported peer: %v", err)
	}
	var n int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unsupported send queued: %d, %v", n, err)
	}
	if n := inboxCount(t, w.bob, "1=1"); n != 0 {
		t.Fatalf("unsupported request admitted: %d", n)
	}
}
