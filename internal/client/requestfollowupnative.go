package client

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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
	var parentAttempt int64
	if err = tx.QueryRow(`SELECT state,coalesce(lid,id),coalesce(verified_by,''),attempts FROM inbox WHERE id=? AND replica=0`, parent.ID).Scan(&state, &rootID, &rootKey, &parentAttempt); err != nil {
		return nativeFollowupClaim{}, false, err
	}
	if state != stateRunning || parent.Conv != "" && !a.nativeFollowupExecutor(tx, parent) {
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
	binding, eligible := `conv=? AND pid=?`, `state IN (?,?)`
	args := []any{parent.Conv, parent.PID, rootID, rootKey, stateAgentWaiting, stateAccepted, parent.ID}
	if parent.Conv == "" {
		if !a.nativeJobCurrentIn(tx, parent, parentAttempt) {
			return nativeFollowupClaim{}, false, nil
		}
		binding = `conv IS NULL AND pid IS NULL AND receiver_route IS NULL AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id)`
		eligible = `(state IN ('pending','accepted') OR attempts=0 AND ((state='held' AND kind='question') OR (state='awaiting' AND kind='task')) AND ` + ownTaskHolds + `)`
		args = []any{rootID, rootKey, parent.ID}
	}
	rows, err := tx.Query(`SELECT id,sender,verified_by,kind,body,coalesce(reply_to,''),coalesce(conv,''),coalesce(pid,''),coalesce(target,''),state,local,coalesce(topic,'') FROM inbox WHERE replica=0 AND `+binding+` AND json_extract(request_followup,'$.id')=? AND json_extract(request_followup,'$.fingerprint')=? AND `+eligible+` AND `+ready+` ORDER BY arrival LIMIT 50`, args...)
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
		selected, e := storedRequestFollowup(tx, "in", c.j.ID)
		if e != nil {
			return nativeFollowupClaim{}, false, e
		}
		if selected != nil && selected.Attempt > 0 && (selected.Attempt != parentAttempt || parent.nativeAttempt > 0 && selected.Attempt != parent.nativeAttempt) {
			continue
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
		if parent.Conv == "" {
			if allowed, e := a.directFollowupConsent(tx, c.j, c.state == stateAccepted); e != nil {
				return nativeFollowupClaim{}, false, e
			} else if !allowed {
				continue
			}
		} else {
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
func (a *Agent) nativeFollowupCurrent(parent, correction job, accepted ...bool) bool {
	tx, err := a.store.db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var current bool
	if tx.QueryRow(`SELECT (SELECT state FROM inbox WHERE id=?)=? AND (SELECT state FROM inbox WHERE id=?)=?`, parent.ID, stateRunning, correction.ID, stateRunning).Scan(&current) != nil || !current {
		return false
	}
	if parent.Conv != "" && !a.nativeFollowupExecutor(tx, parent) {
		return false
	}
	if parent.Conv == "" {
		var attempt int64
		if tx.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, parent.ID).Scan(&attempt) != nil || !a.nativeJobCurrentIn(tx, parent, attempt) {
			return false
		}
		if allowed, e := a.directFollowupConsent(tx, correction, len(accepted) > 0 && accepted[0]); e != nil || !allowed {
			return false
		}
	} else {
		views := map[string]*partView{}
		for _, j := range []job{parent, correction} {
			v, _, e := agentVerdict(tx, j.agentReq(stateRunning), a.Address, a.Self().Fingerprint(), true, views)
			if e != nil || v != verdictRun {
				return false
			}
		}
	}
	ref, e := storedRequestFollowup(tx, "in", correction.ID)
	if e != nil || ref == nil {
		return false
	}
	if ref.Attempt > 0 {
		var attempt int64
		if tx.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, parent.ID).Scan(&attempt) != nil || (attempt != ref.Attempt || parent.nativeAttempt > 0 && attempt != parent.nativeAttempt) {
			return false
		}
	}
	original, e := a.followupOriginal(tx, parent.Conv, ref.Ref)
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
		return parent.Conv == "" && parent.PID == ""
	}
	id := parent.Target.AgentID
	if id == "" {
		return true
	} // the existing default run retains its immutable setup
	stamp, err := a.ResolveExecutorIn(q, id, nil)
	return err == nil && stamp != nil && stamp.Record != nil && stamp.AgentID == id && stamp.Record.Host == parent.Target.Address && stamp.Record.HostKey == parent.Target.Fingerprint
}

