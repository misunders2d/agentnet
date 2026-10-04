package itest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dmShow returns `agentnet dm show` for a conversation.
func dmShow(c *cli, home, conv string) string { return c.run("--home", home, "dm", "show", conv) }

// dmShowArriving is dmShow for a conversation that may still be on its way
// to home: "" while dm show says it has no such conversation, and any other
// failure is fatal.
func dmShowArriving(c *cli, home, conv string) string {
	c.t.Helper()
	out, err := c.try("--home", home, "dm", "show", conv)
	if err != nil {
		if strings.Contains(out, "no such conversation here") {
			return ""
		}
		c.t.Fatalf("agentnet --home %s dm show %s: %v\n%s", home, conv, err, out)
	}
	return out
}

// TestCLIDM: persons are created only explicitly; two DMs with the same
// person stay separate, both ways and across a daemon restart; a question in
// a DM is held for the person, never run, and legacy accept/reply refuse it.
func TestCLIDM(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", addr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.run("--home", "bob", "approve", "admin/laptop") // legacy automatic answers for alice: must not apply to DMs
	c.start("alice.log", "--home", "alice", "daemon")
	stopBob := c.start("bob.log", "--home", "bob", "daemon")

	if out, err := c.try("--home", "alice", "person"); err == nil {
		t.Fatalf("a person exists that nobody created: %s", out)
	}
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	if out, err := c.try("--home", "bob", "person", "create", "Robert"); err == nil {
		t.Fatalf("a second person on one installation: %s", out)
	}

	newDM := func() string {
		var conv string
		waitFor(t, "a DM with bob", func() bool {
			out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
			conv = out
			return err == nil
		})
		return conv
	}
	deploy, budget := newDM(), newDM()
	if deploy == budget || len(deploy) != 64 {
		t.Fatalf("conversations %q and %q", deploy, budget)
	}
	for conv, body := range map[string]string{deploy: "deploy on friday?", budget: "budget for Q4"} {
		if out := c.run("--home", "alice", "dm", "send", conv, body); strings.Contains(out, "waiting") {
			t.Fatalf("send: %s", out)
		}
	}
	waitFor(t, "bob to hold both DMs", func() bool {
		return len(strings.Split(c.run("--home", "bob", "dm", "list"), "\n")) == 2 &&
			strings.Contains(dmShow(c, "bob", deploy), "deploy on friday?") && strings.Contains(dmShow(c, "bob", budget), "budget for Q4")
	})
	if strings.Contains(dmShow(c, "bob", deploy), "budget") {
		t.Fatal("the topics mixed")
	}
	c.run("--home", "bob", "dm", "send", deploy, "friday works")
	waitFor(t, "alice to get the reply", func() bool { return strings.Contains(dmShow(c, "alice", deploy), "friday works") })
	if strings.Contains(dmShow(c, "alice", budget), "friday works") {
		t.Fatal("the reply landed in the other DM")
	}

	c.run("--home", "alice", "dm", "send", "--question", deploy, "can you check the release notes?")
	var qid string
	waitFor(t, "the question held for bob", func() bool {
		m := regexp.MustCompile(`in admin/laptop \[ui\] question \(conv_held\)  ([0-9a-f]{32})`).FindStringSubmatch(dmShow(c, "bob", deploy))
		if m != nil {
			qid = m[1]
		}
		return m != nil
	})
	for _, args := range [][]string{{"accept", qid}, {"reply", qid, "done"}, {"decline", qid, "no"}} {
		if out, err := c.try(append([]string{"--home", "bob"}, args...)...); err == nil {
			t.Fatalf("%s on a DM question: %s", args[0], out)
		}
	}
	if strings.Contains(dmShow(c, "bob", deploy), "answered") || strings.Contains(dmShow(c, "alice", deploy), "answer") {
		t.Fatal("something answered the DM question")
	}

	stopBob()
	c.start("bob2.log", "--home", "bob", "daemon")
	if n := len(strings.Split(c.run("--home", "bob", "dm", "list"), "\n")); n != 2 {
		t.Fatalf("after a restart bob lists %d DMs", n)
	}
	if !strings.Contains(dmShow(c, "bob", deploy), "friday works") {
		t.Fatal("history lost across a restart")
	}
}
