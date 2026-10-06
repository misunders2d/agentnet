package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// admin workspace shows, sets and clears the name every member sees; a
// member is refused by the Hub.
func TestAdminWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	adminHome := t.TempDir()
	admin, err := client.Join(ctx, adminHome, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	code, err := admin.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	admin.Close()
	memberHome := t.TempDir()
	member, err := client.Join(ctx, memberHome, code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	member.Close()
	cli := func(home string, args ...string) (string, error) {
		return diagnosticOutput(t, func() error { return run(append([]string{"--home", home, "admin", "workspace"}, args...)) })
	}
	if out, err := cli(adminHome); err != nil || !strings.Contains(out, "no workspace name set") {
		t.Fatalf("show before: %q %v", out, err)
	}
	if out, err := cli(adminHome, "set", "  Mellanni "); err != nil || strings.TrimSpace(out) != "workspace name Mellanni" {
		t.Fatalf("set: %q %v", out, err)
	}
	if out, err := cli(memberHome, "show"); err != nil || strings.TrimSpace(out) != "workspace name Mellanni" {
		t.Fatalf("member show: %q %v", out, err)
	}
	if _, err := cli(memberHome, "set", "Mine"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("a member set the name: %v", err)
	}
	if _, err := cli(adminHome, "set", "two\nlines"); err == nil {
		t.Fatal("an invalid name was accepted")
	}
	if _, err := cli(adminHome, "set"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("set without a name: %v", err)
	}
	if out, err := cli(adminHome, "clear"); err != nil || !strings.Contains(out, "no workspace name set") {
		t.Fatalf("clear: %q %v", out, err)
	}
	if out, err := cli(memberHome); err != nil || !strings.Contains(out, "no workspace name set") {
		t.Fatalf("show after clear: %q %v", out, err)
	}
}
