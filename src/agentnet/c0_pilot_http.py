"""Strict signed HTTP surface for the selector-free C0 pilot."""

from __future__ import annotations

import json
import logging
from collections.abc import Awaitable, Callable
from typing import Any, TypeVar
from uuid import uuid4

from pydantic import BaseModel, ValidationError as PydanticValidationError
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.c0_pilot import (
    C0PilotCompleteRequest,
    C0PilotRespondRequest,
    C0PilotStartRequest,
    C0PilotStatusRequest,
)
from agentnet.core.app import CommunicationCore
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
    RetryableConflictError,
    ValidationError,
)
from agentnet.security.signatures import canonical_json


LOGGER = logging.getLogger(__name__)
BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]
ModelT = TypeVar("ModelT", bound=BaseModel)


def _headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


def _strict_model(raw: bytes, model: type[ModelT]) -> ModelT:
    def pairs(values: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = value
        return result

    try:
        value = json.loads(
            raw,
            object_pairs_hook=pairs,
            parse_constant=lambda _value: (_ for _ in ()).throw(ValueError("non-finite value")),
        )
        if not isinstance(value, dict) or canonical_json(value) != raw:
            raise ValueError("noncanonical JSON")
        return model.model_validate(value)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError, PydanticValidationError) as exc:
        raise ValidationError("C0 pilot request is invalid") from exc


def _error(*, status: int, code: str, retryable: bool) -> JSONResponse:
    return JSONResponse(
        {
            "schema": "agentnet.c0-pilot.error.v1",
            "code": code,
            "message": "request denied",
            "retryable": retryable,
        },
        status_code=status,
        headers=_headers(),
    )


def _mapped_error(exc: Exception) -> JSONResponse:
    if isinstance(exc, (ValidationError, PydanticValidationError)):
        return _error(status=400, code="invalid_request", retryable=False)
    if isinstance(exc, AuthenticationError):
        return _error(status=401, code="authentication_denied", retryable=False)
    if isinstance(exc, AuthorizationError):
        return _error(status=403, code="c0_pilot_denied", retryable=False)
    if isinstance(exc, RetryableConflictError):
        return _error(status=409, code="c0_pilot_conflict", retryable=True)
    if isinstance(exc, ConflictError):
        return _error(status=409, code="c0_pilot_conflict", retryable=False)
    if not isinstance(exc, GateBlocked):
        LOGGER.error(
            "C0 pilot route unavailable; correlation=%s classification=unexpected",
            uuid4(),
        )
    return _error(status=503, code="c0_pilot_unavailable", retryable=True)


def create_c0_pilot_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
) -> list[Route]:
    if core.c0_pilot_service is None:
        return []

    def handler(model: type[ModelT], operation: str, success_status: int = 200):
        async def route(request: Request) -> Response:
            try:
                body, actor = await body_and_actor(request, core)
                _strict_model(body, model)
                result = getattr(core, operation)(actor=actor)
                return JSONResponse(result, status_code=success_status, headers=_headers())
            except Exception as exc:
                return _mapped_error(exc)

        return route

    return [
        Route(
            "/v1/c0-pilot/start",
            handler(C0PilotStartRequest, "c0_pilot_start", 201),
            methods=["POST"],
        ),
        Route(
            "/v1/c0-pilot/respond",
            handler(C0PilotRespondRequest, "c0_pilot_respond"),
            methods=["POST"],
        ),
        Route(
            "/v1/c0-pilot/complete",
            handler(C0PilotCompleteRequest, "c0_pilot_complete"),
            methods=["POST"],
        ),
        Route(
            "/v1/c0-pilot/status",
            handler(C0PilotStatusRequest, "c0_pilot_status"),
            methods=["POST"],
        ),
    ]


__all__ = ["create_c0_pilot_routes"]
