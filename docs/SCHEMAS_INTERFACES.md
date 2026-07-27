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
| `ApprovalVerifier` | strict receipt consumer plus standalone `webauthn==3.0.0` UV ceremony issuer; real independent deployment still required | exact purpose/domain/transaction and current human/guest-owner consent; ceremony mechanism never owns corporate authority |
| `OIDCHTTPTransport` | direct TLS connection to one provider-validated address snapshot | URL origin, DNS/address policy, TLS hostname/SNI, Host authority, response bounds, and no-proxy/no-redirect behavior remain exact and fail closed |
| `WorkloadIdentityProvider` | verified SPIFFE/mTLS context seam | workload never becomes a human |
| `WorkflowEngine` | explicit transactional effects; optional Temporal-style | workflow success never fabricates effect evidence |
| `MLSProvider` | unavailable until maintained stack passes | room policy/membership and visible key holders remain explicit |
| MCP/direct IPC | official MCP; Unix peer-credential framing on Linux/macOS; protected client-PID named pipes on Windows | arguments cannot establish caller identity |
| A2A SDK routes | official SDK 1.1.0 | public identity remains external-low-trust |

## Host-local session and replay contract

`IPCSessionClaims` remains schema `agentnet.ipc.session.v1` but now requires the
canonical host `platform` and account identity (`uid:<n>` or `sid:S-...`) in
addition to PID, process creation time, executable digest, session, credential
epoch, methods, and expiry. These claims are supervisor-sealed; accepted peer
and direct-parent facts are always server-derived. The replay namespace is
`agentnet.ipc.replay-context.v2`, adding platform and account identity. This is
a deliberate pre-stable replay-domain break: older local capabilities expire
within their bounded one-hour lifetime and are not accepted by the new
supervisor root/session path.

MCP bootstrap assurance is
`server_derived_account_process_parent_module`. Linux/macOS use owner-only Unix
sockets; Windows uses protected named pipes with remote-client rejection and
server-derived client PID. Pi capability bytes never enter argv or environment:
Linux uses sealed memfd, macOS a read-only inherited pipe, and Windows a one-time
exact-process private pipe. Missing delivery acknowledgement fails local-binding
activation.

## Approval configuration, storage, and HTTP contract

`ApprovalServiceConfig` is strict (`extra="forbid"`, schema `1.0`) and binds
one canonical HTTPS `public_origin`, an exactly matching `rp_id`, `verifier_id`,
bounded TTL/body sizes, absolute owner-only storage/key paths, and one or more
unique approvers. Configured purposes must collectively cover all six mandatory
approval consumers, and every approver must cover enrollment. Signer private
keys remain file references; load verifies each key's configured thumbprint.

The approval SQLite catalog is version 4 and is checked on every open against
both exact `sqlite_master` objects, stored catalog SHA-256, and immutable
migration names/checksums. The default self-hosted profile may run this service
on the existing Core server under a distinct OS identity, credential, storage
root, and loopback listener, while retaining
`independent_boundary_proven=false`. Separate administration is the optional
high-assurance profile. Existing exact v1, v2, or v3 stores upgrade under
`BEGIN IMMEDIATE` only after source metadata/catalog and applicable
migration-history verification. The atomic chain ends at v4; failed or
conflicting migration rolls back. It contains:

- `approval_webauthn_credentials`: exact approver/domain/user handle, public key,
  sign count, device/back-up metadata, and active/revoked lifecycle;
- `approval_registration_sessions`: hashed one-time capability, encrypted
  challenge, bounded attempts, expiry, and consumption;
- `approval_requests`: approver/domain/purpose, encrypted canonical transaction,
  exact digest, encrypted challenge, state, active fingerprint, attempts,
  delivery mode, approval-host-only encrypted browser capability, exact pending
  expiry, cumulative claim-code failure/rotation budgets, and the exact owner
  browser session bound to an assertion challenge;
- `approval_request_idempotency`: exact Core request key/digest binding;
- `approval_claim_codes`: request-bound claim-code hash, five-attempt bound,
  expiry, and exact retrieval-digest retry binding;
- `approval_issued_receipts`: one row per request, exact credential,
  authentication/issuance/expiry times, encrypted receipt, and receipt digest;
