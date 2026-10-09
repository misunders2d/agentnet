package client

import (
	"errors"
	"sync"
)

// Ordinary work owns one lane per immutable selected executor. The empty
// identity is the legacy/default executor, regardless of responder changes.
// A lane lasts through runJob's process/session/file cleanup, not just until
// its inbox row becomes terminal. The catalog bounds independent root runs.
type executionLanes struct {
	sync.Mutex
	jobs map[string]executionLane
}
type executionLane struct{ executor, parent string }

var errExecutorBusy = errors.New("selected executor is already running")

// executorAvailable is called under the lane mutex and inside the authority claim
// transaction. Only exact causal descendants may borrow their proven active
// ancestors' lanes; unrelated work, including another participation of that
// executor, waits. Durable claims also fence a row absent from this process.
func (a *Agent) executorAvailable(q dbq, stamp *ExecutorStamp, parent string) (bool, error) {
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
	for id, lane := range a.workerLanes.jobs {
		if lane.executor == stamp.AgentID && !ancestors[id] {
			return false, nil
		}
	}
	rows, err := q.Query(`SELECT id FROM inbox WHERE state IN ('running','cancel_requested') AND coalesce(json_extract(executor,'$.agent_id'),'')=?`, stamp.AgentID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return false, err
		}
		if !ancestors[id] {
			return false, nil
		}
	}
	return true, rows.Err()
}

func (a *Agent) availableResolver(r *Responder, parent string) func(dbq, string) (*ExecutorStamp, error) {
	return func(q dbq, id string) (*ExecutorStamp, error) {
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
		available, err := a.executorAvailable(q, stamp, parent)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, errExecutorBusy
		}
		return stamp, nil
	}
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
	a.workerLanes.jobs[j.ID] = executionLane{executor: key, parent: parent}
}
func (a *Agent) releaseExecutor(id string) {
	a.workerLanes.Lock()
	delete(a.workerLanes.jobs, id)
	a.workerLanes.Unlock()
	a.appUpdateMu.RUnlock()
	a.changes.bump() // broadcast completion even if terminal state was stored earlier
	a.wakeWorker()
}
func (a *Agent) executorsIdle() bool {
	a.workerLanes.Lock()
	defer a.workerLanes.Unlock()
	return len(a.workerLanes.jobs) == 0
}
