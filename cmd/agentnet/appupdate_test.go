package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAppAPIRequiresOwnHostCookieAndOrigin(t *testing.T) {
	r := &appRunner{home: t.TempDir(), addr: "127.0.0.1:17443", token: "session", exe: "/tmp/AgentNet.AppImage"}
	for _, tc := range []struct {
		name, host, cookie, origin, method, path string
		want                                     int
	}{
		{"missing cookie", "127.0.0.1:17443", "", "", "GET", "/api/app/status", 401},
		{"rebound host", "evil.test", "session", "", "GET", "/api/app/status", 401},
		{"status", "127.0.0.1:17443", "session", "", "GET", "/api/app/status", 200},
		{"cross origin", "127.0.0.1:17443", "session", "https://evil.test", "POST", "/api/app/cli", 403},
		{"no explicit replace", "127.0.0.1:17443", "session", "http://127.0.0.1:17443", "POST", "/api/app/cli", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, bytes.NewBufferString("{}"))
			req.Host = tc.host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", tc.origin)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			if !r.appAPI(rec, req) || rec.Code != tc.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestAppUpdateAppImageReplacementAndChecksumFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix replacement")
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "AgentNet.AppImage")
	asset := filepath.Join(dir, "download")
	os.WriteFile(app, []byte("previous app"), 0755)
	os.WriteFile(asset, []byte("new app"), 0755)
	sum := sha256.Sum256([]byte("new app"))
	p := appUpdatePlan{App: app, Asset: asset, Kind: "appimage", Sum: hex.EncodeToString(sum[:])}
	if err := applyAppUpdate(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(app); string(b) != "new app" {
		t.Fatal("new app missing")
	}
	if b, _ := os.ReadFile(app + ".old"); string(b) != "previous app" {
		t.Fatal("previous app not retained")
	}
	os.WriteFile(asset, []byte("tampered"), 0755)
	if err := applyAppUpdate(p); err == nil {
		t.Fatal("tampered download accepted")
	}
	if b, _ := os.ReadFile(app); string(b) != "new app" {
		t.Fatal("failure changed current app")
	}
}

func TestAppUpdateRefusesChangingLinuxPackageFormat(t *testing.T) {
	for _, app := range []string{"/usr/bin/agentnet-app", "/opt/agentnet/agentnet-app"} {
		if _, _, err := appUpdateAsset("linux", app); err == nil {
			t.Fatalf("package replaced: %s", app)
		}
	}
	if name, kind, err := appUpdateAsset("linux", "/home/a/Apps/AgentNet.AppImage"); err != nil || kind != "appimage" || name != "AgentNet-linux-x86_64.AppImage" {
		t.Fatal(name, kind, err)
	}
}

func TestAppUpdateWindowsInstallerRestoresPreviousFiles(t *testing.T) {
	oldRun := appInstallerRun
	defer func() { appInstallerRun = oldRun }()
	dir := t.TempDir()
	install := filepath.Join(dir, "install")
	backup := filepath.Join(dir, "backup")
	os.MkdirAll(install, 0700)
	app := filepath.Join(install, "agentnet-app.exe")
	cli := filepath.Join(install, "agentnet.exe")
	os.WriteFile(app, []byte("old app"), 0700)
	os.WriteFile(cli, []byte("old cli"), 0700)
	if err := copyAppTree(install, backup); err != nil {
		t.Fatal(err)
	}
	appInstallerRun = func(_ context.Context, program string, args ...string) error {
		if len(args) != 2 || args[0] != "/S" || args[1] != "/D="+install {
			t.Fatalf("installer args %q", args)
		}
		os.WriteFile(app, []byte("half installed"), 0700)
		os.WriteFile(cli, []byte("new cli"), 0700)
		return errors.New("installer failed")
	}
	if err := installWindowsApp(appUpdatePlan{App: app, Asset: filepath.Join(dir, "installer.exe"), Backup: backup}); err == nil {
		t.Fatal("failure not reported")
	}
	if b, _ := os.ReadFile(app); string(b) != "old app" {
		t.Fatalf("old app lost: %q", b)
	}
	if b, _ := os.ReadFile(cli); string(b) != "old cli" {
		t.Fatal("old cli lost")
	}
}

