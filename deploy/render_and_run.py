"""Render a secret-free server-agent config, prepare libpq auth, and exec AgentNet.

The PostgreSQL password is never placed in the JSON configuration or process
arguments.  It is copied from the Compose secret to a mode-0600 pgpass file on
the container's private tmpfs immediately before the ordinary server-agent
extension process is executed.
"""

from __future__ import annotations

import json
import ipaddress
import os
import re
import stat
import tempfile
from collections.abc import Mapping
from datetime import datetime
from pathlib import Path
from urllib.parse import quote, urlsplit

from agentnet.core.capabilities import ServerAgentCapability
from agentnet.operations.config import (
    A2AAgentCardConfig,
    A2AServiceConfig,
    A2ASigningCredentialConfig,
    A2ASigningIdentityConfig,
    A2AStandingGrantConfig,
    ExtensionConfig,
    FeatureFlags,
    LocalBindingConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
    ScannerTrustConfig,
)


_DNS_NAME = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$")
_EVIDENCE_NAME = re.compile(r"^[a-z][a-z0-9_]{1,63}$")
_FEATURE_NAMES = frozenset(FeatureFlags.model_fields)


class DeploymentConfigError(ValueError):
    """Raised before startup when a deployment input is absent or unsafe."""


def _required(environ: Mapping[str, str], name: str) -> str:
    value = environ.get(name, "")
    if not value or value != value.strip() or any(ord(character) < 0x21 for character in value):
        raise DeploymentConfigError(f"{name} is required and must be canonical")
    return value


def _required_text(environ: Mapping[str, str], name: str) -> str:
    value = environ.get(name, "")
    if (
        not value
        or value != value.strip()
        or any(ord(character) < 0x20 or ord(character) == 0x7F for character in value)
    ):
        raise DeploymentConfigError(f"{name} is required and must be canonical text")
    return value


def _database_parts(environ: Mapping[str, str]) -> tuple[str, int, str, str]:
    host = _required(environ, "AGENTNET_DATABASE_HOST")
    if not _DNS_NAME.fullmatch(host):
        raise DeploymentConfigError("AGENTNET_DATABASE_HOST must be a canonical DNS name")
    try:
        port = int(environ.get("AGENTNET_DATABASE_PORT", "5432"))
    except ValueError as exc:
        raise DeploymentConfigError("AGENTNET_DATABASE_PORT must be an integer") from exc
    if not 1 <= port <= 65535:
        raise DeploymentConfigError("AGENTNET_DATABASE_PORT must be between 1 and 65535")
    database = _required(environ, "AGENTNET_DATABASE_NAME")
    username = _required(environ, "AGENTNET_DATABASE_USER")
    return host, port, database, username


def _csv_values(
    environ: Mapping[str, str],
    name: str,
    *,
    required: bool = False,
) -> tuple[str, ...]:
    raw = environ.get(name, "")
    if not raw:
        if required:
            raise DeploymentConfigError(f"{name} is required")
        return ()
    values = tuple(raw.split(","))
    if (
        raw != raw.strip()
        or any(not value or value != value.strip() or any(ord(character) < 0x21 for character in value) for value in values)
        or len(set(values)) != len(values)
    ):
        raise DeploymentConfigError(f"{name} must be a canonical duplicate-free comma list")
    return values


def _strict_bool(environ: Mapping[str, str], name: str, *, default: bool) -> bool:
    raw = environ.get(name)
    if raw in {None, ""}:
        return default
    if raw == "true":
        return True
    if raw == "false":
        return False
    raise DeploymentConfigError(f"{name} must be exactly true or false")


