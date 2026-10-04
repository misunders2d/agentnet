package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Agents in DMs on the page, through the client's participation API only.
// An agent joins a DM when a member invites the agent on a member's device
// and that device's person accepts; either person ends it. Nothing here
// runs an agent: what is asked of it is held for the host's person.

// maxClaimedUnix bounds a time a sender wrote (unix seconds) that the page
// shows: 9999-01-01T00:00:00Z, so its year stays within what JSON can
// carry (0–9999) in any time zone. A later claim is not shown.
const maxClaimedUnix = 253370764800

// dmPeople names the two members of a DM for the page: you and them.
type dmPeople struct {
	me, peer PersonView
	role     string
	group    bool
	members  []PersonView
	humans   map[string]client.ParticipationInfo // human participations of the DM, by PID
	known    map[string]PersonView               // other persons pinned here, by id: names for outside hosts, nothing more (marked: outsider)
}

// humanHidden: a guest sees another guest only once that guest's acceptance
// is held here; a lone shared invitation is inert proof, never shown.
func (p dmPeople) humanHidden(info client.ParticipationInfo) bool {
	return p.role != "member" && !info.HostHere && info.Decision == ""
}

// guestActive: this device is an accepted guest here now, who may address
// the conversation's active assistants under their owners' permissions.
func (p dmPeople) guestActive() bool {
	for _, info := range p.humans {
		if info.HostHere && info.HumanActive() {
			return true
		}
	}
	return false
}

// humanName names a human participation's exact host for the timeline.
func (p dmPeople) humanName(info client.ParticipationInfo, subject bool) string {
	if info.Host.Person != "" && info.Host.Person == p.me.Person {
		if subject {
			return "You"
		}
		return "you"
	}
	if info.Host.Label != "" {
		return info.Host.Label
	}
	return info.Host.Address
}

// eventShown says whether a participation record gets its own timeline row.
// For people: hidden while inert for a guest, one row per signed event, and
// an original's copy of another person's event shared onward is not a new row.
func (p dmPeople) eventShown(m client.ConvMessage, seen map[string]bool) bool {
	ev, err := protocol.ParseParticipationEvent([]byte(m.Body))
	if err != nil {
		return true
	}
	if ev.Type == protocol.EventScope && seen["invite/"+ev.PID] {
		return false // the invitation itself is shown
	}
	info, human := p.humans[ev.PID]
	if !human { // one row per exact signed record: each original's shared copy is no new row
		if seen[ev.Hash()] {
			return false
		}
		seen[ev.Hash()] = true
		return true
	}
	if p.humanHidden(info) || m.Dir == "out" && ev.Author.Person != p.me.Person {
		return false
	}
	if seen[ev.Hash()] {
		return false
	}
	seen[ev.Hash()] = true
	return true
}

func (p dmPeople) byPerson(id string) (PersonView, bool) {
	if id != "" {
		for _, m := range p.members {
			if m.Person == id {
				return m, true
			}
		}
	}
	switch id {
	case "":
	case p.me.Person:
		return p.me, true
	case p.peer.Person:
		return p.peer, true
	default:
		if v, ok := p.known[id]; ok {
			return v, true
		}
	}
	return PersonView{}, false
}

func (p dmPeople) byKey(fp string) (PersonView, bool) {
	if p.group && fp != "" {
		for _, m := range p.members {
			if m.Fingerprint == fp {
				return m, true
			}
			for _, d := range m.Devices {
				if d.Fingerprint == fp {
					return m, true
				}
			}
		}
	}
	switch fp {
	case "":
	case p.me.Fingerprint:
		return p.me, true
	case p.peer.Fingerprint:
		return p.peer, true
	}
	return PersonView{}, false
}

// outsider: id is named here only because its person is pinned here
// (p.known): not a member of this DM and not an accepted guest in it now.
// Its label is that person's own claim, so it is marked where it is used.
func (p dmPeople) outsider(id string) bool {
	if _, ok := p.known[id]; !ok || id == p.me.Person || id == p.peer.Person {
		return false
	}
	for _, m := range p.members {
		if m.Person == id {
			return false
		}
	}
	for _, info := range p.humans {
		if info.Host.Person == id && info.HumanActive() {
			return false
		}
	}
	return true
}

// who is a person as the subject of a sentence; whose, as an owner.
func (p dmPeople) who(id string) string {
	if id == p.me.Person && id != "" {
		return "You"
	}
	if v, ok := p.byPerson(id); ok {
		if p.outsider(id) {
			return v.Label + " (not in this DM)"
		}
		return v.Label
	}
	if p.role == "visitor" && len(p.members) < 2 { // an original not held here may be the author
		return "A DM member"
	}
	return "Someone not in this DM"
}

