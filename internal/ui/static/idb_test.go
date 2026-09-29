//go:build linux

package static

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// idbCheck runs in a real browser: the device store's IndexedDB writes are
// all or nothing, keep a CryptoKey (the probe), and page in key order.
const idbCheck = `<!doctype html><meta charset="utf-8"><script type="module">
import { openIDB, probeStore } from "/engine.mjs";
const out = {};
try {
  const name = "agentnet-check-" + Date.now();
  const st = await openIDB(name);
  await probeStore(st);
  out.probe = true;
  await st.write([{ s: "kv", k: "kept", v: 1 }]);
  try {
    await st.write([{ s: "kv", k: "first", v: "rolled back" }, { s: "kv", k: "kept", v: 2 }, { s: "kv", k: "second", v: () => {} }]);
  } catch (e) { out.error = e.name; }
  out.first = (await st.get("kv", "first")) ?? null;
  out.kept = (await st.get("kv", "kept")) ?? null;
  await st.write(["c", "a", "d", "b"].map((k) => ({ s: "held", k, v: { id: k } })));
  out.pages = [];
  for (let pos = "", page; (page = await st.after("held", pos, 2)).length; pos = page[page.length - 1].id) out.pages.push(page.map((v) => v.id).join(""));
  st.close();
  indexedDB.deleteDatabase(name);
} catch (e) { out.fail = String(e && e.message || e); }
await fetch("/result", { method: "POST", body: JSON.stringify(out) });
</script>`

// The store against real IndexedDB, in Chrome or Chromium on Linux named
// by AGENTNET_CHROME (opt-in: AGENTNET_CHROME=google-chrome-stable go test
// ./internal/ui/static). The page is served on loopback http, which a
// browser treats as a secure context.
func TestDeviceStoreInChrome(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME is not set")
	}
	result := make(chan []byte, 1)
	files := http.FileServerFS(Files)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, idbCheck)
		case "/result":
			data, _ := io.ReadAll(r.Body)
			select {
			case result <- data:
			default:
			}
		default:
			files.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	// Chrome runs in its own process group (the command may be a wrapper
	// script), and the whole group is stopped, so no helper process still
	// writes to the profile when it is removed.
	profile, err := os.MkdirTemp("", "agentnet-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profile, srv.URL+"/")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		group := -cmd.Process.Pid
		syscall.Kill(group, syscall.SIGTERM)
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			syscall.Kill(group, syscall.SIGKILL)
			<-exited
		}
		for i := 0; i < 50 && syscall.Kill(group, 0) == nil; i++ { // helpers still stopping
			time.Sleep(100 * time.Millisecond)
		}
		os.RemoveAll(profile)
	}()
	var got struct {
		Probe bool     `json:"probe"`
		Error string   `json:"error"`
		First any      `json:"first"`
		Kept  any      `json:"kept"`
		Pages []string `json:"pages"`
		Fail  string   `json:"fail"`
	}
	select {
	case data := <-result:
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("no result from the browser")
	}
	if got.Fail != "" || !got.Probe {
		t.Fatalf("store: %+v", got)
	}
	// A value that cannot be stored fails the write, and nothing of it is
	// kept: not the value before it, not the change to a kept one.
	if got.Error != "DataCloneError" || got.First != nil || got.Kept != float64(1) {
		t.Fatalf("a failed write kept part of itself: %+v", got)
	}
	if len(got.Pages) != 2 || got.Pages[0] != "ab" || got.Pages[1] != "cd" {
		t.Fatalf("pages: %v", got.Pages)
	}
}
