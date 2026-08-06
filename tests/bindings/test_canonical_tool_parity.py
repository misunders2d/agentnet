from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.tools import (
    CANONICAL_TOOL_NAMES,
    CanonicalToolDispatcher,
    CanonicalToolRequest,
)
from agentnet.discovery.recipient_resolver import ResolvedEndpoint
from agentnet.protocol.models import Classification


EXPECTED_V0145_TOOLS = {
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
}
_RESOLVED_RESULT = [
    {
        "availability": "online",
        "display_name": "The enrolled server",
        "harness_id": "server-harness-0001",
        "harness_kind": "server",
        "scope_id": "scope-1",
    }
]
_TRANSFER_RESULT = {
    "artifact_id": "artifact:/one",
    "digest": "d" * 64,
    "event_id": "event:/one",
    "media_type": "text/plain",
    "size": 12,
    "state": "recipient_committed",
    "transfer_id": "transfer:/one",
}
_DOWNLOAD_RESULT = {
    "artifact_id": "artifact:/one",
    "destination_path": "/safe/destination/report.txt",
    "digest": "d" * 64,
    "size": 12,
    "state": "materialized",
}


_NEW_TOOL_CASES = (
    (
        "agentnet.recipient.resolve",
        {"query": "the enrolled server"},
        "recipient.resolve",
        {"query": "the enrolled server"},
        _RESOLVED_RESULT,
    ),
    (
        "agentnet.file.send",
        {
            "collaboration_scope_id": "scope-1",
            "recipients": ["server-harness-0001"],
            "source_path": "/safe/source/report.txt",
            "media_type": "text/plain",
            "classification": "C1",
            "idempotency_key": "file-send-parity-0001",
        },
        "file.send",
        {
            "collaboration_scope_id": "scope-1",
            "recipients": ("server-harness-0001",),
            "source_path": "/safe/source/report.txt",
            "media_type": "text/plain",
            "classification": Classification.C1_INTERNAL,
            "idempotency_key": "file-send-parity-0001",
        },
        _TRANSFER_RESULT,
    ),
    (
        "agentnet.file.status",
        {"collaboration_scope_id": "scope-1", "transfer_id": "transfer:/one"},
        "file.status",
        {"collaboration_scope_id": "scope-1", "transfer_id": "transfer:/one"},
        _TRANSFER_RESULT,
    ),
    (
        "agentnet.file.download",
        {
            "collaboration_scope_id": "scope-1",
            "artifact_id": "artifact:/one",
            "destination_path": "/safe/destination/report.txt",
            "idempotency_key": "file-download-parity-0001",
        },
        "file.download",
        {
            "collaboration_scope_id": "scope-1",
            "artifact_id": "artifact:/one",
            "destination_path": "/safe/destination/report.txt",
            "idempotency_key": "file-download-parity-0001",
        },
        _DOWNLOAD_RESULT,
    ),
)


class StrictRecipientResolver:
    def __init__(self, core: "StrictBoundCore") -> None:
        self.core = core

    def resolve(self, *, actor: object, query: str) -> tuple[ResolvedEndpoint, ...]:
        result = self.core._record(
            "recipient.resolve",
            actor,
            {"query": query},
            _RESOLVED_RESULT,
        )
        return tuple(ResolvedEndpoint.model_validate(item) for item in result)