- `approval_owner_bindings`, `approval_oidc_login_transactions`, and
  `approval_browser_sessions`: pinned owner OIDC identity, callback claim/consume
  lifecycle, hashed browser sessions, encrypted CSRF state, rotation/revocation,
  and exact RP/origin/verifier bindings;
- `approval_registration_budgets` and `approval_registration_ceremonies`:
  owner-wide cumulative attempt/rotation limits and isolated per-tab WebAuthn
  registration state;
- `approval_store_migrations`: exact ordered approval-store migration names and checksums;
- `approval_internal_broker_replay`: derived key identity, nonce SHA-256, exact
  request bindings, issue/expiry, and committed consumption time; never the raw
  nonce or runtime credential;
- `approval_audit`: content-minimized ordered lifecycle outcomes.

SQLite uses WAL, `synchronous=FULL`, foreign keys, bounded busy timeout, and
`BEGIN IMMEDIATE`; it claims `single_host_local_only`, never HA durability.
`LocalEnvelopeCipher` protects challenges, transactions, and receipts with
purpose-specific AAD. Expiry cleanup commits before stale-request denial so
expiration and audit evidence survive the failed request.

Browser/API routes are isolated from Core routes and selected by exact profile.
Stable owner-OIDC profiles mount only stable routes; explicit lab profiles mount
only legacy capability routes. The three internal broker routes mount only when
both the runtime credential and stable owner-OIDC session service exist; a
fragment-based legacy profile gets no broker surface.

| Route | Input/effect |
|---|---|
| `GET /approval` / `GET /approval.js` | no-store CSP-constrained UI; stable mode uses authenticated owner session, lab mode reads then removes fragment capability |
| `GET /v1/approval/owner/session` | stable preauth/session state; preauth cookie is Secure/HttpOnly/SameSite=Lax for the cross-site OIDC callback, while authenticated session and CSRF cookies remain SameSite=Strict |
| `POST /v1/approval/owner/oidc/start` / `GET .../callback` | exact preauth+CSRF+PKCE/state/nonce login; callback atomically claims then consumes one transaction and rotates to a pinned owner session |
| `POST /v1/approval/owner/registration/begin` / `.../complete` | session-bound, isolated WebAuthn UV registration with cumulative owner budgets |
| `GET /v1/approval/owner/requests` | exact owner/domain pending and approved-unretrieved requests; no capability, receipt, transaction, or claim code |
| `POST /v1/approval/owner/requests/options` | exact owner session/request selection; server resolves encrypted capability and returns a purpose-specific bounded summary plus session-bound assertion options |
| `POST /v1/approval/owner/requests/complete` / `.../reject` | exact session+CSRF request action; completion requires assertion challenge bound to that active session |
| `POST /v1/approval/owner/requests/regenerate-code` | rotates only a current, unretrieved, nonterminal code within cumulative limits and receipt expiry |
| `POST /v1/approval/registration/options` / `.../verify` | lab-only fragment registration flow |
| `POST /v1/approval/requests/options` / `.../verify` / `.../reject` | lab-only exact fragment request flow |
| `POST /v1/approval/internal/requests` | runtime Bearer plus signed one-use broker proof; exact idempotent Core request with challenge-bound expiry; never returns approval URL |
| `POST /v1/approval/internal/requests/status` | runtime Bearer plus signed one-use broker proof; request/digest status |
| `POST /v1/approval/internal/receipts/retrieve` | runtime Bearer plus signed one-use broker proof and exact claim code/domain/purpose/digest binding; retryable receipt retrieval, not claim-code consumption |

Each internal proof uses a fixed schema and domain-separated HMAC-SHA256 key
derived from the existing high-entropy runtime credential. It binds exact
`POST`, route path, canonical wire-body SHA-256, configured Approval origin,
route-specific purpose, derived key ID, random 32-byte nonce, issued time, and a
maximum 30-second lifetime with five seconds future-clock tolerance. Approval
fully verifies the proof, then atomically commits one-use `(key_id, nonce_hash)`
replay custody before parsing/acting on the canonical body. Each successful
consume prunes expired replay rows in the same transaction; idle stores retain
expired rows until the next consume without affecting authorization. A malformed proof
does not touch replay state. A valid proof over a noncanonical or schema-invalid
body may consume its nonce but performs no business action. Transport retry uses
a fresh broker nonce; business idempotency remains a separate unchanged layer.
Core and Approval have no mixed-version compatibility mode.

