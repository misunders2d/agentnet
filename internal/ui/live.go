package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Live is the Provider over this installation's real data. Every action
// goes through the client's existing operation for it, with the same rules
// as the CLI: nothing here grants, approves or runs anything on its own.
type Live struct {
	a              *client.Agent
	assistantSetup AssistantSetupFunc
	// timeout bounds each network request an action makes.
	timeout time.Duration
	staged  staged // files the page handed over, not sent yet
	app     bool   // served by the AgentNet app (SetApp)
}

// SetApp says the page is the AgentNet app's window (overview.app).
func (l *Live) SetApp(app bool) { l.app = app }

// recommended is the build the Hub's operator recommends, only when it is
// newer than this one: a preview newer than the stable release is never
// told to go back to it, and an unknown version string recommends nothing.
func recommended(r protocol.Release, ok bool) string {
	if ok && protocol.Newer(r.Version, protocol.Version) {
		return r.Version
	}
	return ""
}

// NewLive returns the Provider for agent a. It is meant to run inside the
// daemon that owns a's home: files a previous run's page handed over and
// never sent are removed first, as nothing can be staging yet.
func NewLive(a *client.Agent) *Live {
	if err := a.CleanStaging(); err != nil && a.Logf != nil {
		a.Logf("messenger page: files left from an earlier run: %v", err)
	}
	return &Live{a: a, timeout: 20 * time.Second}
}

// Changed implements Provider.
func (l *Live) Changed() (uint64, <-chan struct{}) { return l.a.Changed() }

// Overview implements Provider: every thread, archived topics included.
func (l *Live) Overview() (Overview, error) { return l.overview(true) }

// TopicOverview implements Topics: archived topics counted, not listed.
func (l *Live) TopicOverview() (Overview, error) { return l.overview(false) }

func (l *Live) overview(listArchived bool) (Overview, error) {
	seq, _ := l.a.Changed()
	o := Overview{App: l.app, Me: Me{Address: l.a.Address, Fingerprint: l.a.Self().Fingerprint()}, Seq: seq, Version: protocol.Version,
		Threads: []ThreadSummary{}, Review: []ReviewItem{}, NeedsYou: []ConvItem{}, Held: []ConvItem{}, Quarantine: []QuarantineItem{}}
	if r, err := l.a.Responder(); err == nil && r != nil {
		o.Me.Responder, o.Me.ResponderDir = r.Harness, r.Dir
	}
	o.Me.Agent, o.AgentDevices = l.a.AdvertisesAgent(), l.a.AgentDevices()
	o.Workspace = &WorkspaceView{Name: l.a.WorkspaceName(), Server: l.a.RelayHost()}
	words := l.a.PeerWords() // people never see addresses in sentences
	o.Release = recommended(l.a.Release())
	o.Directory = directoryOf(l.a.MemberView(), l.a.Address)
	if n, err := l.notifyView(); err == nil {
		o.Notify = n
	}
	if rs, err := l.reminders(); err == nil {
		o.Reminders, o.Remind = rs, true
	}
	o.Files = l.fileLimits()
	if err := l.dmOverview(&o); err != nil {
		return o, err
	}
	if err := l.identityOverview(&o); err != nil {
		return o, err
	}
	threads, peers, err := l.a.TopicOverview(listArchived) // livetopics.go
	if err != nil {
		return o, err
	}
	changedKeys := map[string]bool{}
	changed := func(peer string) (bool, error) {
		if v, seen := changedKeys[peer]; seen {
			return v, nil
		}
		k, err := l.a.PeerKeyOf(peer)
		changedKeys[peer] = k.Pending != ""
		return changedKeys[peer], err
	}
	for _, t := range threads {
		kc, err := changed(t.Peer)
		if err != nil {
			return o, err
		}
		o.Threads = append(o.Threads, threadSummary(t, kc))
	}
	for _, p := range peers {
		kc, err := changed(p.Peer)
		if err != nil {
			return o, err
		}
		o.Topics = append(o.Topics, PeerTopics{Peer: p.Peer, Total: p.Total, Archived: p.Archived, ArchivedUnread: p.ArchivedUnread, Latest: threadSummary(p.Latest, kc)})
	}
	review, err := l.a.PageReview()
	if err != nil {
		return o, err
	}
	for _, c := range review.Conv {
		o.NeedsYou = append(o.NeedsYou, convItem(c, words))
	}
	countDecisions(&o)
	for _, c := range review.Held {
		o.Held = append(o.Held, convItem(c, words))
	}
	for _, m := range review.Device {
		item := ReviewItem{ID: m.ID, Peer: m.From, Kind: m.Kind, Why: ReviewWhy(m.Kind, m.State, words(m.From), m.Detail),
			Excerpt: excerpt(m.Body), At: m.ReceivedAt, Notice: IsReviewNotice(m.Kind, m.Status, m.ReplyTo, len(m.Attachments))}
		if item.Notice {
			if r, ok := l.a.NoticeReport(m); ok {
				item.Report, item.Excerpt = &r, fmt.Sprintf("%s reported %d waiting request(s)", r.Host, len(r.Items))
			}
		}
		o.Review = append(o.Review, item)
	}
	if err := l.selfConsentReview(&o); err != nil { // own agents that joined without a click (liveselfconsent.go)
		return o, err
	}
	q, err := l.a.Quarantine()
	if err != nil {
		return o, err
	}
	o.Quarantine = quarantineItems(q)
	for i := range o.Quarantine {
		o.Quarantine[i].Reason = holdReason(q[i].Reason, words(q[i].Sender))
	}
	return o, nil
}

