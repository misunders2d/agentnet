from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from types import SimpleNamespace
from typing import Any

import httpx
import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.remote_manager import RemoteManagerDispatcher
from agentnet.bindings.tools import CanonicalToolDispatcher
from agentnet.discovery.recipient_resolver import ResolvedEndpoint
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.http_api import ExactRecipientScopeBody, MessageBody
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification


_PROVENANCE = {
    "allowed_sinks": {
        "schema_version": "1.0",
        "sinks": ["server-harness"],
    },
    "authority_effect": "none",
    "classification": "C1",
    "content_digest": "d" * 64,
    "domain_id": "owner.example",
    "object_id": "event-1",
    "object_type": "event",
    "policy_revision": 1,
    "provenance_digest": "e" * 64,
    "review_state": "unreviewed",
    "scan_state": "not_required",
    "schema_version": "1.0",
    "tainted": False,
    "version": 1,
}
_ACCEPTED = {
    "audit_hash": "a" * 64,
    "duplicate": False,
    "envelope_digest": "c" * 64,
    "event_id": "event-1",
    "fact": "accepted_local",
    "provenance": _PROVENANCE,
    "receipt_id": "receipt-1",
}
_ENDPOINT = ResolvedEndpoint(
    harness_id="server-harness",
    display_name="The enrolled server",
    harness_kind="server",
    availability="online",
    scope_id="scope-1",
)
_EXPECTED_QUERY_RESULT = {
    **_ACCEPTED,
    "recipient_display_metadata": [_ENDPOINT.model_dump(mode="json")],
    "recipient_harness_ids": ["server-harness"],
}


def _actor() -> VerifiedActor:
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="owner.example",
        principal_id="principal-owner-0001",
        harness_id="owner-harness",
        credential_id="owner-credential",
        credential_epoch=7,
        binding_assurance="os_bound",
    )


class RecordingResolver:
    def __init__(self, result: tuple[ResolvedEndpoint, ...] = (_ENDPOINT,)) -> None:
        self.result = result
        self.calls: list[dict[str, Any]] = []
        self.failure: Exception | None = None

    def resolve(self, *, actor: VerifiedActor, query: str) -> tuple[ResolvedEndpoint, ...]:
        self.calls.append({"actor": actor, "query": query})
        if self.failure is not None:
            raise self.failure
        return self.result


class RecordingScopes:
    def __init__(self, *, inferred_scope_id: str = "scope-1") -> None:
        self.inferred_scope_id = inferred_scope_id
        self.calls: list[dict[str, Any]] = []
        self.failure: Exception | None = None

    def require(self, **arguments: Any) -> Any:
        self.calls.append(arguments)
        if self.failure is not None:
            raise self.failure
        return SimpleNamespace(scope_id=arguments["scope_id"] or self.inferred_scope_id)


class RecordingCore:
    def __init__(self) -> None:
        self.recipient_resolver = RecordingResolver()
        self.collaboration_scopes = RecordingScopes()
        self.calls: list[dict[str, Any]] = []
        self.accepted = dict(_ACCEPTED)

    def send_message(self, **arguments: Any) -> dict[str, Any]:
        self.calls.append(arguments)
        return dict(self.accepted)


@dataclass
class SequencedClient:
    responses: list[Any]
    domain_id: str = "owner.example"
    harness_id: str = "owner-harness"
    credential_id: str = "owner-credential"

    def __post_init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        self.calls.append(
            {
                "json_body": json_body,
                "method": method,
                "path": path,
                "timeout_seconds": timeout_seconds,
            }
        )
        return httpx.Response(200, json=self.responses.pop(0))


def _dispatcher(core: RecordingCore, actor: VerifiedActor | None = None) -> CanonicalToolDispatcher:
    current = actor or _actor()
    return CanonicalToolDispatcher(core, lambda: current)  # type: ignore[arg-type]


def _send_arguments(**recipient_form: Any) -> dict[str, Any]:
    return {
        **recipient_form,
        "payload": {"text": "hello"},
        "idempotency_key": "send-display-0001",
        "classification": "C1",
    }


