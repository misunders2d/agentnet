"""Bilateral, proof-bound administrator relationship governance.

An administrator may create a durable proposal, but a proposal is never an
authority-bearing edge.  Activation requires either a fresh independent
approval from the authoritative current owner of the subordinate endpoint or
an exact, separately signed and recorded domain-policy exception.  Version 18
stores these transactions outside the retired unilateral relationship table.
"""

from __future__ import annotations

import json
import secrets
import sqlite3
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_validator

from agentnet.approval.service import (
    IndependentApprovalReceipt,
    IndependentApprovalVerifier,
    VerifiedIndependentApproval,
    consume_independent_approval,
    independent_approval_replay_binding,
)
from agentnet.authorization.evidence import (
    AUTHORITY_COMMAND_PURPOSE,
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_current_authority_decision,
    require_signed_authority_command,
)
from agentnet.authorization.grants import epoch_seconds
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.protocol.models import (
    AssignmentScope,
    EmptyAssignmentScope,
    Relationship,
    TaskGrant,
)
from agentnet.security.signatures import (
    canonical_digest,
    canonical_json,
    verify_signature,
)
from agentnet.storage.backend import StoreBackend
from agentnet.storage.relationship_governance_schema import (
    require_relationship_governance_schema,
)


RELATIONSHIP_CONSENT_PURPOSE = "organization.relationship.accept"
RELATIONSHIP_PROPOSAL_SCHEMA = "agentnet.relationship-consent-transaction.v1"
RELATIONSHIP_POLICY_EXCEPTION_SCHEMA = "agentnet.relationship-policy-exception.v1"
RELATIONSHIP_ACTIVATION_INTENT_SCHEMA = "agentnet.relationship-activation-intent.v1"
RELATIONSHIP_ACTIVATION_INTENT_ACTION = "organization.relationship.activate"

OwnerKind = Literal["human", "guest"]
LifecycleState = Literal["proposed", "active", "rejected", "expired", "revoked", "superseded"]
ActivationBasis = Literal["subordinate_owner_consent", "domain_policy_exception"]


class RelationshipConsentTransaction(BaseModel):
    """Canonical bytes the subordinate owner independently approves."""

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        serialize_by_alias=True,
        strict=True,
    )

    schema_: Literal[RELATIONSHIP_PROPOSAL_SCHEMA] = Field(
        default=RELATIONSHIP_PROPOSAL_SCHEMA,
        alias="schema",
    )
    transaction_id: str = Field(min_length=1, max_length=128)
    relationship: Relationship
    proposal_expires_at: datetime
    administrator_owner_kind: OwnerKind
    administrator_owner_id: str = Field(min_length=1, max_length=256)
    subordinate_owner_kind: OwnerKind
    subordinate_owner_id: str = Field(min_length=1, max_length=256)
    policy_revision: int = Field(ge=1)
    domain_revocation_epoch: int = Field(ge=1)
    administrator_credential_epoch: int = Field(ge=1)
    subordinate_credential_epoch: int = Field(ge=1)
    lineage_revocation_epoch: int = Field(ge=0)
    predecessor_relationship_id: str | None = None
    predecessor_relationship_revision: int | None = Field(default=None, ge=1)
    predecessor_lifecycle_revision: int | None = Field(default=None, ge=0)
    predecessor_state: str | None = None
    proposed_at: datetime

    @field_validator("proposal_expires_at", "proposed_at")
    @classmethod
    def consent_times_are_aware(cls, value: datetime) -> datetime:
        if value.tzinfo is None or value.utcoffset() is None:
            raise ValueError("relationship consent timestamps must be timezone-aware")
        return value


class RelationshipGovernanceRecord(Relationship):
    """Authoritative relationship terms plus server-owned lifecycle evidence."""

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        serialize_by_alias=True,
        strict=True,
    )

    schema_version: Literal["1.0"] = "1.0"
    transaction_id: str
    lifecycle_state: LifecycleState
    lifecycle_revision: int = Field(ge=1)
    activation_basis: ActivationBasis | None = None
    consent_transaction: RelationshipConsentTransaction
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    proposal_expires_at: datetime
    proposed_at: datetime
    activated_at: datetime | None = None
    approval_receipt_id: str | None = None
    approval_approver_authority_id: str | None = None
    approval_approver_authority_kind: OwnerKind | None = None
    approval_verifier_id: str | None = None
    policy_exception_id: str | None = None
    superseded_by_relationship_id: str | None = None

    @field_validator("proposal_expires_at", "proposed_at", "activated_at")
    @classmethod
    def governance_record_times_are_aware(
        cls,
        value: datetime | None,
    ) -> datetime | None:
        if value is not None and (value.tzinfo is None or value.utcoffset() is None):
            raise ValueError("relationship governance timestamps must be timezone-aware")
        return value

    def active_at(self, when: datetime) -> bool:
        return (
            self.lifecycle_state == "active"
            and self.revoked_at is None
            and when < self.expires_at
        )


class RelationshipPolicyException(BaseModel):
    """Exact domain-policy exception request; this model is not proof itself."""

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        serialize_by_alias=True,
        strict=True,
    )

    schema_: Literal[RELATIONSHIP_POLICY_EXCEPTION_SCHEMA] = Field(
        default=RELATIONSHIP_POLICY_EXCEPTION_SCHEMA,
        alias="schema",
    )
    policy_exception_id: str = Field(default_factory=lambda: str(uuid4()), min_length=1, max_length=128)
    domain_id: str = Field(min_length=1, max_length=256)
    relationship_id: str = Field(min_length=1, max_length=256)
    relationship_revision: int = Field(ge=1)
    expected_lifecycle_revision: int = Field(ge=1)
    relationship_transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    authority_effect: Literal["activate_governance_edge_only"] = "activate_governance_edge_only"
    reason: str = Field(min_length=1, max_length=512)
    expires_at: datetime

    @field_validator("expires_at")
    @classmethod
    def policy_exception_expiry_is_aware(cls, value: datetime) -> datetime:
        if value.tzinfo is None or value.utcoffset() is None:
            raise ValueError("relationship policy exception expiry must be timezone-aware")
        return value


class RelationshipPolicyExceptionRecord(RelationshipPolicyException):
    recorded_at: datetime
    consumed_at: datetime | None = None
    revoked_at: datetime | None = None
    lifecycle_revision: int = Field(ge=1)
    signer_authority_id: str
    signer_harness_id: str

    @field_validator("recorded_at", "consumed_at", "revoked_at")
    @classmethod
    def policy_exception_record_times_are_aware(
        cls,
        value: datetime | None,
    ) -> datetime | None:
        if value is not None and (value.tzinfo is None or value.utcoffset() is None):
            raise ValueError("relationship policy exception timestamps must be timezone-aware")
        return value


@dataclass(frozen=True, slots=True)
class _EndpointSnapshot:
    harness_id: str
    owner_kind: OwnerKind
    owner_id: str
    credential_epoch: int


@dataclass(frozen=True, slots=True)
class _PredecessorSnapshot:
    relationship_id: str
    relationship_revision: int
    lifecycle_revision: int
    state: str


@dataclass(frozen=True, slots=True)
class _ValidatedPolicyException:
    exception: RelationshipPolicyException
    command: SignedAuthorityCommand
    decision_context: dict[str, Any]


@dataclass(frozen=True, slots=True)
class _PendingActivationIntent:
    intent_id: str
    resource_id: str
    evidence_reference: str
    request_digest: str
    activated_at: int


def _is_integrity_error(exc: Exception) -> bool:
    return isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ == "UniqueViolation"


