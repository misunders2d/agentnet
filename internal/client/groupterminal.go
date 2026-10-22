package client

// A terminal context is the original signed removal state, not a new kind of
// membership authority. Only its prior entitled human recipient is exceptional;
// ordinary delivery/participation/history checks are deliberately unchanged.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Private metadata in the EXISTING pending-publication payload, never on wire.
// Keeping the old plaintext here closes the CAS/recovery/parent-replacement gap.
type groupTerminalPublication struct {
	Context   GroupContext `json:"context"`
	Person    string       `json:"person"`
	Admission string       `json:"admission"`
	Pending   string       `json:"pending,omitempty"`
}
type groupPublicationPayload struct {
	GroupContext
	Terminal *groupTerminalPublication `json:"terminal,omitempty"`
}

var errGroupTerminalRecipient = errors.New("group: terminal recipient unavailable")

func sameGroupMember(a, b protocol.GroupMember) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// Signatures/authority are independently checked by the caller. This exact
// disclosure predicate also handles ordinary withdrawals folded by governGroup.
func terminalGroupMember(p GroupContext) (protocol.GroupMember, error) {
	var zero protocol.GroupMember
	if len(p.Proof) == 0 || len(p.Proof) > 4096 || len(p.Memberships) != 0 {
		return zero, errors.New("group: terminal context needs bounded original entitled proof only")
	}
	prev := p.Proof[len(p.Proof)-1]
	next := p.State
	if next.V != prev.V || next.Conv != prev.Conv || next.Realm != prev.Realm || next.Seq != prev.Seq+1 || next.Prev != prev.Hash() || next.Title != prev.Title {
		return zero, errors.New("group: terminal context changed more than membership")
	}
	effective := prev.EffectiveMembers(p.Withdrawals)
	var removed protocol.GroupMember
	i := 0
	for _, m := range effective {
		if i < len(next.Members) && sameGroupMember(m, next.Members[i]) {
			i++
			continue
		}
		if removed.Person != "" {
			return zero, errors.New("group: terminal context has unexplained membership/role/admission change")
		}
		removed = m
	}
	if removed.Person == "" || i != len(next.Members) || len(effective) != len(next.Members)+1 || next.Actor == removed.Person {
		return zero, errors.New("group: terminal context is not another administrator's exact removal")
	}
	for _, w := range p.Withdrawals {
		if w.Person == removed.Person && w.Admission == removed.Admission.Hash() {
			return zero, errors.New("group: own withdrawal is not administrator removal")
		}
	}
	admin, ok := prev.Member(next.Actor)
	if !ok || !admin.Admin || prev.Withdrawn(admin, p.Withdrawals) {
		return zero, errors.New("group: terminal actor is not a previous effective administrator")
	}
	return removed, nil
}

func (a *Agent) prepareGroupTerminal(ctx context.Context, prev, next GroupContext, person string, withdrawals []protocol.GroupWithdrawal) (*groupTerminalPublication, error) {
	m, ok := prev.State.Member(person)
	if !ok || person == next.State.Actor {
		return nil, nil
	}
	p := GroupContext{Root: next.Root, State: next.State, Proof: []protocol.GroupState{prev.State}, Withdrawals: currentGroupWithdrawals(prev.State, withdrawals)}
	removed, err := terminalGroupMember(p)
	if err != nil || removed.Person != person {
		a.Logf("group terminal notice withheld: exact removal disclosure fence failed")
		return nil, nil
	}
	t := &groupTerminalPublication{Context: p, Person: person, Admission: m.Admission.Hash()}
	// Every disclosed predecessor was actually held/decrypted by this publisher.
	// Fresh readers get the original public prefix separately, never invented
	// plaintext or a silent shortened proof when an old key is unavailable.
	var proof []protocol.GroupState
	for seq := m.Admission.Seq; seq <= prev.State.Seq; seq++ {
		if len(proof) >= 4096 {
			t.Pending = "group: terminal entitled proof exceeds 4096 states"
			break
		}
		state := prev.State
		if seq != prev.State.Seq {
			record, e := groupProofRecord(a.store.db, next.State.Conv, next.Root.Creator.Fingerprint, seq)
			if e != nil {
				t.Pending = "group: terminal original proof unavailable"
				break
			}
			decoded, e := a.decodeGroupCommit(ctx, record, nil, func(_ context.Context, p GroupContext, r protocol.GroupRosterResolver) error {
				return a.verifyGroupCurrentPacket(p, r, nil, nil)
			})
			if e != nil {
				t.Pending = "group: terminal entitled plaintext unavailable"
				break
			}
			state = decoded.State
		}
		member, present := state.Member(person)
		if !present || member.Admission.Hash() != t.Admission {
			a.Logf("group terminal notice withheld: original proof crosses another admission")
			return nil, nil
		}
		proof = append(proof, state)
		candidate := p
		candidate.Proof = proof
		raw, _ := json.Marshal(candidate)
		if len(raw) > protocol.MaxGroupState {
			t.Pending = "group: terminal entitled proof exceeds current context bound"
			break
		}
	}
	if t.Pending == "" {
		t.Context.Proof = proof
	}
	return t, nil
}

