package main

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A send the Hub holds for a suspended recipient is said to be held by the
// Hub, never queued for retry here (F4: receipts say only what is proven);
// a send still queued here keeps its retry note.
func TestResultNoteSaysWhoHoldsIt(t *testing.T) {
	held := resultNote(client.SendResult{ID: "m1", State: protocol.StateCustody, Path: protocol.PathRelay, Detail: client.SuspendedText("bob/desk")}, 0)
	if !strings.HasPrefix(held, "held by the Hub: ") || !strings.Contains(held, "bob/desk is suspended until it updates AgentNet") || strings.Contains(held, "retry") {
		t.Fatalf("custody for a suspended recipient: %q", held)
	}
	if queued := resultNote(client.SendResult{ID: "m2", State: "queued", Detail: "hub unreachable"}, 0); queued != "queued for retry by the daemon: hub unreachable" {
		t.Fatalf("queued: %q", queued)
	}
	if plain := resultNote(client.SendResult{ID: "m3", State: protocol.StateCustody, Path: protocol.PathRelay}, 0); plain != "" {
		t.Fatalf("plain custody: %q", plain)
	}
}
