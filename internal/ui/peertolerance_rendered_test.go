package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Synthetic rendering only: a phone-like device lists requests its person's
// laptop runs. A stale word (the laptop suspended until it updates) reads
// as no result reported, never "Decide on"; a running one as work; only a
// current awaiting one asks the person to decide there. A message sent to
// a device the relay suspended says so on that device's copy, and its
// headline is everyone else's delivery. One a member's device was not
// sent at all (its key changed) never reads as delivered: it says not sent
// and to which device, and that copy says why.
type peerToleranceFixture struct{ *Fixture }

func (f *peerToleranceFixture) Overview() (Overview, error) {
	o, err := f.Fixture.Overview()
	o.Me.Address = "alice/tablet"
	o.Role = RolePerson
	o.Persons = true
	o.Agents = true
	o.Person = &PersonView{Person: "alice", Label: "Alice", Address: "alice/tablet", State: "self", Published: true, Devices: []DeviceView{{Address: "alice/laptop", Name: "laptop"}, {Address: "alice/tablet", Name: "tablet"}}}
	o.Threads = []ThreadSummary{}
	o.Review = []ReviewItem{}
	peer := PersonView{Person: "bob", Label: "Bob", Address: "bob/desk", State: "pinned", Devices: []DeviceView{{Address: "bob/desk", Name: "desk"}, {Address: "bob/phone", Name: "phone"}}}
	o.People = []PersonView{peer}
	o.DMs = []DMSummary{{ID: "tolerance-chat", Peer: peer, Title: "Rotation", Count: 1, Last: "Rotate the key", Created: f.now(), LastAt: f.now()}}
	item := func(id, reason, why string, stale bool) ConvItem {
		return ConvItem{Reason: reason, Conv: "tolerance-chat", PID: "tolerance-agent", ID: id, Peer: "alice/tablet", Kind: "task", Why: why, Excerpt: "Rotate " + id, At: f.now(), DecideOn: "alice/laptop", Stale: stale}
	}
	o.NeedsYou = []ConvItem{
		item("stale-request", "agent_awaiting", "No result reported · your laptop is suspended until it updates AgentNet", true),
		item("running-request", "agent_running", "Running on your laptop. This browser runs no agent.", false),
		item("awaiting-request", "agent_awaiting", "Decide on your laptop. This browser runs no agent.", false),
	}
	return o, err
}

func (f *peerToleranceFixture) DM(id string) (DMThread, error) {
	o, _ := f.Overview()
	m := DMMessage{ID: "tolerance-msg", LID: "tolerance-msg", Dir: "out", From: o.Me.Address, Kind: envelope.KindMessage, Body: "Rotate the key", At: f.now(), State: "waiting", Delivery: "delivered",
		Copies: []CopyView{{To: "bob/desk", Person: "Bob", State: "delivered"}, {To: "bob/phone", Person: "Bob", State: "waiting", Detail: "peer_update: bob/phone cannot read this yet", Suspended: true}}}
	skipped := DMMessage{ID: "skipped-msg", LID: "skipped-msg", Dir: "out", From: o.Me.Address, Kind: envelope.KindMessage, Body: "Before the key changed", At: f.now().Add(-time.Minute),
		State: "not_delivered", Delivery: "not_delivered", StateText: StateText("out", KindMessage, "not_delivered", "Carol’s desk"), Detail: "not sent: carol/desk's key changed",
		Copies: []CopyView{{To: "bob/desk", Person: "Bob", State: "delivered"}, {To: "carol/desk", Person: "Carol", State: "not_delivered", Detail: "not sent: carol/desk's key changed"}}}
	return DMThread{ID: id, Peer: o.DMs[0].Peer, Created: f.now(), Mine: true, Messages: []DMMessage{skipped, m}}, nil
}

func TestPeerToleranceRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	f := &peerToleranceFixture{Fixture: NewFixture(time.Now)}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	out, err := exec.Command("node", "testdata/peertolerance_rendered_check.cjs", ts.URL+"/?t="+testToken).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "peer tolerance rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// One device never stops everyone in the browser engine: per-device
// waiting copies in group turns, group publications and DMs with a guest,
// a changed key skipping only that device, and a suspended device holding
// nobody (testdata/peertolerance_engine_check.mjs).
func TestBrowserEnginePeerTolerance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/peertolerance_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS one device never stops everyone") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
