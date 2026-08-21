"""Signed internal Core-broker HTTP boundary for approval requests."""

from __future__ import annotations

import hashlib
import os
import secrets
import time
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.http_common import (
    bounded_body,
    denied_response,
    json_response,
    single_header,
    strict_json,
)
from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_READINESS,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    verify_internal_broker_proof,
)
from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_json


class _InternalCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-request-create.v2"] = Field(
        alias="schema"
    )
    idempotency_key: str = Field(min_length=16, max_length=256)
    approver_principal_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    approval_purpose: str = Field(min_length=1, max_length=256)
    canonical_transaction_b64: str = Field(min_length=2, max_length=1_400_000)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    possession_hash: str = Field(pattern=r"^[0-9a-f]{64}$")
    request_expires_at: int = Field(gt=0)


class _InternalStatusBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-request-status.v1"] = Field(
        alias="schema"
    )
    request_id: str = Field(min_length=1, max_length=128)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")


class _InternalRetrieveBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-receipt-retrieve.v2"] = Field(
        alias="schema"
    )
    request_id: str = Field(min_length=1, max_length=128)
    possession_secret: str = Field(pattern=r"^[\x21-\x7e]{16,256}$")
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    approval_purpose: str = Field(min_length=1, max_length=256)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    idempotency_key: str = Field(min_length=16, max_length=256)


def _internal_failure(exc: Exception) -> JSONResponse:
    if isinstance(exc, (AuthenticationError, ValidationError, PydanticValidationError)):
        return denied_response()
    return json_response({"error": "request_unavailable"}, status_code=503)


def _require_internal_auth(
    request: Request,
    service: WebAuthnApprovalService,
) -> str:
    reference = getattr(service.config, "internal_core_credential_env", None)
    expected = os.environ.get(reference, "") if reference else ""
    supplied = single_header(request, "authorization")
    candidate = supplied.removeprefix("Bearer ") if supplied.startswith("Bearer ") else ""
    valid = (
        bool(reference)
        and 43 <= len(expected) <= 512
        and all(0x21 <= ord(character) <= 0x7E for character in expected)
        and 43 <= len(candidate) <= 512
        and secrets.compare_digest(candidate, expected)
    )
    if not valid:
        raise AuthenticationError("approval request denied")
    return expected


def _require_internal_broker(
    request: Request,
    service: WebAuthnApprovalService,
    *,
    raw_body: bytes,
    path: str,
    purpose: str,
) -> None:
    credential = _require_internal_auth(request, service)
    if (
        request.method != "POST"
        or request.url.path != path
        or request.scope.get("query_string", b"") != b""
    ):
        raise AuthenticationError("approval request denied")
    audience = getattr(service.config, "public_origin", "").rstrip("/")
    clock = getattr(service, "clock", None)
    checked_at = clock() if callable(clock) else int(time.time())
    proof = verify_internal_broker_proof(
        credential=credential,
        header_value=single_header(request, INTERNAL_BROKER_PROOF_HEADER),
        audience=audience,
        method="POST",
        path=path,
        purpose=purpose,
        raw_body=raw_body,
        now=checked_at,
    )
    service.store.consume_internal_broker_replay(
        key_id=proof.key_id,
        nonce=proof.nonce,
        purpose=proof.purpose,
        audience=proof.audience,
        method=proof.method,
        path=proof.path,
        body_sha256=proof.body_sha256,
        issued_at=proof.issued_at,
        expires_at=proof.expires_at,
        consumed_at=checked_at,
    )


