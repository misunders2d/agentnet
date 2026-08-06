"""Canonical local tool surface shared by direct, MCP, and Unix IPC bindings.

Identity is deliberately absent from every argument model.  A dispatcher is
constructed with a server-side actor provider and resolves that provider for
every call, so credential rotation or revocation fences an already-created
binding without accepting replacement identity claims from tool arguments.
"""

from __future__ import annotations

from collections.abc import Callable
from datetime import datetime
from typing import Any, Literal, Protocol

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
    model_validator,
)

from agentnet.authorization.communication_scope_service import CollaborationScopeService
from agentnet.discovery.recipient_resolver import AuthorizedRecipientResolver, ResolvedEndpoint
from agentnet.errors import ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.messaging.conversation import ConversationAction
from agentnet.protocol.models import Classification
from agentnet.provenance import ProvenanceReferenceV1
from agentnet.security.signatures import canonical_digest, canonical_json


CanonicalToolName = Literal[
    "agentnet.inbox",
    "agentnet.inbox.acknowledge",
    "agentnet.send",
    "agentnet.conversation.create",
    "agentnet.conversation.action",
    "agentnet.conversation.thread",
    "agentnet.room.create",
    "agentnet.room.member.add",
    "agentnet.room.get",
    "agentnet.room.send",
    "agentnet.obligation.inbox",
    "agentnet.obligation.list",
    "agentnet.obligation.get",
    "agentnet.obligation.transition",
    "agentnet.obligation.cancel",
    "agentnet.obligation.reconcile",
    "agentnet.recipient.resolve",
    "agentnet.file.send",
    "agentnet.file.status",
    "agentnet.file.download",
]
CANONICAL_TOOL_NAMES: tuple[CanonicalToolName, ...] = (
    "agentnet.inbox",
    "agentnet.inbox.acknowledge",
    "agentnet.send",
    "agentnet.conversation.create",
    "agentnet.conversation.action",
    "agentnet.conversation.thread",
    "agentnet.room.create",
    "agentnet.room.member.add",
    "agentnet.room.get",
    "agentnet.room.send",
    "agentnet.obligation.inbox",
    "agentnet.obligation.list",
    "agentnet.obligation.get",
    "agentnet.obligation.transition",
    "agentnet.obligation.cancel",
    "agentnet.obligation.reconcile",
    "agentnet.recipient.resolve",
    "agentnet.file.send",
    "agentnet.file.status",
    "agentnet.file.download",
)


class BoundRoomService(Protocol):
    def create(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        classification: Classification,
        persistent: bool,
        expires_at: datetime | None,
        policy: dict[str, Any] | None = None,
    ) -> dict[str, Any]: ...

    def add_member(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        room_id: str,
        harness_id: str,
        role: str = "member",
        mls_key_package: bytes | None = None,
    ) -> dict[str, Any]: ...

    def describe(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        room_id: str,
    ) -> dict[str, Any]: ...


class BoundCore(Protocol):
    rooms: BoundRoomService
    collaboration_scopes: CollaborationScopeService
    recipient_resolver: AuthorizedRecipientResolver


    def _require(
        self,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        classification: Classification | None = None,
        context: dict[str, Any] | None = None,
    ) -> Any: ...

    def send_message(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        payload: dict[str, Any],
        idempotency_key: str,
        classification: Classification = Classification.C1_INTERNAL,
        released_artifacts: tuple[Any, ...] = (),
        conversation_id: str | None = None,
        room_id: str | None = None,
        expected_room_control_sequence: int | None = None,
    ) -> dict[str, Any]: ...

    def mailbox(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        after_cursor: int,
        limit: int,
    ) -> list[dict[str, Any]]: ...

    def acknowledge_mailbox(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        event_id: str,
        envelope_digest: str,
    ) -> dict[str, Any]: ...


    def file_send(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        source_path: str,
        media_type: str,
        classification: Classification,
        idempotency_key: str,
    ) -> dict[str, Any]: ...

    def file_status(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        transfer_id: str,
    ) -> dict[str, Any]: ...

    def file_download(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        artifact_id: str,
        destination_path: str,
        idempotency_key: str,
    ) -> dict[str, Any]: ...

    def create_conversation(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        conversation_id: str,
        member_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
    ) -> dict[str, Any]: ...

    def post_conversation_action(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        conversation_id: str,
        thread_id: str,
        action: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]: ...

    def conversation_thread(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]: ...

    def response_obligation_inbox(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
    ) -> dict[str, int]: ...

    def response_obligation_list(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        role: str = "any",
        states: tuple[str, ...] = (),
        limit: int = 100,
    ) -> list[dict[str, Any]]: ...

    def response_obligation(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        obligation_id: str,
    ) -> dict[str, Any]: ...

    def response_obligation_transition(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]: ...

    def response_obligation_cancel(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]: ...

    def response_obligation_reconcile(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        limit: int = 100,
    ) -> dict[str, Any]: ...


