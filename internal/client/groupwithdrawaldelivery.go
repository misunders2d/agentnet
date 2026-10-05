package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupWithdrawalHash(w protocol.GroupWithdrawal) string {
	sum := sha256.Sum256(w.Canonical())
	return hex.EncodeToString(sum[:])
}

// Only the local decision omits network refresh. Every original authority slot,
// signed current state, local roster/key and known head must already be pinned.
// Missing proof never turns into an offline membership assumption.
func (a *Agent) localWithdrawalContext(ctx context.Context, conv string) (GroupContext, error) {
	packet, err := a.GroupContext(conv)
	if err != nil {
		return packet, err
	}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return packet, err
	}
	pins, err := a.groupWithdrawals(conv)
	if err != nil {
		return packet, err
	}
	for _, w := range packet.Withdrawals {
		if !groupWithdrawalPinned(pins, w) {
			return packet, ErrGroupContextPending
		}
	}
	withdrawals := append(append([]protocol.GroupWithdrawal{}, packet.Withdrawals...), pins...)
	if err = a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins); err != nil {
		return packet, err
	}
	// Public prefix records are inserted only by the existing chain verifier.
	var n, min, max int64
	if err = a.store.db.QueryRow(`SELECT count(*),coalesce(min(seq),-1),coalesce(max(seq),-1) FROM group_proof_records WHERE conv=? AND bootstrap=?`, conv, packet.Root.Creator.Fingerprint).Scan(&n, &min, &max); err != nil {
		return packet, err
	}
	if min != 0 || max != packet.State.Seq || n != packet.State.Seq+1 {
		return packet, ErrGroupContextPending
	}
	return packet, nil
}

func groupWithdrawalSource(q dbq, packet GroupContext, w protocol.GroupWithdrawal) error {
	if w.Conv != packet.Root.ID() || w.Realm != packet.State.Realm {
		return errors.New("group: withdrawal differs from current verified root")
	}
	person, ok, err := personByIDIn(q, w.Person)
	if err != nil {
		return err
	}
	if !ok || person.info.State == personConflict || person.roster.Hash() != w.Roster {
		return errors.New("group: withdrawal signing roster changed or conflicted")
	}
	if err = w.Verify(packet.State, func(personID, hash string) (protocol.PersonRoster, bool) {
		if personID == w.Person && hash == w.Roster {
			return person.roster, true
		}
		return protocol.PersonRoster{}, false
	}); err != nil {
		return err
	}
	return nil
}

func groupWithdrawalDestination(q dbq, packet GroupContext, w protocol.GroupWithdrawal, address, fp, epoch string) error {
	current, err := groupTurnPacketIn(q, w.Conv)
	if err != nil {
		return err
	}
	if !sameGroupRoot(current.Root, packet.Root) || current.State.Hash() != packet.State.Hash() {
		return ErrGroupContextPending
	}
	if err = groupWithdrawalSource(q, current, w); err != nil {
		return err
	}
	for _, m := range current.State.Members {
		p, ok, e := personByIDIn(q, m.Person)
		if e != nil {
			return e
		}
		if !ok || !p.has(address, fp) {
			continue
		}
		if p.info.State == personConflict {
			return errPersonConflict
		}
		if epoch == "" || m.Admission.Hash() != epoch {
			return errors.New("group: departure recipient admission changed")
		}
		if m.Person == w.Person {
			// A linked device of the departing person may receive ONLY that exact
			// departure even though the shared person's overlay is already withdrawn.
			if m.Admin || m.Admission.Hash() != w.Admission {
				return errors.New("group: departure conflicts with current own admission")
			}
			var head sql.NullInt64
			if e = q.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, w.Conv, current.Root.Creator.Fingerprint).Scan(&head); e != nil {
				return e
			}
			if !head.Valid || head.Int64 != current.State.Seq {
				return ErrGroupContextPending
			}
			var seq int64
			var hash string
			e = q.QueryRow(`SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?`, w.Conv, current.Root.Creator.Fingerprint).Scan(&seq, &hash)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil && (seq > current.State.Seq || seq == current.State.Seq && hash != current.State.Hash()) {
				return ErrGroupContextPending
			}
			return nil
		}
		return groupDeliveryRecipient(q, current, address, fp)
	}
	return errors.New("group: departure recipient key is not a current member device")
}

