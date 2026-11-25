package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// AccountDeleter erases this installation's person from the relay (Google
// Play account deletion; docs/DECISIONS.md, November 25, 2026).
type AccountDeleter interface {
	DeleteAccount(ctx context.Context, person string) ([]string, error)
}

func (l *Live) DeleteAccount(ctx context.Context, person string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.DeleteAccount(ctx, person)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(AccountDeleter)
	if !ok {
		writeErr(w, NotFound("account deletion is not available here"))
		return
	}
	var v struct {
		Person string `json:"person"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if !protocol.ValidID(v.Person) {
		writeErr(w, Refuse("name your own person to delete it"))
		return
	}
	deleted, err := p.DeleteAccount(r.Context(), v.Person)
	if err != nil {
		writeErr(w, Refuse(sentence(err)))
		return
	}
	writeJSON(w, map[string]any{"note": "Your account was deleted.", "deleted": deleted})
}