// Existing public prefix verification supplies original authority for the
// admission slot; subsequent plaintext predecessors verify consecutively.
func (a *Agent) verifyGroupTerminal(p GroupContext, resolve protocol.GroupRosterResolver) (protocol.GroupMember, error) {
	m, err := terminalGroupMember(p)
	if err != nil {
		return m, err
	}
	raw, _ := json.Marshal(p)
	if len(raw) > protocol.MaxGroupState {
		return m, errors.New("group: terminal context exceeds bound")
	}
	if p.Proof[0].Seq != m.Admission.Seq {
		return m, errors.New("group: terminal proof does not begin at exact admission")
	}
	for _, w := range p.Withdrawals {
		verified := false
		for _, s := range p.Proof {
			if w.Verify(s, resolve) == nil {
				verified = true
				break
			}
		}
		if !verified {
			return m, errors.New("group: terminal folded withdrawal lacks signed ordinary admission")
		}
	}
	states := append(slices.Clone(p.Proof), p.State)
	var previous *protocol.GroupState
	for i := range states {
		s := &states[i]
		record, e := groupProofRecord(a.store.db, s.Conv, p.Root.Creator.Fingerprint, s.Seq)
		if e != nil {
			return m, e
		}
		if !record.Matches(*s) {
			return m, errors.New("group: terminal state differs from original committed slot")
		}
		if previous != nil {
			if e = s.Verify(p.Root, previous, resolve, p.Withdrawals); e != nil {
				return m, e
			}
		}
		if e = s.VerifyCurrent(p.Root, record, resolve, func(seq int64) (protocol.GroupCommit, bool) {
			c, e := groupProofRecord(a.store.db, s.Conv, p.Root.Creator.Fingerprint, seq)
			return c, e == nil
		}, p.Withdrawals); e != nil {
			return m, e
		}
		if i < len(p.Proof) {
			own, ok := s.Member(m.Person)
			if !ok || own.Admission.Hash() != m.Admission.Hash() || s.Withdrawn(own, p.Withdrawals) {
				return m, errors.New("group: terminal proof crosses departed admission")
			}
		}
		previous = s
	}
	return m, nil
}

