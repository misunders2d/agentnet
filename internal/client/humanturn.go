package client

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var errHumanHistoryConsent = errors.New("human: captured consent differs from local proof")

// humanAuthorization authenticates author and reader independently against the
// CAPTURED scopes. It never adds a recipient or turns a guest into a member.
func humanAuthorization(q dbq, conv string, h *envelope.HumanTurn, from, fromFP, to, toFP string) error {
	return humanAuthority(q, conv, h, from, fromFP, to, toFP, false, false, false)
}

// humanTurnAuthorization is humanAuthorization for any captured turn: on an
// addressed request the assistant participation's exact host is also a
// reader, and its output's exact host is its author. The request must name
// that participation's exact host and agent; neither may have ended (unless
// historical). Scope grants no execution: the host's worker decides that.
func humanTurnAuthorization(q dbq, in envelope.Inner, from, fromFP, to, toFP string, historical bool, context ...dmMembers) error {
	if len(context) > 0 && !historical {
		return errors.New("human: historical context cannot authorize live turns")
	}
	topicMembers, topicErr := humanMembers(q, in.Conv, context)
	if topicErr != nil {
		return topicErr
	}
	if err := topicAudienceAuthorization(q, in, topicMembers, to); err != nil {
		return err
	}
	h := in.Human
	if h == nil {
		return errors.New("human: missing captured audience")
	}
	if in.PID == "" || in.PID == h.AuthorPID {
		return humanAuthority(q, in.Conv, h, from, fromFP, to, toFP, historical, false, false, context...)
	}
	m, err := humanMembers(q, in.Conv, context)
	if err != nil {
		return err
	}
	x, err := participationIn(q, in.Conv, in.PID, m, to)
	if err != nil {
		return err
	}
	if x.Role == protocol.RoleHuman || x.Invite == "" {
		return errors.New("human: addressed turn names no assistant participation")
	}
	if err := checkParticipationTopic(q, x.Topic, in); err != nil {
		return err
	}
	live := historical || x.State == PartActive && x.Held == 0
	hostFrom := x.Host.Address == from && x.Host.Fingerprint == fromFP
	hostTo := x.Host.Address == to && x.Host.Fingerprint == toFP
	if in.Target != nil {
		if in.Target.Address != x.Host.Address || in.Target.Fingerprint != x.Host.Fingerprint || in.Target.AgentID != x.AgentID || !live {
			return errors.New("human: request does not name the exact active assistant")
		}
		return humanAuthority(q, in.Conv, h, from, fromFP, to, toFP, historical, false, hostTo, context...)
	}
	if h.AuthorPID != "" || !hostFrom || in.AgentID != x.AgentID || !live {
		return errors.New("human: assistant output is not from its exact active host")
	}
	return humanAuthority(q, in.Conv, h, from, fromFP, to, toFP, historical, true, false, context...)
}
func humanAuthority(q dbq, conv string, h *envelope.HumanTurn, from, fromFP, to, toFP string, historical, hostAuthor, hostReader bool, context ...dmMembers) error {
	if len(context) > 0 && !historical {
		return errors.New("human: historical context cannot authorize live turns")
	}
	if h == nil {
		return errors.New("human: missing captured audience")
	}
	if err := h.Validate(conv); err != nil {
		return err
	}
	m, err := humanMembers(q, conv, context)
	if err != nil {
		return err
	}
	if !humanRoom(m) {
		return errors.New("human: not a two-person DM or a group with verified context")
	}
	sender := h.AuthorPID == "" && (m.device(from, fromFP) || hostAuthor)
	memberReader := m.device(to, toFP)
	reader := memberReader || hostReader
	if historical && !reader {
		return errors.New("human history remains with original members' own linked devices")
	}
	for _, scope := range h.Audience {
		p, err := participationIn(q, conv, scope.PID, m, to)
		if err != nil {
			return err
		}
		if !p.follows() || p.Invite != scope.Invite || p.Decision != scope.Decision {
			if historical && m.group != nil && len(context) == 0 {
				return errHumanHistoryConsent // exact own history may try its verified original witness
			}
			return errors.New("human: captured consent differs from local proof")
		}
		if scope.PID == h.AuthorPID && (p.Role == "") != h.AgentAuthor() {
			return errors.New("human: the author's role differs from its captured scope")
		}
		exactSender := scope.PID == h.AuthorPID && p.Host.Address == from && p.Host.Fingerprint == fromFP
		exactReader := p.Host.Address == to && p.Host.Fingerprint == toFP
		// An original member retains its own inert history independently of
		// an assistant participation hosted on that same device. Captured
		// authors and nonmember readers still need their exact participation.
		if exactSender || exactReader {
			retained := historical && (exactSender || memberReader) && p.Held == 0 && p.State == PartDismissed && p.Conflict == ""
			if !p.Following() && !retained {
				return errors.New("human: author or reader participation ended or is held")
			}
		}
		sender = sender || exactSender
		reader = reader || exactReader
	}
	if !sender || !reader {
		return errors.New("human: author or recipient outside captured authority")
	}
	return nil
}

