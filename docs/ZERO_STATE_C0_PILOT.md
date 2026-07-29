# AgentNet zero-business-state → two-harness C0 architecture

Status: owner-approved target architecture. Repository implementation S0–S9 is authorized; npm publication, server deployment/restarts, live passkey/enrollment/grant/message actions, and A2A cutover remain unauthorized.

This document intentionally describes required behavior that AgentNet 0.1.18 does not yet implement. It is not a description of current runtime behavior. Current-source deltas are explicit in §15.

### Execution-stage map

These labels describe the approved implementation plan; they do not change product requirements or prove release/live status.

| Stages | Scope | Current authority/status |
| --- | --- | --- |
| S0–S1 | freeze contract; write failing contracts and migrations | repository candidate completed |
| S2–S3 | stable owner OIDC/passkey flow; two guided identity-only enrollments | repository candidate completed |
| S4–S5 | atomic bounded `BootstrapGrantPlan`; deterministic responder and seven-fact verifier | repository candidate completed |
| S6 | adversarial security/recovery matrix | repository candidate completed |
| S7 | authoritative docs, schemas, operator skill, traceability, and evidence truth | in progress; H/L candidate only |
| S8 | full validation and independent exact-worktree review | approved repository work, pending |
| S9 | prepare immutable release, commit/tag/push, then stop before npm publication | approved repository work, pending |
| S10 | Sergey-only npm publication and independent public-artifact verification | not authorized yet |
| S11–S12 | separate deployment/restart approval, atomic deployment, zero-state verification | not authorized yet |
| S13 | live owner OIDC/passkey/enrollment/C0 ceremony | not authorized yet |
| S14 | final evidence reconciliation; no A2A cutover | not authorized yet |

## 1. Scope and fixed assumptions

- Existing server runs verified AgentNet Core, PostgreSQL, Approval, OIDC callback, and public TLS routes, but business state starts with zero AgentNet passkeys, principals, harnesses, entitlements, messages, rooms, files, or effects.
- Existing A2A, Pi Hub, PostgreSQL service, cloudflared, and non-AgentNet data remain untouched.
- The C0 peers are the ordinary Hub-hosted human harness `H_owner` and the genuinely fresh-laptop harness `H_fresh`. `H_owner` is an ordinary AgentNet counterparty colocated on the existing server; it does not reuse Pi Hub/A2A identity and receives no Hub/root privilege.
- Both harnesses authenticate as the same exact human principal `P = (domain, OIDC issuer, OIDC subject)`. Distinct-principal messaging is outside this pilot.
- One public, non-secret prompt goes to the fresh laptop. Human browser/passkey actions remain explicit; no completion value is copied.
- Installation creates code only. Each enrollment creates identity only. Positive authority requires a later exact WebAuthn-approved plan.
- Colocated Core/PostgreSQL/Approval remains an ordinary profile and reports `independent_boundary_proven=false`.
- No production, HA, federation, files/scanner, A2A-conformance, or cutover claim.

### Pre-first-test clean-state rule

Until the first real two-harness C0 journey succeeds, backward compatibility must not delay or alter this pilot. Do not add N-1 config migration, legacy aliases/defaults, fallback authority paths, old-schema preservation, or old AgentNet state reuse. After the exact current artifact passes independent verification, the deployment owner removes prior AgentNet runtime/config/database/schema/data/credential/enrollment/authority/message state and initializes the current Core/Approval schema and config from zero. Source history, release evidence, external host/PostgreSQL/DNS/tunnel/OIDC infrastructure, unrelated services, and existing A2A remain intact. Any later backward-compatibility work requires successful pilot evidence and separate owner direction.

## 2. Non-negotiable invariants

1. OIDC login authenticates a browser/person; it never approves enrollment or authority.
2. WebAuthn UV approves one canonical transaction and purpose; browser session, claim code, receipt possession, HTTP status, or A2A state never substitutes for approval.
3. The enrolling harness never receives an Approval capability URL or signed approval receipt.
4. Candidate private keys stay local to their exact harness. Candidate proof of possession is mandatory.
5. Enrollment ends `enrolled_identity_only` and grants no positive authority.
6. Positive permission attaches to principal `P` per AUTH-003; exact harness attribution, credential epoch, eligibility, and revocation still attenuate every action per ID-006/007 and AUTH-004.
7. First authority contains no wildcard and no `authorization.entitlement.issue`.
8. Core commits the whole first-authority plan in one Core database transaction or commits nothing.
9. Exact retry returns the stored committed result; a drifted retry fails closed.
10. Public/model-visible output never contains credentials, bearer capabilities, cookies, OAuth state, continuation values, claim codes, nonces, proof values, approval receipts, identities, event IDs, envelope digests, payloads, or private URLs. Detailed evidence stays in protected audit/evidence storage.
11. `COMPLETED_C0_ROUND_TRIP` requires application facts owned by their issuers; transport acceptance alone never counts.

