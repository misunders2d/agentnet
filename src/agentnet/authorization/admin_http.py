"""Authenticated entitlement and temporary-elevation administration routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.elevation import ElevationRequest, ElevationService
from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.policy import HumanEntitlement
from agentnet.core.app import CommunicationCore
from agentnet.identity.actors import VerifiedActor
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
DecisionIssuer = Callable[..., IssuanceAuthority]


class EntitlementIssueBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    entitlement: HumanEntitlement
    command: SignedAuthorityCommand


class EntitlementRevokeBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    command: SignedAuthorityCommand


class ElevationIssueBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    request: ElevationRequest
    independent_approvals: tuple[dict[str, Any], ...] = Field(
        min_length=1,
        max_length=5,
    )


def create_authority_admin_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    elevations: ElevationService,
    issue_decision: DecisionIssuer,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only entitlement and elevation administration routes."""

    async def issue_entitlement(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = EntitlementIssueBody.model_validate_json(body)
        resource, mutation = core.policy.entitlement_issuance_binding(
            parsed.entitlement,
            reason=parsed.command.reason,
        )
        authority = issue_decision(
            core,
            actor=actor,
            action="authorization.entitlement.issue",
            resource=resource,
            request_digest=canonical_digest(mutation),
        )
        result = core.policy.add_entitlement(
            parsed.entitlement,
            command=parsed.command,
            authority=authority,
        )
        return JSONResponse(
            result.model_dump(mode="json"),
            status_code=201,
            headers=response_headers,
        )

    async def revoke_entitlement(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = EntitlementRevokeBody.model_validate_json(body)
        entitlement_id = request.path_params["entitlement_id"]
        resource, mutation = core.policy.entitlement_revocation_binding(
            entitlement_id,
            expected_entity_revision=parsed.command.expected_entity_revision,
            reason=parsed.command.reason,
        )
        authority = issue_decision(
            core,
            actor=actor,
            action="authorization.entitlement.revoke",
            resource=resource,
            request_digest=canonical_digest(mutation),
        )
        core.policy.revoke_entitlement(
            entitlement_id,
            command=parsed.command,
            authority=authority,
        )
        return JSONResponse(
            {"entitlement_id": entitlement_id, "revoked": True},
            headers=response_headers,
        )

    async def issue_elevation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ElevationIssueBody.model_validate_json(body)
        resource, mutation = elevations.authority_binding(parsed.request)
        authority = issue_decision(
            core,
            actor=actor,
            action="authorization.elevation.request",
            resource=resource,
            request_digest=mutation["request_digest"],
        )
        result = elevations.issue(
            parsed.request,
            beneficiary=actor,
            authority=authority,
            approvals=parsed.independent_approvals,
        )
        return JSONResponse(
            result.model_dump(mode="json"),
            status_code=201,
            headers=response_headers,
        )

    return [
        Route("/v1/admin/entitlements", issue_entitlement, methods=["POST"]),
        Route(
            "/v1/admin/entitlements/{entitlement_id}/revoke",
            revoke_entitlement,
            methods=["POST"],
        ),
        Route("/v1/admin/elevations", issue_elevation, methods=["POST"]),
    ]


__all__ = ["create_authority_admin_routes"]
