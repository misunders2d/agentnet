package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Host-local queue binding, separate from the signed message and from a
// native handover receipt. A queued parent's next attempt is reserved here
// before either request can be claimed.
const queuedFollowupSchema = `
ALTER TABLE inbox ADD COLUMN queued_followup_parent TEXT;
ALTER TABLE inbox ADD COLUMN queued_followup_attempt INTEGER;
CREATE INDEX inbox_queued_followup ON inbox(queued_followup_parent,queued_followup_attempt,arrival) WHERE queued_followup_parent IS NOT NULL;
CREATE INDEX inbox_followup_scope ON inbox(conv,pid,kind,topic,target,arrival)
 WHERE replica=0 AND local=0 AND state IN ('part_waiting','accepted','running');
`

func bindQueuedFollowup(tx *sql.Tx, in envelope.Inner, key, state string) error {
	if state != stateAgentWaiting || in.Replica || in.Target == nil || in.Followup != nil || in.ReplyTo != "" || in.Quote != "" ||
		in.ReceiverRoute != nil || in.Sub != "" || in.Body == "" || envelope.AgentOrigin(in.Origin) ||
		in.Human != nil && in.Human.AgentAuthor() || in.Kind != envelope.KindQuestion && in.Kind != envelope.KindTask {
		return nil
	}
	// Use the exact live scope and earlier durable arrival. A signed timestamp
	// can rule out newer work, but only the host's queue chooses a parent.
	rows, err := tx.Query(`SELECT p.id,p.sender,p.verified_by,p.state,p.attempts
		FROM inbox p WHERE p.conv=? AND p.pid=? AND p.kind=? AND p.topic IS nullif(?,'') AND p.target=?
		AND p.replica=0 AND p.local=0 AND p.state IN ('part_waiting','accepted','running')
		AND p.arrival<(SELECT arrival FROM inbox WHERE id=?) AND p.ts<=?
		AND p.steering_parent IS NULL AND (p.queued_followup_parent IS NULL OR p.state='running')
		AND p.receiver_route IS NULL AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=p.id)
		AND p.sub IS NULL AND p.body<>'' AND coalesce(p.origin,'') NOT LIKE 'agent:%'
		AND (p.sender=? AND p.verified_by=? OR EXISTS(SELECT 1 FROM person_devices d JOIN person_devices prior ON prior.person=d.person
			WHERE d.address=? AND d.fingerprint=? AND prior.address=p.sender AND prior.fingerprint=p.verified_by))
		LIMIT 2`, in.Conv, in.PID, in.Kind, in.Topic, targetJSON(in.Target), in.ID, in.TS, in.From, key, in.From, key)
	if err != nil {
		return err
	}
	var parent agentReq
	var attempt int64
	count := 0
	for rows.Next() {
		if err = rows.Scan(&parent.ID, &parent.Sender, &parent.Key, &parent.State, &attempt); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || count != 1 {
		return err // ambiguous work keeps the ordinary request path
	}
	parent.Conv, parent.PID, parent.Kind, parent.Target = in.Conv, in.PID, in.Kind, in.Target
	if h, err := storedHuman(tx, "in", parent.ID); err != nil || h != nil && h.AgentAuthor() {
		return err
	}
	same, err := followupHumanIn(tx, in.To, in.Target.Fingerprint, in.From, key, HistoryItem{From: parent.Sender, FromKey: parent.Key})
	if err != nil || !same {
		return err
	}
	v, _, err := agentVerdict(tx, parent, in.To, in.Target.Fingerprint, parent.State == stateRunning, map[string]*partView{})
	if err != nil || v != verdictRun {
		return err
	}
	if parent.State != stateRunning {
		attempt++
	}
	_, err = tx.Exec(`UPDATE inbox SET queued_followup_parent=?,queued_followup_attempt=? WHERE id=? AND body<>''`, parent.ID, attempt, in.ID)
	return err
}

const queuedFollowupEarlier = `NOT EXISTS(SELECT 1 FROM inbox earlier WHERE earlier.queued_followup_parent=inbox.queued_followup_parent
	AND earlier.queued_followup_attempt=inbox.queued_followup_attempt AND earlier.arrival<inbox.arrival
	AND earlier.state IN ('part_waiting','accepted','awaiting','running','cancel_requested','helpers_waiting'))`

func queuedFollowupReadyIn(q dbq, id string) (bool, error) {
	var ready bool
	err := q.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM inbox parent WHERE parent.id=inbox.queued_followup_parent
		AND parent.state IN ('part_waiting','accepted','awaiting','running','cancel_requested','helpers_waiting'))
		AND `+queuedFollowupEarlier+` FROM inbox WHERE id=?`, id).Scan(&ready)
	return ready, err
}

// The explicit carrier stays part of signed history. Only execution reads
// the host's derived queue binding, referring to the exact selected request.
func executionFollowup(q dbq, id string) (*envelope.FollowupRef, error) {
	var raw, lid, key sql.NullString
	var attempt, actual int64
	err := q.QueryRow(`SELECT c.request_followup,p.lid,p.verified_by,coalesce(c.queued_followup_attempt,0),coalesce(p.attempts,0)
		FROM inbox c LEFT JOIN inbox p ON p.id=c.queued_followup_parent AND p.replica=0 WHERE c.id=?`, id).Scan(&raw, &lid, &key, &attempt, &actual)
	if err != nil {
		return nil, err
	}
	if raw.Valid {
		var ref envelope.FollowupRef
		err = json.Unmarshal([]byte(raw.String), &ref)
		return &ref, err
	}
	if attempt <= 0 {
		return nil, nil
	}
	if actual != attempt || !lid.Valid || !key.Valid {
		return nil, errors.New("the queued follow-up's exact original attempt is no longer available")
	}
	return &envelope.FollowupRef{Ref: envelope.Ref{ID: lid.String, Fingerprint: key.String}, Attempt: attempt}, nil
}

// Pending corrections reach native input in order. Keep the current request's
// corrections and any later queued sibling out of its ambient chat context.
func queuedFollowupRefs(q dbq, parent job, refs []protocol.GrantRef) ([]protocol.GrantRef, error) {
	rows, err := q.Query(`SELECT lid,verified_by FROM inbox WHERE queued_followup_parent=? AND queued_followup_attempt=?
		UNION ALL SELECT later.lid,later.verified_by FROM inbox current JOIN inbox later
		ON later.queued_followup_parent=current.queued_followup_parent AND later.queued_followup_attempt=current.queued_followup_attempt
		AND later.arrival>current.arrival WHERE current.id=?`, parent.ID, parent.nativeAttempt, parent.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref protocol.GrantRef
		if err := rows.Scan(&ref.LID, &ref.Fingerprint); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
