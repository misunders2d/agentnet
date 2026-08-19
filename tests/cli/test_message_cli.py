from __future__ import annotations

import argparse
from pathlib import Path

import pytest

from agentnet import cli


class _Response:
    def __init__(self, status_code: int, value: dict[str, object]) -> None:
        self.status_code = status_code
        self._value = value

    def json(self) -> dict[str, object]:
        return self._value


class _Client:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, dict[str, object] | None]] = []
        self.closed = False

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, object] | None = None,
    ) -> _Response:
        self.calls.append((method, path, json_body))
        return _Response(202 if path == "/v1/messages" else 200, {"ok": True})

    def acknowledge_mailbox(
        self,
        *,
        collaboration_scope_id: str,
        event_id: str,
        envelope_digest: str,
    ) -> _Response:
        self.calls.append(
            (
                "POST",
                f"/v1/mailbox/{event_id}/acknowledge",
                {
                    "collaboration_scope_id": collaboration_scope_id,
                    "envelope_digest": envelope_digest,
                },
            )
        )
        return _Response(200, {"ok": True})

    def close(self) -> None:
        self.closed = True


def _load(client: _Client):
    return lambda _path: (client, object(), object())


def test_message_cli_binds_every_operation_to_exact_collaboration_scope(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    payload = tmp_path / "payload.json"
    payload.write_text('{"text":"hello"}', encoding="utf-8")
    client = _Client()
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))
    scope_id = "collaboration-scope-0001"

    assert cli.command_message_send(
        argparse.Namespace(
            identity="identity.json",
            collaboration_scope_id=scope_id,
            recipient=["recipient-harness"],
            payload=str(payload),
            idempotency_key="message-idempotency-key-0001",
            classification="C1",
        )
    ) == 0
    assert cli.command_message_inbox(
        argparse.Namespace(
            identity="identity.json",
            collaboration_scope_id=scope_id,
            after=0,
            limit=100,
        )
    ) == 0
    assert cli.command_message_acknowledge(
        argparse.Namespace(
            identity="identity.json",
            collaboration_scope_id=scope_id,
            event_id="event-1",
            envelope_digest="a" * 64,
        )
    ) == 0

    assert client.calls == [
        (
            "POST",
            "/v1/messages",
            {
                "collaboration_scope_id": scope_id,
                "recipients": ["recipient-harness"],
                "payload": {"text": "hello"},
                "idempotency_key": "message-idempotency-key-0001",
                "classification": "C1",
            },
        ),
        (
            "GET",
            "/v1/mailbox?collaboration_scope_id=collaboration-scope-0001&after=0&limit=100",
            None,
        ),
        (
            "POST",
            "/v1/mailbox/event-1/acknowledge",
            {
                "collaboration_scope_id": scope_id,
                "envelope_digest": "a" * 64,
            },
        ),
    ]
    assert client.closed is True
    assert capsys.readouterr().out.count('"ok": true') == 3


def test_message_parser_requires_collaboration_scope_for_every_operation() -> None:
    parser = cli.build_parser()
    for arguments in (
        ["message", "send", "--recipient", "peer", "--payload", "payload.json"],
        ["message", "inbox"],
        ["message", "acknowledge", "event", "--envelope-digest", "a" * 64],
    ):
        with pytest.raises(SystemExit):
            parser.parse_args(arguments)
