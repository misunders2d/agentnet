package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Only an original recorded local invitation can create historical fanout.
// A remotely received admission or a later membership refresh cannot do so.
func (a *Agent) groupHistoryIntent(q dbq, packet GroupContext) (groupInvitationRow, bool, error) {
	rows, err := q.Query(`SELECT id FROM group_invitations WHERE direction='out' AND conv=? AND json_extract(payload,'$.seq')=? AND state IN('accepted','history-unavailable','published','published-history-unavailable')`, packet.State.Conv, packet.State.Seq)
	if err != nil {
		return groupInvitationRow{}, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return groupInvitationRow{}, false, err
	}
	var found groupInvitationRow
	ok := false
	for _, id := range ids {
		r, e := groupInvitationIn(q, id, "out")
		if e != nil {
			return found, false, e
		}
		if len(r.Proposal.History) == 0 {
			continue
		}
		var consent protocol.GroupConsent
		if json.Unmarshal(r.consent, &consent) != nil || consent.Validate() != nil || consent.Invitation != id || consent.Admission == nil {
			return found, false, errors.New("group: historical fanout lacks recorded exact consent")
		}
		member, present := packet.State.Member(r.Proposal.Target)
		if !present || member.Admission.Hash() != consent.Admission.Hash() {
			continue
		}
		if r.Inviter != a.Address || r.peerFP != a.Self().Fingerprint() || r.Proposal.Root.ID() != packet.Root.ID() || r.Proposal.Prev != packet.State.Prev {
			return found, false, ErrGroupInvitationStale
		}
		if ok {
			return found, false, errors.New("group: ambiguous local historical fanout intent")
		}
		found, ok = r, true
	}
	return found, ok, nil
}

func (a *Agent) groupHistoryPreflight(ctx context.Context, c protocol.GroupCommit, packet GroupContext) error {
	r, ok, err := a.groupHistoryIntent(a.store.db, packet)
	if err != nil || !ok {
		return err
	}
	if _, err = a.groupHistorySelectionIn(a.store.db, packet.State.Conv, r.Proposal.History); err == nil {
		return nil
	}
	if !errors.Is(err, ErrGroupHistoryUnavailable) {
		return err
	}
	// Never append new CAS bytes for unavailable content. An already committed
	// exact original record is custody recovery, and must not be rolled back.
	if e := a.ensureGroupProof(ctx, packet.Root, c); e != nil {
		return errors.Join(ErrGroupHistoryUnavailable, e)
	}
	stored, e := groupProofRecord(a.store.db, c.Conv, c.Bootstrap, c.Seq)
	if e != nil {
		return e
	}
	x, _ := json.Marshal(stored)
	y, _ := json.Marshal(c)
	if !bytes.Equal(x, y) {
		return ErrGroupHistoryUnavailable
	}
	return nil
}

func (a *Agent) groupHistoryPublicationCopies(ctx context.Context, packet GroupContext) ([]outCopy, *groupInvitationRow, error) {
	r, ok, err := a.groupHistoryIntent(a.store.db, packet)
	if err != nil || !ok {
		return nil, nil, err
	}
	items, err := a.groupHistorySelectionIn(a.store.db, packet.State.Conv, r.Proposal.History)
	if errors.Is(err, ErrGroupHistoryUnavailable) {
		return nil, &r, err
	}
	if err != nil {
		return nil, &r, err
	}
	target, present, err := a.store.personByID(r.Proposal.Target)
	if err != nil {
		return nil, &r, err
	}
	if !present {
		return nil, &r, ErrGroupContextPending
	}
	var copies []outCopy
	for _, dev := range target.roster.Devices {
		key, e := a.sendKey(ctx, dev.Address)
		if e != nil {
			return nil, &r, e
		}
		if key.Fingerprint() != dev.Fingerprint() {
			return nil, &r, ErrGroupInvitationStale
		}
		for _, item := range items {
			if e = groupHistorySelected(a.store.db, packet, dev.Address, dev.Fingerprint(), historyRef(packet.State.Conv, item)); e != nil {
				return nil, &r, e
			}
			item.GroupAdmission = "" // imported selections never acquire direct-live provenance
			copy, e := a.groupHistoryCarrier(key, packet, item)
			if e != nil {
				return nil, &r, e
			}
			admission, e := groupMemberAdmission(a.store.db, packet, dev.Address, dev.Fingerprint())
			if e != nil {
				return nil, &r, e
			}
			copy.groupAdmission = admission.Hash() // queued receiver epoch, never a direct-source stamp
			copies = append(copies, copy)
		}
	}
	return copies, &r, nil
}

func (a *Agent) groupHistoryPublicationCheck(tx *sql.Tx, packet GroupContext, intent *groupInvitationRow, copies []outCopy, unavailable bool) error {
	if intent == nil {
		return nil
	}
	current, ok, err := a.groupHistoryIntent(tx, packet)
	if err != nil {
		return err
	}
	if !ok || current.ID != intent.ID {
		return ErrGroupInvitationStale
	}
	if err = groupTurnCheck(tx, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	if !unavailable {
		if _, err = a.groupHistorySelectionIn(tx, packet.State.Conv, current.Proposal.History); err != nil {
			return err
		}
		for _, copy := range copies {
			var item HistoryItem
			if decodeStrict([]byte(copy.in.Body), &item) != nil {
				return errors.New("group: invalid selected outbox item")
			}
			if err = groupHistorySelected(tx, packet, copy.env.To, copy.recipientFP, historyRef(packet.State.Conv, item)); err != nil {
				return err
			}
			admission, e := groupMemberAdmission(tx, packet, copy.env.To, copy.recipientFP)
			if e != nil {
				return e
			}
			if admission.Hash() != copy.groupAdmission {
				return ErrGroupContextPending
			}
		}
	}
	state := "published"
	if unavailable {
		state = "published-history-unavailable"
	}
	_, err = tx.Exec(`UPDATE group_invitations SET state=? WHERE id=? AND direction='out'`, state, current.ID)
	return err
}
