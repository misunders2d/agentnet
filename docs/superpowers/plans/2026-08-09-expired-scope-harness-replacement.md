# Expired Scope Harness Replacement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one Approval-backed, atomic, idempotent operation that replaces an expired same-principal `member` harness in an active collaboration scope.

**Architecture:** A focused authorization service prepares and commits a strict canonical replacement transaction against Core state. The managed server command owns the resumable Approval ceremony, while runtime policy resolves current peers from schema-v7 membership and retains schema-v6 records only as provenance.

**Tech Stack:** Python 3.13, Pydantic v2, SQLite, PostgreSQL adapter, existing AgentNet Approval client/verifier, argparse, pytest.

## Global Constraints

- Preserve verified actor context; caller-supplied identifiers never establish identity.
- Reuse the existing Approval service and receipt-consumption ledger; add no dependency.
- Support only exact same-principal, same-domain `member` replacement where the old current credential is expired and the new current credential is active.
- Make the membership, digest, revision, audit, and receipt-consumption changes one database transaction.
- Preserve schema-v6 communication records as immutable source evidence.
- Fail closed on ambiguity, drift, replay mismatch, stale policy, stale revocation epoch, or partial state.
- Keep owner, guest, role-change, cross-principal, cross-domain, and active-credential migration unsupported.
- Do not promote any release, owner, privileged-host, or external evidence gate.

---

### Task 1: Canonical replacement service

**Files:**
- Create: `src/agentnet/authorization/scope_harness_replacement.py`
- Create: `tests/authorization/test_scope_harness_replacement.py`
- Modify: `src/agentnet/authorization/__init__.py`

**Interfaces:**
- Produces: `ScopeHarnessReplacementRequest`, `ScopeHarnessReplacementResult`, and `ScopeHarnessReplacementService`.
- `ScopeHarnessReplacementService.prepare(*, actor, scope_id, old_harness_id, new_harness_id, role, request_id, issued_at, expires_at) -> ScopeHarnessReplacementRequest` reads and binds the exact pre-state.
- `ScopeHarnessReplacementService.replace(*, actor, request, approval) -> ScopeHarnessReplacementResult` verifies Approval and commits or returns an exact idempotent replay.

- [ ] **Step 1: Write the failing success test**

Create a real SQLite fixture with one active direct scope, active owner, expired member credential, active replacement credential, and a real signed Approval receipt. Assert that `replace` tombstones the old row, inserts the replacement, increments `membership_sequence` and `revision` once, changes `scope_digest`, binds a new `audit_record_hash`, consumes the receipt, and returns `idempotent_repeat=False`.

- [ ] **Step 2: Run the success test and verify RED**

Run:

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_scope_harness_replacement.py::test_replacement_atomically_tombstones_old_member_and_activates_new_member
```

Expected: collection or import failure because `scope_harness_replacement` does not exist.

- [ ] **Step 3: Implement strict models and preparation**

Implement a request whose canonical transaction binds request/purpose, scope and owner identity, old/new harness and exact current credentials, old expiry/new validity, current scope digest/revision/membership sequence/policy/revocation epoch, proposed next counters, and ceremony times. `prepare` must validate the caller and every narrow precondition from the approved design.

- [ ] **Step 4: Implement atomic replacement**

Inside one `store.transaction()` call: revalidate the request, verify and consume the receipt, update the old member to `removed`, insert the new active member, recompute both member digests and the aggregate scope digest, append audit, and compare-and-swap the scope row on the old digest/revision/membership sequence. Return a strict result containing request ID, scope ID, old/new harness IDs, role, new sequence/revision/digest, audit hash, and `idempotent_repeat`.

- [ ] **Step 5: Run the success test and verify GREEN**

Run the exact command from Step 2. Expected: one passed test.

- [ ] **Step 6: Add denial, concurrency, rollback, and replay tests**

Add parameterized tests for wrong caller, non-owner caller, cross-domain/principal harness, wrong role, unexpired old credential, inactive/expired new credential, absent or duplicate member, stale scope digest/revision/sequence, wrong approver, wrong purpose/domain/transaction, consumed unrelated receipt, compare-and-swap loss, and injected failure after tombstoning. Add an exact replay test asserting unchanged counters/digests and `idempotent_repeat=True`.

- [ ] **Step 7: Run the service suite**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_scope_harness_replacement.py
```

