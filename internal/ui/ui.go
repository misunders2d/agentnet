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
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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
	Groups           bool                  `json:"groups"` // explicit human group operations; never agent execution
	GroupInvitations []GroupInvitationView `json:"group_invitations,omitempty"`
	ReplyReceivers   bool                  `json:"reply_receivers"` // native local continuation selection/read-only state
	ReplySessions    bool                  `json:"reply_sessions"`  // safe native registration catalog, not process liveness
	Demo             bool                  `json:"demo"`
	// App says the page is the AgentNet app's window (cmd/agentnet app.go):
	// the app updates as a whole, and the page offers its new version
	// rather than a terminal command.
	App bool `json:"app,omitempty"`
	Me  Me   `json:"me"`
	// Threads are the device threads, each with its topic state. A page
	// that lists topics itself asks GET /api/overview?topics=1 and gets
	// them without archived topics: those are counted per peer in Topics
	// and listed through GET /api/topics (TopicList says that route is
	// served here). Without the flag every thread is listed, archived
	// topics too, as a page that knows nothing of topics needs.
	Threads    []ThreadSummary  `json:"threads"`
	Topics     []PeerTopics     `json:"topics,omitempty"`
	TopicList  bool             `json:"topic_list"`
	Review     []ReviewItem     `json:"review"`
	Quarantine []QuarantineItem `json:"quarantine"`
	Release    string           `json:"release,omitempty"` // a recommended build other than this one
	Seq        uint64           `json:"seq"`
	Version    string           `json:"version"` // the program serving the page (an update changes it)
	Directory  Directory        `json:"directory"`
	// NeedsYou are the conversation items waiting for the person's decision
	// (requests to the agent here, invitations for it); Held are the person
	// turns held in conversations, answered there. Neither is in Review,
	// which is device history.
	NeedsYou []ConvItem `json:"needs_you"`
	Held     []ConvItem `json:"held"`
	// Human DMs, when the provider holds them (Persons): this installation's
	// person (nil until the person creates one), the people known or listed,
	// and the two-person conversations. They are never part of Threads.
	Persons bool        `json:"persons"`
	Agents  bool        `json:"agents"`           // agents can be invited into DMs here (Participants)
	Notify  *NotifyView `json:"notify,omitempty"` // optional DM alerts, where the provider has them (Alerts)
	// Reminders are the pending "remind me later" reminders, soonest
	// first (Reminders providers only; absent elsewhere).
	Reminders []ReminderView `json:"reminders,omitempty"`
	Remind    bool           `json:"remind"`          // reminders can be set here
	Files     *FileLimits    `json:"files,omitempty"` // files can be sent in DMs here (Files providers)
	Person    *PersonView    `json:"person,omitempty"`
	People    []PersonView   `json:"people"`
	DMs       []DMSummary    `json:"dms"`
	// One person on several devices (Identity providers, MEL-433): what
	// this installation is (RoleUnset, RolePerson, RoleService), this
	// device's own request to join its person while it is not done, new
	// devices asking to join this person (approved here, by a person), and
	// the chats being copied to newly added devices.
	Role    string        `json:"role,omitempty"`
	Link    *LinkState    `json:"link,omitempty"`
	Links   []LinkRequest `json:"links,omitempty"`
	History []HistoryCopy `json:"history,omitempty"`

	// The workspace as people see it (MEL-524): the name its admin set and
	// the relay's host name, the fallback when none is set.
	Workspace *WorkspaceView `json:"workspace,omitempty"`
	// AgentDevices are the other devices that, as last listed, say they run
	// an agent (MEL-529): the only "runs an agent" signal for skins, kept
	// offline. A hint for display and offers, never authority.
	AgentDevices []string `json:"agent_devices"`
}

// WorkspaceView is the workspace's own name ("" when its admin set none)
// and its relay's host name.
type WorkspaceView struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// WorkspaceInfoView answers GET /api/workspace: the workspace view and
// whether this device may rename it for everyone (it is a Hub admin, or a
// device of a person who is; unknown counts as no).
type WorkspaceInfoView struct {
	WorkspaceView
	CanRename bool `json:"can_rename"`
}

// WorkspaceNameChange is POST /api/workspace/name: the name for everyone;
// an empty name clears it.
type WorkspaceNameChange struct {
	Name string `json:"name"`
}

// Roles an installation has (Overview.Role).
const (
	RoleUnset   = "unset"   // nothing chosen yet: a person, or a service
	RolePerson  = "person"  // a person's device
	RoleService = "service" // a service or bot: no person, set up by invitation
)

// Identity is implemented by providers where one person has several
// devices (MEL-433). A device of a person offers a one-scan link that
// lets a new device join as the same person; it becomes that person's only
// once a person approves it here. A service never has a person.
type Identity interface {
	// SetService records that this installation is a service, not a person.
	SetService() (string, error)
	// NewDeviceLink makes a one-use link (a QR and the text of it) that a
	// new device of this person joins with; it expires.
	NewDeviceLink() (DeviceLink, error)
	// DecideLink approves or refuses a new device asking to join.
	DecideLink(id string, accept bool) (string, error)
	// RemoveDevice takes a device off this person (never the last one).
	RemoveDevice(address string) (string, error)
}

