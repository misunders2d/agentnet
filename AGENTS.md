# AGENTS.md — working on AgentNet

AgentNet is a small, self-hosted communicator for coding agents: one Go
program, `agentnet`, that is both the laptop client and the Hub.

- **Fresh Agent & Contributor Entry**: read [docs/HANDOFF.md](docs/HANDOFF.md) for current scope, architecture, verification evidence, operations, and next tasks.
- **Architectural Decisions & Roadmap**: read [docs/DECISIONS.md](docs/DECISIONS.md) for debate records, Linear mapping, and design choices.
- **Product Overview & Quickstart**: read [README.md](README.md).
- **Installation & Operations**: read [docs/revival/INSTALL.md](docs/revival/INSTALL.md).
- **Milestone Notes**: `docs/revival/M1.md` … `M4.md` record each part's design, tests and honest limits.

## Product rules that code must keep

- **Identity is a key, membership is an admin's invite.** Every Hub request
  and every message is signed; messages and files are end-to-end encrypted
  (age). The Hub stores ciphertext and routing metadata only. Nothing a
  sender writes (kind, labels, text) grants authority on the recipient's
  machine.
- **Receipts say only what is proven:** custody, delivered, quarantined,
  expired; direct delivery counts only after the peer stored it. Never turn
  transport success into "done".
- **Offline is normal.** Outbox, spool, uploads and downloads resume after
  crashes and restarts; retries are idempotent; nothing already answered or
  run is repeated silently.
- **No polling.** The daemon holds one push stream; the only periodic
  request is the signed ping acknowledgement.
- **Questions** are answered automatically only for approved senders, by the
  recipient's chosen harness with the recipient's own setup: their skills,
  plugins, MCP servers and permissions, minus editing tools and anything
  needing a new approval (see docs/revival/M4.md). The recipient's own
  permission grants stay the authority: tools they already allow keep their
  effects, and AgentNet must not claim otherwise. Never blanket-disable skills
  or tools, or run questions with an empty or substitute configuration: the
  point is that a coworker gets the answer this person's agent would give.
  If an answer needs an action the harness may not take, the result is
  needs-human, not an invented answer. **Tasks** run only after the
  recipient accepts them (`accept ID`), or when the recipient has granted
  that sender's exact verified key standing permission (`approve --tasks`,
  `accept --always`); a grant is local only, never set by anything received
  or by names, stops holding when that key changes (until granted again) and
  never reruns failed or interrupted work. Either way the harness's normal
  permissions apply — nothing is bypassed. Workers never touch the user's
  open sessions.
- **Fail closed:** TLS is never skipped (pinned certificate or system CAs),
  changed peer keys block until trusted, revoked agents are refused, secrets
  and plaintext never go to logs.
- **Keep it lean:** standard library first; current direct dependencies are
  age, modernc SQLite, x/sys and the official A2A SDK. Add one only when a
  requirement needs it. No MCP server, no extra services.

## Code layout

| Path | What |
|---|---|
| `cmd/agentnet` | CLI and Hub commands |
| `internal/protocol` | Hub API types, addresses, request signing, invites |
| `internal/envelope` | signed, encrypted messages and attachment manifests |
| `internal/identity`, `secfile`, `lockfile`, `sqlitedb` | keys, owner-only files, process locks, schema steps |
| `internal/hub` | Hub service, presence, blobs, maintenance/backup |
| `internal/client` | agent: send/receive, files, daemon, direct delivery, worker |
| `internal/a2abind` | local A2A adapter (official SDK) |
| `itest` | separate-process journeys with the real binary |

## Working rules

- Credit participating coding agents in commit trailers: Codex uses
  `Co-authored-by: Codex <noreply@openai.com>` and Claude uses
  `Co-authored-by: Claude <noreply@anthropic.com>`. Credit actual contributors;
  do not invent account emails for other assistants.
- Tests: `go vet ./...` and `go test -race -count=1 -timeout 600s ./...`.
  Add a focused regression for every bug. Tests that need a real model or
  infrastructure are opt-in (`AGENTNET_LIVE=claude`, `scripts/hub-container-test.sh`).
- Claims must match evidence: cross-compiling is not running on that OS;
  a local container test is not a platform deployment.
- Schema changes are new SQL steps appended in the owning store; never edit
  shipped steps.
- Never commit keys, Hub data, backups, logs or build output.
