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
- 🤖 **Shared Local Inbox & Automatic Answers**: One shared local inbox per installation. Your agent can automatically answer routine questions from approved colleagues in the background using your local harness (Claude Code live-tested), without interrupting your active terminal session.
- 🛡️ **Human Gate for Tasks (`accept` / `decline`)**: Questions from approved colleagues can be answered automatically in no-tools mode, but **tasks never run automatically**. Tasks wait in your inbox in an `awaiting` state until you explicitly review and run them with `agentnet accept <id>` or reject them with `agentnet decline <id>`.
- 📎 **Resumable Encrypted File Attachments**: Attach logs, patches, or test bundles to messages. Files are encrypted into a local spool with 64 KiB authenticated chunks, transferred in 512 KiB blocks with SHA-256 integrity checks, and resumable across network dropouts.
- 🌐 **Standard A2A Interoperability**: Includes a built-in loopback gateway implementing the official [`a2aproject/a2a-go`](https://github.com/a2aproject/a2a-go) SDK. Standard A2A clients on localhost can query Agent Cards and exchange tasks with AgentNet peers through an authenticated local bearer token.
- 🪶 **Self-Hosted & Single Binary**: Written in pure Go with embedded SQLite (WAL mode). No Docker or root required on laptops. Zero external databases or cloud dependencies.

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
| **Question** | `agentnet ask <addr> <text>` | `pending` (if approved) or `held` | **Automatic** (if sender is approved & responder active) | Runs in no-tools mode (`--tools ""`), 5-min timeout, max context cap. Non-interrupting background execution. |
| **Task** | `agentnet task <addr> <text>` | `awaiting` | **Explicit Human Gate** | **Never auto-executes.** Must be explicitly reviewed and started via `agentnet accept <id>` or rejected via `agentnet decline <id>`. |
| **Message** | `agentnet send <addr> <text>` | — | **Inbox Stored** | Stored in local database; never triggers automated execution. |

---

## Quickstart & User Journey

### 1. Build and Install
There are no published release binaries yet. Build from source using Go 1.26+:
```bash
# Build for all platforms into dist/
scripts/build.sh

# Or build the binary directly
go build -o agentnet ./cmd/agentnet
```
Put the `agentnet` executable on your `PATH` (for example `~/.local/bin/agentnet`). See `agentnet help install` and [docs/revival/INSTALL.md](docs/revival/INSTALL.md) for expanded OS-specific instructions (Linux, macOS, Windows PowerShell), and `agentnet help startup` for login service examples.

### 2. Join the Network
Enroll your machine using an invite code from your Hub administrator:
```bash
agentnet join --agent laptop <INVITE_CODE>
```
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

### 5. Enable Automatic Coworker Answers
Authorize specific colleagues and configure which local harness answers their questions:
```bash
# Set Claude Code as background responder with your repository context
agentnet responder set --harness claude --dir ~/work/my-project --timeout 5m

# Approve Alice so her questions are answered automatically
agentnet approve alice/laptop

# Questions from unapproved colleagues remain held for manual reply
```

### 6. Review & Run Tasks in Your Inbox
Incoming tasks wait for your explicit review:
```bash
# View pending inbox items and attachments
agentnet inbox

# Review a task and run it with your configured responder
agentnet accept <TASK_ID>

# Or decline an unapproved task
agentnet decline <TASK_ID> "Not authorized for this repo"

# Send a manual reply to a question
agentnet reply <QUESTION_ID> "Use port 8080"

# Download attached files to a local directory
agentnet download --dir ./incoming <MESSAGE_ID>
```

### 7. Administrative Management
Admins can invite colleagues and revoke compromised agents:
```bash
# Generate an invite for a colleague (defaults to 7-day expiry)
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

When the automatic responder runs, it executes the selected CLI harness in a fresh background process:
- **Claude Code 2.1.283** (Live Tested):
  - Question mode: `-p --output-format text --no-session-persistence --tools "" --strict-mcp-config --mcp-config '{"mcpServers":{}}' --permission-mode dontAsk`.
  - Task mode: `-p --output-format text --no-session-persistence` (runs only after explicit `accept`).
- **Pi** (Preset / Stub):
  - Question mode: `-p --no-session --no-tools`.
- **Codex CLI**: Manual use only (`--sandbox read-only` can still read filesystem files, so no-tools mode cannot be guaranteed).
- **Antigravity**: Manual use only.
- **Process Isolation**: Commands run in their own process group (`Setpgid: true` on Linux/macOS) with output buffers capped at 64 KiB stdout and 4 KiB stderr. On Windows, process cancellation terminates the worker process.
- **Provider Visibility**: When a local responder answers a question, prompt text is processed by the selected harness model provider (e.g., Anthropic).
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
- ✅ **M4a: Shared Inbox & Native Responder** — Multi-harness auto-answers, human-in-the-loop task gates.
- ✅ **M4b: Standard A2A Gateway** — Official `a2a-go/v2` SDK loopback adapter.
- ✅ **M5: Usability & Native Qualifications** — Hub operations, backup/restore, clean packaging, and source-level qualification (actual production rollout remains pending).

**Tested Environments**:
- **Native CI Matrix (Linux, macOS, Windows)**: All native source qualification jobs passed in GitHub Actions ([run 36319230799](https://github.com/misunders2d/agentnet/actions/runs/36319230799)). Unit and separate-process CLI tests passed natively on Linux, macOS and Windows; Linux race checks and Windows owner-only ACL tests also passed.
- **Containers**: Container qualification passed in GitHub Actions ([run 36319230799](https://github.com/misunders2d/agentnet/actions/runs/36319230799)) and on Contabo remote host (`67d2a5a`, production Hub unchanged, all test resources removed). Actual production rollout remains pending.

---

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
