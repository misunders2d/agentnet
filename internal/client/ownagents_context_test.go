package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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
	replyAt(t, w.alice, conv, ask.ID)
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
	// Turns stored before this fix have no admission record, and a display
	// copy is never recovered as context without one (exact proof only,
	// TestRetainedContextNativePromptAndRefresh): those show from the next
	// message on.
	for _, a := range []*Agent{w.alice, server} {
		if _, err := a.store.db.Exec(`DELETE FROM room_context WHERE conv=?`, conv); err != nil {
			t.Fatal(err)
		}
	}
	if retained := contextText(t, w.alice, laptopPID); strings.Contains(retained, "SERVER_AGENT_REPLY") {
		t.Errorf("a display copy with no admission record became context:\n%s", retained)
	}
}

// Owner, 2026-11-09: the owner asked their own agent, in their own chat, to
// move messages into a topic; it failed as "selected original was not
// admitted to this agent's scope". The host's own person may move any
// message there themselves, so their request covers what they select.
func TestOwnerAsksOwnAgentToOrganizeLiveMessages(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "own agent")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Claimable() {
		if _, err = w.alice.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "own agent active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() && stateAt(t, w.bob, p.PID).Claimable() })
	for _, a := range []*Agent{w.alice, w.bob} {
		addCapSuccessor(t, a, protocol.CapAgentOrganization)
	}
	live, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "BOB_LIVE_TURN after the invitation"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob's turn on the host", func() bool {
		_, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == live.LID })
		return n == 1
	})
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "move today's messages into a topic")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, q.ID, stateAgentWaiting)
	j := claimAt(t, w.alice, q.ID)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	c, err := w.alice.agentContext(stateAt(t, w.alice, p.PID), j.ID, agentContextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.captureAgentContext(j, c); err != nil {
		t.Fatal(err)
	}
	var attempt int
	if err = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, j.ID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{live.LID}, New: true, Title: "Today"})
	if err != nil {
		t.Fatalf("owner's own agent could not move the owner's selected live message: %v", err)
	}
	row, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
	eventually(t, "the move admitted at the other member", func() bool {
		msgs, _ := w.bob.ConversationMessages(conv)
		return row.Topic != "" && ChatTopicAssignments(msgs)[live.LID] == row.Topic
	})
}
