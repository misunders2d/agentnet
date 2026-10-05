package hub

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Only an admin names the workspace, only with a valid name; members read
// it on the member list; it survives a Hub restart and clears with "".
func TestWorkspaceNameAdminAndValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	admin, bob := enrollAdmin(t, h, "boss"), enroll(t, h, "bob")
	if c, _ := bob.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "Mellanni"}); c != http.StatusForbidden {
		t.Fatalf("non-admin: %d", c)
	}
	for _, bad := range []string{"   ", "two\nlines", "c1\u0085c1", strings.Repeat("x", 121)} {
		if c, _ := admin.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: bad}); c != http.StatusBadRequest {
			t.Fatalf("%q accepted: %d", bad, c)
		}
	}
	if c, _ := admin.call(t, h, "PUT", "/v1/admin/workspace", []byte(`{"name":"x","extra":1}`)); c != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", c)
	}
	if listMembers(t, h, bob).Workspace != "" {
		t.Fatal("refused requests changed the name")
	}
	if c, b := admin.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "  Mellanni "}); c != http.StatusOK || !strings.Contains(string(b), `"Mellanni"`) {
		t.Fatalf("set: %d %s", c, b)
	}
	if got := listMembers(t, h, bob).Workspace; got != "Mellanni" {
		t.Fatalf("member reads %q", got)
	}
	h.Close()
	h, err = Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := listMembers(t, h, bob).Workspace; got != "Mellanni" {
		t.Fatalf("after restart: %q", got)
	}
	if c, b := admin.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{}); c != http.StatusOK {
		t.Fatalf("clear: %d %s", c, b)
	}
	if got := listMembers(t, h, bob).Workspace; got != "" {
		t.Fatalf("after clear: %q", got)
	}
}

// A new name reaches connected members on the members push; setting the
// same name again pushes nothing.
func TestWorkspaceNamePushed(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: time.Minute, SessionGrace: time.Hour})
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
	guard := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer guard.Stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 2*protocol.MaxBody)
	until := func(what string, ok func(protocol.Members) bool) {
		t.Helper()
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if e, found := strings.CutPrefix(line, "event: "); found {
				event = e
			} else if d, found := strings.CutPrefix(line, "data: "); found && event == "members" {
				var m protocol.Members
				if err := json.Unmarshal([]byte(d), &m); err != nil {
					t.Fatalf("bad member list %s: %v", d, err)
				}
				if ok(m) {
					return
				}
			}
		}
		t.Fatalf("%s: stream ended: %v", what, sc.Err())
	}
	until("on connect", func(m protocol.Members) bool { return m.Workspace == "" })
	if c, b := admin.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "Mellanni"}); c != http.StatusOK {
		t.Fatalf("set: %d %s", c, b)
	}
	until("named", func(m protocol.Members) bool { return m.Workspace == "Mellanni" })
	gen := h.membersGen.Load()
	if c, _ := admin.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "Mellanni"}); c != http.StatusOK {
		t.Fatalf("same again: %d", c)
	}
	if h.membersGen.Load() != gen {
		t.Fatal("the same name pushed the member list again")
	}
}

// putCapsAs publishes m's signed caps record for session.
func putCapsAs(t *testing.T, h *Hub, m member, session string, ts int64, caps ...string) {
	t.Helper()
	rec := protocol.CapsRecord{Address: m.addr, Session: session, Caps: caps, TS: ts}
	rec.Sign(m.id.Sign)
	if c, b := m.call(t, h, "PUT", "/v1/caps", rec); c != http.StatusNoContent {
		t.Fatalf("caps: %d %s", c, b)
	}
}

// The member list says a device runs an agent while its newest signed caps
// record lists agent1; a newer record without it ends the hint, and a new
// session that has not published yet keeps the previous one (no flicker
// on reconnect).
func TestMembersAgentHint(t *testing.T) {
	h, _, _ := testHub(t)
	bob, zen := joinMember(t, h, "bob"), joinMember(t, h, "zen")
	agentOf := func() bool {
		t.Helper()
		m := listed(t, h, bob, zen.addr)
		if m == nil {
			t.Fatal("zen not listed")
		}
		return m.Agent
	}
	if agentOf() {
		t.Fatal("no caps record: no hint")
	}
	s1 := protocol.NewID()
	now := time.Now().Unix()
	gen := h.membersGen.Load()
	putCapsAs(t, h, zen, s1, now, protocol.CapAgent, protocol.CapEnv2)
	if !agentOf() {
		t.Fatal("agent1 in the newest record: hint expected")
	}
	if h.membersGen.Load() == gen {
		t.Fatal("a caps change did not push the member list")
	}
	putCapsAs(t, h, zen, s1, now+1, protocol.CapEnv2)
	if agentOf() {
		t.Fatal("a newer record without agent1 must end the hint")
	}
	putCapsAs(t, h, zen, s1, now+2, protocol.CapAgent, protocol.CapEnv2)
	// A new stream session connects: last_session moves before it publishes.
	if err := h.store.setLastSession(zen.addr, protocol.NewID()); err != nil {
		t.Fatal(err)
	}
	if !agentOf() {
		t.Fatal("a reconnect without caps yet flickered the hint off")
	}
	// An unreadable newest record counts for nothing.
	if _, err := h.store.db.Exec(`UPDATE caps SET record = 'not json' WHERE address = ?`, zen.addr); err != nil {
		t.Fatal(err)
	}
	if agentOf() {
		t.Fatal("an unreadable record gave a hint")
	}
}

