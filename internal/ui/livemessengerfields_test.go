package ui

import (
	"context"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// What the new messenger reads about a conversation without parsing the
// timeline's sentences: each participation record's type and its author's
// person label (or device address) on the DM's messages, and in the chat
// list the number of guests present (people and agents), the requests
// waiting for this device's person's decision there, and the latest
// message when it is a participation record.
func TestLiveMessengerConversationFields(t *testing.T) {
	alice, bob, carol, conv, eventually, _ := liveGuestWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pa, pb := NewLive(alice), NewLive(bob)
	summary := func(l *Live) DMSummary {
		t.Helper()
		o, err := l.Overview()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range o.DMs {
			if s.ID == conv {
				return s
			}
		}
		t.Fatalf("no summary of %s", conv)
		return DMSummary{}
	}
	if s := summary(pa); s.Guests != 0 || s.Decide != 0 || s.LastEvent != nil {
		t.Fatalf("a new DM: %+v", s)
	}
	guest := inviteGuest(t, alice, carol, conv, eventually)
	inv, err := pa.InviteAgent(AgentInvite{Conv: conv, Host: bob.Address})
	if err != nil {
		t.Fatal(err)
	}
	eventually("bob's agent invited at bob", func() bool { p, e := bob.Participation(inv.PID); return e == nil && p.State == "invited" })
	if _, err := pb.DecideAgent(inv.PID, true); err != nil {
		t.Fatal(err)
	}
	eventually("bob's acceptance last at alice", func() bool {
		s := summary(pa)
		return s.Guests == 2 && s.LastEvent != nil && *s.LastEvent == LastEvent{Kind: "accept", PID: inv.PID, By: "Bob"}
	})

	want := map[string]bool{"invite " + guest.PID + " Alice": true, "accept " + guest.PID + " Carol": true,
		"invite " + inv.PID + " Alice": true, "accept " + inv.PID + " Bob": true}
	d, err := pa.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, m := range d.Messages {
		if m.Event == "" {
			if m.EventType != "" || m.EventBy != "" {
				t.Fatalf("a turn with event fields: %+v", m)
			}
			continue
		}
		key := m.EventType + " " + m.PID + " " + m.EventBy
		seen = append(seen, key)
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("records missing %v; shown %v", want, seen)
	}

	// A task for bob's agent waits for bob's decision: counted on his chat
	// only, until he decides it.
	task, err := pa.AskAgent(AgentAsk{PID: inv.PID, Kind: envelope.KindTask, Body: "tidy the logs"})
	if err != nil {
		t.Fatal(err)
	}
	eventually("bob's chat counts the decision", func() bool { s := summary(pb); return s.Decide == 1 && s.LastEvent == nil })
	if s := summary(pa); s.Decide != 0 || s.LastEvent != nil || s.Guests != 2 {
		t.Fatalf("alice's chat after asking: %+v", s)
	}
	if _, err := pb.Act(Action{Do: DoDecline, ID: task.ID, Reason: "not now"}); err != nil {
		t.Fatal(err)
	}
	if s := summary(pb); s.Decide != 0 {
		t.Fatalf("bob's chat after declining: %+v", s)
	}

	// A device conversation names its agent: Alice asks Bob's named agent.
	named, err := bob.CreateLocalAgent("Builder", client.Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := bob.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	asked, err := pa.Send(Draft{To: bob.Address, Kind: KindQuestion, Body: "which build?", AgentID: named.ID})
	if err != nil {
		t.Fatal(err)
	}
	o, err := pa.Overview()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, th := range o.Threads {
		if th.ID == asked.ID {
			found = true
			if th.AgentID != named.ID {
				t.Fatalf("the device thread's agent: %+v", th)
			}
		}
	}
	if !found {
		t.Fatalf("no device thread %s: %+v", asked.ID, o.Threads)
	}
}
