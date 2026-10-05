package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// Stewards (MEL-532): the grant may instead name a person, the steward,
// once, on this machine (operator grant --person, or person service
// --steward at install). It covers every current device of that person as
// the person's signed device list (roster) pinned here names it, now and
// later: a device added to the roster decides once this machine pins the
// newer roster step, one removed stops then. A person in conflict (frozen)
// covers nothing. Grants stay local: nothing received creates or widens
// one. The person's label is read from persons, never copied.
const operatorPersonsSchema = `
CREATE TABLE operator_persons(
  person TEXT PRIMARY KEY,
  added_at INTEGER NOT NULL);
`

// operatorHoldsFor is the grant condition for an address and a verifying
// fingerprint, as SQL expressions.
const operatorHoldsFor = `EXISTS (SELECT 1 FROM operators g JOIN peers p ON p.address = g.address
	WHERE g.address = %s AND g.fingerprint = %s AND p.public = g.public AND p.pending IS NULL)`

// ErrNotOperator means the sender is not a granted operator here.
var ErrNotOperator = errors.New("not an operator of this machine")

// operatorHolds reports whether a decision from address verified by
// fingerprint fp may be applied here: a device grant for that exact key,
// or a steward grant to a pinned person whose newest pinned roster step
// lists that device with that key, pinned here with no change pending.
func operatorHolds(q querier, address, fp string) (bool, error) {
	if fp == "" {
		return false, nil
	}
	var ok bool
	if err := q.QueryRow(`SELECT `+fmt.Sprintf(operatorHoldsFor, "?", "?"), address, fp).Scan(&ok); err != nil || ok {
		return ok, err
	}
	return stewardHolds(q, address, fp)
}

// stewardHolds is operatorHolds for a steward (person) grant.
func stewardHolds(q querier, address, fp string) (bool, error) {
	var pub string
	err := q.QueryRow(`SELECT p.public FROM operator_persons g
		JOIN persons pr ON pr.person = g.person AND pr.state = ?
		JOIN person_devices d ON d.person = g.person AND d.address = ? AND d.fingerprint = ?
		JOIN peers p ON p.address = d.address AND p.pending IS NULL`, personPinned, address, fp).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var key identity.Public
	if err := json.Unmarshal([]byte(pub), &key); err != nil {
		return false, err
	}
	return key.Fingerprint() == fp, nil
}

// PersonGrant is a steward grant: the person and the devices it covers now.
type PersonGrant struct {
	Person  string       `json:"person"`
	Label   string       `json:"label"`
	State   string       `json:"state"` // pinned, or conflict (frozen: covers nothing)
	Devices []DeviceInfo `json:"devices,omitempty"`
}

