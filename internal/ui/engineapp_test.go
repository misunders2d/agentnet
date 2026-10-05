package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The browser engine serves the app routes as the daemon's page does:
// an admin's browser sees whether it may invite and the waiting
// invitations and withdraws one; another member may not; a server whose
// page browsers cannot trust refuses a link in plain words; the app's
// downloads come from getapp.json; folders are refused; a device link
// carries its app link. Two browsers given the same label and device name
// get the next free name, never a question.
func TestBrowserEngineInvites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.StartConfig(t, hub.Config{DataDir: dir, Web: true}, "127.0.0.1:0")
	laptop, err := client.Join(ctx, filepath.Join(t.TempDir(), "laptop"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	adminCode, _ := laptop.Invite(ctx, "eve", time.Hour, true)
	memberCode, _ := laptop.Invite(ctx, "eve", time.Hour, false)
	if _, err := laptop.Invite(ctx, "dana", time.Hour, false); err != nil {
		t.Fatal(err)
	}
	base := "https://" + h.Addr
	admin := startEngineNode(t, dir)
	admin.ok(map[string]any{"op": "init", "base": base})
	if a := admin.ok(map[string]any{"op": "joinAuto", "code": browserCode(t, adminCode), "base": "android-phone"})["address"]; a != "eve/android-phone" {
		t.Fatalf("first browser: %v", a)
	}
	member := startEngineNode(t, dir)
	member.ok(map[string]any{"op": "init", "base": base})
	if a := member.ok(map[string]any{"op": "joinAuto", "code": browserCode(t, memberCode), "base": "android-phone"})["address"]; a != "eve/android-phone-2" {
		t.Fatalf("a taken name was not followed by the next one: %v", a)
	}
	list := admin.api("/api/invites", nil)
	invites, _ := list["invites"].([]any)
	if list["can_invite"] != true || len(invites) != 1 || invites[0].(map[string]any)["label"] != "dana" {
		t.Fatalf("admin's list: %v", list)
	}
	if m := member.api("/api/invites", nil); m["can_invite"] != false || len(m["invites"].([]any)) != 0 {
		t.Fatalf("member's list: %v", m)
	}
	id := invites[0].(map[string]any)["id"].(string)
	member.refuses("a member withdraws", member.call(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}}), "Only an admin")
	admin.refuses("no name", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": " ", "days": 7}}), "Write the name")
	admin.refuses("3 days", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": "Bob", "days": 3}}), "1, 7 or 30 days")
	admin.refuses("a link from a pinned server", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": "Bob", "days": 7}}), "browsers trust")
	admin.ok(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}})
	admin.refuses("withdrawn twice", admin.call(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}}), "already used")
	app := admin.api("/api/get-app", nil)
	if ps, _ := app["platforms"].([]any); len(ps) < 5 || !strings.Contains(ps[0].(map[string]any)["url"].(string), "/releases/") {
		t.Fatalf("get-app: %v", app)
	}
	admin.refuses("folders in a browser", admin.call(map[string]any{"op": "api", "path": "/api/folders"}), "AgentNet app")
	admin.ok(map[string]any{"op": "person", "label": "Eve"})
	admin.ok(map[string]any{"op": "start"})
	var link map[string]any
	admin.until("a device link", func() bool {
		v := admin.call(map[string]any{"op": "api", "path": "/api/device/link", "body": map[string]any{}})
		link, _ = v["v"].(map[string]any)
		return link != nil
	})
	if u, _ := link["url"].(string); link["app_url"] != "agentnet://open#"+u[strings.Index(u, "#")+1:] {
		t.Fatalf("device link %v", link)
	}
}

// The browser reads an invitation's hints exactly as Go does, dropping
// what is not readable text.
func TestInviteDecodeParity(t *testing.T) {
	w := startEngineNode(t, t.TempDir())
	for _, inv := range []protocol.Invite{
		{Hub: "https://hub.example.test", Label: "bohdan", Secret: "s", Name: "Bohdan K", From: "Sergey", Workspace: "Mellanni"},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s", Name: "Eve‮", From: " Sergey", Workspace: strings.Repeat("w", protocol.MaxWorkspaceHint+1)},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s", Name: "Анна-Марія", From: "O'Neil & Co."},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s"},
	} {
		code := inv.Encode()
		goInv, err := protocol.DecodeInvite(code)
		if err != nil {
			t.Fatal(err)
		}
		js := w.ok(map[string]any{"op": "decodeInvite", "code": code})["v"].(map[string]any)
		if js["name"] != goInv.Name || js["from"] != goInv.From || js["workspace"] != goInv.Workspace || js["label"] != goInv.Label {
			t.Errorf("%+v: browser %v, Go %+v", inv, js, goInv)
		}
	}
}
