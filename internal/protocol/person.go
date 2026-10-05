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

	"github.com/misunders2d/agentnet/internal/identity"
)

// Signed records for human DMs: a person's roster, a conversation's root
// (E0) and a device's capabilities. Each is signed by one device key over
// canonical bytes: a domain line, then the JSON encoding (encoding/json, the
// field order of the struct below) of the record without its signatures.
// That encoding has no spaces, keeps other UTF-8 as is, and escapes <, >
// and & as \u003c, \u003e and \u0026; another implementation must produce
// the same bytes (person_test.go has exact vectors). Hashes are the
// lowercase hex SHA-256 of those same bytes.

// Person roster (version 2).
//
// A person is created explicitly on one device and never inferred from an
// enrollment, address, label or migration. Its roster names a stable random
// person id, a display label that is only the person's own claim, and the
// devices that speak for it, each by its public directory entry (address
// and keys), so old roots, events and history stay verifiable after a
// device is removed or revoked. Rosters form a chain:
//
//   - seq 0: exactly one device, signed by it; no by, no join.
//   - seq n > 0: prev is the hash of seq n-1; by is the fingerprint of the
//     device of seq n-1 that signed it. At most one device is added per
//     step; an added device consents with join, its signature over
//     JoinBytes (the person, seq, prev and its own entry), which it can make
//     before the roster exists. Removing devices needs no join. A device
//     keeps its address and keys: anything else is a removal and an
//     addition.
//
// Canonical bytes (one line; sig and join are not part of them):
//
//	agentnet-person-v2\n{"person":"<32 hex>","label":"<label>","seq":<n>,"prev":"<64 hex or empty>",
//	"devices":[{"address":…,"sign_key":…,"box_recipient":…,"box_sig":…},…],"by":"<fingerprint, omitted at seq 0>"}
//
// Limits: 1–MaxPersonDevices devices with distinct addresses and keys; a
// label of 1–64 bytes of printable UTF-8; the signed record at most
// MaxPersonRecord bytes. A different record for a person id at a seq already
// pinned is a conflict, and freezes the person.

// PersonDomain starts a person roster's canonical bytes.
const PersonDomain = "agentnet-person-v2\n"

// PersonJoinDomain starts the bytes an added device signs to join.
const PersonJoinDomain = "agentnet-person-join-v2\n"

// Bounds of a person record.
const (
	MaxPersonLabel   = 64
	MaxPersonDevices = 8
	MaxPersonRecord  = 4096 // the signed record as JSON (8 devices are about 3.3 KiB)
)

// PersonRoster is one signed step of a person's roster chain.
type PersonRoster struct {
	Person  string            `json:"person"`
	Label   string            `json:"label"`
	Seq     int64             `json:"seq"`
	Prev    string            `json:"prev"`
	Devices []identity.Public `json:"devices"`
	By      string            `json:"by,omitempty"`
	Sig     []byte            `json:"sig,omitempty"`
	Join    []byte            `json:"join,omitempty"`
	// Email is set only by Google enrollment. It is signed and immutable
	// along the roster chain; omitted on existing invite-code persons.
	Email string `json:"email,omitempty"`
}

// Canonical returns the bytes the signing device signs.
func (r PersonRoster) Canonical() []byte {
	r.Sig, r.Join = nil, nil
	data, _ := json.Marshal(r)
	return append([]byte(PersonDomain), data...)
}

// Hash identifies this exact roster.
func (r PersonRoster) Hash() string { return hashHex(r.Canonical()) }

// Sign signs r with the key of the device r.By names (seq 0: its device).
func (r *PersonRoster) Sign(key ed25519.PrivateKey) { r.Sig = ed25519.Sign(key, r.Canonical()) }

// Device returns the device of r with key fingerprint fp.
func (r PersonRoster) Device(fp string) (identity.Public, bool) {
	for _, d := range r.Devices {
		if d.Fingerprint() == fp {
			return d, true
		}
	}
	return identity.Public{}, false
}

// Has reports whether the device at address with fingerprint fp is in r.
func (r PersonRoster) Has(address, fp string) bool {
	d, ok := r.Device(fp)
	return ok && d.Address == address
}

// JoinBytes are what a device signs to join person at seq, after the roster
// whose hash is prev, as the entry dev.
func JoinBytes(person string, seq int64, prev string, dev identity.Public) []byte {
	data, _ := json.Marshal(struct {
		Person string          `json:"person"`
		Seq    int64           `json:"seq"`
		Prev   string          `json:"prev"`
		Device identity.Public `json:"device"`
	}{person, seq, prev, dev})
	return append([]byte(PersonJoinDomain), data...)
}

