package itest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
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

// Delivery waits must not consume the effective unread state under test.
func (c *cli) peekInbox(home string) []inboxEntry {
	c.t.Helper()
	var out []inboxEntry
	if err := json.Unmarshal([]byte(c.run("--home", home, "inbox", "--peek", "--json")), &out); err != nil {
		c.t.Fatal(err)
	}
	return out
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
	// The question comes as an older device sends it (MEL-713 step 1);
	// replies in its thread are still version 1.
	q := c.oldSend("alice", client.Outgoing{To: "bob/desk", Kind: "question", Body: "SECRET-Q which port?", Files: []string{filepath.Join(c.dir, "spec.txt")}}).ID
	ids := []string{q}
	waitFor(t, "question at bob", func() bool { return len(c.peekInbox("bob")) == 1 })
	for i, turn := range []struct{ home, peer string }{{"bob", "alice"}, {"alice", "bob"}, {"bob", "alice"}, {"alice", "bob"}, {"bob", "alice"}} {
		id := strings.Fields(c.run("--home", turn.home, "reply", ids[len(ids)-1], "SECRET-R"+string(rune('1'+i))))[0]
		ids = append(ids, id)
		waitFor(t, "reply at "+turn.peer, func() bool {
			for _, m := range c.peekInbox(turn.peer) {
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

	// Hub stopped: hooks read only the local database. Stop stays silent
	// without consuming arrivals that the next ordinary boundary must show.
	for _, s := range []struct{ harness, id string }{{"claude", "S1"}, {"codex", "S2"}} {
		if raw := c.hook("alice", s.harness, session(s.id, "Stop")); raw != "" {
			t.Fatalf("%s Stop blocked on ordinary replies: %s", s.harness, raw)
		}
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

	// Displaying the inbox marks these replies read for the person.
	// A new native session must not treat that history as another unread inbox.
	if rows := c.inbox("alice"); len(rows) != 3 {
		t.Fatalf("displaying all three received replies: %+v", rows)
	}
	for _, harness := range []string{"claude", "codex"} {
		id := "after-read-" + harness
		raw := c.hook("alice", harness, session(id, "SessionStart"))
		for _, received := range []string{ids[1], ids[3], ids[5]} {
			if strings.Contains(raw, received) {
				t.Fatalf("%s replayed displayed reply %s: %s", harness, received, raw)
			}
		}
		if again := c.hook("alice", harness, session(id, "UserPromptSubmit")); again != "" {
			t.Fatalf("%s replayed read arrivals after SessionStart: %s", harness, again)
		}
		if stopped := c.hook("alice", harness, session(id, "Stop")); stopped != "" {
			t.Fatalf("%s read history blocked Stop: %s", harness, stopped)
		}
	}
	if out := c.hook("alice", "claude", session("S1", "Stop")); out != "" {
		t.Fatalf("stop with nothing new: %s", out)
	}
	// A session the worker started is not told anything and not recorded.
	c.env = []string{"AGENTNET_BACKGROUND=1"}
	if out := c.hook("alice", "claude", session("S3", "SessionStart")); out != "" {
		t.Fatalf("background session told: %s", out)
	}
	c.env = nil
	if out := c.hook("alice", "claude", session("S3", "SessionStart")); !strings.Contains(out, "received message(s)") {
		t.Fatalf("S3 was recorded by the background call: %s", out)
	}
}
