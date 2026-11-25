package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// MEL-713: an agent's terminal (Claude Code, Codex, or Pi/OMP by its reply
// session) or a run serving a request goes to the My agents room, as that
// agent. A person at a plain shell sends in a DM and asks in a chat
// (TestPlainSendGoesToTheDM); an agent terminal with no registered agent is
// refused, never shown as the person.
func TestRoomAskFromChoosesAgentTerminals(t *testing.T) {
	for _, k := range []string{"AGENTNET_BACKGROUND", "AGENTNET_REPLY_BINDING", client.ClaudeSessionEnv, "CODEX_THREAD_ID", client.AgentRequestEnv, client.RoomRequestEnv, client.ProgressRequestEnv, "AGENTNET_REPLY_SESSION", "AGENTNET_REPLY_SESSION_HOME"} {
		t.Setenv(k, "")
	}
	a, home := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.CreatePerson(ctx, "Owner"); err != nil {
		t.Fatal(err)
	}
	code, err := a.Invite(ctx, "peer", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })

	if _, room, err := roomAskFrom(a, a.Address, ""); room || err != nil {
		t.Fatalf("plain shell: room=%v %v", room, err)
	}
	// A Pi or OMP session names itself by its reply session registered in
	// this home; one inherited from another home names nothing here.
	file := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(file, []byte(`{"type":"session","id":"s1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := a.RegisterReplySession(client.ReplySessionRegistration{Harness: "pi", SessionID: "s1", File: file})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_REPLY_SESSION", owner.Handle)
	t.Setenv("AGENTNET_REPLY_SESSION_HOME", t.TempDir())
	if _, room, err := roomAskFrom(a, a.Address, ""); room || err != nil {
		t.Fatalf("Pi session of another home: room=%v %v", room, err)
	}
	t.Setenv("AGENTNET_REPLY_SESSION_HOME", home)
	if _, room, err := roomAskFrom(a, a.Address, ""); !room || !errors.Is(err, client.ErrNoSessionAgent) {
		t.Fatalf("Pi session without a Pi agent here must be refused as that agent: room=%v %v", room, err)
	}
	t.Setenv("AGENTNET_REPLY_SESSION", "")
	t.Setenv(client.ClaudeSessionEnv, "synthetic-claude-session")
	// Another person's agent visits the room (owner, October 29, 2026).
	if _, room, err := roomAskFrom(a, peer.Address, ""); !room || !errors.Is(err, client.ErrNoSessionAgent) {
		t.Fatalf("another person's device from an unregistered agent terminal: room=%v %v", room, err)
	}
	if _, room, err := roomAskFrom(a, a.Address, ""); !room || !errors.Is(err, client.ErrNoSessionAgent) {
		t.Fatalf("own device from an unregistered agent terminal must be refused: room=%v %v", room, err)
	}
	t.Setenv("AGENTNET_BACKGROUND", "1")
	if _, room, err := roomAskFrom(a, a.Address, ""); room || err != nil {
		t.Fatalf("background run without its request: room=%v %v", room, err)
	}
	// A run serving a request asks through the room (chain rule checked in
	// client.RunAgentRoomAsk); a delegated reply binding keeps its frozen routing.
	t.Setenv(client.AgentRequestEnv, "11111111111111111111111111111111")
	if via, room, err := roomAskFrom(a, a.Address, ""); !room || err != nil || via.cause == "" {
		t.Fatalf("run to an own device: %+v room=%v %v", via, room, err)
	}
	if via, room, err := roomAskFrom(a, peer.Address, ""); !room || err != nil || via.cause == "" {
		t.Fatalf("run to another person's device: %+v room=%v %v", via, room, err)
	}
	t.Setenv("AGENTNET_REPLY_BINDING", "binding")
	if _, room, err := roomAskFrom(a, a.Address, ""); room || err != nil {
		t.Fatalf("reply-binding run: room=%v %v", room, err)
	}
}

func TestSendToExactAgentOnOwnDevice(t *testing.T) {
	for _, key := range []string{client.BackgroundEnv, client.CodexThreadEnv, "AGENTNET_REPLY_BINDING", "AGENTNET_REPLY_SESSION"} {
		t.Setenv(key, "")
	}
	t.Setenv(client.ClaudeSessionEnv, "synthetic-claude-session")
	t.Setenv("AGENTNET_NOTIFY", "off")
	a, _ := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := a.CreatePerson(ctx, "Owner"); err != nil {
		t.Fatal(err)
	}
	start := func(device *client.Agent) {
		run, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := device.Run(run, client.RunOptions{}); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s daemon: %v", device.Address, err)
			}
		}()
		t.Cleanup(func() { stop(); <-done })
	}
	waitFor := func(what string, ready func() bool) {
		t.Helper()
		for !ready() {
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v", what, ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	start(a)
	link, err := a.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.JoinAndLink(ctx, t.TempDir(), link.Code, "second-laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	var linkID string
	waitFor("the link request", func() bool {
		links, _ := a.PendingLinks()
		for _, req := range links {
			if req.State == client.LinkPending && req.Address == b.Address {
				linkID = req.ID
				return true
			}
		}
		return false
	})
	if err := a.DecideLink(ctx, linkID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AwaitLink(ctx); err != nil {
		t.Fatal(err)
	}
	start(b)
	work := t.TempDir()
	create := func(device *client.Agent, harness string) protocol.AgentRecord {
		t.Helper()
		agent, err := device.CreateLocalAgent(harness, client.Responder{Harness: harness, Dir: work})
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	sender := create(a, "claude")
	target := create(b, "pi")
	create(b, "codex")
	for _, device := range []*client.Agent{a, b} {
		if err := device.PublishAgentCatalog(ctx); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("both advertised recipient agents", func() bool {
		catalog, err := a.AgentCatalog(ctx, b.Address)
		return err == nil && len(catalog) == 2
	})
	if err := runSend(ctx, a, []string{b.Address, "ambiguous update"}, false); err == nil || !strings.Contains(err.Error(), "choose one with --agent ID") {
		t.Fatalf("ambiguous send: %v", err)
	}
	if err := runSend(ctx, a, []string{"--agent", sender.ID, b.Address, "wrong host's agent"}, false); !errors.Is(err, client.ErrUnknownAgent) {
		t.Fatalf("send to an agent from a different device: %v", err)
	}
	room, err := a.MyAgentsRoom(ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string) string {
		t.Helper()
		out, err := diagnosticOutput(t, func() error {
			return runSend(ctx, a, []string{"--agent", target.ID, b.Address, body}, false)
		})
		if err != nil || !strings.Contains(out, `sent in "My agents"`) {
			t.Fatalf("selected send: %q, %v", out, err)
		}
		var delivered client.ConvMessage
		waitFor("the plain update on the other device", func() bool {
			messages, _ := b.ConversationMessages(room)
			for _, msg := range messages {
				if msg.Body == body && msg.From == a.Address && !msg.History {
					delivered = msg
					return true
				}
			}
			return false
		})
		if delivered.Kind != envelope.KindMessage || delivered.Job != "" || delivered.Human == nil {
			t.Fatalf("update did not arrive as an ordinary agent message: %+v", delivered)
		}
		author, err := b.Participation(delivered.Human.AuthorPID)
		if err != nil || author.AgentID != sender.ID {
			t.Fatalf("unexpected sender: %+v, %v", author, err)
		}
		participants, err := b.Participations(room)
		if err != nil {
			t.Fatal(err)
		}
		var recipient string
		for _, p := range participants {
			if !p.Claimable() || p.Topic == nil || *p.Topic != delivered.Topic || p.Host.Address != b.Address {
				continue
			}
			if p.AgentID != target.ID || recipient != "" {
				t.Fatalf("unexpected recipient: %+v", p)
			}
			recipient = p.PID
		}
		if recipient == "" {
			t.Fatal("named recipient did not join the topic")
		}
		return recipient
	}
	old := send("build is green")
	if _, err := a.DismissParticipation(ctx, old); err != nil {
		t.Fatal(err)
	}
	if send("build is uploaded") == old {
		t.Fatal("explicit send reused a dismissed participation")
	}
}
