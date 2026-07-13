"""Versioned canonical event, receipt, task, and room schemas."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from enum import StrEnum
from typing import Annotated, Any, Literal
from uuid import uuid4

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    field_serializer,
    field_validator,
    model_validator,
)

from agentnet.identity.actors import ActorKind, VerifiedActor


_SHA256_PATTERN = r"^[a-f0-9]{64}$"
_UUID_PATTERN = r"^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$"
_MEDIA_TYPE_PATTERN = r"^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$"


def utc_now() -> datetime:
    return datetime.now(UTC)


class Classification(StrEnum):
    C0_PUBLIC = "C0"
    C1_INTERNAL = "C1"
    C2_RESTRICTED = "C2"
    C3_SEALED = "C3"


class EventType(StrEnum):
    MESSAGE = "message"
    TASK_ASSIGNMENT = "task_assignment"
    ROOM_EVENT = "room_event"
    ARTIFACT_EVENT = "artifact_event"
    CONTROL = "control"


class ReleasedArtifactBinding(BaseModel):
    """Exact, non-fetching reference to one corporately released object version.

    The binding intentionally excludes the plaintext digest and object key:
    both are private manifest data whose disclosure would create equality and
    storage-topology side channels.  Consumers must resolve ``artifact_id``
    through the authoritative artifact service and compare every field before
    making bytes available.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    artifact_id: str = Field(pattern=_UUID_PATTERN)
    domain_id: str = Field(min_length=1, max_length=256)
    object_version: str = Field(pattern=_SHA256_PATTERN)
    size: int = Field(ge=0, le=16_777_216)
    media_type: str = Field(min_length=3, max_length=255, pattern=_MEDIA_TYPE_PATTERN)
    classification: Classification
    release_intent_id: str = Field(pattern=_UUID_PATTERN)
    released_at: datetime

    @field_validator("released_at")
    @classmethod
    def released_at_is_aware(cls, value: datetime) -> datetime:
        if value.tzinfo is None or value.utcoffset() is None:
            raise ValueError("artifact release time must be timezone-aware")
        return value


class DeliveryFact(StrEnum):
    CREATED_LOCAL = "created_local"
    SUBMITTED = "submitted"
    REJECTED_BEFORE_ACCEPT = "rejected_before_accept"
    AUTHORIZATION_HOLD = "authorization_hold"
    ACCEPTED_LOCAL = "accepted_local"
    ACCEPTED_DURABLE = "accepted_durable"
    ACCEPTED_QUEUED = "accepted_queued"
    PENDING_HUMAN = "pending_human"
    QUEUED = "queued"
    RETRY_SCHEDULED = "retry_scheduled"
    DISPATCH_ATTEMPTED = "dispatch_attempted"
    REMOTE_ACCEPTED = "remote_accepted"
    REMOTE_REJECTED = "remote_rejected"
    REMOTE_DELAYED = "remote_delayed"
    RECIPIENT_COMMITTED = "recipient_committed"
    PRESENTED = "presented"
    PROCESSING = "processing"
    EFFECT_PREPARED = "effect_prepared"
    COMPLETED = "completed"
    FAILED_RETRYABLE = "failed_retryable"
    FAILED_TERMINAL = "failed_terminal"
    EXPIRED = "expired"
    CANCEL_REQUESTED = "cancel_requested"
    CANCELED = "canceled"
    TOO_LATE = "too_late"
    EFFECT_UNKNOWN = "effect_unknown"
    RECONCILING = "reconciling"
    COMPENSATED = "compensated"
    ADJUDICATION_REQUIRED = "adjudication_required"
    QUARANTINED = "quarantined"
    DEAD_LETTERED = "dead_lettered"
    CONFLICT_PENDING = "conflict_pending"


