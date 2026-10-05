package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"time"
)

type groupVisitorTarget struct {
	pid string
	key identity.Public
}

// Compute audience before the transition installs/removes membership. A queued
// exact update may then invalidate the old PID at its isolated visitor host.
func (a *Agent) groupVisitorTargets(conv string) ([]groupVisitorTarget, error) {
	packet, err := groupTurnPacketIn(a.store.db, conv)
	if errors.Is(err, ErrGroupContextPending) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pins, err := a.groupWithdrawals(conv)
	if err != nil {
		return nil, err
	}
	var members []protocol.ConvMember
	for _, member := range packet.State.EffectiveMembers(append(packet.Withdrawals, pins...)) {
		members = append(members, member.ConvMember)
	}
	m, err := memberRowsIn(a.store.db, packet.Root, members)
	if err != nil {
		return nil, err
	}
	m.group = &packet
	ids, err := a.store.participationIDs(conv)
	if err != nil {
		return nil, err
	}
	var out []groupVisitorTarget
	for _, pid := range ids {
		events, e := participationEventsIn(a.store.db, conv, pid)
		if e != nil {
			return nil, e
		}
		if e = m.loadHosts(a.store.db, events); e != nil {
			return nil, e
		}
		info := resolve(conv, pid, events, m)
		if !info.External || info.Invite == "" || info.State != PartInvited && info.State != PartActive {
			continue
		}
		p, ok := m.hosts[info.Host.Person]
		if !ok {
			continue
		}
		key, ok := p.device(info.Host.Address)
		if !ok || key.Fingerprint() != info.Host.Fingerprint || key.Address == a.Address {
			continue
		}
		out = append(out, groupVisitorTarget{pid, key})
	}
	return out, nil
}

// Only a locally known, signed exact invite supplies disclosure authorization.
// Current-member/worker usability is checked separately; ended epochs may still
// receive an already-queued authority update, never room contents.
func groupVisitorInvite(q dbq, root protocol.ConvRoot, pid, address, fp string) (protocol.ParticipationEvent, error) {
	events, err := participationEventsIn(q, root.ID(), pid)
	if err != nil {
		return protocol.ParticipationEvent{}, err
	}
	var found *protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Type != protocol.EventInvite || ev.Group == nil || ev.Group.HostRole != "visitor" || ev.Host == nil || ev.Host.Address != address || ev.Host.Fingerprint != fp {
			continue
		}
		p, ok, e := personByIDIn(q, ev.Author.Person)
		if e != nil {
			return ev, e
		}
		if !ok || p.info.State == personConflict {
			return ev, ErrGroupContextPending
		}
		var raw []byte
		if e = q.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, ev.Author.Person, ev.Author.Roster).Scan(&raw); errors.Is(e, sql.ErrNoRows) {
			return ev, ErrGroupContextPending
		} else if e != nil {
			return ev, e
		}
		var roster protocol.PersonRoster
		if e = json.Unmarshal(raw, &roster); e != nil {
			return ev, e
		}
		key, ok := roster.Device(ev.Author.Fingerprint)
		if !ok || key.Address != ev.Author.Address || ev.Verify(key.SignKey) != nil {
			return ev, errors.New("group: visitor invitation author proof invalid")
		}
		if found != nil && found.Hash() != ev.Hash() {
			return ev, errors.New("group: conflicting visitor invitations")
		}
		copy := ev
		found = &copy
	}
	if found == nil {
		return protocol.ParticipationEvent{}, ErrGroupContextPending
	}
	host, ok, err := personByIDIn(q, found.Host.Person)
	if err != nil {
		return *found, err
	}
	if !ok || host.info.State == personConflict || !host.has(address, fp) {
		return *found, errors.New("group: exact visitor host no longer pinned")
	}
	return *found, nil
}
func groupVisitorCarrierDestination(q dbq, packet GroupContext, pid, address, fp string) error {
	inv, err := groupVisitorInvite(q, packet.Root, pid, address, fp)
	if err != nil {
		return err
	}
	original, err := groupProofRecord(q, packet.State.Conv, packet.Root.Creator.Fingerprint, inv.Group.Seq)
	if err != nil {
		return err
	}
	if original.Hash != inv.Group.Hash {
		return errors.New("group: visitor invitation original proof differs")
	}
	current, err := groupProofRecord(q, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		return err
	}
	if !current.Matches(packet.State) {
		return errors.New("group: visitor context has no matching original record")
	}
	var head sql.NullInt64
	if err = q.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&head); err != nil {
		return err
	}
	if !head.Valid || head.Int64 != packet.State.Seq {
		return ErrGroupContextPending
	}
	var seq int64
	var hash string
	err = q.QueryRow(`SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?`, packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (seq > packet.State.Seq || seq == packet.State.Seq && hash != packet.State.Hash()) {
		return ErrGroupContextPending
	}
	return nil
}