// A silent NSIS installer exits 0 when Windows or security software keeps
// it from writing a program (a colleague's PC, November 2, 2026). Exit 0 is
// not success: the missing or unreplaced program is named and the previous
// app comes back.
func TestAppUpdateWindowsInstallerExitZeroChecksBothPrograms(t *testing.T) {
	oldRun := appInstallerRun
	defer func() { appInstallerRun = oldRun }()
	for _, c := range []struct {
		name, want string
		install    func(app, cli string)
	}{
		{"app removed", "did not write agentnet-app.exe", func(app, cli string) { os.Remove(app); os.WriteFile(cli, []byte("new cli"), 0700) }},
		{"nothing written", "did not replace agentnet-app.exe", func(app, cli string) {}},
		{"only the command", "did not replace agentnet-app.exe", func(app, cli string) { os.WriteFile(cli, []byte("new cli"), 0700) }},
		{"only the app", "did not replace agentnet.exe", func(app, cli string) { os.WriteFile(app, []byte("new app"), 0700) }},
		{"command emptied", "did not write agentnet.exe", func(app, cli string) { os.WriteFile(app, []byte("new app"), 0700); os.WriteFile(cli, nil, 0700) }},
		{"both replaced", "", func(app, cli string) {
			os.WriteFile(app, []byte("new app"), 0700)
			os.WriteFile(cli, []byte("new cli"), 0700)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			install, backup := filepath.Join(dir, "install"), filepath.Join(dir, "backup")
			os.MkdirAll(install, 0700)
			app, cli := filepath.Join(install, "agentnet-app.exe"), filepath.Join(install, "agentnet.exe")
			os.WriteFile(app, []byte("old app"), 0700)
			os.WriteFile(cli, []byte("old cli"), 0700)
			if err := copyAppTree(install, backup); err != nil {
				t.Fatal(err)
			}
			appInstallerRun = func(context.Context, string, ...string) error { c.install(app, cli); return nil }
			err := installWindowsApp(appUpdatePlan{App: app, Asset: filepath.Join(dir, "installer.exe"), Backup: backup})
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("exit 0 with %s reported %v", c.name, err)
			}
			for path, old := range map[string]string{app: "old app", cli: "old cli"} {
				if b, _ := os.ReadFile(path); string(b) != old {
					t.Fatalf("%s not restored: %q", filepath.Base(path), b)
				}
			}
			if problem := installedWindowsApp(context.Background(), appUpdatePlan{App: app, Backup: filepath.Join(dir, "none")}); problem != nil {
				t.Fatal(problem)
			}
			// The reason the helper records names the program.
			c.install(app, cli)
			if problem := installedWindowsApp(context.Background(), appUpdatePlan{App: app, Backup: backup}); problem == nil || !strings.Contains(problem.Error(), c.want) {
				t.Fatalf("reason %v, want %q", problem, c.want)
			}
		})
	}
}

// Restoring waits out a file Windows or security software holds briefly,
// within a bound, as Telegram Desktop's updater does.
func TestCopyAppTreeRetriesHeldFile(t *testing.T) {
	oldOpen, oldTries, oldPause := appOpenWrite, appCopyTries, appCopyPause
	defer func() { appOpenWrite, appCopyTries, appCopyPause = oldOpen, oldTries, oldPause }()
	appCopyTries, appCopyPause = 5, time.Millisecond
	src, dst := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "agentnet-app.exe"), []byte("previous app"), 0700)
	refuse := 0
	appOpenWrite = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if refuse > 0 {
			refuse--
			return nil, &os.PathError{Op: "open", Path: name, Err: errors.New("Access is denied.")}
		}
		return os.OpenFile(name, flag, perm)
	}
	refuse = 4
	if err := copyAppTree(src, dst); err != nil {
		t.Fatal("held four times, then free:", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "agentnet-app.exe")); string(b) != "previous app" {
		t.Fatalf("copied %q", b)
	}
	refuse = 5
	if err := copyAppTree(src, t.TempDir()); err == nil || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatal("held past the bound must fail with its reason:", err)
	}
	if refuse != 0 {
		t.Fatalf("%d tries left unused", refuse)
	}
}

