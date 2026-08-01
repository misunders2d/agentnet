#!/usr/bin/env python3
"""Fail-closed verification for the local release evidence manifest.

This verifies reproducible local inputs only.  It deliberately cannot promote
external, privileged, owner, supply-chain, installer, or production gates.
"""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import importlib.util
import io
import json
import platform
import re
import stat
import sys
import tarfile
import tomllib
import zipfile
import xml.etree.ElementTree as ET

from pathlib import Path, PurePosixPath
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
MANIFEST_PATH = ROOT / "RELEASE_MANIFEST.json"

EXPECTED_SOURCES = {
    "concept": (
        "docs/specification.md",
        "1a6b90660a3bc9eb6c5e9a8ab1d004de5e89d73dfe7505f3997f90285a401b94",
    ),
    "requirements": (
        "docs/requirements.md",
        "bd95f53685be5a6188c8833414d3bebab07725f3dd5c0734dc2b6c8052cd27f1",
    ),
    "final_verification": (
        "docs/final-verification.md",
        "d359856efbe4e94c3b57e138b5ddb2927f233be10281538b06216af9d41fc466",
    ),
}

EXPECTED_PROTOCOLS = {
    "a2a": {
        "spec_release": "1.0.1",
        "wire_version": "1.0",
        "sdk_name": "a2a-sdk",
        "sdk_version": "1.1.0",
        "bindings": ["HTTP+JSON", "JSONRPC"],
    },
    "mcp": {
        "spec_version": "2025-11-25",
        "sdk_name": "mcp",
        "sdk_version": "1.28.1",
        "role": "optional_local_binding_only",
    },
}

EXPECTED_SCHEMA_NAMES = {
    "actor",
    "artifact-manifest",
    "audit-intent",
    "enrollment-transaction",
    "event",
    "federation-invitation",
    "identity",
    "independent-approval-receipt",
    "internal-invitation-acceptance",
    "internal-invitation-record",
    "internal-invitation-request",
    "internal-invitation-transaction",
    "presence",
    "protocol-error",
    "receipt",
    "relationship",
    "relationship-consent-transaction",
    "relationship-policy-exception",
    "revocation",
    "room",
    "provenance-reference",
    "task-conflict-adjudication",
    "task-conflict-outcome",
    "task-execution-intent",
    "task-grant",
}

EXPECTED_COMPONENT_STATUSES = {
    "canonical_schemas": "BUILD_OWN",
    "supervisor_harness_lifecycle": "BUILD_OWN_INTEGRATION",
    "a2a_gateway": "ACCEPT_BASELINE_PRODUCTION_GATE_OPEN",
    "a2a_internal_fabric": "REJECT_AS_CORE",
    "agntcy_oasf": "NOT_SELECTED_EXTERNAL_BAKEOFF_OPEN",
    "agntcy_directory": "NOT_SELECTED_EXTERNAL_BAKEOFF_OPEN",
    "agntcy_slim": "NOT_SELECTED_BASELINE_COMPARATOR_ONLY",
    "matrix_components": "REJECT_AS_AUTHORITY_COMPONENT",
    "mls": "C3_DISABLED_EXTERNAL_MLS_OWNER_BLOCKED",
    "spiffe_spire": "REGISTERED_WORKLOAD_BOUNDARY_BUILT_EXTERNAL_SPIFFE_OPEN",
    "human_auth_approval": "OIDC_WEBAUTHN_COMPONENT_BUILT_EXTERNAL_OWNER_BLOCKED",
    "cedar": "OPTIONAL_TARGET_RUNTIME_NOT_ENABLED",
    "postgresql": "MULTI_HOST_PRIMARY_RECONNECT_FENCED_LOCAL_HA_EXTERNAL",
    "artifact_store": "PERSISTENT_BYTE_QUOTA_FILESYSTEM_POSTGRES_MANIFEST_RESTORE_EXTERNAL",
    "file_safety": "LOCAL_PREFILTER_QUARANTINE_ATTESTATION_SCANNER_EXTERNAL",
    "mcp_local_binding": "PARENT_BOUND_MCP_AND_PI_CAPABILITY_COMPOSED_INTEROP_EXTERNAL",
    "clean_worker_launcher": "DETERMINISTIC_INSTALLED_TESTED_SEMANTIC_EXTERNAL",
    "cryptographic_primitives": "REUSE_REQUIRED_PROFILE_GATE_OPEN",
    "temporal_style_workflow": "EXPLICIT_EFFECT_LIFECYCLE_COMPOSED",
    "audit_witness": "HASH_CHAIN_AND_RELEASE_INTENT_BUILT_WITNESS_EXTERNAL",
    "future_hubless_custody": "ONE_HOP_PEER_RELAY_COMPOSED_DISTRIBUTED_AUTHORITY_DISABLED",
}

EXPECTED_GATE_STATUSES = {
    "G01": "BLOCKED_EXTERNAL",
    "G02": "BLOCKED_EXTERNAL",
    "G03": "BLOCKED_EXTERNAL",
    "G04": "FAILED",
    "G05": "BLOCKED_EXTERNAL",
    "G06": "BLOCKED_OWNER",
    "G07": "BLOCKED_EXTERNAL",
    "G08": "BLOCKED_OWNER",
    "G09": "BLOCKED_EXTERNAL",
    "G10": "PARTIAL",
    "G11": "BLOCKED_OWNER",
    "G12": "BLOCKED_OWNER",
    "G13": "BLOCKED_OWNER",
    "G14": "BLOCKED_EXTERNAL",
    "G15": "BLOCKED_EXTERNAL",
    "G16": "BLOCKED_OWNER",
    "G17": "BLOCKED_OWNER",
    "G18": "BLOCKED_EXTERNAL",
    "G19": "BLOCKED_OWNER",
}

EXPECTED_RELEASE_INPUT_PATHS = {
    "README.md",
    "REQUIREMENTS_STATUS.md",
    "deploy/Dockerfile",
    "deploy/compose.production.json",
    "deploy/nginx-agent.conf",
    "deploy/render_and_run.py",
    "docs/GATE_EVIDENCE.md",
    "docs/RELEASE_MANIFEST.md",
    "evidence/gates/G01/2026-07-13-installed-harnesses/manifest.json",
    "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
    "evidence/gates/G04/2026-07-13-alpha2-http-json/manifest.json",
    "evidence/gates/G09/2026-07-13-postgresql-18.4-local/manifest.json",
    "scripts/export_schemas.py",
    "scripts/verify_release.py",
}

