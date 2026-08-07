from __future__ import annotations

import hashlib
import json
import time
from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.http_api import create_app
from agentnet.identity.credentials import (
    CREDENTIAL_ROTATION_POP_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    CredentialRenewalRequest,
    CredentialRenewalResult,
    CredentialRenewalService,
    CredentialRotationRequest,
    CredentialRotationService,
    ManagedServerCredentialReauthorizationRequest,
    ManagedServerCredentialReauthorizationService,
    ManagedServerCredentialReauthorizationRequestV2,
)
from agentnet.operations.config import ExtensionConfig
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json


def _rotation_request(
    actor,
    new_key: P256KeyPair,
    *,
    expected_epoch: int,
    signer: P256KeyPair | None = None,
    request_id: str | None = None,
) -> CredentialRotationRequest:
    request_id = request_id or str(uuid4())
    fields = CredentialRotationRequest.possession_fields(
        request_id=request_id,
        actor=actor,
        expected_credential_epoch=expected_epoch,
        new_key_id=new_key.thumbprint,
    )
    return CredentialRotationRequest(
        request_id=request_id,
        expected_credential_epoch=expected_epoch,
        new_public_key_pem=new_key.public_pem,
        new_key_possession_signature=(signer or new_key).sign(
            CREDENTIAL_ROTATION_POP_PURPOSE,
            fields,
        ),
    )


