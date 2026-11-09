package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A local completeness checkpoint binds exact revocations to the head actually
// fetched from the authenticated Hub. It is not a successor record or grant.
const continuityHeadPrefix = "continuity-head/"

// One immutable alias map per exact input state (continuityGenerationSchema).
// History catch-up asks for it on every proof check of every item, and each
// computation parses and verifies every pinned roster.
const continuityAliasesKept = 32

var continuityAliases = struct {
	sync.Mutex
	byToken map[string]map[string]string
}{byToken: map[string]map[string]string{}}

// continuityResolves counts real computations, for the regression test.
var continuityResolves atomic.Int64

func (s *store) Query(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(query, args...)
}
func (s *store) QueryRow(query string, args ...any) *sql.Row { return s.db.QueryRow(query, args...) }

// Missing map entries mean identity. With no checkpoint, do not parse persons.
// Native grants and historical signature verification still use exact IDs.
// The result is computed from current rows whenever those rows changed, in the
// caller's own transaction too; this never remembers authority across a change.
func logicalPersons(q dbq) (map[string]string, error) {
	if have, e := hasContinuity(q); e != nil || !have {
		return nil, e
	}
	var token string
	if e := q.QueryRow(`SELECT token FROM continuity_generation`).Scan(&token); e != nil {
		return nil, e
	}
	continuityAliases.Lock()
	aliases, ok := continuityAliases.byToken[token]
	continuityAliases.Unlock()
	if ok {
		return aliases, nil
	}
	continuityResolves.Add(1)
	aliases, e := resolveLogicalPersons(q)
	if e != nil {
		return nil, e
	}
	// Outside a transaction a write may land between the statements above.
	var after string
	if q.QueryRow(`SELECT token FROM continuity_generation`).Scan(&after) == nil && after == token {
		continuityAliases.Lock()
		if len(continuityAliases.byToken) >= continuityAliasesKept {
			clear(continuityAliases.byToken)
		}
		continuityAliases.byToken[token] = aliases
		continuityAliases.Unlock()
	}
	return aliases, nil
}

func resolveLogicalPersons(q dbq) (map[string]string, error) {
	rows, e := q.Query(`SELECT person,record,state,coalesce(conflict,'') FROM persons ORDER BY person`)
	if e != nil {
		return nil, e
	}
	type candidate struct {
		r               protocol.PersonRoster
		state, conflict string
	}
	var all []candidate
	for rows.Next() {
		var id, raw, state, conflict string
		if e = rows.Scan(&id, &raw, &state, &conflict); e != nil {
			rows.Close()
			return nil, e
		}
		r, err := protocol.ParsePersonRoster([]byte(raw))
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, candidate{r, state, conflict})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	result := map[string]string{}
	groups := map[string][]candidate{}
	for _, p := range all {
		result[p.r.Person] = p.r.Person
		if p.r.Email != "" && (p.state == personPinned || p.state == personSelf) && p.conflict == "" {
			groups[p.r.Email] = append(groups[p.r.Email], p)
		}
	}
	for _, ps := range groups {
		var terminal string
		var left []string
		ambiguous := false
		for _, p := range ps {
			var checkpoint string
			e = q.QueryRow(`SELECT v FROM config WHERE k=?`, continuityHeadPrefix+p.r.Person).Scan(&checkpoint)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			gone, e := personLeftIn(q, p.r.Person)
			if e != nil {
				return nil, e
			}
			if gone && checkpoint == p.r.Hash() {
				left = append(left, p.r.Person)
			} else {
				if terminal != "" {
					ambiguous = true
				}
				terminal = p.r.Person
			}
		}
		if !ambiguous && terminal != "" {
			for _, id := range left {
				result[id] = terminal
			}
		}
	}
	return result, nil
}
func (s *store) logicalPerson(id string) (string, error) {
	m, e := logicalPersons(s)
	if e != nil {
		return "", e
	}
	if to := m[id]; to != "" {
		return to, nil
	}
	return id, nil
}

