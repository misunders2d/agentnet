from __future__ import annotations

import json
from pathlib import Path

import pytest
import agentnet.operations.canonical_owner_recovery as recovery

from agentnet.approval.config import (
    MANDATORY_APPROVAL_PURPOSES,
    ApprovalOwnerOIDCConfig,
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
)
from agentnet.approval.store import ApprovalStore, approval_user_handle
from agentnet.errors import GateBlocked
from agentnet.operations.canonical_owner_recovery import (
    CanonicalOwnerAdoptionRequest,
    converge_canonical_approval_owner,
    adopt_canonical_approval_owner,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, b64url_encode


NOW = 1_800_000_000
DOMAIN = "corp.example"
SOURCE = "sergey-owner"
VERIFIER_ID = "approval.corp.example"
TARGET = "6fac7b4c-de08-4192-9f6a-ef29b5ae6b0"
ISSUER = "https://accounts.example/oidc"
SUBJECT = "oidc-sergey-subject"
EMAIL = "sergey@corp.example"
RP_ID = "approval.corp.example"


def _store(tmp_path: Path) -> ApprovalStore:
    root = tmp_path / "approval"
    root.mkdir(mode=0o700)
    root.chmod(0o700)
    path = root / "approval.sqlite3"
    path.touch(mode=0o600)
    path.chmod(0o600)
    store = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32), initialize=True)
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO approval_owner_bindings(
                   binding_id,domain_id,approver_principal_id,oidc_issuer,oidc_subject,
                   verified_email,pin_source,status,pinned_at
               ) VALUES(?,?,?,?,?,?,'exact_subject','active',?)""",
            ("binding-owner", DOMAIN, SOURCE, ISSUER, SUBJECT, EMAIL, NOW - 10_000),
        )
        connection.execute(
            """INSERT INTO approval_webauthn_credentials(
                   credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                   credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
            (
                "active-credential",
                SOURCE,
                DOMAIN,
                b64url_encode(
                    approval_user_handle(
                        verifier_id=VERIFIER_ID,
                        principal_id=SOURCE,
                        domain_id=DOMAIN,
                    )
                ),
                "active-public-key",
                17,
                "single_device",
                1,
                "active",
                NOW - 9_000,
            ),
        )
        connection.execute(
            """INSERT INTO approval_webauthn_credentials(
                   credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                   credential_public_key_b64,sign_count,device_type,backed_up,status,
                   created_at,revoked_at,revocation_reason
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "historical-credential",
                SOURCE,
                DOMAIN,
                b64url_encode(b"historical-user-handle"),
                "historical-public-key",
                3,
                "single_device",
                0,
                "revoked",
                NOW - 20_000,
                NOW - 15_000,
                "rotated",
            ),
        )
        connection.execute(
            """INSERT INTO approval_requests(
                   request_id,approver_principal_id,domain_id,approval_purpose,capability_hash,
                   canonical_transaction_encrypted,transaction_digest,state,active_fingerprint,
                   created_at,expires_at,delivery_mode
               ) VALUES(?,?,?,?,?,?,?,'issued',NULL,?,?,'direct_receipt')""",
            (
                "historical-request",
                SOURCE,
                DOMAIN,
                "core.enrollment",
                "1" * 64,
                "encrypted-transaction",
                "2" * 64,
                NOW - 8_000,
                NOW - 7_000,
            ),
        )
        connection.execute(
            """INSERT INTO approval_issued_receipts(
                   request_id,credential_id_b64,authenticated_at,issued_at,
                   receipt_expires_at,receipt_encrypted,receipt_digest
               ) VALUES(?,?,?,?,?,?,?)""",
            (
                "historical-request",
                "historical-credential",
                NOW - 7_500,
                NOW - 7_499,
                NOW - 7_000,
                "encrypted-receipt",
                "3" * 64,
            ),
        )
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            (
                "approval.issued",
                "historical-request",
                SOURCE,
                DOMAIN,
                "core.enrollment",
                "2" * 64,
                NOW - 7_499,
                "issued",
                "webauthn_verified",
            ),
        )
    return store


def _request(**updates: object) -> CanonicalOwnerAdoptionRequest:
    values: dict[str, object] = {
        "schema": "agentnet.canonical-owner-adoption.v1",
        "recovery_id": "93756ff6-6337-4ed1-9697-250b63fb68a2",
        "domain_id": DOMAIN,
        "source_principal_id": SOURCE,
        "target_principal_id": TARGET,
        "oidc_issuer": ISSUER,
        "oidc_subject": SUBJECT,
        "verified_email": EMAIL,
        "verifier_id": VERIFIER_ID,
        "approved_at": NOW - 100,
    }
    values.update(updates)
    return CanonicalOwnerAdoptionRequest.model_validate(values)

def _profile(tmp_path: Path) -> tuple[ApprovalStore, ApprovalServiceConfig, Path, Path]:
    store = _store(tmp_path)
    data_dir = tmp_path / "approval"
    signer_dir = data_dir / "signers"
    signer_dir.mkdir(mode=0o700)
    signer = P256KeyPair.generate()
    signer_path = signer_dir / "approver-1.pem"
    signer_path.write_bytes(signer.private_pem)
    signer_path.chmod(0o600)
    secrets_dir = data_dir / "secrets"
    secrets_dir.mkdir(mode=0o700)
    record_key_path = secrets_dir / "records.key"
    record_key_path.write_bytes(b"r" * 32)
    record_key_path.chmod(0o600)
    config = ApprovalServiceConfig(
        public_origin="https://approval.corp.example",
        rp_id=RP_ID,
        verifier_id=VERIFIER_ID,
        data_dir=data_dir,
        database_path=data_dir / "approval.sqlite3",
        record_key_path=record_key_path,
        owner_oidc=ApprovalOwnerOIDCConfig(
            issuer=ISSUER,
            client_id="approval-client",
            redirect_uri="https://approval.corp.example/v1/approval/owner/oidc/callback",
        ),
        approvers=(
            ApprovalServiceApproverConfig(
                principal_id=SOURCE,
                domain_id=DOMAIN,
                signer_key_id=signer.thumbprint,
                signer_private_key_path=signer_path,
                allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
                oidc_issuer=ISSUER,
                oidc_subject=SUBJECT,
            ),
        ),
    )
    config_path = data_dir / "config.json"
    config_path.write_text(
        config.model_dump_json(indent=2) + "\n",
        encoding="utf-8",
    )
    config_path.chmod(0o600)
    return store, config, config_path, data_dir / "canonical-owner-recovery.json"


def test_profile_recovery_rotates_signer_and_converges(tmp_path: Path) -> None:
    store, config, config_path, journal_path = _profile(tmp_path)
    source_signer = config.approvers[0].signer_private_key_path.read_bytes()
    try:
        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW,
        )
        replay = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        recovered = ApprovalServiceConfig.model_validate_json(config_path.read_text())
        assert result["status"] == "recovered"
        assert replay == {**result, "status": "already_exact"}
        assert recovered.approvers[0].principal_id == TARGET
        assert recovered.approvers[0].signer_key_id != config.approvers[0].signer_key_id
        assert recovered.approvers[0].signer_private_key_path.read_bytes() != source_signer
        assert not config.approvers[0].signer_private_key_path.exists()
        assert recovered.approvers[0].signer_private_key_path.exists()
        assert journal_path.exists()
        assert not (config.data_dir / "canonical-owner-recovery.backup.pem").exists()
    finally:
        store.close()


def test_profile_recovery_rejects_email_alias_without_exact_subject(
    tmp_path: Path,
) -> None:
    store, config, config_path, journal_path = _profile(tmp_path)
    alias_only = config.approvers[0].model_copy(
        update={
            "oidc_subject": None,
            "verified_email_alias": EMAIL,
        }
    )
    config_path.write_text(
        config.model_copy(update={"approvers": (alias_only,)}).model_dump_json(indent=2)
        + "\n",
        encoding="utf-8",
    )
    try:
        with pytest.raises(GateBlocked, match="configured owner state is ambiguous"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
            )
        assert not journal_path.exists()
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings "
            "WHERE binding_id='binding-owner'"
        )[0] == SOURCE
    finally:
        store.close()


def test_profile_recovery_resumes_after_prepared_journal_crash(
    tmp_path: Path,
) -> None:
    store, config, config_path, journal_path = _profile(tmp_path)
    target_path = config.data_dir / "signers" / "canonical-owner-recovery.pem"
    try:
        with pytest.raises(RuntimeError, match="injected recovery interruption"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
                _interrupt_after="prepared_journal",
            )
        assert journal_path.exists()
        assert not target_path.exists()

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        assert result["status"] == "recovered"
        assert ApprovalServiceConfig.model_validate_json(
            config_path.read_text(encoding="utf-8")
        ).approvers[0].principal_id == TARGET
    finally:
        store.close()


def test_profile_recovery_preserves_counts_after_authority_commit_crash(
    tmp_path: Path,
) -> None:
    store, _config, config_path, journal_path = _profile(tmp_path)
    try:
        with pytest.raises(RuntimeError, match="injected recovery interruption"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
                _interrupt_after="authority_committed",
            )
        journal = json.loads(journal_path.read_text(encoding="utf-8"))
        assert journal["phase"] == "prepared"
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings "
            "WHERE binding_id='binding-owner'"
        )[0] == TARGET

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        assert result["authority_adoption"] == {
            "schema": "agentnet.canonical-owner-adoption-result.v1",
            "status": "already_exact",
            "recovery_id": "93756ff6-6337-4ed1-9697-250b63fb68a2",
            "migrated_active_credentials": 1,
            "revoked_browser_sessions": 0,
            "canceled_registration_ceremonies": 0,
        }
    finally:
        store.close()


def test_profile_recovery_resumes_after_authority_commit(tmp_path: Path) -> None:
    store, _config, config_path, journal_path = _profile(tmp_path)
    try:
        with pytest.raises(RuntimeError, match="injected recovery interruption"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
                _interrupt_after="authority_adopted",
            )
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings WHERE binding_id='binding-owner'"
        )[0] == TARGET
        assert ApprovalServiceConfig.model_validate_json(
            config_path.read_text()
        ).approvers[0].principal_id == SOURCE

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        assert result["status"] == "recovered"
        assert ApprovalServiceConfig.model_validate_json(
            config_path.read_text()
        ).approvers[0].principal_id == TARGET
    finally:
        store.close()


def test_profile_recovery_resumes_after_signer_staging(tmp_path: Path) -> None:
    store, source_config, config_path, journal_path = _profile(tmp_path)
    try:
        with pytest.raises(RuntimeError, match="injected recovery interruption"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
                _interrupt_after="signer_replaced",
            )
        interrupted = ApprovalServiceConfig.model_validate_json(
            config_path.read_text(encoding="utf-8")
        )
        assert interrupted.approvers[0].principal_id == SOURCE
        assert (
            P256KeyPair.from_private_pem(
                interrupted.approvers[0].signer_private_key_path.read_bytes()
            ).thumbprint
            == interrupted.approvers[0].signer_key_id
        )

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        recovered = ApprovalServiceConfig.model_validate_json(
            config_path.read_text(encoding="utf-8")
        )
        assert result["status"] == "recovered"
        assert recovered.approvers[0].principal_id == TARGET
        assert recovered.approvers[0].signer_private_key_path != (
            source_config.approvers[0].signer_private_key_path
        )
    finally:
        store.close()


def test_profile_recovery_removes_retired_signers_before_completion(
    tmp_path: Path,
) -> None:
    store, config, config_path, journal_path = _profile(tmp_path)
    source_path = config.approvers[0].signer_private_key_path
    backup_path = config.data_dir / "canonical-owner-recovery.backup.pem"
    try:
        with pytest.raises(RuntimeError, match="injected recovery interruption"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
                _interrupt_after="retired_signers_removed",
            )
        assert not source_path.exists()
        assert not backup_path.exists()
        assert json.loads(journal_path.read_text(encoding="utf-8"))["phase"] == (
            "config_replaced"
        )

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        assert result["status"] == "recovered"
        assert not source_path.exists()
        assert not backup_path.exists()
    finally:
        store.close()


def test_profile_recovery_resumes_after_config_replace_response_loss(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    store, _source_config, config_path, journal_path = _profile(tmp_path)
    original_write = recovery._journal_write
    failed = {"value": False}

    def fail_after_config_replace(path: Path, value: dict[str, object]) -> None:
        if value.get("phase") == "config_replaced" and not failed["value"]:
            failed["value"] = True
            raise RuntimeError("injected journal response loss")
        original_write(path, value)

    monkeypatch.setattr(recovery, "_journal_write", fail_after_config_replace)
    try:
        with pytest.raises(RuntimeError, match="injected journal response loss"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW,
            )
        interrupted = ApprovalServiceConfig.model_validate_json(
            config_path.read_text(encoding="utf-8")
        )
        assert interrupted.approvers[0].principal_id == TARGET
        assert json.loads(journal_path.read_text(encoding="utf-8"))["phase"] == (
            "config_replacing"
        )

        result = converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW + 1,
        )
        assert result["status"] == "recovered"
    finally:
        store.close()


def test_profile_recovery_rejects_tampered_completed_journal(tmp_path: Path) -> None:
    store, _config, config_path, journal_path = _profile(tmp_path)
    try:
        converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW,
        )
        journal = json.loads(journal_path.read_text(encoding="utf-8"))
        journal["target_signer_key_id"] = "tampered-signer-key"
        journal_path.write_text(
            json.dumps(journal, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        with pytest.raises(GateBlocked, match="signer evidence is invalid"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW + 1,
            )
    finally:
        store.close()


def test_private_read_rejects_file_swapped_to_symlink(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state = tmp_path / "state.json"
    alternate = tmp_path / "alternate.json"
    state.write_bytes(b"safe")
    state.chmod(0o600)
    alternate.write_bytes(b"unsafe")
    alternate.chmod(0o600)
    original_open = recovery.os.open
    swapped = False

    def swap_before_open(path: object, *args: object, **kwargs: object):
        nonlocal swapped
        if path == state.name and kwargs.get("dir_fd") is not None and not swapped:
            swapped = True
            state.unlink()
            state.symlink_to(alternate)
        return original_open(path, *args, **kwargs)

    monkeypatch.setattr(recovery.os, "open", swap_before_open)
    with pytest.raises(GateBlocked, match="recovery state is unavailable"):
        recovery._private_read(state, maximum=32)


def test_private_write_does_not_follow_replaced_parent(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    parent = tmp_path / "state"
    parent.mkdir(mode=0o700)
    moved_parent = tmp_path / "original-state"
    alternate = tmp_path / "alternate"
    alternate.mkdir(mode=0o700)
    target = parent / "journal.json"
    original_replace = recovery.os.replace
    swapped = False

    def swap_parent(
        source: object,
        destination: object,
        *args: object,
        **kwargs: object,
    ) -> None:
        nonlocal swapped
        if not swapped:
            swapped = True
            parent.rename(moved_parent)
            parent.symlink_to(alternate, target_is_directory=True)
        original_replace(source, destination, *args, **kwargs)

    monkeypatch.setattr(recovery.os, "replace", swap_parent)
    with pytest.raises(GateBlocked, match="recovery path changed"):
        recovery._private_write(target, b"protected")
    assert not (alternate / target.name).exists()


def test_profile_recovery_rejects_adoption_evidence_from_another_recovery(
    tmp_path: Path,
) -> None:
    store, _config, config_path, journal_path = _profile(tmp_path)
    try:
        converge_canonical_approval_owner(
            store,
            config_path=config_path,
            journal_path=journal_path,
            request=_request(),
            now=NOW,
        )
        journal = json.loads(journal_path.read_text(encoding="utf-8"))
        journal["authority_adoption"]["recovery_id"] = (
            "d47b5211-385a-4581-9284-5af35d4fe196"
        )
        journal["authority_adoption_digest"] = recovery.hashlib.sha256(
            recovery.canonical_json(journal["authority_adoption"])
        ).hexdigest()
        journal_path.write_text(
            json.dumps(journal, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        with pytest.raises(GateBlocked, match="authority adoption evidence conflicts"):
            converge_canonical_approval_owner(
                store,
                config_path=config_path,
                journal_path=journal_path,
                request=_request(),
                now=NOW + 1,
            )
    finally:
        store.close()



def test_adoption_moves_only_current_owner_authority_and_is_idempotent(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        before_active = dict(
            store.fetch_one(
                "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
                ("active-credential",),
            )
        )
        historical_request = dict(
            store.fetch_one("SELECT * FROM approval_requests WHERE request_id='historical-request'")
        )
        historical_audit = dict(
            store.fetch_one("SELECT * FROM approval_audit WHERE action='approval.issued'")
        )

        result = adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        replay = adopt_canonical_approval_owner(store, request=_request(), now=NOW + 1)

        assert result == {
            "schema": "agentnet.canonical-owner-adoption-result.v1",
            "status": "adopted",
            "recovery_id": "93756ff6-6337-4ed1-9697-250b63fb68a2",
            "migrated_active_credentials": 1,
            "revoked_browser_sessions": 0,
            "canceled_registration_ceremonies": 0,
        }
        assert replay == {**result, "status": "already_exact"}
        binding = store.fetch_one("SELECT * FROM approval_owner_bindings WHERE binding_id='binding-owner'")
        assert binding is not None
        assert binding["approver_principal_id"] == TARGET
        assert binding["oidc_issuer"] == ISSUER
        assert binding["oidc_subject"] == SUBJECT
        assert binding["verified_email"] == EMAIL

        active = dict(
            store.fetch_one(
                "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
                ("active-credential",),
            )
        )
        assert active == {
            **before_active,
            "approver_principal_id": TARGET,
            "user_handle_b64": b64url_encode(
                approval_user_handle(
                    verifier_id=VERIFIER_ID,
                    principal_id=TARGET,
                    domain_id=DOMAIN,
                )
            ),
        }
        historical_credential = store.fetch_one(
            "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
            ("historical-credential",),
        )
        assert historical_credential is not None
        assert historical_credential["approver_principal_id"] == SOURCE
        assert dict(
            store.fetch_one("SELECT * FROM approval_requests WHERE request_id='historical-request'")
        ) == historical_request
        assert dict(
            store.fetch_one("SELECT * FROM approval_audit WHERE action='approval.issued'")
        ) == historical_audit
        adoption_audits = store.fetch_all(
            "SELECT * FROM approval_audit WHERE action='owner.canonical_adoption'"
        )
        assert len(adoption_audits) == 1
        assert adoption_audits[0]["approver_principal_id"] == TARGET
        assert (
            adoption_audits[0]["detail_code"]
            == "canonical_owner_adopted:v1:1:0:0"
        )
    finally:
        store.close()


def test_adoption_rejects_oidc_mismatch_without_mutation(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with pytest.raises(GateBlocked, match="source state does not match"):
            adopt_canonical_approval_owner(
                store,
                request=_request(oidc_subject="different-subject"),
                now=NOW,
            )
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings WHERE binding_id='binding-owner'"
        )[0] == SOURCE
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_webauthn_credentials WHERE credential_id_b64='active-credential'"
        )[0] == SOURCE
    finally:
        store.close()


def test_adoption_rejects_nonterminal_approval_request(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_requests(
                       request_id,approver_principal_id,domain_id,approval_purpose,capability_hash,
                       canonical_transaction_encrypted,transaction_digest,state,active_fingerprint,
                       created_at,expires_at,delivery_mode
                   ) VALUES(?,?,?,?,?,?,?,'pending',?,?,?,'direct_receipt')""",
                (
                    "pending-request",
                    SOURCE,
                    DOMAIN,
                    "communication.scope",
                    "4" * 64,
                    "encrypted-pending",
                    "5" * 64,
                    "6" * 64,
                    NOW - 10,
                    NOW + 300,
                ),
            )
        with pytest.raises(GateBlocked, match="nonterminal approval state"):
            adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings WHERE binding_id='binding-owner'"
        )[0] == SOURCE
    finally:
        store.close()


