package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// MEL-713 step 1: a person's plain shell starts no old device thread. Its
// send goes in the newest DM with that person, again and again; its ask or
// task is refused with the chat commands to use; a send to one of its own
// devices names the room.
func TestPlainSendGoesToTheDM(t *testing.T) {
	for _, k := range []string{client.ClaudeSessionEnv, client.CodexThreadEnv, "AGENTNET_REPLY_SESSION", client.BackgroundEnv} {
		t.Setenv(k, "")
	}
	alice, bob, _, conv, eventually := dmWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, body := range []string{"hello from the shell", "and again"} {
		var out, note bytes.Buffer
		if err := sendPlain(ctx, alice, bob.Address, body, "", "", nil, 5*time.Second, &out, &note); err != nil {
			t.Fatal(err)
		}
		if f := strings.Fields(out.String()); len(f) != 3 || f[1] != "delivered" || f[2] != "relay" || !strings.Contains(note.String(), "sent in your DM "+conv) {
			t.Fatalf("%q did not go in the DM %s: %s / %s", body, conv, out.String(), note.String())
		}
		eventually("bob holds "+body, func() bool {
			msgs, err := bob.ConversationMessages(conv)
			if err != nil {
				return false
			}
			for _, m := range msgs {
				if m.Body == body {
					return true
				}
			}
			return false
		})
	}
	convs, err := alice.Conversations()
	if err != nil || len(convs) != 1 {
		t.Fatalf("plain sends opened another conversation: %+v %v", convs, err)
	}
	if err := runSendKind(ctx, alice, "ask", []string{bob.Address, "which branch?"}); err == nil || err.Error() != plainShellAsk {
		t.Fatalf("plain-shell ask: %v", err)
	}
	if err := sendPlain(ctx, alice, alice.Address, "to myself", "", "", nil, 0, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "My agents room") {
		t.Fatalf("plain-shell send to an own device: %v", err)
	}
}
