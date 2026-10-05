package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"slices"
)

func (m dmMembers) authorEpoch(au protocol.EventAuthor) bool {
	if m.group == nil {
		return au.GroupAdmission == ""
	}
	member, ok := m.group.State.Member(au.Person)
	return ok && au.GroupAdmission == member.Admission.Hash()
}
func (m dmMembers) inviteEpoch(ev protocol.ParticipationEvent) bool {
	if m.group == nil {
		return ev.Group == nil
	}
	return m.groupInvites[ev.Hash()]
}
func (m dmMembers) keyEpoch(fp string) string {
	if m.group == nil {
		return ""
	}
	for person, p := range m.persons {
		if _, ok := p.roster.Device(fp); ok {
			member, ok := m.group.State.Member(person)
			if ok {
				return member.Admission.Hash()
			}
		}
	}
	return ""
}

// Scope is bound to original public proof, never a label or today's inferred host role.
func (m dmMembers) verifyInviteEpoch(q dbq, ev protocol.ParticipationEvent) (bool, error) {
	if ev.Group == nil || ev.Host == nil || !m.authorEpoch(ev.Author) && !m.roomEvents[ev.Hash()] {
		return false, nil
	}
	scope := ev.Group
	c, err := groupProofRecord(q, ev.Conv, m.root.Creator.Fingerprint, scope.Seq)
	if errors.Is(err, ErrGroupContextPending) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if c.Hash != scope.Hash || scope.Seq > m.group.State.Seq {
		return false, nil
	}
	if scope.HostRole == "visitor" && (ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope) && !slices.Contains(c.Admins, ev.Author.Person) {
		return false, nil
	}
	_, member := m.persons[ev.Host.Person]
	if scope.HostRole == "member" {
		if !member || m.keyEpoch(ev.Host.Fingerprint) != scope.HostAdmission {
			return false, nil
		}
	} else if scope.HostRole != "visitor" || member || scope.HostAdmission != "" {
		return false, nil
	}
	if len(scope.TaskAdmissions) != len(ev.TaskKeys) {
		return false, nil
	}
	for i, fp := range ev.TaskKeys {
		if current := m.keyEpoch(fp); !m.roomEvents[ev.Hash()] && (current == "" || current != scope.TaskAdmissions[i]) {
			return false, nil
		}
	}
	return true, nil
}
func (m dmMembers) bindGroupInvite(ev *protocol.ParticipationEvent) error {
	member, ok := m.group.State.Member(ev.Author.Person)
	if !ok || !m.device(ev.Author.Address, ev.Author.Fingerprint) {
		return errors.New("group: inviter is not a current member")
	}
	ev.Author.GroupAdmission = member.Admission.Hash()
	scope := &protocol.ParticipationGroup{Seq: m.group.State.Seq, Hash: m.group.State.Hash(), HostRole: "visitor"}
	if _, ok := m.persons[ev.Host.Person]; ok {
		scope.HostRole = "member"
		scope.HostAdmission = m.keyEpoch(ev.Host.Fingerprint)
		if scope.HostAdmission == "" {
			return ErrGroupContextPending
		}
	} else if ev.Type == protocol.EventInvite && !member.Admin {
		return errors.New("only a group administrator can add an agent whose owner is outside this group")
	}
	for _, fp := range ev.TaskKeys {
		epoch := m.keyEpoch(fp)
		if epoch == "" {
			return errors.New("group: task key has no current admission")
		}
		scope.TaskAdmissions = append(scope.TaskAdmissions, epoch)
	}
	ev.Group = scope
	return nil
}

func (m dmMembers) mayRemoveAgent(info ParticipationInfo, author protocol.EventAuthor) bool {
	if m.group == nil || !info.Member || !info.External {
		return true
	}
	if author.Person == info.Host.Person || author.Person == info.Inviter.Person {
		return true
	}
	member, ok := m.group.State.Member(author.Person)
	return ok && member.Admin && m.authorEpoch(author)
}
func (m dmMembers) requestEpoch(sender, fp string, t *envelope.Target) bool {
	if m.group == nil {
		return t == nil || t.GroupAdmission == ""
	}
	return t != nil && m.device(sender, fp) && t.GroupAdmission != "" && t.GroupAdmission == m.keyEpoch(fp)
}

// PID rows retain signed source/request scope and a local exact destination
// admission fence. Ordinary/history/file rows keep their existing interpretation.
func (a *Agent) mayDeliverGroupParticipation(env envelope.Envelope) (bool, bool, error) {
	var in envelope.Inner
	var state, required, fp, epoch, target, human string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(pid,''),kind,body,coalesce(sub,''),coalesce(origin,''),coalesce(reply_to,''),coalesce(agent_id,''),coalesce(target,''),state,coalesce(required_cap,''),coalesce(recipient_fp,''),coalesce(group_admission,''),coalesce(status,''),coalesce(human,'') FROM outbox WHERE id=?`, env.ID).Scan(&in.Conv, &in.PID, &in.Kind, &in.Body, &in.Sub, &in.Origin, &in.ReplyTo, &in.AgentID, &target, &state, &required, &fp, &epoch, &in.Status, &human)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if in.PID == "" || in.Sub == envelope.SubGroupProof || in.Sub == envelope.SubGroupContext {
		return false, false, nil
	}
	root, _, found, err := a.store.conversation(in.Conv)
	if err != nil {
		return true, false, err
	}
	if !found || root.Kind != protocol.ConvKindGroup {
		return false, false, nil
	}
	if state != stateQueued || required != protocol.CapGroup || fp == "" {
		return true, false, nil
	}
	if target != "" {
		if err = json.Unmarshal([]byte(target), &in.Target); err != nil {
			return true, false, err
		}
	}
	if human != "" {
		if err = json.Unmarshal([]byte(human), &in.Human); err != nil {
			return true, false, err
		}
	}
	in.Replica = in.Sub == envelope.SubExcerpt
	m, err := membersIn(a.store.db, in.Conv)
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	info, err := participationIn(a.store.db, in.Conv, in.PID, m, a.Address)
	if errors.Is(err, ErrNoParticipation) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	allowed := m.device(env.To, fp) && epoch != "" && epoch == m.keyEpoch(fp) || info.External && env.To == info.Host.Address && fp == info.Host.Fingerprint && epoch == ""
	if in.Human != nil {
		err = humanTurnAuthorization(a.store.db, in, a.Address, a.Self().Fingerprint(), env.To, fp, false)
		allowed = err == nil && (!m.device(env.To, fp) || epoch != "" && epoch == m.keyEpoch(fp))
	}
	if allowed && in.Human != nil && in.Human.AgentAuthor() && in.Target != nil {
		var originState string
		err = a.store.db.QueryRow(`SELECT state FROM inbox WHERE conv=? AND (id=? OR lid=?)`, in.Conv, in.ReplyTo, in.ReplyTo).Scan(&originState)
		allowed = err == nil && originState == stateRunning
	}
	if allowed {
		err = externalTurn(in, info, m, a.Address, a.Self().Fingerprint())
		allowed = err == nil
	}
	if allowed {
		_, err = externalOutputRequest(a.store.db, in, info, m, a.Address, a.Self().Fingerprint())
		allowed = err == nil
	}
	if !allowed {
		if e := a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: group PID authority or original recipient admission changed", ""); e != nil {
			return true, false, e
		}
		a.releaseSpool(env)
	}
	return true, allowed, nil
}
