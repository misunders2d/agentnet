from __future__ import annotations

import hashlib
import json

import ssl
import httpx
import pytest

import agentnet.client as client_module
from agentnet.client import MAX_ARTIFACT_BYTES, AgentNetClient
from agentnet.errors import ValidationError
from agentnet.security.dpop import proof_from_headers
from agentnet.security.signatures import P256KeyPair, canonical_json


def test_client_uses_platform_default_trust_store(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    captured: dict[str, object] = {}

    class StubClient:
        def close(self) -> None:
            pass

    def client_factory(**kwargs: object) -> StubClient:
        captured.update(kwargs)
        return StubClient()

    monkeypatch.setattr(client_module.ssl, "create_default_context", lambda: context)
    monkeypatch.setattr(client_module.httpx, "Client", client_factory)
    client = AgentNetClient(
        base_url="https://api.corp.example",
        key=P256KeyPair.generate(),
        domain_id="corp.example",
        harness_id="harness-1",
        credential_id="credential-1",
        audience="urn:agentnet:corp.example:corporate-api",
    )
    client.close()

    assert captured["verify"] is context


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
            collaboration_scope_id="collaboration-scope-1",
            event_id="event-1",
            envelope_digest="a" * 64,
        )
    finally:
        client.close()

    assert response.status_code == 200
    assert captured["url"] == (
        "https://api.corp.example/v1/mailbox/event-1/acknowledge"
    )
    assert json.loads(captured["body"]) == {
        "collaboration_scope_id": "collaboration-scope-1",
        "envelope_digest": "a" * 64,
    }
    proof = captured["proof"]
    assert (proof.method, proof.path, proof.query) == (
        "POST",
        "/v1/mailbox/event-1/acknowledge",
        "",
    )


