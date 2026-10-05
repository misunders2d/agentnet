package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/ui"
)

// `agentnet app` is the AgentNet app's own program: the desktop shell
// (desktop/) starts it and shows the page it serves in the app's window.
// One loopback address per home, kept across starts, so the window's own
// storage (skin, drafts) stays; a first-run page until this computer has
// joined, then the messenger page on the same address and session; and,
// while another agentnet daemon already runs for this home (a systemd
// service, say), that daemon's page, until it stops for good.
//
// It talks to the shell in JSON lines on stdout, one per event:
//
//	{"event":"page","mode":"setup|daemon|attached","url":"http://127.0.0.1:17443/?t=…"}
//	{"event":"error","text":"…"}
//
// and stops when stdin says appStopLine or closes (the shell is gone).
// Page addresses carry the page's token: they go to the shell only, never
// to a log.

// App tunables (code constants, not settings).
const (
	appUIPort        = 17443            // the page's address the first time, if free
	appUIAddrFile    = "ui-addr"        // the address this home's app keeps using
	appExeFile       = "app-exe"        // the installed app, for `agentnet ui` and notification clicks
	appStopLine      = "quit"           // the shell's stop request on stdin
	appNameTries     = 20               // device names tried: linux-laptop, linux-laptop-2 …
	shellPathTimeout = 5 * time.Second  // reading the login shell's PATH
	appTakeoverGrace = 10 * time.Second // after another daemon let go: longer than systemd's restart delay and an update's exec
	appStableExeDir  = "bin"            // <home>/bin/agentnet: the program hooks run (appexe.go)
	appJoinTimeout   = 2 * time.Minute  // one join from the first-run page
	appStartTimeout  = 30 * time.Second // the messenger page after a join
	appAsidePrefix   = "old-"           // <home>/old-<time>: what an ended membership left, kept on Start again
)

// getAppURL is where people get the AgentNet app.
const getAppURL = repoURL + "/releases/latest"

// Test seams: the address tried when this home has none yet (tests use a
// free port, never the real one), the take-over wait, and the log.
var (
	appFirstAddr  = "127.0.0.1:" + strconv.Itoa(appUIPort)
	takeoverGrace = appTakeoverGrace
	appLogf       = log.Printf
)

// errAnotherDaemon: the home was taken by another daemon between this
// program's checks; it then shows that daemon's page.
var errAnotherDaemon = errors.New("another agentnet daemon took this home")

type appEvent struct {
	Event string `json:"event"`
	Mode  string `json:"mode,omitempty"`
	URL   string `json:"url,omitempty"`
	Text  string `json:"text,omitempty"`
}

type appRunner struct {
	home string
	exe  string // the installed app (AGENTNET_APP_EXE), "" when unknown
	logf func(string, ...any)

	outMu sync.Mutex
	out   io.Writer

	ln      net.Listener
	addr    string
	token   string
	handler atomic.Pointer[http.Handler]
	srv     *http.Server

	// ended is how the last daemon's membership ended (client.EnrollRemoved
	// …), for the next look at the home; only run uses it.
	ended string
}

// runApp runs `agentnet app` until ctx ends or the shell asks it to stop.
func runApp(ctx context.Context, home string, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) > 0 {
		return errors.New("usage: agentnet app (started by the AgentNet app; people open the app from their apps)")
	}
	// Read once and removed, so the coding agents, hooks and programs this
	// process starts never see them.
	exe := os.Getenv("AGENTNET_APP_EXE")
	os.Unsetenv("AGENTNET_APP")
	os.Unsetenv("AGENTNET_APP_EXE")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // a stop request, or the shell gone
		sc := bufio.NewScanner(stdin)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == appStopLine {
				break
			}
		}
		cancel()
	}()
	if err := secfile.EnsureDir(home); err != nil {
		return err
	}
	r := &appRunner{home: home, exe: exe, logf: appLogf, out: stdout, token: protocol.NewID() + protocol.NewID()}
	addLoginShellPath(r.logf)
	if os.Getenv("APPIMAGE") != "" || runtime.GOOS == "darwin" {
		appStable.on, appStable.home = true, home
	}
	if exe != "" {
		if err := secfile.Write(filepath.Join(home, appExeFile), []byte(exe+"\n")); err != nil {
			r.logf("the app's location could not be saved: %v", err)
		}
	}
	defer r.close()
	err := r.run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.emit(appEvent{Event: "error", Text: appWords(err)})
	}
	return err
}

