# Messenger architecture (reviewed design, partly built)

**Status:**
- Reviewed design. Codex, Agy and core Claude reviewed the initial draft, the
  author corrected it, and Codex checked the corrections.
- Parts are built, each on its own scoped assignment and listed below with its
  commits; the rest is still proposal. Being written here is **not**
  implementation approval, and the open owner decisions are in §15.
- Nothing is frozen: field, table, route and API names stay provisional until
  a release ships them.
- **Increments, not the messenger (owner correction, 2026-09-28).** What is
  built (S-A: the page over one installation's inbox, contacts by exact
  address, v1 reply-linked conversations, the member directory) and the planned
  N7 browser transport (S-W, v1 envelopes) are increments. They do **not**
  complete the messenger contract of DECISIONS §3.2 and §2. The required
  journey is:
  - choose a person (for example Vitalii), not a device address;
  - hold a human DM with them;
  - explicitly invite an agent, with the earlier context you allow;
  - see which participants are humans and which are agents, and who wrote
    each message;
  - ask the agent follow-ups until someone explicitly dismisses it.

  A message kind (message, question, task) is neither a participant's identity
  nor an agent invitation. An address names one installation's key, not a
  person. Nothing is merged by address, and no human label is made up.
- **Zoom: agents shown with their human (owner requirement, 2026-09-28).** In
  Zoom, a human is a node with visible links to the agent nodes that belong to
  them. The link means ownership or association only, and is drawn apart from
  online state and from being invited into a conversation. An agent invited
  into someone else's conversation stays linked to its own human. The link
  comes only from the reviewed identity and agent relationship (the person's
  roster, the agent's host installation and participation), never from a label
  or an address. There is no device-as-human shortcut in the meantime. It is
  part of the human-DM acceptance and of the final Zoom view.
- **Human-DM core: corrections accepted (2026-09-29).** Core Claude's
  `1d97945` adds, from the command line (`agentnet person create NAME`,
  `agentnet dm new|list|show|send`):
  - explicit person creation, one signed single-device roster per
    installation, pinned by peers, with a conflicting record frozen, never a
    replacement;
  - signed DM roots (E0) between two persons, each a separate conversation;
  - envelope v2 (conversation, logical id, origin as an assertion, emotion,
    explicit target) and capability records;
  - admission once per verifying key and logical id;
  - every conversation question or task held for the person: nothing runs
    it.

  Root review found defects, now corrected: a signed person conflict ignored
  on the cached and event path (R1) and the missing-proof queue starving past
  its oldest 50 items (R2), with DMs kept out of device history (C1, C2), in
  `9f5bea1` and `5da7fa1`; a frozen person got a deferred send and a new DM,
  fixed in `7247759`. Root accepted `7247759` (source checked; focused race
  tests of waiting, queued, fresh-conflict and normal-recovery sends passed),
  which closes the core's correction gate. The tests are core Claude's and
  root's evidence. Not built yet:
  - agent participation (invite, allowed context, follow-ups, dismissal) and
    execution;
  - Zoom's human–agent links;
  - linking devices, groups, browser devices.

  Human DMs are **not** complete.
- **Human DMs on the page: accepted (2026-09-29).** `4a0659c` (Agy's review
  passed, no defects) adds to the daemon's page, through the core's APIs only:
  - setting up your person by hand (never automatically);
  - people on your server shown by the name they claim until checked (the
    check happens when you start a DM);
  - separate DMs with a person, kept across restarts;
  - a message-only DM composer, with receipts. A question or task from them is
    held for you and nothing runs it; a frozen DM sends nothing.

  DMs stay apart from device history (the contacts by address). `9c1d4b4`
  (candidate, review pending) adds people search (by the name they give or
  their device, and DMs by their lines), a DM as an issue in Comic (held and
  kept captions, no decision controls), and Zoom's People group, a person's
  DMs, one DM and a write dialog that sends only to it. Not built: agent
  invitation and participation, Zoom's human–agent links (they wait for real
  participation data), linking devices, DMs on the relay's browser page.
- **Release gate: a whole-team pre-release review (owner request,
  2026-09-29).** Before this release, Codex (synthesis), core Claude, frontend
  Claude and Agy review a stable candidate SHA against the owner's requested
  outcomes: inventory, then each area and its cross-area failure cases, one
  cross-challenge round (two at most), an owner, fix and recheck for each
  confirmed defect, and only genuine product decisions to the owner. Release
  only when each outcome is verified or explicitly excluded by the owner;
  deferred requests stay visible. A build or a local commit is not a published
  release or a deployed messenger. The working checklist is root's
  `/tmp/agentnet-release-review-20260929.md` (temporary); its rows are:
  - R1 explicit persons, no merging, two DMs apart across restarts;
  - R2 agent invitation with selected context, explicit host acceptance,
    dismissal by either person;
  - R3 questions and tasks only for the exact host, key, scope and grants;
    nothing a person or replica sent runs by itself;
  - R4 Classic, Comic, Zoom and search agree, with agents linked to their
    human;
  - R5 a relay HTTPS URL hosts a usable human messenger (ciphertext-only
    relay, honest browser storage);
  - R6 Chrome install gives a separate app; updates strand no code or drafts;
  - R7 "Remind me later";
  - R8 one-command update with published binaries;
  - R9 truthful directory, presence and search;
  - R10 Pi attention, onboarding and the bundled skill;
  - R11 migration, dedupe, offline retry, key replacement, mixed versions;
  - R12 the complete inventory, known limits and platform qualification.

  All rows were pending when recorded; the inventory is not approval.
- **Also built since the design was written:** the People directory (`aec4d4f`,
  `b88f782`; §16); Pi attention (`2479a7e`, `4b1c7a2`, `587a1d5`;
  `docs/revival/M4.md`); the browser device's wire format and the relay's page
  handler (`306adaf`, `421f3b6`, `b61a494`), with nothing mounted and no
  browser device yet (§16, S-W).

**Sources of requirements:**
- `docs/DECISIONS.md` §2 (identity), §3.2 (persistent conversations and
  temporary participation, canonical) and §4 (avatars);
- owner steering for this assignment:
  - the frontend must work against the real backend;
  - phones must be able to join;
  - "ideally the relay should serve this";
  - the relay stays one compact, portable unit.

**Labels:** **Required** = owner requirement. **Proposed** = engineering
proposal under review. **Owner decision** = still open (§15). The disposition of
the consolidated review items 1–10 is in §18.

A first report **never** dismisses an agent (DECISIONS §3.2). Follow-ups stay
possible until an explicit dismissal. There is no automatic expiry: that is the
owner default.

---

## 1. What people do (plain-language journeys)

1. **Return after days.** Alice opens "Auth retry policy" with Bob on Thursday.
   It is separate from their "CI cache" conversation, with or without any model
   session.
2. **Talk as a group.** Alice starts "Deploy freeze" with Bob and Carol. Each
   message reaches each member's devices; everyone sees who sent what.
3. **Bring someone in with chosen history.**
   - Carol asks to add Dave. The conversation's keeper (Alice's device by default,
     §8) approves.
   - Dave sees the 6 earlier messages chosen for him and everything after he
     joined.
4. **Share an excerpt, go private again.** Alice shows two messages from her
   private chat with Bob to "Deploy freeze" as a copy marked "shared by Alice".
   Nobody gains access to the private chat.
5. **An agent steps in and stays for follow-ups.**
   - Bernard's agent is invited with selected history and reports.
   - Ruslan asks a follow-up question; it answers without a new invite.
   - When dismissed, it stops receiving the chat.
6. **Join from a phone.**
   - Dana opens an invite on her phone and joins as her own device, `dana/phone`.
   - She messages Alice's laptop, goes offline and returns to the same
     conversation (§12). Group use comes with S-C.
   - Offline notifications come with push, after envelope v2 (S-P).
7. **Nothing old runs again.** Joining, syncing, sharing or re-delivering old
   messages never re-runs work.

Comic, Zoom and Classic are views over the same conversations.

## 2. Components

```
            ┌──────────────── Relay: ONE binary / container, ONE data dir, ONE port ───────────┐
            │  HTTPS (platform-terminated, or own certificate)                                 │
            │  ├─ /v1/*  signed API: messages, receipts, SSE stream, blobs, directory,         │
            │  │         sessions, join/invites; new: capability records, push subscriptions   │
            │  └─ /      web app assets embedded in the binary (the same frontend bundle)      │
            │  /data: hub.db (SQLite), blobs, TLS files, push (VAPID) key — ciphertext only    │
            └──────▲───────────────────────────────▲───────────────────────────────────────────┘
                   │ signed, age-encrypted envelopes │ page + signed API from the browser
   ┌───────────────┴───────────┐          ┌──────────┴───────────────────────────┐
   │ Laptop: agentnet daemon   │          │ Phone / any browser: "browser device"│
   │  store (SQLite)           │          │  keys: WebCrypto (non-extractable)   │
   │  worker, grants, sessions │          │  store: IndexedDB; service worker    │
   │  UI host on loopback ─────┼─► same   │  engine: sign, age, relay API, outbox│
   │  (keys stay in daemon)    │  bundle  │  human-only: runs no agents          │
   └───────────────────────────┘          └──────────────────────────────────────┘
```