// A separate recipient class, never a relaxation of groupDeliveryRecipient.
func groupTerminalRecipient(q dbq, t *groupTerminalPublication, address, fp string) error {
	if t == nil {
		return errors.New("group: terminal publication missing")
	}
	m, err := terminalGroupMember(t.Context)
	if err != nil {
		return err
	}
	if m.Person != t.Person || m.Admission.Hash() != t.Admission {
		return errors.New("group: terminal prior admission differs")
	}
	c, err := groupProofRecord(q, t.Context.State.Conv, t.Context.Root.Creator.Fingerprint, t.Context.State.Seq)
	if err != nil {
		return err
	}
	if !c.Matches(t.Context.State) {
		return errors.New("group: terminal removal has no exact committed custody")
	}
	actor, known, e := personByIDIn(q, t.Context.State.Actor)
	if e != nil {
		return e
	}
	if known && actor.info.State == personConflict {
		return errPersonConflict
	}
	if !known || !actor.has(c.Writer, t.Context.State.By) || !actor.roster.Human(t.Context.State.By) {
		return fmt.Errorf("%w: original sender key is no longer a pinned human", errGroupTerminalRecipient)
	}
	p, ok, err := personByIDIn(q, t.Person)
	if err != nil {
		return err
	}
	if ok && p.info.State == personConflict {
		return errPersonConflict
	}
	if !ok || !p.has(address, fp) || !p.roster.Human(fp) {
		return fmt.Errorf("%w: not an exact pinned human device", errGroupTerminalRecipient)
	}
	var pub string
	var pending sql.NullString
	if err = q.QueryRow("SELECT public,pending FROM peers WHERE address=?", address).Scan(&pub, &pending); err != nil {
		return err
	}
	var key identity.Public
	if err = json.Unmarshal([]byte(pub), &key); err != nil {
		return err
	}
	if pending.Valid {
		return errPersonConflict
	}
	if key.Fingerprint() != fp {
		return fmt.Errorf("%w: key changed", errGroupTerminalRecipient)
	}
	current, err := groupTurnPacketIn(q, t.Context.State.Conv)
	if err != nil {
		return err
	}
	if current.State.Seq >= t.Context.State.Seq {
		if _, readmitted := current.State.Member(t.Person); readmitted {
			return fmt.Errorf("%w: later readmission supersedes removal", errGroupTerminalRecipient)
		}
	}
	return nil
}

func terminalLogicalID(p GroupContext, fp, sub string, seq int64) string {
	h := sha256.Sum256([]byte("agentnet-group-terminal-v1\n" + p.State.Conv + "\n" + p.State.Hash() + "\n" + fp + "\n" + sub + "\n" + strconv.FormatInt(seq, 10)))
	return hex.EncodeToString(h[:16])
}

func (a *Agent) groupTerminalCopies(ctx context.Context, t *groupTerminalPublication, base GroupContext) (copies []outCopy, err error) {
	if t == nil {
		return nil, nil
	}
	defer func() {
		if err != nil {
			a.releaseGroupCopies(copies)
		}
	}()
	payloads, err := groupDeliveryPayloads(a.store.db, base)
	if err != nil {
		return nil, err
	}
	proofs := payloads[:0]
	for _, p := range payloads {
		if p.sub == envelope.SubGroupProof {
			proofs = append(proofs, p)
		}
	}
	if t.Pending == "" {
		raw, _ := json.Marshal(t.Context)
		proofs = append(proofs, groupDeliveryPayload{envelope.SubGroupContext, protocol.GroupCarrier{V: 1, Seq: t.Context.State.Seq, Hash: t.Context.State.Hash()}, raw})
	}
	person, ok, err := a.store.personByID(t.Person)
	if err != nil || !ok {
		return nil, err
	}
	for _, device := range person.roster.Devices {
		if !person.roster.Human(device.Fingerprint()) {
			continue
		}
		if err = groupTerminalRecipient(a.store.db, t, device.Address, device.Fingerprint()); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errGroupTerminalRecipient) || errors.Is(err, errPersonConflict) || deviceKeyUnusable(err) {
				err = nil
				continue
			}
			return copies, err
		}
		key, e := a.sendKey(ctx, device.Address)
		if deviceKeyUnusable(e) {
			a.Logf("group terminal state for %s not sent: %v", device.Address, e)
			continue
		}
		if e != nil {
			return copies, e
		}
		if key.Fingerprint() != device.Fingerprint() {
			continue
		}
		for _, p := range proofs {
			copy, e := a.groupLifecycleCopy(base.Root, p.sub, p.descriptor, json.RawMessage(p.raw), key)
			if e != nil {
				return copies, e
			}
			copy.in.ID = terminalLogicalID(t.Context, key.Fingerprint(), p.sub, p.descriptor.Seq)
			copy.in.LID = copy.in.ID
			copy.groupAdmission = "terminal:" + strconv.FormatInt(t.Context.State.Seq, 10) + ":" + t.Admission
			recipient, _ := key.Recipient()
			copy.env, e = envelope.Seal(copy.in, a.id.Sign, recipient)
			if e != nil {
				a.releaseGroupCopies([]outCopy{copy})
				return copies, e
			}
			copies = append(copies, copy)
		}
	}
	if t.Pending != "" {
		a.Logf("group terminal notice retained as proof-pending: %s", t.Pending)
	}
	return copies, nil
}

