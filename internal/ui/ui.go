// Package ui serves the local messenger: a conversation-first web page on a
// loopback address, backed by a Provider.
//
// Today the only Provider is the in-memory demo fixture (agentnet ui --demo):
// nothing is read from or written to an AgentNet home, the Hub or the
// network. The same handler is meant to be hosted later by the daemon with a
// Provider built on the client's existing methods.
//
// The page gets changes pushed over one server-sent event stream that carries
// only a change counter; it never polls and events never carry content.
package ui

import (
	"errors"
	"strings"
	"time"
)

// Provider is what the page needs from its backing store. It covers only
// the actions the page uses.
type Provider interface {
	// State is the overview: this computer, conversations and review items.
	State() State
	// Conversation is every message with one peer, oldest first.
	Conversation(peer string) (Conversation, error)
	// Send sends a new message, question or task, or a reply.
	Send(d Draft) (Message, error)
	// Act applies a local decision to a received item or a peer.
	Act(a Action) error
	// Changed returns the current change counter and a channel that is
	// closed at the next change.
	Changed() (uint64, <-chan struct{})
}

// Simulator is implemented only by demo providers: controls that stand in
// for things that happen elsewhere (a peer writing, a responder finishing).
type Simulator interface {
	Simulate(what string) error
}

// State is the overview shown around the conversation.
type State struct {
	Demo          bool          `json:"demo"`
	Me            string        `json:"me"`
	Machine       Machine       `json:"machine"`
	Conversations []ConvSummary `json:"conversations"`
	Review        []ReviewItem  `json:"review"`
	Seq           uint64        `json:"seq"`
}

// Machine describes this computer's side.
type Machine struct {
	Hub          string `json:"hub"`           // plain-language connection state
	Responder    string `json:"responder"`     // harness name, or "" for manual only
	ResponderDir string `json:"responder_dir"` // where the responder runs
}

// ConvSummary is one row of the conversation list.
type ConvSummary struct {
	Peer     string    `json:"peer"`
	Presence string    `json:"presence"` // plain text; never claims a person is there
	Last     string    `json:"last"`     // first line of the latest message
	LastAt   time.Time `json:"last_at"`
	Next     string    `json:"next"`   // who owes the next move, plain text; "" when nothing is open
	Review   int       `json:"review"` // items in this conversation waiting for you
	Paused   bool      `json:"paused"` // sending paused (key changed)
}

// ReviewItem is a received item waiting for a local decision.
type ReviewItem struct {
	ID      string `json:"id"`
	Peer    string `json:"peer"`
	Kind    string `json:"kind"`
	Why     string `json:"why"`
	Excerpt string `json:"excerpt"`
}

// Conversation is one peer's messages.
type Conversation struct {
	Peer     string    `json:"peer"`
	Presence string    `json:"presence"`
	Notice   *Notice   `json:"notice,omitempty"`
	Messages []Message `json:"messages"`
}

// Notice is a conversation-wide warning such as a changed key.
type Notice struct {
	Kind string `json:"kind"` // "key_changed"
	Text string `json:"text"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
}

// Message is one row of a conversation.
type Message struct {
	ID        string    `json:"id"`
	Dir       string    `json:"dir"` // in, out or system
	Peer      string    `json:"peer"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body,omitempty"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	At        time.Time `json:"at"`
	State     string    `json:"state,omitempty"`  // stored state, shown under details
	Status    string    `json:"status,omitempty"` // outcome carried by an answer or result
	Path      string    `json:"path,omitempty"`   // relay or direct
	Author    Author    `json:"author"`
	StateText string    `json:"state_text,omitempty"` // plain-language state
	Next      string    `json:"next,omitempty"`       // who owes the next move
	Note      *Note     `json:"note,omitempty"`
	Files     []File    `json:"files,omitempty"`
	Actions   []string  `json:"actions,omitempty"` // decisions available here
	Detail    string    `json:"detail,omitempty"`  // reason for failures and needs-human
}

