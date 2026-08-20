from __future__ import annotations

import argparse
import json
import stat
import subprocess
import sys
import time
from collections.abc import Callable
from pathlib import Path

import pytest

from agentnet import cli
from agentnet.host import host_platform
from agentnet.cli.commands import services


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
_TERMINAL_ERROR = {
    "schema": "agentnet.communication-scope.error.v1",
    "code": "communication_scope_terminal",
    "message": "request denied",
    "retryable": False,
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
    begin = parser.parse_args(["communication-scope", "begin"])
    assert begin.replace_terminal_state is False
    assert parser.parse_args(
        ["communication-scope", "begin", "--replace-terminal-state"]
    ).replace_terminal_state is True


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
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))
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


def test_begin_replaces_state_only_after_core_proves_terminal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    original: dict[str, object] = {
        "schema": "agentnet.communication-scope-cli-state.v1",
        "begin_idempotency_key": "communication-begin-key-0001",
        "completion_idempotency_key": "communication-complete-key-0001",
    }
    cli._write_owner_json(state_path, original)
    client = _Client(
        [
            _Response(410, _TERMINAL_ERROR),
            _Response(201, _BEGIN_RESULT),
        ]
    )
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

    assert cli.command_communication_scope_begin(
        argparse.Namespace(
            identity="identity.json",
            state=str(state_path),
            replace_terminal_state=True,
        )
    ) == 0
    assert json.loads(capsys.readouterr().out) == _BEGIN_RESULT

    replaced = json.loads(state_path.read_text(encoding="utf-8"))
    assert replaced["schema"] == original["schema"]
    assert replaced["begin_idempotency_key"] != original["begin_idempotency_key"]
    assert (
        replaced["completion_idempotency_key"]
        != original["completion_idempotency_key"]
    )
    assert stat.S_IMODE(state_path.stat().st_mode) == 0o600
    assert [request[2]["begin_idempotency_key"] for request in client.requests] == [
        original["begin_idempotency_key"],
        replaced["begin_idempotency_key"],
    ]


