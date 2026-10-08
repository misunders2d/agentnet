package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestAppUpdateSourceMissingAPIKeepsAppUnpaused(t *testing.T) {
	home := t.TempDir()
	var events bytes.Buffer
	r := &appRunner{home: home, exe: filepath.Join(home, "AgentNet.AppImage"), addr: "127.0.0.1:17443", token: "private", out: &events}
	req := httptest.NewRequest("POST", "http://"+r.addr+"/api/app/update", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://"+r.addr)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: "private"})
	rec := httptest.NewRecorder()
	r.appAPI(rec, req)
	if rec.Code != http.StatusConflict || r.updating.Load() || events.Len() != 0 {
		t.Fatalf("missing source changed app state: status=%d updating=%v events=%s", rec.Code, r.updating.Load(), events.String())
	}
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal("update retained daemon pause lock", err)
	}
	release()
}

func TestAppUpdateSourcePreflightBeforeShutdown(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "changed"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			dir, e := os.MkdirTemp(home, "app-update-")
			if e != nil {
				t.Fatal(e)
			}
			app := filepath.Join(home, "AgentNet.AppImage")
			if kind == "directory" {
				e = os.Mkdir(app, 0700)
			} else if kind == "changed" {
				e = os.WriteFile(app, []byte("different source"), 0700)
			}
			if e != nil {
				t.Fatal(e)
			}
			payload, _ := json.Marshal(map[string]any{"app": app, "asset": filepath.Join(dir, "download"), "kind": "appimage", "version": "v0.8.9", "source_sum": strings.Repeat("0", 64)})
			plan := filepath.Join(dir, "plan.json")
			if e = secfile.Write(plan, payload); e != nil {
				t.Fatal(e)
			}
			var events bytes.Buffer
			r := &appRunner{home: home, out: &events, updateReplies: make(chan bool, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if e = r.handoffAppUpdate(ctx, "unused-helper", plan); e == nil {
				t.Fatal("unsafe source accepted")
			}
			if events.Len() != 0 {
				t.Fatalf("shutdown handoff emitted for %s source: %s", kind, events.String())
			}
		})
	}
}

type removeAppAtShutdown struct {
	path    string
	removed bool
}

func (r *removeAppAtShutdown) Read([]byte) (int, error) {
	if !r.removed {
		r.removed = true
		if e := os.Remove(r.path); e != nil {
			return 0, e
		}
	}
	return 0, io.EOF
}
func TestAppUpdateSourceLostAfterShutdownTruthfulRecovery(t *testing.T) {
	home, plan := helperUpdateFixture(t, []byte("new checked asset"))
	var p appUpdatePlan
	b, e := secfile.Read(plan)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	if e = runAppUpdateHelper(home, []string{plan}, &removeAppAtShutdown{path: p.App}); e == nil {
		t.Fatal("lost source unexpectedly updated")
	}
	result, e := readAppUpdateResult(home)
	if e != nil || result.State != "failed" || strings.Contains(result.Problem, "previous app is still available") || !strings.Contains(result.Problem, "Reinstall") {
		t.Fatalf("misleading recovery %+v %v", result, e)
	}
	if _, e = os.Stat(p.Asset); e != nil {
		t.Fatal("checked recovery asset removed", e)
	}
}
