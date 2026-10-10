package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// External hosts are separate from member persons: their keys never become
// asker, invite, dismiss, TaskKeys or Fan authority.
func (m *dmMembers) loadHosts(q dbq, events []protocol.ParticipationEvent) error {
	if m.hosts == nil {
		m.hosts = map[string]personRow{}
	} else {
		clear(m.hosts)
	}
	m.roomEvents = map[string]bool{}
	m.roomAuthors = map[string]personRow{}
	if m.group != nil {
		rows, err := q.Query(`SELECT hash FROM room_membership_events WHERE conv=?`, m.group.State.Conv)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h string
			if err = rows.Scan(&h); err != nil {
				rows.Close()
				return err
			}
			m.roomEvents[h] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, ev := range events {
			if !m.roomEvents[ev.Hash()] {
				continue
			}
			p, ok, err := personByIDIn(q, ev.Author.Person)
			if err != nil {
				return err
			}
			if !ok || p.info.State != personSelf && p.info.State != personPinned || !p.has(ev.Author.Address, ev.Author.Fingerprint) {
				continue
			}
			var n int
			if err = q.QueryRow(`SELECT count(*) FROM person_chain WHERE person=? AND hash=?`, ev.Author.Person, ev.Author.Roster).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				m.roomAuthors[ev.Author.Person] = p
			}
		}
	}
	m.shareGrants = map[string][]protocol.GrantRef{}
	for _, ev := range events {
		if ev.Type != protocol.EventShare {
			continue
		}
		for _, ref := range ev.Grant {
			var n int
			if err := q.QueryRow(`SELECT count(*) FROM room_turn_readers WHERE conv=? AND lid=? AND fingerprint=? AND person=? AND admission=?`, ev.Conv, ref.LID, ref.Fingerprint, ev.Author.Person, ev.Author.GroupAdmission).Scan(&n); err != nil {
				return err
			}
			allowed := n > 0
			if !allowed && m.group != nil {
				member, ok := m.group.State.Member(ev.Author.Person)
				if ok && member.Admission.Hash() == ev.Author.GroupAdmission {
					for _, h := range member.Admission.History {
						if h.LID == ref.LID && h.Author == ref.Fingerprint {
							var hash string
							if e := q.QueryRow(`SELECT content_hash FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp)=?`, ev.Conv, ref.LID, ref.Fingerprint).Scan(&hash); e == nil && hash == h.Hash {
								allowed = true
							}
						}
					}
				}
			}
			if allowed {
				m.shareGrants[ev.Hash()] = append(m.shareGrants[ev.Hash()], ref)
			}
		}
	}
	m.groupInvites = map[string]bool{}
	for _, ev := range events {
		if (ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope || ev.Type == protocol.EventShare) && m.group != nil { // a room scope carries its invite's binding
			valid, err := m.verifyInviteEpoch(q, ev)
			if err != nil {
				return err
			}
			m.groupInvites[ev.Hash()] = valid
		}
	}
	for _, ev := range events {
		if ev.Type != protocol.EventInvite && ev.Type != protocol.EventScope && ev.Type != protocol.EventShare || ev.Host == nil {
			continue
		}
		h := ev.Host
		if _, member := m.persons[h.Person]; member {
			continue
		}
		p, ok, err := personByIDIn(q, h.Person)
		if err != nil {
			return err
		}
		if !ok || p.info.State != personPinned && p.info.State != personSelf || !p.has(h.Address, h.Fingerprint) {
			continue
		}
		m.hosts[h.Person] = p
		chain := map[string]bool{}
		rows, err := q.Query(`SELECT hash FROM person_chain WHERE person=?`, h.Person)
		if err != nil {
			return err
		}
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				rows.Close()
				return err
			}
			chain[hash] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		m.chains[h.Person] = chain
	}
	return nil
}

func externalDM(root protocol.ConvRoot) bool {
	return root.Kind == protocol.ConvKindDM && len(root.Members) == 2
}

// Direct and linked-human invitation copies must pin the same outside host
// proof before their signed event can resolve a usable participation.
func (a *Agent) externalHostProof(ctx context.Context, h *protocol.ParticipationHost) (string, error) {
	key, err := a.sendKey(ctx, h.Address)
	if err != nil {
		return "", err
	}
	if key.Fingerprint() != h.Fingerprint {
		return reasonInvalid, errors.New("invite host key does not match its pinned device")
	}
	p, err := a.personOfKey(ctx, key.Address, key)
	if err != nil {
		return "", err
	}
	if p.info.Person != h.Person || !p.has(key.Address, key.Fingerprint()) {
		return reasonInvalid, errors.New("invite host person does not match its current proof")
	}
	return "", nil
}

