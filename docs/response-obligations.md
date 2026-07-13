# Durable response obligations

A `ResponseObligation` records that one accepted conversation request demands
an answer from one exact responsible recipient harness, and tracks whether
that answer was durably recognized. It is opt-in, separate from delivery
custody, and can never be satisfied by prose or by an unrelated reply.

## Creating an obligation

Add the strict `response_obligation` spec to a `post` or `structured_request`
conversation action. The spec is part of the exact request payload digest.

```json
{
  "kind": "structured_request",
  "request_type": "inventory.lookup",
  "arguments": {"sku": "ABC-123"},
  "response_obligation": {
    "response_required": true,
    "responsible_harness_id": "harness-abc",
    "deadline_at": "2026-07-14T12:00:00+00:00",
    "response_schema_id": "inventory.lookup.result"
  }
}
```

Rules:

- `response_required` must be `true`; omit `response_obligation` entirely for
  informational content that does not create answer ownership;
- the requester must be the request author and hold the
  `conversation.response_obligation.create` entitlement for the conversation;
- exactly one responsible recipient harness is bound; with a single recipient
  the spec may omit `responsible_harness_id`, otherwise it is mandatory and
  must be one of the recipients;
- the deadline, when present, must be timezone-aware and in the future;
- the obligation row commits in the same transaction as request acceptance;
  an idempotent request retry never creates a second obligation and returns
  the existing obligation identifier, state, and revision;
- multi-recipient `any`/`all`/quorum rules are not implemented; create one
  obligation per responsible recipient.

## Lifecycle

```text
created -> recipient_committed -> acknowledged -> in_progress
                |                     |     \        |    \
                |                     |  pending_human <-> blocked
                v                     v               v
   (terminal) completed | failed | canceled | expired
```

| State | Owner | Meaning |
|---|---|---|
| `created` | requester transaction | request accepted, obligation open |
| `recipient_committed` | mirrored from mailbox | the durable recipient record proves custody; never independently asserted |
| `acknowledged` | responsible recipient | recipient explicitly owns the question |
| `in_progress` | responsible recipient | work started |
| `pending_human` | responsible recipient | waiting on a human decision |
| `blocked` | responsible recipient | cannot proceed; stays owned |
| `completed` / `failed` | typed response only | terminal, atomically linked to the accepted `obligation_response` event |
| `canceled` | exact requester | terminal withdrawal |
| `expired` | reconciliation | terminal execution of the deadline bound at creation |

Every transition is revision-fenced, recorded in
`response_obligation_transitions`, and audited.

## Closing an obligation

Only the typed `obligation_response` conversation action closes an
obligation. It must be posted by the exact responsible recipient harness and
repeat the original binding:

```json
{
  "kind": "obligation_response",
  "obligation_id": "…",
  "request_event_id": "…",
  "request_digest": "<sha256 of the exact request payload>",
  "outcome": "completed",
  "body": "answer text",
  "structured_response": {"quantity": 4}
}
```

A wrong request event ID, wrong digest, wrong harness, missing demanded
`response_schema_id`, or an already-terminal obligation fails closed. The
response event and the terminal obligation state commit in one transaction,
so the system can never report `awaiting peer` after the answer is durable.
A duplicate idempotent retry returns the original acceptance; a second,
different terminal response conflicts.

## Reconciliation

`POST /v1/response-obligations/reconcile` (or `agentnet obligation
reconcile`) is idempotent and restart/offline-safe. For the calling verified
party it:

1. moves `created` obligations to `recipient_committed` exactly when the
   durable mailbox recipient fact already proves commitment; and
2. moves the caller's own overdue requested obligations to `expired`.

It mints no new authority and consumes no fresh policy decision; both
mutations re-execute already-authorized durable facts. Overdue visibility
never depends on reconciliation: the `overdue` inbox counter is derived at
read time.

## Visibility

- `GET /v1/response-obligations/{id}` — exact fetch with full transition
  history; requester and responsible authorities only.
- `GET /v1/response-obligations?role=&state=&limit=` — participant-scoped
  list.
- `GET /v1/response-obligations/inbox` — content-free counters:
  `unread_information` (mailbox items with no obligation for me),
  `action_required` (I must answer), `awaiting_peer` (I asked, peer owes),
  `awaiting_human` (`pending_human` either side), `overdue` (open past
  deadline), `failed` (my requests that terminally failed). `overdue`
  intentionally overlaps the ownership counters.

## Harness-local tools

The credential-free local adapter surface exposes the same journey through MCP
and direct Unix IPC. Identity comes only from the supervisor-bound harness
session; none of these tools accepts actor, principal, credential, or domain
arguments.

- `agentnet.conversation.create`, `.action`, and `.thread`
- `agentnet.obligation.inbox`, `.list`, `.get`, `.transition`, `.cancel`, and
  `.reconcile`

The strict conversation action accepts both requests carrying
`response_obligation` and typed `obligation_response` closures. Installed
harnesses therefore do not need to hand-craft signed HTTP requests.

## Authorization actions

| Action | Used by |
|---|---|
| `conversation.response_obligation.create` | requester, at request post |
| `conversation.response_obligation.respond` | responsible recipient, at typed response |
| `conversation.response_obligation.update` | responsible recipient progress transitions |
| `conversation.response_obligation.cancel` | requester cancellation |

Reads and reconciliation validate current actor state and participant scope;
they do not consume entitlements.
