from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

import pytest

from agentnet import cli
from agentnet.operations.client_setup import ClientSetupResult, SetupNextAction
from agentnet.operations.endpoint_lifecycle import EndpointActivationState


class _Coordinator:
    def __init__(self, result: ClientSetupResult) -> None:
        self.result = result
        self.calls: list[str] = []
        self.closed = False

    def setup(self) -> ClientSetupResult:
        self.calls.append("setup")
        return self.result

    def status(self) -> ClientSetupResult:
        self.calls.append("status")
        return self.result

    def continue_setup(self) -> ClientSetupResult:
        self.calls.append("continue")
        return self.result

    def close(self) -> None:
        self.closed = True


def _result(
    *,
    state: EndpointActivationState = EndpointActivationState.RESTART_REQUIRED,
    next_action: SetupNextAction = SetupNextAction.RESTART_YOUR_AGENT,
) -> ClientSetupResult:
    return ClientSetupResult(
        endpoint_id="harness-v0144",
        state=state,
        next_action=next_action,
        public_url=None,
        identity_created=False,
    )


def _args() -> argparse.Namespace:
    return argparse.Namespace(
        config="agentnet.json",
        identity=["identity.json"],
        state="continuation.json",
        harness_kind="omp",
        profile_key="default",
        server=None,
        domain=None,
        name=None,
        browser="system",
        private_key=None,
    )


def test_parser_exposes_setup_start_status_and_continue() -> None:
    parser = cli.build_parser()

    start = parser.parse_args(["setup"])
    status = parser.parse_args(["setup", "status"])
    continuation = parser.parse_args(["setup", "continue"])

    assert start.func is cli.command_client_setup
    assert status.func is cli.command_client_setup_status
    assert continuation.func is cli.command_client_setup_continue
    assert start.state.endswith(".agentnet/setup-continuation.json")
    assert start.identity == []


def test_setup_status_prints_strict_public_result(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    coordinator = _Coordinator(
        _result(
            state=EndpointActivationState.WAITING_FOR_APPROVAL,
            next_action=SetupNextAction.WAIT_FOR_APPROVAL,
        )
    )
    monkeypatch.setattr(cli, "_build_client_setup_coordinator", lambda _args: coordinator)

    assert cli.command_client_setup_status(_args()) == 0

    assert json.loads(capsys.readouterr().out) == coordinator.result.model_dump(mode="json")
    assert coordinator.calls == ["status"]
    assert coordinator.closed is True


def test_setup_continue_never_restarts_or_signals_harness(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    coordinator = _Coordinator(_result())
    monkeypatch.setattr(cli, "_build_client_setup_coordinator", lambda _args: coordinator)
    monkeypatch.setattr(
        os,
        "kill",
        lambda *_args: (_ for _ in ()).throw(AssertionError("must not signal a harness")),
    )
    monkeypatch.setattr(
        cli.subprocess,
        "run",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(AssertionError("must not launch process control")),
    )

    assert cli.command_client_setup_continue(_args()) == 0

    output = capsys.readouterr().out
    assert "Restart your agent to enable AgentNet" in output
    assert '"endpoint_id": "harness-v0144"' in output
    assert coordinator.calls == ["continue"]
    assert coordinator.closed is True


def test_setup_does_not_mutate_shell_profile_or_request_sudo(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    shell_profile = tmp_path / ".profile"
    shell_profile.write_text("export USER_SETTING=preserved\n", encoding="utf-8")
    before = shell_profile.read_bytes()
    coordinator = _Coordinator(_result())
    monkeypatch.setattr(cli, "_build_client_setup_coordinator", lambda _args: coordinator)
    monkeypatch.setattr(
        cli.subprocess,
        "run",
        lambda command, *_args, **_kwargs: (_ for _ in ()).throw(
            AssertionError(f"unexpected command: {command!r}")
        ),
    )

    assert cli.command_client_setup(_args()) == 0

    assert shell_profile.read_bytes() == before
    assert "sudo" not in capsys.readouterr().out.lower()
