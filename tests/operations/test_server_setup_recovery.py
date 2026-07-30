"""Adversarial setup runtime, reconciliation, upgrade, and reset regressions.

Every test here starts from a failure the fixed server profile must survive:
a lost response, an interrupted upgrade, a drifted realized state, a stale
journal, a live service that is not the approved runtime, or a reset asked to
remove state this package does not own.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import shutil
import stat
import subprocess
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from types import SimpleNamespace

import pytest

import agentnet.operations.server_reset as reset
import agentnet.operations.server_setup as setup
from agentnet.approval.config import MANDATORY_APPROVAL_PURPOSES
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
from agentnet.security.signatures import P256KeyPair
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


@dataclass
class _Harness:
    request: ServerSetupRequest
    layout: SetupLayout
    runtime_generation: list[int]
    product_calls: list[list[str]]

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

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path(f"/opt/agentnet-{generation[0]}/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path(f"/opt/agentnet-{generation[0]}/bin/uv"))
    monkeypatch.setattr(
        setup,
        "_resolve_executable",
        lambda *_args, **_kwargs: Path(f"/opt/agentnet-{generation[0]}/npm/bin/agentnet.mjs"),
    )
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
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
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
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

    monkeypatch.setattr(
        setup,
        "load_config_json",
        lambda _text: SimpleNamespace(
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
            enrolled_harness_id=None,
            enrolled_credential_id=None,
            model_dump=lambda **_kwargs: {"immutable": "fixed"},
        ),
    )

    product_calls: list[list[str]] = []

    def fake_run_as(_account, argv, *, environment, stage, accepted_returncodes=frozenset({0})):
        product_calls.append(list(argv))
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

    monkeypatch.setattr(setup, "_run_as", fake_run_as)
    return _Harness(
        request=request,
        layout=layout,
        runtime_generation=generation,
        product_calls=product_calls,
    )


def _realized_0130_deployment(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> tuple[_Harness, str]:
    """One completed 0.1.30 apply, ready for an upgrade attempt."""

    harness = _harness(tmp_path, monkeypatch)
    monkeypatch.setattr(setup, "__version__", "0.1.30")
    digest = harness.plan_digest()
    harness.apply(digest)
    assert harness.marker()["package_version"] == "0.1.30"
    assert harness.marker()["revision"] == 1
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
        scanner_path = _private_json(
            tmp_path / "scanner-trust.json",
            {
                "trusted_public_keys": {"scanner-key": P256KeyPair.generate().public_pem},
                "required_engine": "synthetic-scanner",
                "required_rules_digest": "a" * 64,
                "required_profile_digest": "b" * 64,
            },
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
