"""Strict authenticated HTTP routes for the bounded C0 BootstrapGrantPlan."""

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

from agentnet.authorization.bootstrap_plan import (
    BootstrapPlanBeginRequest,
    BootstrapPlanCompletionRequest,
    BootstrapPlanStatusRequest,
)
from agentnet.authorization.bootstrap_plan_service import (
    BootstrapPlanService,
    BootstrapPlanTerminalError,
)
from agentnet.core.app import CommunicationCore
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
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
            parse_constant=lambda _value: (_ for _ in ()).throw(
                ValueError("non-finite value")
            ),
        )
        if not isinstance(value, dict) or canonical_json(value) != raw:
            raise ValueError("noncanonical JSON")
        return model.model_validate(value)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError, PydanticValidationError) as exc:
        raise ValidationError("bootstrap plan request is invalid") from exc


def _error(*, status: int, code: str, retryable: bool) -> JSONResponse:
    return JSONResponse(
        {
            "schema": "agentnet.bootstrap-plan.error.v1",
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
        return _error(status=403, code="bootstrap_plan_denied", retryable=False)
    if isinstance(exc, BootstrapPlanTerminalError):
        return _error(status=410, code="bootstrap_plan_terminal", retryable=False)
    if isinstance(exc, ConflictError):
        return _error(status=409, code="bootstrap_plan_conflict", retryable=False)
    if not isinstance(exc, GateBlocked):
        LOGGER.error(
            "bootstrap plan route unavailable; correlation=%s classification=unexpected",
            uuid4(),
        )
    return _error(status=503, code="bootstrap_plan_unavailable", retryable=True)


def create_bootstrap_plan_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    *,
    service: BootstrapPlanService,
) -> list[Route]:
    """Mount only the narrow begin/status/complete S4 surface."""

    if service.store is not core.store:
        raise ValueError("bootstrap plan HTTP service must share the Core store")

    async def begin(request: Request) -> Response:
        try:
            body, actor = await body_and_actor(request, core)
            parsed = _strict_model(body, BootstrapPlanBeginRequest)
            return JSONResponse(
                service.begin(actor=actor, request=parsed),
                status_code=201,
                headers=_headers(),
            )
        except Exception as exc:
            return _mapped_error(exc)

    async def status(request: Request) -> Response:
        try:
            body, actor = await body_and_actor(request, core)
            parsed = _strict_model(body, BootstrapPlanStatusRequest)
            return JSONResponse(
                service.status(actor=actor, request=parsed),
                status_code=200,
                headers=_headers(),
            )
        except Exception as exc:
            return _mapped_error(exc)

    async def complete(request: Request) -> Response:
        try:
            body, actor = await body_and_actor(request, core)
            parsed = _strict_model(body, BootstrapPlanCompletionRequest)
            return JSONResponse(
                service.complete(actor=actor, request=parsed),
                status_code=201,
                headers=_headers(),
            )
        except Exception as exc:
            return _mapped_error(exc)

    return [
        Route("/v1/bootstrap-plan/begin", begin, methods=["POST"]),
        Route("/v1/bootstrap-plan/status", status, methods=["POST"]),
        Route("/v1/bootstrap-plan/complete", complete, methods=["POST"]),
    ]


__all__ = ["create_bootstrap_plan_routes"]
