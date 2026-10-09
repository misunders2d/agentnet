package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Two independent roots can each await an exact question for the other.
// Allow one fresh question beside each target root, only while that reciprocal
// dependency is proven. The causal parent retains cancellation ownership.
// The caller holds workerLanes and all ordinary admission/output fences apply.
func (a *Agent) reciprocalResolver(r *Responder, parent string) func(dbq, job) (*ExecutorStamp, error) {
	return func(q dbq, child job) (*ExecutorStamp, error) {
		if child.Kind != envelope.KindQuestion || child.PID == "" || child.Receiver != nil {
			return nil, errExecutorBusy
		}
		owner, ok := a.workerLanes.jobs[parent]
		if !ok || owner.parent != "" || owner.executor == child.AgentID {
			return nil, errExecutorBusy
		}
		from, _, err := roomLaneRequest(q, parent)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errExecutorBusy
		}
		if err != nil {
			return nil, err
		}
		if from.State != stateRunning || from.Conv != child.Conv || from.Target == nil || from.Target.AgentID != owner.executor {
			return nil, errExecutorBusy
		}
		lender := ""
		for id, lane := range a.workerLanes.jobs {
			if lane.executor != child.AgentID {
				continue
			}
			if lender != "" || lane.parent != "" {
				return nil, errExecutorBusy
			}
			lender = id
		}
		if lender == "" || lender == parent {
			return nil, errExecutorBusy
		}
		to, lid, err := roomLaneRequest(q, lender)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errExecutorBusy
		}
		if err != nil {
			return nil, err
		}
		if to.State != stateRunning || to.Conv != child.Conv || to.PID != child.PID || to.Target == nil || to.Target.AgentID != child.AgentID {
			return nil, errExecutorBusy
		}
		// Cleanup reservations and orphaned durable claims also consume the slot.
		var count int
		var only string
		if err := q.QueryRow(`SELECT count(*),coalesce(max(id),'') FROM inbox
   WHERE state IN ('running','cancel_requested') AND coalesce(json_extract(executor,'$.agent_id'),'')=?`, child.AgentID).Scan(&count, &only); err != nil {
			return nil, err
		}
		if count != 1 || only != lender {
			return nil, errExecutorBusy
		}
		self, fp := a.Address, a.Self().Fingerprint()
		views := map[string]*partView{}
		verdict, _, err := agentVerdict(q, to, self, fp, true, views)
		if err != nil {
			return nil, err
		}
		view := views[to.Conv+"/"+to.PID]
		if verdict != verdictRun || view == nil || view.m.group == nil {
			return nil, errExecutorBusy
		}
		// A running reverse child still proves the dependency while the second
		// child is claimed. Terminal records cannot lend a slot.
		rows, err := q.Query(`SELECT id FROM inbox WHERE conv=? AND pid=? AND reply_to=?
   AND kind=? AND replica=0 AND state IN (?,?,?)`, child.Conv, from.PID, lid,
			envelope.KindQuestion, stateAgentWaiting, stateAccepted, stateRunning)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			reverse, _, err := roomLaneRequest(q, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if reverse.Target == nil || reverse.Target.AgentID != owner.executor {
				continue
			}
			running := reverse.State == stateRunning
			if running {
				lane, live := a.workerLanes.jobs[id]
				if !live || lane.executor != owner.executor || lane.parent != lender {
					continue
				}
			}
			verdict, _, err := agentVerdict(q, reverse, self, fp, running, views)
			if err != nil {
				return nil, err
			}
			if verdict != verdictRun {
				continue
			}
			cause, err := roomLocalParent(q, reverse, self, fp)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if cause == lender {
				return a.ResolveExecutorIn(q, child.AgentID, r)
			}
		}
		return nil, errExecutorBusy
	}
}

// Only an executable local row can own or justify a lane. Receiver
// continuations retain their separate native-session serialization.
func roomLaneRequest(q dbq, id string) (r agentReq, lid string, err error) {
	var target string
	err = q.QueryRow(`SELECT id,sender,coalesce(verified_by,''),kind,conv,pid,state,target,local,coalesce(lid,id)
  FROM inbox WHERE id=? AND pid IS NOT NULL AND target IS NOT NULL AND replica=0
  AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id)`, id).
		Scan(&r.ID, &r.Sender, &r.Key, &r.Kind, &r.Conv, &r.PID, &r.State, &target, &r.Local, &lid)
	if err != nil {
		return r, lid, err
	}
	r.Target = &envelope.Target{}
	if err = json.Unmarshal([]byte(target), r.Target); err != nil {
		return r, lid, err
	}
	return r, lid, nil
}
