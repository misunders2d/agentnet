package client

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Local execution provenance, not received authority. Claim records the exact
// owned attempt before input; only its native acknowledgement marks steered.
const steeringLifecycleSchema = `
ALTER TABLE inbox ADD COLUMN steering_parent TEXT;
ALTER TABLE inbox ADD COLUMN steering_attempt INTEGER;
CREATE INDEX inbox_steering_parent ON inbox(steering_parent,steering_attempt) WHERE steering_parent IS NOT NULL;
CREATE INDEX inbox_steering_result ON inbox(result_id) WHERE result_id IS NOT NULL AND replica=0;
`

func settleSteeringIn(tx *sql.Tx, parent string) error {
	var state, result, kind, conv, lid, responder, key, pid, agent string
	var attempt, started int64
	err := tx.QueryRow(`SELECT state,coalesce(result_id,''),kind,coalesce(conv,''),coalesce(lid,id),attempts,coalesce(last_attempt_at,0),coalesce(responder,''),coalesce(verified_by,''),coalesce(pid,''),coalesce(agent_id,'') FROM inbox WHERE id=? AND replica=0`, parent).
		Scan(&state, &result, &kind, &conv, &lid, &attempt, &started, &responder, &key, &pid, &agent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	parentState := state
	switch state {
	case stateAnswered:
		// A reply to the root alone proves nothing about a consumed attempt.
		// The worker's exact transactional result link must name its stored
		// answer/result, in this conversation and after this attempt began.
		var matches bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE id=? AND coalesce(conv,'')=? AND created_at>=? AND (
 ((reply_to=? OR reply_to=?) AND CASE WHEN conv IS NULL THEN json_extract(envelope,'$.kind') ELSE kind END=? AND status IN (?,?,?))
 OR (sub=? AND kind=? AND (ref_id=? OR ref_id=?) AND ref_fp=? AND coalesce(pid,'')=? AND coalesce(agent_id,'')=?
 AND (?<>'' OR ?<>'' OR origin=?))))`,
			result, conv, started, parent, lid, replyKind(kind), envelope.StatusDone, envelope.StatusProposal, envelope.StatusDeclined,
			envelope.SubReaction, envelope.KindMessage, parent, lid, key, pid, agent, pid, agent, envelope.OriginAgentPrefix+responder).Scan(&matches); err != nil {
			return err
		}
		if !matches || responder == "manual" {
			state, result = stateInterrupt, "" // no matching native result proves completion
		}
	case stateManual:
		state, result = stateInterrupt, "" // a manual reply cannot prove the native outcome
	case stateCancelled, stateJobFailed, stateInterrupt, stateNotRun, stateNotDelivered, stateNeedHuman, stateResolved, stateDeclined:
		result = ""
	default:
		return nil
	}
	detail := fmt.Sprintf("Accepted correction to request %s attempt %d; that run is %s", parent, attempt, parentState)
	if state == stateInterrupt && (parentState == stateAnswered || parentState == stateManual) {
		detail += "; no matching native result proves this correction's completion"
	}
	if result != "" {
		detail += "; result " + result
	}
	_, err = tx.Exec(`UPDATE inbox SET state=?,detail=?,result_id=nullif(?,''),status_due=status_due+1,notified=0,review_sent=0 WHERE steering_parent=? AND steering_attempt=? AND state=? AND replica=0`,
		state, detail, result, parent, attempt, stateSteered)
	return err
}

// Called after the result bytes and its parent's ownership claim are both in
// the same transaction. A crash cannot leave a committed result unpropagated.
func settleSteeringResultIn(tx *sql.Tx, result string) error {
	var parent string
	err := tx.QueryRow(`SELECT id FROM inbox WHERE result_id=? AND state IN (?,?,?) AND replica=0 LIMIT 1`, result, stateAnswered, stateManual, stateDeclined).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return settleSteeringIn(tx, parent)
}

// Only call after this daemon has stopped its owned survivors and interrupted
// their records. Open/ordinary readers must never make an active steer manually
// replyable. Reconcile retained records, never enqueue or replay native input.
func (a *Agent) reconcileSteeringAfterStop() error {
	var active bool
	if err := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE state IN (?,?) AND replica=0)`, stateRunning, stateCancelReq).Scan(&active); err != nil {
		return err
	}
	if active {
		return nil // no ambiguous accepted input becomes replyable during a run
	}
	rows, err := a.store.db.Query(`SELECT id FROM inbox WHERE state=? AND replica=0 ORDER BY id`, stateSteered)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.reconcileSteeringAfterStopOne(id); err != nil {
			return err
		}
	}
	if len(ids) != 0 {
		a.store.changed()
		a.wakeStatus()
	}
	return nil
}

