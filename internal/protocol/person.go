package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Signed records for human DMs: a person's roster, a conversation's root
// (E0) and a device's capabilities. Each is signed by one device key over
// canonical bytes: a domain line, then the JSON encoding (encoding/json, the
// field order of the struct below) of the record without its signature.
// That encoding has no spaces, keeps other UTF-8 as is, and escapes <, >
// and & as \u003c, \u003e and \u0026; another implementation must produce
// the same bytes (person_test.go has exact vectors). Hashes are the
// lowercase hex SHA-256 of those same bytes.

// Person roster (this checkpoint: one device, no transitions).
//
// A person is created explicitly on one device and never inferred from an
// enrollment, address, label or migration. The record names a stable random
// person id, a display label that is only the person's own claim, and the
// one device (address and key fingerprint) that speaks for it; that
// device's key signs it.
//
// Canonical bytes:
//
//	agentnet-person-v1\n{"person":"<32 hex>","label":"<label>","seq":0,"prev":"",
//	"devices":[{"address":"<person/agent>","fingerprint":"<fingerprint>"}]}
//
// (one line; JSON string escaping as encoding/json does it). Limits: seq 0
// and prev "" (linking, retirement and recovery will append transitions);
// exactly one device; a label of 1–64 bytes of printable UTF-8; the signed
// record at most MaxPersonRecord bytes. A different record for a person id
// or an address that is already pinned is a conflict, and is frozen.

// PersonDomain starts a person roster's canonical bytes.
const PersonDomain = "agentnet-person-v1\n"

// Bounds of a person record.
const (
	MaxPersonLabel  = 64
	MaxPersonRecord = 768 // the signed record as JSON (a worst-case roster is about 700)
)

// RosterDevice is a device that speaks for a person.
type RosterDevice struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// PersonRoster is a person's signed roster.
type PersonRoster struct {
	Person  string         `json:"person"`
	Label   string         `json:"label"`
	Seq     int64          `json:"seq"`
	Prev    string         `json:"prev"`
	Devices []RosterDevice `json:"devices"`
	Sig     []byte         `json:"sig,omitempty"`
}

// Canonical returns the bytes the device signs.
func (r PersonRoster) Canonical() []byte {
	r.Sig = nil
	data, _ := json.Marshal(r)
	return append([]byte(PersonDomain), data...)
}

// Hash identifies this exact roster.
func (r PersonRoster) Hash() string { return hashHex(r.Canonical()) }

// Sign signs r with the key of its one device.
func (r *PersonRoster) Sign(key ed25519.PrivateKey) { r.Sig = ed25519.Sign(key, r.Canonical()) }

// Validate checks the shape and bounds of r (not its signature).
func (r PersonRoster) Validate() error {
	if !ValidID(r.Person) {
		return errors.New("person: invalid id")
	}
	if err := validLabel(r.Label); err != nil {
		return err
	}
	if r.Seq != 0 || r.Prev != "" {
		return errors.New("person: only a first roster (seq 0) is supported")
	}
	if len(r.Devices) != 1 {
		return errors.New("person: exactly one device is supported")
	}
	if _, _, err := SplitAddress(r.Devices[0].Address); err != nil {
		return fmt.Errorf("person: %w", err)
	}
	if !ValidFingerprint(r.Devices[0].Fingerprint) {
		return errors.New("person: invalid device fingerprint")
	}
	return nil
}

// Verify checks r and that signKey, the device's key, signed it.
func (r PersonRoster) Verify(signKey ed25519.PublicKey) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if len(signKey) != ed25519.PublicKeySize || !ed25519.Verify(signKey, r.Canonical(), r.Sig) {
		return errors.New("person: signature invalid")
	}
	return nil
}

// ParsePersonRoster decodes a signed roster strictly and checks its shape.
func ParsePersonRoster(data []byte) (PersonRoster, error) {
	var r PersonRoster
	if len(data) > MaxPersonRecord {
		return r, errors.New("person: record too large")
	}
	if err := decodeStrictJSON(data, &r); err != nil {
		return r, fmt.Errorf("person: %w", err)
	}
	return r, r.Validate()
}

func validLabel(s string) error {
	if s == "" || len(s) > MaxPersonLabel || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return fmt.Errorf("person: label must be 1-%d bytes of text without surrounding spaces", MaxPersonLabel)
	}
	for _, c := range s {
		if !unicode.IsPrint(c) {
			return errors.New("person: label has a control character")
		}
	}
	return nil
}

