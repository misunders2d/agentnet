package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
)

const (
	frozenMemberRecoveryScan = "group-frozen-member-invalid-recovery-v1"
	frozenRecordConflictScan = "frozen-record-conflict-recovery-v1"
)

// Earlier clients refused group lifecycle and context carriers for good when
// a person record the group names was frozen here (MEL-747): a recovered
// person's own old identity, frozen as an email claim, also one released
// since. Reconsider those inert carriers once: they wait for proof again
// and normal ingress verifies them in full; one refused for another reason
// is refused again.
func (a *Agent) recoverFrozenMemberCarriers() error {
	return a.reconsiderHeld(frozenMemberRecoveryScan, reasonInvalid, "admission_failed", func(in envelope.Inner) bool {
		switch in.Sub {
		case envelope.SubGroupInvite, envelope.SubGroupConsent, envelope.SubGroupProof, envelope.SubGroupContext:
			return in.V == envelope.Version2
		}
		return false
	})
}

// Earlier clients also held, as an identity conflict, messages that named
// a person frozen here only as an email claim without being sent by it:
// own devices' history of what that person wrote, a new conversation listing
// it (MEL-747). Releasing the person released only what its own devices
// sent, so the rest stayed held after it settled. Reconsider every message
// held as an identity conflict once: what still conflicts is held again
// by admission, the rest arrives.
func (a *Agent) recoverFrozenRecordConflicts() error {
	return a.reconsiderHeld(frozenRecordConflictScan, reasonConflict, "", func(in envelope.Inner) bool {
		return in.V == envelope.Version2 || in.V == envelope.Version3
	})
}

// reconsiderHeld moves held messages (reason, detail code when not empty)
// that take accepts back to waiting for proof, once (marker), locally and in
// pages; normal ingress then verifies each in full. Nothing runs work or
// grants membership here, and a notice its reader archived stays as it was.
// Only matching rows are opened, each outside any write transaction; each
// move is its own conditional update, so an interrupted scan resumes where
// it stopped.
func (a *Agent) reconsiderHeld(marker, reason, code string, take func(envelope.Inner) bool) error {
	db := a.store.db
	var done string
	switch err := db.QueryRow(`SELECT v FROM config WHERE k=?`, marker).Scan(&done); {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	moved := 0
	type held struct{ id, sender, raw string }
	for after := ""; ; {
		rows, err := db.Query(`SELECT id,sender,envelope FROM quarantine WHERE reason=? AND (?='' OR detail_code=?) AND notice_archived=0 AND id>? ORDER BY id LIMIT ?`, reason, code, code, after, proofPage)
		if err != nil {
			return err
		}
		var page []held
		for rows.Next() {
			var h held
			if err = rows.Scan(&h.id, &h.sender, &h.raw); err != nil {
				rows.Close()
				return err
			}
			page = append(page, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].id
		for _, h := range page {
			var env envelope.Envelope
			if json.Unmarshal([]byte(h.raw), &env) != nil || env.ID != h.id || env.From != h.sender || env.To != a.Address {
				continue
			}
			sender, found, err := pinnedKey(db, env.From)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			in, err := envelope.Open(env, a.id, a.Address, sender)
			if err != nil || !take(in) {
				continue
			}
			res, err := db.Exec(`UPDATE quarantine SET reason=? WHERE id=? AND reason=? AND (?='' OR detail_code=?) AND notice_archived=0`, reasonProof, env.ID, reason, code, code)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				moved++
			}
		}
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?, '1')`, marker); err != nil {
		return err
	}
	if moved > 0 {
		a.store.changed()
		a.convWork.due(convRetry)
	}
	return nil
}
