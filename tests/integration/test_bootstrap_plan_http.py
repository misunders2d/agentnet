from __future__ import annotations

from types import SimpleNamespace

import pytest
from starlette.applications import Starlette
from starlette.testclient import TestClient

from agentnet.authorization.bootstrap_plan_service import BootstrapPlanTerminalError
from agentnet.bootstrap_plan_http import create_bootstrap_plan_routes
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor


ACTOR = VerifiedActor(
    kind=ActorKind.VERIFIED_HUMAN_HARNESS,
    domain_id="corp.example",
    principal_id="owner",
    harness_id="fresh-harness",
    credential_id="fresh-credential",
    credential_epoch=1,
    binding_assurance="os_bound",
)


class StubService:
    def __init__(self):
        self.store = object()
        self.failure: Exception | None = None
        self.calls: list[tuple[str, object, object]] = []

    def _call(self, name, actor, request, result):
        if self.failure is not None:
            raise self.failure
        self.calls.append((name, actor, request))
        return result

    def begin(self, *, actor, request):
        return self._call(
            "begin",
            actor,
            request,
            {
                "schema": "agentnet.bootstrap-plan.begin-result.v1",
                "status": "approval_pending",
                "approval_url": "https://approval.example/approval",
                "expires_at": 1_800_000_300,
            },
        )

    def status(self, *, actor, request):
        return self._call(
            "status",
            actor,
            request,
            {
                "schema": "agentnet.bootstrap-plan.status-result.v1",
                "status": "approval_ready",
                "approval_url": "https://approval.example/approval",
                "expires_at": 1_800_000_300,
                "next_action": "complete_automatically",
            },
        )

    def complete(self, *, actor, request):
        return self._call(
            "complete",
            actor,
            request,
            {
                "schema": "agentnet.bootstrap-plan.complete-result.v1",
                "status": "prepared_unusable",
                "authority_granted": False,
                "communication_usable": False,
            },
        )


@pytest.fixture
def http_stack():
    service = StubService()
    core = SimpleNamespace(store=service.store)

    async def body_and_actor(request, _core):
        return await request.body(), ACTOR

    app = Starlette(routes=create_bootstrap_plan_routes(core, body_and_actor, service=service))
    return TestClient(app, raise_server_exceptions=False), service


def test_bootstrap_plan_http_success_bodies_are_exact_and_non_disclosing(http_stack) -> None:
    client, service = http_stack
    begin = client.post(
        "/v1/bootstrap-plan/begin",
        content=b'{"begin_idempotency_key":"bootstrap-begin-key-0001","schema":"agentnet.bootstrap-plan.begin.v1"}',
        headers={"content-type": "application/json"},
    )
    status = client.post(
        "/v1/bootstrap-plan/status",
        content=b'{"begin_idempotency_key":"bootstrap-begin-key-0001","schema":"agentnet.bootstrap-plan.status.v1"}',
        headers={"content-type": "application/json"},
    )
    complete = client.post(
        "/v1/bootstrap-plan/complete",
        content=b'{"begin_idempotency_key":"bootstrap-begin-key-0001","completion_idempotency_key":"bootstrap-complete-key-0001","schema":"agentnet.bootstrap-plan.complete.v2"}',
        headers={"content-type": "application/json"},
    )

    assert begin.status_code == 201
    assert begin.json() == {
        "schema": "agentnet.bootstrap-plan.begin-result.v1",
        "status": "approval_pending",
        "approval_url": "https://approval.example/approval",
        "expires_at": 1_800_000_300,
    }
    assert status.status_code == 200
    assert status.json()["status"] == "approval_ready"
    assert complete.status_code == 201
    assert complete.json() == {
        "schema": "agentnet.bootstrap-plan.complete-result.v1",
        "status": "prepared_unusable",
        "authority_granted": False,
        "communication_usable": False,
    }
    assert [call[0] for call in service.calls] == ["begin", "status", "complete"]
    assert all(call[1] == ACTOR for call in service.calls)
    assert begin.headers["cache-control"] == "no-store"


@pytest.mark.parametrize(
    ("failure", "status", "code", "retryable"),
    [
        (AuthenticationError("secret detail"), 401, "authentication_denied", False),
        (AuthorizationError("candidate detail"), 403, "bootstrap_plan_denied", False),
        (ConflictError("identifier detail"), 409, "bootstrap_plan_conflict", False),
        (BootstrapPlanTerminalError("terminal detail"), 410, "bootstrap_plan_terminal", False),
        (GateBlocked("approval", "private endpoint detail"), 503, "bootstrap_plan_unavailable", True),
        (RuntimeError("sensitive unexpected detail"), 503, "bootstrap_plan_unavailable", True),
        (ValueError("unexpected value detail"), 503, "bootstrap_plan_unavailable", True),
    ],
)
def test_bootstrap_plan_http_maps_failures_to_exact_non_leaking_envelope(
    http_stack, failure, status, code, retryable
) -> None:
    client, service = http_stack
    service.failure = failure
    response = client.post(
        "/v1/bootstrap-plan/begin",
        content=b'{"begin_idempotency_key":"bootstrap-begin-key-0001","schema":"agentnet.bootstrap-plan.begin.v1"}',
        headers={"content-type": "application/json"},
    )
    assert response.status_code == status
    assert response.json() == {
        "schema": "agentnet.bootstrap-plan.error.v1",
        "code": code,
        "message": "request denied",
        "retryable": retryable,
    }
    assert "detail" not in response.text


def test_bootstrap_plan_http_rejects_noncanonical_or_extra_body_before_service(http_stack) -> None:
    client, service = http_stack
    bodies = (
        b'{"schema":"agentnet.bootstrap-plan.begin.v1","begin_idempotency_key":"bootstrap-begin-key-0001","extra":true}',
        b'{"begin_idempotency_key":"bootstrap-begin-key-0001","schema":"agentnet.bootstrap-plan.begin.v1"} ',
        b'{"schema":"agentnet.bootstrap-plan.begin.v1","begin_idempotency_key":"bootstrap-begin-key-0001","schema":"agentnet.bootstrap-plan.begin.v1"}',
    )
    for body in bodies:
        response = client.post(
            "/v1/bootstrap-plan/begin",
            content=body,
            headers={"content-type": "application/json"},
        )
        assert response.status_code == 400
        assert response.json() == {
            "schema": "agentnet.bootstrap-plan.error.v1",
            "code": "invalid_request",
            "message": "request denied",
            "retryable": False,
        }
    assert service.calls == []
