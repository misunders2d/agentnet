"""Construction and digest validation for immutable events."""

from __future__ import annotations

from datetime import datetime
from typing import Any
from uuid import uuid4

from agentnet.errors import ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import (
    Classification,
    EventEnvelope,
    EventType,
    ReleasedArtifactBinding,
)
from agentnet.security.signatures import canonical_digest


def new_event(
    *,
    domain_id: str,
    actor: VerifiedActor,
    event_id: str | None = None,
    event_type: EventType,
    classification: Classification,
    payload: dict[str, Any],
    idempotency_key: str,
    recipients: tuple[str, ...],
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = (),
    conversation_id: str | None = None,
    room_id: str | None = None,
    room_control_sequence: int | None = None,
    room_application_epoch: int | None = None,
    room_file_key_epoch: int | None = None,
    room_mls_epoch: int | None = None,
    thread_id: str | None = None,
    task_id: str | None = None,
    causal_parent_ids: tuple[str, ...] = (),
    delivery_expires_at: datetime | None = None,
    effect_deadline: datetime | None = None,
    retention_delete_at: datetime | None = None,
    policy_revision: int = 1,
) -> EventEnvelope:
    return EventEnvelope(
        event_id=event_id or str(uuid4()),
        domain_id=domain_id,
        actor=actor,
        event_type=event_type,
        classification=classification,
        payload=payload,
        payload_digest=canonical_digest(payload),
        idempotency_key=idempotency_key,
        recipients=recipients,
        released_artifacts=released_artifacts,
        conversation_id=conversation_id,
        room_id=room_id,
        room_control_sequence=room_control_sequence,
        room_application_epoch=room_application_epoch,
        room_file_key_epoch=room_file_key_epoch,
        room_mls_epoch=room_mls_epoch,
        thread_id=thread_id,
        task_id=task_id,
        causal_parent_ids=causal_parent_ids,
        delivery_expires_at=delivery_expires_at,
        effect_deadline=effect_deadline,
        retention_delete_at=retention_delete_at,
        policy_revision=policy_revision,
        credential_epoch=actor.credential_epoch,
    )


def validate_event_digest(event: EventEnvelope) -> None:
    if canonical_digest(event.payload) != event.payload_digest:
        raise ValidationError("event payload digest does not match exact payload")


def envelope_digest(event: EventEnvelope) -> str:
    return canonical_digest(event.model_dump(mode="json", exclude_none=True))


def envelope_metadata(event: EventEnvelope) -> dict[str, Any]:
    value = event.model_dump(mode="json", exclude_none=True)
    value.pop("payload", None)
    return value