All POST bodies are bounded while streaming regardless of `Content-Length`, use
duplicate-key/non-finite rejecting JSON and strict schemas, and expose only a
generic `request_denied` error. The service binds loopback only. TLS exposure belongs to a separately
credentialed reverse-proxy role; that role may share the existing server in the
default profile or use separate administration in the optional high-assurance
profile.

## OIDC endpoint configuration contract

`OIDCEnrollmentConfig` and `OIDCProviderConfig` preserve the public-only default.
`token_endpoint_auth_method` is exactly `none`, `client_secret_post`, or
`client_secret_basic`; `none` is the backward-compatible default.
`client_secret_env` is an environment-variable name, never a secret value.
Confidential methods require that reference and explicit discovery
advertisement; `none` rejects it. Runtime composition resolves a bounded secret,
keeps it out of `redacted_export()`, repr, logs, audit, and evidence, and uses
only the explicitly configured POST-body or Basic authentication form.
`allowed_endpoint_origins` contains canonical HTTPS origins;
`allowed_private_endpoint_cidrs` contains canonical private networks; and
`pinned_endpoint_addresses` contains canonical safe unicast IPv4/IPv6
addresses. Unspecified, loopback, link-local, multicast, reserved,
documentation, benchmark, transition/softwire, and IPv4-mapped classes are not
admissible pins. Private/non-global pins require explicitly configured origins
and non-empty exact `pinned_jwk_thumbprints`. When exact endpoint addresses are
present, every DNS result must match one. Otherwise a global result is eligible
by default and a non-global result must match an exact address or configured
private network. The default resolver is the host system `getaddrinfo` facility;
its result is untrusted input. The validated canonical address tuple is supplied
to `OIDCHTTPTransport` and is the only set the direct TLS transport may try for
that request.

The internal-invitation OIDC verifier digest includes origins, private networks,
exact endpoint addresses, and the token-endpoint authentication method, so
changing endpoint or client-authentication policy changes verifier identity
rather than silently reusing an old binding. It excludes the runtime secret and
the environment-variable reference.

`ApprovalServiceClientConfig` is a non-secret child of `OIDCEnrollmentConfig`.
It binds one canonical HTTPS approval origin, an environment-variable reference
for the runtime Core bearer, one approver principal, timeout, and response-body
ceiling. Core uses a direct `httpx` client with environment proxies and redirects
disabled, serializes each internal body once as canonical JSON, creates a fresh
broker proof per attempt, and rejects duplicate-key, non-object, oversized, or
wrong-status response JSON.

Core schema migration 3 adds `oidc_enrollment_continuations`. It stores only a
SHA-256 continuation hash, bounded poll state, encrypted callback challenge,
exact approval request ID/digest/expiry, completion request digest, and encrypted
completion response. Core migration 4 adds the bounded C0 bootstrap-plan
contract: `bootstrap_grant_plans`, `bootstrap_grant_plan_items`,
`c0_plan_guards`, `c0_plan_guard_entitlements`, `c0_pilot_attempts`, and
`c0_pilot_facts`. The plan binds one exact principal, two distinct enrolled
harnesses and credential epochs, policy/revocation epochs, one-use profile,
expiry, idempotency digest, and encrypted committed result. Its ten deterministic
items are exactly five communication entitlements plus five entitlement-specific
revoke powers; plan and guard state commits all ten or none.