// An override is supplied only by the inert own-history verifier, after its
// signed original state and exact current own-device gates have passed.
func humanMembers(q dbq, conv string, context []dmMembers) (dmMembers, error) {
	if len(context) != 0 {
		if len(context) != 1 || context[0].historyEvents == nil || context[0].root.ID() != conv {
			return dmMembers{}, errors.New("human: historical context scope differs")
		}
		return context[0], nil
	}
	return membersIn(q, conv)
}

// humanRoom reports whether m's conversation carries a captured audience
// (ROOM_V1 §3): an unchanged two-person DM, or a group whose current
// context is verified here.
func humanRoom(m dmMembers) bool { return externalDM(m.root) || m.group != nil }

// A lifecycle end may reach another exact following host (an accepted human
// guest or room participant). This only shares signed lifecycle evidence;
// no text, files, membership or jobs.
func humanEndReader(q dbq, conv, address, fp string) (bool, error) {
	m, err := membersIn(q, conv)
	if err != nil {
		return false, err
	}
	if !humanRoom(m) {
		return false, nil
	}
	rows, err := q.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv=? ORDER BY pid`, conv)
	if err != nil {
		return false, err
	}
	var pids []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return false, err
		}
		pids = append(pids, pid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, pid := range pids {
		p, err := participationIn(q, conv, pid, m, address)
		if err != nil {
			return false, err
		}
		if p.Following() && p.Host.Address == address && p.Host.Fingerprint == fp {
			return true, nil
		}
	}
	return false, nil
}
func humanEndEvent(in envelope.Inner, info ParticipationInfo) bool {
	if info.Role != protocol.RoleHuman || in.Sub != envelope.SubEvent {
		return false
	}
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	return err == nil && ev.Type == protocol.EventDismiss && ev.PID == info.PID && ev.Conv == info.Conv
}
func (a *Agent) humanPlan(ctx context.Context, conv, authorPID string, topic ...string) (*envelope.HumanTurn, error) {
	m, err := a.dmMembers(conv)
	if err != nil {
		return nil, err
	}
	if !externalDM(m.root) {
		return nil, nil
	}
	infos, err := a.Participations(conv)
	if err != nil {
		return nil, err
	}
	h := &envelope.HumanTurn{AuthorPID: authorPID, Audience: []envelope.HumanScope{}, Proof: []protocol.ParticipationEvent{}}
	slices.SortFunc(infos, func(x, y ParticipationInfo) int { return strings.Compare(x.PID, y.PID) })
	for _, p := range infos {
		if p.Topic != nil && (len(topic) == 0 || *p.Topic != topic[0]) {
			continue
		}
		if p.Role != protocol.RoleHuman {
			continue
		}
		if p.State == PartActive && p.Held != 0 {
			return nil, errors.New("human: audience evidence is pending")
		}
		if !p.HumanActive() {
			continue
		}
		if _, err := a.refreshPerson(ctx, p.Host.Person, false); err != nil {
			return nil, err
		}
		p, err = a.Participation(p.PID)
		if err != nil {
			return nil, err
		}
		if !p.HumanActive() {
			return nil, errors.New("human: audience changed during send; review again")
		}
		events, err := a.store.participationEvents(conv, p.PID)
		if err != nil {
			return nil, err
		}
		var decision *protocol.ParticipationEvent
		for i := range events {
			if events[i].Hash() == p.Decision {
				decision = &events[i]
			}
		}
		if decision == nil {
			return nil, errors.New("human: acceptance proof missing")
		}
		scope, err := a.participationScope(ctx, p) // the public projection: never the invitation's note or grant
		if err != nil {
			return nil, errors.New("human: audience evidence is pending")
		}
		h.Audience = append(h.Audience, envelope.HumanScope{PID: p.PID, Invite: p.Invite, Decision: p.Decision})
		h.Proof = append(h.Proof, scope, *decision)
	}
	if len(h.Audience) == 0 {
		if authorPID != "" {
			return nil, errors.New("human: participation is not active")
		}
		return nil, nil
	}
	if err := h.Validate(conv); err != nil {
		return nil, err
	}
	return h, nil
}

// Verify carried signed metadata before atomically admitting it. Root/person
// proofs are the existing pinned chains; event labels never grant authority.
func (a *Agent) verifyHumanProof(ctx context.Context, root protocol.ConvRoot, h *envelope.HumanTurn, context ...dmMembers) error {
	if err := h.Validate(root.ID()); err != nil {
		return err
	}
	for _, member := range root.Members {
		if root.Kind == protocol.ConvKindGroup {
			break // a group's members are its current verified context's (dmMembers), not its root's
		}
		if ok, err := a.boundIn(ctx, member.Person, member.Roster); err != nil {
			return err
		} else if !ok {
			return errors.New("human: original member chain missing")
		}
	}
	m, err := humanMembers(a.store.db, root.ID(), context)
	if err != nil {
		return err
	}
	if err := m.loadHosts(a.store.db, h.Proof); err != nil {
		return err
	}
	invites := map[string]protocol.ParticipationEvent{}
	for _, e := range h.Proof {
		if e.Type != protocol.EventScope {
			continue
		}
		p, ok := m.author(e.Author)
		if !ok && m.roomEvents[e.Hash()] {
			p, ok = m.roomAuthor(e.Author)
		}
		if !ok {
			return errors.New("human: scope not authored by original member")
		}
		dev, ok := p.roster.Device(e.Author.Fingerprint)
		if !ok || dev.Address != e.Author.Address {
			return errors.New("human: scope author key missing")
		}
		if err := e.Verify(dev.SignKey); err != nil {
			return err
		}
		if _, err := a.externalHostProof(ctx, e.Host); err != nil {
			return err
		}
		invites[e.PID] = e
	}
	for _, e := range h.Proof {
		if e.Type != protocol.EventAccept {
			continue
		}
		inv, ok := invites[e.PID]
		if !ok || e.Prev != inv.Prev || e.Author.Person != inv.Host.Person || e.Author.Address != inv.Host.Address || e.Author.Fingerprint != inv.Host.Fingerprint {
			return errors.New("human: acceptance differs from exact invited host")
		}
		p, ok, err := a.store.personByID(e.Author.Person)
		if err != nil {
			return err
		}
		if !ok || !a.store.inChain(e.Author.Person, e.Author.Roster) || !p.has(e.Author.Address, e.Author.Fingerprint) {
			return errors.New("human: acceptance author chain missing")
		}
		dev, ok := p.roster.Device(e.Author.Fingerprint)
		if !ok {
			return errors.New("human: acceptance key missing")
		}
		if err := e.Verify(dev.SignKey); err != nil {
			return err
		}
	}
	return nil
}
func insertHumanProof(tx *sql.Tx, h *envelope.HumanTurn) error {
	for _, e := range h.Proof {
		raw, _ := json.Marshal(e)
		if err := insertParticipationEvent(tx, e, raw); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) admitHumanTurn(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sp personRow, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	if _, _, found, err := a.store.conversation(in.Conv); err != nil {
		return err
	} else if !found {
		return hold(reasonProof, "human turn waits for its original invitation root")
	}
	if err := a.verifyRoot(ctx, root, sp); err != nil {
		return hold(reasonProof, err.Error())
	}
	if err := a.verifyHumanProof(ctx, root, in.Human); err != nil {
		return hold(reasonProof, err.Error())
	}
	if in.ReplyTo != "" { // a parent held here must be named by its logical id
		parent, known, err := humanReplyParent(a.store.db, in.Conv, in.ReplyTo)
		if err != nil {
			return hold(reasonInvalid, err.Error())
		}
		if known && parent != in.ReplyTo {
			return hold(reasonInvalid, "human reply must name its exact logical parent")
		}
	}
	if in.PID != "" && in.PID != in.Human.AuthorPID && in.Target == nil && in.ReplyTo != "" { // an output names one exact request of its assistant
		var kind, pid string
		err := a.store.db.QueryRow(`SELECT kind, coalesce(pid,'') FROM inbox WHERE (id=? OR lid=?) AND conv=? UNION ALL SELECT kind, coalesce(pid,'') FROM outbox WHERE (id=? OR lid=?) AND conv=? LIMIT 1`,
			in.ReplyTo, in.ReplyTo, in.Conv, in.ReplyTo, in.ReplyTo, in.Conv).Scan(&kind, &pid)
		if err == nil && (pid != in.PID || kind != envelope.KindQuestion && kind != envelope.KindTask) {
			return hold(reasonInvalid, "assistant output does not reply to its exact request")
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if root.Kind == protocol.ConvKindGroup {
		if reason, err := a.checkConversationAgent(in, sender, false); err != nil {
			return hold(reason, err.Error())
		}
		m, err := a.dmMembers(in.Conv)
		if err != nil {
			return hold(reasonProof, err.Error())
		}
		if in.Target != nil && !in.Human.AgentAuthor() && !m.requestEpoch(in.From, sender.Fingerprint(), in.Target) {
			return hold(reasonInvalid, "group request admission changed")
		}
		if in.Target == nil {
			p, err := a.Participation(in.PID)
			if err != nil {
				return hold(reasonProof, err.Error())
			}
			if reason, err := externalOutputRequest(a.store.db, in, p, m, a.Address, a.Self().Fingerprint()); err != nil {
				return hold(reason, err.Error())
			}
		}
	}
	state := ""
	if in.Target != nil && !in.Replica && in.Target.Address == a.Address && in.Target.Fingerprint == a.Self().Fingerprint() {
		state = stateAgentWaiting // for this device's agent: the worker decides whether it may run (agentjob.go)
	}
	also := func(tx *sql.Tx) error {
		if err := insertHumanProof(tx, in.Human); err != nil {
			return err
		}
		if err := humanTurnAuthorization(tx, in, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint(), false); err != nil {
			return err
		}
		if root.Kind == protocol.ConvKindGroup {
			m, err := membersIn(tx, in.Conv)
			if err != nil {
				return err
			}
			if in.Target != nil && !in.Human.AgentAuthor() && !m.requestEpoch(in.From, sender.Fingerprint(), in.Target) {
				return errors.New("group request admission changed before storage")
			}
			if in.Target == nil {
				p, err := participationIn(tx, in.Conv, in.PID, m, a.Address)
				if err != nil {
					return err
				}
				if _, err = externalOutputRequest(tx, in, p, m, a.Address, a.Self().Fingerprint()); err != nil {
					return err
				}
			}
			// Preserve this member's receive epoch for selected replies and own history.
			// An outside following host keeps no member admission.
			if m.device(a.Address, a.Self().Fingerprint()) {
				admission, err := groupMemberAdmission(tx, *m.group, a.Address, a.Self().Fingerprint())
				if err != nil {
					return err
				}
				_, err = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, admission.Hash(), in.ID)
				return err
			}
		}
		return nil
	}
	result, err := a.store.addConvInbox(in, sender.Fingerprint(), state, fromQuarantine, also)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	if result == admitConflict {
		return hold(reasonDuplicate, "human logical copy differs")
	}
	if result == admitted {
		a.convWork.due(convRetry)
		if len(in.Attachments) > 0 {
			a.convWork.due(convFetch)
		}
		a.kickNow()
		a.wakeWorker() // an exactly correlated selected receiver may own this output
		if statusDue(state, in.Kind) != 0 {
			a.wakeStatus() // its requester hears it waits here (stored with it)
		}
	}
	return nil
}
func (a *Agent) mayDeliverHuman(env envelope.Envelope, in envelope.Inner, state, capturedFP string) (bool, bool, error) {
	if state != stateQueued {
		return true, false, nil
	}
	err := humanTurnAuthorization(a.store.db, in, a.Address, a.Self().Fingerprint(), env.To, capturedFP, false)
	if err != nil {
		if e := a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: "+err.Error(), ""); e != nil {
			return true, false, e
		}
		a.releaseSpool(env)
		return true, false, nil
	}
	return true, true, nil
}

// Human excerpts are deferred until consent. Push/retry recovery reuses the
// existing idempotent excerpt IDs/queue rather than generating another grant.
func (a *Agent) recoverHumanExcerpts(ctx context.Context) {
	rows, err := a.store.db.Query(`SELECT event FROM participation_events WHERE type=? AND author=? AND json_extract(event,'$.role')=?`, protocol.EventInvite, a.Self().Fingerprint(), protocol.RoleHuman)
	if err != nil {
		return
	}
	var invites []protocol.ParticipationEvent
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			if ev, err := protocol.ParseParticipationEvent([]byte(raw)); err == nil {
				invites = append(invites, ev)
			}
		}
	}
	rows.Close()
	for _, inv := range invites {
		p, err := a.Participation(inv.PID)
		if err == nil && p.HumanActive() {
			if err := a.sendGrantedExcerpts(ctx, inv); err != nil {
				a.Logf("human context delivery held: %v", err)
			}
		}
	}
}

// humanDisclosureEvent reports whether in carries a participation's public
// record (scope, acceptance or end): the signed evidence an original shares
// with exact accepted guests (and a human end with outside assistant hosts).
// An invitation or decline is never shared.
func humanDisclosureEvent(in envelope.Inner, info ParticipationInfo) bool {
	if in.Sub != envelope.SubEvent {
		return false
	}
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	return err == nil && ev.PID == info.PID && ev.Conv == info.Conv && (ev.Type == protocol.EventScope || ev.Type == protocol.EventAccept || ev.Type == protocol.EventDismiss)
}

// disclosedCounted checks a shared public record against its participation as
// resolved here: a scope, acceptance or end it counts. No other authority.
func disclosedCounted(in envelope.Inner, info ParticipationInfo) error {
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	if err != nil {
		return err
	}
	if ev.Type != protocol.EventScope && ev.Type != protocol.EventAccept && ev.Type != protocol.EventDismiss || !slices.Contains([]string{info.Scope, info.Decision, info.Dismissal}, ev.Hash()) || ev.Hash() == "" {
		return errors.New("shared participation record is not counted for its participation")
	}
	return nil
}

// assistantHostReader reports whether address/fp is the exact outside host of
// an active assistant participation of conv: it may learn a human end, so a
// departed guest's queued request never runs there.
func assistantHostReader(q dbq, conv, address, fp string) (bool, error) {
	m, err := membersIn(q, conv)
	if err != nil || !externalDM(m.root) {
		return false, err
	}
	rows, err := q.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv=?`, conv)
	if err != nil {
		return false, err
	}
	var pids []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return false, err
		}
		pids = append(pids, pid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, pid := range pids {
		p, err := participationIn(q, conv, pid, m, address)
		if err != nil {
			return false, err
		}
		if p.Claimable() && p.External && p.Host.Address == address && p.Host.Fingerprint == fp {
			return true, nil
		}
	}
	return false, nil
}

