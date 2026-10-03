# Optional messenger notifications: core contract (checkpoint 1)

Owner requirement (2026-09-29): optional notifications for ordinary DM
activity on desktop and mobile, in this release, off until the person turns
them on. This file is the core side (protocol, envelope, Hub, desktop
daemon). The browser device's service worker, permission prompt, settings,
mutes and click handling are the frontend's, built on the types here.
Status: §2-8 built in core (protocol, envelope, Go sender, Hub, desktop
daemon); the browser device (service worker, settings, page controls,
`#conv=` on the daemon page) is the frontend's.

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
	PushHosts []string `json:"push_hosts,omitempty"` // push service hosts this Hub sends to (exact, or a dot-delimited subdomain)
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

Built: `internal/hub/notify.go`, `push.go`; schema step 6.

- Stored in the same transaction as the envelope, persisted (a Hub restart
  loses nothing), keyed by (device, channel, sender): the first message
  sets the deadline `now + grace`; later ones count, replace `last_msg` and
  bump a generation, never extending the deadline.
- **Provenance:** every alert keeps its sender. A sender gets at most 16
  exact channels pending per device; beyond that, one summary per sender,
  which records the channels it stands for (at most 64; schema step 7).
  So removing, re-keying or revoking a sender stops even its summary, and
  mutes still decide: a summary goes out only if at least one channel it
  recorded is not muted; a channel beyond its record never alerts on its
  own (a sender spraying channels is the documented honest-sender limit).
- `POST /v1/notify/seen` cancels the channel's pending alerts (and its
  senders' summaries) whose `last_msg` is among the ids: newer, unseen
  activity keeps them. A stream connection, stream acks and delivery
  receipts never cancel anything.
- One scheduler goroutine: a timer on the earliest deadline, woken by
  changes; no polling, no timer or goroutine per message. At a deadline it
  checks every due alert of a device again: enabled, device not revoked,
  not expired, channel not muted, the sender still allowed with the key the
  Hub has enrolled for it now and not revoked. Then ONE push for the device:
  `chan` = the conversation if exactly one is due, else a summary (`""`);
  `Topic` = that channel or `summary`; `TTL` 24 h; `Urgency: high`; the §3
  payload padded to one 1024-byte record.
- **Completion only of what was sent:** the push is built from a snapshot
  of each alert's state (its generation AND its newest message: a row
  presented and made again restarts at generation 1 but never with the
  same newest message) and of the subscription's identity. Every outcome
  (completion, drop, retry, reschedule) touches an alert only in that
  state: one that gained activity, or was removed and made again, while the
  push was out stays with its own deadline (after a sent push, no sooner
  than the grace); a 404/410 removes the subscription only if it is still
  the one the push went to (a replacement made meanwhile stays, and the
  alerts go to it).
- Outcomes: 2xx = accepted by the push service (queued there; not shown,
  not read). 404/410 → that subscription and the device's alerts are
  removed. 429/5xx/network → retried after the service's `Retry-After`
  (a minimum, never shortened), else after 10 s, 20 s, … up to 1 h; at
  most 6 attempts, and never past the 24 h lifetime (a wait beyond it drops
  the alert); drops are counted in the log (never the endpoint). Other statuses (including a
  redirect, never followed) → dropped, logged by status only. A failure
  inside the Hub reschedules that device a minute later (no spin).
- Bounds (named constants in `notify.go`): grace 5 s; lifetime 24 h; 16
  channels per sender and device plus one summary; 32 devices per round;
  per device a token bucket of 3 pushes refilled one per 10 s (a limited
  alert keeps a finite next deadline, never dropped silently); one push at
  a time, 10 s each, response bodies read ≤ 4 KiB.

## 5. Outbound safety (SSRF)

The Hub makes HTTPS requests to URLs an enrolled device supplied.