// Conversation root (E0) of a DM.
//
// The creator's device makes and signs it; its members are two persons,
// each bound to the hash of the roster the creator had pinned. The
// conversation id is the hash of the canonical bytes, which therefore do
// not contain it: a conversation cannot be claimed without its root, and two
// DMs with the same person differ by their random nonce.
//
// Canonical bytes:
//
//	agentnet-conv-root-v1\n{"v":1,"kind":"dm","creator":{"person":"<32 hex>",
//	"roster":"<64 hex>","address":"<person/agent>","fingerprint":"<fp>"},
//	"members":[{"person":"<32 hex>","roster":"<64 hex>"},{…}],
//	"nonce":"<32 hex>","created":<unix seconds>}
//
// Members are sorted by person id and distinct; the creator's person is one
// of them with the same roster hash. The signed root is at most MaxConvRoot
// bytes. A DM never changes members: there are no later epochs here.

// ConvRootDomain starts a conversation root's canonical bytes.
const ConvRootDomain = "agentnet-conv-root-v1\n"

// MaxConvRoot bounds a signed conversation root as JSON.
const MaxConvRoot = 2048

// ConvKindDM is a two-person conversation.
const ConvKindDM = "dm"

// ConvCreator is the device that created a conversation, and its person.
type ConvCreator struct {
	Person      string `json:"person"`
	Roster      string `json:"roster"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// ConvMember is a person member and the roster it was bound to.
type ConvMember struct {
	Person string `json:"person"`
	Roster string `json:"roster"`
}

// ConvRoot is a conversation's signed root (E0).
type ConvRoot struct {
	V       int          `json:"v"`
	Kind    string       `json:"kind"`
	Creator ConvCreator  `json:"creator"`
	Members []ConvMember `json:"members"`
	Nonce   string       `json:"nonce"`
	Created int64        `json:"created"`
	Sig     []byte       `json:"sig,omitempty"`
}

// Canonical returns the bytes the creator signs; ID is their hash.
func (c ConvRoot) Canonical() []byte {
	c.Sig = nil
	data, _ := json.Marshal(c)
	return append([]byte(ConvRootDomain), data...)
}

// ID is the conversation id.
func (c ConvRoot) ID() string { return hashHex(c.Canonical()) }

// Sign signs c with the creator device's key.
func (c *ConvRoot) Sign(key ed25519.PrivateKey) { c.Sig = ed25519.Sign(key, c.Canonical()) }

// Member reports whether person is a member, and the roster it is bound to.
func (c ConvRoot) Member(person string) (roster string, ok bool) {
	for _, m := range c.Members {
		if m.Person == person {
			return m.Roster, true
		}
	}
	return "", false
}

// Validate checks the shape of c (not its signature or rosters).
func (c ConvRoot) Validate() error {
	if c.V != 1 || c.Kind != ConvKindDM {
		return errors.New("conversation: unsupported root")
	}
	if !ValidID(c.Creator.Person) || !ValidHash(c.Creator.Roster) || !ValidFingerprint(c.Creator.Fingerprint) {
		return errors.New("conversation: invalid creator")
	}
	if _, _, err := SplitAddress(c.Creator.Address); err != nil {
		return fmt.Errorf("conversation: %w", err)
	}
	if len(c.Members) != 2 {
		return errors.New("conversation: a DM has two members")
	}
	for _, m := range c.Members {
		if !ValidID(m.Person) || !ValidHash(m.Roster) {
			return errors.New("conversation: invalid member")
		}
	}
	if c.Members[0].Person >= c.Members[1].Person {
		return errors.New("conversation: members must be distinct and sorted")
	}
	if roster, ok := c.Member(c.Creator.Person); !ok || roster != c.Creator.Roster {
		return errors.New("conversation: the creator is not a member with that roster")
	}
	if !ValidID(c.Nonce) || c.Created <= 0 {
		return errors.New("conversation: invalid nonce or time")
	}
	return nil
}

// Verify checks c and that creatorKey, the creator device's key, signed it.
func (c ConvRoot) Verify(creatorKey ed25519.PublicKey) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(creatorKey) != ed25519.PublicKeySize || !ed25519.Verify(creatorKey, c.Canonical(), c.Sig) {
		return errors.New("conversation: root signature invalid")
	}
	return nil
}

// ParseConvRoot decodes a signed root strictly and checks its shape.
func ParseConvRoot(data []byte) (ConvRoot, error) {
	var c ConvRoot
	if len(data) > MaxConvRoot {
		return c, errors.New("conversation: root too large")
	}
	if err := decodeStrictJSON(data, &c); err != nil {
		return c, fmt.Errorf("conversation: %w", err)
	}
	return c, c.Validate()
}

// Capability record of one device session.
//
// Canonical bytes:
//
//	agentnet-caps-v1\n{"address":"<person/agent>","session":"<32 hex>",
//	"caps":["env2"],"ts":<unix seconds>}
//
// At most MaxCaps entries of 1–32 characters [a-z0-9-], sorted and unique;
// the signed record at most MaxCapsRecord bytes. Each daemon session
// publishes its own; the relay keeps the newest per (device, session).
// A relay can withhold a record but cannot forge one; which sessions are
// live is only the relay's statement (see Profile).

// CapsDomain starts a capability record's canonical bytes.
const CapsDomain = "agentnet-caps-v1\n"

// CapEnv2 means the device reads envelope version 2 (conversations).
const CapEnv2 = "env2"

// Bounds of a capability record.
const (
	MaxCaps       = 16
	MaxCapsRecord = 1024
)

// CapsRecord lists what one device session can read.
type CapsRecord struct {
	Address string   `json:"address"`
	Session string   `json:"session"`
	Caps    []string `json:"caps"`
	TS      int64    `json:"ts"`
	Sig     []byte   `json:"sig,omitempty"`
}

// Canonical returns the bytes the device signs.
func (c CapsRecord) Canonical() []byte {
	c.Sig = nil
	data, _ := json.Marshal(c)
	return append([]byte(CapsDomain), data...)
}

// Sign signs c with the device's key.
func (c *CapsRecord) Sign(key ed25519.PrivateKey) { c.Sig = ed25519.Sign(key, c.Canonical()) }

// Has reports whether c lists capability name.
func (c CapsRecord) Has(name string) bool { return slices.Contains(c.Caps, name) }

// Validate checks the shape of c.
func (c CapsRecord) Validate() error {
	if _, _, err := SplitAddress(c.Address); err != nil {
		return fmt.Errorf("caps: %w", err)
	}
	if !ValidID(c.Session) || c.TS <= 0 || len(c.Caps) > MaxCaps {
		return errors.New("caps: invalid record")
	}
	for i, name := range c.Caps {
		if !validToken(name, 32) || (i > 0 && c.Caps[i-1] >= name) {
			return errors.New("caps: names must be sorted, unique [a-z0-9-] tokens")
		}
	}
	return nil
}

// Verify checks c and that signKey, the device's key, signed it.
func (c CapsRecord) Verify(signKey ed25519.PublicKey) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(signKey) != ed25519.PublicKeySize || !ed25519.Verify(signKey, c.Canonical(), c.Sig) {
		return errors.New("caps: signature invalid")
	}
	return nil
}

// ParseCapsRecord decodes a signed record strictly and checks its shape.
func ParseCapsRecord(data []byte) (CapsRecord, error) {
	var c CapsRecord
	if len(data) > MaxCapsRecord {
		return c, errors.New("caps: record too large")
	}
	if err := decodeStrictJSON(data, &c); err != nil {
		return c, fmt.Errorf("caps: %w", err)
	}
	return c, c.Validate()
}

// Profile is what the relay holds for one device (GET
// /v1/agents/{label}/{agent}/profile): its published person roster, and the
// capability records of the sessions that decide what it can read, as the
// device signed them.
//
// Sessions is the relay's statement, not signed by the device: the sessions
// that are live now (connected or within reconnect grace), or, when none is,
// the session that connected last (kept across relay restarts, so an offline
// device keeps what it last supported). A device supports a capability only
// if every listed session has a record with it: concurrent sessions of old
// and new programs count as the least capable. Signatures prove each record,
// not the relay's freshness claim.
type Profile struct {
	Person   json.RawMessage   `json:"person,omitempty"`
	Sessions []string          `json:"sessions,omitempty"`
	Live     bool              `json:"live"` // Sessions are live now (else: the last one)
	Caps     []json.RawMessage `json:"caps,omitempty"`
}

// Supports reports whether the device with signKey at address supports
// capability name by p: there are sessions, and each has a record that
// verifies, names that device and session, and lists name.
func (p Profile) Supports(address string, signKey ed25519.PublicKey, name string) bool {
	if len(p.Sessions) == 0 {
		return false
	}
	has := map[string]bool{}
	for _, raw := range p.Caps {
		c, err := ParseCapsRecord(raw)
		if err != nil || c.Verify(signKey) != nil || c.Address != address {
			continue
		}
		if c.Has(name) {
			has[c.Session] = true
		}
	}
	for _, s := range p.Sessions {
		if !has[s] {
			return false
		}
	}
	return true
}

// Relay features listed by GET /v1/version. An older relay lists none.
const (
	FeatureMembers = "members" // GET /v1/agents and the members push
	FeatureEnv2    = "env2"    // accepts envelope version 2
	FeaturePerson  = "person"  // PUT /v1/person, profiles, person in member lists
	FeatureCaps    = "caps"    // PUT /v1/caps, profiles
)

// ValidHash reports whether s is a lowercase hex SHA-256.
func ValidHash(s string) bool { return len(s) == 64 && isLowerHex(s) }

// ValidFingerprint reports whether s has the form of a key fingerprint
// (identity.Public.Fingerprint): four groups of 8 lowercase hex digits.
func ValidFingerprint(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) != 8 || !isLowerHex(p) {
			return false
		}
	}
	return true
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func decodeStrictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}