def _component_evidence(environ: Mapping[str, str]) -> dict[str, str]:
    raw = environ.get("AGENTNET_COMPONENT_EVIDENCE", "")
    if not raw:
        return {}
    if len(raw.encode("utf-8")) > 16_384:
        raise DeploymentConfigError("AGENTNET_COMPONENT_EVIDENCE exceeds its bounded profile")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise DeploymentConfigError("AGENTNET_COMPONENT_EVIDENCE must be a JSON object") from exc
    if not isinstance(value, dict):
        raise DeploymentConfigError("AGENTNET_COMPONENT_EVIDENCE must be a JSON object")
    result: dict[str, str] = {}
    for key, evidence_id in value.items():
        if (
            not isinstance(key, str)
            or not _EVIDENCE_NAME.fullmatch(key)
            or not isinstance(evidence_id, str)
            or not 8 <= len(evidence_id) <= 256
            or evidence_id != evidence_id.strip()
            or any(ord(character) < 0x21 for character in evidence_id)
        ):
            raise DeploymentConfigError("AGENTNET_COMPONENT_EVIDENCE contains a non-canonical reference")
        result[key] = evidence_id
    return result


def _public_json_file(
    environ: Mapping[str, str],
    name: str,
) -> dict[str, object] | None:
    """Read bounded, integrity-protected public bootstrap configuration."""

    raw_path = environ.get(name, "")
    if not raw_path:
        return None
    path = Path(raw_path)
    if not path.is_absolute() or path.is_symlink():
        raise DeploymentConfigError(f"{name} must be an absolute non-symlink path")
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise DeploymentConfigError(f"{name} is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o022
            or not 2 <= metadata.st_size <= 1_048_576
        ):
            raise DeploymentConfigError(
                f"{name} must be a bounded regular file that is not group/other writable"
            )
        chunks: list[bytes] = []
        remaining = metadata.st_size
        while remaining:
            chunk = os.read(descriptor, min(remaining, 65_536))
            if not chunk:
                raise DeploymentConfigError(f"{name} changed while being read")
            chunks.append(chunk)
            remaining -= len(chunk)
    finally:
        os.close(descriptor)
    try:
        value = json.loads(b"".join(chunks))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise DeploymentConfigError(f"{name} must contain one UTF-8 JSON object") from exc
    if not isinstance(value, dict):
        raise DeploymentConfigError(f"{name} must contain one JSON object")
    return value


def _oidc_enrollment_config(environ: Mapping[str, str]) -> OIDCEnrollmentConfig | None:
    value = _public_json_file(environ, "AGENTNET_OIDC_ENROLLMENT_CONFIG_FILE")
    if value is None:
        return None
    try:
        return OIDCEnrollmentConfig.model_validate(value)
    except ValueError as exc:
        raise DeploymentConfigError("OIDC enrollment public configuration is invalid") from exc


def _scanner_trust_config(environ: Mapping[str, str]) -> ScannerTrustConfig | None:
    value = _public_json_file(environ, "AGENTNET_SCANNER_TRUST_CONFIG_FILE")
    if value is None:
        return None
    try:
        return ScannerTrustConfig.model_validate(value)
    except ValueError as exc:
        raise DeploymentConfigError("scanner trust public configuration is invalid") from exc


def _features(environ: Mapping[str, str]) -> FeatureFlags:
    names = frozenset(_csv_values(environ, "AGENTNET_FEATURES"))
    unknown = names - _FEATURE_NAMES
    if unknown:
        raise DeploymentConfigError("AGENTNET_FEATURES contains an unknown feature")
    return FeatureFlags(**{name: name in names for name in _FEATURE_NAMES})