// appWords is an error in the words the app's window shows.
func appWords(err error) string {
	if errors.Is(err, sqlitedb.ErrNewerSchema) {
		return "This computer's AgentNet data comes from a newer AgentNet. Get the new AgentNet app: " + getAppURL
	}
	if errors.Is(err, client.ErrRevoked) {
		return "This computer was removed from its AgentNet server. Ask your server's admin for a new invitation."
	}
	return "AgentNet could not start: " + err.Error()
}

func (r *appRunner) emit(e appEvent) {
	data, _ := json.Marshal(e)
	r.outMu.Lock()
	defer r.outMu.Unlock()
	r.out.Write(append(data, '\n'))
}

func (r *appRunner) pageURL() string { return "http://" + r.addr + "/?t=" + r.token }

// run decides the mode, in this order: another daemon holds the home
// (attached: its database is never opened here, since opening it would
// bring it to this program's schema while an older program may use it);
// nothing joined yet, or a membership that ended (setup); joined (daemon).
// A daemon whose membership the Hub ends goes back to setup.
func (r *appRunner) run(ctx context.Context) error {
	lock := filepath.Join(r.home, "daemon.lock")
	for ctx.Err() == nil {
		release, err := lockfile.Acquire(lock)
		if errors.Is(err, lockfile.ErrLocked) {
			if err := r.attached(ctx, lock); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		state, err := client.EnrollmentState(r.home) // this process holds the home
		if err != nil {
			release()
			return err
		}
		if r.ended != "" { // the Hub said so; the home alone cannot tell a removal
			if state == client.EnrollEnrolled {
				state = r.ended
			}
			r.ended = ""
		}
		var a *client.Agent
		var started chan error
		if state != client.EnrollEnrolled {
			if a, started, err = r.setup(ctx, state); err != nil {
				release()
				return err
			}
		} else if a, err = client.Open(r.home); err != nil {
			release()
			return err
		}
		// The daemon takes the home on its own lock (a new descriptor, which
		// this process's own lock would refuse): let go just before.
		release()
		err = r.daemon(ctx, a, started)
		a.Close()
		if errors.Is(err, errAnotherDaemon) {
			continue
		}
		if ended := endedState(err); ended != "" {
			// The first-run page says what happened and offers to start again.
			r.logf("this computer's AgentNet membership ended: %v", err)
			r.ended = ended
			continue
		}
		return err
	}
	return nil
}

// endedState is the state a daemon's end leaves the home in when the Hub
// ended its membership, or "".
func endedState(err error) string {
	switch {
	case errors.Is(err, client.ErrRevoked):
		return client.EnrollRemoved
	case errors.Is(err, client.ErrLinkRefused):
		return client.EnrollRefused
	case errors.Is(err, client.ErrLinkExpired):
		return client.EnrollExpired
	}
	return ""
}

// attached shows the page of the daemon that holds the home and waits, in
// the OS, until it lets the home go. A daemon that comes back within
// appTakeoverGrace (systemd restarting it, an update starting the new
// program in its place) keeps the home: its page is shown again. Otherwise
// it returns, and this program takes the home over on its next look.
func (r *appRunner) attached(ctx context.Context, lock string) error {
	for {
		r.showAttached()
		got := make(chan func(), 1)
		go func() {
			release, err := lockfile.Wait(lock)
			if err != nil {
				release = func() {}
			}
			got <- release
		}()
		select {
		case <-ctx.Done():
			go func() { (<-got)() }() // let go when the wait ends
			return nil
		case release := <-got:
			release()
		}
		r.logf("the other agentnet daemon for this home stopped; taking over unless it comes back within %s", takeoverGrace)
		t := time.NewTimer(takeoverGrace)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		release, err := lockfile.Acquire(lock)
		if errors.Is(err, lockfile.ErrLocked) {
			continue // it came back: show its page again (its token may be new)
		}
		if err != nil {
			return err
		}
		release()
		return nil
	}
}

func (r *appRunner) showAttached() {
	data, err := secfile.Read(filepath.Join(r.home, uiURLFile))
	if line := strings.TrimSpace(string(data)); err == nil && strings.HasPrefix(line, "http://") {
		r.emit(appEvent{Event: "page", Mode: "attached", URL: line})
		return
	}
	r.emit(appEvent{Event: "error", Text: "AgentNet already runs on this computer without its page (agentnet daemon). " +
		"Stop it, or start it with its page (agentnet daemon --ui 127.0.0.1:" + strconv.Itoa(appUIPort) + "); the app takes over when it stops."})
}

// listen opens the page's one listener for this process: the address
// this home used before, else appFirstAddr, else any free port; the
// address used is kept for the next start.
func (r *appRunner) listen() error {
	if r.ln != nil {
		return nil
	}
	path := filepath.Join(r.home, appUIAddrFile)
	var tries []string
	if data, err := secfile.Read(path); err == nil {
		if prev := strings.TrimSpace(string(data)); prev != "" {
			tries = append(tries, prev)
		}
	}
	tries = append(tries, appFirstAddr, "127.0.0.1:0")
	var err error
	for _, a := range tries {
		var ln net.Listener
		if ln, err = listenLoopback(a); err == nil {
			r.ln, r.addr = ln, ln.Addr().String()
			break
		}
	}
	if r.ln == nil {
		return err
	}
	if prev, _ := secfile.Read(path); strings.TrimSpace(string(prev)) != r.addr {
		if err := secfile.Write(path, []byte(r.addr+"\n")); err != nil {
			r.logf("the page's address could not be kept for the next start: %v", err)
		}
	}
	starting := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "AgentNet is starting.", http.StatusServiceUnavailable)
	}))
	r.handler.Store(&starting)
	r.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		(*r.handler.Load()).ServeHTTP(w, req)
	})}
	go func() {
		if err := r.srv.Serve(r.ln); !errors.Is(err, http.ErrServerClosed) {
			r.logf("the page stopped: %v", err)
		}
	}()
	return nil
}

