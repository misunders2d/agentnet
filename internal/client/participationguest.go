package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The parent is its exact acceptance, found by the existing indexed hash. No
// label, guessed PID, historical replica or later reinvitation can replace it.
func guestParentIn(q dbq, conv, hash string, m dmMembers) (ParticipationInfo, error) {
	// participationIn rebuilds host evidence. Its operation must not clear a
	// sibling parent's map in the caller's captured audience.
	m.hosts = map[string]personRow{}
	m.chains = cloneChains(m.chains)
	var pid string
	var events []protocol.ParticipationEvent
	var err error
	if m.historyEvents != nil {
		for _, e := range m.historyEvents {
			if e.Conv == conv && e.Type == protocol.EventAccept && e.Hash() == hash {
				pid = e.PID
			}
		}
		if pid == "" {
			return ParticipationInfo{}, ErrGroupContextPending
		}
		for _, e := range m.historyEvents {
			if e.PID == pid {
				events = append(events, e)
			}
		}
	} else {
		err = q.QueryRow(`SELECT pid FROM participation_events WHERE hash=? AND conv=? AND type=?`, hash, conv, protocol.EventAccept).Scan(&pid)
	}
	if err != nil {
		return ParticipationInfo{}, err
	}
	if m.historyEvents == nil {
		events, err = participationEventsIn(q, conv, pid)
	}
	if err != nil {
		return ParticipationInfo{}, err
	}
	human := false
	for _, e := range events {
		if e.Parent != "" {
			return ParticipationInfo{}, errors.New("participation: a derived agent cannot be a parent")
		}
		human = human || (e.Type == protocol.EventInvite || e.Type == protocol.EventScope) && e.Role == protocol.RoleHuman
	}
	if !human {
		return ParticipationInfo{}, errors.New("participation: parent has no human scope")
	}
	var p ParticipationInfo
	if m.historyEvents != nil {
		err = m.loadHosts(q, events)
		if err == nil {
			p = resolve(conv, pid, events, m)
		}
	} else {
		p, err = participationIn(q, conv, pid, m, "")
	}
	if err != nil {
		return p, err
	}
	if p.Role != protocol.RoleHuman || p.Parent != "" || p.Invite == "" || p.Decision != hash || p.Topic == nil || *p.Topic == "" || p.Held != 0 {
		return p, errors.New("participation: exact human parent is not verified")
	}
	owner, ok, err := personByIDIn(q, p.Host.Person)
	if err != nil || !ok || !owner.has(p.Host.Address, p.Host.Fingerprint) || !owner.roster.Human(p.Host.Fingerprint) {
		return p, errors.New("participation: parent is not a current human device")
	}
	return p, nil
}

func cloneChains(in map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(in))
	for id, chain := range in {
		out[id] = chain
	}
	return out
}

func (a *Agent) guestParentProof(ctx context.Context, info ParticipationInfo) (*envelope.HumanTurn, error) {
	m, err := a.dmMembers(info.Conv)
	if err != nil {
		return nil, err
	}
	p, err := guestParentIn(a.store.db, info.Conv, info.Parent, m)
	if err != nil || !p.HumanActive() {
		return nil, errors.New("participation: human parent is no longer active")
	}
	scope, err := a.participationScope(ctx, p)
	if err != nil {
		return nil, err
	}
	events, err := a.store.participationEvents(info.Conv, p.PID)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Hash() == p.Decision {
			return &envelope.HumanTurn{AuthorPID: p.PID, Audience: []envelope.HumanScope{{PID: p.PID, Invite: p.Invite, Decision: p.Decision}}, Proof: []protocol.ParticipationEvent{scope, e}}, nil
		}
	}
	return nil, ErrNoParticipation
}

