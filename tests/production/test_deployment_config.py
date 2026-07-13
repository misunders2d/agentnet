from __future__ import annotations

import importlib.util
import json
import os
import re
import stat
from pathlib import Path
from urllib.parse import urlsplit

import pytest

from agentnet.operations.config import RuntimeProfile
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.security.signatures import P256KeyPair


ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = ROOT / "deploy" / "compose.production.json"
RENDERER_PATH = ROOT / "deploy" / "render_and_run.py"

_AGENT_OWNED_ENVIRONMENT = {
    "identity": (
        "AGENTNET_ENROLLED_HARNESS_ID",
        "AGENTNET_ENROLLED_CREDENTIAL_ID",
    ),
    "public_config": (
        "AGENTNET_OIDC_ENROLLMENT_CONFIG_FILE",
        "AGENTNET_SCANNER_TRUST_CONFIG_FILE",
    ),
    "capability": ("AGENTNET_SERVER_AGENT_CAPABILITIES",),
    "feature": ("AGENTNET_FEATURES",),
    "component": ("AGENTNET_COMPONENT_EVIDENCE",),
    "key": ("AGENTNET_A2A_PRIVATE_KEY_FILE",),
    "a2a": (
        "AGENTNET_A2A_ROUTE_TOKEN",
        "AGENTNET_A2A_RECIPIENT_HARNESS_ID",
        "AGENTNET_A2A_SIGNING_HARNESS_ID",
        "AGENTNET_A2A_SIGNING_CREDENTIAL_ID",
        "AGENTNET_A2A_SIGNING_SUCCESSORS_FILE",
        "AGENTNET_A2A_CARD_NAME",
        "AGENTNET_A2A_CARD_DESCRIPTION",
        "AGENTNET_A2A_CARD_VERSION",
        "AGENTNET_A2A_CARD_STREAMING",
        "AGENTNET_A2A_CARD_PUSH_NOTIFICATIONS",
    ),
    "grant": (
        "AGENTNET_A2A_GRANT_ID",
        "AGENTNET_A2A_ALLOWED_ACTIONS",
        "AGENTNET_A2A_ALLOWED_PEER_NAMESPACES",
        "AGENTNET_A2A_ALLOWED_OUTPUT_SINKS",
        "AGENTNET_A2A_GRANT_EXPIRES_AT",
        "AGENTNET_A2A_GRANT_REVOKED_AT",
        "AGENTNET_A2A_GRANT_REVISION",
    ),
    "callback": (
        "AGENTNET_A2A_CALLBACK_ALLOWED_HOSTS",
        "AGENTNET_A2A_CALLBACK_ALLOWED_PORTS",
        "AGENTNET_A2A_ALLOW_LOOPBACK_CALLBACK_HTTP_LAB",
    ),
    "endpoint": ("AGENTNET_PUBLIC_BASE_URL",),
}
_AGENT_OWNED_ENVIRONMENT_KEYS = frozenset(
    key for keys in _AGENT_OWNED_ENVIRONMENT.values() for key in keys
)


