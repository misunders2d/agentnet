package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestTopicGuestTwoOwnersCapturedAudience(t *testing.T) {
	w, first, conv, _, stub := humanWorld(t)
	second := proofReader(t, w, "second-topic-guest")
	runAgent(t, second)
	humanTestCaps(t, second)
	if err := second.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	topic := protocol.NewID()
	for _, guest := range []*Agent{first, second} {
		p, e := w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, nil, "", &topic)
		if e != nil {
			t.Fatal(e)
		}
		eventually(t, "parent received", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
		if _, e = guest.AcceptParticipation(tctx(t), p.PID); e != nil {
			t.Fatal(e)
		}
		child, e := guest.InviteAgentInScope(tctx(t), conv, guest.Address, "", nil, nil, "", &topic)
		if e != nil {
			t.Fatal(e)
		}
		eventually(t, "child active at original member", func() bool { return stateAt(t, w.alice, child.PID).Claimable() })
	}
	h, e := w.alice.livePlan(tctx(t), conv, "", "", topic)
	if e != nil {
		t.Fatal(e)
	}
	if len(h.Audience) != 4 {
		t.Fatalf("expected two parents and two children: %+v", h)
	}
	root, _, _, e := w.alice.store.conversation(conv)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.verifyHumanProof(tctx(t), root, h); e != nil {
		t.Fatalf("sibling parent erased valid host: %v", e)
	}
	sent, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, Topic: topic, Body: "BOTH_PARENT_CONTEXT"})
	if e != nil {
		t.Fatal(e)
	}
	for _, guest := range []*Agent{first, second} {
		eventually(t, "both hosts receive exact same-topic audience", func() bool { return inboxCount(t, guest, `conv=? AND lid=?`, conv, sent.LID) == 1 })
	}
}
