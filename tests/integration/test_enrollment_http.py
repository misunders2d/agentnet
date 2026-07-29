from __future__ import annotations

import base64
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace

import httpx
import pytest
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse

from agentnet.core.app import CommunicationCore
from agentnet.enrollment_http import create_enrollment_routes
from agentnet.errors import AuthenticationError
from agentnet.http_api import create_app
from agentnet.identity.enrollment import EnrollmentChallenge, EnrollmentResult
from agentnet.identity.oidc import (
    OIDCGuidedAuthorizationRequest,
    OIDCPollResult,
    RemoteActivationIdentityMismatch,
)
from agentnet.operations.config import ExtensionConfig
from agentnet.security.signatures import P256KeyPair, canonical_json


class FakeEnrollment:
    def __init__(self, result: EnrollmentResult) -> None:
        self.result = result
        self.completed: dict | None = None

    def complete(self, **kwargs):
        self.completed = kwargs
        return self.result


class FakeRecoveryCoordinator:
    def __init__(self, state: str) -> None:
        self.state = state
        self.authorization_failures: list[str] = []

    def has_state(self, state: str) -> bool:
        return state == self.state

    def fail_authorization(self, *, state: str) -> None:
        assert state == self.state
        self.authorization_failures.append(state)


class FakeCoordinator:
    def __init__(self, store, result: EnrollmentResult) -> None:
        self.store = store
        self.enrollment = FakeEnrollment(result)
        self.begun: dict | None = None
        self.guided_completed: dict | None = None
        self.authorization_completions: list[tuple[str, str]] = []
        self.authorization_failures: list[str] = []
        self.remote_activation = False
        self.wrong_account = False
        self.approval_client = SimpleNamespace(
            config=SimpleNamespace(
                origin="https://approval-internal.corp.example",
                public_origin="https://approval.corp.example",
            )
        )

    def begin_authorization(self, **kwargs):
        self.begun = kwargs
        return OIDCGuidedAuthorizationRequest(
            transaction_id="oidc-transaction-http-0001",
            authorization_url="https://idp.example/authorize?opaque=1",
            state="s" * 43,
            expires_at=2_000_000_000,
            continuation_token="c" * 43,
        )

    def remote_activation_authorization_url(self) -> str:
        return "https://idp.example/authorize?opaque=1"

    def remote_activation_for_challenge(self, challenge_id: str) -> bool:
        assert challenge_id == "enrollment-challenge-http-0001"
        return self.remote_activation

    def poll_continuation(self, *, transaction_id: str, continuation_token: str):
        assert transaction_id == "oidc-transaction-http-0001"
        assert continuation_token == "c" * 43
        return OIDCPollResult(
            status="approval_ready",
            interval_seconds=2,
            expires_at=2_000_000_000,
            challenge_id="enrollment-challenge-http-0001",
            nonce="n" * 43,
            canonical_transaction_b64=base64.b64encode(
                canonical_json({"schema": "agentnet.enrollment.challenge.v1", "exact": True})
            ).decode("ascii"),
            approval_url="https://approval.corp.example/approval",
        )

    def complete_guided_enrollment(self, **kwargs):
        self.guided_completed = kwargs
        return self.enrollment.result

    def fail_authorization(self, *, state: str) -> None:
        assert state == "s" * 43
        self.authorization_failures.append(state)

    def complete_authorization(self, *, state: str, code: str):
        assert state == "s" * 43
        assert code == "authorization-code-http"
        self.authorization_completions.append((state, code))
        if self.wrong_account:
            raise RemoteActivationIdentityMismatch(
                "verified OIDC account is not approved for this server activation"
            )
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
        activation_page = await client.get("/activate")
        assert activation_page.status_code == 200
        assert "Activate AgentNet server" in activation_page.text
        assert "opaque=1" not in activation_page.text
        activation_start = await client.get(
            "/v1/enrollment/oidc/activate", follow_redirects=False
        )
        assert activation_start.status_code == 303
        assert activation_start.headers["location"] == "https://idp.example/authorize?opaque=1"

        for _ in range(53):
            assert (await client.get("/activate")).status_code == 200
        page_limited = await client.get("/activate")
        assert page_limited.status_code == 503
        assert "opaque=1" not in page_limited.text

        for _ in range(17):
            response = await client.get(
                "/v1/enrollment/oidc/activate", follow_redirects=False
            )
            assert response.status_code == 303
        start_limited = await client.get(
            "/v1/enrollment/oidc/activate", follow_redirects=False
        )
        assert start_limited.status_code == 503
        assert "opaque=1" not in start_limited.text

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
            "remote_activation": False,
        }
        assert "identity" not in begin.text and "email" not in begin.text

        malformed_queries = (
            [
                ("state", "s" * 43),
                ("state", "s" * 43),
                ("code", "authorization-code-http"),
            ],
            [
                ("state", "s" * 43),
                ("code", "authorization-code-http"),
                ("scope", "openid"),
                ("scope", "email"),
            ],
            [
                ("state", "s" * 43),
                ("code", "authorization-code-http"),
                ("error", "access_denied"),
            ],
            [
                ("state", "s" * 43),
                ("code", "authorization-code-http"),
                ("error_description", "denied"),
            ],
        )
        for query in malformed_queries:
            denied = await client.get(
                "/v1/enrollment/oidc/callback",
                params=query,
            )
            assert denied.status_code == 401
        assert coordinator.authorization_completions == []
        assert coordinator.authorization_failures == []

        provider_error = await client.get(
            "/v1/enrollment/oidc/callback",
            params={
                "state": "s" * 43,
                "error": "access_denied_sensitive",
                "error_description": "owner-canceled-sensitive",
                "error_uri": "https://idp.example/errors/private-sensitive",
                "authuser": "0",
            },
        )
        assert provider_error.status_code == 401
        assert "access_denied_sensitive" not in provider_error.text
        assert "owner-canceled-sensitive" not in provider_error.text
        assert "private-sensitive" not in provider_error.text
        assert coordinator.authorization_failures == ["s" * 43]
        assert coordinator.authorization_completions == []

        callback = await client.get(
            "/v1/enrollment/oidc/callback",
            params={
                "state": "s" * 43,
                "code": "authorization-code-http",
                "scope": "openid email",
                "authuser": "0",
                "prompt": "consent",
            },
            headers={"Accept": "application/json"},
        )
        assert callback.status_code == 200, callback.text
        challenge = callback.json()

        browser_callback = await client.get(
            "/v1/enrollment/oidc/callback",
            params={"state": "s" * 43, "code": "authorization-code-http"},
            headers={"Accept": "text/html"},
        )
        assert "Return to the AgentNet onboarding command" in browser_callback.text
        assert "challenge_id" not in browser_callback.text

        coordinator.remote_activation = True
        coordinator.wrong_account = True
        wrong_account = await client.get(
            "/v1/enrollment/oidc/callback",
            params={"state": "s" * 43, "code": "authorization-code-http"},
            headers={"Accept": "text/html"},
            follow_redirects=False,
        )
        assert wrong_account.status_code == 403
        assert wrong_account.headers["cache-control"] == "no-store"
        assert "Approved company account required" in wrong_account.text
        assert "security-owner" not in wrong_account.text
        assert "person@corp.example" not in wrong_account.text
        assert "authorization-code-http" not in wrong_account.text
        assert "s" * 43 not in wrong_account.text
        coordinator.wrong_account = False

        remote_callback = await client.get(
            "/v1/enrollment/oidc/callback",
            params={"state": "s" * 43, "code": "authorization-code-http"},
            headers={"Accept": "text/html"},
            follow_redirects=False,
        )
        assert remote_callback.status_code == 303
        assert remote_callback.headers["location"] == "https://approval.corp.example/approval"
        assert "authorization-code-http" not in remote_callback.text
        coordinator.remote_activation = False

        poll = await client.post(
            "/v1/enrollment/oidc/poll",
            content=canonical_json(
                {
                    "transaction_id": "oidc-transaction-http-0001",
                    "continuation_token": "c" * 43,
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert poll.status_code == 200
        assert poll.json()["status"] == "approval_ready"
        assert "independent_approval" not in poll.text

        guided_completed = await client.post(
            "/v1/enrollment/oidc/complete",
            content=canonical_json(
                {
                    "transaction_id": "oidc-transaction-http-0001",
                    "continuation_token": "c" * 43,
                    "possession_signature": candidate.sign(
                        "agentnet.enrollment.pop.v1",
                        {"schema": "agentnet.enrollment.challenge.v1", "exact": True},
                    ),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert guided_completed.status_code == 201, guided_completed.text
        assert guided_completed.json()["actor"]["harness_id"] == actor.harness_id
        assert "approval" not in guided_completed.text
        assert coordinator.guided_completed is not None
        assert "claim_code" not in coordinator.guided_completed
        assert coordinator.guided_completed["continuation_token"] == "c" * 43

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


@pytest.mark.anyio
async def test_provider_error_routes_exact_recovery_state_without_metadata_leak(
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
    recovery_state = "r" * 43
    recovery = FakeRecoveryCoordinator(recovery_state)

    async def denied(_request: Request, exc: Exception):
        assert isinstance(exc, AuthenticationError)
        return JSONResponse(
            {"code": "request_denied", "message": "request denied"},
            status_code=401,
        )

    app = Starlette(
        routes=create_enrollment_routes(
            core,
            coordinator,
            recovery_coordinator=recovery,  # type: ignore[arg-type]
        ),
        exception_handlers={Exception: denied},
    )
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        response = await client.get(
            "/v1/enrollment/oidc/callback",
            params={
                "state": recovery_state,
                "error": "access_denied_sensitive",
                "error_description": "recovery-owner-canceled-sensitive",
                "error_uri": "https://idp.example/errors/recovery-private-sensitive",
                "extension": "ignored-sensitive-extension",
            },
        )

    assert response.status_code == 401
    assert recovery.authorization_failures == [recovery_state]
    assert coordinator.authorization_failures == []
    assert coordinator.authorization_completions == []
    for forbidden in (
        "access_denied_sensitive",
        "recovery-owner-canceled-sensitive",
        "recovery-private-sensitive",
        "ignored-sensitive-extension",
    ):
        assert forbidden not in response.text
