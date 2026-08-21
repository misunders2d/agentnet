"""Standalone browser/API surface for independent WebAuthn approval."""

from __future__ import annotations

from importlib.metadata import version as package_version
import secrets
import time
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from starlette.applications import Starlette
from starlette.datastructures import MutableHeaders
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, RedirectResponse, Response
from starlette.routing import Route

from agentnet.approval.owner_session import (
    OWNER_CSRF_COOKIE_NAME,
    OWNER_PREAUTH_COOKIE_NAME,
    OWNER_SESSION_COOKIE_NAME,
    OwnerApprovalCompleteRequest,
    OwnerApprovalSelectRequest,
    OwnerOIDCStartRequest,
    OwnerRegistrationBeginRequest,
    OwnerRegistrationCompleteRequest,
)
from agentnet.approval.http_common import (
    bounded_json as _bounded_json,
    denied_response as _denied,
    json_response as _json,
    single_header as _single_header,
)
from agentnet.approval.internal_http import create_internal_broker_routes
from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.oidc_callback import OIDCCallbackError, parse_oidc_callback_pairs


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


def _optional_cookie(request: Request, name: str) -> str | None:
    values = [
        value.decode("latin-1")
        for key, value in request.scope.get("headers", [])
        if key.lower() == b"cookie"
    ]
    if not values:
        return None
    if len(values) != 1:
        raise AuthenticationError("approval request denied")
    matches: list[str] = []
    for item in values[0].split(";"):
        key, separator, value = item.strip().partition("=")
        if separator and key == name:
            matches.append(value)
    if len(matches) > 1:
        raise AuthenticationError("approval request denied")
    return matches[0] if matches else None


def _require_same_origin(request: Request, service: WebAuthnApprovalService) -> None:
    origin = _single_header(request, "origin")
    if not secrets.compare_digest(origin, service.config.public_origin.rstrip("/")):
        raise AuthenticationError("approval request denied")


def _set_browser_cookie(
    response: Response,
    *,
    name: str,
    value: str,
    max_age: int,
    http_only: bool,
    same_site: Literal["strict", "lax"] = "strict",
) -> None:
    response.set_cookie(
        name,
        value,
        max_age=max_age,
        path="/",
        secure=True,
        httponly=http_only,
        samesite=same_site,
    )


def _delete_browser_cookie(response: Response, name: str, *, http_only: bool) -> None:
    response.delete_cookie(
        name,
        path="/",
        secure=True,
        httponly=http_only,
        samesite="strict",
    )


_APPROVAL_HTML = """<!doctype html>
<meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<title>AgentNet Approval</title>
<h1>AgentNet Approval</h1>
<p id="status">Checking owner session…</p>
<button id="signin" type="button" hidden>Sign in</button>
<button id="register" type="button" hidden>Register passkey</button>
<section id="requests-section" hidden>
  <h2>Pending approvals</h2>
  <div id="requests"></div>
</section>
<section id="review" hidden>
  <h2 id="review-title">Review request</h2>
  <ul id="review-statements"></ul>
  <details><summary>Advanced verification</summary><code id="review-digest"></code></details>
  <button id="approve" type="button">Approve with passkey</button>
  <button id="reject" type="button">Reject</button>
  <button id="regenerate" type="button" hidden>Generate a new one-time code</button>
</section>
<section id="claim-code-section" hidden>
  <h2>One-time approval code</h2>
  <p>Enter this code only in the local masked AgentNet prompt. It will not be shown again.</p>
  <code id="claim-code"></code>
</section>
<pre id="result"></pre>
<script src="/approval.js" defer></script>
"""