## 3. Actors and trust boundaries

- **Owner human:** controls pinned workforce/OIDC account and UV passkey.
- **Owner browser/authenticator:** separate human-confirmation surface not readable by either enrolling harness.
- **`H_owner`:** ordinary Hub-hosted human harness and explicit C0 counterparty; never Pi Hub/A2A identity or a privileged Hub/root.
- **`H_fresh`:** ordinary fresh-laptop harness driven by one public prompt.
- **Core:** authoritative identity, policy, bootstrap-plan, mailbox, idempotency, and audit owner.
- **Approval:** purpose-limited WebAuthn verifier and receipt signer under a separate service/OS identity. Colocation does not prove independent administration.
- **PostgreSQL:** durable Core transaction store. Durability does not imply HA.
- **A2A/Hub:** outside this ceremony and unchanged.

## 4. Approval/Core receipt boundary

Approval and Core do not share a distributed transaction.

Approval signs a receipt bound to exact purpose, domain, canonical transaction digest, approver, ceremony, RP/origin facts, issue/expiry, and receipt ID. That receipt is evidence, not business authority.

Core uses existing broker protections: authenticated Core→Approval channel, canonical body, signed broker proof, fresh one-use broker nonce, exact request/retrieval digest, replay custody, and strict status/retrieve semantics. Initiating process retains private Core continuation/begin state. Core derives a transaction-specific OIDC Approval possession secret or generates and encrypts a distinct high-entropy bootstrap-plan secret, sends only that secret's SHA-256 hash to Approval, and later retrieves with exact purpose-separated secret. Signed canonical retrieval hashes supplied secret; browser, model, logs, and audit details never receive it. Target plan completion persists a completion-request digest before retrieval. Exact retrieval may repeat with same possession secret and retrieval digest while valid; wrong secret consumes cumulative attempt budget and conflicting retrieval fails closed.

Core consumes verified receipt and writes identity/entitlements/result/audit in one Core transaction. Crash before Core commit produces no business state. Crash after commit returns stored result on exact retry. If Core loses encrypted purpose-separated possession custody before commit, initiating process begins a fresh approval after expiry; no human-visible regeneration or recovery from logs/plaintext exists. Legacy claim-code regeneration remains compatibility-only.

## 5. Phase A — first owner passkey without a capability URL

1. Owner opens a stable public Approval origin containing no bearer value. This replaces 0.1.18 registration behavior, which requires a fragment capability and prints the generated registration URL.
2. Approval starts OIDC Authorization Code + PKCE using server-side pre-auth state, exact `state`, `nonce`, issuer, callback, and redirect URI.
3. Bootstrap eligibility is not first-login-wins. Server policy contains either:
   - exact approved issuer+subject; or
   - approved verified-email alias. First successful `email_verified=true` match pins exact issuer+subject permanently before passkey registration.
4. Callback rotates to a fresh server-side browser session referenced by a `__Host-`, Secure, HttpOnly, SameSite=Strict cookie. The cookie is a browser-confined bearer capability but is never exposed outside the browser/server channel.
5. Session binds owner identity, domain, ceremony, CSRF secret, RP ID/origin, creation/expiry, and one pending registration transaction.
6. WebAuthn registration requires exact RP/origin and UV. Approval records audit before credential activation.

Registration states:

`pending_oidc → authenticated → options_issued → credential_registered`

Terminal states:

`canceled | rejected | expired | failed`

Multiple tabs use separate challenge rows or return `ceremony_already_active`; they never overwrite a live challenge. Reopening an approved possession-bound ceremony reports `waiting_agent`/`retrieved` without rotating or displaying any value. Legacy explicit regeneration requires owner authentication, preserves cumulative failure/rotation limits, and never redisplays stored plaintext because none exists.

