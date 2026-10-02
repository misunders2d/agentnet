package hub

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Reuse signed request proof; live replay protection stays in memory.
// This route never calls authenticateBody/useNonce or writes SQLite.
func (h *Hub) handleSignal(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxSignalBody)
	var sender agent
	sr, err := protocol.ReadSignedRequest(r, func(address string) (ed25519.PublicKey, error) {
		var e error
		sender, e = h.store.agent(address)
		if e != nil {
			return nil, errors.New("unknown agent")
		}
		return sender.Public.SignKey, nil
	})
	if err != nil {
		writeError(w, 401, "", "invalid signal authentication")
		return
	}
	if sender.Revoked || sender.Pending {
		writeError(w, 403, "", "signal sender is not admitted")
		return
	}
	v, err := protocol.ParseSignal(sr.Body)
	if err != nil || v.From != sr.Agent || v.Verify(sender.Public.SignKey, time.Now()) != nil {
		writeError(w, 400, "", "invalid live signal")
		return
	}
	to, err := h.store.agent(v.To)
	if err != nil || to.Revoked || to.Pending {
		writeError(w, 403, "", "signal recipient is not admitted")
		return
	}
	if !h.signals.push(v, time.Now()) {
		writeError(w, 429, "", "live signal replay or rate limit")
		return
	}
	w.WriteHeader(http.StatusAccepted) // live fanout accepted, no delivery/storage claim
}

type signalRate struct {
	at      time.Time
	limiter *rate.Limiter
}
type signalRuntime struct {
	mu     sync.Mutex
	replay protocol.ReplayWindow
	rates  map[string]signalRate
	queues map[string]map[*subscriber]chan protocol.Signal
}

func (s *signalRuntime) subscribe(address string, sub *subscriber) <-chan protocol.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queues == nil {
		s.queues = map[string]map[*subscriber]chan protocol.Signal{}
	}
	if s.queues[address] == nil {
		s.queues[address] = map[*subscriber]chan protocol.Signal{}
	}
	ch := make(chan protocol.Signal, 32)
	s.queues[address][sub] = ch
	return ch
}
func (s *signalRuntime) unsubscribe(address string, sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queues[address], sub)
	if len(s.queues[address]) == 0 {
		delete(s.queues, address)
	}
}

// push uses sender and sender-target token buckets: rates/bursts 128 and 8.
// Queues never survive a connection. Full queues drop signals, never block.
func (s *signalRuntime) push(v protocol.Signal, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replay == nil {
		s.replay = protocol.ReplayWindow{}
		s.rates = map[string]signalRate{}
	}
	for k, r := range s.rates {
		// Both bursts fully refill after one second idle. No reservation escapes mu.
		if now.Sub(r.at) >= time.Second {
			delete(s.rates, k)
		}
	}
	keys := [2]string{v.From, v.From + "\x00" + v.To}
	limits := [2]int{128, 8}
	var entries [2]signalRate
	missing := 0
	for i, key := range keys {
		entries[i] = s.rates[key]
		if entries[i].limiter == nil {
			entries[i].limiter = rate.NewLimiter(rate.Limit(limits[i]), limits[i])
			missing++
		}
	}
	if len(s.rates)+missing > 8192 {
		return false
	}
	// Private limiter references and mu keep admission/rollback atomic across keys.
	var reservations [2]*rate.Reservation
	for i := range entries {
		reservations[i] = entries[i].limiter.ReserveN(now, 1)
		if !reservations[i].OK() || reservations[i].DelayFrom(now) > 0 {
			for j := i; j >= 0; j-- {
				reservations[j].CancelAt(now)
			}
			return false
		}
	}
	if !s.replay.Accept(v.From+"\x00"+v.ID, time.UnixMilli(v.TS).Add(protocol.SignalTTL), now, 8192) {
		for i := len(reservations) - 1; i >= 0; i-- {
			reservations[i].CancelAt(now)
		}
		return false
	}
	for i, key := range keys {
		entries[i].at = now
		s.rates[key] = entries[i]
	}
	for _, ch := range s.queues[v.To] {
		select {
		case ch <- v:
		default:
		}
	}
	return true
}
