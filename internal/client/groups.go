package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// GroupClientSchema is appended by the owning client store migration.
const GroupClientSchema = `
CREATE TABLE group_known_heads(conv TEXT PRIMARY KEY, bootstrap TEXT NOT NULL, seq INTEGER NOT NULL, hash TEXT NOT NULL);
CREATE TABLE group_context(conv TEXT PRIMARY KEY, payload BLOB NOT NULL);
CREATE TABLE group_publications(conv TEXT NOT NULL, bootstrap TEXT NOT NULL, seq INTEGER NOT NULL, record BLOB NOT NULL, payload BLOB NOT NULL, published INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(conv,bootstrap,seq));
CREATE TABLE group_withdrawals(conv TEXT NOT NULL, person TEXT NOT NULL, admission TEXT NOT NULL, record BLOB NOT NULL, PRIMARY KEY(conv,person,admission));
`

var ErrGroupContextPending = errors.New("group: verified decryptable context unavailable")

// GroupContext contains authority history only, never prior message bodies.
// Proof is bounded with the complete payload; no chain is silently truncated.
// Admission.History is the only selected-message grant, checked separately by
// the existing verified history importer before any message is stored.
type GroupContext struct {
	Root        protocol.ConvRoot          `json:"root"`
	Proof       []protocol.GroupState      `json:"proof"`
	State       protocol.GroupState        `json:"state"`
	Withdrawals []protocol.GroupWithdrawal `json:"withdrawals"`
}

func (a *Agent) groupResolver(ctx context.Context, packet GroupContext) (protocol.GroupRosterResolver, error) {
	refs := map[string]map[string]bool{}
	add := func(person, hash string) {
		if refs[person] == nil {
			refs[person] = map[string]bool{}
		}
		refs[person][hash] = true
	}
	add(packet.Root.Creator.Person, packet.Root.Creator.Roster)
	for _, s := range append(append([]protocol.GroupState{}, packet.Proof...), packet.State) {
		add(s.Actor, s.ActorRoster)
		for _, m := range s.Members {
			add(m.Person, m.Roster)
			add(m.Admission.Person, m.Admission.Roster)
		}
	}
	for _, w := range packet.Withdrawals {
		add(w.Person, w.Roster)
	}
	pinned := map[string]protocol.PersonRoster{}
	for person, hashes := range refs {
		if current, ok, err := a.store.personByID(person); err != nil {
			return nil, err
		} else if ok && current.info.State == personConflict {
			return nil, errPersonConflict
		}
		for hash := range hashes {
			r, ok, err := a.store.chainStep(person, hash)
			if err != nil {
				return nil, err
			}
			if !ok {
				if _, err = a.refreshPerson(ctx, person, false); err != nil {
					return nil, err
				}
				r, ok, err = a.store.chainStep(person, hash)
			}
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, ErrGroupContextPending
			}
			pinned[person+":"+hash] = r
		}
	}
	return func(person, hash string) (protocol.PersonRoster, bool) {
		r, ok := pinned[person+":"+hash]
		return r, ok
	}, nil
}

