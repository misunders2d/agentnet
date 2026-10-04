# AgentNet Revival — Architectural Decision Records & Linear Roadmap

> **Current contract:** [NEXT_RELEASE.md](NEXT_RELEASE.md) records the Sep 30
> owner scope and reconciled group, agent, workspace, storage and interface
> decisions. It supersedes conflicting older deferrals/proposals below.
> Implementation and acceptance remain incomplete; historical issue statuses
> in this document are not a current Linear export.

> **Target Audience:** Any incoming coding agent or human contributor evaluating architecture choices, historical debates, or future roadmap items.
>
> **Companion Document:** [`docs/HANDOFF.md`](HANDOFF.md) (Operational state and verification matrix).
>
> **Linear Project:** [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580) (This document ensures the repository stands completely alone even without Linear access).

---

## 1. Linear Ticket & Architecture Mapping

| Ticket | Linear Title | Linear Status | Shipped State & Technical Contract in v0.2.1 |
|---|---|---|---|
| **MEL-409** | M1: Deliver identity enrollment and encrypted offline messaging | **Done** | Local Ed25519 signing + age X25519 encryption (`identity.json`). Blind Hub relay, signed HTTP requests, push stream, TOFU peer key pinning. |
| **MEL-410** | M2: Deliver resumable encrypted file transfer | **Done** | Resumable chunked file transfers (age streaming, 64 KiB authenticated chunks, 512 KiB upload blocks). Verified atomic downloads, sender spool cleanup, quota accounting. |
| **MEL-411** | M3: Deliver presence, direct peer transfer and Hub fallback | **Done** | Ephemeral session presence (`person/agent#SESSION`). Opt-in direct HTTPS transfers with pinned certificates; automatic Hub fallback. |
| **MEL-412** | M4a: Shared inbox, default responder and human-confirmed tasks | **Done** | Headless responder execution for Claude Code, Codex CLI, and Pi. Skills-on question fencing with native settings preserved. Mandatory operator task acceptance (`awaiting` -> `agentnet accept`). |
| **MEL-413** | M4b: Standard A2A interoperability through the CLI | **Done** | Loopback adapter implementing the official `a2aproject/a2a-go/v2` SDK. Authenticated via local bearer token (`<home>/a2a-token`). |
| **MEL-414** | M5: Native installation and Linux/macOS/Windows qualification | **Done** | Windows owner-only protected ACLs (`internal/secfile`). Linux race testing. Shipped with known platform limits (Windows hooks refused; Windows worker process kill parent-only). |
| **MEL-415** | M5: Containerize Hub and verify isolated Contabo deployment | **Done** | Multi-stage Dockerfile, `compose.yaml`, non-root user (UID 65532), named volume support. Verified via `scripts/hub-container-test.sh`. |
| **MEL-416** | M5: Backup, restore, storage controls and final MVP journey | **Done** | `agentnet hub backup` and `agentnet hub restore` with checksum verification. Automated client pre-migration backup (`agent.db.vN.bak`). Client and Hub cleanup commands (`agentnet cleanup [--saved]`, `agentnet hub cleanup`). |
| **MEL-417** | Final CLI help: agent can install, configure and operate for its human | **Done** | `agentnet help <topic>` providing offline documentation for install, startup, update, uninstall, security, and commands. |
| **MEL-418** | GitHub README: plain-English overview, visuals and collapsed technical guides | **Done** | Comprehensive user-facing overview, quickstart, multi-harness tables, collapsible deep dives, and visual architecture diagram. |
| **MEL-419** | Deploy tested MVP relay and operator laptop | **Done** | Verified self-signed built-in TLS with invite pin vs Platform TLS with system CAs. Deployed live on operator laptop and headless nodes. |
| **MEL-420** | Make fresh-laptop invites self-contained and publish revived project on main | **Done** | Single-use bootstrap admin invite (`<data>/bootstrap-invite.txt`) and self-contained member invites with embedded TLS pins, owner/instance metadata, and .NET/PowerShell compatibility. |
| **MEL-421** | Require explicit owner and instance naming during agent-led onboarding | **Done** | `person/agent` address format with explicit name validation; separate `--admin` role flag during invitation creation. |
| **MEL-422** | Separate durable verified identity from human-readable agent addresses | **Backlog** | Current v0.2.1 binds identity to device keypair at `person/agent`. Decoupling cryptographic identity from mutable human addresses is planned for future identity rollout. |
| **MEL-423** | Show delivery receipts to sending agents without status polling | **Done** | `custody`, `delivered`, `quarantined`, `expired` receipt tracking. Recipient posts signed HTTP receipt; sender queries on-demand or bounded wait (`agentnet status [--wait 30s] <id>`). |
| **MEL-424** | Consolidate local and remote branches to main plus preserved legacy | **Done** | Unified revived Go codebase on `main` branch; archived legacy Python codebase preserved under `legacy/agentnet` branch. |
| **MEL-425** | Handle agent questions and correlated replies without human inbox nudges | **In Progress** | Shipped subset: `agentnet hooks install claude|codex`, `inbox.arrival` triggers, per-session cursors, Stop hook, and background session resumption. Residual gates remain open in Linear. |
| **MEL-426** | Notify the human only when AgentNet needs approval or a decision | **In Progress** | Shipped subset: `awaiting`, `held`, and `needs_human` review states; local follow-up summaries; headless review notice routing to operator laptop with desktop notifications. Residual gates remain open in Linear. |
| **MEL-427** | Clarify peer diagnostics: build revision, admin role and non-mutating inbox inspection | **Backlog** | Diagnostic commands and read-only inbox inspection extensions remain in Backlog. Current inspection via `agentnet doctor`, `agentnet whoami`, `agentnet inbox [--review]`. |
| **MEL-428** | Make default background responder selection explicit during onboarding | **Done** | `agentnet join` prints responder configuration hint; member invitation instructs coding agent to inspect PATH and configure default responder via `agentnet responder list/set`; CLI itself does not auto-select. |
| **MEL-429** | Let humans join AgentNet conversations through a lightweight interface | **In Progress** | Shipped subset: `agentnet ui --demo` interactive fixture-backed web interface with token-to-cookie loopback auth. Live daemon connection is deferred (issue umbrella remains In Progress in Linear). |
| **MEL-430** | Clear the SSE write deadline after each flush to stop idle reconnect churn | **Done** | Hub push stream SSE writer clears 15s write deadline after successful flush (heartbeat is 90s), preventing stale deadline disconnect churn on idle connections. |
| **MEL-431** | Announce AgentNet updates without polling and support agent-operated upgrades | **Done** | `agentnet admin release set` publishes version recommendations over push stream; string equality comparison; content-free desktop banner and hook notice; agent upgrade playbook. |
| **MEL-432** | Synchronize interrupted-worker test with actual subprocess startup | **Done** | Test synchronization in `internal/client/background_test.go` and `worker_test.go` (`5aae937`) waiting for actual subprocess startup before simulating worker interruption. |
| **MEL-433** | Decide human identity, linked devices and shared conversations for messenger rollout | **In Progress** | Next release in progress: one person across devices, own-device admission confirmed on an existing device, shared chats/history, and visible person-to-device linkage. See §2. |
| **MEL-434** | Explore comic-book conversations, expressive avatars and message expressions | **Backlog** | Specification synthesized: comic speech balloons; required avatar facial emotion attribute (`happy`, `sad`, `curious`, `concerned`, `excited`, `focused`, `confused`) emitted with turn and distinct from operational status indicators (`searching`, `working`, `blocked`, `done`); cached predefined emotional reaction set at enrollment/avatar change (no extra per-message model call). |
| **MEL-435** | Open the exact conversation from a review notification | **Backlog** | Specification synthesized: notification click opens/focuses exact conversation without auto-accept side effects. Native OS action handlers unimplemented in v0.2.1. |

