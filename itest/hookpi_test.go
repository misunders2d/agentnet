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
		if strings.Contains(input, `"hook_event_name":"Stop"`) {
			t.Fatalf("ordinary Pi Stop must be silent, including continuation/ack: %s", out)
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
	ackInput := func(a map[string]any, session string) string {
		a["session_id"], a["hook_event_name"] = session, "Ack"
		data, _ := json.Marshal(a)
		return string(data)
	}

	if _, a := hook(nil, ev("SessionStart")); a != nil {
		hook(nil, ackInput(a, "PI1"))
	}
	c.dmWithBob()
	id := strings.Fields(c.run("--home", "alice", "send", "bob/desk", "hello pi"))[0]
	waitFor(t, "bob stored it", func() bool {
		for _, m := range c.peekInbox("bob") {
			if m.ID == id {
				return true
			}
		}
		return false
	})
	if stopped, a := hook(nil, ev("Stop")); stopped != "" || a != nil {
		t.Fatalf("ordinary unread arrival blocked Stop: %q %v", stopped, a)
	}
	text, a := hook(nil, ev("Idle"))
	if !strings.Contains(text, id) || strings.Contains(text, "hello pi") || a == nil || a["has_pos"] != true {
		t.Fatalf("idle notice %q ack %v", text, a)
	}
	if again, _ := hook(nil, ev("UserPromptSubmit")); !strings.Contains(again, id) {
		t.Fatalf("unacknowledged notice not offered again: %q", again)
	}
	hook(nil, ackInput(a, "PI1"))
	if after, _ := hook(nil, ev("Idle")); after != "" {
		t.Fatalf("acknowledged notice shown again: %q", after)
	}
	if rows := c.inbox("bob"); len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("displaying exact received message: %+v", rows)
	}
	newSession := func(name string) string { return strings.Replace(ev(name), "PI1", "PI2", 1) }
	read, overviewAck := hook(nil, newSession("SessionStart"))
	if strings.Contains(read, id) || strings.Contains(read, "hello pi") {
		t.Fatalf("new Pi session replayed a displayed message: %q", read)
	}
	if overviewAck != nil {
		hook(nil, ackInput(overviewAck, "PI2"))
	}
	if read, _ := hook(nil, newSession("Idle")); read != "" {
		t.Fatalf("read arrival reappeared after overview ack: %q", read)
	}
	if stopped, a := hook(nil, newSession("Stop")); stopped != "" || a != nil {
		t.Fatalf("read history blocked Stop: %q %v", stopped, a)
	}
	if bg, _ := hook([]string{"AGENTNET_BACKGROUND=1"}, ev("SessionStart")); bg != "" {
		t.Fatal("background session got a notice")
	}
}
