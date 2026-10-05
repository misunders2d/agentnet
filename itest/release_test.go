package itest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestCLIRelease: an admin recommends a version with flags before the
// version; a running member daemon saves it; `version` of a release build
// keeps its identity and build diagnostic lines on stdout and adds a newer
// recommendation on stderr, while a
// development build that cannot be compared says nothing; `version` for a
// missing home creates nothing; doctor reports it.
func TestCLIRelease(t *testing.T) {
	c := buildCLI(t)
	devBin := c.bin
	c.bin = filepath.Join(c.dir, "release", filepath.Base(devBin))
	os.MkdirAll(filepath.Dir(c.bin), 0o700)
	buildVersion(t, c.bin, "v0.3.0", "https://example.test/releases")
	_, _ = c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	out := c.run("--home", "alice", "admin", "release", "set", "--url", "https://example.test/update", "--note", "for people", "v0.3.1")
	if !strings.Contains(out, "recommended client version v0.3.1") {
		t.Fatalf("set: %s", out)
	}
	if out, err := c.try("--home", "bob", "admin", "release", "set", "--url", "https://example.test/", "v9.9.9"); err == nil {
		t.Fatalf("non-admin set: %s", out)
	}
	version := func(bin string) (stdout, stderr string) {
		cmd := exec.Command(bin, "--home", "bob", "version")
		cmd.Dir = c.dir
		e, _ := cmd.StderrPipe()
		o, _ := cmd.StdoutPipe()
		cmd.Start()
		stdout, _ = readAll(o)
		stderr, _ = readAll(e)
		cmd.Wait()
		return stdout, stderr
	}
	versionOutput := func(out, version string) bool {
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if len(lines) != 2 || lines[0] != fmt.Sprintf("agentnet %s (protocol %d)", version, protocol.ProtocolVersion) {
			return false
		}
		diagnostic, ok := strings.CutPrefix(lines[1], "build revision ")
		revision, tree, found := strings.Cut(diagnostic, ", working tree ")
		return ok && found && revision != "" && (tree == "clean" || tree == "dirty" || tree == "unknown")
	}
	waitFor(t, "bob saves the recommendation", func() bool {
		o, e := version(c.bin)
		return versionOutput(o, "v0.3.0") &&
			strings.Contains(e, "your Hub recommends agentnet v0.3.1 (this is v0.3.0)") && strings.Contains(e, "https://example.test/update")
	})
	if out, _ := c.try("--home", "bob", "doctor"); !strings.Contains(out, "the Hub recommends v0.3.1; this is v0.3.0") {
		t.Fatalf("doctor: %s", out)
	}
	// A development build cannot be compared with a release: never told.
	if o, e := version(devBin); !versionOutput(o, "dev") || strings.Contains(e, "recommends") {
		t.Fatalf("dev build: %q %q", o, e)
	}
	dev := *c
	dev.bin = devBin
	if out, _ := dev.try("--home", "bob", "doctor"); !strings.Contains(out, "the Hub recommends v0.3.1; this build is dev: no comparable newer recommendation") {
		t.Fatalf("dev doctor: %s", out)
	}
	missing := filepath.Join(c.dir, "nobody")
	if out, err := c.try("--home", missing, "version"); err != nil || !versionOutput(out, "v0.3.0") || strings.Contains(out, "recommends") {
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
	q1 := strings.Fields(c.run("--home", "alice", "ask", "--answer-wait", "0", "bob/desk", "first"))[0]
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
	q2 := strings.Fields(c.run("--home", "alice", "ask", "--answer-wait", "0", "--reply-to", a1, "bob/desk", "second"))[0]
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
