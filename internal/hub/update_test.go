package hub

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// releaseHub opens a Hub running build (""; protocol.Version, as stamped
// into a release binary) with an update grace (negative: none) and
// heartbeat (zero: the default).
func releaseHub(t *testing.T, dir, build string, grace, heartbeat time.Duration) *Hub {
	t.Helper()
	h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Version: build, UpdateGrace: grace, Heartbeat: heartbeat})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// callAs is m.call from a client that reports version ("": none, as
// programs before v0.8.17).
func (m member) callAs(t *testing.T, h *Hub, version, method, path string, in any) (int, []byte) {
	t.Helper()
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	r := signed(t, m.id, m.addr, method, path, body)
	if version != "" {
		r.Header.Set(protocol.VersionHeader, version)
	}
	w := serve(h, r)
	return w.Code, w.Body.Bytes()
}

// sendAs posts a message from one member to another from a client of
// version and returns its id.
func sendAs(t *testing.T, h *Hub, version string, from, to member) string {
	t.Helper()
	r, _ := to.id.Public(to.addr).Recipient()
	env, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: from.addr, To: to.addr, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "x"}, from.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if c, b := from.callAs(t, h, version, "POST", "/v1/messages", env); c != http.StatusAccepted {
		t.Fatalf("post: %d %s", c, b)
	}
	return env.ID
}

// backdate moves the times this Hub first asked for each release back by d,
// as if it had asked that much earlier, and plans the grace ends again.
func backdate(t *testing.T, h *Hub, d time.Duration) {
	t.Helper()
	if _, err := h.store.db.Exec(`UPDATE releases_served SET since_ms = since_ms - ?`, d.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.serveLatest(); err != nil {
		t.Fatal(err)
	}
}

func memberOf(t *testing.T, ms protocol.Members, addr string) protocol.Member {
	t.Helper()
	for _, m := range ms.Members {
		if m.Address == addr {
			return m
		}
	}
	t.Fatalf("%s not listed: %+v", addr, ms)
	return protocol.Member{}
}

const updateRequiredV120 = `{"error":"update_required","latest":"v1.2.0","url":"https://github.com/misunders2d/agentnet/releases/tag/v1.2.0","message":"Update AgentNet to v1.2.0 to continue."}`

// A release relay serves a client older than its release (or one that says
// no version, as programs before v0.8.17) through the grace period, then
// answers its requests with 426 and the release to update to. Current
// clients, development builds of the release and newer ones are served
// throughout; a suspended client keeps its stream's acknowledgements, the
// recommendation and what needs no authentication.
func TestOutdatedClientSuspendedAfterGrace(t *testing.T) {
	h := releaseHub(t, filepath.Join(t.TempDir(), "hub"), "v1.2.0", time.Hour, 0)
	defer h.Close()
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	clients := map[string]bool{ // reported version: current
		"": false, "dev": false, "v1.1.9": false, "v1.1.9-3-gabcdef1": false, "v1.2.0-rc1": false, "v 1.2.0": false,
		"v1.2.0": true, "v1.2.0-3-gabcdef1": true, "v1.2.0+0760ccc": true, "v1.2.0-dirty": true, "v1.3.0": true,
	}
	for v := range clients {
		if c, b := alice.callAs(t, h, v, "GET", "/v1/agents", nil); c != http.StatusOK {
			t.Fatalf("%q within the grace period: %d %s", v, c, b)
		}
	}
	backdate(t, h, time.Hour)
	for v, current := range clients {
		c, b := alice.callAs(t, h, v, "GET", "/v1/agents", nil)
		switch {
		case current && c != http.StatusOK:
			t.Errorf("current %q after the grace period: %d %s", v, c, b)
		case !current && (c != http.StatusUpgradeRequired || strings.TrimSpace(string(b)) != updateRequiredV120):
			t.Errorf("outdated %q after the grace period: %d %s", v, c, b)
		}
	}
	const old = "v1.1.9"
	if c, b := alice.callAs(t, h, old, "POST", "/v1/stream/ack", protocol.PingAck{Conn: protocol.NewID()}); c != http.StatusNotFound { // reached the handler: no such stream
		t.Fatalf("ping acknowledgement while suspended: %d %s", c, b)
	}
	if c, b := alice.callAs(t, h, old, "GET", "/v1/release", nil); c != http.StatusOK || !strings.Contains(string(b), `"version":"v1.2.0"`) {
		t.Fatalf("recommendation while suspended: %d %s", c, b)
	}
	if w := serve(h, httptestGet("/v1/version")); w.Code != http.StatusOK {
		t.Fatalf("version while suspended: %d", w.Code)
	}
	if c, b := alice.callAs(t, h, old, "PUT", "/v1/caps", capsFor(alice, protocol.NewID(), 1, protocol.CapEnv2)); c != http.StatusUpgradeRequired {
		t.Fatalf("caps while suspended: %d %s", c, b)
	}
	r, _ := bob.id.Public(bob.addr).Recipient()
	env, _ := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: alice.addr, To: bob.addr, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "x"}, alice.id.Sign, r)
	if c, b := alice.callAs(t, h, old, "POST", "/v1/messages", env); c != http.StatusUpgradeRequired {
		t.Fatalf("send while suspended: %d %s", c, b)
	}
	sendAs(t, h, "v1.2.0", alice, bob) // the same device, updated
}

