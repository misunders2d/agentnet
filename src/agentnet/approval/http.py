"""Standalone browser/API surface for independent WebAuthn approval."""

from __future__ import annotations

import json
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.applications import Starlette
from starlette.datastructures import MutableHeaders
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import ValidationError


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


def _reject_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def _reject_nonfinite(_value: str) -> object:
    raise ValueError("non-finite JSON number")


async def _bounded_json(request: Request, service: WebAuthnApprovalService) -> dict[str, Any]:
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
    try:
        value = json.loads(
            b"".join(chunks).decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=_reject_nonfinite,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValidationError("request body is invalid") from exc
    if not isinstance(value, dict):
        raise ValidationError("request body is invalid")
    return value


def _json(value: Any, *, status_code: int = 200) -> JSONResponse:
    return JSONResponse(value, status_code=status_code)


def _denied() -> JSONResponse:
    return _json({"error": "request_denied"}, status_code=400)


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
    const receipt = await post('/v1/approval/requests/verify', {token, approved: true, credential: authenticationJSON(credential)});
    statusNode.textContent = 'Approved. Copy receipt to the existing AgentNet operation.';
    transactionNode.textContent = '';
    resultNode.textContent = JSON.stringify(receipt, null, 2);
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

    app = Starlette(
        debug=False,
        routes=[
            Route("/healthz", health, methods=["GET"]),
            Route("/approval", page, methods=["GET"]),
            Route("/approval.js", javascript, methods=["GET"]),
            Route("/v1/approval/registration/options", registration_options, methods=["POST"]),
            Route("/v1/approval/registration/verify", registration_verify, methods=["POST"]),
            Route("/v1/approval/requests/options", request_options, methods=["POST"]),
            Route("/v1/approval/requests/verify", request_verify, methods=["POST"]),
            Route("/v1/approval/requests/reject", request_reject, methods=["POST"]),
        ],
    )
    app.state.approval_service = service
    return _SecurityHeadersMiddleware(app)  # type: ignore[return-value]


__all__ = ["SECURITY_HEADERS", "create_approval_app"]
