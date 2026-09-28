package itest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestCLIHookPi: the real binary's Pi hook protocol against an enrolled
// agent. A notice comes with its acknowledgement data and is offered again
// until Pi acknowledges it; Ack records it; background runs stay silent.
func TestCLIHookPi(t *testing.T) {
	c := buildCLI(t)
	_, _ = c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	hook := func(env []string, input string) (text string, ack map[string]any) {
		t.Helper()
		cmd := exec.Command(c.bin, "--home", "bob", "hook", "pi")
		cmd.Dir = c.dir
		cmd.Env = append(cmd.Environ(), env...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("hook pi: %v", err)
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return "", nil
		}
		var r struct {
			Text string         `json:"text"`
			Ack  map[string]any `json:"ack"`
		}
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("hook pi output %q: %v", out, err)
		}
		return r.Text, r.Ack
	}
	ev := func(name string) string { return `{"session_id":"PI1","hook_event_name":"` + name + `"}` }
	ackInput := func(a map[string]any) string {
		a["session_id"], a["hook_event_name"] = "PI1", "Ack"
		data, _ := json.Marshal(a)
		return string(data)
	}

	if _, a := hook(nil, ev("SessionStart")); a != nil {
		hook(nil, ackInput(a))
	}
	id := strings.Fields(c.run("--home", "alice", "send", "bob/desk", "hello pi"))[0]
	waitFor(t, "bob stored it", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == id {
				return true
			}
		}
		return false
	})
	text, a := hook(nil, ev("Idle"))
	if !strings.Contains(text, id) || strings.Contains(text, "hello pi") || a == nil || a["has_pos"] != true {
		t.Fatalf("idle notice %q ack %v", text, a)
	}
	if again, _ := hook(nil, ev("UserPromptSubmit")); !strings.Contains(again, id) {
		t.Fatalf("unacknowledged notice not offered again: %q", again)
	}
	hook(nil, ackInput(a))
	if after, _ := hook(nil, ev("Idle")); after != "" {
		t.Fatalf("acknowledged notice shown again: %q", after)
	}
	if bg, _ := hook([]string{"AGENTNET_BACKGROUND=1"}, ev("SessionStart")); bg != "" {
		t.Fatal("background session got a notice")
	}
}
