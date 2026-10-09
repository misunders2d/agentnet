package client

import (
	"context"
	"database/sql"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) groupHistoryReceiveCheck(q dbq, root protocol.ConvRoot, sender identity.Public, item HistoryItem) error {
	if err := envelope.CheckTopic(item.inner(root.ID())); err != nil {
		return err
	}
	if envelope.TopicOrganization(item.TopicEvent) {
		if err := topicOrganizationAuthor(q, root.ID(), item.From, item.FromKey); err != nil {
			return err
		}
	}
	if err := receiverHistoryRoute(q, item.inner(root.ID()), item.FromKey); err != nil {
		return err
	}
	packet, err := groupTurnPacketIn(q, root.ID())
	if err != nil {
		return err
	}
	if !sameGroupRoot(root, packet.Root) {
		return errors.New("group: history root differs from verified context")
	}
	if err = groupTurnCheck(q, packet, sender.Address, sender.Fingerprint()); err != nil {
		return err
	}
	recipient, err := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	ref := historyRef(root.ID(), item)
	if !recipient.AllowsHistory(ref) {
		me, ok, e := scanPersonIn(q, "state = ?", personSelf)
		if e != nil {
			return e
		}
		if !ok || !me.has(sender.Address, sender.Fingerprint()) || item.GroupAdmission == "" || item.GroupAdmission != recipient.Hash() {
			return errors.New("group: historical item lacks exact selected grant/current own live admission")
		}
		// Our exact linked device vouches for historical attribution, as in
		// DMs. A departed author's old messages stay history, with no current
		// author authority. Sender, recipient and own admission stay current.
	}
	if retractedRef(q, root.ID(), item.LID, item.FromKey) {
		return ErrGroupHistoryUnavailable
	}
	return groupHistoryCollision(q, root.ID(), item)
}

func (a *Agent) admitGroupHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	var item HistoryItem
	if decodeGroupCarrierJSON([]byte(in.Body), &item) != nil || item.GroupHistory != nil && (!groupParticipationHistoryItem(item) || item.PID == "" && item.Sub != envelope.SubStatus) {
		return hold(reasonInvalid, "group: invalid historical witness scope")
	}
	if decodeGroupCarrierJSON([]byte(in.Body), &item) == nil && item.V == 1 && groupControlSub(item.Sub) {
		return a.admitGroupControlHistory(ctx, env, in, root, sender, item, fromQuarantine, hold)
	}
	if item.V == 1 && item.PID != "" && item.Human != nil {
		for _, ev := range item.Human.Proof {
			if ev.Role == protocol.RoleHuman {
				return hold(reasonInvalid, "group human guest execution audience is not enabled")
			}
		}
	}
	if item.V == 1 && groupParticipationHistoryItem(item) {
		return a.admitGroupParticipationHistory(ctx, env, in, root, sender, item, fromQuarantine, hold)
	}
	if !in.Replica || len(in.Attachments) != 0 || decodeGroupCarrierJSON([]byte(in.Body), &item) != nil || item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) || item.TS <= 0 || len(item.Attachments) > envelope.MaxAttachments || !ordinaryGroupTurn(item.inner(in.Conv)) || item.Ref != nil {
		return hold(reasonInvalid, "group: malformed/nonordinary historical item")
	}
	if _, _, err := protocol.SplitAddress(item.From); err != nil {
		return hold(reasonInvalid, "group: malformed claimed historical author")
	}
	if item.ReplyTo != "" && !protocol.ValidID(item.ReplyTo) {
		return hold(reasonInvalid, "group: history reply is not a logical ID")
	}
	for _, f := range item.Attachments {
		if f.Name == "" || f.Size < 0 || f.Size > MaxFileSize || !protocol.ValidHash(f.SHA256) || f.Blob.ID != "" || f.Blob.Size != 0 || f.Blob.SHA256 != "" {
			return hold(reasonInvalid, "group: history file is not a manifest")
		}
	}
	packet, err := groupTurnPacketIn(a.store.db, in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	for _, m := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, m.Person, false); err != nil {
			return err
		}
	}
	check := func(q dbq) error { return a.groupHistoryReceiveCheck(q, root, sender, item) }
	if err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	// Parent absence is deliberate selected-history context, never a fetch or
	// authority failure. The original logical ReplyTo is preserved as a claim.
	_, err = a.store.addHistoryInbox(item.inner(in.Conv), item.At, item.FromKey, env.From, env.ID, fromQuarantine, func(tx *sql.Tx) error {
		// A selected import remains unstamped. A verified current own live
		// forwarding copy preserves that exact admission, never today's guess.
		current, e := groupTurnPacketIn(tx, in.Conv)
		if e != nil {
			return e
		}
		admission, e := groupMemberAdmission(tx, current, a.Address, a.Self().Fingerprint())
		if e != nil {
			return e
		}
		if admission.AllowsHistory(historyRef(in.Conv, item)) {
			return nil
		}
		_, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, item.GroupAdmission, item.ID)
		return e
	}, func(tx *sql.Tx) error { return check(tx) })
	if err != nil {
		return err
	}
	a.convWork.due(convRetry)
	a.kickNow()
	return nil
}
