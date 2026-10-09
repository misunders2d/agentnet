package main

import (
	"bytes"
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

const refusedText = "is outside this delegated reply binding"

// runEnv sets the environment the worker gives a harness run (worker.go).
func runEnv(t *testing.T, background, request, requester, binding string) {
	t.Helper()
	t.Setenv(client.BackgroundEnv, background)
	t.Setenv(client.ProgressRequestEnv, request)
	t.Setenv(client.ProgressPeerEnv, requester)
	t.Setenv(replyBindingEnv, binding)
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
		{"--reply-to", asked.ID, bob.Address, "wrong recipient"},
	} {
		err := send(args...)
		if err == nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := send(alice.Address, "native permitted message"); err != nil {
		t.Fatal(err)
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
	if strings.Join(sent, " | ") != "halfway/progress/delivered | native permitted message//delivered | outside a run//delivered | which branch?//delivered" {
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
		{"do", protocol.NewID()},
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

func TestBackgroundRunSendsExactDMAttachment(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	runEnv(t, "", "", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hub := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, _ := guardPeer(t, ctx, testhub.BootstrapCode(t, hub), "alice")
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, home := guardPeer(t, ctx, code, "desk")
	for label, a := range map[string]*client.Agent{"Alice": alice, "Bob": bob} {
		if _, err = a.CreatePerson(ctx, label); err != nil {
			t.Fatal(err)
		}
	}
	var conv string
	eventually(t, "verified DM", func() bool { conv, err = alice.CreateDM(ctx, bob.Address); return err == nil })
	original, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "return the translated document", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "exact request on Bob", func() bool {
		rows, e := bob.ConversationMessages(conv)
		return e == nil && slices.ContainsFunc(rows, func(m client.ConvMessage) bool { return m.LID == original.LID })
	})
	file := filepath.Join(t.TempDir(), "translated.md")
	payload := []byte("# Completed translation\n")
	if err = os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	runEnv(t, "1", original.ID, alice.Address, "")
	cli := func(ref string) error {
		_, e := diagnosticOutput(t, func() error {
			return run([]string{"--home", home, "dm", "send", "--reply-to", ref, "--file", file, conv, "translated document"})
		})
		return e
	}
	if err = cli(protocol.NewID()); err == nil {
		t.Fatal("accepted unknown reply reference")
	}
	if err = cli(original.LID); err != nil {
		t.Fatal(err)
	}
	var got client.ConvMessage
	eventually(t, "exact attachment reaches requester", func() bool {
		rows, e := alice.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range rows {
			if m.Body == "translated document" {
				got = m
				return true
			}
		}
		return false
	})
	if got.Topic == "" || got.ReplyTo != original.LID || got.Quote != original.LID || len(got.Attachments) != 1 || got.Attachments[0].Name != "translated.md" {
		t.Fatalf("wrong delivered attachment: %+v", got)
	}
	paths, err := alice.Download(ctx, got.ID, t.TempDir(), false)
	if err != nil || len(paths) != 1 {
		t.Fatalf("download: %v %v", paths, err)
	}
	actual, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatalf("bytes: %q %v", actual, err)
	}
	rows, err := bob.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range rows {
		if m.Body == "translated document" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("attachment sent %d times", n)
	}
}
