# Combined Live Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow the exact retained v0.1.50 marker to safely recognize the already-applied canonical-owner repair plus the known one-hour Approval TTL hotfix before the existing v0.1.51 journaled upgrade runs.

**Architecture:** Add one marker-relative reconstruction helper in `server_setup.py`. It reads the current Approval/Core configuration and exact canonical-owner evidence under existing private-file custody checks, reverses only the evidence-bound Approval and Core owner/signer fields plus the known TTL hotfix in memory, and accepts the source marker only when both reconstructed digests match. All mutation remains in the existing recovery/upgrade journals, TTL migration, canonical-owner recovery command, and Core policy cutover.

**Tech Stack:** Python 3.13, Pydantic v2, pytest, AgentNet setup journals and canonical JSON digests.

## Global Constraints

- Only the exact `0.1.50 -> 0.1.51` transition may use this reconstruction.
- Caller identity and target authority remain derived from the fixed setup request, enrolled identity, and signed recovery journal; payload prose grants nothing.
- Missing, incomplete, malformed, cross-domain, wrong-target, signer-mismatched, or digest-mismatched state fails closed before host mutation.
- Do not add a generic migration or manual database/config rewrite command.
- Recovery and rollback continue through the existing root-owned setup upgrade journal.
- Affected IDs: `ID-001`, `ID-002`, `ID-005`, `ID-006`, `ID-009`, `AUTH-001..005`, `COM-001`, `COM-002`, `COM-009`, `AVL-003`, `AVL-005`, `AVL-006`, `SEC-003`, `SEC-005`, `SEC-007`, `OPS-003`, `OPS-006`.

---

### Task 1: Combined-state marker reconstruction

**Files:**
- Modify: `src/agentnet/operations/server_setup.py:4680-4720,5541-5745,6504-6522`
- Test: `tests/operations/test_server_setup_recovery.py`

**Interfaces:**
- Consumes: retained setup marker; managed Approval/Core paths and accounts; Approval state path; fixed `ServerSetupRequest`.
- Produces: a private helper returning the pair `(approval_digest, core_digest)` used by `_require_marker_realized_state`.

- [ ] **Step 1: Write the failing combined-state test**

Create an exact v0.1.50 harness state, retain its marker, materialize the known one-hour TTL hotfix, then materialize a completed canonical-owner recovery journal and target owner/signer fields in Approval while Core remains marker-identical. Assert v0.1.51 apply succeeds, normalizes ordinary request TTL to `600`, retains communication-scope TTL `3600`, verifies canonical authority as already exact, advances the marker, and clears the setup upgrade journal.

- [ ] **Step 2: Write fail-closed tests**

Parameterize changes to the completed journal and realized documents: incomplete phase, wrong domain/target, source signer mismatch, extra Approval field drift, and extra Core OIDC drift. Assert `ServerSetupError`, unchanged managed bytes, unchanged v0.1.50 marker, and no new setup upgrade journal.

- [ ] **Step 3: Run tests and verify the new positive test fails before mutation**

Run:

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/operations/test_server_setup_recovery.py -k 'combined_owner_ttl or combined_recovery'
```

Expected: the positive case fails at `setup_upgrade_conflict`; negative cases remain fail-closed.

- [ ] **Step 4: Implement minimal reconstruction**

Add a helper that:

1. reads Approval and Core JSON through `_read_private_managed_file`;
2. returns realized digests immediately when both equal the marker;
3. requires the exact package edge and a completed canonical-owner journal bound to the fixed request;
4. validates the realized target approver and Core trust entry against the journal target signer;
5. replaces only target Approval principal/key/path fields with journal source values in a copy;
6. reverses only the exact one-hour TTL shape to the published v0.1.50 shape;
7. validates the reconstructed Approval and Core models; and
8. returns marker digests only when both reconstructed canonical digests match, otherwise fails closed so the existing gate rejects.

Pass the fixed request and Approval state into `_prepare_supported_upgrade`; do not mutate files in this helper.

- [ ] **Step 5: Run focused positive and negative tests**

Run the command from Step 3. Expected: all selected tests pass.

### Task 2: Authority and recovery documentation

**Files:**
- Modify: `docs/superpowers/specs/2026-08-09-agentnet-canonical-owner-recovery-design.md`
- Modify: `docs/implementation-guide.md`
- Modify: `docs/SCHEMAS_INTERFACES.md`
- Modify: `REQUIREMENTS_STATUS.md`
- Modify: `docs/GATE_EVIDENCE.md`

**Interfaces:**
- Consumes: verified runtime behavior from Task 1.
- Produces: exact operator-facing and evidence-ledger wording without gate promotion.

- [ ] **Step 1: Document the combined source state**

State that only a completed canonical-owner journal can explain target owner/signer drift relative to the retained marker, and only the exact known TTL hotfix can explain TTL drift. Both Approval and Core reconstructed digests must match before journal creation.

- [ ] **Step 2: Document fail-closed and rollback behavior**

State that extra drift, incomplete journals, identity mismatches, or signer mismatches block before mutation; after journal creation, existing compare-and-swap resume/rollback semantics apply.

- [ ] **Step 3: Update evidence wording without promotion**

Record focused hermetic coverage only. Keep PostgreSQL/live convergence and every must-not-ship gate non-green until exact external evidence exists.

### Task 3: Verification and delivery

**Files:**
- Modify generated release artifacts only through the repository's release assembly procedure.

**Interfaces:**
- Consumes: Tasks 1-2.
- Produces: a reproducible exact candidate commit for server validation.

- [ ] **Step 1: Run focused recovery suites**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/operations/test_server_setup_recovery.py tests/operations/test_canonical_owner_recovery.py
```

- [ ] **Step 2: Run the broad source gate**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run --extra test pytest -q --ignore=tests/adapters/test_installed_live_inference.py --ignore=tests/adapters/test_subprocess_lifecycle.py --ignore=tests/components/test_bakeoff_evidence.py --ignore=tests/conformance/test_release_manifest.py
```

- [ ] **Step 3: Reassemble release evidence in repository order**

Update verifier counts and release-input hashes, build two isolated package generations with `SOURCE_DATE_EPOCH`, bind artifact hashes, then run:

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run python scripts/verify_release.py
npm run check
```

- [ ] **Step 4: Run AgentNet self-verification**

```bash
PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache uv run agentnet verify
```

- [ ] **Step 5: Review, commit, push, and request disposable server validation**

Commit only after focused and release gates pass. Push the exact commit and instruct the server operator to validate it in a new disposable PostgreSQL environment before any live apply.
