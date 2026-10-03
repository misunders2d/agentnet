package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// WorkspaceProviders is an allowlist of providers owned by the host daemon.
// Each mount generation gets a new handle; disconnected handles fail closed.
// There is no active global provider: every request resolves its own binding.
type WorkspaceProviders struct {
	mu         sync.RWMutex
	entries    map[string]workspaceProvider
	Join       func(*http.Request, WorkspaceJoin) (client.Workspace, Provider, error)
	Disconnect func(string) error
	Rename     func(string, string) (client.Workspace, error)
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
}

func NewWorkspaceProviders() *WorkspaceProviders {
	return &WorkspaceProviders{entries: map[string]workspaceProvider{}}
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
	return b, nil
}
func (s *WorkspaceProviders) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
}
func (s *WorkspaceProviders) List() []WorkspaceBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []WorkspaceBinding{}
	for _, v := range s.entries {
		out = append(out, v.WorkspaceBinding)
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
		case r.URL.Path == "/api/workspaces" && r.Method == "GET":
			writeJSON(w, set.List())
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
				if _, decodeErr := protocol.DecodeInvite(body.Invite); decodeErr != nil {
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
			writeJSON(w, entry.WorkspaceBinding)
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
