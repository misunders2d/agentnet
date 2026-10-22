# AGENTS.md — working on AgentNet

AgentNet is a small, self-hosted communicator for coding agents: one Go
program, `agentnet`, that is both the laptop client and the Hub.

- **Fresh Agent & Contributor Entry**: read [docs/HANDOFF.md](docs/HANDOFF.md) for current scope, architecture, verification evidence, operations, and next tasks.
- **Architectural Decisions & Roadmap**: read [docs/DECISIONS.md](docs/DECISIONS.md) for debate records, Linear mapping, and design choices.
- **Product Overview & Quickstart**: read [README.md](README.md).
- **Installation & Operations**: read [docs/revival/INSTALL.md](docs/revival/INSTALL.md).
- **Milestone Notes**: `docs/revival/M1.md` … `M4.md` record each part's design, tests and honest limits.

## Product rules that code must keep

- **Ordinary clarification belongs in tagged chat** (owner rule,
  2026-10-22; MEL-497). Missing business information and ordinary choices are
  normal questions tagging the intended person, requester by default. A fresh
  authorized exact linked tagged reply continues with the recorded context and
  clears only its corresponding stale clarification attention when claimed.
  Untagged history never starts work. Reserve blocking requests for a concrete
  native permission or required execution authorization; preserve those grants.
- **Shared state follows each person across their devices** (owner rule,
  2026-10-22; MEL-693). For every user, messages and granted history,
  reactions, edits/deletions, read/unread state and personal reminders must
  converge across all current verified linked devices, including desktop,
  Android and browser. Reading a message or completing/canceling a reminder
  anywhere clears its corresponding attention everywhere once synchronized;
  snoozing/rescheduling clears the old due attention. Badges, native notices
  and harness hooks must respect that state. A separate harness-session cursor
  is not another unread inbox: already-read chats and completed reminders must
  not return as new or block unrelated agent work (MEL-692). Offline/restart
  catch-up and duplicate history must not resurrect cleared attention or run
  work again. Preserve newer unseen messages, unresolved decisions, exact
  selected-session replies and invited helpers' authorized context. Private
  read/reminder state stays within the verified person; membership/history
  grants and native execution permissions remain authoritative. Reuse existing
  signed/encrypted synchronization and push updates. This records the required
  behavior; the current implementation is not yet fully verified against it.
- **One active agent per harness per device** (owner rule, 2026-10-21).
  Default is a setting on that concrete agent, never another chat identity.
  Active directories and chat lists show usable current agents; retired
  configurations and unbound legacy conversations remain searchable history.
  Preserve original signed identities, messages and admitted executor snapshots.
  Never guess a historical harness from today's default or silently rebind a
  conversation. Reuse a proven existing identity when changing its default or
  folder; do not create a duplicate enabled configuration of the same harness.
- **AgentNet communicates; native harnesses govern execution** (owner rule,
  2026-10-14). Agents keep their own tools, skills, configuration and guardrails.
  Authorized collaboration must proceed through peer questions, task delegation,
  replies, steering and continuations without the owner manually relaying each
  step or repeatedly telling agents to continue. AgentNet must not invent an
  execution restriction because an upstream message is called a question,
  impose arbitrary workflow/agent limits, or override native permissions.
  Verified identity, current participation, selected audience/context and
  recipient consent remain communication boundaries. The old rule that a
  question cannot delegate a task is superseded; existing code enforcing it is
  a defect to repair, not a product requirement. See docs/DECISIONS.md and
  MEL-540/MEL-608; recording this rule is not proof that the code is repaired.
- **Invited helpers follow live chat context** (MEL-633, owner rule,
  2026-10-14). An active helper sees new messages and other agents' verified
  replies within its invited chat or topic, for DMs and groups alike. Earlier
  history remains limited to selected grants. Dismissal stops future access;
  other chats/topics and other agents' native sessions remain outside scope.
  Reading a peer's reply never starts another job or grants execution authority.
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
  Invitations, questions and tasks from any current verified human device
  of the agent owner need no additional
  AgentNet approval: those devices are the same person. Current signed roster,
  human role, exact pinned key and revocation checks still apply. Pending
  never-run own questions and tasks may proceed after upgrade; history replicas and
  completed, failed or interrupted work never restart.
  AgentNet injects no tool exclusions, sandbox or approval overrides and no
  blanket CLI command restrictions on background runs. The recipient's own
  permission grants stay the authority: tools they already allow keep their
  effects, and AgentNet must not claim otherwise. Never blanket-disable skills
  or tools, or run questions with an empty or substitute configuration: the
  point is that a coworker gets the answer this person's agent would give.
  If an answer needs an action the harness may not take, the result is
  needs-human, not an invented answer. **Other senders’ tasks** run only after the
  recipient accepts them (`accept ID`), or when the recipient has granted
  that sender's verified person or exact device key standing permission
  (`approve --tasks`, `accept --always`). Person grants cover current and
  future devices of the current verified roster; removed devices lose that
  access, pending key changes and frozen persons fail closed. Only own human
  keys may enroll roster devices, never agent hosts. Grants are local only,
  never set by received names or messages. Exact device grants need renewal
  after a changed key is trusted. Neither kind of grant
  reruns failed or interrupted work. Self-invite consent (owner correction
  2026-10-12, docs/plans/ROOM_V1.md §5) applies to all current verified own
  human devices, phones and browsers included, for every person and harness.
  It requires no `--native`, `approve --tasks` or `accept` step. The current
  signed roster, exact pinned human key, current invitation and task-key
  scope, enabled host agent, revocation and freeze checks remain mandatory.
  Agent-host keys cannot self-authorize as a human. Own human devices may
  also confirm an exact bound proposal without a second AgentNet approval.
  All harness permissions remain native; workers never touch open sessions.
