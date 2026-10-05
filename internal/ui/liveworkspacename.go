package ui

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The workspace's own name (MEL-524): the Hub admin sets it once and every
// member's devices show it. GET /api/workspace reads it with whether this
// device may rename it for everyone; POST /api/workspace/name renames it
// (admin only: a company setting, the person's own tap on this page).

// WorkspaceNamer is implemented by providers that know the workspace's
// name and can ask the Hub to change it.
type WorkspaceNamer interface {
	WorkspaceInfo(ctx context.Context) (WorkspaceInfoView, error)
	SetWorkspaceName(ctx context.Context, name string) (WorkspaceInfoView, error)
}

// hubNamer is implemented by providers that keep the workspace's name for
// the workspace list (WorkspaceBinding.HubName).
type hubNamer interface{ HubWorkspaceName() string }

const (
	renameNotAdmin = "Only an admin of this workspace can rename it for everyone."
	renameInvalid  = "Enter a readable workspace name, up to 120 characters. Nothing changed."
)

// canAdmin reports whether this device holds the Hub's admin role (its own,
// or its person's): one signal for every admin-only screen. Unknown (the
// Hub not reached) counts as no.
func (l *Live) canAdmin(ctx context.Context) bool {
	role, err := l.a.HubRole(ctx)
	return err == nil && role == protocol.RoleAdmin
}

// WorkspaceInfo implements WorkspaceNamer.
func (l *Live) WorkspaceInfo(ctx context.Context) (WorkspaceInfoView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return WorkspaceInfoView{WorkspaceView: WorkspaceView{Name: l.a.WorkspaceName(), Server: l.a.RelayHost()}, CanRename: l.canAdmin(ctx)}, nil
}

// SetWorkspaceName implements WorkspaceNamer.
func (l *Live) SetWorkspaceName(ctx context.Context, name string) (WorkspaceInfoView, error) {
	rctx, cancel := context.WithTimeout(ctx, l.timeout)
	_, err := l.a.SetWorkspaceName(rctx, name)
	cancel()
	var he *client.HubError
	switch {
	case errors.Is(err, client.ErrWorkspaceName):
		return WorkspaceInfoView{}, Refuse(renameInvalid)
	case errors.As(err, &he) && he.Status == http.StatusForbidden:
		return WorkspaceInfoView{}, Refuse(renameNotAdmin)
	case errors.As(err, &he) && he.Status == http.StatusBadRequest:
		return WorkspaceInfoView{}, Refuse(renameInvalid)
	case err != nil:
		return WorkspaceInfoView{}, Refuse("Cannot rename the workspace now: " + sentence(err))
	}
	return l.WorkspaceInfo(ctx)
}

// HubWorkspaceName is the workspace's own name, as last listed.
func (l *Live) HubWorkspaceName() string { return l.a.WorkspaceName() }

func (s *Server) workspaceInfo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(WorkspaceNamer)
	if !ok {
		writeErr(w, NotFound("the workspace's name is not known here"))
		return
	}
	v, err := p.WorkspaceInfo(r.Context())
	writeResult(w, v, err)
}

func (s *Server) renameWorkspace(w http.ResponseWriter, r *http.Request) {
	var x WorkspaceNameChange
	if !readJSON(w, r, &x) {
		return
	}
	if x.Name != "" {
		if _, ok := protocol.ValidWorkspaceName(x.Name); !ok {
			writeErr(w, Refuse(renameInvalid))
			return
		}
	}
	p, ok := s.p.(WorkspaceNamer)
	if !ok {
		writeErr(w, NotFound("the workspace cannot be renamed from here"))
		return
	}
	v, err := p.SetWorkspaceName(r.Context(), x.Name)
	writeResult(w, v, err)
}