// refreshContinuity never runs a worker or releases execution claims. All old
// heads are refreshed before their devices are checked, including a stale pin.
func (a *Agent) refreshContinuity(ctx context.Context) error {
	var email bool
	if e := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM persons WHERE state='conflict' AND conflict IS NULL AND coalesce(json_extract(record,'$.email'),'')!='') OR EXISTS(SELECT 1 FROM persons WHERE state IN ('pinned','self') AND coalesce(json_extract(record,'$.email'),'')!='' GROUP BY json_extract(record,'$.email') HAVING count(*)>1)`).Scan(&email); e != nil || !email {
		return e
	}
	// Older clients may have frozen the new identity before learning that
	// the email holder's devices were revoked. Retry those claims before
	// counting pinned identities; step conflicts must remain frozen.
	claims, e := a.store.db.Query(`SELECT person FROM persons WHERE state=? AND conflict IS NULL AND coalesce(json_extract(record,'$.email'),'')!='' ORDER BY person`, personConflict)
	if e != nil {
		return e
	}
	var held []string
	for claims.Next() {
		var id string
		if e = claims.Scan(&id); e != nil {
			claims.Close()
			return e
		}
		held = append(held, id)
	}
	e = claims.Err()
	claims.Close()
	if e != nil {
		return e
	}
	for _, id := range held {
		if _, e = a.refreshPerson(ctx, id, false); e != nil && !errors.Is(e, errPersonConflict) {
			return e
		}
	}
	rows, e := a.store.db.Query(`SELECT person,record FROM persons WHERE state IN (?,?) AND coalesce(json_extract(record,'$.email'),'')!=''`, personPinned, personSelf)
	if e != nil {
		return e
	}
	type entry struct{ id, email string }
	var entries []entry
	counts := map[string]int{}
	for rows.Next() {
		var id, raw string
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return e
		}
		r, err := protocol.ParsePersonRoster([]byte(raw))
		if err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, entry{id, r.Email})
		counts[r.Email]++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	for _, entry := range entries {
		if counts[entry.email] < 2 {
			continue
		}
		p, ok, e := a.store.personByID(entry.id)
		if e != nil {
			return e
		}
		if !ok || p.info.State == personSelf {
			continue
		}
		if e = a.refreshContinuityPerson(ctx, p); e != nil {
			return e
		}
	}
	return nil
}

// MessagingPersons is a directory projection, never a source of grants.
func (a *Agent) MessagingPersons() ([]PersonInfo, error) {
	ps, e := a.KnownPersons()
	if e != nil {
		return nil, e
	}
	aliases, e := logicalPersons(a.store)
	if e != nil {
		return nil, e
	}
	out := ps[:0]
	for _, p := range ps {
		if to := aliases[p.Person]; to == "" || to == p.Person {
			out = append(out, p)
		}
	}
	return out, nil
}

func (a *Agent) continuityDMs(conv string) ([]ConversationInfo, error) {
	if have, e := hasContinuity(a.store.db); e != nil || !have {
		return nil, e
	}
	rows, e := a.store.conversations()
	if e != nil {
		return nil, e
	}
	aliases, e := logicalPersons(a.store)
	if e != nil {
		return nil, e
	}
	var peer string
	for _, r := range rows {
		if r.ID == conv && r.Kind == protocol.ConvKindDM {
			peer = aliases[r.Peer.Person]
			break
		}
	}
	if peer == "" {
		return nil, nil
	}
	recovered := false
	for id, to := range aliases {
		if id != to && to == peer {
			recovered = true
		}
	}
	var out []ConversationInfo
	for _, r := range rows {
		if r.ID == conv || recovered && r.Kind == protocol.ConvKindDM && aliases[r.Peer.Person] == peer {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func (a *Agent) ContinuityConversation(conv string) (string, error) {
	rows, e := a.continuityDMs(conv)
	if e != nil {
		return "", e
	}
	if len(rows) > 0 {
		return rows[0].ID, nil
	}
	return conv, nil
}
func (a *Agent) projectContinuityDMs(rows []ConversationInfo) ([]ConversationInfo, error) {
	if have, e := hasContinuity(a.store.db); e != nil || !have {
		return rows, e
	}
	aliases, e := logicalPersons(a.store)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	out := rows[:0]
	for _, r := range rows {
		if r.Kind != protocol.ConvKindDM {
			out = append(out, r)
			continue
		}
		view, e := a.ContinuityConversation(r.ID)
		if e != nil {
			return nil, e
		}
		if seen[view] {
			continue
		}
		seen[view] = true
		r.ID = view
		if to := aliases[r.Peer.Person]; to != "" && to != r.Peer.Person {
			p, ok, e := a.store.personByID(to)
			if e != nil {
				return nil, e
			}
			if ok {
				r.Peer = p.info
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (a *Agent) currentDM(ctx context.Context, conv string) (string, error) {
	rows, e := a.continuityDMs(conv)
	if e != nil || len(rows) == 0 {
		return conv, e
	}
	peer, e := a.store.logicalPerson(rows[0].Peer.Person)
	if e != nil {
		return "", e
	}
	for _, r := range rows {
		if r.Peer.Person == peer {
			return r.ID, nil
		}
	}
	p, ok, e := a.store.personByID(peer)
	if e != nil {
		return "", e
	}
	if !ok || len(p.roster.Devices) == 0 {
		return "", errPersonRecord
	}
	return a.CreateDM(ctx, p.roster.Devices[0].Address)
}

func messagingPersonIn(q dbq, id string) (personRow, bool, error) {
	if have, e := hasContinuity(q); e != nil {
		return personRow{}, false, e
	} else if !have {
		return personByIDIn(q, id)
	}
	aliases, e := logicalPersons(q)
	if e != nil {
		return personRow{}, false, e
	}
	if to := aliases[id]; to != "" {
		id = to
	}
	return personByIDIn(q, id)
}

// Removal and withdrawal apply to the whole logical family. Only an explicit
// later admission can supersede them; signed state/admissions stay intact.
func logicalGroupMembers(q dbq, p GroupContext) ([]protocol.GroupMember, error) {
	aliases, e := logicalPersons(q)
	if e != nil {
		return nil, e
	}
	logical := func(person string) string {
		if id := aliases[person]; id != "" {
			return id
		}
		return person
	}
	withdrawn := map[string]bool{}
	withdrawnPeople := map[string]bool{}
	rows, e := q.Query(`SELECT person,admission FROM group_withdrawals WHERE conv=?`, p.State.Conv)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var person, admission string
		if e = rows.Scan(&person, &admission); e != nil {
			rows.Close()
			return nil, e
		}
		withdrawn[person+"/"+admission] = true
		withdrawnPeople[person] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, w := range p.Withdrawals {
		if w.Conv == p.State.Conv && w.Realm == p.State.Realm {
			withdrawn[w.Person+"/"+w.Admission] = true
			withdrawnPeople[w.Person] = true
		}
	}
	if len(aliases) == 0 {
		out := []protocol.GroupMember{}
		for _, m := range p.State.Members {
			if !withdrawnPeople[m.Person] || !withdrawn[m.Person+"/"+m.Admission.Hash()] {
				out = append(out, m)
			}
		}
		return out, nil
	}
	blockedThrough := map[string]int64{}
	block := func(person string, seq int64) {
		id := logical(person)
		if old, ok := blockedThrough[id]; !ok || seq > old {
			blockedThrough[id] = seq
		}
	}
	states := append(append([]protocol.GroupState(nil), p.Proof...), p.State)
	for i, state := range states {
		for _, m := range state.Members {
			if withdrawnPeople[m.Person] && withdrawn[m.Person+"/"+m.Admission.Hash()] {
				block(m.Person, m.Admission.Seq)
			}
			if i+1 < len(states) {
				if _, present := states[i+1].Member(m.Person); !present {
					block(m.Person, states[i+1].Seq)
				}
			}
		}
	}
	chosen := map[string]protocol.GroupMember{}
	for _, m := range p.State.Members {
		id := logical(m.Person)
		if seq, blocked := blockedThrough[id]; blocked && m.Admission.Seq <= seq {
			continue
		}
		old, exists := chosen[id]
		if !exists || m.Person == id && old.Person != id || m.Person != id && old.Person != id && (m.Admission.Seq > old.Admission.Seq || m.Admission.Seq == old.Admission.Seq && m.Person < old.Person) {
			chosen[id] = m
		}
	}
	keys := make([]string, 0, len(chosen))
	for id := range chosen {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	out := make([]protocol.GroupMember, 0, len(keys))
	for _, id := range keys {
		out = append(out, chosen[id])
	}
	return out, nil
}

func (a *Agent) refreshContinuityPerson(ctx context.Context, p personRow) error {
	// Verify from the pinned chain without email settlement recursion.
	raws, e := a.fetchChain(ctx, p.roster.Person, p.info.Seq-1)
	if e != nil {
		return e
	}
	if len(raws) == 0 {
		return errPersonRecord
	}
	candidate := p.roster
	for _, raw := range raws {
		next, err := protocol.ParsePersonRoster(raw)
		if err != nil {
			return err
		}
		if next.Person != p.roster.Person {
			return errPersonRecord
		}
		if next.Seq == candidate.Seq {
			if next.Hash() != candidate.Hash() {
				_, err = a.store.pinChain(p.roster.Person, raws, a.Self(), false)
				if err != nil {
					return err
				}
				return errPersonConflict
			}
			continue
		}
		if _, err = next.VerifyNext(candidate); err != nil {
			return err
		}
		candidate = next
	}
	if candidate.Hash() != p.roster.Hash() {
		if e = a.store.deleteConfig(continuityHeadPrefix + p.roster.Person); e != nil {
			return e
		}
	}
	all := true
	for _, d := range candidate.Devices {
		directory, e := a.directory(ctx, d.Address)
		if e != nil {
			return e
		}
		if !directory.Revoked || directory.Public.Fingerprint() != d.Fingerprint() {
			all = false
			continue
		}
		if e = a.store.endRecipient(d.Address, d.Fingerprint(), true); e != nil {
			return e
		}
	}
	if _, e = a.store.pinChain(p.roster.Person, raws, a.Self(), false); e != nil {
		return e
	}
	if all && len(candidate.Devices) > 0 {
		if e = a.store.setConfig(map[string]string{continuityHeadPrefix + p.roster.Person: candidate.Hash()}); e != nil {
			return e
		}
	}
	return nil
}

func sameLogicalDMIn(q dbq, left, right string) (bool, error) {
	if have, e := hasContinuity(q); e != nil || !have {
		return left == right, e
	}
	if left == right {
		return true, nil
	}
	a, _, ok, e := conversationIn(q, left)
	if e != nil || !ok {
		return false, e
	}
	b, _, ok, e := conversationIn(q, right)
	if e != nil || !ok || a.Kind != protocol.ConvKindDM || b.Kind != protocol.ConvKindDM {
		return false, e
	}
	aliases, e := logicalPersons(q)
	if e != nil {
		return false, e
	}
	normalize := func(id string) string {
		if v := aliases[id]; v != "" {
			return v
		}
		return id
	}
	ids := map[string]bool{}
	recovered := false
	for _, m := range a.Members {
		ids[normalize(m.Person)] = true
	}
	for _, m := range b.Members {
		if !ids[normalize(m.Person)] {
			return false, nil
		}
	}
	for from, to := range aliases {
		if from != to && ids[to] {
			recovered = true
		}
	}
	return recovered, nil
}
func (a *Agent) continuityReplyParent(conv, ref string) (string, bool, error) {
	if have, e := hasContinuity(a.store.db); e != nil || !have {
		if e != nil {
			return "", false, e
		}
		return humanReplyParent(a.store.db, conv, ref)
	}
	parent, known, e := humanReplyParent(a.store.db, conv, ref)
	if e == nil && known {
		return parent, known, nil
	}
	var source string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,'') FROM inbox WHERE (id=? OR lid=?) AND ref_id IS NULL UNION SELECT coalesce(conv,'') FROM outbox WHERE (id=? OR lid=?) AND ref_id IS NULL LIMIT 1`, ref, ref, ref, ref).Scan(&source)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if source == "" {
		return parent, known, e
	}
	same, err := sameLogicalDMIn(a.store.db, conv, source)
	if err != nil {
		return "", false, err
	}
	if !same {
		return parent, known, e
	}
	return humanReplyParent(a.store.db, source, ref)
}

