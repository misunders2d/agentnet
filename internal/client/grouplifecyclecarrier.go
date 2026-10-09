package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupLifecycleTransient(err error) bool {
	var he *HubError
	var ne net.Error
	return errors.As(err, &he) && retryable(err) || errors.As(err, &ne) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (a *Agent) groupLifecycleCopy(root protocol.ConvRoot, sub string, descriptor protocol.GroupCarrier, value any, key identity.Public, pids ...string) (copy outCopy, err error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return copy, err
	}
	limit := protocol.MaxGroupState
	if sub == envelope.SubGroupProof {
		limit = protocol.MaxBody - 1024
	}
	if len(raw) > limit {
		return copy, errors.New("group: lifecycle carrier exceeds bound")
	}
	recipient, err := key.Recipient()
	if err != nil {
		return copy, err
	}
	descriptor.ToKey = key.Fingerprint()
	body, _ := json.Marshal(descriptor)
	rootRaw, _ := json.Marshal(root)
	copy.in = envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: a.Address, To: key.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: root.ID(), Root: rootRaw, Sub: sub, Body: string(body)}
	if len(pids) > 0 {
		copy.in.PID = pids[0]
	}
	copy.state, copy.required, copy.recipientFP = stateQueued, protocol.CapGroup, key.Fingerprint()
	if p, ok := value.(protocol.GroupInvitation); ok && p.Nonce != "" {
		copy.required = protocol.CapGroupInvitationControl
	}
	if c, ok := value.(protocol.GroupConsent); ok && c.Decision == "cancelled" {
		copy.required = protocol.CapGroupInvitationControl
	}
	path, cleanup, err := a.StageUpload(sub+".json", bytes.NewReader(raw))
	if err != nil {
		return copy, err
	}
	defer cleanup()
	att, err := a.spoolNamed(OutgoingFile{Path: path, Name: sub + ".json"}, recipient)
	if err != nil {
		return copy, err
	}
	copy.in.Attachments = []envelope.Attachment{att}
	defer func() {
		if err != nil {
			a.releaseGroupCopies([]outCopy{copy})
		}
	}()
	copy.env, err = envelope.Seal(copy.in, a.id.Sign, recipient)
	if err == nil {
		encoded, _ := json.Marshal(copy.env)
		if len(encoded) > protocol.MaxBody {
			err = errors.New("group: lifecycle envelope exceeds transport bound")
		}
	}
	return copy, err
}

func (a *Agent) groupInvitationCopies(ctx context.Context, p protocol.GroupInvitation, target protocol.PersonRoster) (copies []outCopy, err error) {
	defer func() {
		if err != nil {
			a.releaseGroupCopies(copies)
		}
	}()
	rows, err := a.store.db.Query(`SELECT record FROM group_proof_records WHERE conv=? AND bootstrap=? ORDER BY seq`, p.Root.ID(), p.Root.Creator.Fingerprint)
	if err != nil {
		return nil, err
	}
	var pages []protocol.GroupJournalPage
	page := protocol.GroupJournalPage{}
	var expected int64
	for rows.Next() {
		var raw []byte
		var record protocol.GroupCommit
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			break
		}
		if record.Seq != expected || record.Seq > p.State.Seq {
			err = ErrGroupInvitationStale
			break
		}
		next := protocol.GroupJournalPage{Records: append(append([]protocol.GroupCommit{}, page.Records...), record)}
		encoded, _ := json.Marshal(next)
		if len(next.Records) > 16 || len(encoded) > protocol.MaxBody-1024 {
			if len(page.Records) == 0 {
				err = errors.New("group: original proof record exceeds carrier bound")
				break
			}
			pages = append(pages, page)
			page = protocol.GroupJournalPage{Records: []protocol.GroupCommit{record}}
			encoded, _ = json.Marshal(page)
			if len(encoded) > protocol.MaxBody-1024 {
				err = errors.New("group: original proof record exceeds carrier bound")
				break
			}
		} else {
			page = next
		}
		expected++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if expected != p.State.Seq+1 {
		return nil, ErrGroupContextPending
	}
	if len(page.Records) > 0 {
		pages = append(pages, page)
	}
	for _, device := range target.Devices {
		key, e := a.sendKey(ctx, device.Address)
		if errors.Is(e, ErrPeerRevoked) { // every device of the person gets the proposal, or none does
			return copies, fmt.Errorf("%w: %s, a device of the invited person, was revoked by a Hub admin; that person takes it off their devices first (agentnet person remove %s), then invite again", e, device.Address, device.Address)
		}
		if e != nil {
			return copies, e
		}
		if key.Fingerprint() != device.Fingerprint() {
			return copies, ErrGroupInvitationStale
		}
		for _, page := range pages {
			last := page.Records[len(page.Records)-1]
			copy, e := a.groupLifecycleCopy(p.Root, envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: last.Seq, Hash: last.Hash}, page, key)
			if e != nil {
				return copies, e
			}
			copies = append(copies, copy)
		}
		copy, e := a.groupLifecycleCopy(p.Root, envelope.SubGroupInvite, protocol.GroupCarrier{V: 1, Seq: p.State.Seq, Hash: p.State.Hash()}, p, key)
		if e != nil {
			return copies, e
		}
		copies = append(copies, copy)
	}
	return copies, nil
}

