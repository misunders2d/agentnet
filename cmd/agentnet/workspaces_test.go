package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui"
)

func TestWorkspaceUIOwnsSeparateHomesAndDrainsShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	home := filepath.Join(t.TempDir(), "home")
	firstHub := filepath.Join(t.TempDir(), "hub-a")
	testhub.Start(t, firstHub, "127.0.0.1:0", "")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, firstHub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	secondHub := filepath.Join(t.TempDir(), "hub-b")
	testhub.Start(t, secondHub, "127.0.0.1:0", "")
	registry, err := client.OpenWorkspaces(home)
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.Join(ctx, "", "Personal", testhub.BootstrapCode(t, secondHub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	secondHome, err := registry.Home(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	releaseDefault, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDefault()
	_, handler, stop, err := startWorkspaceUI(a, home, "127.0.0.1:12345", "workspace-test-token", "")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	request := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/workspaces", nil)
	request.Header.Set("Cookie", "agentnet_ui=workspace-test-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var bindings []ui.WorkspaceBinding
	if err = json.Unmarshal(response.Body.Bytes(), &bindings); err != nil || len(bindings) != 2 {
		t.Fatalf("memberships: %s %v", response.Body.String(), err)
	}
	if release, err := lockfile.Acquire(filepath.Join(secondHome, "daemon.lock")); err == nil {
		release()
		t.Fatal("second workspace served without ownership")
	}
	// Wait for this runtime's authenticated stream/member snapshot, rather
	// than cancelling a just-opened TLS handshake during fixture shutdown.
	var extra ui.WorkspaceBinding
	for _, binding := range bindings {
		if binding.ID == b.ID {
			extra = binding
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := httptest.NewRequest("GET", "http://127.0.0.1:12345/workspaces/"+extra.ID+"/"+extra.Handle+"/api/overview", nil)
		r.Header.Set("Cookie", "agentnet_ui=workspace-test-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		var overview ui.Overview
		if json.Unmarshal(rec.Body.Bytes(), &overview) == nil && overview.Directory.Status == "listed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second runtime never connected: %s", rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stopped = true
	releaseSecond, err := lockfile.Acquire(filepath.Join(secondHome, "daemon.lock"))
	if err != nil {
		t.Fatalf("shutdown left second daemon running: %v", err)
	}
	releaseSecond()
	if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
		release()
		t.Fatal("workspace shutdown released default daemon lock")
	}
}
