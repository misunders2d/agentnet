package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// withoutRoom signs a's live sessions' capabilities without rm1, as a
// program of before ROOM_V1 P1 (or an older session) advertises them.
func withoutRoom(t *testing.T, a *Agent) {
	t.Helper()
	signCapsAfter(t, a, without(withCap(ownCaps, protocol.CapRoom), protocol.CapRoom))
}

func copyTo(t *testing.T, sent ConvSent, to string) string {
	t.Helper()
	for _, c := range sent.Copies {
		if c.To == to {
			return c.ID
		}
	}
	t.Fatalf("no copy to %s in %+v", to, sent)
	return ""
}

func roomOutboxState(t *testing.T, a *Agent, id string) (string, string) {
	t.Helper()
	var state, why string
	if err := a.store.db.QueryRow(`SELECT state, coalesce(error,'') FROM outbox WHERE id=?`, id).Scan(&state, &why); err != nil {
		t.Fatal(err)
	}
	return state, why
}

// ROOM_V1 §2.1: this program advertises rm1 (it enforces every room
// reader rule), within the 16 names a device lists so readers keep their
// parse headroom; the list is sorted and unique, as a signed record needs.
func TestOwnCapsAdvertiseRoom(t *testing.T) {
	if !slices.Contains(ownCaps, protocol.CapRoom) || len(ownCaps) > protocol.MaxAdvertisedCaps || !slices.IsSorted(ownCaps) || len(slices.Compact(slices.Clone(ownCaps))) != len(ownCaps) {
		t.Fatalf("own capabilities %v", ownCaps)
	}
	rec := protocol.CapsRecord{Address: "fixture/desk", Session: protocol.NewID(), Caps: ownCaps, TS: 1}
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
}

// ROOM_V1 §2.5 in a DM: a room shape keeps its primary requirement (hgp1
// for a DM's room event) and needs rm1 besides it; a device without rm1
// keeps it waiting, and gets it once its sessions read rm1, while a copy of
// the same primary requirement that is no room shape is delivered at once.
func TestRoomCopiesWaitForRM1(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	withoutRoom(t, w.bob)
	legacy := roomInvite(t, w.alice, conv, w.bob, protocol.RoleHuman, 0)
	legacy.Audience = protocol.AudienceConversation
	legacy = signedAs(w.alice, legacy)
	plain := sendEvent(t, w.alice, conv, legacy)
	room := roomInvite(t, w.alice, conv, w.bob, "", 0)
	sent := sendEvent(t, w.alice, conv, room)
	plainID, roomID := copyTo(t, plain, w.bob.Address), copyTo(t, sent, w.bob.Address)
	for _, id := range []string{plainID, roomID} {
		var required string
		w.alice.store.db.QueryRow(`SELECT coalesce(required_cap,'') FROM outbox WHERE id=?`, id).Scan(&required)
		if required != protocol.CapHumanParticipation {
			t.Fatalf("copy %s keeps primary %q, want hgp1", id, required)
		}
	}
	eventually(t, "the hgp1 copy that is no room shape delivered", func() bool { return inboxHas(t, w.bob, plainID) })
	eventually(t, "the room copy waiting for rm1", func() bool {
		state, why := roomOutboxState(t, w.alice, roomID)
		return state == stateConvWaiting && strings.Contains(why, "room participation")
	})
	if inboxHas(t, w.bob, roomID) {
		t.Fatal("a room copy reached a device without rm1")
	}
	roomReader(t, w.bob)
	eventually(t, "the room copy released once its reader reads rm1", func() bool { return inboxHas(t, w.bob, roomID) })
	if p := stateAt(t, w.bob, room.PID); p.State != PartInvited || p.Audience != protocol.AudienceRoom {
		t.Fatalf("the room invitation at its reader: %+v", p)
	}
}

