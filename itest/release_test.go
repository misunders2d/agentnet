package itest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLIRelease: an admin recommends a version with flags before the
// version; a running member daemon saves it; `version` keeps its stdout line
// and adds the recommendation on stderr; `version` for a missing home
// creates nothing; doctor reports it.
func TestCLIRelease(t *testing.T) {
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	out := c.run("--home", "alice", "admin", "release", "set", "--url", "https://example.test/update", "--note", "for people", "v42")
	if !strings.Contains(out, "recommended client version v42") {
		t.Fatalf("set: %s", out)
	}
	if out, err := c.try("--home", "bob", "admin", "release", "set", "--url", "https://example.test/", "v1"); err == nil {
		t.Fatalf("non-admin set: %s", out)
	}
	waitFor(t, "bob saves the recommendation", func() bool {
		cmd := exec.Command(c.bin, "--home", "bob", "version")
		cmd.Dir = c.dir
		stderr, _ := cmd.StderrPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Start()
		o, _ := readAll(stdout)
		e, _ := readAll(stderr)
		cmd.Wait()
		return strings.HasPrefix(o, "agentnet dev (protocol ") && strings.Count(o, "\n") == 1 &&
			strings.Contains(e, "your Hub recommends agentnet v42 (this is dev)") && strings.Contains(e, "https://example.test/update")
	})
	if out, _ := c.try("--home", "bob", "doctor"); !strings.Contains(out, "the Hub recommends v42; this is dev") {
		t.Fatalf("doctor: %s", out)
	}
	missing := filepath.Join(c.dir, "nobody")
	if out, err := c.try("--home", missing, "version"); err != nil || strings.Contains(out, "recommends") {
		t.Fatalf("version without a home: %v %s", err, out)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("version created a home")
	}
	if out := c.run("--home", "alice", "admin", "release", "clear"); !strings.Contains(out, "no client version recommended") {
		t.Fatalf("clear: %s", out)
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String(), nil
		}
	}
}

// TestCLIReplyToResumesSession: `ask --reply-to` continues a conversation
// from the CLI; the recipient's responder resumes its session after a
// daemon restart. Unknown ids and ids with another agent are refused.
func TestCLIReplyToResumesSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	stub := filepath.Join(c.dir, "stub")
	os.MkdirAll(stub, 0o700)
	os.WriteFile(filepath.Join(stub, "claude"), []byte("#!/bin/sh\necho \"$*\" >> \"$0.args\"\ncat > /dev/null\necho answered\n"), 0o700)
	work := filepath.Join(c.dir, "work")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work)
	c.run("--home", "bob", "approve", "admin/laptop")
	c.env = []string{"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH")}
	stopBob := c.start("bob1.log", "--home", "bob", "daemon")
	c.env = nil
	c.start("alice.log", "--home", "alice", "daemon")
	answerTo := func(q string) string {
		var id string
		waitFor(t, "answer to "+q, func() bool {
			for _, m := range c.inbox("alice") {
				if m.Kind == "answer" && m.replyTo(c, q) {
					id = m.ID
					return true
				}
			}
			return false
		})
		return id
	}
	q1 := strings.Fields(c.run("--home", "alice", "ask", "bob/desk", "first"))[0]
	a1 := answerTo(q1)
	stopBob()
	c.env = []string{"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("bob2.log", "--home", "bob", "daemon")
	c.env = nil
	if out, err := c.try("--home", "alice", "ask", "--reply-to", "unknown-id", "bob/desk", "x"); err == nil || !strings.Contains(out, "no message unknown-id") {
		t.Fatalf("unknown id: %v %s", err, out)
	}
	if out, err := c.try("--home", "alice", "ask", "--reply-to", a1, "carol/desk", "x"); err == nil || !strings.Contains(out, "not carol/desk") {
		t.Fatalf("other agent: %v %s", err, out)
	}
	q2 := strings.Fields(c.run("--home", "alice", "ask", "--reply-to", a1, "bob/desk", "second"))[0]
	answerTo(q2)
	data, _ := os.ReadFile(filepath.Join(stub, "claude.args"))
	runs := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(runs) != 2 || !strings.Contains(runs[0], "--session-id ") {
		t.Fatalf("runs: %q", runs)
	}
	id := strings.Fields(runs[0][strings.Index(runs[0], "--session-id ")+len("--session-id "):])[0]
	if !strings.Contains(runs[1], "--resume "+id) {
		t.Fatalf("second run did not resume %s: %s", id, runs[1])
	}
}

// replyTo reports whether inbox entry m replies to id (read from JSON).
func (m inboxEntry) replyTo(c *cli, id string) bool {
	var all []struct {
		ID      string `json:"id"`
		ReplyTo string `json:"reply_to"`
	}
	json.Unmarshal([]byte(c.run("--home", "alice", "inbox", "--json")), &all)
	for _, x := range all {
		if x.ID == m.ID {
			return x.ReplyTo == id
		}
	}
	return false
}
