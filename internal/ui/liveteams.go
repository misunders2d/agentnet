package ui

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TeamProvider is optional. Selection produces a reviewable directory
// snapshot; this interface never adds conversation members or grants history.
type TeamProvider interface {
	Teams(context.Context) (client.TeamsView, error)
	ChangeTeam(context.Context, client.TeamChange) (protocol.TeamState, error)
	TeamSnapshot(context.Context, []string) (protocol.TeamSnapshot, error)
}

func (l *Live) Teams(ctx context.Context) (client.TeamsView, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.Teams(ctx)
}
func (l *Live) ChangeTeam(ctx context.Context, c client.TeamChange) (protocol.TeamState, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.ChangeTeam(ctx, c)
}
func (l *Live) TeamSnapshot(ctx context.Context, ids []string) (protocol.TeamSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.TeamSnapshot(ctx, ids)
}

func (s *Server) teams(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TeamProvider)
	if !ok {
		writeErr(w, NotFound("teams are not available from this provider"))
		return
	}
	v, err := p.Teams(r.Context())
	if err != nil && v.Status == "" {
		http.Error(w, "team directory unavailable", 500)
		return
	}
	writeJSON(w, v) // unavailable/offline includes retained state and sanitized status
}
func (s *Server) changeTeam(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TeamProvider)
	if !ok {
		writeErr(w, NotFound("teams are not available from this provider"))
		return
	}
	var c client.TeamChange
	if !readJSON(w, r, &c) {
		return
	}
	v, err := p.ChangeTeam(r.Context(), c)
	if err != nil {
		writeErr(w, Refuse(teamProblem(err)))
		return
	}
	writeJSON(w, v)
}
func (s *Server) teamSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TeamProvider)
	if !ok {
		writeErr(w, NotFound("team selection is not available from this provider"))
		return
	}
	var c struct {
		Teams []string `json:"teams"`
	}
	if !readJSON(w, r, &c) {
		return
	}
	v, err := p.TeamSnapshot(r.Context(), c.Teams)
	if err != nil {
		writeErr(w, Refuse(teamProblem(err)))
		return
	}
	writeJSON(w, v)
}

func teamProblem(err error) string {
	for _, known := range []error{protocol.ErrTeamArchived, protocol.ErrTeamManager, protocol.ErrTeamLastManager, client.ErrNoPerson, client.ErrTeamsUnsupported, client.ErrTeamConflict, client.ErrTeamStale, client.ErrRealmInvalid} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	switch err.Error() {
	case "remove your manager role explicitly before leaving the team", "remove the manager role explicitly before removing membership", "a new manager must already be a current member", "team not found", "invalid team id", "select one or more teams", "invalid selected team", "selected team not in current directory":
		return err.Error()
	}
	return "The team operation was not confirmed; refresh and review the directory before trying again."
}