func (a *Agent) reconcileSteeringAfterStopOne(id string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent, conv, from, key string
	if err = tx.QueryRow(`SELECT coalesce(steering_parent,''),coalesce(conv,''),sender,coalesce(verified_by,'') FROM inbox WHERE id=? AND state=? AND replica=0`, id, stateSteered).Scan(&parent, &conv, &from, &key); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if parent == "" {
		ref, e := storedRequestFollowup(tx, "in", id)
		if e == nil && ref != nil && ref.Attempt > 0 {
			// Shipped steered means native acceptance, but only an explicit
			// attempt plus the exact retained scope/executor can recover which
			// run consumed it. Never infer that from today's default or a name.
			var matches int
			var candidate string
			if err = tx.QueryRow(`SELECT count(*),coalesce(min(p.id),'') FROM inbox p JOIN inbox c ON c.id=?
 WHERE p.replica=0 AND c.replica=0 AND c.attempts=1 AND p.attempts=? AND p.last_attempt_at>0 AND c.last_attempt_at>=p.last_attempt_at
 AND (p.id=? OR p.lid=?) AND p.verified_by=? AND coalesce(p.conv,'')=coalesce(c.conv,'') AND coalesce(p.pid,'')=coalesce(c.pid,'')
 AND p.kind=c.kind AND coalesce(p.topic,'')=coalesce(c.topic,'') AND coalesce(p.target,'')=coalesce(c.target,'')
 AND CASE WHEN p.executor IS NULL OR p.executor='null' THEN 'null' ELSE p.executor END=CASE WHEN c.executor IS NULL OR c.executor='null' THEN 'null' ELSE c.executor END AND coalesce(p.responder,'')=coalesce(c.responder,'') AND coalesce(p.responder,'')<>'manual'
 AND p.state NOT IN ('pending','accepted','awaiting','held','running','cancel_requested',?)`, id, ref.Attempt, ref.ID, ref.ID, ref.Fingerprint, stateAgentWaiting).Scan(&matches, &candidate); err != nil {
				return err
			}
			if matches == 1 {
				original, e := a.followupOriginal(tx, conv, ref.Ref)
				if e == nil && original.to == a.Address && original.key == a.Self().Fingerprint() {
					human, e := a.followupHuman(tx, from, key, original.h)
					if e != nil {
						return e
					}
					if human {
						if _, err = tx.Exec(`UPDATE inbox SET steering_parent=?,steering_attempt=? WHERE id=? AND state=? AND steering_parent IS NULL`, candidate, ref.Attempt, id, stateSteered); err != nil {
							return err
						}
						parent = candidate
					}
				}
			}
		}
	}
	if parent != "" {
		if err = settleSteeringIn(tx, parent); err != nil {
			return err
		}
	}
	// Survivors were joined before this recovery pass. Missing/ambiguous
	// result provenance is an interruption, not evidence of success.
	if _, err = tx.Exec(`UPDATE inbox SET state=?,detail='The accepted native correction no longer has an owned active run; its exact outcome is unproven. No automatic replay; review the original work before replying or resolving.',status_due=status_due+1,notified=0,review_sent=0 WHERE id=? AND state=? AND replica=0`, stateInterrupt, id, stateSteered); err != nil {
		return err
	}
	return tx.Commit()
}
