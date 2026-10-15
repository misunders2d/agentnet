package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The same self-driving fixture runs in Chromium and in the installed native
// shell's WebKitGTK. All data and actions belong to an invented local host.
func TestUIResponsivenessRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" && os.Getenv("AGENTNET_NATIVE_SHELL") == "" {
		t.Skip("opt-in rendered/native latency fixture")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewDM("vitalii/laptop"); err != nil {
		t.Fatal(err)
	}
	var server *Server
	mux := http.NewServeMux()
	var reportMu sync.Mutex
	reports := map[string][]byte{}
	mux.HandleFunc("POST /latency-report", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
		reportMu.Lock()
		reports[r.URL.Query().Get("run")] = b
		reportMu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /latency-result", func(w http.ResponseWriter, r *http.Request) {
		reportMu.Lock()
		b := reports[r.URL.Query().Get("run")]
		reportMu.Unlock()
		if b != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		} else {
			w.WriteHeader(204)
		}
	})
	mux.HandleFunc("GET /latency", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") == testToken {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: testToken, Path: "/", HttpOnly: true})
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>AgentNet responsiveness fixture</title><style>html,body,#fixture{margin:0;width:100%;height:100%}</style><div id="fixture"></div><script type="module" src="/latency-check.js"></script>`))
	})
	mux.HandleFunc("GET /latency-check.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFile(w, r, "testdata/latency_fixture.mjs")
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	server = New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	if os.Getenv("AGENTNET_PLAYWRIGHT") != "" {
		cmd := exec.CommandContext(t.Context(), "node", "testdata/latency_rendered_check.cjs")
		evidence := os.Getenv("AGENTNET_LATENCY_EVIDENCE")
		if evidence == "" {
			evidence = filepath.Join(t.TempDir(), "latency.png")
		}
		cmd.Env = append(os.Environ(), "LATENCY_URL="+ts.URL+"/latency?run=chromium", "LATENCY_TOKEN="+testToken, "LATENCY_EVIDENCE="+evidence)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		t.Log(string(out))
	}
	if shell := os.Getenv("AGENTNET_NATIVE_SHELL"); shell != "" {
		cmd := exec.CommandContext(t.Context(), "/usr/bin/python3", "testdata/latency_native_check.py", shell, ts.URL+"/latency?run=native", testToken)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		t.Log(string(out))
	}
}
