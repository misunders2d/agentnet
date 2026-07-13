"""Strict, versioned seams that keep corporate semantics component-independent.

These protocols intentionally do not exchange arbitrary mappings.  A reused
component's native response must be parsed into the exact model at its adapter
boundary before AgentNet policy code can consume it.  Constructing a model is
shape validation only; authenticated facts still require their owning verifier.
"""

from __future__ import annotations

from datetime import datetime
from pathlib import Path
from typing import BinaryIO, Literal, Protocol, runtime_checkable

from pydantic import BaseModel, ConfigDict, Field

from agentnet.approval.service import (
    IndependentApprovalReceipt,
    VerifiedIndependentApproval,
)
from agentnet.authorization.decision import AuthorizationDecision
from agentnet.authorization.policy import AuthorizationRequest
from agentnet.effects.workflow import WorkflowState
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.workload import (
    AuthenticatedSPIFFETransport,
    WorkloadIdentity,
)
from agentnet.protocol.models import DeliveryFact, EventEnvelope


class StrictInterfaceModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)


class ArtifactStoredVersionV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    object_key: str = Field(pattern=r"^[a-f0-9]{32}$")
    version: str = Field(pattern=r"^[a-f0-9]{64}$")
    ciphertext_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    ciphertext_size: int = Field(ge=0)


class MailboxAcceptanceV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    event_id: str = Field(min_length=1, max_length=256)
    fact: Literal[
        DeliveryFact.ACCEPTED_LOCAL,
        DeliveryFact.ACCEPTED_DURABLE,
    ]
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    duplicate: bool


class MailboxReconciliationItemV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    cursor: int = Field(ge=1)
    recipient_id: str = Field(min_length=1, max_length=256)
    event: EventEnvelope
    current_fact: DeliveryFact


class WorkflowStartRequestV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    workflow_type: str = Field(min_length=1, max_length=256)
    workflow_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(min_length=1, max_length=256)
    actor: VerifiedActor
    parent_event_id: str = Field(min_length=1, max_length=256)
    task_grant_id: str = Field(min_length=1, max_length=256)
    input_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    encrypted_input_ref: str = Field(min_length=1, max_length=2048)
    policy_revision: int = Field(ge=1)
    expires_at: int = Field(gt=0)


class WorkflowStartResultV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    workflow_id: str = Field(min_length=1, max_length=256)
    workflow_run_id: str = Field(min_length=1, max_length=256)
    state: Literal[WorkflowState.PENDING, WorkflowState.RUNNING]
    accepted_at: int = Field(gt=0)


class WorkflowSignalV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    workflow_id: str = Field(min_length=1, max_length=256)
    workflow_run_id: str = Field(min_length=1, max_length=256)
    signal_id: str = Field(min_length=16, max_length=256)
    signal: Literal["cancel", "reconcile"]
    actor: VerifiedActor
    payload_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    issued_at: int = Field(gt=0)


class WorkflowSignalResultV1(StrictInterfaceModel):
    schema_version: Literal["1.0"]
    workflow_id: str = Field(min_length=1, max_length=256)
    workflow_run_id: str = Field(min_length=1, max_length=256)
    signal_id: str = Field(min_length=16, max_length=256)
    duplicate: bool


@runtime_checkable
class PolicyDecisionPoint(Protocol):
    def authorize(self, *, request: AuthorizationRequest) -> AuthorizationDecision: ...


@runtime_checkable
class ArtifactStore(Protocol):
    def put_quarantine(
        self,
        object_key: str,
        source: Path,
        *,
        expected_digest: str,
    ) -> ArtifactStoredVersionV1: ...

    def open_version(self, object_key: str, version: str) -> BinaryIO: ...

    def delete_version(self, object_key: str, version: str) -> None: ...


@runtime_checkable
class MailboxCustodian(Protocol):
    def accept(self, event: EventEnvelope) -> MailboxAcceptanceV1: ...

    def reconcile(
        self,
        recipient_id: str,
        cursor: int,
        limit: int,
    ) -> tuple[MailboxReconciliationItemV1, ...]: ...


@runtime_checkable
class ApprovalVerifier(Protocol):
    def verify(
        self,
        *,
        canonical_transaction: bytes,
        approval: IndependentApprovalReceipt,
        expected_purpose: str,
        expected_domain_id: str,
        when: datetime,
    ) -> VerifiedIndependentApproval: ...


@runtime_checkable
class WorkloadIdentityProvider(Protocol):
    def verified_workload(
        self,
        transport_context: AuthenticatedSPIFFETransport,
    ) -> WorkloadIdentity: ...


@runtime_checkable
class WorkflowEngine(Protocol):
    def start(self, request: WorkflowStartRequestV1) -> WorkflowStartResultV1: ...

    def signal(self, request: WorkflowSignalV1) -> WorkflowSignalResultV1: ...


__all__ = [
    "ApprovalVerifier",
    "ArtifactStore",
    "ArtifactStoredVersionV1",
    "MailboxAcceptanceV1",
    "MailboxCustodian",
    "MailboxReconciliationItemV1",
    "PolicyDecisionPoint",
    "StrictInterfaceModel",
    "WorkflowEngine",
    "WorkflowSignalResultV1",
    "WorkflowSignalV1",
    "WorkflowStartRequestV1",
    "WorkflowStartResultV1",
    "WorkloadIdentityProvider",
]
