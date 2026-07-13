"""Threshold-approved, accountable bounds for unattended registered workloads.

The charter is an additional necessary condition.  It never replaces a current
task grant, data-class/sink decision, or effect authorization.
"""

from __future__ import annotations

import json
from collections.abc import Sequence
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.approval.service import (
    IndependentApprovalReceipt,
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_approver_entitlement,
    require_current_authority_decision,
)
from agentnet.authorization.grants import epoch_seconds
from agentnet.authorization.policy import validate_actor_state
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.post_audit_schema import require_post_audit_schema


AUTOMATION_CHARTER_APPROVAL_PURPOSE = "automation.charter.approve"


class AutomationCharter(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    charter_id: str = Field(default_factory=lambda: str(uuid4()), min_length=16, max_length=128)
    domain_id: str = Field(min_length=1, max_length=256)
    accountable_principal_id: str = Field(min_length=1, max_length=256)
    accountable_harness_id: str = Field(min_length=1, max_length=256)
    workload_registration_id: str = Field(min_length=16, max_length=128)
    workload_id: str = Field(min_length=1, max_length=256)
    triggers: frozenset[str] = Field(min_length=1, max_length=64)
    actions: frozenset[str] = Field(min_length=1, max_length=256)
    resources: frozenset[str] = Field(min_length=1, max_length=512)
    output_sinks: frozenset[str] = Field(min_length=1, max_length=256)
    data_classes: frozenset[Classification] = Field(min_length=1)
    max_runtime_seconds: int = Field(ge=1, le=86_400)
    max_fanout: int = Field(ge=1, le=10_000)
    max_spend_micros: int = Field(ge=0, le=10**15)
    use_limit: int = Field(ge=1, le=1_000_000)
    approval_threshold: int = Field(ge=1, le=5)
    expires_at: datetime
    reason: str = Field(min_length=1, max_length=1024)

    @field_validator("expires_at")
    @classmethod
    def aware_expiry(cls, value: datetime) -> datetime:
        if value.tzinfo is None or value.utcoffset() is None:
            raise ValueError("automation charter expiry must be timezone-aware")
        return value

    @field_validator("triggers", "actions", "resources", "output_sinks")
    @classmethod
    def canonical_values(cls, values: frozenset[str]) -> frozenset[str]:
        if any(
            not value
            or value != value.strip()
            or len(value) > 1024
            or any(ord(character) < 32 for character in value)
            for value in values
        ):
            raise ValueError("automation charter values must be bounded canonical text")
        return values

    @field_validator("reason")
    @classmethod
    def canonical_reason(cls, value: str) -> str:
        if value != value.strip() or any(ord(character) < 32 for character in value):
            raise ValueError("automation charter reason must be canonical text")
        return value

    def canonical_transaction(self) -> dict[str, Any]:
        return {
            "type": "automation_charter",
            "schema_version": self.schema_version,
            "charter_id": self.charter_id,
            "domain_id": self.domain_id,
            "accountable_principal_id": self.accountable_principal_id,
            "accountable_harness_id": self.accountable_harness_id,
            "workload_registration_id": self.workload_registration_id,
            "workload_id": self.workload_id,
            "triggers": sorted(self.triggers),
            "actions": sorted(self.actions),
            "resources": sorted(self.resources),
            "output_sinks": sorted(self.output_sinks),
            "data_classes": sorted(value.value for value in self.data_classes),
            "budgets": {
                "max_runtime_seconds": self.max_runtime_seconds,
                "max_fanout": self.max_fanout,
                "max_spend_micros": self.max_spend_micros,
                "use_limit": self.use_limit,
            },
            "approval_threshold": self.approval_threshold,
            "expires_at": self.expires_at.isoformat(),
            "reason": self.reason,
        }

    @property
    def digest(self) -> str:
        return canonical_digest(self.canonical_transaction())


class AutomationCharterRecord(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    charter: AutomationCharter
    state: Literal["proposed", "active", "revoked", "expired", "emergency_stopped"]
    revision: int
    policy_revision: int
    domain_revocation_epoch: int
    workload_credential_epoch: int
    use_count: int
    approval_set_digest: str
    created_at: datetime
    updated_at: datetime
    activated_at: datetime | None = None
    revoked_at: datetime | None = None
    emergency_stopped_at: datetime | None = None


class AutomationInvocation(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    invocation_id: str = Field(min_length=16, max_length=128)
    charter_id: str = Field(min_length=16, max_length=128)
    workload_registration_id: str = Field(min_length=16, max_length=128)
    expected_charter_revision: int = Field(ge=1)
    expected_charter_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    trigger: str = Field(min_length=1, max_length=1024)
    action: str = Field(min_length=1, max_length=1024)
    resource: str = Field(min_length=1, max_length=1024)
    output_sink: str = Field(min_length=1, max_length=1024)
    data_class: Classification
    fanout: int = Field(ge=0, le=10_000)
    spend_micros: int = Field(ge=0, le=10**15)
    requested_runtime_seconds: int = Field(ge=1, le=86_400)
    parent_event_id: str = Field(min_length=1, max_length=256)
    task_grant_id: str = Field(min_length=1, max_length=256)
    policy_revision: int = Field(ge=1)

    @model_validator(mode="after")
    def canonical_text(self) -> "AutomationInvocation":
        if any(
            value != value.strip() or any(ord(character) < 32 for character in value)
            for value in (self.trigger, self.action, self.resource, self.output_sink)
        ):
            raise ValueError("automation invocation values must be canonical text")
        return self

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))


class AutomationInvocationCompletion(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    invocation_id: str = Field(min_length=16, max_length=128)
    charter_id: str = Field(min_length=16, max_length=128)
    workload_registration_id: str = Field(min_length=16, max_length=128)
    expected_intent_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    terminal_state: Literal["committed", "released", "failed"]
    result_digest: str = Field(pattern=r"^[0-9a-f]{64}$")


class AutomationInvocationReservation(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    use_id: str
    invocation_id: str
    charter_id: str
    charter_revision: int
    intent_digest: str
    state: Literal["reserved", "committed", "released", "failed"]
    duplicate: bool
    charter_boundary_satisfied: Literal[True] = True
    task_grant_still_required: Literal[True] = True
    data_access_authorized: Literal[False] = False
    effect_authorized: Literal[False] = False


class AutomationCharterService:
    APPROVAL_PURPOSE = AUTOMATION_CHARTER_APPROVAL_PURPOSE

    def __init__(
        self,
        store: StoreBackend,
        *,
        approval_verifier: IndependentApprovalVerifier | None = None,
        outage_gate: OutageGate | None = None,
    ) -> None:
        self.store = store
        self.approval_verifier = approval_verifier
        self.outage_gate = outage_gate
        require_post_audit_schema(store)

    @staticmethod
    def authority_binding(charter: AutomationCharter) -> tuple[str, dict[str, str]]:
        return f"automation-charter:{charter.charter_id}", {"charter_digest": charter.digest}

    @staticmethod
    def _from_row(row: Any) -> AutomationCharterRecord:
        try:
            charter = AutomationCharter.model_validate_json(
                str(row["canonical_charter_json"]), strict=True
            )
        except Exception as exc:
            raise AuthorizationError("stored automation charter is invalid") from exc
        if charter.digest != row["charter_digest"]:
            raise AuthorizationError("stored automation charter digest is invalid")

        def as_time(value: Any) -> datetime | None:
            return None if value is None else datetime.fromtimestamp(int(value), UTC)

        return AutomationCharterRecord(
            charter=charter,
            state=row["state"],
            revision=int(row["revision"]),
            policy_revision=int(row["policy_revision"]),
            domain_revocation_epoch=int(row["domain_revocation_epoch"]),
            workload_credential_epoch=int(row["workload_credential_epoch"]),
            use_count=int(row["use_count"]),
            approval_set_digest=row["approval_set_digest"],
            created_at=as_time(row["created_at"]),
            updated_at=as_time(row["updated_at"]),
            activated_at=as_time(row["activated_at"]),
            revoked_at=as_time(row["revoked_at"]),
            emergency_stopped_at=as_time(row["emergency_stopped_at"]),
        )

    @staticmethod
    def _workload(connection: Any, charter: AutomationCharter, *, now: int) -> Any:
        row = connection.execute(
            "SELECT * FROM workload_registrations WHERE registration_id=?",
            (charter.workload_registration_id,),
        ).fetchone()
        if (
            row is None
            or row["domain_id"] != charter.domain_id
            or row["workload_id"] != charter.workload_id
            or row["status"] != "active"
            or int(row["expires_at"]) <= now
        ):
            raise AuthorizationError("automation charter workload registration is not current")
        return row

    @staticmethod
    def _require_workload_actor(
        actor: VerifiedActor,
        workload: Any,
        *,
        charter: AutomationCharter,
        invocation: AutomationInvocation | None = None,
    ) -> None:
        if (
            actor.kind is not ActorKind.WORKLOAD
            or actor.binding_assurance != "workload_mtls"
            or actor.domain_id != charter.domain_id
            or actor.workload_registration_id != charter.workload_registration_id
            or actor.workload_id != charter.workload_id
            or actor.workload_role != workload["workload_role"]
            or actor.workload_process_id != int(workload["process_id"])
            or actor.workload_process_start_time != int(workload["process_start_time"])
            or actor.workload_session_id != workload["session_id"]
            or actor.workload_revocation_epoch != int(workload["revocation_epoch"])
            or actor.credential_id != workload["registration_id"]
            or actor.credential_epoch != int(workload["credential_epoch"])
            or actor.parent_event_id != workload["parent_event_id"]
            or actor.task_grant_id != workload["task_grant_id"]
        ):
            raise AuthorizationError("automation invocation workload identity is not exact")
        if invocation is not None and (
            invocation.workload_registration_id != actor.workload_registration_id
            or invocation.parent_event_id != actor.parent_event_id
            or invocation.task_grant_id != actor.task_grant_id
        ):
            raise AuthorizationError("automation invocation delegation binding is not exact")

    @staticmethod
    def _require_current_task_grant(
        connection: Any,
        *,
        invocation: AutomationInvocation,
        charter: AutomationCharter,
        now: int,
    ) -> None:
        row = connection.execute(
            "SELECT * FROM task_grants WHERE grant_id=?", (invocation.task_grant_id,)
        ).fetchone()
        if row is None:
            raise AuthorizationError("automation invocation requires its current task grant")
        try:
            grant = TaskGrant.model_validate_json(str(row["grant_json"]))
        except Exception as exc:
            raise AuthorizationError("automation invocation task grant is invalid") from exc
        binding_row = connection.execute(
            "SELECT value FROM metadata WHERE key=?",
            (f"authority-binding:task-grant:{invocation.task_grant_id}",),
        ).fetchone()
        domain = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?",
            (charter.domain_id,),
        ).fetchone()
        harness = connection.execute(
            "SELECT status,domain_id,credential_epoch FROM harnesses WHERE harness_id=?",
            (grant.harness_id,),
        ).fetchone()
        try:
            binding = json.loads(binding_row["value"]) if binding_row is not None else None
        except (TypeError, ValueError):
            binding = None
        if (
            row["domain_id"] != charter.domain_id
            or row["grant_id"] != grant.grant_id
            or int(row["max_uses"]) != grant.max_uses
            or row["revoked_at"] is not None
            or grant.revoked_at is not None
            or int(row["expires_at"]) <= now
            or epoch_seconds(grant.expires_at) <= now
            or int(row["uses"]) >= int(row["max_uses"])
            or invocation.action not in grant.actions
            or invocation.resource not in grant.resources
            or invocation.trigger not in grant.input_sources
            or invocation.output_sink not in grant.output_sinks
            or invocation.data_class not in grant.data_classes
            or not isinstance(binding, dict)
            or binding.get("schema") != "agentnet.task-grant.authority-binding.v1"
            or binding.get("grant_id") != grant.grant_id
            or binding.get("domain_id") != charter.domain_id
            or binding.get("principal_id") != grant.principal_id
            or binding.get("harness_id") != grant.harness_id
            or domain is None
            or domain["status"] != "active"
            or int(binding.get("policy_revision", 0)) != int(domain["policy_revision"])
            or harness is None
            or harness["status"] != "active"
            or harness["domain_id"] != charter.domain_id
            or int(binding.get("harness_credential_epoch", 0))
            != int(harness["credential_epoch"])
        ):
            raise AuthorizationError("automation invocation task grant is stale or out of scope")

    def propose(
        self,
        charter: AutomationCharter,
        *,
        authority: IssuanceAuthority,
        when: datetime | None = None,
    ) -> AutomationCharterRecord:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
            self.outage_gate.require_privileged()
        actor = authority.actor
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.domain_id != charter.domain_id
            or actor.principal_id != charter.accountable_principal_id
            or actor.harness_id != charter.accountable_harness_id
        ):
            raise AuthorizationError("automation charter requires its exact accountable human")
        if epoch_seconds(charter.expires_at) <= now:
            raise ValidationError("automation charter must expire in the future")
        resource, expected_request = self.authority_binding(charter)
        with self.store.transaction() as connection:
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="automation.charter.propose",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?", (charter.domain_id,)
            ).fetchone()
            if domain is None or domain["status"] != "active":
                raise AuthorizationError("automation charter domain is not active")
            workload = self._workload(connection, charter, now=now)
            if epoch_seconds(charter.expires_at) > int(workload["expires_at"]):
                raise ValidationError("automation charter outlives its workload credential")
            existing = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?", (charter.charter_id,)
            ).fetchone()
            if existing is not None:
                if existing["charter_digest"] != charter.digest:
                    raise IdempotencyConflict("automation charter identifier names different bytes")
                return self._from_row(existing)
            budgets = {
                "max_runtime_seconds": charter.max_runtime_seconds,
                "use_limit": charter.use_limit,
            }
            connection.execute(
                """INSERT INTO automation_charters(
                    charter_id,schema_version,domain_id,accountable_principal_id,
                    accountable_harness_id,workload_registration_id,workload_id,
                    triggers_json,actions_json,resources_json,sinks_json,data_classes_json,
                    budgets_json,max_fanout,max_spend_micros,approval_threshold,
                    approval_set_digest,proposer_actor_json,reason,policy_revision,
                    domain_revocation_epoch,workload_credential_epoch,use_limit,use_count,
                    state,revision,canonical_charter_json,charter_digest,expires_at,
                    created_at,updated_at,activated_at,revoked_at,emergency_stopped_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
                         'proposed',1,?,?,?,?,?,NULL,NULL,NULL)""",
                (
                    charter.charter_id,
                    charter.schema_version,
                    charter.domain_id,
                    charter.accountable_principal_id,
                    charter.accountable_harness_id,
                    charter.workload_registration_id,
                    charter.workload_id,
                    canonical_json(sorted(charter.triggers)).decode(),
                    canonical_json(sorted(charter.actions)).decode(),
                    canonical_json(sorted(charter.resources)).decode(),
                    canonical_json(sorted(charter.output_sinks)).decode(),
                    canonical_json(sorted(value.value for value in charter.data_classes)).decode(),
                    canonical_json(budgets).decode(),
                    charter.max_fanout,
                    charter.max_spend_micros,
                    charter.approval_threshold,
                    canonical_digest([]),
                    canonical_json(actor.audit_view()).decode(),
                    charter.reason,
                    policy_revision,
                    int(domain["revocation_epoch"]),
                    int(workload["credential_epoch"]),
                    charter.use_limit,
                    0,
                    canonical_json(charter.model_dump(mode="json")).decode(),
                    charter.digest,
                    epoch_seconds(charter.expires_at),
                    now,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "automation_charter.proposed",
                    "actor": actor.audit_view(),
                    "charter_digest": charter.digest,
                    "charter_id": charter.charter_id,
                    "workload_registration_id": charter.workload_registration_id,
                },
            )
            return self._from_row(
                connection.execute(
                    "SELECT * FROM automation_charters WHERE charter_id=?", (charter.charter_id,)
                ).fetchone()
            )

    def activate(
        self,
        *,
        actor: VerifiedActor,
        charter_id: str,
        expected_charter_digest: str,
        expected_revision: int,
        approvals: Sequence[IndependentApprovalReceipt],
        when: datetime | None = None,
    ) -> AutomationCharterRecord:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
            self.outage_gate.require_privileged()
        if self.approval_verifier is None:
            raise AuthorizationError("automation charter activation requires an independent verifier")
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?", (charter_id,)
            ).fetchone()
            if row is None:
                raise AuthorizationError("automation charter is unavailable")
            record = self._from_row(row)
            charter = record.charter
            if (
                row["state"] != "proposed"
                or record.revision != expected_revision
                or charter.digest != expected_charter_digest
            ):
                raise ConflictError("automation charter is not proposed at the expected revision")
            if (
                actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
                or actor.domain_id != charter.domain_id
                or actor.principal_id != charter.accountable_principal_id
                or actor.harness_id != charter.accountable_harness_id
            ):
                raise AuthorizationError("automation charter activation requires its accountable human")
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?", (charter.domain_id,)
            ).fetchone()
            workload = self._workload(connection, charter, now=now)
            denial, _current_revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=record.policy_revision,
                when=when,
            )
            if (
                denial is not None
                or domain is None
                or domain["status"] != "active"
                or int(domain["policy_revision"]) != record.policy_revision
                or int(domain["revocation_epoch"]) != record.domain_revocation_epoch
                or int(workload["credential_epoch"]) != record.workload_credential_epoch
                or epoch_seconds(charter.expires_at) <= now
            ):
                raise ConflictError("automation charter authority binding drifted")
            if not 1 <= len(approvals) <= 5:
                raise ValidationError("automation charter approval set is invalid")
            canonical = canonical_json(charter.canonical_transaction())
            verified = [
                self.approval_verifier.verify(
                    canonical_transaction=canonical,
                    approval=approval.model_dump(mode="python", by_alias=True),
                    expected_purpose=self.APPROVAL_PURPOSE,
                    expected_domain_id=charter.domain_id,
                    when=when,
                )
                for approval in approvals
            ]
            approver_ids = [receipt.approver_principal_id for receipt in verified]
            if charter.accountable_principal_id in approver_ids:
                raise AuthorizationError("accountable automation human cannot self-approve")
            if len(set(approver_ids)) != len(approver_ids):
                raise AuthorizationError("duplicate automation approver cannot satisfy threshold")
            if len(approver_ids) < charter.approval_threshold:
                raise AuthorizationError("automation charter approval threshold was not met")
            resource = f"automation-charter:{charter.charter_id}"
            for receipt, approval in zip(verified, approvals, strict=True):
                require_current_approver_entitlement(
                    connection,
                    domain_id=charter.domain_id,
                    approver_principal_id=receipt.approver_principal_id,
                    action=self.APPROVAL_PURPOSE,
                    resource=resource,
                    policy_revision=record.policy_revision,
                    when=when,
                )
                consume_independent_approval(
                    connection,
                    receipt=receipt,
                    retain_until=epoch_seconds(charter.expires_at),
                )
                receipt_data = approval.model_dump(mode="json", by_alias=True)
                receipt_json = canonical_json(receipt_data).decode()
                connection.execute(
                    """INSERT INTO automation_charter_approvals(
                        charter_id,receipt_id,receipt_digest,receipt_json,
                        approver_authority_kind,approver_authority_id,verifier_id,
                        signer_key_id,receipt_expires_at,consumed_at
                    ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                    (
                        charter.charter_id,
                        receipt.receipt_id,
                        canonical_digest(receipt_data),
                        receipt_json,
                        receipt.approver_authority_kind,
                        receipt.approver_principal_id,
                        receipt.verifier_id,
                        receipt.signer_key_id,
                        receipt.expires_at,
                        now,
                    ),
                )
            approval_set_digest = canonical_digest(
                [
                    {
                        "approver": receipt.approver_principal_id,
                        "receipt_id": receipt.receipt_id,
                        "signer_key_id": receipt.signer_key_id,
                    }
                    for receipt in sorted(verified, key=lambda item: item.receipt_id)
                ]
            )
            cursor = connection.execute(
                """UPDATE automation_charters
                      SET state='active',revision=revision+1,approval_set_digest=?,
                          activated_at=?,updated_at=?
                    WHERE charter_id=? AND state='proposed' AND revision=?
                      AND charter_digest=?""",
                (
                    approval_set_digest,
                    now,
                    now,
                    charter.charter_id,
                    expected_revision,
                    expected_charter_digest,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("automation charter activation raced with another mutation")
            self.store.append_audit(
                connection,
                {
                    "action": "automation_charter.activated",
                    "accountable_actor": actor.audit_view(),
                    "approval_set_digest": approval_set_digest,
                    "approver_principal_ids": sorted(approver_ids),
                    "charter_digest": charter.digest,
                    "charter_id": charter.charter_id,
                },
            )
            return self._from_row(
                connection.execute(
                    "SELECT * FROM automation_charters WHERE charter_id=?", (charter.charter_id,)
                ).fetchone()
            )

    @staticmethod
    def mutation_binding(
        *,
        charter_id: str,
        expected_revision: int,
        expected_charter_digest: str,
        reason: str,
        emergency: bool,
    ) -> tuple[str, dict[str, Any]]:
        return f"automation-charter:{charter_id}", {
            "charter_id": charter_id,
            "emergency": emergency,
            "expected_charter_digest": expected_charter_digest,
            "expected_revision": expected_revision,
            "reason": reason,
        }

    def stop(
        self,
        *,
        authority: IssuanceAuthority,
        charter_id: str,
        expected_revision: int,
        expected_charter_digest: str,
        reason: str,
        emergency: bool,
        when: datetime | None = None,
    ) -> AutomationCharterRecord:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if (
            not reason
            or len(reason) > 1024
            or reason != reason.strip()
            or any(ord(character) < 32 for character in reason)
        ):
            raise ValidationError("automation charter stop reason is invalid")
        resource, request = self.mutation_binding(
            charter_id=charter_id,
            expected_revision=expected_revision,
            expected_charter_digest=expected_charter_digest,
            reason=reason,
            emergency=emergency,
        )
        action = "automation.charter.emergency_stop" if emergency else "automation.charter.revoke"
        with self.store.transaction() as connection:
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=action,
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            row = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?", (charter_id,)
            ).fetchone()
            if row is None:
                raise AuthorizationError("automation charter is unavailable")
            record = self._from_row(row)
            if not emergency and (
                authority.actor.domain_id != record.charter.domain_id
                or authority.actor.positive_authority_id
                != record.charter.accountable_principal_id
                or authority.actor.harness_id != record.charter.accountable_harness_id
            ):
                raise AuthorizationError("automation charter revocation requires its accountable human")
            if emergency:
                cursor = connection.execute(
                    """UPDATE automation_charters
                          SET state='emergency_stopped',revision=revision+1,revoked_at=?,
                              emergency_stopped_at=?,updated_at=?
                        WHERE charter_id=? AND state='active' AND revision=?
                          AND charter_digest=?""",
                    (now, now, now, charter_id, expected_revision, expected_charter_digest),
                )
            else:
                cursor = connection.execute(
                    """UPDATE automation_charters
                          SET state='revoked',revision=revision+1,revoked_at=?,updated_at=?
                        WHERE charter_id=? AND state IN ('proposed','active') AND revision=?
                          AND charter_digest=?""",
                    (now, now, charter_id, expected_revision, expected_charter_digest),
                )
            if cursor.rowcount != 1:
                raise ConflictError("automation charter stop raced or is no longer active")
            connection.execute(
                """UPDATE automation_charter_uses
                      SET state='released',result_digest=?,updated_at=?,completed_at=?
                    WHERE charter_id=? AND state='reserved'""",
                (
                    canonical_digest(
                        {
                            "charter_id": charter_id,
                            "reason": reason,
                            "release": "emergency_stop" if emergency else "revoke",
                        }
                    ),
                    now,
                    now,
                    charter_id,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": (
                        "automation_charter.emergency_stopped"
                        if emergency
                        else "automation_charter.revoked"
                    ),
                    "actor": authority.actor.audit_view(),
                    "charter_id": charter_id,
                    "reason": reason,
                },
            )
            return self._from_row(
                connection.execute(
                    "SELECT * FROM automation_charters WHERE charter_id=?", (charter_id,)
                ).fetchone()
            )

    @staticmethod
    def _reservation_from_row(row: Any, *, duplicate: bool) -> AutomationInvocationReservation:
        return AutomationInvocationReservation(
            use_id=row["use_id"],
            invocation_id=row["invocation_id"],
            charter_id=row["charter_id"],
            charter_revision=int(row["charter_revision"]),
            intent_digest=row["intent_digest"],
            state=row["state"],
            duplicate=duplicate,
        )

    def reserve_invocation(
        self,
        *,
        actor: VerifiedActor,
        invocation: AutomationInvocation,
        when: datetime | None = None,
    ) -> AutomationInvocationReservation:
        """Atomically reserve one bounded charter use without granting task authority."""

        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?",
                (invocation.charter_id,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("automation charter is unavailable")
            record = self._from_row(row)
            charter = record.charter
            domain = connection.execute(
                "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
                (charter.domain_id,),
            ).fetchone()
            workload = self._workload(connection, charter, now=now)
            self._require_workload_actor(
                actor,
                workload,
                charter=charter,
                invocation=invocation,
            )
            if (
                domain is None
                or domain["status"] != "active"
                or int(domain["policy_revision"]) != record.policy_revision
                or int(domain["revocation_epoch"]) != record.domain_revocation_epoch
                or int(workload["credential_epoch"]) != record.workload_credential_epoch
                or invocation.policy_revision != record.policy_revision
                or invocation.expected_charter_revision != record.revision
                or invocation.expected_charter_digest != charter.digest
            ):
                raise ConflictError("automation charter binding drifted")

            existing = connection.execute(
                """SELECT * FROM automation_charter_uses
                     WHERE charter_id=? AND (invocation_id=? OR intent_digest=?)""",
                (charter.charter_id, invocation.invocation_id, invocation.digest),
            ).fetchone()
            if existing is not None:
                if (
                    existing["invocation_id"] != invocation.invocation_id
                    or existing["intent_digest"] != invocation.digest
                    or existing["intent_json"]
                    != canonical_json(invocation.model_dump(mode="json")).decode()
                ):
                    raise IdempotencyConflict(
                        "automation invocation identifier or intent names different bytes"
                    )
                return self._reservation_from_row(existing, duplicate=True)

            if row["state"] != "active" or epoch_seconds(charter.expires_at) <= now:
                raise AuthorizationError("automation charter is not active")
            if (
                invocation.trigger not in charter.triggers
                or invocation.action not in charter.actions
                or invocation.resource not in charter.resources
                or invocation.output_sink not in charter.output_sinks
                or invocation.data_class not in charter.data_classes
                or invocation.fanout > charter.max_fanout
                or invocation.spend_micros > charter.max_spend_micros
                or invocation.requested_runtime_seconds > charter.max_runtime_seconds
            ):
                raise AuthorizationError("automation invocation exceeds its exact charter")
            self._require_current_task_grant(
                connection,
                invocation=invocation,
                charter=charter,
                now=now,
            )

            use_id = str(uuid4())
            intent_json = canonical_json(invocation.model_dump(mode="json")).decode()
            cursor = connection.execute(
                """INSERT INTO automation_charter_uses(
                       use_id,charter_id,invocation_id,intent_digest,intent_json,
                       charter_revision,workload_credential_epoch,fanout,spend_micros,
                       state,result_digest,created_at,updated_at,completed_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,'reserved',NULL,?,?,NULL)
                   ON CONFLICT DO NOTHING""",
                (
                    use_id,
                    charter.charter_id,
                    invocation.invocation_id,
                    invocation.digest,
                    intent_json,
                    record.revision,
                    record.workload_credential_epoch,
                    invocation.fanout,
                    invocation.spend_micros,
                    now,
                    now,
                ),
            )
            if cursor.rowcount != 1:
                raced = connection.execute(
                    """SELECT * FROM automation_charter_uses
                         WHERE charter_id=? AND (invocation_id=? OR intent_digest=?)""",
                    (charter.charter_id, invocation.invocation_id, invocation.digest),
                ).fetchone()
                if (
                    raced is not None
                    and raced["invocation_id"] == invocation.invocation_id
                    and raced["intent_digest"] == invocation.digest
                    and raced["intent_json"] == intent_json
                ):
                    return self._reservation_from_row(raced, duplicate=True)
                raise IdempotencyConflict("automation invocation raced with conflicting bytes")
            counted = connection.execute(
                """UPDATE automation_charters
                      SET use_count=use_count+1,updated_at=?
                    WHERE charter_id=? AND state='active' AND revision=?
                      AND charter_digest=? AND expires_at>?
                      AND policy_revision=? AND domain_revocation_epoch=?
                      AND workload_credential_epoch=? AND use_count<use_limit""",
                (
                    now,
                    charter.charter_id,
                    record.revision,
                    charter.digest,
                    now,
                    record.policy_revision,
                    record.domain_revocation_epoch,
                    record.workload_credential_epoch,
                ),
            )
            if counted.rowcount != 1:
                raise ConflictError("automation charter use limit or lifecycle raced")
            self.store.append_audit(
                connection,
                {
                    "action": "automation_charter.invocation_reserved",
                    "charter_id": charter.charter_id,
                    "intent_digest": invocation.digest,
                    "invocation_id": invocation.invocation_id,
                    "task_grant_id": invocation.task_grant_id,
                    "workload_registration_id": invocation.workload_registration_id,
                },
            )
            return self._reservation_from_row(
                connection.execute(
                    "SELECT * FROM automation_charter_uses WHERE use_id=?", (use_id,)
                ).fetchone(),
                duplicate=False,
            )

    def finish_invocation(
        self,
        *,
        actor: VerifiedActor,
        completion: AutomationInvocationCompletion,
        when: datetime | None = None,
    ) -> AutomationInvocationReservation:
        """Record a terminal use fact; this does not assert the downstream effect succeeded."""

        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        with self.store.transaction() as connection:
            charter_row = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?",
                (completion.charter_id,),
            ).fetchone()
            if charter_row is None:
                raise AuthorizationError("automation charter is unavailable")
            record = self._from_row(charter_row)
            workload = self._workload(connection, record.charter, now=now)
            self._require_workload_actor(actor, workload, charter=record.charter)
            if completion.workload_registration_id != actor.workload_registration_id:
                raise AuthorizationError("automation completion workload identity is not exact")
            row = connection.execute(
                """SELECT * FROM automation_charter_uses
                     WHERE charter_id=? AND invocation_id=?""",
                (completion.charter_id, completion.invocation_id),
            ).fetchone()
            if row is None or row["intent_digest"] != completion.expected_intent_digest:
                raise ConflictError("automation invocation terminal binding is stale")
            if row["state"] != "reserved":
                if (
                    row["state"] == completion.terminal_state
                    and row["result_digest"] == completion.result_digest
                ):
                    return self._reservation_from_row(row, duplicate=True)
                raise ConflictError("automation invocation already has a different terminal fact")
            cursor = connection.execute(
                """UPDATE automation_charter_uses
                      SET state=?,result_digest=?,updated_at=?,completed_at=?
                    WHERE use_id=? AND state='reserved' AND intent_digest=?""",
                (
                    completion.terminal_state,
                    completion.result_digest,
                    now,
                    now,
                    row["use_id"],
                    completion.expected_intent_digest,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("automation invocation terminal update raced")
            self.store.append_audit(
                connection,
                {
                    "action": "automation_charter.invocation_finished",
                    "charter_id": completion.charter_id,
                    "intent_digest": completion.expected_intent_digest,
                    "invocation_id": completion.invocation_id,
                    "result_digest": completion.result_digest,
                    "terminal_state": completion.terminal_state,
                },
            )
            return self._reservation_from_row(
                connection.execute(
                    "SELECT * FROM automation_charter_uses WHERE use_id=?", (row["use_id"],)
                ).fetchone(),
                duplicate=False,
            )

    def get_for_owner(
        self,
        *,
        actor: VerifiedActor,
        charter_id: str,
        when: datetime | None = None,
    ) -> AutomationCharterRecord:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM automation_charters WHERE charter_id=?", (charter_id,)
            ).fetchone()
            if row is None:
                raise AuthorizationError("automation charter is not visible")
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?", (row["domain_id"],)
            ).fetchone()
            expected_revision = 0 if domain is None else int(domain["policy_revision"])
            denial, _current = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=expected_revision,
                when=when,
            )
            if (
                denial is not None
                or actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
                or actor.domain_id != row["domain_id"]
                or actor.principal_id != row["accountable_principal_id"]
                or actor.harness_id != row["accountable_harness_id"]
            ):
                raise AuthorizationError("automation charter is not visible")
            return self._from_row(row)

    def list_for_owner(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
        when: datetime | None = None,
    ) -> list[AutomationCharterRecord]:
        if type(limit) is not int or not 1 <= limit <= 100:
            raise ValidationError("automation charter list limit is invalid")
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?", (actor.domain_id,)
            ).fetchone()
            expected_revision = 0 if domain is None else int(domain["policy_revision"])
            denial, _current = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=expected_revision,
                when=when,
            )
            if denial is not None or actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
                raise AuthorizationError("automation charters are not visible")
            rows = connection.execute(
                """SELECT * FROM automation_charters
                     WHERE domain_id=? AND accountable_principal_id=?
                       AND accountable_harness_id=?
                     ORDER BY created_at DESC,charter_id LIMIT ?""",
                (actor.domain_id, actor.principal_id, actor.harness_id, limit),
            ).fetchall()
            return [self._from_row(row) for row in rows]

    def expire_due(self, *, when: datetime | None = None) -> int:
        """Expire proposed or active charters and release unstarted reservations."""

        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        expired = 0
        with self.store.transaction() as connection:
            rows = connection.execute(
                """SELECT charter_id,revision,charter_digest FROM automation_charters
                     WHERE state IN ('proposed','active') AND expires_at<=?
                     ORDER BY charter_id""",
                (now,),
            ).fetchall()
            for row in rows:
                cursor = connection.execute(
                    """UPDATE automation_charters
                          SET state='expired',revision=revision+1,revoked_at=?,updated_at=?
                        WHERE charter_id=? AND revision=? AND charter_digest=?
                          AND state IN ('proposed','active') AND expires_at<=?""",
                    (
                        now,
                        now,
                        row["charter_id"],
                        int(row["revision"]),
                        row["charter_digest"],
                        now,
                    ),
                )
                if cursor.rowcount != 1:
                    continue
                release_digest = canonical_digest(
                    {"charter_id": row["charter_id"], "release": "expiry"}
                )
                connection.execute(
                    """UPDATE automation_charter_uses
                          SET state='released',result_digest=?,updated_at=?,completed_at=?
                        WHERE charter_id=? AND state='reserved'""",
                    (release_digest, now, now, row["charter_id"]),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "automation_charter.expired",
                        "charter_id": row["charter_id"],
                        "charter_digest": row["charter_digest"],
                    },
                )
                expired += 1
        return expired


__all__ = [
    "AUTOMATION_CHARTER_APPROVAL_PURPOSE",
    "AutomationCharter",
    "AutomationCharterRecord",
    "AutomationCharterService",
    "AutomationInvocation",
    "AutomationInvocationCompletion",
    "AutomationInvocationReservation",
]
