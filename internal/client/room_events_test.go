package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// roomReader signs rm1 for a's live sessions (ROOM_V1 §2.1), as a current
// program does once it advertises it, so room copies may reach it.
func roomReader(t *testing.T, a *Agent) {
	t.Helper()
	signCapsAfter(t, a, withCap(ownCaps, protocol.CapRoom))
}

// signCapsAfter replaces a's live sessions' signed capabilities with caps:
// at the current time after the session's own record (signCapsNow), or
// right after a record an earlier fixture (humanTestCaps,
// publishGroupFixtureCaps) dated ahead of the clock.
func signCapsAfter(t *testing.T, a *Agent, caps []string) {
	t.Helper()
	waitNamedAgentCaps(t, a)
	label, name, _ := protocol.SplitAddress(a.Address)
	var prof protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		t.Fatal(err)
	}
	var last int64
	for _, raw := range prof.Caps {
		if r, err := protocol.ParseCapsRecord(raw); err == nil && r.TS > last {
			last = r.TS
		}
	}
	if last > time.Now().Unix()+1 {
		for _, session := range prof.Sessions {
			rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: last + 1}
			rec.Sign(a.id.Sign)
			if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	signCapsNow(t, a, caps)
}

// roomInvite is a's signed room invitation in conv for an agent (role "")
// or a person guest (protocol.RoleHuman) hosted on host's device; in a
// group it is bound to a's admission as inviteParticipation binds one. No
// client makes these yet (ROOM_V1 P2): the test stands for a later sender.
func roomInvite(t *testing.T, a *Agent, conv string, host *Agent, role string, until int64) protocol.ParticipationEvent {
	t.Helper()
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	hp, _, err := host.store.selfPerson(host.Address)
	if err != nil {
		t.Fatal(err)
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author:   protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint},
		Host:     &protocol.ParticipationHost{Person: hp.info.Person, Address: host.Address, Fingerprint: host.Self().Fingerprint()},
		Audience: protocol.AudienceRoom, Role: role, Until: until}
	m, err := a.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	if m.group != nil {
		if err := m.bindGroupInvite(&ev); err != nil {
			t.Fatal(err)
		}
	}
	ev.Sign(a.id.Sign)
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
	return ev
}

// signedAs is ev signed by a (its author or host device).
func signedAs(a *Agent, ev protocol.ParticipationEvent) protocol.ParticipationEvent {
	ev.Sign(a.id.Sign)
	return ev
}

// acceptOf is host's signed accept of invite inv, as decide signs one: a
// group member host names its admission.
func acceptOf(t *testing.T, host *Agent, inv protocol.ParticipationEvent) protocol.ParticipationEvent {
	t.Helper()
	me, _, err := host.store.selfPerson(host.Address)
	if err != nil {
		t.Fatal(err)
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: inv.Conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: host.Address, Fingerprint: me.info.Fingerprint}}
	if inv.Group != nil && inv.Group.HostRole == "member" {
		ev.Author.GroupAdmission = inv.Group.HostAdmission
	}
	return signedAs(host, ev)
}

// sendEvent stores ev at a and sends it in conv, as recordAndSend does its
// own.
func sendEvent(t *testing.T, a *Agent, conv string, ev protocol.ParticipationEvent) ConvSent {
	t.Helper()
	storeEvents(t, a, ev)
	raw, _ := json.Marshal(ev)
	sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, Body: string(raw), PID: ev.PID, sub: envelope.SubEvent})
	if err != nil {
		t.Fatal(err)
	}
	return sent
}

