package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

const refusedText = "is not allowed inside a run AgentNet started for a request"

// guardedCommands are command lines the run guard refuses, one each: every
// send, decision, trust, participation or group change, and every command
// that changes this installation, its person or its Hub, or reaches past
// AgentNet (dir holds the files they would write). Each returns at once
// even unguarded, so a broken guard fails the test rather than hanging it;
// commands that would keep running (daemon, a2a serve, ui --demo, open) are
// checked in TestRunGuardIsAnAllowList.
func guardedCommands(dir string) [][]string {
	id, person, hash := protocol.NewID(), protocol.NewID(), strings.Repeat("ab", 32)
	return [][]string{
		{"send", "peer/desk", "hello"},
		{"send", "--reply-to", id, "--progress", "peer/desk", "halfway"},
		{"reply", id, "done"},
		{"ask", "peer/desk", "where is it?"},
		{"task", "peer/desk", "ship it"},
		{"accept", id},
		{"accept", "--always", "peer/desk"},
		{"approve", "peer/desk"},
		{"approve", "--tasks", "peer/desk"},
		{"unapprove", "peer/desk"},
		{"unapprove", "--tasks", "peer/desk"},
		{"decline", id},
		{"trust", "peer/desk"},
		{"resolve", id},
		{"cancel", id},
		{"dm", "new", "peer/desk"},
		{"dm", "send", id, "hello"},
		{"dm", "send", "--task", id, "ship it"},
		{"dm", "invite", id, "peer/desk"},
		{"dm", "accept-agent", id},
		{"dm", "decline-agent", id},
		{"dm", "dismiss-agent", id},
		{"dm", "ask-agent", id, "where is it?"},
		{"group", "create", "Ops"},
		{"group", "invite", id, person},
		{"group", "accept", hash},
		{"group", "decline", hash},
		{"group", "retry", hash},
		{"group", "rename", id, "Ops"},
		{"group", "promote", id, person},
		{"group", "demote", id, person},
		{"group", "remove", id, person},
		{"group", "leave", id},
		{"group", "request-file", id, id, "0"},
		{"team", "create", "Ops"},
		{"team", "join", id},
		{"team", "leave", id},
		{"operator", "grant", "peer/desk"},
		{"operator", "revoke", "peer/desk"},
		{"person", "create", "Me"},
		{"person", "rename", "Me"},
		{"person", "service"},
		{"person", "link"},
		{"person", "approve", id},
		{"person", "refuse", id},
		{"person", "remove", "peer/desk"},
		{"responder", "set", "--harness", "claude", "--dir", dir},
		{"responder", "set", "--harness", "claude", "--dir", dir, "--context", filepath.Join(dir, "secret.txt")},
		{"responder", "off"},
		{"review-to", "peer/desk"},
		{"review-to", "--off"},
		{"remind", id, "tomorrow"},
		{"remind", "done", id},
		{"remind", "cancel", id},
		{"inbox"},
		{"inbox", "--unread"},
		{"inbox", "--review"},
		{"cleanup"},
		{"admin", "invite", "carol"},
		{"admin", "revoke", "peer/desk"},
		{"admin", "release"},
		{"ui"},
		{"update", "--status"},
		{"hooks", "install", "claude", "--file", filepath.Join(dir, "settings.json")},
		{"hooks", "remove", "claude", "--file", filepath.Join(dir, "settings.json")},
		{"join", "--agent", "second", "not-a-code"},
		{"frobnicate"}, // a command added later is refused until it is allowed
	}
}

// runEnv sets the environment the worker gives a harness run (worker.go).
func runEnv(t *testing.T, background, request, requester, binding string) {
	t.Helper()
	t.Setenv(client.BackgroundEnv, background)
	t.Setenv(client.ProgressRequestEnv, request)
	t.Setenv(client.ProgressPeerEnv, requester)
	t.Setenv(replyBindingEnv, binding)
}

// homeRows renders every row of the home's database.
func homeRows(t *testing.T, home string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	var b strings.Builder
	for _, name := range tables {
		rows, err := db.Query(`SELECT * FROM "` + name + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %v\n", name, values)
		}
		rows.Close()
	}
	return b.String()
}

