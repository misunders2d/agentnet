package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHelpBeforeEnrollment: every help form succeeds, prints usage, and
// creates nothing, on a machine with no agent home.
func TestHelpBeforeEnrollment(t *testing.T) {
	c := buildCLI(t)
	home := filepath.Join(c.dir, "no-home-yet")
	commands := []string{"join", "whoami", "send", "ask", "task", "reply", "inbox", "download", "status",
		"sessions", "accept", "decline", "cancel", "approve", "unapprove", "approvals", "open", "update", "skill", "responder", "fingerprint", "trust",
		"daemon", "doctor", "version", "cleanup", "admin", "hub", "a2a"}
	forms := [][]string{{"--help"}, {"-h"}, {"help"}, {}}
	for _, f := range forms {
		out := c.run(append([]string{"--home", home}, f...)...)
		if !strings.Contains(out, "Get started on a laptop") {
			t.Fatalf("root help %v:\n%s", f, out)
		}
	}
	for _, cmd := range commands {
		for _, args := range [][]string{{cmd, "--help"}, {"help", cmd}, {cmd, "-h"}} {
			out := c.run(append([]string{"--home", home}, args...)...)
			if !strings.Contains(out, "Usage: agentnet") {
				t.Fatalf("%v:\n%s", args, out)
			}
		}
	}
	for _, sub := range [][]string{{"hub", "serve", "--help"}, {"hub", "backup", "--help"}, {"help", "hub restore"},
		{"responder", "set", "--help"}, {"admin", "invite", "--help"}, {"a2a", "serve", "--help"},
		{"send", "--file", "x", "--help"}} {
		if out := c.run(append([]string{"--home", home}, sub...)...); !strings.Contains(out, "Usage: agentnet") {
			t.Fatalf("%v:\n%s", sub, out)
		}
	}
	if out := c.run("--home", home, "hub", "serve", "--help"); !strings.Contains(out, "--platform-tls") {
		t.Fatalf("hub serve help lacks flags:\n%s", out)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("help created the home directory (%v)", err)
	}
	for g, title := range map[string]string{"install": "Install agentnet", "startup": "Start agentnet daemon at login",
		"uninstall": "Uninstall agentnet"} {
		for _, args := range [][]string{{g}, {"help", g}, {g, "--help"}} {
			if out := c.run(append([]string{"--home", home}, args...)...); !strings.HasPrefix(out, title) {
				t.Fatalf("guide %v:\n%s", args, out)
			}
		}
	}
	if out := c.run("--home", home, "hub", "--help"); !strings.Contains(out, "AGENTNET_UPLOAD_TTL") || strings.Contains(out, "Every flag") {
		t.Fatalf("hub help env list:\n%s", out)
	}
	// "--help" as message text after the recipient, or after --, is text.
	for _, args := range [][]string{{"send", "bob/desk", "--help"}, {"send", "--", "--help"}, {"send", "--file", "x", "bob/desk", "-h"}} {
		out, err := c.try(append([]string{"--home", home}, args...)...)
		if err == nil || strings.Contains(out, "Usage:") || !strings.Contains(out, "no enrolled agent") {
			t.Fatalf("%v treated as help: %v\n%s", args, err, out)
		}
	}
	if out := c.run("--home", home, "send", "--file", "x", "--help"); !strings.Contains(out, "Usage: agentnet send") {
		t.Fatalf("flag-position help:\n%s", out)
	}
	if out, err := c.try("help", "no-such-command"); err == nil || !strings.Contains(out, "commands:") {
		t.Fatalf("unknown topic: %v %s", err, out)
	}
	if out, err := c.try("--home", home, "frobnicate"); err == nil || !strings.Contains(out, "see agentnet --help") {
		t.Fatalf("unknown command: %v %s", err, out)
	}
}
