"""Setup request loading, validation, runtime resolution, and planning."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import time
from pathlib import Path
from typing import Any, Mapping
from urllib.parse import urlsplit

if os.name == "posix":
    import pwd

from agentnet import __version__
from agentnet.artifacts.clamav import (
    ClamAVScanner,
    ScannerEndpoint,
    clamav_profile_digest,
    clamav_rules_digest,
)
from agentnet.artifacts.scanner import ScannerTrustPolicy
from agentnet.approval.config import ApprovalOwnerOIDCConfig, MANDATORY_APPROVAL_PURPOSES
from agentnet.approval.internal_client import require_approval_tls_environment
from agentnet.errors import GateBlocked
from agentnet.operations.config import (
    ApprovalServiceClientConfig,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    ScannerTrustConfig,
)
from agentnet.security.signatures import P256KeyPair, canonical_digest, verify_signature
from agentnet.storage.postgres import (
    ORDINARY_SERVER_POSTGRES_DATABASE,
    ORDINARY_SERVER_POSTGRES_SOCKET,
    ORDINARY_SERVER_POSTGRES_USER,
)

from . import systemd as _systemd
from .models import (
    ScannerSetupSpec,
    ServerSetupError,
    ServerSetupPreflight,
    ServerSetupRequest,
    SetupApprover,
    SetupLayout,
    SetupOIDCProvider,
    SetupRuntimeIdentity,
)
from .systemd import (
    APPROVAL_DATA,
    APPROVAL_PORT,
    APPROVAL_USER,
    C0_RESPONDER_DATA,
    C0_RESPONDER_USER,
    CORE_DATA,
    CORE_PORT,
    CORE_USER,
)

SCANNER_SIGNING_KEY = CORE_DATA / "scanner-signing-key.pem"
_ENV_NAME = re.compile(r"^[A-Z_][A-Z0-9_]{0,127}$")
_ENV_VALUE = re.compile(r"""^[^\s'"\\\x00-\x1f\x7f]+$""")
_BROKER_CREDENTIAL_NAME = "AGENTNET_APPROVAL_CORE_TOKEN"
_BROKER_CREDENTIAL_MIN_LENGTH = 43
_BROKER_CREDENTIAL_MAX_LENGTH = 512
_SYSTEM_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
_PROTECTED_SERVICE_PATHS = (
    Path("/home"),
    Path("/root"),
    Path("/run/user"),
    Path("/tmp"),
    Path("/var/tmp"),
)
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
_MAX_CONFIG_BYTES = 1_048_576
_BASE_INPUT_FIELDS = (
    "core_environment_file",
    "approval_environment_file",
    "oidc_provider_file",
    "approval_owner_oidc_file",
    "approval_approvers_file",
)


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
    from .custody import _service_environment
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
    from .custody import _account_fact
    try:
        units = _systemd.render_managed_units(
            preflight.runtime.node_executable,
            preflight.runtime.agentnet_executable,
            preflight.runtime.uv_executable,
            package_version=__version__,
        )
    except _systemd.UnitRenderError as exc:
        raise ServerSetupError("unit_input", str(exc)) from exc
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
        "public_core_origin": request.core_public_origin,
        "laptop_join_command": (
            "npm install --global @misunders2d/agentnet@"
            + __version__
            + " --ignore-scripts --no-audit --no-fund && agentnet join guided --server "
            + request.core_public_origin
        ),
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