// quarantineItems is the overview's held-back list (never null): each
// item's code says why (holdCode), so a page words it with the sender's name.
func quarantineItems(q []client.Quarantined) []QuarantineItem {
	out := make([]QuarantineItem, 0, len(q))
	for _, x := range q {
		out = append(out, QuarantineItem{ID: x.ID, Peer: x.Sender, Code: holdCode(x.Reason), Reason: holdReason(x.Reason, x.Sender), At: x.ReceivedAt})
	}
	return out
}

// countDecisions sets each conversation's Decide: its requests among
// o.NeedsYou that this device's person decides here (with actions; an
// invitation is decided by its PID, not counted).
func countDecisions(o *Overview) {
	decide := map[string]int{}
	for _, c := range o.NeedsYou {
		if c.ID != "" && len(c.Actions) > 0 {
			decide[c.Conv]++
		}
	}
	for i := range o.DMs {
		o.DMs[i].Decide = decide[o.DMs[i].ID]
	}
}

// convItem is a conversation item waiting for the person, as the page
// lists it, with the decisions this installation takes on it. words names
// its sender for the sentence (client.PeerWords).
func convItem(c client.ConvReview, words func(string) string) ConvItem {
	v := ConvItem{Reason: c.Reason, Conv: c.Conv, PID: c.PID, ID: c.ID, Peer: c.From, Kind: c.Kind, Excerpt: excerpt(c.Body), At: c.At, Unread: c.Unread}
	switch c.Reason {
	case client.ReviewInvite:
		v.Why, v.Actions = c.Detail, []string{DoAccept, DoDecline}
	case client.ReviewHeldTurn:
		v.Why = DMStateText("in", c.Kind, c.State, words(c.From), c.Detail)
	default:
		v.Why, v.Actions = ReviewWhy(c.Kind, c.State, words(c.From), c.Detail), AgentActions(c.Kind, c.State)
	}
	return v
}

