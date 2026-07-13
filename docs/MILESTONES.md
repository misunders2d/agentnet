# Milestone breakdown

Phases sequence work; they are not permission to silently defer requirements.
Every unblocked lane is represented now, while high-risk features remain
disabled until their named evidence exists.

| Milestone | Delivered in this session | Remaining genuine gate |
|---|---|---|
| M0 Architecture/conformance baseline | runtime choice, repo layout, schemas/interfaces, reuse inventory, bake-off plan, threat plan, 85-ID ledger | none for reversible local work |
| M1 Identity and authority kernel | actor union, exact proof/replay, enrollment transcript, lab OOB, revocation, policy/grants/elevation | real OIDC/WebAuthn/independent device; PD-001–004/009/010 |
| M2 Communication/durability kernel | local encrypted queue, transactional mailbox, receipts, state machine, expiry, effects, offline reconciliation | HA PostgreSQL/object backend, failover/PITR/restore; PD-005/009/010 |
| M3 Collaboration/files | rooms, membership/history, transfer/tombstone, artifact staged acceptance and access | scanner/WORM/legal/retention evidence; PD-005–007/010 |
| M4 Harness and local bindings | four explicit adapter paths, MCP, Pi direct IPC, clean-worker/model-broker gates | real version-pinned harness isolation/recovery and installer tests |
| M5 Native A2A | official SDK route/mapping lane, strict trust/version/security helpers | TCK/cross-SDK/callback/public-peer Gate 4 evidence |
| M6 Federation | bilateral invitation/guest/non-transitive/revoke implementation, default disabled | partner lab and PD-008/009 |
| M7 C3 and future hubless | maintained-MLS and relay/quorum interfaces, default disabled | MLS lifecycle/PD-007; quorum/partition/revocation program |
| M8 Operations/release | config gates, audit, quotas, telemetry, schemas, tests, evidence ledgers | all applicable G1–G19 and PD-001–011 recorded |

Completion means observable evidence, not file presence. Local tests can promote
only H/L evidence; P/E/O gates remain external or owner-blocked.

## Post-initial-release roadmap

The items in this section are explicitly **not initial-release scope**. They
must not delay the initial release or be represented as already implemented.

### R1 — Durable response obligations

Add a first-class, opt-in `ResponseObligation` for ordinary conversations and
structured requests that require a reply. The initial release provides durable
delivery, exact retrieval, task completion semantics, and honest delivery
facts, but it does not durably track whether a non-task request has been
answered.

Required design outcomes:

- bind each obligation to the originating request ID and digest, conversation,
  accountable requester, responsible recipient, exact harness, deadline, and
  response schema where applicable;
- require a typed terminal response bound to the original request; prose alone
  must not silently satisfy a structured obligation;
- track an explicit lifecycle such as `open`, `acknowledged`, `in_progress`,
  `pending_human`, `blocked`, `satisfied`, `failed`, `canceled`, and `expired`;
- keep delivery/custody facts separate from obligation state rather than
  duplicating them as competing sources of truth;
- support authorization to create and satisfy obligations, amendments,
  cancellation, idempotency, revision fencing, multi-recipient `any`/`all`/
  quorum rules, rate limits, and escalation controls;
- derive privacy-safe counters for unread information, action required,
  awaiting peer, awaiting human, overdue, and failed;
- atomically link an accepted terminal response to the obligation so the
  system cannot report `awaiting peer` after that response is durably present;
- test missed wake events, offline replies, duplicate responses, conflicting
  terminal responses, unauthorized closure, overdue escalation, crash/retry,
  and exact retrieval of unclassified replies.

This roadmap item exists because the original design and use-case review tested
durable arrival, retrieval, typed tasks, retries, and duplicate handling, but
did not test the complete ordinary-conversation outcome: “a request requiring
an answer arrived; was its answer durably recognized, or was it escalated when
unanswered?”

Status: a local implementation now exists (`messaging/obligation.py`, storage
schema version 2, `/v1/response-obligations` routes, `agentnet obligation`
CLI, and hermetic tests) with the lifecycle spelled `created`,
`recipient_committed`, `acknowledged`, `in_progress`, `pending_human`,
`blocked`, `completed`, `failed`, `canceled`, `expired`. Evidence is H-tier
local only. Multi-recipient `any`/`all`/quorum satisfaction rules and
escalation-channel policy (PD-011) remain open; an obligation currently binds
exactly one responsible recipient harness.