def _mcp_value(value: Any) -> Any:
    if isinstance(value, dict):
        return value
    if (
        isinstance(value, tuple)
        and len(value) == 2
        and isinstance(value[0], list)
        and isinstance(value[1], dict)
    ):
        return value[1].get("result", value[1])
    structured = getattr(value, "structuredContent", None)
    if structured is not None:
        return structured.get("result", structured)
    for content in getattr(value, "content", ()):
        text = getattr(content, "text", None)
        if isinstance(text, str):
            import json

            return json.loads(text)
    raise AssertionError("MCP result did not contain canonical JSON")


def test_agentnet_send_resolves_friendly_exact_recipient_before_scope_and_send() -> None:
    actor = _actor()
    core = RecordingCore()

    result = _dispatcher(core, actor).call(
        "agentnet.send",
        _send_arguments(recipient_query="the enrolled server"),
    )

    assert result == _EXPECTED_QUERY_RESULT
    assert core.recipient_resolver.calls == [
        {"actor": actor, "query": "the enrolled server"}
    ]
    assert core.collaboration_scopes.calls == [
        {
            "actor": actor,
            "scope_id": "scope-1",
            "action": "message.send",
            "resource": "conversation:direct",
            "target_harness_ids": ("server-harness",),
            "classification": Classification.C1_INTERNAL,
        }
    ]
    assert core.calls == [
        {
            "actor": actor,
            "collaboration_scope_id": "scope-1",
            "recipients": ("server-harness",),
            "payload": {"text": "hello"},
            "idempotency_key": "send-display-0001",
            "classification": Classification.C1_INTERNAL,
        }
    ]


def test_agentnet_send_ambiguity_fails_without_enumerating_candidates() -> None:
    core = RecordingCore()
    core.recipient_resolver.failure = ConflictError("recipient could not be resolved")
    with pytest.raises(ConflictError) as failure:
        _dispatcher(core).call(
            "agentnet.send",
            _send_arguments(recipient_query="shared server"),
        )

    assert str(failure.value) == "recipient could not be resolved"
    assert "server-harness" not in str(failure.value)
    assert core.collaboration_scopes.calls == []
    assert core.calls == []


def test_agentnet_send_exact_ids_freezes_one_unambiguous_current_scope() -> None:
    actor = _actor()
    core = RecordingCore()

    result = _dispatcher(core, actor).call(
        "agentnet.send",
        _send_arguments(recipients=["server-harness"]),
    )

    assert result == {
        **_ACCEPTED,
        "recipient_display_metadata": [],
        "recipient_harness_ids": ["server-harness"],
    }
    assert core.recipient_resolver.calls == []
    assert core.collaboration_scopes.calls == [
        {
            "actor": actor,
            "scope_id": None,
            "action": "message.send",
            "resource": "conversation:direct",
            "target_harness_ids": ("server-harness",),
            "classification": "C1",
        }
    ]
    assert core.calls[0]["collaboration_scope_id"] == "scope-1"
    assert core.calls[0]["recipients"] == ("server-harness",)


@pytest.mark.parametrize(
    "arguments",
    (
        _send_arguments(),
        _send_arguments(
            recipient_query="the enrolled server",
            recipients=["server-harness"],
        ),
    ),
)
def test_agentnet_send_requires_exactly_one_recipient_form(arguments: dict[str, Any]) -> None:
    core = RecordingCore()

    with pytest.raises(Exception, match="exactly one recipient form"):
        _dispatcher(core).call("agentnet.send", arguments)

    assert core.recipient_resolver.calls == []
    assert core.collaboration_scopes.calls == []
    assert core.calls == []


def test_http_message_submission_requires_a_frozen_collaboration_scope() -> None:
    exact = _send_arguments(recipients=["server-harness"])

    with pytest.raises(PydanticValidationError):
        MessageBody.model_validate(exact)

    parsed = MessageBody.model_validate(
        {**exact, "collaboration_scope_id": "scope-1"}
    )
    assert parsed.collaboration_scope_id == "scope-1"
    assert parsed.recipients == ("server-harness",)