def test_renewal_is_finite_idempotent_and_expired_credentials_fail_closed(
    store,
    identity_factory,
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    original_expiry = int(
        store.fetch_one(
            "SELECT expires_at FROM credentials WHERE credential_id=?",
            (actor.credential_id,),
        )["expires_at"]
    )
    outside = CredentialRenewalService(
        store,
        credential_ttl_seconds=86_400,
        renewal_window_seconds=300,
        clock=lambda: original_expiry - 600,
    )
    current_request = CredentialRenewalRequest(request_id=str(uuid4()))
    current = outside.renew(actor=actor, request=current_request)
    assert current.status == "current"
    assert current.expires_at == original_expiry
    assert outside.renew(actor=actor, request=current_request) == current

    inside = CredentialRenewalService(
        store,
        credential_ttl_seconds=86_400,
        renewal_window_seconds=300,
        clock=lambda: original_expiry - 100,
    )
    renewal_request = CredentialRenewalRequest(request_id=str(uuid4()))
    renewed = inside.renew(actor=actor, request=renewal_request)
    assert renewed.status == "renewed"
    assert renewed.expires_at == original_expiry - 100 + 86_400
    assert inside.renew(actor=actor, request=renewal_request) == renewed
    assert store.fetch_one(
        "SELECT COUNT(*) AS n FROM credential_renewal_requests"
    )["n"] == 2

    expired = CredentialRenewalService(
        store,
        credential_ttl_seconds=86_400,
        renewal_window_seconds=300,
        clock=lambda: renewed.expires_at,
    )
    with pytest.raises(AuthenticationError, match="validity interval"):
        expired.renew(
            actor=actor,
            request=CredentialRenewalRequest(request_id=str(uuid4())),
        )

    reauthorization_time = renewed.expires_at + 1
    signer = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset(
            {MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE}
        ),
    )
    verifier = IndependentApprovalVerifier(
        {signer.thumbprint: trusted},
        verifier_id="managed-server-recovery.example",
    )
    request_values = {
        "request_id": str(uuid4()),
        "domain_id": actor.domain_id,
        "principal_id": actor.principal_id,
        "harness_id": actor.harness_id,
        "expired_credential_id": actor.credential_id,
        "expected_credential_epoch": actor.credential_epoch,
        "expected_expired_at": renewed.expires_at,
        "expected_key_id": key.thumbprint,
        "expected_binding_assurance": actor.binding_assurance,
        "managed_config_sha256": "a" * 64,
        "managed_identity_sha256": "b" * 64,
        "maximum_new_credential_ttl_seconds": 86_400,
    }
    unsigned = ManagedServerCredentialReauthorizationRequest(
        **request_values,
        old_key_possession_signature="pending",
    )
    request = ManagedServerCredentialReauthorizationRequest(
        **request_values,
        old_key_possession_signature=key.sign(
            MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
            unsigned.possession_fields(),
        ),
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=reauthorization_time,
        expires_at=reauthorization_time + 300,
    )
    before_expiry = renewed.expires_at - 1
    before_receipt = create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=before_expiry,
        expires_at=before_expiry + 300,
    )
    with pytest.raises(AuthenticationError, match="signature verification failed"):
        ManagedServerCredentialReauthorizationService(
            store,
            verifier,
            credential_ttl_seconds=86_400,
            clock=lambda: reauthorization_time,
        ).reauthorize(
            request=request.model_copy(
                update={
                    "old_key_possession_signature": P256KeyPair.generate().sign(
                        MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                        request.possession_fields(),
                    )
                }
            ),
            approval=receipt,
        )
    with pytest.raises(AuthorizationError, match="not expired"):
        ManagedServerCredentialReauthorizationService(
            store,
            verifier,
            credential_ttl_seconds=86_400,
            clock=lambda: before_expiry,
        ).reauthorize(request=request, approval=before_receipt)
    wrong_purpose = create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose="identity.enrollment.approve",
        canonical_transaction=request.canonical_transaction,
        issued_at=reauthorization_time,
        expires_at=reauthorization_time + 300,
    )
    with pytest.raises(AuthenticationError, match="purpose or domain mismatch"):
        ManagedServerCredentialReauthorizationService(
            store,
            verifier,
            credential_ttl_seconds=86_400,
            clock=lambda: reauthorization_time,
        ).reauthorize(request=request, approval=wrong_purpose)
    other_signer = P256KeyPair.generate()
    other = TrustedApprover(
        principal_id="different-owner",
        domain_id=actor.domain_id,
        signer_key_id=other_signer.thumbprint,
        public_key_pem=other_signer.public_pem,
        allowed_purposes=trusted.allowed_purposes,
    )
    other_verifier = IndependentApprovalVerifier(
        {other_signer.thumbprint: other},
        verifier_id=verifier.verifier_id,
    )
    other_receipt = create_independent_approval_receipt(
        other_signer,
        approver=other,
        verifier_id=verifier.verifier_id,
        approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=reauthorization_time,
        expires_at=reauthorization_time + 300,
    )
    with pytest.raises(AuthorizationError, match="configured owner"):
        ManagedServerCredentialReauthorizationService(
            store,
            other_verifier,
            credential_ttl_seconds=86_400,
            clock=lambda: reauthorization_time,
        ).reauthorize(request=request, approval=other_receipt)
    recovery = ManagedServerCredentialReauthorizationService(
        store,
        verifier,
        credential_ttl_seconds=86_400,
        clock=lambda: reauthorization_time,
    )
    rebound = recovery.reauthorize(request=request, approval=receipt)
    assert rebound.credential_epoch == actor.credential_epoch + 1
    assert rebound.key_id == key.thumbprint
    assert rebound.expires_at == reauthorization_time + 86_400
    assert rebound.authority_granted is False
    assert dict(
        store.fetch_one(
            "SELECT status,expires_at FROM credentials WHERE credential_id=?",
            (actor.credential_id,),
        )
    ) == {"status": "retired", "expires_at": renewed.expires_at}
    repeated = recovery.reauthorize(request=request, approval=receipt)
    assert repeated.credential_id == rebound.credential_id
    assert repeated.idempotent_repeat is True
    drifted_unsigned = request.model_copy(
        update={"managed_config_sha256": "c" * 64, "old_key_possession_signature": "pending"}
    )
    drifted = drifted_unsigned.model_copy(
        update={
            "old_key_possession_signature": key.sign(
                MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                drifted_unsigned.possession_fields(),
            )
        }
    )
    drifted_receipt = create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=drifted.canonical_transaction,
        issued_at=reauthorization_time,
        expires_at=reauthorization_time + 300,
    )
    with pytest.raises(ConflictError, match="expired binding changed"):
        recovery.reauthorize(request=drifted, approval=drifted_receipt)


