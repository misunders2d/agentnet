package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A proven fresh conversation can run beside another conversation of the same
// immutable executor. Saved/default sessions and unknown launchers serialize.
// Every lane lasts through process/session/file cleanup, including after its
// inbox row becomes terminal. The existing catalog bound caps independent roots.
type executionLanes struct {
	sync.Mutex
	jobs map[string]executionLane
}
type executionLane struct {
	executor, parent, conv, native string
	fresh                          bool
	peerWaits                      int
}

const roomWaitDetail = "Waiting for an exact peer reply; this request remains active. Stop and exact follow-ups are available."

// A validated owned native wait reports its actual dependency. Overlapping
// waits keep this reason until the last one finishes. No polling or new job.
func (a *Agent) noteRoomWait(id string, delta int) {
	a.workerLanes.Lock()
	lane, ok := a.workerLanes.jobs[id]
	if !ok {
		a.workerLanes.Unlock()
		return
	}
	lane.peerWaits = max(0, lane.peerWaits+delta)
	a.workerLanes.jobs[id] = lane
	var changed bool
	if lane.peerWaits > 0 {
		res, err := a.store.db.Exec(`UPDATE inbox SET detail=? WHERE id=? AND state=? AND coalesce(detail,'')<>?`, roomWaitDetail, id, stateRunning, roomWaitDetail)
		if err == nil {
			n, _ := res.RowsAffected()
			changed = n > 0
		}
	} else {
		_, _ = a.store.db.Exec(`UPDATE inbox SET detail=NULL WHERE id=? AND state=? AND detail=?`, id, stateRunning, roomWaitDetail)
		changed, _ = a.store.markRunVisibility(id, false)
	}
	a.workerLanes.Unlock()
	if changed {
		a.store.changed()
		a.noteStatus(id)
	}
}

var errExecutorBusy = errors.New("selected executor is already running")
var errIndependentLimit = fmt.Errorf("%w: waiting: independent run limit reached; existing work must finish", errExecutorBusy)

// executorAvailable is called under the lane mutex and inside the authority claim
// transaction. Only exact causal descendants may borrow their proven active
// ancestors' lanes. Independent fresh conversations use the same predicate for
// memory reservations and durable claims absent from this process.
func (a *Agent) executorAvailable(q dbq, stamp *ExecutorStamp, parent string, candidates ...job) (bool, error) {
	if stamp == nil {
		return true, nil
	}
	ancestors := map[string]bool{}
	for id := parent; id != ""; {
		lane, ok := a.workerLanes.jobs[id]
		if !ok {
			break
		}
		ancestors[id] = true
		id = lane.parent
	}
	var candidate job
	if len(candidates) > 0 {
		candidate = candidates[0]
	}
	fresh := independentSession(candidate, stamp)
	roots := 0
	for id, lane := range a.workerLanes.jobs {
		if lane.parent == "" {
			roots++
		}
		if lane.executor == stamp.AgentID && !ancestors[id] && !(fresh && lane.fresh && candidate.Conv != lane.conv) {
			return false, nil
		}
	}
	// Reuse the previous effective maximum: one root per catalog entry plus
	// the default. Exact controls and causal descendants do not use this cap.
	if parent == "" && roots >= protocol.MaxAgentCatalog+1 {
		return false, errIndependentLimit
	}
	rows, err := q.Query(`SELECT id,coalesce(conv,''),coalesce(pid,''),coalesce(agent_id,''),kind,coalesce(executor,'null'),
	EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id)
	FROM inbox WHERE state IN ('running','cancel_requested') AND coalesce(json_extract(executor,'$.agent_id'),'')=?`, stamp.AgentID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var other job
		var raw string
		var receiver bool
		if err = rows.Scan(&other.ID, &other.Conv, &other.PID, &other.AgentID, &other.Kind, &raw, &receiver); err != nil {
			return false, err
		}
		var selected ExecutorStamp
		if json.Unmarshal([]byte(raw), &selected) != nil {
			return false, errors.New("invalid running executor snapshot")
		}
		if receiver {
			other.Receiver = &ReplyReceiverBinding{}
		}
		if !ancestors[other.ID] && !(fresh && independentSession(other, &selected) && candidate.Conv != other.Conv) {
			return false, nil
		}
	}
	return true, rows.Err()
}

