package client

import (
	"bytes"
	"errors"
	"github.com/misunders2d/agentnet/internal/testhub"
	"os"
	"path/filepath"
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
