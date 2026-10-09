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

var errGroupParticipationHistoryEpoch = errors.New("group: historical participation original admission changed")

func groupParticipationHistoryItem(item HistoryItem) bool {
	return item.Sub == envelope.SubStatus || item.PID != "" && (item.Sub == "" || item.Sub == envelope.SubEvent || item.Sub == envelope.SubExcerpt)
}

// Linked replicas vouch for original attribution, not membership, execution,
// or selected cross-person history. Original signed events and request scope
// supply the PID authority; the vouched stamp is the original own admission.
func (a *Agent) groupParticipationHistoryCheck(q dbq, root protocol.ConvRoot, forwarder identity.Public, item HistoryItem) (*protocol.ParticipationEvent, error) {
	packet, err := groupTurnPacketIn(q, root.ID())
	if err != nil {
		return nil, err
	}
	if !sameGroupRoot(root, packet.Root) {
		return nil, errors.New("group: participation history root differs")
	}
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return nil, err
	}
	if !ok || !me.has(forwarder.Address, forwarder.Fingerprint()) || item.GroupHistory != nil && (!me.roster.Human(forwarder.Fingerprint()) || !me.roster.Human(a.Self().Fingerprint())) {
		return nil, errors.New("group: participation history requires current own linked forwarder")
	}
	if item.GroupHistory != nil {
		if err = historyRecoveryCurrent(q, a.Self(), forwarder); err != nil {
			return nil, err
		}
	}
	if err = groupTurnCheck(q, packet, forwarder.Address, forwarder.Fingerprint()); err != nil {
		return nil, err
	}
	own, err := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return nil, err
	}
	if item.GroupAdmission == "" || item.GroupAdmission != own.Hash() {
		return nil, errGroupParticipationHistoryEpoch
	}
	if item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) || item.TS <= 0 || !groupParticipationHistoryItem(item) || len(item.Attachments) > envelope.MaxAttachments {
		return nil, errors.New("group: malformed participation history")
	}
	if _, _, err = protocol.SplitAddress(item.From); err != nil {
		return nil, err
	}
	for _, f := range item.Attachments {
		if f.Name == "" || f.Size < 0 || f.Size > MaxFileSize || !protocol.ValidHash(f.SHA256) || f.Blob != (envelope.Blob{}) {
			return nil, errors.New("group: historical participation file is not a manifest")
		}
	}
	original := item.inner(root.ID())
	if err := envelope.CheckSendGroup(original); err != nil {
		return nil, err
	}
	if err = receiverHistoryRoute(q, original, item.FromKey); err != nil {
		return nil, err
	}
	if item.Sub == envelope.SubStatus {
		if item.PID != "" || len(item.Attachments) != 0 || envelope.ValidateControl(original) != nil || item.Ref == nil {
			return nil, errors.New("group: malformed historical status")
		}
		var context []dmMembers
		if item.GroupHistory != nil {
			scoped, e := groupHistoryScope(q, root.ID(), item, a.Self().Fingerprint())
			if e != nil {
				return nil, e
			}
			m, e := groupHistoryMembers(q, root, scoped)
			if e != nil {
				return nil, e
			}
			if m.keyEpoch(a.Self().Fingerprint()) != item.GroupAdmission {
				return nil, errGroupParticipationHistoryEpoch
			}
			context = append(context, m)
		}
		_, err = a.groupStatusAuthority(q, ControlRef{Conv: root.ID(), ID: item.Ref.ID, Fingerprint: item.Ref.Fingerprint}, item.From, item.FromKey, true, context...)
		return nil, err
	}
	if !protocol.ValidID(item.PID) || item.Ref != nil || item.ReplyTo != "" && !protocol.ValidID(item.ReplyTo) {
		return nil, errors.New("group: malformed historical PID scope")
	}
	m, err := membersIn(q, root.ID())
	if err == nil && item.GroupHistory != nil {
		m, err = groupHistoryMembers(q, root, item)
	}
	if err != nil {
		return nil, err
	}
	events, err := participationEventsIn(q, root.ID(), item.PID)
	if err != nil {
		return nil, err
	}
	if item.GroupHistory != nil {
		events = mergeHistoryEvents(nil, m.historyEvents, item.PID)
		if m.keyEpoch(a.Self().Fingerprint()) != item.GroupAdmission {
			return nil, errGroupParticipationHistoryEpoch
		}
	}
	var ev *protocol.ParticipationEvent
	retiredOwnEnd := false
	if item.Sub == envelope.SubEvent {
		candidate, e := protocol.ParseParticipationEvent([]byte(original.Body))
		if e != nil {
			return nil, e
		}
		author := candidate.Author
		forwarded := author.Address != item.From || author.Fingerprint != item.FromKey
		if forwarded && !m.device(item.From, item.FromKey) && candidate.Type == protocol.EventDismiss {
			if item.GroupHistory == nil {
				return nil, errGroupParticipationHistoryEpoch
			}
			retiredOwnEnd, e = groupHistoryOwnHumanTransport(q, me, m, item)
			if e != nil {
				return nil, e
			}
		}
		if forwarded && (candidate.Type != protocol.EventDismiss || !m.device(item.From, item.FromKey) && !retiredOwnEnd) {
			return nil, errors.New("group: only verified original members forward historical participation ends")
		}
		p, found, e := scanPersonIn(q, "person IN (SELECT person FROM person_devices WHERE address=?)", author.Address)
		if e != nil {
			return nil, e
		}
		if !found {
			return nil, ErrGroupContextPending
		}
		key, found := p.device(author.Address)
		if !found || key.Fingerprint() != author.Fingerprint || p.info.Person != author.Person || p.info.State != personSelf && p.info.State != personPinned {
			return nil, errGroupParticipationHistoryEpoch
		}
		var bound int
		if e = q.QueryRow(`SELECT count(*) FROM person_chain WHERE person=? AND hash=?`, author.Person, author.Roster).Scan(&bound); e != nil {
			return nil, e
		}
		if bound != 1 {
			return nil, errGroupParticipationHistoryEpoch
		}
		// Like live forwarded ends, retain the transport sender while checking
		// the embedded record under its original author's exact pinned key.
		signed := original
		signed.From = author.Address
		candidate, e = checkParticipationEvent(signed, author.Fingerprint, key.SignKey)
		if e != nil {
			return nil, e
		}
		if author.GroupAdmission != "" && m.keyEpoch(author.Fingerprint) != author.GroupAdmission {
			return nil, errGroupParticipationHistoryEpoch
		}
		ev = &candidate
		duplicate := false
		for _, stored := range events {
			if stored.Hash() == candidate.Hash() {
				duplicate = true
			}
		}
		if !duplicate {
			events = append(events, candidate)
		}
	}
	if err = m.loadHosts(q, events); err != nil {
		return nil, err
	}
	info := resolve(root.ID(), item.PID, events, m)
	if info.Invite == "" {
		return nil, ErrGroupContextPending
	}
	if item.Human != nil {
		var context []dmMembers
		if item.GroupHistory != nil {
			context = append(context, m)
		}
		if err = humanTurnAuthorization(q, original, item.From, item.FromKey, a.Address, a.Self().Fingerprint(), true, context...); err != nil {
			return nil, err
		}
	}
	// Ending an accepted assistant does not erase an original member's inert
	// addressed history. Reuse the live role/target checks with only that
	// lifecycle gate adapted; unresolved evidence and fresh delivery stay closed.
	roleInfo := info
	if item.Sub == envelope.SubExcerpt || item.Sub == "" && (item.Kind == envelope.KindQuestion || item.Kind == envelope.KindTask || item.Kind == envelope.KindAnswer || item.Kind == envelope.KindResult || isResponderProgress(original)) {
		retained, e := retainedAssistant(q, info, m.historyEvents)
		if e != nil {
			return nil, e
		}
		if retained {
			roleInfo.State = PartActive
		}
	}
	if retiredOwnEnd {
		// The original own human only transported this independently signed,
		// counted end. Its removal cannot grant live membership or revive the PID.
		if info.Held != 0 || info.Conflict != "" || ev == nil || ev.Type != protocol.EventDismiss || ev.Hash() != info.Dismissal {
			return nil, errors.New("group: historical forwarded end is not the exact counted dismissal")
		}
	} else if err = externalTurn(original, roleInfo, m, item.From, item.FromKey, q); err != nil {
		if info.State == PartInvited && item.Sub == "" {
			return nil, ErrGroupContextPending
		}
		return nil, err
	}
	if reason, e := externalOutputRequestMode(q, original, info, m, a.Address, a.Self().Fingerprint(), true); e != nil {
		if reason == reasonProof {
			return nil, ErrGroupContextPending
		}
		return nil, e
	}
	return ev, nil
}

