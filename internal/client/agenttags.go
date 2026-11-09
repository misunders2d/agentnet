package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Owner, 2026-11-09: "agentnet is an AGENT NET: they communicate, delegate
// and cooperate". An agent's exact mention of another agent present in its
// chat is a real request to that agent, as a person's tag is: the tagging
// reply is posted as the agent's own ask (postAgentAsk, MEL-540/MEL-713) and
// its run waits for the answer (WaitForHelper, parkForHelpers), then runs
// again with it and answers its requester.
//
// Authority is the existing one. The sending host keeps the chain rule
// (RunAgentRoomAsk): a run may give a task only to its person's own agents,
// and only when its requester may task it; otherwise it asks a question.
// The receiving host admits it as any agent's ask: its own person's agents
// run (own devices are the same person); another person's need this host's
// normal question or task grant, or a one-time accept (agentVerdict).
//
// Loop safety, as for room asks (ROOM_V1 §4.3: no count or nesting
// limits; actual cycles stop): an agent already in this request's tree of
// agent asks, as asker or asked, this run's own agent included, is never
// asked again from it, and a rerun's repeated tag is the same ask (one per
// target per request). The tree is the chain upstream and the asks this
// request started downstream, by tag or `room ask`: a reply relaying their
// answers carries those agents' mentions (a group answer opens with its
// asker's), which are not a new request (TestRoomBusyAncestorCallback). The chain is what this host holds; another
// person's host checks its own grants for every ask regardless.

const agentTagPromptText = "Writing an agent's exact mention from this list in your reply hands your reply to that agent as a request: a task for your owner's own agents when your requester may give you tasks, otherwise a question. Your reply is then held, and you run again with its answer before you answer here. Tag an agent only when you want it to act or answer; a plain name is not a tag.\n"

var agentTagRe = regexp.MustCompile(`\[@[^\]\r\n]{1,80}\]\(agentnet:agent/([A-Za-z0-9_-]{1,64})\)`)

var codeRe = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