// A person's devices do not inherit the admin role: a device linked into
// the admin's person, the phone or a device that runs agents, stays a
// member and is refused admin calls until the person grants it from a
// device that holds the role. Only that device's own person's linked,
// active devices can be changed, never the admin's own invite role; the
// grant can be taken back and ends when the device is removed.
func TestPersonDeviceAdminIsTheirGrant(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	bob := joinMember(t, h, "bob")
	rosters := map[string]protocol.PersonRoster{boss.addr: personOf(t, h, boss), bob.addr: personOf(t, h, bob)}
	link := func(owner member, addr string) member {
		t.Helper()
		r := rosters[owner.addr]
		offer := protocol.NewID()
		secret := deviceInvite(t, h, owner, offer)
		id, _ := identity.Generate()
		dev := member{id, addr}
		if c, e := joinLinked(t, h, secret, addr, offer, r, id); c != http.StatusCreated {
			t.Fatalf("join: %d %+v", c, e)
		}
		next := step(r, owner, &dev, append(append([]identity.Public{}, r.Devices...), id.Public(addr))...)
		if c, e := put(t, h, owner, next); c != http.StatusNoContent {
			t.Fatalf("activation: %d %+v", c, e)
		}
		rosters[owner.addr] = next
		return dev
	}
	role := func(m member) string {
		t.Helper()
		label, name, _ := protocol.SplitAddress(m.addr)
		c, b := m.call(t, h, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil)
		if c != http.StatusOK {
			t.Fatalf("profile: %d %s", c, b)
		}
		var p protocol.Profile
		json.Unmarshal(b, &p)
		return p.SelfRole
	}
	rename := func(m member, name string) int {
		t.Helper()
		c, _ := m.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: name})
		return c
	}
	grant := func(by member, address string, admin bool) int {
		t.Helper()
		c, _ := by.call(t, h, "POST", "/v1/person/device-admin", protocol.DeviceAdminRequest{Address: address, Admin: admin})
		return c
	}
	phone, zen, bobPhone := link(boss, "boss/phone"), link(boss, "boss/zenbook"), link(bob, "bob/phone")
	putCapsAs(t, h, zen, protocol.NewID(), time.Now().Unix(), protocol.CapAgent, protocol.CapEnv2) // it runs an agent

	for _, d := range []member{phone, zen, bobPhone} {
		if r := role(d); r != protocol.RoleMember {
			t.Fatalf("%s reads %q before any grant", d.addr, r)
		}
		if c := rename(d, "Mine"); c != http.StatusForbidden {
			t.Fatalf("%s named the workspace without a grant: %d", d.addr, c)
		}
	}
	if c := grant(phone, zen.addr, true); c != http.StatusForbidden {
		t.Fatalf("a device without the role granted it: %d", c)
	}
	if c := grant(bob, bobPhone.addr, true); c != http.StatusForbidden {
		t.Fatalf("a member granted the role: %d", c)
	}
	for _, addr := range []string{bobPhone.addr, boss.addr, bob.addr, "boss/nobody"} {
		if c := grant(boss, addr, true); c != http.StatusConflict {
			t.Fatalf("granted %s, not another linked device of the admin's person: %d", addr, c)
		}
	}
	// A device still waiting for its person's approval cannot be granted.
	pendingID, _ := identity.Generate()
	offer := protocol.NewID()
	if c, e := joinLinked(t, h, deviceInvite(t, h, boss, offer), "boss/tablet", offer, rosters[boss.addr], pendingID); c != http.StatusCreated {
		t.Fatalf("pending join: %d %+v", c, e)
	}
	if c := grant(boss, "boss/tablet", true); c != http.StatusConflict {
		t.Fatalf("granted a pending device: %d", c)
	}

	// The person's grant on the admin device: the phone only.
	if c := grant(boss, phone.addr, true); c != http.StatusNoContent {
		t.Fatalf("grant: %d", c)
	}
	if r := role(phone); r != protocol.RoleAdmin {
		t.Fatalf("the granted phone reads %q", r)
	}
	if c := rename(phone, "Mellanni"); c != http.StatusOK {
		t.Fatalf("the granted phone may name the workspace: %d", c)
	}
	if r := role(zen); r != protocol.RoleMember || rename(zen, "Zen") != http.StatusForbidden {
		t.Fatalf("the agent device gained the role from the phone's grant: %q", r)
	}
	// The phone cannot take the role of the device whose invite made it admin.
	if c := grant(phone, boss.addr, false); c != http.StatusConflict {
		t.Fatalf("the phone changed the admin invite's role: %d", c)
	}
	if a, _ := h.store.agent(boss.addr); !a.Admin {
		t.Fatal("the admin lost its role")
	}
	// Taken back, and again: removal from the person ends it.
	if c := grant(boss, phone.addr, false); c != http.StatusNoContent || role(phone) != protocol.RoleMember {
		t.Fatalf("withdraw: %d", c)
	}
	if c := grant(boss, phone.addr, true); c != http.StatusNoContent {
		t.Fatalf("grant again: %d", c)
	}
	r := rosters[boss.addr]
	if c, e := put(t, h, boss, step(r, boss, nil, boss.id.Public(boss.addr), zen.id.Public(zen.addr))); c != http.StatusNoContent {
		t.Fatalf("remove: %d %+v", c, e)
	}
	if c := rename(phone, "Gone"); c == http.StatusOK {
		t.Fatal("a removed phone still named the workspace")
	}
	if a, _ := h.store.agent(phone.addr); !a.Revoked {
		t.Fatalf("the removed phone was not revoked: %+v", a)
	}
}
