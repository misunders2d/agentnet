package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// "Delete conversation…" on a DM where nothing was said yet takes it off the
// chat list (as TDLib removes an empty chat), with the list already built
// and kept: no "nothing here to delete", and the person stays.
func TestDeleteEmptyDMLeavesTheList(t *testing.T) {
	alice, bob, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { alice.Run(run, client.RunOptions{}); close(done) }()
	defer func() { stop(); <-done }()
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := bob.CreateDM(ctx, alice.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	listed := func() (bool, []PersonView) {
		o, err := live.Overview()
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(o.DMs, func(d DMSummary) bool { return d.ID == conv }), o.People
	}
	shown, before := listed()
	if !shown {
		t.Fatal("the empty DM is not listed")
	}
	note, err := live.DeleteConversation(DeleteConversationAction{Conv: conv})
	if err != nil || !strings.HasPrefix(note, "Deleted here.") {
		t.Fatalf("delete: %q %v", note, err)
	}
	shown, after := listed()
	if shown {
		t.Fatal("the empty DM is still listed after its deletion")
	}
	if len(after) != len(before) || !slices.ContainsFunc(after, func(p PersonView) bool { return strings.Contains(p.Label, alice.Address) }) {
		t.Fatalf("the people changed: %+v then %+v", before, after)
	}
}