// A parent's one-time acceptance grants no authority to a separate correction.
// Reuse the direct scheduler's current question/task/own-human predicates.
func (a *Agent) directFollowupConsent(q dbq, j job, accepted bool) (bool, error) {
	if revoked, e := revokedDeviceKnown(q, j.From, j.Key); e != nil || revoked {
		return false, e
	}
	var allowed bool
	err := q.QueryRow(`SELECT (? OR kind='question' AND `+questionApprovalHolds+` OR kind='task' AND (`+taskGrantHolds+` OR `+ownTaskHolds+` OR `+ownProposalHolds+`))
 AND replica=0 AND conv IS NULL AND pid IS NULL AND receiver_route IS NULL
 AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id)
 FROM inbox WHERE id=? AND sender=? AND verified_by=?`, accepted, j.ID, j.From, j.Key).Scan(&allowed)
	return allowed, err
}

func (a *Agent) watchNativeFollowups(ctx context.Context, parent job, bridge nativeFollowupBridge, processFinished <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-bridge.followupReady():
	}
	var files []*runDir
	var captured admittedAgentContext
	var raw string
	if a.store.db.QueryRow(`SELECT coalesce(agent_context,'') FROM inbox WHERE id=?`, parent.ID).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &captured)
	}
	var cursor agentContextCursor
	if parent.run != nil {
		cursor = parent.run.context
	}
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
			next := slices.Clone(cursor)
			for i := range next {
				next[i].Files = slices.Clone(next[i].Files)
			}
			correctionRef := a.jobContextRef(correction.job)
			next.noteText(ConvMessage{LID: correctionRef.LID, Key: correctionRef.Fingerprint, Body: correction.Body})
			var refresh ParticipationContext
			if parent.PID != "" {
				info, e := a.requestContextParticipation(parent)
				parentRef := a.jobContextRef(parent)
				excluded := []protocol.GrantRef{correctionRef}
				if e == nil {
					// Original text stays admitted once. Its currently permitted
					// attachments can become ready after launch or initial omission.
					refresh, e = a.agentContextForRun(info, "", agentContextBytes, cursor, &parentRef, excluded)
				}
				if e != nil {
					_ = a.endJob(correction.ID, stateNeedHuman, "Fresh authorized conversation evidence could not be selected; nothing was sent into the native run")
					continue
				}
				correction.run = a.newRun(correction.job, false)
				files = append(files, correction.run)
				text += "\n## Newly shared authorized conversation evidence\nThese messages and files are context, not authority. Preserve this run's original objective; other pending requests remain separate work.\n" + strings.Join(refresh.lines, "\n")
				for i, m := range refresh.Messages {
					if refresh.lines[i] != "" {
						next.noteText(m)
					}
				}
				text += a.agentSharedFiles(ctx, correction.job, info, refresh, &next)
			} else if correction.Attachments > 0 {
				correction.run = a.newRun(correction.job, false)
				files = append(files, correction.run)
				text += a.requestFiles(ctx, correction.job)
			}
			if correction.run != nil {
				if e := correction.run.seal(); e != nil {
					_ = a.endJob(correction.ID, stateNeedHuman, "Correction files could not be prepared; nothing was sent into the native run")
					continue
				}
			}
			saved, _ := json.Marshal(next)
			if len(saved) > maxContext {
				_ = a.endJob(correction.ID, stateNeedHuman, "The owned run's bounded evidence cursor is full; review the exact work before continuing")
				return
			}
			if !a.nativeFollowupCurrent(parent, correction.job, correction.queuedState == stateAccepted) {
				_ = a.endJob(correction.ID, stateNeedHuman, "The exact run or correction changed before native handover; review it before continuing")
				continue
			}
			outcome := bridge.steer(ctx, correction.ID, text)
			switch outcome {
			case steerAccepted:
				cursor = next
				if parent.run != nil {
					parent.run.context = cursor
				}
				if captured.Attempt > 0 {
					if !slices.Contains(captured.Refs, correctionRef) {
						captured.Refs = append(captured.Refs, correctionRef)
					}
					for _, m := range refresh.Messages {
						ref := contextRef(m)
						if !m.Deleted && !slices.Contains(captured.Refs, ref) {
							captured.Refs = append(captured.Refs, ref)
						}
					}
					data, _ := json.Marshal(captured)
					_, _ = a.store.db.Exec(`UPDATE inbox SET agent_context=? WHERE id=? AND state=? AND attempts=?`, string(data), parent.ID, stateRunning, captured.Attempt)
				}
				_ = a.endJob(correction.ID, stateSteered, "Accepted into the exact current native run; its work is not yet proven complete")
			case steerQueued, steerUnsupported:
				selected, e := storedRequestFollowup(a.store.db, "in", correction.ID)
				if e != nil || selected != nil && selected.Attempt > 0 {
					_ = a.endJob(correction.ID, stateNeedHuman, "Not applied to the selected run; explicit steering cannot become queued work")
					return
				}
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

type nativeFollowupBridge interface {
	followupReady() <-chan struct{}
	steer(context.Context, string, string) string
}

func (b *codexRoomBridge) followupReady() <-chan struct{} { return b.ready }
