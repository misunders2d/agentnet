package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// GroupLifecycleSchema is a new appended store step. Invitations contain
// current signed public metadata and selected refs, never historical plaintext.
// Copies are associated with exact local intent in the enqueue transaction.
const GroupLifecycleSchema = `
CREATE TABLE group_invitations(id TEXT NOT NULL, direction TEXT NOT NULL, conv TEXT NOT NULL, peer_person TEXT NOT NULL, peer_address TEXT NOT NULL, peer_fp TEXT NOT NULL, payload BLOB NOT NULL, state TEXT NOT NULL, consent BLOB, PRIMARY KEY(id,direction));
CREATE TABLE group_invitation_copies(id TEXT PRIMARY KEY, invitation TEXT NOT NULL, direction TEXT NOT NULL);
`

var ErrGroupInvitationStale = errors.New("group: invitation changed or stale; send a fresh proposal and obtain fresh consent")
var errGroupDecisionRecorded = errors.New("group: exact decision already recorded")

// errGroupInvitationOutdated is the invitee's side of a stale invitation: a
// newer one for the same group superseded it (see staleOlderGroupInvitations).
var errGroupInvitationOutdated = errors.New("group: this invitation is out of date because the group changed; accept the newer invitation for this group, or ask the inviter for one")

type GroupInvitationInfo struct {
	ID        string                   `json:"id"`
	Direction string                   `json:"direction"`
	State     string                   `json:"status"`
	Inviter   string                   `json:"inviter"`
	Proposal  protocol.GroupInvitation `json:"proposal"`
}

type groupInvitationRow struct {
	GroupInvitationInfo
	peerPerson, peerFP string
	consent            []byte
}

func groupInvitationIn(q dbq, id, direction string) (groupInvitationRow, error) {
	var r groupInvitationRow
	var raw []byte
	r.ID, r.Direction = id, direction
	err := q.QueryRow(`SELECT peer_person,peer_address,peer_fp,payload,state,consent FROM group_invitations WHERE id=? AND direction=?`, id, direction).Scan(&r.peerPerson, &r.Inviter, &r.peerFP, &raw, &r.State, &r.consent)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r.Proposal)
	if err == nil && (r.Proposal.Validate() != nil || r.Proposal.ID() != id) {
		err = errors.New("group: stored invitation differs")
	}
	return r, err
}

