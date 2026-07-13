"""Exact, current positive-authority evidence for administrative mutations.

An identifier naming an allow decision is not authority by itself.  Every
administrative service re-loads the decision in its mutation transaction and
binds it to the authenticated actor, exact action/resource/request digest,
current policy revision, and a short freshness window.
"""

from __future__ import annotations

import json
import secrets
import sqlite3
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.security.signatures import canonical_digest, canonical_json, verify_signature


AUTHORITY_COMMAND_VERSION = 1
AUTHORITY_COMMAND_PURPOSE = "agentnet.authority.command.v1"


class IssuanceAuthority(BaseModel):
    """A transport-authenticated actor plus one pre-recorded exact allow."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    actor: VerifiedActor
    policy_decision_id: str = Field(min_length=1)


class SignedAuthorityCommand(BaseModel):
    """One exact, short-lived administrative command signed by its actor.

    The signed bytes bind both policy and entity revisions.  The request digest
    binds the separate typed mutation payload, so changing a target, reason,
    scope, expiry, or any other request field invalidates both the policy
    decision and the credential signature.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    command_version: Literal[1] = AUTHORITY_COMMAND_VERSION
    command_id: str = Field(default_factory=lambda: str(uuid4()), min_length=1, max_length=128)
    actor: VerifiedActor
    action: str = Field(min_length=1, max_length=256)
    resource: str = Field(min_length=1, max_length=512)
    request_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_policy_revision: int = Field(ge=1)
    expected_entity_revision: int = Field(ge=0)
    reason: str = Field(min_length=1, max_length=512)
    issued_at: datetime
    expires_at: datetime
    approval_threshold: int = Field(default=1, ge=1, le=5)
    signature: str = Field(min_length=1, max_length=2048)

    @model_validator(mode="after")
    def validate_command_window(self) -> "SignedAuthorityCommand":
        if self.issued_at.tzinfo is None or self.expires_at.tzinfo is None:
            raise ValueError("authority command timestamps must be timezone-aware")
        if self.expires_at <= self.issued_at:
            raise ValueError("authority command expiry must follow issuance")
        if int((self.expires_at - self.issued_at).total_seconds()) > 300:
            raise ValueError("authority command lifetime cannot exceed 300 seconds")
        if not self.reason.strip():
            raise ValueError("authority command reason cannot be blank")
        return self

    def signed_fields(self) -> dict[str, Any]:
        return {
            "command_version": self.command_version,
            "command_id": self.command_id,
            "actor": self.actor.audit_view(),
            "action": self.action,
            "resource": self.resource,
            "request_digest": self.request_digest,
            "expected_policy_revision": self.expected_policy_revision,
            "expected_entity_revision": self.expected_entity_revision,
            "reason": self.reason,
            "issued_at": self.issued_at.isoformat(),
            "expires_at": self.expires_at.isoformat(),
            "approval_threshold": self.approval_threshold,
        }

    @classmethod
    def signing_fields(
        cls,
        *,
        command_id: str,
        actor: VerifiedActor,
        action: str,
        resource: str,
        request_digest: str,
        expected_policy_revision: int,
        expected_entity_revision: int,
        reason: str,
        issued_at: datetime,
        expires_at: datetime,
        approval_threshold: int = 1,
    ) -> dict[str, Any]:
        """Return the only accepted preimage shape for an external signer."""

        unsigned = {
            "command_version": AUTHORITY_COMMAND_VERSION,
            "command_id": command_id,
            "actor": actor.audit_view(),
            "action": action,
            "resource": resource,
            "request_digest": request_digest,
            "expected_policy_revision": expected_policy_revision,
            "expected_entity_revision": expected_entity_revision,
            "reason": reason,
            "issued_at": issued_at.isoformat(),
            "expires_at": expires_at.isoformat(),
            "approval_threshold": approval_threshold,
        }
        # Validate every non-signature field before a caller sends it to a KMS.
        cls.model_validate({**unsigned, "signature": "pending"})
        return unsigned


def _epoch_seconds(value: datetime) -> int:
    if value.tzinfo is None:
        raise ValidationError("security timestamps must be timezone-aware")
    return int(value.timestamp())