func (a *Agent) acceptGroupTerminal(ctx context.Context, p GroupContext, sender identity.Public) error {
	resolve, err := a.groupResolver(ctx, p)
	if err != nil {
		return err
	}
	m, err := a.verifyGroupTerminal(p, resolve)
	if err != nil {
		return err
	}
	person, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || person.roster.Person != m.Person || !person.has(a.Address, a.Self().Fingerprint()) || !person.roster.Human(a.Self().Fingerprint()) {
		return errors.New("group: terminal target is not this exact admitted human")
	}
	actor, ok, err := a.store.personByID(p.State.Actor)
	if err != nil {
		return err
	}
	if !ok || actor.info.State == personConflict || !actor.has(sender.Address, sender.Fingerprint()) || !actor.roster.Human(sender.Fingerprint()) || sender.Fingerprint() != p.State.By {
		return errors.New("group: terminal sender is not its pinned original removing administrator")
	}
	current, err := a.GroupContext(p.State.Conv)
	if err != nil {
		return err
	}
	if !sameGroupRoot(current.Root, p.Root) {
		return errors.New("group: terminal root differs")
	}
	if current.State.Seq >= p.State.Seq {
		return nil
	} // duplicates/old notices never revoke a newer readmission
	withdrawals, e := a.groupWithdrawals(p.State.Conv)
	if e != nil {
		return e
	}
	for _, w := range withdrawals {
		if w.Person == m.Person && w.Admission == m.Admission.Hash() {
			return errors.New("group: own departure supersedes administrator removal notice")
		}
	}
	own, ok := current.State.Member(m.Person)
	if !ok || own.Admission.Hash() != m.Admission.Hash() {
		return errors.New("group: terminal context differs from held own admission")
	}
	anchor := false
	for _, s := range p.Proof {
		anchor = anchor || s.Seq == current.State.Seq && s.Hash() == current.State.Hash()
	}
	if !anchor {
		return ErrGroupContextPending
	}
	// Keep the existing signed-state installer and transaction, adding only an
	// exact-parent compare fence rather than disabling forwarded target checks.
	return a.acceptGroupContext(ctx, p, false, &sender, func(_ context.Context, packet GroupContext, r protocol.GroupRosterResolver) error {
		_, e := a.verifyGroupTerminal(packet, r)
		return e
	})
}

func groupRemovalNotice(q dbq, p GroupContext, self string) string {
	if _, member := p.State.Member(self); member {
		return ""
	}
	m, err := terminalGroupMember(p)
	if err != nil || m.Person != self {
		return ""
	}
	by := "An administrator"
	if actor, ok, e := personByIDIn(q, p.State.Actor); e == nil && ok && actor.info.Label != "" {
		by = actor.info.Label
	}
	return fmt.Sprintf("%s removed you from this group. You can read earlier messages; you can't send.", by)
}

func (a *Agent) finishSupersededGroupTerminal(ctx context.Context, c protocol.GroupCommit, base GroupContext, t *groupTerminalPublication, exact []byte) (bool, error) {
	if eligible, err := groupSupersedingPublication(a.store.db, c); err != nil || !eligible {
		return false, err
	}
	// Newer ordinary state is retained; only this original terminal is eligible.
	current, err := a.GroupContext(c.Conv)
	if err != nil {
		return false, err
	}
	var copies []outCopy
	if _, readmitted := current.State.Member(t.Person); !readmitted {
		copies, err = a.groupTerminalCopies(ctx, t, base)
		if err != nil {
			return false, err
		}
	}
	stored := false
	defer func() {
		if !stored {
			a.releaseGroupCopies(copies)
		}
	}()
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if eligible, e := groupSupersedingPublication(tx, c); e != nil {
			return e
		} else if !eligible {
			return ErrGroupContextPending
		}
		var raw []byte
		var done int
		if e := tx.QueryRow("SELECT record,published FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?", c.Conv, c.Bootstrap, c.Seq).Scan(&raw, &done); e != nil {
			return e
		}
		if !bytes.Equal(raw, exact) {
			return errors.New("group: terminal publication changed")
		}
		if done != 0 {
			return errGroupPublished
		}
		for _, copy := range copies {
			if e := groupTerminalRecipient(tx, t, copy.env.To, copy.recipientFP); e != nil {
				return e
			}
		}
		_, e := tx.Exec("UPDATE group_publications SET published=1 WHERE conv=? AND bootstrap=? AND seq=?", c.Conv, c.Bootstrap, c.Seq)
		return e
	}, "")
	if errors.Is(err, errGroupPublished) {
		return true, nil
	}
	stored = err == nil
	if stored {
		a.kickNow()
	}
	return stored, err
}

