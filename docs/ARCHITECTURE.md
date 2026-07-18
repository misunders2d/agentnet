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

## Host-local binding profile

The npm launcher and local conformance profile run on Linux, macOS, and Windows
without treating installation as enrollment or authority. Linux binds Unix
peers with `SO_PEERCRED`, hashes the live `/proc/<pid>/exe` object, delivers Pi
capabilities through sealed memfd state, and owns child process groups. macOS
uses Unix `LOCAL_PEERPID` plus `getpeereid`, a caller-closed inherited read-only
pipe, and a separate process group. Windows uses protected current-user DACLs,
`PIPE_REJECT_REMOTE_CLIENTS`, `GetNamedPipeClientProcessId`, one-time exact-
process capability pipes, and `CREATE_SUSPENDED` admission to a kill-on-close
Job Object before child execution resumes.

Every host binds platform, account UID/SID, PID, repeated creation time,
executable digest, parent identity where required, session, generation, expiry,
and replay nonce. Payload fields never establish those facts. Windows npm
runtime roots and private state reject reparse points and broad allow ACEs.
macOS/Windows executable hashing is path-based with before/after identity checks;
it remains a documented lower-assurance boundary than Linux's live executable
handle until privileged host trials establish a stronger mechanism. Real-host
CI proves only the named package/local contracts, not production deployment or
semantic clean-worker qualification.

An always-on process has a separate deployment-identity binding step. After
real enrollment, `server-agent activate` holds the exact runtime lease under a
distinct activation owner, verifies the current credential and private key
against the same PostgreSQL authority, and atomically adds only the exact
harness/credential labels to the offline config. Startup does not follow a
retired label to a successor credential. These labels and server capability
limits can only narrow process eligibility; protected operations still derive
and authorize their exact caller independently.

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
Protected release is intentionally separate from every generic projection.
The signed recipient supervisor first authorizes the exact task with one current
`task.process` grant use, durably records the exact local queue custody, then
calls `/v1/supervisor/executions/payload-release`. The release transaction
rechecks current actor/grant/policy/credential/domain state, immutable
cursor/envelope/payload bindings, deadlines/retention, active conflict-free
intent, and provenance. It commits one `task_payload_releases` receipt and audit
record before returning validated plaintext. Exact retries recheck current
state and reuse the receipt without another grant use. Result upload requires
that committed receipt. Generic mailbox, conversation, relay, and supervisor
reconciliation remain redacted; release grants no tool or effect authority.
Ordinary non-task messages are unchanged.

## Authenticated mailbox acknowledgement

Signed HTTP, CLI, MCP, direct IPC, and Pi adapter paths share one canonical
mailbox acknowledgement operation. The request names only an event and its
immutable envelope digest. The verified transport/binding supplies the exact
current recipient harness; caller-provided actor, recipient, credential,
domain, or fact fields are rejected.

The operation writes `recipient_committed`, the specification's baseline
delivery acknowledgement proving recipient custody and dedup evidence. It does
not write a new delivery `acknowledged` fact: that name belongs to the separate
response-obligation lifecycle. It also cannot assert `presented`, `processing`,
completion, or any business effect. Recipient state, one recipient-owned
receipt, and one transition audit record commit atomically. Exact retries return
the original receipt without another transition write or state downgrade, even
when the current fact has advanced. Existing protected semantic-supervisor
custody converges on that same delivery receipt instead of minting a second
`recipient_committed` fact. Current credential/revocation/policy checks still
run on every retry. A revoked credential cannot retrieve even its previously
recorded receipt; the receipt remains in the protected audit history and a
current authorized actor must inspect it. Event IDs on this path are bounded to
a single canonical ASCII route segment.

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

## Versioned storage authority boundary

Immutable schema migration 1 is the complete first-release authority model,
including response obligations and bilateral governance. Migration 2 adds only
protected task-payload disclosure receipts. Unreleased migration 3 adds one
`oidc_enrollment_continuations` table for hash-only guided tokens, encrypted
challenge/completion state, approval request binding, bounded polling, and
response-loss idempotency. Fresh SQLite creates v3; only exact
catalog/checksum-verified v2 stores may upgrade to v3 under the N/N-1 window.
PostgreSQL applies the same contiguous checksum-bound catalog. The independent
approval host has its own exact SQLite catalog and atomic v1→v2 migration for
Core request idempotency, encrypted local capabilities, claim codes, and
migration evidence; it is not Core authority storage. Relationship authority
still exists only in bilateral governance transactions and exact
policy-exception records. Startup requires exact current metadata, migration
rows/checksums, tables, indexes, constraints, and security triggers and fails
closed on missing, altered, prototype, noncontiguous, future, or unsupported
older state.

There is no conversion from a pre-release/differently named database and no
rule inferring consent from a unilateral edge. Operator exports only reviewed
non-authority data, initializes a fresh current store, and obtains fresh exact
bilateral consent. Rollback may restore only an exact signed, verified,
compatible backup and may never downgrade metadata or synthesize/reactivate
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

