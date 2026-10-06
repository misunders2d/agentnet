package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// dm show prints what people see now: an edited message's latest text,
// marked edited (never the text first sent), and a deleted message as
// deleted, without its files.
func TestDMShowPrintsEditsAndDeletions(t *testing.T) {
	var out bytes.Buffer
	printConvMessages(&out, []client.ConvMessage{
		{ID: strings.Repeat("1", 32), LID: strings.Repeat("a", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 1,
			Body: "ship 400 boxes", Controls: client.Controls{Edited: true, Revision: 1, Text: "ship 450 boxes"}},
		{ID: strings.Repeat("2", 32), LID: strings.Repeat("b", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 2,
			Body: "salaries attached", Controls: client.Controls{Deleted: true},
			Attachments: []client.FileInfo{{Name: "salaries.csv", Size: 10}}},
		{ID: strings.Repeat("3", 32), LID: strings.Repeat("c", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 3,
			Body: "unchanged"},
	})
	got := out.String()
	for _, want := range []string{"\n  ship 450 boxes (edited)\n", "\n  (deleted)\n", "\n  unchanged\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dm show lacks %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"400 boxes", "salaries", "unchanged (edited)"} {
		if strings.Contains(got, stale) {
			t.Fatalf("dm show still prints %q:\n%s", stale, got)
		}
	}
}

// dm list names a group by its title and members, and a DM this person
// is not a member of (an assistant it hosts there, or a guest there, who
// can read and send) by both of its people, never as an empty "with", as
// a DM with one of them or as one it is "not in".
func TestDMListNamesGroupsAndHostedDMs(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local).Unix()
	for _, tc := range []struct {
		c         client.ConversationInfo
		want, not string
	}{
		{client.ConversationInfo{ID: "g1", Kind: protocol.ConvKindGroup, Title: "Freight desk", Created: at,
			Members: []client.PersonInfo{{Label: "Anna"}, {Label: "Sergey"}, {Label: "Vitalii"}}},
			`g1  group "Freight desk" with "Anna", "Sergey", "Vitalii"  since 2026-10-03`, `with "" (, )`},
		{client.ConversationInfo{ID: "d1", Kind: protocol.ConvKindDM, Role: "visitor", Created: at,
			Peer:    client.PersonInfo{Label: "Vitalii", Address: "vitalii/desk", State: "pinned"},
			Members: []client.PersonInfo{{Label: "Sergey"}, {Label: "Vitalii"}}},
			`d1  between "Sergey" and "Vitalii" (you are not a member)  since 2026-10-03`, `with "Vitalii"`},
		{client.ConversationInfo{ID: "d2", Kind: protocol.ConvKindDM, Created: at,
			Peer: client.PersonInfo{Label: "Vitalii", Address: "vitalii/desk", State: "pinned"}},
			`d2  with "Vitalii" (vitalii/desk, pinned)  since 2026-10-03`, "between"},
	} {
		if got := convLine(tc.c); got != tc.want || strings.Contains(got, tc.not) {
			t.Errorf("dm list line: %q, want %q", got, tc.want)
		}
	}
}

// dm show of a conversation not held here is an error, not an empty
// success.
func TestDMShowUnknownConversationFails(t *testing.T) {
	a, _ := diagnosticAgent(t)
	for _, id := range []string{strings.Repeat("0", 64), "nonsense"} {
		var out bytes.Buffer
		if err := runDM(context.Background(), a, []string{"show", id}, &out); !errors.Is(err, client.ErrNoConversation) {
			t.Errorf("dm show %s: %v, printed %q", id, err, out.String())
		}
	}
}

// help dm says what happens to an assistant's reply without an emotion
// line: it is sent, shown neutral (agentjob.go, MEL-434); only a reply the
// agent marks for a person's decision is held for review.
func TestDMHelpSaysReplyWithoutEmotionIsNeutral(t *testing.T) {
	var out bytes.Buffer
	if err := printHelp(&out, []string{"dm"}); err != nil {
		t.Fatal(err)
	}
	help := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(help, "without one, or when the agent says the person must decide, nothing is sent") || !strings.Contains(help, "shown neutral") {
		t.Fatalf("help dm misstates replies without an emotion line:\n%s", out.String())
	}
}

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
// and approvals lists the grant while it stands. It names how the grant ends
// only where that works: a host outside the DM cannot dismiss its agent's
// participation (only a member can), so its device is told exactly that.
func TestDMAgentTaskGrantShown(t *testing.T) {
	alice, bob, carol, conv, eventually := dmWorld(t)
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

	// carol, outside the DM, hosts the agent.
	var ext client.ParticipationInfo
	eventually("alice invites carol's agent with tasks from her key", func() bool {
		var err error
		ext, err = alice.InviteAgent(ctx, conv, carol.Address, nil, []string{fp}, "")
		return err == nil
	})
	eventually("the invitation at carol", func() bool {
		got, err := carol.Participation(ext.PID)
		return err == nil && got.State == client.PartInvited && got.Held == 0 && got.External
	})
	out.Reset()
	if err := runDM(ctx, carol, []string{"accept-agent", ext.PID}, &out); err != nil {
		t.Fatal(err)
	}
	theirs := "only a DM member can end it: they run agentnet dm dismiss-agent " + ext.PID + "; this device cannot"
	for _, want := range []string{ext.PID + " active", "without asking", fp, theirs} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("accept-agent at an outside host lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "dismiss-agent "+ext.PID+" ends") {
		t.Fatalf("accept-agent tells an outside host it can end the grant:\n%s", out.String())
	}
	listed, err = diagnosticOutput(t, func() error { return runApprovals(carol) })
	if err != nil || !strings.Contains(listed, ext.PID) || !strings.Contains(listed, theirs) || strings.Contains(listed, "dismiss-agent "+ext.PID+" ends") {
		t.Fatalf("approvals at an outside host: %v\n%s", err, listed)
	}
	if _, err := carol.DismissParticipation(ctx, ext.PID); err == nil {
		t.Fatal("an outside host dismissed its agent: the text above is wrong")
	}
	out.Reset()
	eventually("alice holds carol's acceptance", func() bool {
		got, err := alice.Participation(ext.PID)
		return err == nil && got.State == client.PartActive
	})
	if err := runDM(ctx, alice, []string{"dismiss-agent", ext.PID}, &out); err != nil {
		t.Fatalf("a member ends the outside host's agent: %v", err)
	}
	eventually("carol holds the dismissal", func() bool {
		got, err := carol.Participation(ext.PID)
		return err == nil && got.State == client.PartDismissed
	})
	if listed, _ = diagnosticOutput(t, func() error { return runApprovals(carol) }); strings.Contains(listed, ext.PID) {
		t.Fatalf("approvals at an outside host still lists a dismissed agent's grant:\n%s", listed)
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

func TestDMShowPrintsCopies(t *testing.T) {
	var out bytes.Buffer
	printConvMessages(&out, []client.ConvMessage{{ID: "a", Dir: "out", Body: "hello", Delivery: "delivered", State: "custody", At: 100, Sent: 90, Copies: []client.ConvCopy{{To: "you/phone", State: "custody", Own: true}, {To: "bob/laptop", State: "delivered"}}}})
	for _, word := range []string{"(delivered; you/phone: custody; bob/laptop: delivered)", "hello"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("missing %q in %s", word, out.String())
		}
	}
}

func TestDMShowPrintsSingleCopy(t *testing.T) {
	var out bytes.Buffer
	printConvMessages(&out, []client.ConvMessage{{ID: "a", Dir: "out", Body: "hello", Delivery: "delivered", State: "delivered", At: 100, Copies: []client.ConvCopy{{To: "bob/laptop", State: "delivered"}}}})
	if !strings.Contains(out.String(), "(delivered; bob/laptop: delivered)") {
		t.Fatal(out.String())
	}
}
