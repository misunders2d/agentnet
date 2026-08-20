from __future__ import annotations

import json
import os
import stat
import threading
from argparse import Namespace
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet.cli import command_c0_pilot_responder
from agentnet.errors import GateBlocked, ValidationError
from agentnet.security.signatures import P256KeyPair

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


@pytest.mark.parametrize("mode", [0o400, 0o440])
def test_systemd_load_credential_custody_is_accepted(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    mode: int,
) -> None:
    credential_dir = tmp_path / "credentials"
    credential_dir.mkdir()
    credential_path = credential_dir / "signing-key.pem"
    payload = P256KeyPair.generate().private_pem
    credential_path.write_bytes(payload)

    actual_fstat = os.fstat

    def systemd_fstat(descriptor: int) -> SimpleNamespace:
        info = actual_fstat(descriptor)
        return SimpleNamespace(
            st_mode=stat.S_IFREG | mode,
            st_nlink=1,
            st_uid=0,
            st_gid=0,
            st_size=info.st_size,
        )

    monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(credential_dir))
    monkeypatch.setattr(c0_responder.os, "geteuid", lambda: 995)
    monkeypatch.setattr(c0_responder.os, "fstat", systemd_fstat)

    assert (
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )
        == payload
    )


def test_c0_client_loads_systemd_pem_as_bytes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    credential_dir = tmp_path / "credentials"
    credential_dir.mkdir()
    credential_path = credential_dir / "signing-key.pem"
    credential_path.write_bytes(P256KeyPair.generate().private_pem)
    actual_fstat = os.fstat

    def systemd_fstat(descriptor: int) -> SimpleNamespace:
        info = actual_fstat(descriptor)
        return SimpleNamespace(
            st_mode=stat.S_IFREG | 0o440,
            st_nlink=1,
            st_uid=0,
            st_gid=0,
            st_size=info.st_size,
        )

    monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(credential_dir))
    monkeypatch.setattr(c0_responder.os, "geteuid", lambda: 995)
    monkeypatch.setattr(c0_responder.os, "fstat", systemd_fstat)

    client, _core = c0_responder._client(
        _config(),
        credential_path,
        transport=None,
    )
    client.close()


@pytest.mark.parametrize(
    "overrides",
    [
        {"st_mode": stat.S_IFDIR | 0o440},
        {"st_mode": stat.S_IFREG | 0o600},
        {"st_mode": stat.S_IFREG | 0o640},
        {"st_mode": stat.S_IFREG | 0o450},
        {"st_mode": stat.S_IFREG | 0o444},
        {"st_nlink": 2},
        {"st_uid": 1},
        {"st_gid": 1},
        {"st_size": 65_537},
    ],
)
def test_systemd_load_credential_rejects_invalid_metadata(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    overrides: dict[str, int],
) -> None:
    credential_dir = tmp_path / "credentials"
    credential_dir.mkdir()
    credential_path = credential_dir / "signing-key.pem"
    credential_path.write_bytes(P256KeyPair.generate().private_pem)
    actual_fstat = os.fstat

    def invalid_fstat(descriptor: int) -> SimpleNamespace:
        info = actual_fstat(descriptor)
        metadata = {
            "st_mode": stat.S_IFREG | 0o440,
            "st_nlink": 1,
            "st_uid": 0,
            "st_gid": 0,
            "st_size": info.st_size,
        }
        return SimpleNamespace(**(metadata | overrides))

    monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(credential_dir))
    monkeypatch.setattr(c0_responder.os, "geteuid", lambda: 995)
    monkeypatch.setattr(c0_responder.os, "fstat", invalid_fstat)

    with pytest.raises(ValidationError, match="custody"):
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )


@pytest.mark.parametrize(
    ("credential_root", "credential_name"),
    [
        (None, "signing-key.pem"),
        ("relative", "signing-key.pem"),
        ("exact", "other.pem"),
    ],
)
def test_systemd_load_credential_rejects_wrong_path_provenance(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    credential_root: str | None,
    credential_name: str,
) -> None:
    credential_dir = tmp_path / "credentials"
    credential_dir.mkdir()
    credential_path = credential_dir / credential_name
    credential_path.write_bytes(P256KeyPair.generate().private_pem)
    actual_fstat = os.fstat

    def systemd_fstat(descriptor: int) -> SimpleNamespace:
        info = actual_fstat(descriptor)
        return SimpleNamespace(
            st_mode=stat.S_IFREG | 0o440,
            st_nlink=1,
            st_uid=0,
            st_gid=0,
            st_size=info.st_size,
        )

    if credential_root == "relative":
        monkeypatch.setenv("CREDENTIALS_DIRECTORY", "credentials")
    elif credential_root == "exact":
        monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(credential_dir))
    else:
        monkeypatch.delenv("CREDENTIALS_DIRECTORY", raising=False)
    monkeypatch.setattr(c0_responder.os, "geteuid", lambda: 995)
    monkeypatch.setattr(c0_responder.os, "fstat", systemd_fstat)

    with pytest.raises(ValidationError, match="custody"):
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )


def test_direct_c0_credential_remains_owner_only(
    tmp_path: Path,
) -> None:
    credential_path = tmp_path / "credential.pem"
    payload = P256KeyPair.generate().private_pem
    credential_path.write_bytes(payload)
    credential_path.chmod(0o600)

    assert (
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )
        == payload
    )

    target = tmp_path / "target.pem"
    target.write_bytes(payload)
    target.chmod(0o600)
    credential_path.unlink()
    credential_path.symlink_to(target)
    with pytest.raises(ValidationError, match="unavailable"):
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )


@pytest.mark.skipif(
    not getattr(os, "O_NONBLOCK", 0),
    reason="host does not expose nonblocking file opens",
)
def test_c0_credential_open_cannot_block_on_special_files(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    credential_path = tmp_path / "credential.pem"
    payload = P256KeyPair.generate().private_pem
    credential_path.write_bytes(payload)
    credential_path.chmod(0o600)
    actual_open = os.open

    def guarded_open(path: Path, flags: int) -> int:
        assert flags & os.O_NONBLOCK, "credential open could block on a FIFO or device"
        return actual_open(path, flags)

    monkeypatch.setattr(c0_responder.os, "open", guarded_open)

    assert (
        c0_responder._credential_file(
            credential_path,
            label="C0 responder credential",
        )
        == payload
    )

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
    monkeypatch.setattr("agentnet.cli.commands.auth.check_c0_responder", c0_responder.check_c0_responder)

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
