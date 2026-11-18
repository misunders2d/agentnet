package ui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A chat's kept projection shows its files as they stand now: a download
// started after the chat was read is "fetching" on the next page, though
// starting it wrote nothing that moves the chat's mark.
func TestDMPageFilesAsTheyStandNow(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "notes.bin") // over 3 MiB: it waits for a tap
	if err := os.WriteFile(path, make([]byte, 3<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := live.SendDM(DMDraft{Conv: conv, Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if rows, err := alice.ConversationMessages(conv); err == nil && len(rows) > 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("alice has no DM: %v", err)
		}
	}
	if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "a file", Files: []client.OutgoingFile{{Path: path}}}); err != nil {
		t.Fatal(err)
	}
	var projected atomic.Int64
	dmProjected = func(c string) {
		if c == conv {
			projected.Add(1)
		}
	}
	defer func() { dmProjected = nil }()
	received := func() DMMessage {
		t.Helper()
		projected.Store(0)
		d, err := live.DMPage(conv, DMPage{})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range d.Messages {
			if m.Dir == "in" && len(m.Attachments) == 1 {
				return m
			}
		}
		return DMMessage{}
	}
	var m DMMessage
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if m = received(); m.ID != "" && m.Attachments[0].Fetch == "wait" && projected.Load() == 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("bob has no waiting file: %+v", m)
		}
	}
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { hub.SetHold(nil); close(release) })
	hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == bob.Address && strings.HasPrefix(r.URL.Path, "/v1/blobs/") {
			<-release
		}
	})
	if _, err := live.FetchFile("in", m.ID, 0); err != nil {
		t.Fatal(err)
	}
	got := received()
	if n := projected.Load(); n != 0 {
		t.Fatalf("the chat was projected again (%d): the kept projection was not what served it", n)
	}
	if got.ID != m.ID || got.Attachments[0].Fetch != "fetching" {
		t.Fatalf("a started download shows %+v, want fetching", got.Attachments)
	}
}
