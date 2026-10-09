package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// capturedHumanEdit retains only the original turn's still-following scopes.
// It never obtains a fresh room audience or adds a later guest.
func (a *Agent) capturedHumanEdit(q dbq, ref ControlRef) (*envelope.HumanTurn, bool, error) {
	if ref.Conv == "" || ref.Fingerprint != a.Self().Fingerprint() {
		return nil, false, nil
	}
	root, _, found, e := conversationIn(q, ref.Conv)
	if e != nil || !found || root.Kind != protocol.ConvKindGroup {
		return nil, false, e
	}
	where, args := outScope(ref, a.Self().Fingerprint())
	var raw, origin string
	e = q.QueryRow(`SELECT coalesce(human,''),coalesce(origin,'') FROM outbox o WHERE `+where+` AND ref_id IS NULL AND coalesce(sub,'')='' LIMIT 1`, args...).Scan(&raw, &origin)
	if errors.Is(e, sql.ErrNoRows) || e == nil && raw == "" {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	var original envelope.HumanTurn
	if e = json.Unmarshal([]byte(raw), &original); e != nil {
		return nil, true, e
	}
	if !humanGuestScope(&original) {
		return nil, false, nil
	}
	if original.AgentAuthor() || envelope.AgentOrigin(origin) {
		return nil, true, errors.New("an assistant's captured turn cannot be rewritten")
	}
	m, e := membersIn(q, ref.Conv)
	if e != nil {
		return nil, true, e
	}
	h := &envelope.HumanTurn{AuthorPID: original.AuthorPID, Audience: []envelope.HumanScope{}, Proof: []protocol.ParticipationEvent{}}
	kept := map[string]bool{}
	for _, scope := range original.Audience {
		p, e := participationIn(q, ref.Conv, scope.PID, m, a.Address)
		if e != nil {
			return nil, true, e
		}
		if !p.Following() || p.Invite != scope.Invite || p.Decision != scope.Decision {
			if scope.PID == original.AuthorPID {
				return nil, true, errors.New("captured guest author is no longer active")
			}
			continue
		}
		h.Audience = append(h.Audience, scope)
		kept[scope.PID] = true
	}
	for _, event := range original.Proof {
		if kept[event.PID] {
			h.Proof = append(h.Proof, event)
		}
	}
	if len(h.Audience) == 0 {
		if !m.device(a.Address, a.Self().Fingerprint()) {
			return nil, true, errors.New("captured member author is no longer current")
		}
		return nil, true, nil
	} // member-only control now
	if e = h.Validate(ref.Conv); e != nil {
		return nil, true, e
	}
	if e = topicAudienceAuthorization(q, envelope.Inner{Conv: ref.Conv, Human: h, Ref: &envelope.Ref{ID: ref.ID, Fingerprint: ref.Fingerprint}}, m, a.Address); e != nil {
		return nil, true, e
	}
	if e = humanAuthorization(q, ref.Conv, h, a.Address, a.Self().Fingerprint(), a.Address, a.Self().Fingerprint()); e != nil {
		return nil, true, e
	}
	return h, true, nil
}

// The existing HumanEdit wire contract is shared by native and browser readers.
func (a *Agent) mayDeliverCapturedHumanEdit(env envelope.Envelope) (bool, bool, error) {
	var conv, sub, state, fp, raw, refID, refFP string
	e := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(sub,''),state,coalesce(recipient_fp,''),coalesce(human,''),coalesce(ref_id,''),coalesce(ref_fp,'') FROM outbox WHERE id=?`, env.ID).Scan(&conv, &sub, &state, &fp, &raw, &refID, &refFP)
	if errors.Is(e, sql.ErrNoRows) {
		return false, false, nil
	}
	if e != nil {
		return true, false, e
	}
	if raw == "" || conv == "" || sub != envelope.SubRevision && sub != envelope.SubRetraction {
		return false, false, nil
	}
	var h envelope.HumanTurn
	if e = json.Unmarshal([]byte(raw), &h); e != nil {
		return true, false, e
	}
	key, pending, ok, e := a.store.peer(env.To)
	if e != nil {
		return true, false, e
	}
	if !ok || pending != nil || key.Fingerprint() != fp {
		return true, false, nil
	}
	return a.mayDeliverHuman(env, envelope.Inner{V: envelope.Version3, Conv: conv, Sub: sub, Human: &h, Ref: &envelope.Ref{ID: refID, Fingerprint: refFP}}, state, fp)
}

func (a *Agent) capturedHumanControlAuthor(conv string, c controlRow, target ControlRef, me personRow) (string, string, bool, bool) {
	if c.human == "" || c.authorFP != target.Fingerprint || c.sub != envelope.SubRevision && c.sub != envelope.SubRetraction {
		return "", "", false, false
	}
	var h envelope.HumanTurn
	if json.Unmarshal([]byte(c.human), &h) != nil || h.AuthorPID == "" || h.Validate(conv) != nil {
		return "", "", false, false
	}
	for _, e := range h.Proof {
		if e.Type == protocol.EventScope && e.PID == h.AuthorPID && e.Role == protocol.RoleHuman && e.Host != nil && e.Host.Address == c.author && e.Host.Fingerprint == c.authorFP {
			label := e.Host.Address
			if p, err := a.Participation(e.PID); err == nil && p.Host.Label != "" {
				label = p.Host.Label
			}
			return e.Host.Person, label, e.Host.Person == me.info.Person, true
		}
	}
	return "", "", false, false
}

func humanGuestScope(h *envelope.HumanTurn) bool {
	if h == nil {
		return false
	}
	for _, e := range h.Proof {
		if e.Role == protocol.RoleHuman {
			return true
		}
	}
	return false
}
