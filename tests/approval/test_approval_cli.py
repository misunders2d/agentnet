from __future__ import annotations

import json
import os
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import cli
from agentnet.approval import cli_commands as approval_cli
from agentnet.approval.config import MANDATORY_APPROVAL_PURPOSES, load_approval_service_config
from agentnet.errors import ValidationError


def _private_dir(path: Path) -> Path:
    path.mkdir(parents=True, mode=0o700)
    path.chmod(0o700)
    return path


def _approver_spec(path: Path) -> Path:
    value = {
        "approvers": [
            {
                "principal_id": "security-owner",
                "authority_kind": "human",
                "domain_id": "corp.example",
                "allowed_purposes": sorted(MANDATORY_APPROVAL_PURPOSES),
            }
        ]
    }
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path


def test_approval_provision_outputs_only_public_trust_and_refuses_overwrite(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    private = _private_dir(tmp_path / "private")
    spec = _approver_spec(private / "approvers.json")
    config_path = private / "config.json"
    data_dir = tmp_path / "approval-data"
    args = [
        "approval",
        "provision",
        "--config",
        str(config_path),
        "--data-dir",
        str(data_dir),
        "--public-origin",
        "https://approval.corp.example",
        "--rp-id",
        "approval.corp.example",
        "--verifier-id",
        "approval.corp.example",
        "--approvers",
        str(spec),
    ]
    assert cli.main(args) == 0
    output_text = capsys.readouterr().out
    output = json.loads(output_text)
    assert output["provisioned"] is True
    assert output["authority_granted"] is False
    assert output["webauthn_registered"] is False
    assert "PRIVATE KEY" not in output_text
    assert "signer_private_key_path" not in output_text
    assert "PUBLIC KEY" in output_text

    config = load_approval_service_config(config_path)
    assert config.verifier_id == "approval.corp.example"
    for path in (
        config_path,
        config.database_path,
        config.record_key_path,
        config.approvers[0].signer_private_key_path,
    ):
        assert stat_mode(path) == 0o600
    assert stat_mode(data_dir) == 0o700
    assert stat_mode(data_dir / "secrets") == 0o700
    assert stat_mode(data_dir / "signers") == 0o700

    with pytest.raises(SystemExit, match="refuses to overwrite"):
        cli.main(args)

    assert cli.main(["approval", "status", "--config", str(config_path)]) == 0
    status_text = capsys.readouterr().out
    status = json.loads(status_text)
    assert status["ready"] is True
    assert status["active_credentials"] == 0
    assert status["independent_boundary_proven"] is False
    for forbidden in ("PRIVATE KEY", "PUBLIC KEY", "receipt", "transaction", "credential_id"):
        assert forbidden not in status_text


def test_pending_open_and_watch_keep_approval_capability_local(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    pending = {
        "request_id": "request-1",
        "approver_principal_id": "security-owner",
        "domain_id": "corp.example",
        "approval_purpose": "identity.enrollment.approve",
        "transaction_digest": "a" * 64,
        "delivery_mode": "core_claim_code",
        "openable_locally": True,
        "created_at": 1_800_000_000,
        "expires_at": 1_800_000_300,
    }
    secret_url = "https://approval.corp.example/approval#token=agcap1.SECRET&kind=approval"
    opened: list[str] = []

    class FakeStore:
        def close(self) -> None:
            return None

    class FakeService:
        def pending_requests(self) -> list[dict[str, object]]:
            return [pending]

        def local_approval_url(self, request_id: str) -> str:
            assert request_id == "request-1"
            return secret_url

    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: (SimpleNamespace(), FakeStore(), FakeService()),
    )
    monkeypatch.setattr(
        approval_cli.webbrowser,
        "open",
        lambda url, new=0: opened.append(url) or True,
    )

    assert cli.main(["approval", "pending", "--config", "/private/config.json"]) == 0
    pending_output = capsys.readouterr().out
    assert json.loads(pending_output)["requests"] == [pending]
    assert "agcap1." not in pending_output

    assert (
        cli.main(
            [
                "approval",
                "open",
                "--config",
                "/private/config.json",
                "--request-id",
                "request-1",
            ]
        )
        == 0
    )
    open_output = capsys.readouterr().out
    assert json.loads(open_output) == {
        "schema": "agentnet.approval.local-open.v1",
        "request_id": "request-1",
        "opened": True,
    }
    assert "agcap1." not in open_output

    assert (
        cli.main(
            [
                "approval",
                "watch",
                "--config",
                "/private/config.json",
                "--open",
                "--once",
            ]
        )
        == 0
    )
    watch_output = capsys.readouterr().out
    assert json.loads(watch_output)["opened"] is True
    assert "agcap1." not in watch_output
    assert opened == [secret_url, secret_url]


def test_approval_config_rejects_duplicate_json_keys_before_parsing(tmp_path: Path) -> None:
    private = _private_dir(tmp_path / "private")
    config = private / "config.json"
    config.write_text('{"schema_version":"1.0","schema_version":"1.0"}', encoding="utf-8")
    config.chmod(0o600)
    with pytest.raises(ValidationError, match="configuration is invalid"):
        load_approval_service_config(config)


def test_approval_serve_rejects_non_loopback_before_loading_config() -> None:
    with pytest.raises(SystemExit, match="loopback"):
        cli.main(
            [
                "approval",
                "serve",
                "--config",
                "/does/not/exist",
                "--host",
                "0.0.0.0",
            ]
        )
    with pytest.raises(SystemExit, match="explicit loopback IP"):
        cli.main(
            [
                "approval",
                "serve",
                "--config",
                "/does/not/exist",
                "--host",
                "localhost",
            ]
        )


def stat_mode(path: Path) -> int:
    return os.stat(path).st_mode & 0o777