// externalTurn checks roles without giving the host ordinary DM membership.
func externalTurn(in envelope.Inner, info ParticipationInfo, m dmMembers, sender, fp string, queries ...dbq) error {
	if info.Topic != nil && in.Sub != envelope.SubEvent && in.Sub != envelope.SubExcerpt {
		if len(queries) != 1 {
			return errParticipationTopicPending
		}
		if err := checkParticipationTopic(queries[0], info.Topic, in); err != nil {
			return err
		}
	}
	member := m.device(sender, fp)
	host := sender == info.Host.Address && fp == info.Host.Fingerprint
	if in.PID != info.PID || info.Invite == "" || (!info.External || len(m.persons) != 2) && m.group == nil {
		return errors.New("external participation has incomplete pinned evidence")
	}
	switch in.Sub {
	case envelope.SubEvent:
		ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
		if err != nil {
			return err
		}
		if member && (ev.Author.Address != sender || ev.Author.Fingerprint != fp) {
			// An original member device forwards another author's exact
			// counted public record (scope, acceptance, end) of a human or
			// assistant participation; ingress verified its author.
			if ev.Type == protocol.EventDecline || ev.Type == protocol.EventInvite || !slices.Contains([]string{info.Scope, info.Decision, info.Dismissal}, ev.Hash()) {
				return errors.New("forwarded participation event is not counted for its participation")
			}
			return nil
		}
		switch ev.Type {
		case protocol.EventScope:
			if !member || ev.Hash() != info.Scope {
				return errors.New("scope is not its inviter's exact counted projection")
			}
		case protocol.EventShare:
			if !member || ev.Prev != info.Invite || ev.Host == nil || ev.Host.Address != info.Host.Address || ev.Host.Fingerprint != info.Host.Fingerprint || ev.Host.AgentID != info.AgentID || !slices.Contains(info.Shares, ev.Hash()) {
				return errors.New("share does not name the exact group agent membership")
			}
		case protocol.EventInvite:
			_, author := m.author(ev.Author)
			if !member || !author || ev.Hash() != info.Invite || ev.Author.Address != sender || ev.Author.Fingerprint != fp {
				return errors.New("invite is not this member's exact invitation")
			}
		case protocol.EventAccept, protocol.EventDecline:
			p, ok := m.hosts[ev.Author.Person]
			if !ok {
				p, ok = m.persons[ev.Author.Person]
			}
			if !host || !ok || !m.chains[ev.Author.Person][ev.Author.Roster] || !p.has(ev.Author.Address, ev.Author.Fingerprint) || ev.Author.Person != info.Host.Person || ev.Prev != info.Invite || (m.group != nil && (member && !m.authorEpoch(ev.Author) || !member && ev.Author.GroupAdmission != "")) {
				return errors.New("decision is not from the exact invited host for its invite")
			}
		case protocol.EventDismiss:
			_, author := m.author(ev.Author)
			if (!member || !author) && !((info.Role == protocol.RoleHuman || info.Member && info.External) && host && ev.Hash() == info.Dismissal) || !m.mayRemoveAgent(info, ev.Author) {
				return errors.New("only a current DM member or exact accepted human host ends participation")
			}
			events := []string{info.Invite, info.Decision, info.Dismissal}
			if !slices.Contains(events, ev.Prev) {
				return errors.New("dismissal does not follow a counted participation event")
			}
		}
	case envelope.SubExcerpt:
		if info.Role == protocol.RoleHuman && !info.HumanActive() {
			return errors.New("human selected context waits for exact acceptance")
		}
		if !member || !slices.ContainsFunc(info.Inviters, func(p PersonInfo) bool { return sender == p.Address && fp == p.Fingerprint }) || !in.Replica || in.Kind != envelope.KindMessage || info.Held != 0 || info.State != PartInvited && info.State != PartActive {
			return errors.New("excerpt is not from the inviter for a live granted participation")
		}
		_, err := parseGrantedExcerpt(in, info)
		return err
	case "":
		if !info.Claimable() {
			return errors.New("external participation is not active")
		}
		switch in.Kind {
		case envelope.KindQuestion, envelope.KindTask:
			if in.Human != nil && in.Human.AgentAuthor() {
				if in.Target == nil || in.Target.Address != info.Host.Address || in.Target.Fingerprint != info.Host.Fingerprint || in.Target.AgentID != info.AgentID {
					return errors.New("room ask targets a different agent")
				}
				return nil
			}
			if !member || in.Target == nil || in.Target.Address != info.Host.Address || in.Target.Fingerprint != info.Host.Fingerprint || in.Target.AgentID != info.AgentID || !m.requestEpoch(sender, fp, in.Target) {
				return errors.New("request does not name the exact invited agent")
			}
		case envelope.KindAnswer, envelope.KindResult:
			if !host || in.AgentID != info.AgentID || in.ReplyTo == "" {
				return errors.New("output does not belong to the exact invited agent")
			}
		case envelope.KindMessage:
			// Nonterminal progress is an output under the same authority.
			if !isResponderProgress(in) || !host || in.AgentID != info.AgentID {
				return errors.New("external participation confers no ordinary DM send authority")
			}
		default:
			return errors.New("external participation confers no ordinary DM send authority")
		}
	default:
		return errors.New("external host cannot receive ordinary history or controls")
	}
	return nil
}

// Outputs must name the immutable request, not merely another turn in this
// DM. A new external request uses its executable host copy's ID as the LID
// shared by its audience copies. Older copies need the exact physical request
// here; absence remains retryable proof, never an inferred match by PID.
func externalOutputRequest(q dbq, in envelope.Inner, info ParticipationInfo, m dmMembers, self, selfFP string) (string, error) {
	return externalOutputRequestMode(q, in, info, m, self, selfFP, false)
}

func externalOutputRequestMode(q dbq, in envelope.Inner, info ParticipationInfo, m dmMembers, self, selfFP string, historical bool) (string, error) {
	progress := isResponderProgress(in)
	if in.Sub != "" || in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult && !progress {
		return "", nil
	}
	if info.Decision == "" {
		return reasonProof, errors.New("external output has no host acceptance proof yet")
	}
	var decision string
	if historical && m.historyEvents != nil {
		for _, ev := range m.historyEvents {
			if ev.Conv == in.Conv && ev.PID == info.PID && ev.Hash() == info.Decision {
				decision = ev.Type
			}
		}
	}
	if decision == "" {
		if err := q.QueryRow(`SELECT type FROM participation_events WHERE hash=? AND conv=? AND pid=?`, info.Decision, in.Conv, info.PID).Scan(&decision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return reasonProof, errors.New("external output has no host acceptance proof yet")
			}
			return "", err
		}
	}
	if decision != protocol.EventAccept {
		return reasonInvalid, errors.New("external output participation was not accepted by its host")
	}
	rows, err := q.Query(`SELECT coalesce(conv,''), coalesce(pid,''), coalesce(lid,''), kind, coalesce(sub,''), coalesce(target,''), sender, coalesce(verified_by,claimed_fp,''), coalesce(human,'')
		FROM inbox WHERE id=? OR lid=?
		UNION ALL SELECT coalesce(conv,''), coalesce(pid,''), coalesce(lid,''), kind, coalesce(sub,''), coalesce(target,''), ?, ?, coalesce(human,'')
 FROM outbox WHERE id=? OR lid=?
 UNION ALL SELECT coalesce(json_extract(receiver,'$.remote.request.conv'),''),coalesce(json_extract(receiver,'$.remote.request.pid'),''),coalesce(json_extract(receiver,'$.remote.request.lid'),''),json_extract(receiver,'$.remote.request.kind'),'',coalesce(json_extract(receiver,'$.remote.request.target'),''),json_extract(receiver,'$.remote.request.from'),json_extract(receiver,'$.remote.request.from_key'), '' FROM reply_receivers WHERE conv=? AND request_ref=? AND json_extract(receiver,'$.remote.role')='imported' AND json_extract(receiver,'$.remote.ready')=1`, in.ReplyTo, in.ReplyTo, self, selfFP, in.ReplyTo, in.ReplyTo, in.Conv, in.ReplyTo)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var originalLID, originalKey string
	var agentAuthors []struct{ pid, from, fp string }
	found := false
	for rows.Next() {
		var conv, pid, lid, kind, sub, raw, from, fp, human string
		if err := rows.Scan(&conv, &pid, &lid, &kind, &sub, &raw, &from, &fp, &human); err != nil {
			return "", err
		}
		var target envelope.Target
		var h envelope.HumanTurn
		if human != "" {
			if err := json.Unmarshal([]byte(human), &h); err != nil {
				return "", err
			}
		}
		agent := h.AgentAuthor()
		if agent {
			agentAuthors = append(agentAuthors, struct{ pid, from, fp string }{h.AuthorPID, from, fp})
		}
		if conv != in.Conv || pid != info.PID || sub != "" || kind != envelope.KindQuestion && kind != envelope.KindTask || !progress && replyKind(kind) != in.Kind ||
			json.Unmarshal([]byte(raw), &target) != nil || target.Address != info.Host.Address || target.Fingerprint != info.Host.Fingerprint || target.AgentID != info.AgentID ||
			!agent && (!m.device(from, fp) || !m.requestEpoch(from, fp, &target)) || lid == "" || found && (lid != originalLID || fp != originalKey) {
			return reasonInvalid, errors.New("external output does not match one exact member request and participation host")
		}
		found, originalLID, originalKey = true, lid, fp
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	for _, au := range agentAuthors {
		p, err := participationIn(q, in.Conv, au.pid, m, self)
		if err != nil {
			return reasonProof, err
		}
		authorized := p.Claimable()
		if !authorized && historical {
			authorized, err = retainedAssistant(q, p, m.historyEvents)
			if err != nil {
				return reasonProof, err
			}
		}
		if !authorized || p.Host.Address != au.from || p.Host.Fingerprint != au.fp {
			return reasonInvalid, errors.New("output requester agent no longer has its original authority")
		}
	}
	if !found {
		return reasonProof, errors.New("external output has no matching request proof yet")
	}
	if err := checkParticipationTopic(q, info.Topic, envelope.Inner{Conv: in.Conv, Ref: &envelope.Ref{ID: originalLID, Fingerprint: originalKey}}); err != nil {
		if errors.Is(err, errParticipationTopicPending) {
			return reasonProof, err
		}
		return reasonInvalid, err
	}
	return "", nil
}

