package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Keep the requesting terminal and the app's bundled command distinct. No
// fixture executable is run: the legacy ownership record proves the old CLI.
type globalUpdateFixture struct {
	home, user, terminal, canonical, bundle string
	runner                                  *appRunner
	server                                  *httptest.Server
	gets, posts                             atomic.Int32
}

func newGlobalUpdateFixture(t *testing.T) *globalUpdateFixture {
	t.Helper()
	f := &globalUpdateFixture{home: t.TempDir(), user: t.TempDir()}
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	f.terminal = filepath.Join(t.TempDir(), name)
	f.bundle = filepath.Join(t.TempDir(), name)
	f.canonical = appCommandPath(f.user)
	if f.canonical == "" {
		f.canonical = f.bundle
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(f.terminal, []byte("previous official CLI"))
	write(f.canonical, []byte("previous official CLI"))
	write(f.bundle, []byte("current app bundled CLI"))
	sum, err := fileSum(f.terminal)
	if err != nil {
		t.Fatal(err)
	}
	oldSum := hex.EncodeToString(sum)
	record := appCommandRecord{Path: f.terminal, Sum: oldSum}
	if f.canonical != f.bundle {
		record.Targets = []appCommandTarget{{Path: f.canonical, Sum: oldSum}}
	}
	b, _ := json.Marshal(record)
	if err := secfile.Write(filepath.Join(f.home, "app-command.json"), b); err != nil {
		t.Fatal(err)
	}
	previousExe, previousBundle := executable, appCommandExecutable
	previousVersion, previousBundled, previousStable := protocol.Version, bundledWith, appStable
	executable = func() (string, error) { return f.terminal, nil }
	appCommandExecutable = func() (string, error) { return f.bundle, nil }
	protocol.Version, bundledWith = "v1.2.3", ""
	appStable.on, appStable.home = false, ""
	t.Cleanup(func() {
		executable, appCommandExecutable = previousExe, previousBundle
		protocol.Version, bundledWith, appStable = previousVersion, previousBundled, previousStable
	})
	t.Setenv("HOME", f.user)
	t.Setenv("USERPROFILE", f.user)
	t.Setenv("PATH", filepath.Dir(f.terminal))
	app := filepath.Join(t.TempDir(), "AgentNet.AppImage")
	if runtime.GOOS == "darwin" {
		app = filepath.Join(t.TempDir(), "AgentNet.app", "Contents", "MacOS", "agentnet-app")
	} else if runtime.GOOS == "windows" {
		app = filepath.Join(t.TempDir(), "agentnet-app.exe")
	}
	// Registration is intentionally nonexistent: even a connection-failure
	// regression cannot launch a desktop program or shell process.
	if err := secfile.Write(filepath.Join(f.home, appExeFile), []byte(app)); err != nil {
		t.Fatal(err)
	}
	f.runner = &appRunner{home: f.home, exe: app, token: "fictional-fixture-session"}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			f.gets.Add(1)
		} else {
			f.posts.Add(1)
		}
		cookie, err := req.Cookie("agentnet_ui")
		if err != nil || cookie.Value != f.runner.token || req.Host != f.runner.addr || (req.Method == http.MethodPost && req.Header.Get("Origin") != "http://"+f.runner.addr) {
			t.Error("terminal did not authenticate to the actual app handler")
		}
		if !f.runner.appAPI(w, req) {
			t.Error("request did not use the app updater API")
			http.NotFound(w, req)
		}
	}))
	f.runner.addr = f.server.Listener.Addr().String()
	f.server.Start()
	t.Cleanup(f.server.Close)
	if err := secfile.Write(filepath.Join(f.home, uiURLFile), []byte(f.server.URL+"/?t="+f.runner.token)); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *globalUpdateFixture) aboutUpdate(t *testing.T, tag string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"version": tag})
	req := httptest.NewRequest(http.MethodPost, f.server.URL+"/api/app/update", bytes.NewReader(b))
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: f.runner.token})
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !f.runner.appAPI(w, req) {
		t.Fatal("About did not reach the app updater")
	}
	return w
}

func (f *globalUpdateFixture) assertComplete(t *testing.T) {
	t.Helper()
	want, err := os.ReadFile(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.canonical, f.terminal} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("app/terminal parity missing at %s: %q %v", path, got, err)
		}
	}
	if status := f.runner.currentCommandStatus(); status.State != "installed" {
		t.Fatalf("completion lacks fresh command proof: %+v", status)
	}
	result, err := readAppUpdateResult(f.home)
	if err != nil || result.State != "complete" || result.Version != protocol.Version {
		t.Fatalf("unverified result: %+v %v", result, err)
	}
}