func storeEvents(t *testing.T, a *Agent, events ...protocol.ParticipationEvent) {
	t.Helper()
	for _, ev := range events {
		raw, _ := json.Marshal(ev)
		if err := a.store.addParticipationEventWith(ev, raw, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// ROOM_V1 §2.2 in a DM: a room invitation and its exact scope are read and
// counted; the participation carries its audience and end time and
// follows the room once accepted; a scope that disagrees with the invite
// it names (on the end time, here) is held, never counted; scopes standing
// for an invitation not held must agree on audience, group and end time;
// past its end time, by this device's clock, it counts as ended.
func TestRoomEventsInADM(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	roomReader(t, w.alice)
	roomReader(t, w.bob)
	until := time.Now().Unix() + 3600
	inv := roomInvite(t, w.alice, conv, w.bob, "", until)
	sendEvent(t, w.alice, conv, inv)
	eventually(t, "the room invitation at its host", func() bool {
		p := stateAt(t, w.bob, inv.PID)
		return p.State == PartInvited && p.HostHere && p.Audience == protocol.AudienceRoom && p.Until == until
	})
	scope := signedAs(w.alice, protocol.ScopeOf(inv, time.Now().Unix()))
	sendEvent(t, w.alice, conv, scope)
	eventually(t, "its scope counted", func() bool { return stateAt(t, w.bob, inv.PID).Scope == scope.Hash() })
	other := protocol.ScopeOf(inv, time.Now().Unix())
	other.Until++
	other = signedAs(w.alice, other)
	held := sendEvent(t, w.alice, conv, other)
	var copyToBob string
	for _, c := range held.Copies {
		if c.To == w.bob.Address {
			copyToBob = c.ID
		}
	}
	eventually(t, "a disagreeing scope held", func() bool { return quarantined(t, w.bob, copyToBob) })
	if events, _ := w.bob.store.participationEvents(conv, inv.PID); len(events) != 2 {
		t.Fatalf("a disagreeing scope was stored: %d events", len(events))
	}
	if _, err := w.bob.AcceptParticipation(tctx(t), inv.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follower active at both", func() bool {
		a, b := stateAt(t, w.alice, inv.PID), stateAt(t, w.bob, inv.PID)
		return a.Following() && b.Following() && a.Claimable() && !a.HumanActive()
	})

	// Scopes standing for an invitation not held: equal ones count, ones that
	// disagree on audience, group or end time are a conflict.
	lone := roomInvite(t, w.alice, conv, w.bob, "", until)
	first := signedAs(w.alice, protocol.ScopeOf(lone, time.Now().Unix()))
	again := signedAs(w.alice, protocol.ScopeOf(lone, time.Now().Unix()+1))
	storeEvents(t, w.bob, first, again)
	if p := stateAt(t, w.bob, lone.PID); p.State != PartInvited || p.Audience != protocol.AudienceRoom || p.Until != until {
		t.Fatalf("agreeing scopes: %+v", p)
	}
	for what, change := range map[string]func(*protocol.ParticipationEvent){
		"audience": func(e *protocol.ParticipationEvent) { e.Audience, e.Until = protocol.AudienceConversation, 0 },
		"until":    func(e *protocol.ParticipationEvent) { e.Until = until + 1 },
	} {
		lone := roomInvite(t, w.alice, conv, w.bob, "", until)
		x := protocol.ScopeOf(lone, time.Now().Unix())
		change(&x)
		storeEvents(t, w.bob, signedAs(w.alice, protocol.ScopeOf(lone, time.Now().Unix())), signedAs(w.alice, x))
		if p := stateAt(t, w.bob, lone.PID); p.State != PartConflict {
			t.Errorf("scopes disagreeing on %s: %+v", what, p)
		}
	}

	// Past its end time it counts as ended here, and follows no more.
	past := roomInvite(t, w.alice, conv, w.bob, "", time.Now().Unix()-1)
	storeEvents(t, w.bob, past, acceptOf(t, w.bob, past))
	if p := stateAt(t, w.bob, past.PID); p.State != PartDismissed || p.Following() || p.Claimable() || p.Dismissal != "" {
		t.Fatalf("past its end time: %+v", p)
	}
}

// ROOM_V1 §2.2 in a group: a room scope carries its invitation's group
// binding and counts at a member who never held the invitation once that
// binding verifies (verifyInviteEpoch); with a binding that does not, it
// does not count. A person guest of a group is a room visitor.
func TestRoomEventsGroupScopeCounts(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	guestHost := proofReader(t, w, "guest")
	inv := roomInvite(t, w.alice, conv, w.bob, "", 0)
	if inv.Group == nil || inv.Group.HostRole != "member" {
		t.Fatalf("group binding %+v", inv.Group)
	}
	scope := signedAs(w.alice, protocol.ScopeOf(inv, time.Now().Unix()))
	storeEvents(t, carol, scope, acceptOf(t, w.bob, inv))
	if p := stateAt(t, carol, inv.PID); p.State != PartActive || p.Scope != scope.Hash() || p.Audience != protocol.AudienceRoom || !p.Following() || p.External {
		t.Fatalf("a verified room scope at a member who never held the invite: %+v", p)
	}
	// The same scope bound to a group state that is not the one recorded.
	forged := roomInvite(t, w.alice, conv, w.bob, "", 0)
	forged.Group.Hash = strings.Repeat("e", 64)
	forged = signedAs(w.alice, forged)
	storeEvents(t, carol, signedAs(w.alice, protocol.ScopeOf(forged, time.Now().Unix())), acceptOf(t, w.bob, forged))
	if p := stateAt(t, carol, forged.PID); p.Invite != "" || p.State == PartActive || p.Held == 0 {
		t.Fatalf("an unverified group binding counted: %+v", p)
	}
	// A person guest (a visitor outside the group) as a room participant.
	guest := roomInvite(t, w.alice, conv, guestHost, protocol.RoleHuman, 0)
	if guest.Group == nil || guest.Group.HostRole != "visitor" {
		t.Fatalf("guest binding %+v", guest.Group)
	}
	if _, err := carol.externalHostProof(tctx(t), guest.Host); err != nil { // the guest's person pinned here, as admission pins it
		t.Fatal(err)
	}
	gs := signedAs(w.alice, protocol.ScopeOf(guest, time.Now().Unix()))
	storeEvents(t, carol, gs, acceptOf(t, guestHost, guest))
	if p := stateAt(t, carol, guest.PID); p.State != PartActive || !p.HumanActive() || !p.Following() || !p.External {
		t.Fatalf("a group person guest at a member: %+v", p)
	}
}