// DeviceLink is a one-use link for a new device of this person: URL opens
// the browser page on this server and joins there; the same text can be
// given to the command line.
type DeviceLink struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
	// AppURL opens the same link in the AgentNet app on this computer
	// (agentnet://open#code): a browser hands the link to the app it
	// offers to install, so the app joins as this person.
	AppURL string `json:"app_url,omitempty"`
}

// Invitations (MEL-533): an admin's device makes one invitation link per
// new person, sees the unused ones and withdraws them. Live and the
// browser engine serve the same routes (/api/invite, /api/invites,
// /api/invite/revoke).
type Invitations interface {
	// Invite makes an invitation for a new person.
	Invite(r InviteRequest) (InviteView, error)
	// Invites says whether this device may invite people and lists the
	// unused invitations (admins only).
	Invites() (InvitesView, error)
	// RevokeInvite withdraws an unused invitation by its id.
	RevokeInvite(id string) error
}

// InviteRequest is the Invite people form: the person's name, whether they
// become an admin, and how many days the link works (1, 7 or 30).
type InviteRequest struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	Days  int    `json:"days"`
}

// InviteDays are the lifetimes an invitation can be given, in days.
var InviteDays = []int{1, 7, 30}

// InviteView is an invitation made: the link to share, the label the
// person gets, when it stops working and a message ready to send.
type InviteView struct {
	Link    string    `json:"link"`
	Label   string    `json:"label"`
	Expires time.Time `json:"expires"`
	Message string    `json:"message"`
}

// InvitesView answers GET /api/invites.
type InvitesView struct {
	CanInvite bool                `json:"can_invite"`
	Invites   []PendingInviteView `json:"invites"`
}

// PendingInviteView is one unused invitation.
type PendingInviteView struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Label   string    `json:"label"`
	Admin   bool      `json:"admin"`
	By      string    `json:"by"`
	Created time.Time `json:"created,omitzero"` // zero (left out) for invitations made before the relay kept it
	Expires time.Time `json:"expires"`
}

// GetAppView answers GET /api/get-app: where to download the AgentNet app
// (static/getapp.json, one table for Go and the page) for this program's
// version, and the platform this page runs on, if known.
type GetAppView struct {
	Version   string           `json:"version"`
	Detected  string           `json:"detected,omitempty"`
	Platforms []GetAppPlatform `json:"platforms"`
}

// GetAppPlatform is one download of the app.
type GetAppPlatform struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Folders is implemented where the page runs on the computer whose folders
// it lists (the daemon, never a browser): the folder picker of Connect an
// agent. Read-only.
type Folders interface {
	Folders(path string) (FoldersView, error)
}

// FoldersView is one folder's subfolders (hidden ones left out, at most
// MaxFolders), with its parent and the person's home; Roots are the
// drives on Windows.
type FoldersView struct {
	Path      string       `json:"path"`
	Parent    string       `json:"parent,omitempty"`
	Home      string       `json:"home"`
	Roots     []string     `json:"roots,omitempty"`
	Dirs      []FolderView `json:"dirs"`
	Truncated bool         `json:"truncated,omitempty"`
}

// FolderView is one subfolder.
type FolderView struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// MaxFolders bounds the subfolders one listing shows.
const MaxFolders = 500

// DeviceView is one device of a person.
type DeviceView struct {
	Address     string `json:"address"`
	Name        string `json:"name"` // the device's own name, as its address shows it
	Fingerprint string `json:"fingerprint"`
	This        bool   `json:"this,omitempty"`  // this installation
	Human       bool   `json:"human,omitempty"` // may enroll devices; false for an agent host
}

