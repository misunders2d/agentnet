package client

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// capturedOf is pid's audience entry and proof (its counted scope and
// accept) as a holds them.
func capturedOf(t *testing.T, a *Agent, pid string) (envelope.HumanScope, []protocol.ParticipationEvent) {
	t.Helper()
	p, err := a.Participation(pid)
	if err != nil || p.Scope == "" || p.Decision == "" {
		t.Fatalf("%s at %s: %+v %v", pid, a.Address, p, err)
	}
	events, err := a.store.participationEvents(p.Conv, pid)
	if err != nil {
		t.Fatal(err)
	}
	var proof []protocol.ParticipationEvent
	for _, h := range []string{p.Scope, p.Decision} {
		for _, e := range events {
			if e.Hash() == h {
				proof = append(proof, e)
			}
		}
	}
	return envelope.HumanScope{PID: pid, Invite: p.Invite, Decision: p.Decision}, proof
}

// captured is a HumanTurn by author over the participations pids, as a
// holds them.
func captured(t *testing.T, a *Agent, author string, pids ...string) *envelope.HumanTurn {
	t.Helper()
	h := &envelope.HumanTurn{AuthorPID: author}
	for _, pid := range pids {
		s, proof := capturedOf(t, a, pid)
		h.Audience, h.Proof = append(h.Audience, s), append(h.Proof, proof...)
	}
	return h
}

// sealTo seals in from from to to's device and hands it to the relay, as a
// later sender would (no client sends these shapes yet, ROOM_V1 P2+).
func sealTo(t *testing.T, from, to *Agent, in envelope.Inner) envelope.Envelope {
	t.Helper()
	key, err := from.sendKey(tctx(t), to.Address)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := key.Recipient()
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.From, in.To, in.TS = protocol.NewID(), from.Address, to.Address, time.Now().Unix()
	env, err := envelope.Seal(in, from.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err := from.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
		t.Fatal(err)
	}
	return env
}

func inboxHas(t *testing.T, a *Agent, id string) bool { return inboxCount(t, a, "id = ?", id) == 1 }

// roomTurn is a DM turn of conv as a holds its root.
func roomTurn(t *testing.T, a *Agent, conv string, h *envelope.HumanTurn) envelope.Inner {
	t.Helper()
	_, raw := rootOf(t, a, conv)
	return envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Body: "room turn " + protocol.NewID(), Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI, Human: h}
}

// roomDM: the guestAssistant world (Bob's assistant ap, Carol an accepted
// person guest hp) where Alice also brought Carol's agent in as a follower
// of the room (inv: no role, audience room), active at both members.
func roomDM(t *testing.T) (w *world, carol *Agent, conv string, ap, hp ParticipationInfo, inv protocol.ParticipationEvent) {
	t.Helper()
	w, carol, conv, _, _, ap, hp = guestAssistant(t)
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		roomReader(t, a)
	}
	inv = roomInvite(t, w.alice, conv, carol, "", 0)
	if _, err := w.alice.externalHostProof(tctx(t), inv.Host); err != nil {
		t.Fatal(err)
	}
	sendEvent(t, w.alice, conv, inv)
	sendEvent(t, w.alice, conv, signedAs(w.alice, protocol.ScopeOf(inv, time.Now().Unix())))
	eventually(t, "the follower invited at its host", func() bool { return stateAt(t, carol, inv.PID).State == PartInvited })
	if _, err := carol.AcceptParticipation(tctx(t), inv.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follower active at both members", func() bool {
		return stateAt(t, w.alice, inv.PID).Following() && stateAt(t, w.bob, inv.PID).Following()
	})
	return
}

// ROOM_V1 §3 receivers in a DM: an agent room participant (a follower) is
// of the captured audience: its exact host reads a member's turn captured
// for it while it follows, and not once it ended.
func TestRoomFollowerReadsInADM(t *testing.T) {
	w, carol, conv, _, _, inv := roomDM(t)
	turn := roomTurn(t, w.alice, conv, captured(t, w.alice, "", inv.PID))
	env := sealTo(t, w.alice, carol, turn)
	eventually(t, "the follower's host reads it", func() bool { return inboxHas(t, carol, env.ID) })
	if _, err := w.alice.DismissParticipation(tctx(t), inv.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follower ended at its host", func() bool { return stateAt(t, carol, inv.PID).State == PartDismissed })
	late := roomTurn(t, w.alice, conv, turn.Human)
	env = sealTo(t, w.alice, carol, late)
	eventually(t, "a turn for an ended follower held", func() bool { return quarantined(t, carol, env.ID) })
	if inboxHas(t, carol, env.ID) {
		t.Fatal("an ended follower's host read a turn captured for it")
	}
}

// ROOM_V1 §2.3 and §3 in a DM: a follower asks another agent only as itself
// (an agent origin, from its exact host), which members read; a person
// guest's turn under a scope that says it is an agent (a second scope its
// inviter signed) disagrees with the guest's counted invitation and is
// refused.
func TestRoomAgentAsksInADM(t *testing.T) {
	w, carol, conv, ap, hp, inv := roomDM(t)
	ask := roomTurn(t, carol, conv, captured(t, carol, inv.PID, hp.PID, inv.PID))
	ask.Kind, ask.PID, ask.Origin, ask.Emotion = envelope.KindQuestion, ap.PID, "agent:claude", "curious"
	ask.Target = &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}
	env := sealTo(t, carol, w.alice, ask)
	eventually(t, "a member reads the follower's ask", func() bool { return inboxHas(t, w.alice, env.ID) })

	_, hproof := capturedOf(t, w.alice, hp.PID)
	var hinv protocol.ParticipationEvent
	events, _ := w.alice.store.participationEvents(conv, hp.PID)
	for _, e := range events {
		if e.Type == protocol.EventInvite {
			hinv = e
		}
	}
	bogus := protocol.ScopeOf(hinv, time.Now().Unix())
	bogus.Role, bogus.Audience = "", protocol.AudienceRoom
	bogus = signedAs(w.alice, bogus)
	forged := ask
	forged.LID = protocol.NewID()
	forged.Human = &envelope.HumanTurn{AuthorPID: hp.PID, Audience: []envelope.HumanScope{{PID: hp.PID, Invite: hinv.Hash(), Decision: hproof[1].Hash()}}, Proof: []protocol.ParticipationEvent{bogus, hproof[1]}}
	env = sealTo(t, carol, w.bob, forged)
	eventually(t, "a guest's ask labelled as an agent's held", func() bool { return quarantined(t, w.bob, env.ID) })
	if inboxHas(t, w.bob, env.ID) {
		t.Fatal("a guest's turn was read as an agent's")
	}
}

