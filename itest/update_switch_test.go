//go:build linux

package itest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// releases stands in for the project's release page: /releases/latest
// redirects to the tag named latest; each tag serves one binary and its
// SHA256SUMS (a wrong sum when bad is set).
type releases struct {
	mu     sync.Mutex
	latest string
	bins   map[string][]byte
	bad    map[string]bool
}

func (r *releases) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset := "agentnet-" + runtime.GOOS + "-" + runtime.GOARCH
	p := req.URL.Path
	switch {
	case p == "/releases/latest":
		http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
	case strings.HasPrefix(p, "/releases/download/"):
		parts := strings.Split(strings.TrimPrefix(p, "/releases/download/"), "/")
		bin, ok := r.bins[parts[0]]
		if len(parts) != 2 || !ok {
			http.NotFound(w, req)
			return
		}
		switch parts[1] {
		case "SHA256SUMS":
			sum := sha256.Sum256(bin)
			if r.bad[parts[0]] {
				sum[0] ^= 0xff
			}
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case asset:
			w.Write(bin)
		default:
			http.NotFound(w, req)
		}
	default:
		http.NotFound(w, req)
	}
}

func (r *releases) set(latest string, bad bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latest = latest
	r.bad[latest] = bad
}

// buildVersion builds agentnet stamped as version, taking releases from base.
func buildVersion(t *testing.T, out, version, base string) []byte {
	t.Helper()
	ld := "-X github.com/misunders2d/agentnet/internal/protocol.Version=" + version + " -X main.releaseBase=" + base
	if b, err := exec.Command("go", "build", "-ldflags", ld, "-o", out, "../cmd/agentnet").CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// streamEvents follows a page's event stream and delivers event names.
func (p *page) streamEvents(ctx context.Context) <-chan string {
	p.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", p.base+"/events", nil)
	resp, err := (&http.Client{Jar: p.cl.Jar}).Do(req)
	if err != nil || resp.StatusCode != 200 {
		p.t.Fatalf("events: %v", err)
	}
	ch := make(chan string, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				ch <- v
			}
		}
	}()
	return ch
}

func pidOf(t *testing.T, c *cli, home string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "--", c.bin+" --home "+home+" daemon").Output()
	if err != nil {
		t.Fatalf("no daemon for %s", home)
	}
	pid, _ := strconv.Atoi(strings.Fields(string(out))[0])
	return pid
}

func exeOf(pid int) string {
	target, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	return target
}

func fingerprint(c *cli, home string) string {
	out := c.run("--home", home, "whoami")
	_, fp, _ := strings.Cut(out, "fingerprint ")
	return fp
}

