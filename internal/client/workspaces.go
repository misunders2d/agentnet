package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const DefaultWorkspace = "default"

var ErrWorkspaceUnknown = errors.New("unknown or disconnected workspace")

// Workspace names an independently enrolled local membership. ID is a local
// routing handle, Realm is the relay's namespace; neither grants authority.
type Workspace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Address  string `json:"address"`
	Realm    string `json:"realm,omitempty"`
	State    string `json:"state"`
}
type Workspaces struct {
	home string
	mu   sync.Mutex
}
type workspaceRegistry struct {
	Version int         `json:"version"`
	Items   []Workspace `json:"items"`
}

// OpenWorkspaces registers the existing home without moving keys or history.
func OpenWorkspaces(home string) (*Workspaces, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	w := &Workspaces{home: home}
	err = w.modify(func(r *workspaceRegistry) error {
		if len(r.Items) > 0 {
			return nil
		}
		a, err := Open(home)
		if err != nil {
			return err
		}
		defer a.Close()
		realm, _ := a.RealmID()
		r.Items = []Workspace{{ID: DefaultWorkspace, Name: "Current workspace", Endpoint: a.hub.base, Address: a.Address, Realm: realm, State: "enrolled"}}
		return nil
	})
	return w, err
}
func validWorkspaceID(id string) bool {
	if id == DefaultWorkspace {
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
func (w *Workspaces) path() string { return filepath.Join(w.home, "workspaces.json") }
func (w *Workspaces) load() (workspaceRegistry, error) {
	r := workspaceRegistry{Version: 1}
	data, err := secfile.Read(w.path())
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 {
		return r, errors.New("unsupported workspace registry version")
	}
	seen := map[string]bool{}
	for _, v := range r.Items {
		if !validWorkspaceID(v.ID) || seen[v.ID] {
			return r, errors.New("invalid workspace registry identity")
		}
		seen[v.ID] = true
		if v.State != "enrolled" && v.State != "joining" && v.State != "disconnected" {
			return r, errors.New("invalid workspace state")
		}
	}
	return r, nil
}
func (w *Workspaces) modify(f func(*workspaceRegistry) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	release, err := lockfile.Acquire(filepath.Join(w.home, "workspaces.lock"))
	if err != nil {
		return err
	}
	defer release()
	r, err := w.load()
	if err != nil {
		return err
	}
	if err = f(&r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return secfile.Write(w.path(), data)
}
func (w *Workspaces) List() ([]Workspace, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load()
	return r.Items, e
}
func (w *Workspaces) homeFor(id string) (string, error) {
	if !validWorkspaceID(id) {
		return "", ErrWorkspaceUnknown
	}
	if id == DefaultWorkspace {
		return w.home, nil
	}
	return filepath.Join(w.home, "workspaces", id), nil
}

// Home resolves only an allowlisted enrolled ID, never a requested path.
func (w *Workspaces) Home(id string) (string, error) {
	items, err := w.List()
	if err != nil {
		return "", err
	}
	for _, v := range items {
		if v.ID == id && v.State == "enrolled" {
			return w.homeFor(id)
		}
	}
	return "", ErrWorkspaceUnknown
}
func (w *Workspaces) Open(id string) (*Agent, error) {
	home, err := w.Home(id)
	if err != nil {
		return nil, err
	}
	return Open(home)
}

// Join retains its local joining ID on failure for exact retry. The registry
// never stores invitations, credentials, arbitrary paths or peer grants.
func (w *Workspaces) Join(ctx context.Context, id, name, code, agentName string) (Workspace, error) {
	var result Workspace
	err := w.modify(func(r *workspaceRegistry) error {
		if id == "" {
			id = protocol.NewID()
		} else if !validWorkspaceID(id) || id == DefaultWorkspace {
			return ErrWorkspaceUnknown
		}
		index := -1
		for i, v := range r.Items {
			if v.ID == id {
				if v.State != "joining" {
					return errors.New("workspace already exists")
				}
				index = i
			}
		}
		if index < 0 {
			r.Items = append(r.Items, Workspace{ID: id, Name: name, State: "joining"})
			index = len(r.Items) - 1
		}
		result = r.Items[index]
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = secfile.Write(w.path(), data); err != nil {
			return err
		}
		home, _ := w.homeFor(id)
		a, joinErr := Join(ctx, home, code, agentName)
		if joinErr != nil {
			a, err = Open(home)
			if err != nil {
				return fmt.Errorf("workspace %s remains joining: %w", id, joinErr)
			}
		}
		defer a.Close()
		inv, err := protocol.DecodeInvite(code)
		if err != nil {
			return err
		}
		cert, err := a.store.config("hub_cert")
		if err != nil {
			return err
		}
		if a.hub.base != inv.Hub || a.Address != protocol.Address(inv.Label, agentName) || cert != inv.CertPEM {
			return errors.New("join recovery invitation does not match this membership")
		}
		realm, _ := a.RealmID()
		result = Workspace{ID: id, Name: name, Endpoint: a.hub.base, Address: a.Address, Realm: realm, State: "enrolled"}
		r.Items[index] = result
		return nil
	})
	return result, err
}

// Disconnect disables local routing, preserving keys/history. It does not
// revoke relay membership or erase data.
func (w *Workspaces) Disconnect(id string) error {
	return w.modify(func(r *workspaceRegistry) error {
		for i := range r.Items {
			if r.Items[i].ID == id {
				r.Items[i].State = "disconnected"
				return nil
			}
		}
		return ErrWorkspaceUnknown
	})
}
func (w *Workspaces) Reconnect(id string) error {
	return w.modify(func(r *workspaceRegistry) error {
		for i := range r.Items {
			if r.Items[i].ID == id && r.Items[i].State == "disconnected" {
				home, _ := w.homeFor(id)
				a, err := Open(home)
				if err != nil {
					return err
				}
				a.Close()
				r.Items[i].State = "enrolled"
				return nil
			}
		}
		return ErrWorkspaceUnknown
	})
}
