package hub

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// MaxReceiptWait bounds how long one receipt wait request may be held.
const MaxReceiptWait = 60 * time.Second

// waiters wakes requests waiting for a message's receipt to change.
type waiters struct {
	mu sync.Mutex
	m  map[string]map[chan struct{}]struct{}
}

func (w *waiters) add(id string) chan struct{} {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[string]map[chan struct{}]struct{}{}
	}
	if w.m[id] == nil {
		w.m[id] = map[chan struct{}]struct{}{}
	}
	w.m[id][ch] = struct{}{}
	return ch
}

func (w *waiters) remove(id string, ch chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m[id], ch)
	if len(w.m[id]) == 0 {
		delete(w.m, id)
	}
}

func wake(set map[chan struct{}]struct{}) {
	for ch := range set {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// notify wakes waiters of one message; notifyAll wakes every waiter (used
// when a session ends and several messages may have expired).
func (w *waiters) notify(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wake(w.m[id])
}

func (w *waiters) notifyAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, set := range w.m {
		wake(set)
	}
}

// handleReceiptWait answers with the message's receipt as soon as it is no
// longer in custody, or with the current state after ?timeout (at most
// MaxReceiptWait). It is one bounded request per wait, woken by the
// recipient's acknowledgement; the waiter subscribes before reading the
// state, so a change between the two is never missed. Response headers are
// sent at once so clients' header timeouts do not cut the wait short; the
// caller's membership is checked again before every answer, and revocation
// wakes waiters, so a revoked agent learns nothing more.
func (h *Hub) handleReceiptWait(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	timeout, err := time.ParseDuration(r.URL.Query().Get("timeout"))
	if err != nil || timeout < 0 {
		writeError(w, http.StatusBadRequest, "", "timeout must be a duration such as 5s")
		return
	}
	timeout = min(timeout, MaxReceiptWait)
	id := r.PathValue("id")
	ch := h.waiters.add(id)
	defer h.waiters.remove(id, ch)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	if _, _, _, err := h.store.messageState(id, caller); err != nil {
		writeError(w, http.StatusNotFound, "", "unknown message")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	http.NewResponseController(w).Flush()
	answer := func(state string) {
		if a, err := h.store.agent(caller); err != nil || a.Revoked {
			json.NewEncoder(w).Encode(protocol.Receipt{ID: id}) // no state for a revoked caller
			return
		}
		json.NewEncoder(w).Encode(protocol.Receipt{ID: id, State: state})
	}
	for {
		_, _, state, err := h.store.messageState(id, caller)
		if err != nil {
			answer("")
			return
		}
		if state != protocol.StateCustody {
			answer(state)
			return
		}
		select {
		case <-ch:
			if a, err := h.store.agent(caller); err != nil || a.Revoked {
				answer("")
				return
			}
		case <-deadline.C:
			answer(state)
			return
		case <-r.Context().Done():
			return
		case <-h.done:
			answer(state)
			return
		}
	}
}
