package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/ui/static"
)

func TestBrowserDeviceHistory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/devicehistory_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserDeviceHistoryRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "devicehistory_engine_check.mjs", "deviceHistoryResult")
}

func TestBrowserReceivePendingRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "receive_pending_check.mjs", "receivePendingResult")
}

func testOwnDeviceModuleIndexedDB(t *testing.T, module, resultField string) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME not set")
	}
	done := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<script type="module">let result;try{await import('/testdata/`+module+`');result=window.`+resultField+`}catch(e){result={error:e.stack}}await fetch('/result',{method:'POST',body:JSON.stringify(result)});</script>`)
		case "/result":
			var result map[string]any
			if json.NewDecoder(r.Body).Decode(&result) == nil {
				done <- result
			}
		default:
			if !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/testdata/"+module {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/static/") {
				http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))).ServeHTTP(w, r)
			} else {
				http.ServeFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
			}
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--disable-background-networking", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"), server.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	select {
	case result := <-done:
		if result["ok"] != true {
			t.Fatalf("actual IndexedDB: %v", result)
		}
		t.Log(result)
	case <-time.After(60 * time.Second):
		t.Fatalf("actual IndexedDB timed out: %s", stderr.String())
	}
}
