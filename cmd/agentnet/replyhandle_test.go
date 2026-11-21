package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// `agentnet send --reply-handle H --from-agent ID [--client-id KEY] TEXT`
// takes exactly TEXT, refuses the flags that belong to other sends, and a
// handle this device did not give; --client-id needs --reply-handle.
func TestSendReplyHandleFlags(t *testing.T) {
	a, _ := diagnosticAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.CreatePerson(ctx, "Owner"); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"address and text": {"--reply-handle", "rh-x", "--from-agent", "a", "bob/desk", "text"},
		"with --reply-to":  {"--reply-handle", "rh-x", "--from-agent", "a", "--reply-to", "x", "text"},
		"with --progress":  {"--reply-handle", "rh-x", "--from-agent", "a", "--progress", "text"},
	} {
		if err := runSend(ctx, a, args, false); err == nil || !strings.HasPrefix(err.Error(), "usage: send --reply-handle") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := runSend(ctx, a, []string{"--client-id", "k", "bob/desk", "text"}, false); err == nil || !strings.Contains(err.Error(), "--client-id requires --reply-handle") {
		t.Fatalf("client id alone: %v", err)
	}
	if err := runSend(ctx, a, []string{"--reply-handle", "rh-0123456789abcdef0123456789abcdef", "--from-agent", "a", "--client-id", "k", "text"}, false); !errors.Is(err, client.ErrReplyHandle) {
		t.Fatalf("a handle this device did not give: %v", err)
	}
}
