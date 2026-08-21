"""Authenticated automation-charter and invocation HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval import IndependentApprovalReceipt
from agentnet.automation import (
    AutomationCharter,
    AutomationInvocation,
    AutomationInvocationCompletion,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import VerifiedActor


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
AuthorityIssuer = Callable[..., IssuanceAuthority]
WorkloadJson = Callable[[Request], Awaitable[bytes]]
WorkloadActorResolver = Callable[[Request, str], VerifiedActor]


class AutomationCharterProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    charter: AutomationCharter


class AutomationCharterActivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    expected_charter_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_revision: int = Field(ge=1)
    approvals: tuple[IndependentApprovalReceipt, ...] = Field(min_length=1, max_length=5)


class AutomationCharterStopBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    expected_charter_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_revision: int = Field(ge=1)
    reason: str = Field(min_length=1, max_length=1024)


class AutomationInvocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    invocation: AutomationInvocation


class AutomationInvocationCompletionBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    completion: AutomationInvocationCompletion


def create_automation_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    issue_authority: AuthorityIssuer,
    workload_json: WorkloadJson,
    resolve_workload_actor: WorkloadActorResolver,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only automation-charter and invocation routes."""

    async def propose_automation_charter(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterProposalBody.model_validate_json(body, strict=True)
        resource, exact_request = core.automation.authority_binding(parsed.charter)
        authority = issue_authority(
            core,
            actor=actor,
            action="automation.charter.propose",
            resource=resource,
            request=exact_request,
        )
        record = core.automation.propose(parsed.charter, authority=authority)
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            status_code=201,
            headers=response_headers,
        )

    async def activate_automation_charter(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterActivationBody.model_validate_json(body, strict=True)
        record = core.automation.activate(
            actor=actor,
            charter_id=request.path_params["charter_id"],
            expected_charter_digest=parsed.expected_charter_digest,
            expected_revision=parsed.expected_revision,
            approvals=parsed.approvals,
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=response_headers,
        )

    async def list_automation_charters(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        raw_limit = request.query_params.get("limit", "100")
        if not raw_limit.isascii() or not raw_limit.isdigit():
            raise ValidationError("automation charter list limit is invalid")
        records = core.automation.list_for_owner(actor=actor, limit=int(raw_limit))
        return JSONResponse(
            {"charters": [record.model_dump(mode="json") for record in records]},
            headers=response_headers,
        )

    async def get_automation_charter(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        record = core.automation.get_for_owner(
            actor=actor,
            charter_id=request.path_params["charter_id"],
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=response_headers,
        )

    async def stop_automation_charter(
        request: Request,
        *,
        emergency: bool,
    ) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterStopBody.model_validate_json(body, strict=True)
        charter_id = request.path_params["charter_id"]
        resource, exact_request = core.automation.mutation_binding(
            charter_id=charter_id,
            expected_revision=parsed.expected_revision,
            expected_charter_digest=parsed.expected_charter_digest,
            reason=parsed.reason,
            emergency=emergency,
        )
        action = (
            "automation.charter.emergency_stop"
            if emergency
            else "automation.charter.revoke"
        )
        authority = issue_authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        record = core.automation.stop(
            authority=authority,
            charter_id=charter_id,
            expected_revision=parsed.expected_revision,
            expected_charter_digest=parsed.expected_charter_digest,
            reason=parsed.reason,
            emergency=emergency,
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=response_headers,
        )

    async def revoke_automation_charter(request: Request) -> Response:
        return await stop_automation_charter(request, emergency=False)

    async def emergency_stop_automation_charter(request: Request) -> Response:
        return await stop_automation_charter(request, emergency=True)

    async def reserve_automation_invocation(request: Request) -> Response:
        parsed = AutomationInvocationBody.model_validate_json(
            await workload_json(request),
            strict=True,
        )
        charter_id = request.path_params["charter_id"]
        if parsed.invocation.charter_id != charter_id:
            raise AuthorizationError("automation invocation charter is unavailable")
        actor = resolve_workload_actor(
            request,
            parsed.invocation.workload_registration_id,
        )
        reservation = core.automation.reserve_invocation(
            actor=actor,
            invocation=parsed.invocation,
        )
        return JSONResponse(
            {"reservation": reservation.model_dump(mode="json")},
            status_code=200 if reservation.duplicate else 201,
            headers=response_headers,
        )

    async def finish_automation_invocation(request: Request) -> Response:
        parsed = AutomationInvocationCompletionBody.model_validate_json(
            await workload_json(request),
            strict=True,
        )
        charter_id = request.path_params["charter_id"]
        invocation_id = request.path_params["invocation_id"]
        if (
            parsed.completion.charter_id != charter_id
            or parsed.completion.invocation_id != invocation_id
        ):
            raise AuthorizationError("automation completion is unavailable")
        actor = resolve_workload_actor(
            request,
            parsed.completion.workload_registration_id,
        )
        reservation = core.automation.finish_invocation(
            actor=actor,
            completion=parsed.completion,
        )
        return JSONResponse(
            {"reservation": reservation.model_dump(mode="json")},
            headers=response_headers,
        )

    return [
        Route("/v1/automation-charters", propose_automation_charter, methods=["POST"]),
        Route("/v1/automation-charters", list_automation_charters, methods=["GET"]),
        Route(
            "/v1/automation-charters/{charter_id}",
            get_automation_charter,
            methods=["GET"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/activate",
            activate_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/revoke",
            revoke_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/emergency-stop",
            emergency_stop_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/invocations",
            reserve_automation_invocation,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/invocations/{invocation_id}/terminal",
            finish_automation_invocation,
            methods=["POST"],
        ),
    ]


__all__ = ["create_automation_routes"]
