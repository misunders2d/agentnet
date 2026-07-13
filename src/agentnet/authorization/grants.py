"""Exact, use-counted task grants.

Task grants attenuate an existing human entitlement (or provide the host-local
positive grant for a guest).  They never create authority for a workload,
external peer, harness, device, or session.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import UTC, datetime

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_current_authority_decision,
    require_signed_authority_command,
)
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.config import RuntimeProfile
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.sqlite import SQLiteStore


def epoch_seconds(value: datetime) -> int:
    if value.tzinfo is None:
        raise ValidationError("security timestamps must be timezone-aware")
    return int(value.timestamp())


class GrantUse(BaseModel):
    """One exact operation to intersect with a task grant."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    grant_id: str = Field(min_length=1)
    action: str = Field(min_length=1)
    resource: str = Field(min_length=1)
    input_source: str = Field(min_length=1)
    output_sink: str = Field(min_length=1)
    data_class: Classification


class GrantConsumption(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    allowed: bool
    reason: str
    consumed: bool = False
    grant: TaskGrant | None = None


class TaskGrantService:
    def __init__(
        self,
        store: SQLiteStore,
        *,
        runtime_profile: RuntimeProfile = RuntimeProfile.LOCAL_CONFORMANCE,
    ) -> None:
        self.store = store
        self.runtime_profile = RuntimeProfile(runtime_profile)

    @staticmethod
    def _validate_nonempty(grant: TaskGrant) -> None:
        dimensions = {
            "actions": grant.actions,
            "resources": grant.resources,
            "input_sources": grant.input_sources,
            "output_sinks": grant.output_sinks,
            "data_classes": grant.data_classes,
        }
        empty = [name for name, values in dimensions.items() if not values]
        if empty:
            raise ValidationError(f"task grant dimensions must be non-empty: {','.join(empty)}")

    @staticmethod
    def issuance_binding(grant: TaskGrant) -> tuple[str, dict[str, str]]:
        """Return the exact PolicyEngine resource/context required to issue."""

        return (
            f"task-grant:{grant.grant_id}",
            {"request_digest": canonical_digest(grant.model_dump(mode="json"))},
        )

    @staticmethod
    def read_binding(grant_id: str) -> tuple[str, dict[str, object]]:
        resource = f"task-grant:{grant_id}"
        return resource, {"schema": "agentnet.task-grant.read.v1", "grant_id": grant_id}

    @staticmethod
    def revocation_binding(
        grant_id: str,
        *,
        expected_entity_revision: int,
        reason: str,
    ) -> tuple[str, dict[str, object]]:
        resource = f"task-grant:{grant_id}"
        return resource, {
            "schema": "agentnet.task-grant.revoke.v1",
            "grant_id": grant_id,
            "expected_entity_revision": expected_entity_revision,
            "reason": reason,
        }

    def issue(
        self,
        grant: TaskGrant,
        *,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> TaskGrant:
        """Issue a grant only from the beneficiary's exact current allow.

        Temporary cross-human elevation uses ElevationService, which verifies
        and consumes independent approval receipts transactionally.  This
        ordinary interface deliberately denies absent authority evidence.
        """

        self._validate_nonempty(grant)
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        if epoch_seconds(grant.expires_at) <= now:
            raise ValidationError("task grant must expire in the future")

        with self.store.transaction() as connection:
            resource, request = self.issuance_binding(grant)
            revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="authorization.task_grant.issue",
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            if authority is None:  # narrowed by the verifier; retained for static analyzers
                raise AuthorizationError("exact issuance authority evidence is required")
            if (
                authority.actor.domain_id != grant.domain_id
                or authority.actor.positive_authority_id != grant.principal_id
                or authority.actor.harness_id != grant.harness_id
            ):
                raise AuthorizationError("task grant issuer must be its exact beneficiary actor")
            return self._insert_in_transaction(
                connection,
                grant=grant,
                when=when,
                issuance_evidence={
                    "kind": "local_human_authority",
                    "actor": authority.actor.audit_view(),
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": revision,
                },
            )

    def _insert_in_transaction(
        self,
        connection: sqlite3.Connection,
        *,
        grant: TaskGrant,
        when: datetime,
        issuance_evidence: dict[str, object],
    ) -> TaskGrant:
        """Insert after the caller verified authority in this transaction."""

        self._validate_nonempty(grant)
        now = epoch_seconds(when)
        if epoch_seconds(grant.expires_at) <= now:
            raise ValidationError("task grant must expire in the future")
        serialized = canonical_json(grant.model_dump(mode="json")).decode("utf-8")
        existing = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (grant.grant_id,)).fetchone()
        if existing is not None:
            if existing["grant_json"] != serialized:
                raise ConflictError("task grant identifier already names different bytes")
            return self._from_row(existing)

        domain = connection.execute("SELECT * FROM domains WHERE domain_id=?", (grant.domain_id,)).fetchone()
        harness = connection.execute("SELECT * FROM harnesses WHERE harness_id=?", (grant.harness_id,)).fetchone()
        if domain is None or domain["status"] != "active":
            raise ValidationError("task grant domain is not active")
        if harness is None or harness["domain_id"] != grant.domain_id or harness["status"] != "active":
            raise ValidationError("task grant harness is not active in the domain")
        if grant.principal_id not in {harness["principal_id"], harness["guest_id"]}:
            raise ValidationError("task grant beneficiary does not own the harness")

        connection.execute(
            """
            INSERT INTO task_grants(
                grant_id, domain_id, principal_id, harness_id, grant_json,
                max_uses, uses, expires_at, revoked_at
            ) VALUES(?,?,?,?,?,?,0,?,?)
            """,
            (
                grant.grant_id,
                grant.domain_id,
                grant.principal_id,
                grant.harness_id,
                serialized,
                grant.max_uses,
                epoch_seconds(grant.expires_at),
                epoch_seconds(grant.revoked_at) if grant.revoked_at else None,
            ),
        )
        authority_binding = {
            "schema": "agentnet.task-grant.authority-binding.v1",
            "grant_id": grant.grant_id,
            "domain_id": grant.domain_id,
            "principal_id": grant.principal_id,
            "harness_id": grant.harness_id,
            "policy_revision": int(domain["policy_revision"]),
            "harness_credential_epoch": int(harness["credential_epoch"]),
            "issued_at": now,
        }
        connection.execute(
            "INSERT INTO metadata(key,value) VALUES(?,?)",
            (
                f"authority-binding:task-grant:{grant.grant_id}",
                canonical_json(authority_binding).decode("utf-8"),
            ),
        )
        self.store.append_audit(
            connection,
            {
                "type": "task_grant_issued",
                "grant": grant.model_dump(mode="json"),
                "issuance_evidence": issuance_evidence,
            },
        )
        return grant

    @staticmethod
    def _from_row(row: sqlite3.Row) -> TaskGrant:
        grant = TaskGrant.model_validate(json.loads(row["grant_json"]))
        return grant.model_copy(
            update={
                "revoked_at": (
                    datetime.fromtimestamp(int(row["revoked_at"]), UTC)
                    if row["revoked_at"] is not None
                    else None
                )
            }
        )

    @staticmethod
    def _entity_revision(row: sqlite3.Row) -> int:
        # Issuance is revision 1.  Every committed use advances the lifecycle
        # revision, so a revoke prepared before a concurrent consume is stale.
        return int(row["uses"]) + 1 + int(row["revoked_at"] is not None)

    def get(
        self,
        grant_id: str,
        *,
        authority: IssuanceAuthority | None = None,
        administrative: bool = False,
        when: datetime | None = None,
    ) -> TaskGrant | None:
        """Return a grant only after an exact current non-enumerating read allow."""

        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, request = self.read_binding(grant_id)
            action = (
                "authorization.task_grant.admin_read"
                if administrative
                else "authorization.task_grant.read"
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=action,
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            if authority is None:  # narrowed by verifier
                raise AuthorizationError("authenticated task grant reader is required")
            row = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (grant_id,)).fetchone()
            if row is None:
                return None
            if row["domain_id"] != authority.actor.domain_id:
                return None
            if not administrative and (
                authority.actor.positive_authority_id != row["principal_id"]
                or authority.actor.harness_id != row["harness_id"]
            ):
                return None
            return self._from_row(row)

    def get_for_local_conformance(self, grant_id: str) -> TaskGrant | None:
        """Explicit lab-only fixture inspection; never a remotely mounted API."""

        if self.runtime_profile is not RuntimeProfile.LOCAL_CONFORMANCE:
            raise AuthorizationError("local task grant inspection is disabled outside local conformance")
        row = self.store.fetch_one("SELECT * FROM task_grants WHERE grant_id=?", (grant_id,))
        if row is None:
            return None
        return self._from_row(row)

    def uses(
        self,
        grant_id: str,
        *,
        authority: IssuanceAuthority | None = None,
        administrative: bool = False,
        when: datetime | None = None,
    ) -> int | None:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            resource, request = self.read_binding(grant_id)
            action = (
                "authorization.task_grant.admin_read"
                if administrative
                else "authorization.task_grant.read"
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
                raise AuthorizationError("authenticated task grant reader is required")
            row = connection.execute(
                "SELECT domain_id,principal_id,harness_id,uses FROM task_grants WHERE grant_id=?",
                (grant_id,),
            ).fetchone()
            if row is None:
                return None
            if row["domain_id"] != authority.actor.domain_id:
                return None
            if not administrative and (
                authority.actor.positive_authority_id != row["principal_id"]
                or authority.actor.harness_id != row["harness_id"]
            ):
                return None
            return int(row["uses"])

    def uses_for_local_conformance(self, grant_id: str) -> int | None:
        if self.runtime_profile is not RuntimeProfile.LOCAL_CONFORMANCE:
            raise AuthorizationError("local task grant inspection is disabled outside local conformance")
        row = self.store.fetch_one("SELECT uses FROM task_grants WHERE grant_id=?", (grant_id,))
        return None if row is None else int(row["uses"])

    def revoke(
        self,
        grant_id: str,
        *,
        command: SignedAuthorityCommand | None = None,
        authority: IssuanceAuthority | None = None,
        when: datetime | None = None,
    ) -> bool:
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            action = command.action if command is not None else "authorization.task_grant.revoke"
            if action not in {
                "authorization.task_grant.revoke",
                "authorization.task_grant.admin_revoke",
            }:
                raise AuthorizationError("task grant revocation action is invalid")
            resource, expected_request = self.revocation_binding(
                grant_id,
                expected_entity_revision=command.expected_entity_revision if command is not None else 0,
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
                raise AuthorizationError("signed task grant revocation authority is required")
            row = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (grant_id,)).fetchone()
            if row is None or row["domain_id"] != authority.actor.domain_id:
                raise ConflictError("task grant revision changed before revocation")
            owner = (
                authority.actor.positive_authority_id == row["principal_id"]
                and authority.actor.harness_id == row["harness_id"]
            )
            if not owner and action != "authorization.task_grant.admin_revoke":
                raise ConflictError("task grant revision changed before revocation")
            if row["revoked_at"] is not None or self._entity_revision(row) != command.expected_entity_revision:
                raise ConflictError("task grant revision changed before revocation")
            begin_authority_mutation_intent(connection, command=command, authority=authority, when=when)
            cursor = connection.execute(
                """
                UPDATE task_grants SET revoked_at=?
                 WHERE grant_id=? AND uses=? AND revoked_at IS NULL
                """,
                (epoch_seconds(when), grant_id, command.expected_entity_revision - 1),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task grant revocation raced with grant consumption")
            self.store.append_audit(
                connection,
                {
                    "type": "task_grant_revoked",
                    "grant_id": grant_id,
                    "revoked_at": when.isoformat(),
                    "revocation_actor": authority.actor.audit_view(),
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": policy_revision,
                    "command_id": command.command_id,
                    "reason": command.reason,
                    "actor_role": (
                        "beneficiary"
                        if owner
                        else "authorized_administrator"
                    ),
                },
            )
            complete_authority_mutation_intent(connection, command_id=command.command_id, when=when)
            return True

    def _cascade_revoke_for_harness_in_transaction(
        self,
        connection: sqlite3.Connection,
        *,
        harness_id: str,
        when: datetime,
        reason: str,
    ) -> int:
        """Identity offboarding hook; caller owns the authenticated transaction."""

        cursor = connection.execute(
            "UPDATE task_grants SET revoked_at=? WHERE harness_id=? AND revoked_at IS NULL",
            (epoch_seconds(when), harness_id),
        )
        if cursor.rowcount:
            self.store.append_audit(
                connection,
                {
                    "type": "task_grants_offboarding_revoked",
                    "harness_id": harness_id,
                    "count": cursor.rowcount,
                    "reason": reason,
                    "revoked_at": when.isoformat(),
                },
            )
        return int(cursor.rowcount)

    def _consume_exact(
        self,
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        use: GrantUse,
        when: datetime,
    ) -> GrantConsumption:
        """Check and consume inside the caller's decision transaction."""

        row = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (use.grant_id,)).fetchone()
        if row is None:
            return GrantConsumption(allowed=False, reason="missing_task_grant")
        try:
            grant = TaskGrant.model_validate(json.loads(row["grant_json"]))
        except Exception:
            return GrantConsumption(allowed=False, reason="invalid_task_grant_state")

        binding_row = connection.execute(
            "SELECT value FROM metadata WHERE key=?",
            (f"authority-binding:task-grant:{use.grant_id}",),
        ).fetchone()
        domain = connection.execute(
            "SELECT policy_revision,status FROM domains WHERE domain_id=?",
            (grant.domain_id,),
        ).fetchone()
        try:
            binding = json.loads(binding_row["value"]) if binding_row is not None else None
        except (TypeError, ValueError):
            binding = None
        if not isinstance(binding, dict) or binding.get("schema") != "agentnet.task-grant.authority-binding.v1":
            return GrantConsumption(allowed=False, reason="missing_task_grant_authority_binding", grant=grant)
        try:
            binding_matches = (
                domain is not None
                and domain["status"] == "active"
                and binding.get("domain_id") == grant.domain_id
                and binding.get("principal_id") == grant.principal_id
                and binding.get("harness_id") == grant.harness_id
                and int(binding.get("policy_revision", 0)) == int(domain["policy_revision"])
            )
        except (TypeError, ValueError):
            binding_matches = False
        if not binding_matches:
            return GrantConsumption(allowed=False, reason="stale_task_grant_policy_binding", grant=grant)
        try:
            credential_epoch_matches = int(binding.get("harness_credential_epoch", 0)) == actor.credential_epoch
        except (TypeError, ValueError):
            credential_epoch_matches = False
        if not credential_epoch_matches:
            return GrantConsumption(allowed=False, reason="stale_task_grant_credential_epoch", grant=grant)

        if (
            row["domain_id"] != grant.domain_id
            or row["principal_id"] != grant.principal_id
            or row["harness_id"] != grant.harness_id
            or row["max_uses"] != grant.max_uses
        ):
            return GrantConsumption(allowed=False, reason="inconsistent_task_grant_state")

        authority_id = actor.positive_authority_id
        if authority_id is None:
            return GrantConsumption(allowed=False, reason="actor_has_no_positive_authority", grant=grant)
        if actor.domain_id != grant.domain_id or authority_id != grant.principal_id or actor.harness_id != grant.harness_id:
            return GrantConsumption(allowed=False, reason="task_grant_actor_mismatch", grant=grant)

        now = epoch_seconds(when)
        if row["revoked_at"] is not None or grant.revoked_at is not None:
            return GrantConsumption(allowed=False, reason="task_grant_revoked", grant=grant)
        if row["expires_at"] <= now or epoch_seconds(grant.expires_at) <= now:
            return GrantConsumption(allowed=False, reason="task_grant_expired", grant=grant)
        if row["uses"] >= row["max_uses"]:
            return GrantConsumption(allowed=False, reason="task_grant_exhausted", grant=grant)

        checks = (
            (use.action in grant.actions, "task_grant_action_mismatch"),
            (use.resource in grant.resources, "task_grant_resource_mismatch"),
            (use.input_source in grant.input_sources, "task_grant_source_mismatch"),
            (use.output_sink in grant.output_sinks, "task_grant_sink_mismatch"),
            (use.data_class in grant.data_classes, "task_grant_class_mismatch"),
        )
        for matches, reason in checks:
            if not matches:
                return GrantConsumption(allowed=False, reason=reason, grant=grant)

        cursor = connection.execute(
            """
            UPDATE task_grants
               SET uses=uses+1
             WHERE grant_id=? AND uses < max_uses AND revoked_at IS NULL AND expires_at > ?
            """,
            (grant.grant_id, now),
        )
        if cursor.rowcount != 1:
            return GrantConsumption(allowed=False, reason="task_grant_raced_or_exhausted", grant=grant)
        return GrantConsumption(allowed=True, reason="task_grant_consumed", consumed=True, grant=grant)
