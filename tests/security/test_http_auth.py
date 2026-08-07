from __future__ import annotations

import logging
from types import SimpleNamespace

import pytest
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import Response
from starlette.routing import Route
from starlette.testclient import TestClient

import agentnet.http_auth as http_auth
from agentnet.errors import AuthenticationError


PROOF_HEADERS = {
    "x-agentnet-harness": "private-harness-id",
    "x-agentnet-credential": "private-credential-id",
    "x-agentnet-key": "private-key-id",
    "x-agentnet-domain": "corp.example",
    "x-agentnet-audience": "urn:agentnet:corp.example:corporate-api",
    "x-agentnet-method": "POST",
    "x-agentnet-scheme": "https",
    "x-agentnet-authority": "api.corp.example",
    "x-agentnet-path": "/protected",
    "x-agentnet-query": "",
    "x-agentnet-body-digest": "opaque-digest",
    "x-agentnet-timestamp": "1800000000",
    "x-agentnet-nonce": "opaque-nonce-with-enough-entropy",
    "x-agentnet-signature": "opaque-signature",
}


def test_http_auth_logs_fixed_reason_without_identity_or_body(
    caplog: pytest.LogCaptureFixture,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(http_auth, "_LOGGED_AUTH_FAILURE_REASONS", set())

    class RejectingCore:
        config = SimpleNamespace(max_request_bytes=1024)

        @staticmethod
        def authenticate(*_args, **_kwargs):
            raise AuthenticationError("request proof target mismatch")

    core = RejectingCore()

    async def protected(request: Request) -> Response:
        await http_auth.authenticate_proof_request(request, core)  # type: ignore[arg-type]
        return Response(status_code=204)

    client = TestClient(Starlette(routes=[Route("/protected", protected, methods=["POST"])]))
    with caplog.at_level(logging.WARNING, logger="agentnet.http_auth"):
        for _ in range(2):
            with pytest.raises(AuthenticationError, match="request proof target mismatch"):
                client.post(
                    "/protected",
                    content=b"private-request-body",
                    headers=PROOF_HEADERS,
                )

    messages = [record.getMessage() for record in caplog.records]
    assert messages == [
        "AgentNet request authentication denied; reason=request proof target mismatch"
    ]
    rendered = "\n".join(messages)
    assert "private-request-body" not in rendered
    assert "private-harness-id" not in rendered
    assert "private-credential-id" not in rendered
    assert "private-key-id" not in rendered
