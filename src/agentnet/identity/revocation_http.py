"""Authenticated harness-revocation preparation and commit routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping
from dataclasses import asdict
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.revocation import HarnessRevocationRequest, HarnessRevocationService
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
DecisionIssuer = Callable[..., IssuanceAuthority]


class HarnessRevocationPrepareBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    harness_id: str = Field(min_length=1, max_length=256)
    reason: str = Field(min_length=1, max_length=512)


class HarnessRevocationCommitBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    request: HarnessRevocationRequest
    independent_approval: dict[str, Any]


def create_harness_revocation_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    revocations: HarnessRevocationService,
    issue_decision: DecisionIssuer,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only harness-revocation preparation and commit routes."""

    async def prepare_harness_revocation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = HarnessRevocationPrepareBody.model_validate_json(body)
        # Gate the named target before reading it so this route cannot become
        # an authenticated harness-existence oracle.
        issue_decision(
            core,
            actor=actor,
            action="identity.harness.revoke",
            resource=f"harness:{parsed.harness_id}",
            request_digest=canonical_digest(
                {
                    "domain_id": core.config.domain_id,
                    "harness_id": parsed.harness_id,
                    "reason": parsed.reason,
                }
            ),
        )
        prepared = revocations.prepare(
            domain_id=core.config.domain_id,
            harness_id=parsed.harness_id,
            reason=parsed.reason,
        )
        return JSONResponse(
            prepared.model_dump(mode="json"),
            status_code=201,
            headers=response_headers,
        )

    async def commit_harness_revocation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = HarnessRevocationCommitBody.model_validate_json(body)
        if parsed.request.domain_id != core.config.domain_id:
            raise AuthenticationError("harness revocation domain binding mismatch")
        resource, mutation = revocations.authority_binding(parsed.request)
        authority = issue_decision(
            core,
            actor=actor,
            action="identity.harness.revoke",
            resource=resource,
            request_digest=mutation["request_digest"],
        )
        result = revocations.revoke(
            request=parsed.request,
            authority=authority,
            approval=parsed.independent_approval,
        )
        return JSONResponse(asdict(result), headers=response_headers)

    return [
        Route(
            "/v1/admin/harness-revocations/prepare",
            prepare_harness_revocation,
            methods=["POST"],
        ),
        Route(
            "/v1/admin/harness-revocations/commit",
            commit_harness_revocation,
            methods=["POST"],
        ),
    ]


__all__ = ["create_harness_revocation_routes"]
