"""Standalone browser/API surface for independent WebAuthn approval."""

from __future__ import annotations

import hashlib
import json
import os
import secrets
import time
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.applications import Starlette
from starlette.datastructures import MutableHeaders
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    verify_internal_broker_proof,
)
from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_json


SECURITY_HEADERS = {
    "Cache-Control": "no-store",
    "Pragma": "no-cache",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
    "X-Frame-Options": "DENY",
    "Cross-Origin-Resource-Policy": "same-origin",
    "Content-Security-Policy": (
        "default-src 'none'; script-src 'self'; connect-src 'self'; "
        "style-src 'none'; img-src 'none'; frame-ancestors 'none'; "
        "base-uri 'none'; form-action 'none'"
    ),
}


class _SecurityHeadersMiddleware:
    def __init__(self, app: Any) -> None:
        self.app = app

    async def __call__(self, scope: dict[str, Any], receive: Any, send: Any) -> None:
        async def secured(message: dict[str, Any]) -> None:
            if message.get("type") == "http.response.start":
                headers = MutableHeaders(scope=message)
                for name, value in SECURITY_HEADERS.items():
                    headers[name] = value
            await send(message)

        await self.app(scope, receive, secured)


class _TokenBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    token: str = Field(min_length=50, max_length=128)


class _CredentialBody(_TokenBody):
    credential: dict[str, Any]


class _ApproveBody(_CredentialBody):
    approved: Literal[True]


class _RejectBody(_TokenBody):
    pass


class _InternalCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-request-create.v1"] = Field(alias="schema")
    idempotency_key: str = Field(min_length=16, max_length=256)
    approver_principal_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    approval_purpose: str = Field(min_length=1, max_length=256)
    canonical_transaction_b64: str = Field(min_length=2, max_length=1_400_000)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")


class _InternalStatusBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-request-status.v1"] = Field(alias="schema")
    request_id: str = Field(min_length=1, max_length=128)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")


class _InternalRetrieveBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    schema_id: Literal["agentnet.approval.internal-receipt-retrieve.v1"] = Field(alias="schema")
    request_id: str = Field(min_length=1, max_length=128)
    claim_code: str = Field(pattern=r"^[0-9A-Fa-f]{4}(?:-[0-9A-Fa-f]{4}){7}$")
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    approval_purpose: str = Field(min_length=1, max_length=256)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    idempotency_key: str = Field(min_length=16, max_length=256)


def _reject_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def _reject_nonfinite(_value: str) -> object:
    raise ValueError("non-finite JSON number")


async def _bounded_body(request: Request, service: WebAuthnApprovalService) -> bytes:
    maximum = service.config.max_http_body_bytes
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            length = int(declared)
        except ValueError as exc:
            raise ValidationError("request body is invalid") from exc
        if length < 0 or length > maximum:
            raise ValidationError("request body is invalid")
    chunks: list[bytes] = []
    size = 0
    async for chunk in request.stream():
        size += len(chunk)
        if size > maximum:
            raise ValidationError("request body is invalid")
        chunks.append(chunk)
    return b"".join(chunks)