func (r *appRunner) serve(h http.Handler) { r.handler.Store(&h) }

// close stops serving the page and lets its address go.
func (r *appRunner) close() {
	if r.srv != nil {
		r.srv.Close()
	}
	if r.ln != nil {
		r.ln.Close()
	}
}

// openPage opens the app's window on a page destination of workspace: the
// installed app, started with agentnet://open#fragment, hands it to the
// window it already has.
func (r *appRunner) openPage(workspace string) func(fragment string) []string {
	return func(fragment string) []string {
		if r.exe == "" {
			return nil
		}
		if fragment != "" && workspace != "" {
			fragment += "&workspace=" + url.QueryEscape(workspace)
		}
		return []string{r.exe, protocol.AppOpenURL(fragment)}
	}
}

// daemon runs a as this home's daemon with the messenger page on the
// app's address. started, from a join on the first-run page, hears when
// the page is served (or why not).
func (r *appRunner) daemon(ctx context.Context, a *client.Agent, started chan error) error {
	answer := func(err error) {
		if started != nil {
			started <- err
			started = nil
		}
	}
	if err := r.listen(); err != nil {
		answer(err)
		return err
	}
	if appStable.on {
		if exe, err := selfExe(); err == nil { // hooks and coding agents run the kept copy
			keepDaemonInstallDirOnPath(exe)
		}
	} else {
		keepDaemonInstallDirOnPath(os.Args[0])
	}
	a.Logf = r.logf
	err := a.Run(ctx, r.runOptions(a, answer))
	if errors.Is(err, client.ErrDaemonRunning) {
		answer(errAnotherDaemon)
		return errAnotherDaemon
	}
	if err == nil {
		err = ctx.Err()
	}
	answer(err)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// runOptions are the daemon's options in the app: never switched in place
// for an update (the app is updated as a whole), clicks open the app's
// window, and the messenger page takes the app's address.
func (r *appRunner) runOptions(a *client.Agent, answer func(error)) client.RunOptions {
	var opts client.RunOptions
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			opts.Executable = exe
		}
	}
	opts.CanSwitch = func() (bool, string) {
		return false, "AgentNet is updated as a whole with the AgentNet app"
	}
	opts.OpenPage = r.openPage(client.DefaultWorkspace)
	opts.Owned = func() (func(), error) {
		_, handler, stop, err := startWorkspaceUI(a, r.home, r.addr, r.token, filepath.Join(r.home, "skins"), r.openPage)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(r.home, uiURLFile)
		if err := secfile.Write(path, []byte(r.pageURL()+"\n")); err != nil {
			stop()
			return nil, err
		}
		r.serve(handler)
		r.logf("messenger page on http://%s (in the AgentNet app)", r.addr)
		r.emit(appEvent{Event: "page", Mode: "daemon", URL: r.pageURL()})
		answer(nil)
		return func() { stop(); os.Remove(path) }, nil
	}
	return opts
}

