package ui

import (
	"os"
	"os/exec"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
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
