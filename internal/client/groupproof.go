package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// GroupProofSchema is appended after GroupClientSchema by the store owner.
// Original signed records retain ciphertext; pending withdrawals grant no role.
const GroupProofSchema = `
CREATE TABLE group_proof_roots(conv TEXT PRIMARY KEY, root BLOB NOT NULL);
CREATE TABLE group_proof_records(conv TEXT NOT NULL, bootstrap TEXT NOT NULL, seq INTEGER NOT NULL, record BLOB NOT NULL, PRIMARY KEY(conv,bootstrap,seq));
CREATE TABLE group_pending_withdrawals(conv TEXT NOT NULL, person TEXT NOT NULL, admission TEXT NOT NULL, record BLOB NOT NULL, PRIMARY KEY(conv,person,admission));
`

func groupProofRecord(q interface{ QueryRow(string, ...any) *sql.Row }, conv, bootstrap string, seq int64) (protocol.GroupCommit, error) {
	var c protocol.GroupCommit
	var raw []byte
	err := q.QueryRow("SELECT record FROM group_proof_records WHERE conv=? AND bootstrap=? AND seq=?", conv, bootstrap, seq).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrGroupContextPending
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(raw, &c)
}

func (a *Agent) groupProofRoot(conv string) (protocol.ConvRoot, error) {
	var root protocol.ConvRoot
	var raw []byte
	err := a.store.db.QueryRow("SELECT root FROM group_proof_roots WHERE conv=?", conv).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return root, ErrGroupContextPending
	}
	if err != nil {
		return root, err
	}
	return root, json.Unmarshal(raw, &root)
}