class StrictBoundCore:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.recipient_resolver = StrictRecipientResolver(self)

    def _record(
        self,
        operation: str,
        actor: object,
        arguments: dict[str, Any],
        result: Any,
    ) -> Any:
        self.calls.append((operation, {"actor": actor, **arguments}))
        return result


    def file_send(
        self,
        *,
        actor: object,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        source_path: str,
        media_type: str,
        classification: Classification,
        idempotency_key: str,
    ) -> dict[str, Any]:
        return self._record(
            "file.send",
            actor,
            {
                "collaboration_scope_id": collaboration_scope_id,
                "recipients": recipients,
                "source_path": source_path,
                "media_type": media_type,
                "classification": classification,
                "idempotency_key": idempotency_key,
            },
            _TRANSFER_RESULT,
        )

    def file_status(
        self,
        *,
        actor: object,
        collaboration_scope_id: str,
        transfer_id: str,
    ) -> dict[str, Any]:
        return self._record(
            "file.status",
            actor,
            {
                "collaboration_scope_id": collaboration_scope_id,
                "transfer_id": transfer_id,
            },
            _TRANSFER_RESULT,
        )

    def file_download(
        self,
        *,
        actor: object,
        collaboration_scope_id: str,
        artifact_id: str,
        destination_path: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        return self._record(
            "file.download",
            actor,
            {
                "collaboration_scope_id": collaboration_scope_id,
                "artifact_id": artifact_id,
                "destination_path": destination_path,
                "idempotency_key": idempotency_key,
            },
            _DOWNLOAD_RESULT,
        )


def _mcp_value(value: Any) -> Any:
    if isinstance(value, dict):
        if set(value) == {"result"} and isinstance(value["result"], (dict, list)):
            return value["result"]
        return value
    if isinstance(value, list):
        return value
    if (
        isinstance(value, tuple)
        and len(value) == 2
        and isinstance(value[1], (dict, list))
    ):
        return _mcp_value(value[1])
    for item in value:
        if isinstance(item, list):
            return _mcp_value(item)
        text = getattr(item, "text", None)
        if isinstance(text, str):
            parsed = json.loads(text)
            if isinstance(parsed, (dict, list)):
                return _mcp_value(parsed)
    raise AssertionError("MCP result did not contain canonical JSON")


def _provider(actors: list[object], calls: list[object]) -> Callable[[], object]:
    remaining = iter(actors)

    def provide() -> object:
        actor = next(remaining)
        calls.append(actor)
        return actor

    return provide


@pytest.mark.parametrize("binding", ["direct_ipc", "mcp", "remote"])
def test_each_binding_exposes_exact_v0145_surface(binding: str) -> None:
    core = StrictBoundCore()
    dispatcher = CanonicalToolDispatcher(core, lambda: object())  # type: ignore[arg-type]

    if binding == "mcp":
        registered = {
            tool.name.replace("_", ".")
            for tool in create_mcp_binding(dispatcher)._tool_manager.list_tools()
        }
    else:
        registered = {
            CanonicalToolRequest.model_validate({"method": name, "arguments": {}}).method
            for name in CANONICAL_TOOL_NAMES
        }

    assert set(CANONICAL_TOOL_NAMES) == EXPECTED_V0145_TOOLS
    assert registered == EXPECTED_V0145_TOOLS


@pytest.mark.anyio
@pytest.mark.parametrize("binding", ["direct_ipc", "mcp"])
async def test_new_tools_have_exact_dispatch_parity_and_refresh_actor_per_call(binding: str) -> None:
    actors = [object() for _case in _NEW_TOOL_CASES]
    provider_calls: list[object] = []
    core = StrictBoundCore()
    dispatcher = CanonicalToolDispatcher(
        core, _provider(actors, provider_calls)  # type: ignore[arg-type]
    )
    mcp = create_mcp_binding(dispatcher)

    results: list[Any] = []
    for method, arguments, _operation, _expected_arguments, _expected_result in _NEW_TOOL_CASES:
        if binding == "mcp":
            results.append(
                _mcp_value(await mcp.call_tool(method.replace(".", "_"), arguments))
            )
        else:
            results.append(dispatcher.call(method, arguments))  # type: ignore[arg-type]

    assert results == [case[4] for case in _NEW_TOOL_CASES]
    assert provider_calls == actors
    assert core.calls == [
        (operation, {"actor": actor, **expected_arguments})
        for actor, (_method, _arguments, operation, expected_arguments, _result) in zip(
            actors, _NEW_TOOL_CASES, strict=True
        )
    ]


def test_new_tool_arguments_deny_unknown_fields_and_caller_identity_overrides() -> None:
    provider_calls: list[object] = []
    actor = object()
    core = StrictBoundCore()
    dispatcher = CanonicalToolDispatcher(
        core,
        lambda: provider_calls.append(actor) or actor,  # type: ignore[arg-type,return-value]
    )
    spoof_fields = (
        {"actor": {"harness_id": "attacker"}},
        {"caller": {"harness_id": "attacker"}},
        {"harness_id": "attacker"},
        {"credential_id": "attacker"},
        {"identity_path": "/tmp/attacker-identity.json"},
        {"unexpected": True},
    )

    for method, arguments, _operation, _expected_arguments, _result in _NEW_TOOL_CASES:
        for spoof in spoof_fields:
            with pytest.raises(PydanticValidationError):
                dispatcher.call(method, {**arguments, **spoof})  # type: ignore[arg-type]

    assert len(provider_calls) == len(_NEW_TOOL_CASES) * len(spoof_fields)
    assert core.calls == []


def test_new_mcp_schemas_expose_only_semantic_arguments() -> None:
    dispatcher = CanonicalToolDispatcher(StrictBoundCore(), lambda: object())  # type: ignore[arg-type]
    tools = {
        tool.name: tool.parameters["properties"]
        for tool in create_mcp_binding(dispatcher)._tool_manager.list_tools()
    }

    assert set(tools["agentnet_recipient_resolve"]) == {"query"}
    assert set(tools["agentnet_file_send"]) == {
        "collaboration_scope_id",
        "classification",
        "idempotency_key",
        "media_type",
        "recipients",
        "source_path",
    }
    assert set(tools["agentnet_file_status"]) == {
        "collaboration_scope_id",
        "transfer_id",
    }
    assert set(tools["agentnet_file_download"]) == {
        "collaboration_scope_id",
        "artifact_id",
        "destination_path",
        "idempotency_key",
    }
