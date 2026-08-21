"""Authenticated effect reservation and transition HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.grants import GrantUse
from agentnet.core.app import CommunicationCore
from agentnet.effects.reservations import (
    EffectExecutionEvidence,
    EffectReconciliationEvidence,
    EffectState,
    EffectTerminalEvidence,
    EffectTransitionProof,
    EffectUncertaintyEvidence,
)
from agentnet.identity.actors import VerifiedActor


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
WorkloadJson = Callable[[Request], Awaitable[bytes]]
AuthenticatedWorkloadActor = Callable[
    [Request, EffectTransitionProof],
    VerifiedActor,
]


class EffectReserveBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    event_id: str = Field(min_length=1, max_length=256)
    grant_use: GrantUse
    request: dict[str, Any]


class EffectStartBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proof: EffectTransitionProof
    evidence: EffectExecutionEvidence


class EffectUnknownBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proof: EffectTransitionProof
    evidence: EffectUncertaintyEvidence


class EffectTerminalBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proof: EffectTransitionProof
    terminal_state: EffectState
    evidence: EffectTerminalEvidence


class EffectReconcileBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proof: EffectTransitionProof
    evidence: EffectReconciliationEvidence


def create_effect_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    workload_json: WorkloadJson,
    authenticated_workload_actor: AuthenticatedWorkloadActor,
) -> list[Route]:
    """Mount only effect reservation, status, cancellation, and transitions."""

    async def reserve_effect(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = EffectReserveBody.model_validate_json(body)
        result = core.reserve_effect(
            actor=actor,
            event_id=parsed.event_id,
            grant_use=parsed.grant_use,
            request=parsed.request,
        )
        return JSONResponse(
            result,
            status_code=200 if result["duplicate"] else 201,
        )

    async def effect_status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        effect_id = request.path_params["effect_id"]
        core._require(actor=actor, action="effect.status", resource=effect_id)
        return JSONResponse(core.effects.status(effect_id, actor=actor))

    async def cancel_effect(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        return JSONResponse(
            core.cancel_effect(
                actor=actor,
                effect_id=request.path_params["effect_id"],
            )
        )

    async def start_effect(request: Request) -> Response:
        parsed = EffectStartBody.model_validate_json(await workload_json(request))
        actor = authenticated_workload_actor(request, parsed.proof)
        return JSONResponse(
            core.start_effect_execution(
                actor=actor,
                effect_id=request.path_params["effect_id"],
                proof=parsed.proof,
                evidence=parsed.evidence,
            )
        )

    async def mark_effect_unknown(request: Request) -> Response:
        parsed = EffectUnknownBody.model_validate_json(await workload_json(request))
        actor = authenticated_workload_actor(request, parsed.proof)
        return JSONResponse(
            core.mark_effect_unknown(
                actor=actor,
                effect_id=request.path_params["effect_id"],
                proof=parsed.proof,
                evidence=parsed.evidence,
            )
        )

    async def acknowledge_effect_terminal(request: Request) -> Response:
        parsed = EffectTerminalBody.model_validate_json(await workload_json(request))
        actor = authenticated_workload_actor(request, parsed.proof)
        return JSONResponse(
            core.acknowledge_effect_terminal(
                actor=actor,
                effect_id=request.path_params["effect_id"],
                proof=parsed.proof,
                terminal_state=parsed.terminal_state,
                evidence=parsed.evidence,
            )
        )

    async def reconcile_effect(request: Request) -> Response:
        parsed = EffectReconcileBody.model_validate_json(await workload_json(request))
        actor = authenticated_workload_actor(request, parsed.proof)
        return JSONResponse(
            core.reconcile_effect(
                actor=actor,
                effect_id=request.path_params["effect_id"],
                proof=parsed.proof,
                evidence=parsed.evidence,
            )
        )

    return [
        Route("/v1/effects", reserve_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}", effect_status, methods=["GET"]),
        Route("/v1/effects/{effect_id}/cancel", cancel_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}/start", start_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}/unknown", mark_effect_unknown, methods=["POST"]),
        Route(
            "/v1/effects/{effect_id}/terminal",
            acknowledge_effect_terminal,
            methods=["POST"],
        ),
        Route(
            "/v1/effects/{effect_id}/reconcile",
            reconcile_effect,
            methods=["POST"],
        ),
    ]


__all__ = ["create_effect_routes"]