// Bootstrap records a verified signed invitation as PENDING evidence only.
// It never installs context, membership, grants, or executable work.
func (a *Agent) admitGroupVisitorInvite(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, me, sp personRow, sender identity.Public, fromQuarantine bool, hold func(string, string) error) (bool, error) {
	if in.Sub != envelope.SubEvent {
		return false, nil
	}
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	if err != nil {
		return true, hold(reasonInvalid, err.Error())
	}
	if ev.Type != protocol.EventInvite || ev.Group == nil || ev.Group.HostRole != "visitor" || ev.Host == nil || ev.Host.Address != a.Address || ev.Host.Fingerprint != a.Self().Fingerprint() || ev.Host.Person != me.info.Person {
		return false, nil
	}
	ev, err = checkParticipationEvent(in, sender.Fingerprint(), sender.SignKey)
	if err != nil {
		return true, hold(reasonInvalid, err.Error())
	}
	if ev.Author.Person != sp.info.Person || !sp.has(env.From, sender.Fingerprint()) {
		return true, hold(reasonInvalid, "group: visitor inviter exact person differs")
	}
	realm, err := a.RealmID()
	if err != nil {
		return true, err
	}
	if root.Realm != realm {
		return true, hold(reasonInvalid, "group: foreign visitor invitation")
	}
	if err = a.verifyRoot(ctx, root, sp); err != nil {
		return true, err
	}
	if err = a.IngestGroupProofPage(ctx, root, protocol.GroupJournalPage{}); err != nil {
		return true, err
	}
	if err = a.store.addConversation(root, in.Root, ""); err != nil {
		return true, err
	}
	result, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error { return insertParticipationEvent(tx, ev, []byte(in.Body)) })
	if err == nil && result == admitConflict {
		return true, hold(reasonDuplicate, "group: visitor invitation logical conflict")
	}
	if err == nil {
		a.convWork.due(convRetry)
		a.kickNow()
		a.wakeWorker()
	}
	return true, err
}

func (a *Agent) acceptGroupVisitorContext(ctx context.Context, packet GroupContext, in envelope.Inner, sender identity.Public) error {
	if len(packet.Proof) != 0 {
		return errors.New("group: visitor context contains old plaintext states")
	}
	if _, err := groupVisitorInvite(a.store.db, packet.Root, in.PID, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if packet.Root.Realm != realm || packet.State.Realm != realm {
		return errors.New("group: foreign visitor context")
	}
	root, err := a.groupProofRoot(packet.State.Conv)
	if err != nil {
		return err
	}
	if !sameGroupRoot(root, packet.Root) {
		return errors.New("group: visitor original root differs")
	}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return err
	}
	withdrawals, pins, err := a.groupPacketWithdrawals(ctx, packet, resolve)
	if err != nil {
		return err
	}
	if err = a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins); err != nil {
		return err
	}
	// Sender is a current effective member or the original state-authorized
	// departing administrator; its current roster key is still independently pinned.
	p, ok, err := a.store.personByAddress(sender.Address)
	if err != nil {
		return err
	}
	if !ok || !p.has(sender.Address, sender.Fingerprint()) {
		return ErrGroupContextPending
	}
	checkSender := func(q dbq) error {
		current, present, e := personByIDIn(q, p.info.Person)
		if e != nil {
			return e
		}
		if !present || current.info.State == personConflict || !current.has(sender.Address, sender.Fingerprint()) || current.info.Roster != p.info.Roster {
			return ErrGroupContextPending
		}
		if _, e = groupMemberAdmission(q, packet, sender.Address, sender.Fingerprint()); e == nil {
			return nil
		}
		if packet.State.Actor == current.info.Person && packet.State.By == sender.Fingerprint() && packet.State.ActorRoster == current.info.Roster {
			return nil // exact original departing administrator's signed state
		}
		for _, w := range withdrawals {
			if w.Person == current.info.Person && w.By == sender.Fingerprint() && w.Roster == current.info.Roster && groupWithdrawalSource(q, packet, w) == nil {
				return nil // exact verified ordinary departure overlay, no room grant
			}
		}
		return e
	}
	if err = checkSender(a.store.db); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkSender(tx); err != nil {
		return err
	}
	if err = groupVisitorCarrierDestination(tx, packet, in.PID, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	if err = checkGroupWithdrawalPromotion(tx, packet, withdrawals); err != nil {
		return err
	}
	for _, w := range withdrawals {
		if m, ok := packet.State.Member(w.Person); ok && !m.Admin && m.Admission.Hash() == w.Admission {
			if err = pinGroupWithdrawal(tx, w); err != nil {
				return err
			}
		}
	}
	if err = installGroupContext(tx, packet); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.convWork.due(convRetry | convRelease)
	a.kickNow()
	a.wakeWorker()
	return nil
}

