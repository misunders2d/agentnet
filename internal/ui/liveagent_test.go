package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// An agent in a DM, from both people's pages: Bob invites the agent on
// Alice's laptop, sharing one earlier message and letting his own key give
// it tasks; only Alice's page can accept; Bob's question to it is held for
// Alice and nothing runs it; Alice dismisses it. Records read as sentences,
// never as their JSON.
func TestLiveAgentsInDMs(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	dm := func(l *Live, id string) DMThread {
		t.Helper()
		d, err := l.DM(id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if _, _, err := pa.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	me, _, err := pb.CreatePerson("Bob")
	if err != nil {
		t.Fatal(err)
	}
	var conv string
	eventually("bob's DM with alice", func() bool { conv, err = pb.NewDM(alice.Address); return err == nil })
	var shared, private string
	for _, body := range []string{"the deploy plan", "something private"} {
		s, err := pb.SendDM(DMDraft{Conv: conv, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if shared == "" {
			shared = s.ID
		} else {
			private = s.ID
		}
	}
	eventually("alice holds both", func() bool { v, e := pa.DM(conv); return e == nil && len(v.Messages) == 2 })

	// What cannot be invited is refused before anything is sent.
	for _, bad := range []AgentInvite{
		{Conv: conv, Host: alice.Address, Share: []string{"0123456789abcdef0123456789abcdef"}},
		{Conv: conv, Host: "carol/box"},
		{Conv: conv, Host: alice.Address, TasksFrom: []string{strings.Repeat("ab", 32)}},
	} {
		if _, err := pb.InviteAgent(bad); !errors.Is(err, ErrRefused) {
			t.Fatalf("invite %+v: %v", bad, err)
		}
	}
	if n := len(dm(pb, conv).Agents); n != 0 {
		t.Fatalf("%d agents after refused invites", n)
	}

	inv, err := pb.InviteAgent(AgentInvite{Conv: conv, Host: alice.Address, Share: []string{shared}, TasksFrom: []string{me.Fingerprint}, Note: "help with the deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != "invited" || inv.HostHere || inv.CanDecide || !inv.CanDismiss || inv.CanAsk || len(inv.Shared) != 1 || inv.Shared[0] != shared ||
		len(inv.TasksFrom) != 1 || inv.TasksFrom[0].Label != "Bob" || !strings.Contains(inv.StateText, "Alice accepts or declines") {
		t.Fatalf("invite on bob's page: %+v", inv)
	}
	if _, err := pb.DecideAgent(inv.PID, true); !errors.Is(err, ErrRefused) {
		t.Fatalf("bob accepted alice's agent: %v", err)
	}
	var seen AgentView
	eventually("the invitation on alice's page", func() bool {
		if a := dm(pa, conv).Agents; len(a) == 1 {
			seen = a[0]
		}
		return seen.State == "invited"
	})
	if !seen.HostHere || !seen.CanDecide || seen.Note != "help with the deploy" || len(seen.Shared) != 1 || seen.Shared[0] != shared ||
		!strings.Contains(seen.StateText, "Bob invited your agent. Nothing runs unless you accept") {
		t.Fatalf("invite on alice's page: %+v", seen)
	}
	for _, id := range seen.Shared {
		if id == private {
			t.Fatal("a message not chosen is shown as shared")
		}
	}

	// Only Alice's page accepts; both pages then show it in the DM.
	if v, err := pa.DecideAgent(seen.PID, true); err != nil || v.State != "active" || !v.CanAsk || !strings.Contains(v.StateText, "no responder is chosen") {
		t.Fatalf("accept (alice has no responder): %+v %v", v, err)
	}
	eventually("active on bob's page", func() bool { a := dm(pb, conv).Agents; return len(a) == 1 && a[0].State == "active" && a[0].CanAsk })

	// Each page links the agent to Alice's person, by the invitation's host.
	o, _ := pb.Overview()
	var linked []AgentLink
	for _, p := range o.People {
		if p.Address == alice.Address {
			linked = p.Agents
		} else if len(p.Agents) != 0 {
			t.Fatalf("an agent linked to %s: %+v", p.Label, p.Agents)
		}
	}
	if len(linked) != 1 || linked[0].Address != alice.Address || len(linked[0].DMs) != 1 || linked[0].DMs[0].Conv != conv || linked[0].DMs[0].State != "active" {
		t.Fatalf("alice's agent as bob's page links it: %+v", linked)
	}
	if o.Person == nil || len(o.Person.Agents) != 0 {
		t.Fatalf("bob's own person has an agent: %+v", o.Person)
	}
	if o, _ = pa.Overview(); o.Person == nil || len(o.Person.Agents) != 1 || o.Person.Agents[0].Address != alice.Address {
		t.Fatalf("alice's page does not link her agent to her: %+v", o.Person)
	}

	// Bob asks it: the question goes to Alice's device, held for her.
	if _, err := pb.AskAgent(AgentAsk{PID: inv.PID, Body: "  "}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an empty question: %v", err)
	}
	sent, err := pb.AskAgent(AgentAsk{PID: inv.PID, Body: "which branch?"})
	if err != nil {
		t.Fatal(err)
	}
	var asked DMMessage
	eventually("the question at alice", func() bool {
		for _, m := range dm(pa, conv).Messages {
			if m.ID == sent.ID {
				asked = m
			}
		}
		return asked.ID != ""
	})
	// Alice has no responder chosen: it waits for her agent, and nothing runs.
	if asked.PID != inv.PID || asked.To != alice.Address || asked.Kind != "question" || asked.State != "part_waiting" || asked.Event != "" ||
		!strings.Contains(asked.StateText, "has not run yet") {
		t.Fatalf("the question as alice's page shows it: %+v", asked)
	}
	time.Sleep(300 * time.Millisecond) // the daemon had its chance to run anything
	for _, m := range dm(pa, conv).Messages {
		if m.ID == sent.ID && m.State != "part_waiting" {
			t.Fatalf("the question to the agent changed state without a responder: %+v", m)
		}
	}

	// Alice dismisses it; neither page can ask it anything more.
	if v, err := pa.DismissAgent(seen.PID); err != nil || v.State != "dismissed" || v.CanAsk || v.CanDismiss {
		t.Fatalf("dismiss: %+v %v", v, err)
	}
	eventually("dismissed on bob's page", func() bool { a := dm(pb, conv).Agents; return len(a) == 1 && a[0].State == "dismissed" })
	if _, err := pb.AskAgent(AgentAsk{PID: inv.PID, Body: "still there?"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("asked after the dismissal: %v", err)
	}

	// The records read as sentences on both pages, never as JSON.
	for who, l := range map[string]*Live{"alice": pa, "bob": pb} {
		var events []string
		for _, m := range dm(l, conv).Messages {
			if strings.Contains(m.Body, `"pid"`) {
				t.Fatalf("%s's page shows a record's JSON: %+v", who, m)
			}
			if m.Event != "" {
				events = append(events, m.Event)
			}
		}
		got := strings.Join(events, "|")
		for _, want := range []string{"invited", "accepted", "dismissed"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s's page: events %q lack %q", who, got, want)
			}
		}
		o, _ := l.Overview()
		for _, s := range o.DMs {
			if strings.Contains(s.Last, "{") {
				t.Fatalf("%s's sidebar shows a record's JSON: %q", who, s.Last)
			}
		}
	}
	if got := dm(pb, conv).Messages; !strings.HasPrefix(eventLines(got), "You invited Alice's agent (on "+alice.Address+")") {
		t.Fatalf("bob's page: %q", eventLines(got))
	}
	if got := dm(pa, conv).Messages; !strings.HasPrefix(eventLines(got), "Bob invited your agent (on "+alice.Address+")") {
		t.Fatalf("alice's page: %q", eventLines(got))
	}
}

func eventLines(ms []DMMessage) string {
	var out []string
	for _, m := range ms {
		if m.Event != "" {
			out = append(out, m.Event)
		}
	}
	return strings.Join(out, "|")
}

// Only this device's person decides on a request to its agent, and only
// what the core allows in that state.
func TestAgentActions(t *testing.T) {
	for _, c := range []struct {
		kind, state string
		want        string
	}{
		{"task", "awaiting", "accept,decline"},
		{"question", "awaiting", "accept,decline"}, // a conversation guest's question: one-time acceptance, or not
		{"task", "running", "cancel"},
		{"question", "needs_human", "accept,resolve"},
		{"task", "failed", "accept"},
		{"task", "interrupted", "accept,resolve"}, // run it again, or close it without running it (review finding 3)
		{"question", "interrupted", "accept,resolve"},
		{"question", "cancelled", "accept"},
		{"question", "part_waiting", ""},
		{"question", "answered", ""},
		{"task", "not_run", ""},
		{"task", "not_delivered", ""},
		{"question", "conv_held", ""},
	} {
		if got := strings.Join(AgentActions(c.kind, c.state), ","); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.kind, c.state, got, c.want)
		}
	}
}

// A task needs no standing permission to be asked: with no task keys in
// the invitation, Bob's task to Alice's agent waits for Alice, and runs
// once when she accepts it from her page. The agent is a stand-in binary
// named like a supported harness (no model is called).
func TestLiveAgentTaskWaitsForItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("HOME", t.TempDir()) // nothing of the real harness's sessions is read
	bin := t.TempDir()
	log := filepath.Join(bin, "runs")
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho run >> '"+log+"'\nprintf 'rotated\\n\\nemotion: calm\\n'\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	if err := alice.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	pa.CreatePerson("Alice")
	pb.CreatePerson("Bob")
	var conv string
	eventually("a DM", func() bool { conv, err = pb.NewDM(alice.Address); return err == nil })
	inv, err := pb.InviteAgent(AgentInvite{Conv: conv, Host: alice.Address})
	if err != nil || len(inv.TasksFrom) != 0 {
		t.Fatalf("invite without task keys: %+v %v", inv, err)
	}
	eventually("the invitation at alice", func() bool { d, _ := pa.DM(conv); return len(d.Agents) == 1 })
	if _, err := pa.DecideAgent(inv.PID, true); err != nil {
		t.Fatal(err)
	}
	eventually("active at bob", func() bool { d, _ := pb.DM(conv); return len(d.Agents) == 1 && d.Agents[0].CanAsk })
	sent, err := pb.AskAgent(AgentAsk{PID: inv.PID, Kind: "task", Body: "rotate the key"})
	if err != nil {
		t.Fatal(err)
	}
	var task DMMessage
	eventually("the task waiting for alice", func() bool {
		d, _ := pa.DM(conv)
		for _, m := range d.Messages {
			if m.ID == sent.ID {
				task = m
			}
		}
		return task.State == "awaiting"
	})
	if strings.Join(task.Actions, ",") != "accept,decline" || !strings.Contains(task.StateText, "accept") {
		t.Fatalf("the waiting task on alice's page: %+v", task)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the agent ran before alice accepted")
	}
	if _, err := pa.Act(Action{Do: DoAccept, ID: sent.ID}); err != nil {
		t.Fatal(err)
	}
	var result DMMessage
	eventually("the agent's result at bob", func() bool {
		d, _ := pb.DM(conv)
		for _, m := range d.Messages {
			if m.Dir == "in" && m.PID == inv.PID && strings.HasPrefix(m.Origin, "agent:") && strings.Contains(m.Body, "rotated") {
				result = m
				return true
			}
		}
		return false
	})
	// Marked as the agent's from its host's key, never from its origin: the
	// person's request beside it is not.
	if !result.VerifiedAgent || task.VerifiedAgent {
		t.Fatalf("verified agent: result %+v, task %+v", result, task)
	}
	if data, _ := os.ReadFile(log); strings.Count(string(data), "run") != 1 {
		t.Fatalf("the agent ran %q", data)
	}
	// Own requests run automatically, even without grants. A failed run
	// offers an explicit retry, not another consent decision.
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho run >> '"+log+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"question", "task"} {
		own, err := pa.AskAgent(AgentAsk{PID: inv.PID, Kind: kind, Body: "own failed request"})
		if err != nil {
			t.Fatal(err)
		}
		var failed DMMessage
		eventually("own "+kind+" automatic failure", func() bool {
			d, _ := pa.DM(conv)
			for _, m := range d.Messages {
				if m.LID == own.LID && m.Dir == "out" {
					failed = m
					return strings.Contains(m.StateText, "failed") && len(m.Actions) > 0
				}
			}
			return false
		})
		if failed.Exec == nil || failed.Exec.State != "failed" || failed.Exec.Host != alice.Address || failed.State == "failed" || strings.Join(failed.Actions, ",") != "accept" {
			t.Fatalf("own retry lost local execution or transport state: %+v", failed)
		}
	}
	if data, _ := os.ReadFile(log); strings.Count(string(data), "run") != 3 {
		t.Fatalf("own requests did not run exactly once without approval: %q", data)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho run >> '"+log+"'\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	own, err := pa.AskAgent(AgentAsk{PID: inv.PID, Kind: "question", Body: "own stopped request"})
	if err != nil {
		t.Fatal(err)
	}
	var stopped DMMessage
	findOwn := func(state string) bool {
		d, _ := pa.DM(conv)
		for _, m := range d.Messages {
			if m.LID == own.LID && m.Dir == "out" {
				stopped = m
				return m.Exec != nil && m.Exec.State == state
			}
		}
		return false
	}
	eventually("own question running", func() bool { return findOwn("running") })
	eventually("own harness started", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Count(string(data), "run") == 4
	})
	if _, err := pa.Act(Action{Do: DoCancel, ID: stopped.ID}); err != nil {
		t.Fatal(err)
	}
	eventually("own question stopped", func() bool { return findOwn("cancelled") })
	if strings.Join(stopped.Actions, ",") != "accept" || stopped.State == "cancelled" {
		t.Fatalf("stopped request must offer retry while preserving transport: %+v", stopped)
	}
	if data, _ := os.ReadFile(log); strings.Count(string(data), "run") != 4 {
		t.Fatalf("Stop or viewing silently reran the request: %q", data)
	}
}

// Desktop alerts from the daemon's page, through the client's alert
// preferences: off by default; on adds the people this person started a
// DM with (an arrival adds nobody); a DM mutes by its id; allowing names
// the exact key; a presentation report goes through; nothing asks a
// browser for anything (native).
func TestLiveAlerts(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	code, _ = alice.Invite(ctx, "carol", time.Hour, false)
	carol, err := client.Join(ctx, filepath.Join(t.TempDir(), "carol"), code, "box")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { carol.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	runDaemon(t, carol)
	for _, a := range []*client.Agent{alice, carol} {
		if _, err := a.CreatePerson(ctx, strings.Split(a.Address, "/")[0]); err != nil {
			t.Fatal(err)
		}
	}
	pb := NewLive(bob)
	pb.CreatePerson("Bob")
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	var mine, theirs string
	eventually("bob's DM with alice", func() bool { mine, err = pb.NewDM(alice.Address); return err == nil })
	pb.SendDM(DMDraft{Conv: mine, Body: "hi alice"})
	eventually("carol's DM with bob", func() bool { theirs, err = carol.CreateDM(ctx, bob.Address); return err == nil })
	hello, _ := carol.SendConv(ctx, theirs, client.ConvOutgoing{Body: "hi bob"})
	eventually("carol's DM at bob", func() bool { _, err := pb.DM(theirs); return err == nil })

	o, _ := pb.Overview()
	if o.Notify == nil || !o.Notify.Native || !o.Notify.Available || o.Notify.Enabled {
		t.Fatalf("before turning on: %+v", o.Notify)
	}
	if _, err := pb.NotifyEnable(); err != nil {
		t.Fatal(err)
	}
	p, _ := bob.AlertPrefs()
	if !p.Enabled || len(p.Senders) != 1 || p.Senders[0].Address != alice.Address || p.Senders[0].Fingerprint != alice.Self().Fingerprint() {
		t.Fatalf("on: %+v (only alice, whom bob started a DM with)", p)
	}
	d, _ := pb.DM(theirs)
	if _, err := pb.NotifyAllow(d.Peer.Person, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.NotifyMute(mine, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.NotifyMute(strings.Repeat("ab", 32), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("muting an unknown DM: %v", err)
	}
	p, _ = bob.AlertPrefs()
	if len(p.Senders) != 2 || len(p.Mutes) != 1 || p.Mutes[0] != mine {
		t.Fatalf("after allowing carol and muting alice's DM: %+v", p)
	}
	if err := pb.NotifySeen(theirs, []string{hello.ID}); err != nil {
		t.Fatal(err)
	}
	if err := pb.NotifySeen("nope", []string{hello.ID}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a report for no conversation: %v", err)
	}
	if o, _ = pb.Overview(); !o.Notify.Enabled || len(o.Notify.Allowed) != 2 || len(o.Notify.Mutes) != 1 {
		t.Fatalf("overview when on: %+v", o.Notify)
	}
	// A later signed root belongs to the same person chat, including its mute.
	later, err := bob.CreateDM(ctx, alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	if later == mine {
		t.Fatal("fixture reused the original root")
	}
	p, _ = bob.AlertPrefs()
	if !slices.Contains(p.Mutes, later) {
		t.Fatalf("later root lost mute: %+v", p)
	}
	if _, err := pb.NotifyMute(theirs, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.NotifyMute(later, false); err != nil {
		t.Fatal(err)
	}
	p, _ = bob.AlertPrefs()
	if slices.Contains(p.Mutes, mine) || slices.Contains(p.Mutes, later) || !slices.Contains(p.Mutes, theirs) {
		t.Fatalf("person-chat unmute changed wrong scope: %+v", p)
	}
	if _, err := pb.NotifyDisable(); err != nil {
		t.Fatal(err)
	}
	if p, _ = bob.AlertPrefs(); p.Enabled || len(p.Senders) != 2 {
		t.Fatalf("off keeps the choices: %+v", p)
	}
}

// "Remind me later" from the daemon's page (S-R), through the client's
// reminders: on a received DM message and a received device message, not
// on one's own; only in the future; overdue once past its time, until it
// ends; a reply to that message ends it, and the message's own state
// never changes.
func TestLiveReminders(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	alice.CreatePerson(ctx, "Alice")
	pb := NewLive(bob)
	pb.CreatePerson("Bob")
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	var conv string
	eventually("alice's DM with bob", func() bool { conv, err = alice.CreateDM(ctx, bob.Address); return err == nil })
	q, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Kind: "question", Body: "can you check the budget?\nsecond line"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := alice.Send(ctx, bob.Address, "a device message", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually("both at bob", func() bool {
		d, err := pb.DM(conv)
		_, terr := pb.Thread(dev.ID)
		return err == nil && len(d.Messages) == 1 && terr == nil
	})
	mine, _ := pb.SendDM(DMDraft{Conv: conv, Body: "my own"})
	before, _ := pb.DM(conv) // bob's own turn answered the held question (BUG-24): it is no longer held

	o, _ := pb.Overview()
	if !o.Remind || len(o.Reminders) != 0 {
		t.Fatalf("before any reminder: %v %v", o.Remind, o.Reminders)
	}
	if err := pb.SetReminder(mine.ID, time.Now().Add(time.Hour)); !errors.Is(err, ErrRefused) {
		t.Fatalf("a reminder on one's own message: %v", err)
	}
	if err := pb.SetReminder(q.ID, time.Now().Add(-time.Minute)); !errors.Is(err, ErrRefused) {
		t.Fatalf("a reminder in the past: %v", err)
	}
	if err := pb.SetReminder(q.ID, time.Now().Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := pb.SetReminder(dev.ID, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	o, _ = pb.Overview()
	if len(o.Reminders) != 2 || o.Reminders[0].Message != q.ID || o.Reminders[0].Conv != conv || o.Reminders[0].Title != "can you check the budget?" ||
		o.Reminders[0].From != alice.Address || o.Reminders[1].Message != dev.ID || o.Reminders[1].Conv != "" || o.Reminders[1].Title != "a device message" {
		t.Fatalf("reminders: %+v", o.Reminders)
	}
	eventually("the DM reminder overdue", func() bool { o, _ = pb.Overview(); return o.Reminders[0].Overdue })
	if d, _ := pb.DM(conv); d.Messages[0].State != before.Messages[0].State || before.Messages[0].State != "manual" {
		t.Fatalf("the reminded question changed: %+v (was %s)", d.Messages[0], before.Messages[0].State)
	}
	// Done and cancel end it; again is not found.
	if err := pb.CancelReminder(dev.ID); err != nil {
		t.Fatal(err)
	}
	if err := pb.DoneReminder(dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("done after cancel: %v", err)
	}
	// A reply to that very message ends it (still overdue until then).
	if _, err := pb.SendDM(DMDraft{Conv: conv, Body: "not today", ReplyTo: q.ID}); err != nil {
		t.Fatal(err)
	}
	eventually("the reply ends the reminder", func() bool { o, _ = pb.Overview(); return len(o.Reminders) == 0 })
	if r, ok, _ := bob.Reminder(q.ID); !ok || r.State != client.ReminderReplied {
		t.Fatalf("after the reply: %+v %v", r, ok)
	}
}

// Files in DMs from the daemon's page (MEL-489), through the client: the
// page hands bytes over (kept privately until sent), sends a file-only
// message naming them; the other page lists the file under a safe name and
// opens it only as the sender signed it; sent files and unknown ids are
// refused.
func TestLiveFiles(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	pa.CreatePerson("Alice")
	pb.CreatePerson("Bob")
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	var conv string
	eventually("bob's DM with alice", func() bool { conv, err = pb.NewDM(alice.Address); return err == nil })
	if o, _ := pb.Overview(); o.Files == nil || o.Files.MaxCount != 8 || o.Files.MaxFile != client.MaxFileSize {
		t.Fatalf("limits: %+v", o.Files)
	}
	content := []byte("quarterly numbers\n")
	id, err := pb.StageFile("../numbers.csv", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pb.SendDM(DMDraft{Conv: conv, Files: []string{"nope"}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an unknown file: %v", err)
	}
	if _, err := pb.SendDM(DMDraft{Conv: conv}); !errors.Is(err, ErrRefused) {
		t.Fatalf("nothing to send: %v", err)
	}
	if _, err := pb.SendDM(DMDraft{Conv: conv, Body: "\u200b\u3000"}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "Write a message") {
		t.Fatalf("text that shows nothing: %v", err)
	}
	// A send takes every file it names: with one gone it is refused, and
	// the ones found go with it, plaintext included; a discarded file goes.
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	kept, _ := pb.StageFile("kept.txt", strings.NewReader("k"))
	gone, _ := pb.StageFile("gone.txt", strings.NewReader("g"))
	keptPath, gonePath := pb.staged.files[kept].path, pb.staged.files[gone].path
	if !exists(keptPath) || !exists(gonePath) {
		t.Fatal("staged files are not kept")
	}
	if _, err := pb.SendDM(DMDraft{Conv: conv, Files: []string{kept, "nope"}}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "send again") {
		t.Fatalf("a send naming a file gone: %v", err)
	}
	if _, ok := pb.staged.files[kept]; ok || exists(keptPath) {
		t.Fatal("a refused send left the file it took")
	}
	pb.DiscardFiles([]string{gone, "never"})
	if _, ok := pb.staged.files[gone]; ok || exists(gonePath) || len(pb.staged.files) != 1 {
		t.Fatalf("a discarded file is kept: %v", pb.staged.files)
	}
	sent, err := pb.SendDM(DMDraft{Conv: conv, Files: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.staged.files) != 0 {
		t.Fatalf("a sent file is still staged: %v", pb.staged.files)
	}
	if _, err := pb.SendDM(DMDraft{Conv: conv, Files: []string{id}}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a file sent twice: %v", err)
	}
	// A device conversation (v1) takes files too, under the names chosen;
	// the receiving page shows the name a received file is saved under.
	v1Name := "../budget <plan>.xlsx"
	v1, _ := pb.StageFile(v1Name, strings.NewReader("cells"))
	v1Sent, err := pb.Send(Draft{To: alice.Address, Kind: KindMessage, Files: []string{v1}})
	if err != nil || len(pb.staged.files) != 0 {
		t.Fatalf("a file in a device conversation: %v (staged %d)", err, len(pb.staged.files))
	}
	eventually("the v1 file at alice", func() bool {
		o, _ := pa.Overview()
		for _, ts := range o.Threads {
			th, err := pa.Thread(ts.ID)
			if err != nil || ts.Peer != bob.Address {
				continue
			}
			for _, m := range th.Messages {
				if m.ID == v1Sent.ID {
					return len(m.Files) == 1 && m.Files[0].Name == client.SafeName(v1Name) && m.Files[0].Name != v1Name
				}
			}
		}
		return false
	})
	// A new run removes what an earlier one staged and never sent.
	left, _ := pb.StageFile("left.txt", strings.NewReader("l"))
	leftPath := pb.staged.files[left].path
	NewLive(bob)
	if exists(leftPath) {
		t.Fatal("a new run kept an earlier run's staged file")
	}
	delete(pb.staged.files, left)

	var got DMMessage
	eventually("the file at alice", func() bool {
		d, err := pa.DM(conv)
		if err == nil && len(d.Messages) == 1 {
			got = d.Messages[0]
		}
		return got.ID != ""
	})
	if got.ID != sent.ID || got.Body != "" || len(got.Attachments) != 1 || got.Attachments[0].Name != "_numbers.csv" || got.Attachments[0].Size != int64(len(content)) {
		t.Fatalf("alice's page: %+v", got)
	}
	// A message of files only is listed by their names, not as nothing.
	if o, _ := pa.Overview(); len(o.DMs) != 1 || o.DMs[0].Last != "📎 _numbers.csv" {
		t.Fatalf("alice's DM list: %+v", o.DMs)
	}
	r, name, err := pa.OpenFile(ctx, "in", got.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	opened, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(opened, content) || name != "_numbers.csv" {
		t.Fatalf("opened %q as %q", opened, name)
	}
	if _, _, err := pa.OpenFile(ctx, "in", got.ID, 1); !errors.Is(err, ErrRefused) {
		t.Fatalf("a file that is not there: %v", err)
	}
	// Bob reopens what he sent from the copy his device kept for itself.
	r, name, err = pb.OpenFile(ctx, "out", sent.ID, 0)
	if err != nil {
		t.Fatalf("bob reopening what he sent: %v", err)
	}
	opened, _ = io.ReadAll(r)
	r.Close()
	if !bytes.Equal(opened, content) || name != "_numbers.csv" {
		t.Fatalf("bob reopened %q as %q", opened, name)
	}
	if d, _ := pb.DM(conv); len(d.Messages[0].Attachments) != 1 || d.Messages[0].Attachments[0].Name != "_numbers.csv" {
		t.Fatalf("bob's page lists his sent file: %+v", d.Messages[0])
	}
}

func TestDMNotRunUsesExactCauseReason(t *testing.T) {
	for _, c := range []struct{ detail, want string }{
		{"not run: originating local run stopped", "Not run: the agent request that started this request finished or stopped first."},
		{"not run: selected agent unavailable", "Not run: the selected agent is unavailable on its host."},
		{"not run: the asking guest's participation ended", "Not run: the guest who asked left or was removed first."},
		{"not run: the agent's participation is dismissed", "Not run: the agent's part in this DM ended first."},
	} {
		if got := DMStateText("in", "question", "not_run", "", c.detail); got != c.want {
			t.Errorf("%q: %q, want %q", c.detail, got, c.want)
		}
	}
}
