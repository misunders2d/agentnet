package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// streams tracks live push connections per agent so new messages wake them
// and revocation closes them.
type streams struct {
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	id      string // connection id, echoed back in ping acknowledgements
	wake    chan struct{}
	cancel  context.CancelFunc
	lastAck atomic.Int64 // unix nanoseconds of the last acknowledged ping
}

func (s *streams) add(agent string, cancel context.CancelFunc) *subscriber {
	sub := &subscriber{id: protocol.NewID(), wake: make(chan struct{}, 1), cancel: cancel}
	sub.lastAck.Store(time.Now().UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[string]map[*subscriber]struct{}{}
	}
	if s.subs[agent] == nil {
		s.subs[agent] = map[*subscriber]struct{}{}
	}
	s.subs[agent][sub] = struct{}{}
	return sub
}

func (s *streams) remove(agent string, sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs[agent], sub)
	if len(s.subs[agent]) == 0 {
		delete(s.subs, agent)
	}
}

func (s *streams) notify(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		select {
		case sub.wake <- struct{}{}:
		default: // already pending
		}
	}
}

// ack records that agent answered a ping on connection id. Acks for a
// closed or someone else's connection do nothing, so they can never keep a
// replacement connection (or another agent's) alive.
func (s *streams) ack(agent, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		if sub.id == id {
			sub.lastAck.Store(time.Now().UnixNano())
			return true
		}
	}
	return false
}

func (s *streams) disconnect(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		sub.cancel()
	}
}

// streamWriteTimeout bounds each write to a push stream, so a peer that
// stopped reading (asleep, half-open) cannot hold a handler forever.
const streamWriteTimeout = 15 * time.Second

// handleStream pushes every unacknowledged message, then new ones as they
// arrive. Unacknowledged messages are pushed again on the next connection.
//
// Liveness: every ping carries this connection's id, and the client answers
// with a signed acknowledgement (one small request per ping interval). A
// connection with no acknowledgement for two ping intervals is closed, so a
// silently vanished laptop's session ends within about three ping intervals
// plus the session grace period.
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ad, err := protocol.DecodeAd(r.URL.Query().Get("ad"))
	if err == nil && ad.Address != caller {
		err = errors.New("session ad is for another agent")
	}
	if err == nil {
		var a agent
		if a, err = h.store.agent(caller); err == nil {
			err = ad.Verify(a.Public.SignKey)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "", "stream needs a valid signed session ad: "+err.Error())
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := h.streams.add(caller, cancel)
	defer h.streams.remove(caller, sub)
	h.presence.connect(caller, ad)
	defer h.presence.disconnect(caller, ad.Session)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	http.NewResponseController(w).Flush()

	rc := http.NewResponseController(w)
	write := func(format string, args ...any) bool {
		rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	ping := time.NewTicker(h.heartbeat)
	defer ping.Stop()
	lease := 2 * h.heartbeat
	var lastSeq int64
	for {
		msgs, err := h.store.pendingFor(caller, ad.Session, lastSeq)
		if err != nil {
			return
		}
		for _, m := range msgs {
			if !write("event: message\ndata: %s\n\n", m.Envelope) {
				return
			}
			lastSeq = m.Seq
		}
		if len(msgs) == 100 {
			continue // more backlog
		}
		select {
		case <-sub.wake:
		case <-ping.C:
			if time.Since(time.Unix(0, sub.lastAck.Load())) > lease {
				h.cfg.Logf("closing silent stream of %s#%s", caller, ad.Session)
				return
			}
			if !write("event: ping\ndata: {\"conn\":%q}\n\n", sub.id) {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}

func (h *Hub) handleStreamAck(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.PingAck
	if err := decodeStrict(body, &req); err != nil || !h.streams.ack(caller, req.Conn) {
		writeError(w, http.StatusNotFound, "", "unknown stream")
		return
	}
	h.stats.Acks.Add(1)
	w.WriteHeader(http.StatusNoContent)
}
