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
verified/OOB identity                 owner-controlled WebAuthn approval
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
separate interfaces and actor types. The ordinary self-hosted deployment may
colocate Core, PostgreSQL, and approval on the existing server under distinct
OS identities, credentials, storage roots, and loopback services. Human
confirmation remains independent of the enrolling harness through the owner's
WebAuthn authenticator. This profile reports
`independent_boundary_proven=false`. Separate physical/administrative approval
hosting is the optional high-assurance topology governed by PD-002.

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
handle until privileged host trials establish a stronger mechanism. For MCP
bootstrap discovery, the runtime pins each accepted owner-only Unix socket with
a non-inheritable platform path descriptor before publishing its locator. The
open reference prevents an unlinked socket inode from being immediately reused;
renewal compares the live pathname against that pinned object, swaps the pin
only after successful re-registration, and closes it on restart, stop,
post-publication startup failure, or terminal restart exhaustion. Retryable
renewal keeps the old pin; terminal failure removes the locator, stops the retry
loop, and records a fixed content-free failure code. A host without a stable
path-descriptor primitive fails the MCP binding closed.
Real-host CI proves only the named package/local contracts, not production
deployment or semantic clean-worker qualification.

The packaged local-communication gate composes a narrower signed lab lane. It
creates exact `binding_assurance=lab`, `deterministic_only` harnesses through the
existing local-conformance bootstrap, then runs Core and every client as separate
OS processes from an unrelated npm installation over real loopback HTTP. Only
`LocalConformancePolicyEngine` may resolve a policy revision for those harnesses,
and only the existing inert C0 allowlist may authorize them. Recipient resolution
and exact mailbox acknowledgement accept `deterministic_only` only when the
persisted verified-human harness is also lab-bound. No code promotes it to
`active`; the production `PolicyEngine` continues to reject it. The gate proves
`accepted_local`, exact proof-derived attribution, request/receipt idempotency,
`recipient_committed`, typed obligation completion, restart recovery, and fresh
authentication refusal after a clearly labeled local credential fixture. It does
not prove enrollment, bounded C0 pilot completion, approved revocation,
five-power cleanup, ordinary server-agent topology, or production durability.

An always-on process has a separate deployment-identity binding step. After
real enrollment, `server-agent activate` holds the exact runtime lease under a
distinct activation owner, verifies the current credential and private key
against the same PostgreSQL authority, and atomically adds only the exact
harness/credential labels to the offline config. Startup does not follow a
retired label to a successor credential. Always-on credentials remain finite:
24-hour issuance with a six-hour renewal window. The hourly managed timer calls
a selector-free signed current-binding renewal route; exact request replay
returns the persisted result, compare-and-swap prevents expiry races, and an
expired/revoked/rotated binding cannot renew or satisfy readiness. These labels
and server capability limits can only narrow process eligibility; protected
operations still derive and authorize their exact caller independently.

Published 0.1.35 adds one narrower recovery for the released Hub condition: an
expired, still-possessed managed-server key in the pre-C0 communication-only
topology. The root-only command freezes the exact expired binding and managed
config/identity digests, proves possession with that same key, and requires a
fresh owner WebAuthn-UV Approval receipt before one PostgreSQL transaction
retires the expired row unchanged and creates a finite next-epoch credential.
It grants no authority and performs no service restart. Service-owned config
and identity files are then updated by inode-checked, mode/owner-preserving CAS;
an interrupted update is idempotently reconciled before setup is rerun. A2A,
relay, or retained C0-terminal bindings are refused rather than silently
rewritten or excluded from marker evidence.

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

Only a typed task assignment may wake a semantic background worker. The
corporate service issues no `task.process` execution grant for an ordinary
request obligation and accepts no worker result without a committed task
payload release, so waking a model worker for one would spend tokens on work
the service must refuse. An ordinary request obligation therefore stays
durable and passively counted, and its answer is produced from the responsible
harness's own authorized session through the canonical conversation
`obligation_response` action. Both the wake gate and the corporate
`/v1/supervisor/executions/authorize` route enforce this boundary
independently.

## Persistent same-principal communication activation

