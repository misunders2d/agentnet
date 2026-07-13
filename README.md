# AgentNet

AgentNet is a self-hosted, agent-agnostic communication extension for Claude,
Codex, Pi, Antigravity, and other harnesses. It provides signed corporate
communication, durable offline delivery, rooms, task delegation, file sharing,
federation, and native A2A interoperability without requiring a separate Hub
product or external cloud dependency.

## Start here

All implementation work must preserve the hard requirements and security
boundaries documented here:

- [Hard requirements](docs/requirements.md)
- [Architecture and product specification](docs/specification.md)
- [Final concept verification](docs/final-verification.md)
- [Implementation architecture](docs/ARCHITECTURE.md)
- [Evidence ledger](REQUIREMENTS_STATUS.md)

The requirements document is the authoritative implementation checklist.
Changes must not weaken signed caller identity, fail-closed authorization,
human-bound enrollment, harness attribution, non-interrupting background
operation, durable offline delivery, directional administration, or federated
trust-domain isolation.

## Bilateral relationship governance

Management relationships are created by the ordinary extension through a
bilateral, revision-fenced lifecycle. `POST /v1/relationships` creates only a
zero-authority proposal. An edge becomes active only after either
`POST /v1/relationships/{relationship_id}/accept` verifies a fresh,
purpose-bound approval from the current human or guest owner of the subordinate
harness, or an exact signed domain-policy exception is separately recorded and
consumed. Either basis must also create and complete an exact local activation
audit intent in the same transaction as proof consumption and edge activation;
an active row without matching completed provenance has no assignment
authority. This is durable local evidence, not an independently administered
audit witness. Renewal uses a new relationship ID, the next coherent
relationship revision, and fresh consent; expiry, revocation, subject exit,
supersession, and concurrent lifecycle changes are audited and fenced by exact
revisions.

An active `may_assign` edge authorizes only scoped automatic task custody
(`accepted_queued`). It never grants protected-data access, semantic-processing
permission, tool permission, or business-effect authority. An omitted task
deadline is derived by the server at whole-second precision, capped by the
scope's full `max_duration` and strictly before relationship expiry, then bound
into the request and immutable event. Generic mailbox, conversation, explicit
open, and supervisor/background reads never reveal task-assignment or
task-linked-control payloads, including legacy events without the marker; they
return only metadata, digests, and a custody reference. There is no protected
TaskGrant payload-release route in this patch, so task execution remains
unavailable while this non-grant boundary is enforced. Ordinary messages are
unaffected. The clean first-release storage schema contains only bilateral
governance tables; it has no unilateral relationship table or conversion path.
Prototype databases are not accepted as release databases and cannot be
silently imported. Operators initialize a new AgentNet network and obtain fresh
exact consent for every relationship.

The mechanism is implemented, but **ORG-006 remains owner-blocked**. No policy
for eligible proposers or proposal entitlements, exception signers,
security/legal override, mandatory relationships, notice, review, retention,
or appeal is represented here as approved or production-evidenced;
accountable owners must still approve who receives those exact policy
entitlements and under what rules.

## Repository layout

```text
docs/                              product specification and hard requirements
src/                               AgentNet implementation
tests/                             conformance, integration, and security tests
schemas/                           versioned protocol schemas
deploy/                            self-hosted deployment assets
```

## Current status

The retained local implementation suite result is superseded by the current
evidence run recorded in `REQUIREMENTS_STATUS.md`. All 85 rows have executable
local mechanisms where this host can provide them. Incompatible typed task
intents from multiple authorized administrators atomically place every member
in `conflict_pending`; only the subordinate endpoint's current positive-
authority owner may partition the exact versioned member set into released and
rejected tasks. Release remains custody-only and grants no data, semantic,
tool, or effect authority. The project deliberately does not claim production
certification: official A2A, external infrastructure, partner,
privileged-host, and accountable-owner gates remain blocked or incomplete.
