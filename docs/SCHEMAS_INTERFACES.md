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
| `EndpointLifecycleService` | schema-v7 exact endpoint row plus derived reconciliation | locator/display state never creates identity or authority; restart requires the exact actor, generation, and new process measurement |
| `ClientSetupCoordinator` | owner-private resumable user-level setup/update | current credential is authoritative; only an opaque continuation is setup-specific durable state |
| recipient resolver | authenticated friendly selector to one exact endpoint and current scope | zero/multiple/stale/revoked/cross-domain results deny; no sibling or last-active fallback |

Corporate native A2A `message:send` ingress requires the strict request-metadata
strings `agentnetIntent`, `agentnetIdempotencyKey`, `agentnetTaskGrantId`,
`agentnetDataClass`, and `agentnetCollaborationScopeId`. The last field is only
a selector for one existing immutable scope ID. It supplies neither caller
identity nor authority: transport proof supplies the actor, the mounted route
supplies the exact recipient, and Core resolves the selected current scope
against those facts, the declared classification, and the deterministic
operation resource. There is no omitted-ID default, inferred scope, sibling
selection, or allow-all path.

For messages the collaboration operation is `message.send` on
`conversation:<context_id>` (or `conversation:direct` when no context exists).
For tasks it is `task.propose` on `task:<deterministic_event_id>`; immediate
queueing and later recipient approval separately require `task.accept` on the
same task resource. These checks do not replace the existing
`a2a.message.submit` or `a2a.task.submit` business grant, and a business grant
does not replace collaboration scope. Core binds the resolved scope's complete
`authorization_context()` snapshot into the immutable event payload and binds
the same scope ID into `AssignmentRequest`. Missing, mismatched, revoked,
expired, ambiguous, cross-domain, wrong-recipient, wrong-action, wrong-resource,
and unsupported-class selections fail before mailbox persistence. Exact replay
rechecks current scope without consuming the business grant again; scope
revocation also blocks mailbox reads. Unsigned public A2A requests remain
tainted, non-executable proposals and do not acquire scope authority from
metadata.

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
sockets; the runtime opens a non-inheritable stable path descriptor, validates
the exact socket through that descriptor, and retains it until successful
renewal, restart, or stop so unlink/rebind cannot hide behind inode reuse.
Post-publication startup failure and terminal restart exhaustion remove the
locator and close the pin; retryable renewal preserves it. Runtime status and
its content-free state expose a fixed `last_failure` code, never raw exception
text. Platforms without that primitive fail the MCP binding closed. Windows uses
protected named pipes with remote-client rejection and server-derived client
PID. Pi capability bytes never enter argv or environment. Interactive
`manager-run` stages the packaged Pi extension inside the private session and
owns extension discovery/tool-selection flags; caller overrides fail before
identity loading. Supervisor Pi capability delivery uses sealed memfd on Linux,
a read-only inherited pipe on macOS, and a one-time exact-process pipe on
Windows. Interactive `manager-run` is Linux-only until equivalent process-tree
and filesystem containment exists elsewhere. Missing delivery acknowledgement
fails local-binding activation.

## Schema-v7 endpoint lifecycle and user setup contract

`EndpointLifecycleStatus` is strict and content-free. It carries the endpoint
ID and canonical `agentnet:<domain>:<harness-kind>:<profile-key>` locator,
domain/principal/harness/current-credential binding, harness kind/profile,
state, adapter generation, mailbox cursor, optional capability-root digest and
process measurement, state reason, revision, and timestamps. The locator is not
caller identity or authority; the service resolves it to the unique durable row
and rechecks the exact current verified-human harness credential.

`endpoint_lifecycle` permits only
`ready_to_connect`, `waiting_for_approval`, `enrolled`, `access_ready`,
`restart_required`, `connected`, or `blocked`. `(domain_id,harness_id)` is the
primary key and `(domain_id,harness_kind,profile_key)` is unique. Registration
rejects kind/profile conflicts and creates `access_ready` only from a current
`VerifiedActor`. Activation is revision-fenced and returns
`restart_required`. Recording connection requires the same exact actor,
expected adapter generation, and a new 64-hex process measurement; it never
signals the harness. A connected endpoint presented by a different process
instance is rebound only through the audited
`endpoint.lifecycle.process_reconnected` transition under its own verified
harness actor and expected generation; the measurement is the exact framed
platform/account/pid/start-time/executable digest, and an executable-only
measurement never proves instance identity. Reconciliation may only preserve
current state or narrow it to `blocked`.

