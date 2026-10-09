package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Status authority remains the exact executor's claim about one immutable
// addressed request. It grants no human control or ordinary room authority.
func (a *Agent) groupStatusScope(q dbq, ref ControlRef, host, hostFP string) (ParticipationInfo, error) {
	return a.groupStatusAuthority(q, ref, host, hostFP, false)
}

func (a *Agent) groupStatusAuthority(q dbq, ref ControlRef, host, hostFP string, historical bool, context ...dmMembers) (ParticipationInfo, error) {
	if len(context) > 0 && !historical {
		return ParticipationInfo{}, errors.New("group: historical context cannot authorize live status")
	}
	rows, err := q.Query(`SELECT pid,kind,coalesce(target,''),sender,coalesce(verified_by,claimed_fp,''),coalesce(human,'') FROM inbox WHERE conv=? AND lid=? AND ref_id IS NULL
 UNION ALL SELECT pid,kind,coalesce(target,''),?,?,coalesce(human,'') FROM outbox WHERE conv=? AND lid=? AND ref_id IS NULL`, ref.Conv, ref.ID, a.Address, a.Self().Fingerprint(), ref.Conv, ref.ID)
	if err != nil {
		return ParticipationInfo{}, err
	}
	var pid, kind, raw, from, fp, human string
	found := false
	for rows.Next() {
		var p, k, t, s, f, h string
		if err = rows.Scan(&p, &k, &t, &s, &f, &h); err != nil {
			break
		}
		if f != ref.Fingerprint {
			continue
		}
		if p == "" || k != envelope.KindQuestion && k != envelope.KindTask || found && (p != pid || k != kind || t != raw || f != fp) {
			err = errors.New("group: status original request conflicting")
			break
		}
		pid, kind, raw, from, fp, human, found = p, k, t, s, f, h, true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return ParticipationInfo{}, err
	}
	if !found {
		return ParticipationInfo{}, ErrGroupContextPending
	}
	var target envelope.Target
	if json.Unmarshal([]byte(raw), &target) != nil {
		return ParticipationInfo{}, errors.New("group: status request target malformed")
	}
	m, err := humanMembers(q, ref.Conv, context)
	if err != nil {
		return ParticipationInfo{}, err
	}
	if m.group == nil {
		return ParticipationInfo{}, errors.New("group: status scope is not a group")
	}
	info, err := participationIn(q, ref.Conv, pid, m, a.Address)
	if err != nil {
		return info, err
	}
	if historical && info.Invite == "" {
		return info, ErrGroupContextPending
	}
	var captured *envelope.HumanTurn
	if human != "" {
		if err = json.Unmarshal([]byte(human), &captured); err != nil {
			return info, err
		}
	}
	agent := captured != nil && captured.AgentAuthor()
	authorized := info.Claimable()
	if !authorized && historical {
		authorized, err = retainedAssistant(q, info, m.historyEvents)
		if err != nil {
			return info, err
		}
	}
	if !authorized || info.Host.Address != host || info.Host.Fingerprint != hostFP || target.Address != host || target.Fingerprint != hostFP || target.AgentID != info.AgentID || !agent && !m.requestEpoch(from, fp, &target) {
		return info, errors.New("group: status does not match current exact request/PID/host/requester epoch")
	}
	output := envelope.Inner{Conv: ref.Conv, PID: pid, Kind: replyKind(kind), ReplyTo: ref.ID, AgentID: info.AgentID}
	_, err = externalOutputRequestMode(q, output, info, m, a.Address, a.Self().Fingerprint(), historical)
	return info, err
}
func (a *Agent) admitGroupParticipationStatus(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	packet, err := groupTurnPacketIn(a.store.db, in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return err
		}
	}
	ref := ControlRef{Conv: in.Conv, ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint}
	check := func(q dbq) error {
		if e := statusSenderCheck(q, sender); e != nil {
			return e
		}
		if e := statusRecoveryCheck(q, env.From, env.ID); e != nil {
			return e
		}
		if _, e := a.groupStatusScope(q, ref, sender.Address, sender.Fingerprint()); e != nil {
			return e
		}
		current, e := groupTurnPacketIn(q, in.Conv)
		if e != nil {
			return e
		}
		return groupTurnCheck(q, current, a.Address, a.Self().Fingerprint())
	}
	if err = check(a.store.db); err != nil {
		if errors.Is(err, errStatusRecoveryAuthority) {
			return hold(reasonKeyChanged, err.Error())
		}
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	_, err = a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if e := check(tx); e != nil {
			return e
		}
		packet, e := groupTurnPacketIn(tx, in.Conv)
		if e != nil {
			return e
		}
		admission, e := groupMemberAdmission(tx, packet, a.Address, a.Self().Fingerprint())
		if e != nil {
			return e
		}
		_, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, admission.Hash(), in.ID)
		return e
	})
	return err
}
func (a *Agent) mayDeliverGroupStatus(env envelope.Envelope) (bool, bool, error) {
	var conv, sub, state, required, fp, epoch, refID, refFP string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(sub,''),state,coalesce(required_cap,''),coalesce(recipient_fp,''),coalesce(group_admission,''),coalesce(ref_id,''),coalesce(ref_fp,'') FROM outbox WHERE id=?`, env.ID).Scan(&conv, &sub, &state, &required, &fp, &epoch, &refID, &refFP)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if conv == "" || sub != envelope.SubStatus {
		return false, false, nil
	}
	root, _, found, err := a.store.conversation(conv)
	if err != nil {
		return true, false, err
	}
	if !found || root.Kind != protocol.ConvKindGroup {
		return false, false, nil
	}
	if state != stateQueued || required != protocol.CapGroup || fp == "" || epoch == "" {
		return true, false, nil
	}
	_, err = a.groupStatusScope(a.store.db, ControlRef{Conv: conv, ID: refID, Fingerprint: refFP}, a.Address, a.Self().Fingerprint())
	if err == nil {
		packet, e := groupTurnPacketIn(a.store.db, conv)
		err = e
		if err == nil {
			admission, e := groupMemberAdmission(a.store.db, packet, env.To, fp)
			err = e
			if err == nil && admission.Hash() != epoch {
				err = errors.New("group: status recipient admission changed")
			}
		}
	}
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: group status request or recipient authority changed", "")
	}
	return true, true, nil
}