def _strict_json(raw_body: bytes) -> dict[str, Any]:
    try:
        value = json.loads(
            raw_body.decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=_reject_nonfinite,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValidationError("request body is invalid") from exc
    if not isinstance(value, dict):
        raise ValidationError("request body is invalid")
    return value


async def _bounded_json(request: Request, service: WebAuthnApprovalService) -> dict[str, Any]:
    return _strict_json(await _bounded_body(request, service))


def _json(value: Any, *, status_code: int = 200) -> JSONResponse:
    return JSONResponse(value, status_code=status_code)


def _denied() -> JSONResponse:
    return _json({"error": "request_denied"}, status_code=400)


def _single_header(request: Request, name: str) -> str:
    encoded = name.lower().encode("ascii")
    values = [
        value.decode("latin-1")
        for key, value in request.scope.get("headers", [])
        if key.lower() == encoded
    ]
    if len(values) != 1:
        raise AuthenticationError("approval request denied")
    return values[0]


def _require_internal_auth(request: Request, service: WebAuthnApprovalService) -> str:
    reference = getattr(service.config, "internal_core_credential_env", None)
    expected = os.environ.get(reference, "") if reference else ""
    supplied = _single_header(request, "authorization")
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
        header_value=_single_header(request, INTERNAL_BROKER_PROOF_HEADER),
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


_APPROVAL_HTML = """<!doctype html>
<meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<title>AgentNet Independent Approval</title>
<h1>AgentNet Independent Approval</h1>
<p id="status">Loading exact ceremony…</p>
<pre id="transaction"></pre>
<button id="approve" type="button" hidden>Approve exact transaction</button>
<button id="reject" type="button" hidden>Reject</button>
<pre id="result"></pre>
<script src="/approval.js" defer></script>
"""


_APPROVAL_JS = r"""'use strict';
const statusNode = document.getElementById('status');
const transactionNode = document.getElementById('transaction');
const approveButton = document.getElementById('approve');
const rejectButton = document.getElementById('reject');
const resultNode = document.getElementById('result');
const fragment = new URLSearchParams(location.hash.slice(1));
const token = fragment.get('token');
const kind = fragment.get('kind');
history.replaceState(null, '', '/approval');

function fromB64url(value) {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - value.length % 4) % 4);
  const raw = atob(padded);
  return Uint8Array.from(raw, c => c.charCodeAt(0));
}
function toB64url(value) {
  const bytes = new Uint8Array(value);
  let raw = '';
  for (const byte of bytes) raw += String.fromCharCode(byte);
  return btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function creationOptions(value) {
  value.challenge = fromB64url(value.challenge);
  value.user.id = fromB64url(value.user.id);
  value.excludeCredentials = (value.excludeCredentials || []).map(x => ({...x, id: fromB64url(x.id)}));
  return value;
}
function requestOptions(value) {
  value.challenge = fromB64url(value.challenge);
  value.allowCredentials = (value.allowCredentials || []).map(x => ({...x, id: fromB64url(x.id)}));
  return value;
}
function registrationJSON(credential) {
  return {
    id: credential.id,
    rawId: toB64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: toB64url(credential.response.clientDataJSON),
      attestationObject: toB64url(credential.response.attestationObject),
      transports: credential.response.getTransports ? credential.response.getTransports() : [],
    },
    clientExtensionResults: credential.getClientExtensionResults(),
    authenticatorAttachment: credential.authenticatorAttachment,
  };
}
function authenticationJSON(credential) {
  return {
    id: credential.id,
    rawId: toB64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: toB64url(credential.response.clientDataJSON),
      authenticatorData: toB64url(credential.response.authenticatorData),
      signature: toB64url(credential.response.signature),
      userHandle: credential.response.userHandle ? toB64url(credential.response.userHandle) : null,
    },
    clientExtensionResults: credential.getClientExtensionResults(),
    authenticatorAttachment: credential.authenticatorAttachment,
  };
}
async function post(path, body) {
  const response = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body),
  });
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || 'request_denied');
  return value;
}
async function start() {
  if (!token || !['registration', 'approval'].includes(kind)) throw new Error('request_denied');
  if (kind === 'registration') {
    statusNode.textContent = 'Register phishing-resistant approver credential.';
    approveButton.textContent = 'Register passkey';
    approveButton.hidden = false;
    approveButton.onclick = async () => {
      approveButton.disabled = true;
      const options = await post('/v1/approval/registration/options', {token});
      const credential = await navigator.credentials.create({publicKey: creationOptions(options.publicKey)});
      const result = await post('/v1/approval/registration/verify', {token, credential: registrationJSON(credential)});
      statusNode.textContent = 'Registration complete.';
      resultNode.textContent = JSON.stringify(result, null, 2);
    };
    return;
  }
  const options = await post('/v1/approval/requests/options', {token});
  statusNode.textContent = `Review ${options.approval_purpose} for ${options.domain_id}. Digest: ${options.transaction_digest}`;
  transactionNode.textContent = options.canonical_transaction_text;
  approveButton.hidden = false;
  rejectButton.hidden = false;
  approveButton.onclick = async () => {
    approveButton.disabled = true;
    rejectButton.disabled = true;
    const credential = await navigator.credentials.get({publicKey: requestOptions(options.publicKey)});
    const result = await post('/v1/approval/requests/verify', {token, approved: true, credential: authenticationJSON(credential)});
    transactionNode.textContent = '';
    if (options.delivery_mode === 'core_claim_code') {
      statusNode.textContent = 'Approved. Enter this one-time code into the fresh laptop AgentNet prompt.';
      resultNode.textContent = `One-time AgentNet approval code (expires ${result.expires_at}):\n${result.claim_code}`;
    } else {
      statusNode.textContent = 'Approved. Copy receipt to the existing AgentNet operation.';
      resultNode.textContent = JSON.stringify(result, null, 2);
    }
  };
  rejectButton.onclick = async () => {
    approveButton.disabled = true;
    rejectButton.disabled = true;
    await post('/v1/approval/requests/reject', {token});
    statusNode.textContent = 'Rejected. No receipt was issued.';
    transactionNode.textContent = '';
  };
}
start().catch(() => {
  statusNode.textContent = 'Request denied.';
  transactionNode.textContent = '';
  approveButton.hidden = true;
  rejectButton.hidden = true;
});
"""


def create_approval_app(service: WebAuthnApprovalService) -> Starlette:
    async def health(_request: Request) -> Response:
        return Response(status_code=204)

    async def page(_request: Request) -> Response:
        return HTMLResponse(_APPROVAL_HTML)

    async def javascript(_request: Request) -> Response:
        return Response(_APPROVAL_JS, media_type="application/javascript")

    async def registration_options(request: Request) -> Response:
        try:
            body = _TokenBody.model_validate(await _bounded_json(request, service))
            return _json(service.registration_options(body.token))
        except Exception:
            return _denied()

    async def registration_verify(request: Request) -> Response:
        try:
            body = _CredentialBody.model_validate(await _bounded_json(request, service))
            return _json(service.complete_registration(body.token, body.credential))
        except Exception:
            return _denied()

    async def request_options(request: Request) -> Response:
        try:
            body = _TokenBody.model_validate(await _bounded_json(request, service))
            return _json(service.request_options(body.token))
        except Exception:
            return _denied()

    async def request_verify(request: Request) -> Response:
        try:
            body = _ApproveBody.model_validate(await _bounded_json(request, service))
            return _json(
                service.approve_request(
                    body.token,
                    body.credential,
                    approved=body.approved,
                )
            )
        except Exception:
            return _denied()

    async def request_reject(request: Request) -> Response:
        try:
            body = _RejectBody.model_validate(await _bounded_json(request, service))
            return _json(service.reject_request(body.token))
        except Exception:
            return _denied()

    async def internal_create(request: Request) -> Response:
        try:
            raw_body = await _bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/requests",
                purpose=INTERNAL_BROKER_PURPOSE_CREATE,
            )
            value = _strict_json(raw_body)
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
            )
            return _json(
                {
                    "schema": "agentnet.approval.internal-request-created.v1",
                    "request_id": created.identifier,
                    "state": created.state,
                    "transaction_digest": created.transaction_digest,
                    "expires_at": created.expires_at,
                    "duplicate": created.duplicate,
                },
                status_code=200 if created.duplicate else 201,
            )
        except Exception:
            return _denied()

    async def internal_status(request: Request) -> Response:
        try:
            raw_body = await _bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/requests/status",
                purpose=INTERNAL_BROKER_PURPOSE_STATUS,
            )
            value = _strict_json(raw_body)
            if canonical_json(value) != raw_body:
                raise AuthenticationError("approval request denied")
            body = _InternalStatusBody.model_validate(value)
            return _json(
                service.request_status(
                    request_id=body.request_id,
                    transaction_digest=body.transaction_digest,
                )
            )
        except Exception:
            return _denied()

    async def internal_retrieve(request: Request) -> Response:
        try:
            raw_body = await _bounded_body(request, service)
            _require_internal_broker(
                request,
                service,
                raw_body=raw_body,
                path="/v1/approval/internal/receipts/retrieve",
                purpose=INTERNAL_BROKER_PURPOSE_RETRIEVE,
            )
            value = _strict_json(raw_body)
            if canonical_json(value) != raw_body:
                raise AuthenticationError("approval request denied")
            body = _InternalRetrieveBody.model_validate(value)
            retrieval_digest = hashlib.sha256(
                canonical_json(
                    {
                        "schema": body.schema_id,
                        "request_id": body.request_id,
                        "claim_code_sha256": hashlib.sha256(
                            body.claim_code.upper().encode("ascii")
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
                claim_code=body.claim_code,
                domain_id=body.domain_id,
                approval_purpose=body.approval_purpose,
                transaction_digest=body.transaction_digest,
                retrieval_digest=retrieval_digest,
            )
            return _json(
                {
                    "schema": "agentnet.approval.internal-receipt-retrieve-result.v1",
                    "request_id": body.request_id,
                    "receipt": receipt,
                    "receipt_digest": hashlib.sha256(canonical_json(receipt)).hexdigest(),
                }
            )
        except Exception:
            return _denied()

    routes = [
        Route("/healthz", health, methods=["GET"]),
        Route("/approval", page, methods=["GET"]),
        Route("/approval.js", javascript, methods=["GET"]),
        Route("/v1/approval/registration/options", registration_options, methods=["POST"]),
        Route("/v1/approval/registration/verify", registration_verify, methods=["POST"]),
        Route("/v1/approval/requests/options", request_options, methods=["POST"]),
        Route("/v1/approval/requests/verify", request_verify, methods=["POST"]),
        Route("/v1/approval/requests/reject", request_reject, methods=["POST"]),
    ]
    if getattr(service.config, "internal_core_credential_env", None) is not None:
        routes.extend(
            [
                Route("/v1/approval/internal/requests", internal_create, methods=["POST"]),
                Route("/v1/approval/internal/requests/status", internal_status, methods=["POST"]),
                Route(
                    "/v1/approval/internal/receipts/retrieve",
                    internal_retrieve,
                    methods=["POST"],
                ),
            ]
        )
    app = Starlette(
        debug=False,
        routes=routes,
    )
    app.state.approval_service = service
    return _SecurityHeadersMiddleware(app)  # type: ignore[return-value]


__all__ = ["SECURITY_HEADERS", "create_approval_app"]
