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

from pydantic import BaseModel, ConfigDict, Field

from agentnet.identity.actors import VerifiedActor
from agentnet.messaging.conversation import ConversationAction
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest


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
)


class BoundRoomService(Protocol):
    def create(
        self,
        *,
        actor: VerifiedActor,
        classification: Classification,
        persistent: bool,
        expires_at: datetime | None,
        policy: dict[str, Any] | None = None,
    ) -> dict[str, Any]: ...

    def add_member(
        self,
        *,
        actor: VerifiedActor,
        room_id: str,
        harness_id: str,
        role: str = "member",
        mls_key_package: bytes | None = None,
    ) -> dict[str, Any]: ...

    def describe(self, *, actor: VerifiedActor, room_id: str) -> dict[str, Any]: ...


class BoundCore(Protocol):
    rooms: BoundRoomService

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
        after_cursor: int,
        limit: int,
    ) -> list[dict[str, Any]]: ...

    def acknowledge_mailbox(
        self,
        *,
        actor: VerifiedActor,
        event_id: str,
        envelope_digest: str,
    ) -> dict[str, Any]: ...

    def create_conversation(
        self,
        *,
        actor: VerifiedActor,
        conversation_id: str,
        member_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
    ) -> dict[str, Any]: ...

    def post_conversation_action(
        self,
        *,
        actor: VerifiedActor,
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
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]: ...

    def response_obligation_inbox(self, *, actor: VerifiedActor) -> dict[str, int]: ...

    def response_obligation_list(
        self,
        *,
        actor: VerifiedActor,
        role: str = "any",
        states: tuple[str, ...] = (),
        limit: int = 100,
    ) -> list[dict[str, Any]]: ...

    def response_obligation(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
    ) -> dict[str, Any]: ...

    def response_obligation_transition(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]: ...

    def response_obligation_cancel(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]: ...

    def response_obligation_reconcile(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
    ) -> dict[str, Any]: ...


ActorProvider = Callable[[], VerifiedActor]


class CanonicalToolRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    method: CanonicalToolName
    arguments: dict[str, Any]


class SendArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL


class InboxArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    after_cursor: int = Field(default=0, ge=0)
    limit: int = Field(default=25, ge=1, le=100)


class InboxAcknowledgeArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    event_id: str = Field(
        min_length=1,
        max_length=256,
        pattern=r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
    )
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")


class EmptyArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class ConversationCreateArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    conversation_id: str = Field(min_length=1, max_length=256)
    member_harness_ids: tuple[str, ...] = Field(min_length=1, max_length=1000)
    classification: Classification = Classification.C1_INTERNAL


class ConversationActionArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    conversation_id: str = Field(min_length=1, max_length=256)
    thread_id: str = Field(min_length=1, max_length=256)
    action: ConversationAction
    idempotency_key: str = Field(min_length=16, max_length=256)


class ConversationThreadArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    conversation_id: str = Field(min_length=1, max_length=256)
    thread_id: str = Field(min_length=1, max_length=256)
    limit: int = Field(default=100, ge=1, le=1000)


class RoomCreateArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    classification: Classification = Classification.C1_INTERNAL
    persistent: bool = True
    expires_at: datetime | None = None
    policy: dict[str, Any] | None = None


class RoomMemberAddArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    room_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    role: Literal["member", "guest", "moderator"] = "member"


class RoomGetArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    room_id: str = Field(min_length=1, max_length=256)


class RoomSendArguments(RoomGetArguments):
    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    expected_control_sequence: int = Field(ge=1)
    conversation_id: str | None = Field(default=None, min_length=1, max_length=256)


class ObligationListArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    role: Literal["requester", "responsible", "any"] = "any"
    states: tuple[str, ...] = Field(default=(), max_length=10)
    limit: int = Field(default=100, ge=1, le=1000)


class ObligationGetArguments(BaseModel):
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


class ObligationReconcileArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    limit: int = Field(default=100, ge=1, le=1000)


