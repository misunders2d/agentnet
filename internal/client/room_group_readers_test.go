package client

import (
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ROOM_V1 §2.4 and §3 in a group: a person guest of the room (a visitor
// outside the group, captured under its room scope) speaks in the group:
// a member reads its turn, and a member's turn captured for it; a turn
// under a guest scope whose group binding does not verify is held. The
// guest's exact host, holding the group's current context (as a visitor
// does), reads a member's turn captured for it, keeping no admission.
func TestRoomGroupTurns(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	dave := proofReader(t, w, "guest")
	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	for _, a := range []*Agent{w.alice, w.bob, carol, dave} {
		roomReader(t, a)
	}
	// Dave's device gets the group's current context as a visitor host does.
	visitor, err := w.alice.InviteAgent(tctx(t), conv, dave.Address, nil, nil, "context for a visitor")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the visitor's current context", func() bool {
		p, e := dave.Participation(visitor.PID)
		return e == nil && p.State == PartInvited && p.External
	})
	guest := roomInvite(t, w.alice, conv, dave, protocol.RoleHuman, 0)
	scope, accept := signedAs(w.alice, protocol.ScopeOf(guest, time.Now().Unix())), acceptOf(t, dave, guest)
	for _, a := range []*Agent{w.bob, dave} {
		if _, err := a.externalHostProof(tctx(t), guest.Host); err != nil {
			t.Fatal(err)
		}
	}
	storeEvents(t, w.bob, scope, accept)
	storeEvents(t, dave, guest, scope, accept)
	if p := stateAt(t, w.bob, guest.PID); !p.Following() || !p.External {
		t.Fatalf("the room guest at a member: %+v", p)
	}
	h := &envelope.HumanTurn{AuthorPID: guest.PID, Audience: []envelope.HumanScope{{PID: guest.PID, Invite: guest.Hash(), Decision: accept.Hash()}}, Proof: []protocol.ParticipationEvent{scope, accept}}
	raw, _ := json.Marshal(packet.Root)
	turn := func(author string) envelope.Inner {
		c := *h
		c.AuthorPID = author
		return envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Body: "group room turn " + protocol.NewID(), Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI, PID: author, Human: &c}
	}
	env := sealTo(t, dave, w.bob, turn(guest.PID))
	eventually(t, "a member reads the guest's turn", func() bool { return inboxHas(t, w.bob, env.ID) })
	env = sealTo(t, w.alice, w.bob, turn(""))
	eventually(t, "a member reads a member's captured turn", func() bool { return inboxHas(t, w.bob, env.ID) })
	env = sealTo(t, w.alice, dave, turn(""))
	eventually(t, "the guest's host reads a member's captured turn", func() bool { return inboxHas(t, dave, env.ID) })
	var stamp string
	dave.store.db.QueryRow(`SELECT coalesce(group_admission,'') FROM inbox WHERE id=?`, env.ID).Scan(&stamp)
	if stamp != "" {
		t.Fatalf("a following host kept an admission %q", stamp)
	}

	// A guest scope bound to a group state that is not the recorded one.
	forged := roomInvite(t, w.alice, conv, dave, protocol.RoleHuman, 0)
	forged.Group.Hash = strings.Repeat("e", 64)
	forged = signedAs(w.alice, forged)
	fs, fa := signedAs(w.alice, protocol.ScopeOf(forged, time.Now().Unix())), acceptOf(t, dave, forged)
	bad := turn(forged.PID)
	bad.Human = &envelope.HumanTurn{AuthorPID: forged.PID, Audience: []envelope.HumanScope{{PID: forged.PID, Invite: forged.Hash(), Decision: fa.Hash()}}, Proof: []protocol.ParticipationEvent{fs, fa}}
	env = sealTo(t, dave, w.bob, bad)
	eventually(t, "a turn under an unverified group binding held", func() bool { return quarantined(t, w.bob, env.ID) })
	if inboxHas(t, w.bob, env.ID) {
		t.Fatal("a guest's turn under an unverified group binding was read")
	}
}

