package hub

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The Hub operator may recommend one client version. It is kept in the
// database, pushed on every stream when a stream connects and whenever it
// changes, and never pushed again when set to the same value. A release
// relay pushes its own release instead when the operator's is older or
// none (update.go): the recommendation can no longer go stale. A newer one
// the operator names suspends nobody; member daemons with automatic update
// on install it from the project's release origin (client autoupdate.go).

func (s *store) release() (protocol.Release, error) {
	var r protocol.Release
	err := s.db.QueryRow(`SELECT version, url, note FROM release WHERE id = 1`).Scan(&r.Version, &r.URL, &r.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Release{}, nil
	}
	return r, err
}

func (s *store) setRelease(r protocol.Release, by string) error {
	if r.Version == "" {
		_, err := s.db.Exec(`DELETE FROM release`)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO release(id, version, url, note, set_by, set_at) VALUES(1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version, url = excluded.url, note = excluded.note,
		set_by = excluded.set_by, set_at = excluded.set_at`, r.Version, r.URL, r.Note, by, time.Now().Unix())
	return err
}

// loadRelease reads the stored recommendation at start.
func (h *Hub) loadRelease() error {
	r, err := h.store.release()
	if err == nil {
		h.release.Store(&r)
	}
	return err
}

// currentRelease returns the recommendation clients are told
// (pushedRelease: on a release relay, never older than its own) and its
// generation, which changes only when that recommendation does.
func (h *Hub) currentRelease() (protocol.Release, int64) {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	return h.pushedRelease(*h.release.Load()), h.releaseGen
}

func (h *Hub) handleRelease(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.ReleaseRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed release request")
		return
	}
	next := req.Release
	if req.Clear {
		next = protocol.Release{}
	} else if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	h.releaseMu.Lock()
	prev := *h.release.Load()
	changed, pushed := prev != next, h.pushedRelease(prev) != h.pushedRelease(next)
	err := h.store.setRelease(next, caller) // saved before any stream is woken
	if err == nil && changed {
		h.release.Store(&next)
		if pushed {
			h.releaseGen++
		}
	}
	h.releaseMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "could not store the release")
		return
	}
	if pushed { // a notice: who is suspended depends on the relay's own release only (update.go)
		h.streams.notifyAll()
	}
	if changed {
		h.cfg.Logf("client release recommendation set to %q by %s", next.Version, caller)
	}
	writeJSON(w, http.StatusOK, next)
}

func (h *Hub) handleReleaseGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	rel, _ := h.currentRelease()
	writeJSON(w, http.StatusOK, rel)
}