EXPECTED_EXTERNAL_EVIDENCE = {
    "sbom",
    "provenance",
    "signature",
    "installer_lifecycle",
}

EXPECTED_STORAGE_SCHEMA_VERSION = 1

EXPECTED_SDIST_ONLY_INCLUDE = (
    "LICENSE",
    "README.md",
    "RELEASE_MANIFEST.json",
    "REQUIREMENTS_STATUS.md",
    "deploy/.env.production.example",
    "deploy/Dockerfile",
    "deploy/compose.production.json",
    "deploy/nginx-agent.conf",
    "deploy/render_and_run.py",
    "docs/ARCHITECTURE.md",
    "docs/BAKEOFF_PLAN.md",
    "docs/BUILD_VS_REUSE.md",
    "docs/GATE_EVIDENCE.md",
    "docs/MILESTONES.md",
    "docs/OWNER_DECISIONS.md",
    "docs/RELEASE_MANIFEST.md",
    "docs/SCHEMAS_INTERFACES.md",
    "docs/THREAT_MODEL_TEST_PLAN.md",
    "docs/ZERO_STATE_C0_PILOT.md",
    "docs/final-verification.md",
    "docs/implementation-guide.md",
    "docs/requirements.md",
    "docs/response-obligations.md",
    "docs/specification.md",
    "pyproject.toml",
    "schemas/README.md",
    "schemas/v1",
    "scripts/export_schemas.py",
    "scripts/verify_release.py",
    "src/agentnet",
    "uv.lock",
)

EXPECTED_SDIST_STATIC_FILES = frozenset(
    path
    for path in EXPECTED_SDIST_ONLY_INCLUDE
    if path not in {"schemas/v1", "src/agentnet"}
)

# Hatchling 1.28.0 always includes root VCS ignore metadata in sdists and
# explicitly does not allow excluding it. Keep its exact expected bytes here so
# installed npm generations can verify the retained sdist without requiring a
# root .gitignore or .npmignore as a runtime/package input.
EXPECTED_HATCH_SDIST_VCS_METADATA = {
    ".gitignore": (
        b".venv/\n.pytest_cache/\n.hypothesis/\n__pycache__/\n*.py[cod]\n"
        b"*.sqlite3\n*.sqlite3-*\n.agentnet/\ndist/\nbuild/\n*.egg-info/\n"
        b"coverage.xml\n.coverage\n\n"
    )
}

EXPECTED_WHEEL_ENTRY_POINTS = b"[console_scripts]\nagentnet = agentnet.cli:main\n"
EXPECTED_WHEEL_METADATA = (
    b"Wheel-Version: 1.0\n"
    b"Generator: hatchling 1.28.0\n"
    b"Root-Is-Purelib: true\n"
    b"Tag: py3-none-any\n"
)
REPRODUCIBLE_TIMESTAMP = 1_580_601_600

# Historical product constants used uppercase ACE_* names. Lowercase ACL
# vocabulary such as `ace_type` is generic security terminology, not a legacy
# product namespace, and must not make otherwise-valid source unreleasable.
_FORBIDDEN_PRODUCT_NAMESPACE = re.compile(
    rb"(?<![A-Za-z0-9_])(?:ACE_[A-Z0-9_]+|(?i:agentic[_-]communication|"
    rb"ace\.[a-z0-9_.-]+|\.ace(?:[/\\]|$)|ace(?:\s*=|\s+(?:init|status|"
    rb"serve|demo|a2a-demo|harness(?:-probe|-demo|-live-gate)?))))"
)

_EXACT_REQUIREMENT = re.compile(
    r"^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[([A-Za-z0-9._,-]+)\])?"
    r"==([A-Za-z0-9][A-Za-z0-9._+!-]*)$"
)
_EXACT_PLATFORM_MARKER = re.compile(
    r"^sys_platform\s*==\s*(['\"])(win32|darwin|linux)\1$"
)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _source_tree_sha256(root: Path) -> str:
    """Hash every source path and byte in a deterministic, cache-free order."""

    digest = hashlib.sha256()
    source_root = root / "src"
    for path in sorted(source_root.rglob("*"), key=lambda item: item.as_posix()):
        if not path.is_file() or "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        relative = path.relative_to(root).as_posix().encode("utf-8")
        digest.update(relative + b"\x00")
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
        digest.update(b"\x00")
    return digest.hexdigest()


def _expected_source_files(root: Path) -> dict[str, bytes]:
    return {
        path.relative_to(root / "src").as_posix(): path.read_bytes()
        for path in sorted((root / "src").rglob("*"), key=lambda item: item.as_posix())
        if path.is_file() and "__pycache__" not in path.parts and path.suffix != ".pyc"
    }


def _safe_archive_name(name: str) -> bool:
    if not name or "\\" in name or name.startswith("/"):
        return False
    path = PurePosixPath(name)
    return not path.is_absolute() and all(part not in {"", ".", ".."} for part in path.parts)


def _record_digest(payload: bytes) -> str:
    encoded = base64.urlsafe_b64encode(hashlib.sha256(payload).digest()).rstrip(b"=")
    return f"sha256={encoded.decode('ascii')}"


def _expected_sdist_files(root: Path, source_files: dict[str, bytes]) -> dict[str, bytes]:
    expected = {
        relative: (root / relative).read_bytes()
        for relative in EXPECTED_SDIST_STATIC_FILES
        if (root / relative).is_file()
    }
    expected.update(EXPECTED_HATCH_SDIST_VCS_METADATA)
    expected.update({f"src/{relative}": payload for relative, payload in source_files.items()})
    expected.update(
        {
            f"schemas/v1/{name}.json": (root / f"schemas/v1/{name}.json").read_bytes()
            for name in EXPECTED_SCHEMA_NAMES
            if (root / f"schemas/v1/{name}.json").is_file()
        }
    )
    return expected