func (a *Agent) GroupInvitations() ([]GroupInvitationInfo, error) {
	rows, err := a.store.db.Query(`SELECT id,direction FROM group_invitations ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	var keys [][2]string
	for rows.Next() {
		var k [2]string
		if err = rows.Scan(&k[0], &k[1]); err != nil {
			break
		}
		keys = append(keys, k)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []GroupInvitationInfo{}
	for _, k := range keys {
		r, e := groupInvitationIn(a.store.db, k[0], k[1])
		if e != nil {
			return nil, e
		}
		result = append(result, r.GroupInvitationInfo)
	}
	return result, nil
}

// CreateGroup obtains only the creator's explicit self consent, then uses the
// same durable CAS publication and pairwise delivery path as later changes.
func (a *Agent) CreateGroup(ctx context.Context, title string) (GroupContext, error) {
	defer notifyDaemon(a.home)
	var packet GroupContext
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return packet, err
	}
	if !ok || !self.has(a.Address, a.Self().Fingerprint()) {
		return packet, ErrGroupContextPending
	}
	realm, err := a.RealmID()
	if err != nil {
		return packet, err
	}
	member := protocol.ConvMember{Person: self.roster.Person, Roster: self.roster.Hash()}
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Realm: realm, Title: title, Creator: protocol.ConvCreator{Person: member.Person, Roster: member.Roster, Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Members: []protocol.ConvMember{member}, Admins: []string{member.Person}, Nonce: protocol.NewID(), Created: time.Now().Unix()}
	if err = protocol.ValidateGroupRoot(root); err != nil {
		return packet, err
	}
	root.Sign(a.id.Sign)
	admission, err := a.SignGroupAdmission(root, 0, "", nil)
	if err != nil {
		return packet, err
	}
	packet = GroupContext{Root: root, State: protocol.GroupState{V: 1, Conv: root.ID(), Realm: realm, Title: title, Members: []protocol.GroupMember{{ConvMember: member, Admin: true, Admission: admission}}}}
	packet, err = a.SignGroupState(ctx, packet)
	if err != nil {
		return packet, err
	}
	commit, err := a.BuildGroupCommit(ctx, packet)
	if err == nil {
		_, err = a.PublishGroup(ctx, commit, packet)
	}
	return packet, err
}

func (a *Agent) verifyGroupInvitation(ctx context.Context, p protocol.GroupInvitation, inviterPerson, inviterAddress, inviterFP string) error {
	return a.verifyGroupInvitationState(ctx, p, inviterPerson, inviterAddress, inviterFP, true)
}

// A local human can record exact consent while offline. This uses only the
// previously authenticated invitation and locally pinned current evidence;
// missing evidence still fails. It never installs membership. All ingress,
// publication and delivery callers retain fresh roster verification.
func (a *Agent) verifyGroupInvitationState(ctx context.Context, p protocol.GroupInvitation, inviterPerson, inviterAddress, inviterFP string, refresh bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if realm != p.Root.Realm {
		return errors.New("group: foreign invitation realm")
	}
	// Requires every original public slot through the proposed current state;
	// no installation or historical ciphertext decryption is performed here.
	var max sql.NullInt64
	if err = a.store.db.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, p.Root.ID(), p.Root.Creator.Fingerprint).Scan(&max); err != nil {
		return err
	}
	if !max.Valid || max.Int64 < p.State.Seq {
		return ErrGroupContextPending
	}
	if max.Int64 > p.State.Seq {
		return ErrGroupInvitationStale
	}
	var seq int64
	var hash string
	err = a.store.db.QueryRow(`SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?`, p.Root.ID(), p.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (seq > p.State.Seq || seq == p.State.Seq && hash != p.State.Hash()) {
		return ErrGroupInvitationStale
	}
	packet := GroupContext{Root: p.Root, State: p.State, Withdrawals: p.Withdrawals}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return err
	}
	withdrawals, pins, err := a.groupPacketWithdrawals(ctx, packet, resolve)
	if err == nil {
		err = a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins)
	}
	if err != nil {
		return err
	}
	admin, ok := p.State.Member(inviterPerson)
	if !ok || !admin.Admin || p.State.Withdrawn(admin, withdrawals) {
		return errors.New("group: inviter is not an effective current administrator")
	}
	if refresh {
		for _, person := range []string{inviterPerson, p.Target} {
			if _, err = a.refreshPerson(ctx, person, false); err != nil {
				return err
			}
		}
	}
	inviter, ok, err := a.store.personByID(inviterPerson)
	if err != nil {
		return err
	}
	if !ok || inviter.info.State == personConflict || !inviter.has(inviterAddress, inviterFP) {
		return errors.New("group: inviting administrator device removed or conflicted")
	}
	target, ok, err := a.store.personByID(p.Target)
	if err != nil {
		return err
	}
	if !ok || target.info.State == personConflict || target.roster.Hash() != p.Roster {
		return ErrGroupInvitationStale
	}
	if m, member := p.State.Member(p.Target); member && !p.State.Withdrawn(m, withdrawals) {
		return errors.New("group: target already has effective membership")
	}
	return nil
}

func (a *Agent) ownGroupInvitation(ctx context.Context, p protocol.GroupInvitation) error {
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGroupContextPending
	}
	if err = a.verifyGroupInvitation(ctx, p, self.roster.Person, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	current, err := a.GroupContext(p.Root.ID())
	if err != nil {
		return err
	}
	if current.State.Hash() != p.State.Hash() {
		return ErrGroupInvitationStale
	}
	_, err = a.GroupMembers(p.Root.ID())
	return err
}

func (a *Agent) ownRecordedGroupInvitation(ctx context.Context, r groupInvitationRow) error {
	if err := a.groupInvitationOwner(r); err != nil {
		return err
	}
	return a.ownGroupInvitation(ctx, r.Proposal)
}

func (a *Agent) groupInvitationOwner(r groupInvitationRow) error {
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || self.roster.Person != r.peerPerson || a.Address != r.Inviter || a.Self().Fingerprint() != r.peerFP {
		return ErrGroupInvitationStale
	}
	return nil
}

// InviteGroup is a persisted local administration intent. Receipt of its
// exact consent can publish it automatically; unrelated remote consents cannot.
func (a *Agent) InviteGroup(ctx context.Context, conv, person string, history []protocol.GroupHistoryRef) (GroupInvitationInfo, error) {
	defer notifyDaemon(a.home)
	var info GroupInvitationInfo
	packet, err := a.GroupContext(conv)
	if err != nil {
		return info, err
	}
	if _, err = a.refreshPerson(ctx, person, false); err != nil {
		return info, err
	}
	target, ok, err := a.store.personByID(person)
	if err != nil {
		return info, err
	}
	if !ok || target.info.State != personPinned {
		return info, errors.New("group: target must be an independently pinned person")
	}
	p := protocol.GroupInvitation{V: 1, Root: packet.Root, State: packet.State, Withdrawals: packet.Withdrawals, Target: person, Roster: target.roster.Hash(), Seq: packet.State.Seq + 1, Prev: packet.State.Hash(), History: slices.Clone(history)}
	if err = a.ownGroupInvitation(ctx, p); err != nil {
		return info, err
	}
	if _, err = a.groupHistorySelectionIn(a.store.db, conv, history); err != nil {
		return info, err
	}
	if old, e := groupInvitationIn(a.store.db, p.ID(), "out"); e == nil {
		return old.GroupInvitationInfo, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return info, e
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return info, err
	}
	defer release()
	copies, err := a.groupInvitationCopies(ctx, p, target.roster)
	if err != nil {
		return info, err
	}
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	raw, _ := json.Marshal(p)
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if _, err := a.groupHistorySelectionIn(tx, conv, history); err != nil {
			return err
		}
		if err := groupTurnCheck(tx, GroupContext{Root: p.Root, State: p.State, Withdrawals: p.Withdrawals}, a.Address, a.Self().Fingerprint()); err != nil {
			return err
		}
		if err := groupInvitationTarget(tx, p); err != nil {
			return err
		}
		self, ok, err := scanPersonIn(tx, "state = ?", personSelf)
		if err != nil {
			return err
		}
		if !ok || !self.has(a.Address, a.Self().Fingerprint()) {
			return ErrGroupInvitationStale
		}
		if _, err := tx.Exec(`INSERT INTO group_invitations(id,direction,conv,peer_person,peer_address,peer_fp,payload,state)VALUES(?,'out',?,?,?,?,?,'pending')`, p.ID(), conv, self.roster.Person, a.Address, a.Self().Fingerprint(), raw); err != nil {
			return err
		}
		return associateGroupInvitationCopies(tx, p.ID(), "out", copies)
	}, "")
	if err != nil {
		return info, err
	}
	stored = true
	a.kickNow()
	return GroupInvitationInfo{ID: p.ID(), Direction: "out", State: "pending", Inviter: a.Address, Proposal: p}, nil
}

func groupInvitationTarget(q dbq, p protocol.GroupInvitation) error {
	target, ok, err := personByIDIn(q, p.Target)
	if err != nil {
		return err
	}
	if !ok || target.info.State == personConflict || target.roster.Hash() != p.Roster {
		return ErrGroupInvitationStale
	}
	return nil
}

func groupInvitationHeadIn(q dbq, p protocol.GroupInvitation, adminPerson, address, fp string) error {
	var head sql.NullInt64
	if err := q.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, p.Root.ID(), p.Root.Creator.Fingerprint).Scan(&head); err != nil {
		return err
	}
	if !head.Valid || head.Int64 < p.State.Seq {
		return ErrGroupContextPending
	}
	if head.Int64 != p.State.Seq {
		return ErrGroupInvitationStale
	}
	record, err := groupProofRecord(q, p.Root.ID(), p.Root.Creator.Fingerprint, p.State.Seq)
	if err != nil {
		return err
	}
	if !record.Matches(p.State) {
		return ErrGroupInvitationStale
	}
	var seq int64
	var hash string
	err = q.QueryRow(`SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?`, p.Root.ID(), p.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (seq > p.State.Seq || seq == p.State.Seq && hash != p.State.Hash()) {
		return ErrGroupInvitationStale
	}
	admin, ok, err := personByIDIn(q, adminPerson)
	if err != nil {
		return err
	}
	if !ok || admin.info.State == personConflict || !admin.has(address, fp) {
		return ErrGroupInvitationStale
	}
	member, ok := p.State.Member(adminPerson)
	if !ok || !member.Admin || p.State.Withdrawn(member, p.Withdrawals) {
		return errors.New("group: inviting admin no longer effective")
	}
	return groupInvitationTarget(q, p)
}

func associateGroupInvitationCopies(tx *sql.Tx, id, direction string, copies []outCopy) error {
	for _, c := range copies {
		if _, err := tx.Exec(`INSERT INTO group_invitation_copies(id,invitation,direction)VALUES(?,?,?)`, c.in.ID, id, direction); err != nil {
			return err
		}
	}
	return nil
}

// DecideGroupInvitation is the explicit human operation. Its exact signature
// and outbox copies commit atomically; retry does not sign or enqueue twice.
func (a *Agent) DecideGroupInvitation(ctx context.Context, id string, accept bool) error {
	defer notifyDaemon(a.home)
	releaseDecision, err := lockfile.Wait(filepath.Join(a.home, "group-lifecycle.lock"))
	if err != nil {
		return err
	}
	defer releaseDecision()
	r, err := groupInvitationIn(a.store.db, id, "in")
	if err != nil {
		return err
	}
	decision := "declined"
	if accept {
		decision = "accepted"
	}
	if r.State == decision {
		return nil
	}
	if r.State == "stale" {
		return errGroupInvitationOutdated
	}
	if r.State != "pending" {
		return errors.New("group: invitation already decided")
	}
	if err = a.verifyGroupInvitationState(ctx, r.Proposal, r.peerPerson, r.Inviter, r.peerFP, false); err != nil {
		return err
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || self.roster.Person != r.Proposal.Target || self.roster.Hash() != r.Proposal.Roster || !self.has(a.Address, a.Self().Fingerprint()) {
		return ErrGroupInvitationStale
	}
	c := protocol.GroupConsent{V: 1, Invitation: id, Decision: decision}
	if accept {
		admission, e := a.SignGroupAdmission(r.Proposal.Root, r.Proposal.Seq, r.Proposal.Prev, r.Proposal.History)
		if e != nil {
			return e
		}
		c.Admission = &admission
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return err
	}
	defer release()
	key, err := a.sendKey(ctx, r.Inviter)
	if err != nil {
		return err
	}
	if key.Fingerprint() != r.peerFP {
		return ErrGroupInvitationStale
	}
	copy, err := a.groupLifecycleCopy(r.Proposal.Root, envelope.SubGroupConsent, protocol.GroupCarrier{V: 1, Seq: r.Proposal.Seq, Hash: r.Proposal.Prev, ToKey: key.Fingerprint()}, c, key)
	if err != nil {
		return err
	}
	copies := []outCopy{copy}
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	raw, _ := json.Marshal(c)
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		old, e := groupInvitationIn(tx, id, "in")
		if e != nil {
			return e
		}
		if old.State != "pending" {
			return errGroupDecisionRecorded
		}
		if e = groupInvitationHeadIn(tx, r.Proposal, r.peerPerson, r.Inviter, r.peerFP); e != nil {
			return e
		}
		if e = groupInvitationTarget(tx, r.Proposal); e != nil {
			return e
		}
		if _, e = tx.Exec(`UPDATE group_invitations SET state=?,consent=? WHERE id=? AND direction='in'`, decision, raw, id); e != nil {
			return e
		}
		return associateGroupInvitationCopies(tx, id, "in", copies)
	}, "")
	if err != nil {
		return err
	}
	stored = true
	a.kickNow()
	return nil
}

// PublishGroupInvitation replays only an accepted, originally local intent.
// The existing publication ledger owns uncertain custody and fanout recovery.
func (a *Agent) PublishGroupInvitation(ctx context.Context, id string) error {
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
	if r.State == "published" {
		return nil
	}
	if r.State == "published-history-unavailable" {
		return ErrGroupHistoryUnavailable
	}
	if r.State == "history-unavailable" {
		return ErrGroupHistoryUnavailable
	}
	if r.State != "accepted" {
		return fmt.Errorf("group: invitation is %s", r.State)
	}
	if err = a.groupInvitationOwner(r); err != nil {
		return err
	}
	var consent protocol.GroupConsent
	if err = decodeGroupCarrierJSON(r.consent, &consent); err != nil || consent.Validate() != nil || consent.Admission == nil {
		return errors.New("group: stored consent invalid")
	}
	if current, e := a.GroupContext(r.Proposal.Root.ID()); e == nil {
		if member, ok := current.State.Member(r.Proposal.Target); ok && member.Admission.Hash() == consent.Admission.Hash() {
			_, err = a.store.db.Exec(`UPDATE group_invitations SET state='published' WHERE id=? AND direction='out'`, id)
			return err
		}
	}
	// Reuse original sealed publication bytes after response loss; rebuilding
	// age ciphertext would conflict with the existing durable publication slot.
	var record, payload []byte
	err = a.store.db.QueryRow(`SELECT record,payload FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?`, r.Proposal.Root.ID(), r.Proposal.Root.Creator.Fingerprint, r.Proposal.Seq).Scan(&record, &payload)
	var packet GroupContext
	var commit protocol.GroupCommit
	if err == nil {
		if json.Unmarshal(record, &commit) != nil || json.Unmarshal(payload, &packet) != nil {
			return errors.New("group: pending publication malformed")
		}
		m, ok := packet.State.Member(r.Proposal.Target)
		if !ok || m.Admission.Hash() != consent.Admission.Hash() {
			return ErrGroupInvitationStale
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, e := a.groupHistorySelectionIn(a.store.db, r.Proposal.Root.ID(), r.Proposal.History); e != nil {
			if errors.Is(e, ErrGroupHistoryUnavailable) {
				a.store.db.Exec(`UPDATE group_invitations SET state='history-unavailable' WHERE id=? AND direction='out' AND state='accepted'`, id)
			}
			return e
		}
		if err = a.ownRecordedGroupInvitation(ctx, r); err != nil {
			if errors.Is(err, ErrGroupInvitationStale) {
				a.store.db.Exec(`UPDATE group_invitations SET state='stale' WHERE id=? AND direction='out'`, id)
			}
			return err
		}
		if err = a.verifyGroupConsent(ctx, r.Proposal, *consent.Admission); err != nil {
			return err
		}
		packet = GroupContext{Root: r.Proposal.Root, State: r.Proposal.State, Withdrawals: r.Proposal.Withdrawals}
		packet.State.Seq, packet.State.Prev = r.Proposal.Seq, r.Proposal.Prev
		packet.State.Members = slices.Clone(packet.State.Members)
		packet.State.Members = slices.DeleteFunc(packet.State.Members, func(m protocol.GroupMember) bool { return m.Person == r.Proposal.Target })
		packet.State.Members = append(packet.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: r.Proposal.Target, Roster: r.Proposal.Roster}, Admission: *consent.Admission})
		slices.SortFunc(packet.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
		packet, err = a.SignGroupState(ctx, packet)
		if err == nil {
			commit, err = a.BuildGroupCommit(ctx, packet)
		}
		if err != nil {
			return err
		}
	} else {
		return err
	}
	if _, err = a.PublishGroup(ctx, commit, packet); err != nil {
		return err
	}
	_, err = a.store.db.Exec(`UPDATE group_invitations SET state='published' WHERE id=? AND direction='out' AND state='accepted'`, id)
	return err
}

func (a *Agent) verifyGroupConsent(ctx context.Context, p protocol.GroupInvitation, admission protocol.GroupAdmission) error {
	want := protocol.GroupAdmission{Conv: p.Root.ID(), Realm: p.Root.Realm, Person: p.Target, Roster: p.Roster, Seq: p.Seq, Prev: p.Prev, History: p.History, By: admission.By}
	if !bytes.Equal(want.Canonical(), admission.Canonical()) {
		return errors.New("group: consent differs from exact proposal")
	}
	if _, err := a.refreshPerson(ctx, p.Target, false); err != nil {
		return err
	}
	target, ok, err := a.store.personByID(p.Target)
	if err != nil {
		return err
	}
	if !ok || target.info.State == personConflict || target.roster.Hash() != p.Roster {
		return ErrGroupInvitationStale
	}
	return admission.Verify(func(person, hash string) (protocol.PersonRoster, bool) {
		return target.roster, person == p.Target && hash == p.Roster
	})
}

// RecoverGroupInvitations is invoked with existing reconnect publication work,
// and after a durable consent arrives. No polling or new execution worker.
func (a *Agent) RecoverGroupInvitations(ctx context.Context) error {
	rows, err := a.store.db.Query(`SELECT id FROM group_invitations WHERE direction='out' AND state='accepted'`)
	if err != nil {
		return err
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
		return err
	}
	var failures []error
	for _, id := range ids {
		if e := a.PublishGroupInvitation(ctx, id); e != nil {
			failures = append(failures, e)
		}
	}
	if e := a.reissueGroupInvitations(ctx); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}

// reissueGroupInvitations re-issues this device's stale invitations whose
// group moved on before they were published: another admission (or any other
// change) came first. A consent binds the head it was signed for, so nothing
// is rebased: the same target gets a fresh proposal at the current head, with
// the same selected history, and must accept it again. Each is re-issued once
// (the newer invitation for that target is its successor). One that no longer
// fits stays stale for its person to review. Only a transient failure is
// returned, so recovery retries it; nothing polls.
func (a *Agent) reissueGroupInvitations(ctx context.Context) error {
	rows, err := a.store.db.Query(`SELECT id FROM group_invitations WHERE direction='out' AND state='stale' ORDER BY rowid`)
	if err != nil {
		return err
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
		return err
	}
	var failures []error
	for _, id := range ids {
		r, err := groupInvitationIn(a.store.db, id, "out")
		if err == nil && a.groupInvitationReissuable(r) {
			var fresh GroupInvitationInfo
			if fresh, err = a.InviteGroup(ctx, r.Proposal.Root.ID(), r.Proposal.Target, r.Proposal.History); err == nil {
				a.Logf("group invitation %s was out of date (the group changed first); sent a fresh invitation %s at the current state: it needs a fresh acceptance", id, fresh.ID)
			}
		}
		if groupLifecycleTransient(err) {
			failures = append(failures, err)
		} else if err != nil {
			a.Logf("group invitation %s stays stale: %v", id, err)
		}
	}
	return errors.Join(failures...)
}

// groupInvitationReissuable says whether stale invitation r of this device
// only fell behind its group: the group moved past the state it names, this
// device's person is still an effective administrator, the target is not a
// member, no newer invitation for the target exists and the selected history
// is still available. Local evidence only; InviteGroup verifies it afresh.
func (a *Agent) groupInvitationReissuable(r groupInvitationRow) bool {
	if a.groupInvitationOwner(r) != nil {
		return false
	}
	p := r.Proposal
	current, err := a.GroupContext(p.Root.ID())
	if err != nil || current.State.Seq <= p.State.Seq {
		return false
	}
	admin, ok := current.State.Member(r.peerPerson)
	if !ok || !admin.Admin || current.State.Withdrawn(admin, current.Withdrawals) {
		return false
	}
	if m, member := current.State.Member(p.Target); member && !current.State.Withdrawn(m, current.Withdrawals) {
		return false
	}
	if _, err = a.groupHistorySelectionIn(a.store.db, p.Root.ID(), p.History); err != nil {
		return false
	}
	newer, err := groupInvitationsBy(a.store.db, "out", p, func(o protocol.GroupInvitation) bool { return o.State.Seq > p.State.Seq })
	return err == nil && len(newer) == 0
}

// groupInvitationsBy are the ids of direction's invitations for p's group
// and target, other than p, for which keep holds.
func groupInvitationsBy(q dbq, direction string, p protocol.GroupInvitation, keep func(protocol.GroupInvitation) bool) ([]string, error) {
	rows, err := q.Query(`SELECT id FROM group_invitations WHERE direction=? AND conv=? AND id!=? ORDER BY rowid`, direction, p.Root.ID(), p.ID())
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var out []string
	for _, id := range ids {
		o, err := groupInvitationIn(q, id, direction)
		if err != nil {
			return nil, err
		}
		if o.Proposal.Target == p.Target && keep(o.Proposal) {
			out = append(out, id)
		}
	}
	return out, nil
}
