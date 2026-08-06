"""Fixed product-owned setup for one ordinary Linux server agent.

This module deliberately owns one profile instead of exposing a deployment DSL.  It
composes the existing Approval, network/bootstrap, serve, status, guided-enrollment,
and activation surfaces while keeping host-specific writes bounded to AgentNet users,
private roots, environment files, and systemd units.
"""

from __future__ import annotations

import base64
import hashlib
import ipaddress
import json
import os
import re
import select
import shutil
import signal
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Literal, Mapping
from urllib.parse import urlsplit

if os.name == "posix":
    import grp
    import pwd

from pydantic import BaseModel, ConfigDict, Field, PrivateAttr, ValidationError, model_validator

from agentnet import __version__
from agentnet.artifacts.clamav import (
    ClamAVScanner,
    ScannerEndpoint,
    clamav_profile_digest,
    clamav_rules_digest,
)
from agentnet.artifacts.scanner import ScannerTrustPolicy
from agentnet.approval.internal_client import (
    ApprovalServiceClient,
    require_approval_tls_environment,
)
from agentnet.approval.config import (
    ApprovalOwnerOIDCConfig,
    ApprovalServiceConfig,
    MANDATORY_APPROVAL_PURPOSES,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.config import (
    ExtensionConfig,
    ApprovalServiceClientConfig,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    OIDCTokenEndpointAuthMethod,
    RuntimeProfile,
    ScannerTrustConfig,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.security.signatures import P256KeyPair, canonical_digest, verify_signature
from agentnet.storage.migrations import MIGRATIONS
from agentnet.storage.postgres import (
    MIGRATION_LOCK_ID,
    ORDINARY_SERVER_POSTGRES_DATABASE,
    ORDINARY_SERVER_POSTGRES_DSN,
    ORDINARY_SERVER_POSTGRES_SOCKET,
    ORDINARY_SERVER_POSTGRES_USER,
    apply_postgres_migrations,
    inspect_ordinary_server_postgres_auth,
    probe_ordinary_server_postgres_connection,
    validate_applied_migrations,
    validate_ordinary_server_postgres_dsn,
)
from agentnet.storage.postgres_catalog import require_exact_postgres_catalog


CORE_USER = "agentnet"
APPROVAL_USER = "agentnet-approval"
C0_RESPONDER_USER = "agentnet-c0"
CORE_UNIT = "agentnet-core.service"
APPROVAL_UNIT = "agentnet-approval.service"
C0_RESPONDER_UNIT = "agentnet-c0-responder.service"
CREDENTIAL_RENEW_UNIT = "agentnet-credential-renew.service"
CREDENTIAL_RENEW_TIMER = "agentnet-credential-renew.timer"
MANAGED_UNITS = (
    APPROVAL_UNIT,
    CORE_UNIT,
    C0_RESPONDER_UNIT,
    CREDENTIAL_RENEW_UNIT,
    CREDENTIAL_RENEW_TIMER,
)
LEGACY_COMMUNICATION_ONLY_UNITS = (
    APPROVAL_UNIT,
    CORE_UNIT,
)
CORE_DATA = Path("/var/lib/agentnet")
APPROVAL_DATA = Path("/var/lib/agentnet-approval")
C0_RESPONDER_DATA = Path("/var/lib/agentnet-c0")
C0_RESPONDER_CONFIG = C0_RESPONDER_DATA / "config.json"
C0_RESPONDER_TERMINAL = C0_RESPONDER_DATA / "terminal.json"
SERVER_AGENT_IDENTITY = CORE_DATA / "server-agent-identity.json"
SERVER_AGENT_KEY = CORE_DATA / "guided-join.key.pem"
CREDENTIAL_RENEW_STATE = CORE_DATA / "credential-renewal-state.json"
CORE_CONFIG = CORE_DATA / "agentnet.json"
CORE_OIDC_CONFIG = CORE_DATA / "oidc-enrollment.json"
SCANNER_SIGNING_KEY = CORE_DATA / "scanner-signing-key.pem"
SCANNER_WORKER_CONFIG = CORE_DATA / "scanner-worker.json"
APPROVAL_CONFIG = APPROVAL_DATA / "config.json"
APPROVAL_STATE = APPROVAL_DATA / "state"
SETUP_ROOT = Path("/var/lib/agentnet-setup")
SETUP_MARKER = SETUP_ROOT / "setup.json"
SETUP_ATTEMPT = SETUP_ROOT / "attempt.json"
SETUP_RUNTIME_ROOT = SETUP_ROOT / "npm-runtime"
SETUP_UPGRADE_JOURNAL = SETUP_ROOT / "upgrade.json"
SECRET_ROOT = Path("/etc/agentnet-secrets")
CORE_ENV = SECRET_ROOT / "core.env"
APPROVAL_ENV = SECRET_ROOT / "approval.env"
SYSTEMD_UNIT_ROOT = Path("/etc/systemd/system")
_SYSTEMD_UNIT_SEARCH_ROOTS = (
    Path("/etc/systemd/system.control"),
    Path("/run/systemd/system.control"),
    Path("/run/systemd/transient"),
    Path("/run/systemd/generator.early"),
    SYSTEMD_UNIT_ROOT,
    Path("/etc/systemd/system.attached"),
    Path("/run/systemd/system"),
    Path("/run/systemd/system.attached"),
    Path("/run/systemd/generator"),
    Path("/usr/local/lib/systemd/system"),
    Path("/usr/local/share/systemd/system"),
    Path("/usr/lib/systemd/system"),
    Path("/usr/share/systemd/system"),
    Path("/lib/systemd/system"),
    Path("/run/systemd/generator.late"),
)
CORE_PORT = 8080
APPROVAL_PORT = 8090
_ENV_NAME = re.compile(r"^[A-Z_][A-Z0-9_]{0,127}$")
_ENV_VALUE = re.compile(r"^[^\s'\"\\\x00-\x1f\x7f]+$")
_HEX64 = re.compile(r"^[a-f0-9]{64}$")
_BROKER_CREDENTIAL_NAME = "AGENTNET_APPROVAL_CORE_TOKEN"
_BROKER_CREDENTIAL_MIN_LENGTH = 43
_BROKER_CREDENTIAL_MAX_LENGTH = 512
_SYSTEMCTL_TIMEOUT_SECONDS = 30
_SYSTEM_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
# ProtectHome=true hides /home, /root, and /run/user; PrivateTmp=true replaces
# /tmp and /var/tmp.  A runtime executable under any of them is invisible to the
# managed units even though privileged setup can still read it.
_PROTECTED_SERVICE_PATHS = (
    Path("/home"),
    Path("/root"),
    Path("/run/user"),
    Path("/tmp"),
    Path("/var/tmp"),
)
# Exact supported setup-marker upgrade windows.  A package upgrade is the only
# reason an already realized deployment may present a different request digest.
# The 0.1.31 -> 0.1.33 window is deliberately narrower than the historical
# same-topology windows: only the released communication-only Core+Approval
# profile may expand to the current five-unit profile.  Corrective releases
# accept only the exact five-unit 0.1.33 target they repair.
_SUPPORTED_MARKER_UPGRADE_UNIT_PROFILES = {
    ("0.1.28", "0.1.31"): MANAGED_UNITS,
    ("0.1.30", "0.1.31"): MANAGED_UNITS,
    ("0.1.31", "0.1.33"): LEGACY_COMMUNICATION_ONLY_UNITS,
    ("0.1.32", "0.1.33"): MANAGED_UNITS,
    ("0.1.33", "0.1.34"): MANAGED_UNITS,
    ("0.1.33", "0.1.35"): MANAGED_UNITS,
    ("0.1.33", "0.1.37"): MANAGED_UNITS,
    ("0.1.37", "0.1.38"): MANAGED_UNITS,
    ("0.1.39", "0.1.40"): MANAGED_UNITS,
    ("0.1.40", "0.1.41"): MANAGED_UNITS,
    ("0.1.41", "0.1.42"): MANAGED_UNITS,
    ("0.1.44", "0.1.45"): MANAGED_UNITS,
    ("0.1.45", "0.1.46"): MANAGED_UNITS,
}
_FORWARD_ONLY_SETUP_UPGRADES = frozenset(
    {
        ("0.1.31", "0.1.33"),
        ("0.1.32", "0.1.33"),
        ("0.1.33", "0.1.34"),
        ("0.1.33", "0.1.35"),
        ("0.1.33", "0.1.37"),
        ("0.1.37", "0.1.38"),
        ("0.1.39", "0.1.40"),
        ("0.1.40", "0.1.41"),
        ("0.1.41", "0.1.42"),
        ("0.1.44", "0.1.45"),
        ("0.1.45", "0.1.46"),
    }
)
# The lifecycle release is the sole rollback-capable database upgrade.  Older
# setup edges retain their released forward-only recovery behavior.
_LIFECYCLE_SETUP_UPGRADE = ("0.1.44", "0.1.45")
_LIFECYCLE_SOURCE_SCHEMA = 6
_LIFECYCLE_TARGET_SCHEMA = 7
_LIFECYCLE_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v4"
_LIFECYCLE_RELEASE_TABLES = (
    "invitation_link_failures",
    "invitation_links",
    "artifact_transfer_recipients",
    "artifact_transfers",
    "collaboration_scope_members",
    "collaboration_scopes",
    "endpoint_lifecycle",
)
_LIFECYCLE_PRESERVED_TABLES = (
    "domains",
    "principals",
    "principal_aliases",
    "harnesses",
    "credentials",
    "entitlements",
    "events",
    "recipients",
    "communication_scopes",
)
# Blockers that mean "the response was lost", not "the operation was refused".
# Only these justify one bounded idempotent retry of a product command.
_RESPONSE_LOSS_BLOCKERS = frozenset({"invalid_product_evidence", "product_command_failed"})
_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v2"
_LEGACY_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v1"
_MAX_UNIT_BYTES = 65_536
_MAX_CONFIG_BYTES = 1_048_576
_SCANNER_ENVIRONMENT_KEYS = frozenset(
    {
        "AGENTNET_CLAMAV_ENDPOINT",
        "AGENTNET_CLAMAV_SCANNER_ID",
        "AGENTNET_CLAMAV_KEY_EPOCH",
        "AGENTNET_CLAMAV_SIGNING_KEY_FILE",
        "AGENTNET_CLAMAV_ENGINE_VERSION",
        "AGENTNET_CLAMAV_SIGNATURE_VERSION",
        "AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT",
        "AGENTNET_CLAMAV_SIGNATURE_MAX_AGE_SECONDS",
    }
)
_JOURNALED_CONFIG_KEYS = frozenset({"core_config", "core_oidc_config"})


class ServerSetupError(RuntimeError):
    """Fail-closed setup blocker safe to show in redacted operator evidence."""

    def __init__(
        self,
        blocker: str,
        message: str,
        *,
        identity_enrolled: bool = False,
    ) -> None:
        super().__init__(message)
        self.blocker = blocker
        self.identity_enrolled = identity_enrolled


class SetupOIDCProvider(BaseModel):
    """Provider fields only; Approval trust is generated by the product setup."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    issuer: str = Field(min_length=8, max_length=512)
    client_id: str = Field(min_length=1, max_length=512)
    redirect_uri: str = Field(min_length=8, max_length=2_048)
    audience: str | None = Field(default=None, min_length=1, max_length=512)
    token_endpoint_auth_method: OIDCTokenEndpointAuthMethod = OIDCTokenEndpointAuthMethod.NONE
    client_secret_env: str | None = Field(default=None, pattern=r"^[A-Z][A-Z0-9_]{2,127}$")
    allowed_endpoint_origins: tuple[str, ...] = Field(default=(), max_length=32)
    allowed_private_endpoint_cidrs: tuple[str, ...] = Field(default=(), max_length=64)
    pinned_endpoint_addresses: tuple[str, ...] = Field(default=(), max_length=128)
    allowed_signing_algorithms: tuple[Literal["RS256", "ES256"], ...] = ("RS256",)
    pinned_jwk_thumbprints: dict[str, str] = Field(default_factory=dict)
    binding_assurance: Literal["os_bound", "hardware_bound"] = "hardware_bound"

    @model_validator(mode="after")
    def provider_contract(self) -> "SetupOIDCProvider":
        confidential = self.token_endpoint_auth_method is not OIDCTokenEndpointAuthMethod.NONE
        if confidential != (self.client_secret_env is not None):
            raise ValueError("OIDC client-secret reference policy is inconsistent")
        try:
            issuer = urlsplit(self.issuer)
            issuer_port = issuer.port
        except ValueError as exc:
            raise ValueError("OIDC issuer is invalid") from exc
        if (
            issuer.scheme != "https"
            or not issuer.hostname
            or issuer.username is not None
            or issuer.password is not None
            or issuer.query
            or issuer.fragment
            or self.issuer.endswith("/")
            or issuer_port not in {None, 443}
        ):
            raise ValueError("OIDC issuer must be one canonical HTTPS issuer")
        return self


class SetupApprover(BaseModel):
    """Non-secret approver policy applied exactly during Approval provisioning."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    principal_id: str = Field(min_length=1, max_length=256)
    authority_kind: Literal["human", "guest"] = "human"
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    allowed_purposes: frozenset[str] = Field(min_length=1, max_length=32)
    oidc_issuer: str | None = Field(default=None, min_length=8, max_length=512)
    oidc_subject: str | None = Field(default=None, min_length=1, max_length=512)
    verified_email_alias: str | None = Field(default=None, min_length=3, max_length=320)

    @model_validator(mode="after")
    def exact_identity(self) -> "SetupApprover":
        identity_values = (self.oidc_issuer, self.oidc_subject, self.verified_email_alias)
        if any(value is not None for value in identity_values) and (
            self.oidc_issuer is None
            or (self.oidc_subject is None) == (self.verified_email_alias is None)
        ):
            raise ValueError("approver requires one exact OIDC subject or verified email alias")
        if self.verified_email_alias is not None:
            normalized = self.verified_email_alias.strip().casefold()
            try:
                normalized.encode("ascii")
            except UnicodeEncodeError as exc:
                raise ValueError("approver verified email alias must be normalized") from exc
            local, separator, domain = normalized.partition("@")
            if (
                separator != "@"
                or not local
                or not domain
                or "@" in domain
                or normalized != self.verified_email_alias
                or any(ord(character) <= 0x20 or ord(character) == 0x7F for character in normalized)
            ):
                raise ValueError("approver verified email alias must be normalized")
        if any(
            not purpose
            or len(purpose) > 256
            or purpose != purpose.strip()
            or any(ord(character) < 0x21 for character in purpose)
            for purpose in self.allowed_purposes
        ):
            raise ValueError("approver purpose is invalid")
        return self


class ServerSetupRequest(BaseModel):
    """Strict non-secret references for the fixed ordinary server-agent profile."""

    model_config = ConfigDict(extra="forbid", frozen=True, populate_by_name=True)
    _source_sha256: str = PrivateAttr(default="")

    schema_version: Literal[
        "agentnet.server-setup.request.v1",
        "agentnet.server-setup.request.v2",
    ] = Field(alias="schema")
    profile: Literal["always_on_server_agent"] = "always_on_server_agent"
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    service_audience: str = Field(min_length=8, max_length=512)
    runtime_instance_id: str = Field(min_length=1, max_length=128)
    core_public_origin: str = Field(min_length=8, max_length=2048)
    approval_public_origin: str = Field(min_length=8, max_length=2048)
    database_url: str = Field(min_length=16, max_length=2048)
    database_url_env: str = Field(default="AGENTNET_DATABASE_URL", pattern=r"^AGENTNET_[A-Z0-9_]{1,118}$")
    core_environment_file: Path
    approval_environment_file: Path
    oidc_provider_file: Path
    approval_owner_oidc_file: Path
    approval_approvers_file: Path
    artifact_mode: Literal["enabled", "disabled"] | None = None
    scanner_trust_file: Path | None = None
    approval_approver_principal_id: str = Field(min_length=1, max_length=256)
    approval_verifier_id: str = Field(min_length=1, max_length=128)

    @staticmethod
    def _origin(value: str, *, label: str) -> str:
        try:
            parsed = urlsplit(value)
            port = parsed.port
        except ValueError as exc:
            raise ValueError(f"{label} is invalid") from exc
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError(f"{label} must be one exact HTTPS origin")
        hostname = parsed.hostname.lower()
        if hostname == "localhost" or hostname.endswith((".localhost", ".local")):
            raise ValueError(f"{label} must not use a local hostname")
        try:
            literal_address = ipaddress.ip_address(hostname)
        except ValueError:
            literal_address = None
        if literal_address is not None and not literal_address.is_global:
            raise ValueError(f"{label} must not use a non-public literal address")
        host = f"[{hostname}]" if ":" in hostname else hostname
        canonical = f"https://{host}"
        if port not in {None, 443}:
            canonical += f":{port}"
        if value.rstrip("/") != canonical:
            raise ValueError(f"{label} must use canonical spelling")
        return canonical

    @model_validator(mode="after")
    def fixed_profile(self) -> "ServerSetupRequest":
        self._origin(self.core_public_origin, label="Core public origin")
        self._origin(self.approval_public_origin, label="Approval public origin")
        expected_audience = f"urn:agentnet:{self.domain_id}:corporate-api"
        if self.service_audience != expected_audience:
            raise ValueError("service_audience must be the canonical domain audience")
        try:
            validate_ordinary_server_postgres_dsn(self.database_url)
        except Exception as exc:
            raise ValueError(
                "database_url must use the fixed local PostgreSQL peer-auth contract"
            ) from exc
        fields_set = self.model_fields_set
        if self.schema_version == "agentnet.server-setup.request.v1":
            if "artifact_mode" in fields_set or self.scanner_trust_file is None:
                raise ValueError("request.v1 requires the original scanner-backed artifact profile")
        elif self.artifact_mode == "enabled":
            if "artifact_mode" not in fields_set or self.scanner_trust_file is None:
                raise ValueError("request.v2 artifact mode enabled requires scanner_trust_file")
        elif self.artifact_mode == "disabled":
            if "artifact_mode" not in fields_set or "scanner_trust_file" in fields_set:
                raise ValueError("request.v2 artifact mode disabled forbids scanner_trust_file")
        else:
            raise ValueError("request.v2 requires explicit artifact_mode")
        paths = [
            self.core_environment_file,
            self.approval_environment_file,
            self.oidc_provider_file,
            self.approval_owner_oidc_file,
            self.approval_approvers_file,
        ]
        if self.scanner_trust_file is not None:
            paths.append(self.scanner_trust_file)
        for path in paths:
            if not path.is_absolute() or ".." in path.parts:
                raise ValueError("server setup input files must use absolute canonical paths")
        if self.core_public_origin == self.approval_public_origin:
            raise ValueError("Core and Approval require distinct public HTTPS origins")
        return self

    @property
    def effective_artifact_mode(self) -> Literal["enabled", "disabled"]:
        return "enabled" if self.schema_version == "agentnet.server-setup.request.v1" else self.artifact_mode  # type: ignore[return-value]


@dataclass(frozen=True)
class SetupLayout:
    root: Path = Path("/")

    def host(self, path: Path) -> Path:
        if self.root == Path("/"):
            return path
        return self.root / path.relative_to("/")

    @property
    def lock(self) -> Path:
        return self.host(SETUP_ROOT / "setup.lock")

    def unit(self, name: str) -> Path:
        return self.host(SYSTEMD_UNIT_ROOT / name)

    @property
    def core_unit(self) -> Path:
        return self.unit(CORE_UNIT)

    @property
    def approval_unit(self) -> Path:
        return self.unit(APPROVAL_UNIT)


@dataclass(frozen=True)
class SetupRuntimeIdentity:
    node_executable: Path
    node_sha256: str
    uv_executable: Path
    uv_sha256: str
    agentnet_executable: Path
    agentnet_sha256: str
    package_root: Path
    package_tree_sha256: str
    systemctl_executable: Path
    systemctl_sha256: str
    useradd_executable: Path
    useradd_sha256: str

@dataclass(frozen=True)
class ScannerSetupSpec:
    endpoint: ScannerEndpoint
    key: P256KeyPair
    key_input: bytes
    scanner_id: str
    scanner_key_epoch: int
    engine_version: str
    signature_version: str
    signature_updated_at: int
    signature_max_age_seconds: int
    rules_digest: str
    profile_digest: str
    trust_policy: ScannerTrustPolicy



@dataclass(frozen=True)
class ServerSetupPreflight:
    runtime: SetupRuntimeIdentity
    input_bundle: dict[str, bytes]
    oidc_provider: SetupOIDCProvider
    owner_oidc: ApprovalOwnerOIDCConfig
    approvers: tuple[SetupApprover, ...]
    scanner_trust: ScannerTrustConfig | None
    scanner_setup: ScannerSetupSpec | None
    core_values: dict[str, str]
    approval_values: dict[str, str]
    core_environment: dict[str, str]
    approval_environment: dict[str, str]
    request_digest: str
    legacy_request_digest: str


def _strict_json_bytes(raw: bytes, *, label: str) -> dict[str, Any]:
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_reject_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ServerSetupError("invalid_input", f"{label} is invalid") from exc
    if not isinstance(value, dict):
        raise ServerSetupError("invalid_input", f"{label} must be one JSON object")
    return value


def _reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _allowed_input_owners() -> set[int]:
    owners = {os.geteuid()}
    sudo_uid = os.environ.get("SUDO_UID")
    if sudo_uid and sudo_uid.isdecimal():
        owners.add(int(sudo_uid))
    return owners


def _read_bounded_snapshot(
    descriptor: int,
    expected_size: int,
    *,
    blocker: str,
    message: str,
) -> bytes:
    try:
        os.lseek(descriptor, 0, os.SEEK_SET)
        chunks: list[bytes] = []
        remaining = expected_size + 1
        while remaining:
            chunk = os.read(descriptor, min(remaining, 262_144))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)
    except OSError as exc:
        raise ServerSetupError(blocker, message) from exc


def _read_private_input(path: Path, *, label: str, max_bytes: int = 1_048_576) -> bytes:
    try:
        canonical = path.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError("missing_input", f"{label} is unavailable") from exc
    if canonical != path or path.is_symlink() or path.parent.is_symlink():
        raise ServerSetupError("unsafe_input", f"{label} must be a canonical non-symlink file")
    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
        )
    except OSError as exc:
        raise ServerSetupError("missing_input", f"{label} is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid not in _allowed_input_owners()
            or before.st_nlink != 1
            or before.st_mode & 0o077
            or not 1 <= before.st_size <= max_bytes
        ):
            raise ServerSetupError("unsafe_input", f"{label} must be one bounded owner-only file")
        changed_message = f"{label} changed while being read"
        first = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker="unsafe_input",
            message=changed_message,
        )
        middle = os.fstat(descriptor)
        second = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker="unsafe_input",
            message=changed_message,
        )
        after = os.fstat(descriptor)
        if (
            len(first) != before.st_size
            or first != second
            or any(
                getattr(snapshot, field) != getattr(before, field)
                for snapshot in (middle, after)
                for field in (
                    "st_dev",
                    "st_ino",
                    "st_size",
                    "st_mtime_ns",
                    "st_ctime_ns",
                )
            )
        ):
            raise ServerSetupError("unsafe_input", changed_message)
        return first
    finally:
        os.close(descriptor)


def load_server_setup_request(path: Path) -> ServerSetupRequest:
    try:
        canonical = path.absolute()
        raw = _read_private_input(canonical, label="setup request")
        request = ServerSetupRequest.model_validate(
            _strict_json_bytes(raw, label="setup request")
        )
        object.__setattr__(request, "_source_sha256", hashlib.sha256(raw).hexdigest())
        return request
    except ServerSetupError:
        raise
    except Exception as exc:
        raise ServerSetupError("invalid_request", "setup request is invalid") from exc


def _parse_environment(raw: bytes, *, label: str) -> dict[str, str]:
    result: dict[str, str] = {}
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ServerSetupError("invalid_environment", f"{label} is not UTF-8") from exc
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped != line or "=" not in line:
            raise ServerSetupError("invalid_environment", f"{label} line {number} is invalid")
        name, value = line.split("=", 1)
        if (
            not _ENV_NAME.fullmatch(name)
            or name in result
            or not _ENV_VALUE.fullmatch(value)
        ):
            raise ServerSetupError("invalid_environment", f"{label} line {number} is invalid")
        result[name] = value
    return result


def _parse_environment_file(path: Path, *, label: str) -> dict[str, str]:
    return _parse_environment(
        _read_private_input(path, label=label, max_bytes=262_144),
        label=label,
    )


_BASE_INPUT_FIELDS = (
    "core_environment_file",
    "approval_environment_file",
    "oidc_provider_file",
    "approval_owner_oidc_file",
    "approval_approvers_file",
)


def _input_fields(request: ServerSetupRequest) -> tuple[str, ...]:
    return (
        *_BASE_INPUT_FIELDS,
        *(("scanner_trust_file",) if request.effective_artifact_mode == "enabled" else ()),
    )


def _read_input_bundle(request: ServerSetupRequest) -> dict[str, bytes]:
    public = request.model_dump(mode="json", by_alias=True)
    return {
        key: _read_private_input(
            Path(str(public[key])),
            label=f"{key} input",
            max_bytes=262_144 if key in {"core_environment_file", "approval_environment_file"} else 1_048_576,
        )
        for key in _input_fields(request)
    }


def _request_references(
    request: ServerSetupRequest,
    inputs: Mapping[str, bytes],
) -> dict[str, dict[str, str]]:
    if not re.fullmatch(r"[a-f0-9]{64}", request._source_sha256):
        raise ServerSetupError("invalid_request", "setup request source binding is unavailable")
    public = request.model_dump(mode="json", by_alias=True)
    references: dict[str, dict[str, str]] = {}
    for key in _input_fields(request):
        path = Path(str(public[key]))
        raw = inputs[key]
        if key in {"core_environment_file", "approval_environment_file"}:
            names = sorted(_parse_environment(raw, label=f"{key} digest input"))
            fingerprint = canonical_digest({"environment_names": names})
        else:
            fingerprint = hashlib.sha256(raw).hexdigest()
        references[key] = {"path": str(path), "fingerprint": fingerprint}
    return references


