package ui

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Human DMs on the page, through the client's conversation API only. A
// person exists only when the person creates it here; people on the server
// are listed with their own claimed name until one is checked (on starting a
// DM); nothing merges people or conversations by name or address.

func personView(p client.PersonInfo) PersonView {
	return PersonView{Person: p.Person, Label: p.Label, Address: p.Address, Fingerprint: p.Fingerprint, State: p.State}
}

// dmOverview adds this installation's person, the people and the DMs.
func (l *Live) dmOverview(o *Overview) error {
	o.Persons = true
	me, ok, err := l.a.Person()
	if err != nil {
		return err
	}
	if ok {
		v := personView(me)
		v.Published = l.a.PersonPublished()
		o.Person = &v
	}
	known, err := l.a.KnownPersons()
	if err != nil {
		return err
	}
	byAddress := map[string]bool{l.a.Address: true}
	for _, p := range known {
		o.People = append(o.People, personView(p))
		byAddress[p.Address] = true
	}
	// Someone the server lists with a person record, not checked here yet:
	// shown with the name they claim and their device, without an id.
	for _, m := range l.a.MemberView().Members.Members {
		if len(m.Person) == 0 || byAddress[m.Address] {
			continue
		}
		r, err := protocol.ParsePersonRoster(m.Person)
		if err != nil || len(r.Devices) == 0 || r.Devices[0].Address != m.Address {
			continue
		}
		o.People = append(o.People, PersonView{Label: r.Label, Address: m.Address, State: PersonListed})
	}
	convs, err := l.a.Conversations()
	if err != nil {
		return err
	}
	unread, err := l.a.ConvUnread()
	if err != nil {
		return err
	}
	// The agents each person's device runs in DMs here, from the
	// participations as resolved (the host's person and device).
	links := map[string][]AgentLink{}
	for _, c := range convs {
		infos, err := l.a.Participations(c.ID)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if info.Host.Person == "" {
				continue
			}
			ls := links[info.Host.Person]
			i := slices.IndexFunc(ls, func(x AgentLink) bool { return x.Address == info.Host.Address })
			if i < 0 {
				ls, i = append(ls, AgentLink{Address: info.Host.Address}), len(ls)
			}
			ls[i].DMs = append(ls[i].DMs, AgentInDM{Conv: c.ID, PID: info.PID, State: info.State})
			links[info.Host.Person] = ls
		}
	}
	for i := range o.People {
		o.People[i].Agents = links[o.People[i].Person]
	}
	if o.Person != nil {
		o.Person.Agents = links[o.Person.Person]
	}
	for _, c := range convs {
		msgs, err := l.a.ConversationMessages(c.ID)
		if err != nil {
			return err
		}
		s := DMSummary{ID: c.ID, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Count: len(msgs), Unread: len(unread[c.ID]), LastAt: time.Unix(c.Created, 0)}
		for _, m := range msgs {
			switch {
			case m.Dir == "in" && m.State == "conv_held":
				s.Held++
			case m.Dir == "out" && m.State == "waiting":
				s.Waiting++
			}
		}
		if len(msgs) > 0 {
			people := l.people(c.Peer)
			line := func(m client.ConvMessage) string { // a participation record in words, not its body
				if m.Sub == envelope.SubEvent {
					return eventText(m.Body, people)
				}
				return firstLine(m.Body)
			}
			s.Title, s.Last = line(msgs[0]), line(msgs[len(msgs)-1])
			s.LastAt = time.Unix(msgs[len(msgs)-1].At, 0)
		}
		o.DMs = append(o.DMs, s)
	}
	sort.SliceStable(o.DMs, func(i, j int) bool { return o.DMs[i].LastAt.After(o.DMs[j].LastAt) })
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// CreatePerson implements Persons.
func (l *Live) CreatePerson(label string) (PersonView, string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return PersonView{}, "", Refuse("Write the name you want others to see.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	p, err := l.a.CreatePerson(ctx, label)
	note := "Your person is set up: others can start a DM with you."
	if errors.Is(err, client.ErrNotPublished) {
		note = "Your person is set up here, but your server does not hold it yet: it is sent the next time this computer connects."
	} else if err != nil {
		return PersonView{}, "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	v := personView(p)
	v.Published = l.a.PersonPublished()
	return v, note, nil
}

// NewDM implements Persons.
func (l *Live) NewDM(address string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	id, err := l.a.CreateDM(ctx, strings.TrimSpace(address))
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	return id, nil
}

// DM implements Persons.
func (l *Live) DM(id string) (DMThread, error) {
	convs, err := l.a.Conversations()
	if err != nil {
		return DMThread{}, err
	}
	for _, c := range convs {
		if c.ID != id {
			continue
		}
		msgs, err := l.a.ConversationMessages(id)
		if err != nil {
			return DMThread{}, err
		}
		unread, err := l.a.ConvUnread()
		if err != nil {
			return DMThread{}, err
		}
		isUnread := map[string]bool{}
		for _, u := range unread[id] {
			isUnread[u] = true
		}
		t := DMThread{ID: id, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Messages: []DMMessage{}}
		if c.Peer.State == PersonConflict {
			t.Frozen = c.Peer.Address + " published a different person record than the one kept here, so this conversation is frozen: nothing more is sent in it."
		}
		people := l.people(c.Peer)
		for _, m := range msgs {
			dm := DMMessage{ID: m.ID, Dir: m.Dir, From: m.From, Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo,
				Origin: m.Origin, State: m.State, StateText: DMStateText(m.Dir, m.Kind, m.State, c.Peer.Address, m.Detail),
				Detail: m.Detail, At: time.Unix(m.At, 0), Unread: isUnread[m.ID], Replica: m.Replica, PID: m.PID, Attachments: fileViews(m.Attachments)}
			if m.Target != nil {
				dm.To = m.Target.Address
			}
			// A request to this device's agent: its job state is the row's
			// (received), or the local job's (this person asking their own agent).
			switch {
			case m.PID != "" && m.Dir == "in" && m.Target != nil && m.Target.Address == l.a.Address && !m.Replica:
				dm.Actions, dm.JobDetail = AgentActions(m.Kind, m.State), m.JobDetail
			case m.PID != "" && m.Dir == "out" && m.Job != "":
				dm.Actions, dm.JobDetail = AgentActions(m.Kind, m.Job), m.JobDetail
				if text := DMStateText("in", m.Kind, m.Job, c.Peer.Address, ""); text != "" {
					dm.StateText = "Your agent: " + strings.ToLower(text[:1]) + text[1:]
				}
			}
			if m.Sub == envelope.SubEvent {
				dm.Event, dm.Body, dm.StateText = eventText(m.Body, people), "", ""
			}
			t.Messages = append(t.Messages, dm)
		}
		if t.Agents, err = l.agentViews(id, people, msgs); err != nil {
			return DMThread{}, err
		}
		return t, nil
	}
	return DMThread{}, NotFound("no conversation with that id")
}

// SendDM implements Persons: a message in the conversation, never in the
// older format and never a question or task.
func (l *Live) SendDM(d DMDraft) (Sent, error) {
	body := strings.TrimSpace(d.Body)
	if body == "" && len(d.Files) == 0 {
		return Sent{}, Refuse("Write a message or add a file first.")
	}
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return Sent{}, err
	}
	defer cleanup() // SendConv encrypted them into the spool, or refused: either way the staged copies go
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	res, err := l.a.SendConv(ctx, d.Conv, client.ConvOutgoing{Kind: envelope.KindMessage, Body: body, ReplyTo: d.ReplyTo, Origin: envelope.OriginUI, Files: files})
	if err != nil {
		return Sent{}, Refuse(sentence(err))
	}
	if res.State == protocol.StateCustody { // as a page send does: wait once, in the background, for the receipt
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), receiptWait+l.timeout)
			defer cancel()
			l.a.Status(ctx, res.ID, receiptWait)
		}()
	}
	return Sent{ID: res.ID, State: res.State, Detail: res.Detail}, nil
}

// refreshDM is Refresh for a DM (ok false when id is not one): once per
// open, ask the Hub about messages it still holds for the person's device,
// and about that device.
func (l *Live) refreshDM(id string) (Presence, bool, error) {
	convs, err := l.a.Conversations()
	if err != nil {
		return Presence{}, false, err
	}
	for _, c := range convs {
		if c.ID != id {
			continue
		}
		msgs, err := l.a.ConversationMessages(id)
		if err != nil {
			return Presence{}, true, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		const maxReceipts = 20
		for i, asked := len(msgs)-1, 0; i >= 0 && asked < maxReceipts; i-- {
			if m := msgs[i]; m.Dir == "out" && m.State == protocol.StateCustody {
				asked++
				l.a.Status(ctx, m.ID, 0) // stores a changed state; the change is pushed to the page
			}
		}
		return l.presence(ctx, c.Peer.Address), true, nil
	}
	return Presence{}, false, nil
}
