"""Fail-closed positive-human authorization policy.

The policy engine deliberately has no harness, session, device, relationship,
or workload entitlement source.  Those inputs are eligibility constraints only.
"""

from __future__ import annotations

import sqlite3
from collections.abc import Callable
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.decision import AuthorizationDecision, DecisionRecorder
from agentnet.authorization.evidence import (
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_signed_authority_command,
)
from agentnet.authorization.grants import GrantUse, TaskGrantService, epoch_seconds
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.policy_defaults import AttenuationPolicy
from agentnet.protocol.models import Classification
from agentnet.storage.sqlite import SQLiteStore


class OperationClass(StrEnum):
    BUSINESS = "business"
    PRIVILEGED = "privileged"
    PROTECTED_READ = "protected_read"
    PROTECTED_EFFECT = "protected_effect"
    DISCLOSURE = "disclosure"
    CREDENTIAL_USE = "credential_use"


GRANT_REQUIRED_OPERATION_CLASSES = frozenset(
    {
        OperationClass.PROTECTED_READ,
        OperationClass.PROTECTED_EFFECT,
        OperationClass.DISCLOSURE,
        OperationClass.CREDENTIAL_USE,
    }
)

_LOCAL_CONFORMANCE_C0_ACTIONS = frozenset(
    {
        "conversation.create",
        "conversation.message.send",
        "conversation.structured_request.send",
        "conversation.task.cancel_request",
        "conversation.task.complete",
        "conversation.task.handoff",
        "conversation.task.request",
        "mailbox.read",
        "message.send",
        "server_agent.relay.send",
    }
)


class DenyOnlyEligibility(BaseModel):
    """Trusted eligibility inputs that can deny but can never authorize."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    harness_eligible: bool = True
    session_eligible: bool = True
    device_eligible: bool = True
    credential_fresh: bool = True
    capability_eligible: bool = True
    additional_denials: tuple[str, ...] = ()

    def denial_reason(self) -> str | None:
        checks = (
            (self.harness_eligible, "harness_ineligible"),
            (self.session_eligible, "session_ineligible"),
            (self.device_eligible, "device_ineligible"),
            (self.credential_fresh, "credential_not_fresh"),
            (self.capability_eligible, "capability_ineligible"),
        )
        for eligible, reason in checks:
            if not eligible:
                return reason
        if self.additional_denials:
            return f"eligibility_denied:{self.additional_denials[0]}"
        return None


class HumanEntitlement(BaseModel):
    """A positive entitlement belonging to a verified human principal."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    entitlement_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str = Field(min_length=1)
    principal_id: str = Field(min_length=1)
    action: str = Field(min_length=1)
    resource_pattern: str = Field(min_length=1)
    revision: int = Field(ge=1)
    expires_at: datetime | None = None
    revoked_at: datetime | None = None


class AuthorizationRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    actor: VerifiedActor
    action: str = Field(min_length=1)
    resource: str = Field(min_length=1)
    operation_class: OperationClass = OperationClass.BUSINESS
    classification: Classification | None = None
    policy_revision: int = Field(ge=1)
    context: dict[str, Any] = Field(default_factory=dict)
    eligibility: DenyOnlyEligibility = Field(default_factory=DenyOnlyEligibility)
    grant_use: GrantUse | None = None


