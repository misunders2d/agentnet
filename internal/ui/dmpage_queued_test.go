package ui

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A page of /api/dm counts the messages still waiting to go like the stored
// ones: with one stored turn and three queued, a page of one is one message,
// and a queued message anchors a page as any other does.
func TestDMPageCountsQueuedSends(t *testing.T) {
	alice, bob, live, hub := liveWorldHub(t)
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
	stored := protocol.NewID()
	if _, err := live.SendDM(DMDraft{ID: stored, Conv: conv, Body: "stored turn"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if rows := queuedRowsOf(t, live, conv, stored); len(rows) == 1 && rows[0].QueuedSend == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the first message was not stored: %+v", rows)
		}
	}
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { hub.SetHold(nil); close(release) }) }
	defer free()
	hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == bob.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			<-release
		}
	})
	var queued []string
	for i := range 3 {
		id := protocol.NewID()
		sent, err := live.SendDM(DMDraft{ID: id, Conv: conv, Body: "waits " + string(rune('a'+i))})
		if err != nil || sent.State != "queued" {
			t.Fatalf("not queued: %+v %v", sent, err)
		}
		queued = append(queued, id)
	}
	page, err := live.DMPage(conv, DMPage{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].LID != queued[2] || !page.Older || page.Newer {
		t.Fatalf("newest page of one: %d messages %+v older=%v newer=%v", len(page.Messages), page.Messages, page.Older, page.Newer)
	}
	page, err = live.DMPage(conv, DMPage{From: queued[0], Limit: 2})
	if err != nil {
		t.Fatalf("a queued message as the anchor: %v", err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != stored && page.Messages[0].LID != stored || page.Messages[1].LID != queued[0] || page.Older || !page.Newer {
		t.Fatalf("page up to a queued message: %+v older=%v newer=%v", page.Messages, page.Older, page.Newer)
	}
}