`ClientSetupResult`, `ClientSetupState`, `ClientIdentityProfile`, and
`EnrollmentProgress` are strict Pydantic models. Setup-specific persistence is
only `agentnet.client-setup-continuation.v1`, containing one bounded opaque
`SecretStr` under owner-private, no-follow, atomic file custody. Existing
identity profiles are re-read from current credentials. Multiple matching
profiles, a completed enrollment for another harness, lost pending
continuation, stale generation/revision, or unavailable current authority fail
closed. `agentnet setup`, `setup status`, and `setup continue` never grant
scope, start a Manager, or restart a harness.

Friendly resolution returns one strict `ResolvedEndpoint`: exact harness ID,
safe display metadata, and the current scope ID. Zero, ambiguous, stale,
revoked, unauthorized, and cross-domain results share a generic
non-enumerating failure. Canonical send freezes that endpoint/scope; the
dispatcher re-requires the scope against exact recipients and classification,
and `/v1/messages` rechecks it at Core. Explicit harness IDs must infer exactly
one unambiguous current scope. The signed actor provider, not payload, supplies
caller identity. Public receipts accept only authoritative acceptance fields
and add proof-derived exact recipient IDs/safe metadata; unknown internal or
projection-owned fields are rejected. An allowed offline send remains queued
for its original exact harness.

## Approval configuration, storage, and HTTP contract

`ApprovalServiceConfig` is strict (`extra="forbid"`, schema `1.0`) and binds
one canonical HTTPS `public_origin`, an exactly matching `rp_id`, `verifier_id`,
bounded TTL/body sizes, absolute owner-only storage/key paths, and one or more
unique approvers. Ordinary requests retain the five-minute default and ceiling;
only `authorization.communication_scope.approve` has the fixed one-hour
request ceiling required by the persistent communication transaction. Configured
purposes must collectively cover all six mandatory approval consumers, and
every approver must cover enrollment. Signer private keys remain file
references; load verifies each key's configured thumbprint.

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
  expiry, cumulative retrieval-failure/legacy-code-rotation budgets, optional
  encrypted possession-hash binding, and exact owner browser session bound to an
  assertion challenge;
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
only legacy capability routes. The four internal broker routes mount only when
both the runtime credential and stable owner-OIDC session service exist; a
fragment-based legacy profile gets no broker surface.

