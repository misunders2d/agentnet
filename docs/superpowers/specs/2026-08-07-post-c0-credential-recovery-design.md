# Post-C0 managed-server credential recovery design

Date: 2026-08-07
Status: owner-approved design

## Goal and scope

Extend the existing root-only, owner-WebAuthn-approved managed-server credential reauthorization ceremony so it can recover an expired credential after the communication-only server has completed its C0 round trip.

The change affects ID-009 credential lifecycle and the retained C0/setup provenance boundary. It does not grant authority, restart services, broaden A2A or relay topology, reinterpret C0 success, promote a release gate, or authorize direct PostgreSQL or managed-file edits.

A2A and relay identities remain unsupported because they have separate credential-bound signing state. This design covers only the current communication-only ordinary server profile.

## Invariants

1. `/var/lib/agentnet/c0-responder/terminal.json` remains byte-for-byte immutable. Recovery never rewrites, relabels, deletes, or excludes it.
2. The original terminal marker remains evidence about the exact credential that performed C0, not the replacement credential.
3. Every replacement requires the existing same-key proof of possession, a fresh owner WebAuthn-UV Approval receipt, current domain/principal/harness eligibility, an expired active credential, and a finite next epoch.
4. Reauthorization grants no authority and performs no service restart.
5. Setup accepts a post-C0 credential only when a complete package-owned supersession chain starts at the immutable terminal marker and ends at the exact managed config/identity credential.
6. Missing, malformed, discontinuous, stale, racing, or externally changed state fails closed.
7. The ceremony is restart-safe after every PostgreSQL and filesystem transition. Operators recover only by rerunning the exact package command.
8. Historical C0 evidence and PostgreSQL audit history are append-only facts. Recovery adds a new transition; it never changes an old fact.

## Options considered

### Selected: separate auditable supersession chain

Preserve the terminal marker and create a separate private, hash-chained supersession journal. Bind the exact terminal and prior-journal digests into the fresh approval transaction. Serialize reauthorization against setup and reconcile each committed step on retry.

This is the smallest design that preserves historical meaning, supports repeated future expiry, and provides deterministic crash recovery.

### Rejected: rewrite the terminal marker

Changing `credential_id` in `terminal.json` would relabel historical C0 evidence. Even an atomic compare-and-swap would preserve bytes safely while changing their meaning. This contradicts the retained-marker invariant.

### Rejected: keep post-C0 recovery unsupported

The current baseline can fail closed through destructive package reset and reenrollment, but it turns an ordinary missed-renewal event into a rebuild. The owner explicitly authorized expanding the lifecycle baseline instead.

### Rejected: one-off migration for the current server

A server-specific marker exception would not survive the next expiry and would create an unauditable alternate recovery path.

## State model

Add the fixed private file:

`/var/lib/agentnet/credential-supersessions.json`

It is a regular, single-link, mode-0600 file under the existing Core service
account and directory custody. The retired C0 responder account cannot write
it. It has a strict versioned schema containing:

- schema identifier;
- domain and harness IDs;
- SHA-256 digest of the immutable terminal marker;
- original terminal credential ID;
- an ordered list of transition entries.

Each transition entry contains:

- reauthorization request ID and canonical transaction digest;
- Approval receipt ID and digest;
- authoritative PostgreSQL audit-record hash;
- previous and new credential IDs;
- previous and new credential epochs;
- unchanged key ID;
- new validity interval;
- digest of the preceding entry, or the terminal-marker digest for the first entry.

The canonical entry digest covers every entry field. The first entry must name the terminal credential and epoch. Every later entry must name the preceding entry's new credential and epoch. Epochs advance by exactly one. The journal tail is the only credential that setup may reconcile with managed config and identity.

The journal is provenance, not positive authority. PostgreSQL remains
authoritative for the active credential and tamper-evident audit record. Core
bootstrap verifies every journal entry against the exact audit record named by
`audit_record_hash`, including request and Approval digests, predecessor and
successor credentials, epochs, key ID, validity interval, terminal digest, and
prior-journal digest. Internal journal hashes alone never establish that a
transition was approved or committed. Root-file custody does not replace
WebAuthn approval or database authorization.

## Approval and request compatibility

Keep the released pre-C0 request-v1 semantics unchanged so a retained v1 approval/state can finish without a digest change.

Add request-v2 for post-C0 recovery. In addition to the existing exact expired binding and managed config/identity digests, request-v2 binds:

- the immutable C0 terminal digest;
- the exact prior supersession-journal digest, or an explicit absent value for the first transition;
- the current chain-tail credential and epoch.

The same old managed key signs the v2 possession preimage. Approval signs the complete v2 canonical transaction. Unknown fields and cross-version state fail closed.