After the ordinary server harness is enrolled, it may request one independent
Approval transaction for permanent communication with the owner's existing
harness. Core resolves both current harnesses and credentials server-side,
binds their common principal/domain, and presents one exact 38-item
transaction: 19 communication actions for each exact harness. The Approval
request itself expires after one hour and its possession-bound receipt remains
short-lived; successful completion stores `authority_expires_at=NULL`. This is
not elevation: every policy decision still starts from the human principal,
then the committed scope narrows use to the two named caller harnesses. Each
caller may communicate only with the other active harness named by that scope,
subject to current room/conversation membership.

The scope covers signed direct messages, mailbox read/acknowledgement,
conversation/thread/task/handoff/response-obligation lifecycle operations, and
room create/action/read. It deliberately excludes artifacts, tools, effects,
federation, public A2A, administration, and entitlement mutation. Its 38
entitlements and scope audit record commit atomically. Policy denies a missing,
revoked, stale-policy, cross-principal caller, sibling-caller, cross-domain
target, or unknown/inactive/revoked target. It does not confine communication
to the two same-principal scope harnesses; they are the authorized callers, not
the only permissible network recipients.
Normal current-credential renewal preserves the scope because authority is
harness-bound; harness/principal/domain revocation stops it immediately.

`agentnet manager-run` is the laptop-side interactive composition. Before
identity loading it accepts only a Pi command and rejects caller extension/tool
selection overrides. It copies the exact packaged AgentNet extension into the
private session, disables extension discovery, launches one Pi child without
signing keys, exposes only the canonical AgentNet tool surface through a private
per-process Unix socket, and derives every remote signed request from the
Manager's authenticated actor. A short-lived inherited
capability binds the exact child PID/process measurement, credential epoch,
method set, session, replay store, and socket. The first-release gateway is
Linux-only and requires a trusted Bubblewrap filesystem sandbox. It uses a
sealed anonymous `memfd` when the Python runtime exposes it and an inherited
one-way pipe otherwise; macOS and Windows fail closed until equivalent protected
local process and filesystem containment is implemented. The PID namespace's
trusted init contains and reaps the entire child process tree; when the measured
child exits or is terminated, namespace teardown kills every surviving
descendant before private session state is removed. The child receives no
reusable AgentNet credential, and session state is removed after normal exit,
signal termination, or launch failure.

## Exact endpoint lifecycle and routing

Schema v7 gives each local installation/profile one durable endpoint row keyed
by exact domain and harness. The public locator
`agentnet:<domain>:<harness-kind>:<profile-key>` resolves through that row to
one verified human principal, exact enrolled harness, current credential,
adapter generation, mailbox cursor, and optional measured process/capability
root. The locator and display labels are selectors only; authenticated proof
and current durable bindings construct identity.

`EndpointLifecycleService` owns
`ready_to_connect→waiting_for_approval→enrolled→access_ready→restart_required→connected`,
with `blocked` as the fail-closed narrowing state. Existing verified enrollment
registers directly as `access_ready`; activation moves it to
`restart_required`. Every mutation is revision-fenced. `reconcile` may keep a
current state or narrow stale/revoked/mismatched authority to `blocked`; it
cannot create positive identity, enrollment, scope, or authorization. A blocked
endpoint does not recover by inference. A fresh approved flow must re-establish
the exact current facts.

AgentNet never restarts an active harness. `connected` is recorded only after
the user restarts and a new process measurement proves the expected generation
for the same exact actor and endpoint. The durable harness identity and mailbox
cursor survive process and conversation restart; a new or ambiguous profile
does not inherit them.

Friendly recipient resolution is authenticated and non-enumerating. It searches
only targets visible under the sender's current domain, exact harness
eligibility, policy, and communication/collaboration scope. Success returns one
`ResolvedEndpoint` with exact harness, safe display metadata, and current
scope ID; zero, multiple, stale, revoked, cross-domain, or unauthorized matches
share one generic denial. The dispatcher freezes and re-requires that scope
against the exact recipients and classification. Explicit harness IDs must
infer exactly one current scope. Core requires the frozen scope again on the
signed request, while actor and receipt recipient attribution remain
proof-derived rather than payload-selected. Offline custody stays attached to
the original harness; no sibling, wildcard, or last-active endpoint is selected.

