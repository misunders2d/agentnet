package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/testhub"
	_ "modernc.org/sqlite"
)

// appProc is `agentnet app` running in this test, as the shell runs it:
// events read from its stdout, its stdin written to.
type appProc struct {
	t      *testing.T
	stdin  *io.PipeWriter
	events chan appEvent
	done   chan error
	logs   chan string
	quit   sync.Once
}

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// fakeShell is a $SHELL that answers the login-shell PATH question with
// path, as a real login shell would, without reading anyone's profile.
func fakeShell(t *testing.T, path string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return ""
	}
	sh := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\necho 'profile noise'\nprintf '%s%s%s' " + pathStart + " '" + path + "' " + pathEnd + "\n"
	if err := os.WriteFile(sh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return sh
}

func startApp(t *testing.T, home string, env map[string]string) *appProc {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("SHELL", fakeShell(t, "/usr/bin"))
	for k, v := range env {
		t.Setenv(k, v)
	}
	oldAddr, oldGrace, oldLog := appFirstAddr, takeoverGrace, appLogf
	t.Cleanup(func() { appFirstAddr, takeoverGrace, appLogf = oldAddr, oldGrace, oldLog })
	if appFirstAddr == oldAddr && strings.HasSuffix(appFirstAddr, ":17443") {
		appFirstAddr = freeAddr(t) // never the real app's port
	}
	p := &appProc{t: t, events: make(chan appEvent, 16), done: make(chan error, 1), logs: make(chan string, 64)}
	appLogf = func(f string, v ...any) {
		select {
		case p.logs <- fmt.Sprintf(f, v...):
		default:
		}
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p.stdin = inW
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var e appEvent
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Errorf("not an event line: %q", sc.Text())
				continue
			}
			p.events <- e
		}
	}()
	go func() {
		err := runApp(context.Background(), home, nil, inR, outW)
		outW.Close()
		p.done <- err
	}()
	t.Cleanup(func() { p.stop() })
	return p
}

func (p *appProc) next(what string) appEvent {
	p.t.Helper()
	select {
	case e := <-p.events:
		return e
	case <-time.After(time.Minute):
		p.t.Fatalf("no %s event", what)
	}
	return appEvent{}
}

// stop asks the app to quit, as the tray's Quit does, and waits for it.
func (p *appProc) stop() error {
	p.quit.Do(func() { go func() { p.stdin.Write([]byte(appStopLine + "\n")); p.stdin.Close() }() })
	select {
	case err := <-p.done:
		p.done <- err
		return err
	case <-time.After(time.Minute):
		p.t.Fatal("the app did not stop on quit")
	}
	return nil
}

func (p *appProc) waitLog(sub string) {
	p.t.Helper()
	deadline := time.After(time.Minute)
	for {
		select {
		case l := <-p.logs:
			if strings.Contains(l, sub) {
				return
			}
		case <-deadline:
			p.t.Fatalf("never logged %q", sub)
		}
	}
}

// pageClient opens url as the app's window does: the token becomes the
// session cookie.
func pageClient(t *testing.T, url string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opening the page: %d", resp.StatusCode)
	}
	return cl, url[:strings.Index(url, "/?t=")]
}