// taggedAgents are the participation IDs text tags exactly, outside code,
// in order, once each.
func taggedAgents(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range agentTagRe.FindAllStringSubmatch(codeRe.ReplaceAllString(text, ""), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// routeAgentTags sends run j's reply, as its agent, to each present agent it
// tags; the run then parks for their answers (worker.go). Failures are
// logged and leave the reply to go out as usual.
func (a *Agent) routeAgentTags(ctx context.Context, j job, harness, body string) {
	if j.PID == "" || j.Receiver != nil {
		return
	}
	text, _, ok := splitEmotion(body)
	if !ok {
		text = strings.TrimSpace(body)
	}
	targets := taggedAgents(text)
	if len(targets) == 0 {
		return
	}
	from, err := a.requestContextParticipation(j)
	if err != nil || !from.Claimable() || !from.HostHere || from.Role != "" {
		return
	}
	chain, err := a.agentAskChain(j.Conv, j.ID)
	if err != nil {
		a.Logf("%s %s: tagged agents not asked: %v", j.Kind, j.ID, err)
		return
	}
	if err := a.agentAskDescendants(j.Conv, j.ID, chain); err != nil {
		a.Logf("%s %s: tagged agents not asked: %v", j.Kind, j.ID, err)
		return
	}
	chain[from.PID] = true
	cause, lid := j.ID, a.jobContextRef(j).LID // sent by physical ID; every copy names the logical one (conv.go)
	var topic string
	if err := a.store.db.QueryRow(`SELECT coalesce(topic,'') FROM inbox WHERE id=?`, j.ID).Scan(&topic); err != nil {
		return
	}
	mayTask, err := a.runMayTask(j.From, j.Key, j.Kind)
	if err != nil {
		return
	}
	for _, pid := range targets {
		if chain[pid] {
			continue
		}
		to, err := a.participation(j.Conv, pid)
		if err != nil || to.Role != "" || !to.Claimable() || to.Topic != nil && *to.Topic != topic {
			continue
		}
		var dup bool
		if err := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE conv=? AND pid=? AND reply_to IN (?,?) AND kind IN ('question','task') AND json_extract(human,'$.author_pid')=?)`, j.Conv, to.PID, cause, lid, from.PID).Scan(&dup); err != nil || dup {
			continue
		}
		kind := envelope.KindQuestion
		if mayTask && to.Host.Person == from.Host.Person {
			kind = envelope.KindTask
		}
		sent, err := a.postAgentAsk(ctx, j.Conv, topic, cause, from, to, kind, text, harness, nil, nil)
		if err != nil {
			a.Logf("%s %s: could not ask tagged agent %s: %v", j.Kind, j.ID, to.PID, err)
			continue
		}
		if err := a.WaitForHelper(j.ID, sent.ID); err != nil {
			a.Logf("%s %s: asked tagged agent %s (%s) but cannot wait for it: %v", j.Kind, j.ID, to.PID, sent.ID, err)
			continue
		}
		a.Logf("%s %s: asked tagged agent %s (%s %s)", j.Kind, j.ID, to.PID, kind, sent.ID)
	}
}

// agentAskChain is the agents in request id's chain of agent asks in
// conv, as held here: each ask (its captured audience names an agent
// author) adds its author and the agent it asks, then its cause, until a
// request no agent asked.
func (a *Agent) agentAskChain(conv, id string) (map[string]bool, error) {
	chain := map[string]bool{}
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		var raw, pid, reply string
		err := a.store.db.QueryRow(`SELECT coalesce(human,''),coalesce(pid,''),coalesce(reply_to,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND coalesce(sub,'')='' AND kind IN ('question','task')
			UNION ALL SELECT coalesce(human,''),coalesce(pid,''),coalesce(reply_to,'') FROM outbox WHERE conv=? AND (id=? OR lid=?) AND coalesce(sub,'')='' AND kind IN ('question','task') LIMIT 1`, conv, id, id, conv, id, id).Scan(&raw, &pid, &reply)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		var h envelope.HumanTurn
		if raw == "" || json.Unmarshal([]byte(raw), &h) != nil || !h.AgentAuthor() {
			break
		}
		chain[h.AuthorPID], chain[pid] = true, true
		id = reply
	}
	return chain, nil
}

// agentAskDescendants adds to tree the agents in the asks request id
// started in conv, and in theirs, as held here: each ask replying to one
// of them (by physical or logical ID) whose captured audience names an
// agent author adds its author and the agent it asks.
func (a *Agent) agentAskDescendants(conv, id string, tree map[string]bool) error {
	frontier := []string{id}
	if lid := a.jobContextRef(job{ID: id}).LID; lid != id {
		frontier = append(frontier, lid)
	}
	seen := map[string]bool{}
	for len(frontier) > 0 {
		ref := frontier[0]
		frontier = frontier[1:]
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		rows, err := a.store.db.Query(`SELECT id,coalesce(lid,''),coalesce(pid,''),coalesce(human,'') FROM inbox WHERE conv=? AND reply_to=? AND coalesce(sub,'')='' AND kind IN ('question','task')
			UNION ALL SELECT id,coalesce(lid,''),coalesce(pid,''),coalesce(human,'') FROM outbox WHERE conv=? AND reply_to=? AND coalesce(sub,'')='' AND kind IN ('question','task')`, conv, ref, conv, ref)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid, clid, pid, raw string
			if err := rows.Scan(&cid, &clid, &pid, &raw); err != nil {
				rows.Close()
				return err
			}
			var h envelope.HumanTurn
			if raw == "" || json.Unmarshal([]byte(raw), &h) != nil || !h.AgentAuthor() {
				continue
			}
			tree[h.AuthorPID], tree[pid] = true, true
			frontier = append(frontier, cid, clid)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// dmAgentAskRuns: may an agent's ask r in a DM run without a one-time accept?
// Its author must be a current agent participation hosted by r's exact
// sender key. An agent of this host's own person runs (the same person's
// devices; the sending host kept the chain rule); another person's agent
// needs this host's standing grant for that device or person.
func dmAgentAskRuns(q dbq, r agentReq, m dmMembers, info ParticipationInfo) (bool, error) {
	h, err := storedHuman(q, "in", r.ID)
	if err != nil || h == nil || !h.AgentAuthor() {
		return false, err
	}
	author, err := participationIn(q, r.Conv, h.AuthorPID, m, "")
	if errors.Is(err, ErrNoParticipation) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if author.Role != "" || !author.Claimable() || author.Host.Address != r.Sender || author.Host.Fingerprint != r.Key || author.PID == info.PID {
		return false, nil
	}
	if own, ok := m.persons[info.Host.Person]; ok && author.Host.Person == info.Host.Person && own.has(r.Sender, r.Key) {
		return true, nil
	}
	if r.Kind == envelope.KindQuestion {
		return questionApproved(q, r.Sender, r.Key)
	}
	return taskGranted(q, r.Sender, r.Key)
}
