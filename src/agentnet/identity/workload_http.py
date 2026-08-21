"""Transport-bound workload identity and mailbox-transition HTTP routes."""

from __future__ import annotations

import time
from collections.abc import Awaitable, Callable, Mapping
from datetime import UTC, datetime

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.workload import (
    AuthenticatedSPIFFETransport,
    WorkloadIdentity,
    WorkloadTransitionProof,
)
from agentnet.mailbox.service import ExpiryAuthorization
from agentnet.protocol.models import DeliveryFact
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
DecisionIssuer = Callable[..., IssuanceAuthority]


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


def create_workload_admin_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    issue_decision: DecisionIssuer,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount workload lifecycle and mailbox-expiry routes."""

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
        authority = issue_decision(
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
        return JSONResponse(
            result.model_dump(mode="json"),
            status_code=201,
            headers=response_headers,
        )

    async def renew_workload(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = WorkloadRenewalBody.model_validate_json(body)
        identity, process_id, process_start_time, session_id = _workload_transport(request, core)
        registration_id = request.path_params["registration_id"]
        authority = issue_decision(
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
        return JSONResponse(result.model_dump(mode="json"), headers=response_headers)

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
        authority = issue_decision(
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
        return JSONResponse(result, headers=response_headers)

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
        return JSONResponse({"expired": count}, headers=response_headers)

    return [
        Route("/v1/admin/workloads", register_workload, methods=["POST"]),
        Route(
            "/v1/admin/workloads/{registration_id}/renew",
            renew_workload,
            methods=["POST"],
        ),
        Route(
            "/v1/admin/workloads/{registration_id}/revoke",
            revoke_workload,
            methods=["POST"],
        ),
        Route("/v1/workloads/mailbox/expire-due", expire_mailbox_due, methods=["POST"]),
    ]


__all__ = ["create_workload_admin_routes"]