// Author says who wrote a message, only as far as it is recorded.
type Author struct {
	Label  string `json:"label"`            // short, plain
	About  string `json:"about"`            // what is and is not known, for details
	Future bool   `json:"future,omitempty"` // illustrates a field the protocol does not have
}

// Note is text written on this computer about a message, never by the peer.
type Note struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// File is an attachment as the sender named it.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Draft is a message to send.
type Draft struct {
	Peer    string `json:"peer"`
	Kind    string `json:"kind"` // message, question or task
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
	Files   []File `json:"files,omitempty"`
}

// Action is a local decision.
type Action struct {
	ID     string `json:"id"`     // message id, or peer address for trust
	Do     string `json:"do"`     // accept, decline, approve, resolve, trust
	Reason string `json:"reason"` // decline only
}

// Errors a Provider returns for requests the page should explain. Wrap
// them with Refuse or NotFound so the person sees a useful sentence.
var (
	ErrNotFound = errors.New("not found")
	ErrRefused  = errors.New("refused")
)

type explained struct {
	kind error
	text string
}

func (e explained) Error() string        { return e.text }
func (e explained) Is(target error) bool { return target == e.kind }

// Refuse is an ErrRefused whose message is text.
func Refuse(text string) error { return explained{ErrRefused, text} }

// NotFound is an ErrNotFound whose message is text.
func NotFound(text string) error { return explained{ErrNotFound, text} }

// Kinds and stored states the page understands.
const (
	KindMessage  = "message"
	KindQuestion = "question"
	KindAnswer   = "answer"
	KindTask     = "task"
	KindResult   = "result"
	KindSystem   = "system"
)

// StateText is the plain-language line for a stored state. It says only
// what the state proves: delivered is not read, answered is not correct.
func StateText(dir, kind, state, peer string) string {
	if dir == "out" {
		switch state {
		case "queued":
			return "Waiting to send; retries automatically"
		case "custody":
			return "Held by the Hub until " + peer + " connects"
		case "delivered":
			return "Delivered to " + peer
		case "expired":
			return "Not delivered: the session it was sent to ended"
		case "failed":
			return "Not sent"
		}
		return ""
	}
	if dir != "in" || (kind != KindQuestion && kind != KindTask) {
		return ""
	}
	switch state {
	case "pending", "accepted":
		return "Waiting for your responder"
	case "held":
		return "Needs you: " + peer + " is not approved for automatic answers"
	case "awaiting":
		return "Needs you: tasks run only if you accept them"
	case "running":
		return "Your responder is working on this"
	case "cancel_requested":
		return "Stopping your responder"
	case "answered":
		if kind == KindTask {
			return "Your responder sent the result"
		}
		return "Your responder answered"
	case "manual":
		return "Replied by hand"
	case "declined":
		return "Declined"
	case "failed":
		return "Your responder failed"
	case "cancelled":
		return "Cancelled"
	case "interrupted":
		return "Interrupted"
	case "summarized":
		return "Summarized on this computer"
	case "needs_human":
		return "Needs you: your responder asked for a person"
	case "resolved":
		return "Closed by you"
	}
	return ""
}

// Next says who owes the next move for one message, or "" when nothing is
// open. It is derived from stored states only.
func Next(dir, kind, state, peer string, answered bool) string {
	if dir == "in" && (kind == KindQuestion || kind == KindTask) {
		switch state {
		case "held", "awaiting", "needs_human":
			return "you"
		case "pending", "accepted", "running", "cancel_requested":
			return "your responder"
		}
		return ""
	}
	if dir == "out" && (kind == KindQuestion || kind == KindTask) && !answered {
		switch state {
		case "queued", "custody", "delivered":
			return peer
		}
	}
	return ""
}

// excerpt is the first line of s, shortened.
func excerpt(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 90 {
		s = string(r[:89]) + "…"
	}
	return s
}
