package hub

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// joinMember enrolls label/x through the join API, as a new member would.
func joinMember(t *testing.T, h *Hub, label string) member {
	t.Helper()
	id, _ := identity.Generate()
	secret := protocol.NewID()
	if err := h.store.createInvite(secret, label, false, time.Hour, "admin/test"); err != nil {
		t.Fatal(err)
	}
	if code, e := joinAs(t, h, secret, label+"/x", id); code != http.StatusCreated {
		t.Fatalf("join %s: %d %+v", label, code, e)
	}
	return member{id, label + "/x"}
}

func listMembers(t *testing.T, h *Hub, m member) protocol.Members {
	t.Helper()
	code, body := m.call(t, h, "GET", "/v1/agents", nil)
	if code != http.StatusOK {
		t.Fatalf("list as %s: %d %s", m.addr, code, body)
	}
	var out protocol.Members
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Valid(); err != nil {
		t.Fatal(err)
	}
	return out
}

func presenceOf(ms protocol.Members) map[string]string {
	out := map[string]string{}
	for _, m := range ms.Members {
		out[m.Address] = m.Presence
	}
	return out
}

func testAd(m member) protocol.SessionAd {
	ad := protocol.SessionAd{Address: m.addr, Session: protocol.NewID()}
	protocol.SignAd(&ad, m.id.Sign)
	return ad
}

