package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// The workspace's own name: only an admin device may rename it for
// everyone; members read it from their overview without reloading, with
// the relay's host name beside it.
func TestWorkspaceNameLive(t *testing.T) {
	alice, bob, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := NewLive(alice)
	if v, err := admin.WorkspaceInfo(ctx); err != nil || !v.CanRename || v.Name != "" || v.Server != "127.0.0.1" {
		t.Fatalf("admin view: %+v %v", v, err)
	}
	if v, err := live.WorkspaceInfo(ctx); err != nil || v.CanRename {
		t.Fatalf("a member may rename: %+v %v", v, err)
	}
	if _, err := live.SetWorkspaceName(ctx, "Mine"); !errors.Is(err, ErrRefused) || err.Error() != renameNotAdmin {
		t.Fatalf("member rename: %v", err)
	}
	if _, err := admin.SetWorkspaceName(ctx, "two\nlines"); !errors.Is(err, ErrRefused) || err.Error() != renameInvalid {
		t.Fatalf("invalid: %v", err)
	}
	if v, err := admin.SetWorkspaceName(ctx, " Mellanni "); err != nil || v.Name != "Mellanni" {
		t.Fatalf("admin rename: %+v %v", v, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		o, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		if o.Workspace != nil && o.Workspace.Name == "Mellanni" && o.Workspace.Server == "127.0.0.1" {
			if o.Me.Agent || o.AgentDevices == nil {
				t.Fatalf("agent hint: me %v devices %v", o.Me.Agent, o.AgentDevices)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the member never saw the name: %+v", o.Workspace)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if live.HubWorkspaceName() != "Mellanni" {
		t.Fatal("binding name")
	}
	_ = bob
}

// The page's sentences name people and devices, never addresses.
func TestPeerWordsInSentences(t *testing.T) {
	alice, bob, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Body: "which port?", Kind: KindQuestion}); err != nil {
		t.Fatal(err)
	}
	o := waitReview(t, live)
	why := o.Review[0].Why
	if strings.Contains(why, alice.Address) || why != "Laptop is not approved for automatic answers" {
		t.Fatalf("review sentence: %q", why)
	}
}

// The page routes: GET /api/workspace reads the provider, POST
// /api/workspace/name refuses an unreadable name before the provider sees
// it, and the workspace list carries the workspace's own name beside this
// device's label.
func TestWorkspaceNameRoutes(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := do(t, ts, "GET", "/api/workspace", "", authed(ts, nil))
	var v WorkspaceInfoView
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Name != "Mellanni" || !v.CanRename {
		t.Fatalf("GET: %d %+v", resp.StatusCode, v)
	}
	if resp := do(t, ts, "POST", "/api/workspace/name", `{"name":"a\nb"}`, post(ts)); resp.StatusCode != 409 {
		t.Fatalf("invalid name: %d", resp.StatusCode)
	}
	if resp := do(t, ts, "POST", "/api/workspace/name", `{"name":"Acme"}`, post(ts)); resp.StatusCode != 200 {
		t.Fatalf("rename: %d", resp.StatusCode)
	}
	set := NewWorkspaceProviders()
	f := NewFixture(time.Now)
	if _, err := set.Bind(client.Workspace{ID: client.DefaultWorkspace, State: "enrolled"}, f); err != nil {
		t.Fatal(err)
	}
	if l := set.List(); len(l) != 1 || l[0].HubName != "Mellanni" || l[0].Name != "" {
		t.Fatalf("binding: %+v", l)
	}
}
