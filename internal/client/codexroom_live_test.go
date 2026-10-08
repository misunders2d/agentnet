package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// One real ephemeral Codex question; all identities, Hub and target replies are
// synthetic. Opt in with an absolute installed binary, never an open session.
// The normal inherited Codex config/environment remains intact: no model,
// reasoning, plugin, skill or MCP settings are substituted by this test.
func TestCodexRoomActualQuestionSmoke(t *testing.T) {
	configured := os.Getenv("AGENTNET_LIVE_CODEX_ROOM")
	if configured == "" {
		t.Skip("opt-in: AGENTNET_LIVE_CODEX_ROOM=/absolute/path/to/codex")
	}
	if !filepath.IsAbs(configured) {
		t.Fatal("AGENTNET_LIVE_CODEX_ROOM must be an absolute installed Codex binary")
	}
	binary, err := filepath.EvalSymlinks(configured)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("installed Codex binary unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	versionCtx, stopVersion := context.WithTimeout(ctx, 5*time.Second)
	version, err := exec.CommandContext(versionCtx, binary, "--version").Output()
	stopVersion()
	if err != nil {
		t.Fatalf("installed Codex version: %v", err)
	}
	t.Logf("installed Codex %s; model/reasoning inherited from normal configuration (not overridden)", strings.TrimSpace(string(version)))
	original := Harnesses["codex"]
	selected := original
	selected.bin = binary
	Harnesses["codex"] = selected
	// Registered before fixture daemons: they stop before restoring this map.
	t.Cleanup(func() { Harnesses["codex"] = original })
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	if err = w.alice.SetResponder(&Responder{Harness: "codex", Dir: t.TempDir(), Timeout: 120 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir, Timeout: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{w.alice.Address, carol.Address} {
		if err = w.bob.Approve(address); err != nil {
			t.Fatal(err)
		}
	}
	source := p6Member(t, w.alice, w.alice, conv)
	target := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "target knows the current source agent", func() bool { return stateAt(t, w.bob, source.PID).Claimable() })
	eventually(t, "human knows both current agents", func() bool {
		return stateAt(t, carol, source.PID).Claimable() && stateAt(t, carol, target.PID).Claimable()
	})
	// ask waits for its correlated reply itself. No shell, network, other tool,
	// task, file access, continuation, model retry or permission change is asked.
	prompt := fmt.Sprintf("Compatibility smoke in this synthetic AgentNet group. Use only the dynamic agentnet_room tool exactly once with action ask, pid %q, and text %q. Await that tool's correlated reply, then give that reply as your final answer. Do not use shell commands, other tools, files or tasks, and do not invent an answer if the tool is unavailable.", target.PID, "Explain the synthetic deploy failure")
	root, err := carol.AskAgent(ctx, source.PID, envelope.KindQuestion, prompt)
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := jobState(t, w.alice, root.LID)
		if state == stateAnswered {
			break
		}
		if state == stateJobFailed || state == stateNeedHuman || state == stateDeclined {
			t.Fatalf("real Codex question ended %s; no model retry", state)
		}
		select {
		case <-ctx.Done():
			t.Fatal("real Codex smoke exceeded the fixed 180-second budget; no retry")
		case <-ticker.C:
		}
	}
	// Check admitted, verified signed content at the target, not model claims.
	targetMessages, err := w.bob.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var child ConvMessage
	count := 0
	for _, m := range targetMessages {
		if m.Kind == envelope.KindQuestion && m.PID == target.PID && m.ReplyTo == root.LID {
			count++
			child = m
		}
	}
	if count != 1 || child.Key != w.alice.Self().Fingerprint() || child.From != w.alice.Address || child.Replica || child.Human == nil || child.Human.AuthorPID != source.PID || child.Body != "Explain the synthetic deploy failure" {
		t.Fatalf("missing exact signed correlated child: count=%d child=%+v", count, child)
	}
	messages, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var answer ConvMessage
	count = 0
	for _, m := range messages {
		if m.Kind == envelope.KindAnswer && m.ReplyTo == child.LID && m.PID == target.PID {
			count++
			answer = m
		}
	}
	if count != 1 || !answer.VerifiedAgent || answer.Key != w.bob.Self().Fingerprint() || answer.From != w.bob.Address || answer.Replica || !strings.Contains(answer.Body, "the deploy failed at step 3") {
		t.Fatalf("missing verified correlated target reply: count=%d answer=%+v", count, answer)
	}
	if stub.runs() != 1 {
		t.Fatalf("target ran %d times, want exactly one child question", stub.runs())
	}
	// Final human-facing result must complete the original request as well.
	for {
		replies, _ := carol.ConversationMessages(conv)
		for _, m := range replies {
			if m.Kind == envelope.KindAnswer && m.ReplyTo == root.LID && m.PID == source.PID && m.VerifiedAgent && m.Key == w.alice.Self().Fingerprint() && strings.Contains(strings.ToLower(m.Body), "the deploy failed at step 3") {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("original human question has no verified completed answer within fixed budget")
		case <-ticker.C:
		}
	}
}
