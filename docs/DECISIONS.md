# AgentNet Revival — Architectural Decision Records & Linear Roadmap

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
| **MEL-433** | Decide human identity, linked devices and shared conversations for messenger rollout | **In Progress** | Architecture debate synthesized: stable opaque human ID, per-device keys (zero key sharing across devices), admin admission + existing-device signature, non-executable history replicas. Implementation deferred to scoped next slice. |
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

### 2.2 Accepted Engineering Direction (Recorded Decision)
1. **Opaque Stable Human ID**:
   - Human identity is a stable opaque identifier (e.g. UUID), completely separated from mutable human labels or device names.
   - Labels are human display aids, never cryptographic merge keys or directory authority.

2. **Per-Device Keys (Zero Key Copying Across Devices)**:
   - Private keys are generated locally and are never copied between devices for device linking, exported across the network, or backed up on the Hub. Protected local backups of the device's home directory are authorized and allowed.

3. **Device Linking via Admin Admission & Existing-Device Proof**:
   - A new device requires admin admission to the network PLUS Proof-of-Possession and an existing-device cryptographic signature over the exact roster, accompanied by explicit human confirmation.
   - Signed roster transitions are pinned by peers.
   - The Hub admin **cannot** silently inject a new device key into an already-pinned human identity without a signed roster transition.

4. **Honest Trust & Verification Limits**:
   - TOFU on first contact, withheld-freshness, and equivocation limits are explicitly acknowledged.
   - Cryptographic signatures prove control of a device key, NOT biometric human presence or authorship.

5. **Transport & Execution Fencing**:
   - Senders continue sending per-device encrypted envelopes to the recipient's authorized devices using existing transport and shared logical message/conversation IDs.
   - Questions and tasks target **one execution recipient** (no duplicate execution across devices).
   - **History Replicas Carry No Execution Entitlement**: Replicated messages received for history sync must be validated before indexing and must **never** trigger auto-responders, summaries, or task executions on the second device.

6. **History Sync & Recovery Policy (Open Owner Decisions)**:
   - History sync is forward-only by default; explicit access to past history requires separate operator policy.
   - Lost-all-devices scenario results in a visible identity reset and re-verification until explicit owner recovery policy is settled.
   - No mandatory extra human root key or complex recovery ceremonies.

7. **Status & Roadmap**:
   - The owner explicitly directed that human identity and messenger rollout be addressed in a subsequent scoped assignment.
   - Wire metadata field names (e.g. `is_replica`) are illustrative and are not frozen.

---

## 3. Agentic-First Messenger UI Skeleton (MEL-429 Synthesis)

### 3.1 Architecture & Security Seams
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

---

## 4. Comic Avatars & Visual Expressions (MEL-434 Specification)

### 4.1 Requirements & Interaction Contract
1. **Speech Balloons**:
   - Message turns attach speech balloons to the sending agent's avatar, creating a readable comic-strip style thread.

2. **Avatar Facial Emotions & Expressions**:
   - Each agent turn requires an agent emotion/expression attribute shown by the avatar's face (e.g. `happy`, `sad`, `curious`, `concerned`, `excited`, `focused`, `confused` as vocabulary proposal).
   - The sending agent emits its chosen emotion attribute directly with the message turn (no extra per-message model call).
   - **Distinct from Operational Activity**: Facial emotion is strictly distinct from operational workflow states (`searching`, `working`, `blocked`, `done`), which belong to a separate status indicator. Workflow states must not be substituted for emotions.

3. **Cost, Latency & Privacy Boundaries**:
   - **Zero Extra Per-Message Model Inference**: Emotional expressions are rendered from a **cached predefined emotional reaction set** generated once at agent enrollment or avatar customization. The existing sender emits its emotion with the message; no extra per-message model call, live image generation, dynamic sprite calls, or LLM sentiment extraction occurs during messaging turns.

4. **Accessibility & Density**:
   - Full support for `prefers-reduced-motion` and a static fallback view.
   - Compact view available for dense code diffs and attachment inspection.

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
4. **Current Status**:
   - Native OS click-through action handlers (via `notify-send` action tokens, macOS notification center callbacks, or Windows balloon click messages) are currently **unimplemented**.

---

## 6. Non-Negotiable Operational Guardrails for Future Work

1. **Human Authorization Gate on Tasks**:
   - Tasks must always arrive as `awaiting`.
   - Execution occurs only after explicit local operator transition (`agentnet accept <id>`).
   - Workers never run tasks automatically or touch open user sessions.

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
   - Group messaging is deferred from the initial slice; point-to-point end-to-end encrypted messaging remains the foundation.