def create_internal_broker_routes(
    service: WebAuthnApprovalService,
) -> list[Route]:
    """Build only signed internal-broker routes for an approval service."""

    async def readiness(request: Request) -> Response:
        try:
            raw_body = await bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/readiness",
                purpose=INTERNAL_BROKER_PURPOSE_READINESS,
            )
            value = strict_json(raw_body)
            if value != {"schema": "agentnet.approval.internal-readiness.v1"}:
                raise AuthenticationError("approval request denied")
            return json_response(
                {
                    "schema": "agentnet.approval.internal-readiness-result.v1",
                    "status": "ready",
                }
            )
        except Exception as exc:
            return _internal_failure(exc)

    async def create_request(request: Request) -> Response:
        try:
            raw_body = await bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/requests",
                purpose=INTERNAL_BROKER_PURPOSE_CREATE,
            )
            value = strict_json(raw_body)
            if canonical_json(value) != raw_body:
                raise AuthenticationError("approval request denied")
            body = _InternalCreateBody.model_validate(value)
            canonical = b64url_decode(body.canonical_transaction_b64)
            if (
                b64url_encode(canonical) != body.canonical_transaction_b64
                or not secrets.compare_digest(
                    hashlib.sha256(canonical).hexdigest(),
                    body.transaction_digest,
                )
            ):
                raise ValidationError("request body is invalid")
            created = service.create_request(
                principal_id=body.approver_principal_id,
                domain_id=body.domain_id,
                approval_purpose=body.approval_purpose,
                canonical_transaction=canonical,
                delivery_mode="core_claim_code",
                idempotency_key=body.idempotency_key,
                possession_hash=body.possession_hash,
                request_expires_at=body.request_expires_at,
            )
            return json_response(
                {
                    "schema": "agentnet.approval.internal-request-created.v1",
                    "request_id": created.identifier,
                    "state": created.state,
                    "approval_purpose": body.approval_purpose,
                    "transaction_digest": created.transaction_digest,
                    "expires_at": created.expires_at,
                    "duplicate": created.duplicate,
                },
                status_code=200 if created.duplicate else 201,
            )
        except Exception as exc:
            return _internal_failure(exc)

    async def request_status(request: Request) -> Response:
        try:
            raw_body = await bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/requests/status",
                purpose=INTERNAL_BROKER_PURPOSE_STATUS,
            )
            value = strict_json(raw_body)
            if canonical_json(value) != raw_body:
                raise AuthenticationError("approval request denied")
            body = _InternalStatusBody.model_validate(value)
            return json_response(
                service.request_status(
                    request_id=body.request_id,
                    transaction_digest=body.transaction_digest,
                )
            )
        except Exception as exc:
            return _internal_failure(exc)

    async def retrieve_receipt(request: Request) -> Response:
        try:
            raw_body = await bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/receipts/retrieve",
                purpose=INTERNAL_BROKER_PURPOSE_RETRIEVE,
            )
            value = strict_json(raw_body)
            if canonical_json(value) != raw_body:
                raise AuthenticationError("approval request denied")
            body = _InternalRetrieveBody.model_validate(value)
            retrieval_digest = hashlib.sha256(
                canonical_json(
                    {
                        "schema": body.schema_id,
                        "request_id": body.request_id,
                        "possession_secret_sha256": hashlib.sha256(
                            body.possession_secret.encode("ascii")
                        ).hexdigest(),
                        "domain_id": body.domain_id,
                        "approval_purpose": body.approval_purpose,
                        "transaction_digest": body.transaction_digest,
                        "idempotency_key": body.idempotency_key,
                    }
                )
            ).hexdigest()
            receipt = service.retrieve_core_receipt(
                request_id=body.request_id,
                possession_secret=body.possession_secret,
                domain_id=body.domain_id,
                approval_purpose=body.approval_purpose,
                transaction_digest=body.transaction_digest,
                retrieval_digest=retrieval_digest,
            )
            return json_response(
                {
                    "schema": "agentnet.approval.internal-receipt-retrieve-result.v1",
                    "request_id": body.request_id,
                    "receipt": receipt,
                    "receipt_digest": hashlib.sha256(
                        canonical_json(receipt)
                    ).hexdigest(),
                }
            )
        except Exception as exc:
            return _internal_failure(exc)

    return [
        Route("/v1/approval/internal/readiness", readiness, methods=["POST"]),
        Route("/v1/approval/internal/requests", create_request, methods=["POST"]),
        Route(
            "/v1/approval/internal/requests/status",
            request_status,
            methods=["POST"],
        ),
        Route(
            "/v1/approval/internal/receipts/retrieve",
            retrieve_receipt,
            methods=["POST"],
        ),
    ]


__all__ = ["create_internal_broker_routes"]