func VerifyGroupContext(packet GroupContext, resolve protocol.GroupRosterResolver) error {
	if len(packet.Proof) > 4096 {
		return errors.New("group: authority proof exceeds bound")
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	if len(raw) > protocol.MaxGroupState {
		return errors.New("group: authority context exceeds bound")
	}
	if err = protocol.ValidateGroupRoot(packet.Root); err != nil {
		return err
	}
	var prev *protocol.GroupState
	states := append(append([]protocol.GroupState{}, packet.Proof...), packet.State)
	// Verify withdrawals against the exact signed historical admission, then
	// retain their monotone overlay through every subsequent state.
	for _, w := range packet.Withdrawals {
		valid := false
		for _, s := range states {
			if w.Verify(s, resolve) == nil {
				valid = true
				break
			}
		}
		if !valid {
			return errors.New("group: withdrawal lacks verified own admission")
		}
	}
	for i := range states {
		if err = states[i].Verify(packet.Root, prev, resolve, packet.Withdrawals); err != nil {
			return err
		}
		prev = &states[i]
	}
	return nil
}

// BuildGroupCommit independently validates all authority, then encrypts current
// context to current effective members' verified pinned devices. The writer
// is retained as a recipient for response-loss recovery after admin transfer.
func (a *Agent) BuildGroupCommit(ctx context.Context, packet GroupContext) (protocol.GroupCommit, error) {
	var out protocol.GroupCommit
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return out, err
	}
	if err = a.verifyGroupPacket(ctx, packet, resolve); err != nil {
		return out, err
	}
	knownWithdrawals, err := a.groupWithdrawals(packet.State.Conv)
	if err != nil {
		return out, err
	}
	applicable, err := a.groupPendingForState(ctx, packet.State, resolve)
	if err != nil {
		return out, err
	}
	knownWithdrawals = append(knownWithdrawals, applicable...)
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return out, err
	}
	if packet.State.Seq == 0 && ok && packet.Root.Creator.Roster != self.roster.Hash() {
		return out, errors.New("group: bootstrap creator roster changed; rebuild root and obtain fresh consent")
	}
	if !ok || packet.State.Actor != self.roster.Person || packet.State.ActorRoster != self.roster.Hash() || packet.State.By != a.id.Public(a.Address).Fingerprint() {
		return out, errors.New("group: current own admin signature required")
	}
	realm, realmErr := a.RealmID()
	if realmErr != nil {
		return out, realmErr
	}
	if packet.State.Realm != realm {
		return out, errors.New("group: foreign workspace")
	}
	recipients := []age.Recipient{a.id.Box.Recipient()}
	seen := map[string]bool{a.id.Box.Recipient().String(): true}
	for _, m := range packet.State.EffectiveMembers(append(packet.Withdrawals, knownWithdrawals...)) {
		// Membership binds the independently pinned person chain, not a forever
		// device list. Refresh its current verified step before encrypting so
		// removed devices receive no new packet.
		if _, e := a.refreshPerson(ctx, m.Person, false); e != nil {
			return out, e
		}
		current, ok, e := a.store.personByID(m.Person)
		if e != nil {
			return out, e
		}
		if !ok {
			return out, ErrGroupContextPending
		}
		for _, d := range current.roster.Devices {
			recipient, e := d.Recipient()
			if e != nil {
				return out, e
			}
			key := fmt.Sprint(recipient)
			if !seen[key] {
				seen[key] = true
				recipients = append(recipients, recipient)
			}
		}
	}
	// New publications carry only the current packet, never prior plaintext states.
	packet.Proof = nil
	packet.Withdrawals = currentGroupWithdrawals(packet.State, append(packet.Withdrawals, knownWithdrawals...))
	raw, _ := json.Marshal(packet)
	if len(raw) > protocol.MaxGroupState {
		return out, errors.New("group: current context exceeds bound")
	}
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipients...)
	if err != nil {
		return out, err
	}
	if _, err = writer.Write(raw); err != nil {
		return out, err
	}
	if err = writer.Close(); err != nil {
		return out, err
	}
	s := packet.State
	out = protocol.GroupCommit{Bootstrap: packet.Root.Creator.Fingerprint, V: 1, Conv: s.Conv, Realm: s.Realm, Seq: s.Seq, Prev: s.Prev, Hash: s.Hash(), Admins: s.Admins(), Writer: a.Address, Actor: s.Actor, ActorRoster: s.ActorRoster, Ciphertext: ciphertext.Bytes()}
	out.Sign(a.id.Sign)
	return out, out.Validate()
}

var errGroupCiphertextUnavailable = errors.New("group: original ciphertext excludes this device")