---

## 2. Human Identity & Multi-Device Linking (MEL-433 Synthesis)

### 2.1 Context & Problem Statement
In AgentNet v0.2.x, identity is bound to a single device keypair (`person/agent`). If a person works across multiple laptops or workstations:
- Each device has a distinct cryptographic key and address (`alice/laptop` vs `alice/desk`).
- Outgoing messages from one device are not automatically visible on another.
- Display names cannot serve as identity authority without enabling forgery.

Linear ticket **MEL-433** convened an architecture debate between UI Claude (`p7`), Critic Agy (`p4`), Core Claude (`p3`), and Supervisor Codex (`p1`).

### 2.2 Owner Decisions — 2026-09-29

These decisions replace the earlier forward-only-history and admin-invite-per-device
proposal. Implementation is in progress; they are not claims about shipped v0.3.0.

- **One person, several devices.** A stable person ID has a signed roster of up to
  eight devices. Each device generates and keeps its own keys. Names never merge
  identities, and the address prefix is not proof of a human identity.
- **Admission and linking.** The first installation needs an admin invitation.
  An admitted person's existing device may create a short-lived, one-use invite
  restricted to adding a device to that same person. One QR/link covers joining
  and requesting linkage. Explicit confirmation on the existing device activates
  the new device; neither an admin nor possession of the link alone can add a key
  to an already-pinned person. No admin privilege is inherited.