// A development relay suspends nobody and pushes the admin's own
// recommendation as before, however old a client is.
func TestDevelopmentRelaySuspendsNobody(t *testing.T) {
	for _, build := range []string{"dev", "v1.2.0-3-gabcdef1", "v1.2.0+0760ccc"} {
		t.Run(build, func(t *testing.T) {
			h := releaseHub(t, filepath.Join(t.TempDir(), "hub"), build, -1, 0)
			defer h.Close()
			alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
			events := pushStream(t, h, bob, "v0.1.0")
			for e := range events {
				if e.name == "update_required" {
					t.Fatal("a development relay asked for an update")
				}
				if e.name == "members" {
					break
				}
			}
			sendAs(t, h, "", alice, bob)
			if m := memberOf(t, listMembers(t, h, alice), bob.addr); m.Version != "v0.1.0" || m.Suspended {
				t.Fatalf("bob: %+v", m)
			}
			if r, _ := h.currentRelease(); r != (protocol.Release{}) {
				t.Fatalf("pushed recommendation: %+v", r)
			}
		})
	}
}

// The stream of a suspended device says release and update_required, then
// carries pings only, which it may acknowledge; a message to it stays in
// custody, its own receipt is refused, and once it connects updated the
// message is delivered. The member list shows each device's reported
// version and whether it is suspended.
func TestSuspendedStreamKeepsCustody(t *testing.T) {
	const hb = 100 * time.Millisecond
	h := releaseHub(t, filepath.Join(t.TempDir(), "hub"), "v1.2.0", -1, hb)
	defer h.Close()
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	old := pushStream(t, h, bob, "v1.1.0") // its first event, release, read
	if e := <-old; e.name != "update_required" || e.data != `{"latest":"v1.2.0","url":"https://github.com/misunders2d/agentnet/releases/tag/v1.2.0"}` {
		t.Fatalf("after release: %+v", e)
	}
	id := sendAs(t, h, "v1.2.0", alice, bob)
	for until := time.Now().Add(4 * 2 * hb); time.Now().Before(until); { // several leases
		e, ok := <-old
		if !ok {
			t.Fatal("the suspended stream ended although its pings were acknowledged")
		}
		if e.name != "ping" {
			t.Fatalf("the suspended stream carried %s: %s", e.name, e.data)
		}
		var p protocol.PingAck
		json.Unmarshal([]byte(e.data), &p)
		if c, b := bob.callAs(t, h, "v1.1.0", "POST", "/v1/stream/ack", p); c != http.StatusNoContent {
			t.Fatalf("ping acknowledgement: %d %s", c, b)
		}
	}
	state := func() string {
		_, _, s, err := h.store.messageState(id, alice.addr)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := state(); s != protocol.StateCustody {
		t.Fatalf("message to the suspended device: %s", s)
	}
	if c, _ := bob.callAs(t, h, "v1.1.0", "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: protocol.StateDelivered}); c != http.StatusUpgradeRequired {
		t.Fatalf("receipt from the suspended device: %d", c)
	}
	ms := listMembersAs(t, h, alice, "v1.2.0")
	if b, a := memberOf(t, ms, bob.addr), memberOf(t, ms, alice.addr); b.Version != "v1.1.0" || !b.Suspended || b.Presence != protocol.PresenceConnected || a.Version != "" || a.Suspended {
		t.Fatalf("members: bob %+v, alice (no stream yet) %+v", b, a)
	}

	updated := pushStream(t, h, bob, "v1.2.0")
	for e := range updated {
		if e.name == "update_required" {
			t.Fatal("an updated device was asked to update")
		}
		if e.name == "message" {
			if got := envelopeID(t, e.data); got != id {
				t.Fatalf("pushed %s, want %s", got, id)
			}
			break
		}
	}
	if c, b := bob.callAs(t, h, "v1.2.0", "POST", "/v1/messages/"+id+"/ack", protocol.AckRequest{State: protocol.StateDelivered}); c != http.StatusOK {
		t.Fatalf("receipt once updated: %d %s", c, b)
	}
	if s := state(); s != protocol.StateDelivered {
		t.Fatalf("after the update: %s", s)
	}
	if b := memberOf(t, listMembersAs(t, h, alice, "v1.2.0"), bob.addr); b.Version != "v1.2.0" || b.Suspended {
		t.Fatalf("bob updated: %+v", b)
	}
}

func listMembersAs(t *testing.T, h *Hub, m member, version string) protocol.Members {
	t.Helper()
	code, body := m.callAs(t, h, version, "GET", "/v1/agents", nil)
	if code != http.StatusOK {
		t.Fatalf("list as %s: %d %s", m.addr, code, body)
	}
	var out protocol.Members
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// When the grace period ends, with nothing else happening, every stream
// gets the member list with the outdated device suspended, and that
// device's open stream turns into a suspended one.
func TestGraceEndSuspendsOpenStream(t *testing.T) {
	h := releaseHub(t, filepath.Join(t.TempDir(), "hub"), "v1.2.0", time.Hour, 500*time.Millisecond)
	defer h.Close()
	alice, bob := enroll(t, h, "alice"), enroll(t, h, "bob")
	watch := pushStream(t, h, alice, "v1.2.0")
	old := pushStream(t, h, bob, "v1.1.0")
	bobIn := func(data string) protocol.Member {
		var ms protocol.Members
		json.Unmarshal([]byte(data), &ms)
		for _, m := range ms.Members {
			if m.Address == bob.addr {
				return m
			}
		}
		return protocol.Member{}
	}
	for { // bob connected within the grace period
		e := nextEvent(t, h, alice, "v1.2.0", watch)
		if m := bobIn(e.data); e.name == "members" && m.Version == "v1.1.0" {
			if m.Suspended {
				t.Fatalf("suspended within the grace period: %+v", m)
			}
			break
		}
	}
	for e := nextEvent(t, h, bob, "v1.1.0", old); e.name != "groups"; e = nextEvent(t, h, bob, "v1.1.0", old) {
		if e.name == "update_required" {
			t.Fatal("asked to update within the grace period")
		}
	}
	backdate(t, h, time.Hour-200*time.Millisecond) // the grace period ends in 200ms
	for e := nextEvent(t, h, alice, "v1.2.0", watch); e.name != "members" || !bobIn(e.data).Suspended; e = nextEvent(t, h, alice, "v1.2.0", watch) {
	}
	for e := nextEvent(t, h, bob, "v1.1.0", old); e.name != "update_required"; e = nextEvent(t, h, bob, "v1.1.0", old) {
		if e.name == "message" || e.name == "receipt" || e.name == "members" {
			t.Fatalf("a suspended stream carried %s", e.name)
		}
	}
}

// The recommendation pushed is always the latest release: the relay's own
// over none or an older admin recommendation (which pushes nothing new),
// the admin's when it names a newer release. A newer latest gives devices
// on the relay's release a grace period of its own but none again to those
// suspended already, and the time a release was first asked for survives a
// restart, also on an upgraded relay.
func TestPushedReleaseIsLatest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	h := releaseHub(t, dir, "v1.2.0", time.Hour, 0)
	admin := enrollAdmin(t, h, "boss")
	own := protocol.Release{Version: "v1.2.0", URL: protocol.ReleaseURL("v1.2.0")}
	set := func(r protocol.Release) {
		t.Helper()
		if c, b := admin.callAs(t, h, "v1.3.0", "POST", "/v1/admin/release", protocol.ReleaseRequest{Release: r, Clear: r.Version == ""}); c != http.StatusOK {
			t.Fatalf("set %+v: %d %s", r, c, b)
		}
	}
	if r, _ := h.currentRelease(); r != own {
		t.Fatalf("no recommendation: %+v", r)
	}
	_, gen := h.currentRelease()
	set(protocol.Release{Version: "v0.8.5", URL: "https://example.test/old"}) // the stale v0.8.5 of HP-1
	if r, g := h.currentRelease(); r != own || g != gen {
		t.Fatalf("stale recommendation: %+v (generation %d, was %d)", r, g, gen)
	}
	backdate(t, h, 2*time.Hour) // v1.2.0 asked for two hours ago
	if _, required := h.updateRequired("v1.1.0"); !required {
		t.Fatal("v1.1.0 is not suspended two hours on")
	}
	newer := protocol.Release{Version: "v1.3.0", URL: "https://example.test/new", Note: "please"}
	set(newer)
	if r, g := h.currentRelease(); r != newer || g == gen {
		t.Fatalf("newer recommendation: %+v (generation %d)", r, g)
	}
	if latest, required := h.updateRequired("v1.2.0"); latest != "v1.3.0" || required {
		t.Fatalf("v1.2.0 just after v1.3.0 was asked for: %q %v", latest, required)
	}
	if _, required := h.updateRequired("v1.1.0"); !required {
		t.Fatal("a newer recommendation gave a suspended device another grace period")
	}
	set(protocol.Release{})
	if r, _ := h.currentRelease(); r != own {
		t.Fatalf("after clear: %+v", r)
	}
	h.Close()

	was := protocol.Version // restarted, upgraded: a binary stamped v1.4.0
	protocol.Version = "v1.4.0"
	t.Cleanup(func() { protocol.Version = was })
	h = releaseHub(t, dir, "", time.Hour, 0)
	defer h.Close()
	if r, _ := h.currentRelease(); r != (protocol.Release{Version: "v1.4.0", URL: protocol.ReleaseURL("v1.4.0")}) {
		t.Fatalf("upgraded relay: %+v", r)
	}
	if latest, required := h.updateRequired("v1.1.0"); latest != "v1.4.0" || !required {
		t.Fatalf("v1.1.0 after the relay's upgrade: %q %v", latest, required)
	}
	for _, v := range []string{"v1.2.0", "v1.3.0"} {
		if _, required := h.updateRequired(v); required {
			t.Fatalf("%s suspended within its grace period", v)
		}
	}
	if _, required := h.updateRequired("v1.4.0"); required {
		t.Fatal("the relay's own release is outdated")
	}
}