class CanonicalToolDispatcher:
    """Dispatch exact local tools under a fresh server-derived actor."""

    def __init__(self, core: BoundCore, actor_provider: ActorProvider) -> None:
        self.core = core
        self.actor_provider = actor_provider

    def call(self, method: CanonicalToolName, arguments: dict[str, Any]) -> Any:
        request = CanonicalToolRequest(method=method, arguments=arguments)
        actor = self.actor_provider()
        if request.method == "agentnet.send":
            parsed = SendArguments.model_validate(request.arguments)
            return self.core.send_message(
                actor=actor,
                recipients=parsed.recipients,
                payload=parsed.payload,
                idempotency_key=parsed.idempotency_key,
                classification=parsed.classification,
            )
        if request.method == "agentnet.inbox":
            parsed = InboxArguments.model_validate(request.arguments)
            return self.core.mailbox(
                actor=actor,
                after_cursor=parsed.after_cursor,
                limit=parsed.limit,
            )
        if request.method == "agentnet.inbox.acknowledge":
            parsed = InboxAcknowledgeArguments.model_validate(request.arguments)
            return self.core.acknowledge_mailbox(
                actor=actor,
                event_id=parsed.event_id,
                envelope_digest=parsed.envelope_digest,
            )
        if request.method == "agentnet.conversation.create":
            parsed = ConversationCreateArguments.model_validate(request.arguments)
            return self.core.create_conversation(
                actor=actor,
                conversation_id=parsed.conversation_id,
                member_harness_ids=parsed.member_harness_ids,
                classification=parsed.classification,
            )
        if request.method == "agentnet.conversation.action":
            parsed = ConversationActionArguments.model_validate(request.arguments)
            return self.core.post_conversation_action(
                actor=actor,
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
                room_id=parsed.room_id,
                harness_id=parsed.harness_id,
                role=parsed.role,
                mls_key_package=None,
            )
        if request.method == "agentnet.room.get":
            parsed = RoomGetArguments.model_validate(request.arguments)
            self.core._require(actor=actor, action="room.read", resource=parsed.room_id)
            return self.core.rooms.describe(actor=actor, room_id=parsed.room_id)
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
            EmptyArguments.model_validate(request.arguments)
            return self.core.response_obligation_inbox(actor=actor)
        if request.method == "agentnet.obligation.list":
            parsed = ObligationListArguments.model_validate(request.arguments)
            return self.core.response_obligation_list(
                actor=actor,
                role=parsed.role,
                states=parsed.states,
                limit=parsed.limit,
            )
        if request.method == "agentnet.obligation.get":
            parsed = ObligationGetArguments.model_validate(request.arguments)
            return self.core.response_obligation(
                actor=actor,
                obligation_id=parsed.obligation_id,
            )
        if request.method == "agentnet.obligation.transition":
            parsed = ObligationTransitionArguments.model_validate(request.arguments)
            return self.core.response_obligation_transition(
                actor=actor,
                obligation_id=parsed.obligation_id,
                to_state=parsed.to_state,
                reason=parsed.reason,
                expected_revision=parsed.expected_revision,
            )
        if request.method == "agentnet.obligation.cancel":
            parsed = ObligationCancelArguments.model_validate(request.arguments)
            return self.core.response_obligation_cancel(
                actor=actor,
                obligation_id=parsed.obligation_id,
                reason_code=parsed.reason_code,
                expected_revision=parsed.expected_revision,
            )
        parsed = ObligationReconcileArguments.model_validate(request.arguments)
        return self.core.response_obligation_reconcile(actor=actor, limit=parsed.limit)


__all__ = [
    "ActorProvider",
    "BoundCore",
    "BoundRoomService",
    "CANONICAL_TOOL_NAMES",
    "CanonicalToolDispatcher",
    "CanonicalToolName",
    "CanonicalToolRequest",
    "InboxAcknowledgeArguments",
    "InboxArguments",
    "ConversationActionArguments",
    "ConversationCreateArguments",
    "ConversationThreadArguments",
    "RoomCreateArguments",
    "RoomGetArguments",
    "RoomMemberAddArguments",
    "RoomSendArguments",
    "EmptyArguments",
    "ObligationCancelArguments",
    "ObligationGetArguments",
    "ObligationListArguments",
    "ObligationReconcileArguments",
    "ObligationTransitionArguments",
    "SendArguments",
]
