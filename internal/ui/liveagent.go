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

// dmPeople names the two members of a DM for the page: you and them.
type dmPeople struct {
	me, peer PersonView
}

func (p dmPeople) byPerson(id string) (PersonView, bool) {
	switch id {
	case "":
	case p.me.Person:
		return p.me, true
	case p.peer.Person:
		return p.peer, true
	}
	return PersonView{}, false
}

func (p dmPeople) byKey(fp string) (PersonView, bool) {
	switch fp {
	case "":
	case p.me.Fingerprint:
		return p.me, true
	case p.peer.Fingerprint:
		return p.peer, true
	}
	return PersonView{}, false
}

// who is a person as the subject of a sentence; whose, as an owner.
func (p dmPeople) who(id string) string {
	if id == p.me.Person && id != "" {
		return "You"
	}
	if v, ok := p.byPerson(id); ok {
		return v.Label
	}
	return "Someone not in this DM"
}

func (p dmPeople) whose(id string) string {
	if id == p.me.Person && id != "" {
		return "your"
	}
	if v, ok := p.byPerson(id); ok {
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

// eventText says what a participation record does, for the DM's timeline.
func eventText(body string, p dmPeople) string {
	ev, err := protocol.ParseParticipationEvent([]byte(body))
	if err != nil {
		return "A record about an agent that cannot be read here."
	}
	who := p.who(ev.Author.Person)
	switch ev.Type {
	case protocol.EventInvite:
		host := "an agent"
		if ev.Host != nil {
			host = p.whose(ev.Host.Person) + " agent (on " + ev.Host.Address + ")"
		}
		return who + " invited " + host + " into this DM."
	case protocol.EventAccept:
		return who + " accepted: the agent joins this DM."
	case protocol.EventDecline:
		return who + " declined the invitation for the agent."
	case protocol.EventDismiss:
		return who + " dismissed the agent: it gets nothing more from this DM."
	}
	return "A record about an agent (" + ev.Type + ")."
}

// agentViews are conv's participations as the page shows them.
func (l *Live) agentViews(conv string, p dmPeople, msgs []client.ConvMessage) ([]AgentView, error) {
	infos, err := l.a.Participations(conv)
	if err != nil {
		return nil, err
	}
	out := make([]AgentView, 0, len(infos))
	for _, info := range infos {
		out = append(out, agentView(info, p, msgs, l.hasResponder()))
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
	v := AgentView{PID: info.PID, State: info.State, Host: personView(info.Host), HostHere: info.HostHere,
		Inviter: personView(info.Inviter), Note: info.Note, Shared: []string{}, TasksFrom: []PersonView{}, Held: info.Held}
	if info.Invited != 0 {
		v.Invited = time.Unix(info.Invited, 0)
	}
	for _, g := range info.Grant {
		found := false
		for _, m := range msgs {
			if m.LID == g.LID && m.Key == g.Fingerprint && m.Sub == "" && !m.Replica {
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
			return c, msgs, l.people(c.Peer), err
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
	return agentView(info, p, msgs, l.hasResponder()), nil
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
	return l.agentResult(l.a.InviteAgent(ctx, d.Conv, strings.TrimSpace(d.Host), lids, d.TasksFrom, strings.TrimSpace(d.Note)))
}

// DecideAgent implements Participants.
func (l *Live) DecideAgent(pid string, accept bool) (AgentView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if accept {
		return l.agentResult(l.a.AcceptParticipation(ctx, pid))
	}
	return l.agentResult(l.a.DeclineParticipation(ctx, pid))
}

// DismissAgent implements Participants.
func (l *Live) DismissAgent(pid string) (AgentView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	return l.agentResult(l.a.DismissParticipation(ctx, pid))
}

// AskAgent implements Participants.
func (l *Live) AskAgent(d AgentAsk) (Sent, error) {
	body := strings.TrimSpace(d.Body)
	if body == "" {
		return Sent{}, Refuse("Write what to ask first.")
	}
	kind := d.Kind
	if kind == "" {
		kind = envelope.KindQuestion
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	res, err := l.a.AskAgent(ctx, d.PID, kind, body)
	if err != nil {
		if errors.Is(err, client.ErrNoParticipation) {
			return Sent{}, NotFound("no such agent in a DM here")
		}
		return Sent{}, Refuse(sentence(err))
	}
	l.a.NoteChange()
	return Sent{ID: res.ID, State: res.State, Detail: res.Detail}, nil
}