def _legacy_request_digest(
    request: ServerSetupRequest,
    bundle: Mapping[str, bytes],
) -> str:
    return canonical_digest(
        {
            "schema": "agentnet.server-setup.approval-digest.v1",
            "request_file_sha256": request._source_sha256,
            "referenced_inputs": _request_references(request, bundle),
        }
    )


def _request_digest(
    request: ServerSetupRequest,
    bundle: Mapping[str, bytes] | None = None,
    *,
    runtime: SetupRuntimeIdentity,
) -> str:
    inputs = dict(bundle) if bundle is not None else _read_input_bundle(request)
    digest_schema = (
        "agentnet.server-setup.approval-digest.v2"
        if request.schema_version == "agentnet.server-setup.request.v1"
        else "agentnet.server-setup.approval-digest.v3"
    )
    return canonical_digest(
        {
            "schema": digest_schema,
            "request_file_sha256": request._source_sha256,
            "referenced_inputs": _request_references(request, inputs),
            "runtime_identity": {
                "agentnet_executable": str(runtime.agentnet_executable),
                "agentnet_sha256": runtime.agentnet_sha256,
                "package_root": str(runtime.package_root),
                "package_tree_sha256": runtime.package_tree_sha256,
                "node_executable": str(runtime.node_executable),
                "node_sha256": runtime.node_sha256,
                "systemctl_executable": str(runtime.systemctl_executable),
                "systemctl_sha256": runtime.systemctl_sha256,
                "useradd_executable": str(runtime.useradd_executable),
                "useradd_sha256": runtime.useradd_sha256,
                "uv_executable": str(runtime.uv_executable),
                "uv_sha256": runtime.uv_sha256,
            },
        }
    )


def _require_service_visible_path(value: Path, *, label: str) -> None:
    if any(value == root or root in value.parents for root in _PROTECTED_SERVICE_PATHS):
        raise ServerSetupError(
            "service_executable_inaccessible",
            f"installed {label} executable is hidden by the managed service sandbox",
        )


def _require_root_owned_executable(value: Path, *, label: str) -> Path:
    try:
        resolved = value.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError("missing_executable", f"installed {label} executable is unavailable") from exc
    if not resolved.is_file() or not os.access(resolved, os.X_OK):
        raise ServerSetupError("unsafe_executable", f"installed {label} executable is not executable")
    _require_service_visible_path(resolved, label=label)
    for item in (resolved, *resolved.parents):
        if item == Path("/"):
            break
        metadata = item.stat()
        if metadata.st_uid != 0:
            raise ServerSetupError(
                "unsafe_executable",
                f"installed {label} executable ownership is unsafe for server setup",
            )
        writable_by_others = bool(metadata.st_mode & 0o022)
        if writable_by_others:
            raise ServerSetupError(
                "unsafe_executable",
                f"installed {label} executable path is writable by another identity",
            )
        required = stat.S_IXOTH if item.is_dir() else stat.S_IROTH | stat.S_IXOTH
        if metadata.st_mode & required != required:
            raise ServerSetupError(
                "service_executable_inaccessible",
                f"installed {label} executable is not accessible to dedicated service identities",
            )
    return resolved


def _require_root_owned_tree(root: Path) -> Path:
    resolved = _require_root_owned_executable(root / "npm" / "bin" / "agentnet.mjs", label="agentnet")
    package_root = resolved.parents[2]
    if package_root != root.resolve(strict=True):
        raise ServerSetupError("unsafe_executable", "installed AgentNet package root is inconsistent")
    def walk_error(exc: OSError) -> None:
        raise ServerSetupError("unsafe_executable", "installed AgentNet package tree is not inspectable") from exc

    for directory, names, files in os.walk(
        package_root,
        followlinks=False,
        onerror=walk_error,
    ):
        directory_path = Path(directory)
        metadata = directory_path.lstat()
        if metadata.st_uid != 0 or metadata.st_mode & 0o022 or metadata.st_mode & 0o005 != 0o005:
            raise ServerSetupError("unsafe_executable", "installed AgentNet package tree custody is unsafe")
        for name in (*names, *files):
            item = directory_path / name
            item_metadata = item.lstat()
            required = 0o005 if stat.S_ISDIR(item_metadata.st_mode) else 0o004
            if (
                item.is_symlink()
                or item_metadata.st_uid != 0
                or item_metadata.st_mode & 0o022
                or item_metadata.st_mode & required != required
            ):
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree custody is unsafe")
    return resolved


def _resolve_host_tool(name: str) -> Path:
    located = shutil.which(name, path=_SYSTEM_PATH)
    if located is None:
        raise ServerSetupError("missing_host_tool", f"ordinary server setup requires {name}")
    return _require_root_owned_executable(Path(located), label=name)


def _package_owned_executable(variable: str, *, label: str) -> Path:
    """Select one runtime executable from the installed package binding only.

    Ambient ``PATH`` lookup is deliberately absent: the managed units execute the
    exact absolute paths recorded here, so a runtime chosen from the invoking
    shell's environment could differ from the launcher's own runtime and could be
    invisible inside the unit sandbox.
    """

    configured = os.environ.get(variable)
    if not configured:
        raise ServerSetupError(
            "missing_package_provenance",
            f"ordinary server setup requires the installed package {label} binding",
        )
    candidate = Path(configured)
    if (
        not candidate.is_absolute()
        or ".." in candidate.parts
        or configured != os.path.normpath(configured)
    ):
        raise ServerSetupError(
            "unsafe_executable",
            f"configured {label} executable must be one absolute canonical path",
        )
    resolved = _require_root_owned_executable(candidate, label=label)
    if resolved != candidate:
        raise ServerSetupError(
            "unsafe_executable",
            f"configured {label} executable must not resolve through a symbolic link",
        )
    return resolved


def _resolve_uv_executable() -> Path:
    return _package_owned_executable("AGENTNET_UV", label="uv")


def _resolve_node_executable() -> Path:
    return _package_owned_executable("AGENTNET_NODE_EXECUTABLE", label="Node.js")


def _sha256_stable_file(path: Path, *, label: str) -> str:
    try:
        descriptor = os.open(
            path,
            os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0),
        )
    except OSError as exc:
        raise ServerSetupError("unsafe_executable", f"installed {label} executable is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or before.st_nlink < 1 or before.st_size < 1:
            raise ServerSetupError("unsafe_executable", f"installed {label} executable custody is unsafe")
        digest = hashlib.sha256()
        while True:
            chunk = os.read(descriptor, 1_048_576)
            if not chunk:
                break
            digest.update(chunk)
        after = os.fstat(descriptor)
        if any(
            getattr(after, field) != getattr(before, field)
            for field in (
                "st_dev",
                "st_ino",
                "st_mode",
                "st_uid",
                "st_gid",
                "st_nlink",
                "st_size",
                "st_mtime_ns",
                "st_ctime_ns",
            )
        ):
            raise ServerSetupError("unsafe_executable", f"installed {label} executable changed during preflight")
        return digest.hexdigest()
    finally:
        os.close(descriptor)


def _sha256_stable_tree(root: Path) -> str:
    maximum_records = 20_000
    maximum_bytes = 536_870_912
    records: list[dict[str, object]] = [{"path": ".", "type": "directory"}]
    total_bytes = 0

    def unchanged(before: os.stat_result, after: os.stat_result) -> bool:
        return all(
            getattr(before, field) == getattr(after, field)
            for field in (
                "st_dev",
                "st_ino",
                "st_mode",
                "st_uid",
                "st_gid",
                "st_nlink",
                "st_size",
                "st_mtime_ns",
                "st_ctime_ns",
            )
        )

    def stable_file(path: Path, relative: str) -> None:
        nonlocal total_bytes
        try:
            descriptor = os.open(
                path,
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0),
            )
        except OSError as exc:
            raise ServerSetupError("unsafe_executable", "installed AgentNet package tree is not stable") from exc
        try:
            before = os.fstat(descriptor)
            if not stat.S_ISREG(before.st_mode) or before.st_size < 0:
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree contains an unsupported entry")
            total_bytes += before.st_size
            if total_bytes > maximum_bytes:
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree exceeds the fixed evidence bound")
            digest = hashlib.sha256()
            while True:
                chunk = os.read(descriptor, 1_048_576)
                if not chunk:
                    break
                digest.update(chunk)
            after = os.fstat(descriptor)
            if not unchanged(before, after):
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree changed during preflight")
            records.append(
                {
                    "path": relative,
                    "sha256": digest.hexdigest(),
                    "size": before.st_size,
                    "type": "file",
                }
            )
        finally:
            os.close(descriptor)

    def visit(directory: Path) -> None:
        try:
            before = directory.lstat()
            if not stat.S_ISDIR(before.st_mode) or directory.is_symlink():
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree contains an unsupported entry")
            entries = sorted(os.scandir(directory), key=lambda entry: entry.name.encode("utf-8"))
            for entry in entries:
                path = directory / entry.name
                relative = path.relative_to(root).as_posix()
                metadata = path.lstat()
                if stat.S_ISLNK(metadata.st_mode):
                    raise ServerSetupError("unsafe_executable", "installed AgentNet package tree contains a symbolic link")
                if stat.S_ISDIR(metadata.st_mode):
                    records.append({"path": relative, "type": "directory"})
                    visit(path)
                elif stat.S_ISREG(metadata.st_mode):
                    stable_file(path, relative)
                else:
                    raise ServerSetupError("unsafe_executable", "installed AgentNet package tree contains an unsupported entry")
                if len(records) > maximum_records:
                    raise ServerSetupError("unsafe_executable", "installed AgentNet package tree exceeds the fixed evidence bound")
            after = directory.lstat()
            if not unchanged(before, after):
                raise ServerSetupError("unsafe_executable", "installed AgentNet package tree changed during preflight")
        except ServerSetupError:
            raise
        except OSError as exc:
            raise ServerSetupError("unsafe_executable", "installed AgentNet package tree is not inspectable") from exc

    visit(root)
    return canonical_digest(
        {
            "records": records,
            "schema": "agentnet.package-tree-content.v1",
        }
    )


def _resolve_setup_runtime() -> SetupRuntimeIdentity:
    node_executable = _resolve_node_executable()
    uv_executable = _resolve_uv_executable()
    agentnet_executable = _resolve_executable(node_executable, uv_executable)
    package_root = agentnet_executable.parents[2]
    systemctl_executable = _resolve_host_tool("systemctl")
    useradd_executable = _resolve_host_tool("useradd")
    return SetupRuntimeIdentity(
        node_executable=node_executable,
        node_sha256=_sha256_stable_file(node_executable, label="Node.js"),
        uv_executable=uv_executable,
        uv_sha256=_sha256_stable_file(uv_executable, label="uv"),
        agentnet_executable=agentnet_executable,
        agentnet_sha256=_sha256_stable_file(agentnet_executable, label="agentnet"),
        package_root=package_root,
        package_tree_sha256=_sha256_stable_tree(package_root),
        systemctl_executable=systemctl_executable,
        systemctl_sha256=_sha256_stable_file(systemctl_executable, label="systemctl"),
        useradd_executable=useradd_executable,
        useradd_sha256=_sha256_stable_file(useradd_executable, label="useradd"),
    )


def _resolve_executable(node_executable: Path, uv_executable: Path) -> Path:
    package_root_value = os.environ.get("AGENTNET_PACKAGE_ROOT")
    if not package_root_value:
        raise ServerSetupError(
            "missing_package_provenance",
            "ordinary server setup requires the installed public AgentNet package launcher",
        )
    package_root = Path(package_root_value)
    if not package_root.is_absolute() or ".." in package_root.parts:
        raise ServerSetupError("unsafe_executable", "installed AgentNet package root is invalid")
    resolved = _require_root_owned_tree(package_root)
    runtime_root_value = os.environ.get("AGENTNET_NPM_RUNTIME_DIR")
    if not runtime_root_value:
        raise ServerSetupError(
            "missing_package_provenance",
            "ordinary server setup requires the installed package runtime binding",
        )
    runtime_root = Path(runtime_root_value)
    if not runtime_root.is_absolute() or ".." in runtime_root.parts:
        raise ServerSetupError("unsafe_executable", "installed package runtime binding is invalid")
    completed = subprocess.run(
        [str(node_executable), str(resolved), "--version"],
        env={
            "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "HOME": pwd.getpwuid(os.geteuid()).pw_dir,
            "LANG": "C.UTF-8",
            "AGENTNET_PACKAGE_ROOT": str(package_root),
            "AGENTNET_NODE_EXECUTABLE": str(node_executable),
            "AGENTNET_UV": str(uv_executable),
            "AGENTNET_NPM_RUNTIME_DIR": str(runtime_root),
            "PYTHONDONTWRITEBYTECODE": "1",
            "UV_NO_MODIFY_PATH": "1",
        },
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        timeout=30,
        check=False,
    )
    if completed.returncode != 0 or completed.stdout.strip() != f"agentnet {__version__}":
        raise ServerSetupError("package_version", "installed agentnet executable version does not match package")
    return resolved


def _validate_inputs(
    request: ServerSetupRequest,
    bundle: Mapping[str, bytes] | None = None,
) -> tuple[
    SetupOIDCProvider,
    ApprovalOwnerOIDCConfig,
    tuple[SetupApprover, ...],
    ScannerTrustConfig | None,
]:
    inputs = dict(bundle) if bundle is not None else _read_input_bundle(request)
    try:
        oidc = SetupOIDCProvider.model_validate(
            _strict_json_bytes(inputs["oidc_provider_file"], label="Core OIDC provider input")
        )
        owner_oidc = ApprovalOwnerOIDCConfig.model_validate(
            _strict_json_bytes(inputs["approval_owner_oidc_file"], label="Approval owner OIDC input")
        )
    except ServerSetupError:
        raise
    except Exception as exc:
        raise ServerSetupError("invalid_oidc_input", "OIDC setup input is invalid") from exc
    scanner_trust: ScannerTrustConfig | None = None
    if request.effective_artifact_mode == "enabled":
        try:
            scanner_trust = ScannerTrustConfig.model_validate(
                _strict_json_bytes(inputs["scanner_trust_file"], label="scanner trust input")
            )
        except ServerSetupError:
            raise
        except Exception as exc:
            raise ServerSetupError("invalid_scanner_trust", "scanner trust input is invalid") from exc
    approver_value = _strict_json_bytes(inputs["approval_approvers_file"], label="Approval approver input")
    entries = approver_value.get("approvers")
    if set(approver_value) != {"approvers"} or not isinstance(entries, list) or not entries:
        raise ServerSetupError("invalid_approvers", "Approval approver input is invalid")
    try:
        approvers = tuple(SetupApprover.model_validate(item) for item in entries)
    except Exception as exc:
        raise ServerSetupError("invalid_approvers", "Approval approver input is invalid") from exc
    if len({item.principal_id for item in approvers}) != len(approvers):
        raise ServerSetupError("invalid_approvers", "Approval approver principals must be unique")
    configured_issuers = {item.oidc_issuer for item in approvers if item.oidc_issuer is not None}
    if configured_issuers != {owner_oidc.issuer}:
        raise ServerSetupError("invalid_approvers", "Approval approvers do not match owner OIDC issuer")
    secret_references = {
        value
        for value in (oidc.client_secret_env, owner_oidc.client_secret_env)
        if value is not None
    }
    reserved_references = {
        "AGENTNET_NPM_RUNTIME_DIR",
        "AGENTNET_PACKAGE_ROOT",
        "AGENTNET_UV",
        "AGENTNET_NODE_EXECUTABLE",
    }
    if any(not value.startswith("AGENTNET_") for value in secret_references) or (
        secret_references & reserved_references
    ):
        raise ServerSetupError("invalid_secret_reference", "OIDC secret environment reference is unsafe")
    broker_reference = "AGENTNET_APPROVAL_CORE_TOKEN"
    credential_references = [
        request.database_url_env,
        broker_reference,
        *(value for value in (oidc.client_secret_env, owner_oidc.client_secret_env) if value is not None),
    ]
    if len(credential_references) != len(set(credential_references)):
        raise ServerSetupError(
            "invalid_secret_reference",
            "database, broker, and OIDC credentials require distinct environment references",
        )
    selected = [item for item in approvers if item.principal_id == request.approval_approver_principal_id]
    if len(selected) != 1:
        raise ServerSetupError("invalid_approvers", "selected Approval approver is unavailable")
    if not MANDATORY_APPROVAL_PURPOSES <= selected[0].allowed_purposes:
        raise ServerSetupError("invalid_approvers", "selected Approval approver lacks mandatory purposes")
    approval_host = urlsplit(request.approval_public_origin).hostname
    if oidc.redirect_uri != f"{request.core_public_origin}/v1/enrollment/oidc/callback":
        raise ServerSetupError("core_callback", "Core OIDC callback does not match public origin")
    if owner_oidc.redirect_uri != f"{request.approval_public_origin}/v1/approval/owner/oidc/callback":
        raise ServerSetupError("approval_callback", "Approval owner OIDC callback does not match public origin")
    if approval_host is None:
        raise ServerSetupError("approval_origin", "Approval public origin is invalid")
    probe_signer = P256KeyPair.generate()
    try:
        OIDCEnrollmentConfig(
            **oidc.model_dump(mode="python"),
            verifier_id=request.approval_verifier_id,
            trusted_approvers=(
                IndependentApproverConfig(
                    principal_id=selected[0].principal_id,
                    authority_kind=selected[0].authority_kind,
                    signer_key_id=probe_signer.thumbprint,
                    public_key_pem=probe_signer.public_pem,
                    allowed_purposes=selected[0].allowed_purposes,
                ),
            ),
            approval_service=ApprovalServiceClientConfig(
                origin=request.approval_public_origin,
                public_origin=request.approval_public_origin,
                service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
                approver_principal_id=request.approval_approver_principal_id,
                remote_activation_oidc_subject=selected[0].oidc_subject,
                remote_activation_verified_email_alias=selected[0].verified_email_alias,
            ),
        )
    except Exception as exc:
        raise ServerSetupError("invalid_oidc_input", "Core OIDC provider input is invalid") from exc
    return oidc, owner_oidc, approvers, scanner_trust


def _validate_account(account: pwd.struct_passwd, name: str, home: Path) -> None:
    shells = {"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"}
    try:
        primary_group = grp.getgrgid(account.pw_gid)
        effective_groups = set(os.getgrouplist(name, account.pw_gid))
        primary_peers = {
            candidate.pw_name
            for candidate in pwd.getpwall()
            if candidate.pw_gid == account.pw_gid
        }
    except (KeyError, OSError) as exc:
        raise ServerSetupError("identity_conflict", f"existing {name} account conflicts with fixed profile") from exc
    if (
        account.pw_uid == 0
        or account.pw_gid == 0
        or Path(account.pw_dir) != home
        or account.pw_shell not in shells
        or primary_group.gr_name != name
        or effective_groups != {account.pw_gid}
        or primary_peers != {name}
        or not set(primary_group.gr_mem) <= {name}
    ):
        raise ServerSetupError("identity_conflict", f"existing {name} account conflicts with fixed profile")


def _account_fact(name: str, home: Path) -> str:
    try:
        account = pwd.getpwnam(name)
    except KeyError:
        try:
            grp.getgrnam(name)
        except KeyError:
            return "create"
        raise ServerSetupError(
            "identity_conflict",
            f"existing {name} group conflicts with fixed profile",
        )
    _validate_account(account, name, home)
    return "already_satisfied"


def _require_no_unit_overrides(layout: SetupLayout, unit: str) -> None:
    override_paths = (
        Path(f"/etc/systemd/system/{unit}.d"),
        Path(f"/run/systemd/system/{unit}.d"),
        Path(f"/etc/systemd/system.control/{unit}.d"),
        Path(f"/run/systemd/system.control/{unit}.d"),
        Path(f"/usr/local/lib/systemd/system/{unit}.d"),
        Path(f"/usr/lib/systemd/system/{unit}.d"),
        Path(f"/lib/systemd/system/{unit}.d"),
    )
    if any(layout.host(path).exists() or layout.host(path).is_symlink() for path in override_paths):
        raise ServerSetupError("unit_override_conflict", "managed AgentNet unit has unsupported overrides")


def _require_absent_topology_upgrade_unit(
    layout: SetupLayout,
    systemctl_executable: Path,
    unit: str,
    *,
    journaled: bool,
) -> None:
    """Prove one target-only unit is absent/inert before topology expansion."""

    for root in _SYSTEMD_UNIT_SEARCH_ROOTS:
        candidate = layout.host(root / unit)
        present = candidate.exists() or candidate.is_symlink()
        if present and (not journaled or candidate != layout.unit(unit)):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "target-only managed unit exists before topology upgrade",
            )
    properties = _systemd_show(systemctl_executable, unit)
    service_pid_invalid = (
        unit.endswith(".service")
        and properties.get("MainPID") not in {"", "0"}
    )
    load_state = properties.get("LoadState")
    unit_file_state = properties.get("UnitFileState")
    fragment_path = properties.get("FragmentPath", "")
    absent = (
        load_state == "not-found"
        and unit_file_state in {"", "disabled"}
        and not fragment_path
    )
    inert_journaled = (
        journaled
        and load_state == "loaded"
        and unit_file_state in {"disabled", "static"}
        and fragment_path == str(layout.unit(unit))
    )
    if (
        not (absent or inert_journaled)
        or properties.get("ActiveState") != "inactive"
        or properties.get("DropInPaths", "").strip()
        or service_pid_invalid
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "target-only managed unit is loaded, enabled, or active before topology upgrade",
        )


def _unit_arg(value: str) -> str:
    if any(character in value for character in "\n\r\x00"):
        raise ServerSetupError("unit_input", "systemd unit input contains a forbidden character")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def render_units(
    node_executable: Path,
    executable: Path,
    uv_executable: Path,
) -> dict[str, bytes]:
    common = "\n".join(
        (
            "Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "UnsetEnvironment=NODE_OPTIONS NODE_PATH PYTHONPATH PYTHONHOME PYTHONSTARTUP UV_CONFIG_FILE UV_PROJECT UV_WORKING_DIR VIRTUAL_ENV SSL_CERT_FILE SSL_CERT_DIR SSLKEYLOGFILE",
            "NoNewPrivileges=true",
            "PrivateTmp=true",
            "PrivateDevices=true",
            "ProtectSystem=strict",
            "ProtectHome=true",
            "ProtectKernelTunables=true",
            "ProtectKernelModules=true",
            "ProtectControlGroups=true",
            "RestrictSUIDSGID=true",
            "CapabilityBoundingSet=",
            "AmbientCapabilities=",
            "SupplementaryGroups=",
            "UMask=0077",
            "LockPersonality=true",
            "RestrictRealtime=true",
            "SystemCallArchitectures=native",
        )
    )
    approval = f"""[Unit]
Description=AgentNet independent Approval service
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User={APPROVAL_USER}
Group={APPROVAL_USER}
EnvironmentFile={APPROVAL_ENV}
Environment=HOME={APPROVAL_DATA}
Environment=XDG_STATE_HOME={APPROVAL_DATA}/.local/state
Environment=XDG_CACHE_HOME={APPROVAL_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={APPROVAL_DATA}/npm-runtime
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} approval serve --config {_unit_arg(str(APPROVAL_CONFIG))} --host 127.0.0.1 --port {APPROVAL_PORT}
SuccessExitStatus=143 SIGTERM
Restart=on-failure
RestartSec=2
{common}
ReadWritePaths={APPROVAL_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    core = f"""[Unit]
Description=AgentNet ordinary server agent
After=network-online.target {APPROVAL_UNIT}
Wants=network-online.target
Requires={APPROVAL_UNIT}