Persisted guard state is `pending | active | revoked | expired | invalidated`.
Successful exact cleanup transitions `active` to `revoked`; identity-set drift
terminalizes it as `invalidated` and fails an active attempt. Neither terminal
state can return to `pending` or `active`. Persisted attempt state is `active |
evidence_complete | communication_revoked | failed | expired`;
`evidence_complete` exists only inside the final atomic transaction before the
same transaction records exact cleanup and `communication_revoked`. `c0_pilot_facts` permits exactly:
`request_durable_custody`, `request_retrieved`,
`request_recipient_acknowledged`, `reply_sent`, `reply_durable_custody`,
`reply_retrieved`, and `reply_final_acknowledged`. Each row binds its typed
issuer kind, optional exact issuer harness, event/receipt/envelope evidence, and
canonical evidence JSON. Completion also revalidates the authoritative event and
receipt rows; fact rows alone are not a completion oracle. The only public
result fields are schema plus one sanitized status:
`prepared_unusable | waiting_owner | waiting_fresh | expired | invalidated |
COMPLETED_C0_ROUND_TRIP`.

The guided-enrollment public routes are:

| Route | Input/effect |
|---|---|
| `POST /v1/enrollment/oidc/begin` | candidate key/harness metadata; creates OIDC transaction plus hash-only continuation |
| `GET /v1/enrollment/oidc/callback` | exact state/code; stores challenge atomically and returns safe HTML unless explicit JSON compatibility is requested |
| `POST /v1/enrollment/oidc/poll` | transaction ID plus opaque continuation; bounded 2–10 second polling and Core-side approval staging |
| `POST /v1/enrollment/oidc/complete` | continuation, human claim code, and candidate PoP; Core retrieves receipt and atomically consumes enrollment challenge |

The signed selector-free C0 routes are:

| Route | Exact request/effect |
|---|---|
| `POST /v1/c0-pilot/start` | body is only `agentnet.c0-pilot.start.v1`; exact fresh actor starts or reconstructs the fixed request phase |
| `POST /v1/c0-pilot/respond` | body is only `agentnet.c0-pilot.respond.v1`; exact owner actor performs the no-model request ACK/fixed reply phase |
| `POST /v1/c0-pilot/complete` | body is only `agentnet.c0-pilot.complete.v1`; exact fresh actor verifies reply ACK plus seven facts and atomically cleans up five communication powers |
| `POST /v1/c0-pilot/status` | body is only `agentnet.c0-pilot.status.v1`; either exact planned actor receives one sanitized stage after current-binding checks |

Bodies are canonical JSON with duplicate keys, unknown fields, non-finite values,
and caller selectors rejected. Every response is non-cacheable. Generic integrity
conflicts are non-retryable; only typed transaction races return retryable 409.
CLI exposes only `agentnet c0-pilot start|status|complete`; the owner side runs
through `agentnet supervisor-run --c0-pilot-responder` and the internal respond
route. Neither surface prints protected identifiers or evidence.

Receipt bytes never appear in candidate responses. Core reserves an exact
completion digest only after PoP and claim-code retrieval succeed. Exact retry
returns the encrypted result; a crash after enrollment commit reconstructs the
same deterministic harness/credential binding from authoritative rows.
Enrollment creates no entitlement.

`join guided`, `approval open`, and `approval watch --open` expose an additive
`--browser system|terminal` presentation option. `system` remains the default.
`terminal` is POSIX-only, opens and verifies `/dev/tty`, accepts printable-ASCII
HTTPS URLs without userinfo, writes one flushed framed block, and returns only
sanitized failures. Browser mode is not stored in continuation, identity,
approval, or audit schemas and changes no route or authority contract.

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
envelope digests. Supervisor/background consumers receive that same projection and cannot
promote it through any generic read. Protected execution uses strict supervisor
operations in this order: `authorize` (one exact current `task.process` grant
use), durable `custody`, then `payload-release`. The release request binds the
existing authorization, cursor, and server-recorded local queue ID; it accepts
no caller-selected actor, recipient, credential, domain, role, or idempotency
key. The response binds event/envelope/payload/intent/provenance/grant/decision
and release receipt, sets payload/semantic authority true, and keeps tool/effect
authority false. The `task_payload_releases` row and audit append commit before
plaintext leaves the transaction. Exact retries fresh-check current authority
and return the same receipt without another grant use. `result` requires that
exact committed release row. Ordinary non-task message projections retain
their existing authorized payload shape.

## Protected TaskGrant payload-release wire and storage contract

