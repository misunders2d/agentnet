# Post-C0 Managed-Server Credential Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reauthorize an expired communication-only managed-server credential after C0 without changing the original C0 terminal evidence.

**Architecture:** Add a strict Core-owned hash-chained supersession journal beside the immutable C0 evidence boundary. Request-v2 binds the terminal and prior-journal digests into same-key proof and owner WebAuthn approval. The CLI serializes with setup, commits PostgreSQL audit first, then reconciles journal/config/identity; Core bootstrap cross-checks every journal entry against the authoritative audit chain before setup accepts it.

**Tech Stack:** Python 3.13, Pydantic v2, SQLite/PostgreSQL storage contracts, descriptor-safe POSIX files, pytest.

## Global Constraints

- Preserve `/var/lib/agentnet-c0/terminal.json` byte-for-byte.
- Store supersessions at `/var/lib/agentnet/credential-supersessions.json`, Core-owned mode 0600; the C0 account cannot write it.
- Keep A2A/relay post-C0 recovery refused.
- Require same-key proof, fresh owner WebAuthn-UV Approval, current actor eligibility, and an expired active credential.
- Grant no authority and restart no service.
- Preserve request-v1 canonical bytes and pending pre-C0 compatibility.
- Hold the permanent setup lock for the entire ceremony.
- Fail closed on file drift, broken chains, missing/conflicting audit records, or uncertain state.
- Affected requirement: ID-009. Promote no gate or production claim.

---

### Task 1: Strict supersession journal and audit verifier

**Files:**
- Create: `src/agentnet/operations/c0_credential_supersession.py`
- Create: `tests/operations/test_c0_credential_supersession.py`

**Interfaces:**
- Produces `C0CredentialSupersessionEntry`, `C0CredentialSupersessionJournal`, `validated_terminal_marker`, `load_supersession_journal`, `append_supersession`, and `verify_supersession_audit(store, journal)`.

- [ ] **Step 1: Write failing strict-chain tests**

Test one and two transitions plus rejection of extras, wrong terminal digest/ID, domain/harness drift, reordering, duplicate links, skipped epochs, changed key, invalid validity, and altered entry digest.

```python
def test_chain_starts_at_immutable_terminal() -> None:
    terminal = terminal_bytes(credential_id="credential-1")
    journal = append_supersession(
        terminal_raw=terminal,
        existing=None,
        request_id=REQUEST_ID,
        transaction_sha256="a" * 64,
        approval_receipt_id="receipt-1",
        approval_receipt_sha256="b" * 64,
        audit_record_hash="c" * 64,
        previous_credential_id="credential-1",
        credential_id="credential-2",
        previous_credential_epoch=1,
        credential_epoch=2,
        key_id="key-1",
        not_before=100,
        expires_at=200,
    )
    assert journal.terminal_sha256 == hashlib.sha256(terminal).hexdigest()
    assert journal.current_credential == ("credential-2", 2)
```

- [ ] **Step 2: Write failing authoritative-audit tests**

Seed `audit_log` with an exact `credential.managed_server_reauthorized` record and require every entry to match by record hash, request/approval digests, predecessor/successor, epochs, key, validity, terminal digest, and prior-journal digest. An internally valid journal with a missing or conflicting audit row must fail.

- [ ] **Step 3: Verify RED**

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q tests/operations/test_c0_credential_supersession.py
```

Expected: import failure because the module does not exist.

- [ ] **Step 4: Implement strict models and verification**

```python
class C0CredentialSupersessionEntry(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)
    request_id: str
    transaction_sha256: HexDigest
    approval_receipt_id: str
    approval_receipt_sha256: HexDigest
    audit_record_hash: HexDigest
    previous_credential_id: str
    credential_id: str
    previous_credential_epoch: int = Field(ge=1)
    credential_epoch: int = Field(ge=2)
    key_id: str
    not_before: int = Field(ge=0)
    expires_at: int = Field(ge=1)
    previous_entry_sha256: HexDigest
    entry_sha256: HexDigest