// Snapshot all fixture-owned files, including ownership, shell profiles and
// update results. Read-only routes must not add, rewrite or remove any of them.
func (f *globalUpdateFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{f.home, f.user, filepath.Dir(f.terminal), filepath.Dir(f.bundle)} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err == nil {
				out[path] = string(b)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestGlobalUpdateStandaloneRoutesToActualAppAndRepairsBothCommands(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{protocol.Version}) })
	if err != nil || out != "App and terminal command verified at v1.2.3.\n" {
		t.Fatalf("global update did not give verified plain-language completion: %q %v", out, err)
	}
	if f.posts.Load() != 1 || f.gets.Load() != 0 {
		t.Fatalf("wrong app route: POST=%d GET=%d", f.posts.Load(), f.gets.Load())
	}
	f.assertComplete(t)
}

func TestGlobalUpdateAboutRepairsOldTerminalWithCurrentApp(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	w := f.aboutUpdate(t, protocol.Version)
	var reply struct {
		State, Version string
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.State != "complete" || reply.Version != protocol.Version {
		t.Fatalf("About repair failed: %d %s", w.Code, w.Body)
	}
	f.assertComplete(t)
}

func TestGlobalUpdateCheckAndStatusAreReadOnly(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	if err := writeAppUpdateResult(f.home, protocol.Version, "pending", "Awaiting verification."); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	for _, args := range [][]string{{"--check", protocol.Version}, {"--status"}} {
		out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, args) })
		if err != nil || strings.Contains(out, "verified at") || strings.Contains(out, "standalone CLI") {
			t.Fatalf("incorrect read-only global result: %q %v", out, err)
		}
		if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
			t.Fatalf("%v changed fixture bytes or ownership", args)
		}
	}
	if f.posts.Load() != 0 || f.gets.Load() != 1 {
		t.Fatalf("read-only routes installed commands: POST=%d GET=%d", f.posts.Load(), f.gets.Load())
	}
}

func TestGlobalUpdateUnsupportedAppCannotFallBackOrClaimCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installer does not distinguish package formats by app path")
	}
	f := newGlobalUpdateFixture(t)
	f.runner.exe = filepath.Join(t.TempDir(), "unpackaged-app")
	before := f.snapshot(t)
	out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{protocol.Version}) })
	if err == nil || strings.Contains(out, "verified") || strings.Contains(out, "standalone CLI") {
		t.Fatalf("unsupported app claimed or fell back to an update: %q %v", out, err)
	}
	for _, path := range []string{f.terminal, f.canonical} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != before[path] {
			t.Fatalf("unsupported app replaced command %s: %q %v", path, got, err)
		}
	}
}

func TestGlobalUpdateUnavailableRegisteredAppNeverUpdatesStandalone(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	f.server.Close()
	before := f.snapshot(t)
	out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{protocol.Version}) })
	if err == nil || strings.Contains(out, "standalone CLI") || strings.Contains(out, "verified") {
		t.Fatalf("unavailable registered app did not fail closed: %q %v", out, err)
	}
	for _, path := range []string{f.terminal, f.canonical, f.bundle} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != before[path] {
			t.Fatalf("unavailable app replaced %s: %q %v", path, got, err)
		}
	}
}

func TestGlobalUpdateNamedCompletionMustMatchRequestedVersion(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	f.server.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"complete","version":"v1.2.4"}`))
	}))
	defer server.Close()
	if err := secfile.Write(filepath.Join(f.home, uiURLFile), []byte(server.URL+"/?t=fictional-fixture-session")); err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{protocol.Version}) })
	if err == nil || !strings.Contains(err.Error(), "requested version") || strings.Contains(out, "verified") {
		t.Fatalf("mismatching completion accepted: %q %v", out, err)
	}
}

func TestGlobalUpdatePrivateStableCopyMismatchPreventsVerification(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	appStable.on, appStable.home = true, f.home
	w := f.aboutUpdate(t, protocol.Version)
	if w.Code != http.StatusOK {
		t.Fatalf("initial stable repair: %d %s", w.Code, w.Body)
	}
	f.assertComplete(t)
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	private := filepath.Join(f.home, appStableExeDir, name)
	if err := os.WriteFile(private, []byte("stale private connected-tool command"), 0700); err != nil {
		t.Fatal(err)
	}
	status := f.runner.currentCommandStatus()
	if status.State != "error" || status.Path != private {
		t.Fatalf("private copy mismatch claimed parity: %+v", status)
	}
	check, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{"--check", protocol.Version}) })
	if err != nil || !strings.Contains(check, "connected tools does not match") || strings.Contains(check, "Terminal command matches") {
		t.Fatalf("public status missed private command mismatch: %q %v", check, err)
	}
	f.runner.confirmAppUpdate()
	result, err := readAppUpdateResult(f.home)
	if err != nil || result.State != "partial" {
		t.Fatalf("private mismatch retained verified completion: %+v %v", result, err)
	}
	out, err := diagnosticOutput(t, func() error { return runUpdate(context.Background(), f.home, []string{"--status"}) })
	if err != nil || !strings.Contains(out, "incomplete") || strings.Contains(out, "Updated app and CLI") {
		t.Fatalf("private mismatch reported success: %q %v", out, err)
	}
}
