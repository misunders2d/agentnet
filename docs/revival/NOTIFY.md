# Optional messenger notifications: core contract (checkpoint 1)

Owner requirement (2026-09-29): optional notifications for ordinary DM
activity on desktop and mobile, in this release, off until the person turns
them on. This file is the core side (protocol, envelope, Hub, desktop
daemon). The browser device's service worker, permission prompt, settings,
mutes and click handling are the frontend's, built on the types here.
Status: contract; nothing below is built yet unless marked.

## 1. Model

- **Per device.** Every AgentNet device is an enrolled address: a desktop
  daemon installation or a browser device. Preferences, mutes, the push
  subscription and presentation acknowledgements belong to the device that
  signs them; no API names another device. A person with a laptop and a
  phone turns notifications on (and mutes) on each.
- **Two delivery paths, one each.**
  - *Browser device (desktop browser, installed PWA, iPhone Home Screen
    app):* Web Push. The Hub holds a content-free pending alert and, after a
    grace period, sends it to the device's push service; the service worker
    shows it. The page never shows its own notification.
  - *Desktop daemon:* the daemon holds the stream and decrypts. It decides
    locally from the decrypted message and shows a native notification
    (`internal/notify`) after the same grace, unless its local page reports
    that it presented the message. No Hub involvement.
- **What asks for attention.** A DM turn meant for a person: a message,
  question or task typed by a person (`origin: ui`), and an invited agent's
  answer or result (`pid`, `origin: agent:*`). Never: participation events,
  excerpts, replicas, receipts, legacy (v1) agent traffic, agent background
  work. The sender marks it (a hint); the receiver's preferences decide.
- **Content-free.** Banner "AgentNet" / "New activity". The payload names
  only an opaque channel (§2); the click opens the conversation that the
  device itself resolves from that channel, after syncing if needed, or the
  inbox with an honest "not available here" state. Never a URL or id from
  the payload used as is.
- **Not a promise.** An alert may be late, dropped or suppressed by the OS
  (permission, Focus, battery, network); a crash between showing it and
  recording it may show it twice. Presentation suppression has a bounded
  race: an alert already dispatched is not recalled.

## 2. Wire: attention hint and channel (envelope v2 outer)

```go
type Envelope struct {
	// ... v, id, from, to, ts, kind, ct, blobs, session, fallback (unchanged)
	Attn bool   `json:"attn,omitempty"` // v2 only: the sender asks for the recipient's attention
	Chan string `json:"chan,omitempty"` // v2 only, with attn: the recipient's channel for this conversation
	Sig  []byte `json:"sig,omitempty"`
}
```

- Signed like every outer field (domain `agentnet-envelope-v2\n`, the JSON
  of the envelope without `sig`, field order above; both omitted when
  unset, so an envelope without them signs exactly as today). Version 1:
  both must be absent, as now.
- Valid: `attn` false and `chan` empty, or `attn` true and `chan` a valid
  channel. The Hub and every reader check the shape; nobody can check the
  truth of the claim.
- **Channel** (`protocol.NotifyChannel`): per conversation and recipient
  device, computable by the sender and the recipient, never by the Hub:

  `base64url_nopad( SHA-256( "agentnet-notify-channel-v1\n" || conv || "\n" || recipient_fp )[0:16] )`

  22 characters. `conv` is the 64-hex conversation id (entropy from the
  root's random nonce); `recipient_fp` the recipient device's key
  fingerprint. Two DMs with the same person have different channels, so
  they mute separately. It is not a secret the receiver chose, and the Hub
  can group a recipient's messages by it (§7).
- **Sender rule** (Go client and browser device alike): set `attn` and
  `chan` only on a turn that asks for attention (§1), only if the relay
  lists feature `notify1` and every listed session of the recipient device
  lists capability `notify1` (as for `env2`). Otherwise omit both: a device
  that cannot read the fields would reject the signature.
- **Reader rule:** after decrypting, the recipient recomputes the channel
  from the inner `conv` and its own fingerprint. A mismatch (or `attn` on
  a turn that should not ask) is recorded and never trusted for routing;
  the message itself is admitted as today.

## 3. Hub API (all signed by the calling device; scoped to it)

```go
const FeatureNotify = "notify1" // relay: takes attn/chan and the notify APIs
const CapNotify     = "notify1" // device caps record: reads attn/chan

// GET /v1/notify (no signature needed)
type NotifyInfo struct {
	PushKey   string   `json:"push_key,omitempty"`   // VAPID public key, base64url uncompressed P-256; "" when this Hub sends no Web Push
	PushHosts []string `json:"push_hosts,omitempty"` // host suffixes of push services this Hub sends to
	GraceMS   int64    `json:"grace_ms"`             // how long a pending alert waits for a presentation ack
}

// PUT /v1/notify/prefs replaces the caller's preferences; GET returns them.
type NotifyPrefs struct {
	Enabled bool           `json:"enabled"`
	Senders []NotifySender `json:"senders"` // exact sender keys whose attention may alert this device (≤ 256)
	Mutes   []string       `json:"mutes"`   // channels that never alert this device (≤ 1024)
}
type NotifySender struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"` // must equal the sender's enrolled key when an alert is queued and sent
}
type NotifyState struct {
	Prefs      NotifyPrefs `json:"prefs"`
	Subscribed bool        `json:"subscribed"`
}

