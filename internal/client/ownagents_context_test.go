package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// ownAgentsWorld: a DM between alice and bob in which alice's person hosts
// two agents on two of her devices (laptop: alice, server: a linked device),
// both invited by alice's laptop after earlier history. MEL-633 / MEL-540:
// each agent sees the chat's new turns and the other agent's verified
// replies; earlier history stays limited to grants.
func ownAgentsWorld(t *testing.T) (w *world, server *Agent, conv string, laptopPID, serverPID string, stub *agentStub) {
	t.Helper()
	stub = installAgentStub(t)
	w, conv, _, _ = agentWorld(t)
	server = linked(t, w.alice)
	eventually(t, "server to hold the history", func() bool { return len(convBodies(t, server, conv)) == 3 })
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	setResponder(t, server, "agentstub", stub.dir, time.Minute)
	laptop, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "own laptop agent")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "laptop agent active", func() bool { return stateAt(t, w.alice, laptop.PID).Claimable() })
	srv, err := w.alice.InviteAgent(tctx(t), conv, server.Address, nil, nil, "own server agent")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "server agent invited", func() bool {
		s := pollState(t, server, srv.PID).State
		return s == PartInvited || s == PartActive
	})
	if pollState(t, server, srv.PID).State == PartInvited {
		if _, err = server.AcceptParticipation(tctx(t), srv.PID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "server agent active everywhere", func() bool {
		return stateAt(t, w.alice, srv.PID).Claimable() && stateAt(t, server, srv.PID).Claimable() && stateAt(t, w.alice, laptop.PID).Claimable() && stateAt(t, server, laptop.PID).Claimable()
	})
	return w, server, conv, laptop.PID, srv.PID, stub
}

func contextText(t *testing.T, a *Agent, pid string) string {
	t.Helper()
	c, err := a.ParticipationContext(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(c.lines, "\n")
}

func TestOwnAgentsOnTwoDevicesSeeEachOther(t *testing.T) {
	w, server, conv, laptopPID, serverPID, stub := ownAgentsWorld(t)
	// A new human turn from the other member, after both invitations.
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "BOB_NEW_TURN after the invitations"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob's turn on both hosts", func() bool {
		return strings.Contains(strings.Join(convBodies(t, w.alice, conv), "|"), "BOB_NEW_TURN") && strings.Contains(strings.Join(convBodies(t, server, conv), "|"), "BOB_NEW_TURN")
	})
	// The owner's laptop asks the server agent; it answers.
	stub.mode("echo")
	t.Setenv("AGENT_ANSWER", "SERVER_AGENT_REPLY: checked the offer")
	ask, err := w.alice.AskAgent(tctx(t), serverPID, envelope.KindQuestion, "OWNER_ASKS_SERVER: check the offer")
	if err != nil {
		t.Fatal(err)
	}
	serverReply := replyAt(t, w.alice, conv, ask.ID)
	// The owner's laptop asks its own agent; it answers.
	t.Setenv("AGENT_ANSWER", "LAPTOP_AGENT_REPLY: here is what is needed")
	ask2, err := w.alice.AskAgent(tctx(t), laptopPID, envelope.KindQuestion, "OWNER_ASKS_LAPTOP: tell the server agent")
	if err != nil {
		t.Fatal(err)
	}
	laptopReply := replyAt(t, w.alice, conv, ask2.ID)
	eventually(t, "laptop reply on the server", func() bool {
		_, n := convMsg(t, server, conv, func(m ConvMessage) bool { return m.LID == laptopReply.LID })
		return n == 1
	})
	laptopCtx := contextText(t, w.alice, laptopPID)
	for _, want := range []string{"BOB_NEW_TURN", "OWNER_ASKS_SERVER", "SERVER_AGENT_REPLY"} {
		if !strings.Contains(laptopCtx, want) {
			t.Errorf("laptop agent context lacks %q:\n%s", want, laptopCtx)
		}
	}
	serverCtx := contextText(t, server, serverPID)
	for _, want := range []string{"BOB_NEW_TURN", "OWNER_ASKS_LAPTOP", "LAPTOP_AGENT_REPLY"} {
		if !strings.Contains(serverCtx, want) {
			t.Errorf("server agent context lacks %q:\n%s", want, serverCtx)
		}
	}
	for _, ctx := range []string{laptopCtx, serverCtx} {
		if strings.Contains(ctx, "logs are in the ticket") || strings.Contains(ctx, "lunch") {
			t.Errorf("ungranted earlier history exposed:\n%s", ctx)
		}
	}
	_ = serverReply
}
