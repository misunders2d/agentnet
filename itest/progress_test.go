package itest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// progressResponder is a stand-in `claude` that, mid-run, sends one explicit
// nonterminal update through the installed CLI with the worker's
// authoritative values, then waits for the test before its final result.
const progressResponder = `#!/bin/sh
echo run >> "$0.runs"
cat > "$0.prompt"
agentnet --home "$AGENTNET_HOME" send --reply-to "$AGENTNET_REQUEST_ID" --progress "$AGENTNET_REQUESTER" "STUB PROGRESS halfway" > "$0.progress" 2>&1 || echo "PROGRESS FAILED $?" >> "$0.progress"
while [ ! -f "$0.release" ]; do sleep 0.1; done
echo "STUB RESULT done"
`

// followUpResponder is the requester's stand-in: it counts follow-up runs.
const followUpResponder = `#!/bin/sh
echo run >> "$0.runs"
cat > "$0.prompt"
echo "follow-up summary"
`

type progressEntry struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to"`
}

func (c *cli) entries(home string) []progressEntry {
	var out []progressEntry
	json.Unmarshal([]byte(c.run("--home", home, "inbox", "--json")), &out)
	return out
}

func lines(path string) int {
	data, _ := os.ReadFile(path)
	return strings.Count(string(data), "\n")
}

// TestCLIProgressThenResultJourney: separate processes, a stand-in harness
// started by the real daemon. The harness's progress update reaches the
// requester while the task is still running; the task stays running and
// waiting until the harness finishes; then exactly one result and exactly one
// follow-up run arrive.
func TestCLIProgressThenResultJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	stub := func(name, script string) (dir, bin string) {
		dir = filepath.Join(c.dir, name)
		os.MkdirAll(dir, 0o700)
		bin = filepath.Join(dir, "claude")
		os.WriteFile(bin, []byte(script), 0o700)
		return dir, bin
	}
	bobStub, bobBin := stub("bobstub", progressResponder)
	aliceStub, aliceBin := stub("alicestub", followUpResponder)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := os.ReadFile(bobBin + ".progress")
			t.Logf("--- harness progress send\n%s", out)
		}
	})
	for _, who := range []string{"bob", "alice"} {
		work := filepath.Join(c.dir, who+"work")
		os.MkdirAll(work, 0o700)
		c.run("--home", who, "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "60s")
	}
	// PATH holds only the stand-in: the daemon itself must put agentnet on it.
	c.env = []string{"PATH=" + bobStub + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("bob.log", "--home", "bob", "daemon")
	c.env = []string{"PATH=" + aliceStub + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("alice.log", "--home", "alice", "daemon")
	c.env = nil

	task := strings.Fields(c.run("--home", "alice", "task", "--follow-up", "summarize it", "bob/desk", "do the work"))[0]
	stateOf := func(id string) string {
		for _, m := range c.entries("bob") {
			if m.ID == id {
				return m.State
			}
		}
		return ""
	}
	replies := func(kind, status string) []progressEntry {
		var out []progressEntry
		for _, m := range c.entries("alice") {
			if m.ReplyTo == task && m.Kind == kind && m.Status == status {
				out = append(out, m)
			}
		}
		return out
	}
	waiting := func() bool {
		a, err := client.Open(filepath.Join(c.dir, "alice"))
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		threads, err := a.Threads()
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range threads {
			if th.ID == task {
				return th.Waiting
			}
		}
		t.Fatalf("no thread %s: %+v", task, threads)
		return false
	}
	waitFor(t, "task waits", func() bool { return stateOf(task) == "awaiting" })
	c.run("--home", "bob", "accept", task)

	waitFor(t, "progress at requester", func() bool { return len(replies("message", "progress")) == 1 })
	if p := replies("message", "progress")[0]; p.Body != "STUB PROGRESS halfway" {
		t.Fatalf("progress = %+v", p)
	}
	progressOut, _ := os.ReadFile(bobBin + ".progress")
	if strings.Contains(string(progressOut), "FAILED") {
		t.Fatalf("harness progress send: %s", progressOut)
	}
	// Progress settles nothing: the task runs on, the requester still waits,
	// and no follow-up has run.
	time.Sleep(500 * time.Millisecond)
	if s := stateOf(task); s != "running" {
		t.Fatalf("task state after progress = %q", s)
	}
	if len(replies("result", "done")) != 0 || lines(aliceBin+".runs") != 0 || !waiting() {
		t.Fatal("progress finished the request or ran the follow-up")
	}
	prompt, _ := os.ReadFile(bobBin + ".prompt")
	if !strings.Contains(string(prompt), "send --reply-to <AGENTNET_REQUEST_ID> --progress <AGENTNET_REQUESTER>") {
		t.Fatalf("worker prompt:\n%s", prompt)
	}

	os.WriteFile(bobBin+".release", nil, 0o600)
	waitFor(t, "terminal result", func() bool { return len(replies("result", "done")) == 1 })
	if r := replies("result", "done")[0]; r.Body != "STUB RESULT done" {
		t.Fatalf("result = %+v", r)
	}
	waitFor(t, "task answered", func() bool { return stateOf(task) == "answered" })
	waitFor(t, "follow-up ran", func() bool { return lines(aliceBin+".runs") == 1 })
	time.Sleep(time.Second)
	if n := len(replies("result", "done")); n != 1 {
		t.Fatalf("%d results", n)
	}
	if n := len(replies("message", "progress")); n != 1 {
		t.Fatalf("%d progress updates", n)
	}
	if lines(aliceBin+".runs") != 1 || lines(bobBin+".runs") != 1 {
		t.Fatalf("runs: follow-up %d, responder %d", lines(aliceBin+".runs"), lines(bobBin+".runs"))
	}
	if waiting() {
		t.Fatal("result did not end waiting")
	}
}
