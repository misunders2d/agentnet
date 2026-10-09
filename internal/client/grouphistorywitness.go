package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const groupHistoryWitnessSchema = `ALTER TABLE inbox ADD COLUMN group_history TEXT;`
const maxHistoryMemberships = 8 * (envelope.MaxHumanAudience + 1)

func groupHistoryJSON(p *GroupContext) string {
	if p == nil {
		return ""
	}
	b, _ := json.Marshal(p)
	return string(b)
}
func storedGroupHistory(q dbq, id string) (*GroupContext, error) {
	var raw string
	err := q.QueryRow(`SELECT coalesce(group_history,'') FROM inbox WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p GroupContext
	if err = decodeStrict([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func mergeHistoryEvents(events, proof []protocol.ParticipationEvent, pid string) []protocol.ParticipationEvent {
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Hash()] = true
	}
	for _, e := range proof {
		if e.PID == pid && !seen[e.Hash()] {
			events = append(events, e)
			seen[e.Hash()] = true
		}
	}
	return events
}
func historyWitnessPIDs(item HistoryItem) map[string]bool {
	pids := map[string]bool{item.PID: true}
	if item.Human != nil {
		for _, s := range item.Human.Audience {
			pids[s.PID] = true
		}
	}
	return pids
}

// groupHistoryMembers verifies an inert snapshot against the original public
// authority already accepted here. It cannot install context, membership,
// grants, jobs, or a new person roster. Present-day own-reader and forwarder
// checks remain the caller's outer fence.
func groupHistoryMembers(q dbq, root protocol.ConvRoot, item HistoryItem) (dmMembers, error) {
	p := item.GroupHistory
	if p == nil || !sameGroupRoot(root, p.Root) || len(p.Proof) != 0 || len(p.Memberships) == 0 || len(p.Memberships) > maxHistoryMemberships || item.PID == "" {
		return dmMembers{}, errors.New("group: malformed historical authority witness")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return dmMembers{}, err
	}
	if len(raw) > protocol.MaxGroupState {
		return dmMembers{}, errors.New("group: historical witness exceeds bound")
	}
	authority, err := groupProofRecord(q, root.ID(), root.Creator.Fingerprint, p.State.Seq)
	if err != nil {
		return dmMembers{}, err
	}
	var resolveErr error
	resolve := func(person, hash string) (protocol.PersonRoster, bool) {
		current, ok, e := personByIDIn(q, person)
		if e != nil {
			resolveErr = e
			return protocol.PersonRoster{}, false
		}
		if !ok || current.info.State != personSelf && current.info.State != personPinned {
			return protocol.PersonRoster{}, false
		}
		var raw string
		e = q.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, person, hash).Scan(&raw)
		if e != nil {
			if !errors.Is(e, sql.ErrNoRows) {
				resolveErr = e
			}
			return protocol.PersonRoster{}, false
		}
		var r protocol.PersonRoster
		e = json.Unmarshal([]byte(raw), &r)
		if e != nil {
			resolveErr = e
		}
		return r, e == nil && r.Person == person && r.Hash() == hash
	}
	slots := map[int64]protocol.GroupCommit{}
	for _, m := range p.State.Members {
		c, e := groupProofRecord(q, root.ID(), root.Creator.Fingerprint, m.Admission.Seq)
		if e != nil {
			return dmMembers{}, e
		}
		slots[c.Seq] = c
	}
	for _, w := range p.Withdrawals {
		if err = w.Verify(p.State, resolve); err != nil {
			return dmMembers{}, err
		}
	}
	err = p.State.VerifyCurrent(root, authority, resolve, func(seq int64) (protocol.GroupCommit, bool) { c, ok := slots[seq]; return c, ok }, p.Withdrawals)
	if resolveErr != nil {
		return dmMembers{}, resolveErr
	}
	if err != nil {
		return dmMembers{}, err
	}
	pids := historyWitnessPIDs(item)
	bound := false
	for _, ev := range p.Memberships {
		if ev.Conv != root.ID() || !pids[ev.PID] || ev.Type == protocol.EventShare {
			return dmMembers{}, errors.New("group: historical witness outside exact captured participation")
		}
		author, ok, e := personByIDIn(q, ev.Author.Person)
		if e != nil {
			return dmMembers{}, e
		}
		if !ok || author.info.State != personSelf && author.info.State != personPinned || !author.has(ev.Author.Address, ev.Author.Fingerprint) {
			return dmMembers{}, errors.New("group: historical event author key changed")
		}
		pin, found, e := pinnedKey(q, ev.Author.Address)
		if e != nil {
			return dmMembers{}, e
		}
		var pending int
		if e = q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, ev.Author.Address).Scan(&pending); e != nil {
			return dmMembers{}, e
		}
		if pending != 0 || found && pin.Fingerprint() != ev.Author.Fingerprint {
			return dmMembers{}, errors.New("group: historical event author pin changed")
		}
		roster, ok := resolve(ev.Author.Person, ev.Author.Roster)
		if !ok {
			return dmMembers{}, ErrGroupContextPending
		}
		key, ok := roster.Device(ev.Author.Fingerprint)
		if !ok || key.Address != ev.Author.Address {
			return dmMembers{}, errors.New("group: historical event lacks exact signed roster")
		}
		if err = ev.Verify(key.SignKey); err != nil {
			return dmMembers{}, err
		}
		if ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope {
			member, ok := p.State.Member(ev.Author.Person)
			if !ok || ev.Author.GroupAdmission != member.Admission.Hash() {
				return dmMembers{}, errors.New("group: historical inviter original admission differs")
			}
		}
		if ev.PID == item.PID && (ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope) && ev.Group != nil && ev.Group.Seq == p.State.Seq && ev.Group.Hash == p.State.Hash() {
			bound = true
		}
	}
	if !bound {
		return dmMembers{}, errors.New("group: historical state is not bound to item participation")
	}
	var members []protocol.ConvMember
	for _, m := range p.State.EffectiveMembers(p.Withdrawals) {
		members = append(members, m.ConvMember)
	}
	m, err := memberRowsIn(q, root, members)
	if err != nil {
		return dmMembers{}, err
	}
	m.group = p
	// Validate historical bindings without the live room-event retention exception.
	// A witnessed state proves original author/host/task epochs directly.
	if err = m.loadHosts(q, p.Memberships); err != nil {
		return dmMembers{}, err
	}
	m.roomEvents = nil
	m.roomAuthors = nil
	m.historyTaskEpochs = map[string]string{}
	for _, ev := range p.Memberships {
		if ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope {
			// A device removed since this invitation remains part of its signed
			// original task-key list. Resolve only that historical binding, never
			// restore the device as a current author, reader or task executor.
			roster, ok := resolve(ev.Author.Person, ev.Author.Roster)
			member, present := p.State.Member(ev.Author.Person)
			if ok && present && ev.Group != nil {
				for i, fp := range ev.TaskKeys {
					if i >= len(ev.Group.TaskAdmissions) || m.keyEpoch(fp) != "" {
						continue
					}
					if _, retained := roster.Device(fp); retained && member.Admission.Hash() == ev.Group.TaskAdmissions[i] {
						m.historyTaskEpochs[fp] = member.Admission.Hash()
					}
				}
			}
			valid, e := m.verifyInviteEpoch(q, ev)
			if e != nil {
				return dmMembers{}, e
			}
			if !valid {
				return dmMembers{}, errors.New("group: historical invite epochs differ from signed original state")
			}
			m.groupInvites[ev.Hash()] = true
		}
	}
	m.historyEvents = p.Memberships
	return m, nil
}

// Reconstruct only the exact originally encrypted state. A new device which
// could not decrypt it uses its retained received witness; absence stays pending.
func (a *Agent) makeGroupHistoryWitness(q dbq, current GroupContext, item HistoryItem) (*GroupContext, error) {
	item, err := groupHistoryScope(q, current.State.Conv, item, a.Self().Fingerprint())
	if err != nil {
		return nil, err
	}
	events, err := participationEventsIn(q, current.State.Conv, item.PID)
	if err != nil {
		return nil, err
	}
	if item.Human != nil {
		events = mergeHistoryEvents(events, item.Human.Proof, item.PID)
	}
	var scope *protocol.ParticipationGroup
	for _, ev := range events {
		if (ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope) && ev.Group != nil {
			if scope != nil && (scope.Seq != ev.Group.Seq || scope.Hash != ev.Group.Hash) {
				return nil, errors.New("group: ambiguous historical participation state")
			}
			scope = ev.Group
		}
	}
	if scope == nil {
		return nil, ErrGroupContextPending
	}
	record, err := groupProofRecord(q, current.State.Conv, current.Root.Creator.Fingerprint, scope.Seq)
	if err != nil {
		return nil, err
	}
	if record.Hash != scope.Hash {
		return nil, errors.New("group: historical scope differs from original authority")
	}
	reader, err := age.Decrypt(bytes.NewReader(record.Ciphertext), a.id.Box)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, errors.Join(ErrGroupContextPending, errGroupCiphertextUnavailable)
		}
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, protocol.MaxGroupState+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > protocol.MaxGroupState {
		return nil, errors.New("group: original context exceeds bound")
	}
	var p GroupContext
	if err = decodeStrict(raw, &p); err != nil {
		return nil, err
	}
	if !record.Matches(p.State) || !sameGroupRoot(current.Root, p.Root) {
		return nil, errors.New("group: original context differs from exact signed record")
	}
	p.Proof = nil
	p.Memberships = nil
	pids := make([]string, 0)
	for pid := range historyWitnessPIDs(item) {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	for _, pid := range pids {
		kept, e := participationEventsIn(q, current.State.Conv, pid)
		if e != nil {
			return nil, e
		}
		if item.Human != nil {
			kept = mergeHistoryEvents(kept, item.Human.Proof, pid)
		}
		for _, ev := range kept {
			if ev.Type != protocol.EventShare {
				p.Memberships = append(p.Memberships, ev)
			}
		}
	}
	item.GroupHistory = &p
	if _, err = groupHistoryMembers(q, current.Root, item); err != nil {
		return nil, err
	}
	return &p, nil
}

// A status has no PID on the wire. Its immutable exact request supplies the
// same scope; an absent or conflicting request remains a dependency.
func groupHistoryScope(q dbq, conv string, item HistoryItem, selfFP string) (HistoryItem, error) {
	if item.Sub != envelope.SubStatus {
		return item, nil
	}
	if item.Ref == nil {
		return item, errors.New("group: historical status lacks exact request")
	}
	rows, err := q.Query(`SELECT coalesce(pid,''),coalesce(human,'') FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp,'')=? AND kind IN ('question','task') AND sub IS NULL
 UNION ALL SELECT coalesce(pid,''),coalesce(human,'') FROM outbox WHERE conv=? AND lid=? AND ?=? AND kind IN ('question','task') AND sub IS NULL`, conv, item.Ref.ID, item.Ref.Fingerprint, conv, item.Ref.ID, selfFP, item.Ref.Fingerprint)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	var pid, human string
	found := false
	for rows.Next() {
		var p, h string
		if err = rows.Scan(&p, &h); err != nil {
			return item, err
		}
		if p == "" || found && (p != pid || h != human) {
			return item, errors.New("group: conflicting historical status scope")
		}
		pid, human, found = p, h, true
	}
	if err = rows.Err(); err != nil {
		return item, err
	}
	if !found {
		return item, ErrGroupContextPending
	}
	item.PID = pid
	if human != "" {
		if err = json.Unmarshal([]byte(human), &item.Human); err != nil {
			return item, err
		}
	}
	return item, nil
}

// Reuse exact source authority for both immutable history and its file bytes.
// q keeps original rows, current own membership, and the witness in one snapshot.
func (a *Agent) groupParticipationHistorySource(q dbq, packet GroupContext, item HistoryItem) (HistoryItem, error) {
	var err error
	if item.GroupHistory == nil {
		item.GroupHistory, err = storedGroupHistory(q, item.ID)
		if err != nil {
			return item, err
		}
	}
	stamp, err := a.groupParticipationSourceAdmission(q, packet, item)
	if err != nil && item.GroupHistory == nil && (errors.Is(err, ErrGroupContextPending) || errors.Is(err, errGroupParticipationHistoryEpoch) || errors.Is(err, errHumanHistoryConsent)) {
		item.GroupHistory, err = a.makeGroupHistoryWitness(q, packet, item)
		if err == nil {
			stamp, err = a.groupParticipationSourceAdmission(q, packet, item)
		}
	}
	if err != nil {
		return item, err
	}
	item.GroupAdmission = stamp
	return item, nil
}