// Validate checks the shape and bounds of r (not its signatures or chain).
func (r PersonRoster) Validate() error {
	if r.Email != "" {
		if email, err := NormalizeEmail(r.Email); err != nil || email != r.Email {
			return errors.New("person: invalid email")
		}
	}
	if !ValidID(r.Person) {
		return errors.New("person: invalid id")
	}
	if err := validLabel(r.Label); err != nil {
		return err
	}
	switch {
	case r.Seq < 0:
		return errors.New("person: invalid seq")
	case r.Seq == 0 && (r.Prev != "" || r.By != "" || len(r.Join) > 0 || len(r.Devices) != 1):
		return errors.New("person: a first roster has one device and nothing before it")
	case r.Seq > 0 && (!ValidHash(r.Prev) || !ValidFingerprint(r.By)):
		return errors.New("person: a later roster names the roster before it and its signer")
	}
	if len(r.Devices) == 0 || len(r.Devices) > MaxPersonDevices {
		return fmt.Errorf("person: 1-%d devices", MaxPersonDevices)
	}
	addrs, fps := map[string]bool{}, map[string]bool{}
	for _, d := range r.Devices {
		if _, _, err := SplitAddress(d.Address); err != nil {
			return fmt.Errorf("person: %w", err)
		}
		if err := d.Verify(); err != nil {
			return fmt.Errorf("person: device %s: %w", d.Address, err)
		}
		fp := d.Fingerprint()
		if addrs[d.Address] || fps[fp] {
			return errors.New("person: a device is listed twice")
		}
		addrs[d.Address], fps[fp] = true, true
	}
	return nil
}

// VerifyFirst checks r as the first roster of its person: signed by its
// one device.
func (r PersonRoster) VerifyFirst() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Seq != 0 {
		return errors.New("person: not a first roster")
	}
	if !ed25519.Verify(r.Devices[0].SignKey, r.Canonical(), r.Sig) {
		return errors.New("person: signature invalid")
	}
	return nil
}

// VerifyNext checks r as the roster that follows prev (itself verified):
// the chain link, the signer, and the consent of an added device. It
// returns the added device, if any.
func (r PersonRoster) VerifyNext(prev PersonRoster) (added *identity.Public, err error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Person != prev.Person || r.Seq != prev.Seq+1 || r.Prev != prev.Hash() || r.Email != prev.Email {
		return nil, errors.New("person: not the next roster of that chain")
	}
	signer, ok := prev.Device(r.By)
	if !ok {
		return nil, errors.New("person: signed by a device that is not in the roster before it")
	}
	if !ed25519.Verify(signer.SignKey, r.Canonical(), r.Sig) {
		return nil, errors.New("person: signature invalid")
	}
	for _, d := range r.Devices {
		old, kept := prev.Device(d.Fingerprint())
		switch {
		case kept && old.Address != d.Address:
			return nil, errors.New("person: a device changed its address")
		case !kept && added != nil:
			return nil, errors.New("person: more than one device added in one step")
		case !kept:
			d := d
			added = &d
		}
	}
	if added == nil {
		if len(r.Join) > 0 {
			return nil, errors.New("person: a join without an added device")
		}
		return nil, nil
	}
	if !ed25519.Verify(added.SignKey, JoinBytes(r.Person, r.Seq, r.Prev, *added), r.Join) {
		return nil, errors.New("person: the added device did not consent (join signature invalid)")
	}
	return added, nil
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

// ValidLabel checks a person label (also a group title or team name)
// against the rules every record carrying one is checked with, so a
// refusal can say why before anything is signed or sent.
func ValidLabel(s string) error { return validLabel(s) }

func validLabel(s string) error {
	if s == "" || len(s) > MaxPersonLabel || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return fmt.Errorf("person: label must be 1-%d bytes of text without surrounding spaces", MaxPersonLabel)
	}
	for _, c := range s {
		if !unicode.IsPrint(c) {
			return errors.New("person: label may hold only letters, marks, numbers, punctuation, symbols and plain spaces")
		}
	}
	return nil
}

// Conversation root (E0) of a DM.
//
// The creator's device makes and signs it; its members are two persons,
// each bound to the hash of the roster the creator had pinned; a later
// device of a member speaks in it once its roster follows that one in the
// member's chain. The
// conversation id is the hash of the canonical bytes, which therefore do
// not contain it: a conversation cannot be claimed without its root, and two
// DMs with the same person differ by their random nonce.
//
// Canonical bytes:
//
//	agentnet-conv-root-v2\n{"v":2,"kind":"dm","creator":{"person":"<32 hex>",
//	"roster":"<64 hex>","address":"<person/agent>","fingerprint":"<fp>"},
//	"members":[{"person":"<32 hex>","roster":"<64 hex>"},{…}],
//	"nonce":"<32 hex>","created":<unix seconds>}
//
// Members are sorted by person id and distinct; the creator's person is one
// of them with the same roster hash. The signed root is at most MaxConvRoot
// bytes. A DM never changes members: there are no later epochs here.