Result: one Approval-side passkey; still zero AgentNet principal, harness, or authority.

## 6. Phase B — Hub-hosted counterparty identity-only enrollment

1. `H_owner` generates its candidate key locally and starts guided enrollment.
2. OIDC resolves pinned principal `P`; candidate proves key possession.
3. Core creates canonical `identity.enrollment.approve` transaction bound to domain, principal, candidate key digest, harness metadata, policy revision, issue/expiry, and ceremony ID.
4. Core brokers request to Approval. Neither CLI nor model receives capability/receipt.
5. Approval renders plain-language summary from canonical bytes, with advanced digest hidden but available to the human locally.
6. Owner WebAuthn-approves exact transaction.
7. Approval returns only `waiting_agent`; browser displays automatic-completion status and no code/receipt.
8. Exact waiting process proves its private continuation secret through signed broker. Core retrieves receipt internally and atomically binds principal, `H_owner`, credential, epochs, approval consumption, stored result, and audit.

Exact retry after response loss returns the same result.

Result: `H_owner` is `enrolled_identity_only`; `authority_granted=false`.

## 7. Phase C — fresh-laptop one-prompt identity-only enrollment

The public prompt performs only public/reversible actions: exact package install/verification, local candidate-key creation, public metadata/preflight validation, and guided enrollment start. Prompt has no private URL, secret, receipt, claim code, identity, digest, or unresolved technical value.

Owner signs in and WebAuthn-approves exact fresh-laptop transaction. Exact waiting `H_fresh` process retrieves approval automatically using private continuation state; browser/human transfers no value. Core binds `H_fresh` to existing principal `P` and stores exact result as in Phase B.

Wrong issuer/subject/account/domain, missing verified alias, wrong candidate key, stale policy, revoked context, replay, expiry, RP/origin mismatch, route/config mismatch, or unknown critical field fails before authority.

Result: `H_owner` and `H_fresh` are active exact harnesses under `P`; positive authority remains zero.

## 8. Phase D — bounded first-positive-authority plan

Only a freshly enrolled identity-only harness may request predefined profile:

`ordinary-two-harness-c0:v1`

Caller cannot provide arbitrary actions, resources, peer names, entitlement IDs, or profile contents. Core resolves `H_owner` from the prepared owner-enrollment record and `H_fresh` from the current guided-enrollment record. Ambiguity fails; Core never chooses by display name, latest activity, hostname, prompt text, or caller-supplied peer.

Core builds canonical `BootstrapGrantPlan` containing:

- schema/profile version
- domain and principal
- exact `H_owner`/`H_fresh` harness IDs, credential IDs, credential epochs
- domain revocation epoch and policy revision
- exact ordered entitlement list
- deterministic plan and item IDs
- one idempotency key and canonical digest
- issue/expiry and one-use limit
- `independent_boundary_proven=false`

### Exact current-policy communication entitlements

Because both harnesses share principal `P`, first authority creates five communication entitlements:

1. `message.send` on resource `direct` for `P`.
2. `mailbox.read` on resource `H_owner` for `P`.
3. `mailbox.acknowledge` on resource `H_owner` for `P`.
4. `mailbox.read` on resource `H_fresh` for `P`.
5. `mailbox.acknowledge` on resource `H_fresh` for `P`.

This preserves current Core resource semantics and AUTH-003. Current entitlements alone do not bind direct peers, C0 classification, payload, event lineage, or use count. Therefore every plan-issued communication entitlement is also linked to a typed deny-only C0 plan guard. Normal principal entitlement authorization must succeed first; the guard can only narrow it. The guard binds exact `H_owner ↔ H_fresh` directions, classification C0, fixed harmless request/reply payload schema and digests, one send per direction, exact plan event lineage, mailbox filtering to those events, exact recipient ACK ownership, expiry, and current harness/credential epochs. Generic direct recipients, non-C0 bytes, extra sends, unrelated mailbox events, and unrelated acknowledgements fail closed. Adding another harness/principal or changing an epoch invalidates the active pilot guard and requires fresh approval.

### Exact revocation entitlements

For each deterministic communication entitlement ID, the plan creates one principal entitlement:

`authorization.entitlement.revoke` on `entitlement:<exact communication entitlement ID>`.

