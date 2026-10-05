package hub

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Person roster chains and device links (protocol/person.go, link.go).
//
// The Hub keeps each person's chain as its devices publish it, strictly
// linear: the next step must follow the newest one it holds, so two devices
// racing get one success and one "stale", never a fork it stores. It checks
// every signature against the keys it admitted, but it is not trusted for
// them: clients verify the chain themselves.
//
// A device invite admits a device only as PENDING for one person: not a
// member (no list entry, profile, directory entry or message either way),
// with only its stream, until a device of that person publishes the step
// that adds it (activation, in the same transaction) or it is refused or
// expires (revocation). A device removed from its person is revoked too if
// a link admitted it, so a link never leaves a standalone member behind.

var (
	errRosterStale    = errors.New("the person's roster changed meanwhile; this step does not follow it")
	errLinkGone       = errors.New("that device is not waiting to be linked (refused, expired or never invited)")
	errTooManyDevices = errors.New("the person already has the most devices it can have")
	errHasPerson      = errors.New("this device already speaks for a person")
	errNotPersonal    = errors.New("only a device of a person can link devices to it")
	errBadStep        = errors.New("roster step refused")
)

// personHead is the newest step of a person's chain the Hub holds.
type personHead struct {
	seq    int64
	hash   string
	record []byte
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
}

func personHeadIn(q querier, person string) (personHead, bool, error) {
	var h personHead
	var rec string
	err := q.QueryRow(`SELECT seq, hash, record FROM persons WHERE person = ?`, person).Scan(&h.seq, &h.hash, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	h.record = []byte(rec)
	return h, err == nil, err
}

// devicesAvailable reports whether person can take one more device: its
// current devices and those pending for it stay within the limit.
func devicesAvailable(tx *sql.Tx, person string, head personHead) (bool, error) {
	r, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return false, err
	}
	var pending int
	if err := tx.QueryRow(`SELECT count(*) FROM agents WHERE pending_person = ? AND revoked_at IS NULL AND pending_until > ?`, person, time.Now().Unix()).Scan(&pending); err != nil {
		return false, err
	}
	return len(r.Devices)+pending < protocol.MaxPersonDevices, nil
}

// enrollPending registers a device joining with a device invite for person,
// PENDING: its join signature must consent to exactly the step after the
// person's newest one.
func enrollPending(tx *sql.Tx, person, inviter string, pub identity.Public, data []byte, label string, link *protocol.JoinLink, expires int64) error {
	head, ok, err := personHeadIn(tx, person)
	if err != nil {
		return err
	}
	if !ok || !ed25519.Verify(pub.SignKey, protocol.JoinBytes(person, head.seq+1, head.hash, pub), link.Join) {
		return errRosterStale
	}
	if room, err := devicesAvailable(tx, person, head); err != nil {
		return err
	} else if !room {
		return errTooManyDevices
	}
	ev, _ := json.Marshal(protocol.LinkEvent{Offer: link.Offer, Device: pub, Join: link.Join, MAC: link.MAC})
	_, err = tx.Exec(`INSERT INTO agents(address, label, public, admin, created_at, linked, pending_person, pending_inviter, pending_until, pending_event)
		VALUES(?, ?, ?, 0, ?, 1, ?, ?, ?, ?)`,
		pub.Address, label, string(data), time.Now().Unix(), person, inviter, expires+protocol.PendingGrace, string(ev))
	return err
}

// stepResult is what storing a roster step changed.
type stepResult struct {
	same      bool     // the step was stored already: nothing changed
	activated string   // the pending device it admitted
	removed   []string // devices it took out of the person
}