// IngestGroupProofPage stores original signed ciphertext, not membership or
// message-history grants. Only contiguous root-linked records become durable.
func (a *Agent) IngestGroupProofPage(ctx context.Context, root protocol.ConvRoot, page protocol.GroupJournalPage) error {
	encoded, err := json.Marshal(page)
	if err != nil {
		return err
	}
	if len(page.Records) > 16 || len(encoded) > protocol.MaxBody-1024 || page.More && len(page.Records) == 0 {
		return errors.New("group: proof page exceeds bound or empty continuation")
	}
	if err = protocol.ValidateGroupRoot(root); err != nil {
		return err
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if root.Realm != realm {
		return errors.New("group: foreign proof realm")
	}
	refs := GroupContext{Root: root, State: protocol.GroupState{Actor: root.Creator.Person, ActorRoster: root.Creator.Roster}}
	for i, c := range page.Records {
		if err = c.Validate(); err != nil {
			return err
		}
		if c.Conv != root.ID() || c.Bootstrap != root.Creator.Fingerprint || c.Realm != root.Realm || i > 0 && c.Seq != page.Records[i-1].Seq+1 {
			return errors.New("group: proof page root or sequence mismatch")
		}
		refs.Proof = append(refs.Proof, protocol.GroupState{Actor: c.Actor, ActorRoster: c.ActorRoster})
	}
	resolve, err := a.groupResolver(ctx, refs)
	if err != nil {
		return err
	}
	roster, ok := resolve(root.Creator.Person, root.Creator.Roster)
	if !ok {
		return ErrGroupContextPending
	}
	creator, ok := roster.Device(root.Creator.Fingerprint)
	if !ok || creator.Address != root.Creator.Address {
		return errors.New("group: proof creator mismatch")
	}
	if err = root.Verify(creator.SignKey); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rootRaw, _ := json.Marshal(root)
	var oldRoot []byte
	err = tx.QueryRow("SELECT root FROM group_proof_roots WHERE conv=?", root.ID()).Scan(&oldRoot)
	if err == nil && !bytes.Equal(oldRoot, rootRaw) {
		return errors.New("group: conflicting proof root")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec("INSERT INTO group_proof_roots(conv,root)VALUES(?,?) ON CONFLICT(conv)DO NOTHING", root.ID(), rootRaw); err != nil {
		return err
	}
	var headSeq sql.NullInt64
	if err = tx.QueryRow("SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?", root.ID(), root.Creator.Fingerprint).Scan(&headSeq); err != nil {
		return err
	}
	var previous *protocol.GroupCommit
	if headSeq.Valid {
		c, e := groupProofRecord(tx, root.ID(), root.Creator.Fingerprint, headSeq.Int64)
		if e != nil {
			return e
		}
		previous = &c
	}
	for _, c := range page.Records {
		raw, _ := json.Marshal(c)
		if previous != nil && c.Seq <= previous.Seq {
			old, e := groupProofRecord(tx, root.ID(), root.Creator.Fingerprint, c.Seq)
			if e != nil {
				return e
			}
			stored, _ := json.Marshal(old)
			if !bytes.Equal(raw, stored) {
				return errors.New("group: conflicting original proof record")
			}
			continue
		}
		if err = c.VerifyChain(root, previous, resolve); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO group_proof_records(conv,bootstrap,seq,record)VALUES(?,?,?,?)", c.Conv, c.Bootstrap, c.Seq, raw); err != nil {
			return err
		}
		copy := c
		previous = &copy
	}
	return tx.Commit()
}

func (a *Agent) groupPacketWithdrawals(ctx context.Context, packet GroupContext, resolve protocol.GroupRosterResolver) ([]protocol.GroupWithdrawal, []protocol.GroupWithdrawal, error) {
	raw, _ := json.Marshal(packet)
	if len(raw) > protocol.MaxGroupState {
		return nil, nil, errors.New("group: current context exceeds bound")
	}
	pins, err := a.groupWithdrawals(packet.State.Conv)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range packet.Withdrawals {
		if groupWithdrawalPinned(pins, w) {
			continue
		}
		if err = a.latestGroupWithdrawal(ctx, w); err != nil {
			return nil, nil, err
		}
		if err = w.Verify(packet.State, resolve); err != nil {
			if e := a.deferGroupWithdrawal(w); e != nil {
				return nil, nil, e
			}
			if m, ok := packet.State.Member(w.Person); ok && m.Admission.Hash() == w.Admission && m.Admin {
				return nil, nil, errors.New("group: withdrawal conflicts with promoted admin; explicit authority/context resolution required")
			}
			return nil, nil, ErrGroupContextPending
		}
	}
	pending, err := a.groupPendingForState(ctx, packet.State, resolve)
	if err != nil {
		return nil, nil, err
	}
	withdrawals := append(append(append([]protocol.GroupWithdrawal{}, packet.Withdrawals...), pins...), pending...)
	return withdrawals, pins, nil
}

func (a *Agent) verifyGroupPacket(ctx context.Context, packet GroupContext, resolve protocol.GroupRosterResolver) error {
	withdrawals, pins, err := a.groupPacketWithdrawals(ctx, packet, resolve)
	if err != nil {
		return err
	}
	if len(packet.Proof) > 0 || packet.State.Seq == 0 {
		if err = VerifyGroupContext(packet, resolve); err != nil {
			return err
		}
		var prev *protocol.GroupState
		if len(packet.Proof) > 0 {
			prev = &packet.Proof[len(packet.Proof)-1]
		}
		return packet.State.Verify(packet.Root, prev, resolve, withdrawals)
	}
	old, oldErr := a.GroupContext(packet.State.Conv)
	if oldErr == nil && old.State.Seq+1 == packet.State.Seq {
		return packet.State.Verify(packet.Root, &old.State, resolve, withdrawals)
	}
	if oldErr == nil && packet.State.Seq > old.State.Seq+1 {
		if err := a.recoverGroupGap(ctx, packet, old); err != nil {
			return err
		}
		return a.verifyGroupPacket(ctx, packet, resolve) // pins/context may have advanced during replay
	}
	if oldErr != nil && !errors.Is(oldErr, ErrGroupContextPending) {
		return oldErr
	}
	return a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins)
}

func (a *Agent) verifyGroupCurrentPacket(packet GroupContext, resolve protocol.GroupRosterResolver, withdrawals, pins []protocol.GroupWithdrawal) error {
	authority, err := groupProofRecord(a.store.db, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		return err
	}
	slots := map[int64]protocol.GroupCommit{}
	for _, m := range packet.State.Members {
		if _, ok := slots[m.Admission.Seq]; !ok {
			c, e := groupProofRecord(a.store.db, packet.State.Conv, packet.Root.Creator.Fingerprint, m.Admission.Seq)
			if e != nil {
				return e
			}
			slots[m.Admission.Seq] = c
		}
	}
	for _, w := range packet.Withdrawals {
		if e := w.Verify(packet.State, resolve); e != nil {
			pinned := false
			for _, p := range pins {
				if p.Conv == w.Conv && p.Person == w.Person && p.Admission == w.Admission && bytes.Equal(p.Canonical(), w.Canonical()) && bytes.Equal(p.Sig, w.Sig) {
					pinned = true
				}
			}
			if !pinned {
				return fmt.Errorf("group: withdrawal needs matching verified ordinary context: %w", e)
			}
		}
	}
	return packet.State.VerifyCurrent(packet.Root, authority, resolve, func(seq int64) (protocol.GroupCommit, bool) { c, ok := slots[seq]; return c, ok }, withdrawals)
}