// GrantOperatorPerson names the person that the device at address speaks
// for as a steward here: every current and future device of that person,
// as its signed roster lists it, decides this machine's waiting requests
// from its own messenger and receives the reports with the requests named.
// The device's key is the one pinned here, or the directory's (trust on
// first use), and its person's chain is pinned from the Hub; the grant
// returns that person, its label and devices, for the person granting it
// to check. This installation's own person and a frozen person are
// refused.
func (a *Agent) GrantOperatorPerson(ctx context.Context, address string) (PersonGrant, error) {
	key, err := a.sendKey(ctx, address)
	if err != nil {
		return PersonGrant{}, err
	}
	p, err := a.personOfKey(ctx, address, key)
	if err != nil {
		return PersonGrant{}, err
	}
	switch p.info.State {
	case personSelf:
		return PersonGrant{}, errors.New("that is this installation's own person, who decides here already")
	case personConflict:
		return PersonGrant{}, errPersonConflict
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return PersonGrant{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO operator_persons(person, added_at) VALUES(?, ?) ON CONFLICT(person) DO NOTHING`, p.info.Person, time.Now().Unix()); err != nil {
		return PersonGrant{}, err
	}
	// A count sent to one of its devices before this grant must not
	// suppress the named snapshot (as GrantOperator).
	args := append([]any{p.info.Person}, alertReviewStates...)
	args = append(args, envelope.KindMessage, envelope.StatusReviewNotice)
	if _, err := tx.Exec(`DELETE FROM reported WHERE recipient IN (SELECT address FROM person_devices WHERE person = ?) AND item IN
		(SELECT id FROM inbox WHERE `+inAlertReview+` AND NOT (`+receivedNotice+`))`, args...); err != nil {
		return PersonGrant{}, err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO config(k, v) VALUES(?, ?)`, reviewToGenKey, protocol.NewID()); err != nil {
		return PersonGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonGrant{}, err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return PersonGrant{Person: p.info.Person, Label: p.info.Label, State: p.info.State, Devices: p.info.Devices}, nil
}

// RevokeOperatorPerson ends a steward grant, named by the person's id or
// by the address of one of its devices. Decisions already applied stay.
func (a *Agent) RevokeOperatorPerson(who string) (PersonGrant, error) {
	p, ok, err := a.store.personByID(who)
	if err == nil && !ok {
		p, ok, err = a.store.personByAddress(who)
	}
	if err != nil {
		return PersonGrant{}, err
	}
	if !ok {
		return PersonGrant{}, errors.New("no steward grant for " + who)
	}
	res, err := a.store.db.Exec(`DELETE FROM operator_persons WHERE person = ?`, p.info.Person)
	if err != nil {
		return PersonGrant{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PersonGrant{}, errors.New("no steward grant for " + who)
	}
	a.store.changed()
	notifyDaemon(a.home)
	return PersonGrant{Person: p.info.Person, Label: p.info.Label, State: p.info.State, Devices: p.info.Devices}, nil
}

// Stewards lists the steward grants with each person's current devices.
func (a *Agent) Stewards() ([]PersonGrant, error) {
	rows, err := a.store.db.Query(`SELECT person FROM operator_persons ORDER BY added_at, person`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []PersonGrant
	for _, id := range ids {
		p, ok, err := a.store.personByID(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, PersonGrant{Person: id, State: "unknown"})
			continue
		}
		out = append(out, PersonGrant{Person: id, Label: p.info.Label, State: p.info.State, Devices: p.info.Devices})
	}
	return out, nil
}

// stewardOf reports whether person holds a steward grant here.
func stewardOf(q querier, person string) bool {
	var n int
	q.QueryRow(`SELECT count(*) FROM operator_persons WHERE person = ?`, person).Scan(&n)
	return n > 0
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

// activeOperators lists the addresses whose grant holds now: device grants
// under their pinned key, and the current devices of each pinned steward
// person, whose key is the roster's (or not pinned here yet: sending the
// report pins it, and the report goes only if it is the roster's key).
func (s *store) activeOperators() ([]string, error) {
	rows, err := s.db.Query(`SELECT g.address, '', '' FROM operators g JOIN peers p ON p.address = g.address WHERE p.public = g.public AND p.pending IS NULL
		UNION ALL SELECT d.address, d.fingerprint, coalesce(p.public, '') FROM operator_persons g
		  JOIN persons pr ON pr.person = g.person AND pr.state = ?
		  JOIN person_devices d ON d.person = g.person
		  LEFT JOIN peers p ON p.address = d.address
		  WHERE p.address IS NULL OR p.pending IS NULL`, personPinned)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var addr, fp, pub string
		if err := rows.Scan(&addr, &fp, &pub); err != nil {
			return nil, err
		}
		if pub != "" {
			var key identity.Public
			if json.Unmarshal([]byte(pub), &key) != nil || key.Fingerprint() != fp {
				continue // its key here is not the roster's
			}
		}
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}

// Decider is who can decide this machine's waiting requests from their own
// devices, as a report names them: a steward person, or a granted device.
type Decider struct {
	Person  string `json:"person,omitempty"`
	Label   string `json:"label,omitempty"`
	Address string `json:"address,omitempty"`
}

// deciders lists the steward persons (pinned) and the device grants that
// hold now, for count-only reports: a device that may not decide names who
// can, never "decide on that machine".
func (s *store) deciders() ([]Decider, error) {
	var out []Decider
	rows, err := s.db.Query(`SELECT pr.person, pr.label FROM operator_persons g JOIN persons pr ON pr.person = g.person AND pr.state = ? ORDER BY g.added_at, pr.person`, personPinned)
	if err != nil {
		return nil, err
	}
	stewards := map[string]bool{}
	for rows.Next() {
		var d Decider
		if err := rows.Scan(&d.Person, &d.Label); err != nil {
			rows.Close()
			return nil, err
		}
		stewards[d.Person] = true
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT g.address, coalesce(d.person, '') FROM operators g JOIN peers p ON p.address = g.address AND p.public = g.public AND p.pending IS NULL
		LEFT JOIN person_devices d ON d.address = g.address ORDER BY g.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var addr, person string
		if err := rows.Scan(&addr, &person); err != nil {
			return nil, err
		}
		if person != "" && stewards[person] {
			continue // its person is named already
		}
		out = append(out, Decider{Address: addr})
	}
	return out, rows.Err()
}
