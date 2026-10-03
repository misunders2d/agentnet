package itest

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubOutboxClaude is a stand-in `claude` for a task run: it copies the
// file the task brought (the read-only copy its prompt names) into the
// outbox its prompt names, then reports.
const stubOutboxClaude = `#!/bin/sh
prompt=$(cat)
printf '%s\n' "$prompt" > "$0.prompt"
in=$(printf '%s\n' "$prompt" | sed -n 's/^- "notes.bin" .*: "\([^"]*\)"$/\1/p')
out=$(printf '%s\n' "$prompt" | sed -n 's/.*place them directly in "\([^"]*\)".*/\1/p')
if [ -n "$in" ] && [ -n "$out" ]; then cp "$in" "$out/x.bin"; fi
echo "report written"
`

// TestCLIOutboxCompat is the P0f gate's compatibility check (ROOM_V1 §9):
// a requester running the released version named by AGENTNET_COMPAT_TAG
// (see buildCompatCLI) sends a device-thread task with a file; this build
// runs it, its run copies that file into its outbox, and the older
// requester receives the result with the file attached and downloads the
// same bytes. Opt-in, like TestCLIMembersCompat.
func TestCLIOutboxCompat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script; Windows runs get no outbox")
	}
	c := buildCLI(t)
	old := buildCompatCLI(t, c)
	addr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", addr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	old.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", old.run("--home", "alice", "admin", "invite", "--raw", "bob"))

	stubDir := filepath.Join(c.dir, "stub")
	os.MkdirAll(stubDir, 0o700)
	os.WriteFile(filepath.Join(stubDir, "claude"), []byte(stubOutboxClaude), 0o700)
	work := filepath.Join(c.dir, "bobwork")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "30s")
	c.env = []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("bob.log", "--home", "bob", "daemon")
	c.env = nil
	old.start("alice.log", "--home", "alice", "daemon")

	data := c.writeRandom("notes.bin", 300<<10)
	task := strings.Fields(old.run("--home", "alice", "task", "--file", "notes.bin", "bob/desk", "send the notes back"))[0]
	waitFor(t, "the task waits at bob", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == task && m.State == "awaiting" {
				return true
			}
		}
		return false
	})
	c.run("--home", "bob", "accept", task)
	var res inboxEntry
	waitFor(t, "the result at the older requester", func() bool {
		for _, m := range old.inbox("alice") {
			if m.Kind == "result" {
				res = m
				return true
			}
		}
		return false
	})
	if len(res.Attachments) != 1 || res.Attachments[0].Name != "x.bin" || !strings.HasPrefix(res.Body, "report written") || strings.Contains(res.Body, "outbox") {
		prompt, _ := os.ReadFile(filepath.Join(stubDir, "claude.prompt"))
		t.Fatalf("result at the older requester: %+v\nthe run's prompt:\n%s", res, prompt)
	}
	if got := old.downloadInto("alice", "got", res.ID); !bytes.Equal(got, data) {
		t.Fatalf("the older requester downloaded %d bytes, want the %d sent", len(got), len(data))
	}
	for _, m := range c.inbox("bob") {
		if m.ID == task && m.State != "answered" {
			t.Fatalf("bob's task: %+v", m)
		}
	}
	waitFor(t, "bob's run folder removed", func() bool {
		entries, err := os.ReadDir(filepath.Join(c.dir, "bob", "runs"))
		return err == nil && len(entries) == 0
	})
}