**Preferred end state:** the relay serves the app by URL for phones and
daemon-less browsers.

**Laptops with a daemon** use the same bundle hosted by the daemon. Keys and
agents stay local there. This is the long-term desktop engine, not a throwaway
app.

**No crossing between the two hosts.** The relay page never talks to a local
daemon, and the daemon UI refuses other origins (existing Host/Origin guard,
`internal/ui/server.go`).

## 3. Where authoritative state lives

| State | Authority | Location |
|---|---|---|
| Message content | sender's signed, encrypted envelope | each entitled device's store |
| Delivery states | relay or direct-peer receipts | sender, per recipient device |
| Read state | the reading device | local; no read receipts |
| Membership and history grants | keeper-signed epoch chain (§8) | verified and held on each member device |
| Participation | the agent's owner daemon, announced as events | owner daemon + visible events |
| Execution | the agent's owner daemon (worker, grants, job state and attempts) | laptop daemon only |
| Device keys | the device | daemon home / browser key store |
| Capability records | the device, signed | relay stores; peers verify |
| Push subscriptions | the browser device | relay (endpoint + allowlist; no content) |
| Device roster (DECISIONS §2, later) | device-signed transitions | relay (public) + peer pins |

The relay holds ciphertext, routing metadata, public keys, signed records and
push endpoints. It holds no plaintext and no private keys.

## 4. Devices, principals and identity

- **Laptop daemon device:** today's installation. It can run agents.
- **Browser device:** its own admin invite, its own address (e.g. `dana/phone`),
  its own keys.
  - **Human-only:** it has no worker, so it never executes anything.
  - Its capability record says so (§9.1), so peers do not address tasks to it.