def require_current_authority_decision(
    connection: sqlite3.Connection,
    *,
    authority: IssuanceAuthority | None,
    expected_action: str,
    expected_resource: str,
    expected_request: dict[str, Any],
    when: datetime,
    max_age_seconds: int = 300,
) -> int:
    """Validate exact positive authority in the caller's mutation transaction.

    Returns the authoritative policy revision.  Missing evidence is an
    authorization failure rather than a compatibility bypass.
    """

    if authority is None:
        raise AuthorizationError("exact issuance authority evidence is required")
    if max_age_seconds < 1 or max_age_seconds > 600:
        raise ValueError("authority decision freshness must be between one and 600 seconds")

    # Local import avoids an import cycle: policy owns actor-state evaluation
    # and imports TaskGrantService, which in turn consumes this helper.
    from agentnet.authorization.policy import validate_actor_state

    row = connection.execute(
        "SELECT * FROM policy_decisions WHERE decision_id=?",
        (authority.policy_decision_id,),
    ).fetchone()
    if row is None:
        raise AuthorizationError("issuance authority decision is unavailable")

    now = _epoch_seconds(when)
    occurred_at = int(row["occurred_at"])
    if occurred_at > now or occurred_at < now - max_age_seconds:
        raise AuthorizationError("issuance authority decision is stale")
    if not bool(row["allowed"]):
        raise AuthorizationError("issuance authority decision denied the operation")

    actor_bytes = canonical_json(authority.actor.audit_view()).decode("utf-8")
    if not secrets.compare_digest(str(row["actor_json"]), actor_bytes):
        raise AuthorizationError("issuance authority actor binding mismatch")
    if row["action"] != expected_action:
        raise AuthorizationError("issuance authority action binding mismatch")

    try:
        resource = json.loads(row["resource_json"])
        context = json.loads(row["context_json"])
    except (TypeError, ValueError):
        raise AuthorizationError("issuance authority decision is malformed") from None
    if resource != {"id": expected_resource}:
        raise AuthorizationError("issuance authority resource binding mismatch")
    if not isinstance(context, dict) or context.get("request") != expected_request:
        raise AuthorizationError("issuance authority request binding mismatch")

    denial, current_revision = validate_actor_state(
        connection,
        actor=authority.actor,
        expected_policy_revision=int(row["policy_revision"]),
        when=datetime.fromtimestamp(now, UTC),
    )
    if denial is not None:
        raise AuthorizationError(f"issuance actor is not current: {denial}")

    entitlement_id = context.get("entitlement_id")
    if authority.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
        if not isinstance(entitlement_id, str) or not entitlement_id:
            raise AuthorizationError("issuance authority lacks exact positive entitlement evidence")
        entitlement = connection.execute(
            "SELECT * FROM entitlements WHERE entitlement_id=?",
            (entitlement_id,),
        ).fetchone()
        if (
            entitlement is None
            or entitlement["domain_id"] != authority.actor.domain_id
            or entitlement["principal_id"] != authority.actor.principal_id
            or entitlement["action"] != expected_action
            or entitlement["resource_pattern"] not in {expected_resource, "*"}
            or int(entitlement["revision"]) != current_revision
            or entitlement["revoked_at"] is not None
            or (
                entitlement["expires_at"] is not None
                and int(entitlement["expires_at"]) <= now
            )
        ):
            raise AuthorizationError("issuance authority entitlement is no longer current")
    elif authority.actor.kind is ActorKind.HOST_GUEST_HARNESS:
        grant_id = context.get("task_grant_id")
        if not isinstance(grant_id, str) or not grant_id:
            raise AuthorizationError("guest issuance authority lacks exact task grant evidence")
        grant_row = connection.execute(
            "SELECT * FROM task_grants WHERE grant_id=?",
            (grant_id,),
        ).fetchone()
        try:
            grant = json.loads(grant_row["grant_json"]) if grant_row is not None else None
        except (TypeError, ValueError):
            grant = None
        if (
            grant_row is None
            or not isinstance(grant, dict)
            or grant_row["domain_id"] != authority.actor.domain_id
            or grant_row["principal_id"] != authority.actor.guest_id
            or grant_row["harness_id"] != authority.actor.harness_id
            or grant_row["revoked_at"] is not None
            or int(grant_row["expires_at"]) <= now
        ):
            raise AuthorizationError("guest issuance authority grant is no longer current")
    return current_revision


