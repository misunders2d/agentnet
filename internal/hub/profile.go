package hub

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Capability records and profiles (human DMs). A device publishes its own
// records, signed by its key; the Hub checks that signature and that the
// record names the calling device, stores it, and serves it as signed. It
// never makes or changes one, so it can withhold a record but not forge
// one. Which sessions are live is the Hub's own statement (see
// protocol.Profile). Person rosters are persons.go's.

// handlePutCaps stores a capability record of one of the caller's sessions.
// An older record for the same session never replaces a newer one.
func (h *Hub) handlePutCaps(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	rec, err := protocol.ParseCapsRecord(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	a, err := h.store.agent(caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if rec.Address != caller {
		writeError(w, http.StatusBadRequest, "", "the record must name the caller")
		return
	}
	if err := rec.Verify(a.Public.SignKey); err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	if err := h.store.putCaps(caller, rec.Session, rec.TS, body); err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.membersChanged() // senders waiting for this device look again
	w.WriteHeader(http.StatusNoContent)
}

// handleProfile answers what a device published and which of its sessions
// decide what it can read: the live ones, or the one that connected last.
func (h *Hub) handleProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	address := protocol.Address(r.PathValue("label"), r.PathValue("agent"))
	live := h.presence.sessionIDs(address)
	person, last, caps, err := h.store.profileRows(address, live)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "", "unknown agent")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	p := protocol.Profile{Sessions: live, Live: len(live) > 0}
	if len(live) == 0 && last != "" {
		p.Sessions = []string{last}
	}
	if person != "" {
		p.Person = json.RawMessage(person)
	}
	for _, s := range p.Sessions {
		if rec, ok := caps[s]; ok {
			p.Caps = append(p.Caps, json.RawMessage(rec))
		}
	}
	writeJSON(w, http.StatusOK, p)
}

// features lists what this Hub supports (GET /v1/version).
var features = []string{protocol.FeatureCaps, protocol.FeatureEnv2, protocol.FeatureMembers, protocol.FeaturePerson, protocol.FeatureNotify}