// A group execution request with a human guest audience stays refused;
// assistant membership audiences use the captured-turn path (P6).
func TestRoomGroupRequestCarryingHumanHeld(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	dave := proofReader(t, w, "guest")
	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	for _, a := range []*Agent{w.alice, w.bob, dave} {
		roomReader(t, a)
	}
	visitor, err := w.alice.InviteAgent(tctx(t), conv, dave.Address, nil, nil, "visitor agent")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the visitor invited at its host", func() bool {
		p, e := dave.Participation(visitor.PID)
		return e == nil && p.State == PartInvited && p.External
	})
	if _, err := dave.AcceptParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the visitor active at its host", func() bool {
		p, e := dave.Participation(visitor.PID)
		return e == nil && p.Claimable()
	})
	m, err := w.alice.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(packet.Root)
	request := func(h *envelope.HumanTurn) envelope.Inner {
		return envelope.Inner{V: envelope.Version2, Kind: envelope.KindQuestion, Body: "q " + protocol.NewID(), Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI, PID: visitor.PID,
			Target: &envelope.Target{Address: dave.Address, Fingerprint: dave.Self().Fingerprint(), GroupAdmission: m.keyEpoch(w.alice.Self().Fingerprint())}, Human: h}
	}
	plain := sealTo(t, w.alice, dave, request(nil))
	eventually(t, "the plain request read at its host", func() bool { return inboxHas(t, dave, plain.ID) })

	// A captured audience for an invented guest, its signatures random.
	guest := protocol.NewID()
	host, _, _ := dave.store.selfPerson(dave.Address)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	scope := protocol.ParticipationEvent{V: 1, Conv: conv, PID: guest, Type: protocol.EventScope, Prev: strings.Repeat("ab", 32), TS: time.Now().Unix(),
		Author:   protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: w.alice.Address, Fingerprint: me.info.Fingerprint},
		Host:     &protocol.ParticipationHost{Person: host.info.Person, Address: dave.Address, Fingerprint: dave.Self().Fingerprint()},
		Audience: protocol.AudienceConversation, Role: protocol.RoleHuman, Sig: make([]byte, 64)}
	accept := protocol.ParticipationEvent{V: 1, Conv: conv, PID: guest, Type: protocol.EventAccept, Prev: scope.Prev, TS: time.Now().Unix(),
		Author: protocol.EventAuthor{Person: host.info.Person, Roster: host.info.Roster, Address: dave.Address, Fingerprint: dave.Self().Fingerprint()}, Sig: make([]byte, 64)}
	rand.Read(scope.Sig)
	rand.Read(accept.Sig)
	h := &envelope.HumanTurn{Audience: []envelope.HumanScope{{PID: guest, Invite: scope.Prev, Decision: accept.Hash()}}, Proof: []protocol.ParticipationEvent{scope, accept}}
	if err := h.Validate(conv); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	env := sealTo(t, w.alice, dave, request(h))
	eventually(t, "the request carrying an audience decided", func() bool { return inboxHas(t, dave, env.ID) || quarantined(t, dave, env.ID) })
	if inboxHas(t, dave, env.ID) {
		t.Fatal("a group request carrying a captured audience was read")
	}

	// The same request as history from an own device is not read either.
	item := itemOf(request(h), w.alice.Self().Fingerprint(), time.Now().UnixMilli())
	item.ID, item.From, item.TS = protocol.NewID(), w.alice.Address, time.Now().Unix()
	item.GroupAdmission = m.keyEpoch(w.alice.Self().Fingerprint())
	body, _ := json.Marshal(item)
	history := envelope.Inner{V: envelope.Version2, Kind: envelope.KindMessage, Sub: envelope.SubHistory, Conv: conv, Root: raw, LID: protocol.NewID(), Replica: true, Body: string(body)}
	var why string
	err = w.alice.admitGroupHistory(tctx(t), envelope.Envelope{ID: protocol.NewID(), From: w.alice.Address}, history, packet.Root, w.alice.Self(), false,
		func(reason, s string) error { why = reason + ": " + s; return nil })
	if inboxHas(t, w.alice, item.ID) {
		t.Fatal("history of a group request carrying a captured audience was read")
	}
	if err != nil || why != reasonInvalid+": group human guest execution audience is not enabled" {
		t.Fatalf("history carrying an audience: %q %v", why, err)
	}
}
