package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Compose existing production presenter, real Node Engine fixture and native
// daemon/Hub. Node's pinned test CA is retained; Chromium only contacts loopback
// HTTP UI, never an unverified TLS Hub or a user's browser profile.
func TestTypingCombinedRenderedLinkedEngineAndDaemon(t *testing.T) {
	if os.Getenv("AGENTNET_TYPING_COMBINED") != "1" {
		t.Skip("opt-in existing Playwright installation")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	alice := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Alice")
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob := personAgent(t, ctx, code, "desk", "Bob")
	conv, err := alice.CreateDM(ctx, bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "known shared conversation"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Bob holds DM", goHas(bob, conv, "known shared conversation", nil))

	offer, err := alice.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	link, err := protocol.DecodeLinkOffer(offer.Code)
	if err != nil {
		t.Fatal(err)
	}
	link.Invite = browserCode(t, link.Invite)
	engine := startEngineNode(t, dir)
	base := "https://" + hub.Addr
	engine.ok(map[string]any{"op": "init", "base": base})
	address := engine.ok(map[string]any{"op": "joinLink", "code": base + "/#" + link.Encode(), "name": "tablet"})["address"].(string)
	engine.ok(map[string]any{"op": "start"})
	var request client.LinkRequest
	waitFor(t, "actual linked browser-engine request", func() bool {
		requests, _ := alice.PendingLinks()
		for _, r := range requests {
			if r.State == "pending" && r.Address == address {
				request = r
			}
		}
		return request.ID != ""
	})
	if err = alice.DecideLink(ctx, request.ID, true); err != nil {
		t.Fatal(err)
	}
	engine.until("linked engine and its existing history", func() bool {
		return engine.ok(map[string]any{"op": "status"})["link"] == "linked" && dmMessage(engine, conv, "known shared conversation") != nil
	})
	engine.api("/api/dm/send", map[string]any{"conv": conv, "body": "authenticated linked-tablet message"})
	waitFor(t, "Bob admits linked tablet and current roster", goHas(bob, conv, "authenticated linked-tablet message", nil))
	view := NewLive(bob)
	waitTypingHook(t, bob, "native typing stream", func() bool {
		v, e := view.Typing(protocol.TypingScope{Conv: conv})
		return e == nil && v.Current && v.Supported
	})
	engine.until("engine typing stream", func() bool {
		v := engine.api("/api/typing?conv="+conv, nil)
		return v["current"] == true && v["supported"] == true
	})
	if err = bob.Approve(address); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	release := filepath.Join(bin, "release")
	script := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\ntouch '%s'\nwhile [ ! -f '%s' ]; do sleep 0.05; done\necho synthetic-answer\n", started, release)
	if err = os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err = bob.SetResponder(&client.Responder{Harness: "claude", Dir: bin, Timeout: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	call := func(path string, body any) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		request := map[string]any{"op": "api", "path": path}
		if body != nil {
			request["body"] = body
		}
		return engine.call(request)
	}
	markup, err := os.ReadFile("static/default.html")
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`).ReplaceAllString(string(markup), "")
	ready := make(chan struct{})
	var server *Server
	var testServer *httptest.Server
	testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ready
		server.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/sender" || r.URL.Path == "/receiver":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentNet combined typing fixture</title><link rel="icon" href="/fixture-icon.svg"><link rel="stylesheet" href="/assets/app.css">`+stripped+`<script type="module" src="/combined-bootstrap.mjs"></script>`)
			case r.URL.Path == "/fixture-icon.svg":
				w.Header().Set("Content-Type", "image/svg+xml")
				fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`)
			case r.URL.Path == "/combined-bootstrap.mjs":
				w.Header().Set("Content-Type", "text/javascript")
				config, _ := json.Marshal(map[string]string{"conv": conv, "peer": bob.Address})
				fmt.Fprintf(w, `import {mountTyping} from '/assets/typing.mjs';
const config=%s, sender=location.pathname==='/sender', prefix=sender?'/engine':'/native';
window.calls=[];window.api=async(path,body)=>{calls.push({path,body});const r=await fetch(prefix+path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined});if(!r.ok)throw new Error('API '+r.status);return r.json();};
document.querySelector('#composer').hidden=false;document.body.classList.add('show-conv');document.querySelector('#conv-name').textContent='Synthetic linked-device conversation';document.querySelector('#to-name').textContent=sender?'Bob':'Alice';
window.scope={conv:config.conv};window.remount=async()=>{window.ui=mountTyping({api,input:document.querySelector('#body'),line:document.querySelector('#typing-line'),settings:document.querySelector('#typing-settings')});await ui.setScope(scope);};await remount();
if(!sender){window.events=new EventSource('/native/events');events.addEventListener('change',()=>ui.refresh());}
window.ready=true;`, config)
			case serveBundledApp(w, r):
			case strings.HasPrefix(r.URL.Path, "/assets/"):
				server.Handler().ServeHTTP(w, r)
			case strings.HasPrefix(r.URL.Path, "/native/"):
				clone := r.Clone(r.Context())
				u := *r.URL
				u.Path = strings.TrimPrefix(u.Path, "/native")
				clone.URL = &u
				server.Handler().ServeHTTP(w, clone)
			case strings.HasPrefix(r.URL.Path, "/engine/"):
				var body any
				if r.Method == "POST" {
					if !readJSON(w, r, &body) {
						return
					}
				}
				path := strings.TrimPrefix(r.URL.RequestURI(), "/engine")
				result := call(path, body)
				if result["error"] != nil {
					http.Error(w, fmt.Sprint(result["error"]), 400)
					return
				}
				writeJSON(w, result["v"])
			case r.URL.Path == "/control/offline":
				var body struct {
					On bool `json:"on"`
				}
				if !readJSON(w, r, &body) {
					return
				}
				mu.Lock()
				result := engine.call(map[string]any{"op": "offline", "on": body.On})
				mu.Unlock()
				writeJSON(w, result)
			case r.URL.Path == "/control/run-agent":
				result := call("/api/send", map[string]any{"to": bob.Address, "kind": "question", "body": "synthetic background run"})
				writeJSON(w, result)
			case r.URL.Path == "/control/agent-started":
				_, e := os.Stat(started)
				writeJSON(w, map[string]bool{"started": e == nil})
			case r.URL.Path == "/control/release-agent":
				if err := os.WriteFile(release, nil, 0600); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				writeJSON(w, map[string]bool{"released": true})
			default:
				http.NotFound(w, r)
			}
		})).ServeHTTP(w, r)
	}))
	server = New(view, strings.TrimPrefix(testServer.URL, "http://"), testToken)
	close(ready)
	t.Cleanup(testServer.Close)
	cmd := exec.CommandContext(ctx, "node", "testdata/typing_combined_journey.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_COMBINED_URL="+testServer.URL, "AGENTNET_COMBINED_TOKEN="+testToken)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