func (a *Agent) mayDeliverGroupTerminal(env envelope.Envelope, sub, body, marker string) (bool, bool, error) {
	parts := strings.Split(marker, ":")
	if len(parts) != 3 || parts[0] != "terminal" || !protocol.ValidHash(parts[2]) {
		return true, false, errors.New("group: invalid retained terminal admission")
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || seq < 0 {
		return true, false, errors.New("group: invalid retained terminal sequence")
	}
	var raw []byte
	var fp, conv string
	var done int
	err = a.store.db.QueryRow(`SELECT p.payload,p.published,o.recipient_fp,o.conv FROM outbox o JOIN group_publications p ON p.conv=o.conv AND p.seq=? WHERE o.id=?`, seq, env.ID).Scan(&raw, &done, &fp, &conv)
	if err != nil {
		return true, false, err
	}
	var saved groupPublicationPayload
	if err = json.Unmarshal(raw, &saved); err != nil {
		return true, false, err
	}
	t := saved.Terminal
	var desc protocol.GroupCarrier
	if done != 1 || t == nil || t.Admission != parts[2] || t.Context.State.Conv != conv || decodeStrict([]byte(body), &desc) != nil || desc.ToKey != fp || env.ID != terminalLogicalID(t.Context, fp, sub, desc.Seq) {
		return true, false, errors.New("group: terminal retained copy differs from exact publication")
	}
	err = groupTerminalRecipient(a.store.db, t, env.To, fp)
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "Terminal removal notice stopped: "+err.Error(), "")
	}
	return true, true, nil
}

// Called in the installation transaction after complete terminal verification.
func (a *Agent) terminalInstallCheck(q dbq, p GroupContext) (terminal, old bool, err error) {
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil || !ok {
		return false, false, err
	}
	if _, member := p.State.Member(me.roster.Person); member || len(p.Proof) == 0 {
		return false, false, nil
	}
	m, e := terminalGroupMember(p)
	if e != nil || m.Person != me.roster.Person {
		return false, false, nil
	}
	if !me.has(a.Address, a.Self().Fingerprint()) || !me.roster.Human(a.Self().Fingerprint()) {
		return true, false, errors.New("group: terminal own device changed during install")
	}
	current, e := groupTurnPacketIn(q, p.State.Conv)
	if e != nil {
		return true, false, e
	}
	if current.State.Seq >= p.State.Seq {
		return true, true, nil
	}
	var departures int
	if e = q.QueryRow("SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?", p.State.Conv, m.Person, m.Admission.Hash()).Scan(&departures); e != nil {
		return true, false, e
	}
	if departures > 0 {
		return true, false, errors.New("group: own departure changed during terminal install")
	}
	own, present := current.State.Member(m.Person)
	if !present || own.Admission.Hash() != m.Admission.Hash() {
		return true, false, errors.New("group: terminal admission changed during install")
	}
	for _, s := range p.Proof {
		if s.Seq == current.State.Seq && s.Hash() == current.State.Hash() {
			return true, false, nil
		}
	}
	return true, false, ErrGroupContextPending
}

func stopRemovedGroupSends(q *sql.Tx, conv, notice string) error {
	_, err := q.Exec(`UPDATE outbox SET send_stopped=1,state=CASE WHEN handover_proven=1 AND handover_started=0 THEN 'not_delivered' ELSE 'failed' END,error=CASE WHEN handover_proven=1 AND handover_started=0 THEN 'Not sent: ' ELSE 'Sending stopped; previous delivery unconfirmed. ' END || ? WHERE conv=? AND coalesce(sub,'')='' AND state IN ('queued','waiting','receiver_waiting') AND send_stopped=0`, notice, conv)
	return err // retained ciphertext/files are intentionally not released
}