// ROOM_V1 §2.3 in a DM: an edit carrying its turn's captured audience is
// read only from that turn's author: the key that sent it, under the same
// author scope, to no wider audience.
func TestRoomEditsInADM(t *testing.T) {
	w, carol, conv, _, hp, inv := roomDM(t)
	sent, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "a guest's line"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the guest's turn at Bob", func() bool { return humanBodyCount(t, w.bob, conv, "a guest's line") == 1 })
	edit := func(h *envelope.HumanTurn, key string) envelope.Inner {
		return envelope.Inner{V: envelope.Version3, Kind: envelope.KindMessage, Sub: envelope.SubRevision, Body: `{"rev":1,"text":"a guest's corrected line"}`,
			Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: sent.LID, Fingerprint: key}, Human: h}
	}
	for what, c := range map[string]struct {
		from *Agent
		in   envelope.Inner
	}{
		"another key":        {w.alice, edit(captured(t, w.alice, hp.PID, hp.PID), carol.Self().Fingerprint())},
		"another author":     {carol, edit(captured(t, carol, "", hp.PID), carol.Self().Fingerprint())},
		"a broader audience": {carol, edit(captured(t, carol, hp.PID, hp.PID, inv.PID), carol.Self().Fingerprint())},
	} {
		env := sealTo(t, c.from, w.bob, c.in)
		eventually(t, "an edit from "+what+" held", func() bool { return quarantined(t, w.bob, env.ID) })
	}
	env := sealTo(t, carol, w.bob, edit(captured(t, carol, hp.PID, hp.PID), carol.Self().Fingerprint()))
	eventually(t, "the guest's own edit read", func() bool { return inboxHas(t, w.bob, env.ID) })
}

// ROOM_V1 §3 in a DM: a follower's exact host reads another participation's
// public lifecycle (its scope and acceptance) that a member shares with it,
// as an accepted guest does (humanEndReader); nobody else's device does.
func TestRoomFollowerReadsSharedLifecycle(t *testing.T) {
	w, carol, conv, _, _, _, hp := guestAssistant(t)
	dave := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "follower")
	runAgent(t, dave)
	persons(t, dave)
	for _, a := range []*Agent{w.alice, w.bob, carol, dave} {
		roomReader(t, a)
	}
	inv := roomInvite(t, w.alice, conv, dave, "", 0)
	if _, err := w.alice.externalHostProof(tctx(t), inv.Host); err != nil {
		t.Fatal(err)
	}
	sendEvent(t, w.alice, conv, inv)
	sendEvent(t, w.alice, conv, signedAs(w.alice, protocol.ScopeOf(inv, time.Now().Unix())))
	eventually(t, "the follower invited at its host", func() bool { return stateAt(t, dave, inv.PID).State == PartInvited })
	if _, err := dave.AcceptParticipation(tctx(t), inv.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follower active at Alice", func() bool { return stateAt(t, w.alice, inv.PID).Following() })
	_, raw := rootOf(t, w.alice, conv)
	_, proof := capturedOf(t, w.alice, hp.PID)
	for _, ev := range proof { // the guest's scope (Alice's), then its acceptance (Carol's)
		body, _ := json.Marshal(ev)
		env := sealTo(t, w.alice, dave, envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Body: string(body), Conv: conv, LID: protocol.NewID(), Root: raw, PID: hp.PID, Sub: envelope.SubEvent, Origin: envelope.OriginUI})
		eventually(t, "the follower's host reads the shared "+ev.Type, func() bool { return inboxHas(t, dave, env.ID) })
	}
	if p := stateAt(t, dave, hp.PID); p.Scope == "" || p.Decision == "" || !p.HumanActive() {
		t.Fatalf("the shared guest at the follower's host: %+v", p)
	}
}