// ROOM_V1 §2.5 in a group: an ordinary group copy (grp1) still reaches a
// member without rm1, while a group copy carrying a captured audience
// keeps grp1 as its primary and waits for rm1.
func TestRoomGroupCopiesWaitForRM1(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	withoutRoom(t, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "an ordinary group turn"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ordinary group copy delivered", func() bool { return inboxHas(t, w.bob, copyTo(t, sent, w.bob.Address)) })

	// A member's group turn captured for a room guest, as a later sender
	// stores it (ROOM_V1 P4): one copy for Bob.
	guestHost := proofReader(t, w, "guest")
	guest := roomInvite(t, w.alice, conv, guestHost, protocol.RoleHuman, 0)
	scope, accept := signedAs(w.alice, protocol.ScopeOf(guest, time.Now().Unix())), acceptOf(t, guestHost, guest)
	h := &envelope.HumanTurn{Audience: []envelope.HumanScope{{PID: guest.PID, Invite: guest.Hash(), Decision: accept.Hash()}}, Proof: []protocol.ParticipationEvent{scope, accept}}
	raw, _ := json.Marshal(packet.Root)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "captured for the room",
		Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI, Human: h}
	if agentRequirement(in) != protocol.CapGroup {
		t.Fatalf("a group's captured turn has primary %q, want grp1", agentRequirement(in))
	}
	key, err := w.alice.sendKey(tctx(t), w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := key.Recipient()
	env, err := envelope.Seal(in, w.alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := groupMemberAdmission(w.alice.store.db, packet, w.alice.Address, w.alice.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addConvOutbox([]outCopy{{in: in, env: env, state: stateQueued, required: protocol.CapGroup, recipientFP: key.Fingerprint(), groupAdmission: admission.Hash()}}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if r, err := w.alice.deliver(tctx(t), env, nil); err != nil || r.State != stateConvWaiting || !strings.Contains(r.Detail, "room participation") {
		t.Fatalf("a group room copy to a member without rm1: %+v %v", r, err)
	}
	if inboxHas(t, w.bob, env.ID) {
		t.Fatal("a group room copy reached a member without rm1")
	}
	_ = carol
}

// agentRequirement keeps each room shape's primary requirement (ROOM_V1
// §2.5), before and after a history unwrap; roomCopy names the room shapes
// from the columns a copy is stored with.
func TestRoomRequirementAndRoomCopy(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	room := roomInvite(t, w.alice, conv, w.bob, "", 0)
	named := room
	named.Host = &protocol.ParticipationHost{Person: room.Host.Person, Address: room.Host.Address, Fingerprint: room.Host.Fingerprint, AgentID: protocol.NewID()}
	named = signedAs(w.alice, named)
	body := func(e protocol.ParticipationEvent) string { raw, _ := json.Marshal(e); return string(raw) }
	history := func(in envelope.Inner) string {
		raw, _ := json.Marshal(itemOf(in, w.alice.Self().Fingerprint(), 1))
		return string(raw)
	}
	for what, c := range map[string]struct {
		in   envelope.Inner
		want string
	}{
		"a room invitation":            {envelope.Inner{Sub: envelope.SubEvent, PID: room.PID, Body: body(room)}, protocol.CapHumanParticipation},
		"a named agent's room invite":  {envelope.Inner{Sub: envelope.SubEvent, PID: named.PID, Body: body(named)}, protocol.CapHumanParticipation},
		"a room scope":                 {envelope.Inner{Sub: envelope.SubEvent, PID: room.PID, Body: body(protocol.ScopeOf(room, 1))}, protocol.CapHumanParticipation},
		"a room invitation as history": {envelope.Inner{Sub: envelope.SubHistory, Body: history(envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), From: w.alice.Address, Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: room.PID, Body: body(room)})}, protocol.CapHumanParticipation},
	} {
		if got := agentRequirement(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", what, got, c.want)
		}
	}
	storeEvents(t, w.alice, room, acceptOf(t, w.bob, room))
	accept := acceptOf(t, w.bob, room)
	for what, c := range map[string]struct {
		sub, body, human string
		want             bool
	}{
		"a room invitation":             {envelope.SubEvent, body(room), "", true},
		"a room participation's accept": {envelope.SubEvent, body(accept), "", true},
		"a conversation invitation": {envelope.SubEvent, body(func() protocol.ParticipationEvent {
			e := room
			e.Audience = protocol.AudienceConversation
			e.PID = protocol.NewID()
			return e
		}()), "", false},
		"a room invitation as history":      {envelope.SubHistory, history(envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: room.PID, Body: body(room)}), "", true},
		"a turn captured with a room scope": {"", "hi", humanJSON(&envelope.HumanTurn{Proof: []protocol.ParticipationEvent{protocol.ScopeOf(room, 1)}}), true},
		"a turn captured for DM guests only": {"", "hi", humanJSON(&envelope.HumanTurn{Proof: []protocol.ParticipationEvent{func() protocol.ParticipationEvent {
			s := protocol.ScopeOf(room, 1)
			s.Audience, s.Role = protocol.AudienceConversation, protocol.RoleHuman
			return s
		}()}}), false},
		"an edit carrying a captured audience": {envelope.SubRevision, `{"rev":1,"text":"x"}`, humanJSON(&envelope.HumanTurn{}), true},
		"an ordinary turn":                     {"", "hi", "", false},
	} {
		got, err := roomCopy(w.alice.store.db, conv, c.sub, c.body, c.human)
		if err != nil || got != c.want {
			t.Errorf("%s: room %v (%v), want %v", what, got, err, c.want)
		}
	}
}
