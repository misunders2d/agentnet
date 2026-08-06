from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.cli import main
from agentnet.errors import GateBlocked, ValidationError
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.daemon import (
    SupervisorDaemonConfig,
    _owner_queue_cipher,
    load_supervisor_config,
)


def _write_config(tmp_path: Path, *, mode: int = 0o600) -> Path:
    evidence_key = P256KeyPair.generate()
    config = tmp_path / "agentnet-supervisor.json"
    config.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "core_base_url": "https://agentnet.example",
                "audience": "https://agentnet.example",
                "domain_id": "corp.example",
                "harness_id": "laptop-codex",
                "credential_id": "credential-laptop-codex",
                "collaboration_scope_id": "scope:supervisor-background",
                "signing_key_path": str(tmp_path / "signing-key.pem"),
                "harness": "codex",
                "runtime_root": str(tmp_path / "runtime"),
                "queue_database_path": str(tmp_path / "queue.sqlite3"),
                "queue_key_path": str(tmp_path / "queue.key"),
                "evidence_dir": str(tmp_path / "evidence"),
                "trusted_evidence_keys": {evidence_key.thumbprint: evidence_key.public_pem},
                "private_auth_source": str(tmp_path / "worker-auth.json"),
            }
        ),
        encoding="utf-8",
    )
    os.chmod(config, mode)
    return config


def test_supervisor_run_check_validates_owner_only_config_and_redacts_secrets(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    config = _write_config(tmp_path)

    assert main(["supervisor-run", "--config", str(config), "--check"]) == 0

    status = json.loads(capsys.readouterr().out)
    assert status == {
        "auth_environment_names": [],
        "core_base_url": "https://agentnet.example",
        "credential_id": "credential-laptop-codex",
        "domain_id": "corp.example",
        "harness": "codex",
        "harness_id": "laptop-codex",
        "local_bindings_required": False,
        "private_auth_configured": True,
        "reconciliation_interval_seconds": 30.0,
        "reconnect_max_seconds": 5.0,
        "schema": "agentnet.supervisor-daemon.config.v1",
        "trusted_evidence_key_count": 1,
        "watch_wait_seconds": 5.0,
    }
    assert "worker-auth.json" not in json.dumps(status)
    assert "PUBLIC KEY" not in json.dumps(status)


def test_generic_supervisor_rejects_removed_c0_responder_mode(tmp_path: Path) -> None:
    config = _write_config(tmp_path)

    with pytest.raises(SystemExit) as exc_info:
        main(["supervisor-run", "--config", str(config), "--check", "--c0-pilot-responder"])
    assert exc_info.value.code == 2


def test_supervisor_config_rejects_group_or_world_access(tmp_path: Path) -> None:
    config = _write_config(tmp_path, mode=0o640)

    with pytest.raises(ValidationError, match="owner-only"):
        load_supervisor_config(config)


def test_supervisor_config_reports_wrong_config_kind_concisely(tmp_path: Path) -> None:
    config = tmp_path / "agentnet.json"
    config.write_text("{}", encoding="utf-8")
    os.chmod(config, 0o600)

    with pytest.raises(
        ValidationError,
        match=(
            "supervisor daemon config is invalid at core_base_url: Field required; "
            "expected agentnet-supervisor.json, not the core agentnet.json"
        ),
    ) as stopped:
        load_supervisor_config(config)

    assert isinstance(stopped.value.__cause__, PydanticValidationError)


def test_supervisor_run_check_hides_raw_pydantic_error(tmp_path: Path) -> None:
    config = tmp_path / "agentnet.json"
    config.write_text("{}", encoding="utf-8")
    os.chmod(config, 0o600)

    with pytest.raises(SystemExit) as stopped:
        main(["supervisor-run", "--config", str(config), "--check"])

    message = str(stopped.value)
    assert "expected agentnet-supervisor.json, not the core agentnet.json" in message
    assert "validation error for SupervisorDaemonConfig" not in message


def test_supervisor_queue_key_is_exact_owner_file_and_never_followed(tmp_path: Path) -> None:
    key = tmp_path / "queue.key"
    key.write_bytes(b"q" * 32)
    os.chmod(key, 0o600)
    cipher = _owner_queue_cipher(key)
    token = cipher.encrypt_json({"bounded": True}, purpose="daemon-test")
    assert cipher.decrypt_json(token, purpose="daemon-test") == {"bounded": True}

    link = tmp_path / "queue-link.key"
    link.symlink_to(key)
    with pytest.raises(GateBlocked, match="unavailable"):
        _owner_queue_cipher(link)

    key.write_bytes(b"short")
    with pytest.raises(GateBlocked, match="256-bit"):
        _owner_queue_cipher(key)


def test_antigravity_local_binding_remains_explicitly_fail_closed(tmp_path: Path) -> None:
    value = load_supervisor_config(_write_config(tmp_path)).model_dump(mode="python")
    value.update({"harness": "antigravity", "local_bindings_required": True})
    with pytest.raises(ValueError, match="deterministic-only"):
        SupervisorDaemonConfig.model_validate(value)