// humanEndRecipient: who may learn a guest's end, an exact accepted guest or
// an active assistant's exact outside host.
func humanEndRecipient(q dbq, conv, address, fp string) (bool, error) {
	if ok, err := humanEndReader(q, conv, address, fp); err != nil || ok {
		return ok, err
	}
	return assistantHostReader(q, conv, address, fp)
}

// disclosedHumanEvent checks, at an exact accepted guest, another human
// participation's lifecycle event shared by a current original member device:
// that device's own invitation, or another author's event it forwards. The
// author's pinned key verifies it; ok is false for anything else, which keeps
// its existing path.
func (a *Agent) disclosedHumanEvent(ctx context.Context, in envelope.Inner, sender identity.Public) (protocol.ParticipationEvent, bool, string, error) {
	ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
	if err != nil || in.Kind != envelope.KindMessage || ev.Conv != in.Conv || ev.PID != in.PID {
		return ev, false, "", nil
	}
	own := ev.Author.Address == sender.Address && ev.Author.Fingerprint == sender.Fingerprint()
	self := ev.Author.Address == a.Address && ev.Author.Fingerprint == a.Self().Fingerprint()
	switch {
	case ev.Type == protocol.EventScope && ev.Host != nil: // a human or assistant participation's public projection
		if ev.Host.Address == a.Address && ev.Host.Fingerprint == a.Self().Fingerprint() {
			return ev, false, "", nil // this guest's own participation: its existing path
		}
	case (ev.Type == protocol.EventAccept || ev.Type == protocol.EventDismiss) && !self: // own: a member-hosted assistant's, by its host
	default:
		return ev, false, "", nil
	}
	if _, _, found, err := a.store.conversation(in.Conv); err != nil || !found {
		return ev, false, "", err // only an accepted guest, who holds the root, is shared with
	}
	m, err := a.dmMembers(in.Conv)
	if err != nil {
		return ev, false, reasonProof, err
	}
	if !humanRoom(m) || !m.device(sender.Address, sender.Fingerprint()) {
		return ev, false, "", nil
	}
	host, role := ev.Host, ev.Role
	if host == nil { // a decision or end needs its held scope
		events, err := a.store.participationEvents(in.Conv, in.PID)
		if err != nil {
			return ev, false, "", err
		}
		for _, e := range events {
			if (e.Type == protocol.EventScope || e.Type == protocol.EventInvite) && e.Host != nil && (ev.Type != protocol.EventAccept || e.Hash() == ev.Prev || e.Type == protocol.EventScope && e.Prev == ev.Prev) {
				host, role = e.Host, e.Role
			}
		}
		if host == nil {
			return ev, false, reasonProof, errors.New("participation: shared lifecycle waits for its scope")
		}
	}
	if own && ev.Type != protocol.EventScope && (role == protocol.RoleHuman || ev.Type == protocol.EventAccept && (ev.Author.Address != host.Address || ev.Author.Fingerprint != host.Fingerprint)) {
		return ev, false, "", nil // a member's own human end, or a sender's accept for another host: its existing path
	}
	if host.Address == a.Address && host.Fingerprint == a.Self().Fingerprint() {
		return ev, false, "", nil // this guest's own participation: its existing path
	}
	reader := humanEndReader
	if ev.Type == protocol.EventDismiss && role == protocol.RoleHuman {
		reader = humanEndRecipient // a guest's end also reaches outside assistant hosts
	}
	if ok, err := reader(a.store.db, in.Conv, a.Address, a.Self().Fingerprint()); err != nil {
		return ev, false, "", err
	} else if !ok {
		return ev, false, reasonInvalid, errors.New("human: lifecycle is shared only with an exact accepted guest")
	}
	var key ed25519.PublicKey
	if p, member := m.author(ev.Author); member && ev.Type != protocol.EventAccept {
		dev, ok := p.roster.Device(ev.Author.Fingerprint)
		if !ok || dev.Address != ev.Author.Address {
			return ev, false, reasonInvalid, errors.New("human: shared event author key missing")
		}
		key = dev.SignKey
	} else if ev.Type != protocol.EventScope && ev.Author.Person == host.Person && ev.Author.Address == host.Address && ev.Author.Fingerprint == host.Fingerprint {
		p, ok, err := a.store.personByID(host.Person)
		if err != nil {
			return ev, false, "", err
		}
		if !ok || !a.store.inChain(ev.Author.Person, ev.Author.Roster) || !p.has(ev.Author.Address, ev.Author.Fingerprint) {
			return ev, false, reasonProof, errors.New("human: shared event host chain missing")
		}
		dev, ok := p.roster.Device(ev.Author.Fingerprint)
		if !ok {
			return ev, false, reasonProof, errors.New("human: shared event host key missing")
		}
		key = dev.SignKey
	} else {
		return ev, false, reasonInvalid, errors.New("human: shared event is not by an original member or its exact invited host")
	}
	if err := ev.Verify(key); err != nil {
		return ev, false, reasonInvalid, err
	}
	return ev, true, "", nil
}