// A retired transport is attribution, never present-day authority. Only the
// verified own chain can establish its original human identity; the independently
// signed end and witnessed admission supply the exact historical group scope.
func groupHistoryOwnHumanTransport(q dbq, own personRow, m dmMembers, item HistoryItem) (bool, error) {
	if item.GroupHistory == nil || m.group == nil || m.historyEvents == nil {
		return false, nil
	}
	member, ok := m.group.State.Member(own.info.Person)
	if !ok || member.Admission.Hash() != item.GroupAdmission {
		return false, nil
	}
	pin, found, err := pinnedKey(q, item.From)
	if err != nil {
		return false, err
	}
	var pending int
	if err = q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, item.From).Scan(&pending); err != nil {
		return false, err
	}
	if pending != 0 || found && pin.Fingerprint() != item.FromKey {
		return false, errors.New("group: historical transport pin changed")
	}
	rows, err := q.Query(`SELECT record FROM person_chain WHERE person=? ORDER BY seq DESC`, own.info.Person)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		var roster protocol.PersonRoster
		if err = json.Unmarshal([]byte(raw), &roster); err != nil {
			return false, err
		}
		if roster.Person == own.info.Person && roster.Has(item.From, item.FromKey) && roster.Human(item.FromKey) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// retainedAssistant authenticates a cleanly ended, previously accepted