- **Independent services.** Servers and bots can enroll through admin invitations
  without a human person. Choosing this role is stored. A responsible human is
  not implicitly their author, conversation member or execution authority.
- **Same chats on all personal devices.** New messages fan out to the peer's
  devices and the sender's other devices. Linking also synchronizes existing
  conversations. Each encrypted delivery keeps its own receipts and retry state.
  History supplied by one's own device carries its provenance; it does not become
  independently verified authorship by the original sender.
- **One execution host.** Questions/tasks for an agent retain an exact host
  device. History and replicas confer no execution authority and never trigger
  jobs, notifications or reminders. A separately verified direct request still
  receives normal admission checks if its history copy arrived first.
- **Visible belonging.** Show one person, including one Me, with devices visibly
  beneath that person. In Zoom, expand the person node to show linked devices.
  In Classic/mobile, keep chats directly accessible and show a device-count
  disclosure beside the person's name. Mark the current device. Standalone
  services stay separate; do not repeat personal devices as unrelated contacts.
- **Uncluttered conversations.** The sidebar lists people/services once. Their
  separate conversations open inside that person's view, with clear unread,
  search and back navigation. Do not silently merge discussions.
- **Clean preview replacement.** Re-enrollment is acceptable. Old preview persons
  and DMs need not migrate; existing rows stay unused on disk. This does not waive
  synchronization of chats created under the new model. No live deletion is
  authorized or scheduled by this design record.
- **Removal and loss.** A current roster device may sign removal of another
  device or itself; the last device cannot be removed. A device admitted only
  through linking loses that admission when removed. Losing all devices means a
  visible identity reset, not silent admin recovery.

### 2.3 Implementation Boundaries and Acceptance

Use the existing signed requests, per-device envelopes, push stream and durable
outbox. The replacement uses the `agentnet-person-v2` roster domain, version-2
conversation roots and `person2` capability; unsupported peers get an explicit
update requirement, not a partial single-device fallback. Standalone device
messaging remains separate.

Pairing uses a random secret carried in the QR/link fragment, a bound HMAC
transcript, the new device's proof of possession, and the existing device's signed
roster approval. Offers expire and are single-use. Pending devices cannot exchange
messages before activation. Public keys remain in the signed roster chain for
verification after removal. No private keys move between devices.

Initial history transfer is resumable. Subsequent messages addressed to an older
roster are forwarded to missing current devices without a fixed catch-up cutoff.
History alone never grants authority. Historical files may need an online device
holding the encrypted copy; show that limitation plainly. Browser work pauses
while its page is closed and resumes when reopened.

Acceptance is the full laptop/phone journey: one person on both devices; existing
chats and new replies visible on both; files survive source deletion and restart;
offline recovery without duplicate messages or execution; explicit removal;
forged, expired and replayed linking refused; independent service enrollment;
and readable desktop/mobile navigation with visible device relationships.