def test_managed_server_reauthorization_v1_vector_remains_frozen() -> None:
    request = ManagedServerCredentialReauthorizationRequest(
        request_id="00000000-0000-4000-8000-000000000001",
        domain_id="corp.example",
        principal_id="principal-1",
        harness_id="harness-1",
        expired_credential_id="credential-1",
        expected_credential_epoch=1,
        expected_expired_at=100,
        expected_key_id="a" * 64,
        expected_binding_assurance="os_bound",
        managed_config_sha256="b" * 64,
        managed_identity_sha256="c" * 64,
        maximum_new_credential_ttl_seconds=86_400,
        old_key_possession_signature="synthetic-signature",
    )

    assert hashlib.sha256(request.canonical_transaction).hexdigest() == (
        "400778d5125c2dea012bae857d8dd4781fcff8fee6dc85197c54efc6938c2005"
    )


def test_managed_server_reauthorization_v2_binds_c0_provenance() -> None:
    values = {
        "request_id": "00000000-0000-4000-8000-000000000001",
        "domain_id": "corp.example",
        "principal_id": "principal-1",
        "harness_id": "harness-1",
        "expired_credential_id": "credential-1",
        "expected_credential_epoch": 1,
        "expected_expired_at": 100,
        "expected_key_id": "a" * 64,
        "expected_binding_assurance": "os_bound",
        "managed_config_sha256": "b" * 64,
        "c0_terminal_credential_epoch": 1,
        "managed_identity_sha256": "c" * 64,
        "maximum_new_credential_ttl_seconds": 86_400,
        "old_key_possession_signature": "synthetic-signature",
        "c0_terminal_sha256": "d" * 64,
        "prior_supersession_journal_sha256": None,
    }
    request = ManagedServerCredentialReauthorizationRequestV2(**values)
    changed = request.model_copy(update={"c0_terminal_sha256": "e" * 64})

    assert request.schema_version == "agentnet.managed-server-credential-reauthorization.v2"
    assert request.transaction_fields()["c0_terminal_sha256"] == "d" * 64
    assert hashlib.sha256(request.canonical_transaction).digest() != hashlib.sha256(
        changed.canonical_transaction
    ).digest()
    assert request.possession_fields()["transaction_sha256"] != changed.possession_fields()[
        "transaction_sha256"
    ]



