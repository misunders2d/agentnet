package itest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLITaskGrantJourney: separate processes and a stand-in responder. A
// task waits; accept --always runs it and lets the next one run without
// asking; approvals shows the key; unapprove --tasks makes tasks wait again.
func TestCLITaskGrantJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	stubDir := filepath.Join(c.dir, "stub")
	os.MkdirAll(stubDir, 0o700)
	os.WriteFile(filepath.Join(stubDir, "claude"), []byte(stubClaude), 0o700)
	work := filepath.Join(c.dir, "bobwork")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "30s")
	c.env = []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("bob.log", "--home", "bob", "daemon")
	c.env = nil
	c.start("alice.log", "--home", "alice", "daemon")

	state := func(id string) string {
		for _, m := range c.inbox("bob") {
			if m.ID == id {
				return m.State
			}
		}
		return ""
	}
	results := func() int {
		n := 0
		for _, m := range c.inbox("alice") {
			if m.Kind == "result" {
				n++
			}
		}
		return n
	}
	task := func(text string) string { return strings.Fields(c.run("--home", "alice", "task", "bob/desk", text))[0] }

	t1 := task("first")
	waitFor(t, "first task waits", func() bool { return state(t1) == "awaiting" })
	fp := strings.Fields(c.run("--home", "alice", "whoami"))[2]
	out := c.run("--home", "bob", "accept", "--always", t1)
	if !strings.Contains(out, "tasks from admin/laptop (key "+fp+") now run without asking") {
		t.Fatalf("accept --always:\n%s", out)
	}
	waitFor(t, "first result", func() bool { return results() == 1 })
	t2 := task("second")
	waitFor(t, "second task ran without asking", func() bool { return state(t2) == "answered" && results() == 2 })

	if out := c.run("--home", "bob", "approvals"); !strings.Contains(out, "tasks      admin/laptop  key "+fp+"  active") {
		t.Fatalf("approvals:\n%s", out)
	}
	if out := c.run("--home", "bob", "unapprove", "--tasks", "admin/laptop"); !strings.Contains(out, "wait for you again") {
		t.Fatalf("unapprove --tasks:\n%s", out)
	}
	t3 := task("third")
	waitFor(t, "third task waits", func() bool { return state(t3) == "awaiting" })
	if out := c.run("--home", "bob", "approvals"); !strings.Contains(out, "none") {
		t.Fatalf("approvals after revoke:\n%s", out)
	}
	if _, err := c.try("--home", "bob", "accept", "--always", "0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("accept --always of an unknown id succeeded")
	}
}