Reuse established libraries and the current transport. No generic synchronization
framework, name-based merging, legacy-history salvage or hidden compatibility
path is required. TOFU, withheld updates and compromised authorized devices remain
trust limits; signatures prove control of keys, not human authorship.

---

## 3. Agentic-First Messenger UI Skeleton (MEL-429 Synthesis)

### 3.1 Original Demo Architecture & Security Seams

This subsection records the v0.2.1 demo baseline, not the current source.
The source now has a live daemon provider and a relay-served browser device.
The browser's encryption bundle is vendored with pinned dependencies; its
rebuild uses Node, while the Go build embeds the resulting assets. Current
implementation evidence and limits are in [HANDOFF.md](HANDOFF.md) and
[MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md).
1. **Zero External Dependencies**:
   - Built with standard library Go using embedded static assets (`embed.FS`).
   - Vanilla HTML, CSS, and modern JavaScript. Zero npm, zero Node.js build pipeline, zero Electron.

2. **Strict Loopback Authentication**:
   - Binds strictly to `127.0.0.1:0`.
   - Security handshake: A one-time random token is passed via query parameter on launch (`/?t=...`), which the server immediately exchanges for an `HttpOnly` session cookie (`cookieName = "agentnet_ui"`). All subsequent API and SSE requests are authenticated by that cookie.
   - Strict `Host` header matching and origin checks prevent DNS rebinding and cross-site requests.

3. **Demo Isolation Guardrail**:
   - Shipped in v0.2.x strictly as a fixture-backed demo: `agentnet ui --demo`.
   - Running without `--demo` immediately halts with the verified error message:
     ```
     the messenger page runs only with --demo for now: it is not connected to your inbox yet (see agentnet help ui)
     ```
   - Uses synthetic in-memory mock data (`internal/ui/fixture.go`) and does not touch the real `agent.db`.

4. **Live Daemon Integration**:
   - Live daemon integration is not blocked on a full multi-device identity implementation; next slice design decides UI integration scope.
   - The current preview uses simulated actions only. Before a real provider is added, account for writes from other CLI processes waking the UI, stale receipt values, worker ownership of running jobs, and honest unknown presence/authorship. Do not invent a persisted thread root that breaks when a parent arrives late.
   - Design motivation: reduce notification noise, context recovery and fragmented threads; preserve provenance and distinguish delivery from execution. Incumbent-complaint research supplied hypotheses, not validated market statistics or resource guarantees. Keep plain text, code and files usable alongside the comic presentation.

---

### 3.2 Persistent Conversations & Temporary Participation (2026-09-28)

**Status, 2026-09-29: partially implemented in the unreleased source.** Explicit
single-device persons, separate two-person DMs, and invited agents hosted on a
member's computer are implemented. Both people see the chosen earlier context;
the host accepts explicitly, and either person may dismiss the agent. The agent
remains available for addressed follow-ups until dismissed. Classic, Comic and
Zoom, the relay-served browser device, optional notifications and daemon reminders
have implementation evidence in [HANDOFF.md](HANDOFF.md). Files on messenger
pages and platform qualification are still being completed.

Groups, agents hosted outside the DM's member devices, device linking, shared
history and recovery remain future slices. The broader requirements below are
not claims that those slices shipped. This section preserves the owner's
conversation model; implementation checkpoints do not authorize deployment.

**Owner requirements and examples**

- A conversation persists independently of a laptop connection or model session.
  People must be able to return days later and continue it.
- The same people/laptops can have separate discussions about different topics.
  Support human DMs, multi-user discussions and invited agents through a coherent
  conversation model; do not bind a chat's identity to one harness session.
- Bringing someone in, sharing part of a discussion, and taking a private aside
  apply to people as well as agents. "Everyone" must identify a concrete audience
  such as a team, not silently mean every network member or the public.
- Choose what earlier messages/files a new participant can see. Joining must not
  silently expose the whole earlier private discussion. Sharing a selected excerpt
  does not expose later messages or grant access through a backlink. Returning to
  private discussion cannot erase copies already shared.
