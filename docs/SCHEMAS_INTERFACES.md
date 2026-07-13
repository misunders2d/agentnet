# Schemas and interfaces

## Canonical schemas

`scripts/export_schemas.py` exports strict JSON Schemas to `schemas/v1` for:

- actor and identity binding;
- enrollment transaction;
- immutable event and actor-owned receipt;
- bilateral relationship consent transaction, governance record, exact policy
  exception, and exact task grant;
- room, presence, artifact manifest, and revocation;
- federation invitation, audit intent, and protocol error.

All trust-boundary Pydantic models set `extra="forbid"`. Unknown major versions,
critical fields, actor variants, security algorithms, and state transitions
deny rather than downgrade. Event IDs, digests, authority epochs, delivery
expiry, effect deadline, retention deletion, and legal hold are separate.

## Owned interfaces

| Interface | Implementations/current state | Corporate invariant retained |
|---|---|---|
| `PolicyDecisionPoint` | local reference evaluator; Cedar adapter fail-closed | one positive authority and revision |
| `MailboxCustodian` | SQLite local; PostgreSQL target; mesh future | actor-owned custody receipts and exact digest |
| `ArtifactStore` | encrypted filesystem local; provider-neutral production | manifest/quarantine/authorization stays authoritative |
| `ApprovalVerifier` | signed lab verifier; independent provider required | exact transaction and independent current human/guest-owner consent |
| `WorkloadIdentityProvider` | verified SPIFFE/mTLS context seam | workload never becomes a human |
| `WorkflowEngine` | explicit transactional effects; optional Temporal-style | workflow success never fabricates effect evidence |
| `MLSProvider` | unavailable until maintained stack passes | room policy/membership and visible key holders remain explicit |
| MCP/direct IPC | official MCP or private Unix framing | arguments cannot establish caller identity |
| A2A SDK routes | official SDK 1.1.0 | public identity remains external-low-trust |

## Relationship wire and storage contract

Authenticated product routes use strict Pydantic bodies (`extra="forbid"`) and
derive the actor only from the existing transport proof. Relationship responses
and errors are non-cacheable; ordinary reads are participant-scoped and
non-enumerating.

| Operation | Exact body/evidence | Authority effect |
|---|---|---|
| `POST /v1/relationships` | `relationship`, `proposal_expires_at` | Creates `proposed`; zero authority |
| `POST /v1/relationships/{relationship_id}/accept` | strict independent approval receipt plus expected transaction digest, relationship revision, and lifecycle revision | Activates only after verifier-derived current subordinate human/guest-owner consent |
| `POST /v1/relationships/{relationship_id}/policy-exceptions` | exact exception plus signed authority command | Records one bounded exception; does not activate by itself |
| `POST /v1/relationships/{relationship_id}/policy-exceptions/activate` | exception ID plus expected digest and revisions | Atomically consumes the exact current exception and activates |
| `GET /v1/relationships/{relationship_id}` | authenticated participant read; separately entitled administrative read when requested | Read only; unauthorized/missing is non-disclosing |
| `POST /v1/relationships/{relationship_id}/revoke` | exact signed command at current lifecycle revision | Endpoint exit/revoke, or separately entitled admin-revoke mechanism |

The canonical consent transaction covers both endpoint owner kinds/IDs and
credential epochs, relationship bytes and revision, proposal and authority
expiry, policy/domain epochs, and predecessor lifecycle. Stored activation
evidence distinguishes `subordinate_owner_consent` from
`domain_policy_exception`. Only an active, current, unrevoked, unexpired edge
may supply scoped assignment custody; no relationship schema field grants data,
semantic, tool, or effect authority.

For either activation basis, authority also requires one exact completed local
`organization.relationship.activate` audit intent. Its request digest binds the
canonical relationship transaction/digest/revisions, lifecycle transition,
activation basis, transport-derived actor, exact receipt or exception evidence,
activation timestamp, and `custody_only` effect. It is inserted pending before
evidence consumption and completed in the same transaction as the active-edge
compare-and-swap. Missing, pending, noncanonical, or inconsistent intent bytes
invalidate the stored edge. This is durable local provenance only; the
`audit_intents` row is not an independently administered witness and cannot
close the external audit-root gates.

## Task-custody wire and disclosure contract

An automatically accepted assignment has one exact timezone-aware deadline. If
the request omits it, the service derives a whole-second value from the
normalized immutable event time, capped at the exact scope's complete
`max_duration` and strictly before relationship expiry. Before digesting and
persisting custody, it writes the value into the assignment request and event
`effect_deadline`, caps `delivery_expires_at` at the same value, and stamps
`payload_access: task_grant_required` on extension-owned task custody.

The marker is not an unlock condition. Generic mailbox and conversation
projections validate canonical envelope/payload bytes and both immutable
digests, then withhold payload for a marked event, any typed
`task_assignment`, or any task-linked `control`. The typed fallback therefore
also protects records with no marker. The returned projection contains
`payload: null`, `payload_available: false`, the access reason, and an
`agentnet.custody-payload-reference.v1` object binding event, payload, and
envelope digests. Supervisor/background consumers receive that same projection
and cannot promote it to work. The current build defines no protected TaskGrant
payload-release route, so task execution remains unavailable. Ordinary
non-task message projections retain their existing authorized payload shape.

## Schema evolution

The handshake selects the highest mutually allowed protocol/schema profile.
The supported window is N/N-1 only after explicit migration tests. Expansions
precede backfill/verification and contraction. Revocation/security state never
rolls back. Unsupported events remain queued or receive a typed rejection;
intermediaries never strip unknown signed fields.

AgentNet's first release uses storage schema version 1. SQLite initializes the
complete v1 schema; PostgreSQL has one contiguous checksum-bound v1 migration.
The relationship governance and policy-exception tables are part of that first
schema, not a retrofit. Startup fails closed on a missing or altered migration,
table, index, trigger/constraint, or unsupported version.

No pre-release or differently named database is accepted as an authority
source, and no unilateral relationship can be converted into consent. Import
requires a reviewed non-authority export into a fresh v1 store followed by fresh
exact bilateral approval. Rollback restores only an exact verified v1 backup;
it cannot infer, preserve, or reactivate authority from unsupported bytes.

## Provenance and conflict interfaces

`POST /v1/provenance/origins` accepts only a human-input origin matching the
exact authenticated human harness. `POST /v1/provenance/derivations` binds every
transformation executor to the authenticated harness and resolves all exact
parent versions/digests from the authoritative ledger. Versioned GET routes are
policy checked. Composed mailbox and artifact services create their own derived
records transactionally; caller-supplied server origins are forbidden.

Assignments accept an optional strict `TaskExecutionIntent`. If incompatible
live intents overlap, `GET /v1/task-conflicts` returns only conflicts belonging
to the authenticated current subordinate owner. The adjudication route requires
an exact complete member partition, current conflict/policy/domain/credential
revisions, and pairwise-compatible releases. Replays, stale decisions, wrong
owners, widened releases, and competing revisions fail closed. Rejects propagate
through overlapping conflicts and release queues an event only when every
pending conflict membership has cleared.

ORG-006 is still an accountable-owner decision. These schemas prove exact
mechanics, not approval of eligible proposers or proposal-entitlement holders,
policy-exception signers, or administrative-override authorities, nor any
mandatory-relationship, notice, review, retention, or appeal policy, nor
production evidence.