// The existing HistoryItem is a forwarder-claimed frozen snapshot. Its
// original PID is preserved inside the body; the carrier PID is the grant.
func parseGrantedExcerpt(in envelope.Inner, info ParticipationInfo) (HistoryItem, error) {
	var h HistoryItem
	if decodeStrict([]byte(in.Body), &h) != nil || h.V != 1 || !protocol.ValidID(h.ID) || !protocol.ValidID(h.LID) || !protocol.ValidFingerprint(h.FromKey) || h.TS <= 0 || h.Sub != "" || h.Ref != nil {
		return h, errors.New("malformed participation excerpt")
	}
	if _, _, err := protocol.SplitAddress(h.From); err != nil {
		return h, err
	}
	if !slices.Contains([]string{envelope.KindMessage, envelope.KindQuestion, envelope.KindTask, envelope.KindAnswer, envelope.KindResult}, h.Kind) || !slices.Contains(info.Grant, protocol.GrantRef{LID: h.LID, Fingerprint: h.FromKey}) {
		return h, errors.New("excerpt does not match an exact signed grant reference")
	}
	if info.Topic != nil && h.Topic != *info.Topic {
		return h, errParticipationTopic
	}
	if err := envelope.CheckQuote(h.inner(in.Conv)); err != nil {
		return h, err
	}
	if len(h.Attachments) > envelope.MaxAttachments {
		return h, errors.New("too many excerpt files")
	}
	for _, f := range h.Attachments {
		if f.Name == "" || f.Size < 0 || f.Size > MaxFileSize || !protocol.ValidHash(f.SHA256) || f.Blob.ID != "" || f.Blob.Size != 0 || f.Blob.SHA256 != "" {
			return h, errors.New("invalid claimed file manifest")
		}
	}
	used := make([]bool, len(h.Attachments))
	for _, f := range in.Attachments {
		match := -1
		for i, original := range h.Attachments {
			if !used[i] && f.Name == original.Name && f.Size == original.Size && f.SHA256 == original.SHA256 {
				match = i
				break
			}
		}
		if match < 0 {
			return h, errors.New("excerpt carries a file outside its claimed selected turn")
		}
		used[match] = true
	}
	return h, nil
}

