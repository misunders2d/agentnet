package hub

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// streams tracks live push connections per agent so new messages wake them
// and revocation closes them.
type streams struct {
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	wake   chan struct{}
	cancel context.CancelFunc
}

func (s *streams) add(agent string, cancel context.CancelFunc) *subscriber {
	sub := &subscriber{wake: make(chan struct{}, 1), cancel: cancel}
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

func (s *streams) disconnect(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		sub.cancel()
	}
}

// handleStream pushes every unacknowledged message, then new ones as they
// arrive. Unacknowledged messages are pushed again on the next connection.
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "", "streaming unsupported")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := h.streams.add(caller, cancel)
	defer h.streams.remove(caller, sub)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ping := time.NewTicker(h.heartbeat)
	defer ping.Stop()
	var lastSeq int64
	for {
		msgs, err := h.store.pendingFor(caller, lastSeq)
		if err != nil {
			return
		}
		for _, m := range msgs {
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", m.Envelope); err != nil {
				return
			}
			lastSeq = m.Seq
		}
		flusher.Flush()
		if len(msgs) == 100 {
			continue // more backlog
		}
		select {
		case <-sub.wake:
		case <-ping.C:
			if _, err := fmt.Fprint(w, "event: ping\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}
