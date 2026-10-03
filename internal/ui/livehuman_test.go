package ui

import (
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func TestHumanGuestProjectionTruthAndPositiveAuthority(t *testing.T) {
	p := client.ParticipationInfo{PID: "guest", Role: protocol.RoleHuman, State: client.PartInvited, HostHere: true}
	v := guestView(p, false)
	if !v.CanDecide || v.CanSend || v.CanEnd || v.CanLeave {
		t.Fatal("pending guest acquired non-consent authority")
	}
	p.State = client.PartActive
	v = guestView(p, false)
	if !v.CanSend || !v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("accepted exact host projection")
	}
	p.HostHere = false
	v = guestView(p, false)
	if v.CanSend || v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("non-member or linked guest device acquired authority")
	}
	v = guestView(p, true)
	if !v.CanSend || !v.CanEnd {
		t.Fatal("positive original member projection")
	}
	p.Held = 1
	v = guestView(p, true)
	if v.CanSend || !v.AudiencePending || !strings.Contains(v.StateText, "verified") {
		t.Fatal("held proof represented as active")
	}
	p.Held = 0
	p.State = client.PartDismissed
	v = guestView(p, true)
	if v.CanSend || v.CanEnd || !v.AudiencePending || !strings.Contains(v.StateText, "Previously shared copies remain") || !strings.Contains(v.StateText, "Other devices") {
		t.Fatal("ended state claims global recall or grants sends")
	}
}