Expected: all tests pass with no partial rows after negative cases.

### Task 2: Membership digest and authorization cutover

**Files:**
- Modify: `src/agentnet/authorization/communication_scope_service.py`
- Modify: `src/agentnet/authorization/policy.py`
- Modify: `tests/authorization/test_communication_scope_service.py`
- Modify: `tests/authorization/test_policy.py`

**Interfaces:**
- Consumes: removed and active member rows written by Task 1.
- Produces: validated collaboration scope snapshots containing both active members and tombstones, while `require` and peer authorization use only active members.

- [ ] **Step 1: Write failing scope-validation tests**

Create a replaced scope with one removed old row and one active replacement row. Assert `CommunicationScopeService.require` accepts the valid aggregate digest for the replacement and denies the removed harness.

- [ ] **Step 2: Verify scope tests fail**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_communication_scope_service.py -k replacement
```

Expected: failure because `_members` currently rejects every non-active row.

- [ ] **Step 3: Validate tombstones without weakening active checks**

Update member parsing to validate both legal states, exact removal sequence/time invariants, authority identity, digest, and ordering. Include active and removed rows in the aggregate scope digest; filter to active rows only when determining membership and recipients.

- [ ] **Step 4: Write failing runtime-policy tests**

Using a migrated schema-v6 communication scope plus its schema-v7 projection, assert the old removed harness receives `communication_scope_harness_mismatch`, the new active harness receives the existing principal entitlement, and its peer set contains exactly the two current active schema-v7 members.

- [ ] **Step 5: Verify policy tests fail**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_policy.py -k collaboration_projection
```

Expected: the replacement remains denied because policy uses frozen schema-v6 harness IDs.

- [ ] **Step 6: Cut policy to current schema-v7 membership**

When an entitlement is linked to a communication scope with a collaboration projection, validate the active projection, current policy/revocation state, exact principal/domain, and allowed action, then derive the peer set from active member rows. Retain the existing schema-v6 path only when no projection exists.

- [ ] **Step 7: Run scope and policy suites**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_communication_scope_service.py tests/authorization/test_policy.py tests/integration/test_collaboration_scope_messaging.py
```

Expected: all current and replacement cases pass.

### Task 3: Resumable managed server command

**Files:**
- Modify: `src/agentnet/cli.py`
- Modify: `tests/cli/test_server_agent_cli.py`
- Create or modify: `tests/operations/test_scope_harness_replacement_cli.py`

**Interfaces:**
- Consumes: `ScopeHarnessReplacementService` and the existing managed Approval client/verifier/configuration helpers.
- Produces: `agentnet server-agent replace-expired-scope-harness` with `--scope-id`, `--old-harness-id`, `--new-harness-id`, fixed `--role member`, managed defaults for config/identity/state, and `--replace-terminal-state`.

- [ ] **Step 1: Write failing parser and pending-ceremony tests**

Assert the parser exposes the exact command and that the first invocation writes owner-only resumable state before creating one Approval request, returns status `waiting_owner_approval`, does not mutate membership, and does not restart services.

- [ ] **Step 2: Verify command tests fail**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/cli/test_server_agent_cli.py tests/operations/test_scope_harness_replacement_cli.py
```

Expected: command/parser missing.

- [ ] **Step 3: Implement the minimal managed command**

Under the existing managed-server setup/recovery lock, load exact config and server identity, open the configured Core store, prepare or reload a canonical request, create/status/retrieve the Approval receipt, call `replace`, remove private pending state only after verified completion, and emit content-minimized JSON. Never edit managed identity/configuration or restart a unit.

- [ ] **Step 4: Add completion, exact replay, crash, and terminal replacement tests**

