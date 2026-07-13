"""Strict A2A v1 boundary mappings.

The official SDK owns protobuf parsing and wire objects.  This module translates
those objects into tainted, authority-ineligible facts.  Foreign identifiers are
stable and origin-namespaced but are never usable as corporate principal, room,
grant, idempotency, or authorization identifiers.
"""

from __future__ import annotations

import base64
import hashlib

from collections.abc import Callable, Iterable
from enum import StrEnum
from typing import Any, Literal

from a2a.types import (
    Artifact,
    Message,
    Part,
    Role,
    SendMessageResponse,
    StreamResponse,
    Task,
    TaskArtifactUpdateEvent,
    TaskState,
    TaskStatusUpdateEvent,
)
from google.protobuf.json_format import MessageToDict
from google.protobuf.message import Message as ProtoMessage
from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import ValidationError


MAX_EXTERNAL_ID_BYTES = 1_024
EXTERNAL_ACTOR_KIND = "external_human_unverified"


class A2AMappedKind(StrEnum):
    TASK = "task"
    DIRECT_MESSAGE = "direct_message"
    TASK_STATUS_UPDATE = "task_status_update"
    TASK_ARTIFACT_UPDATE = "task_artifact_update"
    QUARANTINED = "quarantined"


class A2ARecoveryMode(StrEnum):
    GET_TASK = "get_task"
    NOT_GET_TASK_RECOVERABLE = "not_get_task_recoverable"
    NONE = "none"


