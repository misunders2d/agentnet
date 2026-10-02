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
	root, _, found, err := conversationIn(q, conv)
	if err != nil || !found {
		return dmMembers{}, err
	}
	if root.Kind != protocol.ConvKindGroup {
		return membersIn(q, conv)
	}
	packet, err := groupTurnPacketIn(q, conv)
	if err != nil {
		return dmMembers{}, err
	}
	if !sameGroupRoot(root, packet.Root) {
		return dmMembers{}, errors.New("group control: current root differs")
	}
	for _, m := range packet.State.Members {
		var pending int
		if err = q.QueryRow(`SELECT count(*) FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?`, conv, m.Person, m.Admission.Hash()).Scan(&pending); err != nil {
			return dmMembers{}, err
		}
		if pending != 0 {
			return dmMembers{}, ErrGroupContextPending
		}
	}
	var members []protocol.ConvMember
	for _, m := range packet.State.Members {
		p, ok, err := personByIDIn(q, m.Person)
		if err != nil {
			return dmMembers{}, err
		}
		if !ok || p.info.State == personConflict {
			return dmMembers{}, ErrGroupContextPending
		}
		var withdrawn int
		if err = q.QueryRow(`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`, conv, m.Person, m.Admission.Hash()).Scan(&withdrawn); err != nil {
			return dmMembers{}, err
		}
		if withdrawn != 0 || packet.State.Withdrawn(m, packet.Withdrawals) {
			continue
		}
		for _, d := range p.roster.Devices {
			if err = groupDeliveryRecipient(q, packet, d.Address, d.Fingerprint()); err != nil {
				return dmMembers{}, err
			}
		}
		members = append(members, m.ConvMember)
	}
	m, err := memberRowsIn(q, root, members)
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
	hash := sha256.Sum256([]byte("agentnet-group-control-epochs-v1\n" + sender.Hash() + "\x00" + recipient.Hash()))
	return hex.EncodeToString(hash[:]), nil
}

func (a *Agent) requireGroupControlCapability(ctx context.Context, key identity.Public) error {
	label, device, err := protocol.SplitAddress(key.Address)
	if err != nil {
		return err
	}
	var profile protocol.Profile
	if err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		return err
	}
	if !profile.Supports(key.Address, key.SignKey, protocol.CapControl) {
		return fmt.Errorf("%w: %s cannot read group message controls", errAgentIdentityUnsupported, key.Address)
	}
	return nil
}

func (a *Agent) mayDeliverGroupControl(env envelope.Envelope) (bool, bool, error) {
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
