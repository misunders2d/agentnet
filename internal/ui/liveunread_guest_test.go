package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// MEL-545: the badge counts the timeline's unread rows, not hidden copies
// of guest participation records. Reading every shown row clears it.
func TestLiveGuestReadClearsChatUnread(t *testing.T) {
	alice, bob, carol, conv, eventually, _ := liveGuestWorld(t)
	inviteGuest(t, alice, carol, conv, eventually)
	lives := map[string]*Live{"alice": NewLive(alice), "bob": NewLive(bob), "carol": NewLive(carol)}
	for name, a := range map[string]*client.Agent{"alice": alice, "bob": bob, "carol": carol} {
		eventually("active guest at "+name, func() bool {
			parts, err := a.Participations(conv)
			return err == nil && len(parts) == 1 && parts[0].HumanActive()
		})
	}
	for _, turn := range []struct {
		who, body string
	}{{"carol", "Guest message"}, {"bob", "Owner reply"}} {
		if _, err := lives[turn.who].SendDM(DMDraft{Conv: conv, Body: turn.body}); err != nil {
			t.Fatal(err)
		}
		for name, live := range lives {
			eventually(turn.body+" at "+name, func() bool {
				d, err := live.DM(conv)
				if err != nil {
					return false
				}
				for _, m := range d.Messages {
					if m.Body == turn.body {
						return true
					}
				}
				return false
			})
		}
	}
	for name, live := range lives {
		d, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range d.Messages {
			if m.Dir == "out" && m.Unread {
				t.Errorf("%s: own row %s is unread", name, m.ID)
			}
			if m.Unread {
				ids = append(ids, m.ID)
			}
		}
		if _, err := live.Act(Action{Do: DoRead, IDs: ids}); err != nil {
			t.Fatal(err)
		}
		o, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, dm := range o.DMs {
			if dm.ID == conv {
				found = true
				if dm.Unread != 0 {
					t.Errorf("%s: read chat still has %d unread", name, dm.Unread)
				}
			}
		}
		if !found {
			t.Fatalf("%s: missing DM", name)
		}
	}
	// Clearing a read chat must not suppress a later unseen guest turn.
	if _, err := lives["carol"].SendDM(DMDraft{Conv: conv, Body: "Next guest message"}); err != nil {
		t.Fatal(err)
	}
	for name, live := range lives {
		eventually("next guest message at "+name, func() bool {
			d, err := live.DM(conv)
			if err != nil {
				return false
			}
			for _, m := range d.Messages {
				if m.Body == "Next guest message" {
					return true
				}
			}
			return false
		})
		o, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if name == "carol" {
			want = 0 // the guest's own outgoing turn
		}
		for _, dm := range o.DMs {
			if dm.ID == conv && dm.Unread != want {
				t.Errorf("%s: next guest turn has %d unread, want %d", name, dm.Unread, want)
			}
		}
	}
}