## Versioned storage authority boundary

Immutable Core schema migration 1 is the complete first-release authority
model, including response obligations and bilateral governance. Migration 2
adds protected task-payload disclosure receipts. Migration 3 adds
`oidc_enrollment_continuations` for hash-only guided tokens, encrypted
challenge/completion state, approval-request binding, bounded polling, and
response-loss idempotency. Migration 4 adds the bounded same-principal C0
bootstrap-plan, exact ten-item plan/guard mapping, pilot-attempt, and seven-fact
evidence tables. Migration 5 adds recoverable OIDC-begin idempotency and exact
finite current-credential renewal request custody. Migration 6 adds the durable
persistent communication scope, private administration state, and exact-harness
entitlement mapping. Migration 7 adds the endpoint lifecycle,
collaboration-scope/member, artifact-transfer/recipient, and invitation-link
catalogs. Fresh SQLite and PostgreSQL stores create v7; the tested N/N-1 Core
path accepts only an exact catalog/checksum-verified v6 store for atomic
v6→v7 upgrade. PostgreSQL verifies the complete live v6/v7 table, column type,
nullability/default, constraint-definition, and non-constraint-index catalog in
addition to contiguous migration checksums; any mismatch fails before use.

Approval owns a separate exact SQLite catalog and is not Core authority
storage. Its v2 migration adds guided handoff custody, v3 adds signed-broker
replay custody, and v4 adds stable owner OIDC sessions plus isolated WebAuthn
registration/approval ceremonies. Exact verified v1, v2, or v3 Approval stores
apply every missing migration atomically to v4; a failure rolls back the whole
upgrade. In the default topology Approval runs under a distinct OS identity on
the existing server; optional high-assurance deployments place it under
separate administration. Relationship authority still exists only in bilateral
governance transactions and exact policy-exception records. Core and Approval
startup require exact current metadata, migration rows/checksums, tables,
indexes, constraints, and security triggers and fail closed on missing,
altered, prototype, noncontiguous, future, or unsupported older state.

There is no conversion from a pre-release/differently named database and no
rule inferring consent from a unilateral edge. Operator imports use only
reviewed non-authority data in a fresh current store followed by fresh exact
bilateral consent. A committed current store is never downgraded or used to
synthesize/reactivate authority. The sole `0.1.44→0.1.45` server transition may
restore schema v6 only as an in-flight rollback before target-marker commit,
under its exact source journal and only while every journal-selected protected
relation digest and candidate artifact is unchanged. A process interruption
retains that journal for exact resume; once v7 target state commits, the
rollback path is closed.

## Causal and artifact provenance

Workload-created local events bind exactly one local parent event. The mailbox
resolves that parent and its immutable ledger digest inside the submission
transaction, records a derived provenance version with no invented
transformation, preserves taint, and limits sinks to the authenticated actor and
the conversation/room/recipient set already authorized by current state. A
missing parent, lower-class output, widened sink, replay mismatch, or policy
drift aborts the event transaction. A cross-domain relay retains its signed v2
packet and source-event digest as audit/provenance facts rather than pretending
a remote event is a local ledger parent. The source host signs the required
opaque `target_collaboration_scope_id` into
`agentnet.server-relay.packet.v2`; packet v1 is unsupported and cannot be
silently upgraded.

The destination derives one deterministic local event ID from the packet ID,
resolves the transport-bound host-local guest, and uses the mailbox's exact
`CollaborationScopeService` to require the signed target scope against the
current target-domain policy/revocation epoch, exact recipient, classification,
action, and resource. Messages require `message.send` on the exact source
conversation or `conversation:direct`; tasks require `task.propose` on the
derived `task:<local-event-id>`, and automatic task custody separately requires
`task.accept`. The existing exact target grant/business-policy check remains a
second mandatory boundary.

Remote `authorization_context` is never target authority. Relay reconstruction
removes it, inserts only the target scope's immutable authorization context,
and recomputes the local payload and envelope digests. Both stores retain the
same canonical signed packet bytes, so replay returns only the already accepted
custody fact; byte drift conflicts and consumes neither scope nor grant.

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