def _local_binding_config(
    environ: Mapping[str, str],
    *,
    features: FeatureFlags,
) -> LocalBindingConfig | None:
    if not features.local_bindings:
        return None
    socket_path = Path(_required(environ, "AGENTNET_LOCAL_IPC_SOCKET_PATH"))
    root_path = _owner_only_key_path(
        Path(_required(environ, "AGENTNET_LOCAL_IPC_CAPABILITY_ROOT_FILE")),
        label="local IPC capability root",
    )
    if not socket_path.is_absolute() or socket_path.is_symlink():
        raise DeploymentConfigError("AGENTNET_LOCAL_IPC_SOCKET_PATH must be an absolute non-symlink path")
    try:
        ttl = int(environ.get("AGENTNET_LOCAL_IPC_CAPABILITY_TTL_SECONDS", "300"))
        max_frame = int(environ.get("AGENTNET_LOCAL_IPC_MAX_FRAME_BYTES", "1048576"))
    except ValueError as exc:
        raise DeploymentConfigError("local IPC TTL and frame limit must be integers") from exc
    try:
        return LocalBindingConfig(
            socket_path=socket_path,
            capability_root_path=root_path,
            capability_ttl_seconds=ttl,
            max_frame_bytes=max_frame,
        )
    except ValueError as exc:
        raise DeploymentConfigError("local IPC configuration is invalid") from exc


