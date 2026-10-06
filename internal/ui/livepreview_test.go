package ui

import (
	"context"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestLiveConversationEditedPreviews(t *testing.T) {
	alice, bob, _, dm, _, _ := liveGuestWorld(t)
	group := liveTwoMemberGroup(t, alice, bob)
	live := NewLive(alice)
	for _, conv := range []string{dm, group} {
		if _, err := alice.SendConv(context.Background(), conv, client.ConvOutgoing{Body: "unfinished sentence"}); err != nil {
			t.Fatal(err)
		}
		thread, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		id := thread.Messages[len(thread.Messages)-1].ID
		assertPreview := func(want string) {
			t.Helper()
			o, err := live.Overview()
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range o.DMs {
				if d.ID == conv {
					if d.Last != want {
						t.Fatalf("preview %q, want %q", d.Last, want)
					}
					return
				}
			}
			t.Fatal("conversation missing")
		}
		assertPreview("unfinished sentence")
		if _, err := live.EditMessage(ControlAction{Conv: conv, ID: id, Dir: "out", Text: "finished sentence"}); err != nil {
			t.Fatal(err)
		}
		assertPreview("finished sentence")
		if _, err := live.DeleteMessage(ControlAction{Conv: conv, ID: id, Dir: "out"}); err != nil {
			t.Fatal(err)
		}
		assertPreview("Message deleted")
	}
}
