package client

import (
	"bytes"
	"context"
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkspacesTwoIndependentMembershipsRestart(t *testing.T) {
	first := newWorld(t, "")
	secondDir := filepath.Join(t.TempDir(), "second-hub")
	testhub.Start(t, secondDir, "127.0.0.1:0", "")
	secondAdmin := mustJoin(t, filepath.Join(t.TempDir(), "second-admin"), testhub.BootstrapCode(t, secondDir), "alice")
	secondInvite, err := secondAdmin.Invite(tctx(t), "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := OpenWorkspaces(first.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(first.bobHome, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := registry.Join(tctx(t), "", "Same name", secondInvite, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if w.ID == DefaultWorkspace || w.Address != first.bob.Address {
		t.Fatalf("independent enrollment failed: %#v", w)
	}
	other, err := registry.Open(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Self().Fingerprint() == first.bob.Self().Fingerprint() {
		t.Fatal("keys reused across memberships")
	}
	if first.bob.hub.base == other.hub.base {
		t.Fatal("not independent relay endpoints")
	}
	if err = first.bob.Approve(first.alice.Address); err != nil {
		t.Fatal(err)
	}
	// Verify the second store has not inherited the first store's pins/grants.
	if approvals, err := other.Approvals(); err != nil || approvals != 0 {
		t.Fatalf("cross-membership approval: %d %v", approvals, err)
	}
	if _, _, found, err := other.store.peer(first.alice.Address); err != nil || found {
		t.Fatalf("cross-membership pin: %v %v", found, err)
	}
	if _, err = registry.Home("../../identity.json"); err == nil {
		t.Fatal("accepted arbitrary path")
	}
	registry, err = OpenWorkspaces(first.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	items, err := registry.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("restart lost memberships: %v %#v", err, items)
	}
	home, err := registry.Home(DefaultWorkspace)
	if err != nil || home != first.bobHome {
		t.Fatalf("legacy home moved: %s %v", home, err)
	}
	if err = registry.Disconnect(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Open(w.ID); err == nil {
		t.Fatal("disconnected membership remained routable")
	}
	if err = registry.Reconnect(w.ID); err != nil {
		t.Fatal(err)
	}
	first.hub.Stop()
	if _, err := other.Sessions(tctx(t), secondAdmin.Address); err != nil {
		t.Fatalf("offline A broke healthy B: %v", err)
	}
	if err := secondAdmin.Revoke(tctx(t), other.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Sessions(tctx(t), secondAdmin.Address); !errors.Is(err, ErrRevoked) {
		t.Fatalf("authenticated revocation not preserved: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(first.bobHome, "identity.json"))
	if !bytes.Equal(original, after) {
		t.Fatal("legacy key changed")
	}
}
func TestWorkspacesFailedInviteRetainsRetryID(t *testing.T) {
	w := newWorld(t, "")
	registry, err := OpenWorkspaces(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := registry.Join(tctx(t), "", "Other", "invalid invitation", "laptop")
	if err == nil || pending.ID == "" {
		t.Fatal("failed invite did not retain recoverable ID")
	}
	items, _ := registry.List()
	if len(items) != 2 || items[1].State != "joining" {
		t.Fatal("lost interrupted join")
	}
	code, err := w.alice.Invite(tctx(t), "carol", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := registry.Join(tctx(t), pending.ID, "Other", code, "laptop")
	if err != nil || enrolled.ID != pending.ID {
		t.Fatalf("retry replaced identity: %#v %v", enrolled, err)
	}
}

// BUG-40c: a workspace or device name that can never enroll is refused with
// a clear error before any joining record (or home) is made for it.
func TestWorkspacesJoinRefusesBadNamesBeforeRecording(t *testing.T) {
	w := newWorld(t, "")
	registry, err := OpenWorkspaces(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	code, err := w.alice.Invite(tctx(t), "carol", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, agent string }{
		{"\u0007" + strings.Repeat("N", 400) + "\n<b>x</b>", "laptop"},
		{"Other", "Laptop!"},
		{"Other", ""},
	} {
		item, err := registry.Join(tctx(t), "", c.name, code, c.agent)
		if err == nil || item.ID != "" || !strings.Contains(err.Error(), "name") {
			t.Fatalf("join %q as %q: %+v %v", c.name, c.agent, item, err)
		}
	}
	if items, err := registry.List(); err != nil || len(items) != 1 {
		t.Fatalf("refused joins left records: %+v %v", items, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.bobHome, "workspaces")); len(entries) != 0 {
		t.Fatalf("refused joins left homes: %d", len(entries))
	}
	// No label is fine: the workspace's own name shows.
	if item, err := registry.Join(tctx(t), "", "   ", code, "laptop"); err != nil || item.Name != "" || item.State != "enrolled" {
		t.Fatalf("join without a label: %+v %v", item, err)
	}
}

func TestWorkspacesExpiredInviteNeverEnrolled(t *testing.T) {
	w := newWorld(t, "")
	registry, err := OpenWorkspaces(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	code, err := w.alice.Invite(tctx(t), "expired", time.Nanosecond, false)
	if err != nil {
		t.Fatal(err)
	}
	item, err := registry.Join(tctx(t), "", "Expired", code, "laptop")
	if err == nil {
		t.Fatal("expired invitation enrolled")
	}
	if _, err = registry.Home(item.ID); err == nil {
		t.Fatal("failed invitation made a routable membership")
	}
}

// One action asks the Hub each check once: concurrent asks share one
// answer, an error is asked again unless the Hub was unreachable, and
// without an action every ask goes out (roundtrips.go).
func TestRoundTripsAskOnce(t *testing.T) {
	table := func(rt *roundTrips) *map[string]*roundTrip[pinResult] { return &rt.persons }
	var asked atomic.Int32
	release := make(chan struct{})
	fetch := func() (pinResult, error) { asked.Add(1); <-release; return pinResult{changed: true}, nil }
	ctx := withRoundTrips(context.Background())
	if withRoundTrips(ctx) != ctx {
		t.Fatal("an action inside another asks again")
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if r, err := askOnce(ctx, table, "p", fetch); err != nil || !r.changed {
				t.Errorf("shared answer %+v %v", r, err)
			}
		})
	}
	eventually(t, "one ask in flight", func() bool { return asked.Load() == 1 })
	close(release)
	wg.Wait()
	if _, err := askOnce(ctx, table, "p", fetch); err != nil || asked.Load() != 1 {
		t.Fatalf("one action asked %d times: %v", asked.Load(), err)
	}
	askOnce(context.Background(), table, "p", fetch)
	askOnce(context.Background(), table, "p", fetch)
	if asked.Load() != 3 {
		t.Fatalf("without an action %d asks, want every one", asked.Load()-1)
	}
	fails := 0
	refused := func() (pinResult, error) { fails++; return pinResult{}, errors.New("refused") }
	askOnce(ctx, table, "q", refused)
	askOnce(ctx, table, "q", refused)
	if fails != 2 {
		t.Fatalf("an error was kept: %d asks", fails)
	}
	down := 0
	unreachable := func() (pinResult, error) {
		down++
		return pinResult{}, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	}
	for range 2 {
		if _, err := askOnce(ctx, table, "r", unreachable); !hubUnreachable(err) {
			t.Fatal(err)
		}
	}
	if down != 1 {
		t.Fatalf("one action waited for an unreachable Hub %d times", down)
	}
	// A long action asks again once an answer is older than it reuses.
	long := context.WithValue(context.Background(), roundTripsKey{}, &roundTrips{fresh: 50 * time.Millisecond})
	n := 0
	count := func() (pinResult, error) { n++; return pinResult{}, nil }
	askOnce(long, table, "s", count)
	askOnce(long, table, "s", count)
	time.Sleep(60 * time.Millisecond)
	askOnce(long, table, "s", count)
	if n != 2 {
		t.Fatalf("%d asks, want one per fresh answer", n)
	}
}

// While the push stream is open the relay's features are read once and an
// unchanged capability record or agent catalog is not published again; a
// new connection reads and publishes afresh (profilecache.go relayCache).
func TestRelayCacheOnOneConnection(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	count := func(req string) int {
		n := 0
		for _, r := range w.hub.Requests() {
			if r == a.Address+" "+req {
				n++
			}
		}
		return n
	}
	ctx, session := tctx(t), protocol.NewID()
	// step returns how many feature reads, capability records and catalogs
	// one round of reading and publishing sent.
	step := func() [3]int {
		t.Helper()
		v, c, g := count("GET /v1/version"), count("PUT /v1/caps"), count("PUT /v1/agent-catalog")
		if _, err := a.relayFeatures(ctx); err != nil {
			t.Fatal(err)
		}
		if err := a.putCaps(ctx, session); err != nil {
			t.Fatal(err)
		}
		if err := a.PublishAgentCatalog(ctx); err != nil {
			t.Fatal(err)
		}
		return [3]int{count("GET /v1/version") - v, count("PUT /v1/caps") - c, count("PUT /v1/agent-catalog") - g}
	}
	want := func(what string, got, want [3]int) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: features, caps, catalog %v, want %v", what, got, want)
		}
	}
	step() // the first request also checks the relay once
	unconnected := step()
	if unconnected[0] == 0 || unconnected[1] != 1 || unconnected[2] != 1 {
		t.Fatalf("no stream: features, caps, catalog %v, want every one", unconnected)
	}
	want("no stream again", step(), unconnected)
	a.relay.reset(true)
	want("connected", step(), [3]int{1, 1, 1})
	want("same connection, nothing changed", step(), [3]int{0, 0, 0})
	if err := a.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateLocalAgent("Reviewer", Responder{Harness: "codex", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	want("changed record and catalog", step(), [3]int{0, 1, 1})
	a.relay.reset(true)
	want("reconnected", step(), [3]int{1, 1, 1})
	a.relay.reset(false)
	want("disconnected", step(), unconnected)
}

// A group message asks for every member's roster together, not one after
// another, and each once (roundtrips.go prefetchGroup).
func TestGroupSendAsksRostersTogether(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]() // only the send asks
	members := len(p.State.Members)
	var mu sync.Mutex
	var once sync.Once
	inFlight, most := 0, 0
	all := make(chan struct{})
	w.hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) != w.alice.Address || !strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			return
		}
		mu.Lock()
		inFlight++
		most = max(most, inFlight)
		if inFlight == members {
			once.Do(func() { close(all) })
		}
		mu.Unlock()
		select { // one after another, each waits here alone
		case <-all:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
	})
	defer w.hub.SetHold(nil)
	before := len(w.hub.Requests())
	if _, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "together"}); err != nil {
		t.Fatal(err)
	}
	asked := map[string]int{}
	for _, r := range w.hub.Requests()[before:] {
		if strings.HasPrefix(r, w.alice.Address+" GET /v1/persons/") {
			asked[r]++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if most != members || len(asked) != members {
		t.Fatalf("%d of %d rosters asked together; asked %v", most, members, asked)
	}
	for r, n := range asked {
		if n != 1 {
			t.Fatalf("%s asked %d times", r, n)
		}
	}
}

// An agent's invitation into a group asks for each key, profile, roster
// and catalog once, however many of its sends need them (roundtrips.go).
func TestGroupInvitationAsksEachCheckOnce(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	host := proofReader(t, w, "agy-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	record, err := host.CreateLocalAgent("Antigravity reviewer", Responder{Harness: "agy", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	stops[w.alice]() // only the invitation asks
	before := len(w.hub.Requests())
	if _, err = w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	asked := map[string]int{}
	for _, r := range w.hub.Requests()[before:] {
		if strings.HasPrefix(r, w.alice.Address+" GET ") {
			asked[r]++
		}
	}
	rosters := 0
	for r, n := range asked {
		if n != 1 {
			t.Errorf("%s asked %d times", r, n)
		}
		if strings.HasPrefix(r, w.alice.Address+" GET /v1/persons/") {
			rosters++
		}
	}
	if rosters != len(packet.State.Members)+1 {
		t.Errorf("%d rosters asked, want the members' and the host's: %v", rosters, asked)
	}
}
