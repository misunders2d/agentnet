from __future__ import annotations

import hashlib
import json
import os
import shutil
import socket
import signal
import threading
import stat
import subprocess
import sys
import time
import traceback
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet.artifacts.clamav import (
    ScannerEndpoint,
    clamav_profile_digest,
    clamav_rules_digest,
)
from agentnet.approval.config import (
    MANDATORY_APPROVAL_PURPOSES,
    ApprovalOwnerOIDCConfig,
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
)
from agentnet.operations.config import IndependentApproverConfig, ScannerTrustConfig
from agentnet.cli import (
    _server_setup_deadline,
    build_parser,
    command_server_agent_setup,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.postgres import ORDINARY_SERVER_POSTGRES_DSN
from agentnet.operations.server_setup import (
    APPROVAL_UNIT,
    CORE_UNIT,
    C0_RESPONDER_UNIT,
    CREDENTIAL_RENEW_TIMER,
    CREDENTIAL_RENEW_UNIT,
    MANAGED_UNITS,
    ServerSetupError,
    SetupApprover,
    SetupLayout,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
    render_units,
)


@pytest.fixture(autouse=True)
def _stable_synthetic_runtime_hashes(
    monkeypatch: pytest.MonkeyPatch,
    request: pytest.FixtureRequest,
) -> None:
    import agentnet.operations.server_setup as setup

    for name in ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(
        setup,
        "_sha256_stable_file",
        lambda path, **_kwargs: hashlib.sha256(str(path).encode()).hexdigest(),
    )
    if request.node.name != "test_host_tool_resolution_uses_fixed_system_path":
        monkeypatch.setattr(
            setup,
            "_resolve_host_tool",
            lambda name: Path(f"/usr/bin/{name}"),
        )
    if request.node.name not in {
        "test_launcher_preflight_digest_matches_python_plan",
        "test_package_tree_digest_rejects_symlinks_and_changes_with_content",
    }:
        monkeypatch.setattr(
            setup,
            "_sha256_stable_tree",
            lambda path: hashlib.sha256(f"tree:{path}".encode()).hexdigest(),
        )
    if request.node.name.startswith("test_scanner_setup_"):
        monkeypatch.setattr(
            setup,
            "_resolve_node_executable",
            lambda: Path("/opt/agentnet-test/bin/node"),
        )
        monkeypatch.setattr(
            setup,
            "_resolve_uv_executable",
            lambda: Path("/opt/agentnet-test/bin/uv"),
        )
        monkeypatch.setattr(
            setup,
            "_resolve_executable",
            lambda *_args, **_kwargs: Path(
                "/opt/agentnet-test/npm/bin/agentnet.mjs"
            ),
        )
    else:
        monkeypatch.setattr(
            setup,
            "_require_scanner_readiness",
            lambda spec: {
                "scanner_id": spec.scanner_id,
                "status": "ready",
            },
        )


def test_setup_layout_keeps_runtime_lock_and_marker_in_persistent_setup_custody(
    tmp_path: Path,
) -> None:
    import agentnet.operations.server_setup as setup

    assert SetupLayout().lock == Path("/var/lib/agentnet-setup/setup.lock")
    layout = SetupLayout(root=tmp_path)
    assert layout.lock == tmp_path / "var/lib/agentnet-setup/setup.lock"
    assert layout.host(setup.SETUP_MARKER) == tmp_path / "var/lib/agentnet-setup/setup.json"


def _bootstrap_evidence(domain_id: str) -> dict[str, object]:
    """Exactly the structured evidence `agentnet bootstrap-server-agent` prints."""

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


def _private_json(path: Path, value: object) -> Path:
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path


def _request(tmp_path: Path) -> Path:
    scanner_endpoint = ScannerEndpoint.from_uri("unix:///run/clamav/clamd.sock")
    scanner_key = P256KeyPair.generate()
    scanner_key_path = tmp_path / "scanner-key.pem"
    scanner_key_path.write_bytes(scanner_key.private_pem)
    scanner_key_path.chmod(0o600)
    signature_updated_at = int(time.time())
    signature_max_age = 172_800
    rules_digest = clamav_rules_digest(
        signature_version="daily-1",
        signature_updated_at=signature_updated_at,
    )
    profile_digest = clamav_profile_digest(
        endpoint=scanner_endpoint,
        engine_version="1.4.3",
        timeout_seconds=30.0,
        max_bytes=16_777_216,
        max_response_bytes=4_096,
        max_signature_age_seconds=signature_max_age,
    )
    core_env = tmp_path / "core.env"
    core_env.write_text(
        f"AGENTNET_DATABASE_URL={ORDINARY_SERVER_POSTGRES_DSN}\n"
        "AGENTNET_CORE_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token-0123456789abcdef0123456789\n"
        f"AGENTNET_CLAMAV_ENDPOINT={scanner_endpoint.uri}\n"
        "AGENTNET_CLAMAV_SCANNER_ID=maintained-scanner\n"
        "AGENTNET_CLAMAV_KEY_EPOCH=1\n"
        f"AGENTNET_CLAMAV_SIGNING_KEY_FILE={scanner_key_path}\n"
        "AGENTNET_CLAMAV_ENGINE_VERSION=1.4.3\n"
        "AGENTNET_CLAMAV_SIGNATURE_VERSION=daily-1\n"
        f"AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT={signature_updated_at}\n"
        f"AGENTNET_CLAMAV_SIGNATURE_MAX_AGE_SECONDS={signature_max_age}\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    approval_env = tmp_path / "approval.env"
    approval_env.write_text(
        "AGENTNET_APPROVAL_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token-0123456789abcdef0123456789\n",
        encoding="utf-8",
    )
    approval_env.chmod(0o600)
    oidc = _private_json(
        tmp_path / "core-oidc.json",
        {
            "issuer": "https://accounts.example",
            "client_id": "core-client",
            "redirect_uri": "https://core.corp.example/v1/enrollment/oidc/callback",
            "token_endpoint_auth_method": "client_secret_post",
            "client_secret_env": "AGENTNET_CORE_OIDC_CLIENT_SECRET",
            "allowed_endpoint_origins": ["https://accounts.example"],
            "allowed_signing_algorithms": ["RS256"],
            "binding_assurance": "hardware_bound",
        },
    )
    owner_oidc = _private_json(
        tmp_path / "owner-oidc.json",
        {
            "issuer": "https://accounts.example",
            "client_id": "approval-client",
            "redirect_uri": "https://approval.corp.example/v1/approval/owner/oidc/callback",
            "token_endpoint_auth_method": "client_secret_post",
            "client_secret_env": "AGENTNET_APPROVAL_OIDC_CLIENT_SECRET",
            "allowed_endpoint_origins": ["https://accounts.example"],
            "allowed_signing_algorithms": ["RS256"],
        },
    )
    approvers = _private_json(
        tmp_path / "approvers.json",
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
    scanner_trust = _private_json(
        tmp_path / "scanner-trust.json",
        {
            "trusted_public_keys": {
                "maintained-scanner:1": scanner_key.public_pem,
            },
            "required_engine": "clamav",
            "required_rules_digest": rules_digest,
            "required_profile_digest": profile_digest,
        },
    )
    return _private_json(
        tmp_path / "setup.json",
        {
            "schema": "agentnet.server-setup.request.v1",
            "profile": "always_on_server_agent",
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
            "scanner_trust_file": str(scanner_trust),
            "approval_approver_principal_id": "owner-principal",
            "approval_verifier_id": "approval.corp.example",
        },
    )


def _artifact_enabled_v2_request(tmp_path: Path) -> Path:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["schema"] = "agentnet.server-setup.request.v2"
    value["artifact_mode"] = "enabled"
    return _private_json(request_path, value)


def _communication_only_request(tmp_path: Path) -> Path:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["schema"] = "agentnet.server-setup.request.v2"
    value["artifact_mode"] = "disabled"
    value.pop("scanner_trust_file")
    core_environment = Path(value["core_environment_file"])
    core_environment.write_text(
        "".join(
            line
            for line in core_environment.read_text(encoding="utf-8").splitlines(
                keepends=True
            )
            if not line.startswith("AGENTNET_CLAMAV_")
        ),
        encoding="utf-8",
    )
    return _private_json(request_path, value)
def _replace_environment_value(path: Path, name: str, value: str) -> None:
    values = {
        key: current
        for key, current in (
            line.split("=", 1)
            for line in path.read_text(encoding="utf-8").splitlines()
        )
    }
    values[name] = value
    path.write_text(
        "".join(f"{key}={current}\n" for key, current in values.items()),
        encoding="utf-8",
    )


def test_scanner_setup_requires_one_local_endpoint(tmp_path: Path) -> None:
    request_path = _artifact_enabled_v2_request(tmp_path)
    request_value = json.loads(request_path.read_text(encoding="utf-8"))
    core_environment = Path(request_value["core_environment_file"])
    _replace_environment_value(
        core_environment,
        "AGENTNET_CLAMAV_ENDPOINT",
        "tcp://scanner.example:3310",
    )

    with pytest.raises(ServerSetupError) as exc_info:
        plan_server_setup(load_server_setup_request(request_path), layout=SetupLayout(tmp_path / "host"))

    assert exc_info.value.blocker == "scanner_configuration"


def test_scanner_setup_rejects_stale_signature_database(tmp_path: Path) -> None:
    request_path = _artifact_enabled_v2_request(tmp_path)
    request_value = json.loads(request_path.read_text(encoding="utf-8"))
    core_environment = Path(request_value["core_environment_file"])
    _replace_environment_value(
        core_environment,
        "AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT",
        str(int(time.time()) - 700_000),
    )

    with pytest.raises(ServerSetupError) as exc_info:
        plan_server_setup(load_server_setup_request(request_path), layout=SetupLayout(tmp_path / "host"))

    assert exc_info.value.blocker == "scanner_signatures_stale"


def test_scanner_setup_rejects_key_outside_private_custody(tmp_path: Path) -> None:
    request_path = _artifact_enabled_v2_request(tmp_path)
    request_value = json.loads(request_path.read_text(encoding="utf-8"))
    core_values = {
        key: value
        for key, value in (
            line.split("=", 1)
            for line in Path(request_value["core_environment_file"])
            .read_text(encoding="utf-8")
            .splitlines()
        )
    }
    Path(core_values["AGENTNET_CLAMAV_SIGNING_KEY_FILE"]).chmod(0o644)

    with pytest.raises(ServerSetupError) as exc_info:
        plan_server_setup(load_server_setup_request(request_path), layout=SetupLayout(tmp_path / "host"))

    assert exc_info.value.blocker == "scanner_key_custody"


def test_scanner_setup_rejects_unpinned_signing_key(tmp_path: Path) -> None:
    request_path = _artifact_enabled_v2_request(tmp_path)
    request_value = json.loads(request_path.read_text(encoding="utf-8"))
    trust_path = Path(request_value["scanner_trust_file"])
    trust = json.loads(trust_path.read_text(encoding="utf-8"))
    trust["trusted_public_keys"]["maintained-scanner:1"] = (
        P256KeyPair.generate().public_pem
    )
    _private_json(trust_path, trust)

    with pytest.raises(ServerSetupError) as exc_info:
        plan_server_setup(load_server_setup_request(request_path), layout=SetupLayout(tmp_path / "host"))

    assert exc_info.value.blocker == "scanner_trust_mismatch"


def test_scanner_setup_requires_live_signed_readiness(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    request_path = _artifact_enabled_v2_request(tmp_path)
    request = load_server_setup_request(request_path)
    spec = setup._server_setup_preflight(
        request,
        layout=SetupLayout(tmp_path / "host"),
    ).scanner_setup
    assert spec is not None

    class ReadyScanner:
        def __init__(self, *_args: object, **_kwargs: object) -> None:
            pass

        def scan(self, **_kwargs: object) -> SimpleNamespace:
            return SimpleNamespace(
                profile_digest=spec.profile_digest,
                result="allow",
                rules_digest=spec.rules_digest,
                scanner_engine="clamav",
                scanner_id=spec.scanner_id,
                scanner_key_epoch=spec.scanner_key_epoch,
                signature="test-signature",
                signed_fields=lambda: {},
            )

    monkeypatch.setattr(setup, "verify_signature", lambda *_args, **_kwargs: None)
    monkeypatch.setattr(setup, "ClamAVScanner", ReadyScanner)
    assert setup._require_scanner_readiness(spec)["status"] == "ready"

    monkeypatch.setattr(
        setup,
        "ClamAVScanner",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(ConnectionError("down")),
    )
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_scanner_readiness(spec)
    assert exc_info.value.blocker == "scanner_unready"


def test_scanner_setup_accepts_one_exact_live_loopback_daemon(
    tmp_path: Path,
) -> None:
    """The deployed lane's exact TCP configuration must satisfy apply readiness."""

    import agentnet.operations.server_setup as setup

    responses: list[bytes] = [b"stream: OK\x00"]
    received: list[bytes] = []

    stop = threading.Event()
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", 0))
    listener.listen(4)
    listener.settimeout(0.1)
    port = listener.getsockname()[1]

    def serve() -> None:
        while not stop.is_set():
            try:
                connection, _peer = listener.accept()
            except TimeoutError:
                continue
            except OSError:
                return
            with connection:
                payload = bytearray()
                connection.settimeout(5)
                while not payload.endswith(b"\x00\x00\x00\x00"):
                    chunk = connection.recv(4096)
                    if not chunk:
                        break
                    payload.extend(chunk)
                received.append(bytes(payload))
                connection.sendall(responses[0])

    server = threading.Thread(target=serve)
    server.start()
    try:
        key = P256KeyPair.generate()
        key_path = tmp_path / "scanner.key"
        key_path.write_bytes(key.private_pem)
        key_path.chmod(0o600)
        scanner_id = "ordinary-server-e2e-scanner"
        epoch = 1
        engine_version = "1.0.7"
        signature_version = "27000"
        updated_at = int(time.time())
        max_age = 86_400
        endpoint_uri = f"tcp://127.0.0.1:{port}"
        core_values = {
            "AGENTNET_CLAMAV_ENDPOINT": endpoint_uri,
            "AGENTNET_CLAMAV_SCANNER_ID": scanner_id,
            "AGENTNET_CLAMAV_KEY_EPOCH": str(epoch),
            "AGENTNET_CLAMAV_SIGNING_KEY_FILE": str(key_path),
            "AGENTNET_CLAMAV_ENGINE_VERSION": engine_version,
            "AGENTNET_CLAMAV_SIGNATURE_VERSION": signature_version,
            "AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT": str(updated_at),
            "AGENTNET_CLAMAV_SIGNATURE_MAX_AGE_SECONDS": str(max_age),
        }
        trust = ScannerTrustConfig.model_validate(
            {
                "trusted_public_keys": {f"{scanner_id}:{epoch}": key.public_pem},
                "required_engine": "clamav",
                "required_rules_digest": clamav_rules_digest(
                    signature_version=signature_version,
                    signature_updated_at=updated_at,
                ),
                "required_profile_digest": clamav_profile_digest(
                    endpoint=ScannerEndpoint.from_uri(endpoint_uri),
                    engine_version=engine_version,
                    timeout_seconds=30.0,
                    max_bytes=16_777_216,
                    max_response_bytes=4_096,
                    max_signature_age_seconds=max_age,
                ),
            }
        )
        spec = setup._resolve_scanner_setup(
            load_server_setup_request(_artifact_enabled_v2_request(tmp_path)),
            core_values=core_values,
            scanner_trust=trust,
        )
        assert spec is not None

        evidence = setup._require_scanner_readiness(spec)

        assert evidence["status"] == "ready"
        assert evidence["endpoint"] == endpoint_uri
        assert evidence["scanner_id"] == scanner_id
        assert received and received[0].startswith(b"zINSTREAM\x00")

        responses[0] = b"stream: Eicar-Test-Signature FOUND\x00"
        with pytest.raises(ServerSetupError) as exc_info:
            setup._require_scanner_readiness(spec)
        assert exc_info.value.blocker == "scanner_unready"
    finally:
        stop.set()
        server.join(timeout=5)
        listener.close()
        assert not server.is_alive()


def test_server_setup_is_one_fixed_public_cli_surface(tmp_path: Path) -> None:
    request = _request(tmp_path)
    parser = build_parser()
    planned = parser.parse_args(["server-agent", "setup", "--request", str(request)])
    applied = parser.parse_args(
        [
            "server-agent",
            "setup",
            "--request",
            str(request),
            "--apply",
            "--start",
            "--expected-request-digest",
            "a" * 64,
        ]
    )
    assert planned.func is command_server_agent_setup
    assert planned.apply is False and planned.start is False
    assert applied.func is command_server_agent_setup
    assert applied.apply is True and applied.start is True
    assert applied.expected_request_digest == "a" * 64


def test_server_setup_guided_mode_needs_no_request_or_digest_arguments() -> None:
    parsed = build_parser().parse_args(["server-agent", "setup"])

    assert parsed.request is None
    assert parsed.apply is False
    assert parsed.start is False
    assert parsed.expected_request_digest is None


def test_guided_server_setup_refuses_noninteractive_approval(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    from agentnet.cli.commands import server_agent as cli_setup

    request = object()
    monkeypatch.setattr(cli_setup, "load_server_setup_request", lambda _path: request)
    monkeypatch.setattr(
        cli_setup,
        "plan_server_setup",
        lambda candidate: {
            "schema": "agentnet.server-setup.evidence.v1",
            "status": "planned",
            "request_digest": "a" * 64,
            "package_version": "0.1.50",
            "profile": "always_on_server_agent",
        }
        if candidate is request
        else pytest.fail("guided setup changed the loaded request"),
    )
    monkeypatch.setattr(cli_setup.sys.stdin, "isatty", lambda: False)

    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup", "--apply", "--start"])
    )

    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["blocker"] == "plan_approval_required"
    assert output["authority_granted"] is False
    assert output["phase"] == "approve"
    assert output["rerun_resumes"] is True
    assert output["responsible_component"] == "agentnet-server-setup"


def test_guided_server_setup_approves_and_applies_exact_planned_digest(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    from agentnet.cli.commands import server_agent as cli_setup

    request = object()
    applied: list[tuple[object, bool, str]] = []

    class ApprovingTerminal:
        def isatty(self) -> bool:
            return True

        def readline(self) -> str:
            return "yes\n"

    monkeypatch.setattr(cli_setup, "load_server_setup_request", lambda _path: request)
    monkeypatch.setattr(
        cli_setup,
        "plan_server_setup",
        lambda candidate: {
            "schema": "agentnet.server-setup.evidence.v1",
            "status": "planned",
            "request_digest": "b" * 64,
            "package_version": "0.1.50",
            "profile": "always_on_server_agent",
            "managed_units": ["agentnet-core.service"],
        }
        if candidate is request
        else pytest.fail("guided setup changed the loaded request"),
    )

    def apply(candidate: object, *, start: bool, expected_request_digest: str) -> dict[str, object]:
        applied.append((candidate, start, expected_request_digest))
        return {
            "schema": "agentnet.server-setup.evidence.v1",
            "status": "applied",
            "request_digest": expected_request_digest,
        }

    monkeypatch.setattr(cli_setup, "apply_server_setup", apply)
    monkeypatch.setattr(cli_setup.sys, "stdin", ApprovingTerminal())

    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup", "--apply", "--start"])
    )

    captured = capsys.readouterr()
    output = json.loads(captured.out)
    assert rc == 0
    assert output["status"] == "applied"
    assert output["request_digest"] == "b" * 64
    assert applied == [(request, True, "b" * 64)]
    assert "Apply this exact AgentNet server setup plan? [yes/no]" in captured.err
    for phase in ("discover", "plan", "approve", "apply", "verify"):
        assert f"phase={phase}" in captured.err


def test_guided_server_setup_reports_missing_standard_prerequisite(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    from agentnet.cli.commands import server_agent as cli_setup

    monkeypatch.setattr(cli_setup, "SETUP_ROOT", tmp_path / "missing")

    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup"])
    )

    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["phase"] == "discover"
    assert output["blocker"] == "missing_external_prerequisite"
    assert output["rerun_resumes"] is True


@pytest.mark.skipif(not hasattr(signal, "setitimer"), reason="POSIX timers unavailable")
def test_guided_server_setup_deadline_is_typed_and_bounded() -> None:

    with pytest.raises(ServerSetupError) as exc_info:
        with _server_setup_deadline(0.01):
            time.sleep(0.1)

    assert exc_info.value.blocker == "setup_deadline_exceeded"
    assert "rerun resumes exact state" in str(exc_info.value)


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_launcher_preflight_digest_matches_python_plan(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    v1_root = tmp_path / "v1"
    v2_enabled_root = tmp_path / "v2-enabled"
    v2_disabled_root = tmp_path / "v2-disabled"
    v1_root.mkdir()
    v2_enabled_root.mkdir()
    v2_disabled_root.mkdir()
    request_paths = (
        _request(v1_root),
        _artifact_enabled_v2_request(v2_enabled_root),
        _communication_only_request(v2_disabled_root),
    )
    requests = tuple(load_server_setup_request(path) for path in request_paths)
    import agentnet.operations.server_setup as setup

    node = Path(str(shutil.which("node"))).resolve()
    uv_value = shutil.which("uv")
    if uv_value is None:
        pytest.skip("uv is unavailable")
    uv = Path(uv_value).resolve()
    package_root = tmp_path / "package"
    executable = package_root / "npm/bin/agentnet.mjs"
    systemctl = tmp_path / "systemctl"
    useradd = tmp_path / "useradd"
    systemctl.write_bytes(b"systemctl-v1")
    useradd.write_bytes(b"useradd-v1")
    executable.parent.mkdir(parents=True)
    shutil.copy2(Path(__file__).parents[2] / "npm/bin/agentnet.mjs", executable)
    (package_root / "src/agentnet").mkdir(parents=True)
    (package_root / "src/agentnet/runtime.py").write_text("RUNTIME = 'fixed'\n", encoding="utf-8")
    executable = executable.resolve()
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(
        setup,
        "_resolve_host_tool",
        lambda name: {"systemctl": systemctl, "useradd": useradd}[name],
    )
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: node)
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: uv)
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: executable)
    monkeypatch.setattr(
        setup,
        "_sha256_stable_file",
        lambda path, **_kwargs: hashlib.sha256(path.read_bytes()).hexdigest(),
    )
    module = (Path(__file__).parents[2] / "npm/lib/server-setup-preflight.mjs").as_uri()
    # The hermetic fixture tree lives under the test temporary root, so this
    # exercises the digest contract with the sandbox-root rule relaxed; the
    # default rule itself is proven by the focused rejection tests below.
    script = f"""
      import {{ privilegedApprovalDigest }} from {json.dumps(module)};
      const request = process.argv.at(-1);
      console.log(privilegedApprovalDigest([
        'server-agent', 'setup', '--request', request, '--apply',
      ], process.env, {{ serviceHiddenRoots: ['/home', '/root', '/run/user'] }}));
    """
    digest_environment = {
        "PATH": "/usr/bin:/bin",
        "SUDO_UID": str(os.geteuid()),
        "AGENTNET_EXECUTABLE": str(executable),
        "AGENTNET_NODE_EXECUTABLE": str(node),
        "AGENTNET_PACKAGE_ROOT": str(package_root),
        "AGENTNET_SYSTEMCTL": str(systemctl),
        "AGENTNET_USERADD": str(useradd),
        "AGENTNET_UV": str(uv),
    }
    expected_by_path: dict[Path, str] = {}
    for request_path, request in zip(request_paths, requests, strict=True):
        planned = plan_server_setup(request)
        expected = planned["request_digest"]
        expected_by_path[request_path] = expected
        assert planned["artifact_mode"] == request.effective_artifact_mode
        assert planned["prerequisites"]["artifact_scanner"] == (
            "validated_required"
            if request.effective_artifact_mode == "enabled"
            else "disabled_not_required"
        )
        completed = subprocess.run(
            [str(shutil.which("node")), "--input-type=module", "-e", script, str(request_path)],
            env=digest_environment,
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )
        assert completed.returncode == 0, completed.stderr
        assert completed.stdout.strip() == expected

    runtime_file = package_root / "src/agentnet/runtime.py"
    runtime_file.write_text("RUNTIME = 'changed'\n", encoding="utf-8")
    for request_path, request in zip(request_paths, requests, strict=True):
        changed = plan_server_setup(request)["request_digest"]
        changed_completed = subprocess.run(
            [str(shutil.which("node")), "--input-type=module", "-e", script, str(request_path)],
            env=digest_environment,
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )
        assert changed != expected_by_path[request_path]
        assert changed_completed.returncode == 0, changed_completed.stderr
        assert changed_completed.stdout.strip() == changed

    missing_schema = json.loads(request_paths[0].read_text(encoding="utf-8"))
    missing_schema.pop("schema")
    _private_json(request_paths[0], missing_schema)
    rejected = subprocess.run(
        [str(shutil.which("node")), "--input-type=module", "-e", script, str(request_paths[0])],
        env=digest_environment,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert rejected.returncode != 0
    assert "setup request schema is invalid" in rejected.stderr


def test_package_tree_digest_rejects_symlinks_and_changes_with_content(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    root = tmp_path / "package"
    root.mkdir()
    runtime = root / "runtime.py"
    runtime.write_text("VALUE = 1\n", encoding="utf-8")
    first = setup._sha256_stable_tree(root)
    runtime.write_text("VALUE = 2\n", encoding="utf-8")
    assert setup._sha256_stable_tree(root) != first
    (root / "linked.py").symlink_to(runtime)
    with pytest.raises(ServerSetupError, match="symbolic link"):
        setup._sha256_stable_tree(root)


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_launcher_executable_digest_rejects_in_place_change(tmp_path: Path) -> None:
    runtime = tmp_path / "runtime.bin"
    runtime.write_bytes(b"a" * 2_097_152)
    module = (Path(__file__).parents[2] / "npm/lib/server-setup-preflight.mjs").as_uri()
    script = f"""
      import {{ stableExecutableSha256 }} from {json.dumps(module)};
      import {{ readSync, writeFileSync }} from 'node:fs';
      const runtime = process.argv.at(-1);
      let changed = false;
      try {{
        stableExecutableSha256(runtime, (...args) => {{
          const count = readSync(...args);
          if (count > 0 && !changed) {{
            changed = true;
            writeFileSync(runtime, Buffer.from('x'));
          }}
          return count;
        }});
      }} catch (error) {{
        console.log(error.message);
      }}
    """
    completed = subprocess.run(
        [str(shutil.which("node")), "--input-type=module", "-e", script, str(runtime)],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == "setup runtime executable changed during preflight"


def test_private_input_rejects_fifo_without_blocking(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    fifo = tmp_path / "input"
    os.mkfifo(fifo, mode=0o600)
    with pytest.raises(ServerSetupError, match="bounded owner-only file"):
        setup._read_private_input(fifo, label="test input")


def test_private_input_detects_same_size_in_place_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    path = tmp_path / "input.json"
    path.write_bytes(b'{"a":1}')
    path.chmod(0o600)
    metadata = path.stat()
    stable_metadata = SimpleNamespace(
        **{
            field: getattr(metadata, field)
            for field in (
                "st_mode",
                "st_uid",
                "st_gid",
                "st_nlink",
                "st_size",
                "st_dev",
                "st_ino",
                "st_mtime_ns",
                "st_ctime_ns",
            )
        }
    )
    original_read = setup.os.read
    changed = False

    def mutate_after_read(descriptor: int, size: int) -> bytes:
        nonlocal changed
        payload = original_read(descriptor, size)
        if payload and not changed:
            changed = True
            path.write_bytes(b'{"b":2}')
            path.chmod(0o600)
        return payload

    monkeypatch.setattr(setup.os, "fstat", lambda _descriptor: stable_metadata)
    monkeypatch.setattr(setup.os, "read", mutate_after_read)
    with pytest.raises(ServerSetupError, match="changed while being read"):
        setup._read_private_input(path, label="test input")


def test_private_input_accumulates_short_reads(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    path = tmp_path / "input.json"
    path.write_bytes(b'{"stable":true}')
    path.chmod(0o600)
    original_read = setup.os.read
    monkeypatch.setattr(
        setup.os,
        "read",
        lambda descriptor, size: original_read(descriptor, min(size, 2)),
    )

    assert setup._read_private_input(path, label="test input") == b'{"stable":true}'


def test_private_input_seek_error_fails_closed(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    path = tmp_path / "input.json"
    path.write_bytes(b'{"stable":true}')
    path.chmod(0o600)
    monkeypatch.setattr(
        setup.os,
        "lseek",
        lambda *_args: (_ for _ in ()).throw(OSError("synthetic seek failure")),
    )

    with pytest.raises(ServerSetupError, match="changed while being read") as exc_info:
        setup._read_private_input(path, label="test input")
    assert exc_info.value.blocker == "unsafe_input"


def test_account_requires_one_private_nonprivileged_group(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(
        pw_name="agentnet",
        pw_uid=1234,
        pw_gid=1234,
        pw_dir=str(tmp_path),
        pw_shell="/usr/sbin/nologin",
    )
    monkeypatch.setattr(setup.grp, "getgrgid", lambda _gid: SimpleNamespace(gr_name="agentnet", gr_mem=[]))
    monkeypatch.setattr(setup.pwd, "getpwall", lambda: [account])
    monkeypatch.setattr(setup.os, "getgrouplist", lambda _name, _gid: [1234])
    setup._validate_account(account, "agentnet", tmp_path)

    monkeypatch.setattr(setup.os, "getgrouplist", lambda _name, _gid: [1234, 9999])
    with pytest.raises(ServerSetupError, match="account conflicts"):
        setup._validate_account(account, "agentnet", tmp_path)


def test_account_creation_rejects_preexisting_same_named_group(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(
        setup.pwd,
        "getpwnam",
        lambda _name: (_ for _ in ()).throw(KeyError),
    )
    monkeypatch.setattr(
        setup.grp,
        "getgrnam",
        lambda name: SimpleNamespace(gr_name=name, gr_gid=1234, gr_mem=[]),
    )

    with pytest.raises(ServerSetupError, match="group conflicts") as exc_info:
        setup._account_fact("agentnet-c0", tmp_path)
    assert exc_info.value.blocker == "identity_conflict"


def test_identity_drop_clears_supplementary_groups(monkeypatch: pytest.MonkeyPatch) -> None:
    import agentnet.operations.server_setup as setup

    calls: list[tuple[str, object]] = []
    monkeypatch.setattr(setup.os, "setgroups", lambda value: calls.append(("groups", value)))
    monkeypatch.setattr(setup.os, "setgid", lambda value: calls.append(("gid", value)))
    monkeypatch.setattr(setup.os, "setuid", lambda value: calls.append(("uid", value)))
    setup._drop_identity(SimpleNamespace(pw_uid=1234, pw_gid=5678))()
    assert calls == [("groups", []), ("gid", 5678), ("uid", 1234)]


def test_atomic_write_rejects_fifo_and_oversized_conflicts(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    fifo = tmp_path / "managed"
    os.mkfifo(fifo, mode=0o600)
    with pytest.raises(ServerSetupError, match="managed AgentNet path conflicts"):
        setup._atomic_write(fifo, b"fixed", mode=0o600, uid=os.geteuid(), gid=os.getegid())
    fifo.unlink()
    fifo.write_bytes(b"x" * 1_048_576)
    fifo.chmod(0o600)
    with pytest.raises(ServerSetupError, match="managed AgentNet path conflicts"):
        setup._atomic_write(fifo, b"fixed", mode=0o600, uid=os.geteuid(), gid=os.getegid())


def test_private_managed_file_custody_rejects_mode_and_symlink(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    target = tmp_path / "managed.json"
    target.write_text("{}", encoding="utf-8")
    target.chmod(0o600)
    setup._require_private_file(target, account, blocker="test_custody")

    target.chmod(0o644)
    with pytest.raises(ServerSetupError, match="custody conflicts"):
        setup._require_private_file(target, account, blocker="test_custody")

    target.chmod(0o600)
    linked = tmp_path / "linked.json"
    linked.symlink_to(target)
    with pytest.raises(ServerSetupError, match="custody conflicts"):
        setup._require_private_file(linked, account, blocker="test_custody")


def test_terminal_replacement_requires_and_reports_exact_supersession_journal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup
    from agentnet.operations.c0_credential_supersession import (
        append_supersession,
        canonical_supersession_journal,
    )

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    terminal = {
        "schema": "agentnet.c0-pilot-responder.terminal.v1",
        "status": "COMPLETED_C0_ROUND_TRIP",
        "domain_id": "corp.example",
        "harness_id": "server-harness",
        "credential_id": "credential-1",
    }
    terminal_path = _private_json(tmp_path / "terminal.json", terminal)
    terminal_raw = terminal_path.read_bytes()
    journal = append_supersession(
        terminal_raw=terminal_raw,
        existing=None,
        domain_id="corp.example",
        principal_id="owner-principal",
        terminal_credential_epoch=1,
        harness_id="server-harness",
        request_id="00000000-0000-4000-8000-000000000001",
        transaction_sha256="a" * 64,
        approval_receipt_id="receipt-1",
        approval_receipt_sha256="b" * 64,
        audit_record_hash="c" * 64,
        prior_journal_sha256=None,
        previous_credential_id="credential-1",
        credential_id="credential-2",
        previous_credential_epoch=1,
        credential_epoch=2,
        key_id="key-thumbprint-1",
        not_before=100,
        expires_at=200,
    )
    journal_raw = canonical_supersession_journal(journal)
    journal_path = tmp_path / "credential-supersessions.json"
    journal_path.write_bytes(journal_raw)
    journal_path.chmod(0o600)
    config = SimpleNamespace(
        domain_id="corp.example",
        enrolled_harness_id="server-harness",
        enrolled_credential_id="credential-2",
    )
    audit_calls: list[dict[str, object]] = []

    def verify_audit(
        _account: object,
        database_url: str,
        **kwargs: object,
    ) -> dict[str, object]:
        audit_calls.append({"database_url": database_url, **kwargs})
        return {
            "ready": True,
            "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
            "transition_count": 1,
            "audit_records_verified": 1,
            "credential_id": "credential-2",
            "credential_epoch": 2,
        }

    monkeypatch.setattr(setup, "_run_supersession_audit_as", verify_audit)

    marker, evidence = setup._validated_c0_terminal_marker(
        terminal_path,
        account,
        config=config,
        principal_id="owner-principal",
        credential_epoch=2,
        supersession_path=journal_path,
        core_account=account,
        database_url="postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
    )

    assert marker == terminal
    assert evidence == {
        "status": "verified",
        "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
        "transition_count": 1,
        "audit_records_verified": 1,
        "credential_id": "credential-2",
        "credential_epoch": 2,
    }
    assert audit_calls == [
        {
            "database_url": "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
            "journal_raw": journal_raw,
            "terminal_raw": terminal_raw,
            "domain_id": "corp.example",
            "principal_id": "owner-principal",
            "harness_id": "server-harness",
        }
    ]

    def reject_audit(*_args: object, **_kwargs: object) -> dict[str, object]:
        raise ServerSetupError(
            "c0_credential_supersession",
            "credential supersession audit could not be proven exact",
        )

    monkeypatch.setattr(setup, "_run_supersession_audit_as", reject_audit)
    with pytest.raises(ServerSetupError, match="audit could not be proven exact"):
        setup._validated_c0_terminal_marker(
            terminal_path,
            account,
            config=config,
            principal_id="owner-principal",
            credential_epoch=2,
            supersession_path=journal_path,
            core_account=account,
            database_url="postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
        )


def test_terminal_replacement_without_supersession_journal_fails_closed(
    tmp_path: Path,
) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    terminal_path = _private_json(
        tmp_path / "terminal.json",
        {
            "schema": "agentnet.c0-pilot-responder.terminal.v1",
            "status": "COMPLETED_C0_ROUND_TRIP",
            "domain_id": "corp.example",
            "harness_id": "server-harness",
            "credential_id": "credential-1",
        },
    )
    config = SimpleNamespace(
        domain_id="corp.example",
        enrolled_harness_id="server-harness",
        enrolled_credential_id="credential-2",
    )

    with pytest.raises(ServerSetupError, match="lacks valid supersession provenance"):
        setup._validated_c0_terminal_marker(
            terminal_path,
            account,
            config=config,
            principal_id="owner-principal",
            credential_epoch=2,
            supersession_path=tmp_path / "missing.json",
            core_account=account,
            database_url="postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
        )


def test_private_tree_custody_rejects_symlinks_and_nonregular_entries(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    root = tmp_path / "managed"
    nested = root / "nested"
    nested.mkdir(parents=True, mode=0o700)
    root.chmod(0o700)
    nested.chmod(0o700)
    private_file = nested / "fixed.json"
    private_file.write_text("{}", encoding="utf-8")
    private_file.chmod(0o600)
    setup._require_private_tree(root, account, blocker="test_custody")

    linked = nested / "linked"
    linked.symlink_to(private_file)
    with pytest.raises(ServerSetupError, match="unsupported entry"):
        setup._require_private_tree(root, account, blocker="test_custody")
    linked.unlink()

    fifo = nested / "fifo"
    os.mkfifo(fifo, mode=0o600)
    with pytest.raises(ServerSetupError, match="unsupported entry"):
        setup._require_private_tree(root, account, blocker="test_custody")


def test_private_managed_read_detects_same_size_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    path = tmp_path / "managed.json"
    path.write_bytes(b'{"a":1}')
    path.chmod(0o600)
    metadata = path.stat()
    stable_metadata = SimpleNamespace(
        **{
            field: getattr(metadata, field)
            for field in (
                "st_mode",
                "st_uid",
                "st_gid",
                "st_nlink",
                "st_size",
                "st_dev",
                "st_ino",
                "st_mtime_ns",
                "st_ctime_ns",
            )
        }
    )
    original_read = setup.os.read
    changed = False

    def mutate_after_read(descriptor: int, size: int) -> bytes:
        nonlocal changed
        payload = original_read(descriptor, size)
        if payload and not changed:
            changed = True
            path.write_bytes(b'{"b":2}')
            path.chmod(0o600)
        return payload

    monkeypatch.setattr(setup.os, "fstat", lambda _descriptor: stable_metadata)
    monkeypatch.setattr(setup.os, "read", mutate_after_read)
    with pytest.raises(ServerSetupError, match="changed while being read"):
        setup._read_private_managed_file(
            path,
            account,
            blocker="test_custody",
            max_bytes=1024,
        )


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_node_private_input_detects_same_size_change(tmp_path: Path) -> None:
    path = tmp_path / "input.json"
    path.write_bytes(b'{"a":1}')
    path.chmod(0o600)
    module = (Path(__file__).parents[2] / "npm/lib/server-setup-preflight.mjs").as_uri()
    script = f"""
      import {{ chmodSync, fstatSync, readSync, writeFileSync }} from 'node:fs';
      import {{ readPrivateSetupInput }} from {json.dumps(module)};
      const target = process.argv.at(-1);
      const environment = {{ SUDO_UID: String(process.getuid?.() ?? 0) }};
      const stable = readPrivateSetupInput(
        target,
        1024,
        environment,
        (descriptor, buffer, offset, length, position) => readSync(
          descriptor,
          buffer,
          offset,
          Math.min(length, 2),
          position,
        ),
      );
      console.log(stable.toString('utf8'));
      let changed = false;
      let frozenMetadata;
      const stableStat = (descriptor, options) => {{
        frozenMetadata ??= fstatSync(descriptor, options);
        return frozenMetadata;
      }};
      try {{
        readPrivateSetupInput(
          target,
          1024,
          environment,
          (...args) => {{
            const count = readSync(...args);
            if (count > 0 && !changed) {{
              changed = true;
              writeFileSync(target, Buffer.from('{{"b":2}}'));
              chmodSync(target, 0o600);
            }}
            return count;
          }},
          stableStat,
        );
      }} catch (error) {{
        console.log(error.message);
      }}
    """
    completed = subprocess.run(
        [str(shutil.which("node")), "--input-type=module", "-e", script, str(path)],
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.splitlines() == [
        '{"a":1}',
        "setup input changed while being read",
    ]


def test_non_root_owned_launcher_is_rejected_deterministically(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    launcher = Path("/opt/agentnet-runtime/bin/agentnet")
    monkeypatch.setattr(Path, "resolve", lambda self, strict=True: self)
    monkeypatch.setattr(Path, "is_file", lambda self: self == launcher)
    monkeypatch.setattr(Path, "is_dir", lambda self: self != launcher)
    monkeypatch.setattr(
        Path,
        "stat",
        lambda self: SimpleNamespace(
            st_uid=1234 if self == launcher else 0,
            st_mode=(stat.S_IFREG | 0o755) if self == launcher else (stat.S_IFDIR | 0o755),
        ),
    )
    monkeypatch.setattr(setup.os, "access", lambda *_args: True)
    with pytest.raises(ServerSetupError, match="ownership is unsafe") as exc_info:
        setup._require_root_owned_executable(launcher, label="agentnet")
    assert exc_info.value.blocker == "unsafe_executable"


@pytest.mark.parametrize(
    "hidden",
    ["/home/owner/.nvm/bin/node", "/root/.local/bin/uv", "/run/user/1000/node", "/tmp/node", "/var/tmp/uv"],
)
def test_runtime_hidden_by_the_managed_sandbox_is_rejected(hidden: str) -> None:
    import agentnet.operations.server_setup as setup

    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_service_visible_path(Path(hidden), label="Node.js")
    assert exc_info.value.blocker == "service_executable_inaccessible"


@pytest.mark.parametrize(
    ("variable", "resolver", "label"),
    [
        ("AGENTNET_NODE_EXECUTABLE", "_resolve_node_executable", "Node.js"),
        ("AGENTNET_UV", "_resolve_uv_executable", "uv"),
    ],
)
def test_runtime_selection_is_package_owned_and_never_uses_ambient_path(
    monkeypatch: pytest.MonkeyPatch,
    variable: str,
    resolver: str,
    label: str,
) -> None:
    import agentnet.operations.server_setup as setup

    def unexpected_which(*_args: object, **_kwargs: object) -> str:
        raise AssertionError("hermetic runtime selection must not search PATH")

    monkeypatch.setattr(setup.shutil, "which", unexpected_which)
    monkeypatch.setattr(setup, "_require_root_owned_executable", lambda value, **_kwargs: value)
    monkeypatch.delenv(variable, raising=False)
    with pytest.raises(ServerSetupError) as missing:
        getattr(setup, resolver)()
    assert missing.value.blocker == "missing_package_provenance"

    for unsafe in (f"relative/{label}", f"/opt/agentnet-runtime/../{label}", f"/opt/agentnet-runtime//{label}"):
        monkeypatch.setenv(variable, unsafe)
        with pytest.raises(ServerSetupError) as rejected:
            getattr(setup, resolver)()
        assert rejected.value.blocker == "unsafe_executable"

    monkeypatch.setenv(variable, "/opt/agentnet-runtime/bin/runtime")
    assert getattr(setup, resolver)() == Path("/opt/agentnet-runtime/bin/runtime")

    monkeypatch.setattr(
        setup,
        "_require_root_owned_executable",
        lambda value, **_kwargs: Path("/opt/other/runtime"),
    )
    with pytest.raises(ServerSetupError) as linked:
        getattr(setup, resolver)()
    assert linked.value.blocker == "unsafe_executable"


def test_root_owned_nontraversable_launcher_is_rejected_deterministically(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    target = Path("/opt/agentnet-runtime/bin/agentnet")
    monkeypatch.setattr(Path, "resolve", lambda self, strict=True: self)
    monkeypatch.setattr(Path, "is_file", lambda self: self == target)
    monkeypatch.setattr(Path, "is_dir", lambda self: self != target)
    monkeypatch.setattr(
        Path,
        "stat",
        lambda self: SimpleNamespace(
            st_uid=0,
            st_mode=(stat.S_IFREG | 0o755) if self == target else (stat.S_IFDIR | 0o700),
        ),
    )
    monkeypatch.setattr(setup.os, "access", lambda *_args: True)

    with pytest.raises(ServerSetupError, match="dedicated service identities") as exc_info:
        setup._require_root_owned_executable(target, label="agentnet")
    assert exc_info.value.blocker == "service_executable_inaccessible"


def test_executable_lineage_accepts_traversable_nonwritable_directories(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    target = Path("/opt/agentnet-runtime/bin/node")
    monkeypatch.setattr(Path, "resolve", lambda self, strict=True: self)
    monkeypatch.setattr(Path, "is_file", lambda self: self == target)
    monkeypatch.setattr(Path, "is_dir", lambda self: self != target)
    monkeypatch.setattr(
        Path,
        "stat",
        lambda self: SimpleNamespace(
            st_uid=0,
            st_mode=(stat.S_IFREG | 0o755) if self == target else (stat.S_IFDIR | 0o711),
        ),
    )
    monkeypatch.setattr(setup.os, "access", lambda *_args: True)

    assert setup._require_root_owned_executable(target, label="Node.js") == target


def test_request_versions_bind_artifact_mode_without_reinterpreting_v1(tmp_path: Path) -> None:
    legacy_root = tmp_path / "legacy"
    enabled_root = tmp_path / "enabled"
    communication_root = tmp_path / "communication"
    legacy_root.mkdir()
    enabled_root.mkdir()
    communication_root.mkdir()
    legacy_path = _request(legacy_root)
    legacy = load_server_setup_request(legacy_path)
    assert legacy.schema_version == "agentnet.server-setup.request.v1"
    assert legacy.effective_artifact_mode == "enabled"
    assert legacy.scanner_trust_file is not None

    enabled_path = _artifact_enabled_v2_request(enabled_root)
    enabled = load_server_setup_request(enabled_path)
    assert enabled.schema_version == "agentnet.server-setup.request.v2"
    assert enabled.artifact_mode == "enabled"
    assert enabled.effective_artifact_mode == "enabled"
    assert enabled.scanner_trust_file is not None

    communication_path = _communication_only_request(communication_root)
    communication = load_server_setup_request(communication_path)
    assert communication.schema_version == "agentnet.server-setup.request.v2"
    assert communication.artifact_mode == "disabled"
    assert communication.effective_artifact_mode == "disabled"
    assert communication.scanner_trust_file is None

    communication_value = json.loads(communication_path.read_text(encoding="utf-8"))
    invalid = dict(communication_value)
    invalid["scanner_trust_file"] = str(communication_root / "scanner-trust.json")
    _private_json(communication_path, invalid)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(communication_path)

    invalid = dict(communication_value)
    invalid["scanner_trust_file"] = None
    _private_json(communication_path, invalid)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(communication_path)

    invalid = json.loads(legacy_path.read_text(encoding="utf-8"))
    invalid["artifact_mode"] = None
    _private_json(legacy_path, invalid)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(legacy_path)

    invalid["artifact_mode"] = "disabled"
    _private_json(legacy_path, invalid)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(legacy_path)

    enabled_value = json.loads(enabled_path.read_text(encoding="utf-8"))
    enabled_value.pop("scanner_trust_file")
    _private_json(enabled_path, enabled_value)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(enabled_path)

    invalid.pop("artifact_mode")
    invalid.pop("schema")
    _private_json(legacy_path, invalid)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(legacy_path)


def test_core_create_arguments_omit_scanner_only_for_communication_v2(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    legacy_root = tmp_path / "legacy"
    communication_root = tmp_path / "communication"
    legacy_root.mkdir()
    communication_root.mkdir()
    legacy = load_server_setup_request(_request(legacy_root))
    communication = load_server_setup_request(_communication_only_request(communication_root))
    scanner = setup.ScannerTrustConfig.model_validate_json(
        legacy.scanner_trust_file.read_text(encoding="utf-8")
    )
    common = {
        "node_executable": Path("/usr/bin/node"),
        "executable": Path("/opt/agentnet/bin/agentnet"),
        "core_config_path": Path("/var/lib/agentnet/agentnet.json"),
        "core_data": Path("/var/lib/agentnet"),
        "oidc_path": Path("/var/lib/agentnet/oidc.json"),
        "scanner_path": Path("/var/lib/agentnet/scanner-trust.json"),
    }
    legacy_arguments = setup._core_create_arguments(
        legacy,
        scanner_trust=scanner,
        **common,
    )
    assert legacy_arguments[legacy_arguments.index("--artifact-mode") + 1] == "enabled"
    assert "--scanner-trust-config" in legacy_arguments

    communication_arguments = setup._core_create_arguments(
        communication,
        scanner_trust=None,
        **common,
    )
    assert communication_arguments[
        communication_arguments.index("--artifact-mode") + 1
    ] == "disabled"
    assert "--scanner-trust-config" not in communication_arguments


def test_request_is_strict_non_secret_and_callback_bound(tmp_path: Path) -> None:
    request_path = _request(tmp_path)
    request = load_server_setup_request(request_path)
    assert request.domain_id == "corp.example"
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["agentnet_executable"] = "/tmp/request-selected-code"
    request_path.write_text(json.dumps(value), encoding="utf-8")
    request_path.chmod(0o600)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(request_path)


def test_unit_overrides_are_rejected(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    layout = SetupLayout(tmp_path / "host")
    override = layout.host(Path(f"/run/systemd/system.control/{CORE_UNIT}.d"))
    override.mkdir(parents=True)
    with pytest.raises(ServerSetupError, match="unsupported overrides"):
        setup._require_no_unit_overrides(layout, CORE_UNIT)


def test_rendered_units_are_fixed_loopback_hardened_and_secret_free(tmp_path: Path) -> None:
    request = load_server_setup_request(_request(tmp_path))
    units = render_units(
        Path("/usr/bin/node"),
        Path("/usr/local/bin/agentnet"),
        Path("/usr/local/bin/uv"),
    )
    assert set(units) == set(MANAGED_UNITS)
    rendered = b"\n".join(units.values()).decode("utf-8")
    assert '"/usr/bin/node" "/usr/local/bin/agentnet"' in rendered
    assert "--host 127.0.0.1 --port 8080" in rendered
    assert "--host 127.0.0.1 --port 8090" in rendered
    assert "NoNewPrivileges=true" in rendered
    assert "ProtectSystem=strict" in rendered
    assert "EnvironmentFile=/etc/agentnet-secrets/core.env" in rendered
    assert "EnvironmentFile=/etc/agentnet-secrets/approval.env" in rendered
    assert 'Environment="AGENTNET_UV=/usr/local/bin/uv"' in rendered
    assert "Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" in rendered
    import agentnet.operations.server_setup as setup

    assert (
        f"Environment=AGENTNET_NPM_RUNTIME_DIR=/var/lib/agentnet/npm-runtimes/{setup.__version__}"
        in rendered
    )
    assert (
        f"Environment=AGENTNET_NPM_RUNTIME_DIR=/var/lib/agentnet-approval/npm-runtimes/{setup.__version__}"
        in rendered
    )
    assert (
        f"Environment=AGENTNET_NPM_RUNTIME_DIR=/var/lib/agentnet-c0/npm-runtimes/{setup.__version__}"
        in rendered
    )
    assert "SupplementaryGroups=" in rendered
    assert "UnsetEnvironment=NODE_OPTIONS NODE_PATH PYTHONPATH" in rendered
    for unit in (APPROVAL_UNIT, CORE_UNIT, C0_RESPONDER_UNIT, CREDENTIAL_RENEW_UNIT):
        service = units[unit].decode("utf-8")
        assert "SSL_CERT_FILE SSL_CERT_DIR SSLKEYLOGFILE" in service
    assert "UnsetEnvironment=" not in units[CREDENTIAL_RENEW_TIMER].decode("utf-8")
    assert "synthetic-test-secret" not in rendered
    assert "AGENTNET_APPROVAL_CORE_TOKEN=" not in rendered
    for unit in (APPROVAL_UNIT, CORE_UNIT, C0_RESPONDER_UNIT):
        assert "Type=exec" in units[unit].decode()
    assert "Type=simple" not in rendered
    assert "Type=oneshot" in units[CREDENTIAL_RENEW_UNIT].decode()
    assert "SuccessExitStatus=143 SIGTERM" in units[APPROVAL_UNIT].decode()
    assert "ConditionPathExists=/var/lib/agentnet-c0/config.json" in units[C0_RESPONDER_UNIT].decode()
    assert "LoadCredential=signing-key.pem:/var/lib/agentnet/guided-join.key.pem" in units[C0_RESPONDER_UNIT].decode()
    assert "c0-pilot responder --run" in units[C0_RESPONDER_UNIT].decode()
    assert 'credential renew --identity "/var/lib/agentnet/server-agent-identity.json"' in units[CREDENTIAL_RENEW_UNIT].decode()
    assert f"Unit={CREDENTIAL_RENEW_UNIT}" in units[CREDENTIAL_RENEW_TIMER].decode()
    timer = units[CREDENTIAL_RENEW_TIMER].decode()
    assert "OnActiveSec=5min" in timer
    assert "OnUnitInactiveSec=1h" in timer
    assert "OnUnitActiveSec=" not in timer
    assert "OnBootSec=" not in timer
    assert "Persistent=" not in timer


@pytest.mark.parametrize("variable", ["SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"])
def test_plan_rejects_tls_environment_before_runtime_resolution(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    variable: str,
) -> None:
    request = load_server_setup_request(_request(tmp_path))
    import agentnet.operations.server_setup as setup

    monkeypatch.setenv(variable, "/private/unsupported-tls-state")

    def unexpected_runtime() -> setup.SetupRuntime:
        raise AssertionError("runtime resolution must not precede TLS environment rejection")

    monkeypatch.setattr(setup, "_resolve_setup_runtime", unexpected_runtime)
    with pytest.raises(ServerSetupError, match="TLS environment is unsupported") as exc_info:
        plan_server_setup(request)
    assert exc_info.value.blocker == "approval_broker_auth"
    rendered = "".join(traceback.format_exception(exc_info.value))
    assert "unsupported-tls-state" not in rendered
    assert exc_info.value.__cause__ is None


def test_plan_is_read_only_and_emits_redacted_fixed_steps(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request = load_server_setup_request(_request(tmp_path))
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_account_fact", lambda name, home: "create")
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    report = plan_server_setup(request)
    assert report["schema"] == "agentnet.server-setup.evidence.v1"
    assert report["status"] == "planned"
    assert report["managed_units"] == sorted(MANAGED_UNITS)
    assert report["https_topology"] == "external_self_hosted_reverse_proxy_to_loopback"
    assert report["package_version"]
    assert report["public_core_origin"] == "https://core.corp.example"
    assert report["laptop_join_command"] == (
        "npm install --global @misunders2d/agentnet@"
        + report["package_version"]
        + " --ignore-scripts --no-audit --no-fund && "
        "agentnet join guided --server https://core.corp.example"
    )
    assert report["prerequisites"]["database_reference"] == (
        "validated_fixed_local_peer_contract_service_canary_pending_apply"
    )
    assert report["prerequisites"]["postgresql"] == {
        "auth_method": "peer",
        "database": "agentnet",
        "hba_rule": "local agentnet agentnet peer",
        "hba_rule_order": "before_any_potentially_matching_local_rule",
        "ident_map": "none_exact_name_match",
        "operator_action": "install exact scoped HBA rule, reload PostgreSQL, then rerun same approved digest",
        "os_user": "agentnet",
        "role": "agentnet",
        "socket": "/var/run/postgresql",
    }
    assert report["authority_granted"] is False
    assert report["identity_enrolled"] is False
    assert "synthetic-test-secret" not in json.dumps(report)
    assert str(tmp_path) not in json.dumps(report)
    assert not any(tmp_path.glob("*.service"))


def test_request_digest_binds_absolute_references_and_input_content(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    first_root = tmp_path / "first"
    second_root = tmp_path / "second"
    first_root.mkdir()
    second_root.mkdir()
    first_path = _request(first_root)
    second_path = _request(second_root)
    for name in (
        "core.env",
        "approval.env",
        "core-oidc.json",
        "owner-oidc.json",
        "approvers.json",
        "scanner-trust.json",
    ):
        (second_root / name).write_bytes((first_root / name).read_bytes())
        (second_root / name).chmod(0o600)
    first = load_server_setup_request(first_path)
    second = load_server_setup_request(second_path)
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    first_digest = plan_server_setup(first)["request_digest"]
    second_digest = plan_server_setup(second)["request_digest"]
    assert first_digest != second_digest

    core_environment = first.core_environment_file.read_text(encoding="utf-8")
    first.core_environment_file.write_text(
        core_environment.replace("synthetic-test-secret", "rotated-high-entropy-test-secret"),
        encoding="utf-8",
    )
    first.core_environment_file.chmod(0o600)
    assert plan_server_setup(first)["request_digest"] == first_digest

    first_value = json.loads(first.oidc_provider_file.read_text(encoding="utf-8"))
    first_value["client_id"] = "different-approved-client"
    _private_json(first.oidc_provider_file, first_value)
    changed = load_server_setup_request(first_root / "setup.json")
    assert plan_server_setup(changed)["request_digest"] != first_digest


def test_request_digest_binds_runtime_content_and_locked_apply_rechecks_it(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    request = load_server_setup_request(_request(tmp_path))
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    tree_calls = 0

    def changing_tree_hash(path: Path) -> str:
        nonlocal tree_calls
        tree_calls += 1
        generation = "approved" if tree_calls <= 2 else "changed"
        return hashlib.sha256(f"{path}:{generation}".encode()).hexdigest()

    monkeypatch.setattr(setup, "_sha256_stable_tree", changing_tree_hash)
    approved = str(plan_server_setup(request, layout=layout)["request_digest"])
    monkeypatch.setattr(
        setup,
        "_ensure_account",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(AssertionError("account mutation reached")),
    )
    with pytest.raises(ServerSetupError, match="runtime changed after approved preflight") as exc_info:
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "request_changed"
    assert not layout.host(setup.CORE_ENV).exists()
    assert not layout.host(setup.APPROVAL_ENV).exists()


def test_request_digest_binds_privileged_host_tool_content(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    request = load_server_setup_request(_request(tmp_path))
    runtime_root = tmp_path / "runtime"
    executable = runtime_root / "npm/bin/agentnet.mjs"
    executable.parent.mkdir(parents=True)
    tools = {
        "node": tmp_path / "node",
        "uv": tmp_path / "uv",
        "systemctl": tmp_path / "systemctl",
        "useradd": tmp_path / "useradd",
        "agentnet": executable,
    }
    for name, path in tools.items():
        path.write_bytes(f"{name}-v1".encode("ascii"))
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: tools["node"])
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: tools["uv"])
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: tools["agentnet"])
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: tools[name])
    monkeypatch.setattr(
        setup,
        "_sha256_stable_file",
        lambda path, **_kwargs: hashlib.sha256(path.read_bytes()).hexdigest(),
    )

    approved = plan_server_setup(request)["request_digest"]
    tools["systemctl"].write_bytes(b"systemctl-v2")
    assert plan_server_setup(request)["request_digest"] != approved


def test_request_rejects_same_core_and_approval_origin(tmp_path: Path) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["approval_public_origin"] = value["core_public_origin"]
    _private_json(request_path, value)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(request_path)


@pytest.mark.parametrize(
    "origin",
    ["https://localhost", "https://core.local", "https://127.0.0.1", "https://[::1]"],
)
def test_request_rejects_local_public_origin(tmp_path: Path, origin: str) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["core_public_origin"] = origin
    _private_json(request_path, value)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(request_path)


def test_plan_rejects_noncanonical_core_oidc_before_mutation(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    oidc_path = Path(value["oidc_provider_file"])
    oidc = json.loads(oidc_path.read_text(encoding="utf-8"))
    oidc["issuer"] = "http://accounts.example"
    _private_json(oidc_path, oidc)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="OIDC setup input is invalid"):
        plan_server_setup(load_server_setup_request(request_path))


def test_plan_rejects_interpreter_control_as_oidc_secret_reference(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    oidc_path = Path(value["oidc_provider_file"])
    oidc = json.loads(oidc_path.read_text(encoding="utf-8"))
    oidc["client_secret_env"] = "NODE_OPTIONS"
    _private_json(oidc_path, oidc)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="secret environment reference is unsafe"):
        plan_server_setup(load_server_setup_request(request_path))


@pytest.mark.parametrize("collision", ["core_broker", "owner_database", "shared_oidc"])
def test_plan_rejects_credential_reference_reuse(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    collision: str,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    oidc_path = Path(value["oidc_provider_file"])
    owner_path = Path(value["approval_owner_oidc_file"])
    oidc = json.loads(oidc_path.read_text(encoding="utf-8"))
    owner = json.loads(owner_path.read_text(encoding="utf-8"))
    if collision == "core_broker":
        oidc["client_secret_env"] = "AGENTNET_APPROVAL_CORE_TOKEN"
    elif collision == "owner_database":
        owner["client_secret_env"] = value["database_url_env"]
    else:
        oidc["client_secret_env"] = "AGENTNET_SHARED_OIDC_SECRET"
        owner["client_secret_env"] = "AGENTNET_SHARED_OIDC_SECRET"
    _private_json(oidc_path, oidc)
    _private_json(owner_path, owner)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="require distinct environment references"):
        plan_server_setup(load_server_setup_request(request_path))


def test_exact_approval_policy_rejects_extra_approver(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    owner_oidc = ApprovalOwnerOIDCConfig.model_validate(
        json.loads((_request(tmp_path).parent / "owner-oidc.json").read_text(encoding="utf-8"))
    )
    state = tmp_path / "approval-state"
    requested = SetupApprover(
        principal_id="owner-principal",
        authority_kind="human",
        domain_id="corp.example",
        allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
        oidc_issuer="https://accounts.example",
        oidc_subject="owner-subject",
    )

    def configured(principal: str, key_name: str) -> ApprovalServiceApproverConfig:
        return ApprovalServiceApproverConfig(
            principal_id=principal,
            authority_kind="human",
            domain_id="corp.example",
            signer_key_id="k" * 64,
            signer_private_key_path=state / "secrets" / key_name,
            allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
            oidc_issuer="https://accounts.example",
            oidc_subject="owner-subject" if principal == "owner-principal" else "other-subject",
        )

    config = ApprovalServiceConfig(
        public_origin="https://approval.corp.example",
        rp_id="approval.corp.example",
        verifier_id="approval.corp.example",
        data_dir=state,
        database_path=state / "approval.sqlite3",
        record_key_path=state / "secrets" / "records.key",
        internal_core_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        owner_oidc=owner_oidc,
        approvers=(configured("owner-principal", "owner.pem"),),
    )
    request = load_server_setup_request(tmp_path / "setup.json")
    setup._require_exact_approval_policy(
        config,
        request=request,
        owner_oidc=owner_oidc,
        approvers=(requested,),
        approval_state=state,
    )
    drifted = config.model_copy(
        update={"approvers": config.approvers + (configured("extra-principal", "extra.pem"),)}
    )
    with pytest.raises(ServerSetupError, match="existing Approval state conflicts"):
        setup._require_exact_approval_policy(
            drifted,
            request=request,
            owner_oidc=owner_oidc,
            approvers=(requested,),
            approval_state=state,
        )


def test_managed_identity_profile_accepts_only_canonical_strict_actor(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    domain_id = "corp.example"
    harness_id = "hub-harness"
    credential_id = "hub-credential"
    key = P256KeyPair.generate()
    key_path = tmp_path / "guided-join.key.pem"
    key_path.write_bytes(key.private_pem)
    key_path.chmod(0o600)
    identity_path = tmp_path / "server-agent-identity.json"
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=domain_id,
        principal_id="owner-principal",
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="hardware_bound",
    ).model_dump(mode="json")
    profile = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": "https://core.corp.example",
        "audience": "agentnet-core",
        "actor": actor,
        "private_key_path": str(key_path),
    }
    account = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
    config = SimpleNamespace(
        enrolled_harness_id=harness_id,
        enrolled_credential_id=credential_id,
    )
    request = SimpleNamespace(
        domain_id=domain_id,
        core_public_origin="https://core.corp.example",
        service_audience="agentnet-core",
    )

    _private_json(identity_path, profile)
    assert setup._validated_managed_identity_profile(
        identity_path,
        key_path,
        account,
        config=config,
        request=request,
    ) == profile

    duplicate_top_level = json.dumps(profile)[:-1] + ', "schema": "agentnet.identity-profile.v1"}'
    identity_path.write_text(duplicate_top_level, encoding="utf-8")
    identity_path.chmod(0o600)
    with pytest.raises(ServerSetupError, match="identity is invalid"):
        setup._validated_managed_identity_profile(
            identity_path,
            key_path,
            account,
            config=config,
            request=request,
        )

    duplicate_actor = json.dumps(profile).replace(
        '"harness_id": "hub-harness"',
        '"harness_id": "hub-harness", "harness_id": "hub-harness"',
        1,
    )
    identity_path.write_text(duplicate_actor, encoding="utf-8")
    identity_path.chmod(0o600)
    with pytest.raises(ServerSetupError, match="identity is invalid"):
        setup._validated_managed_identity_profile(
            identity_path,
            key_path,
            account,
            config=config,
            request=request,
        )

    nonfinite_top_level = json.dumps(profile)[:-1] + ', "unexpected": NaN}'
    nonfinite_actor = json.dumps(profile).replace('"credential_epoch": 1', '"credential_epoch": NaN', 1)
    with monkeypatch.context() as strict_parser:
        strict_parser.setattr(
            setup.VerifiedActor,
            "model_validate",
            lambda *_args, **_kwargs: pytest.fail("non-finite JSON reached VerifiedActor validation"),
        )
        for payload in (nonfinite_top_level, nonfinite_actor):
            identity_path.write_text(payload, encoding="utf-8")
            identity_path.chmod(0o600)
            with pytest.raises(ServerSetupError, match="identity is invalid"):
                setup._validated_managed_identity_profile(
                    identity_path,
                    key_path,
                    account,
                    config=config,
                    request=request,
                )

    profile["actor"] = {**actor, "key_id": key.thumbprint}
    _private_json(identity_path, profile)
    with pytest.raises(ServerSetupError, match="identity is invalid"):
        setup._validated_managed_identity_profile(
            identity_path,
            key_path,
            account,
            config=config,
            request=request,
        )

    profile["actor"] = {**actor, "harness_id": "other-harness"}
    _private_json(identity_path, profile)
    with pytest.raises(ServerSetupError, match="mismatches current binding"):
        setup._validated_managed_identity_profile(
            identity_path,
            key_path,
            account,
            config=config,
            request=request,
        )

    profile["actor"] = actor
    _private_json(identity_path, profile)
    key_path.write_bytes(b"not-a-private-key")
    key_path.chmod(0o600)
    with pytest.raises(ServerSetupError, match="key is invalid"):
        setup._validated_managed_identity_profile(
            identity_path,
            key_path,
            account,
            config=config,
            request=request,
        )


def test_health_requires_exact_agentnet_json_identity(monkeypatch: pytest.MonkeyPatch) -> None:
    import agentnet.operations.server_setup as setup

    class Response:
        status = 200

        def __init__(self, payload: object) -> None:
            self.payload = json.dumps(payload).encode()

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self, _limit: int) -> bytes:
            return self.payload

    class Opener:
        def __init__(self, payload: object) -> None:
            self.payload = payload

        def open(self, request: object, *, timeout: int):
            assert isinstance(request, setup.urllib.request.Request)
            assert request.get_method() == "GET"
            headers = {name.lower(): value for name, value in request.header_items()}
            assert headers["user-agent"] == f"AgentNet/{setup.__version__}"
            assert headers["accept"] == "application/json"
            assert timeout == 2
            return Response(self.payload)

    monkeypatch.setattr(setup.time, "sleep", lambda _seconds: None)
    monkeypatch.setattr(setup.urllib.request, "build_opener", lambda *_args: Opener({"status": "ok"}))
    with pytest.raises(ServerSetupError, match="exact healthy identity evidence"):
        setup._health("https://core.corp.example/healthz", expected={"service": "agentnet-core"}, attempts=1)

    monkeypatch.setattr(
        setup.urllib.request,
        "build_opener",
        lambda *_args: Opener({"service": "agentnet-core", "status": "alive"}),
    )
    setup._health(
        "https://core.corp.example/healthz",
        expected={"service": "agentnet-core", "status": "alive"},
        attempts=1,
    )

    monkeypatch.setattr(
        setup.urllib.request,
        "build_opener",
        lambda *_args: Opener(
            {
                "service": "agentnet-core",
                "ready": True,
                "deployment_binding": {
                    "ready": True,
                    "required": True,
                    "credential_state": "renewal_needed",
                },
            }
        ),
    )
    setup._health(
        "https://core.corp.example/readyz",
        expected={
            "service": "agentnet-core",
            "ready": True,
            "deployment_binding": {
                "ready": True,
                "required": True,
                "credential_state": ("current", "renewal_needed"),
            },
        },
        attempts=1,
    )

    transient_attempts = 0
    sleep_calls: list[int] = []

    class TransientOpener:
        def open(self, _url: str, *, timeout: int):
            nonlocal transient_attempts
            assert timeout == 2
            transient_attempts += 1
            if transient_attempts < 50:
                raise setup.urllib.error.URLError("public route still converging")
            return Response({"service": "agentnet-core", "status": "alive"})

    monkeypatch.setattr(setup.time, "sleep", sleep_calls.append)
    monkeypatch.setattr(setup.urllib.request, "build_opener", lambda *_args: TransientOpener())
    setup._health(
        "https://core.corp.example/healthz",
        expected={"service": "agentnet-core", "status": "alive"},
        attempts=setup._START_HEALTH_ATTEMPTS,
    )
    assert transient_attempts == 50
    assert sleep_calls == [1] * 49


def test_plan_rejects_database_reference_drift(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    _replace_environment_value(
        core_env,
        "AGENTNET_DATABASE_URL",
        "postgresql://other@%2Fvar%2Frun%2Fpostgresql/agentnet",
    )
    core_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="database reference does not match"):
        plan_server_setup(load_server_setup_request(request_path))


@pytest.mark.parametrize(
    "database_url",
    [
        "postgresql://agentnet@127.0.0.1/agentnet",
        "postgresql://agentnet@%2Ftmp%2Fpostgresql/agentnet",
        "postgresql://other@%2Fvar%2Frun%2Fpostgresql/agentnet",
        "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/other",
    ],
)
def test_request_rejects_noncanonical_postgres_peer_contract(
    tmp_path: Path,
    database_url: str,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    value["database_url"] = database_url
    _private_json(request_path, value)
    with pytest.raises(ServerSetupError, match="setup request is invalid"):
        load_server_setup_request(request_path)


def test_postgres_auth_rules_require_exact_unshadowed_peer_rule() -> None:
    from agentnet.errors import ValidationError
    from agentnet.storage.postgres import validate_ordinary_server_postgres_auth_rules

    exact = {
        "rule_number": 2,
        "type": "local",
        "database": ["agentnet"],
        "user_name": ["agentnet"],
        "auth_method": "peer",
        "options": None,
        "error": None,
    }
    postgres_only = {
        "rule_number": 1,
        "type": "local",
        "database": ["all"],
        "user_name": ["postgres"],
        "auth_method": "peer",
        "options": None,
        "error": None,
    }
    result = validate_ordinary_server_postgres_auth_rules(
        [postgres_only, exact],
        [],
    )
    assert result["auth_method"] == "peer"
    assert result["ident_map"] == "none_exact_name_match"

    broad_shadow = {**exact, "rule_number": 1, "database": ["all"], "user_name": ["all"]}
    with pytest.raises(ValidationError, match="first matching local rule"):
        validate_ordinary_server_postgres_auth_rules([broad_shadow, exact], [])
    for database, user_name in (
        (["samegroup"], ["all"]),
        (["/agent.*/"], ["all"]),
        (["@database-list"], ["all"]),
        (["all"], ["+agentnet_role"]),
        (["all"], ["/agent.*/"]),
        (["all"], ["@user-list"]),
    ):
        with pytest.raises(ValidationError, match="first matching local rule"):
            validate_ordinary_server_postgres_auth_rules(
                [{**exact, "rule_number": 1, "database": database, "user_name": user_name}, exact],
                [],
            )
    with pytest.raises(ValidationError, match="parse errors"):
        validate_ordinary_server_postgres_auth_rules(
            [{**exact, "error": "synthetic parse error"}],
            [],
        )
    with pytest.raises(ValidationError, match="first matching local rule"):
        validate_ordinary_server_postgres_auth_rules(
            [{**exact, "options": ["map=agentnet"]}],
            [],
        )


def test_postgres_connection_probe_uses_fixed_dsn_and_query_shape() -> None:
    from agentnet.storage.postgres import (
        ORDINARY_SERVER_POSTGRES_DSN,
        probe_ordinary_server_postgres_connection,
    )

    calls: list[tuple[str, dict[str, object]]] = []
    statements: list[str] = []

    class Result:
        def fetchone(self) -> dict[str, object]:
            return {
                "current_user": "agentnet",
                "current_database": "agentnet",
                "unix_socket": True,
                "in_recovery": False,
                "server_version": "18.4",
            }

    class Connection:
        def __enter__(self) -> "Connection":
            return self

        def __exit__(self, *_args: object) -> None:
            return None

        def execute(self, query: str) -> Result:
            statements.append(" ".join(query.split()))
            return Result()

    def connector(dsn: str, **kwargs: object) -> Connection:
        calls.append((dsn, kwargs))
        return Connection()

    result = probe_ordinary_server_postgres_connection(
        ORDINARY_SERVER_POSTGRES_DSN,
        connector=connector,
    )
    assert result == {
        "ready": True,
        "current_user": "agentnet",
        "current_database": "agentnet",
        "transport": "unix_socket",
        "writable_primary": True,
        "server_version": "18.4",
    }
    assert calls[0][0] == ORDINARY_SERVER_POSTGRES_DSN
    assert calls[0][1]["autocommit"] is True
    assert calls[0][1]["connect_timeout"] == 3
    assert statements[0] == "SET statement_timeout = '3s'"
    assert "inet_server_addr() IS NULL AS unix_socket" in statements[1]
    assert "pg_is_in_recovery() AS in_recovery" in statements[1]


def test_postgres_auth_probe_uses_live_parsed_views() -> None:
    from agentnet.storage.postgres import (
        ORDINARY_SERVER_POSTGRES_ADMIN_DSN,
        inspect_ordinary_server_postgres_auth,
    )

    calls: list[tuple[str, dict[str, object]]] = []
    statements: list[str] = []

    class Result:
        def __init__(
            self,
            rows: list[dict[str, object]],
            row: dict[str, object] | None = None,
        ) -> None:
            self._rows = rows
            self._row = row

        def fetchall(self) -> list[dict[str, object]]:
            return self._rows

        def fetchone(self) -> dict[str, object] | None:
            return self._row

    class Connection:
        def __enter__(self) -> "Connection":
            return self

        def __exit__(self, *_args: object) -> None:
            return None

        def execute(self, query: str) -> Result:
            normalized = " ".join(query.split())
            statements.append(normalized)
            if "FROM pg_hba_file_rules" in normalized:
                return Result(
                    [
                        {
                            "rule_number": 1,
                            "type": "local",
                            "database": ["agentnet"],
                            "user_name": ["agentnet"],
                            "auth_method": "peer",
                            "options": None,
                            "error": None,
                        }
                    ]
                )
            if "FROM pg_ident_file_mappings" in normalized:
                assert "map_number,map_name,sys_name,pg_username,error" in normalized
                return Result([])
            return Result([], {"hba_loaded": True, "ident_loaded": True})

    def connector(dsn: str, **kwargs: object) -> Connection:
        calls.append((dsn, kwargs))
        return Connection()

    result = inspect_ordinary_server_postgres_auth(connector=connector)
    assert result == {
        "ready": True,
        "configuration_loaded": True,
        "auth_method": "peer",
        "database": "agentnet",
        "ident_map": "none_exact_name_match",
        "rule_number": 1,
        "user": "agentnet",
    }
    assert calls[0][0] == ORDINARY_SERVER_POSTGRES_ADMIN_DSN
    assert calls[0][1]["autocommit"] is True
    assert any("FROM pg_hba_file_rules" in statement for statement in statements)
    assert any("FROM pg_ident_file_mappings" in statement for statement in statements)
    assert any("pg_conf_load_time()" in statement for statement in statements)

    class StaleConnection(Connection):
        def execute(self, query: str) -> Result:
            normalized = " ".join(query.split())
            if "pg_conf_load_time()" in normalized:
                return Result([], {"hba_loaded": False, "ident_loaded": True})
            return super().execute(query)

    stale = inspect_ordinary_server_postgres_auth(
        connector=lambda *_args, **_kwargs: StaleConnection()
    )
    assert stale == {"ready": False, "reason": "ValidationError"}


def test_apply_blocks_after_identity_before_agentnet_writes_when_postgres_not_ready(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    request = load_server_setup_request(_request(tmp_path))
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    account = SimpleNamespace(
        pw_name=setup.CORE_USER,
        pw_uid=os.geteuid(),
        pw_gid=os.getegid(),
    )
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    monkeypatch.setattr(setup, "_ensure_account", lambda *_args, **_kwargs: account)
    approved = str(plan_server_setup(request, layout=layout)["request_digest"])
    monkeypatch.setattr(
        setup,
        "_postgres_peer_gate",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError(
                "postgres_auth_not_ready",
                "PostgreSQL peer rule is not ready; reload and retry",
            )
        ),
    )
    monkeypatch.setattr(
        setup,
        "_run_as",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(AssertionError("product mutation reached")),
    )
    with pytest.raises(ServerSetupError) as exc_info:
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "postgres_auth_not_ready"
    assert not layout.host(setup.CORE_ENV).exists()
    assert not layout.host(setup.APPROVAL_ENV).exists()
    assert not layout.host(setup.CORE_CONFIG).exists()
    assert not layout.host(setup.APPROVAL_CONFIG).exists()


@pytest.mark.skipif(not hasattr(os, "fork"), reason="POSIX fork is unavailable")
def test_postgres_probe_runs_under_bounded_child_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_drop_identity", lambda _account: lambda: None)
    monkeypatch.setenv("PGSERVICE", "untrusted-service")
    monkeypatch.setenv("PGPASSFILE", "/private/credential-path")
    monkeypatch.setenv("UNRELATED_SECRET", "private-value")
    account = SimpleNamespace(
        pw_uid=os.geteuid(),
        pw_gid=os.getegid(),
        pw_dir="/var/lib/agentnet",
    )
    evidence = setup._run_postgres_probe_as(
        account,
        lambda: {
            "ready": True,
            "transport": "unix_socket",
            "pg_environment": sorted(name for name in os.environ if name.startswith("PG")),
            "unrelated_secret_present": "UNRELATED_SECRET" in os.environ,
            "home": os.environ.get("HOME"),
        },
        stage="postgres_test",
    )
    assert evidence == {
        "ready": True,
        "transport": "unix_socket",
        "pg_environment": [],
        "unrelated_secret_present": False,
        "home": "/var/lib/agentnet",
    }
    with pytest.raises(ServerSetupError, match=r"postgres_test failed \(SyntheticFailure\)") as exc_info:
        setup._run_postgres_probe_as(
            account,
            lambda: {"ready": False, "reason": "SyntheticFailure"},
            stage="postgres_test",
        )
    assert exc_info.value.blocker == "postgres_auth_not_ready"


@pytest.mark.parametrize(
    ("target_name", "entry_kind", "expected_blocker"),
    [
        ("approval_config", "dangling_symlink", "approval_config"),
        ("approval_state", "directory_symlink", "approval_custody"),
        ("core_config", "dangling_symlink", "core_custody"),
        ("core_config", "fifo", "core_custody"),
        ("core_runtime", "directory_symlink", "core_custody"),
    ],
)
def test_apply_rejects_managed_child_conflicts_before_product_writes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    target_name: str,
    entry_kind: str,
    expected_blocker: str,
) -> None:
    import agentnet.operations.server_setup as setup

    request = load_server_setup_request(_request(tmp_path))
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    account = SimpleNamespace(
        pw_name=setup.CORE_USER,
        pw_uid=os.geteuid(),
        pw_gid=os.getegid(),
    )
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    monkeypatch.setattr(setup, "_ensure_account", lambda *_args, **_kwargs: account)
    monkeypatch.setattr(
        setup,
        "_postgres_peer_gate",
        lambda *_args, **_kwargs: {"status": "validated_exact_local_peer"},
    )
    product_calls: list[list[str]] = []
    monkeypatch.setattr(
        setup,
        "_run_as",
        lambda _account, argv, **_kwargs: product_calls.append(argv),
    )
    approved = str(plan_server_setup(request, layout=layout)["request_digest"])

    for private_root in (layout.host(setup.CORE_DATA), layout.host(setup.APPROVAL_DATA)):
        private_root.mkdir(parents=True, mode=0o700)
        private_root.chmod(0o700)
    targets = {
        "approval_config": layout.host(setup.APPROVAL_CONFIG),
        "approval_state": layout.host(setup.APPROVAL_STATE),
        "core_config": layout.host(setup.CORE_CONFIG),
        "core_runtime": layout.host(setup.CORE_DATA) / "core",
    }
    target = targets[target_name]
    if entry_kind == "dangling_symlink":
        target.symlink_to(tmp_path / "missing-target")
    elif entry_kind == "directory_symlink":
        external = tmp_path / f"external-{target_name}"
        external.mkdir(mode=0o700)
        target.symlink_to(external, target_is_directory=True)
    elif entry_kind == "fifo":
        os.mkfifo(target, mode=0o600)
    else:  # pragma: no cover - parameter invariant
        raise AssertionError(entry_kind)

    with pytest.raises(ServerSetupError) as exc_info:
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == expected_blocker
    assert product_calls == []
    assert not layout.host(setup.CORE_ENV).exists()
    assert not layout.host(setup.APPROVAL_ENV).exists()


def test_postgres_peer_gate_requires_exact_service_and_rule_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    core_account = SimpleNamespace(pw_name="agentnet", pw_uid=1234, pw_gid=1234)
    postgres_account = SimpleNamespace(pw_name="postgres", pw_uid=1235, pw_gid=1235)
    monkeypatch.setattr(setup.pwd, "getpwnam", lambda name: postgres_account if name == "postgres" else None)
    stages: list[str] = []

    def exact_probe(_account: object, _probe: object, *, stage: str) -> dict[str, object]:
        stages.append(stage)
        if stage == "postgres_service_identity_canary":
            return {
                "ready": True,
                "current_user": "agentnet",
                "current_database": "agentnet",
                "transport": "unix_socket",
                "writable_primary": True,
            }
        return {
            "ready": True,
            "auth_method": "peer",
            "ident_map": "none_exact_name_match",
        }

    monkeypatch.setattr(setup, "_run_postgres_probe_as", exact_probe)
    evidence = setup._postgres_peer_gate(core_account, ORDINARY_SERVER_POSTGRES_DSN)
    assert evidence["status"] == "validated_exact_local_peer"
    assert stages == ["postgres_service_identity_canary", "postgres_auth_rule_inspection"]

    def wrong_auth(_account: object, _probe: object, *, stage: str) -> dict[str, object]:
        result = exact_probe(_account, _probe, stage=stage)
        if stage == "postgres_auth_rule_inspection":
            result["auth_method"] = "trust"
        return result

    monkeypatch.setattr(setup, "_run_postgres_probe_as", wrong_auth)
    with pytest.raises(ServerSetupError, match="does not match the fixed profile"):
        setup._postgres_peer_gate(core_account, ORDINARY_SERVER_POSTGRES_DSN)


def test_plan_rejects_shell_syntax_in_runtime_environment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    core_env.write_text(
        f"AGENTNET_DATABASE_URL={ORDINARY_SERVER_POSTGRES_DSN}\n"
        "AGENTNET_CORE_OIDC_CLIENT_SECRET='quoted value'\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token-0123456789abcdef0123456789\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="Core environment input line 2 is invalid"):
        plan_server_setup(load_server_setup_request(request_path))


def test_plan_rejects_setup_owned_environment_override(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    core_env.write_text(core_env.read_text(encoding="utf-8") + "PATH=/tmp/untrusted\n", encoding="utf-8")
    core_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="setup-owned variable"):
        plan_server_setup(load_server_setup_request(request_path))


def test_plan_rejects_unexpected_interpreter_environment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    core_env.write_text(
        core_env.read_text(encoding="utf-8") + "NODE_OPTIONS=--require=/tmp/untrusted.js\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="do not match fixed request references"):
        plan_server_setup(load_server_setup_request(request_path))


def test_plan_rejects_mismatched_broker_credentials(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    approval_env = Path(value["approval_environment_file"])
    approval_env.write_text(
        "AGENTNET_APPROVAL_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=different-synthetic-broker-token-0123456789abcdef012345\n",
        encoding="utf-8",
    )
    approval_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="Core and Approval broker credentials do not match"):
        plan_server_setup(load_server_setup_request(request_path))


@pytest.mark.parametrize(
    "broker_value",
    ["too-short", "é" * 43, "x" * 513],
)
def test_plan_rejects_invalid_broker_credential_before_managed_writes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    broker_value: str,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    for field in (
        "core_environment_file",
        "approval_environment_file",
    ):
        environment = Path(value[field])
        _replace_environment_value(
            environment,
            "AGENTNET_APPROVAL_CORE_TOKEN",
            broker_value,
        )
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    managed_root = tmp_path / "managed-host"
    with pytest.raises(ServerSetupError) as exc_info:
        plan_server_setup(
            load_server_setup_request(request_path),
            layout=SetupLayout(managed_root),
        )
    assert exc_info.value.blocker == "invalid_broker_credential"
    assert not managed_root.exists()


def test_host_tool_resolution_uses_fixed_system_path(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    observed: dict[str, str | None] = {}
    validated: list[Path] = []

    def which(name: str, *, path: str | None = None) -> str:
        observed[name] = path
        return f"/usr/bin/{name}"

    monkeypatch.setenv("PATH", "/tmp/untrusted")
    monkeypatch.setattr(setup.shutil, "which", which)
    monkeypatch.setattr(
        setup,
        "_require_root_owned_executable",
        lambda value, **_kwargs: validated.append(value) or value,
    )
    assert setup._resolve_host_tool("systemctl") == Path("/usr/bin/systemctl")
    assert observed == {"systemctl": setup._SYSTEM_PATH}
    assert validated == [Path("/usr/bin/systemctl")]


def test_uv_resolution_uses_exact_configured_path_and_rejects_protected_home(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    selected: list[Path] = []
    monkeypatch.setenv("AGENTNET_UV", "/usr/local/lib/agentnet-runtime/uv")
    monkeypatch.setattr(
        setup,
        "_require_root_owned_executable",
        lambda value, **_kwargs: selected.append(value) or value,
    )
    assert setup._resolve_uv_executable() == Path("/usr/local/lib/agentnet-runtime/uv")
    assert selected == [Path("/usr/local/lib/agentnet-runtime/uv")]
    with pytest.raises(ServerSetupError) as exc_info:
        setup._require_service_visible_path(Path("/root/.local/bin/uv"), label="uv")
    assert exc_info.value.blocker == "service_executable_inaccessible"


def test_run_as_reports_nonzero_stage_without_leaking_stderr(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(
        setup,
        "_run_bounded_product_process",
        lambda *_args, **_kwargs: setup._BoundedCommandResult(
            returncode=1,
            stdout=b"",
            stderr_present=True,
        ),
    )
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_as(
            account,
            ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
            environment={"PATH": "/usr/bin:/bin"},
            stage="core_create",
        )
    assert exc_info.value.blocker == "product_command_failed"
    assert "core_create failed with exit status 1" in str(exc_info.value)
    assert "private-token" not in str(exc_info.value)


def test_run_as_accepts_only_network_create_not_ready_exit_with_structured_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    evidence = {
        "config": "/etc/agentnet/agentnet.json",
        "local_readiness": {
            "ready": False,
            "storage": {"ready": True},
            "audit": {"valid": True},
            "deployment_binding": {"ready": False},
        },
    }
    monkeypatch.setattr(
        setup,
        "_run_bounded_product_process",
        lambda *_args, **_kwargs: setup._BoundedCommandResult(
            returncode=1,
            stdout=json.dumps(evidence).encode("utf-8"),
            stderr_present=False,
        ),
    )
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())

    assert setup._run_as(
        account,
        ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
        environment={"PATH": "/usr/bin:/bin"},
        stage="core_create",
        accepted_returncodes=frozenset({1}),
    ) == evidence

    monkeypatch.setattr(
        setup,
        "_run_bounded_product_process",
        lambda *_args, **_kwargs: setup._BoundedCommandResult(
            returncode=0,
            stdout=json.dumps(evidence).encode("utf-8"),
            stderr_present=False,
        ),
    )
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_as(
            account,
            ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
            environment={"PATH": "/usr/bin:/bin"},
            stage="core_create",
            accepted_returncodes=frozenset({1}),
        )
    assert exc_info.value.blocker == "product_command_failed"


def test_systemctl_commands_are_bounded_and_redacted(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    observed: dict[str, object] = {}

    def timeout(argv: list[str], **kwargs: object) -> object:
        observed["argv"] = argv
        observed.update(kwargs)
        raise subprocess.TimeoutExpired(cmd="private-systemctl-detail", timeout=30)

    monkeypatch.setattr(setup.subprocess, "run", timeout)
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_systemctl(
            Path("/usr/bin/systemctl"),
            ["restart", setup.CORE_UNIT],
            failure_message="failed to start AgentNet managed units",
        )
    assert exc_info.value.blocker == "systemd_start"
    assert str(exc_info.value) == "failed to start AgentNet managed units"
    assert "private-systemctl-detail" not in str(exc_info.value)
    assert observed["timeout"] == 30
    assert observed["env"] == {
        "PATH": setup._SYSTEM_PATH,
        "HOME": "/root",
        "LANG": "C.UTF-8",
    }


def test_inactive_auxiliary_timer_does_not_require_service_only_main_pid() -> None:
    import agentnet.operations.server_setup as setup

    setup._validate_inactive_auxiliary_unit_state(
        unit=setup.CREDENTIAL_RENEW_TIMER,
        expected_unit_file_state="disabled",
        properties={
            "LoadState": "loaded",
            "UnitFileState": "disabled",
            "ActiveState": "inactive",
        },
    )

    with pytest.raises(ServerSetupError, match="fixed state"):
        setup._validate_inactive_auxiliary_unit_state(
            unit=setup.CREDENTIAL_RENEW_TIMER,
            expected_unit_file_state="disabled",
            properties={
                "LoadState": "loaded",
                "UnitFileState": "disabled",
                "ActiveState": "active",
            },
        )

    with pytest.raises(ServerSetupError, match="fixed state"):
        setup._validate_inactive_auxiliary_unit_state(
            unit=setup.CREDENTIAL_RENEW_UNIT,
            expected_unit_file_state="static",
            properties={
                "LoadState": "loaded",
                "UnitFileState": "static",
                "ActiveState": "inactive",
            },
        )

def test_credential_renewal_activation_stops_failed_oneshot_before_timer() -> None:
    import agentnet.operations.server_setup as setup

    assert setup._credential_renewal_activation_commands(c0_responder_required=True) == (
        ["stop", setup.CREDENTIAL_RENEW_UNIT],
        ["reset-failed", setup.CREDENTIAL_RENEW_UNIT],
        ["enable", "--now", setup.C0_RESPONDER_UNIT],
        ["enable", "--now", setup.CREDENTIAL_RENEW_TIMER],
    )
    assert setup._credential_renewal_activation_commands(c0_responder_required=False) == (
        ["stop", setup.CREDENTIAL_RENEW_UNIT],
        ["reset-failed", setup.CREDENTIAL_RENEW_UNIT],
        ["disable", "--now", setup.C0_RESPONDER_UNIT],
        ["enable", "--now", setup.CREDENTIAL_RENEW_TIMER],
    )


@pytest.mark.parametrize("next_run_usec", [None, 0, 1_785_868_799_999_999])
def test_credential_renewal_timer_requires_finite_future_schedule(
    next_run_usec: int | None,
) -> None:
    import agentnet.operations.server_setup as setup

    properties = {
        "LoadState": "loaded",
        "UnitFileState": "enabled",
        "ActiveState": "active",
    }
    with pytest.raises(ServerSetupError) as exc_info:
        setup._validate_active_renewal_timer_state(
            properties,
            next_run_usec=next_run_usec,
            now_usec=1_785_868_800_000_000,
        )
    assert exc_info.value.blocker == "credential_renewal_schedule"


def test_credential_renewal_timer_accepts_only_future_microsecond_epoch() -> None:
    import agentnet.operations.server_setup as setup

    properties = {
        "LoadState": "loaded",
        "UnitFileState": "enabled",
        "ActiveState": "active",
    }
    setup._validate_active_renewal_timer_state(
        properties,
        next_run_usec=1_785_869_100_000_000,
        now_usec=1_785_868_800_000_000,
    )

def test_core_create_evidence_accepts_only_exact_healthy_pre_enrollment_state() -> None:
    import agentnet.operations.server_setup as setup

    config_path = Path("/etc/agentnet/agentnet.json")
    evidence = {
        "config": str(config_path),
        "local_readiness": {
            "schema": "agentnet.core.readiness.v1",
            "ready": False,
            "artifact_mode": "enabled",
            "storage": {"ready": True},
            "audit": {"valid": True},
            "artifacts": {"ready": True},
            "deployment_binding": {"ready": False, "required": True},
            "a2a_schema": {"ready": True},
            "scanner_trust": {"ready": True},
        },
    }
    setup._require_core_create_evidence(
        evidence,
        config_path,
        artifact_mode="enabled",
    )

    mutations = (
        lambda item: item.update({"config": "/wrong/config.json"}),
        lambda item: item["local_readiness"].update({"schema": "wrong"}),
        lambda item: item["local_readiness"].update({"ready": True}),
        lambda item: item["local_readiness"]["storage"].update({"ready": False}),
        lambda item: item["local_readiness"]["audit"].update({"valid": False}),
        lambda item: item["local_readiness"]["artifacts"].update({"ready": False}),
        lambda item: item["local_readiness"]["deployment_binding"].update({"ready": True}),
        lambda item: item["local_readiness"]["deployment_binding"].update({"required": False}),
        lambda item: item["local_readiness"]["a2a_schema"].update({"ready": False}),
        lambda item: item["local_readiness"]["scanner_trust"].update({"ready": False}),
    )
    for mutate in mutations:
        changed = json.loads(json.dumps(evidence))
        mutate(changed)
        with pytest.raises(ServerSetupError) as exc_info:
            setup._require_core_create_evidence(
                changed,
                config_path,
                artifact_mode="enabled",
            )
        assert exc_info.value.blocker == "core_evidence"

    communication_only = json.loads(json.dumps(evidence))
    readiness = communication_only["local_readiness"]
    readiness["artifact_mode"] = "disabled"
    readiness["artifacts"] = {
        "enabled": False,
        "required": False,
        "ready": False,
        "reason": "disabled",
    }
    readiness["scanner_trust"] = {
        "enabled": False,
        "ready": False,
        "required": False,
        "trusted_key_count": 0,
    }
    setup._require_core_create_evidence(
        communication_only,
        config_path,
        artifact_mode="disabled",
    )


@pytest.mark.parametrize(
    ("failure", "expected"),
    [
        (subprocess.TimeoutExpired(cmd="agentnet", timeout=300), "core_create timed out"),
        (OSError("private path detail"), "core_create could not start"),
        (subprocess.SubprocessError("private preexec detail"), "core_create could not start"),
    ],
)
def test_run_as_reports_bounded_launch_failures(
    monkeypatch: pytest.MonkeyPatch,
    failure: BaseException,
    expected: str,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(
        setup,
        "_run_bounded_product_process",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(failure),
    )
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_as(
            account,
            ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
            environment={"PATH": "/usr/bin:/bin"},
            stage="core_create",
        )
    assert exc_info.value.blocker == "product_command_failed"
    assert str(exc_info.value) == expected
    assert "private path detail" not in str(exc_info.value)
    assert "private preexec detail" not in str(exc_info.value)


def test_bounded_product_process_redacts_preexec_launch_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(
        setup.subprocess,
        "Popen",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            subprocess.SubprocessError("private preexec detail")
        ),
    )
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())

    with pytest.raises(ServerSetupError) as exc_info:
        setup._run_bounded_product_process(
            account,
            ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
            environment={"PATH": "/usr/bin:/bin"},
            stage="core_create",
        )

    assert exc_info.value.blocker == "product_command_failed"
    assert str(exc_info.value) == "core_create could not start"
    assert "private preexec detail" not in str(exc_info.value)


def test_run_as_rejects_invalid_success_evidence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    outputs = iter(("", "[]"))
    monkeypatch.setattr(
        setup,
        "_run_bounded_product_process",
        lambda *_args, **_kwargs: setup._BoundedCommandResult(
            returncode=0,
            stdout=next(outputs).encode("utf-8"),
            stderr_present=False,
        ),
    )
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    for expected in ("invalid structured evidence", "invalid structured evidence"):
        with pytest.raises(ServerSetupError, match=expected) as exc_info:
            setup._run_as(
                account,
                ["/usr/bin/node", "/usr/local/bin/agentnet", "network", "create"],
                environment={"PATH": "/usr/bin:/bin"},
                stage="core_create",
            )
        assert exc_info.value.blocker == "invalid_product_evidence"


@pytest.mark.parametrize(
    ("stream", "size", "expected"),
    [
        ("stdout", 1_048_577, "oversized structured evidence"),
        ("stderr", 65_537, "oversized error evidence"),
    ],
)
def test_run_as_kills_real_product_process_on_evidence_overflow(
    monkeypatch: pytest.MonkeyPatch,
    stream: str,
    size: int,
    expected: str,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_drop_identity", lambda _account: lambda: None)
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    script = (
        "import sys; target=getattr(sys," + repr(stream) + ").buffer; "
        f"target.write(b'x'*{size}); target.flush()"
    )
    with pytest.raises(ServerSetupError, match=expected) as exc_info:
        setup._run_as(
            account,
            [sys.executable, "-c", script],
            environment={"PATH": "/usr/bin:/bin"},
            stage="bounded_evidence",
        )
    assert exc_info.value.blocker == "invalid_product_evidence"


@pytest.mark.skipif(not hasattr(os, "killpg"), reason="process groups are unavailable")
def test_bounded_product_process_rejects_inherited_pipe_eof_stall(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_drop_identity", lambda _account: lambda: None)
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    script = (
        "import subprocess,sys; "
        "subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); "
        "sys.stdout.write('{}'); sys.stdout.flush()"
    )
    started = time.monotonic()
    with pytest.raises(ServerSetupError, match="left structured evidence streams open") as exc_info:
        setup._run_as(
            account,
            [sys.executable, "-c", script],
            environment={"PATH": "/usr/bin:/bin"},
            stage="bounded_evidence",
        )
    assert exc_info.value.blocker == "invalid_product_evidence"
    assert time.monotonic() - started < 5


def test_managed_config_digest_uses_stable_persisted_json(
    tmp_path: Path,
) -> None:
    import agentnet.operations.server_setup as setup

    path = tmp_path / "config.json"
    payload = {
        "allowed_purposes": ["purpose.z", "purpose.a"],
        "enrolled_harness_id": "harness-changing-after-ceremony",
        "enrolled_credential_id": "credential-changing-after-ceremony",
        "schema_version": "1.0",
    }
    path.write_text(json.dumps(payload, indent=2), encoding="utf-8")
    path.chmod(0o600)
    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())

    assert setup._managed_config_digest(
        path,
        account,
        blocker="test_config",
        exclude_top_level=frozenset(
            {"enrolled_harness_id", "enrolled_credential_id"}
        ),
    ) == setup.canonical_digest(
        {
            "allowed_purposes": ["purpose.z", "purpose.a"],
            "schema_version": "1.0",
        }
    )


def test_setup_marker_migrates_and_revisions_same_request_state(
    tmp_path: Path,
) -> None:
    import agentnet.operations.server_setup as setup

    marker_path = tmp_path / "setup.json"
    legacy = {
        "schema": "agentnet.server-setup.marker.v1",
        "request_digest": "1" * 64,
        "approval_config_digest": "2" * 64,
        "core_config_digest": "3" * 64,
        "units": list(setup.MANAGED_UNITS),
    }
    legacy_payload = json.dumps(legacy, sort_keys=True).encode() + b"\n"
    marker_path.write_bytes(legacy_payload)
    marker_path.chmod(0o600)
    existing = setup._validated_setup_marker(
        legacy_payload,
        request_digest="4" * 64,
        legacy_request_digest="1" * 64,
    )
    units = {
        unit: f"{unit}\n".encode()
        for unit in setup.MANAGED_UNITS
    }
    status = setup._commit_setup_marker(
        marker_path,
        existing_payload=legacy_payload,
        existing_marker=existing,
        request_digest="4" * 64,
        approval_config_digest="5" * 64,
        core_config_digest="6" * 64,
        unit_payloads=units,
        uid=os.geteuid(),
        gid=os.getegid(),
    )
    assert status == "updated_same_request"
    v2_payload = marker_path.read_bytes()
    v2 = setup._validated_setup_marker(
        v2_payload,
        request_digest="4" * 64,
        legacy_request_digest="1" * 64,
    )
    assert v2 is not None
    assert v2["schema"] == "agentnet.server-setup.marker.v2"
    assert v2["revision"] == 1
    assert v2["previous_marker_digest"] == hashlib.sha256(legacy_payload).hexdigest()
    assert setup._commit_setup_marker(
        marker_path,
        existing_payload=v2_payload,
        existing_marker=v2,
        request_digest="4" * 64,
        approval_config_digest="5" * 64,
        core_config_digest="6" * 64,
        unit_payloads=units,
        uid=os.geteuid(),
        gid=os.getegid(),
    ) == "already_satisfied"
    latest_payload = marker_path.read_bytes()
    latest = setup._validated_setup_marker(
        latest_payload,
        request_digest="4" * 64,
        legacy_request_digest="1" * 64,
    )
    assert setup._commit_setup_marker(
        marker_path,
        existing_payload=latest_payload,
        existing_marker=latest,
        request_digest="4" * 64,
        approval_config_digest="5" * 64,
        core_config_digest="7" * 64,
        unit_payloads=units,
        uid=os.geteuid(),
        gid=os.getegid(),
    ) == "updated_same_request"
    revised = json.loads(marker_path.read_text(encoding="utf-8"))
    assert revised["revision"] == 2
    assert revised["core_config_digest"] == "7" * 64


@pytest.mark.parametrize(
    "mutation",
    [
        {"schema": "agentnet.server-setup.marker.v3"},
        {"request_digest": "9" * 64},
        {"units": list(reversed(MANAGED_UNITS))},
        {"unit_digests": {unit: ("invalid" if unit == CORE_UNIT else "8" * 64) for unit in MANAGED_UNITS}},
        {"unexpected": True},
    ],
)
def test_setup_marker_rejects_unknown_or_tampered_provenance(
    mutation: dict[str, object],
) -> None:
    import agentnet.operations.server_setup as setup

    marker: dict[str, object] = {
        "schema": "agentnet.server-setup.marker.v2",
        "request_digest": "4" * 64,
        "approval_config_digest": "5" * 64,
        "core_config_digest": "6" * 64,
        "units": list(MANAGED_UNITS),
        "package_version": "0.1.test",
        "previous_marker_digest": None,
        "revision": 1,
        "unit_digests": {unit: "7" * 64 for unit in MANAGED_UNITS},
    }
    marker.update(mutation)
    payload = json.dumps(marker, sort_keys=True).encode() + b"\n"
    with pytest.raises(ServerSetupError) as exc_info:
        setup._validated_setup_marker(
            payload,
            request_digest="4" * 64,
            legacy_request_digest="1" * 64,
        )
    assert exc_info.value.blocker == "setup_marker_conflict"


@pytest.mark.parametrize("artifact_mode", ["enabled", "disabled"])
def test_setup_marker_v3_binds_explicit_artifact_mode_and_rejects_legacy_marker(
    tmp_path: Path,
    artifact_mode: str,
) -> None:
    import agentnet.operations.server_setup as setup

    marker_path = tmp_path / "setup.json"
    units = {
        unit: f"{unit}\n".encode()
        for unit in setup.MANAGED_UNITS
    }
    assert setup._commit_setup_marker(
        marker_path,
        existing_payload=None,
        existing_marker=None,
        request_digest="4" * 64,
        approval_config_digest="5" * 64,
        core_config_digest="6" * 64,
        unit_payloads=units,
        artifact_mode=artifact_mode,
        uid=os.geteuid(),
        gid=os.getegid(),
    ) == "completed"
    payload = marker_path.read_bytes()
    marker = setup._validated_setup_marker(
        payload,
        request_digest="4" * 64,
        legacy_request_digest="",
        artifact_mode=artifact_mode,
    )
    assert marker is not None
    assert marker["schema"] == "agentnet.server-setup.marker.v3"
    assert marker["artifact_mode"] == artifact_mode

    with pytest.raises(ServerSetupError, match="version or provenance"):
        setup._validated_setup_marker(
            payload,
            request_digest="4" * 64,
            legacy_request_digest="",
            artifact_mode="disabled" if artifact_mode == "enabled" else "enabled",
        )

    legacy = dict(marker)
    legacy.pop("artifact_mode")
    legacy["schema"] = "agentnet.server-setup.marker.v2"
    with pytest.raises(ServerSetupError, match="version or provenance"):
        setup._validated_setup_marker(
            json.dumps(legacy, sort_keys=True).encode() + b"\n",
            request_digest="4" * 64,
            legacy_request_digest="",
            artifact_mode=artifact_mode,
        )


def test_setup_marker_compare_and_swap_detects_external_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    path = tmp_path / "setup.json"
    expected = b'{"marker":"expected"}\n'
    path.write_bytes(expected)
    path.chmod(0o600)
    reads = 0
    original_read = setup._read_setup_marker

    def changed_on_second_read(target: Path, *, uid: int, gid: int) -> bytes | None:
        nonlocal reads
        reads += 1
        if reads == 2:
            return b'{"marker":"changed"}\n'
        return original_read(target, uid=uid, gid=gid)

    monkeypatch.setattr(setup, "_read_setup_marker", changed_on_second_read)
    with pytest.raises(ServerSetupError, match="changed before compare-and-swap"):
        setup._atomic_replace_exact(
            path,
            expected=expected,
            payload=b'{"marker":"replacement"}\n',
            mode=0o600,
            uid=os.geteuid(),
            gid=os.getegid(),
        )
    assert path.read_bytes() == expected


@pytest.mark.parametrize("request_version", ["v1", "v2-disabled"])
def test_apply_resumes_after_interruption_and_restarts_only_managed_core(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    request_version: str,
) -> None:
    request_path = (
        _request(tmp_path)
        if request_version == "v1"
        else _communication_only_request(tmp_path)
    )
    request = load_server_setup_request(request_path)
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    import agentnet.operations.server_setup as setup

    uid = os.geteuid()
    gid = os.getegid()
    accounts = {
        setup.CORE_USER: SimpleNamespace(pw_name=setup.CORE_USER, pw_uid=uid, pw_gid=gid),
        setup.APPROVAL_USER: SimpleNamespace(pw_name=setup.APPROVAL_USER, pw_uid=uid, pw_gid=gid),
        setup.C0_RESPONDER_USER: SimpleNamespace(pw_name=setup.C0_RESPONDER_USER, pw_uid=uid, pw_gid=gid),
    }
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_ensure_account", lambda name, _home, **_kwargs: accounts[name])
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    monkeypatch.setattr(
        setup,
        "_postgres_peer_gate",
        lambda _account, _database_url: {"status": "validated_exact_local_peer"},
    )

    signer = P256KeyPair.generate()
    trusted = IndependentApproverConfig(
        principal_id=request.approval_approver_principal_id,
        authority_kind="human",
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
    )
    changed_signer = P256KeyPair.generate()
    changed_trusted = IndependentApproverConfig(
        principal_id=request.approval_approver_principal_id,
        authority_kind="human",
        signer_key_id=changed_signer.thumbprint,
        public_key_pem=changed_signer.public_pem,
        allowed_purposes=MANDATORY_APPROVAL_PURPOSES,
    )
    drift_trust_during_apply = False
    trust_reads = 0

    def fake_approval_trust(_path: Path, _account: object, _state: Path):
        nonlocal trust_reads
        trust_reads += 1
        effective = changed_trusted if drift_trust_during_apply and trust_reads % 2 == 0 else trusted
        return SimpleNamespace(model_dump=lambda **_kwargs: {"policy": "fixed"}), [effective]

    monkeypatch.setattr(setup, "_approval_trust", fake_approval_trust)
    monkeypatch.setattr(setup, "_require_exact_approval_policy", lambda *_args, **_kwargs: None)

    class _Equal:
        def __eq__(self, _other: object) -> bool:
            return True

    enrolled = False
    config_drift = False
    config_generation = 0
    mutate_bootstrap_once = False

    def fake_load_config(_text: str):
        return SimpleNamespace(
            profile=setup.RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            domain_id=request.domain_id,
            data_dir=layout.host(setup.CORE_DATA) / "core",
            database_url=request.database_url,
            database_url_env=request.database_url_env,
            artifact_mode=request.effective_artifact_mode,
            artifact_backend="postgres-manifest",
            artifact_dir=layout.host(setup.CORE_DATA) / "core" / "artifacts",
            public_base_url=request.core_public_origin,
            effective_service_audience=request.service_audience,
            runtime_instance_id="drifted-runtime" if config_drift else request.runtime_instance_id,
            oidc_enrollment=_Equal(),
            scanner_trust=_Equal() if request.effective_artifact_mode == "enabled" else None,
            server_agent_capabilities=(
                {
                    setup.ServerAgentCapability.OFFLINE_CUSTODY,
                    setup.ServerAgentCapability.ARTIFACT_STORAGE,
                }
                if request.effective_artifact_mode == "enabled"
                else {setup.ServerAgentCapability.OFFLINE_CUSTODY}
            ),
            a2a=None,
            local_bindings=None,
            relay=None,
            federation_trust=None,
            postgres_recovery_topology=False,
            enrolled_harness_id="server-harness" if enrolled else None,
            enrolled_credential_id="server-credential" if enrolled else None,
            model_dump=lambda **_kwargs: {
                "immutable": "fixed",
                "generation": config_generation,
            },
        )

    monkeypatch.setattr(setup, "load_config_json", fake_load_config)

    fail_network = True
    product_calls: list[list[str]] = []

    def fake_run_as(_account, argv, *, environment, stage, accepted_returncodes=frozenset({0})):
        nonlocal fail_network, config_generation, mutate_bootstrap_once
        product_calls.append(argv)
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
            assert accepted_returncodes == frozenset({1})
            if fail_network:
                fail_network = False
                raise ServerSetupError("injected_failure", "injected network interruption")
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
                    "artifact_mode": request.effective_artifact_mode,
                    "storage": {"ready": True},
                    "audit": {"valid": True},
                    "artifacts": (
                        {"ready": True}
                        if request.effective_artifact_mode == "enabled"
                        else {
                            "enabled": False,
                            "required": False,
                            "ready": False,
                            "reason": "disabled",
                        }
                    ),
                    "deployment_binding": {"ready": False, "required": True},
                    "a2a_schema": {"ready": True},
                    "scanner_trust": (
                        {"ready": True}
                        if request.effective_artifact_mode == "enabled"
                        else {
                            "enabled": False,
                            "required": False,
                            "ready": False,
                            "trusted_key_count": 0,
                        }
                    ),
                },
            }
        if command[0] == "bootstrap-server-agent":
            if mutate_bootstrap_once:
                config_generation += 1
                config_path = Path(argv[argv.index("--config") + 1])
                config_path.write_text(
                    json.dumps({"generation": config_generation}),
                    encoding="utf-8",
                )
                mutate_bootstrap_once = False
            return _bootstrap_evidence(request.domain_id)
        raise AssertionError(argv)

    monkeypatch.setattr(setup, "_run_as", fake_run_as)
    approved_digest = str(plan_server_setup(request, layout=layout)["request_digest"])
    with pytest.raises(ServerSetupError, match="frozen human-approved digest"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest="0" * 64,
            layout=layout,
            _allow_test_layout=True,
        )
    assert not layout.host(setup.APPROVAL_CONFIG).exists()

    layout.lock.parent.mkdir(parents=True, mode=0o700)
    lock_target = tmp_path / "lock-target"
    lock_target.write_text("", encoding="utf-8")
    layout.lock.symlink_to(lock_target)
    with pytest.raises(ServerSetupError, match="lock custody"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    layout.lock.unlink()
    os.mkfifo(layout.lock, mode=0o600)
    with pytest.raises(ServerSetupError, match="lock custody"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    layout.lock.unlink()

    with pytest.raises(ServerSetupError, match="injected network interruption"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert layout.host(setup.APPROVAL_CONFIG).exists()
    assert not layout.host(setup.CORE_CONFIG).exists()

    resumed = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert resumed["status"] == "configured_not_started"
    marker = layout.host(setup.SETUP_MARKER)
    assert marker.parent == layout.host(Path("/var/lib/agentnet-setup"))
    assert stat.S_IMODE(marker.parent.stat().st_mode) == 0o700
    assert stat.S_IMODE(marker.stat().st_mode) == 0o600
    mutate_bootstrap_once = True
    rerun = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert rerun["status"] == "configured_not_started"
    assert any(
        step["id"] == "setup_marker" and step["status"] == "updated_same_request"
        for step in rerun["steps"]
    )
    assert json.loads(marker.read_text(encoding="utf-8"))["revision"] == 2
    stable_rerun = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert any(
        step["id"] == "setup_marker" and step["status"] == "already_satisfied"
        for step in stable_rerun["steps"]
    )
    if request.effective_artifact_mode == "disabled":
        artifact_key = layout.host(setup.CORE_DATA) / "core" / "secrets" / "artifact.key"
        artifact_key.parent.mkdir(parents=True, exist_ok=True)
        artifact_key.parent.chmod(0o700)
        artifact_key.write_bytes(b"x" * 32)
        artifact_key.chmod(0o600)
        with pytest.raises(ServerSetupError, match="forbidden artifact state"):
            apply_server_setup(
                request,
                start=False,
                expected_request_digest=approved_digest,
                layout=layout,
                _allow_test_layout=True,
            )
        artifact_key.unlink()
        artifact_key.symlink_to(tmp_path / "missing-artifact-key")
        with pytest.raises(ServerSetupError, match="forbidden artifact state"):
            apply_server_setup(
                request,
                start=False,
                expected_request_digest=approved_digest,
                layout=layout,
                _allow_test_layout=True,
            )
        artifact_key.unlink()
        artifact_dir = layout.host(setup.CORE_DATA) / "core" / "artifacts"
        artifact_dir.mkdir(parents=True)
        artifact_dir.chmod(0o700)
        with pytest.raises(ServerSetupError, match="forbidden artifact state"):
            apply_server_setup(
                request,
                start=False,
                expected_request_digest=approved_digest,
                layout=layout,
                _allow_test_layout=True,
            )
        artifact_dir.rmdir()
    assert all(argv[:2] == ["/usr/bin/node", "/usr/local/bin/agentnet"] for argv in product_calls)
    network_call = next(argv for argv in product_calls if argv[2:4] == ["network", "create"])
    assert "--database-url-from-env" in network_call
    assert request.database_url not in network_call
    assert network_call[network_call.index("--artifact-mode") + 1] == request.effective_artifact_mode
    if request.effective_artifact_mode == "enabled":
        assert "--scanner-trust-config" in network_call
        assert layout.host(setup.CORE_DATA / "scanner-trust.json").is_file()
    else:
        assert "--scanner-trust-config" not in network_call
        assert not layout.host(setup.CORE_DATA / "scanner-trust.json").exists()
        assert json.loads(marker.read_text(encoding="utf-8"))["schema"] == "agentnet.server-setup.marker.v3"
        assert json.loads(marker.read_text(encoding="utf-8"))["artifact_mode"] == "disabled"
        assert not (
            layout.host(setup.CORE_DATA) / "core" / "secrets" / "artifact.key"
        ).exists()
        assert not (layout.host(setup.CORE_DATA) / "core" / "artifacts").exists()

    bootstrap_calls = sum(argv[2] == "bootstrap-server-agent" for argv in product_calls)
    drift_trust_during_apply = True
    trust_reads = 0
    with pytest.raises(ServerSetupError, match="Approval trust changed during setup"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    drift_trust_during_apply = False
    # Pre-write Approval trust revalidation rejects drift before Core bootstrap.
    assert sum(argv[2] == "bootstrap-server-agent" for argv in product_calls) == bootstrap_calls

    config_drift = True
    with pytest.raises(ServerSetupError, match="Core state conflicts"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert sum(argv[2] == "bootstrap-server-agent" for argv in product_calls) == bootstrap_calls
    config_drift = False

    original_atomic_write = setup._atomic_write
    fail_core_unit_once = True

    def interrupted_unit_write(path: Path, payload: bytes, **kwargs: object) -> str:
        nonlocal fail_core_unit_once
        if path == layout.core_unit and fail_core_unit_once:
            fail_core_unit_once = False
            raise ServerSetupError("injected_failure", "injected unit interruption")
        return original_atomic_write(path, payload, **kwargs)

    mutate_bootstrap_once = True
    monkeypatch.setattr(setup, "_atomic_write", interrupted_unit_write)
    with pytest.raises(ServerSetupError, match="injected unit interruption"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert json.loads(marker.read_text(encoding="utf-8"))["revision"] == 2
    monkeypatch.setattr(setup, "_atomic_write", original_atomic_write)
    recovered_unit = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert any(
        step["id"] == "setup_marker" and step["status"] == "updated_same_request"
        for step in recovered_unit["steps"]
    )
    assert json.loads(marker.read_text(encoding="utf-8"))["revision"] == 3

    original_commit_marker = setup._commit_setup_marker
    mutate_bootstrap_once = True
    monkeypatch.setattr(
        setup,
        "_commit_setup_marker",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ServerSetupError("injected_failure", "injected marker interruption")
        ),
    )
    with pytest.raises(ServerSetupError, match="injected marker interruption"):
        apply_server_setup(
            request,
            start=False,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert json.loads(marker.read_text(encoding="utf-8"))["revision"] == 3
    monkeypatch.setattr(setup, "_commit_setup_marker", original_commit_marker)
    recovered_marker = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert any(
        step["id"] == "setup_marker" and step["status"] == "updated_same_request"
        for step in recovered_marker["steps"]
    )
    assert json.loads(marker.read_text(encoding="utf-8"))["revision"] == 4

    enrolled = True
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

    class ReadyBrokerClient:
        def __init__(self, *_args, **_kwargs):
            pass

        def readiness(self):
            return {"schema": "agentnet.approval.internal-readiness-result.v1", "status": "ready"}

        def close(self):
            pass

    monkeypatch.setattr(setup, "ApprovalServiceClient", ReadyBrokerClient)
    systemctl_calls: list[list[str]] = []
    health_requests: list[tuple[str, dict[str, object]]] = []
    health_attempts: list[tuple[str, object]] = []
    live_pids = {
        setup.APPROVAL_UNIT: 4321,
        setup.CORE_UNIT: 4322,
        setup.C0_RESPONDER_UNIT: 4323,
    }
    live_argv = {
        setup.APPROVAL_UNIT: (
            "/usr/bin/node", "/usr/local/bin/agentnet", "approval", "serve",
            "--config", str(layout.host(setup.APPROVAL_CONFIG)),
            "--host", "127.0.0.1", "--port", str(setup.APPROVAL_PORT),
        ),
        setup.CORE_UNIT: (
            "/usr/bin/node", "/usr/local/bin/agentnet", "serve",
            "--config", str(layout.host(setup.CORE_CONFIG)),
            "--host", "127.0.0.1", "--port", str(setup.CORE_PORT),
        ),
        setup.C0_RESPONDER_UNIT: (
            "/usr/bin/node", "/usr/local/bin/agentnet", "c0-pilot", "responder",
            "--run", "--config", str(layout.host(setup.C0_RESPONDER_CONFIG)),
            "--credential", "/run/credentials/agentnet-c0-responder.service/signing-key.pem",
        ),
    }
    disabled_units: set[str] = set()

    def fake_systemctl_run(argv, **_kwargs):
        systemctl_calls.append(argv)
        arguments = argv[1:]
        if arguments and arguments[0] == "list-timers":
            unit = arguments[1]
            rows = (
                []
                if unit in disabled_units
                else [{"unit": unit, "next": 9_000_000_000_000_000_000}]
            )
            return SimpleNamespace(
                returncode=0,
                stdout=json.dumps(rows).encode("utf-8"),
            )
        if len(arguments) >= 3 and arguments[:2] == ["disable", "--now"]:
            unit = arguments[2]
            disabled_units.add(unit)
            live_pids.pop(unit, None)
        elif len(arguments) >= 3 and arguments[:2] == ["enable", "--now"]:
            unit = arguments[2]
            disabled_units.discard(unit)
            if unit == setup.C0_RESPONDER_UNIT:
                live_pids[unit] = 4323
        return SimpleNamespace(returncode=0, stdout=b"")

    monkeypatch.setattr(setup.subprocess, "run", fake_systemctl_run)
    monkeypatch.setattr(
        setup,
        "_systemd_show",
        lambda _executable, unit: {
            "LoadState": "loaded",
            "UnitFileState": (
                "static"
                if unit == setup.CREDENTIAL_RENEW_UNIT
                else "disabled" if unit in disabled_units else "enabled"
            ),
            "FragmentPath": str(layout.unit(unit)),
            "DropInPaths": "",
            "User": (
                setup.APPROVAL_USER
                if unit == setup.APPROVAL_UNIT
                else setup.C0_RESPONDER_USER if unit == setup.C0_RESPONDER_UNIT else setup.CORE_USER
            ),
            "Group": (
                setup.APPROVAL_USER
                if unit == setup.APPROVAL_UNIT
                else setup.C0_RESPONDER_USER if unit == setup.C0_RESPONDER_UNIT else setup.CORE_USER
            ),
            "NoNewPrivileges": "yes",
            "PrivateDevices": "yes",
            "PrivateTmp": "yes",
            "ProtectHome": "yes",
            "ProtectSystem": "strict",
            "ActiveState": (
                "active"
                if (
                    unit in live_pids
                    or (
                        unit == setup.CREDENTIAL_RENEW_TIMER
                        and unit not in disabled_units
                    )
                )
                else "inactive"
            ),
            **(
                {}
                if unit == setup.CREDENTIAL_RENEW_TIMER
                else {"MainPID": str(live_pids.get(unit, 0))}
            ),
            "Environment": "AGENTNET_UV=/usr/local/bin/uv",
            "ReadWritePaths": str(
                layout.host(
                    setup.APPROVAL_DATA
                    if unit == setup.APPROVAL_UNIT
                    else setup.C0_RESPONDER_DATA if unit == setup.C0_RESPONDER_UNIT else setup.CORE_DATA
                )
            ),
        },
    )
    monkeypatch.setattr(
        setup,
        "_read_live_process_identity",
        lambda pid: (
            Path("/usr/bin/node"),
            live_argv[next(unit for unit, live_pid in live_pids.items() if live_pid == pid)],
        ),
    )
    fail_health_once = True

    def health_with_interruption(url: str, **_kwargs: object) -> None:
        nonlocal fail_health_once
        if fail_health_once:
            fail_health_once = False
            raise ServerSetupError("service_health", "injected health interruption")
        health_requests.append((url, dict(_kwargs["expected"])))
        health_attempts.append((url, _kwargs.get("attempts")))

    monkeypatch.setattr(setup, "_health", health_with_interruption)
    with pytest.raises(ServerSetupError, match="injected health interruption") as exc_info:
        apply_server_setup(
            request,
            start=True,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.identity_enrolled is True
    assert not layout.host(setup.C0_RESPONDER_CONFIG).exists()
    assert [
        "/usr/bin/systemctl",
        "enable",
        "--now",
        setup.CREDENTIAL_RENEW_TIMER,
    ] not in systemctl_calls
    assert [
        "/usr/bin/systemctl",
        "enable",
        "--now",
        setup.C0_RESPONDER_UNIT,
    ] not in systemctl_calls

    def fail_operational_readiness(url: str, **_kwargs: object) -> None:
        if url.endswith("/readyz"):
            raise ServerSetupError(
                "service_readiness",
                "injected expired deployment binding",
            )
        health_requests.append((url, dict(_kwargs["expected"])))

    systemctl_calls.clear()
    health_requests.clear()
    monkeypatch.setattr(setup, "_health", fail_operational_readiness)
    with pytest.raises(ServerSetupError, match="expired deployment binding") as exc_info:
        apply_server_setup(
            request,
            start=True,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.identity_enrolled is True
    assert not layout.host(setup.C0_RESPONDER_CONFIG).exists()
    assert [
        "/usr/bin/systemctl",
        "enable",
        "--now",
        setup.CREDENTIAL_RENEW_TIMER,
    ] not in systemctl_calls
    assert [
        "/usr/bin/systemctl",
        "enable",
        "--now",
        setup.C0_RESPONDER_UNIT,
    ] not in systemctl_calls

    systemctl_calls.clear()
    health_requests.clear()
    health_attempts.clear()
    monkeypatch.setattr(setup, "_health", health_with_interruption)
    started = apply_server_setup(
        request,
        start=True,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert started["status"] == "operational"
    assert started["identity_enrolled"] is True
    assert started["authority_granted"] is False

    successful_systemctl_calls = list(systemctl_calls)
    successful_health_requests = list(health_requests)
    successful_health_attempts = list(health_attempts)

    class BlockedBrokerClient(ReadyBrokerClient):
        def readiness(self):
            raise setup.GateBlocked("approval_broker_auth", "private broker detail")

    monkeypatch.setattr(setup, "ApprovalServiceClient", BlockedBrokerClient)
    with pytest.raises(ServerSetupError, match="Approval broker readiness failed") as exc_info:
        apply_server_setup(
            request,
            start=True,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "approval_broker_auth"
    assert "private broker detail" not in "".join(traceback.format_exception(exc_info.value))
    assert exc_info.value.__cause__ is None
    assert exc_info.value.identity_enrolled is True

    class UnavailableBrokerClient:
        def __init__(self, *_args: object, **_kwargs: object) -> None:
            raise setup.GateBlocked(
                "approval_broker_unavailable",
                "private TLS context detail",
            )

    monkeypatch.setattr(setup, "ApprovalServiceClient", UnavailableBrokerClient)
    with pytest.raises(ServerSetupError, match="Approval broker readiness failed") as exc_info:
        apply_server_setup(
            request,
            start=True,
            expected_request_digest=approved_digest,
            layout=layout,
            _allow_test_layout=True,
        )
    assert exc_info.value.blocker == "approval_broker_unavailable"
    rendered_error = "".join(traceback.format_exception(exc_info.value))
    assert "private TLS context detail" not in rendered_error
    assert exc_info.value.__cause__ is None
    assert exc_info.value.identity_enrolled is True

    monkeypatch.setattr(setup, "ApprovalServiceClient", ReadyBrokerClient)
    recovered_after_broker_failure = apply_server_setup(
        request,
        start=True,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert recovered_after_broker_failure["status"] == "operational"
    systemctl_calls[:] = successful_systemctl_calls
    health_requests[:] = successful_health_requests
    health_attempts[:] = successful_health_attempts
    assert systemctl_calls == [
        ["/usr/bin/systemctl", "daemon-reload"],
        ["/usr/bin/systemctl", "disable", "--now", setup.CREDENTIAL_RENEW_TIMER],
        ["/usr/bin/systemctl", "disable", "--now", setup.C0_RESPONDER_UNIT],
        ["/usr/bin/systemctl", "stop", setup.CREDENTIAL_RENEW_UNIT],
        ["/usr/bin/systemctl", "enable", "--now", setup.APPROVAL_UNIT],
        ["/usr/bin/systemctl", "enable", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "restart", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.APPROVAL_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "stop", setup.CREDENTIAL_RENEW_UNIT],
        ["/usr/bin/systemctl", "reset-failed", setup.CREDENTIAL_RENEW_UNIT],
        ["/usr/bin/systemctl", "enable", "--now", setup.C0_RESPONDER_UNIT],
        ["/usr/bin/systemctl", "enable", "--now", setup.CREDENTIAL_RENEW_TIMER],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.APPROVAL_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.CREDENTIAL_RENEW_TIMER],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.C0_RESPONDER_UNIT],
        [
            "/usr/bin/systemctl",
            "list-timers",
            setup.CREDENTIAL_RENEW_TIMER,
            "--no-pager",
            "--no-legend",
            "--plain",
            "--output=json",
        ],
    ]
    assert [url for url, _expected in health_requests] == [
        "http://127.0.0.1:8090/healthz",
        "http://127.0.0.1:8080/healthz",
        "https://approval.corp.example/healthz",
        "https://core.corp.example/healthz",
        "http://127.0.0.1:8080/readyz",
        "https://core.corp.example/readyz",
    ]
    assert health_attempts == [
        ("http://127.0.0.1:8090/healthz", setup._START_HEALTH_ATTEMPTS),
        ("http://127.0.0.1:8080/healthz", setup._START_HEALTH_ATTEMPTS),
        ("https://approval.corp.example/healthz", setup._START_HEALTH_ATTEMPTS),
        ("https://core.corp.example/healthz", setup._START_HEALTH_ATTEMPTS),
        ("http://127.0.0.1:8080/readyz", None),
        ("https://core.corp.example/readyz", setup._START_HEALTH_ATTEMPTS),
    ]
    core_expected = [
        expected
        for url, expected in health_requests
        if "127.0.0.1:8080" in url or url.startswith(request.core_public_origin)
    ]
    assert core_expected
    for expected in core_expected:
        assert expected["artifact_mode"] == request.effective_artifact_mode
        assert expected["server_agent_capabilities"] == (
            ["artifact_storage", "offline_custody"]
            if request.effective_artifact_mode == "enabled"
            else ["offline_custody"]
        )
        if "deployment_binding" in expected:
            assert expected["deployment_binding"]["credential_state"] == (
                "current",
                "renewal_needed",
            )

    responder_config = layout.host(setup.C0_RESPONDER_CONFIG)
    responder_terminal = layout.host(setup.C0_RESPONDER_TERMINAL)
    responder_terminal.write_text(
        json.dumps(
            {
                "schema": "agentnet.c0-pilot-responder.terminal.v1",
                "status": "COMPLETED_C0_ROUND_TRIP",
                "domain_id": request.domain_id,
                "harness_id": "server-harness",
                "credential_id": "server-credential",
            }
        ),
        encoding="utf-8",
    )
    responder_terminal.chmod(0o600)
    # Simulate response loss after terminal marker commit but before config cleanup.
    # Same-digest setup must remove only the validated private responder config.
    disabled_units.add(setup.C0_RESPONDER_UNIT)
    live_pids.pop(setup.C0_RESPONDER_UNIT)
    systemctl_calls.clear()
    health_requests.clear()

    terminal_rerun = apply_server_setup(
        request,
        start=True,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )

    assert terminal_rerun["status"] == "operational"
    assert not responder_config.exists()
    assert any(
        step == {"id": "c0_responder_config", "status": "terminal_cleanup_reconciled"}
        for step in terminal_rerun["steps"]
    )
    assert ["/usr/bin/systemctl", "disable", "--now", setup.C0_RESPONDER_UNIT] in systemctl_calls
    assert ["/usr/bin/systemctl", "enable", "--now", setup.C0_RESPONDER_UNIT] not in systemctl_calls

    systemctl_calls.clear()
    health_requests.clear()
    terminal_retry = apply_server_setup(
        request,
        start=True,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert terminal_retry["status"] == "operational"
    assert any(
        step == {"id": "c0_responder_config", "status": "terminal_not_recreated"}
        for step in terminal_retry["steps"]
    )


def test_clean_state_gate_rejects_unowned_preexisting_state_and_resumes_exact_attempt(
    tmp_path: Path,
) -> None:
    import agentnet.operations.server_setup as setup

    attempt = tmp_path / "attempt.json"
    with pytest.raises(ServerSetupError, match="no current-package setup custody"):
        setup._prepare_setup_attempt(
            attempt,
            existing_marker=None,
            preexisting_state=True,
            request_digest="a" * 64,
            uid=os.geteuid(),
            gid=os.getegid(),
        )

    attempt.write_text(
        json.dumps(
            {
                "schema": "agentnet.server-setup.attempt.v1",
                "package_version": setup.__version__,
                "request_digest": "a" * 64,
            }
        ),
        encoding="utf-8",
    )
    attempt.chmod(0o600)
    assert setup._prepare_setup_attempt(
        attempt,
        existing_marker=None,
        preexisting_state=True,
        request_digest="a" * 64,
        uid=os.geteuid(),
        gid=os.getegid(),
    ) == ("resumed_exact_attempt", True)


def test_unexpected_setup_error_is_redacted(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    request = _request(tmp_path)
    from agentnet.cli.commands import server_agent as cli_setup

    monkeypatch.setattr(cli_setup, "plan_server_setup", lambda _request: (_ for _ in ()).throw(RuntimeError("private-token")))
    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup", "--request", str(request)])
    )
    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["blocker"] == "internal_setup_failure"
    assert "private-token" not in json.dumps(output)


def test_plan_cli_returns_structured_tls_environment_blocker(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    request = _request(tmp_path)
    private_value = str(tmp_path / "private-ca.pem")
    monkeypatch.setenv("SSL_CERT_FILE", private_value)

    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup", "--request", str(request)])
    )

    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    elapsed = output.pop("setup_elapsed_seconds")
    assert isinstance(elapsed, float)
    assert output == {
        "schema": "agentnet.server-setup.evidence.v1",
        "status": "blocked",
        "blocker": "approval_broker_auth",
        "message": "Approval broker TLS environment is unsupported",
        "authority_granted": False,
        "identity_enrolled": False,
        "production_durability_proven": False,
        "phase": "plan",
        "responsible_component": "agentnet-server-setup",
        "safe_action": "retained exact resumable setup state",
        "rerun_resumes": True,
        "human_action": "supply the named prerequisite or approval, then rerun the same command",
    }
    assert private_value not in json.dumps(output)


def test_apply_requires_frozen_plan_digest(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    request = _request(tmp_path)
    rc = command_server_agent_setup(
        build_parser().parse_args(
            ["server-agent", "setup", "--request", str(request), "--apply"]
        )
    )
    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["blocker"] == "approval_digest_required"


def test_start_without_apply_returns_typed_blocker(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    request = _request(tmp_path)
    rc = command_server_agent_setup(
        build_parser().parse_args(
            ["server-agent", "setup", "--request", str(request), "--start"]
        )
    )
    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["status"] == "blocked"
    assert output["blocker"] == "invalid_action"
    assert output["authority_granted"] is False