[Service]
Type=exec
User={CORE_USER}
Group={CORE_USER}
EnvironmentFile={CORE_ENV}
Environment=HOME={CORE_DATA}
Environment=XDG_STATE_HOME={CORE_DATA}/.local/state
Environment=XDG_CACHE_HOME={CORE_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={CORE_DATA}/npm-runtime
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} serve --config {_unit_arg(str(CORE_CONFIG))} --host 127.0.0.1 --port {CORE_PORT}
SuccessExitStatus=143 SIGTERM
Restart=on-failure
RestartSec=2
{common}
ReadWritePaths={CORE_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    responder = f"""[Unit]
Description=AgentNet package-owned C0 responder
After=network-online.target {CORE_UNIT}
Wants=network-online.target
Requires={CORE_UNIT}
ConditionPathExists={C0_RESPONDER_CONFIG}
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=exec
User={C0_RESPONDER_USER}
Group={C0_RESPONDER_USER}
Environment=HOME={C0_RESPONDER_DATA}
Environment=XDG_STATE_HOME={C0_RESPONDER_DATA}/.local/state
Environment=XDG_CACHE_HOME={C0_RESPONDER_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={C0_RESPONDER_DATA}/npm-runtime
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
LoadCredential=signing-key.pem:{SERVER_AGENT_KEY}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} c0-pilot responder --run --config {_unit_arg(str(C0_RESPONDER_CONFIG))} --credential %d/signing-key.pem
Restart=on-failure
RestartSec=2
{common}
RestrictAddressFamilies=AF_INET AF_INET6
ReadWritePaths={C0_RESPONDER_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    renewal = f"""[Unit]
Description=AgentNet current credential renewal
After=network-online.target {CORE_UNIT}
Requires={CORE_UNIT}

[Service]
Type=oneshot
User={CORE_USER}
Group={CORE_USER}
Environment=HOME={CORE_DATA}
Environment=XDG_STATE_HOME={CORE_DATA}/.local/state
Environment=XDG_CACHE_HOME={CORE_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={CORE_DATA}/npm-runtime
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} credential renew --identity {_unit_arg(str(SERVER_AGENT_IDENTITY))} --state {_unit_arg(str(CREDENTIAL_RENEW_STATE))}
{common}
RestrictAddressFamilies=AF_INET AF_INET6
ReadWritePaths={CORE_DATA}
""".encode()
    renewal_timer = f"""[Unit]
Description=Hourly AgentNet credential renewal

[Timer]
OnBootSec=5min
OnUnitInactiveSec=1h
Unit={CREDENTIAL_RENEW_UNIT}

[Install]
WantedBy=timers.target
""".encode()
    return {
        APPROVAL_UNIT: approval,
        CORE_UNIT: core,
        C0_RESPONDER_UNIT: responder,
        CREDENTIAL_RENEW_UNIT: renewal,
        CREDENTIAL_RENEW_TIMER: renewal_timer,
    }


def _validated_managed_identity_profile(
    path: Path,
    key_path: Path,
    account: pwd.struct_passwd,
    *,
    config: ExtensionConfig,
    request: ServerSetupRequest,
) -> dict[str, object]:
    try:
        value = json.loads(
            _read_private_managed_file(
                path,
                account,
                blocker="server_agent_identity",
                max_bytes=_MAX_CONFIG_BYTES,
            ),
            object_pairs_hook=_reject_duplicates,
            parse_constant=lambda _value: (_ for _ in ()).throw(
                ValueError("non-finite JSON number")
            ),
        )
    except (UnicodeError, json.JSONDecodeError, ValueError) as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent identity is invalid") from exc
    if (
        not isinstance(value, dict)
        or set(value) != {"schema", "server_base_url", "audience", "actor", "private_key_path"}
        or value.get("schema") != "agentnet.identity-profile.v1"
        or value.get("server_base_url") != request.core_public_origin
        or value.get("audience") != request.service_audience
        or value.get("private_key_path") != str(key_path)
        or not isinstance(value.get("actor"), dict)
    ):
        raise ServerSetupError("server_agent_identity", "managed server-agent identity mismatches fixed profile")
    try:
        actor = VerifiedActor.model_validate(value["actor"])
    except ValidationError as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent identity is invalid") from exc
    if (
        actor.domain_id != request.domain_id
        or actor.harness_id != config.enrolled_harness_id
        or actor.credential_id != config.enrolled_credential_id
    ):
        raise ServerSetupError("server_agent_identity", "managed server-agent identity mismatches current binding")
    key_payload = _read_private_managed_file(
        key_path,
        account,
        blocker="server_agent_identity",
        max_bytes=65_536,
    )
    try:
        P256KeyPair.from_private_pem(key_payload)
    except Exception as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent key is invalid") from exc
    return value


def _validate_broker_credential(value: str) -> None:
    if (
        not _BROKER_CREDENTIAL_MIN_LENGTH <= len(value) <= _BROKER_CREDENTIAL_MAX_LENGTH
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in value)
    ):
        raise ServerSetupError(
            "invalid_broker_credential",
            "Approval broker credential does not satisfy the fixed runtime policy",
        )
def _scanner_integer(
    values: Mapping[str, str],
    name: str,
    *,
    minimum: int,
    maximum: int,
) -> int:
    try:
        value = int(values[name])
    except (KeyError, ValueError) as exc:
        raise ServerSetupError(
            "scanner_configuration",
            f"{name} is not a bounded integer",
        ) from exc
    if str(value) != values[name] or not minimum <= value <= maximum:
        raise ServerSetupError(
            "scanner_configuration",
            f"{name} is not a bounded integer",
        )
    return value


def _resolve_scanner_setup(
    request: ServerSetupRequest,
    *,
    core_values: Mapping[str, str],
    scanner_trust: ScannerTrustConfig | None,
) -> ScannerSetupSpec | None:
    configured = _SCANNER_ENVIRONMENT_KEYS & set(core_values)
    if request.effective_artifact_mode == "disabled":
        if configured:
            raise ServerSetupError(
                "scanner_configuration",
                "communication-only setup must not receive scanner secrets",
            )
        return None
    if configured != _SCANNER_ENVIRONMENT_KEYS or scanner_trust is None:
        raise ServerSetupError(
            "scanner_configuration",
            "artifact setup requires the complete maintained ClamAV configuration",
        )
    try:
        endpoint = ScannerEndpoint.from_uri(core_values["AGENTNET_CLAMAV_ENDPOINT"])
    except ValueError as exc:
        raise ServerSetupError(
            "scanner_configuration",
            "ClamAV endpoint is not one exact loopback or Unix endpoint",
        ) from exc
    scanner_id = core_values["AGENTNET_CLAMAV_SCANNER_ID"]
    if not scanner_id or len(scanner_id) > 256 or scanner_id != scanner_id.strip():
        raise ServerSetupError("scanner_configuration", "scanner identity is invalid")
    key_epoch = _scanner_integer(
        core_values,
        "AGENTNET_CLAMAV_KEY_EPOCH",
        minimum=1,
        maximum=2**31 - 1,
    )
    signature_updated_at = _scanner_integer(
        core_values,
        "AGENTNET_CLAMAV_SIGNATURE_UPDATED_AT",
        minimum=1,
        maximum=2**63 - 1,
    )
    signature_max_age_seconds = _scanner_integer(
        core_values,
        "AGENTNET_CLAMAV_SIGNATURE_MAX_AGE_SECONDS",
        minimum=1,
        maximum=604_800,
    )
    now = int(time.time())
    if (
        signature_updated_at > now + scanner_trust.allowed_future_skew_seconds
        or now - signature_updated_at > signature_max_age_seconds
    ):
        raise ServerSetupError(
            "scanner_signatures_stale",
            "ClamAV signature database freshness is outside the approved bound",
        )
    key_path = Path(core_values["AGENTNET_CLAMAV_SIGNING_KEY_FILE"])
    try:
        key_input = _read_private_input(
            key_path,
            label="ClamAV scanner signing key",
            max_bytes=65_536,
        )
    except ServerSetupError as exc:
        raise ServerSetupError(
            "scanner_key_custody",
            "ClamAV scanner signing key custody is unsafe",
        ) from exc
    try:
        key = P256KeyPair.from_private_pem(key_input)
    except Exception as exc:
        raise ServerSetupError(
            "scanner_key_custody",
            "ClamAV scanner signing key is invalid",
        ) from exc
    engine_version = core_values["AGENTNET_CLAMAV_ENGINE_VERSION"]
    signature_version = core_values["AGENTNET_CLAMAV_SIGNATURE_VERSION"]
    try:
        rules_digest = clamav_rules_digest(
            signature_version=signature_version,
            signature_updated_at=signature_updated_at,
        )
        profile_digest = clamav_profile_digest(
            endpoint=endpoint,
            engine_version=engine_version,
            timeout_seconds=30.0,
            max_bytes=16_777_216,
            max_response_bytes=4_096,
            max_signature_age_seconds=signature_max_age_seconds,
        )
        trust_policy = ScannerTrustPolicy(
            max_attestation_age_seconds=scanner_trust.max_attestation_age_seconds,
            allowed_future_skew_seconds=scanner_trust.allowed_future_skew_seconds,
            required_engine=scanner_trust.required_engine,
            required_rules_digest=scanner_trust.required_rules_digest,
            required_profile_digest=scanner_trust.required_profile_digest,
            revoked_key_epochs=scanner_trust.revoked_key_epochs,
        )
    except (TypeError, ValueError) as exc:
        raise ServerSetupError(
            "scanner_configuration",
            "maintained scanner evidence is invalid",
        ) from exc
    if (
        scanner_trust.trusted_public_keys.get(f"{scanner_id}:{key_epoch}")
        != key.public_pem
        or scanner_trust.required_engine != "clamav"
        or scanner_trust.required_rules_digest != rules_digest
        or scanner_trust.required_profile_digest != profile_digest
    ):
        raise ServerSetupError(
            "scanner_trust_mismatch",
            "ClamAV runtime does not match pinned scanner trust",
        )
    return ScannerSetupSpec(
        endpoint=endpoint,
        key=key,
        key_input=key_input,
        scanner_id=scanner_id,
        scanner_key_epoch=key_epoch,
        engine_version=engine_version,
        signature_version=signature_version,
        signature_updated_at=signature_updated_at,
        signature_max_age_seconds=signature_max_age_seconds,
        rules_digest=rules_digest,
        profile_digest=profile_digest,
        trust_policy=trust_policy,
    )



def _require_scanner_readiness(spec: ScannerSetupSpec) -> dict[str, Any]:
    """Probe the exact maintained daemon and require signed clean evidence."""

    issued_at = int(time.time())
    digest = hashlib.sha256(b"agentnet-clamav-readiness\n").hexdigest()
    try:
        scanner = ClamAVScanner(
            spec.endpoint,
            spec.key,
            scanner_id=spec.scanner_id,
            scanner_key_epoch=spec.scanner_key_epoch,
            engine_version=spec.engine_version,
            signature_version=spec.signature_version,
            signature_updated_at=spec.signature_updated_at,
            policy_revision=1,
            trust_policy=spec.trust_policy,
            max_signature_age_seconds=spec.signature_max_age_seconds,
        )
        attestation = scanner.scan(
            artifact_id="agentnet-scanner-readiness",
            classification="C0",
            ciphertext_digest=digest,
            object_key="0" * 32,
            object_version=digest,
            plaintext_digest=digest,
            policy_revision=1,
            content=b"agentnet-clamav-readiness\n",
            issued_at=issued_at,
            expires_at=issued_at + min(
                60,
                spec.trust_policy.max_attestation_age_seconds,
            ),
        )
        verify_signature(
            spec.key.public_pem,
            "agentnet.artifact.attestation.v1",
            attestation.signed_fields(),
            attestation.signature,
        )
        spec.trust_policy.require_profile(attestation)
    except Exception as exc:
        raise ServerSetupError(
            "scanner_unready",
            "maintained ClamAV readiness could not be proven",
        ) from exc
    if (
        attestation.result != "allow"
        or attestation.scanner_id != spec.scanner_id
        or attestation.scanner_key_epoch != spec.scanner_key_epoch
        or attestation.rules_digest != spec.rules_digest
        or attestation.profile_digest != spec.profile_digest
    ):
        raise ServerSetupError(
            "scanner_unready",
            "maintained ClamAV readiness evidence is not exact",
        )
    return {
        "endpoint": spec.endpoint.uri,
        "engine_version": spec.engine_version,
        "profile_digest": spec.profile_digest,
        "rules_digest": spec.rules_digest,
        "scanner_id": spec.scanner_id,
        "scanner_key_epoch": spec.scanner_key_epoch,
        "signature_updated_at": spec.signature_updated_at,
        "status": "ready",
    }



def _server_setup_preflight(
    request: ServerSetupRequest,
    *,
    layout: SetupLayout,
) -> ServerSetupPreflight:
    try:
        require_approval_tls_environment()
    except GateBlocked:
        raise ServerSetupError(
            "approval_broker_auth",
            "Approval broker TLS environment is unsupported",
        ) from None
    if os.name != "posix" or not Path("/proc/1/comm").exists():
        raise ServerSetupError("unsupported_host", "ordinary server setup requires Linux with systemd")
    try:
        init_name = Path("/proc/1/comm").read_text(encoding="utf-8").strip()
    except OSError as exc:
        raise ServerSetupError("unsupported_host", "ordinary server setup cannot inspect init") from exc
    if init_name != "systemd" and layout.root == Path("/"):
        raise ServerSetupError("unsupported_host", "ordinary server setup requires systemd as PID 1")
    runtime = _resolve_setup_runtime()
    input_bundle = _read_input_bundle(request)
    oidc, owner_oidc, approvers, scanner_trust = _validate_inputs(
        request,
        input_bundle,
    )
    core_values = _parse_environment(input_bundle["core_environment_file"], label="Core environment input")
    approval_values = _parse_environment(input_bundle["approval_environment_file"], label="Approval environment input")
    scanner_setup = _resolve_scanner_setup(
        request,
        core_values=core_values,
        scanner_trust=scanner_trust,
    )
    if scanner_setup is not None:
        input_bundle["scanner_signing_key_file"] = scanner_setup.key_input
    required_core = {request.database_url_env, _BROKER_CREDENTIAL_NAME}
    if scanner_setup is not None:
        required_core.update(_SCANNER_ENVIRONMENT_KEYS)
    if oidc.client_secret_env is not None:
        required_core.add(oidc.client_secret_env)
    required_approval = {_BROKER_CREDENTIAL_NAME}
    if owner_oidc.client_secret_env is not None:
        required_approval.add(owner_oidc.client_secret_env)
    if not required_core <= set(core_values) or not required_approval <= set(approval_values):
        raise ServerSetupError("missing_secret_reference", "required runtime environment reference is absent")
    core_environment = _service_environment(
        core_values,
        CORE_DATA,
        runtime.uv_executable,
        allowed_names=frozenset(required_core),
    )
    approval_environment = _service_environment(
        approval_values,
        APPROVAL_DATA,
        runtime.uv_executable,
        allowed_names=frozenset(required_approval),
    )
    if scanner_setup is not None:
        core_environment["AGENTNET_CLAMAV_SIGNING_KEY_FILE"] = str(
            SCANNER_SIGNING_KEY
        )
    core_broker_credential = core_environment[_BROKER_CREDENTIAL_NAME]
    approval_broker_credential = approval_environment[_BROKER_CREDENTIAL_NAME]
    _validate_broker_credential(core_broker_credential)
    _validate_broker_credential(approval_broker_credential)
    if core_broker_credential != approval_broker_credential:
        raise ServerSetupError("broker_credential_mismatch", "Core and Approval broker credentials do not match")
    if core_environment[request.database_url_env] != request.database_url:
        raise ServerSetupError("database_reference_mismatch", "Core database reference does not match setup request")
    return ServerSetupPreflight(
        runtime=runtime,
        input_bundle=input_bundle,
        oidc_provider=oidc,
        owner_oidc=owner_oidc,
        approvers=approvers,
        scanner_trust=scanner_trust,
        scanner_setup=scanner_setup,
        core_values=core_values,
        approval_values=approval_values,
        core_environment=core_environment,
        approval_environment=approval_environment,
        request_digest=_request_digest(request, input_bundle, runtime=runtime),
        legacy_request_digest=(
            _legacy_request_digest(request, input_bundle)
            if request.schema_version == "agentnet.server-setup.request.v1"
            else ""
        ),
    )


def _planned_setup_evidence(
    request: ServerSetupRequest,
    preflight: ServerSetupPreflight,
) -> dict[str, Any]:
    units = render_units(
        preflight.runtime.node_executable,
        preflight.runtime.agentnet_executable,
        preflight.runtime.uv_executable,
    )
    steps = [
        {"id": "preflight", "status": "completed"},
        {"id": "core_identity", "status": _account_fact(CORE_USER, CORE_DATA)},
        {"id": "approval_identity", "status": _account_fact(APPROVAL_USER, APPROVAL_DATA)},
        {"id": "c0_responder_identity", "status": _account_fact(C0_RESPONDER_USER, C0_RESPONDER_DATA)},
        {"id": "private_roots", "status": "inspect_or_create"},
        {"id": "approval_provision", "status": "inspect_or_create"},
        {"id": "core_bootstrap", "status": "inspect_or_create"},
        {"id": "systemd_units", "status": "inspect_or_create"},
        {"id": "service_start", "status": "pending_explicit_apply_start"},
        {"id": "owner_ceremony", "status": "pending_human"},
    ]
    return {
        "schema": "agentnet.server-setup.evidence.v1",
        "status": "planned",
        "profile": request.profile,
        "artifact_mode": request.effective_artifact_mode,
        "request_digest": preflight.request_digest,
        "package_version": __version__,
        "managed_units": sorted(units),
        "loopback_ports": {"core": CORE_PORT, "approval": APPROVAL_PORT},
        "https_topology": "external_self_hosted_reverse_proxy_to_loopback",
        "prerequisites": {
            "host": "validated_linux_systemd",
            "runtime": "validated_service_visible_and_digest_bound",
            "inputs": "validated_owner_only",
            "artifact_scanner": (
                "validated_required"
                if request.effective_artifact_mode == "enabled"
                else "disabled_not_required"
            ),
            "broker_credential": "validated_redacted_runtime_policy",
            "database_reference": "validated_fixed_local_peer_contract_service_canary_pending_apply",
            "postgresql": {
                "auth_method": "peer",
                "database": ORDINARY_SERVER_POSTGRES_DATABASE,
                "hba_rule": "local agentnet agentnet peer",
                "hba_rule_order": "before_any_potentially_matching_local_rule",
                "ident_map": "none_exact_name_match",
                "os_user": CORE_USER,
                "role": ORDINARY_SERVER_POSTGRES_USER,
                "socket": ORDINARY_SERVER_POSTGRES_SOCKET,
                "operator_action": "install exact scoped HBA rule, reload PostgreSQL, then rerun same approved digest",
            },
            "public_routes": "pending_start_health_checks",
            "human_ceremonies": "pending_owner_oidc_and_passkey",
        },
        "steps": steps,
        "authority_granted": False,
        "identity_enrolled": False,
        "production_durability_proven": False,
        "next": "freeze request_digest, then rerun with --expected-request-digest and --apply after one human approval",
    }


def plan_server_setup(
    request: ServerSetupRequest,
    *,
    layout: SetupLayout = SetupLayout(),
) -> dict[str, Any]:
    preflight = _server_setup_preflight(request, layout=layout)
    return _planned_setup_evidence(request, preflight)


def _atomic_write(path: Path, payload: bytes, *, mode: int, uid: int = 0, gid: int = 0) -> str:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.parent.is_symlink():
        raise ServerSetupError("unsafe_path", "managed AgentNet path is a symlink")
    try:
        existing = os.open(
            path,
            os.O_RDONLY | os.O_NONBLOCK | getattr(os, "O_NOFOLLOW", 0),
        )
    except FileNotFoundError:
        existing = None
    except OSError as exc:
        raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile") from exc
    if existing is not None:
        try:
            before = os.fstat(existing)
            if (
                not stat.S_ISREG(before.st_mode)
                or before.st_nlink != 1
                or before.st_size != len(payload)
                or stat.S_IMODE(before.st_mode) != mode
                or before.st_uid != uid
                or before.st_gid != gid
            ):
                raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile")
            current = os.read(existing, len(payload) + 1)
            after = os.fstat(existing)
            if (
                current == payload
                and after.st_dev == before.st_dev
                and after.st_ino == before.st_ino
                and after.st_size == before.st_size
                and after.st_mtime_ns == before.st_mtime_ns
                and after.st_ctime_ns == before.st_ctime_ns
            ):
                return "already_satisfied"
            raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile")
        finally:
            os.close(existing)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        os.fchown(descriptor, uid, gid)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        try:
            os.link(temporary, path, follow_symlinks=False)
        except FileExistsError as exc:
            raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile") from exc
        temporary.unlink()
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise
    return "completed"


def _read_managed_exact(
    path: Path,
    *,
    uid: int,
    gid: int,
    mode: int,
    blocker: str,
    label: str,
    max_bytes: int = 65_536,
) -> bytes | None:
    """Read one root-owned managed file exactly, or report that it is absent."""

    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
        )
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise ServerSetupError(blocker, f"{label} custody is unsafe") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or before.st_uid != uid
            or before.st_gid != gid
            or stat.S_IMODE(before.st_mode) != mode
            or not 1 <= before.st_size <= max_bytes
        ):
            raise ServerSetupError(blocker, f"{label} custody is unsafe")
        payload = os.read(descriptor, before.st_size + 1)
        after = os.fstat(descriptor)
        if (
            len(payload) != before.st_size
            or after.st_dev != before.st_dev
            or after.st_ino != before.st_ino
            or after.st_size != before.st_size
            or after.st_mtime_ns != before.st_mtime_ns
            or after.st_ctime_ns != before.st_ctime_ns
        ):
            raise ServerSetupError(blocker, f"{label} changed during preflight")
        return payload
    finally:
        os.close(descriptor)


def _read_setup_marker(path: Path, *, uid: int, gid: int) -> bytes | None:
    return _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o600,
        blocker="setup_marker_conflict",
        label="setup marker",
    )


def _read_managed_unit(path: Path, *, uid: int, gid: int, blocker: str) -> bytes | None:
    return _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o644,
        blocker=blocker,
        label="managed AgentNet unit",
        max_bytes=_MAX_UNIT_BYTES,
    )


def _marker_upgrade_unit_profile(marker: Mapping[str, Any]) -> tuple[str, ...] | None:
    """Return the exact released source-unit profile eligible for this target."""

    source = marker.get("package_version")
    if not isinstance(source, str) or source == __version__:
        return None
    profile = _SUPPORTED_MARKER_UPGRADE_UNIT_PROFILES.get((source, __version__))
    if profile is None:
        return None
    if (
        source == "0.1.31"
        and (
            marker.get("schema") != "agentnet.server-setup.marker.v3"
            or marker.get("artifact_mode") != "disabled"
        )
    ):
        return None
    units = marker.get("units")
    unit_digests = marker.get("unit_digests")
    if (
        units != list(profile)
        or not isinstance(unit_digests, dict)
        or set(unit_digests) != set(profile)
    ):
        return None
    return profile


def _supported_marker_upgrade(marker: Mapping[str, Any]) -> bool:
    """Report whether a realized marker may present one package-caused digest drift.

    ``request_digest`` binds the runtime identity, so every package upgrade
    changes it even when the operator's request and inputs are byte-identical.
    Only explicitly mapped released source/target profiles are supported, and
    the same version never counts: same-version drift means the request itself
    changed and must keep failing closed.
    """

    return _marker_upgrade_unit_profile(marker) is not None


def _forward_only_setup_upgrade(source: object, target: object) -> bool:
    return (
        isinstance(source, str)
        and isinstance(target, str)
        and (source, target) in _FORWARD_ONLY_SETUP_UPGRADES
    )


def _accepted_marker_request_digest(marker: Mapping[str, Any], request_digest: str) -> bool:
    recorded = marker.get("request_digest")
    if recorded == request_digest:
        return marker.get("package_version") == __version__
    return (
        isinstance(recorded, str)
        and bool(_HEX64.fullmatch(recorded))
        and _supported_marker_upgrade(marker)
    )


def _validated_setup_marker(
    payload: bytes | None,
    *,
    request_digest: str,
    legacy_request_digest: str,
    artifact_mode: Literal["enabled", "disabled"] | None = None,
) -> dict[str, Any] | None:
    if payload is None:
        return None
    marker = _strict_json_bytes(payload, label="setup marker")
    upgrade_profile = _marker_upgrade_unit_profile(marker)
    expected_units = upgrade_profile or MANAGED_UNITS
    common = {
        "schema",
        "request_digest",
        "approval_config_digest",
        "core_config_digest",
        "units",
    }
    digests = (marker.get("approval_config_digest"), marker.get("core_config_digest"))
    if (
        marker.get("units") != list(expected_units)
        or any(not isinstance(value, str) or not re.fullmatch(r"[a-f0-9]{64}", value) for value in digests)
    ):
        raise ServerSetupError("setup_marker_conflict", "setup marker does not match the fixed profile")
    if marker.get("schema") == "agentnet.server-setup.marker.v1":
        if (
            artifact_mode is not None
            or not legacy_request_digest
            or set(marker) != common
            or marker.get("request_digest") != legacy_request_digest
        ):
            raise ServerSetupError("setup_marker_conflict", "legacy setup marker does not match this request")
        return marker
    v2_keys = common | {
        "package_version",
        "previous_marker_digest",
        "revision",
        "unit_digests",
    }
    previous = marker.get("previous_marker_digest")
    unit_digests = marker.get("unit_digests")
    if marker.get("schema") == "agentnet.server-setup.marker.v2":
        if (
            artifact_mode is not None
            or not legacy_request_digest
            or set(marker) != v2_keys
            or not _accepted_marker_request_digest(marker, request_digest)
            or not isinstance(marker.get("revision"), int)
            or isinstance(marker.get("revision"), bool)
            or marker["revision"] < 1
            or (previous is not None and (not isinstance(previous, str) or not re.fullmatch(r"[a-f0-9]{64}", previous)))
            or not isinstance(marker.get("package_version"), str)
            or not isinstance(unit_digests, dict)
            or set(unit_digests) != set(expected_units)
            or any(not isinstance(value, str) or not re.fullmatch(r"[a-f0-9]{64}", value) for value in unit_digests.values())
        ):
            raise ServerSetupError("setup_marker_conflict", "setup marker version or provenance is invalid")
        return marker
    v3_keys = v2_keys | {"artifact_mode"}
    if (
        marker.get("schema") != "agentnet.server-setup.marker.v3"
        or artifact_mode is None
        or set(marker) != v3_keys
        or marker.get("artifact_mode") != artifact_mode
        or not _accepted_marker_request_digest(marker, request_digest)
        or not isinstance(marker.get("revision"), int)
        or isinstance(marker.get("revision"), bool)
        or marker["revision"] < 1
        or (previous is not None and (not isinstance(previous, str) or not re.fullmatch(r"[a-f0-9]{64}", previous)))
        or not isinstance(marker.get("package_version"), str)
        or not isinstance(unit_digests, dict)
        or set(unit_digests) != set(expected_units)
        or any(not isinstance(value, str) or not re.fullmatch(r"[a-f0-9]{64}", value) for value in unit_digests.values())
    ):
        raise ServerSetupError("setup_marker_conflict", "setup marker version or provenance is invalid")
    return marker


def _prepare_setup_attempt(
    path: Path,
    *,
    existing_marker: Mapping[str, Any] | None,
    preexisting_state: bool,
    request_digest: str,
    uid: int,
    gid: int,
) -> tuple[str, bool]:
    payload = _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o600,
        blocker="clean_state_required",
        label="setup attempt",
    )
    if payload is not None:
        attempt = _strict_json_bytes(payload, label="setup attempt")
        if attempt != {
            "schema": "agentnet.server-setup.attempt.v1",
            "package_version": __version__,
            "request_digest": request_digest,
        }:
            raise ServerSetupError(
                "clean_state_required",
                "existing AgentNet setup attempt is not this exact package request",
            )
        return "resumed_exact_attempt", True
    if existing_marker is not None:
        return "not_required_existing_marker", False
    if preexisting_state:
        raise ServerSetupError(
            "clean_state_required",
            "pre-existing AgentNet state has no current-package setup custody",
        )
    result = _atomic_write(
        path,
        (
            json.dumps(
                {
                    "schema": "agentnet.server-setup.attempt.v1",
                    "package_version": __version__,
                    "request_digest": request_digest,
                },
                sort_keys=True,
            ).encode("utf-8")
            + b"\n"
        ),
        mode=0o600,
        uid=uid,
        gid=gid,
    )
    return result, True


def _atomic_replace_exact(
    path: Path,
    *,
    expected: bytes,
    payload: bytes,
    mode: int,
    uid: int,
    gid: int,
    reader: Callable[[Path], bytes | None] | None = None,
    blocker: str = "setup_marker_conflict",
    label: str = "setup marker",
    result: str = "updated_same_request",
) -> str:
    def read(target: Path) -> bytes | None:
        if reader is not None:
            return reader(target)
        return _read_setup_marker(target, uid=uid, gid=gid)

    current = read(path)
    if current != expected:
        raise ServerSetupError(blocker, f"{label} changed before compare-and-swap")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        os.fchown(descriptor, uid, gid)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        before_replace = path.lstat()
        if (
            not stat.S_ISREG(before_replace.st_mode)
            or before_replace.st_nlink != 1
            or before_replace.st_uid != uid
            or before_replace.st_gid != gid
            or stat.S_IMODE(before_replace.st_mode) != mode
            or read(path) != expected
        ):
            raise ServerSetupError(blocker, f"{label} changed before compare-and-swap")
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise
    return result


def _write_managed_unit(
    path: Path,
    payload: bytes,
    *,
    uid: int,
    gid: int,
    previous: bytes | None,
) -> str:
    """Install one managed unit, replacing exactly the journaled previous payload.

    Outside a journaled package upgrade this keeps the strict behaviour that a
    managed path holding unexpected content is a conflict, never an overwrite.
    """

    if previous is not None and previous != payload:
        current = _read_managed_unit(path, uid=uid, gid=gid, blocker="managed_path_conflict")
        if current == previous:
            return _atomic_replace_exact(
                path,
                expected=previous,
                payload=payload,
                mode=0o644,
                uid=uid,
                gid=gid,
                reader=lambda target: _read_managed_unit(
                    target,
                    uid=uid,
                    gid=gid,
                    blocker="managed_path_conflict",
                ),
                blocker="managed_path_conflict",
                label="managed AgentNet unit",
                result="updated_package_upgrade",
            )
    return _atomic_write(path, payload, mode=0o644, uid=uid, gid=gid)


def _remove_managed_unit_exact(
    path: Path,
    *,
    expected: bytes,
    uid: int,
    gid: int,
) -> None:
    """Remove one upgrade-created unit only while its exact bytes remain."""

    current = _read_managed_unit(
        path,
        uid=uid,
        gid=gid,
        blocker="setup_upgrade_conflict",
    )
    if current is None:
        return
    if current != expected:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade-created managed unit changed before rollback",
        )
    before = path.lstat()
    if (
        not stat.S_ISREG(before.st_mode)
        or before.st_nlink != 1
        or before.st_uid != uid
        or before.st_gid != gid
        or stat.S_IMODE(before.st_mode) != 0o644
        or _read_managed_unit(
            path,
            uid=uid,
            gid=gid,
            blocker="setup_upgrade_conflict",
        )
        != expected
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade-created managed unit changed before rollback",
        )
    path.unlink()
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _write_journaled_core_config(
    path: Path,
    payload: bytes,
    *,
    account: pwd.struct_passwd,
    previous: bytes,
) -> str:
    """Replace one Core config only from its exact journaled payload."""

    current = _read_private_managed_file(
        path,
        account,
        blocker="setup_upgrade_conflict",
        max_bytes=_MAX_CONFIG_BYTES,
    )
    if current == payload:
        return "already_satisfied"
    if current != previous:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "managed Core config changed after upgrade journal creation",
        )
    return _atomic_replace_exact(
        path,
        expected=previous,
        payload=payload,
        mode=0o600,
        uid=account.pw_uid,
        gid=account.pw_gid,
        reader=lambda target: _read_private_managed_file(
            target,
            account,
            blocker="setup_upgrade_conflict",
            max_bytes=_MAX_CONFIG_BYTES,
        ),
        blocker="setup_upgrade_conflict",
        label="managed Core config",
        result="updated_package_upgrade",
    )


def _require_marker_realized_state(
    marker: Mapping[str, Any],
    *,
    approval_config_digest: str,
    core_config_digest: str,
    unit_paths: Mapping[str, Path],
    uid: int,
    gid: int,
) -> None:
    """Prove the recorded pre-upgrade state is exactly what is realized on disk.

    A supported package upgrade may rewrite managed units, so it may only start
    from the exact realized state the previous package version committed.  Any
    drift means the deployment was changed outside setup and fails closed.
    """

    for key, actual in (
        ("approval_config_digest", approval_config_digest),
        ("core_config_digest", core_config_digest),
    ):
        if marker.get(key) != actual:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                f"realized {key} does not match the recorded pre-upgrade setup state",
            )
    recorded = marker.get("unit_digests")
    if not isinstance(recorded, dict) or set(recorded) != set(unit_paths):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "recorded pre-upgrade unit provenance does not match the fixed profile",
        )
    for unit, path in unit_paths.items():
        payload = _read_managed_unit(path, uid=uid, gid=gid, blocker="setup_upgrade_conflict")
        if payload is None or hashlib.sha256(payload).hexdigest() != recorded[unit]:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "realized managed unit does not match the recorded pre-upgrade setup state",
            )


def _validated_v0145_database_snapshot(value: object) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != {
        "schema_version",
        "migration_catalog",
        "endpoint_lifecycle_absent",
        "endpoint_mailbox_cursor",
        "identity",
        "migrated_collaboration",
        "preserved_relation_digests",
    }:
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    migrated = value.get("migrated_collaboration")
    if not isinstance(migrated, list) or any(
        not isinstance(entry, dict)
        or set(entry) != {"scope_id", "owner_harness_id", "member_harness_id"}
        or any(not isinstance(entry[key], str) or not entry[key] for key in entry)
        for entry in migrated
    ):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    if len({str(entry["scope_id"]) for entry in migrated}) != len(migrated):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    catalog = value.get("migration_catalog")
    identity = value.get("identity")
    preserved = value.get("preserved_relation_digests")
    if (
        value.get("schema_version") != _LIFECYCLE_SOURCE_SCHEMA
        or value.get("endpoint_lifecycle_absent") is not True
        or not isinstance(value.get("endpoint_mailbox_cursor"), int)
        or isinstance(value.get("endpoint_mailbox_cursor"), bool)
        or int(value["endpoint_mailbox_cursor"]) < 0
        or not isinstance(catalog, list)
        or len(catalog) != _LIFECYCLE_SOURCE_SCHEMA
        or not isinstance(identity, dict)
        or set(identity)
        != {
            "domain_id",
            "harness_id",
            "principal_id",
            "credential_id",
            "source_harness_kind",
            "harness_kind",
            "profile_key",
        }
        or identity.get("harness_kind") != "server"
        or any(
            not isinstance(identity.get(key), str) or not identity[key]
            for key in identity
        )
        or not isinstance(preserved, dict)
        or set(preserved) != set(_LIFECYCLE_PRESERVED_TABLES)
        or any(
            not isinstance(digest, str) or _HEX64.fullmatch(digest) is None
            for digest in preserved.values()
        )
    ):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    for expected_migration, record in zip(MIGRATIONS[:6], catalog, strict=True):
        if (
            not isinstance(record, dict)
            or set(record) != {"version", "name", "checksum", "applied_at"}
            or record.get("version") != expected_migration.version
            or record.get("name") != expected_migration.name
            or record.get("checksum") != expected_migration.checksum
            or not isinstance(record.get("applied_at"), int)
            or isinstance(record.get("applied_at"), bool)
            or int(record["applied_at"]) < 0
        ):
            raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    return dict(value)


def _validated_upgrade_systemd_snapshot(value: object) -> dict[str, dict[str, str]]:
    if not isinstance(value, dict) or set(value) != set(MANAGED_UNITS):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    result: dict[str, dict[str, str]] = {}
    for unit, raw in value.items():
        if (
            not isinstance(raw, dict)
            or set(raw) != {"LoadState", "UnitFileState", "ActiveState"}
            or any(
                not isinstance(raw.get(key), str)
                or not raw[key]
                or len(raw[key]) > 64
                or any(character in raw[key] for character in "\r\n\x00")
                for key in raw
            )
        ):
            raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
        result[str(unit)] = {key: str(raw[key]) for key in raw}
    return result


def _read_upgrade_journal(path: Path, *, uid: int, gid: int) -> dict[str, Any] | None:
    payload = _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o600,
        blocker="setup_upgrade_conflict",
        label="setup upgrade journal",
        max_bytes=4 * _MAX_CONFIG_BYTES,
    )
    if payload is None:
        return None
    journal = _strict_json_bytes(payload, label="setup upgrade journal")
    schema = journal.get("schema")
    units = journal.get("previous_units")
    configs = journal.get("previous_configs")
    from_package_version = journal.get("from_package_version")
    to_package_version = journal.get("to_package_version")
    source_profile = (
        _SUPPORTED_MARKER_UPGRADE_UNIT_PROFILES.get(
            (from_package_version, to_package_version)
        )
        if isinstance(from_package_version, str) and isinstance(to_package_version, str)
        else None
    )
    legacy_unit_shape = (
        schema == _LEGACY_UPGRADE_JOURNAL_SCHEMA
        and source_profile == MANAGED_UNITS
        and isinstance(units, dict)
        and set(units) == set(MANAGED_UNITS)
        and all(isinstance(value, str) for value in units.values())
    )
    current_unit_shape = (
        schema in {_UPGRADE_JOURNAL_SCHEMA, _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA}
        and source_profile is not None
        and isinstance(units, dict)
        and set(units) == set(MANAGED_UNITS)
        and all(
            isinstance(units[unit], str)
            if unit in source_profile
            else units[unit] is None
            for unit in MANAGED_UNITS
        )
    )
    base_keys = {
        "schema",
        "from_marker_sha256",
        "from_package_version",
        "from_request_digest",
        "to_package_version",
        "to_request_digest",
        "previous_units",
        "previous_configs",
    }
    lifecycle_keys = base_keys | {
        "previous_marker",
        "previous_database",
        "previous_systemd",
    }
    expected_keys = (
        lifecycle_keys
        if schema == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
        else base_keys
    )
    lifecycle_shape = (
        schema != _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
        or (
            (from_package_version, to_package_version) == _LIFECYCLE_SETUP_UPGRADE
            and isinstance(journal.get("previous_marker"), str)
            and len(str(journal["previous_marker"])) <= 2 * _MAX_CONFIG_BYTES
        )
    )
    if (
        schema
        not in {
            _LEGACY_UPGRADE_JOURNAL_SCHEMA,
            _UPGRADE_JOURNAL_SCHEMA,
            _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA,
        }
        or set(journal) != expected_keys
        or any(
            not isinstance(journal.get(key), str) or not _HEX64.fullmatch(str(journal.get(key)))
            for key in ("from_marker_sha256", "from_request_digest", "to_request_digest")
        )
        or not isinstance(from_package_version, str)
        or not isinstance(to_package_version, str)
        or not (legacy_unit_shape or current_unit_shape)
        or not isinstance(configs, dict)
        or set(configs) != _JOURNALED_CONFIG_KEYS
        or not lifecycle_shape
    ):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    for value in units.values():
        if value is not None and (
            not isinstance(value, str) or len(value) > 4 * _MAX_UNIT_BYTES
        ):
            raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    for value in configs.values():
        if not isinstance(value, str) or len(value) > 2 * _MAX_CONFIG_BYTES:
            raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    _journaled_unit_payloads(journal)
    _journaled_config_payloads(journal)
    if schema == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA:
        try:
            previous_marker = base64.b64decode(
                str(journal["previous_marker"]),
                validate=True,
            )
        except (ValueError, TypeError) as exc:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "setup upgrade journal is invalid",
            ) from exc
        if (
            not previous_marker
            or hashlib.sha256(previous_marker).hexdigest()
            != journal["from_marker_sha256"]
        ):
            raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
        _validated_v0145_database_snapshot(journal.get("previous_database"))
        _validated_upgrade_systemd_snapshot(journal.get("previous_systemd"))
    return journal


def _journaled_unit_payloads(journal: Mapping[str, Any]) -> dict[str, bytes | None]:
    try:
        return {
            unit: (
                None
                if value is None
                else base64.b64decode(str(value), validate=True)
            )
            for unit, value in dict(journal["previous_units"]).items()
        }
    except (KeyError, ValueError, TypeError) as exc:
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid") from exc


def _journaled_config_payloads(journal: Mapping[str, Any]) -> dict[str, bytes]:
    try:
        payloads = {
            key: base64.b64decode(str(value), validate=True)
            for key, value in dict(journal["previous_configs"]).items()
        }
    except (KeyError, ValueError, TypeError) as exc:
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid") from exc
    if set(payloads) != _JOURNALED_CONFIG_KEYS or any(
        not payload or len(payload) > _MAX_CONFIG_BYTES for payload in payloads.values()
    ):
        raise ServerSetupError("setup_upgrade_conflict", "setup upgrade journal is invalid")
    return payloads


def _write_upgrade_journal(path: Path, journal: Mapping[str, Any], *, uid: int, gid: int) -> None:
    payload = json.dumps(dict(journal), sort_keys=True, separators=(",", ":")).encode() + b"\n"
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        os.fchown(descriptor, uid, gid)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise


def _clear_upgrade_journal(path: Path) -> None:
    path.unlink(missing_ok=True)
    try:
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    except OSError:
        return
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _commit_setup_marker(
    path: Path,
    *,
    existing_payload: bytes | None,
    existing_marker: dict[str, Any] | None,
    request_digest: str,
    approval_config_digest: str,
    core_config_digest: str,
    unit_payloads: Mapping[str, bytes],
    artifact_mode: Literal["enabled", "disabled"] | None = None,
    uid: int,
    gid: int,
) -> str:
    unit_digests = {
        unit: hashlib.sha256(unit_payloads[unit]).hexdigest()
        for unit in MANAGED_UNITS
    }
    marker_schema = (
        "agentnet.server-setup.marker.v3"
        if artifact_mode is not None
        else "agentnet.server-setup.marker.v2"
    )
    realized = {
        "approval_config_digest": approval_config_digest,
        "core_config_digest": core_config_digest,
        "package_version": __version__,
        "request_digest": request_digest,
        "unit_digests": unit_digests,
        "units": list(MANAGED_UNITS),
    }
    if artifact_mode is not None:
        realized["artifact_mode"] = artifact_mode
    if (
        existing_marker is not None
        and existing_marker.get("schema") == marker_schema
        and all(existing_marker.get(key) == value for key, value in realized.items())
    ):
        return _atomic_write(path, existing_payload or b"", mode=0o600, uid=uid, gid=gid)
    previous_revision = existing_marker.get("revision") if existing_marker is not None else None
    revision = (
        previous_revision + 1
        if isinstance(previous_revision, int) and not isinstance(previous_revision, bool) and previous_revision >= 1
        else 1
    )
    marker = {
        "schema": marker_schema,
        "revision": revision,
        "previous_marker_digest": hashlib.sha256(existing_payload).hexdigest() if existing_payload is not None else None,
        **realized,
    }
    payload = json.dumps(marker, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    if existing_payload is None:
        return _atomic_write(path, payload, mode=0o600, uid=uid, gid=gid)
    return _atomic_replace_exact(
        path,
        expected=existing_payload,
        payload=payload,
        mode=0o600,
        uid=uid,
        gid=gid,
    )


def _run_systemctl(
    executable: Path,
    arguments: list[str],
    *,
    failure_message: str,
) -> None:
    try:
        completed = subprocess.run(
            [str(executable), *arguments],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError("systemd_start", failure_message) from exc
    if completed.returncode != 0:
        raise ServerSetupError("systemd_start", failure_message)


_SYSTEMD_SHOW_PROPERTIES = (
    "LoadState",
    "UnitFileState",
    "ActiveState",
    "FragmentPath",
    "DropInPaths",
    "User",
    "Group",
    "NoNewPrivileges",
    "PrivateDevices",
    "PrivateTmp",
    "ProtectHome",
    "ProtectSystem",
    "MainPID",
    "Environment",
    "ReadWritePaths",
)


def _systemd_show(executable: Path, unit: str) -> dict[str, str]:
    """Read exact live systemd properties for one managed unit."""

    if any(character in unit for character in "\n\r\x00"):
        raise ServerSetupError("unit_input", "systemd unit input contains a forbidden character")
    try:
        completed = subprocess.run(
            [
                str(executable),
                "show",
                unit,
                *(f"--property={name}" for name in _SYSTEMD_SHOW_PROPERTIES),
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError("service_runtime", "managed AgentNet unit runtime is not inspectable") from exc
    stdout = completed.stdout or b""
    if completed.returncode != 0 or len(stdout) > 262_144:
        raise ServerSetupError("service_runtime", "managed AgentNet unit runtime is not inspectable")
    try:
        text = stdout.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ServerSetupError("service_runtime", "managed AgentNet unit runtime evidence is invalid") from exc
    properties: dict[str, str] = {}
    for line in text.splitlines():
        name, separator, value = line.partition("=")
        if separator != "=" or name not in _SYSTEMD_SHOW_PROPERTIES:
            continue
        properties[name] = value
    return properties


def _validate_inactive_auxiliary_unit_state(
    *,
    unit: str,
    expected_unit_file_state: str,
    properties: dict[str, str],
) -> None:
    """Validate one inactive auxiliary unit without requiring service-only fields."""

    service_pid_invalid = (
        unit.endswith(".service")
        and properties.get("MainPID") not in {"", "0"}
    )
    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != expected_unit_file_state
        or properties.get("ActiveState") != "inactive"
        or service_pid_invalid
    ):
        raise ServerSetupError(
            "service_runtime_binding",
            "inactive AgentNet auxiliary unit does not match fixed state",
        )

def _systemd_timer_next_run(executable: Path, unit: str) -> int | None:
    """Return the exact next realtime activation in microseconds, if scheduled."""

    if any(character in unit for character in "\n\r\x00"):
        raise ServerSetupError("unit_input", "systemd unit input contains a forbidden character")
    try:
        completed = subprocess.run(
            [
                str(executable),
                "list-timers",
                unit,
                "--no-pager",
                "--no-legend",
                "--plain",
                "--output=json",
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule is not inspectable",
        ) from exc
    stdout = completed.stdout or b""
    if completed.returncode != 0 or len(stdout) > 262_144:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule is not inspectable",
        )
    try:
        rows = json.loads(stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule evidence is invalid",
        ) from exc
    if (
        not isinstance(rows, list)
        or len(rows) != 1
        or not isinstance(rows[0], dict)
        or rows[0].get("unit") != unit
    ):
        return None
    next_run = rows[0].get("next")
    if isinstance(next_run, bool) or not isinstance(next_run, int):
        return None
    return next_run


def _validate_active_renewal_timer_state(
    properties: Mapping[str, str],
    *,
    next_run_usec: int | None,
    now_usec: int,
) -> None:
    """Require an active timer with one finite future activation."""

    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != "enabled"
        or properties.get("ActiveState") != "active"
        or next_run_usec is None
        or next_run_usec <= now_usec
    ):
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal timer has no finite future run",
        )


def _credential_renewal_activation_commands(
    *,
    c0_responder_required: bool,
) -> tuple[list[str], ...]:
    """Return the race-free auxiliary activation sequence."""

    return (
        ["stop", CREDENTIAL_RENEW_UNIT],
        ["reset-failed", CREDENTIAL_RENEW_UNIT],
        (
            ["enable", "--now", C0_RESPONDER_UNIT]
            if c0_responder_required
            else ["disable", "--now", C0_RESPONDER_UNIT]
        ),
        ["enable", "--now", CREDENTIAL_RENEW_TIMER],
    )


def _read_live_process_identity(pid: int) -> tuple[Path, tuple[str, ...]]:
    """Read the exact executable and argv of one live managed service process."""

    try:
        executable = Path(os.readlink(f"/proc/{pid}/exe"))
        with open(f"/proc/{pid}/cmdline", "rb") as handle:
            raw = handle.read(262_145)
    except OSError as exc:
        raise ServerSetupError("service_runtime", "managed AgentNet service process is not inspectable") from exc
    if len(raw) > 262_144:
        raise ServerSetupError("service_runtime", "managed AgentNet service process evidence is oversized")
    try:
        arguments = tuple(raw.decode("utf-8").split("\x00"))
    except UnicodeDecodeError as exc:
        raise ServerSetupError("service_runtime", "managed AgentNet service process evidence is invalid") from exc
    while arguments and arguments[-1] == "":
        arguments = arguments[:-1]
    return executable, arguments


def _validate_systemd_service_runtime(
    systemctl_executable: Path,
    *,
    unit: str,
    user: str,
    data_root: Path,
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    expected_argv: tuple[str, ...],
    layout: SetupLayout,
) -> None:
    """Prove the live unit runs exactly the package-owned hermetic runtime.

    Unit files on disk are not evidence: systemd may still be running a stale
    fragment, an operator drop-in may have replaced the sandbox or the command,
    and a PATH-selected runtime would be a different executable from the one the
    approved digest bound.  This checks the loaded fragment, the sandbox, and the
    live process itself.
    """

    properties = _systemd_show(systemctl_executable, unit)
    expected_fragment = layout.unit(unit)
    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != "enabled"
        or properties.get("FragmentPath") != str(expected_fragment)
        or properties.get("DropInPaths", "").strip()
        or properties.get("User") != user
        or properties.get("Group") != user
        or properties.get("NoNewPrivileges") != "yes"
        or properties.get("PrivateDevices") != "yes"
        or properties.get("PrivateTmp") != "yes"
        or properties.get("ProtectHome") != "yes"
        or properties.get("ProtectSystem") != "strict"
    ):
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit is not running the exact managed fragment and sandbox",
        )
    if f"AGENTNET_UV={uv_executable}" not in properties.get("Environment", "").split():
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit does not bind the package-owned uv runtime",
        )
    if str(data_root) not in properties.get("ReadWritePaths", "").split():
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit does not bind the fixed private state root",
        )
    main_pid = properties.get("MainPID", "0")
    if not main_pid.isdecimal() or int(main_pid) < 1:
        raise ServerSetupError("service_runtime", "managed AgentNet unit has no live main process")
    live_executable, live_argv = _read_live_process_identity(int(main_pid))
    if live_executable != node_executable:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service executable does not match the approved hermetic runtime",
        )
    if live_argv != tuple(expected_argv):
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service argv does not match the approved hermetic runtime",
        )
    if str(agentnet_executable) not in live_argv:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service process does not run the approved package launcher",
        )


def _run_systemctl_sequence_or_reconcile(
    executable: Path,
    sequence: tuple[list[str], ...],
    *,
    reconcile: Callable[[], None],
) -> str:
    """Apply the managed unit sequence, reconciling one lost systemd response.

    A timed-out or failed ``systemctl`` call proves nothing about the effect: the
    unit may already be exactly in the intended end state.  On failure the exact
    live end state is verified instead; only that verified evidence, never the
    transport outcome, is allowed to report success.
    """

    first_failure: ServerSetupError | None = None
    for arguments in sequence:
        try:
            _run_systemctl(
                executable,
                arguments,
                failure_message="failed to start AgentNet managed units",
            )
        except ServerSetupError as failure:
            # A transport failure proves neither success nor failure. Continue
            # the bounded idempotent sequence, then accept only its exact final
            # live postcondition. This prevents an early lost response from
            # silently skipping a later Core restart.
            if first_failure is None:
                first_failure = failure
    if first_failure is None:
        return "completed"
    try:
        reconcile()
    except ServerSetupError:
        raise first_failure from None
    return "reconciled_after_response_loss"


def _require_core_bootstrap_evidence(
    evidence: Mapping[str, Any],
    *,
    expected_domain_id: str,
) -> None:
    domain = evidence.get("domain")
    recovery = evidence.get("recovery")
    storage = evidence.get("storage")
    audit = evidence.get("audit")
    binding = evidence.get("deployment_binding")
    if (
        not isinstance(domain, dict)
        or domain.get("domain_id") != expected_domain_id
        or not isinstance(recovery, dict)
        or recovery.get("ready") is not True
        or not isinstance(storage, dict)
        or storage.get("ready") is not True
        or not isinstance(audit, dict)
        or audit.get("valid") is not True
        or not isinstance(binding, dict)
    ):
        raise ServerSetupError(
            "core_bootstrap_evidence",
            "Core bootstrap evidence did not prove exact healthy durable state",
        )


def _run_bootstrap_idempotently(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    expected_domain_id: str,
) -> tuple[dict[str, Any], str]:
    """Run the idempotent Core bootstrap, retrying exactly one lost response.

    ``bootstrap-server-agent`` is idempotent, so a lost or truncated response is
    reconciled by rerunning it and requiring fresh exact evidence.  A refused or
    unhealthy bootstrap is never retried and never reported as reconciled.
    """

    attempts = 2
    for attempt in range(1, attempts + 1):
        try:
            evidence = _run_as(
                account,
                argv,
                environment=environment,
                stage="core_bootstrap",
            )
        except ServerSetupError as exc:
            if attempt >= attempts or exc.blocker not in _RESPONSE_LOSS_BLOCKERS:
                raise
            continue
        _require_core_bootstrap_evidence(evidence, expected_domain_id=expected_domain_id)
        return evidence, "completed" if attempt == 1 else "reconciled_after_response_loss"
    raise ServerSetupError("core_bootstrap_evidence", "Core bootstrap did not produce exact evidence")


def _ensure_account(
    name: str,
    home: Path,
    *,
    useradd_executable: Path,
) -> pwd.struct_passwd:
    try:
        account = pwd.getpwnam(name)
    except KeyError:
        completed = subprocess.run(
            [
                str(useradd_executable),
                "--system",
                "--user-group",
                "--no-create-home",
                "--home-dir",
                str(home),
                "--shell",
                "/usr/sbin/nologin",
                name,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if completed.returncode != 0:
            raise ServerSetupError("identity_create_failed", f"failed to create dedicated {name} identity")
        account = pwd.getpwnam(name)
    _account_fact(name, home)
    return account


def _ensure_root_private_directory(path: Path, *, uid: int, gid: int, label: str) -> str:
    if path.exists() or path.is_symlink():
        try:
            metadata = path.lstat()
        except OSError as exc:
            raise ServerSetupError(f"{label}_conflict", f"{label} root is unavailable") from exc
        if (
            not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != uid
            or metadata.st_gid != gid
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            raise ServerSetupError(f"{label}_conflict", f"{label} root conflicts with fixed profile")
        return "already_satisfied"
    path.mkdir(parents=True, mode=0o700)
    os.chown(path, uid, gid)
    os.chmod(path, 0o700)
    return "completed"


def _ensure_private_root(path: Path, account: pwd.struct_passwd) -> str:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        metadata = None
    except OSError as exc:
        raise ServerSetupError("private_root_conflict", "private AgentNet root is unavailable") from exc
    if metadata is not None:
        if (
            not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != account.pw_uid
            or metadata.st_gid != account.pw_gid
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            raise ServerSetupError("private_root_conflict", "private AgentNet root conflicts with fixed profile")
        return "already_satisfied"
    path.mkdir(parents=True, mode=0o700)
    os.chown(path, account.pw_uid, account.pw_gid)
    os.chmod(path, 0o700)
    return "completed"


def _service_environment(
    base: Mapping[str, str],
    data: Path,
    uv_executable: Path,
    *,
    allowed_names: frozenset[str],
) -> dict[str, str]:
    reserved = {
        "PATH",
        "HOME",
        "XDG_STATE_HOME",
        "XDG_CACHE_HOME",
        "AGENTNET_NPM_RUNTIME_DIR",
        "AGENTNET_UV",
        "AGENTNET_PACKAGE_ROOT",
        "AGENTNET_NODE_EXECUTABLE",
    }
    supplied = set(base)
    if reserved & supplied:
        raise ServerSetupError("reserved_environment", "runtime environment overrides a setup-owned variable")
    if supplied != set(allowed_names):
        raise ServerSetupError("unexpected_environment", "runtime environment names do not match fixed request references")
    environment = {
        "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "HOME": str(data),
        "XDG_STATE_HOME": str(data / ".local/state"),
        "XDG_CACHE_HOME": str(data / ".cache"),
        "AGENTNET_NPM_RUNTIME_DIR": str(data / "npm-runtime"),
        "AGENTNET_UV": str(uv_executable),
    }
    environment.update(base)
    return environment


def _drop_identity(account: pwd.struct_passwd):
    def apply() -> None:
        os.setgroups([])
        os.setgid(account.pw_gid)
        os.setuid(account.pw_uid)
    return apply


def _run_postgres_probe_as(
    account: pwd.struct_passwd,
    probe: Callable[[], dict[str, Any]],
    *,
    stage: str,
) -> dict[str, Any]:
    read_descriptor, write_descriptor = os.pipe()
    try:
        child = os.fork()
    except OSError as exc:
        os.close(read_descriptor)
        os.close(write_descriptor)
        raise ServerSetupError("postgres_preflight", f"{stage} could not start") from exc
    if child == 0:
        try:
            os.close(read_descriptor)
            try:
                _drop_identity(account)()
                os.environ.clear()
                os.environ.update(
                    {
                        "PATH": _SYSTEM_PATH,
                        "HOME": account.pw_dir,
                        "LANG": "C.UTF-8",
                    }
                )
                evidence = probe()
            except BaseException as exc:
                evidence = {"ready": False, "reason": type(exc).__name__}
            payload = json.dumps(evidence, sort_keys=True, separators=(",", ":")).encode()
            if len(payload) > 16_384:
                payload = b'{"ready":false,"reason":"OversizedEvidence"}'
            os.write(write_descriptor, payload)
        finally:
            os.close(write_descriptor)
            os._exit(0)
    os.close(write_descriptor)
    try:
        readable, _, _ = select.select([read_descriptor], [], [], 10)
        if not readable:
            os.kill(child, 9)
            os.waitpid(child, 0)
            raise ServerSetupError("postgres_preflight", f"{stage} timed out")
        payload = os.read(read_descriptor, 16_385)
        _, wait_status = os.waitpid(child, 0)
    finally:
        os.close(read_descriptor)
    if not payload or len(payload) > 16_384 or os.waitstatus_to_exitcode(wait_status) != 0:
        raise ServerSetupError("postgres_preflight", f"{stage} returned invalid evidence")
    try:
        evidence = _strict_json_bytes(payload, label=f"{stage} evidence")
    except ServerSetupError as exc:
        raise ServerSetupError("postgres_preflight", f"{stage} returned invalid evidence") from exc
    if evidence.get("ready") is not True:
        reason = evidence.get("reason")
        reason_class = reason if isinstance(reason, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", reason) else "Unavailable"
        raise ServerSetupError(
            "postgres_auth_not_ready",
            f"{stage} failed ({reason_class}); apply the exact operator-owned PostgreSQL peer rule, reload PostgreSQL, and retry the same approved digest",
        )
    return evidence


def _postgres_relation_digest(connection: Any, relation: str) -> str:
    """Hash one preserved relation without exporting its protected row values."""

    if relation not in _LIFECYCLE_PRESERVED_TABLES:
        raise ServerSetupError("setup_upgrade_conflict", "upgrade digest relation is invalid")
    digest = hashlib.sha256()
    cursor = connection.cursor()
    try:
        cursor.execute(
            f"""
            SELECT row_json
              FROM (
                    SELECT to_jsonb(snapshot_row)::text AS row_json
                      FROM "{relation}" AS snapshot_row
                   ) AS serialized_rows
             ORDER BY row_json
            """
        )
        while True:
            rows = cursor.fetchmany(256)
            if not rows:
                break
            for row in rows:
                value = row["row_json"]
                if not isinstance(value, str):
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "preserved PostgreSQL relation could not be serialized",
                    )
                digest.update(value.encode("utf-8"))
                digest.update(b"\n")
    finally:
        cursor.close()
    return digest.hexdigest()


def _postgres_migration_catalog(connection: Any) -> list[dict[str, Any]]:
    rows = connection.execute(
        "SELECT version,name,checksum,applied_at FROM schema_migrations ORDER BY version"
    ).fetchall()
    return [
        {
            "version": int(row["version"]),
            "name": str(row["name"]),
            "checksum": str(row["checksum"]),
            "applied_at": int(row["applied_at"]),
        }
        for row in rows
    ]


def _postgres_schema_version(connection: Any) -> int:
    row = connection.execute(
        "SELECT value FROM metadata WHERE key='schema_version'"
    ).fetchone()
    if row is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL schema version metadata is absent",
        )
    try:
        value = int(row["value"])
    except (KeyError, TypeError, ValueError) as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL schema version metadata is invalid",
        ) from exc
    return value


def _postgres_v0145_identity(
    connection: Any,
    *,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, str]:
    row = connection.execute(
        """
        SELECT harness.domain_id,harness.harness_id,harness.principal_id,
               harness.kind,harness.status AS harness_status,
               harness.credential_epoch,credential.credential_id,
               credential.status AS credential_status,credential.epoch
          FROM harnesses AS harness
          JOIN credentials AS credential
            ON credential.harness_id=harness.harness_id
         WHERE harness.domain_id=%s AND harness.harness_id=%s
           AND credential.credential_id=%s
        """,
        (domain_id, harness_id, credential_id),
    ).fetchone()
    if (
        row is None
        or not isinstance(row["principal_id"], str)
        or row["harness_status"] != "active"
        or row["credential_status"] != "active"
        or int(row["credential_epoch"]) != int(row["epoch"])
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.44 enrolled server identity is not the exact active binding",
        )
    return {
        "domain_id": str(row["domain_id"]),
        "harness_id": str(row["harness_id"]),
        "principal_id": str(row["principal_id"]),
        "credential_id": str(row["credential_id"]),
        "source_harness_kind": str(row["kind"]),
        "harness_kind": "server",
        "profile_key": profile_key,
    }


def _postgres_v0145_source_snapshot(
    connection: Any,
    *,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    catalog_rows = connection.execute(
        "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
    ).fetchall()
    if (
        _postgres_schema_version(connection) != _LIFECYCLE_SOURCE_SCHEMA
        or validate_applied_migrations(catalog_rows, migrations=MIGRATIONS[:6])
        != _LIFECYCLE_SOURCE_SCHEMA
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 requires the exact schema-v6 source",
        )
    require_exact_postgres_catalog(connection, migrations=MIGRATIONS[:6])
    lifecycle_relation = connection.execute(
        "SELECT to_regclass('endpoint_lifecycle') AS relation"
    ).fetchone()
    if lifecycle_relation is None or lifecycle_relation["relation"] is not None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "schema-v6 source contains endpoint lifecycle state",
        )
    identity = _postgres_v0145_identity(
        connection,
        domain_id=domain_id,
        harness_id=harness_id,
        credential_id=credential_id,
        profile_key=profile_key,
    )
    cursor_row = connection.execute(
        "SELECT COALESCE(MAX(cursor),0) AS cursor FROM recipients WHERE recipient_id=%s",
        (identity["harness_id"],),
    ).fetchone()
    committed_scopes = connection.execute(
        "SELECT scope_id,owner_harness_id,fresh_harness_id FROM communication_scopes "
        "WHERE state='committed' ORDER BY domain_id,principal_id,scope_id"
    ).fetchall()
    return {
        "schema_version": _LIFECYCLE_SOURCE_SCHEMA,
        "migration_catalog": _postgres_migration_catalog(connection),
        "endpoint_lifecycle_absent": True,
        "endpoint_mailbox_cursor": (
            int(cursor_row["cursor"]) if cursor_row is not None else 0
        ),
        "identity": identity,
        "migrated_collaboration": _expected_migrated_collaboration(committed_scopes),
        "preserved_relation_digests": {
            relation: _postgres_relation_digest(connection, relation)
            for relation in _LIFECYCLE_PRESERVED_TABLES
        },
    }

def _expected_migrated_collaboration(rows: Any) -> list[dict[str, str]]:
    """Project committed v6 communication authority into its exact v7 image."""

    expectation: list[dict[str, str]] = []
    for row in rows:
        expectation.append(
            {
                "scope_id": str(row["scope_id"]),
                "owner_harness_id": str(row["owner_harness_id"]),
                "member_harness_id": str(row["fresh_harness_id"]),
            }
        )
    expectation.sort(key=lambda entry: entry["scope_id"])
    return expectation


def _require_migrated_collaboration_state(
    *,
    expected: Any,
    scope_rows: Any,
    member_rows: Any,
) -> None:
    """Admit exactly the migrated authority and nothing else.

    An upgraded deployment legitimately carries one v7 collaboration scope per
    committed v6 communication scope, so emptiness is not the invariant.  Any
    extra scope, missing scope, foreign member, or changed role means new
    v0.1.45 activity that rollback would silently discard.
    """

    expectation = sorted(
        (
            str(entry["scope_id"]),
            str(entry["owner_harness_id"]),
            str(entry["member_harness_id"]),
        )
        for entry in expected
    )
    observed_scopes = sorted(
        (
            str(row["scope_id"]),
            str(row["owner_harness_id"]),
            str(row["source_communication_scope_id"]),
            str(row["state"]),
            str(row["state_reason"]),
        )
        for row in scope_rows
    )
    if observed_scopes != [
        (scope_id, owner, scope_id, "active", "migrated_v6_communication_scope")
        for scope_id, owner, _member in expectation
    ]:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 release state changed before rollback",
        )
    expected_members = sorted(
        [(scope_id, owner, "owner") for scope_id, owner, _member in expectation]
        + [(scope_id, member, "member") for scope_id, _owner, member in expectation]
    )
    observed_members = sorted(
        (str(row["scope_id"]), str(row["harness_id"]), str(row["role"]))
        for row in member_rows
    )
    if expected_members != observed_members:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 release state changed before rollback",
        )


def _require_v0145_source_snapshot(
    actual: Mapping[str, Any],
    expected: Mapping[str, Any],
) -> None:
    if dict(actual) != dict(expected):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL source changed after the upgrade journal was committed",
        )


def _postgres_v0145_target_endpoint(
    connection: Any,
    source: Mapping[str, Any],
) -> dict[str, Any]:
    identity = dict(source["identity"])
    endpoint_rows = connection.execute(
        "SELECT * FROM endpoint_lifecycle ORDER BY domain_id,harness_id"
    ).fetchall()
    if len(endpoint_rows) != 1:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target is not exact",
        )
    row = dict(endpoint_rows[0])
    expected = {
        "domain_id": identity["domain_id"],
        "harness_id": identity["harness_id"],
        "principal_id": identity["principal_id"],
        "current_credential_id": identity["credential_id"],
        "harness_kind": identity["harness_kind"],
        "profile_key": identity["profile_key"],
        "state": "restart_required",
        "adapter_generation": 1,
        "mailbox_cursor": source["endpoint_mailbox_cursor"],
        "capability_root_digest": None,
        "process_measurement": None,
        "state_reason": "explicit_user_restart_required",
        "revision": 2,
    }
    if any(row.get(key) != value for key, value in expected.items()):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target changed unexpectedly",
        )
    if (
        not isinstance(row.get("mailbox_cursor"), int)
        or isinstance(row.get("mailbox_cursor"), bool)
        or int(row["mailbox_cursor"]) < 0
        or not isinstance(row.get("created_at"), int)
        or not isinstance(row.get("updated_at"), int)
        or row["updated_at"] < row["created_at"]
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target metadata is invalid",
        )
    return row


def _postgres_v0145_target_is_rollback_safe(
    connection: Any,
    source: Mapping[str, Any],
) -> dict[str, Any]:
    catalog_rows = connection.execute(
        "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
    ).fetchall()
    if (
        _postgres_schema_version(connection) != _LIFECYCLE_TARGET_SCHEMA
        or validate_applied_migrations(catalog_rows) != _LIFECYCLE_TARGET_SCHEMA
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 PostgreSQL target changed before rollback",
        )
    require_exact_postgres_catalog(connection, migrations=MIGRATIONS)
    source_catalog = list(source["migration_catalog"])
    target_catalog = _postgres_migration_catalog(connection)
    if target_catalog[:6] != source_catalog or len(target_catalog) != 7:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL migration catalog changed before rollback",
        )
    preserved = dict(source["preserved_relation_digests"])
    if {
        relation: _postgres_relation_digest(connection, relation)
        for relation in _LIFECYCLE_PRESERVED_TABLES
    } != preserved:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "protected identity, access, or message state changed before rollback",
        )
    endpoint = _postgres_v0145_target_endpoint(connection, source)
    expected_migration = source.get("migrated_collaboration")
    if not isinstance(expected_migration, list):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 upgrade journal lacks its exact migrated authority expectation",
        )
    _require_migrated_collaboration_state(
        expected=expected_migration,
        scope_rows=connection.execute(
            "SELECT scope_id,owner_harness_id,source_communication_scope_id,state,"
            "state_reason FROM collaboration_scopes"
        ).fetchall(),
        member_rows=connection.execute(
            "SELECT scope_id,harness_id,role FROM collaboration_scope_members "
            "WHERE state='active'"
        ).fetchall(),
    )
    for relation in _LIFECYCLE_RELEASE_TABLES:
        if relation in {
            "endpoint_lifecycle",
            "collaboration_scopes",
            "collaboration_scope_members",
        }:
            continue
        row = connection.execute(
            f'SELECT COUNT(*) AS count FROM "{relation}"'
        ).fetchone()
        if row is None or int(row["count"]) != 0:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.45 release state changed before rollback",
            )
    return endpoint


def _postgres_v0145_database_operation(
    database_url: str,
    *,
    operation: Literal["snapshot", "migrate", "rollback"],
    source: Mapping[str, Any] | None,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    """Run one exact schema-6/7 transition under the PostgreSQL peer identity."""

    import psycopg
    from psycopg.rows import dict_row

    connection = psycopg.connect(
        database_url,
        autocommit=True,
        row_factory=dict_row,
        connect_timeout=5,
        application_name=f"agentnet:server-setup-{operation}",
    )
    try:
        with connection.transaction():
            connection.execute("SELECT pg_advisory_xact_lock(%s)", (MIGRATION_LOCK_ID,))
            if operation == "snapshot":
                snapshot = _postgres_v0145_source_snapshot(
                    connection,
                    domain_id=domain_id,
                    harness_id=harness_id,
                    credential_id=credential_id,
                    profile_key=profile_key,
                )
                return {"ready": True, "source": snapshot}
            if source is None:
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "v0.1.45 database journal is absent",
                )
            if operation == "migrate":
                if _postgres_schema_version(connection) == _LIFECYCLE_SOURCE_SCHEMA:
                    actual = _postgres_v0145_source_snapshot(
                        connection,
                        domain_id=domain_id,
                        harness_id=harness_id,
                        credential_id=credential_id,
                        profile_key=profile_key,
                    )
                    _require_v0145_source_snapshot(actual, source)
                    apply_postgres_migrations(connection)
                    identity = dict(source["identity"])
                    now = int(time.time())
                    connection.execute(
                        """
                        INSERT INTO endpoint_lifecycle(
                            domain_id,harness_id,principal_id,current_credential_id,
                            harness_kind,profile_key,state,adapter_generation,
                            mailbox_cursor,capability_root_digest,process_measurement,
                            state_reason,revision,created_at,updated_at
                        ) VALUES(%s,%s,%s,%s,%s,%s,'restart_required',1,%s,NULL,NULL,
                                 'explicit_user_restart_required',2,%s,%s)
                        """,
                        (
                            identity["domain_id"],
                            identity["harness_id"],
                            identity["principal_id"],
                            identity["credential_id"],
                            identity["harness_kind"],
                            identity["profile_key"],
                            int(source["endpoint_mailbox_cursor"]),
                            now,
                            now,
                        ),
                    )
                endpoint = _postgres_v0145_target_is_rollback_safe(connection, source)
                return {"ready": True, "endpoint_lifecycle": endpoint}
            if operation == "rollback":
                if _postgres_schema_version(connection) == _LIFECYCLE_SOURCE_SCHEMA:
                    actual = _postgres_v0145_source_snapshot(
                        connection,
                        domain_id=domain_id,
                        harness_id=harness_id,
                        credential_id=credential_id,
                        profile_key=profile_key,
                    )
                    _require_v0145_source_snapshot(actual, source)
                    return {"ready": True, "rolled_back": "already_source"}
                _postgres_v0145_target_is_rollback_safe(connection, source)
                for relation in _LIFECYCLE_RELEASE_TABLES:
                    connection.execute(f'DROP TABLE "{relation}"')
                migration = MIGRATIONS[6]
                deleted = connection.execute(
                    """
                    DELETE FROM schema_migrations
                     WHERE version=%s AND name=%s AND checksum=%s
                    """,
                    (migration.version, migration.name, migration.checksum),
                )
                if deleted.rowcount != 1:
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "v0.1.45 migration catalog changed before rollback",
                    )
                updated = connection.execute(
                    """
                    UPDATE metadata SET value=%s
                     WHERE key='schema_version' AND value=%s
                    """,
                    (str(_LIFECYCLE_SOURCE_SCHEMA), str(_LIFECYCLE_TARGET_SCHEMA)),
                )
                if updated.rowcount != 1:
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "v0.1.45 schema metadata changed before rollback",
                    )
                require_exact_postgres_catalog(connection, migrations=MIGRATIONS[:6])
                return {"ready": True, "rolled_back": "schema_v6_restored"}
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.45 database operation is invalid",
            )
    finally:
        connection.close()


def _run_v0145_database_operation_as(
    account: pwd.struct_passwd,
    database_url: str,
    *,
    operation: Literal["snapshot", "migrate", "rollback"],
    source: Mapping[str, Any] | None,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    try:
        return _run_postgres_probe_as(
            account,
            lambda: _postgres_v0145_database_operation(
                database_url,
                operation=operation,
                source=source,
                domain_id=domain_id,
                harness_id=harness_id,
                credential_id=credential_id,
                profile_key=profile_key,
            ),
            stage=f"v0145_database_{operation}",
        )
    except ServerSetupError as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            f"v0.1.45 PostgreSQL {operation} could not be proven exact",
        ) from exc


def _postgres_peer_gate(core_account: pwd.struct_passwd, database_url: str) -> dict[str, Any]:
    service = _run_postgres_probe_as(
        core_account,
        lambda: probe_ordinary_server_postgres_connection(database_url),
        stage="postgres_service_identity_canary",
    )
    try:
        postgres_account = pwd.getpwnam("postgres")
    except KeyError as exc:
        raise ServerSetupError(
            "postgres_admin_identity",
            "local PostgreSQL administrator identity is unavailable for read-only auth-rule inspection",
        ) from exc
    if postgres_account.pw_uid == 0 or postgres_account.pw_name != "postgres":
        raise ServerSetupError(
            "postgres_admin_identity",
            "local PostgreSQL administrator identity conflicts with fixed profile",
        )
    auth = _run_postgres_probe_as(
        postgres_account,
        inspect_ordinary_server_postgres_auth,
        stage="postgres_auth_rule_inspection",
    )
    if (
        service.get("current_user") != ORDINARY_SERVER_POSTGRES_USER
        or service.get("current_database") != ORDINARY_SERVER_POSTGRES_DATABASE
        or service.get("transport") != "unix_socket"
        or service.get("writable_primary") is not True
        or auth.get("auth_method") != "peer"
        or auth.get("ident_map") != "none_exact_name_match"
    ):
        raise ServerSetupError(
            "postgres_auth_not_ready",
            "PostgreSQL service identity or exact peer rule does not match the fixed profile",
        )
    return {
        "status": "validated_exact_local_peer",
        "database": ORDINARY_SERVER_POSTGRES_DATABASE,
        "os_user": CORE_USER,
        "role": ORDINARY_SERVER_POSTGRES_USER,
        "socket": ORDINARY_SERVER_POSTGRES_SOCKET,
        "auth_method": "peer",
        "ident_map": "none_exact_name_match",
    }


@dataclass(frozen=True)
class _BoundedCommandResult:
    returncode: int
    stdout: bytes
    stderr_present: bool


def _kill_product_process_tree(process: subprocess.Popen[bytes]) -> None:
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except OSError:
        if process.poll() is None:
            try:
                process.kill()
            except OSError:
                pass


def _run_bounded_product_process(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    stage: str,
) -> _BoundedCommandResult:
    try:
        process = subprocess.Popen(
            argv,
            env=dict(environment),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            preexec_fn=_drop_identity(account),
            start_new_session=True,
            text=False,
            bufsize=0,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise ServerSetupError("product_command_failed", f"{stage} could not start") from exc
    if process.stdout is None or process.stderr is None:  # pragma: no cover - Popen invariant
        _kill_product_process_tree(process)
        process.wait()
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid evidence streams")

    stdout = bytearray()
    stderr_bytes = 0
    streams = {
        process.stdout.fileno(): "stdout",
        process.stderr.fileno(): "stderr",
    }
    deadline = time.monotonic() + 300
    try:
        for descriptor in streams:
            os.set_blocking(descriptor, False)
        while streams:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(argv, 300)
            readable, _, _ = select.select(tuple(streams), (), (), min(remaining, 1.0))
            if not readable:
                if process.poll() is not None:
                    raise ServerSetupError(
                        "invalid_product_evidence",
                        f"{stage} left structured evidence streams open",
                    )
                continue
            for descriptor in readable:
                try:
                    chunk = os.read(descriptor, 65_536)
                except BlockingIOError:
                    continue
                if not chunk:
                    streams.pop(descriptor, None)
                    continue
                if streams[descriptor] == "stdout":
                    if len(stdout) + len(chunk) > 1_048_576:
                        raise ServerSetupError(
                            "invalid_product_evidence",
                            f"{stage} returned oversized structured evidence",
                        )
                    stdout.extend(chunk)
                else:
                    stderr_bytes += len(chunk)
                    if stderr_bytes > 65_536:
                        raise ServerSetupError(
                            "invalid_product_evidence",
                            f"{stage} returned oversized error evidence",
                        )
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise subprocess.TimeoutExpired(argv, 300)
        returncode = process.wait(timeout=remaining)
    except BaseException:
        _kill_product_process_tree(process)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        raise
    finally:
        process.stdout.close()
        process.stderr.close()
    return _BoundedCommandResult(
        returncode=returncode,
        stdout=bytes(stdout),
        stderr_present=stderr_bytes > 0,
    )


def _run_as(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    stage: str,
    accepted_returncodes: frozenset[int] = frozenset({0}),
) -> dict[str, Any]:
    try:
        completed = _run_bounded_product_process(
            account,
            argv,
            environment=environment,
            stage=stage,
        )
    except subprocess.TimeoutExpired as exc:
        raise ServerSetupError("product_command_failed", f"{stage} timed out") from exc
    except (OSError, subprocess.SubprocessError) as exc:
        raise ServerSetupError("product_command_failed", f"{stage} could not start") from exc
    if completed.returncode not in accepted_returncodes:
        stderr_state = "stderr_present" if completed.stderr_present else "no_stderr"
        raise ServerSetupError(
            "product_command_failed",
            f"{stage} failed with exit status {completed.returncode} ({stderr_state})",
        )
    try:
        value = json.loads(completed.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid structured evidence") from exc
    if not isinstance(value, dict):
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid structured evidence")
    return value


def _private_entry_exists(
    path: Path,
    account: pwd.struct_passwd,
    *,
    expected: str,
    blocker: str,
) -> bool:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        return False
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private path is unavailable") from exc
    if expected == "file":
        valid_type = stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1
        expected_mode = 0o600
    elif expected == "directory":
        valid_type = stat.S_ISDIR(metadata.st_mode)
        expected_mode = 0o700
    else:  # pragma: no cover - internal invariant
        raise AssertionError(expected)
    if (
        stat.S_ISLNK(metadata.st_mode)
        or not valid_type
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != expected_mode
    ):
        raise ServerSetupError(blocker, "managed private path custody conflicts with fixed profile")
    return True


def _require_communication_only_artifact_absence(core_runtime: Path) -> None:
    forbidden = (
        core_runtime / "secrets" / "artifact.key",
        core_runtime / "artifacts",
    )
    if any(path.exists() or path.is_symlink() for path in forbidden):
        raise ServerSetupError(
            "core_conflict",
            "communication-only Core state contains forbidden artifact state",
        )


def _require_private_file(path: Path, account: pwd.struct_passwd, *, blocker: str) -> None:
    try:
        metadata = path.lstat()
        resolved = path.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private file is unavailable") from exc
    if (
        resolved != path
        or not stat.S_ISREG(metadata.st_mode)
        or metadata.st_nlink != 1
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != 0o600
    ):
        raise ServerSetupError(blocker, "managed private file custody conflicts with fixed profile")


def _read_private_managed_file(
    path: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
    max_bytes: int,
) -> bytes:
    _require_private_file(path, account, blocker=blocker)
    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
        )
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private file is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or before.st_uid != account.pw_uid
            or before.st_gid != account.pw_gid
            or stat.S_IMODE(before.st_mode) != 0o600
            or not 1 <= before.st_size <= max_bytes
        ):
            raise ServerSetupError(blocker, "managed private file size or custody conflicts with fixed profile")
        changed_message = "managed private file changed while being read"
        first = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker=blocker,
            message=changed_message,
        )
        middle = os.fstat(descriptor)
        second = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker=blocker,
            message=changed_message,
        )
        after = os.fstat(descriptor)
        current = path.lstat()
        if (
            len(first) != before.st_size
            or first != second
            or any(
                getattr(snapshot, field) != getattr(before, field)
                for snapshot in (middle, after)
                for field in (
                    "st_dev",
                    "st_ino",
                    "st_size",
                    "st_mtime_ns",
                    "st_ctime_ns",
                )
            )
            or current.st_dev != before.st_dev
            or current.st_ino != before.st_ino
        ):
            raise ServerSetupError(blocker, changed_message)
        return first
    finally:
        os.close(descriptor)


def _validated_c0_terminal_marker(
    path: Path,
    account: pwd.struct_passwd,
    *,
    config: ExtensionConfig,
) -> dict[str, str] | None:
    if not path.exists() and not path.is_symlink():
        return None
    try:
        value = json.loads(
            _read_private_managed_file(
                path,
                account,
                blocker="c0_responder_terminal",
                max_bytes=4096,
            )
        )
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError(
            "c0_responder_terminal",
            "C0 responder terminal marker is invalid",
        ) from exc
    expected = {
        "schema": "agentnet.c0-pilot-responder.terminal.v1",
        "status": value.get("status") if isinstance(value, dict) else None,
        "domain_id": config.domain_id,
        "harness_id": config.enrolled_harness_id,
        "credential_id": config.enrolled_credential_id,
    }
    if (
        not isinstance(value, dict)
        or set(value) != set(expected)
        or value != expected
        or value.get("status")
        not in {"COMPLETED_C0_ROUND_TRIP", "expired", "invalidated", "failed"}
    ):
        raise ServerSetupError(
            "c0_responder_terminal",
            "C0 responder terminal marker conflicts with managed identity",
        )
    return value


def _managed_config_digest(
    path: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
    exclude_top_level: frozenset[str] = frozenset(),
) -> str:
    value = _strict_json_bytes(
        _read_private_managed_file(
            path,
            account,
            blocker=blocker,
            max_bytes=1_048_576,
        ),
        label="managed AgentNet configuration",
    )
    for key in exclude_top_level:
        value.pop(key, None)
    return canonical_digest(value)


def _require_private_directory(path: Path, account: pwd.struct_passwd, *, blocker: str) -> None:
    try:
        metadata = path.lstat()
        resolved = path.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private directory is unavailable") from exc
    if (
        resolved != path
        or not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != 0o700
    ):
        raise ServerSetupError(blocker, "managed private directory custody conflicts with fixed profile")


def _require_private_tree(
    root: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
) -> None:
    _require_private_directory(root, account, blocker=blocker)
    pending = [root]
    records = 0
    while pending:
        directory = pending.pop()
        try:
            entries = tuple(os.scandir(directory))
        except OSError as exc:
            raise ServerSetupError(blocker, "managed private tree is unavailable") from exc
        for entry in entries:
            records += 1
            if records > 20_000:
                raise ServerSetupError(blocker, "managed private tree exceeds fixed custody bound")
            item = Path(entry.path)
            try:
                metadata = entry.stat(follow_symlinks=False)
            except OSError as exc:
                raise ServerSetupError(blocker, "managed private tree changed during validation") from exc
            if stat.S_ISDIR(metadata.st_mode):
                _require_private_directory(item, account, blocker=blocker)
                pending.append(item)
            elif stat.S_ISREG(metadata.st_mode):
                _require_private_file(item, account, blocker=blocker)
            else:
                raise ServerSetupError(blocker, "managed private tree contains an unsupported entry")


def _approval_trust(
    config_path: Path,
    account: pwd.struct_passwd,
    approval_state: Path,
) -> tuple[ApprovalServiceConfig, list[IndependentApproverConfig]]:
    _require_private_file(config_path, account, blocker="approval_config")
    _require_private_directory(approval_state, account, blocker="approval_custody")
    try:
        config = ApprovalServiceConfig.model_validate(
            json.loads(
                _read_private_managed_file(
                    config_path,
                    account,
                    blocker="approval_config",
                    max_bytes=1_048_576,
                ).decode("utf-8")
            )
        )
    except Exception as exc:
        raise ServerSetupError("approval_config", "Approval configuration is invalid") from exc
    expected_database = approval_state / "approval.sqlite3"
    expected_record_key = approval_state / "secrets" / "records.key"
    if (
        config.data_dir != approval_state
        or config.database_path != expected_database
        or config.record_key_path != expected_record_key
    ):
        raise ServerSetupError("approval_conflict", "existing Approval custody paths conflict with fixed request")
    _require_private_file(expected_record_key, account, blocker="approval_custody")
    _require_private_file(expected_database, account, blocker="approval_custody")
    trusted: list[IndependentApproverConfig] = []
    for index, item in enumerate(config.approvers, start=1):
        expected_signer = approval_state / "signers" / f"approver-{index}.pem"
        if item.signer_private_key_path != expected_signer:
            raise ServerSetupError("approval_custody", "Approval signer path conflicts with fixed profile")
        _require_private_file(expected_signer, account, blocker="approval_custody")
        try:
            signer = P256KeyPair.from_private_pem(
                _read_private_managed_file(
                    expected_signer,
                    account,
                    blocker="approval_custody",
                    max_bytes=65_536,
                )
            )
        except Exception as exc:
            raise ServerSetupError("approval_custody", "Approval signer custody is invalid") from exc
        if signer.thumbprint != item.signer_key_id:
            raise ServerSetupError("approval_custody", "Approval signer key identifier mismatch")
        trusted.append(
            IndependentApproverConfig(
                principal_id=item.principal_id,
                authority_kind=item.authority_kind,
                signer_key_id=item.signer_key_id,
                public_key_pem=signer.public_pem,
                allowed_purposes=item.allowed_purposes,
            )
        )
    return config, trusted


def _require_exact_approval_policy(
    config: ApprovalServiceConfig,
    *,
    request: ServerSetupRequest,
    owner_oidc: ApprovalOwnerOIDCConfig,
    approvers: tuple[SetupApprover, ...],
    approval_state: Path,
) -> None:
    actual_approvers = tuple(
        SetupApprover(
            principal_id=item.principal_id,
            authority_kind=item.authority_kind,
            domain_id=item.domain_id,
            allowed_purposes=item.allowed_purposes,
            oidc_issuer=item.oidc_issuer,
            oidc_subject=item.oidc_subject,
            verified_email_alias=item.verified_email_alias,
        )
        for item in config.approvers
    )
    approval_host = urlsplit(request.approval_public_origin).hostname
    if (
        approval_host is None
        or config.public_origin != request.approval_public_origin
        or config.rp_id != approval_host
        or config.verifier_id != request.approval_verifier_id
        or config.data_dir != approval_state
        or config.database_path != approval_state / "approval.sqlite3"
        or config.record_key_path != approval_state / "secrets" / "records.key"
        or config.internal_core_credential_env != "AGENTNET_APPROVAL_CORE_TOKEN"
        or config.owner_oidc != owner_oidc
        or actual_approvers != approvers
    ):
        raise ServerSetupError("approval_conflict", "existing Approval state conflicts with fixed request")


class _RejectRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        return None


# A first managed start also materializes the service-private uv runtime, and a
# public route can converge after loopback health is exact.  Setup gives those
# startup and public-route probes one longer bounded window; ordinary probes keep
# the shorter default so a genuinely broken deployment still fails in bounded time.
_START_HEALTH_ATTEMPTS = 90
_HEALTH_USER_AGENT = f"AgentNet/{__version__}"


def _health_value_matches(actual: object, expected: object) -> bool:
    if isinstance(expected, tuple):
        return actual in expected
    if isinstance(expected, Mapping):
        return isinstance(actual, Mapping) and all(
            key in actual and _health_value_matches(actual[key], item)
            for key, item in expected.items()
        )
    return actual == expected


def _health(url: str, *, expected: Mapping[str, object], attempts: int = 30) -> None:
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        _RejectRedirects(),
    )
    request = urllib.request.Request(
        url,
        headers={"User-Agent": _HEALTH_USER_AGENT, "Accept": "application/json"},
        method="GET",
    )
    for _ in range(attempts):
        try:
            with opener.open(request, timeout=2) as response:  # noqa: S310 - fixed validated setup URL
                payload = response.read(65_537)
                if response.status != 200 or len(payload) > 65_536:
                    raise ValueError("invalid health response")
                value = json.loads(payload)
                if isinstance(value, dict) and _health_value_matches(value, expected):
                    return
        except (urllib.error.URLError, TimeoutError, ValueError, json.JSONDecodeError):
            pass
        time.sleep(1)
    raise ServerSetupError("service_health", "AgentNet service did not return exact healthy identity evidence")


def _core_create_arguments(
    request: ServerSetupRequest,
    *,
    node_executable: Path,
    executable: Path,
    core_config_path: Path,
    core_data: Path,
    oidc_path: Path,
    scanner_path: Path,
    scanner_trust: ScannerTrustConfig | None,
) -> list[str]:
    arguments = [
        str(node_executable), str(executable), "network", "create",
        "--config", str(core_config_path),
        "--data-dir", str(core_data / "core"),
        "--domain", request.domain_id,
        "--database-url-env", request.database_url_env,
        "--database-url-from-env",
        "--public-base-url", request.core_public_origin,
        "--oidc-config", str(oidc_path),
        "--artifact-mode", request.effective_artifact_mode,
        "--runtime-instance-id", request.runtime_instance_id,
    ]
    if scanner_trust is not None:
        arguments.extend(["--scanner-trust-config", str(scanner_path)])
    return arguments


def _require_core_create_evidence(
    result: Mapping[str, Any],
    core_config_path: Path,
    *,
    artifact_mode: Literal["enabled", "disabled"],
) -> None:
    readiness = result.get("local_readiness")
    artifact_evidence = readiness.get("artifacts") if isinstance(readiness, dict) else None
    scanner_evidence = readiness.get("scanner_trust") if isinstance(readiness, dict) else None
    artifact_ready = (
        isinstance(artifact_evidence, dict)
        and (
            artifact_evidence.get("ready") is True
            if artifact_mode == "enabled"
            else artifact_evidence == {
                "enabled": False,
                "required": False,
                "ready": False,
                "reason": "disabled",
            }
        )
    )
    scanner_ready = (
        isinstance(scanner_evidence, dict)
        and (
            scanner_evidence.get("ready") is True
            if artifact_mode == "enabled"
            else scanner_evidence == {
                "enabled": False,
                "ready": False,
                "required": False,
                "trusted_key_count": 0,
            }
        )
    )
    if (
        result.get("config") != str(core_config_path)
        or not isinstance(readiness, dict)
        or readiness.get("schema") != "agentnet.core.readiness.v1"
        or readiness.get("ready") is not False
        or not isinstance(readiness.get("storage"), dict)
        or readiness["storage"].get("ready") is not True
        or not isinstance(readiness.get("audit"), dict)
        or readiness["audit"].get("valid") is not True
        or readiness.get("artifact_mode") != artifact_mode
        or not artifact_ready
        or not isinstance(readiness.get("deployment_binding"), dict)
        or readiness["deployment_binding"].get("ready") is not False
        or readiness["deployment_binding"].get("required") is not True
        or not isinstance(readiness.get("a2a_schema"), dict)
        or readiness["a2a_schema"].get("ready") is not True
        or not scanner_ready
    ):
        raise ServerSetupError(
            "core_evidence",
            "Core create evidence did not prove exact healthy pre-enrollment state",
        )


def _build_core_oidc_config(
    request: ServerSetupRequest,
    oidc_provider: SetupOIDCProvider,
    *,
    trusted: Sequence[IndependentApproverConfig],
    approvers: Sequence[SetupApprover],
) -> OIDCEnrollmentConfig:
    selected = [
        item for item in trusted
        if item.principal_id == request.approval_approver_principal_id
    ]
    owner_policy = [
        item for item in approvers
        if item.principal_id == request.approval_approver_principal_id
    ]
    if len(selected) != 1 or len(owner_policy) != 1:
        raise ServerSetupError(
            "approval_conflict",
            "selected Approval trust anchor is unavailable",
        )
    return OIDCEnrollmentConfig(
        **oidc_provider.model_dump(mode="python"),
        verifier_id=request.approval_verifier_id,
        trusted_approvers=tuple(trusted),
        approval_service=ApprovalServiceClientConfig(
            origin=request.approval_public_origin,
            public_origin=request.approval_public_origin,
            service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
            approver_principal_id=request.approval_approver_principal_id,
            remote_activation_oidc_subject=owner_policy[0].oidc_subject,
            remote_activation_verified_email_alias=owner_policy[0].verified_email_alias,
        ),
    )


def _require_core_config_matches(
    config: Any,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
) -> None:
    if (
        config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT
        or config.domain_id != request.domain_id
        or config.data_dir != core_data / "core"
        or config.database_url != request.database_url
        or config.database_url_env != request.database_url_env
        or config.artifact_mode != request.effective_artifact_mode
        or config.artifact_backend != "postgres-manifest"
        or config.artifact_dir != core_data / "core" / "artifacts"
        or config.public_base_url != request.core_public_origin
        or config.effective_service_audience != request.service_audience
        or config.runtime_instance_id != request.runtime_instance_id
        or config.oidc_enrollment != oidc
        or config.scanner_trust != scanner_trust
        or config.server_agent_capabilities
        != (
            {ServerAgentCapability.OFFLINE_CUSTODY, ServerAgentCapability.ARTIFACT_STORAGE}
            if request.effective_artifact_mode == "enabled"
            else {ServerAgentCapability.OFFLINE_CUSTODY}
        )
        or config.a2a is not None
        or config.local_bindings is not None
        or config.relay is not None
        or config.federation_trust is not None
        or config.postgres_recovery_topology
    ):
        raise ServerSetupError("core_conflict", "existing Core state conflicts with fixed request")


def _load_validated_core_config(
    core_config_path: Path,
    core_account: pwd.struct_passwd,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
):
    config = load_config_json(
        _read_private_managed_file(
            core_config_path,
            core_account,
            blocker="core_custody",
            max_bytes=1_048_576,
        ).decode("utf-8")
    )
    _require_core_config_matches(
        config,
        request=request,
        core_data=core_data,
        oidc=oidc,
        scanner_trust=scanner_trust,
    )
    return config


def _legacy_remote_activation_oidc(
    oidc: OIDCEnrollmentConfig,
) -> OIDCEnrollmentConfig:
    approval = oidc.approval_service
    if approval is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "Core OIDC config has no Approval client to bind during upgrade",
        )
    return oidc.model_copy(
        update={
            "approval_service": approval.model_copy(
                update={
                    "remote_activation_oidc_subject": None,
                    "remote_activation_verified_email_alias": None,
                }
            )
        }
    )


def _load_upgrade_compatible_core_config(
    core_config_path: Path,
    core_oidc_path: Path,
    core_account: pwd.struct_passwd,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
) -> tuple[Any, bool]:
    """Validate exact current semantics, allowing only missing 0.1.30 owner pins."""

    legacy_oidc = _legacy_remote_activation_oidc(oidc)
    config = load_config_json(
        _read_private_managed_file(
            core_config_path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=_MAX_CONFIG_BYTES,
        ).decode("utf-8")
    )
    core_current = config.oidc_enrollment == oidc
    core_legacy = not core_current and config.oidc_enrollment == legacy_oidc
    if not core_current and not core_legacy:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "existing Core OIDC policy differs beyond supported owner binding migration",
        )
    normalized = (
        config.model_copy(update={"oidc_enrollment": oidc})
        if core_legacy and hasattr(config, "model_copy")
        else config
    )
    _require_core_config_matches(
        normalized,
        request=request,
        core_data=core_data,
        oidc=oidc,
        scanner_trust=scanner_trust,
    )

    standalone = _strict_json_bytes(
        _read_private_managed_file(
            core_oidc_path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=_MAX_CONFIG_BYTES,
        ),
        label="Core OIDC config",
    )
    desired_document = oidc.model_dump(mode="json")
    legacy_document = legacy_oidc.model_dump(mode="json")
    standalone_legacy = standalone == legacy_document
    if standalone != desired_document and not standalone_legacy:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "standalone Core OIDC policy differs beyond supported owner binding migration",
        )
    return normalized, core_legacy or standalone_legacy


def _migrate_legacy_remote_activation_policy(
    *,
    core_config_path: Path,
    core_oidc_path: Path,
    core_account: pwd.struct_passwd,
    oidc: OIDCEnrollmentConfig,
    pending: dict[str, Any],
) -> str:
    journal = pending.get("journal")
    if not isinstance(journal, Mapping):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "legacy owner binding migration requires an active upgrade journal",
        )
    previous = _journaled_config_payloads(journal)
    legacy_document = _legacy_remote_activation_oidc(oidc).model_dump(mode="json")
    desired_document = oidc.model_dump(mode="json")

    previous_oidc_document = _strict_json_bytes(
        previous["core_oidc_config"],
        label="journaled Core OIDC config",
    )
    if previous_oidc_document not in (legacy_document, desired_document):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "journaled Core OIDC policy is not an exact supported upgrade source",
        )
    oidc_payload = json.dumps(desired_document, indent=2, sort_keys=True).encode() + b"\n"

    previous_core_document = _strict_json_bytes(
        previous["core_config"],
        label="journaled Core config",
    )
    if previous_core_document.get("oidc_enrollment") not in (
        legacy_document,
        desired_document,
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "journaled Core config is not an exact supported owner binding source",
        )
    replacement_core_document = dict(previous_core_document)
    replacement_core_document["oidc_enrollment"] = desired_document
    core_payload = (
        json.dumps(replacement_core_document, indent=2, sort_keys=True).encode() + b"\n"
    )
    replacements = {
        "core_config": core_payload,
        "core_oidc_config": oidc_payload,
    }
    pending["replacement_configs"] = replacements
    core_status = _write_journaled_core_config(
        core_config_path,
        core_payload,
        account=core_account,
        previous=previous["core_config"],
    )
    oidc_status = _write_journaled_core_config(
        core_oidc_path,
        oidc_payload,
        account=core_account,
        previous=previous["core_oidc_config"],
    )
    return (
        "updated_package_upgrade"
        if "updated_package_upgrade" in {core_status, oidc_status}
        else "already_satisfied"
    )


def _prepare_supported_upgrade(
    *,
    existing_marker: dict[str, Any] | None,
    existing_marker_payload: bytes | None,
    approved_digest: str,
    approval_config_path: Path,
    approval_account: pwd.struct_passwd,
    approval_preexisting: bool,
    core_config_path: Path,
    core_oidc_path: Path,
    core_account: pwd.struct_passwd,
    core_preexisting: bool,
    database_url: str,
    domain_id: str,
    enrolled_harness_id: str | None,
    enrolled_credential_id: str | None,
    profile_key: str,
    systemctl_executable: Path,
    unit_paths: Mapping[str, Path],
    marker_path: Path,
    journal_path: Path,
    uid: int,
    gid: int,
    pending: dict[str, Any],
) -> str:
    """Gate one supported package upgrade before any managed host write.

    The marker validator already refused every digest drift except a released
    upgrade source.  This proves the recorded pre-upgrade state is exactly what
    is realized, then journals the exact previous units so a failed or
    interrupted upgrade rolls back to that same state instead of leaving a
    deployment that matches neither marker.
    """

    journal = _read_upgrade_journal(journal_path, uid=uid, gid=gid)
    upgrading = existing_marker is not None and existing_marker.get("request_digest") != approved_digest
    if journal is not None:
        superseded_committed_target = (
            existing_marker is not None
            and existing_marker_payload is not None
            and existing_marker.get("request_digest") == journal["to_request_digest"]
            and existing_marker.get("package_version") == journal["to_package_version"]
            and existing_marker.get("previous_marker_digest")
            == journal["from_marker_sha256"]
            and journal["to_package_version"] != __version__
            and _forward_only_setup_upgrade(
                journal["from_package_version"],
                journal["to_package_version"],
            )
            and _supported_marker_upgrade(existing_marker)
        )
        if superseded_committed_target:
            if not approval_preexisting or not core_preexisting:
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "committed setup marker has no realized Core and Approval state",
                )
            _require_marker_realized_state(
                existing_marker,
                approval_config_digest=_managed_config_digest(
                    approval_config_path,
                    approval_account,
                    blocker="approval_config",
                ),
                core_config_digest=_managed_config_digest(
                    core_config_path,
                    core_account,
                    blocker="core_custody",
                    exclude_top_level=frozenset(
                        {"enrolled_harness_id", "enrolled_credential_id"}
                    ),
                ),
                unit_paths=unit_paths,
                uid=uid,
                gid=gid,
            )
            # The prior target marker is already the no-rollback boundary.
            # Keep its journal until the separately approved next-edge journal
            # atomically replaces it.  Clearing it here creates a crash window
            # where a failed write loses both recovery records.
            journal = None
    if journal is not None:
        committed_target = (
            existing_marker is not None
            and existing_marker_payload is not None
            and existing_marker.get("request_digest") == journal["to_request_digest"]
            and existing_marker.get("package_version") == journal["to_package_version"]
            and existing_marker.get("previous_marker_digest")
            == journal["from_marker_sha256"]
            and journal["to_request_digest"] == approved_digest
            and journal["to_package_version"] == __version__
        )
        if committed_target:
            if not approval_preexisting or not core_preexisting:
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "committed setup marker has no realized Core and Approval state",
                )
            _require_marker_realized_state(
                existing_marker,
                approval_config_digest=_managed_config_digest(
                    approval_config_path,
                    approval_account,
                    blocker="approval_config",
                ),
                core_config_digest=_managed_config_digest(
                    core_config_path,
                    core_account,
                    blocker="core_custody",
                    exclude_top_level=frozenset(
                        {"enrolled_harness_id", "enrolled_credential_id"}
                    ),
                ),
                unit_paths=unit_paths,
                uid=uid,
                gid=gid,
            )
            if journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA:
                _clear_upgrade_journal(journal_path)
                return "cleared_committed_lifecycle_upgrade"
            if _forward_only_setup_upgrade(
                journal["from_package_version"],
                journal["to_package_version"],
            ):
                pending.update(
                    forward_only_upgrade=True,
                    journal_path=journal_path,
                )
                return "resumed_committed_forward_only_upgrade"
            _clear_upgrade_journal(journal_path)
            return "cleared_committed_upgrade"
        if (
            existing_marker_payload is None
            or journal["from_marker_sha256"] != hashlib.sha256(existing_marker_payload).hexdigest()
            or journal["from_package_version"] != existing_marker.get("package_version")
            or journal["from_request_digest"] != existing_marker.get("request_digest")
            or journal["to_request_digest"] != approved_digest
            or journal["to_package_version"] != __version__
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "an unrelated interrupted AgentNet setup upgrade is journaled on this host",
            )
        lifecycle_upgrade = journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
        pending.update(
            journal=journal,
            journal_path=journal_path,
            marker_path=marker_path,
            unit_paths=dict(unit_paths),
            config_paths={
                "core_config": core_config_path,
                "core_oidc_config": core_oidc_path,
            },
            core_account=core_account,
            database_url=database_url,
            systemctl_executable=systemctl_executable,
            rollback_capable_upgrade=lifecycle_upgrade,
            uid=uid,
            gid=gid,
        )
        return (
            "resumed_journaled_lifecycle_upgrade"
            if lifecycle_upgrade
            else "resumed_journaled_upgrade"
        )
    if not upgrading:
        return "not_required"
    assert existing_marker is not None and existing_marker_payload is not None
    source_profile = _marker_upgrade_unit_profile(existing_marker)
    if source_profile is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "recorded setup marker is not an exact supported upgrade source",
        )
    source_unit_paths = {unit: unit_paths[unit] for unit in source_profile}
    if not approval_preexisting or not core_preexisting:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "recorded setup marker has no realized Core and Approval state to upgrade",
        )
    _require_marker_realized_state(
        existing_marker,
        approval_config_digest=_managed_config_digest(
            approval_config_path,
            approval_account,
            blocker="approval_config",
        ),
        core_config_digest=_managed_config_digest(
            core_config_path,
            core_account,
            blocker="core_custody",
            exclude_top_level=frozenset({"enrolled_harness_id", "enrolled_credential_id"}),
        ),
        unit_paths=source_unit_paths,
        uid=uid,
        gid=gid,
    )
    previous_units: dict[str, str | None] = {}
    for unit, path in unit_paths.items():
        if unit not in source_profile:
            if path.exists() or path.is_symlink():
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "target-only managed unit exists before topology upgrade",
                )
            previous_units[unit] = None
            continue
        payload = _read_managed_unit(path, uid=uid, gid=gid, blocker="setup_upgrade_conflict")
        if payload is None:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "realized managed unit disappeared during upgrade preparation",
            )
        previous_units[unit] = base64.b64encode(payload).decode("ascii")
    previous_configs = {
        "core_config": base64.b64encode(
            _read_private_managed_file(
                core_config_path,
                core_account,
                blocker="setup_upgrade_conflict",
                max_bytes=_MAX_CONFIG_BYTES,
            )
        ).decode("ascii"),
        "core_oidc_config": base64.b64encode(
            _read_private_managed_file(
                core_oidc_path,
                core_account,
                blocker="setup_upgrade_conflict",
                max_bytes=_MAX_CONFIG_BYTES,
            )
        ).decode("ascii"),
    }
    lifecycle_upgrade = (
        str(existing_marker["package_version"]),
        __version__,
    ) == _LIFECYCLE_SETUP_UPGRADE
    journal: dict[str, Any] = {
        "schema": (
            _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
            if lifecycle_upgrade
            else _UPGRADE_JOURNAL_SCHEMA
        ),
        "from_marker_sha256": hashlib.sha256(existing_marker_payload).hexdigest(),
        "from_package_version": str(existing_marker["package_version"]),
        "from_request_digest": str(existing_marker["request_digest"]),
        "to_package_version": __version__,
        "to_request_digest": approved_digest,
        "previous_units": previous_units,
        "previous_configs": previous_configs,
    }
    if lifecycle_upgrade:
        if not enrolled_harness_id or not enrolled_credential_id:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.44 source has no exact enrolled server identity",
            )
        database_evidence = _run_v0145_database_operation_as(
            core_account,
            database_url,
            operation="snapshot",
            source=None,
            domain_id=domain_id,
            harness_id=enrolled_harness_id,
            credential_id=enrolled_credential_id,
            profile_key=profile_key,
        )
        previous_database = _validated_v0145_database_snapshot(
            database_evidence.get("source")
        )
        previous_systemd: dict[str, dict[str, str]] = {}
        for unit in MANAGED_UNITS:
            properties = _systemd_show(systemctl_executable, unit)
            previous_systemd[unit] = {
                key: properties[key]
                for key in ("LoadState", "UnitFileState", "ActiveState")
            }
        _validated_upgrade_systemd_snapshot(previous_systemd)
        journal.update(
            previous_marker=base64.b64encode(existing_marker_payload).decode("ascii"),
            previous_database=previous_database,
            previous_systemd=previous_systemd,
        )
    _write_upgrade_journal(journal_path, journal, uid=uid, gid=gid)
    pending.update(
        journal=journal,
        journal_path=journal_path,
        marker_path=marker_path,
        unit_paths=dict(unit_paths),
        config_paths={
            "core_config": core_config_path,
            "core_oidc_config": core_oidc_path,
        },
        core_account=core_account,
        database_url=database_url,
        systemctl_executable=systemctl_executable,
        rollback_capable_upgrade=lifecycle_upgrade,
        uid=uid,
        gid=gid,
    )
    return "validated_pre_upgrade_realized_state"


def _restore_upgrade_systemd_state(pending: Mapping[str, Any]) -> None:
    if pending.get("service_state_changed") is not True:
        return
    journal = pending["journal"]
    previous = _validated_upgrade_systemd_snapshot(journal.get("previous_systemd"))
    systemctl_executable = Path(str(pending["systemctl_executable"]))
    _run_systemctl(
        systemctl_executable,
        ["daemon-reload"],
        failure_message="systemd could not reload restored v0.1.44 units",
    )
    for unit in MANAGED_UNITS:
        state = previous[unit]
        active = state["ActiveState"] == "active"
        unit_file_state = state["UnitFileState"]
        if unit_file_state == "enabled":
            arguments = ["enable", "--now", unit] if active else ["enable", unit]
            _run_systemctl(
                systemctl_executable,
                arguments,
                failure_message="systemd could not restore v0.1.44 enablement",
            )
            if not active:
                _run_systemctl(
                    systemctl_executable,
                    ["stop", unit],
                    failure_message="systemd could not restore v0.1.44 inactive state",
                )
        elif unit_file_state == "disabled":
            _run_systemctl(
                systemctl_executable,
                ["disable", "--now", unit],
                failure_message="systemd could not restore v0.1.44 disablement",
            )
            if active:
                _run_systemctl(
                    systemctl_executable,
                    ["start", unit],
                    failure_message="systemd could not restore v0.1.44 active state",
                )
        elif unit_file_state == "static":
            _run_systemctl(
                systemctl_executable,
                ["start" if active else "stop", unit],
                failure_message="systemd could not restore v0.1.44 static unit state",
            )
        else:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "journaled v0.1.44 systemd state cannot be restored exactly",
            )
    for unit in MANAGED_UNITS:
        actual = _systemd_show(systemctl_executable, unit)
        expected = previous[unit]
        if any(
            actual.get(key) != expected[key]
            for key in ("LoadState", "UnitFileState", "ActiveState")
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "restored v0.1.44 systemd state could not be proven exact",
            )


def _rollback_pending_upgrade(pending: Mapping[str, Any]) -> None:
    """Restore only exact journaled state and retain evidence on uncertainty."""

    journal = pending.get("journal")
    if not isinstance(journal, Mapping):
        return
    previous_configs = _journaled_config_payloads(journal)
    replacements = pending.get("replacement_configs", {})
    if not isinstance(replacements, Mapping):
        raise ServerSetupError("setup_upgrade_conflict", "upgrade rollback state is invalid")
    core_account = pending["core_account"]
    config_paths = dict(pending["config_paths"])
    current_configs: dict[str, bytes] = {}
    for key, path in config_paths.items():
        current = _read_private_managed_file(
            path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=_MAX_CONFIG_BYTES,
        )
        replacement = replacements.get(key)
        if current != previous_configs[key] and (
            not isinstance(replacement, bytes) or current != replacement
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "managed Core config changed before upgrade rollback",
            )
        current_configs[key] = current

    previous_units = _journaled_unit_payloads(journal)
    unit_paths = dict(pending["unit_paths"])
    replacements_units = pending.get("replacement_units", {})
    if set(previous_units) != set(unit_paths) or not isinstance(replacements_units, Mapping):
        raise ServerSetupError("setup_upgrade_conflict", "upgrade rollback state is invalid")
    current_units: dict[str, bytes | None] = {}
    for unit, path in unit_paths.items():
        current = _read_managed_unit(
            path,
            uid=int(pending["uid"]),
            gid=int(pending["gid"]),
            blocker="setup_upgrade_conflict",
        )
        replacement = replacements_units.get(unit)
        if current != previous_units[unit] and (
            not isinstance(replacement, bytes) or current != replacement
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "managed unit changed before upgrade rollback",
            )
        current_units[unit] = current

    lifecycle_upgrade = journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
    if lifecycle_upgrade:
        marker_payload = _read_setup_marker(
            Path(str(pending["marker_path"])),
            uid=int(pending["uid"]),
            gid=int(pending["gid"]),
        )
        try:
            previous_marker = base64.b64decode(
                str(journal["previous_marker"]),
                validate=True,
            )
        except (ValueError, TypeError) as exc:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "journaled setup marker is invalid",
            ) from exc
        if marker_payload != previous_marker:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "setup marker changed before upgrade rollback",
            )
        if pending.get("service_state_changed") is True:
            systemctl_executable = Path(str(pending["systemctl_executable"]))
            for arguments in (
                ["disable", "--now", C0_RESPONDER_UNIT],
                ["disable", "--now", CREDENTIAL_RENEW_TIMER],
                ["stop", CREDENTIAL_RENEW_UNIT],
                ["disable", "--now", CORE_UNIT],
                ["disable", "--now", APPROVAL_UNIT],
            ):
                _run_systemctl(
                    systemctl_executable,
                    arguments,
                    failure_message="v0.1.45 services could not be quiesced for rollback",
                )
        source = _validated_v0145_database_snapshot(journal.get("previous_database"))
        identity = source["identity"]
        _run_v0145_database_operation_as(
            core_account,
            str(pending["database_url"]),
            operation="rollback",
            source=source,
            domain_id=str(identity["domain_id"]),
            harness_id=str(identity["harness_id"]),
            credential_id=str(identity["credential_id"]),
            profile_key=str(identity["profile_key"]),
        )

    for key, path in config_paths.items():
        current = current_configs[key]
        if current == previous_configs[key]:
            continue
        _write_journaled_core_config(
            path,
            previous_configs[key],
            account=core_account,
            previous=current,
        )
    for unit, path in unit_paths.items():
        current = current_units[unit]
        previous_payload = previous_units[unit]
        if current == previous_payload:
            continue
        if not isinstance(current, bytes):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "upgrade-created unit disappeared before rollback",
            )
        if previous_payload is None:
            _remove_managed_unit_exact(
                path,
                expected=current,
                uid=int(pending["uid"]),
                gid=int(pending["gid"]),
            )
        else:
            _write_managed_unit(
                path,
                previous_payload,
                uid=int(pending["uid"]),
                gid=int(pending["gid"]),
                previous=current,
            )
    if lifecycle_upgrade:
        _restore_upgrade_systemd_state(pending)
    try:
        _clear_upgrade_journal(Path(str(pending["journal_path"])))
    except OSError as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade rollback completed but its journal evidence could not be cleared",
        ) from exc


def apply_server_setup(
    request: ServerSetupRequest,
    *,
    start: bool,
    expected_request_digest: str,
    layout: SetupLayout = SetupLayout(),
    _allow_test_layout: bool = False,
) -> dict[str, Any]:
    pending: dict[str, Any] = {}
    verified: dict[str, bool] = {}
    try:
        return _apply_server_setup(
            request,
            start=start,
            expected_request_digest=expected_request_digest,
            layout=layout,
            _allow_test_layout=_allow_test_layout,
            _pending_upgrade=pending,
            _verified=verified,
        )
    except BaseException as exc:
        try:
            _rollback_pending_upgrade(pending)
        except ServerSetupError as rollback_exc:
            if (
                isinstance(exc, ServerSetupError)
                and exc.blocker == "managed_path_conflict"
            ):
                if verified.get("identity_enrolled"):
                    exc.identity_enrolled = True
                raise exc from rollback_exc
            if verified.get("identity_enrolled"):
                rollback_exc.identity_enrolled = True
            raise rollback_exc from exc
        if isinstance(exc, ServerSetupError) and verified.get("identity_enrolled"):
            exc.identity_enrolled = True
        raise


def _apply_server_setup(
    request: ServerSetupRequest,
    *,
    start: bool,
    expected_request_digest: str,
    layout: SetupLayout,
    _allow_test_layout: bool,
    _pending_upgrade: dict[str, Any],
    _verified: dict[str, bool],
) -> dict[str, Any]:
    preflight = _server_setup_preflight(request, layout=layout)
    actual_digest = preflight.request_digest
    if not re.fullmatch(r"[a-f0-9]{64}", expected_request_digest) or actual_digest != expected_request_digest:
        raise ServerSetupError(
            "approval_digest_mismatch",
            "current setup request does not match the frozen human-approved digest",
        )
    approved_digest = expected_request_digest
    if layout.root != Path("/") and not _allow_test_layout:
        raise ServerSetupError("test_layout", "apply requires the real host layout")
    if not _allow_test_layout and os.geteuid() != 0:
        raise ServerSetupError("privilege_required", "server setup apply requires root")
    try:
        import fcntl as posix_fcntl
    except ModuleNotFoundError as exc:
        raise ServerSetupError("unsupported_host", "ordinary server setup requires POSIX file locking") from exc
    root_uid = 0 if layout.root == Path("/") else os.geteuid()
    root_gid = 0 if layout.root == Path("/") else os.getegid()
    _ensure_root_private_directory(
        layout.lock.parent,
        uid=root_uid,
        gid=root_gid,
        label="setup_lock",
    )
    try:
        lock_descriptor = os.open(
            layout.lock,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
            0o600,
        )
    except OSError as exc:
        raise ServerSetupError("setup_lock", "AgentNet setup lock custody is unsafe") from exc
    lock_metadata = os.fstat(lock_descriptor)
    if (
        not stat.S_ISREG(lock_metadata.st_mode)
        or lock_metadata.st_nlink != 1
        or lock_metadata.st_uid != root_uid
        or lock_metadata.st_gid != root_gid
        or stat.S_IMODE(lock_metadata.st_mode) != 0o600
    ):
        os.close(lock_descriptor)
        raise ServerSetupError("setup_lock", "AgentNet setup lock custody conflicts with fixed profile")
    try:
        try:
            posix_fcntl.flock(lock_descriptor, posix_fcntl.LOCK_EX | posix_fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ServerSetupError("setup_locked", "another AgentNet server setup is active") from exc
        core_data = layout.host(CORE_DATA)
        approval_data = layout.host(APPROVAL_DATA)
        c0_responder_data = layout.host(C0_RESPONDER_DATA)
        c0_responder_config_path = layout.host(C0_RESPONDER_CONFIG)
        c0_responder_terminal_path = layout.host(C0_RESPONDER_TERMINAL)
        core_config_path = layout.host(CORE_CONFIG)
        approval_config_path = layout.host(APPROVAL_CONFIG)
        approval_state = layout.host(APPROVAL_STATE)
        setup_marker = layout.host(SETUP_MARKER)
        scanner_signing_key_path = layout.host(SCANNER_SIGNING_KEY)
        scanner_worker_config_path = layout.host(SCANNER_WORKER_CONFIG)
        setup_attempt = layout.host(SETUP_ATTEMPT)
        journal_path = layout.host(SETUP_UPGRADE_JOURNAL)
        core_env_path = layout.host(CORE_ENV)
        approval_env_path = layout.host(APPROVAL_ENV)

        locked_preflight = _server_setup_preflight(request, layout=layout)
        if locked_preflight.request_digest != approved_digest:
            raise ServerSetupError("request_changed", "setup request, inputs, or runtime changed after approved preflight")
        preflight = locked_preflight
        plan = _planned_setup_evidence(request, preflight)
        node_executable = preflight.runtime.node_executable
        uv_executable = preflight.runtime.uv_executable
        executable = preflight.runtime.agentnet_executable
        systemctl_executable = preflight.runtime.systemctl_executable
        useradd_executable = preflight.runtime.useradd_executable
        input_bundle = preflight.input_bundle
        scanner_setup = preflight.scanner_setup
        oidc_provider = preflight.oidc_provider
        owner_oidc = preflight.owner_oidc
        approvers = preflight.approvers
        scanner_trust = preflight.scanner_trust
        existing_marker_payload = _read_setup_marker(setup_marker, uid=root_uid, gid=root_gid)
        existing_marker = _validated_setup_marker(
            existing_marker_payload,
            request_digest=approved_digest,
            legacy_request_digest=preflight.legacy_request_digest,
            artifact_mode=(
                request.effective_artifact_mode
                if request.schema_version == "agentnet.server-setup.request.v2"
                else None
            ),
        )
        upgrade_profile = (
            _marker_upgrade_unit_profile(existing_marker)
            if existing_marker is not None
            else None
        )
        topology_expansion = upgrade_profile == LEGACY_COMMUNICATION_ONLY_UNITS
        forward_only_upgrade = (
            existing_marker is not None
            and _forward_only_setup_upgrade(
                existing_marker.get("package_version"),
                __version__,
            )
        )
        journal_present = journal_path.exists() or journal_path.is_symlink()
        journal_preview = (
            _read_upgrade_journal(journal_path, uid=root_uid, gid=root_gid)
            if journal_present
            else None
        )
        journaled_topology = (
            journal_preview is not None
            and any(
                payload is None
                for payload in _journaled_unit_payloads(journal_preview).values()
            )
        )
        topology_transition = topology_expansion or journaled_topology
        journaled_forward_only_upgrade = (
            journal_preview is not None
            and _forward_only_setup_upgrade(
                journal_preview.get("from_package_version"),
                journal_preview.get("to_package_version"),
            )
        )
        forward_only_transition = (
            forward_only_upgrade or journaled_forward_only_upgrade
        )
        if topology_transition:
            if (
                c0_responder_data.exists()
                or c0_responder_data.is_symlink()
                or layout.host(CREDENTIAL_RENEW_STATE).exists()
                or layout.host(CREDENTIAL_RENEW_STATE).is_symlink()
                or _account_fact(C0_RESPONDER_USER, C0_RESPONDER_DATA) != "create"
            ):
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "legacy communication-only state has unexpected target-only state",
                )
            for unit in set(MANAGED_UNITS) - set(LEGACY_COMMUNICATION_ONLY_UNITS):
                _require_absent_topology_upgrade_unit(
                    layout,
                    systemctl_executable,
                    unit,
                    journaled=journal_present,
                )
        for unit in MANAGED_UNITS:
            _require_no_unit_overrides(layout, unit)
        core_input = input_bundle["core_environment_file"]
        approval_input = input_bundle["approval_environment_file"]
        core_values = preflight.core_values
        approval_values = preflight.approval_values
        if topology_transition:
            if (
                _account_fact(CORE_USER, CORE_DATA) != "already_satisfied"
                or _account_fact(APPROVAL_USER, APPROVAL_DATA)
                != "already_satisfied"
                or not core_data.is_dir()
                or core_data.is_symlink()
                or not approval_data.is_dir()
                or approval_data.is_symlink()
                or not layout.host(SECRET_ROOT).is_dir()
                or layout.host(SECRET_ROOT).is_symlink()
                or _read_managed_exact(
                    core_env_path,
                    uid=root_uid,
                    gid=root_gid,
                    mode=0o600,
                    blocker="setup_upgrade_conflict",
                    label="legacy Core environment",
                    max_bytes=_MAX_CONFIG_BYTES,
                )
                != core_input
                or _read_managed_exact(
                    approval_env_path,
                    uid=root_uid,
                    gid=root_gid,
                    mode=0o600,
                    blocker="setup_upgrade_conflict",
                    label="legacy Approval environment",
                    max_bytes=_MAX_CONFIG_BYTES,
                )
                != approval_input
            ):
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "legacy communication-only prerequisite state is incomplete",
                )
        core_account = _ensure_account(
            CORE_USER,
            CORE_DATA,
            useradd_executable=useradd_executable,
        )
        postgres_evidence = _postgres_peer_gate(core_account, request.database_url)
        approval_account = _ensure_account(
            APPROVAL_USER,
            APPROVAL_DATA,
            useradd_executable=useradd_executable,
        )
        c0_responder_account = (
            None
            if topology_transition
            else _ensure_account(
                C0_RESPONDER_USER,
                C0_RESPONDER_DATA,
                useradd_executable=useradd_executable,
            )
        )
        steps: list[dict[str, Any]] = [
            {"id": "preflight", "status": "completed"},
            {"id": "core_identity", "status": "completed"},
            {"id": "postgres_service_identity", "status": postgres_evidence["status"]},
            {"id": "approval_identity", "status": "completed"},
        ]
        if c0_responder_account is not None:
            steps.append({"id": "c0_responder_identity", "status": "completed"})
        steps.append({"id": "core_private_root", "status": _ensure_private_root(core_data, core_account)})
        steps.append({"id": "approval_private_root", "status": _ensure_private_root(approval_data, approval_account)})
        if c0_responder_account is not None:
            steps.append(
                {
                    "id": "c0_responder_private_root",
                    "status": _ensure_private_root(
                        c0_responder_data,
                        c0_responder_account,
                    ),
                }
            )
        approval_preexisting = _private_entry_exists(
            approval_config_path,
            approval_account,
            expected="file",
            blocker="approval_config",
        )
        approval_state_preexisting = _private_entry_exists(
            approval_state,
            approval_account,
            expected="directory",
            blocker="approval_custody",
        )
        core_preexisting = _private_entry_exists(
            core_config_path,
            core_account,
            expected="file",
            blocker="core_custody",
        )
        c0_responder_config_preexisting = (
            False
            if c0_responder_account is None
            else _private_entry_exists(
                c0_responder_config_path,
                c0_responder_account,
                expected="file",
                blocker="c0_responder_custody",
            )
        )
        c0_responder_terminal_preexisting = (
            False
            if c0_responder_account is None
            else _private_entry_exists(
                c0_responder_terminal_path,
                c0_responder_account,
                expected="file",
                blocker="c0_responder_custody",
            )
        )
        core_runtime = core_data / "core"
        core_oidc_path = layout.host(CORE_OIDC_CONFIG)
        if request.effective_artifact_mode == "disabled":
            _require_communication_only_artifact_absence(core_runtime)
        core_runtime_preexisting = _private_entry_exists(
            core_runtime,
            core_account,
            expected="directory",
            blocker="core_custody",
        )
        unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
        preexisting_managed_state = any(
            (
                approval_preexisting,
                approval_state_preexisting,
                core_preexisting,
                core_runtime_preexisting,
                c0_responder_config_preexisting,
                c0_responder_terminal_preexisting,
                *(path.exists() or path.is_symlink() for path in unit_paths.values()),
            )
        )
        if (
            existing_marker is None
            and preexisting_managed_state
            and not (setup_attempt.exists() or setup_attempt.is_symlink())
        ):
            raise ServerSetupError(
                "clean_state_required",
                "pre-existing AgentNet state has no current-package setup custody",
            )
        steps.append(
            {
                "id": "setup_marker_root",
                "status": _ensure_root_private_directory(
                    setup_marker.parent,
                    uid=root_uid,
                    gid=root_gid,
                    label="setup_marker",
                ),
            }
        )
        if approval_state_preexisting:
            _require_private_tree(approval_state, approval_account, blocker="approval_custody")
        if core_runtime_preexisting:
            _require_private_tree(core_runtime, core_account, blocker="core_custody")
        prevalidated_oidc: OIDCEnrollmentConfig | None = None
        prevalidated_config: Any | None = None
        legacy_owner_policy = False
        if approval_preexisting and core_preexisting:
            approval_config_before, trusted_before = _approval_trust(
                approval_config_path,
                approval_account,
                approval_state,
            )
            _require_exact_approval_policy(
                approval_config_before,
                request=request,
                owner_oidc=owner_oidc,
                approvers=approvers,
                approval_state=approval_state,
            )
            prevalidated_oidc = _build_core_oidc_config(
                request,
                oidc_provider,
                trusted=trusted_before,
                approvers=approvers,
            )
            prevalidated_config, legacy_owner_policy = _load_upgrade_compatible_core_config(
                core_config_path,
                core_oidc_path,
                core_account,
                request=request,
                core_data=core_data,
                oidc=prevalidated_oidc,
                scanner_trust=scanner_trust,
            )
        upgrade_status = _prepare_supported_upgrade(
            existing_marker=existing_marker,
            existing_marker_payload=existing_marker_payload,
            approved_digest=approved_digest,
            approval_config_path=approval_config_path,
            approval_account=approval_account,
            approval_preexisting=approval_preexisting,
            core_config_path=core_config_path,
            core_oidc_path=core_oidc_path,
            core_account=core_account,
            core_preexisting=core_preexisting,
            database_url=request.database_url,
            domain_id=request.domain_id,
            enrolled_harness_id=(
                prevalidated_config.enrolled_harness_id
                if prevalidated_config is not None
                else None
            ),
            enrolled_credential_id=(
                prevalidated_config.enrolled_credential_id
                if prevalidated_config is not None
                else None
            ),
            profile_key=request.runtime_instance_id,
            systemctl_executable=systemctl_executable,
            unit_paths=unit_paths,
            marker_path=setup_marker,
            journal_path=journal_path,
            uid=root_uid,
            gid=root_gid,
            pending=_pending_upgrade,
        )
        steps.append({"id": "package_upgrade", "status": upgrade_status})
        attempt_status, attempt_active = _prepare_setup_attempt(
            setup_attempt,
            existing_marker=existing_marker,
            preexisting_state=preexisting_managed_state,
            request_digest=approved_digest,
            uid=root_uid,
            gid=root_gid,
        )
        steps.append({"id": "setup_attempt", "status": attempt_status})
        if scanner_setup is not None:
            scanner_config_payload = (
                json.dumps(
                    {
                        "endpoint": scanner_setup.endpoint.uri,
                        "engine_version": scanner_setup.engine_version,
                        "key_file": str(SCANNER_SIGNING_KEY),
                        "profile_digest": scanner_setup.profile_digest,
                        "rules_digest": scanner_setup.rules_digest,
                        "scanner_id": scanner_setup.scanner_id,
                        "scanner_key_epoch": scanner_setup.scanner_key_epoch,
                        "schema": "agentnet.scanner-worker.config.v1",
                        "signature_max_age_seconds": (
                            scanner_setup.signature_max_age_seconds
                        ),
                        "signature_updated_at": scanner_setup.signature_updated_at,
                        "signature_version": scanner_setup.signature_version,
                    },
                    sort_keys=True,
                    separators=(",", ":"),
                ).encode()
                + b"\n"
            )
            steps.append(
                {
                    "id": "scanner_signing_key_custody",
                    "status": _atomic_write(
                        scanner_signing_key_path,
                        scanner_setup.key_input,
                        mode=0o600,
                        uid=core_account.pw_uid,
                        gid=core_account.pw_gid,
                    ),
                }
            )
            steps.append(
                {
                    "id": "scanner_worker_config",
                    "status": _atomic_write(
                        scanner_worker_config_path,
                        scanner_config_payload,
                        mode=0o600,
                        uid=core_account.pw_uid,
                        gid=core_account.pw_gid,
                    ),
                }
            )
            steps.append(
                {
                    "id": "scanner_readiness",
                    "status": _require_scanner_readiness(scanner_setup)["status"],
                }
            )
        else:
            steps.append(
                {
                    "id": "scanner_readiness",
                    "status": "not_configured_file_capability_disabled",
                }
            )
        journaled_units = (
            _journaled_unit_payloads(_pending_upgrade["journal"])
            if _pending_upgrade.get("journal") is not None
            else {}
        )

        def write_setup_units() -> dict[str, bytes]:
            unit_payloads = render_units(
                node_executable,
                executable,
                uv_executable,
            )
            if _pending_upgrade.get("journal") is not None:
                _pending_upgrade["replacement_units"] = dict(unit_payloads)
            for unit, payload in unit_payloads.items():
                steps.append(
                    {
                        "id": f"unit:{unit}",
                        "status": _write_managed_unit(
                            unit_paths[unit],
                            payload,
                            uid=root_uid,
                            gid=root_gid,
                            previous=journaled_units.get(unit),
                        ),
                    }
                )
            return unit_payloads

        def commit_setup_profile(
            unit_payloads: dict[str, bytes] | None = None,
        ) -> dict[str, bytes]:
            """Commit exact managed files and marker, then cross the boundary."""

            if unit_payloads is None:
                unit_payloads = write_setup_units()
            approval_config_digest = _managed_config_digest(
                approval_config_path,
                approval_account,
                blocker="approval_config",
            )
            core_config_digest = _managed_config_digest(
                core_config_path,
                core_account,
                blocker="core_custody",
                exclude_top_level=frozenset(
                    {"enrolled_harness_id", "enrolled_credential_id"}
                ),
            )
            artifact_mode = (
                request.effective_artifact_mode
                if request.schema_version == "agentnet.server-setup.request.v2"
                else None
            )
            try:
                marker_status = _commit_setup_marker(
                    setup_marker,
                    existing_payload=existing_marker_payload,
                    existing_marker=existing_marker,
                    request_digest=approved_digest,
                    approval_config_digest=approval_config_digest,
                    core_config_digest=core_config_digest,
                    unit_payloads=unit_payloads,
                    artifact_mode=artifact_mode,
                    uid=root_uid,
                    gid=root_gid,
                )
            except BaseException:
                # A directory-fsync or response failure may occur after the
                # compare-and-swap became observable.  Never restore source
                # files over an exact target marker: retain the journal and
                # force the next invocation through committed-marker recovery.
                if forward_only_upgrade and existing_marker_payload is not None:
                    try:
                        observed_payload = _read_setup_marker(
                            setup_marker,
                            uid=root_uid,
                            gid=root_gid,
                        )
                        observed_marker = _validated_setup_marker(
                            observed_payload,
                            request_digest=approved_digest,
                            legacy_request_digest=preflight.legacy_request_digest,
                            artifact_mode=artifact_mode,
                        )
                        if (
                            observed_marker is None
                            or observed_marker.get("package_version") != __version__
                            or observed_marker.get("revision")
                            != int(existing_marker.get("revision", 0)) + 1
                            or observed_marker.get("previous_marker_digest")
                            != hashlib.sha256(existing_marker_payload).hexdigest()
                        ):
                            raise ServerSetupError(
                                "setup_upgrade_conflict",
                                "setup marker commit outcome is not the exact upgrade target",
                            )
                        _require_marker_realized_state(
                            observed_marker,
                            approval_config_digest=approval_config_digest,
                            core_config_digest=core_config_digest,
                            unit_paths=unit_paths,
                            uid=root_uid,
                            gid=root_gid,
                        )
                    except Exception:
                        pass
                    else:
                        _pending_upgrade.clear()
                raise
            # The marker records only managed config/unit provenance.  Crossing
            # it disarms byte rollback; database/bootstrap, target identity, and
            # service reconciliation after this point resume forward on retry.
            _pending_upgrade.clear()
            steps.append({"id": "setup_marker", "status": marker_status})
            if not forward_only_upgrade:
                _clear_upgrade_journal(journal_path)
            if attempt_active:
                _clear_upgrade_journal(setup_attempt)
            return unit_payloads

        if legacy_owner_policy:
            if prevalidated_oidc is None:  # pragma: no cover - guarded above
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "legacy owner policy was not prevalidated",
                )
            steps.append(
                {
                    "id": "core_remote_activation_policy_upgrade",
                    "status": _migrate_legacy_remote_activation_policy(
                        core_config_path=core_config_path,
                        core_oidc_path=core_oidc_path,
                        core_account=core_account,
                        oidc=prevalidated_oidc,
                        pending=_pending_upgrade,
                    ),
                }
            )
        secret_root = layout.host(SECRET_ROOT)
        steps.append(
            {
                "id": "secret_root",
                "status": _ensure_root_private_directory(
                    secret_root,
                    uid=root_uid,
                    gid=root_gid,
                    label="secret",
                ),
            }
        )
        steps.append({"id": "core_environment", "status": _atomic_write(core_env_path, core_input, mode=0o600, uid=root_uid, gid=root_gid)})
        steps.append({"id": "approval_environment", "status": _atomic_write(approval_env_path, approval_input, mode=0o600, uid=root_uid, gid=root_gid)})

        approval_environment = preflight.approval_environment
        if not approval_preexisting:
            staging_root = layout.host(Path("/run"))
            staging_root.mkdir(parents=True, exist_ok=True)
            staging = Path(tempfile.mkdtemp(prefix="agentnet-approval-setup-", dir=staging_root))
            os.chown(staging, approval_account.pw_uid, approval_account.pw_gid)
            os.chmod(staging, 0o700)
            try:
                approvers_copy = staging / "approvers.json"
                owner_oidc_copy = staging / "owner-oidc.json"
                _atomic_write(
                    approvers_copy,
                    input_bundle["approval_approvers_file"],
                    mode=0o600,
                    uid=approval_account.pw_uid,
                    gid=approval_account.pw_gid,
                )
                _atomic_write(
                    owner_oidc_copy,
                    input_bundle["approval_owner_oidc_file"],
                    mode=0o600,
                    uid=approval_account.pw_uid,
                    gid=approval_account.pw_gid,
                )
                result = _run_as(
                    approval_account,
                    [
                        str(node_executable), str(executable), "approval", "provision",
                        "--config", str(approval_config_path),
                        "--data-dir", str(approval_state),
                        "--public-origin", request.approval_public_origin,
                        "--rp-id", str(urlsplit(request.approval_public_origin).hostname),
                        "--verifier-id", request.approval_verifier_id,
                        "--approvers", str(approvers_copy),
                        "--owner-oidc-config", str(owner_oidc_copy),
                        "--internal-core-credential-env", "AGENTNET_APPROVAL_CORE_TOKEN",
                    ],
                    environment=approval_environment,
                    stage="approval_provision",
                )
                if result.get("schema") != "agentnet.approval.provision-result.v1":
                    raise ServerSetupError("approval_evidence", "Approval provision evidence schema is invalid")
                _require_private_tree(approval_state, approval_account, blocker="approval_custody")
                steps.append({"id": "approval_provision", "status": "completed"})
            finally:
                shutil.rmtree(staging, ignore_errors=True)
        else:
            _require_private_file(approval_config_path, approval_account, blocker="approval_config")

        approval_config, trusted = _approval_trust(
            approval_config_path,
            approval_account,
            approval_state,
        )
        _require_exact_approval_policy(
            approval_config,
            request=request,
            owner_oidc=owner_oidc,
            approvers=approvers,
            approval_state=approval_state,
        )
        if approval_preexisting:
            _run_as(
                approval_account,
                [str(node_executable), str(executable), "approval", "status", "--config", str(approval_config_path)],
                environment=approval_environment,
                stage="approval_status",
            )
            steps.append({"id": "approval_provision", "status": "already_satisfied"})
        oidc = _build_core_oidc_config(
            request,
            oidc_provider,
            trusted=trusted,
            approvers=approvers,
        )
        if prevalidated_oidc is not None and oidc != prevalidated_oidc:
            raise ServerSetupError(
                "approval_conflict",
                "Approval trust changed during setup",
            )
        oidc_path = core_oidc_path
        oidc_payload = json.dumps(oidc.model_dump(mode="json"), indent=2, sort_keys=True).encode() + b"\n"
        steps.append({"id": "core_oidc_config", "status": _atomic_write(oidc_path, oidc_payload, mode=0o600, uid=core_account.pw_uid, gid=core_account.pw_gid)})
        scanner_path = core_data / "scanner-trust.json"
        if scanner_trust is not None:
            scanner_payload = json.dumps(
                scanner_trust.model_dump(mode="json"), indent=2, sort_keys=True
            ).encode() + b"\n"
            steps.append(
                {
                    "id": "scanner_trust",
                    "status": _atomic_write(
                        scanner_path,
                        scanner_payload,
                        mode=0o600,
                        uid=core_account.pw_uid,
                        gid=core_account.pw_gid,
                    ),
                }
            )
        else:
            if scanner_path.exists() or scanner_path.is_symlink():
                raise ServerSetupError(
                    "core_conflict",
                    "communication-only Core state contains forbidden scanner trust",
                )
            _require_communication_only_artifact_absence(core_runtime)
            steps.append({"id": "scanner_trust", "status": "disabled_not_created"})

        core_environment = preflight.core_environment
        if not core_preexisting:
            core_create_arguments = _core_create_arguments(
                request,
                node_executable=node_executable,
                executable=executable,
                core_config_path=core_config_path,
                core_data=core_data,
                oidc_path=oidc_path,
                scanner_path=scanner_path,
                scanner_trust=scanner_trust,
            )
            result = _run_as(
                core_account,
                core_create_arguments,
                environment=core_environment,
                stage="core_create",
                accepted_returncodes=frozenset({1}),
            )
            _require_private_file(core_config_path, core_account, blocker="core_custody")
            _require_core_create_evidence(
                result,
                core_config_path,
                artifact_mode=request.effective_artifact_mode,
            )
            bootstrap_status = "completed"
        else:
            _require_private_file(core_config_path, core_account, blocker="core_custody")
            bootstrap_status = "revalidated"

        _require_private_tree(core_runtime, core_account, blocker="core_custody")
        config = _load_validated_core_config(
            core_config_path,
            core_account,
            request=request,
            core_data=core_data,
            oidc=oidc,
            scanner_trust=scanner_trust,
        )
        profile_committed_early = False
        rollback_capable_upgrade = (
            _pending_upgrade.get("rollback_capable_upgrade") is True
        )
        endpoint_lifecycle_result: dict[str, Any] | None = None
        unit_payloads: dict[str, bytes] | None = None
        if rollback_capable_upgrade:
            unit_payloads = write_setup_units()
        elif forward_only_upgrade:
            unit_payloads = commit_setup_profile()
            profile_committed_early = True
        if forward_only_transition:
            def verify_upgrade_quiescence() -> None:
                expected_states = {
                    APPROVAL_UNIT: "disabled",
                    CORE_UNIT: "disabled",
                    C0_RESPONDER_UNIT: "disabled",
                    CREDENTIAL_RENEW_UNIT: "static",
                    CREDENTIAL_RENEW_TIMER: "disabled",
                }
                for unit, expected_state in expected_states.items():
                    _validate_inactive_auxiliary_unit_state(
                        unit=unit,
                        expected_unit_file_state=expected_state,
                        properties=_systemd_show(systemctl_executable, unit),
                    )

            if rollback_capable_upgrade:
                _pending_upgrade["service_state_changed"] = True
            quiesce_status = _run_systemctl_sequence_or_reconcile(
                systemctl_executable,
                [
                    ["daemon-reload"],
                    ["disable", "--now", C0_RESPONDER_UNIT],
                    ["reset-failed", C0_RESPONDER_UNIT],
                    ["disable", "--now", CREDENTIAL_RENEW_TIMER],
                    ["reset-failed", CREDENTIAL_RENEW_TIMER],
                    ["stop", CREDENTIAL_RENEW_UNIT],
                    ["reset-failed", CREDENTIAL_RENEW_UNIT],
                    ["disable", "--now", CORE_UNIT],
                    ["reset-failed", CORE_UNIT],
                    ["disable", "--now", APPROVAL_UNIT],
                    ["reset-failed", APPROVAL_UNIT],
                ],
                reconcile=verify_upgrade_quiescence,
            )
            if quiesce_status == "completed":
                verify_upgrade_quiescence()
            steps.append(
                {
                    "id": "package_upgrade_service_quiescence",
                    "status": quiesce_status,
                }
            )
        if rollback_capable_upgrade:
            journal = _pending_upgrade.get("journal")
            if not isinstance(journal, Mapping):
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "v0.1.45 migration requires its exact upgrade journal",
                )
            source = _validated_v0145_database_snapshot(
                journal.get("previous_database")
            )
            identity = source["identity"]
            migration_evidence = _run_v0145_database_operation_as(
                core_account,
                request.database_url,
                operation="migrate",
                source=source,
                domain_id=str(identity["domain_id"]),
                harness_id=str(identity["harness_id"]),
                credential_id=str(identity["credential_id"]),
                profile_key=str(identity["profile_key"]),
            )
            endpoint_row = migration_evidence.get("endpoint_lifecycle")
            if (
                not isinstance(endpoint_row, dict)
                or endpoint_row.get("harness_id") != identity["harness_id"]
                or endpoint_row.get("state") != "restart_required"
            ):
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "v0.1.45 migration did not prove the exact endpoint lifecycle",
                )
            endpoint_lifecycle_result = {
                "endpoint_id": str(identity["harness_id"]),
                "state": "restart_required",
                "public_url": request.core_public_origin,
                "identity_created": False,
            }
            steps.append(
                {
                    "id": "schema_v7_endpoint_lifecycle",
                    "status": "restart_required",
                }
            )
        if core_preexisting and not rollback_capable_upgrade:
            _, bootstrap_status = _run_bootstrap_idempotently(
                core_account,
                [str(node_executable), str(executable), "bootstrap-server-agent", "--config", str(core_config_path)],
                environment=core_environment,
                expected_domain_id=request.domain_id,
            )
            if bootstrap_status == "completed":
                bootstrap_status = "revalidated"
            _require_private_tree(core_runtime, core_account, blocker="core_custody")
            config = _load_validated_core_config(
                core_config_path,
                core_account,
                request=request,
                core_data=core_data,
                oidc=oidc,
                scanner_trust=scanner_trust,
            )
        elif rollback_capable_upgrade:
            bootstrap_status = "schema_v7_migrated_preserved_identity"
        if forward_only_transition and not rollback_capable_upgrade:
            _clear_upgrade_journal(journal_path)
        if c0_responder_account is None:
            c0_responder_account = _ensure_account(
                C0_RESPONDER_USER,
                C0_RESPONDER_DATA,
                useradd_executable=useradd_executable,
            )
            steps.append({"id": "c0_responder_identity", "status": "completed"})
            steps.append(
                {
                    "id": "c0_responder_private_root",
                    "status": _ensure_private_root(
                        c0_responder_data,
                        c0_responder_account,
                    ),
                }
            )
        identity_enrolled = bool(config.enrolled_harness_id and config.enrolled_credential_id)
        c0_responder_required = False
        responder_payload: bytes | None = None
        steps.append({"id": "core_bootstrap", "status": bootstrap_status})
        if identity_enrolled and start:
            identity_path = layout.host(SERVER_AGENT_IDENTITY)
            signing_key_path = layout.host(SERVER_AGENT_KEY)
            _validated_managed_identity_profile(
                identity_path,
                signing_key_path,
                core_account,
                config=config,
                request=request,
            )
            _verified["identity_enrolled"] = True
            terminal = _validated_c0_terminal_marker(
                c0_responder_terminal_path,
                c0_responder_account,
                config=config,
            )
            if terminal is not None:
                if c0_responder_config_path.exists() or c0_responder_config_path.is_symlink():
                    _require_private_file(
                        c0_responder_config_path,
                        c0_responder_account,
                        blocker="c0_responder_terminal",
                    )
                    try:
                        c0_responder_config_path.unlink()
                        directory = os.open(
                            c0_responder_config_path.parent,
                            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0),
                        )
                        try:
                            os.fsync(directory)
                        finally:
                            os.close(directory)
                    except OSError as exc:
                        raise ServerSetupError(
                            "c0_responder_terminal",
                            "C0 responder terminal cleanup could not be reconciled",
                        ) from exc
                    responder_config_status = "terminal_cleanup_reconciled"
                else:
                    responder_config_status = "terminal_not_recreated"
                steps.append({"id": "c0_responder_config", "status": responder_config_status})
            else:
                c0_responder_required = True
                responder_payload = json.dumps(
                    {
                        "schema": "agentnet.c0-pilot-responder.config.v1",
                        "core_base_url": request.core_public_origin,
                        "audience": request.service_audience,
                        "domain_id": request.domain_id,
                        "harness_id": config.enrolled_harness_id,
                        "credential_id": config.enrolled_credential_id,
                        "poll_seconds": 2,
                        "max_consecutive_errors": 5,
                    },
                    indent=2,
                    sort_keys=True,
                ).encode() + b"\n"
        elif not identity_enrolled and any(
            path.exists() or path.is_symlink()
            for path in (c0_responder_config_path, c0_responder_terminal_path)
        ):
            raise ServerSetupError(
                "c0_responder_conflict",
                "C0 responder state exists before exact activated identity",
            )

        approval_config, trusted_after = _approval_trust(
            approval_config_path,
            approval_account,
            approval_state,
        )
        _require_exact_approval_policy(
            approval_config,
            request=request,
            owner_oidc=owner_oidc,
            approvers=approvers,
            approval_state=approval_state,
        )
        if trusted_after != trusted:
            raise ServerSetupError("approval_conflict", "Approval trust changed during setup")

        if unit_payloads is None:
            unit_payloads = commit_setup_profile()
        assert unit_payloads is not None
        if start:
            approval_health = {
                "schema": "agentnet.approval.health.v1",
                "service": "agentnet-approval",
                "version": __version__,
                "status": "alive",
                "public_origin": request.approval_public_origin,
                "verifier_id": request.approval_verifier_id,
            }
            core_health = {
                "schema": "agentnet.core.health.v1",
                "service": "agentnet-core",
                "version": __version__,
                "status": "alive",
                "profile": request.profile,
                "artifact_mode": request.effective_artifact_mode,
                "server_agent_capabilities": sorted(
                    capability.value
                    for capability in (
                        {ServerAgentCapability.OFFLINE_CUSTODY, ServerAgentCapability.ARTIFACT_STORAGE}
                        if request.effective_artifact_mode == "enabled"
                        else {ServerAgentCapability.OFFLINE_CUSTODY}
                    )
                ),
                "domain_id": request.domain_id,
                "public_origin": request.core_public_origin,
                "service_audience": request.service_audience,
                "runtime_instance_id": request.runtime_instance_id,
            }

            def verify_live_service_state(*, auxiliary_ready: bool) -> None:
                active_units = [APPROVAL_UNIT, CORE_UNIT]
                if auxiliary_ready and identity_enrolled:
                    active_units.append(CREDENTIAL_RENEW_TIMER)
                if auxiliary_ready and c0_responder_required:
                    active_units.append(C0_RESPONDER_UNIT)
                for unit in active_units:
                    _run_systemctl(
                        systemctl_executable,
                        ["is-active", "--quiet", unit],
                        failure_message="AgentNet managed unit is not active",
                    )
                _validate_systemd_service_runtime(
                    systemctl_executable,
                    unit=APPROVAL_UNIT,
                    user=APPROVAL_USER,
                    data_root=approval_data,
                    node_executable=node_executable,
                    agentnet_executable=executable,
                    uv_executable=uv_executable,
                    expected_argv=(
                        str(node_executable), str(executable), "approval", "serve",
                        "--config", str(approval_config_path),
                        "--host", "127.0.0.1", "--port", str(APPROVAL_PORT),
                    ),
                    layout=layout,
                )
                _validate_systemd_service_runtime(
                    systemctl_executable,
                    unit=CORE_UNIT,
                    user=CORE_USER,
                    data_root=core_data,
                    node_executable=node_executable,
                    agentnet_executable=executable,
                    uv_executable=uv_executable,
                    expected_argv=(
                        str(node_executable), str(executable), "serve",
                        "--config", str(core_config_path),
                        "--host", "127.0.0.1", "--port", str(CORE_PORT),
                    ),
                    layout=layout,
                )
                if auxiliary_ready and c0_responder_required:
                    _validate_systemd_service_runtime(
                        systemctl_executable,
                        unit=C0_RESPONDER_UNIT,
                        user=C0_RESPONDER_USER,
                        data_root=c0_responder_data,
                        node_executable=node_executable,
                        agentnet_executable=executable,
                        uv_executable=uv_executable,
                        expected_argv=(
                            str(node_executable), str(executable), "c0-pilot", "responder",
                            "--run", "--config", str(c0_responder_config_path),
                            "--credential", "/run/credentials/agentnet-c0-responder.service/signing-key.pem",
                        ),
                        layout=layout,
                    )
                expected_states = {CREDENTIAL_RENEW_UNIT: "static"}
                if not auxiliary_ready or not c0_responder_required:
                    expected_states[C0_RESPONDER_UNIT] = "disabled"
                if not auxiliary_ready or not identity_enrolled:
                    expected_states[CREDENTIAL_RENEW_TIMER] = "disabled"
                for unit, expected_state in expected_states.items():
                    _validate_inactive_auxiliary_unit_state(
                        unit=unit,
                        expected_unit_file_state=expected_state,
                        properties=_systemd_show(systemctl_executable, unit),
                    )
                if auxiliary_ready and identity_enrolled:
                    _validate_active_renewal_timer_state(
                        _systemd_show(systemctl_executable, CREDENTIAL_RENEW_TIMER),
                        next_run_usec=_systemd_timer_next_run(
                            systemctl_executable,
                            CREDENTIAL_RENEW_TIMER,
                        ),
                        now_usec=time.time_ns() // 1_000,
                    )
            base_systemctl_commands: list[list[str]] = [
                ["daemon-reload"],
                ["disable", "--now", CREDENTIAL_RENEW_TIMER],
                ["disable", "--now", C0_RESPONDER_UNIT],
                ["stop", CREDENTIAL_RENEW_UNIT],
                ["enable", "--now", APPROVAL_UNIT],
                ["enable", CORE_UNIT],
                ["restart", CORE_UNIT],
            ]
            base_start_status = _run_systemctl_sequence_or_reconcile(
                systemctl_executable,
                base_systemctl_commands,
                reconcile=lambda: verify_live_service_state(auxiliary_ready=False),
            )
            if base_start_status == "completed":
                verify_live_service_state(auxiliary_ready=False)
            _health(
                f"http://127.0.0.1:{APPROVAL_PORT}/healthz",
                expected=approval_health,
                attempts=_START_HEALTH_ATTEMPTS,
            )
            _health(
                f"http://127.0.0.1:{CORE_PORT}/healthz",
                expected=core_health,
                attempts=_START_HEALTH_ATTEMPTS,
            )
            _health(
                f"{request.approval_public_origin}/healthz",
                expected=approval_health,
                attempts=_START_HEALTH_ATTEMPTS,
            )
            _health(
                f"{request.core_public_origin}/healthz",
                expected=core_health,
                attempts=_START_HEALTH_ATTEMPTS,
            )
            if oidc.approval_service is None:  # pragma: no cover - fixed profile invariant
                raise ServerSetupError("approval_broker_auth", "Approval broker configuration is unavailable")
            broker_client: ApprovalServiceClient | None = None
            try:
                broker_client = ApprovalServiceClient(
                    oidc.approval_service,
                    core_values[_BROKER_CREDENTIAL_NAME],
                )
                broker_client.readiness()
            except GateBlocked as exc:
                blocker = (
                    exc.gate
                    if exc.gate in {"approval_broker_auth", "approval_broker_unavailable"}
                    else "approval_broker_auth"
                )
                raise ServerSetupError(blocker, "Approval broker readiness failed") from None
            finally:
                if broker_client is not None:
                    broker_client.close()
            steps.append({"id": "approval_broker_readiness", "status": "completed"})
            start_status = base_start_status
            if identity_enrolled:
                readiness = {
                    "schema": "agentnet.core.readiness.v1",
                    "service": "agentnet-core",
                    "version": __version__,
                    "ready": True,
                    "profile": request.profile,
                    "artifact_mode": request.effective_artifact_mode,
                    "server_agent_capabilities": sorted(
                        capability.value
                        for capability in (
                            {ServerAgentCapability.OFFLINE_CUSTODY, ServerAgentCapability.ARTIFACT_STORAGE}
                            if request.effective_artifact_mode == "enabled"
                            else {ServerAgentCapability.OFFLINE_CUSTODY}
                        )
                    ),
                    "domain_id": request.domain_id,
                    "public_origin": request.core_public_origin,
                    "service_audience": request.service_audience,
                    "runtime_instance_id": request.runtime_instance_id,
                    "deployment_binding": {
                        "ready": True,
                        "required": True,
                        "credential_state": ("current", "renewal_needed"),
                    },
                    "approval_broker": {"ready": True, "required": True},
                }
                _health(f"http://127.0.0.1:{CORE_PORT}/readyz", expected=readiness)
                try:
                    _health(
                        f"{request.core_public_origin}/readyz",
                        expected=readiness,
                        attempts=_START_HEALTH_ATTEMPTS,
                    )
                except ServerSetupError as exc:
                    raise ServerSetupError(
                        exc.blocker,
                        str(exc),
                        identity_enrolled=True,
                    ) from exc
                if c0_responder_required:
                    if responder_payload is None:  # pragma: no cover - fixed branch invariant
                        raise ServerSetupError(
                            "c0_responder_conflict",
                            "C0 responder configuration is unavailable",
                        )
                    steps.append(
                        {
                            "id": "c0_responder_config",
                            "status": _atomic_write(
                                c0_responder_config_path,
                                responder_payload,
                                mode=0o600,
                                uid=c0_responder_account.pw_uid,
                                gid=c0_responder_account.pw_gid,
                            ),
                        }
                    )
                auxiliary_commands = _credential_renewal_activation_commands(
                    c0_responder_required=c0_responder_required,
                )
                auxiliary_status = _run_systemctl_sequence_or_reconcile(
                    systemctl_executable,
                    auxiliary_commands,
                    reconcile=lambda: verify_live_service_state(
                        auxiliary_ready=True
                    ),
                )
                if auxiliary_status == "completed":
                    verify_live_service_state(auxiliary_ready=True)
                if auxiliary_status != "completed":
                    start_status = auxiliary_status
                steps.append({"id": "operational_readiness", "status": "completed"})
                status = "operational"
                next_action = "enroll additional ordinary laptops with agentnet join guided"
            else:
                status = "waiting_owner_oidc_or_passkey"
                next_action = "complete owner passkey registration and guided identity-only enrollment"
            steps.append({"id": "service_start", "status": start_status})
            steps.append(
                {
                    "id": "managed_unit_runtime",
                    "status": "validated_hermetic_live_binding",
                }
            )
            steps.append({"id": "public_https_routes", "status": "completed"})
        else:
            steps.append({"id": "service_start", "status": "pending_explicit_start"})
            status = "configured_not_started"
            next_action = "rerun with the same --expected-request-digest plus --apply --start inside the approved scope"
        if rollback_capable_upgrade:
            commit_setup_profile(unit_payloads)
            _clear_upgrade_journal(journal_path)
        return {
            **plan,
            "status": status,
            "steps": steps,
            "next": next_action,
            "authority_granted": False,
            "identity_enrolled": identity_enrolled,
            "endpoint_lifecycle": endpoint_lifecycle_result,
            "production_durability_proven": False,
        }
    finally:
        os.close(lock_descriptor)


__all__ = [
    "APPROVAL_PORT",
    "APPROVAL_UNIT",
    "CORE_PORT",
    "CORE_UNIT",
    "SECRET_ROOT",
    "SETUP_MARKER",
    "SETUP_ROOT",
    "SETUP_RUNTIME_ROOT",
    "SETUP_UPGRADE_JOURNAL",
    "SYSTEMD_UNIT_ROOT",
    "ServerSetupError",
    "ServerSetupRequest",
    "SetupLayout",
    "apply_server_setup",
    "load_server_setup_request",
    "plan_server_setup",
    "render_units",
]
