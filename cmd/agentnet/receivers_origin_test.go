package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func originSelection(t *testing.T, request bool, args ...string) func() (string, error) {
	t.Helper()
	a, _ := diagnosticAgent(t)
	return func() (string, error) {
		fs := flag.NewFlagSet("ask", flag.ContinueOnError)
		f := receiverFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		r, err := f.selected(a, request)
		if r == nil {
			return "", err
		}
		return r.Kind, err
	}
}

// A CLI request names its origin session only through the harness's own
// session environment; a named session that cannot be the receiver refuses
// (never the inbox), an explicit receiver or a background job decides alone,
// and a plain command keeps the inbox.
func TestCLIOriginSelection(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_BINDING", "AGENTNET_BACKGROUND", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	plain := originSelection(t, true)
	if kind, err := plain(); kind != "" || err != nil {
		t.Fatalf("plain command: %q %v", kind, err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	if kind, err := plain(); kind != "" || err == nil || !strings.Contains(err.Error(), "Claude Code session synthetic-claude-session") || !strings.Contains(err.Error(), "--reply-receiver human") {
		t.Fatalf("unregistered Claude origin: %q %v", kind, err)
	}
	if kind, err := originSelection(t, true, "--reply-receiver", "human")(); kind != "human" || err != nil {
		t.Fatalf("explicit human override: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "1")
	if kind, err := plain(); kind != "" || err != nil {
		t.Fatalf("background job captured an interactive origin: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "")
	t.Setenv("CODEX_THREAD_ID", "synthetic-codex-thread")
	if _, err := plain(); err == nil || !strings.Contains(err.Error(), "cannot tell which assistant") {
		t.Fatalf("both harness names: %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := plain(); err == nil || !strings.Contains(err.Error(), "Codex thread synthetic-codex-thread") {
		t.Fatalf("unregistered Codex origin: %v", err)
	}
}

// A refused origin sends nothing: no outbox row, no binding.
func TestCLIOriginRefusalSendsNothing(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_BACKGROUND", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	a, home := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runSendKind(ctx, a, "ask", []string{"peer/desk", "where is the shipment?"}); err == nil || !strings.Contains(err.Error(), "--reply-receiver human") {
		t.Fatalf("ask from an unregistered Claude session: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused request queued %d outbox rows", n)
	}
}

// A plain message expects no answer, so the Pi, Claude Code or Codex session
// it is sent from selects no receiver and never blocks it; a question or task
// from the same session keeps origin return, an explicit receiver still binds,
// and --on-close-agent still asks for the origin session.
func TestCLIPlainMessageSkipsOrigin(t *testing.T) {
	origins := []string{"AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_SESSION_HOME", "AGENTNET_REPLY_SESSION_GENERATION", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"}
	for _, k := range append([]string{"AGENTNET_REPLY_BINDING", "AGENTNET_BACKGROUND"}, origins...) {
		t.Setenv(k, "")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	message, request := originSelection(t, false), originSelection(t, true)
	for _, c := range []struct {
		name    string
		env     map[string]string
		refusal string
	}{
		{"Claude", map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-claude-session"}, "Claude Code session synthetic-claude-session"},
		{"Codex", map[string]string{"CODEX_THREAD_ID": "synthetic-codex-thread"}, "Codex thread synthetic-codex-thread"},
		{"both", map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-claude-session", "CODEX_THREAD_ID": "synthetic-codex-thread"}, "cannot tell which assistant"},
		{"Pi", map[string]string{"AGENTNET_REPLY_SESSION": "synthetic-pi-handle", "AGENTNET_REPLY_SESSION_HOME": t.TempDir(), "AGENTNET_REPLY_SESSION_GENERATION": "1"}, "another AgentNet home"},
	} {
		for _, k := range origins {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if kind, err := message(); kind != "" || err != nil {
			t.Fatalf("%s: plain message blocked or bound: %q %v", c.name, kind, err)
		}
		if _, err := request(); err == nil || !strings.Contains(err.Error(), c.refusal) {
			t.Fatalf("%s: request lost origin return: %v", c.name, err)
		}
	}
	for _, k := range origins {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	if kind, err := originSelection(t, false, "--reply-receiver", "human")(); kind != "human" || err != nil {
		t.Fatalf("explicit receiver on a plain message: %q %v", kind, err)
	}
	if _, err := originSelection(t, false, "--on-close-agent", strings.Repeat("a", 32))(); err == nil || !strings.Contains(err.Error(), "Claude Code session synthetic-claude-session") {
		t.Fatalf("on-close-agent on a plain message skipped origin: %v", err)
	}
}

// The live audit: dm send run by Claude Code in a session AgentNet cannot
// return answers to (no Claude config here). The plain message is stored with
// no receiver bound; a question from that session is refused and stores
// nothing.
func TestCLIDMSendPlainFromHarnessSession(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_BINDING", "AGENTNET_BACKGROUND", "CODEX_THREAD_ID", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(k, "")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	a, _ := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	code, err := a.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	person, err := bob.CreatePerson(ctx, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	// Both people consent through the native signed group state operations.
	packet, err := a.CreateGroup(ctx, "Origin group")
	if err != nil {
		t.Fatal(err)
	}
	admission, err := bob.SignGroupAdmission(packet.Root, 1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	packet.Proof = nil
	packet.State.Seq, packet.State.Prev = 1, packet.State.Hash()
	packet.State.Members = append(slices.Clone(packet.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.Person, Roster: person.Roster}, Admission: admission})
	slices.SortFunc(packet.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	if packet, err = a.SignGroupState(ctx, packet); err != nil {
		t.Fatal(err)
	}
	commit, err := a.BuildGroupCommit(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.PublishGroup(ctx, commit, packet); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	var out bytes.Buffer
	if err = runDM(ctx, a, []string{"send", conv, "plain note"}, &out); err != nil || out.Len() == 0 {
		t.Fatalf("plain message from an unregistered Claude session: %q %v", out.String(), err)
	}
	if err = runDM(ctx, a, []string{"send", "--question", conv, "where is the shipment?"}, io.Discard); err == nil || !strings.Contains(err.Error(), ".claude") || !strings.Contains(err.Error(), "--reply-receiver human") {
		t.Fatalf("question from an unregistered Claude session: %v", err)
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil || len(msgs) != 1 || msgs[0].Kind != envelope.KindMessage || msgs[0].Body != "plain note" {
		t.Fatalf("conversation: %+v %v", msgs, err)
	}
	if rows, err := a.ReplyReceiverBindings(); err != nil || len(rows) != 0 {
		t.Fatalf("plain message bound a receiver: %+v %v", rows, err)
	}
}
