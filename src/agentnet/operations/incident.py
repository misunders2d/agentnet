"""Durable domain incident controls with real fail-closed enforcement."""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_signed_authority_command,
)
from agentnet.errors import AuthorizationError, ConflictError, GateBlocked
from agentnet.operations.outage import IncidentMode
from agentnet.security.signatures import canonical_digest
from agentnet.storage.backend import StoreBackend


class DomainIncidentState(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    domain_id: str = Field(min_length=1, max_length=256)
    mode: IncidentMode
    revision: int = Field(ge=0)
    reason: str | None = Field(default=None, min_length=1, max_length=512)
    reason_digest: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")
    updated_at: datetime | None = None


class IncidentModeChange(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    domain_id: str = Field(min_length=1, max_length=256)
    expected_revision: int = Field(ge=0)
    target_mode: IncidentMode
    reason: str = Field(min_length=1, max_length=512)


class DomainIncidentService:
    ACTION = "operator.incident.set"

    def __init__(self, store: StoreBackend) -> None:
        self.store = store

    @staticmethod
    def authority_binding(change: IncidentModeChange) -> tuple[str, dict[str, Any]]:
        return (
            f"operator-domain:{change.domain_id}",
            {
                "schema": "agentnet.domain-incident-change.v1",
                **change.model_dump(mode="json"),
            },
        )

    def state(self, domain_id: str) -> DomainIncidentState:
        row = self.store.fetch_one(
            "SELECT * FROM domain_incident_controls WHERE domain_id=?",
            (domain_id,),
        )
        if row is None:
            domain = self.store.fetch_one(
                "SELECT status FROM domains WHERE domain_id=?",
                (domain_id,),
            )
            if domain is None or domain["status"] != "active":
                raise AuthorizationError("incident control domain is unavailable")
            return DomainIncidentState(
                domain_id=domain_id,
                mode=IncidentMode.NORMAL,
                revision=0,
            )
        try:
            reason = str(row["reason"])
            return DomainIncidentState(
                domain_id=str(row["domain_id"]),
                mode=IncidentMode(str(row["mode"])),
                revision=int(row["revision"]),
                reason=reason,
                reason_digest=canonical_digest({"reason": reason}),
                updated_at=datetime.fromtimestamp(int(row["updated_at"]), UTC),
            )
        except Exception as exc:
            raise GateBlocked(
                "incident_control",
                "durable incident state is malformed",
            ) from exc

    def current_mode(self, domain_id: str) -> IncidentMode:
        return self.state(domain_id).mode

    def set_mode(
        self,
        change: IncidentModeChange,
        *,
        authority: IssuanceAuthority | None,
        command: SignedAuthorityCommand | None,
        when: datetime | None = None,
    ) -> DomainIncidentState:
        when = when or datetime.now(UTC).replace(microsecond=0)
        if when.tzinfo is None:
            raise ValueError("incident mutation time must be timezone-aware")
        now = int(when.timestamp())
        resource, exact_request = self.authority_binding(change)
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT status FROM domains WHERE domain_id=?",
                (change.domain_id,),
            ).fetchone()
            if domain is None or domain["status"] != "active":
                raise AuthorizationError("incident control domain is unavailable")
            row = connection.execute(
                "SELECT * FROM domain_incident_controls WHERE domain_id=?",
                (change.domain_id,),
            ).fetchone()
            actual_revision = 0 if row is None else int(row["revision"])
            if actual_revision != change.expected_revision:
                raise ConflictError("incident control revision changed")
            if command is None or command.expected_entity_revision != change.expected_revision:
                raise ConflictError("incident command revision does not match the requested transition")
            require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action=self.ACTION,
                expected_resource=resource,
                expected_request=exact_request,
                when=when,
            )
            if authority is None or authority.actor.domain_id != change.domain_id:
                raise AuthorizationError("incident authority domain binding mismatch")
            begin_authority_mutation_intent(
                connection,
                command=command,
                authority=authority,
                when=when,
            )
            if row is None:
                connection.execute(
                    """INSERT INTO domain_incident_controls(
                           domain_id,mode,revision,reason,actor_json,policy_decision_id,updated_at
                       ) VALUES(?,?,1,?,?,?,?)""",
                    (
                        change.domain_id,
                        change.target_mode.value,
                        change.reason,
                        command.actor.model_dump_json(),
                        authority.policy_decision_id,
                        now,
                    ),
                )
                revision = 1
            else:
                updated = connection.execute(
                    """UPDATE domain_incident_controls
                          SET mode=?,revision=revision+1,reason=?,actor_json=?,
                              policy_decision_id=?,updated_at=?
                        WHERE domain_id=? AND revision=?""",
                    (
                        change.target_mode.value,
                        change.reason,
                        command.actor.model_dump_json(),
                        authority.policy_decision_id,
                        now,
                        change.domain_id,
                        change.expected_revision,
                    ),
                )
                if updated.rowcount != 1:
                    raise ConflictError("incident control transition raced")
                revision = change.expected_revision + 1
            self.store.append_audit(
                connection,
                {
                    "action": "operator.incident.changed",
                    "domain_id": change.domain_id,
                    "from_mode": IncidentMode.NORMAL.value if row is None else row["mode"],
                    "to_mode": change.target_mode.value,
                    "previous_revision": change.expected_revision,
                    "revision": revision,
                    "reason": change.reason,
                    "actor": authority.actor.audit_view(),
                    "command_id": command.command_id,
                    "policy_decision_id": authority.policy_decision_id,
                },
            )
            complete_authority_mutation_intent(
                connection,
                command_id=command.command_id,
                when=when,
            )
        return self.state(change.domain_id)


__all__ = [
    "DomainIncidentService",
    "DomainIncidentState",
    "IncidentMode",
    "IncidentModeChange",
]
