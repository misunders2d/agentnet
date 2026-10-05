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

// A device linked into a person holds that person's admin role: the
// admin's phone may name the workspace and reads self_role admin. A member's
// phone does not, and the role ends when the admin device is revoked.
func TestPersonDevicesInheritAdmin(t *testing.T) {
	h, _, _ := testHub(t)
	boss := enrollAdmin(t, h, "boss")
	bob := joinMember(t, h, "bob")
	link := func(owner member, addr string) member {
		t.Helper()
		r0 := personOf(t, h, owner)
		offer := protocol.NewID()
		secret := deviceInvite(t, h, owner, offer)
		id, _ := identity.Generate()
		phone := member{id, addr}
		if c, e := joinLinked(t, h, secret, addr, offer, r0, id); c != http.StatusCreated {
			t.Fatalf("join: %d %+v", c, e)
		}
		if c, e := put(t, h, owner, step(r0, owner, &phone, owner.id.Public(owner.addr), id.Public(addr))); c != http.StatusNoContent {
			t.Fatalf("activation: %d %+v", c, e)
		}
		return phone
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
	bossPhone, bobPhone := link(boss, "boss/phone"), link(bob, "bob/phone")
	if r := role(bossPhone); r != protocol.RoleAdmin {
		t.Fatalf("the admin's phone reads %q", r)
	}
	if c, b := bossPhone.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "Mellanni"}); c != http.StatusOK {
		t.Fatalf("the admin's phone may name the workspace: %d %s", c, b)
	}
	if r := role(bobPhone); r != protocol.RoleMember {
		t.Fatalf("a member's phone reads %q", r)
	}
	if c, _ := bobPhone.call(t, h, "PUT", "/v1/admin/workspace", protocol.WorkspaceNameRequest{Name: "Mine"}); c != http.StatusForbidden {
		t.Fatalf("a member's phone named the workspace: %d", c)
	}
	if err := h.store.revoke(boss.addr); err != nil {
		t.Fatal(err)
	}
	if a, err := h.store.agent(bossPhone.addr); err != nil || a.Admin {
		t.Fatalf("the phone kept admin after its admin device was revoked: %+v %v", a.Admin, err)
	}
}