// Inside a run every command that would send, decide or change a
// conversation for the local user is refused, plainly and with what the run
// can do instead, and the home is left as it was.
func TestRunGuardRefusesInsideARun(t *testing.T) {
	_, home := diagnosticAgent(t)
	dir := t.TempDir()
	runEnv(t, "1", "", "", "")
	before := homeRows(t, home)
	for _, args := range guardedCommands(dir) {
		err := run(append([]string{"--home", home}, args...))
		if err == nil || !strings.Contains(err.Error(), refusedText) || !strings.Contains(err.Error(), "in your final output") {
			t.Errorf("%v: %v", args, err)
		}
		if homeRows(t, home) != before {
			t.Fatalf("%v changed the home", args)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused command wrote %v", entries)
	}
}

// Inside a run only the commands below pass the guard: reading, help, the
// harness's own hook (which does nothing there) and send, which sendGuard
// decides once its flags are parsed. Everything else is refused, with no
// arguments or with any, including a command added later.
func TestRunGuardIsAnAllowList(t *testing.T) {
	runEnv(t, "1", "", "", "")
	id := protocol.NewID()
	for _, args := range [][]string{
		{"version"}, {"version", "--schema"}, {"skill"}, {"whoami"}, {"approvals"}, {"status", "--wait", "30s", id},
		{"fingerprint", "peer/desk"}, {"sessions", "peer/desk"}, {"members"}, {"doctor"}, {"conversation", id},
		{"receivers", "--json"}, {"download", "--dir", t.TempDir(), id},
		{"inbox", "--peek"}, {"inbox", "--unread", "--json", "--peek"}, {"inbox", "-peek=true", "--review"},
		{"team", "list"}, {"team", "snapshot", id}, {"dm", "list"}, {"dm", "show", id}, {"dm", "agents", id},
		{"group", "invitations"}, {"person"}, {"person", "links"}, {"operator", "list"}, {"review-to"},
		{"responder", "list"}, {"responder", "show"}, {"remind", "list"}, {"remind", "list", "--all"},
		{"send", "peer/desk", "hello"}, {"hook", "claude"},
	} {
		if err := runGuard(args[0], args[1:]); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"daemon"}, {"daemon", "--ui", "127.0.0.1:0"}, {"a2a", "serve", "--peer", "peer/desk"}, {"ui", "--demo"},
		{"open", "--review"}, {"open", id}, {"hub", "serve"}, {"hub", "bootstrap-invite"}, {"update"}, {"update", "--check"},
		{"update", "v0.0.1"}, {updateHelperCmd}, {"hooks", "show", "claude"}, {"join", "--agent", "x", "CODE"},
		{"inbox", "--peek=false"}, {"inbox", "--peek", "--bogus"}, {"inbox", "--json"},
		{"team"}, {"team", "frobnicate"}, {"dm"}, {"dm", "frobnicate"}, {"group"}, {"group", "frobnicate"},
		{"person", "frobnicate"}, {"operator"}, {"responder"}, {"responder", "frobnicate"}, {"remind"}, {"remind", "frobnicate", "tomorrow"},
		{"frobnicate"}, {"frobnicate", "list"},
	} {
		if err := runGuard(args[0], args[1:]); err == nil || !strings.Contains(err.Error(), refusedText) {
			t.Errorf("%v: %v", args, err)
		}
	}
	// Every command help lists, with no arguments: only these pass.
	bare := map[string]bool{"version": true, "skill": true, "whoami": true, "approvals": true, "status": true, "fingerprint": true,
		"sessions": true, "members": true, "doctor": true, "conversation": true, "receivers": true, "download": true,
		"person": true, "review-to": true, "send": true, "hook": true}
	for name := range topics {
		if strings.Contains(name, " ") || guides[name] {
			continue
		}
		if err := runGuard(name, nil); (err == nil) != bare[name] {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Help and the harness's own hook still work through run itself.
	home := t.TempDir()
	for _, args := range [][]string{{"help", "dm"}, {"dm", "--help"}, {"hook", "claude"}} {
		if _, err := diagnosticOutput(t, func() error { return run(append([]string{"--home", home}, args...)) }); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// The refusal does not name the switch that turns the guard off, and says
// not to change AgentNet's environment to get around it.
func TestRunGuardRefusalKeepsTheSwitchOut(t *testing.T) {
	for _, env := range [][3]string{{"", "", ""}, {protocol.NewID(), "peer/desk", ""}, {"", "", protocol.NewID()}} {
		runEnv(t, "1", env[0], env[1], env[2])
		msg := refusedInRun("dm invite").Error()
		if strings.Contains(msg, client.BackgroundEnv) || !strings.Contains(msg, refusedText) || !strings.Contains(msg, "Do not change AgentNet's environment to get around this") {
			t.Errorf("%v: %s", env, msg)
		}
	}
}

// In a reply receiver's run the refusal offers exactly the follow-up the
// guard allows: an answer to a message of the binding, with ask or task for a
// device request and dm send --question or --task in a conversation.
func TestRunGuardRefusalInAReceiverRun(t *testing.T) {
	runEnv(t, "1", "", "", protocol.NewID())
	msg := refusedInRun("dm invite").Error()
	for _, want := range []string{
		"agentnet ask (or task) --reply-to ID ADDRESS TEXT",
		"agentnet dm send --question (or --task) --reply-to ID CONV TEXT",
		"ID is a message of your reply binding",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("no %q in: %s", want, msg)
		}
	}
	if strings.Contains(msg, "without --reply-receiver") || strings.Contains(msg, "ask-agent") {
		t.Errorf("offers what the guard refuses: %s", msg)
	}
}

// Outside a run (no env, or any value but the worker's "1") the guard lets
// every command through to its own handler, as before.
func TestRunGuardOffOutsideARun(t *testing.T) {
	for _, value := range []string{"", "0", "true"} {
		runEnv(t, value, protocol.NewID(), "peer/desk", "")
		for _, args := range guardedCommands(t.TempDir()) {
			if err := runGuard(args[0], args[1:]); err != nil {
				t.Errorf("%q %v: %v", value, args, err)
			}
		}
		if err := sendGuard(""); err != nil {
			t.Errorf("%q send: %v", value, err)
		}
		if err := sendGuard(protocol.NewID()); err != nil {
			t.Errorf("%q send --reply-to: %v", value, err)
		}
	}
}

// A run may still send what its prompt offers: an update or a clarification
// to its own request ($AGENTNET_REQUEST_ID), and both reach the requester.
// Any other send is refused, and the refusal names that route.
func TestRunGuardLetsARunUpdateItsOwnRequest(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	runEnv(t, "", "", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, _ := guardPeer(t, ctx, testhub.BootstrapCode(t, hub), "alice")
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, bobHome := guardPeer(t, ctx, code, "laptop")

	send := func(args ...string) error {
		_, err := diagnosticOutput(t, func() error { return run(append([]string{"--home", bobHome, "send", "--wait", "30s"}, args...)) })
		return err
	}
	if err := send(alice.Address, "outside a run"); err != nil {
		t.Fatalf("send outside a run: %v", err)
	}
	asked, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindQuestion, Body: "how far along?", Wait: 30 * time.Second})
	if err != nil || asked.State != protocol.StateDelivered {
		t.Fatalf("question: %+v %v", asked, err)
	}
	// Progress goes only to a requester whose every session reads it; alice's
	// daemon publishes that with the rest of its capabilities.
	eventually(t, "alice's published capabilities", func() bool { _, err := bob.AgentCatalog(ctx, alice.Address); return err == nil })

	runEnv(t, "1", asked.ID, alice.Address, "")
	if err := send("--reply-to", asked.ID, "--progress", alice.Address, "halfway"); err != nil {
		t.Fatalf("progress to the run's request: %v", err)
	}
	if err := send("--reply-to", asked.ID, alice.Address, "which branch?"); err != nil {
		t.Fatalf("clarification to the run's request: %v", err)
	}
	for _, args := range [][]string{
		{"--reply-to", protocol.NewID(), "--progress", alice.Address, "elsewhere"},
		{alice.Address, "unasked"},
	} {
		err := send(args...)
		if err == nil || !strings.Contains(err.Error(), refusedText) || !strings.Contains(err.Error(), `agentnet send --reply-to "$AGENTNET_REQUEST_ID" --progress "$AGENTNET_REQUESTER" TEXT`) {
			t.Errorf("%v: %v", args, err)
		}
	}
	got := map[string]string{}
	msgs, err := alice.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		got[m.Body] = m.ReplyTo + "/" + m.Status
	}
	if got["halfway"] != asked.ID+"/progress" || got["which branch?"] != asked.ID+"/" {
		t.Errorf("alice holds %q", got)
	}

	db, err := sql.Open("sqlite", filepath.Join(bobHome, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sent []string
	rows, err := db.Query(`SELECT body, coalesce(reply_to,''), coalesce(status,''), state, coalesce(error,'') FROM outbox WHERE recipient=? AND sub IS NULL`, alice.Address) // not the daemon's own status records
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body, replyTo, status, state, why string
		if err := rows.Scan(&body, &replyTo, &status, &state, &why); err != nil {
			t.Fatal(err)
		}
		if replyTo != "" && replyTo != asked.ID {
			t.Errorf("%q went to %s", body, replyTo)
		}
		sent = append(sent, body+"/"+status+"/"+state+why)
	}
	slices.Sort(sent)
	// Both reached alice: the progress update and the clarification.
	if strings.Join(sent, " | ") != "halfway/progress/delivered | outside a run//delivered | which branch?//delivered" {
		t.Fatalf("bob sent %q", sent)
	}
}

// guardPeer joins an agent to the test Hub and runs its daemon until the
// test ends (AGENTNET_NOTIFY is off for the caller).
func guardPeer(t *testing.T, ctx context.Context, code, name string) (*client.Agent, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	a, err := client.Join(ctx, home, code, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	daemon, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(daemon, client.RunOptions{}); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	return a, home
}

// eventually waits up to 30 seconds for ok.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// In a selected reply receiver's run (AGENTNET_REPLY_BINDING), ask, task
// and dm send carry on the local user's delegated work only as an answer to
// a message of that run's binding (--reply-to), which also keeps them with
// that thread's peer. Without it, with one from outside the binding, as a
// plain dm send, with --agent or --follow-up, with dm ask-agent or another
// receiver, they are refused.
func TestRunGuardKeepsAReceiverRunOnItsBinding(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	runEnv(t, "", "", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, aliceHome := guardPeer(t, ctx, testhub.BootstrapCode(t, hub), "alice")
	invite := func(label string) string {
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	bob, _ := guardPeer(t, ctx, invite("bob"), "laptop")
	carol, _ := guardPeer(t, ctx, invite("carol"), "desk")

	sent, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindQuestion, Body: "original request",
		ReplyReceiver: &client.ReplyReceiver{Kind: "human"}, Wait: 30 * time.Second})
	if err != nil || sent.State != protocol.StateDelivered {
		t.Fatalf("original request: %+v %v", sent, err)
	}
	if _, err := bob.Reply(ctx, sent.ID, "which branch?"); err != nil {
		t.Fatal(err)
	}
	var binding client.ReplyReceiverBinding
	eventually(t, "bob's clarification bound to alice's receiver", func() bool {
		rows, err := alice.ReplyReceiverBindings()
		if err == nil && len(rows) == 1 && len(rows[0].Inputs) == 1 {
			binding = rows[0]
			return true
		}
		return false
	})
	input := binding.Inputs[0].ID

	runEnv(t, "1", "", "", binding.ID)
	cli := func(args ...string) error {
		_, err := diagnosticOutput(t, func() error { return run(append([]string{"--home", aliceHome}, args...)) })
		return err
	}
	for _, args := range [][]string{
		{"task", "--wait", "0", carol.Address, "unrelated work"},
		{"ask", "--wait", "0", bob.Address, "no reference"},
		{"ask", "--wait", "0", "--reply-to", protocol.NewID(), bob.Address, "outside the binding"},
		{"ask", "--wait", "0", "--reply-to", input, "--agent", "helper", bob.Address, "a named agent"},
		{"ask", "--wait", "0", "--reply-to", input, "--follow-up", "summarize", bob.Address, "a follow-up"},
		{"ask", "--wait", "0", "--reply-to", input, "--reply-receiver", "human", bob.Address, "another receiver"},
		{"ask", "--wait", "0", "--reply-to", input, "--reply-binding", protocol.NewID(), bob.Address, "another binding"},
		{"dm", "send", "--reply-to", input, protocol.NewID(), "a plain message"},
		{"dm", "ask-agent", protocol.NewID(), "an agent"},
		{"send", "--wait", "0", "--reply-to", input, bob.Address, "a plain send"},
		{"reply", input, "an answer"},
	} {
		if err := cli(args...); err == nil || !strings.Contains(err.Error(), refusedText) {
			t.Errorf("%v: %v", args, err)
		}
	}
	// A message of the binding still keeps the send with its thread's peer.
	if err := cli("task", "--wait", "0", "--reply-to", input, carol.Address, "elsewhere"); err == nil {
		t.Error("a follow-up went to carol")
	}
	if err := cli("ask", "--wait", "0", "--reply-to", input, bob.Address, "follow-up on the original"); err != nil {
		t.Fatalf("follow-up on the binding: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(aliceHome, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sent2 []string
	rows, err := db.Query(`SELECT recipient, body, coalesce(reply_receiver,'') FROM outbox ORDER BY created_ms`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var to, body, receiver string
		if err := rows.Scan(&to, &body, &receiver); err != nil {
			t.Fatal(err)
		}
		sent2 = append(sent2, fmt.Sprintf("%s %q %t", to, body, receiver == binding.ID))
	}
	want := []string{fmt.Sprintf("%s %q true", bob.Address, "original request"), fmt.Sprintf("%s %q true", bob.Address, "follow-up on the original")}
	if !slices.Equal(sent2, want) {
		t.Fatalf("alice sent %q, want %q", sent2, want)
	}
}
