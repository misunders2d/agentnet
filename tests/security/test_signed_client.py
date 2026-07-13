from __future__ import annotations

import httpx
import pytest

from agentnet.client import AgentNetClient
from agentnet.errors import ValidationError
from agentnet.security.dpop import proof_from_headers
from agentnet.security.signatures import P256KeyPair


def test_client_binds_configured_audience_and_canonical_full_target() -> None:
    captured: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["proof"] = proof_from_headers(dict(request.headers))
        return httpx.Response(200, json={"ok": True})

    client = AgentNetClient(
        base_url="https://api.corp.example",
        key=P256KeyPair.generate(),
        domain_id="corp.example",
        harness_id="harness-1",
        credential_id="credential-1",
        audience="urn:agentnet:corp.example:corporate-api",
        transport=httpx.MockTransport(handler),
    )
    try:
        response = client.request("GET", "/v1/mailbox?after=10&limit=5")
    finally:
        client.close()
    assert response.status_code == 200
    proof = captured["proof"]
    assert proof.audience == "urn:agentnet:corp.example:corporate-api"
    assert (proof.scheme, proof.authority, proof.path, proof.query) == (
        "https",
        "api.corp.example",
        "/v1/mailbox",
        "after=10&limit=5",
    )
    assert captured["url"] == "https://api.corp.example/v1/mailbox?after=10&limit=5"


@pytest.mark.parametrize(
    "base_url",
    [
        "HTTPS://api.corp.example",
        "https://API.corp.example",
        "https://api.corp.example:443",
        "https://user@api.corp.example",
        "https://api.corp.example/base",
    ],
)
def test_client_rejects_noncanonical_or_credentialed_origin(base_url: str) -> None:
    with pytest.raises(ValidationError):
        AgentNetClient(
            base_url=base_url,
            key=P256KeyPair.generate(),
            domain_id="corp.example",
            harness_id="harness-1",
            credential_id="credential-1",
            audience="urn:agentnet:corp.example:corporate-api",
        )
