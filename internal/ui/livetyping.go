package ui

import (
	"context"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
)

type TypingProvider interface {
	Typing(protocol.TypingScope) (client.TypingView, error)
	SendTyping(context.Context, protocol.TypingScope, bool) (client.TypingResult, error)
	SetTypingPreferences(client.TypingPreferences) error
}

func (l *Live) Typing(scope protocol.TypingScope) (client.TypingView, error) {
	return l.a.Typing(scope)
}
func (l *Live) SendTyping(ctx context.Context, scope protocol.TypingScope, active bool) (client.TypingResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.SendTyping(ctx, scope, active)
}
func (l *Live) SetTypingPreferences(p client.TypingPreferences) error {
	return l.a.SetTypingPreferences(p)
}
func (s *Server) typingView(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TypingProvider)
	if !ok {
		writeErr(w, NotFound("typing is unavailable"))
		return
	}
	v, e := p.Typing(protocol.TypingScope{Conv: r.URL.Query().Get("conv"), Peer: r.URL.Query().Get("peer"), Thread: r.URL.Query().Get("thread")})
	if e != nil {
		writeErr(w, Refuse("typing scope is unavailable"))
		return
	}
	writeJSON(w, v)
}
func (s *Server) sendTyping(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TypingProvider)
	if !ok {
		writeErr(w, NotFound("typing is unavailable"))
		return
	}
	var v struct {
		Scope  protocol.TypingScope `json:"scope"`
		Active bool                 `json:"active"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	result, e := p.SendTyping(r.Context(), v.Scope, v.Active)
	if e != nil {
		writeErr(w, Refuse("typing was not confirmed for this scope"))
		return
	}
	writeJSON(w, result)
}
func (s *Server) typingPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(TypingProvider)
	if !ok {
		writeErr(w, NotFound("typing is unavailable"))
		return
	}
	var v client.TypingPreferences
	if !readJSON(w, r, &v) {
		return
	}
	if e := p.SetTypingPreferences(v); e != nil {
		writeErr(w, Refuse("typing preferences were not saved"))
		return
	}
	writeJSON(w, v)
}
