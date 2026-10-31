package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOrdinaryMentionAgentReaction(t *testing.T) {
	stub := installAgentStub(t)
	w, author, packet, _ := groupTurnsFixture(t)
	host := w.bob
	if host == author {
		host = w.alice
	}
	for _, a := range []*Agent{w.alice, w.bob, author} {
		roomReader(t, a)
		addCapSuccessor(t, a, protocol.CapMentionReaction)
	}
	setResponder(t, host, "agentstub", stub.dir, time.Minute)
	p, err := author.InviteAgent(tctx(t), packet.State.Conv, host.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "room agent invite", func() bool { return stateAt(t, host, p.PID).Invite != "" })
	if !stateAt(t, host, p.PID).Claimable() {
		if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "author sees agent", func() bool { return stateAt(t, author, p.PID).Claimable() })
	before := count(t, host, "inbox WHERE kind IN ('question','task')")
	sent, err := author.SendConv(tctx(t), p.Conv, ConvOutgoing{Body: "Thanks [@Agent](agentnet:agent/" + p.PID + ") for the update."})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "actual ordinary mention admitted", func() bool {
		m, n := convMsg(t, host, p.Conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		return n == 1 && m.Kind == envelope.KindMessage && m.Target == nil
	})
	ref := ControlRef{Conv: p.Conv, ID: sent.LID, Fingerprint: author.Self().Fingerprint()}
	reactors := func() []Reactor {
		messages, err := author.ConversationMessages(p.Conv)
		if err != nil {
			t.Fatal(err)
		}
		return reactorsOn(messages, sent.LID, "👍")
	}
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ref, "👍", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent mark on ordinary message", func() bool {
		rs := reactors()
		return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID
	})
	if count(t, host, "inbox WHERE kind IN ('question','task')") != before || stub.runs() != 0 {
		t.Fatal("mention reaction fabricated inbound work or invoked a model")
	}
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ref, "👍", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "duplicate mark stays one", func() bool { return len(reactors()) == 1 })
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ref, "👍", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "agent removed own mark", func() bool { return len(reactors()) == 0 })
	if _, err = author.SendAgentReaction(tctx(t), p.PID, ref, "👍", false); err == nil {
		t.Fatal("different host impersonated the agent")
	}
	wrong := ref
	wrong.Fingerprint = host.Self().Fingerprint()
	if _, err = host.SendAgentReaction(tctx(t), p.PID, wrong, "👍", false); err == nil {
		t.Fatal("different original author key accepted")
	}
	plain, err := author.SendConv(tctx(t), p.Conv, ConvOutgoing{Body: "plain @Agent label"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "plain label message", func() bool {
		_, n := convMsg(t, host, p.Conv, func(m ConvMessage) bool { return m.LID == plain.LID })
		return n == 1
	})
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ControlRef{Conv: p.Conv, ID: plain.LID, Fingerprint: ref.Fingerprint}, "👍", false); err == nil {
		t.Fatal("plain name substituted for exact PID")
	}
	own, err := host.SendConv(tctx(t), p.Conv, ConvOutgoing{Body: "Owner's ordinary [@Agent](agentnet:agent/" + p.PID + ") message"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ControlRef{Conv: p.Conv, ID: own.LID, Fingerprint: host.Self().Fingerprint()}, "🎉", false); err != nil {
		t.Fatal("exact own outbox original", err)
	}
	eventually(t, "own original agent mark at reader", func() bool {
		messages, e := author.ConversationMessages(p.Conv)
		return e == nil && len(reactorsOn(messages, own.LID, "🎉")) == 1
	})
	if stub.runs() != 0 {
		t.Fatal("own ordinary original fabricated a model invocation")
	}
	if _, err = author.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "host revoked", func() bool { return pollState(t, host, p.PID).State == PartDismissed })
	if _, err = host.SendAgentReaction(tctx(t), p.PID, ref, "👍", false); err == nil {
		t.Fatal("revoked agent retained reactions")
	}
}

func TestMentionReactionWireScope(t *testing.T) {
	base := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: "owner/host", To: "other/phone", TS: 1, Kind: envelope.KindMessage, Conv: protocol.NewID() + protocol.NewID(), LID: protocol.NewID(), PID: protocol.NewID(), Sub: envelope.SubReaction, Origin: envelope.OriginMentionReaction, Ref: &envelope.Ref{ID: protocol.NewID(), Fingerprint: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}, Body: `{"emoji":"👍","op":"add","n":1}`}
	if !envelope.MentionReaction(base) || agentRequirement(base) != protocol.CapMentionReaction {
		t.Fatal("mention extension not classified")
	}
	item := itemOf(base, base.Ref.Fingerprint, 1)
	raw, _ := json.Marshal(item)
	if got := agentRequirement(envelope.Inner{Conv: base.Conv, Sub: envelope.SubHistory, Body: string(raw)}); got != protocol.CapMentionReaction {
		t.Fatalf("history requirement %s", got)
	}
}
