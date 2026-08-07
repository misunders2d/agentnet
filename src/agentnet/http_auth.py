"""One canonical proof-authenticated Starlette request boundary."""

from __future__ import annotations

import logging

from starlette.requests import Request

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.actors import TrustedTransportContext
from agentnet.security.dpop import proof_from_headers


LOGGER = logging.getLogger(__name__)


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
        LOGGER.warning("AgentNet request authentication denied; reason=%s", exc)
        raise
    request.scope["agentnet.trusted_transport"] = context
    return body, context


__all__ = ["authenticate_proof_request"]
