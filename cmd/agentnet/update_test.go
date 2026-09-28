package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestMain lets a copy of this test binary stand in for a release build of
// agentnet (on every OS, Windows included): with AGENTNET_FAKE_BINARY set,
// "version" prints the version in <its file>.fakeversion if present, else
// AGENTNET_FAKE_VERSION, and "sleep" waits.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTNET_FAKE_BINARY") != "" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			v := os.Getenv("AGENTNET_FAKE_VERSION")
			if self, err := os.Executable(); err == nil {
				if data, err := os.ReadFile(self + ".fakeversion"); err == nil {
					v = strings.TrimSpace(string(data))
				}
			}
			fmt.Printf("agentnet %s (protocol %d)\n", v, protocol.ProtocolVersion)
			if n := os.Getenv("AGENTNET_FAKE_SCHEMA"); n != "" && len(os.Args) > 2 && os.Args[2] == "--schema" {
				fmt.Printf("schema %s\n", n)
			}
		case "sleep":
			time.Sleep(30 * time.Second)
		case "spew":
			os.Stdout.Write(bytes.Repeat([]byte("x"), 3<<20))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// release is a stand-in for the project's release server.
type releaseStub struct {
	latest   string
	asset    []byte // served as this platform's asset
	sums     string // SHA256SUMS body; "" means correct for asset
	truncate bool
	redirect string // if set, the asset answers with a redirect here
}

func fakeReleaseServer(t *testing.T, r *releaseStub) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/releases/latest":
			http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
		case strings.HasSuffix(req.URL.Path, "/SHA256SUMS"):
			sums := r.sums
			if sums == "" {
				sum := sha256.Sum256(r.asset)
				sums = hex.EncodeToString(sum[:]) + "  agentnet-other-arch\n" + hex.EncodeToString(sum[:]) + "  " + assetName() + "\n"
			}
			io.WriteString(w, sums)
		case strings.HasSuffix(req.URL.Path, "/"+assetName()):
			if r.redirect != "" {
				http.Redirect(w, req, r.redirect, http.StatusFound)
				return
			}
			if r.truncate {
				w.Header().Set("Content-Length", fmt.Sprint(len(r.asset)))
				w.Write(r.asset[:len(r.asset)/2])
				return
			}
			w.Write(r.asset)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	oldBase, oldClient := releaseBase, updateClient
	releaseBase = srv.URL + "/releases"
	c := srv.Client()
	c.CheckRedirect = httpsOnly
	updateClient = c
	t.Cleanup(func() { releaseBase, updateClient = oldBase, oldClient })
}

// installed is a temporary "installed agentnet" (a copy of the fake that
// reports current) that the update replaces, with this invocation's version
// set to current. It returns the file and its identity before the update.
func installed(t *testing.T, current string) (exe string, before os.FileInfo) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "agentnet")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.WriteFile(exe, selfBytes(t), 0o755)
	os.WriteFile(exe+".fakeversion", []byte(current), 0o600)
	before, err := fileIdentity(exe)
	if err != nil {
		t.Fatal(err)
	}
	oldExe, oldVersion := executable, protocol.Version
	executable = func() (string, error) { return exe, nil }
	protocol.Version = current
	t.Cleanup(func() { executable, protocol.Version = oldExe, oldVersion })
	t.Setenv("AGENTNET_FAKE_BINARY", "1")
	return exe, before
}

// fileIdentity is the identity of the file at path now. It is read through
// an open handle: on Windows, os.Stat by path reads the identity only at the
// first os.SameFile, from whatever file the path names by then, so a
// FileInfo kept from before an update would take on the new file's identity.
func fileIdentity(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// sameFileNow reports whether two paths name the same file now, however
// they are spelled (a symlinked or short temporary directory, for example).
func sameFileNow(a, b string) bool {
	fa, err := fileIdentity(a)
	if err != nil {
		return false
	}
	fb, err := fileIdentity(b)
	return err == nil && os.SameFile(fa, fb)
}

// unchanged reports whether exe is still the file before was.
func unchanged(exe string, before os.FileInfo) bool {
	now, err := fileIdentity(exe)
	return err == nil && os.SameFile(now, before)
}

func selfBytes(t *testing.T) []byte {
	t.Helper()
	self, _ := os.Executable()
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func leftovers(t *testing.T, exe string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".agentnet-update-*"))
	return m
}

func update(t *testing.T, args ...string) error {
	t.Helper()
	return runUpdate(context.Background(), filepath.Join(t.TempDir(), "no-home"), args)
}