def test_v2_reauthorization_returns_authoritative_audit_hash(
    store,
    identity_factory,
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    now = int(time.time())
    expired_at = now - 1
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id=?",
            (expired_at, actor.credential_id),
        )
    signer = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset(
            {MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE}
        ),
    )
    verifier = IndependentApprovalVerifier(
        {signer.thumbprint: trusted},
        verifier_id="managed-server-recovery.example",
    )
    terminal_raw = (
        json.dumps(
            {
                "schema": "agentnet.c0-pilot-responder.terminal.v1",
                "status": "COMPLETED_C0_ROUND_TRIP",
                "domain_id": actor.domain_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
            },
            sort_keys=True,
        ).encode()
        + b"\n"
    )
    values = {
        "request_id": str(uuid4()),
        "domain_id": actor.domain_id,
        "principal_id": actor.principal_id,
        "harness_id": actor.harness_id,
        "expired_credential_id": actor.credential_id,
        "expected_credential_epoch": actor.credential_epoch,
        "expected_expired_at": expired_at,
        "expected_key_id": key.thumbprint,
        "expected_binding_assurance": actor.binding_assurance,
        "managed_config_sha256": "a" * 64,
        "managed_identity_sha256": "b" * 64,
        "maximum_new_credential_ttl_seconds": 86_400,
        "c0_terminal_credential_epoch": actor.credential_epoch,
        "c0_terminal_sha256": hashlib.sha256(terminal_raw).hexdigest(),
        "prior_supersession_journal_sha256": None,
    }
    unsigned = ManagedServerCredentialReauthorizationRequestV2(
        **values,
        old_key_possession_signature="pending",
    )
    request = unsigned.model_copy(
        update={
            "old_key_possession_signature": key.sign(
                MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                unsigned.possession_fields(),
            )
        }
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=now,
        expires_at=now + 300,
    )
    service = ManagedServerCredentialReauthorizationService(
        store,
        verifier,
        credential_ttl_seconds=86_400,
        clock=lambda: now,
    )
    with pytest.raises(GateBlocked, match="terminal provenance"):
        service.reauthorize(
            request=request.model_copy(update={"c0_terminal_sha256": "d" * 64}),
            approval=receipt,
            c0_terminal_raw=terminal_raw,
        )
    assert store.fetch_one(
        "SELECT status FROM credentials WHERE credential_id=?",
        (actor.credential_id,),
    )["status"] == "active"


    result = service.reauthorize(
        request=request,
        approval=receipt,
        c0_terminal_raw=terminal_raw,
    )

    assert result.schema_version == "agentnet.managed-server-credential-reauthorization-result.v2"
    assert len(result.audit_record_hash) == 64
    row = store.fetch_one(
        "SELECT record_json FROM audit_log WHERE record_hash=?",
        (result.audit_record_hash,),
    )
    assert row is not None
    record = json.loads(row["record_json"])
    assert record == {
        "action": "credential.managed_server_reauthorized",
        "request_id": request.request_id,
        "domain_id": request.domain_id,
        "principal_id": request.principal_id,
        "harness_id": request.harness_id,
        "old_credential_id": request.expired_credential_id,
        "new_credential_id": result.credential_id,
        "key_id": request.expected_key_id,
        "new_credential_epoch": result.credential_epoch,
        "previous_credential_epoch": request.expected_credential_epoch,
        "terminal_credential_epoch": request.c0_terminal_credential_epoch,
        "not_before": result.not_before,
        "expires_at": result.expires_at,
        "approval_receipt_id": receipt["receipt_id"],
        "approval_receipt_digest": hashlib.sha256(canonical_json(receipt)).hexdigest(),
        "transaction_digest": hashlib.sha256(request.canonical_transaction).hexdigest(),
        "c0_terminal_sha256": request.c0_terminal_sha256,
        "c0_supersession_sha256": request.prior_supersession_journal_sha256,
    }
    repeated = service.reauthorize(
        request=request,
        approval=receipt,
        c0_terminal_raw=terminal_raw,
    )
    assert repeated.idempotent_repeat is True
    assert repeated.audit_record_hash == result.audit_record_hash

def test_rotation_atomically_retires_current_key_and_fences_replay_without_touching_sibling(
    store,
    identity_factory,
) -> None:
    actor, _old_key = identity_factory(binding_assurance="os_bound")
    sibling, _sibling_key = identity_factory(binding_assurance="os_bound")
    now = int(time.time())
    service = CredentialRotationService(store, credential_ttl_seconds=600, clock=lambda: now)
    new_key = P256KeyPair.generate()
    request = _rotation_request(actor, new_key, expected_epoch=1)

    result = service.rotate(actor=actor, request=request)

    assert result.harness_id == actor.harness_id
    assert result.key_id == new_key.thumbprint
    assert result.credential_epoch == 2
    assert result.expires_at == now + 600
    assert dict(
        store.fetch_one(
            "SELECT status,epoch FROM credentials WHERE credential_id=?",
            (actor.credential_id,),
        )
    ) == {"status": "retired", "epoch": 1}
    assert dict(
        store.fetch_one(
            "SELECT status,epoch,key_id,expires_at FROM credentials WHERE credential_id=?",
            (result.credential_id,),
        )
    ) == {
        "status": "active",
        "epoch": 2,
        "key_id": new_key.thumbprint,
        "expires_at": now + 600,
    }
    assert store.fetch_one(
        "SELECT credential_epoch FROM harnesses WHERE harness_id=?",
        (actor.harness_id,),
    )["credential_epoch"] == 2
    assert dict(
        store.fetch_one(
            """SELECT h.credential_epoch,c.status,c.epoch
                 FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                WHERE h.harness_id=? AND c.credential_id=?""",
            (sibling.harness_id, sibling.credential_id),
        )
    ) == {"credential_epoch": 1, "status": "active", "epoch": 1}

    with pytest.raises(AuthenticationError, match="credential is unavailable"):
        service.rotate(actor=actor, request=request)