def _verify_built_artifacts(root: Path, artifacts: Any, failures: list[str]) -> None:
    base = "evidence/local/2026-08-01-v0.1.35/artifacts"
    ignore_path = root / base / ".gitignore"
    retention_path = root / base / "RETENTION.md"
    expected_ignore = "*\n!/.gitignore\n!/RETENTION.md\n!/*.whl\n!/*.tar.gz\n"
    expected_retention = (
        "# Retained release archives\n\n"
        "This directory intentionally retains only `.gitignore`, `*.whl`, and `*.tar.gz` "
        "release-evidence files.\n"
    )
    if not retention_path.is_file() or retention_path.read_text(encoding="utf-8") != expected_retention:
        failures.append("final package artifact directory lacks portable archive-retention evidence")
    if ignore_path.is_file():
        if ignore_path.read_text(encoding="utf-8") != expected_ignore:
            failures.append("final package artifact ignore policy does not retain its archives")
    elif (root / ".git").exists():
        failures.append("final package artifact ignore policy does not retain its archives")
    expected_paths = {
        f"{base}/agentnet-0.1.35.tar.gz",
        f"{base}/agentnet-0.1.35-py3-none-any.whl",
    }
    if not isinstance(artifacts, list) or len(artifacts) != 2:
        failures.append("final package evidence must contain exactly the sdist and wheel")
        return
    records = {
        str(record.get("path")): record
        for record in artifacts
        if isinstance(record, dict)
    }
    if set(records) != expected_paths:
        failures.append("final package artifacts must use the fixed durable evidence paths")
        return
    paths: dict[str, Path] = {}
    for relative, record in records.items():
        path = root / relative
        paths[relative] = path
        if not path.is_file():
            failures.append(f"final package artifact is missing: {relative}")
        elif _sha256(path) != record.get("sha256"):
            failures.append(f"final package artifact hash drifted: {relative}")
    if any(not path.is_file() for path in paths.values()):
        return

    source_files = _expected_source_files(root)
    schema_readme = root / "schemas/README.md"
    missing_schema_inputs = [
        root / f"schemas/v1/{name}.json"
        for name in sorted(EXPECTED_SCHEMA_NAMES)
        if not (root / f"schemas/v1/{name}.json").is_file()
    ]
    if not schema_readme.is_file():
        failures.append("final package schema README is missing")
    for path in missing_schema_inputs:
        failures.append(f"final package schema input is missing: {path.relative_to(root)}")
    if not schema_readme.is_file() or missing_schema_inputs:
        return
    schema_files = {
        "schemas/README.md": schema_readme.read_bytes(),
        **{
            f"schemas/v1/{name}.json": (root / f"schemas/v1/{name}.json").read_bytes()
            for name in EXPECTED_SCHEMA_NAMES
        },
    }
    wheel_path = paths[next(path for path in expected_paths if path.endswith(".whl"))]
    wheel_metadata: bytes | None = None
    try:
        with zipfile.ZipFile(wheel_path) as archive:
            infos = archive.infolist()
            listed_names = [info.filename for info in infos]
            names = set(listed_names)
            if len(names) != len(listed_names):
                failures.append("wheel contains duplicate archive member names")
            dist_info = "agentnet-0.1.35.dist-info"
            shared = "agentnet-0.1.35.data/data/share/agentnet"
            expected_payloads = dict(source_files)
            expected_payloads.update(
                {
                    f"{dist_info}/licenses/LICENSE": (root / "LICENSE").read_bytes(),
                    f"{shared}/RELEASE_MANIFEST.json": (root / "RELEASE_MANIFEST.json").read_bytes(),
                    f"{shared}/REQUIREMENTS_STATUS.md": (root / "REQUIREMENTS_STATUS.md").read_bytes(),
                }
            )
            expected_payloads.update(
                {f"{shared}/{relative}": payload for relative, payload in schema_files.items()}
            )
            generated_names = {
                f"{dist_info}/METADATA",
                f"{dist_info}/WHEEL",
                f"{dist_info}/entry_points.txt",
                f"{dist_info}/RECORD",
            }
            expected_names = set(expected_payloads) | generated_names
            if names != expected_names:
                failures.append("wheel member catalog differs from the exact approved release set")
            if archive.comment:
                failures.append("wheel contains an unexpected archive comment")
            for info in infos:
                mode = (info.external_attr >> 16) & 0xFFFF
                if (
                    not _safe_archive_name(info.filename)
                    or info.is_dir()
                    or stat.S_IFMT(mode) == stat.S_IFLNK
                    or info.flag_bits & 0x1
                    or info.extra
                    or info.comment
                ):
                    failures.append(f"wheel contains an unsafe member: {info.filename}")
                if info.date_time != (2020, 2, 2, 0, 0, 0):
                    failures.append(f"wheel member timestamp is not reproducible: {info.filename}")
                if mode & 0o777 != 0o644 or info.compress_type != zipfile.ZIP_DEFLATED:
                    failures.append(f"wheel member metadata is not normalized: {info.filename}")
            for archive_name, expected in expected_payloads.items():
                if archive_name not in names or archive.read(archive_name) != expected:
                    failures.append(f"wheel member differs from current tree: {archive_name}")
            for archive_name, payload in source_files.items():
                if _FORBIDDEN_PRODUCT_NAMESPACE.search(payload):
                    failures.append(f"wheel runtime source retains a forbidden product namespace: {archive_name}")
            entry_name = f"{dist_info}/entry_points.txt"
            if entry_name not in names or archive.read(entry_name) != EXPECTED_WHEEL_ENTRY_POINTS:
                failures.append("wheel console entry point is not exactly agentnet = agentnet.cli:main")
            wheel_name = f"{dist_info}/WHEEL"
            if wheel_name not in names or archive.read(wheel_name) != EXPECTED_WHEEL_METADATA:
                failures.append("wheel generator metadata differs from the pinned pure-Python profile")
            metadata_name = f"{dist_info}/METADATA"
            if metadata_name in names:
                wheel_metadata = archive.read(metadata_name)
                if (
                    b"\nName: agentnet\n" not in b"\n" + wheel_metadata
                    or b"\nVersion: 0.1.35\n" not in b"\n" + wheel_metadata
                    or b"\nRequires-Python: <3.15,>=3.13\n" not in b"\n" + wheel_metadata
                ):
                    failures.append("wheel core metadata differs from the release identity/runtime")
            record_name = f"{dist_info}/RECORD"
            if record_name in names:
                rows = list(csv.reader(io.StringIO(archive.read(record_name).decode("utf-8"))))
                record_paths = [row[0] for row in rows if len(row) == 3]
                if len(rows) != len(record_paths) or len(set(record_paths)) != len(record_paths):
                    failures.append("wheel RECORD is malformed or contains duplicate paths")
                elif set(record_paths) != expected_names:
                    failures.append("wheel RECORD does not cover the exact member catalog")
                else:
                    records = {row[0]: row for row in rows}
                    for name in sorted(expected_names):
                        digest, size = records[name][1:]
                        if name == record_name:
                            if digest or size:
                                failures.append("wheel RECORD self-entry must omit digest and size")
                            continue
                        payload = archive.read(name)
                        if digest != _record_digest(payload) or size != str(len(payload)):
                            failures.append(f"wheel RECORD hash or size differs: {name}")
    except (OSError, UnicodeError, zipfile.BadZipFile, KeyError) as exc:
        failures.append(f"final wheel is unreadable or malformed: {exc}")

    sdist_path = paths[next(path for path in expected_paths if path.endswith(".tar.gz"))]
    prefix = "agentnet-0.1.35/"
    try:
        with tarfile.open(sdist_path, mode="r:gz") as archive:
            all_members = archive.getmembers()
            listed_names = [member.name for member in all_members]
            if len(set(listed_names)) != len(listed_names):
                failures.append("sdist contains duplicate archive member names")
            expected_payloads = _expected_sdist_files(root, source_files)
            expected_names = {f"{prefix}{relative}" for relative in expected_payloads} | {
                f"{prefix}PKG-INFO"
            }
            if set(listed_names) != expected_names:
                failures.append("sdist member catalog differs from the exact approved release set")
            members = {member.name: member for member in all_members}
            for member in all_members:
                if (
                    not _safe_archive_name(member.name)
                    or not member.isfile()
                    or member.issym()
                    or member.islnk()
                    or member.pax_headers
                ):
                    failures.append(f"sdist contains an unsafe member: {member.name}")
                if (
                    member.mode != 0o644
                    or member.uid != 0
                    or member.gid != 0
                    or member.uname
                    or member.gname
                    or member.mtime != REPRODUCIBLE_TIMESTAMP
                ):
                    failures.append(f"sdist member metadata is not normalized: {member.name}")
            for relative, expected in expected_payloads.items():
                name = f"{prefix}{relative}"
                extracted = archive.extractfile(members[name]) if name in members else None
                if extracted is None or extracted.read() != expected:
                    failures.append(f"sdist member differs from current tree: {relative}")
            package_info_name = f"{prefix}PKG-INFO"
            if package_info_name in members:
                extracted = archive.extractfile(members[package_info_name])
                package_info = extracted.read() if extracted is not None else b""
                if wheel_metadata is None or package_info != wheel_metadata:
                    failures.append("sdist PKG-INFO differs from wheel METADATA")
    except (OSError, tarfile.TarError, KeyError) as exc:
        failures.append(f"final sdist is unreadable or malformed: {exc}")