def _renderer():
    spec = importlib.util.spec_from_file_location("agentnet_deployment_renderer", RENDERER_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _deployment_environment() -> dict[str, str]:
    environ = {
        "AGENTNET_DATABASE_HOST": "postgres",
        "AGENTNET_DATABASE_PORT": "5432",
        "AGENTNET_DATABASE_NAME": "agentnet",
        "AGENTNET_DATABASE_USER": "agentnet",
        "AGENTNET_DOMAIN_ID": "corp.example",
        "AGENTNET_PUBLIC_BASE_URL": "https://agent-a.corp.example",
        "AGENTNET_SERVICE_AUDIENCE": "urn:agentnet:corp.example:corporate-api",
        "AGENTNET_RUNTIME_INSTANCE_ID": "server-agent-a",
        "AGENTNET_ENROLLED_HARNESS_ID": "externally-enrolled-harness-a",
        "AGENTNET_ENROLLED_CREDENTIAL_ID": "externally-enrolled-credential-a",
    }
    return environ


def test_compose_wires_two_ordinary_server_agents_to_shared_durable_storage() -> None:
    compose = json.loads(COMPOSE_PATH.read_text(encoding="utf-8"))
    services = compose["services"]
    server_agents = [services["server-agent-a"], services["server-agent-b"]]

    assert "postgres" in services
    assert "ports" not in services["postgres"]
    assert services["postgres"]["volumes"] == ["agentnet-postgres-data:/var/lib/postgresql/data"]
    assert compose["networks"]["agentnet-backplane"]["internal"] is True

    shared_database = {
        key: server_agents[0]["environment"][key]
        for key in (
            "AGENTNET_DATABASE_HOST",
            "AGENTNET_DATABASE_PORT",
            "AGENTNET_DATABASE_NAME",
            "AGENTNET_DATABASE_USER",
            "AGENTNET_DATABASE_PASSWORD_FILE",
        )
    }
    assert set(server_agents[0]["environment"]) == set(server_agents[1]["environment"])
    assert len(server_agents[0]["volumes"]) == len(server_agents[1]["volumes"])
    for owner, server_agent in zip(("A", "B"), server_agents, strict=True):
        assert {key: server_agent["environment"][key] for key in shared_database} == shared_database
        assert server_agent["environment"]["AGENTNET_DATABASE_HOST"] == "postgres"
        assert server_agent["environment"]["AGENTNET_COMMAND"] == "serve"
        assert server_agent["environment"]["AGENTNET_ARTIFACT_DIR"] == "/var/lib/agentnet/artifacts"
        assert server_agent["environment"]["AGENTNET_BIND_HOST"] == "127.0.0.1"
        assert server_agent["environment"]["AGENTNET_BIND_PORT"] == "8080"
        assert "agentnet-artifacts:/var/lib/agentnet/artifacts" in server_agent["volumes"]
        assert "agentnet-runtime-secrets:/var/lib/agentnet/secrets" in server_agent["volumes"]
        assert server_agent["depends_on"] == {"agentnet-bootstrap": {"condition": "service_completed_successfully"}}
        assert server_agent["read_only"] is True
        assert server_agent["cap_drop"] == ["ALL"]
        assert "no-new-privileges:true" in server_agent["security_opt"]
        assert server_agent["secrets"] == ["postgres_password"]
        assert server_agent["environment"]["AGENTNET_ENROLLED_HARNESS_ID"] == f"${{AGENTNET_AGENT_{owner}_HARNESS_ID:-}}"
        assert server_agent["environment"]["AGENTNET_ENROLLED_CREDENTIAL_ID"] == f"${{AGENTNET_AGENT_{owner}_CREDENTIAL_ID:-}}"
        assert server_agent["environment"]["AGENTNET_SERVER_AGENT_CAPABILITIES"] == (
            f"${{AGENTNET_AGENT_{owner}_CAPABILITIES:-offline_custody,artifact_storage}}"
        )

    assert server_agents[0]["environment"]["AGENTNET_RUNTIME_INSTANCE_ID"] != server_agents[1]["environment"]["AGENTNET_RUNTIME_INSTANCE_ID"]
    assert server_agents[0]["environment"]["AGENTNET_PUBLIC_BASE_URL"] != server_agents[1]["environment"]["AGENTNET_PUBLIC_BASE_URL"]
    assert set(compose["volumes"]) == {"agentnet-postgres-data", "agentnet-artifacts", "agentnet-runtime-secrets"}
    assert services["agentnet-bootstrap"]["depends_on"] == {"postgres": {"condition": "service_healthy"}}
    assert services["agentnet-bootstrap"]["environment"]["AGENTNET_COMMAND"] == "bootstrap"


@pytest.mark.parametrize(
    ("service_name", "owner", "other", "default_port"),
    (
        ("server-agent-a", "A", "B", 8081),
        ("server-agent-b", "B", "A", 8082),
    ),
)
def test_compose_rejects_every_cross_agent_owned_value(
    service_name: str,
    owner: str,
    other: str,
    default_port: int,
) -> None:
    compose = json.loads(COMPOSE_PATH.read_text(encoding="utf-8"))
    service = compose["services"][service_name]
    environment = service["environment"]
    owner_prefix = f"${{AGENTNET_AGENT_{owner}_"
    other_prefix = f"${{AGENTNET_AGENT_{other}_"

    scoped_environment_keys = {
        key
        for key, value in environment.items()
        if isinstance(value, str) and "${AGENTNET_AGENT_" in value
    }
    assert scoped_environment_keys == _AGENT_OWNED_ENVIRONMENT_KEYS
    for category, keys in _AGENT_OWNED_ENVIRONMENT.items():
        for key in keys:
            value = environment[key]
            assert value.startswith(owner_prefix), f"{service_name} {category} {key} is not owner-scoped"
            assert other_prefix not in value, f"{service_name} {category} {key} is cross-wired"

    assert other_prefix not in json.dumps(service, sort_keys=True)
    assert environment["AGENTNET_RUNTIME_INSTANCE_ID"] == service_name
    assert service["ports"] == [
        f"${{AGENTNET_AGENT_{owner}_BIND_ADDRESS:-127.0.0.1}}:"
        f"${{AGENTNET_AGENT_{owner}_PORT:-{default_port}}}:8443"
    ]
    private_mounts = [
        volume for volume in service["volumes"] if volume.endswith(":/run/agentnet-private:ro")
    ]
    assert private_mounts == [
        f"${{AGENTNET_AGENT_{owner}_PRIVATE_CONFIG_DIR:-./private/agent-{owner.casefold()}}}:"
        "/run/agentnet-private:ro"
    ]
    public_mounts = [
        volume for volume in service["volumes"] if volume.endswith(":/run/agentnet-public:ro")
    ]
    assert public_mounts == [
        f"${{AGENTNET_AGENT_{owner}_PUBLIC_CONFIG_DIR:-./config/agent-{owner.casefold()}}}:"
        "/run/agentnet-public:ro"
    ]


def test_compose_terminates_tls_before_loopback_only_plaintext_upstreams() -> None:
    compose = json.loads(COMPOSE_PATH.read_text(encoding="utf-8"))
    services = compose["services"]
    nginx = (ROOT / "deploy" / "nginx-agent.conf").read_text(encoding="utf-8")

    assert "listen 8443 ssl;" in nginx
    assert "ssl_protocols TLSv1.2 TLSv1.3;" in nginx
    assert "proxy_pass http://127.0.0.1:8080;" in nginx
    assert "proxy_set_header X-Forwarded-Proto https;" in nginx
    for owner in ("a", "b"):
        agent = services[f"server-agent-{owner}"]
        proxy = services[f"tls-proxy-{owner}"]
        assert agent["environment"]["AGENTNET_BIND_HOST"] == "127.0.0.1"
        assert all(not port.endswith(":8080") for port in agent["ports"])
        assert all(port.endswith(":8443") for port in agent["ports"])
        assert proxy["network_mode"] == f"service:server-agent-{owner}"
        assert "networks" not in proxy and "ports" not in proxy
        assert proxy["read_only"] is True
        assert proxy["cap_drop"] == ["ALL"]
        assert proxy["user"] == "101:101"
        assert proxy["depends_on"] == {
            f"server-agent-{owner}": {"condition": "service_healthy"}
        }
        secret_sources = {item["source"] for item in proxy["secrets"]}
        assert secret_sources == {f"agent_{owner}_tls_cert", f"agent_{owner}_tls_key"}
        assert all(item["mode"] == 0o444 for item in proxy["secrets"])
        other = "b" if owner == "a" else "a"
        assert f"agent_{other}_tls_" not in json.dumps(proxy, sort_keys=True)


def test_renderer_emits_secret_free_parseable_server_agent_config(tmp_path: Path) -> None:
    renderer = _renderer()
    environ = _deployment_environment()
    config = renderer.build_config(environ)

    assert config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT
    assert config.artifact_backend == "postgres-manifest"
    assert config.artifact_dir == Path("/var/lib/agentnet/artifacts")
    assert config.data_dir == Path("/var/lib/agentnet")
    assert config.server_agent_capabilities == {
        ServerAgentCapability.OFFLINE_CUSTODY,
        ServerAgentCapability.ARTIFACT_STORAGE,
    }
    assert config.policies.confidentiality.c3_enabled_by_default is False
    assert config.policies.outage.privileged_operations == "hold"
    assert not any(config.features.model_dump().values())
    parsed_url = urlsplit(config.database_url)
    assert parsed_url.scheme == "postgresql"
    assert parsed_url.hostname == "postgres"
    assert parsed_url.username == "agentnet"
    assert parsed_url.password is None

    config_path = tmp_path / "config.json"
    renderer.write_config(config, config_path)
    exported = json.loads(config_path.read_text(encoding="utf-8"))
    assert "password" not in exported["database_url"].casefold()
    assert stat.S_IMODE(config_path.stat().st_mode) == 0o600

    pgpass_path = tmp_path / "pgpass"
    renderer.write_pgpass(environ, r"colon:and\\slash", pgpass_path)
    assert pgpass_path.read_text(encoding="utf-8") == "postgres:5432:agentnet:agentnet:colon\\:and\\\\\\\\slash\n"
    assert stat.S_IMODE(pgpass_path.stat().st_mode) == 0o600


def _write_public_json(path: Path, value: dict[str, object]) -> None:
    path.write_text(json.dumps(value), encoding="utf-8")
    os.chmod(path, 0o644)


def test_renderer_loads_explicit_owner_only_a2a_signing_successors(tmp_path: Path) -> None:
    renderer = _renderer()
    private_dir = tmp_path / "private"
    private_dir.mkdir(mode=0o700)
    key = P256KeyPair.generate()
    key_path = private_dir / "rotated.pem"
    key_path.write_bytes(key.private_pem)
    os.chmod(key_path, 0o600)
    lineage_path = tmp_path / "signing-lineage.json"
    _write_public_json(
        lineage_path,
        {
            "successors": [
                {
                    "credential_id": "rotated-credential-epoch-2",
                    "private_key_path": str(key_path),
                }
            ]
        },
    )
    successors = renderer._a2a_signing_successors(
        {"AGENTNET_A2A_SIGNING_SUCCESSORS_FILE": str(lineage_path)}
    )
    assert len(successors) == 1
    assert successors[0].credential_id == "rotated-credential-epoch-2"
    assert successors[0].private_key_path == key_path


def test_renderer_supports_oidc_only_first_boot_and_public_scanner_trust(tmp_path: Path) -> None:
    renderer = _renderer()
    environ = _deployment_environment()
    environ.pop("AGENTNET_ENROLLED_HARNESS_ID")
    environ.pop("AGENTNET_ENROLLED_CREDENTIAL_ID")
    environ["AGENTNET_PUBLIC_BASE_URL"] = "https://agent-a.corp.example"
    approver = P256KeyPair.generate()
    scanner = P256KeyPair.generate()
    oidc_path = tmp_path / "oidc-enrollment.json"
    scanner_path = tmp_path / "scanner-trust.json"
    _write_public_json(
        oidc_path,
        {
            "issuer": "https://identity.corp.example",
            "allowed_endpoint_origins": ["https://identity.corp.example"],
            "client_id": "agentnet-agent-a",
            "redirect_uri": "https://agent-a.corp.example/v1/enrollment/oidc/callback",
            "audience": "agentnet-agent-a",
            "allowed_signing_algorithms": ["ES256"],
            "pinned_jwk_thumbprints": {"idp-key-1": "a" * 64},
            "binding_assurance": "os_bound",
            "verifier_id": "independent-approval-verifier",
            "trusted_approvers": [
                {
                    "principal_id": "security-approver",
                    "signer_key_id": approver.thumbprint,
                    "public_key_pem": approver.public_pem,
                    "allowed_purposes": [
                        "authorization.entitlement.bootstrap.approve",
                        "authorization.elevation.approve",
                        "identity.credential.recover.approve",
                        "identity.enrollment.approve",
                        "identity.harness.revoke.approve",
                        "organization.relationship.accept",
                    ],
                }
            ],
        },
    )
    _write_public_json(
        scanner_path,
        {
            "trusted_public_keys": {"maintained-scanner:1": scanner.public_pem},
            "required_engine": "clamav",
            "required_rules_digest": "b" * 64,
            "required_profile_digest": "c" * 64,
            "max_attestation_age_seconds": 300,
            "allowed_future_skew_seconds": 30,
            "revoked_key_epochs": [],
        },
    )
    environ["AGENTNET_OIDC_ENROLLMENT_CONFIG_FILE"] = str(oidc_path)
    environ["AGENTNET_SCANNER_TRUST_CONFIG_FILE"] = str(scanner_path)

    config = renderer.build_config(environ)

    assert config.enrolled_harness_id is None
    assert config.enrolled_credential_id is None
    assert config.oidc_enrollment is not None
    assert config.oidc_enrollment.allowed_endpoint_origins == (
        "https://identity.corp.example",
    )
    assert config.oidc_enrollment.trusted_approvers[0].signer_key_id == approver.thumbprint
    assert config.scanner_trust is not None
    assert config.scanner_trust.trusted_public_keys == {
        "maintained-scanner:1": scanner.public_pem
    }
    report = renderer.dry_run_report(config)
    assert report["enrollment_mode"] == "oidc_first_boot"
    assert report["scanner_trust_configured"] is True
    config_path = tmp_path / "rendered.json"
    renderer.write_config(config, config_path)
    rendered = config_path.read_text(encoding="utf-8")
    assert "PRIVATE KEY" not in rendered
    exported = json.loads(rendered)
    assert exported["oidc_enrollment"]["trusted_approvers"][0]["public_key_pem"] == approver.public_pem


def test_renderer_first_boot_fails_closed_without_oidc_or_complete_binding(tmp_path: Path) -> None:
    renderer = _renderer()
    environ = _deployment_environment()
    environ.pop("AGENTNET_ENROLLED_HARNESS_ID")
    environ.pop("AGENTNET_ENROLLED_CREDENTIAL_ID")
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)

    environ["AGENTNET_ENROLLED_HARNESS_ID"] = "only-one-half"
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)

    unsafe = tmp_path / "unsafe.json"
    _write_public_json(unsafe, {"not": "oidc"})
    os.chmod(unsafe, 0o666)
    environ.pop("AGENTNET_ENROLLED_HARNESS_ID")
    environ["AGENTNET_OIDC_ENROLLMENT_CONFIG_FILE"] = str(unsafe)
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)