- **Explicit provider policy:** the endpoint must be `https://`, port 443,
  a DNS host name (no IP literal), no userinfo, no fragment, and its host
  must be one of the listed push hosts or a dot-delimited subdomain of one
  (never a raw suffix match). Always listed: Apple (`push.apple.com`),
  Google (`fcm.googleapis.com`), Mozilla (`push.services.mozilla.com`),
  Microsoft (`notify.windows.com`); an operator adds others by host name
  with `hub serve --push-hosts` (env `AGENTNET_PUSH_HOSTS`). The list is
  published in `GET /v1/notify`.
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

Built: `internal/client/alerts.go`; client schema step 18.

- **Preferences** are local, in the client store, off by default:
  `Agent.AlertPrefs` / `SetAlertPrefs(AlertPrefs{Enabled, Senders
  (exact keys, ≤ 256), Mutes (conversation ids, ≤ 1024)})`; no Hub API.
  The daemon page (frontend) offers the controls; turning off drops
  pending alerts.
- **Queue:** admitting a DM turn that asks for attention (decided from the
  decrypted message, §1; the outer hint is not needed), from a current
  device (with its exact key) of a pinned person one of whose current
  devices' exact keys is allowed (alerts are per person: a device the person
  adds later alerts too; the page allows and removes all of a person's
  device keys together), in a conversation not muted, with alerts on, writes the alert
  in the message's own admission transaction (persisted), keyed by
  conversation: the first message sets the deadline (grace 5 s), later ones
  replace `last_id` without moving it.
- **Presentation:** `Agent.AlertPresented(conv, ids)` (the local page's
  report: visible, focused, that conversation, newest in view) cancels the
  alert whose `last_id` is among the ids; nothing else cancels.
- **Show:** one loop in the daemon, a timer on the earliest deadline, woken
  by admissions and preference changes; no polling. At the deadline it
  checks again (on, not muted, the sender still a current device of a
  pinned, not frozen, person who is still allowed), removes the due alerts in one transaction,
  and only then asks for one native notification "AgentNet" / "New
  activity" (one conversation: its click; several: the page).
- **Order and limits, plainly:** removal is committed before the OS call,
  so a crash in between loses that alert (never repeats it); the OS may
  drop, delay or hide it (Focus, permissions, no notification server). On
  Linux a new alert replaces this daemon's previous one; Windows and macOS
  stack as they do. A pending alert survives a daemon restart and is shown
  once after it. Nothing claims exactly-once display.
- **Click:** on Linux, with `agentnet daemon --ui`, the click runs
  `xdg-open http://127.0.0.1:PORT/#conv=ID`: the page's address WITHOUT its
  token (a command line is readable by other local users), so it opens
  where the browser still holds the page's session cookie; otherwise the
  person runs `agentnet ui`. The page must honour `#conv=` (frontend).
  **Windows:** the notification-area icon names a callback message; the
  daemon's message-only window runs a message loop on the notifier's own
  thread, and only `NIN_BALLOONUSERCLICK` for the current notification's
  icon runs its action (each notification gets an icon id of its own, never
  used twice in a daemon run, so a late click on a replaced or closed one is
  ignored and never opens another conversation; after 65535 notifications in
  one run the ids are used up and later ones show without a click action
  until the daemon restarts). The action opens the
  page with `%SystemRoot%\System32\rundll32.exe url.dll,FileProtocolHandler
  http://127.0.0.1:PORT/#conv=ID` (one argument, no shell, no token); a
  reminder or review on a direct message runs `agentnet open ID` in a new
  console that is its standard input and output (CreateProcess with
  CREATE_NEW_CONSOLE and no inherited or redirected handles; not os/exec,
  whose pipes a new console does not replace). Tested: the id rule and one-shot dispatch (all platforms), the
  window procedure with synthetic callback messages and that the new
  console is the child's standard input and output (Windows); NOT yet
  proven: that the Shell sends the click when a person clicks the Windows
  10/11 banner, a click from the Windows 10 Notification Center after it
  timed out, and that the URL handler keeps the `#conv=` fragment. Those
  need a real Windows desktop.
  **macOS** (osascript) has no click callback: the alert is a banner only.
  A click needs an app bundle with a UNUserNotificationCenter delegate (a
  signed helper app, a macOS build step and its distribution): an open,
  explicit gap that does not close the click-to-chat release acceptance.

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
