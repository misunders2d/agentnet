package client

import (
	"context"
	"database/sql"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const sendGroupSchema = `ALTER TABLE inbox ADD COLUMN send_group TEXT NOT NULL DEFAULT '';
ALTER TABLE inbox ADD COLUMN send_group_conflict INTEGER NOT NULL DEFAULT 0;
ALTER TABLE outbox ADD COLUMN send_group TEXT NOT NULL DEFAULT '';
ALTER TABLE outbox ADD COLUMN wire_send_group INTEGER NOT NULL DEFAULT 0;`

type sendGroupKey struct{}

func WithHumanSendGroup(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sendGroupKey{}, id)
}
func (a *Agent) sendGroupSupported(ctx context.Context, key identity.Public) bool {
	if key.Address == a.Address && key.Fingerprint() == a.Self().Fingerprint() {
		return true
	}
	return a.requireParticipationCaps(ctx, key, protocol.CapSendGroup) == nil
}

// Presentation metadata is not payload identity. Absence never erases a known
// group; differing nonempty groups permanently disable grouping for this turn.
func mergeSendGroup(tx *sql.Tx, in envelope.Inner, key string) error {
	if in.SendGroup == "" {
		return nil
	}
	if err := envelope.CheckSendGroup(in); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE inbox SET send_group_conflict=CASE WHEN send_group<>'' AND send_group<>? THEN 1 ELSE send_group_conflict END,
 send_group=CASE WHEN send_group_conflict<>0 OR send_group<>'' AND send_group<>? THEN '' ELSE ? END
 WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp)=?`, in.SendGroup, in.SendGroup, in.SendGroup, in.Conv, in.LID, key)
	return err
}
func storedSendGroup(q dbq, dir, id string) (string, error) {
	table := "inbox"
	if dir == "out" {
		table = "outbox"
	}
	var group string
	err := q.QueryRow("SELECT send_group FROM "+table+" WHERE id=?", id).Scan(&group)
	return group, err
}