func (p dmPeople) whose(id string) string {
	if id == p.me.Person && id != "" {
		return "your"
	}
	if v, ok := p.byPerson(id); ok {
		if p.outsider(id) {
			return v.Label + " (not in this DM)'s"
		}
		return v.Label + "'s"
	}
	return "an unknown person's"
}

// people is this installation's person and the DM's other one.
func (l *Live) people(peer client.PersonInfo) dmPeople {
	p := dmPeople{peer: personView(peer)}
	if me, ok, err := l.a.Person(); err == nil && ok {
		p.me = personView(me)
	}
	return p
}

func (l *Live) conversationPeople(c client.ConversationInfo) dmPeople {
	p := l.people(c.Peer)
	p.role = c.Role
	p.group = c.Kind == protocol.ConvKindGroup
	if !p.group {
		if infos, err := l.a.Participations(c.ID); err == nil {
			for _, info := range infos {
				if info.Role == protocol.RoleHuman {
					if p.humans == nil {
						p.humans = map[string]client.ParticipationInfo{}
					}
					p.humans[info.PID] = info
				}
			}
			if len(infos) > 0 { // records may name an outside host or author
				if known, err := l.a.KnownPersons(); err == nil {
					p.known = map[string]PersonView{}
					for _, k := range known {
						if k.State == PersonPinned {
							p.known[k.Person] = personView(k)
						}
					}
				}
			}
		}
	}
	for _, m := range c.Members { // group persons, or a visitor's two verified originals
		p.members = append(p.members, personView(m))
	}
	return p
}

// eventText says what a participation record does, for the DM's timeline.
func eventText(body string, p dmPeople) string {
	ev, err := protocol.ParseParticipationEvent([]byte(body))
	if err != nil {
		return "A record about an agent that cannot be read here."
	}
	who := p.who(ev.Author.Person)
	if info, human := p.humans[ev.PID]; human || (ev.Type == protocol.EventInvite || ev.Type == protocol.EventScope) && ev.Role == protocol.RoleHuman {
		return humanEventText(ev, info, human, who, p)
	}
	room := "this DM"
	if p.group {
		room = "this group"
		if _, known := p.byPerson(ev.Author.Person); !known {
			who = ev.Author.Address
		}
	}
	switch ev.Type {
	case protocol.EventInvite, protocol.EventScope: // a scope is the invitation's public part
		host := "an agent"
		if ev.Host != nil {
			host = p.whose(ev.Host.Person) + " agent (on " + ev.Host.Address + ")"
			if p.group && ev.Group != nil && ev.Group.HostRole == "visitor" {
				host = "an outside host's agent (on " + ev.Host.Address + ")"
			}
		}
		return who + " invited " + host + " into " + room + "."
	case protocol.EventAccept:
		if p.group {
			if ev.Author.GroupAdmission == "" {
				return "Outside host " + ev.Author.Address + " accepted participation in this group."
			}
			return who + " accepted: the agent participates in this group."
		}
		return who + " accepted: the agent joins " + room + "."
	case protocol.EventDecline:
		return who + " declined the invitation for the agent."
	case protocol.EventDismiss:
		return who + " dismissed the agent: it gets nothing more from " + room + "."
	}
	return "A record about an agent (" + ev.Type + ")."
}

// eventFields are a participation record's type and its author, plainly
// (DMMessage.EventType, EventBy): the author's person label as known here,
// whoever that is, else the signing device's address; nothing for a record
// that cannot be read.
func eventFields(body string, p dmPeople) (kind, pid, by string) {
	ev, err := protocol.ParseParticipationEvent([]byte(body))
	if err != nil {
		return "", "", ""
	}
	by = ev.Author.Address
	if v, ok := p.byPerson(ev.Author.Person); ok && v.Label != "" {
		by = v.Label
	}
	return ev.Type, ev.PID, by
}

// lastEvent is conv's latest message as a chat list says it when it is a
// participation record (DMSummary.LastEvent): an invitation's public scope
// is its invite.
func lastEvent(m client.ConvMessage, p dmPeople) *LastEvent {
	if m.Sub != envelope.SubEvent {
		return nil
	}
	kind, pid, by := eventFields(m.Body, p)
	if kind == "" {
		return nil
	}
	if kind == protocol.EventScope {
		kind = protocol.EventInvite
	}
	return &LastEvent{Kind: kind, PID: pid, By: by}
}