// LinkRequest is a new device asking to join this person, until decided.
type LinkRequest struct {
	ID          string    `json:"id"`
	Address     string    `json:"address"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	RequestedAt time.Time `json:"requested_at"`
	Expires     time.Time `json:"expires"`
	State       string    `json:"state"` // pending, or why it ended
}

// LinkState is this device's own request to join its person: pending,
// refused, expired, stale or failed (linked is shown as the person).
type LinkState struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// HistoryCopy is the copying of this person's chats to a new device.
type HistoryCopy struct {
	Device string `json:"device"` // its address
	Name   string `json:"name"`
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	State  string `json:"state"` // running, done, or ended (that device is no longer yours)
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
	Email       string `json:"email,omitempty"`
	Address     string `json:"address"` // the one device the person speaks through
	Fingerprint string `json:"fingerprint,omitempty"`
	State       string `json:"state"`
	Published   bool   `json:"published,omitempty"` // own person: the server holds it, as far as this installation knows
	// Agents are the agents this person's installation runs in DMs here:
	// each is linked to the person by the invitation's host (their person
	// and their device), never by a name or an address alone.
	Agents []AgentLink `json:"agents,omitempty"`
	// Devices are the person's devices, as their signed record names them
	// (Identity providers); one person, one row, however many devices.
	Devices []DeviceView `json:"devices,omitempty"`
}

// AgentLink is one person's agent, on their device, and the DMs it was
// invited into here.
type AgentLink struct {
	AgentID string      `json:"agent_id,omitempty"`
	Address string      `json:"address"` // the device that runs it
	DMs     []AgentInDM `json:"dms"`
}

// AgentInDM is an agent's participation in one DM.
type AgentInDM struct {
	Conv  string `json:"conv"`
	PID   string `json:"pid"`
	State string `json:"state"`
}

// DMSummary is one two-person conversation in the sidebar.
type DMSummary struct {
	Kind    string            `json:"kind,omitempty"`
	Members []GroupMemberView `json:"members,omitempty"`
	Frozen  string            `json:"frozen,omitempty"`
	Role    string            `json:"role,omitempty"` // member or invited visitor, computed from native proof
	ID      string            `json:"id"`
	Peer    PersonView        `json:"peer"`
	Created time.Time         `json:"created"` // the creator's claim
	Mine    bool              `json:"mine"`    // started on this installation
	Count   int               `json:"count"`
	Title   string            `json:"title"` // first line of the first message
	Last    string            `json:"last"`  // first line of the latest message
	LastAt  time.Time         `json:"last_at"`
	Unread  int               `json:"unread"`
	Held    int               `json:"held"`    // their questions or tasks held for the person; nothing runs them
	Waiting int               `json:"waiting"` // messages kept here because they cannot read conversations now
	// Guests are the participations active in it now: people and agents
	// brought in to help (GuestView, AgentView). Decide counts its requests
	// waiting for this device's person's decision here (Overview.NeedsYou
	// items with actions; none on a device that runs no agent). LastEvent
	// is set when its latest message is a participation record.
	Guests    int        `json:"guests"`
	Decide    int        `json:"decide"`
	LastEvent *LastEvent `json:"last_event,omitempty"`
}

// LastEvent is a participation record as a chat list shows it: what
// happened (invite, accept, decline or dismiss; an invitation's public
// scope counts as its invite), to which participation, and who did it (the
// author's person label as known here, or their device's address). It is
// the record's own word, plainly; DMMessage.Event says it in a sentence.
type LastEvent struct {
	Kind string `json:"kind"`
	PID  string `json:"pid"`
	By   string `json:"by"`
}

// DMThread is one conversation's messages, oldest first.
type DMThread struct {
	Topics          []ThreadSummary   `json:"topics,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	Title           string            `json:"title,omitempty"`
	Members         []GroupMemberView `json:"members,omitempty"`
	Role            string            `json:"role,omitempty"` // visitor context confers no ordinary room actions
	ID              string            `json:"id"`
	Peer            PersonView        `json:"peer"`
	Created         time.Time         `json:"created"`
	Mine            bool              `json:"mine"`
	Frozen          string            `json:"frozen,omitempty"` // why nothing can be sent in it
	Messages        []DMMessage       `json:"messages"`
	Agents          []AgentView       `json:"agents"` // agents invited into it, oldest first
	Guests          []GuestView       `json:"guests,omitempty"`
	AudiencePending bool              `json:"audience_pending,omitempty"`
}

// DMMessage is one message of a DM.
type DMMessage struct {
	Status         string                    `json:"status,omitempty"`
	Topic          string                    `json:"topic,omitempty"`
	TopicEvent     *envelope.TopicEvent      `json:"topic_event,omitempty"`
	AgentAuthorPID string                    `json:"agent_author_pid,omitempty"`
	GroupRef       *protocol.GroupHistoryRef `json:"group_ref,omitempty"`   // exact selectable frozen content, supplied by group history selection
	ExcerptPID     string                    `json:"excerpt_pid,omitempty"` // grant scope; PID retains original snapshot PID
	ClaimedKey     string                    `json:"claimed_key,omitempty"` // forwarded authorship, never verified here
	Target         *envelope.Target          `json:"target,omitempty"`
	AgentID        string                    `json:"agent_id,omitempty"`
	ID             string                    `json:"id"`
	LID            string                    `json:"lid,omitempty"`
	Dir            string                    `json:"dir"` // in or out
	From           string                    `json:"from"`
	Kind           string                    `json:"kind"`
	Body           string                    `json:"body"`
	ReplyTo        string                    `json:"reply_to,omitempty"`
	Origin         string                    `json:"origin,omitempty"` // what the sending device says wrote it, not proof
	State          string                    `json:"state"`
	StateText      string                    `json:"state_text"`
	Detail         string                    `json:"detail,omitempty"`
	At             time.Time                 `json:"at"`
	Unread         bool                      `json:"unread,omitempty"`
	Replica        bool                      `json:"replica,omitempty"`
	PID            string                    `json:"pid,omitempty"` // the agent participation it is for, from or about
	Attachments    []FileView                `json:"attachments,omitempty"`

	// VerifiedAgent: an agent's turn as sent, by its participation's exact
	// host key (client.ConvMessage.VerifiedAgent). UIs should label an
	// agent's turn only from this, never from Origin or AgentID. It speaks
	// for the turn as sent (Body): an edit, which any device of the host's
	// person may make, shows as Edited, and its Text is not the host key's.
	VerifiedAgent bool `json:"verified_agent"`

	// Sent by you: Via is the device of yours it was sent from when that is
	// not this one; Copies are this device's copies, one per device it went
	// to (the other person's and your own), with how far each got.
	Via    string     `json:"via,omitempty"`
	Copies []CopyView `json:"copies,omitempty"`
	// SyncedFrom is the device of yours this message came from as history
	// (copied when this device was added): who sent it is that device's
	// word, not checked here, and nothing runs it.
	SyncedFrom string           `json:"synced_from,omitempty"`
	To         string           `json:"to,omitempty"`    // a request's one target: the device whose agent is asked
	Event      string           `json:"event,omitempty"` // a participation record, said in words (its body is the record)
	Controls                    // reactions, edit and deletion applied to it (client.Controls, flattened)
	Exec       *client.ExecView `json:"exec,omitempty"` // a request: where its executing device last said it stands
	// A request to this device's agent: what its person can do with it
	// here (accept, cancel, resolve), and what the run left to say.
	Actions   []string             `json:"actions,omitempty"`
	JobDetail string               `json:"job_detail,omitempty"`
	Proposal  *client.ProposalView `json:"proposal,omitempty"`
	// A participation record's type (invite, accept, decline, dismiss or
	// scope) and its author (their person's label as known here, or their
	// device's address), plainly, so a page never parses Event.
	EventType string    `json:"event_type,omitempty"`
	EventBy   string    `json:"event_by,omitempty"`
	Quote     string    `json:"quote,omitempty"`
	SentAt    time.Time `json:"sent_at,omitzero"`
	Delivery  string    `json:"delivery,omitempty"`
}