// PUT /v1/notify/subscription replaces the caller's one Web Push
// subscription; DELETE removes it (and its pending alerts).
type PushSubscription struct {
	Endpoint string `json:"endpoint"` // https, allowed push host, ≤ 2048 bytes
	P256DH   string `json:"p256dh"`   // base64url, 65-byte uncompressed P-256 point
	Auth     string `json:"auth"`     // base64url, 16 bytes
}

// POST /v1/notify/seen: the caller presented these messages of a channel
// (visible, focused, that DM, newest in view). Cancels a matching pending
// alert. Not a read receipt: never forwarded, never changes custody or
// delivery states.
type NotifySeen struct {
	Channel string   `json:"channel"`
	IDs     []string `json:"ids"` // envelope ids presented, ≤ 32
}

// The Web Push body (encrypted to the subscription, RFC 8291).
type PushPayload struct {
	V       int    `json:"v"`              // 1
	Channel string `json:"chan,omitempty"` // "" = several conversations (a summary)
}
```

- Request bodies ≤ 64 KiB; strict JSON (unknown fields refused); invalid
  shapes refused with a generic error. Subscription endpoint and keys never
  appear in logs, errors or responses (GET returns only `subscribed`).
- **Preferences are the authority.** An alert is queued only if all hold:
  the envelope has `attn`; the recipient has `enabled`, a subscription and
  is not revoked; the sender's address is in `senders` with the
  fingerprint of the key the Hub has enrolled for it now; the channel is
  not muted. Membership is not checked (the Hub cannot see it). An allowed
  sender can mislabel `chan` or `attn`; the recipient's defenses are
  removing that sender and the per-device rate limit, not per-DM mute.
  "From your contacts" is the page's wording for `senders`; nothing is
  added to it by receiving a message.
- Turning off (`enabled: false`), deleting the subscription, or a revoke
  drops the device's pending alerts.

## 4. Pending alerts and dispatch (Hub)

- Stored in the same transaction as the envelope, persisted (a Hub restart
  loses nothing), keyed by (recipient, channel): the first message sets the
  deadline `now + grace`; later ones of that channel count and replace
  `last_msg`, never extending the deadline.
- `POST /v1/notify/seen` cancels the channel's pending alert when its
  `last_msg` is among the ids (newer, unseen activity keeps it). A stream
  connection, stream acks and delivery receipts never cancel anything.
- One scheduler goroutine: a timer on the earliest deadline, woken by
  changes; no polling, no timer or goroutine per message. At a deadline it
  checks again: enabled, subscription, not revoked, channel not muted, and
  the last sender still allowed with its current key; then it sends through
  the push client (§5) with `Topic` = the channel (or `summary`), `TTL`
  24 h, `Urgency: high`, and the §3 payload padded to one 1024-byte record
  (every push the same size, well under the 4 KiB service limits).
- Outcomes: 2xx = accepted by the push service (queued there; not shown,
  not read) → the alert is done. 404/410 → that subscription is deleted
  (and its alerts). 429/5xx/network → retried with backoff honouring
  `Retry-After`, at most 6 attempts within 24 h, then dropped and counted in
  the log (no endpoint). Other 4xx → dropped, logged by status only.
- Bounds (operational constants, named in code, tested):
  - grace 5 s;
  - at most 64 channel rows per device; beyond, one summary row
    (`chan` empty) takes the rest; never unbounded per-channel records;
  - per device, a token bucket of 3 pushes, refilled one per 10 s; a
    limited alert stays pending with the next finite deadline (never
    dropped silently);
  - one send at a time, 10 s per request, response bodies read ≤ 4 KiB.

## 5. Outbound safety (SSRF)

The Hub makes HTTPS requests to URLs an enrolled device supplied.

- **Explicit provider policy:** the endpoint must be `https://`, port 443,
  a DNS host name (no IP literal), no userinfo, no fragment, and its host
  must end in a configured push host suffix. Default list: Apple
  (`push.apple.com`), Google (`fcm.googleapis.com`), Mozilla
  (`push.services.mozilla.com`), Microsoft (`notify.windows.com`). An
  operator can widen it (`hub serve` config); the list is published in
  `GET /v1/notify`.
