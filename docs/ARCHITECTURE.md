# Implementation architecture

## Authoritative baseline

- Concept SHA-256: `d55c90e71721e7e4f9001a531b65531077c9786adffb7b361bb2500690583042`
- Requirements SHA-256: `e45d2d8fc6afcee9d1c150cfc9ceea5c9b77f07f0f076673ce6c7929614cc3e8`
- Runtime: CPython 3.13; package supports 3.13–3.14.
- Native A2A mechanism: official `a2a-sdk[http-server]==1.1.0`.
- Local binding mechanism: official `mcp==1.28.1`; Pi uses direct IPC.
- Crypto primitives: `cryptography==49.0.0`; no local primitive design.

## Deployment shape

```text
verified/OOB identity                 independent approval verifier
          |                                      |
          +---------- enrollment authority ------+
                              |
harness shim -> private IPC -> device supervisor -> signed internal API
                                  |                         |
                     encrypted local queue        policy/identity PEP
                                  |                         |
                     clean background worker      transactional core
                                                            |
                            +---------------+---------------+---------------+
                            |               |               |               |
                         mailbox   room/task/relationship state   artifact manifest  audit intent
                            |                               |               |
                    per-recipient facts          immutable quarantine   checkpoint/witness

public A2A -> isolated SDK gateway -> external-human-unverified lane -> typed core proposal
partner domain -> federation gateway -> host-local guest candidate -> exact host grant
```

The local executable composes logical roles in one process but preserves
separate interfaces and actor types. Production requires distinct credentials
and the physical/administrative topology selected under PD-010.

## Canonical state and evidence

- Canonical implementation state: this repository.
- Runtime state: SQLite only for `local_conformance`; it emits
  `accepted_local`, never `accepted_durable`.
- Production target: one PostgreSQL transaction for identity/policy/event/
  recipient/receipt/audit-intent/outbox authority plus an immutable artifact
  backend. Startup remains blocked until that backend and evidence are wired.
- Evidence ledger: `REQUIREMENTS_STATUS.md` and `docs/GATE_EVIDENCE.md`.
- Generated schemas: `schemas/v1/*.json`.

## Authority boundaries

1. Transport proof constructs the actor; payload claims are ignored.
2. Only a verified human or host-local guest is a positive authority source.
3. Harness, session, device, credential, assurance, and capability only deny.
4. A relationship proposal has zero authority. An active bilateral edge grants
   only its exact meta-actions; assignment custody is not data, tool, semantic,
   or effect permission.
5. Either relationship activation basis must have one exact completed local
   activation intent in the same transaction as evidence consumption and the
   edge compare-and-swap. That row is local provenance, not an independent
   external witness.
6. Every protected operation rechecks current policy and exact task intent.
7. A receipt states only its owner's fact. No transport/global completion leap.
8. A2A Card/task/message metadata remains external, namespaced, and tainted.
9. Routine communication has no path to the active conversation.

## Bilateral relationship lifecycle

The ordinary extension owns one relationship state machine for laptop and
always-on profiles:

```text
exact administrator-owner proposal (zero authority)
                    |
                    v
                 proposed
             /                 \
fresh exact subordinate-       separately recorded exact signed
owner approval                  domain-policy exception
             \                 /
                    v
                  active
          /          |           \
       expired    revoked      superseded by a freshly
                                consented next revision
```

The proposal transaction canonically binds the domain, both harnesses, both
current owner kinds and IDs (`human` or `guest`), both credential epochs,
relationship terms and revision, proposal/relationship expiries, current
policy and domain-revocation epochs, and predecessor state. Activation rechecks
all of them. Approval trust comes only from the configured
`IndependentApprovalVerifier`; request JSON cannot assert an actor, verifier
result, or policy decision. The consent purpose is exactly
`organization.relationship.accept`, and production configuration must
explicitly allow it for a trusted approver.

Before either the verifier-derived owner receipt or the separately recorded
policy exception is consumed, the activation transaction inserts a pending
`organization.relationship.activate` audit intent. Its canonical digest binds
the exact transaction/digest/revisions, `proposed` to `active` transition,
activation basis, transport-derived actor, consumed receipt or exception
evidence, activation time, and custody-only effect. The same transaction
consumes the evidence, compare-and-swaps the edge, and completes the exact
intent at the activation timestamp. Assignment authority validation rejects an
active edge when that intent is absent, pending, noncanonical, or inconsistent.
The row supplies durable local provenance only; it does not replace the
independently administered audit exporter/checkpoint/witness required for a
production audit claim.

