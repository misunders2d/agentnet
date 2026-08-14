from __future__ import annotations

import argparse
import hashlib
import json
import os
import stat
from contextlib import nullcontext
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import cli
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.config import (
    ApprovalServiceClientConfig,
    ExtensionConfig,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
)
from agentnet.operations.c0_credential_supersession import load_supersession_journal
from agentnet.security.signatures import P256KeyPair


PURPOSES = frozenset(
    {
        "authorization.bootstrap_plan.approve",
        "authorization.communication_scope.approve",
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

    captured.clear()
    assert cli._open_server_agent_activation_store(
        state.config,
        database_url_override="postgresql://runtime-secret@postgres/agentnet",
    ) is fake_store
    assert captured["database_url"] == "postgresql://runtime-secret@postgres/agentnet"
    parser = cli.build_parser()
    reauthorization = parser.parse_args(
        [
            "server-agent",
            "reauthorize-expired-credential",
            "--config",
            str(state.config_path),
            "--identity",
            str(state.identity_path),
            "--state",
            str(tmp_path / "reauthorization.json"),
        ]
    )
    assert reauthorization.func is cli.command_server_agent_reauthorize_expired_credential
    assert reauthorization.replace_terminal_state is False

    defaults = parser.parse_args(["server-agent", "reauthorize-expired-credential"])
    assert defaults.config == str(cli.CORE_CONFIG)
    assert defaults.identity == str(cli.SERVER_AGENT_IDENTITY)
    assert defaults.state == str(cli.SETUP_ROOT / "credential-reauthorization.json")
    replacement = parser.parse_args(
        [
            "server-agent",
            "replace-expired-scope-harness",
            "--scope-id",
            "scope-replacement-command-0001",
            "--old-harness-id",
            "expired-harness",
            "--new-harness-id",
            "replacement-harness",
        ]
    )
    assert replacement.func is cli.command_server_agent_replace_expired_scope_harness
    assert replacement.role == "member"
    assert replacement.config == str(cli.CORE_CONFIG)
    assert replacement.identity == str(cli.SERVER_AGENT_IDENTITY)
    assert replacement.state == str(cli.SETUP_ROOT / "scope-harness-replacement.json")
    assert replacement.replace_terminal_state is False

    monkeypatch.setattr(cli.os.path, "lexists", lambda _path: True)
    cli._require_managed_server_reauthorization_topology(state.config)

    managed = tmp_path / "managed.json"
    managed.write_text('{"old":true}\n', encoding="utf-8")
    managed.chmod(0o600)
    before = managed.stat()
    assert cli._cas_managed_private_json(
        managed,
        expected_sha256=__import__("hashlib").sha256(managed.read_bytes()).hexdigest(),
        replacement={"new": True},
        label="managed test file",
        expected_uid=before.st_uid,
    ) == "updated"
    after = managed.stat()
    assert (after.st_uid, after.st_gid, after.st_mode & 0o777) == (
        before.st_uid,
        before.st_gid,
        0o600,
    )

    # Exercise the complete two-call CLI ceremony and its most important crash
    # seam without another test ID: config committed, identity still old, state
    # retained.  The exact retry must reconcile instead of issuing a second
    # database credential or Approval request.
    managed_root = tmp_path / "managed"
    setup_root = tmp_path / "setup"
    managed_root.mkdir(mode=0o700)
    setup_root.mkdir(mode=0o700)
    config_path = managed_root / "agentnet.json"
    identity_path = managed_root / "server-agent-identity.json"
    key_path = managed_root / "guided-join.key.pem"
    state_path = setup_root / "credential-reauthorization.json"
    actor = state.actor
    approver = state.config.oidc_enrollment.trusted_approvers[0].model_copy(
        update={"principal_id": actor.principal_id}
    )
    oidc = state.config.oidc_enrollment.model_copy(
        update={
            "trusted_approvers": (approver,),
            "approval_service": ApprovalServiceClientConfig(
                origin="https://approval.corp.example",
                public_origin="https://approval.corp.example",
                service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
                approver_principal_id=actor.principal_id,
            ),
        }
    )
    managed_config = ExtensionConfig.model_validate(
        {
            **state.config.model_dump(mode="python"),
            "data_dir": managed_root,
            "database_url_env": "AGENTNET_DATABASE_URL",
            "oidc_enrollment": oidc,
            "enrolled_harness_id": actor.harness_id,
            "enrolled_credential_id": actor.credential_id,
        }
    )
    managed_identity = {
        **state.identity,
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(key_path),
    }
    terminal_path = tmp_path / "c0-terminal.json"
    journal_path = managed_root / "credential-supersessions.json"
    files: dict[Path, bytes] = {
        config_path: json.dumps(managed_config.redacted_export(), indent=2, sort_keys=True).encode() + b"\n",
        identity_path: json.dumps(managed_identity, indent=2, sort_keys=True).encode() + b"\n",
        key_path: state.key.private_pem,
        terminal_path: json.dumps(
            {
                "schema": "agentnet.c0-pilot-responder.terminal.v1",
                "status": "COMPLETED_C0_ROUND_TRIP",
                "domain_id": actor.domain_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
            },
            sort_keys=True,
        ).encode(),
    }
    monkeypatch.setattr(cli, "CORE_CONFIG", config_path)
    monkeypatch.setattr(cli, "SERVER_AGENT_IDENTITY", identity_path)
    monkeypatch.setattr(cli, "SERVER_AGENT_KEY", key_path)
    monkeypatch.setattr(cli, "SETUP_ROOT", setup_root)
    monkeypatch.setattr(cli, "C0_RESPONDER_TERMINAL", terminal_path)
    monkeypatch.setattr(cli, "CREDENTIAL_SUPERSESSION_JOURNAL", journal_path)
    monkeypatch.setattr(cli, "_managed_server_reauthorization_lock", nullcontext)
    monkeypatch.setattr(cli.os, "geteuid", lambda: 0)
    import pwd

    account = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
    monkeypatch.setattr(pwd, "getpwnam", lambda _name: account)
    original_lstat = Path.lstat

    def managed_lstat(path: Path):
        if path == managed_root:
            return SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=account.pw_uid, st_gid=account.pw_gid)
        if path == setup_root:
            return SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0, st_gid=0)
        return original_lstat(path)

    monkeypatch.setattr(Path, "lstat", managed_lstat)
    monkeypatch.setattr(
        cli.os.path,
        "lexists",
        lambda path: Path(path) in files,
    )

    def managed_read(path: Path, *, label: str, expected_uid=None):
        raw = files[path]
        return raw, SimpleNamespace(
            st_uid=account.pw_uid,
            st_gid=account.pw_gid,
            st_dev=1,
            st_ino=hash(path),
        )

    monkeypatch.setattr(cli, "_managed_private_file", managed_read)
    monkeypatch.setattr(
        cli,
        "_parse_environment_file",
        lambda _path, *, label: {
            "AGENTNET_DATABASE_URL": "postgresql://agentnet@postgres/agentnet",
            "AGENTNET_APPROVAL_CORE_TOKEN": "x" * 43,
        },
    )

    def write_state(path: Path, value: dict[str, object], *, force=False):
        assert path == state_path
        if not force:
            assert path not in files
        files[path] = json.dumps(value, indent=2, sort_keys=True).encode() + b"\n"

    monkeypatch.setattr(cli, "_write_private_config", write_state)
    monkeypatch.setattr(cli, "_owner_only_file", lambda path, *, label: files[path])
    monkeypatch.setattr(cli, "_remove_private_state", lambda path: files.pop(path, None))
    expired_binding = SimpleNamespace(
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        harness_id=actor.harness_id,
        credential_epoch=actor.credential_epoch,
        harness_credential_epoch=actor.credential_epoch,
        credential_status="active",
        harness_status="active",
        principal_status="active",
        domain_status="active",
        binding_assurance=actor.binding_assurance,
        public_key_pem=state.key.public_pem,
        key_id=state.key.thumbprint,
        expires_at=100,
    )
    stores: list[FakeStore] = []

    def open_reauthorization_store(_config, *, database_url_override=None):
        assert database_url_override == "postgresql://agentnet@postgres/agentnet"
        opened_store = FakeStore()
        stores.append(opened_store)
        return opened_store

    monkeypatch.setattr(cli, "_open_server_agent_activation_store", open_reauthorization_store)
    monkeypatch.setattr(cli, "load_credential_binding", lambda _store, _credential_id: expired_binding)
    monkeypatch.setattr(
        cli,
        "completed_c0_terminal_credential",
        lambda *_args, **_kwargs: (actor.credential_id, actor.credential_epoch),
    )
    monkeypatch.setattr(
        cli,
        "load_audited_supersession_journal",
        lambda raw, *_args, **kwargs: load_supersession_journal(
            raw,
            terminal_raw=files[terminal_path],
            domain_id=kwargs["domain_id"],
            principal_id=kwargs["principal_id"],
            harness_id=kwargs["harness_id"],
        ),
    )
    monkeypatch.setattr(cli.time, "time", lambda: 1_000)

    class Broker:
        issued = False
        creates = 0
        retrieves = 0

        def create_request(self, **_kwargs):
            self.creates += 1
            return {"request_id": "approval-request-1"}

        def request_status(self, **_kwargs):
            return {"state": "issued" if self.issued else "pending"}

        def retrieve_receipt(self, **_kwargs):
            self.retrieves += 1
            return {"receipt_id": "receipt-1", "signed": "receipt"}

        def close(self):
            return None

    broker = Broker()
    monkeypatch.setattr(cli, "_managed_server_reauthorization_client", lambda *_args, **_kwargs: broker)
    service_calls: list[str] = []

    class Recovery:
        def __init__(self, *_args, **_kwargs):
            pass

        def reauthorize(
            self,
            *,
            request,
            approval,
            c0_terminal_raw,
            c0_supersession_journal_raw,
        ):
            assert approval == {"receipt_id": "receipt-1", "signed": "receipt"}
            assert c0_terminal_raw == files[terminal_path]
            assert c0_supersession_journal_raw is None
            service_calls.append(request.request_id)
            return SimpleNamespace(
                credential_id="credential-2",
                credential_epoch=2,
                not_before=1_000,
                expires_at=87_400,
                audit_record_hash="a" * 64,
                idempotent_repeat=len(service_calls) > 1,
            )

    monkeypatch.setattr(cli, "ManagedServerCredentialReauthorizationService", Recovery)
    fail_after_config = {"armed": True}
    fail_after_journal = {"armed": True}

    def crashable_cas(path: Path, *, expected_sha256: str, replacement, label: str, expected_uid: int):
        current = files[path]
        if json.loads(current) == replacement:
            return "reconciled"
        assert hashlib.sha256(current).hexdigest() == expected_sha256
        files[path] = json.dumps(replacement, indent=2, sort_keys=True).encode() + b"\n"
        if label == "managed server configuration" and fail_after_config["armed"]:
            fail_after_config["armed"] = False
            raise RuntimeError("injected crash after config CAS")
        return "updated"

    monkeypatch.setattr(cli, "_cas_managed_private_json", crashable_cas)

    def replace_journal(path: Path, *, expected, replacement, uid, gid):
        assert path == journal_path
        if path in files:
            assert files[path] == expected
            if files[path] == replacement:
                return "already_current"
        else:
            assert expected is None
        files[path] = replacement
        if fail_after_journal["armed"]:
            fail_after_journal["armed"] = False
            raise RuntimeError("injected crash after supersession journal")
        return "updated"

    monkeypatch.setattr(cli, "_replace_managed_private_bytes", replace_journal)
    args = argparse.Namespace(
        config=str(config_path),
        identity=str(identity_path),
        state=str(state_path),
        replace_terminal_state=False,
    )
    assert cli.command_server_agent_reauthorize_expired_credential(args) == 2
    assert broker.creates == 1
    broker.issued = True
    pending_v2 = json.loads(files[state_path])
    assert pending_v2["schema"] == "agentnet.managed-server-credential-reauthorization-state.v2"
    assert pending_v2["request"]["c0_terminal_sha256"] == hashlib.sha256(
        files[terminal_path]
    ).hexdigest()
    broker.issued = True
    with pytest.raises(RuntimeError, match="injected crash after supersession journal"):
        cli.command_server_agent_reauthorize_expired_credential(args)
    assert journal_path in files
    assert json.loads(files[config_path])["enrolled_credential_id"] == actor.credential_id
    assert json.loads(files[identity_path])["actor"]["credential_id"] == actor.credential_id
    with pytest.raises(RuntimeError, match="injected crash after config CAS"):
        cli.command_server_agent_reauthorize_expired_credential(args)
    assert state_path in files
    assert json.loads(files[config_path])["enrolled_credential_id"] == "credential-2"
    assert json.loads(files[identity_path])["actor"]["credential_id"] == actor.credential_id
    assert cli.command_server_agent_reauthorize_expired_credential(args) == 0
    assert broker.creates == 1
    assert broker.retrieves == 3
    assert len(service_calls) == 3 and len(set(service_calls)) == 1
    assert json.loads(files[identity_path])["actor"]["credential_id"] == "credential-2"
    assert all(opened_store.closed for opened_store in stores)