var errGroupWithdrawalQueued = errors.New("group: exact departure fanout already queued")

func (a *Agent) enqueueGroupWithdrawal(ctx context.Context, packet GroupContext, w protocol.GroupWithdrawal) error {
	if w.By != a.Self().Fingerprint() {
		return errors.New("group: only this signing session may enqueue its own departure")
	}
	if err := groupWithdrawalSource(a.store.db, packet, w); err != nil {
		return err
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return err
	}
	defer release()
	var copies []outCopy
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	// Capture disclosure recipients before the departure pin ends its PID.
	// These copies share the original pin+fanout transaction and survive restart.
	visitors, err := a.groupVisitorTargets(w.Conv)
	if err != nil {
		return err
	}
	visitorPacket := packet
	visitorPacket.Proof = nil
	visitorPacket.Memberships = nil // never other agents' private original invitations
	pins, err := a.groupWithdrawals(w.Conv)
	if err != nil {
		return err
	}
	visitorPacket.Withdrawals = append(append(append([]protocol.GroupWithdrawal{}, packet.Withdrawals...), pins...), w)
	for _, target := range visitors {
		// Reuse this captured departure disclosure on recovery; its eligibility
		// still depends on the current invite authority. This private context marker
		// binds only its exact withdrawal, not a human recipient admission.
		var existing int
		if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub=? AND pid=? AND recipient=? AND recipient_fp=? AND group_admission=?`, w.Conv, envelope.SubGroupContext, target.pid, target.key.Address, target.key.Fingerprint(), groupWithdrawalHash(w)).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			continue
		}
		if err = groupVisitorCarrierDestination(a.store.db, visitorPacket, target.pid, target.key.Address, target.key.Fingerprint()); err != nil {
			return err
		}
		copy, e := a.groupLifecycleCopy(packet.Root, envelope.SubGroupContext, protocol.GroupCarrier{V: 1, Seq: packet.State.Seq, Hash: packet.State.Hash()}, visitorPacket, target.key, target.pid)
		if e != nil {
			return e
		}
		copy.groupAdmission = groupWithdrawalHash(w)
		copies = append(copies, copy)
	}
	for _, m := range packet.State.Members {
		person, ok, e := a.store.personByID(m.Person)
		if e != nil {
			return e
		}
		if !ok {
			return ErrGroupContextPending
		}
		for _, key := range person.roster.Devices {
			if key.Address == a.Address {
				continue
			}
			if e = groupWithdrawalDestination(a.store.db, packet, w, key.Address, key.Fingerprint(), m.Admission.Hash()); e != nil {
				if errors.Is(e, ErrGroupContextPending) || errors.Is(e, errPersonConflict) {
					return e
				}
				// Another already departed person is not a fanout destination.
				if m.Person != w.Person && packet.State.Withdrawn(m, packet.Withdrawals) {
					continue
				}
				pins, e2 := a.groupWithdrawals(w.Conv)
				if e2 != nil {
					return e2
				}
				if m.Person != w.Person && packet.State.Withdrawn(m, pins) {
					continue
				}
				return e
			}
			var count int
			if e = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub=? AND recipient=? AND recipient_fp=? AND group_admission=? AND json_extract(body,'$.hash')=?`, w.Conv, envelope.SubGroupWithdrawal, key.Address, key.Fingerprint(), m.Admission.Hash(), groupWithdrawalHash(w)).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				continue
			}
			copy, e := a.groupLifecycleCopy(packet.Root, envelope.SubGroupWithdrawal, protocol.GroupCarrier{V: 1, Seq: packet.State.Seq, Hash: groupWithdrawalHash(w)}, w, key)
			if e != nil {
				return e
			}
			copy.groupAdmission = m.Admission.Hash()
			copies = append(copies, copy)
		}
	}
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		current, e := groupTurnPacketIn(tx, w.Conv)
		if e != nil {
			return e
		}
		if current.State.Hash() != packet.State.Hash() || !sameGroupRoot(current.Root, packet.Root) {
			return ErrGroupContextPending
		}
		if e = groupWithdrawalSource(tx, current, w); e != nil {
			return e
		}
		if e = pinGroupWithdrawal(tx, w); e != nil {
			return e
		}
		for _, copy := range copies {
			if copy.in.PID != "" {
				if e = groupVisitorCarrierDestination(tx, visitorPacket, copy.in.PID, copy.env.To, copy.recipientFP); e != nil {
					return e
				}
				continue
			}
			if e = groupWithdrawalDestination(tx, current, w, copy.env.To, copy.recipientFP, copy.groupAdmission); e != nil {
				return e
			}
			var count int
			if e = tx.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub=? AND recipient=? AND recipient_fp=? AND group_admission=? AND json_extract(body,'$.hash')=?`, w.Conv, envelope.SubGroupWithdrawal, copy.env.To, copy.recipientFP, copy.groupAdmission, groupWithdrawalHash(w)).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				return errGroupWithdrawalQueued
			}
		}
		return nil
	}, "")
	if errors.Is(err, errGroupWithdrawalQueued) {
		return nil
	}
	if err != nil {
		return err
	}
	stored = true
	a.convWork.due(convRelease | convRetry)
	a.changes.bump()
	a.kickNow()
	notifyDaemon(a.home)
	return nil
}

// Recover only departures signed by this exact local key. Receiving somebody
// else's quiet pin does not authorize this device to broadcast it or run work.
func (a *Agent) RecoverGroupWithdrawals(ctx context.Context) error {
	rows, err := a.store.db.Query(`SELECT record FROM group_withdrawals`)
	if err != nil {
		return err
	}
	var own []protocol.GroupWithdrawal
	for rows.Next() {
		var raw []byte
		var w protocol.GroupWithdrawal
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &w); err != nil {
			break
		}
		if w.By == a.Self().Fingerprint() {
			own = append(own, w)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, w := range own {
		packet, e := a.localWithdrawalContext(ctx, w.Conv)
		if e != nil {
			return e
		}
		m, ok := packet.State.Member(w.Person)
		if !ok || m.Admission.Hash() != w.Admission {
			continue
		} // already folded, never revive an old epoch
		if e = a.latestGroupWithdrawal(ctx, w); e != nil {
			return e
		}
		for _, member := range packet.State.Members {
			if _, e = a.refreshPerson(ctx, member.Person, false); e != nil {
				return e
			}
		}
		if e = a.enqueueGroupWithdrawal(ctx, packet, w); e != nil {
			return e
		}
	}
	return nil
}

func (a *Agent) mayDeliverGroupWithdrawal(env envelope.Envelope) (bool, bool, error) {
	var sub, state, body, conv, fp, epoch string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),state,body,conv,coalesce(recipient_fp,''),coalesce(group_admission,'') FROM outbox WHERE id=?`, env.ID).Scan(&sub, &state, &body, &conv, &fp, &epoch)
	if err != nil || sub != envelope.SubGroupWithdrawal {
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	var descriptor protocol.GroupCarrier
	if err = decodeStrict([]byte(body), &descriptor); err == nil {
		err = descriptor.Validate()
	}
	var w protocol.GroupWithdrawal
	found := false
	pins, e := a.groupWithdrawals(conv)
	if err == nil {
		err = e
	}
	if err == nil {
		for _, pin := range pins {
			if pin.By == a.Self().Fingerprint() && groupWithdrawalHash(pin) == descriptor.Hash {
				w = pin
				found = true
				break
			}
		}
		if !found {
			err = errors.New("group: departure lacks exact local signed pin")
		}
	}
	packet, e := a.GroupContext(conv)
	if err == nil {
		err = e
	}
	if err == nil && (descriptor.ToKey != fp || descriptor.Seq > packet.State.Seq) {
		err = errors.New("group: departure descriptor differs")
	}
	if err == nil {
		err = groupWithdrawalDestination(a.store.db, packet, w, env.To, fp, epoch)
	}
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		e = a.store.setOutboxState(env.ID, stateNotDelivered, "departure recipient or original admission no longer eligible", "")
		if e == nil {
			a.releaseSpool(env)
		}
		return true, false, e
	}
	return true, true, nil
}

