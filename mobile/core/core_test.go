package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
)

func fixture(t *testing.T) (*Session, string) {
	t.Helper()
	home := t.TempDir()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = id.Save(filepath.Join(home, "identity.json")); err != nil {
		t.Fatal(err)
	}
	_, _ = client.Open(home) // create schema without an enrollment or any network
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"enrolled": "1", "address": "member/phone", "hub": "https://127.0.0.1:1", "hub_cert": ""} {
		if _, err = db.Exec("INSERT OR REPLACE INTO config(k,v) VALUES(?,?)", k, v); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, home
}

type callback func()

func (f callback) OnChange() { f() }

func TestOfflineLifecycleAndOneRun(t *testing.T) {
	s, _ := fixture(t)
	if raw, err := s.OverviewJSON(); err != nil || !json.Valid([]byte(raw)) {
		t.Fatalf("offline overview %q %v", raw, err)
	}
	var active, maximum atomic.Int32
	s.run = func(ctx context.Context) error {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		defer active.Add(-1)
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Start(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s.Stop()
	if maximum.Load() != 1 || active.Load() != 0 {
		t.Fatalf("runs max=%d active=%d", maximum.Load(), active.Load())
	}
	if _, err := s.OverviewJSON(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	s.Close()
	s.Close()
	if err := s.Start(); err == nil {
		t.Fatal("closed session restarted")
	}
	if _, err := s.OverviewJSON(); err == nil {
		t.Fatal("closed read succeeded")
	}
}

func TestListenerCanReadAndStop(t *testing.T) {
	s, _ := fixture(t)
	called := make(chan struct{}, 1)
	s.SetListener(callback(func() {
		if _, err := s.OverviewJSON(); err != nil {
			t.Error(err)
		}
		s.Stop()
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("listener deadlocked")
	}
	s.SetListener(nil)
}
func TestRejectConfiguredResponder(t *testing.T) {
	s, home := fixture(t)
	if err := s.a.SetResponder(&client.Responder{Harness: "codex", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("configured responder started")
	}
	s.Close()
	if other, err := Open(home); err == nil {
		other.Close()
		t.Fatal("configured responder opened")
	}
}
func TestInvalidHomeAndDraft(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("empty home accepted")
	}
	if _, err := Link("relative", "bad", "phone"); err == nil {
		t.Fatal("relative home accepted")
	}
	s, _ := fixture(t)
	if _, err := s.MarkReadJSON(`[]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkReadJSON(`{"do":"approve"}`); err == nil {
		t.Fatal("generic action accepted")
	}
	if _, err := s.SendJSON(`{"files":["../../desktop"]}`); err == nil {
		t.Fatal("desktop file accepted")
	}
	if _, err := s.SendDMJSON("{"); err == nil {
		t.Fatal("malformed draft accepted")
	}
}

// A send owns only a shared lifetime lease while it waits on the network.
// Cached data, events and transport cancellation must remain available.
func TestBlockedOperationDoesNotBlockOfflineReadsOrStop(t *testing.T) {
	s, _ := fixture(t)
	s.run = func(ctx context.Context) error { <-ctx.Done(); return nil }
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() { s.data.RLock(); close(blocked); <-release; s.data.RUnlock(); close(finished) }()
	<-blocked
	defer func() { close(release); <-finished }()
	callbackDone := make(chan struct{}, 1)
	s.SetListener(callback(func() {
		select {
		case callbackDone <- struct{}{}:
		default:
		}
	}))
	readDone := make(chan error, 1)
	go func() { _, err := s.OverviewJSON(); readDone <- err }()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("network operation blocked saved overview")
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("network operation blocked event")
	}
	stopDone := make(chan struct{})
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("network operation blocked transport cancellation")
	}
}
