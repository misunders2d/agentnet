package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestLiveHumanInvitationNeedsYou(t *testing.T) {
	alice, bob, guest, dm, wait, _ := liveGuestWorld(t)
	live := NewLive(guest)
	group := liveTwoMemberGroup(t, alice, bob)
	for _, conv := range []string{dm, group} {
		invite, err := alice.InviteHuman(t.Context(), conv, guest.Address, nil, "Join this conversation")
		if err != nil {
			t.Fatal(err)
		}
		wait("exact human invitation received", func() bool {
			p, e := guest.Participation(invite.PID)
			return e == nil && p.State == client.PartInvited && p.Held == 0
		})
		view, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range view.NeedsYou {
			if item.PID == invite.PID && item.Conv == conv && item.Reason == client.ReviewInvite && item.Role == "human" {
				found = true
			}
		}
		if !found {
			t.Fatal("received human guest invitation is absent from OKs")
		}
		accept := false
		if _, err = live.ChangeHuman(t.Context(), GuestAction{Action: "decide", PID: invite.PID, Accept: &accept}); err != nil {
			t.Fatal(err)
		}
		view, err = live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range view.NeedsYou {
			if item.PID == invite.PID {
				t.Fatal("declined guest invitation still asks for a decision")
			}
		}
	}
}
