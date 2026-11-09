package itest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Owner, 2026-11-09: "make sure agents can tag each other and delegate
// tasks, cooperate and communicate without me passing over every message."
// Real binaries, stand-in agents: in a DM with bob, alice asks the agent on
// her laptop once; it tags her agent on her linked phone with work; that
// agent runs it as a task and replies (tagging its asker back, which must
// not loop); the laptop agent runs again with that reply and reports to
// alice. Alice writes nothing after her one request.
func TestCLIAgentsTagEachOther(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responders are shell scripts")
	}
	c := buildCLI(t)
	c.setup(t, "hub")
	stubs := map[string]string{}
	for _, home := range []string{"alice", "phone"} {
		stubs[home] = filepath.Join(c.dir, home+"-stub")
		os.MkdirAll(stubs[home], 0o700)
		writeClaudeFixture(t, filepath.Join(stubs[home], "claude"), "#!/bin/sh\ncat >/dev/null\nprintf 'not ready\\nemotion: calm\\n'\n")
	}
	c.start("bob.log", "--home", "bob", "daemon")
	c.env = []string{"PATH=" + stubs["alice"] + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("alice.log", "--home", "alice", "daemon")
	c.env = nil
	c.run("--home", "alice", "person", "create", "Alice")
	c.run("--home", "bob", "person", "create", "Bob")
	var link string
	for _, line := range strings.Split(c.run("--home", "alice", "person", "link"), "\n") {
		if strings.HasPrefix(line, "agentnet-link-v2:") {
			link = line
		}
	}
	if link == "" {
		t.Fatal("no device link offer")
	}
	c.run("--home", "phone", "join", "--agent", "phone", link)
	c.env = []string{"PATH=" + stubs["phone"] + string(os.PathListSeparator) + os.Getenv("PATH")}
	c.start("phone.log", "--home", "phone", "daemon")
	c.env = nil
	var request string
	waitFor(t, "the phone's link request", func() bool {
		for _, line := range strings.Split(c.run("--home", "alice", "person", "links"), "\n") {
			if f := strings.Fields(line); len(f) > 2 && f[1] == "pending" && f[2] == "admin/phone" {
				request = f[0]
			}
		}
		return request != ""
	})
	c.run("--home", "alice", "person", "approve", request)
	for _, home := range []string{"alice", "phone"} {
		work := filepath.Join(c.dir, home+"-work")
		os.MkdirAll(work, 0o700)
		c.run("--home", home, "responder", "set", "--harness", "claude", "--dir", work, "--timeout", "60s")
	}
	var conv string
	waitFor(t, "a DM with bob", func() bool {
		out, err := c.try("--home", "alice", "dm", "new", "bob/desk")
		conv = out
		return err == nil
	})
	c.run("--home", "alice", "dm", "send", conv, "the disk on the build box is nearly full")
	waitFor(t, "the DM on the phone", func() bool { return dmShowArriving(c, "phone", conv) != "" })

	invite := func(host, accepter string) string {
		pid := strings.Fields(c.run("--home", "alice", "dm", "invite", "--note", "help here", conv, host))[0]
		waitFor(t, accepter+" to see the agent", func() bool {
			out := c.run("--home", accepter, "dm", "agents", conv)
			return strings.Contains(out, pid+"  invited") || strings.Contains(out, pid+"  active")
		})
		if strings.Contains(c.run("--home", accepter, "dm", "agents", conv), pid+"  invited") {
			c.run("--home", accepter, "dm", "accept-agent", pid)
		}
		for _, home := range []string{"alice", "phone", "bob"} {
			waitFor(t, home+" to see "+pid+" active", func() bool { return strings.Contains(c.run("--home", home, "dm", "agents", conv), pid+"  active") })
		}
		return pid
	}
	lead := invite("admin/laptop", "alice")
	helper := invite("admin/phone", "phone")

	// The lead hands work to the helper by its exact tag, then reports what
	// the helper said; the helper does the work and tags its asker back.
	writeClaudeFixture(t, filepath.Join(stubs["alice"], "claude"), `#!/bin/sh
cat > "$0.prompt.$$"
echo run >> "$0.runs"
case "$(cat "$0.prompt.$$")" in
*"## Results of the agents you asked"*) printf 'LEAD_REPORT: the phone agent finished the cleanup\nemotion: calm\n' ;;
*) printf 'Handing this over to [@phone](agentnet:agent/`+helper+`): TAGGED_WORK clean the build box disk\nemotion: calm\n' ;;
esac
`)
	writeClaudeFixture(t, filepath.Join(stubs["phone"], "claude"), `#!/bin/sh
cat > "$0.prompt.$$"
echo "run $*" >> "$0.runs"
printf 'TAGGED_DONE: freed 40 GB, over to you [@lead](agentnet:agent/`+lead+`)\nemotion: happy\n'
`)
	c.run("--home", "alice", "dm", "ask-agent", "--answer-wait", "0", lead, "OWNER_ASKS: get the build box disk cleaned")
	waitFor(t, "the lead's report at alice", func() bool {
		return regexp.MustCompile(`answer pid ` + lead + ` .*\n  LEAD_REPORT`).MatchString(dmShow(c, "alice", conv))
	})
	show := dmShow(c, "alice", conv)
	if !regexp.MustCompile(`task pid ` + helper + ` .*\n  Handing this over to \[@phone\]\(agentnet:agent/` + helper + `\): TAGGED_WORK`).MatchString(show) {
		t.Fatalf("the lead's tag is not a task to the helper:\n%s", show)
	}
	if !regexp.MustCompile(`result pid ` + helper + ` .*\n  TAGGED_DONE`).MatchString(show) {
		t.Fatalf("no result from the helper:\n%s", show)
	}
	if !strings.Contains(dmShow(c, "bob", conv), "TAGGED_DONE") {
		t.Fatalf("bob does not see the agents' exchange:\n%s", dmShow(c, "bob", conv))
	}
	// One request from alice and one tag, no more: the helper's tag of its
	// asker and the lead's rerun start nothing.
	if n := len(regexp.MustCompile(`(?m) (question|task) pid `).FindAllString(show, -1)); n != 2 {
		t.Fatalf("%d requests in the chat, want 2 (alice's and one tag):\n%s", n, show)
	}
	runs := func(home string) string {
		data, _ := os.ReadFile(filepath.Join(stubs[home], "claude.runs"))
		return string(data)
	}
	if strings.Count(runs("alice"), "run") != 2 || strings.Count(runs("phone"), "run") != 1 {
		t.Fatalf("runs: lead %q, helper %q; want 2 and 1", runs("alice"), runs("phone"))
	}
}
