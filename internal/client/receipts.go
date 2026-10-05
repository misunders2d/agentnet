package client

import (
	"strconv"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// applyReceipt commits the row and cursor together. Replays never downgrade
// a final state and unknown copies still advance the resumable stream.
func (s *store) applyReceipt(r protocol.ReceiptEvent) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE outbox SET state=?, error=NULL WHERE id=? AND (state IN ('custody','queued') OR (state='quarantined' AND ?='delivered'))`, r.State, r.ID, r.State)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO config(k,v) VALUES('receipt_cursor',?) ON CONFLICT(k) DO UPDATE SET v=CAST(max(CAST(config.v AS INTEGER),CAST(excluded.v AS INTEGER)) AS TEXT)`, strconv.FormatInt(r.Seq, 10))
	if err != nil {
		return err
	}
	return s.done(tx.Commit())
}
