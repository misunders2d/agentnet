# C0 Systemd Credential Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the package-owned C0 responder consume its exact systemd `LoadCredential` safely, then reuse the existing VM, passkey registration, and enrolled credentials to prove the first native AgentNet message.

**Architecture:** Preserve the existing owner-only config reader and introduce a dedicated credential reader with two explicit custody forms: direct owner-only input or the exact systemd credential path and metadata. Build an unpublished `0.1.40` candidate, replace only package bytes on the disposable VM, and resume the existing live journey without deleting identity or enrollment state.

**Tech Stack:** Python 3.13, pytest 9, systemd 255 `LoadCredential`, Node.js/npm package wrapper, uv 0.11.28, Ubuntu 24.04 VM.

## Global Constraints

- Preserve the existing VM, Google OIDC/passkey registration, actor IDs, credentials, PostgreSQL data, and service identities.
- Do not copy the private key, broaden permissions, weaken the owner-file validator, synthesize enrollment, or grant authority.
- Fail closed on any path, owner/group, mode, link, type, size, or read mismatch.
- Keep `0.1.40` unpublished and make no release-readiness claim.
- Affected requirements: `ID-004`, `COM-002`, `COM-003`, `SEC-006`, `OPS-001`, `OPS-006`, `OPS-007`.

---

### Task 1: Credential custody regression and fix

**Files:**
- Modify: `tests/supervisor/test_c0_pilot_responder.py`
- Modify: `src/agentnet/supervisor/c0_responder.py`

**Interfaces:**
- Consumes: systemd-provided `CREDENTIALS_DIRECTORY` and CLI `--credential` path.
- Produces: `_credential_file(path: Path, *, label: str) -> bytes`, used only by `_client` for the P-256 signing key.

- [ ] **Step 1: Write the failing systemd custody test**

Create a real P-256 PEM file named `signing-key.pem`, monkeypatch `os.fstat` to report the observed systemd metadata (`root:root`, regular, one link, `0440`), set `CREDENTIALS_DIRECTORY` to its parent, and assert `_credential_file` returns the exact bytes. Force `os.geteuid()` to a non-root service UID so the test exercises the systemd branch.

- [ ] **Step 2: Write fail-closed negative tests**

Parameterize wrong filename/parent, missing or relative `CREDENTIALS_DIRECTORY`, non-root owner/group, writable/executable/other-readable modes, non-regular type, extra link, and oversized size. Retain the existing direct owner-only credential form and symlink denial.

- [ ] **Step 3: Run tests and observe the intended failure**

Run:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/supervisor/test_c0_pilot_responder.py
```

Expected before implementation: the new systemd custody test fails because `_credential_file` does not exist or the root-owned `0440` file is rejected.

- [ ] **Step 4: Implement the dedicated reader**

Add a shared bounded descriptor read helper only if it avoids duplication without changing `_owner_file` semantics. Accept systemd custody only when all are true:

```python
credential_root = os.environ.get("CREDENTIALS_DIRECTORY")
systemd_path = (
    bool(credential_root)
    and Path(credential_root).is_absolute()
    and path.parent == Path(credential_root)
    and path.name == "signing-key.pem"
)
systemd_custody = (
    systemd_path
    and info.st_uid == 0
    and info.st_gid == 0
    and stat.S_IMODE(info.st_mode) in {0o400, 0o440}
)
```

The existing direct form remains `info.st_uid == os.geteuid()` with no group/other bits. Both forms also require regular file, one link, at most 65,536 bytes, `O_NOFOLLOW`, and an exact-size read. Change `_client` to use `_credential_file`; leave config loading on `_owner_file`.

- [ ] **Step 5: Run focused tests**

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/supervisor/test_c0_pilot_responder.py tests/operations/test_server_setup.py tests/operations/test_server_setup_recovery.py
```

Expected: all selected tests pass; dedicated PostgreSQL/host-only skips remain explicitly reported, not counted as proof.

- [ ] **Step 6: Commit the source fix**

```bash
git add src/agentnet/supervisor/c0_responder.py tests/supervisor/test_c0_pilot_responder.py
git commit -m "fix(supervisor): accept systemd C0 credentials"
```

---

### Task 2: Build and deploy unpublished 0.1.40 candidate

