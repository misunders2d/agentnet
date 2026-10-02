package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Agent participation in a DM (S-D1, first checkpoint: records only).
//
// A participation brings an agent into a DM. The agent is the one hosted on
// a DM member's installation (the host device, a device of a member
// person's roster); its own locally configured responder would run it. A
// participation is a set of signed events, each carried inside a DM message
// (sub "event") and signed by its author's device:
//
//   - invite: by a member's device. It names the host, the earlier messages
//     of the DM that may be given to the agent (Grant: each exactly one
//     message, by its logical id and its sender's key, the pair a message is
//     admitted under; nothing else earlier), and the member keys that may
//     ask it for follow-up tasks within this participation only (TaskKeys).
//   - accept or decline: by the host device only, answering one invite
//     (Prev is that invite's hash), after the host's person decided. An
//     accept is all-or-nothing consent to exactly that invite's scope; a
//     host who wants less declines and, if it likes, invites its own agent
//     with the narrower scope (a counter-invite). Nothing widens a scope.
//   - dismiss: by either member person's device (owner decision
//     2026-09-29), naming an event of the participation it ends (Prev);
//     it ends it for good. A later invite is a new participation.
//
// The state is resolved from the set of events held, never from their
// arrival order (see the client).
//
// Canonical bytes: "agentnet-participation-v1\n" followed by the JSON
// encoding (encoding/json, field order below) of the event without its
// signature; the event hash is the lowercase hex SHA-256 of those bytes.

// ParticipationDomain starts a participation event's canonical bytes.
const ParticipationDomain = "agentnet-participation-v1\n"

// CapExternalParticipation covers exact non-member DM hosts and PID-bound
// claimed excerpts. Every active session must advertise it; agi1 is separate.
const CapExternalParticipation = "apx1"

// Participation event types.
const (
	EventInvite  = "invite"
	EventAccept  = "accept"
	EventDecline = "decline"
	EventDismiss = "dismiss"
)

// AudienceConversation: the agent's outputs go to the DM (its members).
const AudienceConversation = "conversation"

// Bounds of a participation event.
const (
	MaxGrant              = 200  // message references
	MaxTaskKeys           = 16   // member key fingerprints
	MaxInviteNote         = 1024 // bytes
	MaxParticipationEvent = 8192 // the signed event as JSON
)

// EventAuthor is the device (and its person) that signed an event.
type EventAuthor struct {
	Person         string `json:"person"`
	Roster         string `json:"roster"`
	Address        string `json:"address"`
	Fingerprint    string `json:"fingerprint"`
	GroupAdmission string `json:"group_admission,omitempty"`
}

// ParticipationGroup binds the original group invitation to signed epochs.
// Current authority/context travels separately in bounded existing carriers.
type ParticipationGroup struct {
	Seq            int64    `json:"seq"`
	Hash           string   `json:"hash"`
	HostRole       string   `json:"host_role"`
	HostAdmission  string   `json:"host_admission,omitempty"`
	TaskAdmissions []string `json:"task_admissions,omitempty"`
}

// GrantRef names one earlier message of the DM exactly: its logical id and
// the key fingerprint of the device that sent it.
type GrantRef struct {
	LID         string `json:"lid"`
	Fingerprint string `json:"fingerprint"`
}

