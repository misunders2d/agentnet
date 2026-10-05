package ui

import (
	"cmp"
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
	return PersonView{Person: p.Person, Label: p.Label, Email: p.Email, Address: p.Address, Fingerprint: p.Fingerprint, State: p.State, Devices: deviceViews(p.Devices)}
}

// dmOverview adds this installation's person, the people and the DMs.
func (l *Live) dmOverview(o *Overview) error {
	o.Persons = true
	o.Groups = true
	var err error
	if o.GroupInvitations, err = l.GroupInvitations(); err != nil {
		return err
	}
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
	listed, err := l.a.ListedPersons()
	if err != nil {
		return err
	}
	for _, p := range listed {
		if !byAddress[p.Address] {
			o.People = append(o.People, PersonView{Label: p.Label, Email: p.Email, Address: p.Address, State: PersonListed, Devices: deviceViews(p.Devices)})
		}
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
		if c.Kind == protocol.ConvKindGroup {
			continue
		}
		infos, err := l.a.Participations(c.ID)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if info.Host.Person == "" || info.Role == protocol.RoleHuman {
				continue
			}
			ls := links[info.Host.Person]
			i := slices.IndexFunc(ls, func(x AgentLink) bool { return x.Address == info.Host.Address && x.AgentID == info.AgentID })
			if i < 0 {
				ls, i = append(ls, AgentLink{Address: info.Host.Address, AgentID: info.AgentID}), len(ls)
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
		if c.Deleted { // deleted here, nothing later yet (client convclear.go)
			continue
		}
		msgs, err := l.a.ConversationMessages(c.ID)
		if err != nil {
			return err
		}
		people := l.conversationPeople(c)
		shown := shownRows(msgs, people) // its rows as the conversation shows them (DM)
		s := DMSummary{ID: c.ID, Role: c.Role, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Count: len(shown), LastAt: time.Unix(c.Created, 0)}
		// The badge counts rows the open timeline can mark read. Hidden
		// participation copies and this person's own turns add no unread.
		isUnread := map[string]bool{}
		for _, id := range unread[c.ID] {
			isUnread[id] = true
		}
		for _, m := range shown {
			if m.Dir == "in" && isUnread[m.ID] {
				s.Unread++
			}
		}
		s.Kind, s.Frozen = c.Kind, c.Frozen
		if c.Role == "visitor" && c.Kind == protocol.ConvKindDM {
			views, e := l.guestViews(c.ID)
			if e != nil {
				return e
			}
			if v, ok := hostGuest(views); ok {
				s.Role = "human_guest"
				s.Frozen = guestFrozen(v)
				if v.CanSend {
					s.Frozen = ""
				}
			}
		}
		if c.Kind == protocol.ConvKindGroup {
			s.Title = c.Title
			s.Peer.Label = c.Title
			if s.Members, err = l.groupMembers(c); err != nil {
				return err
			}
		} else {
			s.Members = originalViews(c)
		}
		// Who is present to help: a conversation whose participations
		// cannot be resolved here now (a group's pending context) counts none.
		if infos, err := l.a.Participations(c.ID); err == nil {
			for _, info := range infos {
				if info.State == client.PartActive {
					s.Guests++
				}
			}
		}
		for _, m := range msgs {
			switch {
			case m.Dir == "in" && m.State == "conv_held":
				s.Held++
			case m.Dir == "out" && m.State == "waiting":
				s.Waiting++
			}
		}
		if len(shown) > 0 {
			line := func(m client.ConvMessage) string { // a participation record in words, not its body
				if m.Sub == envelope.SubEvent {
					return eventText(m.Body, people)
				}
				if m.Body == "" && len(m.Attachments) > 0 { // files only: their names
					var names []string
					for _, f := range m.Attachments {
						names = append(names, client.SafeName(f.Name))
					}
					return "📎 " + strings.Join(names, ", ")
				}
				return firstLine(m.Body)
			}
			last := shown[len(shown)-1]
			s.Title, s.Last, s.LastEvent = line(shown[0]), line(last), lastEvent(last, people)
			if c.Kind == protocol.ConvKindGroup {
				s.Title = c.Title
			}
			s.LastAt = time.Unix(last.At, 0)
		}
		o.DMs = append(o.DMs, s)
	}
	sort.SliceStable(o.DMs, func(i, j int) bool { return o.DMs[i].LastAt.After(o.DMs[j].LastAt) })
	return nil
}

// shownRows are a conversation's messages as its timeline shows them: a
// participation record gets its own row only as dmPeople.eventShown says
// (one row per signed record, a held invitation standing for its own
// public scope), every turn its row. The chat list's lines and count are
// the timeline's.
func shownRows(msgs []client.ConvMessage, people dmPeople) []client.ConvMessage {
	seen := map[string]bool{}
	for _, m := range msgs { // a held invitation stands for its own public scope
		if ev, err := protocol.ParseParticipationEvent([]byte(m.Body)); m.Sub == envelope.SubEvent && err == nil && ev.Type == protocol.EventInvite {
			seen["invite/"+ev.PID] = true
		}
	}
	var out []client.ConvMessage
	for _, m := range msgs {
		if m.Sub == envelope.SubEvent && !people.eventShown(m, seen) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// linkReplies points each shown reply at the message it replies to as this
// device shows it. A reply names the copy its author's device holds: an
// agent's answer names its executor's copy of the request, which this
// device shows under another copy's id when it sent the request, or under
// its logical id (an external request's executor copy has the logical id
// as its id). A reply naming any copy or the logical id of a message here
// names that message's id; anything else stays as sent. A logical id is
// unique per sender key only: one that two keys used here names no one
// message, so a reply naming it stays as sent.
func linkReplies(msgs []client.ConvMessage, out []DMMessage) {
	keyOf, twice := map[string]string{}, map[string]bool{} // logical id → the key that sent it; ones two keys used
	for _, m := range msgs {
		key := cmp.Or(m.Key, m.Claimed)
		if k, ok := keyOf[m.LID]; ok && k != key {
			twice[m.LID] = true
		}
		keyOf[m.LID] = key
	}
	shown := map[string]string{}
	for _, m := range msgs {
		if m.LID != "" && !twice[m.LID] {
			shown[m.LID] = m.ID
		}
		for _, c := range m.Copies {
			shown[c.ID] = m.ID
		}
	}
	for _, m := range msgs { // an exact id is always its own message
		shown[m.ID] = m.ID
	}
	for i := range out {
		if id, ok := shown[out[i].ReplyTo]; ok {
			out[i].ReplyTo = id
		}
		if id, ok := shown[out[i].Quote]; ok {
			out[i].Quote = id
		}
	}
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
		t := DMThread{ID: id, Role: c.Role, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Messages: []DMMessage{}}
		t.Kind, t.Title, t.Frozen = c.Kind, c.Title, c.Frozen
		if c.Kind == protocol.ConvKindGroup {
			t.Peer.Label = c.Title
			if t.Members, err = l.groupMembers(c); err != nil {
				return DMThread{}, err
			}
		} else {
			t.Members = originalViews(c)
		}
		topics, err := l.a.ChatTopics(id)
		if err != nil {
			return DMThread{}, err
		}
		for _, topic := range topics {
			t.Topics = append(t.Topics, threadSummary(topic, false))
		}
		assigned := client.ChatTopicAssignments(msgs)
		var groupRefs []protocol.GroupHistoryRef
		if c.Kind == protocol.ConvKindGroup && c.Role == "member" && c.Frozen == "" {
			ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
			groupRefs, err = l.a.SelectGroupHistory(ctx, id, client.GroupHistorySelection{Last: 64})
			cancel()
			if err != nil {
				return DMThread{}, err
			}
		}
		if c.Peer.State == PersonConflict {
			t.Frozen = c.Peer.Address + " published a different person record than the one kept here, so this conversation is frozen: nothing more is sent in it."
		}
		people := l.conversationPeople(c)
		words := l.a.PeerWords() // sentences name a person and device, never the address
		if t.Guests, err = l.guestViews(id); err != nil {
			return DMThread{}, err
		}
		for _, guest := range t.Guests {
			t.AudiencePending = t.AudiencePending || guest.AudiencePending
		}
		if c.Role == "visitor" && c.Kind != protocol.ConvKindGroup {
			t.Frozen = "Invited agent context only: this host cannot send ordinary room messages."
			if guest, ok := hostGuest(t.Guests); ok {
				t.Role = "human_guest"
				t.Frozen = guestFrozen(guest)
				if guest.CanSend {
					t.Frozen = ""
				}
			}
		}
		known, err := l.a.KnownPersons()
		if err != nil {
			return DMThread{}, err
		}
		labels := map[string]string{}
		for _, p := range known {
			labels[p.Person] = p.Label
		}
		for _, m := range shownRows(msgs, people) {
			dm := DMMessage{Status: m.Status(), Topic: assigned[m.LID], TopicEvent: m.TopicEvent, ID: m.ID, LID: m.LID, AgentID: m.AgentID, Target: m.Target, Dir: m.Dir, From: m.From, Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Quote: m.Quote, SentAt: shownSent(time.Unix(m.Sent, 0), time.Unix(m.At, 0)), Delivery: m.Delivery,
				Origin: m.Origin, State: m.State, StateText: DMStateText(m.Dir, m.Kind, m.State, words(laggingCopy(m, c.Peer.Address)), m.Detail),
				Detail: m.Detail, JobDetail: m.JobDetail, At: time.Unix(m.At, 0), Unread: m.Dir == "in" && isUnread[m.ID], Replica: m.Replica, PID: m.PID, Attachments: fileViews(m.Attachments), Via: m.Via, Copies: copyViews(m.Copies), SyncedFrom: syncedFrom(m), Controls: m.Controls, Exec: m.Exec}

			for i := range dm.Copies {
				if dm.Copies[i].Own {
					dm.Copies[i].Person = "You"
				} else if label := labels[dm.Copies[i].Person]; label != "" {
					dm.Copies[i].Person = label
				} else {
					dm.Copies[i].Person = "Someone"
				}
			}

			if m.Human != nil && m.Human.AgentAuthor() {
				dm.AgentAuthorPID = m.Human.AuthorPID
			}
			dm.ExcerptPID, dm.ClaimedKey, dm.VerifiedAgent = m.ExcerptPID, m.Claimed, m.VerifiedAgent
			if c.Kind == protocol.ConvKindGroup {
				key := m.Key
				if key == "" {
					key = m.Claimed
				}
				for _, ref := range groupRefs {
					if ref.LID == m.LID && ref.Author == key {
						r := ref
						dm.GroupRef = &r
						break
					}
				}
			}
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
				if text := DMStateText("in", m.Kind, m.Job, words(c.Peer.Address), ""); text != "" {
					dm.StateText = "Your agent: " + strings.ToLower(text[:1]) + text[1:]
				}
			}
			if m.Kind == KindAnswer && m.Status() == envelope.StatusProposal && l.a.CanConfirmProposal(m.ID) {
				dm.Actions = []string{DoIt}
			}
			if m.Kind == KindTask {
				dm.Proposal, _ = l.a.ProposalOf(m.ID)
			}
			if m.Sub == envelope.SubEvent {
				dm.Event, dm.Body, dm.StateText = eventText(m.Body, people), "", ""
				dm.EventType, _, dm.EventBy = eventFields(m.Body, people)
			}
			t.Messages = append(t.Messages, dm)
		}
		linkReplies(msgs, t.Messages)
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
	if envelope.Blank(body) && len(d.Files) == 0 {
		return Sent{}, Refuse("Write a message or add a file first.")
	}
	receiver, err := l.selectedReplyReceiver(d.ReplyReceiver)
	if err != nil {
		return Sent{}, err
	}
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return Sent{}, err
	}
	defer cleanup() // SendConv encrypted them into the spool, or refused: either way the staged copies go
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	ctx = client.WithQueuedSend(ctx, d.ID)
	res, err := l.a.SendConv(ctx, d.Conv, client.ConvOutgoing{Topic: d.Topic, Kind: envelope.KindMessage, Body: body, ReplyTo: d.ReplyTo, Quote: d.Quote, Origin: envelope.OriginUI, Files: files, ReplyReceiver: receiver, PID: d.PID})
	if err != nil {
		return Sent{}, Refuse(sentence(err))
	}
	return Sent{LID: res.LID, ID: res.ID, State: res.State, Detail: res.Detail}, nil
}

// refreshDM is Refresh for a DM or group (ok false when id is not one): once
// per open, ask the Hub about copies it still holds, and, for a DM, about the
// person's device.
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
		// A message shows its least advanced copy's state, so every copy
		// still held is asked about, not only the first one (often the other
		// person's, long delivered, while a copy to one of this person's own
		// devices stays shown as held forever), and also the held copies of
		// a message shown by a less advanced one (failed, or kept here).
		const maxReceipts = 20
		asked := 0
		for i := len(msgs) - 1; i >= 0 && asked < maxReceipts; i-- {
			m := msgs[i]
			if m.Dir != "out" {
				continue
			}
			for _, copy := range m.Copies { // none: sent from another device of this person
				if copy.State == protocol.StateCustody && asked < maxReceipts {
					asked++
					l.a.Status(ctx, copy.ID, 0) // stores a changed state; the change is pushed to the page
				}
			}
		}
		if c.Kind == protocol.ConvKindGroup {
			return Presence{}, true, nil
		}
		return l.presence(ctx, c.Peer.Address), true, nil
	}
	return Presence{}, false, nil
}