```

The journal has strict schema `agentnet.c0-pilot-responder.credential-supersessions.v1`, domain/harness, terminal digest/credential, and a nonempty tuple of entries. Digest canonical entry JSON excluding only `entry_sha256`; first predecessor digest is terminal SHA-256; later predecessor digest is prior entry digest. Require exact credential/epoch/key continuity and `expires_at > not_before`.

`verify_supersession_audit` first requires `store.verify_audit_chain()` success, maps exact `record_hash` rows, parses canonical `record_json`, and compares every security field. It returns content-free `{journal_sha256, transition_count, audit_records_verified, credential_id, credential_epoch}`.

- [ ] **Step 5: Verify GREEN and commit**

Run Step 3; expect all tests pass.

```bash
git add src/agentnet/operations/c0_credential_supersession.py tests/operations/test_c0_credential_supersession.py
git commit -m "feat: model C0 credential supersessions"
```

---

### Task 2: Request-v2 and authoritative audit result

**Files:**
- Modify: `src/agentnet/identity/credentials.py:147-155,369-707`
- Modify: `tests/identity/test_credential_rotation.py:114-280`
- Modify: `src/agentnet/approval/transaction_summary.py`
- Modify: `tests/approval/test_transaction_summary.py`

**Interfaces:**
- Produces `ManagedServerCredentialReauthorizationRequestV2`, a v1/v2 union, and `audit_record_hash` in `CredentialReauthorizationResult`.

- [ ] **Step 1: Freeze current v1 bytes and add failing v2 tests**

Use the fixed v1 request vector from the design branch and assert v2 changes to terminal/prior-journal digest invalidate possession and Approval proof while v1 bytes remain unchanged.

```python
assert sha256(v1.canonical_transaction).hexdigest() == (
    "f4e57f246f70f7d53fc354f9fe9fe8015fa53cac39313aab183a16e0b117887b"
)
assert request_v2.transaction_fields()["c0_terminal_sha256"] == "c" * 64
```

- [ ] **Step 2: Verify RED**

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q tests/identity/test_credential_rotation.py tests/approval/test_transaction_summary.py
```

Expected: v2 model and summary assertions fail.

- [ ] **Step 3: Implement v2 without altering v1**

```python
class ManagedServerCredentialReauthorizationRequestV2(
    ManagedServerCredentialReauthorizationRequest
):
    schema_version: Literal[
        "agentnet.managed-server-credential-reauthorization.v2"
    ] = Field(alias="schema")
    c0_terminal_sha256: HexDigest
    c0_supersession_sha256: HexDigest | None
    current_chain_credential_id: str
    current_chain_credential_epoch: int = Field(ge=1)
```

Override `transaction_fields()` to sign all four new fields. Service v2 requires chain ID/epoch equal the actor's expired binding. Audit details include request ID, transaction digest, Approval receipt ID/digest, old/new IDs and epochs, key, validity, terminal digest, and prior-journal digest.

Capture `audit_record_hash = store.append_audit(...)` and return it. The idempotent path finds the one exact matching audit record and returns its hash; none or multiple fail closed.

- [ ] **Step 4: Update owner summary, verify GREEN, commit**

Summary names immutable C0 origin and exact epoch transition, without secrets.

Run Step 2; expect all pass.

```bash
git add src/agentnet/identity/credentials.py src/agentnet/approval/transaction_summary.py tests/identity/test_credential_rotation.py tests/approval/test_transaction_summary.py
git commit -m "feat: bind post-C0 credential approval"
```

---

### Task 3: Core bootstrap and setup provenance validation

**Files:**
- Modify: `src/agentnet/core/app.py`
- Modify: `src/agentnet/operations/server_setup.py:84-123,3164-3220,6394-6440`
- Modify: `tests/core/test_app.py`
- Modify: `tests/operations/test_server_setup.py:3923-3975`
- Modify: `tests/operations/test_server_setup_recovery.py`

