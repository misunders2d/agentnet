from __future__ import annotations

import argparse
import json
import stat
from collections.abc import Callable
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


_BEGIN_RESULT = {
    "schema": "agentnet.communication-scope.begin-result.v1",
    "status": "approval_pending",
    "approval_url": "https://approval.example/approval",
    "expires_at": 1_800_000_300,
}
_STATUS_RESULT = {
    "schema": "agentnet.communication-scope.status-result.v1",
    "status": "approval_ready",
    "approval_url": "https://approval.example/approval",
    "expires_at": 1_800_000_300,
    "next_action": "complete_automatically",
}
_COMPLETE_RESULT = {
    "schema": "agentnet.communication-scope.complete-result.v1",
    "status": "communication_active",
    "authority_granted": True,
    "communication_usable": True,
    "authority_expires_at": None,
    "artifacts_enabled": False,
    "business_effects_enabled": False,
    "federation_enabled": False,
    "public_a2a_enabled": False,
}


def test_parser_exposes_exact_communication_scope_commands() -> None:
    parser = cli.build_parser()
    for operation, function in (
        ("begin", cli.command_communication_scope_begin),
        ("status", cli.command_communication_scope_status),
        ("complete", cli.command_communication_scope_complete),
    ):
        args = parser.parse_args(["communication-scope", operation])
        assert args.func is function
        assert args.identity == ".agentnet/identity.json"
        assert args.state == ".agentnet/communication-scope-state.json"


def test_communication_scope_requests_use_stable_owner_only_retry_keys(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    responses = [
        _Response(201, _BEGIN_RESULT),
        _Response(201, _BEGIN_RESULT),
        _Response(200, _STATUS_RESULT),
        _Response(201, _COMPLETE_RESULT),
        _Response(201, _COMPLETE_RESULT),
    ]
    client = _Client(responses)
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))
    state_path = tmp_path / "communication-scope.json"
    args = argparse.Namespace(identity="identity.json", state=str(state_path))

    assert cli.command_communication_scope_begin(args) == 0
    assert json.loads(capsys.readouterr().out) == _BEGIN_RESULT
    state = json.loads(state_path.read_text(encoding="utf-8"))
    assert set(state) == {
        "schema",
        "begin_idempotency_key",
        "completion_idempotency_key",
    }
    assert state["schema"] == "agentnet.communication-scope-cli-state.v1"
    assert len(state["begin_idempotency_key"]) >= 16
    assert len(state["completion_idempotency_key"]) >= 16
    assert stat.S_IMODE(state_path.stat().st_mode) == 0o600

    assert cli.command_communication_scope_begin(args) == 0
    assert json.loads(capsys.readouterr().out) == _BEGIN_RESULT
    assert json.loads(state_path.read_text(encoding="utf-8")) == state

    assert cli.command_communication_scope_status(args) == 0
    assert json.loads(capsys.readouterr().out) == _STATUS_RESULT

    assert cli.command_communication_scope_complete(args) == 0
    assert json.loads(capsys.readouterr().out) == _COMPLETE_RESULT
    assert cli.command_communication_scope_complete(args) == 0
    assert json.loads(capsys.readouterr().out) == _COMPLETE_RESULT
    assert json.loads(state_path.read_text(encoding="utf-8")) == state

    begin_body = {
        "schema": "agentnet.communication-scope.begin.v1",
        "begin_idempotency_key": state["begin_idempotency_key"],
    }
    status_body = {
        "schema": "agentnet.communication-scope.status.v1",
        "begin_idempotency_key": state["begin_idempotency_key"],
    }
    complete_body = {
        "schema": "agentnet.communication-scope.complete.v1",
        "begin_idempotency_key": state["begin_idempotency_key"],
        "completion_idempotency_key": state["completion_idempotency_key"],
    }
    assert client.requests == [
        ("POST", "/v1/communication-scope/begin", begin_body),
        ("POST", "/v1/communication-scope/begin", begin_body),
        ("POST", "/v1/communication-scope/status", status_body),
        ("POST", "/v1/communication-scope/complete", complete_body),
        ("POST", "/v1/communication-scope/complete", complete_body),
    ]
    assert client.closed is True