// ParticipationHost is the device that would run the agent, and its person.
type ParticipationHost struct {
	Person      string `json:"person"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	AgentID     string `json:"agent_id,omitempty"`
}

// ParticipationEvent is one signed step of a participation.
type ParticipationEvent struct {
	V      int         `json:"v"`
	Conv   string      `json:"conv"`
	PID    string      `json:"pid"`
	Type   string      `json:"type"`
	Prev   string      `json:"prev"` // "" for an invite; the invite's hash for accept and decline; an event of it for dismiss
	Author EventAuthor `json:"author"`
	TS     int64       `json:"ts"` // the author's claim; never used for ordering

	// Invite only.
	Host     *ParticipationHost  `json:"host,omitempty"`
	Grant    []GrantRef          `json:"grant,omitempty"`     // earlier messages of this DM the agent may be given
	Audience string              `json:"audience,omitempty"`  // AudienceConversation
	TaskKeys []string            `json:"task_keys,omitempty"` // member key fingerprints allowed follow-up tasks here
	Note     string              `json:"note,omitempty"`      // shown to the host's person
	Group    *ParticipationGroup `json:"group,omitempty"`

	Sig []byte `json:"sig,omitempty"`
}

// Canonical returns the bytes the author signs.
func (e ParticipationEvent) Canonical() []byte {
	e.Sig = nil
	data, _ := json.Marshal(e)
	return append([]byte(ParticipationDomain), data...)
}

// Hash identifies the event.
func (e ParticipationEvent) Hash() string { return hashHex(e.Canonical()) }

// Sign signs e with the author device's key.
func (e *ParticipationEvent) Sign(key ed25519.PrivateKey) { e.Sig = ed25519.Sign(key, e.Canonical()) }

// Validate checks the shape and bounds of e (not its signature, and not
// whether its author, host or keys belong to the DM: that needs the DM's
// pinned persons).
func (e ParticipationEvent) Validate() error {
	if e.V != 1 || !ValidHash(e.Conv) || !ValidID(e.PID) || e.TS <= 0 {
		return errors.New("participation: invalid event")
	}
	a := e.Author
	if !ValidID(a.Person) || !ValidHash(a.Roster) || !ValidFingerprint(a.Fingerprint) {
		return errors.New("participation: invalid author")
	}
	if a.GroupAdmission != "" && !ValidHash(a.GroupAdmission) {
		return errors.New("participation: invalid author group admission")
	}
	if _, _, err := SplitAddress(a.Address); err != nil {
		return fmt.Errorf("participation: %w", err)
	}
	switch e.Type {
	case EventInvite:
		if e.Prev != "" || e.Host == nil || e.Audience != AudienceConversation {
			return errors.New("participation: an invite has no prev, and names a host and the conversation audience")
		}
		h := e.Host
		if !ValidID(h.Person) || !ValidFingerprint(h.Fingerprint) || (h.AgentID != "" && !ValidAgentID(h.AgentID)) {
			return errors.New("participation: invalid host")
		}
		if _, _, err := SplitAddress(h.Address); err != nil {
			return fmt.Errorf("participation: host: %w", err)
		}
		refs := make([]string, 0, len(e.Grant))
		for _, g := range e.Grant {
			if !ValidID(g.LID) || !ValidFingerprint(g.Fingerprint) {
				return errors.New("participation: grant: invalid message reference")
			}
			refs = append(refs, g.LID+"/"+g.Fingerprint)
		}
		if err := uniqueValid(refs, MaxGrant, func(string) bool { return true }); err != nil {
			return fmt.Errorf("participation: grant: %w", err)
		}
		if err := uniqueValid(e.TaskKeys, MaxTaskKeys, ValidFingerprint); err != nil {
			return fmt.Errorf("participation: task keys: %w", err)
		}
		if g := e.Group; g != nil {
			if g.Seq < 0 || !ValidHash(g.Hash) || !ValidHash(a.GroupAdmission) || (g.HostRole != "member" && g.HostRole != "visitor") || (g.HostRole == "member" && !ValidHash(g.HostAdmission)) || (g.HostRole == "visitor" && g.HostAdmission != "") || len(g.TaskAdmissions) != len(e.TaskKeys) {
				return errors.New("participation: malformed group invitation scope")
			}
			for _, epoch := range g.TaskAdmissions {
				if !ValidHash(epoch) {
					return errors.New("participation: invalid task group admission")
				}
			}
		}
		if len(e.Note) > MaxInviteNote || !utf8.ValidString(e.Note) {
			return errors.New("participation: note too long or not text")
		}
		for _, c := range e.Note {
			if !unicode.IsPrint(c) && c != '\n' {
				return errors.New("participation: note has a control character")
			}
		}
	case EventAccept, EventDecline, EventDismiss:
		if !ValidHash(e.Prev) || e.Host != nil || e.Grant != nil || e.Audience != "" || e.TaskKeys != nil || e.Note != "" || e.Group != nil {
			return errors.New("participation: an accept, decline or dismiss names only the event it follows")
		}
	default:
		return fmt.Errorf("participation: unknown event type %q", e.Type)
	}
	return nil
}

// Verify checks e and that authorKey, the author device's key, signed it.
func (e ParticipationEvent) Verify(authorKey ed25519.PublicKey) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if len(authorKey) != ed25519.PublicKeySize || !ed25519.Verify(authorKey, e.Canonical(), e.Sig) {
		return errors.New("participation: signature invalid")
	}
	return nil
}

// ParseParticipationEvent decodes a signed event strictly and checks its shape.
func ParseParticipationEvent(data []byte) (ParticipationEvent, error) {
	var e ParticipationEvent
	if len(data) > MaxParticipationEvent {
		return e, errors.New("participation: event too large")
	}
	if err := decodeStrictJSON(data, &e); err != nil {
		return e, fmt.Errorf("participation: %w", err)
	}
	return e, e.Validate()
}

func uniqueValid(items []string, max int, valid func(string) bool) error {
	if len(items) > max {
		return fmt.Errorf("more than %d", max)
	}
	seen := make(map[string]bool, len(items))
	for _, s := range items {
		if !valid(s) || seen[s] {
			return fmt.Errorf("invalid or repeated %q", s)
		}
		seen[s] = true
	}
	return nil
}