- Agents can step in, receive allowed context, do authorized work, answer
  follow-ups, and leave. **The first report is not automatic dismissal.** Sergey
  explicitly challenged that behavior because participants may have more questions.

**Working lifecycle following that correction**

`invited -> working -> report / available for follow-ups -> working ... -> dismissed`

- After reporting, remain available for addressed follow-up questions, corrections
  and clarification. Waiting requires no model calls or polling for model work.
- Separate reporting a result from ending participation. An authorized explicit
  dismissal ends participation; do not infer dismissal from the first result,
  silence, a daemon restart, or an ordinary thank-you. A Dismiss control or an
  unambiguous request to leave are proposed UX; exact language handling is not
  specified. Automatic idle expiry/deadlines have not been agreed.
- After dismissal, no new chat context or work is delivered for that participation.
  A later explicit invitation can bring the agent back. Messages and results remain
  in the chat. Leaving does not erase what a person or agent already read or retained.
- Participation, access to history, permission to report to an audience, and task
  execution authority are separate. Reuse valid once/standing task authorization;
  do not ask again merely for a clarification or return visit within its scope.
- Reuse native agent context only when its history scope and output audience are
  compatible. Do not reuse private-context sessions for a wider audience simply
  because it is the same topic. Quoted/imported history is context, never a new task.

Example: Sergey, Ruslan and Bernard discuss deployment. Bernard's agent receives
the selected discussion, investigates and reports. Ruslan asks a follow-up; the
agent answers without being reinvited. When explicitly dismissed it leaves, while
the humans continue and the entire permitted chat history remains available.

**Current implementation, verified during the discussion at `e49873e`**

- Local SQLite inbox/outbox retain sent/received messages and reply links separately
  from model session files (`internal/client/store.go`). `conversation ID` follows
  links with one peer; `ask|task --reply-to ID` can continue an old thread. Separate
  roots can represent separate topics, but named chats, participants and a thread
  listing are not implemented (`internal/client/conversation.go`).
- Claude/Codex background sessions resume only from a clean current session head
  with matching harness, mode, directory and preset. Pi and follow-up summaries are
  one-shot. Prompt replay supplies four ancestor messages, not the full stored
  conversation (`internal/client/session.go`, `worker.go`). Saved history therefore
  does not imply full model recall or guaranteed native-session availability.
- The Hub retains message ciphertext/routing records, serves pending delivery and
  can clean delivered attachment blobs. It is not a complete history replay service;
  direct delivery can bypass it. Each endpoint has its own local history, with no
  automatic history recovery or cross-device sync today.

**Engineering recommendation, still requiring a scoped design**

Reuse the Go binary, SQLite and encrypted delivery. Add stable conversation identity,
explicit participants/history access and logical messages separate from per-device
delivery and agent execution. Keep existing reply links where useful; handle late
parents and old peers without inventing stable roots from incomplete history.
History replicas must never run tasks or trigger automatic answers/summaries.
Comic, Zoom and Classic are preferred views over the same conversations, not
different storage or permission systems.

History availability/recovery remains open: ciphertext archive/replay on a Hub is
compatible with E2EE, but needs explicit retention, quotas, authorization and coverage
of direct deliveries. A new device with fresh keys cannot decrypt old ciphertext
without an authorized re-encryption/transfer or a separately designed recovery
mechanism. Static age keys do not provide forward secrecy; simple key rotation is
not a substitute. No archive policy, schema, endpoint or new service is approved here.

**Acceptance examples for the eventual implementation**

1. Return to the same topic after days and restarts, independently of native model
   session availability; other topics with the same people stay separate.
2. Invite a person/agent with selected history; excluded messages remain inaccessible.
3. Receive an agent's first report, ask a follow-up, and get an answer without a
   new invitation or repeated approval for already-authorized work.
4. Waiting makes no model calls. Dismissal stops further participation; reinviting
   uses only the then-authorized history and reporting audience.
5. Joining, replaying or syncing old messages never re-executes an old task.

---

## 4. Comic Avatars & Visual Expressions (MEL-434 Specification)