Ordinary self-hosted onboarding uses one stable `/approval` origin. The owner
starts OIDC Authorization Code + PKCE from that page, returns through one
preauth cookie that is Secure, HttpOnly, `__Host-`, and `SameSite=Lax`, and
rotates into authenticated owner-session and CSRF cookies that remain
`SameSite=Strict`. Callback parsing retains decoded query pairs until every
name, including extension names, is proven unique. It strictly projects either
`code`+`state` success or `error`+`state` failure, rejects mixed/orphan shapes,
and ignores only unique unrecognized OAuth response extensions. A bound provider
error terminally fails the exact pending transaction without token exchange.
Approval atomically claims then consumes a successful callback, permanently
pins the preapproved issuer+subject or verified alias, and binds
every session to the exact RP ID, public origin, verifier ID, owner principal,
and domain. The owner registers a passkey and reviews requests without a secret
URL. Stable request selection resolves encrypted capabilities only inside
Approval and returns a purpose-specific bounded summary; the browser receives
neither capability nor receipt.

Profiles without owner OIDC preserve the legacy host-admin flow and are
lab-only by policy; they cannot satisfy the ordinary C0 deployment/release gate.
Capabilities are 32 random bytes with an `agcap1.` prefix; only SHA-256 hashes are stored, and lab
browser URLs carry the capability in the fragment. JavaScript immediately
removes the fragment before using the lab-only registration/request routes.
Stable profiles do not mount those routes or serve that JavaScript.

Duo Labs `webauthn==3.0.0` verifies challenge, exact origin, exact RP ID,
credential identity, signature, user verification, and sign-count progression.
AgentNet retains purpose, domain, approver, transaction, receipt, audit, and
lifecycle semantics. Challenge and canonical transaction custody is encrypted;
unknown/modified SQLite catalog, non-owner-only files, stale/replayed
capabilities, revoked credentials, expired challenges/requests/receipts, and
missing configured purpose all fail closed. One receipt and audit record commit
before response; response-loss retries return the exact encrypted stored receipt
without re-signing or extending expiry. Approval SQLite schema v4 preserves
exact catalog/checksum-verified upgrades from v1, v2, or v3. Version 2 adds
encrypted local capability custody, idempotent Core request bindings, and hashed
bounded claim-code state. Version 3 adds persistent one-use Core→Approval
broker-proof replay custody keyed by derived key identity and a SHA-256 nonce
hash; raw broker nonces and runtime credentials are not stored. Version 4 adds
pinned owner OIDC identity, callback transactions, RP/origin/verifier-bound
browser sessions, isolated registration ceremonies, cumulative registration and
retrieval/legacy-code budgets, and exact assertion-challenge-to-session binding.

Core services remain receipt-only consumers through
`IndependentApprovalVerifier`. An optional, disabled-by-default broker surface
mounts only when both the runtime credential and stable owner-OIDC session
service are configured; fragment-based legacy profiles cannot broker guided
enrollment or the C0 plan. A configured Core may create/status exact requests
and retrieve an already issued receipt over runtime-credential-authenticated
HTTPS. Every internal POST
also carries a domain-separated HMAC-SHA256 proof derived from that runtime
credential. The fixed proof binds exact method, route path, canonical wire-body
digest, Approval origin audience, route purpose, key identity, random 32-byte
nonce, issue time, and expiry. Approval verifies the complete proof and commits
one-use replay custody before any route action. Response-loss retries use a fresh
broker nonce while preserving the separate business idempotency key. Mixed
Bearer-only/signed versions fail closed and Core plus Approval must upgrade
together. This broker cannot approve, sign a human receipt, or bypass WebAuthn.
Core-created browser capabilities remain encrypted in the Approval store.
Stable `agentnet approval pending|watch` emits only content-free counts and may
open only public `/approval`; stable `open` never resolves or prints a request
capability or request ID. Profiles without owner OIDC retain request IDs and
local fragment opening for lab compatibility only. Ordinary Core requests are
possession-bound with purpose separation. Initiating process retains its Core
continuation or begin state; before creating an Approval request, Core derives a
per-transaction OIDC possession secret with HKDF or generates a distinct
high-entropy bootstrap possession secret. Core sends only SHA-256 hash to
Approval, stores bootstrap secret only inside encrypted begin custody, and uses
exact purpose-separated secret for signed retrieval. Approval stores binding in
existing encrypted capability custody. Broker proof canonicalization hashes
supplied secret so it never enters signed wire digest or audit detail. After WebAuthn, stable browser reports only `waiting_agent` or
`retrieved`; it shows no receipt or claim code and denies code regeneration.
Core binds request expiry exactly to candidate enrollment challenge. Exact
secret/domain/purpose/transaction/retrieval binding returns same current receipt
for response-loss retry; wrong secret consumes cumulative attempt budget and
conflicting retrieval digest fails closed. Legacy claim-code lifecycle remains
explicit compatibility only.