// AgentActions are the decisions this device's person can take on a
// request to its agent in state: run a task that needs their accept (or
// run one again) or decline it (the requester is told, nothing runs it),
// stop a run, or close what the agent handed back or what a stopped daemon
// left interrupted (client.Resolve).
func AgentActions(kind, state string) []string {
	switch state {
	case "awaiting": // a task, or a guest's question, waiting for this host's one-time acceptance
		if kind == KindTask || kind == KindQuestion {
			return []string{DoAccept, DoDecline}
		}
	case "running":
		return []string{DoCancel}
	case "needs_human":
		return []string{DoAccept, DoResolve}
	case "interrupted":
		if kind == KindTask || kind == KindQuestion {
			return []string{DoAccept, DoResolve}
		}
		return []string{DoAccept}
	case "failed", "cancelled":
		return []string{DoAccept}
	}
	return nil
}

// FileLimits are what one DM message may carry here.
type FileLimits struct {
	MaxFile    int64 `json:"max_file"`              // bytes per file
	MaxMessage int64 `json:"max_message,omitempty"` // bytes per message (the browser device only)
	MaxCount   int   `json:"max_count"`
}

// CopyView is one device's copy of a message you sent.
type CopyView struct {
	Own    bool   `json:"own,omitempty"`
	Person string `json:"person,omitempty"`
	To     string `json:"to"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// FileView is one file of a message as the page lists it: its name made
// safe, its size, where it was saved (received files saved from the
// command line).
type FileView struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Saved string `json:"saved,omitempty"`
	// Availability is where a history message's file stands: "" (here),
	// requestable, requested or unavailable (client.FileInfo). Note says
	// more when there is more to say (the browser device: not kept here).
	Availability string `json:"availability,omitempty"`
	Note         string `json:"note,omitempty"`
	// Openable says GET /api/files/{message}/{index}?dir=in|out (dir: the
	// message's Dir) can serve this file
	// now: a received file this device holds or can fetch, or a file sent
	// from this device, from the copy it kept for itself at send time.
	// False (with Note) for a sent file this device kept no copy of: one
	// sent before copies were kept, or from another device of this
	// person. The page shows Open only when true; never a fake Open.
	Openable bool `json:"openable"`
}

// Files is implemented by providers that send files and open them (MEL-489).
// Bytes the page hands over are kept privately until sent; a received file
// is opened only after it matches what its sender signed; a file sent from
// this device opens from the copy kept for the sender at send time (a
// message id from either direction works; FileView.Openable says whether).
//
// Who owns a staged file: the page, from StageFile until it names the id in
// a send or discards it. A send takes every id it names, whether it sends
// or refuses; DiscardFiles removes files the page no longer sends (unknown
// ids are ignored); what the page never names again is removed after an
// hour, and whatever a previous run left when this one starts.
type Files interface {
	StageFile(name string, r io.Reader) (id string, err error)
	DiscardFiles(ids []string)
	// OpenFile: dir is the message's direction as the page shows it, "in"
	// or "out" (DMMessage.Dir, Message.Dir); a message id is chosen by its
	// sender, so a received id can equal a sent one, and only the direction
	// makes the reference exact. With dir "" an id in both directions is
	// refused, never resolved to the other file.
	OpenFile(ctx context.Context, dir, msgID string, index int) (io.ReadCloser, string, error)
}

// MessageControls is implemented by providers that can react to, edit and
// delete messages (MEL-476, MEL-477): reactions by anyone in the thread or
// conversation, edits and deletions by the sender only; every control is
// signed and sent to the devices that can read one (an older peer is
// refused, never sent something else). None of them runs, cancels or
// decides anything.
type MessageControls interface {
	React(ControlAction) (string, error)
	EditMessage(ControlAction) (string, error)
	DeleteMessage(ControlAction) (string, error)
}

// ControlAction names the message as the page shows it (its conversation
// or "", its id and direction) and what to do.
type ControlAction struct {
	Conv   string `json:"conv,omitempty"`
	ID     string `json:"id"`
	Dir    string `json:"dir"` // in or out: the message's own
	Emoji  string `json:"emoji,omitempty"`
	Remove bool   `json:"remove,omitempty"` // react: take the emoji off
	Text   string `json:"text,omitempty"`   // edit: the new text
}

// ResponderControl is implemented by the daemon provider: the person sees
// and changes how questions and tasks are handled on this computer (their
// responder, MEL-428/MEL-498). Detection is the daemon's own PATH lookup;
// nothing is run to find out, so Found says installed, not working. The
// browser device never implements it: it runs nothing.
type ResponderControl interface {
	ResponderStatus() (ResponderView, error)
	SetResponder(ResponderChange) (string, error)
}

// ResponderView is how this computer handles questions and tasks now.
type ResponderView struct {
	Chosen  bool     `json:"chosen"`            // the person has decided (a harness, or manual)
	Manual  bool     `json:"manual"`            // no automatic responder: everything waits for the person
	Harness string   `json:"harness,omitempty"` // the chosen harness, when not manual
	Dir     string   `json:"dir,omitempty"`     // where it runs
	Timeout int      `json:"timeout_seconds,omitempty"`
	Context []string `json:"context,omitempty"` // files given with every question (kept across edits)
	// Ready says the chosen harness is on the daemon's PATH and Dir exists.
	// Problem says why not, in plain words. Ready never claims it is
	// logged in or working: only running a job shows that.
	Ready     bool          `json:"ready"`
	Problem   string        `json:"problem,omitempty"`
	Harnesses []HarnessView `json:"harnesses"` // every supported harness, found or not
}

// HarnessView is one supported harness as seen from the daemon.
type HarnessView struct {
	Name         string `json:"name"`
	Found        bool   `json:"found"`
	Path         string `json:"path,omitempty"`
	TestedLive   string `json:"tested_live,omitempty"`
	QuestionMode string `json:"question_mode,omitempty"`
}

// ResponderChange is what the page may change: manual handling, or the
// harness and its directory. Timeout and context files are kept as they
// are (the CLI sets them); an empty Dir keeps the current one.
type ResponderChange struct {
	Manual  bool   `json:"manual"`
	Harness string `json:"harness"`
	Dir     string `json:"dir"`
}

// HistoryFiles is implemented by providers whose person's devices share
// their chats (MEL-433): a history message's file is asked for from the
// device of yours it came from, and opens once that device sends it.
type HistoryFiles interface {
	RequestFile(ctx context.Context, msgID string, index int) error
}

// ReminderView is a pending reminder on a received message (S-R): it only
// asks for the person's attention, and nobody else sees it.
type ReminderView struct {
	Message string    `json:"message"`
	Conv    string    `json:"conv,omitempty"` // its DM; "" for a device conversation
	From    string    `json:"from"`
	Title   string    `json:"title"` // the message's first line
	Due     time.Time `json:"due"`
	Overdue bool      `json:"overdue"` // past its time, until it ends
}

// Reminders is implemented by providers where received messages can take
// a reminder. A reminder ends when the message is replied to, or when the
// person marks it done or cancels it.
type Reminders interface {
	SetReminder(id string, due time.Time) error
	DoneReminder(id string) error
	CancelReminder(id string) error
}

// NotifyView is what the page shows about optional DM alerts
// (docs/revival/NOTIFY.md). The browser device's engine sends the same
// shape for Web Push.
type NotifyView struct {
	Available bool     `json:"available"`
	Native    bool     `json:"native"` // this computer shows the alert itself: no browser permission is asked
	Enabled   bool     `json:"enabled"`
	Pending   bool     `json:"pending"`
	Reason    string   `json:"reason,omitempty"`
	Mutes     []string `json:"mutes"`   // conversation ids
	Allowed   []string `json:"allowed"` // addresses whose DM turns may alert
}

// Alerts is implemented by providers that alert for DMs on this computer
// (the desktop daemon). Off until the person turns them on; the page
// reports only messages it actually presented.
type Alerts interface {
	NotifyEnable() (string, error)
	NotifyDisable() (string, error)
	NotifyMute(conv string, muted bool) (string, error)
	NotifyAllow(person string, allowed bool) (string, error)
	NotifySeen(conv string, ids []string) error
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
	AgentID   string   `json:"agent_id,omitempty"`
	Conv      string   `json:"conv"`
	Host      string   `json:"host"`
	Share     []string `json:"share"`
	TasksFrom []string `json:"tasks_from"`
	Note      string   `json:"note"`
}

// AgentAsk is a question (or task) for an active participation's agent.
type AgentAsk struct {
	Topic         string                  `json:"topic,omitempty"`
	ID            string                  `json:"id,omitempty"` // optional local send correlation (logical id in a conversation)
	ReplyReceiver *ReplyReceiverSelection `json:"reply_receiver,omitempty"`
	PID           string                  `json:"pid"`
	Kind          string                  `json:"kind"`
	Body          string                  `json:"body"`
	Files         []string                `json:"files,omitempty"` // staged-file IDs owned by this provider; no task authority
}

// AgentView is an agent invited into a DM, as the page shows it: whose
// installation runs it, who invited it, what it may be shown and who may
// give it tasks. The host's person decides; either person can end it.
type AgentView struct {
	PIDs       []string     `json:"pids,omitempty"`
	Member     bool         `json:"member,omitempty"`
	Inviters   []PersonView `json:"inviters,omitempty"`
	External   bool         `json:"external,omitempty"` // exact invited host outside the DM member persons
	AgentID    string       `json:"agent_id,omitempty"`
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
// only: a DM's question or task would run nowhere yet. Files are the ids of
// files the page handed over (Files.StageFile).
type DMDraft struct {
	Topic         string                  `json:"topic,omitempty"`
	ID            string                  `json:"id,omitempty"`  // optional local send correlation (logical id in a conversation)
	PID           string                  `json:"pid,omitempty"` // exact human author participation; not a receiver/executor
	ReplyReceiver *ReplyReceiverSelection `json:"reply_receiver,omitempty"`
	Conv          string                  `json:"conv"`
	Body          string                  `json:"body"`
	ReplyTo       string                  `json:"reply_to,omitempty"`
	Files         []string                `json:"files,omitempty"`
	Quote         string                  `json:"quote,omitempty"`
}

// DMStateText is what the page says about a DM message's state.
func DMStateText(dir, kind, state, peer, detail string) string {
	if dir == "out" {
		if state == "waiting" {
			switch {
			case strings.HasPrefix(detail, client.WaitPeerUpdate):
				if peer == "You" || peer == "your other device" {
					return "Your other device needs to update AgentNet"
				}
				return "Waiting for " + peer + " to update AgentNet"
			case strings.HasPrefix(detail, client.WaitServerUpdate):
				return "Your server needs an update before this can be sent"
			case strings.HasPrefix(detail, client.WaitServerUnavailable):
				return "Cannot reach your server; retries automatically"
			case detail != "":
				return "Kept here, not sent yet: " + detail
			default:
				return "Kept here, not sent yet"
			}
		}
		return StateText("out", kind, state, peer)
	}
	switch state {
	case "conv_held":
		return "Held for you: nothing runs it. Answer here if you want to."
	case "part_waiting": // a request to this device's agent, not claimed yet
		return "For your agent. It has not run yet: it runs here only with a responder chosen on this computer, while the invitation allows it."
	case "not_run":
		if strings.Contains(detail, "asking guest's participation ended") { // agentjob.go: the guest's end, not the assistant's
			return "Not run: the guest who asked left or was removed first."
		}
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
	Agent        bool   `json:"agent"`         // this device runs an agent (a responder or a named agent): what it tells others
}

// ThreadSummary is one row of the thread list.
type ThreadSummary struct {
	Conv       string    `json:"conv,omitempty"`
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
	// The thread as a topic (client topics.go, docs/plans/TOPICS.md):
	// State is active, done or archived (TopicActive...); DoneBy agent or
	// you while done (kept once archived); Conclusion is the first line of
	// the agent's final reply and ConcludedBy the device that sent it;
	// Pending says something in it is still open (it is never archived
	// then); Renamed says Title is the person's own name for it here, and
	// AutoTitle is then the automatic one (its first line). A name and
	// Mark done / Reopen are kept on this device only.
	State       string `json:"state"`
	DoneBy      string `json:"done_by,omitempty"`
	Conclusion  string `json:"conclusion,omitempty"`
	ConcludedBy string `json:"concluded_by,omitempty"`
	Pending     bool   `json:"pending"`
	Renamed     bool   `json:"renamed,omitempty"`
	AutoTitle   string `json:"auto_title,omitempty"`
	// QuietSince is when it went quiet (its last message, or a later Mark
	// done or Reopen here); it is archived TopicArchiveAfter later.
	QuietSince time.Time `json:"quiet_since"`
	// AgentID is the thread's agent, when one is named (client.ThreadSummary).
	AgentID string `json:"agent_id,omitempty"`
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
// there (or, with Reason, a notice of this machine's own). It is not a
// decision here and carries no request.
type ReviewItem struct {
	ID      string    `json:"id"`
	Peer    string    `json:"peer"`
	Kind    string    `json:"kind"`
	Why     string    `json:"why"`
	Excerpt string    `json:"excerpt"`
	At      time.Time `json:"at"`
	Notice  bool      `json:"notice,omitempty"`
	// Report: a notice that names the waiting requests (client.Report): the
	// host's snapshot at Report.At, never a live queue. Items marked
	// actionable may be decided from here (this device is a granted
	// operator on that host: POST /api/operator/decide); their Result is
	// the host's answer to this device's last decision. A count-only
	// notice has no Report and offers nothing but reading and dismissing.
	Report *client.Report `json:"report,omitempty"`
	// Proposal: a task that carries out an action this device's agent
	// proposed (MEL-521): the question, the proposal and who confirmed
	// it, for the person approving it. It grants nothing.
	Proposal *client.ProposalView `json:"proposal,omitempty"`
	// Reason ReasonSelfConsented (owner decision D3): a notice, not a
	// received item, that this person's own agent AgentID ("" the default
	// one) joined conversation Conv without their accept, invited from
	// their own trusted device Peer; ID is the participation's id. It is
	// only read and dismissed: DoResolve with ID removes the notice, and
	// the agent stays.
	Reason  string `json:"reason,omitempty"`
	Conv    string `json:"conv,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// ReasonSelfConsented is ReviewItem.Reason for an own agent that joined
// without the person's accept.
const ReasonSelfConsented = "self_consented"

// ReasonDeviceAdmin is a Hub-reported company-settings access change: a
// local notice, never a request or permission. Resolve dismisses it here.
const ReasonDeviceAdmin = "device_admin"

// ConvItem is a conversation item waiting for the person
// (client.ConvReview); Reason is one of client.ReviewAwaiting,
// ReviewNeedsHuman, ReviewInterrupted, ReviewInvite or ReviewHeldTurn, and Conv is the
// conversation the page opens for it. Opening it, or clicking an alert
// about it, decides nothing. Actions are the decisions available here: on
// a request (ID) through /api/act, on an invitation (PID only) through
// /api/dm/agent/decide; a held turn has none, it is answered in the
// conversation. DecideOn names the host device where it is decided when
// that is not this one (a browser runs no agent): then it is read-only.
type ConvItem struct {
	Reason   string    `json:"reason"`
	Conv     string    `json:"conv"`
	PID      string    `json:"pid,omitempty"`
	ID       string    `json:"id,omitempty"`
	Peer     string    `json:"peer"`
	Kind     string    `json:"kind,omitempty"`
	Why      string    `json:"why"`
	Excerpt  string    `json:"excerpt"`
	At       time.Time `json:"at"`
	Unread   bool      `json:"unread,omitempty"`
	Actions  []string  `json:"actions,omitempty"`
	DecideOn string    `json:"decide_on,omitempty"`
}

// OperatorDecisions is implemented by the daemon provider: deciding a
// request another machine holds, as one of its granted operators. The
// host applies the decision once, in the state and attempt named, and
// answers; nothing is decided by reading, clicking or dismissing.
type OperatorDecisions interface {
	Decide(DecisionAction) (string, error)
}

// DecisionAction names a reported request exactly and what to do with it.
type DecisionAction struct {
	Host    string `json:"host"`
	ID      string `json:"id"`  // the request's id on the host (ReportItem.ID)
	Key     string `json:"key"` // the requester's key (ReportItem.Key)
	Action  string `json:"action"`
	Expect  string `json:"expect"`  // the state the item showed
	Attempt int64  `json:"attempt"` // the attempt the item showed
	Text    string `json:"text,omitempty"`
	Report  string `json:"report"` // the notice (ReviewItem.ID) the item came from
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
// Code says why (Hold*), so a page writes its own sentence with the
// sender's name from its people list; Reason is the provider's own
// sentence, a fallback that names the sender's address.
type QuarantineItem struct {
	ID     string    `json:"id"`
	Peer   string    `json:"peer"`
	Code   string    `json:"code"`
	Reason string    `json:"reason"` // plain text
	At     time.Time `json:"at"`
}

// Why a received message is held back (QuarantineItem.Code). The browser
// engine gives the same codes (engine.mjs holdCode).
const (
	HoldKeyChanged = "key_changed"           // the sender's key changed: held until the person trusts the new one
	HoldProof      = "proof_pending"         // the conversation or person it names cannot be checked here yet
	HoldConflict   = "identity_conflict"     // it disagrees with the person record kept here
	HoldDuplicate  = "conflicting_duplicate" // different content under a message already received
	HoldUnverified = "unverified"            // it did not verify: its content is never shown
)

// holdCode is the code for a client quarantine reason; any reason not
// named here did not verify.
func holdCode(reason string) string {
	switch reason {
	case HoldKeyChanged, HoldProof, HoldConflict, HoldDuplicate:
		return reason
	}
	return HoldUnverified
}

// Thread is one conversation with one peer.
type Thread struct {
	ID               string      `json:"id"`
	Peer             string      `json:"peer"`
	Key              PeerKey     `json:"key"`
	Approved         bool        `json:"approved"`                    // questions from this peer are answered automatically
	PermissionPerson *PersonView `json:"permission_person,omitempty"` // verified current grant target
	QuestionTarget   string      `json:"question_target,omitempty"`   // exact saved grant responsible
	TaskTarget       string      `json:"task_target,omitempty"`
	TaskGrant        string      `json:"task_grant"` // "" none, "active", or why a grant does not hold
	Messages         []Message   `json:"messages"`
	// Topic is this thread as a topic, archived or not (Topics providers).
	Topic *ThreadSummary `json:"topic,omitempty"`
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
	Target    *envelope.Target `json:"target,omitempty"`
	AgentID   string           `json:"agent_id,omitempty"`
	ID        string           `json:"id"`
	Dir       string           `json:"dir"` // in or out
	From      string           `json:"from"`
	To        string           `json:"to"`
	Kind      string           `json:"kind"`
	Body      string           `json:"body"`
	ReplyTo   string           `json:"reply_to,omitempty"`
	At        time.Time        `json:"at"`
	State     string           `json:"state,omitempty"`  // stored state, shown under details
	Status    string           `json:"status,omitempty"` // outcome carried by an answer or result
	Path      string           `json:"path,omitempty"`   // relay or direct
	Responder string           `json:"responder,omitempty"`
	Summary   string           `json:"summary,omitempty"` // follow-up summary written locally
	Detail    string           `json:"detail,omitempty"`  // local note: failure or needs-human reason
	Unread    bool             `json:"unread,omitempty"`
	Files     []File           `json:"files,omitempty"`
	Author    Author           `json:"author"`
	StateText string           `json:"state_text,omitempty"`
	Next      string           `json:"next,omitempty"`
	Actions   []string         `json:"actions,omitempty"` // decisions available on this message
	Controls                   // reactions, edit and deletion applied to it (client.Controls, flattened)
	// Exec: a request this device sent, where its executor last said it
	// stands (client.ExecView): from the executing device only, never from
	// delivery or presence; absent when it never said. Stale: that device
	// is not connected now.
	Exec     *client.ExecView     `json:"exec,omitempty"`
	Quote    string               `json:"quote,omitempty"`
	SentAt   time.Time            `json:"sent_at,omitzero"`
	Proposal *client.ProposalView `json:"proposal,omitempty"`
}

// Controls is what reactions, edits and deletion did to a message, as
// this device resolves them (client.Controls). Body stays what was sent
// or admitted; when Edited, Text is what to show, and a question or task
// keeps running on Body. Deleted hides Body and files; nothing was
// cancelled or recalled. Can lists what this device may do: react, edit,
// delete (POST /api/message/{what}).
type Controls = client.Controls

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
	// Index and Openable are as FileView's: GET /api/files/{message}/{index}
	// serves the file when Openable, for received and sent messages alike.
	Index    int    `json:"index"`
	Openable bool   `json:"openable"`
	Note     string `json:"note,omitempty"`
}

// Draft is a message to send.
type Draft struct {
	ID            string                  `json:"id,omitempty"` // optional local send correlation (logical id in a conversation)
	ReplyReceiver *ReplyReceiverSelection `json:"reply_receiver,omitempty"`
	AgentID       string                  `json:"agent_id,omitempty"`
	To            string                  `json:"to"`
	Kind          string                  `json:"kind"` // message, question or task
	Body          string                  `json:"body"`
	ReplyTo       string                  `json:"reply_to,omitempty"`
	Files         []string                `json:"files,omitempty"` // ids of files the page handed over (Files.StageFile)
	Quote         string                  `json:"quote,omitempty"`
}

// Sent is the outcome of Send.
type Sent struct {
	LID    string `json:"lid,omitempty"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Action is a local decision.
type Action struct {
	SendID string   `json:"send_id,omitempty"`
	Do     string   `json:"do"`
	ID     string   `json:"id,omitempty"`     // message id, or peer address for peer actions
	Body   string   `json:"body,omitempty"`   // reply text
	Reason string   `json:"reason,omitempty"` // decline reason
	IDs    []string `json:"ids,omitempty"`    // read: messages to mark read
	Key    string   `json:"key,omitempty"`    // trust: the fingerprint the person compared
}

// Actions the page can request.
const (
	DoIt           = "do_it"         // confirm the stored proposal, with its original target and bytes
	DoReply        = "reply"         // answer a received item by hand (takes it over)
	DoAccept       = "accept"        // run a task once, let the responder answer a held question, or run again
	DoAcceptAlways = "accept_always" // run this task and grant its verified person (or exact device key)
	DoDecline      = "decline"
	DoResolve      = "resolve" // close a needs-human item without sending anything
	DoCancel       = "cancel"
	DoApprove      = "approve"   // ID = verified person or explicit device: answer future questions
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
		case "custody": // the server has it; whether peer is connected is not known from this
			return "On the server; delivery to " + peer + " not confirmed yet"
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
		if kind == KindQuestion { // only a conversation guest's question waits so (agentjob.go)
			return "Needs you: a guest's question runs only if you accept it or approve them"
		}
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
		case "interrupted": // client reviewStates: a request is run again or closed by the person
			if kind == KindQuestion || kind == KindTask {
				return "you"
			}
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
	case "interrupted": // run again, answered by hand, or closed without running (client.Resolve)
		return []string{DoAccept, DoReply, DoResolve}
	case "failed", "cancelled":
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
	if strings.HasPrefix(why, peer) && strings.Contains(peer, "/") {
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
