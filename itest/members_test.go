package itest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lastMembersLine is the daemon's latest "hub members: …" log line.
func lastMembersLine(c *cli, log string) string {
	data, _ := os.ReadFile(filepath.Join(c.dir, log))
	last := ""
	for l := range strings.SplitSeq(string(data), "\n") {
		if _, rest, ok := strings.Cut(l, "hub members: "); ok {
			last = rest
		}
	}
	return last
}

func waitMembersLine(t *testing.T, c *cli, log, want string) {
	t.Helper()
	waitFor(t, log+": "+want, func() bool { return lastMembersLine(c, log) == want })
}

// memberLine is address's line in `agentnet members` output.
func memberLine(c *cli, home, address string) string {
	for l := range strings.SplitSeq(c.run("--home", home, "members"), "\n") {
		if strings.HasPrefix(l, address+"  ") {
			return l
		}
	}
	return ""
}

// TestCLIMembers: someone joins the Hub and the others' running daemons are
// told, without knowing the new address; presence follows the newcomer's
// daemon; a daemon that was offline gets the current list when it
// reconnects; a revoked member disappears and can no longer list; and being
// listed records no key, approval or grant.
func TestCLIMembers(t *testing.T) {
	c := buildCLI(t)
	addr := freeAddr(t)
	c.start("hub.log", "hub", "serve", "--data", "hub", "--listen", addr)
	waitFile(t, filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	code, _ := os.ReadFile(filepath.Join(c.dir, "hub", "bootstrap-invite.txt"))
	c.run("--home", "alice", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
	c.run("--home", "bob", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "bob"))
	c.start("alice.log", "--home", "alice", "daemon")
	stopBob := c.start("bob.log", "--home", "bob", "daemon")
	waitMembersLine(t, c, "bob.log", "2 listed, 2 connected")

	c.run("--home", "vit", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "vitalii"))
	waitMembersLine(t, c, "bob.log", "3 listed, 2 connected")
	out := c.run("--home", "bob", "members")
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "vitalii/desk  offline  joined ") || !strings.Contains(out, "bob/desk  connected  joined ") || !strings.HasSuffix(memberLine(c, "bob", "bob/desk"), "(this agent)") {
		t.Fatalf("members:\n%s", out)
	}

	stopVit := c.start("vit.log", "--home", "vit", "daemon")
	waitMembersLine(t, c, "bob.log", "3 listed, 3 connected")
	stopVit()
	waitMembersLine(t, c, "bob.log", "3 listed, 2 connected")
	if l := memberLine(c, "bob", "vitalii/desk"); !strings.Contains(l, "  reconnecting  ") {
		t.Fatalf("after the newcomer's daemon stopped: %q", l)
	}

	// Bob is offline while carol joins; his daemon learns of her on reconnect.
	stopBob()
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	c.start("bob2.log", "--home", "bob", "daemon")
	waitMembersLine(t, c, "bob2.log", "4 listed, 2 connected")

	c.run("--home", "alice", "admin", "revoke", "vitalii/desk")
	waitMembersLine(t, c, "bob2.log", "3 listed, 2 connected")
	if memberLine(c, "bob", "vitalii/desk") != "" {
		t.Fatal("revoked member still listed")
	}
	if out, err := c.try("--home", "vit", "members"); err == nil {
		t.Fatalf("a revoked member listed the members: %s", out)
	}

	db := openDB(t, filepath.Join(c.dir, "bob", "agent.db"))
	for _, table := range []string{"peers", "approvals", "task_grants"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE address IN ('vitalii/desk', 'carol/desk')`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d rows for listed members (%v)", table, n, err)
		}
	}
}

// TestCLIMembersCompat is a bounded compatibility check against a released
// version built from its local git tag (AGENTNET_COMPAT_TAG, e.g. v0.2.1):
// this build's daemon and members command with that Hub, and that version's
// daemon with this build's Hub (which pushes member lists it does not know).
// Opt-in: it needs the tag in a local repository (AGENTNET_COMPAT_REPO,
// default the one this test is in) and a Go build of it.
func TestCLIMembersCompat(t *testing.T) {
	tag := os.Getenv("AGENTNET_COMPAT_TAG")
	if tag == "" {
		t.Skip("set AGENTNET_COMPAT_TAG to a released tag, e.g. v0.2.1")
	}
	repo := os.Getenv("AGENTNET_COMPAT_REPO")
	if repo == "" {
		repo = ".."
	}
	c := buildCLI(t)
	src := t.TempDir()
	tarball := filepath.Join(t.TempDir(), "src.tar")
	for _, cmd := range []*exec.Cmd{
		exec.Command("git", "-C", repo, "archive", "-o", tarball, tag),
		exec.Command("tar", "-x", "-f", tarball, "-C", src),
	} {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("extract %s: %v\n%s", tag, err, out)
		}
	}
	oldBin := filepath.Join(c.dir, "agentnet-"+tag)
	build := exec.Command("go", "build", "-o", oldBin, "./cmd/agentnet")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", tag, err, out)
	}
	old := &cli{t: t, bin: oldBin, dir: c.dir}
	if v := old.run("version"); !strings.Contains(v, tag) {
		t.Logf("%s reports %q", tag, v)
	}
	// received: the inbox (read with that home's own program) holds id.
	received := func(p *cli, home, id string) func() bool {
		return func() bool {
			var msgs []struct{ ID string }
			json.Unmarshal([]byte(p.run("--home", home, "inbox", "--json")), &msgs)
			for _, m := range msgs {
				if m.ID == id {
					return true
				}
			}
			return false
		}
	}

	t.Run("older Hub", func(t *testing.T) {
		c, old := &cli{t: t, bin: c.bin, dir: c.dir}, &cli{t: t, bin: old.bin, dir: old.dir}
		addr := freeAddr(t)
		old.start("oldhub.log", "hub", "serve", "--data", "oldhub", "--listen", addr)
		waitFile(t, filepath.Join(c.dir, "oldhub", "bootstrap-invite.txt"))
		code, _ := os.ReadFile(filepath.Join(c.dir, "oldhub", "bootstrap-invite.txt"))
		c.run("--home", "a1", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
		c.run("--home", "b1", "join", "--agent", "desk", c.run("--home", "a1", "admin", "invite", "--raw", "bob"))
		c.start("b1.log", "--home", "b1", "daemon")
		waitFor(t, "b1 connected", func() bool {
			data, _ := os.ReadFile(filepath.Join(c.dir, "b1.log"))
			return strings.Contains(string(data), "connected to hub")
		})
		if out, err := c.try("--home", "b1", "members"); err == nil || !strings.Contains(out, "does not list its members") {
			t.Fatalf("members on an older Hub: %v %s", err, out)
		}
		id := strings.Fields(c.run("--home", "a1", "send", "bob/desk", "still works"))[0]
		waitFor(t, "delivery through the older Hub", received(c, "b1", id))
		if l := lastMembersLine(c, "b1.log"); l != "" {
			t.Fatalf("a member list from an older Hub: %s", l)
		}
	})

	t.Run("older client", func(t *testing.T) {
		c, old := &cli{t: t, bin: c.bin, dir: c.dir}, &cli{t: t, bin: old.bin, dir: old.dir}
		addr := freeAddr(t)
		c.start("newhub.log", "hub", "serve", "--data", "newhub", "--listen", addr)
		waitFile(t, filepath.Join(c.dir, "newhub", "bootstrap-invite.txt"))
		code, _ := os.ReadFile(filepath.Join(c.dir, "newhub", "bootstrap-invite.txt"))
		c.run("--home", "a2", "join", "--agent", "laptop", strings.TrimSpace(string(code)))
		old.run("--home", "b2", "join", "--agent", "desk", c.run("--home", "a2", "admin", "invite", "--raw", "bob"))
		old.start("b2.log", "--home", "b2", "daemon")
		connects := func() int {
			data, _ := os.ReadFile(filepath.Join(c.dir, "b2.log"))
			return strings.Count(string(data), "connected to hub")
		}
		waitFor(t, "older daemon connected", func() bool { return connects() == 1 })
		// A join pushes a new member list to the older daemon too.
		c.run("--home", "c2", "join", "--agent", "desk", c.run("--home", "a2", "admin", "invite", "--raw", "carol"))
		id := strings.Fields(c.run("--home", "a2", "send", "bob/desk", "after a member list"))[0]
		waitFor(t, "delivery to the older daemon", received(old, "b2", id))
		if n := connects(); n != 1 {
			t.Fatalf("the older daemon reconnected %d times", n)
		}
	})
}
