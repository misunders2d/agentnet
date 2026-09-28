package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestMain lets a copy of this test binary stand in for a release build of
// agentnet (on every OS, Windows included): with AGENTNET_FAKE_BINARY set,
// "version" prints AGENTNET_FAKE_VERSION and "sleep" waits.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTNET_FAKE_BINARY") != "" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("agentnet %s (protocol %d)\n", os.Getenv("AGENTNET_FAKE_VERSION"), protocol.ProtocolVersion)
		case "sleep":
			time.Sleep(30 * time.Second)
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

// installed is a temporary "installed agentnet" (not the test binary) that
// the update replaces; the current version is set to current.
func installed(t *testing.T, current string) (exe string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "agentnet")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.WriteFile(exe, []byte("previous build"), 0o755)
	oldExe, oldVersion := executable, protocol.Version
	executable = func() (string, error) { return exe, nil }
	protocol.Version = current
	t.Cleanup(func() { executable, protocol.Version = oldExe, oldVersion })
	t.Setenv("AGENTNET_FAKE_BINARY", "1")
	return exe
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
	exe := installed(t, "v9.9.8")
	t.Setenv("AGENTNET_FAKE_VERSION", "v9.9.9")
	if err := update(t, "--check"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "previous build" {
		t.Fatal("--check changed the file")
	}
	if err := update(t); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); len(data) != len(asset) {
		t.Fatal("the release was not installed")
	}
	if data, _ := os.ReadFile(exe + ".old"); string(data) != "previous build" {
		t.Fatalf("previous file not kept: %q", data)
	}
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
	exe := installed(t, "v9.9.8")
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
	if data, _ := os.ReadFile(exe); string(data) != "previous build" {
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
			exe := installed(t, "v9.9.8")
			t.Setenv("AGENTNET_FAKE_VERSION", c.version)
			err := update(t)
			if err == nil || (c.want != "" && !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("error: %v", err)
			}
			if data, _ := os.ReadFile(exe); string(data) != "previous build" {
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
	exe := installed(t, "v9.9.8")
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
	reportRunning(home, exe)
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