// putPersonStep stores r, published by caller, as the next step of its
// person's chain, with everything it implies, in one transaction.
func (s *store) putPersonStep(caller identity.Public, raw []byte, r protocol.PersonRoster, now time.Time) (stepResult, error) {
	var res stepResult
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	var stored string
	switch err := tx.QueryRow(`SELECT hash FROM person_chain WHERE person = ? AND seq = ?`, r.Person, r.Seq).Scan(&stored); {
	case err == nil && stored == r.Hash():
		res.same = true
		return res, nil
	case err == nil:
		return res, errRosterStale
	case !errors.Is(err, sql.ErrNoRows):
		return res, err
	}
	me, err := agentIn(tx, caller.Address)
	if err != nil {
		return res, err
	}
	if r.Seq == 0 {
		// Email-bearing first rosters are created atomically by Google join,
		// never by an invite-code device's self-assertion.
		if r.Email != "" {
			return res, errBadStep
		}
		if err := r.VerifyFirst(); err != nil {
			return res, fmt.Errorf("%w: %v", errBadStep, err)
		}
		if r.Devices[0].Address != caller.Address || r.Devices[0].Fingerprint() != caller.Fingerprint() {
			return res, fmt.Errorf("%w: a person's first roster must name the device that publishes it", errBadStep)
		}
		if me.Person != "" {
			return res, errHasPerson
		}
		if _, ok, err := personHeadIn(tx, r.Person); err != nil {
			return res, err
		} else if ok {
			return res, errRosterStale
		}
	} else {
		head, ok, err := personHeadIn(tx, r.Person)
		if err != nil {
			return res, err
		}
		if !ok || r.Seq != head.seq+1 || r.Prev != head.hash {
			return res, errRosterStale
		}
		prev, err := protocol.ParsePersonRoster(head.record)
		if err != nil {
			return res, err
		}
		added, err := r.VerifyNext(prev)
		if err != nil {
			return res, fmt.Errorf("%w: %v", errBadStep, err)
		}
		if signer, _ := prev.Device(r.By); signer.Address != caller.Address || r.By != caller.Fingerprint() || me.Person != r.Person {
			return res, fmt.Errorf("%w: a roster step is published by the device that signed it", errBadStep)
		}
		if added != nil {
			var inviter string
			var until int64
			err := tx.QueryRow(`SELECT pending_inviter, pending_until FROM agents WHERE address = ? AND pending_person = ? AND revoked_at IS NULL`,
				added.Address, r.Person).Scan(&inviter, &until)
			if errors.Is(err, sql.ErrNoRows) || err == nil && ((inviter != caller.Address && prev.Email == "") || until <= now.Unix()) {
				return res, errLinkGone
			}
			if err != nil {
				return res, err
			}
			a, err := agentIn(tx, added.Address)
			if err != nil {
				return res, err
			}
			if a.Public.Fingerprint() != added.Fingerprint() {
				return res, errLinkGone
			}
			if _, err := tx.Exec(`UPDATE agents SET pending_person = NULL, pending_inviter = NULL, pending_until = NULL, pending_event = NULL, person_id = ?
				WHERE address = ?`, r.Person, added.Address); err != nil {
				return res, err
			}
			res.activated = added.Address
			if r.Email != "" {
				if _, err := tx.Exec(`UPDATE agents SET admin=coalesce((SELECT admin FROM google_emails WHERE email=? AND denied=0),0) WHERE address=?`, r.Email, added.Address); err != nil {
					return res, err
				}
			}
		}
		for _, d := range prev.Devices {
			if r.Has(d.Address, d.Fingerprint()) {
				continue
			}
			res.removed = append(res.removed, d.Address)
			if _, err := tx.Exec(`UPDATE agents SET person_id = NULL WHERE address = ? AND person_id = ?`, d.Address, r.Person); err != nil {
				return res, err
			}
			rev, err := tx.Exec(`UPDATE agents SET revoked_at = ?, revoked_reason = 'removed' WHERE address = ? AND linked = 1 AND revoked_at IS NULL`, now.Unix(), d.Address)
			if err != nil {
				return res, err
			}
			if n, _ := rev.RowsAffected(); n > 0 {
				if err := dropNotify(tx, d.Address); err != nil {
					return res, err
				}
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO person_chain(person, seq, hash, record) VALUES(?, ?, ?, ?)`, r.Person, r.Seq, r.Hash(), string(raw)); err != nil {
		return res, err
	}
	if _, err := tx.Exec(`INSERT INTO persons(person, seq, hash, record) VALUES(?, ?, ?, ?)
		ON CONFLICT(person) DO UPDATE SET seq = excluded.seq, hash = excluded.hash, record = excluded.record`, r.Person, r.Seq, r.Hash(), string(raw)); err != nil {
		return res, err
	}
	if r.Seq == 0 {
		if _, err := tx.Exec(`UPDATE agents SET person_id = ? WHERE address = ?`, r.Person, caller.Address); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}

// handlePutPerson stores the next step of the caller's person's chain.
func (h *Hub) handlePutPerson(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	roster, err := protocol.ParsePersonRoster(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	}
	a, err := h.store.agent(caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	res, err := h.store.putPersonStep(a.Public, body, roster, time.Now())
	switch {
	case errors.Is(err, errRosterStale):
		writeError(w, http.StatusConflict, protocol.CodeRosterStale, err.Error())
		return
	case errors.Is(err, errLinkGone):
		writeError(w, http.StatusConflict, protocol.CodeLinkExpired, err.Error())
		return
	case errors.Is(err, errHasPerson):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case errors.Is(err, errBadStep):
		writeError(w, http.StatusBadRequest, "", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if !res.same {
		if res.activated != "" {
			h.cfg.Logf("linked %s to its person", res.activated)
			h.streams.notify(res.activated) // its pending stream closes; it reconnects as a member
		}
		for _, d := range res.removed {
			h.streams.notify(d)
		}
		h.membersChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceInvite gives a device of a person a one-use invite that admits
// one more device of that person, pending its approval.
func (h *Hub) handleDeviceInvite(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.DeviceInviteRequest
	now := time.Now().Unix()
	if err := decodeStrict(body, &req); err != nil || !protocol.ValidID(req.Offer) || req.Expires <= now || req.Expires > now+protocol.MaxLinkTTL {
		writeError(w, http.StatusBadRequest, "", "a device invite needs an offer id and an expiry within 10 minutes")
		return
	}
	secret := protocol.NewID() + protocol.NewID()
	label, err := h.store.createDeviceInvite(caller, secret, req)
	switch {
	case errors.Is(err, errNotPersonal), errors.Is(err, errTooManyDevices):
		writeError(w, http.StatusConflict, "", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	code := protocol.Invite{Hub: h.cfg.PublicURL, Label: label, Secret: secret, CertPEM: h.certPEM}.Encode()
	writeJSON(w, http.StatusCreated, protocol.DeviceInvite{Code: code})
}

// createDeviceInvite stores a device invite for the caller's person, if the
// caller is one of its current devices and the person has room.
func (s *store) createDeviceInvite(caller, secret string, req protocol.DeviceInviteRequest) (label string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	me, err := agentIn(tx, caller)
	if err != nil {
		return "", err
	}
	if me.Person == "" {
		return "", errNotPersonal
	}
	head, ok, err := personHeadIn(tx, me.Person)
	if err != nil {
		return "", err
	}
	r, perr := protocol.ParsePersonRoster(head.record)
	if !ok || perr != nil || !r.Has(caller, me.Public.Fingerprint()) {
		return "", errNotPersonal
	}
	if room, err := devicesAvailable(tx, me.Person, head); err != nil {
		return "", err
	} else if !room {
		return "", errTooManyDevices
	}
	label, _, _ = protocol.SplitAddress(caller)
	if _, err := tx.Exec(`INSERT INTO invites(secret_hash, label, admin, expires_at, created_by, person, offer) VALUES(?, ?, 0, ?, ?, ?, ?)`,
		protocol.HashSecret(secret), label, req.Expires, caller, me.Person, req.Offer); err != nil {
		return "", err
	}
	return label, tx.Commit()
}

// handleDeviceRefuse revokes a pending device the caller invited.
func (h *Hub) handleDeviceRefuse(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.DeviceRefusal
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "malformed refusal")
		return
	}
	n, err := h.store.endPending(`address = ? AND (pending_inviter = ? OR (json_extract(pending_event, '$.google') IS NOT NULL AND pending_person IN (SELECT person_id FROM agents WHERE address=? AND revoked_at IS NULL AND pending_person IS NULL)))`, "refused", req.Address, caller, caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	if len(n) == 0 {
		writeError(w, http.StatusNotFound, "", "no such device waits for your approval")
		return
	}
	h.streams.disconnect(req.Address)
	w.WriteHeader(http.StatusNoContent)
}

// endPending revokes the pending devices matching where (with args), for
// reason, and returns their addresses.
func (s *store) endPending(where, reason string, args ...any) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT address FROM agents WHERE pending_person IS NOT NULL AND revoked_at IS NULL AND `+where, args...)
	if err != nil {
		return nil, err
	}
	var ended []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return nil, err
		}
		ended = append(ended, a)
	}
	rows.Close()
	for _, a := range ended {
		if _, err := tx.Exec(`UPDATE agents SET revoked_at = ?, revoked_reason = ? WHERE address = ?`, time.Now().Unix(), reason, a); err != nil {
			return nil, err
		}
	}
	return ended, tx.Commit()
}