// recoverGroupGap reuses original encrypted commits. Intermediate installs
// remain unusable while the durable public head is newer (GroupMembers/turn gate).
func (a *Agent) recoverGroupGap(ctx context.Context, target, old GroupContext) error {
	for old.State.Seq+1 < target.State.Seq {
		record, err := groupProofRecord(a.store.db, target.State.Conv, target.Root.Creator.Fingerprint, old.State.Seq+1)
		if err != nil {
			return err
		}
		if err = a.AcceptGroupCommit(ctx, record); err != nil {
			if !errors.Is(err, errGroupCiphertextUnavailable) {
				return err
			}
			// Only unavailable original ciphertext permits a fresh SELF join.
			// A decrypted invalid transition is never converted to a fresh read.
			join, e := a.recoverGroupJoin(ctx, target, old)
			if e != nil {
				return e
			}
			old = join
			continue
		}
		next, err := a.GroupContext(target.State.Conv)
		if err != nil {
			return err
		}
		if next.State.Seq <= old.State.Seq {
			return ErrGroupContextPending
		}
		old = next
	}
	return nil
}

// recoverGroupJoin restarts at the exact new SELF consent slot, never at an
// arbitrary latest snapshot. Earlier encrypted rosters remain fresh-reader
// evidence; every transition after this join is verified sequentially.
func (a *Agent) recoverGroupJoin(ctx context.Context, target, old GroupContext) (GroupContext, error) {
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return GroupContext{}, err
	}
	if !ok || !self.has(a.Address, a.Self().Fingerprint()) {
		return GroupContext{}, ErrGroupContextPending
	}
	member, ok := target.State.Member(self.roster.Person)
	if !ok || member.Admission.Seq <= old.State.Seq {
		return GroupContext{}, ErrGroupContextPending
	}
	if before, exists := old.State.Member(self.roster.Person); exists && before.Admission.Hash() == member.Admission.Hash() {
		return GroupContext{}, ErrGroupContextPending
	}
	joinRecord, err := groupProofRecord(a.store.db, target.State.Conv, target.Root.Creator.Fingerprint, member.Admission.Seq)
	if err != nil {
		return GroupContext{}, err
	}
	check := func(ctx context.Context, packet GroupContext, resolve protocol.GroupRosterResolver) error {
		if len(packet.Proof) > 0 {
			if err := VerifyGroupContext(packet, resolve); err != nil {
				return err
			}
		}
		joined, exists := packet.State.Member(self.roster.Person)
		if !exists || packet.State.Seq != member.Admission.Seq || joined.Admission.Hash() != member.Admission.Hash() || joined.Roster != member.Roster || joined.Admission.Seq != packet.State.Seq || joined.Admission.Prev != packet.State.Prev {
			return errors.New("group: fresh self admission differs from exact original join slot")
		}
		roster, found := resolve(joined.Person, joined.Roster)
		if !found {
			return ErrGroupContextPending
		}
		current := false
		for _, d := range roster.Devices {
			if d.Address == a.Address && d.Fingerprint() == a.Self().Fingerprint() {
				current = true
			}
		}
		if !current {
			return errors.New("group: fresh join excludes this exact device")
		}
		withdrawals, pins, e := a.groupPacketWithdrawals(ctx, packet, resolve)
		if e != nil {
			return e
		}
		if packet.State.Withdrawn(joined, withdrawals) {
			return errors.New("group: fresh self admission is withdrawn")
		}
		return a.verifyGroupCurrentPacket(packet, resolve, withdrawals, pins)
	}
	packet, err := a.decodeGroupCommit(ctx, joinRecord, nil, check)
	if err != nil {
		return GroupContext{}, err
	}
	if err = a.acceptGroupContext(ctx, packet, packet.State.Seq == target.State.Seq, nil, check); err != nil {
		return GroupContext{}, err
	}
	return packet, nil
}

// AcceptGroupCommit requires durable original authority evidence before making
// the decryptable current membership usable. Proof ingestion alone does not.
func (a *Agent) AcceptGroupCommit(ctx context.Context, c protocol.GroupCommit) error {
	stored, err := groupProofRecord(a.store.db, c.Conv, c.Bootstrap, c.Seq)
	if err != nil {
		return err
	}
	got, _ := json.Marshal(c)
	want, _ := json.Marshal(stored)
	if !bytes.Equal(got, want) {
		return errors.New("group: current record differs from durable authority proof")
	}
	packet, err := a.DecodeGroupCommit(ctx, c)
	if err != nil {
		return err
	}
	return a.acceptGroupContext(ctx, packet, false, nil)
}

