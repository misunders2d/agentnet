package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
)

const frozenMemberRecoveryScan = "group-frozen-member-invalid-recovery-v1"

// Earlier clients refused group lifecycle and context carriers for good when
// a person record the group names was frozen here (MEL-747): a recovered
// person's own old identity, frozen as an email claim, also one released
// since. Reconsider those inert carriers once, locally and in pages: they
// wait for proof again and normal ingress verifies them in full; one refused
// for another reason is refused again. Nothing runs work or grants
// membership here, and a notice its reader already archived stays as it
// was. Only rows refused at admission are opened, each outside any write
// transaction; each move is its own conditional update, so an interrupted
// scan resumes where it stopped.
func (a *Agent) recoverFrozenMemberCarriers() error {
	db := a.store.db
	var done string
	switch err := db.QueryRow(`SELECT v FROM config WHERE k=?`, frozenMemberRecoveryScan).Scan(&done); {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	moved := 0
	type held struct{ id, sender, raw string }
	for after := ""; ; {
		rows, err := db.Query(`SELECT id,sender,envelope FROM quarantine WHERE reason=? AND detail_code='admission_failed' AND notice_archived=0 AND id>? ORDER BY id LIMIT ?`, reasonInvalid, after, proofPage)
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
			if json.Unmarshal([]byte(h.raw), &env) != nil || env.V != envelope.Version2 || env.ID != h.id || env.From != h.sender || env.To != a.Address {
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
			if err != nil {
				continue
			}
			switch in.Sub {
			case envelope.SubGroupInvite, envelope.SubGroupConsent, envelope.SubGroupProof, envelope.SubGroupContext:
			default:
				continue
			}
			res, err := db.Exec(`UPDATE quarantine SET reason=? WHERE id=? AND reason=? AND detail_code='admission_failed' AND notice_archived=0`, reasonProof, env.ID, reasonInvalid)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				moved++
			}
		}
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?, '1')`, frozenMemberRecoveryScan); err != nil {
		return err
	}
	if moved > 0 {
		a.store.changed()
		a.convWork.due(convRetry)
	}
	return nil
}
