package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A steer is terminal for the correction's scheduler, but says nothing about
// the original work's success or completion. Interrupted RPC claims keep the
// existing interrupted/needs-human no-replay rule.
const stateSteered = "steered"

// claimNativeFollowup uses the ordinary participation verdict, with the exact
// already-owned run as its executor. No free-executor claim or new process is
// involved. Durable running precedes any native input.
type nativeFollowupClaim struct {
	job
	queuedState string
}

func (a *Agent) claimNativeFollowup(parent job) (nativeFollowupClaim, bool, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return nativeFollowupClaim{}, false, err
	}
	defer tx.Rollback()
	var state, rootID, rootKey string
	if err = tx.QueryRow(`SELECT state,coalesce(lid,id),coalesce(verified_by,'') FROM inbox WHERE id=? AND replica=0`, parent.ID).Scan(&state, &rootID, &rootKey); err != nil {
		return nativeFollowupClaim{}, false, err
	}
	if state != stateRunning || !a.nativeFollowupExecutor(tx, parent) {
		return nativeFollowupClaim{}, false, nil
	}
	root, err := storedRequestFollowup(tx, "in", parent.ID)
	if err != nil {
		return nativeFollowupClaim{}, false, err
	}
	if root != nil {
		rootID, rootKey = root.ID, root.Fingerprint
	}
	ready := strings.Replace(requestFollowupReady, "earlier.id<>inbox.id", "earlier.id<>inbox.id AND earlier.id<>?", 1)
	rows, err := tx.Query(`SELECT id,sender,verified_by,kind,body,coalesce(reply_to,''),conv,pid,coalesce(target,''),state,local,coalesce(topic,'') FROM inbox WHERE replica=0 AND conv=? AND pid=? AND json_extract(request_followup,'$.id')=? AND json_extract(request_followup,'$.fingerprint')=? AND state IN (?,?) AND `+ready+` ORDER BY arrival LIMIT 50`, parent.Conv, parent.PID, rootID, rootKey, stateAgentWaiting, stateAccepted, parent.ID)
	if err != nil {
		return nativeFollowupClaim{}, false, err
	}
	type candidate struct {
		j                    job
		target, state, topic string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.j.ID, &c.j.From, &c.j.Key, &c.j.Kind, &c.j.Body, &c.j.ReplyTo, &c.j.Conv, &c.j.PID, &c.target, &c.state, &c.j.Local, &c.topic); err != nil {
			rows.Close()
			return nativeFollowupClaim{}, false, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nativeFollowupClaim{}, false, err
	}
	for _, c := range candidates {
		if c.target != "" {
			c.j.Target = &envelope.Target{}
			if json.Unmarshal([]byte(c.target), c.j.Target) != nil {
				continue
			}
		}
		ref := envelope.Ref{ID: rootID, Fingerprint: rootKey}
		original, e := a.followupOriginal(tx, parent.Conv, ref)
		if e != nil {
			continue
		}
		human, e := a.followupHuman(tx, c.j.From, c.j.Key, original.h)
		if e != nil {
			return nativeFollowupClaim{}, false, e
		}
		var parentTopic string
		if e = tx.QueryRow(`SELECT coalesce(topic,'') FROM inbox WHERE id=?`, parent.ID).Scan(&parentTopic); e != nil {
			return nativeFollowupClaim{}, false, e
		}
		if !human || c.j.Kind != parent.Kind || c.topic != parentTopic || !sameFollowupTarget(c.j.Target, parent.Target) || original.to != a.Address || original.key != a.Self().Fingerprint() {
			continue
		}
		views := map[string]*partView{}
		v, _, e := agentVerdict(tx, parent.agentReq(stateRunning), a.Address, a.Self().Fingerprint(), true, views)
		if e != nil {
			return nativeFollowupClaim{}, false, e
		}
		if v != verdictRun {
			return nativeFollowupClaim{}, false, nil
		}
		v, _, e = agentVerdict(tx, c.j.agentReq(c.state), a.Address, a.Self().Fingerprint(), false, views)
		if e != nil {
			return nativeFollowupClaim{}, false, e
		}
		if v != verdictRun {
			continue
		}
		c.j.Executor, c.j.AgentID = parent.Executor, parent.AgentID
		stamp, _ := json.Marshal(parent.Executor)
		res, e := tx.Exec(`UPDATE inbox SET state=?,detail='Native correction handover pending; do not replay automatically',responder=(SELECT responder FROM inbox WHERE id=?),executor=?,agent_id=?,attempts=attempts+1,last_attempt_at=unixepoch() WHERE id=? AND state=?`, stateRunning, parent.ID, string(stamp), parent.AgentID, c.j.ID, c.state)
		if e != nil {
			return nativeFollowupClaim{}, false, e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			continue
		}
		table := "attachments"
		if c.j.Local {
			table = "sent_attachments"
		}
		if e = tx.QueryRow(`SELECT count(*) FROM `+table+` WHERE message_id=?`, c.j.ID).Scan(&c.j.Attachments); e != nil {
			return nativeFollowupClaim{}, false, e
		}
		if e = tx.Commit(); e != nil {
			return nativeFollowupClaim{}, false, e
		}
		a.store.changed()
		return nativeFollowupClaim{job: c.j, queuedState: c.state}, true, nil
	}
	return nativeFollowupClaim{}, false, nil
}

