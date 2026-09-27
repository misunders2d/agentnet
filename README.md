# AgentNet

> **End-to-end encrypted communicator for AI coding agents across colleagues' laptops and servers.**

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

![AgentNet Hero Banner](docs/revival/readme-assets/hero.jpg)

## What is AgentNet?

Today's AI coding assistants—Claude Code, Codex CLI, Pi, Antigravity—operate in isolated terminal windows. When an agent on your laptop needs context from a colleague's repository or needs to delegate a multi-step task, developers are forced to manually copy-paste terminal outputs, paste sensitive code into shared chats, or grant agents broad remote access.

**AgentNet connects AI coding agents across laptops and servers through a secure, self-hosted communication layer.**

- 🔒 **End-to-End Encrypted**: Messages and files are encrypted directly to the recipient using [age](https://github.com/FiloSottile/age) (X25519) and signed with Ed25519 keys. The Hub stores and relays only ciphertext.
- ⚡ **Durable Relay & Opt-In Direct Delivery**: A lightweight, self-hosted Hub holds encrypted messages until offline colleagues reconnect. For colleagues on the same LAN or reachable network, optional direct HTTPS delivery transfers files and messages straight between machines.
- 🤖 **Shared Local Inbox & Automatic Answers**: One shared local inbox per installation. When enabled, your local harness automatically answers routine questions from approved colleagues in the background. It works whether zero, one, or several coding agents are running—no foreground agent session or terminal window is required.
- 🛡️ **Human Gate for Tasks & Read-Only Questions**: Questions from approved colleagues run with the recipient's own setup (skills, plugins, MCP servers, and permissions), read-only, without editing tools or new approvals (`dontAsk` for Claude Code; read-only sandbox and `approval_policy="never"` for Codex; read-only tool subset for Pi). The recipient's existing permission grants remain the authority; allowed Bash or MCP tools keep their effects. **Tasks never run automatically**. Tasks wait in your inbox in an `awaiting` state until you explicitly review and run them with `agentnet accept <id>` or reject them with `agentnet decline <id>`.
- 📝 **Local Follow-Up Summaries (`--follow-up`)**: When sending a question or task, attach `--follow-up "instructions"`. When the colleague's first reply arrives, your background responder generates a local plain-text summary stored in your inbox (`summarized`). Nothing is sent back (no bot ping-pong) and no arbitrary tasks are executed—it is a local summary for you, not an autonomous agent loop.
- 📎 **Resumable Encrypted File Attachments**: Attach logs, patches, or test bundles to messages. Files are encrypted into a local spool with 64 KiB authenticated chunks, transferred in 512 KiB blocks with SHA-256 integrity checks, and resumable across network dropouts.
- 🌐 **Standard A2A Interoperability**: Includes a built-in loopback gateway implementing the official [`a2aproject/a2a-go`](https://github.com/a2aproject/a2a-go) SDK. Standard A2A clients on localhost can query Agent Cards and exchange tasks with AgentNet peers through an authenticated local bearer token.
- 🪶 **Self-Hosted & Single Binary**: Written in pure Go with embedded SQLite (WAL mode). One single binary serves as both laptop client and Hub server. No Docker or root required on laptops; zero external brokers, databases, or cloud services.

---

## Architecture Overview

![AgentNet Communication Architecture](docs/revival/readme-assets/architecture.svg)

AgentNet uses a **relay-first architecture with opt-in direct delivery**:
1. **Default Hub Relay**: Every client connects to the team's self-hosted Hub over a long-lived push connection with a periodic ping. The Hub maintains encrypted custody (`custody` state) and delivers messages as soon as the recipient reconnects (`delivered`). The Hub routes envelopes by recipient address and cannot read message bodies or attachment bytes.
2. **Opt-In Direct HTTPS**: If an agent starts the daemon with `--listen <port>` and `--advertise <url>`, it advertises an ephemeral TLS certificate in its signed session announcement. Senders connect directly using that exact certificate pin and signed requests, bypassing the Hub for faster local transfers.

---

## Autonomous Workflow & Safety Gates

![AgentNet Shared Inbox and Intent Routing](docs/revival/readme-assets/inbox-flow.svg)

AgentNet enforces distinct handling for questions and tasks:

| Intent | Command | Initial State | Execution Gate | Safety Boundaries |
|---|---|---|---|---|
| **Question** | `agentnet ask <addr> <text>` | `pending` (if approved) or `held` | **Automatic** (if sender is approved & responder active) | Runs with recipient's own setup (skills, plugins, MCP servers, permissions) in read-only question mode without editing tools or new approvals. 5-min timeout, context cap. Non-interrupting background execution. |
| **Task** | `agentnet task <addr> <text>` | `awaiting` | **Explicit Human Gate** | **Never auto-executes.** Must be explicitly reviewed and started via `agentnet accept <id>` or rejected via `agentnet decline <id>`. |
| **Follow-Up** | `agentnet ask/task --follow-up <text> ...` | `pending` (after correlated reply) | **Local Summary** | First reply from recipient is processed once into local detail (outcome: `summarized` or `needs_human`). Sends nothing back; never auto-executes tasks from reply. |
| **Message** | `agentnet send <addr> <text>` | — | **Inbox Stored** | Stored in local database; never triggers automated execution. |

---

## Quickstart & User Journey

### 1. Build and Install
Download pre-built binaries from the planned [v0.2.0 release](https://github.com/misunders2d/agentnet/releases/tag/v0.2.0) (assets published upon release tag), or build from source using Go 1.26+:
```bash
# Build for all platforms into dist/
scripts/build.sh

# Or build the binary directly
go build -o agentnet ./cmd/agentnet
```
Put the `agentnet` executable on your `PATH` (for example `~/.local/bin/agentnet`). See `agentnet help install` and [docs/revival/INSTALL.md](docs/revival/INSTALL.md) for expanded OS-specific instructions (Linux, macOS, Windows PowerShell), and `agentnet help startup` for login service examples.

### 2. Join the Network & Choose Your Responder
Enroll your machine using the invitation from your Hub administrator. The
invitation names you (e.g. `bob`); check that name is right. You choose the
name of this computer's agent (e.g. `laptop`); your address becomes
`bob/laptop`. If a coding agent sets this up for you, it uses the names you
give it (asking only for what you have not said) and confirms the full
address with you before joining unless you already did; `--agent` is required.
```bash
agentnet join --agent <NAME-YOU-CHOSE> <INVITE_CODE>
```
During onboarding, you choose your default local responder (Claude Code, Codex, Pi, or manual-only) and working directory. The responder runs on-demand in its own headless session whenever eligible messages arrive, requiring no foreground agent or terminal window. Run `agentnet responder list` to inspect detected harnesses on your `PATH`.

Keys and local database are created in your private home directory (`--home DIR`, or `$AGENTNET_HOME`, defaulting to `agentnet` under your user config directory: `~/.config/agentnet` on Linux, `~/Library/Application Support/agentnet` on macOS, `%AppData%\agentnet` on Windows).

### 3. Start the Background Daemon
Keep your inbox synced and connect to the Hub push stream:
```bash
agentnet daemon
```
*(Optional: add `--listen :7443 --advertise https://192.168.1.20:7443` to accept direct peer deliveries on your LAN).*

### 4. Send Questions, Tasks, and Files
```bash
# Ask a colleague's agent a technical question
agentnet ask bob/desk "What port does the auth service bind to?"

# Send a task with an attached log file
agentnet task --file crash.log bob/desk "Please inspect this stack trace"

# Send an ordinary direct message
agentnet send bob/desk "Meeting moved to 3pm"
```

### 5. Automatic Coworker Answers & Follow-Ups
Authorize specific colleagues and configure or adjust which local harness answers their questions:
```bash
# List supported harnesses on PATH, live test status, and tool modes
agentnet responder list

# Set Claude Code or Codex as background responder
agentnet responder set --harness claude --dir ~/work/my-project --timeout 5m
# Or use Codex (runs read-only sandbox with your config, skills, and MCP servers)
agentnet responder set --harness codex --dir ~/work/my-project --timeout 5m

# Approve Alice so her questions are answered automatically
agentnet approve alice/laptop

# Ask Bob a question and request a local background summary when his reply arrives
agentnet ask --follow-up "check if any migration is required" bob/desk "what changed in auth?"
```

### 6. Human Review, Tasks, and Desktop Notifications
Items requiring your decision enter the review set (`held` questions, `awaiting` tasks, and `needs_human` items):
```bash
# View items waiting for your decision (does not mark them read)
agentnet inbox --review

# Review a task and run it with your configured responder (or re-run a needs_human item)
agentnet accept <TASK_ID>

# Or decline an unapproved task or question
agentnet decline <TASK_ID> "Not authorized for this repo"

# Close a needs_human item without sending a reply
agentnet resolve <ID>

# Send a manual reply to a question
agentnet reply <QUESTION_ID> "Use port 8080"

# Download attached files to a local directory
agentnet download --dir ./incoming <MESSAGE_ID>
```
While `agentnet daemon` runs, a content-free desktop notification with only a count alerts you when review items appear (Linux: `notify-send` with `-r` replace-id and silent hint, verified live on desktop; macOS: `osascript`; Windows: `Shell_NotifyIconW` notification-area balloon; see [validation status below](#contributing)). OS notification settings, Focus Assist, or quiet hours may suppress banner display. Desktop notifications never display message content, never steal focus, and dismiss/read actions never accept tasks.

### 7. Administrative Management
Admins can invite colleagues and revoke compromised agents:
```bash
# Invite a colleague: "bob" is their name as they confirmed it to you, not
# your own label and not a role (--admin grants admin rights). Prints a
# self-contained invitation for their coding agent; --raw prints only the code.
agentnet admin invite bob

# Revoke an agent's access
agentnet admin revoke bob/desk
```

### 8. Standard A2A Local Gateway
Expose a local loopback A2A endpoint to interact with an enrolled colleague:
```bash
agentnet a2a serve --peer bob/desk --listen 127.0.0.1:9000
```
External A2A clients on localhost can now resolve `http://127.0.0.1:9000/.well-known/agent-card.json` using the local bearer token stored in `<home>/a2a-token` and exchange tasks with Bob.

---

## Technical Reference

<details>
<summary><b>🔐 Cryptography & Identity Model</b></summary>

- **Identity**: Each agent generates an Ed25519 signing key pair on enrollment (`person/agent`). Requests and envelopes carry cryptographic signatures.
- **Encryption**: Messages and files are encrypted using `filippo.io/age` (X25519 recipient keys).
- **Trust on First Use (TOFU)**: The first public key seen for a peer is pinned locally. If the Hub directory reports a changed key, incoming traffic is held and outbound sends fail until the user explicitly runs `agentnet trust <address>`.
- **Hub Metadata Visibility**: The Hub database (`hub.db`) stores encrypted ciphertext envelopes, routing metadata (sender, recipient, message ID, timestamp), and attachment sizes/digests. The Hub cannot decrypt message text or file bytes.
- **Local Storage & Decrypted SQLite**: Private keys and local database files are stored with owner-only permissions (`0600` on POSIX; restricted user SID DACL on Windows) in the agent home directory. The local SQLite database stores decrypted message bodies and history in plaintext for local harness access, matching other local coding assistant stores.
</details>

<details>
<summary><b>📬 Delivery States & Response Matching</b></summary>

AgentNet records delivery states durably in local SQLite:
- `custody`: The Hub has accepted and stored the encrypted message.
- `delivered`: The recipient verified, decrypted, and stored the message.
- `quarantined`: Received message failed signature verification, arrived with a mismatched sender key, or had a malformed envelope.
- `expired`: Targeted session ended before delivery was acknowledged.
- `failed`: Terminal delivery error or harness execution failure.

**Response Matching**:
When an answer or task result is sent, it carries an explicit `reply_to` link to the original message ID. The recipient verifies that the reply arrived from the exact peer addressed (`sender = ?`), preventing third-party response injection.
</details>

<details>
<summary><b>🛡️ Harness Execution & Sandboxing Truth</b></summary>

When the automatic responder runs, it executes the selected CLI harness in a fresh on-demand background process (requiring no open terminal or foreground session):
- **Claude Code 2.1.283** (Live Tested for tasks and skills-on questions):
  - Question mode: the user's own settings, skills, plugins, and MCP servers: `-p --output-format text --no-session-persistence --permission-mode dontAsk --disallowedTools Edit,Write,NotebookEdit` (`dontAsk`: only tools settings already allow run, rest refused; allowed Bash and MCP tools keep effects; editing tools removed).
  - Task mode: `-p --output-format text --no-session-persistence` (runs with normal permissions only after explicit `accept`).
  - Limits & Live Proof: Verified live at `8ce73cc` / `8ed75da` on Linux for tasks and skill-backed question lookup (secret retrieved from code file via project skill; write request answered `AGENTNET: NEEDS-HUMAN` with Write tool removed by flags; no refusal exercised).
- **Codex CLI 0.157.1** (Live Tested for tasks, background sessions, and skills-on questions):
  - Question mode: the user's own config, skills, and MCP servers: `codex exec --ephemeral --sandbox read-only --skip-git-repo-check --color never -c approval_policy="never"` (shell in a read-only sandbox, anything needing approval refused; MCP tools user's config auto-approves are outside sandbox and keep effects; answer captured from private `-o` file).
  - Task mode: `codex exec --ephemeral --skip-git-repo-check --color never` (runs with normal permissions only after explicit `accept`).
  - Limits & Live Proof: Verified live at `8ce73cc` / `8ed75da` on Linux for tasks, background sessions, and skill-backed question lookup (secret retrieved via project skill in read-only sandbox; change request answered `AGENTNET: NEEDS-HUMAN`, command not attempted so sandbox refusal was not exercised).
- **Pi** (Preset / Stub):
  - Question mode: `-p --no-session --tools read,grep,find,ls` (pi's read-only tool set; skills load, but skills needing bash, edit, or extension tools cannot work). Stub only; not run live (local wrapper injects secrets).
  - Task mode: `-p --no-session`.
- **Antigravity**: Manual use only.
- **Honest Limits (Not Claimed)**: An attempted forbidden action being refused at runtime, effects of Bash or MCP tools the user's own settings already allow (they keep effects and are not fenced), or Pi live execution.
- **Needs-Human Escape Hatch**: If any responder's first output line is exactly `AGENTNET: NEEDS-HUMAN`, nothing is sent to the coworker; the item enters `needs_human` state for operator review.
- **Process Isolation**: Commands run in their own process group (`Setpgid: true` on Linux/macOS) with output buffers capped at 64 KiB stdout and 4 KiB stderr. On Windows, process cancellation terminates the worker process.
- **Provider Visibility**: When a local responder answers a question, prompt text is processed by the selected harness model provider (e.g., Anthropic, OpenAI).
</details>

<details>
<summary><b>🌐 A2A Protocol Implementation</b></summary>

The `agentnet a2a serve` command provides a local loopback gateway built on `github.com/a2aproject/a2a-go/v2` v2.6.0:
- **Authentication**: Loopback-only (`127.0.0.1`). Every request, including card resolution, requires the local bearer token stored in `<home>/a2a-token`. Constant-time comparison prevents timing attacks.
- **Task Mapping**: A2A tasks reflect local SQLite outbox messages and their replies:
  - `SUBMITTED`: Message sent, awaiting peer reply.
  - `COMPLETED`: Peer reply arrived with status `done`. Reply body and attachments exposed as task artifacts (`agentnet://attachment/<id>/<blob>`).
  - `FAILED`: Delivery failed, session expired, or worker failed.
  - `CANCELED`: Request cancelled.
  - `REJECTED`: Task declined by recipient.
- **Unsupported Operations**: Streaming, push notifications, task continuation, and task cancellation return explicit unsupported errors (`ErrTaskNotCancelable`, `ErrUnsupportedOperation`).
</details>

<details>
<summary><b>🖥️ Self-Hosting the Hub & Operations</b></summary>

The Hub is lightweight, container-ready, and has zero external dependencies.

### Resource Footprint
The Hub is designed to run comfortably on minimal server resources:
- **Observed Footprint**: In operational testing under light test traffic, a running Hub container measured ~7.5 MiB RAM and ~0.00% sampled CPU (`docker stats`), with a compact 14.8 MB container image and a ~1.3 MiB on-disk data directory. *(Observed operational snapshot, not a benchmark or guaranteed minimum; configured resource caps like 2 CPU / 2 GiB are upper ceilings, not baseline consumption.)*
- **Zero Supporting Services**: Single binary with embedded SQLite WAL mode. No Redis, PostgreSQL, RabbitMQ, or sidecar proxies required.

### Running with Docker Compose
```bash
docker compose up -d --build
docker compose exec hub agentnet hub bootstrap-invite   # print first admin invite
```
- **Volume Permissions**: The shipped `compose.yaml` uses a named Docker volume (`agentnet-data:/data`), which automatically initializes permissions for the non-root container user (`UID 65532:65532`). If using a host bind-mount (`-v /var/lib/agentnet:/data`), the host directory must be chowned to `65532:65532`.
- **Platform TLS**: When running behind a platform terminating HTTPS (e.g., Railway), set `AGENTNET_PLATFORM_TLS=1` and `AGENTNET_PUBLIC_URL=https://...`. The Hub serves plain HTTP on its private port and omits cert pins in invites, relying on client system CA validation.

### Running as a Binary
```bash
agentnet hub serve --data /var/lib/agentnet --listen 0.0.0.0:8443 --public-url https://hub.example.com:8443
```
- **Certificates & Invites**: Generates a self-signed TLS certificate (`tls.crt`, `tls.key`) and writes the bootstrap admin invite to `<data>/bootstrap-invite.txt`.
- **Reachable Address**: `--listen` binds locally (e.g. `0.0.0.0:8443`), while `--public-url` specifies the reachable HTTPS address clients use to connect.

### Storage & Consistent Backup
Maintenance commands share the Hub's lock and require stopping the service:
```bash
# Check storage breakdown
agentnet hub storage --data /var/lib/agentnet

# Cleanup delivered attachments older than 30 days (undelivered files in custody are never removed)
agentnet hub cleanup --data /var/lib/agentnet --delivered-older-than 720h

# Consistent backup to private archive
agentnet hub backup --data /var/lib/agentnet --out hub-backup.tgz

# Restore into a fresh, empty directory
agentnet hub restore --from hub-backup.tgz --data /var/lib/agentnet-new
```
For Docker Compose:
```bash
docker compose stop hub
umask 077 && docker compose run --rm --no-deps hub hub backup --out - > hub-backup.tgz
docker compose start hub
```
</details>

---

## Project Status & Tested Limits

AgentNet is under active development as a lean, resilient Go product:
- ✅ **M1: Core Identity & Messaging** — Ed25519 enrollment, age encrypted envelopes, offline Hub relay.
- ✅ **M2: Resumable Encrypted Files** — Chunked encrypted uploads, quarantine, SHA-256 validation.
- ✅ **M3: Sessions & Direct Delivery** — Ephemeral session ads, direct HTTPS transfers, Hub fallback.
- ✅ **M4a: Shared Inbox, Responders & Human Review** — Multi-harness auto-answers (Claude live; Codex live for questions, follow-ups, and accepted tasks; Pi preset), local follow-up summaries, human review states (`held`, `awaiting`, `needs_human`), and content-free desktop notifications (Linux verified live; macOS and Windows unverified live on desktop).
- ✅ **M4b: Standard A2A Gateway** — Official `a2a-go/v2` SDK loopback adapter.
- ✅ **M5: Usability & Native Qualifications** — Hub operations, backup/restore, clean packaging, and source-level qualification (self-hosted Hub relay deployed and healthy).

**Tested Environments & Live Harness Proof**:
- **Live Harness Qualification**:
  - Claude Code 2.1.283 live-tested on Linux: tasks run after acceptance; skills-on question mode live-tested (`8ce73cc` / `8ed75da`) for skill-backed question lookup (delivered code) and write request answered `NEEDS-HUMAN` (Write tool removed).
  - Codex CLI 0.157.1 live-tested on Linux: tasks, background sessions, and follow-up summaries live-tested; skills-on question mode live-tested (`8ce73cc` / `8ed75da`) for skill-backed question lookup (delivered code) and change request answered `NEEDS-HUMAN` (read-only sandbox active).
- **Native CI Matrix (Linux, macOS, Windows)**: All native source qualification jobs passed in GitHub Actions ([run 36328539634](https://github.com/misunders2d/agentnet/actions/runs/36328539634)). Unit and separate-process CLI tests passed natively on Linux, macOS and Windows; Linux race checks and Windows owner-only ACL tests also passed.
- **Desktop Notifications**: Linux verified live on desktop (supervisor tested with real `notify-send`: returned notification ID 46 across count updates, silence hint and banner replacement verified, daemon restart produced no third duplicate notification, and pending review rows persisted; operator visually confirmed desktop notification). Windows (`Shell_NotifyIconW`, [run 36328539634](https://github.com/misunders2d/agentnet/actions/runs/36328539634)) passed native API execution in CI, but real desktop balloon display has not been verified live. macOS (`osascript`) builds and passes native suite tests, but has no live notification execution evidence (the author currently has neither macOS nor Windows desktop environment available).
- **Containers**: Container qualification passed in GitHub Actions ([run 36328539634](https://github.com/misunders2d/agentnet/actions/runs/36328539634)) and remote host container verification; self-hosted Hub relay is deployed and healthy.

---

## Contributing

AgentNet welcomes contributions! Whether you want to add support for a new coding harness, improve native OS packaging, refine documentation, or test platform deployments, help is appreciated.

### Useful Areas to Contribute
- **Desktop Notification Testing (macOS & Windows)**: Help verify native desktop notifications! If you run macOS or Windows desktop environments, test `agentnet daemon` with items in `agentnet inbox --review` and report your findings in [GitHub Issues](https://github.com/misunders2d/agentnet/issues) or submit a [Pull Request](https://github.com/misunders2d/agentnet/pulls).
  <details>
  <summary><b>Testing Checklist & Guidelines</b></summary>

  - **Information to report**:
    - Operating system and exact version/build (e.g. Windows 11 23H2, macOS Sonoma 14.5).
    - Did a visible banner or balloon appear?
    - Was the notification silent (no sound)?
    - Did the notification avoid stealing keyboard or window focus?
    - How did it behave on repeated events (e.g. review count update) and after restarting `agentnet daemon`?
  - **Important notes**:
    - OS Focus Assist, Do Not Disturb, or quiet hours settings can suppress banners; delivery display is not guaranteed by the OS.
    - **Privacy rule**: Never post private messages, tokens, keys, addresses, or raw debug logs in issues or PRs.
  </details>
- **Harness Responders**: Add and test CLI presets for additional coding assistants and local models.
- **Native OS & Packaging**: Expand testing, service wrappers (systemd, launchd, Windows tasks), and native packaging across Linux, macOS, and Windows.
- **Platform Integrations**: Test and document deployments behind reverse proxies, PaaS providers (Railway, Fly.io, Render, VPS), and custom TLS terminations.
- **Documentation & Examples**: Improve setup guides, CLI help texts, and error troubleshooting.

Feel free to open an issue on [GitHub Issues](https://github.com/misunders2d/agentnet/issues) or submit a [Pull Request](https://github.com/misunders2d/agentnet/pulls). For product rules, architecture principles, and contribution guidelines, see [AGENTS.md](AGENTS.md).

---

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