def _canonical_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def _normalized_constraint(value: str) -> str:
    return value.replace(" ", "")


def parse_exact_requirement(requirement: str) -> tuple[str, dict[str, Any]] | None:
    """Parse one exact pin plus at most one allowlisted platform marker."""

    if "*" in requirement or requirement.count(";") > 1:
        return None
    pinned, separator, raw_marker = requirement.strip().partition(";")
    match = _EXACT_REQUIREMENT.fullmatch(pinned.strip())
    if match is None:
        return None
    marker: str | None = None
    if separator:
        marker_match = _EXACT_PLATFORM_MARKER.fullmatch(raw_marker.strip())
        if marker_match is None:
            return None
        marker = f"sys_platform == '{marker_match.group(2)}'"
    name, raw_extras, version = match.groups()
    extras = sorted(filter(None, (raw_extras or "").split(",")))
    return _canonical_name(name), {
        "version": version,
        "extras": extras,
        "marker": marker,
    }


def _collect_direct_dependencies(pyproject: dict[str, Any], failures: list[str]) -> dict[str, Any]:
    direct: dict[str, dict[str, dict[str, Any]]] = {}
    groups: list[tuple[str, list[str]]] = [
        ("build", list(pyproject.get("build-system", {}).get("requires", []))),
        ("runtime", list(pyproject.get("project", {}).get("dependencies", []))),
    ]
    for group, requirements in pyproject.get("project", {}).get("optional-dependencies", {}).items():
        groups.append((str(group), list(requirements)))

    for scope, requirements in groups:
        parsed_scope: dict[str, dict[str, Any]] = {}
        for requirement in requirements:
            parsed = parse_exact_requirement(requirement)
            if parsed is None:
                failures.append(f"unpinned direct dependency in {scope}: {requirement!r}")
                continue
            name, details = parsed
            if name in parsed_scope:
                failures.append(f"duplicate direct dependency in {scope}: {name}")
                continue
            parsed_scope[name] = details
        direct[scope] = parsed_scope
    return direct


def _locked_resolution(lock: dict[str, Any], failures: list[str]) -> dict[str, str]:
    resolution: dict[str, str] = {}
    for package in lock.get("package", []):
        if package.get("source", {}).get("editable") == ".":
            continue
        name = _canonical_name(str(package.get("name", "")))
        version = str(package.get("version", ""))
        if not name or not version:
            failures.append("uv.lock contains a package without an exact name/version")
            continue
        if name in resolution and resolution[name] != version:
            failures.append(f"uv.lock contains multiple versions for {name}")
            continue
        resolution[name] = version
    return resolution


