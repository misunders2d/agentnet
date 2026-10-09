// Package envelope seals end-to-end encrypted, sender-signed messages.
//
// The outer Envelope is visible to the Hub and signed by the sender. The
// encrypted Inner repeats id, from and to so a member who strips the outer
// signature and re-signs someone else's ciphertext is detected by the
// recipient.
package envelope

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"filippo.io/age"

	gdrive "github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Version is the envelope format version.
const Version = 1

// Version2 is the conversation envelope (human DMs). Its signature uses its
// own domain, so a v1 signature can never pass for v2 or back; an older
// client rejects it and quarantines it (fail closed), which is why senders
// use it only for devices whose signed capabilities include protocol.CapEnv2.
const Version2 = 2

// Version3 is a control message: a signed, encrypted record ABOUT one
// earlier message (a reaction, a revision of its text, or its retraction),
// never a message of its own. It names its target exactly (Ref: the
// target's id and the key that sent it) and carries no root, executor,
// participation or fan-out. Its signature has its own domain. Senders use
// it only for devices whose signed capabilities include
// protocol.CapControl and relays that list protocol.FeatureEnv3; an older
// reader refuses it (fail closed). Two scopes: with Conv "" the target is a
// device message (its envelope id, in the thread with that device); with
// Conv set the target is a conversation turn (its logical id).
const Version3 = 3

// MaxCiphertext bounds a message body; files travel separately.
const MaxCiphertext = 256 << 10

// MaxAttachments bounds the files one message may carry.
const MaxAttachments = 8

// Blob identifies one encrypted attachment as the Hub stores it: the
// ciphertext's id, length and SHA-256. It is signed in the envelope, so the
// Hub cannot substitute another file.
type Blob struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Attachment is the encrypted manifest entry for one file.
type Attachment struct {
	Blob   Blob   `json:"blob"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`   // plaintext bytes
	SHA256 string `json:"sha256"` // plaintext digest
}

