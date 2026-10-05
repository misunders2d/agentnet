package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type GoogleMembership interface {
	GoogleAccess() (protocol.GoogleAccess, error)
	ChangeGoogleAccess(protocol.GoogleAccessChange) error
}

func (l *Live) GoogleAccess() (protocol.GoogleAccess, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	return l.a.GoogleAccess(ctx)
}
func (l *Live) ChangeGoogleAccess(c protocol.GoogleAccessChange) error {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	return l.a.ChangeGoogleAccess(ctx, c)
}
func (s *Server) googleAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(GoogleMembership)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == "GET" {
		v, e := p.GoogleAccess()
		writeResult(w, v, e)
		return
	}
	var c protocol.GoogleAccessChange
	if readJSON(w, r, &c) {
		writeResult(w, struct{}{}, p.ChangeGoogleAccess(c))
	}
}
