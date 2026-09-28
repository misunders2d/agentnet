package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Live is the Provider over this installation's real data. Every action
// goes through the client's existing operation for it, with the same rules
// as the CLI: nothing here grants, approves or runs anything on its own.
type Live struct {
	a *client.Agent
	// timeout bounds each network request an action makes.
	timeout time.Duration
}

// NewLive returns the Provider for agent a. It is meant to run inside the
// daemon that owns a's home.
func NewLive(a *client.Agent) *Live { return &Live{a: a, timeout: 20 * time.Second} }

// Changed implements Provider.
func (l *Live) Changed() (uint64, <-chan struct{}) { return l.a.Changed() }

// Overview implements Provider.
func (l *Live) Overview() (Overview, error) {
	seq, _ := l.a.Changed()
	o := Overview{Me: Me{Address: l.a.Address, Fingerprint: l.a.Self().Fingerprint()}, Seq: seq,
		Threads: []ThreadSummary{}, Review: []ReviewItem{}, Quarantine: []QuarantineItem{}}
	if r, err := l.a.Responder(); err == nil && r != nil {
		o.Me.Responder, o.Me.ResponderDir = r.Harness, r.Dir
	}
	if r, ok := l.a.Release(); ok && r.Version != protocol.Version {
		o.Release = r.Version
	}
	threads, err := l.a.Threads()
	if err != nil {
		return o, err
	}
	changedKeys := map[string]bool{}
	for _, t := range threads {
		if _, seen := changedKeys[t.Peer]; !seen {
			k, err := l.a.PeerKeyOf(t.Peer)
			if err != nil {
				return o, err
			}
			changedKeys[t.Peer] = k.Pending != ""
		}
		o.Threads = append(o.Threads, ThreadSummary{ID: t.ID, Peer: t.Peer, Title: t.Title, Last: t.Last, LastAt: t.LastAt,
			Count: t.Count, Review: t.Review, Unread: t.Unread, Running: t.Running, Waiting: t.Waiting, KeyChanged: changedKeys[t.Peer]})
	}
	review, err := l.a.Review()
	if err != nil {
		return o, err
	}
	for _, m := range review {
		o.Review = append(o.Review, ReviewItem{ID: m.ID, Peer: m.From, Kind: m.Kind, Why: ReviewWhy(m.Kind, m.State, m.From, m.Detail), Excerpt: excerpt(m.Body)})
	}
	q, err := l.a.Quarantine()
	if err != nil {
		return o, err
	}
	for _, x := range q {
		reason := "It did not verify, so its content is not shown."
		if x.Reason == "key_changed" {
			reason = "Held until you trust " + x.Sender + "'s changed key."
		}
		o.Quarantine = append(o.Quarantine, QuarantineItem{ID: x.ID, Peer: x.Sender, Reason: reason, At: x.ReceivedAt})
	}
	return o, nil
}

