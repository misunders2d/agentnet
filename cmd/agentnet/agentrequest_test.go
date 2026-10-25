package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Exercise the public command handlers against real signed/encrypted traffic.
// Only the two native executables are synthetic; no model or live home is used.
func TestCLIQueuedNamedAgentJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell native stand-ins")
	}
	for _, key := range []string{client.BackgroundEnv, client.ClaudeSessionEnv, client.CodexThreadEnv, "AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_BINDING"} {
		t.Setenv(key, "")
	}
	bin, work := t.TempDir(), t.TempDir()
	log, release := filepath.Join(work, "runs"), filepath.Join(work, "release")
	t.Setenv("CLI_TEST_RUNS", log)
	t.Setenv("CLI_TEST_RELEASE", release)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, result := range map[string]string{
		"pi":     "printf '%s\\n' '{\"type\":\"agent_start\"}' '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"exact CLI answer\\nemotion: calm\"}],\"stopReason\":\"stop\"}}' '{\"type\":\"agent_end\"}' '{\"type\":\"agent_settled\",\"aborted\":false}'\n",
		"claude": "printf 'synthetic native execution failed\\n' >&2\nexit 7\n",
	} {
		// Both native adapters send one JSON input record and keep stdin open
		// for steering, so the stand-ins must not wait for EOF.
		script := "#!/bin/sh\nif test \"$1\" = --help; then exit 0; fi\nIFS= read -r prompt\nprintf '%s\\n' \"$AGENTNET_REQUEST_ID\" >> \"$CLI_TEST_RUNS\"\ncase \"$prompt\" in *'delayed request'*) while ! test -f \"$CLI_TEST_RELEASE\"; do sleep 0.05; done ;; esac\n" + result
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	stops := map[*client.Agent]func(){}
	a, b, c, dm, eventually := dmWorld(t, stops)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	named := func(host *client.Agent, harness string) protocol.AgentRecord {
		t.Helper()
		r, err := host.CreateLocalAgent("Same label", client.Responder{Harness: harness, Dir: work})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, second, offline := named(b, "pi"), named(b, "claude"), named(c, "pi")
	for _, host := range []*client.Agent{b, c} {
		if err := host.PublishAgentCatalog(ctx); err != nil {
			t.Fatal(err)
		}
		if err := host.Approve(a.Address); err != nil {
			t.Fatal(err)
		}
	}
	dmCommand := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runDM(ctx, a, args, &out)
		return out.String(), err
	}
	groupCommand := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runGroup(ctx, a, args, &out, io.Discard)
		return out.String(), err
	}
	var catalog []protocol.AgentRecord
	text, err := dmCommand("catalog", "--json", b.Address)
	if err != nil || json.Unmarshal([]byte(text), &catalog) != nil || len(catalog) != 2 {
		t.Fatalf("catalog=%s error=%v", text, err)
	}
	for _, r := range catalog {
		if err := r.Verify(b.Self()); err != nil || r.Host != b.Address || r.HostKey != b.Self().Fingerprint() || r.ID != first.ID && r.ID != second.ID {
			t.Fatalf("wrong catalog identity: %+v %v", r, err)
		}
	}
	if strings.Contains(text, work) || strings.Contains(text, "responder") {
		t.Fatalf("private setup in public catalog: %s", text)
	}
	text, err = groupCommand("create", "CLI exact agents")
	var group client.GroupContext
	if err != nil || json.Unmarshal([]byte(text), &group) != nil {
		t.Fatalf("create=%s error=%v", text, err)
	}
	invite := func(conv string, host *client.Agent, record protocol.AgentRecord, inGroup bool) string {
		t.Helper()
		args := []string{"invite", "--agent", record.ID, conv, host.Address}
		command := dmCommand
		if inGroup {
			args[0], command = "invite-agent", groupCommand
		}
		text, err := command(args...)
		fields := strings.Fields(text)
		if err != nil || len(fields) < 2 || !strings.Contains(text, "agent_id="+record.ID) || !strings.Contains(text, "host_key="+host.Self().Fingerprint()) {
			t.Fatalf("invite=%s error=%v", text, err)
		}
		pid := fields[0]
		eventually("exact CLI invitation", func() bool {
			p, err := host.Participation(pid)
			return err == nil && p.State == client.PartInvited
		})
		var accepted bytes.Buffer
		if err := runDM(ctx, host, []string{"accept-agent", pid}, &accepted); err != nil {
			t.Fatal(err)
		}
		eventually("exact CLI acceptance", func() bool {
			p, err := a.Participation(pid)
			return err == nil && p.Claimable() && p.AgentID == record.ID
		})
		return pid
	}
	firstPID := invite(group.State.Conv, b, first, true)
	secondPID := invite(group.State.Conv, b, second, true)
	offlinePID := invite(group.State.Conv, c, offline, true)
	dmPID := invite(dm, b, first, false)
	text, err = groupCommand("agents", group.State.Conv)
	if err != nil || !strings.Contains(text, firstPID) || !strings.Contains(text, secondPID) || !strings.Contains(text, "agent_id="+first.ID) || !strings.Contains(text, "agent_id="+second.ID) {
		t.Fatalf("agents=%s error=%v", text, err)
	}
	stops[c]()
	firstID, failedID, offlineID, dmID := protocol.NewID(), protocol.NewID(), protocol.NewID(), protocol.NewID()
	ask := func(pid, id, body string) []string {
		return []string{"ask-agent", "--id", id, "--reply-receiver", "human", pid, body}
	}
	started := time.Now()
	for _, target := range []struct{ pid, id, body string }{
		{offlinePID, offlineID, "offline request"}, {firstPID, firstID, "delayed request"}, {secondPID, failedID, "failing request"},
	} {
		text, err := groupCommand(ask(target.pid, target.id, target.body)...)
		if err != nil || !strings.Contains(text, "logical="+target.id) || !strings.Contains(text, "participation="+target.pid) {
			t.Fatalf("ask=%s error=%v", text, err)
		}
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("queued commands waited for an offline or executing target")
	}
	original, err := a.AgentRequest(firstID)
	if err != nil {
		t.Fatal(err)
	}
	text, err = groupCommand(ask(firstPID, firstID, "delayed request")...)
	if err != nil || !strings.HasPrefix(text, original.Sent.ID+" ") || !strings.Contains(text, "logical="+firstID) {
		t.Fatalf("retry changed retained IDs: %s %v", text, err)
	}
	for _, changed := range [][]string{
		ask(secondPID, firstID, "delayed request"),
		ask(firstPID, firstID, "changed request"),
		{"ask-agent", "--id", firstID, "--task", "--reply-receiver", "human", firstPID, "delayed request"},
		{"ask-agent", "--id", firstID, firstPID, "delayed request"},
	} {
		if _, err := groupCommand(changed...); !errors.Is(err, client.ErrAgentRequestConflict) {
			t.Fatalf("changed retry %v: %v", changed, err)
		}
	}
	text, err = groupCommand("wait", "--answer-wait", "1ms", firstID)
	if err != nil || !strings.Contains(text, "pending: no answer yet") {
		t.Fatalf("pending wait=%s error=%v", text, err)
	}
	canceled, stopWait := context.WithCancel(ctx)
	stopWait()
	var interrupted bytes.Buffer
	if err := runGroup(canceled, a, []string{"wait", firstID}, &interrupted, io.Discard); err != nil || !strings.Contains(interrupted.String(), "stopped waiting; request retained") {
		t.Fatalf("interrupted wait=%s error=%v", interrupted.String(), err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	text, err = groupCommand("wait", "--answer-wait", "30s", original.Sent.ID)
	if err != nil || !strings.Contains(text, "exact CLI answer") || !strings.Contains(text, "answer from "+b.Address) {
		t.Fatalf("resumed correlated answer=%s error=%v", text, err)
	}
	text, err = groupCommand("wait", "--answer-wait", "30s", failedID)
	if err != nil || !strings.Contains(text, "failed") || strings.Contains(text, "exact CLI answer") || strings.Contains(text, "pending:") {
		t.Fatalf("native failure=%s error=%v", text, err)
	}
	text, err = groupCommand("wait", "--answer-wait", "1ms", offlineID)
	if err != nil || !strings.Contains(text, "pending: no answer yet") {
		t.Fatalf("offline wait=%s error=%v", text, err)
	}
	text, err = dmCommand(ask(dmPID, dmID, "DM request")...)
	if err != nil || !strings.Contains(text, "logical="+dmID) {
		t.Fatalf("DM ask=%s error=%v", text, err)
	}
	text, err = dmCommand("wait", "--answer-wait", "30s", dmID)
	if err != nil || !strings.Contains(text, "exact CLI answer") {
		t.Fatalf("DM result=%s error=%v", text, err)
	}
	for _, args := range [][]string{
		{"agents", dm}, {"invite-agent", "--agent", first.ID, dm, b.Address}, ask(dmPID, protocol.NewID(), "wrong group"), {"wait", dmID},
	} {
		if _, err := groupCommand(args...); err == nil {
			t.Fatalf("group alias accepted DM: %v", args)
		}
	}
	// Both queued command variants must read completed results with no daemon.
	stops[a]()
	for _, completed := range []struct {
		command       func(...string) (string, error)
		pid, id, body string
	}{{groupCommand, firstPID, firstID, "delayed request"}, {dmCommand, dmPID, dmID, "DM request"}} {
		args := append([]string{"ask-agent", "--answer-wait", "1s"}, ask(completed.pid, completed.id, completed.body)[1:]...)
		text, err := completed.command(args...)
		if err != nil || !strings.Contains(text, "exact CLI answer") || strings.Contains(text, "pending:") {
			t.Fatalf("completed retry after daemon stop=%s error=%v", text, err)
		}
	}
	runs, err := os.ReadFile(log)
	if err != nil || len(strings.Fields(string(runs))) != 3 {
		t.Fatalf("retry or wait reran native work: runs=%q error=%v", runs, err)
	}
	messages, err := a.ConversationMessages(group.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, m := range messages {
		if m.Kind == envelope.KindQuestion && m.Dir == "out" {
			counts[m.LID]++
		}
	}
	for _, id := range []string{firstID, failedID, offlineID} {
		if counts[id] != 1 {
			t.Fatalf("request %s has %d stored logical messages", id, counts[id])
		}
	}
}
