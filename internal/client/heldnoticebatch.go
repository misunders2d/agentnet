package client

import (
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

const MaxHeldNoticeBatch = 256

// HeldNoticeRef is the exact diagnostic snapshot the person chose to hide.
// A newly arrived notice or changed reason cannot be hidden by an older view.
type HeldNoticeRef struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	DetailCode string `json:"detail_code"`
}

func (a *Agent) ArchiveHeldNotices(refs []HeldNoticeRef) (int, error) {
	if len(refs) == 0 || len(refs) > MaxHeldNoticeBatch {
		return 0, errors.New("choose between 1 and 256 held notices")
	}
	for _, ref := range refs {
		if !protocol.ValidID(ref.ID) || ref.Reason != reasonInvalid && ref.Reason != reasonProof || len(ref.DetailCode) > 80 {
			return 0, errors.New("invalid held-notice snapshot")
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	total := 0
	for _, ref := range refs {
		res, err := tx.Exec(`UPDATE quarantine SET notice_archived=1 WHERE id=? AND reason=? AND detail_code=? AND notice_archived=0`, ref.ID, ref.Reason, ref.DetailCode)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		total += int(n)
	}
	return total, a.store.done(tx.Commit())
}
