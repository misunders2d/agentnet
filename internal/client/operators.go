package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Operators: on a machine nobody sits at (a server running a bot), the
// person responsible names, once, the devices that may decide its waiting
// requests from their own messenger: accept, decline, resolve, reply, stop.
// A grant names one address and its exact pinned key, recorded here by the
// local person only; nothing received, no label, no admin role and no
// review destination ever grants it. A decision counts only while the
// sender's verified key is that granted key and it is still the pinned
// one with no change pending (as task grants do, taskgrant.go). Granted
// operators also receive this machine's review reports with the requests
// named (reviewnotice.go); nobody else gets more than a count.

// operatorHoldsFor is the grant condition for an address and a verifying
// fingerprint, as SQL expressions.
const operatorHoldsFor = `EXISTS (SELECT 1 FROM operators g JOIN peers p ON p.address = g.address
	WHERE g.address = %s AND g.fingerprint = %s AND p.public = g.public AND p.pending IS NULL)`

// ErrNotOperator means the sender is not a granted operator here.
var ErrNotOperator = errors.New("not an operator of this machine")

// operatorHolds reports whether a decision from address verified by
// fingerprint fp may be applied here.
func operatorHolds(q querier, address, fp string) (bool, error) {
	if fp == "" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(`SELECT `+fmt.Sprintf(operatorHoldsFor, "?", "?"), address, fp).Scan(&ok)
	return ok, err
}

// GrantOperator lets the device at address, under its currently pinned
// key, decide this machine's waiting requests from its messenger, and
// receive this machine's reports with the requests named. It returns the
// granted key's fingerprint.
func (a *Agent) GrantOperator(address string) (string, error) {
	if address == a.Address {
		return "", errors.New("this machine's own person decides here already")
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var pub string
	var pending sql.NullString
	switch err := tx.QueryRow(`SELECT public, pending FROM peers WHERE address = ?`, address).Scan(&pub, &pending); {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrNoPinnedKey
	case err != nil:
		return "", err
	case pending.Valid:
		return "", ErrKeyPending
	}
	var key identity.Public
	if err := json.Unmarshal([]byte(pub), &key); err != nil {
		return "", err
	}
	fp := key.Fingerprint()
	active, err := operatorHolds(tx, address, fp)
	if err != nil {
		return "", err
	}
	if active {
		return fp, tx.Commit() // renewing an unchanged active grant reports nothing again
	}
	if _, err := tx.Exec(`INSERT INTO operators(address, fingerprint, public, added_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET fingerprint = excluded.fingerprint, public = excluded.public, added_at = excluded.added_at`,
		address, fp, pub, time.Now().Unix()); err != nil {
		return "", err
	}
	// A count sent before this exact grant must not suppress its actionable
	// snapshot. Other recipients and already-settled items keep their marks.
	args := append([]any{address}, alertReviewStates...)
	args = append(args, envelope.KindMessage, envelope.StatusReviewNotice)
	if _, err := tx.Exec(`DELETE FROM reported WHERE recipient = ? AND item IN
		(SELECT id FROM inbox WHERE `+inAlertReview+` AND NOT (`+receivedNotice+`))`, args...); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO config(k, v) VALUES(?, ?)`, reviewToGenKey, protocol.NewID()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return fp, nil
}

// RevokeOperator ends a grant. Decisions already applied stay applied.
func (a *Agent) RevokeOperator(address string) error {
	res, err := a.store.db.Exec(`DELETE FROM operators WHERE address = ?`, address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no operator grant for " + address)
	}
	a.store.changed()
	notifyDaemon(a.home)
	return nil
}

// Operators lists the grants with the granted key and whether it still
// holds (the key is still the pinned one).
func (a *Agent) Operators() ([]Grant, error) {
	rows, err := a.store.db.Query(`SELECT g.address, g.fingerprint, g.public = p.public, p.public IS NOT NULL, p.pending IS NOT NULL
		FROM operators g LEFT JOIN peers p ON p.address = g.address ORDER BY g.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		var same, pinned, pending sql.NullBool
		if err := rows.Scan(&g.Address, &g.Fingerprint, &same, &pinned, &pending); err != nil {
			return nil, err
		}
		switch {
		case !pinned.Bool:
			g.Status = "inactive: no key pinned"
		case pending.Bool:
			g.Status = "inactive: key change pending (verify, trust, then grant again)"
		case !same.Bool:
			g.Status = "inactive: key changed since the grant (grant again to renew)"
		default:
			g.Status = "active"
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// activeOperators lists the addresses whose grant holds now.
func (s *store) activeOperators() ([]string, error) {
	rows, err := s.db.Query(`SELECT g.address FROM operators g JOIN peers p ON p.address = g.address WHERE p.public = g.public AND p.pending IS NULL ORDER BY g.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, rows.Err()
}
