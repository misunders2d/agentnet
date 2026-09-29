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

	"filippo.io/age"

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
	Sig      []byte `json:"sig,omitempty"`
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

	// StatusReviewNotice on a plain message with no reply_to and no files
	// says only that items wait for a person on the sender's machine. It is
	// content-free and grants nothing: the recipient files it for its own
	// person's attention and never runs, accepts or forwards it. Clients
	// before it store such a message as an ordinary one.
	StatusReviewNotice = "review_notice"
)

// Version 2 inner values.
const (
	SubEvent   = "event"   // a conversation event; history only, never a request
	SubExcerpt = "excerpt" // shared history; never a request
	OriginUI   = "ui"      // typed by a person, as the sending device asserts
	// OriginAgentPrefix starts "agent:<harness>", written by an agent.
	OriginAgentPrefix = "agent:"
)

// Target names the one device that may execute a question or task (its
// own locally configured responder decides how; a sender selects no
// command, path or settings). Without a target, a question or task is
// addressed to the person and is never run by an agent.
type Target struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// AgentOrigin reports whether origin says an agent wrote the turn.
func AgentOrigin(origin string) bool { return strings.HasPrefix(origin, OriginAgentPrefix) }

// checkVersion2 validates the version 2 fields of in (or their absence in
// version 1).
func checkVersion2(in Inner) error {
	if in.V != Version2 {
		if in.Conv != "" || in.LID != "" || len(in.Root) != 0 || in.Sub != "" || in.Replica || in.Origin != "" || in.Emotion != "" || in.Target != nil || in.PID != "" {
			return errors.New("conversation fields in a version 1 message")
		}
		return nil
	}
	if !protocol.ValidHash(in.Conv) || !validID(in.LID) {
		return errors.New("invalid conversation or logical id")
	}
	if len(in.Root) == 0 || len(in.Root) > protocol.MaxConvRoot {
		return errors.New("missing or oversized conversation root")
	}
	switch in.Sub {
	case "", SubEvent, SubExcerpt:
	default:
		return fmt.Errorf("unknown sub %q", in.Sub)
	}
	if in.Origin != "" && in.Origin != OriginUI && !(AgentOrigin(in.Origin) && validToken(strings.TrimPrefix(in.Origin, OriginAgentPrefix), 32)) {
		return fmt.Errorf("invalid origin %q", in.Origin)
	}
	if in.Emotion != "" && !validToken(in.Emotion, 24) {
		return fmt.Errorf("invalid emotion %q", in.Emotion)
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
		case in.Sub == SubEvent:
		case in.Sub == "" && (in.Kind == KindQuestion || in.Kind == KindTask) && in.Target != nil:
		case in.Sub == "" && (in.Kind == KindAnswer || in.Kind == KindResult):
		default:
			return errors.New("a participation id belongs on an event, a request to the agent (with its target) or the agent's answer or result")
		}
	}
	return nil
}

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
	if e.V == Version2 {
		domain = "agentnet-envelope-v2\n"
	}
	e.Sig = nil
	data, _ := json.Marshal(e)
	return append([]byte(domain), data...)
}

// Seal encrypts in to recipient and signs the envelope with the sender key.
// in.V selects the version: Version2 for a conversation message, anything
// else version 1. An agent-origin version 2 turn must carry an emotion.
func Seal(in Inner, sender ed25519.PrivateKey, recipient age.Recipient) (Envelope, error) {
	if !validKind(in.Kind) {
		return Envelope{}, fmt.Errorf("unknown message kind %q", in.Kind)
	}
	if in.V != Version2 {
		in.V = Version
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
		Session: in.Session, Fallback: in.Fallback}
	for _, a := range in.Attachments {
		env.Blobs = append(env.Blobs, a.Blob)
	}
	env.Sig = ed25519.Sign(sender, env.signed())
	return env, nil
}

// VerifySig checks the outer signature and shape; the Hub calls this.
func (e Envelope) VerifySig(senderKey ed25519.PublicKey) error {
	if e.V != Version && e.V != Version2 {
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
