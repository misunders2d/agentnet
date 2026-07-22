from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import cli
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.config import (
    ExtensionConfig,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
)
from agentnet.security.signatures import P256KeyPair


PURPOSES = frozenset(
    {
        "authorization.bootstrap_plan.approve",
        "authorization.elevation.approve",
        "identity.credential.recover.approve",
        "identity.enrollment.approve",
        "identity.harness.revoke.approve",
        "organization.relationship.accept",
    }
)


@dataclass
class FakeBinding:
    credential_id: str
    domain_id: str
    harness_id: str
    principal_id: str
    credential_epoch: int
    binding_assurance: str
    public_key_pem: str
    key_id: str
    active_error: Exception | None = None
    active_checks: int = 0

    def require_active(self, *, now: int) -> None:
        assert now > 0
        self.active_checks += 1
        if self.active_error is not None:
            raise self.active_error


class FakeStore:
    def __init__(self) -> None:
        self.closed = False

    def close(self) -> None:
        self.closed = True


def activation_fixture(tmp_path: Path):
    approver_key = P256KeyPair.generate()
    key = P256KeyPair.generate()
    public_base_url = "https://agents.corp.example"
    domain_id = "corp.example"
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=domain_id,
        principal_id="principal-1",
        harness_id="harness-1",
        credential_id="credential-1",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    oidc = OIDCEnrollmentConfig(
        issuer="https://identity.corp.example",
        client_id="agentnet-server",
        redirect_uri=f"{public_base_url}/v1/enrollment/oidc/callback",
        verifier_id="independent-approval.corp.example",
        trusted_approvers=(
            IndependentApproverConfig(
                principal_id="security-owner",
                signer_key_id=approver_key.thumbprint,
                public_key_pem=approver_key.public_pem,
                allowed_purposes=PURPOSES,
            ),
        ),
    )
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=domain_id,
        data_dir=tmp_path / "server",
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        artifact_dir=tmp_path / "server" / "artifacts",
        public_base_url=public_base_url,
        runtime_instance_id="server-agent-primary",
        oidc_enrollment=oidc,
    )
    identity = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": public_base_url,
        "audience": config.effective_service_audience,
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(tmp_path / "identity.key.pem"),
    }
    binding = FakeBinding(
        credential_id=actor.credential_id or "",
        domain_id=domain_id,
        harness_id=actor.harness_id or "",
        principal_id=actor.principal_id or "",
        credential_epoch=actor.credential_epoch,
        binding_assurance=actor.binding_assurance,
        public_key_pem=key.public_pem,
        key_id=key.thumbprint,
    )
    return SimpleNamespace(
        config=config,
        identity=identity,
        actor=actor,
        key=key,
        binding=binding,
        store=FakeStore(),
        config_path=tmp_path / "agentnet.json",
        identity_path=tmp_path / "identity.json",
    )


def install_activation_fakes(
    monkeypatch: pytest.MonkeyPatch,
    state,
) -> tuple[list[tuple[Path, dict[str, object], bool]], list[ExtensionConfig]]:
    writes: list[tuple[Path, dict[str, object], bool]] = []
    opened: list[ExtensionConfig] = []
    monkeypatch.setattr(cli, "_load_config", lambda _path: state.config)
    monkeypatch.setattr(
        cli,
        "_load_identity_profile",
        lambda _path: (state.identity, state.actor, state.key),
    )

    def open_store(config: ExtensionConfig) -> FakeStore:
        opened.append(config)
        return state.store

    monkeypatch.setattr(cli, "_open_server_agent_activation_store", open_store)
    monkeypatch.setattr(cli, "load_credential_binding", lambda _store, _credential_id: state.binding)
    monkeypatch.setattr(
        cli,
        "_write_private_config",
        lambda path, value, *, force=False: writes.append((path, value, force)),
    )
    return writes, opened


def activation_args(state) -> argparse.Namespace:
    return argparse.Namespace(config=str(state.config_path), identity=str(state.identity_path))