// Earlier attempts' folders (download, helper, previous app) go when a new
// update is prepared; a recent one and one whose helper still runs stay.
func TestRemoveOldAppUpdates(t *testing.T) {
	home := t.TempDir()
	exe := "agentnet"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	old := time.Now().Add(-time.Hour)
	folder := func(name string, running bool, at time.Time) string {
		dir := filepath.Join(home, name)
		os.MkdirAll(filepath.Join(dir, "previous-app"), 0700)
		os.WriteFile(filepath.Join(dir, "previous-app", "agentnet-app.exe"), []byte("app"), 0600)
		os.MkdirAll(filepath.Join(dir, appStableExeDir), 0700)
		helper := filepath.Join(dir, appStableExeDir, exe)
		if running {
			// Stands in for a running program: removing it fails.
			os.MkdirAll(filepath.Join(helper, "busy"), 0700)
		} else {
			os.WriteFile(helper, []byte("helper"), 0700)
		}
		os.Chtimes(dir, at, at)
		return dir
	}
	finished := folder("app-update-1", false, old)
	empty := filepath.Join(home, "app-update-2")
	os.Mkdir(empty, 0700)
	os.Chtimes(empty, old, old)
	recent := folder("app-update-3", false, time.Now())
	running := folder("app-update-4", true, old)
	other := folder("update-helper-5", false, old)
	removeOldAppUpdates(home)
	for dir, want := range map[string]bool{finished: false, empty: false, recent: true, running: true, other: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Fatalf("%s kept=%v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	if _, err := os.Stat(filepath.Join(running, "previous-app", "agentnet-app.exe")); err != nil {
		t.Fatal("a running attempt's backup was touched:", err)
	}
}

func TestAppUpdateLinuxPackageCancelledKeepsRunningApp(t *testing.T) {
	oldRun := appInstallerRun
	defer func() { appInstallerRun = oldRun }()
	dir := t.TempDir()
	app := filepath.Join(dir, "agentnet-app")
	asset := filepath.Join(dir, "new.deb")
	os.WriteFile(app, []byte("old app"), 0700)
	os.WriteFile(asset, []byte("verified package"), 0600)
	sum := sha256.Sum256([]byte("verified package"))
	calls := 0
	appInstallerRun = func(_ context.Context, program string, args ...string) error {
		calls++
		if program != "/usr/bin/pkexec" || len(args) != 3 || args[0] != "/usr/bin/dpkg" || args[1] != "--install" {
			t.Fatalf("unexpected installer: %s %q", program, args)
		}
		return errors.New("authorization cancelled")
	}
	if err := installLinuxPackage(appUpdatePlan{App: app, Asset: asset, Sum: hex.EncodeToString(sum[:]), Kind: "deb", Backup: "old.deb"}); err == nil {
		t.Fatal("cancel not reported")
	}
	if calls != 1 {
		t.Fatal("cancel must not ask for authorization again")
	}
	if b, _ := os.ReadFile(app); string(b) != "old app" {
		t.Fatal("current app changed")
	}
}

func TestAppUpdateLinuxPackageRollsBackPartialInstall(t *testing.T) {
	oldRun := appInstallerRun
	defer func() { appInstallerRun = oldRun }()
	dir := t.TempDir()
	app := filepath.Join(dir, "agentnet-app")
	asset := filepath.Join(dir, "new.rpm")
	os.WriteFile(app, []byte("old app"), 0700)
	os.WriteFile(asset, []byte("verified package"), 0600)
	sum := sha256.Sum256([]byte("verified package"))
	calls := 0
	appInstallerRun = func(_ context.Context, program string, args ...string) error {
		calls++
		if program != "/usr/bin/pkexec" || args[0] != "/usr/bin/rpm" {
			t.Fatalf("unexpected installer: %s %q", program, args)
		}
		if calls == 1 {
			os.WriteFile(app, []byte("partial install"), 0700)
			return errors.New("install failed")
		}
		if args[len(args)-1] != "old.rpm" {
			t.Fatalf("recovery package %q", args)
		}
		os.WriteFile(app, []byte("old app"), 0700)
		return nil
	}
	if err := installLinuxPackage(appUpdatePlan{App: app, Asset: asset, Sum: hex.EncodeToString(sum[:]), Kind: "rpm", Backup: "old.rpm"}); err == nil {
		t.Fatal("failure not reported")
	}
	if calls != 2 {
		t.Fatal("previous package not reinstalled")
	}
	if b, _ := os.ReadFile(app); string(b) != "old app" {
		t.Fatal("current app not recovered")
	}
}

func TestAppUpdateStagingOnlyEmitsVerifiedCompleteHandoff(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AppImage fixture")
	}
	oldBase, oldClient, oldVersion := releaseBase, updateClient, protocol.Version
	defer func() { releaseBase, updateClient, protocol.Version = oldBase, oldClient, oldVersion }()
	protocol.Version = "v0.8.0"
	asset := []byte("new AppImage")
	sum := sha256.Sum256(asset)
	valid := true
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", server.URL+"/releases/tag/v0.8.1")
			w.WriteHeader(302)
		case "/releases/download/v0.8.1/SHA256SUMS":
			fmt.Fprintf(w, "%x  AgentNet-linux-x86_64.AppImage\n", sum)
		case "/releases/download/v0.8.1/AgentNet-linux-x86_64.AppImage":
			if valid {
				w.Write(asset)
			} else {
				w.Write([]byte("broken download"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	releaseBase, updateClient = server.URL+"/releases", server.Client()
	home := t.TempDir()
	app := filepath.Join(home, "AgentNet.AppImage")
	os.WriteFile(app, []byte("working app"), 0700)
	r := &appRunner{home: home, exe: app}
	helper, plan, err := r.stageAppUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(helper); err != nil || st.Mode().Perm()&0100 == 0 {
		t.Fatalf("helper is not ready %v", err)
	}
	b, err := secfile.Read(plan)
	if err != nil {
		t.Fatal(err)
	}
	var p appUpdatePlan
	if err = json.Unmarshal(b, &p); err != nil || p.Version != "v0.8.1" || p.Sum != hex.EncodeToString(sum[:]) {
		t.Fatalf("plan %s %v", b, err)
	}
	if b, _ := os.ReadFile(app); string(b) != "working app" {
		t.Fatal("staging changed current app")
	}
	os.RemoveAll(filepath.Dir(plan))
	valid = false
	if _, _, err = r.stageAppUpdate(context.Background()); err == nil {
		t.Fatal("bad download staged")
	}
	entries, _ := os.ReadDir(home)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "app-update-") {
			t.Fatalf("failure left staging %s", entry.Name())
		}
	}
}

func TestAppUpdateHandoffFailureKeepsAppAndPermitsRetry(t *testing.T) {
	home := t.TempDir()
	dir, err := os.MkdirTemp(home, "app-update-")
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "plan.json")
	app := filepath.Join(home, "AgentNet.AppImage")
	if err = os.WriteFile(app, []byte("current app"), 0700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(appUpdatePlan{App: app})
	if err = secfile.Write(plan, payload); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	r := &appRunner{home: home, out: writer, updateReplies: make(chan bool, 1)}
	r.updating.Store(true)
	done := make(chan error, 1)
	go func() {
		err := r.handoffAppUpdate(t.Context(), "helper", plan)
		// A failed preflight emits no event. Propagate that error to the
		// reader instead of leaving the shell fixture waiting forever.
		writer.CloseWithError(err)
		done <- err
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil || !strings.Contains(line, `"event":"update"`) {
		t.Fatalf("handoff %q %v", line, err)
	}
	r.updateReplies <- false
	if err = <-done; err == nil {
		t.Fatal("shell failure not reported")
	}
	if r.updating.Load() {
		t.Fatal("failed handoff remains busy")
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed handoff leaves staged files")
	}
}