def test_renderer_plaintext_upstream_is_loopback_only(tmp_path: Path) -> None:
    renderer = _renderer()
    config_path = tmp_path / "config.json"
    argv = renderer.serve_argv({}, config_path)
    assert argv[argv.index("--host") + 1] == "127.0.0.1"
    assert argv[argv.index("--port") + 1] == "8080"
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.serve_argv({"AGENTNET_BIND_HOST": "0.0.0.0"}, config_path)
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.serve_argv({"AGENTNET_BIND_HOST": "server-agent-a"}, config_path)

    environ = _deployment_environment()
    environ["AGENTNET_PUBLIC_BASE_URL"] = "http://127.0.0.1:8080"
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)


@pytest.mark.parametrize("bad_value", ["mailbox_custody", "peer_relay", "unknown"])
def test_renderer_rejects_unknown_capability_vocabulary(bad_value: str) -> None:
    renderer = _renderer()
    environ = _deployment_environment()
    environ["AGENTNET_SERVER_AGENT_CAPABILITIES"] = bad_value
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)


def test_renderer_uses_secure_policy_defaults_without_owner_placeholders() -> None:
    config = _renderer().build_config(_deployment_environment())
    assert config.policies.elevation.break_glass_enabled is False


def test_renderer_local_bindings_require_owner_only_root_and_explicit_capability(
    tmp_path: Path,
) -> None:
    renderer = _renderer()
    private_dir = tmp_path / "private"
    private_dir.mkdir(mode=0o700)
    root_path = private_dir / "ipc-root.key"
    root_path.write_bytes(b"owner-provisioned-local-ipc-root-32b")
    os.chmod(root_path, 0o600)
    socket_path = tmp_path / "runtime" / "agentnet.sock"
    environ = {
        **_deployment_environment(),
        "AGENTNET_FEATURES": "local_bindings",
        "AGENTNET_SERVER_AGENT_CAPABILITIES": "offline_custody,artifact_storage,local_binding",
        "AGENTNET_LOCAL_IPC_SOCKET_PATH": str(socket_path),
        "AGENTNET_LOCAL_IPC_CAPABILITY_ROOT_FILE": str(root_path),
    }

    config = renderer.build_config(environ)

    assert config.local_bindings is not None
    assert config.local_bindings.socket_path == socket_path
    assert config.local_bindings.capability_root_path == root_path
    assert renderer.dry_run_report(config)["local_bindings_configured"] is True

    os.chmod(root_path, 0o644)
    with pytest.raises(renderer.DeploymentConfigError, match="owner-only"):
        renderer.build_config(environ)

    os.chmod(root_path, 0o600)
    environ.pop("AGENTNET_LOCAL_IPC_SOCKET_PATH")
    with pytest.raises(renderer.DeploymentConfigError):
        renderer.build_config(environ)


