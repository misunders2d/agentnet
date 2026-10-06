package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// liveGuestWorld: alice and bob with persons and a DM between them, and
// carol, a person on the same server who can be invited into it as a guest.
// Every daemon runs; stop stops one (runDaemon starts it again).
func liveGuestWorld(t *testing.T) (alice, bob, carol *client.Agent, conv string, eventually func(string, func() bool), stop map[*client.Agent]func()) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	join := func(code, name string) *client.Agent {
		a, err := client.Join(ctx, filepath.Join(t.TempDir(), name), code, name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	alice = join(testhub.BootstrapCode(t, dir), "laptop")
	for _, label := range []string{"bob", "carol"} {
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		if label == "bob" {
			bob = join(code, "desk")
		} else {
			carol = join(code, "box")
		}
	}
	stop = map[*client.Agent]func(){}
	for _, a := range []*client.Agent{alice, bob, carol} {
		stop[a] = runDaemon(t, a)
	}
	for _, p := range []struct {
		a     *client.Agent
		label string
	}{{alice, "Alice"}, {bob, "Bob"}, {carol, "Carol"}} {
		if _, err := p.a.CreatePerson(ctx, p.label); err != nil {
			t.Fatal(err)
		}
	}
	eventually = func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	eventually("a DM between alice and bob", func() bool {
		var err error
		conv, err = alice.CreateDM(ctx, bob.Address)
		return err == nil
	})
	return alice, bob, carol, conv, eventually, stop
}

// inviteGuest has alice invite carol into conv and carol accept; both see
// the participation active.
func inviteGuest(t *testing.T, alice, carol *client.Agent, conv string, eventually func(string, func() bool)) client.ParticipationInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var p client.ParticipationInfo
	eventually("carol invited as a guest", func() bool {
		var err error
		p, err = alice.InviteHuman(ctx, conv, carol.Address, nil, "")
		return err == nil
	})
	eventually("the invitation at carol", func() bool {
		got, err := carol.Participation(p.PID)
		return err == nil && got.State == client.PartInvited && got.Held == 0
	})
	if _, err := carol.AcceptParticipation(ctx, p.PID); err != nil {
		t.Fatal(err)
	}
	eventually("alice holds carol's acceptance", func() bool {
		got, err := alice.Participation(p.PID)
		return err == nil && got.HumanActive()
	})
	return p
}

// guestOf is pid's guest view in conv on live's DM.
func guestOf(t *testing.T, live *Live, conv, pid string) (DMThread, GuestView) {
	t.Helper()
	d, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range d.Guests {
		if g.PID == pid {
			return d, g
		}
	}
	t.Fatalf("no guest %s in %+v", pid, d.Guests)
	return d, GuestView{}
}

// BUG-40a: "audience pending" ends. A guest who left applied that end
// first, so nothing new can reach them: nobody shows it pending. When a
// member ends an accepted guest, the device that ended it shows it pending
// until every device in the DM has stored the end: one that has not (bob's,
// offline here) may still send to the guest, even once the guest's own
// device holds it.
func TestLiveGuestEndSettlesAudience(t *testing.T) {
	alice, bob, carol, conv, eventually, stop := liveGuestWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lives := map[string]*Live{"alice": NewLive(alice), "bob": NewLive(bob), "carol": NewLive(carol)}
	settled := func(what, pid, text string) {
		t.Helper()
		for name, live := range lives {
			eventually(what+" settled for "+name, func() bool {
				live.Refresh(conv) // as an open DM does: receipts of copies still held
				d, g := guestOf(t, live, conv, pid)
				return g.State == client.PartDismissed && !g.AudiencePending && !d.AudiencePending && strings.Contains(g.StateText, text)
			})
		}
	}
	left := inviteGuest(t, alice, carol, conv, eventually)
	if _, err := carol.DismissParticipation(ctx, left.PID); err != nil {
		t.Fatal(err)
	}
	eventually("bob holds carol's leaving", func() bool {
		got, err := bob.Participation(left.PID)
		return err == nil && got.State == client.PartDismissed
	})
	settled("carol's leaving", left.PID, "Left")

	removed := inviteGuest(t, alice, carol, conv, eventually)
	eventually("bob holds carol's acceptance", func() bool {
		got, err := bob.Participation(removed.PID)
		return err == nil && got.HumanActive()
	})
	stop[bob]() // bob's device will not store the end until it is back
	ended, err := lives["alice"].ChangeHuman(ctx, GuestAction{Action: "end", PID: removed.PID})
	if err != nil {
		t.Fatal(err)
	}
	if !ended.AudiencePending {
		t.Fatalf("just ended, before the other devices have it: %+v", ended)
	}
	eventually("carol holds her removal", func() bool {
		got, err := carol.Participation(removed.PID)
		return err == nil && got.State == client.PartDismissed
	})
	eventually("alice holds the receipt of carol's copy of the end", func() bool {
		lives["alice"].Refresh(conv)
		msgs, err := alice.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if ev, err := protocol.ParseParticipationEvent([]byte(m.Body)); err != nil || m.PID != removed.PID || m.Dir != "out" || ev.Type != protocol.EventDismiss {
				continue
			}
			for _, c := range m.Copies {
				if c.To == carol.Address && c.State == protocol.StateDelivered {
					return true
				}
			}
		}
		return false
	})
	if _, g := guestOf(t, lives["alice"], conv, removed.PID); !g.AudiencePending {
		t.Fatalf("pending cleared while bob's device has not stored the end: %+v", g)
	}
	for _, text := range []string{ended.StateText, guestOfText(t, lives["alice"], conv, removed.PID)} {
		if !strings.Contains(text, "Until every device in this conversation has stored the end") {
			t.Fatalf("pending end worded as if only the guest's device mattered: %q", text)
		}
	}
	runDaemon(t, bob)
	settled("carol's removal", removed.PID, "Ended")
}