func (a *Agent) DecodeGroupCommit(ctx context.Context, c protocol.GroupCommit) (GroupContext, error) {
	return a.decodeGroupCommit(ctx, c, nil)
}
func (a *Agent) decodeGroupCommit(ctx context.Context, c protocol.GroupCommit, proposed *GroupContext, verifier ...func(context.Context, GroupContext, protocol.GroupRosterResolver) error) (GroupContext, error) {
	var packet GroupContext
	if err := c.Validate(); err != nil {
		return packet, err
	}
	realm, realmErr := a.RealmID()
	if realmErr != nil {
		return packet, realmErr
	}
	if c.Realm != realm {
		return packet, errors.New("group: foreign workspace")
	}
	reader, err := age.Decrypt(bytes.NewReader(c.Ciphertext), a.id.Box)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return packet, errors.Join(ErrGroupContextPending, errGroupCiphertextUnavailable)
		}
		return packet, err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, protocol.MaxGroupState+1))
	if err != nil {
		return packet, err
	}
	if len(raw) > protocol.MaxGroupState {
		return packet, errors.New("group: context too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&packet); err != nil {
		return packet, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return packet, errors.New("group: trailing context")
	}
	if c.Bootstrap != packet.Root.Creator.Fingerprint || !c.Matches(packet.State) {
		return packet, errors.New("group: ciphertext state disagrees with signed header")
	}
	verification := packet
	if proposed != nil && len(packet.Proof) == 0 {
		verification = *proposed
	}
	resolve, err := a.groupResolver(ctx, verification)
	if err != nil {
		return packet, err
	}
	check := a.verifyGroupPacket
	if len(verifier) != 0 {
		check = verifier[0]
	}
	if err = check(ctx, verification, resolve); err != nil {
		return packet, err
	}
	roster, ok := resolve(c.Actor, c.ActorRoster)
	if !ok {
		return packet, ErrGroupContextPending
	}
	for _, d := range roster.Devices {
		if d.Address == c.Writer {
			return packet, c.Verify(d)
		}
	}
	return packet, errors.New("group: writer absent from verified actor roster")
}