- **Principals today.**
  - Members and keepers are **device keys**: each entry is `(address, key
    fingerprint)`, and authority follows the fingerprint, never the address or
    label alone. `dana/phone` and `dana/laptop` are two members, shown as two
    devices.
  - **Key replacement does not inherit rights.** If a member's pinned key changes
    (today's "key changed → `agentnet trust`"), the old entry no longer matches.
    Until the keeper publishes an epoch with the new fingerprint, that address is
    shown but holds no membership, participation or task authority.
  - They become one human only through explicit linking (DECISIONS §2, slice S-E),
    never by matching labels.
  - When linking lands, a member entry becomes a human id that resolves to roster
    devices. The group rules below keep working, because they name members, not
    labels.

## 5. The relay as one portable unit (Required)

- **What it is:** one Go binary and one container image. There is no separate
  frontend server, Node, Redis or Postgres. Web assets are embedded (`embed.FS`),
  as `internal/ui` already does.
- **Storage:** one data directory (volume `/data`) holds `hub.db` (SQLite WAL),
  blob files, TLS files (self-managed mode), and the push key pair.
  Subscriptions and capability records live in `hub.db`.
  - `hub backup` already archives every regular file in the data directory
    except lock/WAL/SHM/`.bak` files (`internal/hub/maint.go:147,155`). Push keys
    and subscriptions are therefore backed up and restored with everything else.
  - Restoring an old backup restores old subscriptions. Stale ones fail at the
    push service and are removed.
- **Port:** one listener (`--listen`, or the platform's `PORT`) for API, stream
  and app.
- **HTTPS modes:**
  - **Platform-terminated TLS** (`AGENTNET_PLATFORM_TLS=1`, documented in
    `INSTALL.md:134-160` but **not yet deployed to Railway**, `:146`, `:260`).
    Browsers work, since the platform has a CA certificate.
  - **Self-managed certificate** (VPS default today). It is self-signed and pinned
    for daemons, and **browsers reject it**.
  - **For browser devices on a VPS,** the relay needs a CA-valid certificate.
    Proposed options: built-in ACME (`golang.org/x/crypto`, already an indirect
    module), an optional reverse proxy in front, or a TLS-terminating platform.
    This is an engineering choice for review, not an owner decision.
- **Stream:** Server-Sent Events on `/v1/stream` (`internal/hub/stream.go:141`),
  with a 90 s heartbeat (`protocol.HeartbeatInterval`, `protocol.go:232`) and
  signed acknowledgements. Clients reconnect with jittered backoff
  (`internal/client/daemon.go:31`).
  - **Railway's published limits:** responses up to 15 minutes while data flows,
    closed after 5 minutes without data, idle HTTP/1.1 closed after 60 s,
    WebSockets exempt.
  - **Fit:** the 90 s heartbeat is inside the documented idle limit. Expect a
    stream restart at most every 15 minutes, a bounded reconnect with custody
    preventing loss. This is **not live-tested on Railway.**
  - **Browsers** send the signed `X-Agentnet-*` headers (`protocol.go:236`) via
    `fetch()` streaming, because `EventSource` cannot set headers.
- **Web Push:** gated. See §12.3.

## 6. Hosted access: trust, storage, discovery (honest tradeoff)

**Code trust.**
- A browser device runs whatever code its origin serves. Whoever controls the
  relay, its host or its TLS can serve modified HTML, script or service worker to
  a chosen user.
- That code can use the device's non-extractable keys (sign, decrypt) and read
  plaintext in the page.
- "Non-extractable" only prevents exporting the raw key bytes. It does not
  protect against code running in the same origin.
- Service-worker version pinning, Subresource Integrity, strict CSP and "no
  third-party scripts" do **not** protect against a malicious origin. They are
  defence-in-depth for narrower threats, such as a compromised third-party asset
  or injected markup.
- So for browser devices, "the relay is blind" holds only while the relay serves
  honest code. Laptop daemons are unaffected, except that what they send to a
  browser device is exposed with it.

**Alternatives for the owner** (§15):
- (a) accept relay-served code trust for browser devices (the preferred hosted
  experience);
- (b) laptops only via daemon, with no browser devices;
- (c) a native mobile client, whose code trust comes from release signing and app
  stores, at the cost of a second codebase, store accounts and push credentials;
- (d) (a) now, with a later code-transparency mechanism if one matures.

**Keys.**
- Keys are generated in the browser as non-extractable WebCrypto keys: Ed25519
  signing and X25519 for age; `typage` accepts non-extractable X25519 keys.
- Nothing goes to the relay. The keys cannot be backed up.

**Storage.**
- In the Safari browser, script-writable storage is removed after 7 days without
  use. Home Screen web apps are exempt from that cap, but the exemption is **not**
  a guarantee of indefinite storage.
- The app requests `navigator.storage.persist()`. Browser or OS clearing, or loss,
  is still possible anywhere.
- **Loss = a new device:** rejoin and revoke the old one. Old history returns only
  through an authorized transfer (owner decision).

**Discovery.**
- The invite link carries the relay URL and secret in the URL **fragment**, which
  is not sent on page load.
- Joining uses the existing signed join. Peers are found through the signed
  directory with TOFU pinning. Changed keys block, as today.
- The browser trusts the relay's TLS via a CA, not a pin.

## 7. A1: reuse vs change map

| Area (source) | Reuse | Change needed | Why existing data cannot suffice |
|---|---|---|---|
| Envelope (`internal/envelope/envelope.go`) | age, Ed25519, strict decode | **envelope version 2** (§9.1) | Outer and inner `V` and `Kind` are signed and cross-checked (`Seal :127`, `VerifySig :160`, `Open :194`). `validKind` rejects new kinds. The **relay itself calls `VerifySig`**, so an Inner-only change is not compatible |
| Relay (`internal/hub/api.go:18-40`, `stream.go`) | API, SSE stream, blobs, directory, sessions, invites, backup | accept v2 envelopes; capability records; feature list on `/v1/version`; embedded app; gated push | these do not exist |
| Local store (`internal/client/store.go`) | inbox/outbox and reply links as the message table; attachments; receipts; task grants; approvals | append steps: conversations, epochs, message links (`lid` + sender fingerprint on the inbox row), participations, attempt audit | Reply roots cannot name topics, membership, history grants or logical identity |
| Conversation walk (`conversation.go`) | per-peer reply-graph traversal | the legacy derivation for v1 threads | one peer only |
| Worker and grants (`worker.go`, `respond.go`, `taskgrant.go`, `store.go:755 claimJob`) | question/task modes, accept once, standing grant for one exact key, needs_human | job gate (§10), admission dedupe + auditable attempts on the existing job state (§9.4), participation authority (§9.3), bounded context builder | Today's claim follows per-address approvals and task grants, and replays 4 ancestors (`worker.go` `threadText`) |
| Sessions (`session.go`) | clean-head resume | add scope + audience to the resume key | DECISIONS §3.2 |
| Kick socket (`kick.go`) | CLI wakes the daemon | audited wake after every commit (§11) | some writers may not kick today |
| UI (`internal/ui`, `uicmd.go`) | Provider seam, guard, SSE counter, text-only rendering; demo fixtures as the test engine | real daemon Provider; relay-served bundle + browser engine | the demo has fixtures only |

**New code:** the browser engine, a second client of the existing wire protocol.
It is checked against test vectors generated by the Go code.

## 8. A2: conversations, membership authority and history grants

- **Conversation:** a stable random id, a title, and a membership chain. A DM is
  two members; a group is more. Separate conversations with the same people are
  separate topics. A "team" is a saved member list, never the whole network.

**Keeper-signed epochs (Proposed default; the authority choice is for owner
assessment, §15).**

- **Keeper.** One keeper device key per conversation, by default the creator's:
  `(address, fingerprint)`.
- **Epoch.** An epoch `E_n = {conv, n, prev = hash(E_{n-1}), keeper, members,
  grants_for_new_members}`.
  - `keeper` and every member are `(address, key fingerprint)`.
  - It is signed by the keeper's key and sent as a v2 event to the union of old
    and new members. There is no new service.
- **Root and bootstrap.**
  - `E_0` is created by the creator, names the creator as keeper, and is signed by
    that key.
  - The **conversation id is `hash(E_0)`**, so a conversation cannot be claimed
    without its root.
  - To avoid circularity, `E_0`'s canonical signed and hashed form **omits**
    `conv`. Later epochs name the derived `conv`. The encoding stays provisional.
  - A device accepts `E_0` only if it arrives in an envelope that verifies under
    the key **already pinned** for the sender's address (existing TOFU), and that
    key's fingerprint equals `E_0.keeper`. The device then pins the root hash for
    that conversation.
  - A newcomer receives the whole chain `E_0…E_n` with the epoch that adds them
    and verifies it from the root. Their first view of the conversation therefore
    rests on the keeper's pinned key. That is the same TOFU limit as today, stated
    in the UI.
- **Unknown conversation or epoch.** A message naming a conversation whose root is
  not pinned, or an epoch beyond the verified chain, is:
  - stored as **pending membership proof**;
  - shown apart and labelled unverified;
  - never counted as a member message;
  - never executable.
  The device asks the keeper for the chain. If the chain never arrives, the
  message stays pending. There is no silent trust bypass.
- **Requesting a change.** Any member may *request* an invite or removal by
  sending a request event. Only the keeper *authorizes*, by publishing the next
  epoch after its person approves.
- **Accepting an epoch.** A device accepts `E_{n+1}` only if it is signed by the
  keeper **key** named in its held `E_n`, has the next number, and its `prev`
  matches.
  - **Missing predecessor:** hold it and ask the keeper for the chain. Nothing is
    applied.
  - **Fork** (two valid, different `E_{n+1}`): freeze membership changes and
    fan-out for that conversation. Show "membership conflict". Fail closed and
    never pick the newest timestamp.
- **Keeper unavailable.** Membership is frozen; messaging among current members
  continues. A keeper may name a successor key in an epoch (future).
  - A lost keeper device, or a **replaced keeper key**, leaves the membership
    frozen. The new key cannot continue the chain. Members can start a new
    conversation with excerpts.
- **Sending.** Senders fan out to the members of the epoch they hold. Messages
  carry that epoch number as a claim.

**Display vs authority.**
- A claimed epoch is **not** proof of send time: a removed member's key can still
  claim an old epoch.
- **Membership check.** "Member" always means the sender's verifying key
  fingerprint matches its entry in the receiver's current epoch.
- *Display:* a message whose verifying key is not a member in the receiver's
  current epoch is shown, marked "received after <member>'s removal was known
  here" or "not a current member key".
- *Authority:* execution and participation rights are evaluated **at claim time
  against the receiver's current epoch**. Once a removal or key change is known,
  no message from that key creates a job or participation effect, whatever epoch
  it claims.
- *Limits:* removal is not instant or global. Devices that have not yet received
  `E_{n+1}` keep sending to the removed member. The relay can delay or withhold
  epochs, and freshness is not guaranteed. Admin revoke remains the network-wide
  stop.

**History grants.**
- `grants_for_new_members` names what each newcomer may receive: selected logical
  message ids, or "from join".
- The keeper, or the inviter at the keeper's authorization, sends a **history
  excerpt**: snapshots of exactly those messages, encrypted to the newcomer and
  stored as **replicas**. Excluded messages are never sent.
- Each snapshot is labelled "shared by <sender>" with original author and time as
  that sender's claim. Today's signature covers ciphertext for the original
  recipient (`envelope.go:120 signed()`), so it cannot prove authorship of a copy.

**Other rules.**
- **Private aside or sharing** uses the same excerpt, into another conversation,
  with a local backlink shown only to people in both. The source membership is
  unchanged, and copies already shared cannot be recalled.
- **Messages are immutable;** a correction is a new message.
- **Days later:** only the local store is needed. Model sessions are optional.
- **Legacy v1 peers:** threads are derived from reply roots, recomputed on read
  and never persisted. v1 peers cannot be members of epoch-managed conversations,
  and the UI says why.

## 9. A3/A4: protocol, participation, idempotency, context

### 9.1 Envelope version 2 and compatibility (Proposed)

**Envelope v2 is a new, fully signed version, not a hidden field.**
- **Outer and inner versions:** outer `V=2` and inner `V=2`, still cross-checked
  as in v1.
- **New signing domain:** `agentnet-envelope-v2\n`, so v1 and v2 signatures
  cannot be confused.
- **Outer kinds:** the **existing five**. Membership and excerpts travel as outer
  `message` with an inner subtype (`sub: event|excerpt`), so the relay learns
  nothing new about them. `Open` keeps checking inner Kind = outer Kind.
- **Inner additions:** `conv`, `epoch` (a claim), `lid` (logical id, the same
  across per-device copies), `sub`, `replica`, `origin` (`ui` / `agent:<harness>`
  / unspecified, an assertion) and `emotion` (§13).
- **Outer addition:** `attn` (gated, §12.3).

**How clients learn what is supported.**
- **Relay support:** a `features` list added to `GET /v1/version`. Old clients
  decode Hub responses leniently (`internal/client/transport.go:134`), so they
  ignore it. No field means an old relay, so the client sends v1 only.
- **Peer support:** a separately signed **capability record** `{address,
  session, caps, ts}` under the domain `agentnet-caps-v1`, stored and served by
  new relays at a new endpoint.
  - Each daemon or browser session publishes a record. The relay serves the
    device's **latest** record together with the session it came from, and whether
    a newer session has connected without publishing one.
  - Senders cache the last verified record per device. An offline peer keeps its
    capabilities, so v2 work is sent into custody as normal.
  - **Unknown** (no record ever): v1 for legacy 1:1 threads only. The device
    cannot be added to v2 conversations.
  - **Changed** (a newer session published none, or published fewer
    capabilities): the sender does **not** silently downgrade conversation-scoped
    v2 work to v1. It holds that work in the outbox, visibly: "waiting: <device>'s
    AgentNet can't read this conversation".
  - **Conflicting concurrent sessions** of one device: use the least capable.
  - v2 envelopes already queued or in custody before a downgrade may still be
    quarantined by the old client. Quarantine is visible; there is no promise of
    zero quarantines under races (§19).
  - Peers verify it against the pinned key. A relay can withhold it but cannot
    forge it. Withholding means v1 **only for legacy pairwise threads**.
    Conversation-scoped v2 work is held, never downgraded.
  - An old relay returns 404, which means v1.
- **Signed session ads stay exactly v1.** Their signatures re-marshal the struct,
  and old relays verify the old struct (`protocol.go:341`), so they are not
  extended.

**Compatibility matrix:**

| Relay | Sender | Recipient | Result |
|---|---|---|---|
| old | any | any | v1 only (no `features`) |
| new | old | any | v1 as today |
| new | new | old or unknown (no capability record) | v1 to that device; it cannot join epoch conversations |
| new | new | new (verified `env2`) | v2 |
| any | – | a v2 envelope reaches an old client by mistake | `VerifySig` rejects it; stored as quarantined, and the sender sees "quarantined". Fail closed |

The rollout is relay-first: upgrade the relay, then clients.

### 9.2 Participation lifecycle (Required, DECISIONS §3.2)

`invited → working → available ⇄ working → dismissed`

- **invited → working.** The owner daemon accepts with an explicit **assignment
  scope**:
  - history (a grant from the conversation);
  - report audience (the conversation, or drafts to the owner);
  - job;
  - optional **named follow-up task allowance** (see §9.3).
  Acceptance is announced as a visible event.
- **available.** After a report the agent stays, with no model calls and no
  polling. A member's addressed message creates at most one job (§9.4).
- **dismissed.** Only an explicit, authorized dismissal ends it. After that there
  is no context and there are no jobs. History stays, and nothing already read is
  erased. A new invite starts a new participation.
- **Owner decisions for a two-person DM (2026-09-29):** either person may
  dismiss an invited agent. Selected earlier messages of the DM may be shared
  with the agent; the selection is visible to both people, and the other person
  gives no separate approval. The agent's host still accepts the invitation
  explicitly.
- **Not dismissals:** the first report, silence, a restart, a thank-you. No
  automatic expiry.

### 9.3 Participation authority (Proposed)

**Questions.** An accepted participation is **bounded authority for questions**:
- **from:** senders whose **verifying key fingerprint** is a member of the
  receiver's current epoch and entitled to the conversation;
- **in:** that conversation, addressed to that agent, within the assignment scope;
- **how:** in question mode.
This works **independently of the per-address approvals table**. Without it,
today's claim would hold a different member's follow-up (`claimJob`,
`store.go:755`).

**Tasks** still need real task authority, one of:
- accept once;
- the existing standing grant for that sender's **exact key**;
- the assignment's **explicit** follow-up task allowance, recorded as structured
  data at acceptance and bound to member key fingerprints (e.g. "follow-up tasks
  from Ruslan's key within this assignment").

**No inheritance.** One member's standing grant never covers another member, and
a replaced key inherits nothing (§4). Task boundaries are never inferred from
message text: anything unclear waits for the person.

**No repeated approval.** A clarification, correction or question inside an
accepted scope is not re-asked.

### 9.4 Idempotency and context (Proposed)

**Admission and execution are separate.**

**Admission: each logical request is stored once.**
- v1: the inbox id is already unique (`INSERT OR IGNORE`, `store.go:376`).
- v2: an appended step adds `lid` and the sender key fingerprint to the inbox row,
  with a unique index on `(sender key fingerprint, lid)`.
- A duplicate envelope, re-delivery, or a copy with a different envelope id but
  the same `(sender, lid)` is not inserted again. It is only acknowledged.
- `lid` is a random 128-bit id per sender, so the conversation is not needed in
  the key. Dedupe is per home; cloned homes are not deduplicated globally.
- Replicas, excerpts and events are admitted as history but are never executable
  (§10).

**Execution attempts reuse today's job state on that one row.**
- `pending`/`accepted` → `running` → `answered`, `failed`, `interrupted`,
  `cancelled` or `needs_human`, claimed by the worker's existing atomic update
  (`claimJob`, `store.go:755`). Only one attempt runs at a time.
- **No automatic retry.**
- An explicit, authorized re-run stays possible exactly as today: `Accept`
  re-queues interrupted, failed, cancelled or needs-human questions and tasks
  (`respond.go`).
- Each attempt is auditable: attempt count and last attempt time go in an appended
  column. Each attempt's reply links to the same request, so peers can see several
  results after an explicit re-run.
- No extra claims table.

**Context builder.**
- Inputs: the participation's history grant plus messages addressed to the agent,
  never other conversations.
- **Bounded bytes** (a configured cap), newest first within scope. The prompt and
  the UI both state "N earlier permitted messages omitted".

**Native sessions** are reused only when harness/mode/dir/preset **and**
scope/audience match. A scope or audience change starts a fresh context.

## 10. A4: honesty rules

- **Per-device delivery.** Fan-out goes to member devices. Linked devices later
  add roster fan-out and `replica=true` self-sync copies.
- **Job gate (laptop daemon only).** A job needs **all** of the following:
  - not a replica, excerpt or event;
  - kind question or task;
  - addressed to this device's agent;
  - a sender key that is a member in the receiver's **current** epoch (multi-party);
  - authority per §9.3, or today's 1:1 rules, checked against the verifying key;
  - admitted once, and claimed through the existing job state (§9.4).
  Browser devices never create jobs.
- **One execution target** per question or task.
- **Identity limits.**
  - Signatures prove device-key control, not keystrokes.
  - `origin` can be imitated by same-user software.
  - First contact is TOFU.
  - Rollback is detected; withheld updates are not.
- **Keys.**
  - Keys are per device and never copied.
  - Static age keys give no forward secrecy.
  - Removal stops future sends only.
  - A new device cannot read old ciphertext.
  - A ciphertext archive, forward secrecy and group rekeying each need their own
    bounded design. No new cryptographic protocol is proposed.
  - Multi-relay mesh is future work.

## 11. A6: every screen and action maps to the real backend

The frontend uses one **Provider contract**: JSON operations plus an
invalidation stream.
- **Daemon engine:** loopback HTTP into existing `client.Agent` methods.
- **Browser engine:** in-page code speaking the relay API.
- **Demo fixtures:** a test engine only.

| Screen / action | Daemon engine | Browser engine | Relay API | Status |
|---|---|---|---|---|
| Conversation list | derived v1 threads (S-A); conversations (S-B) | IndexedDB | – | existing / new tables |
| Open conversation | `Agent.Conversation` + store | IndexedDB | – | existing |
| Send / reply | `Agent.SendMessage`, `Agent.Reply` (`client.go`, `respond.go`) | sign + age in page | `POST /v1/messages` | existing / new engine |
| Files | `files.go` | chunked age streams (after text) | `/v1/blobs/*` | existing / new engine |
| Delivery states | outbox + receipts | local outbox + receipts | `/v1/messages/{id}`, `/wait`, stream acks | existing |
| Review: accept / decline / approve / resolve / cancel | `respond.go` | not available (human-only; replies by hand) | – | existing |
| Task grants | `taskgrant.go` | not available | – | existing |
| Trust a changed key | trust | same rule | directory | existing / new engine |
| Presence | `Agent.Sessions` | same | sessions | existing |
| Request / approve membership; share excerpt | epoch events; keeper approval | request + display | `POST /v1/messages` | new (S-C) |
| Invite / accept / dismiss agent | participation; owner daemon accepts | request only | `POST /v1/messages` | new (S-D) |
| Join | `agentnet join` | in-page join | `POST /v1/join` | existing / new engine |
| Capabilities | publish on start | publish on join | new capability endpoints | new (S-B/S-W) |

**Truthful states:**
- queued (offline);
- custody;
- delivered (never "read");
- quarantined;
- expired;
- failed;
- key changed (sending blocked);
- revoked;
- relay unreachable;
- storage full;
- "older AgentNet";
- membership conflict;
- "N earlier messages omitted".

**Invalidation (no polling).**
- **Every mutating CLI or API path** wakes the daemon after a successful commit.
  Audit these and add a test that each one wakes: send, reply, accept, decline,
  resolve, cancel, approve/unapprove, trust, grants, responder changes, read
  marking, download bookkeeping.
- **The daemon's own writers** (receive loop, receipts, worker, epoch
  processing) signal the UI directly, in process.
- **Honest recovery:** if a writer crashes between commit and wake, the page is
  stale until the next event, a page refocus or reload (it refetches), or a daemon
  restart. `PRAGMA data_version` is not used as a correctness mechanism.
- **Browser engine:** raises its own events and relay stream events. One tab owns
  the stream; the others follow via a same-origin channel (Web Locks +
  BroadcastChannel, to verify per browser). The page refetches on focus.

## 12. A7: joining from a phone (browser device)

### 12.1 S-W journey: phone ↔ laptop DM (v1, achievable before groups)
1. The admin sends Dana an invite. She opens the link on her phone, and the page
   explains: "This phone becomes the device dana/phone".
   - On iPhone she adds it to the Home Screen, which is needed for durable storage
     (and later push).
2. The page creates non-extractable keys and joins with the existing signed join.
3. Alice's laptop daemon sends Dana a message. With the app open, the phone
   receives it over the stream and Dana replies. The page signs and encrypts, and
   the laptop receives it in its existing inbox.
4. **Offline on a train:**
   - Dana's replies queue in IndexedDB;
   - Alice's new messages wait in relay custody;
   - Alice sees "custody", never "delivered".
5. **Back online:** she reopens the app. It reconnects, drains custody, sends the
   queue, and shows the same thread (derived from reply links, as on the laptop).
   - **No offline notifications in this slice.** Push needs the v2 attention flag
     (§12.3), so on v1 the phone shows new messages only when opened. The UI says
     so.
6. **Lost phone:** the admin runs `agentnet admin revoke dana/phone` (existing).
   Nothing it stored is recalled.

`dana/phone` is **not** the same human as any `dana/laptop` until explicit
linking (S-E).

### 12.2 Later journey (after S-C)
The same phone is added to "Deploy freeze" through a keeper-approved epoch with
selected history, participates, goes offline and returns to the conversation by id.

### 12.3 Push: attention only (slice S-P, after S-B; contract reviewed before coding)

**What wakes the phone.** Only messages meant for Dana's attention. Background
agent traffic, replicas, events and excerpts stay quiet.
- The relay cannot see `Inner.Status` (e.g. `review_notice`) or the recipient's
  needs-human state, and the outer kind alone is not attention.

**Proposed minimal contract.** The relay pushes to a browser device only if **all**
of these hold:
- the envelope carries a sender-set **v2 outer `attn` flag**. It is a hint, not
  authority. Senders set it only for human-directed messages, never on replicas,
  events or agent background replies.
- the sender is on the **receiver-controlled allowlist** stored with the
  subscription (the relay already sees `from`);
- a per-sender and per-device **rate limit** holds;
- the device has no live stream.

**What the push contains.**
- The payload is empty or content-free. iOS allows push only for Home Screen apps,
  subscribed on a user gesture, and every push must show a notification, so it
  says "New activity".
- There are no silent pushes. The page does not run in the background, and
  decryption happens when it is opened.

**Implementation.** Select an audited Web Push implementation, or validate a
minimal one against a reference test suite. The deciding factors are correctness
and validation effort, not counting dependencies. It is a build-time library, not
a runtime service.

Push depends on the v2 `attn` flag, so it cannot ship with v1-only S-W. It is its
own slice, **S-P**, after S-B.

**Notification content** follows existing owner direction: content-free and
attention-only. Richer, locally decrypted notification content is a possible later
option, not a gate.

## 13. Emotional avatar reactions (Required, DECISIONS §4)

- **Agent turns must carry an emotion.** Every agent-origin v2 turn **must**
  include `emotion`, emitted by the sending agent with its turn. There is no
  extra model call.
- **Truthful neutral fallback:**
  - human turns;
  - legacy (v1) peers;
  - a missing value, labelled "no emotion sent";
  - missing assets.
- **Independent of status:** emotion is independent of working, waiting and done,
  which come from participation and job state.
- **Deferred:** vocabulary, art and asset delivery. Nothing is created now.

## 14. Engineering choices for review (not owner decisions)

- **Browser engine:** TypeScript with WebCrypto + `typage`, vendored into the
  embedded bundle (no npm at runtime), or the Go core compiled to WebAssembly
  (raw extractable keys). Choose with Go-generated interop vectors and bounded
  validation, not by dependency count.
- **VPS TLS for browsers:** built-in ACME vs optional proxy vs platform TLS.
- **Web Push:** an audited library vs a validated minimal implementation.
- **Capability endpoint:** exact shape and caching.
- **Epoch encoding and hash;** the keeper-successor event format.
- **Context byte cap** and the wording of omission notes.

## 15. Owner decisions

**Needed before the named slice.**
1. **Before S-W:** hosted-access code trust (§6). Accept relay-served code for
   browser devices, or choose daemon-only laptops or a native client.
   *Owner direction so far:* the owner asked for relay hosting. Engineering
   follows that direction with §6's limit stated on the page and nothing
   activated in production; this record is not a separate sign-off on §6's
   tradeoff.
2. **Before S-C:** the group authority default. The proposal is keeper-authorized
   epochs, any member may request, and the creator's device is keeper.
   Alternatives: several keepers, or any member authorizes, with weaker
   fork/removal guarantees.
**Decided by the owner (2026-09-29), for two-person DMs:**
- Either person may dismiss an invited agent.
- Selected earlier DM messages may be shared with an invited agent, the choice
  visible to both people, with no separate approval from the other person.
- The host's acceptance of an invitation stays explicit.

**Later (not blocking S-A or S-B).**

3. Recovery authority after losing all devices (DECISIONS §2).
4. Prior-history entitlement for a newly linked or joined device (DECISIONS §2).
5. Emotion vocabulary, art direction and asset provenance (DECISIONS §4).
6. Whether participations ever expire. The default is no automatic expiry; that
   is already the owner requirement, not a blocker.

## 16. A5: rollout

**S-A: the frontend on the real backend (laptop).**
- **Status:** implemented (`agentnet daemon --ui`, `internal/ui/live.go`):
  Classic, Comic and Zoom over the same Provider (`static/lenses.js`). Faces are
  initials only, since authorship by person or agent is not recorded and
  emotions are S-B/§13. Accepting a task and letting the responder run it are
  covered by the existing worker tests, not by a model run from the page.
- **Build:**
  - the daemon hosts the bundle;
  - the daemon Provider sits over the existing inbox/outbox;
  - derived v1 threads;
  - Classic, then Comic and Zoom;
  - existing `Agent` actions;
  - audited wake-ups (§11).
- **No wire or schema change.**
- **Journeys:**
  - real send and receive with a real peer;
  - reply;
  - accept a held task under existing grants;
  - `agentnet send` in a terminal appears live;
  - restart keeps everything;
  - quarantined and key-changed states render.
- **Why first:** it proves the Provider contract and the bundle on real data with
  no new crypto code. It is not a disconnected app, and the same bundle is what
  the relay serves in S-W.

**Next release scope (recorded 2026-09-28).** Implemented so far: the v1
navigation part of contacts and conversations, search of known agents and
conversations, separated review notices (see "In this build" below), the
People directory and Pi attention (below), and the human-DM core with its page
candidate (status at the top). S-A was qualified at `0760ccc` (all four CI jobs
passed then). The next release bundles:
- **S-W** as designed below: the relay serves the page over HTTPS to a browser
  or phone. §6's code-trust limit stands: a browser device trusts the code the
  relay serves. *In source:* the wire format checked against the Go code and
  the relay's page handler (`306adaf`, `421f3b6`, `b61a494`); not mounted, and
  the browser's conversation screens wait for the person, DM and
  participation contract.
- **People directory:** the enrolled members, each with presence online,
  reconnecting, offline or unknown, so a newly joined member (for example
  Vitalii) can be found without knowing their exact address. It is kept current
  by server events on the existing push stream, not by polling. Online means a
  daemon is connected, not that a person is present. Being listed grants no
  trust or permission: keys, approvals and task grants work as today.
  - *In this build:* the relay lists members (`GET /v1/agents`, newest first,
    at most 1,000) and pushes changes on the stream; the page shows them
    through the same Provider in Classic, Comic and Zoom. Members that are not
    yet contacts appear under "Also on your server" and in search as kind
    Agent (exact address, when they joined); contacts show presence beside
    their name. Presence is shown only while this daemon is connected to the
    relay; otherwise the list is marked as of its time and presence as not
    known. A relay that does not list members is named as needing an update;
    a cut list says so. Choosing a member opens the existing contact or a new
    conversation draft addressed to them; it never sends, pins, trusts or
    grants. Evidence: `internal/ui` live test on a real test relay (arrival,
    connect, disconnect, revoke, relay gone), page checks, and a real
    browser on synthetic homes with no reload.
- **Pi native attention:** built for upstream Pi 0.87.1 (`2479a7e`,
  `4b1c7a2`, `587a1d5`; details and limits in `docs/revival/M4.md`): a Pi
  session is told what arrived, as metadata, at session start, with a prompt,
  at the end of a run and while idle, without starting a model turn. Evidence:
  tests and a live Pi run in RPC mode without a model; not run in an
  interactive Pi TUI or with OMP.
- **Contacts and conversations** (reviewed direction below, corrected by the
  owner).
- **Search (required by the owner):** one clear search entry finds agents
  (exact addresses) and conversations, and later people. Results say which kind
  each is and show the address or identity context when names match, so
  same-named results can be told apart; choosing one opens exactly that contact
  or conversation, never a guess or a merge (contact, then its separate
  conversations, then messages, as below). Search reaches only what the
  directory and local history already show and grants no trust or permission.
  Person results are not built: persons now exist only as explicit
  single-device records (human-DM core), and search does not reach them yet;
  an address's label (such as "admin") is never shown as a verified person.
  How search is built (index, service, any AI) is not decided here.

Still open, not implemented: a Chrome standalone/PWA install. S-R (reminders)
keeps its status above. No group work is in this release, and identity is
limited to explicit single-device persons: no linking, merging or recovery.

**Contacts and conversations (reviewed direction, corrected by the owner
2026-09-28; v1 navigation implemented, named conversations not).** This supersedes the contact-wide history
recorded earlier the same day (715ac2a).
- **Problem:** agents send most messages without `reply_to`, so every v1
  reply-linked thread is one message long. One contact then shows about 30 rows
  or Zoom cards, and remote review notices crowd the list.
- **Model:** one row per contact (exact address), holding that contact's
  separate conversations, each holding its messages. For admin/zenbook, for
  example: "AgentNet rollout", "Pi cleanup", another discussion. Clutter goes
  down by grouping under the contact, never by merging conversations. Nothing is
  merged by person label or inferred human identity.
- **The selected conversation owns the composer, draft and context.** A normal
  send continues it; an explicit "New conversation" starts another. A reply
  target is optional inside a conversation and never permission to combine
  jobs.
- **Never from a harness session:** conversation identity is not derived from
  a native harness session, which is local execution state that may restart or
  branch while the chat goes on. Task, grant and session isolation are
  unchanged.
- **Old v1 material:** reply links prove a chain; a message without one proves
  neither the same topic nor a different one. Such messages stay reachable
  under their contact without being assigned to a conversation by content,
  time, key or session, and without being called one conversation. How they
  are shown and later sorted is unresolved. No automatic (AI) grouping.
- **All activity** per contact is optional and display-only: a time-ordered
  view that is never a send target or a source of context.
- **Identity:** named, shared, persistent conversations are S-B's stable
  conversation identity (§8, §9.1 `conv`), so they need that wire and schema
  work. Only the contact navigation itself is view-only.
- **Kept from the review:** counts per contact are never summed (unread,
  actionable here, remote notices); each question or task and its result stays
  its own targeted card and "done" is never inferred from a reply; paging is
  stable (no message moves, repeats or is skipped).
- **Remote review notices:** grouped per sender into one line with the latest
  *reported* count and time, never shown as that machine's current queue. Every
  record stays in Details. Dismissing clears only the local notices. No approve,
  accept or run control for remote requests; a machine command is never the
  main instruction to the person.
- **Lenses:** Classic, contact rows that open into their conversations; Zoom,
  people, then a contact's conversations, then a conversation, then a message;
  Comic, a series per contact and an issue per conversation.
- **Unresolved, next release:**
  - a notice has no request content and no allowed response route, so it
    cannot ask the person to decide anything; that needs both. Until then no
    automatic remote query or task and no new authority;
  - how old unlinked messages are shown and sorted.
- **In this build (v1, no wire or schema change):** one sidebar row, Zoom node
  and Zoom contact level per exact address; a contact opens into a compact list
  of its conversations (reply-linked chains, plus single messages that still
  need something), with its other single messages folded under "N single
  messages (not linked to a conversation)", each still its own item; the
  selected conversation keeps its own composer target and draft; "New
  conversation with ..." starts a separate one. Search finds known agents by
  address and conversations or single messages by their first and latest
  lines, labelled by kind, and opens exactly that item; there are no people
  results. Review notices are reports: counted apart (unread, decisions here,
  reports), grouped per sender with the latest reported text and time,
  dismissed locally, never offered for approval. Not yet: named conversations
  (S-B), sorting old single messages into conversations, stable paging for very
  long conversations, and full-text search.
- **Acceptance:**
  - A1: 30 unlinked messages from one address are one contact entry in every
    lens; they are not merged into one conversation; each message is
    reachable once; counts match across lenses.
  - A2: two conversations with the same contact stay separate: sends, drafts,
    context and replies never cross.
  - A3: a normal send continues the selected conversation; "New conversation"
    starts a separate one; an explicit reply carries exactly the chosen id and
    combines no jobs; answering a question takes it over once.
  - A4: a held question or awaiting task counts 1 actionable, with the same
    dialogs and gates; unread counts separately.
  - A5: five notices from one sender make one line with the latest reported
    count and time, outside the actionable count, with no approve, accept or run
    control; dismissing clears all five locally, a newer one shows again, and
    Details lists each.
  - A6: a task and its result are each a targeted card with delivery state;
    done is not inferred; a running received task can be stopped; nothing runs
    by itself.
  - A7: drafts are per conversation (text, kind, target); a stale target is
    dropped; nothing sends while switching.
  - A8: admin/laptop and admin/zenbook stay two contacts.
  - A9: a push changes only the affected contact and conversation; no polling.
  - A10: loading older messages keeps the reading position; nothing repeats or
    is skipped while new ones arrive.
  - A11: restarting or branching a local harness session neither changes nor
    splits a conversation.
  - A12: All activity offers no composer and passes no context.

**S-W: relay serves the app; phone/browser device (1:1, v1).**
- **Build:** embedded assets, the browser engine, IndexedDB outbox, and a
  human-only capability record (the relay's capability endpoint arrives here).
- **No push in S-W.**
- **Journeys:**
  - §12.1 against a laptop daemon, including "no offline notifications" stated
    truthfully;
  - interop against Go vectors;
  - a real browser on a Railway deployment and on a VPS;
  - relay store inspected: no plaintext or private keys.

**S-B: envelope v2 and conversations.**
- **Build:** relay-first upgrade; the `env2` capability flag;
  `conv`/`lid`/`origin`/`emotion`/`attn`; tables; admission dedupe and attempt
  audit.
- **Journeys:**
  - two topics with one peer stay separate across days;
  - v1 peers still work;
  - a misdirected v2 envelope quarantines;
  - a duplicate logical request is admitted once;
  - an explicit accept re-runs a failed attempt, recorded, with no automatic
    retry.

**S-P: push for browser devices (after S-B).**
- **Build:** the §12.3 contract (attn + receiver allowlist + rate limit),
  subscriptions in `/data`, content-free visible notifications, and the selected
  Web Push implementation.
- **Journeys:**
  - an attention message wakes an installed iPhone app, with a visible
    notification;
  - agent background traffic and replicas stay quiet;
  - spam from a sender not on the allowlist does nothing.

**S-C: keeper epochs, groups, history grants, excerpts.**
- **Journeys:**
  - exact entitlement;
  - a missing predecessor holds;
  - a fork freezes;
  - a removed member's old-epoch message cannot create a job;
  - a replaced member key holds no rights until a new epoch names it;
  - a message for an unpinned conversation stays pending and never runs;
  - replicas never run;
  - §12.2 on the phone.

**S-D: agent participation.**
- **Journeys:** DECISIONS §3.2 examples 2–5, plus: a different member's follow-up
  question is answered under participation authority, and a follow-up task
  outside the allowance waits.

**S-E: linked devices** (DECISIONS §2 S1). After S-B.

**S-F (owner-gated):** history transfer, archive, recovery.

**S-R: "Remind me later" on a message (Required by the owner, 2026-09-28; not
part of S-A).** A question the person cannot answer now gets a reminder at a
time they choose. It is an independent follow-on to S-A: it needs only S-A
and a small appended reminder store, not S-B through S-F (listed here last
does not mean after them). Reminders on a phone come separately with S-W and
S-P.
- **Build:** a local, owner-only reminder record in the client store (a new
  appended schema step): message id (and so its derived thread), due time,
  state. The daemon arms one timer for the earliest due reminder and re-arms
  it after each change; no polling and no extra service.
- **Rules:**
  - A reminder only asks for attention. Snoozing never answers, accepts,
    declines or runs anything, and changes nothing the sender sees.
  - It is personal to this installation. It is separate from remote
    scheduling and from agent task state (`inbox.state`, jobs, grants).
  - No exact-time promise while the laptop is off or the daemon is stopped:
    a reminder that came due then is shown as overdue at the next start or
    wake. A phone alert needs S-W and S-P.
- **Journeys:**
  - set a time on a held question; restart the daemon; at the due time an
    attention-only notification appears and opens that conversation;
  - due while the daemon was stopped: shown as overdue at the next start;
  - reschedule, cancel, and mark done; answering the message by any path
    also ends its reminder;
  - the question's own state is unchanged throughout.

**Migration.** Schema changes are appended steps only. Existing rows are linked
to derived v1 threads. Nothing is merged across installations by label.

## 17. Source evidence

**Envelope:** `internal/envelope/envelope.go`:
- `:23` Version;
- `:120` `signed()`;
- `:127` `Seal` (valid kind, V);
- `:160` `VerifySig` (V, kind, called by the relay);
- `:194` `Open` (strict decode; inner and outer V/Kind/ID/From/To/TS
  cross-check).

**Protocol and client transport:**
- `internal/protocol/protocol.go:232,236,341`;
- `internal/client/transport.go:134` (lenient decode of relay responses).

**Store and worker:**
- `internal/client/store.go:370` (`seen`), `:376` (`INSERT OR IGNORE` inbox),
  `:755` (`claimJob`: approvals and task grants);
- `taskgrant.go`, `session.go`, `worker.go` (`threadText`), `kick.go`,
  `daemon.go:31`.

**Relay:**
- `internal/hub/api.go:18-40`;
- `stream.go:141`;
- `maint.go:147,155` (backup scope);
- `go.mod`, `Dockerfile`, `compose.yaml`, `docs/revival/INSTALL.md:134-160,260`.

**UI:** `internal/ui/ui.go:21`, `internal/ui/server.go`.

**Requirements:** `docs/DECISIONS.md` §2.2–2.3, §3.2, §4; `docs/HANDOFF.md:88`.

**Platform facts** (published documentation; not live-tested here):
- WebKit Web Push for Home Screen web apps (16.4+, user gesture, visible
  notification): https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/
- WebKit tracking prevention (7-day cap; Home Screen exemption):
  https://webkit.org/tracking-prevention/
- Ed25519/X25519 in WebCrypto:
  https://blogs.igalia.com/jfernandez/2025/08/25/ed25519-support-lands-in-chrome-what-it-means-for-developers-and-the-web/
- typage: https://github.com/FiloSottile/typage
- Railway limits:
  https://docs.railway.com/networking/public-networking/specs-and-limits ;
  volumes: https://docs.railway.com/volumes
- Code transparency efforts (not relied on): https://webcat.tech/ ,
  https://waict.dev/

**Uncertainties still open:**
- Web Locks and BroadcastChannel per browser;
- the Railway stream fit, which is documented but not tested;
- the Web Push implementation choice;
- browser storage durability, which is best-effort even when installed.

## 18. Disposition of review items 1–10

| # | Item | Disposition | Where |
|---|---|---|---|
| 1 | Membership authority/order | Keeper-signed epoch chain; request vs authorize; hold on missing predecessor, freeze on fork; keeper unavailable = frozen; successor later; epoch is a claim; authority checked at claim time against the current epoch; no instant-revocation claim | §8, §10 |
| 2 | Protocol compatibility | Full envelope v2 (outer and inner V=2, new signing domain, existing outer kinds + inner `sub`); relay `features` on `/v1/version`; separately signed capability record with 404 fallback; session ads unchanged; matrix | §9.1 |
| 3 | Participation authority | Bounded question authority for current-epoch members within scope; tasks need once / exact-key standing grant / explicit structured allowance; no inheritance or text inference | §9.3 |
| 4 | Idempotency/context | Admission dedupe on `(sender fp, lid)` separate from execution attempts on the existing job state; explicit re-run kept and audited; no automatic retry; non-executables never run; per-home only; bounded context with stated omissions; fresh session on scope/audience change | §9.4 |
| 5 | Real UI invalidation | Audited wake after commit plus a test per path; in-process signals for daemon writers; `data_version` removed; honest refocus/restart recovery | §11 |
| 6 | Browser code/key trust | Same-origin threat stated; pinning/SRI/CSP only defence-in-depth; non-extractable ≠ protection; storage loss possible when installed; four owner alternatives | §6, §15 |
| 7 | Mobile push | Attention-only via v2 `attn` hint + receiver allowlist + rate limit; quiet for background, replicas, events; visible content-free notifications; library by validation; its own slice S-P after S-B, so v1 phones have no offline notifications | §12, §16 |
| 8 | Emotion | Required on agent-origin v2 turns; truthful neutral fallback; independent of status | §13 |
| 9 | Rollout clarity | S-W has its own 1:1 v1 phone journey; group journey after S-C; S-A justified as the reused engine; devices are not humans until S-E | §12, §16, §4 |
| 10 | Status and single unit | Review before owner confirmation; TLS is engineering; push keys/subscriptions in `/data` and backed up; owner decisions split into before-slice vs later; no expiry is the default | header, §5, §14, §15 |

**Final review fixes** (request `messenger-final`):
- (a) Push moved to S-P after S-B; S-W states "no offline notifications" (§12.1,
  §12.3, §16).
- (b) Admission dedupe separated from auditable, explicitly re-runnable attempts
  on the existing job state; no claims table (§9.4, §10).
- (c) Members, keepers and authority bound to `(address, key fingerprint)`. Key
  replacement inherits nothing. Root `E_0` bootstrap, with conversation id =
  `hash(E_0)`, pinned via the keeper's pinned key. Unknown conversations and
  epochs stay pending and non-executable (§4, §8, §9.3).
- (d) The notification-content owner gate is removed: content-free and
  attention-only is existing direction (§12.3, §15).

## 19. Updates and versions

### 19.1 Three different versions, never conflated

| Version | What it is | How it is compared | Status |
|---|---|---|---|
| **Build / served frontend release** | the binary's `protocol.Version` string (e.g. `git describe`). Relay-served assets would be part of the relay binary; daemon-hosted assets are part of the daemon binary | equality only; no ordering (`internal/client/release.go`) | build stamp and recommendation existing; relay-served bundle **not shipped** (proposed) |
| **Protocol capability** | relay API generation `ProtocolVersion` (a mismatch refuses, naming the side to update; protocol generation check in `client.go`); envelope `V` (1 today, 2 proposed); relay `features` and per-session capability records (§9.1) | exact generation; negotiated capabilities | generation existing; the rest proposed |
| **Database schema** | append-only SQL steps per store (client, relay); browser IndexedDB version (proposed) | step count; `*.vN.bak` snapshot before upgrading (`internal/sqlitedb/sqlitedb.go:69-80`) | existing (SQLite); proposed (IndexedDB) |

A build change does not imply a protocol change. A protocol capability is never
inferred from a build string.

### 19.2 What exists today
- **Recommendation, not installation.** A relay admin can recommend a client
  build: `admin release set`, pushed on the stream, no polling.
  - Members whose build differs get one content-free desktop notice. Each hooked
    session gets one line saying to ask the person unless already authorized.
    `version` and `doctor` show it.
  - In the shipped build, AgentNet never downloads or installs anything
    (`release.go`, `INSTALL.md` "Updating and downgrading"). This release adds an
    explicit update command (§19.2a).
- **Laptop update.** Stop the daemon, rebuild or replace the binary, start it,
  run `doctor`. The schema migrates with a `*.vN.bak` snapshot.
- **Downgrade** is manual. It restores the previous binary plus `*.vN.bak` or a
  home backup, and loses what arrived after that backup (documented).
- **Busy jobs.** A job running when the daemon stops is marked `interrupted` at
  the next start (`daemon.go:319`, `store.go:823`). There is no automatic retry,
  and an explicit `accept` re-runs it.
- **Relay update.** Back up (stop, `hub backup`), rebuild or replace, start.

### 19.2a This release: explicit CLI self-update (reviewed source; not shipped)

- **Where it stands.** Owner direction: self-update is part of this release and
  stays small.
  - Implemented in source at `d99fdcc`, with a subsequent under-lock check of
    the installed binary's version. Claude implemented it; Agy reviewed the
    candidate; Codex reviewed the race correction.
  - The full Linux race suite passed before that correction; the affected CLI
    package and help integration test passed afterwards. The new race regression
    failed against the earlier code. An isolated Linux binary updated itself
    from the official release and matched its published checksum.
  - Native Windows/macOS execution is pending; cross-platform vet is not runtime
    proof. Nothing has been pushed or installed on the operator's machine.
- **Exact behaviour** is documented in `agentnet help update` and
  `docs/revival/INSTALL.md`, not repeated here. In summary:
  - it is explicit, with no periodic check, automatic install, service or new
    dependency;
  - it downloads the official release asset only from the project's fixed release
    address; the relay's recommendation is advice, never a download location;
  - it checks the release's `SHA256SUMS`: integrity, not host trust, and there is
    no separate signature;
  - it keeps the previous file as `<file>.old`;
  - older versions are refused, because databases only move forward.
- **It replaces the file it runs from.** That can be a laptop client, or a bare
  Hub executable run with `hub serve`.
  - A copy inside a container is refused. Container deployments stay
    operator-managed (§19.3).
- **Nothing running is stopped or restarted.** A running daemon or Hub keeps its
  old program until someone restarts it. The command reports that.
  - Only a stop or restart interrupts a running job, which then follows today's
    rule (§19.2): it becomes `interrupted`, with explicit accept to re-run.
- **Unchanged:** schema migration with `*.vN.bak` on the next start; identity
  keys, history, approvals and grants in the same home; manual downgrade.

### 19.2b Owner requirement: one-command update (2026-09-28; in source, not released)

- **Requirement.** One `agentnet update`, with no agent, script, curl or manual
  restart sequence, installs the latest *published stable* release from the
  project's GitHub releases for this platform, checked against its
  `SHA256SUMS`, and the person keeps their home: keys, history, approvals and
  grants.
- **Activation.** The binary carries the page, so the new release brings its UI
  with it. This home's running daemon switches to the new program by itself,
  and an open messenger page keeps its address and settings and reconnects.
  - It never interrupts active work: a running job finishes first.
  - It never restarts another home's daemon or a Hub.
  - No background service, periodic check or new dependency.
- **Safety kept.** No downgrade and no schema going backwards; a development
  build is not silently replaced by an older release.
- **Honest limits to state:** a binary with today's updater can only replace
  the file (its daemon still needs one restart), and a page loaded from an
  older build reloads before it uses the new UI, with drafts kept or clearly
  lost.
- **Stamps.** Development builds used to be stamped with
  `git describe --always --dirty`, which ignores the lightweight release tags
  (v0.2.x): a build after v0.2.1 reports `v0.1.50-N-g…`. The build line and
  `scripts/build.sh` now use `--tags`. Because old stamps remain installed, a
  stamp is only a lower bound, and the schema check below is what keeps older
  code off a newer database.
- **In source (not released, not installed):**
  - resolution: a release takes only a newer release; a development build
    stamped from a release (`vX.Y.Z-N-gHASH`, `vX.Y.Z+…`) only one newer than
    `vX.Y.Z` (an equal release is refused, not called an update); other dev
    builds name the release;
  - schema check: the target must report (`version --schema`) that it opens
    the home database's schema, read read-only; a target too old to say is
    accepted only as a newer release over a release;
  - switch (Linux, macOS): an owner-only request in the home plus the local
    wake-up. The daemon acts only for the program file it started from,
    starts no new job, waits until the running job has stored its result,
    checks the file reports the version, stops cleanly and re-executes the
    file in place (same process). The program that starts records the
    outcome once and clears the request (no loops, nothing left pending); a
    daemon that cannot switch records "not applied" with the reason and keeps
    serving;
  - page: a content-free `restart` event, the same address and token for the
    program started for that update only, bounded reconnection (also after a
    missed event), and a reload on a new version that keeps every unsent text
    (conversation drafts, composer, open dialogs) or, without storage, asks;
  - `agentnet update` reports only what it observed (switched, pending, not
    applied, failed); `--status` later.
  - Windows: a daemon that is exactly the running instance of the scheduled
    task `\agentnet` (same file, same arguments including `--home` and
    `--ui`, same user, not StopExisting, and not in a kill-on-close job)
    starts a helper (the updated program) before it stops and waits until
    the helper holds it; the helper waits for it to be gone and its lock
    free, checks the task again, runs `schtasks /run` a bounded few times,
    and records a failure if no start completes the request. A daemon
    started any other way, such as a standalone daemon in a console, is not
    stopped (explicit limit).
  - Evidence: `itest/update_switch_test.go` (Linux, real binaries, local
    release server), client switch tests, updater tests, page checks. The
    native Windows product test (`itest/update_switch_windows_test.go`,
    opt-in `AGENTNET_WINTASK=1`, the real program and a real scheduled task)
    passed on `windows-latest` in GitHub Actions run 36458378377 (job
    `windows-task-probe`, `TestWindowsUpdateSwitchThroughTask` 42.71s) at
    `77cc1f1`, whose update-switch code is identical to `af144f0`: switch
    through the task, lost helper, task that cannot start, console daemon
    not stopped. Not covered on Windows: a switch that waits for a busy job
    (Linux and shared-code evidence only) and a real Windows browser.

### 19.3 Proposed lifecycle for later work (all unimplemented)

**Relay: one unit.**
- **Procedure:** stop, `hub backup`, replace the binary or container (assets
  inside), start (schema steps plus `.bak`), check a real browser and a daemon.
- **Keep old clients working.** API changes are additive only: new endpoints, and
  fields that clients decode leniently. v1 envelopes stay accepted.
  `ProtocolVersion` is bumped only for a deliberate breaking change, which older
  daemons and cached browser apps then report as "update required", never as a
  silent failure.
- **Rollback.** Restore the backup into a new volume.
  - **Limit:** the relay forgets messages and receipts recorded after the backup.
  - **Unknown is not lost.** A message the restored relay does not know may already
    have been delivered, by relay or by the direct path, or may not have been.
    Proposed: clients reconcile by asking for each message's state. An
    unknown answer becomes **"delivery unknown — reconciliation needed"**, not
    "lost".
  - **Transport retransmission** of the **same stored envelope** (same id, same
    signature) stays allowed, using today's idempotent path. The relay accepts an
    identical re-post and rejects a different one (`internal/hub/store.go:293-296`).
    The recipient stores an id once (`INSERT OR IGNORE`).
  - Retransmission is never permission to re-run effects. No new envelope or new
    id is created automatically.
  - If the recipient's own store, and so its dedupe and job state, was also
    restored, the outcome is uncertain. That recipient's automation is paused
    until a person reconciles (see restore procedure below).

**Browser device: served frontend release.**
- **How it arrives.** The relay serves the new assets. The installed app's service
  worker fetches them, but **activates only at a safe point**:
  - no send in flight (the outbox is durably queued in IndexedDB);
  - drafts already saved (drafts are written to IndexedDB continuously);
  - one coordinating tab confirms all tabs can switch (Web Locks +
    BroadcastChannel).
  The person sees "Update ready (build X) — restart app". There is never a blind
  reload in the middle of a send.
- **Kept across versions:** keys (same origin, non-extractable), IndexedDB data,
  outbox and drafts.
  - IndexedDB schema steps are append-only.
  - An app older than its data, after a relay rollback, refuses to write and says
    so rather than risk corruption.
- **Trust.** Every update is new code from the relay, the same trust as §6. No
  signing or updater is assumed.

**Laptop daemon.**
- **Update:** the explicit `agentnet update` of this release (§19.2a), or the
  manual build as today. Restarting the daemon afterwards is a separate step; see
  the stop/restart rule for running jobs.
  - Proposed small improvement: `doctor` and the UI show running jobs before the
    person stops the daemon. Interrupted jobs follow today's rule: no automatic
    retry, explicit accept.
- **Kept:** identity keys, history, approvals and task grants, because the same
  home migrates in place.
  - The key fingerprint must be unchanged after the update; a test checks it.
- **Downgrade** as today (`.vN.bak` or a home backup), with its data-loss limit.
  - A downgraded v1 session publishes no capability record. Peers see
    "changed" and hold new conversation-scoped v2 work visibly (§9.1).
  - v2 already queued or in custody may be quarantined, visibly.

**Restoring any store rewinds authority, not only messages.**
- A restored daemon database rewinds several things at once:
  - approvals and task grants;
  - epochs and participations;
  - job states and the dedupe ledger.
- If no newer event arrives, nothing fails closed on its own. An older epoch could
  still list a removed member, and an older grant could still hold.
- **Proposed future restore procedure** (not part of the core updater, and no new
  command is imposed on it now): restoring writes a local "restored, automation
  paused" mark into the restored store. While it is set:
  - approved-sender auto-answers stop;
  - standing and assignment task grants stop;
  - participation jobs stop.
- The person clears the mark after reviewing two things:
  - authority (grants, approvals, current epochs and participations), reconfirmed;
  - post-backup work (jobs that may have run after the backup), reconciled.
- **Manual restores** (copying `*.vN.bak` or a home directory) and existing old
  binaries do **not** set this mark. The procedure documents setting it, or the
  person must pause automation themselves. Nothing enforces it magically.
- No new service or counter infrastructure is implied.

**Authority.**
- Once/always task grants authorize **tasks from a specific peer key**. They do
  **not** make that peer, the relay or its release recommendation a trusted
  software channel.
- An "update AgentNet" task is an ordinary task, accepted by the person (or under
  a grant the person deliberately gave for tasks from that key).
- `agentnet update` itself (§19.2a) runs only when invoked: by a person, or inside
  a task accepted once or covered by an existing standing task grant for that key.
  There is no extra reapproval.
- A relay recommendation, a peer message or a notification never triggers it.
- There is no new permission system and no background updater.

### 19.4 Acceptance tests (real upgrades, not compilation)
- **Relay:**
  - back up; upgrade to a build with a new schema step;
  - a previous-build daemon and a new daemon keep exchanging messages;
  - a cached older browser app keeps working or truthfully says "update required";
  - restore the backup: post-backup messages show "delivery unknown";
  - retransmitting the identical envelope does not duplicate at a recipient that
    already stored it;
  - a recipient whose store was also restored keeps the re-arrived task waiting
    for the person (automation paused).
- **Daemon:**
  - `agentnet update` while a job runs: nothing stops, and the job finishes on the
    old program;
  - **stopping or restarting** the daemon while a job runs: the job becomes
    `interrupted`, and an explicit accept re-runs it once;
  - key fingerprint, history, approvals and grants are unchanged;
  - restore via the documented procedure: an approved sender's question is not
    auto-answered and a granted task does not auto-run until the person resumes
    automation;
  - downgrade: peers mark the device "changed", and new conversation-scoped v2
    work is held visibly (not sent as v1). Earlier queued v2 may quarantine
    visibly.
  - an offline v2 peer still receives v2 into custody;
  - two conflicting concurrent sessions: the least capable wins.
- **Browser:**
  - deploy new assets during a send: no reload until the send is queued or done;
  - two open tabs: one activation;
  - keys, drafts and outbox survive;
  - an older app with newer data refuses writes.
- **Mixed versions:** the §9.1 matrix run with real binaries (itest style), across
  restarts.

## 20. Owner requests for later (backlog, not this release)

Recorded 2026-09-29. Neither is designed or scheduled; nothing here widens the
current release.

**Custom visual plugins** ([MEL-475](https://linear.app/mellanni/issue/MEL-475/secure-custom-messenger-ui-plugins)). Every user can bundle their own presentation of the
messenger, as Classic, Comic and Zoom are, without changing how messages flow.
It must be secure:
- *Isolation:* a plugin runs apart from the page's own code and from other
  plugins, and sees only the view data it is given. It cannot reach keys,
  tokens, cookies, the page's API, the network or local files.
- *Least permission, protected core authority:* a plugin only draws and asks
  for core actions. Sending, accepting, trusting, granting and approving stay
  with the core, which shows its own confirmation and applies its own rules; a
  plugin cannot perform them, decide them, or imitate the core's dialogs.
- *No silent export:* nothing a plugin sees leaves the page or the computer
  except by the person's explicit action in the core's own screens.
- *No change to the core flow:* sending, storing, receipts and execution are
  the same with or without plugins.

Undecided: the architecture, the API, the package format, and how plugins are
installed, reviewed and updated. Signing a plugin, or running it in a sandbox,
is not by itself shown to meet these requirements.

**Editing sent messages** ([MEL-476](https://linear.app/mellanni/issue/MEL-476/edit-sent-messages)). As in Slack or Telegram, a sender can edit a message
they sent:
- the message visibly shows that it was edited;
- an edit is an authenticated revision by the same sender, delivered as
  messages are, reaching devices that were offline and applied in order;
- messages stay immutable underneath (§8): a revision is a new signed record
  linked to the original, never a silent rewrite;
- an edit never reruns or re-decides anything: a question or task already
  accepted, answered or run keeps its record.

Open design questions: time limits on editing, who can see earlier versions,
attachments, conflicting or concurrent edits, what an agent that already read
the message does with a revision, and how older clients show edits.