def test_http_exact_recipient_scope_request_is_bounded_and_unique() -> None:
    parsed = ExactRecipientScopeBody.model_validate(
        {"classification": "C1", "recipients": ["server-harness"]}
    )
    assert parsed.recipients == ("server-harness",)

    with pytest.raises(PydanticValidationError):
        ExactRecipientScopeBody.model_validate(
            {
                "classification": "C1",
                "recipients": ["server-harness", "server-harness"],
            }
        )




def test_agentnet_send_scope_mismatch_fails_before_message_submission() -> None:
    core = RecordingCore()
    core.collaboration_scopes.failure = AuthorizationError(
        "collaboration scope does not authorize the operation"
    )

    with pytest.raises(AuthorizationError, match="does not authorize"):
        _dispatcher(core).call(
            "agentnet.send",
            _send_arguments(recipient_query="the enrolled server"),
        )

    assert core.calls == []


@pytest.mark.parametrize(
    ("sensitive_field", "sensitive_value"),
    (
        ("payload", {"private": "must not escape"}),
        ("hidden_candidates", ["hidden-harness"]),
        ("caller_identity", {"harness_id": "attacker"}),
        ("recipient_harness_ids", ["attacker-harness"]),
        (
            "recipient_display_metadata",
            [{"display_name": "Hidden candidate", "harness_id": "attacker-harness"}],
        ),
    ),
)
def test_agentnet_send_rejects_non_exact_or_sensitive_success_fields(
    sensitive_field: str,
    sensitive_value: Any,
) -> None:
    core = RecordingCore()
    core.accepted[sensitive_field] = sensitive_value

    with pytest.raises(ValidationError, match="result schema") as failure:
        _dispatcher(core).call(
            "agentnet.send",
            _send_arguments(recipient_query="the enrolled server"),
        )

    assert str(sensitive_value) not in str(failure.value)


@pytest.mark.anyio
async def test_direct_mcp_and_remote_query_send_have_identical_semantics() -> None:
    direct_core = RecordingCore()
    direct = _dispatcher(direct_core).call(
        "agentnet.send",
        _send_arguments(recipient_query="the enrolled server"),
    )

    mcp_core = RecordingCore()
    mcp = create_mcp_binding(_dispatcher(mcp_core))
    mcp_result = _mcp_value(
        await mcp.call_tool(
            "agentnet_send",
            _send_arguments(recipient_query="the enrolled server"),
        )
    )

    client = SequencedClient(
        responses=[
            {"items": [_ENDPOINT.model_dump(mode="json")]},
            _ACCEPTED,
        ]
    )
    remote = RemoteManagerDispatcher(client, _actor).dispatch(
        "agentnet.send",
        _send_arguments(recipient_query="the enrolled server"),
    )

    assert direct == mcp_result == remote == _EXPECTED_QUERY_RESULT
    assert client.calls == [
        {
            "json_body": {"query": "the enrolled server"},
            "method": "POST",
            "path": "/v1/recipients/resolve",
            "timeout_seconds": None,
        },
        {
            "json_body": {
                "classification": "C1",
                "collaboration_scope_id": "scope-1",
                "idempotency_key": "send-display-0001",
                "payload": {"text": "hello"},
                "recipients": ["server-harness"],
            },
            "method": "POST",
            "path": "/v1/messages",
            "timeout_seconds": None,
        },
    ]


def test_remote_exact_ids_infer_one_scope_then_forward_frozen_scope_id() -> None:
    client = SequencedClient(
        responses=[
            {
                "recipient_harness_ids": ["server-harness"],
                "scope_id": "scope-1",
            },
            _ACCEPTED,
        ]
    )

    result = RemoteManagerDispatcher(client, _actor).dispatch(
        "agentnet.send",
        _send_arguments(recipients=["server-harness"]),
    )

    assert result == {
        **_ACCEPTED,
        "recipient_display_metadata": [],
        "recipient_harness_ids": ["server-harness"],
    }
    assert client.calls[0] == {
        "json_body": {
            "classification": "C1",
            "recipients": ["server-harness"],
        },
        "method": "POST",
        "path": "/v1/recipients/exact-scope",
        "timeout_seconds": None,
    }
    assert client.calls[1]["json_body"]["collaboration_scope_id"] == "scope-1"