**Interfaces:**
- Adds fixed `C0_CREDENTIAL_SUPERSESSIONS = CORE_DATA / "credential-supersessions.json"`.
- Core bootstrap returns `credential_supersession` evidence from Task 1's audit verifier.
- Setup cross-checks root-read terminal/journal evidence against Core's database-backed evidence.

- [ ] **Step 1: Write failing Core audit-evidence tests**

With valid journal plus exact audit rows, require content-free evidence. Mutate one audit field while leaving journal internally hash-valid; bootstrap must fail. Assert no journal reports `not_applicable` only when config still names the terminal credential.

- [ ] **Step 2: Write failing setup tests**

Preserve terminal bytes, change managed config/identity to replacement, and show setup rejects without journal. Add valid Core-owned journal/audit and show acceptance. Reject C0-owned journal, symlink/nonregular/multilink, wrong terminal digest, relabeled origin, chain gap, stale tail, or Core evidence mismatch.

- [ ] **Step 3: Verify RED**

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q tests/core/test_app.py tests/operations/test_server_setup.py tests/operations/test_server_setup_recovery.py
```

Expected: absent provenance/evidence behavior fails.

- [ ] **Step 4: Implement Core and setup checks**

Core loads only the fixed Core-owned 0600 journal path and calls `verify_supersession_audit`. Setup root keeps existing strict terminal validation, validates journal origin/chain, then requires Core evidence fields to equal the same journal digest/count/tail. Existing managed identity, database binding, health, and readiness checks remain mandatory.

- [ ] **Step 5: Verify GREEN and commit**

Run Step 3; expect all pass.

```bash
git add src/agentnet/core/app.py src/agentnet/operations/server_setup.py tests/core/test_app.py tests/operations/test_server_setup.py tests/operations/test_server_setup_recovery.py
git commit -m "feat: verify C0 supersession provenance"
```

---

### Task 4: Locked crash-recoverable CLI ceremony

**Files:**
- Modify: `src/agentnet/cli.py:1503-2055`
- Modify: `tests/cli/test_server_agent_activation.py:399-641`

**Interfaces:**
- Consumes request-v2, audit hash, journal builder, fixed setup lock, and Core account custody.
- Produces exact `c0_supersession`, `config`, and `identity` reconciliation statuses.

- [ ] **Step 1: Write failing lock, post-C0, and crash tests**

A second process holding the setup lock must make reauthorization fail before broker/database calls. Post-C0 success keeps terminal bytes unchanged and creates one Core-owned journal entry. Inject failure after DB, journal, config, identity, and state deletion response loss; retry must retain one request, Approval, audit row, journal entry, and credential.

```python
assert broker.creates == 1
assert len(service_request_ids) == 1
assert len(journal.entries) == 1
assert terminal_after == terminal_before
assert config_id == identity_id == journal.current_credential[0]
```

Add file-drift cases that overwrite nothing.

- [ ] **Step 2: Verify RED**

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q tests/cli/test_server_agent_activation.py
```

Expected: current retained-terminal guard and missing lock/reconciliation fail tests.

- [ ] **Step 3: Wrap command with permanent setup lock**

```python
def command_server_agent_reauthorize_expired_credential(args: argparse.Namespace) -> int:
    with _managed_server_setup_lock():
        return _command_server_agent_reauthorize_expired_credential_locked(args)
```

Validate existing lock inode as root/root, regular, single-link, mode 0600, no-follow; take nonblocking exclusive lock and retain descriptor through output. Never replace the lock inode.

- [ ] **Step 4: Select exact request/state version**

Preserve pre-C0 v1. For retained C0, validate immutable marker and existing journal against current actor, then create v2 binding exact terminal/prior-journal digests and chain tail. Resume parses state schema then exact request version. Re-read and hash both inputs before Approval creation and before filesystem commit.

- [ ] **Step 5: Reconcile after PostgreSQL commit**

Install the new Core-owned journal first with absent `O_EXCL` creation or exact-digest CAS; fully write, file-fsync, atomic rename where applicable, directory-fsync, owner/mode validation, and exact reread. Exact successor bytes report `reconciled`. Then run existing config and identity CAS and remove pending state only after all successors verify.