For headless Core identity, server-local `join guided --browser remote` stores
OIDC authorization URL only in purpose-encrypted continuation custody. Fixed
public `/activate` and its internal redirect route are unauthenticated and
independently rate-limited; they accept no transaction selector or private
value and redirect only when exactly one unexpired remote transaction is
waiting. Callback must match exact server-staged approved OIDC subject or
normalized verified-email alias. Wrong-account denial stages no challenge or
Approval request and remains retryable. OIDC callback atomically replaces
remote custody with challenge custody, marks challenge remote internally, and
redirects to fixed Approval page. Approval UI polls at most 10 seconds to close
callback/Core-staging race. Local-browser, zero, multiple, expired,
wrong-account, replayed, malformed, or conflicted state cannot activate
identity. Owner uses no server terminal or transfer value. The 60-poll
anti-abuse budget applies only while waiting for OIDC callback; callback and
Approval stages retain 2–10 second rate control and terminate at the fresh
challenge expiry. Exact nonterminal local state is resumable. CLI replacement
requires Core proof that the stored continuation is `expired` or `failed`,
reuses the same candidate key, and refuses absent, completed, malformed,
argument-drifted, or nonterminal state. This presentation path creates no
authority and guided completion remains identity-only. Six purposes are mandatory in configuration:
`identity.enrollment.approve`,
`authorization.bootstrap_plan.approve`,
`authorization.elevation.approve`,
`identity.credential.recover.approve`,
`identity.harness.revoke.approve`, and
`organization.relationship.accept`. The ordinary C0 profile does not mount or
require the legacy wildcard founder ceremony. Its bounded bootstrap-plan path
resolves the exact guided harness pair server-side, presents one purpose-specific
browser summary, and may prepare only the exact five communication plus five
matching revoke entitlements behind a `pending` guard in one atomic commit.
Only the dedicated C0 composition below can activate and consume that authority;
generic principal policy, messaging, mailbox, and administrative revocation
paths always deny bootstrap-plan entitlements. Optional purposes require explicit
configuration. Local SQLite and mocked ceremony vectors provide H evidence only;
real passkeys, independent host/device custody, TLS, key rotation and recovery
drills, and PD-002/004/005/009 remain external/owner gates.

## Bounded same-principal C0 composition

`C0PilotService` is the sole policy-enforcement and composition path for the
fixed `ordinary-two-harness-c0:v1` proof. Signed transport supplies the actor;
the four strict request bodies contain only their schema discriminator and no
plan, peer, recipient, payload, event, acknowledgement, digest, entitlement, or
use-count selector. Core resolves the committed plan and exact owner/fresh role
from authoritative state.

Every operation revalidates the domain, principal, policy revision, revocation
epoch, exact two active same-principal harnesses, exact two approved active
credentials, each credential epoch, caller credential, plan expiry, and guard
state. Added active harness or credential state persistently moves
`pending|active` to `invalidated`, fails any active attempt, and appends the
binding-invalidation audit record; later removal cannot reactivate approved
authority. A caller presenting a credential other than its approved role binding
is denied before it can mutate the guard.

The proof uses designated internal transaction seams on existing mailbox and
policy services (`_accept_in_transaction`, `_acknowledge_in_transaction`, and
the C0 guard checks) solely to keep each cross-service phase atomic. These are
not public/caller-facing APIs and do not duplicate mailbox or policy semantics.

The proof uses three atomic Core transactions:

1. **Fresh start:** creates/reconstructs the deterministic attempt, activates the
   guard, authorizes and accepts the fixed request, consumes its one use, records
   request custody and audit, then returns `waiting_owner`.
2. **Owner respond:** reads only the fact-linked request, acknowledges its exact
   event/envelope, sends one fixed causally linked reply, consumes the reply use,
   records retrieval/ACK/send/custody facts and audit, then returns
   `waiting_fresh`.
3. **Fresh complete:** reads and acknowledges only the fact-linked reply,
   revalidates all seven issuer-owned facts and both authoritative encrypted
   events/receipts, revokes exactly communication ordinals 1–5, terminalizes the
   guard/result/audit, and returns `COMPLETED_C0_ROUND_TRIP`.

Exact retries reconstruct from authoritative events, receipts, facts, uses, and
cleanup state; prose, status echoes, transport ACKs, and fact rows alone cannot
prove completion. Durable ambiguity/tamper is non-retryable; only typed
compare-and-swap races are retryable. The package-owned dedicated command
`agentnet c0-pilot responder` calls only status/respond through the signed
client. It does not construct semantic workers, models, queues, tasks,
artifacts, effects, tools, or A2A services. Local acceptance remains `accepted_local`; no
single-primary or HA durability promotion follows from this proof.

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
| Workers | deterministic-only unless exact sandbox evidence | per-harness clean worker and credential-free model broker with persistent one-use request nonces |
| Federation/C3/mesh | schemas and disabled seams | bilateral/MLS/quorum evidence plus owner decisions |

## Fixed ordinary Linux server setup composition

`agentnet server-agent setup` is the product-owned host composition boundary for the default self-hosted Linux profile. It accepts strict versioned non-secret requests whose sensitive values remain owner-only file references and exposes read-only `planned`, privileged `configured_not_started`/`waiting_owner_oidc_or_passkey`, and post-activation `operational` states. Request-v1 is the unchanged scanner-backed artifact profile. Request-v2 requires explicit artifact mode: enabled remains scanner-backed; disabled is communication-only, carries only `offline_custody`, creates no scanner trust/artifact key, and fails all artifact routes/bindings before protected custody. Disabled mode does not satisfy FILE/G13 or production/ship gates. `status=blocked` with `blocker=postgres_auth_not_ready` is the typed pre-configuration failure. The command is a fixed state machine, not a provider API or deployment DSL.

Approval digest v2 belongs only to request-v1. Approval digest v3 belongs to request-v2 and binds explicit artifact mode plus its mode-applicable input set. Both bind exact request bytes and canonical input fingerprints plus canonical Node.js, uv, AgentNet launcher, `systemctl`, and `useradd` paths/content and a deterministic SHA-256 identity of every directory path and regular-file path/size/content in the root-owned AgentNet package tree executed by `uv run --project`. Every executable is hashed from one no-follow descriptor with before/after metadata equality; symbolic links, nonregular entries, unstable reads, more than 20,000 package-tree records, or more than 512 MiB of package files fail closed. Privileged setup inputs are read twice through bounded descriptor snapshots in both Node and Python; partial reads accumulate, content mismatch rejects even when a filesystem does not advance same-size rewrite timestamps, and metadata/path custody checks remain mandatory. Privileged npm launch independently reproduces the selected digest. Python repeats full preflight under the exclusive setup lock and rejects request, input, path, executable, or package-tree drift before managed write; bytecode generation is disabled so setup execution does not mutate its approved source tree. After Core bootstrap, setup re-reads exact Approval policy and effective trust, including signer key IDs and public keys; any drift blocks before unit or marker commit.

Ordinary database contract is fixed local peer authentication: OS user, PostgreSQL role, and database are all `agentnet`; socket is `/var/run/postgresql`; DSN is `postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet`; loaded HBA must select an unshadowed `local agentnet agentnet peer` rule with no ident map. Apply may create the fixed Core OS identity, then forks a bounded read-only canary under that identity and inspects parsed current-file `pg_hba_file_rules`/`pg_ident_file_mappings` under local `postgres` identity. `pg_conf_load_time()` must be no older than both auth files, so current-file parsing plus service canary cannot masquerade as a loaded rule. Wrong transport/role/database, read-only/recovery server, parse error, mapped/broad/shadowing/non-peer rule, or stale unreloaded configuration blocks before AgentNet environment/config/database write. PostgreSQL role/database/HBA/ident administration and reload stay operator-owned separate approval boundaries.