These five powers can only deny/revoke their exact planned communication entitlements. They cannot issue authority, revoke unrelated authority, elevate, cross domains, or act as wildcard. Their expiry is no earlier than the communication entitlement expiry; both use explicit pilot TTLs. Harness revocation still independently blocks that harness.

Total atomic entitlement rows: five communication + five exact revoke = ten. Matching plan/guard records commit in the same transaction; they add no positive authority.

No messaging-admin role, founder root, `authorization.entitlement.issue`, wildcard, relationship, task, file, room, federation, elevation, A2A-admission, or server-agent privilege is created.

### Plan approval

Plan states:

`pending_approval → approval_issued → completion_reserved → committed`

Terminal:

`rejected | canceled | expired | invalidated`

S4 makes Core broker `authorization.bootstrap_plan.approve` and removes the pre-S4 `authorization.entitlement.bootstrap.approve` purpose, wildcard founder root, and single-root path from the supported ordinary C0 profile. Core mounts only the bounded bootstrap-plan path. The stable authenticated Approval browser lists the pending plan without a secret URL and derives its summary from canonical bytes: two harness labels backed by local exact identity details, five communication powers, five exact revoke powers, TTL, assurance label, and explicit no-other-authority statement. The resulting guard remains `pending`; S5 alone may activate communication enforcement.

Owner WebAuthn-approves exact digest. Exact waiting `H_fresh` process completes automatically through possession-bound signed broker; browser/human transfers no value.

### Atomic commit and retry

Before retrieval Core reserves exact completion-request digest. Inside one Core transaction it revalidates principal, both harnesses/credentials/epochs, policy revision, domain revocation epoch, plan/digest/expiry, receipt purpose/expiry, audit availability, and absence of conflicting active ordinary-C0 plan authority. Then Core consumes receipt, inserts all ten deterministic entitlement rows plus exact deny-only guard records, stores committed result, marks plan committed, and appends audit.

- Crash/failure before commit: zero entitlement rows.
- Crash after commit: all ten rows and stored result.
- Same idempotency key + same digest: same stored result.
- Same key + different digest: security conflict.
- Different key while the same ordinary-C0 plan authority remains active: reject duplicate active plan.
- Renewal requires a fresh approved plan after prior planned authority is inactive.

Deterministic IDs derive from canonical plan digest plus fixed item ordinal. No random retry IDs and no global uniqueness index. No partial-ready or repair workflow.

## 9. Hub-hosted ordinary deterministic responder

Before the fresh prompt reaches C0 verification, `H_owner` starts a product-owned deterministic C0 responder through its normal supervisor lifecycle. It is an ordinary AgentNet harness process colocated on the Hub host, not Pi Hub/A2A identity or Hub/root. It waits without positive authority, then after plan commit reads only its own mailbox, acknowledges the exact event, and sends one fixed harmless C0 reply correlated to the original event. It performs no model inference, company-data access, file operation, task, external effect, or A2A action.

## 10. Phase E — C0 verifier

`H_fresh` uses stable per-attempt idempotency `K1`; `H_owner` uses stable correlated `K2`.

This is explicitly a same-principal, two-harness C0 transport/authority test—not a distinct-principal authorization test. Machine evidence requires seven facts:

1. Original C0 message accepted into PostgreSQL transaction-backed recipient custody, recorded honestly as `accepted_local`; this proves durable local commit/restart behavior, not replicated HA or `accepted_durable`.
2. `H_owner` retrieved exact original event.
3. `H_owner` acknowledged exact event ID + envelope digest (`recipient_committed`).
4. `H_owner` sent correlated fixed reply under `K2`.
5. Reply accepted into durable recipient custody.
6. `H_fresh` retrieved exact reply.
7. `H_fresh` acknowledged exact reply ID + envelope digest.

User-visible sequence remains:

`send → durable custody → recipient acknowledgement → reply → retrieval → final acknowledgement`

Only all seven machine facts produce `COMPLETED_C0_ROUND_TRIP`. Missing facts yield typed resumable status. Duplicate `K1`/`K2` never creates duplicate events/effects. Detailed identifiers, digests, receipts, and payloads remain protected in audit/evidence storage; public/model output shows only sanitized stage/result labels.

## 11. Recovery and fail-closed matrix

