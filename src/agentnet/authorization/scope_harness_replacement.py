"""Owner-approved replacement of one expired collaboration-scope harness."""

from __future__ import annotations

import hashlib
import json
import secrets
import time
from collections.abc import Callable, Mapping
from datetime import UTC, datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

from agentnet.approval import IndependentApprovalVerifier, consume_independent_approval
from agentnet.authorization.communication_scope_service import (
    CollaborationScope,
    CollaborationScopeService,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend

SCOPE_HARNESS_REPLACEMENT_SCHEMA = "agentnet.scope-harness-replacement.v1"
SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE = "identity.credential.recover.approve"
_REQUEST_TTL_SECONDS = 600
_STRICT = ConfigDict(extra="forbid", frozen=True)


class ScopeHarnessReplacementRequest(BaseModel):
    """Exact owner-approved membership cutover request."""

    model_config = _STRICT

    schema_version: Literal["agentnet.scope-harness-replacement.v1"] = (
        SCOPE_HARNESS_REPLACEMENT_SCHEMA
    )
    request_id: str = Field(min_length=16, max_length=128)
    domain_id: str = Field(min_length=1, max_length=256)
    owner_principal_id: str = Field(min_length=1, max_length=256)
    owner_harness_id: str = Field(min_length=1, max_length=256)
    scope_id: str = Field(min_length=16, max_length=256)
    expected_scope_revision: int = Field(ge=1)
    expected_scope_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    expected_membership_sequence: int = Field(ge=1)
    expected_policy_revision: int = Field(ge=1)
    expected_domain_revocation_epoch: int = Field(ge=1)
    old_harness_id: str = Field(min_length=1, max_length=256)
    old_credential_id: str = Field(min_length=1, max_length=256)
    old_credential_epoch: int = Field(ge=1)
    new_harness_id: str = Field(min_length=1, max_length=256)
    new_credential_id: str = Field(min_length=1, max_length=256)
    new_credential_epoch: int = Field(ge=1)
    role: Literal["member"]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)

    @property
    def canonical_transaction(self) -> bytes:
        return canonical_json(self.model_dump(mode="json"))

    @property
    def digest(self) -> str:
        return hashlib.sha256(self.canonical_transaction).hexdigest()


class ScopeHarnessReplacementResult(BaseModel):
    """Durable result of one exact membership cutover."""

    model_config = _STRICT

    schema_version: Literal["agentnet.scope-harness-replacement.result.v1"] = (
        "agentnet.scope-harness-replacement.result.v1"
    )
    request_id: str
    request_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    scope_id: str
    old_harness_id: str
    new_harness_id: str
    role: Literal["member"]
    membership_sequence: int = Field(ge=2)
    scope_revision: int = Field(ge=2)
    scope_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    audit_record_hash: str = Field(pattern=r"^[0-9a-f]{64}$")
    idempotent_repeat: bool


