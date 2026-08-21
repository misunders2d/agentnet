"""Authenticated presence and directory HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.core.app import CommunicationCore
from agentnet.discovery.directory import DirectoryRecord
from agentnet.errors import ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import PresenceLease
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]


class PresenceUpdateBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    lease: PresenceLease
    signature: str = Field(min_length=1, max_length=2048)


class DirectoryPublishBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    record: DirectoryRecord


def create_discovery_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
) -> list[Route]:
    """Mount only presence and bounded-directory routes."""

    async def update_presence(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = PresenceUpdateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="presence.update",
            resource=parsed.lease.harness_id,
            context={
                "lease_digest": canonical_digest(parsed.lease.model_dump(mode="json"))
            },
        )
        core.presence.update(parsed.lease, actor=actor, signature=parsed.signature)
        return JSONResponse({"harness_id": parsed.lease.harness_id, "state": "live"})

    async def presence_state(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        harness_id = request.path_params["harness_id"]
        try:
            recent_window = int(request.query_params.get("recent_window", "300"))
        except ValueError as exc:
            raise ValidationError("recent_window must be an integer") from exc
        core._require(actor=actor, action="presence.read", resource=harness_id)
        state = core.presence.state_for(
            actor=actor,
            harness_id=harness_id,
            recent_window_seconds=recent_window,
        )
        return JSONResponse({"harness_id": harness_id, "state": state})

    async def directory_record(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        record_id = request.path_params["record_id"]
        core._require(actor=actor, action="directory.read", resource=record_id)
        record = core.directory.get_record(actor, record_id)
        return JSONResponse({"record": record.model_dump(mode="json")})

    async def publish_directory_record(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = DirectoryPublishBody.model_validate_json(body)
        resource, exact_request = core.directory.publication_binding(parsed.record)
        decision = core._require(
            actor=actor,
            action="directory.publish",
            resource=resource,
            context=exact_request,
        )
        result = core.directory.publish(
            parsed.record,
            authority=IssuanceAuthority(
                actor=actor,
                policy_decision_id=decision.decision_id,
            ),
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def directory_records(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        try:
            limit = int(request.query_params.get("limit", "100"))
        except ValueError as exc:
            raise ValidationError("directory limit must be an integer") from exc
        types_text = request.query_params.get("types")
        record_types = None if types_text is None else frozenset(types_text.split(","))
        if not 1 <= limit <= 100:
            raise ValidationError("directory limit is outside the bounded range")
        if record_types is not None and (
            not record_types
            or not record_types.issubset({"agent", "room", "domain", "endpoint"})
        ):
            raise ValidationError("directory record type filter is invalid")
        core._require(
            actor=actor,
            action="directory.list",
            resource="directory:self",
            context={
                "limit": limit,
                "record_types": sorted(record_types) if record_types else None,
            },
        )
        records = core.directory.list_records(
            actor,
            record_types=record_types,
            limit=limit,
        )
        return JSONResponse(
            {"records": [record.model_dump(mode="json") for record in records]}
        )

    return [
        Route("/v1/presence", update_presence, methods=["POST"]),
        Route("/v1/presence/{harness_id}", presence_state, methods=["GET"]),
        Route("/v1/directory", directory_records, methods=["GET"]),
        Route("/v1/directory", publish_directory_record, methods=["POST"]),
        Route("/v1/directory/{record_id}", directory_record, methods=["GET"]),
    ]


__all__ = ["create_discovery_routes"]
