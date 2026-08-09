# AgentNet canonical owner and communication-scope recovery design

**Date:** 2026-08-09
**Status:** Approved for implementation in the authenticated owner session; not production-certified
**Issue:** MEL-251

## 1. Purpose

AgentNet v0.1.50 can enroll an ordinary server agent yet leave two package-owned authority representations inconsistent:

1. Approval may remain bound to the pre-enrollment placeholder owner identifier while Core uses the canonical enrolled principal.
2. A communication scope created after the database reached schema v7 may commit only its legacy authority rows, without the collaboration-scope projection required by messaging.

The live server was repaired manually. This design replaces those interventions with one bounded, fail-closed package path. It preserves the enrolled server and laptop, passkey, active communication scope, messages, and receipts. It does not broaden communication authority or claim a production gate.

## 2. Requirements and trust boundaries

Affected requirements are ID-001, ID-002, ID-005, ID-006, AUTH-001, AUTH-002, AUTH-003, AUTH-004, AUTH-005, COM-001, COM-002, COM-009, AVL-003, AVL-005, SEC-003, SEC-005, SEC-007, OPS-003, and OPS-006.

The change crosses these boundaries:

- authenticated Core enrollment identity;
- Approval's owner binding, passkey custody, and receipt signer;
- Core's trusted approver configuration;
- SQLite Approval state;
- SQLite or PostgreSQL Core authority state;
- root-owned setup journal, setup marker, and managed service lifecycle.

Positive authority continues to come from the verified human principal. The setup process may reconcile already-approved deployment state, but it cannot invent a principal, OIDC binding, passkey, signer, scope, or entitlement.

## 3. Owner-decision boundary

PD-001 records OIDC issuer plus subject as the canonical principal source, but its previous bounded default excluded general migration and appeal semantics. On 2026-08-09 the accountable owner explicitly approved this design in the authenticated coding session.

That approval is limited to automatic reconciliation of the exact ordinary-onboarding placeholder with the exact enrolled canonical owner when all proof, row-count, domain, OIDC, credential, signer, configuration, and revision checks succeed. It is not a general principal merge, alias migration, appeal, account recovery, cross-domain migration, or production policy decision. The path remains unavailable for ambiguous or unsupported states. The repository record is not independent signed O-tier evidence and does not pass PD-001 or any release gate.

## 4. Considered approaches

### 4.1 Keep the placeholder as an alias

Rejected. It would preserve two authority identities and weaken the invariant that the approval receipt principal equals the authenticated canonical actor.

### 4.2 Derive deterministic principal identifiers before enrollment

Rejected for this repair. It would alter the global identity model and require a much broader migration of existing principals, credentials, events, and references.

### 4.3 Online permanent dual trust

Rejected. It avoids a short maintenance window but leaves the placeholder signer able to authorize new approvals and complicates exact recovery.

### 4.4 Managed offline cutover with journaled resume

Selected. Setup derives the canonical target from the enrolled identity, stops the two managed services, stages complete replacement state, commits a bounded cutover, and starts only after exact postconditions hold. The old public key may remain only in immutable historical verification evidence, never current approval authority.

## 5. Canonical owner adoption

### 5.1 Trigger

The adoption check runs during managed setup or supported upgrade after Core has an enrolled harness and credential. It is not exposed as a generic database-editing command.

The target principal is read from the package-owned server identity profile and must match the current Core enrollment binding. The OIDC issuer and exact subject must match both the enrolled identity evidence and the configured Approval owner binding. Message text, CLI principal arguments, email equality, or an unverified payload cannot select the target.

### 5.2 Supported source states

The reconciler accepts only:

- an unmodified v0.1.50 placeholder state;
- the exact known partial repair state;
- the exact known operational live repair state;
- the already-converged target state.

Each state is identified by typed configuration, exact signer thumbprints, database schema versions, bounded row shapes, current/revoked state, and digests. Package source hashes are handled by the existing immutable package-upgrade mechanism, not trusted as authority evidence.

Anything else returns a redacted `canonical_owner_recovery` blocker before mutation.

### 5.3 Approval database transition

Inside one SQLite `BEGIN IMMEDIATE` transaction, the reconciler:

1. verifies one active owner binding for the configured domain and OIDC identity;
2. verifies the source identifier is either the exact placeholder or exact canonical target;
3. rejects revoked, duplicate, cross-domain, or ambiguous bindings;
4. verifies every active passkey row belongs to that owner and no target collision exists;
5. expires or rejects any nonterminal owner session, registration ceremony, or approval request that cannot remain valid across the identity change;
6. rewrites the owner binding and active passkey ownership to the canonical principal;
7. updates the deterministic passkey user-handle value while preserving the credential ID, public key, sign count, device type, backup state, and creation time;
8. leaves terminal requests, receipts, and historical audit rows immutable under their original recorded principal, then binds that historical identifier to the canonical adoption only through new evidence;
9. appends a privacy-minimized adoption audit record bound to source-state digest, target-state digest, OIDC-binding digest, signer transition digest, and setup-journal identifier.

The transaction commits only after exact postconditions and SQLite foreign-key checks pass. A repeated run returns the same converged result without another migration.

### 5.4 Signer cutover

Setup generates a replacement P-256 key under Approval's existing owner-only signer custody. The new key is bound only to the canonical principal.

Core's current trusted approver set and Approval's active approver configuration are replaced while both services are stopped. The target state contains only the canonical principal and replacement signer for new approvals. The placeholder signer is absent from current Core trust and absent from Approval's active approver configuration.

