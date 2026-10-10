// Package core binds the existing AgentNet client to a native mobile UI.
// It owns no listener, alternate protocol, or harness configuration.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/ui"
)

// Listener receives event-driven invalidations. Implementations must return
// promptly and marshal UI work onto their platform's UI thread.
type Listener interface{ OnChange() }

// Session owns one app-private home and at most one transport run.
type Session struct {
	data        sync.RWMutex
	lifecycle   sync.Mutex
	mu          sync.Mutex
	a           *client.Agent
	live        *ui.Live
	listener    Listener
	notify      func(string)
	cancel      context.CancelFunc
	done        chan struct{}
	closed      bool
	lastError   string
	eventCancel context.CancelFunc
	wake        chan struct{}
	run         func(context.Context) error
}

func checkHome(home string) error {
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("mobile home must be an absolute app-private path")
	}
	return nil
}

// Open exposes saved local data immediately, without starting a network stream.
// The caller supplies a directory inside Android's application-private storage.
func Open(home string) (*Session, error) {
	if err := checkHome(home); err != nil {
		return nil, err
	}
	a, err := client.Open(home)
	if err != nil {
		return nil, err
	}
	return newSession(a)
}

// Link enrolls a new human device using an existing device's link code. Approval
// waits on Start's single stream; enrollment itself has a bounded network wait.
func Link(home, code, deviceName string) (*Session, error) {
	if err := checkHome(home); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := client.JoinAndLink(ctx, home, code, deviceName)
	if err != nil {
		return nil, err
	}
	return newSession(a)
}

func humanHome(a *client.Agent) error {
	r, err := a.Responder()
	if err != nil {
		return err
	}
	if r != nil {
		return errors.New("mobile home contains a responder; use a separately linked human device")
	}
	agents, err := a.LocalAgents()
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent.Responder != nil {
			return errors.New("mobile home contains a local agent responder")
		}
	}
	bindings, err := a.ReplyReceiverBindings()
	if err != nil {
		return err
	}
	if len(bindings) != 0 {
		return errors.New("mobile home contains native reply receiver bindings")
	}
	return nil
}

func newSession(a *client.Agent) (*Session, error) {
	if err := humanHome(a); err != nil {
		a.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{a: a, live: ui.NewLive(a), eventCancel: cancel, wake: make(chan struct{}, 1)}
	s.run = func(ctx context.Context) error {
		return a.Run(ctx, client.RunOptions{HumanOnly: true, Notify: func(fragment string) {
			s.mu.Lock()
			fn := s.notify
			s.mu.Unlock()
			if fn != nil {
				fn(fragment)
			}
		}})
	}
	go s.events(ctx)
	return s, nil
}

func (s *Session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Session) events(ctx context.Context) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		_, changed := s.a.Changed()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-s.wake:
		}
		s.mu.Lock()
		listener := s.listener
		closed := s.closed
		s.mu.Unlock()
		if !closed && listener != nil {
			listener.OnChange()
		}
	}
}

// SetListener replaces the invalidation listener; nil detaches it. A fresh
// listener receives an initial event, so it can render saved offline data.
func (s *Session) SetListener(listener Listener) {
	s.mu.Lock()
	if !s.closed {
		s.listener = listener
	}
	s.mu.Unlock()
	s.signal()
}

// Start starts one existing client push stream. Repeated calls are idempotent.
func (s *Session) Start() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("mobile session is closed")
	}
	if s.done != nil {
		select {
		case <-s.done:
			s.done = nil
		default:
			return nil
		}
	}
	if err := humanHome(s.a); err != nil {
		s.lastError = err.Error()
		s.a.NoteChange()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel, s.done, s.lastError = cancel, done, ""
	go func() {
		err := s.run(ctx)
		s.mu.Lock()
		if err != nil && ctx.Err() == nil {
			s.lastError = err.Error()
		}
		s.mu.Unlock()
		close(done)
		s.a.NoteChange() // invalidate the served Comic page as well as native listeners
		s.signal()
	}()
	s.signal()
	return nil
}

func (s *Session) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.mu.Lock()
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if done != nil {
		s.signal()
	}
}

// Stop cancels and joins transport, including pending device-link waits, while
// keeping the database available for offline reads and queued sends.
func (s *Session) Stop() { s.lifecycle.Lock(); defer s.lifecycle.Unlock(); s.stop() }

// Close stops transport and releases the home. It is safe to call repeatedly.
func (s *Session) Close() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.stop()
	s.data.Lock()
	defer s.data.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.listener = nil
	s.eventCancel()
	s.a.Close()
}

func marshal(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	return string(b), err
}
func (s *Session) available() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("mobile session is closed")
	}
	return nil
}
func (s *Session) OverviewJSON() (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	return marshal(s.live.Overview())
}
func (s *Session) ConversationJSON(id string) (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	return marshal(s.live.DM(id))
}
func (s *Session) ThreadJSON(id string) (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	return marshal(s.live.Thread(id))
}
func (s *Session) SendDMJSON(draft string) (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	var d ui.DMDraft
	if err := json.Unmarshal([]byte(draft), &d); err != nil {
		return "", err
	}
	if len(d.Files) != 0 || d.ReplyReceiver != nil {
		return "", errors.New("native file staging and reply receivers are unavailable")
	}
	return marshal(s.live.SendDM(d))
}
func (s *Session) SendJSON(draft string) (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	var d ui.Draft
	if err := json.Unmarshal([]byte(draft), &d); err != nil {
		return "", err
	}
	if len(d.Files) != 0 || d.ReplyReceiver != nil {
		return "", errors.New("native file staging and reply receivers are unavailable")
	}
	return marshal(s.live.Send(d))
}

// StatusJSON reports local lifecycle and the existing client's device-link state.
func (s *Session) StatusJSON() (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running := false
	if s.done != nil {
		select {
		case <-s.done:
		default:
			running = true
		}
	}
	connection := s.a.MemberView()
	return marshal(struct {
		Running    bool              `json:"running"`
		Error      string            `json:"error,omitempty"`
		Link       client.LinkStatus `json:"link"`
		Connected  bool              `json:"connected"`
		Connection client.MemberView `json:"connection"`
	}{running, s.lastError, s.a.LinkState(), connection.Current, connection}, nil)
}

// MarkReadJSON marks only the supplied message IDs as read; it exposes no
// grant, trust, execution, or general action interface.
func (s *Session) MarkReadJSON(idsJSON string) (string, error) {
	s.data.RLock()
	defer s.data.RUnlock()
	if err := s.available(); err != nil {
		return "", err
	}
	var ids []string
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return "", err
	}
	note, err := s.live.Act(ui.Action{Do: ui.DoRead, IDs: ids})
	return marshal(struct {
		Note string `json:"note"`
	}{note}, err)
}
