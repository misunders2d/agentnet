"""Authenticated routes for the first-positive-authority ceremony."""

from __future__ import annotations

import base64
import binascii
from collections.abc import Awaitable, Callable
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authorization.authority_bootstrap import FirstAuthorityBootstrapService
from agentnet.core.app import CommunicationCore
from agentnet.errors import GateBlocked, ValidationError


class AuthorityBootstrapBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")


class AuthorityBootstrapCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    nonce: str = Field(min_length=32, max_length=256)
    canonical_transaction_b64: str = Field(min_length=32, max_length=131_072)
    independent_approval: dict[str, Any]


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]


def _headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


def create_authority_bootstrap_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    *,
    service: FirstAuthorityBootstrapService | None = None,
) -> list[Route]:
    """Mount a challenge/approval pair behind the ordinary DPoP boundary."""

    if service is None:
        if core.oidc_enrollment is None:
            raise GateBlocked(
                "authority_bootstrap",
                "authority bootstrap requires configured production OIDC enrollment",
            )
        verifier = core.oidc_enrollment.enrollment.approval_verifier
        if not isinstance(verifier, IndependentApprovalVerifier):
            raise GateBlocked(
                "authority_bootstrap",
                "authority bootstrap requires the configured independent approval verifier",
            )
        service = FirstAuthorityBootstrapService(
            core.store,
            core.policy,
            verifier,
            runtime_profile=core.config.profile,
            outage_gate=core.outage,
        )
    elif service.store is not core.store:
        raise ValueError("authority bootstrap HTTP service must share the core store")

    async def begin(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        AuthorityBootstrapBeginBody.model_validate_json(body)
        challenge = service.begin(actor=actor)
        return JSONResponse(
            {
                "candidate_entitlement": challenge.candidate_entitlement.model_dump(mode="json"),
                "canonical_transaction_b64": base64.b64encode(
                    challenge.canonical_transaction
                ).decode("ascii"),
                "challenge_id": challenge.challenge_id,
                "expires_at": challenge.expires_at.isoformat(),
                "nonce": challenge.nonce,
            },
            status_code=201,
            headers=_headers(),
        )

    async def complete(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AuthorityBootstrapCompleteBody.model_validate_json(body)
        try:
            canonical_transaction = base64.b64decode(
                parsed.canonical_transaction_b64.encode("ascii"),
                validate=True,
            )
        except (UnicodeEncodeError, binascii.Error, ValueError) as exc:
            raise ValidationError("authority bootstrap transaction is not base64") from exc
        if not canonical_transaction or len(canonical_transaction) > 98_304:
            raise ValidationError("authority bootstrap transaction is outside the size bound")
        result = service.complete(
            actor=actor,
            challenge_id=request.path_params["challenge_id"],
            nonce=parsed.nonce,
            canonical_transaction=canonical_transaction,
            approval=parsed.independent_approval,
        )
        return JSONResponse(
            {
                "approval_receipt_id": result.approval_receipt_id,
                "challenge_id": result.challenge_id,
                "entitlement": result.entitlement.model_dump(mode="json"),
            },
            status_code=201,
            headers=_headers(),
        )

    return [
        Route("/v1/authority-bootstrap/challenges", begin, methods=["POST"]),
        Route(
            "/v1/authority-bootstrap/challenges/{challenge_id}/complete",
            complete,
            methods=["POST"],
        ),
    ]


__all__ = ["create_authority_bootstrap_routes"]