Cover approval pending, issued completion, crash after Approval creation, crash after database commit, exact rerun, rejected/expired ceremony, explicit `--replace-terminal-state`, managed-file drift, wrong state-file ownership/mode, and concurrent setup-lock refusal.

- [ ] **Step 5: Run command suites**

Run the command from Step 2. Expected: all tests pass.

### Task 4: PostgreSQL parity and operator contracts

**Files:**
- Modify: `tests/production/test_postgres_runtime.py`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/SCHEMAS_INTERFACES.md`
- Modify: `docs/implementation-guide.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: service and command behavior from Tasks 1–3.
- Produces: dedicated PostgreSQL evidence and exact operator instructions.

- [ ] **Step 1: Add the PostgreSQL replacement contract test**

Use the mutation-authorized dedicated test database to execute preparation, approval, replacement, replay, and stale concurrent update behavior through the PostgreSQL adapter. Assert membership rows, counters, digests, audit chain, and receipt consumption match SQLite behavior.

- [ ] **Step 2: Run the dedicated PostgreSQL test**

```bash
AGENTNET_TEST_POSTGRES_URL='postgresql:///agentnet_test_governance_final?host=/tmp/agentnet-pgsocket&port=55432' AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1 PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/production/test_postgres_runtime.py -k scope_harness_replacement
```

Expected: pass when the dedicated server is available; otherwise report the infrastructure blocker without claiming PostgreSQL evidence.

- [ ] **Step 3: Update architecture, schema, and operator docs**

Document the strict transaction, tombstone semantics, schema-v7 authorization source, Approval boundary, exact command/rerun flow, crash recovery, no-restart behavior, and unsupported cases. Do not imply production certification.

### Task 5: Evidence, release inputs, review, and delivery

**Files:**
- Modify: `REQUIREMENTS_STATUS.md`
- Modify: `docs/GATE_EVIDENCE.md`
- Modify: `RELEASE_MANIFEST.json`
- Modify as required by verifier: `docs/RELEASE_MANIFEST.json`, `evidence/local/2026-08-09-v0.1.51/manifest.json`

**Interfaces:**
- Consumes: exact test outputs and final source tree.
- Produces: honest H/L evidence, rebound release artifacts, and updated PR/Linear records.

- [ ] **Step 1: Run focused SQLite suites**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/authorization/test_scope_harness_replacement.py tests/authorization/test_communication_scope_service.py tests/authorization/test_policy.py tests/integration/test_collaboration_scope_messaging.py tests/cli/test_server_agent_cli.py tests/operations/test_scope_harness_replacement_cli.py
```

- [ ] **Step 2: Run broad release validation**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run --extra test pytest -q --ignore=tests/adapters/test_installed_live_inference.py --ignore=tests/adapters/test_subprocess_lifecycle.py
npm run check
UV_CACHE_DIR=/tmp/uv-cache uv run python scripts/verify_release.py
UV_CACHE_DIR=/tmp/uv-cache uv run agentnet verify
```

Record exact counts and expected skips. Do not waive failures.

- [ ] **Step 3: Update evidence ledgers honestly**

Record only observed H/L evidence for the affected IDs. Keep all owner, privileged, production HA, external, and must-not-ship gates blocked.

- [ ] **Step 4: Rebind release inputs and reproducible artifacts**

Follow `agentnet-release-candidate-assembly`: update source hashes, rebuild source/wheel twice under fixed environment, update artifact digests, and rerun package/release checks until clean.

- [ ] **Step 5: Review the stabilized diff once**

Review the final candidate for authorization bypass, incomplete rollback, digest drift, replay weakness, legacy privilege retention, SQLite/PostgreSQL divergence, and documentation overclaim. Fix only concrete findings and rerun affected verification.

- [ ] **Step 6: Commit, push, and update tracked work**

Commit the stabilized candidate on `fix/mel-251-recovery`, push it, watch GitHub Actions for the exact commit, update PR #3 with exact evidence, and update MEL-251 with implementation status, remaining gates, and the exact server command. Do not publish npm or mutate the live server.