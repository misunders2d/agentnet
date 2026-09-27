package hub

import (
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
// state, so a change between the two is never missed.
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
	for {
		_, _, state, err := h.store.messageState(id, caller)
		if err != nil {
			writeError(w, http.StatusNotFound, "", "unknown message")
			return
		}
		if state != protocol.StateCustody {
			writeJSON(w, http.StatusOK, protocol.Receipt{ID: id, State: state})
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			writeJSON(w, http.StatusOK, protocol.Receipt{ID: id, State: state})
			return
		case <-r.Context().Done():
			return
		case <-h.done:
			writeJSON(w, http.StatusOK, protocol.Receipt{ID: id, State: state})
			return
		}
	}
}
