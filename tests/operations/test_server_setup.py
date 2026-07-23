from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet.approval.config import (
    MANDATORY_APPROVAL_PURPOSES,
    ApprovalOwnerOIDCConfig,
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
)
from agentnet.operations.config import IndependentApproverConfig
from agentnet.cli import build_parser, command_server_agent_setup
from agentnet.security.signatures import P256KeyPair
from agentnet.operations.server_setup import (
    APPROVAL_UNIT,
    CORE_UNIT,
    ServerSetupError,
    SetupApprover,
    SetupLayout,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
    render_units,
)


def _private_json(path: Path, value: object) -> Path:
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path


def _request(tmp_path: Path) -> Path:
    core_env = tmp_path / "core.env"
    core_env.write_text(
        "AGENTNET_DATABASE_URL=postgresql://agentnet@127.0.0.1/agentnet\n"
        "AGENTNET_CORE_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    approval_env = tmp_path / "approval.env"
    approval_env.write_text(
        "AGENTNET_APPROVAL_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token\n",
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
            "trusted_public_keys": {"scanner-key": P256KeyPair.generate().public_pem},
            "required_engine": "synthetic-scanner",
            "required_rules_digest": "a" * 64,
            "required_profile_digest": "b" * 64,
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
            "database_url": "postgresql://agentnet@127.0.0.1/agentnet",
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


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_launcher_preflight_digest_matches_python_plan(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    request = load_server_setup_request(request_path)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_resolve_host_tool", lambda name: Path(f"/usr/bin/{name}"))
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    expected = plan_server_setup(request)["request_digest"]
    module = (Path(__file__).parents[2] / "npm/lib/server-setup-preflight.mjs").as_uri()
    script = f"""
      import {{ privilegedApprovalDigest }} from {json.dumps(module)};
      const request = process.argv.at(-1);
      console.log(privilegedApprovalDigest([
        'server-agent', 'setup', '--request', request, '--apply',
      ], process.env));
    """
    completed = subprocess.run(
        [str(shutil.which("node")), "--input-type=module", "-e", script, str(request_path)],
        env={
            "PATH": "/usr/bin:/bin",
            "SUDO_UID": str(os.geteuid()),
        },
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == expected


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
    original_read = setup.os.read

    def mutate_after_read(descriptor: int, size: int) -> bytes:
        payload = original_read(descriptor, size)
        path.write_bytes(b'{"b":2}')
        path.chmod(0o600)
        return payload

    monkeypatch.setattr(setup.os, "read", mutate_after_read)
    with pytest.raises(ServerSetupError, match="changed while being read"):
        setup._read_private_input(path, label="test input")


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


def test_private_managed_read_detects_same_size_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import agentnet.operations.server_setup as setup

    account = SimpleNamespace(pw_uid=os.geteuid(), pw_gid=os.getegid())
    path = tmp_path / "managed.json"
    path.write_bytes(b'{"a":1}')
    path.chmod(0o600)
    original_read = setup.os.read

    def mutate_after_read(descriptor: int, size: int) -> bytes:
        payload = original_read(descriptor, size)
        path.write_bytes(b'{"b":2}')
        path.chmod(0o600)
        return payload

    monkeypatch.setattr(setup.os, "read", mutate_after_read)
    with pytest.raises(ServerSetupError, match="changed while being read"):
        setup._read_private_managed_file(
            path,
            account,
            blocker="test_custody",
            max_bytes=1024,
        )


def test_temporary_or_user_owned_launcher_is_rejected(tmp_path: Path) -> None:
    import agentnet.operations.server_setup as setup

    launcher = tmp_path / "agentnet"
    launcher.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    launcher.chmod(0o755)
    with pytest.raises(ServerSetupError, match="unsafe"):
        setup._require_root_owned_executable(launcher, label="agentnet")


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
    assert set(units) == {APPROVAL_UNIT, CORE_UNIT}
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
    assert "SupplementaryGroups=" in rendered
    assert "UnsetEnvironment=NODE_OPTIONS NODE_PATH PYTHONPATH" in rendered
    assert "synthetic-test-secret" not in rendered
    assert "AGENTNET_APPROVAL_CORE_TOKEN=" not in rendered


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
    assert report["managed_units"] == [APPROVAL_UNIT, CORE_UNIT]
    assert report["https_topology"] == "external_self_hosted_reverse_proxy_to_loopback"
    assert report["package_version"]
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

        def open(self, _url: str, *, timeout: int):
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


def test_plan_rejects_database_reference_drift(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    core_env.write_text(
        "AGENTNET_DATABASE_URL=postgresql://other@127.0.0.1/agentnet\n"
        "AGENTNET_CORE_OIDC_CLIENT_SECRET=synthetic-test-secret\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token\n",
        encoding="utf-8",
    )
    core_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="database reference does not match"):
        plan_server_setup(load_server_setup_request(request_path))


def test_plan_rejects_shell_syntax_in_runtime_environment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request_path = _request(tmp_path)
    value = json.loads(request_path.read_text(encoding="utf-8"))
    core_env = Path(value["core_environment_file"])
    core_env.write_text(
        "AGENTNET_DATABASE_URL=postgresql://agentnet@127.0.0.1/agentnet\n"
        "AGENTNET_CORE_OIDC_CLIENT_SECRET='quoted value'\n"
        "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-shared-test-token\n",
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
        "AGENTNET_APPROVAL_CORE_TOKEN=different-token\n",
        encoding="utf-8",
    )
    approval_env.chmod(0o600)
    import agentnet.operations.server_setup as setup

    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    with pytest.raises(ServerSetupError, match="broker credential references do not match"):
        plan_server_setup(load_server_setup_request(request_path))


def test_apply_resumes_after_interruption_and_restarts_only_managed_core(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    request = load_server_setup_request(_request(tmp_path))
    layout = SetupLayout(tmp_path / "host")
    layout.root.mkdir()
    import agentnet.operations.server_setup as setup

    uid = os.geteuid()
    gid = os.getegid()
    accounts = {
        setup.CORE_USER: SimpleNamespace(pw_name=setup.CORE_USER, pw_uid=uid, pw_gid=gid),
        setup.APPROVAL_USER: SimpleNamespace(pw_name=setup.APPROVAL_USER, pw_uid=uid, pw_gid=gid),
    }
    monkeypatch.setattr(setup, "_resolve_node_executable", lambda: Path("/usr/bin/node"))
    monkeypatch.setattr(setup, "_resolve_uv_executable", lambda: Path("/usr/local/bin/uv"))
    monkeypatch.setattr(setup, "_resolve_executable", lambda *_args, **_kwargs: Path("/usr/local/bin/agentnet"))
    monkeypatch.setattr(setup, "_account_fact", lambda _name, _home: "create")
    monkeypatch.setattr(setup, "_ensure_account", lambda name, _home, **_kwargs: accounts[name])
    monkeypatch.setattr(setup.shutil, "which", lambda name: f"/usr/bin/{name}")

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
        lambda _path, _account, _state: (
            SimpleNamespace(model_dump=lambda **_kwargs: {"policy": "fixed"}),
            [trusted],
        ),
    )
    monkeypatch.setattr(setup, "_require_exact_approval_policy", lambda *_args, **_kwargs: None)

    class _Equal:
        def __eq__(self, _other: object) -> bool:
            return True

    enrolled = False
    config_drift = False

    def fake_load_config(_text: str):
        return SimpleNamespace(
            profile=setup.RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            domain_id=request.domain_id,
            data_dir=layout.host(setup.CORE_DATA) / "core",
            database_url=request.database_url,
            database_url_env=request.database_url_env,
            artifact_backend="postgres-manifest",
            artifact_dir=layout.host(setup.CORE_DATA) / "core" / "artifacts",
            public_base_url=request.core_public_origin,
            effective_service_audience=request.service_audience,
            runtime_instance_id="drifted-runtime" if config_drift else request.runtime_instance_id,
            oidc_enrollment=_Equal(),
            scanner_trust=_Equal(),
            a2a=None,
            local_bindings=None,
            relay=None,
            federation_trust=None,
            postgres_recovery_topology=False,
            enrolled_harness_id="server-harness" if enrolled else None,
            enrolled_credential_id="server-credential" if enrolled else None,
            model_dump=lambda **_kwargs: {"immutable": "fixed"},
        )

    monkeypatch.setattr(setup, "load_config_json", fake_load_config)

    fail_network = True
    product_calls: list[list[str]] = []

    def fake_run_as(_account, argv, *, environment, allowed_returncodes=(0,)):
        nonlocal fail_network
        product_calls.append(argv)
        command = argv[2:]
        if command[:2] == ["approval", "provision"]:
            config_path = Path(argv[argv.index("--config") + 1])
            config_path.write_text("{}", encoding="utf-8")
            config_path.chmod(0o600)
            return {"schema": "agentnet.approval.provision-result.v1"}
        if command[:2] == ["approval", "status"]:
            return {"ready": True}
        if command[:2] == ["network", "create"]:
            if fail_network:
                fail_network = False
                raise ServerSetupError("injected_failure", "injected network interruption")
            config_path = Path(argv[argv.index("--config") + 1])
            config_path.write_text("{}", encoding="utf-8")
            config_path.chmod(0o600)
            return {
                "config": str(config_path),
                "local_readiness": {"storage": {"ready": True}, "audit": {"valid": True}},
            }
        if command[0] == "bootstrap-server-agent":
            return {"ready": True}
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
    rerun = apply_server_setup(
        request,
        start=False,
        expected_request_digest=approved_digest,
        layout=layout,
        _allow_test_layout=True,
    )
    assert rerun["status"] == "configured_not_started"
    assert any(
        step["id"] == "setup_marker" and step["status"] == "already_satisfied"
        for step in rerun["steps"]
    )
    assert all(argv[:2] == ["/usr/bin/node", "/usr/local/bin/agentnet"] for argv in product_calls)
    network_call = next(argv for argv in product_calls if argv[2:4] == ["network", "create"])
    assert "--database-url-from-env" in network_call
    assert request.database_url not in network_call

    bootstrap_calls = sum(argv[2] == "bootstrap-server-agent" for argv in product_calls)
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

    enrolled = True
    systemctl_calls: list[list[str]] = []
    health_urls: list[str] = []
    monkeypatch.setattr(
        setup.subprocess,
        "run",
        lambda argv, **_kwargs: systemctl_calls.append(argv) or SimpleNamespace(returncode=0),
    )
    monkeypatch.setattr(setup, "_health", lambda url, **_kwargs: health_urls.append(url))
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
    assert systemctl_calls == [
        ["/usr/bin/systemctl", "daemon-reload"],
        ["/usr/bin/systemctl", "enable", "--now", setup.APPROVAL_UNIT],
        ["/usr/bin/systemctl", "enable", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "restart", setup.CORE_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.APPROVAL_UNIT],
        ["/usr/bin/systemctl", "is-active", "--quiet", setup.CORE_UNIT],
    ]
    assert health_urls == [
        "http://127.0.0.1:8090/healthz",
        "http://127.0.0.1:8080/healthz",
        "https://approval.corp.example/healthz",
        "https://core.corp.example/healthz",
        "http://127.0.0.1:8080/readyz",
        "https://core.corp.example/readyz",
    ]


def test_unexpected_setup_error_is_redacted(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    request = _request(tmp_path)
    import agentnet.cli as cli

    monkeypatch.setattr(cli, "plan_server_setup", lambda _request: (_ for _ in ()).throw(RuntimeError("private-token")))
    rc = command_server_agent_setup(
        build_parser().parse_args(["server-agent", "setup", "--request", str(request)])
    )
    output = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert output["blocker"] == "internal_setup_failure"
    assert "private-token" not in json.dumps(output)


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