ActorProvider = Callable[[], VerifiedActor]


class CanonicalToolRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    method: CanonicalToolName
    arguments: dict[str, Any]


class SendArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipient_query: str | None = Field(default=None, min_length=1, max_length=256)
    recipients: tuple[str, ...] | None = Field(default=None, min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL

    @model_validator(mode="after")
    def exact_recipient_form(self) -> "SendArguments":
        if (self.recipient_query is None) == (self.recipients is None):
            raise ValueError("exactly one recipient form is required")
        if self.recipients is not None and (
            len(self.recipients) != len(set(self.recipients))
            or any(not recipient or len(recipient) > 256 for recipient in self.recipients)
        ):
            raise ValueError("exact recipients must be a bounded unique tuple")
        return self


class SendAcceptanceResult(BaseModel):
    """Exact receipt fields produced by the authoritative mailbox acceptor."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    event_id: str = Field(min_length=1, max_length=256)
    fact: Literal["accepted_local", "accepted_durable"]
    duplicate: bool
    receipt_id: str | None = Field(default=None, min_length=1, max_length=256)
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    provenance: ProvenanceReferenceV1
    audit_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


class SendToolResult(SendAcceptanceResult):
    """Minimal public receipt plus proof-derived recipient display metadata."""

    recipient_harness_ids: tuple[str, ...] = Field(min_length=1, max_length=1000)
    recipient_display_metadata: tuple[ResolvedEndpoint, ...] = Field(max_length=20)


def public_send_result(
    value: Any,
    *,
    recipient_harness_ids: tuple[str, ...],
    recipient_display_metadata: tuple[ResolvedEndpoint, ...],
) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValidationError("canonical send result schema is invalid")
    try:
        accepted = SendAcceptanceResult.model_validate_json(canonical_json(value))
        result = SendToolResult(
            event_id=accepted.event_id,
            fact=accepted.fact,
            duplicate=accepted.duplicate,
            receipt_id=accepted.receipt_id,
            envelope_digest=accepted.envelope_digest,
            provenance=accepted.provenance,
            audit_hash=accepted.audit_hash,
            recipient_harness_ids=recipient_harness_ids,
            recipient_display_metadata=recipient_display_metadata,
        )
    except (PydanticValidationError, ValidationError):
        raise ValidationError("canonical send result schema is invalid") from None
    return result.model_dump(mode="json")


class CollaborationScopeArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)


class InboxArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    after_cursor: int = Field(default=0, ge=0)
    limit: int = Field(default=25, ge=1, le=100)


class InboxAcknowledgeArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    event_id: str = Field(
        min_length=1,
        max_length=256,
        pattern=r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
    )
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")



class RecipientResolveArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    query: str = Field(min_length=1, max_length=256)


class FileSendArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    source_path: str = Field(min_length=1, max_length=4096)
    media_type: str = Field(min_length=3, max_length=255)
    classification: Classification = Classification.C1_INTERNAL
    idempotency_key: str = Field(min_length=16, max_length=256)


class FileStatusArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    transfer_id: str = Field(min_length=1, max_length=256)


class FileDownloadArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    artifact_id: str = Field(min_length=1, max_length=256)
    destination_path: str = Field(min_length=1, max_length=4096)
    idempotency_key: str = Field(min_length=16, max_length=256)


class _FileTransferToolResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    transfer_id: str = Field(min_length=1, max_length=256)
    state: Literal[
        "reserved",
        "quarantined",
        "scanning",
        "released",
        "event_committed",
        "recipient_committed",
        "failed",
        "canceled",
    ]
    artifact_id: str | None = Field(default=None, min_length=1, max_length=256)
    event_id: str | None = Field(default=None, min_length=1, max_length=256)
    digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    size: int = Field(ge=0)
    media_type: str = Field(min_length=3, max_length=255)


class _FileDownloadToolResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    artifact_id: str = Field(min_length=1, max_length=256)
    state: Literal["materialized"]
    destination_path: str = Field(min_length=1, max_length=4096)
    digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    size: int = Field(ge=0)


def _public_file_result(model: type[BaseModel], value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("file tool result must be an object")
    projected = {name: value[name] for name in model.model_fields if name in value}
    return model.model_validate(projected).model_dump(mode="json")


class ConversationCreateArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    conversation_id: str = Field(min_length=1, max_length=256)
    member_harness_ids: tuple[str, ...] = Field(min_length=1, max_length=1000)
    classification: Classification = Classification.C1_INTERNAL


class ConversationActionArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    conversation_id: str = Field(min_length=1, max_length=256)
    thread_id: str = Field(min_length=1, max_length=256)
    action: ConversationAction
    idempotency_key: str = Field(min_length=16, max_length=256)


class ConversationThreadArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    conversation_id: str = Field(min_length=1, max_length=256)
    thread_id: str = Field(min_length=1, max_length=256)
    limit: int = Field(default=100, ge=1, le=1000)


class RoomCreateArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    classification: Classification = Classification.C1_INTERNAL
    persistent: bool = True
    expires_at: datetime | None = None
    policy: dict[str, Any] | None = None


class RoomMemberAddArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    room_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    role: Literal["member", "guest", "moderator"] = "member"


class RoomGetArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    room_id: str = Field(min_length=1, max_length=256)


class RoomSendArguments(RoomGetArguments):
    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    expected_control_sequence: int = Field(ge=1)
    conversation_id: str | None = Field(default=None, min_length=1, max_length=256)


class ObligationListArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    role: Literal["requester", "responsible", "any"] = "any"
    states: tuple[str, ...] = Field(default=(), max_length=10)
    limit: int = Field(default=100, ge=1, le=1000)


class ObligationGetArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    obligation_id: str = Field(min_length=1, max_length=256)


class ObligationTransitionArguments(ObligationGetArguments):
    to_state: Literal[
        "recipient_committed", "acknowledged", "in_progress", "pending_human", "blocked"
    ]
    reason: str = Field(default="recipient_update", min_length=1, max_length=128)
    expected_revision: int | None = Field(default=None, ge=1)


class ObligationCancelArguments(ObligationGetArguments):
    reason_code: str = Field(default="requester_canceled", min_length=1, max_length=128)
    expected_revision: int | None = Field(default=None, ge=1)


class ObligationReconcileArguments(CollaborationScopeArguments):
    model_config = ConfigDict(extra="forbid", frozen=True)

    limit: int = Field(default=100, ge=1, le=1000)


class CanonicalToolDispatcher:
    """Dispatch exact local tools under a fresh server-derived actor."""

    def __init__(self, core: BoundCore, actor_provider: ActorProvider) -> None:
        self.core = core
        self.actor_provider = actor_provider

    def call(self, method: CanonicalToolName, arguments: dict[str, Any]) -> Any:
        actor = self.actor_provider()
        request = CanonicalToolRequest(method=method, arguments=arguments)
        if request.method == "agentnet.recipient.resolve":
            parsed = RecipientResolveArguments.model_validate(request.arguments)
            resolved = self.core.recipient_resolver.resolve(actor=actor, query=parsed.query)
            return [endpoint.model_dump(mode="json") for endpoint in resolved]
        if request.method == "agentnet.file.send":
            parsed = FileSendArguments.model_validate(request.arguments)
            result = self.core.file_send(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                recipients=parsed.recipients,
                source_path=parsed.source_path,
                media_type=parsed.media_type,
                classification=parsed.classification,
                idempotency_key=parsed.idempotency_key,
            )
            return _public_file_result(_FileTransferToolResult, result)
        if request.method == "agentnet.file.status":
            parsed = FileStatusArguments.model_validate(request.arguments)
            result = self.core.file_status(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                transfer_id=parsed.transfer_id,
            )
            return _public_file_result(_FileTransferToolResult, result)
        if request.method == "agentnet.file.download":
            parsed = FileDownloadArguments.model_validate(request.arguments)
            result = self.core.file_download(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                artifact_id=parsed.artifact_id,
                destination_path=parsed.destination_path,
                idempotency_key=parsed.idempotency_key,
            )
            return _public_file_result(_FileDownloadToolResult, result)
        if request.method == "agentnet.send":
            parsed = SendArguments.model_validate(request.arguments)
            if parsed.recipient_query is not None:
                resolved = self.core.recipient_resolver.resolve(
                    actor=actor,
                    query=parsed.recipient_query,
                )
                if len(resolved) != 1:
                    raise ValidationError("recipient could not be resolved")
                recipients = (resolved[0].harness_id,)
                selected_scope_id: str | None = resolved[0].scope_id
                display_metadata = resolved
            else:
                assert parsed.recipients is not None
                recipients = tuple(parsed.recipients)
                selected_scope_id = None
                display_metadata = ()
            scope = self.core.collaboration_scopes.require(
                actor=actor,
                scope_id=selected_scope_id,
                action="message.send",
                resource="conversation:direct",
                target_harness_ids=recipients,
                classification=parsed.classification,
            )
            result = self.core.send_message(
                actor=actor,
                collaboration_scope_id=scope.scope_id,
                recipients=recipients,
                payload=parsed.payload,
                idempotency_key=parsed.idempotency_key,
                classification=parsed.classification,
            )
            return public_send_result(
                result,
                recipient_harness_ids=recipients,
                recipient_display_metadata=display_metadata,
            )
        if request.method == "agentnet.inbox":
            parsed = InboxArguments.model_validate(request.arguments)
            return self.core.mailbox(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                after_cursor=parsed.after_cursor,
                limit=parsed.limit,
            )
        if request.method == "agentnet.inbox.acknowledge":
            parsed = InboxAcknowledgeArguments.model_validate(request.arguments)
            return self.core.acknowledge_mailbox(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                event_id=parsed.event_id,
                envelope_digest=parsed.envelope_digest,
            )
        if request.method == "agentnet.conversation.create":
            parsed = ConversationCreateArguments.model_validate(request.arguments)
            return self.core.create_conversation(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                conversation_id=parsed.conversation_id,
                member_harness_ids=parsed.member_harness_ids,
                classification=parsed.classification,
            )
        if request.method == "agentnet.conversation.action":
            parsed = ConversationActionArguments.model_validate(request.arguments)
            return self.core.post_conversation_action(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                recipients=parsed.recipients,
                conversation_id=parsed.conversation_id,
                thread_id=parsed.thread_id,
                action=parsed.action.model_dump(mode="json", exclude_none=True),
                idempotency_key=parsed.idempotency_key,
            )
        if request.method == "agentnet.conversation.thread":
            parsed = ConversationThreadArguments.model_validate(request.arguments)
            return self.core.conversation_thread(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                conversation_id=parsed.conversation_id,
                thread_id=parsed.thread_id,
                limit=parsed.limit,
            )
        if request.method == "agentnet.room.create":
            parsed = RoomCreateArguments.model_validate(request.arguments)
            self.core._require(
                actor=actor,
                action="room.create",
                resource="room:new",
                classification=parsed.classification,
                context={
                    "classification": parsed.classification.value,
                    "persistent": parsed.persistent,
                    "expires_at": parsed.expires_at.isoformat() if parsed.expires_at else None,
                    "policy_digest": canonical_digest(parsed.policy or {}),
                },
            )
            return self.core.rooms.create(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                classification=parsed.classification,
                persistent=parsed.persistent,
                expires_at=parsed.expires_at,
                policy=parsed.policy,
            )
        if request.method == "agentnet.room.member.add":
            parsed = RoomMemberAddArguments.model_validate(request.arguments)
            self.core._require(
                actor=actor,
                action="room.action",
                resource=parsed.room_id,
                context={
                    "harness_id": parsed.harness_id,
                    "role": parsed.role,
                    "mls_key_package_digest": None,
                    "operation": "member.add",
                },
            )
            return self.core.rooms.add_member(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                room_id=parsed.room_id,
                harness_id=parsed.harness_id,
                role=parsed.role,
                mls_key_package=None,
            )
        if request.method == "agentnet.room.get":
            parsed = RoomGetArguments.model_validate(request.arguments)
            self.core._require(actor=actor, action="room.read", resource=parsed.room_id)
            return self.core.rooms.describe(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                room_id=parsed.room_id,
            )
        if request.method == "agentnet.room.send":
            parsed = RoomSendArguments.model_validate(request.arguments)
            self.core._require(
                actor=actor,
                action="room.action",
                resource=parsed.room_id,
                classification=parsed.classification,
                context={
                    "operation": "message.send",
                    "recipient_harness_ids": sorted(parsed.recipients),
                    "payload_digest": canonical_digest(parsed.payload),
                    "expected_control_sequence": parsed.expected_control_sequence,
                },
            )
            return self.core.send_message(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                recipients=parsed.recipients,
                payload=parsed.payload,
                idempotency_key=parsed.idempotency_key,
                classification=parsed.classification,
                released_artifacts=(),
                conversation_id=parsed.conversation_id,
                room_id=parsed.room_id,
                expected_room_control_sequence=parsed.expected_control_sequence,
            )
        if request.method == "agentnet.obligation.inbox":
            parsed = CollaborationScopeArguments.model_validate(request.arguments)
            return self.core.response_obligation_inbox(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
            )
        if request.method == "agentnet.obligation.list":
            parsed = ObligationListArguments.model_validate(request.arguments)
            return self.core.response_obligation_list(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                role=parsed.role,
                states=parsed.states,
                limit=parsed.limit,
            )
        if request.method == "agentnet.obligation.get":
            parsed = ObligationGetArguments.model_validate(request.arguments)
            return self.core.response_obligation(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                obligation_id=parsed.obligation_id,
            )
        if request.method == "agentnet.obligation.transition":
            parsed = ObligationTransitionArguments.model_validate(request.arguments)
            return self.core.response_obligation_transition(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                obligation_id=parsed.obligation_id,
                to_state=parsed.to_state,
                reason=parsed.reason,
                expected_revision=parsed.expected_revision,
            )
        if request.method == "agentnet.obligation.cancel":
            parsed = ObligationCancelArguments.model_validate(request.arguments)
            return self.core.response_obligation_cancel(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                obligation_id=parsed.obligation_id,
                reason_code=parsed.reason_code,
                expected_revision=parsed.expected_revision,
            )
        parsed = ObligationReconcileArguments.model_validate(request.arguments)
        return self.core.response_obligation_reconcile(
            actor=actor,
            collaboration_scope_id=parsed.collaboration_scope_id,
            limit=parsed.limit,
        )


__all__ = [
    "ActorProvider",
    "BoundCore",
    "BoundRoomService",
    "CANONICAL_TOOL_NAMES",
    "CanonicalToolDispatcher",
    "CanonicalToolName",
    "CanonicalToolRequest",
    "InboxAcknowledgeArguments",
    "FileDownloadArguments",
    "FileSendArguments",
    "FileStatusArguments",
    "InboxArguments",
    "ConversationActionArguments",
    "ConversationCreateArguments",
    "ConversationThreadArguments",
    "RoomCreateArguments",
    "RoomGetArguments",
    "RoomMemberAddArguments",
    "RoomSendArguments",
    "CollaborationScopeArguments",
    "ObligationCancelArguments",
    "ObligationGetArguments",
    "ObligationListArguments",
    "ObligationReconcileArguments",
    "ObligationTransitionArguments",
    "RecipientResolveArguments",
    "SendAcceptanceResult",
    "SendToolResult",
    "public_send_result",
    "SendArguments",
]
