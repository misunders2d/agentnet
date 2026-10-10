package core

import (
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/ui"
)

// activeSessions is called with the state mutex held. Disconnected providers
// keep their database alive until App.Close, so an admitted handler may finish.
func (a *App) activeSessions() []*Session {
	out := []*Session{}
	for id, s := range a.sessions {
		if a.active[id] {
			out = append(out, s)
		}
	}
	return out
}
func (a *App) phone(id string, s *Session) *phoneProvider {
	s.mu.Lock()
	s.notify = func(fragment string) {
		a.mu.Lock()
		n := a.notifier
		closed := a.closed
		a.mu.Unlock()
		if n != nil && !closed {
			n.Notify(id, fragment)
		}
	}
	s.mu.Unlock()
	s.live.SetApp(true)
	return &phoneProvider{Live: s.live, available: a.notificationsAvailable, address: s.a.Address}
}
func (a *App) messenger(defaultSession *Session) (handler http.Handler, failure error) {
	defer func() {
		if failure != nil {
			for _, s := range a.sessions {
				s.Close()
			}
			a.sessions = map[string]*Session{}
			a.active = map[string]bool{}
		}
	}()
	registry, err := client.OpenWorkspaces(a.home)
	if err != nil {
		return nil, err
	}
	entries, err := registry.List()
	if err != nil {
		return nil, err
	}
	set := ui.NewWorkspaceProviders()
	a.registry = registry
	a.providers = set
	a.sessions[client.DefaultWorkspace] = defaultSession
	for _, entry := range entries {
		if entry.State != "enrolled" {
			continue
		}
		s := defaultSession
		if entry.ID != client.DefaultWorkspace {
			agent, e := registry.Open(entry.ID)
			if e != nil {
				return nil, e
			}
			s, e = newSession(agent)
			if e != nil {
				return nil, e
			}
		}
		a.sessions[entry.ID] = s
		a.active[entry.ID] = true
		if _, err = set.Bind(entry, a.phone(entry.ID, s)); err != nil {
			return nil, err
		}
	}
	server := ui.New(a.phone(client.DefaultWorkspace, defaultSession), a.host, a.token)
	server.SetMobile()
	a.disconnected = server.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "Disconnected workspace", 409) }))
	set.Known = registry.List
	set.Join = func(r *http.Request, j ui.WorkspaceJoin) (client.Workspace, ui.Provider, error) {
		a.join.Lock()
		defer a.join.Unlock()
		a.lifecycle.Lock()
		defer a.lifecycle.Unlock()
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return client.Workspace{}, nil, errors.New("App closed")
		}
		entry, e := registry.Join(r.Context(), j.ID, j.Name, j.Invite, j.Agent)
		if e != nil {
			return entry, nil, e
		}
		agent, e := registry.Open(entry.ID)
		if e != nil {
			return entry, nil, e
		}
		s, e := newSession(agent)
		if e != nil {
			return entry, nil, e
		}
		a.mu.Lock()
		a.sessions[entry.ID] = s
		a.active[entry.ID] = true
		wanted := a.wanted
		a.mu.Unlock()
		if wanted {
			if e = s.Start(); e != nil {
				return entry, nil, e
			}
		}
		return entry, a.phone(entry.ID, s), nil
	}
	set.Disconnect = func(id string) error {
		a.lifecycle.Lock()
		defer a.lifecycle.Unlock()
		if e := registry.Disconnect(id); e != nil {
			return e
		}
		set.Remove(id)
		a.mu.Lock()
		a.active[id] = false
		s := a.sessions[id]
		a.mu.Unlock()
		if s != nil {
			s.Stop()
		}
		return nil
	}
	set.Reconnect = func(id string) error {
		a.lifecycle.Lock()
		defer a.lifecycle.Unlock()
		if e := registry.Reconnect(id); e != nil {
			return e
		}
		entries, e := registry.List()
		if e != nil {
			return e
		}
		var entry client.Workspace
		for _, v := range entries {
			if v.ID == id {
				entry = v
			}
		}
		a.mu.Lock()
		s := a.sessions[id]
		a.mu.Unlock()
		if s == nil {
			agent, e := registry.Open(id)
			if e != nil {
				return e
			}
			s, e = newSession(agent)
			if e != nil {
				return e
			}
		}
		if _, e = set.Bind(entry, a.phone(id, s)); e != nil {
			return e
		}
		a.mu.Lock()
		a.sessions[id] = s
		a.active[id] = true
		wanted := a.wanted
		a.mu.Unlock()
		if wanted {
			return s.Start()
		}
		return nil
	}
	set.Rename = registry.Rename
	return server.WorkspaceHandler(set), nil
}