Output includes:

```json
{
  "status": "completed",
  "c0_supersession": "updated|reconciled|not_applicable",
  "config": "updated|reconciled",
  "identity": "updated|reconciled",
  "authority_granted": false,
  "service_restart": "not_performed"
}
```

- [ ] **Step 6: Verify GREEN and commit**

Run Step 2; expect all pass.

```bash
git add src/agentnet/cli.py tests/cli/test_server_agent_activation.py
git commit -m "feat: recover post-C0 server credentials"
```

---

### Task 5: End-to-end recovery boundaries

**Files:**
- Modify: `tests/integration/test_persistent_communication_journey.py`
- Modify: `tests/operations/test_fail_closed_config.py`

**Interfaces:**
- Proves existing communication authority/content state is unchanged while only credential lifecycle advances.

- [ ] **Step 1: Add integration journey and fail-closed cases**

Create a completed-C0 communication-only server, expire credential, approve v2, recover, run setup validation, and compare protected-state digest before/after. Add missing journal, journal/audit conflict, stale tail, unsafe custody, and response-loss cases.

```python
before = protected_state_digest(store)
result = run_post_c0_reauthorization(...)
assert result["authority_granted"] is False
assert protected_state_digest(store) == before
assert current_actor.credential_epoch == old_epoch + 1
```

- [ ] **Step 2: Run affected suite**

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q tests/identity/test_credential_rotation.py tests/approval/test_transaction_summary.py tests/operations/test_c0_credential_supersession.py tests/core/test_app.py tests/cli/test_server_agent_activation.py tests/operations/test_server_setup.py tests/operations/test_server_setup_recovery.py tests/integration/test_persistent_communication_journey.py tests/operations/test_fail_closed_config.py
```

Expected: all pass; only existing declared environment skips.

- [ ] **Step 3: Commit integration coverage**

```bash
git add tests/integration/test_persistent_communication_journey.py tests/operations/test_fail_closed_config.py
git commit -m "test: prove post-C0 recovery boundaries"
```

---

### Task 6: Documentation, manifest, and release verification

**Files:**
- Modify: `docs/ARCHITECTURE.md`, `docs/SCHEMAS_INTERFACES.md`, `docs/implementation-guide.md`
- Modify: `REQUIREMENTS_STATUS.md`, `docs/RELEASE_MANIFEST.md`, `PUBLIC_RELEASE_STATUS.md`
- Modify: `RELEASE_MANIFEST.json` through the repository manifest procedure only.

**Interfaces:**
- Produces honest operator and release truth; promotes no gate.

- [ ] **Step 1: Update behavior documentation**

Document immutable terminal evidence, Core-owned audited journal, v2 approval, setup lock, crash retry, communication-only scope, unchanged A2A/relay refusal, and exact operator sequence: run reauthorization, approve WebAuthn, rerun exact command, then run the setup `--apply --start` command named by its result. Forbid manual terminal/journal/config/identity/database edits.

- [ ] **Step 2: Run focused and full verification**

Run Task 5 Step 2, then:

```bash
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run pytest -q
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run python scripts/verify_release.py
UV_CACHE_DIR=/tmp/uv-cache UV_LINK_MODE=copy uv run agentnet verify
```

Expected: zero failures; only declared skips; both verifiers exit zero. Update manifest hashes only through the current repository procedure, then rerun.

- [ ] **Step 3: Commit and push verified implementation**

```bash
git diff --check
git status --short
git add docs/ARCHITECTURE.md docs/SCHEMAS_INTERFACES.md docs/implementation-guide.md REQUIREMENTS_STATUS.md docs/RELEASE_MANIFEST.md PUBLIC_RELEASE_STATUS.md RELEASE_MANIFEST.json
git commit -m "docs: document post-C0 credential recovery"
git push -u origin fix/v0146-post-c0-credential-recovery
```

Report exact commit, tests/skips, release verifier, and `agentnet verify`. Only then give the server operator the exact package-owned update/recovery commands.