### 4.1 Requirements & Interaction Contract
1. **Speech Balloons**:
   - Message turns attach speech balloons to the sending person's or agent's avatar, creating a readable comic-strip style thread.

2. **Avatar Facial Emotions & Expressions**:
   - Each agent turn requires an agent emotion/expression attribute shown by the avatar's face (e.g. `happy`, `sad`, `curious`, `concerned`, `excited`, `focused`, `confused` as vocabulary proposal).
   - The sending agent emits its chosen emotion attribute directly with the message turn (no extra per-message model call).
   - **Distinct from Operational Activity**: Facial emotion is strictly distinct from operational workflow states (`searching`, `working`, `blocked`, `done`), which belong to a separate status indicator. Workflow states must not be substituted for emotions.

3. **Cost, Latency & Privacy Boundaries**:
   - **Zero Extra Per-Message Model Inference**: Emotional expressions are rendered from a **cached predefined emotional reaction set** generated once at agent enrollment or avatar customization. The existing sender emits its emotion with the message; no extra per-message model call, live image generation, dynamic sprite calls, or LLM sentiment extraction occurs during messaging turns.

4. **Accessibility & Density**:
   - Full support for `prefers-reduced-motion` and a static fallback view.
   - Compact view available for dense code diffs and attachment inspection.
   - These are design requirements, not completed UI features. Preserve selectable/searchable text, keyboard access, long replies, attachments and mobile layouts. Old peers or missing assets use a neutral fallback without blocking delivery.
   - Explore brief balloon entrances, expression transitions and speaker handoffs. Avoid endless animation, focus stealing, polling, extra services or idle GPU/model work; pause motion offscreen. Measure before claiming a resource budget.

5. **Current Status**:
   - Emotional reaction set requirement accepted by owner; exact emotion vocabulary, art direction, image generation providers, cost, privacy, and licensing remain open design questions.
   - **Strict Constraint:** No visual assets or generative pipelines are to be created or checked in at this stage.

---

## 5. Notification Click Focus (MEL-435 Specification)

### 5.1 Focus & Navigation Contract
1. **Direct Conversation Opening**:
   - Clicking an AgentNet desktop notification must open or focus the exact persisted conversation (`agentnet conversation <id>`).
2. **Review Notice List**:
   - Clicking a batched or grouped review notice opens the review list (`agentnet inbox --review`).
3. **Strict Safety Invariants**:
   - Clicking a notification must **never** auto-accept a task, trigger any side effect, or hijack an unrelated active terminal or interactive session.
   - Open the selected local agent or future UI with the persisted conversation/review context. Resume an actual recorded model session only where supported; never fabricate continuity. A headless review notice does not grant access or acceptance authority over the remote item.
4. **Current Status**:
   - Linux: implemented. The click command (`xdg-terminal-exec … agentnet open ID|--review`) is sent as Omarchy's persistent `omarchy-exec-argv` hint (popup and history clicks, run by the desktop session) and as the standard `default` action (other servers, live notification only). `agentnet open` starts the chosen coding agent interactively in a new session with a review prompt. A physical click was verified on Omarchy (see docs/revival/M4.md).
   - macOS notification center callbacks and Windows balloon click messages remain **unimplemented**; those notifications have no click action.

---

## 6. Non-Negotiable Operational Guardrails for Future Work

1. **Human Authorization Gate on Tasks**:
   - Tasks arrive as `awaiting` unless the local operator granted that sender's exact verified key (`approve --tasks ADDRESS`, `accept --always ID`); then they arrive `pending` and the worker rechecks the grant, the verifying key and the current pin when it claims them.
   - Otherwise execution occurs only after explicit local operator transition (`agentnet accept <id>`). Grants are local, per key, revocable, never transferred to a new key, and never rerun failed or interrupted work (decided 2026-09-28, owner-authorized).
   - Workers never touch open user sessions.

2. **Skills-On Question Guardrails**:
   - Questions retain the recipient's skills, plugins, and MCP servers.
   - Editing tools are removed (`Edit,Write,NotebookEdit` for Claude) and interactive approvals denied (`dontAsk` for Claude, read-only sandbox and `approval_policy="never"` for Codex, read-only tool subset for Pi).
   - Tools and Bash commands already permitted by user settings keep their native effects.

