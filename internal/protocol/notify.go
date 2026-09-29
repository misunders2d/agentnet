package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Optional messenger notifications (docs/revival/NOTIFY.md). A device that
// turns them on tells its Hub which senders' attention may alert it, which
// conversations are muted and, for a browser device, its Web Push
// subscription; the Hub holds a content-free pending alert per conversation
// channel for a grace period, which the device's presentation of the
// message cancels.

// FeatureNotify: the relay takes the envelope's attention hint and channel,
// and serves the notify APIs.
const FeatureNotify = "notify1"

// CapNotify: the device reads envelopes carrying the attention hint and
// channel (a device without it would refuse their signature).
const CapNotify = "notify1"

// NotifyChannelDomain starts the bytes a notification channel is hashed from.
const NotifyChannelDomain = "agentnet-notify-channel-v1\n"

// NotifyChannelLen is the length of a notification channel.
const NotifyChannelLen = 22

// NotifyChannel is the channel of conversation conv for the recipient
// device with key fingerprint recipientFP: base64url (no padding) of the
// first 16 bytes of SHA-256(domain || conv || "\n" || recipientFP). The
// sender and the recipient can compute it; the Hub cannot. Two
// conversations with the same person have different channels.
func NotifyChannel(conv, recipientFP string) string {
	sum := sha256.Sum256([]byte(NotifyChannelDomain + conv + "\n" + recipientFP))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// ValidNotifyChannel reports whether s has a channel's shape.
func ValidNotifyChannel(s string) bool {
	if len(s) != NotifyChannelLen {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 16 && base64.RawURLEncoding.EncodeToString(b) == s
}

// Bounds of the notify APIs.
const (
	MaxNotifySenders  = 256
	MaxNotifyMutes    = 1024
	MaxNotifySeenIDs  = 32
	MaxPushEndpoint   = 2048
	MaxNotifyRequest  = 64 << 10
	PushPayloadRecord = 1024 // every push body is one record of this size
)

// NotifyInfo is GET /v1/notify.
type NotifyInfo struct {
	PushKey   string   `json:"push_key,omitempty"`   // VAPID public key (base64url, uncompressed P-256); "" when this Hub sends no Web Push
	PushHosts []string `json:"push_hosts,omitempty"` // host suffixes of the push services this Hub sends to
	GraceMS   int64    `json:"grace_ms"`             // how long a pending alert waits for the device's presentation of it
}

// NotifySender is one sender whose attention may alert the device, by its
// exact key: an alert is queued and sent only while the Hub has that very
// key enrolled for the address.
type NotifySender struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// NotifyPrefs is the calling device's notification preferences, replaced
// whole by PUT /v1/notify/prefs.
type NotifyPrefs struct {
	Enabled bool           `json:"enabled"`
	Senders []NotifySender `json:"senders"`
	Mutes   []string       `json:"mutes"` // channels that never alert this device
}

// Validate checks the shape and bounds of p.
func (p NotifyPrefs) Validate() error {
	if len(p.Senders) > MaxNotifySenders || len(p.Mutes) > MaxNotifyMutes {
		return errors.New("notify: too many senders or mutes")
	}
	seen := map[string]bool{}
	for _, s := range p.Senders {
		if _, _, err := SplitAddress(s.Address); err != nil || !ValidFingerprint(s.Fingerprint) || seen[s.Address] {
			return errors.New("notify: invalid or repeated sender")
		}
		seen[s.Address] = true
	}
	muted := map[string]bool{}
	for _, c := range p.Mutes {
		if !ValidNotifyChannel(c) || muted[c] {
			return errors.New("notify: invalid or repeated mute")
		}
		muted[c] = true
	}
	return nil
}

// NotifyState is GET /v1/notify/prefs: the preferences and whether a push
// subscription is held (never the subscription itself).
type NotifyState struct {
	Prefs      NotifyPrefs `json:"prefs"`
	Subscribed bool        `json:"subscribed"`
}

// PushSubscription is the calling device's one Web Push subscription
// (PUT /v1/notify/subscription; DELETE removes it). It is sensitive: never
// logged, echoed or put in an error.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"` // base64url, a 65-byte uncompressed P-256 point
	Auth     string `json:"auth"`   // base64url, 16 bytes
}

// DefaultPushHosts are the push services a Hub sends to unless its operator
// configures others: Apple, Google, Mozilla, Microsoft.
var DefaultPushHosts = []string{"push.apple.com", "fcm.googleapis.com", "push.services.mozilla.com", "notify.windows.com"}

// Validate checks s against the explicit push host list hosts (host
// suffixes): an https URL on port 443 whose DNS host name is one of them or
// ends in "."+one, with no user information and no fragment (path and query
// are the push service's own); and well-formed keys. Errors never contain
// the endpoint or keys.
func (s PushSubscription) Validate(hosts []string) error {
	if len(s.Endpoint) > MaxPushEndpoint {
		return errors.New("notify: push endpoint too long")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Host == "" {
		return errors.New("notify: push endpoint must be a plain https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("notify: push endpoint must use port 443")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return errors.New("notify: push endpoint must name its host")
	}
	allowed := false
	for _, h := range hosts {
		h = strings.ToLower(h)
		allowed = allowed || host == h || strings.HasSuffix(host, "."+h)
	}
	if !allowed {
		return errors.New("notify: this Hub does not send to that push service")
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s.P256DH, "=")); err != nil || len(b) != 65 || b[0] != 4 {
		return errors.New("notify: invalid p256dh key")
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s.Auth, "=")); err != nil || len(b) != 16 {
		return errors.New("notify: invalid auth secret")
	}
	return nil
}

// NotifySeen is POST /v1/notify/seen: the calling device presented these
// messages of a channel (visible, focused, that conversation, its newest
// message in view). It cancels a pending alert whose newest message is
// among them. It is not a read receipt: it is never forwarded and never
// changes a message's custody or delivery state.
type NotifySeen struct {
	Channel string   `json:"channel"`
	IDs     []string `json:"ids"`
}

// Validate checks the shape and bounds of s.
func (s NotifySeen) Validate() error {
	if !ValidNotifyChannel(s.Channel) || len(s.IDs) == 0 || len(s.IDs) > MaxNotifySeenIDs {
		return fmt.Errorf("notify: a channel and 1-%d message ids", MaxNotifySeenIDs)
	}
	for _, id := range s.IDs {
		if !ValidID(id) {
			return errors.New("notify: invalid message id")
		}
	}
	return nil
}

// PushPayload is the body of a Web Push message, encrypted to the
// subscription. It names only the channel; "" means several conversations.
type PushPayload struct {
	V       int    `json:"v"`
	Channel string `json:"chan,omitempty"`
}