class ScopeHarnessReplacementService:
    """Replace an expired same-principal member under exact independent approval."""

    def __init__(
        self,
        store: StoreBackend,
        approval_verifier: IndependentApprovalVerifier,
        *,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.store = store
        self.approval_verifier = approval_verifier
        self.clock = clock
        self.scopes = CollaborationScopeService(store, clock=clock)

    @staticmethod
    def _current_credential(connection: Any, harness_id: str) -> tuple[Any, Any]:
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?",
            (harness_id,),
        ).fetchone()
        if harness is None:
            raise AuthorizationError("scope replacement harness is unavailable")
        credentials = connection.execute(
            """SELECT * FROM credentials
                 WHERE harness_id=? AND epoch=? AND status='active'
                 ORDER BY credential_id""",
            (harness_id, int(harness["credential_epoch"])),
        ).fetchall()
        if len(credentials) != 1:
            raise AuthorizationError("scope replacement current credential is ambiguous")
        return harness, credentials[0]

    @staticmethod
    def _require_request_window(
        *,
        issued_at: int,
        expires_at: int,
        now: int,
    ) -> None:
        if (
            issued_at > now
            or now >= expires_at
            or expires_at <= issued_at
            or expires_at - issued_at > _REQUEST_TTL_SECONDS
        ):
            raise AuthenticationError("scope replacement request is expired or overlong")

    def _require_owner_and_scope(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        scope_id: str,
        when: datetime,
    ) -> CollaborationScope:
        self.scopes._require_actor(connection, actor=actor, when=when)
        row = self.scopes._scope_row(connection, scope_id)
        if row is None:
            raise AuthorizationError("collaboration scope is unavailable")
        scope = self.scopes._scope_from_row(connection, row)
        if (
            scope.state != "active"
            or actor.domain_id != scope.domain_id
            or actor.principal_id != scope.owner_principal_id
        ):
            raise AuthorizationError(
                "scope replacement requires a current harness of the exact scope-owning principal"
            )
        return scope

    def _require_harness_pair(
        self,
        connection: Any,
        *,
        scope: CollaborationScope,
        old_harness_id: str,
        new_harness_id: str,
        role: str,
        now: int,
    ) -> tuple[Any, Any]:
        if old_harness_id == new_harness_id or role != "member":
            raise ValidationError("scope replacement harness pair is invalid")
        member = connection.execute(
            """SELECT * FROM collaboration_scope_members
                 WHERE scope_id=? AND harness_id=?""",
            (scope.scope_id, old_harness_id),
        ).fetchone()
        if (
            member is None
            or member["state"] != "active"
            or member["role"] != role
            or member["authority_kind"] != "principal"
            or member["authority_id"] != scope.owner_principal_id
        ):
            raise AuthorizationError("expired scope member does not match the owner-approved target")
        old_harness, old_credential = self._current_credential(connection, old_harness_id)
        new_harness, new_credential = self._current_credential(connection, new_harness_id)
        if any(
            harness["domain_id"] != scope.domain_id
            or harness["principal_id"] != scope.owner_principal_id
            or harness["status"] != "active"
            for harness in (old_harness, new_harness)
        ):
            raise AuthorizationError("scope replacement requires active same-principal harnesses")
        if int(old_credential["expires_at"]) > now:
            raise AuthorizationError("scope replacement requires the old current credential to be expired")
        if (
            int(new_credential["not_before"]) > now
            or int(new_credential["expires_at"]) <= now
        ):
            raise AuthorizationError("scope replacement requires a current replacement credential")
        existing_new = connection.execute(
            """SELECT state FROM collaboration_scope_members
                 WHERE scope_id=? AND harness_id=?""",
            (scope.scope_id, new_harness_id),
        ).fetchone()
        if existing_new is not None:
            raise ConflictError("replacement harness already has scope membership history")
        return old_credential, new_credential

    def prepare(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str,
        old_harness_id: str,
        new_harness_id: str,
        role: Literal["member"],
        request_id: str,
        issued_at: int,
        expires_at: int,
    ) -> ScopeHarnessReplacementRequest:
        now = int(self.clock())
        self._require_request_window(issued_at=issued_at, expires_at=expires_at, now=now)
        when = datetime.fromtimestamp(now, UTC)
        with self.store.transaction() as connection:
            scope = self._require_owner_and_scope(
                connection,
                actor=actor,
                scope_id=scope_id,
                when=when,
            )
            old_credential, new_credential = self._require_harness_pair(
                connection,
                scope=scope,
                old_harness_id=old_harness_id,
                new_harness_id=new_harness_id,
                role=role,
                now=now,
            )
            return ScopeHarnessReplacementRequest(
                request_id=request_id,
                domain_id=scope.domain_id,
                owner_principal_id=scope.owner_principal_id,
                owner_harness_id=scope.owner_harness_id,
                scope_id=scope.scope_id,
                expected_scope_revision=scope.revision,
                expected_scope_digest=scope.scope_digest,
                expected_policy_revision=scope.policy_revision,
                expected_domain_revocation_epoch=scope.domain_revocation_epoch,
                expected_membership_sequence=scope.membership_sequence,
                old_harness_id=old_harness_id,
                old_credential_id=str(old_credential["credential_id"]),
                old_credential_epoch=int(old_credential["epoch"]),
                new_harness_id=new_harness_id,
                new_credential_id=str(new_credential["credential_id"]),
                new_credential_epoch=int(new_credential["epoch"]),
                role=role,
                issued_at=issued_at,
                expires_at=expires_at,
            )

    def _completed_result(
        self,
        connection: Any,
        *,
        scope: CollaborationScope,
        request: ScopeHarnessReplacementRequest,
    ) -> ScopeHarnessReplacementResult | None:
        if (
            scope.revision != request.expected_scope_revision + 1
            or scope.membership_sequence != request.expected_membership_sequence + 1
        ):
            return None
        audit_row = connection.execute(
            "SELECT record_json FROM audit_log WHERE record_hash=?",
            (connection.execute(
                "SELECT audit_record_hash FROM collaboration_scopes WHERE scope_id=?",
                (scope.scope_id,),
            ).fetchone()["audit_record_hash"],),
        ).fetchone()
        if audit_row is None:
            return None
        try:
            audit = json.loads(str(audit_row["record_json"]))
        except (TypeError, ValueError):
            return None
        if (
            audit.get("action") != "collaboration_scope.harness_replaced"
            or audit.get("request_digest") != request.digest
            or audit.get("old_harness_id") != request.old_harness_id
            or audit.get("new_harness_id") != request.new_harness_id
            or audit.get("scope_digest") != scope.scope_digest
        ):
            return None
        return ScopeHarnessReplacementResult(
            request_id=request.request_id,
            request_digest=request.digest,
            scope_id=request.scope_id,
            old_harness_id=request.old_harness_id,
            new_harness_id=request.new_harness_id,
            role=request.role,
            membership_sequence=scope.membership_sequence,
            scope_revision=scope.revision,
            scope_digest=scope.scope_digest,
            audit_record_hash=str(audit.get("record_hash", "")) or str(
                connection.execute(
                    "SELECT audit_record_hash FROM collaboration_scopes WHERE scope_id=?",
                    (scope.scope_id,),
                ).fetchone()["audit_record_hash"]
            ),
            idempotent_repeat=True,
        )

    def replace(
        self,
        *,
        actor: VerifiedActor,
        request: ScopeHarnessReplacementRequest,
        approval: Mapping[str, Any],
    ) -> ScopeHarnessReplacementResult:
        now = int(self.clock())
        when = datetime.fromtimestamp(now, UTC)
        with self.store.transaction() as connection:
            scope = self._require_owner_and_scope(
                connection,
                actor=actor,
                scope_id=request.scope_id,
                when=when,
            )
            if (
                request.domain_id != scope.domain_id
                or request.owner_principal_id != scope.owner_principal_id
                or request.owner_harness_id != scope.owner_harness_id
            ):
                raise AuthenticationError("scope replacement owner binding changed")
            completed = self._completed_result(connection, scope=scope, request=request)
            if completed is not None:
                return completed
            self._require_request_window(
                issued_at=request.issued_at,
                expires_at=request.expires_at,
                now=now,
            )
            verified = self.approval_verifier.verify(
                canonical_transaction=request.canonical_transaction,
                approval=approval,
                expected_purpose=SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE,
                expected_domain_id=request.domain_id,
                when=when,
            )
            if (
                verified.approver_authority_kind != "human"
                or verified.approver_principal_id != request.owner_principal_id
            ):
                raise AuthorizationError("scope replacement requires exact owner approval")
            if (
                scope.revision != request.expected_scope_revision
                or scope.membership_sequence != request.expected_membership_sequence
                or scope.policy_revision != request.expected_policy_revision
                or scope.domain_revocation_epoch
                != request.expected_domain_revocation_epoch
                or not secrets.compare_digest(scope.scope_digest, request.expected_scope_digest)
            ):
                raise ConflictError("scope replacement revision conflict")
            old_credential, new_credential = self._require_harness_pair(
                connection,
                scope=scope,
                old_harness_id=request.old_harness_id,
                new_harness_id=request.new_harness_id,
                role=request.role,
                now=now,
            )
            if (
                old_credential["credential_id"] != request.old_credential_id
                or int(old_credential["epoch"]) != request.old_credential_epoch
                or new_credential["credential_id"] != request.new_credential_id
                or int(new_credential["epoch"]) != request.new_credential_epoch
            ):
                raise AuthenticationError("scope replacement credential binding changed")
            consume_independent_approval(
                connection,
                receipt=verified,
                retain_until=request.expires_at,
            )

            next_membership_sequence = scope.membership_sequence + 1
            next_revision = scope.revision + 1
            old_row = connection.execute(
                """SELECT * FROM collaboration_scope_members
                     WHERE scope_id=? AND harness_id=?""",
                (scope.scope_id, request.old_harness_id),
            ).fetchone()
            removed_digest = self.scopes._member_digest(
                scope_id=scope.scope_id,
                authority_kind="principal",
                authority_id=scope.owner_principal_id,
                harness_id=request.old_harness_id,
                role=request.role,
                joined_at=int(old_row["joined_at"]),
                state="removed",
                joined_sequence=int(old_row["joined_sequence"]),
                removed_sequence=next_membership_sequence,
                removed_at=now,
            )
            cursor = connection.execute(
                """UPDATE collaboration_scope_members
                      SET state='removed',removed_sequence=?,member_digest=?,removed_at=?
                    WHERE scope_id=? AND harness_id=? AND state='active'
                      AND member_digest=?""",
                (
                    next_membership_sequence,
                    removed_digest,
                    now,
                    scope.scope_id,
                    request.old_harness_id,
                    old_row["member_digest"],
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("scope replacement membership changed")
            new_digest = self.scopes._member_digest(
                scope_id=scope.scope_id,
                authority_kind="principal",
                authority_id=scope.owner_principal_id,
                harness_id=request.new_harness_id,
                role=request.role,
                joined_at=now,
                joined_sequence=next_membership_sequence,
            )
            connection.execute(
                """INSERT INTO collaboration_scope_members(
                    scope_id,authority_kind,authority_id,harness_id,role,state,
                    joined_sequence,removed_sequence,member_digest,joined_at,removed_at
                ) VALUES(?,'principal',?,?,?,'active',?,NULL,?,?,NULL)""",
                (
                    scope.scope_id,
                    scope.owner_principal_id,
                    request.new_harness_id,
                    request.role,
                    next_membership_sequence,
                    new_digest,
                    now,
                ),
            )
            cursor = connection.execute(
                """UPDATE collaboration_scopes
                      SET membership_sequence=?,revision=?,state_reason='harness_replaced',updated_at=?
                    WHERE scope_id=? AND revision=? AND membership_sequence=?
                      AND scope_digest=? AND state='active'""",
                (
                    next_membership_sequence,
                    next_revision,
                    now,
                    scope.scope_id,
                    scope.revision,
                    scope.membership_sequence,
                    scope.scope_digest,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("scope replacement revision conflict")
            updated_row = self.scopes._scope_row(connection, scope.scope_id)
            members = self.scopes._members(connection, row=updated_row)[1]
            next_scope_digest = self.scopes._scope_digest(
                scope_id=scope.scope_id,
                scope_kind=scope.scope_kind,
                domain_id=scope.domain_id,
                owner_principal_id=scope.owner_principal_id,
                owner_harness_id=scope.owner_harness_id,
                members=members,
                allowed_actions=scope.allowed_actions,
                allowed_resource_prefixes=scope.allowed_resource_prefixes,
                allowed_classifications=scope.allowed_classifications,
                canonical_references=scope.canonical_references,
                policy_revision=scope.policy_revision,
                domain_revocation_epoch=scope.domain_revocation_epoch,
                control_sequence=scope.control_sequence,
                membership_sequence=next_membership_sequence,
                proposal_digest=scope.proposal_digest,
                revision=next_revision,
                state="active",
                state_reason="harness_replaced",
                created_at=scope.created_at,
                updated_at=now,
                expires_at=scope.expires_at,
                revoked_at=None,
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "collaboration_scope.harness_replaced",
                    "actor": actor.audit_view(),
                    "approval_receipt_id": verified.receipt_id,
                    "approver_principal_id": verified.approver_principal_id,
                    "membership_sequence": next_membership_sequence,
                    "new_harness_id": request.new_harness_id,
                    "old_harness_id": request.old_harness_id,
                    "previous_scope_digest": scope.scope_digest,
                    "request_digest": request.digest,
                    "request_id": request.request_id,
                    "role": request.role,
                    "scope_digest": next_scope_digest,
                    "scope_id": scope.scope_id,
                    "scope_revision": next_revision,
                },
            )
            cursor = connection.execute(
                """UPDATE collaboration_scopes
                      SET scope_digest=?,audit_record_hash=?
                    WHERE scope_id=? AND revision=? AND membership_sequence=?
                      AND state_reason='harness_replaced'""",
                (
                    next_scope_digest,
                    audit_hash,
                    scope.scope_id,
                    next_revision,
                    next_membership_sequence,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("scope replacement finalization conflict")
            return ScopeHarnessReplacementResult(
                request_id=request.request_id,
                request_digest=request.digest,
                scope_id=scope.scope_id,
                old_harness_id=request.old_harness_id,
                new_harness_id=request.new_harness_id,
                role=request.role,
                membership_sequence=next_membership_sequence,
                scope_revision=next_revision,
                scope_digest=next_scope_digest,
                audit_record_hash=audit_hash,
                idempotent_repeat=False,
            )


__all__ = [
    "SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE",
    "SCOPE_HARNESS_REPLACEMENT_SCHEMA",
    "ScopeHarnessReplacementRequest",
    "ScopeHarnessReplacementResult",
    "ScopeHarnessReplacementService",
]
