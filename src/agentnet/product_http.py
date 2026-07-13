"""Authenticated routes composing the ordinary extension's domain services.

These handlers deliberately mint policy decisions from the transport-resolved
actor.  Policy-decision identifiers are never accepted from request JSON.
Administrative revocations additionally require the service's exact signed,
versioned authority command.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
from collections.abc import Awaitable, Callable
from datetime import datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.grants import GrantUse
from agentnet.approval import IndependentApprovalReceipt
from agentnet.automation import (
    AutomationCharter,
    AutomationInvocation,
    AutomationInvocationCompletion,
)
from agentnet.artifacts.scanner import ArtifactDerivationV1
from agentnet.core.app import CommunicationCore
from agentnet.discovery.directory import DirectoryRecord
from agentnet.effects.reservations import (
    EffectExecutionEvidence,
    EffectReconciliationEvidence,
    EffectState,
    EffectTerminalEvidence,
    EffectTransitionProof,
    EffectUncertaintyEvidence,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ValidationError
from agentnet.identity.actors import ActorKind, TrustedTransportContext, VerifiedActor
from agentnet.identity.credentials import CredentialRotationRequest
from agentnet.identity.workload import AuthenticatedSPIFFETransport
from agentnet.operations.incident import IncidentModeChange
from agentnet.operations.authority_inspection import DenialExplanationQuery
from agentnet.organization import (
    RelationshipPolicyException,
    TaskConflictAdjudication,
)
from agentnet.protocol.models import (
    Classification,
    PresenceLease,
    Relationship,
    ReleasedArtifactBinding,
    TaskGrant,
)
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceDerivation,
    ProvenanceObjectType,
)
from agentnet.rooms.governance import (
    RoomTransferSnapshot,
    SourceTransferProposal,
    TargetTransferAcceptance,
)
from agentnet.security.signatures import canonical_digest, canonical_json


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, VerifiedActor]]]

RELATIONSHIP_RESPONSE_HEADERS = {
    "Cache-Control": "no-store",
    "Pragma": "no-cache",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
}


class RelationshipProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    relationship: Relationship
    proposal_expires_at: datetime


class RelationshipAcceptanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    approval: IndependentApprovalReceipt
    expected_transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_relationship_revision: int = Field(ge=1)
    expected_lifecycle_revision: int = Field(ge=1)


class RelationshipPolicyExceptionRecordBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    exception: RelationshipPolicyException
    command: SignedAuthorityCommand


class RelationshipPolicyExceptionActivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    policy_exception_id: str = Field(min_length=1, max_length=128)
    expected_transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_relationship_revision: int = Field(ge=1)
    expected_lifecycle_revision: int = Field(ge=1)


class TaskGrantIssueBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    grant: TaskGrant


class AuthorityCommandBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    command: SignedAuthorityCommand


class TaskConflictAdjudicationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    decision: TaskConflictAdjudication


class AutomationCharterProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    charter: AutomationCharter


class AutomationCharterActivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    expected_charter_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_revision: int = Field(ge=1)
    approvals: tuple[IndependentApprovalReceipt, ...] = Field(min_length=1, max_length=5)


class AutomationCharterStopBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    expected_charter_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_revision: int = Field(ge=1)
    reason: str = Field(min_length=1, max_length=1024)


class AutomationInvocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    invocation: AutomationInvocation


class AutomationInvocationCompletionBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    completion: AutomationInvocationCompletion


class ProvenanceOriginBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    registration: OriginRegistration


class ProvenanceDerivationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    derivation: ProvenanceDerivation


class RoomCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    classification: Classification = Classification.C1_INTERNAL
    persistent: bool = True
    expires_at: datetime | None = None
    policy: dict[str, Any] | None = None


class MeetingCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    classification: Classification = Classification.C1_INTERNAL
    expires_at: datetime
    policy: dict[str, Any] | None = None


class RoomMemberBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    harness_id: str = Field(min_length=1, max_length=256)
    role: str = Field(default="member", pattern=r"^(member|guest|moderator)$")
    mls_key_package_b64: str | None = Field(default=None, max_length=1_000_000)


class RoomMemberRemoveBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    harness_id: str = Field(min_length=1, max_length=256)


class RoomSendBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()
    expected_control_sequence: int = Field(ge=1)
    conversation_id: str | None = None


class TransferProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proposal: SourceTransferProposal
    snapshot: RoomTransferSnapshot
    signature: str = Field(min_length=1, max_length=2048)
    additional_signatures: dict[str, str] = Field(default_factory=dict)


class TransferAcceptanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    acceptance: TargetTransferAcceptance
    signature: str = Field(min_length=1, max_length=2048)


class PresenceUpdateBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    lease: PresenceLease
    signature: str = Field(min_length=1, max_length=2048)


class DirectoryPublishBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    record: DirectoryRecord


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


def create_product_routes(core: CommunicationCore, body_and_actor: BodyAndActor) -> list[Route]:
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

    async def rotate_current_credential(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = CredentialRotationRequest.model_validate_json(body)
        result = core.rotate_credential(actor=actor, request=parsed)
        return JSONResponse({"credential": result.model_dump(mode="json")}, status_code=201)

    async def propose_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipProposalBody.model_validate_json(body, strict=True)
        proposal = core.propose_relationship(
            actor=actor,
            relationship=parsed.relationship,
            proposal_expires_at=parsed.proposal_expires_at,
        )
        return JSONResponse(
            {"proposal": proposal.model_dump(mode="json")},
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def accept_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipAcceptanceBody.model_validate_json(body, strict=True)
        relationship = core.accept_relationship(
            actor=actor,
            relationship_id=request.path_params["relationship_id"],
            approval=parsed.approval.model_dump(mode="json"),
            expected_transaction_digest=parsed.expected_transaction_digest,
            expected_relationship_revision=parsed.expected_relationship_revision,
            expected_lifecycle_revision=parsed.expected_lifecycle_revision,
        )
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def record_relationship_policy_exception(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipPolicyExceptionRecordBody.model_validate_json(body, strict=True)
        relationship_id = request.path_params["relationship_id"]
        if parsed.exception.relationship_id != relationship_id:
            raise AuthorizationError("relationship policy exception is not visible")
        policy_exception = core.record_relationship_policy_exception(
            actor=actor,
            exception=parsed.exception,
            command=parsed.command,
        )
        return JSONResponse(
            {"policy_exception": policy_exception.model_dump(mode="json")},
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def activate_relationship_policy_exception(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RelationshipPolicyExceptionActivationBody.model_validate_json(body, strict=True)
        relationship = core.activate_relationship_policy_exception(
            actor=actor,
            relationship_id=request.path_params["relationship_id"],
            policy_exception_id=parsed.policy_exception_id,
            expected_transaction_digest=parsed.expected_transaction_digest,
            expected_relationship_revision=parsed.expected_relationship_revision,
            expected_lifecycle_revision=parsed.expected_lifecycle_revision,
        )
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def get_relationship(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        relationship_id = request.path_params["relationship_id"]
        administrative = request.query_params.get("administrative", "false")
        if administrative not in {"true", "false"}:
            raise ValidationError("administrative must be true or false")
        action = (
            "organization.relationship.admin_read"
            if administrative == "true"
            else "organization.relationship.read"
        )
        resource, exact_request = core.relationships.read_binding(relationship_id)
        authority = _authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        relationship = core.relationships.get(
            relationship_id,
            authority=authority,
            administrative=administrative == "true",
        )
        if relationship is None:
            raise AuthorizationError("relationship is not visible")
        return JSONResponse(
            {"relationship": relationship.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def revoke_relationship(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AuthorityCommandBody.model_validate_json(body, strict=True)
        relationship_id = request.path_params["relationship_id"]
        core.revoke_relationship(
            actor=actor,
            relationship_id=relationship_id,
            command=parsed.command,
        )
        return JSONResponse(
            {"relationship_id": relationship_id, "revoked": True},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def list_task_conflicts(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        return JSONResponse(
            {
                "conflicts": core.assignments.pending_conflicts_for_owner(
                    actor=actor,
                    limit=100,
                )
            },
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def adjudicate_task_conflict(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TaskConflictAdjudicationBody.model_validate_json(body)
        conflict_id = request.path_params["conflict_id"]
        if parsed.decision.conflict_id != conflict_id:
            raise AuthorizationError("task conflict decision is unavailable")
        outcome = core.assignments.adjudicate_conflict(
            actor=actor,
            decision=parsed.decision,
        )
        return JSONResponse(
            {"conflict": outcome.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def propose_automation_charter(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterProposalBody.model_validate_json(body, strict=True)
        resource, exact_request = core.automation.authority_binding(parsed.charter)
        authority = _authority(
            core,
            actor=actor,
            action="automation.charter.propose",
            resource=resource,
            request=exact_request,
        )
        record = core.automation.propose(parsed.charter, authority=authority)
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def activate_automation_charter(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterActivationBody.model_validate_json(body, strict=True)
        record = core.automation.activate(
            actor=actor,
            charter_id=request.path_params["charter_id"],
            expected_charter_digest=parsed.expected_charter_digest,
            expected_revision=parsed.expected_revision,
            approvals=parsed.approvals,
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def list_automation_charters(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        raw_limit = request.query_params.get("limit", "100")
        if not raw_limit.isascii() or not raw_limit.isdigit():
            raise ValidationError("automation charter list limit is invalid")
        records = core.automation.list_for_owner(actor=actor, limit=int(raw_limit))
        return JSONResponse(
            {"charters": [record.model_dump(mode="json") for record in records]},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def get_automation_charter(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        record = core.automation.get_for_owner(
            actor=actor,
            charter_id=request.path_params["charter_id"],
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def stop_automation_charter(request: Request, *, emergency: bool) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AutomationCharterStopBody.model_validate_json(body, strict=True)
        charter_id = request.path_params["charter_id"]
        resource, exact_request = core.automation.mutation_binding(
            charter_id=charter_id,
            expected_revision=parsed.expected_revision,
            expected_charter_digest=parsed.expected_charter_digest,
            reason=parsed.reason,
            emergency=emergency,
        )
        action = (
            "automation.charter.emergency_stop"
            if emergency
            else "automation.charter.revoke"
        )
        authority = _authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        record = core.automation.stop(
            authority=authority,
            charter_id=charter_id,
            expected_revision=parsed.expected_revision,
            expected_charter_digest=parsed.expected_charter_digest,
            reason=parsed.reason,
            emergency=emergency,
        )
        return JSONResponse(
            {"charter": record.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def revoke_automation_charter(request: Request) -> Response:
        return await stop_automation_charter(request, emergency=False)

    async def emergency_stop_automation_charter(request: Request) -> Response:
        return await stop_automation_charter(request, emergency=True)

    async def reserve_automation_invocation(request: Request) -> Response:
        parsed = AutomationInvocationBody.model_validate_json(
            await workload_json(request), strict=True
        )
        charter_id = request.path_params["charter_id"]
        if parsed.invocation.charter_id != charter_id:
            raise AuthorizationError("automation invocation charter is unavailable")
        actor = resolved_workload_actor(
            request,
            parsed.invocation.workload_registration_id,
        )
        reservation = core.automation.reserve_invocation(
            actor=actor,
            invocation=parsed.invocation,
        )
        return JSONResponse(
            {"reservation": reservation.model_dump(mode="json")},
            status_code=200 if reservation.duplicate else 201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def finish_automation_invocation(request: Request) -> Response:
        parsed = AutomationInvocationCompletionBody.model_validate_json(
            await workload_json(request), strict=True
        )
        charter_id = request.path_params["charter_id"]
        invocation_id = request.path_params["invocation_id"]
        if (
            parsed.completion.charter_id != charter_id
            or parsed.completion.invocation_id != invocation_id
        ):
            raise AuthorizationError("automation completion is unavailable")
        actor = resolved_workload_actor(
            request,
            parsed.completion.workload_registration_id,
        )
        reservation = core.automation.finish_invocation(
            actor=actor,
            completion=parsed.completion,
        )
        return JSONResponse(
            {"reservation": reservation.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def register_provenance_origin(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ProvenanceOriginBody.model_validate_json(body, strict=True)
        registration = parsed.registration
        if registration.domain_id != actor.domain_id:
            raise AuthorizationError("provenance origin crossed the authenticated domain")
        if registration.origin.kind is not OriginKind.HUMAN_INPUT:
            raise AuthorizationError("non-human provenance origins require a composed server service")
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or registration.origin.principal_id != actor.principal_id
            or registration.origin.harness_id != actor.harness_id
        ):
            raise AuthorizationError("human provenance origin is not the authenticated human harness")
        resource = f"provenance:{registration.object_type.value}:{registration.object_id}"
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
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def derive_provenance(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ProvenanceDerivationBody.model_validate_json(body, strict=True)
        derivation = parsed.derivation
        if derivation.domain_id != actor.domain_id:
            raise AuthorizationError("derived provenance crossed the authenticated domain")
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
            headers=RELATIONSHIP_RESPONSE_HEADERS,
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
        records = core.provenance.versions(object_type=object_type, object_id=object_id)
        return JSONResponse(
            {"versions": [record.model_dump(mode="json") for record in records]},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def provenance_version(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        try:
            object_type = ProvenanceObjectType(request.path_params["object_type"])
        except ValueError as exc:
            raise ValidationError("provenance object type is invalid") from exc
        raw_version = request.path_params["version"]
        if not raw_version.isascii() or not raw_version.isdigit() or int(raw_version) < 1:
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
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def issue_task_grant(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TaskGrantIssueBody.model_validate_json(body)
        issued = core.issue_task_grant(actor=actor, grant=parsed.grant)
        return JSONResponse({"grant": issued.model_dump(mode="json")}, status_code=201)

    async def get_task_grant(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        grant_id = request.path_params["grant_id"]
        administrative = request.query_params.get("administrative", "false")
        if administrative not in {"true", "false"}:
            raise ValidationError("administrative must be true or false")
        action = (
            "authorization.task_grant.admin_read"
            if administrative == "true"
            else "authorization.task_grant.read"
        )
        resource, exact_request = core.grants.read_binding(grant_id)
        authority = _authority(
            core,
            actor=actor,
            action=action,
            resource=resource,
            request=exact_request,
        )
        grant = core.grants.get(
            grant_id,
            authority=authority,
            administrative=administrative == "true",
        )
        if grant is None:
            raise AuthorizationError("task grant is not visible")
        return JSONResponse({"grant": grant.model_dump(mode="json")})

    async def revoke_task_grant(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = AuthorityCommandBody.model_validate_json(body)
        grant_id = request.path_params["grant_id"]
        if parsed.command.resource != f"task-grant:{grant_id}":
            raise AuthorizationError("task grant authority binding mismatch")
        authority = _authority(
            core,
            actor=actor,
            action=parsed.command.action,
            resource=parsed.command.resource,
            request={"request_digest": parsed.command.request_digest},
        )
        core.grants.revoke(grant_id, command=parsed.command, authority=authority)
        return JSONResponse({"grant_id": grant_id, "revoked": True})

    async def create_room(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomCreateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="room.create",
            resource="room:new",
            classification=parsed.classification,
            context={
                "classification": parsed.classification.value,
                "persistent": parsed.persistent,
                "expires_at": parsed.expires_at.isoformat() if parsed.expires_at else None,
                "policy_digest": canonical_digest(parsed.policy or {}),
            },
        )
        result = core.rooms.create(
            actor=actor,
            classification=parsed.classification,
            persistent=parsed.persistent,
            expires_at=parsed.expires_at,
            policy=parsed.policy,
        )
        return JSONResponse(result, status_code=201)

    async def create_meeting(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = MeetingCreateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="room.create",
            resource="room:new",
            classification=parsed.classification,
            context={
                "classification": parsed.classification.value,
                "persistent": False,
                "expires_at": parsed.expires_at.isoformat(),
                "policy_digest": canonical_digest(parsed.policy or {}),
            },
        )
        result = core.rooms.create(
            actor=actor,
            classification=parsed.classification,
            persistent=False,
            expires_at=parsed.expires_at,
            policy=parsed.policy,
        )
        return JSONResponse(result, status_code=201)

    async def add_room_member(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomMemberBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        mls_key_package = (
            _decode_b64(parsed.mls_key_package_b64, field="mls_key_package_b64")
            if parsed.mls_key_package_b64 is not None
            else None
        )
        core._require(
            actor=actor,
            action="room.member.add",
            resource=room_id,
            context={
                "harness_id": parsed.harness_id,
                "role": parsed.role,
                "mls_key_package_digest": (
                    hashlib.sha256(mls_key_package).hexdigest()
                    if mls_key_package is not None
                    else None
                ),
            },
        )
        result = core.rooms.add_member(
            actor=actor,
            room_id=room_id,
            harness_id=parsed.harness_id,
            role=parsed.role,
            mls_key_package=mls_key_package,
        )
        return JSONResponse(result, status_code=201)

    async def remove_room_member(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomMemberRemoveBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        core._require(
            actor=actor,
            action="room.member.remove",
            resource=room_id,
            context={"harness_id": parsed.harness_id},
        )
        return JSONResponse(
            core.rooms.remove_member(actor=actor, room_id=room_id, harness_id=parsed.harness_id)
        )

    async def describe_room(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        room_id = request.path_params["room_id"]
        core._require(actor=actor, action="room.read", resource=room_id)
        return JSONResponse(core.rooms.describe(actor=actor, room_id=room_id))

    async def send_room_message(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomSendBody.model_validate_json(body)
        result = core.send_message(
            actor=actor,
            recipients=parsed.recipients,
            payload=parsed.payload,
            idempotency_key=parsed.idempotency_key,
            classification=parsed.classification,
            released_artifacts=parsed.released_artifacts,
            conversation_id=parsed.conversation_id,
            room_id=request.path_params["room_id"],
            expected_room_control_sequence=parsed.expected_control_sequence,
        )
        return JSONResponse(result, status_code=202)

    async def propose_room_transfer(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TransferProposalBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        if parsed.proposal.room_id != room_id or parsed.snapshot.room_id != room_id:
            raise AuthorizationError("room transfer path binding mismatch")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.propose",
            resource=room_id,
            context={"proposal_digest": parsed.proposal.digest, "snapshot_digest": parsed.snapshot.digest},
        )
        return JSONResponse(
            core.room_governance.propose_transfer(
                actor=actor,
                proposal=parsed.proposal,
                snapshot=parsed.snapshot,
                signature=parsed.signature,
                additional_signatures=parsed.additional_signatures,
            ),
            status_code=202,
        )

    async def accept_room_transfer(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TransferAcceptanceBody.model_validate_json(body)
        transfer_id = request.path_params["transfer_id"]
        if parsed.acceptance.transfer_id != transfer_id:
            raise AuthorizationError("room transfer path binding mismatch")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.accept",
            resource=f"room-transfer:{transfer_id}",
            context={"acceptance_digest": parsed.acceptance.digest},
        )
        return JSONResponse(
            core.room_governance.accept_target(
                actor=actor,
                acceptance=parsed.acceptance,
                signature=parsed.signature,
            )
        )

    async def commit_room_transfer(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        transfer_id = request.path_params["transfer_id"]
        with core.store.transaction(immediate=False) as connection:
            transfer = connection.execute(
                "SELECT target_domain_id,target_credential_id,state FROM room_transfers WHERE transfer_id=?",
                (transfer_id,),
            ).fetchone()
            if (
                transfer is None
                or transfer["state"] != "target_accepted"
                or transfer["target_domain_id"] != actor.domain_id
                or transfer["target_credential_id"] != actor.credential_id
            ):
                raise AuthorizationError("room transfer is not visible")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.commit",
            resource=f"room-transfer:{transfer_id}",
        )
        return JSONResponse(core.room_governance.commit(transfer_id))

    async def update_presence(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = PresenceUpdateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="presence.update",
            resource=parsed.lease.harness_id,
            context={"lease_digest": canonical_digest(parsed.lease.model_dump(mode="json"))},
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
        authority = _authority(
            core,
            actor=actor,
            action="directory.publish",
            resource=resource,
            request=exact_request,
        )
        result = core.directory.publish(parsed.record, authority=authority)
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
            not record_types or not record_types.issubset({"agent", "room", "domain", "endpoint"})
        ):
            raise ValidationError("directory record type filter is invalid")
        core._require(
            actor=actor,
            action="directory.list",
            resource="directory:self",
            context={"limit": limit, "record_types": sorted(record_types) if record_types else None},
        )
        records = core.directory.list_records(actor, record_types=record_types, limit=limit)
        return JSONResponse({"records": [record.model_dump(mode="json") for record in records]})

    async def reserve_artifact(request: Request) -> Response:
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
        body, actor = await body_and_actor(request, core)
        parsed = ArtifactDownloadBody.model_validate_json(body)
        content = core.artifacts.consume_download(parsed.token, actor=actor)
        return Response(content, media_type="application/octet-stream")

    async def artifact_lifecycle(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        artifact_id = request.path_params["artifact_id"]
        core._require(actor=actor, action="artifact.lifecycle.read", resource=artifact_id)
        return JSONResponse(core.artifacts.lifecycle_status(artifact_id, actor=actor))

    async def set_artifact_legal_hold(request: Request) -> Response:
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

    async def reserve_effect(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = EffectReserveBody.model_validate_json(body)
        result = core.reserve_effect(
            actor=actor,
            event_id=parsed.event_id,
            grant_use=parsed.grant_use,
            request=parsed.request,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def effect_status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        effect_id = request.path_params["effect_id"]
        core._require(actor=actor, action="effect.status", resource=effect_id)
        return JSONResponse(core.effects.status(effect_id, actor=actor))

    async def cancel_effect(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        return JSONResponse(
            core.cancel_effect(actor=actor, effect_id=request.path_params["effect_id"])
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

    return [
        Route("/v1/credentials/current/rotate", rotate_current_credential, methods=["POST"]),
        Route("/v1/relationships", propose_relationship, methods=["POST"]),
        Route("/v1/relationships/{relationship_id}/accept", accept_relationship, methods=["POST"]),
        Route(
            "/v1/relationships/{relationship_id}/policy-exceptions",
            record_relationship_policy_exception,
            methods=["POST"],
        ),
        Route(
            "/v1/relationships/{relationship_id}/policy-exceptions/activate",
            activate_relationship_policy_exception,
            methods=["POST"],
        ),
        Route("/v1/relationships/{relationship_id}", get_relationship, methods=["GET"]),
        Route("/v1/relationships/{relationship_id}/revoke", revoke_relationship, methods=["POST"]),
        Route("/v1/task-conflicts", list_task_conflicts, methods=["GET"]),
        Route(
            "/v1/task-conflicts/{conflict_id}/adjudicate",
            adjudicate_task_conflict,
            methods=["POST"],
        ),
        Route("/v1/automation-charters", propose_automation_charter, methods=["POST"]),
        Route("/v1/automation-charters", list_automation_charters, methods=["GET"]),
        Route(
            "/v1/automation-charters/{charter_id}",
            get_automation_charter,
            methods=["GET"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/activate",
            activate_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/revoke",
            revoke_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/emergency-stop",
            emergency_stop_automation_charter,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/invocations",
            reserve_automation_invocation,
            methods=["POST"],
        ),
        Route(
            "/v1/automation-charters/{charter_id}/invocations/{invocation_id}/terminal",
            finish_automation_invocation,
            methods=["POST"],
        ),
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
        Route("/v1/task-grants", issue_task_grant, methods=["POST"]),
        Route("/v1/task-grants/{grant_id}", get_task_grant, methods=["GET"]),
        Route("/v1/task-grants/{grant_id}/revoke", revoke_task_grant, methods=["POST"]),
        Route("/v1/rooms", create_room, methods=["POST"]),
        Route("/v1/meetings", create_meeting, methods=["POST"]),
        Route("/v1/rooms/{room_id}", describe_room, methods=["GET"]),
        Route("/v1/rooms/{room_id}/members", add_room_member, methods=["POST"]),
        Route("/v1/rooms/{room_id}/members/remove", remove_room_member, methods=["POST"]),
        Route("/v1/rooms/{room_id}/messages", send_room_message, methods=["POST"]),
        Route("/v1/rooms/{room_id}/transfers", propose_room_transfer, methods=["POST"]),
        Route("/v1/room-transfers/{transfer_id}/accept", accept_room_transfer, methods=["POST"]),
        Route("/v1/room-transfers/{transfer_id}/commit", commit_room_transfer, methods=["POST"]),
        Route("/v1/presence", update_presence, methods=["POST"]),
        Route("/v1/presence/{harness_id}", presence_state, methods=["GET"]),
        Route("/v1/directory", directory_records, methods=["GET"]),
        Route("/v1/directory", publish_directory_record, methods=["POST"]),
        Route("/v1/directory/{record_id}", directory_record, methods=["GET"]),
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
        Route("/v1/effects", reserve_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}", effect_status, methods=["GET"]),
        Route("/v1/effects/{effect_id}/cancel", cancel_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}/start", start_effect, methods=["POST"]),
        Route("/v1/effects/{effect_id}/unknown", mark_effect_unknown, methods=["POST"]),
        Route("/v1/effects/{effect_id}/terminal", acknowledge_effect_terminal, methods=["POST"]),
        Route("/v1/effects/{effect_id}/reconcile", reconcile_effect, methods=["POST"]),
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


__all__ = ["create_product_routes"]
