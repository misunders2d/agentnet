package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLILinkedDeviceGetsHistory: alice links her phone from the command
// line while her laptop's daemon keeps running; the approval, in its own
// process, makes that daemon send her chats to the phone (no reconnect, no
// restart).
func TestCLILinkedDeviceGetsHistory(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", addr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.start("alice.log", "--home", "alice", "daemon")
	c.start("bob.log", "--home", "bob", "daemon")
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	var conv string
	waitFor(t, "a DM with bob", func() bool {
		out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		conv = strings.TrimSpace(out)
		return err == nil
	})
	c.run("--home", "alice", "dm", "send", conv, "before the phone")
	waitFor(t, "bob to have it", func() bool { return strings.Contains(dmShowArriving(c, "bob", conv), "before the phone") })

	var link string
	for _, line := range strings.Split(c.run("--home", "alice", "person", "link"), "\n") {
		if strings.HasPrefix(line, "agentnet-link-v2:") {
			link = line
		}
	}
	if link == "" {
		t.Fatal("no link code")
	}
	c.run("--home", "phone", "join", "--agent", "phone", link)
	c.start("phone.log", "--home", "phone", "daemon")
	var id string
	waitFor(t, "the phone's request at the laptop", func() bool {
		for _, line := range strings.Split(c.run("--home", "alice", "person", "links"), "\n") {
			if f := strings.Fields(line); len(f) > 2 && f[1] == "pending" && f[2] == "admin/phone" {
				id = f[0]
			}
		}
		return id != ""
	})
	c.run("--home", "alice", "person", "approve", id)
	waitFor(t, "the history on the phone", func() bool {
		out, err := c.try("--home", "phone", "dm", "show", conv)
		return err == nil && strings.Contains(out, "before the phone")
	})
}