// ConvRootDomain starts a conversation root's canonical bytes.
const ConvRootDomain = "agentnet-conv-root-v2\n"

// ConvRootVersion is the root version (persons with roster chains).
const ConvRootVersion = 2

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
	// Group roots only (version 3, protocol/group*.go): a DM root (version
	// 2) carries none of these, so its canonical bytes are unchanged.
	Realm  string   `json:"realm,omitempty"`
	Title  string   `json:"title,omitempty"`
	Admins []string `json:"admins,omitempty"`
	Sig    []byte   `json:"sig,omitempty"`
}

// Canonical returns the bytes the creator signs; ID is their hash.
func (c ConvRoot) Canonical() []byte {
	c.Sig = nil
	data, _ := json.Marshal(c)
	domain := ConvRootDomain
	if c.V == GroupRootVersion { // group roots (groups.go); DM bytes unchanged
		domain = GroupRootDomain
	}
	return append([]byte(domain), data...)
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
	if c.V == GroupRootVersion {
		return ValidateGroupRoot(c)
	}
	if c.V != ConvRootVersion || c.Kind != ConvKindDM {
		return errors.New("conversation: unsupported root")
	}
	if c.Realm != "" || c.Title != "" || len(c.Admins) != 0 {
		return errors.New("conversation: a DM root carries no realm, title or admins")
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
	if len(data) > MaxGroupRoot {
		return c, errors.New("conversation: root too large")
	}
	if err := decodeStrictJSON(data, &c); err != nil {
		return c, fmt.Errorf("conversation: %w", err)
	}
	if len(data) > ConvRootVersionLimit(c.V) { // a DM root keeps its 2 KiB bound
		return c, errors.New("conversation: root too large")
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
// the signed record at most MaxCapsRecord bytes. A device advertises at
// most MaxAdvertisedCaps: readers parse twice that, so a later program may
// list more without looking, to an older reader, as if it read nothing (a
// record that does not parse counts for nothing). Each daemon session
// publishes its own; the relay keeps the newest per (device, session).
// A relay can withhold a record but cannot forge one; which sessions are
// live is only the relay's statement (see Profile).

// CapsDomain starts a capability record's canonical bytes.
const CapsDomain = "agentnet-caps-v1\n"

// CapEnv2 means the device reads envelope version 2 (conversations).
const CapEnv2 = "env2"

// CapPerson means the device reads person roster chains, conversation
// roots of version 2, fan-out sends and history (a device without it is
// asked to update, never sent a partial conversation).
const CapPerson = "person2"

// Relay membership roles (Profile.SelfRole).
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// CapDriveSpace means the device reads a conversation's Drive space record
// (envelope.SubDriveSpace): a dedicated version 2 record, not a control.
const CapDriveSpace = "drv1"

// CapHeadless means the device reads execution status and operator
// decisions (version 3 controls "status" and "decision") and structured
// review reports. CapControl alone says nothing about these.
const CapHeadless = "hdl1"

// CapControl means the device reads version 3 controls (reactions,
// revisions, retractions of messages).
const CapControl = "ctl3"

// Bounds of a capability record: what a reader parses, and what a device
// advertises (ROOM_V1 §2.1: parse headroom).
const (
	MaxCaps           = 32
	MaxAdvertisedCaps = 16
	MaxCapsRecord     = 1024
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

// Reads reports whether c says its session reads capability name: it lists
// it, or lists CapRoom, which implies each of RoomImplies.
func (c CapsRecord) Reads(name string) bool {
	return c.Has(name) || c.Has(CapRoom) && slices.Contains(RoomImplies, name)
}

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
	Person   json.RawMessage `json:"person,omitempty"`
	Sessions []string        `json:"sessions,omitempty"`
	// SelfRole is the caller's own membership role on this relay ("admin"
	// or "member"), set only when the caller asks for its own profile;
	// absent for anyone else's profile and on older relays.
	SelfRole string            `json:"self_role,omitempty"`
	Live     bool              `json:"live"` // Sessions are live now (else: the last one)
	Caps     []json.RawMessage `json:"caps,omitempty"`
}

// Supports reports whether the device with signKey at address supports
// capability name by p: there are sessions, and each has a record that
// verifies, names that device and session, and reads name (CapsRecord.Reads:
// lists it, or a capability that implies it).
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
		if c.Reads(name) {
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
	FeaturePerson  = "person2" // person roster chains: PUT /v1/person, chains, device links, profiles
	FeatureCaps    = "caps"    // PUT /v1/caps, profiles
	FeatureEnv3    = "env3"    // accepts envelope version 3 (controls)
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