class MappedA2AFact(BaseModel):
    """One inert projection of an SDK response variant."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    kind: A2AMappedKind
    source_variant: str
    peer_namespace: str
    actor_kind: Literal["external_human_unverified"] = EXTERNAL_ACTOR_KIND
    authority_eligible: Literal[False] = False
    credential_disclosure_allowed: Literal[False] = False
    recovery: A2ARecoveryMode = A2ARecoveryMode.NONE
    task_id: str | None = None
    context_id: str | None = None
    message_id: str | None = None
    artifact_id: str | None = None
    reference_task_ids: tuple[str, ...] = ()
    role: Literal["user", "agent"] | None = None
    task_state: str | None = None
    requires_human_input: bool = False
    terminal_remote_state: bool = False
    quarantined_reason: str | None = None
    wire_payload: dict[str, Any] = Field(default_factory=dict)


URLValidator = Callable[[str], object]


_TASK_STATE_NAMES: dict[int, str] = {
    TaskState.TASK_STATE_SUBMITTED: "submitted",
    TaskState.TASK_STATE_WORKING: "working",
    TaskState.TASK_STATE_COMPLETED: "completed",
    TaskState.TASK_STATE_FAILED: "failed",
    TaskState.TASK_STATE_CANCELED: "canceled",
    TaskState.TASK_STATE_INPUT_REQUIRED: "input_required",
    TaskState.TASK_STATE_REJECTED: "rejected",
    TaskState.TASK_STATE_AUTH_REQUIRED: "auth_required",
}

_TERMINAL_TASK_STATES = frozenset({"completed", "failed", "canceled", "rejected"})
_HUMAN_TASK_STATES = frozenset({"input_required", "auth_required"})


def _b64url(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def _bounded_external_text(value: str, *, label: str) -> bytes:
    if not isinstance(value, str) or not value:
        raise ValidationError(f"A2A {label} must be non-empty")
    encoded = value.encode("utf-8")
    if len(encoded) > MAX_EXTERNAL_ID_BYTES:
        raise ValidationError(f"A2A {label} exceeds the boundary limit")
    return encoded


def external_peer_namespace(peer_id: str) -> str:
    """Return a stable opaque namespace for one authenticated external peer."""

    encoded = _bounded_external_text(peer_id, label="peer identifier")
    return f"a2a-peer:{_b64url(hashlib.sha256(encoded).digest()[:18])}"


def namespace_external_id(peer_id: str, kind: str, external_id: str) -> str:
    """Namespace a foreign ID without making it a corporate authority key."""

    if kind not in {"task", "context", "message", "artifact", "reference-task"}:
        raise ValidationError("unsupported A2A external identifier kind")
    peer_namespace = external_peer_namespace(peer_id)
    value = _bounded_external_text(external_id, label=f"{kind} identifier")
    digest = _b64url(hashlib.sha256(value).digest())
    return f"{peer_namespace}:{kind}:{digest}"


def _proto_dict(value: ProtoMessage) -> dict[str, Any]:
    return MessageToDict(
        value,
        preserving_proto_field_name=True,
        use_integers_for_enums=False,
    )


def _namespace_optional(peer_id: str, kind: str, value: str) -> str | None:
    if not value:
        return None
    return namespace_external_id(peer_id, kind, value)


def _quarantined(
    *,
    peer_id: str,
    source_variant: str,
    reason: str,
    value: ProtoMessage,
) -> MappedA2AFact:
    return MappedA2AFact(
        kind=A2AMappedKind.QUARANTINED,
        source_variant=source_variant,
        peer_namespace=external_peer_namespace(peer_id),
        quarantined_reason=reason,
        wire_payload=_proto_dict(value),
    )


def _role_name(message: Message) -> Literal["user", "agent"] | None:
    if message.role == Role.ROLE_USER:
        return "user"
    if message.role == Role.ROLE_AGENT:
        return "agent"
    return None


def _iter_part_urls(parts: Iterable[Part]) -> Iterable[str]:
    for part in parts:
        if part.WhichOneof("content") == "url":
            yield part.url


def _message_url_error(message: Message, validator: URLValidator | None) -> str | None:
    for url in _iter_part_urls(message.parts):
        if validator is None:
            return "A2A URL part lacks SSRF validation"
        try:
            validator(url)
        except Exception:
            return "A2A URL part failed SSRF validation"
    return None


def _artifact_url_error(artifact: Artifact, validator: URLValidator | None) -> str | None:
    for url in _iter_part_urls(artifact.parts):
        if validator is None:
            return "A2A artifact URL lacks SSRF validation"
        try:
            validator(url)
        except Exception:
            return "A2A artifact URL failed SSRF validation"
    return None


def validate_message_part_urls(message: Message, validator: URLValidator) -> None:
    """Fail closed before an inbound URL-bearing message reaches a handler."""

    error = _message_url_error(message, validator)
    if error:
        raise ValidationError(error)


def _message_validation_error(message: Message, validator: URLValidator | None) -> str | None:
    if not message.message_id:
        return "A2A Message is missing message_id"
    if _role_name(message) is None:
        return "A2A Message role is unspecified or unsupported"
    return _message_url_error(message, validator)


def _task_validation_error(task: Task, validator: URLValidator | None) -> str | None:
    if not task.id:
        return "A2A Task is missing its server-owned id"
    if task.status.state == TaskState.TASK_STATE_UNSPECIFIED:
        return "A2A Task state is unspecified"
    if task.status.HasField("message"):
        error = _message_validation_error(task.status.message, validator)
        if error:
            return error
    for message in task.history:
        error = _message_validation_error(message, validator)
        if error:
            return error
    for artifact in task.artifacts:
        if not artifact.artifact_id:
            return "A2A Artifact is missing artifact_id"
        error = _artifact_url_error(artifact, validator)
        if error:
            return error
    return None


def map_message(
    message: Message,
    *,
    peer_id: str,
    source_variant: str,
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    error = _message_validation_error(message, url_validator)
    if error:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason=error,
            value=message,
        )
    return MappedA2AFact(
        kind=A2AMappedKind.DIRECT_MESSAGE,
        source_variant=source_variant,
        peer_namespace=external_peer_namespace(peer_id),
        recovery=A2ARecoveryMode.NOT_GET_TASK_RECOVERABLE,
        task_id=_namespace_optional(peer_id, "task", message.task_id),
        context_id=_namespace_optional(peer_id, "context", message.context_id),
        message_id=namespace_external_id(peer_id, "message", message.message_id),
        reference_task_ids=tuple(
            namespace_external_id(peer_id, "reference-task", reference)
            for reference in message.reference_task_ids
        ),
        role=_role_name(message),
        wire_payload=_proto_dict(message),
    )


def map_task(
    task: Task,
    *,
    peer_id: str,
    source_variant: str,
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    error = _task_validation_error(task, url_validator)
    if error:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason=error,
            value=task,
        )
    state = _TASK_STATE_NAMES[task.status.state]
    return MappedA2AFact(
        kind=A2AMappedKind.TASK,
        source_variant=source_variant,
        peer_namespace=external_peer_namespace(peer_id),
        recovery=A2ARecoveryMode.GET_TASK,
        task_id=namespace_external_id(peer_id, "task", task.id),
        context_id=_namespace_optional(peer_id, "context", task.context_id),
        task_state=state,
        requires_human_input=state in _HUMAN_TASK_STATES,
        terminal_remote_state=state in _TERMINAL_TASK_STATES,
        wire_payload=_proto_dict(task),
    )


def map_status_update(
    update: TaskStatusUpdateEvent,
    *,
    peer_id: str,
    source_variant: str = "stream.status_update",
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    if not update.task_id:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason="A2A status update is missing task_id",
            value=update,
        )
    if update.status.state == TaskState.TASK_STATE_UNSPECIFIED:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason="A2A status update state is unspecified",
            value=update,
        )
    if update.status.HasField("message"):
        error = _message_validation_error(update.status.message, url_validator)
        if error:
            return _quarantined(
                peer_id=peer_id,
                source_variant=source_variant,
                reason=error,
                value=update,
            )
    state = _TASK_STATE_NAMES[update.status.state]
    return MappedA2AFact(
        kind=A2AMappedKind.TASK_STATUS_UPDATE,
        source_variant=source_variant,
        peer_namespace=external_peer_namespace(peer_id),
        recovery=A2ARecoveryMode.GET_TASK,
        task_id=namespace_external_id(peer_id, "task", update.task_id),
        context_id=_namespace_optional(peer_id, "context", update.context_id),
        task_state=state,
        requires_human_input=state in _HUMAN_TASK_STATES,
        terminal_remote_state=state in _TERMINAL_TASK_STATES,
        wire_payload=_proto_dict(update),
    )


def map_artifact_update(
    update: TaskArtifactUpdateEvent,
    *,
    peer_id: str,
    source_variant: str = "stream.artifact_update",
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    if not update.task_id:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason="A2A artifact update is missing task_id",
            value=update,
        )
    if not update.artifact.artifact_id:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason="A2A artifact update is missing artifact_id",
            value=update,
        )
    error = _artifact_url_error(update.artifact, url_validator)
    if error:
        return _quarantined(
            peer_id=peer_id,
            source_variant=source_variant,
            reason=error,
            value=update,
        )
    return MappedA2AFact(
        kind=A2AMappedKind.TASK_ARTIFACT_UPDATE,
        source_variant=source_variant,
        peer_namespace=external_peer_namespace(peer_id),
        recovery=A2ARecoveryMode.GET_TASK,
        task_id=namespace_external_id(peer_id, "task", update.task_id),
        context_id=_namespace_optional(peer_id, "context", update.context_id),
        artifact_id=namespace_external_id(
            peer_id,
            "artifact",
            update.artifact.artifact_id,
        ),
        wire_payload=_proto_dict(update),
    )


def map_send_message_response(
    response: SendMessageResponse,
    *,
    peer_id: str,
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    variant = response.WhichOneof("payload")
    if variant == "task":
        return map_task(
            response.task,
            peer_id=peer_id,
            source_variant="send_message.task",
            url_validator=url_validator,
        )
    if variant == "message":
        return map_message(
            response.message,
            peer_id=peer_id,
            source_variant="send_message.message",
            url_validator=url_validator,
        )
    return _quarantined(
        peer_id=peer_id,
        source_variant="send_message.unspecified",
        reason="A2A SendMessageResponse payload is unspecified",
        value=response,
    )


def map_stream_response(
    response: StreamResponse,
    *,
    peer_id: str,
    url_validator: URLValidator | None = None,
) -> MappedA2AFact:
    variant = response.WhichOneof("payload")
    if variant == "task":
        return map_task(
            response.task,
            peer_id=peer_id,
            source_variant="stream.task",
            url_validator=url_validator,
        )
    if variant == "message":
        return map_message(
            response.message,
            peer_id=peer_id,
            source_variant="stream.message",
            url_validator=url_validator,
        )
    if variant == "status_update":
        return map_status_update(
            response.status_update,
            peer_id=peer_id,
            url_validator=url_validator,
        )
    if variant == "artifact_update":
        return map_artifact_update(
            response.artifact_update,
            peer_id=peer_id,
            url_validator=url_validator,
        )
    return _quarantined(
        peer_id=peer_id,
        source_variant="stream.unspecified",
        reason="A2A StreamResponse payload is unspecified",
        value=response,
    )


__all__ = [
    "A2AMappedKind",
    "A2ARecoveryMode",
    "EXTERNAL_ACTOR_KIND",
    "MappedA2AFact",
    "URLValidator",
    "external_peer_namespace",
    "map_artifact_update",
    "map_message",
    "map_send_message_response",
    "map_status_update",
    "map_stream_response",
    "map_task",
    "namespace_external_id",
    "validate_message_part_urls",
]