func guestOfText(t *testing.T, live *Live, conv, pid string) string {
	t.Helper()
	_, g := guestOf(t, live, conv, pid)
	return g.StateText
}

// BUG-18: a guest invited back after leaving can send. The page acts on the
// guest's active participation, not on the first (ended) one it holds.
func TestLiveGuestInvitedBackCanSend(t *testing.T) {
	alice, _, carol, conv, eventually, _ := liveGuestWorld(t)
	live := NewLive(carol)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for visit := 1; visit <= 2; visit++ {
		p := inviteGuest(t, alice, carol, conv, eventually)
		d, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		if d.Role != "human_guest" || d.Frozen != "" {
			t.Fatalf("visit %d: guest's DM role %q frozen %q", visit, d.Role, d.Frozen)
		}
		o, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range o.DMs {
			if s.ID == conv && (s.Role != "human_guest" || s.Frozen != "") {
				t.Fatalf("visit %d: guest's DM summary role %q frozen %q", visit, s.Role, s.Frozen)
			}
		}
		if _, err := carol.DismissParticipation(ctx, p.PID); err != nil {
			t.Fatal(err)
		}
		eventually("alice holds carol's leaving", func() bool {
			got, err := alice.Participation(p.PID)
			return err == nil && got.State == client.PartDismissed
		})
		if d, err = live.DM(conv); err != nil || d.Frozen == "" {
			t.Fatalf("visit %d: after leaving, sending stays open: %q %v", visit, d.Frozen, err)
		}
	}
}

// BUG-40b: the timeline names an outside host whose person is known here
// (pinned) instead of calling them "an unknown person" or "someone not in
// this DM", and marks that name: the label is the person's own claim.
func TestLiveTimelineNamesKnownOutsideHost(t *testing.T) {
	alice, bob, carol, conv, eventually, _ := liveGuestWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	eventually("bob's DM with carol (carol pinned at bob)", func() bool {
		_, err := bob.CreateDM(ctx, carol.Address)
		return err == nil
	})
	var p client.ParticipationInfo
	eventually("alice invites carol's agent", func() bool {
		var err error
		p, err = alice.InviteAgent(ctx, conv, carol.Address, nil, nil, "")
		return err == nil
	})
	eventually("the invitation at carol", func() bool {
		got, err := carol.Participation(p.PID)
		return err == nil && got.State == client.PartInvited && got.Held == 0
	})
	if _, err := carol.AcceptParticipation(ctx, p.PID); err != nil {
		t.Fatal(err)
	}
	live := NewLive(bob)
	var lines string
	eventually("bob's timeline shows the acceptance", func() bool {
		d, err := live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		lines = eventLines(d.Messages)
		return strings.Contains(lines, "accepted")
	})
	for _, want := range []string{"Alice invited Carol (not in this DM)'s agent (on " + carol.Address + ") into this DM.", "Carol (not in this DM) accepted: the agent joins this DM."} {
		if !strings.Contains(lines, want) {
			t.Fatalf("timeline %q lacks %q", lines, want)
		}
	}
}
