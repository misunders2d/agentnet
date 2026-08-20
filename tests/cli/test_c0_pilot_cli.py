from __future__ import annotations

import argparse
import json
from pathlib import Path

import pytest

from agentnet import cli
from agentnet.cli.commands import auth


class _Response:
    def __init__(self, status_code: int, body: object) -> None:
        self.status_code = status_code
        self._body = body

    def json(self) -> object:
        return self._body


class _Client:
    def __init__(self, responses: list[_Response]) -> None:
        self.responses = list(responses)
        self.calls: list[str] = []
        self.closed = False

    def _next(self, operation: str) -> _Response:
        self.calls.append(operation)
        return self.responses.pop(0)

    def c0_pilot_start(self) -> _Response:
        return self._next("start")

    def c0_pilot_status(self) -> _Response:
        return self._next("status")

    def c0_pilot_complete(self) -> _Response:
        return self._next("complete")

    def close(self) -> None:
        self.closed = True


def _load(client: _Client):
    return lambda _path: (client, object(), object())


def test_parser_exposes_only_selector_free_c0_commands() -> None:
    parser = cli.build_parser()
    for operation in ("start", "status", "complete"):
        args = parser.parse_args(["c0-pilot", operation])
        assert args.func is cli.command_c0_pilot
        assert args.c0_pilot_command == operation
        assert vars(args).keys() >= {"identity"}
    with pytest.raises(SystemExit):
        parser.parse_args(["c0-pilot", "respond"])
    with pytest.raises(SystemExit):
        parser.parse_args(["c0-pilot", "start", "--peer-harness-id", "forbidden"])


@pytest.mark.parametrize(
    ("operation", "status_code", "stage"),
    [
        ("start", 201, "waiting_owner"),
        ("status", 200, "waiting_fresh"),
        ("complete", 200, "COMPLETED_C0_ROUND_TRIP"),
    ],
)
def test_c0_command_calls_exact_client_method_and_prints_only_stage(
    operation: str,
    status_code: int,
    stage: str,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    body = {"schema": "agentnet.c0-pilot.result.v1", "status": stage}
    client = _Client([_Response(status_code, body)])
    monkeypatch.setattr(auth, "_load_identity_client", _load(client))

    assert cli.command_c0_pilot(
        argparse.Namespace(identity="identity.json", c0_pilot_command=operation)
    ) == 0

    assert json.loads(capsys.readouterr().out) == body
    assert client.calls == [operation]
    assert client.closed is True


def test_c0_command_rejects_non_strict_response_without_printing_private_data(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    client = _Client(
        [
            _Response(
                200,
                {
                    "schema": "agentnet.c0-pilot.result.v1",
                    "status": "waiting_fresh",
                    "private_event_id": "must-not-print",
                },
            )
        ]
    )
    monkeypatch.setattr(auth, "_load_identity_client", _load(client))

    with pytest.raises(SystemExit, match="C0 pilot response is invalid"):
        cli.command_c0_pilot(
            argparse.Namespace(identity="identity.json", c0_pilot_command="status")
        )
    assert "must-not-print" not in capsys.readouterr().out
    assert client.closed is True


def test_c0_command_rejects_wrong_http_status_without_printing_body(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    client = _Client([_Response(403, {"private": "must-not-print"})])
    monkeypatch.setattr(auth, "_load_identity_client", _load(client))

    with pytest.raises(SystemExit, match="rejected with HTTP 403"):
        cli.command_c0_pilot(
            argparse.Namespace(identity=Path("identity.json"), c0_pilot_command="complete")
        )
    assert "must-not-print" not in capsys.readouterr().out
    assert client.closed is True