// appSetup is the first-run page's side in this program (ui.SetupProvider).
type appSetup struct {
	r      *appRunner
	ctx    context.Context
	device string
	mu     sync.Mutex // one join or start again at a time
	joined chan appJoined

	stateMu      sync.Mutex
	state        string // client.EnrollNone …
	googleDone   chan struct{}
	googleStatus ui.SetupGoogleStatus
}

type appJoined struct {
	a       *client.Agent
	started chan error
}

// setup serves the first-run page, holding the home, until the person
// joins there; it returns the joined agent and the channel the join waits
// on for the messenger page.
func (r *appRunner) setup(ctx context.Context, state string) (*client.Agent, chan error, error) {
	if err := r.listen(); err != nil {
		return nil, nil, err
	}
	s := &appSetup{r: r, ctx: ctx, state: state, device: autoDeviceName(runtime.GOOS, hasBattery()), joined: make(chan appJoined)}
	r.serve(ui.NewSetup(s, r.addr, r.token))
	r.emit(appEvent{Event: "page", Mode: "setup", URL: r.pageURL()})
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case j := <-s.joined:
		return j.a, j.started, nil
	}
}

func (s *appSetup) current() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state
}

func (s *appSetup) SetupState() ui.SetupView {
	return ui.SetupView{State: s.current(), Device: s.device, DeviceWords: deviceWords(s.device)}
}

// SetupStartAgain, on the person's click, moves what an ended membership
// left into a folder in the home (appSetAside): nothing is deleted, and
// the next join makes new keys.
func (s *appSetup) SetupStartAgain() (ui.SetupView, error) {
	if !s.mu.TryLock() {
		return ui.SetupView{}, ui.Refuse("Already joining: wait a moment.")
	}
	defer s.mu.Unlock()
	if !client.EnrollEnded(s.current()) {
		return s.SetupState(), nil
	}
	dir, err := appSetAside(s.r.home, time.Now())
	if err != nil {
		s.r.logf("starting again: %v", err)
		return ui.SetupView{}, ui.Refuse("AgentNet could not start again: " + sentenceOf(err))
	}
	s.r.logf("started again; this computer's earlier AgentNet data is kept in %s", dir)
	s.stateMu.Lock()
	s.state = client.EnrollNone
	s.stateMu.Unlock()
	return s.SetupState(), nil
}

// appSetAside moves everything in home but the app's own files
// (appKeepsOnStartAgain) into a new folder there, old-<time>, and answers
// it. Nothing is deleted; if one move fails, the moved ones go back. It
// runs only while this process holds the home and nothing has it open.
func appSetAside(home string, now time.Time) (string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, appAsidePrefix+now.UTC().Format("2006-01-02-150405"))
	dir := base
	for i := 2; ; i++ {
		if err = os.Mkdir(dir, 0o700); !errors.Is(err, os.ErrExist) {
			break
		}
		dir = base + "-" + strconv.Itoa(i)
	}
	if err == nil {
		err = secfile.EnsureDir(dir) // owner-only, on Windows too
	}
	if err != nil {
		return "", err
	}
	var moved []string
	for _, e := range entries {
		if appKeepsOnStartAgain(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(home, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				os.Rename(filepath.Join(dir, moved[i]), filepath.Join(home, moved[i]))
			}
			os.Remove(dir)
			return "", err
		}
		moved = append(moved, e.Name())
	}
	return dir, nil
}

// appKeepsOnStartAgain names the home's entries that stay when the person
// starts again: the app's own (its page's address, the installed app's
// location, the program copy hooks run, the hooks and the person's
// skins), an update's handoff, locks, and earlier set-aside folders.
// Everything else belonged to the membership that ended: keys, database,
// files.
func appKeepsOnStartAgain(name string) bool {
	switch name {
	case appUIAddrFile, appExeFile, appStableExeDir, "skins", "hooks", "update-request.json":
		return true
	}
	return strings.HasSuffix(name, ".lock") || strings.HasPrefix(name, "update-helper-") || strings.HasPrefix(name, appAsidePrefix)
}

// sentenceOf is err as the end of a sentence.
func sentenceOf(err error) string {
	t := strings.TrimSpace(err.Error())
	if !strings.HasSuffix(t, ".") {
		t += "."
	}
	return t
}

