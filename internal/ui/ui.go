// Package ui serves the local messenger: a conversation-first web page on a
// loopback address, backed by a Provider.
//
// The daemon hosts it over the installation's real data (`agentnet daemon
// --ui`, live.go): threads, sends and decisions go through the client's
// existing operations and rules. `agentnet ui --demo` serves the same page
// over invented data (fixture.go) for trying it out and for tests.
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
	// Overview is this computer, its threads and what waits for the person.
	Overview() (Overview, error)
	// Thread is every message of the thread that contains message id,
	// oldest first.
	Thread(id string) (Thread, error)
	// Send sends a new message, question or task (optionally linked to an
	// earlier message of the same thread).
	Send(d Draft) (Sent, error)
	// Act applies a local decision; the result is a short note for the
	// person.
	Act(a Action) (string, error)
	// Changed returns the current change counter and a channel that is
	// closed at the next change.
	Changed() (uint64, <-chan struct{})
}

// Refresher is implemented by providers that can ask the network about a
// thread once when it is opened: the peer's presence and the receipts of
// messages still in the Hub's custody. It is one bounded request per open,
// never a poll.
type Refresher interface {
	Refresh(threadID string) (Presence, error)
}

// Simulator is implemented only by demo providers: controls that stand in
// for things that happen elsewhere (a peer writing, a responder finishing).
type Simulator interface {
	Simulate(what string) error
}

// Overview is what the page shows around the open thread.
type Overview struct {
	Demo       bool             `json:"demo"`
	Me         Me               `json:"me"`
	Threads    []ThreadSummary  `json:"threads"`
	Review     []ReviewItem     `json:"review"`
	Quarantine []QuarantineItem `json:"quarantine"`
	Release    string           `json:"release,omitempty"` // a recommended build other than this one
	Seq        uint64           `json:"seq"`
	Version    string           `json:"version"` // the program serving the page (an update changes it)
	Directory  Directory        `json:"directory"`
	// Human DMs, when the provider holds them (Persons): this installation's
	// person (nil until the person creates one), the people known or listed,
	// and the two-person conversations. They are never part of Threads.
	Persons bool         `json:"persons"`
	Agents  bool         `json:"agents"` // agents can be invited into DMs here (Participants)
	Person  *PersonView  `json:"person,omitempty"`
	People  []PersonView `json:"people"`
	DMs     []DMSummary  `json:"dms"`
}

// Persons is implemented by providers that hold human DMs.
type Persons interface {
	// CreatePerson creates this installation's person, once, as the person
	// asked; the note says what is not done yet (the server may not hold it).
	CreatePerson(label string) (PersonView, string, error)
	DM(id string) (DMThread, error)
	// NewDM starts a separate conversation with the person on the device at
	// address; nothing is sent until a message is.
	NewDM(address string) (string, error)
	SendDM(d DMDraft) (Sent, error)
}

// Person states the page shows.
const (
	PersonSelf     = "self"     // created on this installation
	PersonPinned   = "pinned"   // checked against their device's key and kept here
	PersonConflict = "conflict" // a different record was seen: frozen
	PersonListed   = "listed"   // the server lists a record; not checked here yet
)

// PersonView is a person as the page shows them. The label is the person's
// own claim, never a checked name; the person id and the device say who it
// is here. A listed person has no id yet: it is known once it is checked.
type PersonView struct {
	Person      string `json:"person,omitempty"`
	Label       string `json:"label"`
	Address     string `json:"address"` // the one device the person speaks through
	Fingerprint string `json:"fingerprint,omitempty"`
	State       string `json:"state"`
	Published   bool   `json:"published,omitempty"` // own person: the server holds it, as far as this installation knows
}

// DMSummary is one two-person conversation in the sidebar.
type DMSummary struct {
	ID      string     `json:"id"`
	Peer    PersonView `json:"peer"`
	Created time.Time  `json:"created"` // the creator's claim
	Mine    bool       `json:"mine"`    // started on this installation
	Count   int        `json:"count"`
	Title   string     `json:"title"` // first line of the first message
	Last    string     `json:"last"`  // first line of the latest message
	LastAt  time.Time  `json:"last_at"`
	Unread  int        `json:"unread"`
	Held    int        `json:"held"`    // their questions or tasks held for the person; nothing runs them
	Waiting int        `json:"waiting"` // messages kept here because they cannot read conversations now
}

// DMThread is one conversation's messages, oldest first.
type DMThread struct {
	ID       string      `json:"id"`
	Peer     PersonView  `json:"peer"`
	Created  time.Time   `json:"created"`
	Mine     bool        `json:"mine"`
	Frozen   string      `json:"frozen,omitempty"` // why nothing can be sent in it
	Messages []DMMessage `json:"messages"`
	Agents   []AgentView `json:"agents"` // agents invited into it, oldest first
}