def test_status_accepts_exact_committed_result(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.communication-scope-cli-state.v1",
            "begin_idempotency_key": "communication-begin-key-0001",
            "completion_idempotency_key": "communication-complete-key-0001",
        },
        force=False,
    )
    client = _Client([_Response(200, _COMPLETE_RESULT)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    assert cli.command_communication_scope_status(
        argparse.Namespace(identity="identity.json", state=str(state_path))
    ) == 0
    assert json.loads(capsys.readouterr().out) == _COMPLETE_RESULT


@pytest.mark.parametrize(
    "body",
    [
        {**_STATUS_RESULT, "private_detail": "must-not-print"},
        {**_STATUS_RESULT, "schema": "agentnet.communication-scope.status-result.v2"},
        {**_STATUS_RESULT, "approval_url": "http://approval.example/approval"},
        {**_STATUS_RESULT, "approval_url": "https://approval.example/approval?token=secret"},
    ],
)
def test_status_rejects_malformed_or_non_public_response_without_printing_it(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    body: dict[str, object],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.communication-scope-cli-state.v1",
            "begin_idempotency_key": "communication-begin-key-0001",
            "completion_idempotency_key": "communication-complete-key-0001",
        },
        force=False,
    )
    client = _Client([_Response(200, body)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    with pytest.raises(SystemExit, match="communication scope response is invalid"):
        cli.command_communication_scope_status(
            argparse.Namespace(identity="identity.json", state=str(state_path))
        )
    assert capsys.readouterr().out == ""


def test_complete_rejects_wrong_http_status_before_printing(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    cli._write_owner_json(
        state_path,
        {
            "schema": "agentnet.communication-scope-cli-state.v1",
            "begin_idempotency_key": "communication-begin-key-0001",
            "completion_idempotency_key": "communication-complete-key-0001",
        },
        force=False,
    )
    client = _Client([_Response(200, _COMPLETE_RESULT)])
    monkeypatch.setattr(cli, "_load_identity_client", _load(client))

    with pytest.raises(
        SystemExit,
        match="communication scope request was rejected with HTTP 200",
    ):
        cli.command_communication_scope_complete(
            argparse.Namespace(identity="identity.json", state=str(state_path))
        )
    assert capsys.readouterr().out == ""



def test_manager_run_parser_requires_identity_and_child_command() -> None:
    parser = cli.build_parser()
    args = parser.parse_args(
        [
            "manager-run",
            "--identity",
            "identity.json",
            "--state-dir",
            ".agentnet/manager",
            "--",
            "pi",
            "--print",
            "hello",
        ]
    )
    assert args.func is cli.command_manager_run
    assert args.identity == "identity.json"
    assert args.state_dir == ".agentnet/manager"
    assert args.manager_command == ["pi", "--print", "hello"]

    with pytest.raises(SystemExit):
        parser.parse_args(["manager-run", "--identity", "identity.json"])
    with pytest.raises(SystemExit):
        parser.parse_args(["manager-run", "--", "pi"])


def test_manager_run_invokes_exact_gateway_runner_and_closes_client(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    client = _Client([])
    initially_loaded_actor = object()
    current_actor = object()
    loaded_paths: list[Path] = []
    refreshed_paths: list[Path] = []
    calls: list[tuple[object, object, tuple[str, ...], Path | None, Path]] = []

    def load_identity(path: Path):
        loaded_paths.append(path)
        return client, initially_loaded_actor, object()

    def load_current_identity(path: Path):
        refreshed_paths.append(path)
        return object(), current_actor, object()

    def run_gateway(
        loaded_client: object,
        signing_context: Callable[[], object],
        command: tuple[str, ...],
        *,
        state_dir: Path | None = None,
        pi_extension: Path,
    ) -> int:
        calls.append(
            (loaded_client, signing_context(), command, state_dir, pi_extension)
        )
        return 23

    monkeypatch.setattr(cli, "_load_identity_client", load_identity)
    monkeypatch.setattr(cli, "_load_identity_profile", load_current_identity)
    monkeypatch.setattr(cli, "run_manager_gateway", run_gateway)
    monkeypatch.chdir(tmp_path)
    state_dir = tmp_path / "manager"
    args = argparse.Namespace(
        identity="identity.json",
        state_dir=str(state_dir),
        manager_command=["pi", "--print", "hello"],
    )

    assert cli.command_manager_run(args) == 23
    identity_path = tmp_path / "identity.json"
    assert loaded_paths == [identity_path]
    assert refreshed_paths == [identity_path]
    assert calls == [
        (
            client,
            current_actor,
            ("pi", "--print", "hello"),
            state_dir,
            cli.resolve_packaged_pi_extension(),
        )
    ]
    assert client.closed is True