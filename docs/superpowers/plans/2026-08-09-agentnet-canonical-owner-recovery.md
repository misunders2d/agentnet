# AgentNet canonical owner and scope recovery implementation plan

> **Execution:** Continue in this isolated worktree with test-driven implementation. Do not mutate the live server until all package and upgrade evidence is green and the owner separately authorizes deployment.

**Goal:** Replace the v0.1.50 manual owner/signer/database repairs with a bounded package-owned recovery path and make every new communication-scope activation atomically materialize schema-v7 messaging authority.

**Architecture:** Reuse one strict single-scope projection function for migration, runtime completion, and repair. Add a focused canonical-owner recovery module for state classification, Approval SQLite adoption, signer-history evidence, and journal validation. Keep root orchestration and service lifecycle in `server_setup.py`, while Approval mutation runs under the Approval service identity through an internal, fixed-shape package command.

**Requirements:** ID-001, ID-002, ID-005, ID-006, AUTH-001..005, COM-001, COM-002, COM-009, AVL-003, AVL-005, SEC-003, SEC-005, SEC-007, OPS-003, OPS-006.

**Owner boundary:** The 2026-08-09 authenticated owner approval covers only exact placeholder-to-enrolled-owner recovery. It is not general migration/appeal policy and is not O-tier evidence.

---

## Task 1: Make schema-v7 projection reusable

**Files:**
- Modify: `src/agentnet/storage/release_v7_schema.py`
- Modify: `tests/production/test_release_v7_schema.py`

1. Add failing tests for one committed scope materialized by exact `scope_id` on an already-v7 database.
2. Cover exact idempotent retry, partial target rejection, extra member rejection, wrong source row rejection, and SQLite transaction rollback.
3. Extract `materialize_v6_communication_scope(connection, *, scope_id, postgres=False) -> Literal["created", "already_exact"]` from the existing batch loop.
4. Keep `migrate_v6_communication_scopes` as the batch wrapper used by schema 6→7 migration.
5. Ensure the materializer resolves every field from committed source rows and verifies the exact target projection before accepting an existing row.
6. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/production/test_release_v7_schema.py
```

7. Commit: `refactor: reuse communication scope projection`

## Task 2: Materialize projection inside scope completion

**Files:**
- Modify: `src/agentnet/authorization/communication_scope_service.py`
- Modify: `tests/authorization/test_communication_scope_service.py`
- Modify: `tests/integration/test_collaboration_scope_messaging.py`

1. Add failing completion tests proving one canonical collaboration scope and exactly two active members exist when `complete()` returns `communication_active`.
2. Add fault injection at projection insertion and prove the communication scope, entitlements, items, approval consumption, audit record, and terminal result all roll back.
3. Add retry/response-loss coverage proving no duplicate projection or authority rows.
4. Call the shared single-scope materializer after the source scope is marked committed but before returning, inside the existing final transaction.
5. Pass the backend dialect only when using a raw migration connection; runtime store connections already translate qmark SQL.
6. Exercise the actual collaboration-scope messaging lookup after completion.
7. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q \
  tests/authorization/test_communication_scope_service.py \
  tests/integration/test_collaboration_scope_messaging.py \
  tests/production/test_release_v7_schema.py
```

8. Commit: `fix: project communication scope atomically`

## Task 3: Define canonical-owner recovery contracts

**Files:**
- Create: `src/agentnet/operations/canonical_owner_recovery.py`
- Create: `tests/operations/test_canonical_owner_recovery.py`

1. Write failing model and classifier tests for:
   - unmodified v0.1.50 placeholder state;
   - exact partial live repair;
   - exact operational live repair;
   - already-converged state;
   - wrong domain/OIDC subject, email-only match, duplicate owner, duplicate target, revoked passkey, unknown signer, unexpected active request/session, and arbitrary drift.
