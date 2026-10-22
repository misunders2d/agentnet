package ui

import (
	"context"
	"encoding/json"
	"os"
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
	// Project actual signed group guest events before and after an ordinary
	// exchange. Their role and room must survive the presentation boundary.
	type snapshot struct {
		Phase    string   `json:"phase"`
		Overview Overview `json:"overview"`
		Thread   DMThread `json:"thread"`
	}
	var snapshots []snapshot
	capture := func(phase string, types ...string) {
		t.Helper()
		view, e := memberLive.DM(conv)
		if e != nil {
			t.Fatal(e)
		}
		for _, typ := range types {
			found := false
			for _, m := range view.Messages {
				if m.PID != invite.PID || m.EventType != typ {
					continue
				}
				found = true
				if strings.Contains(m.Event, "agent") || strings.Contains(m.Event, "this DM") || strings.Contains(m.Event, guest.Address) {
					t.Fatalf("group person %s mislabeled: %q", typ, m.Event)
				}
				if typ == protocol.EventAccept && m.Event != "Carol joined this group." {
					t.Fatalf("group acceptance: %q", m.Event)
				}
				if typ == protocol.EventDismiss && m.Event != "You removed Carol from this group." {
					t.Fatalf("group dismissal: %q", m.Event)
				}
			}
			if !found {
				t.Fatalf("missing signed %s event in group", typ)
			}
		}
		overview, e := memberLive.Overview()
		if e != nil {
			t.Fatal(e)
		}
		for _, summary := range overview.DMs {
			if summary.ID != conv {
				continue
			}
			want := "Carol joined this group."
			if phase == "dismissed" {
				want = "You removed Carol from this group."
			}
			if summary.Last != want {
				t.Fatalf("group person %s preview: %q, want %q", phase, summary.Last, want)
			}
		}
		snapshots = append(snapshots, snapshot{phase, overview, view})
	}
	capture("accepted", protocol.EventInvite, protocol.EventAccept)
	if _, err = bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "Member welcomes the guest"}); err != nil {
		t.Fatal(err)
	}
	eventually("guest receives the member message", goHas(guest, conv, "Member welcomes the guest", nil))
	if _, err = guest.SendConv(ctx, conv, client.ConvOutgoing{PID: invite.PID, Body: "Guest replies in the group"}); err != nil {
		t.Fatal(err)
	}
	eventually("member receives the guest reply", goHas(bob, conv, "Guest replies in the group", nil))
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
	capture("dismissed", protocol.EventInvite, protocol.EventAccept, protocol.EventDismiss)
	if dest := os.Getenv("AGENTNET_GUEST_EVENT_PROJECTION"); dest != "" {
		raw, e := json.Marshal(snapshots)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, raw, 0600); e != nil {
			t.Fatal(e)
		}
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