// Envelope is the Hub-visible, sender-signed message wrapper.
type Envelope struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	CT    []byte `json:"ct"`
	Blobs []Blob `json:"blobs,omitempty"`
	// Session, when set, addresses one running daemon of the recipient.
	// Without Fallback the message expires if that session ends first;
	// with Fallback it goes to the agent's inbox instead.
	Session  string `json:"session,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	// Version 2 only: the sender asks for the recipient's attention (a hint;
	// the recipient's preferences decide), on the recipient's notification
	// channel for the conversation (protocol.NotifyChannel). Both or
	// neither; set only for recipients whose capabilities include
	// protocol.CapNotify, since an older reader would refuse the signature.
	Attn bool   `json:"attn,omitempty"`
	Chan string `json:"chan,omitempty"`
	Sig  []byte `json:"sig,omitempty"`
}

// Inner is the encrypted content.
type Inner struct {
	V           int          `json:"v"`
	ID          string       `json:"id"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	TS          int64        `json:"ts"`
	Kind        string       `json:"kind"`
	Body        string       `json:"body"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Session     string       `json:"session,omitempty"`
	Fallback    bool         `json:"fallback,omitempty"`
	Status      string       `json:"status,omitempty"` // for answers and results

	// Version 2 only; each must be empty in version 1.
	Conv    string          `json:"conv,omitempty"`    // conversation id (protocol.ConvRoot.ID)
	LID     string          `json:"lid,omitempty"`     // logical id, the same in every per-device copy
	Root    json.RawMessage `json:"root,omitempty"`    // the conversation's signed root, at most protocol.MaxConvRoot bytes
	Sub     string          `json:"sub,omitempty"`     // "" (a turn), SubEvent or SubExcerpt
	Replica bool            `json:"replica,omitempty"` // a history copy: never executes
	Origin  string          `json:"origin,omitempty"`  // OriginUI or "agent:<harness>": the sender's assertion, not proof
	Emotion string          `json:"emotion,omitempty"` // the agent's chosen emotion; required on agent-origin turns
	Target  *Target         `json:"target,omitempty"`  // the one execution recipient of a question or task
	PID     string          `json:"pid,omitempty"`     // the agent participation a request is for, an output is from, or an event is about
	Fan     []Fan           `json:"fan,omitempty"`     // the member persons' rosters the sender sent copies to (one per device)

	// Version 3 only: the message this control is about.
	Ref *Ref `json:"ref,omitempty"`
	// AgentID identifies the host's named executor on an answer or result.
	// It is negotiated with CapAgentIdentity; absent fields preserve old bytes.
	AgentID string `json:"agent_id,omitempty"`
	// ReceiverRoute commits a selected own-device return route, never execution authority.
	ReceiverRoute *ReceiverRoute `json:"receiver_route,omitempty"`
	Human         *HumanTurn     `json:"human,omitempty"`
	Quote         string         `json:"quote,omitempty"`
	TopicDone     bool           `json:"topic_done,omitempty"`
	Topic         string         `json:"topic,omitempty"`
	TopicEvent    *TopicEvent    `json:"topic_event,omitempty"`
	SendGroup     string         `json:"send_group,omitempty"` // signed presentation only; never execution identity
}

// TopicEvent is a shared human action. Seen names exact logical turns covered
// by done/open; a later or previously unseen turn always ends the mark.
type TopicEvent struct {
	Action string   `json:"action"`
	Seen   []string `json:"seen,omitempty"`
}

// CheckTopic is shared by live envelopes and retained history.
func CheckTopic(in Inner) error {
	if in.Topic != "" && (in.V != Version2 || !validID(in.Topic) || in.Sub != "") {
		return errors.New("topic belongs only on a conversation turn")
	}
	if e := in.TopicEvent; e != nil {
		if in.Topic == "" || in.Kind != KindMessage || in.Target != nil || AgentOrigin(in.Origin) || in.Status != "" || len(in.Attachments) != 0 {
			return errors.New("topic event belongs only on a human conversation message")
		}
		if e.Action != "create" && e.Action != "done" && e.Action != "open" {
			return errors.New("invalid topic action")
		}
		for _, id := range e.Seen {
			if !validID(id) {
				return errors.New("invalid topic seen message")
			}
		}
	}
	if in.TopicDone && (in.V != Version && in.V != Version2 || in.Status != StatusDone || in.Kind != KindAnswer && in.Kind != KindResult || in.V == Version2 && (in.Topic == "" || in.PID == "" || !AgentOrigin(in.Origin))) {
		return errors.New("topic_done belongs only on a completed agent answer or result")
	}
	return nil
}

// Ref names one earlier message exactly: its id (a device message's
// envelope id, or a conversation turn's logical id) and the fingerprint of
// the key that sent it, as the receiver verified it. A control whose Ref
// matches no message held here is kept aside until one arrives; it is
// never applied to another message with the same id.
type Ref struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

// Fan names a member person and the roster step whose devices the sender
// sent this message's copies to. A device of that person that knows a newer
// step forwards the message to the devices it adds (client).
type Fan struct {
	Person string `json:"person"`
	Roster string `json:"roster"`
}

// Kinds of messages. The kind is signed and encrypted; it states intent,
// it never grants the sender any authority on the recipient's machine.
const (
	KindMessage  = "message"
	KindQuestion = "question" // may be answered automatically for approved senders
	KindAnswer   = "answer"   // reply to a question
	KindTask     = "task"     // runs only after the recipient accepts it
	KindResult   = "result"   // outcome of a task
)

func validKind(k string) bool {
	switch k {
	case KindMessage, KindQuestion, KindAnswer, KindTask, KindResult:
		return true
	}
	return false
}

// Outcome statuses carried by answers and results.
const (
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusTimeout     = "timeout"
	StatusCancelled   = "cancelled"
	StatusDeclined    = "declined"
	StatusInterrupted = "interrupted"

	// StatusProgress marks a version 1 plain-text message replying to one
	// exact request as a nonterminal update. It never settles that request,
	// feeds a selected receiver or counts as an answer.
	StatusProgress = "progress"

	// StatusProposal marks an answer whose whole body is the exact,
	// self-contained task its agent proposes instead of an action its
	// question run may not take (MEL-521). It is an offer: the asker may
	// confirm it as a task, which then meets the recipient's normal task
	// approval. It runs nothing and grants nothing. Only on an answer
	// replying to one request (version 1, or a participation's output),
	// with a non-blank plain body: no files, target, route or sub.
	StatusProposal = "proposal"

	// StatusReviewNotice on a plain message with no reply_to and no files
	// says only that items wait for a person on the sender's machine. It is
	// content-free and grants nothing: the recipient files it for its own
	// person's attention and never runs, accepts or forwards it. Clients
	// before it store such a message as an ordinary one.
	StatusReviewNotice = "review_notice"
)

// Version 2 inner values.
const (
	SubEvent           = "event"            // a conversation event; history only, never a request
	SubExcerpt         = "excerpt"          // shared history; never a request
	SubHistory         = "history"          // a message or event of the conversation, forwarded by a device of the recipient's own person; never a request
	SubInvitationSync  = "invitation-sync"  // inert outgoing invitation view for own-human devices
	SubReadSync        = "read-sync"        // exact read references between current own-human devices
	SubRootSync        = "root-sync"        // signed DM root only, current own-human devices; no turn or execution
	SubFile            = "file"             // a request for, or the offer of, a history message's file between devices of one person; never a request to run
	SubGroupProof      = "group-proof"      // original signed ciphertext records in one JSON attachment; never a turn
	SubGroupContext    = "group-context"    // current-only signed membership in one JSON attachment; never a turn
	SubGroupInvite     = "group-invite"     // verified proposal; never installs membership or runs work
	SubGroupWithdrawal = "group-withdrawal" // exact ordinary departure; quiet non-executing overlay
	SubGroupConsent    = "group-consent"    // explicit human admission/decline; never a task approval
	// SubDriveSpace is a conversation's shared Drive space record
	// (gdrive.Space as JSON): metadata a member publishes, applied by the
	// receiver under the sender's verified person, never shown as a turn,
	// given to an agent or run; sent only to devices with
	// protocol.CapDriveSpace.
	SubDriveSpace = "drive-space"
	// Version 3 controls (Body is the control's JSON payload).
	SubReaction   = "reaction"   // Reaction: add or remove one emoji on the target
	SubRevision   = "revision"   // Revision: new text for the target, shown as an edit; nothing runs or reruns
	SubRetraction = "retraction" // Retraction: the target is shown as deleted here; nothing is cancelled or recalled
	SubStatus     = "status"     // Status: the executing host's word on one request's state (never a job or alert)
	SubDecision   = "decision"   // Decision: an operator's signed decision on one request held by a host
	SubClear      = "clear"      // Clear: its person's copy of a conversation erased on that person's own devices only
	OriginUI      = "ui"         // typed by a person, as the sending device asserts
	// OriginAgentPrefix starts "agent:<harness>", written by an agent.
	OriginAgentPrefix = "agent:"
)

// Control payloads, the Body of a version 3 message.

// Reaction adds (Op "add") or removes (Op "remove") one emoji on the
// target. N is the author's own counter per target: of two reactions by
// one author with the same emoji, the higher N wins, so devices that see
// them in any order converge; equal N: the later message id wins.
type Reaction struct {
	Emoji string `json:"emoji"`
	Op    string `json:"op"`
	N     int64  `json:"n"`
}

// Revision replaces the target's displayed text; Rev is the author's own
// revision number for the target (highest wins; equal: the later id).
// What was admitted for execution never changes.
type Revision struct {
	Rev  int64  `json:"rev"`
	Text string `json:"text"`
}

// Retraction marks the target deleted on every device that holds it.
type Retraction struct {
	Reason string `json:"reason,omitempty"`
}

// Status is what the device that holds and executes a request says about
// it: one of the states below, its own counter N per request (highest
// wins; equal: later id), when (At), and a bounded, audience-safe Detail
// (a blocker category or plain reason, never a harness's own output). A
// status answering an operator's decision names it (Decision, Report,
// Attempt) and, when refused, says why in Refused; such an answer never
// stands in for the request's execution state.
type Status struct {
	State    string `json:"state"`
	N        int64  `json:"n"`
	At       int64  `json:"at"`
	Detail   string `json:"detail,omitempty"`
	Refused  string `json:"refused,omitempty"`
	Decision string `json:"decision,omitempty"` // the decision message this answers
	Report   string `json:"report,omitempty"`   // the report the decision named
	Attempt  int64  `json:"attempt,omitempty"`  // the request's attempt the decision named
}

// Execution states a Status may carry (the terminal answer or result is a
// message of its own kind, not a status).
var statusStates = map[string]bool{"queued": true, "awaiting": true, "running": true, "needs_human": true, "resolved": true,
	"stopped": true, "not_run": true, "declined": true, "failed": true, "cancelled": true, "interrupted": true, "answered": true}

// Decision is an operator's decision on a request a host holds: Action on
// the request in state Expect (the host's own state name, as its report
// showed it) at attempt Attempt, with Text for a reply or a decline.
type Decision struct {
	Action  string `json:"action"`
	Expect  string `json:"expect"`
	Attempt int64  `json:"attempt"`
	Text    string `json:"text,omitempty"`
	Report  string `json:"report,omitempty"` // the report message the operator acted on
}

// Decision actions.
var decisionActions = map[string]bool{"accept": true, "decline": true, "resolve": true, "reply": true, "cancel": true, "continue": true}

// Clear erases its sender person's copy of one conversation on that
// person's own devices; it is sent to no one else. Ref and Turns are the
// exact turns erased (logical id and sender key), and nothing else is:
// a turn not named stays. Parts split the turns of one deletion; each part
// stands alone.
type Clear struct {
	Deletion string `json:"deletion"`
	Part     int    `json:"part"`
	Parts    int    `json:"parts"`
	Turns    []Ref  `json:"turns,omitempty"`
}

// Bounds of a Clear.
const (
	MaxClearParts = 1024
	MaxClearTurns = 2000
)

func (c Clear) valid() bool {
	if !validID(c.Deletion) || c.Part < 1 || c.Part > c.Parts || c.Parts > MaxClearParts || len(c.Turns) > MaxClearTurns {
		return false
	}
	for _, r := range c.Turns {
		if !validID(r.ID) || !protocol.ValidFingerprint(r.Fingerprint) {
			return false
		}
	}
	return true
}

// Bounds of control payloads.
const (
	MaxEmojiBytes    = 64
	MaxEmojiRunes    = 12
	MaxRevisionBytes = 64 << 10
	MaxReasonBytes   = 200
	MaxDetailBytes   = 400
	MaxDecisionText  = 16 << 10
)

// ValidEmoji reports whether s is one emoji: valid UTF-8 of bounded size,
// made of symbol runes (any script's) and the joiners emoji sequences use
// (zero-width joiner, variation selectors, keycap, tags, skin tones,
// regional indicators), with no letters, digits, spaces or controls.
func ValidEmoji(s string) bool {
	if s == "" || len(s) > MaxEmojiBytes || !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxEmojiRunes {
		return false
	}
	symbol := false
	keycap := strings.ContainsRune(s, 0x20E3) // "1️⃣": a digit, # or * made an emoji by the keycap mark
	for _, r := range s {
		switch {
		case r == 0x200D, r == 0xFE0F, r == 0xFE0E, r == 0x20E3, r >= 0xE0020 && r <= 0xE007F, r >= 0x1F3FB && r <= 0x1F3FF:
			// joiners, variation selectors, keycap, tags, skin tones
		case keycap && (r >= '0' && r <= '9' || r == '#' || r == '*'):
			symbol = true
		case r >= 0x1F1E6 && r <= 0x1F1FF, r >= 0x1F000, unicode.IsSymbol(r), unicode.Is(unicode.So, r):
			symbol = true
		default:
			return false
		}
	}
	return symbol
}

// Blank reports whether text shows nothing: it holds only white space,
// default ignorable code points (zero-width spaces and joiners, direction
// marks and isolates, word joiners, variation selectors, fillers, the byte
// order mark, tags) and the braille blank (U+2800, a symbol that draws no
// dot). Text composed here, a message or an edit, needs more than that
// unless files go with it; what arrives is not judged by it.
func Blank(text string) bool {
	for _, r := range text {
		if !unicode.IsSpace(r) && !ignorable(r) && r != 0x2800 {
			return false
		}
	}
	return true
}

// ignorable is Unicode's Default_Ignorable_Code_Point property.
func ignorable(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x034F, r == 0x061C, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x3164, r == 0xFEFF, r == 0xFFA0:
		return true
	case r >= 0x180B && r <= 0x180F, r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x206F,
		r >= 0xFE00 && r <= 0xFE0F, r >= 0xFFF0 && r <= 0xFFF8, r >= 0x1BCA0 && r <= 0x1BCA3, r >= 0x1D173 && r <= 0x1D17A, r >= 0xE0000 && r <= 0xE0FFF:
		return true
	}
	return false
}

// OneEmoji reports whether s is one emoji as a reaction composed here
// must be: ValidEmoji, and exactly one emoji sequence (a base emoji, or a
// pair of regional indicators, joined to more only by zero-width joiners,
// with its selectors, keycap, tags and skin tone), whose base runes are
// pictographs or other symbols, never currency, math (but the emoji among
// them) or modifier signs or characters of other planes. A row of emoji is
// not one. What arrives is
// still judged by ValidEmoji alone, so reactions peers already sent stay
// readable.
func OneEmoji(s string) bool {
	if !ValidEmoji(s) {
		return false
	}
	bases, joined, pairing := 0, false, false
	for _, r := range s {
		switch {
		case r == 0x200D:
			joined = true
			continue
		case r == 0xFE0F, r == 0xFE0E, r == 0x20E3, r >= 0xE0020 && r <= 0xE007F, r >= 0x1F3FB && r <= 0x1F3FF:
			continue // part of the emoji before it
		case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators: two make one flag
			if pairing {
				pairing = false
				continue
			}
			pairing = true
		case r >= '0' && r <= '9', r == '#', r == '*': // a keycap's base (ValidEmoji holds them to one)
			pairing = false
		case emojiBase(r):
			pairing = false
		default:
			return false
		}
		if !joined {
			bases++
		}
		joined = false
	}
	return bases == 1 && !joined
}

// emojiBase reports whether r may be the base of an emoji: any rune of the
// emoji blocks (U+1F000 to U+1FAFF, also ones assigned after this
// program), an arrow, the emoji that are math symbols (◻️ ◼️ ◽ ◾ ⤴️ ⤵️), or
// another symbol of the Basic Multilingual Plane (©, ☕, ★, ✓) but the
// braille blank, which shows nothing.
func emojiBase(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	case r > 0xFFFF, r == 0x2800:
		return false
	case r >= 0x2190 && r <= 0x21FF, r >= 0x25FB && r <= 0x25FE, r == 0x2934, r == 0x2935:
		return true
	}
	return unicode.Is(unicode.So, r)
}

// checkVersion3 validates a control: only a plain message with a known
// control sub, an exact Ref, a bounded payload of that sub's shape, and
// none of the fields of a turn (root, target, participation, fan, files,
// reply, origin, emotion). Conv "" scopes it to a device thread; Conv set
// needs a logical id for the control itself (its copies to several
// devices share it).
// ValidateControl shares the native control shape checks with authenticated
// historical forwarding. It verifies no author or membership authority.
func ValidateControl(in Inner) error {
	if in.V != Version3 {
		return errors.New("a control requires version 3")
	}
	return checkVersion3(in)
}

func checkVersion3(in Inner) error {
	if in.Kind != KindMessage || in.Ref == nil || !validID(in.Ref.ID) || !protocol.ValidFingerprint(in.Ref.Fingerprint) {
		return errors.New("a control is a message about one exact earlier message (ref: id and sender key)")
	}
	if in.ReceiverRoute != nil || len(in.Root) != 0 || in.Target != nil || in.PID != "" && !AssistantReaction(in) || len(in.Attachments) != 0 || in.ReplyTo != "" ||
		in.Origin != "" && !defaultAssistantReaction(in) || in.Emotion != "" || in.Status != "" || in.Session != "" || in.Fallback {
		return errors.New("a control carries nothing but its ref and payload")
	}
	if AssistantReaction(in) {
		// An assistant's reaction names the assistant: a device thread's
		// named executor, or a conversation participation (and its agent).
		if in.AgentID != "" && !validID(in.AgentID) || in.PID != "" && !validID(in.PID) ||
			in.Conv == "" && in.PID != "" || in.Conv != "" && in.PID == "" ||
			in.Origin != "" && (in.Conv != "" || in.AgentID != "" || !validToken(strings.TrimPrefix(in.Origin, OriginAgentPrefix), 32)) {
			return errors.New("an assistant reaction names its executor or default responder (device thread) or its participation (conversation)")
		}
	}
	if in.Conv == "" {
		if in.LID != "" || in.Replica || in.Fan != nil {
			return errors.New("a device-thread control has no logical id, no fan and is no replica")
		}
	} else if !protocol.ValidHash(in.Conv) || !validID(in.LID) {
		return errors.New("a conversation control names its conversation and its own logical id")
	} else if err := checkFan(in.Fan); err != nil { // as a turn's: the rosters its copies went to, so receivers forward to devices the sender did not know
		return err
	}
	dec := json.NewDecoder(strings.NewReader(in.Body))
	dec.DisallowUnknownFields()
	switch in.Sub {
	case SubReaction:
		var r Reaction
		if dec.Decode(&r) != nil || !ValidEmoji(r.Emoji) || (r.Op != "add" && r.Op != "remove") || r.N < 0 {
			return errors.New("malformed reaction")
		}
	case SubRevision:
		var r Revision
		if dec.Decode(&r) != nil || r.Rev <= 0 || strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxRevisionBytes || !utf8.ValidString(r.Text) {
			return errors.New("malformed revision")
		}
	case SubRetraction:
		var r Retraction
		if dec.Decode(&r) != nil || len(r.Reason) > MaxReasonBytes || !utf8.ValidString(r.Reason) {
			return errors.New("malformed retraction")
		}
	case SubStatus:
		var r Status
		if dec.Decode(&r) != nil || !statusStates[r.State] || r.N <= 0 || r.At <= 0 || len(r.Detail) > MaxDetailBytes || !utf8.ValidString(r.Detail) ||
			len(r.Refused) > MaxDetailBytes || !utf8.ValidString(r.Refused) || (r.Decision != "" && !validID(r.Decision)) || (r.Report != "" && !validID(r.Report)) ||
			r.Attempt < 0 || (r.Refused != "" && r.Decision == "") {
			return errors.New("malformed status")
		}
	case SubDecision:
		var r Decision
		if in.Conv != "" || dec.Decode(&r) != nil || !decisionActions[r.Action] || !validStateToken(r.Expect) || r.Attempt < 0 ||
			len(r.Text) > MaxDecisionText || !utf8.ValidString(r.Text) || (r.Report != "" && !validID(r.Report)) ||
			((r.Action == "reply" || r.Action == "decline" || r.Action == "continue") && strings.TrimSpace(r.Text) == "") {
			return errors.New("malformed decision")
		}
	case SubClear:
		var r Clear
		if in.Conv == "" || dec.Decode(&r) != nil || !r.valid() {
			return errors.New("malformed conversation deletion")
		}
	default:
		return fmt.Errorf("unknown control %q", in.Sub)
	}
	if dec.More() {
		return errors.New("malformed control payload")
	}
	return nil
}

// checkFan validates a fan: at most the two member persons, each once.
func checkFan(fan []Fan) error {
	if len(fan) > 2 {
		return errors.New("a conversation message names at most its two member persons")
	}
	for i, f := range fan {
		if !validID(f.Person) || !protocol.ValidHash(f.Roster) || i > 0 && f.Person == fan[0].Person {
			return errors.New("invalid fan")
		}
	}
	return nil
}

// validStateToken reports whether s looks like a stored state name: 1-32
// lowercase letters or underscores.
func validStateToken(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c == '_') {
			return false
		}
	}
	return true
}

// AssistantReaction reports whether in is a reaction by an assistant: a
// version 3 reaction naming an agent or a participation, or a device
// thread's default responder (agent origin). Only reactions may.
func AssistantReaction(in Inner) bool {
	return in.V == Version3 && in.Sub == SubReaction && (in.AgentID != "" || in.PID != "" || defaultAssistantReaction(in))
}

// defaultAssistantReaction is a device thread's default responder reacting:
// no agent or participation, only its harness as an agent origin.
func defaultAssistantReaction(in Inner) bool {
	return in.V == Version3 && in.Sub == SubReaction && in.Conv == "" && in.AgentID == "" && in.PID == "" && AgentOrigin(in.Origin)
}

// IsControl reports whether sub names a version 3 control.
func IsControl(sub string) bool {
	return sub == SubReaction || sub == SubRevision || sub == SubRetraction || sub == SubStatus || sub == SubDecision
}

// IsHeadlessControl reports whether sub is a status or decision: what
// protocol.CapHeadless covers, apart from message controls (CapControl).
func IsHeadlessControl(sub string) bool { return sub == SubStatus || sub == SubDecision }

// Target names the one device that may execute a question or task (its
// own locally configured responder decides how; a sender selects no
// command, path or settings). Without a target, a question or task is
// addressed to the person and is never run by an agent.
type Target struct {
	Address        string `json:"address"`
	Fingerprint    string `json:"fingerprint"`
	AgentID        string `json:"agent_id,omitempty"`
	GroupAdmission string `json:"group_admission,omitempty"`
}

// AgentOrigin reports whether origin says an agent wrote the turn.
func AgentOrigin(origin string) bool { return strings.HasPrefix(origin, OriginAgentPrefix) }

// CheckQuote validates the quote rule shared by envelopes and retained history.
func CheckQuote(in Inner) error {
	if in.Quote != "" && (!validID(in.Quote) || in.Quote == in.ID || in.V == Version3 || in.Sub != "" || in.Status != "" || AgentOrigin(in.Origin) || (in.Kind != KindMessage && in.Kind != KindQuestion && in.Kind != KindTask)) {
		return errors.New("quote belongs only on a person's turn and must name another message")
	}
	return nil
}

// checkVersion2 validates the version 2 fields of in (or their absence in
// version 1).
func checkVersion2(in Inner) error {
	if err := CheckSendGroup(in); err != nil {
		return err
	}
	if err := CheckQuote(in); err != nil {
		return err
	}
	if err := CheckTopic(in); err != nil {
		return err
	}
	// Progress replies to one request in plain text: in version 1 (a named
	// executor's progress names it), or as a conversation participation's
	// nonterminal output with its PID (and its agent, when named).
	if in.Status == StatusProgress && (in.V != Version && (in.V != Version2 || in.PID == "") || in.Kind != KindMessage || in.ReplyTo == "" || strings.TrimSpace(in.Body) == "" ||
		len(in.Attachments) != 0 || in.Target != nil || in.ReceiverRoute != nil || in.Human != nil && in.V != Version2 || in.Sub != "") { // a participation's progress may carry its captured human audience (human.go)
		return errors.New("progress is a plain-text update replying to one request, in version 1 or as a participation's output")
	}
	// A proposal carries neither a display quote nor a topic completion hint.
	if in.Status == StatusProposal && (in.Kind != KindAnswer || in.ReplyTo == "" || strings.TrimSpace(in.Body) == "" || len(in.Attachments) != 0 ||
		in.Quote != "" || in.TopicDone || in.Target != nil || in.ReceiverRoute != nil || in.Sub != "" || in.V == Version3 || in.V == Version2 && in.PID == "") {
		return errors.New("a proposal is an answer carrying one plain-text task, replying to one request, in version 1 or as a participation's output")
	}
	if err := validateHumanInner(in); err != nil {
		return err
	}
	if err := ValidateReceiverRoute(in); err != nil {
		return err
	}
	if in.Target != nil && in.Target.GroupAdmission != "" {
		root, err := protocol.ParseConvRoot(in.Root)
		if err != nil || root.Kind != protocol.ConvKindGroup || in.PID == "" || !protocol.ValidHash(in.Target.GroupAdmission) || (in.Kind != KindQuestion && in.Kind != KindTask) {
			return errors.New("group: requester admission is only for a PID-addressed group request")
		}
	}
	if in.AgentID != "" && !AssistantReaction(in) && (!validID(in.AgentID) || (in.Kind != KindAnswer && in.Kind != KindResult && in.Status != StatusProgress) || in.ReplyTo == "" || in.Sub != "" || in.V == Version3) {
		return errors.New("a named agent author belongs on a reply answer, result or progress")
	}
	if in.Target != nil && in.Target.AgentID != "" && !validID(in.Target.AgentID) {
		return errors.New("invalid named agent target")
	}
	if in.V == Version3 {
		return checkVersion3(in)
	}
	if in.Ref != nil {
		return errors.New("a control ref belongs to a version 3 message")
	}
	if in.V != Version2 {
		if in.Conv != "" || in.LID != "" || len(in.Root) != 0 || in.Sub != "" || in.Replica || in.Origin != "" || in.Emotion != "" || in.PID != "" || in.Fan != nil {
			return errors.New("conversation fields in a version 1 message")
		}
		if t := in.Target; t != nil {
			if t.AgentID == "" || (in.Kind != KindQuestion && in.Kind != KindTask) || t.Address != in.To || !protocol.ValidFingerprint(t.Fingerprint) {
				return errors.New("a device message target must name an agent on its exact recipient")
			}
		}
		return nil
	}
	if in.Sub == SubReadSync || in.Sub == SubInvitationSync {
		if in.Conv != "" || in.LID != "" || len(in.Root) != 0 || in.Kind != KindMessage || !in.Replica || in.Target != nil || in.PID != "" || len(in.Attachments) != 0 || in.ReplyTo != "" || in.Origin != "" || in.Emotion != "" || in.Status != "" || in.Fan != nil || in.Human != nil || in.ReceiverRoute != nil || in.AgentID != "" || in.Topic != "" || in.TopicEvent != nil || in.TopicDone || in.Quote != "" || in.Session != "" || in.Fallback {
			return errors.New("read sync: quiet rootless reference carrier required")
		}
		if in.Sub == SubInvitationSync {
			_, err := protocol.ParseInvitationSync([]byte(in.Body))
			return err
		}
		_, err := protocol.ParseReadSync([]byte(in.Body))
		return err
	}
	if !protocol.ValidHash(in.Conv) || !validID(in.LID) {
		return errors.New("invalid conversation or logical id")
	}
	if len(in.Root) == 0 || len(in.Root) > protocol.ConvRootSizeLimit(in.Root) { // version-specific bound; the strict parser verifies
		return errors.New("missing or oversized conversation root")
	}
	switch in.Sub {
	case "", SubEvent, SubHistory, SubFile:
	case SubRootSync:
		root, err := protocol.ParseConvRoot(in.Root)
		if err != nil || root.Kind != protocol.ConvKindDM || root.ID() != in.Conv || in.Kind != KindMessage || !in.Replica || in.Body != `{"v":1}` || in.Target != nil || in.PID != "" || len(in.Attachments) != 0 || in.ReplyTo != "" || in.Origin != "" || in.Emotion != "" || in.Status != "" || in.Fan != nil || in.Human != nil || in.ReceiverRoute != nil {
			return errors.New("root sync: a quiet replica carries only one signed DM root")
		}
	case SubGroupProof, SubGroupContext, SubGroupInvite, SubGroupConsent, SubGroupWithdrawal:
		root, err := protocol.ParseConvRoot(in.Root)
		if err != nil || root.V != protocol.GroupRootVersion || root.Kind != protocol.ConvKindGroup || root.ID() != in.Conv || in.Kind != KindMessage || len(in.Attachments) != 1 || in.Target != nil || (in.PID != "" && (in.Sub != SubGroupProof && in.Sub != SubGroupContext || !protocol.ValidID(in.PID))) || in.ReplyTo != "" || in.Origin != "" || in.Emotion != "" || in.Status != "" || in.Fan != nil || in.Replica {
			return errors.New("group: carrier must be a plain group message with one attachment")
		}
		var carrier protocol.GroupCarrier
		dec := json.NewDecoder(strings.NewReader(in.Body))
		dec.DisallowUnknownFields()
		if dec.Decode(&carrier) != nil || dec.Decode(new(any)) != io.EOF || carrier.Validate() != nil {
			return errors.New("group: malformed carrier descriptor")
		}
		limit := int64(protocol.MaxBody - 1024)
		if in.Sub == SubGroupContext || in.Sub == SubGroupInvite || in.Sub == SubGroupConsent {
			limit = protocol.MaxGroupState
		}
		att := in.Attachments[0]
		if att.Name != in.Sub+".json" || att.Size <= 0 || att.Size > limit || att.Blob.Size > limit+(64<<10) {
			return errors.New("group: carrier attachment exceeds bound")
		}
	case SubExcerpt:
		if in.PID == "" || in.Kind != KindMessage || !in.Replica || in.Target != nil || in.ReplyTo != "" {
			return errors.New("a participation excerpt is a non-executing message replica naming its PID")
		}
	case SubDriveSpace:
		if in.Kind != KindMessage || in.Target != nil || in.PID != "" || len(in.Attachments) != 0 || in.ReplyTo != "" || in.Origin != "" || in.Emotion != "" || in.Status != "" {
			return errors.New("a Drive space record is a plain message carrying nothing else")
		}
		var sp gdrive.Space
		dec := json.NewDecoder(strings.NewReader(in.Body))
		dec.DisallowUnknownFields()
		if dec.Decode(&sp) != nil || dec.More() || sp.Validate() != nil || sp.Conv != in.Conv {
			return errors.New("malformed Drive space record")
		}
	default:
		return fmt.Errorf("unknown sub %q", in.Sub)
	}
	if in.Origin != "" && in.Origin != OriginUI && !(AgentOrigin(in.Origin) && validToken(strings.TrimPrefix(in.Origin, OriginAgentPrefix), 32)) {
		return fmt.Errorf("invalid origin %q", in.Origin)
	}
	if in.Emotion != "" && !ValidEmotion(in.Emotion) {
		return fmt.Errorf("invalid emotion %q", in.Emotion)
	}
	if err := checkFan(in.Fan); err != nil {
		return err
	}
	if t := in.Target; t != nil {
		if in.Kind != KindQuestion && in.Kind != KindTask {
			return errors.New("only a question or task has an execution target")
		}
		if _, _, err := protocol.SplitAddress(t.Address); err != nil || !protocol.ValidFingerprint(t.Fingerprint) {
			return errors.New("invalid execution target")
		}
	}
	// A participation id: on an event about it, on a request to its agent
	// (which then names its target), or on the agent's answer or result
	// (which has none). A target without one stays a request for the person.
	if in.PID != "" {
		if !validID(in.PID) {
			return errors.New("invalid participation id")
		}
		switch {
		case in.Sub == SubGroupProof || in.Sub == SubGroupContext:
			// The carrier branch above validates the bounded PID discriminator.
		case in.Sub == SubEvent:
		case in.Human != nil && in.Sub == "" && in.Kind == KindMessage:
		case in.Sub == SubExcerpt && in.Kind == KindMessage && in.Replica && in.Target == nil && in.ReplyTo == "":
		case in.Sub == "" && (in.Kind == KindQuestion || in.Kind == KindTask) && in.Target != nil:
		case in.Sub == "" && (in.Kind == KindAnswer || in.Kind == KindResult):
		case in.Sub == "" && in.Kind == KindMessage && in.Status == StatusProgress:
		default:
			return errors.New("a participation id belongs on an event, a request to the agent (with its target) or the agent's answer or result")
		}
	}
	return nil
}

// ValidEmotion reports whether s is a well-formed emotion: 1–24
// characters of [a-z0-9-]. What it names is the sender's choice.
func ValidEmotion(s string) bool { return validToken(s, 24) }

// validToken reports whether s is 1–max characters of [a-z0-9-].
func validToken(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ErrNotForMe means the envelope is addressed to another agent.
var ErrNotForMe = errors.New("envelope addressed to another agent")

// signed is what the sender signs: a domain line for the envelope's
// version, then the envelope without its signature as JSON.
func (e Envelope) signed() []byte {
	domain := "agentnet-envelope-v1\n"
	switch e.V {
	case Version2:
		domain = "agentnet-envelope-v2\n"
	case Version3:
		domain = "agentnet-envelope-v3\n"
	}
	e.Sig = nil
	data, _ := json.Marshal(e)
	return append([]byte(domain), data...)
}

// Seal encrypts in to recipient and signs the envelope with the sender key.
// in.V selects the version: Version2 for a conversation message, Version3
// for a control, 0 or Version for version 1; any other value is refused,
// never silently sent as version 1. An agent-origin version 2 turn must
// carry an emotion.
func Seal(in Inner, sender ed25519.PrivateKey, recipient age.Recipient) (Envelope, error) {
	return sealEnvelope(in, sender, recipient, "")
}

// SealAttention is Seal for a version 2 turn that asks for the recipient's
// attention on its notification channel.
func SealAttention(in Inner, sender ed25519.PrivateKey, recipient age.Recipient, channel string) (Envelope, error) {
	if in.V != Version2 || !protocol.ValidNotifyChannel(channel) {
		return Envelope{}, errors.New("attention needs a version 2 message and a notification channel")
	}
	return sealEnvelope(in, sender, recipient, channel)
}

func sealEnvelope(in Inner, sender ed25519.PrivateKey, recipient age.Recipient, channel string) (Envelope, error) {
	if (in.Sub == SubRootSync || in.Sub == SubReadSync || in.Sub == SubInvitationSync) && channel != "" {
		return Envelope{}, errors.New("root sync carries no attention")
	}
	if !validKind(in.Kind) {
		return Envelope{}, fmt.Errorf("unknown message kind %q", in.Kind)
	}
	switch in.V {
	case 0:
		in.V = Version
	case Version, Version2, Version3:
	default:
		return Envelope{}, fmt.Errorf("unsupported envelope version %d", in.V)
	}
	if err := checkVersion2(in); err != nil {
		return Envelope{}, err
	}
	if in.V == Version2 && AgentOrigin(in.Origin) && in.Sub == "" && in.Emotion == "" {
		return Envelope{}, errors.New("an agent's turn must carry an emotion")
	}
	plain, err := json.Marshal(in)
	if err != nil {
		return Envelope{}, err
	}
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, recipient)
	if err != nil {
		return Envelope{}, err
	}
	if _, err := w.Write(plain); err != nil {
		return Envelope{}, err
	}
	if err := w.Close(); err != nil {
		return Envelope{}, err
	}
	if ct.Len() > MaxCiphertext {
		return Envelope{}, fmt.Errorf("message too large (%d bytes encrypted, max %d)", ct.Len(), MaxCiphertext)
	}
	env := Envelope{V: in.V, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind, CT: ct.Bytes(),
		Session: in.Session, Fallback: in.Fallback, Attn: channel != "", Chan: channel}
	for _, a := range in.Attachments {
		env.Blobs = append(env.Blobs, a.Blob)
	}
	env.Sig = ed25519.Sign(sender, env.signed())
	return env, nil
}

// VerifySig checks the outer signature and shape; the Hub calls this.
func (e Envelope) VerifySig(senderKey ed25519.PublicKey) error {
	if e.V != Version && e.V != Version2 && e.V != Version3 {
		return fmt.Errorf("unsupported envelope version %d", e.V)
	}
	if e.ID == "" || e.From == "" || e.To == "" || e.Kind == "" || len(e.CT) == 0 {
		return errors.New("incomplete envelope")
	}
	if !validKind(e.Kind) {
		return fmt.Errorf("unknown message kind %q", e.Kind)
	}
	if len(e.CT) > MaxCiphertext {
		return errors.New("envelope too large")
	}
	if len(e.Blobs) > MaxAttachments {
		return fmt.Errorf("too many attachments (max %d)", MaxAttachments)
	}
	if e.Session != "" && !validID(e.Session) {
		return errors.New("invalid session id")
	}
	if e.Attn != (e.Chan != "") || (e.Attn && (e.V != Version2 || !protocol.ValidNotifyChannel(e.Chan))) {
		return errors.New("invalid attention hint")
	}
	seen := map[string]bool{}
	for _, b := range e.Blobs {
		if !validID(b.ID) || b.Size <= 0 || len(b.SHA256) != 64 || seen[b.ID] {
			return errors.New("invalid attachment reference")
		}
		seen[b.ID] = true
	}
	if !ed25519.Verify(senderKey, e.signed(), e.Sig) {
		return errors.New("envelope signature invalid")
	}
	return nil
}

// Open verifies the envelope against the trusted sender entry, decrypts it
// with self, and checks the encrypted fields match the signed outer ones.
func Open(e Envelope, self *identity.Identity, selfAddress string, sender identity.Public) (Inner, error) {
	var in Inner
	if e.To != selfAddress {
		return in, ErrNotForMe
	}
	if e.From != sender.Address {
		return in, errors.New("sender key does not belong to envelope sender")
	}
	if err := e.VerifySig(sender.SignKey); err != nil {
		return in, err
	}
	r, err := age.Decrypt(bytes.NewReader(e.CT), self.Box)
	if err != nil {
		return in, fmt.Errorf("decrypt: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, MaxCiphertext))
	if err != nil {
		return in, fmt.Errorf("decrypt: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("inner: %w", err)
	}
	if in.V != e.V || in.ID != e.ID || in.From != e.From || in.To != e.To || in.TS != e.TS || in.Kind != e.Kind ||
		in.Session != e.Session || in.Fallback != e.Fallback {
		return in, errors.New("encrypted header does not match signed envelope")
	}
	if err := checkVersion2(in); err != nil {
		return in, err
	}
	if (in.Sub == SubRootSync || in.Sub == SubReadSync || in.Sub == SubInvitationSync) && e.Attn {
		return in, errors.New("root sync carries no attention")
	}
	if len(in.Attachments) != len(e.Blobs) {
		return in, errors.New("encrypted manifest does not match signed attachments")
	}
	for i, a := range in.Attachments {
		if a.Blob != e.Blobs[i] || a.Size < 0 || len(a.SHA256) != 64 {
			return in, errors.New("encrypted manifest does not match signed attachments")
		}
	}
	return in, nil
}

// validID reports whether s is a 128-bit lowercase hex identifier.
func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