`POST /v1/supervisor/executions/payload-release` accepts strict
`agentnet.supervisor.task-payload-release.request.v1` content: the existing
background authorization, immutable mailbox cursor, and exact local queue ID
already committed by supervisor custody. Signed transport supplies the current
recipient harness. The service revalidates current policy, credential, domain,
grant, task intent, conflict, lifetime, payload, envelope, and provenance state
inside one transaction.

Schema migration 2 adds `task_payload_releases`, one row per
`(event_id, recipient_harness_id)`, with a composite foreign key to
`supervisor_executions`. It binds release request and authorization digests,
local queue, TaskGrant and policy-decision IDs, intent/payload/envelope digests,
policy/credential/domain epochs, authorization expiry, release time, and one
unique receipt ID. Migration 3 is the guided-enrollment continuation table described above;
migration 4 is the bounded C0 bootstrap-plan contract. SQLite permits only the
exact N/N-1 v3→v4 Core upgrade in one transaction; PostgreSQL requires the same
contiguous checksums plus an exact live table/column/default/constraint/index
catalog before v3 migration, after migration, and on v4 open. Unknown, future, prototype, altered, partial,
noncontiguous, or unsupported pre-v3 Core state fails closed. Immutable
migrations 1 through 3 remain unchanged.

A successful nonduplicate response is HTTP 201; exact retry is HTTP 200.
Successful responses are `Cache-Control: no-store` and `Pragma: no-cache`.
Result upload requires the same release row and incorporates its receipt into
result provenance. Neither transport success nor result upload proves any
business effect.

## Mailbox acknowledgement wire and storage contract

`POST /v1/mailbox/{event_id}/acknowledge` accepts only the exact stored
`envelope_digest`. Caller identity and recipient harness come exclusively from
the signed transport context; request bodies cannot select an actor, recipient,
domain, credential, fact, presentation state, processor, obligation, or effect.
The current exact recipient must hold `mailbox.acknowledge` authority.

The operation records the existing canonical delivery fact
`recipient_committed`, which means durable recipient custody and dedup evidence.
It does **not** mean `presented`, `processing`, response-obligation
`acknowledged`, or business-effect completion. The recipient row update,
recipient-owned receipt, and `mailbox.acknowledge` audit record commit in one
transaction. A fresh signed retry after response loss returns the original
receipt with `duplicate=true` and the current later delivery fact without
rewriting or downgrading state. Wrong recipient/digest, stale or revoked actor,
late expiry, and illegal predecessor state fail closed.

The operator CLI exposes this as `agentnet message acknowledge EVENT_ID
--envelope-digest DIGEST`. Credential-free local bindings expose the same
operation as `agentnet.inbox.acknowledge`; MCP and Pi render it as
`agentnet_inbox_acknowledge`. All argument schemas omit identity and recipient
fields.

## Binary artifact client and operator contract

The signed HTTP client exposes the existing staged artifact routes without
changing their authority model:

- `reserve_artifact` binds idempotency key, lowercase plaintext SHA-256, exact
  size (0..16,777,216), canonical media type, classification, attachment role,
  and reservation lifetime;
- `upload_artifact_bytes` signs the exact raw body as
  `application/octet-stream` and binds one route-safe reservation ID;
- `promote_artifact` binds the immutable object version and strict provenance;
- `abort_artifact_reservation` acts only on the current caller's unpromoted
  reservation;
- `artifact_lifecycle` reads content-free lifecycle metadata;
- `download_artifact` internally issues and consumes one current exact-harness,
  short-lived, single-use capability and returns only the resulting HTTP
  response. Normal CLI output never contains the capability.

`agentnet artifact upload` composes reserve → raw bytes → promotion and reports
the exact returned state; first success remains `quarantined`. It reads one
caller-owned, non-symlink regular file from a stable descriptor and caps bytes
at 16 MiB. `agentnet artifact download` refuses replacement, pins a
caller-owned non-shared output directory, requires
`application/octet-stream`, and creates an exclusive `0600` file. Scanner
attestation, release, legal hold, and deletion remain separate privileged
service operations and have no ordinary artifact CLI shortcut.

Canonical MCP/Pi tools deliberately do not accept artifact bytes, base64, or
arbitrary host paths. Such arguments would cross model-context and filesystem
trust boundaries. A future local artifact binding needs configured staging
roots plus supervisor-issued opaque handles; no caller-supplied actor,
credential, scanner authority, release decision, task grant, object key, or
download audience is part of this client/CLI contract.