func (a *Agent) MessagingConversations() ([]ConversationInfo, error) {
	rows, e := a.Conversations()
	if e != nil {
		return nil, e
	}
	return a.projectContinuityDMs(rows)
}

// Local-only rows are excluded from signed history and native work. Their
// deterministic IDs survive retries, duplicate catch-up and process restarts.
func (a *Agent) continuityNotices(conv string, family []ConversationInfo) ([]ConvMessage, error) {
	if have, e := hasContinuity(a.store.db); e != nil || !have {
		return nil, e
	}
	aliases, e := logicalPersons(a.store)
	if e != nil {
		return nil, e
	}
	targets := map[string]bool{}
	if len(family) > 0 {
		for _, r := range family {
			if to := aliases[r.Peer.Person]; to != "" && to != r.Peer.Person {
				targets[to] = true
			}
		}
	} else {
		packet, err := groupTurnPacketIn(a.store.db, conv)
		if err == nil {
			ms, err := logicalGroupMembers(a.store, packet)
			if err != nil {
				return nil, err
			}
			current := map[string]bool{}
			for _, m := range ms {
				id := aliases[m.Person]
				if id == "" {
					id = m.Person
				}
				current[id] = true
			}
			history := append([]protocol.ConvMember{}, packet.Root.Members...)
			for _, m := range packet.State.Members {
				history = append(history, m.ConvMember)
			}
			for _, state := range packet.Proof {
				for _, m := range state.Members {
					history = append(history, m.ConvMember)
				}
			}
			for _, m := range history {
				if to := aliases[m.Person]; to != "" && to != m.Person && current[to] {
					targets[to] = true
				}
			}
		}
	}
	for target := range targets {
		p, ok, err := a.store.personByID(target)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		scope := conv
		if len(family) > 0 {
			scope = "dm/" + target
		}
		sum := sha256.Sum256([]byte("agentnet-local-continuity-v1\n" + scope + "\n" + target))
		id := "continuity/" + hex.EncodeToString(sum[:])
		var exists bool
		if e = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE id=? AND local=1 AND sub='continuity')`, id).Scan(&exists); e != nil {
			return nil, e
		}
		if exists {
			continue
		}
		now := time.Now().UnixMilli()
		_, e = a.store.db.Exec(`INSERT OR IGNORE INTO inbox(id,sender,ts,kind,body,received_at,received_ms,read_at,acked,local,conv,lid,sub) VALUES(?, ?, ?, 'message', ?, ?, ?, ?,1,1,?,?,'continuity')`, id, a.Address, now/1000, p.info.Label+" signed in again", now/1000, now, now/1000, conv, id)
		if e != nil {
			return nil, e
		}
	}
	// Older transition notices stay in the same union when another recovery occurs.
	ids := []string{conv}
	if len(family) > 0 {
		ids = nil
		for _, r := range family {
			ids = append(ids, r.ID)
		}
	}
	var out []ConvMessage
	for _, id := range ids {
		rows, err := a.store.db.Query(`SELECT id,body,received_at,received_ms FROM inbox WHERE conv=? AND local=1 AND sub='continuity'`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m ConvMessage
			if err = rows.Scan(&m.ID, &m.Body, &m.At, &m.AtMS); err != nil {
				rows.Close()
				return nil, err
			}
			m.LID = m.ID
			m.Kind = "message"
			m.Sub = "continuity"
			m.Dir = "in"
			out = append(out, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func logicalGroupMember(q dbq, p GroupContext, person string) (protocol.GroupMember, bool, error) {
	aliases, e := logicalPersons(q)
	if e != nil {
		return protocol.GroupMember{}, false, e
	}
	target := aliases[person]
	if target == "" {
		target = person
	}
	ms, e := logicalGroupMembers(q, p)
	if e != nil {
		return protocol.GroupMember{}, false, e
	}
	for _, m := range ms {
		id := aliases[m.Person]
		if id == "" {
			id = m.Person
		}
		if id == target {
			return m, true, nil
		}
	}
	return protocol.GroupMember{}, false, nil
}

// Reuse the existing proof/current-context carriers and durable outbox batch
// markers. Recovery shares current group state, never historical ciphertext or
// execution grants, and never asks a group admin to sign another membership.
func (a *Agent) syncContinuityGroups(ctx context.Context) error {
	if have, e := hasContinuity(a.store.db); e != nil || !have {
		return e
	}
	aliases, e := logicalPersons(a.store)
	if e != nil {
		return e
	}
	recovered := map[string]bool{}
	for from, to := range aliases {
		if from != to {
			recovered[to] = true
		}
	}
	if len(recovered) == 0 {
		return nil
	}
	rows, e := a.store.db.Query(`SELECT conv FROM group_context ORDER BY conv`)
	if e != nil {
		return e
	}
	var convs []string
	for rows.Next() {
		var conv string
		if e = rows.Scan(&conv); e != nil {
			rows.Close()
			return e
		}
		convs = append(convs, conv)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	release, e := lockfile.Wait(a.spoolLockPath())
	if e != nil {
		return e
	}
	defer release()
	for _, conv := range convs {
		packet, e := groupTurnPacketIn(a.store.db, conv)
		if e != nil {
			return e
		}
		if e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); e != nil {
			continue
		}
		members, e := logicalGroupMembers(a.store, packet)
		if e != nil {
			return e
		}
		payloads, e := groupDeliveryPayloads(a.store.db, packet)
		if e != nil {
			return e
		}
		for _, m := range members {
			p, ok, e := messagingPersonIn(a.store, m.Person)
			if e != nil {
				return e
			}
			if !ok || !recovered[p.roster.Person] {
				continue
			}
			for _, dev := range p.roster.Devices {
				if dev.Address == a.Address {
					continue
				}
				present, e := a.groupHistoryBatchPresent(a.store.db, dev, packet, payloads)
				if e != nil {
					return e
				}
				if present {
					continue
				}
				copies, e := a.groupDeliveryTo(ctx, packet, payloads, dev)
				if e != nil {
					return e
				}
				e = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
					if e := groupTurnCheck(tx, packet, a.Address, a.Self().Fingerprint()); e != nil {
						return e
					}
					if e := groupTurnCheck(tx, packet, dev.Address, dev.Fingerprint()); e != nil {
						return e
					}
					present, e := a.groupHistoryBatchPresent(tx, dev, packet, payloads)
					if e != nil {
						return e
					}
					if present {
						return errGroupPublished
					}
					return nil
				}, "")
				if e != nil {
					a.releaseGroupCopies(copies)
					if !errors.Is(e, errGroupPublished) {
						return e
					}
				}
			}
		}
	}
	return nil
}

// UI controls keep the selected message's original signed conversation. A
// merged view is not permission to edit or react under a different root.
func (a *Agent) MessagingRefOf(conv, id, dir string) (ControlRef, error) {
	ref, e := a.RefOf(conv, id, dir)
	if e == nil || !errors.Is(e, ErrNoMessage) {
		return ref, e
	}
	family, err := a.continuityDMs(conv)
	if err != nil {
		return ControlRef{}, err
	}
	for _, c := range family {
		if c.ID == conv {
			continue
		}
		ref, err = a.RefOf(c.ID, id, dir)
		if err == nil {
			return ref, nil
		}
		if !errors.Is(err, ErrNoMessage) {
			return ControlRef{}, err
		}
	}
	return ref, e
}

const continuityHeadQuery = `SELECT EXISTS(SELECT 1 FROM config WHERE k >= 'continuity-head/' AND k < 'continuity-head0')`

func hasContinuity(q dbq) (bool, error) {
	var exists bool
	e := q.QueryRow(continuityHeadQuery).Scan(&exists)
	return exists, e
}

// MessagingGroupMembership exposes the original signed admission selected for
// the logical current person. It is display/messaging metadata, not a grant.
func (a *Agent) MessagingGroupMembership(conv, person string) (protocol.GroupMember, bool, error) {
	p, e := groupTurnPacketIn(a.store.db, conv)
	if e != nil {
		return protocol.GroupMember{}, false, e
	}
	return logicalGroupMember(a.store, p, person)
}
