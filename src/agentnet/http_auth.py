"""One canonical proof-authenticated Starlette request boundary."""

from __future__ import annotations

import logging
from threading import Lock

from starlette.requests import Request

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.actors import TrustedTransportContext
from agentnet.identity.context import ExpiredCredentialTransportContext
from agentnet.security.dpop import proof_from_headers


LOGGER = logging.getLogger(__name__)
_AUTH_FAILURE_LOG_LOCK = Lock()
_LOGGED_AUTH_FAILURE_REASONS: set[str] = set()


def _log_auth_failure_once(exc: AuthenticationError) -> None:
    reason = str(exc)
    with _AUTH_FAILURE_LOG_LOCK:
        if reason in _LOGGED_AUTH_FAILURE_REASONS:
            return
        _LOGGED_AUTH_FAILURE_REASONS.add(reason)
    LOGGER.warning("AgentNet request authentication denied; reason=%s", reason)


async def authenticate_proof_request(
    request: Request,
    core: CommunicationCore,
) -> tuple[bytes, TrustedTransportContext]:
    body = await request.body()
    if len(body) > core.config.max_request_bytes:
        raise ValidationError("request body exceeds configured limit")
    try:
        proof = proof_from_headers(dict(request.headers))
        try:
            raw_path = request.scope.get(
                "raw_path", request.url.path.encode("ascii")
            ).decode("ascii")
            raw_query = request.scope.get("query_string", b"").decode("ascii")
        except UnicodeError as exc:
            raise ValidationError("request target must use canonical ASCII encoding") from exc
        authority = request.headers.get("host")
        if not authority:
            raise ValidationError("request authority is required")
        context = core.authenticate(
            proof,
            method=request.method,
            scheme=request.scope.get("scheme", ""),
            authority=authority,
            path=raw_path,
            query=raw_query,
            body=body,
            caller_claims=None,
        )
    except AuthenticationError as exc:
        _log_auth_failure_once(exc)
        raise
    request.scope["agentnet.trusted_transport"] = context
    return body, context


async def authenticate_expired_credential_request(
    request: Request,
    core: CommunicationCore,
    *,
    allow_retired_predecessor: bool,
) -> tuple[bytes, ExpiredCredentialTransportContext]:
    """Authenticate only an expired binding; never mint a policy actor."""

    body = await request.body()
    if len(body) > core.config.max_request_bytes:
        raise ValidationError("request body exceeds configured limit")
    try:
        proof = proof_from_headers(dict(request.headers))
        try:
            raw_path = request.scope.get(
                "raw_path", request.url.path.encode("ascii")
            ).decode("ascii")
            raw_query = request.scope.get("query_string", b"").decode("ascii")
        except UnicodeError as exc:
            raise ValidationError(
                "request target must use canonical ASCII encoding"
            ) from exc
        authority = request.headers.get("host")
        if not authority:
            raise ValidationError("request authority is required")
        context = core.authenticate_expired_credential(
            proof,
            method=request.method,
            scheme=request.scope.get("scheme", ""),
            authority=authority,
            path=raw_path,
            query=raw_query,
            body=body,
            allow_retired_predecessor=allow_retired_predecessor,
        )
    except AuthenticationError as exc:
        _log_auth_failure_once(exc)
        raise
    request.scope["agentnet.expired_credential_transport"] = context
    return body, context


__all__ = [
    "authenticate_expired_credential_request",
    "authenticate_proof_request",
]