// The latest release (or a named one) replaces the file; the previous file
// is kept; the new one runs.
func TestUpdateInstallsRelease(t *testing.T) {
	asset := selfBytes(t)
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: asset})
	exe, before := installed(t, "v9.9.8")
	t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
	if err := update(t, "--check"); err != nil {
		t.Fatal(err)
	}
	if !unchanged(exe, before) {
		t.Fatal("--check changed the file")
	}
	if err := update(t); err != nil {
		t.Fatal(err)
	}
	if unchanged(exe, before) {
		t.Fatal("the release was not installed")
	}
	if old, err := fileIdentity(exe + ".old"); err != nil || !os.SameFile(old, before) {
		t.Fatalf("previous file not kept: %v", err)
	}
	os.Remove(exe + ".fakeversion") // the new file reports the release's version
	out, err := exec.Command(exe, "version").Output()
	if err != nil || !strings.HasPrefix(string(out), "agentnet v9.9.9 (protocol ") {
		t.Fatalf("installed program: %q %v", out, err)
	}
	if l := leftovers(t, exe); len(l) != 0 {
		t.Fatalf("staged files left: %v", l)
	}
	// Already current: nothing to do.
	protocol.Version = "v9.9.9"
	if err := update(t); err != nil {
		t.Fatal(err)
	}
}

// Refusals change nothing: an older target, a development build without a
// named release, a malformed version, and more than one argument.
func TestUpdateRefusals(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	exe, before := installed(t, "v9.9.8")
	for _, c := range []struct {
		version string
		args    []string
		want    string
	}{
		{"v9.9.8", []string{"v9.9.7"}, "downgrades are not supported"},
		{"v9.9.8", []string{"9.9.9"}, "not a release version"},
		{"v9.9.8", []string{"v9.9.9", "extra"}, "usage"},
		{"dev", nil, "development build"},
		{"v0.2.1-3-gabc", nil, "development build"},
	} {
		protocol.Version = c.version
		if err := update(t, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v", c.version, c.args, err)
		}
	}
	if !unchanged(exe, before) {
		t.Fatal("a refusal changed the file")
	}
}

// Every download or validation failure leaves the installed file as it was
// and removes the staged copy.
func TestUpdateRejectsBadDownloads(t *testing.T) {
	asset := selfBytes(t)
	sum := sha256.Sum256([]byte("something else"))
	for _, c := range []struct {
		name    string
		r       releaseStub
		version string
		want    string
	}{
		{"checksum mismatch", releaseStub{latest: "v9.9.9", asset: asset, sums: hex.EncodeToString(sum[:]) + "  " + assetName() + "\n"}, "v9.9.9", "does not match the release checksum"},
		{"no checksum line", releaseStub{latest: "v9.9.9", asset: asset, sums: "abc  agentnet-other\n"}, "v9.9.9", "has no checksum for"},
		{"truncated", releaseStub{latest: "v9.9.9", asset: asset, truncate: true}, "v9.9.9", ""},
		{"wrong version inside", releaseStub{latest: "v9.9.9", asset: asset}, "v1.0.0", "reports"},
		{"plain-HTTP redirect", releaseStub{latest: "v9.9.9", asset: asset, redirect: "http://example.invalid/asset"}, "v9.9.9", "non-HTTPS redirect"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			fakeReleaseServer(t, &r)
			exe, before := installed(t, "v9.9.8")
			t.Setenv("AGENTNET_FAKE_VERSION", c.version)
			err := update(t)
			if err == nil || (c.want != "" && !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("error: %v", err)
			}
			if !unchanged(exe, before) {
				t.Fatal("the installed file changed")
			}
			if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a failed update moved the file aside")
			}
			if l := leftovers(t, exe); len(l) != 0 {
				t.Fatalf("staged files left: %v", l)
			}
		})
	}
}

// Only one update of a file runs at a time.
func TestUpdateSerialized(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	exe, _ := installed(t, "v9.9.8")
	release, err := lockfile.Acquire(updateLockPath(exe))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := update(t); err == nil || !strings.Contains(err.Error(), "another update") {
		t.Fatalf("concurrent update: %v", err)
	}
}

// The file can be replaced while a process runs it (on Windows too, by
// renaming the running file aside); the process keeps running.
func TestReplaceRunningExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agentnet")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	self := selfBytes(t)
	os.WriteFile(exe, self, 0o755)
	cmd := exec.Command(exe, "sleep")
	cmd.Env = append(os.Environ(), "AGENTNET_FAKE_BINARY=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	staged := filepath.Join(dir, ".agentnet-update-1")
	os.WriteFile(staged, []byte("new build"), 0o755)
	if err := replaceExecutable(exe, staged); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new build" {
		t.Fatal("not replaced")
	}
	if data, _ := os.ReadFile(exe + ".old"); len(data) != len(self) {
		t.Fatal("previous file not kept")
	}
	if cmd.ProcessState != nil {
		t.Fatal("the running process was disturbed")
	}
	// A failed install puts the previous file back and leaves exe usable.
	if err := replaceExecutable(exe, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("installing a missing file succeeded")
	}
	if data, _ := os.ReadFile(exe); string(data) != "new build" {
		t.Fatalf("after a failed install: %q", data)
	}
}

