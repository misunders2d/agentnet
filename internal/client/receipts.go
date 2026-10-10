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
	_, err = tx.Exec(`UPDATE outbox SET state=?, error=NULL WHERE id=? AND (state IN ('custody','queued') OR (state='not_delivered' AND send_stopped=1 AND coalesce(handover_started,1)=1) OR (state='quarantined' AND ?='delivered'))`, r.State, r.ID, r.State)
	if err != nil {
		return err
	}
	if r.State == protocol.StateDelivered {
		// The descriptor ACK proves durable chunk retention, not completion
		// of its child imports. Retire these rows from pending window scans.
		if _, err = tx.Exec(`UPDATE outbox SET state=? WHERE state=? AND id IN (SELECT child FROM history_archive_entries WHERE manifest=?) AND EXISTS(SELECT 1 FROM outbox WHERE id=? AND sub='history-archive' AND state='delivered')`, archiveAccepted, archiveStaged, r.ID, r.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO config(k,v) VALUES('receipt_cursor',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.FormatInt(r.Seq, 10))
	if err != nil {
		return err
	}
	return s.done(tx.Commit())
}
