package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
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