After database gate, wrapper owns only locked Approval/Core/C0 service identities, private AgentNet roots, Approval provisioning, Core schema/config bootstrap, mode-applicable scanner public trust, fixed loopback ports, five hardened systemd units, bounded service start/restart, and redacted evidence. Approval, Core, and the isolated fixed C0 responder use systemd `Type=exec`, so successful start/restart evidence begins only after the approved Node executable has replaced systemd's transient executor; current-credential renewal remains a static selector-free oneshot with a first boot-relative check and each later hourly check scheduled from the service's inactive transition. Scanner trust is created and passed only for enabled mode; disabled mode rejects preexisting scanner trust and never provisions an artifact key. Before any Approval/Core product subprocess, direct child config/state/data paths are inspected with `lstat`; symlink, dangling-symlink, nonregular, ownership, or mode conflicts block. Existing private state trees and realized post-create/post-bootstrap Core custody are recursively revalidated as owner-only directories and single-link regular files before convergence continues. Existing operator-owned PostgreSQL administration, DNS, TLS/reverse-proxy routes, certificates, firewall, host system trust, and secret injection are explicit prerequisites and never mutated. Start verifies loopback and exact public HTTPS health, including exact artifact mode and capability set. Approval broker calls use an explicit TLS context backed by trust visible to CPython `ssl.create_default_context()` with certificate and hostname verification, never HTTPX's bundled CA fallback; ambient `SSL_CERT_FILE`, `SSL_CERT_DIR`, and `SSLKEYLOGFILE` fail closed before setup and all four process-spawning service units remove them; the fifth unit is the timer that invokes the hardened renewal service. Context-construction and transport failures remain sanitized. Post-activation start additionally proves `credential_state=current|renewal_needed`, performs a purpose-specific signed non-mutating Approval broker-readiness request through the configured public origin, starts the renewal timer, and starts the responder only while its exact terminal marker is absent. If terminal-marker commit succeeded but responder-config removal lost its response, same-digest setup validates both files' private custody and exact terminal binding, removes only the stale responder config, fsyncs the directory, and leaves the responder disabled; later retries never recreate it.

Exact reruns do not trust old marker as realized state. Apply reruns bootstrap, reloads and validates Core configuration, reloads and validates Approval trust, and writes exact unit bytes before marker commit. Request-v1 marker-v2 retains original request/package/config/unit provenance and same-request v1 migration semantics. Request-v2 marker-v3 additionally binds explicit artifact mode and rejects marker-v1/v2 as evidence. Both preserve monotonic revision, previous-marker digest, and exact prior-byte compare-and-swap under setup lock. A root-owned current-package attempt record is written before first product mutation and removed only after marker commit, allowing exact interruption recovery while rejecting unowned pre-existing state.

The `0.1.37` corrective migration boundary admits only the exact released
`0.1.33` five-unit marker. Earlier sources use the separately released 0.1.33
boundary first; 0.1.37 does not add another direct legacy edge and does not
accept 0.1.34 or 0.1.35 as source markers. Exact prior managed configs/units are
journaled before writes. For this edge, the target marker is the forward-only
boundary: successful or exactly reconciled marker commit disarms source-byte
rollback; all five units are quiesced before Core bootstrap may migrate
PostgreSQL; and the journal is retained until bootstrap succeeds. A retained
journal whose exact committed target is 0.1.33 may be superseded only after its
marker/config/unit provenance is revalidated; it remains durable until 0.1.37
atomically replaces it with the separate new journal before changing managed
bytes.
Quiescence clears systemd's failed latch only after bounded stop/disable and
still requires exact loaded/unit-file/inactive/PID postconditions. Approval and
Core both declare SIGTERM/status 143 successful. Only the `0.1.31` topology
expansion may create the responder account/root and target-only units, and it
rejects pre-existing account/group/state/unit, override, enablement, or
live-runtime residue before marker commit. There is no automatic rollback after
the boundary. Marker never proves identity, authority, service health,
readiness, PostgreSQL durability, HA, or production certification.
OIDC/WebAuthn, guided key-possession enrollment, and offline activation remain
explicit ceremonies. Remote Managers may provide immutable package guidance
and inspect sanitized evidence only; target coding agents own host execution.

