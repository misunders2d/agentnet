from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES, CanonicalToolDispatcher
from agentnet.security.signatures import canonical_digest


class RecordingRooms:
    def __init__(self, core: RecordingCore) -> None:
        self.core = core

    def create(self, **arguments: Any) -> dict[str, Any]:
        return self.core._record("room.create", **arguments)

    def add_member(self, **arguments: Any) -> dict[str, Any]:
        return self.core._record("room.member.add", **arguments)

    def describe(self, **arguments: Any) -> dict[str, Any]:
        return self.core._record("room.get", **arguments)



class RecordingCore:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.rooms = RecordingRooms(self)

    def _record(self, name: str, **arguments: Any) -> dict[str, Any]:
        self.calls.append((name, arguments))
        return {"operation": name}


    def _require(self, **arguments: Any) -> dict[str, Any]:
        return self._record("require", **arguments)

    def send_message(self, **arguments: Any) -> dict[str, Any]:
        return self._record("send", **arguments)

    def acknowledge_mailbox(self, **arguments: Any) -> dict[str, Any]:
        return self._record("inbox.acknowledge", **arguments)

    def create_conversation(self, **arguments: Any) -> dict[str, Any]:
        return self._record("conversation.create", **arguments)

    def post_conversation_action(self, **arguments: Any) -> dict[str, Any]:
        return self._record("conversation.action", **arguments)

    def conversation_thread(self, **arguments: Any) -> list[dict[str, Any]]:
        self._record("conversation.thread", **arguments)
        return []

    def response_obligation_inbox(self, **arguments: Any) -> dict[str, int]:
        self._record("obligation.inbox", **arguments)
        return {"action_required": 0}

    def response_obligation_list(self, **arguments: Any) -> list[dict[str, Any]]:
        self._record("obligation.list", **arguments)
        return []

    def response_obligation(self, **arguments: Any) -> dict[str, Any]:
        return self._record("obligation.get", **arguments)

    def response_obligation_transition(self, **arguments: Any) -> dict[str, Any]:
        return self._record("obligation.transition", **arguments)

    def response_obligation_cancel(self, **arguments: Any) -> dict[str, Any]:
        return self._record("obligation.cancel", **arguments)

    def response_obligation_reconcile(self, **arguments: Any) -> dict[str, Any]:
        return self._record("obligation.reconcile", **arguments)


