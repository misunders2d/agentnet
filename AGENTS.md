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
  plugins, MCP servers and permissions unchanged (see docs/revival/M4.md).
  AgentNet injects no tool exclusions, sandbox or approval overrides and no
  blanket CLI command restrictions on background runs. The recipient's own
  permission grants stay the authority: tools they already allow keep their
  effects, and AgentNet must not claim otherwise. Never blanket-disable skills
  or tools, or run questions with an empty or substitute configuration: the
  point is that a coworker gets the answer this person's agent would give.
  If an answer needs an action the harness may not take, the result is
  needs-human, not an invented answer. **Tasks** run only after the
  recipient accepts them (`accept ID`), or when the recipient has granted
  that sender's verified person or exact device key standing permission
  (`approve --tasks`, `accept --always`). Person grants cover current and
  future devices of the current verified roster; removed devices lose that
  access, pending key changes and frozen persons fail closed. Only own human
  keys may enroll roster devices, never agent hosts. Grants are local only,
  never set by received names or messages. Exact device grants need renewal
  after a changed key is trusted. Neither kind of grant
  reruns failed or interrupted work. Self-invite consent (owner
  decision D3, docs/plans/ROOM_V1.md §5) is the one invite accepted without
  a click: the host person's own agent, invited from the host device or an
  own device trusted there with `person approve --native`, with task keys
  only of trusted own devices. Separately, the person's own approved human
  devices, phones included (risk recorded on MEL-525), may confirm their
  own agent's exact bound proposal without a second approval. Agent-host
  keys have no such right; that trust too is local only. Either way the harness's normal
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

- **Batch verification before expensive runs.** Reproduce and fix known issues
  with cheap, focused checks first. Collect failures from an existing full run,
  address them together, and start another full/platform suite only when the
  integrated candidate is stable. Do not spend a separate 20–30-minute run on
  each bug. Retain valid evidence for unchanged code and rerun affected checks
  after a fix; complete the required release gates before publishing.
- Credit participating coding agents in commit trailers: Codex uses
  `Co-authored-by: Codex <noreply@openai.com>` and Claude uses
  `Co-authored-by: Claude <noreply@anthropic.com>`. Credit actual contributors;
  do not invent account emails for other assistants.
- Tests: `go vet ./...`, `scripts/race-shard.sh others`, and all client
  partitions `scripts/race-shard.sh client-K 12` for K=0 through 11. The script
  derives the current test list and keeps every race package at 600 seconds.
  `others` runs the remaining packages first, then isolates `internal/ui` in
  two sequential partitions. Focused UI reruns use `scripts/race-shard.sh ui-0`
  and `ui-1`; UI always has two partitions, independent of the client count.
  The older four/six partitions outgrew that bound; do not reuse their obsolete
  duration estimates or raise timeouts to make a run fit. Retain passing
  unchanged partitions when correcting a failure. Native Windows client
  qualification may use disjoint `^Test[A-G]` and `^Test[H-Z]` selections in
  parallel, separately from other packages, at the existing 1800-second bound.
  Platform-specific and opt-in skips must not be reported as executed tests.
  Add a focused regression for every bug. Tests that need a real model or
  infrastructure are opt-in (`AGENTNET_LIVE=claude`, `scripts/hub-container-test.sh`).
- Claims must match evidence: cross-compiling is not running on that OS;
  a local container test is not a platform deployment.
- Schema changes are new SQL steps appended in the owning store; never edit
  shipped steps.
- Never commit keys, Hub data, backups, logs or build output.