func (a *Agent) sendExternalParticipation(ctx context.Context, root protocol.ConvRoot, raw []byte, info ParticipationInfo, out ConvOutgoing) (ConvSent, error) {
	binding, err := a.prepareReplyReceiver(out.ReplyReceiver)
	if err != nil {
		return ConvSent{}, err
	}
	if root.Kind != protocol.ConvKindGroup && (!externalDM(root) || out.selfJob) {
		return ConvSent{}, errors.New("external participation is a two-person DM scoped path")
	}
	m, err := a.dmMembers(info.Conv)
	if err != nil {
		return ConvSent{}, err
	}
	// Refresh every affected proof before creating a recipient-encrypted copy.
	ids := []string{info.Host.Person}
	for id := range m.persons {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, err := a.refreshPerson(ctx, id, false); err != nil {
			return ConvSent{}, err
		}
	}
	m, err = a.dmMembers(info.Conv)
	if err != nil {
		return ConvSent{}, err
	}
	info, err = participationIn(a.store.db, info.Conv, info.PID, m, a.Address)
	if err != nil {
		return ConvSent{}, err
	}
	if out.Kind == "" {
		out.Kind = envelope.KindMessage
	}
	if out.Origin == "" {
		out.Origin = envelope.OriginUI
	}
	lid, _ := sendID(ctx)
	if m.group != nil && out.sub == "" && out.human == nil && info.Member {
		if out.Target == nil && out.ReplyTo != "" {
			c, e := roomCauseIn(a.store.db, info.Conv, out.ReplyTo, a.Address, a.Self().Fingerprint())
			if e != nil {
				return ConvSent{}, e
			}
			if c.human != nil {
				h, e := a.roomAudience(info.Conv, "", out.Topic)
				if e != nil {
					return ConvSent{}, e
				}
				if h == nil {
					return ConvSent{}, errParticipationTopic
				}
				captured := map[string]bool{}
				for _, scope := range c.human.Audience {
					captured[scope.PID] = true
				}
				h.Audience = slices.DeleteFunc(h.Audience, func(s envelope.HumanScope) bool { return !captured[s.PID] })
				h.Proof = slices.DeleteFunc(h.Proof, func(e protocol.ParticipationEvent) bool { return !captured[e.PID] })
				out.human = h
			}
		}
		if out.human == nil {
			h, e := a.roomAudience(info.Conv, "", out.Topic)
			if e != nil {
				return ConvSent{}, e
			}
			out.human = h
		}
	}
	if m.group != nil && out.human != nil && out.Target != nil {
		for _, e := range out.human.Proof {
			if e.Role == protocol.RoleHuman {
				return ConvSent{}, errors.New("group human guest execution audience is not enabled")
			}
		}
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, TS: time.Now().Unix(), Kind: out.Kind, Body: out.Body,
		ReplyTo: out.ReplyTo, Followup: out.Followup, SendGroup: out.SendGroup, Quote: out.Quote, Topic: out.Topic, TopicEvent: out.TopicEvent, TopicDone: out.TopicDone, Conv: info.Conv, LID: lid, Root: raw, PID: info.PID, Sub: out.sub, Status: out.status, Origin: out.Origin, Emotion: out.Emotion, Target: out.Target, AgentID: out.AgentID, Human: out.human}
	if in.Human != nil {
		if err := humanTurnAuthorization(a.store.db, in, a.Address, a.Self().Fingerprint(), info.Host.Address, info.Host.Fingerprint, false); err != nil {
			return ConvSent{}, err
		}
	}
	if err := externalTurn(in, info, m, a.Address, a.Self().Fingerprint(), a.store.db); err != nil {
		return ConvSent{}, err
	}
	if _, err := externalOutputRequest(a.store.db, in, info, m, a.Address, a.Self().Fingerprint()); err != nil {
		return ConvSent{}, err
	}
	request := in.Sub == "" && (in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask)
	if request {
		if queuedSend(ctx) {
			in.ID = in.LID
		} else {
			in.LID = in.ID
		}
	}
	if len(out.Files) > envelope.MaxAttachments || len(out.Files) != 0 && out.sub != "" {
		return ConvSent{}, errors.New("files belong only to an addressed participation turn, within the attachment limit")
	}
	if envelope.Blank(out.Body) && out.sub == "" && len(out.Files) == 0 { // text that shows nothing, as SendConv judges it
		return ConvSent{}, errors.New("nothing to send: no text and no files")
	}
	var devices []identity.Public
	// Host copy first for requests: the public request ID is the host's
	// executable copy, so all replies correlate across every audience copy.
	if a.Address != info.Host.Address {
		p, ok, err := a.store.personByID(info.Host.Person)
		if err != nil || !ok {
			return ConvSent{}, errors.New("external host proof missing")
		}
		dev, ok := p.device(info.Host.Address)
		if !ok {
			return ConvSent{}, errors.New("external host device left its roster")
		}
		devices = append(devices, dev)
	}
	members := root.Members
	if m.group != nil {
		members = nil
		for person := range m.persons {
			members = append(members, protocol.ConvMember{Person: person})
		}
		slices.SortFunc(members, func(x, y protocol.ConvMember) int { return strings.Compare(x.Person, y.Person) })
	}
	seen := map[string]bool{}
	for _, d := range devices {
		seen[d.Address] = true
	}
	if in.Human != nil {
		for _, scope := range in.Human.Audience {
			p, e := participationIn(a.store.db, in.Conv, scope.PID, m, a.Address)
			if e != nil {
				return ConvSent{}, e
			}
			if seen[p.Host.Address] || p.Host.Address == a.Address {
				continue
			}
			key, e := a.sendKey(ctx, p.Host.Address)
			if e != nil {
				return ConvSent{}, e
			}
			if key.Fingerprint() != p.Host.Fingerprint {
				return ConvSent{}, errors.New("room audience host key changed")
			}
			devices = append(devices, key)
			seen[key.Address] = true
		}
	}
	if humanEndEvent(in, info) {
		infos, err := a.Participations(info.Conv)
		if err != nil {
			return ConvSent{}, err
		}
		for _, other := range infos {
			if !other.HumanActive() || seen[other.Host.Address] || other.Host.Address == a.Address {
				continue
			}
			if _, err := a.refreshPerson(ctx, other.Host.Person, false); err != nil {
				return ConvSent{}, err
			}
			other, err = a.Participation(other.PID)
			if err != nil {
				return ConvSent{}, err
			}
			if !other.HumanActive() {
				continue
			}
			p, ok, err := a.store.personByID(other.Host.Person)
			if err != nil {
				return ConvSent{}, err
			}
			if !ok {
				return ConvSent{}, errors.New("human end recipient proof unavailable")
			}
			d, ok := p.device(other.Host.Address)
			if !ok || d.Fingerprint() != other.Host.Fingerprint {
				return ConvSent{}, errors.New("human end recipient key changed")
			}
			devices = append(devices, d)
			seen[d.Address] = true
		}
	}
	for _, mem := range members {
		p, ok := m.persons[mem.Person]
		if !ok {
			return ConvSent{}, errors.New("DM member proof missing")
		}
		in.Fan = append(in.Fan, envelope.Fan{Person: mem.Person, Roster: p.info.Roster})
		for _, dev := range p.roster.Devices {
			if dev.Address != a.Address && !seen[dev.Address] {
				seen[dev.Address] = true
				devices = append(devices, dev)
			}
		}
	}
	feats, ferr := a.relayFeatures(ctx)
	var copies []outCopy
	stored := false
	defer func() {
		if !stored && binding != nil && binding.setup != nil {
			a.releaseGroupCopies([]outCopy{*binding.setup})
		}
	}()
	var spooled []envelope.Blob
	release := func() {}
	if len(out.Files) > 0 {
		for _, file := range out.Files {
			if err := a.keepSent(file.Path); err != nil {
				return ConvSent{}, err
			}
		}
		release, err = lockfile.Wait(a.spoolLockPath())
		if err != nil {
			return ConvSent{}, err
		}
		defer func() {
			if !stored {
				a.releaseSpool(envelope.Envelope{Blobs: spooled})
			}
			release()
		}()
	}
	for _, dev := range devices {
		key, err := a.sendKey(ctx, dev.Address)
		if err != nil {
			return ConvSent{}, err
		}
		if key.Fingerprint() != dev.Fingerprint() {
			return ConvSent{}, errors.New("recipient key no longer matches pinned roster")
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ConvSent{}, err
		}
		copyIn := in
		copyIn.ID, copyIn.To = protocol.NewID(), dev.Address
		if copyIn.SendGroup != "" && !a.sendGroupSupported(ctx, key) {
			copyIn.SendGroup = ""
		}
		if request && (dev.Address == info.Host.Address || out.selfJob && len(copies) == 0) {
			copyIn.ID = in.LID
		}
		// All non-target request copies are display-only, including devices
		// of the inviter: none may claim a job for another host.
		if out.Target != nil {
			copyIn.Replica = dev.Address != info.Host.Address
		}
		if m.group != nil {
			copyIn.Fan = nil
			for person, p := range m.persons {
				if p.has(a.Address, a.Self().Fingerprint()) || p.has(dev.Address, dev.Fingerprint()) {
					copyIn.Fan = append(copyIn.Fan, envelope.Fan{Person: person, Roster: p.roster.Hash()})
				}
			}
		}
		for _, file := range out.Files {
			attachment, err := a.spoolNamed(file, recipient)
			if err != nil {
				return ConvSent{}, err
			}
			copyIn.Attachments = append(copyIn.Attachments, attachment)
			spooled = append(spooled, attachment.Blob)
		}
		sealed, err := envelope.Seal(copyIn, a.id.Sign, recipient)
		if err != nil {
			return ConvSent{}, err
		}
		c := outCopy{in: copyIn, env: sealed, state: stateQueued, required: protocol.CapExternalParticipation}
		if info.Role == protocol.RoleHuman || agentRequirement(copyIn) == protocol.CapHumanParticipation { // a scope record: never under apx1 alone
			c.required = protocol.CapHumanParticipation
			c.recipientFP = key.Fingerprint()
		}
		if m.group != nil {
			c.required = protocol.CapGroup
			c.recipientFP = key.Fingerprint()
			if m.device(dev.Address, key.Fingerprint()) {
				adm, e := groupMemberAdmission(a.store.db, *m.group, dev.Address, key.Fingerprint())
				if e != nil {
					return ConvSent{}, e
				}
				c.groupAdmission = adm.Hash()
			}
		}
		if binding != nil {
			c.recipientFP = key.Fingerprint()
		}
		if ferr != nil {
			c.state, c.why = stateConvWaiting, WaitServerUnavailable+ferr.Error()
		} else if ok, why, _ := a.convSupport(ctx, dev.Address, key, feats); !ok {
			c.state, c.why = stateConvWaiting, why
		}
		copies = append(copies, c)
	}
	if len(copies) == 0 {
		return ConvSent{}, errors.New("no participation recipient")
	}
	if m.group != nil && out.sub == envelope.SubEvent {
		ev, e := protocol.ParseParticipationEvent([]byte(out.Body))
		if e != nil {
			return ConvSent{}, e
		}
		if ev.Type == protocol.EventInvite && ev.Group.HostRole == "visitor" {
			releaseContext, e := lockfile.Wait(a.spoolLockPath())
			if e != nil {
				return ConvSent{}, e
			}
			release = releaseContext
			defer func() {
				if !stored {
					a.releaseGroupCopies(copies)
				}
				releaseContext()
			}()
			contextCopies, e := a.groupDeliveryCopies(ctx, *m.group)
			if e != nil {
				return ConvSent{}, e
			}
			// Context carriers use exact original public proof; no room turns.
			copies = append(contextCopies, copies...)
		}
	}

	claim := out.claim
	guard := func(tx *sql.Tx, first string) error {
		members, err := membersIn(tx, info.Conv)
		if err != nil {
			return err
		}
		current, err := participationIn(tx, info.Conv, info.PID, members, a.Address)
		if err != nil {
			return err
		}
		if in.Human != nil {
			for _, copy := range copies {
				if err := humanTurnAuthorization(tx, in, a.Address, a.Self().Fingerprint(), copy.env.To, copy.recipientFP, false); err != nil {
					return err
				}
			}
		}
		if err := externalTurn(in, current, members, a.Address, a.Self().Fingerprint(), tx); err != nil {
			return &heldBack{err.Error()}
		}
		if _, err := externalOutputRequest(tx, in, current, members, a.Address, a.Self().Fingerprint()); err != nil {
			return &heldBack{err.Error()}
		}
		if humanEndEvent(in, current) {
			for _, copy := range copies {
				if members.device(copy.env.To, copy.recipientFP) || copy.env.To == current.Host.Address && copy.recipientFP == current.Host.Fingerprint {
					continue
				}
				ok, err := humanEndReader(tx, in.Conv, copy.env.To, copy.recipientFP)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("human end captured recipient no longer accepted")
				}
			}
		}
		if members.group != nil {
			for _, copy := range copies {
				if copy.in.Sub == envelope.SubGroupProof || copy.in.Sub == envelope.SubGroupContext {
					if copy.in.PID != "" {
						if e := groupVisitorCarrierDestination(tx, *members.group, copy.in.PID, copy.env.To, copy.recipientFP); e != nil {
							return e
						}
					} else if e := groupTurnCheck(tx, *members.group, copy.env.To, copy.recipientFP); e != nil {
						return e
					}
					continue
				}
				p, ok, e := scanPersonIn(tx, "person IN (SELECT person FROM person_devices WHERE address = ?)", copy.env.To)
				if e != nil {
					return e
				}
				if !ok || !p.has(copy.env.To, copy.recipientFP) {
					return ErrGroupContextPending
				}
				if members.device(copy.env.To, copy.recipientFP) {
					adm, e := groupMemberAdmission(tx, *members.group, copy.env.To, copy.recipientFP)
					if e != nil {
						return e
					}
					if adm.Hash() != copy.groupAdmission {
						return ErrGroupContextPending
					}
				} else if in.Human == nil && !humanEndEvent(in, current) && (copy.env.To != current.Host.Address || copy.recipientFP != current.Host.Fingerprint) {
					return errors.New("group: outside PID audience differs")
				}
			}
		}
		if claim != nil {
			return claim(tx, first)
		}
		return nil
	}
	jobKey := ""
	if out.selfJob {
		jobKey = a.Self().Fingerprint()
	}
	if err := a.prepareRemoteCopies(ctx, binding, copies, out.Files); err != nil {
		return ConvSent{}, err
	}
	if err := a.store.addConvOutbox(copies, in, a.queuedClaim(ctx, info.Conv, guard), jobKey, binding); err != nil {
		return ConvSent{}, err
	}
	stored = true
	release()
	if queuedSend(ctx) {
		return a.queuedConv(copies, copies[0].env.ID, in.LID), nil
	}
	if binding != nil && binding.setup != nil {
		if _, e := a.deliver(ctx, binding.setup.env, nil); e != nil && !retryable(e) {
			return ConvSent{}, e
		}
	}
	defer notifyDaemon(a.home)
	sent := ConvSent{ID: copies[0].env.ID, LID: in.LID, State: protocol.StateDelivered}
	for _, c := range copies {
		cp := ConvCopy{ID: c.env.ID, To: c.in.To, State: c.state, Detail: c.why}
		if c.state == stateQueued {
			result, err := a.deliver(ctx, c.env, nil)
			if err == nil {
				cp.State, cp.Detail = result.State, result.Detail
			} else if retryable(err) {
				cp.Detail = err.Error()
			} else {
				cp.State, cp.Detail = stateFailed, err.Error()
			}
		}
		sent.Copies = append(sent.Copies, cp)
		if rank(cp.State) < rank(sent.State) {
			sent.State, sent.Detail = cp.State, cp.Detail
		}
	}
	return sent, nil
}