Published `0.1.37` adds only the exact `0.1.33` five-unit corrective migration edge above.
Setup rejects duplicate/non-finite JSON members, strictly parses the managed
identity actor with canonical `VerifiedActor`, checks current
domain/harness/credential labels, and retains exact profile shape plus private
P-256 key custody/readability. It removes the impossible requirement for
`actor.key_id`, which canonical actor serialization forbids and never emits.
Database-backed credential-to-key proof remains owned by `server-agent
activate`; setup does not replace it with a self-asserted profile field.

Published `0.1.38` adds one exact `0.1.37` five-unit forward marker edge and
changes post-restart public probe timing. Public Approval/Core health and public
Core readiness reuse the existing finite 90-attempt startup bound; the ordinary
30-attempt default remains for non-startup probes. The remote Hub peer reported
from bounded read-only fresh-install preflight that its default
`Python-urllib/*` request identity was rejected with HTTP 403 before origin
routing. That report is corroboration only, not retained reproducible proof.

Candidate `0.1.39` changes only the health request object: explicit GET,
`User-Agent: AgentNet/0.1.39`, and `Accept: application/json`, sent through the
same proxy-disabled, redirect-rejecting stdlib opener. System TLS and hostname
verification, bounded attempts and timeout, response bounds, exact JSON
identity/readiness, authority, and auxiliary-unit ordering remain unchanged.
The candidate adds no migration edge: clean-state setup is allowed, while every
existing release marker or journal fails closed.

Candidate `0.1.45` adds one server upgrade edge from the exact `0.1.44`
five-unit marker and schema-v6 PostgreSQL catalog. Before mutation it journals
the exact source marker, Core configuration bytes, managed unit bytes, systemd
state, migration catalog, active identity/credential, protected relation
digests, and mailbox cursor. The candidate migrates only v6→v7 and creates one
exact server endpoint in `restart_required`; it preserves the enrolled identity,
credential, authorization/message state, and cursor.

Before target-marker commit, a caught failure invokes exact rollback: candidate
services are quiesced; unchanged v7-only state and migration 7 are removed;
schema metadata, source files, and systemd state are restored while the source
marker must remain byte-exact. A process interruption retains the journal and
the next run resumes only that transition. If any journal-selected protected
relation digest, file, unit, marker, or service fact drifted, rollback stops and
retains the journal rather than overwriting uncertain state. Exact target
commit clears the journal and closes the downgrade path. This mechanism is not
production deployment, HA/restore, signed-installer, or high-tier gate evidence.

Corrective `0.1.46` accepts only the exact `0.1.45` five-unit marker and keeps
the schema-v7 catalog unchanged. Its forward-only setup transition journals and
compare-and-swaps the prior managed bytes, replaces the broken renewal timer
with `OnUnitInactiveSec=1h`, and removes the ineffective monotonic
`Persistent=true`. A new package digest requires a new owner approval, but the
server identity, credential/key material, PostgreSQL state, endpoint lifecycle,
and external prerequisites remain in place. The packaged Ubuntu upgrade lane
accelerates a copy of the installed timer and requires two successful real
systemd activations with a later finite `NEXT` before restoring the production
hourly schedule.

`agentnet server-agent reset` is destructive server-manager-only package recovery. It acquires the same permanent root-only setup lock before inventory, rejects state without pre-existing lock custody, stops/disables and proves all five managed units inactive, removes only allowlisted package deployment units/state, and preserves the lock/root so a concurrent or later setup cannot lock a different inode. It always reloads systemd, including exact response-loss retry, and retains PostgreSQL, runtimes, package installation, proxy/TLS/DNS/firewall inputs, and locked service identities. Reset is not a browser action, onboarding step, or secret-rotation path. Exact AgentNet database/role reinitialization is a separate destructive operator boundary requiring sanitized target inventory, explicit named approval, an explicit backup/rollback decision, and redacted audit evidence; unrelated/shared/valuable targets fail closed.
