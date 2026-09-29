# AgentNet Revival — Fresh Agent & Contributor Handoff Guide

> **Target Audience:** Any incoming coding agent (Claude, Codex, Antigravity, Pi) or human engineer starting fresh in this repository without access to prior chat transcripts.
>
> **Messenger preview — 2026-09-29:** [v0.4.0](https://github.com/misunders2d/agentnet/releases/tag/v0.4.0) is published as a prerelease from `09c4da6`. [Final CI 36609670816](https://github.com/misunders2d/agentnet/actions/runs/36609670816) passed native Linux, macOS and Windows, Linux race and real IndexedDB checks, and the container journey. The preceding Windows failures were corrected in test-only commits `b0b8e7f` (close an opened file) and `09c4da6` (realistic heartbeat timing); their failed run remains recorded. All seven uploaded release asset digests match the reproducible build. Stable/latest remains v0.2.1; install this preview with `agentnet update v0.4.0`.
>
> **Live rollout:** the operator's relay at `https://agentnet.bezosapp.uk` and Linux laptop run v0.4.0. This hostname is deployment configuration, not a product default. The Hub was restored into a fresh volume from a stopped v0.3.0 backup (six enrollments, 121 messages, two attachments); the original volume remains intact. Public HTTPS and the unchanged legacy pinned endpoint report v0.4.0. Hosted JavaScript bytes match the release source; the real hosted landing page passed desktop and 390px Chrome checks. The laptop's key, connection/responder settings, peer pins, approvals and direct-device message IDs are unchanged. Its new person is Sergey, currently on admin/laptop. A read-only real laptop page check showed one self in Classic and Zoom, laptop under Your devices, Add a device available, no downgrade prompt and no 390px overflow; no message or link was created. Physical Android linking/install/files/background push/click remains the owner's next test, not yet qualified. Real Windows banner clicks and physical suspend/resume remain unverified; macOS is banner-only.
>
> **Identity and UI cutover:** v0.4.0 implements one person across linked devices, existing-DM history/files, grouped conversations, visible devices and browser-to-device messaging. Old preview person/DM rows stay unused; direct-device conversations remain. Before relinking an old preview browser, its owner must close all AgentNet tabs/app windows and clear that relay origin's site data. Then use the laptop page's **Your devices → Add a device**, choose an unused device name (the operator's old `admin/phone` address remains taken), and approve the request on the laptop within ten minutes. No agent cleared the owner's phone. Browser data and notification permission are lost by this explicit reset. The local page is opened through `agentnet ui`; the hosted page holds its own linked device key. [DECISIONS §2](DECISIONS.md#2-human-identity--multi-device-linking-mel-433-synthesis) records the model and limits.
>
> **Dated baseline — 2026-09-27:** [Client release v0.2.1](https://github.com/misunders2d/agentnet/releases/tag/v0.2.1) (Binary: `611b633`, prior evidence docs: `2395d11`, CI: `36341139910`), compatible with deployed Hub relay `v0.2.0`. Recheck current state before operations.
>
> **Linear Tracking:** Project [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580) (Tickets `MEL-409` through `MEL-435`; this repository stands completely alone if Linear is inaccessible).

---

## 1. Quick Orientation & Cold-Start Entry

### 1.1 What is AgentNet?
AgentNet is a minimal, self-hosted Go MVP communicator for coding agents. It enables agents on separate laptops, workstations, or servers to send messages, exchange large encrypted files, ask questions answered by the coworker's local agent setup, and delegate tasks—with no inbox polling, strict human authorization gates, and end-to-end encrypted payloads.

Routing metadata (sender, recipient, message ID, timestamps, attachment sizes and digests) is visible to the Hub for delivery; message text and file attachments remain encrypted ciphertext.

It is implemented as **one Go program**, `agentnet`, that serves as both the client CLI and the Hub relay.

### 1.2 Two Codebases: Main (Go) vs Legacy (Python)
- **`main` (Go Revival, v0.2.x)**: The active Go codebase. Standard library first, modernc SQLite, filippo.io/age encryption, official A2A SDK. This is the **only** active development branch.
- **`legacy/agentnet` (Python, v0.1.x)**: The historical prototype. It is preserved for archival reference only. **Never mix, edit, or import from the legacy Python codebase.**

### 1.3 Recommended Reading Path for Fresh Agents
To reach full productive context without reading lost chat sessions, follow this exact sequence:
1. **[`AGENTS.md`](../AGENTS.md)**: Product rules that all code must maintain (non-negotiable safety and architecture invariants).
2. **[`docs/HANDOFF.md`](HANDOFF.md)** (this document): Operational state, contracts, verification matrix, and current handoff boundaries.
3. **[`docs/DECISIONS.md`](DECISIONS.md)**: Architectural decisions ledger, debate resolutions (human identity, UI skeleton, comic avatars, notification clicks), and ticket mapping.
4. **[`README.md`](../README.md)**: Public user guide, quickstart, command summary, and tested limits.
5. **[`docs/revival/INSTALL.md`](revival/INSTALL.md)**: Setup, daemon login services, Hub operations, backup/restore, and updates.
6. **Milestone Design & Limit Notes**:
   - **[`docs/revival/M1.md`](revival/M1.md)**: Cryptographic identity (Ed25519/age), enrollment, Hub protocol, signed requests, push stream.
   - **[`docs/revival/M2.md`](revival/M2.md)**: Resumable chunked encrypted file attachments, streaming age, quota accounting.
   - **[`docs/revival/M3.md`](revival/M3.md)**: Ephemeral session presence (`#SESSION`), direct peer HTTPS delivery, Hub fallback.
   - **[`docs/revival/M4.md`](revival/M4.md)**: Multi-harness auto-answers, skills-on question mode, human task review states, headless review notices, conversation threading, loopback A2A gateway.

---

## 2. Core Invariants & Product Contracts

Every change to AgentNet must uphold these fundamental invariants:

1. **Identity is a Key, Membership is an Invite**:
   - Agents enroll with an Ed25519 signing key and an age (X25519) encryption key generated locally, stored in `<home>/identity.json`.
   - Address format is `person/agent` (e.g. `alice/laptop`).
   - Admins invite members (`agentnet admin invite <label>`); invite codes carry the Hub URL, label, secret, and certificate pin.
   - Public keys are pinned on first contact (TOFU). Changed peer keys block sending and hold incoming messages until explicitly trusted (`agentnet trust <address>`).

2. **End-to-End Encryption & Blind Hub**:
   - Each agent instance has a unique Ed25519 identity key and an X25519 age encryption key stored in `<home>/identity.json` (`0600` permissions; Windows protected owner-only ACLs via `internal/secfile`).
   - Private keys are generated locally and are never copied between devices for device linking, exported across the network, or backed up on the Hub. Protected local backups of the device's home directory are authorized and allowed.
   - Messages and attachments are encrypted directly to the recipient's public age key.
   - The Hub relay stores only ciphertext, routing metadata (sender, recipient, message ID, timestamp), and attachment sizes/digests. The Hub cannot decrypt content.

3. **Receipts Say Only What is Proven**:
   - `custody`: Hub stored the ciphertext.
   - `delivered`: Recipient verified, decrypted, and stored the message.
   - `quarantined`: Recipient received the envelope but signature or key verification failed.
   - `expired`: Directed session ended before delivery was acknowledged.
   - **Sender Delivery State Lag**: Sender delivery state can lag. Use `agentnet status --wait 30s ID` to refresh with a bounded receipt wait; there is no `agentnet wait` subcommand. Connect/ping retries queued sends and sends stored recipient receipts; it does not refresh every sender outbox state. Transport success is **never** conflated with task completion.

4. **No Polling**:
   - The client daemon maintains a single persistent HTTP/2 push stream to the Hub.
   - The only periodic network traffic is a signed ping acknowledgement every 90 seconds.
   - Inbox checks, file updates, and review notices wake reactively via daemon stream events, local socket kicks, or job completions.

5. **Questions vs Tasks vs Follow-ups**:
   - **Question (`agentnet ask`)**: Automatically answered **only** if the sender is approved (`agentnet approve <address>`) and a responder is active. Runs with the recipient's **own setup** (skills, plugins, MCP servers, permissions), with harness-specific fencing:
     - Claude Code: `--permission-mode dontAsk --disallowedTools Edit,Write,NotebookEdit`.
     - Codex CLI: `--sandbox read-only -c approval_policy="never"`.
     - Pi: `--tools read,grep,find,ls` (read-only tool subset).
     - Tools and Bash commands already permitted by user settings keep their native effects (not a blanket sandbox). If the model cannot answer without forbidden tools, it responds `AGENTNET: NEEDS-HUMAN`.
   - **Task (`agentnet task`)**: Stored as `awaiting` for the person and run only after `agentnet accept <id>` (or declined with `agentnet decline <id>`), unless the recipient has given that sender's **exact verified key** standing permission (`agentnet approve --tasks ADDRESS` or `agentnet accept --always <id>`). A grant is local only, never set by anything received or by names, stops holding when that key changes (until granted again), and never reruns failed or interrupted work; either way the responder runs with the recipient's normal permissions. A task inside a DM (conversation) is held for the person and nothing runs it. Workers never touch open user sessions.
   - **Follow-Up (`--follow-up <text>`)**: Stored locally on the sender's machine with the outgoing request. When the peer's first reply arrives, it triggers a single local summary run (`summarized` or `needs_human`). It sends nothing back to the coworker and never auto-executes tasks.

6. **Interactive Session Awareness & Hooks Contract**:
   - `agentnet hooks install claude|codex|pi`: Installs handlers (`SessionStart`, `UserPromptSubmit`, `PostToolUse`, `Stop`; for Pi, an extension with the matching events and an idle watch).
   - **Durable History vs Model Context**: Hooks provide metadata only (arrival summaries, at most 8 items per call). Full conversation text stays behind explicit `agentnet conversation <id>` reads.
   - **Per-Harness & Per-Session Cursors**: Each interactive session maintains its own cursor over `inbox.arrival`, independent of other sessions.
   - **No Idle Wake**: Claude and Codex sessions check arrivals only at natural turn hook points; an idle session waiting at a prompt is not woken up. Pi (below) shows a notice while idle without starting a model turn.
   - **Claude Stop Hook**: Live (`decision: "block"`, `reason: text`).
   - **Codex Hooks Adoption**: Installed into `~/.codex/hooks.json`; adoption requires manual user trust in `/hooks`.
   - **Windows Hooks Refused**: Hook installation is explicitly refused on Windows (`MEL-414`).
   - **Pi Hooks**: `agentnet hooks install pi` writes an extension for upstream Pi 0.87.1 (see [M4](revival/M4.md)): metadata-only notices at session start, with a prompt, at the end of a run, and while idle (a notice plus a message for the next turn, without starting a model turn). Evidence: tests and a live Pi run in RPC mode without a model; not run in an interactive Pi TUI or with OMP.
   - **Antigravity Hooks**: Unsupported (no verified hook contract).
   - Parallel hook calls in one session may repeat an arrival. Codex trust/adoption on the operator laptop remains unconfirmed; installation is not live proof. No request-origin routing chooses one foreground session: cursors are independent for each hooked session.

7. **Persistent Background Sessions & Context Retention (MEL-425)**:
   - Claude/Codex store `session_ref` on an inbox job and `session_head` per harness/session ID. Resume requires the first linked ancestor to be the latest job in that native session, to have ended cleanly, and to match peer, mode, canonical directory and preset. Branching from an older job, a changed preset/directory/mode, or interrupted/failed/cancelled/needs_human state starts fresh. Failed resume is not retried automatically. There is no context-exhaustion recovery mechanism in AgentNet. Native context lives in the harness store with harness-controlled retention; AgentNet history is separate. Pi stays one-shot. See M4 for full rules.

8. **Process & State Isolation**:
   - Responders run headless in their own process group (`Setpgid: true` on Unix).
   - Output buffers: standard command stdout is capped at 64 KiB and stderr at 4 KiB. For streaming Codex CLI executions (`codex exec --json`), individual JSON lines are bounded to 4 MiB (`codexLineMax = 4 << 20` in `internal/client/codexstream.go`), with bounded fallback to `-o`.
   - Process cancellation: On Windows, cancellation terminates the worker process (`cmd.Process.Kill()`), but child process cleanup is unverified. Pi argument length limits on Windows remain an open uncertainty (`MEL-414`).
   - Database operations use SQLite in WAL mode with append-only schema migrations.
   - All state, keys, databases, and downloads are restricted to owner-only permissions (POSIX `0700` home, `0600` files; Windows protected owner-only ACLs via `internal/secfile`).

---

## 3. Shipped State & Verification Matrix

### 3.1 Stable Release Baseline (preview status above)
- **Client Version:** `v0.2.1`
- **Git Commits:** Code: `611b633`, Documentation baseline: `2395d11`
- **Hub Version:** Compatible with `v0.2.0` and `v0.2.1`.
- **Binary Artifacts:** Cross-compiled into `dist/` via `scripts/build.sh` (emits six binaries only; release manifest `SHA256SUMS` is prepared separately during release packaging):
  - `agentnet-linux-amd64`, `agentnet-linux-arm64`
  - `agentnet-darwin-amd64`, `agentnet-darwin-arm64`
  - `agentnet-windows-amd64.exe`, `agentnet-windows-arm64.exe`

### 3.2 Automated CI Matrix (GitHub Actions)
All native source qualification jobs passed in GitHub Actions ([Run 36341139910](https://github.com/misunders2d/agentnet/actions/runs/36341139910)):
- **Linux (`ubuntu-latest`)**: Unit tests, race detector (`-race`), integration journeys (`itest`).
- **macOS (`macos-latest`)**: Native compilation, unit and integration test suite.
- **Windows (`windows-latest`)**: Native unit tests, owner-only ACL verification (`TestOwnerOnlyACL`).
- **Container**: Hub image build, bootstrap, backup/restore, offline catchup (`scripts/hub-container-test.sh`).

### 3.3 Live Verification Ledger
The following capabilities have been tested and verified on live machines:
- **Claude Code 2.1.283 (Linux)**:
  - Native background session resumption verified live at `c7b5d1a` / `0a73a04`.
  - Skills-on question mode verified live (`8ce73cc` / `8ed75da`): synthetic skill read delivered secret; file creation request answered `AGENTNET: NEEDS-HUMAN`; Write tool removed by flags.
  - Accepted tasks verified live in earlier milestone runs.
- **Codex CLI 0.157.1 (Linux)**:
  - Native background session resumption and streaming JSON event capture (`turn.completed`) verified live at `0a73a04`.
  - Skills-on question mode verified live (`8ce73cc` / `8ed75da`): read-only shell sandbox active; synthetic skill read delivered secret; `touch` request answered `AGENTNET: NEEDS-HUMAN`.
  - Standalone questions, follow-up summaries, and accepted tasks verified live in earlier milestone runs (`d6785bd`, `9575b2a`).
- **Pi (Linux, 2026-09-27 18:55 UTC, binary `611b633` / `v0.2.1`)**:
  - Diagnostic question lookup answered live from read-only records.
  - Explicitly accepted task sent exactly one Telegram notification via canonical skill/tool setup (`done` status; provider message ID independently verified in delivery ledger by supervisor with zero duplicates).
  - Headless environment credential timeout resolved via dedicated AgentNet PATH wrapper invoking host Infisical runner (host environment configuration only; zero daemon secrets or product dependencies; exact cause of the initial 5-minute timeout remains unproven due to absent transcript).
- **Desktop Notifications & Headless Review Notices**:
  - Linux desktop verified live with real `notify-send` (notification ID replacement, silence hint, duplicate suppression across daemon restarts, visual banner confirmed by owner).
  - Headless review notices verified live at `v0.2.1`: awaiting task on headless host never executed; single review notice recorded as `needs_human`/`notified=1` at laptop; owner visually confirmed desktop banner; synthetic item declined and resolved.
- **Zenbook Host Update Verification**:
  - Update notice reached relay custody only; recipient delivery and upgrade were not confirmed. Full unattended follow-up and returned-file roundtrip remain unproven on that host.

### 3.4 Honest Qualification Limits (What is NOT Claimed)
- **Runtime Tool Refusal**: Models answered `NEEDS-HUMAN` per system prompt; active refusal by the sandbox/harness was not exercised.
- **Allowed Bash/MCP Effects**: Tools and commands already auto-approved in user settings keep their effects; AgentNet does not provide a hypervisor sandbox.
- **Pi Broad Qualification & Persistent Sessions**: Pi is qualified **only** for the single diagnostic question and accepted task demonstrated; persistent sessions are not supported for Pi.
- **macOS & Windows Desktop UI**: macOS `osascript` builds and passes test suites, but has NO live desktop API execution evidence. Windows `Shell_NotifyIconW` passes native API execution in CI, but real physical desktop balloon display has not been verified live.
- **Platform Deployments**: Hub platform TLS tested with local proxy; not deployed to Railway or cloud PaaS.
- **Security boundary:** local history is plaintext in owner-only storage, not an encrypted local database. The Hub sees routing metadata and is trusted at first contact; compare fingerprints independently. There is no forward secrecy or retroactive removal of delivered data. Responder plaintext reaches its chosen model provider. See M1 for the full limits.

---

## 4. Codebase Architecture & File Map

```
agentnet/
├── cmd/agentnet/               # CLI Entrypoint & Subcommands
│   ├── main.go                 # Command routing, flags, global error handling
│   ├── help.go                 # Structured help topics (agentnet help <topic>)
│   ├── invite.go               # Join, admin invite, bootstrap invite parsing
│   ├── hook.go                 # Hook runner and installer for Claude Code & Codex
│   ├── hubcmd.go               # Hub serve, storage, cleanup, backup, restore
│   └── uicmd.go                # agentnet ui: the daemon-hosted messenger page (daemon --ui) and --demo
├── internal/
│   ├── protocol/               # Wire types, addresses, invite format, request signing
│   ├── envelope/               # Encrypted envelope format, age encryption, manifests
│   ├── identity/               # Ed25519 signing keys, age keys (identity.json)
│   ├── secfile/                # Owner-only file creation (0600/0700, Windows ACLs)
│   ├── lockfile/               # Process locking (prevent dual daemon/CLI corruption)
│   ├── sqlitedb/               # SQLite connection, WAL mode, schema migrations
│   ├── notify/                 # Native desktop notifications (Linux, macOS, Windows)
│   │   ├── notify_linux.go     # notify-send implementation with ID replacement
│   │   ├── notify_darwin.go    # osascript implementation
│   │   └── notify_windows.go   # Shell_NotifyIconW implementation
│   ├── hub/                    # Hub service, push streams, presence, blob storage
│   ├── client/                 # Agent client logic
│   │   ├── client.go           # High-level client API (send, ask, task, download)
│   │   ├── daemon.go           # Long-lived push connection, stream listener, ping ACK
│   │   ├── store.go            # Local SQLite schema (inbox, outbox, config, attention)
│   │   ├── worker.go           # Job runner for auto-questions and accepted tasks
│   │   ├── respond.go          # Manual transitions: accept, decline, resolve, takeOver
│   │   ├── responder.go        # Responder selection, detection on PATH, configuration
│   │   ├── attention.go        # Hook attention manager (per-session inbox cursors)
│   │   ├── session.go          # Session state tracking (session_head), native resume
│   │   ├── codexstream.go      # Streaming JSON event parser for Codex (4 MiB line cap)
│   │   ├── reviewnotice.go     # Headless review notice generator and forwarder
│   │   ├── conversation.go     # Threading and reply graph reconstructor
│   │   ├── direct.go           # Direct peer HTTPS server and client transfers
│   │   └── files.go            # Spooling, chunked uploads, verified atomic downloads
│   ├── a2abind/                # Loopback A2A protocol gateway (a2aproject/a2a-go/v2)
│   └── ui/                     # Embedded web UI for demo/inspection
│       ├── ui.go               # Web server setup, loopback binding, token auth
│       ├── server.go           # HTTP handlers, cookie exchange, event streaming (SSE)
│       ├── fixture.go          # Mock data fixtures for UI demo mode
│       └── static/             # Embedded HTML/CSS/JS assets
├── itest/                      # Out-of-process integration tests with real binary
├── docs/                       # Tracked documentation
│   ├── HANDOFF.md              # This handoff and operational guide
│   ├── DECISIONS.md            # Architecture decision records, ticket map, roadmap
│   └── revival/                # Milestone design notes and install guide
├── scripts/
│   ├── build.sh                # Cross-platform build script into dist/
│   └── hub-container-test.sh   # Container integration test script
└── compose.yaml                # Docker Compose file for self-hosted Hub
```

---

## 5. Standard Operating Procedures & Playbooks

### 5.1 Local Verification (Hermetic, No Model Calls)
For hermetic source verification without model calls or live deployments (tests use local network fixtures; dependency downloads may require network access):
```bash
# 1. Format and code analysis
go vet ./...

# 2. Run unit and integration tests with race detector
go test -race -count=1 -timeout 600s ./...

# Optional focused alternative: out-of-process CLI journeys (already included above)
go test -count=1 -timeout 300s ./itest/...

# Packaging only when required; compilation is not native runtime evidence
scripts/build.sh
```

### 5.2 Upgrading Clients
1. **Resolve Environment & Check Idle State**:
   - Resolve actual home, binary and owning service; inspect inbox job states and process tree privately.
   - Review-only listing (`agentnet inbox --review`) cannot prove idle.
   - Defer while `running`, `cancel_requested`, `accepted`, or claimable pending jobs exist.
2. **Stop Service & Recheck**:
   - Stop exact service (e.g. systemd user unit, launchd service, or Scheduled Task/daemon PID) and recheck before replacing anything.
   - *Never use blanket `pkill` commands.*
3. **Unique Owner-Only Backup**:
   - Create one unique owner-only backup directory; copy the stopped whole home, old binary, and relevant external hook configs (`~/.claude/settings.json`, `~/.codex/hooks.json`) there; keep its path for rollback.
4. **Verify Checksum & Install New Binary**:
   - Explicitly verify the new binary checksum against the release distribution manifest (`SHA256SUMS`).
   - Replace binary on `PATH` (e.g. `install -m 0755 agentnet-linux-amd64 ~/.local/bin/agentnet`).
5. **Restart & Verify Continuity**:
   - Start daemon service, run `agentnet doctor` to verify connectivity and automated schema migration (`agent.db.vN.bak` created before migration).
   - Compare identity fingerprint, responder/settings, approvals/peers and message IDs before/after without exposing private values.
6. **Rollback Procedure**:
   - If an issue occurs, stop the service first, then roll back **both binary and home directory/database together** (restore stopped home and database from the backup AND reinstall previous binary). Rolling back restores key and history consistency with the older binary, at the cost of any messages or attachments received after the backup was made.

### 5.3 Maintenance & Cleanup Commands
- **Client Local Cleanup**:
  ```bash
  agentnet cleanup [--saved]
  ```
  With the daemon stopped, removes encrypted copies of abandoned sends and direct uploads never attached to a message. `--saved` also removes directly received ciphertext of files already saved.
- **Hub Cleanup**:
  ```bash
  agentnet hub cleanup --data DIR [--delivered-older-than 720h] [--unattached-older-than 24h]
  ```
  With Hub stopped, removes delivered attachment ciphertext and old unattached reservations. Undelivered files in custody are never deleted.
- **Hub Consistent Backup & Restore**:
  ```bash
  agentnet hub backup --data DIR --out hub-backup.tgz
  agentnet hub restore --from hub-backup.tgz --data NEWDIR
  ```
- **Admin Release Recommendations (Equality-Only Nudges)**:
  ```bash
  agentnet admin release set --url https://... [--note TEXT] VERSION
  ```
  Broadcasts advice to members. Compared strictly for string equality (no semver ordering, no automatic installation).

### 5.4 Private Recovery Discovery (No Secrets In Logs)
If a local database or key file is damaged:
- Check for automatic pre-migration backups: `<home>/*.vN.bak`.
- Keys are located in `<home>/identity.json` (holds Ed25519 and age keys). Permissions must be owner-only (`0600`). On Windows, verify protected owner-only ACLs via `icacls`.
- To test Hub connectivity without sending messages: `agentnet doctor`.
- **Never paste raw keys, invite codes, or auth tokens into public issues or transcripts.**

### 5.5 Headless Troubleshooting & Operational Continuation
- A relay and an enrolled server responder are different processes/homes. The relay does not answer questions by itself. On the receiving installation check `doctor`, `responder show`, sender approval and `inbox --review`. A delivered question may be held; approval does not itself run an older held item. Tasks require explicit local acceptance (`accept ID`) unless the local user granted that sender's exact key (`approvals` lists grants and whether they still hold).
- Use `conversation ID` for durable two-way history without changing read state. Ordinary `inbox` marks listed messages read; `inbox --review` does not. Read state never proves a model or person understood the message.
- The daemon's service environment can differ from an interactive shell. Resolve the actual harness on its PATH and its canonical credential bootstrap. In the live Pi task, an AgentNet-only wrapper reused the host's existing secret runner for the child; the daemon received no injected Telegram secret. This was host configuration, not a mandatory Infisical dependency. Preserve the host's non-poller setting and AgentNet background-hook isolation.
- A timeout does not prove an external effect failed. Before retrying an effectful task, check its provider/ledger evidence and any human confirmation needed to rule out duplicates. The successful Telegram acceptance was independently verified once; it did not prove the person read it.
- `review-to ADDRESS` forwards count-only attention to a chosen installation; both ends need the feature. It neither grants remote acceptance nor forwards request content. Optional notifications for ordinary messages are separate and built in local commits (desktop alerts from the daemon, browser push; see the notifications item below).
- On an existing host, discover the actual service, home, responder directory, private stopped backups and rollback record locally with authorized access. Never assume another machine has the operator's paths or credentials. The repository deliberately contains no private deployment inventory or recovery secrets.

---

## 6. What to Do Next & Active Boundaries

### 6.1 Immediate Focus Areas
1. **Pilot Deployment & Real-World Use**:
   - Begin small-scale operator deployment using released `v0.2.1` binaries.
   - Monitor real-world desktop notifications and review notices across Linux desktop environments.
2. **Community Verification (Call for macOS & Windows Testers)**:
   - Verify native desktop balloon/banner display on physical macOS and Windows machines (see checklist in [`README.md`](../README.md#useful-areas-to-contribute)).
3. **Linear Tracking**:
   - Track progress across tickets `MEL-409` through `MEL-435` in project [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580).

### 6.2 Explicitly Held / Deferred Work
- **Messenger continuation contract (2026-09-28)**: Before scoping chat work, read
  [DECISIONS §3.2](DECISIONS.md#32-persistent-conversations--temporary-participation-2026-09-28).
  Conversations outlive model sessions; participant/history access is explicit.
  An agent's first report does **not** dismiss it: it remains available for
  follow-ups, with no model calls merely to wait, until explicitly dismissed.
  This records design direction and acceptance examples, not implementation approval.
- **Messenger architecture (reviewed design, partly built: S-A, the People directory, Pi attention, the
  human-DM core and its page candidate, browser-wire infrastructure; the rest is proposal)**: read
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) before any messenger code.
  It covers the real Provider/backend contract, relay-served phone/browser devices
  and the requirement that the relay stays one portable unit (binary/container,
  one data volume, one port). Owner choices listed there (§15) remain open.
- **Increments, not the messenger (owner correction, 2026-09-28)**: the device-inbox page (S-A,
  directory included) and the N7 browser transport (S-W, v1) do not complete the messenger contract of
  [DECISIONS §3.2 and §2](DECISIONS.md). Still required: choose a person (e.g. Vitalii), hold a human DM,
  explicitly invite an agent with the context you allow, clear human/agent participants and authorship,
  and follow-ups until an explicit dismissal. A message kind is not a participant's identity or an agent
  invitation; an address is one installation's key, not a person; nothing is merged by address. Zoom
  must show each human with visible links to their own agents (ownership, not presence or invitation;
  from the reviewed identity/agent relationship, never labels or addresses; no device-as-human shortcut).
  See [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Human-DM core (corrections accepted)**: `1d97945` (core Claude) adds, from the CLI (`agentnet person create`,
  `agentnet dm`), explicit single-device persons, signed DM roots, envelope v2, capability records, admission
  dedupe, and holds every conversation question/task (nothing runs). Root review defects R1, R2, C1, C2 and the
  frozen-person deferred send are fixed (`9f5bea1`, `5da7fa1`, `7247759`); root accepted `7247759` with focused
  race tests, closing the core correction gate. Agent participation and execution, Zoom's links and the browser
  device were built since (items below), device linking too (MEL-433 item below); not built: groups.
- **Human DMs on the page (`4a0659c`, accepted after Agy's review)**: person setup by hand, people listed by their claimed name until checked,
  separate persistent DMs, a message-only DM composer with receipts, held questions/tasks (nothing runs), frozen
  DMs; apart from device history. `9c1d4b4` (candidate, review pending) adds people search and DMs in Comic and
  Zoom. Agent invitation, Zoom's person–agent links, browser DMs and device linking were built since (items
  below).
- **Release gate (owner request, 2026-09-29)**: a whole-team pre-release review of a stable candidate SHA
  (inventory, per-area and cross-area review, one or two challenge rounds, fix and recheck per confirmed defect,
  Codex synthesis, only genuine product decisions to Sergey). Rows R1–R12 are listed in
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status); the working checklist is root's temporary
  `/tmp/agentnet-release-review-20260929.md`. Nothing is released until each outcome is verified or explicitly
  excluded by Sergey; the inventory is not approval.
- **Notifications required for this release (owner, 2026-09-29)**: "it's messenger! it's supposed to notify
  people. optionally, of course." Optional notifications for ordinary messages on desktop and mobile, also with
  the app or page closed; opt-in only (no permission request on page load; denied or off leaves a fully usable
  messenger); no polling, no plaintext to the relay or a push service, no model calls; mutes and click to the
  conversation; platform limits stated plainly. This supersedes the S-P deferral (release row R13). Contract
  [`docs/revival/NOTIFY.md`](revival/NOTIFY.md); built in local commits (Hub and browser device, daemon desktop
  alerts and page controls; evidence and what is not shown are in the next item and in
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status)).
- **Agents in DMs (2026-09-29, local commits)**: core records and execution (`1c35ad4`, `fc87c3f`, `85af6a8`, accepted
  for UI use; assembled/platform gates open); daemon pages invite, decide (host only), ask, dismiss and act on requests
  (`78b172f`, `fa9ccc0`), with people linked to their agents in Classic/Comic/Zoom (`3c61430`); the browser device
  invites, asks and dismisses the other person's agent and never runs one (`061faaf`). See
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status). Notifications ([`docs/revival/NOTIFY.md`](revival/NOTIFY.md)):
  Hub side `a83aa4d` and the browser device's side (`6adc18d`, `63f1ac9`) built locally; one real desktop Chrome 154
  (Linux) implementer-observed pass through Google's push service showed opt-in (permission answered by the test),
  presented suppression, closed-page alerts, click to the exact DM, per-DM mute and off (loopback, synthetic; no
  provider 2xx log). Desktop daemon alerts: core `a85b84c`/`99b2559`, page controls `cd54168`, one Linux pass (native
  banner, suppression, mute, off; click route opened directly; a click reaching an already open tab is taken too,
  `81792ef`). Windows clicks: core/root `b08e65b`, `ab440c7` (their evidence). Not shown: deployed HTTPS,
  phone/iPhone, other browsers, macOS clicks.
- **Files and pictures (MEL-489, local commits)**: core `e6a3048`, `960ceda`, `423e937`; pages `8e1c64b`, `6ddb450`,
  `fdca26f`; browser gate `2a86600`. DMs on both pages and device conversations (v1) on the daemon's page: choose,
  paste or drop, pending name/size/remove, explicit Send (file-only allowed), per-conversation drafts in all three
  views, limits said first; received pictures (PNG/JPEG/GIF/WebP by their bytes) shown in place, anything else only
  saved under its safe name; agents are told files exist, never given them. Evidence: Go/browser interop both ways
  (tampered blob and lying manifest refused, hostile names), provider/page tests, one real Chrome journey on loopback
  (both pages, offline, reload, back online, phone width, second tab). Not shown: a phone's picker, a deployed relay;
  unsent draft files live only in the open page.
- **Remind me later (S-R, local commits)**: core `ac517b5`, daemon page `0474bda` (set/move/done/cancel on received
  messages, overdue first in the list and Zoom); one Linux pass: banner at the chosen minute, due shown live, a reply
  ended it. Not on the browser device.
- **Browser device (2026-09-29, local commits, review in progress)**: `hub serve --web` serves the page at the Hub
  origin (`7a97aaf`), `admin invite --link` prints browser invite links (`3a588c3`); the human-only browser engine,
  plain join page and installable manifest are `6061c11`, `9ecd3c8`, `2d05b2c`, `e971c16`, `1cab993`. The app icon
  is the owner's chosen ant on a light tile (`ae7f7ea`: favicon, header mark and the installed app's icons, both pages). Verified: Node journeys against a real
  test Hub, the opt-in real IndexedDB test (`AGENTNET_CHROME=google-chrome-stable go test ./internal/ui/static`), a
  real Chrome 154 journey on loopback including install and standalone launch. Not verified: deployed HTTPS relay,
  phone, Firefox, Safari. See [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Owner decisions for two-person DMs (2026-09-29)**: either person may dismiss an invited agent; selected earlier
  DM messages may be shared with an agent, the choice visible to both people, with no separate approval from the
  other person; the host's acceptance stays explicit. See [MESSENGER_ARCHITECTURE.md §9.2 and §15](MESSENGER_ARCHITECTURE.md).
- **Next release scope (recorded; v1 contacts/conversations navigation, search of known agents and
  conversations, separated review reports and the People directory are implemented, the rest not)**: S-W relay-served HTTPS browser/phone,
  a People directory with event-driven presence (online = daemon connected, shown only while this daemon
  is connected to the relay; listing grants no trust; choosing a member opens a draft, never sends), Pi native attention (built for Pi 0.87.1:
  `2479a7e`, `4b1c7a2`, `587a1d5`; evidence and limits in [M4](revival/M4.md)), and contacts and conversations (reviewed
  direction, corrected by the owner: one row per exact address holding separate conversations,
  never merged; grouped remote notices; no remote approval), and one search for agents and
  conversations (kinds shown, opens exactly the chosen item, grants no trust; people are not searchable yet). See
  [MESSENGER_ARCHITECTURE.md §16](MESSENGER_ARCHITECTURE.md#16-a5-rollout). Chrome install: the relay page's
  manifest is built (`1cab993`; the ant icon since `ae7f7ea`) and was installed and launched in Chrome 154 on loopback, not
  on a deployed relay; S-R reminders unchanged. One-command `agentnet update` (latest stable,
  same home, daemon and page switch safely): in source, not released; Windows switches only a daemon
  started by the scheduled task `\agentnet` (native test passed in Actions run 36458378377; a standalone
  daemon is not stopped, an explicit limit)
  ([§19.2b](MESSENGER_ARCHITECTURE.md#19-updates-and-versions)).
- **Human Identity & Multi-Device Linking (`MEL-433`, local commits, review pending)**: one person on several devices,
  a clean cut to person v2 (re-enrollment, no v1 salvage). Core (core Claude): roster chains, own-device links (one QR,
  approved on an existing device; no admin invite per device), fan-out to every device, history snapshot and
  forwarding, files of history on request (`886afe1`, `80f9717`, `ef125db`, `4ea4f92`, `efeb9aa`, `dbb640f`); CLI join
  with a link (`c2d34a1`, root). Pages (frontend Claude): setup as a person or a service, "You on N devices" and each
  person's devices behind one closed disclosure, add/approve/remove devices with a QR, where each message came from,
  history progress, history files (`8201b6c`, `0f1026d`, `cd4702f`, `3a0af06`, `9616b1e`, `63ad48e`, `fc897bc`,
  `d94b84c`); the browser device speaks person v2 and links either way, copies and keeps chats and files
  (`a94b3d2`, `c050417`, `77dd232`, `63ad48e`) and writes to devices again (v1, `f5be650`; owner's Android report).
  Evidence: Go/browser tests against real Hubs and daemons (browser as new and as approving device, history,
  stale-roster forwarding, files after the Hub dropped them, device questions with a stub responder); one sole-tab
  Chrome journey at phone width on loopback (link, CLI approval, history and its files, Zoom device chat, DM after).
  Not shown: a phone, a deployed relay, the browser approving a real second browser; recovery of a lost last device
  is not built. See [`docs/DECISIONS.md`](DECISIONS.md) and [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Comic Avatars & Visual Expressions (`MEL-434`)**: Avatar facial emotion requirement agreed (sender emits emotion with turn; cached predefined reaction set; no extra per-message model call); asset generation, emotion vocabulary, art direction, and implementation deferred.
- **Notification Click Focus (`MEL-435`)**: a click opens its conversation: the browser device's service worker, the daemon's Linux alert (`#conv=`, also in an open tab since `81792ef`), Windows (`b08e65b`, `ab440c7`); macOS alerts have no click.
- **Owner requests for later (2026-09-29; backlog, not this release)**:
  - custom visual plugins, [MEL-475](https://linear.app/mellanni/issue/MEL-475/secure-custom-messenger-ui-plugins):
    each user bundles presentations like Classic/Comic/Zoom; isolated, least permission, no secret access or silent
    export, core keeps all authority; architecture, API and package format undecided;
  - editing sent messages, [MEL-476](https://linear.app/mellanni/issue/MEL-476/edit-sent-messages): visible edited
    state, authenticated sender revisions synced to offline devices, never a silent rewrite or rerun of accepted/run
    work; edit windows, history visibility, attachments and conflicts undecided.

  See
  [MESSENGER_ARCHITECTURE.md §20](MESSENGER_ARCHITECTURE.md#20-owner-requests-for-later-backlog-not-this-release).
- **Live Daemon Web UI Integration (`MEL-429`, slice S-A)**: `agentnet daemon --ui 127.0.0.1:0` serves the
  messenger page over this home's real inbox from the daemon that owns the home; `agentnet ui` prints its
  address (token kept in the owner-only `HOME/ui-url`, never logged). Classic, Comic and Zoom presentations
  share one Provider and the same decision dialogs. Threads are derived v1 reply links;
  every action is an existing `Agent` operation with its CLI gates; changes are pushed (store writes and
  the local kick socket), never polled. It is a page on this computer only: no relay hosting, phone or
  browser devices, groups, or push (S-W, S-C, S-P and later); human DMs (v2), files (MEL-489), the browser
  device and notifications were built since (items above).
  Tests: `internal/ui`, `internal/client/uiview_test.go`, `itest/ui_test.go` (two synthetic homes, real binary).
- **Group Messaging & Federation**: Deferred from initial slice.
- **Automated Task Execution**: Tasks run only after `agentnet accept ID` or under a local, per-key task grant (`approve --tasks`, `accept --always`; see docs/revival/M4.md). No other path may run a task: nothing received, no name match and no policy engine grants it.

### 6.3 Team Roles & Next Slice Design Scope
- **Human Owner & Caller**: Sergey (final authority on product scope, security rules, emotion requirements, and release approvals).
- **Supervisor**: Codex (`p1`) (architecture, Linear project coordination, verification reviews).
- **Critic & Systems**: Agy (`p4`) (code reviews, documentation audits, system verification, hermetic testing).
- **Stopped Workers**: Core Claude and UI Claude were exited at the owner's request. Pane identifiers above describe this session only; discover current agents before delegation. Do not relaunch them without a new assignment.
- **Next Slice Design Scope**:
  - **MEL-433**: Human identity, linked devices, and shared conversations for messenger rollout.
  - **MEL-434**: Comic-book conversations, expressive avatars with required facial emotions (`happy`, `sad`, `curious`, etc.) distinct from operational status, and cached predefined reaction sets.
  - **MEL-435**: Deep-link notification click focus to exact conversation without task side effects.
  - **MEL-429**: Live daemon integration for lightweight messenger UI.