class RelationshipService:
    def __init__(
        self,
        store: StoreBackend,
        *,
        approval_verifier: IndependentApprovalVerifier | None = None,
    ) -> None:
        self.store = store
        self.approval_verifier = approval_verifier
        require_relationship_governance_schema(store)

    @staticmethod
    def _normalized_relationship(relationship: Relationship) -> Relationship:
        if relationship.administrator_harness_id == relationship.subordinate_harness_id:
            raise ValidationError("a harness cannot administer itself")
        if relationship.revoked_at is not None:
            raise ValidationError("a relationship proposal cannot already be revoked")
        if relationship.may_assign:
            scope = AssignmentScope.model_validate(relationship.assignment_scope)
            assignment_scope: dict[str, Any] = scope.model_dump(mode="json")
        else:
            if not isinstance(relationship.assignment_scope, EmptyAssignmentScope):
                raise ValidationError("non-assigning relationship must have an empty assignment scope")
            assignment_scope = {}
        value = relationship.model_dump(mode="json")
        value["assignment_scope"] = assignment_scope
        return Relationship.model_validate(value)

    @staticmethod
    def proposal_binding(
        relationship: Relationship,
        *,
        proposal_expires_at: datetime,
    ) -> tuple[str, dict[str, str]]:
        request = {
            "schema": "agentnet.relationship.propose.v1",
            "relationship": relationship.model_dump(mode="json"),
            "proposal_expires_at": proposal_expires_at.isoformat(),
        }
        return f"relationship:{relationship.relationship_id}", {
            "request_digest": canonical_digest(request)
        }

    @staticmethod
    def issuance_binding(
        relationship: Relationship,
        *,
        proposal_expires_at: datetime | None = None,
    ) -> tuple[str, dict[str, str]]:
        """Compatibility name whose semantics are proposal-only."""

        return RelationshipService.proposal_binding(
            relationship,
            proposal_expires_at=proposal_expires_at or relationship.expires_at,
        )

    @staticmethod
    def read_binding(relationship_id: str) -> tuple[str, dict[str, object]]:
        resource = f"relationship:{relationship_id}"
        return resource, {
            "schema": "agentnet.relationship.read.v2",
            "relationship_id": relationship_id,
        }

    @staticmethod
    def pair_read_binding(
        *,
        domain_id: str,
        administrator_harness_id: str,
        subordinate_harness_id: str,
    ) -> tuple[str, dict[str, object]]:
        pair = {
            "schema": "agentnet.relationship-pair.read.v2",
            "domain_id": domain_id,
            "administrator_harness_id": administrator_harness_id,
            "subordinate_harness_id": subordinate_harness_id,
        }
        return f"relationship-pair:{canonical_digest(pair)}", pair

    @staticmethod
    def revocation_binding(
        relationship_id: str,
        *,
        expected_relationship_revision: int,
        expected_lifecycle_revision: int,
        reason: str,
    ) -> tuple[str, dict[str, object]]:
        resource = f"relationship:{relationship_id}"
        return resource, {
            "schema": "agentnet.relationship.revoke.v2",
            "relationship_id": relationship_id,
            "expected_relationship_revision": expected_relationship_revision,
            "expected_lifecycle_revision": expected_lifecycle_revision,
            "reason": reason,
        }

    @staticmethod
    def policy_exception_binding(
        exception: RelationshipPolicyException,
    ) -> tuple[str, dict[str, object]]:
        return f"relationship:{exception.relationship_id}", {
            "schema": "agentnet.relationship-policy-exception.record.v1",
            "exception": exception.model_dump(mode="json"),
        }

    @staticmethod
    def _endpoint_snapshot(
        connection: Any,
        *,
        domain_id: str,
        harness_id: str,
        now: int,
    ) -> _EndpointSnapshot:
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?", (harness_id,)
        ).fetchone()
        if (
            harness is None
            or harness["domain_id"] != domain_id
            or harness["status"] != "active"
        ):
            raise ValidationError("relationship endpoint is not an active harness in the domain")
        principal_id = harness["principal_id"]
        guest_id = harness["guest_id"]
        if (principal_id is None) == (guest_id is None):
            raise ValidationError("relationship endpoint owner binding is ambiguous")
        if principal_id is not None:
            owner = connection.execute(
                "SELECT domain_id,status FROM principals WHERE principal_id=?",
                (principal_id,),
            ).fetchone()
            if owner is None or owner["domain_id"] != domain_id or owner["status"] != "active":
                raise ValidationError("relationship endpoint human owner is not active")
            owner_kind: OwnerKind = "human"
            owner_id = str(principal_id)
        else:
            owner = connection.execute(
                "SELECT host_domain_id,status,expires_at FROM guests WHERE guest_id=?",
                (guest_id,),
            ).fetchone()
            if (
                owner is None
                or owner["host_domain_id"] != domain_id
                or owner["status"] != "active"
                or int(owner["expires_at"]) <= now
            ):
                raise ValidationError("relationship endpoint guest owner is not active")
            owner_kind = "guest"
            owner_id = str(guest_id)
        credential_epoch = int(harness["credential_epoch"])
        credential = connection.execute(
            """
            SELECT credential_id FROM credentials
             WHERE harness_id=? AND epoch=? AND status='active'
               AND not_before<=? AND expires_at>?
             ORDER BY credential_id LIMIT 1
            """,
            (harness_id, credential_epoch, now, now),
        ).fetchone()
        if credential is None:
            raise ValidationError("relationship endpoint lacks a current active credential")
        return _EndpointSnapshot(
            harness_id=harness_id,
            owner_kind=owner_kind,
            owner_id=owner_id,
            credential_epoch=credential_epoch,
        )

    @classmethod
    def _validate_persisted_policy_exception(
        cls,
        connection: Any,
        exception_row: Any,
        relationship_row: Any,
        *,
        now: int,
    ) -> _ValidatedPolicyException:
        """Reconstruct every signed exception byte and its recording ceremony.

        Mutable projection columns are never proof.  This verifier starts from
        the canonical exception and command bytes, binds them back to every
        projection used by activation, verifies the exact historical signing
        key at ``recorded_at``, and requires the completed purpose-bound audit
        intent that the normal recording path creates atomically.
        """

        try:
            cls._transaction_from_row(relationship_row)
            raw_exception = str(exception_row["exception_json"])
            exception_value = json.loads(raw_exception)
            exception = RelationshipPolicyException.model_validate_json(
                raw_exception,
                strict=True,
            )
            rendered_exception = canonical_json(
                exception.model_dump(mode="json")
            ).decode("utf-8")
            exception_digest = canonical_digest(exception.model_dump(mode="json"))
            if (
                not secrets.compare_digest(raw_exception, rendered_exception)
                or not secrets.compare_digest(
                    canonical_json(exception_value).decode("utf-8"),
                    raw_exception,
                )
                or not secrets.compare_digest(
                    str(exception_row["exception_digest"]),
                    exception_digest,
                )
            ):
                raise ValueError("policy exception canonical bytes changed")

            state = str(relationship_row["state"])
            if state == "proposed":
                expected_lifecycle_revision = int(
                    relationship_row["lifecycle_revision"]
                )
                expected_consumed_at = None
                expected_exception_lifecycle = 1
                exception_valid_at = now
            elif (
                state == "active"
                and relationship_row["activation_basis"]
                == "domain_policy_exception"
                and relationship_row["policy_exception_id"]
                == exception_row["policy_exception_id"]
                and relationship_row["activated_at"] is not None
            ):
                expected_lifecycle_revision = (
                    int(relationship_row["lifecycle_revision"]) - 1
                )
                expected_consumed_at = int(relationship_row["activated_at"])
                expected_exception_lifecycle = 2
                exception_valid_at = expected_consumed_at
            else:
                raise ValueError("policy exception is not bound to this lifecycle")

            row_binding = (
                exception.policy_exception_id
                == exception_row["policy_exception_id"]
                and exception.domain_id == exception_row["domain_id"]
                and exception.domain_id == relationship_row["domain_id"]
                and exception.relationship_id
                == relationship_row["relationship_id"]
                and exception.relationship_revision
                == int(exception_row["relationship_revision"])
                == int(relationship_row["relationship_revision"])
                and exception.expected_lifecycle_revision
                == expected_lifecycle_revision
                and secrets.compare_digest(
                    exception.relationship_transaction_digest,
                    str(exception_row["relationship_transaction_digest"]),
                )
                and secrets.compare_digest(
                    exception.relationship_transaction_digest,
                    str(relationship_row["transaction_digest"]),
                )
                and exception.authority_effect
                == "activate_governance_edge_only"
                and epoch_seconds(exception.expires_at)
                == int(exception_row["expires_at"])
                and int(exception_row["policy_revision"])
                == int(relationship_row["proposal_policy_revision"])
                and int(exception_row["domain_revocation_epoch"])
                == int(relationship_row["proposal_domain_revocation_epoch"])
                and int(exception_row["administrator_credential_epoch"])
                == int(
                    relationship_row["proposal_administrator_credential_epoch"]
                )
                and int(exception_row["subordinate_credential_epoch"])
                == int(
                    relationship_row["proposal_subordinate_credential_epoch"]
                )
                and exception_row["revoked_at"] is None
                and exception_row["consumed_at"] == expected_consumed_at
                and int(exception_row["lifecycle_revision"])
                == expected_exception_lifecycle
                and int(exception_row["expires_at"]) > exception_valid_at
            )
            if not row_binding:
                raise ValueError("policy exception row binding changed")

            raw_command = str(exception_row["command_json"])
            command_value = json.loads(raw_command)
            command = SignedAuthorityCommand.model_validate_json(
                raw_command,
                strict=True,
            )
            rendered_command = canonical_json(
                command.model_dump(mode="json")
            ).decode("utf-8")
            resource, request = cls.policy_exception_binding(exception)
            recorded_at = int(exception_row["recorded_at"])
            expected_signer_kind = (
                "human"
                if command.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
                else "guest"
                if command.actor.kind is ActorKind.HOST_GUEST_HARNESS
                else None
            )
            command_binding = (
                secrets.compare_digest(raw_command, rendered_command)
                and secrets.compare_digest(
                    canonical_json(command_value).decode("utf-8"),
                    raw_command,
                )
                and command.command_id == exception_row["command_id"]
                and command.action
                == "organization.relationship.policy_exception.record"
                and command.resource == resource
                and secrets.compare_digest(
                    command.request_digest,
                    canonical_digest(request),
                )
                and command.expected_policy_revision
                == int(exception_row["policy_revision"])
                and command.expected_entity_revision
                == exception.expected_lifecycle_revision
                and expected_signer_kind
                == exception_row["signer_authority_kind"]
                and command.actor.domain_id == exception.domain_id
                and command.actor.positive_authority_id
                == exception_row["signer_authority_id"]
                and command.actor.harness_id
                == exception_row["signer_harness_id"]
                and command.actor.credential_id
                == exception_row["signer_credential_id"]
                and command.actor.credential_epoch
                == int(exception_row["signer_credential_epoch"])
                and epoch_seconds(command.issued_at) <= recorded_at
                and recorded_at < epoch_seconds(command.expires_at)
            )
            if not command_binding:
                raise ValueError("policy exception command binding changed")

            credential = connection.execute(
                """
                SELECT credential_id,harness_id,key_id,public_key_pem,epoch,
                       not_before,expires_at
                  FROM credentials WHERE credential_id=?
                """,
                (command.actor.credential_id,),
            ).fetchone()
            if (
                credential is None
                or credential["credential_id"]
                != exception_row["signer_credential_id"]
                or credential["harness_id"] != command.actor.harness_id
                or int(credential["epoch"]) != command.actor.credential_epoch
                or int(credential["not_before"]) > recorded_at
                or int(credential["expires_at"]) <= recorded_at
                or public_key_thumbprint(str(credential["public_key_pem"]))
                != credential["key_id"]
            ):
                raise ValueError("policy exception signing credential changed")
            verify_signature(
                str(credential["public_key_pem"]),
                AUTHORITY_COMMAND_PURPOSE,
                command.signed_fields(),
                command.signature,
            )

            decision = connection.execute(
                "SELECT * FROM policy_decisions WHERE decision_id=?",
                (exception_row["policy_decision_id"],),
            ).fetchone()
            if decision is None:
                raise ValueError("policy exception decision is missing")
            decision_resource = json.loads(str(decision["resource_json"]))
            decision_context = json.loads(str(decision["context_json"]))
            actor_json = canonical_json(command.actor.audit_view()).decode("utf-8")
            if (
                decision["decision_id"] != exception_row["policy_decision_id"]
                or int(decision["allowed"]) != 1
                or decision["action"] != command.action
                or not secrets.compare_digest(str(decision["actor_json"]), actor_json)
                or not secrets.compare_digest(
                    str(decision["resource_json"]),
                    canonical_json(decision_resource).decode("utf-8"),
                )
                or decision_resource != {"id": command.resource}
                or not secrets.compare_digest(
                    str(decision["context_json"]),
                    canonical_json(decision_context).decode("utf-8"),
                )
                or not isinstance(decision_context, dict)
                or decision_context.get("request")
                != {"request_digest": command.request_digest}
                or decision_context.get("positive_authority_id")
                != command.actor.positive_authority_id
                or int(decision["policy_revision"])
                != command.expected_policy_revision
                or int(decision["occurred_at"]) > recorded_at
                or int(decision["occurred_at"]) < recorded_at - 300
            ):
                raise ValueError("policy exception decision binding changed")

            intent = connection.execute(
                "SELECT * FROM audit_intents WHERE intent_id=?",
                (command.command_id,),
            ).fetchone()
            if (
                intent is None
                or intent["intent_id"] != exception_row["command_id"]
                or intent["action"] != command.action
                or intent["resource_id"] != command.resource
                or not secrets.compare_digest(str(intent["actor_json"]), actor_json)
                or intent["policy_decision_id"]
                != exception_row["policy_decision_id"]
                or not secrets.compare_digest(
                    str(intent["request_digest"]),
                    command.request_digest,
                )
                or intent["state"] != "completed"
                or int(intent["created_at"]) != recorded_at
                or intent["completed_at"] is None
                or int(intent["completed_at"]) != recorded_at
            ):
                raise ValueError("policy exception recording intent is invalid")
        except Exception as exc:
            raise AuthenticationError(
                "persisted relationship policy exception proof is invalid"
            ) from exc
        return _ValidatedPolicyException(
            exception=exception,
            command=command,
            decision_context=decision_context,
        )

    @staticmethod
    def _policy_exception_signer_is_current(
        connection: Any,
        persisted: _ValidatedPolicyException,
        *,
        now: int,
    ) -> bool:
        """Require the original signer and exact positive authority at use."""

        from agentnet.authorization.policy import validate_actor_state

        command = persisted.command
        context = persisted.decision_context
        denial, current_revision = validate_actor_state(
            connection,
            actor=command.actor,
            expected_policy_revision=command.expected_policy_revision,
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None or current_revision != command.expected_policy_revision:
            return False
        if command.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            entitlement_id = context.get("entitlement_id")
            if not isinstance(entitlement_id, str) or not entitlement_id:
                return False
            entitlement = connection.execute(
                "SELECT * FROM entitlements WHERE entitlement_id=?",
                (entitlement_id,),
            ).fetchone()
            return bool(
                entitlement is not None
                and entitlement["domain_id"] == command.actor.domain_id
                and entitlement["principal_id"] == command.actor.principal_id
                and entitlement["action"] == command.action
                and entitlement["resource_pattern"] in {command.resource, "*"}
                and int(entitlement["revision"]) == current_revision
                and entitlement["revoked_at"] is None
                and (
                    entitlement["expires_at"] is None
                    or int(entitlement["expires_at"]) > now
                )
            )
        if command.actor.kind is not ActorKind.HOST_GUEST_HARNESS:
            return False
        grant_id = context.get("task_grant_id")
        if (
            not isinstance(grant_id, str)
            or not grant_id
            or context.get("task_grant_consumed") is not True
        ):
            return False
        grant_row = connection.execute(
            "SELECT * FROM task_grants WHERE grant_id=?",
            (grant_id,),
        ).fetchone()
        try:
            raw_grant = str(grant_row["grant_json"]) if grant_row is not None else ""
            grant = TaskGrant.model_validate_json(raw_grant, strict=True)
            canonical_grant = canonical_json(grant.model_dump(mode="json")).decode(
                "utf-8"
            )
        except Exception:
            return False
        binding_row = connection.execute(
            "SELECT value FROM metadata WHERE key=?",
            (f"authority-binding:task-grant:{grant_id}",),
        ).fetchone()
        try:
            binding = json.loads(binding_row["value"]) if binding_row else None
        except (TypeError, ValueError):
            binding = None
        return bool(
            grant_row is not None
            and secrets.compare_digest(raw_grant, canonical_grant)
            and grant.grant_id == grant_id == grant_row["grant_id"]
            and grant.domain_id == grant_row["domain_id"] == command.actor.domain_id
            and grant.principal_id
            == grant_row["principal_id"]
            == command.actor.guest_id
            and grant.harness_id
            == grant_row["harness_id"]
            == command.actor.harness_id
            and int(grant_row["max_uses"]) == grant.max_uses
            and grant_row["revoked_at"] is None
            and grant.revoked_at is None
            and int(grant_row["expires_at"]) > now
            and epoch_seconds(grant.expires_at) > now
            and command.action in grant.actions
            and command.resource in grant.resources
            and isinstance(binding, dict)
            and binding.get("schema") == "agentnet.task-grant.authority-binding.v1"
            and binding.get("domain_id") == command.actor.domain_id
            and binding.get("principal_id") == command.actor.guest_id
            and binding.get("harness_id") == command.actor.harness_id
            and int(binding.get("policy_revision", 0)) == current_revision
            and int(binding.get("harness_credential_epoch", 0))
            == command.actor.credential_epoch
        )

    @staticmethod
    def _activation_intent_id(transaction_id: str) -> str:
        """Return a namespace-separated ID that cannot collide with command UUIDs."""

        return "relationship-activation:" + canonical_digest(
            {
                "schema": RELATIONSHIP_ACTIVATION_INTENT_SCHEMA,
                "transaction_id": transaction_id,
            }
        )

    @staticmethod
    def _owner_consent_activation_evidence(
        *,
        receipt_id: str,
        receipt_digest: str,
        approver_authority_id: str,
        approver_authority_kind: OwnerKind,
        verifier_id: str,
        signer_key_id: str,
        expires_at: int,
    ) -> dict[str, Any]:
        return {
            "kind": "independent_owner_approval",
            "approval_receipt_id": receipt_id,
            "approval_receipt_digest": receipt_digest,
            "approver_authority_id": approver_authority_id,
            "approver_authority_kind": approver_authority_kind,
            "verifier_id": verifier_id,
            "signer_key_id": signer_key_id,
            "expires_at": expires_at,
        }

    @staticmethod
    def _policy_exception_activation_evidence(exception_row: Any) -> dict[str, Any]:
        return {
            "kind": "recorded_domain_policy_exception",
            "policy_exception_id": str(exception_row["policy_exception_id"]),
            "policy_exception_digest": str(exception_row["exception_digest"]),
            "policy_decision_id": str(exception_row["policy_decision_id"]),
        }

    @staticmethod
    def _activation_evidence_reference(evidence: Mapping[str, Any]) -> str:
        """Map the legacy audit-intent column to a real positive-evidence ID.

        ``audit_intents.policy_decision_id`` predates independent approvals.  For
        relationship activation intents it deliberately stores a namespaced
        reference to the real consumed approval receipt or recorded policy
        exception.  It is never interpreted as a policy decision and never
        participates in the ordinary command-intent uniqueness check.
        """

        if evidence.get("kind") == "independent_owner_approval":
            return f"approval-receipt:{evidence['approval_receipt_id']}"
        if evidence.get("kind") == "recorded_domain_policy_exception":
            return f"policy-exception:{evidence['policy_exception_id']}"
        raise ValidationError("relationship activation evidence kind is invalid")

    @staticmethod
    def _activation_intent_request(
        row: Any,
        *,
        activation_basis: ActivationBasis,
        activation_actor: VerifiedActor,
        evidence: Mapping[str, Any],
        activated_at: int,
        expected_lifecycle_revision: int,
    ) -> dict[str, Any]:
        return {
            "schema": RELATIONSHIP_ACTIVATION_INTENT_SCHEMA,
            "domain_id": str(row["domain_id"]),
            "relationship_id": str(row["relationship_id"]),
            "relationship_revision": int(row["relationship_revision"]),
            "transaction_id": str(row["transaction_id"]),
            "transaction_digest": str(row["transaction_digest"]),
            "expected_lifecycle_revision": expected_lifecycle_revision,
            "activated_lifecycle_revision": expected_lifecycle_revision + 1,
            "from_state": "proposed",
            "to_state": "active",
            "activation_basis": activation_basis,
            "activation_actor": activation_actor.audit_view(),
            "activation_evidence": dict(evidence),
            "activated_at": activated_at,
            "authority_effect": "custody_only",
        }

    @classmethod
    def _begin_activation_intent(
        cls,
        connection: Any,
        row: Any,
        *,
        activation_basis: ActivationBasis,
        activation_actor: VerifiedActor,
        evidence: Mapping[str, Any],
        when: datetime,
    ) -> _PendingActivationIntent:
        """Record the exact activation purpose before consuming its proof."""

        if row["state"] != "proposed":
            raise ConflictError("relationship activation no longer targets a proposal")
        activated_at = epoch_seconds(when)
        intent_id = cls._activation_intent_id(str(row["transaction_id"]))
        resource_id = f"relationship:{row['relationship_id']}"
        evidence_reference = cls._activation_evidence_reference(evidence)
        request_digest = canonical_digest(
            cls._activation_intent_request(
                row,
                activation_basis=activation_basis,
                activation_actor=activation_actor,
                evidence=evidence,
                activated_at=activated_at,
                expected_lifecycle_revision=int(row["lifecycle_revision"]),
            )
        )
        try:
            connection.execute(
                """
                INSERT INTO audit_intents(
                    intent_id,action,resource_id,actor_json,policy_decision_id,
                    request_digest,state,created_at
                ) VALUES(?,?,?,?,?,?,'pending',?)
                """,
                (
                    intent_id,
                    RELATIONSHIP_ACTIVATION_INTENT_ACTION,
                    resource_id,
                    canonical_json(activation_actor.audit_view()).decode("utf-8"),
                    evidence_reference,
                    request_digest,
                    activated_at,
                ),
            )
        except Exception as exc:
            if _is_integrity_error(exc):
                raise ConflictError(
                    "relationship activation intent was already consumed"
                ) from exc
            raise
        return _PendingActivationIntent(
            intent_id=intent_id,
            resource_id=resource_id,
            evidence_reference=evidence_reference,
            request_digest=request_digest,
            activated_at=activated_at,
        )

    @staticmethod
    def _complete_activation_intent(
        connection: Any,
        intent: _PendingActivationIntent,
    ) -> None:
        cursor = connection.execute(
            """
            UPDATE audit_intents SET state='completed',completed_at=?
             WHERE intent_id=? AND action=? AND resource_id=?
               AND policy_decision_id=? AND request_digest=?
               AND state='pending' AND created_at=? AND completed_at IS NULL
            """,
            (
                intent.activated_at,
                intent.intent_id,
                RELATIONSHIP_ACTIVATION_INTENT_ACTION,
                intent.resource_id,
                intent.evidence_reference,
                intent.request_digest,
                intent.activated_at,
            ),
        )
        if cursor.rowcount != 1:
            raise ConflictError("relationship activation audit intent is not exact and pending")

    @staticmethod
    def _activation_actor_is_bound(
        connection: Any,
        row: Any,
        *,
        actor: VerifiedActor,
        activated_at: int,
        additional_activation_actor: VerifiedActor | None = None,
    ) -> bool:
        if actor.domain_id != row["domain_id"] or actor.harness_id is None:
            return False
        is_exact_additional_actor = bool(
            additional_activation_actor is not None
            and secrets.compare_digest(
                canonical_json(actor.audit_view()),
                canonical_json(additional_activation_actor.audit_view()),
            )
        )
        if is_exact_additional_actor:
            owner_kind = (
                "human"
                if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
                else "guest"
                if actor.kind is ActorKind.HOST_GUEST_HARNESS
                else None
            )
            owner_id = actor.positive_authority_id
            credential_epoch = actor.credential_epoch
        elif actor.harness_id == row["administrator_harness_id"]:
            owner_kind = row["administrator_owner_kind"]
            owner_id = row["administrator_owner_id"]
            credential_epoch = int(row["proposal_administrator_credential_epoch"])
        elif actor.harness_id == row["subordinate_harness_id"]:
            owner_kind = row["subordinate_owner_kind"]
            owner_id = row["subordinate_owner_id"]
            credential_epoch = int(row["proposal_subordinate_credential_epoch"])
        else:
            return False
        actor_owner_kind = (
            "human"
            if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
            else "guest"
            if actor.kind is ActorKind.HOST_GUEST_HARNESS
            else None
        )
        if (
            actor_owner_kind != owner_kind
            or actor.positive_authority_id != owner_id
            or owner_id is None
            or actor.credential_id is None
            or actor.credential_epoch != credential_epoch
        ):
            return False
        credential = connection.execute(
            """
            SELECT harness_id,epoch,not_before,expires_at FROM credentials
             WHERE credential_id=?
            """,
            (actor.credential_id,),
        ).fetchone()
        return bool(
            credential is not None
            and credential["harness_id"] == actor.harness_id
            and int(credential["epoch"]) == credential_epoch
            and int(credential["not_before"]) <= activated_at
            and int(credential["expires_at"]) > activated_at
        )

    @classmethod
    def _persisted_activation_intent_is_valid(
        cls,
        connection: Any,
        row: Any,
        *,
        activation_basis: ActivationBasis,
        evidence: Mapping[str, Any],
        additional_activation_actor: VerifiedActor | None = None,
    ) -> bool:
        """Require exact atomic activation provenance for either proof basis.

        This is durable local evidence, not an externally witnessed root.  A
        fully coherent database compromise can forge or erase all local rows;
        production claims still require the independently anchored audit witness
        tracked by the release evidence gates.
        """

        try:
            if (
                row["state"] != "active"
                or row["activation_basis"] != activation_basis
                or row["activated_at"] is None
                or int(row["lifecycle_revision"]) != 2
            ):
                return False
            activated_at = int(row["activated_at"])
            intent_id = cls._activation_intent_id(str(row["transaction_id"]))
            intent = connection.execute(
                "SELECT * FROM audit_intents WHERE intent_id=?",
                (intent_id,),
            ).fetchone()
            if intent is None:
                return False
            raw_actor = str(intent["actor_json"])
            actor_value = json.loads(raw_actor)
            actor = VerifiedActor.model_validate_json(raw_actor, strict=True)
            if not secrets.compare_digest(
                raw_actor,
                canonical_json(actor_value).decode("utf-8"),
            ) or not secrets.compare_digest(
                raw_actor,
                canonical_json(actor.audit_view()).decode("utf-8"),
            ):
                return False
            expected_request = cls._activation_intent_request(
                row,
                activation_basis=activation_basis,
                activation_actor=actor,
                evidence=evidence,
                activated_at=activated_at,
                expected_lifecycle_revision=int(row["lifecycle_revision"]) - 1,
            )
            return bool(
                intent["action"] == RELATIONSHIP_ACTIVATION_INTENT_ACTION
                and intent["resource_id"] == f"relationship:{row['relationship_id']}"
                and intent["policy_decision_id"]
                == cls._activation_evidence_reference(evidence)
                and secrets.compare_digest(
                    str(intent["request_digest"]),
                    canonical_digest(expected_request),
                )
                and intent["state"] == "completed"
                and int(intent["created_at"]) == activated_at
                and intent["completed_at"] is not None
                and int(intent["completed_at"]) == activated_at
                and cls._activation_actor_is_bound(
                    connection,
                    row,
                    actor=actor,
                    activated_at=activated_at,
                    additional_activation_actor=additional_activation_actor,
                )
            )
        except Exception:
            return False

    @classmethod
    def _persisted_owner_consent_is_valid(
        cls,
        connection: Any,
        row: Any,
        *,
        approval_verifier: IndependentApprovalVerifier | None,
    ) -> bool:
        """Verify the exact receipt ceremony behind one active consent edge."""

        if not isinstance(approval_verifier, IndependentApprovalVerifier):
            return False
        try:
            cls._transaction_from_row(row)
            if row["activated_at"] is None:
                return False
            raw_receipt = str(row["approval_receipt_json"])
            receipt_value = json.loads(raw_receipt)
            receipt = IndependentApprovalReceipt.model_validate_json(
                raw_receipt,
                strict=True,
            )
            canonical_receipt = canonical_json(
                receipt.model_dump(mode="json")
            ).decode("utf-8")
            if (
                not secrets.compare_digest(raw_receipt, canonical_receipt)
                or not secrets.compare_digest(
                    canonical_json(receipt_value).decode("utf-8"),
                    raw_receipt,
                )
                or not secrets.compare_digest(
                    canonical_digest(receipt.model_dump(mode="json")),
                    str(row["approval_receipt_digest"]),
                )
            ):
                return False
            activated_at = int(row["activated_at"])
            verified = approval_verifier.verify(
                canonical_transaction=str(
                    row["canonical_transaction_json"]
                ).encode("utf-8"),
                approval=receipt.model_dump(mode="json"),
                expected_purpose=RELATIONSHIP_CONSENT_PURPOSE,
                expected_domain_id=row["domain_id"],
                when=datetime.fromtimestamp(activated_at, UTC),
            )
            if (
                verified.receipt_id != row["approval_receipt_id"]
                or verified.approver_principal_id
                != row["approval_approver_authority_id"]
                or verified.approver_authority_kind
                != row["approval_approver_authority_kind"]
                or verified.approver_principal_id != row["subordinate_owner_id"]
                or verified.approver_authority_kind
                != row["subordinate_owner_kind"]
                or verified.verifier_id != row["approval_verifier_id"]
                or verified.signer_key_id != row["approval_signer_key_id"]
                or verified.expires_at != int(row["approval_expires_at"])
            ):
                return False
            actor_id, nonce_hash, retain_until = independent_approval_replay_binding(
                verified,
                retain_until=max(
                    int(row["relationship_expires_at"]),
                    verified.expires_at,
                ),
            )
            replay = connection.execute(
                """
                SELECT expires_at FROM replay_nonces
                 WHERE actor_id=? AND nonce_hash=?
                """,
                (actor_id, nonce_hash),
            ).fetchone()
            evidence = cls._owner_consent_activation_evidence(
                receipt_id=verified.receipt_id,
                receipt_digest=str(row["approval_receipt_digest"]),
                approver_authority_id=verified.approver_principal_id,
                approver_authority_kind=verified.approver_authority_kind,
                verifier_id=verified.verifier_id,
                signer_key_id=verified.signer_key_id,
                expires_at=verified.expires_at,
            )
            return bool(
                replay is not None
                and int(replay["expires_at"]) == retain_until
                and cls._persisted_activation_intent_is_valid(
                    connection,
                    row,
                    activation_basis="subordinate_owner_consent",
                    evidence=evidence,
                )
            )
        except Exception:
            return False

    @staticmethod
    def _predecessor(
        connection: Any,
        *,
        domain_id: str,
        administrator_harness_id: str,
        subordinate_harness_id: str,
        before_revision: int | None = None,
    ) -> _PredecessorSnapshot | None:
        bound = " AND relationship_revision<?" if before_revision is not None else ""
        parameters: tuple[Any, ...] = (
            domain_id,
            administrator_harness_id,
            subordinate_harness_id,
        ) + ((before_revision,) if before_revision is not None else ())
        row = connection.execute(
            f"""
            SELECT relationship_id,relationship_revision,lifecycle_revision,state
              FROM relationship_governance_transactions
             WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?{bound}
             ORDER BY relationship_revision DESC LIMIT 1
            """,
            parameters,
        ).fetchone()
        if row is not None:
            return _PredecessorSnapshot(
                relationship_id=str(row["relationship_id"]),
                relationship_revision=int(row["relationship_revision"]),
                lifecycle_revision=int(row["lifecycle_revision"]),
                state=str(row["state"]),
            )
        return None

    @staticmethod
    def _transaction_from_row(row: Any) -> RelationshipConsentTransaction:
        try:
            canonical_transaction_json = str(row["canonical_transaction_json"])
            value = json.loads(canonical_transaction_json)
            if not secrets.compare_digest(
                canonical_json(value).decode("utf-8"),
                canonical_transaction_json,
            ) or not secrets.compare_digest(
                canonical_digest(value),
                str(row["transaction_digest"]),
            ):
                raise ValueError("stored canonical transaction bytes or digest changed")
            transaction = RelationshipConsentTransaction.model_validate_json(
                canonical_transaction_json,
                strict=True,
            )
        except Exception as exc:
            raise AuthenticationError("relationship consent transaction is malformed") from exc
        relationship = transaction.relationship
        stored_scope = json.loads(row["assignment_scope_json"])
        exact_binding = (
            transaction.transaction_id == row["transaction_id"]
            and relationship.relationship_id == row["relationship_id"]
            and relationship.domain_id == row["domain_id"]
            and relationship.administrator_harness_id
            == row["administrator_harness_id"]
            and relationship.subordinate_harness_id == row["subordinate_harness_id"]
            and relationship.may_assign is bool(row["may_assign"])
            and value["relationship"]["assignment_scope"] == stored_scope
            and relationship.revision == int(row["relationship_revision"])
            and epoch_seconds(relationship.expires_at)
            == int(row["relationship_expires_at"])
            and relationship.revoked_at is None
            and epoch_seconds(transaction.proposal_expires_at)
            == int(row["proposal_expires_at"])
            and transaction.administrator_owner_kind == row["administrator_owner_kind"]
            and transaction.administrator_owner_id == row["administrator_owner_id"]
            and transaction.subordinate_owner_kind == row["subordinate_owner_kind"]
            and transaction.subordinate_owner_id == row["subordinate_owner_id"]
            and transaction.policy_revision == int(row["proposal_policy_revision"])
            and transaction.domain_revocation_epoch
            == int(row["proposal_domain_revocation_epoch"])
            and transaction.administrator_credential_epoch
            == int(row["proposal_administrator_credential_epoch"])
            and transaction.subordinate_credential_epoch
            == int(row["proposal_subordinate_credential_epoch"])
            and transaction.lineage_revocation_epoch
            == int(row["proposal_lineage_revocation_epoch"])
            and epoch_seconds(transaction.proposed_at) == int(row["created_at"])
        )
        if not exact_binding:
            raise AuthenticationError("relationship consent transaction row binding is invalid")
        return transaction

    @classmethod
    def _from_row(cls, row: Any, *, when: datetime) -> RelationshipGovernanceRecord:
        transaction = cls._transaction_from_row(row)
        state = str(row["state"])
        if state == "active" and int(row["relationship_expires_at"]) <= epoch_seconds(when):
            state = "expired"
        return RelationshipGovernanceRecord(
            relationship_id=row["relationship_id"],
            domain_id=row["domain_id"],
            administrator_harness_id=row["administrator_harness_id"],
            subordinate_harness_id=row["subordinate_harness_id"],
            may_assign=bool(row["may_assign"]),
            assignment_scope=json.loads(row["assignment_scope_json"]),
            revision=int(row["relationship_revision"]),
            expires_at=datetime.fromtimestamp(row["relationship_expires_at"], UTC),
            revoked_at=(
                datetime.fromtimestamp(row["revoked_at"], UTC)
                if row["revoked_at"] is not None
                else None
            ),
            transaction_id=row["transaction_id"],
            lifecycle_state=state,
            lifecycle_revision=int(row["lifecycle_revision"]),
            activation_basis=row["activation_basis"],
            consent_transaction=transaction,
            transaction_digest=row["transaction_digest"],
            proposal_expires_at=datetime.fromtimestamp(row["proposal_expires_at"], UTC),
            proposed_at=datetime.fromtimestamp(row["created_at"], UTC),
            activated_at=(
                datetime.fromtimestamp(row["activated_at"], UTC)
                if row["activated_at"] is not None
                else None
            ),
            approval_receipt_id=row["approval_receipt_id"],
            approval_approver_authority_id=row["approval_approver_authority_id"],
            approval_approver_authority_kind=row["approval_approver_authority_kind"],
            approval_verifier_id=row["approval_verifier_id"],
            policy_exception_id=row["policy_exception_id"],
            superseded_by_relationship_id=row["superseded_by_relationship_id"],
        )

    def _expire_proposals(
        self,
        connection: Any,
        *,
        domain_id: str,
        administrator_harness_id: str,
        subordinate_harness_id: str,
        now: int,
    ) -> None:
        rows = connection.execute(
            """
            SELECT transaction_id,relationship_id,lifecycle_revision
              FROM relationship_governance_transactions
             WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
               AND state='proposed' AND proposal_expires_at<=?
            """,
            (domain_id, administrator_harness_id, subordinate_harness_id, now),
        ).fetchall()
        for row in rows:
            cursor = connection.execute(
                """
                UPDATE relationship_governance_transactions
                   SET state='expired',lifecycle_revision=lifecycle_revision+1,updated_at=?
                 WHERE transaction_id=? AND lifecycle_revision=? AND state='proposed'
                """,
                (now, row["transaction_id"], row["lifecycle_revision"]),
            )
            if cursor.rowcount == 1:
                self.store.append_audit(
                    connection,
                    {
                        "type": "relationship_proposal_expired",
                        "relationship_id": row["relationship_id"],
                        "expired_at": datetime.fromtimestamp(now, UTC).isoformat(),
                    },
                )

    @staticmethod
    def expire_active_in_transaction(
        store: StoreBackend,
        connection: Any,
        row: Any,
        *,
        when: datetime,
    ) -> Any:
        """Persist and audit an authority edge's automatic expiry exactly once."""

        now = epoch_seconds(when)
        if (
            row is None
            or row["state"] != "active"
            or int(row["relationship_expires_at"]) > now
        ):
            return row
        cursor = connection.execute(
            """
            UPDATE relationship_governance_transactions
               SET state='expired',lifecycle_revision=lifecycle_revision+1,updated_at=?
             WHERE transaction_id=? AND lifecycle_revision=? AND state='active'
               AND relationship_expires_at<=?
            """,
            (now, row["transaction_id"], row["lifecycle_revision"], now),
        )
        if cursor.rowcount == 1:
            store.append_audit(
                connection,
                {
                    "type": "relationship_expired",
                    "relationship_id": row["relationship_id"],
                    "relationship_revision": int(row["relationship_revision"]),
                    "previous_lifecycle_revision": int(row["lifecycle_revision"]),
                    "expired_at": when.isoformat(),
                    "authority_effect": "none",
                },
            )
        current = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE transaction_id=?",
            (row["transaction_id"],),
        ).fetchone()
        if current is None:
            raise ConflictError("relationship expiry lost its lifecycle row")
        return current

    def propose(
        self,
        relationship: Relationship,
        *,
        proposal_expires_at: datetime,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> RelationshipGovernanceRecord:
        when = when or datetime.now(UTC)
        if proposal_expires_at.tzinfo is None:
            raise ValidationError("relationship proposal expiry must be timezone-aware")
        normalized = self._normalized_relationship(relationship)
        now = epoch_seconds(when)
        if epoch_seconds(normalized.expires_at) <= now:
            raise ValidationError("relationship must expire in the future")
        if not (now < epoch_seconds(proposal_expires_at) <= epoch_seconds(normalized.expires_at)):
            raise ValidationError("relationship proposal expiry is outside the relationship lifetime")

        with self.store.transaction() as connection:
            resource, expected_request = self.proposal_binding(
                normalized,
                proposal_expires_at=proposal_expires_at,
            )
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="organization.relationship.propose",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if authority is None:
                raise AuthorizationError("exact relationship proposal authority is required")
            actor = authority.actor
            if (
                actor.domain_id != normalized.domain_id
                or actor.harness_id != normalized.administrator_harness_id
                or actor.positive_authority_id is None
                or actor.credential_id is None
            ):
                raise AuthorizationError("relationship proposer must be the exact administrator endpoint")
            domain = connection.execute(
                "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
                (normalized.domain_id,),
            ).fetchone()
            if domain is None or domain["status"] != "active":
                raise ValidationError("relationship domain is not active")
            administrator = self._endpoint_snapshot(
                connection,
                domain_id=normalized.domain_id,
                harness_id=normalized.administrator_harness_id,
                now=now,
            )
            subordinate = self._endpoint_snapshot(
                connection,
                domain_id=normalized.domain_id,
                harness_id=normalized.subordinate_harness_id,
                now=now,
            )
            if administrator.owner_id != actor.positive_authority_id:
                raise AuthorizationError("relationship proposer does not own the administrator endpoint")

            connection.execute(
                """
                INSERT INTO relationship_governance_lineages(
                    domain_id,administrator_harness_id,subordinate_harness_id,
                    revocation_epoch,lifecycle_revision,last_revoked_at,
                    last_revocation_command_id,updated_at
                ) VALUES(?,?,?,0,1,NULL,NULL,?)
                ON CONFLICT(domain_id,administrator_harness_id,subordinate_harness_id) DO NOTHING
                """,
                (
                    normalized.domain_id,
                    normalized.administrator_harness_id,
                    normalized.subordinate_harness_id,
                    now,
                ),
            )
            lineage = connection.execute(
                """
                SELECT * FROM relationship_governance_lineages
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                """,
                (
                    normalized.domain_id,
                    normalized.administrator_harness_id,
                    normalized.subordinate_harness_id,
                ),
            ).fetchone()
            if lineage is None:
                raise ConflictError("relationship lineage could not be established")

            existing = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (normalized.relationship_id,),
            ).fetchone()
            if existing is not None:
                existing_transaction = self._transaction_from_row(existing)
                _resource, original_request = self.proposal_binding(
                    existing_transaction.relationship,
                    proposal_expires_at=existing_transaction.proposal_expires_at,
                )
                actor_authority_kind = (
                    "human"
                    if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
                    else "guest"
                )
                same = (
                    secrets.compare_digest(
                        original_request["request_digest"],
                        expected_request["request_digest"],
                    )
                    and existing_transaction.policy_revision == policy_revision
                    and existing_transaction.domain_revocation_epoch
                    == int(domain["revocation_epoch"])
                    and existing_transaction.administrator_owner_kind
                    == administrator.owner_kind
                    and existing_transaction.administrator_owner_id
                    == administrator.owner_id
                    and existing_transaction.subordinate_owner_kind
                    == subordinate.owner_kind
                    and existing_transaction.subordinate_owner_id == subordinate.owner_id
                    and existing_transaction.administrator_credential_epoch
                    == administrator.credential_epoch
                    and existing_transaction.subordinate_credential_epoch
                    == subordinate.credential_epoch
                    and existing_transaction.lineage_revocation_epoch
                    == int(lineage["revocation_epoch"])
                    and existing["proposer_authority_kind"] == actor_authority_kind
                    and existing["proposer_authority_id"] == actor.positive_authority_id
                    and existing["proposer_harness_id"] == actor.harness_id
                    and existing["proposer_credential_id"] == actor.credential_id
                    and int(existing["proposer_credential_epoch"])
                    == actor.credential_epoch
                )
                if same:
                    return self._from_row(existing, when=when)
                raise ConflictError("relationship identifier already binds different proposal bytes")
            self._expire_proposals(
                connection,
                domain_id=normalized.domain_id,
                administrator_harness_id=normalized.administrator_harness_id,
                subordinate_harness_id=normalized.subordinate_harness_id,
                now=now,
            )
            active = connection.execute(
                """
                SELECT * FROM relationship_governance_transactions
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                   AND state='active' LIMIT 1
                """,
                (
                    normalized.domain_id,
                    normalized.administrator_harness_id,
                    normalized.subordinate_harness_id,
                ),
            ).fetchone()
            if active is not None:
                self.expire_active_in_transaction(
                    self.store,
                    connection,
                    active,
                    when=when,
                )
            pending = connection.execute(
                """
                SELECT relationship_id FROM relationship_governance_transactions
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                   AND state='proposed' LIMIT 1
                """,
                (
                    normalized.domain_id,
                    normalized.administrator_harness_id,
                    normalized.subordinate_harness_id,
                ),
            ).fetchone()
            if pending is not None:
                raise ConflictError("a relationship proposal for this exact pair is already pending")

            predecessor = self._predecessor(
                connection,
                domain_id=normalized.domain_id,
                administrator_harness_id=normalized.administrator_harness_id,
                subordinate_harness_id=normalized.subordinate_harness_id,
            )
            required_revision = 1 if predecessor is None else predecessor.relationship_revision + 1
            if normalized.revision != required_revision:
                raise ConflictError("relationship revision is not the next coherent revision")

            transaction_id = str(uuid4())
            consent_transaction = RelationshipConsentTransaction(
                transaction_id=transaction_id,
                relationship=normalized,
                proposal_expires_at=proposal_expires_at,
                administrator_owner_kind=administrator.owner_kind,
                administrator_owner_id=administrator.owner_id,
                subordinate_owner_kind=subordinate.owner_kind,
                subordinate_owner_id=subordinate.owner_id,
                policy_revision=policy_revision,
                domain_revocation_epoch=int(domain["revocation_epoch"]),
                administrator_credential_epoch=administrator.credential_epoch,
                subordinate_credential_epoch=subordinate.credential_epoch,
                lineage_revocation_epoch=int(lineage["revocation_epoch"]),
                predecessor_relationship_id=(predecessor.relationship_id if predecessor else None),
                predecessor_relationship_revision=(
                    predecessor.relationship_revision if predecessor else None
                ),
                predecessor_lifecycle_revision=(
                    predecessor.lifecycle_revision if predecessor else None
                ),
                predecessor_state=(predecessor.state if predecessor else None),
                proposed_at=when,
            )
            transaction_json = canonical_json(
                consent_transaction.model_dump(mode="json")
            ).decode("utf-8")
            transaction_digest = canonical_digest(
                consent_transaction.model_dump(mode="json")
            )
            columns = (
                "transaction_id", "relationship_id", "schema_version", "domain_id",
                "administrator_harness_id", "subordinate_harness_id",
                "administrator_owner_kind", "administrator_owner_id",
                "subordinate_owner_kind", "subordinate_owner_id", "may_assign",
                "assignment_scope_json", "relationship_revision", "relationship_expires_at",
                "proposal_expires_at", "canonical_transaction_json", "transaction_digest",
                "proposal_policy_revision", "proposal_domain_revocation_epoch",
                "proposal_administrator_credential_epoch", "proposal_subordinate_credential_epoch",
                "proposal_lineage_revocation_epoch",
                "proposer_authority_kind", "proposer_authority_id", "proposer_harness_id",
                "proposer_credential_id", "proposer_credential_epoch", "state",
                "lifecycle_revision", "created_at", "updated_at",
            )
            values = (
                transaction_id, normalized.relationship_id, "1.0", normalized.domain_id,
                normalized.administrator_harness_id, normalized.subordinate_harness_id,
                administrator.owner_kind, administrator.owner_id, subordinate.owner_kind,
                subordinate.owner_id, int(normalized.may_assign),
                canonical_json(
                    normalized.model_dump(mode="json")["assignment_scope"]
                ).decode("utf-8"),
                normalized.revision,
                epoch_seconds(normalized.expires_at), epoch_seconds(proposal_expires_at),
                transaction_json, transaction_digest, policy_revision,
                int(domain["revocation_epoch"]), administrator.credential_epoch,
                subordinate.credential_epoch, int(lineage["revocation_epoch"]),
                "human" if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS else "guest",
                actor.positive_authority_id, actor.harness_id, actor.credential_id,
                actor.credential_epoch, "proposed", 1, now, now,
            )
            try:
                connection.execute(
                    f"INSERT INTO relationship_governance_transactions({','.join(columns)}) "
                    f"VALUES({','.join('?' for _ in columns)})",
                    values,
                )
            except Exception as exc:
                if _is_integrity_error(exc):
                    raise ConflictError("relationship proposal conflicts with current governance state") from exc
                raise
            self.store.append_audit(
                connection,
                {
                    "type": "relationship_proposed",
                    "relationship_id": normalized.relationship_id,
                    "relationship_revision": normalized.revision,
                    "transaction_id": transaction_id,
                    "transaction_digest": transaction_digest,
                    "proposal_expires_at": proposal_expires_at.isoformat(),
                    "proposer": actor.audit_view(),
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": policy_revision,
                    "authority_effect": "none_until_bilateral_activation",
                },
            )
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            return self._from_row(row, when=when)

    def issue(
        self,
        relationship: Relationship,
        *,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
        proposal_expires_at: datetime | None = None,
    ) -> RelationshipGovernanceRecord:
        """Backward-compatible name that can only create a zero-authority proposal."""

        return self.propose(
            relationship,
            proposal_expires_at=proposal_expires_at or relationship.expires_at,
            authority=authority,
            when=when,
        )

    def _require_current_transaction(
        self,
        connection: Any,
        row: Any,
        *,
        when: datetime,
    ) -> RelationshipConsentTransaction:
        now = epoch_seconds(when)
        if row["state"] != "proposed":
            raise ConflictError("relationship proposal is no longer activatable")
        if int(row["proposal_expires_at"]) <= now:
            cursor = connection.execute(
                """
                UPDATE relationship_governance_transactions
                   SET state='expired',lifecycle_revision=lifecycle_revision+1,updated_at=?
                 WHERE transaction_id=? AND lifecycle_revision=? AND state='proposed'
                """,
                (now, row["transaction_id"], row["lifecycle_revision"]),
            )
            if cursor.rowcount == 1:
                self.store.append_audit(
                    connection,
                    {
                        "type": "relationship_proposal_expired",
                        "relationship_id": row["relationship_id"],
                        "expired_at": when.isoformat(),
                    },
                )
            raise AuthenticationError("relationship proposal is expired")
        if int(row["relationship_expires_at"]) <= now:
            raise AuthenticationError("relationship authority lifetime is expired")
        transaction = self._transaction_from_row(row)
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (row["domain_id"],),
        ).fetchone()
        if (
            domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != int(row["proposal_policy_revision"])
            or int(domain["revocation_epoch"]) != int(row["proposal_domain_revocation_epoch"])
        ):
            raise AuthenticationError("relationship proposal policy or domain epoch is stale")
        administrator = self._endpoint_snapshot(
            connection,
            domain_id=row["domain_id"],
            harness_id=row["administrator_harness_id"],
            now=now,
        )
        subordinate = self._endpoint_snapshot(
            connection,
            domain_id=row["domain_id"],
            harness_id=row["subordinate_harness_id"],
            now=now,
        )
        current = (
            administrator.owner_kind,
            administrator.owner_id,
            subordinate.owner_kind,
            subordinate.owner_id,
            administrator.credential_epoch,
            subordinate.credential_epoch,
        )
        proposed = (
            row["administrator_owner_kind"],
            row["administrator_owner_id"],
            row["subordinate_owner_kind"],
            row["subordinate_owner_id"],
            int(row["proposal_administrator_credential_epoch"]),
            int(row["proposal_subordinate_credential_epoch"]),
        )
        if current != proposed:
            raise AuthenticationError("relationship proposal endpoint owner or credential epoch is stale")
        lineage = connection.execute(
            """
            SELECT revocation_epoch FROM relationship_governance_lineages
             WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
            """,
            (
                row["domain_id"],
                row["administrator_harness_id"],
                row["subordinate_harness_id"],
            ),
        ).fetchone()
        if (
            lineage is None
            or int(lineage["revocation_epoch"])
            != int(row["proposal_lineage_revocation_epoch"])
            or transaction.lineage_revocation_epoch
            != int(row["proposal_lineage_revocation_epoch"])
        ):
            raise ConflictError("relationship proposal crossed a committed revocation fence")
        latest = self._predecessor(
            connection,
            domain_id=row["domain_id"],
            administrator_harness_id=row["administrator_harness_id"],
            subordinate_harness_id=row["subordinate_harness_id"],
        )
        if latest is None or latest.relationship_id != row["relationship_id"]:
            raise ConflictError("relationship proposal conflicts with a newer revision")
        predecessor = self._predecessor(
            connection,
            domain_id=row["domain_id"],
            administrator_harness_id=row["administrator_harness_id"],
            subordinate_harness_id=row["subordinate_harness_id"],
            before_revision=int(row["relationship_revision"]),
        )
        expected_predecessor = (
            transaction.predecessor_relationship_id,
            transaction.predecessor_relationship_revision,
            transaction.predecessor_lifecycle_revision,
            transaction.predecessor_state,
        )
        current_predecessor = (
            predecessor.relationship_id if predecessor else None,
            predecessor.relationship_revision if predecessor else None,
            predecessor.lifecycle_revision if predecessor else None,
            predecessor.state if predecessor else None,
        )
        if current_predecessor != expected_predecessor:
            raise ConflictError("relationship renewal raced with revocation or lifecycle change")
        return transaction

    @staticmethod
    def _require_activation_caller(
        connection: Any,
        *,
        actor: VerifiedActor,
        row: Any,
        when: datetime,
        additional_harness_id: str | None = None,
    ) -> None:
        from agentnet.authorization.policy import validate_actor_state

        denial, _revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(row["proposal_policy_revision"]),
            when=when,
        )
        allowed_harnesses = {
            row["administrator_harness_id"],
            row["subordinate_harness_id"],
        }
        if additional_harness_id is not None:
            allowed_harnesses.add(additional_harness_id)
        if (
            denial is not None
            or actor.domain_id != row["domain_id"]
            or actor.harness_id not in allowed_harnesses
            or actor.positive_authority_id is None
        ):
            raise AuthorizationError("relationship activation caller is not a current exact participant")

    def _activate(
        self,
        connection: Any,
        *,
        row: Any,
        when: datetime,
        activation_basis: ActivationBasis,
        approval: Mapping[str, Any] | None = None,
        verified_approval: VerifiedIndependentApproval | None = None,
        policy_exception_id: str | None = None,
        activation_actor: VerifiedActor,
        activation_intent: _PendingActivationIntent,
    ) -> RelationshipGovernanceRecord:
        now = epoch_seconds(when)
        predecessor = connection.execute(
            """
            SELECT * FROM relationship_governance_transactions
             WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
               AND state='active' AND relationship_id<>? LIMIT 1
            """,
            (
                row["domain_id"],
                row["administrator_harness_id"],
                row["subordinate_harness_id"],
                row["relationship_id"],
            ),
        ).fetchone()
        if predecessor is not None:
            predecessor = self.expire_active_in_transaction(
                self.store,
                connection,
                predecessor,
                when=when,
            )
        if predecessor is not None and predecessor["state"] == "active":
            cursor = connection.execute(
                """
                UPDATE relationship_governance_transactions
                   SET state='superseded',lifecycle_revision=lifecycle_revision+1,
                       superseded_by_relationship_id=?,revoked_at=?,updated_at=?
                 WHERE relationship_id=? AND lifecycle_revision=? AND state='active'
                """,
                (
                    row["relationship_id"],
                    now,
                    now,
                    predecessor["relationship_id"],
                    predecessor["lifecycle_revision"],
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("relationship activation raced with predecessor revocation")

        fields: dict[str, Any] = {
            "activation_basis": activation_basis,
            "activated_at": now,
            "updated_at": now,
        }
        if activation_basis == "subordinate_owner_consent":
            if approval is None or verified_approval is None:
                raise AuthorizationError("verified subordinate-owner consent is required")
            fields.update(
                approval_receipt_id=verified_approval.receipt_id,
                approval_receipt_digest=canonical_digest(dict(approval)),
                approval_receipt_json=canonical_json(dict(approval)).decode("utf-8"),
                approval_approver_authority_id=verified_approval.approver_principal_id,
                approval_approver_authority_kind=verified_approval.approver_authority_kind,
                approval_verifier_id=verified_approval.verifier_id,
                approval_signer_key_id=verified_approval.signer_key_id,
                approval_expires_at=verified_approval.expires_at,
            )
        else:
            if policy_exception_id is None:
                raise AuthorizationError("a recorded domain policy exception is required")
            fields["policy_exception_id"] = policy_exception_id
        assignments = ["state='active'", "lifecycle_revision=lifecycle_revision+1"]
        values: list[Any] = []
        for key, value in fields.items():
            assignments.append(f"{key}=?")
            values.append(value)
        values.extend((row["transaction_id"], row["lifecycle_revision"]))
        cursor = connection.execute(
            f"UPDATE relationship_governance_transactions SET {','.join(assignments)} "
            "WHERE transaction_id=? AND lifecycle_revision=? AND state='proposed'",
            tuple(values),
        )
        if cursor.rowcount != 1:
            raise ConflictError("relationship activation lost its lifecycle race")
        self.store.append_audit(
            connection,
            {
                "type": "relationship_activated",
                "relationship_id": row["relationship_id"],
                "relationship_revision": int(row["relationship_revision"]),
                "transaction_id": row["transaction_id"],
                "transaction_digest": row["transaction_digest"],
                "activation_basis": activation_basis,
                "approval_receipt_id": (
                    verified_approval.receipt_id if verified_approval is not None else None
                ),
                "policy_exception_id": policy_exception_id,
                "activation_actor": activation_actor.audit_view(),
                "authority_effect": "custody_only",
                "data_access_authorized": False,
                "semantic_processing_authorized": False,
                "tool_authorized": False,
                "business_effect_authorized": False,
            },
        )
        self._complete_activation_intent(connection, activation_intent)
        activated = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE transaction_id=?",
            (row["transaction_id"],),
        ).fetchone()
        return self._from_row(activated, when=when)

    def accept(
        self,
        relationship_id: str,
        *,
        actor: VerifiedActor,
        approval: Mapping[str, Any],
        expected_transaction_digest: str,
        expected_relationship_revision: int,
        expected_lifecycle_revision: int,
        when: datetime | None = None,
    ) -> RelationshipGovernanceRecord:
        when = when or datetime.now(UTC)
        if not isinstance(self.approval_verifier, IndependentApprovalVerifier):
            raise AuthorizationError("independent relationship approval verifier is required")
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (relationship_id,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("relationship proposal is not visible")
            # Visibility is established from the transport-derived current actor
            # before caller-supplied revisions can disclose that an ID exists.
            self._require_activation_caller(connection, actor=actor, row=row, when=when)
            if (
                row["transaction_digest"] != expected_transaction_digest
                or int(row["relationship_revision"]) != expected_relationship_revision
                or int(row["lifecycle_revision"]) != expected_lifecycle_revision
            ):
                raise ConflictError("relationship consent targets stale proposal state")
            transaction = self._require_current_transaction(connection, row, when=when)
            verified = self.approval_verifier.verify(
                canonical_transaction=str(row["canonical_transaction_json"]).encode("utf-8"),
                approval=approval,
                expected_purpose=RELATIONSHIP_CONSENT_PURPOSE,
                expected_domain_id=row["domain_id"],
                when=when,
            )
            if (
                verified.approver_authority_kind != row["subordinate_owner_kind"]
                or not secrets.compare_digest(
                    verified.approver_principal_id,
                    str(row["subordinate_owner_id"]),
                )
            ):
                raise AuthorizationError(
                    "independent approver is not the current owner of the subordinate endpoint"
                )
            receipt_digest = canonical_digest(dict(approval))
            activation_intent = self._begin_activation_intent(
                connection,
                row,
                activation_basis="subordinate_owner_consent",
                activation_actor=actor,
                evidence=self._owner_consent_activation_evidence(
                    receipt_id=verified.receipt_id,
                    receipt_digest=receipt_digest,
                    approver_authority_id=verified.approver_principal_id,
                    approver_authority_kind=verified.approver_authority_kind,
                    verifier_id=verified.verifier_id,
                    signer_key_id=verified.signer_key_id,
                    expires_at=verified.expires_at,
                ),
                when=when,
            )
            consume_independent_approval(
                connection,
                receipt=verified,
                retain_until=max(
                    int(row["relationship_expires_at"]),
                    verified.expires_at,
                ),
            )
            return self._activate(
                connection,
                row=row,
                when=when,
                activation_basis="subordinate_owner_consent",
                approval=approval,
                verified_approval=verified,
                activation_actor=actor,
                activation_intent=activation_intent,
            )

    def record_policy_exception(
        self,
        exception: RelationshipPolicyException,
        *,
        command: SignedAuthorityCommand | None,
        authority: IssuanceAuthority | None,
        when: datetime | None = None,
    ) -> RelationshipPolicyExceptionRecord:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        with self.store.transaction() as connection:
            # Prove the signed, policy-derived exception authority against only
            # request-known bytes before consulting proposal existence/state.
            resource, expected_request = self.policy_exception_binding(exception)
            policy_revision = require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="organization.relationship.policy_exception.record",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if command is None or authority is None:
                raise AuthorizationError("signed domain policy exception authority is required")
            if (
                command.expected_entity_revision != exception.expected_lifecycle_revision
                or authority.actor.domain_id != exception.domain_id
                or authority.actor.positive_authority_id is None
                or authority.actor.harness_id is None
                or authority.actor.credential_id is None
            ):
                raise AuthorizationError("domain policy exception signer binding is invalid")
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (exception.relationship_id,),
            ).fetchone()
            if row is None or row["domain_id"] != exception.domain_id:
                raise AuthorizationError("relationship proposal is not visible")
            self._require_current_transaction(connection, row, when=when)
            if (
                int(row["relationship_revision"]) != exception.relationship_revision
                or int(row["lifecycle_revision"]) != exception.expected_lifecycle_revision
                or row["transaction_digest"] != exception.relationship_transaction_digest
                or epoch_seconds(exception.expires_at) <= now
                or epoch_seconds(exception.expires_at) > int(row["proposal_expires_at"])
            ):
                raise ConflictError("domain policy exception does not cover the exact current proposal")
            begin_authority_mutation_intent(connection, command=command, authority=authority, when=when)
            exception_json = canonical_json(exception.model_dump(mode="json")).decode("utf-8")
            exception_digest = canonical_digest(exception.model_dump(mode="json"))
            columns = (
                "policy_exception_id", "domain_id", "relationship_transaction_digest",
                "relationship_revision", "policy_revision", "domain_revocation_epoch",
                "administrator_credential_epoch", "subordinate_credential_epoch",
                "signer_authority_kind", "signer_authority_id", "signer_harness_id",
                "signer_credential_id", "signer_credential_epoch", "command_id",
                "policy_decision_id", "command_json", "exception_json", "exception_digest",
                "expires_at", "recorded_at", "lifecycle_revision",
            )
            values = (
                exception.policy_exception_id, exception.domain_id,
                exception.relationship_transaction_digest, exception.relationship_revision,
                policy_revision, int(row["proposal_domain_revocation_epoch"]),
                int(row["proposal_administrator_credential_epoch"]),
                int(row["proposal_subordinate_credential_epoch"]),
                "human" if authority.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS else "guest",
                authority.actor.positive_authority_id, authority.actor.harness_id,
                authority.actor.credential_id, authority.actor.credential_epoch,
                command.command_id, authority.policy_decision_id,
                canonical_json(command.model_dump(mode="json")).decode("utf-8"),
                exception_json, exception_digest, epoch_seconds(exception.expires_at), now, 1,
            )
            try:
                connection.execute(
                    f"INSERT INTO relationship_policy_exceptions({','.join(columns)}) "
                    f"VALUES({','.join('?' for _ in columns)})",
                    values,
                )
            except Exception as exc:
                if _is_integrity_error(exc):
                    raise ConflictError("domain policy exception conflicts or was replayed") from exc
                raise
            self.store.append_audit(
                connection,
                {
                    "type": "relationship_policy_exception_recorded",
                    "policy_exception_id": exception.policy_exception_id,
                    "relationship_id": exception.relationship_id,
                    "transaction_digest": exception.relationship_transaction_digest,
                    "exception_digest": exception_digest,
                    "signer": authority.actor.audit_view(),
                    "command_id": command.command_id,
                    "policy_decision_id": authority.policy_decision_id,
                    "reason": exception.reason,
                },
            )
            complete_authority_mutation_intent(connection, command_id=command.command_id, when=when)
            return RelationshipPolicyExceptionRecord(
                **exception.model_dump(mode="python"),
                recorded_at=when,
                lifecycle_revision=1,
                signer_authority_id=authority.actor.positive_authority_id,
                signer_harness_id=authority.actor.harness_id,
            )

    def activate_with_policy_exception(
        self,
        relationship_id: str,
        *,
        policy_exception_id: str,
        actor: VerifiedActor,
        expected_transaction_digest: str,
        expected_relationship_revision: int,
        expected_lifecycle_revision: int,
        when: datetime | None = None,
    ) -> RelationshipGovernanceRecord:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (relationship_id,),
            ).fetchone()
            exception = connection.execute(
                "SELECT * FROM relationship_policy_exceptions WHERE policy_exception_id=?",
                (policy_exception_id,),
            ).fetchone()
            if row is None or exception is None:
                raise AuthorizationError("relationship policy exception is not visible")
            # Nonparticipants receive the same non-enumerating denial for an
            # existing pair as for an absent identifier.  The raw signer
            # projection is used only to widen this denial check; it never
            # confers authority.  Full signed-byte validation remains mandatory
            # before consumption or activation.
            self._require_activation_caller(
                connection,
                actor=actor,
                row=row,
                when=when,
                additional_harness_id=str(exception["signer_harness_id"]),
            )
            if (
                row["transaction_digest"] != expected_transaction_digest
                or int(row["relationship_revision"]) != expected_relationship_revision
                or int(row["lifecycle_revision"]) != expected_lifecycle_revision
            ):
                raise ConflictError("relationship exception activation targets stale proposal state")
            self._require_current_transaction(connection, row, when=when)
            if (
                exception["domain_id"] != row["domain_id"]
                or exception["relationship_transaction_digest"] != row["transaction_digest"]
                or int(exception["relationship_revision"]) != int(row["relationship_revision"])
                or int(exception["policy_revision"]) != int(row["proposal_policy_revision"])
                or int(exception["domain_revocation_epoch"]) != int(row["proposal_domain_revocation_epoch"])
                or int(exception["administrator_credential_epoch"])
                != int(row["proposal_administrator_credential_epoch"])
                or int(exception["subordinate_credential_epoch"])
                != int(row["proposal_subordinate_credential_epoch"])
                or exception["consumed_at"] is not None
                or exception["revoked_at"] is not None
                or int(exception["expires_at"]) <= now
            ):
                raise AuthorizationError("relationship policy exception is not visible")
            try:
                persisted_exception = self._validate_persisted_policy_exception(
                    connection,
                    exception,
                    row,
                    now=now,
                )
            except AuthenticationError:
                raise AuthorizationError("relationship policy exception is not visible")
            if not self._policy_exception_signer_is_current(
                connection,
                persisted_exception,
                now=now,
            ):
                raise AuthenticationError("domain policy exception signer authority is stale")
            activation_intent = self._begin_activation_intent(
                connection,
                row,
                activation_basis="domain_policy_exception",
                activation_actor=actor,
                evidence=self._policy_exception_activation_evidence(exception),
                when=when,
            )
            cursor = connection.execute(
                """
                UPDATE relationship_policy_exceptions
                   SET consumed_at=?,lifecycle_revision=lifecycle_revision+1
                 WHERE policy_exception_id=? AND lifecycle_revision=?
                   AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?
                """,
                (now, policy_exception_id, exception["lifecycle_revision"], now),
            )
            if cursor.rowcount != 1:
                raise ConflictError("domain policy exception activation lost its replay race")
            return self._activate(
                connection,
                row=row,
                when=when,
                activation_basis="domain_policy_exception",
                policy_exception_id=policy_exception_id,
                activation_actor=actor,
                activation_intent=activation_intent,
            )

    def get(
        self,
        relationship_id: str,
        *,
        authority: IssuanceAuthority | None = None,
        administrative: bool = False,
        when: datetime | None = None,
    ) -> RelationshipGovernanceRecord | None:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, request = self.read_binding(relationship_id)
            action = (
                "organization.relationship.admin_read"
                if administrative
                else "organization.relationship.read"
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=action,
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            if authority is None:
                raise AuthorizationError("authenticated relationship reader is required")
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (relationship_id,),
            ).fetchone()
            if row is None or row["domain_id"] != authority.actor.domain_id:
                return None
            if not administrative and authority.actor.harness_id not in {
                row["administrator_harness_id"],
                row["subordinate_harness_id"],
            }:
                return None
            row = self.expire_active_in_transaction(
                self.store,
                connection,
                row,
                when=when,
            )
            return self._from_row(row, when=when)

    def latest_for_pair(
        self,
        *,
        domain_id: str,
        administrator_harness_id: str,
        subordinate_harness_id: str,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> RelationshipGovernanceRecord | None:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, request = self.pair_read_binding(
                domain_id=domain_id,
                administrator_harness_id=administrator_harness_id,
                subordinate_harness_id=subordinate_harness_id,
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="organization.relationship.admin_read",
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            if authority is None or authority.actor.domain_id != domain_id:
                raise AuthorizationError("relationship pair is not visible to this actor")
            row = connection.execute(
                """
                SELECT * FROM relationship_governance_transactions
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                 ORDER BY relationship_revision DESC LIMIT 1
                """,
                (domain_id, administrator_harness_id, subordinate_harness_id),
            ).fetchone()
            if row is not None:
                row = self.expire_active_in_transaction(
                    self.store,
                    connection,
                    row,
                    when=when,
                )
            return None if row is None else self._from_row(row, when=when)

    def revoke(
        self,
        relationship_id: str,
        *,
        command: SignedAuthorityCommand | None = None,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> bool:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            action = command.action if command is not None else "organization.relationship.revoke"
            if action not in {
                "organization.relationship.revoke",
                "organization.relationship.admin_revoke",
            }:
                raise AuthorizationError("relationship revocation action is invalid")
            row = connection.execute(
                "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
                (relationship_id,),
            ).fetchone()
            if row is None:
                raise ConflictError("relationship lifecycle changed before revocation")
            resource, expected_request = self.revocation_binding(
                relationship_id,
                expected_relationship_revision=int(row["relationship_revision"]),
                expected_lifecycle_revision=(
                    command.expected_entity_revision if command is not None else 0
                ),
                reason=command.reason if command is not None else "missing",
            )
            policy_revision = require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action=action,
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if command is None or authority is None:
                raise AuthorizationError("signed relationship revocation authority is required")
            if row["domain_id"] != authority.actor.domain_id:
                raise ConflictError("relationship lifecycle changed before revocation")
            is_endpoint = authority.actor.harness_id in {
                row["administrator_harness_id"],
                row["subordinate_harness_id"],
            }
            if not is_endpoint and action != "organization.relationship.admin_revoke":
                raise ConflictError("relationship lifecycle changed before revocation")
            current_lifecycle_revision = int(row["lifecycle_revision"])
            exact_target = (
                row["state"] in {"proposed", "active"}
                and current_lifecycle_revision == command.expected_entity_revision
            )
            if not exact_target:
                raise ConflictError("relationship lifecycle changed before revocation")
            lineage = connection.execute(
                """
                SELECT revocation_epoch,lifecycle_revision
                  FROM relationship_governance_lineages
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                """,
                (
                    row["domain_id"],
                    row["administrator_harness_id"],
                    row["subordinate_harness_id"],
                ),
            ).fetchone()
            if (
                lineage is None
                or int(lineage["revocation_epoch"])
                != int(row["proposal_lineage_revocation_epoch"])
            ):
                raise ConflictError("relationship revocation crossed a committed lineage fence")
            begin_authority_mutation_intent(connection, command=command, authority=authority, when=when)
            now = epoch_seconds(when)
            lineage_cursor = connection.execute(
                """
                UPDATE relationship_governance_lineages
                   SET revocation_epoch=revocation_epoch+1,
                       lifecycle_revision=lifecycle_revision+1,
                       last_revoked_at=?,last_revocation_command_id=?,updated_at=?
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                   AND revocation_epoch=? AND lifecycle_revision=?
                """,
                (
                    now,
                    command.command_id,
                    now,
                    row["domain_id"],
                    row["administrator_harness_id"],
                    row["subordinate_harness_id"],
                    lineage["revocation_epoch"],
                    lineage["lifecycle_revision"],
                ),
            )
            if lineage_cursor.rowcount != 1:
                raise ConflictError("relationship revocation lost its lineage race")
            affected = connection.execute(
                """
                SELECT relationship_id,transaction_digest,state,lifecycle_revision
                  FROM relationship_governance_transactions
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                   AND state IN ('proposed','active')
                 ORDER BY relationship_revision
                """,
                (
                    row["domain_id"],
                    row["administrator_harness_id"],
                    row["subordinate_harness_id"],
                ),
            ).fetchall()
            if not affected:
                raise ConflictError("relationship revocation has no current lineage authority to remove")
            cursor = connection.execute(
                """
                UPDATE relationship_governance_transactions
                   SET state='revoked',revoked_at=?,updated_at=?,lifecycle_revision=lifecycle_revision+1
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                   AND state IN ('proposed','active')
                """,
                (
                    now,
                    now,
                    row["domain_id"],
                    row["administrator_harness_id"],
                    row["subordinate_harness_id"],
                ),
            )
            if cursor.rowcount != len(affected):
                raise ConflictError("relationship revocation raced with lineage activation")
            digests = tuple(str(item["transaction_digest"]) for item in affected)
            placeholders = ",".join("?" for _ in digests)
            connection.execute(
                f"""
                UPDATE relationship_policy_exceptions
                   SET revoked_at=?,lifecycle_revision=lifecycle_revision+1
                 WHERE relationship_transaction_digest IN ({placeholders})
                   AND consumed_at IS NULL AND revoked_at IS NULL
                """,
                (now, *digests),
            )
            actor_role = "authorized_administrator"
            if authority.actor.harness_id == row["administrator_harness_id"]:
                actor_role = "owner"
            elif authority.actor.harness_id == row["subordinate_harness_id"]:
                actor_role = "subject_exit"
            self.store.append_audit(
                connection,
                {
                    "type": "relationship_revoked",
                    "relationship_id": relationship_id,
                    "relationship_revision": int(row["relationship_revision"]),
                    "previous_lifecycle_revision": int(row["lifecycle_revision"]),
                    "revoked_at": when.isoformat(),
                    "revocation_actor": authority.actor.audit_view(),
                    "actor_role": actor_role,
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": policy_revision,
                    "command_id": command.command_id,
                    "reason": command.reason,
                    "target_state_at_commit": row["state"],
                    "lineage_revocation_epoch": int(lineage["revocation_epoch"]) + 1,
                    "affected_relationship_ids": [
                        item["relationship_id"] for item in affected
                    ],
                    "authority_effect": "none_for_entire_exact_directed_lineage",
                },
            )
            complete_authority_mutation_intent(connection, command_id=command.command_id, when=when)
            return True

    @classmethod
    def authority_binding_denial(
        cls,
        connection: Any,
        row: Any,
        *,
        current_policy_revision: int,
        approval_verifier: IndependentApprovalVerifier | None = None,
        when: datetime | None = None,
    ) -> str | None:
        """Validate active consent, owner, policy, domain, and credential epochs."""

        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if row is None:
            return "missing_relationship_acceptance"
        try:
            cls._transaction_from_row(row)
        except AuthenticationError:
            return "stale_relationship_authority_binding"
        if int(row["relationship_expires_at"]) <= now:
            return "relationship_expired"
        if row["state"] != "active":
            return "missing_relationship_acceptance"
        if row["activation_basis"] == "subordinate_owner_consent":
            if not cls._persisted_owner_consent_is_valid(
                connection,
                row,
                approval_verifier=approval_verifier,
            ):
                return "missing_relationship_acceptance"
        elif row["activation_basis"] == "domain_policy_exception":
            if row["policy_exception_id"] is None:
                return "missing_relationship_acceptance"
            exception = connection.execute(
                "SELECT * FROM relationship_policy_exceptions WHERE policy_exception_id=?",
                (row["policy_exception_id"],),
            ).fetchone()
            if exception is None:
                return "stale_relationship_policy_exception"
            try:
                persisted_exception = cls._validate_persisted_policy_exception(
                    connection,
                    exception,
                    row,
                    now=now,
                )
            except AuthenticationError:
                return "stale_relationship_policy_exception"
            if not cls._persisted_activation_intent_is_valid(
                connection,
                row,
                activation_basis="domain_policy_exception",
                evidence=cls._policy_exception_activation_evidence(exception),
                additional_activation_actor=persisted_exception.command.actor,
            ):
                return "stale_relationship_policy_exception"
            if not cls._policy_exception_signer_is_current(
                connection,
                persisted_exception,
                now=now,
            ):
                return "stale_relationship_policy_exception"
        else:
            return "missing_relationship_acceptance"
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (row["domain_id"],),
        ).fetchone()
        if (
            domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != current_policy_revision
            or int(row["proposal_policy_revision"]) != current_policy_revision
            or int(domain["revocation_epoch"]) != int(row["proposal_domain_revocation_epoch"])
        ):
            return "stale_relationship_authority_binding"
        try:
            administrator = cls._endpoint_snapshot(
                connection,
                domain_id=row["domain_id"],
                harness_id=row["administrator_harness_id"],
                now=now,
            )
            subordinate = cls._endpoint_snapshot(
                connection,
                domain_id=row["domain_id"],
                harness_id=row["subordinate_harness_id"],
                now=now,
            )
            transaction = cls._transaction_from_row(row)
            lineage = connection.execute(
                """
                SELECT revocation_epoch FROM relationship_governance_lineages
                 WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
                """,
                (
                    row["domain_id"],
                    row["administrator_harness_id"],
                    row["subordinate_harness_id"],
                ),
            ).fetchone()
        except Exception:
            return "stale_relationship_authority_binding"
        matches = (
            administrator.owner_kind == row["administrator_owner_kind"]
            and administrator.owner_id == row["administrator_owner_id"]
            and subordinate.owner_kind == row["subordinate_owner_kind"]
            and subordinate.owner_id == row["subordinate_owner_id"]
            and administrator.credential_epoch
            == int(row["proposal_administrator_credential_epoch"])
            and subordinate.credential_epoch
            == int(row["proposal_subordinate_credential_epoch"])
            and transaction.relationship.relationship_id == row["relationship_id"]
            and transaction.relationship.revision == int(row["relationship_revision"])
            and lineage is not None
            and int(lineage["revocation_epoch"])
            == int(row["proposal_lineage_revocation_epoch"])
            and transaction.lineage_revocation_epoch
            == int(row["proposal_lineage_revocation_epoch"])
        )
        return None if matches else "stale_relationship_authority_binding"

    def _cascade_revoke_for_harness_in_transaction(
        self,
        connection: Any,
        *,
        harness_id: str,
        when: datetime,
        reason: str,
    ) -> int:
        """Identity-offboarding hook; the caller owns the authenticated transaction."""

        now = epoch_seconds(when)
        rows = connection.execute(
            """
            SELECT relationship_id,lifecycle_revision
              FROM relationship_governance_transactions
             WHERE (administrator_harness_id=? OR subordinate_harness_id=?)
               AND state IN ('proposed','active')
            """,
            (harness_id, harness_id),
        ).fetchall()
        count = 0
        for row in rows:
            cursor = connection.execute(
                """
                UPDATE relationship_governance_transactions
                   SET state='revoked',revoked_at=?,updated_at=?,lifecycle_revision=lifecycle_revision+1
                 WHERE relationship_id=? AND lifecycle_revision=? AND state IN ('proposed','active')
                """,
                (now, now, row["relationship_id"], row["lifecycle_revision"]),
            )
            count += int(cursor.rowcount)
        connection.execute(
            """
            UPDATE relationship_policy_exceptions
               SET revoked_at=?,lifecycle_revision=lifecycle_revision+1
             WHERE signer_harness_id=? AND consumed_at IS NULL AND revoked_at IS NULL
            """,
            (now, harness_id),
        )
        if count:
            self.store.append_audit(
                connection,
                {
                    "type": "relationships_offboarding_revoked",
                    "harness_id": harness_id,
                    "count": count,
                    "reason": reason,
                    "revoked_at": when.isoformat(),
                },
            )
        return count


__all__ = [
    "AssignmentScope",
    "RELATIONSHIP_CONSENT_PURPOSE",
    "RelationshipConsentTransaction",
    "RelationshipGovernanceRecord",
    "RelationshipPolicyException",
    "RelationshipPolicyExceptionRecord",
    "RelationshipService",
]