- **Wrong account/issuer/domain:** reject before ceremony; do not reveal whether another owner exists.
- **OIDC mix-up/login CSRF/session fixation:** exact issuer/state/nonce/PKCE/redirect checks; rotate server session at callback; CSRF token on state-changing browser calls.
- **Wrong RP/origin:** WebAuthn verification rejects.
- **Multiple tabs:** no challenge overwrite or possession-binding rotation on status refresh; cumulative attempt budget.
- **Cancel/reject:** terminal, audited, no business effect.
- **Expiry:** terminal; new ceremony gets new IDs/proof. No resurrection.
- **Possession secret lost before commit:** fresh approval after expiry; never recover from browser, human, logs, or plaintext storage.
- **Broker replay/response loss:** exact signed request/retrieval digest and replay custody; same retrieval repeats, conflicting retrieval fails.
- **Core restart before commit:** zero authority; resume exact reserved completion only if same locally retained possession state remains valid, otherwise fresh approval.
- **Core restart after commit:** return stored result.
- **Policy/credential/harness/domain epoch drift:** invalidate pending plan; post-commit policy evaluates current state on every action.
- **Audit outage:** block protected finalization/commit and never claim success.
- **Offline owner harness:** Core retains durable recipient custody; verifier reports queued/resumable.
- **Duplicate calls:** exact idempotency produces one event and one stored business result.
- **Guard escape attempt:** third recipient, non-C0 classification, changed payload, extra request/reply, unrelated mailbox event, or unrelated ACK fails before protected use.
- **Harness revocation:** revoked harness cannot authenticate/read/ack/send; sibling remains attributable and current if its own state is valid.
- **Entitlement revocation/expiry:** removes positive communication authority; exact revoke entitlement cannot affect unrelated records.
- **Approval-store loss:** pending ceremony expires; no receipt is reconstructed.
- **Backup/restore/PITR:** never counts as production proof; restored pre-consumption state must pass epoch/replay reconciliation before any protected effect.
- **Malicious enrolling harness:** cannot read browser session/passkey/receipt and cannot alter predefined plan.
- **Malicious Core/server root:** can deny service and, in colocated profile, may compromise several boundaries; system reports the assurance limitation and never claims independent-host protection.

## 12. Human journey

1. Owner opens stable Approval page, signs in, and registers passkey.
2. Headless owner/server harness stages remote guided enrollment; owner opens fixed public Core `/activate`, signs in, and passkey-approves; exact waiting process completes automatically.
3. Fresh laptop receives one public prompt and performs guided identity-only enrollment in its system browser; exact waiting process completes automatically.
4. Fresh wizard requests fixed C0 plan; owner passkey-approves and exact waiting process completes automatically.
5. Deterministic responder and verifier complete C0 automatically.
6. User sees sanitized status only; no technical values require copying.

This is one public prompt to the fresh laptop, not one total human approval. Independent approval remains explicit at every identity or privilege-expanding boundary.

## 13. Rejected options

- Printed/private `agcap1` URL or QR/private URL.
- Clipboard/chat/A2A/Slack/Telegram capability or receipt transfer.
- First-login-wins owner registration.
- Mandatory second human, second host, SaaS, named secret manager, or SSH-identity assumption.
- Server responder harness for this pilot.
- Hidden preauthorized recipient, loopback self-message, or one-way send called round trip.
- Wildcard/temporary founder authority.
- Caller-defined grant contents.
- Six duplicate communication entitlements for one principal.
- Random entitlement IDs or separate grant transactions.
- Global active-entitlement uniqueness index that blocks renewal.
- Partial grant commit plus repair machinery.
- Treating OIDC, browser session, transport ACK, receipt possession, or A2A state as approval/completion.

## 14. Deterministic test matrix

### Registration
- Approved owner subject/verified alias succeeds and pins exact issuer+subject.
- Wrong issuer/subject/email verification/state/nonce/PKCE/redirect/RP/origin/CSRF fails.
- Session rotates; fixed session cannot survive login.
- Two tabs do not overwrite challenge or reset attempt budget.
- Cancel/expiry and legacy regeneration obey terminal/cumulative limits.
- No response, logs, URL, process args, or public artifact contains capability/session/possession secret/code.

### Each guided enrollment
- Candidate proof of possession; exact OIDC principal; canonical approval purpose/digest.
- Possession mismatch/attempt/expiry/replay; broker replay; restart and response-loss convergence.
- Wrong account/domain/key/policy/epoch fails.
- Completion creates identity only; sibling harness attribution remains exact.