// DMMessage is one message of a DM.
type DMMessage struct {
	ID        string    `json:"id"`
	Dir       string    `json:"dir"` // in or out
	From      string    `json:"from"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	Origin    string    `json:"origin,omitempty"` // what the sending device says wrote it, not proof
	State     string    `json:"state"`
	StateText string    `json:"state_text"`
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at"`
	Unread    bool      `json:"unread,omitempty"`
	Replica   bool      `json:"replica,omitempty"`
	PID       string    `json:"pid,omitempty"`   // the agent participation it is for, from or about
	To        string    `json:"to,omitempty"`    // a request's one target: the device whose agent is asked
	Event     string    `json:"event,omitempty"` // a participation record, said in words (its body is the record)
}

// Participants is implemented by providers where agents can be invited
// into DMs. Nothing here runs an agent: the host's person decides, and
// what is asked of an agent is held for that person.
type Participants interface {
	InviteAgent(d AgentInvite) (AgentView, error)
	// DecideAgent accepts or declines an invite for this installation's agent.
	DecideAgent(pid string, accept bool) (AgentView, error)
	DismissAgent(pid string) (AgentView, error)
	AskAgent(d AgentAsk) (Sent, error)
}

// AgentInvite invites the agent on a member's device (Host, an address)
// into a DM: the earlier messages it may be shown (Share, message ids of
// that DM, each exactly) and the member keys that may give it follow-up
// tasks (TasksFrom, fingerprints).
type AgentInvite struct {
	Conv      string   `json:"conv"`
	Host      string   `json:"host"`
	Share     []string `json:"share"`
	TasksFrom []string `json:"tasks_from"`
	Note      string   `json:"note"`
}

