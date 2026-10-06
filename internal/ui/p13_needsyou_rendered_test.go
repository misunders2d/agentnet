package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// Synthetic rendering only: this provider never starts an agent or a Hub.
type needsYouFixture struct {
	*Fixture
	phone bool
}

var needsYouFull = "Which account should I use?\n\nChoose the company account before I continue.\nThe whole final paragraph must be readable.\n" + strings.Repeat("LongUnbroken🙂", 80)

func (f *needsYouFixture) Overview() (Overview, error) {
	o, err := f.Fixture.Overview()
	address := "alice/laptop"
	if f.phone {
		address = "alice/tablet"
	}
	o.Me.Address = address
	o.Role = RolePerson
	o.Persons = true
	o.Agents = true
	o.Person = &PersonView{Person: "alice", Label: "Alice", Address: address, State: "self", Published: true, Devices: []DeviceView{{Address: "alice/laptop", Name: "laptop"}, {Address: "alice/tablet", Name: "tablet"}}}
	o.Threads = []ThreadSummary{}
	o.Review = []ReviewItem{}
	peer := PersonView{Person: "bob", Label: "Bob", Address: "bob/desk", State: "pinned"}
	o.DMs = []DMSummary{{ID: "needs-chat", Peer: peer, Title: "Account choice", Count: 1, Last: "Original request", Created: f.now(), LastAt: f.now()}}
	item := ConvItem{Reason: "agent_needs_human", Conv: "needs-chat", PID: "needs-agent", ID: "needs-request", Peer: address, Kind: "task", Why: needsYouFull, Excerpt: "Original request", At: f.now(), Actions: []string{"accept", "resolve"}}
	if f.phone {
		item.Actions = nil
		item.DecideOn = "alice/laptop"
	}
	o.NeedsYou = []ConvItem{item}
	return o, err
}

func (f *needsYouFixture) DM(id string) (DMThread, error) {
	o, _ := f.Overview()
	host := *o.Person
	host.Address = "alice/laptop"
	m := DMMessage{ID: "needs-request", LID: "needs-request", PID: "needs-agent", Dir: "out", From: o.Me.Address, Kind: "task", Body: "Original request", At: f.now(), State: "delivered", JobDetail: needsYouFull, Target: &envelope.Target{Address: host.Address}, Actions: []string{"accept", "resolve"}, Exec: &client.ExecView{State: "needs_human", Host: host.Address}}
	if f.phone {
		m.Actions = nil
	}
	return DMThread{ID: id, Peer: o.DMs[0].Peer, Created: f.now(), Mine: true, Messages: []DMMessage{m}, Agents: []AgentView{{PID: m.PID, State: "active", Host: host, HostHere: !f.phone, Inviter: host, Shared: []string{}, TasksFrom: []PersonView{}}}}, nil
}

func TestNeedsYouRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	for _, phone := range []bool{false, true} {
		f := &needsYouFixture{Fixture: NewFixture(time.Now), phone: phone}
		ts := httptest.NewUnstartedServer(nil)
		ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
		ts.Start()
		cmd := exec.Command("node", "testdata/p13_needsyou_rendered_check.cjs", ts.URL+"/?t="+testToken)
		mode := "desktop"
		if phone {
			mode = "phone"
		}
		cmd.Env = append(os.Environ(), "P13_MODE="+mode, "P13_FULL="+needsYouFull)
		out, err := cmd.CombinedOutput()
		ts.Close()
		if err != nil || !strings.Contains(string(out), "P13 rendered PASS") {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		t.Logf("%s", out)
	}
}
