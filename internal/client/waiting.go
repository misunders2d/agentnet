package client

import (
	"time"
)

// Waiting is what else waits here besides the review items (Review): the
// requests to this device's agent that have not run, invitations for its
// agent or its person, devices asking to be linked, and messages held
// back. Listing it decides, runs and marks nothing; each is decided where
// its own command says.
type Waiting struct {
	AgentRequests []WaitingRequest      `json:"agent_requests,omitempty"` // part_waiting, with why
	AgentInvites  []ConvReview          `json:"agent_invites,omitempty"`  // for this device's agent (dm accept-agent, dm decline-agent)
	GroupInvites  []GroupInvitationInfo `json:"group_invites,omitempty"`  // for this person, not decided (group accept, group decline)
	Links         []LinkRequest         `json:"links,omitempty"`          // devices asking to be linked to this person (person approve, person refuse)
	Held          []Quarantined         `json:"held,omitempty"`           // messages held back, except those only waiting for proof
}

// WaitingRequest is a request to this device's agent that has not run.
type WaitingRequest struct {
	ID   string `json:"id"`
	Conv string `json:"conv"`
	PID  string `json:"pid"`
	From string `json:"from"`
	Kind string `json:"kind"`
	Why  string `json:"why"`
	// Stuck: nothing here would run it (NothingRuns), whatever arrives.
	Stuck bool      `json:"stuck,omitempty"`
	At    time.Time `json:"at"`
}

// Count is how many items Waiting lists.
func (w Waiting) Count() int {
	return len(w.AgentRequests) + len(w.AgentInvites) + len(w.GroupInvites) + len(w.Links) + len(w.Held)
}

// Waiting lists what else waits here (see Waiting). A request whose agent
// nothing here would run says so (NothingRuns); any other waits for its
// participation to allow it (evidence, an acceptance), checked again on
// every change.
func (a *Agent) Waiting() (Waiting, error) {
	var w Waiting
	rows, err := a.store.db.Query(`SELECT id, conv, pid, sender, kind, coalesce(json_extract(target, '$.agent_id'), ''), received_at FROM inbox
		WHERE state = ? AND pid IS NOT NULL AND replica = 0 AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs ri WHERE ri.inbox_id = inbox.id)
		ORDER BY received_ms, id`, stateAgentWaiting)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var r WaitingRequest
		var agentID string
		var at int64
		if err := rows.Scan(&r.ID, &r.Conv, &r.PID, &r.From, &r.Kind, &agentID, &at); err != nil {
			rows.Close()
			return w, err
		}
		r.At, r.Why = time.Unix(at, 0), agentID // resolved below, once the rows are closed
		w.AgentRequests = append(w.AgentRequests, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return w, err
	}
	for i := range w.AgentRequests {
		r := &w.AgentRequests[i]
		if r.Why = a.NothingRuns(r.Why); r.Why == "" {
			r.Why = "it runs once its participation allows it (an acceptance, or evidence still on its way)"
		} else {
			r.Stuck = true
		}
	}
	if w.AgentInvites, err = a.hostInvites(); err != nil {
		return w, err
	}
	groups, err := a.GroupInvitations()
	if err != nil {
		return w, err
	}
	for _, g := range groups {
		if g.Direction == "in" && g.State == "pending" {
			w.GroupInvites = append(w.GroupInvites, g)
		}
	}
	links, err := a.PendingLinks()
	if err != nil {
		return w, err
	}
	now := time.Now().Unix()
	for _, l := range links {
		if l.State == LinkPending && l.Expires > now {
			w.Links = append(w.Links, l)
		}
	}
	held, err := a.Quarantine()
	if err != nil {
		return w, err
	}
	for _, q := range held {
		if q.Reason != reasonProof { // retried on new evidence by itself
			w.Held = append(w.Held, q)
		}
	}
	return w, nil
}
