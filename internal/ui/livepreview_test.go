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

func TestLivePreviewTargetsExactTopicRow(t *testing.T) {
	alice, bob, _, dm, _, _ := liveGuestWorld(t)
	group := liveTwoMemberGroup(t, alice, bob)
	live := NewLive(alice)
	for _, conv := range []string{dm, group} {
		if _, err := alice.SendConv(context.Background(), conv, client.ConvOutgoing{Body: "Main flow stays separate"}); err != nil {
			t.Fatal(err)
		}
		sent, err := alice.SendConv(context.Background(), conv, client.ConvOutgoing{Body: "Preview inside a topic", Topic: "new"})
		if err != nil {
			t.Fatal(err)
		}
		view, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		var target DMMessage
		for _, m := range view.Messages {
			if m.LID == sent.LID {
				target = m
			}
		}
		if target.ID == "" || target.Topic == "" {
			t.Fatal("topic row absent")
		}
		check := func(want string) {
			t.Helper()
			o, e := live.Overview()
			if e != nil {
				t.Fatal(e)
			}
			for _, d := range o.DMs {
				if d.ID == conv {
					if d.LastID != sent.LID || d.Last != want {
						t.Fatalf("preview target=%q text=%q, want logical=%q text=%q", d.LastID, d.Last, sent.LID, want)
					}
					return
				}
			}
			t.Fatal("preview absent")
		}
		check("Preview inside a topic")
		if _, err = live.EditMessage(ControlAction{Conv: conv, ID: target.ID, Dir: "out", Text: "Edited topic preview"}); err != nil {
			t.Fatal(err)
		}
		check("Edited topic preview")
		if _, err = live.DeleteMessage(ControlAction{Conv: conv, ID: target.ID, Dir: "out"}); err != nil {
			t.Fatal(err)
		}
		check("Message deleted")
	}
}

func TestPreviewLogicalReferenceCollisions(t *testing.T) {
	last := client.ConvMessage{ID: "physical-last", LID: "logical-last"}
	if got := previewMessageID([]client.ConvMessage{last}, last); got != last.LID {
		t.Fatal("unambiguous logical reference lost", got)
	}
	for _, other := range []client.ConvMessage{{ID: "other", LID: last.LID}, {ID: last.LID, LID: "another"}} {
		if got := previewMessageID([]client.ConvMessage{other, last}, last); got != last.ID {
			t.Fatal("ambiguous logical reference exposed", got)
		}
	}
	last.LID = ""
	if got := previewMessageID([]client.ConvMessage{last}, last); got != last.ID {
		t.Fatal("legacy fallback lost", got)
	}
}
