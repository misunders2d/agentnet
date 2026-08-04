from __future__ import annotations

import hashlib
from datetime import UTC, datetime
from pathlib import Path
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

import pytest
from webauthn.helpers.structs import CredentialDeviceType

from agentnet.approval.config import (
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
    MANDATORY_APPROVAL_PURPOSES,
)
from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.approval.store import ApprovalStore
from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, b64url_encode, canonical_json


NOW = 1_800_000_000
PURPOSE = "identity.enrollment.approve"


def _token(url: str) -> str:
    return parse_qs(urlsplit(url).fragment)["token"][0]


def _approval_transaction(marker: str = "Owner laptop") -> bytes:
    return canonical_json(
        {
            "candidate_key": {
                "algorithm": "ES256/P-256",
                "thumbprint": "synthetic-candidate-thumbprint",
            },
            "challenge_id": "synthetic-enrollment-challenge",
            "domain_id": "corp.example",
            "expires_at": NOW + 300,
            "harness": {
                "binding_assurance": "os_bound",
                "display_name": marker,
                "kind": "pi",
                "requested_capabilities": ["message.send"],
                "requested_class": "protected_business",
            },
            "human": {
                "oidc_issuer": "https://idp.example.test",
                "oidc_subject": "synthetic-owner-subject",
                "verified_email": "owner@example.test",
            },
            "issued_at": NOW,
            "nonce": "synthetic-enrollment-nonce",
            "purpose": "human_harness_credential_binding",
            "schema": "agentnet.enrollment.challenge.v1",
        }
    )


def _stack(tmp_path: Path):
    data = tmp_path / "approval"
    secrets = data / "secrets"
    signers = data / "signers"
    secrets.mkdir(parents=True, mode=0o700)
    data.chmod(0o700)
    signers.mkdir(mode=0o700)
    signer = P256KeyPair.generate()
    signer_path = signers / "approver.pem"
    signer_path.write_bytes(signer.private_pem)
    signer_path.chmod(0o600)
    record_path = secrets / "records.key"
    record_path.write_bytes(b"r" * 32)
    record_path.chmod(0o600)
    database = data / "approval.sqlite3"
    database.touch(mode=0o600)
    config = ApprovalServiceConfig(
        public_origin="https://approval.corp.example",
        rp_id="approval.corp.example",
        verifier_id="approval.corp.example",
        data_dir=data,
        database_path=database,
        record_key_path=record_path,
        approvers=(
            ApprovalServiceApproverConfig(
                principal_id="security-owner",
                domain_id="corp.example",
                signer_key_id=signer.thumbprint,
                signer_private_key_path=signer_path,
                allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
            ),
        ),
    )
    cipher = LocalEnvelopeCipher(b"r" * 32)
    store = ApprovalStore(database, cipher, initialize=True)
    service = WebAuthnApprovalService(config, store, cipher, clock=lambda: NOW)
    return SimpleNamespace(config=config, store=store, service=service, signer=signer)


