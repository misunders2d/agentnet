"""Host-bound sponsored-enrollment candidate API routes."""

from __future__ import annotations

import json
from collections.abc import Callable

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.console.headers import protected_headers
from agentnet.errors import GateBlocked, ValidationError
from agentnet.identity.sponsored_enrollment import SponsoredEnrollmentService


HostGuard = Callable[[Request], None]


class _SponsoredCandidateBegin(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    candidate_harness_id: str = Field(min_length=1, max_length=256)
    harness_kind: str = Field(min_length=1, max_length=64)
    harness_name: str = Field(min_length=1, max_length=128)
    binding_assurance: str = Field(pattern=r"^(os_bound|hardware_bound)$")
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    idempotency_key: str = Field(min_length=16, max_length=128)


def create_sponsored_candidate_routes(
    *,
    sponsored_enrollment: SponsoredEnrollmentService | None,
    require_host: HostGuard,
    max_request_bytes: int,
) -> list[Route]:
    """Mount candidate begin/status routes on their verified host boundary."""

    async def sponsored_candidate_begin(request: Request) -> Response:
        require_host(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        raw = await request.body()
        if len(raw) > max_request_bytes:
            raise ValidationError("request is too large")
        try:
            body = _SponsoredCandidateBegin.model_validate_json(raw)
        except PydanticValidationError as exc:
            raise ValidationError("candidate enrollment request is invalid") from exc
        result = sponsored_enrollment.begin_candidate(**body.model_dump())
        return JSONResponse(
            {
                "schema": "agentnet.sponsored-enrollment.candidate-begin-result.v1",
                "transaction_id": result.transaction_id,
                "authorization_url": result.authorization_url,
                "state": result.state,
                "continuation_token": result.continuation_token,
                "expires_at": result.expires_at,
            },
            status_code=201,
            headers=protected_headers(),
        )

    async def sponsored_candidate_status(request: Request) -> Response:
        require_host(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        raw = await request.body()
        if len(raw) > max_request_bytes:
            raise ValidationError("request is too large")
        try:
            body = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("candidate status request is invalid") from exc
        if (
            not isinstance(body, dict)
            or set(body) != {"continuation_token"}
            or not isinstance(body["continuation_token"], str)
        ):
            raise ValidationError("candidate status request is invalid")
        result = sponsored_enrollment.candidate_status(
            continuation_token=body["continuation_token"]
        )
        return JSONResponse(
            {
                "schema": "agentnet.sponsored-enrollment.candidate-status-result.v1",
                **result,
            },
            headers=protected_headers(),
        )

    return [
        Route(
            "/v1/sponsored-enrollment/candidate/begin",
            sponsored_candidate_begin,
            methods=["POST"],
        ),
        Route(
            "/v1/sponsored-enrollment/candidate/status",
            sponsored_candidate_status,
            methods=["POST"],
        ),
    ]


__all__ = ["create_sponsored_candidate_routes"]