// Proof/context need only exact pending invitation author binding. The full
// current context verifier supplies all membership/epoch authority afterwards.
func (a *Agent) admitGroupVisitorCarrier(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	inv, err := groupVisitorInvite(a.store.db, root, in.PID, a.Address, a.Self().Fingerprint())
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	// Bootstrap proof must come from the exact inviter. Later valid context
	// updates can come from a verified member/admin once context is held.
	if sender.Address != inv.Author.Address || sender.Fingerprint() != inv.Author.Fingerprint {
		packet, e := groupTurnPacketIn(a.store.db, in.Conv)
		if e != nil {
			return hold(reasonProof, e.Error())
		}
		// The public proof page may already have advanced the head. The old
		// verified disclosure audience authorizes only carrier transport; the
		// new context verifier below supplies current membership authority.
		p, present, e := a.store.personByAddress(sender.Address)
		if e != nil {
			return e
		}
		member, memberPresent := packet.State.Member(p.info.Person)
		pins, e := a.groupWithdrawals(in.Conv)
		if e != nil {
			return e
		}
		if !present || p.info.State == personConflict || !p.has(sender.Address, sender.Fingerprint()) || !memberPresent || packet.State.Withdrawn(member, append(packet.Withdrawals, pins...)) {
			return hold(reasonInvalid, "group: carrier author is not the verified prior disclosure audience")
		}
	}
	var desc protocol.GroupCarrier
	if decodeStrict([]byte(in.Body), &desc) != nil || desc.Validate() != nil || desc.ToKey != a.Self().Fingerprint() {
		return hold(reasonInvalid, "group: visitor carrier targets another exact key")
	}
	data, err := a.groupCarrierBytes(ctx, in.Attachments[0])
	if err != nil {
		return err
	}
	if in.Sub == envelope.SubGroupProof {
		var page protocol.GroupJournalPage
		if decodeGroupCarrierJSON(data, &page) != nil || len(page.Records) == 0 {
			return hold(reasonInvalid, "group: malformed visitor public proof")
		}
		last := page.Records[len(page.Records)-1]
		if last.Seq != desc.Seq || last.Hash != desc.Hash {
			return hold(reasonInvalid, "group: visitor public proof descriptor differs")
		}
		var head sql.NullInt64
		err = a.store.db.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, in.Conv, root.Creator.Fingerprint).Scan(&head)
		next := int64(0)
		if head.Valid {
			next = head.Int64 + 1
		}
		if err == nil && page.Records[0].Seq > next {
			err = ErrGroupContextPending
		}
		if err == nil {
			err = a.IngestGroupProofPage(ctx, root, page)
		}
	} else {
		var packet GroupContext
		if decodeGroupCarrierJSON(data, &packet) != nil || !sameGroupRoot(root, packet.Root) || packet.State.Seq != desc.Seq || packet.State.Hash() != desc.Hash {
			return hold(reasonInvalid, "group: visitor context descriptor differs")
		}
		err = a.acceptGroupVisitorContext(ctx, packet, in, sender)
	}
	if err != nil {
		if errors.Is(err, ErrGroupContextPending) || errors.Is(err, ErrNoPerson) {
			return hold(reasonProof, err.Error())
		}
		if groupLifecycleTransient(err) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	result, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, time.Now().Unix(), in.ID)
		return e
	})
	if err == nil && result == admitConflict {
		return hold(reasonDuplicate, "group: visitor carrier logical conflict")
	}
	if err == nil {
		a.convWork.due(convRetry)
		a.kickNow()
		a.wakeWorker()
	}
	return err
}