def validate_actor_state(
    connection: sqlite3.Connection,
    *,
    actor: VerifiedActor,
    expected_policy_revision: int,
    when: datetime,
    allow_deterministic_only: bool = False,
) -> tuple[str | None, int]:
    """Return a denial reason and the authoritative policy revision."""

    now = epoch_seconds(when)
    domain = connection.execute("SELECT * FROM domains WHERE domain_id=?", (actor.domain_id,)).fetchone()
    if domain is None:
        return "missing_domain_state", 0
    policy_revision = int(domain["policy_revision"])
    if domain["status"] != "active":
        return "domain_not_active", policy_revision
    if expected_policy_revision != policy_revision:
        return "stale_policy_revision", policy_revision

    if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
        return "actor_kind_has_no_positive_authority", policy_revision

    harness = connection.execute("SELECT * FROM harnesses WHERE harness_id=?", (actor.harness_id,)).fetchone()
    if harness is None:
        return "missing_harness_state", policy_revision
    if harness["domain_id"] != actor.domain_id:
        return "harness_domain_mismatch", policy_revision
    allowed_harness_states = {"active", "deterministic_only"} if allow_deterministic_only else {"active"}
    if harness["status"] not in allowed_harness_states:
        return "harness_not_active", policy_revision
    if harness["binding_assurance"] != actor.binding_assurance:
        return "binding_assurance_mismatch", policy_revision
    if int(harness["credential_epoch"]) != actor.credential_epoch:
        return "stale_harness_credential_epoch", policy_revision

    if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
        principal = connection.execute("SELECT * FROM principals WHERE principal_id=?", (actor.principal_id,)).fetchone()
        if principal is None:
            return "missing_principal_state", policy_revision
        if principal["domain_id"] != actor.domain_id or harness["principal_id"] != actor.principal_id:
            return "principal_binding_mismatch", policy_revision
        if principal["status"] != "active":
            return "principal_not_active", policy_revision
    else:
        guest = connection.execute("SELECT * FROM guests WHERE guest_id=?", (actor.guest_id,)).fetchone()
        if guest is None:
            return "missing_guest_state", policy_revision
        if guest["host_domain_id"] != actor.domain_id or harness["guest_id"] != actor.guest_id:
            return "guest_binding_mismatch", policy_revision
        if guest["status"] != "active" or int(guest["expires_at"]) <= now:
            return "guest_not_active", policy_revision

    credential = connection.execute("SELECT * FROM credentials WHERE credential_id=?", (actor.credential_id,)).fetchone()
    if credential is None:
        return "missing_credential_state", policy_revision
    if credential["harness_id"] != actor.harness_id or int(credential["epoch"]) != actor.credential_epoch:
        return "credential_binding_mismatch", policy_revision
    if credential["status"] != "active":
        return "credential_not_active", policy_revision
    if int(credential["not_before"]) > now or int(credential["expires_at"]) <= now:
        return "credential_outside_validity", policy_revision
    return None, policy_revision


