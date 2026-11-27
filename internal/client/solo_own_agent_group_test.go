package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
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
	named, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "pi", Dir: t.TempDir()})
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
