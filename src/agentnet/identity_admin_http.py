"""Authenticated, revision-fenced identity and authority administration routes.

The human administrator is always derived from the ordinary DPoP transport.
Workload identity is independently supplied by the hosting server's verified
mTLS/SPIFFE scope; no header or JSON field can manufacture workload authority.
"""

from __future__ import annotations

import time
from collections.abc import Awaitable, Callable
from dataclasses import asdict
from datetime import UTC, datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authorization.admin_http import create_authority_admin_routes
from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.policy import AuthorizationRequest
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, GateBlocked, ValidationError
from agentnet.identity.revocation import HarnessRevocationRequest, HarnessRevocationService
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator
from agentnet.identity.workload import (
    AuthenticatedSPIFFETransport,
    WorkloadIdentity,
    WorkloadTransitionProof,
)
from agentnet.mailbox.service import ExpiryAuthorization
from agentnet.protocol.models import DeliveryFact
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]


def _headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


class HarnessRevocationPrepareBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    harness_id: str = Field(min_length=1, max_length=256)
    reason: str = Field(min_length=1, max_length=512)


class HarnessRevocationCommitBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    request: HarnessRevocationRequest
    independent_approval: dict[str, Any]


class WorkloadRegistrationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    registration_id: str = Field(min_length=16, max_length=128)
    workload_id: str = Field(min_length=1, max_length=256)
    workload_role: str = Field(min_length=1, max_length=128)
    recipient_scope: str = Field(min_length=1, max_length=256)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    key_id: str = Field(min_length=32, max_length=128)
    credential_epoch: int = Field(ge=1)
    revocation_epoch: int = Field(ge=1)
    parent_event_id: str | None = Field(default=None, max_length=256)
    task_grant_id: str | None = Field(default=None, max_length=256)
    issued_at: int = Field(gt=0)
    expires_at: int = Field(gt=0)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    command: SignedAuthorityCommand


class WorkloadRenewalBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    expected_credential_epoch: int = Field(ge=1)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    key_id: str = Field(min_length=32, max_length=128)
    issued_at: int = Field(gt=0)
    expires_at: int = Field(gt=0)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    command: SignedAuthorityCommand


class WorkloadRevocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    expected_credential_epoch: int = Field(ge=1)
    expected_revocation_epoch: int = Field(ge=1)
    reason: str = Field(min_length=1, max_length=512)
    command: SignedAuthorityCommand


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


class MailboxExpiryDispatchBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    authoritative_clock: int = Field(gt=0)
    proofs: tuple[WorkloadTransitionProof, ...] = Field(min_length=1, max_length=1_000)