| Route | Input/effect |
|---|---|
| `GET /approval` / `GET /approval.js` | no-store CSP-constrained UI; stable mode uses authenticated owner session, lab mode reads then removes fragment capability |
| `GET /v1/approval/owner/session` | stable preauth/session state; preauth cookie is Secure/HttpOnly/SameSite=Lax for the cross-site OIDC callback, while authenticated session and CSRF cookies remain SameSite=Strict |
| `POST /v1/approval/owner/oidc/start` / `GET .../callback` | exact preauth+CSRF+PKCE/state/nonce login; decoded names must be globally unique; strict success/error projections ignore only unique unknown OAuth extensions; provider error terminally fails the matching browser-bound transaction without token exchange; success atomically claims/consumes and rotates to a pinned owner session |
| `POST /v1/approval/owner/registration/begin` / `.../complete` | session-bound, isolated WebAuthn UV registration with cumulative owner budgets |
| `GET /v1/approval/owner/requests` | exact owner/domain pending and approved-unretrieved requests; no capability, receipt, transaction, or claim code |
| `POST /v1/approval/owner/requests/options` | exact owner session/request selection; server resolves encrypted capability and returns a purpose-specific bounded summary plus session-bound assertion options |
| `POST /v1/approval/owner/requests/complete` / `.../reject` | exact session+CSRF request action; completion requires assertion challenge bound to that active session; possession-bound completion returns only `waiting_agent`/`retrieved` and no claim code |
| `POST /v1/approval/owner/requests/regenerate-code` | legacy-only; possession-bound requests deny regeneration; otherwise rotates only a current, unretrieved, nonterminal code within cumulative limits and receipt expiry |
| `POST /v1/approval/registration/options` / `.../verify` | lab-only fragment registration flow |
| `POST /v1/approval/requests/options` / `.../verify` / `.../reject` | lab-only exact fragment request flow |
| `POST /v1/approval/internal/readiness` | runtime Bearer plus purpose-specific signed one-use broker proof; non-mutating proof of the configured public reverse-proxy path and broker authentication |
| `POST /v1/approval/internal/requests` | runtime Bearer plus signed one-use broker proof; strict v2 binds exact SHA-256 possession hash, idempotent Core request, and challenge expiry; never returns approval URL or claim code |
| `POST /v1/approval/internal/requests/status` | runtime Bearer plus signed one-use broker proof; request/digest status |
| `POST /v1/approval/internal/receipts/retrieve` | runtime Bearer plus signed one-use broker proof and exactly one possession secret or legacy claim code, with exact domain/purpose/digest binding; signed canonical body hashes possession secret; exact retry returns same receipt and conflicting digest fails |

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
`c0_pilot_facts`. Core migration 5 adds recoverable OIDC-begin idempotency
(hash-only public key, exact request digest, and encrypted exact response) plus
`credential_renewal_requests` for selector-free finite server-credential renewal.
Core migration 6 adds `communication_scopes` and
`communication_scope_items`. The parent row binds the exact owner/fresh
harnesses, issuance credentials and epochs, current domain revocation/policy
epochs, canonical preimage and Approval transaction digests, hash-only
idempotency keys, encrypted responses, and terminal/audit state. The child
table contains exactly 38 rows: each of 19 fixed actions for each exact harness,
with `resource_pattern='*'`, `expires_at=NULL`, and an entitlement foreign key.
The scope and all items commit or roll back together.
The plan binds one exact principal, two distinct enrolled
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
| `GET /activate` | unauthenticated, world-reachable fixed no-store Core entry page for human-reviewed headless server activation; page body contains no transaction, state, authorization URL, code, receipt, identity, or secret and its only action targets fixed internal activation route |
| `GET /v1/enrollment/oidc/activate` | unauthenticated, rate-limited route that selects exactly one waiting nonexpired `remote_browser` transaction from encrypted Core custody and 303-redirects to OIDC authorization URL; redirect necessarily publishes OIDC `state`, `nonce`, and PKCE `code_challenge` to browser/provider but never verifier, continuation, receipt, possession secret, or authority; zero/multiple/local requests deny and callback requires exact server-staged approved owner identity |
| `POST /v1/enrollment/oidc/begin` | candidate key/harness metadata, exact 32-byte base64url idempotency key, plus optional exact `local_browser|remote_browser`; atomically creates OIDC transaction plus hash-only continuation and stores the encrypted exact response; same-key/same-request response-loss retry returns that winner, drift conflicts, and unrelated integrity faults propagate |
| `GET /v1/enrollment/oidc/callback` | one strict `code`+`state` success or `error`+`state` failure; duplicate/mixed/orphan recognized shapes deny, unique unrecognized OAuth extensions are ignored, bound errors terminally consume matching enrollment/recovery transaction without token exchange, and success atomically replaces remote activation custody with challenge custody before safe local HTML or fixed Approval redirect |
| `POST /v1/enrollment/oidc/poll` | transaction ID plus opaque continuation; bounded 2–10 second polling, Core-side approval staging, and `approval_ready` only after remote Approval is ready; 60-poll anti-abuse budget applies only to pre-callback `awaiting_oidc`, while callback/Approval stages remain rate-controlled until fresh challenge expiry |
| `POST /v1/enrollment/oidc/complete` | opaque Core continuation plus candidate PoP; Core retrieves receipt automatically with distinct transaction-derived Approval possession secret and atomically consumes enrollment challenge; no human claim code |

`POST /v1/credentials/current/renew` is selector-free signed renewal for the
current authenticated always-on binding. Its strict body contains only schema
plus UUID request ID. Before the six-hour window it returns the unchanged finite
expiry; inside the window it compare-and-swaps to `now + 24 hours`; exact retries
return the persisted result; expired, revoked, rotated, wrong-profile, or stale
bindings fail closed. `agentnet credential renew` stores the request ID in one
owner-only file before network and rotates it only after an exact response.