// PublishGroup persists the exact ciphertext before attempting CAS. A lost
// response retries those same bytes; it never creates a replacement admission.
func (a *Agent) PublishGroup(ctx context.Context, c protocol.GroupCommit, packet GroupContext) (protocol.GroupJournalResult, error) {
	var result protocol.GroupJournalResult
	// Completed exact attempts are receipts, never a second fan-out or an
	// opportunity to reinstall an older current snapshot.
	if err := c.Validate(); err != nil {
		return result, err
	}
	exact, _ := json.Marshal(c)
	var completed int
	var record, payload []byte
	if err := a.store.db.QueryRow(`SELECT published,record,payload FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?`, c.Conv, c.Bootstrap, c.Seq).Scan(&completed, &record, &payload); err == nil && completed == 1 {
		raw, _ := json.Marshal(packet)
		if !bytes.Equal(raw, payload) {
			// Current-only producers accept the caller's locally supplied
			// verification prefix; legacy accepted bytes remain exact.
			packet.Proof = nil
			raw, _ = json.Marshal(packet)
		}
		if !bytes.Equal(exact, record) || !bytes.Equal(raw, payload) {
			return result, errors.New("group: completed publication differs")
		}
		return protocol.GroupJournalResult{Seq: c.Seq, Hash: c.Hash}, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	decoded, err := a.decodeGroupCommit(ctx, c, &packet)
	if err != nil {
		return result, err
	}
	if len(decoded.Proof) == 0 {
		packet.Proof = nil
		packet.Withdrawals = decoded.Withdrawals
	}
	want, _ := json.Marshal(packet)
	got, _ := json.Marshal(decoded)
	if !bytes.Equal(want, got) {
		return result, errors.New("group: publication context differs")
	}
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return result, err
	}
	withdrawals, err := a.groupPendingForState(ctx, packet.State, resolve)
	if err != nil {
		return result, err
	}
	withdrawals = append(withdrawals, packet.Withdrawals...)
	delivery := packet
	delivery.Proof = nil
	delivery.Withdrawals = currentGroupWithdrawals(packet.State, withdrawals)
	forwarded, _ := json.Marshal(delivery)
	if len(forwarded) > protocol.MaxGroupState {
		return result, errors.New("group: current context exceeds delivery bound before publication")
	}
	if err = a.groupHistoryPreflight(ctx, c, packet); err != nil {
		return result, err
	}
	raw, _ := json.Marshal(c)
	_, err = a.store.db.Exec("INSERT INTO group_publications(conv,bootstrap,seq,record,payload)VALUES(?,?,?,?,?) ON CONFLICT(conv,bootstrap,seq) DO NOTHING", c.Conv, c.Bootstrap, c.Seq, raw, want)
	if err != nil {
		return result, err
	}
	var saved []byte
	if err = a.store.db.QueryRow("SELECT record FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?", c.Conv, c.Bootstrap, c.Seq).Scan(&saved); err != nil {
		return result, err
	}
	if !bytes.Equal(saved, raw) {
		return result, errors.New("group: another publication is pending at this sequence")
	}
	if err = a.hub.do(ctx, "POST", "/v1/groups/"+c.Conv+"/chain?creator="+c.Bootstrap, c, &result); err != nil {
		retired, proofErr := a.retireGroupLoser(ctx, c, packet, raw)
		if retired {
			return result, fmt.Errorf("group %s: competing committed record won sequence %d; pending attempt retired, fetch current state and explicitly retry with fresh consent: %w", c.Conv, c.Seq, err)
		}
		if proofErr != nil {
			return result, errors.Join(err, fmt.Errorf("group %s: custody uncertain; pending ciphertext retained: %w", c.Conv, proofErr))
		}
		return result, err
	}
	if result.Seq != c.Seq || result.Hash != c.Hash {
		return result, errors.New("group: relay returned inconsistent custody")
	}
	// Exact custody may remove this writer's administrator read permission.
	// Ingest its already signed original record against the durable contiguous
	// public prefix before requiring exact byte equality; no new read authority.
	if err = a.IngestGroupProofPage(ctx, packet.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c}}); err != nil {
		return result, err
	}
	if err = a.ensureGroupProof(ctx, packet.Root, c); err != nil {
		return result, err
	}
	if completed, err := a.completeSupersededGroupPublication(c, exact, want); err != nil || completed {
		return result, err
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return result, err
	}
	defer release()
	copies, err := a.groupDeliveryCopies(ctx, delivery)
	if err != nil {
		return result, err
	}
	contextCopies := copies
	historyCopies, intent, historyErr := a.groupHistoryPublicationCopies(ctx, delivery)
	unavailable := errors.Is(historyErr, ErrGroupHistoryUnavailable)
	if historyErr != nil && !unavailable {
		a.releaseGroupCopies(copies)
		return result, historyErr
	}
	if !unavailable {
		copies = append(copies, historyCopies...)
	}
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	beforeOutbox()
	enqueue := func(batch []outCopy, missing bool) error {
		return a.store.addConvOutbox(batch, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
			var published int
			var raw []byte
			if err := tx.QueryRow(`SELECT published,record FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?`, c.Conv, c.Bootstrap, c.Seq).Scan(&published, &raw); err != nil {
				return err
			}
			if !bytes.Equal(raw, exact) {
				return errors.New("group: pending record changed before enqueue")
			}
			if published == 1 {
				return errGroupPublished
			}
			for _, copy := range contextCopies {
				var desc protocol.GroupCarrier
				if err := decodeStrict([]byte(copy.in.Body), &desc); err != nil {
					return err
				}
				var err error
				if copy.in.PID != "" {
					err = groupVisitorCarrierDestination(tx, delivery, copy.in.PID, copy.env.To, desc.ToKey)
				} else {
					err = groupDeliveryRecipient(tx, delivery, copy.env.To, desc.ToKey)
				}
				if err != nil {
					return err
				}
			}
			if err := installGroupMembership(tx, packet, withdrawals); err != nil {
				return err
			}
			if err := a.groupHistoryPublicationCheck(tx, delivery, intent, historyCopies, missing); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE group_publications SET published=1 WHERE conv=? AND bootstrap=? AND seq=?`, c.Conv, c.Bootstrap, c.Seq)
			return err
		}, "")
	}
	err = enqueue(copies, unavailable)
	if errors.Is(err, ErrGroupHistoryUnavailable) && !unavailable {
		// Exact custody already exists; content changed before the atomic local
		// enqueue. Complete context only, with an explicit durable outcome.
		copies = contextCopies
		unavailable = true
		err = enqueue(copies, true)
	}
	if errors.Is(err, errGroupPublished) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	stored = true
	release()
	a.kickNow()
	if unavailable {
		return result, ErrGroupHistoryUnavailable
	}
	return result, nil
}

// retireGroupLoser needs a durable competing record verified against the same
// original root. Transport failure, unavailable context or denied reads prove
// nothing about custody; exact accepted records remain available for replay.
func (a *Agent) retireGroupLoser(ctx context.Context, attempted protocol.GroupCommit, packet GroupContext, raw []byte) (bool, error) {
	var page protocol.GroupJournalPage
	path := "/v1/groups/" + attempted.Conv + "/chain?creator=" + attempted.Bootstrap + "&after=" + strconv.FormatInt(attempted.Seq-1, 10)
	if err := a.hub.do(ctx, "GET", path, nil, &page); err != nil {
		return false, err
	}
	if len(page.Records) == 0 {
		return false, nil
	}
	winner := page.Records[0]
	if winner.Conv != attempted.Conv || winner.Bootstrap != attempted.Bootstrap || winner.Realm != attempted.Realm || winner.Seq != attempted.Seq {
		return false, errors.New("group: competing journal slot mismatch")
	}
	stored, err := json.Marshal(winner)
	if err != nil {
		return false, err
	}
	if bytes.Equal(stored, raw) {
		return false, nil
	}
	verified, err := a.DecodeGroupCommit(ctx, winner)
	if err != nil {
		return false, err
	}
	if verified.Root.ID() != packet.Root.ID() || verified.Root.Creator.Fingerprint != packet.Root.Creator.Fingerprint {
		return false, errors.New("group: competing record has another root")
	}
	res, err := a.store.db.Exec("DELETE FROM group_publications WHERE conv=? AND bootstrap=? AND seq=? AND published=0 AND record=?", attempted.Conv, attempted.Bootstrap, attempted.Seq, raw)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

// RecoverGroupPublications runs once on daemon start/reconnect. The existing
// push loop triggers synchronization thereafter; no polling is introduced.
func (a *Agent) RecoverGroupPublications(ctx context.Context) error {
	rows, err := a.store.db.Query("SELECT record,payload FROM group_publications WHERE published=0 ORDER BY conv,seq")
	if err != nil {
		return err
	}
	type pending struct {
		c protocol.GroupCommit
		p GroupContext
	}
	list := []pending{}
	var failures []error
	for rows.Next() {
		var raw, payload []byte
		if err = rows.Scan(&raw, &payload); err != nil {
			rows.Close()
			return err
		}
		var v pending
		if err = json.Unmarshal(raw, &v.c); err != nil {
			failures = append(failures, fmt.Errorf("group: invalid pending record retained: %w", err))
			continue
		}
		if err = json.Unmarshal(payload, &v.p); err != nil {
			failures = append(failures, fmt.Errorf("group %s: invalid pending context retained: %w", v.c.Conv, err))
			continue
		}
		list = append(list, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range list {
		if _, err = a.PublishGroup(ctx, v.c, v.p); err != nil {
			failures = append(failures, fmt.Errorf("recovering group %s sequence %d: %w", v.c.Conv, v.c.Seq, err))
		}
	}
	return errors.Join(failures...)
}

func (a *Agent) GroupContext(conv string) (GroupContext, error) {
	var p GroupContext
	var raw []byte
	err := a.store.db.QueryRow("SELECT payload FROM group_context WHERE conv=?", conv).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrGroupContextPending
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

// SyncGroup retrieves durable ciphertext after push hints. A linked admin
// without a decryptable current packet stays context-pending until approved
// history re-encryption arrives; having a person key alone grants no plaintext.
func (a *Agent) SyncGroup(ctx context.Context, conv string) error {
	p, err := a.GroupContext(conv)
	if errors.Is(err, ErrGroupContextPending) {
		root, proofErr := a.groupProofRoot(conv)
		if proofErr != nil {
			return proofErr
		}
		return a.SyncGroupFromRoot(ctx, root)
	}
	if err != nil {
		return err
	}
	return a.syncGroup(ctx, conv, p.Root.Creator.Fingerprint)
}

// SyncGroupFromRoot accepts only an independently pinned signed bootstrap root,
// never an expected creator inferred from an untrusted push hint.
func (a *Agent) SyncGroupFromRoot(ctx context.Context, root protocol.ConvRoot) error {
	if err := protocol.ValidateGroupRoot(root); err != nil {
		return err
	}
	r, ok, err := a.store.chainStep(root.Creator.Person, root.Creator.Roster)
	if err != nil {
		return err
	}
	if !ok {
		if _, err = a.refreshPerson(ctx, root.Creator.Person, false); err != nil {
			return err
		}
		r, ok, err = a.store.chainStep(root.Creator.Person, root.Creator.Roster)
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrGroupContextPending
	}
	d, ok := r.Device(root.Creator.Fingerprint)
	if !ok || d.Address != root.Creator.Address {
		return ErrGroupContextPending
	}
	if err = root.Verify(d.SignKey); err != nil {
		return err
	}
	if old, e := a.GroupContext(root.ID()); e == nil && old.Root.Creator.Fingerprint != root.Creator.Fingerprint {
		return errors.New("group: conflicting bootstrap")
	}
	if err = a.IngestGroupProofPage(ctx, root, protocol.GroupJournalPage{}); err != nil {
		return err
	}
	return a.syncGroup(ctx, root.ID(), root.Creator.Fingerprint)
}

func (a *Agent) syncGroup(ctx context.Context, conv, bootstrap string) error {
	if !protocol.ValidHash(conv) {
		return errors.New("group: invalid conversation")
	}
	after := int64(-1)
	for {
		var page protocol.GroupJournalPage
		if err := a.hub.do(ctx, "GET", "/v1/groups/"+conv+"/chain?creator="+bootstrap+"&after="+strconv.FormatInt(after, 10), nil, &page); err != nil {
			return err
		}
		root, err := a.groupProofRoot(conv)
		if errors.Is(err, ErrGroupContextPending) {
			if local, e := a.GroupContext(conv); e == nil {
				root = local.Root
				err = nil
			}
		}
		if err != nil {
			return err
		}
		if err = a.IngestGroupProofPage(ctx, root, page); err != nil {
			return err
		}
		for _, c := range page.Records {
			if c.Bootstrap != bootstrap || c.Seq != after+1 || c.Conv != conv {
				return errors.New("group: unordered journal")
			}
			after = c.Seq
			if err = a.AcceptGroupCommit(ctx, c); errors.Is(err, ErrGroupContextPending) {
				continue
			} else if err != nil {
				return err
			}
		}
		if !page.More {
			return nil
		}
		if len(page.Records) == 0 {
			return errors.New("group: empty continuation")
		}
	}
}

// installGroupContext accepts an already verified context. The caller's
// immediate SQLite transaction serializes this comparison with its write.
func installGroupContext(tx *sql.Tx, packet GroupContext) error {
	var stored []byte
	err := tx.QueryRow("SELECT payload FROM group_context WHERE conv=?", packet.State.Conv).Scan(&stored)
	if err == nil {
		var old GroupContext
		if err = json.Unmarshal(stored, &old); err != nil {
			return err
		}
		if old.State.Seq > packet.State.Seq {
			return nil
		}
		if old.State.Seq == packet.State.Seq {
			if old.State.Hash() != packet.State.Hash() {
				return errors.New("group: conflicting verified state")
			}
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO group_context(conv,payload)VALUES(?,?) ON CONFLICT(conv)DO UPDATE SET payload=excluded.payload", packet.State.Conv, raw)
	return err
}

// GroupMembers resolves only the latest locally verified effective state. It
// never falls back to the immutable founding roster after a failed lookup.
func (a *Agent) GroupMembers(conv string) ([]protocol.ConvMember, error) {
	p, err := a.GroupContext(conv)
	if err != nil {
		return nil, err
	}
	withdrawals, err := a.groupWithdrawals(conv)
	if err != nil {
		return nil, err
	}
	var proofSeq sql.NullInt64
	if err = a.store.db.QueryRow("SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?", conv, p.Root.Creator.Fingerprint).Scan(&proofSeq); err != nil {
		return nil, err
	}
	if proofSeq.Valid && proofSeq.Int64 > p.State.Seq {
		return nil, ErrGroupContextPending
	}
	pending, err := groupWithdrawalRows(a.store.db, "group_pending_withdrawals", conv)
	if err != nil {
		return nil, err
	}
	for _, w := range pending {
		if m, ok := p.State.Member(w.Person); ok && m.Admission.Hash() == w.Admission {
			return nil, errors.New("group: unresolved withdrawal authority/context conflict")
		}
	}
	var seq int64
	var hash string
	err = a.store.db.QueryRow("SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?", conv, p.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && (seq > p.State.Seq || seq == p.State.Seq && hash != p.State.Hash()) {
		return nil, ErrGroupContextPending
	}
	effective := p.State.EffectiveMembers(append(p.Withdrawals, withdrawals...))
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGroupContextPending
	}
	out := []protocol.ConvMember{}
	own := false
	for _, m := range effective {
		out = append(out, m.ConvMember)
		own = own || m.Person == self.roster.Person
	}
	if !own {
		return nil, errors.New("group: own membership withdrawn or removed")
	}
	return out, nil
}

func (a *Agent) NoteGroupHead(h protocol.GroupHead) error {
	if !protocol.ValidFingerprint(h.Bootstrap) || !protocol.ValidHash(h.Conv) || !protocol.ValidHash(h.Hash) || h.Seq < 0 {
		return errors.New("group: invalid head hint")
	}
	p, err := a.GroupContext(h.Conv)
	root := p.Root
	if errors.Is(err, ErrGroupContextPending) {
		root, err = a.groupProofRoot(h.Conv)
	}
	if err != nil {
		return err
	}
	if root.Creator.Fingerprint != h.Bootstrap {
		return errors.New("group: foreign bootstrap hint")
	}
	_, err = a.store.db.Exec("INSERT INTO group_known_heads(conv,bootstrap,seq,hash)VALUES(?,?,?,?) ON CONFLICT(conv)DO UPDATE SET seq=excluded.seq,hash=excluded.hash WHERE excluded.seq>=group_known_heads.seq", h.Conv, h.Bootstrap, h.Seq, h.Hash)
	return err
}
func (a *Agent) groupWithdrawals(conv string) ([]protocol.GroupWithdrawal, error) {
	rows, err := a.store.db.Query("SELECT record FROM group_withdrawals WHERE conv=?", conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.GroupWithdrawal{}
	for rows.Next() {
		var raw []byte
		var w protocol.GroupWithdrawal
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// AcceptGroupWithdrawal persists the monotone overlay before acknowledgment.
// Delivery to peers remains an encrypted quiet event in the existing outbox.
func (a *Agent) AcceptGroupWithdrawal(ctx context.Context, w protocol.GroupWithdrawal) error {
	pins, err := a.groupWithdrawals(w.Conv)
	if err != nil {
		return err
	}
	for _, pin := range pins {
		if sameGroupWithdrawal(pin, w) {
			return nil
		}
	}
	realm, err := a.RealmID()
	if err != nil {
		return err
	}
	if w.Realm != realm {
		return errors.New("group: foreign withdrawal realm")
	}
	if err = a.latestGroupWithdrawal(ctx, w); err != nil {
		return err
	}
	packet, err := a.GroupContext(w.Conv)
	if errors.Is(err, ErrGroupContextPending) {
		if err = a.deferGroupWithdrawal(w); err != nil {
			return err
		}
		return ErrGroupContextPending
	}
	if err != nil {
		return err
	}
	withWithdrawal := packet
	withWithdrawal.Withdrawals = append(append([]protocol.GroupWithdrawal{}, packet.Withdrawals...), w)
	resolve, err := a.groupResolver(ctx, withWithdrawal)
	if err != nil {
		return err
	}
	if err = w.Verify(packet.State, resolve); err != nil {
		if e := a.deferGroupWithdrawal(w); e != nil {
			return e
		}
		if m, ok := packet.State.Member(w.Person); ok && m.Admission.Hash() == w.Admission && m.Admin {
			return errors.New("group: withdrawal conflicts with promoted admin; explicit authority/context resolution required")
		}
		return ErrGroupContextPending
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Recheck the transaction-local current role against a concurrent promotion.
	var raw []byte
	if err = tx.QueryRow("SELECT payload FROM group_context WHERE conv=?", w.Conv).Scan(&raw); err != nil {
		return err
	}
	var current GroupContext
	if err = json.Unmarshal(raw, &current); err != nil {
		return err
	}
	if err = w.Verify(current.State, resolve); err != nil {
		return err
	}
	if err = pinGroupWithdrawal(tx, w); err != nil {
		return err
	}
	return tx.Commit()
}

// GroupHistoryAllows checks the exact new admission's signed selection before
// the existing history importer may store a historical turn for that person.
func (a *Agent) GroupHistoryAllows(conv, person string, ref protocol.GroupHistoryRef) (bool, error) {
	p, err := a.GroupContext(conv)
	if err != nil {
		return false, err
	}
	withdrawals, err := a.groupWithdrawals(conv)
	if err != nil {
		return false, err
	}
	for _, m := range p.State.EffectiveMembers(append(p.Withdrawals, withdrawals...)) {
		if m.Person == person {
			return m.Admission.AllowsHistory(ref), nil
		}
	}
	return false, nil
}

// SignGroupAdmission records explicit consent to one proposed membership step
// and its exact selected history. The caller presents this proposal to the
// person; receipt of a remote proposal must never invoke signing implicitly.
func (a *Agent) SignGroupAdmission(root protocol.ConvRoot, seq int64, prev string, history []protocol.GroupHistoryRef) (protocol.GroupAdmission, error) {
	var out protocol.GroupAdmission
	if err := protocol.ValidateGroupRoot(root); err != nil {
		return out, err
	}
	realm, err := a.RealmID()
	if err != nil {
		return out, err
	}
	if realm != root.Realm {
		return out, errors.New("group: foreign workspace")
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, ErrGroupContextPending
	}
	if seq == 0 && root.Creator.Person == self.roster.Person && root.Creator.Roster != self.roster.Hash() {
		return out, errors.New("group: bootstrap creator roster changed; rebuild root and obtain fresh consent")
	}
	out = protocol.GroupAdmission{Conv: root.ID(), Realm: realm, Person: self.roster.Person, Roster: self.roster.Hash(), Seq: seq, Prev: prev, History: history, By: a.id.Public(a.Address).Fingerprint()}
	if err = out.Validate(); err != nil {
		return out, err
	}
	out.Sign(a.id.Sign)
	return out, nil
}

// SignGroupState signs only an independently verified root/ordered transition.
// A conflicted CAS requires a new proposal and new joining consent, not merely
// changing seq/prev on a previously signed admission.
func (a *Agent) SignGroupState(ctx context.Context, packet GroupContext) (GroupContext, error) {
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return packet, err
	}
	if !ok {
		return packet, ErrGroupContextPending
	}
	if packet.State.Seq == 0 && (packet.Root.Creator.Person != self.roster.Person || packet.Root.Creator.Roster != self.roster.Hash()) {
		return packet, errors.New("group: bootstrap creator roster changed; rebuild root and obtain fresh consent")
	}
	packet.State.Actor = self.roster.Person
	packet.State.ActorRoster = self.roster.Hash()
	packet.State.By = a.id.Public(a.Address).Fingerprint()
	packet.State.Sign(a.id.Sign)
	resolve, err := a.groupResolver(ctx, packet)
	if err != nil {
		return packet, err
	}
	return packet, a.verifyGroupPacket(ctx, packet, resolve)
}

// SignGroupWithdrawal records a local ordinary departure and its exact encrypted
// fanout atomically. It uses verified pinned local evidence so offline leave is
// possible; ingress and reconnect still verify fresh rosters before delivery.
func (a *Agent) SignGroupWithdrawal(ctx context.Context, conv string) (protocol.GroupWithdrawal, error) {
	var out protocol.GroupWithdrawal
	packet, err := a.localWithdrawalContext(ctx, conv)
	if err != nil {
		return out, err
	}
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return out, err
	}
	if !ok || !self.has(a.Address, a.Self().Fingerprint()) {
		return out, ErrGroupContextPending
	}
	member, ok := packet.State.Member(self.roster.Person)
	if !ok {
		return out, errors.New("group: ordinary current membership required")
	}
	if member.Admin {
		return out, errors.New("group: administrator departure uses explicit CAS leave; promote a successor first")
	}
	pins, err := a.groupWithdrawals(conv)
	if err != nil {
		return out, err
	}
	for _, pin := range pins {
		if pin.Person == self.roster.Person && pin.Admission == member.Admission.Hash() {
			if pin.By != a.Self().Fingerprint() {
				return pin, nil
			} // already left on another linked device; do not impersonate its signature
			return pin, a.enqueueGroupWithdrawal(ctx, packet, pin)
		}
	}
	out = protocol.GroupWithdrawal{Conv: conv, Realm: packet.State.Realm, Person: self.roster.Person, Admission: member.Admission.Hash(), Roster: self.roster.Hash(), By: a.Self().Fingerprint()}
	out.Sign(a.id.Sign)
	return out, a.enqueueGroupWithdrawal(ctx, packet, out)
}
