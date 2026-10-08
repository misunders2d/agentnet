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
	if !ok || !me.has(forwarder.Address, forwarder.Fingerprint()) {
		return nil, errors.New("group: participation history requires current own linked forwarder")
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
	if err = receiverHistoryRoute(q, original, item.FromKey); err != nil {
		return nil, err
	}
	if item.Sub == envelope.SubStatus {
		if item.PID != "" || len(item.Attachments) != 0 || envelope.ValidateControl(original) != nil || item.Ref == nil {
			return nil, errors.New("group: malformed historical status")
		}
		_, err = a.groupStatusAuthority(q, ControlRef{Conv: root.ID(), ID: item.Ref.ID, Fingerprint: item.Ref.Fingerprint}, item.From, item.FromKey, true)
		return nil, err
	}
	if !protocol.ValidID(item.PID) || item.Ref != nil || item.ReplyTo != "" && !protocol.ValidID(item.ReplyTo) {
		return nil, errors.New("group: malformed historical PID scope")
	}
	m, err := membersIn(q, root.ID())
	if err != nil {
		return nil, err
	}
	events, err := participationEventsIn(q, root.ID(), item.PID)
	if err != nil {
		return nil, err
	}
	var ev *protocol.ParticipationEvent
	if item.Sub == envelope.SubEvent {
		candidate, e := protocol.ParseParticipationEvent([]byte(original.Body))
		if e != nil {
			return nil, e
		}
		author := candidate.Author
		forwarded := author.Address != item.From || author.Fingerprint != item.FromKey
		if forwarded && (candidate.Type != protocol.EventDismiss || !m.device(item.From, item.FromKey)) {
			return nil, errors.New("group: only current members forward historical participation ends")
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
		if err = humanTurnAuthorization(q, original, item.From, item.FromKey, a.Address, a.Self().Fingerprint(), true); err != nil {
			return nil, err
		}
	}
	// Ending an accepted assistant does not erase an original member's inert
	// addressed history. Reuse the live role/target checks with only that
	// lifecycle gate adapted; unresolved evidence and fresh delivery stay closed.
	roleInfo := info
	if item.Sub == envelope.SubExcerpt || item.Sub == "" && (item.Kind == envelope.KindQuestion || item.Kind == envelope.KindTask || item.Kind == envelope.KindAnswer || item.Kind == envelope.KindResult || isResponderProgress(original)) {
		retained, e := retainedAssistant(q, info)
		if e != nil {
			return nil, e
		}
		if retained {
			roleInfo.State = PartActive
		}
	}
	if err = externalTurn(original, roleInfo, m, item.From, item.FromKey); err != nil {
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

// retainedAssistant authenticates a cleanly ended, previously accepted
// assistant for inert own-member history only. It confers no live authority.
func retainedAssistant(q dbq, info ParticipationInfo) (bool, error) {
	if info.Role == protocol.RoleHuman || info.Invite == "" || info.State != PartDismissed || info.Held != 0 || info.Conflict != "" || info.Decision == "" {
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
	stored.ReceiverRoute, err = receiverStoredRoute(q, dir, item.ID)
	if err != nil {
		return "", err
	}
	stored.Human, err = storedHuman(q, dir, item.ID)
	if err != nil {
		return "", err
	}
	rows, err := q.Query("SELECT name,size,sha256 FROM "+fileTable+" WHERE message_id=? ORDER BY rowid", item.ID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var f envelope.Attachment
		if err = rows.Scan(&f.Name, &f.Size, &f.SHA256); err != nil {
			break
		}
		stored.Attachments = append(stored.Attachments, f)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	if !sameControlItem(stored, item) {
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
	if !ok || !own.has(to, fp) {
		return errors.New("group: PID history recipient is not an own linked device")
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
		if err = a.verifyHumanProof(ctx, root, item.Human); err != nil {
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
		if item.Human != nil {
			if err := insertHumanProof(tx, item.Human); err != nil {
				return err
			}
		}
		ev, e := check(tx)
		if e != nil {
			return e
		}
		if ev != nil {
			if e = insertParticipationEvent(tx, *ev, []byte(item.Body)); e != nil {
				return e
			}
		}
		_, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, item.GroupAdmission, item.ID)
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
