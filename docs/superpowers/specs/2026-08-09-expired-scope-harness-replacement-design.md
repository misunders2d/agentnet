# Expired Collaboration-Scope Harness Replacement Design

## Purpose

Add one package-owned, independently approved operation that replaces an expired same-principal harness in an active collaboration scope without hand-editing Core state. The operation preserves the member role, records the former membership as a tombstone, activates the exact replacement harness, and immediately makes current schema-v7 membership authoritative for communication authorization.

## Scope

The first supported case is deliberately narrow:

- the caller is the exact active owner harness of an active direct collaboration scope;
- the former member and replacement harness belong to the scope owner's exact principal and domain;
- the former member has role `member` and an expired current credential;
- the replacement harness has an active current credential and is not already a scope member;
- the replacement preserves role `member`;
- an independent Approval receipt from the exact scope-owning principal authorizes the transaction.

Owner replacement, cross-principal transfer, cross-domain transfer, guest replacement, role changes, active-credential migration, and generic membership administration are rejected.

## Affected requirements and boundaries

Affected stable requirements: `ID-005`, `ID-006`, `ID-009`, `AUTH-001`, `AUTH-002`, `AUTH-003`, `AUTH-004`, `AUTH-005`, `COM-001`, `COM-002`, `COM-009`, `AVL-003`, `SEC-003`, `SEC-005`, `SEC-007`, `OPS-003`, and `OPS-006`.

Trust boundaries:

1. The managed server CLI reads the installed Core configuration and exact server identity under package-owned custody.
2. Core state is authoritative for scope ownership, membership, harness identity, credential state, policy revision, and revocation epoch.
3. Approval remains a separate signer and store. Core accepts only a verified, current, single-use receipt bound to the exact canonical replacement transaction.
4. Legacy schema-v6 communication records remain immutable source evidence. Current schema-v7 collaboration membership becomes the authorization membership source.

No new third-party component is needed. The implementation reuses the existing Approval client, verifier, single-use receipt ledger, audit chain, PostgreSQL/SQLite transaction abstractions, and managed private-state custody.

## Canonical transaction

The replacement transaction is a strict canonical object that binds:

- request ID and approval purpose;
- scope ID, domain ID, owner principal ID, and owner harness ID;
- former harness ID, replacement harness ID, and preserved role;
- exact former and replacement credential IDs and epochs;
- former credential expiry and replacement credential validity window;
- current scope digest, revision, membership sequence, policy revision, and domain revocation epoch;
- proposed next membership sequence and revision;
- request creation and expiry times.

The managed command writes this request and a receipt-possession secret to an owner-only resumable state file before creating the Approval request. Approval request idempotency is keyed by the replacement request ID.

## Validation

Before requesting approval and again immediately before commit, the operation requires all of the following:

- the scope exists, is active, is not expired, and the caller is a current non-lab harness of the exact scope-owning principal;
- the caller is an active verified human harness with a current active credential;
- scope policy revision and domain revocation epoch are current;
- exactly one active former member row exists with role `member`;
- the former harness shares the owner principal and domain;
- the former harness current credential matches the transaction and is expired;
- the replacement harness shares the owner principal and domain;
- the replacement harness current credential matches the transaction and is active;
- the replacement is not already present in the scope;
- all stored digests and sequence values match the canonical pre-state;
- the Approval receipt is current, has the exact `identity.credential.recover.approve` purpose/domain/transaction digest, and names the exact owner principal.

Any mismatch fails closed without mutation.

## Atomic mutation

One Core database transaction performs the complete cutover:

1. Reverify the canonical transaction against current state.
2. Verify and consume the single-use independent Approval receipt.
3. Change the former member to `removed`, set its removal sequence/time, and recompute its member digest.
4. Insert the replacement as an active `member` at the same next membership sequence and compute its member digest.
5. Increment the scope membership sequence and revision exactly once.
6. Recompute the scope digest over the complete active and removed membership set.
7. Append a tamper-evident audit record containing the exact transaction and receipt digests.
8. Bind the resulting audit hash and new scope digest to the updated scope row.

A compare-and-swap update on the previous scope digest, revision, and membership sequence prevents concurrent or stale replacement.

## Authorization cutover

Schema-v6 communication scope rows and item records are retained unchanged as approval provenance. When an entitlement originates from a communication scope that has a schema-v7 collaboration projection, authorization obtains its permitted peer harness set from the projection's current active membership rows. It rejects removed, expired, revoked, cross-principal, digest-invalid, or incomplete projections.

This prevents the former harness from retaining access and grants the replacement harness only the same principal-scoped communication actions already approved for the scope. It does not create new actions, data access, roles, or generic authority.

## Idempotency and recovery

The Approval store makes request creation idempotent for the exact canonical transaction. The Core audit record and exact post-state make commit retry idempotent:

- retry before approval returns the same pending ceremony;
- retry after approval but before commit performs the single transaction;
- crash before commit leaves membership unchanged;
- crash after commit returns the recorded result without incrementing sequences again;
- reuse of a request ID with different bytes fails;
- a different replacement request against the changed pre-state fails stale.

Rejected or expired ceremonies remain terminal. A separate explicit terminal-state replacement flag creates a fresh request only after Approval proves the previous ceremony is rejected or expired and the managed inputs have not drifted.

## Operator interface

The managed operation is:

```text
sudo agentnet server-agent replace-expired-scope-harness \
  --scope-id <scope-id> \
  --old-harness-id <expired-harness-id> \
  --new-harness-id <active-harness-id> \
  --role member
```

The command defaults to package-owned managed config, identity, and private-state paths. The first run prints a content-minimized owner approval action and exits nonzero while pending. Rerunning the exact command after approval commits the replacement. The command does not restart services or mutate identity/configuration files.

## Verification

Required hermetic evidence covers success, exact idempotent replay, request conflict, wrong caller, wrong owner approval, cross-domain/principal mismatch, incorrect role, unexpired former credential, inactive replacement credential, existing replacement membership, stale scope revision/digest, concurrent compare-and-swap failure, rollback after injected mutation failure, removed-member denial, replacement-member authorization, SQLite behavior, and dedicated PostgreSQL behavior.

No production, owner-policy, or external gate is promoted by these tests.