def _workload_transport(
    request: Request,
    core: CommunicationCore,
) -> tuple[WorkloadIdentity, int, int, str]:
    raw = request.scope.get("agentnet.workload_transport")
    if not isinstance(raw, AuthenticatedSPIFFETransport):
        raise AuthenticationError("verified workload transport context is required")
    identity = core.workloads.spiffe.resolve(raw)
    return (
        identity,
        raw.facts.process_id,
        raw.facts.process_start_time,
        raw.facts.session_id,
    )


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

    async def prepare_harness_revocation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = HarnessRevocationPrepareBody.model_validate_json(body)
        # Gate the named target before reading it so this route cannot become
        # an authenticated harness-existence oracle.
        _decision(
            core,
            actor=actor,
            action="identity.harness.revoke",
            resource=f"harness:{parsed.harness_id}",
            request_digest=canonical_digest(
                {
                    "domain_id": core.config.domain_id,
                    "harness_id": parsed.harness_id,
                    "reason": parsed.reason,
                }
            ),
        )
        prepared = revocations.prepare(
            domain_id=core.config.domain_id,
            harness_id=parsed.harness_id,
            reason=parsed.reason,
        )
        return JSONResponse(prepared.model_dump(mode="json"), status_code=201, headers=_headers())

    async def commit_harness_revocation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = HarnessRevocationCommitBody.model_validate_json(body)
        if parsed.request.domain_id != core.config.domain_id:
            raise AuthenticationError("harness revocation domain binding mismatch")
        resource, mutation = revocations.authority_binding(parsed.request)
        authority = _decision(
            core,
            actor=actor,
            action="identity.harness.revoke",
            resource=resource,
            request_digest=mutation["request_digest"],
        )
        result = revocations.revoke(
            request=parsed.request,
            authority=authority,
            approval=parsed.independent_approval,
        )
        return JSONResponse(asdict(result), headers=_headers())

    async def register_workload(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = WorkloadRegistrationBody.model_validate_json(body)
        identity, process_id, process_start_time, session_id = _workload_transport(request, core)
        mutation = core.workloads.registration_request(
            registration_id=parsed.registration_id,
            domain_id=core.config.domain_id,
            workload_id=parsed.workload_id,
            workload_role=parsed.workload_role,
            recipient_scope=parsed.recipient_scope,
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=parsed.public_key_pem,
            key_id=parsed.key_id,
            credential_epoch=parsed.credential_epoch,
            revocation_epoch=parsed.revocation_epoch,
            parent_event_id=parsed.parent_event_id,
            task_grant_id=parsed.task_grant_id,
            issued_at=parsed.issued_at,
            expires_at=parsed.expires_at,
        )
        authority = _decision(
            core,
            actor=actor,
            action="identity.workload.register",
            resource=f"workload:{parsed.registration_id}",
            request_digest=canonical_digest(mutation),
        )
        result = core.workloads.register(
            authority=authority,
            command=parsed.command,
            registration_id=parsed.registration_id,
            domain_id=core.config.domain_id,
            workload_id=parsed.workload_id,
            workload_role=parsed.workload_role,
            recipient_scope=parsed.recipient_scope,
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=parsed.public_key_pem,
            key_id=parsed.key_id,
            credential_epoch=parsed.credential_epoch,
            revocation_epoch=parsed.revocation_epoch,
            parent_event_id=parsed.parent_event_id,
            task_grant_id=parsed.task_grant_id,
            issued_at=parsed.issued_at,
            expires_at=parsed.expires_at,
            possession_signature=parsed.possession_signature,
        )
        return JSONResponse(result.model_dump(mode="json"), status_code=201, headers=_headers())

    async def renew_workload(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = WorkloadRenewalBody.model_validate_json(body)
        identity, process_id, process_start_time, session_id = _workload_transport(request, core)
        registration_id = request.path_params["registration_id"]
        authority = _decision(
            core,
            actor=actor,
            action="identity.workload.renew",
            resource=f"workload:{registration_id}",
            request_digest=parsed.command.request_digest,
        )
        current = core.store.fetch_one(
            "SELECT revocation_epoch FROM workload_registrations WHERE registration_id=?",
            (registration_id,),
        )
        if current is None:
            raise AuthenticationError("workload registration is unavailable")
        mutation = core.workloads.renewal_request(
            registration_id=registration_id,
            expected_credential_epoch=parsed.expected_credential_epoch,
            credential_epoch=parsed.expected_credential_epoch + 1,
            revocation_epoch=int(current["revocation_epoch"]),
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=parsed.public_key_pem,
            key_id=parsed.key_id,
            issued_at=parsed.issued_at,
            expires_at=parsed.expires_at,
        )
        if parsed.command.request_digest != canonical_digest(mutation):
            raise AuthenticationError("workload renewal command request binding mismatch")
        result = core.workloads.renew(
            authority=authority,
            command=parsed.command,
            registration_id=registration_id,
            expected_credential_epoch=parsed.expected_credential_epoch,
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=parsed.public_key_pem,
            key_id=parsed.key_id,
            issued_at=parsed.issued_at,
            expires_at=parsed.expires_at,
            possession_signature=parsed.possession_signature,
        )
        return JSONResponse(result.model_dump(mode="json"), headers=_headers())

    async def revoke_workload(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = WorkloadRevocationBody.model_validate_json(body)
        registration_id = request.path_params["registration_id"]
        mutation = core.workloads.revocation_request(
            registration_id=registration_id,
            expected_credential_epoch=parsed.expected_credential_epoch,
            expected_revocation_epoch=parsed.expected_revocation_epoch,
            reason=parsed.reason,
        )
        authority = _decision(
            core,
            actor=actor,
            action="identity.workload.revoke",
            resource=f"workload:{registration_id}",
            request_digest=canonical_digest(mutation),
        )
        result = core.workloads.revoke(
            authority=authority,
            command=parsed.command,
            registration_id=registration_id,
            expected_credential_epoch=parsed.expected_credential_epoch,
            expected_revocation_epoch=parsed.expected_revocation_epoch,
            reason=parsed.reason,
        )
        return JSONResponse(result, headers=_headers())

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

    async def expire_mailbox_due(request: Request) -> Response:
        body = await request.body()
        if len(body) > core.config.max_request_bytes:
            raise ValidationError("mailbox expiry request exceeds the configured limit")
        parsed = MailboxExpiryDispatchBody.model_validate_json(body)
        server_now = int(time.time())
        if parsed.authoritative_clock > server_now or parsed.authoritative_clock < server_now - 60:
            raise AuthenticationError("mailbox expiry authoritative clock is stale or future-dated")
        _identity, process_id, process_start_time, session_id = _workload_transport(request, core)
        authorizations: dict[tuple[str, str], ExpiryAuthorization] = {}
        registration_id: str | None = None
        for proof in parsed.proofs:
            if proof.proposed_fact is not DeliveryFact.EXPIRED:
                raise AuthenticationError("mailbox expiry proof proposes the wrong delivery fact")
            if registration_id is None:
                registration_id = proof.registration_id
            elif registration_id != proof.registration_id:
                raise AuthenticationError("one transport cannot combine multiple workload registrations")
            actor = core.workloads.resolve(
                transport=request.scope["agentnet.workload_transport"],
                registration_id=proof.registration_id,
                process_id=process_id,
                process_start_time=process_start_time,
                session_id=session_id,
                now=parsed.authoritative_clock,
            )
            key = (proof.event_id, proof.recipient_id)
            if key in authorizations:
                raise ValidationError("mailbox expiry contains a duplicate event/recipient proof")
            authorizations[key] = ExpiryAuthorization(proof=proof, actor=actor)
        core.quotas.consume(
            scope=f"workload-expiry:{registration_id}",
            metric="expiry_transitions",
            amount=len(authorizations),
            limit=1_000,
        )
        count = core.mailboxes.expire_due(
            authoritative_now=datetime.fromtimestamp(parsed.authoritative_clock, UTC),
            authorizations=authorizations,
        )
        return JSONResponse({"expired": count}, headers=_headers())

    routes = create_authority_admin_routes(
        core,
        body_and_actor,
        elevations,
        _decision,
        _headers(),
    )
    routes += [
        Route("/v1/admin/harness-revocations/prepare", prepare_harness_revocation, methods=["POST"]),
        Route("/v1/admin/harness-revocations/commit", commit_harness_revocation, methods=["POST"]),
        Route("/v1/admin/workloads", register_workload, methods=["POST"]),
        Route("/v1/admin/workloads/{registration_id}/renew", renew_workload, methods=["POST"]),
        Route("/v1/admin/workloads/{registration_id}/revoke", revoke_workload, methods=["POST"]),
        Route("/v1/workloads/mailbox/expire-due", expire_mailbox_due, methods=["POST"]),
    ]
    if recovery_coordinator is not None:
        routes.extend(
            [
                Route("/v1/credential-recovery/oidc/begin", begin_recovery, methods=["POST"]),
                Route("/v1/credential-recovery/complete", complete_recovery, methods=["POST"]),
            ]
        )
    return routes


__all__ = ["create_identity_admin_routes"]
