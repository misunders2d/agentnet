"""Authenticated relationship and task-conflict HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping
from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval import IndependentApprovalReceipt
from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.organization import (
    RelationshipPolicyException,
    TaskConflictAdjudication,
)
from agentnet.protocol.models import Relationship


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
AuthorityIssuer = Callable[..., IssuanceAuthority]


class RelationshipProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    relationship: Relationship
    proposal_expires_at: datetime


class RelationshipAcceptanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    approval: IndependentApprovalReceipt
    expected_transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_relationship_revision: int = Field(ge=1)
    expected_lifecycle_revision: int = Field(ge=1)


class RelationshipPolicyExceptionRecordBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    exception: RelationshipPolicyException
    command: SignedAuthorityCommand


class RelationshipPolicyExceptionActivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    policy_exception_id: str = Field(min_length=1, max_length=128)
    expected_transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_relationship_revision: int = Field(ge=1)
    expected_lifecycle_revision: int = Field(ge=1)


class RelationshipAuthorityBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    command: SignedAuthorityCommand


class TaskConflictAdjudicationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    decision: TaskConflictAdjudication


def create_organization_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    issue_authority: AuthorityIssuer,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only relationship lifecycle and task-conflict routes."""

    async def propose_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipProposalBody.model_validate_json(body, strict=True)
        proposal = core.propose_relationship(
            actor=actor,
            relationship=parsed.relationship,
            proposal_expires_at=parsed.proposal_expires_at,
        )
        return JSONResponse(
            {"proposal": proposal.model_dump(mode="json")},
            status_code=201,
            headers=response_headers,
        )

    async def accept_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipAcceptanceBody.model_validate_json(body, strict=True)
        relationship = core.accept_relationship(
            actor=actor,
            relationship_id=request.path_params["relationship_id"],
            approval=parsed.approval.model_dump(mode="json"),
            expected_transaction_digest=parsed.expected_transaction_digest,
            expected_relationship_revision=parsed.expected_relationship_revision,
            expected_lifecycle_revision=parsed.expected_lifecycle_revision,
        )
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=response_headers,
        )

    async def record_relationship_policy_exception(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipPolicyExceptionRecordBody.model_validate_json(
            body,
            strict=True,
        )
        relationship_id = request.path_params["relationship_id"]
        if parsed.exception.relationship_id != relationship_id:
            raise AuthorizationError(
                "relationship policy exception is not visible"
            )
        policy_exception = core.record_relationship_policy_exception(
            actor=actor,
            exception=parsed.exception,
            command=parsed.command,
        )
        return JSONResponse(
            {"policy_exception": policy_exception.model_dump(mode="json")},
            status_code=201,
            headers=response_headers,
        )

    async def activate_relationship_policy_exception(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipPolicyExceptionActivationBody.model_validate_json(
            body,
            strict=True,
        )
        relationship = core.activate_relationship_policy_exception(
            actor=actor,
            relationship_id=request.path_params["relationship_id"],
            policy_exception_id=parsed.policy_exception_id,
            expected_transaction_digest=parsed.expected_transaction_digest,
            expected_relationship_revision=parsed.expected_relationship_revision,
            expected_lifecycle_revision=parsed.expected_lifecycle_revision,
        )
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=response_headers,
        )

    async def get_relationship(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        relationship_id = request.path_params["relationship_id"]
        administrative = request.query_params.get("administrative", "false")
        if administrative not in {"true", "false"}:
            raise ValidationError("administrative must be true or false")
        action = (
            "organization.relationship.admin_read"
            if administrative == "true"
            else "organization.relationship.read"
        )
        resource, exact_request = core.relationships.read_binding(relationship_id)
        authority = issue_authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        relationship = core.relationships.get(
            relationship_id,
            authority=authority,
            administrative=administrative == "true",
        )
        if relationship is None:
            raise AuthorizationError("relationship is not visible")
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=response_headers,
        )

    async def revoke_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipAuthorityBody.model_validate_json(body, strict=True)
        relationship_id = request.path_params["relationship_id"]
        core.revoke_relationship(
            actor=actor,
            relationship_id=relationship_id,
            command=parsed.command,
        )
        return JSONResponse(
            {"relationship_id": relationship_id, "revoked": True},
            headers=response_headers,
        )

    async def list_task_conflicts(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        return JSONResponse(
            {
                "conflicts": core.assignments.pending_conflicts_for_owner(
                    actor=actor,
                    limit=100,
                )
            },
            headers=response_headers,
        )

    async def adjudicate_task_conflict(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TaskConflictAdjudicationBody.model_validate_json(body)
        conflict_id = request.path_params["conflict_id"]
        if parsed.decision.conflict_id != conflict_id:
            raise AuthorizationError("task conflict decision is unavailable")
        outcome = core.assignments.adjudicate_conflict(
            actor=actor,
            decision=parsed.decision,
        )
        return JSONResponse(
            {"conflict": outcome.model_dump(mode="json")},
            headers=response_headers,
        )

    return [
        Route("/v1/relationships", propose_relationship, methods=["POST"]),
        Route(
            "/v1/relationships/{relationship_id}/accept",
            accept_relationship,
            methods=["POST"],
        ),
        Route(
            "/v1/relationships/{relationship_id}/policy-exceptions",
            record_relationship_policy_exception,
            methods=["POST"],
        ),
        Route(
            "/v1/relationships/{relationship_id}/policy-exceptions/activate",
            activate_relationship_policy_exception,
            methods=["POST"],
        ),
        Route(
            "/v1/relationships/{relationship_id}",
            get_relationship,
            methods=["GET"],
        ),
        Route(
            "/v1/relationships/{relationship_id}/revoke",
            revoke_relationship,
            methods=["POST"],
        ),
        Route("/v1/task-conflicts", list_task_conflicts, methods=["GET"]),
        Route(
            "/v1/task-conflicts/{conflict_id}/adjudicate",
            adjudicate_task_conflict,
            methods=["POST"],
        ),
    ]


__all__ = ["create_organization_routes"]
