package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

// App owns an authenticated loopback UI independently of Hub transport.
// URL contains a secret: callers must never log, share, or open it externally.
type App struct {
	setup             *appSetup
	disconnected      http.Handler
	sessions          map[string]*Session
	active            map[string]bool
	registry          *client.Workspaces
	providers         *ui.WorkspaceProviders
	lifecycle         sync.Mutex
	closeDone         chan struct{}
	mu                sync.Mutex
	join              sync.Mutex
	home, host, token string
	port              int
	session           *Session
	handler           http.Handler
	server            *http.Server
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	requests          sync.WaitGroup
	closed            bool
	wanted            bool
	notifier          Notifier
	unread            map[string]workspaceUnread
	unreadSink        UnreadSink
	unreadGeneration  uint64
	unreadDirty       bool
	unreadFlushing    bool
	unreadLast        unreadDelivery
}

// OpenApp binds exactly preferredPort, or chooses a port when it is zero.
// Persist Port in app-private preferences; an occupied saved port fails closed.
func OpenApp(home string, preferredPort int) (*App, error) {
	if err := checkHome(home); err != nil {
		return nil, err
	}
	if preferredPort < 0 || preferredPort > 65535 {
		return nil, errors.New("invalid UI port")
	}
	state, err := client.EnrollmentState(home)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	if err != nil {
		return nil, fmt.Errorf("cannot bind saved UI port: %w", err)
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{home: home, host: l.Addr().String(), port: l.Addr().(*net.TCPAddr).Port, token: base64.RawURLEncoding.EncodeToString(secret[:]), ctx: ctx, cancel: cancel, done: make(chan struct{}), closeDone: make(chan struct{}), sessions: map[string]*Session{}, active: map[string]bool{}}
	a.setup = &appSetup{App: a}
	if state == client.EnrollEnrolled {
		s, e := Open(home)
		if e != nil {
			cancel()
			l.Close()
			return nil, e
		}
		a.session = s
		a.mu.Lock()
		a.handler, e = a.messenger(s)
		a.mu.Unlock()
		if e != nil {
			s.Close()
			cancel()
			l.Close()
			return nil, e
		}
	} else {
		a.handler = ui.NewMobileSetup(a.setup, a.host, a.token)
	}
	a.server = &http.Server{Handler: http.HandlerFunc(a.serve), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { defer close(a.done); _ = a.server.Serve(l) }()
	return a, nil
}
func (a *App) URL() string    { return a.Origin() + "/?t=" + a.token }
func (a *App) Origin() string { return "http://" + a.host }
func (a *App) Port() int      { return a.port }
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		http.Error(w, "App closed", 503)
		return
	}
	h := a.handler
	if a.registry != nil && !a.active[client.DefaultWorkspace] && (r.URL.Path == "/events" || (strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/workspaces"))) {
		h = a.disconnected
	}
	a.requests.Add(1)
	a.mu.Unlock()
	defer a.requests.Done()
	h.ServeHTTP(w, r)
}
func (a *App) Start() error {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("App closed")
	}
	a.wanted = true
	sessions := a.activeSessions()
	a.mu.Unlock()
	for _, s := range sessions {
		if err := s.Start(); err != nil {
			return err
		}
	}
	return nil
}

// NetworkChanged refreshes wanted remote sessions without replacing the host.
func (a *App) NetworkChanged() {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	if a.closed || !a.wanted {
		a.mu.Unlock()
		return
	}
	sessions := a.activeSessions()
	a.mu.Unlock()
	for _, s := range sessions {
		s.NetworkChanged()
	}
}

func (a *App) Stop() {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	a.wanted = false
	sessions := a.activeSessions()
	a.mu.Unlock()
	for _, s := range sessions {
		s.Stop()
	}
}
func (a *App) Close() {
	a.mu.Lock()
	if a.closed {
		done := a.closeDone
		a.mu.Unlock()
		<-done
		return
	}
	a.closed = true
	clear(a.unread)
	a.markUnreadDirtyLocked()
	a.cancel()
	a.mu.Unlock()
	defer close(a.closeDone)
	_ = a.server.Close()
	<-a.done
	a.requests.Wait()
	a.join.Lock()
	defer a.join.Unlock()
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	sessions := []*Session{}
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	if len(sessions) == 0 && a.session != nil {
		sessions = append(sessions, a.session)
	}
	a.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}
