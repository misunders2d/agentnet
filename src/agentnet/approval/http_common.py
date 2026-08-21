"""Shared strict HTTP primitives for approval trust-boundary adapters."""

from __future__ import annotations

import json
from typing import Any

from starlette.requests import Request
from starlette.responses import JSONResponse

from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import AuthenticationError, ValidationError


def reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def reject_nonfinite_number(_value: str) -> object:
    raise ValueError("non-finite JSON number")


async def bounded_body(request: Request, service: WebAuthnApprovalService) -> bytes:
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


def strict_json(raw_body: bytes) -> dict[str, Any]:
    try:
        value = json.loads(
            raw_body.decode("utf-8"),
            object_pairs_hook=reject_duplicate_keys,
            parse_constant=reject_nonfinite_number,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValidationError("request body is invalid") from exc
    if not isinstance(value, dict):
        raise ValidationError("request body is invalid")
    return value


async def bounded_json(
    request: Request,
    service: WebAuthnApprovalService,
) -> dict[str, Any]:
    return strict_json(await bounded_body(request, service))


def json_response(value: Any, *, status_code: int = 200) -> JSONResponse:
    return JSONResponse(value, status_code=status_code)


def denied_response() -> JSONResponse:
    return json_response({"error": "request_denied"}, status_code=400)


def single_header(request: Request, name: str) -> str:
    encoded = name.lower().encode("ascii")
    values = [
        value.decode("latin-1")
        for key, value in request.scope.get("headers", [])
        if key.lower() == encoded
    ]
    if len(values) != 1:
        raise AuthenticationError("approval request denied")
    return values[0]


__all__ = [
    "bounded_body",
    "bounded_json",
    "denied_response",
    "json_response",
    "single_header",
    "strict_json",
]
