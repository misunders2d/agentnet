"""Authenticated operator, authority, incident, and version HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ValidationError
from agentnet.identity.actors import TrustedTransportContext, VerifiedActor
from agentnet.operations.authority_inspection import DenialExplanationQuery
from agentnet.operations.incident import IncidentModeChange


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
AuthorityIssuer = Callable[..., IssuanceAuthority]


class VersionRolloutBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    from_protocol_version: str = Field(
        pattern=r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$"
    )
    to_protocol_version: str = Field(
        pattern=r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$"
    )
    from_schema_version: int = Field(ge=1)
    to_schema_version: int = Field(ge=1)
    compatibility_deadline: int = Field(ge=1)


class VersionRolloutAdvanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    expected_phase: str = Field(
        pattern=r"^(expanded|migrated_backfilled|verified)$"
    )
    target_phase: str = Field(
        pattern=r"^(migrated_backfilled|verified|contracted)$"
    )
    verification_digest: str | None = Field(
        default=None,
        pattern=r"^[a-f0-9]{64}$",
    )


class VersionRolloutRollbackBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    verification_digest: str = Field(pattern=r"^[a-f0-9]{64}$")


class VersionReplayBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    peer_namespace: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,127}$")
    limit: int = Field(default=100, ge=1, le=1000)


class IncidentModeChangeBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    change: IncidentModeChange
    command: SignedAuthorityCommand


def _trusted_transport(request: Request) -> TrustedTransportContext:
    """Return only transport state installed by proof authentication."""

    transport = request.scope.get("agentnet.trusted_transport")
    if not isinstance(transport, TrustedTransportContext):
        raise AuthenticationError("verified transport context is unavailable")
    return transport


def create_operator_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    issue_authority: AuthorityIssuer,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only content-free operator and authority routes."""

    async def operator_status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        core._require(
            actor=actor,
            action="operator.status.read",
            resource="operator:self",
        )
        readiness = core.readiness()
        try:
            telemetry = {"available": True, **core.telemetry.operational_snapshot()}
        except Exception:
            telemetry = {
                "available": False,
                "counters": {},
                "latency_buckets": {},
                "gauges": {},
            }
        try:
            admission_controls = {
                "available": True,
                **core.quotas.content_free_status(),
            }
        except Exception:
            admission_controls = {"available": False}
        try:
            versioning = {
                "available": True,
                **core.versioning.content_free_status(),
            }
        except Exception:
            versioning = {"available": False}
        return JSONResponse(
            {
                "status": "ready" if readiness["ready"] else "degraded",
                "profile": readiness["profile"],
                "acceptance_fact": readiness["acceptance_fact"],
                "storage_ready": bool(readiness["storage"].get("ready")),
                "artifacts_ready": bool(readiness["artifacts"].get("ready")),
                "audit_valid": bool(readiness["audit"].get("valid")),
                "deployment_binding_ready": bool(
                    readiness["deployment_binding"].get("ready")
                ),
                "a2a_ready": bool(readiness["a2a_schema"].get("ready")),
                "scanner_trust_ready": bool(
                    readiness["scanner_trust"].get("ready")
                ),
                "telemetry": telemetry,
                "admission_controls": admission_controls,
                "versioning": versioning,
            }
        )

    async def authority_inventory(request: Request) -> Response:
        if request.scope.get("query_string", b""):
            raise ValidationError(
                "authority inventory does not accept caller-selected scope"
            )
        await body_and_actor(request, core)
        inventory = core.authority_inspection.authority_inventory(
            transport=_trusted_transport(request),
        )
        return JSONResponse(
            {"authority": inventory.model_dump(mode="json")},
            headers=response_headers,
        )

    async def explain_denial(request: Request) -> Response:
        if request.scope.get("query_string", b""):
            raise ValidationError(
                "denial explanation does not accept caller-selected scope"
            )
        await body_and_actor(request, core)
        query = DenialExplanationQuery(
            decision_id=request.path_params["decision_id"]
        )
        explanation = core.authority_inspection.explain_denial(
            transport=_trusted_transport(request),
            query=query,
        )
        return JSONResponse(
            {"explanation": explanation.model_dump(mode="json")},
            headers=response_headers,
        )

    async def incident_status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        resource = f"operator-domain:{core.config.domain_id}"
        core._require(
            actor=actor,
            action="operator.incident.read",
            resource=resource,
        )
        return JSONResponse(
            {
                "incident": core.incidents.state(
                    core.config.domain_id
                ).model_dump(mode="json")
            },
            headers=response_headers,
        )

    async def set_incident_mode(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = IncidentModeChangeBody.model_validate_json(body, strict=True)
        if parsed.change.domain_id != core.config.domain_id:
            raise AuthorizationError(
                "incident change crossed the authenticated domain"
            )
        resource, _exact_request = core.incidents.authority_binding(parsed.change)
        authority = issue_authority(
            core,
            actor=actor,
            action=core.incidents.ACTION,
            resource=resource,
            request={"request_digest": parsed.command.request_digest},
        )
        state = core.incidents.set_mode(
            parsed.change,
            authority=authority,
            command=parsed.command,
        )
        return JSONResponse(
            {"incident": state.model_dump(mode="json")},
            headers=response_headers,
        )

    async def begin_version_rollout(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = VersionRolloutBeginBody.model_validate_json(body)
        return JSONResponse(
            core.begin_version_rollout(actor=actor, **parsed.model_dump()),
            status_code=201,
        )

    async def advance_version_rollout(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = VersionRolloutAdvanceBody.model_validate_json(body)
        return JSONResponse(
            core.advance_version_rollout(
                actor=actor,
                rollout_id=request.path_params["rollout_id"],
                **parsed.model_dump(),
            )
        )

    async def rollback_version_rollout(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = VersionRolloutRollbackBody.model_validate_json(body)
        return JSONResponse(
            core.rollback_version_rollout(
                actor=actor,
                rollout_id=request.path_params["rollout_id"],
                verification_digest=parsed.verification_digest,
            )
        )

    async def replay_version_events(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = VersionReplayBody.model_validate_json(body)
        return JSONResponse(
            core.replay_unsupported_events(
                actor=actor,
                **parsed.model_dump(),
            )
        )

    return [
        Route("/v1/operator/status", operator_status, methods=["GET"]),
        Route("/v1/authority", authority_inventory, methods=["GET"]),
        Route(
            "/v1/authority/denials/{decision_id}",
            explain_denial,
            methods=["GET"],
        ),
        Route("/v1/operator/incident", incident_status, methods=["GET"]),
        Route("/v1/operator/incident", set_incident_mode, methods=["POST"]),
        Route(
            "/v1/operator/version-rollouts",
            begin_version_rollout,
            methods=["POST"],
        ),
        Route(
            "/v1/operator/version-rollouts/{rollout_id}/advance",
            advance_version_rollout,
            methods=["POST"],
        ),
        Route(
            "/v1/operator/version-rollouts/{rollout_id}/rollback",
            rollback_version_rollout,
            methods=["POST"],
        ),
        Route(
            "/v1/operator/version-replay",
            replay_version_events,
            methods=["POST"],
        ),
    ]


__all__ = ["create_operator_routes"]
