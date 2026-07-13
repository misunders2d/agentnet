"""Exact-transaction, non-self-approved temporary elevation grants.

Approval cryptography is verified here through a preconfigured independent
approval verifier. Caller-asserted ``verified=True`` values are rejected.
"""

from __future__ import annotations

from collections.abc import Mapping
from datetime import UTC, datetime, timedelta
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_approver_entitlement,
    require_current_authority_decision,
)
from agentnet.authorization.grants import TaskGrantService, epoch_seconds
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import ElevationPolicy
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.signatures import canonical_digest, canonical_json


class ElevationRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str = Field(min_length=1)
    beneficiary_authority_id: str = Field(min_length=1)
    harness_id: str = Field(min_length=1)
    actions: frozenset[str] = Field(min_length=1)
    resources: frozenset[str] = Field(min_length=1)
    input_sources: frozenset[str] = Field(min_length=1)
    output_sinks: frozenset[str] = Field(min_length=1)
    data_classes: frozenset[Classification] = Field(min_length=1)
    max_uses: int = Field(default=1, ge=1)
    approval_threshold: int = Field(default=1, ge=1)
    risk_class: Literal["ordinary", "high_impact", "break_glass"] = "ordinary"
    expires_at: datetime
    reason: str = Field(min_length=1)

    @model_validator(mode="after")
    def require_high_risk_threshold(self) -> "ElevationRequest":
        if self.risk_class in {"high_impact", "break_glass"} and self.approval_threshold < 2:
            raise ValueError("high-impact and break-glass elevation require at least two approvers")
        if self.risk_class == "break_glass" and self.max_uses != 1:
            raise ValueError("break-glass elevation must be single-use")
        return self

    def canonical_transaction(self) -> dict[str, object]:
        return {
            "type": "temporary_elevation",
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "beneficiary_authority_id": self.beneficiary_authority_id,
            "harness_id": self.harness_id,
            "actions": sorted(self.actions),
            "resources": sorted(self.resources),
            "input_sources": sorted(self.input_sources),
            "output_sinks": sorted(self.output_sinks),
            "data_classes": sorted(value.value for value in self.data_classes),
            "max_uses": self.max_uses,
            "approval_threshold": self.approval_threshold,
            "risk_class": self.risk_class,
            "expires_at": self.expires_at.isoformat(),
            "reason": self.reason,
        }

    @property
    def transaction_digest(self) -> str:
        return canonical_digest(self.canonical_transaction())


class VerifiedElevationApproval(BaseModel):
    """Legacy caller assertion retained only so old callers fail explicitly."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    approver_principal_id: str = Field(min_length=1)
    transaction_digest: str = Field(min_length=64, max_length=64)
    verified: bool
    verified_at: datetime
    expires_at: datetime


class ElevationService:
    APPROVAL_PURPOSE = "authorization.elevation.approve"

    def __init__(
        self,
        grants: TaskGrantService,
        approval_verifier: IndependentApprovalVerifier | None = None,
        *,
        policy: ElevationPolicy | None = None,
        outage_gate: OutageGate | None = None,
    ) -> None:
        self.grants = grants
        self.approval_verifier = approval_verifier
        self.policy = policy
        self.outage_gate = outage_gate

    @staticmethod
    def authority_binding(request: ElevationRequest) -> tuple[str, dict[str, str]]:
        return (
            f"elevation:{request.request_id}",
            {"request_digest": request.transaction_digest},
        )

    def issue(
        self,
        request: ElevationRequest,
        *,
        beneficiary: VerifiedActor,
        authority: IssuanceAuthority | None = None,
        approvals: tuple[Mapping[str, Any] | VerifiedElevationApproval, ...],
        when: datetime | None = None,
    ) -> TaskGrant:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
            self.outage_gate.require_privileged()
        if epoch_seconds(request.expires_at) <= now:
            raise ValidationError("elevation must expire in the future")
        if beneficiary.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError("only a human or host guest may receive positive elevation")
        if (
            beneficiary.domain_id != request.domain_id
            or beneficiary.positive_authority_id != request.beneficiary_authority_id
            or beneficiary.harness_id != request.harness_id
        ):
            raise AuthorizationError("elevation beneficiary binding mismatch")
        if request.risk_class == "break_glass" and request.expires_at > when + timedelta(minutes=15):
            raise ValidationError("break-glass elevation cannot exceed fifteen minutes")
        if self.policy is not None:
            required_threshold = self.policy.threshold_for(request.risk_class)
            if request.approval_threshold < required_threshold:
                raise ValidationError("elevation approval threshold is below the configured policy")
            if request.max_uses > self.policy.maximum_uses:
                raise ValidationError("elevation use budget exceeds the configured policy")
            if request.expires_at > when + timedelta(seconds=self.policy.ttl_for(request.risk_class)):
                raise ValidationError("elevation lifetime exceeds the configured policy")
            if request.risk_class == "break_glass" and not self.policy.break_glass_enabled:
                raise AuthorizationError("break-glass elevation is disabled by policy")
        if self.approval_verifier is None:
            raise AuthorizationError("independent approval verifier is required for elevation")
        if authority is None or authority.actor.audit_view() != beneficiary.audit_view():
            raise AuthorizationError("elevation requester must be the exact authenticated beneficiary actor")

        canonical_transaction = canonical_json(request.canonical_transaction())
        verified_approvals = []
        for approval in approvals:
            if isinstance(approval, VerifiedElevationApproval):
                raise AuthorizationError("caller-asserted verified elevation approvals are not accepted")
            verified_approvals.append(
                self.approval_verifier.verify(
                    canonical_transaction=canonical_transaction,
                    approval=approval,
                    expected_purpose=self.APPROVAL_PURPOSE,
                    expected_domain_id=request.domain_id,
                    when=when,
                )
            )

        approvers = [receipt.approver_principal_id for receipt in verified_approvals]
        if request.beneficiary_authority_id in approvers:
            raise AuthorizationError("beneficiary cannot approve its own elevation")
        if len(set(approvers)) != len(approvers):
            raise AuthorizationError("duplicate approver cannot satisfy the threshold")
        if len(approvers) < request.approval_threshold:
            raise AuthorizationError("independent approval threshold was not met")

        grant = TaskGrant(
            grant_id=request.request_id,
            domain_id=request.domain_id,
            principal_id=request.beneficiary_authority_id,
            harness_id=request.harness_id,
            actions=request.actions,
            resources=request.resources,
            input_sources=request.input_sources,
            output_sinks=request.output_sinks,
            data_classes=request.data_classes,
            max_uses=request.max_uses,
            expires_at=request.expires_at,
        )
        resource, expected_request = self.authority_binding(request)
        with self.grants.store.transaction() as connection:
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="authorization.elevation.request",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            for receipt in verified_approvals:
                require_current_approver_entitlement(
                    connection,
                    domain_id=request.domain_id,
                    approver_principal_id=receipt.approver_principal_id,
                    action=self.APPROVAL_PURPOSE,
                    resource=resource,
                    policy_revision=policy_revision,
                    when=when,
                )
                consume_independent_approval(connection, receipt=receipt)
            return self.grants._insert_in_transaction(
                connection,
                grant=grant,
                when=when,
                issuance_evidence={
                    "kind": "independent_elevation_approval",
                    "request_policy_decision_id": authority.policy_decision_id,
                    "policy_revision": policy_revision,
                    "approval_receipt_ids": [receipt.receipt_id for receipt in verified_approvals],
                    "approver_principal_ids": approvers,
                    "risk_class": request.risk_class,
                },
            )
