"""Fail-closed positive-human authorization policy.

The policy engine deliberately has no harness, session, device, relationship,
or workload entitlement source.  Those inputs are eligibility constraints only.
"""

from __future__ import annotations

import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any
from uuid import NAMESPACE_URL, uuid4, uuid5

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.bootstrap_plan import C0_REQUIRED_FACTS
from agentnet.authorization.communication_scope import COMMUNICATION_SCOPE_ACTIONS
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

_LOCAL_CONFORMANCE_C0_ACTIONS = COMMUNICATION_SCOPE_ACTIONS | frozenset(
    {"server_agent.relay.send"}
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


@dataclass(frozen=True, slots=True)
class C0GuardedOperation:
    """Core-created exact context for one S5 operation.

    Generic request models cannot carry this type.  Only the C0 service calls
    the internal transaction method that consumes it.
    """

    attempt_id: str
    operation_scope: str
    peer_harness_id: str | None
    classification: Classification
    payload_digest: str | None = None
    event_id: str | None = None
    envelope_digest: str | None = None
    causal_parent_event_id: str | None = None


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
        harness_id: str,
        action: str,
        resource: str,
        revision: int,
        now: int,
    ) -> tuple[str | None, str, frozenset[str] | None]:
        rows = connection.execute(
            """
            SELECT e.* FROM entitlements AS e
             WHERE e.domain_id=? AND e.principal_id=? AND e.action=?
               AND e.resource_pattern IN (?, '*')
               AND NOT EXISTS (
                   SELECT 1
                     FROM bootstrap_grant_plan_items AS i
                    WHERE i.entitlement_id=e.entitlement_id
               )
             ORDER BY CASE WHEN e.resource_pattern=? THEN 0 ELSE 1 END,
                      e.entitlement_id
            """,
            (domain_id, principal_id, action, resource, resource),
        ).fetchall()
        if not rows:
            return None, "no_positive_human_entitlement", None
        scope_harness_mismatch = False
        for row in rows:
            if int(row["revision"]) != revision:
                continue
            if row["revoked_at"] is not None:
                continue
            if row["expires_at"] is not None and int(row["expires_at"]) <= now:
                continue
            scope = connection.execute(
                """
                SELECT i.harness_id,s.owner_harness_id,s.fresh_harness_id,s.state,
                       s.domain_revocation_epoch,
                       d.revocation_epoch AS current_domain_revocation_epoch,
                       owner_h.status AS owner_harness_status,
                       fresh_h.status AS fresh_harness_status
                  FROM communication_scope_items AS i
                  JOIN communication_scopes AS s ON s.scope_id=i.scope_id
                  JOIN domains AS d ON d.domain_id=s.domain_id
                  JOIN harnesses AS owner_h
                    ON owner_h.harness_id=s.owner_harness_id
                  JOIN harnesses AS fresh_h
                    ON fresh_h.harness_id=s.fresh_harness_id
                 WHERE i.entitlement_id=?
                """,
                (row["entitlement_id"],),
            ).fetchone()
            if scope is not None:
                if (
                    scope["state"] != "committed"
                    or int(scope["domain_revocation_epoch"])
                    != int(scope["current_domain_revocation_epoch"])
                    or scope["harness_id"] != harness_id
                    or scope["owner_harness_status"] != "active"
                    or scope["fresh_harness_status"] != "active"
                ):
                    scope_harness_mismatch = True
                    continue
                return (
                    str(row["entitlement_id"]),
                    "positive_human_entitlement",
                    frozenset(
                        {
                            str(scope["owner_harness_id"]),
                            str(scope["fresh_harness_id"]),
                        }
                    ),
                )
            return str(row["entitlement_id"]), "positive_human_entitlement", None
        if scope_harness_mismatch:
            return None, "communication_scope_harness_mismatch", None
        if any(int(row["revision"]) != revision for row in rows):
            return None, "stale_positive_entitlement", None
        return None, "no_current_positive_entitlement", None

    @staticmethod
    def _communication_scope_request_allowed(
        connection: sqlite3.Connection,
        *,
        request: AuthorizationRequest,
        peer_harness_ids: frozenset[str],
        now: int,
    ) -> bool:
        if (
            len(peer_harness_ids) != 2
            or request.actor.harness_id not in peer_harness_ids
        ):
            return False

        def current_same_domain_harness(harness_id: Any) -> bool:
            if not isinstance(harness_id, str) or not harness_id:
                return False
            return (
                connection.execute(
                    """
                    SELECT 1
                      FROM harnesses AS h
                      JOIN principals AS p
                        ON p.principal_id=h.principal_id AND p.domain_id=h.domain_id
                      JOIN domains AS d ON d.domain_id=h.domain_id
                      JOIN credentials AS c
                        ON c.harness_id=h.harness_id AND c.epoch=h.credential_epoch
                     WHERE h.harness_id=? AND h.domain_id=?
                       AND h.status='active' AND p.status='active' AND d.status='active'
                       AND d.policy_revision=?
                       AND c.status='active' AND c.not_before<=? AND c.expires_at>?
                     LIMIT 1
                    """,
                    (
                        harness_id,
                        request.actor.domain_id,
                        request.policy_revision,
                        now,
                        now,
                    ),
                ).fetchone()
                is not None
            )

        def current_scope_peer(harness_id: Any) -> bool:
            return (
                isinstance(harness_id, str)
                and harness_id in peer_harness_ids
                and current_same_domain_harness(harness_id)
            )

        for key in (
            "harness_id",
            "responsible_harness_id",
            "recipient_harness_id",
            "to_harness_id",
        ):
            target = request.context.get(key)
            if target is not None and not current_scope_peer(target):
                return False
        released_artifact_count = request.context.get("released_artifact_count", 0)
        if (
            not isinstance(released_artifact_count, int)
            or isinstance(released_artifact_count, bool)
            or released_artifact_count != 0
        ):
            return False
        if request.action == "message.send":
            recipients = request.context.get("recipient_harness_ids")
            return (
                isinstance(recipients, list)
                and bool(recipients)
                and all(current_scope_peer(recipient) for recipient in recipients)
            )
        if request.action in {"mailbox.read", "mailbox.acknowledge", "room.create"}:
            return True
        if request.action == "conversation.create":
            members = request.context.get("member_harness_ids")
            return (
                isinstance(members, list)
                and bool(members)
                and all(current_scope_peer(member) for member in members)
            )
        if request.action.startswith("conversation."):
            if request.action not in COMMUNICATION_SCOPE_ACTIONS:
                return False
            if not request.resource.startswith("conversation:"):
                return False
            conversation_id = request.resource.removeprefix("conversation:")
            members = connection.execute(
                """
                SELECT harness_id FROM conversation_members
                 WHERE conversation_id=? AND status='active'
                """,
                (conversation_id,),
            ).fetchall()
            active = frozenset(str(row["harness_id"]) for row in members)
            return bool(active) and all(current_scope_peer(member) for member in active)
        if request.action in {"room.action", "room.read"}:
            members = connection.execute(
                """
                SELECT harness_id FROM room_members
                 WHERE room_id=? AND removed_sequence IS NULL
                """,
                (request.resource,),
            ).fetchall()
            active = frozenset(str(row["harness_id"]) for row in members)
            return bool(active) and all(current_scope_peer(member) for member in active)
        return False

    def _record_c0_decision(
        self,
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        policy_revision: int,
        context: C0GuardedOperation | dict[str, Any],
        entitlement_id: str | None,
        allowed: bool,
        reason: str,
        when: datetime,
    ) -> AuthorizationDecision:
        value = (
            {
                "attempt_id": context.attempt_id,
                "operation_scope": context.operation_scope,
                "peer_harness_id": context.peer_harness_id,
                "classification": context.classification.value,
                "payload_digest": context.payload_digest,
                "event_id": context.event_id,
                "envelope_digest": context.envelope_digest,
                "causal_parent_event_id": context.causal_parent_event_id,
            }
            if isinstance(context, C0GuardedOperation)
            else context
        )
        return self.recorder.record(
            connection,
            AuthorizationDecision(
                occurred_at=when,
                actor=actor,
                action=action,
                resource={"id": resource},
                context={
                    "c0_guard": value,
                    "entitlement_id": entitlement_id,
                    "positive_authority_id": actor.positive_authority_id,
                },
                allowed=allowed,
                reason=reason,
                policy_revision=policy_revision,
            ),
        )

    def _require_c0_operation_in_transaction(
        self,
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        context: C0GuardedOperation,
        when: datetime,
    ) -> AuthorizationDecision:
        """Authorize one exact S5 operation; generic policy never sees this path."""

        now = epoch_seconds(when)
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        revision = 0 if domain is None else int(domain["policy_revision"])
        denial, revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=when,
        )
        if denial is None and self.attenuation_policy is not None:
            denial = self.attenuation_policy.denial_reason(actor.binding_assurance)
        row = None
        if denial is None and actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            rows = connection.execute(
                """SELECT e.entitlement_id,e.revision,e.domain_id AS entitlement_domain_id,
                          e.principal_id AS entitlement_principal_id,
                          e.expires_at AS entitlement_expires_at,e.revoked_at,
                          i.item_kind,g.*,g.expires_at AS guard_expires_at,
                          a.state AS attempt_state,b.operation_scope,
                          b.actor_harness_id,b.peer_harness_id,
                          d.revocation_epoch AS current_revocation_epoch
                     FROM c0_pilot_attempts a
                     JOIN c0_plan_guards g ON g.guard_id=a.guard_id
                     JOIN domains d ON d.domain_id=g.domain_id
                     JOIN c0_plan_guard_entitlements b ON b.guard_id=g.guard_id
                     JOIN entitlements e ON e.entitlement_id=b.entitlement_id
                     JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id
                    WHERE a.attempt_id=? AND b.operation_scope=?
                      AND e.action=? AND e.resource_pattern=?""",
                (context.attempt_id, context.operation_scope, action, resource),
            ).fetchall()
            if len(rows) == 1:
                row = rows[0]
            else:
                denial = "c0_guard_context_mismatch"
        elif denial is None:
            denial = "actor_kind_has_no_positive_authority"

        expected: dict[str, tuple[str, str | None, str, str, str | None]] = {}
        if row is not None:
            expected = {
                "fresh_to_owner_send": (
                    str(row["fresh_harness_id"]), str(row["owner_harness_id"]),
                    "message.send", "direct", str(row["request_payload_digest"]),
                ),
                "owner_to_fresh_send": (
                    str(row["owner_harness_id"]), str(row["fresh_harness_id"]),
                    "message.send", "direct", str(row["reply_payload_digest"]),
                ),
                "owner_mailbox_read": (
                    str(row["owner_harness_id"]), None, "mailbox.read",
                    str(row["owner_harness_id"]), None,
                ),
                "owner_mailbox_acknowledge": (
                    str(row["owner_harness_id"]), None, "mailbox.acknowledge",
                    str(row["owner_harness_id"]), None,
                ),
                "fresh_mailbox_read": (
                    str(row["fresh_harness_id"]), None, "mailbox.read",
                    str(row["fresh_harness_id"]), None,
                ),
                "fresh_mailbox_acknowledge": (
                    str(row["fresh_harness_id"]), None, "mailbox.acknowledge",
                    str(row["fresh_harness_id"]), None,
                ),
            }
            binding = expected.get(context.operation_scope)
            if binding is None:
                denial = "c0_guard_context_mismatch"
            else:
                expected_actor, expected_peer, expected_action, expected_resource, expected_payload = binding
                checks = (
                    row["item_kind"] == "communication",
                    row["entitlement_domain_id"] == actor.domain_id,
                    row["entitlement_principal_id"] == actor.principal_id,
                    row["state"] == "active",
                    row["attempt_state"] == "active",
                    row["domain_id"] == actor.domain_id,
                    row["principal_id"] == actor.principal_id,
                    row["actor_harness_id"] == expected_actor == actor.harness_id,
                    row["peer_harness_id"] == expected_peer == context.peer_harness_id,
                    action == expected_action,
                    resource == expected_resource,
                    context.classification is Classification.C0_PUBLIC,
                    row["classification"] == "C0",
                    int(row["policy_revision"]) == revision == int(row["revision"]),
                    int(row["domain_revocation_epoch"]) == int(row["current_revocation_epoch"]),
                    row["revoked_at"] is None,
                    row["entitlement_expires_at"] is not None
                    and int(row["entitlement_expires_at"]) > now,
                    int(row["guard_expires_at"]) > now,
                    actor.credential_epoch
                    == (
                        int(row["owner_credential_epoch"])
                        if actor.harness_id == row["owner_harness_id"]
                        else int(row["fresh_credential_epoch"])
                    ),
                    context.payload_digest == expected_payload,
                )
                if not all(checks):
                    denial = "c0_guard_context_mismatch"
                if denial is None and context.operation_scope.endswith("send"):
                    direction = (
                        "request"
                        if context.operation_scope == "fresh_to_owner_send"
                        else "reply"
                    )
                    expected_event_id = str(
                        uuid5(
                            NAMESPACE_URL,
                            f"agentnet:c0-event:{context.attempt_id}:{direction}",
                        )
                    )
                    if context.event_id != expected_event_id:
                        denial = "c0_guard_context_mismatch"
                    remaining_column = (
                        "request_remaining_uses"
                        if context.operation_scope == "fresh_to_owner_send"
                        else "reply_remaining_uses"
                    )
                    if int(row[remaining_column]) != 1:
                        denial = "c0_guard_use_unavailable"
                if denial is None and context.operation_scope == "owner_to_fresh_send":
                    parent = connection.execute(
                        """SELECT event_id FROM c0_pilot_facts
                             WHERE attempt_id=? AND fact_kind='request_durable_custody'""",
                        (context.attempt_id,),
                    ).fetchone()
                    if parent is None or context.causal_parent_event_id != parent["event_id"]:
                        denial = "c0_guard_context_mismatch"
                if denial is None and not context.operation_scope.endswith("send"):
                    fact_kind = (
                        "request_durable_custody"
                        if context.operation_scope.startswith("owner_")
                        else "reply_durable_custody"
                    )
                    fact = connection.execute(
                        """SELECT event_id,envelope_digest FROM c0_pilot_facts
                             WHERE attempt_id=? AND fact_kind=?""",
                        (context.attempt_id, fact_kind),
                    ).fetchone()
                    if (
                        fact is None
                        or context.event_id != fact["event_id"]
                        or context.envelope_digest != fact["envelope_digest"]
                    ):
                        denial = "c0_guard_context_mismatch"

        allowed = denial is None and row is not None
        decision = self._record_c0_decision(
            connection,
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=revision,
            context=context,
            entitlement_id=None if row is None else str(row["entitlement_id"]),
            allowed=allowed,
            reason=(
                "authorized_by_exact_c0_guard_and_human_entitlement"
                if allowed else denial or "c0_guard_context_mismatch"
            ),
            when=when,
        )
        if not decision.allowed:
            raise AuthorizationError(decision.reason)
        return decision

    def _require_c0_cleanup_in_transaction(
        self,
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        attempt_id: str,
        when: datetime,
    ) -> tuple[tuple[str, int], ...]:
        """Authorize exact five-target cleanup after complete typed evidence."""

        now = epoch_seconds(when)
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        revision = 0 if domain is None else int(domain["policy_revision"])
        denial, revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=when,
        )
        attempt = connection.execute(
            """SELECT a.state AS attempt_state,g.*,d.revocation_epoch AS current_revocation_epoch
                 FROM c0_pilot_attempts a JOIN c0_plan_guards g ON g.guard_id=a.guard_id
                 JOIN domains d ON d.domain_id=g.domain_id
                WHERE a.attempt_id=?""",
            (attempt_id,),
        ).fetchone()
        if (
            denial is not None
            or attempt is None
            or actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.harness_id != attempt["fresh_harness_id"]
            or actor.principal_id != attempt["principal_id"]
            or attempt["attempt_state"] != "evidence_complete"
            or attempt["state"] != "active"
            or int(attempt["expires_at"]) <= now
            or int(attempt["policy_revision"]) != revision
            or int(attempt["domain_revocation_epoch"]) != int(attempt["current_revocation_epoch"])
        ):
            raise AuthorizationError(denial or "c0_cleanup_context_mismatch")
        fact_rows = connection.execute(
            "SELECT fact_kind FROM c0_pilot_facts WHERE attempt_id=?",
            (attempt_id,),
        ).fetchall()
        if {str(row["fact_kind"]) for row in fact_rows} != set(C0_REQUIRED_FACTS):
            raise AuthorizationError("c0_cleanup_evidence_incomplete")
        rows = connection.execute(
            """SELECT c.item_ordinal,c.entitlement_id AS target_entitlement_id,
                      c.action AS target_action,c.resource_pattern AS target_resource,
                      c.expires_at AS target_item_expires_at,
                      te.action AS target_entitlement_action,
                      te.resource_pattern AS target_entitlement_resource,
                      te.domain_id AS target_domain_id,te.principal_id AS target_principal_id,
                      te.expires_at AS target_entitlement_expires_at,
                      te.revoked_at AS target_revoked_at,
                      c.item_kind AS target_kind,r.entitlement_id AS revoke_entitlement_id,
                      r.action AS revoke_action,r.resource_pattern AS revoke_resource,
                      r.target_entitlement_id AS revoke_target_entitlement_id,
                      r.expires_at AS revoke_item_expires_at,
                      re.action AS revoke_entitlement_action,
                      re.resource_pattern AS revoke_entitlement_resource,
                      re.domain_id AS revoke_domain_id,re.principal_id AS revoke_principal_id,
                      re.revoked_at AS revoke_revoked_at,
                      re.expires_at AS revoke_expires_at,re.revision AS revoke_revision,
                      te.revision AS target_revision
                 FROM c0_pilot_attempts a
                 JOIN bootstrap_grant_plan_items c ON c.plan_id=a.plan_id
                 JOIN entitlements te ON te.entitlement_id=c.entitlement_id
                 JOIN bootstrap_grant_plan_items r
                   ON r.plan_id=c.plan_id AND r.item_ordinal=c.item_ordinal+5
                 JOIN entitlements re ON re.entitlement_id=r.entitlement_id
                WHERE a.attempt_id=? AND c.item_ordinal BETWEEN 1 AND 5
                ORDER BY c.item_ordinal""",
            (attempt_id,),
        ).fetchall()
        if len(rows) != 5:
            raise AuthorizationError("c0_cleanup_context_mismatch")
        targets: list[tuple[str, int]] = []
        expected_targets = (
            ("message.send", "direct"),
            ("mailbox.read", str(attempt["owner_harness_id"])),
            ("mailbox.acknowledge", str(attempt["owner_harness_id"])),
            ("mailbox.read", str(attempt["fresh_harness_id"])),
            ("mailbox.acknowledge", str(attempt["fresh_harness_id"])),
        )
        for row, expected_target in zip(rows, expected_targets, strict=True):
            resource = f"entitlement:{row['target_entitlement_id']}"
            valid = (
                row["target_kind"] == "communication"
                and (row["target_action"], row["target_resource"]) == expected_target
                and row["target_entitlement_action"] == row["target_action"]
                and row["target_entitlement_resource"] == row["target_resource"]
                and row["target_domain_id"] == actor.domain_id
                and row["target_principal_id"] == actor.principal_id
                and row["revoke_action"] == "authorization.entitlement.revoke"
                and row["revoke_resource"] == resource
                and row["revoke_entitlement_action"] == row["revoke_action"]
                and row["revoke_entitlement_resource"] == resource
                and row["revoke_domain_id"] == actor.domain_id
                and row["revoke_principal_id"] == actor.principal_id
                and row["revoke_target_entitlement_id"] == row["target_entitlement_id"]
                and row["revoke_revoked_at"] is None
                and int(row["revoke_item_expires_at"]) == int(row["revoke_expires_at"])
                and int(row["revoke_expires_at"]) > now
                and int(row["revoke_revision"]) == revision
                and row["target_revoked_at"] is None
                and int(row["target_item_expires_at"])
                == int(row["target_entitlement_expires_at"])
                and int(row["target_entitlement_expires_at"]) > now
                and int(row["target_revision"]) == revision
            )
            decision = self._record_c0_decision(
                connection,
                actor=actor,
                action="authorization.entitlement.revoke",
                resource=resource,
                policy_revision=revision,
                context={"attempt_id": attempt_id, "operation_scope": "exact_cleanup"},
                entitlement_id=str(row["revoke_entitlement_id"]),
                allowed=valid,
                reason=("authorized_by_exact_c0_cleanup_entitlement" if valid else "c0_cleanup_context_mismatch"),
                when=when,
            )
            if not decision.allowed:
                raise AuthorizationError(decision.reason)
            targets.append((str(row["target_entitlement_id"]), revision))
        if len({target for target, _revision in targets}) != 5:
            raise AuthorizationError("c0_cleanup_context_mismatch")
        return tuple(targets)

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
        communication_scope_peers: frozenset[str] | None = None
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
            entitlement_id, entitlement_reason, communication_scope_peers = (
                self._current_entitlement(
                    connection,
                    domain_id=request.actor.domain_id,
                    principal_id=request.actor.principal_id or "",
                    harness_id=request.actor.harness_id or "",
                    action=request.action,
                    resource=request.resource,
                    revision=policy_revision,
                    now=now,
                )
            )
            if entitlement_id is None:
                denial = entitlement_reason
        if (
            denial is None
            and communication_scope_peers is not None
            and not self._communication_scope_request_allowed(
                connection,
                request=request,
                peer_harness_ids=communication_scope_peers,
                now=now,
            )
        ):
            denial = "communication_scope_request_mismatch"

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

    def current_policy_revision(self, actor: VerifiedActor, *, when: datetime | None = None) -> int:
        """Resolve a lab actor revision without promoting its harness to active."""

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
                allow_deterministic_only=bool(
                    actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
                    and actor.binding_assurance == "lab"
                ),
            )
            if denial is not None:
                raise AuthorizationError(denial)
            return current

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
