package client

import (
	"database/sql"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Old queued rows cannot prove whether a previous handover was attempted.
// New rows start at zero; one is recorded before the final network request.
const queuedRetractionSchema = `
ALTER TABLE outbox ADD COLUMN handover_started INTEGER DEFAULT 0;
ALTER TABLE outbox ADD COLUMN send_stopped INTEGER NOT NULL DEFAULT 0;
UPDATE outbox SET handover_started=NULL;
`

func stopRequestRows(tx *sql.Tx, ref ControlRef, selfFP string) error {
	if ref.Fingerprint != selfFP {
		return nil
	}
	where, args := outScope(ref, selfFP)
	_, err := tx.Exec(`UPDATE outbox AS o SET send_stopped=1,state='not_delivered',
	 error=CASE WHEN handover_started=0 THEN 'Deleted before handover; not sent.'
	 ELSE 'Deleted; delivery may have been attempted and is unconfirmed. Cancellation cannot be confirmed.' END
	 WHERE `+where+` AND ref_id IS NULL AND coalesce(sub,'')=''
	 AND coalesce(kind,json_extract(envelope,'$.kind')) IN ('question','task')
	 AND state IN ('queued','waiting','receiver_waiting','failed')`, args...)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE outbox SET send_stopped=1,state='not_delivered',
	 error=CASE WHEN handover_started=0 THEN 'Deleted before handover; not sent.'
	 ELSE 'Deleted; delivery may have been attempted and is unconfirmed. Cancellation cannot be confirmed.' END
	 WHERE id IN (SELECT json_extract(receiver,'$.remote.route.delegation_id') FROM reply_receivers
	 WHERE conv=? AND request_ref=? AND json_extract(receiver,'$.remote.request.from_key')=?
	 AND json_extract(receiver,'$.remote.request.kind') IN ('question','task'))
	 AND state IN ('queued','waiting','receiver_waiting','failed')`, ref.Conv, ref.ID, selfFP)
	return err
}

func (a *Agent) stopRetractedRequests(ref ControlRef) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = stopRequestRows(tx, ref, a.Self().Fingerprint()); err != nil {
		return err
	}
	return a.store.done(tx.Commit())
}

// beginHandover is the exact local cancellation fence. Retraction persisted
// before a crash is checked again here, using its already verified authority.
// Nothing here recalls a handover that may already have reached another host.
func (a *Agent) beginHandover(env envelope.Envelope) (bool, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, conv, lid, sub string
	var stopped bool
	if err = tx.QueryRow(`SELECT state,coalesce(conv,''),coalesce(lid,''),coalesce(sub,''),send_stopped FROM outbox WHERE id=?`, env.ID).Scan(&state, &conv, &lid, &sub, &stopped); err != nil {
		return false, err
	}
	if stopped || state == stateNotDelivered {
		return false, nil
	}
	if env.Kind != envelope.KindQuestion && env.Kind != envelope.KindTask || sub != "" {
		return true, nil
	}
	ref := ControlRef{Conv: conv, ID: env.ID, Fingerprint: a.Self().Fingerprint()}
	if conv != "" {
		ref.ID = lid
	}
	var remoteConv, remoteID string
	e := tx.QueryRow(`SELECT conv,request_ref FROM reply_receivers WHERE json_extract(receiver,'$.remote.route.delegation_id')=? AND json_extract(receiver,'$.remote.request.from_key')=?`, env.ID, ref.Fingerprint).Scan(&remoteConv, &remoteID)
	if e == nil {
		ref.Conv, ref.ID = remoteConv, remoteID
	} else if e != sql.ErrNoRows {
		return false, e
	}
	retracted, err := retractedRefChecked(tx, ref.Conv, ref.ID, ref.Fingerprint)
	if err != nil {
		return false, err
	}
	if retracted {
		if err = stopRequestRows(tx, ref, ref.Fingerprint); err != nil {
			return false, err
		}
		return false, a.store.done(tx.Commit())
	}
	_, err = tx.Exec(`UPDATE outbox SET handover_started=1 WHERE id=? AND send_stopped=0`, env.ID)
	if err != nil {
		return false, err
	}
	return true, a.store.done(tx.Commit())
}
