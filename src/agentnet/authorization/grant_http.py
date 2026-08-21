"""Authenticated task-grant HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable

from pydantic import BaseModel, ConfigDict
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import TaskGrant


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
AuthorityIssuer = Callable[..., IssuanceAuthority]


class TaskGrantIssueBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    grant: TaskGrant


class TaskGrantAuthorityBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    command: SignedAuthorityCommand


def create_task_grant_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    issue_authority: AuthorityIssuer,
) -> list[Route]:
    """Mount only task-grant issue, read, and revocation routes."""

    async def issue_task_grant(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TaskGrantIssueBody.model_validate_json(body)
        issued = core.issue_task_grant(actor=actor, grant=parsed.grant)
        return JSONResponse(
            {"grant": issued.model_dump(mode="json")},
            status_code=201,
        )

    async def get_task_grant(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        grant_id = request.path_params["grant_id"]
        administrative = request.query_params.get("administrative", "false")
        if administrative not in {"true", "false"}:
            raise ValidationError("administrative must be true or false")
        action = (
            "authorization.task_grant.admin_read"
            if administrative == "true"
            else "authorization.task_grant.read"
        )
        resource, exact_request = core.grants.read_binding(grant_id)
        authority = issue_authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        grant = core.grants.get(
            grant_id,
            authority=authority,
            administrative=administrative == "true",
        )
        if grant is None:
            raise AuthorizationError("task grant is not visible")
        return JSONResponse({"grant": grant.model_dump(mode="json")})

    async def revoke_task_grant(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TaskGrantAuthorityBody.model_validate_json(body)
        grant_id = request.path_params["grant_id"]
        if parsed.command.resource != f"task-grant:{grant_id}":
            raise AuthorizationError("task grant authority binding mismatch")
        authority = issue_authority(
            core,
            actor=actor,
            action=parsed.command.action,
            resource=parsed.command.resource,
            request={"request_digest": parsed.command.request_digest},
        )
        core.grants.revoke(
            grant_id,
            command=parsed.command,
            authority=authority,
        )
        return JSONResponse({"grant_id": grant_id, "revoked": True})

    return [
        Route("/v1/task-grants", issue_task_grant, methods=["POST"]),
        Route("/v1/task-grants/{grant_id}", get_task_grant, methods=["GET"]),
        Route(
            "/v1/task-grants/{grant_id}/revoke",
            revoke_task_grant,
            methods=["POST"],
        ),
    ]


__all__ = ["create_task_grant_routes"]