def test_adoption_revokes_owner_sessions_and_cancels_pending_registration(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_browser_sessions(
                       session_hash,owner_binding_id,csrf_secret_encrypted,rp_id,public_origin,
                       verifier_id,created_at,authenticated_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?,?,?)""",
                (
                    "7" * 64,
                    "binding-owner",
                    "encrypted-csrf",
                    RP_ID,
                    "https://approval.corp.example",
                    "approval.corp.example",
                    NOW - 50,
                    NOW - 50,
                    NOW + 300,
                ),
            )
            connection.execute(
                """INSERT INTO approval_registration_ceremonies(
                       ceremony_id,owner_binding_id,session_hash,challenge_encrypted,
                       challenge_hash,state,created_at,expires_at
                   ) VALUES(?,?,?,?,?,'pending',?,?)""",
                (
                    "pending-ceremony",
                    "binding-owner",
                    "7" * 64,
                    "encrypted-challenge",
                    "8" * 64,
                    NOW - 20,
                    NOW + 100,
                ),
            )
        result = adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        assert result["revoked_browser_sessions"] == 1
        assert result["canceled_registration_ceremonies"] == 1
        session = store.fetch_one(
            "SELECT revoked_at,revocation_reason FROM approval_browser_sessions WHERE session_hash=?",
            ("7" * 64,),
        )
        assert tuple(session) == (NOW, "canonical_owner_adoption")
        assert store.fetch_one(
            "SELECT state FROM approval_registration_ceremonies WHERE ceremony_id='pending-ceremony'"
        )[0] == "canceled"
    finally:
        store.close()


def test_adoption_rejects_preexisting_target_authority(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_webauthn_credentials(
                       credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                       credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    "target-credential",
                    TARGET,
                    DOMAIN,
                    b64url_encode(b"target-user-handle"),
                    "target-public-key",
                    0,
                    "single_device",
                    0,
                    "active",
                    NOW - 1,
                ),
            )
        with pytest.raises(GateBlocked, match="target authority already exists"):
            adopt_canonical_approval_owner(store, request=_request(), now=NOW)
    finally:
        store.close()
