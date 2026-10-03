package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// liveGuestWorld: alice and bob with persons and a DM between them, and
// carol, a person on the same server who can be invited into it as a guest.
// Every daemon runs.
func liveGuestWorld(t *testing.T) (alice, bob, carol *client.Agent, conv string, eventually func(string, func() bool)) {
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
	for _, a := range []*client.Agent{alice, bob, carol} {
		runDaemon(t, a)
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
	return alice, bob, carol, conv, eventually
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

// BUG-18: a guest invited back after leaving can send. The page acts on the
// guest's active participation, not on the first (ended) one it holds.
func TestLiveGuestInvitedBackCanSend(t *testing.T) {
	alice, _, carol, conv, eventually := liveGuestWorld(t)
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
