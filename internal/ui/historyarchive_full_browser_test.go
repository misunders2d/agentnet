package ui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/ui/static"
)

// The engine packs full parts and imports one end to end (memory store).
func TestBrowserHistoryArchiveFullPart(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/historyarchive_full_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// The same flow in Chromium over real IndexedDB, timed, with the page's
// renderer memory: its resident size when the import starts and its
// high-water mark over the import alone (reset when the import starts). Small bodies are a typical part; large ones
// fill a part close to its 16 MiB byte bound.
func TestBrowserHistoryArchiveFullPartRealIndexedDB(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME not set")
	}
	for _, c := range []struct {
		name         string
		count, bytes int
	}{{"typical", 1100, 200}, {"near-byte-bound", 550, 22000}} {
		t.Run(c.name, func(t *testing.T) {
			result, marks, peak := runArchiveFullPage(t, chrome, c.count, c.bytes)
			t.Logf("result %v", result)
			t.Logf("renderer RSS at import start %.1f MiB, at import end %.1f MiB, import high-water %.1f MiB", float64(marks["import_start"])/1024, float64(marks["import_end"])/1024, float64(peak)/1024)
		})
	}
}

func runArchiveFullPage(t *testing.T, chrome string, count, size int) (map[string]any, map[string]int, int) {
	const module = "historyarchive_full_check.mjs"
	done := make(chan map[string]any, 1)
	var mu sync.Mutex
	marks := map[string]int{}
	var chromePID atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, fmt.Sprintf(`<script type="module">window.archiveFull={count:%d,bytes:%d};let result;try{await import('/testdata/%s');result=window.archiveFullResult}catch(e){result={error:e.stack}}await fetch('/result',{method:'POST',body:JSON.stringify(result)});</script>`, count, size, module))
		case "/mark":
			mu.Lock()
			name := r.URL.Query().Get("name")
			marks[name] = rendererKiB(int(chromePID.Load()), "VmRSS")
			if name == "import_start" { // the high-water mark then covers the import alone
				resetRendererPeak(int(chromePID.Load()))
			}
			mu.Unlock()
		case "/result":
			var result map[string]any
			if json.NewDecoder(r.Body).Decode(&result) == nil {
				done <- result
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/static/") {
				http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))).ServeHTTP(w, r)
			} else if r.URL.Path == "/testdata/"+module {
				http.ServeFile(w, r, strings.TrimPrefix(r.URL.Path, "/"))
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--disable-background-networking", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--enable-precise-memory-info", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"), server.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	chromePID.Store(int64(cmd.Process.Pid))
	stop := sync.OnceFunc(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	defer stop()
	select {
	case result := <-done:
		if result["ok"] != true {
			t.Fatalf("actual IndexedDB: %v", result)
		}
		peak := rendererKiB(cmd.Process.Pid, "VmHWM")
		mu.Lock()
		defer mu.Unlock()
		return result, marks, peak
	case <-time.After(5 * time.Minute):
		stop()
		t.Fatalf("actual IndexedDB timed out: %s", stderr.String())
	}
	return nil, nil, 0
}

// resetRendererPeak resets the renderers' VmHWM to their current size.
func resetRendererPeak(root int) {
	for _, pid := range renderers(root) {
		os.WriteFile(fmt.Sprintf("/proc/%d/clear_refs", pid), []byte("5"), 0)
	}
}

// rendererKiB reads one /proc status field (KiB) of the largest renderer
// process under root; the test page is the only page Chrome opens.
func rendererKiB(root int, field string) int {
	best := 0
	for _, pid := range renderers(root) {
		best = max(best, procStatus(pid, field))
	}
	return best
}

func renderers(root int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid := procStatus(pid, "PPid"); ppid > 0 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var out []int
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		pid := queue[0]
		queue = append(queue, children[pid]...)
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if bytes.Contains(cmdline, []byte("--type=renderer")) {
			out = append(out, pid)
		}
	}
	return out
}

func procStatus(pid int, field string) int {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer f.Close()
	for s := bufio.NewScanner(f); s.Scan(); {
		if name, value, ok := strings.Cut(s.Text(), ":"); ok && name == field {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
			return n
		}
	}
	return 0
}
