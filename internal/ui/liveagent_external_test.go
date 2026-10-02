package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Provider decisions must distinguish a visitor's context from room
// membership, and a vouched excerpt from original verified authorship.
func TestExternalAgentProviderRolesAndExcerptClaims(t *testing.T) {
	ref := protocol.GrantRef{LID: protocol.NewID(), Fingerprint: "11111111-22222222-33333333-44444444"}
	info := client.ParticipationInfo{PID: protocol.NewID(), State: client.PartActive, External: true, HostHere: true,
		Host:    client.PersonInfo{Person: protocol.NewID(), Address: "charlie/host", Label: "Charlie"},
		Inviter: client.PersonInfo{Person: protocol.NewID(), Address: "alice/desk", Label: "Alice"}, Grant: []protocol.GrantRef{ref}}
	claimed := client.ConvMessage{ID: protocol.NewID(), LID: ref.LID, History: true, Replica: true, Claimed: ref.Fingerprint, SyncedFrom: info.Inviter.Address, ExcerptPID: info.PID}
	people := dmPeople{me: personView(info.Host), peer: personView(info.Inviter), role: "visitor"}
	view := agentView(info, people, []client.ConvMessage{claimed}, true)
	if !view.External || view.CanAsk || view.CanDismiss || view.CanDecide || view.Missing != 0 || len(view.Shared) != 1 || view.Shared[0] != claimed.ID {
		t.Fatalf("active visitor action/context projection %+v", view)
	}
	info.State = client.PartInvited
	view = agentView(info, people, []client.ConvMessage{claimed}, true)
	if !view.CanDecide || view.CanAsk || view.CanDismiss || view.Missing != 0 {
		t.Fatalf("invited host role %+v", view)
	}
	for _, mutate := range []func(*client.ConvMessage){
		func(m *client.ConvMessage) { m.ExcerptPID = protocol.NewID() },
		func(m *client.ConvMessage) { m.Claimed = "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd" },
		func(m *client.ConvMessage) { m.SyncedFrom = "bob/other" },
	} {
		bad := claimed
		mutate(&bad)
		view = agentView(info, people, []client.ConvMessage{bad}, true)
		if view.Missing != 1 || len(view.Shared) != 0 {
			t.Fatalf("unbound claimed excerpt counted %+v", view)
		}
	}
	// The same accepted agent still accepts asks/dismissals from an actual
	// room member; visitor denial is a projection, not a new auth system.
	info.HostHere = false
	info.State = client.PartActive
	people.role = "member"
	view = agentView(info, people, []client.ConvMessage{claimed}, true)
	if !view.CanAsk || !view.CanDismiss || view.CanDecide {
		t.Fatalf("member actions lost %+v", view)
	}
}