class PolicyEngine:
    """Production positive-authority engine over one SQLite revision snapshot.

    ``runtime_profile`` is retained solely to scope the separately implemented
    task-grant inspection helpers.  It never changes an authorization result.
    In particular, constructing this engine with the local profile does not
    enable synthetic identities, deterministic-only harnesses, or C0 actions.
    """

    def __init__(
        self,
        store: SQLiteStore,
        *,
        attenuation_policy: AttenuationPolicy | None = None,
        outage_gate: OutageGate | None = None,
        runtime_profile: RuntimeProfile = RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
    ) -> None:
        self.store = store
        self.recorder = DecisionRecorder(store)
        self.grants = TaskGrantService(
            store,
            runtime_profile=RuntimeProfile(runtime_profile),
        )
        self.attenuation_policy = attenuation_policy
        self.outage_gate = outage_gate

    def _is_synthetic_inert_c0(self, request: AuthorizationRequest) -> bool:
        """Return false in the production policy for every caller and profile."""

        del request
        return False

    def allows_local_conformance_conversation_harness(
        self,
        *,
        binding_assurance: str,
        classification: Classification,
    ) -> bool:
        """Production conversations never admit a synthetic lab harness."""

        del binding_assurance, classification
        return False

    @staticmethod
    def entitlement_issuance_binding(
        entitlement: HumanEntitlement,
        *,
        reason: str,
    ) -> tuple[str, dict[str, Any]]:
        resource = f"entitlement:{entitlement.entitlement_id}"
        request = {
            "schema": "agentnet.entitlement.issue.v1",
            "entitlement": entitlement.model_dump(mode="json"),
            "reason": reason,
        }
        return resource, request

    @staticmethod
    def entitlement_revocation_binding(
        entitlement_id: str,
        *,
        expected_entity_revision: int,
        reason: str,
    ) -> tuple[str, dict[str, Any]]:
        resource = f"entitlement:{entitlement_id}"
        request = {
            "schema": "agentnet.entitlement.revoke.v1",
            "entitlement_id": entitlement_id,
            "expected_entity_revision": expected_entity_revision,
            "reason": reason,
        }
        return resource, request

    def current_policy_revision(self, actor: VerifiedActor, *, when: datetime | None = None) -> int:
        """Resolve a current actor's coherent revision without trusting a caller default.

        The eventual operation still validates the same revision in its own
        transaction, so an intervening policy update fails closed as stale.
        """

        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("missing_domain_state")
            revision = int(row["policy_revision"])
            denial, current = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=revision,
                when=when,
            )
            if denial is not None:
                raise AuthorizationError(denial)
            return current

    def _insert_entitlement_in_transaction(
        self,
        connection: sqlite3.Connection,
        entitlement: HumanEntitlement,
        *,
        when: datetime,
        audit_record: dict[str, Any],
        require_future_expiry: bool = True,
    ) -> HumanEntitlement:
        principal = connection.execute(
            "SELECT * FROM principals WHERE principal_id=?", (entitlement.principal_id,)
        ).fetchone()
        domain = connection.execute("SELECT * FROM domains WHERE domain_id=?", (entitlement.domain_id,)).fetchone()
        if principal is None or principal["domain_id"] != entitlement.domain_id or principal["status"] != "active":
            raise ValidationError("positive entitlement requires an active human principal")
        if domain is None or domain["status"] != "active":
            raise ValidationError("positive entitlement domain is not active")
        if int(domain["policy_revision"]) != entitlement.revision:
            raise ConflictError("positive entitlement revision is not the current policy revision")
        if (
            require_future_expiry
            and entitlement.expires_at is not None
            and epoch_seconds(entitlement.expires_at) <= epoch_seconds(when)
        ):
            raise ValidationError("positive entitlement must expire in the future")
        if entitlement.revoked_at is not None:
            raise ValidationError("new positive entitlement cannot already be revoked")
        try:
            connection.execute(
                """
                INSERT INTO entitlements(
                    entitlement_id, domain_id, principal_id, action,
                    resource_pattern, expires_at, revoked_at, revision
                ) VALUES(?,?,?,?,?,?,?,?)
                """,
                (
                    entitlement.entitlement_id,
                    entitlement.domain_id,
                    entitlement.principal_id,
                    entitlement.action,
                    entitlement.resource_pattern,
                    epoch_seconds(entitlement.expires_at) if entitlement.expires_at else None,
                    None,
                    entitlement.revision,
                ),
            )
        except sqlite3.IntegrityError as exc:
            raise ConflictError("entitlement identifier already exists") from exc
        self.store.append_audit(connection, audit_record)
        return entitlement

    def add_entitlement(
        self,
        entitlement: HumanEntitlement,
        *,
        command: SignedAuthorityCommand | None = None,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> HumanEntitlement:
        """Issue positive authority from one signed, exact, current command."""

        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, expected_request = self.entitlement_issuance_binding(
                entitlement,
                reason=command.reason if command is not None else "missing",
            )
            revision = require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="authorization.entitlement.issue",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if command is None or authority is None:  # narrowed by verifier
                raise AuthorizationError("signed entitlement issuance authority is required")
            if authority.actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
                raise AuthorizationError("only a verified domain human may issue entitlements")
            if authority.actor.domain_id != entitlement.domain_id:
                raise AuthorizationError("entitlement issuer domain binding mismatch")
            if command.expected_entity_revision != 0:
                raise ConflictError("new entitlement expected entity revision must be zero")
            if entitlement.revision != revision:
                raise ConflictError("entitlement policy revision changed before commit")
            begin_authority_mutation_intent(connection, command=command, authority=authority, when=when)
            issued = self._insert_entitlement_in_transaction(
                connection,
                entitlement,
                when=when,
                audit_record={
                    "type": "human_entitlement_issued",
                    "entitlement": entitlement.model_dump(mode="json"),
                    "issuance_actor": authority.actor.audit_view(),
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": revision,
                    "command_id": command.command_id,
                    "reason": command.reason,
                },
            )
            complete_authority_mutation_intent(connection, command_id=command.command_id, when=when)
            return issued

    def revoke_entitlement(
        self,
        entitlement_id: str,
        *,
        command: SignedAuthorityCommand | None = None,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> bool:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, expected_request = self.entitlement_revocation_binding(
                entitlement_id,
                expected_entity_revision=command.expected_entity_revision if command is not None else 0,
                reason=command.reason if command is not None else "missing",
            )
            revision = require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="authorization.entitlement.revoke",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if command is None or authority is None:  # narrowed by verifier
                raise AuthorizationError("signed entitlement revocation authority is required")
            if authority.actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
                raise AuthorizationError("only a verified domain human may revoke entitlements")
            row = connection.execute(
                "SELECT * FROM entitlements WHERE entitlement_id=?",
                (entitlement_id,),
            ).fetchone()
            if row is None or row["domain_id"] != authority.actor.domain_id:
                raise ConflictError("entitlement revision changed before revocation")
            if row["revoked_at"] is not None or int(row["revision"]) != command.expected_entity_revision:
                raise ConflictError("entitlement revision changed before revocation")
            begin_authority_mutation_intent(connection, command=command, authority=authority, when=when)
            cursor = connection.execute(
                """
                UPDATE entitlements SET revoked_at=?
                 WHERE entitlement_id=? AND revision=? AND revoked_at IS NULL
                """,
                (epoch_seconds(when), entitlement_id, command.expected_entity_revision),
            )
            if cursor.rowcount != 1:
                raise ConflictError("entitlement revocation raced with another lifecycle mutation")
            self.store.append_audit(
                connection,
                {
                    "type": "human_entitlement_revoked",
                    "entitlement_id": entitlement_id,
                    "revoked_at": when.isoformat(),
                    "revocation_actor": authority.actor.audit_view(),
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": revision,
                    "command_id": command.command_id,
                    "reason": command.reason,
                },
            )
            complete_authority_mutation_intent(connection, command_id=command.command_id, when=when)
            return True

    @staticmethod
    def _current_entitlement(
        connection: sqlite3.Connection,
        *,
        domain_id: str,
        principal_id: str,
        action: str,
        resource: str,
        revision: int,
        now: int,
    ) -> tuple[str | None, str]:
        rows = connection.execute(
            """
            SELECT * FROM entitlements
             WHERE domain_id=? AND principal_id=? AND action=?
               AND resource_pattern IN (?, '*')
             ORDER BY CASE WHEN resource_pattern=? THEN 0 ELSE 1 END, entitlement_id
            """,
            (domain_id, principal_id, action, resource, resource),
        ).fetchall()
        if not rows:
            return None, "no_positive_human_entitlement"
        for row in rows:
            if int(row["revision"]) != revision:
                continue
            if row["revoked_at"] is not None:
                continue
            if row["expires_at"] is not None and int(row["expires_at"]) <= now:
                continue
            return str(row["entitlement_id"]), "positive_human_entitlement"
        if any(int(row["revision"]) != revision for row in rows):
            return None, "stale_positive_entitlement"
        return None, "no_current_positive_entitlement"

    def _decide_in_transaction(
        self,
        connection: sqlite3.Connection,
        request: AuthorizationRequest,
        *,
        when: datetime,
        phase_hook: Callable[[str], None] | None = None,
    ) -> AuthorizationDecision:
        """Evaluate, consume an exact grant, and record on one caller transaction."""

        now = epoch_seconds(when)
        entitlement_id: str | None = None
        grant_consumed = False
        grant_id = request.grant_use.grant_id if request.grant_use else None

        inert_local_c0 = self._is_synthetic_inert_c0(request)
        denial, policy_revision = validate_actor_state(
            connection,
            actor=request.actor,
            expected_policy_revision=request.policy_revision,
            when=when,
            allow_deterministic_only=inert_local_c0,
        )
        if denial is None:
            denial = request.eligibility.denial_reason()
        if denial is None and not inert_local_c0 and self.attenuation_policy is not None:
            denial = self.attenuation_policy.denial_reason(request.actor.binding_assurance)

        if denial is None and request.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            entitlement_id, entitlement_reason = self._current_entitlement(
                connection,
                domain_id=request.actor.domain_id,
                principal_id=request.actor.principal_id or "",
                action=request.action,
                resource=request.resource,
                revision=policy_revision,
                now=now,
            )
            if entitlement_id is None:
                denial = entitlement_reason

        requires_grant = (
            request.actor.kind is ActorKind.HOST_GUEST_HARNESS
            or request.operation_class in GRANT_REQUIRED_OPERATION_CLASSES
        )
        if denial is None and requires_grant and request.grant_use is None:
            denial = "exact_task_grant_required"

        if denial is None and request.grant_use is not None:
            if request.grant_use.action != request.action or request.grant_use.resource != request.resource:
                denial = "task_grant_request_mismatch"
            else:
                consumption = self.grants._consume_exact(
                    connection,
                    actor=request.actor,
                    use=request.grant_use,
                    when=when,
                )
                if not consumption.allowed:
                    denial = consumption.reason
                else:
                    grant_consumed = consumption.consumed
                    if grant_consumed and phase_hook is not None:
                        phase_hook("after_grant_consumed")

        allowed = denial is None
        reason = "authorized_by_human_entitlement_and_current_constraints" if allowed else denial or "denied"
        decision = AuthorizationDecision(
            occurred_at=when,
            actor=request.actor,
            action=request.action,
            resource={"id": request.resource},
            context={
                "request": request.context,
                "operation_class": request.operation_class.value,
                "eligibility": request.eligibility.model_dump(mode="json"),
                "entitlement_id": entitlement_id,
                "task_grant_id": grant_id,
                "task_grant_consumed": grant_consumed,
                "positive_authority_id": request.actor.positive_authority_id,
            },
            allowed=allowed,
            reason=reason,
            policy_revision=policy_revision,
        )
        recorded = self.recorder.record(connection, decision)
        if phase_hook is not None:
            phase_hook("after_decision_recorded")
        return recorded

    def decide(self, request: AuthorizationRequest, *, when: datetime | None = None) -> AuthorizationDecision:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            return self._decide_in_transaction(connection, request, when=when)

    def require(self, request: AuthorizationRequest, *, when: datetime | None = None) -> AuthorizationDecision:
        decision = self.decide(request, when=when)
        if not decision.allowed:
            raise AuthorizationError(decision.reason)
        return decision


class LocalConformancePolicyEngine(PolicyEngine):
    """Explicit lab-only policy used by local conformance and test fixtures.

    Its synthetic allowance is deliberately tiny: a current verified human lab
    harness, an inert business action from the fixed allowlist, and C0 bytes.
    Protected reads/effects, disclosures, credential use, semantic processing,
    and every non-C0 classification continue through the production deny path.
    """

    def __init__(
        self,
        store: SQLiteStore,
        *,
        attenuation_policy: AttenuationPolicy | None = None,
        outage_gate: OutageGate | None = None,
    ) -> None:
        super().__init__(
            store,
            attenuation_policy=attenuation_policy,
            outage_gate=outage_gate,
            runtime_profile=RuntimeProfile.LOCAL_CONFORMANCE,
        )

    def _is_synthetic_inert_c0(self, request: AuthorizationRequest) -> bool:
        return bool(
            request.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
            and request.actor.binding_assurance == "lab"
            and request.classification is Classification.C0_PUBLIC
            and request.operation_class is OperationClass.BUSINESS
            and request.action in _LOCAL_CONFORMANCE_C0_ACTIONS
        )

    def allows_local_conformance_conversation_harness(
        self,
        *,
        binding_assurance: str,
        classification: Classification,
    ) -> bool:
        return bool(
            binding_assurance == "lab"
            and classification is Classification.C0_PUBLIC
        )

    def bootstrap_entitlement_for_local_conformance(
        self,
        entitlement: HumanEntitlement,
        *,
        when: datetime | None = None,
    ) -> HumanEntitlement:
        """Seed a local lab entitlement without creating a production API."""

        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            return self._insert_entitlement_in_transaction(
                connection,
                entitlement,
                when=when,
                audit_record={
                    "type": "local_conformance_entitlement_bootstrapped",
                    "entitlement": entitlement.model_dump(mode="json"),
                    "non_production": True,
                },
                require_future_expiry=False,
            )
