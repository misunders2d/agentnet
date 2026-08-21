"""Authenticated provenance registration and read HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping

from pydantic import BaseModel, ConfigDict
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceDerivation,
    ProvenanceObjectType,
)
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]


class ProvenanceOriginBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    registration: OriginRegistration


class ProvenanceDerivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    derivation: ProvenanceDerivation


def create_provenance_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount only provenance registration, derivation, and read routes."""

    async def register_provenance_origin(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ProvenanceOriginBody.model_validate_json(body, strict=True)
        registration = parsed.registration
        if registration.domain_id != actor.domain_id:
            raise AuthorizationError(
                "provenance origin crossed the authenticated domain"
            )
        if registration.origin.kind is not OriginKind.HUMAN_INPUT:
            raise AuthorizationError(
                "non-human provenance origins require a composed server service"
            )
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or registration.origin.principal_id != actor.principal_id
            or registration.origin.harness_id != actor.harness_id
        ):
            raise AuthorizationError(
                "human provenance origin is not the authenticated human harness"
            )
        resource = (
            f"provenance:{registration.object_type.value}:{registration.object_id}"
        )
        core._require(
            actor=actor,
            action="provenance.origin.register",
            resource=resource,
            classification=registration.classification,
            context={
                "registration_digest": canonical_digest(
                    registration.model_dump(mode="json")
                )
            },
        )
        record = core.provenance.register_origin(registration)
        return JSONResponse(
            {"provenance": record.model_dump(mode="json")},
            status_code=201,
            headers=response_headers,
        )

    async def derive_provenance(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ProvenanceDerivationBody.model_validate_json(body, strict=True)
        derivation = parsed.derivation
        if derivation.domain_id != actor.domain_id:
            raise AuthorizationError(
                "derived provenance crossed the authenticated domain"
            )
        if actor.harness_id is None or any(
            step.executor_harness_id != actor.harness_id
            for step in derivation.transformations
        ):
            raise AuthorizationError(
                "provenance transformation executor is not the authenticated harness"
            )
        resource = f"provenance:{derivation.object_type.value}:{derivation.object_id}"
        core._require(
            actor=actor,
            action="provenance.derive",
            resource=resource,
            classification=derivation.classification,
            context={
                "derivation_digest": canonical_digest(
                    derivation.model_dump(mode="json")
                )
            },
        )
        record = core.provenance.derive(derivation)
        return JSONResponse(
            {"provenance": record.model_dump(mode="json")},
            status_code=201,
            headers=response_headers,
        )

    async def provenance_versions(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        try:
            object_type = ProvenanceObjectType(request.path_params["object_type"])
        except ValueError as exc:
            raise ValidationError("provenance object type is invalid") from exc
        object_id = request.path_params["object_id"]
        resource = f"provenance:{object_type.value}:{object_id}"
        core._require(
            actor=actor,
            action="provenance.read",
            resource=resource,
            context={"object_type": object_type.value, "object_id": object_id},
        )
        records = core.provenance.versions(
            object_type=object_type,
            object_id=object_id,
        )
        return JSONResponse(
            {"versions": [record.model_dump(mode="json") for record in records]},
            headers=response_headers,
        )

    async def provenance_version(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        try:
            object_type = ProvenanceObjectType(request.path_params["object_type"])
        except ValueError as exc:
            raise ValidationError("provenance object type is invalid") from exc
        raw_version = request.path_params["version"]
        if (
            not raw_version.isascii()
            or not raw_version.isdigit()
            or int(raw_version) < 1
        ):
            raise ValidationError("provenance version is invalid")
        object_id = request.path_params["object_id"]
        resource = f"provenance:{object_type.value}:{object_id}"
        core._require(
            actor=actor,
            action="provenance.read",
            resource=resource,
            context={
                "object_type": object_type.value,
                "object_id": object_id,
                "version": int(raw_version),
            },
        )
        record = core.provenance.get_version(
            object_type=object_type,
            object_id=object_id,
            version=int(raw_version),
        )
        return JSONResponse(
            {"provenance": record.model_dump(mode="json")},
            headers=response_headers,
        )

    return [
        Route("/v1/provenance/origins", register_provenance_origin, methods=["POST"]),
        Route("/v1/provenance/derivations", derive_provenance, methods=["POST"]),
        Route(
            "/v1/provenance/{object_type}/{object_id}",
            provenance_versions,
            methods=["GET"],
        ),
        Route(
            "/v1/provenance/{object_type}/{object_id}/{version}",
            provenance_version,
            methods=["GET"],
        ),
    ]


__all__ = ["create_provenance_routes"]