// Thread implements Provider.
func (l *Live) Thread(id string) (Thread, error) {
	c, err := l.a.Conversation(id, 0, 0)
	if errors.Is(err, client.ErrNoMessage) {
		return Thread{}, NotFound("no message with that id")
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
	if approved, err := l.a.QuestionApprovals(); err == nil {
		for _, a := range approved {
			if a == c.Peer {
				t.Approved = true
			}
		}
	}
	if grants, err := l.a.TaskGrants(); err == nil {
		for _, g := range grants {
			if g.Address == c.Peer {
				t.TaskGrant = g.Status
			}
		}
	}
	replied := map[string]bool{}
	for _, m := range c.Messages {
		if m.Dir == "in" && m.ReplyTo != "" {
			replied[m.ReplyTo] = true
		}
	}
	for _, m := range c.Messages {
		v := Message{ID: m.ID, Dir: m.Dir, From: m.From, To: m.To, Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, At: m.At,
			State: m.State, Status: m.Status, Path: m.Path, Responder: m.Responder, Summary: m.Summary, Detail: m.Detail}
		if m.Read != nil && !*m.Read {
			v.Unread = true
		}
		for _, f := range m.Attachments {
			v.Files = append(v.Files, File{Name: f.Name, Size: f.Size, Saved: f.SavedPath})
		}
		if m.Dir == "in" {
			v.Author = Author{Label: m.From, About: "Signed with " + m.From + "'s key. Whether a person or one of their agents wrote it is not recorded."}
			v.Actions = ActionsFor(m.Kind, m.State)
		} else {
			v.Author = Author{Label: "This computer", About: "Sent from this installation: this page, the command line, or an agent session here. Which one is not recorded."}
		}
		v.StateText = StateText(m.Dir, m.Kind, m.State, c.Peer)
		switch Next(m.Dir, m.Kind, m.State, c.Peer, replied[m.ID]) {
		case "you":
			v.Next = "Needs you"
		case "your responder":
			v.Next = "Your responder is on it"
		case "":
		default:
			v.Next = "Waiting on " + c.Peer
		}
		t.Messages = append(t.Messages, v)
	}
	return t, nil
}

// Refresh implements Refresher: once per open thread, ask the Hub about the
// peer's daemons and the receipts of messages it still holds for them.
func (l *Live) Refresh(threadID string) (Presence, error) {
	c, err := l.a.Conversation(threadID, 0, 0)
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
	sessions, err := l.a.Sessions(ctx, c.Peer)
	if err != nil {
		return Presence{Text: "Connection unknown"}, nil
	}
	live := 0
	for _, s := range sessions {
		if s.Connected {
			live++
		}
	}
	switch {
	case live > 0:
		return Presence{Text: "Their computer is connected"}, nil
	case len(sessions) > 0:
		return Presence{Text: "Their computer is reconnecting"}, nil
	}
	return Presence{Text: "Their computer is offline"}, nil
}

// Send implements Provider.
func (l *Live) Send(d Draft) (Sent, error) {
	body := strings.TrimSpace(d.Body)
	if body == "" {
		return Sent{}, Refuse("Write a message first.")
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
			return Sent{}, Refuse(err.Error())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	res, err := l.a.SendMessage(ctx, client.Outgoing{To: d.To, Body: body, ReplyTo: d.ReplyTo, Kind: d.Kind})
	if err != nil {
		return Sent{}, Refuse(sentence(err))
	}
	l.awaitReceipt(res)
	return Sent{ID: res.ID, State: res.State, Path: res.Path, Detail: res.Detail}, nil
}

// receiptWait is how long a send from the page waits for the recipient's
// receipt, as agentnet send does by default.
const receiptWait = 5 * time.Second

// awaitReceipt waits once, in the background, for the receipt of a message
// the Hub holds. A delivery is stored and so pushed to the page; the page
// does not wait for it and nothing is asked again afterwards.
func (l *Live) awaitReceipt(res client.SendResult) {
	if res.Path != protocol.PathRelay || res.State != protocol.StateCustody {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), receiptWait+l.timeout)
		defer cancel()
		l.a.Status(ctx, res.ID, receiptWait)
	}()
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
		var res client.SendResult
		res, err = l.a.Reply(ctx, x.ID, x.Body)
		l.awaitReceipt(res)
		note = "Reply sent."
	case DoAccept:
		err = l.a.Accept(x.ID)
		note = "Accepted. Your responder takes it from here."
	case DoAcceptAlways:
		var sender, fp string
		sender, fp, err = l.a.AcceptAlways(x.ID)
		note = fmt.Sprintf("Accepted. Later tasks from %s's key %s run without asking.", sender, shortFP(fp))
	case DoDecline:
		var res client.SendResult
		res, err = l.a.Decline(ctx, x.ID, strings.TrimSpace(x.Reason))
		l.awaitReceipt(res)
		note = "Declined."
	case DoResolve:
		err = l.a.Resolve(x.ID)
		note = "Closed. Nothing was sent."
	case DoCancel:
		err = l.a.Cancel(x.ID)
		note = "Stopping your responder."
	case DoApprove:
		err = l.a.Approve(x.ID)
		note = x.ID + "'s future questions are answered automatically."
	case DoUnapprove:
		err = l.a.Unapprove(x.ID)
		note = x.ID + "'s questions wait for you again."
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
func sentence(err error) string { return capitalize(err.Error()) }

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