// After an update, a running daemon of the home and (on Linux) every
// process still running the previous file are named; nothing is stopped.
func TestUpdateReportsRunning(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agentnet")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.WriteFile(exe, selfBytes(t), 0o755)
	cmd := exec.Command(exe, "sleep")
	cmd.Env = append(os.Environ(), "AGENTNET_FAKE_BINARY=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	staged := filepath.Join(dir, ".agentnet-update-1")
	os.WriteFile(staged, []byte("new build"), 0o755)
	if err := replaceExecutable(exe, staged); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	reportRunning(home, exe, false)
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "a daemon for "+home+" is running") {
		t.Fatalf("home daemon not reported:\n%s", out)
	}
	if runtime.GOOS == "linux" && !strings.Contains(string(out), fmt.Sprint(cmd.Process.Pid)) {
		t.Fatalf("process on the previous file not listed:\n%s", out)
	}
	if cmd.ProcessState != nil {
		t.Fatal("a process was stopped")
	}
}

// Another update replaced the file after this one decided what to install
// (from its own, now stale, version) and before it took the lock: it must
// not install over the newer file.
func TestUpdateRefusesFileChangedBeforeLock(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	exe, before := installed(t, "v9.9.8")
	t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
	// The concurrent update's result: the same file now reports v10.0.0,
	// while this invocation still runs v9.9.8.
	os.WriteFile(exe+".fakeversion", []byte("v10.0.0"), 0o600)
	err := update(t)
	if err == nil || !strings.Contains(err.Error(), "changed since this update started") || !strings.Contains(err.Error(), "v10.0.0") {
		t.Fatalf("error: %v", err)
	}
	if !unchanged(exe, before) {
		t.Fatal("installed over a newer file")
	}
	if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("moved the newer file aside")
	}
	if l := leftovers(t, exe); len(l) != 0 {
		t.Fatalf("staged files left: %v", l)
	}
}

// updateIn runs update for home, capturing what it prints.
func updateIn(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := runUpdate(context.Background(), home, args)
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), err
}

// A development build updates only to a release newer than the one it was
// made after, and only to a program that says it can open this home; an
// equal release is never called an update.
func TestUpdateFromDevelopmentBuilds(t *testing.T) {
	for _, c := range []struct {
		current, latest, schema string
		args                    []string
		want                    string // "" installs
	}{
		{"v9.9.8+0760ccc", "v9.9.8", "999", nil, "not newer than v9.9.8"},
		{"v9.9.8-28-gcc5d858-dirty", "v9.9.8", "999", nil, "not newer than v9.9.8"},
		{"v9.9.8+0760ccc", "v9.9.9", "999", []string{"v9.9.7"}, "not newer than v9.9.8"},
		{"v0.1.50-143-gcc5d858", "v9.9.9", "", nil, "does not say which home databases"},
		{"cc5d858", "v9.9.9", "999", nil, "not made from a release"},
		{"v9.9.8+0760ccc", "v9.9.9", "999", nil, ""},
		{"v9.9.8-28-gcc5d858-dirty", "v9.9.9", "999", nil, ""},
	} {
		fakeReleaseServer(t, &releaseStub{latest: c.latest, asset: selfBytes(t)})
		exe, before := installed(t, c.current)
		t.Setenv("AGENTNET_FAKE_VERSION", c.latest)
		t.Setenv("AGENTNET_FAKE_SCHEMA", c.schema)
		_, err := updateIn(t, filepath.Join(t.TempDir(), "no-home"), c.args...)
		switch {
		case c.want == "" && (err != nil || unchanged(exe, before)):
			t.Errorf("%s -> %s: not installed (%v)", c.current, c.latest, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || !unchanged(exe, before)):
			t.Errorf("%s -> %s %v: %v (changed: %v)", c.current, c.latest, c.args, err, !unchanged(exe, before))
		}
		if l := leftovers(t, exe); len(l) != 0 {
			t.Errorf("staged files left: %v", l)
		}
	}
}

// homeWithSchema makes a home whose database is at schema version n.
func homeWithSchema(t *testing.T, n int) string {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
		t.Fatal(err)
	}
	return home
}