// AgentAsk is a question (or task) for an active participation's agent.
type AgentAsk struct {
	PID  string `json:"pid"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// AgentView is an agent invited into a DM, as the page shows it: whose
// installation runs it, who invited it, what it may be shown and who may
// give it tasks. The host's person decides; either person can end it.
type AgentView struct {
	PID        string       `json:"pid"`
	State      string       `json:"state"` // pending, invited, active, declined, conflict, dismissed
	StateText  string       `json:"state_text"`
	Host       PersonView   `json:"host"`
	HostHere   bool         `json:"host_here"` // this installation runs it
	Inviter    PersonView   `json:"inviter"`
	Note       string       `json:"note,omitempty"`
	Shared     []string     `json:"shared"`  // ids of the shared earlier messages held here
	Missing    int          `json:"missing"` // shared messages not held here
	TasksFrom  []PersonView `json:"tasks_from"`
	Held       int          `json:"held"` // its records not counted here (yet)
	Invited    time.Time    `json:"invited"`
	CanDecide  bool         `json:"can_decide"`
	CanDismiss bool         `json:"can_dismiss"`
	CanAsk     bool         `json:"can_ask"`
}

// DMDraft is a message the person writes in a DM. The page sends messages
// only: a DM's question or task would run nowhere yet.
type DMDraft struct {
	Conv    string `json:"conv"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// DMStateText is what the page says about a DM message's state.
func DMStateText(dir, kind, state, peer, detail string) string {
	if dir == "out" {
		if state == "waiting" {
			if detail == "" {
				detail = peer + " cannot read conversations now"
			}
			return "Kept here, not sent yet: " + detail
		}
		return StateText("out", kind, state, peer)
	}
	switch state {
	case "conv_held":
		return "Held for you: nothing runs it. Answer here if you want to."
	case "part_waiting": // a request to this device's agent, not claimed yet
		return "For your agent. It has not run yet: it runs here only with a responder chosen on this computer, while the invitation allows it."
	case "not_run":
		return "Not run: the agent's part in this DM ended first."
	case "not_delivered":
		return "Your agent's reply was kept here: the invitation no longer allowed sending it."
	case "":
		return ""
	}
	return StateText("in", kind, state, peer)
}

// Me describes this installation.
type Me struct {
	Address      string `json:"address"`
	Fingerprint  string `json:"fingerprint"`
	Responder    string `json:"responder"`     // harness, or "" when questions and tasks wait for the person
	ResponderDir string `json:"responder_dir"` // where the responder runs
}

// ThreadSummary is one row of the thread list.
type ThreadSummary struct {
	ID         string    `json:"id"`
	Peer       string    `json:"peer"`
	Title      string    `json:"title"`
	Last       string    `json:"last"`
	LastAt     time.Time `json:"last_at"`
	Count      int       `json:"count"`
	Review     int       `json:"review"` // decisions here; review notices not counted
	Unread     int       `json:"unread"` // review notices not counted
	Running    int       `json:"running"`
	Waiting    bool      `json:"waiting"`
	KeyChanged bool      `json:"key_changed"`
	Notices    int       `json:"notices"`     // open review notices (reports from another machine)
	NoticeOnly bool      `json:"notice_only"` // the thread is only review notices: not a conversation
}

// Directory is who else the server (Hub) lists as enrolled, for finding
// someone new. Being listed trusts, approves or contacts no one, and the
// person label in an address is the name an admin gave the invite, not a
// verified identity.
type Directory struct {
	// Status is DirectoryUnknown (not connected to the server since the
	// daemon started), DirectoryListed or DirectoryNotListed (an older
	// server that does not list its members).
	Status string `json:"status"`
	// Current: the presence below is the server's view now. Otherwise the
	// list is as of At and every presence is unknown ("").
	Current   bool        `json:"current"`
	At        time.Time   `json:"at,omitzero"`
	Truncated bool        `json:"truncated"` // more agents are enrolled than listed
	Members   []DirMember `json:"members"`   // newest first, this installation left out
}

// Directory statuses.
const (
	DirectoryUnknown   = "unknown"
	DirectoryListed    = "listed"
	DirectoryNotListed = "not_listed"
)

// DirMember is one listed agent. Presence is "connected", "reconnecting",
// "offline", or "" when not current: whether the server sees that agent's
// daemon, never whether a person is there.
type DirMember struct {
	Address  string    `json:"address"`
	Presence string    `json:"presence"`
	Joined   time.Time `json:"joined"`
}

// ReviewItem is a received item waiting for the person. Notice marks a
// review notice: another machine reported that requests wait for a person
// there. It is not a decision here and carries no request.
type ReviewItem struct {
	ID      string    `json:"id"`
	Peer    string    `json:"peer"`
	Kind    string    `json:"kind"`
	Why     string    `json:"why"`
	Excerpt string    `json:"excerpt"`
	At      time.Time `json:"at"`
	Notice  bool      `json:"notice,omitempty"`
}

// StatusReviewNotice is the status of a review notice (a plain message).
const StatusReviewNotice = "review_notice"

// IsReviewNotice reports whether a received message is exactly a review
// notice, the shape the client files as one: a plain message with that
// status, no reply link and no files. Anything else is a normal message.
func IsReviewNotice(kind, status, replyTo string, files int) bool {
	return kind == KindMessage && status == StatusReviewNotice && replyTo == "" && files == 0
}

// QuarantineItem is a received envelope held back; its content is not shown.
type QuarantineItem struct {
	ID     string    `json:"id"`
	Peer   string    `json:"peer"`
	Reason string    `json:"reason"` // plain text
	At     time.Time `json:"at"`
}

// Thread is one conversation with one peer.
type Thread struct {
	ID        string    `json:"id"`
	Peer      string    `json:"peer"`
	Key       PeerKey   `json:"key"`
	Approved  bool      `json:"approved"`   // questions from this peer are answered automatically
	TaskGrant string    `json:"task_grant"` // "" none, "active", or why a grant does not hold
	Messages  []Message `json:"messages"`
}

// PeerKey is what this installation knows about the peer's key.
type PeerKey struct {
	Pinned  string `json:"pinned,omitempty"`
	Pending string `json:"pending,omitempty"` // a changed key waiting for trust; sending is blocked
}

// Presence is what the Hub said about the peer's running daemons when
// asked once, and when: an observation, not a live state. It describes
// computers, never whether a person is there.
type Presence struct {
	Text string    `json:"text"`
	At   time.Time `json:"at,omitzero"` // when the Hub answered; zero when it did not
}

// Message is one row of a thread.
type Message struct {
	ID        string    `json:"id"`
	Dir       string    `json:"dir"` // in or out
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	At        time.Time `json:"at"`
	State     string    `json:"state,omitempty"`  // stored state, shown under details
	Status    string    `json:"status,omitempty"` // outcome carried by an answer or result
	Path      string    `json:"path,omitempty"`   // relay or direct
	Responder string    `json:"responder,omitempty"`
	Summary   string    `json:"summary,omitempty"` // follow-up summary written locally
	Detail    string    `json:"detail,omitempty"`  // local note: failure or needs-human reason
	Unread    bool      `json:"unread,omitempty"`
	Files     []File    `json:"files,omitempty"`
	Author    Author    `json:"author"`
	StateText string    `json:"state_text,omitempty"`
	Next      string    `json:"next,omitempty"`
	Actions   []string  `json:"actions,omitempty"` // decisions available on this message
}

// Author says who wrote a message, only as far as it is recorded.
type Author struct {
	Label string `json:"label"`
	About string `json:"about"`
}

// File is an attachment as the sender named it.
type File struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Saved string `json:"saved,omitempty"` // where it was downloaded, if it was
}