**Files:**
- Modify: `pyproject.toml`
- Modify: `src/agentnet/__init__.py`
- Modify: `package.json`
- Modify: `package-lock.json`
- Modify: `uv.lock`
- Modify only where current-candidate truth requires it: `README.md`, `PUBLIC_RELEASE_STATUS.md`, `REQUIREMENTS_STATUS.md`, `docs/implementation-guide.md`, `docs/GATE_EVIDENCE.md`

**Interfaces:**
- Consumes: Task 1 source fix and existing frozen `0.1.39` evidence as immutable historical evidence.
- Produces: locally packed npm/Python `0.1.40` candidate bytes; no registry publication or release tag.

- [ ] **Step 1: Change current package identity to 0.1.40**

Update the five authoritative package/lock version fields together. Keep every `0.1.39` evidence path and historical assertion unchanged; add concise current-candidate notes rather than rewriting frozen evidence.

- [ ] **Step 2: Verify version coherence and focused package tests**

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q tests/conformance/test_npm_package.py tests/cli/test_cli_diagnostics.py
npm pack --dry-run
```

Expected: Python, npm, lock, and CLI versions agree at `0.1.40`; package dry run succeeds without private runtime state.

- [ ] **Step 3: Build reproducible local package bytes**

Build wheel/sdist twice with the repository's pinned `SOURCE_DATE_EPOCH`, compare SHA-256 digests, and pack npm from the clean source tree. Do not publish or tag.

- [ ] **Step 4: Install only corrected package bytes on the same VM**

Copy the local npm tarball and wheel to the existing VM, stop AgentNet units, install under `/opt/agentnet-runtime`, and start the existing package-owned units. Do not delete `/var/lib/agentnet*`, `/etc/agentnet-secrets`, PostgreSQL state, passkeys, actor identities, or credentials.

- [ ] **Step 5: Exercise the real systemd credential boundary**

Start `agentnet-c0-responder.service` through systemd and verify it reaches `active/running` or a legitimate protocol waiting/terminal state without `credential custody is invalid`. Inspect only metadata and sanitized logs.

- [ ] **Step 6: Commit candidate identity and truthful documentation**

```bash
git add pyproject.toml src/agentnet/__init__.py package.json package-lock.json uv.lock README.md PUBLIC_RELEASE_STATUS.md REQUIREMENTS_STATUS.md docs/implementation-guide.md docs/GATE_EVIDENCE.md
git commit -m "chore: prepare 0.1.40 credential fix candidate"
```

---

### Task 3: Resume enrollment and prove first native message

**Files:**
- Retain sanitized evidence beneath the existing local activation workspace outside the repository.
- Update only evidence/status documents warranted by observed results; never upgrade external or owner gates.

**Interfaces:**
- Consumes: running corrected server, retained owner registration and server credential, independent laptop endpoint enrollment path.
- Produces: exact enrolled sender/recipient identities, native signed message ID, recipient custody/ack receipt, and revocation refusal evidence.

- [ ] **Step 1: Resume package-owned setup and guided enrollment**

Use the existing request, activation binding, and retained credentials. If package-owned resume rejects stale coordination state, fix the source-level resume contract or use the package's explicit safe recovery command; do not manually delete markers or private state.

- [ ] **Step 2: Enroll an independent laptop endpoint**

Run the documented identity-only join with a fresh laptop credential while reusing the already registered owner passkey. Confirm exact domain, principal, harness, credential, purpose, expiry, and OOB approval binding.

- [ ] **Step 3: Send and receive the first native message**

Send one signed direct message through canonical AgentNet operations, verify recipient durable/local custody truth, retrieve it as the exact recipient, acknowledge it, and verify deduplicated receipt/state transitions. Do not substitute A2A, MCP payload identity, synthetic lab actors, or a transport-only response.

- [ ] **Step 4: Verify revocation cleanup**

With explicit owner approval, revoke the test laptop credential, prove all five relevant powers are denied after revocation, and confirm sibling/server credentials remain valid.

- [ ] **Step 5: Run final applicable verification**

```bash
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q
UV_CACHE_DIR=/tmp/uv-cache uv run python scripts/verify_release.py
UV_CACHE_DIR=/tmp/uv-cache uv run agentnet verify
```

Report every pass, skip, blocker, and non-green gate exactly. A candidate tested end to end remains unpublished and not production-certified.
