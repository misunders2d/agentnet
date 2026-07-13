from __future__ import annotations

from typing import Any

import httpx
import pytest

from agentnet.errors import AuthorizationError, ValidationError
from agentnet.supervisor.client import AgentNetSupervisorCoreClient


class StubSignedClient:
    def __init__(self, value: dict[str, Any], *, status_code: int = 200) -> None:
        self.value = value
        self.status_code = status_code
        self.calls: list[tuple[str, str, float | None]] = []

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        assert json_body is None
        self.calls.append((method, path, timeout_seconds))
        return httpx.Response(
            self.status_code,
            json=self.value,
            request=httpx.Request(method, f"https://agent.example{path}"),
        )


class StubObligationClient:
    def __init__(self, responses: list[dict[str, Any]]) -> None:
        self.responses = responses
        self.calls: list[tuple[str, str, dict[str, Any] | None]] = []

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        assert timeout_seconds is None
        self.calls.append((method, path, json_body))
        return httpx.Response(
            200,
            json=self.responses.pop(0),
            request=httpx.Request(method, f"https://agent.example{path}"),
        )


def test_signed_watch_is_strict_content_free_and_only_a_reconciliation_hint() -> None:
    signed = StubSignedClient(
        {
            "schema": "agentnet.mailbox-wake.v1",
            "kind": "wake",
            "cursor_hint": 8,
        }
    )
    client = AgentNetSupervisorCoreClient(signed)  # type: ignore[arg-type]

    assert client.watch(after_cursor=7, wait_seconds=1.25) is True
    assert signed.calls == [
        ("GET", "/v1/mailbox/watch?after=7&wait_ms=1250", 3.25)
    ]


@pytest.mark.parametrize(
    "value",
    [
        {
            "schema": "agentnet.mailbox-wake.v1",
            "kind": "wake",
            "cursor_hint": 8,
            "payload": {"forbidden": True},
        },
        {
            "schema": "agentnet.mailbox-wake.v1",
            "kind": "wake",
            "cursor_hint": 7,
        },
        {
            "schema": "agentnet.mailbox-wake.v1",
            "kind": "idle",
            "cursor_hint": 8,
        },
        {
            "schema": "agentnet.mailbox-wake.v0",
            "kind": "wake",
            "cursor_hint": 8,
        },
    ],
)
def test_watch_rejects_content_authority_and_cursor_schema_drift(
    value: dict[str, Any],
) -> None:
    client = AgentNetSupervisorCoreClient(StubSignedClient(value))  # type: ignore[arg-type]

    with pytest.raises(ValidationError, match="wake|idle"):
        client.watch(after_cursor=7, wait_seconds=1)


def test_watch_fails_closed_on_authentication_or_unbounded_wait() -> None:
    unauthorized = AgentNetSupervisorCoreClient(
        StubSignedClient({}, status_code=401)  # type: ignore[arg-type]
    )
    with pytest.raises(AuthorizationError, match="not authorized"):
        unauthorized.watch(after_cursor=0, wait_seconds=1)

    valid = AgentNetSupervisorCoreClient(  # type: ignore[arg-type]
        StubSignedClient(
            {
                "schema": "agentnet.mailbox-wake.v1",
                "kind": "idle",
                "cursor_hint": 0,
            }
        )
    )
    with pytest.raises(ValidationError, match="bounds"):
        valid.watch(after_cursor=0, wait_seconds=31)


def test_signed_supervisor_obligation_reconciliation_and_counters_are_strict() -> None:
    signed = StubObligationClient(
        [
            {"recipient_committed": ["obligation-1"], "expired": []},
            {
                "unread_information": 1,
                "action_required": 2,
                "awaiting_peer": 3,
                "awaiting_human": 4,
                "overdue": 5,
                "failed": 6,
            },
        ]
    )
    client = AgentNetSupervisorCoreClient(signed)  # type: ignore[arg-type]

    assert client.reconcile_obligations(limit=25) == {
        "recipient_committed": ["obligation-1"],
        "expired": [],
    }
    assert client.obligation_inbox()["action_required"] == 2
    assert signed.calls == [
        ("POST", "/v1/response-obligations/reconcile", {"limit": 25}),
        ("GET", "/v1/response-obligations/inbox", None),
    ]

    malformed = AgentNetSupervisorCoreClient(
        StubObligationClient([{"action_required": True}])  # type: ignore[arg-type]
    )
    with pytest.raises(ValidationError, match="inbox response schema"):
        malformed.obligation_inbox()
