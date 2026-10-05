package hub

import (
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A person's devices do not inherit the Hub admin role: changing company
// settings (the workspace name, invites, release notices, storage) is one of
// the things a person grants with their own tap. A device that holds the
// role gives it to another device of its own person, or takes it back
// (POST /v1/person/device-admin); nothing else does. So the owner's phone
// can name the workspace once the owner says so on their laptop, while a
// device that runs agents (a laptop, a shared agent server) stays a member
// unless the person grants it. Only devices admitted through a link of
// that person can be changed this way: a device whose own invite made it
// an admin keeps that. The grant ends with the link: a linked device
// removed from its person is revoked (persons.go).

var errNotOwnLinkedDevice = errors.New("that is not another device linked to your person on this Hub")

// setDeviceAdmin sets the admin role of the device at address for the
// admin device caller: only another active device of the caller's person,
// admitted through a link.
func (s *store) setDeviceAdmin(caller, address string, admin bool) error {
	me, err := s.agent(caller)
	if err != nil {
		return err
	}
	if me.Person == "" || address == caller {
		return errNotOwnLinkedDevice
	}
	res, err := s.db.Exec(`UPDATE agents SET admin = ? WHERE address = ? AND person_id = ? AND linked = 1
		AND revoked_at IS NULL AND pending_person IS NULL`, admin, address, me.Person)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errNotOwnLinkedDevice
	}
	return nil
}

// handleDeviceAdmin grants or withdraws the admin role of another device of
// the caller's person. Admin only.
func (h *Hub) handleDeviceAdmin(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.DeviceAdminRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed device admin request")
		return
	}
	if _, _, err := protocol.SplitAddress(req.Address); err != nil {
		writeError(w, http.StatusBadRequest, "", "invalid address")
		return
	}
	switch err := h.store.setDeviceAdmin(caller, req.Address, req.Admin); {
	case errors.Is(err, errNotOwnLinkedDevice):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if req.Admin {
		h.cfg.Logf("%s may now change company settings, granted by %s", req.Address, caller)
	} else {
		h.cfg.Logf("%s may no longer change company settings, by %s", req.Address, caller)
	}
	w.WriteHeader(http.StatusNoContent)
}
