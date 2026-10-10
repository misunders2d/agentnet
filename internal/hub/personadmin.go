package hub

import (
	"database/sql"
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

// Unattributed grants from the branch draft cannot establish authority.
// This is an appended schema step; shipped steps remain untouched.
const deviceAdminSchema = `
ALTER TABLE agents ADD COLUMN admin_granted_by TEXT;
UPDATE agents SET admin = 0 WHERE linked = 1;
CREATE INDEX agents_admin_granter ON agents(admin_granted_by);
`

func currentPersonDevice(q querier, a agent) (protocol.PersonRoster, bool, error) {
	head, ok, err := personHeadIn(q, a.Person)
	if err != nil || !ok {
		return protocol.PersonRoster{}, false, err
	}
	r, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return protocol.PersonRoster{}, false, nil
	}
	return r, !a.Revoked && !a.Pending && r.Person == a.Person && r.Seq == head.seq && r.Hash() == head.hash && r.Has(a.Public.Address, a.Public.Fingerprint()), nil
}

// A linked role holds only while its complete grant chain leads to an
// active invite admin and every edge still joins exact current person keys.
func deviceAdminHolds(q querier, address string, a agent, seen map[string]bool) (bool, error) {
	if seen[address] || !a.Admin || a.Revoked || a.Pending {
		return false, nil
	}
	seen[address] = true
	if !a.linked {
		return true, nil
	}
	if a.Person == "" {
		return false, nil
	}
	if a.grantedBy == "" {
		// A Google person's current devices hold the role the workspace's
		// admin gave that email (google.go); no other linked device does.
		if ok, err := googleEmailAdmin(q, a.Person); err != nil || !ok {
			return false, err
		}
		_, current, err := currentPersonDevice(q, a)
		return current, err
	}
	r, current, err := currentPersonDevice(q, a)
	if err != nil || !current {
		return false, err
	}
	by, err := rawAgentIn(q, a.grantedBy)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil || by.Person != a.Person || !r.Has(by.Public.Address, by.Public.Fingerprint()) {
		return false, err
	}
	return deviceAdminHolds(q, a.grantedBy, by, seen)
}

// Delete descendants when a source grant ends, so a later re-grant cannot
// silently restore old authority. The caller owns the transaction.
func revokeDeviceAdminGrants(tx *sql.Tx, address, by string) error {
	rows, err := tx.Query(`WITH RECURSIVE descendants(address) AS (
		SELECT address FROM agents WHERE admin_granted_by = ?
		UNION SELECT a.address FROM agents a JOIN descendants d ON a.admin_granted_by = d.address
	) SELECT address, coalesce(person_id,'') FROM agents WHERE admin=1 AND address IN (SELECT address FROM descendants)`, address)
	if err != nil {
		return err
	}
	type withdrawn struct{ address, person string }
	var targets []withdrawn
	for rows.Next() {
		var d withdrawn
		if err := rows.Scan(&d.address, &d.person); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range targets {
		if d.person != "" {
			if err := addDeviceAdminNotice(tx, d.person, d.address, by, false); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`WITH RECURSIVE descendants(address) AS (
		SELECT address FROM agents WHERE admin_granted_by = ?
		UNION SELECT a.address FROM agents a JOIN descendants d ON a.admin_granted_by = d.address
	) UPDATE agents SET admin = 0, admin_granted_by = NULL WHERE address IN (SELECT address FROM descendants)`, address)
	return err
}

// setDeviceAdmin sets the admin role of the device at address for the
// admin device caller: only another active device of the caller's person,
// admitted through a link.
func (s *store) setDeviceAdmin(caller, address string, admin bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	me, err := agentIn(tx, caller)
	if err != nil {
		return err
	}
	if !me.Admin || me.Revoked || me.Pending || me.Person == "" || address == caller {
		return errNotOwnLinkedDevice
	}
	r, current, err := currentPersonDevice(tx, me)
	if err != nil {
		return err
	}
	target, err := agentIn(tx, address)
	if errors.Is(err, errNotFound) {
		return errNotOwnLinkedDevice
	}
	if err != nil {
		return err
	}
	if !current || !target.linked || target.Revoked || target.Pending || target.Person != me.Person || !r.Has(address, target.Public.Fingerprint()) {
		return errNotOwnLinkedDevice
	}
	// An idempotent re-grant cannot replace a valid edge with a cycle.
	if admin && target.Admin {
		return tx.Commit()
	}
	stored, err := rawAgentIn(tx, address)
	if err != nil {
		return err
	}
	if !admin && !stored.Admin && stored.grantedBy == "" {
		return tx.Commit()
	}
	if err := addDeviceAdminNotice(tx, me.Person, address, caller, admin); err != nil {
		return err
	}
	var by any
	if admin {
		by = caller
	} else if err := revokeDeviceAdminGrants(tx, address, caller); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE agents SET admin = ?, admin_granted_by = ? WHERE address = ?`, admin, by, address); err != nil {
		return err
	}
	return tx.Commit()
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
	h.lookAgain() // push the role change to the person's connected devices
	w.WriteHeader(http.StatusNoContent)
}

// googleEmailAdmin reports whether person signed in with Google under an
// email the workspace's admin made an admin (and has not removed).
func googleEmailAdmin(q querier, person string) (bool, error) {
	var admin bool
	err := q.QueryRow(`SELECT e.admin FROM google_people p JOIN google_emails e ON e.email = p.email WHERE p.person = ? AND e.denied = 0`, person).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return admin, err
}
