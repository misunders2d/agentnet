package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// tagScript stands in for both agents of one person. A run whose roster
// says it is $TAG_SELF answers ("TAGGED_DONE"); a continuation with the
// asked agents' results reports them ("LEAD_REPORT"); any other run tags
// $TAG_PEER with work. TAG_LOOP=1 makes every run tag $TAG_PEER again.
const tagScript = `#!/bin/sh
in=$(cat)
{ echo "=== run $*"; printf '%s\n' "$in"; } >> "$AGENT_LOG"
tag="[@peer](agentnet:agent/$TAG_PEER)"
me=no
printf '%s' "$in" | grep -q "you (this agent), .*(PID $TAG_SELF," && me=yes
if [ "$TAG_LOOP" = 1 ] && [ $me = yes ]; then tag="[@lead](agentnet:agent/$TAG_LEAD)"; fi
case "$in" in
*"## Results of the agents you asked"*)
  if [ "$TAG_LOOP" = 1 ]; then printf 'LEAD_REPORT again %s\nemotion: calm\n' "$tag"; else printf 'LEAD_REPORT: the other agent finished\nemotion: calm\n'; fi ;;
*)
  if [ $me = yes ]; then
    if [ "$TAG_LOOP" = 1 ]; then printf 'TAGGED_DONE and back to you %s\nemotion: calm\n' "$tag"; else printf 'TAGGED_DONE: disk checked\nemotion: calm\n'; fi
  else printf 'Handing over to %s: TAGGED_WORK check the disk\nemotion: calm\n' "$tag"; fi ;;
esac
`

func installTagScript(t *testing.T, self, peer, lead string, loop bool) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tag.sh")
	if err := os.WriteFile(bin, []byte(tagScript), 0o700); err != nil {
		t.Fatal(err)
	}
	Harnesses["agentstub"] = harness{bin: bin, question: []string{"--question-mode"}, task: []string{"--task-mode"}, stdin: true}
	t.Setenv("TAG_SELF", self)
	t.Setenv("TAG_PEER", peer)
	t.Setenv("TAG_LEAD", lead)
	if loop {
		t.Setenv("TAG_LOOP", "1")
	} else {
		t.Setenv("TAG_LOOP", "")
	}
	return os.Getenv("AGENT_LOG")
}

func tagRuns(t *testing.T, log, mode string) int {
	t.Helper()
	data, _ := os.ReadFile(log)
	return strings.Count(string(data), "=== run "+mode)
}

// Owner, 2026-11-09: "make sure agents can tag each other and delegate
// tasks ... without me passing over every message". The owner asks the
// agent on their laptop once; it tags their agent on their server with
// work; that one runs it as a task under the owner's authority and
// replies; the laptop agent then runs again with that reply and reports.
func TestOwnAgentTagHandsTaskToOwnAgent(t *testing.T) {
	w, server, conv, laptopPID, serverPID, _ := ownAgentsWorld(t)
	log := installTagScript(t, serverPID, serverPID, laptopPID, false)
	ask, err := w.alice.AskAgent(tctx(t), laptopPID, envelope.KindQuestion, "OWNER_ASKS: get the disk checked")
	if err != nil {
		t.Fatal(err)
	}
	report := replyAt(t, w.alice, conv, ask.ID)
	if !strings.Contains(report.Body, "LEAD_REPORT") {
		t.Fatalf("the laptop agent answered the owner without the other agent's result: %q", report.Body)
	}
	handed, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return strings.Contains(m.Body, "TAGGED_WORK") })
	if n != 1 || handed.Kind != envelope.KindTask || handed.PID != serverPID {
		t.Fatalf("tag became %d message(s), last kind %q for %q; want one task to the server agent", n, handed.Kind, handed.PID)
	}
	done, n := convMsg(t, server, conv, func(m ConvMessage) bool { return strings.Contains(m.Body, "TAGGED_DONE") })
	if n != 1 || done.Kind != envelope.KindResult || done.PID != serverPID || done.ReplyTo != handed.LID {
		t.Fatalf("server agent replies: %d, last kind %q pid %q reply_to %q (task %q)", n, done.Kind, done.PID, done.ReplyTo, handed.LID)
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "## Results of the agents you asked") || !strings.Contains(string(data)[strings.Index(string(data), "## Results of the agents you asked"):], "TAGGED_DONE") {
		t.Errorf("the lead's second run did not get the other agent's result")
	}
	if got := tagRuns(t, log, "--task-mode"); got != 1 {
		t.Errorf("task runs = %d, want 1 (the server agent's)", got)
	}
	if got := tagRuns(t, log, ""); got != 3 {
		t.Errorf("runs = %d, want 3 (lead, tagged, lead again)", got)
	}
}

// Loop safety: two agents that always tag each other. The asked agent never
// tags back its asker, and a rerun's repeated tag is the same ask, so the
// exchange ends after one ask with the lead's report.
func TestAgentTagsNeverLoop(t *testing.T) {
	w, _, conv, laptopPID, serverPID, _ := ownAgentsWorld(t)
	log := installTagScript(t, serverPID, serverPID, laptopPID, true)
	ask, err := w.alice.AskAgent(tctx(t), laptopPID, envelope.KindQuestion, "OWNER_ASKS: get the disk checked")
	if err != nil {
		t.Fatal(err)
	}
	report := replyAt(t, w.alice, conv, ask.ID)
	if !strings.Contains(report.Body, "LEAD_REPORT") {
		t.Fatalf("report %q", report.Body)
	}
	time.Sleep(2 * time.Second) // anything a loop would start has time to show
	if got := tagRuns(t, log, ""); got != 3 {
		t.Errorf("runs = %d, want 3", got)
	}
	if _, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool {
		return m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask
	}); n != 2 {
		t.Errorf("requests in the chat = %d, want 2 (the owner's and one tag)", n)
	}
}

// Another person's agent is not run by a tag without its host's grant: the
// tag is a question there, held for that host's person.
func TestForeignAgentTagWaitsForItsHost(t *testing.T) {
	w, _, conv, laptopPID, _, stub := ownAgentsWorld(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	bobPID := participate(t, w, conv, nil, nil)
	log := installTagScript(t, "none", bobPID, laptopPID, false)
	if _, err := w.alice.AskAgent(tctx(t), laptopPID, envelope.KindQuestion, "OWNER_ASKS: ask bob's agent"); err != nil {
		t.Fatal(err)
	}
	var id, state, kind string
	eventually(t, "the tag held at bob's device", func() bool {
		err := w.bob.store.db.QueryRow(`SELECT id, state, kind FROM inbox WHERE conv=? AND pid=? AND body LIKE '%TAGGED_WORK%'`, conv, bobPID).Scan(&id, &state, &kind)
		return err == nil && state == stateAwaiting
	})
	if kind != envelope.KindQuestion {
		t.Errorf("a tag of another person's agent is a %s, want a question", kind)
	}
	time.Sleep(time.Second)
	if got := tagRuns(t, log, ""); got != 1 {
		t.Errorf("runs = %d, want 1 (only the lead; bob's agent waits for bob)", got)
	}
}