// humanEventText says what a person's participation record does: people are
// invited, join, decline, leave or are removed; nothing about an agent.
func humanEventText(ev protocol.ParticipationEvent, info client.ParticipationInfo, known bool, who string, p dmPeople) string {
	if known && p.humanHidden(info) {
		return "A participation record not shared here yet."
	}
	guest, Guest := "a person", "A person"
	if known {
		guest, Guest = p.humanName(info, false), p.humanName(info, true)
	} else if ev.Host != nil {
		guest, Guest = ev.Host.Address, ev.Host.Address
	}
	switch ev.Type {
	case protocol.EventInvite, protocol.EventScope:
		return who + " invited " + guest + " into this DM."
	case protocol.EventAccept:
		return Guest + " joined this DM."
	case protocol.EventDecline:
		return Guest + " declined the invitation."
	case protocol.EventDismiss:
		if known && ev.Author.Person == info.Host.Person && ev.Author.Address == info.Host.Address {
			return Guest + " left this DM."
		}
		return who + " removed " + guest + " from this DM."
	}
	return "A participation record (" + ev.Type + ")."
}

// agentViews are conv's participations as the page shows them.
func (l *Live) agentViews(conv string, p dmPeople, msgs []client.ConvMessage) ([]AgentView, error) {
	infos, err := l.a.Participations(conv)
	if err != nil {
		return nil, err
	}
	out := make([]AgentView, 0, len(infos))
	for _, info := range infos {
		if info.Role == protocol.RoleHuman {
			continue
		}
		out = append(out, agentView(info, p, msgs, l.namedAgentReady(info)))
	}
	return out, nil
}

// hasResponder says whether this installation has a responder chosen: its
// agent runs nothing without one.
func (l *Live) hasResponder() bool {
	r, err := l.a.Responder()
	return err == nil && r != nil
}

func agentView(info client.ParticipationInfo, p dmPeople, msgs []client.ConvMessage, responder bool) AgentView {
	v := AgentView{PID: info.PID, AgentID: info.AgentID, State: info.State, Host: personView(info.Host), HostHere: info.HostHere,
		Inviter: personView(info.Inviter), Note: info.Note, Shared: []string{}, TasksFrom: []PersonView{}, Held: info.Held}
	v.External = info.External
	if info.Invited > 0 && info.Invited < maxClaimedUnix { // the inviter's claim: one no page could show is left out
		v.Invited = time.Unix(info.Invited, 0)
	}
	for _, g := range info.Grant {
		found := false
		for _, m := range msgs {
			direct := m.Key == g.Fingerprint && !m.Replica
			claimed := m.ExcerptPID == info.PID && m.Claimed == g.Fingerprint && m.SyncedFrom == info.Inviter.Address && m.History
			if m.LID == g.LID && m.Sub == "" && (direct || claimed) {
				v.Shared, found = append(v.Shared, m.ID), true
				break
			}
		}
		if !found {
			v.Missing++
		}
	}
	for _, fp := range info.TaskKeys {
		if who, ok := p.byKey(fp); ok {
			v.TasksFrom = append(v.TasksFrom, who)
		}
	}
	host := p.whose(info.Host.Person)
	if info.External {
		host = info.Host.Label + "'s"
	}
	switch info.State {
	case client.PartPending:
		v.StateText = "Its invitation is not here yet: nothing counts until it is."
	case client.PartInvited:
		if v.HostHere {
			v.StateText = p.who(info.Inviter.Person) + " invited your agent. Nothing runs unless you accept."
		} else {
			v.StateText = "Invited. " + info.Host.Label + " accepts or declines it on " + info.Host.Address + "."
		}
		v.CanDecide, v.CanDismiss = v.HostHere, true
	case client.PartActive:
		v.StateText = "In this DM. It answers what either of you asks it, on " + info.Host.Address + " with " + host +
			" own setup, and is shown only what was shared and what is asked of it here."
		if v.HostHere {
			v.StateText = "In this DM. It answers what either of you asks it, here with your own setup, and is shown only what was shared and what is asked of it here."
			if !responder {
				v.StateText = "In this DM, but no responder is chosen on this computer: what is asked of it waits until you choose one (agentnet responder)."
				if info.AgentID != "" {
					v.StateText = "In this DM, but the selected agent is not ready on this host. Check its local agent configuration."
				}
			}
		}
		v.CanDismiss, v.CanAsk = true, info.Claimable()
	case client.PartDeclined:
		v.StateText = "Declined by " + info.Host.Label + "."
		if v.HostHere {
			v.StateText = "You declined it."
		}
	case client.PartConflict:
		v.StateText = "Its records conflict (" + info.Conflict + "): nothing runs it."
		v.CanDismiss = true
	case client.PartDismissed:
		v.StateText = "Dismissed: it gets nothing more from this DM."
	}
	if info.Held > 0 {
		v.StateText += " Some of its records do not count here yet."
	}
	if p.role == "visitor" {
		v.CanAsk, v.CanDismiss = p.guestActive() && info.Claimable() && !info.HostHere, false
		if info.State == client.PartActive {
			v.StateText = "Invited agent context for this DM's two members. Only selected snapshots and requests addressed to this agent are supplied."
			if p.guestActive() && !info.HostHere {
				v.StateText = "In this conversation, on " + info.Host.Address + ". Ask it with @mention; its owner's permissions decide whether it runs."
			}
		}
	}
	if p.group {
		v.StateText = strings.ReplaceAll(strings.ReplaceAll(v.StateText, "this DM", "this group"), "either of you", "current group members")
		if p.role == "visitor" && info.State == client.PartActive {
			v.StateText = "Invited agent context for this group. Only selected snapshots and requests addressed to this agent are supplied."
		}
	}
	return v
}

