package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// dmWorld: alice (laptop), bob (desk) and carol (box) on one server, each
// with a person and a running daemon; a DM between alice and bob.
func dmWorld(t *testing.T) (alice, bob, carol *client.Agent, conv string, eventually func(string, func() bool)) {
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
		run, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(run, client.RunOptions{}); close(done) }()
		t.Cleanup(func() { stop(); <-done; a.Close() })
		return a
	}
	alice = join(testhub.BootstrapCode(t, dir), "laptop")
	invite := func(label string) string {
		code, err := alice.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	bob, carol = join(invite("bob"), "desk"), join(invite("carol"), "box")
	for label, a := range map[string]*client.Agent{"Alice": alice, "Bob": bob, "Carol": carol} {
		if _, err := a.CreatePerson(ctx, label); err != nil {
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

// BUG-20: accepting an agent's invitation is the standing task grant for the
// member keys it names. The CLI shows those keys before and on accepting,
// and approvals lists the grant while it stands.
func TestDMAgentTaskGrantShown(t *testing.T) {
	alice, bob, _, conv, eventually := dmWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fp := alice.Self().Fingerprint()
	var p client.ParticipationInfo
	eventually("alice invites bob's agent with tasks from her key", func() bool {
		var err error
		p, err = alice.InviteAgent(ctx, conv, bob.Address, nil, []string{fp}, "")
		return err == nil
	})
	eventually("the invitation at bob", func() bool {
		got, err := bob.Participation(p.PID)
		return err == nil && got.State == client.PartInvited && got.Held == 0
	})
	var out bytes.Buffer
	if err := runDM(ctx, bob, []string{"agents", conv}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{p.PID, "tasks from", `"Alice"`, alice.Address, fp} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dm agents before accepting lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := runDM(ctx, bob, []string{"accept-agent", p.PID}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{p.PID + " active", "without asking", alice.Address, "dismiss-agent " + p.PID} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("accept-agent lacks %q:\n%s", want, out.String())
		}
	}
	listed, err := diagnosticOutput(t, func() error { return runApprovals(bob) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tasks", alice.Address, fp, p.PID} {
		if !strings.Contains(listed, want) {
			t.Fatalf("approvals lacks %q:\n%s", want, listed)
		}
	}
	if _, err := bob.DismissParticipation(ctx, p.PID); err != nil {
		t.Fatal(err)
	}
	if listed, _ = diagnosticOutput(t, func() error { return runApprovals(bob) }); strings.Contains(listed, p.PID) {
		t.Fatalf("approvals still lists a dismissed agent's grant:\n%s", listed)
	}
}

// BUG-40e: a person on the CLI invites a guest into a DM, the guest accepts
// and sends there with dm send, as on the page; dm agents calls a guest a
// guest.
func TestDMGuestFromTheCLI(t *testing.T) {
	alice, bob, carol, conv, eventually := dmWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var out bytes.Buffer
	eventually("alice invites carol as a guest", func() bool {
		out.Reset()
		return runDM(ctx, alice, []string{"invite-guest", conv, carol.Address}, &out) == nil
	})
	fields := strings.Fields(out.String())
	if len(fields) < 2 || fields[1] != client.PartInvited {
		t.Fatalf("invite-guest: %q", out.String())
	}
	pid := fields[0]
	eventually("the invitation at carol", func() bool {
		got, err := carol.Participation(pid)
		return err == nil && got.State == client.PartInvited && got.Held == 0
	})
	out.Reset()
	if err := runDM(ctx, carol, []string{"accept-guest", pid}, &out); err != nil || !strings.HasPrefix(out.String(), pid+" active") {
		t.Fatalf("accept-guest: %q %v", out.String(), err)
	}
	eventually("alice holds carol's acceptance", func() bool {
		got, err := alice.Participation(pid)
		return err == nil && got.HumanActive()
	})
	out.Reset()
	if err := runDM(ctx, carol, []string{"send", conv, "hello from the guest's CLI"}, &out); err != nil {
		t.Fatalf("guest dm send: %v", err)
	}
	for _, a := range []*client.Agent{alice, bob} {
		eventually("the guest's message at "+a.Address, func() bool {
			msgs, _ := a.ConversationMessages(conv)
			for _, m := range msgs {
				if m.Body == "hello from the guest's CLI" {
					return true
				}
			}
			return false
		})
	}
	out.Reset()
	if err := runDM(ctx, alice, []string{"agents", conv}, &out); err != nil || !strings.Contains(out.String(), "guest on "+carol.Address) || strings.Contains(out.String(), "agent on "+carol.Address) {
		t.Fatalf("dm agents for a guest: %q %v", out.String(), err)
	}
	if err := runDM(ctx, alice, []string{"accept-guest", pid}, &out); err == nil {
		t.Fatal("a member decided a guest's invitation for them")
	}
}
