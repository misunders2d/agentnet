package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The native API must distinguish a visitor from a permanent group member
// throughout the public guest lifecycle. A non-admin can invite and end visits.
func TestLiveGroupGuestPermissions(t *testing.T) {
	alice, bob, guest, _, eventually, _ := liveGuestWorld(t)
	ctx := context.Background()
	conv := liveTwoMemberGroup(t, alice, bob)
	var err error
	memberLive, guestLive := NewLive(bob), NewLive(guest)
	var invite client.ParticipationInfo
	eventually("non-admin group member invites guest", func() bool {
		invite, err = bob.InviteHuman(ctx, conv, guest.Address, nil, "help with delivery")
		return err == nil
	})
	eventually("guest's pending invitation", func() bool {
		p, e := guest.Participation(invite.PID)
		return e == nil && p.State == client.PartInvited && p.Held == 0
	})
	d, g := guestOf(t, guestLive, conv, invite.PID)
	if d.Role != "human_guest" || d.Frozen == "" || !g.CanDecide || g.CanSend || g.CanEnd {
		t.Fatalf("pending guest permissions: role=%s frozen=%q guest=%+v", d.Role, d.Frozen, g)
	}
	accept := true
	if _, err = guestLive.ChangeHuman(ctx, GuestAction{Action: "decide", PID: invite.PID, Accept: &accept}); err != nil {
		t.Fatal(err)
	}
	eventually("acceptance at member", func() bool { p, e := bob.Participation(invite.PID); return e == nil && p.HumanActive() })
	d, g = guestOf(t, guestLive, conv, invite.PID)
	if d.Role != "human_guest" || d.Frozen != "" || !g.CanSend || !g.CanLeave || g.CanEnd || g.CanDecide {
		t.Fatalf("active guest permissions: role=%s frozen=%q guest=%+v", d.Role, d.Frozen, g)
	}
	if _, g = guestOf(t, memberLive, conv, invite.PID); !g.CanEnd {
		t.Fatal("non-admin member cannot end guest visit")
	}
	if _, err = memberLive.ChangeHuman(ctx, GuestAction{Action: "end", PID: invite.PID}); err != nil {
		t.Fatal(err)
	}
	eventually("guest dismissed", func() bool {
		p, e := guest.Participation(invite.PID)
		return e == nil && p.State == client.PartDismissed
	})
	d, g = guestOf(t, guestLive, conv, invite.PID)
	if d.Role != "human_guest" || d.Frozen == "" || g.CanSend || g.CanLeave || g.CanEnd {
		t.Fatalf("ended guest permissions: %+v %+v", d, g)
	}
}

func liveTwoMemberGroup(t *testing.T, alice, bob *client.Agent) string {
	t.Helper()
	ctx := context.Background()
	packet, err := alice.CreateGroup(ctx, "Harbor planning")
	if err != nil {
		t.Fatal(err)
	}
	person, _, err := bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	admission, err := bob.SignGroupAdmission(packet.Root, 1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	packet.Proof = nil
	packet.State.Seq, packet.State.Prev = 1, packet.State.Hash()
	packet.State.Members = append(slices.Clone(packet.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.Person, Roster: person.Roster}, Admission: admission})
	slices.SortFunc(packet.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	packet, err = alice.SignGroupState(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := alice.BuildGroupCommit(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = alice.PublishGroup(ctx, commit, packet); err != nil {
		t.Fatal(err)
	}
	return packet.State.Conv
}
