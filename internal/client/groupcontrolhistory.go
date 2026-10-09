package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
	a.SendGroup, b.SendGroup = "", ""
	a.GroupAdmission, b.GroupAdmission = "", ""
	a.At, b.At = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (a *Agent) groupControlSourceAdmission(q dbq, packet GroupContext, item HistoryItem, proof *groupControlIngressProof) (string, error) {
	return a.groupControlSourceAdmissionFor(q, packet, item, proof, false)
}

func (a *Agent) groupControlSourceAdmissionFor(q dbq, packet GroupContext, item HistoryItem, proof *groupControlIngressProof, currentRecipientOnly bool) (string, error) {
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
	if orig := item.inner(packet.State.Conv); envelope.AssistantReaction(orig) && (item.From != a.Address || item.FromKey != a.Self().Fingerprint()) {
		// An assistant's host need not be a member: its participation binds
		// it, as when it arrived. Only a proven mismatch leaves it out; a
		// missing proof waits, as the page does for any other context.
		switch reason, e := assistantHistoryCheck(q, orig, item.From, item.FromKey, a.Address, a.Self().Fingerprint()); {
		case e == nil:
		case reason == reasonInvalid:
			return "", errGroupControlHistoryEpoch
		case reason == reasonProof:
			return "", fmt.Errorf("%w: %v", ErrGroupContextPending, e)
		default:
			return "", e
		}
	} else {
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
		if !currentRecipientOnly && (e == nil && current != fence || errors.Is(e, errGroupRecipientWithdrawn) || errors.Is(e, errGroupRecipientNotCurrent)) {
			if e = groupControlHistoricalRecipient(q, packet, own, env.To, toFP, fence); e == nil {
				return own.Hash(), nil
			}
		}
		if e != nil {
			if errors.Is(e, errGroupRecipientWithdrawn) || errors.Is(e, errGroupRecipientNotCurrent) {
				return "", errGroupControlHistoryEpoch
			}
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

// A departed reader's signed admission can identify an original local control;
// it never grants that reader delivery rights. The current author's admission
// must still reproduce the exact original fence. Only the already verified
// signed context and an exact pinned roster key supply recipient provenance.
// A later state that drops the original admission remains unavailable here;
// this fallback does not search old states or authorize a new delivery.
func groupControlHistoricalRecipient(q dbq, packet GroupContext, own protocol.GroupAdmission, address, fp, fence string) error {
	record, err := groupProofRecord(q, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		return err
	}
	if !record.Matches(packet.State) {
		return errGroupControlHistoryEpoch
	}
	for _, member := range packet.State.Members {
		person, ok, err := personByIDIn(q, member.Person)
		if err != nil {
			return err
		}
		if !ok || person.info.State != personSelf && person.info.State != personPinned || !person.has(address, fp) {
			continue
		}
		if groupControlAdmissionFence(own, member.Admission) == fence {
			return nil
		}
	}
	return errGroupControlHistoryEpoch
}

// The first encrypted copy of a logical control may have gone to a reader
// who has since left. Prefer another exact original signed copy with a current
// recipient, then try the original's retained signed admission for own sync.
func (a *Agent) groupControlHistorySource(q dbq, packet GroupContext, item HistoryItem) (HistoryItem, error) {
	if _, err := a.groupControlSourceAdmissionFor(q, packet, item, nil, true); !errors.Is(err, errGroupControlHistoryEpoch) {
		return item, err
	}
	if item.From != a.Address || item.FromKey != a.Self().Fingerprint() {
		return HistoryItem{}, errGroupControlHistoryEpoch
	}
	rows, err := q.Query(`SELECT id,json_extract(envelope,'$.ts') FROM outbox WHERE conv=? AND lid=? AND kind=? AND sub=? AND body=? AND ref_id=? AND ref_fp=? AND id<>? ORDER BY rowid`, packet.State.Conv, item.LID, item.Kind, item.Sub, item.Body, item.Ref.ID, item.Ref.Fingerprint, item.ID)
	if err != nil {
		return HistoryItem{}, err
	}
	var candidates []HistoryItem
	for rows.Next() {
		candidate := item
		if err = rows.Scan(&candidate.ID, &candidate.TS); err != nil {
			break
		}
		candidates = append(candidates, candidate)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return HistoryItem{}, err
	}
	for _, candidate := range candidates {
		if _, err = a.groupControlSourceAdmissionFor(q, packet, candidate, nil, true); err == nil {
			return candidate, nil
		}
		if !errors.Is(err, errGroupControlHistoryEpoch) {
			return HistoryItem{}, err
		}
	}
	if _, err = a.groupControlSourceAdmission(q, packet, item, nil); err != nil {
		return HistoryItem{}, err
	}
	return item, nil
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
	if envelope.AssistantReaction(original) { // its host need not be a member: bound to its participation instead
		reason, e := assistantHistoryCheck(q, original, item.From, item.FromKey, a.Address, a.Self().Fingerprint())
		if reason == reasonProof {
			return fmt.Errorf("%w: %v", ErrGroupContextPending, e)
		}
		if e != nil {
			return e
		}
		return a.groupControlTarget(q, original)
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