func (a *Agent) sendGrantedExcerpts(ctx context.Context, invite protocol.ParticipationEvent) error {
	info, err := a.participation(invite.Conv, invite.PID)
	if err != nil {
		return err
	}
	key, err := a.sendKey(ctx, info.Host.Address)
	if err != nil || key.Fingerprint() != info.Host.Fingerprint {
		return errors.New("selected host key is no longer pinned")
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	_, raw, _, err := a.store.conversation(invite.Conv)
	if err != nil {
		return err
	}
	msgs, err := a.ConversationMessages(invite.Conv)
	if err != nil {
		return err
	}
	for _, ref := range invite.Grant {
		// Retrying the original invitation retains its frozen excerpt, not
		// a fresh snapshot of mutable room history.
		var exists int
		if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=? AND lid=?`, invite.Conv, invite.PID, envelope.SubExcerpt, excerptLID(invite.PID, ref)).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		var msg *ConvMessage
		for i := range msgs {
			if msgs[i].LID == ref.LID && msgs[i].Key == ref.Fingerprint && msgs[i].Sub == "" && !msgs[i].Deleted {
				msg = &msgs[i]
				break
			}
		}
		if msg == nil {
			continue
		} // receiver reports missing selected context honestly
		if err := a.checkTopicGrants(a.store.db, invite.Conv, invite.Topic, []protocol.GrantRef{ref}); err != nil {
			return err
		}
		original := envelope.Inner{Topic: msg.Topic, TopicEvent: msg.TopicEvent, ID: msg.ID, From: msg.From, LID: msg.LID, TS: msg.At, Kind: msg.Kind, Body: msg.Controls.Shown(msg.Body), ReplyTo: msg.ReplyTo, Quote: msg.Quote, Origin: msg.Origin, Emotion: msg.Emotion, Target: msg.Target, PID: msg.PID, AgentID: msg.AgentID}
		for _, f := range msg.Attachments {
			original.Attachments = append(original.Attachments, envelope.Attachment{Name: f.Name, Size: f.Size, SHA256: f.SHA256})
		}
		item := itemOf(original, ref.Fingerprint, msg.At*1000)
		if invite.Topic != nil {
			item.Topic = *invite.Topic
		} // this excerpt is explicitly a forwarder-claimed snapshot
		body, _ := json.Marshal(item)
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: key.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Body: string(body), Conv: invite.Conv, LID: ref.LID, Root: raw, Sub: envelope.SubExcerpt, PID: invite.PID, Replica: true}
		// The same logical original may be granted in distinct participations:
		// its carrier LID must not alias the existing senderFP+LID inbox key.
		in.LID = excerptLID(invite.PID, ref)
		release, err := lockfile.Wait(a.spoolLockPath())
		if err != nil {
			return err
		}
		var spoolErr error
		stored := false
		for _, f := range msg.Attachments {
			plain, name, err := a.fileSource(ctx, invite.Conv, ref.LID, f.SHA256)
			if err != nil {
				continue
			} // manifest remains: missing bytes are explicit
			att, err := a.spoolNamed(OutgoingFile{Name: name, Path: plain}, recipient)
			os.Remove(plain)
			if err != nil {
				spoolErr = err
				break
			}
			if att.Name != f.Name || att.Size != f.Size || att.SHA256 != f.SHA256 {
				spoolErr = errors.New("selected file changed from its manifest")
				a.releaseSpool(envelope.Envelope{Blobs: []envelope.Blob{att.Blob}})
				break
			}
			in.Attachments = append(in.Attachments, att)
		}
		if spoolErr == nil {
			var sealed envelope.Envelope
			sealed, spoolErr = envelope.Seal(in, a.id.Sign, recipient)
			if spoolErr == nil {
				guard := func(tx *sql.Tx, _ string) error {
					m, err := membersIn(tx, info.Conv)
					if err != nil {
						return err
					}
					current, err := participationIn(tx, info.Conv, info.PID, m, a.Address)
					if err != nil {
						return err
					}
					return externalTurn(in, current, m, a.Address, a.Self().Fingerprint(), tx)
				}
				copy := outCopy{in: in, env: sealed, state: stateQueued, required: protocol.CapExternalParticipation}
				if invite.Role == protocol.RoleHuman {
					copy.required = protocol.CapHumanParticipation
				}
				if m, e := a.dmMembers(invite.Conv); e != nil {
					spoolErr = e
				} else {
					if m.group != nil {
						copy.required = protocol.CapGroup
						copy.recipientFP = key.Fingerprint()
					}
					spoolErr = a.store.addConvOutbox([]outCopy{copy}, in, guard, "")
				}
				if spoolErr == nil {
					stored = true
					release()
					_, spoolErr = a.deliver(ctx, sealed, nil)
					if retryable(spoolErr) {
						spoolErr = nil
					} // queued encrypted bytes survive offline retry
				}
			}
		}
		if spoolErr != nil && !stored {
			a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
		}
		release()
		if spoolErr != nil {
			return spoolErr
		}
	}
	return nil
}

func excerptLID(pid string, ref protocol.GrantRef) string {
	// Stable retry identity, scoped to the original grant. Existing IDs are
	// 128-bit hex, and this carrier is never an execution request.
	hash := sha256.Sum256([]byte(pid + "\x00" + ref.LID + "\x00" + ref.Fingerprint))
	return hex.EncodeToString(hash[:16])
}

func (a *Agent) admitExternalParticipation(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, me, sp personRow, sender identity.Public, fromQuarantine bool, hold func(string, string) error) (bool, error) {
	if (!externalDM(root) && root.Kind != protocol.ConvKindGroup) || in.PID == "" {
		return false, nil
	}
	_, recipientMember := root.Member(me.info.Person)
	_, senderMember := root.Member(sp.info.Person)
	group := root.Kind == protocol.ConvKindGroup
	if group {
		members, e := a.dmMembers(in.Conv)
		if e != nil {
			return true, hold(reasonProof, e.Error())
		}
		recipientMember = members.device(a.Address, a.Self().Fingerprint())
		senderMember = members.device(sender.Address, sender.Fingerprint())
	}
	var ev *protocol.ParticipationEvent
	disclosed := false // another human participation's lifecycle, shared by an original
	if in.Sub == envelope.SubEvent {
		if !recipientMember {
			parsed, ok, reason, err := a.disclosedHumanEvent(ctx, in, sender)
			if err != nil {
				if reason != "" {
					return true, hold(reason, err.Error())
				}
				return true, err
			}
			if ok {
				ev, disclosed = &parsed, true
			}
		}
		if !disclosed {
			parsed, err := checkParticipationEvent(in, sender.Fingerprint(), sender.SignKey)
			if err != nil && group && senderMember && in.Kind == envelope.KindMessage {
				// Current members may forward an exact counted end. Verify
				// its original author here; externalTurn below still requires
				// the signed record to resolve under that author's removal right.
				forwarded, parseErr := protocol.ParseParticipationEvent([]byte(in.Body))
				if parseErr == nil && forwarded.Type == protocol.EventDismiss && forwarded.Conv == in.Conv && forwarded.PID == in.PID && (forwarded.Author.Address != sender.Address || forwarded.Author.Fingerprint != sender.Fingerprint()) {
					key, proofErr := a.sendKey(ctx, forwarded.Author.Address)
					if proofErr != nil {
						return true, proofErr
					}
					person, proofErr := a.personOfKey(ctx, key.Address, key)
					if proofErr != nil {
						return true, proofErr
					}
					bound, proofErr := a.boundIn(ctx, forwarded.Author.Person, forwarded.Author.Roster)
					if proofErr != nil {
						return true, proofErr
					}
					if !bound || person.info.Person != forwarded.Author.Person || !person.has(key.Address, forwarded.Author.Fingerprint) || key.Fingerprint() != forwarded.Author.Fingerprint {
						return true, hold(reasonInvalid, "group: forwarded end original author proof differs")
					}
					parsed, err = forwarded, forwarded.Verify(key.SignKey)
				}
			}
			if err != nil {
				return true, hold(reasonInvalid, err.Error())
			}
			ev = &parsed
		}
	}
	needed := group || !recipientMember || !senderMember || in.Sub == envelope.SubExcerpt
	if ev != nil && ev.Host != nil {
		_, member := root.Member(ev.Host.Person)
		needed = needed || !member
	}
	if !needed {
		p, err := a.participation(in.Conv, in.PID)
		needed = err == nil && p.External
	}
	if !needed {
		return false, nil
	}
	proofErr := func(err error) (bool, error) {
		if errors.Is(err, errPersonConflict) {
			return true, hold(reasonConflict, err.Error())
		}
		if errors.Is(err, ErrNoPerson) || errors.Is(err, errPersonRecord) || errors.Is(err, errRootInvalid) {
			return true, hold(reasonProof, err.Error())
		}
		return true, err
	}
	if ev != nil && ev.Host != nil {
		if reason, err := a.externalHostProof(ctx, ev.Host); err != nil {
			if reason != "" {
				return true, hold(reason, err.Error())
			}
			return proofErr(err)
		}
		if !recipientMember && !disclosed && (ev.Host.Address != a.Address || ev.Host.Fingerprint != a.Self().Fingerprint() || ev.Host.Person != me.info.Person) {
			return true, hold(reasonInvalid, "invite does not name this outside host")
		}
	}
	if err := a.verifyRoot(ctx, root, sp); err != nil {
		return proofErr(err)
	}
	rootMembers := root.Members
	if group {
		rootMembers = nil
		packet, e := a.GroupContext(in.Conv)
		if e != nil {
			return true, hold(reasonProof, e.Error())
		}
		for _, mem := range packet.State.Members {
			rootMembers = append(rootMembers, mem.ConvMember)
			if _, e = a.refreshPerson(ctx, mem.Person, false); e != nil {
				return true, e
			}
		}
	}
	for _, mem := range rootMembers {
		if ok, err := a.boundIn(ctx, mem.Person, mem.Roster); err != nil {
			return proofErr(err)
		} else if !ok {
			return true, hold(reasonProof, "root member chain is not pinned")
		}
	}
	_, _, found, err := a.store.conversation(in.Conv)
	if err != nil {
		return true, err
	}
	if !found {
		// An outside recipient's root can be introduced only by the signed
		// invitation naming it, never by a request, output or excerpt.
		if ev == nil || ev.Type != protocol.EventInvite || !senderMember {
			return true, hold(reasonProof, "outside host has no verified invitation root")
		}
		peer := root.Members[0].Person
		if recipientMember {
			for _, mem := range root.Members {
				if mem.Person != me.info.Person {
					peer = mem.Person
				}
			}
		}
		if err := a.store.addConversation(root, in.Root, peer); err != nil {
			return true, err
		}
	}
	m, err := a.dmMembers(in.Conv)
	if err != nil {
		return true, err
	}
	events, err := a.store.participationEvents(in.Conv, in.PID)
	if err != nil {
		return true, err
	}
	if ev != nil && !slices.ContainsFunc(events, func(e protocol.ParticipationEvent) bool { return e.Hash() == ev.Hash() }) {
		events = append(events, *ev)
	}
	if len(events) == 0 {
		return true, hold(reasonProof, "outside traffic has no invitation proof yet")
	}
	if err := m.loadHosts(a.store.db, events); err != nil {
		return true, err
	}
	info := resolve(in.Conv, in.PID, events, m)
	if info.Invite == "" {
		return true, hold(reasonProof, "external invitation proof is incomplete or conflicting")
	}
	if !recipientMember && (info.Host.Address != a.Address || info.Host.Fingerprint != a.Self().Fingerprint() || info.Host.Person != me.info.Person) {
		allowed := false
		if humanEndEvent(in, info) {
			allowed, err = humanEndRecipient(a.store.db, in.Conv, a.Address, a.Self().Fingerprint())
		} else if disclosed {
			allowed, err = humanEndReader(a.store.db, in.Conv, a.Address, a.Self().Fingerprint())
			if err != nil {
				return true, err
			}
		}
		if !allowed {
			return true, hold(reasonInvalid, "outside recipient differs from the exact invited host")
		}
	}
	turn := externalTurn
	if disclosed { // another participation's shared public record: counted here, no other authority
		turn = func(in envelope.Inner, info ParticipationInfo, _ dmMembers, _, _ string, _ ...dbq) error {
			return disclosedCounted(in, info)
		}
	}
	if err := turn(in, info, m, env.From, sender.Fingerprint(), a.store.db); err != nil {
		reason := reasonInvalid
		if errors.Is(err, errParticipationTopicPending) || info.State == PartInvited && in.Sub == "" || !group && len(m.persons) != 2 {
			reason = reasonProof
		}
		return true, hold(reason, err.Error())
	}
	if reason, err := externalOutputRequest(a.store.db, in, info, m, a.Address, a.Self().Fingerprint()); err != nil {
		if reason != "" {
			return true, hold(reason, err.Error())
		}
		return true, err
	}
	if in.ReplyTo != "" {
		if known, err := a.store.knownMessage(in.ReplyTo); err != nil {
			return true, err
		} else if known {
			conv, err := a.store.convOf(in.ReplyTo)
			if err != nil {
				return true, err
			}
			if conv != in.Conv {
				return true, hold(reasonInvalid, "output replies outside its DM")
			}
		}
	}
	state := ""
	if in.Sub == "" && !in.Replica && in.Target != nil && in.Target.Address == a.Address && in.Target.Fingerprint == a.Self().Fingerprint() {
		state = stateAgentWaiting
	}
	also := func(tx *sql.Tx) error {
		if ev != nil {
			if err := insertParticipationEvent(tx, *ev, []byte(in.Body)); err != nil {
				return err
			}
		}
		members, err := membersIn(tx, in.Conv)
		if err != nil {
			return err
		}
		current, err := participationIn(tx, in.Conv, in.PID, members, a.Address)
		if err != nil {
			return err
		}
		if (disclosed || humanEndEvent(in, current)) && !members.device(a.Address, a.Self().Fingerprint()) && (current.Host.Address != a.Address || current.Host.Fingerprint != a.Self().Fingerprint()) {
			reader := humanEndReader
			if humanEndEvent(in, current) {
				reader = humanEndRecipient
			}
			ok, err := reader(tx, in.Conv, a.Address, a.Self().Fingerprint())
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("human end recipient no longer accepted")
			}
		}
		if members.group != nil {
			if !members.device(a.Address, a.Self().Fingerprint()) && !(current.External && current.Host.Address == a.Address && current.Host.Fingerprint == a.Self().Fingerprint()) && !disclosed && !humanEndEvent(in, current) {
				return errors.New("group: PID recipient no longer authorized")
			}
			if members.device(a.Address, a.Self().Fingerprint()) {
				admission, e := groupMemberAdmission(tx, *members.group, a.Address, a.Self().Fingerprint())
				if e != nil {
					return e
				}
				if _, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, admission.Hash(), in.ID); e != nil {
					return e
				}
			}
		}
		if err := turn(in, current, members, env.From, sender.Fingerprint(), tx); err != nil {
			return err
		}
		if _, err := externalOutputRequest(tx, in, current, members, a.Address, a.Self().Fingerprint()); err != nil {
			return err
		}
		return nil
	}
	humanEnd := info.Role == protocol.RoleHuman && ev != nil && ev.Type == protocol.EventDismiss
	if humanEnd {
		a.humanMu.Lock()
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), state, fromQuarantine, also)
	if humanEnd {
		a.humanMu.Unlock()
	}
	if errors.Is(err, errTooManyEvents) {
		return true, hold(reasonInvalid, err.Error())
	}
	if err != nil {
		return true, err
	}
	if res == admitConflict {
		return true, hold(reasonDuplicate, "external PID turn differs from its existing logical copy")
	}
	if res == admitted {
		a.convWork.due(convRetry)
		if len(in.Attachments) > 0 {
			a.convWork.due(convFetch)
		}
		a.kickNow()
		a.wakeWorker()
		if statusDue(state, in.Kind) != 0 {
			a.wakeStatus() // its requester hears it waits here (stored with it)
		}
		if ev != nil {
			a.trySelfConsent(ctx, in.PID) // an invite of this person's own agent, hosted here (a group's)
		}
	}
	return true, nil
}

// Only explicitly queued apx1 copies enter this exception; ordinary Fan and
// peer membership checks are unchanged. Revalidate context as well as output.
func (a *Agent) mayDeliverExternal(env envelope.Envelope) (bool, bool, error) {
	var required, state string
	var in envelope.Inner
	var target, humanRaw, capturedFP string
	err := a.store.db.QueryRow(`SELECT coalesce(required_cap,''), state, conv, coalesce(pid,''), kind, body, coalesce(sub,''), coalesce(origin,''), coalesce(target,''), coalesce(agent_id,''), coalesce(reply_to,''),coalesce(human,''),coalesce(recipient_fp,''),coalesce(status,''),coalesce(topic,'') FROM outbox WHERE id=?`, env.ID).
		Scan(&required, &state, &in.Conv, &in.PID, &in.Kind, &in.Body, &in.Sub, &in.Origin, &target, &in.AgentID, &in.ReplyTo, &humanRaw, &capturedFP, &in.Status, &in.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if humanRaw != "" {
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(humanRaw), &h); err != nil {
			return true, false, err
		}
		if required != protocol.CapHumanParticipation {
			return true, false, errors.New("human scope missing its reader capability")
		}
		if target != "" {
			if err := json.Unmarshal([]byte(target), &in.Target); err != nil {
				return true, false, err
			}
		}
		in.Human = &h
		return a.mayDeliverHuman(env, in, state, capturedFP)
	}
	if required != protocol.CapExternalParticipation && required != protocol.CapHumanParticipation {
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	if in.Sub == envelope.SubHistory {
		// The carrier has no execution PID. apx1 is its reader requirement,
		// not outside-host send authority: history still stays within one
		// current human member's linked devices on every retry.
		m, err := a.dmMembers(in.Conv)
		if err != nil {
			return true, false, err
		}
		me, ok, err := a.store.selfPerson(a.Address)
		if err != nil {
			return true, false, err
		}
		_, member := m.root.Member(me.info.Person)
		allowed := externalDM(m.root) && ok && member && m.device(a.Address, a.Self().Fingerprint()) && me.has(env.To, recipientFP(a.store, env.To))
		if !allowed {
			if err := a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: external history audience is not this human member's current linked device", ""); err != nil {
				return true, false, err
			}
			a.releaseSpool(env)
		}
		return true, allowed, nil
	}
	in.Replica = in.Sub == envelope.SubExcerpt
	if target != "" {
		if err := json.Unmarshal([]byte(target), &in.Target); err != nil {
			return true, false, err
		}
	}
	// Attachments remain signed in the sealed envelope. Role/grant proof is
	// repeated here; their full subset validation occurred before queueing.
	m, err := a.dmMembers(in.Conv)
	if err != nil {
		return true, false, err
	}
	info, err := participationIn(a.store.db, in.Conv, in.PID, m, a.Address)
	if err != nil {
		return true, false, err
	}
	_, rootMember := m.root.Member(info.Host.Person)
	// A member-hosted participation's own record to an original member device
	// is ordinary member traffic: a scope's hgp1 only fences its reader.
	memberEvent := rootMember && externalDM(m.root) && in.Sub == envelope.SubEvent && m.device(env.To, recipientFP(a.store, env.To))
	allowed := memberEvent || !rootMember && externalDM(m.root) && (m.device(env.To, recipientFP(a.store, env.To)) || env.To == info.Host.Address && a.personSendable(info.Host.Address, ""))
	if env.To == info.Host.Address {
		p, ok, _ := a.store.personByID(info.Host.Person)
		allowed = allowed && ok && p.has(info.Host.Address, info.Host.Fingerprint)
	}
	disclosure := false
	if humanDisclosureEvent(in, info) && !allowed && capturedFP != "" {
		ok, e := humanEndReader(a.store.db, in.Conv, env.To, capturedFP)
		if e != nil {
			return true, false, e
		}
		ev, _ := protocol.ParseParticipationEvent([]byte(in.Body))
		if !ok && info.Role == protocol.RoleHuman && ev.Type == protocol.EventDismiss { // a human end to an outside assistant host
			if ok, e = assistantHostReader(a.store.db, in.Conv, env.To, capturedFP); e != nil {
				return true, false, e
			}
		} else if ok && env.To != info.Host.Address { // shared with another guest: never a stale acceptance
			ok = humanSubjectCurrent(a.store.db, ev, info.PID) == nil
		}
		allowed, disclosure = ok, ok
	}
	if required == protocol.CapHumanParticipation && capturedFP != "" && recipientFP(a.store, env.To) != capturedFP {
		allowed = false
	}
	why := "external recipient is no longer a pinned participation audience device"
	if allowed && !memberEvent {
		roleErr := error(nil)
		if disclosure {
			roleErr = disclosedCounted(in, info)
		} else {
			roleErr = externalTurn(in, info, m, a.Address, a.Self().Fingerprint(), a.store.db)
		}
		if roleErr != nil {
			allowed, why = false, roleErr.Error()
		}
	}
	if allowed {
		if reason, requestErr := externalOutputRequest(a.store.db, in, info, m, a.Address, a.Self().Fingerprint()); requestErr != nil {
			if reason == "" {
				return true, false, requestErr
			}
			allowed, why = false, requestErr.Error()
		}
	}
	if allowed && envelope.AgentOrigin(in.Origin) {
		held, err := a.holdEndedOutputs(env.ID)
		if err != nil {
			return true, false, err
		}
		allowed = held == 0
	}
	if !allowed {
		if err := a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: "+why, ""); err != nil {
			return true, false, err
		}
		a.releaseSpool(env)
	}
	return true, allowed, nil
}

func recipientFP(s *store, address string) string {
	key, _, ok, err := s.peer(address)
	if err != nil || !ok {
		return ""
	}
	return key.Fingerprint()
}