def require_signed_authority_command(
    connection: sqlite3.Connection,
    *,
    command: SignedAuthorityCommand | None,
    authority: IssuanceAuthority | None,
    expected_action: str,
    expected_resource: str,
    expected_request: dict[str, Any],
    when: datetime,
) -> int:
    """Verify the command signature and exact current allow in one transaction."""

    if command is None:
        raise AuthorizationError("signed authority command is required")
    if authority is None:
        raise AuthorizationError("exact authenticated authority evidence is required")
    if command.approval_threshold != 1:
        # Multi-party operations must use a service that cryptographically
        # verifies and consumes independent approval receipts.  Never treat a
        # caller-provided count as proof.
        raise AuthorizationError("independent approval evidence is required for this threshold")
    now = _epoch_seconds(when)
    issued_at = _epoch_seconds(command.issued_at)
    expires_at = _epoch_seconds(command.expires_at)
    if issued_at > now or now >= expires_at:
        raise AuthenticationError("authority command is outside its validity interval")
    if not secrets.compare_digest(
        canonical_json(command.actor.audit_view()),
        canonical_json(authority.actor.audit_view()),
    ):
        raise AuthenticationError("authority command actor binding mismatch")
    if command.action != expected_action or command.resource != expected_resource:
        raise AuthenticationError("authority command action or resource binding mismatch")
    expected_digest = canonical_digest(expected_request)
    if not secrets.compare_digest(command.request_digest, expected_digest):
        raise AuthenticationError("authority command request digest mismatch")
    if command.actor.kind not in {
        ActorKind.VERIFIED_HUMAN_HARNESS,
        ActorKind.HOST_GUEST_HARNESS,
    }:
        raise AuthorizationError("authority commands require positive human authority")
    if command.actor.credential_id is None or command.actor.harness_id is None:
        raise AuthenticationError("authority command actor lacks a credential binding")

    binding = load_credential_binding_from_connection(connection, command.actor.credential_id)
    binding.require_active(now=now)
    expected_binding = (
        command.actor.domain_id,
        command.actor.harness_id,
        command.actor.credential_id,
        command.actor.credential_epoch,
        command.actor.binding_assurance,
    )
    actual_binding = (
        binding.domain_id,
        binding.harness_id,
        binding.credential_id,
        binding.credential_epoch,
        binding.binding_assurance,
    )
    if actual_binding != expected_binding:
        raise AuthenticationError("authority command credential binding mismatch")
    verify_signature(
        binding.public_key_pem,
        AUTHORITY_COMMAND_PURPOSE,
        command.signed_fields(),
        command.signature,
    )

    revision = require_current_authority_decision(
        connection,
        authority=authority,
        expected_action=expected_action,
        expected_resource=expected_resource,
        expected_request={"request_digest": command.request_digest},
        when=when,
    )
    if command.expected_policy_revision != revision:
        raise ConflictError("authority command policy revision is stale")
    return revision


def begin_authority_mutation_intent(
    connection: sqlite3.Connection,
    *,
    command: SignedAuthorityCommand,
    authority: IssuanceAuthority,
    when: datetime,
) -> None:
    """Persist audit intent before mutation and fence command replay."""

    prior_decision_use = connection.execute(
        "SELECT intent_id FROM audit_intents WHERE policy_decision_id=? LIMIT 1",
        (authority.policy_decision_id,),
    ).fetchone()
    if prior_decision_use is not None:
        raise ConflictError("authority decision was already consumed")
    try:
        connection.execute(
            """
            INSERT INTO audit_intents(
                intent_id,action,resource_id,actor_json,policy_decision_id,
                request_digest,state,created_at
            ) VALUES(?,?,?,?,?,?,'pending',?)
            """,
            (
                command.command_id,
                command.action,
                command.resource,
                canonical_json(command.actor.audit_view()).decode("utf-8"),
                authority.policy_decision_id,
                command.request_digest,
                _epoch_seconds(when),
            ),
        )
    except sqlite3.IntegrityError as exc:
        raise ConflictError("authority command was already consumed") from exc


def complete_authority_mutation_intent(
    connection: sqlite3.Connection,
    *,
    command_id: str,
    when: datetime,
) -> None:
    cursor = connection.execute(
        "UPDATE audit_intents SET state='completed',completed_at=? WHERE intent_id=? AND state='pending'",
        (_epoch_seconds(when), command_id),
    )
    if cursor.rowcount != 1:
        raise ConflictError("authority command audit intent is not pending")


def require_current_approver_entitlement(
    connection: sqlite3.Connection,
    *,
    domain_id: str,
    approver_principal_id: str,
    action: str,
    resource: str,
    policy_revision: int,
    when: datetime,
) -> None:
    """Require a current positive entitlement for an approval signer."""

    principal = connection.execute(
        "SELECT * FROM principals WHERE principal_id=?",
        (approver_principal_id,),
    ).fetchone()
    if (
        principal is None
        or principal["domain_id"] != domain_id
        or principal["status"] != "active"
    ):
        raise AuthorizationError("approval signer is not a current domain principal")

    now = _epoch_seconds(when)
    rows = connection.execute(
        """
        SELECT * FROM entitlements
         WHERE domain_id=? AND principal_id=? AND action=?
           AND resource_pattern IN (?, '*')
        """,
        (domain_id, approver_principal_id, action, resource),
    ).fetchall()
    for row in rows:
        if int(row["revision"]) != policy_revision:
            continue
        if row["revoked_at"] is not None:
            continue
        if row["expires_at"] is not None and int(row["expires_at"]) <= now:
            continue
        return
    raise AuthorizationError("approval signer lacks current positive approval authority")
