package hub

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The workspace name is the label the Hub's admin gives the workspace
// ("Mellanni"). It rides the member list (GET /v1/agents and the "members"
// push), so every member's devices show it without reinstalling or
// rejoining. It is a label, never identity: the realm id is that.

// workspaceSchema is the singleton holding the name; no row means none.
const workspaceSchema = `
CREATE TABLE workspace(
  id INTEGER PRIMARY KEY CHECK (id = 1),
  name TEXT NOT NULL,
  set_by TEXT NOT NULL,
  set_at INTEGER NOT NULL);
`

func (s *store) workspaceName() (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM workspace WHERE id = 1`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func (s *store) setWorkspaceName(name, by string) error {
	if name == "" {
		_, err := s.db.Exec(`DELETE FROM workspace`)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO workspace(id, name, set_by, set_at) VALUES(1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, set_by = excluded.set_by, set_at = excluded.set_at`, name, by, time.Now().Unix())
	return err
}

// loadWorkspaceName reads the stored name at start.
func (h *Hub) loadWorkspaceName() error {
	name, err := h.store.workspaceName()
	if err == nil {
		h.workspace.Store(&name)
	}
	return err
}

// currentWorkspaceName is the workspace name, or "" when none is set.
func (h *Hub) currentWorkspaceName() string {
	if p := h.workspace.Load(); p != nil {
		return *p
	}
	return ""
}

// handleWorkspacePut sets (or, with an empty name, clears) the workspace
// name. Admin only; members learn it from the member list, which is pushed
// again only when the name changed.
func (h *Hub) handleWorkspacePut(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.WorkspaceNameRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed workspace request")
		return
	}
	name := ""
	if req.Name != "" {
		var valid bool
		if name, valid = protocol.ValidWorkspaceName(req.Name); !valid {
			writeError(w, http.StatusBadRequest, "", "workspace name must be 1–120 readable characters")
			return
		}
	}
	h.workspaceMu.Lock()
	changed := h.currentWorkspaceName() != name
	err := h.store.setWorkspaceName(name, caller) // saved before any stream is woken
	if err == nil && changed {
		h.workspace.Store(&name)
	}
	h.workspaceMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "could not store the workspace name")
		return
	}
	if changed {
		h.membersChanged()
		if name == "" {
			h.cfg.Logf("workspace name cleared by %s", caller)
		} else {
			h.cfg.Logf("workspace name set to %q by %s", name, caller)
		}
	}
	writeJSON(w, http.StatusOK, protocol.WorkspaceNameRequest{Name: name})
}