func pageCall(t *testing.T, cl *http.Client, base, path string, body any) (int, []byte) {
	t.Helper()
	var resp *http.Response
	var err error
	if body == nil {
		resp, err = cl.Get(base + path)
	} else {
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		resp, err = cl.Do(req)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// A computer with nothing joined gets the first-run page on the app's
// address. The person's Join enrolls it under an automatic device name
// (the next one when the server says it is taken), creates their person,
// and the messenger page then serves the same address and session, from a
// daemon that holds the home: no second listener, no handoff file read,
// no switch in place for updates. Quit stops it and lets the home go.
func TestAppSetupJoinsThenServesMessenger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	base := autoDeviceName(runtime.GOOS, hasBattery())
	// Another computer already has this computer's usual name.
	other, err := client.Join(ctx, filepath.Join(t.TempDir(), "other"), testhub.BootstrapCode(t, hubDir), base)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	code, err := other.Invite(ctx, "admin", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := secfile.EnsureDir(home); err != nil {
		t.Fatal(err)
	}
	handoff := filepath.Join(home, uiHandoffFile)
	if err := secfile.Write(handoff, []byte(`{"id":"x","addr":"127.0.0.1:1","token":"`+strings.Repeat("t", 40)+`"}`)); err != nil {
		t.Fatal(err)
	}
	app := startApp(t, home, map[string]string{"AGENTNET_APP": "1", "AGENTNET_APP_EXE": "/opt/AgentNet.AppImage"})
	first := app.next("setup page")
	if first.Event != "page" || first.Mode != "setup" || !strings.HasPrefix(first.URL, "http://"+appFirstAddr+"/?t=") {
		t.Fatalf("first event %+v (want the setup page on %s)", first, appFirstAddr)
	}
	if os.Getenv("AGENTNET_APP_EXE") != "" || os.Getenv("AGENTNET_APP") != "" {
		t.Fatal("the app's variables stay in the environment its children inherit")
	}
	if data, _ := secfile.Read(filepath.Join(home, appExeFile)); strings.TrimSpace(string(data)) != "/opt/AgentNet.AppImage" {
		t.Fatalf("app-exe %q", data)
	}
	if data, _ := secfile.Read(filepath.Join(home, appUIAddrFile)); strings.TrimSpace(string(data)) != appFirstAddr {
		t.Fatalf("ui-addr %q", data)
	}
	// While joining, the home is this program's: no CLI daemon can start.
	if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
		release()
		t.Fatal("the first-run page left the home free")
	}
	cl, page := pageClient(t, first.URL)
	if code, body := pageCall(t, cl, page, "/api/setup", nil); code != 200 || !strings.Contains(string(body), `"state":"none"`) || !strings.Contains(string(body), `"device":"`+base+`"`) {
		t.Fatalf("setup state: %d %s", code, body)
	}
	if code, body := pageCall(t, cl, page, "/api/setup/inspect", map[string]string{"code": "agentnet://open#" + code}); code != 200 || !strings.Contains(string(body), `"kind":"invite"`) {
		t.Fatalf("inspect: %d %s", code, body)
	}
	if code, body := pageCall(t, cl, page, "/api/setup/join", map[string]string{"code": code, "name": " "}); code != http.StatusConflict {
		t.Fatalf("join without a name: %d %s", code, body)
	}
	if c, body := pageCall(t, cl, page, "/api/setup/join", map[string]string{"code": code, "name": "Sergey"}); c != 200 {
		t.Fatalf("join: %d %s", c, body)
	}
	second := app.next("messenger page")
	if second.Event != "page" || second.Mode != "daemon" || second.URL != first.URL {
		t.Fatalf("after the join: %+v", second)
	}
	// The same session reaches the messenger page: the app's window keeps it.
	code2, body := pageCall(t, cl, page, "/api/overview", nil)
	var o struct {
		App bool `json:"app"`
		Me  struct {
			Address string `json:"address"`
		} `json:"me"`
		Person *struct {
			Label string `json:"label"`
		} `json:"person"`
	}
	json.Unmarshal(body, &o)
	if code2 != 200 || !o.App || o.Me.Address != "admin/"+base+"-2" || o.Person == nil || o.Person.Label != "Sergey" {
		t.Fatalf("overview after the join: %d %s", code2, body)
	}
	if _, err := os.Stat(handoff); err != nil {
		t.Fatalf("the app touched the update handoff file: %v", err)
	}
	if data, _ := secfile.Read(filepath.Join(home, uiURLFile)); strings.TrimSpace(string(data)) != first.URL {
		t.Fatal("ui-url is not the app's page")
	}
	if err := app.stop(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatalf("the home is still held after quit: %v", err)
	}
	release()
	// The next start serves the messenger page at once, on the same address.
	again := startApp(t, home, nil)
	if e := again.next("page"); e.Mode != "daemon" || !strings.HasPrefix(e.URL, page+"/?t=") || e.URL == first.URL {
		t.Fatalf("restart: %+v (want a new token on %s)", e, page)
	}
}

// A computer whose membership ended gets the first-run page saying so,
// not an error and a restart loop: its device link refused (the home
// says so), or removed by its server while away (the Hub says so once the
// messenger runs). Joining waits for Start again, which keeps everything
// it had, keys and database included, in an old-<time> folder in the home
// and leaves the app's own files; a new invitation then joins afresh.
func TestAppStartsAgainWhenItsMembershipEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	admin, err := client.Join(ctx, filepath.Join(t.TempDir(), "admin"), testhub.BootstrapCode(t, hubDir), "desk")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	invite := func() string {
		t.Helper()
		code, err := admin.Invite(ctx, "admin", time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	enrolled := func(name string) string {
		t.Helper()
		home := filepath.Join(t.TempDir(), name)
		a, err := client.Join(ctx, home, invite(), name)
		if err != nil {
			t.Fatal(err)
		}
		a.Close()
		return home
	}
	startAgain := func(app *appProc, home string, e appEvent, want string) (*http.Client, string) {
		t.Helper()
		if e.Event != "page" || e.Mode != "setup" {
			t.Fatalf("%s: %+v (want the first-run page)", want, e)
		}
		cl, page := pageClient(t, e.URL)
		if code, body := pageCall(t, cl, page, "/api/setup", nil); code != 200 || !strings.Contains(string(body), `"state":"`+want+`"`) {
			t.Fatalf("%s: setup state %d %s", want, code, body)
		}
		if code, body := pageCall(t, cl, page, "/api/setup/join", map[string]string{"code": invite(), "name": "Bohdan"}); code != http.StatusConflict || !strings.Contains(string(body), "Start again") {
			t.Fatalf("%s: a join before Start again: %d %s", want, code, body)
		}
		if code, body := pageCall(t, cl, page, "/api/setup/start-again", map[string]any{}); code != 200 || !strings.Contains(string(body), `"state":"none"`) {
			t.Fatalf("%s: start again: %d %s", want, code, body)
		}
		aside, _ := filepath.Glob(filepath.Join(home, appAsidePrefix+"*"))
		if len(aside) != 1 {
			t.Fatalf("%s: set aside %v", want, aside)
		}
		for _, f := range []string{"identity.json", "agent.db"} {
			if _, err := os.Stat(filepath.Join(aside[0], f)); err != nil {
				t.Fatalf("%s: %s was not kept: %v", want, f, err)
			}
			if _, err := os.Stat(filepath.Join(home, f)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: %s is still in the home: %v", want, f, err)
			}
		}
		for _, f := range []string{appUIAddrFile, appExeFile} {
			if _, err := os.Stat(filepath.Join(home, f)); err != nil {
				t.Fatalf("%s: the app's own %s moved: %v", want, f, err)
			}
		}
		return cl, page
	}

	// Refused on the other device: the home says so before anything runs.
	refused := enrolled("tablet")
	db, err := sql.Open("sqlite", filepath.Join(refused, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO config (k, v) VALUES ('link', ?)`, `{"state":"`+client.LinkRefused+`","person":"p"}`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	app := startApp(t, refused, map[string]string{"AGENTNET_APP_EXE": "/opt/AgentNet.AppImage"})
	startAgain(app, refused, app.next("setup page"), client.EnrollRefused)
	if err := app.stop(); err != nil {
		t.Fatal(err)
	}

	// Removed by its server while the app was closed: the messenger page
	// first, then the first-run page once the Hub says so.
	removed := enrolled("gone")
	if err := admin.Revoke(ctx, "admin/gone"); err != nil {
		t.Fatal(err)
	}
	app = startApp(t, removed, map[string]string{"AGENTNET_APP_EXE": "/opt/AgentNet.AppImage"})
	e := app.next("page")
	if e.Mode == "daemon" {
		e = app.next("setup page")
	}
	cl, page := startAgain(app, removed, e, client.EnrollRemoved)
	if code, body := pageCall(t, cl, page, "/api/setup/join", map[string]string{"code": invite(), "name": "Bohdan"}); code != 200 {
		t.Fatalf("join after starting again: %d %s", code, body)
	}
	if e := app.next("messenger page"); e.Event != "page" || e.Mode != "daemon" {
		t.Fatalf("after the new join: %+v", e)
	}
	code, body := pageCall(t, cl, page, "/api/overview", nil)
	var o struct {
		Me struct {
			Address string `json:"address"`
		} `json:"me"`
	}
	json.Unmarshal(body, &o)
	if code != 200 || o.Me.Address == "" || o.Me.Address == "admin/gone" {
		t.Fatalf("overview after starting again: %d %s", code, body)
	}
}

// While another daemon (a systemd service) holds the home, the app shows
// that daemon's page and never opens the home's database: a newer schema
// there is not even noticed. A daemon that comes back within the grace
// period (a restart, an update) keeps the home, and its new page is shown;
// one that is gone for good is taken over. EOF on stdin stops the app.
func TestAppAttachedNeverOpensTheHome(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Chmod(home, 0o700)
	os.Chmod(filepath.Join(home, "agent.db"), 0o600)
	before, _ := os.ReadFile(filepath.Join(home, "agent.db"))
	lock := filepath.Join(home, "daemon.lock")
	release, err := lockfile.Acquire(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := secfile.Write(filepath.Join(home, uiURLFile), []byte("http://127.0.0.1:17443/?t=SERVICE1\n")); err != nil {
		t.Fatal(err)
	}
	takeoverGrace = 300 * time.Millisecond
	app := startApp(t, home, nil)
	if e := app.next("attached page"); e.Event != "page" || e.Mode != "attached" || e.URL != "http://127.0.0.1:17443/?t=SERVICE1" {
		t.Fatalf("attached: %+v", e)
	}
	// The service restarts: the app wakes, waits, finds it back.
	secfile.Write(filepath.Join(home, uiURLFile), []byte("http://127.0.0.1:17443/?t=SERVICE2\n"))
	release()
	app.waitLog("stopped; taking over unless it comes back")
	var back func()
	for back == nil {
		if back, err = lockfile.Acquire(lock); err != nil {
			back = nil
			time.Sleep(10 * time.Millisecond) // the app holds it for an instant before letting go
		}
	}
	if e := app.next("attached page again"); e.Mode != "attached" || e.URL != "http://127.0.0.1:17443/?t=SERVICE2" {
		t.Fatalf("after the service came back: %+v", e)
	}
	if after, _ := os.ReadFile(filepath.Join(home, "agent.db")); !bytes.Equal(before, after) {
		t.Fatal("the database was opened while another daemon held the home")
	}
	// Gone for good: the app takes the home and only then looks at it.
	back()
	if e := app.next("take-over"); e.Event != "error" || !strings.Contains(e.Text, "newer AgentNet") {
		t.Fatalf("take-over of a newer home: %+v", e)
	}
	if err := <-app.done; err == nil {
		t.Fatal("no error for a newer home")
	}
	app.done <- nil
}

func TestAppStopsWhenTheShellIsGone(t *testing.T) {
	home := t.TempDir()
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	app := startApp(t, home, nil)
	if e := app.next("event"); e.Event != "error" || !strings.Contains(e.Text, "without its page") {
		t.Fatalf("a daemon without a page: %+v", e)
	}
	app.stdin.Close() // EOF: the shell died
	select {
	case err := <-app.done:
		app.done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the app kept running without its shell")
	}
}

// The page's address is the one this home used before, else the app's
// usual one, else any free one, and is kept for the next start.
func TestAppAddressIsKept(t *testing.T) {
	home := t.TempDir()
	oldAddr := appFirstAddr
	t.Cleanup(func() { appFirstAddr = oldAddr })
	appFirstAddr = freeAddr(t)
	r := &appRunner{home: home, logf: t.Logf}
	if err := r.listen(); err != nil || r.addr != appFirstAddr {
		t.Fatalf("first address %s %v", r.addr, err)
	}
	r.close()
	kept, _ := secfile.Read(filepath.Join(home, appUIAddrFile))
	if strings.TrimSpace(string(kept)) != appFirstAddr {
		t.Fatalf("kept %q", kept)
	}
	// Another program took the usual port: the kept address still wins
	// when free; when it is taken too, any free one is used and kept.
	busy, err := net.Listen("tcp", appFirstAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	r2 := &appRunner{home: home, logf: t.Logf}
	if err := r2.listen(); err != nil || r2.addr == appFirstAddr {
		t.Fatalf("fallback %s %v", r2.addr, err)
	}
	r2.close()
	kept, _ = secfile.Read(filepath.Join(home, appUIAddrFile))
	if strings.TrimSpace(string(kept)) != r2.addr {
		t.Fatalf("fallback not kept: %q", kept)
	}
	r3 := &appRunner{home: home, logf: t.Logf}
	if err := r3.listen(); err != nil || r3.addr != r2.addr {
		t.Fatalf("restart: %s %v (want %s)", r3.addr, err, r2.addr)
	}
	r3.close()
}

// In the app, a click opens the app's window on its item (the installed
// app started with agentnet://open#…, the workspace named), the daemon is
// never switched in place for an update, and without a known app a click
// gives no command.
func TestAppRunOptions(t *testing.T) {
	r := &appRunner{exe: "/opt/AgentNet.AppImage"}
	ws := strings.Repeat("b", 32)
	for fragment, want := range map[string]string{
		"":                      "agentnet://open",
		"review":                "agentnet://open#review&workspace=" + ws,
		"conv=" + ws + ws:       "agentnet://open#conv=" + ws + ws + "&workspace=" + ws,
		"msg=" + ws + "&dir=in": "agentnet://open#msg=" + ws + "&dir=in&workspace=" + ws,
	} {
		argv := r.openPage(ws)(fragment)
		if len(argv) != 2 || argv[0] != "/opt/AgentNet.AppImage" || argv[1] != want {
			t.Errorf("open %q: %v, want %s", fragment, argv, want)
		}
	}
	if argv := (&appRunner{}).openPage(client.DefaultWorkspace)("review"); argv != nil {
		t.Fatalf("a command with no app: %v", argv)
	}
	opts := r.runOptions(nil, func(error) {})
	if ok, why := opts.CanSwitch(); ok || !strings.Contains(why, "AgentNet app") {
		t.Fatalf("CanSwitch %v %q", ok, why)
	}
	if opts.OpenPage == nil || opts.OpenConv != nil || opts.Owned == nil {
		t.Fatal("app options")
	}
}

func TestAppDeviceNames(t *testing.T) {
	for _, c := range []struct {
		goos    string
		battery bool
		want    string
	}{{"linux", true, "linux-laptop"}, {"linux", false, "linux-pc"}, {"windows", true, "windows-laptop"}, {"windows", false, "windows-pc"},
		{"darwin", true, "mac"}, {"darwin", false, "mac"}, {"freebsd", false, "computer"}} {
		if got := autoDeviceName(c.goos, c.battery); got != c.want {
			t.Errorf("%s battery=%v: %s", c.goos, c.battery, got)
		}
	}
	if got := strings.Join(nameCandidates("linux-laptop", 3), " "); got != "linux-laptop linux-laptop-2 linux-laptop-3" {
		t.Fatal(got)
	}
	long := strings.Repeat("a", 30) + "-b"
	for _, c := range nameCandidates(long, 12) {
		if len(c) > 32 {
			t.Fatalf("candidate %q too long", c)
		}
	}
	for name, want := range map[string]string{"linux-laptop": "Linux laptop", "windows-pc-2": "Windows computer", "mac": "Mac", "work-box": "work box"} {
		if got := deviceWords(name); got != want {
			t.Errorf("deviceWords(%q) = %q", name, got)
		}
	}
}

// Under an AppImage, hooks name a kept copy of the program in the home,
// refreshed only when the program changed, never the passing mount.
func TestAppStableExeForHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks are refused on Windows")
	}
	home := t.TempDir()
	old := appStable
	t.Cleanup(func() { appStable = old })
	appStable.on, appStable.home = true, home
	cmd, err := hookCommand(home, "claude")
	want := filepath.Join(home, appStableExeDir, "agentnet")
	if err != nil || !strings.HasPrefix(cmd, "'"+want+"' --home ") {
		t.Fatalf("hook command %q %v", cmd, err)
	}
	st, err := os.Stat(want)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("copy %v %v", st, err)
	}
	self, _ := os.Executable()
	if a, _ := fileSum(self); !bytes.Equal(a, mustSum(t, want)) {
		t.Fatal("the copy differs from the program")
	}
	mod := st.ModTime()
	time.Sleep(20 * time.Millisecond)
	if _, err := selfExe(); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(want); !st2.ModTime().Equal(mod) {
		t.Fatal("an unchanged program was copied again")
	}
	ext, err := renderNativeExtension(home, "pi")
	if err != nil || !strings.Contains(string(ext), want) {
		t.Fatalf("pi extension does not run the kept copy: %v", err)
	}
	h := map[string]any{"type": "command", "timeout": float64(hookTimeout), "command": cmd}
	if !isAgentNetHook(h, "claude") {
		t.Fatal("the app's own hook not recognized")
	}
}

func mustSum(t *testing.T, path string) []byte {
	t.Helper()
	s, err := fileSum(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// `agentnet ui` opens the installed app (it brings a running one to the
// front) and prints nothing; without the app it prints the address as
// before, with where to get the app.
func TestUIOpensApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	home := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	fake := filepath.Join(t.TempDir(), "AgentNet.AppImage")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$AGENTNET_HOME\" > '"+started+"'\n"), 0o700)
	if err := secfile.Write(filepath.Join(home, appExeFile), []byte(fake+"\n")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUI(context.Background(), home, nil, &out); err != nil || out.Len() != 0 {
		t.Fatalf("ui with the app: %q %v", out.String(), err)
	}
	var once sync.Once
	deadline := time.Now().Add(30 * time.Second)
	for {
		if data, err := os.ReadFile(started); err == nil && strings.TrimSpace(string(data)) == home {
			break
		}
		if time.Now().After(deadline) {
			once.Do(func() { t.Fatal("the app was not started for this home") })
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A moved app: back to the address (none runs here).
	os.Remove(fake)
	if err := runUI(context.Background(), home, nil, &out); err == nil || !strings.Contains(err.Error(), "get the AgentNet app") {
		t.Fatalf("without the app: %v", err)
	}
}

func TestUpdateRefusesBundled(t *testing.T) {
	old := bundledWith
	t.Cleanup(func() { bundledWith = old })
	bundledWith = "app"
	err := runUpdate(context.Background(), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "came with the AgentNet app") {
		t.Fatalf("update in the app's program: %v", err)
	}
	if err := runUpdate(context.Background(), t.TempDir(), []string{"--check"}); err == nil {
		t.Fatal("--check in the app's program")
	}
}

func TestAdminInviteName(t *testing.T) {
	for _, args := range [][]string{
		{"invite", "--name", "Bohdan", "bohdan"},      // --name without --link
		{"invite", "--link"},                          // neither LABEL nor --name
		{"invite", "--link", "--name", "B", "a", "b"}, // two labels
	} {
		if err := runAdmin(context.Background(), nil, args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

// The browser engine names devices as this program does (wire.mjs
// nameCandidates), on the same vectors.
func TestNameCandidatesParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	bases := []string{"linux-laptop", "android-phone", "mac", strings.Repeat("a", 30) + "-b", strings.Repeat("x", 32)}
	want := map[string][]string{}
	for _, b := range bases {
		want[b] = nameCandidates(b, appNameTries)
	}
	data, _ := json.Marshal(bases)
	wire, _ := filepath.Abs(filepath.Join("..", "..", "internal", "ui", "static", "wire.mjs"))
	script := `const { nameCandidates } = await import(process.argv[1]); const bases = JSON.parse(process.argv[2]);
console.log(JSON.stringify(Object.fromEntries(bases.map((b) => [b, nameCandidates(b, ` + fmt.Sprint(appNameTries) + `)]))));`
	out, err := exec.Command(node, "--input-type=module", "-e", script, "file://"+filepath.ToSlash(wire), string(data)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var got map[string][]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, b := range bases {
		if strings.Join(got[b], " ") != strings.Join(want[b], " ") {
			t.Errorf("%s: browser %v, Go %v", b, got[b], want[b])
		}
	}
}

// The first-run page has words for every way a membership ends, and only
// for those.
func TestSetupPageKnowsEndedStates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	setup, _ := filepath.Abs(filepath.Join("..", "..", "internal", "ui", "static", "setup.mjs"))
	script := `const { endedWords } = await import(process.argv[1]); console.log(JSON.stringify(Object.keys(endedWords).sort()));`
	out, err := exec.Command(node, "--input-type=module", "-e", script, "file://"+filepath.ToSlash(setup)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := []string{client.EnrollExpired, client.EnrollRefused, client.EnrollRemoved}
	if got := strings.TrimSpace(string(out)); got != `["`+strings.Join(want, `","`)+`"]` {
		t.Fatalf("setup.mjs ended states %s, Go %v", got, want)
	}
	for _, s := range want {
		if !client.EnrollEnded(s) {
			t.Fatalf("%s is not an ended state in Go", s)
		}
	}
}