// assistant for inert own-member history only. It confers no live authority.
func retainedAssistant(q dbq, info ParticipationInfo, proofs ...[]protocol.ParticipationEvent) (bool, error) {
	if info.Role == protocol.RoleHuman || info.Invite == "" || info.State != PartDismissed || info.Held != 0 || info.Conflict != "" || info.Decision == "" {
		return false, nil
	}
	if len(proofs) > 0 && proofs[0] != nil {
		for _, ev := range proofs[0] {
			if ev.Conv == info.Conv && ev.PID == info.PID && ev.Hash() == info.Decision {
				return ev.Type == protocol.EventAccept, nil
			}
		}
		return false, nil
	}
	var kind string
	err := q.QueryRow(`SELECT type FROM participation_events WHERE conv=? AND pid=? AND hash=?`, info.Conv, info.PID, info.Decision).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return kind == protocol.EventAccept, err
}

// OUT PID/status group_admission pins the destination, never the source.
// Convert only exact original locally signed bytes plus verified signed PID
// source/request epochs. IN preserves the real original receive admission;
// missing stamps stay unavailable rather than becoming backfilled authority.
func (a *Agent) groupParticipationSourceAdmission(q dbq, packet GroupContext, item HistoryItem) (string, error) {
	own, err := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return "", err
	}
	stored := HistoryItem{V: 1, At: item.At}
	var target, refID, refFP, raw, stamp string
	fileTable := "attachments"
	if item.From == a.Address && item.FromKey == a.Self().Fingerprint() {
		fileTable = "sent_attachments"
		stored.From, stored.FromKey = a.Address, a.Self().Fingerprint()
		err = q.QueryRow(`SELECT id,lid,created_at,kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(sub,''),coalesce(origin,''),coalesce(emotion,''),coalesce(target,''),coalesce(pid,''),coalesce(ref_id,''),coalesce(ref_fp,''),coalesce(agent_id,''),envelope FROM outbox WHERE id=? AND conv=?`, item.ID, packet.State.Conv).Scan(&stored.ID, &stored.LID, &stored.TS, &stored.Kind, &stored.Body, &stored.ReplyTo, &stored.Status, &stored.Sub, &stored.Origin, &stored.Emotion, &target, &stored.PID, &refID, &refFP, &stored.AgentID, &raw)
		if err != nil {
			return "", err
		}
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) != nil || env.ID != item.ID || env.From != item.From || env.Kind != item.Kind || env.TS != item.TS || env.VerifySig(a.Self().SignKey) != nil {
			return "", errors.New("group: participation history lacks exact original signed outbox")
		}
		// Status has no signed epoch of its own: the immutable accepted request
		// binds the current invited host epoch. No destination stamp is converted.
		stored.TS = env.TS
		stamp = own.Hash()
	} else {
		err = q.QueryRow(`SELECT id,lid,sender,coalesce(verified_by,claimed_fp,''),ts,kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(sub,''),coalesce(origin,''),coalesce(emotion,''),coalesce(target,''),coalesce(pid,''),coalesce(ref_id,''),coalesce(ref_fp,''),CASE WHEN kind IN ('question','task') THEN '' ELSE coalesce(agent_id,'') END,coalesce(group_admission,'') FROM inbox WHERE id=? AND conv=?`, item.ID, packet.State.Conv).Scan(&stored.ID, &stored.LID, &stored.From, &stored.FromKey, &stored.TS, &stored.Kind, &stored.Body, &stored.ReplyTo, &stored.Status, &stored.Sub, &stored.Origin, &stored.Emotion, &target, &stored.PID, &refID, &refFP, &stored.AgentID, &stamp)
		if err != nil {
			return "", err
		}
		if stamp == "" || stamp != own.Hash() {
			return "", errGroupParticipationHistoryEpoch
		}
	}
	if target != "" {
		if err = json.Unmarshal([]byte(target), &stored.Target); err != nil {
			return "", err
		}
	}
	if refID != "" {
		stored.Ref = &envelope.Ref{ID: refID, Fingerprint: refFP}
	}
	dir := "in"
	if fileTable == "sent_attachments" {
		dir = "out"
	}
	stored, err = a.historySourceItem(q, historySourceRow{dir: dir, key: stored.FromKey, in: stored.inner(packet.State.Conv), pos: historyPos{Ms: stored.At}})
	if err != nil {
		return "", err
	}
	originalItem := item
	originalItem.GroupHistory = nil
	if !sameControlItem(stored, originalItem) {
		return "", errors.New("group: participation history differs from original row")
	}
	item.GroupAdmission = stamp
	if _, err = a.groupParticipationHistoryCheck(q, packet.Root, a.Self(), item); err != nil {
		return "", err
	}
	return stamp, nil
}

