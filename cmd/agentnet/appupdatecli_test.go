package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestBundledUpdateRequestsWholeApp(t *testing.T) {
	old := bundledWith
	bundledWith = "app"
	t.Cleanup(func() { bundledWith = old })
	home := t.TempDir()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cookie, e := r.Cookie("agentnet_ui")
		if e != nil || cookie.Value != "private" || r.Header.Get("Origin") != "http://"+r.Host || r.URL.Path != "/api/app/update" || r.Method != "POST" {
			t.Error("incorrect authenticated whole-app request")
		}
		var choice struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(r.Body).Decode(&choice) != nil || choice.Version != "v0.8.2" {
			t.Error("named release lost")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"restarting"}`))
	}))
	defer server.Close()
	if e := secfile.Write(filepath.Join(home, uiURLFile), []byte(server.URL+"/?t=private")); e != nil {
		t.Fatal(e)
	}
	if e := runUpdate(context.Background(), home, []string{"v0.8.2"}); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestBundledUpdateRejectsRemoteAndRedirect(t *testing.T) {
	home := t.TempDir()
	for _, address := range []string{"http://example.com/?t=secret", "https://127.0.0.1:55/?t=secret", "http://user@127.0.0.1:55/?t=secret"} {
		secfile.Write(filepath.Join(home, uiURLFile), []byte(address))
		if e := requestAppUpdate(context.Background(), home, false, ""); e == nil {
			t.Fatal(address)
		}
	}
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	secfile.Write(filepath.Join(home, uiURLFile), []byte(redirect.URL+"/?t=secret"))
	if e := requestAppUpdate(context.Background(), home, false, ""); e == nil || !strings.Contains(e.Error(), "302") {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("cookie redirect followed")
	}
}

func TestBundledUpdateCheckDoesNotInstall(t *testing.T) {
	home := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/app/status" {
			t.Error("check mutated app")
		}
		w.Write([]byte(`{"app_update_supported":true}`))
	}))
	defer server.Close()
	secfile.Write(filepath.Join(home, uiURLFile), []byte(server.URL+"/?t=private"))
	if e := requestAppUpdate(context.Background(), home, true, "v0.8.2"); e != nil {
		t.Fatal(e)
	}
}

func TestAppUpdateRefusesOtherDaemonBeforeStaging(t *testing.T) {
	home := t.TempDir()
	release, e := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	r := &appRunner{home: home, addr: "127.0.0.1:17443", token: "private", exe: "/tmp/AgentNet.AppImage"}
	req := httptest.NewRequest("POST", "http://"+r.addr+"/api/app/update", bytes.NewBufferString("{}"))
	req.Header.Set("Origin", "http://"+r.addr)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: "private"})
	rec := httptest.NewRecorder()
	r.appAPI(rec, req)
	if rec.Code != 409 || r.updating.Load() {
		t.Fatalf("unsafe update: %d %s", rec.Code, rec.Body)
	}
}

func TestBundledUpdateNeedsExactAppAcknowledgement(t *testing.T) {
	home := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>ordinary messenger</html>")) }))
	defer server.Close()
	secfile.Write(filepath.Join(home, uiURLFile), []byte(server.URL+"/?t=private"))
	if e := requestAppUpdate(context.Background(), home, false, ""); e == nil {
		t.Fatal("ordinary page claimed a whole-app update")
	}
}

func TestBundledUpdateStartsClosedAppWithoutLimitingAcceptedStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolated /bin/sh app-launch fixture")
	}
	f := newGlobalUpdateFixture(t)
	f.server.Close()
	if err := os.Remove(filepath.Join(f.home, uiURLFile)); err != nil {
		t.Fatal(err)
	}
	previousTimeout := appUpdateStartTimeout
	appUpdateStartTimeout = 2 * time.Second
	t.Cleanup(func() { appUpdateStartTimeout = previousTimeout })
	var calls atomic.Int32
	var cancelled atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		cookie, err := req.Cookie("agentnet_ui")
		if err != nil || cookie.Value != f.runner.token || req.Method != http.MethodPost || req.URL.Path != "/api/app/update" || req.Header.Get("Origin") != "http://"+req.Host {
			t.Error("closed app did not receive the authenticated whole-app request")
		}
		// An accepted stage must survive the readiness deadline. The former
		// requestAppUpdateOnce(wait, ...) cancelled this request while waiting.
		select {
		case <-req.Context().Done():
			cancelled.Store(true)
			return
		case <-time.After(appUpdateStartTimeout + 300*time.Millisecond):
		}
		if !f.runner.appAPI(w, req) {
			t.Error("closed app did not use the real updater handler")
		}
	}))
	f.runner.addr = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	stub := filepath.Join(t.TempDir(), "fictional-installed-app")
	// The stub publishes only its synthetic endpoint and launch marker. Its
	// interpreter is explicit; the isolated PATH cannot launch any live app.
	script := fmt.Sprintf("#!/bin/sh\numask 077\nprintf 'launched\\n' >> \"$AGENTNET_HOME/closed-app-started\"\nprintf '%%s\\n' '%s/?t=%s' > \"$AGENTNET_HOME/%s\"\n", server.URL, f.runner.token, uiURLFile)
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := secfile.Write(filepath.Join(f.home, appExeFile), []byte(stub)); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	if err := runUpdate(context.Background(), f.home, []string{"--check", protocol.Version}); !errors.Is(err, errAppUpdateNotReady) {
		t.Fatalf("closed-app check must remain read-only: %v", err)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before, after) || calls.Load() != 0 {
		t.Fatal("closed-app check launched or modified the app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := diagnosticOutput(t, func() error { return runUpdate(ctx, f.home, []string{protocol.Version}) })
	if err != nil || !strings.Contains(out, "Opening the installed AgentNet app") || !strings.Contains(out, "App and terminal command verified at "+protocol.Version) {
		t.Fatalf("closed-app update failed after accepting staging: %q %v", out, err)
	}
	if cancelled.Load() || calls.Load() != 1 {
		t.Fatalf("accepted update cancelled or repeated: cancelled=%v calls=%d", cancelled.Load(), calls.Load())
	}
	launches, err := os.ReadFile(filepath.Join(f.home, "closed-app-started"))
	if err != nil || string(launches) != "launched\n" {
		t.Fatalf("closed app not launched exactly once: %q %v", launches, err)
	}
	f.assertComplete(t)
}
