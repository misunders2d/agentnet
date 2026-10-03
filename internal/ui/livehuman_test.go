package ui

import (
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func TestHumanGuestProjectionTruthAndPositiveAuthority(t *testing.T) {
	p := client.ParticipationInfo{PID: "guest", Role: protocol.RoleHuman, State: client.PartInvited, HostHere: true}
	v := guestView(p, false, guestEnd{})
	if !v.CanDecide || v.CanSend || v.CanEnd || v.CanLeave {
		t.Fatal("pending guest acquired non-consent authority")
	}
	p.State = client.PartActive
	v = guestView(p, false, guestEnd{})
	if !v.CanSend || !v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("accepted exact host projection")
	}
	p.HostHere = false
	v = guestView(p, false, guestEnd{})
	if v.CanSend || v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("non-member or linked guest device acquired authority")
	}
	v = guestView(p, true, guestEnd{})
	if !v.CanSend || !v.CanEnd {
		t.Fatal("positive original member projection")
	}
	p.Held = 1
	v = guestView(p, true, guestEnd{})
	if v.CanSend || !v.AudiencePending || !strings.Contains(v.StateText, "verified") {
		t.Fatal("held proof represented as active")
	}
	p.Held = 0
	p.State = client.PartDismissed
	v = guestView(p, true, guestEnd{unheard: true})
	if v.CanSend || v.CanEnd || !v.AudiencePending || !strings.Contains(v.StateText, "Previously shared copies remain") || !strings.Contains(v.StateText, "not received the end") {
		t.Fatal("ended state claims global recall or grants sends")
	}
	// Once the guest's device holds the end, or the guest left, nothing new
	// can reach them: nothing is pending, and shared copies still remain.
	for _, end := range []guestEnd{{}, {left: true}} {
		v = guestView(p, true, end)
		if v.CanSend || v.CanEnd || v.AudiencePending || !strings.Contains(v.StateText, "Previously shared copies remain") {
			t.Fatalf("settled end %+v: %+v", end, v)
		}
	}
	if v = guestView(p, true, guestEnd{left: true}); !strings.HasPrefix(v.StateText, "Left") {
		t.Fatalf("a guest who left shown as ended by someone: %q", v.StateText)
	}
}