func (a *Agent) groupParticipationHistoryOutboundCheck(q dbq, packet GroupContext, to, fp string, item HistoryItem) error {
	own, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || !own.has(to, fp) || item.GroupHistory != nil && !own.roster.Human(fp) {
		return errors.New("group: PID history recipient is not an own linked device")
	}
	if item.GroupHistory != nil {
		dev, _ := own.device(to) // exact current fingerprint checked above
		if err = historyRecoveryCurrent(q, a.Self(), dev); err != nil {
			return err
		}
	}
	if err = groupTurnCheck(q, packet, to, fp); err != nil {
		return err
	}
	stamp, err := a.groupParticipationSourceAdmission(q, packet, item)
	if err != nil {
		return err
	}
	if stamp != item.GroupAdmission {
		return errGroupParticipationHistoryEpoch
	}
	return nil
}

func (a *Agent) admitGroupParticipationHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, item HistoryItem, fromQuarantine bool, hold func(string, string) error) error {
	if !in.Replica || len(in.Attachments) != 0 {
		return hold(reasonInvalid, "group: PID history requires own replica without carrier files")
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
	if item.Sub == envelope.SubEvent {
		if ev, e := protocol.ParseParticipationEvent([]byte(item.Body)); e == nil && ev.Type == protocol.EventInvite && ev.Host != nil {
			if reason, e := a.externalHostProof(ctx, ev.Host); e != nil {
				if reason != "" {
					return hold(reason, e.Error())
				}
				return e
			}
		}
	}
	if item.Human != nil {
		var context []dmMembers
		if item.GroupHistory != nil {
			m, e := groupHistoryMembers(a.store.db, root, item)
			if e != nil {
				return hold(reasonProof, e.Error())
			}
			context = append(context, m)
		}
		if err = a.verifyHumanProof(ctx, root, item.Human, context...); err != nil {
			return hold(reasonProof, err.Error())
		}
	}
	check := func(q dbq) (*protocol.ParticipationEvent, error) {
		return a.groupParticipationHistoryCheck(q, root, sender, item)
	}
	if _, err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	result, err := a.store.addHistoryInbox(item.inner(in.Conv), item.At, item.FromKey, env.From, env.ID, fromQuarantine, func(tx *sql.Tx) error {
		if item.Human != nil && item.GroupHistory == nil {
			if err := insertHumanProof(tx, item.Human); err != nil {
				return err
			}
		}
		ev, e := check(tx)
		if e != nil {
			return e
		}
		if ev != nil && item.GroupHistory == nil {
			if e = insertParticipationEvent(tx, *ev, []byte(item.Body)); e != nil {
				return e
			}
		}
		_, e = tx.Exec(`UPDATE inbox SET group_admission=?,group_history=nullif(?,'') WHERE id=?`, item.GroupAdmission, groupHistoryJSON(item.GroupHistory), item.ID)
		return e
	})
	if err != nil {
		return err
	}
	if result == admitConflict {
		return hold(reasonDuplicate, "group: PID history differs from original logical copy")
	}
	if result == admitted {
		a.convWork.due(convRetry)
		a.kickNow()
	}
	return nil
}