2. Define strict Pydantic models for the recovery request, journal, phase, historical signer record, source classification, and sanitized result.
3. Require canonical JSON, exact key sets, lowercase digests, bounded identifiers, fixed package/source versions, and monotonic phases.
4. Implement pure classification from typed Approval config, exact bounded SQLite facts, Core config/trust facts, setup marker, and verified enrolled identity facts.
5. Never accept email equality as canonical identity proof. Require exact OIDC issuer and subject plus the enrolled principal derived by setup.
6. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/operations/test_canonical_owner_recovery.py
```

7. Commit: `feat: define bounded owner recovery state`

## Task 4: Implement Approval owner/passkey adoption

**Files:**
- Modify: `src/agentnet/operations/canonical_owner_recovery.py`
- Modify: `src/agentnet/approval/store.py` only if a reusable transaction/backup primitive is required
- Modify: `src/agentnet/cli.py`
- Modify: `tests/operations/test_canonical_owner_recovery.py`
- Modify: `tests/approval/test_webauthn_service.py`

1. Add failing tests for one exact owner binding and active passkey migrating to the canonical principal while credential ID, public key, sign count, device/backup state, and creation time remain unchanged.
2. Prove the deterministic user handle is updated and the existing passkey still verifies a new Approval request.
3. Prove terminal requests, receipts, and historical audit records are not rewritten.
4. Prove nonterminal requests/sessions are rejected or explicitly terminated according to the fixed recovery contract before adoption.
5. Implement an internal fixed-shape command invoked only by managed setup. It consumes a root-orchestrated, digest-bound recovery request file; it is not a generic principal-edit command.
6. Under the Approval service identity:
   - open the exact configured SQLite store;
   - create a complete SQLite backup using the SQLite backup API after quiescence;
   - run `BEGIN IMMEDIATE`;
   - recheck source facts and recovery-request digests;
   - update the binding, active credentials, and user handle;
   - append a new adoption audit record without changing historical rows;
   - run foreign-key and exact postcondition checks;
   - commit once.
7. Make replay return the same target state and reject a different request digest.
8. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q \
  tests/operations/test_canonical_owner_recovery.py \
  tests/approval/test_webauthn_service.py
```

9. Commit: `feat: adopt canonical approval owner`

## Task 5: Implement signer history and current-authority cutover

**Files:**
- Modify: `src/agentnet/operations/canonical_owner_recovery.py`
- Modify: `src/agentnet/approval/config.py`
- Modify: `src/agentnet/operations/config.py`
- Modify: `src/agentnet/operations/server_setup.py`
- Modify: `tests/operations/test_canonical_owner_recovery.py`
- Modify: `tests/operations/test_server_setup_recovery.py`
- Modify: `tests/approval/test_webauthn_service.py`

1. Add failing tests for replacement signer generation, exact canonical principal binding, current Core trust replacement, and placeholder signer denial for new requests.
2. Add historical verification tests proving a receipt issued before cutover remains verifiable only within its recorded authority interval.
3. Store historical public signer evidence in an owner-only, append-only, digest-chained file. Do not include private key bytes or feed historical keys into current `trusted_approvers`.
4. Stage a replacement signer in Approval custody and replacement Approval/Core configs while services are stopped.
5. Replace current Approval approver and Core trusted approver/service principal together under the recovery journal. The target contains no current placeholder authority and no permanent dual trust.
6. Reject unknown historical signer intervals, key reuse across principals, or a signer/config mismatch.
7. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q \
  tests/operations/test_canonical_owner_recovery.py \
  tests/operations/test_server_setup_recovery.py \
  tests/approval/test_webauthn_service.py
```

8. Commit: `feat: cut over canonical approval signer`

## Task 6: Add crash-safe setup orchestration

**Files:**
- Modify: `src/agentnet/operations/server_setup.py`
- Modify: `src/agentnet/cli.py`
- Modify: `tests/operations/test_server_setup_recovery.py`
- Modify: `tests/operations/test_server_setup.py`

1. Add failing phase-interruption tests for `prepared`, `quiesced`, `approval_committed`, `core_committed`, `marker_committed`, `services_verified`, and `completed`.
2. Add source fixtures for the four supported states and unsupported drift.
3. Add response-loss and concurrent setup-lock tests.
4. Introduce a dedicated root-owned recovery journal path and strict compare-and-swap writer, reusing existing setup journal durability primitives rather than inventing a generic framework.
5. Before mutation, validate package/setup custody, enrolled identity proof, exact OIDC binding, current credentials, Approval source state, Core trust/config, signer custody, schema versions, and complete backup.
6. Stop Approval and Core and verify them inactive before database or signer/config mutation.
7. Before marker commit, restore the complete Approval backup, signer/config files, Core config, marker, and prior service state on failure.
8. After marker commit, resume forward only. Verify each phase from durable state before advancing.
9. Invoke the shared projection materializer for any exact committed scope missing its schema-v7 projection.
10. Restart only managed required services, then verify health, readiness, canonical current approval authority, active communication scope, exact members, and current credential.
11. Return redacted blockers and sanitized evidence only.
12. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q \
  tests/operations/test_server_setup.py \
  tests/operations/test_server_setup_recovery.py \
  tests/operations/test_canonical_owner_recovery.py
```

