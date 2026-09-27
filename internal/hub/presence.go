package hub

import (
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// presence tracks which daemons ("sessions") of each agent are connected.
// A session may have several connections at once (a half-open old one and
// its replacement); it stays live while any is open. When the last one
// closes, it stays listed as reconnecting for a grace period, then ends.
// State is in memory: a Hub restart forgets sessions, and daemons
// re-register when they reconnect.
type presence struct {
	mu       sync.Mutex
	grace    time.Duration
	sessions map[string]map[string]*session // agent -> session id
	onEnd    func(agent, session string)
}

type session struct {
	conns int
	ad    protocol.SessionAd // from the newest connection
	timer *time.Timer        // pending end while conns == 0
}

func (p *presence) connect(agent string, ad protocol.SessionAd) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions == nil {
		p.sessions = map[string]map[string]*session{}
	}
	if p.sessions[agent] == nil {
		p.sessions[agent] = map[string]*session{}
	}
	s := p.sessions[agent][ad.Session]
	if s == nil {
		s = &session{}
		p.sessions[agent][ad.Session] = s
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.conns++
	s.ad = ad
}

// disconnect records one closed connection. Closing an old connection never
// removes a newer one's registration.
func (p *presence) disconnect(agent, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[agent][id]
	if s == nil {
		return
	}
	if s.conns--; s.conns > 0 {
		return
	}
	s.timer = time.AfterFunc(p.grace, func() { p.end(agent, id, s) })
}

func (p *presence) end(agent, id string, s *session) {
	p.mu.Lock()
	if p.sessions[agent][id] != s || s.conns > 0 {
		p.mu.Unlock()
		return // reconnected, or already replaced
	}
	delete(p.sessions[agent], id)
	if len(p.sessions[agent]) == 0 {
		delete(p.sessions, agent)
	}
	p.mu.Unlock()
	p.onEnd(agent, id)
}

// drop ends every session of agent at once (revocation).
func (p *presence) drop(agent string) {
	p.mu.Lock()
	for _, s := range p.sessions[agent] {
		if s.timer != nil {
			s.timer.Stop()
		}
	}
	ids := make([]string, 0, len(p.sessions[agent]))
	for id := range p.sessions[agent] {
		ids = append(ids, id)
	}
	delete(p.sessions, agent)
	p.mu.Unlock()
	for _, id := range ids {
		p.onEnd(agent, id)
	}
}

// live reports whether the session is connected or within its grace period.
func (p *presence) live(agent, id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[agent][id] != nil
}

func (p *presence) list(agent string) []protocol.SessionInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []protocol.SessionInfo{}
	for _, s := range p.sessions[agent] {
		out = append(out, protocol.SessionInfo{Ad: s.ad, Connected: s.conns > 0})
	}
	return out
}
