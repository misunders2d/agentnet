package itest

import (
	"regexp"
	"strings"
	"testing"
)

// TestCLIRemind: through the real binary, a reminder is set on a received
// message, listed, moved, cancelled; a time in the past and a message that
// was not received are refused; the message itself is untouched.
func TestCLIRemind(t *testing.T) {
	c := buildCLI(t)
	c.setup(t, "hub")
	c.start("bob.log", "--home", "bob", "daemon")
	c.start("alice.log", "--home", "alice", "daemon")
	sent := strings.Fields(c.run("--home", "alice", "send", "bob/desk", "the budget, when you can"))[0]
	waitFor(t, "bob to hold it", func() bool { return len(c.inbox("bob")) == 1 })
	if out := c.run("--home", "bob", "remind", sent, "2h"); !regexp.MustCompile(`^` + sent + `  due \S+ \S+ \(in 2h0m0s\)  from admin/laptop$`).MatchString(out) {
		t.Fatalf("remind: %q", out)
	}
	if out := c.run("--home", "bob", "remind", "list"); !strings.Contains(out, sent+"  due ") {
		t.Fatalf("list: %q", out)
	}
	if out := c.run("--home", "bob", "remind", sent, "30m"); !strings.Contains(out, "(in 30m0s)") {
		t.Fatalf("moved: %q", out)
	}
	for _, args := range [][]string{{"remind", sent, "-5m"}, {"remind", strings.Repeat("0", 32), "1h"}, {"remind", sent, "someday"}, {"remind", "done", strings.Repeat("0", 32)}} {
		if out, err := c.try(append([]string{"--home", "bob"}, args...)...); err == nil {
			t.Fatalf("%v accepted: %s", args, out)
		}
	}
	if out := c.run("--home", "bob", "remind", "cancel", sent); out != sent+" cancelled" {
		t.Fatalf("cancel: %q", out)
	}
	if out := c.run("--home", "bob", "remind", "list"); out != "no reminders" {
		t.Fatalf("list after cancel: %q", out)
	}
	if out := c.run("--home", "bob", "remind", "list", "--all"); !strings.Contains(out, sent+"  cancelled") {
		t.Fatalf("list --all: %q", out)
	}
	if m := c.inbox("bob"); len(m) != 1 || m[0].State != "" {
		t.Fatalf("the message changed: %+v", m)
	}
}