// Draft is a message to send.
type Draft struct {
	To      string `json:"to"`
	Kind    string `json:"kind"` // message, question or task
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// Sent is the outcome of Send.
type Sent struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Action is a local decision.
type Action struct {
	Do     string   `json:"do"`
	ID     string   `json:"id,omitempty"`     // message id, or peer address for peer actions
	Body   string   `json:"body,omitempty"`   // reply text
	Reason string   `json:"reason,omitempty"` // decline reason
	IDs    []string `json:"ids,omitempty"`    // read: messages to mark read
	Key    string   `json:"key,omitempty"`    // trust: the fingerprint the person compared
}

// Actions the page can request.
const (
	DoReply        = "reply"         // answer a received item by hand (takes it over)
	DoAccept       = "accept"        // run a task once, let the responder answer a held question, or run again
	DoAcceptAlways = "accept_always" // run this task and let later tasks from this exact key run
	DoDecline      = "decline"
	DoResolve      = "resolve" // close a needs-human item without sending anything
	DoCancel       = "cancel"
	DoApprove      = "approve"   // ID = peer: answer its questions automatically
	DoUnapprove    = "unapprove" // ID = peer
	DoTrust        = "trust"     // ID = peer: trust its changed key
	DoRevokeTasks  = "revoke_tasks"
	DoRead         = "read"
)

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

// Kinds the page understands.
const (
	KindMessage  = "message"
	KindQuestion = "question"
	KindAnswer   = "answer"
	KindTask     = "task"
	KindResult   = "result"
)

// StateText is the plain-language line for a stored state. It says only
// what the state proves: delivered is not read, answered is not correct.
func StateText(dir, kind, state, peer string) string {
	if dir == "out" {
		switch state {
		case "queued":
			return "Waiting to send; retries automatically"
		case "custody":
			return "Waiting on the server until " + peer + " connects"
		case "delivered":
			return "Delivered to " + peer
		case "expired":
			return "Not delivered: that session ended first"
		case "failed":
			return "Not sent"
		case "quarantined":
			return peer + " could not verify it"
		}
		return ""
	}
	if dir != "in" {
		return ""
	}
	if kind != KindQuestion && kind != KindTask {
		return otherText(kind, state)
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
		return "Interrupted: the daemon stopped while this ran"
	case "summarized":
		return "Summarized on this computer"
	case "needs_human":
		return "Needs you: your responder asked for a person"
	case "resolved":
		return "Closed by you"
	}
	return ""
}

// otherText covers received messages, answers and results: a review notice
// (a message that only asks for attention) and replies your responder
// follows up on. None of them can be accepted or run from here.
func otherText(kind, state string) string {
	switch state {
	case "needs_human":
		if kind == KindMessage { // a review notice: a report, not a decision here
			return "Reported: requests wait for a person on that machine; nothing here can approve them"
		}
		return "Needs you: your responder's follow-up asked for a person"
	case "pending", "accepted":
		return "Waiting for your responder's follow-up"
	case "running":
		return "Your responder is following up on this"
	case "cancel_requested":
		return "Stopping your responder"
	case "summarized":
		return "Summarized on this computer"
	case "failed":
		return "Your responder's follow-up failed"
	case "cancelled":
		return "Cancelled"
	case "interrupted":
		return "Interrupted: the daemon stopped while this ran"
	case "resolved":
		return "Closed by you"
	}
	return ""
}

// Next says who owes the next move for one message, or "" when nothing is
// open. It is derived from stored states only.
func Next(dir, kind, state, peer string, answered bool) string {
	if dir == "in" {
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

// ActionsFor lists the decisions available on a received item in state,
// following the client's rules (respond.go, taskgrant.go). A message,
// answer or result that needs the person can only be closed here: nothing
// received that way is accepted or run.
func ActionsFor(kind, state string) []string {
	if kind != KindQuestion && kind != KindTask {
		if state == "needs_human" {
			return []string{DoResolve}
		}
		return nil
	}
	switch state {
	case "held":
		return []string{DoReply, DoAccept, DoApprove, DoDecline}
	case "awaiting":
		return []string{DoAccept, DoAcceptAlways, DoReply, DoDecline}
	case "needs_human":
		return []string{DoReply, DoAccept, DoResolve}
	case "running":
		return []string{DoCancel}
	case "interrupted", "failed", "cancelled":
		return []string{DoAccept, DoReply}
	}
	return nil
}

// ReviewWhy is the short reason an item waits for the person: the
// responder's own explanation when there is one, else its state.
func ReviewWhy(kind, state, peer, detail string) string {
	if detail != "" {
		return detail
	}
	why := strings.TrimPrefix(StateText("in", kind, state, peer), "Needs you: ")
	if strings.HasPrefix(why, peer) {
		return why // an address keeps its case
	}
	return capitalize(why)
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
