package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/misunders2d/agentnet/internal/identity"
)

// Linking a new device to an existing person (owner decision 2026-09-29: a
// person adds their own devices; no admin invite per device).
//
//  1. The existing device E asks its Hub for a device invite bound to its
//     person (POST /v1/person/device-invite): one use, short-lived, admitting
//     only a device that becomes PENDING, bound to that person, E and the
//     offer. E shows one code (QR or text), a LinkOffer: that invite, the
//     person's current roster step, E's own device and a 32-byte secret that
//     only E and the code's reader know (never sent to the Hub).
//  2. The new device N joins with the invite, adding JoinLink: its consent to
//     join the person as the next roster step (the join signature, see
//     JoinBytes) and a MAC under the secret over the fixed transcript
//     (LinkTranscript). The Hub checks the join signature against its own
//     chain and holds N PENDING: not a member, no messages, only its stream.
//  3. The Hub tells E (stream event "link", LinkEvent). E checks the MAC and
//     the join signature against its offer, and asks its person.
//  4. On approval E signs the roster step adding N and publishes it; the Hub
//     activates N with the step, in one transaction. On refusal or expiry
//     the Hub revokes N.
//
// The secret authenticates N to E beyond the Hub: the Hub never sees it, so
// it cannot put another key in N's place.

// LinkPrefix starts an encoded LinkOffer (in a QR, the URL fragment).
const LinkPrefix = "agentnet-link-v2:"

// LinkDomain starts the transcript the link MAC covers.
const LinkDomain = "agentnet-link-v2\n"

// LinkSecretSize is the size of an offer's secret.
const LinkSecretSize = 32

// LinkApprover is the existing device that made the offer.
type LinkApprover struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// LinkOffer is what the existing device shows the new one.
type LinkOffer struct {
	V        int          `json:"v"`
	Invite   string       `json:"invite"` // the device invite (an invite code)
	Offer    string       `json:"offer"`  // 32 hex: this offer's id
	Expires  int64        `json:"expires"`
	Person   string       `json:"person"`
	Seq      int64        `json:"seq"`    // the person's current roster step
	Roster   string       `json:"roster"` // its hash
	Approver LinkApprover `json:"approver"`
	Secret   []byte       `json:"secret"`
}

// Encode returns the offer as text.
func (o LinkOffer) Encode() string {
	data, _ := json.Marshal(o)
	return LinkPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// DecodeLinkOffer parses an encoded offer, alone, as a URL fragment ("#…")
// or in a whole URL ending in one (surrounding space is ignored), and
// checks its shape.
func DecodeLinkOffer(s string) (LinkOffer, error) {
	var o LinkOffer
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "#"+LinkPrefix); i >= 0 {
		s = s[i+1:]
	}
	raw, ok := strings.CutPrefix(s, LinkPrefix)
	if !ok {
		return o, errors.New("not an AgentNet device link code")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return o, errors.New("device link code damaged")
	}
	if err := decodeStrictJSON(data, &o); err != nil {
		return o, errors.New("device link code damaged")
	}
	if o.V != 2 || !ValidID(o.Offer) || o.Expires <= 0 || !ValidID(o.Person) || o.Seq < 0 || !ValidHash(o.Roster) ||
		!ValidFingerprint(o.Approver.Fingerprint) || len(o.Secret) != LinkSecretSize {
		return o, errors.New("device link code damaged")
	}
	if _, _, err := SplitAddress(o.Approver.Address); err != nil {
		return o, errors.New("device link code damaged")
	}
	if _, err := DecodeInvite(o.Invite); err != nil {
		return o, errors.New("device link code damaged")
	}
	return o, nil
}

// JoinLink is what a device joining with a device invite adds to its join
// request.
type JoinLink struct {
	Offer string `json:"offer"`
	Join  []byte `json:"join"` // JoinBytes(person, seq+1, roster, the device), signed by the device
	MAC   []byte `json:"mac"`  // LinkMAC under the offer's secret
}

// LinkTranscript is the fixed transcript the link MAC covers: the offer as
// the new device read it, the new device's entry and its join signature.
func LinkTranscript(o LinkOffer, dev identity.Public, join []byte) []byte {
	data, _ := json.Marshal(struct {
		Offer    string          `json:"offer"`
		Expires  int64           `json:"expires"`
		Person   string          `json:"person"`
		Seq      int64           `json:"seq"`
		Roster   string          `json:"roster"`
		Approver string          `json:"approver"`
		Device   identity.Public `json:"device"`
		Join     []byte          `json:"join"`
	}{o.Offer, o.Expires, o.Person, o.Seq, o.Roster, o.Approver.Fingerprint, dev, join})
	return append([]byte(LinkDomain), data...)
}

// LinkMAC is HMAC-SHA256 under the offer's secret over the transcript.
func LinkMAC(o LinkOffer, dev identity.Public, join []byte) []byte {
	m := hmac.New(sha256.New, o.Secret)
	m.Write(LinkTranscript(o, dev, join))
	return m.Sum(nil)
}

// CheckLinkMAC compares mac with LinkMAC in constant time.
func CheckLinkMAC(o LinkOffer, dev identity.Public, join, mac []byte) bool {
	return hmac.Equal(mac, LinkMAC(o, dev, join))
}

// DeviceInviteRequest asks for a device invite for the caller's person.
type DeviceInviteRequest struct {
	Offer   string `json:"offer"`
	Expires int64  `json:"expires"` // unix seconds, at most MaxLinkTTL ahead
}

// DeviceInvite is the Hub's answer: the invite code.
type DeviceInvite struct {
	Code string `json:"code"`
}

// DeviceRefusal asks the Hub to revoke a pending device the caller invited.
type DeviceRefusal struct {
	Address string `json:"address"`
}

// LinkEvent is the "link" push event to the inviting device: a device joined
// with its invite and waits for its person's decision.
type LinkEvent struct {
	Offer  string          `json:"offer"`
	Device identity.Public `json:"device"`
	Join   []byte          `json:"join"`
	MAC    []byte          `json:"mac"`
	// Google asks for an explicit local decision without a device-link
	// code. It never grants permission or approves itself.
	Google *GoogleLink `json:"google,omitempty"`
}

// PersonChain is a page of a person's roster chain, oldest first.
type PersonChain struct {
	Records []json.RawMessage `json:"records"`
	More    bool              `json:"more"`
}

// Bounds of device links and chain pages.
const (
	MaxLinkTTL        = 10 * 60   // seconds an offer may live
	PendingGrace      = 30 * 60   // seconds a pending device outlives its offer before it is revoked
	MaxChainPage      = 64        // records per chain page
	MaxChainPageBytes = 256 << 10 // and bytes
)

// Codes for link refusals the Hub reports (Error.Code).
const (
	CodeLinkPending = "link_pending" // the device waits for its person's approval
	CodeLinkRefused = "link_refused" // its person refused it
	CodeLinkExpired = "link_expired" // nobody approved it in time
	CodeRosterStale = "roster_stale" // the roster moved on; the step does not follow it
)
