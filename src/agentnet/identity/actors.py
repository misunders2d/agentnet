"""Explicit actor union; payload claims never construct verified actors."""

from __future__ import annotations

from enum import StrEnum
from typing import Any, Literal, Self

from pydantic import BaseModel, ConfigDict, Field, model_serializer, model_validator


class ActorKind(StrEnum):
    VERIFIED_HUMAN_HARNESS = "verified_human_harness"
    HOST_GUEST_HARNESS = "host_guest_harness"
    WORKLOAD = "workload"
    EXTERNAL_A2A = "external_human_unverified"


class VerifiedActor(BaseModel):
    """An exact, discriminated actor shape.

    Pydantic cannot expose a union alias while preserving the long-standing
    ``VerifiedActor(...)`` constructor used throughout the core.  This model
    therefore enforces the same tagged-union invariant at both parse and copy
    boundaries: a field belonging to another actor kind is an error even when
    its value is ``null``.  Authority code can consequently branch on ``kind``
    without also having to defend against smuggled cross-kind identity fields.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    kind: ActorKind
    domain_id: str
    principal_id: str | None = None
    guest_id: str | None = None
    harness_id: str | None = None
    workload_id: str | None = None
    workload_registration_id: str | None = None
    workload_role: str | None = None
    workload_process_id: int | None = None
    workload_process_start_time: int | None = None
    workload_session_id: str | None = None
    workload_revocation_epoch: int | None = None
    external_peer_id: str | None = None
    parent_event_id: str | None = None
    task_grant_id: str | None = None
    credential_id: str | None = None
    credential_epoch: int = Field(default=0, ge=0)
    binding_assurance: Literal[
        "external",
        "lab",
        "os_bound",
        "hardware_bound",
        "internal_process",
        "synthetic_lab",
        "workload_mtls",
    ]

    @model_validator(mode="before")
    @classmethod
    def reject_cross_kind_fields(cls, value: Any) -> Any:
        if not isinstance(value, dict):
            return value
        try:
            kind = ActorKind(value.get("kind"))
        except (TypeError, ValueError):
            return value
        common = {"kind", "domain_id", "binding_assurance"}
        fields_by_kind = {
            ActorKind.VERIFIED_HUMAN_HARNESS: {
                "principal_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
            },
            ActorKind.HOST_GUEST_HARNESS: {
                "guest_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
            },
            ActorKind.WORKLOAD: {
                "workload_id",
                "workload_registration_id",
                "workload_role",
                "workload_process_id",
                "workload_process_start_time",
                "workload_session_id",
                "workload_revocation_epoch",
                "parent_event_id",
                "task_grant_id",
                "credential_id",
                "credential_epoch",
            },
            ActorKind.EXTERNAL_A2A: {"external_peer_id"},
        }
        unexpected = sorted(set(value) - common - fields_by_kind[kind])
        if unexpected:
            raise ValueError(f"{kind.value} actor contains cross-kind fields: {', '.join(unexpected)}")
        return value

    @model_validator(mode="after")
    def validate_union(self) -> "VerifiedActor":
        if self.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            if not self.principal_id or not self.harness_id or not self.credential_id:
                raise ValueError("verified human actors require principal, harness, and credential")
            if self.credential_epoch < 1:
                raise ValueError("verified human actors require a positive credential epoch")
            if self.binding_assurance not in {"lab", "os_bound", "hardware_bound"}:
                raise ValueError("verified human actor binding assurance is invalid")
        elif self.kind is ActorKind.HOST_GUEST_HARNESS:
            if not self.guest_id or not self.harness_id or not self.credential_id:
                raise ValueError("guest actors require guest, harness, and credential")
            if self.credential_epoch < 1:
                raise ValueError("guest actors require a positive credential epoch")
            if self.binding_assurance not in {"lab", "os_bound", "hardware_bound"}:
                raise ValueError("guest actor binding assurance is invalid")
        elif self.kind is ActorKind.WORKLOAD:
            if not self.workload_id:
                raise ValueError("workload actors require workload_id")
            if (self.parent_event_id is None) != (self.task_grant_id is None):
                raise ValueError("workload data/effect authority requires both parent event and grant")
            if self.binding_assurance not in {"internal_process", "synthetic_lab", "workload_mtls"}:
                raise ValueError("workload actor binding assurance is invalid")
            is_synthetic = self.workload_id.startswith("synthetic-lab-")
            if is_synthetic and self.binding_assurance != "synthetic_lab":
                raise ValueError("synthetic lab workloads cannot claim production transport assurance")
            if self.binding_assurance == "synthetic_lab" and not is_synthetic:
                raise ValueError("synthetic_lab assurance is restricted to explicit synthetic workloads")
            if self.binding_assurance == "workload_mtls":
                if (
                    not self.workload_registration_id
                    or not self.workload_role
                    or not self.workload_session_id
                    or not self.credential_id
                    or self.credential_epoch < 1
                    or not isinstance(self.workload_process_id, int)
                    or self.workload_process_id < 1
                    or not isinstance(self.workload_process_start_time, int)
                    or self.workload_process_start_time < 1
                    or not isinstance(self.workload_revocation_epoch, int)
                    or self.workload_revocation_epoch < 1
                ):
                    raise ValueError("mTLS workload actor lacks its registered process credential binding")
            elif any(
                value is not None
                for value in (
                    self.workload_registration_id,
                    self.workload_role,
                    self.workload_process_id,
                    self.workload_process_start_time,
                    self.workload_session_id,
                    self.workload_revocation_epoch,
                    self.credential_id,
                )
            ) or self.credential_epoch != 0:
                raise ValueError("unverified workload actor cannot carry registered credential fields")
        elif self.kind is ActorKind.EXTERNAL_A2A:
            if not self.external_peer_id:
                raise ValueError("external A2A actors require a peer identifier")
            if self.binding_assurance != "external":
                raise ValueError("external A2A actors require external assurance")
        return self

    def model_copy(self, *, update: dict[str, Any] | None = None, deep: bool = False) -> Self:
        """Revalidate updates instead of allowing Pydantic's unchecked copy."""

        value = self.audit_view()
        if update:
            value.update(update)
        return type(self).model_validate(value)

    @model_serializer(mode="wrap")
    def serialize_exact_variant(self, handler: Any) -> dict[str, Any]:
        value = handler(self)
        fields_by_kind = {
            ActorKind.VERIFIED_HUMAN_HARNESS: {
                "kind",
                "domain_id",
                "principal_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
                "binding_assurance",
            },
            ActorKind.HOST_GUEST_HARNESS: {
                "kind",
                "domain_id",
                "guest_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
                "binding_assurance",
            },
            ActorKind.WORKLOAD: {
                "kind",
                "domain_id",
                "workload_id",
                "workload_registration_id",
                "workload_role",
                "workload_process_id",
                "workload_process_start_time",
                "workload_session_id",
                "workload_revocation_epoch",
                "parent_event_id",
                "task_grant_id",
                "credential_id",
                "credential_epoch",
                "binding_assurance",
            },
            ActorKind.EXTERNAL_A2A: {
                "kind",
                "domain_id",
                "external_peer_id",
                "binding_assurance",
            },
        }
        result = {
            key: item
            for key, item in value.items()
            if key in fields_by_kind[self.kind] and item is not None
        }
        if self.kind is ActorKind.WORKLOAD and self.binding_assurance != "workload_mtls":
            result.pop("credential_epoch", None)
        return result

    @property
    def positive_authority_id(self) -> str | None:
        if self.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            return self.principal_id
        if self.kind is ActorKind.HOST_GUEST_HARNESS:
            return self.guest_id
        return None

    def audit_view(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude_none=True)


class TrustedTransportContext(BaseModel):
    """Constructed only by a verified transport adapter."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    actor: VerifiedActor
    audience: str
    method: str
    scheme: Literal["http", "https"]
    authority: str
    path: str
    query: str
    body_digest: str
    timestamp: int
    nonce: str
    proof_id: str