def _owner_only_key_path(path: Path, *, label: str) -> Path:
    if not path.is_absolute() or path.is_symlink():
        raise DeploymentConfigError(f"{label} must be an absolute non-symlink path")
    try:
        parent = path.parent.stat()
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise DeploymentConfigError(f"{label} is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
    finally:
        os.close(descriptor)
    if (
        path.parent.is_symlink()
        or parent.st_uid != os.geteuid()
        or parent.st_mode & 0o077
        or not stat.S_ISREG(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_nlink != 1
        or metadata.st_mode & 0o077
        or not 1 <= metadata.st_size <= 16 * 1024
    ):
        raise DeploymentConfigError(f"{label} must be a bounded owner-only file")
    return path


def _owner_only_key_reference(environ: Mapping[str, str]) -> Path:
    return _owner_only_key_path(
        Path(_required(environ, "AGENTNET_A2A_PRIVATE_KEY_FILE")),
        label="AGENTNET_A2A_PRIVATE_KEY_FILE",
    )


def _a2a_signing_successors(
    environ: Mapping[str, str],
) -> tuple[A2ASigningCredentialConfig, ...]:
    value = _public_json_file(environ, "AGENTNET_A2A_SIGNING_SUCCESSORS_FILE")
    if value is None:
        return ()
    if set(value) != {"successors"} or not isinstance(value["successors"], list):
        raise DeploymentConfigError(
            "AGENTNET_A2A_SIGNING_SUCCESSORS_FILE must contain only a successors array"
        )
    raw_successors = value["successors"]
    if len(raw_successors) > 64:
        raise DeploymentConfigError("A2A signing credential lineage exceeds its bound")
    successors: list[A2ASigningCredentialConfig] = []
    for index, item in enumerate(raw_successors):
        if not isinstance(item, dict) or set(item) != {"credential_id", "private_key_path"}:
            raise DeploymentConfigError("A2A signing successor entry is invalid")
        credential_id = item["credential_id"]
        private_key_path = item["private_key_path"]
        if not isinstance(credential_id, str) or not isinstance(private_key_path, str):
            raise DeploymentConfigError("A2A signing successor fields must be strings")
        try:
            successors.append(
                A2ASigningCredentialConfig(
                    credential_id=credential_id,
                    private_key_path=_owner_only_key_path(
                        Path(private_key_path),
                        label=f"A2A successor key {index}",
                    ),
                )
            )
        except ValueError as exc:
            raise DeploymentConfigError("A2A signing successor entry is invalid") from exc
    return tuple(successors)


def _optional_datetime(environ: Mapping[str, str], name: str) -> datetime | None:
    raw = environ.get(name, "")
    if not raw:
        return None
    try:
        value = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError as exc:
        raise DeploymentConfigError(f"{name} must be an ISO-8601 timestamp") from exc
    if value.tzinfo is None:
        raise DeploymentConfigError(f"{name} must include a timezone")
    return value


def _a2a_config(
    environ: Mapping[str, str],
    *,
    features: FeatureFlags,
    enrolled_harness_id: str | None,
    enrolled_credential_id: str | None,
) -> A2AServiceConfig | None:
    if not features.public_a2a:
        return None
    if enrolled_harness_id is None or enrolled_credential_id is None:
        raise DeploymentConfigError(
            "public A2A must remain disabled until the ordinary agent completes enrollment"
        )
    key_path = _owner_only_key_reference(environ)
    try:
        revision = int(environ.get("AGENTNET_A2A_GRANT_REVISION", "1"))
        callback_ports = frozenset(
            int(value) for value in (_csv_values(environ, "AGENTNET_A2A_CALLBACK_ALLOWED_PORTS") or ("443",))
        )
    except ValueError as exc:
        raise DeploymentConfigError("A2A grant revision and callback ports must be integers") from exc
    recipient_harness_id = environ.get("AGENTNET_A2A_RECIPIENT_HARNESS_ID") or enrolled_harness_id
    signing_harness_id = environ.get("AGENTNET_A2A_SIGNING_HARNESS_ID") or enrolled_harness_id
    signing_credential_id = environ.get("AGENTNET_A2A_SIGNING_CREDENTIAL_ID") or enrolled_credential_id
    grant_expires_at = _optional_datetime(environ, "AGENTNET_A2A_GRANT_EXPIRES_AT")
    if grant_expires_at is None:
        raise DeploymentConfigError("AGENTNET_A2A_GRANT_EXPIRES_AT is required")
    return A2AServiceConfig(
        route_token=_required(environ, "AGENTNET_A2A_ROUTE_TOKEN"),
        recipient_harness_id=recipient_harness_id,
        card=A2AAgentCardConfig(
            name=_required_text(environ, "AGENTNET_A2A_CARD_NAME"),
            description=_required_text(environ, "AGENTNET_A2A_CARD_DESCRIPTION"),
            version=_required(environ, "AGENTNET_A2A_CARD_VERSION"),
            streaming=_strict_bool(environ, "AGENTNET_A2A_CARD_STREAMING", default=True),
            push_notifications=_strict_bool(
                environ,
                "AGENTNET_A2A_CARD_PUSH_NOTIFICATIONS",
                default=False,
            ),
        ),
        standing_grant=A2AStandingGrantConfig(
            grant_id=_required(environ, "AGENTNET_A2A_GRANT_ID"),
            allowed_actions=frozenset(
                _csv_values(environ, "AGENTNET_A2A_ALLOWED_ACTIONS", required=True)
            ),
            allowed_peer_namespaces=frozenset(
                _csv_values(environ, "AGENTNET_A2A_ALLOWED_PEER_NAMESPACES")
            ),
            allowed_output_sinks=frozenset(
                _csv_values(environ, "AGENTNET_A2A_ALLOWED_OUTPUT_SINKS", required=True)
            ),
            expires_at=grant_expires_at,
            revoked_at=_optional_datetime(environ, "AGENTNET_A2A_GRANT_REVOKED_AT"),
            revision=revision,
        ),
        signing_identity=A2ASigningIdentityConfig(
            harness_id=signing_harness_id,
            credential_id=signing_credential_id,
            private_key_path=key_path,
            successors=_a2a_signing_successors(environ),
        ),
        callback_allowed_hosts=frozenset(
            _csv_values(environ, "AGENTNET_A2A_CALLBACK_ALLOWED_HOSTS")
        ),
        callback_allowed_ports=callback_ports,
        allow_loopback_callback_http_lab=_strict_bool(
            environ,
            "AGENTNET_A2A_ALLOW_LOOPBACK_CALLBACK_HTTP_LAB",
            default=False,
        ),
    )


def build_config(environ: Mapping[str, str]) -> ExtensionConfig:
    """Build one ordinary always-on server-agent extension configuration."""

    host, port, database, username = _database_parts(environ)
    capability_values = _csv_values(environ, "AGENTNET_SERVER_AGENT_CAPABILITIES") or (
        "offline_custody",
        "artifact_storage",
    )
    try:
        capabilities = frozenset(ServerAgentCapability(value) for value in capability_values)
    except ValueError as exc:
        raise DeploymentConfigError("AGENTNET_SERVER_AGENT_CAPABILITIES contains an unknown capability") from exc
    if not capabilities:
        raise DeploymentConfigError("AGENTNET_SERVER_AGENT_CAPABILITIES contains an unknown or empty capability set")

    public_base_url = _required(environ, "AGENTNET_PUBLIC_BASE_URL")
    if urlsplit(public_base_url).scheme != "https":
        raise DeploymentConfigError(
            "AGENTNET_PUBLIC_BASE_URL must be HTTPS at the deployment TLS reverse proxy"
        )
    database_url = (
        f"postgresql://{quote(username, safe='')}@{host}:{port}/{quote(database, safe='')}"
    )
    features = _features(environ)
    enrolled_harness_id = environ.get("AGENTNET_ENROLLED_HARNESS_ID") or None
    enrolled_credential_id = environ.get("AGENTNET_ENROLLED_CREDENTIAL_ID") or None
    if (enrolled_harness_id is None) != (enrolled_credential_id is None):
        raise DeploymentConfigError(
            "AGENTNET_ENROLLED_HARNESS_ID and AGENTNET_ENROLLED_CREDENTIAL_ID must be supplied together"
        )
    if enrolled_harness_id is not None:
        enrolled_harness_id = _required(environ, "AGENTNET_ENROLLED_HARNESS_ID")
        enrolled_credential_id = _required(environ, "AGENTNET_ENROLLED_CREDENTIAL_ID")
    oidc_enrollment = _oidc_enrollment_config(environ)
    if enrolled_harness_id is None and oidc_enrollment is None:
        raise DeploymentConfigError(
            "first boot requires OIDC enrollment public configuration or an existing enrolled binding"
        )
    return ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=_required(environ, "AGENTNET_DOMAIN_ID"),
        data_dir=Path(environ.get("AGENTNET_DATA_DIR", "/var/lib/agentnet")),
        database_url=database_url,
        artifact_backend="postgres-manifest",
        artifact_dir=Path(environ.get("AGENTNET_ARTIFACT_DIR", "/var/lib/agentnet/artifacts")),
        public_base_url=public_base_url,
        service_audience=_required(environ, "AGENTNET_SERVICE_AUDIENCE"),
        runtime_instance_id=_required(environ, "AGENTNET_RUNTIME_INSTANCE_ID"),
        enrolled_harness_id=enrolled_harness_id,
        enrolled_credential_id=enrolled_credential_id,
        server_agent_capabilities=capabilities,
        features=features,
        a2a=_a2a_config(
            environ,
            features=features,
            enrolled_harness_id=enrolled_harness_id,
            enrolled_credential_id=enrolled_credential_id,
        ),
        local_bindings=_local_binding_config(environ, features=features),
        oidc_enrollment=oidc_enrollment,
        scanner_trust=_scanner_trust_config(environ),
        component_evidence=_component_evidence(environ),
    )


def dry_run_report(config: ExtensionConfig) -> dict[str, object]:
    """Content-free validation output; it grants no identity or peer trust."""

    return {
        "valid": True,
        "profile": config.profile.value,
        "instance_id": config.runtime_instance_id,
        "public_base_url": config.public_base_url,
        "capabilities": sorted(capability.value for capability in config.server_agent_capabilities),
        "features": sorted(name for name, enabled in config.features.model_dump().items() if enabled),
        "a2a_configured": config.a2a is not None,
        "local_bindings_configured": config.local_bindings is not None,
        "enrollment_mode": (
            "pre_enrolled" if config.enrolled_harness_id else "oidc_first_boot"
        ),
        "oidc_token_endpoint_auth_method": (
            config.oidc_enrollment.token_endpoint_auth_method.value
            if config.oidc_enrollment is not None
            else None
        ),
        "scanner_trust_configured": config.scanner_trust is not None,
        "implicit_peer_trust": False,
    }


def _atomic_write(path: Path, payload: bytes, *, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_descriptor = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_descriptor)
        finally:
            os.close(directory_descriptor)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise


def write_config(config: ExtensionConfig, path: Path) -> None:
    payload = json.dumps(config.redacted_export(), indent=2, sort_keys=True).encode("utf-8") + b"\n"
    _atomic_write(path, payload)


def _read_password(path: Path) -> str:
    try:
        password = path.read_text(encoding="utf-8").rstrip("\r\n")
    except OSError as exc:
        raise DeploymentConfigError("PostgreSQL password secret is unreadable") from exc
    if not password or "\n" in password or "\r" in password or "\x00" in password:
        raise DeploymentConfigError("PostgreSQL password secret has an invalid encoding")
    return password


def write_pgpass(environ: Mapping[str, str], password: str, path: Path) -> None:
    host, port, database, username = _database_parts(environ)

    def escaped(value: str) -> str:
        return value.replace("\\", "\\\\").replace(":", "\\:")

    line = ":".join(escaped(value) for value in (host, str(port), database, username, password))
    _atomic_write(path, (line + "\n").encode("utf-8"))


def serve_argv(environ: Mapping[str, str], config_path: Path) -> list[str]:
    """Build only the loopback plaintext upstream accepted by ``agentnet serve``."""

    host = environ.get("AGENTNET_BIND_HOST", "127.0.0.1")
    try:
        address = ipaddress.ip_address(host)
    except ValueError as exc:
        raise DeploymentConfigError("AGENTNET_BIND_HOST must be an explicit loopback address") from exc
    if not address.is_loopback:
        raise DeploymentConfigError(
            "AGENTNET_BIND_HOST must remain loopback behind the deployment TLS reverse proxy"
        )
    try:
        port = int(environ.get("AGENTNET_BIND_PORT", "8080"))
    except ValueError as exc:
        raise DeploymentConfigError("AGENTNET_BIND_PORT must be an integer") from exc
    if not 1 <= port <= 65_535:
        raise DeploymentConfigError("AGENTNET_BIND_PORT must be between 1 and 65535")
    log_level = environ.get("AGENTNET_LOG_LEVEL", "info")
    if log_level not in {"critical", "error", "warning", "info", "debug", "trace"}:
        raise DeploymentConfigError("AGENTNET_LOG_LEVEL is invalid")
    return [
        "agentnet",
        "serve",
        "--config",
        str(config_path),
        "--host",
        str(address),
        "--port",
        str(port),
        "--log-level",
        log_level,
    ]


def main() -> int:
    config = build_config(os.environ)
    command = os.environ.get("AGENTNET_COMMAND", "serve")
    if command == "validate":
        print(json.dumps(dry_run_report(config), separators=(",", ":"), sort_keys=True))
        return 0
    config_path = Path(os.environ.get("AGENTNET_CONFIG_PATH", "/tmp/agentnet-config.json"))
    pgpass_path = Path(os.environ.get("AGENTNET_PGPASS_PATH", "/tmp/agentnet.pgpass"))
    password_path = Path(_required(os.environ, "AGENTNET_DATABASE_PASSWORD_FILE"))
    write_config(config, config_path)
    write_pgpass(os.environ, _read_password(password_path), pgpass_path)
    os.environ["PGPASSFILE"] = str(pgpass_path)

    if command == "serve":
        argv = serve_argv(os.environ, config_path)
    elif command == "status":
        argv = ["agentnet", "status", "--config", str(config_path)]
    elif command == "bootstrap":
        argv = ["agentnet", "bootstrap-server-agent", "--config", str(config_path)]
    else:
        raise DeploymentConfigError("AGENTNET_COMMAND must be bootstrap, serve, status, or validate")
    os.execvp(argv[0], argv)
    return 127


if __name__ == "__main__":
    raise SystemExit(main())