_APPROVAL_JS = r"""'use strict';
const statusNode = document.getElementById('status');
const signInButton = document.getElementById('signin');
const registerButton = document.getElementById('register');
const requestsSection = document.getElementById('requests-section');
const requestsNode = document.getElementById('requests');
const reviewNode = document.getElementById('review');
const reviewTitle = document.getElementById('review-title');
const reviewStatements = document.getElementById('review-statements');
const reviewDigest = document.getElementById('review-digest');
const approveButton = document.getElementById('approve');
const rejectButton = document.getElementById('reject');
const regenerateButton = document.getElementById('regenerate');
const claimCodeSection = document.getElementById('claim-code-section');
const claimCodeNode = document.getElementById('claim-code');
const resultNode = document.getElementById('result');
let csrfToken = null;
let selectedRequestId = null;
let pendingRequestChecks = 0;
const maxPendingRequestChecks = 20;

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
function authenticationOptions(value) {
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
async function get(path) {
  const response = await fetch(path, {credentials: 'same-origin', cache: 'no-store'});
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || 'request_denied');
  return value;
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
function showApprovalResult(result) {
  if (result.delivery_status === 'waiting_agent' || result.delivery_status === 'retrieved') {
    claimCodeSection.hidden = true;
    reviewNode.hidden = true;
    regenerateButton.hidden = true;
    approveButton.hidden = true;
    rejectButton.hidden = true;
    statusNode.textContent = result.delivery_status === 'retrieved'
      ? 'Approval complete. AgentNet continued successfully.'
      : 'Approval complete. AgentNet will continue automatically.';
  } else if (result.claim_code) {
    claimCodeNode.textContent = result.claim_code;
    claimCodeSection.hidden = false;
    reviewNode.hidden = true;
    statusNode.textContent = 'Approval recorded.';
  } else {
    statusNode.textContent = 'Approval is already recorded. Generate a new one-time code.';
    regenerateButton.hidden = false;
    approveButton.hidden = true;
    rejectButton.hidden = true;
  }
}
async function regenerateCode() {
  const result = await post('/v1/approval/owner/requests/regenerate-code', {
    schema: 'agentnet.approval.owner-request-select.v1',
    csrf_token: csrfToken,
    request_id: selectedRequestId,
  });
  showApprovalResult(result);
}
async function reviewRequest(item) {
  selectedRequestId = item.request_id;
  requestsSection.hidden = true;
  reviewNode.hidden = false;
  claimCodeSection.hidden = true;
  resultNode.textContent = '';
  regenerateButton.hidden = item.state !== 'issued';
  approveButton.hidden = item.state === 'issued';
  rejectButton.hidden = item.state === 'issued';
  regenerateButton.onclick = () => regenerateCode().catch(deny);
  if (item.state === 'issued') {
    reviewTitle.textContent = 'Approved request';
    reviewStatements.replaceChildren();
    reviewDigest.textContent = '';
    statusNode.textContent = 'Generate a new code only if the previous code was lost.';
    return;
  }
  const begun = await post('/v1/approval/owner/requests/options', {
    schema: 'agentnet.approval.owner-request-select.v1',
    csrf_token: csrfToken,
    request_id: selectedRequestId,
  });
  reviewTitle.textContent = begun.summary.title;
  reviewStatements.replaceChildren();
  for (const statement of begun.summary.statements) {
    const itemNode = document.createElement('li');
    itemNode.textContent = statement;
    reviewStatements.appendChild(itemNode);
  }
  reviewDigest.textContent = typeof begun.summary.advanced_digest === 'string'
    ? begun.summary.advanced_digest
    : '';
  approveButton.onclick = async () => {
    approveButton.disabled = true;
    const credential = await navigator.credentials.get({
      publicKey: authenticationOptions(begun.publicKey),
    });
    const result = await post('/v1/approval/owner/requests/complete', {
      schema: 'agentnet.approval.owner-request-complete.v1',
      csrf_token: csrfToken,
      request_id: selectedRequestId,
      credential: authenticationJSON(credential),
    });
    showApprovalResult(result);
  };
  rejectButton.onclick = async () => {
    rejectButton.disabled = true;
    await post('/v1/approval/owner/requests/reject', {
      schema: 'agentnet.approval.owner-request-select.v1',
      csrf_token: csrfToken,
      request_id: selectedRequestId,
    });
    reviewNode.hidden = true;
    statusNode.textContent = 'Request rejected.';
  };
}
async function loadRequests() {
  const value = await get('/v1/approval/owner/requests');
  requestsNode.replaceChildren();
  if (value.requests.length === 0) {
    requestsSection.hidden = true;
    if (pendingRequestChecks < maxPendingRequestChecks) {
      pendingRequestChecks += 1;
      statusNode.textContent = 'Waiting for the AgentNet request…';
      window.setTimeout(() => loadRequests().catch(deny), 500);
      return;
    }
    statusNode.textContent = 'Owner passkey is registered. No pending approvals.';
    return;
  }
  pendingRequestChecks = maxPendingRequestChecks;
  statusNode.textContent = 'Review the pending AgentNet request.';
  requestsSection.hidden = false;
  for (const item of value.requests) {
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = item.state === 'issued' ? 'Recover one-time code' : 'Review request';
    button.onclick = () => reviewRequest(item).catch(deny);
    requestsNode.appendChild(button);
  }
}
function deny() {
  statusNode.textContent = 'Request denied.';
  signInButton.hidden = true;
  registerButton.hidden = true;
  requestsSection.hidden = true;
  reviewNode.hidden = true;
  claimCodeSection.hidden = true;
}
async function start() {
  const session = await get('/v1/approval/owner/session');
  csrfToken = session.csrf_token;
  if (!session.authenticated) {
    statusNode.textContent = 'Sign in with the preapproved owner account.';
    signInButton.hidden = false;
    signInButton.onclick = async () => {
      signInButton.disabled = true;
      const started = await post('/v1/approval/owner/oidc/start', {
        schema: 'agentnet.approval.owner-oidc-start.v1',
        csrf_token: csrfToken,
      });
      window.location.assign(started.authorization_url);
    };
    return;
  }
  if (session.credential_registered) {
    await loadRequests();
    return;
  }
  statusNode.textContent = 'Register a phishing-resistant owner passkey.';
  registerButton.hidden = false;
  registerButton.onclick = async () => {
    registerButton.disabled = true;
    const begun = await post('/v1/approval/owner/registration/begin', {
      schema: 'agentnet.approval.owner-registration-begin.v1',
      csrf_token: csrfToken,
    });
    const credential = await navigator.credentials.create({
      publicKey: creationOptions(begun.publicKey),
    });
    const result = await post('/v1/approval/owner/registration/complete', {
      schema: 'agentnet.approval.owner-registration-complete.v1',
      csrf_token: csrfToken,
      ceremony_id: begun.ceremony_id,
      credential: registrationJSON(credential),
    });
    statusNode.textContent = result.registered ? 'Owner passkey registration complete.' : 'Request denied.';
    registerButton.hidden = true;
    await loadRequests();
  };
}
start().catch(deny);
"""