// AcceptGroupContext accepts a forwarded current signed snapshot only after
// its complete original authority prefix is durable. It grants no historical
// decryption key and does not accept plaintext historical membership proof.
func (a *Agent) AcceptGroupContext(ctx context.Context, packet GroupContext) error {
	return a.acceptGroupContextFrom(ctx, packet, identity.Public{})
}

// A forwarded state has authority of its original publisher. Its extra room
// memberships require the exact authenticated carrier sender to vouch for them.
func (a *Agent) acceptGroupContextFrom(ctx context.Context, packet GroupContext, sender identity.Public) error {
	if len(packet.Proof) != 0 {
		return errors.New("group: forwarded context must contain only current state")
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	if len(raw) > protocol.MaxGroupState {
		return errors.New("group: current context exceeds bound")
	}
	if err = protocol.ValidateGroupRoot(packet.Root); err != nil {
		return err
	}
	if err = packet.State.Validate(); err != nil {
		return err
	}
	if packet.State.Conv != packet.Root.ID() || packet.State.Realm != packet.Root.Realm {
		return errors.New("group: forwarded state has a foreign root")
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if packet.Root.Realm != realm {
		return errors.New("group: foreign forwarded context")
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGroupContextPending
	}
	if _, ok = packet.State.Member(self.roster.Person); !ok {
		return errors.New("group: forwarded context does not name this member")
	}
	if _, err = a.refreshPerson(ctx, self.roster.Person, false); err != nil {
		return err
	}
	// Root verification and prefix ingestion store public original records
	// only. Missing/deferred proof never installs membership or old plaintext.
	if err = a.IngestGroupProofPage(ctx, packet.Root, protocol.GroupJournalPage{}); err != nil {
		return err
	}
	if _, err = a.groupProofThrough(ctx, packet.Root, packet.State.Seq); err != nil {
		return err
	}
	return a.acceptGroupContext(ctx, packet, true, &sender)
}

func (a *Agent) acceptGroupContext(ctx context.Context, packet GroupContext, forwarded bool, source *identity.Public, verifier ...func(context.Context, GroupContext, protocol.GroupRosterResolver) error) error {
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	if len(raw) > protocol.MaxGroupState {
		return errors.New("group: current context exceeds bound")
	}
	root, err := a.groupProofRoot(packet.State.Conv)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(root)
	provided, _ := json.Marshal(packet.Root)
	if !bytes.Equal(expected, provided) {
		return errors.New("group: forwarded context differs from durable root")
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if packet.State.Realm != realm || root.Realm != realm {
		return errors.New("group: foreign forwarded context")
	}
	authority, err := groupProofRecord(a.store.db, packet.State.Conv, root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		return err
	}
	if !authority.Matches(packet.State) {
		return errors.New("group: forwarded state differs from original authority")
	}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return err
	}
	check := a.verifyGroupPacket
	if len(verifier) != 0 {
		check = verifier[0]
	}
	if err = check(ctx, packet, resolve); err != nil {
		return err
	}
	// Original ciphertext is vouched by its verified signed publisher. A
	// forwarded packet must instead name the independently verified sender.
	if source == nil {
		source = &identity.Public{}
		if roster, ok := resolve(packet.State.Actor, packet.State.ActorRoster); ok {
			if key, ok := roster.Device(packet.State.By); ok {
				*source = key
			}
		}
	}
	withdrawals, err := a.groupPendingForState(ctx, packet.State, resolve)
	if err != nil {
		return err
	}
	withdrawals = append(withdrawals, packet.Withdrawals...)
	vouched := packet
	vouched.Withdrawals = withdrawals
	if allowed, err := roomMembershipSender(a.store.db, vouched, *source); err != nil {
		return err
	} else if !allowed {
		packet.Memberships = nil
	}
	// A fresh linked member has no outside host roster yet. Pin the exact
	// host named by each original signed invitation before the atomic check.
	for _, ev := range packet.Memberships {
		if ev.Type != protocol.EventInvite || ev.Host == nil {
			continue
		}
		if ev.Conv != packet.State.Conv || ev.Validate() != nil {
			return errors.New("group: malformed membership invitation")
		}
		author, ok, err := a.store.chainStep(ev.Author.Person, ev.Author.Roster)
		if err != nil {
			return err
		}
		if !ok {
			if _, err = a.refreshPerson(ctx, ev.Author.Person, false); err != nil {
				return err
			}
			author, ok, err = a.store.chainStep(ev.Author.Person, ev.Author.Roster)
			if err != nil {
				return err
			}
		}
		if !ok {
			return ErrGroupContextPending
		}
		key, ok := author.Device(ev.Author.Fingerprint)
		if !ok || key.Address != ev.Author.Address || ev.Verify(key.SignKey) != nil {
			return errors.New("group: membership invitation signature differs")
		}
		if _, err = a.externalHostProof(ctx, ev.Host); err != nil {
			return err
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if forwarded {
		if err = a.forwardedGroupTarget(tx, packet, withdrawals); err != nil {
			return err
		}
	}
	// Recheck current key, role and withdrawal state in the installation tx.
	if allowed, err := roomMembershipSender(tx, vouched, *source); err != nil {
		return err
	} else if !allowed {
		packet.Memberships = nil
	}
	if err = installGroupMembership(tx, packet, withdrawals); err != nil {
		return err
	}
	if err = admitRoomMembershipProof(tx, packet); err != nil {
		return err
	}
	return tx.Commit()
}

// forwardedGroupTarget checks local eligibility in the same immediate
// transaction as installation. Original commits retain their existing removal
// and legacy recovery behavior; forwarding cannot grant that exception.
func (a *Agent) forwardedGroupTarget(tx *sql.Tx, packet GroupContext, withdrawals []protocol.GroupWithdrawal) error {
	var proofSeq sql.NullInt64
	if err := tx.QueryRow("SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?", packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&proofSeq); err != nil {
		return err
	}
	if !proofSeq.Valid || proofSeq.Int64 != packet.State.Seq {
		return ErrGroupContextPending
	}
	var seq int64
	var hash string
	err := tx.QueryRow("SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?", packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (seq > packet.State.Seq || seq == packet.State.Seq && hash != packet.State.Hash()) {
		return ErrGroupContextPending
	}
	// selfPerson normally uses the DB; query here to recheck a concurrent
	// linked-device removal/fork against this installation's exact key.
	self, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || !self.has(a.Address, a.Self().Fingerprint()) {
		return errors.New("group: forwarded target is not a current own device")
	}
	member, ok := packet.State.Member(self.roster.Person)
	if !ok || packet.State.Withdrawn(member, withdrawals) {
		return errors.New("group: forwarded target is not an effective current member")
	}
	var withdrawn int
	if err = tx.QueryRow(`SELECT count(*) FROM (
		SELECT admission FROM group_withdrawals WHERE conv=? AND person=? AND admission=?
		UNION ALL SELECT admission FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?
	)`, packet.State.Conv, member.Person, member.Admission.Hash(), packet.State.Conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); err != nil {
		return err
	}
	if withdrawn != 0 {
		return errors.New("group: forwarded target has a withdrawn or pending admission")
	}
	return nil
}

func (a *Agent) ensureGroupProof(ctx context.Context, root protocol.ConvRoot, c protocol.GroupCommit) error {
	stored, err := a.groupProofThrough(ctx, root, c.Seq)
	if err != nil {
		return err
	}
	x, _ := json.Marshal(stored)
	y, _ := json.Marshal(c)
	if !bytes.Equal(x, y) {
		return errors.New("group: custody differs from original proof")
	}
	return nil
}

// groupProofThrough reuses the existing bounded original-record retrieval.
// Relay ACLs still apply: ordinary members need separately delivered original
// proof pages; this helper adds no public read privilege or plaintext history.
func (a *Agent) groupProofThrough(ctx context.Context, root protocol.ConvRoot, seq int64) (protocol.GroupCommit, error) {
	var empty protocol.GroupCommit
	if seq < 0 {
		return empty, errors.New("group: invalid proof sequence")
	}
	conv, bootstrap := root.ID(), root.Creator.Fingerprint
	if stored, err := groupProofRecord(a.store.db, conv, bootstrap, seq); err == nil {
		return stored, nil
	} else if !errors.Is(err, ErrGroupContextPending) {
		return empty, err
	}
	var head sql.NullInt64
	if err := a.store.db.QueryRow("SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?", conv, bootstrap).Scan(&head); err != nil {
		return empty, err
	}
	after := int64(-1)
	if head.Valid {
		after = head.Int64
	}
	for after < seq {
		var page protocol.GroupJournalPage
		path := "/v1/groups/" + conv + "/chain?creator=" + bootstrap + "&after=" + fmt.Sprint(after)
		if err := a.hub.do(ctx, "GET", path, nil, &page); err != nil {
			return empty, err
		}
		if len(page.Records) == 0 || page.Records[len(page.Records)-1].Seq <= after {
			return empty, ErrGroupContextPending
		}
		if err := a.IngestGroupProofPage(ctx, root, page); err != nil {
			return empty, err
		}
		after = page.Records[len(page.Records)-1].Seq
	}
	return groupProofRecord(a.store.db, conv, bootstrap, seq)
}