def test_server_agent_activate_binds_only_exact_identity_without_granting_authority(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state = activation_fixture(tmp_path)
    before = state.config.redacted_export()
    assert before["oidc_enrollment"]["trusted_approvers"][0][
        "allowed_purposes"
    ] == sorted(PURPOSES)
    writes, opened = install_activation_fakes(monkeypatch, state)

    assert cli.command_server_agent_activate(activation_args(state)) == 0

    assert state.store.closed is True
    assert state.binding.active_checks == 1
    assert len(opened) == 1
    assert opened[0].enrolled_harness_id == state.actor.harness_id
    assert opened[0].enrolled_credential_id == state.actor.credential_id
    assert len(writes) == 1
    path, value, force = writes[0]
    assert path == state.config_path
    assert force is True
    changed = {key for key in value if value[key] != before[key]}
    assert changed == {"enrolled_harness_id", "enrolled_credential_id"}
    assert value["server_agent_capabilities"] == before["server_agent_capabilities"]
    assert value["features"] == before["features"]
    assert value["a2a"] == before["a2a"]
    assert value["relay"] == before["relay"]
    output = json.loads(capsys.readouterr().out)
    assert output["activated"] is True
    assert output["idempotent_repeat"] is False
    assert output["authority_granted"] is False
    assert output["service_restart"] == "not_performed"


def test_server_agent_activate_exact_repeat_is_idempotent(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    state = activation_fixture(tmp_path)
    state.config = ExtensionConfig.model_validate(
        {
            **state.config.model_dump(mode="python"),
            "enrolled_harness_id": state.actor.harness_id,
            "enrolled_credential_id": state.actor.credential_id,
        }
    )
    writes, _opened = install_activation_fakes(monkeypatch, state)

    assert cli.command_server_agent_activate(activation_args(state)) == 0

    assert writes == []
    output = json.loads(capsys.readouterr().out)
    assert output["activated"] is False
    assert output["idempotent_repeat"] is True


@pytest.mark.parametrize(
    ("mutation", "message"),
    (
        ("domain", "different AgentNet domain"),
        ("origin", "different AgentNet service origin"),
        ("audience", "different AgentNet service audience"),
    ),
)
def test_server_agent_activate_rejects_identity_scope_mismatch_before_store_open(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    mutation: str,
    message: str,
) -> None:
    state = activation_fixture(tmp_path)
    if mutation == "domain":
        state.actor = state.actor.model_copy(update={"domain_id": "other.example"})
    elif mutation == "origin":
        state.identity["server_base_url"] = "https://other.example"
    else:
        state.identity["audience"] = "urn:agentnet:other.example:corporate-api"
    writes, opened = install_activation_fakes(monkeypatch, state)

    with pytest.raises(SystemExit, match=message):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
    assert opened == []


def test_server_agent_activate_rejects_noncanonical_audience_before_store_open(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state = activation_fixture(tmp_path)
    state.identity["audience"] = " urn:agentnet:corp.example:corporate-api"
    writes, opened = install_activation_fakes(monkeypatch, state)

    with pytest.raises(SystemExit, match="audience is not canonical"):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
    assert opened == []


def test_server_agent_activate_rejects_existing_different_binding_before_store_open(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state = activation_fixture(tmp_path)
    state.config = ExtensionConfig.model_validate(
        {
            **state.config.model_dump(mode="python"),
            "enrolled_harness_id": "different-harness",
            "enrolled_credential_id": "different-credential",
        }
    )
    writes, opened = install_activation_fakes(monkeypatch, state)

    with pytest.raises(SystemExit, match="already bound to a different identity"):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
    assert opened == []


@pytest.mark.parametrize(
    "field",
    (
        "credential_id",
        "domain_id",
        "harness_id",
        "principal_id",
        "credential_epoch",
        "binding_assurance",
        "public_key_pem",
        "key_id",
    ),
)
def test_server_agent_activate_rejects_stored_binding_mismatch(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    field: str,
) -> None:
    state = activation_fixture(tmp_path)
    replacement: object = "different"
    if field == "credential_epoch":
        replacement = 2
    elif field == "public_key_pem":
        replacement = P256KeyPair.generate().public_pem
    setattr(state.binding, field, replacement)
    writes, _opened = install_activation_fakes(monkeypatch, state)

    with pytest.raises(SystemExit, match="does not match its current stored credential binding"):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
    assert state.store.closed is True


@pytest.mark.parametrize("reason", ("revoked", "retired", "expired", "stale epoch"))
def test_server_agent_activate_rejects_inactive_binding(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    reason: str,
) -> None:
    state = activation_fixture(tmp_path)
    state.binding.active_error = AuthenticationError(reason)
    writes, _opened = install_activation_fakes(monkeypatch, state)

    with pytest.raises(AuthenticationError, match=reason):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
    assert state.store.closed is True


def test_server_agent_activate_rejects_non_server_profile_before_identity_read(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config = ExtensionConfig(data_dir=tmp_path / "local")
    monkeypatch.setattr(cli, "_load_config", lambda _path: config)
    monkeypatch.setattr(
        cli,
        "_load_identity_profile",
        lambda _path: pytest.fail("identity must not be read for wrong profile"),
    )

    with pytest.raises(SystemExit, match="requires always_on_server_agent profile"):
        cli.command_server_agent_activate(
            argparse.Namespace(config=str(tmp_path / "agentnet.json"), identity="unused")
        )


def test_server_agent_activation_store_fences_exact_runtime_without_migrations(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state = activation_fixture(tmp_path)
    cipher = object()
    captured: dict[str, object] = {}
    fake_store = FakeStore()
    monkeypatch.setattr(
        cli.LocalEnvelopeCipher,
        "from_key_file",
        lambda path, *, create: captured.update({"key_path": path, "create": create}) or cipher,
    )

    def fake_postgres(database_url, passed_cipher, **kwargs):
        captured.update(
            {
                "database_url": database_url,
                "cipher": passed_cipher,
                **kwargs,
            }
        )
        return fake_store

    monkeypatch.setattr(cli, "PostgreSQLStore", fake_postgres)

    assert cli._open_server_agent_activation_store(state.config) is fake_store
    assert captured["key_path"] == state.config.data_dir / "secrets" / "records.key"
    assert captured["create"] is False
    assert captured["database_url"] == state.config.database_url
    assert captured["cipher"] is cipher
    assert captured["instance_id"] == state.config.runtime_instance_id
    assert str(captured["lease_owner_id"]).startswith("activation-")
    assert captured["lease_owner_id"] != state.config.runtime_instance_id
    assert captured["run_migrations"] is False
    assert captured["start_lease_keeper"] is False


def test_server_agent_activate_store_unavailable_leaves_config_unchanged(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    state = activation_fixture(tmp_path)
    writes, _opened = install_activation_fakes(monkeypatch, state)
    monkeypatch.setattr(
        cli,
        "_open_server_agent_activation_store",
        lambda _config: (_ for _ in ()).throw(GateBlocked("lease_contended", "runtime active")),
    )

    with pytest.raises(GateBlocked, match="runtime active"):
        cli.command_server_agent_activate(activation_args(state))
    assert writes == []