A fresh reauthorization creates v2 only when the terminal marker and existing journal, if any, validate completely against the current managed actor. A retained v1 request remains legal only when no C0 terminal binding exists.

## Coordination and transition order

The command acquires the permanent root-owned server-setup lock nonblockingly before reading managed recovery state. It retains that lock through database lease acquisition, Approval interaction, PostgreSQL commit, journal/config/identity reconciliation, state removal, and final output. Setup, reset, upgrade, and reauthorization therefore cannot mutate overlapping package state concurrently.

After Approval receipt retrieval:

1. PostgreSQL atomically retires the expired row, creates the deterministic next-epoch credential, consumes the Approval receipt, appends the complete audit event, and returns its `audit_record_hash`.
2. The Core-owned supersession journal is created or compare-and-swap replaced using a same-directory temporary file, complete write, file `fsync`, atomic rename, and directory `fsync`.
3. Managed Core config is compare-and-swap updated to the new credential ID.
4. Managed server identity is compare-and-swap updated to the new credential ID and epoch.
5. The root-only pending state is removed and its directory is synchronized.

The journal is installed before managed labels so the authorization evidence exists before files point at the new credential. No intermediate state is accepted as operational. A concurrent setup attempt is excluded by the setup lock; after a process crash, setup detects the incomplete relationship and tells the operator to rerun reauthorization.

## Crash and retry behavior

The root-only pending state retains the exact request and all approved input digests.

- Crash before PostgreSQL commit: retry uses the same Approval request and transaction.
- Crash after PostgreSQL commit: the deterministic database operation retrieves its exact audit record and idempotent result; retry installs the missing journal entry with that record hash.
- Crash after journal commit: retry verifies and reconciles the exact entry without appending a duplicate.
- Crash after config commit: retry reconciles journal and config, then updates identity.
- Crash after identity commit but before state removal: retry verifies all three files and removes only the matching pending state.
- Response loss after completion: absence of pending state plus current consistent files causes a new invocation to report that the credential is current; it does not create another credential.

If any current byte differs from both the request-bound predecessor and the exact expected successor, recovery stops. It never overwrites unknown state.

## Setup validation

The server setup path continues to validate `terminal.json` exactly as historical C0 evidence. If its credential equals the current managed credential, no supersession journal may claim a different tail.

If the terminal credential differs from current config:

1. the supersession journal must exist with exact Core-account private custody;
2. its terminal digest/domain/harness/original credential must match `terminal.json`;
3. its transition hashes, credential IDs, epochs, and key continuity must form one complete chain;
4. every transition must match one exact record in the verified PostgreSQL audit chain by `audit_record_hash`, request/Approval digests, predecessor/successor, epochs, key, validity interval, and provenance digests;
5. its tail must match the exact config and managed identity credential/epoch;
6. Core bootstrap and database binding validation must confirm that tail as the active credential.

Core bootstrap returns content-free evidence containing the journal digest,
verified transition count, verified audit-record count, and active tail. Setup
requires that evidence to match its root-side terminal/journal validation
before startup. An absent journal, orphan or unaudited transition, duplicate or
reordered transition, altered terminal marker, wrong prior digest, skipped
epoch, changed key, foreign domain/harness, stale tail, relabeled original
credential, or writable-by-C0 journal blocks startup.

## Tests

Add behavior-first regression coverage for:

- retained terminal plus no journal remains refused before a v2 request is approved;
- v2 request binds terminal and prior-journal digests;
- original terminal bytes remain unchanged after successful recovery;
- first and repeated supersession entries form the exact chain;
- setup accepts only a complete chain ending at config and identity;
- setup rejects relabeled terminal evidence, missing and reordered links, duplicate entries, skipped epochs, changed key/domain/harness, stale tail, and journal drift;
- setup rejects an internally consistent journal whose transition lacks or conflicts with its named PostgreSQL audit record;
- the retired C0 responder account cannot modify the supersession journal;
- reauthorization and setup/reset lock contention fail closed;
- injected crash after PostgreSQL commit, journal commit, config commit, identity commit, and state removal response loss reconciles exactly once;
- v1 pre-C0 pending state retains its original approval digest and behavior;
- post-C0 recovery performs no authority grant or service restart.

Run the focused identity, CLI, server-setup, recovery, and PostgreSQL lanes, then the broad source suite, release verifier, and `agentnet verify`. External or owner evidence is not promoted by these tests.

## Documentation and operational output

Update architecture, schemas/interfaces, implementation guide, requirements status, release manifest, and public candidate status to describe the new baseline honestly. The CLI result must name journal/config/identity reconciliation separately and continue to require the exact package-owned setup `--apply --start` rerun.

No operator instruction may suggest deleting or editing the terminal marker, journal, identity, config, credential rows, or audit rows manually.