### Bootstrap plan
- Caller cannot alter profile, items, peer, IDs, TTL ceiling, or resources.
- Core rejects ambiguous/mismatched harness selection or added third identity.
- Exactly five communication + five exact revoke entitlements; no wildcard/issue authority.
- Guard tests reject third recipients, non-C0 classification, changed payload, extra sends, unrelated mailbox reads/events, and unrelated ACKs; guard cannot authorize without a current positive entitlement.
- Canonical browser summary matches transaction bytes.
- Crash injection before/between/after inserts yields zero or ten committed rows.
- Exact retry returns stored result; digest/key conflict fails.
- New key cannot duplicate active plan; fresh renewal works after inactivity.
- Policy/domain/harness/credential drift, cancel, rejection, expiry, audit outage fail closed.

### Revocation
- Each revoke power affects only its named communication entitlement.
- It cannot issue, elevate, revoke unrelated authority, cross domain, or wildcard.
- Harness revocation blocks only exact harness while sibling identity remains.

### C0
- Fixed harmless payload only; explicit C0 classification.
- Duplicate `K1`/`K2` creates one event each.
- Every missing/forged fact yields non-success.
- Wrong event, digest, harness, recipient, correlation, or envelope fails.
- Offline/restart resumes from durable facts.
- Only seven complete facts yield `COMPLETED_C0_ROUND_TRIP`.
- Public/model output contains no IDs, digests, identities, receipts, or payloads.

### Packaging/evidence
- Source, packed generation, and recursively repacked generation contain same implementation/tests.
- Linux/macOS/Windows package gates run.
- PostgreSQL 18 catalog verification reconciles every `contype='n'` row exactly against the already-required non-null column catalog, rejects unknown or malformed constraints, and compares CHECK expressions through bounded semantic parsing of both migration and server-rendered forms. Redundant parentheses and `BETWEEN` expansion may normalize; changed operators, literals, precedence, FK actions, or columns must fail closed.
- Hermetic/local results remain distinct from live OIDC/passkey/cross-device pilot evidence.

## 15. Required implementation delta from AgentNet 0.1.18

Owner approval and the separately approved implementation plan authorize repository implementation of these explicit deltas:

1. Replace fragment-capability first-passkey registration and printed `approval_url` output with the stable OIDC-authenticated Approval session in §5; add non-overwriting ceremony challenge storage.
2. Add plan-completion reservation before broker receipt retrieval. Current guided OIDC retrieval ordering remains source evidence but is not the target ordering for new bootstrap plans.
3. Replace the current one-row `authorization.entitlement.issue/*` founder path for this profile with `BootstrapGrantPlan`, new purpose/config allowlist, deterministic ten-row atomic commit, and stored-result replay. Existing founder tests must be replaced or retained only as explicitly disabled legacy-path tests; production profile must not fall back to wildcard bootstrap.
4. Redact current guided-enrollment and C0 human/model-visible CLI output. Protected local identity files and authenticated internal API records may retain exact IDs, but public/model-visible reports must not print principal IDs, harness IDs, credential IDs, event IDs, envelope digests, payloads, receipts, codes, or capabilities.
5. Add the deterministic ordinary Hub-hosted responder and C0 verifier without changing A2A or granting server/Hub privilege.
6. Replace ordinary human claim-code delivery with strict possession-bound internal request/retrieval v2. Add headless server `--browser remote`, fixed public Core `/activate`, exact-one remote selection, and automatic Approval-page waiting without exposing authorization URLs, possession state, codes, or receipts.

These deltas are requirements of the candidate architecture, not claims that 0.1.18 already satisfies it.

## 16. Success criteria and approval boundary

Architecture success means reviewers find no unresolved trust, authority, state, recovery, or usability contradiction for this bounded pilot.

Implementation success, only after separate approval, will require:

- exact source/tests/docs/package changes;
- recursive packaging and platform CI;
- immutable release/public-package verification;
- atomic Core/Approval deployment under separately approved restart scope;
- live zero-state passkey + two identity-only enrollments;
- exact atomic authority inventory;
- live `COMPLETED_C0_ROUND_TRIP` evidence;
- existing A2A unchanged.

Repository implementation, tests, documentation, release preparation, commit/tag/push, and CI are authorized under the approved implementation plan. npm publication, server deployment/restarts, live passkey/enrollment/grant/message actions, and A2A cutover remain separate explicit gates.
