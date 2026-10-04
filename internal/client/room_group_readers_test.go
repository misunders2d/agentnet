package client

import (
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
