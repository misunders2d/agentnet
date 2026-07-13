from __future__ import annotations

import base64
from dataclasses import dataclass
from pathlib import Path

import httpx
import pytest

from agentnet.core.app import CommunicationCore
from agentnet.http_api import create_app
from agentnet.identity.enrollment import EnrollmentChallenge, EnrollmentResult
from agentnet.identity.oidc import OIDCAuthorizationRequest
from agentnet.operations.config import ExtensionConfig
from agentnet.security.signatures import P256KeyPair, canonical_json


class FakeEnrollment:
    def __init__(self, result: EnrollmentResult) -> None:
        self.result = result
        self.completed: dict | None = None

    def complete(self, **kwargs):
        self.completed = kwargs
        return self.result


class FakeCoordinator:
    def __init__(self, store, result: EnrollmentResult) -> None:
        self.store = store
        self.enrollment = FakeEnrollment(result)
        self.begun: dict | None = None

    def begin_authorization(self, **kwargs):
        self.begun = kwargs
        return OIDCAuthorizationRequest(
            transaction_id="oidc-transaction-http-0001",
            authorization_url="https://idp.example/authorize?opaque=1",
            state="s" * 43,
            expires_at=2_000_000_000,
        )

    def complete_authorization(self, *, state: str, code: str):
        assert state == "s" * 43
        assert code == "authorization-code-http"
        return EnrollmentChallenge(
            challenge_id="enrollment-challenge-http-0001",
            nonce="n" * 43,
            expires_at=2_000_000_000,
            canonical_transaction=canonical_json(
                {"schema": "agentnet.enrollment.challenge.v1", "exact": True}
            ),
        )


@pytest.mark.anyio
async def test_public_oidc_enrollment_routes_compose_exact_ceremony_without_claim_injection(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, _actor_key = identity_factory(binding_assurance="os_bound")
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
    result = EnrollmentResult(
        principal_id=actor.principal_id or "",
        harness_id=actor.harness_id or "",
        credential_id=actor.credential_id or "",
        key_id="candidate-key-http",
        credential_epoch=1,
        harness_status="active",
        actor=actor,
    )
    coordinator = FakeCoordinator(store, result)
    core.oidc_enrollment = coordinator  # injectable provider seam; OIDC verifier has its own attack suite.
    candidate = P256KeyPair.generate()

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        begin = await client.post(
            "/v1/enrollment/oidc/begin",
            content=canonical_json(
                {
                    "harness_kind": "codex",
                    "harness_name": "ordinary laptop agent",
                    "public_key_pem": candidate.public_pem,
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert begin.status_code == 201, begin.text
        assert begin.headers["cache-control"] == "no-store"
        assert coordinator.begun == {
            "domain_id": actor.domain_id,
            "harness_kind": "codex",
            "harness_name": "ordinary laptop agent",
            "public_key_pem": candidate.public_pem,
        }
        assert "identity" not in begin.text and "email" not in begin.text

        duplicate_state = await client.get(
            "/v1/enrollment/oidc/callback",
            params=[
                ("state", "s" * 43),
                ("state", "s" * 43),
                ("code", "authorization-code-http"),
            ],
        )
        assert duplicate_state.status_code == 401

        callback = await client.get(
            "/v1/enrollment/oidc/callback",
            params={"state": "s" * 43, "code": "authorization-code-http"},
        )
        assert callback.status_code == 200, callback.text
        challenge = callback.json()
        transaction = base64.b64decode(challenge["canonical_transaction_b64"])

        completed = await client.post(
            "/v1/enrollment/complete",
            content=canonical_json(
                {
                    "challenge_id": challenge["challenge_id"],
                    "nonce": challenge["nonce"],
                    "canonical_transaction_b64": challenge["canonical_transaction_b64"],
                    "possession_signature": candidate.sign(
                        "agentnet.enrollment.pop.v1",
                        {"schema": "agentnet.enrollment.challenge.v1", "exact": True},
                    ),
                    "independent_approval": {"opaque": "verified-by-production-service"},
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert completed.status_code == 201, completed.text
        assert completed.json()["actor"]["harness_id"] == actor.harness_id
        assert coordinator.enrollment.completed is not None
        assert coordinator.enrollment.completed["canonical_transaction"] == transaction

        injected = await client.post(
            "/v1/enrollment/oidc/begin",
            content=canonical_json(
                {
                    "harness_kind": "codex",
                    "harness_name": "agent",
                    "public_key_pem": candidate.public_pem,
                    "verified_email": "attacker@example.test",
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert injected.status_code == 422