// directoryOf turns the daemon's member view into the page's directory:
// presence only while the view is current, and without this installation.
func directoryOf(v client.MemberView, self string) Directory {
	d := Directory{Status: DirectoryUnknown, Members: []DirMember{}}
	switch v.Listed {
	case client.MembersListed:
		d.Status = DirectoryListed
	case client.MembersNotListed:
		d.Status = DirectoryNotListed
	}
	d.Current, d.At, d.Truncated = v.Current, v.At, v.Members.Truncated
	for _, m := range v.Members.Members {
		if m.Address == self {
			continue
		}
		e := DirMember{Address: m.Address, Joined: time.Unix(m.Joined, 0)}
		if v.Current {
			e.Presence = m.Presence
		}
		d.Members = append(d.Members, e)
	}
	return d
}

// holdReason says why a received message is held back, for the person.
func holdReason(reason, sender string) string {
	switch reason {
	case "key_changed":
		return "Held until you trust " + sender + "'s changed key."
	case "proof_pending":
		return "Held until the conversation or person it names can be checked here. Nothing runs it."
	case "identity_conflict":
		return "Held: it disagrees with the person record kept here for " + sender + ". Nothing runs it."
	case "conflicting_duplicate":
		return "Held: " + sender + " sent different content under a message it already sent. Nothing runs it."
	}
	return "It did not verify, so its content is not shown."
}

// notShownHere answers for a conversation (DM) message: the page shows one
// installation's device history, and conversations only through agentnet dm.
const notShownHere = "This message belongs to a conversation, which this page does not show yet: see agentnet dm show."

// Thread implements Provider.
func (l *Live) Thread(id string) (Thread, error) {
	c, err := l.a.Conversation(id, 0, 0)
	if errors.Is(err, client.ErrNoMessage) {
		return Thread{}, NotFound("no message with that id")
	}
	if errors.Is(err, client.ErrConversationItem) {
		return Thread{}, Refuse(notShownHere)
	}
	if err != nil {
		return Thread{}, err
	}
	t := Thread{ID: id, Peer: c.Peer, Messages: []Message{}}
	k, err := l.a.PeerKeyOf(c.Peer)
	if err != nil {
		return t, err
	}
	t.Key = PeerKey{Pinned: k.Pinned, Pending: k.Pending}
	topic, err := l.a.TopicOf(id)
	if err != nil {
		return t, err
	}
	ts := threadSummary(topic, k.Pending != "")
	t.Topic = &ts
	if approved, err := l.a.QuestionApprovals(); err == nil {
		for _, a := range approved {
			if a == c.Peer {
				t.Approved = true
				t.QuestionTarget = c.Peer
			}
		}
	}
	if grants, err := l.a.TaskGrants(); err == nil {
		for _, g := range grants {
			if g.Address == c.Peer {
				t.TaskGrant = g.Status
				t.TaskTarget = c.Peer
			}
		}
	}
	if person, qg, tg, e := l.a.PersonGrantForPeer(c.Peer, k.Pinned); e != nil {
		return t, e
	} else if person != "" {
		if people, e := l.a.KnownPersons(); e != nil {
			return t, e
		} else {
			for _, p := range people {
				if p.Person == person {
					v := personView(p)
					t.PermissionPerson = &v
					break
				}
			}
		}
		if qg {
			t.Approved = true
			t.QuestionTarget = person
		}
		if tg {
			t.TaskGrant = "active"
			t.TaskTarget = person
		}
	}
	if questions, _, e := l.a.PermissionState(c.Peer, k.Pinned); e != nil {
		return t, e
	} else {
		t.Approved = questions
	}
	replied := map[string]bool{}
	for _, m := range c.Messages {
		if m.Dir == "in" && m.ReplyTo != "" {
			replied[m.ReplyTo] = true
		}
	}
	peer := l.a.PeerWords()(c.Peer) // the sentences name a person and device, never the address
	for _, m := range c.Messages {
		v := Message{ID: m.ID, AgentID: m.AgentID, Target: m.Target, Dir: m.Dir, From: m.From, To: m.To, Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Quote: m.Quote, At: m.At, SentAt: shownSent(m.SentAt, m.At),
			State: m.State, Status: m.Status, Path: m.Path, Responder: m.Responder, Summary: m.Summary, Detail: m.Detail, Controls: m.Controls, Exec: m.Exec}
		if m.Read != nil && !*m.Read {
			v.Unread = true
		}
		for i, f := range m.Attachments {
			fv := File{Index: i, Name: client.SafeName(f.Name), Size: f.Size, Saved: f.SavedPath, Openable: f.Openable} // the name it is saved under
			if !fv.Openable {
				fv.Note = notKeptNote
			}
			v.Files = append(v.Files, fv)
		}
		if m.Dir == "in" {
			v.Author = Author{Label: m.From, About: "Signed with " + m.From + "'s key. Whether a person or one of their agents wrote it is not recorded."}
			v.Actions = ActionsFor(m.Kind, m.State)
		} else {
			v.Author = Author{Label: "This computer", About: "Sent from this installation: this page, the command line, or an agent session here. Which one is not recorded."}
		}
		if m.AgentID != "" {
			v.Author = Author{Label: "Agent " + m.AgentID, About: "Named executor asserted by host " + m.From + "; its host key and request bind this ID."}
		}
		v.StateText = StateText(m.Dir, m.Kind, m.State, peer)
		switch Next(m.Dir, m.Kind, m.State, peer, replied[m.ID]) {
		case "you":
			v.Next = "Needs you"
		case "your responder":
			v.Next = "Your responder is on it"
		case "":
		default:
			v.Next = "Waiting on " + peer
		}
		t.Messages = append(t.Messages, v)
	}
	return t, nil
}

