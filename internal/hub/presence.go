package hub

import (
	"slices"
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
	onChange func(agent string) // the agent's state (see state) changed; called without the lock
	closed   bool
}

type session struct {
	conns int
	ad    protocol.SessionAd // from the newest connection
	timer *time.Timer        // pending end while conns == 0
}

func (p *presence) connect(agent string, ad protocol.SessionAd) {
	p.mu.Lock()
	before := p.stateLocked(agent)
	defer func() { p.changedAfterUnlock(agent, before) }()
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
	before := p.stateLocked(agent)
	defer func() { p.changedAfterUnlock(agent, before) }()
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
	if p.closed || p.sessions[agent][id] != s || s.conns > 0 {
		p.mu.Unlock()
		return // reconnected, or already replaced
	}
	// A session's end changes what the device supports as a whole (every
	// live session's capabilities count, profile.go) even while another
	// session keeps it connected: those waiting on it must look again.
	defer func() { p.mu.Unlock(); p.notify(agent) }()
	delete(p.sessions[agent], id)
	if len(p.sessions[agent]) == 0 {
		delete(p.sessions, agent)
	}
	p.onEnd(agent, id) // under the lock, so close() waits for it
}

// drop ends every session of agent at once (revocation).
func (p *presence) drop(agent string) {
	p.mu.Lock()
	if p.stateLocked(agent) != protocol.PresenceOffline {
		defer p.notify(agent) // after the sessions have ended
	}
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

// state is the agent's presence: connected if any of its sessions has an
// open connection, reconnecting if it has sessions but all are within their
// grace period, offline if it has none.
func (p *presence) state(agent string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked(agent)
}

func (p *presence) stateLocked(agent string) string {
	if len(p.sessions[agent]) == 0 {
		return protocol.PresenceOffline
	}
	for _, s := range p.sessions[agent] {
		if s.conns > 0 {
			return protocol.PresenceConnected
		}
	}
	return protocol.PresenceReconnecting
}

// changedAfterUnlock releases the lock and reports a change of agent's state
// since before.
func (p *presence) changedAfterUnlock(agent, before string) {
	after := p.stateLocked(agent)
	p.mu.Unlock()
	if after != before {
		p.notify(agent)
	}
}

func (p *presence) notify(agent string) {
	if p.onChange != nil {
		p.onChange(agent)
	}
}

// sessionIDs lists agent's live sessions (connected or within grace), sorted.
func (p *presence) sessionIDs(agent string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.sessions[agent]))
	for id := range p.sessions[agent] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
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

// close stops pending session ends; used when the Hub shuts down (sessions
// are not persisted anyway).
func (p *presence) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, byID := range p.sessions {
		for _, s := range byID {
			if s.timer != nil {
				s.timer.Stop()
			}
		}
	}
}