- **Fail closed:** TLS is never skipped (pinned certificate or system CAs),
  changed peer keys block until trusted, revoked agents are refused, secrets
  and plaintext never go to logs.
- **Latest version only; outdated devices never impede others** (owner
  rule, v0.8.17). A relay running a release serves only that release: after
  its grace period an older device is suspended (HTTP 426 update_required;
  it may still ack, finish admitted answers/results and their read-only
  lookups and uploads) and is told to update; messages for it wait in custody.
  Only the relay's own release sets the threshold — an admin recommendation
  never suspends anyone. Senders never wait for a suspended device and never
  drop its copies. Every client updates itself (desktop app, headless/server
  daemon, browser reload) from the fixed release origin with checksums, only
  when no job runs. The version header is unsigned: availability only, never
  authority.
- **Keep it lean:** standard library first; current direct dependencies are
  age, modernc SQLite, x/sys, x/text Unicode normalization and the official A2A SDK. Add one only when a
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
  partitions `scripts/race-shard.sh client-K 18` for K=0 through 17 (owner-approved
  October 17, 2026). The script derives the current test list and keeps every race package at 600 seconds.
  `others` runs the remaining packages first, then isolates `internal/ui` in
  three sequential partitions (owner-approved October 22, 2026). Focused UI
  reruns use `scripts/race-shard.sh ui-0`, `ui-1` and `ui-2`; UI always has
  three partitions, independent of the client count.
  The older four/six/twelve partitions outgrew that bound; do not reuse their
  obsolete duration estimates or raise timeouts to make a run fit. Retain passing
  unchanged partitions when correcting a failure. Native Windows client
  qualification uses `bash scripts/native-client-shard.sh K 6` for K=0 through
  5, each on its own Windows runner, separately from other packages, at the
  existing 1800-second bound. The script derives the platform's complete parent
  list and assigns disjoint exact-name selections. Three alphabet bands sharing
  one runner exhausted that bound through disk/process contention; do not
  co-locate those test processes or raise timeouts. Retain every case when
  splitting selections, without changing individual timeouts.
  Platform-specific and opt-in skips must not be reported as executed tests.
  Add a focused regression for every bug. Tests that need a real model or
  infrastructure are opt-in (`AGENTNET_LIVE=claude`, `scripts/hub-container-test.sh`).
- **Mixed-version gate, required before every release** ("older apps must not
  impede others"): `TestMixedVersion` (itest/mixedversion_test.go) runs real
  programs: a relay built from this source as the next release (latest-only
  policy on), devices on this source, and devices on the newest published
  release tag below HEAD (v0.8.16 for v0.8.17), built from that tag. It checks
  that suspension of outdated devices leaves the others' DMs and groups
  undisturbed and nothing loops, and that an update by hand delivers the
  backlog. Run it locally with `git fetch --tags` then
  `go test ./itest -run TestMixedVersion -v -count=1` (about a minute; the tag
  build is cached in the user cache directory, `AGENTNET_COMPAT_CACHE`
  overrides; `AGENTNET_MIXED_TAG=vX.Y.Z` tries another published release).
  Without the tag it skips, and a skip is not a pass. CI's Linux native job
  checks out full history with tags and sets `AGENTNET_REQUIRE_COMPAT=1`, which
  makes a missing tag a failure.
- **Skins gate, required before every release** (owner rule, v0.8.25: "if we
  ever change the contract, the skins should also be updated"). The skin
  contract is docs/UI_SKINS.md with its route reference docs/HOST_API_ROUTES.md
  and the Comic kit (internal/ui/web/src/skin/README.md). Before tagging, in an
  agentnet-skins checkout pin `upstream.json` to the release candidate and run
  `AGENTNET_SOURCE=<this repository> npm test`: every skin in the collection,
  standalone and overlay, must pass against the candidate. A skin that fails is
  fixed in agentnet-skins (and its gallery release republished) or the contract
  change is reverted; never release past it. A skin's `agentnet` field names the
  release it was checked against.
- Claims must match evidence: cross-compiling is not running on that OS;
  a local container test is not a platform deployment.
- Schema changes are new SQL steps appended in the owning store; never edit
  shipped steps.
- Never commit keys, Hub data, backups, logs or build output.