The operator binary path is a thin client over this same lifecycle, not another
artifact authority. One stable caller-owned descriptor supplies at most 16 MiB;
the exact bytes are signed as the HTTP body and promoted only into quarantine.
Scanner attestation and release remain separately authorized server roles.
Downloads mint and consume an exact-harness single-use capability internally,
then create a new private output without replacement. Normal output contains
only artifact identity, size, digest, lifecycle/provenance metadata, and the
operator-selected destination—not private object keys or bearer capabilities.
Model-visible local tools do not accept bytes/base64 or arbitrary host paths;
opaque supervisor staging remains a separate future boundary.

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

Token-endpoint client authentication is explicit. Public configuration carries
only `token_endpoint_auth_method` (`none`, `client_secret_post`, or
`client_secret_basic`) and, for confidential clients, the name of a runtime
environment variable in `client_secret_env`. Runtime composition resolves the
bounded value without adding it to serialized configuration, repr output,
audit, or evidence. Confidential methods must be advertised by discovery and
are never inferred from secret presence or provider ordering. Public clients
remain `none` by default. The selected method, but no secret or secret
reference, is included in the internal-invitation verifier identity so a method
change fails closed for in-flight authorizations.

Private/non-global provider addresses require explicit canonical HTTPS origins
and exact JWK thumbprint pins in addition to canonical address or private-CIDR
pins. Exact address pins, when present, constrain every resolved address.
Unspecified, loopback, link-local, multicast, reserved, documentation,
benchmark, transition/softwire, and IPv4-mapped addresses remain forbidden even
when configured as pins. Hostname allowlists, hosts-file or proxy workarounds, HTTP downgrade, and a
blanket private-network switch are not supported. These mechanics close the
local DNS-rebinding/SSRF connection gap; they do not satisfy the real workforce
IdP, independent WebAuthn/OOB, target-device custody, or owner-decision tiers.

## Independent WebAuthn-UV approval service

`agentnet approval` is a separately runnable component from the same AgentNet
package, not authority inside an enrolled agent. Its configuration, SQLite
database, AES-256-GCM record key, approval receipt signer keys, HTTPS origin,
WebAuthn RP ID, OS account, and administration must be outside every enrolling
or enrolled harness's control. The service binds only an explicit loopback IP;
an independently administered TLS proxy exposes the exact configured HTTPS
origin. Running it beside an agent under the same readable/control boundary is
local mechanism testing only and cannot satisfy independence.

Host-admin CLI creates one-time registration or exact-transaction requests.
Capabilities are 32 random bytes with an `agcap1.` prefix; only SHA-256 hashes
are stored, and browser URLs carry the capability in the fragment so it is not
sent in the initial HTTP request. Browser JavaScript immediately removes the
fragment, fetches bounded strict-JSON options, displays exact canonical
transaction text, purpose, domain, digest, and expiry, then requires an explicit
button action and WebAuthn user verification.

Duo Labs `webauthn==3.0.0` verifies challenge, exact origin, exact RP ID,
credential identity, signature, user verification, and sign-count progression.
AgentNet retains purpose, domain, approver, transaction, receipt, audit, and
lifecycle semantics. Challenge and canonical transaction custody is encrypted;
unknown/modified SQLite catalog, non-owner-only files, stale/replayed
capabilities, revoked credentials, expired challenges/requests/receipts, and
missing configured purpose all fail closed. One receipt and audit record commit
before response; response-loss retries return the exact encrypted stored receipt
without re-signing or extending expiry. Approval SQLite schema v2 adds exact
v1→v2 catalog-verified migration, encrypted local capability custody,
idempotent Core request bindings, and hashed bounded claim-code state.

Core services remain receipt-only consumers through
`IndependentApprovalVerifier`. An optional, disabled-by-default broker surface
lets a configured Core create/status exact requests and retrieve an already
issued receipt over runtime-credential-authenticated HTTPS. It cannot approve,
sign, or bypass WebAuthn. Core-created browser capabilities remain encrypted on
the approval host; `agentnet approval pending|watch|open` discovers and opens
them locally without printing or transporting the URL. After WebAuthn, browser
shows a 128-bit human-transferred claim code instead of receipt JSON. Exact
code/domain/purpose/transaction/retrieval binding returns the same current
receipt for response-loss retry; Core remains responsible for atomic receipt
consumption. Six purposes are mandatory in configuration:
`identity.enrollment.approve`,
`authorization.entitlement.bootstrap.approve`,
`authorization.elevation.approve`,
`identity.credential.recover.approve`,
`identity.harness.revoke.approve`, and
`organization.relationship.accept`. Optional purposes require explicit
configuration. Local SQLite and mocked ceremony vectors provide H evidence
only; real passkeys, independent host/device custody, TLS, key rotation and
recovery drills, and PD-002/004/005/009 remain external/owner gates.

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
