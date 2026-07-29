from __future__ import annotations

import json
import threading
from argparse import Namespace
from pathlib import Path

import pytest

from agentnet.cli import command_c0_pilot_responder
from agentnet.errors import GateBlocked

from agentnet.supervisor import c0_responder
from agentnet.supervisor.c0_responder import (
    C0PilotResponderConfig,
    check_c0_responder,
    load_c0_responder_config,
    run_c0_responder,
)


def _config() -> C0PilotResponderConfig:
    return C0PilotResponderConfig.model_validate(
        {
            "schema": "agentnet.c0-pilot-responder.config.v1",
            "core_base_url": "https://agentnet.example",
            "audience": "urn:agentnet:corp.example:corporate-api",
            "domain_id": "corp.example",
            "harness_id": "owner-harness",
            "credential_id": "owner-credential",
            "poll_seconds": 0.25,
            "max_consecutive_errors": 2,
        }
    )


def test_dedicated_responder_config_is_strict_owner_only_and_redacted(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = tmp_path / "responder.json"
    path.write_text(json.dumps(_config().model_dump(mode="json", by_alias=True)))
    path.chmod(0o600)
    assert load_c0_responder_config(path) == _config()

    class Client:
        def close(self) -> None:
            pass

    class Core:
        def c0_pilot_readiness(self):
            return {
                "schema": "agentnet.c0-pilot.readiness-result.v1",
                "status": "waiting_plan",
            }

    monkeypatch.setattr(c0_responder, "_client", lambda *_args, **_kwargs: (Client(), Core()))
    assert check_c0_responder(_config(), tmp_path / "credential.pem") == {
        "schema": "agentnet.c0-pilot-responder.check.v1",
        "status": "waiting_plan",
    }

    path.chmod(0o644)
    with pytest.raises(Exception, match="custody"):
        load_c0_responder_config(path)


def test_waiting_owner_responds_once_then_waiting_fresh_keeps_config(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config_path = tmp_path / "responder.json"
    config_path.write_text("{}")
    stop = threading.Event()
    calls: list[str] = []

    class Client:
        def close(self) -> None:
            calls.append("close")

    class Core:
        def c0_pilot_readiness(self):
            calls.append("readiness")
            return {"schema": "agentnet.c0-pilot.readiness-result.v1", "status": "ready"}

        def c0_pilot_status(self):
            calls.append("status")
            if calls.count("status") == 2:
                stop.set()
                return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_fresh"}
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_owner"}

        def c0_pilot_respond(self):
            calls.append("respond")
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_fresh"}

    monkeypatch.setattr(c0_responder, "_client", lambda *_args, **_kwargs: (Client(), Core()))
    result = run_c0_responder(
        _config(),
        tmp_path / "credential.pem",
        config_path,
        stop_event=stop,
    )
    assert result["status"] == "stopped"
    assert config_path.exists()
    assert calls == ["readiness", "status", "respond", "readiness", "status", "close"]


def test_responder_check_reports_sanitized_blocked_envelope(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    config_path = tmp_path / "responder.json"
    credential_path = tmp_path / "credential.pem"
    config_path.write_text(json.dumps(_config().model_dump(mode="json", by_alias=True)))
    config_path.chmod(0o600)
    credential_path.write_text("unused")
    credential_path.chmod(0o600)

    monkeypatch.setattr(
        c0_responder,
        "check_c0_responder",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            GateBlocked("c0_pilot_responder", "private detail")
        ),
    )
    monkeypatch.setattr("agentnet.cli.check_c0_responder", c0_responder.check_c0_responder)

    assert command_c0_pilot_responder(
        Namespace(config=str(config_path), credential=str(credential_path), check=True, run=False)
    ) == 1
    assert json.loads(capsys.readouterr().out) == {
        "schema": "agentnet.c0-pilot-responder.check.v1",
        "status": "blocked",
    }


def test_waiting_fresh_keeps_running_and_retains_config(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config_path = tmp_path / "responder.json"
    config_path.write_text("{}")
    stop = threading.Event()
    calls: list[str] = []

    class Client:
        def close(self) -> None:
            calls.append("close")

    class Core:
        def c0_pilot_readiness(self):
            calls.append("readiness")
            return {
                "schema": "agentnet.c0-pilot.readiness-result.v1",
                "status": "ready",
            }

        def c0_pilot_status(self):
            calls.append("status")
            stop.set()
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_fresh"}

    monkeypatch.setattr(c0_responder, "_client", lambda *_args, **_kwargs: (Client(), Core()))
    result = run_c0_responder(
        _config(),
        tmp_path / "credential.pem",
        config_path,
        stop_event=stop,
    )
    assert result["status"] == "stopped"
    assert config_path.exists()
    assert calls == ["readiness", "status", "close"]


def test_terminal_status_removes_config_and_exits_zero_path(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config_path = tmp_path / "responder.json"
    config_path.write_text("{}")

    class Client:
        def close(self) -> None:
            pass

    class Core:
        def c0_pilot_readiness(self):
            return {
                "schema": "agentnet.c0-pilot.readiness-result.v1",
                "status": "ready",
            }

        def c0_pilot_status(self):
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "expired"}

    monkeypatch.setattr(c0_responder, "_client", lambda *_args, **_kwargs: (Client(), Core()))
    assert run_c0_responder(
        _config(),
        tmp_path / "credential.pem",
        config_path,
    ) == {
        "schema": "agentnet.c0-pilot-responder.exit.v1",
        "status": "expired",
        "stopped": True,
    }
    assert not config_path.exists()
    terminal = json.loads((tmp_path / "terminal.json").read_text(encoding="utf-8"))
    assert terminal == {
        "schema": "agentnet.c0-pilot-responder.terminal.v1",
        "status": "expired",
        "domain_id": "corp.example",
        "harness_id": "owner-harness",
        "credential_id": "owner-credential",
    }
    assert (tmp_path / "terminal.json").stat().st_mode & 0o777 == 0o600


def test_dedicated_module_has_no_worker_queue_model_task_artifact_effect_or_a2a_imports() -> None:
    source = Path(c0_responder.__file__).read_text(encoding="utf-8")
    for forbidden in (
        "supervisor.queue",
        "supervisor.workers",
        "model_egress",
        "task_custody",
        "artifacts",
        "effects",
        "gateways.a2a",
    ):
        assert forbidden not in source