// Still-current checks run after attachment staging, immediately before the
// native handover. Store failure or cancellation sends no input.
func (a *Agent) nativeFollowupCurrent(parent, correction job) bool {
	tx, err := a.store.db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var current bool
	if tx.QueryRow(`SELECT (SELECT state FROM inbox WHERE id=?)=? AND (SELECT state FROM inbox WHERE id=?)=?`, parent.ID, stateRunning, correction.ID, stateRunning).Scan(&current) != nil || !current {
		return false
	}
	if !a.nativeFollowupExecutor(tx, parent) {
		return false
	}
	views := map[string]*partView{}
	for _, j := range []job{parent, correction} {
		v, _, e := agentVerdict(tx, j.agentReq(stateRunning), a.Address, a.Self().Fingerprint(), true, views)
		if e != nil || v != verdictRun {
			return false
		}
	}
	ref, e := storedRequestFollowup(tx, "in", correction.ID)
	if e != nil || ref == nil {
		return false
	}
	original, e := a.followupOriginal(tx, parent.Conv, *ref)
	if e != nil {
		return false
	}
	human, e := a.followupHuman(tx, correction.From, correction.Key, original.h)
	return e == nil && human
}

// A named identity can be disabled while its old process still exists. It
// cannot receive new corrections merely because that process was once admitted.
func (a *Agent) nativeFollowupExecutor(q dbq, parent job) bool {
	if parent.Target == nil {
		return false
	}
	id := parent.Target.AgentID
	if id == "" {
		return true
	} // the existing default run retains its immutable setup
	stamp, err := a.ResolveExecutorIn(q, id, nil)
	return err == nil && stamp != nil && stamp.Record != nil && stamp.AgentID == id && stamp.Record.Host == parent.Target.Address && stamp.Record.HostKey == parent.Target.Fingerprint
}

func (a *Agent) watchNativeFollowups(ctx context.Context, parent job, bridge *codexRoomBridge, processFinished <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-bridge.ready:
	}
	var files []*runDir
	defer func() {
		// An uncertain response can still mean the native process received the
		// paths. Keep its readonly copies until that process has actually ended,
		// even when this watcher stops accepting further corrections earlier.
		if len(files) > 0 && processFinished != nil {
			<-processFinished
		}
		for _, r := range files {
			_ = r.remove()
		}
	}()
	for ctx.Err() == nil {
		_, changed := a.Changed()
		for ctx.Err() == nil {
			correction, found, err := a.claimNativeFollowup(parent)
			if err != nil {
				a.Logf("native follow-up claim: %v", err)
				return
			}
			if !found {
				break
			}
			text := fmt.Sprintf("Explicit correction %s to this exact current request. Preserve earlier instructions except where this human correction changes them; do not repeat already-completed effects. Native permissions remain unchanged.\n%s", correction.ID, correction.Body)
			if correction.Attachments > 0 {
				correction.run = a.newRun(correction.job, false)
				files = append(files, correction.run)
				text += a.requestFiles(ctx, correction.job)
				if e := correction.run.seal(); e != nil {
					_ = a.endJob(correction.ID, stateNeedHuman, "Correction files could not be prepared; nothing was sent into the native run")
					continue
				}
			}
			if !a.nativeFollowupCurrent(parent, correction.job) {
				_ = a.endJob(correction.ID, stateNeedHuman, "The exact run or correction changed before native handover; review it before continuing")
				continue
			}
			outcome := bridge.steer(ctx, correction.ID, text)
			switch outcome {
			case steerAccepted:
				_ = a.endJob(correction.ID, stateSteered, "Accepted into the exact current native run; its work is not yet proven complete")
			case steerQueued, steerUnsupported:
				// Explicitly no handover: restore only our running claim. A concurrent
				// cancellation is never undone, and ordinary admission still governs it.
				_, err = a.store.db.Exec(`UPDATE inbox SET state=?,detail='Queued; not accepted into the native run' WHERE id=? AND state=?`, correction.queuedState, correction.ID, stateRunning)
				if err == nil {
					a.store.changed()
					a.noteStatus(correction.ID)
				}
				return
			default:
				_ = a.endJob(correction.ID, stateNeedHuman, "Native correction acceptance is unknown. Check the original work before explicitly continuing; no automatic replay")
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}