class EventEnvelope(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    protocol_profile: Literal["agentnet.internal/1.0"] = "agentnet.internal/1.0"
    event_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str
    actor: VerifiedActor
    event_type: EventType
    classification: Classification
    payload: dict[str, Any]
    payload_digest: str
    # Present only on extension-owned task-custody records.  ``None`` keeps
    # legacy and ordinary message envelope digests byte-compatible.  Missing
    # on a task event is not an allow signal: generic readers also apply the
    # typed event/task fallback and redact fail closed.
    payload_access: Literal["task_grant_required"] | None = None
    idempotency_key: str = Field(min_length=16, max_length=256)
    recipients: tuple[str, ...] = Field(min_length=1)
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()
    conversation_id: str | None = None
    room_id: str | None = None
    room_control_sequence: int | None = Field(default=None, ge=1)
    room_application_epoch: int | None = Field(default=None, ge=1)
    room_file_key_epoch: int | None = Field(default=None, ge=1)
    room_mls_epoch: int | None = Field(default=None, ge=0)
    thread_id: str | None = None
    task_id: str | None = None
    causal_parent_ids: tuple[str, ...] = Field(default=(), max_length=256)
    created_at: datetime = Field(default_factory=utc_now)
    delivery_expires_at: datetime | None = None
    effect_deadline: datetime | None = None
    retention_delete_at: datetime | None = None
    legal_hold: bool = False
    policy_revision: int = Field(default=1, ge=1)
    credential_epoch: int = Field(default=0, ge=0)

    @field_validator("recipients")
    @classmethod
    def unique_recipients(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if len(set(value)) != len(value):
            raise ValueError("recipients must be unique")
        return value

    @field_validator("causal_parent_ids")
    @classmethod
    def canonical_causal_parents(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if any(
            not item
            or len(item) > 256
            or any(ord(character) < 0x21 for character in item)
            for item in value
        ):
            raise ValueError("causal parent identifiers must be bounded visible strings")
        if len(set(value)) != len(value):
            raise ValueError("causal parent identifiers must be unique")
        return tuple(sorted(value))

    @model_validator(mode="after")
    def actor_domain_matches(self) -> "EventEnvelope":
        if self.actor.domain_id != self.domain_id:
            raise ValueError("actor domain does not match event domain")
        if self.event_id in self.causal_parent_ids:
            raise ValueError("event cannot name itself as a causal parent")
        if self.actor.kind is ActorKind.WORKLOAD:
            if self.actor.binding_assurance == "synthetic_lab":
                if self.causal_parent_ids:
                    raise ValueError("synthetic lab root cannot assert a causal parent")
            elif self.causal_parent_ids != (self.actor.parent_event_id,):
                raise ValueError("workload event must bind its exact transport parent event")
        if self.delivery_expires_at and self.delivery_expires_at <= self.created_at:
            raise ValueError("delivery expiry must follow creation")
        room_epochs = (
            self.room_control_sequence,
            self.room_application_epoch,
            self.room_file_key_epoch,
            self.room_mls_epoch,
        )
        if self.room_id is None and any(value is not None for value in room_epochs):
            raise ValueError("room epoch snapshot requires a room identifier")
        if self.room_id is not None and any(value is None for value in room_epochs):
            raise ValueError("room events require the complete authorized epoch snapshot")
        artifact_ids = [binding.artifact_id for binding in self.released_artifacts]
        if len(artifact_ids) != len(set(artifact_ids)):
            raise ValueError("released artifact bindings must be unique by artifact identifier")
        classification_rank = {
            Classification.C0_PUBLIC: 0,
            Classification.C1_INTERNAL: 1,
            Classification.C2_RESTRICTED: 2,
            Classification.C3_SEALED: 3,
        }
        for binding in self.released_artifacts:
            if binding.domain_id != self.domain_id:
                raise ValueError("released artifact binding crossed the event trust domain")
            if classification_rank[binding.classification] > classification_rank[self.classification]:
                raise ValueError("event classification is lower than its released artifact")
        return self


class AcceptingStorageBoundary(BaseModel):
    """Non-actor owner of a transactionally established acceptance fact.

    Acceptance is evidence about the storage boundary that committed the
    envelope bytes.  It is not a statement made by a human harness or a
    workload, so representing it as a :class:`VerifiedActor` would fabricate
    an identity and an assurance mechanism that never authenticated.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    kind: Literal["accepting_storage_boundary"] = "accepting_storage_boundary"
    domain_id: str = Field(min_length=1, max_length=256)
    storage_profile: Literal["local_transactional", "verified_durable"]
    acceptance_fact: Literal[
        DeliveryFact.ACCEPTED_LOCAL,
        DeliveryFact.ACCEPTED_DURABLE,
    ]
    event_digest: str = Field(pattern=_SHA256_PATTERN)


ReceiptOwner = VerifiedActor | AcceptingStorageBoundary


class Receipt(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    receipt_id: str = Field(default_factory=lambda: str(uuid4()))
    event_id: str
    recipient_id: str | None = None
    fact: DeliveryFact
    owner: ReceiptOwner
    event_digest: str
    detail: dict[str, Any] = Field(default_factory=dict)
    created_at: datetime = Field(default_factory=utc_now)
    signature: str | None = None


ScopeValue = Annotated[str, Field(min_length=1, max_length=512)]


class AssignmentScope(BaseModel):
    """Complete automatic-custody scope; never data or effect authority."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    task_types: frozenset[ScopeValue] = Field(min_length=1, max_length=256)
    resources: frozenset[ScopeValue] = Field(min_length=1, max_length=256)
    data_classes: frozenset[Classification] = Field(min_length=1, max_length=4)
    tools: frozenset[ScopeValue] = Field(default_factory=frozenset, max_length=256)
    max_budget: int = Field(ge=0, le=9_223_372_036_854_775_807)
    max_duration_seconds: int = Field(ge=1, le=31_536_000)
    max_concurrency: int = Field(default=1, ge=1, le=65_535)
    authority_effect: Literal["custody_only"] = "custody_only"

    @field_serializer("task_types", "resources", "tools")
    def serialize_string_sets(self, value: frozenset[str]) -> list[str]:
        """Make consent bytes independent of process hash randomization."""

        return sorted(value)

    @field_serializer("data_classes")
    def serialize_data_classes(
        self,
        value: frozenset[Classification],
    ) -> list[str]:
        return sorted(item.value for item in value)

    @staticmethod
    def _contains(allowed: frozenset[str], value: str) -> bool:
        return "*" in allowed or value in allowed

    def allows(
        self,
        *,
        task_type: str,
        resources: frozenset[str],
        data_classes: frozenset[Classification],
        tools: frozenset[str],
        budget: int,
        concurrency: int,
        deadline: datetime | None,
        when: datetime,
        relationship_expires_at: datetime,
    ) -> tuple[bool, str]:
        if not self._contains(self.task_types, task_type):
            return False, "assignment_task_type_out_of_scope"
        if any(not self._contains(self.resources, resource) for resource in resources):
            return False, "assignment_resource_out_of_scope"
        if not data_classes.issubset(self.data_classes):
            return False, "assignment_data_class_out_of_scope"
        if any(not self._contains(self.tools, tool) for tool in tools):
            return False, "assignment_tool_out_of_scope"
        if budget > self.max_budget:
            return False, "assignment_budget_out_of_scope"
        if concurrency > self.max_concurrency:
            return False, "assignment_concurrency_out_of_scope"
        if deadline is None:
            return False, "assignment_deadline_required"
        if deadline <= when:
            return False, "assignment_deadline_expired"
        if deadline > when + timedelta(seconds=self.max_duration_seconds):
            return False, "assignment_deadline_out_of_scope"
        if deadline >= relationship_expires_at:
            return False, "assignment_deadline_exceeds_relationship"
        return True, "assignment_within_custody_scope"


class EmptyAssignmentScope(BaseModel):
    """The only valid scope representation when assignment is disabled."""

    model_config = ConfigDict(extra="forbid", frozen=True)


class Relationship(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    relationship_id: str = Field(
        default_factory=lambda: str(uuid4()), min_length=1, max_length=256
    )
    domain_id: str = Field(min_length=1, max_length=256)
    administrator_harness_id: str = Field(min_length=1, max_length=256)
    subordinate_harness_id: str = Field(min_length=1, max_length=256)
    may_assign: bool = False
    assignment_scope: AssignmentScope | EmptyAssignmentScope = Field(
        default_factory=EmptyAssignmentScope
    )
    revision: int = Field(default=1, ge=1, le=9_223_372_036_854_775_807)
    expires_at: datetime
    revoked_at: datetime | None = None

    @field_validator("expires_at", "revoked_at")
    @classmethod
    def relationship_times_are_aware(cls, value: datetime | None) -> datetime | None:
        if value is not None and (value.tzinfo is None or value.utcoffset() is None):
            raise ValueError("relationship times must be timezone-aware")
        return value

    @model_validator(mode="after")
    def assignment_scope_matches_flag(self) -> "Relationship":
        if self.may_assign and not isinstance(self.assignment_scope, AssignmentScope):
            raise ValueError("assigning relationship requires the complete custody scope")
        if not self.may_assign and not isinstance(self.assignment_scope, EmptyAssignmentScope):
            raise ValueError("non-assigning relationship requires an empty assignment scope")
        return self

    def active_at(self, when: datetime) -> bool:
        return self.revoked_at is None and when < self.expires_at


class TaskGrant(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    grant_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str
    principal_id: str
    harness_id: str
    actions: frozenset[str]
    resources: frozenset[str]
    input_sources: frozenset[str]
    output_sinks: frozenset[str]
    data_classes: frozenset[Classification]
    max_uses: int = Field(default=1, ge=1)
    expires_at: datetime
    revoked_at: datetime | None = None


class PresenceLease(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    harness_id: str
    domain_id: str
    issued_at: datetime = Field(default_factory=utc_now)
    expires_at: datetime
    capability_hints: frozenset[str] = frozenset()

    @property
    def state(self) -> str:
        now = utc_now()
        if now < self.expires_at:
            return "live"
        return "stale"
