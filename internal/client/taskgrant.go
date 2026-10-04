package client

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Task grants: the local user may let tasks from one sender run without
// asking each time, for that sender's exact key. A grant is recorded with
// the key's fingerprint and the pinned key itself. A task runs under it
// only if the key that verified the task (inbox.verified_by, recorded when
// it was verified) is the granted one and, when inserted and again when the
// worker claims it, that key is still the pinned one with no change
// pending. Anything else waits for the person, visibly: every change that
// ends a grant's hold moves the sender's pending tasks back to awaiting.
// Nothing received can create, widen or move a grant; tasks still run with
// the harness's normal task permissions.

// grantHoldsFor is the grant condition for an address and a verifying
// fingerprint, given as SQL expressions.
const grantHoldsFor = `EXISTS (SELECT 1 FROM task_grants g JOIN peers p ON p.address = g.address
	WHERE g.address = %s AND g.fingerprint = %s AND p.public = g.public AND p.pending IS NULL)`

// taskGrantHolds is grantHoldsFor for an inbox row.
var taskGrantHolds = fmt.Sprintf(grantHoldsFor, "inbox.sender", "inbox.verified_by")

// Errors for task grants.
var (
	ErrNoPinnedKey   = errors.New("no key is pinned for this agent yet; it is pinned when you first exchange a message")
	ErrKeyPending    = errors.New("this agent's key may have changed; verify it and run `agentnet trust` first")
	ErrTaskKeyDiffer = errors.New("this task was not verified with the agent's currently pinned key (or predates key tracking); accept it once with `agentnet accept ID`")
)

// taskGranted reports whether a task from address verified by fingerprint
// verifiedBy may run without asking.
func taskGranted(q querier, address, verifiedBy string) (bool, error) {
	if verifiedBy == "" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(`SELECT `+fmt.Sprintf(grantHoldsFor, "?", "?"), address, verifiedBy).Scan(&ok)
	return ok, err
}

// demoteGranted moves address's pending tasks, except those verified by
// keep (if set), back to awaiting the person, saying why.
func demoteGranted(tx *sql.Tx, address, keep, why string) error {
	_, err := tx.Exec(`UPDATE inbox SET state = ?, detail = ?, notified = 0, review_sent = 0
		WHERE sender = ? AND kind = ? AND state = ? AND (? = '' OR verified_by IS NOT ?)`,
		stateAwaiting, "not run without asking: "+why+"; accept ID runs it once", address, envelope.KindTask, statePending, keep, keep)
	if err == nil { // back in review: reported afresh to each recipient
		_, err = tx.Exec(`DELETE FROM reported WHERE item IN (SELECT id FROM inbox WHERE sender = ? AND kind = ? AND state = ? AND review_sent = 0)`, address, envelope.KindTask, stateAwaiting)
	}
	return err
}

// grant records a task grant for address's currently pinned key, within tx,
// and returns its fingerprint. Pending tasks under any other key wait again.
func grant(tx *sql.Tx, address string) (string, error) {
	var public string
	var pending sql.NullString
	err := tx.QueryRow(`SELECT public, pending FROM peers WHERE address = ?`, address).Scan(&public, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoPinnedKey
	}
	if err != nil {
		return "", err
	}
	if pending.Valid {
		return "", ErrKeyPending
	}
	key, _, err := pinnedKey(tx, address)
	if err != nil {
		return "", err
	}
	fp := key.Fingerprint()
	if _, err := tx.Exec(`INSERT INTO task_grants(address, fingerprint, public, added_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET fingerprint = excluded.fingerprint, public = excluded.public, added_at = excluded.added_at`,
		address, fp, public, time.Now().Unix()); err != nil {
		return "", err
	}
	return fp, demoteGranted(tx, address, fp, "the task grant is now for a different key")
}

// GrantTasks lets future tasks from address run without asking, for its
// currently pinned key only. It returns that key's fingerprint. Tasks
// already waiting are not affected.
func (a *Agent) GrantTasks(address string) (string, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	fp, err := grant(tx, address)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	notifyDaemon(a.home) // tasks it moved back to awaiting need the person
	return fp, nil
}

// AcceptAlways accepts task id once and, in the same transaction, grants
// future tasks from its sender, but only for the key that verified this
// task and only while that key is still the pinned one.
func (a *Agent) AcceptAlways(id string) (sender, fp string, err error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var kind, state string
	var verifiedBy sql.NullString
	var conv bool
	err = tx.QueryRow(`SELECT sender, kind, state, verified_by, conv IS NOT NULL FROM inbox WHERE id = ?`, id).Scan(&sender, &kind, &state, &verifiedBy, &conv)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("no inbox message %s", id)
	}
	if err != nil {
		return "", "", err
	}
	if conv {
		return "", "", ErrConversationItem
	}
	if kind != envelope.KindTask {
		return "", "", errors.New("--always is for tasks; to answer an agent's questions automatically use agentnet approve ADDRESS")
	}
	if fp, err = grant(tx, sender); err != nil {
		return "", "", err
	}
	if !verifiedBy.Valid || verifiedBy.String != fp {
		return "", "", ErrTaskKeyDiffer
	}
	res, err := tx.Exec(`UPDATE inbox SET state = ? WHERE id = ? AND state IN (?, ?, ?, ?, ?)`,
		stateAccepted, id, stateAwaiting, stateInterrupt, stateJobFailed, stateCancelled, stateNeedHuman)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", ErrNotPending
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	notifyDaemon(a.home)
	a.noteStatus(id) // the requester learns it is queued (told by the daemon)
	return sender, fp, nil
}

// RevokeTasks ends address's task grant. Its tasks not yet started wait for
// the person again; the ids of ones running now are returned (they may
// finish unless cancelled). Accepted-once tasks are unaffected.
func (a *Agent) RevokeTasks(address string) (running []string, err error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM task_grants WHERE address = ?`, address); err != nil {
		return nil, err
	}
	if err := demoteGranted(tx, address, "", "the task grant was revoked"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id FROM inbox WHERE sender = ? AND kind = ? AND state IN (?, ?) ORDER BY received_at, id`,
		address, envelope.KindTask, stateRunning, stateCancelReq)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		running = append(running, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	notifyDaemon(a.home) // tasks moved back to awaiting need the person
	return running, nil
}

// Grant is one approval, as TaskGrants and Approvals list them.
type Grant struct {
	Address     string
	Fingerprint string // task grants: the granted key
	Status      string // task grants: "active" or why not
}

// TaskGrants lists task grants with the granted key and whether it still
// holds (the same key pinned, no change pending).
func (a *Agent) TaskGrants() ([]Grant, error) {
	rows, err := a.store.db.Query(`SELECT g.address, g.fingerprint, g.public = p.public, p.public IS NOT NULL, p.pending IS NOT NULL
		FROM task_grants g LEFT JOIN peers p ON p.address = g.address ORDER BY g.address`)
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

// QuestionApprovals lists agents whose questions are answered automatically.
func (a *Agent) QuestionApprovals() ([]string, error) {
	rows, err := a.store.db.Query(`SELECT address FROM approvals ORDER BY address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