// Called after thread/start or exact thread/resume, before any turn input.
// Native identity, rather than a catalog label, fences actual shared sessions.
// The existing session_ref records the live identity for durable fencing; an
// ephemeral run removes it only after its owned process has actually exited.
func (a *Agent) bindNativeThread(j job, ref sessionRef, newThread ...bool) bool {
	if a.personGrantStop(j) != "" || j.Receiver != nil && a.receiverStop(j) != "" || j.PID != "" && j.Receiver == nil && a.agentStop(j) != "" {
		return false
	}
	a.workerLanes.Lock()
	defer a.workerLanes.Unlock()
	if len(newThread) > 0 && newThread[0] && a.store.sessionHead(ref) != "" {
		return false
	} // thread/start cannot substitute a known saved thread
	for id, lane := range a.workerLanes.jobs {
		if id != j.ID && lane.native == ref.ID {
			return false
		}
	}
	var busy bool
	err := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE id<>? AND state IN ('running','cancel_requested') AND json_extract(session_ref,'$.harness')=? AND json_extract(session_ref,'$.id')=?)`, j.ID, ref.Harness, ref.ID).Scan(&busy)
	if err != nil || busy || a.store.setSessionRef(j.ID, ref, true) != nil {
		return false
	}
	if lane, ok := a.workerLanes.jobs[j.ID]; ok {
		lane.native = ref.ID
		a.workerLanes.jobs[j.ID] = lane
	}
	return true
}

func (a *Agent) availableResolver(r *Responder, parent string) func(dbq, job) (*ExecutorStamp, error) {
	return func(q dbq, j job) (*ExecutorStamp, error) {
		id := j.AgentID
		current := r
		if id == "" {
			// The scheduler's earlier read is only a candidate hint. Resolve
			// the default from the same transaction that claims this job.
			var err error
			current, err = responderIn(q)
			if err != nil {
				return nil, err
			}
			if current == nil {
				return nil, errExecutorBusy // manual handling leaves it unclaimed
			}
		}
		stamp, err := a.ResolveExecutorIn(q, id, current)
		if err != nil {
			return nil, err
		}
		available, err := a.executorAvailable(q, stamp, parent, j)
		if err != nil {
			return nil, err
		}
		if !available {
			why := "waiting: native session isolation requires earlier selected work to finish"
			if independentSession(j, stamp) {
				why = "waiting: this selected agent already has work in this conversation; choose the exact active request to follow up"
			}
			return nil, fmt.Errorf("%w: %s", errExecutorBusy, why)
		}
		return stamp, nil
	}
}

// Queue status changes use the existing durable status projection. No busy
// request is claimed, and unchanged reasons do not trigger more work/traffic.
func noteExecutionWait(q *sql.Tx, id, why string) (bool, error) {
	res, err := q.Exec(`UPDATE inbox SET detail=?,status_due=status_due+1 WHERE id=? AND state IN ('pending','accepted','part_waiting') AND coalesce(detail,'')<>?`, why, id, why)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// reserveExecutor runs while the same mutex still covers the claim.
func (a *Agent) reserveExecutor(j job, parent string) {
	if a.workerLanes.jobs == nil {
		a.workerLanes.jobs = map[string]executionLane{}
	}
	key := j.AgentID
	if j.Executor != nil {
		key = j.Executor.AgentID
	}
	a.workerLanes.jobs[j.ID] = executionLane{executor: key, parent: parent, conv: j.Conv, fresh: independentSession(j, j.Executor)}
}

// Only this branch of planSession is guaranteed not to select a saved or
// explicitly delegated session. Unrecognized harness/launcher contracts keep
// conservative executor serialization. Codex checks its actual returned thread
// before turn/start, including when the owner's PATH entry is a wrapper.
func independentSession(j job, stamp *ExecutorStamp) bool {
	if stamp == nil || j.Conv == "" || j.Receiver != nil || j.PID == "" && j.AgentID == "" || j.Kind != envelope.KindQuestion && j.Kind != envelope.KindTask {
		return false
	}
	h := Harnesses[stamp.Responder.Harness]
	if h.sessions == codexSessions {
		return true
	}
	args := h.question
	if j.Kind == envelope.KindTask {
		args = h.task
	}
	known := h.bin == stamp.Responder.Harness && (stamp.Responder.Harness == "claude" && h.sessions == claudeSessions && slices.Contains(args, sessionOneShot) ||
		(stamp.Responder.Harness == "pi" || stamp.Responder.Harness == "omp") && h.sessions == noSessions && slices.Contains(args, "--no-session"))
	if !known {
		return false
	}
	for _, flag := range []string{"--resume", "--continue", "--session", "--session-id", "--conversation", "-r", "-c"} {
		if slices.Contains(args, flag) {
			return false
		}
	}
	launch, err := resolveNativeLaunch(h.bin)
	return err == nil && (launch.Installation == "direct" || launch.Installation == "npm-compatible")
}
func (a *Agent) releaseExecutor(id string) {
	a.workerLanes.Lock()
	delete(a.workerLanes.jobs, id)
	a.workerLanes.Unlock()
	a.appUpdateMu.RUnlock()
	a.noteBusyQueue() // the last run ended: queued requests are no longer "busy with another request" (MEL-580)
	a.changes.bump()  // broadcast completion even if terminal state was stored earlier
	a.wakeWorker()
}
func (a *Agent) executorsIdle() bool {
	a.workerLanes.Lock()
	defer a.workerLanes.Unlock()
	return len(a.workerLanes.jobs) == 0
}