def _owner_session(stack, *, session_hash: str = "a" * 64) -> str:
    with stack.store.transaction() as connection:
        connection.execute(
            """INSERT OR IGNORE INTO approval_owner_bindings(
                   binding_id,domain_id,approver_principal_id,oidc_issuer,oidc_subject,
                   verified_email,pin_source,status,pinned_at
               ) VALUES(?,?,?,?,?,?,?,'active',?)""",
            (
                "owner-binding-1",
                "corp.example",
                "security-owner",
                "https://accounts.example",
                "owner-subject",
                "owner@corp.example",
                "exact_subject",
                NOW,
            ),
        )
        connection.execute(
            """INSERT INTO approval_browser_sessions(
                   session_hash,owner_binding_id,csrf_secret_encrypted,rp_id,public_origin,
                   verifier_id,created_at,authenticated_at,expires_at
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            (
                session_hash,
                "owner-binding-1",
                stack.service.cipher.encrypt_json(
                    {"csrf_token": "c" * 32},
                    purpose=f"approval-owner-csrf:{session_hash}",
                ),
                stack.config.rp_id,
                stack.config.public_origin,
                stack.config.verifier_id,
                NOW,
                NOW,
                NOW + 600,
            ),
        )
    return session_hash


def _register(stack, monkeypatch: pytest.MonkeyPatch) -> str:
    created = stack.service.begin_registration("security-owner")
    token = _token(created.url)
    options = stack.service.registration_options(token)
    assert options["publicKey"]["rp"]["id"] == stack.config.rp_id
    assert options["publicKey"]["authenticatorSelection"]["userVerification"] == "required"
    monkeypatch.setattr(
        "agentnet.approval.webauthn_uv.verify_registration_response",
        lambda **kwargs: SimpleNamespace(
            credential_id=b"credential-1",
            credential_public_key=b"credential-public-key",
            sign_count=0,
            credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
            credential_backed_up=False,
            user_verified=True,
        ),
    )
    result = stack.service.complete_registration(token, {"id": "ignored-by-test-seam"})
    assert result["registered"] is True
    return result["credential_id"]


def test_config_requires_exact_https_rp_and_mandatory_purpose_coverage(tmp_path: Path) -> None:
    signer = P256KeyPair.generate()
    data = tmp_path / "approval"
    approver = ApprovalServiceApproverConfig(
        principal_id="owner",
        domain_id="corp.example",
        signer_key_id=signer.thumbprint,
        signer_private_key_path=(data / "signer.pem").absolute(),
        allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
    )
    base = dict(
        public_origin="https://approval.corp.example",
        rp_id="approval.corp.example",
        verifier_id="approval.corp.example",
        data_dir=data.absolute(),
        database_path=(data / "approval.sqlite3").absolute(),
        record_key_path=(data / "secrets" / "records.key").absolute(),
        approvers=(approver,),
    )
    assert ApprovalServiceConfig(**base).rp_id == "approval.corp.example"
    assert ApprovalServiceConfig(
        **base,
        internal_core_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
    ).internal_core_credential_env == "AGENTNET_APPROVAL_CORE_TOKEN"
    with pytest.raises(ValueError, match="String should match pattern"):
        ApprovalServiceConfig(**base, internal_core_credential_env="secret-value")
    with pytest.raises(ValueError, match="RP ID"):
        ApprovalServiceConfig(**{**base, "rp_id": "other.example"})
    with pytest.raises(ValueError, match="HTTPS"):
        ApprovalServiceConfig(**{**base, "public_origin": "http://approval.corp.example"})
    missing = approver.model_copy(
        update={"allowed_purposes": frozenset({"identity.enrollment.approve"})}
    )
    with pytest.raises(ValueError, match="mandatory ceremony"):
        ApprovalServiceConfig(**{**base, "approvers": (missing,)})


def test_store_rejects_schema_metadata_tamper_on_reopen(tmp_path: Path) -> None:
    stack = _stack(tmp_path)
    path = stack.config.database_path
    cipher = stack.service.cipher
    with stack.store.transaction() as connection:
        connection.execute(
            "UPDATE approval_store_meta SET value='wrong' WHERE key='schema_catalog_sha256'"
        )
    stack.store.close()
    with pytest.raises(GateBlocked, match="metadata mismatches"):
        ApprovalStore(path, cipher)


def test_owner_bound_request_methods_keep_capability_inside_approval(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        _register(stack, monkeypatch)
        owner_session_hash = _owner_session(stack)
        transaction = _approval_transaction()
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
        )
        actionable = stack.service.actionable_requests_for_owner(
            principal_id="security-owner",
            domain_id="corp.example",
        )
        assert actionable[0]["request_id"] == created.identifier
        assert actionable[0]["state"] == "pending"
        options = stack.service.request_options_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            owner_session_hash=owner_session_hash,
        )
        assert options["canonical_transaction_text"] == transaction.decode()
        old_challenge = options["publicKey"]["challenge"]
        with stack.store.transaction() as connection:
            connection.execute(
                """UPDATE approval_browser_sessions
                      SET revoked_at=?,revocation_reason='rotated'
                    WHERE session_hash=?""",
                (NOW, owner_session_hash),
            )
        replacement_session_hash = _owner_session(stack, session_hash="b" * 64)
        replacement_options = stack.service.request_options_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            owner_session_hash=replacement_session_hash,
        )
        assert replacement_options["publicKey"]["challenge"] != old_challenge

        database = stack.config.database_path
        cipher = stack.service.cipher
        stack.store.close()
        stack.store = ApprovalStore(database, cipher)
        stack.service = WebAuthnApprovalService(
            stack.config,
            stack.store,
            cipher,
            clock=lambda: NOW,
        )

        with pytest.raises(AuthenticationError, match="approval request denied"):
            stack.service.approve_request_for_owner(
                request_id=created.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
                credential={"id": b64url_encode(b"credential-1")},
                owner_session_hash=owner_session_hash,
            )
        with pytest.raises(AuthenticationError, match="approval request denied"):
            stack.service.request_options_for_owner(
                request_id=created.identifier,
                principal_id="other-owner",
                domain_id="corp.example",
                owner_session_hash=replacement_session_hash,
            )
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        issued = stack.service.approve_request_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            credential={"id": b64url_encode(b"credential-1")},
            owner_session_hash=replacement_session_hash,
        )
        assert issued["schema"] == "agentnet.approval.claim-code.v1"
        assert "token" not in issued and "receipt" not in issued
        duplicate = stack.service.approve_request_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            credential={},
            owner_session_hash=replacement_session_hash,
        )
        assert duplicate["schema"] == "agentnet.approval.claim-code-status.v1"
        assert "claim_code" not in duplicate
        assert stack.service.actionable_requests_for_owner(
            principal_id="security-owner",
            domain_id="corp.example",
        )[0]["state"] == "issued"
    finally:
        stack.store.close()


def test_issued_code_recovery_survives_pending_request_expiry(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        _register(stack, monkeypatch)
        owner_session_hash = _owner_session(stack)
        transaction = _approval_transaction()
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
        )
        approve_at = created.expires_at - 1
        stack.service.request_options_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            owner_session_hash=owner_session_hash,
            now=approve_at,
        )
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        stack.service.approve_request_for_owner(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            credential={"id": b64url_encode(b"credential-1")},
            owner_session_hash=owner_session_hash,
            now=approve_at,
        )

        after_pending_expiry = created.expires_at + 1
        actionable = stack.service.actionable_requests_for_owner(
            principal_id="security-owner",
            domain_id="corp.example",
            now=after_pending_expiry,
        )
        assert [(item["request_id"], item["state"]) for item in actionable] == [
            (created.identifier, "issued")
        ]
        rotated = stack.service.regenerate_claim_code(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
            owner_session_hash=owner_session_hash,
            now=after_pending_expiry,
        )
        assert rotated["schema"] == "agentnet.approval.claim-code.v1"
    finally:
        stack.store.close()


def test_registration_approval_receipt_and_response_loss_are_exact_and_single(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        transaction = _approval_transaction()
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
        )
        token = _token(created.url)
        options = stack.service.request_options(token)
        assert options["canonical_transaction_text"] == transaction.decode()
        assert options["transaction_digest"] == created.transaction_digest
        assert options["publicKey"]["userVerification"] == "required"

        observed: dict[str, object] = {}

        def verify(**kwargs):
            observed.update(kwargs)
            return SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            )

        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            verify,
        )
        credential = {"id": credential_id}
        first = stack.service.approve_request(token, credential, approved=True)
        second = stack.service.approve_request(token, {}, approved=True)
        assert second == first
        assert observed["expected_origin"] == stack.config.public_origin
        assert observed["expected_rp_id"] == stack.config.rp_id
        assert observed["require_user_verification"] is True
        assert observed["credential_current_sign_count"] == 0

        trusted = TrustedApprover(
            principal_id="security-owner",
            domain_id="corp.example",
            signer_key_id=stack.signer.thumbprint,
            public_key_pem=stack.signer.public_pem,
            allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
        )
        verifier = IndependentApprovalVerifier(
            {stack.signer.thumbprint: trusted},
            verifier_id=stack.config.verifier_id,
        )
        verified = verifier.verify(
            canonical_transaction=transaction,
            approval=first,
            expected_purpose=PURPOSE,
            expected_domain_id="corp.example",
            when=datetime.fromtimestamp(NOW, UTC),
        )
        assert verified.approver_principal_id == "security-owner"
        assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM approval_issued_receipts")["n"] == 1
        assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM approval_audit WHERE action='approval.issued'")["n"] == 1
        assert stack.store.fetch_one(
            "SELECT sign_count FROM approval_webauthn_credentials WHERE credential_id_b64=?",
            (credential_id,),
        )["sign_count"] == 1
    finally:
        stack.store.close()


def test_core_request_capability_stays_encrypted_and_opens_only_on_approval_host(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        _register(stack, monkeypatch)
        transaction = canonical_json(
            {"schema": "agentnet.test-core-approval.v1", "domain_id": "corp.example"}
        )
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
        )
        token = _token(created.url)
        row = stack.store.fetch_one(
            "SELECT capability_hash,capability_encrypted,delivery_mode FROM approval_requests "
            "WHERE request_id=?",
            (created.identifier,),
        )
        assert row is not None
        assert row["delivery_mode"] == "core_claim_code"
        assert row["capability_encrypted"] is not None
        assert token not in str(row["capability_encrypted"])

        pending = stack.service.pending_requests()
        assert pending == [
            {
                "request_id": created.identifier,
                "approver_principal_id": "security-owner",
                "domain_id": "corp.example",
                "approval_purpose": PURPOSE,
                "transaction_digest": created.transaction_digest,
                "delivery_mode": "core_claim_code",
                "openable_locally": True,
                "created_at": NOW,
                "expires_at": NOW + stack.config.request_ttl_seconds,
            }
        ]
        assert stack.service.local_approval_url(created.identifier) == created.url
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_audit "
            "WHERE request_id=? AND action='approval.opened_local'",
            (created.identifier,),
        )["n"] == 1

        direct = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=canonical_json(
                {"schema": "agentnet.test-direct-approval.v1", "domain_id": "corp.example"}
            ),
        )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.local_approval_url(direct.identifier)
    finally:
        stack.store.close()


def test_core_claim_code_retrieves_same_receipt_only_for_exact_retry(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        transaction = _approval_transaction("Core retrieval laptop")
        created = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:test-request-1",
            request_expires_at=NOW + 200,
        )
        duplicate = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:test-request-1",
            request_expires_at=NOW + 200,
        )
        assert duplicate.identifier == created.identifier
        assert duplicate.duplicate is True
        with pytest.raises(ConflictError, match="idempotency conflict"):
            stack.service.create_request(
                principal_id="security-owner",
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                canonical_transaction=_approval_transaction("Changed laptop"),
                delivery_mode="core_claim_code",
                idempotency_key="core:enrollment:test-request-1",
                request_expires_at=NOW + 200,
            )
        with pytest.raises(ConflictError, match="idempotency conflict"):
            stack.service.create_request(
                principal_id="security-owner",
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                canonical_transaction=transaction,
                delivery_mode="core_claim_code",
                idempotency_key="core:enrollment:test-request-1",
                request_expires_at=NOW + 201,
            )
        with pytest.raises(ValidationError, match="expiry is invalid"):
            stack.service.create_request(
                principal_id="security-owner",
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                canonical_transaction=transaction,
                delivery_mode="core_claim_code",
                idempotency_key="core:enrollment:too-long",
                request_expires_at=NOW + stack.config.request_ttl_seconds + 1,
            )

        token = _token(created.url)
        first_options = stack.service.request_options(token)
        second_options = stack.service.request_options(token)
        assert second_options["publicKey"]["challenge"] == first_options["publicKey"]["challenge"]
        assert second_options["challenge_expires_at"] == first_options["challenge_expires_at"]
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        result = stack.service.approve_request(
            token,
            {"id": credential_id},
            approved=True,
        )
        assert result["schema"] == "agentnet.approval.claim-code.v1"
        claim_code = result["claim_code"]
        duplicate_approval = stack.service.approve_request(token, {}, approved=True)
        assert duplicate_approval == {
            "schema": "agentnet.approval.claim-code-status.v1",
            "request_id": created.identifier,
            "issued": True,
            "expires_at": result["expires_at"],
        }
        request_row = stack.store.fetch_one(
            "SELECT claim_code_rotations FROM approval_requests WHERE request_id=?",
            (created.identifier,),
        )
        assert request_row is not None and request_row["claim_code_rotations"] == 1
        assert claim_code not in str(
            stack.store.fetch_one(
                "SELECT claim_code_hash FROM approval_claim_codes WHERE request_id=?",
                (created.identifier,),
            )["claim_code_hash"]
        )

        retrieval_digest = "d" * 64
        first = stack.service.retrieve_core_receipt(
            request_id=created.identifier,
            claim_code=claim_code,
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            transaction_digest=created.transaction_digest or "",
            retrieval_digest=retrieval_digest,
        )
        second = stack.service.retrieve_core_receipt(
            request_id=created.identifier,
            claim_code=claim_code,
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            transaction_digest=created.transaction_digest or "",
            retrieval_digest=retrieval_digest,
        )
        assert second == first
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.retrieve_core_receipt(
                request_id=created.identifier,
                claim_code=claim_code,
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                transaction_digest=created.transaction_digest or "",
                retrieval_digest="e" * 64,
            )
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_audit "
            "WHERE request_id=? AND action='approval.receipt_retrieved'",
            (created.identifier,),
        )["n"] == 2
    finally:
        stack.store.close()


def test_corrupt_delivery_binding_isolated_from_other_owner_requests(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        _register(stack, monkeypatch)
        owner_session_hash = _owner_session(stack)
        corrupt = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Corrupt binding laptop"),
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:corrupt-binding",
            possession_hash=hashlib.sha256(b"corrupt-possession").hexdigest(),
            request_expires_at=NOW + 200,
        )
        valid = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Valid binding laptop"),
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:valid-binding",
            possession_hash=hashlib.sha256(b"valid-possession").hexdigest(),
            request_expires_at=NOW + 200,
        )
        with stack.store.transaction() as connection:
            connection.execute(
                "UPDATE approval_requests SET capability_encrypted=? WHERE request_id=?",
                (
                    stack.store.cipher.encrypt_json(
                        {"possession_hash": "invalid"},
                        purpose=f"approval-request-capability:{corrupt.identifier}",
                    ),
                    corrupt.identifier,
                ),
            )

        actionable = stack.service.actionable_requests_for_owner(
            principal_id="security-owner",
            domain_id="corp.example",
        )
        assert {item["request_id"] for item in actionable} == {
            corrupt.identifier,
            valid.identifier,
        }
        by_id = {item["request_id"]: item for item in actionable}
        assert by_id[corrupt.identifier] == {
            "request_id": corrupt.identifier,
            "approval_purpose": PURPOSE,
            "state": "unavailable",
            "automatic_delivery": False,
            "actionable": False,
            "failure_code": "delivery_binding_unavailable",
            "created_at": NOW,
            "expires_at": NOW + 200,
        }
        assert by_id[valid.identifier]["state"] == "pending"
        assert by_id[valid.identifier]["automatic_delivery"] is True
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.request_options_for_owner(
                request_id=corrupt.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
                owner_session_hash=owner_session_hash,
            )
    finally:
        stack.store.close()


def test_possession_bound_delivery_is_automatic_exact_and_retry_safe(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        possession_secret = "P" * 43
        possession_hash = hashlib.sha256(possession_secret.encode("ascii")).hexdigest()
        transaction = _approval_transaction("Automatic delivery laptop")
        created = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:automatic-delivery-1",
            possession_hash=possession_hash,
            request_expires_at=NOW + 200,
        )
        duplicate = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:automatic-delivery-1",
            possession_hash=possession_hash,
            request_expires_at=NOW + 200,
        )
        assert duplicate.identifier == created.identifier
        assert duplicate.duplicate is True
        with pytest.raises(ConflictError, match="idempotency conflict"):
            stack.service.create_request(
                principal_id="security-owner",
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                canonical_transaction=transaction,
                delivery_mode="core_claim_code",
                idempotency_key="core:enrollment:automatic-delivery-1",
                possession_hash="f" * 64,
                request_expires_at=NOW + 200,
            )

        token = _token(created.url)
        stack.service.request_options(token)
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        issued = stack.service.approve_request(token, {"id": credential_id}, approved=True)
        assert issued == {
            "schema": "agentnet.approval.possession-status.v1",
            "request_id": created.identifier,
            "delivery_status": "waiting_agent",
            "expires_at": NOW + stack.config.receipt_ttl_seconds,
        }
        assert "claim_code" not in issued
        assert stack.service.approve_request(token, {}, approved=True) == issued
        assert stack.service.actionable_requests_for_owner(
            principal_id="security-owner", domain_id="corp.example"
        ) == []
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.regenerate_claim_code(
                request_id=created.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
            )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.retrieve_core_receipt(
                request_id=created.identifier,
                possession_secret="W" * 43,
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                transaction_digest=created.transaction_digest or "",
                retrieval_digest="d" * 64,
            )
        first = stack.service.retrieve_core_receipt(
            request_id=created.identifier,
            possession_secret=possession_secret,
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            transaction_digest=created.transaction_digest or "",
            retrieval_digest="d" * 64,
        )
        assert stack.service.retrieve_core_receipt(
            request_id=created.identifier,
            possession_secret=possession_secret,
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            transaction_digest=created.transaction_digest or "",
            retrieval_digest="d" * 64,
        ) == first
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.retrieve_core_receipt(
                request_id=created.identifier,
                possession_secret=possession_secret,
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                transaction_digest=created.transaction_digest or "",
                retrieval_digest="e" * 64,
            )
        stored = stack.store.fetch_one(
            "SELECT claim_code_hash FROM approval_claim_codes WHERE request_id=?",
            (created.identifier,),
        )
        assert stored is not None and stored["claim_code_hash"] == possession_hash
        assert possession_secret not in str(stored)
    finally:
        stack.store.close()


def test_explicit_claim_code_regeneration_preserves_cumulative_failure_budget(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        created = stack.service.create_request(
            principal_id="security-owner",
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Regeneration laptop"),
            delivery_mode="core_claim_code",
            idempotency_key="core:enrollment:regeneration-1",
        )
        token = _token(created.url)
        stack.service.request_options(token)
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        first = stack.service.approve_request(
            token,
            {"id": credential_id},
            approved=True,
        )
        for suffix in range(4):
            wrong = f"0000-0000-0000-0000-0000-0000-0000-000{suffix}"
            with pytest.raises(AuthenticationError, match="denied"):
                stack.service.retrieve_core_receipt(
                    request_id=created.identifier,
                    claim_code=wrong,
                    domain_id="corp.example",
                    approval_purpose=PURPOSE,
                    transaction_digest=created.transaction_digest or "",
                    retrieval_digest="d" * 64,
                )
        rotated = stack.service.regenerate_claim_code(
            request_id=created.identifier,
            principal_id="security-owner",
            domain_id="corp.example",
        )
        assert rotated["claim_code"] != first["claim_code"]
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.retrieve_core_receipt(
                request_id=created.identifier,
                claim_code=first["claim_code"],
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                transaction_digest=created.transaction_digest or "",
                retrieval_digest="d" * 64,
            )
        request_row = stack.store.fetch_one(
            """SELECT claim_code_rotations,claim_code_failed_attempts_total
                 FROM approval_requests WHERE request_id=?""",
            (created.identifier,),
        )
        assert request_row is not None
        assert request_row["claim_code_rotations"] == 2
        assert request_row["claim_code_failed_attempts_total"] == 5
        receipt = stack.service.retrieve_core_receipt(
            request_id=created.identifier,
            claim_code=rotated["claim_code"],
            domain_id="corp.example",
            approval_purpose=PURPOSE,
            transaction_digest=created.transaction_digest or "",
            retrieval_digest="d" * 64,
        )
        assert receipt["schema"] == "agentnet.independent-approval.receipt.v1"
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.regenerate_claim_code(
                request_id=created.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
            )
    finally:
        stack.store.close()


def test_claim_code_regeneration_rejects_expired_or_terminal_current_code(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )

        def issue(schema: str):
            created = stack.service.create_request(
                principal_id="security-owner",
                domain_id="corp.example",
                approval_purpose=PURPOSE,
                canonical_transaction=_approval_transaction(schema),
                delivery_mode="core_claim_code",
                idempotency_key=f"core:enrollment:{schema}",
            )
            token = _token(created.url)
            stack.service.request_options(token)
            result = stack.service.approve_request(
                token,
                {"id": credential_id},
                approved=True,
            )
            return created, result

        expired, expired_code = issue("agentnet.expired-code.v1")
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.regenerate_claim_code(
                request_id=expired.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
                now=expired_code["expires_at"],
            )

        terminal, _terminal_code = issue("agentnet.terminal-code.v1")
        for suffix in range(5):
            with pytest.raises(AuthenticationError, match="denied"):
                stack.service.retrieve_core_receipt(
                    request_id=terminal.identifier,
                    claim_code=f"0000-0000-0000-0000-0000-0000-0000-000{suffix}",
                    domain_id="corp.example",
                    approval_purpose=PURPOSE,
                    transaction_digest=terminal.transaction_digest or "",
                    retrieval_digest="e" * 64,
                )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.regenerate_claim_code(
                request_id=terminal.identifier,
                principal_id="security-owner",
                domain_id="corp.example",
            )
    finally:
        stack.store.close()


def test_duplicate_active_request_wrong_token_reject_and_credential_revocation_fail_closed(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        transaction = _approval_transaction("Response loss laptop")
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=transaction,
        )
        with pytest.raises(ConflictError, match="active approval request"):
            stack.service.create_request(
                principal_id="security-owner",
                approval_purpose=PURPOSE,
                canonical_transaction=transaction,
            )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.request_options("agcap1." + b64url_encode(b"x" * 32))
        revoked = stack.service.revoke_credential(
            principal_id="security-owner",
            credential_id=credential_id,
            reason="lost authenticator",
        )
        assert revoked == {"revoked": True, "expired_pending_requests": 1}
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.request_options(_token(created.url))
    finally:
        stack.store.close()


def test_failed_webauthn_attempt_clears_challenge_and_commits_denial_audit(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        _register(stack, monkeypatch)
        created = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Failed WebAuthn laptop"),
        )
        token = _token(created.url)
        stack.service.request_options(token)
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: (_ for _ in ()).throw(ValueError("invalid assertion")),
        )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.approve_request(
                token,
                {"id": b64url_encode(b"credential-1")},
                approved=True,
            )

        row = stack.store.fetch_one(
            "SELECT challenge_encrypted,challenge_expires_at,failed_attempts,state "
            "FROM approval_requests WHERE request_id=?",
            (created.identifier,),
        )
        assert row is not None
        assert dict(row) == {
            "challenge_encrypted": None,
            "challenge_expires_at": None,
            "failed_attempts": 1,
            "state": "pending",
        }
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_audit "
            "WHERE request_id=? AND action='approval.denied'",
            (created.identifier,),
        )["n"] == 1
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.approve_request(token, {}, approved=True)
    finally:
        stack.store.close()


def test_request_and_issued_receipt_expiry_are_audited_once(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        pending = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Pending laptop"),
        )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.request_options(
                _token(pending.url), now=NOW + stack.config.request_ttl_seconds
            )
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.request_options(
                _token(pending.url), now=NOW + stack.config.request_ttl_seconds + 1
            )
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_audit "
            "WHERE request_id=? AND action='approval.expired' "
            "AND detail_code='request_ttl_expired'",
            (pending.identifier,),
        )["n"] == 1

        issued = stack.service.create_request(
            principal_id="security-owner",
            approval_purpose=PURPOSE,
            canonical_transaction=_approval_transaction("Issued laptop"),
            now=NOW,
        )
        token = _token(issued.url)
        stack.service.request_options(token, now=NOW)
        monkeypatch.setattr(
            "agentnet.approval.webauthn_uv.verify_authentication_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                new_sign_count=1,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        stack.service.approve_request(
            token,
            {"id": credential_id},
            approved=True,
            now=NOW,
        )
        expired_at = NOW + stack.config.receipt_ttl_seconds
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.approve_request(token, {}, approved=True, now=expired_at)
        with pytest.raises(AuthenticationError, match="denied"):
            stack.service.approve_request(token, {}, approved=True, now=expired_at + 1)
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_audit "
            "WHERE request_id=? AND action='approval.expired' "
            "AND detail_code='receipt_ttl_expired'",
            (issued.identifier,),
        )["n"] == 1
    finally:
        stack.store.close()