13. Commit: `feat: recover canonical owner during setup`

## Task 7: Prove PostgreSQL parity

**Files:**
- Modify: `tests/production/test_postgres_store.py` or the existing PostgreSQL communication-scope test module selected by repository convention
- Modify: `tests/production/test_release_v7_schema.py`
- Modify: `scripts/ci/ordinary-server-upgrade-e2e.sh`

1. Add a dedicated PostgreSQL test that completes a new communication scope on schema v7 and observes one exact collaboration scope and two exact members in the same commit.
2. Inject a projection conflict and prove no terminal communication scope or entitlement rows commit.
3. Add exact missing-projection repair and idempotent retry coverage.
4. Extend installed upgrade E2E with the supported v0.1.50 source fixture and verify package-owned convergence without re-enrollment.
5. Run only against the dedicated mutation-authorized database:

```bash
AGENTNET_TEST_POSTGRES_URL='postgresql:///agentnet_test_governance_final?host=/tmp/agentnet-pgsocket&port=55432' \
AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1 \
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q \
  tests/production/test_release_v7_schema.py \
  tests/production -k 'postgres and communication_scope'
```

6. Run the installed upgrade lane in its documented isolated environment.
7. Commit: `test: prove owner recovery across stores`

## Task 8: Update owner, architecture, operator, and evidence records

**Files:**
- Modify: `docs/OWNER_DECISIONS.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/SCHEMAS_INTERFACES.md`
- Modify: `README.md`
- Modify: `docs/implementation-guide.md`
- Modify: `REQUIREMENTS_STATUS.md`
- Modify: `docs/GATE_EVIDENCE.md`

1. Record the bounded 2026-08-09 authenticated owner instruction under PD-001 without calling it O-tier or general migration policy.
2. Document the owner-adoption trust boundary, historical/current signer separation, journal phases, recovery boundary, and atomic runtime projection.
3. Document supported operator behavior and redacted blockers. Do not expose internal identifiers in ordinary output.
4. Update implementation evidence only from test artifacts produced in this change. Keep all owner, production, HA, and external gates non-green.
5. Preserve the unrelated main-worktree edit to `docs/SCHEMAS_INTERFACES.md`; merge intentionally rather than overwriting it.
6. Commit: `docs: document canonical owner recovery`

## Task 9: Package and release verification

**Files:**
- Modify version/release metadata only if the repository release workflow requires the next candidate version after implementation stabilizes.

1. Run focused tests from Tasks 1–7 again after documentation integration.
2. Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q
UV_CACHE_DIR=/tmp/uv-cache uv run python scripts/verify_release.py
UV_CACHE_DIR=/tmp/uv-cache uv run agentnet verify
npm run check
```

3. Run the server setup upgrade E2E and confirm no process, socket, temporary key, credential, or test server remains.
4. Review the stabilized diff once for identity spoofing, stale trust, rollback uncertainty, partial projection, cross-store mismatch, and evidence overclaim.
5. Correct real findings and rerun affected evidence.
6. Commit any final corrections with a narrow conventional commit.

## Task 10: Authorized live convergence and messaging proof

**Prerequisite:** Separate explicit owner authorization after all package evidence is green.

1. Inventory the live server through the approved protected server operations path. Do not use A2A.
2. Install the immutable verified package through the supported upgrade path.
3. Run managed setup recovery once and retain sanitized journal/result evidence.
4. Verify no modified installed source, current placeholder authority, temporary dual trust, or incomplete rollback claim remains.
5. Verify Core and Approval health/readiness, current credential, active communication scope, and exact projection.
6. Send one server-to-laptop and one laptop-to-server AgentNet message. Each must reach durable `recipient_committed` acknowledgement.
7. Record exact evidence in MEL-251 and relevant evidence documents without promoting unrelated gates.
8. Mark MEL-251 complete only after this live proof.
9. Begin MEL-250, then confirm the dashboard implementation scope with the owner.