def test_begin_reuses_nonterminal_state_instead_of_replacing_it(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    original: dict[str, object] = {
        "schema": "agentnet.communication-scope-cli-state.v1",
        "begin_idempotency_key": "communication-begin-key-0001",
        "completion_idempotency_key": "communication-complete-key-0001",
    }
    cli._write_owner_json(state_path, original)
    client = _Client([_Response(201, _BEGIN_RESULT)])
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

    assert cli.command_communication_scope_begin(
        argparse.Namespace(
            identity="identity.json",
            state=str(state_path),
            replace_terminal_state=True,
        )
    ) == 0

    assert json.loads(state_path.read_text(encoding="utf-8")) == original
    assert json.loads(capsys.readouterr().out) == _BEGIN_RESULT
    assert client.requests == [
        (
            "POST",
            "/v1/communication-scope/begin",
            {
                "schema": "agentnet.communication-scope.begin.v1",
                "begin_idempotency_key": original["begin_idempotency_key"],
            },
        )
    ]


def test_begin_refuses_non_core_terminal_response(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state_path = tmp_path / "communication-scope.json"
    original: dict[str, object] = {
        "schema": "agentnet.communication-scope-cli-state.v1",
        "begin_idempotency_key": "communication-begin-key-0001",
        "completion_idempotency_key": "communication-complete-key-0001",
    }
    cli._write_owner_json(state_path, original)
    client = _Client(
        [
            _Response(
                410,
                {**_TERMINAL_ERROR, "proxy_detail": "not Core proof"},
            )
        ]
    )
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

    with pytest.raises(
        SystemExit,
        match="terminal replacement requires exact Core terminal proof",
    ):
        cli.command_communication_scope_begin(
            argparse.Namespace(
                identity="identity.json",
                state=str(state_path),
                replace_terminal_state=True,
            )
        )

    assert json.loads(state_path.read_text(encoding="utf-8")) == original
    assert capsys.readouterr().out == ""


def test_private_state_lock_serializes_two_processes(tmp_path: Path) -> None:
    state_path = tmp_path / "communication-scope.json"
    first_acquired = tmp_path / "first-acquired"
    first_release = tmp_path / "first-release"
    second_attempted = tmp_path / "second-attempted"
    second_acquired = tmp_path / "second-acquired"
    second_release = tmp_path / "second-release"
    second_release.write_text("release", encoding="utf-8")
    child = """
import sys
import time
from pathlib import Path
from agentnet.cli import _private_state_lock

state, attempted, acquired, release = map(Path, sys.argv[1:])
attempted.write_text("attempted", encoding="utf-8")
with _private_state_lock(state):
    acquired.write_text("acquired", encoding="utf-8")
    while not release.exists():
        time.sleep(0.01)
"""

    def launch(attempted: Path, acquired: Path, release: Path) -> subprocess.Popen[str]:
        return subprocess.Popen(
            [
                sys.executable,
                "-c",
                child,
                str(state_path),
                str(attempted),
                str(acquired),
                str(release),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

    def wait_for(path: Path, process: subprocess.Popen[str]) -> None:
        deadline = time.monotonic() + 5
        while not path.exists():
            if process.poll() is not None:
                stdout, stderr = process.communicate()
                pytest.fail(
                    f"lock child exited before {path.name}: {stdout=} {stderr=}"
                )
            if time.monotonic() >= deadline:
                pytest.fail(f"lock child did not create {path.name}")
            time.sleep(0.01)

    first_attempted = tmp_path / "first-attempted"
    first = launch(first_attempted, first_acquired, first_release)
    second: subprocess.Popen[str] | None = None
    try:
        wait_for(first_acquired, first)
        second = launch(second_attempted, second_acquired, second_release)
        wait_for(second_attempted, second)
        time.sleep(0.2)
        assert not second_acquired.exists()

        first_release.write_text("release", encoding="utf-8")
        assert first.wait(timeout=5) == 0
        assert second.wait(timeout=5) == 0
        assert second_acquired.read_text(encoding="utf-8") == "acquired"
    finally:
        first_release.write_text("release", encoding="utf-8")
        for process in (first, second):
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)


@pytest.mark.skipif(
    host_platform() == "windows",
    reason="POSIX peer-unlink boundary",
)
def test_private_state_lock_rejects_peer_writable_parent(tmp_path: Path) -> None:
    parent = tmp_path / "peer-writable"
    parent.mkdir()
    parent.chmod(0o777)
    state_path = parent / "communication-scope.json"
    try:
        with pytest.raises(
            SystemExit,
            match="state lock directory is unsafe",
        ):
            with cli._private_state_lock(state_path):
                pytest.fail("peer-writable parent must not admit a lock")
        assert not (parent / ".communication-scope.json.lock").exists()
    finally:
        parent.chmod(0o700)


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
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

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
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

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
    monkeypatch.setattr(cli.helpers, "_load_identity_client", _load(client))

    with pytest.raises(
        SystemExit,
        match="communication scope request was rejected with HTTP 200",
    ):
        cli.command_communication_scope_complete(
            argparse.Namespace(identity="identity.json", state=str(state_path))
        )
    assert capsys.readouterr().out == ""



@pytest.mark.parametrize("executable", ["pi", "omp"])
def test_manager_run_parser_requires_identity_and_child_command(
    executable: str,
) -> None:
    parser = cli.build_parser()
    args = parser.parse_args(
        [
            "manager-run",
            "--identity",
            "identity.json",
            "--state-dir",
            ".agentnet/manager",
            "--",
            executable,
            "--print",
            "hello",
        ]
    )
    assert args.func is cli.command_manager_run
    assert args.identity == "identity.json"
    assert args.state_dir == ".agentnet/manager"
    assert args.manager_command == [executable, "--print", "hello"]

    with pytest.raises(SystemExit):
        parser.parse_args(["manager-run", "--identity", "identity.json"])
    with pytest.raises(SystemExit):
        parser.parse_args(["manager-run", "--", "pi"])


@pytest.mark.parametrize("executable", ["pi", "omp"])
def test_manager_run_invokes_exact_gateway_runner_and_closes_client(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    executable: str,
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
        manager_extension: Path,
    ) -> int:
        calls.append(
            (loaded_client, signing_context(), command, state_dir, manager_extension)
        )
        return 23

    monkeypatch.setattr(cli.helpers, "_load_identity_client", load_identity)
    monkeypatch.setattr(cli.helpers, "_load_identity_profile", load_current_identity)
    monkeypatch.setattr("agentnet.cli.commands.services.run_manager_gateway", run_gateway)
    monkeypatch.chdir(tmp_path)
    state_dir = tmp_path / "manager"
    args = argparse.Namespace(
        identity="identity.json",
        state_dir=str(state_dir),
        manager_command=[executable, "--print", "hello"],
    )

    assert cli.command_manager_run(args) == 23
    identity_path = tmp_path / "identity.json"
    assert loaded_paths == [identity_path]
    assert refreshed_paths == [identity_path]
    assert calls == [
        (
            client,
            current_actor,
            (executable, "--print", "hello"),
            state_dir,
            services.resolve_packaged_manager_extension(),
        )
    ]
    assert client.closed is True