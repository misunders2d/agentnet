package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Old ordinary rows did not record handover uncertainty. Never infer their
// eligibility from the historical zero in handover_started.
const sendRetryProofSchema = `ALTER TABLE outbox ADD COLUMN handover_proven INTEGER NOT NULL DEFAULT 1;
UPDATE outbox SET handover_proven=0;`

type StorageSendRetry struct {
	Conv string `json:"conv"`
	ID   string `json:"id"`
}
type StorageSendRetried struct {
	ID      string `json:"id"`
	Retried int    `json:"retried"`
}

func retryStorageDetail(detail string) bool {
	return strings.HasPrefix(detail, SendErrorPrefix+protocol.CodeRelayQuota+":") || strings.HasPrefix(detail, SendErrorPrefix+protocol.CodeFileTooLarge+":") || strings.HasPrefix(detail, SendErrorPrefix+protocol.CodeRelayDiskFull+":")
}

// Explicitly retry only intact, proven never-handed-over storage refusals.
// No envelope, file ID, FIFO position or admitted execution is recreated.
func (a *Agent) RetryStorageSend(ctx context.Context, change StorageSendRetry) (StorageSendRetried, error) {
	result := StorageSendRetried{ID: change.ID}
	if !protocol.ValidID(change.ID) {
		return result, errors.New("choose the exact stored message to retry")
	}
	unlock, err := lockfile.Acquire(a.spoolLockPath())
	if err != nil {
		return result, errors.New("a file is being prepared; retry when it is queued")
	}
	defer unlock()
	rows, err := a.store.db.QueryContext(ctx, `SELECT id,envelope,error FROM outbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?)
 AND state='failed' AND handover_proven=1 AND handover_started=0 AND send_stopped=0`, change.Conv, change.ID, change.ID)
	if err != nil {
		return result, err
	}
	type candidate struct{ id, raw, detail string }
	var copies []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.raw, &c.detail); err != nil {
			rows.Close()
			return result, err
		}
		copies = append(copies, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, c := range copies {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		var env envelope.Envelope
		if !retryStorageDetail(c.detail) || json.Unmarshal([]byte(c.raw), &env) != nil || env.ID != c.id || len(env.Blobs) == 0 {
			continue
		}
		intact := true
		for _, b := range env.Blobs {
			if !protocol.ValidID(b.ID) || blobfile.Check(a.spoolPath(b.ID), b.Size, b.SHA256) != nil {
				intact = false
				break
			}
		}
		if !intact {
			continue
		}
		r, e := a.store.db.ExecContext(ctx, `UPDATE outbox SET state='queued',error=NULL WHERE id=? AND envelope=? AND error=? AND state='failed' AND handover_proven=1 AND handover_started=0 AND send_stopped=0`, c.id, c.raw, c.detail)
		if e != nil {
			return result, e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return result, e
		}
		result.Retried += int(n)
	}
	if result.Retried > 0 {
		a.NoteChange()
		a.postOutboxBackground()
	}
	return result, nil
}
