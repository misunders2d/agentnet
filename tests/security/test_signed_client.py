from __future__ import annotations

import json

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


def test_client_signs_exact_mailbox_acknowledgement_path_and_body() -> None:
    captured: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["proof"] = proof_from_headers(dict(request.headers))
        captured["body"] = request.content
        return httpx.Response(200, json={"fact": "recipient_committed"})

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
        response = client.acknowledge_mailbox(
            event_id="event-1",
            envelope_digest="a" * 64,
        )
    finally:
        client.close()

    assert response.status_code == 200
    assert captured["url"] == (
        "https://api.corp.example/v1/mailbox/event-1/acknowledge"
    )
    assert json.loads(captured["body"]) == {"envelope_digest": "a" * 64}
    proof = captured["proof"]
    assert (proof.method, proof.path, proof.query) == (
        "POST",
        "/v1/mailbox/event-1/acknowledge",
        "",
    )


@pytest.mark.parametrize(
    "event_id,envelope_digest",
    [
        ("", "a" * 64),
        ("prefix/suffix", "a" * 64),
        ("prefix%2Fsuffix", "a" * 64),
        ("event+1", "a" * 64),
        ("event-1", "A" * 64),
        ("event-1", "short"),
    ],
)
def test_client_rejects_invalid_mailbox_acknowledgement_binding(
    event_id: str,
    envelope_digest: str,
) -> None:
    client = AgentNetClient(
        base_url="https://api.corp.example",
        key=P256KeyPair.generate(),
        domain_id="corp.example",
        harness_id="harness-1",
        credential_id="credential-1",
        audience="urn:agentnet:corp.example:corporate-api",
        transport=httpx.MockTransport(lambda _request: httpx.Response(500)),
    )
    try:
        with pytest.raises(ValidationError):
            client.acknowledge_mailbox(
                event_id=event_id,
                envelope_digest=envelope_digest,
            )
    finally:
        client.close()


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
