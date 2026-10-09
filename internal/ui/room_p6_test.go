package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestP6GroupAgentView(t *testing.T) {
	host := client.PersonInfo{Person: "owner", Label: "Sergey", Address: "sergey/laptop", Fingerprint: "host-key"}
	inviter := client.PersonInfo{Person: "other", Label: "Vitalii", Address: "vitalii/desktop"}
	info := client.ParticipationInfo{PID: "agent", Member: true, State: client.PartActive, Host: host, Inviter: host, Inviters: []client.PersonInfo{host, inviter}, HostHere: true}
	view := agentView(info, dmPeople{group: true}, nil, true)
	if !view.Member || len(view.Inviters) != 2 || !view.CanAsk || !view.CanDismiss {
		t.Fatalf("group membership projection: %+v", view)
	}
}

func TestP6FixOutsideAcceptanceDisclosureUsesPlainName(t *testing.T) {
	owner := PersonView{Person: protocol.NewID(), Label: "Nora", Address: "nora/office", Fingerprint: "11111111-22222222-33333333-44444444"}
	ev := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("a", 64), PID: protocol.NewID(), Type: protocol.EventAccept, Prev: strings.Repeat("b", 64), TS: 1,
		Author: protocol.EventAuthor{Person: owner.Person, Roster: strings.Repeat("c", 64), Address: owner.Address, Fingerprint: owner.Fingerprint}}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, known := range []map[string]PersonView{nil, {owner.Person: owner}} {
		text := eventText(string(raw), dmPeople{group: true, known: known})
		if strings.Contains(text, owner.Address) || !strings.Contains(text, "participates with the scope of its invitation") || strings.Contains(text, "every new message and file") {
			t.Fatalf("outside disclosure: %q", text)
		}
		if known != nil && !strings.Contains(text, owner.Label) {
			t.Fatalf("known outside owner unnamed: %q", text)
		}
	}
}

func TestP6FixGroupViewWarningsAndLegacy(t *testing.T) {
	host := client.PersonInfo{Person: "owner", Label: "Sergey", Address: "sergey/laptop", Fingerprint: "host-key"}
	for _, tc := range []struct {
		name                    string
		member, external, ready bool
		held                    int
		agent, want             string
	}{
		{"legacy", false, false, true, 0, "", "In this group"},
		{"no responder", true, false, false, 0, "", "no responder is chosen"},
		{"named not ready", true, false, false, 0, "named", "selected agent is not ready"},
		{"held", true, false, true, 1, "", "do not count here yet"},
		{"outside member", true, true, true, 0, "", "receives every new message and file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := client.ParticipationInfo{PID: "agent", Member: tc.member, External: tc.external, State: client.PartActive, Host: host, HostHere: !tc.external, Held: tc.held, AgentID: tc.agent}
			v := agentView(info, dmPeople{group: true, role: "member"}, nil, tc.ready)
			if !v.Member || !strings.Contains(v.StateText, tc.want) {
				t.Fatalf("view: %+v", v)
			}
			if (!tc.member || tc.held > 0 || !tc.ready) && strings.Contains(v.StateText, "Stays until explicitly removed") {
				t.Fatalf("false permanent-ready claim: %s", v.StateText)
			}
		})
	}
}
func TestP6GroupAgentsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_P6_RENDERED") != "1" {
		t.Skip("opt-in inert host rendered fixture")
	}
	out, e := exec.Command("node", "testdata/group_agents_p6_rendered.cjs").CombinedOutput()
	if e != nil {
		t.Fatalf("P6 rendered: %v\n%s", e, out)
	}
	t.Log(string(out))
}

func TestPendingRecordsPreserveBackendAuthority(t *testing.T) {
	pending := agentView(client.ParticipationInfo{PID: "held-only", State: client.PartPending, Held: 2}, dmPeople{group: true, role: "member"}, nil, true)
	if pending.CanAsk || pending.CanDecide || pending.CanDismiss || pending.Host.Person != "" || pending.AgentID != "" {
		t.Fatalf("pending records invented authority or identity: %+v", pending)
	}
	invited := agentView(client.ParticipationInfo{PID: "real-invite", State: client.PartInvited, Host: client.PersonInfo{Person: "host", Label: "Nora", Address: "nora/desk"}}, dmPeople{group: true, role: "member"}, nil, true)
	if !invited.CanDismiss || invited.CanAsk {
		t.Fatalf("real invitation cancellation changed: %+v", invited)
	}
}

func TestPendingRecordsRoomModel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/pending_records_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("room model: %v\n%s", err, out)
	}
}

func TestPendingRecordsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_agents_p6_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_PENDING_RECORDS_ONLY=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("pending records rendered: %v\n%s", err, out)
	}
	t.Log(string(out))
}
