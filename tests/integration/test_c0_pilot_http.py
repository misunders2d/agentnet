from __future__ import annotations

from types import SimpleNamespace

import pytest
from starlette.applications import Starlette
from starlette.testclient import TestClient

from agentnet.c0_pilot_http import create_c0_pilot_routes
from agentnet.errors import (
    AuthorizationError,
    ConflictError,
    GateBlocked,
    RetryableConflictError,
)
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


class StubCore:
    def __init__(self) -> None:
        self.c0_pilot_service = object()
        self.failure: Exception | None = None
        self.calls: list[tuple[str, object]] = []

    def _call(self, name: str, actor: object, status: str):
        if self.failure is not None:
            raise self.failure
        self.calls.append((name, actor))
        return {"schema": "agentnet.c0-pilot.result.v1", "status": status}

    def c0_pilot_start(self, *, actor):
        return self._call("start", actor, "waiting_owner")

    def c0_pilot_respond(self, *, actor):
        return self._call("respond", actor, "waiting_fresh")

    def c0_pilot_complete(self, *, actor):
        return self._call("complete", actor, "COMPLETED_C0_ROUND_TRIP")

    def c0_pilot_status(self, *, actor):
        return self._call("status", actor, "waiting_owner")


@pytest.fixture
def c0_http_stack():
    core = StubCore()

    async def body_and_actor(request, _core):
        return await request.body(), ACTOR

    app = Starlette(routes=create_c0_pilot_routes(core, body_and_actor))
    return TestClient(app, raise_server_exceptions=False), core


def test_c0_http_is_selector_free_and_non_disclosing(c0_http_stack) -> None:
    client, core = c0_http_stack
    cases = (
        ("start", 201, "waiting_owner"),
        ("respond", 200, "waiting_fresh"),
        ("complete", 200, "COMPLETED_C0_ROUND_TRIP"),
        ("status", 200, "waiting_owner"),
    )
    for operation, expected_status, stage in cases:
        response = client.post(
            f"/v1/c0-pilot/{operation}",
            content=(f'{{"schema":"agentnet.c0-pilot.{operation}.v1"}}').encode(),
            headers={"content-type": "application/json"},
        )
        assert response.status_code == expected_status
        assert response.json() == {
            "schema": "agentnet.c0-pilot.result.v1",
            "status": stage,
        }
        assert response.headers["cache-control"] == "no-store"
        assert not any(
            forbidden in response.text
            for forbidden in (
                "plan_id", "guard_id", "attempt_id", "event_id", "digest",
                "receipt", "payload", "harness_id", "entitlement_id",
            )
        )
    assert [name for name, _actor in core.calls] == [
        "start", "respond", "complete", "status"
    ]


def test_c0_http_rejects_selectors_and_maps_failures_without_details(c0_http_stack) -> None:
    client, core = c0_http_stack
    extra = client.post(
        "/v1/c0-pilot/start",
        content=b'{"peer_harness_id":"secret","schema":"agentnet.c0-pilot.start.v1"}',
        headers={"content-type": "application/json"},
    )
    assert extra.status_code == 400
    assert core.calls == []

    for failure, status, code, retryable in (
        (AuthorizationError("private identity"), 403, "c0_pilot_denied", False),
        (ConflictError("private event"), 409, "c0_pilot_conflict", False),
        (RetryableConflictError("private race"), 409, "c0_pilot_conflict", True),
        (GateBlocked("c0", "private state"), 503, "c0_pilot_unavailable", True),
    ):
        core.failure = failure
        response = client.post(
            "/v1/c0-pilot/status",
            content=b'{"schema":"agentnet.c0-pilot.status.v1"}',
            headers={"content-type": "application/json"},
        )
        assert response.status_code == status
        assert response.json() == {
            "schema": "agentnet.c0-pilot.error.v1",
            "code": code,
            "message": "request denied",
            "retryable": retryable,
        }
        assert "private" not in response.text
