package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// notesTo captures the origin notes the CLI prints.
func notesTo(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := originNotes
	originNotes = &b
	t.Cleanup(func() { originNotes = old })
	return &b
}

// A CLI request names its origin session only through the harness's own
// session environment; a named session that cannot be the receiver is never
// refused (MEL-537): its answer goes to this computer's inbox, with one plain
// note saying so. An explicit receiver or a background job decides alone,
// and a plain command keeps the inbox without a note.
func TestCLIOriginSelection(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_BINDING", "AGENTNET_BACKGROUND", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	notes := notesTo(t)
	request := originSelection(t, true)
	if kind, err := request(); kind != "" || err != nil || notes.Len() != 0 {
		t.Fatalf("plain command: %q %v %q", kind, err, notes)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	if kind, err := request(); kind != "" || err != nil || !strings.Contains(notes.String(), "inbox") || !strings.Contains(notes.String(), "agentnet conversation ID") {
		t.Fatalf("unregistered Claude origin: %q %v %q", kind, err, notes)
	}
	notes.Reset()
	if kind, err := originSelection(t, true, "--reply-receiver", "human")(); kind != "human" || err != nil || notes.Len() != 0 {
		t.Fatalf("explicit human override: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "1")
	if kind, err := request(); kind != "" || err != nil || notes.Len() != 0 {
		t.Fatalf("background job captured an interactive origin: %q %v", kind, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "")
	t.Setenv("CODEX_THREAD_ID", "synthetic-codex-thread")
	if kind, err := request(); kind != "" || err != nil || !strings.Contains(notes.String(), "cannot tell which one asked") {
		t.Fatalf("both harness names: %q %v %q", kind, err, notes)
	}
	notes.Reset()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	if kind, err := request(); kind != "" || err != nil || !strings.Contains(notes.String(), "inbox") {
		t.Fatalf("unregistered Codex origin: %q %v %q", kind, err, notes)
	}
}

// Asking from a session that cannot take the answer itself sends the
// question with no receiver bound: it is not refused, and the note says the
// answer comes to this computer's inbox.
func TestCLIOriginWithoutChannelUsesInbox(t *testing.T) {
	for _, k := range []string{"AGENTNET_REPLY_SESSION", "AGENTNET_BACKGROUND", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	t.Setenv("AGENTNET_NOTIFY", "off")
	notes := notesTo(t)
	a, home := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := a.Invite(ctx, "peer", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	if err := runSendKind(ctx, a, "ask", []string{"--wait", "0", "--answer-wait", "0", peer.Address, "where is the shipment?"}); err != nil {
		t.Fatalf("ask from a Claude session without a channel: %v", err)
	}
	if !strings.Contains(notes.String(), "inbox") {
		t.Fatalf("note %q", notes)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sent, bound int
	db.QueryRow(`SELECT count(*), count(reply_receiver) FROM outbox`).Scan(&sent, &bound)
	if sent != 1 || bound != 0 {
		t.Fatalf("outbox %d rows, %d bound", sent, bound)
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
	t.Setenv("CLAUDECODE", "") // as from a terminal, even when these tests run under Claude Code
	message, request := originSelection(t, false), originSelection(t, true)
	notes := notesTo(t)
	for _, c := range []struct {
		name string
		env  map[string]string
		note string
	}{
		{"Claude", map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-claude-session"}, "not started by Claude Code"},
		{"Codex", map[string]string{"CODEX_THREAD_ID": "synthetic-codex-thread"}, "no exact AgentNet registration"},
		{"both", map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-claude-session", "CODEX_THREAD_ID": "synthetic-codex-thread"}, "cannot tell which one asked"},
		{"Pi", map[string]string{"AGENTNET_REPLY_SESSION": "synthetic-pi-handle", "AGENTNET_REPLY_SESSION_HOME": t.TempDir(), "AGENTNET_REPLY_SESSION_GENERATION": "1"}, "another AgentNet home"},
	} {
		for _, k := range origins {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		notes.Reset()
		if kind, err := message(); kind != "" || err != nil || notes.Len() != 0 {
			t.Fatalf("%s: plain message blocked or bound: %q %v %q", c.name, kind, err, notes)
		}
		if kind, err := request(); kind != "" || err != nil || !strings.Contains(notes.String(), c.note) || !strings.Contains(notes.String(), "inbox") {
			t.Fatalf("%s: request refused or bound without a live session: %q %v %q", c.name, kind, err, notes)
		}
	}
	for _, k := range origins {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-claude-session")
	if kind, err := originSelection(t, false, "--reply-receiver", "human")(); kind != "human" || err != nil {
		t.Fatalf("explicit receiver on a plain message: %q %v", kind, err)
	}
	// --on-close-agent still needs that exact session: it fails loudly.
	if _, err := originSelection(t, false, "--on-close-agent", strings.Repeat("a", 32))(); err == nil || !strings.Contains(err.Error(), "on-close-agent requires an exact registered native reply receiver") {
		t.Fatalf("on-close-agent on a plain message skipped origin: %v", err)
	}

	// A registered session that could take the answer is no different: a
	// plain message from it still binds nothing, a request returns to it, and
	// --on-close-agent still binds it for that continuation.
	a, home := diagnosticAgent(t)
	file := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(file, []byte(`{"type":"session","id":"s1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := a.RegisterReplySession(client.ReplySessionRegistration{Harness: "pi", SessionID: "s1", File: file})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range origins {
		t.Setenv(k, "")
	}
	t.Setenv("AGENTNET_REPLY_SESSION", owner.Handle)
	t.Setenv("AGENTNET_REPLY_SESSION_HOME", home)
	t.Setenv("AGENTNET_REPLY_SESSION_GENERATION", strconv.FormatInt(owner.Generation, 10))
	registered := func(request bool, args ...string) (*client.ReplyReceiver, error) {
		fs := flag.NewFlagSet("dm send", flag.ContinueOnError)
		f := receiverFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return f.selected(a, request)
	}
	if r, err := registered(false); r != nil || err != nil {
		t.Fatalf("plain message from a registered session bound a receiver: %+v %v", r, err)
	}
	if r, err := registered(true); err != nil || r == nil || r.Kind != "live_session" || r.SessionHandle != owner.Handle {
		t.Fatalf("request from a registered session lost origin return: %+v %v", r, err)
	}
	if r, err := registered(false, "--on-close-agent", strings.Repeat("a", 32)); err != nil || r == nil || r.Kind != "live_session" || r.SessionHandle != owner.Handle || r.OnClose == nil {
		t.Fatalf("on-close-agent on a plain message from a registered session: %+v %v", r, err)
	}
}

// The receivers help states the origin rules the code keeps: a request
// returns to a live registered session or to this computer's inbox, never
// refused; an ended session's answers go to the inbox; a plain message
// selects no receiver unless --on-close-agent is given.
func TestReceiversHelpStatesOriginRules(t *testing.T) {
	text := topics["receivers"]
	for _, want := range []string{"Pi/OMP, Claude Code or Codex", "never refused", "inbox", "hooks", "session ends", "unless --on-close-agent", "background job"} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
			t.Errorf("receivers help lacks %q:\n%s", want, text)
		}
	}
}

// The live audit: dm send run by Claude Code in a session AgentNet cannot
// return answers to (no Claude config here). The plain message is stored with
// no receiver bound; a question or task from that session (dm send
// --question/--task) is sent too, its answer bound for this computer's inbox
// (MEL-537); nothing binds a receiver.
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
	// Not refused for where the answer would go: the note says the inbox,
	// and only the group's own rule (people's turns there are plain
	// messages) stops a question or task.
	notes := notesTo(t)
	for _, kind := range []string{"--question", "--task"} {
		notes.Reset()
		err = runDM(ctx, a, []string{"send", kind, "--answer-wait", "0", conv, "where is the shipment?"}, io.Discard)
		if err == nil || strings.Contains(err.Error(), "--reply-receiver") || !strings.Contains(err.Error(), "only ordinary human messages") || !strings.Contains(notes.String(), "inbox") {
			t.Fatalf("%s from an unregistered Claude session: %v %q", kind, err, notes)
		}
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil || len(msgs) != 1 || msgs[0].Kind != envelope.KindMessage || msgs[0].Body != "plain note" {
		t.Fatalf("conversation: %+v %v", msgs, err)
	}
	if rows, err := a.ReplyReceiverBindings(); err != nil || len(rows) != 0 {
		t.Fatalf("plain message bound a receiver: %+v %v", rows, err)
	}
}
