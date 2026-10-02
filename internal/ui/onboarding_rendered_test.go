package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

// Exercise production settings, native persistence and guarded APIs with an
// inert executable. The browser-only view tests presentation, not Engine.
func TestOnboardingRenderedNativeChoices(t *testing.T) {
	if os.Getenv("AGENTNET_ONBOARDING_RENDERED") != "1" {
		t.Skip("opt-in existing Playwright installation")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses an inert shell executable")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := filepath.Abs("testdata/onboarding_browser_check.cjs")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("AGENTNET_NOTIFY", "off")
	shots := os.Getenv("AGENTNET_SCREENSHOTS")
	if shots == "" {
		shots = t.TempDir()
	}
	if err := os.MkdirAll(shots, 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "invoked")
	t.Setenv("AGENTNET_ONBOARDING_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf invoked > \"$AGENTNET_ONBOARDING_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Availability checks see only the stand-in, never the user's programs.
	t.Setenv("PATH", bin)
	for _, width := range []int{1280, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			hubDir := filepath.Join(t.TempDir(), "hub")
			testhub.Start(t, hubDir, "127.0.0.1:0", "")
			home := filepath.Join(t.TempDir(), "native-home")
			work := filepath.Join(t.TempDir(), "synthetic-agent-onboarding-working-folder-with-a-long-unbroken-name")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hubDir), "laptop")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { a.Close() }()
			approvals, err := a.QuestionApprovals()
			if err != nil {
				t.Fatal(err)
			}
			grants, err := a.TaskGrants()
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"fresh", "manual", "selected"} {
				if phase != "fresh" {
					if err = a.Close(); err != nil {
						t.Fatal(err)
					}
					a, err = client.Open(home)
					if err != nil {
						t.Fatal(err)
					}
				}
				mux := http.NewServeMux()
				mux.HandleFunc("GET /fixture", func(w http.ResponseWriter, r *http.Request) {
					body, err := fs.ReadFile(static.Files, "default.html")
					if err != nil {
						http.Error(w, "fixture markup unavailable", http.StatusInternalServerError)
						return
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Isolated onboarding verification</title><link rel="icon" href="/favicon.ico"><link rel="stylesheet" href="/assets/app.css">`+string(body)+`<script src="/fixture-bootstrap.js"></script><script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>`)
				})
				mux.HandleFunc("GET /fixture-bootstrap.js", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/javascript")
					// Disable streams; API fetches still pass through the real guard.
					fmt.Fprint(w, `const browserFixture=new URL(location.href).searchParams.has('browser');window.agentnet={platform:browserFixture?'browser':'daemon',skins:[],onOpen:()=>{},listen:()=>()=>{},api:async(path,body)=>{const r=await fetch(path,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw Error((await r.text()).trim());const view=await r.json();if(browserFixture&&path==='/api/overview'){view.me.browser=true;delete view.me.responder;delete view.me.responder_dir;view.device={online:true,revoked:false}}return view}};`)
				})
				var server *Server
				var api http.Handler
				mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
				ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.guard(mux).ServeHTTP(w, r) }))
				server = New(NewLive(a), ts.Listener.Addr().String(), testToken)
				api = server.Handler()
				ts.Start()
				cmd := exec.CommandContext(ctx, node, runner, ts.URL, phase, fmt.Sprint(width), work, shots, testToken)
				cmd.Env = append(os.Environ(), "PATH="+originalPath)
				out, runErr := cmd.CombinedOutput()
				ts.Close()
				if runErr != nil {
					t.Fatalf("rendered %s: %v\n%s", phase, runErr, out)
				}
				var proof struct{ Pass bool }
				if err := json.Unmarshal(out, &proof); err != nil || !proof.Pass {
					t.Fatalf("missing rendered proof: %s", out)
				}
				t.Logf("rendered %s: %s", phase, out)
				nowApprovals, err := a.QuestionApprovals()
				if err != nil {
					t.Fatal(err)
				}
				nowGrants, err := a.TaskGrants()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(approvals, nowApprovals) || !reflect.DeepEqual(grants, nowGrants) {
					t.Fatal("selection mutated permissions")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("stand-in harness invoked or marker unreadable: %v", err)
				}
				chosen, err := a.ResponderChosen()
				if err != nil || !chosen {
					t.Fatalf("choice not stored: %v", err)
				}
				r, err := a.Responder()
				if err != nil {
					t.Fatal(err)
				}
				if phase == "fresh" && r != nil {
					t.Fatal("manual choice not stored")
				}
				if phase != "fresh" && (r == nil || r.Harness != "codex" || r.Dir != work) {
					t.Fatal("selected choice not stored")
				}
			}
		})
	}
}
