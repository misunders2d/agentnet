from __future__ import annotations

import hashlib
import json

import httpx
import pytest

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    verify_internal_broker_proof,
)
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.errors import AuthenticationError
from agentnet.operations.config import ApprovalServiceClientConfig


def _config() -> ApprovalServiceClientConfig:
    return ApprovalServiceClientConfig(
        origin="https://approval.corp.example",
        service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        approver_principal_id="security-owner",
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
        assert request.url.host == "approval.corp.example"
        proof = request.headers[INTERNAL_BROKER_PROOF_HEADER]
        verified = verify_internal_broker_proof(
            credential=secret,
            header_value=proof,
            audience="https://approval.corp.example",
            method="POST",
            path=request.url.path,
            purpose=purposes[request.url.path],
            raw_body=request.content,
        )
        assert verified.path == request.url.path
        proofs.append(proof)
        body = json.loads(request.content)
        observed.append((request.url.path, body))
        if request.url.path.endswith("/requests/status"):
            return httpx.Response(200, json={"state": "issued"})
        if request.url.path.endswith("/receipts/retrieve"):
            return httpx.Response(
                200,
                json={
                    "receipt": {
                        "schema": "agentnet.independent-approval.receipt.v1",
                        "opaque": "signed",
                    }
                },
            )
        return httpx.Response(
            201,
            json={
                "request_id": "request-1",
                "state": "pending",
                "transaction_digest": hashlib.sha256(b"{}").hexdigest(),
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        transaction_digest = hashlib.sha256(b"{}").hexdigest()
        created = client.create_request(
            idempotency_key="core:enrollment:test-1",
            domain_id="corp.example",
            approval_purpose="identity.enrollment.approve",
            canonical_transaction=b"{}",
            transaction_digest=transaction_digest,
        )
        assert created["request_id"] == "request-1"
        assert client.request_status(
            request_id="request-1",
            transaction_digest=transaction_digest,
        )["state"] == "issued"
        receipt = client.retrieve_receipt(
            request_id="request-1",
            claim_code="AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111",
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
                "request_id": "request-1",
                "state": "pending",
                "transaction_digest": "a" * 64,
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


def test_internal_client_rejects_short_runtime_secret() -> None:
    with pytest.raises(Exception, match="credential is unavailable"):
        ApprovalServiceClient(_config(), "short")
