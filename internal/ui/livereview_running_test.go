package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// A real signed direct-device request is received normally. Only the worker
// lifecycle states are staged in its local store: this tests the native page
// projection, without launching an assistant or claiming worker execution.
func TestLiveDirectReviewSeparatesRunningFromDecisions(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	hubHome := t.TempDir()
	testhub.Start(t, hubHome, "127.0.0.1:0", "")
	sender, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, hubHome), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sender.Close() })
	code, err := sender.Invite(ctx, "brin", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	receiver, err := client.Join(ctx, home, code, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { receiver.Close() })
	runDaemon(t, receiver)
	live := NewLive(receiver)
	sent, err := sender.SendMessage(ctx, client.Outgoing{To: receiver.Address, Kind: envelope.KindQuestion, Body: "Check the fictional Orchard dispatch plan"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "signed direct-device request held for review", func() bool {
		o, err := live.Overview()
		return err == nil && len(o.Review) == 1 && o.Review[0].ID == sent.ID
	})
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	check := func(state, reason string, decisions, running int) {
		t.Helper()
		o, err := live.Overview()
		if err != nil || len(o.Review) != 1 || o.Review[0].ID != sent.ID || o.Review[0].Peer != sender.Address || o.Review[0].Reason != reason || o.Review[0].Notice || len(o.NeedsYou) != 0 {
			t.Fatalf("%s direct review: %+v %v", state, o, err)
		}
		th, err := live.Thread(sent.ID)
		if err != nil || len(th.Messages) != 1 || th.Messages[0].ID != sent.ID || th.Messages[0].State != state {
			t.Fatalf("%s exact request: %+v %v", state, th, err)
		}
		if state == "running" && !slices.Contains(th.Messages[0].Actions, DoCancel) {
			t.Fatal("running direct request lost Stop")
		}
		if reason == "agent_running" && (slices.Contains(th.Messages[0].Actions, DoAccept) || slices.Contains(th.Messages[0].Actions, DoDecline)) {
			t.Fatal("running/stopping direct request offers a new decision")
		}
		if len(o.Threads) != 1 || o.Threads[0].Review != decisions || o.Threads[0].Running != running {
			t.Fatalf("%s topic counts: %+v", state, o.Threads)
		}
	}
	check("held", "", 1, 0)
	res, err := db.Exec(`UPDATE inbox SET state='running' WHERE id=? AND state='held'`, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatal("trusted local running-state fixture did not update exact request")
	}
	check("running", "agent_running", 0, 1)
	if _, err := live.Act(Action{ID: sent.ID, Do: DoCancel}); err != nil {
		t.Fatalf("Stop exact running direct request: %v", err)
	}
	check("cancel_requested", "agent_running", 0, 1)
	if _, err := db.Exec(`UPDATE inbox SET state='cancelled' WHERE id=? AND state='cancel_requested'`, sent.ID); err != nil {
		t.Fatal(err)
	}
	o, err := live.Overview()
	if err != nil || len(o.Review) != 0 || len(o.NeedsYou) != 0 || len(o.Threads) != 1 || o.Threads[0].Review != 0 || o.Threads[0].Running != 0 {
		t.Fatalf("completed cancellation still listed as working/decision: %+v %v", o, err)
	}
}

func TestFixtureDirectReviewRunningMetadata(t *testing.T) {
	f := testFixture()
	id := reviewOf(t, f, "dave/srv", KindQuestion)
	find := func() (ReviewItem, bool) {
		for _, item := range overview(t, f).Review {
			if item.ID == id {
				return item, true
			}
		}
		return ReviewItem{}, false
	}
	if item, ok := find(); !ok || item.Reason == "agent_running" {
		t.Fatalf("held demo request is working: %+v", item)
	}
	if _, err := f.Act(Action{ID: id, Do: DoAccept}); err != nil {
		t.Fatal(err)
	}
	if item, ok := find(); !ok || item.Reason != "agent_running" {
		t.Fatalf("running demo request lost working metadata: %+v", item)
	}
	// The demo already starts with one different running request. Finish
	// each public simulation until this exact newly accepted question ends.
	for n := 0; n < 2; n++ {
		if _, ok := find(); !ok {
			break
		}
		if err := f.Simulate("finish"); err != nil {
			t.Fatal(err)
		}
	}
	if item, ok := find(); ok {
		t.Fatalf("completed demo request remains working: %+v", item)
	}
}
