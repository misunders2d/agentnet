package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Membership consent allows future group context, never earlier/native sessions.
// Exact references are recorded at admission, not inferred from claimed times.
const roomSchema = `CREATE TABLE room_context(conv TEXT NOT NULL,pid TEXT NOT NULL,lid TEXT NOT NULL,fingerprint TEXT NOT NULL,PRIMARY KEY(conv,pid,lid,fingerprint));
CREATE TABLE room_membership_events(hash TEXT PRIMARY KEY,conv TEXT NOT NULL,pid TEXT NOT NULL);`

// Local admission evidence for selected shares, never sender-supplied authority.
const roomReaderSchema = `CREATE TABLE room_turn_readers(conv TEXT NOT NULL,lid TEXT NOT NULL,fingerprint TEXT NOT NULL,person TEXT NOT NULL,admission TEXT NOT NULL,PRIMARY KEY(conv,lid,fingerprint,person,admission));`

func recordRoomContext(tx *sql.Tx, in envelope.Inner, fp, self string) error {
	if in.Conv == "" || in.Sub != "" && in.Sub != envelope.SubEvent || in.LID == "" {
		return nil
	}
	m, err := membersIn(tx, in.Conv)
	if errors.Is(err, ErrGroupContextPending) {
		return nil
	} // initial visitor invitation precedes its verified context
	if err != nil {
		return err
	}
	if m.group == nil {
		return nil
	}
	if in.Sub == "" {
		for person := range m.persons {
			member, ok := m.group.State.Member(person)
			if ok {
				if _, err = tx.Exec(`INSERT OR IGNORE INTO room_turn_readers VALUES(?,?,?,?,?)`, in.Conv, in.LID, fp, person, member.Admission.Hash()); err != nil {
					return err
				}
			}
		}
	}
	rows, err := tx.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv=?`, in.Conv)
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
	for _, pid := range ids {
		p, e := participationIn(tx, in.Conv, pid, m, self)
		if e != nil {
			return e
		}
		if p.Member && p.Held == 0 && (p.State == PartActive || p.State == PartDismissed) {
			events, e := participationEventsIn(tx, in.Conv, pid)
			if e != nil {
				return e
			}
			for _, ev := range events {
				h := ev.Hash()
				if h == p.Invite || h == p.Scope || h == p.Decision || h == p.Dismissal || slices.Contains(p.Shares, h) {
					if _, e = tx.Exec(`INSERT OR IGNORE INTO room_membership_events VALUES(?,?,?)`, h, in.Conv, pid); e != nil {
						return e
					}
				}
			}
		}
		if in.Sub != "" {
			continue
		}
		if p.Member && p.Claimable() && p.Host.Address == self && in.Human != nil && slices.ContainsFunc(in.Human.Audience, func(scope envelope.HumanScope) bool {
			return scope.PID == pid && scope.Invite == p.Invite && scope.Decision == p.Decision
		}) {
			if _, e = tx.Exec(`INSERT OR IGNORE INTO room_context VALUES(?,?,?,?)`, in.Conv, pid, in.LID, fp); e != nil {
				return e
			}
		}
	}
	return nil
}

// roomAudience supplies exact signed consent; labels never confer access.
func (a *Agent) roomAudience(conv, author string) (*envelope.HumanTurn, error) {
	infos, err := a.Participations(conv)
	if err != nil {
		return nil, err
	}
	h := &envelope.HumanTurn{AuthorPID: author}
	for _, p := range infos {
		if !p.Following() {
			continue
		}
		scope, err := a.participationScope(context.Background(), p)
		if err != nil {
			return nil, err
		}
		events, err := a.store.participationEvents(conv, p.PID)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Hash() == p.Decision {
				h.Audience = append(h.Audience, envelope.HumanScope{PID: p.PID, Invite: p.Invite, Decision: p.Decision})
				h.Proof = append(h.Proof, scope, e)
				break
			}
		}
	}
	if len(h.Audience) == 0 {
		return nil, errors.New("room has no accepted agents")
	}
	return h, h.Validate(conv)
}

// roomCause reads only locally admitted originals/copies of this group. A
// sender cannot replace an upstream origin by putting a name in its body.
var errAmbiguousRoomCause = errors.New("agent request origin is ambiguous")

type roomCause struct {
	id, from, key, kind, pid, reply, state string
	target                                 *envelope.Target
	human                                  *envelope.HumanTurn
}

func roomCauseIn(q dbq, conv, ref, self, fp string) (roomCause, error) {
	var c roomCause
	var target, human string
	rows, err := q.Query(`SELECT lid,sender,coalesce(verified_by,''),kind,coalesce(pid,''),coalesce(reply_to,''),state,coalesce(target,''),coalesce(human,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND verified_by IS NOT NULL AND sub IS NULL AND kind IN ('question','task') AND target IS NOT NULL
 UNION ALL SELECT lid,?, ?,kind,coalesce(pid,''),coalesce(reply_to,''),state,coalesce(target,''),coalesce(human,'') FROM outbox WHERE conv=? AND (id=? OR lid=?) AND sub IS NULL AND kind IN ('question','task') AND target IS NOT NULL`, conv, ref, ref, self, fp, conv, ref, ref)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var next roomCause
		var t, h string
		if err = rows.Scan(&next.id, &next.from, &next.key, &next.kind, &next.pid, &next.reply, &next.state, &t, &h); err != nil {
			return c, err
		}
		if found && (c.id != next.id || c.from != next.from || c.key != next.key || c.kind != next.kind || c.pid != next.pid || c.reply != next.reply || target != t || human != h) {
			return c, errAmbiguousRoomCause
		}
		if !found {
			c, target, human = next, t, h
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		return c, err
	}
	if !found {
		return c, sql.ErrNoRows
	}
	if target != "" {
		if err = json.Unmarshal([]byte(target), &c.target); err != nil {
			return c, err
		}
	}
	if human != "" {
		err = json.Unmarshal([]byte(human), &c.human)
	}
	return c, err
}

// roomChain checks each cause before using local standing grants. Each
// upstream key needs its own permission at the final host: an intermediary
// never lends its owner's authority. No depth/count cap; actual cycles stop.
func roomChain(q dbq, r agentReq, m dmMembers, info ParticipationInfo, self, fp string, output bool) (int, string, error) {
	c, err := roomCauseIn(q, r.Conv, r.ID, self, fp)
	if err != nil {
		return roomCauseFailure(err)
	}
	seen := map[string]bool{}
	allowed := true
	for {
		if seen[c.id] {
			return verdictStop, "agent request origin contains a loop", nil
		}
		seen[c.id] = true
		target, e := participationIn(q, r.Conv, c.pid, m, self)
		if e != nil {
			return verdictWait, "origin target evidence is pending", nil
		}
		if !target.Claimable() || c.target == nil || target.Host.Address != c.target.Address || target.Host.Fingerprint != c.target.Fingerprint || target.AgentID != c.target.AgentID {
			return verdictStop, "an upstream request target ended or changed", nil
		}
		if target.Host.Address == self && c.state != stateRunning && c.id != r.ID {
			return verdictStop, "originating local run stopped", nil
		}
		if c.kind != envelope.KindQuestion && c.kind != envelope.KindTask || c.target == nil {
			return verdictStop, "agent cause is not an addressed request", nil
		}
		if r.Kind == envelope.KindTask && c.kind != envelope.KindTask {
			return verdictStop, "a question cannot delegate permission to change things", nil
		}
		agent := c.human != nil && c.human.AgentAuthor()
		if agent {
			p, e := participationIn(q, r.Conv, c.human.AuthorPID, m, self)
			if e != nil {
				return verdictWait, "asking agent's membership evidence is pending", nil
			}
			if !p.Claimable() || p.Host.Address != c.from || p.Host.Fingerprint != c.key || p.Audience != protocol.AudienceRoom || p.PID == c.pid {
				return verdictStop, "asking agent membership ended or does not match its exact host", nil
			}
			cause, e := roomCauseIn(q, r.Conv, c.reply, self, fp)
			if errors.Is(e, sql.ErrNoRows) {
				return verdictWait, "upstream request evidence is pending", nil
			}
			if e != nil {
				return roomCauseFailure(e)
			}
			if cause.pid != p.PID || cause.target == nil || cause.target.Address != p.Host.Address || cause.target.Fingerprint != p.Host.Fingerprint || cause.target.AgentID != p.AgentID {
				return verdictStop, "agent ask is not caused by a request to that exact agent", nil
			}
			c = cause
			continue
		}
		if !m.device(c.from, c.key) || !m.requestEpoch(c.from, c.key, c.target) {
			return verdictStop, "original requester's group admission changed", nil
		}
		break
	}
	// Both immediate agent and every upstream human/agent key must hold their
	// own grant. Traverse again without accepting absent proof.
	c, err = roomCauseIn(q, r.Conv, r.ID, self, fp)
	if err != nil {
		return roomCauseFailure(err)
	}
	for {
		own := false
		for _, p := range m.persons {
			own = own || p.info.Person == info.Host.Person && p.has(c.from, c.key)
		}
		if !own {
			if r.Kind == envelope.KindQuestion {
				// The device's or its person's grant (P7), never while its key change waits.
				var pending int
				if err = q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, c.from).Scan(&pending); err != nil {
					return 0, "", err
				}
				ok, e := questionApproved(q, c.from, c.key)
				if e != nil {
					return 0, "", e
				}
				allowed = allowed && ok && pending == 0
			} else {
				ok, e := taskGranted(q, c.from, c.key)
				if e != nil {
					return 0, "", e
				}
				allowed = allowed && (ok || slices.Contains(info.TaskKeys, c.key))
			}
		}
		if c.human == nil || !c.human.AgentAuthor() {
			break
		}
		c, err = roomCauseIn(q, r.Conv, c.reply, self, fp)
		if err != nil {
			return roomCauseFailure(err)
		}
	}
	if output || r.State == stateAccepted || allowed {
		return verdictRun, "", nil
	}
	return verdictAsk, "Someone in this request chain needs your OK before your agent can run it", nil
}

func roomCauseFailure(err error) (int, string, error) {
	if errors.Is(err, errAmbiguousRoomCause) {
		return verdictStop, errAmbiguousRoomCause.Error(), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return verdictWait, "request origin evidence is pending", nil
	}
	return 0, "", err
}

// SendRoomAsk originates only from an exact currently running group job.
func (a *Agent) SendRoomAsk(ctx context.Context, cause, pid, kind, body string) (ConvSent, error) {
	var conv, source, state string
	err := a.store.db.QueryRow(`SELECT conv,pid,state FROM inbox WHERE id=?`, cause).Scan(&conv, &source, &state)
	if err != nil {
		return ConvSent{}, err
	}
	if state != stateRunning {
		return ConvSent{}, errors.New("room ask requires a running request")
	}
	from, err := a.Participation(source)
	if err != nil {
		return ConvSent{}, err
	}
	to, err := a.Participation(pid)
	if err != nil {
		return ConvSent{}, err
	}
	m, err := a.dmMembers(conv)
	if err != nil {
		return ConvSent{}, err
	}
	if m.group == nil || !from.Member || !from.HostHere || !from.Claimable() || !to.Claimable() || to.Conv != conv || to.PID == source {
		return ConvSent{}, errors.New("choose another active agent in this group")
	}
	root, err := roomCauseIn(a.store.db, conv, cause, a.Address, a.Self().Fingerprint())
	if err != nil {
		return ConvSent{}, err
	}
	if kind != envelope.KindQuestion && kind != envelope.KindTask {
		return ConvSent{}, errors.New("choose question or task")
	}
	if kind == envelope.KindTask && root.kind != envelope.KindTask {
		return ConvSent{}, errors.New("a question cannot assign a task")
	}
	h, err := a.roomAudience(conv, source)
	if err != nil {
		return ConvSent{}, err
	}
	return a.SendConv(ctx, conv, ConvOutgoing{Kind: kind, PID: pid, Body: body, ReplyTo: root.id, Origin: envelope.OriginAgentPrefix + "room", Emotion: "neutral", Target: &envelope.Target{Address: to.Host.Address, Fingerprint: to.Host.Fingerprint, AgentID: to.AgentID}, human: h, selfJob: to.HostHere, claim: func(tx *sql.Tx, _ string) error {
		var current string
		if e := tx.QueryRow(`SELECT state FROM inbox WHERE id=?`, cause).Scan(&current); e != nil {
			return e
		}
		if current != stateRunning {
			return errors.New("originating run stopped")
		}
		return nil
	}})
}

// RoomReply is an explicit read of the permitted group's correlated reply.
// CLI waiting reads local storage only; the daemon keeps its one push stream.
func (a *Agent) RoomReply(cause, request string) (*ConvMessage, error) {
	var conv, pid, state string
	if err := a.store.db.QueryRow(`SELECT conv,pid,state FROM inbox WHERE id=?`, cause).Scan(&conv, &pid, &state); err != nil {
		return nil, err
	}
	if state != stateRunning {
		return nil, errors.New("originating run stopped")
	}
	p, err := a.Participation(pid)
	if err != nil {
		return nil, err
	}
	if !p.Member || !p.HostHere || !p.Claimable() {
		return nil, errors.New("agent is no longer a member here")
	}
	req, err := roomCauseIn(a.store.db, conv, request, a.Address, a.Self().Fingerprint())
	if err != nil {
		return nil, err
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		return nil, err
	}
	permitted := func(m ConvMessage) (bool, error) {
		if m.Human != nil && m.Human.AuthorPID == p.PID && m.From == p.Host.Address && m.Key == p.Host.Fingerprint {
			return true, nil
		}
		var n int
		e := a.store.db.QueryRow(`SELECT count(*) FROM room_context WHERE conv=? AND pid=? AND lid=? AND fingerprint=?`, conv, p.PID, m.LID, m.Key).Scan(&n)
		return n > 0, e
	}
	for _, m := range msgs {
		if m.ReplyTo != req.id || m.PID != req.pid || req.target == nil || m.From != req.target.Address || m.Key != req.target.Fingerprint || (m.Kind != envelope.KindAnswer && m.Kind != envelope.KindResult) || !m.VerifiedAgent {
			continue
		}
		ok, e := permitted(m)
		if e != nil {
			return nil, e
		}
		if ok {
			return &m, nil
		}
	}
	for _, m := range msgs {
		if m.LID != req.id || m.Exec == nil {
			continue
		}
		ok, e := permitted(m)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		switch m.Exec.State {
		case stateDeclined, stateCancelled, stateFailed, "interrupted", stateNotRun, "needs_human":
			return nil, fmt.Errorf("request %s from agent participation %s: %s: %s", req.id, req.pid, m.Exec.State, m.Exec.Detail)
		}
	}
	target, err := a.Participation(req.pid)
	if err != nil {
		return nil, err
	}
	if target.Held > 0 {
		return nil, nil
	}
	if !target.Claimable() {
		return nil, errors.New("requested agent membership ended")
	}
	return nil, nil
}

func roomPromptName(relation string, p PersonInfo) string {
	return fmt.Sprintf("%s (claimed name %q; verified device %s)", relation, p.Label, p.Address)
}

func jobKeyOrSelf(q dbq, address string) string {
	p, ok, err := scanPersonIn(q, "person IN (SELECT person FROM person_devices WHERE address = ?)", address)
	if err != nil || !ok {
		return ""
	}
	return p.at(address).info.Fingerprint
}

// Counted consent records survive an inviter leaving the human membership.
// This is not current member authority; its exact pinned author key remains required.
func (m dmMembers) roomAuthor(au protocol.EventAuthor) (personRow, bool) {
	p, ok := m.roomAuthors[au.Person]
	return p.at(au.Address), ok && p.has(au.Address, au.Fingerprint)
}
