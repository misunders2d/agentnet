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

from starlette.requests import Request
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.grant_http import create_task_grant_routes
from agentnet.automation_http import create_automation_routes
from agentnet.artifacts.http import create_artifact_routes
from agentnet.core.app import CommunicationCore
from agentnet.discovery.http import create_discovery_routes
from agentnet.effects.http import create_effect_routes
from agentnet.effects.reservations import EffectTransitionProof
from agentnet.errors import AuthenticationError, AuthorizationError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.credential_http import (
    ExpiredBodyAndContext,
    create_credential_routes,
)
from agentnet.identity.workload import AuthenticatedSPIFFETransport
from agentnet.operations.http import create_operator_routes
from agentnet.organization.http import create_organization_routes
from agentnet.provenance_http import create_provenance_routes
from agentnet.rooms.http import create_room_routes


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, VerifiedActor]]]

RELATIONSHIP_RESPONSE_HEADERS = {
    "Cache-Control": "no-store",
    "Pragma": "no-cache",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
}


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
    routes.extend(create_artifact_routes(core, body_and_actor, _decode_b64))
    routes.extend(
        create_effect_routes(
            core,
            body_and_actor,
            workload_json,
            authenticated_workload_actor,
        )
    )
    routes.extend(
        create_operator_routes(
            core,
            body_and_actor,
            _authority,
            RELATIONSHIP_RESPONSE_HEADERS,
        )
    )
    routes.extend(create_discovery_routes(core, body_and_actor))
    return routes


__all__ = ["create_product_routes"]