def test_canonical_binding_exposes_complete_response_obligation_journey() -> None:
    actor = object()
    core = RecordingCore()
    dispatcher = CanonicalToolDispatcher(core, lambda: actor)  # type: ignore[arg-type]

    dispatcher.call(
        "agentnet.conversation.create",
        {
            "collaboration_scope_id": "scope-1",
            "classification": "C1",
            "conversation_id": "conversation:binding",
            "member_harness_ids": ["harness-responder"],
        },
    )
    dispatcher.call(
        "agentnet.conversation.action",
        {
            "collaboration_scope_id": "scope-1",
            "action": {
                "kind": "structured_request",
                "arguments": {"sku": "ABC-123"},
                "request_type": "inventory.lookup",
                "response_obligation": {
                    "responsible_harness_id": "harness-responder",
                    "response_schema_id": "inventory.lookup.result",
                    "response_schema": {
                        "$schema": "https://json-schema.org/draft/2020-12/schema",
                        "type": "object",
                        "properties": {"quantity": {"type": "integer"}},
                        "required": ["quantity"],
                        "additionalProperties": False,
                    },
                },
            },
            "conversation_id": "conversation:binding",
            "idempotency_key": "binding-request-0001",
            "recipients": ["harness-responder"],
            "thread_id": "thread:binding",
        },
    )
    dispatcher.call(
        "agentnet.obligation.inbox",
        {"collaboration_scope_id": "scope-1"},
    )
    dispatcher.call(
        "agentnet.obligation.transition",
        {
            "collaboration_scope_id": "scope-1",
            "expected_revision": 1,
            "obligation_id": "obligation-1",
            "reason": "accepted",
            "to_state": "acknowledged",
        },
    )
    dispatcher.call(
        "agentnet.conversation.action",
        {
            "collaboration_scope_id": "scope-1",
            "action": {
                "body": "four units",
                "kind": "obligation_response",
                "obligation_id": "obligation-1",
                "outcome": "completed",
                "request_digest": "a" * 64,
                "request_event_id": "request-event-1",
                "response_schema_id": "inventory.lookup.result",
                "structured_response": {"quantity": 4},
            },
            "conversation_id": "conversation:binding",
            "idempotency_key": "binding-response-0001",
            "recipients": ["harness-requester"],
            "thread_id": "thread:binding",
        },
    )

    assert CANONICAL_TOOL_NAMES == (
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
    mcp = create_mcp_binding(dispatcher)
    assert [tool.name for tool in mcp._tool_manager.list_tools()] == [
        "agentnet_recipient_resolve",
        "agentnet_file_send",
        "agentnet_file_status",
        "agentnet_file_download",
        "agentnet_send",
        "agentnet_inbox",
        "agentnet_inbox_acknowledge",
        "agentnet_conversation_create",
        "agentnet_conversation_action",
        "agentnet_conversation_thread",
        "agentnet_room_create",
        "agentnet_room_member_add",
        "agentnet_room_get",
        "agentnet_room_send",
        "agentnet_obligation_inbox",
        "agentnet_obligation_list",
        "agentnet_obligation_get",
        "agentnet_obligation_transition",
        "agentnet_obligation_cancel",
        "agentnet_obligation_reconcile",
    ]
    assert [name for name, _arguments in core.calls] == [
        "conversation.create",
        "conversation.action",
        "obligation.inbox",
        "obligation.transition",
        "conversation.action",
    ]
    request = core.calls[1][1]
    assert request["actor"] is actor
    assert request["action"]["response_obligation"]["response_required"] is True
    assert core.calls[-1][1]["action"]["kind"] == "obligation_response"


def test_canonical_inbox_acknowledgement_has_no_identity_or_recipient_arguments() -> None:
    actor = object()
    core = RecordingCore()
    dispatcher = CanonicalToolDispatcher(core, lambda: actor)  # type: ignore[arg-type]

    result = dispatcher.call(
        "agentnet.inbox.acknowledge",
        {
            "collaboration_scope_id": "scope-1",
            "event_id": "event-1",
            "envelope_digest": "a" * 64,
        },
    )
    assert result == {"operation": "inbox.acknowledge"}
    assert core.calls == [
        (
            "inbox.acknowledge",
            {
                "collaboration_scope_id": "scope-1",
                "actor": actor,
                "event_id": "event-1",
                "envelope_digest": "a" * 64,
            },
        )
    ]
    for injected in (
        {"recipient_id": "attacker"},
        {"actor": {"harness_id": "attacker"}},
        {"credential_id": "attacker"},
    ):
        with pytest.raises(PydanticValidationError):
            dispatcher.call(
                "agentnet.inbox.acknowledge",
                {
                    "collaboration_scope_id": "scope-1",
                    "event_id": "event-1",
                    "envelope_digest": "a" * 64,
                    **injected,
                },
            )


def test_canonical_room_tools_call_production_surfaces_without_unsafe_arguments() -> None:
    actor = object()
    core = RecordingCore()
    dispatcher = CanonicalToolDispatcher(core, lambda: actor)  # type: ignore[arg-type]

    dispatcher.call(
        "agentnet.room.create",
        {"collaboration_scope_id": "scope-1"},
    )
    dispatcher.call(
        "agentnet.room.member.add",
        {
            "collaboration_scope_id": "scope-1",
            "harness_id": "harness-member-0001",
            "role": "moderator",
            "room_id": "room:/owner",
        },
    )
    dispatcher.call(
        "agentnet.room.get",
        {"collaboration_scope_id": "scope-1", "room_id": "room:/owner"},
    )
    dispatcher.call(
        "agentnet.room.send",
        {
            "collaboration_scope_id": "scope-1",
            "expected_control_sequence": 4,
            "idempotency_key": "room-message-0001",
            "payload": {"body": "room hello"},
            "recipients": ["harness-member-0001"],
            "room_id": "room:/owner",
        },
    )

    assert [name for name, _arguments in core.calls] == [
        "require",
        "room.create",
        "require",
        "room.member.add",
        "require",
        "room.get",
        "require",
        "send",
    ]
    assert core.calls[0][1] == {
        "action": "room.create",
        "actor": actor,
        "classification": core.calls[0][1]["classification"],
        "context": {
            "classification": "C1",
            "expires_at": None,
            "persistent": True,
            "policy_digest": canonical_digest({}),
        },
        "resource": "room:new",
    }
    assert core.calls[1][1] == {
        "actor": actor,
        "collaboration_scope_id": "scope-1",
        "classification": core.calls[0][1]["classification"],
        "expires_at": None,
        "persistent": True,
        "policy": None,
    }
    assert core.calls[2][1]["action"] == "room.action"
    assert core.calls[2][1]["context"] == {
        "harness_id": "harness-member-0001",
        "mls_key_package_digest": None,
        "operation": "member.add",
        "role": "moderator",
    }
    assert core.calls[3][1] == {
        "actor": actor,
        "collaboration_scope_id": "scope-1",
        "harness_id": "harness-member-0001",
        "mls_key_package": None,
        "role": "moderator",
        "room_id": "room:/owner",
    }
    assert core.calls[4][1] == {
        "action": "room.read",
        "actor": actor,
        "resource": "room:/owner",
    }
    assert core.calls[5][1] == {
        "actor": actor,
        "collaboration_scope_id": "scope-1",
        "room_id": "room:/owner",
    }
    assert core.calls[6][1] == {
        "action": "room.action",
        "actor": actor,
        "classification": core.calls[0][1]["classification"],
        "context": {
            "expected_control_sequence": 4,
            "operation": "message.send",
            "payload_digest": canonical_digest({"body": "room hello"}),
            "recipient_harness_ids": ["harness-member-0001"],
        },
        "resource": "room:/owner",
    }
    assert core.calls[7][1] == {
        "actor": actor,
        "collaboration_scope_id": "scope-1",
        "classification": core.calls[0][1]["classification"],
        "conversation_id": None,
        "expected_room_control_sequence": 4,
        "idempotency_key": "room-message-0001",
        "payload": {"body": "room hello"},
        "recipients": ("harness-member-0001",),
        "released_artifacts": (),
        "room_id": "room:/owner",
    }

    valid_arguments = {
        "agentnet.room.create": {"collaboration_scope_id": "scope-1"},
        "agentnet.room.member.add": {
            "collaboration_scope_id": "scope-1",
            "harness_id": "harness-member-0001",
            "room_id": "room-1",
        },
        "agentnet.room.get": {
            "collaboration_scope_id": "scope-1",
            "room_id": "room-1",
        },
        "agentnet.room.send": {
            "collaboration_scope_id": "scope-1",
            "expected_control_sequence": 1,
            "idempotency_key": "room-message-0002",
            "payload": {"body": "safe"},
            "recipients": ["harness-member-0001"],
            "room_id": "room-1",
        },
    }
    for method, arguments in valid_arguments.items():
        with pytest.raises(PydanticValidationError):
            dispatcher.call(method, {**arguments, "actor": {"harness_id": "attacker"}})  # type: ignore[arg-type]
    with pytest.raises(PydanticValidationError):
        dispatcher.call(
            "agentnet.room.send",
            {
                **valid_arguments["agentnet.room.send"],
                "released_artifacts": [{"artifact_id": "artifact-1"}],
            },
        )


def test_pi_extension_exposes_the_same_canonical_journey_as_mcp() -> None:
    source = Path("src/agentnet/bindings/pi_extension.ts").read_text(encoding="utf-8")
    for method in CANONICAL_TOOL_NAMES:
        assert f'"{method}"' in source
        assert f'name: "{method.replace(".", "_")}"' in source


def test_canonical_binding_rejects_identity_injection_and_non_obligation_marker() -> None:
    dispatcher = CanonicalToolDispatcher(RecordingCore(), object)  # type: ignore[arg-type]
    base = {
        "collaboration_scope_id": "scope-1",
        "action": {"body": "answer this", "kind": "post", "response_obligation": {}},
        "conversation_id": "conversation:binding",
        "idempotency_key": "binding-request-0002",
        "recipients": ["harness-responder"],
        "thread_id": "thread:binding",
    }
    with pytest.raises(PydanticValidationError):
        dispatcher.call(
            "agentnet.conversation.action",
            {**base, "actor": {"harness_id": "attacker-selected"}},
        )
    with pytest.raises(PydanticValidationError, match="response_required"):
        dispatcher.call(
            "agentnet.conversation.action",
            {
                **base,
                "action": {
                    "body": "not actually required",
                    "kind": "post",
                    "response_obligation": {"response_required": False},
                },
            },
        )