// A program that cannot open this home's database is never installed.
func TestUpdateChecksTheHomeSchema(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	home := homeWithSchema(t, 50)
	for _, c := range []struct {
		schema string
		ok     bool
	}{{"40", false}, {"50x", false}, {"99999999", false}, {"60", true}} {
		exe, before := installed(t, "v9.9.8+0760ccc")
		t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
		t.Setenv("AGENTNET_FAKE_SCHEMA", c.schema)
		_, err := updateIn(t, home)
		if c.ok != (err == nil) || c.ok == unchanged(exe, before) {
			t.Errorf("target schema %s against 50: %v", c.schema, err)
		}
	}
	if v, _, _ := client.HomeSchema(home); v != 50 {
		t.Fatalf("the check changed the database: schema %d", v)
	}
}

// With this home's daemon running, update asks it to switch and says only
// what it has seen: done, or pending while a job runs.
func TestUpdateAsksTheDaemonToSwitch(t *testing.T) {
	fakeReleaseServer(t, &releaseStub{latest: "v9.9.9", asset: selfBytes(t)})
	old := switchWait
	switchWait = 2 * time.Second
	t.Cleanup(func() { switchWait = old })
	home := t.TempDir()
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")) // the daemon
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Pending: nobody completes the request.
	installed(t, "v9.9.8")
	t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
	out, err := updateIn(t, home)
	if err != nil || !strings.Contains(out, "switch to agentnet v9.9.9: pending") || strings.Contains(out, "now runs") {
		t.Fatalf("pending: %v\n%s", err, out)
	}
	if out, _ := updateIn(t, home, "--status"); !strings.Contains(out, "requested: pending") {
		t.Fatalf("status: %s", out)
	}

	// Done: a stand-in daemon completes the request it finds.
	os.Remove(filepath.Join(home, "update-request.json"))
	exe, _ := installed(t, "v9.9.8")
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			data, err := os.ReadFile(filepath.Join(home, "update-request.json"))
			if err != nil {
				continue
			}
			var r client.UpdateRequest
			json.Unmarshal(data, &r)
			if !sameFileNow(r.Exe, exe) || r.To != "v9.9.9" { // the request names the resolved file
				continue
			}
			os.Remove(filepath.Join(home, "update-request.json"))
			client.RecordUpdateActivation(home, client.UpdateActivation{ID: r.ID, To: r.To, Result: client.ActivationRunning, Running: r.To, PID: 4242})
			return
		}
	}()
	out, err = updateIn(t, home)
	if err != nil || !strings.Contains(out, "now runs agentnet v9.9.9 (seen: it restarted as process 4242)") {
		t.Fatalf("done: %v\n%s", err, out)
	}
	if out, _ := updateIn(t, home, "--status"); !strings.Contains(out, "last switch: at ") || !strings.Contains(out, "started as agentnet v9.9.9 (process 4242)") ||
		!strings.Contains(out, "now: a daemon is running") {
		t.Fatalf("status after: %s", out)
	}
}

// --status shows the last switch as a record, then what runs now; a record
// of a daemon that has since stopped or been replaced is never shown as the
// current state.
func TestUpdateStatusIsHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("processes are not checked on Windows")
	}
	home := t.TempDir()
	gone := exec.Command("sh", "-c", "exit 0")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	record := func(pid int) {
		client.RecordUpdateActivation(home, client.UpdateActivation{ID: "x", To: "v9.9.9", Result: client.ActivationRunning, Running: "v9.9.9", PID: pid})
	}
	status := func() string {
		out, err := updateIn(t, home, "--status")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	record(gone.Process.Pid)
	if out := status(); !strings.Contains(out, "last switch: at ") || !strings.Contains(out, "now: no daemon is running for this home") ||
		strings.Contains(out, "still alive") {
		t.Fatalf("stopped daemon: %s", out)
	}
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")) // some daemon runs now
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if out := status(); !strings.Contains(out, fmt.Sprintf("but not process %d", gone.Process.Pid)) {
		t.Fatalf("another daemon: %s", out)
	}
	record(os.Getpid())
	if out := status(); !strings.Contains(out, fmt.Sprintf("process %d is still alive", os.Getpid())) {
		t.Fatalf("same process: %s", out)
	}
	release()
	client.RequestUpdateSwitch(home, client.UpdateRequest{ID: "y", Exe: "/x/agentnet", To: "v9.9.10"})
	if out := status(); !strings.Contains(out, "no daemon is running for this home now, so it completes when the daemon starts") {
		t.Fatalf("pending without a daemon: %s", out)
	}
}