func (a *Agent) admitGroupWithdrawalCarrier(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	var descriptor protocol.GroupCarrier
	if decodeStrict([]byte(in.Body), &descriptor) != nil || descriptor.Validate() != nil || descriptor.ToKey != a.Self().Fingerprint() {
		return hold(reasonInvalid, "group: departure descriptor targets another key")
	}
	data, err := a.groupCarrierBytes(ctx, in.Attachments[0])
	if err != nil {
		return err
	}
	var w protocol.GroupWithdrawal
	if decodeGroupCarrierJSON(data, &w) != nil || groupWithdrawalHash(w) != descriptor.Hash || w.Conv != in.Conv || w.Realm != root.Realm || w.By != sender.Fingerprint() {
		return hold(reasonInvalid, "group: departure descriptor/signing author differs")
	}
	if err = a.latestGroupWithdrawal(ctx, w); err != nil {
		if groupLifecycleTransient(err) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	person, ok, err := a.store.personByID(w.Person)
	if err != nil {
		return err
	}
	if !ok || !person.has(env.From, sender.Fingerprint()) {
		return hold(reasonInvalid, "group: departure sender is not its exact signing person")
	}
	packet, err := a.GroupContext(in.Conv)
	if err == nil && !sameGroupRoot(root, packet.Root) {
		return hold(reasonInvalid, "group: departure differs from durable original root")
	}
	if err == nil && descriptor.Seq > packet.State.Seq {
		err = ErrGroupContextPending
	}
	if errors.Is(err, ErrGroupContextPending) {
		// A candidate arriving before its original current context cannot pin
		// ordinary leave against an older role; the existing pending table and
		// quarantine retry own promotion/admission resolution.
		if e := a.deferGroupWithdrawal(w); e != nil {
			return e
		}
		return hold(reasonProof, ErrGroupContextPending.Error())
	}
	if err != nil {
		return err
	}
	if member, ok := packet.State.Member(w.Person); ok && member.Admin && member.Admission.Hash() == w.Admission {
		// Preserve an exact conflict for explicit resolution; it never becomes
		// an administrator departure or installs a withdrawal overlay.
		if e := a.deferGroupWithdrawal(w); e != nil {
			return e
		}
		return hold(reasonInvalid, "group: departure conflicts with current promoted administrator")
	}
	for _, m := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, m.Person, false); err != nil {
			return err
		}
	}
	epoch := ""
	for _, m := range packet.State.Members {
		p, yes, e := a.store.personByID(m.Person)
		if e != nil {
			return e
		}
		if yes && p.has(a.Address, a.Self().Fingerprint()) {
			epoch = m.Admission.Hash()
			break
		}
	}
	if err = groupWithdrawalDestination(a.store.db, packet, w, a.Address, a.Self().Fingerprint(), epoch); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	if err = a.AcceptGroupWithdrawal(ctx, w); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		if groupLifecycleTransient(err) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if e := groupWithdrawalDestination(tx, packet, w, a.Address, a.Self().Fingerprint(), epoch); e != nil {
			return e
		}
		_, e := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, time.Now().Unix(), in.ID)
		return e
	})
	if err == nil && res == admitConflict {
		return hold(reasonDuplicate, "group: conflicting departure logical carrier")
	}
	if err == nil {
		a.convWork.due(convRetry | convRelease)
		a.changes.bump()
		a.kickNow()
	}
	return err
}
