package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// lastLine returns the invite code a packet ends with.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// TestInvitationPacket: `admin invite` prints a self-contained handoff whose
// code joins a fresh home; --raw prints only the code; the bootstrap invite
// uses the same packet without an inviter.
func TestInvitationPacket(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", addr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))

	boot := c.run("hub", "bootstrap-invite", "--data", "hub")
	for _, want := range []string{"first-admin invitation", "https://github.com/misunders2d/agentnet", "--admin-label",
		"https://github.com/misunders2d/agentnet/blob/main/docs/revival/INSTALL.md", "agentnet admin invite THEIR-NAME"} {
		if !strings.Contains(boot, want) {
			t.Fatalf("bootstrap packet lacks %q:\n%s", want, boot)
		}
	}
	rawBoot := c.run("hub", "bootstrap-invite", "--raw", "--data", "hub")
	if lastLine(boot) != rawBoot {
		t.Fatal("bootstrap packet and --raw disagree on the code")
	}
	c.run("--home", "alice", "join", "--agent", "laptop", lastLine(boot))

	packet := c.run("--home", "alice", "admin", "invite", "bob")
	for _, want := range []string{
		`AgentNet invitation for "bob" from admin/laptop`,
		"Project: https://github.com/misunders2d/agentnet",
		"Install guide: https://github.com/misunders2d/agentnet/blob/main/docs/revival/INSTALL.md",
		"https://go.dev/dl/",
		"go build -trimpath -o ~/.local/bin/agentnet ./cmd/agentnet",
		`go build -trimpath -o "$bin\agentnet.exe" ./cmd/agentnet`,
		"agentnet whoami", "STOP and ask the person",
		"agentnet join --agent NAME", "agentnet daemon", "agentnet help startup", "agentnet doctor",
		`confirm this invitation is for them under the name "bob"`, "ask admin/laptop for a corrected invitation",
		"Do not choose it yourself", "address bob/NAME", "explicitly confirms",
		`agentnet send admin/laptop "bob/NAME joined AgentNet"`,
		"Do not set up an automatic responder",
		"Hub: https://" + addr, "ask\nthe person who invited you for a new invitation",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("invite packet lacks %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "e.g. laptop);") || strings.Contains(strings.ToLower(packet), "hostname") && !strings.Contains(packet, "do not use the host name") {
		t.Fatalf("packet suggests picking a name without asking:\n%s", packet)
	}
	code := lastLine(packet)
	inv, err := protocol.DecodeInvite(code)
	if err != nil || inv.Label != "bob" {
		t.Fatalf("packet code %q: %+v %v", code, inv, err)
	}
	// Without --agent nothing is created and the invite stays unused.
	out, err := c.try("--home", "bob", "join", code)
	if err == nil || !strings.Contains(out, "--agent NAME is required") || !strings.Contains(out, "bob/NAME") {
		t.Fatalf("join without --agent: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "bob")); !os.IsNotExist(err) {
		t.Fatalf("join without --agent created the home (%v)", err)
	}
	out = c.run("--home", "bob", "join", "--agent", "desk", code) // clean, isolated home
	if !strings.Contains(out, "enrolled bob/desk") {
		t.Fatalf("join: %s", out)
	}
	if _, err := c.try("--home", "bob", "join", "--agent", "other", c.run("--home", "alice", "admin", "invite", "--raw", "bob")); err == nil {
		t.Fatal("join overwrote an existing enrollment")
	}
	c.start("alice.log", "--home", "alice", "daemon")
	c.run("--home", "bob", "send", "admin/laptop", "bob/desk joined AgentNet")
	waitFor(t, "confirmation in the inviter's inbox", func() bool {
		for _, m := range c.inbox("alice") {
			if m.From == "bob/desk" && m.Body == "bob/desk joined AgentNet" {
				return true
			}
		}
		return false
	})

	raw := c.run("--home", "alice", "admin", "invite", "--raw", "carol")
	if strings.Contains(raw, "\n") || !strings.HasPrefix(raw, "agentnet-invite-v1:") {
		t.Fatalf("--raw output: %q", raw)
	}
	logs, _ := filepath.Glob(filepath.Join(c.dir, "*.log"))
	for _, l := range logs {
		data, _ := os.ReadFile(l)
		if strings.Contains(string(data), strings.TrimPrefix(code, "agentnet-invite-v1:")) {
			t.Fatalf("invite code appears in %s", l)
		}
	}
}

// TestAdminInviteNeedsLabel: a missing label is refused with an instruction
// to ask the human, and no invite is created.
func TestAdminInviteNeedsLabel(t *testing.T) {
	c := buildCLI(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", freeAddr(t))
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", c.run("hub", "bootstrap-invite", "--raw", "--data", "hub"))
	out, err := c.try("--home", "alice", "admin", "invite")
	if err == nil || !strings.Contains(out, "ask your person who is being invited") || !strings.Contains(out, "--admin does") {
		t.Fatalf("invite without label: %v\n%s", err, out)
	}
}
