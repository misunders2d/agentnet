package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui"
)

func TestAssistantSetupRenderedNativeCatalogBridge(t *testing.T) {
	if os.Getenv("AGENTNET_SETUP_RENDERED") != "1" {
		t.Skip("opt-in installed Chromium/Playwright")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := filepath.Abs("../../internal/ui/testdata/assistant_setup_rendered.cjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{1440, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			c := isolatedSetup(t)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			hubDir := filepath.Join(t.TempDir(), "hub")
			testhub.Start(t, hubDir, "127.0.0.1:0", "")
			a, err := client.Join(ctx, c.home, testhub.BootstrapCode(t, hubDir), "laptop")
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			work := t.TempDir()
			contextFile := filepath.Join(work, "kept-context.md")
			if err = os.WriteFile(contextFile, []byte("Synthetic preserved context."), 0600); err != nil {
				t.Fatal(err)
			}
			if err = a.SetResponder(&client.Responder{Harness: "codex", Dir: work, Context: []string{contextFile}, Timeout: 17 * time.Second}); err != nil {
				t.Fatal(err)
			}
			before, err := a.Responder()
			if err != nil {
				t.Fatal(err)
			}
			grantsBefore, _ := a.TaskGrants()
			approvalsBefore, _ := a.QuestionApprovals()
			live := ui.NewLive(a)
			live.SetAssistantSetup(newAssistantSetup(c.home, a))
			var handler http.Handler
			mux := http.NewServeMux()
			server := httptest.NewServer(mux)
			defer server.Close()
			page := ui.New(live, strings.TrimPrefix(server.URL, "http://"), "setup-rendered-fixture")
			registry, err := client.OpenWorkspaces(c.home)
			if err != nil {
				t.Fatal(err)
			}
			providers := ui.NewWorkspaceProviders()
			memberships, _ := registry.List()
			for _, membership := range memberships {
				if _, err = providers.Bind(membership, live); err != nil {
					t.Fatal(err)
				}
			}
			providers.Rename = registry.Rename
			handler = page.WorkspaceHandler(providers)
			mux.HandleFunc("GET /fixture", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Isolated setup verification</title><link rel="icon" href="/favicon.ico"><link rel="stylesheet" href="/assets/assistant-setup.css"><link rel="stylesheet" href="/assets/workspaces.css"><style>body{margin:0;background:#fafaf9;color:#20262d;font:15px/1.55 system-ui}main{max-width:800px;padding:24px;margin:auto;box-sizing:border-box}input,select,button{font:inherit}h1{font-size:25px}*{box-sizing:border-box}</style><main><h1>Assistants</h1><div id="assistant-setup"></div></main><script type="module" src="/fixture-bootstrap.mjs"></script>`)
			})
			mux.HandleFunc("GET /fixture-bootstrap.mjs", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/javascript")
				fmt.Fprint(w, `import {mountAssistantSetup} from '/assets/assistant-setup.mjs';import {WorkspaceShell,mountWorkspaceSwitcher} from '/assets/workspaces.mjs';const browserOnly=new URLSearchParams(location.search).has('browser');if(!browserOnly){const shell=new WorkspaceShell();await shell.load();mountWorkspaceSwitcher(document.body,shell);}await mountAssistantSetup({root:document.querySelector('#assistant-setup'),isBrowser:browserOnly,api:async(path,body)=>{const r=await fetch(path,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const result=await r.json();if(!r.ok)throw Error(result.error||'Request failed');return result;}});`)
			})
			mux.Handle("/", handler)
			shots := os.Getenv("AGENTNET_SCREENSHOTS")
			if shots == "" {
				shots = t.TempDir()
			}
			if err = os.MkdirAll(shots, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, node, runner, server.URL, fmt.Sprint(width), work, shots)
			if output, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("rendered setup %v\n%s", e, output)
			} else {
				t.Log(string(output))
			}
			entries, err := a.LocalAgents()
			if err != nil || len(entries) != 3 {
				t.Fatalf("setup->named catalog/rerun: count %d %v", len(entries), err)
			}
			for _, entry := range entries {
				if entry.Responder == nil || entry.Responder.Dir != work || entry.Responder.Harness == "omp" {
					t.Fatal("invalid named assistant bridge")
				}
			}
			after, _ := a.Responder()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("setup changed default responder/context")
			}
			grantsAfter, _ := a.TaskGrants()
			approvalsAfter, _ := a.QuestionApprovals()
			if !reflect.DeepEqual(grantsBefore, grantsAfter) || !reflect.DeepEqual(approvalsBefore, approvalsAfter) {
				t.Fatal("setup changed grants")
			}
			public, err := a.PublicAgentCatalog()
			if err != nil || len(public) != 3 {
				t.Fatal("public catalog not populated")
			}
		})
	}
}