3. **Push-Only Architecture (No Polling)**:
   - All client communication remains event-driven over the push stream, local sockets, or direct transfers.

4. **Append-Only Schema Migrations**:
   - Never edit or reorder existing SQL steps in `internal/client/store.go` or `internal/hub/hub.go`.
   - Append new migration steps at the end of the array. Pre-migration backups (`*.vN.bak`) must be preserved.

5. **Group Messaging Scope**:
   - Groups were deferred from the initial slice. They are required by the
     Sep 30 next-release contract; see [NEXT_RELEASE.md](NEXT_RELEASE.md).

## 7. Workspaces and cooperating relays (MEL-436, Sep 30)

The existing Claude/AGY discussion and the Sep 30 seven-question review inform
these design choices. This completes a design exploration, not federation
implementation. MEL-495 separately requires a working workspace switcher and
isolation journeys. No mesh availability, throughput or deployment is claimed.

1. **Identity and authority.** A workspace has a persistent opaque ID that
   survives endpoint and TLS-certificate changes. The ID names the workspace;
   it does not authenticate an endpoint or confer trust. Current enrollments
   keep authenticated TLS endpoints and their existing pins. Future multiple
   relay endpoints require a separately pinned workspace authority statement
   and authenticated key/endpoint continuity. A `/version` ID alone is not
   that future authority protocol.
2. **Credentials and personas.** New independent workspace enrollments use
   separate device credentials and workspace-scoped person records. Existing
   identities remain intact. A local account association stays local unless
   its owner publishes a cross-workspace claim; even then it grants no history
   or permissions. Separate stores limit application routing errors and the
   compromise of one workspace key, not compromise of the whole OS account
   or a trusted client package.
3. **Guests and execution context.** Workspace admission, conversation
   membership, history sharing, agent participation and execution grants are
   separate. Discovery/transit confers none of them. Workers use the explicitly
   selected host context and allowed AgentNet transcript, never another
   workspace's native session. Harness tools retain their real permissions;
   a transcript boundary is not a sandbox for unrestricted Bash or MCP. If
   the host cannot meet its intended company-data boundary, it must choose a
   suitable responder configuration before enabling guest execution.
4. **Contact discovery.** Prefer targeted, policy-authorized lookup over
   whole-directory replication. A device-signed advertisement binds workspace,
   identity, route, generation and expiry. Another relay cannot silently
   replace a pinned device key. Push and reconnect refresh preserve the
   no-polling model. Unknown, contradictory or expired required authority
   evidence blocks the affected route; a signature alone does not prove
   freshness against a withholding relay.
5. **Peering and transit.** Bilateral agreements grant selective discovery,
   transit and durable custody independently. No transitive grant is implied.
   A future first experiment uses two domains and one explicit bilateral
   route. Selected third-party transit would additionally need bounded paths,
   expiry, hop/loop checks and deduplication. Direct delivery uses the same
   recipient authority, with an authorized relay fallback; lack of a route
   never authorizes broader discovery. No DHT or generic routing service is
   justified by this exploration.
6. **Custody and availability.** Begin with one accountable custody owner per
   message, durable storage and tested backup/restore. A handoff releases the
   source obligation only after destination custody is authenticated and
   stored. Lost acknowledgements can leave duplicate ciphertext, so admission
   must stay idempotent. Future cross-domain delivery proof must authenticate
   the destination: today's Hub receipt is not portable recipient-signed
   proof. Blob authorization, quota and retention belong to the accountable
   holder. Do not expire deduplication while valid replay could rerun work.
   Same-domain HA needs an explicit outage/data-loss target and fenced owner
   recovery before selecting replication; two SQLite files are not failover.
7. **Human identity and invitations.** Person roster consent continues to
   govern linked readers. A workspace admin cannot silently add a device to
   someone's person or recover lost private keys. A workspace invitation
   imports no prior conversations or grants. New conversation members receive
   selected earlier history. External agents participate through explicit
   host identities and context grants without making their whole workspace
   readers of the room.

