"""Setup marker, attempt, journal, and realized-state primitives."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import re
import tempfile
from pathlib import Path
from typing import Any, Literal, Mapping, cast

if os.name == "posix":
    import pwd

from agentnet import __version__
from agentnet.storage.migrations import MIGRATIONS

from .custody import (
    _MAX_UNIT_BYTES,
    _atomic_replace_exact,
    _atomic_write,
    _read_managed_exact,
    _read_managed_unit,
    _read_private_managed_file,
)
from .database import (
    _LIFECYCLE_PRESERVED_TABLES,
    _LIFECYCLE_SETUP_UPGRADE,
    _LIFECYCLE_SOURCE_SCHEMA,
    _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA,
)
from .models import SETUP_ROOT, ServerSetupError
from .preflight import _MAX_CONFIG_BYTES, _strict_json_bytes
from .systemd import LEGACY_COMMUNICATION_ONLY_UNITS, MANAGED_UNITS
SETUP_MARKER = SETUP_ROOT / "setup.json"
SETUP_ATTEMPT = SETUP_ROOT / "attempt.json"
SETUP_UPGRADE_JOURNAL = SETUP_ROOT / "upgrade.json"
_HEX64 = re.compile(r"^[a-f0-9]{64}$")
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
    ("0.1.46", "0.1.47"): MANAGED_UNITS,
    ("0.1.47", "0.1.48"): MANAGED_UNITS,
    ("0.1.48", "0.1.49"): MANAGED_UNITS,
    ("0.1.45", "0.1.50"): MANAGED_UNITS,
    ("0.1.46", "0.1.50"): MANAGED_UNITS,
    ("0.1.47", "0.1.50"): MANAGED_UNITS,
    ("0.1.48", "0.1.50"): MANAGED_UNITS,
    ("0.1.49", "0.1.50"): MANAGED_UNITS,
    ("0.1.45", "0.1.51"): MANAGED_UNITS,
    ("0.1.46", "0.1.51"): MANAGED_UNITS,
    ("0.1.47", "0.1.51"): MANAGED_UNITS,
    ("0.1.48", "0.1.51"): MANAGED_UNITS,
    ("0.1.49", "0.1.51"): MANAGED_UNITS,
    ("0.1.50", "0.1.51"): MANAGED_UNITS,
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
        ("0.1.46", "0.1.47"),
        ("0.1.47", "0.1.48"),
        ("0.1.48", "0.1.49"),
        ("0.1.45", "0.1.50"),
        ("0.1.46", "0.1.50"),
        ("0.1.47", "0.1.50"),
        ("0.1.48", "0.1.50"),
        ("0.1.49", "0.1.50"),
        ("0.1.45", "0.1.51"),
        ("0.1.46", "0.1.51"),
        ("0.1.47", "0.1.51"),
        ("0.1.48", "0.1.51"),
        ("0.1.49", "0.1.51"),
        ("0.1.50", "0.1.51"),
    }
)
# Blockers that mean "the response was lost", not "the operation was refused".
# Only these justify one bounded idempotent retry of a product command.
_RESPONSE_LOSS_BLOCKERS = frozenset({"invalid_product_evidence", "product_command_failed"})
_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v2"
_LEGACY_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v1"
_JOURNALED_CONFIG_KEYS = frozenset({"core_config", "core_oidc_config"})
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
    for value in cast(dict[str, Any], units).values():
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
