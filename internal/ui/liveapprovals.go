package ui

import (
	"context"
	"net/http"
	"slices"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Standing grants on the page: what agentnet approvals lists, and revoking
// one through the same operations as agentnet unapprove, unapprove --tasks
// and dm dismiss-agent. Nothing here grants anything: a grant is made by
// a decision (approve, accept --always, accepting an agent's invitation).

// ApprovalsProvider is implemented by providers that keep standing grants
// (the daemon). The browser device holds none: its view is read-only and
// empty.
type ApprovalsProvider interface {
	Approvals() (ApprovalsView, error)
	RevokeApproval(ApprovalRevoke) (string, error)
}

// ApprovalsView is this device's standing grants, as agentnet approvals
// lists them: devices whose questions are answered automatically, devices
// whose tasks run without asking (with the granted key and whether the
// grant still holds), and this device's agents in conversations that run
// the tasks of the member keys their accepted invitation named. ReadOnly:
// grants are not kept on this device (the browser).
type ApprovalsView struct {
	Questions      []QuestionApproval   `json:"questions"`
	Tasks          []TaskGrantView      `json:"tasks"`
	Participations []ParticipationGrant `json:"participations"`
	ReadOnly       bool                 `json:"read_only"`
}

// QuestionApproval is a device whose questions are answered automatically.
type QuestionApproval struct {
	Address string `json:"address"`
}

// TaskGrantView is a device whose tasks run without asking, from the key
// granted: Status is "active", or why the grant does not hold now (the key
// changed, or a change waits for trust).
type TaskGrantView struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
}

// ParticipationGrant is this device's agent in a conversation, running
// without asking the tasks of the member keys (Keys; TasksFrom: their
// people as known here) that its invitation named: accepting it granted
// this, until the participation ends. External: it is an outside host's
// agent there, so only a member of the conversation can end it; revoking
// it here is refused.
type ParticipationGrant struct {
	Conv      string       `json:"conv"`
	PID       string       `json:"pid"`
	AgentID   string       `json:"agent_id,omitempty"`
	Keys      []string     `json:"keys"`
	TasksFrom []PersonView `json:"tasks_from"`
	External  bool         `json:"external,omitempty"`
}

// ApprovalRevoke names one standing grant to end: Kind "question" or
// "task" with the device's Address (agentnet unapprove [--tasks]), or
// "participation" with the PID of this device's agent (agentnet dm
// dismiss-agent: the agent leaves the conversation).
type ApprovalRevoke struct {
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	PID     string `json:"pid,omitempty"`
}

// Approvals implements ApprovalsProvider.
func (l *Live) Approvals() (ApprovalsView, error) {
	v := ApprovalsView{Questions: []QuestionApproval{}, Tasks: []TaskGrantView{}, Participations: []ParticipationGrant{}}
	qs, err := l.a.QuestionApprovals()
	if err != nil {
		return v, err
	}
	for _, q := range qs {
		v.Questions = append(v.Questions, QuestionApproval{Address: q})
	}
	ts, err := l.a.TaskGrants()
	if err != nil {
		return v, err
	}
	for _, g := range ts {
		v.Tasks = append(v.Tasks, TaskGrantView{Address: g.Address, Fingerprint: g.Fingerprint, Status: g.Status})
	}
	convs, err := l.a.Conversations()
	if err != nil {
		return v, err
	}
	for _, c := range convs {
		parts, err := l.a.Participations(c.ID)
		if err != nil {
			return v, err
		}
		people := l.conversationPeople(c)
		for _, p := range parts {
			if g, ok := participationGrant(p, people); ok {
				v.Participations = append(v.Participations, g)
			}
		}
	}
	return v, nil
}

// participationGrant is p as a standing grant, if it is one: this device's
// agent, active, invited with task keys (as agentnet approvals lists it).
func participationGrant(p client.ParticipationInfo, people dmPeople) (ParticipationGrant, bool) {
	if !p.HostHere || !p.Claimable() || len(p.TaskKeys) == 0 {
		return ParticipationGrant{}, false
	}
	g := ParticipationGrant{Conv: p.Conv, PID: p.PID, AgentID: p.AgentID, Keys: slices.Clone(p.TaskKeys), TasksFrom: []PersonView{}, External: p.External}
	for _, fp := range p.TaskKeys {
		if who, ok := people.byKey(fp); ok {
			g.TasksFrom = append(g.TasksFrom, who)
		}
	}
	return g, true
}

// RevokeApproval implements ApprovalsProvider: only a grant listed now is
// revoked, through its own operation.
func (l *Live) RevokeApproval(r ApprovalRevoke) (string, error) {
	v, err := l.Approvals()
	if err != nil {
		return "", err
	}
	switch r.Kind {
	case "question":
		if !slices.Contains(v.Questions, QuestionApproval{Address: r.Address}) {
			return "", NotFound("No standing approval of questions from " + r.Address + ".")
		}
		if err := l.a.Unapprove(r.Address); err != nil {
			return "", Refuse(sentence(err))
		}
		l.a.NoteChange()
		return r.Address + "'s questions wait for you again.", nil
	case "task":
		if !slices.ContainsFunc(v.Tasks, func(g TaskGrantView) bool { return g.Address == r.Address }) {
			return "", NotFound("No standing task grant for " + r.Address + ".")
		}
		running, err := l.a.RevokeTasks(r.Address)
		if err != nil {
			return "", Refuse(sentence(err))
		}
		l.a.NoteChange()
		if len(running) > 0 {
			return "Tasks from " + r.Address + " wait for you again; one already running may finish unless you stop it.", nil
		}
		return "Tasks from " + r.Address + " wait for you again.", nil
	case "participation":
		i := slices.IndexFunc(v.Participations, func(g ParticipationGrant) bool { return g.PID == r.PID })
		if i < 0 {
			return "", NotFound("No standing task grant of your agent with that id.")
		}
		if v.Participations[i].External {
			return "", Refuse("Only a member of that conversation can end your agent's part in it; this device cannot.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		if _, err := l.a.DismissParticipation(ctx, r.PID); err != nil {
			return "", Refuse(sentence(err))
		}
		l.a.NoteChange()
		return "Your agent left that conversation: its standing task grant ended.", nil
	}
	return "", Refuse("Choose a question approval, a task grant or your agent's grant in a conversation.")
}

// approvals serves GET /api/approvals.
func (s *Server) approvals(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(ApprovalsProvider)
	if !ok {
		writeErr(w, NotFound("standing grants are not kept here"))
		return
	}
	v, err := p.Approvals()
	writeResult(w, v, err)
}

// revokeApproval serves POST /api/approvals/revoke: one exact grant, named
// by its kind and its device's address or its agent's PID.
func (s *Server) revokeApproval(w http.ResponseWriter, r *http.Request) {
	var x ApprovalRevoke
	if !readJSON(w, r, &x) {
		return
	}
	_, _, addrErr := protocol.SplitAddress(x.Address)
	valid := false
	switch x.Kind {
	case "question", "task":
		valid = addrErr == nil && x.PID == ""
	case "participation":
		valid = protocol.ValidID(x.PID) && x.Address == ""
	}
	if !valid {
		writeErr(w, Refuse("Choose one standing grant to revoke."))
		return
	}
	p, ok := s.p.(ApprovalsProvider)
	if !ok {
		writeErr(w, NotFound("standing grants are not kept here"))
		return
	}
	note, err := p.RevokeApproval(x)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, struct {
		Note string `json:"note"`
	}{note})
}