Before implementing peering, test forged foreign keys, expired/withheld
authority, selective discovery, denied third-party transit, offline custody,
restart/lost acknowledgements/duplicate replay, and device revocation. Keep
same-named accounts in independent workspaces isolated throughout. These are
future experiment gates; none is represented as a completed mesh test.

## 8. Terminal messenger request (post-release, Sep 30)

Sergey requests an `agentnet tui`: a messenger in the terminal, usable both
standalone and through a possible Herdr plugin, with the same functionality
as the GUI wherever practical. This is a separate, explicitly non-blocking
feature request. Debate with the participating agents **after the current
release**; do not add implementation or dependency work to its critical path.

Initial feasibility: the Go client already exposes conversation, send,
decision and message-action operations, and the GUI uses a provider with
push-based changes. A terminal presentation can reuse those operations and
authority checks. This is a feasible direction, not an implemented TUI or
verified Herdr plugin contract.

Post-release discussion must resolve:

- Standalone invocation and Herdr integration: launching the same TUI in a
  pane versus a supported plugin. Verify Herdr's actual extension contract;
  do not assume an existing plugin API.
- Shared client/provider behavior, identity, workspace state and daemon
  ownership; avoid a second messaging protocol, permission model or poller.
- Keyboard-first navigation, composing/replies, people/teams/groups,
  participant selection, agent step-in/decisions and message actions.
- Practical parity for files, images, reactions, expressions, notifications
  and accessibility, with explicit terminal limitations and fallbacks.
- Packaging/library choice and maintainable shared tests only after the
  product/interaction debate. No framework or new dependency chosen now.

No TUI code, prototype, Herdr change or release commitment is authorized by
this recorded request alone. Root owns bringing it to the post-release debate.

## 9. D7: every interface is a standalone skin package (Oct 4)

Owner decision (Sergey, 2026-10-04), replacing ROOM_V1 D7 ("the previous
interface stays selectable until the skins work"): "every skin is a
standalone module. users can build their own skins."

- **One core, skins on top.** AgentNet is the core: the daemon (or the
  browser engine) and the documented skin contract (docs/UI_SKINS.md, host
  API v1). Every interface is a complete skin package (`skin.json`, entry
  module, stylesheet, declared files) loaded through the same host path:
  catalog, document rules, a root in a shadow tree, `mount`/`unmount`.
  Built-in packages differ only in trust: the host trusts its fixed list
  (`comic`, `classic`, `zoom`), never a manifest; installed and
  browser-local packages need the person's consent to their exact digest.
- **Comic is the default built-in package.** The messenger source
  (`internal/ui/web`) builds `internal/ui/static/skins/comic/`, embedded and
  served at `/assets/skins/comic/` on daemon and relay. It uses only the
  public host API; a contract-only check mounts it (and the Notebook
  example) from copied package bytes at an unrelated path.
- **Classic and Zoom** come in a later update as standalone packages on the
  same contract (extracted from the bundled app's sources, which stay in the
  tree for that and for their tests). The bundled app is no longer offered
  or loadable, and its emoji Comic lens is retired. A saved choice of
  `default`, `classic` or none opens Comic; `?skin=default` names Comic.
- **Contract additions (v1, additive):** `host.onSkinsChange`, a documented
  `host.manageLocalSkins(root) -> teardown`, an optional `host.reconnect()`
  (identity-checked rebind after this computer's program restarts),
  destination kinds for `host.onOpen` (`message` with `{conv, dir}`,
  `review`), and a manifest's `document` rules (`@font-face`, `@property`),
  which the host adopts at document level because browsers ignore them in
  shadow trees.
- **Switching.** Comic: Settings → Appearance → Skin. Every other skin gets
  the host's one switcher bar above it (one step back to Comic, the skin
  menu with import/removal of browser-local skins, the workspace control).
  Wording: interfaces are never called "previous", "earlier", "legacy" or
  "older", and AgentNet (the core) is never something to "switch back to".
