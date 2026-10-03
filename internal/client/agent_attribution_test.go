package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
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

	// The key half: the host's address under any other key (a re-keyed
	// device, or a key that only claims the address) is not the host, live
	// or as history, and the view marks only the exact key, as received or
	// as the original key of history.
	rekey, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rekeyed, hostFP := rekey.Public(w.bob.Address), w.bob.Self().Fingerprint()
	unnamed := envelope.Inner{Conv: conv, Kind: envelope.KindAnswer, Body: "re-keyed", ReplyTo: q.ID, PID: pid, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}
	for _, historical := range []bool{false, true} {
		if r, err := w.alice.checkConversationAgent(unnamed, rekeyed, historical); r != reasonInvalid || err == nil {
			t.Fatalf("the host's address under another key (history %v): held %q, %v", historical, r, err)
		}
		if r, err := w.alice.checkConversationAgent(unnamed, w.bob.Self(), historical); r != "" || err != nil {
			t.Fatalf("the host's exact key (history %v): held %q, %v", historical, r, err)
		}
	}
	rows := []ConvMessage{
		{Dir: "in", From: w.bob.Address, Key: rekeyed.Fingerprint(), Kind: envelope.KindAnswer, ReplyTo: q.ID, PID: pid},
		{Dir: "in", From: w.bob.Address, History: true, Claimed: rekeyed.Fingerprint(), Kind: envelope.KindAnswer, ReplyTo: q.ID, PID: pid},
		{Dir: "in", From: w.bob.Address, Key: hostFP, Kind: envelope.KindAnswer, ReplyTo: q.ID, PID: pid},
		{Dir: "in", From: w.bob.Address, History: true, Claimed: hostFP, Kind: envelope.KindAnswer, ReplyTo: q.ID, PID: pid},
	}
	w.alice.verifyAgents(conv, rows)
	for i, want := range []bool{false, false, true, true} {
		if rows[i].VerifiedAgent != want {
			t.Fatalf("view row %d (key %q, history key %q): verified %v", i, rows[i].Key, rows[i].Claimed, rows[i].VerifiedAgent)
		}
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

	// Ended by its host's decline: refused as well.
	p3, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the third invite at bob", func() bool { return stateAt(t, w.bob, p3.PID).State == PartInvited })
	if _, err := w.bob.DeclineParticipation(tctx(t), p3.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to see the decline", func() bool { return stateAt(t, w.alice, p3.PID).State == PartDeclined })
	declined := craft(t, w.bob, w.alice, turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "declined", ReplyTo: q.ID, PID: p3.PID, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}))
	if err := w.alice.accept(tctx(t), declined); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, w.alice, declined.ID); r != reasonInvalid || inboxCount(t, w.alice, `id = ?`, declined.ID) != 0 {
		t.Fatalf("an output after its host declined: held %q", r)
	}

	// Active, but with a record held here that does not count (one that
	// may yet end or change it): its output waits.
	stray := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p2.PID, Type: protocol.EventDismiss, Prev: strings.Repeat("ab", 32), TS: time.Now().Unix(), Author: stateAuthor(t, w.alice)}
	stray.Sign(w.alice.id.Sign)
	strayRaw, _ := json.Marshal(stray)
	if err := w.alice.store.addParticipationEvent(stray, strayRaw); err != nil {
		t.Fatal(err)
	}
	if p := stateAt(t, w.alice, p2.PID); p.State != PartActive || p.Held != 1 {
		t.Fatalf("an active participation with a record held: %s held %d", p.State, p.Held)
	}
	waiting := craft(t, w.bob, w.alice, turn(envelope.Inner{Kind: envelope.KindAnswer, Body: "while held", ReplyTo: q.ID, PID: p2.PID, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"}))
	if err := w.alice.accept(tctx(t), waiting); err != nil {
		t.Fatal(err)
	}
	if r := heldReason(t, w.alice, waiting.ID); r != reasonProof || inboxCount(t, w.alice, `id = ?`, waiting.ID) != 0 {
		t.Fatalf("an output while a record of its participation is held: held %q", r)
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

// A human guest's participation has no agent: a turn shaped as an agent's
// under it is refused even from its exact host, live or as history, and the
// view never marks one.
func TestAgentTurnUnderAHumanParticipationIsRefused(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "carol to see the invite", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	if _, err := carol.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to see carol active", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
	in := envelope.Inner{Conv: conv, Kind: envelope.KindAnswer, Body: "as an agent", ReplyTo: protocol.NewID(), PID: p.PID, Origin: envelope.OriginAgentPrefix + "claude", Emotion: "calm"}
	for _, historical := range []bool{false, true} {
		if r, err := w.alice.checkConversationAgent(in, carol.Self(), historical); r != reasonInvalid || err == nil {
			t.Fatalf("an agent's turn under a human participation, from its host (history %v): held %q, %v", historical, r, err)
		}
	}
	fp := carol.Self().Fingerprint()
	rows := []ConvMessage{
		{Dir: "in", From: carol.Address, Key: fp, Kind: envelope.KindAnswer, ReplyTo: in.ReplyTo, PID: p.PID, Origin: in.Origin},
		{Dir: "in", From: carol.Address, History: true, Claimed: fp, Kind: envelope.KindAnswer, ReplyTo: in.ReplyTo, PID: p.PID, Origin: in.Origin},
	}
	w.alice.verifyAgents(conv, rows)
	for i, m := range rows {
		if m.VerifiedAgent {
			t.Fatalf("view row %d under a human participation marked as an agent's", i)
		}
	}
}