`agentnet server-agent reauthorize-expired-credential` uses strict local
`agentnet.managed-server-credential-reauthorization.v2` request state. The
signed owner-approval transaction binds the exact expired credential/epoch,
same P-256 key possession, managed config and identity digests, immutable C0
terminal credential/digest/epoch, prior canonical supersession-journal digest,
and a finite replacement TTL. Before PostgreSQL mutation it validates the full
prior journal and its exact audit records. The single transaction retires the
old row, inserts the next epoch for the unchanged key, and appends the exact
audit record. The canonical
`agentnet.c0-credential-supersession-journal.v1` chain then records that audit
hash and all bound inputs. Setup and Core accept a post-C0 current credential
only when this audited chain reaches it exactly. Exact same-request replay
reconciles; same ID with drift, stale/missing links, skipped epochs, changed
actor/key/files, or missing/conflicting audit rows fails closed.

The signed persistent communication-scope routes are:

| Route | Exact request/effect |
|---|---|
| `POST /v1/communication-scope/begin` | `agentnet.communication-scope.begin.v1`; caller supplies only a 16–256 byte retry key, while Core resolves the exact completed same-principal C0 harness pair, requires the authenticated ordinary server harness, accepts only its current active credential on the same harness lineage, and creates the one-hour Approval request |
| `POST /v1/communication-scope/status` | `agentnet.communication-scope.status.v1`; returns only caller-bound pending/ready/terminal state, approval URL/expiry when applicable, and `complete_automatically` only after issuance |
| `POST /v1/communication-scope/complete` | `agentnet.communication-scope.complete.v1`; possession-bound receipt retrieval and one atomic 38-item commit; exact retry returns the encrypted committed result |
Before `begin` resolves an active-scope conflict, Core atomically expires every
due pre-commit reservation for that principal and profile. A not-yet-due
reservation remains exclusive. Same-key retry of a newly expired reservation
returns terminal state without calling Approval again. CLI
`begin --replace-terminal-state` holds an owner-only cross-process state lock
from initial read through replacement begin and rotates both retry keys only
after exact Core `410` error-envelope proof with no extra fields.

The only success body is
`agentnet.communication-scope.complete-result.v1` with
`status=communication_active`, `authority_granted=true`,
`communication_usable=true`, `authority_expires_at=null`, and all artifact,
business-effect, federation, and public-A2A flags false. CLI persists retry
identity in an owner-only file and exposes the same flow as
`agentnet communication-scope begin|status|complete`.

The canonical local tool set additionally exposes
`agentnet.room.create|member.add|get|send`; together with the existing
`agentnet.send`, inbox/acknowledgement, conversation, and obligation methods,
this is the complete communication surface shared by MCP, Pi direct binding,
and the laptop Manager gateway. Local arguments remain strict and contain no
caller identity or reusable remote credential.

The signed selector-free C0 routes are:

| Route | Exact request/effect |
|---|---|
| `POST /v1/c0-pilot/readiness` | body is only `agentnet.c0-pilot.readiness.v1`; exact current managed owner binding receives only `ready|waiting_plan`, before any plan exists |
| `POST /v1/c0-pilot/start` | body is only `agentnet.c0-pilot.start.v1`; exact fresh actor starts or reconstructs the fixed request phase |
| `POST /v1/c0-pilot/respond` | body is only `agentnet.c0-pilot.respond.v1`; exact owner actor performs the no-model request ACK/fixed reply phase |
| `POST /v1/c0-pilot/complete` | body is only `agentnet.c0-pilot.complete.v1`; exact fresh actor verifies reply ACK plus seven facts and atomically cleans up five communication powers |
| `POST /v1/c0-pilot/status` | body is only `agentnet.c0-pilot.status.v1`; either exact planned actor receives one sanitized stage after current-binding checks |

Bodies are canonical JSON with duplicate keys, unknown fields, non-finite values,
and caller selectors rejected. Every response is non-cacheable. Generic integrity
conflicts are non-retryable; only typed transaction races return retryable 409.
CLI exposes `agentnet c0-pilot start|status|complete`; package setup owns the
isolated owner side through `agentnet c0-pilot responder --config ...
--credential ... --check|--run` under the dedicated `agentnet-c0` identity and
the internal respond route. Generic supervisor configuration cannot select this
mode. Neither surface prints protected identifiers or evidence.