// TestUpdateSwitchesTheRunningDaemon: one `agentnet update` from a
// development build, then from a release, installs the latest release and
// switches this home's running daemon to it: same process, same page address
// and cookie, same keys and history; a running job finishes first; a bad
// release, an equal release and a current one change nothing; another home's
// daemon and the Hub keep running the program they started with.
func TestUpdateSwitchesTheRunningDaemon(t *testing.T) {
	rel := &releases{bins: map[string][]byte{}, bad: map[string]bool{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(rel.serve))
	defer srv.Close()
	c := buildCLI(t)
	certFile := filepath.Join(c.dir, "release-ca.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	base := srv.URL + "/releases"
	builds := filepath.Join(c.dir, "builds")
	os.MkdirAll(builds, 0o700)
	dev := buildVersion(t, filepath.Join(builds, "dev"), "v9.0.0+0760ccc", base)
	for _, v := range []string{"v9.0.0", "v9.0.1", "v9.0.2", "v9.0.3"} {
		rel.bins[v] = buildVersion(t, filepath.Join(builds, v), v, base)
	}
	// Installed: the development build, in its own directory.
	bin := filepath.Join(c.dir, "bin", "agentnet")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	os.WriteFile(bin, dev, 0o755)
	c.bin = bin
	stub := filepath.Join(c.dir, "stub")
	os.MkdirAll(stub, 0o700)
	os.WriteFile(filepath.Join(stub, "claude"), []byte("#!/bin/sh\ncat > /dev/null\nsleep 3\necho 'done by the responder'\n"), 0o700)
	c.env = []string{"AGENTNET_NOTIFY=off", "SSL_CERT_FILE=" + certFile, "PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH")}

	c.setup(t, "hub")
	hubPID := func() int {
		out, _ := exec.Command("pgrep", "-f", "--", bin+" hub serve --data hub").Output()
		pid, _ := strconv.Atoi(strings.Fields(string(out))[0])
		return pid
	}()
	work := filepath.Join(c.dir, "work")
	os.MkdirAll(work, 0o700)
	c.run("--home", "bob", "responder", "set", "--harness", "claude", "--dir", work)
	c.start("alice.log", "--home", "alice", "daemon")
	c.start("bob.log", "--home", "bob", "daemon", "--ui", "127.0.0.1:0")
	alicePID, bobPID := pidOf(t, c, "alice"), pidOf(t, c, "bob")
	bp := c.openPage(t, "bob")
	urlBefore, _ := os.ReadFile(filepath.Join(c.dir, "bob", "ui-url"))
	fpBefore := fingerprint(c, "bob")
	c.run("--home", "alice", "send", "bob/desk", "history before the update")
	waitFor(t, "history", func() bool { return len(bp.overview().Threads) == 1 })

	// An equal release is not an update of a build made after it.
	rel.set("v9.0.0", false)
	if out, err := c.try("--home", "bob", "update"); err == nil || !strings.Contains(out, "not newer than v9.0.0") {
		t.Fatalf("equal release: %v\n%s", err, out)
	}
	if v := c.run("version"); !strings.HasPrefix(v, "agentnet v9.0.0+0760ccc ") {
		t.Fatalf("file changed: %s", v)
	}

	// From the development build to the latest release: one command.
	rel.set("v9.0.1", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := bp.streamEvents(ctx)
	out := c.run("--home", "bob", "update")
	if !strings.Contains(out, fmt.Sprintf("this home's daemon now runs agentnet v9.0.1 (seen: it restarted as process %d)", bobPID)) {
		t.Fatalf("update output:\n%s", out)
	}
	if !strings.Contains(out, "processes still running the previous file") || !strings.Contains(out, strconv.Itoa(alicePID)) ||
		!strings.Contains(out, strconv.Itoa(hubPID)) {
		t.Fatalf("other processes on the previous file not reported:\n%s", out)
	}
	sawRestart := false
	for ev := range events {
		sawRestart = sawRestart || ev == "restart"
	}
	if !sawRestart {
		t.Fatal("the page was not told about the switch")
	}
	if pidOf(t, c, "bob") != bobPID || exeOf(bobPID) != bin {
		t.Fatalf("bob's daemon: pid %d exe %s", pidOf(t, c, "bob"), exeOf(bobPID))
	}
	if !strings.HasSuffix(exeOf(alicePID), " (deleted)") && !strings.HasSuffix(exeOf(alicePID), ".old") {
		t.Fatalf("alice's daemon switched too: %s", exeOf(alicePID))
	}
	waitFor(t, "page back", func() bool { return bp.get("/api/overview", nil) == 200 })
	if o := bp.overview(); o.Version != "v9.0.1" || len(o.Threads) != 1 {
		t.Fatalf("after the switch: version %q, %d threads", o.Version, len(o.Threads))
	}
	if urlAfter, _ := os.ReadFile(filepath.Join(c.dir, "bob", "ui-url")); string(urlAfter) != string(urlBefore) {
		t.Fatal("the page address changed")
	}
	if fingerprint(c, "bob") != fpBefore {
		t.Fatal("bob's key changed")
	}

	// Busy: a task runs when the next release arrives; it finishes with its
	// result on the old program, one accepted meanwhile runs on the new.
	first := strings.Fields(c.run("--home", "alice", "task", "bob/desk", "first job"))[0]
	second := strings.Fields(c.run("--home", "alice", "task", "bob/desk", "second job"))[0]
	waitFor(t, "tasks arrive", func() bool { return len(c.inbox("bob")) >= 3 })
	c.run("--home", "bob", "accept", first)
	waitFor(t, "first job running", func() bool {
		for _, m := range c.inbox("bob") {
			if m.ID == first && m.State == "running" {
				return true
			}
		}
		return false
	})
	rel.set("v9.0.2", false)
	c.run("--home", "bob", "accept", second) // accepted before the switch is asked for
	out = c.run("--home", "bob", "update")
	if !strings.Contains(out, "now runs agentnet v9.0.2") {
		t.Fatalf("busy update:\n%s", out)
	}
	states := map[string]string{}
	waitFor(t, "both jobs done", func() bool {
		for _, m := range c.inbox("bob") {
			states[m.ID] = m.State
		}
		return states[first] == "answered" && states[second] == "answered"
	})
	log, _ := os.ReadFile(filepath.Join(c.dir, "bob.log"))
	if strings.Contains(string(log), "interrupted") {
		t.Fatalf("a job was interrupted:\n%s", log)
	}

	// A bad release and a current one change nothing.
	rel.set("v9.0.3", true)
	if out, err := c.try("--home", "bob", "update"); err == nil || !strings.Contains(out, "does not match the release checksum") {
		t.Fatalf("bad checksum: %v\n%s", err, out)
	}
	rel.set("v9.0.2", false)
	if out := c.run("--home", "bob", "update"); !strings.Contains(out, "already installed") {
		t.Fatalf("current:\n%s", out)
	}
	if pidOf(t, c, "bob") != bobPID || !strings.HasPrefix(c.run("version"), "agentnet v9.0.2 ") {
		t.Fatal("a refused update changed something")
	}
	if out := c.run("--home", "bob", "update", "--status"); !strings.Contains(out, "runs agentnet v9.0.2") {
		t.Fatalf("status: %s", out)
	}
}
