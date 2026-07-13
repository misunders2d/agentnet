from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES, CanonicalToolDispatcher


class RecordingCore:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []

    def _record(self, name: str, **arguments: Any) -> dict[str, Any]:
        self.calls.append((name, arguments))
        return {"operation": name}

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
            "classification": "C1",
            "conversation_id": "conversation:binding",
            "member_harness_ids": ["harness-responder"],
        },
    )
    dispatcher.call(
        "agentnet.conversation.action",
        {
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
    dispatcher.call("agentnet.obligation.inbox", {})
    dispatcher.call(
        "agentnet.obligation.transition",
        {
            "expected_revision": 1,
            "obligation_id": "obligation-1",
            "reason": "accepted",
            "to_state": "acknowledged",
        },
    )
    dispatcher.call(
        "agentnet.conversation.action",
        {
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
        "agentnet.send",
        "agentnet.conversation.create",
        "agentnet.conversation.action",
        "agentnet.conversation.thread",
        "agentnet.obligation.inbox",
        "agentnet.obligation.list",
        "agentnet.obligation.get",
        "agentnet.obligation.transition",
        "agentnet.obligation.cancel",
        "agentnet.obligation.reconcile",
    )
    mcp = create_mcp_binding(dispatcher)
    assert [tool.name for tool in mcp._tool_manager.list_tools()] == [
        "agentnet_send",
        "agentnet_inbox",
        "agentnet_conversation_create",
        "agentnet_conversation_action",
        "agentnet_conversation_thread",
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


def test_pi_extension_exposes_the_same_canonical_journey_as_mcp() -> None:
    source = Path("src/agentnet/bindings/pi_extension.ts").read_text(encoding="utf-8")
    for method in CANONICAL_TOOL_NAMES:
        assert f'"{method}"' in source
        assert f'name: "{method.replace(".", "_")}"' in source


def test_canonical_binding_rejects_identity_injection_and_non_obligation_marker() -> None:
    dispatcher = CanonicalToolDispatcher(RecordingCore(), object)  # type: ignore[arg-type]
    base = {
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
