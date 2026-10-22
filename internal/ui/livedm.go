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
	return PersonView{Picture: p.Picture, PictureURL: pictureURL(p.Picture), Person: p.Person, Label: p.Label, Email: p.Email, Address: p.Address, Fingerprint: p.Fingerprint, State: p.State, Devices: deviceViews(p.Devices)}
}

// dmOverview adds this installation's person, the people and the DMs.
func (l *Live) dmOverview(o *Overview, unread *[]UnreadRow, mutes map[string]bool) error {
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
			o.People = append(o.People, PersonView{Label: p.Label, Email: p.Email, Picture: p.Picture, PictureURL: pictureURL(p.Picture), Address: p.Address, State: PersonListed, Devices: deviceViews(p.Devices)})
		}
	}
	convs, err := l.a.Conversations()
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
		var assigned, titles map[string]string
		var titleErr error
		projectTitles := func() error {
			if assigned != nil {
				return titleErr
			}
			assigned = client.ChatTopicAssignments(msgs) // complete graph, including read ancestors
			topics, err := l.a.ChatTopicsForMessages(c.ID, msgs, assigned)
			titleErr = err
			if err != nil {
				return err
			}
			titles = make(map[string]string, len(topics))
			for _, topic := range topics {
				titles[topic.ID] = topic.Title
			}
			return nil
		}
		people := l.conversationPeople(c)
		shown := shownRows(msgs, people) // its rows as the conversation shows them (DM)
		s := DMSummary{ID: c.ID, Role: c.Role, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Count: len(shown), LastAt: time.Unix(c.Created, 0)}
		// The core projects personal attention over canonical admitted rows,
		// including deliberately marked sent turns.
		for _, m := range shown {
			if m.Unread {
				s.Unread++
			}
		}
		*o.UnreadTotal += s.Unread
		if !mutes[c.ID] {
			*o.AlertTotal += s.Unread
		}
		if unread != nil && s.Unread > 0 {
			if err := projectTitles(); err != nil {
				return err
			}
			for _, m := range shown {
				if m.Unread {
					text := m.Body
					if m.Edited {
						text = m.Text
					}
					preview := client.BodyPreview(text)
					if !m.Edited && m.Body == "" && len(m.Attachments) > 0 {
						names := make([]string, 0, len(m.Attachments))
						for _, f := range m.Attachments {
							names = append(names, client.SafeName(f.Name))
						}
						preview = firstLine("📎 " + strings.Join(names, ", "))
					}
					row := UnreadRow{Kind: "chat", Conv: c.ID, Topic: assigned[m.LID], TopicTitle: titles[assigned[m.LID]], ID: m.ID, LID: m.LID, Author: cmp.Or(m.Key, m.Claimed), Dir: m.Dir, At: time.UnixMilli(m.AtMS), Label: firstLine(m.From), Preview: preview, ready: true}
					row.Key = unreadKey(row)
					*unread = append(*unread, row)
				}
			}
		}
		s.Kind, s.Frozen = c.Kind, c.Frozen
		if c.Role == "visitor" {
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
			// Match the message's delivery tick: one lagging device copy
			// must not turn delivery to that person back into an unsent turn.
			case m.Dir == "out" && cmp.Or(m.Delivery, m.State) == "waiting":
				s.Waiting++
			}
		}
		if len(shown) > 0 {
			line := func(m client.ConvMessage) string { // a participation record in words, not its body
				if m.Deleted {
					return "Message deleted"
				}
				if m.Edited {
					return client.BodyPreview(m.Text)
				}
				if m.Sub == envelope.SubEvent {
					return eventText(m.Body, people)
				}
				if envelope.TopicOrganization(m.TopicEvent) {
					if m.Topic != "" {
						if err := projectTitles(); err != nil {
							return client.TopicOrganizationNotice(m.Topic, "")
						}
					}
					return client.TopicOrganizationNotice(m.Topic, titles[m.Topic])
				}
				if m.Body == "" && len(m.Attachments) > 0 { // files only: their names
					var names []string
					for _, f := range m.Attachments {
						names = append(names, client.SafeName(f.Name))
					}
					return "📎 " + strings.Join(names, ", ")
				}
				return client.BodyPreview(m.Body)
			}
			last := shown[len(shown)-1]
			s.Title, s.Last, s.LastEvent = line(shown[0]), line(last), lastEvent(last, people)
			if c.Kind == protocol.ConvKindGroup {
				s.Title = c.Title
			}
			s.LastID, s.LastAt = previewMessageID(shown, last), time.Unix(last.At, 0)
		}
		o.DMs = append(o.DMs, s)
	}
	sort.SliceStable(o.DMs, func(i, j int) bool { return o.DMs[i].LastAt.After(o.DMs[j].LastAt) })
	return nil
}

