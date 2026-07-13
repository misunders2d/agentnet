"""Public, proof-bound enrollment ceremony routes.

These are the only unauthenticated product routes.  They accept no identity
claims: OIDC supplies the human identity, the candidate key proves possession,
and an independently verified WebAuthn receipt authorizes the exact transcript.
"""

from __future__ import annotations

import base64
import binascii
from dataclasses import asdict
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.oidc import OIDCEnrollmentCoordinator
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator


class OIDCBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    harness_kind: str = Field(min_length=1, max_length=64)
    harness_name: str = Field(min_length=1, max_length=128)
    public_key_pem: str = Field(min_length=128, max_length=16_384)


class EnrollmentCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    challenge_id: str = Field(min_length=16, max_length=128)
    nonce: str = Field(min_length=32, max_length=256)
    canonical_transaction_b64: str = Field(min_length=32, max_length=131_072)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    independent_approval: dict[str, Any]


def _public_headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


async def _bounded_body(request: Request, core: CommunicationCore) -> bytes:
    body = await request.body()
    if len(body) > core.config.max_request_bytes:
        raise ValidationError("request body exceeds configured limit")
    return body


def _rate_limit(core: CommunicationCore, request: Request) -> None:
    peer = "unavailable" if request.client is None else request.client.host
    core.quotas.consume(
        scope=f"public-enrollment:{peer}",
        metric="enrollment_attempts",
        amount=1,
        limit=20,
    )


def create_enrollment_routes(
    core: CommunicationCore,
    coordinator: OIDCEnrollmentCoordinator,
    *,
    recovery_coordinator: OIDCCredentialRecoveryCoordinator | None = None,
) -> list[Route]:
    """Mount one OIDC -> challenge -> PoP/OOB completion ceremony."""

    if coordinator.store is not core.store:
        raise ValueError("enrollment HTTP coordinator must share the core store")

    async def begin(request: Request) -> Response:
        _rate_limit(core, request)
        parsed = OIDCBeginBody.model_validate_json(await _bounded_body(request, core))
        authorization = coordinator.begin_authorization(
            domain_id=core.config.domain_id,
            harness_kind=parsed.harness_kind,
            harness_name=parsed.harness_name,
            public_key_pem=parsed.public_key_pem,
        )
        return JSONResponse(asdict(authorization), status_code=201, headers=_public_headers())

    async def callback(request: Request) -> Response:
        _rate_limit(core, request)
        pairs = request.query_params.multi_items()
        if any(key not in {"state", "code"} for key, _value in pairs):
            raise AuthenticationError("OIDC callback parameters are invalid")
        state_values = [value for key, value in pairs if key == "state"]
        code_values = [value for key, value in pairs if key == "code"]
        if len(state_values) != 1 or len(code_values) != 1:
            raise AuthenticationError("OIDC callback parameters are invalid")
        if recovery_coordinator is not None and recovery_coordinator.has_state(state_values[0]):
            recovery = recovery_coordinator.complete_authorization(
                state=state_values[0],
                code=code_values[0],
            )
            return JSONResponse(
                {
                    "recovery_request": recovery.request.model_dump(mode="json"),
                    "recovery_transaction_id": recovery.transaction_id,
                },
                headers=_public_headers(),
            )
        challenge = coordinator.complete_authorization(state=state_values[0], code=code_values[0])
        return JSONResponse(
            {
                "challenge_id": challenge.challenge_id,
                "nonce": challenge.nonce,
                "expires_at": challenge.expires_at,
                "canonical_transaction_b64": base64.b64encode(
                    challenge.canonical_transaction
                ).decode("ascii"),
            },
            headers=_public_headers(),
        )

    async def complete(request: Request) -> Response:
        _rate_limit(core, request)
        parsed = EnrollmentCompleteBody.model_validate_json(await _bounded_body(request, core))
        try:
            transaction = base64.b64decode(
                parsed.canonical_transaction_b64.encode("ascii"),
                validate=True,
            )
        except (UnicodeEncodeError, binascii.Error, ValueError) as exc:
            raise ValidationError("canonical enrollment transaction is not base64") from exc
        if not transaction or len(transaction) > 98_304:
            raise ValidationError("canonical enrollment transaction is outside the size bound")
        result = coordinator.enrollment.complete(
            challenge_id=parsed.challenge_id,
            nonce=parsed.nonce,
            canonical_transaction=transaction,
            possession_signature=parsed.possession_signature,
            approval=parsed.independent_approval,
        )
        value = asdict(result)
        value["actor"] = result.actor.model_dump(mode="json")
        return JSONResponse(value, status_code=201, headers=_public_headers())

    return [
        Route("/v1/enrollment/oidc/begin", begin, methods=["POST"]),
        Route("/v1/enrollment/oidc/callback", callback, methods=["GET"]),
        Route("/v1/enrollment/complete", complete, methods=["POST"]),
    ]


__all__ = ["create_enrollment_routes"]
