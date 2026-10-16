package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// WorkspaceProviders is an allowlist of providers owned by the host daemon.
// Each mount generation gets a new handle; disconnected handles fail closed.
// There is no active global provider: every request resolves its own binding.
type WorkspaceProviders struct {
	mu         sync.RWMutex
	rev        uint64
	wake       chan struct{}
	entries    map[string]workspaceProvider
	Join       func(*http.Request, WorkspaceJoin) (client.Workspace, Provider, error)
	Disconnect func(string) error
	Rename     func(string, string) (client.Workspace, error)
	Reconnect  func(string) error                 // routes a disconnected membership again (binds it)
	Known      func() ([]client.Workspace, error) // every membership of the registry, in any state
}
type WorkspaceJoin struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Invite string `json:"invite"`
	Agent  string `json:"agent"`
}
type workspaceProvider struct {
	WorkspaceBinding
	provider Provider
}
type WorkspaceBinding struct {
	client.Workspace
	Handle string `json:"handle"`
	// HubName is the workspace's own name its admin set, as last listed
	// ("" when none, or not bound); Name stays this device's own label.
	HubName string `json:"hub_name,omitempty"`
}

// named is b with the workspace's own name from its bound provider.
func (e workspaceProvider) named() WorkspaceBinding {
	b := e.WorkspaceBinding
	if n, ok := e.provider.(hubNamer); ok {
		b.HubName = n.HubWorkspaceName()
	}
	return b
}

func NewWorkspaceProviders() *WorkspaceProviders {
	return &WorkspaceProviders{entries: map[string]workspaceProvider{}, wake: make(chan struct{})}
}
func workspaceID(id string) bool {
	if id == client.DefaultWorkspace {
		return true
	}
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s *WorkspaceProviders) Bind(w client.Workspace, p Provider) (WorkspaceBinding, error) {
	if !workspaceID(w.ID) || p == nil || w.State != "enrolled" {
		return WorkspaceBinding{}, errors.New("invalid workspace provider")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[w.ID]; ok {
		return WorkspaceBinding{}, errors.New("workspace already bound")
	}
	b := WorkspaceBinding{Workspace: w, Handle: protocol.NewID()}
	s.entries[w.ID] = workspaceProvider{b, p}
	s.bumpLocked()
	return b, nil
}
func (s *WorkspaceProviders) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; ok {
		delete(s.entries, id)
		s.bumpLocked()
	}
}

// Map, revision and the wake belonging to that exact revision are copied
// atomically. Provider methods are called only after releasing this lock.
func (s *WorkspaceProviders) snapshot() (uint64, <-chan struct{}, []workspaceProvider) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]workspaceProvider, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return s.rev, s.wake, entries
}
func (s *WorkspaceProviders) bumpLocked() { close(s.wake); s.wake = make(chan struct{}); s.rev++ }

type workspaceInvalidation struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Seq    uint64 `json:"seq"`
}

// One local connection selects directly over copied provider and membership
// channels. It carries invalidations only; totals remain provider-owned.
func (s *Server) workspaceEvents(w http.ResponseWriter, r *http.Request, set *WorkspaceProviders) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.host {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	controller := http.NewResponseController(w)
	send := func(name string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return false
		}
		err = controller.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		return err == nil
	}
	first := true
	var previousRev uint64
	last := map[string]uint64{}
	for {
		rev, wake, entries := set.snapshot()
		bindings := make([]workspaceInvalidation, 0, len(entries))
		cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(r.Context().Done())}, {Dir: reflect.SelectRecv, Chan: reflect.ValueOf(s.restarting)}, {Dir: reflect.SelectRecv, Chan: reflect.ValueOf(wake)}}
		for _, entry := range entries {
			seq, next := entry.provider.Changed()
			if detailed, ok := entry.provider.(interface {
				ViewChanges() (client.ViewChange, <-chan struct{})
			}); ok {
				var view client.ViewChange
				view, next = detailed.ViewChanges()
				seq = view.Content
			}
			bindings = append(bindings, workspaceInvalidation{entry.ID, entry.Handle, seq})
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(next)})
		}
		// Recheck the snapshot and copy pending events under the map lock,
		// then release it before any writer I/O. A removal after this check
		// may leave one old-handle invalidation on the wire: its scoped GET
		// fails closed, and the closed wake reconciles membership next time.
		set.mu.RLock()
		if rev != set.rev {
			set.mu.RUnlock()
			continue
		}
		var pending []struct {
			name string
			data any
		}
		if first || rev != previousRev {
			pending = append(pending, struct {
				name string
				data any
			}{"workspaces", struct {
				Rev      uint64                  `json:"rev"`
				Bindings []workspaceInvalidation `json:"bindings"`
			}{rev, bindings}})
			last = map[string]uint64{}
			for _, binding := range bindings {
				last[binding.ID+":"+binding.Handle] = binding.Seq
			}
			first, previousRev = false, rev
		} else {
			for _, binding := range bindings {
				key := binding.ID + ":" + binding.Handle
				if binding.Seq > last[key] {
					pending = append(pending, struct {
						name string
						data any
					}{"change", binding})
					last[key] = binding.Seq
				}
			}
		}
		set.mu.RUnlock()
		for _, event := range pending {
			if !send(event.name, event.data) {
				return
			}
		}
		chosen, _, _ := reflect.Select(cases)
		if chosen < 2 {
			return
		}
	}
}