def test_container_recipe_is_nonroot_and_base_tags_are_not_latest() -> None:
    dockerfile = (ROOT / "deploy" / "Dockerfile").read_text(encoding="utf-8")
    compose = json.loads(COMPOSE_PATH.read_text(encoding="utf-8"))

    assert dockerfile.startswith(
        "ARG AGENTNET_UV_IMAGE_DIGEST\n"
        "ARG AGENTNET_PYTHON_BASE_DIGEST\n"
        "FROM ghcr.io/astral-sh/uv@sha256:${AGENTNET_UV_IMAGE_DIGEST} AS uv-bin\n"
        "FROM python:3.13.13-slim-bookworm@sha256:${AGENTNET_PYTHON_BASE_DIGEST} AS runtime\n"
    )
    assert "COPY pyproject.toml uv.lock" in dockerfile
    assert dockerfile.count("uv sync --frozen --no-default-groups --group build") == 2
    assert "--no-build-isolation" in dockerfile
    assert "python -m pip install" not in dockerfile
    assert "USER 10001:10001" in dockerfile
    assert ":latest" not in dockerfile
    assert "@sha256:${AGENTNET_POSTGRES_IMAGE_DIGEST:?" in compose["services"]["postgres"]["image"]
    for service_name in ("agentnet-bootstrap", "server-agent-a", "server-agent-b"):
        service = compose["services"][service_name]
        assert "@sha256:${AGENTNET_SERVER_AGENT_IMAGE_DIGEST:?" in service["image"]
        assert "build" not in service
    for service_name in ("tls-proxy-a", "tls-proxy-b"):
        assert "@sha256:${AGENTNET_NGINX_IMAGE_DIGEST:?" in compose["services"][service_name]["image"]
    assert not re.search(r"@sha256:[0-9a-f]{64}", json.dumps(compose, sort_keys=True))
    assert all("privileged" not in service for service in compose["services"].values())
