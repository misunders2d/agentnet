"""Authenticated, revision-fenced identity and authority administration routes.

The human administrator is always derived from the ordinary DPoP transport.
Workload identity is independently supplied by the hosting server's verified
mTLS/SPIFFE scope; no header or JSON field can manufacture workload authority.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from typing import Any

from starlette.requests import Request
from starlette.routing import Route

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authorization.admin_http import create_authority_admin_routes
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest
from agentnet.core.app import CommunicationCore
from agentnet.errors import GateBlocked
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator
from agentnet.identity.recovery_http import create_credential_recovery_routes
from agentnet.identity.revocation import HarnessRevocationService
from agentnet.identity.revocation_http import create_harness_revocation_routes
from agentnet.identity.workload_http import create_workload_admin_routes


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]


def _headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


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
            create_credential_recovery_routes(
                core,
                recovery_coordinator,
                _headers(),
            )
        )
    return routes


__all__ = ["create_identity_admin_routes"]