// dmOf finds a DM, its messages and its people.
func (l *Live) dmOf(conv string) (client.ConversationInfo, []client.ConvMessage, dmPeople, error) {
	convs, err := l.a.Conversations()
	if err != nil {
		return client.ConversationInfo{}, nil, dmPeople{}, err
	}
	for _, c := range convs {
		if c.ID == conv {
			msgs, err := l.a.ConversationMessages(conv)
			people := l.conversationPeople(c)
			return c, msgs, people, err
		}
	}
	return client.ConversationInfo{}, nil, dmPeople{}, NotFound("no conversation with that id")
}

func (l *Live) agentResult(info client.ParticipationInfo, err error) (AgentView, error) {
	if err != nil {
		if errors.Is(err, client.ErrNoParticipation) {
			return AgentView{}, NotFound("no such agent in a DM here")
		}
		return AgentView{}, Refuse(sentence(err))
	}
	l.a.NoteChange()
	_, msgs, p, derr := l.dmOf(info.Conv)
	if derr != nil {
		return AgentView{}, derr
	}
	return agentView(info, p, msgs, l.namedAgentReady(info)), nil
}

// InviteAgent implements Participants.
func (l *Live) InviteAgent(d AgentInvite) (AgentView, error) {
	_, msgs, _, err := l.dmOf(d.Conv)
	if err != nil {
		return AgentView{}, err
	}
	var lids []string
	for _, id := range d.Share {
		lid := ""
		for _, m := range msgs {
			if m.ID == id && m.Sub == "" && !m.Replica {
				lid = m.LID
			}
		}
		if lid == "" {
			return AgentView{}, Refuse("A message chosen to share is not an earlier message of this DM.")
		}
		lids = append(lids, lid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if d.AgentID != "" {
		return l.agentResult(l.a.InviteNamedAgent(ctx, d.Conv, strings.TrimSpace(d.Host), d.AgentID, lids, d.TasksFrom, strings.TrimSpace(d.Note)))
	}
	return l.agentResult(l.a.InviteAgent(ctx, d.Conv, strings.TrimSpace(d.Host), lids, d.TasksFrom, strings.TrimSpace(d.Note)))
}

// DecideAgent implements Participants.
func (l *Live) DecideAgent(pid string, accept bool) (AgentView, error) {
	if p, err := l.a.Participation(pid); err == nil && p.Role == protocol.RoleHuman {
		return AgentView{}, Refuse("Use the human participation consent action, not an agent action.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if accept {
		return l.agentResult(l.a.AcceptParticipation(ctx, pid))
	}
	return l.agentResult(l.a.DeclineParticipation(ctx, pid))
}

// DismissAgent implements Participants.
func (l *Live) DismissAgent(pid string) (AgentView, error) {
	if p, err := l.a.Participation(pid); err == nil && p.Role == protocol.RoleHuman {
		return AgentView{}, Refuse("Use the human participation end action, not an agent action.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	return l.agentResult(l.a.DismissParticipation(ctx, pid))
}

// AskAgent implements Participants.
func (l *Live) AskAgent(d AgentAsk) (Sent, error) {
	body := strings.TrimSpace(d.Body)
	if envelope.Blank(body) && len(d.Files) == 0 {
		return Sent{}, Refuse("Write what to ask or attach a file first.")
	}
	kind := d.Kind
	if kind == "" {
		kind = envelope.KindQuestion
	}
	if kind != envelope.KindQuestion && kind != envelope.KindTask {
		return Sent{}, Refuse("Choose a question or task for this agent.")
	}
	if len(d.Files) > envelope.MaxAttachments {
		return Sent{}, Refuse("Too many files for one request.")
	}
	receiver, err := l.selectedReplyReceiver(d.ReplyReceiver)
	if err != nil {
		return Sent{}, err
	}
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return Sent{}, err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	res, err := l.a.AskAgentWithReceiver(ctx, d.PID, kind, body, receiver, files...)
	if err != nil {
		if errors.Is(err, client.ErrNoParticipation) {
			return Sent{}, NotFound("no such agent in a DM here")
		}
		return Sent{}, Refuse(sentence(err))
	}
	l.a.NoteChange()
	return Sent{ID: res.ID, State: res.State, Detail: res.Detail}, nil
}