// This hook handles only exact persisted lifecycle associations. An unrelated
// proof carrier continues through the existing member-only delivery gate.
func (a *Agent) mayDeliverGroupLifecycle(env envelope.Envelope) (bool, bool, error) {
	var sub string
	if err := a.store.db.QueryRow(`SELECT coalesce(sub,'') FROM outbox WHERE id=?`, env.ID).Scan(&sub); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return true, false, err
	}
	if sub != envelope.SubGroupProof && sub != envelope.SubGroupInvite && sub != envelope.SubGroupConsent {
		return false, false, nil
	}
	var id, direction, state, fp, required string
	err := a.store.db.QueryRow(`SELECT c.invitation,c.direction,o.state,coalesce(o.recipient_fp,''),coalesce(o.required_cap,'') FROM group_invitation_copies c JOIN outbox o ON o.id=c.id WHERE c.id=?`, env.ID).Scan(&id, &direction, &state, &fp, &required)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if state != stateQueued {
		return true, false, nil
	}
	if required == protocol.CapGroupInvitationControl {
		key, e := a.sendKey(context.Background(), env.To)
		if e == nil && key.Fingerprint() == fp {
			e = a.requireParticipationCaps(context.Background(), key, required)
		}
		if errors.Is(e, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+e.Error(), "")
		}
		if e != nil {
			return true, false, e
		}
		if key.Fingerprint() != fp {
			return true, false, nil
		}
	}
	r, err := groupInvitationIn(a.store.db, id, direction)
	if err == nil {
		if direction == "out" {
			if r.State == "cancelled" && sub == envelope.SubGroupConsent {
				err = a.groupInvitationOwner(r)
				if err == nil {
					target, ok, e := a.store.personByID(r.Proposal.Target)
					err = e
					if err == nil && (!ok || target.info.State == personConflict || !target.has(env.To, fp)) {
						err = ErrGroupInvitationStale
					}
				}
				if err == nil {
					return true, true, nil
				}
			} else if r.State != "pending" && r.State != "accepted" {
				err = ErrGroupInvitationStale
			} else {
				err = a.ownRecordedGroupInvitation(context.Background(), r)
			}
			if err == nil {
				target, ok, e := a.store.personByID(r.Proposal.Target)
				err = e
				if err == nil && (!ok || !target.has(env.To, fp)) {
					err = ErrGroupInvitationStale
				}
			}
		} else {
			if r.State != "accepted" && r.State != "declined" {
				err = ErrGroupInvitationStale
			}
			if err == nil && (env.To != r.Inviter || fp != r.peerFP) {
				err = ErrGroupInvitationStale
			}
			if err == nil && r.State == "accepted" {
				err = a.verifyGroupInvitation(context.Background(), r.Proposal, r.peerPerson, r.Inviter, r.peerFP)
			}
			if err == nil && r.State == "declined" {
				self, ok, e := a.store.selfPerson(a.Address)
				err = e
				if err == nil && (!ok || self.info.State == personConflict || self.roster.Person != r.Proposal.Target || !self.has(a.Address, a.Self().Fingerprint())) {
					err = ErrGroupInvitationStale
				}
			}
		}
	}
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) || groupLifecycleTransient(err) {
		return true, false, nil
	}
	if err != nil {
		if direction == "out" && errors.Is(err, ErrGroupInvitationStale) {
			a.staleGroupInvitation(r, "'pending','accepted'")
		}
		e := a.store.setOutboxState(env.ID, stateNotDelivered, "group invitation no longer eligible", "")
		if e == nil {
			a.releaseSpool(env)
		}
		return true, false, e
	}
	return true, true, nil
}