// An outside host may retain cryptographically verified pending public proof
// before its group context arrives. Resolution still needs that current context.
func (a *Agent) receiveGuestParentProof(ctx context.Context, in envelope.Inner) error {
	if _, err := envelope.GuestAgentEvent(in); err != nil {
		return err
	}
	for _, e := range in.Human.Proof {
		if ok, err := a.boundIn(ctx, e.Author.Person, e.Author.Roster); err != nil {
			return err
		} else if !ok {
			return ErrGroupContextPending
		}
		p, ok, err := a.store.personByID(e.Author.Person)
		if err != nil {
			return err
		}
		if !ok || !p.has(e.Author.Address, e.Author.Fingerprint) {
			return ErrGroupContextPending
		}
		dev, ok := p.roster.Device(e.Author.Fingerprint)
		if !ok {
			return ErrGroupContextPending
		}
		if err = e.Verify(dev.SignKey); err != nil {
			return err
		}
		if e.Host != nil {
			if _, err = a.externalHostProof(ctx, e.Host); err != nil {
				return err
			}
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range in.Human.Proof {
		raw, _ := json.Marshal(e)
		if err = insertParticipationEvent(tx, e, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func guestEventAuthority(q dbq, in envelope.Inner, m dmMembers, to, fp string) error {
	e, err := envelope.GuestAgentEvent(in)
	if err != nil {
		return err
	}
	p, err := guestParentIn(q, in.Conv, e.Parent, m)
	if err != nil {
		return err
	}
	if !p.HumanActive() || in.From != p.Host.Address || e.Author.Fingerprint != p.Host.Fingerprint || e.Host == nil || e.Host.Person != p.Host.Person || !sameParticipationTopic(e.Topic, p.Topic) {
		return errors.New("participation: derived event parent authority changed")
	}
	if !m.device(to, fp) && (to != e.Host.Address || fp != e.Host.Fingerprint) {
		return errors.New("participation: derived event recipient differs")
	}
	return nil
}

func guestAgentAudience(h *envelope.HumanTurn, p ParticipationInfo) bool {
	if h == nil || p.Parent == "" {
		return false
	}
	for _, s := range h.Audience {
		if s.Decision == p.Parent {
			return true
		}
	}
	return false
}

func guestParentDecisionReader(q dbq, in envelope.Inner, info ParticipationInfo, m dmMembers, address, fp string) bool {
	if info.Parent == "" || in.Sub != envelope.SubEvent {
		return false
	}
	e, err := protocol.ParseParticipationEvent([]byte(in.Body))
	if err != nil || e.Type != protocol.EventAccept && e.Type != protocol.EventDecline && e.Type != protocol.EventScope {
		return false
	}
	p, err := guestParentIn(q, info.Conv, info.Parent, m)
	return err == nil && p.HumanActive() && p.Host.Address == address && p.Host.Fingerprint == fp
}

func (m dmMembers) guestAuthor(e protocol.ParticipationEvent) (personRow, bool) {
	parent, ok := m.guestParents[e.Parent]
	p, pinned := m.hosts[e.Author.Person]
	if !pinned {
		p, pinned = m.persons[e.Author.Person]
	}
	return p.at(e.Author.Address), ok && pinned && sameParticipationTopic(e.Topic, parent.Topic) && e.Host != nil && e.Host.Person == parent.Host.Person && (parent.Until == 0 || e.Until != 0 && e.Until <= parent.Until) && parent.Host.Person == e.Author.Person && parent.Host.Address == e.Author.Address && parent.Host.Fingerprint == e.Author.Fingerprint && p.roster.Human(e.Author.Fingerprint) && p.has(e.Author.Address, e.Author.Fingerprint) && m.chains[e.Author.Person][e.Author.Roster]
}

func (m *dmMembers) loadGuestParents(q dbq, events []protocol.ParticipationEvent) error {
	m.guestParents = nil
	for _, e := range events {
		if e.Parent == "" {
			continue
		}
		if m.guestParents == nil {
			m.guestParents = make(map[string]ParticipationInfo)
		}
		if _, checked := m.guestParents[e.Parent]; checked {
			continue
		}
		p, err := guestParentIn(q, e.Conv, e.Parent, *m)
		if err != nil {
			p = ParticipationInfo{}
		}
		m.guestParents[e.Parent] = p
	}
	return nil
}

func guestAgentCopy(q dbq, conv, pid, sub, body, human string) (bool, error) {
	if sub == envelope.SubHistory && body != "" {
		var h HistoryItem
		if err := json.Unmarshal([]byte(body), &h); err != nil {
			return false, err
		}
		pid, sub, body, human = h.PID, h.Sub, h.Body, humanJSON(h.Human)
		if h.GroupHistory != nil {
			for _, e := range h.GroupHistory.Memberships {
				if e.Parent != "" {
					return true, nil
				}
			}
		}
	}
	if human != "" {
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(human), &h); err != nil {
			return false, err
		}
		for _, e := range h.Proof {
			if e.Parent != "" {
				return true, nil
			}
		}
	}
	if sub == envelope.SubEvent {
		var e protocol.ParticipationEvent
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return false, err
		}
		if e.Parent != "" {
			return true, nil
		}
		pid = e.PID
	}
	return false, nil // ordinary PID rows use topicCopy's existing indexed read
}

func guestAgentRequirement(required string, needed bool) string {
	if !needed {
		return required
	}
	if _, present := splitGuestAgentRequirement(required); present {
		return required
	}
	if required == "" {
		return protocol.CapGuestAgent
	}
	return required + "+" + protocol.CapGuestAgent
}

func splitGuestAgentRequirement(required string) (string, bool) {
	if required == protocol.CapGuestAgent {
		return "", true
	}
	base := strings.TrimSuffix(required, "+"+protocol.CapGuestAgent)
	return base, base != required
}

// A guest can share only a source already admitted to its exact parent: a
// selected excerpt or a live turn carrying that parent's captured consent.
func (a *Agent) guestSelectedItem(q dbq, parent ParticipationInfo, lid string) (HistoryItem, error) {
	rows, err := q.Query(`SELECT body FROM inbox WHERE conv=? AND pid=? AND sub=? AND json_valid(body) AND json_extract(body,'$.lid')=?`, parent.Conv, parent.PID, envelope.SubExcerpt, lid)
	if err != nil {
		return HistoryItem{}, err
	}
	var raw []string
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			break
		}
		raw = append(raw, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return HistoryItem{}, err
	}
	var item HistoryItem
	found := false
	for _, s := range raw {
		h, e := parseGrantedExcerpt(envelope.Inner{Conv: parent.Conv, PID: parent.PID, Body: s}, parent)
		if e != nil {
			return item, e
		}
		if found && (h.FromKey != item.FromKey || contentHash(h.inner(parent.Conv)) != contentHash(item.inner(parent.Conv))) {
			return item, errors.New("selected guest history is ambiguous")
		}
		item, found = h, true
	}
	if !found {
		kr, e := q.Query(`SELECT DISTINCT verified_by FROM inbox WHERE conv=? AND lid=? AND coalesce(sub,'')='' UNION SELECT ? FROM outbox WHERE conv=? AND lid=? AND coalesce(sub,'')=''`, parent.Conv, lid, a.Self().Fingerprint(), parent.Conv, lid)
		if e != nil {
			return item, e
		}
		var keys []string
		for kr.Next() {
			var key string
			if e = kr.Scan(&key); e != nil {
				break
			}
			keys = append(keys, key)
		}
		if e == nil {
			e = kr.Err()
		}
		kr.Close()
		if e != nil {
			return item, e
		}
		if len(keys) != 1 {
			return item, sql.ErrNoRows
		}
		item, e = a.snapshotSourceItem(q, parent.Conv, protocol.GrantRef{LID: lid, Fingerprint: keys[0]})
		if e != nil {
			return item, e
		}
		if item.Human == nil || !guestAgentAudienceForParent(item.Human, parent) {
			return item, errors.New("selected source was not admitted to this guest")
		}
	}
	if parent.Topic == nil || item.Topic != *parent.Topic {
		return item, errors.New("selected guest history is outside its topic")
	}
	deleted, e := retractedRefChecked(q, parent.Conv, item.LID, item.FromKey)
	if e != nil {
		return item, e
	}
	if deleted {
		return item, errors.New("selected guest history was deleted")
	}
	return item, nil
}

func guestAgentAudienceForParent(h *envelope.HumanTurn, p ParticipationInfo) bool {
	for _, s := range h.Audience {
		if s.PID == p.PID && s.Invite == p.Invite && s.Decision == p.Decision {
			return true
		}
	}
	return false
}

func (a *Agent) checkGuestGrants(q dbq, e protocol.ParticipationEvent) error {
	m, err := membersIn(q, e.Conv)
	if err != nil {
		return err
	}
	p, err := guestParentIn(q, e.Conv, e.Parent, m)
	if err != nil {
		return err
	}
	if !p.HumanActive() {
		return errors.New("human parent ended")
	}
	for _, ref := range e.Grant {
		h, err := a.guestSelectedItem(q, p, ref.LID)
		if err != nil {
			return err
		}
		if h.FromKey != ref.Fingerprint {
			return errors.New("selected guest history author differs")
		}
	}
	return nil
}
