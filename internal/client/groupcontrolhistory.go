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

// Constructed ONLY by Open-verified/current-authorized direct group ingress;
// the caller rechecks the original and every copy in its original inbox tx.
type groupControlIngressProof struct {
	in        envelope.Inner
	key       string
	admission string
}

var errGroupControlHistoryEpoch = errors.New("group: historical control original admission changed")

func sameControlItem(a, b HistoryItem) bool {
	a.GroupAdmission, b.GroupAdmission = "", ""
	a.At, b.At = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (a *Agent) groupControlSourceAdmission(q dbq, packet GroupContext, item HistoryItem, proof *groupControlIngressProof) (string, error) {
	if !groupControlSub(item.Sub) || item.Ref == nil || item.TS <= 0 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) {
		return "", errors.New("group: malformed historical control identity")
	}
	if err := envelope.ValidateControl(item.inner(packet.State.Conv)); err != nil {
		return "", err
	}
	own, err := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return "", err
	}
	author, present, err := scanPersonIn(q, "person IN (SELECT person FROM person_devices WHERE address=?)", item.From)
	if err != nil {
		return "", err
	}
	if !present {
		return "", ErrGroupContextPending
	}
	if author.info.State == personConflict {
		return "", errPersonConflict
	}
	member, memberPresent := packet.State.Member(author.info.Person)
	var withdrawn int
	if memberPresent {
		if err = q.QueryRow(`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`, packet.State.Conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); err != nil {
			return "", err
		}
	}
	if !memberPresent || withdrawn != 0 || packet.State.Withdrawn(member, packet.Withdrawals) || !author.has(item.From, item.FromKey) {
		return "", errGroupControlHistoryEpoch // definite current-author exclusion, never a transient failure
	}
	if err = groupTurnCheck(q, packet, item.From, item.FromKey); err != nil {
		return "", err
	}
	if proof != nil {
		if proof.admission != own.Hash() || !sameControlItem(item, itemOf(proof.in, proof.key, item.At)) {
			return "", errors.New("group: direct control provenance differs")
		}
		return own.Hash(), nil
	}
	if item.From == a.Address && item.FromKey == a.Self().Fingerprint() {
		var raw, body, sub, kind, toFP, fence, refID, refFP string
		err = q.QueryRow(`SELECT envelope,body,sub,kind,coalesce(recipient_fp,''),coalesce(group_admission,''),ref_id,ref_fp FROM outbox WHERE id=? AND conv=? AND lid=?`, item.ID, packet.State.Conv, item.LID).Scan(&raw, &body, &sub, &kind, &toFP, &fence, &refID, &refFP)
		if err != nil {
			return "", err
		}
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) != nil || env.V != envelope.Version3 || env.ID != item.ID || env.From != item.From || env.TS != item.TS || env.Kind != item.Kind || env.VerifySig(a.Self().SignKey) != nil || body != item.Body || sub != item.Sub || kind != item.Kind || refID != item.Ref.ID || refFP != item.Ref.Fingerprint {
			return "", errors.New("group: historical control differs from original outbox")
		}
		current, e := groupControlEpochFence(q, packet, a.Address, a.Self().Fingerprint(), env.To, toFP)
		if e != nil {
			return "", e
		}
		if current != fence {
			return "", errGroupControlHistoryEpoch
		}
		return own.Hash(), nil
	}
	var from, fp, body, sub, kind, stamp, refID, refFP string
	var ts int64
	err = q.QueryRow(`SELECT sender,coalesce(verified_by,claimed_fp,''),ts,body,sub,kind,coalesce(group_admission,''),ref_id,ref_fp FROM inbox WHERE id=? AND conv=? AND lid=?`, item.ID, packet.State.Conv, item.LID).Scan(&from, &fp, &ts, &body, &sub, &kind, &stamp, &refID, &refFP)
	if err != nil {
		return "", err
	}
	if from != item.From || fp != item.FromKey || ts != item.TS || body != item.Body || sub != item.Sub || kind != item.Kind || stamp == "" || refID != item.Ref.ID || refFP != item.Ref.Fingerprint {
		return "", errors.New("group: historical control lacks exact original live admission")
	}
	if stamp != own.Hash() {
		return "", errGroupControlHistoryEpoch
	}
	return stamp, nil
}

func (a *Agent) groupControlHistoryCheck(q dbq, root protocol.ConvRoot, forwarder identity.Public, item HistoryItem) error {
	packet, err := groupTurnPacketIn(q, root.ID())
	if err != nil {
		return err
	}
	if !sameGroupRoot(root, packet.Root) {
		return errors.New("group: historical control root differs")
	}
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || !me.has(forwarder.Address, forwarder.Fingerprint()) {
		return errors.New("group: controls sync only from current own linked device")
	}
	if err = groupTurnCheck(q, packet, forwarder.Address, forwarder.Fingerprint()); err != nil {
		return err
	}
	own, err := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	if item.GroupAdmission == "" || item.GroupAdmission != own.Hash() {
		return errors.New("group: historical control own admission changed")
	}
	if !groupControlSub(item.Sub) || item.Ref == nil || item.TS <= 0 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) {
		return errors.New("group: malformed historical control")
	}
	original := item.inner(root.ID())
	if err = envelope.ValidateControl(original); err != nil {
		return err
	}
	if err = groupTurnCheck(q, packet, item.From, item.FromKey); err != nil {
		return err
	}
	m, err := controlMembers(q, root.ID())
	if err != nil {
		return err
	}
	var author string
	for id, p := range m.persons {
		if p.has(item.From, item.FromKey) {
			author = id
		}
	}
	if author == "" {
		return errors.New("group: historical control author absent")
	}
	if reason, why := a.controlAuthorized(m, original, author); reason != "" {
		if reason == reasonProof {
			return ErrGroupContextPending
		}
		return errors.New(why)
	}
	return a.groupControlTarget(q, original)
}

func (a *Agent) admitGroupControlHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, item HistoryItem, fromQuarantine bool, hold func(string, string) error) error {
	if !in.Replica {
		return hold(reasonInvalid, "group: historical control is not an own replica")
	}
	packet, err := groupTurnPacketIn(a.store.db, in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return err
		}
	}
	check := func(q dbq) error { return a.groupControlHistoryCheck(q, root, sender, item) }
	if err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	result, err := a.store.addHistoryInbox(item.inner(in.Conv), item.At, item.FromKey, env.From, env.ID, fromQuarantine, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, item.GroupAdmission, item.ID)
		return e
	}, func(tx *sql.Tx) error { return check(tx) })
	if err != nil {
		return err
	}
	if result == admitted {
		a.applyRetraction(item.inner(in.Conv))
		a.convWork.due(convRetry)
		a.kickNow()
	}
	return nil
}
