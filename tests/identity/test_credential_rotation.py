from __future__ import annotations

import time
from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.http_api import create_app
from agentnet.identity.credentials import (
    CREDENTIAL_ROTATION_POP_PURPOSE,
    CredentialRenewalRequest,
    CredentialRenewalResult,
    CredentialRenewalService,
    CredentialRotationRequest,
    CredentialRotationService,
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
    actor, _key = identity_factory(binding_assurance="os_bound")
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