def test_rotation_rejects_stale_epoch_and_new_key_substitution(store, identity_factory) -> None:
    actor, _old_key = identity_factory(binding_assurance="os_bound")
    service = CredentialRotationService(store)
    candidate = P256KeyPair.generate()

    stale = _rotation_request(actor, candidate, expected_epoch=2)
    with pytest.raises(ConflictError, match="fencing epoch"):
        service.rotate(actor=actor, request=stale)

    substituted = _rotation_request(
        actor,
        candidate,
        expected_epoch=1,
        signer=P256KeyPair.generate(),
    )
    with pytest.raises(AuthenticationError, match="signature verification failed"):
        service.rotate(actor=actor, request=substituted)

    assert store.fetch_one(
        "SELECT credential_epoch FROM harnesses WHERE harness_id=?",
        (actor.harness_id,),
    )["credential_epoch"] == 1
    assert store.fetch_one(
        "SELECT status FROM credentials WHERE credential_id=?",
        (actor.credential_id,),
    )["status"] == "active"


@pytest.mark.anyio
async def test_authenticated_rotation_http_has_no_target_identity_claims(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, old_key = identity_factory(binding_assurance="os_bound")
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=actor.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    app = create_app(core)
    path = "/v1/credentials/current/rotate"
    new_key = P256KeyPair.generate()
    request = _rotation_request(actor, new_key, expected_epoch=1)
    body = canonical_json(request.model_dump(mode="json"))
    proof = create_request_proof(
        old_key,
        harness_id=actor.harness_id,
        credential_id=actor.credential_id,
        domain_id=actor.domain_id,
        audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
        method="POST",
        scheme="http",
        authority="127.0.0.1",
        path=path,
        query="",
        body=body,
    )

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app),
        base_url="http://127.0.0.1",
    ) as client:
        response = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **proof_headers(proof)},
        )

    assert response.status_code == 201
    rendered = response.json()["credential"]
    assert rendered["harness_id"] == actor.harness_id
    assert rendered["credential_epoch"] == 2
    assert rendered["key_id"] == new_key.thumbprint
    assert "principal_id" not in rendered
    assert "domain_id" not in rendered


@pytest.mark.anyio
async def test_authenticated_renewal_http_is_selector_free_and_actor_bound(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=actor.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    observed: dict[str, object] = {}

    def renew_current_credential(*, actor, request):
        observed.update(actor=actor, request=request)
        return CredentialRenewalResult(status="renewed", expires_at=123456)

    monkeypatch.setattr(core, "renew_current_credential", renew_current_credential)
    app = create_app(core)
    path = "/v1/credentials/current/renew"
    request_id = str(uuid4())
    body = canonical_json(
        {"schema": "agentnet.credential-renewal.v1", "request_id": request_id}
    )
    proof = create_request_proof(
        key,
        harness_id=actor.harness_id,
        credential_id=actor.credential_id,
        domain_id=actor.domain_id,
        audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
        method="POST",
        scheme="http",
        authority="127.0.0.1",
        path=path,
        query="",
        body=body,
    )
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app),
        base_url="http://127.0.0.1",
    ) as client:
        response = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **proof_headers(proof)},
        )

    assert response.status_code == 200
    assert response.json() == {
        "schema": "agentnet.credential-renewal-result.v1",
        "status": "renewed",
        "expires_at": 123456,
    }
    assert observed == {
        "actor": actor,
        "request": CredentialRenewalRequest(request_id=request_id),
    }
