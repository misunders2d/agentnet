package itest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// stubAgent is a stand-in `claude` for a participation's agent: it keeps
// its prompt and replies with an emotion line; for a request marked
// slow-question it first runs until stopped.
const stubAgent = `#!/bin/sh
cat > "$0.prompt"
case "$(cat "$0.prompt")" in *slow-question*) touch "$0.started"; sleep 30 ;; esac
printf 'release notes look fine\nemotion: happy\n'
`

// TestCLIDMAgent: through the dm CLI and separate processes, an invited and
// accepted agent answers in the DM with exactly the shared message and the
// agent's own emotion, and a dismissal from the other person stops a run in
// progress and keeps its output from being sent.
func TestCLIDMAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	c.setup(t, "hub")
	stubDir := filepath.Join(c.dir, "stub")
	os.MkdirAll(stubDir, 0o700)
	os.WriteFile(filepath.Join(stubDir, "claude"), []byte(stubAgent), 0o700)
	work := filepath.Join(c.dir, "bobwork")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "60s")
	c.env = []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("bob.log", "--home", "bob", "daemon")
	c.env = nil
	c.start("alice.log", "--home", "alice", "daemon")
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	var conv string
	waitFor(t, "a DM with bob", func() bool {
		out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		conv = out
		return err == nil
	})
	c.run("--home", "alice", "dm", "send", conv, "deploy failed at step 3")
	c.run("--home", "alice", "dm", "send", conv, "unrelated: lunch?")
	shared := regexp.MustCompile(`lid ([0-9a-f]{32})\n  deploy failed at step 3`).FindStringSubmatch(dmShow(c, "alice", conv))
	if shared == nil {
		t.Fatalf("no logical id shown:\n%s", dmShow(c, "alice", conv))
	}

	invite := func() string {
		pid := strings.Fields(c.run("--home", "alice", "dm", "invite", "--grant", shared[1], "--note", "check the deploy", conv, "bob/desk"))[0]
		waitFor(t, "bob to see the invite", func() bool { return strings.Contains(c.run("--home", "bob", "dm", "agents", conv), pid+"  invited") })
		c.run("--home", "bob", "dm", "accept-agent", pid)
		waitFor(t, "alice to see it active", func() bool { return strings.Contains(c.run("--home", "alice", "dm", "agents", conv), pid+"  active") })
		return pid
	}
	pid := invite()
	q := strings.Fields(c.run("--home", "alice", "dm", "ask-agent", pid, "what failed?"))[0]
	waitFor(t, "the agent's answer at alice", func() bool {
		return regexp.MustCompile(`in bob/desk \[agent:claude, happy\] answer pid ` + pid + ` \(\)  [0-9a-f]{32} lid [0-9a-f]{32}\n  release notes look fine`).
			MatchString(dmShow(c, "alice", conv))
	})
	prompt, _ := os.ReadFile(filepath.Join(stubDir, "claude.prompt"))
	if !strings.Contains(string(prompt), "deploy failed at step 3") || strings.Contains(string(prompt), "lunch") ||
		strings.Count(string(prompt), "what failed?") != 1 || !strings.Contains(string(prompt), "check the deploy") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if !strings.Contains(dmShow(c, "bob", conv), "question pid "+pid+" (answered)  "+q) {
		t.Fatalf("bob's view:\n%s", dmShow(c, "bob", conv))
	}

	// A dismissal by the other person while the agent works: the run stops,
	// nothing is sent, the output stays with bob.
	pid2 := invite()
	slow := strings.Fields(c.run("--home", "alice", "dm", "ask-agent", pid2, "slow-question"))[0]
	waitFile(t, filepath.Join(stubDir, "claude.started"))
	c.run("--home", "alice", "dm", "dismiss-agent", pid2)
	waitFor(t, "the run to stop at bob", func() bool {
		return strings.Contains(dmShow(c, "bob", conv), "question pid "+pid2+" (not_delivered: stopped and not sent: the agent's participation is dismissed")
	})
	if strings.Contains(dmShow(c, "alice", conv), "reply "+slow) || strings.Count(dmShow(c, "alice", conv), "release notes look fine") != 1 {
		t.Fatalf("alice got output after dismissing:\n%s", dmShow(c, "alice", conv))
	}
	if out, err := c.try("--home", "alice", "dm", "ask-agent", pid2, "again?"); err == nil {
		t.Fatalf("asked a dismissed agent: %s", out)
	}
}