Receipt bytes never appear in candidate responses. Core reserves an exact
completion digest around PoP and possession-bound retrieval. Exact retry
returns the encrypted result; a crash after enrollment commit reconstructs the
same deterministic harness/credential binding from authoritative rows.
Enrollment creates no entitlement.

`join guided` exposes `--browser system|terminal|remote`; `system` remains fresh-
laptop default. `remote` is server-only, opens/discloses nothing, marks only the
purpose-encrypted pre-callback continuation, and requires fixed public `/activate`.
Marker is replaced by challenge custody after callback and never enters identity,
approval, or public audit schemas. New pending local state uses
`agentnet.guided-join.v3` to bind exact identity path, browser mode, and the
precommitted begin idempotency key in addition to server/domain/harness/name and
candidate key. It is written with `authorization=null` before network I/O;
response-loss retry reuses the same key and exact encrypted Core response.
Legacy v1/v2 remain resumable but are not eligible for v3 begin recovery.
Exact nonterminal owner-only state resumes with the same command.
`--replace-terminal-state` first polls Core with the stored
continuation and proceeds only for `expired|failed`; it refuses absent, completed,
malformed, argument-drifted, or nonterminal state, reuses the candidate key, and
stores the next begin key before network and then atomically stores the returned
OIDC authorization before continuing. A lost begin response returns the exact
Core winner instead of creating an orphan transaction. `terminal` remains
explicit POSIX compatibility, not ordinary onboarding. `approval open` and
`approval watch --open` retain their separate presentation options. Browser mode
and terminal recovery create no identity or authority.

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
migration 4 is the bounded C0 bootstrap-plan contract; migration 5 is the OIDC
begin/credential-renewal lifecycle. SQLite permits only the exact N/N-1 v4→v5
Core upgrade in one transaction; PostgreSQL requires the same contiguous
checksums plus an exact live table/column/default/constraint/index catalog before
v4 migration, after migration, and on v5 open. Unknown, future, prototype, altered, partial,
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
--collaboration-scope-id SCOPE_ID --envelope-digest DIGEST`; `message send`,
`message inbox`, and `message acknowledge` all require the exact collaboration
scope and bind it into the signed request. Credential-free local bindings expose
the same operation as `agentnet.inbox.acknowledge`; MCP and Pi render it as
`agentnet_inbox_acknowledge`. All local-binding argument schemas omit identity
and recipient fields.

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

## Cross-domain server relay packet and target-scope interface

`RelayPacket` is strict `agentnet.server-relay.packet.v2` and its signature
purpose is exactly `agentnet.server-relay.packet.v2`. The signed fields require
`target_collaboration_scope_id` in addition to the existing exact source event,
endpoint, peer-key epoch, target recipient/grant, guest-pairwise subject,
ciphertext, and lifetime bindings. The source staging authority request digest
also includes that target scope ID. The sender carries it only as an opaque
host-issued identifier: it does not resolve, infer, substitute, or widen target
authority. Packet v1 and a v2 packet with an omitted scope ID fail strict
validation; the v0.1.45 relay path has no upgrade, fallback, alias, or
mixed-version signature mode.

The target derives the local event ID as
`UUIDv5(NAMESPACE_URL, "agentnet:server-relay:" + packet_id)`, resolves the
transport-derived host-local guest, and requires the mailbox's current
`CollaborationScopeService` under that guest and the packet's exact recipient
and classification. A message resolves `message.send` on
`conversation:<source-conversation-id-or-direct>`; a task resolves
`task.propose` on `task:<derived-local-event-id>`. The separately mandatory
target grant/business-policy decision remains `message.send` on
`recipient:<target-recipient-id>` with the exact relay input and mailbox output
sinks. Relay tasks pass the same target scope ID in `AssignmentRequest`; an
automatic queue transition still requires `task.accept` on the exact local task
resource.

Source-domain `authorization_context` is tainted input at the target boundary.
The target removes it, inserts only the resolved target scope's immutable
`authorization_context()`, and recomputes both local payload and envelope
digests. The signed source packet/event digests remain provenance facts, not
local authority. Canonical packet JSON is persisted byte-for-byte in both
outbox and inbox; an exact replay returns prior custody without another grant
use, while the same packet ID with different bytes conflicts.

## Public invitation browser continuation

The public invitation page is rendered under an opaque-origin CSP sandbox.
Its empty `POST /join/{opaque_token}/continue` therefore accepts only the exact
configured HTTPS origin or the browser-generated `Origin: null`; every other
origin, host mismatch, non-form encoding, or non-empty form fails with the same
non-enumerating unavailable response. The high-entropy one-use token is never
rendered as page text, and continuation still requires the verified work
account, exact candidate credential, independent approval, and current scoped
admission before returning `restart_required`.

## Schema evolution

The handshake selects the highest mutually allowed protocol/schema profile.
The supported window is N/N-1 only after explicit migration tests. Expansions
precede backfill/verification and contraction. Revocation/security state never
rolls back. Unsupported events remain queued or receive a typed rejection;
intermediaries never strip unknown signed fields.

AgentNet's immutable first-release baseline is migration 1 and already includes
the response-obligation, relationship-governance, and policy-exception tables.
Migration 2 adds protected task-payload release, migration 3 adds guided OIDC
enrollment continuation, migration 4 adds the bounded C0 bootstrap-plan
contract, migration 5 adds OIDC-begin replay recovery plus finite
current-credential renewal custody, and migration 6 adds persistent
same-principal communication scope plus private administration. Migration 7,
`communication_collaboration_release`, adds `endpoint_lifecycle`,
`collaboration_scopes`, `collaboration_scope_members`, `artifact_transfers`,
`artifact_transfer_recipients`, `invitation_links`, and
`invitation_link_failures`. Fresh SQLite and PostgreSQL stores initialize the
same complete v7 catalog. Startup fails closed on a missing or altered
migration, table, index, trigger/constraint, noncontiguous history, future
version, or unsupported older version. The only current N/N-1 Core migration is
an exact catalog/checksum-verified v6→v7 transition.

No pre-release or differently named database is accepted as an authority
source, and no unilateral relationship can be converted into consent. Import
requires a reviewed non-authority export into a fresh current store followed by
fresh exact bilateral approval. General rollback may restore only an exact
verified, current-compatible backup; it cannot downgrade a committed schema or
infer, preserve, or reactivate authority from unsupported bytes. The
`0.1.44→0.1.45` ordinary-server journal is narrower: before target-marker commit
it may restore its exact unchanged schema-v6 source after proving every
journal-selected protected relation digest and candidate artifact still
matches. Process interruption retains the journal for exact resume. Target
commit closes that downgrade path.

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

Every invocation emits `agentnet.server-setup.evidence.v1` with one typed status, effective artifact mode, aggregate request digest, fixed profile, package version, managed units, loopback ports, HTTPS topology, ordered step results, PostgreSQL prerequisite manifest, next action, and explicit `identity_enrolled`, `authority_granted`, and `production_durability_proven` claims. Request-v1 uses `agentnet.server-setup.approval-digest.v2`; request-v2 uses `agentnet.server-setup.approval-digest.v3`, binding explicit artifact mode and only the mode-applicable input set. Both bind exact request-file bytes, canonical absolute references, non-secret JSON fingerprints, environment variable-name sets, canonical Node/uv/AgentNet paths plus stable no-follow executable SHA-256 identities, and one hash computed from deterministic path/type/size/content records for the full root-owned package tree executed by `uv run --project`. Environment values remain reference-only and are never hashed or emitted. Privileged npm launcher independently reproduces the version-selected digest before creating Python runtime state; Python repeats full preflight under setup lock. Both implementations take two bounded descriptor snapshots for every privileged setup input, accumulate partial reads, compare exact content, and retain metadata/path custody checks; mismatch or read failure blocks even on coarse-timestamp filesystems. Blocked output contains bounded blocker/message and all three claims false. Environment values, credentials, signer private material, approval receipts, claim codes, identity profiles, and raw product stderr are never output.

Plan performs no privileged or managed-host mutation: no service identities, AgentNet roots, secret copies, database schema/config, units, or services. The npm launcher may materialize its caller-owned Python runtime before Python preflight. Apply requires root, exact frozen-plan digest, and nonblocking host lock. AgentNet, Node.js, and `uv` come only from current system-wide installed context, never request JSON. Canonical runtime under `/root`, `/home`, or `/run/user` is rejected because hardened units use `ProtectHome=true`. Target coding agent must establish root-owned non-writable service-readable lineage before invoking package code as root because untrusted code cannot self-validate safely; launcher and Python package-tree checks are defense in depth.

After fixed Core account exists, apply requires two bounded read-only PostgreSQL probes before AgentNet environment/config/database write: exact service-identity socket connection proving current role/database and writable-primary state, then parsed current-file HBA/ident inspection proving exact unshadowed `local agentnet agentnet peer` with no map or parse error, plus `pg_conf_load_time()` freshness against both auth-file modification times. Failure emits `status=blocked` with `blocker=postgres_auth_not_ready`; PostgreSQL administration/reload remains external and separately approved. After that gate and before any Approval/Core product subprocess, setup uses `lstat` to reject symlink, dangling-symlink, nonregular, ownership, or mode conflicts at fixed config/state/data child paths. Existing and newly realized service-owned state trees are recursively custody-checked before later product writes, unit commit, or marker commit.

`agentnet server-agent reset --retain-external-prerequisites --confirm-package-state-removal` is the explicit destructive recovery surface. Both flags are required. It creates/acquires and validates the permanent root-only setup lock before inventory; any managed deployment state without a pre-existing package lock fails custody; exact clean-host retry may create the lock/root. It requires exact owner/group/mode/type/link custody for managed roots and files; stops/disables the renewal timer, isolated C0 responder, static renewal oneshot, Core, and Approval in dependency order; proves all five managed units inactive; rejects unexpected secret-root entries; and requires symlink-attack-resistant recursive removal. It removes only allowlisted package deployment units/state while preserving coordination lock/root, then reloads systemd even on exact retry after prior response loss. It emits `agentnet.server-setup.reset-evidence.v1`; exact retry reports `already_absent` and proves deployment-path absence, not lock/root absence. PostgreSQL, runtimes, installed package, proxy/TLS/DNS/firewall, operator inputs, and locked `agentnet`, `agentnet-approval`, and `agentnet-c0` service identities are retained. Exact AgentNet database/role reinitialization is a separate destructive operator boundary requiring sanitized target inventory, explicit named approval, an explicit backup/rollback decision, and redacted audit evidence; unrelated/shared/valuable targets fail closed. Reset grants no authority, enrolls no identity, proves no durability, cannot remove external prerequisites, and is not a secret-rotation path.

Apply reruns bootstrap and validates realized Core/Approval state; marker never skips it. Request-v1 writes root-only marker-v2, retaining original meaning and same-request v1→v2 migration. Request-v2 writes marker-v3, which additionally binds exact `artifact_mode`; marker-v1/v2 cannot satisfy request-v2. Both bind request, package, units, non-secret Approval/Core config digests, revision, and previous-marker digest while excluding only offline-activation enrollment labels. Marker replacement uses exact prior-byte compare-and-swap under setup lock. Marker is provenance, not health/readiness/identity/authority/durability evidence. Existing Approval policy must match exact owner OIDC and complete approver set—extra trust anchors block. Start is valid only with apply. It converges five package units: Approval and Core; an isolated `agentnet-c0` responder using systemd credential delivery; a static selector-free credential-renewal oneshot; and its hourly persistent timer. Pre-enrollment start proves responder/timer disabled and renewal oneshot inactive. Activated start rejects duplicate/non-finite managed identity-profile JSON members, strictly validates the canonical managed identity actor and current binding labels plus private-key custody/readability; database credential-to-key binding remains activation-owned. Setup evidence `identity_enrolled=true` means only that this profile/label/key validation passed—it is not database binding, authority, or business-effect proof. It starts the timer and nonterminal responder only after Core, and never recreates responder config after a signed terminal status has produced the exact owner-only terminal marker. Core configured readiness also requires a signed non-mutating Approval broker-readiness request through the configured public origin; generic health cannot substitute. That request uses explicit trust visible to CPython `ssl.create_default_context()` with certificate and hostname verification, rejects ambient `SSL_CERT_FILE`/`SSL_CERT_DIR`/`SSLKEYLOGFILE` before setup, disables HTTPX environment routing, and maps TLS setup/transport failure to sanitized broker blockers. Core health/readiness bind exact artifact mode and capability set; Approval health returns `agentnet.approval.health.v1`. Setup validates exact service role, version, origin/domain/profile/runtime identity and rejects generic HTTP 200. `operational` requires exact enrolled binding, `credential_state=current|renewal_needed`, broker readiness, and public Core readiness; pre-enrollment start reports `waiting_owner_oidc_or_passkey`. A current-package owner-only attempt marker permits interruption recovery, while pre-existing package state without exact current attempt/marker custody fails `clean_state_required`; 0.1.31 state is not migrated in the first-C0 path.

For `0.1.44→0.1.45` only, setup uses
`agentnet.server-setup.upgrade-journal.v4`. It binds the exact source marker and
request, destination request, source/target versions, all managed unit and Core
configuration bytes, exact systemd state, schema-v6 migration catalog,
active server identity/credential/profile, protected relation digests, the
exact committed v6 communication-scope image the migration must reproduce, and
mailbox cursor. Migration creates the exact schema-v7 catalog plus one
`restart_required` endpoint row at adapter generation 1, preserves that cursor,
and maps every committed v6 communication scope to one active v7 collaboration
scope. Rollback safety therefore admits exactly that migrated authority — one
`migrated_v6_communication_scope` scope per source scope with its exact owner
and member harnesses — and still requires every other release table to be
empty; an extra scope, missing scope, foreign member, changed role, or a
journal without the migrated-authority expectation fails closed. A caught
pre-commit failure rolls back only after proving unchanged candidate state;
interruption retains the journal for exact resume; uncertainty returns
`setup_upgrade_conflict`. Exact target-marker realization clears the journal,
and there is no automatic committed-target downgrade.

For `0.1.45→0.1.46`, the same v4 journal binds the exact source and target
package state, but no database migration or endpoint transition is permitted.
Setup must preserve schema v7 and all enrolled identity, credential,
authorization, mailbox, and endpoint rows while replacing the exact managed
renewal timer bytes. Before invoking candidate package code, setup quiesces all
managed services. Each target unit binds a package-generation runtime beneath
its service account's private data root; setup materializes and verifies that
runtime as the exact account before Approval validation or service restart, so
the released runtime remains intact for bounded pre-commit rollback. The target
timer uses `OnActiveSec=5min` and `OnUnitInactiveSec=1h`, forbids `OnBootSec`,
`OnUnitActiveSec`, and `Persistent`, and must expose a finite future activation
when setup reports operational.

For `0.1.47→0.1.48`, setup accepts only the exact five-unit `0.1.47`
marker and uses the same forward-only journal without database or endpoint
migration. Completed-C0 terminal-credential lookup follows the canonical
foreign-key path `credentials.harness_id → harnesses.harness_id`; domain and
principal constraints apply to the joined harness while epoch remains on the
credential. Any missing or mismatched binding returns no credential and blocks
Core readiness.

For `0.1.48→0.1.49`, setup accepts only the exact five-unit `0.1.48`
marker and uses the same forward-only journal without database or endpoint
migration. Permanent communication scope resolution selects the exact completed
C0 pair, then may substitute only the authenticated ordinary server harness's
current active credential on the same harness lineage. Remote message, inbox,
and acknowledgement requests carry `collaboration_scope_id` inside the signed
request target or body.

For v0.1.50, setup evidence adds `public_core_origin` and the exact
package-pinned `laptop_join_command` to a successful plan. The CLI adds
`phase` and `setup_elapsed_seconds` to terminal JSON. A blocked server command
also returns `responsible_component`, `safe_action`, `rerun_resumes`, and
`human_action`; these fields describe recovery only and never grant authority.
Content-free phase/action/elapsed progress is written to stderr.

The no-argument guided server surface reads the same strict request schema from
`/var/lib/agentnet-setup/server-setup.json`; the `--request` plus
`--expected-request-digest` interface remains the automation contract. Exact
five-unit schema-v7 markers from v0.1.45 through v0.1.49 are distinct
allowlisted forward-only sources to v0.1.50. They use journal v4 and do not
imply an intermediate package install or database migration.

Schema v7 and the lifecycle journal are implementation mechanisms only. Signed
installer/update evidence, hostile-host qualification, independent approval,
HA/restore, production durability, and every other production/high-tier gate
remain blocked until their separate evidence is accepted.
