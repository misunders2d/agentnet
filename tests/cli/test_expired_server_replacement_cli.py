from __future__ import annotations

import argparse
import json
import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import cli
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.identity.expired_server_replacement import ExpiredServerReplacementService
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.config_migration import load_config_json


class _NoCloseStore:
    def __init__(self, store) -> None:
        self._store = store

    def __getattr__(self, name: str):
        return getattr(self._store, name)

    def close(self) -> None:
        pass


def _stage_replacement(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    store,
    identity_factory,
    *,
    expired: bool = True,
) -> SimpleNamespace:
    actor, key = identity_factory(binding_assurance="hardware_bound")
    now = int(time.time())
    expires_at = now - 60 if expired else now + 3_600
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (now - 600, expires_at, actor.credential_id),
        )

    data_dir = tmp_path / "data"
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=actor.domain_id,
        data_dir=data_dir,
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        artifact_mode="disabled",
        artifact_dir=data_dir / "artifacts",
        public_base_url="https://agents.corp.example",
        runtime_instance_id="ordinary-server-1",
        server_agent_capabilities={ServerAgentCapability.OFFLINE_CUSTODY},
        enrolled_harness_id=actor.harness_id,
        enrolled_credential_id=actor.credential_id,
    )
    config_path = tmp_path / "agentnet.json"
    identity_path = tmp_path / "identity.json"
    state_path = tmp_path / "expired-binding-state.json"
    key_path = tmp_path / "identity.key.pem"
    key_path.write_bytes(key.private_pem)
    key_path.chmod(0o600)
    identity = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": config.public_base_url,
        "audience": config.effective_service_audience,
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(key_path),
    }
    cli._write_private_config(config_path, config.redacted_export(), force=False)
    cli._write_private_config(identity_path, identity, force=False)
    monkeypatch.setattr(
        cli,
        "_open_server_agent_activation_store",
        lambda _config: _NoCloseStore(store),
    )
    return SimpleNamespace(
        actor=actor,
        key=key,
        config=config,
        config_path=config_path,
        identity_path=identity_path,
        state_path=state_path,
        args=argparse.Namespace(
            config=str(config_path),
            identity=str(identity_path),
            state=str(state_path),
            setup_request_digest="a" * 64,
        ),
    )