// discloseHumanAudience lets exact accepted guests of a DM where this device
// is an original member know each other without anyone speaking first: each
// active guest's signed invitation and acceptance go to every other active
// guest, and a later end goes where its acceptance went. Copies are recorded
// in the outbox once per recipient key, so retries and restarts repeat none.
func (a *Agent) discloseHumanAudience(ctx context.Context) {
	rows, err := a.store.db.Query(`SELECT DISTINCT conv FROM participation_events WHERE type=? AND json_extract(event,'$.role')=?`, protocol.EventInvite, protocol.RoleHuman)
	if err != nil {
		return
	}
	var convs []string
	for rows.Next() {
		var conv string
		if rows.Scan(&conv) == nil {
			convs = append(convs, conv)
		}
	}
	rows.Close()
	for _, conv := range convs {
		if err := a.discloseHumanConv(ctx, conv); err != nil {
			a.Logf("human audience disclosure held: %v", err)
		}
	}
}

func (a *Agent) discloseHumanConv(ctx context.Context, conv string) error {
	m, err := a.dmMembers(conv)
	if err != nil {
		return err
	}
	if !humanRoom(m) || !m.device(a.Address, a.Self().Fingerprint()) {
		return nil
	}
	_, raw, _, err := a.store.conversation(conv)
	if err != nil {
		return err
	}
	infos, err := a.Participations(conv)
	if err != nil {
		return err
	}
	var guests, hosts []ParticipationInfo
	for _, p := range infos {
		if p.HumanActive() {
			guests = append(guests, p)
		}
		if p.Claimable() && p.External && p.Host.Address != a.Address {
			hosts = append(hosts, p) // outside assistant hosts learn human ends (claim-time fence)
		}
	}
	for _, x := range infos {
		active := x.State == PartActive && x.Held == 0
		if x.Decision == "" || !active && x.State != PartDismissed {
			continue
		}
		events, err := a.store.participationEvents(conv, x.PID)
		if err != nil {
			return err
		}
		lifecycle := map[string]protocol.ParticipationEvent{}
		for _, e := range events {
			lifecycle[e.Hash()] = e
		}
		accept, aok := lifecycle[x.Decision]
		if !aok || accept.Type != protocol.EventAccept {
			continue
		}
		var share []protocol.ParticipationEvent
		if active {
			scope, err := a.participationScope(ctx, x) // its public projection: never the invitation
			if err != nil {
				continue // shared once its inviter's scope is held here
			}
			share = []protocol.ParticipationEvent{scope, accept}
		}
		end, ended := lifecycle[x.Dismissal]
		acceptRaw, _ := json.Marshal(accept)
		for _, g := range guests {
			if g.PID == x.PID || g.Host.Address == x.Host.Address && g.Host.Fingerprint == x.Host.Fingerprint || g.Host.Address == a.Address {
				continue
			}
			send := share
			if !active {
				if sent, err := a.humanShared(a.store.db, conv, x.PID, string(acceptRaw), g.Host.Address, g.Host.Fingerprint); err != nil {
					return err
				} else if !ended || !sent {
					continue // an end goes wherever its acceptance may have gone
				}
				send = []protocol.ParticipationEvent{end}
			}
			for _, ev := range send {
				if err := a.shareHumanEvent(ctx, m, raw, x.PID, ev, g.Host, humanEndReader); err != nil {
					return err
				}
			}
		}
		if x.Role == protocol.RoleHuman && !active && ended {
			for _, h := range hosts {
				if err := a.shareHumanEvent(ctx, m, raw, x.PID, end, h.Host, assistantHostReader); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// humanShared reports whether body was captured for address's key, in any
// state: a lost receipt never proves the copy did not arrive.
func (a *Agent) humanShared(q dbq, conv, pid, body, address, fp string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=? AND body=? AND recipient=? AND coalesce(recipient_fp,'')=?`, conv, pid, envelope.SubEvent, body, address, fp).Scan(&n)
	return n > 0, err
}

// humanSubjectCurrent keeps a shared invitation or acceptance to the subject's
// current accepted pair; an end keeps its own ended-subject semantics.
func humanSubjectCurrent(q dbq, ev protocol.ParticipationEvent, pid string) error {
	if ev.Type == protocol.EventDismiss {
		return nil
	}
	m, err := membersIn(q, ev.Conv)
	if err != nil {
		return err
	}
	x, err := participationIn(q, ev.Conv, pid, m, "")
	if err != nil {
		return err
	}
	if x.State != PartActive || x.Held != 0 || ev.Hash() != x.Scope && ev.Hash() != x.Decision {
		return errors.New("human: shared participation is no longer active")
	}
	return nil
}

// shareHumanEvent queues ev's exact signed bytes once for recipient g's
// captured key; enqueue and delivery recheck reader (an accepted guest, or an
// outside assistant host for a human end) and, for a scope or acceptance,
// that its subject is still active.
func (a *Agent) shareHumanEvent(ctx context.Context, m dmMembers, root []byte, pid string, ev protocol.ParticipationEvent, host PersonInfo, reader func(dbq, string, string, string) (bool, error)) error {
	g := struct{ Host PersonInfo }{host}
	body, _ := json.Marshal(ev)
	if sent, err := a.humanShared(a.store.db, ev.Conv, pid, string(body), g.Host.Address, g.Host.Fingerprint); err != nil || sent {
		return err
	}
	if _, err := a.refreshPerson(ctx, g.Host.Person, false); err != nil {
		return err
	}
	p, ok, err := a.store.personByID(g.Host.Person)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("human guest proof unavailable")
	}
	if d, ok := p.device(g.Host.Address); !ok || d.Fingerprint() != g.Host.Fingerprint {
		return errors.New("human guest key changed")
	}
	key, err := a.sendKey(ctx, g.Host.Address)
	if err != nil {
		return err
	}
	if key.Fingerprint() != g.Host.Fingerprint {
		return errors.New("human guest key changed")
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: g.Host.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body),
		Conv: ev.Conv, LID: protocol.NewID(), Root: root, PID: pid, Sub: envelope.SubEvent, Origin: envelope.OriginUI}
	for person, p := range m.persons {
		if m.group == nil || p.has(a.Address, a.Self().Fingerprint()) {
			in.Fan = append(in.Fan, envelope.Fan{Person: person, Roster: p.info.Roster})
		}
	}
	sealed, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return err
	}
	guard := func(tx *sql.Tx, _ string) error {
		if sent, err := a.humanShared(tx, ev.Conv, pid, string(body), g.Host.Address, g.Host.Fingerprint); err != nil || sent {
			if err == nil {
				err = errHumanShared
			}
			return err
		}
		ok, err := reader(tx, ev.Conv, g.Host.Address, g.Host.Fingerprint)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("shared record recipient no longer current")
		}
		return humanSubjectCurrent(tx, ev, pid)
	}
	err = a.store.addConvOutbox([]outCopy{{in: in, env: sealed, state: stateQueued, required: protocol.CapHumanParticipation, recipientFP: key.Fingerprint()}}, in, guard, "")
	if errors.Is(err, errHumanShared) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := a.deliver(ctx, sealed, nil); err != nil && !retryable(err) {
		return err
	}
	return nil
}

var errHumanShared = errors.New("human lifecycle already shared")

// humanReplyParent names a human turn's parent by its logical id, the same
// for every copy; a parent stored without one keeps its own id.
func humanReplyParent(q dbq, conv, ref string) (parent string, known bool, err error) {
	rows, err := q.Query(`SELECT coalesce(conv,''),id,coalesce(lid,'') FROM inbox WHERE (id=? OR lid=?) AND ref_id IS NULL UNION SELECT coalesce(conv,''),id,coalesce(lid,'') FROM outbox WHERE (id=? OR lid=?) AND ref_id IS NULL`, ref, ref, ref, ref)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, id, lid string
		if err := rows.Scan(&c, &id, &lid); err != nil {
			return "", false, err
		}
		if lid == "" {
			lid = id
		}
		if c != conv || parent != "" && parent != lid {
			return "", true, errors.New("human reply stays within its conversation")
		}
		parent, known = lid, true
	}
	return parent, known, rows.Err()
}