// appCode finds an invitation or a device link in what the person pasted
// or the app was opened with: the link (https://server/#code), the app's
// own link (agentnet://open#code) or the bare code.
func appCode(text string) string {
	s := strings.TrimSpace(text)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[i+1:]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = strings.TrimSpace(u)
	}
	return s
}

func (s *appSetup) SetupInspect(text string) ui.SetupInvite {
	code := appCode(text)
	if o, err := protocol.DecodeLinkOffer(code); err == nil {
		v := ui.SetupInvite{Kind: "link", Expires: time.Unix(o.Expires, 0).UTC().Format(time.RFC3339)}
		inv, err := protocol.DecodeInvite(o.Invite)
		if err != nil {
			v.Problem = "That device link is not complete. Make a new one on your other device (Your devices, Add a device)."
			return v
		}
		v.Host = hostOf(inv.Hub)
		if time.Now().Unix() >= o.Expires {
			v.Problem = "That device link expired. Make a new one on your other device (Your devices, Add a device)."
		}
		return v
	}
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		return ui.SetupInvite{Problem: "That is not a complete AgentNet invitation. Copy the whole link you were sent, or ask for a new one."}
	}
	return ui.SetupInvite{Kind: "invite", Host: hostOf(inv.Hub), Name: inv.Name, From: inv.From, Workspace: inv.Workspace}
}

func hostOf(hub string) string {
	if u, err := url.Parse(hub); err == nil && u.Host != "" {
		return u.Host
	}
	return hub
}

// SetupJoin joins with the code under an automatic device name (the next
// free one if the server says it is taken), creates the person with the
// name they gave (an invitation), and answers once the messenger page
// serves this address.
func (s *appSetup) SetupJoin(j ui.SetupJoin) (ui.SetupResult, error) {
	if !s.mu.TryLock() {
		return ui.SetupResult{}, ui.Refuse("Already joining: wait a moment.")
	}
	defer s.mu.Unlock()
	if client.EnrollEnded(s.current()) {
		return ui.SetupResult{}, ui.Refuse("Press Start again first.")
	}
	code := appCode(j.Code)
	_, linkErr := protocol.DecodeLinkOffer(code)
	link := linkErr == nil
	inv := s.SetupInspect(code)
	if inv.Problem != "" {
		return ui.SetupResult{}, ui.Refuse(inv.Problem)
	}
	name := strings.TrimSpace(j.Name)
	if !link {
		if err := protocol.ValidLabel(name); err != nil {
			return ui.SetupResult{}, ui.Refuse("Write the name people will see: up to 64 characters, letters, numbers and punctuation.")
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, appJoinTimeout)
	defer cancel()
	join := client.Join
	if link {
		join = client.JoinAndLink
	}
	var a *client.Agent
	var err error
	for _, candidate := range nameCandidates(s.device, appNameTries) {
		if a, err = join(ctx, s.r.home, code, candidate); !errors.Is(err, client.ErrAddressTaken) {
			break
		}
	}
	if err != nil {
		return ui.SetupResult{}, ui.Refuse(joinWords(err, link))
	}
	if !link {
		if _, err := a.CreatePerson(ctx, name); err != nil && !errors.Is(err, client.ErrNotPublished) {
			s.r.logf("joined, but the person was not created (%v); the page offers it again", err)
		}
	}
	return s.completeJoin(a, inv.Host)
}

// joinWords is a failed join in the person's words; nothing was enrolled.
func joinWords(err error, link bool) string {
	switch {
	case errors.Is(err, client.ErrLinkExpired):
		return "That device link expired. Make a new one on your other device."
	case errors.Is(err, client.ErrLinkStale):
		return "Your devices changed since that link was made. Make a new one on your other device."
	case errors.Is(err, client.ErrAddressTaken):
		return "This computer's usual names are all taken on that server. Ask your server's admin for help."
	case strings.Contains(err.Error(), "refused this invitation"):
		if link {
			return "That link cannot be used any more (it was used, or it expired). Make a new one on your other device."
		}
		return "That invitation cannot be used: it was used already, withdrawn or expired. Ask for a new one."
	case strings.Contains(err.Error(), "cannot reach the Hub"):
		return "The server could not be reached. Check this computer's internet connection and try again."
	}
	return fmt.Sprintf("Could not join: %v", err)
}
