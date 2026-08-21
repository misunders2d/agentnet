"""Authenticated, revision-fenced identity and authority administration routes.

The human administrator is always derived from the ordinary DPoP transport.
Workload identity is independently supplied by the hosting server's verified
mTLS/SPIFFE scope; no header or JSON field can manufacture workload authority.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import asdict
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authorization.admin_http import create_authority_admin_routes
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest
from agentnet.core.app import CommunicationCore
from agentnet.errors import GateBlocked, ValidationError
from agentnet.identity.revocation import HarnessRevocationService
from agentnet.identity.revocation_http import create_harness_revocation_routes
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator
from agentnet.identity.workload_http import create_workload_admin_routes


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]


def _headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


class RecoveryBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    old_harness_id: str = Field(min_length=1, max_length=256)
    new_harness_kind: str = Field(min_length=1, max_length=64)
    new_harness_name: str = Field(min_length=1, max_length=128)
    new_binding_assurance: Literal["os_bound", "hardware_bound"]
    new_public_key_pem: str = Field(min_length=128, max_length=16_384)


class RecoveryCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recovery_transaction_id: str = Field(min_length=16, max_length=128)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    independent_approvals: tuple[dict[str, Any], ...] = Field(min_length=1, max_length=5)


def _decision(
    core: CommunicationCore,
    *,
    actor: Any,
    action: str,
    resource: str,
    request_digest: str,
) -> IssuanceAuthority:
    revision = core.policy.current_policy_revision(actor)
    decision = core.policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=revision,
            context={"request_digest": request_digest},
        )
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def create_identity_admin_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    verifier: IndependentApprovalVerifier,
    *,
    recovery_coordinator: OIDCCredentialRecoveryCoordinator | None = None,
) -> list[Route]:
    """Create routes only for a configured independent approval ceremony."""

    if not isinstance(verifier, IndependentApprovalVerifier):
        raise GateBlocked(
            "identity_administration",
            "identity administration requires the configured independent approval verifier",
        )
    elevations = core.create_elevation_service(verifier)
    revocations = HarnessRevocationService(
        core.store,
        verifier,
        relationships=core.relationships,
        task_grants=core.grants,
    )

    async def begin_recovery(request: Request) -> Response:
        if recovery_coordinator is None:
            raise GateBlocked("credential_recovery", "OIDC credential recovery is not configured")
        peer = "unavailable" if request.client is None else request.client.host
        core.quotas.consume(
            scope=f"public-credential-recovery:{peer}",
            metric="recovery_attempts",
            amount=1,
            limit=20,
        )
        body = await request.body()
        if len(body) > core.config.max_request_bytes:
            raise ValidationError("credential recovery request exceeds the configured limit")
        parsed = RecoveryBeginBody.model_validate_json(body)
        authorization = recovery_coordinator.begin_authorization(
            domain_id=core.config.domain_id,
            old_harness_id=parsed.old_harness_id,
            new_harness_kind=parsed.new_harness_kind,
            new_harness_name=parsed.new_harness_name,
            new_binding_assurance=parsed.new_binding_assurance,
            new_public_key_pem=parsed.new_public_key_pem,
        )
        return JSONResponse(asdict(authorization), status_code=201, headers=_headers())

    async def complete_recovery(request: Request) -> Response:
        if recovery_coordinator is None:
            raise GateBlocked("credential_recovery", "OIDC credential recovery is not configured")
        peer = "unavailable" if request.client is None else request.client.host
        core.quotas.consume(
            scope=f"public-credential-recovery:{peer}",
            metric="recovery_attempts",
            amount=1,
            limit=20,
        )
        body = await request.body()
        if len(body) > core.config.max_request_bytes:
            raise ValidationError("credential recovery request exceeds the configured limit")
        parsed = RecoveryCompleteBody.model_validate_json(body)
        result = recovery_coordinator.complete_recovery(
            transaction_id=parsed.recovery_transaction_id,
            possession_signature=parsed.possession_signature,
            approvals=parsed.independent_approvals,
        )
        return JSONResponse(result.model_dump(mode="json"), status_code=201, headers=_headers())

    routes = create_authority_admin_routes(
        core,
        body_and_actor,
        elevations,
        _decision,
        _headers(),
    )
    routes.extend(
        create_harness_revocation_routes(
            core,
            body_and_actor,
            revocations,
            _decision,
            _headers(),
        )
    )
    routes.extend(
        create_workload_admin_routes(
            core,
            body_and_actor,
            _decision,
            _headers(),
        )
    )
    if recovery_coordinator is not None:
        routes.extend(
            [
                Route("/v1/credential-recovery/oidc/begin", begin_recovery, methods=["POST"]),
                Route("/v1/credential-recovery/complete", complete_recovery, methods=["POST"]),
            ]
        )
    return routes


__all__ = ["create_identity_admin_routes"]
