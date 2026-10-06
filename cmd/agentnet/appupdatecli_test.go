package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