## Response-obligation wire and storage contract

`PostAction` and `StructuredRequestAction` accept an optional strict
`response_obligation` spec (`response_required`, `responsible_harness_id`,
`deadline_at`, `response_schema_id`, `response_schema`). A schema ID and its
self-contained JSON Schema must be supplied together. The spec is part of the exact request
payload digest, and the obligation row is created in the same transaction as
request acceptance. A new `obligation_response` conversation action is the
only closure path: it must repeat the obligation ID, the original request
event ID, and the exact request payload digest, must come from the exact
responsible recipient harness, and commits the terminal `completed`/`failed`
state atomically with the accepted response event.

| Operation | Exact body/evidence | Authority effect |
|---|---|---|
| `POST /v1/conversations/{id}/actions` with `response_obligation` | strict spec inside the digested request payload; optional schema ID plus self-contained JSON Schema | creates one obligation bound to the accepted request and exact response-schema digest |
| `POST /v1/conversations/{id}/actions` kind `obligation_response` | obligation ID plus exact request event ID and digest | atomically closes the obligation as `completed`/`failed` |
| `POST /v1/response-obligations/{id}/transition` | `to_state` in recipient progress states, optional `expected_revision` | responsible-recipient progress only; `recipient_committed` additionally requires the durable mailbox fact |
| `POST /v1/response-obligations/{id}/cancel` | bounded `reason_code`, optional `expected_revision` | exact accountable requester cancellation |
| `POST /v1/response-obligations/reconcile` | bounded `limit` | derived, idempotent restart/offline reconciliation; no new authority |
| `GET /v1/response-obligations` / `/{id}` / `/inbox` | authenticated participant read | requester/responsible-scoped, non-enumerating; inbox counters are content-free |

The operator CLI mirrors these routes under `agentnet obligation
list|show|inbox|transition|cancel|reconcile`.

Credential-free local bindings mirror the conversation and obligation journey
with exact `agentnet.conversation.*` and `agentnet.obligation.*` methods. Their
argument models are strict and intentionally contain no caller identity; MCP
and direct Unix IPC derive the actor from the current supervisor-bound harness
session. Pi exposes the same complete canonical operation set as MCP.

## Schema evolution

The handshake selects the highest mutually allowed protocol/schema profile.
The supported window is N/N-1 only after explicit migration tests. Expansions
precede backfill/verification and contraction. Revocation/security state never
rolls back. Unsupported events remain queued or receive a typed rejection;
intermediaries never strip unknown signed fields.

AgentNet's immutable first-release baseline is migration 1 and already includes
the response-obligation, relationship-governance, and policy-exception tables.
The current Core schema is v4: migration 2 adds protected task-payload release,
migration 3 adds guided OIDC enrollment continuation, and migration 4 adds the
bounded C0 bootstrap-plan contract. Fresh SQLite and PostgreSQL stores initialize
the same complete v4 catalog. Startup fails closed on a missing or altered
migration, table, index, trigger/constraint, noncontiguous history, future
version, or unsupported older version.

No pre-release or differently named database is accepted as an authority
source, and no unilateral relationship can be converted into consent. Import
requires a reviewed non-authority export into a fresh current store followed by
fresh exact bilateral approval. Rollback may restore only an exact verified,
current-compatible backup; it cannot downgrade schema metadata or infer,
preserve, or reactivate authority from unsupported bytes.

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

## Ordinary server setup request and evidence

`agentnet server-agent setup` accepts two strict, version-disjoint JSON requests. `agentnet.server-setup.request.v1` retains the original scanner-backed artifact profile and forbids `artifact_mode`. `agentnet.server-setup.request.v2` requires explicit `artifact_mode`: `enabled` requires `scanner_trust_file`; `disabled` forbids that field, including explicit JSON `null`. The communication-only disabled profile retains `offline_custody`, omits `artifact_storage`, creates no scanner trust or artifact key, and rejects every artifact operation and non-empty message/task artifact binding before bytes, metadata, mailbox custody, or task custody. It does not satisfy `FILE-001..006`, G13, or production/ship readiness. Unknown fields, omitted schema, and noncanonical origins, audiences, DSNs, paths, environment references, OIDC callbacks, approvers, or scanner trust fail before host mutation. Ordinary requests accept only `postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet`. No request stores a secret value: it binds absolute owner-only file references for Core/Approval environments, provider metadata, approver policy, and scanner public trust only when enabled. Broker credential is validated by value before mutation but never included in evidence: Core and Approval values must match and contain 43–512 printable ASCII bytes `0x21..0x7e`.