// Prefer the stable logical reference only when it identifies this exact row.
// Different authors may deliberately use the same logical ID.
func previewMessageID(shown []client.ConvMessage, last client.ConvMessage) string {
	if last.LID != "" {
		for _, other := range shown {
			if other.ID != last.ID && (other.LID == last.LID || other.ID == last.LID) {
				return last.ID
			}
		}
		return last.LID
	}
	return last.ID
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
		t := DMThread{ID: id, Role: c.Role, Peer: personView(c.Peer), Created: time.Unix(c.Created, 0), Mine: c.Creator == l.a.Address,
			Messages: []DMMessage{}}
		t.Kind, t.Title, t.Frozen = c.Kind, c.Title, c.Frozen
		if c.Kind == protocol.ConvKindGroup {
			t.Peer.Label = c.Title
			if t.Members, err = l.groupMembers(c); err != nil {
				return DMThread{}, err
			}
			if c.Role == "member" {
				me, ok, e := l.a.Person()
				if e != nil {
					return DMThread{}, e
				}
				if ok && me.State == "self" && slices.ContainsFunc(me.Devices, func(d client.DeviceInfo) bool {
					return d.This && d.Human
				}) {
					resources, e := l.a.ConversationResources(id)
					if e != nil {
						return DMThread{}, e
					}
					t.Resources = &resources
				}
			}
		} else {
			t.Members = originalViews(c)
		}
		main, err := l.a.ChatMainTopicForMessages(id, msgs)
		if err != nil {
			return t, err
		}
		if main != nil {
			summary := threadSummary(*main, false)
			t.MainTopic = &summary
		}
		assigned := client.ChatTopicAssignments(msgs)
		topics, err := l.a.ChatTopicsForMessages(id, msgs, assigned)
		if err != nil {
			return DMThread{}, err
		}
		for _, topic := range topics {
			t.Topics = append(t.Topics, threadSummary(topic, false))
		}
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
		// These projections describe the same GET. Reuse its selected chat,
		// fresh participations and messages; actions still resolve authority.
		infos, err := l.a.Participations(id)
		if err != nil {
			return DMThread{}, err
		}
		people := l.conversationPeopleForParticipations(c, infos)
		words := l.a.PeerWords() // sentences name a person and device, never the address
		t.Guests = guestViewsFor(infos, guestMemberOf(c), guestEndsForMessages(msgs, infos))
		for _, guest := range t.Guests {
			t.AudiencePending = t.AudiencePending || guest.AudiencePending
		}
		if c.Role == "visitor" {
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
			dm := DMMessage{SendGroup: m.SendGroup, SendGroupAuthor: cmp.Or(m.Key, m.Claimed), Status: m.Status(), Topic: assigned[m.LID], TopicEvent: m.TopicEvent, ID: m.ID, LID: m.LID, AgentID: m.AgentID, Target: m.Target, Dir: m.Dir, From: m.From, Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Quote: m.Quote, SentAt: shownSent(time.Unix(m.Sent, 0), time.Unix(m.At, 0)), Delivery: m.Delivery,
				Origin: m.Origin, State: m.State, StateText: DMStateText(m.Dir, m.Kind, m.State, words(laggingCopy(m, c.Peer.Address)), m.Detail),
				Detail: m.Detail, SendStopped: m.SendStopped, DeliveryUncertain: m.DeliveryUncertain, JobDetail: m.JobDetail, At: time.Unix(m.At, 0), Unread: m.Unread, Attention: m.Attention, Replica: m.Replica, PID: m.PID, Attachments: fileViews(m.Attachments), Via: m.Via, Copies: copyViews(m.Copies), SyncedFrom: syncedFrom(m), Controls: m.Controls, Exec: m.Exec}
			if envelope.TopicOrganization(m.TopicEvent) {
				title := ""
				for _, topic := range topics {
					if topic.ID == m.Topic {
						title = topic.Title
						break
					}
				}
				dm.Body = client.TopicOrganizationNotice(m.Topic, title)
			}
			if m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask || m.Exec != nil {
				dm.Continuation, err = l.a.ContinuationFor(m.ID, m.Key, m.Exec)
				if err != nil {
					return DMThread{}, err
				}
				dm.Stop, err = l.a.StopFor(m.ID, m.Key, m.Exec)
				if err != nil {
					return DMThread{}, err
				}
			}
			if m.SendStopped && m.State == "not_delivered" {
				dm.StateText = "Not sent; local sending stopped."
				if m.DeliveryUncertain {
					dm.StateText = "Delivery unconfirmed; local retries stopped. Cancellation cannot be confirmed."
				}
			}

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
				dm.Actions, dm.JobDetail = l.agentRequestActions(id, m.ID, m.Kind, m.State, m.JobDetail, m.From == l.a.Address && m.Human != nil && m.Human.AgentAuthor())
			case m.PID != "" && m.Dir == "out" && m.Job != "":
				dm.Actions, dm.JobDetail = l.agentRequestActions(id, m.ID, m.Kind, m.Job, m.JobDetail, m.From == l.a.Address && m.Human != nil && m.Human.AgentAuthor())
				// This device executes its own request. Its local job is the
				// execution state; the outgoing row still keeps transport state.
				dm.Exec = &client.ExecView{State: m.Job, Host: l.a.Address, Attempt: m.JobAttempt}
				if text := DMStateText("in", m.Kind, m.Job, words(c.Peer.Address), m.JobDetail); text != "" {
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
			if dm.Continuation != nil {
				dm.Actions = append(dm.Actions, DoContinue)
			}
			t.Messages = append(t.Messages, dm)
		}
		linkReplies(msgs, t.Messages)
		t.Agents = l.agentViews(infos, people, msgs)
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
