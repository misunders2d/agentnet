package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

const discoveredHistory = "own-human-history/"

func insertHistoryJob(tx *sql.Tx, dev identity.Public) (bool, error) {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM conversations`).Scan(&n); err != nil {
		return false, err
	}
	now := time.Now().Unix()
	pos, _ := json.Marshal(historyPos{})
	res, err := tx.Exec(`INSERT OR IGNORE INTO history_jobs(device, fingerprint, pos, convs_total, state, created_at, updated_at) VALUES(?, ?, ?, ?, 'running', ?, ?)`, dev.Address, dev.Fingerprint(), string(pos), n, now, now)
	if err != nil {
		return false, err
	}
	added, err := res.RowsAffected()
	return added != 0, err
}

// Only the approving device used to start a snapshot. Each current own human
// may hold different accepted history. Discover its missing jobs on existing
// connection/roster wakes; never replace an old job or reset a cursor.
func (a *Agent) reconcileHistory() error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	own, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil || !ok {
		return err
	}
	var added []identity.Public
	for _, dev := range own.roster.Devices {
		if dev.Address == a.Address {
			continue
		}
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM history_jobs WHERE device=?`, dev.Address).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		if err := historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
			if errors.Is(err, errHistoryRecoveryAuthority) {
				continue
			}
			return err
		}
		inserted, err := insertHistoryJob(tx, dev)
		if err != nil {
			return err
		}
		if !inserted {
			continue
		}
		guard, _ := json.Marshal(historyRecoveryDevices{Sender: a.Self(), Reader: dev})
		if _, err := tx.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, discoveredHistory+dev.Address, string(guard)); err != nil {
			return err
		}
		added = append(added, dev)
	}
	if len(added) == 0 {
		return nil
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	for _, dev := range added {
		a.replayErased(dev)
	}
	return nil
}

// Explicitly approved legacy jobs retain their existing policy. Automatically
// discovered jobs keep their exact human-device authority through restart,
// each page transaction and eventual delivery of their queued copies.
func (a *Agent) discoveredHistoryCheck(q dbq, address string) error {
	var raw string
	err := q.QueryRow(`SELECT v FROM config WHERE k=?`, discoveredHistory+address).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var guard historyRecoveryDevices
	if err := json.Unmarshal([]byte(raw), &guard); err != nil {
		return err
	}
	if guard.Reader.Address != address || guard.Sender.Address != a.Address || !sameKeys(guard.Sender, a.Self()) {
		return errHistoryRecoveryAuthority
	}
	return historyRecoveryCurrent(q, guard.Sender, guard.Reader)
}

func (a *Agent) discoveredHistoryDelivery(env envelope.Envelope) error {
	var guarded int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox o JOIN config c ON c.k=? WHERE o.id=? AND o.recipient=? AND o.sub IN ('history','group-proof','group-context','root-sync')`, discoveredHistory+env.To, env.ID, env.To).Scan(&guarded); err != nil {
		return err
	}
	if guarded == 0 {
		return nil
	}
	return a.discoveredHistoryCheck(a.store.db, env.To)
}