Historical receipt verification stores the old public key and its authority interval in append-only historical verification evidence. That evidence has no route into current approval selection. Unknown keys, use outside the recorded interval, or use for a new request fail closed.

## 6. Recovery journal and cutover

### 6.1 Journal

A root-owned, mode `0600` journal records only bounded metadata and encrypted or owner-only rollback bytes:

- schema and package version;
- source-state classification;
- request, configuration, database, signer, and setup-marker digests;
- canonical identity and OIDC-binding digests;
- previous and replacement managed-file bytes;
- complete pre-mutation Approval SQLite backup plus WAL/SHM handling evidence;
- phase and compare-and-swap revision;
- service states before cutover.

No private key material, passkey material, token, raw identity, or protected content appears in command output or ordinary logs.

### 6.2 Phases

The phases are:

1. `prepared`: exact state classified, services not yet changed, complete backup durable;
2. `quiesced`: Approval and Core stopped and verified inactive;
3. `approval_committed`: SQLite owner migration and replacement signer/config committed;
4. `core_committed`: Core current trust/config committed;
5. `marker_committed`: setup marker binds the target configuration and disarms rollback;
6. `services_verified`: services restarted, health/readiness and authority postconditions verified;
7. `completed`: rollback bytes removed, historical public verification evidence retained.

Before `marker_committed`, failure restores the complete pre-change state and original service state. At or after `marker_committed`, retry resumes forward from exact journal evidence. It never guesses whether a phase completed.

The cutover has a bounded maintenance window. This is preferable to temporarily authorizing both signers.

## 7. Atomic schema-v7 projection

The existing v6-to-v7 mapping becomes a reusable single-scope materializer plus the existing batch migration wrapper.

Communication-scope completion calls the single-scope materializer inside the same database transaction that writes:

- terminal communication-scope commitment;
- exact allowed items;
- five communication entitlements and five revoke powers;
- canonical collaboration scope;
- exact owner and peer active member rows;
- audit and idempotent completion result.

The materializer resolves all inputs from server-held scope rows. It does not accept caller-supplied member identity or authority fields. Existing exact projection rows are accepted only when every digest and value matches. Partial, extra, or conflicting projection rows fail the transaction.

Any mapping, insertion, constraint, audit, or result-persistence failure rolls back the entire activation. The service cannot return `communication_active` without the projection. PostgreSQL and SQLite use the same mapping and differ only in parameter syntax.

For a legacy scope already committed without projection, the setup recovery path invokes the same single-scope materializer in one transaction. Repeated repair is a verified no-op.

## 8. Compatibility and rollback

The supported upgrade preserves schema-v7 data and does not create a new public schema version solely for derived runtime rows. Existing v0.1.50 stores remain readable after exact recovery. N/N-1 compatibility remains governed by the existing release catalog.

Rollback before the marker boundary restores the complete Approval database, signer/config files, Core config, setup marker, and original managed-service state. Rollback after new approvals could be externally observable is forbidden; forward recovery is required.

The active communication scope is never replaced. Missing projection is repaired from its committed source rows. No endpoint is re-enrolled and no generic entitlement is minted.

## 9. Fail-closed behavior

Recovery stops before mutation on:

- wrong or missing enrolled identity evidence;
- OIDC issuer or subject mismatch;
- email-only identity match;
- wrong domain;
- revoked canonical credential or owner passkey;
- unexpected active sessions or requests;
- duplicate or ambiguous owner/passkey rows;
- signer, configuration, revision, marker, or digest drift;
- incomplete backup or journal durability;
- unsupported live-repair shape;
- projection source or target mismatch.

After mutation starts, uncertain file, database, or service outcomes retain the journal and require exact resume or pre-boundary restoration. No branch reports operational until managed health, readiness, canonical approval authority, active scope, projection membership, and current credentials are verified.

## 10. Verification

Required evidence includes:

- focused TTL tests proving only communication-scope requests use one hour;
- clean and post-enrollment canonical adoption tests;
- wrong identity/domain/OIDC, revoked passkey, duplicate, stale revision, race, response-loss, and interruption tests;
- schema-v7 activation and repair tests on SQLite and dedicated PostgreSQL;
- projection fault injection proving no partial `communication_active` result;
- upgrade fixtures for the four supported source states and unsupported drift;
- signer historical-verification tests proving the old key cannot authorize current requests;
- installed-package setup/upgrade restart evidence;
- a new server-to-laptop and laptop-to-server message reaching `recipient_committed` after authorized live deployment.

Hermetic tests do not prove production readiness. PostgreSQL, installed-package, live-server, owner, and external evidence retain their actual tiers. No gate status changes without reproducible evidence.

## 11. Documentation

The implementation must update:

- `docs/OWNER_DECISIONS.md` with the bounded 2026-08-09 owner instruction and its non-O-tier limit;
- `docs/ARCHITECTURE.md` and `docs/SCHEMAS_INTERFACES.md` for adoption, signer history, journal, and atomic projection boundaries;
- `README.md` and `docs/implementation-guide.md` for supported operator recovery behavior;
- `REQUIREMENTS_STATUS.md` and `docs/GATE_EVIDENCE.md` only to describe verified implementation evidence without promoting blocked gates.

## 12. Non-goals

This work does not implement the selector-free background inbox receiver, dashboard changes, general identity migration, appeals, account merging, federation, A2A changes, artifact changes, business effects, or production certification. Live deployment requires separate explicit authorization after package verification.
