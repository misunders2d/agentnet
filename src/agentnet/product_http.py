"""Authenticated routes composing the ordinary extension's domain services.

These handlers deliberately mint policy decisions from the transport-resolved
actor.  Policy-decision identifiers are never accepted from request JSON.
Administrative revocations additionally require the service's exact signed,
versioned authority command.
"""

from __future__ import annotations

import base64
import binascii
from collections.abc import Awaitable, Callable
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.grant_http import create_task_grant_routes
from agentnet.automation_http import create_automation_routes
from agentnet.artifacts.scanner import ArtifactDerivationV1
from agentnet.core.app import CommunicationCore
from agentnet.discovery.http import create_discovery_routes
from agentnet.effects.http import create_effect_routes
from agentnet.effects.reservations import EffectTransitionProof
from agentnet.errors import AuthenticationError, AuthorizationError, ValidationError
from agentnet.identity.actors import TrustedTransportContext, VerifiedActor
from agentnet.identity.credential_http import (
    ExpiredBodyAndContext,
    create_credential_routes,
)
from agentnet.identity.workload import AuthenticatedSPIFFETransport
from agentnet.operations.incident import IncidentModeChange
from agentnet.operations.authority_inspection import DenialExplanationQuery
from agentnet.organization.http import create_organization_routes
from agentnet.protocol.models import Classification
from agentnet.provenance_http import create_provenance_routes
from agentnet.rooms.http import create_room_routes
from agentnet.security.signatures import canonical_digest, canonical_json


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, VerifiedActor]]]

RELATIONSHIP_RESPONSE_HEADERS = {
    "Cache-Control": "no-store",
    "Pragma": "no-cache",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
}


class ArtifactReserveBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    idempotency_key: str = Field(min_length=16, max_length=256)
    expected_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    expected_size: int = Field(ge=0, le=16_777_216)
    media_type: str = Field(min_length=1, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    required_attachment: bool = True
    ttl_seconds: int = Field(default=3600, ge=30, le=86_400)


class ArtifactUploadBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    content_b64: str = Field(max_length=22_369_624)


class ArtifactPromoteBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    object_version: str = Field(pattern=r"^[a-f0-9]{64}$")
    provenance: dict[str, Any]
    derivation: ArtifactDerivationV1 | None = None


class ArtifactScanBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    attestation: dict[str, Any]


class ArtifactDownloadCapabilityBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    ttl_seconds: int = Field(default=60, ge=1, le=300)


class ArtifactDownloadBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    token: str = Field(min_length=32, max_length=512)


class ArtifactLifecycleMutationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    expected_revision: int = Field(ge=1)
    reason: str = Field(min_length=1, max_length=512)


class VersionRolloutBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    from_protocol_version: str = Field(pattern=r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
    to_protocol_version: str = Field(pattern=r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
    from_schema_version: int = Field(ge=1)
    to_schema_version: int = Field(ge=1)
    compatibility_deadline: int = Field(ge=1)


class VersionRolloutAdvanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    expected_phase: str = Field(pattern=r"^(expanded|migrated_backfilled|verified)$")
    target_phase: str = Field(pattern=r"^(migrated_backfilled|verified|contracted)$")
    verification_digest: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


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
    """Return only transport state installed by the proof-authentication seam."""

    transport = request.scope.get("agentnet.trusted_transport")
    if not isinstance(transport, TrustedTransportContext):
        raise AuthenticationError("verified transport context is unavailable")
    return transport


def _decode_b64(value: str, *, field: str) -> bytes:
    try:
        return base64.b64decode(value.encode("ascii"), validate=True)
    except (UnicodeEncodeError, binascii.Error, ValueError) as exc:
        raise ValidationError(f"{field} is not canonical base64") from exc


def _authority(core: CommunicationCore, *, actor: VerifiedActor, action: str, resource: str, request: dict[str, Any]) -> IssuanceAuthority:
    decision = core._require(actor=actor, action=action, resource=resource, context=request)
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def create_product_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    expired_body_and_context: ExpiredBodyAndContext,
) -> list[Route]:
    async def workload_json(request: Request) -> bytes:
        """Bound a proof-authenticated workload request without a human key."""

        content_type = request.headers.get("content-type", "").split(";", 1)[0].strip().casefold()
        if content_type != "application/json":
            raise ValidationError("workload lifecycle requests require application/json")
        body = await request.body()
        if not body or len(body) > core.config.max_request_bytes:
            raise ValidationError("workload lifecycle body is empty or exceeds the configured limit")
        peer = "unavailable" if request.client is None else request.client.host
        core.quotas.consume(
            scope=f"effect-workload:{peer}",
            metric="effect_transition_attempts",
            amount=1,
            limit=core.config.policies.operations.per_actor_requests_per_minute,
        )
        return body

    def resolved_workload_actor(request: Request, registration_id: str) -> VerifiedActor:
        """Resolve a worker only from a hosting transport's verified scope.

        Client-controlled HTTP headers and JSON never create workload mTLS
        assurance.  A production TLS/IPC terminator must populate this private
        ASGI scope value after verifying the SPIFFE SVID and local process
        session; ordinary Internet requests therefore fail closed.
        """

        boundary = request.scope.get("agentnet.workload_transport")
        if not isinstance(boundary, AuthenticatedSPIFFETransport):
            raise AuthenticationError("workload lifecycle requires a verified workload transport")
        return core.workloads.resolve(
            transport=boundary,
            registration_id=registration_id,
        )

    def authenticated_workload_actor(
        request: Request,
        proof: EffectTransitionProof,
    ) -> VerifiedActor:
        actor = resolved_workload_actor(request, proof.registration_id)
        proof_actor = {
            "registration_id": proof.registration_id,
            "workload_id": proof.workload_id,
            "workload_role": proof.workload_role,
            "process_id": proof.process_id,
            "process_start_time": proof.process_start_time,
            "session_id": proof.session_id,
            "credential_epoch": proof.credential_epoch,
            "revocation_epoch": proof.revocation_epoch,
            "parent_event_id": proof.parent_event_id,
            "task_grant_id": proof.task_grant_id,
        }
        expected = {
            "registration_id": actor.workload_registration_id,
            "workload_id": actor.workload_id,
            "workload_role": actor.workload_role,
            "process_id": actor.workload_process_id,
            "process_start_time": actor.workload_process_start_time,
            "session_id": actor.workload_session_id,
            "credential_epoch": actor.credential_epoch,
            "revocation_epoch": actor.workload_revocation_epoch,
            "parent_event_id": actor.parent_event_id,
            "task_grant_id": actor.task_grant_id,
        }
        if proof_actor != expected:
            raise AuthenticationError("effect proof and verified workload transport disagree")
        return actor

    async def reserve_artifact(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactReserveBody.model_validate_json(body)
        exact = {
            "actor": actor.audit_view(),
            "classification": parsed.classification.value,
            "expected_digest": parsed.expected_digest,
            "expected_size": parsed.expected_size,
            "media_type": parsed.media_type,
            "required_attachment": parsed.required_attachment,
        }
        decision = core._require(
            actor=actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            classification=parsed.classification,
            context=exact,
        )
        result = core.artifacts.reserve(
            actor=actor,
            idempotency_key=parsed.idempotency_key,
            expected_digest=parsed.expected_digest,
            expected_size=parsed.expected_size,
            media_type=parsed.media_type,
            classification=parsed.classification,
            required_attachment=parsed.required_attachment,
            policy_decision_id=decision.decision_id,
            ttl_seconds=parsed.ttl_seconds,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def upload_artifact(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        content_type = request.headers.get("content-type", "").split(";", 1)[0].strip().casefold()
        if content_type == "application/octet-stream":
            content = body
        elif content_type == "application/json":
            parsed = ArtifactUploadBody.model_validate_json(body)
            content = _decode_b64(parsed.content_b64, field="content_b64")
        else:
            raise ValidationError("artifact upload requires application/octet-stream or application/json")
        reservation_id = request.path_params["reservation_id"]
        row = core.store.fetch_one(
            """SELECT expected_digest,expected_size FROM artifact_reservations
                WHERE reservation_id=? AND actor_json=?""",
            (reservation_id, canonical_json(actor.audit_view()).decode("utf-8")),
        )
        if row is None:
            raise AuthorizationError("artifact reservation is not visible")
        exact = {"expected_digest": row["expected_digest"], "expected_size": int(row["expected_size"])}
        decision = core._require(
            actor=actor,
            action="artifact.upload.bytes",
            resource=reservation_id,
            context=exact,
        )
        result = core.artifacts.upload(
            reservation_id,
            content,
            actor=actor,
            policy_decision_id=decision.decision_id,
        )
        return JSONResponse(result)

    async def abort_artifact_reservation(request: Request) -> Response:
        core.artifacts.require_enabled()
        _body, actor = await body_and_actor(request, core)
        reservation_id = request.path_params["reservation_id"]
        row = core.store.fetch_one(
            """SELECT request_digest FROM artifact_reservations
                WHERE reservation_id=? AND actor_json=?""",
            (reservation_id, canonical_json(actor.audit_view()).decode("utf-8")),
        )
        if row is None:
            raise AuthorizationError("artifact reservation is not visible")
        exact = {"request_digest": row["request_digest"]}
        decision = core._require(
            actor=actor,
            action="artifact.upload.abort",
            resource=reservation_id,
            context=exact,
        )
        return JSONResponse(
            core.artifacts.abort_reservation(
                reservation_id,
                actor=actor,
                policy_decision_id=decision.decision_id,
            )
        )

    async def promote_artifact(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactPromoteBody.model_validate_json(body)
        reservation_id = request.path_params["reservation_id"]
        row = core.store.fetch_one(
            """SELECT request_digest FROM artifact_reservations
                WHERE reservation_id=? AND actor_json=?""",
            (reservation_id, canonical_json(actor.audit_view()).decode("utf-8")),
        )
        if row is None:
            raise AuthorizationError("artifact reservation is not visible")
        exact = {"object_version": parsed.object_version, "request_digest": row["request_digest"]}
        if parsed.derivation is not None:
            exact["derivation_digest"] = canonical_digest(
                parsed.derivation.model_dump(mode="json")
            )
        decision = core._require(
            actor=actor,
            action="artifact.manifest.promote",
            resource=reservation_id,
            context=exact,
        )
        result = core.artifacts.promote_manifest(
            reservation_id=reservation_id,
            object_version=parsed.object_version,
            provenance=parsed.provenance,
            derivation=parsed.derivation,
            actor=actor,
            policy_decision_id=decision.decision_id,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def record_artifact_scan(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactScanBody.model_validate_json(body)
        artifact_id = request.path_params["artifact_id"]
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="artifact.scan.record",
            resource=artifact_id,
            context={"attestation_digest": canonical_digest(parsed.attestation)},
        )
        return JSONResponse(core.artifacts.record_scan(artifact_id, parsed.attestation))

    async def release_artifact(request: Request) -> Response:
        core.artifacts.require_enabled()
        _body, actor = await body_and_actor(request, core)
        artifact_id = request.path_params["artifact_id"]
        decision = core._require(
            actor=actor,
            action="artifact.release",
            resource=artifact_id,
        )
        return JSONResponse(
            core.artifacts.release(
                artifact_id,
                actor=actor,
                policy_decision_id=decision.decision_id,
            )
        )

    async def issue_download_capability(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactDownloadCapabilityBody.model_validate_json(body)
        artifact_id = request.path_params["artifact_id"]
        if actor.harness_id is None:
            raise AuthorizationError("download requires exact harness attribution")
        exact = {"audience_harness_id": actor.harness_id}
        decision = core._require(
            actor=actor,
            action="artifact.download",
            resource=artifact_id,
            context=exact,
        )
        token = core.artifacts.issue_download_capability(
            artifact_id,
            actor=actor,
            audience_harness_id=actor.harness_id,
            policy_decision_id=decision.decision_id,
            ttl_seconds=parsed.ttl_seconds,
        )
        return JSONResponse({"artifact_id": artifact_id, "capability": token})

    async def consume_download(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactDownloadBody.model_validate_json(body)
        content = core.artifacts.consume_download(parsed.token, actor=actor)
        return Response(content, media_type="application/octet-stream")

    async def artifact_lifecycle(request: Request) -> Response:
        core.artifacts.require_enabled()
        _body, actor = await body_and_actor(request, core)
        artifact_id = request.path_params["artifact_id"]
        core._require(actor=actor, action="artifact.lifecycle.read", resource=artifact_id)
        return JSONResponse(core.artifacts.lifecycle_status(artifact_id, actor=actor))

    async def set_artifact_legal_hold(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactLifecycleMutationBody.model_validate_json(body)
        artifact_id = request.path_params["artifact_id"]
        exact = {
            "enabled": True,
            "expected_revision": parsed.expected_revision,
            "reason": parsed.reason.strip(),
        }
        decision = core._require(
            actor=actor,
            action="artifact.legal_hold.set",
            resource=artifact_id,
            context=exact,
        )
        return JSONResponse(
            core.artifacts.set_legal_hold(
                artifact_id,
                actor=actor,
                policy_decision_id=decision.decision_id,
                expected_revision=parsed.expected_revision,
                reason=parsed.reason,
                enabled=True,
            )
        )

    async def clear_artifact_legal_hold(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactLifecycleMutationBody.model_validate_json(body)
        artifact_id = request.path_params["artifact_id"]
        exact = {
            "enabled": False,
            "expected_revision": parsed.expected_revision,
            "reason": parsed.reason.strip(),
        }
        decision = core._require(
            actor=actor,
            action="artifact.legal_hold.clear",
            resource=artifact_id,
            context=exact,
        )
        return JSONResponse(
            core.artifacts.set_legal_hold(
                artifact_id,
                actor=actor,
                policy_decision_id=decision.decision_id,
                expected_revision=parsed.expected_revision,
                reason=parsed.reason,
                enabled=False,
            )
        )

    async def delete_artifact(request: Request) -> Response:
        core.artifacts.require_enabled()
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactLifecycleMutationBody.model_validate_json(body)
        artifact_id = request.path_params["artifact_id"]
        exact = {
            "expected_revision": parsed.expected_revision,
            "reason": parsed.reason.strip(),
        }
        decision = core._require(
            actor=actor,
            action="artifact.delete",
            resource=artifact_id,
            context=exact,
        )
        return JSONResponse(
            core.artifacts.delete(
                artifact_id,
                actor=actor,
                policy_decision_id=decision.decision_id,
                expected_revision=parsed.expected_revision,
                reason=parsed.reason,
            )
        )

    async def operator_status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        core._require(actor=actor, action="operator.status.read", resource="operator:self")
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
            admission_controls = {"available": True, **core.quotas.content_free_status()}
        except Exception:
            admission_controls = {"available": False}
        try:
            versioning = {"available": True, **core.versioning.content_free_status()}
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
                "deployment_binding_ready": bool(readiness["deployment_binding"].get("ready")),
                "a2a_ready": bool(readiness["a2a_schema"].get("ready")),
                "scanner_trust_ready": bool(readiness["scanner_trust"].get("ready")),
                "telemetry": telemetry,
                "admission_controls": admission_controls,
                "versioning": versioning,
            }
        )

    async def authority_inventory(request: Request) -> Response:
        if request.scope.get("query_string", b""):
            raise ValidationError("authority inventory does not accept caller-selected scope")
        await body_and_actor(request, core)
        inventory = core.authority_inspection.authority_inventory(
            transport=_trusted_transport(request),
        )
        return JSONResponse(
            {"authority": inventory.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def explain_denial(request: Request) -> Response:
        if request.scope.get("query_string", b""):
            raise ValidationError("denial explanation does not accept caller-selected scope")
        await body_and_actor(request, core)
        query = DenialExplanationQuery(decision_id=request.path_params["decision_id"])
        explanation = core.authority_inspection.explain_denial(
            transport=_trusted_transport(request),
            query=query,
        )
        return JSONResponse(
            {"explanation": explanation.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
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
            {"incident": core.incidents.state(core.config.domain_id).model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def set_incident_mode(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = IncidentModeChangeBody.model_validate_json(body, strict=True)
        if parsed.change.domain_id != core.config.domain_id:
            raise AuthorizationError("incident change crossed the authenticated domain")
        resource, _exact_request = core.incidents.authority_binding(parsed.change)
        authority = _authority(
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
            headers=RELATIONSHIP_RESPONSE_HEADERS,
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
        return JSONResponse(core.replay_unsupported_events(actor=actor, **parsed.model_dump()))

    routes = create_credential_routes(
        core,
        body_and_actor,
        expired_body_and_context,
    )
    routes.extend(
        create_organization_routes(
            core,
            body_and_actor,
            _authority,
            RELATIONSHIP_RESPONSE_HEADERS,
        )
    )
    routes.extend(
        create_automation_routes(
            core,
            body_and_actor,
            _authority,
            workload_json,
            resolved_workload_actor,
            RELATIONSHIP_RESPONSE_HEADERS,
        )
    )
    routes.extend(
        create_provenance_routes(
            core,
            body_and_actor,
            RELATIONSHIP_RESPONSE_HEADERS,
        )
    )
    routes.extend(create_task_grant_routes(core, body_and_actor, _authority))
    routes.extend(create_room_routes(core, body_and_actor, _decode_b64))
    routes += [
        Route("/v1/artifacts/reservations", reserve_artifact, methods=["POST"]),
        Route("/v1/artifacts/reservations/{reservation_id}/bytes", upload_artifact, methods=["POST"]),
        Route("/v1/artifacts/reservations/{reservation_id}/abort", abort_artifact_reservation, methods=["POST"]),
        Route("/v1/artifacts/reservations/{reservation_id}/promote", promote_artifact, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/scan", record_artifact_scan, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/release", release_artifact, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/download-capabilities", issue_download_capability, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/lifecycle", artifact_lifecycle, methods=["GET"]),
        Route("/v1/artifacts/{artifact_id}/legal-hold", set_artifact_legal_hold, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/legal-hold/clear", clear_artifact_legal_hold, methods=["POST"]),
        Route("/v1/artifacts/{artifact_id}/delete", delete_artifact, methods=["POST"]),
        Route("/v1/artifacts/download", consume_download, methods=["POST"]),
    ]
    routes.extend(
        create_effect_routes(
            core,
            body_and_actor,
            workload_json,
            authenticated_workload_actor,
        )
    )
    routes += [
        Route("/v1/operator/status", operator_status, methods=["GET"]),
        Route("/v1/authority", authority_inventory, methods=["GET"]),
        Route(
            "/v1/authority/denials/{decision_id}",
            explain_denial,
            methods=["GET"],
        ),
        Route("/v1/operator/incident", incident_status, methods=["GET"]),
        Route("/v1/operator/incident", set_incident_mode, methods=["POST"]),
        Route("/v1/operator/version-rollouts", begin_version_rollout, methods=["POST"]),
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
        Route("/v1/operator/version-replay", replay_version_events, methods=["POST"]),
    ]
    routes.extend(create_discovery_routes(core, body_and_actor))
    return routes


__all__ = ["create_product_routes"]
