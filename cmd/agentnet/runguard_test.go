package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
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

const refusedText = "is not allowed inside an agent run (AGENTNET_BACKGROUND=1)"

// guardedCommands is every command the run guard refuses, one line each.
func guardedCommands() [][]string {
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
	runEnv(t, "1", "", "", "")
	before := homeRows(t, home)
	for _, args := range guardedCommands() {
		err := run(append([]string{"--home", home}, args...))
		if err == nil || !strings.Contains(err.Error(), refusedText) || !strings.Contains(err.Error(), "in your final output") {
			t.Errorf("%v: %v", args, err)
		}
		if homeRows(t, home) != before {
			t.Fatalf("%v changed the home", args)
		}
	}
}

// Outside a run (no env, or any value but the worker's "1") the guard lets
// every command through to its own handler, as before.
func TestRunGuardOffOutsideARun(t *testing.T) {
	for _, value := range []string{"", "0", "true"} {
		runEnv(t, value, protocol.NewID(), "peer/desk", "")
		for _, args := range guardedCommands() {
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
// to its own request ($AGENTNET_REQUEST_ID). Any other send is refused, and
// the refusal names that route.
func TestRunGuardLetsARunUpdateItsOwnRequest(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	runEnv(t, "", "", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, hub), "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bobHome := filepath.Join(t.TempDir(), "bob")
	bob, err := client.Join(ctx, bobHome, code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	daemon, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { bob.Run(daemon, client.RunOptions{}); close(done) }()
	t.Cleanup(func() { stop(); <-done })

	send := func(args ...string) error {
		_, err := diagnosticOutput(t, func() error { return run(append([]string{"--home", bobHome, "send", "--wait", "0"}, args...)) })
		return err
	}
	if err := send(alice.Address, "outside a run"); err != nil {
		t.Fatalf("send outside a run: %v", err)
	}
	asked, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindQuestion, Body: "how far along?", Wait: 30 * time.Second})
	if err != nil || asked.State != protocol.StateDelivered {
		t.Fatalf("question: %+v %v", asked, err)
	}

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

	db, err := sql.Open("sqlite", filepath.Join(bobHome, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sent []string
	rows, err := db.Query(`SELECT body, coalesce(reply_to,''), coalesce(status,'') FROM outbox WHERE recipient=? `, alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body, replyTo, status string
		if err := rows.Scan(&body, &replyTo, &status); err != nil {
			t.Fatal(err)
		}
		if replyTo != "" && replyTo != asked.ID {
			t.Errorf("%q went to %s", body, replyTo)
		}
		sent = append(sent, body+"/"+status)
	}
	slices.Sort(sent)
	if strings.Join(sent, " | ") != "halfway/progress | outside a run/ | which branch?/" {
		t.Fatalf("bob sent %q", sent)
	}
}

// In a selected reply receiver's run, ask, task, dm send and dm ask-agent
// carry on the local user's delegated work, but only on the run's own
// binding: choosing any other receiver is refused.
func TestRunGuardKeepsAReceiverRunOnItsBinding(t *testing.T) {
	a, _ := diagnosticAgent(t)
	binding := protocol.NewID()
	runEnv(t, "1", "", "", binding)
	for _, args := range [][]string{
		{"ask", "peer/desk", "follow-up"},
		{"task", "peer/desk", "follow-up"},
		{"dm", "send", "--question", protocol.NewID(), "follow-up"},
		{"dm", "ask-agent", protocol.NewID(), "follow-up"},
	} {
		if err := runGuard(args[0], args[1:]); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"send", "peer/desk", "hello"}, {"reply", protocol.NewID(), "done"}, {"accept", protocol.NewID()}, {"dm", "invite", protocol.NewID(), "peer/desk"}} {
		var err error
		if args[0] == "send" {
			err = sendGuard("")
		} else {
			err = runGuard(args[0], args[1:])
		}
		if err == nil || !strings.Contains(err.Error(), refusedText) || !strings.Contains(err.Error(), "keeps your reply binding") {
			t.Errorf("%v: %v", args, err)
		}
	}
	selection := func(args ...string) error {
		fs := flag.NewFlagSet("ask", flag.ContinueOnError)
		f := receiverFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		_, err := f.selected(a)
		return err
	}
	for _, args := range [][]string{
		{"--reply-receiver", "human"},
		{"--reply-receiver", "session:other"},
		{"--reply-binding", protocol.NewID()},
	} {
		if err := selection(args...); err == nil || !strings.Contains(err.Error(), "choosing another reply receiver "+refusedText) {
			t.Errorf("%v: %v", args, err)
		}
	}
	// The run's own binding goes on to its usual checks (this one is unknown).
	for _, args := range [][]string{nil, {"--reply-binding", binding}} {
		if err := selection(args...); err == nil || strings.Contains(err.Error(), refusedText) {
			t.Errorf("%v: %v", args, err)
		}
	}
}
