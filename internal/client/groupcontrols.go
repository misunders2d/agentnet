package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupControlSub(sub string) bool {
	return sub == envelope.SubReaction || sub == envelope.SubRevision || sub == envelope.SubRetraction
}

// A reordered control stays on proof retry until its exact scoped original
// arrives. Known foreign/ambiguous originals never become that authority.
func (a *Agent) groupControlTarget(q dbq, in envelope.Inner) error {
	rows, err := q.Query(`SELECT coalesce(conv,''),coalesce(lid,''),coalesce(verified_by,claimed_fp,'') FROM inbox WHERE (id=? OR lid=?) AND ref_id IS NULL
	 UNION ALL SELECT coalesce(conv,''),coalesce(lid,''),? FROM outbox WHERE (id=? OR lid=?) AND ref_id IS NULL`, in.Ref.ID, in.Ref.ID, a.Self().Fingerprint(), in.Ref.ID, in.Ref.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var conv, lid, author string
		if err = rows.Scan(&conv, &lid, &author); err != nil {
			return err
		}
		if conv != in.Conv || lid != in.Ref.ID || author != in.Ref.Fingerprint {
			return errors.New("group control: original differs from exact conversation/logical id/author")
		}
		found = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !found {
		return ErrGroupContextPending
	}
	return nil
}

// controlMembers does not enable participation. The group audience derives
// only from already verified current context and its durable authority head.
func controlMembers(q dbq, conv string) (dmMembers, error) {
	// A plain DB call owns one fresh snapshot. Existing transaction callers
	// keep theirs; statement wrappers do not establish a separate read lifetime.
	source := q
	for {
		switch reads := source.(type) {
		case *membershipReads:
			source = reads.dbq
		case *preparedReads:
			source = reads.dbq
		default:
			goto unwrapped
		}
	}
unwrapped:
	if db, ok := source.(*sql.DB); ok {
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return dmMembers{}, err
		}
		defer tx.Rollback()
		q = tx
	}
	root, _, found, err := conversationIn(q, conv)
	if err != nil || !found {
		return dmMembers{}, err
	}
	if root.Kind != protocol.ConvKindGroup {
		return membersIn(q, conv)
	}
	q, closeReads := prepareMembershipReads(q)
	defer closeReads()
	packet, err := groupTurnPacketIn(q, conv)
	if err != nil {
		return dmMembers{}, err
	}
	if !sameGroupRoot(root, packet.Root) {
		return dmMembers{}, errors.New("group control: current root differs")
	}
	if err = groupDeliveryHead(q, packet); err != nil {
		return dmMembers{}, err
	}
	for _, member := range packet.State.Members {
		var pending int
		if err = q.QueryRow(`SELECT count(*) FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?`, conv, member.Person, member.Admission.Hash()).Scan(&pending); err != nil {
			return dmMembers{}, err
		}
		if pending != 0 {
			return dmMembers{}, ErrGroupContextPending
		}
	}
	people := make(map[string]personRow, len(packet.State.Members))
	withdrawn := make(map[string]bool, len(packet.State.Members))
	var members []protocol.ConvMember
	for _, member := range packet.State.Members {
		p, ok, err := personByIDIn(q, member.Person)
		if err != nil {
			return dmMembers{}, err
		}
		if !ok || p.info.State == personConflict {
			return dmMembers{}, ErrGroupContextPending
		}
		people[member.Person] = p
		var count int
		if err = q.QueryRow(`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`, conv, member.Person, member.Admission.Hash()).Scan(&count); err != nil {
			return dmMembers{}, err
		}
		withdrawn[member.Person] = count != 0 || packet.State.Withdrawn(member, packet.Withdrawals)
		if !withdrawn[member.Person] {
			members = append(members, member.ConvMember)
		}
	}
	// Preserve the exact recipient walk, including first-match behavior if
	// conflicting stored rosters happen to claim the same address/key.
	for _, member := range members {
		for _, device := range people[member.Person].roster.Devices {
			found := false
			for _, candidate := range packet.State.Members {
				if !people[candidate.Person].has(device.Address, device.Fingerprint()) {
					continue
				}
				if withdrawn[candidate.Person] {
					return dmMembers{}, errGroupRecipientWithdrawn
				}
				found = true
				break
			}
			if !found {
				return dmMembers{}, errGroupRecipientNotCurrent
			}
		}
	}
	m, err := memberRowsKnownIn(q, root, members, people)
	m.group = &packet
	return m, err
}

// groupControlEpochFence is local retry metadata ONLY for V3 message-control
// outbox rows. It binds both admission epochs, never supplies signed authority.
// Ordinary/history/file rows retain their existing admission interpretation.
func groupControlEpochFence(q dbq, packet GroupContext, from, fromFP, to, toFP string) (string, error) {
	sender, err := groupMemberAdmission(q, packet, from, fromFP)
	if err != nil {
		return "", err
	}
	recipient, err := groupMemberAdmission(q, packet, to, toFP)
	if err != nil {
		return "", err
	}
	return groupControlAdmissionFence(sender, recipient), nil
}

func groupControlAdmissionFence(sender, recipient protocol.GroupAdmission) string {
	hash := sha256.Sum256([]byte("agentnet-group-control-epochs-v1\n" + sender.Hash() + "\x00" + recipient.Hash()))
	return hex.EncodeToString(hash[:])
}

func (a *Agent) requireGroupControlCapability(ctx context.Context, key identity.Public) error {
	profile, err := a.peerProfile(ctx, key.Address)
	if err != nil {
		return err
	}
	if !profile.Supports(key.Address, key.SignKey, protocol.CapControl) {
		return fmt.Errorf("%w: %s cannot read group message controls", errAgentIdentityUnsupported, key.Address)
	}
	return nil
}

func (a *Agent) mayDeliverGroupControl(env envelope.Envelope) (bool, bool, error) {
	if handled, allowed, err := a.mayDeliverAssistantReaction(env); handled {
		return true, allowed, err
	}
	var conv, sub, state, fp, fence, required string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),coalesce(sub,''),state,coalesce(recipient_fp,''),coalesce(group_admission,''),coalesce(required_cap,'') FROM outbox WHERE id=?`, env.ID).Scan(&conv, &sub, &state, &fp, &fence, &required)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if conv == "" || !groupControlSub(sub) {
		return false, false, nil
	}
	root, _, found, err := a.store.conversation(conv)
	if err != nil {
		return true, false, err
	}
	if !found || root.Kind != protocol.ConvKindGroup {
		return false, false, nil
	}
	if state != stateQueued || required != protocol.CapGroup || fp == "" || fence == "" {
		return true, false, nil
	}
	packet, err := groupTurnPacketIn(a.store.db, conv)
	if err == nil {
		var current string
		current, err = groupControlEpochFence(a.store.db, packet, a.Address, a.Self().Fingerprint(), env.To, fp)
		if err == nil && current != fence {
			err = errors.New("group control: original sender or recipient admission changed")
		}
	}
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		err = a.store.setOutboxState(env.ID, stateNotDelivered, "group control membership no longer eligible", "")
		return true, false, err
	}
	return true, true, nil
}
