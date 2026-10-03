package ui

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/misunders2d/agentnet/internal/client"
)

// WorkspaceRuntime owns additional homes only. The existing default daemon
// remains the bootstrap caller's responsibility. Run acquires each home lock
// before NewLive may clean staging or expose any provider.
type WorkspaceRuntime struct {
	registry      *client.Workspaces
	providers     *WorkspaceProviders
	ctx           context.Context
	mu            sync.Mutex
	runs          map[string]*workspaceRun
	closed        bool
	Options       func(client.Workspace) client.RunOptions
	ConfigureLive func(*Live, *client.Agent, client.Workspace)
}
type workspaceRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func NewWorkspaceRuntime(ctx context.Context, registry *client.Workspaces, providers *WorkspaceProviders) *WorkspaceRuntime {
	m := &WorkspaceRuntime{registry: registry, providers: providers, ctx: ctx, runs: map[string]*workspaceRun{}}
	providers.Join = func(r *http.Request, b WorkspaceJoin) (client.Workspace, Provider, error) {
		w, err := registry.Join(r.Context(), b.ID, b.Name, b.Invite, b.Agent)
		if err != nil {
			return w, nil, err
		}
		// Start binds inside Owned, so handler Join must not bind this again.
		p, err := m.start(w, true)
		return w, p, err
	}
	providers.Disconnect = m.Disconnect
	providers.Rename = registry.Rename
	return m
}
func (m *WorkspaceRuntime) StartKnown() error {
	items, err := m.registry.List()
	if err != nil {
		return err
	}
	for _, w := range items {
		if w.ID != client.DefaultWorkspace && w.State == "enrolled" {
			if _, err = m.start(w, true); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *WorkspaceRuntime) start(w client.Workspace, bind bool) (Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("workspace runtime closed")
	}
	if m.ctx.Err() != nil {
		return nil, m.ctx.Err()
	}
	if _, ok := m.runs[w.ID]; ok {
		return nil, errors.New("workspace runtime already started")
	}
	a, err := m.registry.Open(w.ID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	run := &workspaceRun{cancel: cancel, done: make(chan struct{})}
	ready := make(chan Provider, 1)
	opts := client.RunOptions{}
	if m.Options != nil {
		opts = m.Options(w)
	}
	previous := opts.Owned
	opts.Owned = func() (func(), error) {
		var stop func()
		if previous != nil {
			var err error
			stop, err = previous()
			if err != nil {
				return nil, err
			}
		}
		p := NewLive(a)
		if m.ConfigureLive != nil {
			m.ConfigureLive(p, a, w)
		}
		if bind {
			if _, err := m.providers.Bind(w, p); err != nil {
				if stop != nil {
					stop()
				}
				return nil, err
			}
		}
		ready <- p
		return func() {
			m.providers.Remove(w.ID)
			if stop != nil {
				stop()
			}
		}, nil
	}
	m.runs[w.ID] = run
	go func() { defer close(run.done); defer a.Close(); run.err = a.Run(ctx, opts) }()
	select {
	case p := <-ready:
		return p, nil
	case <-run.done:
		delete(m.runs, w.ID)
		cancel()
		return nil, run.err
	case <-m.ctx.Done():
		cancel()
		<-run.done
		delete(m.runs, w.ID)
		return nil, m.ctx.Err()
	}
}
func (m *WorkspaceRuntime) Disconnect(id string) error {
	if id == client.DefaultWorkspace {
		return errors.New("the current daemon owns this workspace; disconnect it through its daemon lifecycle")
	}
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return client.ErrWorkspaceUnknown
	}
	// Disable new routes before awaiting cancellation; already-started actions
	// keep their captured provider and can never switch to another membership.
	m.providers.Remove(id)
	r.cancel()
	<-r.done
	m.mu.Lock()
	delete(m.runs, id)
	m.mu.Unlock()
	return m.registry.Disconnect(id)
}
func (m *WorkspaceRuntime) Close() {
	m.mu.Lock()
	m.closed = true
	runs := make([]*workspaceRun, 0, len(m.runs))
	for id, r := range m.runs {
		m.providers.Remove(id)
		r.cancel()
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		<-r.done
	}
}