_LEGACY_APPROVAL_HTML = """<!doctype html>
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


_LEGACY_APPROVAL_JS = r"""'use strict';
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
function authenticationOptions(value) {
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
    const credential = await navigator.credentials.get({publicKey: authenticationOptions(options.publicKey)});
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
    owner_sessions = getattr(service, "owner_sessions", None)
    owner_oidc = getattr(service.config, "owner_oidc", None)
    if owner_oidc is not None and owner_sessions is None:
        raise ValueError("owner OIDC requires owner session service")
    if owner_oidc is None and owner_sessions is not None:
        raise ValueError("owner session service requires owner OIDC")

    async def health(_request: Request) -> Response:
        return JSONResponse(
            {
                "schema": "agentnet.approval.health.v1",
                "service": "agentnet-approval",
                "version": package_version("agentnet"),
                "status": "alive",
                "public_origin": service.config.public_origin,
                "verifier_id": service.config.verifier_id,
            }
        )

    async def page(_request: Request) -> Response:
        return HTMLResponse(
            _APPROVAL_HTML if owner_sessions is not None else _LEGACY_APPROVAL_HTML
        )

    async def javascript(_request: Request) -> Response:
        return Response(
            _APPROVAL_JS if owner_sessions is not None else _LEGACY_APPROVAL_JS,
            media_type="application/javascript",
        )

    async def owner_session_status(request: Request) -> Response:
        try:
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is not None:
                status = owner_sessions.session_status(session_token)
                return _json(
                    {
                        "schema": "agentnet.approval.owner-session-status.v1",
                        "authenticated": True,
                        "csrf_token": status.csrf_token,
                        "credential_registered": status.credential_registered,
                        "expires_at": status.expires_at,
                    }
                )
        except Exception:
            pass
        try:
            preauth_cookie = _optional_cookie(request, OWNER_PREAUTH_COOKIE_NAME)
            csrf_cookie = _optional_cookie(request, OWNER_CSRF_COOKIE_NAME)
            if not (
                isinstance(preauth_cookie, str)
                and len(preauth_cookie) == 43
                and isinstance(csrf_cookie, str)
                and len(csrf_cookie) == 43
            ):
                preauth = owner_sessions.create_preauth()
                preauth_cookie = preauth.session_token
                csrf_cookie = preauth.csrf_token
            response = _json(
                {
                    "schema": "agentnet.approval.owner-session-status.v1",
                    "authenticated": False,
                    "csrf_token": csrf_cookie,
                }
            )
            ttl = int(owner_sessions.provider.config.authorization_ttl_seconds)
            _set_browser_cookie(
                response,
                name=OWNER_PREAUTH_COOKIE_NAME,
                value=preauth_cookie,
                max_age=ttl,
                http_only=True,
                same_site="lax",
            )
            _set_browser_cookie(
                response,
                name=OWNER_CSRF_COOKIE_NAME,
                value=csrf_cookie,
                max_age=ttl,
                http_only=False,
            )
            _delete_browser_cookie(response, OWNER_SESSION_COOKIE_NAME, http_only=True)
            return response
        except Exception:
            return _denied()

    async def owner_oidc_start(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerOIDCStartRequest.model_validate(await _bounded_json(request, service))
            preauth_cookie = _optional_cookie(request, OWNER_PREAUTH_COOKIE_NAME)
            csrf_cookie = _optional_cookie(request, OWNER_CSRF_COOKIE_NAME)
            if preauth_cookie is None or csrf_cookie is None:
                raise AuthenticationError("approval request denied")
            started = owner_sessions.begin_oidc_login(
                preauth_cookie=preauth_cookie,
                csrf_cookie=csrf_cookie,
                csrf_token=body.csrf_token,
            )
            return _json(
                {
                    "schema": "agentnet.approval.owner-oidc-start-result.v1",
                    "authorization_url": started.authorization_url,
                    "expires_at": started.expires_at,
                }
            )
        except Exception:
            return _denied()

    async def owner_oidc_callback(request: Request) -> Response:
        try:
            query = parse_oidc_callback_pairs(request.query_params.multi_items())
            preauth_cookie = _optional_cookie(request, OWNER_PREAUTH_COOKIE_NAME)
            if preauth_cookie is None:
                raise AuthenticationError("approval request denied")
            if isinstance(query, OIDCCallbackError):
                owner_sessions.fail_oidc_login(
                    preauth_cookie=preauth_cookie,
                    state=query.state,
                )
                return _denied()
            completed = owner_sessions.complete_oidc_login(
                preauth_cookie=preauth_cookie,
                state=query.state,
                code=query.code,
            )
            response = RedirectResponse("/approval", status_code=303)
            now = int(time.time())
            max_age = max(1, int(completed.expires_at) - now)
            _set_browser_cookie(
                response,
                name=OWNER_SESSION_COOKIE_NAME,
                value=completed.session_token,
                max_age=max_age,
                http_only=True,
            )
            _set_browser_cookie(
                response,
                name=OWNER_CSRF_COOKIE_NAME,
                value=completed.csrf_token,
                max_age=max_age,
                http_only=False,
            )
            _delete_browser_cookie(response, OWNER_PREAUTH_COOKIE_NAME, http_only=True)
            return response
        except Exception:
            return _denied()

    async def owner_registration_begin(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerRegistrationBeginRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            begun = owner_sessions.begin_registration(
                session_token=session_token,
                csrf_token=body.csrf_token,
            )
            return _json(
                {
                    "schema": "agentnet.approval.owner-registration-ceremony.v1",
                    "ceremony_id": begun.ceremony_id,
                    "expires_at": begun.expires_at,
                    "publicKey": begun.public_key,
                }
            )
        except Exception:
            return _denied()

    async def owner_registration_complete(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerRegistrationCompleteRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                owner_sessions.complete_registration(
                    session_token=session_token,
                    csrf_token=body.csrf_token,
                    ceremony_id=body.ceremony_id,
                    credential=body.credential,
                )
            )
        except Exception:
            return _denied()

    async def owner_requests(request: Request) -> Response:
        try:
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                {
                    "schema": "agentnet.approval.owner-requests.v1",
                    "requests": owner_sessions.pending_approvals(
                        session_token=session_token,
                    ),
                }
            )
        except Exception:
            return _denied()

    async def owner_request_options(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerApprovalSelectRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                owner_sessions.begin_approval(
                    session_token=session_token,
                    csrf_token=body.csrf_token,
                    request_id=body.request_id,
                )
            )
        except Exception:
            return _denied()

    async def owner_request_complete(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerApprovalCompleteRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                owner_sessions.complete_approval(
                    session_token=session_token,
                    csrf_token=body.csrf_token,
                    request_id=body.request_id,
                    credential=body.credential,
                )
            )
        except Exception:
            return _denied()

    async def owner_request_reject(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerApprovalSelectRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                owner_sessions.reject_approval(
                    session_token=session_token,
                    csrf_token=body.csrf_token,
                    request_id=body.request_id,
                )
            )
        except Exception:
            return _denied()

    async def owner_request_regenerate_code(request: Request) -> Response:
        try:
            _require_same_origin(request, service)
            body = OwnerApprovalSelectRequest.model_validate(
                await _bounded_json(request, service)
            )
            session_token = _optional_cookie(request, OWNER_SESSION_COOKIE_NAME)
            if session_token is None:
                raise AuthenticationError("approval request denied")
            return _json(
                owner_sessions.regenerate_approval_code(
                    session_token=session_token,
                    csrf_token=body.csrf_token,
                    request_id=body.request_id,
                )
            )
        except Exception:
            return _denied()

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

    routes = [
        Route("/healthz", health, methods=["GET"]),
        Route("/approval", page, methods=["GET"]),
        Route("/approval.js", javascript, methods=["GET"]),
    ]
    if owner_sessions is None:
        routes.extend(
            [
                Route(
                    "/v1/approval/registration/options",
                    registration_options,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/registration/verify",
                    registration_verify,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/requests/options",
                    request_options,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/requests/verify",
                    request_verify,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/requests/reject",
                    request_reject,
                    methods=["POST"],
                ),
            ]
        )
    else:
        routes.extend(
            [
                Route("/v1/approval/owner/session", owner_session_status, methods=["GET"]),
                Route("/v1/approval/owner/oidc/start", owner_oidc_start, methods=["POST"]),
                Route(
                    "/v1/approval/owner/oidc/callback",
                    owner_oidc_callback,
                    methods=["GET"],
                ),
                Route(
                    "/v1/approval/owner/registration/begin",
                    owner_registration_begin,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/owner/registration/complete",
                    owner_registration_complete,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/owner/requests",
                    owner_requests,
                    methods=["GET"],
                ),
                Route(
                    "/v1/approval/owner/requests/options",
                    owner_request_options,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/owner/requests/complete",
                    owner_request_complete,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/owner/requests/reject",
                    owner_request_reject,
                    methods=["POST"],
                ),
                Route(
                    "/v1/approval/owner/requests/regenerate-code",
                    owner_request_regenerate_code,
                    methods=["POST"],
                ),
            ]
        )
    if (
        getattr(service.config, "internal_core_credential_env", None) is not None
        and owner_sessions is not None
    ):
        routes.extend(create_internal_broker_routes(service))
    app = Starlette(
        debug=False,
        routes=routes,
    )
    app.state.approval_service = service
    return _SecurityHeadersMiddleware(app)  # type: ignore[return-value]


__all__ = ["SECURITY_HEADERS", "create_approval_app"]