func (s *WorkspaceProviders) List() []WorkspaceBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []WorkspaceBinding{}
	for _, v := range s.entries {
		out = append(out, v.named())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == client.DefaultWorkspace {
			return true
		}
		if out[j].ID == client.DefaultWorkspace {
			return false
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// WorkspaceHandler preserves this Server's loopback Host, cookie and Origin
// checks. Scoped paths route to known providers without any filesystem input.
func (s *Server) WorkspaceHandler(set *WorkspaceProviders) http.Handler {
	base := s.Handler()
	metadata := s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/workspaces/events" && r.Method == "GET":
			s.workspaceEvents(w, r, set)
		case r.URL.Path == "/api/workspaces" && r.Method == "GET":
			writeJSON(w, set.List())
		case r.URL.Path == "/api/workspaces/all" && r.Method == "GET":
			// Every membership with its state; only a connected one has a
			// handle. A disconnected one keeps its keys and history here.
			if set.Known == nil {
				http.Error(w, "workspace list unavailable", http.StatusNotImplemented)
				return
			}
			known, err := set.Known()
			if err != nil {
				writeErr(w, err)
				return
			}
			set.mu.RLock()
			out := []WorkspaceBinding{}
			for _, k := range known {
				b := WorkspaceBinding{Workspace: k}
				if e, ok := set.entries[k.ID]; ok {
					b = e.named()
				}
				out = append(out, b)
			}
			set.mu.RUnlock()
			writeJSON(w, out)
		case r.URL.Path == "/api/workspaces/reconnect" && r.Method == "POST":
			var body struct {
				ID string `json:"id"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil || !workspaceID(body.ID) {
				http.Error(w, "invalid workspace", 400)
				return
			}
			if set.Reconnect == nil {
				http.Error(w, "reconnect unavailable", http.StatusNotImplemented)
				return
			}
			if err := set.Reconnect(body.ID); errors.Is(err, client.ErrWorkspaceUnknown) {
				writeErr(w, NotFound("No disconnected workspace with that ID here."))
				return
			} else if err != nil {
				writeErr(w, Refuse("Cannot reconnect this workspace now: "+sentence(err)))
				return
			}
			set.mu.RLock()
			entry, ok := set.entries[body.ID]
			set.mu.RUnlock()
			if !ok {
				http.Error(w, "stale workspace", 409)
				return
			}
			writeJSON(w, entry.WorkspaceBinding)
		case r.URL.Path == "/api/workspaces/join" && r.Method == "POST":
			if set.Join == nil {
				http.Error(w, "joining unavailable", http.StatusNotImplemented)
				return
			}
			var body WorkspaceJoin
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil {
				http.Error(w, "invalid workspace invitation request", 400)
				return
			}
			membership, p, err := set.Join(r, body)
			if err != nil {
				status, note := http.StatusServiceUnavailable, "Cannot connect to this workspace. Retry with its saved joining ID."
				if errors.Is(err, client.ErrDeviceName) {
					status, note = http.StatusBadRequest, "Use lowercase letters, numbers and dashes for the device name, like laptop or work-phone. Nothing was joined."
				} else if errors.Is(err, client.ErrWorkspaceName) {
					status, note = http.StatusBadRequest, "Enter a readable workspace name, up to 120 characters. Nothing was joined."
				} else if errors.Is(err, client.ErrWorkspaceExists) {
					status, note = http.StatusConflict, "This workspace is already on this computer. Reconnect it if it is disconnected; nothing was joined."
				} else if _, decodeErr := protocol.DecodeInvite(body.Invite); decodeErr != nil {
					status, note = http.StatusBadRequest, "That invitation is damaged or incomplete. Copy the complete invitation again."
				} else if errors.Is(err, client.ErrAddressTaken) {
					status, note = http.StatusConflict, "That device name is already enrolled. Choose another device name and retry."
				} else {
					var relay *client.HubError
					if errors.As(err, &relay) && relay.Status == http.StatusForbidden {
						status, note = http.StatusForbidden, "That invitation was refused, expired or already used. Ask for a new invitation."
					} else if errors.As(err, &relay) && relay.Status >= 400 && relay.Status < 500 {
						status, note = http.StatusBadRequest, "The workspace refused this invitation. Check the invitation and device name."
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				response := map[string]string{"error": note}
				if membership.ID != "" {
					response["retry_id"] = membership.ID
				}
				json.NewEncoder(w).Encode(response)
				return
			}
			set.mu.RLock()
			existing, alreadyBound := set.entries[membership.ID]
			set.mu.RUnlock()
			b := existing.WorkspaceBinding
			if !alreadyBound {
				b, err = set.Bind(membership, p)
			}
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, b)
		case r.URL.Path == "/api/workspaces/rename" && r.Method == "POST":
			var body struct {
				ID     string `json:"id"`
				Handle string `json:"handle"`
				Name   string `json:"name"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil {
				http.Error(w, "invalid workspace name", 400)
				return
			}
			set.mu.Lock()
			defer set.mu.Unlock()
			entry, ok := set.entries[body.ID]
			if !ok || entry.Handle != body.Handle {
				http.Error(w, "stale workspace", 409)
				return
			}
			if set.Rename == nil {
				http.Error(w, "workspace naming unavailable", 501)
				return
			}
			renamed, err := set.Rename(body.ID, body.Name)
			if err != nil {
				writeErr(w, Refuse("Enter a readable workspace name, up to 120 characters. Nothing changed."))
				return
			}
			if renamed.ID != entry.ID || renamed.Endpoint != entry.Endpoint || renamed.Address != entry.Address || renamed.Realm != entry.Realm || renamed.State != entry.State {
				http.Error(w, "workspace identity changed", 409)
				return
			}
			entry.Name = renamed.Name
			set.entries[body.ID] = entry
			writeJSON(w, entry.named())
		case r.URL.Path == "/api/workspaces/disconnect" && r.Method == "POST":
			var body struct {
				ID     string `json:"id"`
				Handle string `json:"handle"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "invalid workspace", 400)
				return
			}
			set.mu.RLock()
			entry, ok := set.entries[body.ID]
			set.mu.RUnlock()
			if !ok || entry.Handle != body.Handle {
				http.Error(w, "stale workspace", 409)
				return
			}
			if set.Disconnect == nil {
				http.Error(w, "disconnect unavailable", 501)
				return
			}
			if err := set.Disconnect(body.ID); err != nil {
				writeErr(w, err)
				return
			}
			set.Remove(body.ID)
			writeJSON(w, map[string]string{"note": "Disconnected here. Relay membership and local history are retained."})
		default:
			http.NotFound(w, r)
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/workspaces") {
			metadata.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/workspaces/") {
			base.ServeHTTP(w, r)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/workspaces/"), "/", 3)
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		set.mu.RLock()
		entry, ok := set.entries[parts[0]]
		set.mu.RUnlock()
		if !ok || entry.Handle != parts[1] {
			s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "stale or disconnected workspace", 409) })).ServeHTTP(w, r)
			return
		}
		path := "/" + parts[2]
		if !(strings.HasPrefix(path, "/api/") || path == "/events") {
			http.NotFound(w, r)
			return
		}
		child := New(entry.provider, s.host, s.token)
		child.restarting = s.restarting
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Path = path
		clone.URL = &u
		child.Handler().ServeHTTP(w, clone)
	})
}