def test_cli_replacement_is_release_bound_to_0132(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(cli, "__version__", "0.1.33")
    with pytest.raises(SystemExit, match="only in AgentNet 0.1.32"):
        cli.command_server_agent_replace_expired_binding(argparse.Namespace())


def test_cli_replaces_exact_expired_binding_without_restart_or_authority(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    store,
    identity_factory,
) -> None:
    staged = _stage_replacement(tmp_path, monkeypatch, store, identity_factory)

    assert cli.command_server_agent_replace_expired_binding(staged.args) == 0

    result = json.loads(capsys.readouterr().out)
    assert result == {
        "schema": "agentnet.expired-server-replacement-cli-result.v1",
        "status": "replaced",
        "authority_granted": False,
        "service_restart": "not_performed",
    }
    config = load_config_json(staged.config_path.read_text(encoding="utf-8"))
    identity = json.loads(staged.identity_path.read_text(encoding="utf-8"))
    assert config.enrolled_harness_id == staged.actor.harness_id
    assert config.enrolled_credential_id != staged.actor.credential_id
    assert identity["actor"]["credential_id"] == config.enrolled_credential_id
    assert identity["actor"]["credential_epoch"] == staged.actor.credential_epoch + 1
    assert not staged.state_path.exists()
    assert store.fetch_one(
        "SELECT status FROM credentials WHERE credential_id=?",
        (staged.actor.credential_id,),
    )["status"] == "retired"
    successor = store.fetch_one(
        "SELECT status,key_id,epoch FROM credentials WHERE credential_id=?",
        (config.enrolled_credential_id,),
    )
    assert dict(successor) == {
        "status": "active",
        "key_id": staged.key.thumbprint,
        "epoch": staged.actor.credential_epoch + 1,
    }


def test_cli_response_loss_replays_committed_database_transition_and_finishes_files(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    store,
    identity_factory,
) -> None:
    staged = _stage_replacement(tmp_path, monkeypatch, store, identity_factory)
    previous_config = staged.config_path.read_bytes()
    previous_identity = staged.identity_path.read_bytes()
    original_replace = ExpiredServerReplacementService.replace
    calls = 0

    def lose_first_response(self, *, actor, request):
        nonlocal calls
        result = original_replace(self, actor=actor, request=request)
        calls += 1
        if calls == 1:
            raise RuntimeError("injected response loss after database commit")
        return result

    monkeypatch.setattr(ExpiredServerReplacementService, "replace", lose_first_response)
    with pytest.raises(RuntimeError, match="response loss"):
        cli.command_server_agent_replace_expired_binding(staged.args)

    assert staged.state_path.exists()
    assert staged.config_path.read_bytes() == previous_config
    assert staged.identity_path.read_bytes() == previous_identity
    assert store.fetch_one(
        "SELECT COUNT(*) AS n FROM expired_server_credential_replacements"
    )["n"] == 1

    assert cli.command_server_agent_replace_expired_binding(staged.args) == 0
    assert calls == 2
    assert not staged.state_path.exists()
    output = json.loads(capsys.readouterr().out)
    assert output["status"] == "replaced"
    assert output["authority_granted"] is False
    config = load_config_json(staged.config_path.read_text(encoding="utf-8"))
    assert config.enrolled_credential_id != staged.actor.credential_id


def test_cli_current_binding_is_idempotent_and_creates_no_transition_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    store,
    identity_factory,
) -> None:
    staged = _stage_replacement(
        tmp_path,
        monkeypatch,
        store,
        identity_factory,
        expired=False,
    )

    assert cli.command_server_agent_replace_expired_binding(staged.args) == 0

    output = json.loads(capsys.readouterr().out)
    assert output["status"] == "already_current"
    assert output["authority_granted"] is False
    assert output["service_restart"] == "not_performed"
    assert not staged.state_path.exists()
    assert staged.config_path.read_bytes() == json.dumps(
        staged.config.redacted_export(), indent=2, sort_keys=True
    ).encode() + b"\n"


def test_replacement_state_reader_accepts_bounded_combined_prior_payloads(
    tmp_path: Path,
) -> None:
    state_path = (tmp_path / "large-state.json").absolute()
    payload = b"x" * 131_072
    cli._write_owner_only(state_path, payload)

    with pytest.raises(SystemExit, match="owner-only bounded regular file"):
        cli._owner_only_file(state_path, label="default private file")
    assert cli._owner_only_file(
        state_path,
        label="expired server replacement state",
        max_bytes=262_144,
    ) == payload


def test_cli_argument_drift_cannot_reuse_durable_transition_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    store,
    identity_factory,
) -> None:
    staged = _stage_replacement(tmp_path, monkeypatch, store, identity_factory)
    original_replace = ExpiredServerReplacementService.replace

    def lose_response(self, *, actor, request):
        original_replace(self, actor=actor, request=request)
        raise RuntimeError("injected response loss")

    monkeypatch.setattr(ExpiredServerReplacementService, "replace", lose_response)
    with pytest.raises(RuntimeError, match="response loss"):
        cli.command_server_agent_replace_expired_binding(staged.args)
    assert staged.state_path.exists()

    changed = argparse.Namespace(**vars(staged.args))
    changed.setup_request_digest = "b" * 64
    with pytest.raises(SystemExit, match="state conflicts"):
        cli.command_server_agent_replace_expired_binding(changed)
    assert staged.state_path.exists()
    assert store.fetch_one(
        "SELECT COUNT(*) AS n FROM expired_server_credential_replacements"
    )["n"] == 1
