package itest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hook runs `agentnet hook HARNESS` for home with event on stdin.
func (c *cli) hook(home, harness, event string) string {
	c.t.Helper()
	cmd := exec.Command(c.bin, "--home", home, "hook", harness)
	cmd.Dir, cmd.Env, cmd.Stdin = c.dir, append(os.Environ(), c.env...), strings.NewReader(event)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() > 0 {
		c.t.Fatalf("hook: %v %s", err, stderr.Bytes())
	}
	return string(out)
}

// TestCLIConversationAndHooks: a two-way conversation with files survives
// every process exiting; hook calls from two sessions each learn of the
// replies once, from the local database alone (Hub stopped), without the
// message text.
func TestCLIConversationAndHooks(t *testing.T) {
	c := buildCLI(t)
	_, stopHub := c.setup(t, "hub")
	stopAlice := c.start("alice.log", "--home", "alice", "daemon")
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	session := func(id, event string) string {
		return `{"session_id":"` + id + `","hook_event_name":"` + event + `","cwd":"/tmp","stop_hook_active":false}`
	}
	if out := c.hook("alice", "claude", session("S1", "SessionStart")); !strings.Contains(out, `"hookEventName":"SessionStart"`) {
		t.Fatalf("session start: %s", out)
	}
	c.hook("alice", "codex", session("S2", "SessionStart"))

	os.WriteFile(filepath.Join(c.dir, "spec.txt"), []byte("spec v1"), 0o600)
	q := strings.Fields(c.run("--home", "alice", "ask", "--file", "spec.txt", "bob/desk", "SECRET-Q which port?"))[0]
	ids := []string{q}
	waitFor(t, "question at bob", func() bool { return len(c.inbox("bob")) == 1 })
	for i, turn := range []struct{ home, peer string }{{"bob", "alice"}, {"alice", "bob"}, {"bob", "alice"}, {"alice", "bob"}, {"bob", "alice"}} {
		id := strings.Fields(c.run("--home", turn.home, "reply", ids[len(ids)-1], "SECRET-R"+string(rune('1'+i))))[0]
		ids = append(ids, id)
		waitFor(t, "reply at "+turn.peer, func() bool {
			for _, m := range c.inbox(turn.peer) {
				if m.ID == id {
					return true
				}
			}
			return false
		})
	}
	stopAlice()
	stopBob()
	stopHub()

	var conv struct {
		Peer     string
		Total    int
		Messages []struct {
			ID, Dir, Body string
			Attachments   []struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(c.run("--home", "alice", "conversation", "--json", ids[3])), &conv); err != nil {
		t.Fatal(err)
	}
	if conv.Total != 6 || len(conv.Messages) != 6 || conv.Peer != "bob/desk" {
		t.Fatalf("conversation: %+v", conv)
	}
	for i, m := range conv.Messages {
		if m.ID != ids[i] || (i%2 == 0) != (m.Dir == "out") {
			t.Fatalf("message %d: %+v, want %s", i, m, ids[i])
		}
	}
	if a := conv.Messages[0].Attachments; len(a) != 1 || a[0].Name != "spec.txt" {
		t.Fatalf("sent file metadata: %+v", a)
	}
	text := c.run("--home", "bob", "conversation", "--limit", "2", ids[0])
	if !strings.Contains(text, "messages 1-2 of 6") || !strings.Contains(text, "(4 more:") {
		t.Fatalf("bob's page: %s", text)
	}

	// Hub stopped: hooks read only the local database.
	for _, s := range []struct{ harness, id string }{{"claude", "S1"}, {"codex", "S2"}} {
		var out struct {
			HookSpecificOutput struct{ HookEventName, AdditionalContext string }
		}
		raw := c.hook("alice", s.harness, session(s.id, "UserPromptSubmit"))
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("%s: %v %q", s.harness, err, raw)
		}
		ctx := out.HookSpecificOutput.AdditionalContext
		if out.HookSpecificOutput.HookEventName != "UserPromptSubmit" || !strings.Contains(ctx, ids[1]) || !strings.Contains(ctx, ids[3]) ||
			!strings.Contains(ctx, ids[5]) || strings.Contains(ctx, "SECRET") {
			t.Fatalf("%s context: %s", s.harness, ctx)
		}
		if again := c.hook("alice", s.harness, session(s.id, "UserPromptSubmit")); again != "" {
			t.Fatalf("%s told twice: %s", s.harness, again)
		}
	}
	if out := c.hook("alice", "claude", session("S1", "Stop")); out != "" {
		t.Fatalf("stop with nothing new: %s", out)
	}
}