def _load_json(path: Path, failures: list[str], label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        failures.append(f"cannot load {label}: {exc}")
        return {}
    if not isinstance(value, dict):
        failures.append(f"{label} must be a JSON object")
        return {}
    return value


def _verify_sources(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    sources = manifest.get("authoritative_sources", {})
    if set(sources) != set(EXPECTED_SOURCES):
        failures.append("authoritative source catalog differs from the sealed three-document set")
    for key, (filename, expected_hash) in EXPECTED_SOURCES.items():
        entry = sources.get(key, {})
        if entry.get("sha256") != expected_hash:
            failures.append(f"wrong authoritative source hash recorded for {key}")
        path = (root / str(entry.get("path", ""))).resolve()
        expected_path = (root / filename).resolve()
        if path != expected_path:
            failures.append(f"authoritative source path mismatch for {key}")
            continue
        if not path.is_file():
            failures.append(f"authoritative source is missing: {path}")
        elif _sha256(path) != expected_hash:
            failures.append(f"authoritative source bytes drifted: {filename}")


def _verify_dependencies(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    lock_record = manifest.get("dependency_lock", {})
    lock_path = root / str(lock_record.get("path", ""))
    pyproject_record = lock_record.get("pyproject", {})
    pyproject_path = root / str(pyproject_record.get("path", ""))
    for path, record, label in (
        (lock_path, lock_record, "uv.lock"),
        (pyproject_path, pyproject_record, "pyproject.toml"),
    ):
        if not path.is_file():
            failures.append(f"missing release dependency input: {label}")
        elif _sha256(path) != record.get("sha256"):
            failures.append(f"release dependency input drifted: {label}")
    if not lock_path.is_file() or not pyproject_path.is_file():
        return

    try:
        lock = tomllib.loads(lock_path.read_text(encoding="utf-8"))
        pyproject = tomllib.loads(pyproject_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, tomllib.TOMLDecodeError) as exc:
        failures.append(f"cannot parse dependency metadata: {exc}")
        return

    if lock.get("version") != lock_record.get("format_version"):
        failures.append("uv.lock format version differs from release manifest")
    if lock.get("revision") != lock_record.get("revision"):
        failures.append("uv.lock revision differs from release manifest")

    runtime = manifest.get("runtime", {})
    lock_python = str(lock.get("requires-python", ""))
    project_python = str(pyproject.get("project", {}).get("requires-python", ""))
    recorded_python = str(runtime.get("requires_python", ""))
    normalized = {_normalized_constraint(value) for value in (lock_python, project_python, recorded_python)}
    if len(normalized) != 1:
        failures.append("Python compatibility range drifted across manifest, pyproject, and uv.lock")
    if runtime.get("implementation") != "CPython":
        failures.append("release runtime must remain the selected CPython implementation")
    if runtime.get("version") != platform.python_version() or platform.python_implementation() != "CPython":
        failures.append(
            f"verification runtime is {platform.python_implementation()} {platform.python_version()}, "
            f"expected CPython {runtime.get('version')}"
        )

    project = pyproject.get("project", {})
    release = manifest.get("release", {})
    if project.get("name") != release.get("name") or project.get("version") != release.get("version"):
        failures.append("project identity/version differs from release manifest")

    direct = _collect_direct_dependencies(pyproject, failures)
    if direct != lock_record.get("direct_dependencies"):
        failures.append("direct dependency pins/extras differ from release manifest")

    resolution = _locked_resolution(lock, failures)
    if resolution != lock_record.get("resolution"):
        failures.append("complete uv.lock name/version resolution differs from release manifest")
    build_group = list(pyproject.get("dependency-groups", {}).get("build", []))
    build_system = list(pyproject.get("build-system", {}).get("requires", []))
    if sorted(build_group) != sorted(build_system) or not build_group:
        failures.append("build dependency group must exactly lock every build-system requirement")
    for scope in ("build", "runtime", "test"):
        for name, details in direct.get(scope, {}).items():
            if resolution.get(name) != details["version"]:
                failures.append(f"direct {scope} dependency is absent or differently pinned in uv.lock: {name}")

    hatch_build = pyproject.get("tool", {}).get("hatch", {}).get("build", {})
    targets = hatch_build.get("targets", {}) if isinstance(hatch_build, dict) else {}
    wheel = targets.get("wheel", {}) if isinstance(targets, dict) else {}
    sdist = targets.get("sdist", {}) if isinstance(targets, dict) else {}
    expected_shared = {
        "schemas": "share/agentnet/schemas",
        "RELEASE_MANIFEST.json": "share/agentnet/RELEASE_MANIFEST.json",
        "REQUIREMENTS_STATUS.md": "share/agentnet/REQUIREMENTS_STATUS.md",
    }
    if hatch_build.get("reproducible") is not True:
        failures.append("Hatch release builds must explicitly remain reproducible")
    if wheel.get("packages") != ["src/agentnet"] or wheel.get("shared-data") != expected_shared:
        failures.append("wheel build target differs from the exact AgentNet package/shared-data contract")
    if sdist.get("only-include") != list(EXPECTED_SDIST_ONLY_INCLUDE):
        failures.append("sdist allowlist differs from the exact approved release file set")


def _verify_release_inputs(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    records = manifest.get("release_inputs", {})
    if set(records) != EXPECTED_RELEASE_INPUT_PATHS:
        failures.append("release input catalog is incomplete or contains unexpected paths")
    for relative_path in sorted(EXPECTED_RELEASE_INPUT_PATHS):
        record = records.get(relative_path, {})
        path = root / relative_path
        if not isinstance(record, dict) or record.get("path") != relative_path:
            failures.append(f"release input record is malformed: {relative_path}")
            continue
        if not path.is_file():
            failures.append(f"release input is missing: {relative_path}")
        elif _sha256(path) != record.get("sha256"):
            failures.append(f"release input drifted: {relative_path}")
    dockerfile = root / "deploy/Dockerfile"
    if dockerfile.is_file():
        runtime_version = manifest.get("runtime", {}).get("version")
        expected_from = f"FROM python:{runtime_version}-slim-bookworm@sha256:${{AGENTNET_PYTHON_BASE_DIGEST}} AS runtime"
        if expected_from not in dockerfile.read_text(encoding="utf-8"):
            failures.append("deployment Python base tag differs from the recorded release runtime")
    source_tree = manifest.get("release_source_tree", {})
    if source_tree.get("path") != "src" or source_tree.get("algorithm") != "sha256(path NUL bytes NUL)":
        failures.append("release source-tree record is malformed")
    elif _source_tree_sha256(root) != source_tree.get("sha256"):
        failures.append("release source tree drifted")


def _verify_public_readme(root: Path, failures: list[str]) -> None:
    readme_path = root / "README.md"
    if not readme_path.is_file():
        return
    normalized = re.sub(r"\s+", " ", readme_path.read_text(encoding="utf-8"))
    required_claims = (
        "latest published package is `0.1.33`",
        "candidate `0.1.35`",
        "two installed-harness pin failures remain non-green and are not waived",
        "provider error without token exchange",
        "fresh-laptop enrollment",
        "COMPLETED_C0_ROUND_TRIP",
    )
    for claim in required_claims:
        if claim not in normalized:
            failures.append(f"public README release status is stale or incomplete: {claim}")
    for stale_claim in (
        "latest published package is `0.1.26`",
        "latest published package is `0.1.27`",
        "latest published package is `0.1.28`",
        "latest published package is `0.1.29`",
        "latest published package is `0.1.30`",
        "latest published package is `0.1.31`",
        "latest published package is `0.1.32`",
        "Current unversioned communication-only changes",
    ):
        if stale_claim in normalized:
            failures.append(f"public README retains stale release status: {stale_claim}")


def _load_generator_catalog(root: Path) -> dict[str, Any]:
    generator_path = root / "scripts/export_schemas.py"
    module_name = f"_agentnet_export_schemas_{hash(root)}"
    spec = importlib.util.spec_from_file_location(module_name, generator_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load schema generator from {generator_path}")
    module = importlib.util.module_from_spec(spec)
    source_path = str(root / "src")
    sys.path.insert(0, source_path)
    try:
        spec.loader.exec_module(module)
    finally:
        try:
            sys.path.remove(source_path)
        except ValueError:
            pass
    return dict(module.CATALOG)


def _verify_schemas(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    catalog_record = manifest.get("schema_catalog", {})
    if catalog_record.get("version") != "1.0":
        failures.append("schema catalog version must be exactly 1.0")
    generator = root / str(catalog_record.get("generator", ""))
    if not generator.is_file():
        failures.append("schema generator is missing")

    schemas = catalog_record.get("schemas", {})
    if set(schemas) != EXPECTED_SCHEMA_NAMES:
        failures.append("release schema catalog is missing or has unexpected schema names")
    actual_files = {path.stem for path in (root / "schemas/v1").glob("*.json")}
    if actual_files != EXPECTED_SCHEMA_NAMES:
        failures.append("generated schemas/v1 directory is missing or has unexpected schemas")

    for name in sorted(EXPECTED_SCHEMA_NAMES):
        entry = schemas.get(name, {})
        expected_relative = f"schemas/v1/{name}.json"
        if entry.get("path") != expected_relative:
            failures.append(f"schema path mismatch: {name}")
            continue
        path = root / expected_relative
        if not path.is_file():
            failures.append(f"schema file is missing: {expected_relative}")
            continue
        if _sha256(path) != entry.get("sha256"):
            failures.append(f"schema hash drifted: {name}")
        schema = _load_json(path, failures, f"schema {name}")
        expected_id = f"https://agentnet.invalid/schemas/v1/{name}.json"
        if entry.get("$id") != expected_id or schema.get("$id") != expected_id:
            failures.append(f"schema $id mismatch: {name}")
        if schema.get("x-agentnet-schema-version") != "1.0":
            failures.append(f"schema version marker mismatch: {name}")

    if not generator.is_file():
        return
    try:
        generated_catalog = _load_generator_catalog(root)
    except Exception as exc:  # pragma: no cover - exact import failures vary by environment
        failures.append(f"cannot regenerate schema catalog: {exc}")
        return
    if set(generated_catalog) != EXPECTED_SCHEMA_NAMES:
        failures.append("schema generator catalog differs from release catalog")
        return
    for name, model in generated_catalog.items():
        schema = model.model_json_schema(by_alias=True)
        schema["$id"] = f"https://agentnet.invalid/schemas/v1/{name}.json"
        schema["x-agentnet-schema-version"] = "1.0"
        generated = (json.dumps(schema, indent=2, sort_keys=True, ensure_ascii=False) + "\n").encode()
        path = root / f"schemas/v1/{name}.json"
        if path.is_file() and path.read_bytes() != generated:
            failures.append(f"checked-in schema is stale relative to its model: {name}")


def _verify_decisions_and_gates(manifest: dict[str, Any], failures: list[str]) -> None:
    if manifest.get("protocols") != EXPECTED_PROTOCOLS:
        failures.append("protocol pins drifted from the approved A2A/MCP profile")

    components = manifest.get("component_decisions", {})
    statuses = {name: value.get("status") for name, value in components.items() if isinstance(value, dict)}
    if statuses != EXPECTED_COMPONENT_STATUSES:
        failures.append("component decision inventory drifted or is incomplete")

    gates = manifest.get("must_not_ship_gates", {})
    gate_statuses = {gate: value.get("status") for gate, value in gates.items() if isinstance(value, dict)}
    if gate_statuses != EXPECTED_GATE_STATUSES:
        failures.append("must-not-ship gate status ledger drifted")
    for gate, value in gates.items():
        if value.get("status") == "PASSED":
            failures.append(f"false production claim: {gate} has no attached external release evidence")
        if value.get("external_evidence") not in {"MISSING", "REVIEWED_PARTIAL"}:
            failures.append(f"false external evidence claim for {gate}")

    release = manifest.get("release", {})
    if (
        release.get("status") != "BLOCKED"
        or release.get("production_ready") is not False
        or release.get("ship_eligible") is not False
    ):
        failures.append("false production claim: this evidence set is blocked and not ship-eligible")

    external = manifest.get("external_release_evidence", {})
    if set(external) != EXPECTED_EXTERNAL_EVIDENCE:
        failures.append("external supply-chain evidence catalog is incomplete")
    for name, value in external.items():
        if value.get("status") != "EXTERNAL_REQUIRED" or value.get("passed") is not False:
            failures.append(f"false external supply-chain claim: {name}")


def _verify_evidence_ledgers(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    requirements_path = root / "REQUIREMENTS_STATUS.md"
    gate_path = root / "docs/GATE_EVIDENCE.md"
    source_path = root / "docs/requirements.md"
    try:
        requirements = requirements_path.read_text(encoding="utf-8")
        gate_text = gate_path.read_text(encoding="utf-8")
        source = source_path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        failures.append(f"cannot read release evidence ledgers: {exc}")
        return

    expected_ids = set(re.findall(r"^- \[ \] \*\*([A-Z]+-[0-9]{3}) —", source, re.MULTILINE))
    rows = re.findall(
        r"^\| ([A-Z]+-[0-9]{3}) — [^|]+\| "
        r"(local-tested|partial-external|owner-blocked|implementation-gap) \|",
        requirements,
        re.MULTILINE,
    )
    row_ids = [requirement_id for requirement_id, _status in rows]
    if len(expected_ids) != 85 or len(rows) != 85 or set(row_ids) != expected_ids or len(row_ids) != len(set(row_ids)):
        failures.append("requirements ledger does not contain the exact 85 unique canonical IDs")
    expected_status_counts = {
        "local-tested": 33,
        "partial-external": 42,
        "owner-blocked": 10,
        "implementation-gap": 0,
    }
    actual_status_counts = {
        status: sum(1 for _requirement_id, actual in rows if actual == status)
        for status in expected_status_counts
    }
    if actual_status_counts != expected_status_counts:
        failures.append("requirements ledger status totals drifted")
    expected_summary = (
        "Requirement totals: **33 local-tested, 42 partial-external, 10 owner-blocked, "
        "0 implementation-gap = 85 unique requirements**."
    )
    if expected_summary not in re.sub(r"\s+", " ", requirements):
        failures.append("requirements ledger human status summary differs from its rows")
    snapshot_date = manifest.get("snapshot_date")
    if not isinstance(snapshot_date, str) or f"Snapshot: {snapshot_date}." not in requirements:
        failures.append("requirements ledger snapshot date differs from the release manifest")
    if not isinstance(snapshot_date, str) or f"Current ledger update: {snapshot_date}." not in gate_text:
        failures.append("gate evidence ledger date differs from the release manifest")
    pd_ids = re.findall(r"^\| (PD-[0-9]{3}) \|", requirements, re.MULTILINE)
    if set(pd_ids) != {f"PD-{index:03d}" for index in range(1, 12)} or len(pd_ids) != 11:
        failures.append("requirements ledger does not contain exactly PD-001 through PD-011")

    gate_rows = dict(
        re.findall(
            r"^\| (G[0-9]{2}) — [^|]+\|[^|]+\|[^|]+\| `([A-Z_]+)` \|",
            gate_text,
            re.MULTILINE,
        )
    )
    machine_statuses = {
        gate: value.get("status")
        for gate, value in manifest.get("must_not_ship_gates", {}).items()
        if isinstance(value, dict)
    }
    if gate_rows != machine_statuses:
        failures.append("human gate evidence ledger differs from machine gate statuses")
    if re.search(r"\bscaffolded\b|\bpending:\s|\b46/235\b", requirements + "\n" + gate_text):
        failures.append("evidence ledgers contain banned stale or misleading language")

    local_evidence = _load_json(
        root / "evidence/local/2026-07-13-final/manifest.json",
        failures,
        "final local evidence manifest",
    )
    commands = [entry for entry in local_evidence.get("commands", []) if isinstance(entry, dict)]
    if local_evidence.get("release_certified") is not False:
        failures.append("final local evidence makes a false release-certification claim")
    full_result = next(
        (
            str(entry.get("result"))
            for entry in commands
            if "AGENTNET_TEST_POSTGRES_URL=" in str(entry.get("command"))
            and "AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1" in str(entry.get("command"))
            and re.search(
                r"(?:^|\s)(?:uv run |\.venv/bin/)?pytest -q"
                r"(?:\s+-p\s+no:cacheprovider)?\s*$",
                str(entry.get("command")),
            )
        ),
        "",
    )
    build_command = next(
        (str(entry.get("command")) for entry in commands if "uv build" in str(entry.get("command"))),
        "",
    )
    status_result = next(
        (
            str(entry.get("result"))
            for entry in commands
            if re.search(r"(?:^|[/\s])agentnet status\b", str(entry.get("command")))
        ),
        "",
    )
    passed_match = re.fullmatch(
        r"([1-9][0-9]*) passed; 0 failed; 0 skipped; 0 xfailed",
        full_result,
    )
    schema_match = re.search(r"schema version ([0-9]+)", status_result)
    if not passed_match:
        failures.append(
            "final local full-suite record must be the mutation-authorized broad run "
            "with zero failures, skips, and xfails"
        )
    elif (
        f"{passed_match.group(1)} passed" not in requirements
        or f"{passed_match.group(1)} passed" not in gate_text
    ):
        failures.append("final local test count differs from the human evidence ledgers")
    if not build_command or build_command not in gate_text:
        failures.append("final package build command differs from the gate evidence ledger")
    if not schema_match or int(schema_match.group(1)) != EXPECTED_STORAGE_SCHEMA_VERSION:
        failures.append(
            f"final clean-install evidence must report storage schema version "
            f"{EXPECTED_STORAGE_SCHEMA_VERSION}"
        )
    elif f"schema-v{schema_match.group(1)}" not in gate_text:
        failures.append("final clean-install schema version differs from the gate evidence ledger")
    if status_result and (
        "ready true" not in status_result
        or "accepted_local" not in status_result
        or "release_certified false" not in status_result
    ):
        failures.append("final clean-install status does not preserve the local blocked-release boundary")
    package_evidence = _load_json(
        root / "evidence/local/2026-08-01-v0.1.35/manifest.json",
        failures,
        "0.1.35 package evidence manifest",
    )
    if package_evidence.get("release_source_tree_sha256") != _source_tree_sha256(root):
        failures.append("0.1.35 package evidence is not bound to the current source tree")
    if package_evidence.get("verification_status") != "PASS":
        failures.append("0.1.35 package evidence must record completed PASS verification")
    command_records = package_evidence.get("commands")
    if not isinstance(command_records, list) or any(
        not isinstance(record, dict)
        or not isinstance(record.get("command"), str)
        or not isinstance(record.get("result"), str)
        for record in command_records
    ):
        failures.append("0.1.35 package evidence commands are malformed")
    else:
        command_results = {
            record["command"]: record["result"]
            for record in command_records
        }
        npm_result = command_results.get("npm run check", "")
        if (
            not npm_result.startswith("PASS:")
            or "1621 passed and 16 expected" not in npm_result
            or "generations 1 and 2" not in npm_result
            or "excludes installed-live-inference, subprocess-lifecycle, and bake-off-evidence" not in npm_result
            or "two installed-harness pin failures remain non-green" not in npm_result
            or "not rerun or waived" not in npm_result
        ):
            failures.append("0.1.35 npm source and recursive packed evidence is incomplete")
        required_focused_paths = (
            "tests/approval",
            "tests/identity",
            "tests/integration/test_enrollment_http.py",
            "tests/integration/test_cli_product_journey.py",
            "tests/integration/test_c0_pilot_http.py",
            "tests/operations/test_fail_closed_config.py",
            "tests/operations/test_server_setup.py",
            "tests/operations/test_server_setup_recovery.py",
            "tests/production/test_postgres_runtime.py",
            "tests/supervisor/test_c0_pilot_responder.py",
            "tests/supervisor/test_daemon_config.py",
            "tests/cli/test_credential_renewal_cli.py",
        )
        if not any(
            command.startswith(
                "PYTHONDONTWRITEBYTECODE=1 UV_CACHE_DIR=/tmp/uv-cache "
                "uv run pytest -q -p no:cacheprovider "
            )
            and all(path in command.split() for path in required_focused_paths)
            and result == "PASS: 625 passed, 7 expected dedicated-PostgreSQL skips"
            for command, result in command_results.items()
        ):
            failures.append("0.1.35 focused release-blocker evidence is incomplete")
        if not any(
            command.startswith("SOURCE_DATE_EPOCH=1580601600 ")
            and result.startswith("PASS: two independent builds")
            for command, result in command_results.items()
        ):
            failures.append("0.1.35 reproducible build evidence is incomplete")
        if any("PENDING" in result for result in command_results.values()):
            failures.append("0.1.35 package evidence cannot retain pending command results")
    execution_context = package_evidence.get("execution_context")
    if not isinstance(execution_context, dict) or not all(
        isinstance(execution_context.get(key), str) and execution_context[key]
        for key in (
            "source",
            "callback_incident",
            "packed_generations",
            "root_installed_external_host",
        )
    ):
        failures.append("0.1.35 package evidence execution context is incomplete")
    _verify_built_artifacts(root, package_evidence.get("artifacts", []), failures)


def _verify_a2a_gate_evidence(root: Path, failures: list[str]) -> None:
    """Keep the non-green official run durable and every skip reviewed."""

    base = Path("evidence/gates/G04/2026-07-13-alpha2-http-json")
    record = _load_json(root / base / "manifest.json", failures, "G04 evidence manifest")
    pytest_result = record.get("pytest", {})
    if pytest_result != {
        "selected": 235,
        "passed": 46,
        "failed": 12,
        "skipped": 177,
        "errors": 0,
        "deselected": 30,
        "xfailed": 0,
    }:
        failures.append("G04 official result counts drifted")
    if (
        record.get("official_gate_green") is not False
        or record.get("remaining_official_failures") != 12
        or record.get("failure_outcomes_classified") != 12
        or record.get("skip_outcomes_exhaustively_adjudicated") is not True
    ):
        failures.append("G04 failure/skip review posture is incomplete or overstated")
    reviewed_skips = record.get("reviewed_skips", [])
    if (
        not isinstance(reviewed_skips, list)
        or sum(
            item.get("count", -1) if isinstance(item, dict) and type(item.get("count")) is int else -1
            for item in reviewed_skips
        )
        != 177
        or any(
            not isinstance(item, dict)
            or not item.get("exact_messages")
            or not item.get("classification")
            or not item.get("reason")
            for item in reviewed_skips
        )
    ):
        failures.append("G04 reviewed skip categories do not account for all 177 outcomes")

    reports = record.get("reports", [])
    expected_names = {
        "compatibility.json",
        "compatibility.html",
        "tck_report.html",
        "junitreport.xml",
    }
    if not isinstance(reports, list) or {
        item.get("name") for item in reports if isinstance(item, dict)
    } != expected_names:
        failures.append("G04 durable report catalog is incomplete")
        return
    for item in reports:
        if not isinstance(item, dict):
            continue
        expected_path = (base / str(item.get("name"))).as_posix()
        if item.get("path") != expected_path:
            failures.append(f"G04 report path is not durable and canonical: {item.get('name')}")
            continue
        path = root / expected_path
        if not path.is_file() or _sha256(path) != item.get("sha256"):
            failures.append(f"G04 report is absent or hash-drifted: {item.get('name')}")

    junit = root / base / "junitreport.xml"
    if not junit.is_file():
        return
    try:
        suite = ET.parse(junit).getroot()
        testcases = suite.findall(".//testcase")
    except (ET.ParseError, OSError) as exc:
        failures.append(f"G04 JUnit report is unreadable: {exc}")
        return
    actual = {
        "selected": len(testcases),
        "failed": sum(case.find("failure") is not None for case in testcases),
        "skipped": sum(case.find("skipped") is not None for case in testcases),
        "errors": sum(case.find("error") is not None for case in testcases),
    }
    actual["passed"] = actual["selected"] - actual["failed"] - actual["skipped"] - actual["errors"]
    if any(actual[key] != pytest_result.get(key) for key in actual):
        failures.append("G04 JUnit outcomes differ from its reviewed manifest")


def _verify_human_manifest(manifest: dict[str, Any], root: Path, failures: list[str]) -> None:
    path = root / "docs/RELEASE_MANIFEST.md"
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        failures.append(f"human release manifest is missing or unreadable: {exc}")
        return
    lock = manifest.get("dependency_lock", {})
    pyproject = lock.get("pyproject", {})
    required_markers = (
        f"Snapshot: {manifest.get('snapshot_date')}",
        f"Candidate: `{manifest.get('release', {}).get('name')} {manifest.get('release', {}).get('version')}`",
        "This is not a production release.",
        "Production ready | `false`",
        "Ship eligible | `false`",
        (
            f"| `{lock.get('path')}` | format `{lock.get('format_version')}`, "
            f"revision `{lock.get('revision')}`, SHA-256 `{lock.get('sha256')}` |"
        ),
        f"| `{pyproject.get('path')}` | SHA-256 `{pyproject.get('sha256')}` |",
        "A2A | release `1.0.1`; wire `1.0`; Python SDK `1.1.0`",
        "MCP | spec `2025-11-25`; Python SDK `1.28.1`",
    )
    for marker in required_markers:
        if marker not in text:
            failures.append(f"human release manifest is missing required marker: {marker}")
    for name, entry in manifest.get("schema_catalog", {}).get("schemas", {}).items():
        marker = f"| `{name}.json` | `{entry.get('sha256')}` |"
        if marker not in text:
            failures.append(f"human release manifest schema row differs: {name}")
    for name, entry in manifest.get("component_decisions", {}).items():
        marker = f"| `{name}` | `{entry.get('status')}` |"
        if marker not in text:
            failures.append(f"human release manifest component row differs: {name}")
    for gate, entry in manifest.get("must_not_ship_gates", {}).items():
        marker = f"| {gate} | `{entry.get('status')}` | `{entry.get('external_evidence')}` |"
        if marker not in text:
            failures.append(f"human release manifest gate row differs: {gate}")
    for name, entry in manifest.get("external_release_evidence", {}).items():
        marker = f"| `{name}` | `{entry.get('status')}` | `{str(entry.get('passed')).lower()}` |"
        if marker not in text:
            failures.append(f"human release manifest external evidence row differs: {name}")


def verify(*, root: Path = ROOT, manifest_path: Path | None = None) -> list[str]:
    """Return every manifest failure; an empty list is the only passing result."""

    root = root.resolve()
    selected_manifest = (manifest_path or (root / "RELEASE_MANIFEST.json")).resolve()
    failures: list[str] = []
    manifest = _load_json(selected_manifest, failures, "RELEASE_MANIFEST.json")
    if not manifest:
        return failures
    if manifest.get("manifest_version") != "1.0":
        failures.append("unsupported release manifest version")
    _verify_sources(manifest, root, failures)
    _verify_dependencies(manifest, root, failures)
    _verify_release_inputs(manifest, root, failures)
    _verify_public_readme(root, failures)
    _verify_schemas(manifest, root, failures)
    _verify_decisions_and_gates(manifest, failures)
    _verify_evidence_ledgers(manifest, root, failures)
    _verify_a2a_gate_evidence(root, failures)
    _verify_human_manifest(manifest, root, failures)
    return failures


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--manifest", type=Path)
    args = parser.parse_args(argv)
    failures = verify(root=args.root, manifest_path=args.manifest)
    if failures:
        print("release manifest verification: FAIL", file=sys.stderr)
        for failure in failures:
            print(f"- {failure}", file=sys.stderr)
        return 1
    print("release manifest verification: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
