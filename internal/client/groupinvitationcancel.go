package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Appended migration: a cancellation may arrive before its invitation. It is
// only a tombstone for this exact signed sender, never membership authority.
const groupInvitationCancellationSchema = `CREATE TABLE group_invitation_cancellations(id TEXT NOT NULL, conv TEXT NOT NULL, peer_person TEXT NOT NULL, peer_address TEXT NOT NULL, peer_fp TEXT NOT NULL, seq INTEGER NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(id,peer_address,peer_fp)); CREATE TABLE group_invitation_refreshes(id TEXT PRIMARY KEY, successor TEXT NOT NULL);`

// RefreshGroupInvitation is an explicit new proposal, never a rebased consent.
func (a *Agent) RefreshGroupInvitation(ctx context.Context, id string) (GroupInvitationInfo, error) {
	release, err := lockfile.Wait(filepath.Join(a.home, "group-invitation-refresh.lock"))
	if err != nil {
		return GroupInvitationInfo{}, err
	}
	defer release()
	var successor string
	err = a.store.db.QueryRow(`SELECT successor FROM group_invitation_refreshes WHERE id=?`, id).Scan(&successor)
	if err == nil {
		r, e := groupInvitationIn(a.store.db, successor, "out")
		if e != nil {
			return GroupInvitationInfo{}, e
		}
		if e = a.groupInvitationOwner(r); e != nil {
			return GroupInvitationInfo{}, e
		}
		r.CanCancel, r.CanRefresh = a.groupInvitationCapabilities(r)
		return r.GroupInvitationInfo, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GroupInvitationInfo{}, err
	}

	r, err := groupInvitationIn(a.store.db, id, "out")
	if err != nil {
		return GroupInvitationInfo{}, err
	}
	if err = a.groupInvitationOwner(r); err != nil {
		return GroupInvitationInfo{}, err
	}
	if r.State == "published" || r.State == "published-history-unavailable" {
		return GroupInvitationInfo{}, errors.New("group: membership already published")
	}
	if err = a.CancelGroupInvitation(ctx, id); err != nil {
		return GroupInvitationInfo{}, err
	}
	fresh, err := a.inviteGroup(ctx, r.Proposal.Root.ID(), r.Proposal.Target, r.Proposal.History, id)
	return fresh, err
}

// CancelGroupInvitation fences this device's unpublished intent and queues a
// signed exact-ID notice atomically. It never removes an existing member.
func (a *Agent) CancelGroupInvitation(ctx context.Context, id string) error {
	defer notifyDaemon(a.home)
	release, err := lockfile.Wait(filepath.Join(a.home, "group-lifecycle.lock"))
	if err != nil {
		return err
	}
	defer release()
	r, err := groupInvitationIn(a.store.db, id, "out")
	if err != nil {
		return err
	}
	if err = a.groupInvitationOwner(r); err != nil {
		return err
	}
	if r.State == "cancelled" {
		return nil
	}
	if r.State != "pending" && r.State != "accepted" && r.State != "reissue" && r.State != "stale" {
		return errors.New("group: invitation cannot be cancelled after publication or decline")
	}
	current, err := a.GroupContext(r.Proposal.Root.ID())
	if err != nil {
		return err
	}
	admin, ok := current.State.Member(r.peerPerson)
	if !ok || !admin.Admin || current.State.Withdrawn(admin, current.Withdrawals) {
		return errors.New("group: current inviting administrator required")
	}
	// A journal submission may already have custody even before local install.
	// Never report a cancellation while that exact admission can still publish.
	if err = groupInvitationPublicationFence(a.store.db, r.Proposal); err != nil {
		return err
	}
	target, ok, err := a.store.personByID(r.Proposal.Target)
	if err != nil {
		return err
	}
	if !ok || target.info.State == personConflict {
		return ErrGroupContextPending
	}
	spoolRelease, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return err
	}
	defer spoolRelease()
	var copies []outCopy
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	c := protocol.GroupConsent{V: 1, Invitation: id, Decision: "cancelled"}
	for _, device := range target.roster.Devices {
		key, e := a.sendKey(ctx, device.Address)
		if e != nil {
			return e
		}
		if key.Fingerprint() != device.Fingerprint() {
			return ErrGroupInvitationStale
		}
		copy, e := a.groupLifecycleCopy(r.Proposal.Root, envelope.SubGroupConsent, protocol.GroupCarrier{V: 1, Seq: r.Proposal.Seq, Hash: r.Proposal.Prev}, c, key)
		if e != nil {
			return e
		}
		copies = append(copies, copy)
	}
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		old, e := groupInvitationIn(tx, id, "out")
		if e != nil {
			return e
		}
		if old.State != r.State {
			return errors.New("group: invitation changed while cancelling")
		}
		if e = groupInvitationPublicationFence(tx, r.Proposal); e != nil {
			return e
		}
		if e = groupTurnCheck(tx, current, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		if _, e = tx.Exec(`UPDATE group_invitations SET state='cancelled' WHERE id=? AND direction='out'`, id); e != nil {
			return e
		}
		return associateGroupInvitationCopies(tx, id, "out", copies)
	}, "")
	if err != nil {
		return err
	}
	stored = true
	a.kickNow()
	return nil
}