// Refresh implements Refresher: once per open thread, ask the Hub about the
// peer's daemons and the receipts of messages it still holds for them.
func (l *Live) Refresh(threadID string) (Presence, error) {
	if p, ok, err := l.refreshDM(threadID); ok || err != nil {
		return p, err
	}
	c, err := l.a.Conversation(threadID, 0, 0)
	if errors.Is(err, client.ErrConversationItem) {
		return Presence{}, Refuse(notShownHere)
	}
	if err != nil {
		return Presence{}, NotFound("no message with that id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	const maxReceipts = 20
	asked := 0
	for i := len(c.Messages) - 1; i >= 0 && asked < maxReceipts; i-- {
		if m := c.Messages[i]; m.Dir == "out" && m.State == protocol.StateCustody {
			asked++
			l.a.Status(ctx, m.ID, 0) // stores a changed state; the change is pushed to the page
		}
	}
	return l.presence(ctx, c.Peer), nil
}

// presence asks the Hub once about peer's daemons.
func (l *Live) presence(ctx context.Context, peer string) Presence {
	sessions, err := l.a.Sessions(ctx, peer)
	if err != nil {
		return Presence{Text: "Connection unknown"}
	}
	live := 0
	for _, s := range sessions {
		if s.Connected {
			live++
		}
	}
	now := time.Now()
	switch {
	case live > 0:
		return Presence{Text: "Their computer is connected", At: now}
	case len(sessions) > 0:
		return Presence{Text: "Their computer is reconnecting", At: now}
	}
	return Presence{Text: "Their computer is offline", At: now}
}

// Send implements Provider.
func (l *Live) Send(d Draft) (Sent, error) {
	body := strings.TrimSpace(d.Body)
	if envelope.Blank(body) && len(d.Files) == 0 {
		return Sent{}, Refuse("Write a message or add a file first.")
	}
	switch d.Kind {
	case "", KindMessage, KindQuestion, KindTask:
	default:
		return Sent{}, Refuse("Choose message, question or task.")
	}
	if _, _, err := protocol.SplitAddress(d.To); err != nil {
		return Sent{}, Refuse("That is not an AgentNet address.")
	}
	if d.ReplyTo != "" {
		if err := l.a.CheckReplyTo(d.ReplyTo, d.To); err != nil {
			return Sent{}, Refuse(sentence(err))
		}
	}
	receiver, err := l.selectedReplyReceiver(d.ReplyReceiver)
	if err != nil {
		return Sent{}, err
	}
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return Sent{}, err
	}
	defer cleanup() // SendMessage encrypted them into the spool, or refused: either way the staged copies go
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	target, err := l.namedSendTarget(ctx, d)
	if err != nil {
		return Sent{}, err
	}
	res, err := l.a.SendMessage(ctx, client.Outgoing{To: d.To, Body: body, ReplyTo: d.ReplyTo, Quote: d.Quote, Kind: d.Kind, Named: files, Target: target, ReplyReceiver: receiver})
	if err != nil {
		return Sent{}, Refuse(sentence(err))
	}
	return Sent{ID: res.ID, State: res.State, Path: res.Path, Detail: res.Detail}, nil
}

// Act implements Provider.
func (l *Live) Act(x Action) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	var err error
	note := ""
	switch x.Do {
	case DoReply:
		if strings.TrimSpace(x.Body) == "" {
			return "", Refuse("Write a reply first.")
		}
		_, err = l.a.Reply(ctx, x.ID, x.Body)
		note = "Reply sent."
	case DoAccept:
		err = l.a.Accept(x.ID)
		note = "Accepted. Your responder takes it from here."
	case DoAcceptAlways:
		var sender, fp string
		sender, fp, err = l.a.AcceptAlways(x.ID)
		if protocol.ValidID(sender) {
			note = "Accepted. Later tasks from " + l.a.PermissionLabel(sender) + "'s current and future verified devices run without asking."
		} else {
			note = fmt.Sprintf("Accepted. Later tasks from %s's key %s run without asking.", sender, shortFP(fp))
		}
	case DoDecline:
		_, err = l.a.Decline(ctx, x.ID, strings.TrimSpace(x.Reason))
		note = "Declined."
	case DoResolve:
		err = l.a.Resolve(x.ID)
		note = "Closed. No reply was sent."
	case DoCancel:
		err = l.a.Cancel(x.ID)
		note = "Stopping your responder."
	case DoApprove:
		err = l.a.Approve(x.ID)
		note = l.a.PermissionLabel(x.ID) + "'s future questions are answered automatically."
	case DoUnapprove:
		err = l.a.Unapprove(x.ID)
		note = l.a.PermissionLabel(x.ID) + "'s questions wait for you again."
	case DoTrust:
		if x.Key == "" {
			return "", Refuse("Compare the fingerprint first: trust names the key you compared.")
		}
		var fp string
		fp, err = l.a.TrustKey(ctx, x.ID, x.Key)
		note = "Trusted " + x.ID + "'s key " + shortFP(fp) + "."
	case DoRevokeTasks:
		_, err = l.a.RevokeTasks(x.ID)
		note = "Tasks from " + x.ID + " wait for you again."
	case DoRead:
		err = l.a.MarkRead(x.IDs)
	default:
		return "", Refuse("Unknown action.")
	}
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	return note, nil
}

// sentence turns a client error into text for the person. Client errors are
// written for people already; this only makes the first letter upper case.
func sentence(err error) string {
	var update *client.NeedsUpdateError
	if errors.As(err, &update) {
		return "This person's app needs an update first."
	}
	return capitalize(strings.TrimPrefix(err.Error(), "cannot be retried: "))
}
func shownSent(sent, arrival time.Time) time.Time {
	if sent.Unix() <= 0 || sent.Unix() >= maxClaimedUnix || sent.After(arrival) {
		return arrival
	}
	return sent
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func shortFP(fp string) string {
	if len(fp) > 23 {
		return fp[:23] + "…"
	}
	return fp
}

// Preserve machine-readable capability errors internally; the page uses names.
func (l *Live) updateSentence(err error) string {
	var update *client.NeedsUpdateError
	if errors.As(err, &update) {
		if people, e := l.a.KnownPersons(); e == nil {
			for _, p := range people {
				for _, d := range p.Devices {
					if d.Address == update.Address {
						return p.Label + "’s app needs an update first."
					}
				}
			}
		}
	}
	return sentence(err)
}