Every invocation emits `agentnet.server-setup.evidence.v1` with one typed status, effective artifact mode, aggregate request digest, fixed profile, package version, managed units, loopback ports, HTTPS topology, ordered step results, PostgreSQL prerequisite manifest, next action, and explicit `identity_enrolled`, `authority_granted`, and `production_durability_proven` claims. Request-v1 uses `agentnet.server-setup.approval-digest.v2`; request-v2 uses `agentnet.server-setup.approval-digest.v3`, binding explicit artifact mode and only the mode-applicable input set. Both bind exact request-file bytes, canonical absolute references, non-secret JSON fingerprints, environment variable-name sets, canonical Node/uv/AgentNet paths plus stable no-follow executable SHA-256 identities, and one hash computed from deterministic path/type/size/content records for the full root-owned package tree executed by `uv run --project`. Environment values remain reference-only and are never hashed or emitted. Privileged npm launcher independently reproduces the version-selected digest before creating Python runtime state; Python repeats full preflight under setup lock. Blocked output contains bounded blocker/message and all three claims false. Environment values, credentials, signer private material, approval receipts, claim codes, identity profiles, and raw product stderr are never output.

Plan performs no privileged or managed-host mutation: no service identities, AgentNet roots, secret copies, database schema/config, units, or services. The npm launcher may materialize its caller-owned Python runtime before Python preflight. Apply requires root, exact frozen-plan digest, and nonblocking host lock. AgentNet, Node.js, and `uv` come only from current system-wide installed context, never request JSON. Canonical runtime under `/root`, `/home`, or `/run/user` is rejected because hardened units use `ProtectHome=true`. Target coding agent must establish root-owned non-writable service-readable lineage before invoking package code as root because untrusted code cannot self-validate safely; launcher and Python package-tree checks are defense in depth.

After fixed Core account exists, apply requires two bounded read-only PostgreSQL probes before AgentNet environment/config/database write: exact service-identity socket connection proving current role/database and writable-primary state, then parsed current-file HBA/ident inspection proving exact unshadowed `local agentnet agentnet peer` with no map or parse error, plus `pg_conf_load_time()` freshness against both auth-file modification times. Failure emits `status=blocked` with `blocker=postgres_auth_not_ready`; PostgreSQL administration/reload remains external and separately approved. After that gate and before any Approval/Core product subprocess, setup uses `lstat` to reject symlink, dangling-symlink, nonregular, ownership, or mode conflicts at fixed config/state/data child paths. Existing and newly realized service-owned state trees are recursively custody-checked before later product writes, unit commit, or marker commit.

Apply reruns bootstrap and validates realized Core/Approval state; marker never skips it. Request-v1 writes root-only marker-v2, retaining original meaning and same-request v1→v2 migration. Request-v2 writes marker-v3, which additionally binds exact `artifact_mode`; marker-v1/v2 cannot satisfy request-v2. Both bind request, package, units, non-secret Approval/Core config digests, revision, and previous-marker digest while excluding only offline-activation enrollment labels. Marker replacement uses exact prior-byte compare-and-swap under setup lock. Marker is provenance, not health/readiness/identity/authority/durability evidence. Existing Approval policy must match exact owner OIDC and complete approver set—extra trust anchors block. Start is valid only with apply and affects only daemon reload, Approval enable/start, Core enable/restart, and exact active-state/local/public probes. Core health/readiness bind exact artifact mode and capability set; Approval health returns `agentnet.approval.health.v1`. Setup validates exact service role, version, origin/domain/profile/runtime identity and rejects generic HTTP 200. `operational` requires exact enrolled binding and public Core readiness; pre-enrollment start reports `waiting_owner_oidc_or_passkey`.
