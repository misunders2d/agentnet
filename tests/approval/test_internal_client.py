from __future__ import annotations

import hashlib
import json

import httpx
import pytest

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_READINESS,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    verify_internal_broker_proof,
)
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.operations.config import ApprovalServiceClientConfig


def _config() -> ApprovalServiceClientConfig:
    return ApprovalServiceClientConfig(
        origin="https://approval.corp.example",
        public_origin="https://approval-public.corp.example",
        service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        approver_principal_id="security-owner",
        remote_activation_oidc_subject="approved-owner-subject",
    )


def test_internal_client_binds_runtime_secret_and_exact_bounded_routes() -> None:
    secret = "S" * 43
    observed: list[tuple[str, dict[str, object]]] = []
    proofs: list[str] = []
    purposes = {
        "/v1/approval/internal/requests": INTERNAL_BROKER_PURPOSE_CREATE,
        "/v1/approval/internal/requests/status": INTERNAL_BROKER_PURPOSE_STATUS,
        "/v1/approval/internal/receipts/retrieve": INTERNAL_BROKER_PURPOSE_RETRIEVE,
    }

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.headers["authorization"] == f"Bearer {secret}"
        assert request.url.host == "approval-public.corp.example"
        proof = request.headers[INTERNAL_BROKER_PROOF_HEADER]
        verified = verify_internal_broker_proof(
            credential=secret,
            header_value=proof,
            audience="https://approval-public.corp.example",
            method="POST",
            path=request.url.path,
            purpose=purposes[request.url.path],
            raw_body=request.content,
        )
        assert verified.path == request.url.path
        proofs.append(proof)
        body = json.loads(request.content)
        observed.append((request.url.path, body))
        transaction_digest = hashlib.sha256(b"{}").hexdigest()
        if request.url.path.endswith("/requests/status"):
            return httpx.Response(
                200,
                json={
                    "schema": "agentnet.approval.internal-request-status-result.v1",
                    "request_id": "request-1",
                    "state": "issued",
                    "transaction_digest": transaction_digest,
                    "expires_at": 1_800_000_300,
                },
            )
        if request.url.path.endswith("/receipts/retrieve"):
            receipt = {
                "schema": "agentnet.independent-approval.receipt.v1",
                "opaque": "signed",
            }
            return httpx.Response(
                200,
                json={
                    "schema": "agentnet.approval.internal-receipt-retrieve-result.v1",
                    "request_id": "request-1",
                    "receipt": receipt,
                    "receipt_digest": hashlib.sha256(
                        json.dumps(
                            receipt, sort_keys=True, separators=(",", ":")
                        ).encode("utf-8")
                    ).hexdigest(),
                },
            )
        return httpx.Response(
            201,
            json={
                "schema": "agentnet.approval.internal-request-created.v1",
                "request_id": "request-1",
                "state": "pending",
                "approval_purpose": "identity.enrollment.approve",
                "transaction_digest": transaction_digest,
                "expires_at": 1_800_000_300,
                "duplicate": False,
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        transaction_digest = hashlib.sha256(b"{}").hexdigest()
        possession_secret = "P" * 43
        possession_hash = hashlib.sha256(possession_secret.encode("ascii")).hexdigest()
        created = client.create_request(
            idempotency_key="core:enrollment:test-1",
            domain_id="corp.example",
            approval_purpose="identity.enrollment.approve",
            canonical_transaction=b"{}",
            transaction_digest=transaction_digest,
            possession_hash=possession_hash,
            request_expires_at=1_800_000_300,
        )
        assert created["request_id"] == "request-1"
        assert client.request_status(
            request_id="request-1",
            transaction_digest=transaction_digest,
        )["state"] == "issued"
        receipt = client.retrieve_receipt(
            request_id="request-1",
            possession_secret=possession_secret,
            domain_id="corp.example",
            approval_purpose="identity.enrollment.approve",
            transaction_digest=transaction_digest,
            idempotency_key="core:enrollment-complete:test-1",
        )
        assert receipt["opaque"] == "signed"
    finally:
        client.close()

    rendered = json.dumps(observed)
    assert secret not in rendered
    assert "approval_url" not in rendered
    assert [path for path, _body in observed] == [
        "/v1/approval/internal/requests",
        "/v1/approval/internal/requests/status",
        "/v1/approval/internal/receipts/retrieve",
    ]
    assert len(set(proofs)) == 3


def test_internal_client_readiness_uses_public_topology_and_exact_proof() -> None:
    secret = "S" * 43

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.host == "approval-public.corp.example"
        assert request.content == b'{"schema":"agentnet.approval.internal-readiness.v1"}'
        verify_internal_broker_proof(
            credential=secret,
            header_value=request.headers[INTERNAL_BROKER_PROOF_HEADER],
            audience="https://approval-public.corp.example",
            method="POST",
            path="/v1/approval/internal/readiness",
            purpose=INTERNAL_BROKER_PURPOSE_READINESS,
            raw_body=request.content,
        )
        return httpx.Response(
            200,
            json={
                "schema": "agentnet.approval.internal-readiness-result.v1",
                "status": "ready",
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        assert client.readiness() == {
            "schema": "agentnet.approval.internal-readiness-result.v1",
            "status": "ready",
        }
    finally:
        client.close()


@pytest.mark.parametrize(
    ("response", "gate"),
    [
        (httpx.Response(199), "approval_broker_auth"),
        (httpx.Response(201, json={}), "approval_broker_auth"),
        (httpx.Response(307), "approval_broker_auth"),
        (httpx.Response(401), "approval_broker_auth"),
        (httpx.Response(408), "approval_broker_unavailable"),
        (httpx.Response(425), "approval_broker_unavailable"),
        (httpx.Response(429), "approval_broker_unavailable"),
        (httpx.Response(503), "approval_broker_unavailable"),
    ],
)
def test_internal_client_readiness_has_total_sanitized_classification(
    response: httpx.Response,
    gate: str,
) -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(lambda _request: response),
    )
    try:
        with pytest.raises(GateBlocked) as exc_info:
            client.readiness()
        assert exc_info.value.gate == gate
    finally:
        client.close()


def test_internal_client_uses_fresh_proof_for_same_business_retry() -> None:
    secret = "S" * 43
    proofs: list[str] = []
    bodies: list[bytes] = []

    def handler(request: httpx.Request) -> httpx.Response:
        proofs.append(request.headers[INTERNAL_BROKER_PROOF_HEADER])
        bodies.append(request.content)
        return httpx.Response(
            200,
            json={
                "schema": "agentnet.approval.internal-request-created.v1",
                "request_id": "request-1",
                "state": "pending",
                "approval_purpose": "identity.enrollment.approve",
                "transaction_digest": hashlib.sha256(b"{}").hexdigest(),
                "expires_at": 1_800_000_300,
                "duplicate": True,
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        for _attempt in range(2):
            client.create_request(
                idempotency_key="core:enrollment:same-business-request",
                domain_id="corp.example",
                approval_purpose="identity.enrollment.approve",
                canonical_transaction=b"{}",
                transaction_digest=hashlib.sha256(b"{}").hexdigest(),
                possession_hash=hashlib.sha256(b"waiting-process-secret").hexdigest(),
                request_expires_at=1_800_000_300,
            )
    finally:
        client.close()

    assert bodies[0] == bodies[1]
    assert proofs[0] != proofs[1]


def test_internal_client_rejects_redirect_and_duplicate_json() -> None:
    for response in (
        httpx.Response(307, headers={"location": "https://attacker.example"}),
        httpx.Response(200, content=b'{"state":"issued","state":"pending"}'),
    ):
        client = ApprovalServiceClient(
            _config(),
            "S" * 43,
            transport=httpx.MockTransport(lambda _request, result=response: result),
        )
        try:
            with pytest.raises(AuthenticationError, match="approval service"):
                client.request_status(request_id="request-1", transaction_digest="a" * 64)
        finally:
            client.close()


@pytest.mark.parametrize(
    "response",
    [
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "request-1",
            "state": "unknown",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
        },
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "different-request",
            "state": "issued",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
        },
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "request-1",
            "state": "issued",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
            "private_detail": "must-be-rejected",
        },
    ],
)
def test_internal_client_rejects_non_exact_status_response(response) -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(lambda _request: httpx.Response(200, json=response)),
    )
    try:
        with pytest.raises(AuthenticationError, match="approval service response"):
            client.request_status(request_id="request-1", transaction_digest="a" * 64)
    finally:
        client.close()


def test_internal_client_maps_service_unavailable_to_retryable_gate() -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(
            lambda _request: httpx.Response(503, json={"error": "request_unavailable"})
        ),
    )
    try:
        with pytest.raises(GateBlocked, match="approval service is unavailable"):
            client.request_status(request_id="request-1", transaction_digest="a" * 64)
    finally:
        client.close()


def test_internal_client_rejects_short_runtime_secret() -> None:
    with pytest.raises(Exception, match="credential is unavailable"):
        ApprovalServiceClient(_config(), "short")
