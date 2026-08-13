"""Adversarial setup runtime, reconciliation, upgrade, and reset regressions.

Every test here starts from a failure the fixed server profile must survive:
a lost response, an interrupted upgrade, a drifted realized state, a stale
journal, a live service that is not the approved runtime, or a reset asked to
remove state this package does not own.
"""

from __future__ import annotations

import base64
import copy
import hashlib
import json
import os
import pwd
import shutil
import stat
import subprocess
import time
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from uuid import NAMESPACE_URL, uuid4, uuid5
from pathlib import Path
from types import SimpleNamespace

import pytest

import agentnet.operations.server_reset as reset
import agentnet.operations.server_setup as setup
from agentnet.artifacts.clamav import (
    ScannerEndpoint,
    clamav_profile_digest,
    clamav_rules_digest,
)
from agentnet.approval.config import MANDATORY_APPROVAL_PURPOSES
from agentnet.approval.store import ApprovalStore
from agentnet.operations.canonical_owner_recovery import (
    CanonicalOwnerAdoptionRequest,
    converge_canonical_approval_owner,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.operations.config import IndependentApproverConfig, OIDCEnrollmentConfig
from agentnet.operations.server_reset import ServerSetupResetError, reset_server_setup
from agentnet.operations.server_setup import (
    ServerSetupError,
    ServerSetupRequest,
    SetupLayout,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
)
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.postgres import ORDINARY_SERVER_POSTGRES_DSN

BROKER = "synthetic-shared-test-token-0123456789abcdef0123456789"


@pytest.fixture(autouse=True)
def _clear_tls_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    for name in ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"):
        monkeypatch.delenv(name, raising=False)


def _private_json(path: Path, value: object) -> Path:
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path


def _communication_only_request(root: Path) -> Path:
    """One canonical communication-only (artifact-disabled) request.v2."""

    core_env = root / "core.env"
    core_env.write_text(
        f"AGENTNET_DATABASE_URL={ORDINARY_SERVER_POSTGRES_DSN}\n"
        f"AGENTNET_APPROVAL_CORE_TOKEN={BROKER}\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    approval_env = root / "approval.env"
    approval_env.write_text(f"AGENTNET_APPROVAL_CORE_TOKEN={BROKER}\n", encoding="utf-8")
    approval_env.chmod(0o600)
    oidc = _private_json(
        root / "core-oidc.json",
        {
            "issuer": "https://accounts.example",
            "client_id": "core-client",
            "redirect_uri": "https://core.corp.example/v1/enrollment/oidc/callback",
            "allowed_endpoint_origins": ["https://accounts.example"],
            "allowed_signing_algorithms": ["RS256"],
            "binding_assurance": "hardware_bound",
        },
    )
    owner_oidc = _private_json(
        root / "owner-oidc.json",
        {
            "issuer": "https://accounts.example",
            "client_id": "approval-client",
            "redirect_uri": "https://approval.corp.example/v1/approval/owner/oidc/callback",
            "allowed_endpoint_origins": ["https://accounts.example"],
            "allowed_signing_algorithms": ["RS256"],
        },
    )
    approvers = _private_json(
        root / "approvers.json",
        {
            "approvers": [
                {
                    "principal_id": "owner-principal",
                    "authority_kind": "human",
                    "domain_id": "corp.example",
                    "allowed_purposes": sorted(MANDATORY_APPROVAL_PURPOSES),
                    "oidc_issuer": "https://accounts.example",
                    "oidc_subject": "owner-subject",
                }
            ]
        },
    )
    return _private_json(
        root / "setup.json",
        {
            "schema": "agentnet.server-setup.request.v2",
            "profile": "always_on_server_agent",
            "artifact_mode": "disabled",
            "domain_id": "corp.example",
            "service_audience": "urn:agentnet:corp.example:corporate-api",
            "runtime_instance_id": "ordinary-server-1",
            "core_public_origin": "https://core.corp.example",
            "approval_public_origin": "https://approval.corp.example",
            "database_url": ORDINARY_SERVER_POSTGRES_DSN,
            "database_url_env": "AGENTNET_DATABASE_URL",
            "core_environment_file": str(core_env),
            "approval_environment_file": str(approval_env),
            "oidc_provider_file": str(oidc),
            "approval_owner_oidc_file": str(owner_oidc),
            "approval_approvers_file": str(approvers),
            "approval_approver_principal_id": "owner-principal",
            "approval_verifier_id": "approval.corp.example",
        },
    )


def _bootstrap_evidence(domain_id: str) -> dict[str, object]:
    return {
        "domain": {
            "domain_id": domain_id,
            "status": "active",
            "policy_revision": 1,
            "revocation_epoch": 0,
            "created_at": 1,
        },
        "recovery": {"ready": True},
        "storage": {"ready": True},
        "audit": {"valid": True},
        "deployment_binding": {"ready": False, "required": True},
        "warning": "software-key/single-PostgreSQL bootstrap; no HA, mTLS, KMS, or restore claim",
    }


def test_canonical_owner_source_rejects_tampered_recovery_phase(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_completed_owner_repair_and_one_hour_hotfix(
        harness
    )
    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    recovery_path = approval_state / "canonical-owner-recovery.json"
    recovery = json.loads(recovery_path.read_text(encoding="utf-8"))
    recovery["phase"] = "authority_adopted"
    _private_json(recovery_path, recovery)

    with pytest.raises(
        ServerSetupError,
        match="canonical owner recovery journal is invalid",
    ):
        getattr(setup, "_canonical_owner_recovery_source")(
            approval_state,
            config_path,
            pwd.getpwuid(os.geteuid()),
            request=harness.request,
        )


@dataclass
class _Harness:
    request: ServerSetupRequest
    layout: SetupLayout
    runtime_generation: list[int]
    product_calls: list[list[str]]
    active_units: set[str]
    disabled_units: set[str]
    loaded_units: set[str]
    systemctl_calls: list[list[str]]
    operation_events: list[tuple[str, object]]
    database_state: dict[str, object]
    approval_signer: P256KeyPair

    @property
    def marker_path(self) -> Path:
        return self.layout.host(setup.SETUP_MARKER)

    @property
    def journal_path(self) -> Path:
        return self.layout.host(setup.SETUP_UPGRADE_JOURNAL)

    def marker(self) -> dict[str, object]:
        return json.loads(self.marker_path.read_text(encoding="utf-8"))

    def unit_payloads(self) -> dict[str, bytes]:
        return {
            unit: self.layout.unit(unit).read_bytes()
            for unit in (setup.APPROVAL_UNIT, setup.CORE_UNIT)
        }

    def install_new_package_runtime(self) -> None:
        """Simulate an installed package upgrade: new tree content and node path."""

        self.runtime_generation[0] += 1

    def plan_digest(self) -> str:
        return str(plan_server_setup(self.request, layout=self.layout)["request_digest"])

    def apply(self, digest: str, *, start: bool = False) -> dict[str, object]:
        return apply_server_setup(
            self.request,
            start=start,
            expected_request_digest=digest,
            layout=self.layout,
            _allow_test_layout=True,
        )


def _harness(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> _Harness:
    request = load_server_setup_request(_communication_only_request(tmp_path))
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    uid = os.geteuid()
    gid = os.getegid()
    accounts = {
        setup.CORE_USER: SimpleNamespace(pw_name=setup.CORE_USER, pw_uid=uid, pw_gid=gid),
        setup.APPROVAL_USER: SimpleNamespace(pw_name=setup.APPROVAL_USER, pw_uid=uid, pw_gid=gid),
        setup.C0_RESPONDER_USER: SimpleNamespace(pw_name=setup.C0_RESPONDER_USER, pw_uid=uid, pw_gid=gid),
    }
    generation = [0]
    active_units: set[str] = set()
    disabled_units: set[str] = set()
    loaded_units: set[str] = set()
    systemctl_calls: list[list[str]] = []
    operation_events: list[tuple[str, object]] = []

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path(f"/opt/agentnet-{generation[0]}/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path(f"/opt/agentnet-{generation[0]}/bin/uv"))
    monkeypatch.setattr(
        setup,
        "_resolve_executable",
        lambda *_args, **_kwargs: Path(f"/opt/agentnet-{generation[0]}/npm/bin/agentnet.mjs"),
    )
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    def fake_systemd_show(_executable: Path, unit: str) -> dict[str, str]:
        path = layout.unit(unit)
        if not path.exists() or unit not in loaded_units:
            return {
                "LoadState": "not-found",
                "UnitFileState": "disabled",
                "ActiveState": "inactive",
                "FragmentPath": "",
                "DropInPaths": "",
                "MainPID": "0",
            }
        return {
            "LoadState": "loaded",
            "UnitFileState": (
                "static"
                if unit == setup.CREDENTIAL_RENEW_UNIT
                else "disabled" if unit in disabled_units else "enabled"
            ),
            "ActiveState": "active" if unit in active_units else "inactive",
            "FragmentPath": str(path),
            "DropInPaths": "",
            "MainPID": (
                "1"
                if unit.endswith(".service") and unit in active_units
                else "0"
            ),
        }

    def fake_run_systemctl(
        _executable: Path,
        arguments: list[str],
        *,
        failure_message: str,
    ) -> None:
        systemctl_calls.append(list(arguments))
        operation_events.append(("systemctl", tuple(arguments)))
        if arguments[:1] == ["daemon-reload"]:
            loaded_units.update(
                unit for unit in setup.MANAGED_UNITS if layout.unit(unit).exists()
            )
        elif arguments[:2] == ["disable", "--now"]:
            unit = arguments[2]
            disabled_units.add(unit)
            active_units.discard(unit)
        elif arguments[:2] == ["enable", "--now"]:
            unit = arguments[2]
            disabled_units.discard(unit)
            active_units.add(unit)
        elif arguments[:1] == ["enable"]:
            disabled_units.discard(arguments[1])
        elif arguments[:1] == ["restart"]:
            active_units.add(arguments[1])
        elif arguments[:1] == ["stop"]:
            active_units.discard(arguments[1])
        elif arguments[:1] == ["start"]:
            active_units.add(arguments[1])
        elif arguments[:2] == ["is-active", "--quiet"]:
            if arguments[2] not in active_units:
                raise ServerSetupError("systemd_start", failure_message)

    monkeypatch.setattr(setup, "_systemd_show", fake_systemd_show)
    monkeypatch.setattr(setup, "_run_systemctl", fake_run_systemctl)
    monkeypatch.setattr(
        setup,
        "_sha256_stable_file",
        lambda path, **_kwargs: hashlib.sha256(str(path).encode()).hexdigest(),
    )
    monkeypatch.setattr(
        setup,
        "_sha256_stable_tree",
        lambda path: hashlib.sha256(f"tree:{path}:{generation[0]}".encode()).hexdigest(),
    )
    monkeypatch.setattr(
        setup,
        "_account_fact",
        lambda _name, home: (
            "already_satisfied" if layout.host(home).is_dir() else "create"
        ),
    )
    monkeypatch.setattr(setup, "_ensure_account", lambda name, _home, **_kwargs: accounts[name])
    monkeypatch.setattr(
        setup,
        "_postgres_peer_gate",
        lambda _account, _url: {"status": "validated_exact_local_peer"},
    )

    signer = P256KeyPair.generate()
    trusted = IndependentApproverConfig(
        principal_id=request.approval_approver_principal_id,
        authority_kind="human",
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
    )
    monkeypatch.setattr(
        setup,
        "_approval_trust",
        lambda *_args, **_kwargs: (SimpleNamespace(model_dump=lambda **_k: {"policy": "fixed"}), [trusted]),
    )
    monkeypatch.setattr(setup, "_require_exact_approval_policy", lambda *_args, **_kwargs: None)

    class _Equal:
        def __eq__(self, _other: object) -> bool:
            return True

    def load_synthetic_config(text: str) -> SimpleNamespace:
        document = json.loads(text)
        return SimpleNamespace(
            profile=setup.RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            domain_id=request.domain_id,
            data_dir=layout.host(setup.CORE_DATA) / "core",
            database_url=request.database_url,
            database_url_env=request.database_url_env,
            artifact_mode="disabled",
            artifact_backend="postgres-manifest",
            artifact_dir=layout.host(setup.CORE_DATA) / "core" / "artifacts",
            public_base_url=request.core_public_origin,
            effective_service_audience=request.service_audience,
            runtime_instance_id=request.runtime_instance_id,
            oidc_enrollment=_Equal(),
            scanner_trust=None,
            server_agent_capabilities={setup.ServerAgentCapability.OFFLINE_CUSTODY},
            a2a=None,
            local_bindings=None,
            relay=None,
            federation_trust=None,
            postgres_recovery_topology=False,
            enrolled_harness_id=document.get("enrolled_harness_id"),
            enrolled_credential_id=document.get("enrolled_credential_id"),
            model_dump=lambda **_kwargs: {"immutable": "fixed"},
        )

    monkeypatch.setattr(setup, "load_config_json", load_synthetic_config)

    product_calls: list[list[str]] = []
    def fake_bounded_product_process(
        account: object,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> setup._BoundedCommandResult:
        del accepted_returncodes
        runtime_root = Path(environment["AGENTNET_NPM_RUNTIME_DIR"])
        runtime_root.mkdir(parents=True, exist_ok=True, mode=0o700)
        runtime_root.chmod(0o700)
        operation_events.append(("runtime", (getattr(account, "pw_name"), stage)))
        assert argv[2:] == ["--version"]
        return setup._BoundedCommandResult(
            returncode=0,
            stdout=f"agentnet {setup.__version__}\n".encode(),
            stderr_present=False,
        )


    def fake_run_as(_account, argv, *, environment, stage, accepted_returncodes=frozenset({0})):
        product_calls.append(list(argv))
        operation_events.append(("product", stage))
        command = argv[2:]
        if command[:2] == ["approval", "provision"]:
            config_path = Path(argv[argv.index("--config") + 1])
            data_dir = Path(argv[argv.index("--data-dir") + 1])
            data_dir.mkdir(parents=True, mode=0o700)
            data_dir.chmod(0o700)
            config_path.write_text("{}", encoding="utf-8")
            config_path.chmod(0o600)
            return {"schema": "agentnet.approval.provision-result.v1"}
        if command[:2] == ["approval", "status"]:
            return {"ready": True}
        if command[:2] == ["network", "create"]:
            config_path = Path(argv[argv.index("--config") + 1])
            data_dir = Path(argv[argv.index("--data-dir") + 1])
            data_dir.mkdir(parents=True, mode=0o700)
            data_dir.chmod(0o700)
            config_path.write_text("{}", encoding="utf-8")
            config_path.chmod(0o600)
            return {
                "config": str(config_path),
                "local_readiness": {
                    "schema": "agentnet.core.readiness.v1",
                    "ready": False,
                    "artifact_mode": "disabled",
                    "storage": {"ready": True},
                    "audit": {"valid": True},
                    "artifacts": {"enabled": False, "required": False, "ready": False, "reason": "disabled"},
                    "deployment_binding": {"ready": False, "required": True},
                    "a2a_schema": {"ready": True},
                    "scanner_trust": {"enabled": False, "required": False, "ready": False, "trusted_key_count": 0},
                },
            }
        if command[0] == "bootstrap-server-agent":
            return _bootstrap_evidence(request.domain_id)
        raise AssertionError(argv)

    source_database = {
        "schema_version": 6,
        "migration_catalog": [
            {
                "version": migration.version,
                "name": migration.name,
                "checksum": migration.checksum,
                "applied_at": migration.version,
            }
            for migration in setup.MIGRATIONS[:6]
        ],
        "endpoint_lifecycle_absent": True,
        "endpoint_mailbox_cursor": 0,
        "identity": {
            "domain_id": request.domain_id,
            "harness_id": "server-harness",
            "principal_id": "server-principal",
            "credential_id": "server-credential",
            "source_harness_kind": "server",
            "harness_kind": "server",
            "profile_key": request.runtime_instance_id,
        },
        "migrated_collaboration": [
            {
                "scope_id": "scope-upgrade-source",
                "owner_harness_id": "harness-upgrade-owner",
                "member_harness_id": "harness-upgrade-fresh",
            }
        ],
        "preserved_relation_digests": {
            relation: hashlib.sha256(relation.encode()).hexdigest()
            for relation in setup._LIFECYCLE_PRESERVED_TABLES
        },
    }
    database_state: dict[str, object] = {
        "phase": "source",
        "source": source_database,
        "endpoint_lifecycle": None,
    }

    def fake_database_operation(
        _account: object,
        _database_url: str,
        *,
        operation: str,
        source: dict[str, object] | None,
        domain_id: str,
        harness_id: str,
        credential_id: str,
        profile_key: str,
    ) -> dict[str, object]:
        expected = database_state["source"]
        assert isinstance(expected, dict)
        identity = expected["identity"]
        assert isinstance(identity, dict)
        assert (domain_id, harness_id, credential_id, profile_key) == (
            identity["domain_id"],
            identity["harness_id"],
            identity["credential_id"],
            identity["profile_key"],
        )
        if operation == "snapshot":
            assert database_state["phase"] == "source"
            return {"status": "source", "source": copy.deepcopy(expected)}
        if operation == "migrate":
            if database_state["phase"] != "source" or source != expected:
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "database changed before migration",
                )
            endpoint = {
                **identity,
                "current_credential_id": identity["credential_id"],
                "state": "restart_required",
                "adapter_generation": 1,
                "mailbox_cursor": expected["endpoint_mailbox_cursor"],
                "capability_root_digest": None,
                "process_measurement": None,
                "state_reason": "explicit_user_restart_required",
                "revision": 2,
                "created_at": 1,
                "updated_at": 1,
            }
            database_state["phase"] = "target"
            database_state["endpoint_lifecycle"] = endpoint
            return {
                "status": "migrated",
                "source": copy.deepcopy(expected),
                "endpoint_lifecycle": copy.deepcopy(endpoint),
            }
        if operation == "rollback":
            if database_state["phase"] == "concurrent":
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "database changed before rollback",
                )
            if database_state["phase"] == "target":
                database_state["phase"] = "source"
                database_state["endpoint_lifecycle"] = None
                return {"status": "rolled_back", "source": copy.deepcopy(expected)}
            if database_state["phase"] == "source":
                return {"status": "already_restored", "source": copy.deepcopy(expected)}
        raise AssertionError(operation)

    monkeypatch.setattr(
        setup,
        "_run_v0145_database_operation_as",
        fake_database_operation,
    )
    monkeypatch.setattr(
        setup,
        "_repair_committed_communication_scope_projection_as",
        lambda _account, _database_url: {"ready": True, "migrated": 0},
    )
    monkeypatch.setattr(setup, "_run_as", fake_run_as)
    monkeypatch.setattr(setup, "_run_bounded_product_process", fake_bounded_product_process)
    return _Harness(
        request=request,
        layout=layout,
        runtime_generation=generation,
        product_calls=product_calls,
        active_units=active_units,
        disabled_units=disabled_units,
        loaded_units=loaded_units,
        systemctl_calls=systemctl_calls,
        operation_events=operation_events,
        database_state=database_state,
        approval_signer=signer,
    )


def test_managed_setup_repairs_committed_scope_projection(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    seen: list[tuple[str, str]] = []

    def repair(account: pwd.struct_passwd, database_url: str) -> dict[str, object]:
        seen.append((account.pw_name, database_url))
        return {"ready": True, "migrated": 1}

    monkeypatch.setattr(
        setup,
        "_repair_committed_communication_scope_projection_as",
        repair,
    )

    result = harness.apply(harness.plan_digest())

    assert seen == [("agentnet", harness.request.database_url)]
    assert {
        "id": "communication_scope_projection",
        "status": "repaired",
        "migrated": 1,
    } in result["steps"]


def _realized_0130_deployment(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> tuple[_Harness, str]:
    """One completed 0.1.30 apply, ready for an upgrade attempt."""

    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.30")
    digest = harness.plan_digest()
    harness.apply(digest)
    assert harness.marker()["package_version"] == "0.1.30"
    assert harness.marker()["revision"] == 1
    return harness, digest


def _realized_0132_deployment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[_Harness, str]:
    """One completed 0.1.32 five-unit apply, ready for an upgrade attempt."""

    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.32")
    digest = harness.plan_digest()
    harness.apply(digest)
    assert harness.marker()["package_version"] == "0.1.32"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    return harness, digest


def _realized_0137_deployment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[_Harness, str]:
    """One exact 0.1.37 five-unit marker and realized topology."""

    harness, digest = _realized_0132_deployment(tmp_path, monkeypatch)
    marker = harness.marker()
    marker["package_version"] = "0.1.37"
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    assert harness.marker()["package_version"] == "0.1.37"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    return harness, digest
def _realized_0144_lifecycle_source(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> _Harness:
    """One enrolled schema-v6 v0.1.44 server with preserved communication state."""

    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.44")
    digest = harness.plan_digest()
    harness.apply(digest)
    config_path = harness.layout.host(setup.CORE_CONFIG)
    config = json.loads(config_path.read_text(encoding="utf-8"))
    identity = harness.database_state["source"]
    assert isinstance(identity, dict)
    identity_row = identity["identity"]
    assert isinstance(identity_row, dict)
    config["enrolled_harness_id"] = identity_row["harness_id"]
    config["enrolled_credential_id"] = identity_row["credential_id"]
    config_path.write_text(
        json.dumps(config, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    config_path.chmod(0o600)
    harness.loaded_units.update(setup.MANAGED_UNITS)
    assert harness.marker()["package_version"] == "0.1.44"
    return harness


def _realized_0145_timer_source(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> _Harness:
    """One exact v0.1.45 server carrying the released non-recurring timer."""

    harness = _harness(tmp_path, monkeypatch)
    current_render_units = setup.render_units

    def released_render_units(
        node_executable: Path,
        executable: Path,
        uv_executable: Path,
    ) -> dict[str, bytes]:
        units = current_render_units(node_executable, executable, uv_executable)
        units[setup.CREDENTIAL_RENEW_TIMER] = units[setup.CREDENTIAL_RENEW_TIMER].replace(
            b"OnUnitInactiveSec=1h\n",
            b"OnUnitActiveSec=1h\nPersistent=true\n",
        )
        units = {
            name: payload.replace(b"/npm-runtimes/0.1.45", b"/npm-runtime")
            for name, payload in units.items()
        }
        return units

    monkeypatch.setattr(setup, "__version__", "0.1.45")
    monkeypatch.setattr(setup, "render_units", released_render_units)
    digest = harness.plan_digest()
    harness.apply(digest)
    monkeypatch.setattr(setup, "render_units", current_render_units)
    assert harness.marker()["package_version"] == "0.1.45"
    assert b"OnUnitActiveSec=1h" in harness.layout.unit(setup.CREDENTIAL_RENEW_TIMER).read_bytes()
    return harness

def _stage_0150_one_hour_approval_hotfix(harness: _Harness) -> tuple[Path, bytes]:
    """Materialize the exact retained v0.1.50 Approval TTL hotfix shape."""

    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    secrets = approval_state / "secrets"
    signers = approval_state / "signers"
    secrets.mkdir(mode=0o700)
    signers.mkdir(mode=0o700)
    record_key = secrets / "records.key"
    record_key.write_bytes(b"k" * 32)
    record_key.chmod(0o600)
    database = approval_state / "approval.sqlite3"
    database.touch(mode=0o600)
    store = ApprovalStore(
        database,
        LocalEnvelopeCipher(b"k" * 32),
        initialize=True,
    )
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_owner_bindings(
                       binding_id,domain_id,approver_principal_id,oidc_issuer,
                       oidc_subject,verified_email,pin_source,status,pinned_at
                   ) VALUES(?,?,?,?,?,?,?,'active',?)""",
                (
                    "owner-binding-1",
                    harness.request.domain_id,
                    harness.request.approval_approver_principal_id,
                    "https://accounts.example",
                    "owner-subject",
                    "owner@corp.example",
                    "exact_subject",
                    1,
                ),
            )
    finally:
        store.close()
    database.chmod(0o600)
    signer = harness.approval_signer
    signer_path = signers / "approver-1.pem"
    signer_path.write_bytes(signer.private_pem)
    signer_path.chmod(0o600)
    config_path = harness.layout.host(setup.APPROVAL_CONFIG)
    _private_json(
        config_path,
        {
            "schema_version": "1.0",
            "public_origin": harness.request.approval_public_origin,
            "rp_id": "approval.corp.example",
            "rp_name": "AgentNet Approval",
            "verifier_id": harness.request.approval_verifier_id,
            "data_dir": str(approval_state),
            "database_path": str(database),
            "record_key_path": str(record_key),
            "request_ttl_seconds": 3_600,
            "challenge_ttl_seconds": 180,
            "receipt_ttl_seconds": 300,
            "registration_ttl_seconds": 600,
            "max_transaction_bytes": 65_536,
            "max_http_body_bytes": 131_072,
            "internal_core_credential_env": "AGENTNET_APPROVAL_CORE_TOKEN",
            "owner_oidc": {
                "issuer": "https://accounts.example",
                "client_id": "approval-client",
                "redirect_uri": (
                    "https://approval.corp.example/v1/approval/owner/oidc/callback"
                ),
                "allowed_endpoint_origins": ["https://accounts.example"],
                "allowed_signing_algorithms": ["RS256"],
            },
            "approvers": [
                {
                    "principal_id": harness.request.approval_approver_principal_id,
                    "authority_kind": "human",
                    "domain_id": harness.request.domain_id,
                    "signer_key_id": signer.thumbprint,
                    "signer_private_key_path": str(signer_path),
                    "allowed_purposes": sorted(MANDATORY_APPROVAL_PURPOSES),
                    "oidc_issuer": "https://accounts.example",
                    "oidc_subject": "owner-subject",
                }
            ],
        },
    )
    source_payload = config_path.read_bytes()
    marker = harness.marker()
    marker["approval_config_digest"] = setup._managed_config_digest(
        config_path,
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        blocker="approval_config",
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    return config_path, source_payload

def _stage_0150_completed_owner_repair_and_one_hour_hotfix(
    harness: _Harness,
    *,
    include_journal: bool = True,
    current_core_target: bool = True,
    communication_scope_ttl_present: bool = True,
) -> tuple[Path, bytes]:
    """Materialize the exact combined live repair over one retained marker."""

    config_path, _hotfix_payload = _stage_0150_one_hour_approval_hotfix(harness)
    approval = json.loads(config_path.read_text(encoding="utf-8"))
    if communication_scope_ttl_present:
        approval["communication_scope_request_ttl_seconds"] = 3_600
    else:
        approval.pop("communication_scope_request_ttl_seconds", None)
    _private_json(config_path, approval)
    target_approver = approval["approvers"][0]
    source_principal = "setup-placeholder-owner"
    source_signer = P256KeyPair.generate()
    original_signer_path = Path(target_approver["signer_private_key_path"])
    if include_journal:
        source_signer_path = original_signer_path.with_name(
            "placeholder-owner.pem"
        )
        target_signer_path = original_signer_path
    else:
        source_signer_path = original_signer_path.with_name(
            "placeholder-owner.pem"
        )
        target_signer_path = original_signer_path
        source_signer_path.write_bytes(source_signer.private_pem)
        source_signer_path.chmod(0o600)

    source_approval = copy.deepcopy(approval)
    source_approval["request_ttl_seconds"] = 300
    source_approval.pop("communication_scope_request_ttl_seconds", None)
    source_approval["approvers"][0]["principal_id"] = source_principal
    source_approval["approvers"][0]["signer_key_id"] = source_signer.thumbprint
    source_approval["approvers"][0]["signer_private_key_path"] = str(
        source_signer_path
    )
    source_hotfix = copy.deepcopy(source_approval)
    source_hotfix["request_ttl_seconds"] = 3_600
    source_hotfix_payload = (
        json.dumps(source_hotfix, indent=2, sort_keys=True).encode("utf-8") + b"\n"
    )

    oidc_provider = setup.SetupOIDCProvider.model_validate(
        json.loads(harness.request.oidc_provider_file.read_text(encoding="utf-8"))
    )
    target_policy = setup.SetupApprover.model_validate(
        json.loads(
            harness.request.approval_approvers_file.read_text(encoding="utf-8")
        )["approvers"][0]
    )
    target_trust = IndependentApproverConfig(
        principal_id=harness.request.approval_approver_principal_id,
        authority_kind=target_policy.authority_kind,
        signer_key_id=harness.approval_signer.thumbprint,
        public_key_pem=harness.approval_signer.public_pem,
        allowed_purposes=target_policy.allowed_purposes,
    )
    target_oidc = setup._build_core_oidc_config(
        harness.request,
        oidc_provider,
        trusted=(target_trust,),
        approvers=(target_policy,),
    )
    source_policy = target_policy.model_copy(
        update={"principal_id": source_principal}
    )
    source_request = harness.request.model_copy(
        update={"approval_approver_principal_id": source_principal}
    )
    source_trust = IndependentApproverConfig(
        principal_id=source_principal,
        authority_kind=source_policy.authority_kind,
        signer_key_id=source_signer.thumbprint,
        public_key_pem=source_signer.public_pem,
        allowed_purposes=source_policy.allowed_purposes,
    )
    source_oidc = setup._build_core_oidc_config(
        source_request,
        oidc_provider,
        trusted=(source_trust,),
        approvers=(source_policy,),
    )
    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    enrolled_identity = {
        "enrolled_harness_id": str(uuid4()),
        "enrolled_credential_id": str(uuid4()),
    }
    target_core = {
        "oidc_enrollment": target_oidc.model_dump(mode="json"),
        **enrolled_identity,
    }
    source_core = {"oidc_enrollment": source_oidc.model_dump(mode="json")}
    realized_core = target_core if current_core_target else {
        **source_core,
        **enrolled_identity,
    }
    core_config_path.write_text(
        json.dumps(realized_core, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    core_config_path.chmod(0o600)
    realized_oidc = target_oidc if current_core_target else source_oidc
    core_oidc_path.write_text(
        json.dumps(realized_oidc.model_dump(mode="json"), indent=2, sort_keys=True)
        + "\n",
        encoding="utf-8",
    )
    core_oidc_path.chmod(0o600)

    recovery_id = str(
        uuid5(
            NAMESPACE_URL,
            f"agentnet:{harness.request.domain_id}:{source_principal}:"
            f"{harness.request.approval_approver_principal_id}",
        )
    )
    adoption = {
        "schema": "agentnet.canonical-owner-adoption-result.v1",
        "status": "adopted",
        "recovery_id": recovery_id,
        "migrated_active_credentials": 1,
        "revoked_browser_sessions": 0,
        "canceled_registration_ceremonies": 0,
    }
    journal = {
        "schema": "agentnet.canonical-owner-recovery-journal.v1",
        "recovery_id": recovery_id,
        "request_digest": hashlib.sha256(
            canonical_json(
                CanonicalOwnerAdoptionRequest(
                    schema="agentnet.canonical-owner-adoption.v1",
                    recovery_id=recovery_id,
                    domain_id=harness.request.domain_id,
                    source_principal_id=source_principal,
                    target_principal_id=harness.request.approval_approver_principal_id,
                    oidc_issuer=target_policy.oidc_issuer,
                    oidc_subject="owner-subject",
                    verified_email="owner@corp.example",
                    verifier_id=harness.request.approval_verifier_id,
                    approved_at=1,
                ).model_dump(by_alias=True, mode="json")
            )
        ).hexdigest(),
        "config_path": str(config_path.absolute()),
        "signer_path": str(source_signer_path),
        "target_signer_path": str(target_signer_path),
        "source_config_sha256": hashlib.sha256(source_hotfix_payload).hexdigest(),
        "domain_id": harness.request.domain_id,
        "source_principal_id": source_principal,
        "target_principal_id": harness.request.approval_approver_principal_id,
        "oidc_issuer": target_policy.oidc_issuer,
        "source_signer_key_id": source_signer.thumbprint,
        "source_signer_public_key_pem": source_signer.public_pem,
        "target_signer_key_id": harness.approval_signer.thumbprint,
        "target_signer_public_key_pem": harness.approval_signer.public_pem,
        "phase": "complete",
        "prepared_at": 1,
        "completed_at": 2,
        "authority_adoption": adoption,
        "authority_adoption_digest": hashlib.sha256(
            canonical_json(adoption)
        ).hexdigest(),
    }
    approval_store = ApprovalStore(
        Path(approval["database_path"]),
        LocalEnvelopeCipher(Path(approval["record_key_path"]).read_bytes()),
    )
    try:
        with approval_store.transaction() as connection:
            target_handle = base64.urlsafe_b64encode(
                hashlib.sha256(
                    canonical_json(
                        {
                            "schema": "agentnet.approval.webauthn-user.v1",
                            "verifier_id": harness.request.approval_verifier_id,
                            "domain_id": harness.request.domain_id,
                            "approver_principal_id": (
                                harness.request.approval_approver_principal_id
                            ),
                        }
                    )
                ).digest()
            ).rstrip(b"=").decode("ascii")
            connection.execute(
                """INSERT INTO approval_webauthn_credentials(
                       credential_id_b64,approver_principal_id,domain_id,
                       user_handle_b64,credential_public_key_b64,sign_count,
                       device_type,backed_up,status,created_at,revoked_at,
                       revocation_reason
                   ) VALUES('recovered-owner-credential',?,?,?,'synthetic-key',
                            0,'single_device',0,'active',1,NULL,NULL)""",
                (
                    harness.request.approval_approver_principal_id,
                    harness.request.domain_id,
                    target_handle,
                ),
            )
            connection.execute(
                """INSERT INTO approval_audit(
                       action,request_id,approver_principal_id,domain_id,
                       approval_purpose,transaction_digest,occurred_at,outcome,detail_code
                   ) VALUES('owner.canonical_adoption',NULL,?,?,
                            'owner.canonical_adoption',?,2,'adopted',
                            'canonical_owner_adopted:v1:1:0:0')""",
                (
                    harness.request.approval_approver_principal_id,
                    harness.request.domain_id,
                    journal["request_digest"],
                ),
            )
            connection.execute(
                """INSERT INTO approval_audit(
                       action,request_id,approver_principal_id,domain_id,
                       approval_purpose,transaction_digest,occurred_at,outcome,detail_code
                   ) VALUES('approval.request',NULL,?,?,
                            'legacy.owner.evidence',?,1,'approved',
                            'legacy_owner_source:v1')""",
                (
                    source_principal,
                    harness.request.domain_id,
                    "f" * 64,
                ),
            )
    finally:
        approval_store.close()

    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    if include_journal:
        _private_json(approval_state / "canonical-owner-recovery.json", journal)

    marker = harness.marker()
    marker["approval_config_digest"] = setup.canonical_digest(source_approval)
    marker["core_config_digest"] = setup.canonical_digest(source_core)
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    return config_path, config_path.read_bytes()


def _stage_0150_partial_owner_repair_and_one_hour_hotfix(
    harness: _Harness,
    *,
    reverse_embedded_trust: bool = False,
) -> tuple[Path, CanonicalOwnerAdoptionRequest]:
    """Materialize the exact retained source/dual-trust/target-sidecar shape."""

    config_path, _ = _stage_0150_one_hour_approval_hotfix(harness)
    approval = json.loads(config_path.read_text(encoding="utf-8"))
    target_approver = approval["approvers"][0]
    target_signer_path = Path(target_approver["signer_private_key_path"])
    source_principal = "setup-placeholder-owner"
    source_signer = P256KeyPair.generate()
    source_signer_path = target_signer_path.with_name("placeholder-owner.pem")
    source_signer_path.write_bytes(source_signer.private_pem)
    source_signer_path.chmod(0o600)
    target_policy = setup.SetupApprover.model_validate(
        json.loads(
            harness.request.approval_approvers_file.read_text(encoding="utf-8")
        )["approvers"][0]
    )
    approval["approvers"][0].update(
        principal_id=source_principal,
        signer_key_id=source_signer.thumbprint,
        signer_private_key_path=str(source_signer_path),
    )
    _private_json(config_path, approval)

    store = ApprovalStore(
        Path(approval["database_path"]),
        LocalEnvelopeCipher(Path(approval["record_key_path"]).read_bytes()),
    )
    try:
        with store.transaction() as connection:
            connection.execute(
                """UPDATE approval_owner_bindings
                      SET approver_principal_id=?
                    WHERE domain_id=? AND status='active'""",
                (source_principal, harness.request.domain_id),
            )
            source_handle = base64.urlsafe_b64encode(
                hashlib.sha256(
                    canonical_json(
                        {
                            "schema": "agentnet.approval.webauthn-user.v1",
                            "verifier_id": harness.request.approval_verifier_id,
                            "domain_id": harness.request.domain_id,
                            "approver_principal_id": source_principal,
                        }
                    )
                ).digest()
            ).rstrip(b"=").decode("ascii")
            connection.execute(
                """INSERT INTO approval_webauthn_credentials(
                       credential_id_b64,approver_principal_id,domain_id,
                       user_handle_b64,credential_public_key_b64,sign_count,
                       device_type,backed_up,status,created_at,revoked_at,
                       revocation_reason
                   ) VALUES('source-owner-credential',?,?,?,'synthetic-key',
                            0,'single_device',0,'active',1,NULL,NULL)""",
                (source_principal, harness.request.domain_id, source_handle),
            )
    finally:
        store.close()

    oidc_provider = setup.SetupOIDCProvider.model_validate(
        json.loads(harness.request.oidc_provider_file.read_text(encoding="utf-8"))
    )
    source_policy = target_policy.model_copy(
        update={"principal_id": source_principal}
    )
    source_request = harness.request.model_copy(
        update={"approval_approver_principal_id": source_principal}
    )
    source_trust = IndependentApproverConfig(
        principal_id=source_principal,
        authority_kind=source_policy.authority_kind,
        signer_key_id=source_signer.thumbprint,
        public_key_pem=source_signer.public_pem,
        allowed_purposes=source_policy.allowed_purposes,
    )
    target_trust = IndependentApproverConfig(
        principal_id=harness.request.approval_approver_principal_id,
        authority_kind=target_policy.authority_kind,
        signer_key_id=harness.approval_signer.thumbprint,
        public_key_pem=harness.approval_signer.public_pem,
        allowed_purposes=target_policy.allowed_purposes,
    )
    source_oidc = setup._build_core_oidc_config(
        source_request,
        oidc_provider,
        trusted=(source_trust,),
        approvers=(source_policy,),
    )
    target_oidc = setup._build_core_oidc_config(
        harness.request,
        oidc_provider,
        trusted=(target_trust,),
        approvers=(target_policy,),
    )
    dual_oidc = target_oidc.model_copy(
        update={
            "trusted_approvers": (
                (target_trust, source_trust)
                if reverse_embedded_trust
                else (source_trust, target_trust)
            )
        }
    )
    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    enrolled_identity = {
        "enrolled_harness_id": str(uuid4()),
        "enrolled_credential_id": str(uuid4()),
    }
    _private_json(
        core_config_path,
        {
            "oidc_enrollment": dual_oidc.model_dump(mode="json"),
            **enrolled_identity,
        },
    )
    _private_json(core_oidc_path, target_oidc.model_dump(mode="json"))

    marker_approval = copy.deepcopy(approval)
    marker_approval["request_ttl_seconds"] = 300
    marker_approval.pop("communication_scope_request_ttl_seconds", None)
    marker = harness.marker()
    marker["approval_config_digest"] = setup.canonical_digest(marker_approval)
    marker["core_config_digest"] = setup.canonical_digest(
        {"oidc_enrollment": source_oidc.model_dump(mode="json")}
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)

    recovery_id = str(
        uuid5(
            NAMESPACE_URL,
            f"agentnet:{harness.request.domain_id}:{source_principal}:"
            f"{harness.request.approval_approver_principal_id}",
        )
    )
    return config_path, CanonicalOwnerAdoptionRequest(
        schema="agentnet.canonical-owner-adoption.v1",
        recovery_id=recovery_id,
        domain_id=harness.request.domain_id,
        source_principal_id=source_principal,
        target_principal_id=harness.request.approval_approver_principal_id,
        oidc_issuer=target_policy.oidc_issuer,
        oidc_subject="owner-subject",
        verified_email="owner@corp.example",
        verifier_id=harness.request.approval_verifier_id,
        approved_at=1,
    )


def _rewrite_repaired_policy_purpose_order(
    harness: _Harness,
    approval_config_path: Path,
) -> None:
    """Simulate a separately seeded repair process reserializing frozensets."""

    approval = json.loads(approval_config_path.read_text(encoding="utf-8"))
    approval["approvers"][0]["allowed_purposes"].reverse()
    _private_json(approval_config_path, approval)

    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    core = json.loads(core_config_path.read_text(encoding="utf-8"))
    core["oidc_enrollment"]["trusted_approvers"][0]["allowed_purposes"].reverse()
    _private_json(core_config_path, core)
    _private_json(
        harness.layout.host(setup.CORE_OIDC_CONFIG),
        core["oidc_enrollment"],
    )





def _realized_public_0131_communication_deployment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[_Harness, str]:
    """One exact released 0.1.31 two-unit marker and realized unit topology."""

    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    digest = harness.plan_digest()
    harness.apply(digest)

    marker = harness.marker()
    for unit in set(setup.MANAGED_UNITS) - set(setup.LEGACY_COMMUNICATION_ONLY_UNITS):
        harness.layout.unit(unit).unlink()
    shutil.rmtree(harness.layout.host(setup.C0_RESPONDER_DATA))
    marker["units"] = list(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    marker["unit_digests"] = {
        unit: marker["unit_digests"][unit]
        for unit in setup.LEGACY_COMMUNICATION_ONLY_UNITS
    }
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)

    assert harness.marker()["schema"] == "agentnet.server-setup.marker.v3"
    assert harness.marker()["artifact_mode"] == "disabled"
    assert harness.marker()["package_version"] == "0.1.31"
    assert harness.marker()["units"] == list(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    harness.active_units.update(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    harness.loaded_units.update(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    return harness, digest


def _stage_public_0130_owner_policy_shape(
    harness: _Harness,
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[OIDCEnrollmentConfig, dict[str, bytes]]:
    """Replace synthetic current configs with exact legacy missing-owner shape."""

    preflight = setup._server_setup_preflight(harness.request, layout=harness.layout)
    approval_config, trusted = setup._approval_trust(
        harness.layout.host(setup.APPROVAL_CONFIG),
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        harness.layout.host(setup.APPROVAL_STATE),
    )
    assert approval_config is not None
    desired = setup._build_core_oidc_config(
        harness.request,
        preflight.oidc_provider,
        trusted=trusted,
        approvers=preflight.approvers,
    )
    legacy = setup._legacy_remote_activation_oidc(desired)
    legacy_document = legacy.model_dump(mode="json")
    core_path = harness.layout.host(setup.CORE_CONFIG)
    oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    core_path.write_bytes(
        json.dumps(
            {"oidc_enrollment": legacy_document},
            indent=2,
            sort_keys=True,
        ).encode()
        + b"\n"
    )
    core_path.chmod(0o600)
    oidc_path.write_bytes(json.dumps(legacy_document, indent=2, sort_keys=True).encode() + b"\n")
    oidc_path.chmod(0o600)

    marker = harness.marker()
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    marker["core_config_digest"] = setup._managed_config_digest(
        core_path,
        account,
        blocker="core_custody",
        exclude_top_level=frozenset({"enrolled_harness_id", "enrolled_credential_id"}),
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)

    original_load = setup.load_config_json

    class ConfigView(SimpleNamespace):
        def model_copy(self, *, update: dict[str, object]) -> "ConfigView":
            values = vars(self).copy()
            values.update(update)
            return ConfigView(**values)

    def load_config_with_real_oidc(text: str) -> ConfigView:
        base = original_load(text)
        values = vars(base).copy()
        document = json.loads(text)
        values["oidc_enrollment"] = OIDCEnrollmentConfig.model_validate(
            document["oidc_enrollment"]
        )
        return ConfigView(**values)

    monkeypatch.setattr(setup, "load_config_json", load_config_with_real_oidc)
    return desired, {
        "core_config": core_path.read_bytes(),
        "core_oidc_config": oidc_path.read_bytes(),
    }


# --------------------------------------------------------------------------
# Supported marker upgrade window
# --------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("schema", "artifact_mode", "source"),
    [
        ("agentnet.server-setup.marker.v2", None, "0.1.28"),
        ("agentnet.server-setup.marker.v3", "disabled", "0.1.28"),
        ("agentnet.server-setup.marker.v3", "enabled", "0.1.30"),
    ],
)
def test_marker_accepts_only_released_package_caused_digest_drift(
    monkeypatch: pytest.MonkeyPatch,
    schema: str,
    artifact_mode: str | None,
    source: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    payload = _marker_payload(schema=schema, package_version=source, artifact_mode=artifact_mode)
    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )
    assert marker is not None
    assert marker["request_digest"] == "1" * 64
    assert marker["package_version"] == source



@pytest.mark.parametrize(
    "source",
    ["0.1.45", "0.1.46", "0.1.47", "0.1.48", "0.1.49", "0.1.50"],
)
def test_0151_accepts_direct_upgrade_from_every_supported_setup_release(
    monkeypatch: pytest.MonkeyPatch,
    source: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version=source,
        artifact_mode="disabled",
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode="disabled",
    )

    assert marker is not None
    assert marker["package_version"] == source
    assert setup._forward_only_setup_upgrade(source, "0.1.51") is True

def test_0151_upgrade_converges_exact_0150_one_hour_approval_hotfix(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, source_payload = _stage_0150_one_hour_approval_hotfix(harness)
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    assert source_payload != config_path.read_bytes()
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert {
        "id": "approval_request_ttl_policy_upgrade",
        "status": "updated_package_upgrade",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


def test_0151_upgrade_rejects_duplicate_core_oidc_purpose(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    _stage_0150_one_hour_approval_hotfix(harness)
    core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    core_oidc = json.loads(core_oidc_path.read_text(encoding="utf-8"))
    core_oidc["trusted_approvers"][0]["allowed_purposes"].append(
        core_oidc["trusted_approvers"][0]["allowed_purposes"][0]
    )
    _private_json(core_oidc_path, core_oidc)

    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert not harness.journal_path.exists()


def test_0151_upgrade_converges_unrecorded_0150_one_hour_approval_hotfix(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_one_hour_approval_hotfix(harness)
    hotfix_document = json.loads(config_path.read_text(encoding="utf-8"))
    published_document = dict(hotfix_document)
    published_document["request_ttl_seconds"] = 300
    _private_json(config_path, published_document)
    marker = harness.marker()
    marker["approval_config_digest"] = setup._managed_config_digest(
        config_path,
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        blocker="approval_config",
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    _private_json(config_path, hotfix_document)

    source_payload = config_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    assert source_payload != config_path.read_bytes()
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert {
        "id": "approval_request_ttl_policy_upgrade",
        "status": "updated_package_upgrade",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


def test_0151_rejects_incomplete_owner_recovery_before_ttl_migration(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, source_payload = _stage_0150_one_hour_approval_hotfix(harness)
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    _private_json(
        recovery_path,
        {
            "schema": "agentnet.canonical-owner-recovery-journal.v1",
            "phase": "prepared",
        },
    )
    recovery_before = recovery_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    real_migrate = getattr(setup, "_migrate_0150_approval_request_ttl_policy")
    migrations: list[bool] = []

    def tracked_migrate(**kwargs: object) -> str:
        migrations.append(True)
        return real_migrate(**kwargs)  # type: ignore[arg-type]

    monkeypatch.setattr(
        setup,
        "_migrate_0150_approval_request_ttl_policy",
        tracked_migrate,
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "canonical_owner_recovery"
    assert migrations == []
    assert config_path.read_bytes() == source_payload
    assert recovery_path.read_bytes() == recovery_before
    assert not harness.journal_path.exists()

@pytest.mark.parametrize(
    "communication_scope_ttl_present",
    (False, True),
    ids=("scope-ttl-absent", "scope-ttl-explicit"),
)
def test_0151_upgrade_converges_completed_owner_repair_and_one_hour_ttl_hotfix(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    communication_scope_ttl_present: bool,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, repaired_payload = (
        _stage_0150_completed_owner_repair_and_one_hour_hotfix(
            harness,
            communication_scope_ttl_present=communication_scope_ttl_present,
        )
    )
    marker = harness.marker()
    realized_approval = json.loads(config_path.read_text(encoding="utf-8"))
    realized_core = json.loads(
        harness.layout.host(setup.CORE_CONFIG).read_text(encoding="utf-8")
    )
    realized_core.pop("enrolled_harness_id", None)
    realized_core.pop("enrolled_credential_id", None)
    assert setup.canonical_digest(realized_approval) != marker[
        "approval_config_digest"
    ]
    assert setup.canonical_digest(realized_core) != marker["core_config_digest"]
    assert (
        "communication_scope_request_ttl_seconds" in realized_approval
    ) is communication_scope_ttl_present
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    original_run_as = setup._run_as

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            harness.operation_events.append(("product", stage))
            return {"status": "already_exact"}
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_run_as", run_as)
    monkeypatch.setattr(
        setup,

        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {
            "actor": {
                "principal_id": harness.request.approval_approver_principal_id
            }
        },
    )

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    assert config_path.read_bytes() != repaired_payload
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert {
        "id": "approval_request_ttl_policy_upgrade",
        "status": "updated_package_upgrade",
    } in result["steps"]
    assert {
        "id": "canonical_owner_recovery",
        "status": "already_exact",
        "source_principal_id": "setup-placeholder-owner",
        "target_principal_id": harness.request.approval_approver_principal_id,
        "core_policy_status": "already_satisfied",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    "reverse_embedded_trust",
    (False, True),
    ids=("source-target-trust", "target-source-trust"),
)
@pytest.mark.parametrize(
    "interrupt_before_recovery",
    (False, True),
    ids=("single-pass", "resume-after-pre-recovery-rollback"),
)
def test_0151_upgrade_converges_exact_partial_owner_repair(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    interrupt_before_recovery: bool,
    reverse_embedded_trust: bool,
) -> None:
    real_approval_trust = setup._approval_trust
    real_require_exact_approval_policy = setup._require_exact_approval_policy
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, recovery_request = (
        _stage_0150_partial_owner_repair_and_one_hour_hotfix(
            harness,
            reverse_embedded_trust=reverse_embedded_trust,
        )
    )
    if reverse_embedded_trust:
        approval = json.loads(config_path.read_text(encoding="utf-8"))
        approval["approvers"][0]["allowed_purposes"].reverse()
        _private_json(config_path, approval)
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    marker_before = harness.marker_path.read_bytes()
    assert not recovery_path.exists()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    monkeypatch.setattr(
        setup,
        "_require_exact_approval_policy",
        real_require_exact_approval_policy,
    )
    original_run_as = setup._run_as
    recovery_attempts: list[str] = []

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            recovery_attempts.append(stage)
            if interrupt_before_recovery and len(recovery_attempts) == 1:
                raise RuntimeError("injected pre-recovery interruption")
            approval = json.loads(config_path.read_text(encoding="utf-8"))
            store = ApprovalStore(
                Path(approval["database_path"]),
                LocalEnvelopeCipher(
                    Path(approval["record_key_path"]).read_bytes()
                ),
            )
            try:
                return converge_canonical_approval_owner(
                    store,
                    config_path=config_path,
                    journal_path=recovery_path,
                    request=recovery_request,
                    now=2,
                )
            finally:
                store.close()
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_run_as", run_as)
    monkeypatch.setattr(
        setup,
        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {
            "actor": {
                "principal_id": harness.request.approval_approver_principal_id
            }
        },
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")

    if interrupt_before_recovery:
        with pytest.raises(
            RuntimeError,
            match="injected pre-recovery interruption",
        ):
            harness.apply(harness.plan_digest())
        interrupted = json.loads(recovery_path.read_text(encoding="utf-8"))
        migrated_ttl = json.loads(config_path.read_text(encoding="utf-8"))
        assert interrupted["phase"] == "prepared"
        assert migrated_ttl["request_ttl_seconds"] == 3_600
        assert "communication_scope_request_ttl_seconds" not in migrated_ttl
        assert harness.marker_path.read_bytes() == marker_before
        assert not harness.journal_path.exists()
    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    recovery = json.loads(recovery_path.read_text(encoding="utf-8"))
    assert marker_before != harness.marker_path.read_bytes()
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert migrated["approvers"][0]["principal_id"] == (
        harness.request.approval_approver_principal_id
    )
    assert recovery["phase"] == "complete"
    assert recovery["partial_recovery"]["schema"] == (
        "agentnet.canonical-owner-partial-recovery.v1"
    )
    assert {
        "id": "canonical_owner_recovery",
        "status": "recovered",
        "source_principal_id": "setup-placeholder-owner",
        "target_principal_id": harness.request.approval_approver_principal_id,
        "core_policy_status": "updated_package_upgrade",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    "tamper",
    (
        "extra-signer",
        "embedded-target-only",
        "reordered-source-authority",
        "sidecar-source",
    ),
)
def test_0151_upgrade_rejects_near_partial_owner_repair_without_writes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    tamper: str,
) -> None:
    real_approval_trust = setup._approval_trust
    real_require_exact_approval_policy = setup._require_exact_approval_policy
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    approval_path, _ = _stage_0150_partial_owner_repair_and_one_hour_hotfix(
        harness
    )
    core_path = harness.layout.host(setup.CORE_CONFIG)
    core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    if tamper == "extra-signer":
        extra = recovery_path.parent / "signers" / "untracked.pem"
        extra.write_bytes(P256KeyPair.generate().private_pem)
        extra.chmod(0o600)
    elif tamper == "embedded-target-only":
        core = json.loads(core_path.read_text(encoding="utf-8"))
        core["oidc_enrollment"]["trusted_approvers"] = [
            core["oidc_enrollment"]["trusted_approvers"][1]
        ]
        _private_json(core_path, core)
    elif tamper == "reordered-source-authority":
        core = json.loads(core_path.read_text(encoding="utf-8"))
        core["oidc_enrollment"]["trusted_approvers"].reverse()
        core["oidc_enrollment"]["trusted_approvers"][1][
            "authority_kind"
        ] = "guest"
        _private_json(core_path, core)
    else:
        core = json.loads(core_path.read_text(encoding="utf-8"))
        source_oidc = copy.deepcopy(core["oidc_enrollment"])
        source_oidc["trusted_approvers"] = [
            source_oidc["trusted_approvers"][0]
        ]
        source_oidc["approval_service"]["approver_principal_id"] = (
            "setup-placeholder-owner"
        )
        _private_json(core_oidc_path, source_oidc)

    protected_paths = (
        approval_path,
        core_path,
        core_oidc_path,
        harness.marker_path,
    )
    before = {path: path.read_bytes() for path in protected_paths}
    signer_root = recovery_path.parent / "signers"
    signers_before = {
        path.name: path.read_bytes() for path in sorted(signer_root.iterdir())
    }
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    monkeypatch.setattr(
        setup,
        "_require_exact_approval_policy",
        real_require_exact_approval_policy,
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker in {
        "canonical_owner_recovery",
        "setup_upgrade_conflict",
    }
    assert {path: path.read_bytes() for path in protected_paths} == before
    assert {
        path.name: path.read_bytes() for path in sorted(signer_root.iterdir())
    } == signers_before
    assert not recovery_path.exists()
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    "purpose_order_rewritten",
    (False, True),
    ids=("original-purpose-order", "reserialized-purpose-order"),
)
@pytest.mark.parametrize(
    "communication_scope_ttl_present",
    (False, True),
    ids=("scope-ttl-absent", "scope-ttl-explicit"),
)
def test_0151_upgrade_reconstructs_journalless_completed_owner_repair(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    communication_scope_ttl_present: bool,
    purpose_order_rewritten: bool,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, repaired_payload = (
        _stage_0150_completed_owner_repair_and_one_hour_hotfix(
            harness,
            include_journal=False,
            communication_scope_ttl_present=communication_scope_ttl_present,
        )
    )
    if purpose_order_rewritten:
        _rewrite_repaired_policy_purpose_order(harness, config_path)
        repaired_payload = config_path.read_bytes()
    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    recovery_path = approval_state / "canonical-owner-recovery.json"
    assert not recovery_path.exists()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    original_run_as = setup._run_as

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            harness.operation_events.append(("product", stage))
            return {"status": "already_exact"}
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_run_as", run_as)
    monkeypatch.setattr(
        setup,
        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {
            "actor": {
                "principal_id": harness.request.approval_approver_principal_id
            }
        },
    )

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    reconstructed = json.loads(recovery_path.read_text(encoding="utf-8"))
    assert config_path.read_bytes() != repaired_payload
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert reconstructed["phase"] == "complete"
    assert reconstructed["reconstruction"]["schema"] == (
        "agentnet.canonical-owner-recovery-reconstruction.v1"
    )
    assert {
        "id": "canonical_owner_recovery",
        "status": "already_exact",
        "source_principal_id": "setup-placeholder-owner",
        "target_principal_id": harness.request.approval_approver_principal_id,
        "core_policy_status": "already_satisfied",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    "tamper_after_upgrade_journal",
    (
        None,
        "core",
        "core_oidc",
        "binding",
        "extra_signer",
        "source_signer",
    ),
)
def test_0151_resume_revalidates_reconstructed_journal_before_ttl_migration(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    tamper_after_upgrade_journal: str | None,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, repaired_payload = (
        _stage_0150_completed_owner_repair_and_one_hour_hotfix(
            harness,
            include_journal=False,
        )
    )
    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    recovery_path = approval_state / "canonical-owner-recovery.json"
    marker_before = harness.marker_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    original_run_as = setup._run_as

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            harness.operation_events.append(("product", stage))
            return {"status": "already_exact"}
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_run_as", run_as)
    monkeypatch.setattr(
        setup,
        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {
            "actor": {
                "principal_id": harness.request.approval_approver_principal_id
            }
        },
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    real_atomic_write = setup._atomic_write
    real_write_upgrade_journal = setup._write_upgrade_journal
    interrupted = False

    def interrupt_after_reconstruction(
        path: Path,
        payload: bytes,
        *,
        mode: int,
        uid: int,
        gid: int,
    ) -> str:
        nonlocal interrupted
        result = real_atomic_write(
            path,
            payload,
            mode=mode,
            uid=uid,
            gid=gid,
        )
        if path == recovery_path and not interrupted:
            interrupted = True
            raise RuntimeError("injected reconstructed-journal process loss")
        return result

    monkeypatch.setattr(setup, "_atomic_write", interrupt_after_reconstruction)
    with pytest.raises(
        RuntimeError,
        match="injected reconstructed-journal process loss",
    ):
        harness.apply(harness.plan_digest())

    assert interrupted
    assert recovery_path.exists()
    assert harness.marker_path.read_bytes() == marker_before
    assert config_path.read_bytes() == repaired_payload

    monkeypatch.setattr(setup, "_atomic_write", real_atomic_write)
    if tamper_after_upgrade_journal is not None:
        interrupted = False

        def interrupt_after_upgrade_journal(
            path: Path,
            journal: dict[str, object],
            *,
            uid: int,
            gid: int,
        ) -> None:
            nonlocal interrupted
            real_write_upgrade_journal(path, journal, uid=uid, gid=gid)
            if path == harness.journal_path and not interrupted:
                interrupted = True
                raise RuntimeError("injected setup-upgrade journal process loss")

        monkeypatch.setattr(
            setup,
            "_write_upgrade_journal",
            interrupt_after_upgrade_journal,
        )
        with pytest.raises(
            RuntimeError,
            match="injected setup-upgrade journal process loss",
        ):
            harness.apply(harness.plan_digest())
        assert interrupted
        assert harness.journal_path.exists()
        assert config_path.read_bytes() == repaired_payload
        if tamper_after_upgrade_journal == "core":
            core_path = harness.layout.host(setup.CORE_CONFIG)
            core = json.loads(core_path.read_text(encoding="utf-8"))
            core["oidc_enrollment"]["client_id"] = "unrelated-client"
            _private_json(core_path, core)
        elif tamper_after_upgrade_journal == "core_oidc":
            core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
            core_oidc = json.loads(core_oidc_path.read_text(encoding="utf-8"))
            core_oidc["client_id"] = "unrelated-client"
            _private_json(core_oidc_path, core_oidc)
        elif tamper_after_upgrade_journal == "binding":
            approval = json.loads(config_path.read_text(encoding="utf-8"))
            store = ApprovalStore(
                Path(approval["database_path"]),
                LocalEnvelopeCipher(
                    Path(approval["record_key_path"]).read_bytes()
                ),
            )
            try:
                with store.transaction() as connection:
                    connection.execute(
                        """UPDATE approval_owner_bindings
                              SET pinned_at=pinned_at+1
                            WHERE domain_id=? AND status='active'""",
                        (harness.request.domain_id,),
                    )
            finally:
                store.close()
        elif tamper_after_upgrade_journal in {"extra_signer", "source_signer"}:
            approval = json.loads(config_path.read_text(encoding="utf-8"))
            target_signer_path = Path(
                approval["approvers"][0]["signer_private_key_path"]
            )
            if tamper_after_upgrade_journal == "extra_signer":
                changed_signer_path = target_signer_path.with_name(
                    "unrelated-owner.pem"
                )
            else:
                changed_signer_path = next(
                    path
                    for path in target_signer_path.parent.iterdir()
                    if path != target_signer_path
                )
            changed_signer_path.write_bytes(P256KeyPair.generate().private_pem)
            changed_signer_path.chmod(0o600)
        monkeypatch.setattr(
            setup,
            "_write_upgrade_journal",
            real_write_upgrade_journal,
        )
        with pytest.raises(ServerSetupError) as exc_info:
            harness.apply(harness.plan_digest())
        assert exc_info.value.blocker == (
            "setup_upgrade_conflict"
            if tamper_after_upgrade_journal in {"core", "core_oidc"}
            else "canonical_owner_recovery"
        )
        assert config_path.read_bytes() == repaired_payload
        assert harness.marker_path.read_bytes() == marker_before
        return

    result = harness.apply(harness.plan_digest())

    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    reconstructed = json.loads(recovery_path.read_text(encoding="utf-8"))
    assert reconstructed["phase"] == "complete"
    assert reconstructed["reconstruction"]["schema"] == (
        "agentnet.canonical-owner-recovery-reconstruction.v1"
    )
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert {
        "id": "canonical_owner_recovery",
        "status": "already_exact",
        "source_principal_id": "setup-placeholder-owner",
        "target_principal_id": harness.request.approval_approver_principal_id,
        "core_policy_status": "already_satisfied",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    "tamper",
    (
        "extra_signer",
        "source_signer",
        "approval_purpose",
        "core_purpose",
        "marker_approval_digest",
        "marker_core_digest",
        "core_oidc_sidecar",
        "core_oidc_sidecar_duplicate_purpose",
        "core_policy",
        "source_core",
        "source_core_with_journal",
        "binding_pinned_at",
        "unit",
    ),
)
def test_0151_journalless_reconstruction_rejects_ambiguous_evidence(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    tamper: str,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, repaired_payload = (
        _stage_0150_completed_owner_repair_and_one_hour_hotfix(
            harness,
            include_journal=tamper == "source_core_with_journal",
            current_core_target=not tamper.startswith("source_core"),
        )
    )
    approval = json.loads(config_path.read_text(encoding="utf-8"))
    target_signer_path = Path(
        approval["approvers"][0]["signer_private_key_path"]
    )
    source_signer_path = next(
        (
            path
            for path in target_signer_path.parent.iterdir()
            if path != target_signer_path
        ),
        None,
    )
    expected_approval_payload = repaired_payload
    if tamper == "extra_signer":
        assert source_signer_path is not None
        extra_signer_path = target_signer_path.with_name("unrelated-owner.pem")
        extra_signer_path.write_bytes(P256KeyPair.generate().private_pem)
        extra_signer_path.chmod(0o600)
    elif tamper == "source_signer":
        assert source_signer_path is not None
        source_signer_path.write_bytes(P256KeyPair.generate().private_pem)
        source_signer_path.chmod(0o600)
    elif tamper == "approval_purpose":
        approval["approvers"][0]["allowed_purposes"].append(
            "unrelated.approve"
        )
        _private_json(config_path, approval)
        expected_approval_payload = config_path.read_bytes()
    elif tamper == "core_purpose":
        expected_approval_payload = config_path.read_bytes()
        core_path = harness.layout.host(setup.CORE_CONFIG)
        core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
        core = json.loads(core_path.read_text(encoding="utf-8"))
        core["oidc_enrollment"]["trusted_approvers"][0][
            "allowed_purposes"
        ].append("unrelated.approve")
        _private_json(core_path, core)
        _private_json(core_oidc_path, core["oidc_enrollment"])
    elif tamper == "marker_approval_digest":
        marker = harness.marker()
        marker["approval_config_digest"] = "a" * 64
        _private_json(harness.marker_path, marker)
    elif tamper == "marker_core_digest":
        marker = harness.marker()
        marker["core_config_digest"] = "a" * 64
        _private_json(harness.marker_path, marker)
    elif tamper == "core_oidc_sidecar":
        core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
        core_oidc = json.loads(core_oidc_path.read_text(encoding="utf-8"))
        core_oidc["client_id"] = "unrelated-client"
        _private_json(core_oidc_path, core_oidc)
    elif tamper == "core_oidc_sidecar_duplicate_purpose":
        core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
        core_oidc = json.loads(core_oidc_path.read_text(encoding="utf-8"))
        core_oidc["trusted_approvers"][0]["allowed_purposes"].append(
            core_oidc["trusted_approvers"][0]["allowed_purposes"][0]
        )
        _private_json(core_oidc_path, core_oidc)
    elif tamper == "core_policy":
        core_path = harness.layout.host(setup.CORE_CONFIG)
        core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
        core = json.loads(core_path.read_text(encoding="utf-8"))
        core["oidc_enrollment"]["client_id"] = "unrelated-client"
        _private_json(core_path, core)
        _private_json(core_oidc_path, core["oidc_enrollment"])
    elif tamper == "binding_pinned_at":
        store = ApprovalStore(
            Path(approval["database_path"]),
            LocalEnvelopeCipher(Path(approval["record_key_path"]).read_bytes()),
        )
        try:
            with store.transaction() as connection:
                connection.execute(
                    """UPDATE approval_owner_bindings SET pinned_at=pinned_at+1
                        WHERE domain_id=? AND status='active'""",
                    (harness.request.domain_id,),
                )
        finally:
            store.close()
    elif tamper == "unit":
        unit_path = harness.layout.unit(setup.CORE_UNIT)
        unit_path.write_bytes(unit_path.read_bytes() + b"\n# unrelated drift\n")
    elif not tamper.startswith("source_core"):
        raise AssertionError(tamper)

    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == (
        "setup_upgrade_conflict"
        if tamper == "unit"
        else "canonical_owner_recovery"
    )
    assert config_path.read_bytes() == expected_approval_payload
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    assert recovery_path.exists() is (tamper == "source_core_with_journal")


def test_0151_rejects_tampered_reconstructed_marker_evidence(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_completed_owner_repair_and_one_hour_hotfix(
        harness,
        include_journal=False,
    )
    account = pwd.getpwuid(os.geteuid())
    approval_state = harness.layout.host(setup.APPROVAL_STATE)
    recovery_path = approval_state / "canonical-owner-recovery.json"
    reconstructed = getattr(
        setup,
        "_reconstruct_completed_canonical_owner_recovery_for_marker",
    )(
        harness.marker(),
        approval_state,
        config_path,
        account,
        harness.layout.host(setup.CORE_CONFIG),
        harness.layout.host(setup.CORE_OIDC_CONFIG),
        account,
        request=harness.request,
        observed_at=42,
        before_write=lambda: None,
    )
    assert reconstructed is not None
    journal = json.loads(recovery_path.read_text(encoding="utf-8"))
    journal["reconstruction"]["marker_core_config_digest"] = "a" * 64
    _private_json(recovery_path, journal)
    monkeypatch.setattr(setup, "__version__", "0.1.51")

    with pytest.raises(ServerSetupError) as exc_info:
        getattr(setup, "_upgrade_marker_config_digests")(
            harness.marker(),
            approval_config_path=config_path,
            approval_account=account,
            approval_state=approval_state,
            core_config_path=harness.layout.host(setup.CORE_CONFIG),
            core_account=account,
            core_oidc_path=harness.layout.host(setup.CORE_OIDC_CONFIG),
            request=harness.request,
        )

    assert exc_info.value.blocker == "canonical_owner_recovery"




@pytest.mark.parametrize(
    ("tamper", "expected_blocker"),
    (
        ("journal_target", "canonical_owner_recovery"),
        ("journal_request_digest", "canonical_owner_recovery"),
        ("journal_oidc_issuer", "canonical_owner_recovery"),
        ("journal_signer_path", "canonical_owner_recovery"),
        ("journal_target_signer_path", "canonical_owner_recovery"),
        ("journal_source_config_sha256", "canonical_owner_recovery"),
        ("binding_verified_email", "canonical_owner_recovery"),
        ("binding_pinned_at", "canonical_owner_recovery"),
        ("binding_second_active", "canonical_owner_recovery"),
        ("target_credential_removed", "canonical_owner_recovery"),
        ("source_credential_active", "canonical_owner_recovery"),
        ("adoption_audit_removed", "canonical_owner_recovery"),
        ("approval_policy", "canonical_owner_recovery"),
        ("core_policy", "canonical_owner_recovery"),
    ),
)
def test_0151_rejects_combined_recovery_evidence_or_state_drift(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    tamper: str,
    expected_blocker: str,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_completed_owner_repair_and_one_hour_hotfix(
        harness
    )
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    if tamper == "journal_target":
        recovery = json.loads(recovery_path.read_text(encoding="utf-8"))
        recovery["target_principal_id"] = "unrelated-owner"
        _private_json(recovery_path, recovery)
    elif tamper.startswith("journal_"):
        recovery = json.loads(recovery_path.read_text(encoding="utf-8"))
        journal_field = tamper.removeprefix("journal_")
        recovery[journal_field] = (
            "b" * 64
            if journal_field in {"request_digest", "source_config_sha256"}
            else str(tmp_path / "unrelated.pem")
            if journal_field in {"signer_path", "target_signer_path"}
            else "https://unrelated.example"
        )
        _private_json(recovery_path, recovery)
    elif tamper.startswith("binding_") or tamper in {
        "target_credential_removed",
        "source_credential_active",
        "adoption_audit_removed",
    }:
        approval = json.loads(config_path.read_text(encoding="utf-8"))
        record_key = Path(approval["record_key_path"]).read_bytes()
        store = ApprovalStore(
            Path(approval["database_path"]),
            LocalEnvelopeCipher(record_key),
        )
        try:
            with store.transaction() as connection:
                if tamper == "binding_verified_email":
                    connection.execute(
                        "UPDATE approval_owner_bindings SET verified_email=?",
                        ("other-owner@corp.example",),
                    )
                elif tamper == "binding_pinned_at":
                    connection.execute(
                        "UPDATE approval_owner_bindings SET pinned_at=?",
                        (2,),
                    )
                elif tamper == "binding_second_active":
                    connection.execute(
                        """INSERT INTO approval_owner_bindings(
                               binding_id,domain_id,approver_principal_id,oidc_issuer,
                               oidc_subject,verified_email,pin_source,status,pinned_at,
                               revoked_at,revocation_reason
                           ) VALUES(?,?,?,'https://other.example','other-subject',
                                    'other-owner@corp.example','exact_subject','active',
                                    2,NULL,NULL)""",
                        (
                            "ambiguous-owner-binding",
                            harness.request.domain_id,
                            "ambiguous-owner",
                        ),
                    )
                elif tamper == "target_credential_removed":
                    connection.execute(
                        """DELETE FROM approval_webauthn_credentials
                           WHERE approver_principal_id=? AND domain_id=?
                             AND status='active'""",
                        (
                            harness.request.approval_approver_principal_id,
                            harness.request.domain_id,
                        ),
                    )
                elif tamper == "source_credential_active":
                    connection.execute(
                        """INSERT INTO approval_webauthn_credentials(
                               credential_id_b64,approver_principal_id,domain_id,
                               user_handle_b64,credential_public_key_b64,sign_count,
                               device_type,backed_up,status,created_at,revoked_at,
                               revocation_reason
                           )
                           SELECT 'stale-source-credential','setup-placeholder-owner',
                                  domain_id,user_handle_b64,credential_public_key_b64,
                                  sign_count,device_type,backed_up,status,created_at,
                                  revoked_at,revocation_reason
                           FROM approval_webauthn_credentials
                           WHERE approver_principal_id=? AND domain_id=?
                             AND status='active' LIMIT 1""",
                        (
                            harness.request.approval_approver_principal_id,
                            harness.request.domain_id,
                        ),
                    )
                else:
                    connection.execute(
                        """DELETE FROM approval_audit
                           WHERE action='owner.canonical_adoption'"""
                    )
        finally:
            store.close()
    elif tamper == "approval_policy":
        approval = json.loads(config_path.read_text(encoding="utf-8"))
        approval["rp_name"] = "Unrelated Approval"
        _private_json(config_path, approval)
    else:
        core = json.loads(core_config_path.read_text(encoding="utf-8"))
        core["oidc_enrollment"]["client_id"] = "unrelated-client"
        core_config_path.write_text(
            json.dumps(core, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        core_config_path.chmod(0o600)
    marker_before = harness.marker_path.read_bytes()
    approval_before = config_path.read_bytes()
    core_before = core_config_path.read_bytes()
    recovery_before = recovery_path.read_bytes()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == expected_blocker
    assert harness.marker_path.read_bytes() == marker_before
    assert config_path.read_bytes() == approval_before
    assert core_config_path.read_bytes() == core_before
    assert recovery_path.read_bytes() == recovery_before
    assert not harness.journal_path.exists()


def test_0151_rejects_completed_owner_repair_without_retained_one_hour_hotfix(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_completed_owner_repair_and_one_hour_hotfix(
        harness
    )
    approval = json.loads(config_path.read_text(encoding="utf-8"))
    approval["request_ttl_seconds"] = 300
    _private_json(config_path, approval)
    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    marker_before = harness.marker_path.read_bytes()
    approval_before = config_path.read_bytes()
    core_before = core_config_path.read_bytes()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.marker_path.read_bytes() == marker_before
    assert config_path.read_bytes() == approval_before
    assert core_config_path.read_bytes() == core_before
    assert not harness.journal_path.exists()


def test_0151_rejects_unrecorded_approval_drift_beyond_one_hour_hotfix(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_one_hour_approval_hotfix(harness)
    drifted_document = json.loads(config_path.read_text(encoding="utf-8"))
    published_document = dict(drifted_document)
    published_document["request_ttl_seconds"] = 300
    _private_json(config_path, published_document)
    marker = harness.marker()
    marker["approval_config_digest"] = setup._managed_config_digest(
        config_path,
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        blocker="approval_config",
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    drifted_document["receipt_ttl_seconds"] = 301
    _private_json(config_path, drifted_document)
    source_payload = config_path.read_bytes()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert config_path.read_bytes() == source_payload
    assert harness.marker()["package_version"] == "0.1.50"
    assert not harness.journal_path.exists()



def test_0151_rejects_one_hour_approval_ttl_from_other_source_release(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.49")
    harness.apply(harness.plan_digest())
    config_path, source_payload = _stage_0150_one_hour_approval_hotfix(harness)
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "approval_config"
    assert config_path.read_bytes() == source_payload
    assert harness.marker()["package_version"] == "0.1.49"
    assert not harness.journal_path.exists()


def test_0151_preserves_published_0150_approval_ttl_policy(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_one_hour_approval_hotfix(harness)
    source = json.loads(config_path.read_text(encoding="utf-8"))
    source["request_ttl_seconds"] = 300
    _private_json(config_path, source)
    marker = harness.marker()
    marker["approval_config_digest"] = setup._managed_config_digest(
        config_path,
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        blocker="approval_config",
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    source_payload = config_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    result = harness.apply(harness.plan_digest())

    assert config_path.read_bytes() == source_payload
    assert {
        "id": "approval_request_ttl_policy_upgrade",
        "status": "already_satisfied",
    } in result["steps"]
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


def test_0151_rejects_already_shortened_ttl_as_upgrade_source(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_one_hour_approval_hotfix(harness)
    source = json.loads(config_path.read_text(encoding="utf-8"))
    source["request_ttl_seconds"] = 600
    _private_json(config_path, source)
    marker = harness.marker()
    marker["approval_config_digest"] = setup._managed_config_digest(
        config_path,
        SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
        blocker="approval_config",
    )
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.marker_path.chmod(0o600)
    source_payload = config_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "approval_config"
    assert config_path.read_bytes() == source_payload
    assert harness.marker()["package_version"] == "0.1.50"
    assert not harness.journal_path.exists()


def test_0151_approval_ttl_migration_rolls_back_before_marker_commit(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, source_payload = _stage_0150_one_hour_approval_hotfix(harness)
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected post-TTL-migration failure")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected post-TTL-migration failure"):
        harness.apply(harness.plan_digest())

    assert config_path.read_bytes() == source_payload
    assert harness.marker()["package_version"] == "0.1.50"
    assert not harness.journal_path.exists()


def test_0151_approval_ttl_migration_resumes_after_process_loss(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, source_payload = _stage_0150_one_hour_approval_hotfix(harness)
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    original_commit = setup._commit_setup_marker
    original_rollback = setup._rollback_pending_upgrade
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", lambda _pending: None)
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            RuntimeError("injected post-TTL-migration process loss")
        ),
    )

    with pytest.raises(RuntimeError, match="injected post-TTL-migration process loss"):
        harness.apply(harness.plan_digest())

    assert config_path.read_bytes() != source_payload
    journal = json.loads(harness.journal_path.read_text(encoding="utf-8"))
    assert journal["schema"] == "agentnet.server-setup.upgrade-journal.v3"
    assert set(journal["previous_configs"]) == {
        "approval_config",
        "core_config",
        "core_oidc_config",
    }

    monkeypatch.setattr(setup, "_rollback_pending_upgrade", original_rollback)
    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    resumed = harness.apply(harness.plan_digest())

    assert {
        "id": "package_upgrade",
        "status": "resumed_journaled_upgrade",
    } in resumed["steps"]
    migrated = json.loads(config_path.read_text(encoding="utf-8"))
    assert migrated["request_ttl_seconds"] == 600
    assert migrated["communication_scope_request_ttl_seconds"] == 3_600
    assert harness.marker()["package_version"] == "0.1.51"
    assert not harness.journal_path.exists()


def test_0151_resume_revalidates_owner_recovery_before_ttl_migration(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    real_approval_trust = setup._approval_trust
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.50")
    harness.apply(harness.plan_digest())
    config_path, _ = _stage_0150_completed_owner_repair_and_one_hour_hotfix(
        harness
    )
    recovery_path = (
        harness.layout.host(setup.APPROVAL_STATE)
        / "canonical-owner-recovery.json"
    )
    recovery_before = recovery_path.read_bytes()
    monkeypatch.setattr(setup, "_approval_trust", real_approval_trust)
    original_run_as = setup._run_as

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            return {"status": "already_exact"}
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_run_as", run_as)
    monkeypatch.setattr(
        setup,
        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {
            "actor": {
                "principal_id": harness.request.approval_approver_principal_id
            }
        },
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    original_commit = setup._commit_setup_marker
    original_rollback = setup._rollback_pending_upgrade
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", lambda _pending: None)
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            RuntimeError("injected combined-recovery process loss")
        ),
    )

    with pytest.raises(RuntimeError, match="injected combined-recovery process loss"):
        harness.apply(harness.plan_digest())

    assert harness.journal_path.exists()
    approval = json.loads(config_path.read_text(encoding="utf-8"))
    store = ApprovalStore(
        Path(approval["database_path"]),
        LocalEnvelopeCipher(Path(approval["record_key_path"]).read_bytes()),
    )
    try:
        with store.transaction() as connection:
            connection.execute(
                "UPDATE approval_owner_bindings SET verified_email=?",
                ("drifted-owner@corp.example",),
            )
    finally:
        store.close()
    real_migrate = getattr(setup, "_migrate_0150_approval_request_ttl_policy")
    migrations: list[bool] = []

    def tracked_migrate(**kwargs: object) -> str:
        migrations.append(True)
        return real_migrate(**kwargs)  # type: ignore[arg-type]

    monkeypatch.setattr(
        setup,
        "_migrate_0150_approval_request_ttl_policy",
        tracked_migrate,
    )
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", original_rollback)
    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "canonical_owner_recovery"
    assert migrations == []
    assert recovery_path.read_bytes() == recovery_before
    assert harness.marker()["package_version"] == "0.1.50"


def test_0151_rejects_direct_upgrade_from_pre_lifecycle_release(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.44",
        artifact_mode="disabled",
    )

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )

    assert exc_info.value.blocker == "setup_marker_conflict"

@pytest.mark.parametrize(
    ("package_version", "current_version"),
    [
        ("0.1.29", "0.1.31"),  # never released with a runtime-bound digest
        ("0.1.31", "0.1.31"),  # same version: the request itself changed
        ("0.1.32", "0.1.31"),  # downgrade
        ("garbage", "0.1.31"),
        ("0.1.30", "0.1.32"),  # unsupported upgrade target
        ("0.1.30", "0.1.30"),
    ],
)
def test_marker_rejects_every_unsupported_request_digest_drift(
    monkeypatch: pytest.MonkeyPatch,
    package_version: str,
    current_version: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", current_version)
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version=package_version,
        artifact_mode="disabled",
    )
    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


def test_marker_upgrade_still_rejects_malformed_recorded_digest(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.30",
        artifact_mode="disabled",
        request_digest="not-a-digest",
    )
    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


@pytest.mark.parametrize(
    ("artifact_mode", "units"),
    [
        ("enabled", setup.LEGACY_COMMUNICATION_ONLY_UNITS),
        ("disabled", setup.MANAGED_UNITS),
        ("disabled", (setup.CORE_UNIT, setup.APPROVAL_UNIT)),
    ],
)
def test_0131_topology_upgrade_accepts_only_the_exact_released_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
    units: tuple[str, ...],
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    value = json.loads(
        _marker_payload(
            schema="agentnet.server-setup.marker.v3",
            package_version="0.1.31",
            artifact_mode=artifact_mode,
        )
    )
    value["units"] = list(units)
    value["unit_digests"] = {unit: "4" * 64 for unit in units}

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n",
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


def test_0131_topology_upgrade_accepts_exact_two_unit_communication_marker(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    value = json.loads(
        _marker_payload(
            schema="agentnet.server-setup.marker.v3",
            package_version="0.1.31",
            artifact_mode="disabled",
        )
    )
    value["units"] = list(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    value["unit_digests"] = {
        unit: "4" * 64 for unit in setup.LEGACY_COMMUNICATION_ONLY_UNITS
    }

    marker = setup._validated_setup_marker(
        json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n",
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode="disabled",
    )

    assert marker is not None
    assert marker["units"] == list(setup.LEGACY_COMMUNICATION_ONLY_UNITS)


@pytest.mark.parametrize("revision", [True, False])
def test_setup_marker_rejects_boolean_revision(
    monkeypatch: pytest.MonkeyPatch,
    revision: bool,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    value = json.loads(
        _marker_payload(
            schema="agentnet.server-setup.marker.v3",
            package_version="0.1.31",
            artifact_mode="disabled",
        )
    )
    value["revision"] = revision
    value["units"] = list(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    value["unit_digests"] = {
        unit: "4" * 64 for unit in setup.LEGACY_COMMUNICATION_ONLY_UNITS
    }

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n",
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0132_upgrade_accepts_exact_released_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.32",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0137_upgrade_accepts_exact_released_0133_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.37")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.33",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0138_upgrade_accepts_exact_released_0137_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.38")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.37",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


def test_0139_fresh_setup_rejects_0138_release_marker(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.39")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.38",
        artifact_mode="disabled",
    )

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0140_upgrade_accepts_exact_0139_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.40")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.39",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0141_upgrade_accepts_exact_0140_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.41")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.40",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0142_upgrade_accepts_exact_0141_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.42")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.41",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0146_upgrade_accepts_exact_0145_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.46")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.45",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


def test_0147_upgrade_accepts_exact_0146_v2_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.47")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v2",
        package_version="0.1.46",
        artifact_mode=None,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=None,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0148_upgrade_accepts_exact_0147_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.48")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.47",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_0149_upgrade_accepts_exact_0148_five_unit_profile(
    monkeypatch: pytest.MonkeyPatch,
    artifact_mode: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.49")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version="0.1.48",
        artifact_mode=artifact_mode,
    )

    marker = setup._validated_setup_marker(
        payload,
        request_digest="9" * 64,
        legacy_request_digest="1" * 64,
        artifact_mode=artifact_mode,
    )

    assert marker is not None
    assert marker["units"] == list(setup.MANAGED_UNITS)


@pytest.mark.parametrize("target_matches_identity", [True, False])
def test_0151_upgrade_converges_placeholder_approval_owner(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    target_matches_identity: bool,
) -> None:
    harness = _harness(tmp_path, monkeypatch)

    source_principal = "setup-placeholder-owner"
    setup_request_path = tmp_path / "setup.json"
    approvers_path = tmp_path / "approvers.json"
    target_request_document = json.loads(setup_request_path.read_text(encoding="utf-8"))
    target_approvers_document = json.loads(approvers_path.read_text(encoding="utf-8"))
    source_request_document = copy.deepcopy(target_request_document)
    source_approvers_document = copy.deepcopy(target_approvers_document)
    source_request_document["approval_approver_principal_id"] = source_principal
    source_approvers_document["approvers"][0]["principal_id"] = source_principal
    _private_json(setup_request_path, source_request_document)
    _private_json(approvers_path, source_approvers_document)
    harness.request = load_server_setup_request(setup_request_path)
    source_signer = P256KeyPair.generate()
    target_signer = P256KeyPair.generate()
    recovered = {"value": False}
    approval_config = SimpleNamespace()

    def approval_trust(*_args: object, **_kwargs: object) -> tuple[object, list[IndependentApproverConfig]]:
        signer = target_signer if recovered["value"] else source_signer
        principal = (
            harness.request.approval_approver_principal_id
            if recovered["value"]
            else source_principal
        )
        return approval_config, [
            IndependentApproverConfig(
                principal_id=principal,
                authority_kind="human",
                signer_key_id=signer.thumbprint,
                public_key_pem=signer.public_pem,
                allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
            )
        ]
    monkeypatch.setattr(setup, "_approval_trust", approval_trust)
    monkeypatch.setattr(setup, "__version__", "0.1.49")
    harness.apply(harness.plan_digest())
    _private_json(setup_request_path, target_request_document)
    _private_json(approvers_path, target_approvers_document)
    harness.request = load_server_setup_request(setup_request_path)

    def require_policy(
        *_args: object,
        allow_canonical_owner_adoption: bool = False,
        **_kwargs: object,
    ) -> str | None:
        if recovered["value"]:
            return None
        if allow_canonical_owner_adoption:
            return source_principal
        raise ServerSetupError("approval_conflict", "placeholder owner remains active")

    seen: dict[str, object] = {}
    original_run_as = setup._run_as

    def run_as(
        account: pwd.struct_passwd,
        argv: list[str],
        *,
        environment: dict[str, str],
        stage: str,
        accepted_returncodes: frozenset[int] = frozenset({0}),
    ) -> dict[str, object]:
        if argv[2:4] == ["approval", "recover-canonical-owner"]:
            seen.update(
                {
                    "account": account,
                    "argv": argv,
                    "environment": environment,
                    "stage": stage,
                }
            )
            harness.operation_events.append(("product", stage))
            recovered["value"] = True
            return {"status": "recovered"}
        return original_run_as(
            account,
            argv,
            environment=environment,
            stage=stage,
            accepted_returncodes=accepted_returncodes,
        )

    monkeypatch.setattr(setup, "_approval_trust", approval_trust)
    monkeypatch.setattr(setup, "_require_exact_approval_policy", require_policy)
    monkeypatch.setattr(setup, "_run_as", run_as)
    def migrate_core_policy(
        *,
        core_config_path: Path,
        core_oidc_path: Path,
        core_account: object,
        source_oidc: object,
        target_oidc: object,
        pending: dict[str, object],
    ) -> str:
        del core_config_path, core_account, source_oidc, pending
        target_payload = (
            json.dumps(target_oidc.model_dump(mode="json"), indent=2, sort_keys=True).encode()
            + b"\n"
        )
        setup._write_journaled_core_config(
            core_oidc_path,
            target_payload,
            account=SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid()),
            previous=core_oidc_path.read_bytes(),
        )
        return "updated_package_upgrade"

    monkeypatch.setattr(
        setup,
        "_migrate_canonical_owner_core_policy",
        migrate_core_policy,
    )

    enrolled_target = (
        harness.request.approval_approver_principal_id
        if target_matches_identity
        else "different-enrolled-owner"
    )
    monkeypatch.setattr(
        setup,
        "_validated_managed_identity_profile",
        lambda *_args, **_kwargs: {"actor": {"principal_id": enrolled_target}},
    )

    upgrade_event_offset = len(harness.operation_events)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.51")
    if not target_matches_identity:
        with pytest.raises(
            ServerSetupError,
            match="recovery target does not match enrolled identity",
        ):
            harness.apply(harness.plan_digest())
        assert seen == {}
        return
    result = harness.apply(harness.plan_digest())

    argv = seen["argv"]
    assert isinstance(argv, list)
    assert argv[argv.index("--source-principal") + 1] == source_principal
    assert (
        argv[argv.index("--target-principal") + 1]
        == harness.request.approval_approver_principal_id
    )
    assert argv[argv.index("--config") + 1] == str(
        harness.layout.host(setup.APPROVAL_CONFIG)
    )
    assert seen["stage"] == "canonical_owner_recovery"
    assert {
        "id": "canonical_owner_recovery",
        "status": "recovered",
        "source_principal_id": source_principal,
        "target_principal_id": harness.request.approval_approver_principal_id,
        "core_policy_status": "updated_package_upgrade",
    } in result["steps"]
    upgrade_events = harness.operation_events[upgrade_event_offset:]
    quiesced = upgrade_events.index(
        ("systemctl", ("disable", "--now", setup.APPROVAL_UNIT))
    )
    recovered_event = upgrade_events.index(("product", "canonical_owner_recovery"))
    assert quiesced < recovered_event
    assert harness.marker()["package_version"] == "0.1.51"


def test_canonical_owner_core_policy_cutover_resumes_partial_write(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    harness.apply(harness.plan_digest())
    core_config_path = harness.layout.host(setup.CORE_CONFIG)
    core_oidc_path = harness.layout.host(setup.CORE_OIDC_CONFIG)
    target_oidc = OIDCEnrollmentConfig.model_validate_json(
        core_oidc_path.read_text(encoding="utf-8")
    )
    target_approver = target_oidc.trusted_approvers[0]
    source_signer = P256KeyPair.generate()
    source_principal = "setup-placeholder-owner"
    source_approver = target_approver.model_copy(
        update={
            "principal_id": source_principal,
            "signer_key_id": source_signer.thumbprint,
            "public_key_pem": source_signer.public_pem,
        }
    )
    assert target_oidc.approval_service is not None
    source_oidc = target_oidc.model_copy(
        update={
            "trusted_approvers": (source_approver,),
            "approval_service": target_oidc.approval_service.model_copy(
                update={"approver_principal_id": source_principal}
            ),
        }
    )
    source_oidc_payload = (
        json.dumps(source_oidc.model_dump(mode="json"), indent=2, sort_keys=True).encode()
        + b"\n"
    )
    source_core = json.loads(core_config_path.read_text(encoding="utf-8"))
    source_core["oidc_enrollment"] = source_oidc.model_dump(mode="json")
    source_core_payload = (
        json.dumps(source_core, indent=2, sort_keys=True).encode() + b"\n"
    )
    core_config_path.write_bytes(source_core_payload)
    core_oidc_path.write_bytes(source_oidc_payload)
    pending: dict[str, object] = {
        "journal": {
            "previous_configs": {
                "core_config": base64.b64encode(source_core_payload).decode("ascii"),
                "core_oidc_config": base64.b64encode(source_oidc_payload).decode("ascii"),
            }
        }
    }
    original_write = setup._write_journaled_core_config
    writes = {"count": 0}

    def interrupt_second_write(*args: object, **kwargs: object) -> str:
        writes["count"] += 1
        if writes["count"] == 2:
            raise RuntimeError("injected Core config interruption")
        return original_write(*args, **kwargs)

    migrate_core_policy = getattr(setup, "_migrate_canonical_owner_core_policy")
    monkeypatch.setattr(setup, "_write_journaled_core_config", interrupt_second_write)
    with pytest.raises(RuntimeError, match="injected Core config interruption"):
        migrate_core_policy(
            core_config_path=core_config_path,
            core_oidc_path=core_oidc_path,
            core_account=pwd.getpwuid(os.geteuid()),
            source_oidc=source_oidc,
            target_oidc=target_oidc,
            pending=pending,
        )
    assert json.loads(core_config_path.read_text(encoding="utf-8"))[
        "oidc_enrollment"
    ] == target_oidc.model_dump(mode="json")
    assert OIDCEnrollmentConfig.model_validate_json(
        core_oidc_path.read_text(encoding="utf-8")
    ) == source_oidc

    monkeypatch.setattr(setup, "_write_journaled_core_config", original_write)
    status = migrate_core_policy(
        core_config_path=core_config_path,
        core_oidc_path=core_oidc_path,
        core_account=pwd.getpwuid(os.geteuid()),
        source_oidc=source_oidc,
        target_oidc=target_oidc,
        pending=pending,
    )
    assert status == "updated_package_upgrade"
    assert OIDCEnrollmentConfig.model_validate_json(
        core_oidc_path.read_text(encoding="utf-8")
    ) == target_oidc


def test_0146_replaces_released_timer_without_resetting_server_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _realized_0145_timer_source(tmp_path, monkeypatch)
    before_marker = harness.marker_path.read_bytes()
    before_database = copy.deepcopy(harness.database_state)
    before_revision = harness.marker()["revision"]
    upgrade_event_offset = len(harness.operation_events)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.46")
    upgraded = harness.apply(harness.plan_digest())

    assert {
        "id": "package_upgrade",
        "status": "validated_pre_upgrade_realized_state",
    } in upgraded["steps"]
    marker = harness.marker()
    assert marker["package_version"] == "0.1.46"
    assert marker["revision"] == before_revision + 1
    assert marker["previous_marker_digest"] == hashlib.sha256(before_marker).hexdigest()
    timer = harness.layout.unit(setup.CREDENTIAL_RENEW_TIMER).read_bytes()
    assert b"OnUnitInactiveSec=1h" in timer
    assert b"OnUnitActiveSec=" not in timer
    assert b"Persistent=" not in timer
    assert harness.database_state == before_database
    assert not harness.journal_path.exists()

    upgrade_events = harness.operation_events[upgrade_event_offset:]
    quiesced = upgrade_events.index(
        ("systemctl", ("disable", "--now", setup.APPROVAL_UNIT))
    )
    runtime_events = [
        (index, payload)
        for index, (kind, payload) in enumerate(upgrade_events)
        if kind == "runtime"
    ]
    assert [payload for _, payload in runtime_events] == [
        (setup.APPROVAL_USER, "approval_runtime_prepare"),
        (setup.CORE_USER, "core_runtime_prepare"),
        (setup.C0_RESPONDER_USER, "c0_responder_runtime_prepare"),
    ]
    assert all(index > quiesced for index, _ in runtime_events)
    approval_status = upgrade_events.index(("product", "approval_status"))
    assert approval_status > max(index for index, _ in runtime_events)


@pytest.mark.parametrize("package_version", ["0.1.33", "0.1.34", "0.1.35", "0.1.36"])
def test_0138_upgrade_rejects_other_release_sources(
    monkeypatch: pytest.MonkeyPatch,
    package_version: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.38")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version=package_version,
        artifact_mode="disabled",
    )

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


@pytest.mark.parametrize("package_version", ["0.1.34", "0.1.35", "0.1.36"])
def test_0137_upgrade_rejects_unapproved_corrective_release_source(
    monkeypatch: pytest.MonkeyPatch,
    package_version: str,
) -> None:
    monkeypatch.setattr(setup, "__version__", "0.1.37")
    payload = _marker_payload(
        schema="agentnet.server-setup.marker.v3",
        package_version=package_version,
        artifact_mode="disabled",
    )

    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="9" * 64,
            legacy_request_digest="1" * 64,
            artifact_mode="disabled",
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


def _marker_payload(
    *,
    schema: str,
    package_version: str,
    artifact_mode: str | None,
    request_digest: str = "1" * 64,
) -> bytes:
    value: dict[str, object] = {
        "schema": schema,
        "request_digest": request_digest,
        "approval_config_digest": "2" * 64,
        "core_config_digest": "3" * 64,
        "units": list(setup.MANAGED_UNITS),
        "package_version": package_version,
        "previous_marker_digest": None,
        "revision": 7,
        "unit_digests": {unit: "4" * 64 for unit in setup.MANAGED_UNITS},
    }
    if artifact_mode is not None:
        value["artifact_mode"] = artifact_mode
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n"


# --------------------------------------------------------------------------
# Pre-upgrade realized-state validation
# --------------------------------------------------------------------------


def test_pre_upgrade_gate_requires_the_exact_recorded_realized_state(tmp_path: Path) -> None:
    approval = tmp_path / "approval.json"
    core = tmp_path / "core.json"
    units = {
        unit: tmp_path / unit
        for unit in setup.MANAGED_UNITS
    }
    for unit, path in units.items():
        path.write_bytes(f"[Unit]\n{unit}\n".encode())
        path.chmod(0o644)
    marker = {
        "approval_config_digest": "a" * 64,
        "core_config_digest": "b" * 64,
        "unit_digests": {
            unit: hashlib.sha256(path.read_bytes()).hexdigest() for unit, path in units.items()
        },
    }
    uid, gid = os.geteuid(), os.getegid()
    setup._require_marker_realized_state(
        marker,
        approval_config_digest="a" * 64,
        core_config_digest="b" * 64,
        unit_paths=units,
        uid=uid,
        gid=gid,
    )

    for drift in ({"approval_config_digest": "c" * 64}, {"core_config_digest": "c" * 64}):
        with pytest.raises(ServerSetupError) as exc_info:
            setup._require_marker_realized_state(
                marker,
                approval_config_digest=drift.get("approval_config_digest", "a" * 64),
                core_config_digest=drift.get("core_config_digest", "b" * 64),
                unit_paths=units,
                uid=uid,
                gid=gid,
            )
        assert exc_info.value.blocker == "setup_upgrade_conflict"

    units[setup.CORE_UNIT].write_bytes(b"[Unit]\ntampered\n")
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_marker_realized_state(
            marker,
            approval_config_digest="a" * 64,
            core_config_digest="b" * 64,
            unit_paths=units,
            uid=uid,
            gid=gid,
        )
    assert exc_info.value.blocker == "setup_upgrade_conflict"

    units[setup.CORE_UNIT].unlink()
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_marker_realized_state(
            marker,
            approval_config_digest="a" * 64,
            core_config_digest="b" * 64,
            unit_paths=units,
            uid=uid,
            gid=gid,
        )
    assert exc_info.value.blocker == "setup_upgrade_conflict"


def test_pre_upgrade_gate_rejects_recorded_units_outside_the_fixed_profile(tmp_path: Path) -> None:
    unit_path = tmp_path / setup.CORE_UNIT
    unit_path.write_bytes(b"[Unit]\n")
    unit_path.chmod(0o644)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_marker_realized_state(
            {
                "approval_config_digest": "a" * 64,
                "core_config_digest": "b" * 64,
                "unit_digests": {"unexpected.service": "c" * 64},
            },
            approval_config_digest="a" * 64,
            core_config_digest="b" * 64,
            unit_paths={setup.CORE_UNIT: unit_path},
            uid=os.geteuid(),
            gid=os.getegid(),
        )
    assert exc_info.value.blocker == "setup_upgrade_conflict"


# --------------------------------------------------------------------------
# End-to-end upgrade: atomic, replay-safe, rollback
# --------------------------------------------------------------------------


def test_supported_upgrade_is_atomic_and_leaves_no_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    before_units = harness.unit_payloads()
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    upgrade_digest = harness.plan_digest()
    assert upgrade_digest != first_digest

    upgraded = harness.apply(upgrade_digest)

    assert {"id": "package_upgrade", "status": "validated_pre_upgrade_realized_state"} in upgraded["steps"]
    assert any(
        step["id"] == f"unit:{setup.CORE_UNIT}" and step["status"] == "updated_package_upgrade"
        for step in upgraded["steps"]
    )
    marker = harness.marker()
    assert marker["package_version"] == "0.1.31"
    assert marker["request_digest"] == upgrade_digest
    assert marker["revision"] == before_marker["revision"] + 1
    assert marker["previous_marker_digest"] == hashlib.sha256(
        json.dumps(before_marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    ).hexdigest()
    assert harness.unit_payloads() != before_units
    assert not harness.journal_path.exists()
    assert stat.S_IMODE(harness.marker_path.stat().st_mode) == 0o600

    # Exact replay of the committed upgrade is idempotent, not a second upgrade.
    replayed = harness.apply(upgrade_digest)
    assert {"id": "package_upgrade", "status": "not_required"} in replayed["steps"]
    assert harness.marker()["revision"] == marker["revision"]
    assert not harness.journal_path.exists()


def test_public_0131_two_unit_topology_expands_atomically_to_five_units(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker_payload = harness.marker_path.read_bytes()
    before_marker = harness.marker()
    before_units = harness.unit_payloads()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    assert upgrade_digest != first_digest

    upgraded = harness.apply(upgrade_digest)

    assert {
        "id": "package_upgrade",
        "status": "validated_pre_upgrade_realized_state",
    } in upgraded["steps"]
    marker = harness.marker()
    assert marker["package_version"] == "0.1.33"
    assert marker["units"] == list(setup.MANAGED_UNITS)
    assert set(marker["unit_digests"]) == set(setup.MANAGED_UNITS)
    assert marker["revision"] == before_marker["revision"] + 1
    assert marker["previous_marker_digest"] == hashlib.sha256(
        before_marker_payload
    ).hexdigest()
    assert harness.unit_payloads() != before_units
    assert all(harness.layout.unit(unit).is_file() for unit in setup.MANAGED_UNITS)
    assert not harness.journal_path.exists()


def test_public_0132_five_unit_topology_upgrades_in_place(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, first_digest = _realized_0132_deployment(tmp_path, monkeypatch)
    before_marker = harness.marker()
    before_units = {
        unit: harness.layout.unit(unit).read_bytes() for unit in setup.MANAGED_UNITS
    }

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    assert upgrade_digest != first_digest
    upgraded = harness.apply(upgrade_digest)

    assert {
        "id": "package_upgrade",
        "status": "validated_pre_upgrade_realized_state",
    } in upgraded["steps"]
    marker = harness.marker()
    assert marker["package_version"] == "0.1.33"
    assert marker["revision"] == before_marker["revision"] + 1
    assert any(
        harness.layout.unit(unit).read_bytes() != before_units[unit]
        for unit in setup.MANAGED_UNITS
    )
    assert marker["unit_digests"] == {
        unit: hashlib.sha256(harness.layout.unit(unit).read_bytes()).hexdigest()
        for unit in setup.MANAGED_UNITS
    }
    assert not harness.journal_path.exists()


def test_0132_upgrade_bootstrap_failure_is_forward_only_and_retries(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0132_deployment(tmp_path, monkeypatch)
    harness.active_units.update(setup.MANAGED_UNITS)
    harness.loaded_units.update(setup.MANAGED_UNITS)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_bootstrap = setup._run_bootstrap_idempotently
    monkeypatch.setattr(
        setup,
        "_run_bootstrap_idempotently",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected 0.1.32 bootstrap failure")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected 0.1.32 bootstrap failure"):
        harness.apply(upgrade_digest)

    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert harness.journal_path.exists()
    assert not harness.active_units

    monkeypatch.setattr(setup, "_run_bootstrap_idempotently", original_bootstrap)
    recovered = harness.apply(upgrade_digest)
    assert {
        "id": "package_upgrade",
        "status": "resumed_committed_forward_only_upgrade",
    } in recovered["steps"]
    assert not harness.journal_path.exists()


def test_0137_upgrade_bootstrap_failure_is_forward_only_and_retries(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0137_deployment(tmp_path, monkeypatch)
    harness.active_units.update(setup.MANAGED_UNITS)
    harness.loaded_units.update(setup.MANAGED_UNITS)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.38")
    upgrade_digest = harness.plan_digest()
    original_bootstrap = setup._run_bootstrap_idempotently
    monkeypatch.setattr(
        setup,
        "_run_bootstrap_idempotently",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected 0.1.37 bootstrap failure")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected 0.1.37 bootstrap failure"):
        harness.apply(upgrade_digest)

    assert harness.marker()["package_version"] == "0.1.38"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert harness.journal_path.exists()
    assert not harness.active_units

    monkeypatch.setattr(setup, "_run_bootstrap_idempotently", original_bootstrap)
    recovered = harness.apply(upgrade_digest)
    assert {
        "id": "package_upgrade",
        "status": "resumed_committed_forward_only_upgrade",
    } in recovered["steps"]
    assert not harness.journal_path.exists()


def test_0132_upgrade_marker_response_loss_never_rolls_back_target(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0132_deployment(tmp_path, monkeypatch)
    harness.active_units.update(setup.MANAGED_UNITS)
    harness.loaded_units.update(setup.MANAGED_UNITS)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_commit = setup._commit_setup_marker

    def committed_then_lost(*args: object, **kwargs: object) -> str:
        original_commit(*args, **kwargs)
        raise ServerSetupError(
            "setup_marker_response_lost",
            "injected marker response loss",
        )

    monkeypatch.setattr(setup, "_commit_setup_marker", committed_then_lost)

    with pytest.raises(ServerSetupError, match="injected marker response loss"):
        harness.apply(upgrade_digest)

    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert harness.journal_path.exists()
    assert harness.active_units == set(setup.MANAGED_UNITS)

    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    recovered = harness.apply(upgrade_digest)
    assert {
        "id": "package_upgrade",
        "status": "resumed_committed_forward_only_upgrade",
    } in recovered["steps"]
    assert not harness.active_units
    assert not harness.journal_path.exists()


@pytest.mark.parametrize("source_version", ["0.1.31", "0.1.32"])
def test_0133_upgrade_orders_marker_then_quiescence_then_bootstrap(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    source_version: str,
) -> None:
    if source_version == "0.1.31":
        harness, _first_digest = _realized_public_0131_communication_deployment(
            tmp_path,
            monkeypatch,
        )
    else:
        harness, _first_digest = _realized_0132_deployment(tmp_path, monkeypatch)
        harness.active_units.update(setup.MANAGED_UNITS)
        harness.loaded_units.update(setup.MANAGED_UNITS)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    events: list[str] = []
    original_commit = setup._commit_setup_marker
    original_sequence = setup._run_systemctl_sequence_or_reconcile
    original_bootstrap = setup._run_bootstrap_idempotently

    def recorded_commit(*args: object, **kwargs: object) -> str:
        events.append("marker")
        return original_commit(*args, **kwargs)

    def recorded_sequence(
        executable: Path,
        sequence: tuple[list[str], ...],
        *,
        reconcile: object,
    ) -> str:
        result = original_sequence(
            executable,
            sequence,
            reconcile=reconcile,
        )
        if ["disable", "--now", setup.CORE_UNIT] in sequence:
            events.append("quiescence")
        return result

    def recorded_bootstrap(*args: object, **kwargs: object) -> tuple[dict[str, object], str]:
        events.append("bootstrap")
        return original_bootstrap(*args, **kwargs)

    monkeypatch.setattr(setup, "_commit_setup_marker", recorded_commit)
    monkeypatch.setattr(
        setup,
        "_run_systemctl_sequence_or_reconcile",
        recorded_sequence,
    )
    monkeypatch.setattr(setup, "_run_bootstrap_idempotently", recorded_bootstrap)

    harness.apply(upgrade_digest)

    assert events[:3] == ["marker", "quiescence", "bootstrap"]


def test_0132_upgrade_reconciles_lost_quiescence_response(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0132_deployment(tmp_path, monkeypatch)
    harness.active_units.update(setup.MANAGED_UNITS)
    harness.loaded_units.update(setup.MANAGED_UNITS)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_systemctl = setup._run_systemctl
    response_lost = [False]

    def lose_one_response(
        executable: Path,
        arguments: list[str],
        *,
        failure_message: str,
    ) -> None:
        original_systemctl(
            executable,
            arguments,
            failure_message=failure_message,
        )
        if (
            arguments == ["disable", "--now", setup.CORE_UNIT]
            and not response_lost[0]
        ):
            response_lost[0] = True
            raise ServerSetupError("systemd_start", "injected lost systemd response")

    monkeypatch.setattr(setup, "_run_systemctl", lose_one_response)

    upgraded = harness.apply(upgrade_digest)

    assert response_lost == [True]
    assert {
        "id": "package_upgrade_service_quiescence",
        "status": "reconciled_after_response_loss",
    } in upgraded["steps"]
    assert not harness.journal_path.exists()


@pytest.mark.parametrize("recovery_target", ["0.1.35", "0.1.37"])
def test_0131_topology_upgrade_commits_marker_before_forward_only_bootstrap(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    recovery_target: str,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_bootstrap = setup._run_bootstrap_idempotently
    monkeypatch.setattr(
        setup,
        "_run_bootstrap_idempotently",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected post-marker bootstrap failure")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected post-marker bootstrap failure"):
        harness.apply(upgrade_digest)

    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert all(harness.layout.unit(unit).is_file() for unit in setup.MANAGED_UNITS)
    assert harness.journal_path.exists()
    assert not harness.active_units
    assert not harness.layout.host(setup.C0_RESPONDER_DATA).exists()

    # Reproduce the exact released 0.1.33 interruption: the target marker and
    # journal are committed, but Approval retains systemd's failed latch after
    # its otherwise successful SIGTERM quiescence.
    failed_units = {setup.APPROVAL_UNIT}
    original_systemd_show = setup._systemd_show
    original_systemctl = setup._run_systemctl

    def show_failed_until_reset(executable: Path, unit: str) -> dict[str, str]:
        properties = original_systemd_show(executable, unit)
        if unit in failed_units:
            properties = {**properties, "ActiveState": "failed"}
        return properties

    def clear_failed_state(
        executable: Path,
        arguments: list[str],
        *,
        failure_message: str,
    ) -> None:
        original_systemctl(
            executable,
            arguments,
            failure_message=failure_message,
        )
        if arguments[:1] == ["reset-failed"]:
            failed_units.discard(arguments[1])

    monkeypatch.setattr(setup, "_systemd_show", show_failed_until_reset)
    monkeypatch.setattr(setup, "_run_systemctl", clear_failed_state)
    monkeypatch.setattr(setup, "_run_bootstrap_idempotently", original_bootstrap)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", recovery_target)
    recovery_digest = harness.plan_digest()
    retained_journal = harness.journal_path.read_bytes()
    original_write_journal = setup._write_upgrade_journal
    monkeypatch.setattr(
        setup,
        "_write_upgrade_journal",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            OSError("injected next-edge journal write failure")
        ),
    )

    with pytest.raises(OSError, match="injected next-edge journal write failure"):
        harness.apply(recovery_digest)

    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.journal_path.read_bytes() == retained_journal

    monkeypatch.setattr(setup, "_write_upgrade_journal", original_write_journal)
    recovered = harness.apply(recovery_digest)
    assert {
        "id": "package_upgrade",
        "status": "validated_pre_upgrade_realized_state",
    } in recovered["steps"]
    assert not failed_units
    assert ["reset-failed", setup.APPROVAL_UNIT] in harness.systemctl_calls
    assert harness.marker()["package_version"] == recovery_target
    assert harness.layout.host(setup.C0_RESPONDER_DATA).is_dir()
    assert not harness.journal_path.exists()


def test_0131_topology_upgrade_quiescence_failure_remains_forward_only(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_systemctl = setup._run_systemctl
    monkeypatch.setattr(
        setup,
        "_run_systemctl",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("systemd_start", "injected quiescence failure")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected quiescence failure"):
        harness.apply(upgrade_digest)

    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert harness.journal_path.exists()
    assert harness.active_units == set(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    assert not harness.layout.host(setup.C0_RESPONDER_DATA).exists()

    monkeypatch.setattr(setup, "_run_systemctl", original_systemctl)
    recovered = harness.apply(upgrade_digest)
    assert {
        "id": "package_upgrade",
        "status": "resumed_committed_forward_only_upgrade",
    } in recovered["steps"]
    assert not harness.journal_path.exists()


def test_0131_topology_upgrade_rejects_target_state_before_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    renewal_state = harness.layout.host(setup.CREDENTIAL_RENEW_STATE)
    renewal_state.write_text("{}\n", encoding="utf-8")
    renewal_state.chmod(0o600)
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()


def test_0131_topology_upgrade_requires_exact_legacy_environment_before_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    harness.layout.host(setup.CORE_ENV).write_text(
        "AGENTNET_DATABASE_URL=postgresql:///wrong\n",
        encoding="utf-8",
    )
    harness.layout.host(setup.CORE_ENV).chmod(0o600)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()


def test_0131_topology_upgrade_rejects_loaded_target_unit_before_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    monkeypatch.setattr(
        setup,
        "_systemd_show",
        lambda _executable, unit: (
            {
                "LoadState": "loaded",
                "UnitFileState": "enabled",
                "ActiveState": "active",
                "FragmentPath": f"/run/systemd/system/{unit}",
                "DropInPaths": "",
                "MainPID": "71",
            }
            if unit == setup.C0_RESPONDER_UNIT
            else {
                "LoadState": "not-found",
                "UnitFileState": "disabled",
                "ActiveState": "inactive",
                "FragmentPath": "",
                "DropInPaths": "",
                "MainPID": "0",
            }
        ),
    )

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()


def test_failed_0131_topology_upgrade_removes_target_only_units_on_rollback(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    before_units = harness.unit_payloads()
    target_only = set(setup.MANAGED_UNITS) - set(setup.LEGACY_COMMUNICATION_ONLY_UNITS)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    original_commit = setup._commit_setup_marker
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected topology marker interruption")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected topology marker interruption"):
        harness.apply(harness.plan_digest())

    assert harness.marker() == before_marker
    assert harness.unit_payloads() == before_units
    assert all(not harness.layout.unit(unit).exists() for unit in target_only)
    assert harness.active_units == set(setup.LEGACY_COMMUNICATION_ONLY_UNITS)
    assert not harness.journal_path.exists()

    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    recovered = harness.apply(harness.plan_digest())
    assert {
        "id": "package_upgrade",
        "status": "validated_pre_upgrade_realized_state",
    } in recovered["steps"]
    assert harness.marker()["package_version"] == "0.1.33"
    assert not harness.journal_path.exists()


def test_interrupted_0131_topology_upgrade_resumes_from_absence_aware_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    target_only = set(setup.MANAGED_UNITS) - set(setup.LEGACY_COMMUNICATION_ONLY_UNITS)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_commit = setup._commit_setup_marker
    original_rollback = setup._rollback_pending_upgrade
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", lambda _pending: None)
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected topology power loss")
        ),
    )

    with pytest.raises(ServerSetupError, match="injected topology power loss"):
        harness.apply(upgrade_digest)

    journal = json.loads(harness.journal_path.read_text(encoding="utf-8"))
    assert journal["schema"] == "agentnet.server-setup.upgrade-journal.v2"
    assert journal["from_package_version"] == "0.1.31"
    assert journal["to_package_version"] == "0.1.33"
    assert all(journal["previous_units"][unit] is None for unit in target_only)
    assert all(harness.layout.unit(unit).is_file() for unit in setup.MANAGED_UNITS)
    assert harness.marker() == before_marker

    monkeypatch.setattr(setup, "_rollback_pending_upgrade", original_rollback)
    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    resumed = harness.apply(upgrade_digest)

    assert {
        "id": "package_upgrade",
        "status": "resumed_journaled_upgrade",
    } in resumed["steps"]
    assert harness.marker()["package_version"] == "0.1.33"
    assert harness.marker()["units"] == list(setup.MANAGED_UNITS)
    assert not harness.journal_path.exists()


def test_interrupted_0131_topology_upgrade_refuses_tampered_created_unit(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    upgrade_digest = harness.plan_digest()
    original_commit = setup._commit_setup_marker
    original_rollback = setup._rollback_pending_upgrade
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", lambda _pending: None)
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected topology power loss")
        ),
    )
    with pytest.raises(ServerSetupError, match="injected topology power loss"):
        harness.apply(upgrade_digest)

    tampered_path = harness.layout.unit(setup.C0_RESPONDER_UNIT)
    tampered_path.write_bytes(tampered_path.read_bytes() + b"# tampered after interruption\n")
    tampered_path.chmod(0o644)
    tampered = tampered_path.read_bytes()
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", original_rollback)
    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(upgrade_digest)

    assert exc_info.value.blocker == "managed_path_conflict"
    assert tampered_path.read_bytes() == tampered
    assert harness.marker() == before_marker
    assert harness.journal_path.exists()


def test_0131_topology_upgrade_rejects_preexisting_target_only_unit(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    before_marker = harness.marker()
    unexpected = harness.layout.unit(setup.C0_RESPONDER_UNIT)
    unexpected.write_bytes(b"[Unit]\nDescription=operator-owned collision\n")
    unexpected.chmod(0o644)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert unexpected.read_bytes() == b"[Unit]\nDescription=operator-owned collision\n"
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()


def test_0131_topology_upgrade_validates_source_before_creating_c0_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_public_0131_communication_deployment(
        tmp_path,
        monkeypatch,
    )
    core_unit = harness.layout.unit(setup.CORE_UNIT)
    core_unit.write_bytes(core_unit.read_bytes() + b"# source drift\n")
    core_unit.chmod(0o644)

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.33")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert not harness.layout.host(setup.C0_RESPONDER_DATA).exists()
    assert not harness.journal_path.exists()


def test_public_0130_owner_policy_is_migrated_under_the_upgrade_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    desired, legacy_payloads = _stage_public_0130_owner_policy_shape(harness, monkeypatch)
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    upgraded = harness.apply(harness.plan_digest())

    assert {
        "id": "core_remote_activation_policy_upgrade",
        "status": "updated_package_upgrade",
    } in upgraded["steps"]
    desired_document = desired.model_dump(mode="json")
    assert json.loads(
        harness.layout.host(setup.CORE_OIDC_CONFIG).read_text(encoding="utf-8")
    ) == desired_document
    assert json.loads(
        harness.layout.host(setup.CORE_CONFIG).read_text(encoding="utf-8")
    )["oidc_enrollment"] == desired_document
    assert harness.layout.host(setup.CORE_CONFIG).read_bytes() != legacy_payloads["core_config"]
    assert harness.marker()["revision"] == before_marker["revision"] + 1
    assert not harness.journal_path.exists()


def test_failed_owner_policy_migration_restores_both_exact_legacy_configs(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    _desired, legacy_payloads = _stage_public_0130_owner_policy_shape(harness, monkeypatch)
    before_marker = harness.marker()
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    original_write = setup._write_journaled_core_config
    writes = 0

    def fail_second_config(*args: object, **kwargs: object) -> str:
        nonlocal writes
        writes += 1
        if writes == 2:
            raise ServerSetupError("injected_failure", "injected config migration failure")
        return original_write(*args, **kwargs)

    monkeypatch.setattr(setup, "_write_journaled_core_config", fail_second_config)
    with pytest.raises(ServerSetupError, match="injected config migration failure"):
        harness.apply(harness.plan_digest())

    assert harness.layout.host(setup.CORE_CONFIG).read_bytes() == legacy_payloads["core_config"]
    assert harness.layout.host(setup.CORE_OIDC_CONFIG).read_bytes() == legacy_payloads[
        "core_oidc_config"
    ]
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()


def test_failed_upgrade_rolls_back_units_and_retains_the_recorded_marker(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    before_units = harness.unit_payloads()
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    upgrade_digest = harness.plan_digest()

    original_commit = setup._commit_setup_marker
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected marker interruption")
        ),
    )
    with pytest.raises(ServerSetupError, match="injected marker interruption"):
        harness.apply(upgrade_digest)

    assert harness.unit_payloads() == before_units
    assert harness.marker() == before_marker
    assert not harness.journal_path.exists()

    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    recovered = harness.apply(upgrade_digest)
    assert {"id": "package_upgrade", "status": "validated_pre_upgrade_realized_state"} in recovered["steps"]
    assert harness.marker()["package_version"] == "0.1.31"
    assert not harness.journal_path.exists()


def test_upgrade_interrupted_without_rollback_resumes_from_the_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    upgrade_digest = harness.plan_digest()

    original_commit = setup._commit_setup_marker
    # A hard kill leaves the journal and the new units behind: no rollback runs.
    monkeypatch.setattr(setup, "_rollback_pending_upgrade", lambda _pending: None)
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected power loss")
        ),
    )
    with pytest.raises(ServerSetupError, match="injected power loss"):
        harness.apply(upgrade_digest)
    assert harness.journal_path.exists()
    assert harness.marker() == before_marker
    journal = json.loads(harness.journal_path.read_text(encoding="utf-8"))
    assert journal["from_package_version"] == "0.1.30"
    assert journal["to_package_version"] == "0.1.31"
    assert journal["to_request_digest"] == upgrade_digest
    assert stat.S_IMODE(harness.journal_path.stat().st_mode) == 0o600

    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit)
    resumed = harness.apply(upgrade_digest)
    assert {"id": "package_upgrade", "status": "resumed_journaled_upgrade"} in resumed["steps"]
    assert harness.marker()["package_version"] == "0.1.31"
    assert harness.marker()["revision"] == before_marker["revision"] + 1
    assert not harness.journal_path.exists()


def test_upgrade_committed_before_journal_clear_recovers_exactly(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    before_marker = harness.marker()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    upgrade_digest = harness.plan_digest()
    original_clear = setup._clear_upgrade_journal

    def simulate_power_loss_after_marker_commit(_path: Path) -> None:
        raise SystemExit("injected power loss after marker commit")

    monkeypatch.setattr(setup, "_clear_upgrade_journal", simulate_power_loss_after_marker_commit)
    with pytest.raises(SystemExit, match="after marker commit"):
        harness.apply(upgrade_digest)

    committed_marker = harness.marker()
    assert committed_marker["package_version"] == "0.1.31"
    assert committed_marker["request_digest"] == upgrade_digest
    assert committed_marker["revision"] == before_marker["revision"] + 1
    assert harness.journal_path.exists()

    monkeypatch.setattr(setup, "_clear_upgrade_journal", original_clear)
    recovered = harness.apply(upgrade_digest)
    assert {"id": "package_upgrade", "status": "cleared_committed_upgrade"} in recovered[
        "steps"
    ]
    assert harness.marker() == committed_marker
    assert not harness.journal_path.exists()


def test_unrelated_journal_fails_closed_before_any_managed_write(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    before_units = harness.unit_payloads()
    setup._write_upgrade_journal(
        harness.journal_path,
        {
            "schema": "agentnet.server-setup.upgrade-journal.v1",
            "from_marker_sha256": "0" * 64,
            "from_package_version": "0.1.28",
            "from_request_digest": "1" * 64,
            "to_package_version": "0.1.31",
            "to_request_digest": "2" * 64,
            "previous_units": {
                setup.APPROVAL_UNIT: base64.b64encode(b"stale-approval").decode(),
                setup.CORE_UNIT: base64.b64encode(b"stale-core").decode(),
            },
        },
        uid=os.geteuid(),
        gid=os.getegid(),
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())
    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.unit_payloads() == before_units
    assert harness.marker()["package_version"] == "0.1.30"


@pytest.mark.parametrize(
    "drift",
    ["domain", "core_origin", "approval_origin", "oidc_issuer", "artifact_mode"],
)
def test_request_semantic_drift_is_rejected_before_any_managed_write(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    drift: str,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    _stage_public_0130_owner_policy_shape(harness, monkeypatch)
    managed_paths = (
        harness.marker_path,
        harness.layout.host(setup.CORE_CONFIG),
        harness.layout.host(setup.CORE_OIDC_CONFIG),
        harness.layout.host(setup.APPROVAL_CONFIG),
        harness.layout.host(setup.CORE_ENV),
        harness.layout.host(setup.APPROVAL_ENV),
        harness.layout.unit(setup.CORE_UNIT),
        harness.layout.unit(setup.APPROVAL_UNIT),
    )
    before = {path: path.read_bytes() for path in managed_paths}
    before_calls = len(harness.product_calls)

    request_path = tmp_path / "setup.json"
    request_document = json.loads(request_path.read_text(encoding="utf-8"))
    core_oidc_path = tmp_path / "core-oidc.json"
    core_oidc = json.loads(core_oidc_path.read_text(encoding="utf-8"))
    owner_oidc_path = tmp_path / "owner-oidc.json"
    owner_oidc = json.loads(owner_oidc_path.read_text(encoding="utf-8"))
    approvers_path = tmp_path / "approvers.json"
    approvers = json.loads(approvers_path.read_text(encoding="utf-8"))

    if drift == "domain":
        request_document["domain_id"] = "other.example"
        request_document["service_audience"] = "urn:agentnet:other.example:corporate-api"
        approvers["approvers"][0]["domain_id"] = "other.example"
    elif drift == "core_origin":
        request_document["core_public_origin"] = "https://core2.corp.example"
        core_oidc["redirect_uri"] = (
            "https://core2.corp.example/v1/enrollment/oidc/callback"
        )
    elif drift == "approval_origin":
        request_document["approval_public_origin"] = "https://approval2.corp.example"
        owner_oidc["redirect_uri"] = (
            "https://approval2.corp.example/v1/approval/owner/oidc/callback"
        )
    elif drift == "oidc_issuer":
        core_oidc["issuer"] = "https://login.example"
        core_oidc["allowed_endpoint_origins"] = ["https://login.example"]
        owner_oidc["issuer"] = "https://login.example"
        owner_oidc["allowed_endpoint_origins"] = ["https://login.example"]
        approvers["approvers"][0]["oidc_issuer"] = "https://login.example"
    else:
        endpoint = ScannerEndpoint.from_uri("unix:///run/clamav/clamd.sock")
        key = P256KeyPair.generate()
        key_path = tmp_path / "scanner-key.pem"
        key_path.write_bytes(key.private_pem)
        key_path.chmod(0o600)
        signature_updated_at = int(time.time())
        signature_max_age = 172_800
        rules_digest = clamav_rules_digest(
            signature_version="daily-1",
            signature_updated_at=signature_updated_at,
        )
        profile_digest = clamav_profile_digest(
            endpoint=endpoint,
            engine_version="1.4.3",
            timeout_seconds=30.0,
            max_bytes=16_777_216,
            max_response_bytes=4_096,
            max_signature_age_seconds=signature_max_age,
        )
        scanner_path = _private_json(
            tmp_path / "scanner-trust.json",
            {
                "trusted_public_keys": {
                    "maintained-scanner:1": key.public_pem,
                },
                "required_engine": "clamav",
                "required_rules_digest": rules_digest,
                "required_profile_digest": profile_digest,
            },
        )
        core_environment_path = Path(request_document["core_environment_file"])
        core_environment_path.write_text(
            core_environment_path.read_text(encoding="utf-8")
            + f"AGENTNET_CLAMAV_ENDPOINT={endpoint.uri}\n"
            + "AGENTNET_CLAMAV_SCANNER_ID=maintained-scanner\n"
            + "AGENTNET_CLAMAV_KEY_EPOCH=1\n"
            + f"AGENTNET_CLAMAV_SIGNING_KEY_FILE={key_path}\n"
            + "AGENTNET_CLAMAV_ENGINE_VERSION=1.4.3\n"
            + "AGENTNET_CLAMAV_SIGNATURE_VERSION=daily-1\n"
            + f"AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT={signature_updated_at}\n"
            + f"AGENTNET_CLAMAV_SIGNATURE_MAX_AGE_SECONDS={signature_max_age}\n",
            encoding="utf-8",
        )
        request_document["artifact_mode"] = "enabled"
        request_document["scanner_trust_file"] = str(scanner_path)

    _private_json(core_oidc_path, core_oidc)
    _private_json(owner_oidc_path, owner_oidc)
    _private_json(approvers_path, approvers)
    _private_json(request_path, request_document)
    harness.request = load_server_setup_request(request_path)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    digest = harness.plan_digest()

    with pytest.raises(ServerSetupError):
        harness.apply(digest)

    assert {path: path.read_bytes() for path in managed_paths} == before
    assert len(harness.product_calls) == before_calls
    assert not harness.journal_path.exists()


def test_upgrade_refuses_a_realized_state_changed_outside_setup(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness, _first_digest = _realized_0130_deployment(tmp_path, monkeypatch)
    core_unit = harness.layout.core_unit
    core_unit.write_bytes(core_unit.read_bytes() + b"# operator edit\n")
    tampered = core_unit.read_bytes()

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())
    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert core_unit.read_bytes() == tampered
    assert harness.marker()["package_version"] == "0.1.30"
    assert not harness.journal_path.exists()
def _lifecycle_source_bytes(harness: _Harness) -> dict[str, object]:
    return {
        "marker": harness.marker_path.read_bytes(),
        "units": {
            unit: harness.layout.unit(unit).read_bytes()
            for unit in setup.MANAGED_UNITS
        },
        "core_config": harness.layout.host(setup.CORE_CONFIG).read_bytes(),
        "core_oidc_config": harness.layout.host(setup.CORE_OIDC_CONFIG).read_bytes(),
        "database": copy.deepcopy(harness.database_state["source"]),
    }


def test_v0145_upgrade_preserves_enrollment_and_creates_restart_postcondition(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _realized_0144_lifecycle_source(tmp_path, monkeypatch)
    before = _lifecycle_source_bytes(harness)
    product_calls = list(harness.product_calls)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.45")

    result = harness.apply(harness.plan_digest())

    assert harness.marker()["package_version"] == "0.1.45"
    assert harness.marker()["revision"] == 2
    assert result["identity_enrolled"] is True
    assert result["endpoint_lifecycle"] == {
        "endpoint_id": "server-harness",
        "state": "restart_required",
        "public_url": harness.request.core_public_origin,
        "identity_created": False,
    }
    assert harness.database_state["phase"] == "target"
    assert harness.database_state["source"] == before["database"]
    endpoint = harness.database_state["endpoint_lifecycle"]
    assert isinstance(endpoint, dict)
    assert endpoint["harness_id"] == "server-harness"
    assert endpoint["adapter_generation"] == 1
    assert endpoint["state"] == "restart_required"
    assert endpoint["state_reason"] == "explicit_user_restart_required"
    assert endpoint["revision"] == 2
    assert endpoint["capability_root_digest"] is None
    assert endpoint["process_measurement"] is None
    assert [
        command[2:]
        for command in harness.product_calls[len(product_calls) :]
    ] == [
        [
            "approval",
            "status",
            "--config",
            str(harness.layout.host(setup.APPROVAL_CONFIG)),
        ]
    ]
    assert not harness.journal_path.exists()


@pytest.mark.parametrize(
    ("checkpoint", "start"),
    [
        ("after_units", False),
        ("after_migration", False),
        ("after_core_restart", True),
    ],
)
def test_v0145_upgrade_injected_failures_restore_exact_journaled_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    checkpoint: str,
    start: bool,
) -> None:
    harness = _realized_0144_lifecycle_source(tmp_path, monkeypatch)
    before = _lifecycle_source_bytes(harness)
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.45")
    if start:
        monkeypatch.setattr(
            setup,
            "_validated_managed_identity_profile",
            lambda *_args, **_kwargs: {
                "actor": {
                    "principal_id": "owner-principal",
                    "credential_epoch": 1,
                }
            },
        )
    if checkpoint == "after_units":
        original_write_unit = setup._write_managed_unit
        written = 0

        def fail_after_units(*args: object, **kwargs: object) -> str:
            nonlocal written
            status = original_write_unit(*args, **kwargs)
            written += 1
            if written == len(setup.MANAGED_UNITS):
                raise ServerSetupError("injected_failure", "after units")
            return status

        monkeypatch.setattr(setup, "_write_managed_unit", fail_after_units)
    elif checkpoint == "after_migration":
        original_database_operation = setup._run_v0145_database_operation_as

        def fail_after_migration(*args: object, **kwargs: object) -> dict[str, object]:
            evidence = original_database_operation(*args, **kwargs)
            if kwargs.get("operation") == "migrate":
                raise ServerSetupError("injected_failure", "after migration")
            return evidence

        monkeypatch.setattr(
            setup,
            "_run_v0145_database_operation_as",
            fail_after_migration,
        )
    else:
        original_systemctl = setup._run_systemctl

        def fail_after_core_restart(
            executable: Path,
            arguments: list[str],
            *,
            failure_message: str,
        ) -> None:
            original_systemctl(
                executable,
                arguments,
                failure_message=failure_message,
            )
            if arguments == ["restart", setup.CORE_UNIT]:
                raise RuntimeError("injected after Core restart")

        monkeypatch.setattr(setup, "_run_systemctl", fail_after_core_restart)

    expected_error = RuntimeError if checkpoint == "after_core_restart" else ServerSetupError
    with pytest.raises(expected_error) as exc_info:
        harness.apply(harness.plan_digest(), start=start)

    if isinstance(exc_info.value, ServerSetupError):
        assert exc_info.value.blocker == "injected_failure"
    assert harness.marker_path.read_bytes() == before["marker"]
    assert {
        unit: harness.layout.unit(unit).read_bytes()
        for unit in setup.MANAGED_UNITS
    } == before["units"]
    assert harness.layout.host(setup.CORE_CONFIG).read_bytes() == before["core_config"]
    assert (
        harness.layout.host(setup.CORE_OIDC_CONFIG).read_bytes()
        == before["core_oidc_config"]
    )
    assert harness.database_state["phase"] == "source"
    assert harness.database_state["source"] == before["database"]
    assert harness.database_state["endpoint_lifecycle"] is None
    assert not harness.journal_path.exists()


def test_v0145_upgrade_denies_rollback_after_concurrent_database_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _realized_0144_lifecycle_source(tmp_path, monkeypatch)
    before_marker = harness.marker_path.read_bytes()
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.45")

    original_database_operation = setup._run_v0145_database_operation_as

    def mutate_then_fail(*args: object, **kwargs: object) -> dict[str, object]:
        evidence = original_database_operation(*args, **kwargs)
        if kwargs.get("operation") == "migrate":
            harness.database_state["phase"] = "concurrent"
            endpoint = harness.database_state["endpoint_lifecycle"]
            assert isinstance(endpoint, dict)
            endpoint["mailbox_cursor"] = 9
            raise RuntimeError("injected concurrent mailbox delivery")
        return evidence

    monkeypatch.setattr(
        setup,
        "_run_v0145_database_operation_as",
        mutate_then_fail,
    )

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert harness.database_state["phase"] == "concurrent"
    assert harness.marker_path.read_bytes() == before_marker
    assert harness.journal_path.exists()


@pytest.mark.parametrize(
    ("source_version", "target_version"),
    [
        ("0.1.43", "0.1.45"),
        ("0.1.44", "0.1.46"),
    ],
)
def test_v0145_upgrade_rejects_wrong_exact_version_window(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    source_version: str,
    target_version: str,
) -> None:
    harness = _realized_0144_lifecycle_source(tmp_path, monkeypatch)
    marker = harness.marker()
    marker["package_version"] = source_version
    harness.marker_path.write_bytes(
        json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    )
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", target_version)

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_marker_conflict"
    assert not harness.journal_path.exists()


def test_v0145_upgrade_rejects_non_v6_source_schema(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _realized_0144_lifecycle_source(tmp_path, monkeypatch)
    source = harness.database_state["source"]
    assert isinstance(source, dict)
    source["schema_version"] = 5
    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.45")

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())

    assert exc_info.value.blocker == "setup_upgrade_conflict"
    assert not harness.journal_path.exists()




def test_unsupported_source_version_still_blocks_the_upgrade(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.29")
    harness.apply(harness.plan_digest())
    assert harness.marker()["package_version"] == "0.1.29"

    harness.install_new_package_runtime()
    monkeypatch.setattr(setup, "__version__", "0.1.31")
    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())
    assert exc_info.value.blocker == "setup_marker_conflict"
    assert harness.marker()["package_version"] == "0.1.29"


# --------------------------------------------------------------------------
# Idempotent bootstrap and lost responses
# --------------------------------------------------------------------------


def test_bootstrap_reconciles_one_lost_response_and_requires_fresh_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[int] = []
    evidence = _bootstrap_evidence("corp.example")

    def response_lost(*_args: object, **_kwargs: object) -> dict[str, object]:
        calls.append(1)
        if len(calls) == 1:
            raise ServerSetupError("invalid_product_evidence", "structured evidence stream was lost")
        return evidence

    monkeypatch.setattr(setup, "_run_as", response_lost)
    result, status = setup._run_bootstrap_idempotently(
        SimpleNamespace(),
        ["/opt/agentnet/bin/node", "/opt/agentnet/npm/bin/agentnet.mjs", "bootstrap-server-agent"],
        environment={},
        expected_domain_id="corp.example",
    )
    assert result == evidence
    assert status == "reconciled_after_response_loss"
    assert len(calls) == 2


def test_bootstrap_retries_at_most_once_and_never_retries_a_refusal(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[int] = []

    def always_lost(*_args: object, **_kwargs: object) -> dict[str, object]:
        calls.append(1)
        raise ServerSetupError("product_command_failed", "core_bootstrap timed out")

    monkeypatch.setattr(setup, "_run_as", always_lost)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_bootstrap_idempotently(
            SimpleNamespace(),
            ["/opt/node", "/opt/agentnet.mjs", "bootstrap-server-agent"],
            environment={},
            expected_domain_id="corp.example",
        )
    assert exc_info.value.blocker == "product_command_failed"
    assert len(calls) == 2

    refusals: list[int] = []

    def refused(*_args: object, **_kwargs: object) -> dict[str, object]:
        refusals.append(1)
        raise ServerSetupError("core_conflict", "core state conflicts with fixed request")

    monkeypatch.setattr(setup, "_run_as", refused)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_bootstrap_idempotently(
            SimpleNamespace(),
            ["/opt/node", "/opt/agentnet.mjs", "bootstrap-server-agent"],
            environment={},
            expected_domain_id="corp.example",
        )
    assert exc_info.value.blocker == "core_conflict"
    assert len(refusals) == 1


@pytest.mark.parametrize(
    "mutation",
    [
        {"domain": {"domain_id": "other.example"}},
        {"recovery": {"ready": False}},
        {"storage": {"ready": False}},
        {"audit": {"valid": False}},
        {"deployment_binding": None},
        {"domain": None},
    ],
)
def test_bootstrap_evidence_must_prove_exact_healthy_durable_state(
    mutation: dict[str, object],
) -> None:
    evidence = _bootstrap_evidence("corp.example")
    evidence.update(mutation)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_core_bootstrap_evidence(evidence, expected_domain_id="corp.example")
    assert exc_info.value.blocker == "core_bootstrap_evidence"


def test_bootstrap_response_loss_is_reconciled_inside_apply(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    digest = harness.plan_digest()
    harness.apply(digest)

    original_run_as = setup._run_as
    attempts: list[list[str]] = []

    def lose_bootstrap_response(account, argv, **kwargs):
        if argv[2] == "bootstrap-server-agent":
            attempts.append(list(argv))
            if len(attempts) == 1:
                raise ServerSetupError("invalid_product_evidence", "core_bootstrap returned invalid evidence")
        return original_run_as(account, argv, **kwargs)

    monkeypatch.setattr(setup, "_run_as", lose_bootstrap_response)
    reconciled = harness.apply(digest)
    assert {"id": "core_bootstrap", "status": "reconciled_after_response_loss"} in reconciled["steps"]
    assert len(attempts) == 2
    assert attempts[0] == attempts[1]


# --------------------------------------------------------------------------
# Live systemd state reconciliation
# --------------------------------------------------------------------------


def _live_properties(layout: SetupLayout, unit: str, **overrides: str) -> dict[str, str]:
    properties = {
        "LoadState": "loaded",
        "UnitFileState": "enabled",
        "FragmentPath": str(layout.unit(unit)),
        "DropInPaths": "",
        "User": setup.CORE_USER,
        "Group": setup.CORE_USER,
        "NoNewPrivileges": "yes",
        "PrivateDevices": "yes",
        "PrivateTmp": "yes",
        "ProtectHome": "yes",
        "ProtectSystem": "strict",
        "MainPID": "4321",
        "Environment": "HOME=/var/lib/agentnet AGENTNET_UV=/opt/agentnet/bin/uv",
        "ReadWritePaths": str(layout.host(setup.CORE_DATA)),
    }
    properties.update(overrides)
    return properties


def _validate_live(
    layout: SetupLayout,
    monkeypatch: pytest.MonkeyPatch,
    *,
    properties: dict[str, str],
    live_executable: Path = Path("/opt/agentnet/bin/node"),
    live_argv: tuple[str, ...] | None = None,
) -> None:
    expected_argv = (
        "/opt/agentnet/bin/node",
        "/opt/agentnet/npm/bin/agentnet.mjs",
        "serve",
        "--config",
        str(layout.host(setup.CORE_CONFIG)),
        "--host",
        "127.0.0.1",
        "--port",
        str(setup.CORE_PORT),
    )
    monkeypatch.setattr(setup, "_systemd_show", lambda *_args, **_kwargs: properties)
    monkeypatch.setattr(
        setup,
        "_read_live_process_identity",
        lambda _pid: (live_executable, expected_argv if live_argv is None else live_argv),
    )
    setup._validate_systemd_service_runtime(
        Path("/usr/bin/systemctl"),
        unit=setup.CORE_UNIT,
        user=setup.CORE_USER,
        data_root=layout.host(setup.CORE_DATA),
        node_executable=Path("/opt/agentnet/bin/node"),
        agentnet_executable=Path("/opt/agentnet/npm/bin/agentnet.mjs"),
        uv_executable=Path("/opt/agentnet/bin/uv"),
        expected_argv=expected_argv,
        layout=layout,
    )


def test_live_runtime_validation_accepts_only_the_approved_hermetic_binding(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    layout = SetupLayout(tmp_path)
    _validate_live(layout, monkeypatch, properties=_live_properties(layout, setup.CORE_UNIT))


@pytest.mark.parametrize(
    "override",
    [
        {"LoadState": "not-found"},
        {"UnitFileState": "disabled"},
        {"FragmentPath": "/etc/systemd/system/other.service"},
        {"DropInPaths": "/etc/systemd/system/agentnet-core.service.d/override.conf"},
        {"User": "root"},
        {"Group": "root"},
        {"NoNewPrivileges": "no"},
        {"PrivateDevices": "no"},
        {"PrivateTmp": "no"},
        {"ProtectHome": "no"},
        {"ProtectSystem": "full"},
        {"MainPID": "0"},
        {"MainPID": "not-a-pid"},
        {"Environment": "AGENTNET_UV=/usr/bin/uv"},
        {"ReadWritePaths": "/var/lib/other"},
    ],
)
def test_live_runtime_validation_fails_closed_on_drifted_unit_state(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    override: dict[str, str],
) -> None:
    layout = SetupLayout(tmp_path)
    with pytest.raises(ServerSetupError) as exc_info:
        _validate_live(
            layout,
            monkeypatch,
            properties=_live_properties(layout, setup.CORE_UNIT, **override),
        )
    assert exc_info.value.blocker == "service_runtime"


def test_live_runtime_validation_rejects_a_process_outside_the_approved_runtime(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    layout = SetupLayout(tmp_path)
    with pytest.raises(ServerSetupError) as exc_info:
        _validate_live(
            layout,
            monkeypatch,
            properties=_live_properties(layout, setup.CORE_UNIT),
            live_executable=Path("/home/owner/.nvm/versions/node/bin/node"),
        )
    assert exc_info.value.blocker == "service_runtime"
    assert str(exc_info.value) == (
        "managed AgentNet service executable does not match the approved hermetic runtime"
    )

    with pytest.raises(ServerSetupError) as exc_info:
        _validate_live(
            layout,
            monkeypatch,
            properties=_live_properties(layout, setup.CORE_UNIT),
            live_argv=(
                "/opt/agentnet/bin/node",
                "/opt/agentnet/npm/bin/agentnet.mjs",
                "serve",
                "--config",
                "/var/lib/agentnet/other.json",
                "--host",
                "0.0.0.0",
                "--port",
                "8080",
            ),
        )
    assert exc_info.value.blocker == "service_runtime"
    assert str(exc_info.value) == (
        "managed AgentNet service argv does not match the approved hermetic runtime"
    )


def test_lost_systemctl_response_succeeds_only_on_verified_live_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    actions: list[list[str]] = []

    def failing_enable(_executable: Path, arguments: list[str], *, failure_message: str) -> None:
        actions.append(arguments)
        if arguments[0] == "enable":
            raise ServerSetupError("systemd_start", failure_message)

    monkeypatch.setattr(setup, "_run_systemctl", failing_enable)
    reconciled: list[int] = []
    assert setup._run_systemctl_sequence_or_reconcile(
        Path("/usr/bin/systemctl"),
        (["daemon-reload"], ["enable", "--now", setup.APPROVAL_UNIT], ["restart", setup.CORE_UNIT]),
        reconcile=lambda: reconciled.append(1),
    ) == "reconciled_after_response_loss"
    assert len(reconciled) == 1
    assert actions == [
        ["daemon-reload"],
        ["enable", "--now", setup.APPROVAL_UNIT],
        ["restart", setup.CORE_UNIT],
    ]


def test_failed_enable_with_running_but_disabled_unit_cannot_reconcile(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    layout = SetupLayout(tmp_path)

    def failing_enable(
        _executable: Path,
        _arguments: list[str],
        *,
        failure_message: str,
    ) -> None:
        raise ServerSetupError("systemd_start", failure_message)

    monkeypatch.setattr(setup, "_run_systemctl", failing_enable)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_systemctl_sequence_or_reconcile(
            Path("/usr/bin/systemctl"),
            (["enable", "--now", setup.CORE_UNIT],),
            reconcile=lambda: _validate_live(
                layout,
                monkeypatch,
                properties=_live_properties(
                    layout,
                    setup.CORE_UNIT,
                    UnitFileState="disabled",
                ),
            ),
        )
    assert exc_info.value.blocker == "systemd_start"


def test_lost_systemctl_response_fails_closed_when_live_state_is_unhealthy(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def failing(_executable: Path, arguments: list[str], *, failure_message: str) -> None:
        raise ServerSetupError("systemd_start", failure_message)

    def unhealthy() -> None:
        raise ServerSetupError("service_runtime", "unit is not running the approved runtime")

    monkeypatch.setattr(setup, "_run_systemctl", failing)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_systemctl_sequence_or_reconcile(
            Path("/usr/bin/systemctl"),
            (["daemon-reload"],),
            reconcile=unhealthy,
        )
    assert exc_info.value.blocker == "systemd_start"
    assert "failed to start AgentNet managed units" in str(exc_info.value)


def test_healthy_sequence_reports_completed_without_reconciliation(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(setup, "_run_systemctl", lambda *_args, **_kwargs: None)

    def unexpected() -> None:
        raise AssertionError("a healthy sequence must not reconcile")

    assert setup._run_systemctl_sequence_or_reconcile(
        Path("/usr/bin/systemctl"),
        (["daemon-reload"], ["restart", setup.CORE_UNIT]),
        reconcile=unexpected,
    ) == "completed"


def test_systemd_show_parses_only_requested_properties(monkeypatch: pytest.MonkeyPatch) -> None:
    observed: list[list[str]] = []

    def fake_run(argv: list[str], **_kwargs: object) -> SimpleNamespace:
        observed.append(argv)
        return SimpleNamespace(
            returncode=0,
            stdout=b"LoadState=loaded\nUnrequested=leaked\nMainPID=17\nmalformed-line\n",
        )

    monkeypatch.setattr(setup.subprocess, "run", fake_run)
    assert setup._systemd_show(Path("/usr/bin/systemctl"), setup.CORE_UNIT) == {
        "LoadState": "loaded",
        "MainPID": "17",
    }
    assert observed[0][:3] == ["/usr/bin/systemctl", "show", setup.CORE_UNIT]

    monkeypatch.setattr(
        setup.subprocess,
        "run",
        lambda argv, **_kwargs: SimpleNamespace(returncode=1, stdout=b""),
    )
    with pytest.raises(ServerSetupError) as exc_info:
        setup._systemd_show(Path("/usr/bin/systemctl"), setup.CORE_UNIT)
    assert exc_info.value.blocker == "service_runtime"


def test_live_process_identity_is_read_from_the_real_running_process() -> None:
    executable, argv = setup._read_live_process_identity(os.getpid())
    assert executable.is_absolute()
    assert argv and all(isinstance(item, str) for item in argv)
    assert "" not in argv

    with pytest.raises(ServerSetupError) as exc_info:
        setup._read_live_process_identity(-1)
    assert exc_info.value.blocker == "service_runtime"


# --------------------------------------------------------------------------
# Hermetic runtime selection agrees across the launcher and the Python preflight
# --------------------------------------------------------------------------


def _node_script(source: str, *arguments: str) -> subprocess.CompletedProcess[str]:
    node = shutil.which("node")
    assert node is not None
    return subprocess.run(
        [node, "--input-type=module", "-e", source, *arguments],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_launcher_and_python_agree_on_the_managed_sandbox_hidden_roots() -> None:
    module = (Path(__file__).parents[2] / "npm/lib/platform.mjs").as_uri()
    completed = _node_script(
        f"""
          import {{ SERVICE_HIDDEN_ROOTS, serviceVisiblePath }} from {json.dumps(module)};
          console.log(JSON.stringify({{
            roots: SERVICE_HIDDEN_ROOTS,
            hidden: SERVICE_HIDDEN_ROOTS.map((root) => serviceVisiblePath(`${{root}}/bin/node`)),
            visible: serviceVisiblePath("/opt/agentnet-runtime/bin/node"),
            prefixSafe: serviceVisiblePath("/tmpfiles/bin/node"),
          }}));
        """
    )
    assert completed.returncode == 0, completed.stderr
    evidence = json.loads(completed.stdout)
    assert evidence["roots"] == [str(path) for path in setup._PROTECTED_SERVICE_PATHS]
    assert evidence["hidden"] == [False] * len(evidence["roots"])
    assert evidence["visible"] is True
    assert evidence["prefixSafe"] is True


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_launcher_digest_refuses_a_runtime_hidden_by_the_managed_sandbox(
    tmp_path: Path,
) -> None:
    if not str(tmp_path).startswith(("/tmp/", "/var/tmp/")):
        pytest.skip("temporary root is not under a managed-sandbox hidden root")
    request_path = _communication_only_request(tmp_path)
    package_root = tmp_path / "package"
    executable = package_root / "npm/bin/agentnet.mjs"
    executable.parent.mkdir(parents=True)
    executable.write_text("#!/usr/bin/env node\n", encoding="utf-8")
    for name in ("node", "uv", "systemctl", "useradd"):
        (tmp_path / name).write_text(f"{name}\n", encoding="utf-8")
    module = (Path(__file__).parents[2] / "npm/lib/server-setup-preflight.mjs").as_uri()
    completed = subprocess.run(
        [
            str(shutil.which("node")),
            "--input-type=module",
            "-e",
            f"""
              import {{ privilegedApprovalDigest }} from {json.dumps(module)};
              console.log(privilegedApprovalDigest([
                'server-agent', 'setup', '--request', process.argv.at(-1), '--apply',
              ], process.env));
            """,
            str(request_path),
        ],
        env={
            "PATH": "/usr/bin:/bin",
            "SUDO_UID": str(os.geteuid()),
            "AGENTNET_EXECUTABLE": str(executable),
            "AGENTNET_NODE_EXECUTABLE": str(tmp_path / "node"),
            "AGENTNET_PACKAGE_ROOT": str(package_root),
            "AGENTNET_SYSTEMCTL": str(tmp_path / "systemctl"),
            "AGENTNET_USERADD": str(tmp_path / "useradd"),
            "AGENTNET_UV": str(tmp_path / "uv"),
        },
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode != 0
    assert "hidden by the managed service sandbox" in completed.stderr
    assert not completed.stdout.strip()


# --------------------------------------------------------------------------
# Package-owned reset
# --------------------------------------------------------------------------


def _realized_paths(layout: SetupLayout) -> tuple[Path, ...]:
    return (
        *(layout.unit(unit) for unit in setup.MANAGED_UNITS),
        layout.host(setup.CORE_DATA),
        layout.host(setup.APPROVAL_DATA),
        layout.host(setup.C0_RESPONDER_DATA),
        layout.host(setup.SETUP_MARKER),
        layout.host(setup.SETUP_ATTEMPT),
        layout.host(setup.SETUP_RUNTIME_ROOT),
        layout.host(setup.SETUP_UPGRADE_JOURNAL),
        layout.host(setup.SECRET_ROOT),
    )


def _fake_realized_deployment(layout: SetupLayout) -> tuple[Path, Path]:
    setup_root = layout.host(setup.SETUP_ROOT)
    setup_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    setup_root.chmod(0o700)
    for root in (
        layout.host(setup.CORE_DATA),
        layout.host(setup.APPROVAL_DATA),
        layout.host(setup.C0_RESPONDER_DATA),
        layout.host(setup.SETUP_RUNTIME_ROOT),
    ):
        root.mkdir(parents=True, exist_ok=True, mode=0o700)
        root.chmod(0o700)
        owned = root / "owned-state"
        owned.write_text("owned", encoding="utf-8")
        owned.chmod(0o600)
    layout.lock.write_bytes(b"")
    layout.lock.chmod(0o600)
    marker = layout.host(setup.SETUP_MARKER)
    marker.write_text("{}", encoding="utf-8")
    marker.chmod(0o600)
    attempt = layout.host(setup.SETUP_ATTEMPT)
    attempt.write_text("{}", encoding="utf-8")
    attempt.chmod(0o600)
    journal = layout.host(setup.SETUP_UPGRADE_JOURNAL)
    journal.write_text("{}", encoding="utf-8")
    journal.chmod(0o600)
    secret_root = layout.host(setup.SECRET_ROOT)
    secret_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    for name in ("core.env", "approval.env"):
        secret = secret_root / name
        secret.write_text("AGENTNET_APPROVAL_CORE_TOKEN=synthetic\n", encoding="utf-8")
        secret.chmod(0o600)
    layout.core_unit.parent.mkdir(parents=True, exist_ok=True)
    for unit in setup.MANAGED_UNITS:
        layout.unit(unit).write_text(f"[Unit]\n{unit}\n", encoding="utf-8")

    unrelated = layout.host(Path("/etc/unrelated-operator.conf"))
    unrelated.write_text("keep", encoding="utf-8")
    postgres = layout.host(Path("/var/lib/postgresql/16/main"))
    postgres.mkdir(parents=True)
    (postgres / "PG_VERSION").write_text("16\n", encoding="utf-8")
    return unrelated, postgres


def test_reset_proves_units_inactive_before_state_removal(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[list[str]] = []

    def run(argv, **_kwargs):
        calls.append(list(argv))
        return SimpleNamespace(returncode=3 if "is-active" in argv else 0)

    monkeypatch.setattr(reset.subprocess, "run", run)
    assert reset._stop_managed_units(Path("/usr/bin/systemctl")) == [
        setup.CREDENTIAL_RENEW_TIMER,
        setup.C0_RESPONDER_UNIT,
        setup.CREDENTIAL_RENEW_UNIT,
        setup.CORE_UNIT,
        setup.APPROVAL_UNIT,
    ]
    assert [call[1:] for call in calls] == [
        ["disable", "--now", setup.CREDENTIAL_RENEW_TIMER],
        ["is-active", "--quiet", setup.CREDENTIAL_RENEW_TIMER],
        ["reset-failed", setup.CREDENTIAL_RENEW_TIMER],
        ["disable", "--now", setup.C0_RESPONDER_UNIT],
        ["is-active", "--quiet", setup.C0_RESPONDER_UNIT],
        ["reset-failed", setup.C0_RESPONDER_UNIT],
        ["stop", setup.CREDENTIAL_RENEW_UNIT],
        ["is-active", "--quiet", setup.CREDENTIAL_RENEW_UNIT],
        ["reset-failed", setup.CREDENTIAL_RENEW_UNIT],
        ["disable", "--now", setup.CORE_UNIT],
        ["is-active", "--quiet", setup.CORE_UNIT],
        ["reset-failed", setup.CORE_UNIT],
        ["disable", "--now", setup.APPROVAL_UNIT],
        ["is-active", "--quiet", setup.APPROVAL_UNIT],
        ["reset-failed", setup.APPROVAL_UNIT],
    ]


def test_reset_refuses_active_or_unprovable_unit(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        reset.subprocess,
        "run",
        lambda argv, **_kwargs: SimpleNamespace(returncode=0),
    )
    with pytest.raises(ServerSetupResetError) as exc_info:
        reset._stop_managed_units(Path("/usr/bin/systemctl"))
    assert exc_info.value.blocker == "reset_service_stop"


def test_reset_is_idempotent_and_retains_every_external_prerequisite(tmp_path: Path) -> None:
    layout = SetupLayout(tmp_path)
    unrelated, postgres = _fake_realized_deployment(layout)

    first = reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)
    second = reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)

    assert first["state"] == "reset"
    assert second["state"] == "already_absent"
    assert first["external_prerequisites"] == "retained"
    assert first["removed_units"] == sorted(setup.MANAGED_UNITS)
    assert first["retained_service_identities"] == ["agentnet", "agentnet-approval", "agentnet-c0"]
    assert first["authority_granted"] is False
    assert unrelated.read_text(encoding="utf-8") == "keep"
    assert (postgres / "PG_VERSION").read_text(encoding="utf-8") == "16\n"
    for target in _realized_paths(layout):
        assert not os.path.lexists(target)
    assert layout.lock.is_file()
    assert sorted(first["absence_proven_paths"]) == sorted(str(path) for path in _realized_paths(layout))


def test_clean_setup_rejects_unowned_c0_responder_artifact(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    harness = _harness(tmp_path, monkeypatch)
    responder_config = harness.layout.host(setup.C0_RESPONDER_CONFIG)
    responder_config.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    responder_config.parent.chmod(0o700)
    responder_config.write_text("{}", encoding="utf-8")
    responder_config.chmod(0o600)

    with pytest.raises(ServerSetupError) as exc_info:
        harness.apply(harness.plan_digest())
    assert exc_info.value.blocker == "clean_state_required"
    assert not harness.layout.host(setup.SETUP_ATTEMPT).exists()


def test_reset_refuses_managed_state_without_package_lock(tmp_path: Path) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    layout.lock.unlink()
    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(
            layout=layout,
            retain_external_prerequisites=True,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "reset_custody"
    for target in _realized_paths(layout):
        assert os.path.lexists(target)


@pytest.mark.parametrize(
    ("target", "mode"),
    [
        ("lock", 0o644),
        ("core_state", 0o755),
        ("core_unit", 0o600),
    ],
)
def test_reset_refuses_wrong_package_custody(
    tmp_path: Path,
    target: str,
    mode: int,
) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    path = {
        "lock": layout.lock,
        "core_state": layout.host(setup.CORE_DATA),
        "core_unit": layout.core_unit,
    }[target]
    path.chmod(mode)

    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(
            layout=layout,
            retain_external_prerequisites=True,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "reset_custody"
    for realized in _realized_paths(layout):
        assert os.path.lexists(realized)


def test_reset_requires_symlink_attack_resistant_tree_removal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    monkeypatch.setattr(reset.shutil.rmtree, "avoids_symlink_attacks", False)

    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(
            layout=layout,
            retain_external_prerequisites=True,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "unsupported_host"
    for realized in _realized_paths(layout):
        assert os.path.lexists(realized)


def test_reset_refuses_to_remove_external_prerequisites(tmp_path: Path) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(layout=layout, retain_external_prerequisites=False, _allow_test_layout=True)
    assert exc_info.value.blocker == "external_prerequisite_ownership"
    for target in _realized_paths(layout):
        assert os.path.lexists(target)


def test_reset_refuses_a_secret_root_holding_unowned_state(tmp_path: Path) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    operator_secret = layout.host(setup.SECRET_ROOT) / "operator-owned.env"
    operator_secret.write_text("OPERATOR=1\n", encoding="utf-8")
    operator_secret.chmod(0o600)

    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)
    assert exc_info.value.blocker == "reset_allowlist"
    assert operator_secret.exists()
    for target in _realized_paths(layout):
        assert os.path.lexists(target)


@pytest.mark.parametrize("managed", ["core_data", "setup_root", "secret_root"])
def test_reset_refuses_a_symlinked_managed_root(tmp_path: Path, managed: str) -> None:
    layout = SetupLayout(tmp_path)
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "valuable").write_text("keep", encoding="utf-8")
    target = {
        "core_data": layout.host(setup.CORE_DATA),
        "setup_root": layout.host(setup.SETUP_ROOT),
        "secret_root": layout.host(setup.SECRET_ROOT),
    }[managed]
    target.parent.mkdir(parents=True, exist_ok=True)
    target.symlink_to(outside, target_is_directory=True)

    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)
    assert exc_info.value.blocker == "reset_custody"
    assert (outside / "valuable").read_text(encoding="utf-8") == "keep"
    assert os.path.lexists(target)


def test_reset_refuses_a_managed_unit_that_is_not_a_regular_file(tmp_path: Path) -> None:
    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    layout.core_unit.unlink()
    layout.core_unit.symlink_to(tmp_path / "elsewhere.service")

    with pytest.raises(ServerSetupResetError) as exc_info:
        reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)
    assert exc_info.value.blocker == "reset_custody"
    assert os.path.lexists(layout.core_unit)


def test_setup_cannot_start_after_reset_has_locked_clean_up(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    harness.apply(harness.plan_digest())
    reset_holds_lock = threading.Event()
    release_reset = threading.Event()
    original_inventory_gate = reset._require_only_managed_setup_entries

    def pause_after_reset_lock(entries: set[str]) -> None:
        reset_holds_lock.set()
        assert release_reset.wait(timeout=5)
        original_inventory_gate(entries)

    monkeypatch.setattr(reset, "_require_only_managed_setup_entries", pause_after_reset_lock)
    with ThreadPoolExecutor(max_workers=1) as pool:
        future = pool.submit(
            reset_server_setup,
            layout=harness.layout,
            retain_external_prerequisites=True,
            _allow_test_layout=True,
        )
        assert reset_holds_lock.wait(timeout=5)
        try:
            with pytest.raises(ServerSetupError) as exc_info:
                harness.apply(harness.plan_digest())
            assert exc_info.value.blocker == "setup_locked"
        finally:
            release_reset.set()
        evidence = future.result(timeout=5)

    assert evidence["state"] == "reset"
    assert harness.layout.lock.is_file()
    for target in _realized_paths(harness.layout):
        assert not os.path.lexists(target)


def test_reset_refuses_to_run_underneath_an_active_setup(tmp_path: Path) -> None:
    import fcntl

    layout = SetupLayout(tmp_path)
    _fake_realized_deployment(layout)
    layout.lock.write_bytes(b"")
    layout.lock.chmod(0o600)
    descriptor = os.open(layout.lock, os.O_WRONLY)
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    try:
        with pytest.raises(ServerSetupResetError) as exc_info:
            reset_server_setup(layout=layout, retain_external_prerequisites=True, _allow_test_layout=True)
    finally:
        os.close(descriptor)
    assert exc_info.value.blocker == "setup_locked"
    assert os.path.lexists(layout.host(setup.CORE_DATA))


def test_reset_removes_exactly_what_apply_created(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    harness = _harness(tmp_path, monkeypatch)
    harness.apply(harness.plan_digest())
    unrelated = harness.layout.host(Path("/etc/operator.conf"))
    unrelated.write_text("keep", encoding="utf-8")

    evidence = reset_server_setup(
        layout=harness.layout,
        retain_external_prerequisites=True,
        _allow_test_layout=True,
    )
    assert evidence["state"] == "reset"
    assert unrelated.read_text(encoding="utf-8") == "keep"
    for target in _realized_paths(harness.layout):
        assert not os.path.lexists(target)

    # A fresh install after reset is an ordinary first apply, not an upgrade.
    reapplied = harness.apply(harness.plan_digest())
    assert {"id": "package_upgrade", "status": "not_required"} in reapplied["steps"]
    assert harness.marker()["revision"] == 1