@pytest.mark.parametrize(
    "collaboration_scope_id,event_id,envelope_digest",
    [
        ("", "event-1", "a" * 64),
        (" collaboration-scope-1", "event-1", "a" * 64),
        ("collaboration-scope-1", "", "a" * 64),
        ("collaboration-scope-1", "prefix/suffix", "a" * 64),
        ("collaboration-scope-1", "prefix%2Fsuffix", "a" * 64),
        ("collaboration-scope-1", "event+1", "a" * 64),
        ("collaboration-scope-1", "event-1", "A" * 64),
        ("collaboration-scope-1", "event-1", "short"),
    ],
)
def test_client_rejects_invalid_mailbox_acknowledgement_binding(
    collaboration_scope_id: str,
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
                collaboration_scope_id=collaboration_scope_id,
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


def test_client_signs_exact_binary_artifact_upload() -> None:
    captured: dict[str, object] = {}
    content = b"\x00binary\xffartifact\n"

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["content_type"] = request.headers["content-type"]
        captured["proof"] = proof_from_headers(dict(request.headers))
        captured["body"] = request.content
        return httpx.Response(200, json={"state": "object_verified"})

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
        response = client.upload_artifact_bytes(
            reservation_id="reservation-1",
            content=content,
        )
    finally:
        client.close()

    assert response.status_code == 200
    assert captured == {
        "url": "https://api.corp.example/v1/artifacts/reservations/reservation-1/bytes",
        "content_type": "application/octet-stream",
        "proof": captured["proof"],
        "body": content,
    }
    proof = captured["proof"]
    assert (proof.method, proof.path, proof.query, proof.body_digest) == (
        "POST",
        "/v1/artifacts/reservations/reservation-1/bytes",
        "",
        hashlib.sha256(content).hexdigest(),
    )


def test_client_artifact_control_methods_bind_exact_routes_and_bodies() -> None:
    calls: list[tuple[str, str, bytes]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append((request.method, request.url.path, request.content))
        if request.url.path == "/v1/artifacts/reservations":
            return httpx.Response(201, json={"reservation_id": "reservation-1"})
        if request.url.path.endswith("/promote"):
            return httpx.Response(201, json={"artifact_id": "artifact-1"})
        if request.url.path.endswith("/abort"):
            return httpx.Response(200, json={"state": "aborted"})
        return httpx.Response(200, json={"lifecycle_state": "active"})

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
        client.reserve_artifact(
            idempotency_key="artifact-client-key-0001",
            expected_digest="a" * 64,
            expected_size=12,
            media_type="application/octet-stream",
            classification="C2",
            required_attachment=False,
            ttl_seconds=600,
        )
        client.promote_artifact(
            reservation_id="reservation-1",
            object_version="b" * 64,
            provenance={"origin": "client-test"},
        )
        client.abort_artifact_reservation(reservation_id="reservation-1")
        client.artifact_lifecycle(artifact_id="artifact-1")
    finally:
        client.close()

    assert calls == [
        (
            "POST",
            "/v1/artifacts/reservations",
            canonical_json(
                {
                    "classification": "C2",
                    "expected_digest": "a" * 64,
                    "expected_size": 12,
                    "idempotency_key": "artifact-client-key-0001",
                    "media_type": "application/octet-stream",
                    "required_attachment": False,
                    "ttl_seconds": 600,
                }
            ),
        ),
        (
            "POST",
            "/v1/artifacts/reservations/reservation-1/promote",
            canonical_json(
                {
                    "object_version": "b" * 64,
                    "provenance": {"origin": "client-test"},
                }
            ),
        ),
        (
            "POST",
            "/v1/artifacts/reservations/reservation-1/abort",
            b"",
        ),
        (
            "GET",
            "/v1/artifacts/artifact-1/lifecycle",
            b"",
        ),
    ]


def test_client_download_keeps_single_use_capability_internal() -> None:
    calls: list[httpx.Request] = []
    capability = "secret-capability-token-value-12345"

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request)
        if request.url.path.endswith("/download-capabilities"):
            return httpx.Response(
                200,
                json={"artifact_id": "artifact-1", "capability": capability},
            )
        assert request.url.path == "/v1/artifacts/download"
        assert json.loads(request.content) == {"token": capability}
        return httpx.Response(
            200,
            content=b"artifact bytes",
            headers={"Content-Type": "application/octet-stream"},
        )

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
        response = client.download_artifact(artifact_id="artifact-1", ttl_seconds=45)
    finally:
        client.close()

    assert response.status_code == 200
    assert response.content == b"artifact bytes"
    with pytest.raises(RuntimeError, match="request instance has not been set"):
        _ = response.request
    assert [request.url.path for request in calls] == [
        "/v1/artifacts/artifact-1/download-capabilities",
        "/v1/artifacts/download",
    ]
    assert json.loads(calls[0].content) == {"ttl_seconds": 45}


def test_client_rejects_oversize_download_before_buffering_body() -> None:
    calls = 0

    def handler(request: httpx.Request) -> httpx.Response:
        nonlocal calls
        calls += 1
        if calls == 1:
            return httpx.Response(
                200,
                json={
                    "artifact_id": "artifact-1",
                    "capability": "secret-capability-token-value-12345",
                },
            )
        return httpx.Response(
            200,
            content=b"",
            headers={
                "Content-Type": "application/octet-stream",
                "Content-Length": str(MAX_ARTIFACT_BYTES + 1),
            },
        )

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
        with pytest.raises(ValidationError, match="exceeds the artifact size limit"):
            client.download_artifact(artifact_id="artifact-1")
    finally:
        client.close()

    assert calls == 2


def test_client_rejects_unsafe_artifact_routes_and_oversize_binary() -> None:
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
            client.upload_artifact_bytes(
                reservation_id="prefix/suffix",
                content=b"safe",
            )
        with pytest.raises(ValidationError):
            client.request_bytes(
                "POST",
                "/v1/artifacts/reservations/reservation-1/bytes",
                content=b"x" * (MAX_ARTIFACT_BYTES + 1),
            )
        with pytest.raises(ValidationError):
            client.request_bytes(
                "POST",
                "/v1/artifacts/reservations/reservation-1/bytes",
                content=b"safe",
                content_type="text/plain",
            )
    finally:
        client.close()