func (a *Agent) admitGroupLifecycle(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	var desc protocol.GroupCarrier
	if decodeStrict([]byte(in.Body), &desc) != nil || desc.Validate() != nil || desc.ToKey != a.Self().Fingerprint() {
		return hold(reasonInvalid, "group: lifecycle descriptor targets another key")
	}
	data, err := a.groupCarrierBytes(ctx, in.Attachments[0])
	if err != nil {
		if groupLifecycleTransient(err) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	person, err := a.personOfKey(ctx, env.From, sender)
	if err != nil {
		return err
	}
	var proposal protocol.GroupInvitation
	var consent protocol.GroupConsent
	var existing groupInvitationRow
	if in.Sub == envelope.SubGroupInvite {
		err = decodeGroupCarrierJSON(data, &proposal)
		if err == nil {
			x, _ := json.Marshal(root)
			y, _ := json.Marshal(proposal.Root)
			if !bytes.Equal(x, y) || proposal.Validate() != nil || desc.Seq != proposal.State.Seq || desc.Hash != proposal.State.Hash() {
				err = errors.New("group: invitation descriptor/root differs")
			}
		}
		if err == nil {
			err = a.IngestGroupProofPage(ctx, root, protocol.GroupJournalPage{})
		}
		if err == nil {
			err = a.verifyGroupInvitation(ctx, proposal, person.roster.Person, env.From, sender.Fingerprint())
		}
		if err == nil {
			self, ok, e := a.store.selfPerson(a.Address)
			err = e
			if err == nil && (!ok || self.roster.Person != proposal.Target || self.roster.Hash() != proposal.Roster || !self.has(a.Address, a.Self().Fingerprint())) {
				err = errors.New("group: invitation does not name current own person and key")
			}
		}
	} else {
		err = decodeGroupCarrierJSON(data, &consent)
		if err == nil {
			err = consent.Validate()
		}
		if err == nil && consent.Decision == "cancelled" {
			return a.admitGroupInvitationCancellation(env, in, root, sender, person, consent, desc, fromQuarantine, hold)
		}
		if err == nil {
			existing, err = groupInvitationIn(a.store.db, consent.Invitation, "out")
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("group: consent has no recorded local invitation")
			}
		}
		if err == nil {
			proposal = existing.Proposal
			x, _ := json.Marshal(root)
			y, _ := json.Marshal(proposal.Root)
			if !bytes.Equal(x, y) || person.roster.Person != proposal.Target || (consent.Decision != "declined" && person.roster.Hash() != proposal.Roster) || desc.Seq != proposal.Seq || desc.Hash != proposal.Prev {
				err = errors.New("group: consent is not the exact local invitation and current person")
			}
		}
		if err == nil && consent.Admission != nil {
			if consent.Admission.By != sender.Fingerprint() {
				err = errors.New("group: consent sender differs from admission signer")
			} else {
				err = a.verifyGroupConsent(ctx, proposal, *consent.Admission)
			}
		}
		if err == nil && consent.Decision != "declined" && existing.State == "pending" {
			err = a.ownRecordedGroupInvitation(ctx, existing)
		}
		if err == nil && existing.State != "pending" && existing.State != consent.Decision && existing.State != "published" && !(consent.Decision == "declined" && existing.State == "stale") {
			err = errors.New("group: decision conflicts with recorded local intent")
		}
	}
	if err != nil {
		if existing.ID != "" && errors.Is(err, ErrGroupInvitationStale) {
			if consent.Decision == "accepted" {
				// Signed for a state the group moved past: never rebased,
				// re-issued for a fresh acceptance instead.
				a.staleGroupInvitation(existing, "'pending'")
			} else { // a decline is never answered with another invitation
				a.store.db.Exec(`UPDATE group_invitations SET state='stale' WHERE id=? AND direction='out' AND state='pending'`, existing.ID)
			}
		}
		if errors.Is(err, ErrGroupContextPending) || errors.Is(err, ErrNoPerson) || errors.Is(err, sql.ErrNoRows) {
			return hold(reasonProof, err.Error())
		}
		if groupLifecycleTransient(err) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if in.Sub == envelope.SubGroupInvite {
			raw, _ := json.Marshal(proposal)
			if err := groupInvitationHeadIn(tx, proposal, person.roster.Person, env.From, sender.Fingerprint()); err != nil {
				return err
			}
			if err := groupInvitationTarget(tx, proposal); err != nil {
				return err
			}
			old, e := groupInvitationIn(tx, proposal.ID(), "in")
			if e == nil && (old.peerPerson != person.roster.Person || old.Inviter != env.From || old.peerFP != sender.Fingerprint()) {
				return errors.New("group: conflicting invitation sender")
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			cancelled, e := groupInvitationCancelledIn(tx, proposal.ID(), root.ID(), person.roster.Person, env.From, sender.Fingerprint(), proposal.Seq, proposal.Prev)
			if e != nil {
				return e
			}
			initialState := "pending"
			if cancelled {
				initialState = "cancelled"
			}
			if _, e = tx.Exec(`INSERT INTO group_invitations(id,direction,conv,peer_person,peer_address,peer_fp,payload,state)VALUES(?,'in',?,?,?,?,?,?) ON CONFLICT(id,direction)DO NOTHING`, proposal.ID(), in.Conv, person.roster.Person, env.From, sender.Fingerprint(), raw, initialState); e != nil {
				return e
			}
			// This one is verified at the group's current state: an earlier
			// invitation to this person names a state the group moved past,
			// so it can never be published, even if accepted. Say so.
			older, e := groupInvitationsBy(tx, "in", proposal, func(o protocol.GroupInvitation) bool { return o.State.Seq < proposal.State.Seq })
			if e != nil {
				return e
			}
			for _, id := range older {
				if _, e = tx.Exec(`UPDATE group_invitations SET state='stale' WHERE id=? AND direction='in' AND state IN ('pending','accepted')`, id); e != nil {
					return e
				}
			}
		} else {
			old, e := groupInvitationIn(tx, consent.Invitation, "out")
			if e != nil {
				return e
			}
			// First valid consent wins for a person across their linked devices.
			// Later copies cannot replace its signature or create another join.
			if old.State == "pending" || consent.Decision == "declined" && old.State == "stale" {
				if old.Inviter != a.Address || old.peerFP != a.Self().Fingerprint() {
					return ErrGroupInvitationStale
				}
				if consent.Decision != "declined" {
					if e = groupInvitationHeadIn(tx, proposal, old.peerPerson, old.Inviter, old.peerFP); e != nil {
						return e
					}
				}
				raw, _ := json.Marshal(consent)
				if _, e = tx.Exec(`UPDATE group_invitations SET state=?,consent=? WHERE id=? AND direction='out' AND state IN ('pending','stale')`, consent.Decision, raw, consent.Invitation); e != nil {
					return e
				}
			}
		}
		_, e := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, time.Now().Unix(), in.ID)
		return e
	})
	if err != nil {
		return err
	}
	if res == admitConflict {
		return hold(reasonDuplicate, "group: lifecycle logical conflict")
	}
	if in.Sub == envelope.SubGroupConsent && consent.Decision == "accepted" {
		if e := a.PublishGroupInvitation(ctx, consent.Invitation); e != nil {
			a.Logf("group invitation publication: %v", e)
			a.groupWork.recover.Store(true)
			a.kickNow()
		}
	}
	a.convWork.due(convRetry | convHistory)
	return nil
}