def test_root_activation_store_connects_to_exact_peer_socket_as_core_user(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import pwd

    state = activation_fixture(tmp_path)
    peer_url = "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet"
    ids = {"uid": 0, "gid": 0}
    transitions: list[tuple[str, int]] = []
    fake_store = FakeStore()
    monkeypatch.setattr(
        cli.LocalEnvelopeCipher,
        "from_key_file",
        lambda _path, *, create: object(),
    )
    monkeypatch.setattr(cli.os, "geteuid", lambda: ids["uid"])
    monkeypatch.setattr(cli.os, "getegid", lambda: ids["gid"])
    monkeypatch.setattr(
        cli.os,
        "seteuid",
        lambda value: transitions.append(("uid", value)) or ids.update(uid=value),
    )
    monkeypatch.setattr(
        cli.os,
        "setegid",
        lambda value: transitions.append(("gid", value)) or ids.update(gid=value),
    )
    monkeypatch.setattr(
        pwd,
        "getpwnam",
        lambda name: SimpleNamespace(pw_uid=123, pw_gid=456)
        if name == cli.CORE_USER
        else pytest.fail("unexpected account"),
    )

    def fake_postgres(database_url, _cipher, **_kwargs):
        assert database_url == peer_url
        assert ids == {"uid": 123, "gid": 456}
        return fake_store

    monkeypatch.setattr(cli, "PostgreSQLStore", fake_postgres)

    assert cli._open_server_agent_activation_store(
        state.config,
        database_url_override=peer_url,
    ) is fake_store
    assert ids == {"uid": 0, "gid": 0}
    assert transitions == [
        ("gid", 456),
        ("uid", 123),
        ("uid", 0),
        ("gid", 0),
    ]


def test_managed_scope_replacement_is_resumable_and_waits_before_commit(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    import pwd

    state = activation_fixture(tmp_path)
    setup_root = tmp_path / "setup"
    setup_root.mkdir(mode=0o700)
    config_path = tmp_path / "core.json"
    identity_path = tmp_path / "server-agent-identity.json"
    key_path = tmp_path / "server-agent.key.pem"
    pending_path = setup_root / "scope-harness-replacement.json"
    config = state.config.model_copy(
        update={
            "database_url": None,
            "database_url_env": "AGENTNET_DATABASE_URL",
            "enrolled_harness_id": state.actor.harness_id,
        }
    )
    identity = dict(state.identity)
    identity["private_key_path"] = str(key_path)
    config_raw = config.model_dump_json().encode()
    identity_raw = json.dumps(identity, sort_keys=True).encode()
    files = {
        config_path: config_raw,
        identity_path: identity_raw,
        key_path: state.key.private_pem,
    }
    monkeypatch.setattr(cli, "CORE_CONFIG", config_path)
    monkeypatch.setattr(cli, "SERVER_AGENT_IDENTITY", identity_path)
    monkeypatch.setattr(cli, "SERVER_AGENT_KEY", key_path)
    monkeypatch.setattr(cli, "SETUP_ROOT", setup_root)
    monkeypatch.setattr(cli, "load_config_json", lambda _raw: config)
    monkeypatch.setattr(
        pwd,
        "getpwnam",
        lambda _name: SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid()),
    )
    monkeypatch.setattr(
        cli,
        "_managed_private_file",
        lambda path, **_kwargs: (
            files[Path(path)],
            SimpleNamespace(st_gid=os.getgid()),
        ),
    )
    monkeypatch.setattr(
        cli,
        "_parse_environment_file",
        lambda _path, **_kwargs: {
            "AGENTNET_DATABASE_URL": "postgresql://runtime@postgres/agentnet",
            "AGENTNET_APPROVAL_CORE_TOKEN": "x" * 43,
        },
    )
    approval_config = SimpleNamespace(
        approver_principal_id=state.actor.principal_id,
        service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        public_origin="https://approval.corp.example",
    )
    verifier = object()
    monkeypatch.setattr(
        cli,
        "_managed_server_reauthorization_verifier",
        lambda _config: (verifier, approval_config),
    )
    monkeypatch.setattr(
        cli,
        "_require_managed_server_reauthorization_topology",
        lambda _config: None,
    )
    monkeypatch.setattr(
        cli,
        "_require_server_agent_activation_binding",
        lambda *_args, **_kwargs: None,
    )
    stores: list[FakeStore] = []

    def open_store(*_args, **_kwargs):
        opened = FakeStore()
        stores.append(opened)
        return opened

    monkeypatch.setattr(cli, "_open_server_agent_activation_store", open_store)
    request = cli.ScopeHarnessReplacementRequest(
        request_id="scope-replacement-command-request-0001",
        domain_id=state.actor.domain_id,
        owner_principal_id=state.actor.principal_id,
        owner_harness_id="expired-harness",
        scope_id="scope-replacement-command-0001",
        expected_scope_revision=1,
        expected_scope_digest="a" * 64,
        expected_membership_sequence=1,
        expected_policy_revision=1,
        expected_domain_revocation_epoch=1,
        old_harness_id="expired-harness",
        old_credential_id="expired-credential",
        old_credential_epoch=1,
        new_harness_id="replacement-harness",
        new_credential_id="replacement-credential",
        new_credential_epoch=1,
        role="member",
        issued_at=1_800_000_000,
        expires_at=1_800_000_600,
    )
    service_calls: list[str] = []

    class FakeReplacementService:
        def __init__(self, _store, _verifier) -> None:
            assert _verifier is verifier

        def prepare(self, **_kwargs):
            service_calls.append("prepare")
            return request

        def replace(self, **kwargs):
            assert kwargs["request"] == request
            assert kwargs["approval"] == {"receipt": "issued"}
            service_calls.append("replace")
            return SimpleNamespace(
                scope_id=request.scope_id,
                old_harness_id=request.old_harness_id,
                new_harness_id=request.new_harness_id,
                scope_revision=2,
                membership_sequence=2,
                idempotent_repeat=False,
            )

    monkeypatch.setattr(cli, "ScopeHarnessReplacementService", FakeReplacementService)

    class FakeApprovalClient:
        state = "pending"
        creates = 0

        def create_request(self, **kwargs):
            assert pending_path.exists()
            assert kwargs["transaction_digest"] == request.digest
            self.creates += 1
            return {"request_id": "approval-request-1"}

        def request_status(self, **_kwargs):
            return {"state": self.state}

        def retrieve_receipt(self, **_kwargs):
            return {"receipt": "issued"}

        def close(self) -> None:
            pass

    broker = FakeApprovalClient()
    monkeypatch.setattr(
        cli,
        "_managed_server_reauthorization_client",
        lambda *_args, **_kwargs: broker,
    )
    monkeypatch.setattr(cli.time, "time", lambda: 1_800_000_000)
    args = argparse.Namespace(
        config=str(config_path),
        identity=str(identity_path),
        state=str(pending_path),
        scope_id=request.scope_id,
        old_harness_id=request.old_harness_id,
        new_harness_id=request.new_harness_id,
        role="member",
        replace_terminal_state=False,
    )

    assert cli._command_server_agent_replace_expired_scope_harness_locked(args) == 2
    waiting = json.loads(capsys.readouterr().out)
    assert waiting["status"] == "waiting_owner_approval"
    assert waiting["membership_changed"] is False
    assert service_calls == ["prepare"]
    assert broker.creates == 1
    broker.state = "issued"
    assert cli._command_server_agent_replace_expired_scope_harness_locked(args) == 0
    completed = json.loads(capsys.readouterr().out)
    assert completed["status"] == "completed"
    assert completed["service_restart"] == "not_performed"
    assert service_calls == ["prepare", "replace"]
    assert broker.creates == 1
    assert not pending_path.exists()
    assert all(opened.closed for opened in stores)
def test_managed_server_reauthorization_lock_rejects_concurrent_setup(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    setup_root = tmp_path / "setup"
    setup_root.mkdir(mode=0o700)
    lock_path = setup_root / "setup.lock"
    lock_path.write_bytes(b"")
    lock_path.chmod(0o600)
    monkeypatch.setattr(cli, "SETUP_ROOT", setup_root)
    real_fstat = os.fstat

    def root_owned_fstat(descriptor: int):
        metadata = real_fstat(descriptor)
        return SimpleNamespace(
            st_mode=metadata.st_mode,
            st_nlink=metadata.st_nlink,
            st_uid=0,
            st_gid=0,
        )

    monkeypatch.setattr(cli.os, "fstat", root_owned_fstat)
    with cli._managed_server_reauthorization_lock():
        with pytest.raises(
            SystemExit,
            match="another AgentNet setup or recovery operation is active",
        ):
            with cli._managed_server_reauthorization_lock():
                raise AssertionError("concurrent setup lock unexpectedly acquired")


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


def test_server_agent_reset_requires_two_explicit_confirmations_and_returns_evidence(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    parser = cli.build_parser()
    with pytest.raises(SystemExit):
        parser.parse_args(["server-agent", "reset"])
    with pytest.raises(SystemExit):
        parser.parse_args(
            ["server-agent", "reset", "--retain-external-prerequisites"]
        )

    calls: list[bool] = []
    monkeypatch.setattr(
        cli,
        "reset_server_setup",
        lambda *, retain_external_prerequisites: calls.append(
            retain_external_prerequisites
        )
        or {
            "schema": "agentnet.server-setup.reset-evidence.v1",
            "state": "reset",
            "external_prerequisites": "retained",
            "authority_granted": False,
            "identity_enrolled": False,
            "production_durability_proven": False,
        },
    )
    args = parser.parse_args(
        [
            "server-agent",
            "reset",
            "--retain-external-prerequisites",
            "--confirm-package-state-removal",
        ]
    )
    assert args.func(args) == 0
    assert calls == [True]
    assert json.loads(capsys.readouterr().out)["state"] == "reset"


def test_server_agent_reset_direct_call_without_confirmation_fails_closed(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setattr(
        cli,
        "reset_server_setup",
        lambda **_kwargs: pytest.fail("missing confirmation must not reach reset"),
    )
    result = cli.command_server_agent_reset(
        SimpleNamespace(
            retain_external_prerequisites=True,
            confirm_package_state_removal=False,
        )
    )
    assert result == 1
    evidence = json.loads(capsys.readouterr().out)
    assert evidence["blocker"] == "reset_confirmation_required"
    assert evidence["external_prerequisites"] == "retained"