- **At dial time,** the address actually being connected must be public:
  loopback, private (RFC 1918, ULA), link-local (incl. 169.254.169.254
  metadata), CGNAT, multicast, unspecified, documentation, benchmark,
  reserved, NAT64 and IPv4-mapped forms of those are refused in the
  dialer's `Control`, so DNS rebinding has no check-then-dial gap.
- No proxy from the environment, redirects refused, system CAs, TLS 1.2+,
  10 s timeout, bounded response. Tests: rebinding to loopback, a redirect,
  each refused range, IPv6 forms, and a disallowed host.

## 6. Keys, dependency, compatibility

- **VAPID key:** generated on first use, kept in the data directory
  (`push.key`, owner-only), kept across updates and in backups; losing it
  invalidates every subscription (devices re-subscribe on their next
  start). Subject: the Hub's public URL.
- **Library:** `github.com/SherClockHolmes/webpush-go` v1.4.0 (tag commit
  f5c3e9f7b642a8dd66cb844050520526f721971d, module sum
  `h1:ocnzNKWN23T9nvHi6IfyrQjkIc0oJWv1B1pULsf9i3s=`), with our client via its
  `HTTPClient` option. Not audited, and not described as such. Inspected
  read-only: RFC 8291 aes128gcm, the body padded to one record of the size
  asked for (4096 by default; we ask 1024), VAPID ES256 JWT with a 12 h
  `exp`; it uses the
  deprecated `crypto/elliptic` point API (still functional in Go 1.26).
  Advisories (Go vulnerability database, 2026-09-29): none for webpush-go;
  its `golang-jwt/jwt/v5` v5.2.1 is affected by GO-2025-3553 (header
  parsing; we only sign), so the module requires v5.3.1; `x/crypto` stays at
  the project's v0.55.0 (its open advisories are in `ssh` and `openpgp`,
  not imported). Tests decrypt the library's output with the receiver key
  (RFC 8291 check) and verify the VAPID JWT.
- **Compatibility:** v1 unchanged. `attn`/`chan` exist only in v2 (never
  released) and are sent only to relays listing `notify1` and devices
  listing `notify1`; older relays are never asked for the notify APIs
  (feature absent). The browser engine's canonical envelope bytes must add
  both fields, in that position, omitted when unset (shared vectors).

## 7. Metadata the Hub newly learns (plainly)

- Which of a recipient device's messages share a conversation (by channel),
  and which the sender flagged for attention.
- The device's notification preferences: allowed sender addresses and
  keys, muted channel tokens, on/off, and its push endpoint (so its push
  service and a device token).
- When the device presented a conversation (presentation acks): timing of
  reading activity, per channel. Not shared with anyone.
- Push services learn the Hub's VAPID key, the time and count of pushes to
  a device, and the opaque `Topic` (channel); the body is encrypted to the
  device.
- Not learned: plaintext, the conversation id, labels, history, who the
  other member is beyond the existing sender/recipient.

## 8. Desktop daemon

- Preferences are local (on/off, allowed sender keys, per-conversation
  mutes by conversation id), in the client store; no Hub API.
- On admitting a DM turn that asks for attention (decided from the
  decrypted message, §1; the outer hint is not needed), from a pinned
  member whose key is allowed, in a conversation not muted: a local pending
  alert (persisted, same grace, coalesced per conversation, one earliest
  deadline timer, no polling). The local page reports presented messages
  (same shape as `NotifySeen`, conversation id instead of channel) through
  the daemon's local API; nothing else cancels.
- At the deadline: a native notification "AgentNet" / "New activity",
  replacing the previous one; a click opens the local page on that
  conversation where the platform supports clicks (Linux today). Each
  message alerts at most once (recorded), so a restart does not repeat.
- Platforms: Linux (freedesktop, with click), Windows and macOS (shown, no
  click yet); proof per OS is a native run, not a cross-compile.

## 9. Tests the core owns

Default off; off/denied leaves messaging unchanged; queued only with all §3
conditions; two DMs with one person mute separately; seen cancels only a
matching newest message; stream/receipt acks never cancel; restart keeps
pending; deadline not extended by later messages; flood of channels caps
at 64 + summary; rate limit keeps a finite next deadline; 404/410 remove
the subscription; retries honour Retry-After and stop; revoke/off/delete
drop pending; changed sender key is not allowed; forged channel from an
allowed sender alerts under that channel only; wrong-device calls cannot
touch another device (no such parameter); nothing sensitive in logs or
errors; SSRF cases (§5); RFC 8291 decrypt and VAPID verify of real library
output; desktop: grace, local seen, mute, once per message, restart.
