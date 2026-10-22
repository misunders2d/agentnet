package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The same self-driving fixture runs in Chromium and in the installed native
// shell's WebKitGTK. All data and actions belong to an invented local host.
func TestUIResponsivenessRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" && os.Getenv("AGENTNET_NATIVE_SHELL") == "" && os.Getenv("AGENTNET_LATENCY_EXPORT") == "" {
		t.Skip("opt-in rendered/native latency fixture")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	conv, err := f.NewDM("vitalii/laptop")
	if err != nil {
		t.Fatal(err)
	}
	overview, _ := f.Overview()
	view, _ := f.DM(conv)
	seed, err := json.Marshal(map[string]any{"overview": overview, "view": view})
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("AGENTNET_LATENCY_EXPORT"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		fixture, err := os.ReadFile("testdata/latency_fixture.mjs")
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"latency-input.json": seed, "latency-fixture.mjs": fixture} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var server *Server
	mux := http.NewServeMux()
	var reportMu sync.Mutex
	reports := map[string][]byte{}
	mux.HandleFunc("POST /latency-report", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
	mux.HandleFunc("GET /latency-input.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(seed)
	})
	// A focused source build can qualify a correction before root regenerates
	// canonical assets. Missing fonts/data still come from the embedded package.
	if dir := os.Getenv("AGENTNET_LATENCY_ASSETS"); dir != "" {
		mux.HandleFunc("GET /assets/skins/comic/", func(w http.ResponseWriter, r *http.Request) {
			rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/assets/skins/comic/")))
			if rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				file := filepath.Join(dir, rel)
				if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
					http.ServeFile(w, r, file)
					return
				}
			}
			server.Handler().ServeHTTP(w, r)
		})
	}
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
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		sidecar := filepath.Join(t.TempDir(), "agentnet"+suffix)
		build := exec.CommandContext(t.Context(), "go", "build", "-o", sidecar, "testdata/latency_sidecar.go")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("fixture sidecar: %v\n%s", err, out)
		}
		python, err := exec.LookPath("python3")
		if err != nil {
			python, err = exec.LookPath("python")
		}
		if err != nil {
			t.Fatal("native fixture requires Python3")
		}
		suites := os.Getenv("AGENTNET_LATENCY_SUITES")
		if suites == "" {
			suites = "history,navigation,settings,unread,actions"
		}
		for _, suite := range strings.Split(suites, ",") {
			t.Run(suite, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), python, "testdata/latency_native_check.py", shell, ts.URL+"/latency?run=native-"+suite+"&suite="+suite, testToken, sidecar)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				t.Log(string(out))
			})
		}
	}
}
