package ui

import (
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Blocker blocks or unblocks a person's direct messages for this
// installation's person, on all of their devices once synchronized (Google
// Play user-generated content policy; docs/DECISIONS.md, November 25, 2026).
type Blocker interface {
	SetBlocked(person string, blocked bool) error
}

func (l *Live) SetBlocked(person string, blocked bool) error { return l.a.SetBlocked(person, blocked) }

func (s *Server) block(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Blocker)
	if !ok {
		writeErr(w, NotFound("blocking is not available here"))
		return
	}
	var v struct {
		Person  string `json:"person"`
		Blocked bool   `json:"blocked"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if !protocol.ValidID(v.Person) {
		writeErr(w, Refuse("that is not a person"))
		return
	}
	if err := p.SetBlocked(v.Person, v.Blocked); err != nil {
		writeErr(w, Refuse(sentence(err)))
		return
	}
	note := "Unblocked."
	if v.Blocked {
		note = "Blocked."
	}
	writeJSON(w, map[string]any{"note": note, "person": v.Person, "blocked": v.Blocked})
}
