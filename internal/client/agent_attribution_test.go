package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An agent's turn, its participation's output named or not or any turn
// whose origin says an agent wrote it, comes only from the participation's
// exact host device key, while it is active (ROOM_V1 §4.1). Here the host
// runs its default agent, so no agent ID binds anything: a member device
// that is not the host cannot post as it, and the view marks a turn as an
// agent's only when that check holds.
func TestAgentTurnsComeOnlyFromTheExactHost(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:1], nil)
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	ans := replyAt(t, w.alice, conv, q.ID)
	if ans.From != w.bob.Address || ans.AgentID != "" || ans.PID != pid || !ans.VerifiedAgent {
		t.Fatalf("the host's own answer at a member: %+v", ans)
	}
	if hosted := replyAt(t, w.bob, conv, q.ID); hosted.Dir != "out" || !hosted.VerifiedAgent {
		t.Fatalf("the host's own answer as sent: %+v", hosted)
	}
	if req, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ID == q.ID }); req.VerifiedAgent {
		t.Fatalf("a person's request marked as an agent's: %+v", req)
	}

	// Alice's device is a member but not the host: none of these is the
	// agent's, whatever its origin says, and none is stored.
	_, root := rootOf(t, w.bob, conv)
	turn := func(in envelope.Inner) envelope.Inner {
		in.Conv, in.Root, in.LID = conv, root, protocol.NewID()
		return in
	}
	for name, in := range map[string]envelope.Inner{
		"unnamed answer":             turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "forged", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm"}),
		"unnamed answer as a person": turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "forged", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginUI}),
		"unnamed progress":           turn(envelope.Inner{Kind: envelope.KindMessage, Status: envelope.StatusProgress, Body: "checking", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "claude", Emotion: "neutral"}),
		"agent origin alone":         turn(envelope.Inner{Kind: envelope.KindMessage, Body: "I am an agent", Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm"}),
	} {
		forged := craft(t, w.alice, w.bob, in)
		if err := w.bob.accept(tctx(t), forged); err != nil {
			t.Fatal(err)
		}
		if r := heldReason(t, w.bob, forged.ID); r != reasonInvalid || inboxCount(t, w.bob, `id = ?`, forged.ID) != 0 {
			t.Fatalf("%s from a member that is not the host: held %q, stored %d", name, r, inboxCount(t, w.bob, `id = ?`, forged.ID))
		}
	}

	// A turn the host sends while its participation is only invited waits
	// for the acceptance, then is admitted; after the end it is refused.
	p2, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second invite at bob", func() bool { return stateAt(t, w.bob, p2.PID).State == PartInvited })
	early := craft(t, w.bob, w.alice, turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "early", ReplyTo: q.ID, PID: p2.PID, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}))
	if err := w.alice.accept(tctx(t), early); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, w.alice, early.ID); r != reasonProof {
		t.Fatalf("an output before the host's acceptance: held %q", r)
	}
	if _, err := w.bob.AcceptParticipation(tctx(t), p2.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the waiting output admitted once accepted", func() bool { return inboxCount(t, w.alice, `id = ?`, early.ID) == 1 })
	if m, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.ID == early.ID }); !m.VerifiedAgent {
		t.Fatalf("the host's admitted output: %+v", m)
	}
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the dismissal", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	late := craft(t, w.bob, w.alice, turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "late", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}))
	if err := w.alice.accept(tctx(t), late); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, w.alice, late.ID); r != reasonInvalid || inboxCount(t, w.alice, `id = ?`, late.ID) != 0 {
		t.Fatalf("an output after the participation ended: held %q", r)
	}

	// Rows an older reader admitted are not marked from their origin or
	// shape: only the host's key marks them.
	legacy := []envelope.Inner{
		{ID: protocol.NewID(), From: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "old forged answer", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm"},
		{ID: protocol.NewID(), From: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "old origin claim", Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm"},
	}
	for _, in := range legacy {
		in = turn(in)
		in.V = envelope.Version2
		if _, err := w.bob.store.addConvInbox(in, w.alice.Self().Fingerprint(), "", false, nil); err != nil {
			t.Fatal(err)
		}
		if m, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.ID == in.ID }); n != 1 || m.VerifiedAgent || m.Origin == "" {
			t.Fatalf("an origin-only or non-host claim marked: %d %+v", n, m)
		}
	}

	// History is checked against the original key: a linked device of
	// alice takes the host's answer as the host's even after the end, and
	// holds the same shape claimed under alice's own key.
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	request := pendingLink(t, w.alice)
	if err = w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	eventually(t, "the host's answer as history on the new device", func() bool {
		m, n := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == ans.LID })
		return n == 1 && m.History && m.VerifiedAgent && m.From == w.bob.Address
	})
	_, raw := rootOf(t, w.alice, conv)
	item := HistoryItem{V: 1, From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: time.Now().Unix(),
		Kind: envelope.KindAnswer, Body: "forged in history", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm", At: time.Now().UnixMilli()}
	c, err := w.alice.historyCopy(phone.Self(), conv, raw, item)
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.accept(tctx(t), c.env); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, phone, c.env.ID); r != reasonInvalid || inboxCount(t, phone, `id = ?`, item.ID) != 0 {
		t.Fatalf("history of a non-host's answer: held %q", r)
	}
	if st.runs() != 1 {
		t.Fatalf("runs %d", st.runs())
	}
}