Renewal is a new transaction and relationship ID at the next pair revision.
Activation atomically supersedes a still-current predecessor. Revocation,
activation, expiry, and renewal compare exact lifecycle revisions so one
committed transition wins and every stale competitor conflicts. A revocation
that commits first also advances the exact pair's lineage epoch and revokes
every then-open proposal/active row, fencing later activation or renewal. If
activation or renewal commits first, a revoke command prepared against the
older lifecycle revision is stale and conflicts. Either endpoint may submit an
exact signed revoke command; a nonparticipant path requires the distinct
`organization.relationship.admin_revoke` action. These are mechanisms only:
ORG-006 still blocks any claim that eligible proposer/proposal-entitlement,
exception-signer, override, mandatory-relationship, notice, review, retention,
or appeal policy has accountable-owner approval.

## Task-custody deadline and disclosure boundary

Automatic `may_assign` custody always binds an exact deadline. A supplied
timezone-aware deadline remains exact. If omitted, the service derives a
whole-second deadline from the normalized immutable event time and caps it at
the assignment scope's complete `max_duration` and one second before the
relationship expiry. It persists that value in the canonical request and event
as `effect_deadline`, with `delivery_expires_at` no later than it, before the
custody digest and write. Retry time can never extend the window.

The stored payload remains encrypted custody. Generic mailbox reconciliation,
conversation threads, supervisor explicit-open/background reconciliation, and
ordinary downstream delivery expose only immutable metadata/digests plus an
`agentnet.custody-payload-reference.v1` reference for task assignments and
task-linked controls. Typed task/control classification is the fallback for
records without `payload_access`, and envelope/payload digests are
validated before projection, so marker removal or substitution fails closed.
There is no protected TaskGrant payload-release route in the current build; therefore
task execution is unavailable through AgentNet while the custody-only
non-grant invariant is enforced. Ordinary non-task messages are unchanged.

## Durable response obligations

Ordinary conversations and structured requests may opt in to a first-class
`ResponseObligation` (`messaging/obligation.py`, tables
`response_obligations`/`response_obligation_transitions`). Creation happens in
the same transaction as request acceptance and binds the request event ID and
exact payload/envelope digests, the accountable requester authority and
harness, one exact responsible recipient harness and authority, the
`response_required` flag, an optional deadline, and an optional response
schema ID.

The lifecycle is `created`, `recipient_committed`, `acknowledged`,
`in_progress`, `pending_human`, `blocked`, and the terminal states
`completed`, `failed`, `canceled`, `expired`. Invariants:

- delivery custody stays authoritative in `recipients`;
  `recipient_committed` is only mirrored from the durable mailbox fact and can
  never be independently asserted;
- only a typed `obligation_response` conversation action that repeats the
  original request event ID and payload digest, posted by the exact
  responsible recipient harness, can produce `completed`/`failed`, and the
  response event and terminal state commit in one transaction — prose replies
  and unrelated events never close an obligation;
- `canceled` requires the exact accountable requester; `expired` executes only
  the deadline that was bound at authorized creation;
- reconciliation (`reconcile`) is idempotent and restart/offline-safe: it
  mirrors already-durable recipient commitments and expires the requester's
  own overdue obligations, minting no new authority;
- every transition is revision-fenced, recorded in the transition table, and
  audited;
- exact-fetch and list visibility is limited to the requester and responsible
  authorities and validates current harness state; an active sibling harness
  shares principal-level visibility but cannot claim the exact responsible
  harness's progress or response ownership; inbox counters (`unread_information`, `action_required`,
  `awaiting_peer`, `awaiting_human`, `overdue`, `failed`) are derived,
  content-free, and never mutate state.

Multi-recipient `any`/`all`/quorum satisfaction rules remain future scope: an
obligation names exactly one responsible recipient harness.

The supervisor's credential-free local binding exposes conversation creation,
strict typed conversation actions, thread reads, and obligation
inbox/list/get/transition/cancel/reconcile operations through the canonical
dispatcher shared by MCP and Pi direct Unix IPC. This keeps answer ownership
reachable from every supported harness binding without trusting model-supplied
identity fields.

The common supervisor runs obligation reconciliation alongside authoritative
mailbox cursor reconciliation after startup, reconnect, wake, and the bounded
fallback interval. It stores the content-free counter snapshot encrypted in the
local WAL queue, so attention survives a supervisor restart without injecting
message content into the foreground. Wake events still carry no authority.

## First-release storage authority boundary

AgentNet starts at storage schema version 1, which includes the
response-obligation tables. SQLite and PostgreSQL initialize that same complete
clean-start authority model atomically from the single checksum-bound baseline.
Relationship authority exists only in the
bilateral governance transaction and exact policy-exception records. Startup
requires the exact current metadata, migration catalog, tables, indexes, constraints,
and security triggers and fails closed on missing, altered, older, or newer
state.

There is no supported in-place conversion from a pre-release or differently
named database and no rule that infers consent from a unilateral edge. An
operator must export non-authority data through a reviewed tool, initialize a
fresh AgentNet v1 store, and obtain fresh exact bilateral consent. Rollback may
restore only an exact verified v1 backup and may never synthesize or reactivate
relationship authority.

