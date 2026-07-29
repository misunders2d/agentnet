from __future__ import annotations

import argparse
import json
import stat
from pathlib import Path

import pytest

from agentnet import cli


class _Response:
    def __init__(self, status_code: int, body: dict[str, object]) -> None:
        self.status_code = status_code
        self._body = body

    def json(self) -> dict[str, object]:
        return self._body


class _Client:
    def __init__(self, responses: list[_Response]) -> None:
        self.responses = list(responses)
        self.requests: list[tuple[str, str, dict[str, object]]] = []
        self.closed = False

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, object],
    ) -> _Response:
        self.requests.append((method, path, json_body))
        return self.responses.pop(0)

    def close(self) -> None:
        self.closed = True


def _load(client: _Client):
    return lambda _path: (client, object(), object())


def test_parser_exposes_only_bounded_bootstrap_plan_commands() -> None:
    parser = cli.build_parser()
    for operation, function in (
        ("begin", cli.command_bootstrap_plan_begin),
        ("status", cli.command_bootstrap_plan_status),
        ("complete", cli.command_bootstrap_plan_complete),
    ):
        args = parser.parse_args(["bootstrap-plan", operation])
        assert args.func is function
    with pytest.raises(SystemExit):
        parser.parse_args(["founder", "begin"])


def test_begin_persists_retry_keys_before_request_and_reuses_them(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    body = {
        "schema": "agentnet.bootstrap-plan.begin-result.v1",
        "status": "approval_pending",
        "approval_url": "https://approval.example/approval",
        "expires_at": 1_800_000_300,
    }
    client = _Client([_Response(201, body), _Response(201, body)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))
    state_path = tmp_path / "bootstrap-plan-state.json"
    args = argparse.Namespace(identity="identity.json", state=str(state_path))

    assert cli.command_bootstrap_plan_begin(args) == 0
    first_state = json.loads(state_path.read_text(encoding="utf-8"))
    assert set(first_state) == {
        "schema",
        "begin_idempotency_key",
        "completion_idempotency_key",
    }
    assert first_state["schema"] == "agentnet.bootstrap-plan-cli-state.v1"
    assert len(first_state["begin_idempotency_key"]) >= 16
    assert len(first_state["completion_idempotency_key"]) >= 16
    assert stat.S_IMODE(state_path.stat().st_mode) == 0o600
    assert json.loads(capsys.readouterr().out) == body

    assert cli.command_bootstrap_plan_begin(args) == 0
    assert json.loads(state_path.read_text(encoding="utf-8")) == first_state
    assert client.requests == [
        (
            "POST",
            "/v1/bootstrap-plan/begin",
            {
                "schema": "agentnet.bootstrap-plan.begin.v1",
                "begin_idempotency_key": first_state["begin_idempotency_key"],
            },
        ),
        (
            "POST",
            "/v1/bootstrap-plan/begin",
            {
                "schema": "agentnet.bootstrap-plan.begin.v1",
                "begin_idempotency_key": first_state["begin_idempotency_key"],
            },
        ),
    ]
    assert client.closed is True


def test_status_uses_saved_begin_key_and_prints_only_strict_public_result(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "state.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.bootstrap-plan-cli-state.v1",
            "begin_idempotency_key": "bootstrap-begin-key-0001",
            "completion_idempotency_key": "bootstrap-complete-key-0001",
        },
        force=False,
    )
    body = {
        "schema": "agentnet.bootstrap-plan.status-result.v1",
        "status": "approval_ready",
        "approval_url": "https://approval.example/approval",
        "expires_at": 1_800_000_300,
        "next_action": "complete_automatically",
    }
    client = _Client([_Response(200, body)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    assert cli.command_bootstrap_plan_status(
        argparse.Namespace(identity="identity.json", state=str(state_path))
    ) == 0
    assert json.loads(capsys.readouterr().out) == body
    assert client.requests[0][2] == {
        "schema": "agentnet.bootstrap-plan.status.v1",
        "begin_idempotency_key": "bootstrap-begin-key-0001",
    }


def test_status_accepts_exact_committed_public_result(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "state.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.bootstrap-plan-cli-state.v1",
            "begin_idempotency_key": "bootstrap-begin-key-0001",
            "completion_idempotency_key": "bootstrap-complete-key-0001",
        },
        force=False,
    )
    body = {
        "schema": "agentnet.bootstrap-plan.complete-result.v1",
        "status": "prepared_unusable",
        "authority_granted": False,
        "communication_usable": False,
    }
    client = _Client([_Response(200, body)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    assert cli.command_bootstrap_plan_status(
        argparse.Namespace(identity="identity.json", state=str(state_path))
    ) == 0
    assert json.loads(capsys.readouterr().out) == body


def test_complete_uses_exact_private_state_without_tty_or_prompt(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "state.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.bootstrap-plan-cli-state.v1",
            "begin_idempotency_key": "bootstrap-begin-key-0001",
            "completion_idempotency_key": "bootstrap-complete-key-0001",
        },
        force=False,
    )
    body = {
        "schema": "agentnet.bootstrap-plan.complete-result.v1",
        "status": "prepared_unusable",
        "authority_granted": False,
        "communication_usable": False,
    }
    monkeypatch.setattr(
        cli,
        "_require_private_terminal_or_exit",
        lambda: (_ for _ in ()).throw(AssertionError("TTY must not be required")),
    )
    monkeypatch.setattr(
        cli.getpass,
        "getpass",
        lambda _prompt: (_ for _ in ()).throw(AssertionError("prompt must not run")),
    )
    client = _Client([_Response(201, body)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    assert cli.command_bootstrap_plan_complete(
        argparse.Namespace(identity="identity.json", state=str(state_path))
    ) == 0
    output = capsys.readouterr().out
    assert json.loads(output) == body
    assert "claim_code" not in output
    assert "claim_code" not in state_path.read_text(encoding="utf-8")
    assert client.requests[0][2] == {
        "schema": "agentnet.bootstrap-plan.complete.v2",
        "begin_idempotency_key": "bootstrap-begin-key-0001",
        "completion_idempotency_key": "bootstrap-complete-key-0001",
    }


def test_complete_runs_without_private_terminal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state_path = tmp_path / "state.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.bootstrap-plan-cli-state.v1",
            "begin_idempotency_key": "bootstrap-begin-key-0001",
            "completion_idempotency_key": "bootstrap-complete-key-0001",
        },
        force=False,
    )
    monkeypatch.setattr(
        cli,
        "_require_private_terminal_or_exit",
        lambda: (_ for _ in ()).throw(AssertionError("TTY must not be required")),
    )
    monkeypatch.setattr(
        cli.getpass,
        "getpass",
        lambda _prompt: (_ for _ in ()).throw(AssertionError("prompt must not run")),
    )
    client = _Client(
        [
            _Response(
                201,
                {
                    "schema": "agentnet.bootstrap-plan.complete-result.v1",
                    "status": "prepared_unusable",
                    "authority_granted": False,
                    "communication_usable": False,
                },
            )
        ]
    )
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    assert cli.command_bootstrap_plan_complete(
        argparse.Namespace(identity="identity.json", state=str(state_path))
    ) == 0


def test_cli_rejects_non_strict_server_result_without_printing_it(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "state.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.bootstrap-plan-cli-state.v1",
            "begin_idempotency_key": "bootstrap-begin-key-0001",
            "completion_idempotency_key": "bootstrap-complete-key-0001",
        },
        force=False,
    )
    client = _Client(
        [
            _Response(
                200,
                {
                    "schema": "agentnet.bootstrap-plan.status-result.v1",
                    "status": "approval_ready",
                    "approval_url": "https://approval.example/approval",
                    "expires_at": 1_800_000_300,
                    "next_action": "complete_automatically",
                    "private_detail": "must-not-print",
                },
            )
        ]
    )
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    with pytest.raises(SystemExit, match="bootstrap plan response is invalid"):
        cli.command_bootstrap_plan_status(
            argparse.Namespace(identity="identity.json", state=str(state_path))
        )
    assert "must-not-print" not in capsys.readouterr().out
