package client

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

// An older own-device producer (v0.8.11–v0.8.13 history catch-up) re-sends a
// held sync record under fresh envelope IDs every time it wakes: hundreds of
// copies of a few records. Each copy keeps its own quarantine row, so its
// exact ciphertext, receive deduplication and truthful quarantined receipt
// are unchanged. While the row it copies is held, a copy is neither a notice
// nor checked on its own; once that row leaves, each copy is checked again.
const heldCopySchema = `
ALTER TABLE quarantine ADD COLUMN copy_key TEXT NOT NULL DEFAULT '';
ALTER TABLE quarantine ADD COLUMN copy_of TEXT NOT NULL DEFAULT '';
CREATE INDEX quarantine_copy_key ON quarantine(sender,copy_key) WHERE copy_key<>'';
CREATE INDEX quarantine_copy_of ON quarantine(copy_of) WHERE copy_of<>'';
`

// heldOwnRow is the SQL condition (on quarantine q) for a row checked and
// shown for itself: not a copy, or a copy whose held row has left.
const heldOwnRow = `(q.copy_of='' OR NOT EXISTS(SELECT 1 FROM quarantine h WHERE h.id=q.copy_of))`

// heldCopyKey identifies a verified history or device-history carrier by
// its sender, verified key and exact inner content without the transport
// identity (envelope ID, carrier LID, TS). The logical record is inside the
// body, so equal keys are one record sent again. Other envelopes return "":
// two equal ordinary turns are two messages.
func heldCopyKey(env envelope.Envelope, in envelope.Inner, sender identity.Public) string {
	if !in.Replica || in.Sub != envelope.SubHistory && in.Sub != envelope.SubDeviceHistory {
		return ""
	}
	in.ID, in.LID, in.TS = "", "", 0
	b, err := json.Marshal(struct {
		From, Key string
		Inner     envelope.Inner
	}{env.From, sender.Fingerprint(), in})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// holdCopy quarantines env with reason, or changes why it is held, and
// settles a keyed copy (heldCopyKey) into the first held row of the same
// record, reason and code. It reports whether the hold state changed (a new
// row, another reason or code) and the row this one is a copy of. Only a
// change is signalled to the UI: an unchanged recheck changes nothing shown.
func (s *store) holdCopy(env envelope.Envelope, key, reason, why string) (of string, changed bool, err error) {
	raw, _ := json.Marshal(env)
	code := heldFailureCode(reason, why)
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var oldReason, oldCode string
	switch err = tx.QueryRow(`SELECT reason,detail_code FROM quarantine WHERE id=?`, env.ID).Scan(&oldReason, &oldCode); {
	case errors.Is(err, sql.ErrNoRows):
		changed = true
	case err != nil:
		return "", false, err
	default:
		changed = oldReason != reason || oldCode != code
	}
	if _, err = tx.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at, detail_code, copy_key) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, detail_code = excluded.detail_code, copy_key = CASE WHEN excluded.copy_key<>'' THEN excluded.copy_key ELSE quarantine.copy_key END,
		notice_archived = CASE WHEN excluded.reason IN ('invalid','proof_pending') AND quarantine.reason=excluded.reason AND quarantine.detail_code=excluded.detail_code THEN quarantine.notice_archived ELSE 0 END`,
		env.ID, env.From, reason, string(raw), time.Now().Unix(), code, key); err != nil {
		return "", false, err
	}
	moved := false
	if key != "" {
		if of, moved, err = settleHeldCopy(tx, env.ID, env.From, key, reason, code); err != nil {
			return "", false, err
		}
	}
	if err = tx.Commit(); err == nil && (changed || moved) {
		s.changed()
	}
	return of, changed, err
}

// settleHeld keys a row held before keys existed and reports the row it is
// a copy of; "" means it is checked for itself. A copy whose record left the
// quarantine (most often admitted) is checked once on its own: a duplicate
// of a stored record is then acknowledged, one held again settles anew.
func (s *store) settleHeld(id, sender, key string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var reason, code, of string
	if err = tx.QueryRow(`SELECT reason,detail_code,copy_of FROM quarantine WHERE id=?`, id).Scan(&reason, &code, &of); errors.Is(err, sql.ErrNoRows) || of != "" {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if _, err = tx.Exec(`UPDATE quarantine SET copy_key=? WHERE id=?`, key, id); err != nil {
		return "", err
	}
	of, moved, err := settleHeldCopy(tx, id, sender, key, reason, code)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err == nil && moved {
		s.changed()
	}
	return of, err
}

// heldCopiesFreed reports whether id left the quarantine while copies of
// its record are still held there: they are due for their own check.
func (s *store) heldCopiesFreed(id string) bool {
	var freed bool
	s.db.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM quarantine WHERE id=?) AND EXISTS(SELECT 1 FROM quarantine WHERE copy_of=? AND copy_of<>'')`, id, id).Scan(&freed)
	return freed
}

// settleHeldCopy points every row of one record (sender, key, reason and
// code) that is checked for itself, id included, and their copies at the
// first stored of them, so a record has one such row however its copies
// arrived, and a later copy never replaces a notice the person archived.
func settleHeldCopy(tx *sql.Tx, id, sender, key, reason, code string) (rep string, moved bool, err error) {
	rows, err := tx.Query(`SELECT id FROM quarantine q WHERE sender=? AND copy_key=? AND copy_key<>'' AND reason=? AND detail_code=? AND (id=? OR `+heldOwnRow+`) ORDER BY rowid`, sender, key, reason, code, id)
	if err != nil {
		return "", false, err
	}
	var own []string
	for rows.Next() {
		var r string
		if err = rows.Scan(&r); err != nil {
			rows.Close()
			return "", false, err
		}
		own = append(own, r)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", false, err
	}
	if len(own) == 0 {
		return "", false, sql.ErrNoRows
	}
	rep, moved = own[0], len(own) > 1
	for _, r := range own[1:] {
		if _, err = tx.Exec(`UPDATE quarantine SET copy_of=? WHERE id=? OR copy_of=? AND copy_of<>''`, rep, r, r); err != nil {
			return "", false, err
		}
	}
	res, err := tx.Exec(`UPDATE quarantine SET copy_of='' WHERE id=? AND copy_of<>''`, rep)
	if err != nil {
		return "", false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		moved = true
	}
	if rep == id {
		return "", moved, nil
	}
	return rep, moved, nil
}

// logHold logs a hold only when its state changed: an unchanged recheck of
// the same failure is silent (it was one journal line per row per pass).
func (a *Agent) logHold(what string, env envelope.Envelope, reason, why, of string, changed bool) {
	switch {
	case !changed:
	case of != "":
		a.Logf("%s %s from %s held (%s): the same record as held %s", what, env.ID, env.From, reason, of)
	default:
		a.Logf("%s %s from %s held (%s): %s", what, env.ID, env.From, reason, why)
	}
}
