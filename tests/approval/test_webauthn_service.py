from __future__ import annotations

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
from agentnet.errors import AuthenticationError, ConflictError, GateBlocked
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, b64url_encode, canonical_json


NOW = 1_800_000_000
PURPOSE = "identity.enrollment.approve"


def _token(url: str) -> str:
    return parse_qs(urlsplit(url).fragment)["token"][0]


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


def test_registration_approval_receipt_and_response_loss_are_exact_and_single(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        transaction = canonical_json(
            {
                "schema": "agentnet.test-approval-transaction.v1",
                "domain_id": "corp.example",
                "beneficiary": "harness-1",
            }
        )
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


def test_duplicate_active_request_wrong_token_reject_and_credential_revocation_fail_closed(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _stack(tmp_path)
    try:
        credential_id = _register(stack, monkeypatch)
        transaction = canonical_json({"schema": "agentnet.test.v1", "domain_id": "corp.example"})
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
            canonical_transaction=canonical_json(
                {"schema": "agentnet.test.v1", "domain_id": "corp.example"}
            ),
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
            canonical_transaction=canonical_json({"schema": "agentnet.pending.v1"}),
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
            canonical_transaction=canonical_json({"schema": "agentnet.issued.v1"}),
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
