package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

// PersonLabelProvider changes only a self-claimed person display label.
type PersonLabelProvider interface {
	RenamePerson(context.Context, string) (client.PersonInfo, error)
}

func (l *Live) RenamePerson(ctx context.Context, label string) (client.PersonInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.RenamePerson(ctx, label)
}
func (s *Server) renamePerson(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(PersonLabelProvider)
	if !ok {
		writeErr(w, NotFound("display label change unavailable"))
		return
	}
	var c struct {
		Label string `json:"label"`
	}
	if !readJSON(w, r, &c) {
		return
	}
	v, err := p.RenamePerson(r.Context(), c.Label)
	if err != nil {
		writeErr(w, Refuse("Display label change was not confirmed. Refresh your person before retrying."))
		return
	}
	writeJSON(w, v)
}