// pendingLinks returns the "link" events of the devices waiting for
// inviter's approval.
func (s *store) pendingLinks(inviter string) (map[string][]byte, error) {
	rows, err := s.db.Query(`SELECT address, pending_event FROM agents WHERE (pending_inviter = ? OR (json_extract(pending_event, '$.google') IS NOT NULL AND pending_person IN (SELECT person_id FROM agents WHERE address=? AND revoked_at IS NULL AND pending_person IS NULL))) AND pending_person IS NOT NULL AND revoked_at IS NULL AND pending_until > ?`,
		inviter, inviter, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var a, ev string
		if err := rows.Scan(&a, &ev); err != nil {
			return nil, err
		}
		out[a] = []byte(ev)
	}
	return out, rows.Err()
}

// handleChain returns a page of a person's roster chain after seq "after".
func (h *Hub) handleChain(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	person := r.PathValue("id")
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "" {
		after, err = -1, nil
	}
	if err != nil || !protocol.ValidID(person) {
		writeError(w, http.StatusBadRequest, "", "a chain request names a person and a seq")
		return
	}
	page, err := h.store.chainPage(person, after)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// chainPage returns the steps after seq, oldest first, bounded by count
// and bytes.
func (s *store) chainPage(person string, after int64) (protocol.PersonChain, error) {
	page := protocol.PersonChain{Records: []json.RawMessage{}}
	rows, err := s.db.Query(`SELECT record FROM person_chain WHERE person = ? AND seq > ? ORDER BY seq LIMIT ?`, person, after, protocol.MaxChainPage+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return page, err
		}
		if len(page.Records) == protocol.MaxChainPage || size+len(rec) > protocol.MaxChainPageBytes {
			page.More = true
			break
		}
		size += len(rec)
		page.Records = append(page.Records, json.RawMessage(rec))
	}
	return page, rows.Err()
}
