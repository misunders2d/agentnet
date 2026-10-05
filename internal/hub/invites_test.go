package hub

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestInviteLabelFromName(t *testing.T) {
	for name, want := range map[string]string{
		"Bohdan":                "bohdan",
		"Bohdan K.":             "bohdan-k",
		"  Anne-Marie  O'Neil ": "anne-marie-o-neil",
		"42 Douglas":            "douglas",
		"Богдан":                "member",
		"":                      "member",
		"R2-D2":                 "r2-d2",
		"a very long name that goes on and on and on": "a-very-long-name-that-goes-on-an",
	} {
		got := labelFromName(name)
		if got != want || !protocol.ValidName(got) {
			t.Errorf("labelFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

// adminHub is a Hub that serves browser invitations, with one admin device.
func adminHub(t *testing.T) (*Hub, member) {
	t.Helper()
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://hub.example.test", Logf: t.Logf, PlatformTLS: true, Web: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	id, _ := identity.Generate()
	if err := h.store.createInvite("s", "admin", true, time.Hour, "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.enroll("s", id.Public("admin/laptop"), "admin", nil); err != nil {
		t.Fatal(err)
	}
	return h, member{id, "admin/laptop"}
}

func createInvite(t *testing.T, h *Hub, m member, req protocol.InviteRequest) (int, protocol.InviteCreated) {
	t.Helper()
	code, body := m.call(t, h, "POST", "/v1/admin/invites", req)
	var out protocol.InviteCreated
	json.Unmarshal(body, &out)
	return code, out
}

// An invitation made from a name gets a label made from it, never one a
// person or a waiting invitation already has, and carries the names as
// hints; an unreadable hint creates nothing.
func TestInviteHintsAndLabel(t *testing.T) {
	h, admin := adminHub(t)
	joinMember(t, h, "alice")
	code, first := createInvite(t, h, admin, protocol.InviteRequest{Name: "Bohdan K", From: "Sergey", Workspace: "Mellanni", Browser: true, TTL: time.Hour})
	if code != http.StatusCreated || first.Label != "bohdan-k" {
		t.Fatalf("named invite: %d %+v", code, first)
	}
	inv, err := protocol.DecodeInvite(first.Code)
	if err != nil || inv.Label != "bohdan-k" || inv.Name != "Bohdan K" || inv.From != "Sergey" || inv.Workspace != "Mellanni" {
		t.Fatalf("decoded %+v %v", inv, err)
	}
	if _, second := createInvite(t, h, admin, protocol.InviteRequest{Name: "Bohdan K", TTL: time.Hour}); second.Label != "bohdan-k-2" {
		t.Fatalf("second invitation for the same name: %+v", second)
	}
	if _, taken := createInvite(t, h, admin, protocol.InviteRequest{Name: "Alice", TTL: time.Hour}); taken.Label != "alice-2" {
		t.Fatalf("a member's label reused: %+v", taken)
	}
	// An explicit label is the admin's choice and is kept (another device
	// of the same person, from the CLI).
	if _, kept := createInvite(t, h, admin, protocol.InviteRequest{Label: "alice", Name: "Alice", TTL: time.Hour}); kept.Label != "alice" {
		t.Fatalf("explicit label: %+v", kept)
	}
	var before int
	h.store.db.QueryRow(`SELECT count(*) FROM invites`).Scan(&before)
	for _, bad := range []protocol.InviteRequest{
		{},
		{Name: " Bob"},
		{Name: "Bob\nEve"},
		{Name: "Bob", From: string(make([]rune, protocol.MaxInviteHint+1))},
		{Label: "Not A Label"},
	} {
		if code, _ := createInvite(t, h, admin, bad); code != http.StatusBadRequest {
			t.Errorf("%+v: %d", bad, code)
		}
	}
	var after int
	h.store.db.QueryRow(`SELECT count(*) FROM invites`).Scan(&after)
	if after != before {
		t.Fatalf("refused requests created %d invites", after-before)
	}
	// A hint that is not readable text in a code is dropped when read.
	forged := protocol.Invite{Hub: "https://hub.example.test", Label: "x", Secret: "s", Name: "Eve‮", From: "  "}.Encode()
	if inv, err := protocol.DecodeInvite(forged); err != nil || inv.Name != "" || inv.From != "" {
		t.Fatalf("unreadable hints kept: %+v %v", inv, err)
	}
}

// Any member may ask whether it can invite; only an admin's device learns
// the waiting invitations, and only unused invitations admins made can be
// withdrawn: never a used, expired, bootstrap or device-link invite.
func TestAdminInvitesListRevoke(t *testing.T) {
	h, admin := adminHub(t)
	bob := joinMember(t, h, "bob")
	list := func(m member) (int, protocol.PendingInvites) {
		code, body := m.call(t, h, "GET", "/v1/admin/invites", nil)
		var out protocol.PendingInvites
		json.Unmarshal(body, &out)
		return code, out
	}
	if code, got := list(bob); code != http.StatusOK || got.CanInvite || len(got.Invites) != 0 {
		t.Fatalf("member's list: %d %+v", code, got)
	}
	_, carol := createInvite(t, h, admin, protocol.InviteRequest{Name: "Carol", TTL: time.Hour, Admin: true})
	_, dave := createInvite(t, h, admin, protocol.InviteRequest{Name: "Dave", TTL: time.Hour})
	// Not listed: an expired one, a used one, the bootstrap invite and a
	// device link's invite.
	if err := h.store.createNamedInvite("expired", "erin", "Erin", false, time.Hour, admin.addr, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.store.createInvite("boot", "admin", true, time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`INSERT INTO invites(secret_hash, label, admin, expires_at, created_by, person, offer) VALUES(?, 'admin', 0, ?, ?, 'p', 'o')`,
		protocol.HashSecret("device"), time.Now().Add(time.Hour).Unix(), admin.addr); err != nil {
		t.Fatal(err)
	}
	code, got := list(admin)
	if code != http.StatusOK || !got.CanInvite || len(got.Invites) != 2 {
		t.Fatalf("admin's list: %d %+v", code, got)
	}
	ids := map[string]protocol.PendingInvite{}
	for _, p := range got.Invites {
		ids[p.Label] = p
		if p.CreatedBy != admin.addr || p.CreatedAt == 0 || p.Expires <= time.Now().Unix() || p.ID == "" {
			t.Fatalf("listed %+v", p)
		}
	}
	if ids[carol.Label].Name != "Carol" || !ids[carol.Label].Admin || ids[dave.Label].Name != "Dave" || ids[dave.Label].Admin {
		t.Fatalf("listed %+v", ids)
	}
	if code, _ := bob.call(t, h, "POST", "/v1/admin/invites/revoke", protocol.InviteRevokeRequest{ID: ids["dave"].ID}); code != http.StatusForbidden {
		t.Fatalf("member withdrew an invitation: %d", code)
	}
	for _, other := range []string{protocol.HashSecret("expired"), protocol.HashSecret("boot"), protocol.HashSecret("device"), "nonsense"} {
		if code, _ := admin.call(t, h, "POST", "/v1/admin/invites/revoke", protocol.InviteRevokeRequest{ID: other}); code != http.StatusNotFound {
			t.Fatalf("withdrew %s: %d", other, code)
		}
	}
	if code, body := admin.call(t, h, "POST", "/v1/admin/invites/revoke", protocol.InviteRevokeRequest{ID: ids["dave"].ID}); code != http.StatusOK {
		t.Fatalf("withdraw: %d %s", code, body)
	}
	inv, _ := protocol.DecodeInvite(dave.Code)
	if code, _ := joinAs(t, h, inv.Secret, "dave/laptop", nil); code != http.StatusForbidden {
		t.Fatalf("a withdrawn invitation enrolled: %d", code)
	}
	// Once used, an invitation is no longer listed or withdrawn.
	ci, _ := protocol.DecodeInvite(carol.Code)
	if code, _ := joinAs(t, h, ci.Secret, "carol/laptop", nil); code != http.StatusCreated {
		t.Fatalf("join: %d", code)
	}
	if _, got := list(admin); len(got.Invites) != 0 {
		t.Fatalf("after use and withdrawal: %+v", got)
	}
	if code, _ := admin.call(t, h, "POST", "/v1/admin/invites/revoke", protocol.InviteRevokeRequest{ID: ids["carol"].ID}); code != http.StatusNotFound {
		t.Fatalf("withdrew a used invitation: %d", code)
	}
}