func (a *appSetup) SetupState() ui.SetupView {
	state, err := client.EnrollmentState(a.home)
	if err != nil {
		state = "incomplete"
	}
	if state == client.EnrollNone {
		state = "none"
	}
	return ui.SetupView{State: state, Device: "android-phone", DeviceWords: "Android phone"}
}
func (a *appSetup) SetupInspect(code string) ui.SetupInvite {
	if o, err := protocol.DecodeLinkOffer(code); err == nil {
		inv, _ := protocol.DecodeInvite(o.Invite)
		v := ui.SetupInvite{Kind: "link", Host: hubHost(inv.Hub), Expires: time.Unix(o.Expires, 0).UTC().Format(time.RFC3339)}
		if time.Now().Unix() >= o.Expires {
			v.Problem = "That device link expired. Make a new one on your other device."
		}
		return v
	}
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		return ui.SetupInvite{Problem: "Copy the complete AgentNet invitation or device link."}
	}
	return ui.SetupInvite{Kind: "invite", Host: hubHost(inv.Hub), Name: inv.Name, From: inv.From, Workspace: inv.Workspace}
}
func hubHost(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		return u.Host
	}
	return raw
}
func (a *appSetup) SetupJoin(j ui.SetupJoin) (ui.SetupResult, error) {
	if !a.join.TryLock() {
		return ui.SetupResult{}, ui.Refuse("Already joining. Wait a moment.")
	}
	defer a.join.Unlock()
	a.mu.Lock()
	closed, existing := a.closed, a.session != nil
	a.mu.Unlock()
	if closed || existing {
		return ui.SetupResult{}, ui.Refuse("This app cannot join now.")
	}
	state, stateErr := client.EnrollmentState(a.home)
	if stateErr != nil {
		return ui.SetupResult{}, stateErr
	}
	if client.EnrollEnded(state) {
		return ui.SetupResult{}, ui.Refuse("This enrollment ended. Preserve saved data before setting up again.")
	}
	v := a.SetupInspect(j.Code)
	if v.Problem != "" {
		return ui.SetupResult{}, ui.Refuse(v.Problem)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	join := client.Join
	if v.Kind == "link" {
		join = client.JoinAndLink
	} else if err := protocol.ValidLabel(strings.TrimSpace(j.Name)); err != nil {
		return ui.SetupResult{}, ui.Refuse("Write your name first.")
	}
	var agent *client.Agent
	var err error
	for i := 0; i < 8; i++ {
		name := "android-phone"
		if i > 0 {
			name = fmt.Sprintf("android-phone-%d", i+1)
		}
		agent, err = join(ctx, a.home, j.Code, name)
		if !errors.Is(err, client.ErrAddressTaken) {
			break
		}
	}
	if err != nil {
		return ui.SetupResult{}, ui.Refuse(err.Error())
	}
	if v.Kind != "link" {
		if _, err = agent.CreatePerson(ctx, strings.TrimSpace(j.Name)); err != nil && !errors.Is(err, client.ErrNotPublished) {
			agent.Close()
			return ui.SetupResult{}, ui.Refuse(err.Error())
		}
	}
	s, err := newSession(agent)
	if err != nil {
		return ui.SetupResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		s.Close()
		return ui.SetupResult{}, errors.New("App closed")
	}
	a.session = s
	handler, e := a.messenger(s)
	if e != nil {
		s.Close()
		return ui.SetupResult{}, e
	}
	a.handler = handler
	if a.wanted {
		if err = s.Start(); err != nil {
			return ui.SetupResult{}, err
		}
	}
	result := ui.SetupResult{Host: v.Host}
	if v.Kind == "link" {
		result.Waiting = agent.LinkState().Approver
	}
	return result, nil
}

// Notifier receives bounded locally decrypted display text and an exact destination.
// The native implementation must redact the public lock-screen presentation.
// Implementations must return promptly and marshal OS work onto their own lane.
type Notifier interface {
	Notify(workspace, fragment, title, body, metadata string)
	Dismiss(workspace, fragment, expectedInstance string)
}

func (a *App) SetNotifier(n Notifier) { a.mu.Lock(); a.notifier = n; a.mu.Unlock() }

// UnreadSink receives the shared cached alert count. Partial means at least one
// workspace is unknown; its last known contribution is retained. Like
// Notifier, implementations return promptly and marshal OS work to their lane.
// Close sends (0, true). This optional interface leaves Notifier unchanged.
type UnreadSink interface {
	Unread(total int64, partial bool)
}

type workspaceUnread struct {
	total int64 // last known value, initially absent/zero
	known bool
}
type unreadDelivery struct {
	generation    uint64
	total         int64
	partial, sent bool
}

// SetUnreadSink replaces the optional observer. Reentrant replacement is safe;
// an already reserved callback may finish, but no further old calls are queued.
func (a *App) SetUnreadSink(sink UnreadSink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.unreadSink = sink
	a.unreadGeneration++
	a.markUnreadDirtyLocked()
}

func (a *App) unreadTotalLocked() (int64, bool) {
	if a.closed {
		return 0, true
	}
	var total int64
	partial, anyActive := false, false
	for id := range a.sessions {
		if !a.active[id] {
			continue
		}
		anyActive = true
		v := a.unread[id]
		partial = partial || !v.known
		if v.total > math.MaxInt64-total {
			total = math.MaxInt64
		} else {
			total += v.total
		}
	}
	return total, partial || !anyActive
}

// One ephemeral flusher coalesces all workspace producers and sink changes.
// The reservation under mu is the callback start; foreign code runs lock-free.
func (a *App) markUnreadDirtyLocked() {
	a.unreadDirty = true
	if !a.unreadFlushing && a.unreadSink != nil {
		a.unreadFlushing = true
		go a.flushUnread()
	}
}
func (a *App) flushUnread() {
	for {
		a.mu.Lock()
		if !a.unreadDirty || a.unreadSink == nil {
			a.unreadFlushing = false
			a.mu.Unlock()
			return
		}
		a.unreadDirty = false
		total, partial := a.unreadTotalLocked()
		next := unreadDelivery{a.unreadGeneration, total, partial, true}
		sink := a.unreadSink
		duplicate := next == a.unreadLast
		if !duplicate {
			a.unreadLast = next
		}
		a.mu.Unlock()
		if !duplicate {
			sink.Unread(total, partial)
		}
	}
}

// Called with mu held at each binding; reads run on the existing Session lane.
func (a *App) bindUnreadLocked(id string) {
	if a.closed {
		return
	}
	if a.unread == nil {
		a.unread = map[string]workspaceUnread{}
	}
	a.unread[id] = workspaceUnread{}
	a.markUnreadDirtyLocked()
}

func (a *App) refreshUnread(id string, s *Session, content, generation uint64, read func() (ui.Overview, error)) (ui.Overview, bool) {
	current := func() bool {
		if !a.phoneCurrentLocked(id, s, generation) {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		revision, _ := s.a.ViewChanges()
		return revision.Content == content
	}
	a.mu.Lock()
	valid := current()
	a.mu.Unlock()
	if !valid {
		return ui.Overview{}, false
	}
	// Use exactly the page's Live and cache; no second unread scan or transport.
	view, err := read()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !current() {
		return ui.Overview{}, false
	}
	v := a.unread[id]
	v.known = err == nil && view.AlertTotal != nil && *view.AlertTotal >= 0
	if v.known {
		v.total = int64(*view.AlertTotal)
	}
	a.unread[id] = v
	a.markUnreadDirtyLocked()
	return view, err == nil
}
func (a *App) notificationsAvailable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.notifier != nil
}
func (a *App) deliverNotification(fragment string) {
	a.mu.Lock()
	n := a.notifier
	closed := a.closed
	a.mu.Unlock()
	if n != nil && !closed {
		_, metadata := newNoticeMetadata(client.DefaultWorkspace, client.NativeNotification{Fragment: fragment})
		n.Notify(client.DefaultWorkspace, fragment, "AgentNet", "New activity", metadata)
	}
}

// appSetup keeps the rich UI provider API out of gomobile exported types.
type appSetup struct{ *App }