func groupInvitationCancelledIn(q dbq, id, conv, person, address, fp string, seq int64, hash string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM group_invitation_cancellations WHERE id=? AND conv=? AND peer_person=? AND peer_address=? AND peer_fp=? AND seq=? AND hash=?`, id, conv, person, address, fp, seq, hash).Scan(&n)
	return n != 0, err
}

func (a *Agent) admitGroupInvitationCancellation(env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, person personRow, c protocol.GroupConsent, desc protocol.GroupCarrier, fromQuarantine bool, hold func(string, string) error) error {
	realm, realmErr := a.RealmID()
	if realmErr != nil {
		return realmErr
	}
	if realm != root.Realm {
		return hold(reasonInvalid, "group: foreign cancellation realm")
	}

	if c.Decision != "cancelled" || c.Admission != nil {
		return hold(reasonInvalid, "group: invalid invitation cancellation")
	}
	// Existing invitations bind sender and descriptor exactly. A notice received
	// first remains inert until a matching verified proposal is admitted.
	row, err := groupInvitationIn(a.store.db, c.Invitation, "in")
	if err == nil && (row.Inviter != env.From || row.peerFP != sender.Fingerprint() || row.peerPerson != person.roster.Person || row.Proposal.Root.ID() != root.ID() || desc.Seq != row.Proposal.Seq || desc.Hash != row.Proposal.Prev) {
		return hold(reasonInvalid, "group: cancellation is not from the exact inviting device")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`INSERT INTO group_invitation_cancellations(id,conv,peer_person,peer_address,peer_fp,seq,hash) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, c.Invitation, root.ID(), person.roster.Person, env.From, sender.Fingerprint(), desc.Seq, desc.Hash); e != nil {
			return e
		}
		_, e := tx.Exec(`UPDATE group_invitations SET state='cancelled' WHERE id=? AND direction='in' AND peer_person=? AND peer_address=? AND peer_fp=? AND state IN ('pending','accepted','stale')`, c.Invitation, person.roster.Person, env.From, sender.Fingerprint())
		return e
	})
	return err
}

func groupInvitationPublicationFence(q dbq, p protocol.GroupInvitation) error {
	var raw []byte
	err := q.QueryRow(`SELECT payload FROM group_publications WHERE conv=? AND seq=?`, p.Root.ID(), p.Seq).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var packet GroupContext
	if json.Unmarshal(raw, &packet) != nil {
		return errors.New("group: publication record requires recovery before cancellation")
	}
	if member, ok := packet.State.Member(p.Target); ok && member.Admission.Seq == p.Seq && member.Admission.Prev == p.Prev && member.Admission.Roster == p.Roster {
		return errors.New("group: publication already started; resolve membership before cancelling")
	}
	return nil
}

func (a *Agent) groupInvitationCapabilities(r groupInvitationRow) (bool, bool) {
	if r.Direction != "out" || a.groupInvitationOwner(r) != nil {
		return false, false
	}
	if r.State != "pending" && r.State != "accepted" && r.State != "stale" && r.State != "reissue" && r.State != "cancelled" {
		return false, false
	}
	current, err := a.GroupContext(r.Proposal.Root.ID())
	if err != nil || groupTurnCheck(a.store.db, current, a.Address, a.Self().Fingerprint()) != nil {
		return false, false
	}
	admin, ok := current.State.Member(r.peerPerson)
	if !ok || !admin.Admin || current.State.Withdrawn(admin, current.Withdrawals) {
		return false, false
	}
	if member, ok := current.State.Member(r.Proposal.Target); ok && !current.State.Withdrawn(member, current.Withdrawals) {
		return false, false
	}
	if groupInvitationPublicationFence(a.store.db, r.Proposal) != nil {
		return false, false
	}
	return r.State != "cancelled", true
}

// Repeated Invite at the same exact live state is one request. A terminal
// cancellation/decline cannot be reopened: the next request gets a new nonce.
func (a *Agent) reusableGroupInvitation(p protocol.GroupInvitation) (groupInvitationRow, bool, error) {
	ids, err := groupInvitationsBy(a.store.db, "out", p, func(other protocol.GroupInvitation) bool {
		other.Nonce = ""
		want := p
		want.Nonce = ""
		return other.ID() == want.ID()
	})
	if err != nil {
		return groupInvitationRow{}, false, err
	}
	for _, id := range ids {
		row, e := groupInvitationIn(a.store.db, id, "out")
		if e != nil {
			return row, false, e
		}
		if (row.State == "pending" || row.State == "accepted") && a.groupInvitationOwner(row) == nil {
			return row, true, nil
		}
	}
	return groupInvitationRow{}, false, nil
}
