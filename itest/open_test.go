package itest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLIOpenReview: what a notification click runs. With a coding agent
// chosen, `open ID` starts it interactively in the responder directory
// with the review prompt as its one argument; without one it prints the
// conversation. Nothing is accepted.
func TestCLIOpenReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in coding agent is a shell script")
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.start("alice.log", "--home", "alice", "daemon")
	c.start("bob.log", "--home", "bob", "daemon")
	task := strings.Fields(c.run("--home", "alice", "task", "bob/desk", "please tidy"))[0]
	waitFor(t, "task waiting", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == task && m.State == "awaiting" {
				return true
			}
		}
		return false
	})

	// Without a default agent it says so and names exactly where to set one.
	out := c.run("--home", "bob", "open", task)
	if !strings.Contains(out, "No coding agent opened. This computer has no default agent, so questions wait for you. Set one: AgentNet → Agents → Default agent, or agentnet responder set --harness ") ||
		!strings.Contains(out, " --dir <folder>.\nShowing it here.") || !strings.Contains(out, "please tidy") {
		t.Fatalf("open without a coding agent:\n%s", out)
	}

	stub := filepath.Join(c.dir, "stub")
	os.MkdirAll(stub, 0o700)
	os.WriteFile(filepath.Join(stub, "claude"), []byte("#!/bin/sh\npwd > \"$0.cwd\"\nprintf '%s' \"$#\" > \"$0.argc\"\nprintf '%s' \"$1\" > \"$0.arg\"\n"), 0o700)
	work := filepath.Join(c.dir, "bobwork")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work)
	c.env = []string{"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH")}
	out = c.run("--home", "bob", "open", task)
	c.env = nil
	if !strings.Contains(out, "Opening claude to review this with you") {
		t.Fatalf("open:\n%s", out)
	}
	cwd, _ := os.ReadFile(filepath.Join(stub, "claude.cwd"))
	argc, _ := os.ReadFile(filepath.Join(stub, "claude.argc"))
	arg, _ := os.ReadFile(filepath.Join(stub, "claude.arg"))
	if strings.TrimSpace(string(cwd)) != work || string(argc) != "1" ||
		!strings.Contains(string(arg), "conversation "+task) || !strings.Contains(string(arg), "Do not accept") {
		t.Fatalf("coding agent started in %q with %s args:\n%s", cwd, argc, arg)
	}
	for _, m := range c.inbox("bob") {
		if m.ID == task && m.State != "awaiting" {
			t.Fatalf("open changed the task: %s", m.State)
		}
	}
	if _, err := c.try("--home", "bob", "open", "--bogus"); err == nil {
		t.Fatal("bad open arguments accepted")
	}
}
