package itest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// progressAgent is a participation's stand-in `claude`: mid-run it sends one
// nonterminal update through the installed CLI with the worker's values,
// waits for the test, then answers with an emotion line.
const progressAgent = `#!/bin/sh
echo run >> "$0.runs"
cat > "$0.prompt"
agentnet --home "$AGENTNET_HOME" send --reply-to "$AGENTNET_REQUEST_ID" --progress "$AGENTNET_REQUESTER" "AGENT PROGRESS halfway" > "$0.progress" 2>&1 || echo "PROGRESS FAILED $?" >> "$0.progress"
while [ ! -f "$0.release" ]; do sleep 0.1; done
printf 'AGENT RESULT done\nemotion: happy\n'
`

// TestCLIDMAgentProgressThenAnswer: separate processes; an accepted DM
// agent's progress reaches the asker in the same conversation with the exact
// participation and agent origin while the request still runs; then exactly
// one answer arrives and the request is answered once.
func TestCLIDMAgentProgressThenAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	c.setup(t, "hub")
	stubDir := filepath.Join(c.dir, "stub")
	os.MkdirAll(stubDir, 0o700)
	bin := filepath.Join(stubDir, "claude")
	os.WriteFile(bin, []byte(progressAgent), 0o700)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := os.ReadFile(bin + ".progress")
			t.Logf("--- harness progress send\n%s", out)
		}
	})
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
	pid := strings.Fields(c.run("--home", "alice", "dm", "invite", conv, "bob/desk"))[0]
	waitFor(t, "bob to see the invite", func() bool { return strings.Contains(c.run("--home", "bob", "dm", "agents", conv), pid+"  invited") })
	c.run("--home", "bob", "dm", "accept-agent", pid)
	waitFor(t, "alice to see it active", func() bool { return strings.Contains(c.run("--home", "alice", "dm", "agents", conv), pid+"  active") })
	q := strings.Fields(c.run("--home", "alice", "dm", "ask-agent", "--answer-wait", "0", pid, "progress-question"))[0]

	messages := func(home string) []client.ConvMessage {
		a, err := client.Open(filepath.Join(c.dir, home))
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		msgs, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		return msgs
	}
	count := func(home, body string) (n int, last client.ConvMessage) {
		for _, m := range messages(home) {
			if m.Body == body {
				n, last = n+1, m
			}
		}
		return
	}
	waitFor(t, "progress at alice", func() bool { n, _ := count("alice", "AGENT PROGRESS halfway"); return n == 1 })
	_, p := count("alice", "AGENT PROGRESS halfway")
	if p.PID != pid || p.Origin != "agent:claude" || p.Kind != "message" || p.ReplyTo == "" {
		t.Fatalf("progress provenance %+v", p)
	}
	if !strings.Contains(dmShow(c, "bob", conv), "question pid "+pid+" (running)") {
		t.Fatalf("progress ended the run:\n%s", dmShow(c, "bob", conv))
	}
	if n, _ := count("alice", "AGENT RESULT done"); n != 0 {
		t.Fatal("answer before release")
	}
	os.WriteFile(bin+".release", nil, 0o600)
	waitFor(t, "the answer at alice", func() bool {
		return regexp.MustCompile(`\[agent:claude, happy\] answer pid ` + pid + ` \(\)  [0-9a-f]{32} lid [0-9a-f]{32}\n  AGENT RESULT done`).MatchString(dmShow(c, "alice", conv))
	})
	waitFor(t, "answered once at bob", func() bool { return strings.Contains(dmShow(c, "bob", conv), "question pid "+pid+" (answered)  "+q) })
	if n, _ := count("alice", "AGENT RESULT done"); n != 1 {
		t.Fatalf("%d answers", n)
	}
	if n, _ := count("alice", "AGENT PROGRESS halfway"); n != 1 {
		t.Fatalf("%d progress updates", n)
	}
	runs, _ := os.ReadFile(bin + ".runs")
	if strings.Count(string(runs), "run") != 1 {
		t.Fatalf("agent ran %d times", strings.Count(string(runs), "run"))
	}
}