// Every enrolled, unrevoked agent can list the others, most recently joined
// first, with presence as the Hub sees it; revoked agents are neither listed
// nor allowed to list, and an unsigned request is refused.
func TestMembersList(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, SessionGrace: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	admin := enrollAdmin(t, h, "boss")
	bob, vit := joinMember(t, h, "bob"), joinMember(t, h, "vitalii")
	for i, addr := range []string{admin.addr, bob.addr, vit.addr} { // distinct join times
		h.store.db.Exec(`UPDATE agents SET created_at = ? WHERE address = ?`, 1000+i, addr)
	}
	got := listMembers(t, h, bob)
	var order []string
	for _, m := range got.Members {
		order = append(order, m.Address)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{vit.addr, bob.addr, admin.addr}) || got.Truncated {
		t.Fatalf("list %v truncated=%v", order, got.Truncated)
	}
	if p := presenceOf(got); p[vit.addr] != protocol.PresenceOffline || got.Members[0].Joined != 1002 {
		t.Fatalf("new member: %+v", got.Members[0])
	}

	ad := testAd(vit)
	h.presence.connect(vit.addr, ad)
	if p := presenceOf(listMembers(t, h, bob)); p[vit.addr] != protocol.PresenceConnected {
		t.Fatalf("connected: %v", p)
	}
	h.presence.disconnect(vit.addr, ad.Session)
	if p := presenceOf(listMembers(t, h, bob)); p[vit.addr] != protocol.PresenceReconnecting {
		t.Fatalf("within grace: %v", p)
	}
	deadline := time.Now().Add(5 * time.Second)
	for presenceOf(listMembers(t, h, bob))[vit.addr] != protocol.PresenceOffline {
		if time.Now().After(deadline) {
			t.Fatal("still listed as reconnecting after the grace period")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if c, b := admin.call(t, h, "POST", "/v1/admin/revoke", protocol.RevokeRequest{Address: vit.addr}); c != http.StatusOK {
		t.Fatalf("revoke: %d %s", c, b)
	}
	if p := presenceOf(listMembers(t, h, bob)); len(p) != 2 || p[vit.addr] != "" {
		t.Fatalf("revoked agent listed: %v", p)
	}
	if c, _ := vit.call(t, h, "GET", "/v1/agents", nil); c != http.StatusForbidden {
		t.Fatalf("revoked agent listed the members: %d", c)
	}
	if w := serve(h, httptest.NewRequest("GET", "/v1/agents", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned request: %d", w.Code)
	}
}

// Presence changes are reported once per change of an agent's state:
// connected, reconnecting (all sessions within grace) or offline; and once
// more when a session ends while others live, since the device's
// capabilities are what its live sessions all support (profile.go).
func TestPresenceStateChanges(t *testing.T) {
	var mu sync.Mutex // grace ends report from timer goroutines
	var changes []string
	done := make(chan struct{}, 8)
	// Exercise state transitions in a fixed order. Equal-deadline grace
	// callbacks may both end before onChange reads the current state.
	// Real timer expiry is covered by TestSessionEndWhileConnectedNotifies.
	p := &presence{grace: time.Hour, onEnd: func(string, string) {}}
	t.Cleanup(p.close)
	p.onChange = func(a string) { // called without the presence lock
		mu.Lock()
		changes = append(changes, a+" "+p.state(a))
		mu.Unlock()
		done <- struct{}{}
	}
	a1 := protocol.SessionAd{Address: "a/x", Session: "s1"}
	a2 := protocol.SessionAd{Address: "a/x", Session: "s2"}
	p.connect("a/x", a1) // offline -> connected
	p.connect("a/x", a2) // still connected
	p.disconnect("a/x", "s1")
	p.disconnect("a/x", "s2") // -> reconnecting
	<-done
	<-done
	p.end("a/x", "s1", p.sessions["a/x"]["s1"])
	<-done // s1 ends after grace while s2 lives: reported (capabilities)
	p.end("a/x", "s2", p.sessions["a/x"]["s2"])
	<-done // s2 ends: -> offline
	p.connect("a/x", a1)
	p.drop("a/x") // connected -> offline, at once
	want := []string{"a/x connected", "a/x reconnecting", "a/x reconnecting", "a/x offline", "a/x connected", "a/x offline"}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(changes) != fmt.Sprint(want) {
		t.Fatalf("changes %v, want %v", changes, want)
	}
}

// The list is pushed on each push stream: on connect (with the stream's own
// agent connected), then whenever an agent joins, connects, disconnects or
// is revoked. The stream announces this with MembersHeader.
func TestMembersPushed(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: time.Minute, SessionGrace: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	admin, bob := enrollAdmin(t, h, "boss"), joinMember(t, h, "bob")
	srv := httptest.NewUnstartedServer(h.routes())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ad := testAd(bob)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/stream?ad="+ad.Encode(), nil)
	protocol.SignRequest(req, bob.addr, bob.id.Sign, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get(protocol.MembersHeader) != "1" {
		t.Fatalf("stream lacks %s", protocol.MembersHeader)
	}
	guard := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer guard.Stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 2*protocol.MaxBody)
	// until reads member lists until one satisfies ok (changes may arrive
	// merged into one list).
	until := func(what string, ok func(map[string]string) bool) {
		t.Helper()
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if e, found := strings.CutPrefix(line, "event: "); found {
				event = e
			} else if d, found := strings.CutPrefix(line, "data: "); found && event == "members" {
				var m protocol.Members
				if err := json.Unmarshal([]byte(d), &m); err != nil || m.Valid() != nil {
					t.Fatalf("bad member list %s: %v", d, err)
				}
				if ok(presenceOf(m)) {
					return
				}
			}
		}
		t.Fatalf("%s: stream ended: %v", what, sc.Err())
	}
	until("on connect", func(p map[string]string) bool {
		return p[bob.addr] == protocol.PresenceConnected && p[admin.addr] == protocol.PresenceOffline
	})
	vit := joinMember(t, h, "vitalii")
	until("new member", func(p map[string]string) bool { return p[vit.addr] == protocol.PresenceOffline })
	vad := testAd(vit)
	h.presence.connect(vit.addr, vad)
	until("connected", func(p map[string]string) bool { return p[vit.addr] == protocol.PresenceConnected })
	h.presence.disconnect(vit.addr, vad.Session)
	until("reconnecting", func(p map[string]string) bool { return p[vit.addr] == protocol.PresenceReconnecting })
	until("offline after grace", func(p map[string]string) bool { return p[vit.addr] == protocol.PresenceOffline })
	if c, b := admin.call(t, h, "POST", "/v1/admin/revoke", protocol.RevokeRequest{Address: vit.addr}); c != http.StatusOK {
		t.Fatalf("revoke: %d %s", c, b)
	}
	until("revoked", func(p map[string]string) bool { _, listed := p[vit.addr]; return !listed })

	// Rapid changes while the stream builds and sends lists: however they
	// are merged, the list that reflects the last one arrives (the
	// generation is read before the list is built).
	for i := range 15 {
		joinMember(t, h, fmt.Sprintf("burst%d", i))
	}
	until("after a burst of joins", func(p map[string]string) bool { _, ok := p["burst14/x"]; return ok && len(p) == 17 })
}

// At most MaxMembers are listed, most recent first, with Truncated set; the
// list stays well below the size limit of one pushed event.
func TestMembersBounded(t *testing.T) {
	h, _, addr := testHub(t)
	h.store.db.Exec(`UPDATE agents SET created_at = 1 WHERE address = ?`, addr) // the oldest
	tx, _ := h.store.db.Begin()
	for i := range protocol.MaxMembers + 1 {
		label := fmt.Sprintf("p%04d", i)
		if _, err := tx.Exec(`INSERT INTO agents(address, label, public, admin, created_at) VALUES(?, ?, '{}', 0, ?)`,
			label+"/agent-with-a-long-name", label, 2000+i); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	m, err := h.members()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if len(m.Members) != protocol.MaxMembers || !m.Truncated || m.Members[0].Address != "p1000/agent-with-a-long-name" {
		t.Fatalf("%d members, truncated %v, first %s", len(m.Members), m.Truncated, m.Members[0].Address)
	}
	if len(data) > protocol.MaxBody/4 {
		t.Fatalf("a full list is %d bytes", len(data))
	}
	for _, e := range m.Members {
		if e.Address == addr {
			t.Fatal("the oldest member should be the one left out")
		}
	}
}