## Causal and artifact provenance

Workload-created local events bind exactly one local parent event. The mailbox
resolves that parent and its immutable ledger digest inside the submission
transaction, records a derived provenance version with no invented
transformation, preserves taint, and limits sinks to the authenticated actor and
the conversation/room/recipient set already authorized by current state. A
missing parent, lower-class output, widened sink, replay mismatch, or policy
drift aborts the event transaction. A cross-domain relay retains its signed
packet/audit binding rather than pretending a remote event is a local ledger
parent.

Artifact promotion may bind a strict derivation containing complete parent
provenance references and one or more canonical transformation steps. The
service resolves current parent digests transactionally, requires each executor
to be the authenticated harness, persists the derived origin as tainted and
unreviewed, and makes replay compare the authoritative parents and exact steps.
Human-input origins can be registered over HTTP only by that exact authenticated
human harness; server-origin kinds are available only through composed internal
services.

## Conflict custody

Accepted assignments carry a typed resource/operation/access/exclusivity
intent. Incompatible live intents for one subordinate enter deterministic
resource conflicts and all affected events move to `conflict_pending` in the
same transaction. Only the exact current human/guest positive-authority owner of
the subordinate endpoint can partition every current member into compatible
release and reject sets at the exact conflict and authority revisions. Competing
decisions are compare-and-swap fenced. Rejection propagates across overlapping
conflicts; a released event queues only after no pending membership remains.
Release remains custody-only and grants no data, semantic, tool, or effect
authority.

## Backup publication and rollback uncertainty

Signed backup manifests, trust records, seals, and archives bind the exact
schema, domain, source fingerprint, key epoch, and bytes. Publication uses
descriptor-relative staging, rename, directory durability, and inode/digest
checks. If cleanup cannot prove that it still owns the installed pathname, it
atomically removes the product-visible name into a random owner-only
`.agentnet-quarantine-*` file and deliberately retains those exact bytes for
authenticated operator inspection and out-of-band removal. Post-commit close or
durability uncertainty is reported as an unknown publication outcome, never as
successful rollback.

## Workforce OIDC endpoint transport

Production OIDC requests use one resolver snapshot per HTTP request. The default
resolver is the host system facility (`getaddrinfo`, including its NSS/hosts/DNS
policy), not an AgentNet trust source. AgentNet validates every returned address
against the configured public-only default or explicit private address/CIDR
pins, then the transport connects directly to an address from that exact
snapshot. Production operators should use exact address pins when DNS alone is
not an acceptable routing dependency. TLS certificate verification and SNI, plus
the HTTP Host authority, continue to use the exact configured URL hostname.
The transport does not consult environment proxies, does not follow redirects,
and does not resolve the hostname a second time.

Private/non-global provider addresses require explicit canonical HTTPS origins
and exact JWK thumbprint pins in addition to canonical address or private-CIDR
pins. Exact address pins, when present, constrain every resolved address.
Unspecified, loopback, link-local, multicast, reserved, documentation,
benchmark, transition/softwire, and IPv4-mapped addresses remain forbidden even
when configured as pins. Hostname allowlists, hosts-file or proxy workarounds, HTTP downgrade, and a
blanket private-network switch are not supported. These mechanics close the
local DNS-rebinding/SSRF connection gap; they do not satisfy the real workforce
IdP, independent WebAuthn/OOB, target-device custody, or owner-decision tiers.

## Component seams

Owned semantics sit in strict models and protocols. Implementations are
replaceable behind `interfaces/contracts.py`, `mailbox/custodian.py`,
`rooms/mls.py`, `authorization/cedar.py`, and `mesh/interfaces.py`. Replacing a
component must not migrate principal IDs or rewrite signed/event history.

## Local profile versus production profile

| Concern | Local conformance | Production gate |
|---|---|---|
| Identity | synthetic or lab OOB, visibly labeled | workforce OIDC, phishing-resistant WebAuthn, independent boundary |
| Binding | P-256 proof plus lab process binding | target OS inherited anonymous capability / LSM / measurement evidence |
| Store | encrypted SQLite WAL, `accepted_local` | HA PostgreSQL, RPO=0 claim, fencing/PITR/restore |
| Artifacts | encrypted immutable filesystem | selected replicated self-hosted backend, scanner/WORM/restore |
| Policy | reference single-revision evaluator | pinned Cedar or measured single-engine replacement |
| A2A | SDK-backed local routes/mapping | pinned TCK, negative suite, cross-SDK/public peers |
| Workers | deterministic-only unless exact sandbox evidence | per-harness clean worker and credential-free model broker |
| Federation/C3/mesh | schemas and disabled seams | bilateral/MLS/quorum evidence plus owner decisions |
