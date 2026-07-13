"""Fail-closed backup, restore, and compromise-rebuild planning.

This module never rotates a credential or resumes service. PostgreSQL commands
remain immutable plans for a separately authenticated operator runner. The
local SQLite profile additionally provides exact offline, owner-only,
no-overwrite copy and restore primitives. A backup becomes restore-eligible
only after its canonical manifest is matched to a seal held in separate trusted
custody.

SQLite support is an offline, exclusive byte-copy primitive for local
conformance only.  PostgreSQL custom archives do not prove HA or PITR.  Every
type below keeps those claims explicitly false.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sqlite3
import stat
import sys
from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from pathlib import Path
from typing import Any, Literal, cast

from cryptography.hazmat.primitives import serialization
from psycopg.conninfo import conninfo_to_dict

from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ValidationError
from agentnet.security.signatures import (
    P256KeyPair,
    b64url_encode,
    canonical_digest,
    canonical_json,
    load_public_key,
    verify_signature,
)
from agentnet.storage.backend import StoreBackend
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS


_SHA256 = re.compile(r"^[a-f0-9]{64}$")
_SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$")
_KEY_ID = re.compile(r"^[A-Za-z0-9_-]{16,128}$")
_SIGNATURE = re.compile(r"^[A-Za-z0-9_-]{32,512}$")
_ENVIRONMENT_NAME = re.compile(r"^[A-Z][A-Z0-9_]{0,127}$")
_SQLITE_HEADER = b"SQLite format 3\x00"
_POSTGRES_CUSTOM_HEADER = b"PGDMP"
_AUDIT_PROFILE = "agentnet.audit.checkpoint/1.0"
_AUDIT_PURPOSE = "agentnet.audit.checkpoint.v1"
_BACKUP_SEAL_PROFILE = "agentnet.backup.manifest-seal/1.0"
_BACKUP_SEAL_PURPOSE = "agentnet.backup.manifest-seal.v1"
_MAX_MANIFEST_BYTES = 1_048_576
_MAX_SEAL_BYTES = 131_072
_TARGET_INSPECTION_MAX_AGE = timedelta(minutes=5)
_VERIFIED_BACKUP_CAPABILITY = object()
_INSPECTED_TARGET_CAPABILITY = object()


class BackupBackend(StrEnum):
    POSTGRESQL = "postgresql"
    SQLITE = "sqlite"


class ArchiveFormat(StrEnum):
    POSTGRESQL_CUSTOM = "postgresql_custom"
    SQLITE_DATABASE = "sqlite_database"


class CredentialClass(StrEnum):
    IDENTITY_SIGNING = "identity_signing"
    RELAY_PEER = "relay_peer"
    RECORD_ENCRYPTION = "record_encryption"
    ARTIFACT_ENCRYPTION = "artifact_encryption"


class RebuildAction(StrEnum):
    ISOLATE_COMPROMISED_HOST = "isolate_compromised_host"
    REVOKE_COMPROMISED_AUTHORITY = "revoke_compromised_authority"
    REQUIRE_EMPTY_OFFLINE_TARGET = "require_empty_offline_target"
    RESTORE_SEALED_BACKUP = "restore_sealed_backup"
    ROTATE_IDENTITY_SIGNING = "rotate_identity_signing"
    ROTATE_RELAY_PEER = "rotate_relay_peer"
    ROTATE_RECORD_ENCRYPTION = "rotate_record_encryption"
    ROTATE_ARTIFACT_ENCRYPTION = "rotate_artifact_encryption"
    INVALIDATE_SESSIONS_AND_CAPABILITIES = "invalidate_sessions_and_capabilities"
    VERIFY_AUDIT_CONTINUITY = "verify_audit_continuity"
    REQUIRE_INDEPENDENT_RESUME_APPROVAL = "require_independent_resume_approval"


_REBUILD_ACTIONS = (
    RebuildAction.ISOLATE_COMPROMISED_HOST,
    RebuildAction.REVOKE_COMPROMISED_AUTHORITY,
    RebuildAction.REQUIRE_EMPTY_OFFLINE_TARGET,
    RebuildAction.RESTORE_SEALED_BACKUP,
    RebuildAction.ROTATE_IDENTITY_SIGNING,
    RebuildAction.ROTATE_RELAY_PEER,
    RebuildAction.ROTATE_RECORD_ENCRYPTION,
    RebuildAction.ROTATE_ARTIFACT_ENCRYPTION,
    RebuildAction.INVALIDATE_SESSIONS_AND_CAPABILITIES,
    RebuildAction.VERIFY_AUDIT_CONTINUITY,
    RebuildAction.REQUIRE_INDEPENDENT_RESUME_APPROVAL,
)


def _is_exact_int(value: object) -> bool:
    return type(value) is int


def _require_string(value: object, name: str, *, maximum: int = 4096) -> str:
    if type(value) is not str or not value or len(value) > maximum or "\x00" in value:
        raise ValidationError(f"{name} is invalid")
    return value


def _require_sha256(value: object, name: str) -> str:
    text = _require_string(value, name, maximum=64)
    if _SHA256.fullmatch(text) is None:
        raise ValidationError(f"{name} must be a lowercase SHA-256 digest")
    return text


def _require_exact_keys(value: Mapping[str, object], expected: frozenset[str], name: str) -> None:
    if not isinstance(value, Mapping) or any(type(key) is not str for key in value):
        raise ValidationError(f"{name} must be an object with string keys")
    if frozenset(value) != expected:
        raise ValidationError(f"{name} fields do not match the exact schema")


def _require_utc_seconds(value: datetime, name: str) -> datetime:
    if type(value) is not datetime or value.tzinfo is None or value.utcoffset() != timedelta(0):
        raise ValidationError(f"{name} must be a timezone-aware UTC timestamp")
    if value.microsecond:
        raise ValidationError(f"{name} must have whole-second precision")
    return value


def _timestamp_text(value: datetime) -> str:
    return _require_utc_seconds(value, "timestamp").strftime("%Y-%m-%dT%H:%M:%SZ")


def _parse_timestamp(value: object, name: str) -> datetime:
    text = _require_string(value, name, maximum=20)
    try:
        parsed = datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=UTC)
    except ValueError as exc:
        raise ValidationError(f"{name} must be canonical UTC whole-second time") from exc
    return parsed


def _require_path(value: Path, name: str) -> Path:
    # ``Path`` is a platform factory whose concrete values are PosixPath or
    # WindowsPath.  Accept only that standard hierarchy, never strings or
    # path-like caller objects with executable coercion hooks.
    if not isinstance(value, Path):
        raise ValidationError(f"{name} must be a pathlib.Path")
    try:
        # Do not resolve symlinks here: the caller-supplied final component (or
        # custody directory) must remain visible to lstat/O_NOFOLLOW checks.
        return value.expanduser().absolute()
    except OSError as exc:
        raise ValidationError(f"{name} could not be normalized") from exc


def _safe_filename(value: object) -> str:
    filename = _require_string(value, "backup filename", maximum=255)
    if filename in {".", ".."} or Path(filename).name != filename or "/" in filename or "\\" in filename:
        raise ValidationError("backup filename must be a single safe path component")
    return filename


@dataclass(frozen=True, slots=True)
class SubprocessCommand:
    argv: tuple[str, ...]
    required_environment: tuple[str, ...]
    required_umask: int = 0o077
    shell: Literal[False] = False

    def __post_init__(self) -> None:
        if type(self.argv) is not tuple or not self.argv:
            raise ValidationError("subprocess command requires a non-empty argv tuple")
        for argument in self.argv:
            _require_string(argument, "subprocess argument")
        if type(self.required_environment) is not tuple or len(set(self.required_environment)) != len(
            self.required_environment
        ):
            raise ValidationError("required subprocess environment must be a unique tuple")
        if any(
            type(name) is not str or _ENVIRONMENT_NAME.fullmatch(name) is None
            for name in self.required_environment
        ):
            raise ValidationError("required subprocess environment name is invalid")
        if self.required_umask != 0o077 or self.shell is not False:
            raise ValidationError("backup commands require umask 077 and shell=False")


@dataclass(frozen=True, slots=True)
class AuditCheckpointBinding:
    algorithm: Literal["ES256"]
    last_hash: str
    last_sequence: int
    profile: str
    signer_key_id: str
    signature: str

    def __post_init__(self) -> None:
        if self.algorithm != "ES256":
            raise ValidationError("audit checkpoint algorithm must be ES256")
        _require_sha256(self.last_hash, "audit checkpoint hash")
        if not _is_exact_int(self.last_sequence) or self.last_sequence < 0:
            raise ValidationError("audit checkpoint sequence must be a non-negative integer")
        if self.profile != _AUDIT_PROFILE:
            raise ValidationError("audit checkpoint profile is unsupported")
        if type(self.signer_key_id) is not str or _KEY_ID.fullmatch(self.signer_key_id) is None:
            raise ValidationError("audit checkpoint signer key id is invalid")
        if type(self.signature) is not str or _SIGNATURE.fullmatch(self.signature) is None:
            raise ValidationError("audit checkpoint signature encoding is invalid")

    @classmethod
    def parse(cls, value: Mapping[str, object]) -> "AuditCheckpointBinding":
        _require_exact_keys(
            value,
            frozenset({"algorithm", "last_hash", "last_sequence", "profile", "signer_key_id", "signature"}),
            "audit checkpoint",
        )
        return cls(
            algorithm=cast(Literal["ES256"], value["algorithm"]),
            last_hash=cast(str, value["last_hash"]),
            last_sequence=cast(int, value["last_sequence"]),
            profile=cast(str, value["profile"]),
            signer_key_id=cast(str, value["signer_key_id"]),
            signature=cast(str, value["signature"]),
        )

    def signed_fields(self) -> dict[str, object]:
        return {
            "algorithm": self.algorithm,
            "last_hash": self.last_hash,
            "last_sequence": self.last_sequence,
            "profile": self.profile,
            "signer_key_id": self.signer_key_id,
        }

    def as_dict(self) -> dict[str, object]:
        return self.signed_fields() | {"signature": self.signature}

    @property
    def digest(self) -> str:
        return canonical_digest(self.as_dict())

    def verify(self, public_key_pem: str) -> None:
        key = load_public_key(public_key_pem)
        key_id = b64url_encode(
            hashlib.sha256(
                key.public_bytes(
                    serialization.Encoding.DER,
                    serialization.PublicFormat.SubjectPublicKeyInfo,
                )
            ).digest()
        )
        if key_id != self.signer_key_id:
            raise AuthenticationError("audit checkpoint signer key binding mismatch")
        verify_signature(public_key_pem, _AUDIT_PURPOSE, self.signed_fields(), self.signature)


@dataclass(frozen=True, slots=True)
class BackupBinding:
    schema_version: int
    domain_id: str
    domain_status: Literal["active", "quarantined", "revoked"]
    policy_revision: int
    revocation_epoch: int
    audit_checkpoint: AuditCheckpointBinding

    def __post_init__(self) -> None:
        if not _is_exact_int(self.schema_version) or self.schema_version < 1:
            raise ValidationError("backup schema version is invalid")
        domain = _require_string(self.domain_id, "backup domain id", maximum=256)
        if _SAFE_ID.fullmatch(domain) is None:
            raise ValidationError("backup domain id is invalid")
        if self.domain_status not in {"active", "quarantined", "revoked"}:
            raise ValidationError("backup domain status is invalid")
        if not _is_exact_int(self.policy_revision) or self.policy_revision < 1:
            raise ValidationError("backup policy revision is invalid")
        if not _is_exact_int(self.revocation_epoch) or self.revocation_epoch < 1:
            raise ValidationError("backup revocation epoch is invalid")
        if type(self.audit_checkpoint) is not AuditCheckpointBinding:
            raise ValidationError("backup audit checkpoint is invalid")

    @classmethod
    def parse(cls, value: Mapping[str, object]) -> "BackupBinding":
        _require_exact_keys(
            value,
            frozenset(
                {
                    "schema_version",
                    "domain_id",
                    "domain_status",
                    "policy_revision",
                    "revocation_epoch",
                    "audit_checkpoint",
                }
            ),
            "backup binding",
        )
        checkpoint = value["audit_checkpoint"]
        if not isinstance(checkpoint, Mapping):
            raise ValidationError("backup audit checkpoint must be an object")
        return cls(
            schema_version=cast(int, value["schema_version"]),
            domain_id=cast(str, value["domain_id"]),
            domain_status=cast(Any, value["domain_status"]),
            policy_revision=cast(int, value["policy_revision"]),
            revocation_epoch=cast(int, value["revocation_epoch"]),
            audit_checkpoint=AuditCheckpointBinding.parse(checkpoint),
        )

    def as_dict(self) -> dict[str, object]:
        return {
            "schema_version": self.schema_version,
            "domain_id": self.domain_id,
            "domain_status": self.domain_status,
            "policy_revision": self.policy_revision,
            "revocation_epoch": self.revocation_epoch,
            "audit_checkpoint": self.audit_checkpoint.as_dict(),
        }

    @property
    def digest(self) -> str:
        return canonical_digest(self.as_dict())


@dataclass(frozen=True, slots=True)
class PostgreSQLBackupPlan:
    archive_path: Path
    manifest_path: Path
    source_fingerprint: str
    binding: BackupBinding
    command: SubprocessCommand
    archive_format: Literal[ArchiveFormat.POSTGRESQL_CUSTOM] = ArchiveFormat.POSTGRESQL_CUSTOM
    consistent_snapshot_requested: Literal[True] = True
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        if self.archive_path == self.manifest_path:
            raise ValidationError("backup archive and manifest paths must differ")
        _require_sha256(self.source_fingerprint, "PostgreSQL source fingerprint")
        if type(self.binding) is not BackupBinding or type(self.command) is not SubprocessCommand:
            raise ValidationError("PostgreSQL backup plan binding or command is invalid")
        if (
            self.archive_format is not ArchiveFormat.POSTGRESQL_CUSTOM
            or self.consistent_snapshot_requested is not True
            or self.ha_proven is not False
            or self.pitr_proven is not False
        ):
            raise ValidationError("PostgreSQL backup plan claim flags are invalid")


@dataclass(frozen=True, slots=True)
class SQLiteBackupPlan:
    source_path: Path
    archive_path: Path
    manifest_path: Path
    source_fingerprint: str
    source_sha256: str
    source_size: int
    archive_parent_fingerprint: str
    manifest_parent_fingerprint: str
    binding: BackupBinding
    primitive: Literal["offline_exclusive_byte_copy"] = "offline_exclusive_byte_copy"
    archive_format: Literal[ArchiveFormat.SQLITE_DATABASE] = ArchiveFormat.SQLITE_DATABASE
    source_offline_required: Literal[True] = True
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        if len({self.source_path, self.archive_path, self.manifest_path}) != 3:
            raise ValidationError("SQLite source, archive, and manifest paths must differ")
        _require_sha256(self.source_fingerprint, "SQLite source fingerprint")
        _require_sha256(self.source_sha256, "SQLite source digest")
        _require_sha256(self.archive_parent_fingerprint, "SQLite archive parent fingerprint")
        _require_sha256(self.manifest_parent_fingerprint, "SQLite manifest parent fingerprint")
        if not _is_exact_int(self.source_size) or self.source_size < len(_SQLITE_HEADER):
            raise ValidationError("SQLite source size is invalid")
        if type(self.binding) is not BackupBinding:
            raise ValidationError("SQLite backup binding is invalid")
        if (
            self.primitive != "offline_exclusive_byte_copy"
            or self.archive_format is not ArchiveFormat.SQLITE_DATABASE
            or self.source_offline_required is not True
            or self.ha_proven is not False
            or self.pitr_proven is not False
        ):
            raise ValidationError("SQLite backup plan claim flags are invalid")


BackupPlan = PostgreSQLBackupPlan | SQLiteBackupPlan


@dataclass(frozen=True, slots=True)
class BackupManifest:
    backup_id: str
    backend: BackupBackend
    archive_format: ArchiveFormat
    filename: str
    sha256: str
    size: int
    created_at: datetime
    source_fingerprint: str
    binding: BackupBinding
    version: Literal[1] = 1
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        backup_id = _require_string(self.backup_id, "backup id", maximum=256)
        if _SAFE_ID.fullmatch(backup_id) is None:
            raise ValidationError("backup id is invalid")
        if type(self.backend) is not BackupBackend or type(self.archive_format) is not ArchiveFormat:
            raise ValidationError("backup manifest backend or archive format is invalid")
        if (self.backend, self.archive_format) not in {
            (BackupBackend.POSTGRESQL, ArchiveFormat.POSTGRESQL_CUSTOM),
            (BackupBackend.SQLITE, ArchiveFormat.SQLITE_DATABASE),
        }:
            raise ValidationError("backup backend and archive format do not match")
        _safe_filename(self.filename)
        _require_sha256(self.sha256, "backup archive digest")
        if not _is_exact_int(self.size) or self.size < 1:
            raise ValidationError("backup archive size is invalid")
        _require_utc_seconds(self.created_at, "backup creation time")
        _require_sha256(self.source_fingerprint, "backup source fingerprint")
        if type(self.binding) is not BackupBinding:
            raise ValidationError("backup manifest binding is invalid")
        if (
            not _is_exact_int(self.version)
            or self.version != 1
            or self.ha_proven is not False
            or self.pitr_proven is not False
        ):
            raise ValidationError("backup manifest version or claim flags are invalid")

    @classmethod
    def parse(cls, value: Mapping[str, object]) -> "BackupManifest":
        _require_exact_keys(
            value,
            frozenset(
                {
                    "version",
                    "backup_id",
                    "backend",
                    "archive_format",
                    "filename",
                    "sha256",
                    "size",
                    "created_at",
                    "source_fingerprint",
                    "binding",
                    "ha_proven",
                    "pitr_proven",
                }
            ),
            "backup manifest",
        )
        binding = value["binding"]
        if not isinstance(binding, Mapping):
            raise ValidationError("backup manifest binding must be an object")
        try:
            backend = BackupBackend(value["backend"])
            archive_format = ArchiveFormat(value["archive_format"])
        except (TypeError, ValueError) as exc:
            raise ValidationError("backup manifest backend or format is invalid") from exc
        return cls(
            version=cast(Literal[1], value["version"]),
            backup_id=cast(str, value["backup_id"]),
            backend=backend,
            archive_format=archive_format,
            filename=cast(str, value["filename"]),
            sha256=cast(str, value["sha256"]),
            size=cast(int, value["size"]),
            created_at=_parse_timestamp(value["created_at"], "backup creation time"),
            source_fingerprint=cast(str, value["source_fingerprint"]),
            binding=BackupBinding.parse(binding),
            ha_proven=cast(Literal[False], value["ha_proven"]),
            pitr_proven=cast(Literal[False], value["pitr_proven"]),
        )

    def as_dict(self) -> dict[str, object]:
        return {
            "version": self.version,
            "backup_id": self.backup_id,
            "backend": self.backend.value,
            "archive_format": self.archive_format.value,
            "filename": self.filename,
            "sha256": self.sha256,
            "size": self.size,
            "created_at": _timestamp_text(self.created_at),
            "source_fingerprint": self.source_fingerprint,
            "binding": self.binding.as_dict(),
            "ha_proven": self.ha_proven,
            "pitr_proven": self.pitr_proven,
        }

    @property
    def digest(self) -> str:
        return canonical_digest(self.as_dict())


@dataclass(frozen=True, slots=True)
class ManifestSeal:
    version: Literal[1]
    profile: Literal["agentnet.backup.manifest-seal/1.0"]
    algorithm: Literal["ES256"]
    signer_key_id: str
    signer_key_epoch: int
    trust_root_revision: int
    sealed_at: datetime
    backup_id: str
    backend: BackupBackend
    archive_format: ArchiveFormat
    archive_filename: str
    archive_sha256: str
    archive_size: int
    manifest_sha256: str
    source_fingerprint: str
    domain_id: str
    schema_version: int
    domain_status: Literal["active", "quarantined", "revoked"]
    policy_revision: int
    revocation_epoch: int
    audit_checkpoint_digest: str
    audit_checkpoint_signer_key_id: str
    audit_last_sequence: int
    audit_last_hash: str
    signature: str

    def __post_init__(self) -> None:
        if (
            not _is_exact_int(self.version)
            or self.version != 1
            or self.profile != _BACKUP_SEAL_PROFILE
            or self.algorithm != "ES256"
        ):
            raise ValidationError("backup manifest seal version, profile, or algorithm is invalid")
        if _KEY_ID.fullmatch(self.signer_key_id) is None:
            raise ValidationError("backup manifest seal signer key id is invalid")
        if not _is_exact_int(self.signer_key_epoch) or self.signer_key_epoch < 1:
            raise ValidationError("backup manifest seal signer epoch is invalid")
        if not _is_exact_int(self.trust_root_revision) or self.trust_root_revision < 1:
            raise ValidationError("backup manifest seal trust-root revision is invalid")
        _require_utc_seconds(self.sealed_at, "backup manifest seal time")
        backup_id = _require_string(self.backup_id, "sealed backup id", maximum=256)
        if _SAFE_ID.fullmatch(backup_id) is None:
            raise ValidationError("sealed backup id is invalid")
        if type(self.backend) is not BackupBackend or type(self.archive_format) is not ArchiveFormat:
            raise ValidationError("sealed backup backend or archive format is invalid")
        _safe_filename(self.archive_filename)
        _require_sha256(self.archive_sha256, "sealed archive digest")
        if not _is_exact_int(self.archive_size) or self.archive_size < 1:
            raise ValidationError("sealed archive size is invalid")
        _require_sha256(self.manifest_sha256, "sealed manifest digest")
        _require_sha256(self.source_fingerprint, "sealed source fingerprint")
        domain = _require_string(self.domain_id, "sealed domain id", maximum=256)
        if _SAFE_ID.fullmatch(domain) is None:
            raise ValidationError("sealed domain id is invalid")
        if not _is_exact_int(self.schema_version) or self.schema_version < 1:
            raise ValidationError("sealed schema version is invalid")
        if self.domain_status not in {"active", "quarantined", "revoked"}:
            raise ValidationError("sealed domain status is invalid")
        if not _is_exact_int(self.policy_revision) or self.policy_revision < 1:
            raise ValidationError("sealed policy revision is invalid")
        if not _is_exact_int(self.revocation_epoch) or self.revocation_epoch < 1:
            raise ValidationError("sealed revocation epoch is invalid")
        _require_sha256(self.audit_checkpoint_digest, "sealed audit checkpoint digest")
        if _KEY_ID.fullmatch(self.audit_checkpoint_signer_key_id) is None:
            raise ValidationError("sealed audit checkpoint signer key id is invalid")
        if not _is_exact_int(self.audit_last_sequence) or self.audit_last_sequence < 0:
            raise ValidationError("sealed audit sequence is invalid")
        _require_sha256(self.audit_last_hash, "sealed audit hash")
        if _SIGNATURE.fullmatch(self.signature) is None:
            raise ValidationError("backup manifest seal signature is invalid")

    @classmethod
    def create(
        cls,
        *,
        manifest: BackupManifest,
        signer: P256KeyPair,
        signer_key_epoch: int,
        trust_root_revision: int,
        sealed_at: datetime | None = None,
    ) -> "ManifestSeal":
        if type(manifest) is not BackupManifest or type(signer) is not P256KeyPair:
            raise ValidationError("backup manifest seal requires exact manifest and signer objects")
        when = sealed_at or datetime.now(UTC).replace(microsecond=0)
        when = _require_utc_seconds(when, "backup manifest seal time")
        if manifest.created_at > when:
            raise ValidationError("backup manifest cannot be created after its seal")
        key = load_public_key(signer.public_pem)
        signer_key_id = b64url_encode(
            hashlib.sha256(
                key.public_bytes(
                    serialization.Encoding.DER,
                    serialization.PublicFormat.SubjectPublicKeyInfo,
                )
            ).digest()
        )
        checkpoint = manifest.binding.audit_checkpoint
        fields: dict[str, object] = {
            "version": 1,
            "profile": _BACKUP_SEAL_PROFILE,
            "algorithm": "ES256",
            "signer_key_id": signer_key_id,
            "signer_key_epoch": signer_key_epoch,
            "trust_root_revision": trust_root_revision,
            "sealed_at": _timestamp_text(when),
            "backup_id": manifest.backup_id,
            "backend": manifest.backend.value,
            "archive_format": manifest.archive_format.value,
            "archive_filename": manifest.filename,
            "archive_sha256": manifest.sha256,
            "archive_size": manifest.size,
            "manifest_sha256": manifest.digest,
            "source_fingerprint": manifest.source_fingerprint,
            "domain_id": manifest.binding.domain_id,
            "schema_version": manifest.binding.schema_version,
            "domain_status": manifest.binding.domain_status,
            "policy_revision": manifest.binding.policy_revision,
            "revocation_epoch": manifest.binding.revocation_epoch,
            "audit_checkpoint_digest": checkpoint.digest,
            "audit_checkpoint_signer_key_id": checkpoint.signer_key_id,
            "audit_last_sequence": checkpoint.last_sequence,
            "audit_last_hash": checkpoint.last_hash,
        }
        return cls.parse(fields | {"signature": signer.sign(_BACKUP_SEAL_PURPOSE, fields)})

    @classmethod
    def parse(cls, value: Mapping[str, object]) -> "ManifestSeal":
        expected = frozenset(
            {
                "version", "profile", "algorithm", "signer_key_id", "signer_key_epoch",
                "trust_root_revision", "sealed_at", "backup_id", "backend", "archive_format",
                "archive_filename", "archive_sha256", "archive_size", "manifest_sha256",
                "source_fingerprint", "domain_id", "schema_version", "domain_status",
                "policy_revision", "revocation_epoch", "audit_checkpoint_digest",
                "audit_checkpoint_signer_key_id", "audit_last_sequence", "audit_last_hash",
                "signature",
            }
        )
        _require_exact_keys(value, expected, "backup manifest seal")
        try:
            return cls(
                version=cast(Literal[1], value["version"]),
                profile=cast(Literal["agentnet.backup.manifest-seal/1.0"], value["profile"]),
                algorithm=cast(Literal["ES256"], value["algorithm"]),
                signer_key_id=cast(str, value["signer_key_id"]),
                signer_key_epoch=cast(int, value["signer_key_epoch"]),
                trust_root_revision=cast(int, value["trust_root_revision"]),
                sealed_at=_parse_timestamp(value["sealed_at"], "backup manifest seal time"),
                backup_id=cast(str, value["backup_id"]),
                backend=BackupBackend(value["backend"]),
                archive_format=ArchiveFormat(value["archive_format"]),
                archive_filename=cast(str, value["archive_filename"]),
                archive_sha256=cast(str, value["archive_sha256"]),
                archive_size=cast(int, value["archive_size"]),
                manifest_sha256=cast(str, value["manifest_sha256"]),
                source_fingerprint=cast(str, value["source_fingerprint"]),
                domain_id=cast(str, value["domain_id"]),
                schema_version=cast(int, value["schema_version"]),
                domain_status=cast(Any, value["domain_status"]),
                policy_revision=cast(int, value["policy_revision"]),
                revocation_epoch=cast(int, value["revocation_epoch"]),
                audit_checkpoint_digest=cast(str, value["audit_checkpoint_digest"]),
                audit_checkpoint_signer_key_id=cast(str, value["audit_checkpoint_signer_key_id"]),
                audit_last_sequence=cast(int, value["audit_last_sequence"]),
                audit_last_hash=cast(str, value["audit_last_hash"]),
                signature=cast(str, value["signature"]),
            )
        except (TypeError, ValueError) as exc:
            raise ValidationError("backup manifest seal enum fields are invalid") from exc

    def signed_fields(self) -> dict[str, object]:
        return {
            "version": self.version,
            "profile": self.profile,
            "algorithm": self.algorithm,
            "signer_key_id": self.signer_key_id,
            "signer_key_epoch": self.signer_key_epoch,
            "trust_root_revision": self.trust_root_revision,
            "sealed_at": _timestamp_text(self.sealed_at),
            "backup_id": self.backup_id,
            "backend": self.backend.value,
            "archive_format": self.archive_format.value,
            "archive_filename": self.archive_filename,
            "archive_sha256": self.archive_sha256,
            "archive_size": self.archive_size,
            "manifest_sha256": self.manifest_sha256,
            "source_fingerprint": self.source_fingerprint,
            "domain_id": self.domain_id,
            "schema_version": self.schema_version,
            "domain_status": self.domain_status,
            "policy_revision": self.policy_revision,
            "revocation_epoch": self.revocation_epoch,
            "audit_checkpoint_digest": self.audit_checkpoint_digest,
            "audit_checkpoint_signer_key_id": self.audit_checkpoint_signer_key_id,
            "audit_last_sequence": self.audit_last_sequence,
            "audit_last_hash": self.audit_last_hash,
        }

    def as_dict(self) -> dict[str, object]:
        return self.signed_fields() | {"signature": self.signature}


@dataclass(frozen=True, slots=True, init=False)
class VerifiedBackup:
    backend: BackupBackend
    archive_path: Path
    manifest_path: Path
    manifest: BackupManifest
    seal: ManifestSeal
    verified_at: datetime
    _capability: object = field(repr=False, compare=False)

    def __init__(
        self,
        *,
        backend: BackupBackend,
        archive_path: Path,
        manifest_path: Path,
        manifest: BackupManifest,
        seal: ManifestSeal,
        verified_at: datetime,
        _capability: object,
    ) -> None:
        if _capability is not _VERIFIED_BACKUP_CAPABILITY:
            raise AuthenticationError("verified backup capability was not produced by verification")
        object.__setattr__(self, "backend", backend)
        object.__setattr__(self, "archive_path", archive_path)
        object.__setattr__(self, "manifest_path", manifest_path)
        object.__setattr__(self, "manifest", manifest)
        object.__setattr__(self, "seal", seal)
        object.__setattr__(self, "verified_at", _require_utc_seconds(verified_at, "backup verification time"))
        object.__setattr__(self, "_capability", _capability)

    @classmethod
    def _create(
        cls,
        *,
        archive_path: Path,
        manifest_path: Path,
        manifest: BackupManifest,
        seal: ManifestSeal,
        verified_at: datetime,
    ) -> "VerifiedBackup":
        return cls(
            backend=manifest.backend,
            archive_path=archive_path,
            manifest_path=manifest_path,
            manifest=manifest,
            seal=seal,
            verified_at=verified_at,
            _capability=_VERIFIED_BACKUP_CAPABILITY,
        )


@dataclass(frozen=True, slots=True, init=False)
class RestoreTargetInspection:
    backend: BackupBackend
    target_fingerprint: str
    inspected_at: datetime
    empty: Literal[True]
    application_offline: Literal[True]
    _capability: object = field(repr=False, compare=False)

    def __init__(
        self,
        *,
        backend: BackupBackend,
        target_fingerprint: str,
        inspected_at: datetime,
        empty: Literal[True],
        application_offline: Literal[True],
        _capability: object,
    ) -> None:
        if _capability is not _INSPECTED_TARGET_CAPABILITY:
            raise AuthenticationError("restore target capability was not produced by inspection")
        _require_sha256(target_fingerprint, "restore target fingerprint")
        if empty is not True or application_offline is not True:
            raise ValidationError("restore target must be empty and application-offline")
        object.__setattr__(self, "backend", backend)
        object.__setattr__(self, "target_fingerprint", target_fingerprint)
        object.__setattr__(self, "inspected_at", _require_utc_seconds(inspected_at, "target inspection time"))
        object.__setattr__(self, "empty", empty)
        object.__setattr__(self, "application_offline", application_offline)
        object.__setattr__(self, "_capability", _capability)

    @classmethod
    def _create(
        cls,
        *,
        backend: BackupBackend,
        target_fingerprint: str,
        inspected_at: datetime,
    ) -> "RestoreTargetInspection":
        return cls(
            backend=backend,
            target_fingerprint=target_fingerprint,
            inspected_at=inspected_at,
            empty=True,
            application_offline=True,
            _capability=_INSPECTED_TARGET_CAPABILITY,
        )


@dataclass(frozen=True, slots=True)
class PostgreSQLRestorePlan:
    backup: VerifiedBackup
    target: RestoreTargetInspection
    command: SubprocessCommand
    required_empty_target: Literal[True] = True
    required_application_offline: Literal[True] = True
    restore_completed: Literal[False] = False
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        if self.backup.backend is not BackupBackend.POSTGRESQL or self.target.backend is not BackupBackend.POSTGRESQL:
            raise ValidationError("PostgreSQL restore plan backend mismatch")
        if type(self.command) is not SubprocessCommand:
            raise ValidationError("PostgreSQL restore command is invalid")
        if not all(
            (
                self.required_empty_target is True,
                self.required_application_offline is True,
                self.restore_completed is False,
                self.ha_proven is False,
                self.pitr_proven is False,
            )
        ):
            raise ValidationError("PostgreSQL restore claim flags are invalid")


@dataclass(frozen=True, slots=True)
class SQLiteRestorePlan:
    backup: VerifiedBackup
    target: RestoreTargetInspection
    target_path: Path
    primitive: Literal["offline_exclusive_byte_copy_to_absent_target"] = (
        "offline_exclusive_byte_copy_to_absent_target"
    )
    required_empty_target: Literal[True] = True
    required_application_offline: Literal[True] = True
    restore_completed: Literal[False] = False
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        if self.backup.backend is not BackupBackend.SQLITE or self.target.backend is not BackupBackend.SQLITE:
            raise ValidationError("SQLite restore plan backend mismatch")
        if self.primitive != "offline_exclusive_byte_copy_to_absent_target":
            raise ValidationError("SQLite restore primitive is invalid")
        if not all(
            (
                self.required_empty_target is True,
                self.required_application_offline is True,
                self.restore_completed is False,
                self.ha_proven is False,
                self.pitr_proven is False,
            )
        ):
            raise ValidationError("SQLite restore claim flags are invalid")


RestorePlan = PostgreSQLRestorePlan | SQLiteRestorePlan


@dataclass(frozen=True, slots=True)
class CredentialRotationRequirement:
    credential_class: CredentialClass
    state: Literal["required_not_executed"] = "required_not_executed"

    def __post_init__(self) -> None:
        if type(self.credential_class) is not CredentialClass or self.state != "required_not_executed":
            raise ValidationError("credential rotation requirement is invalid")


@dataclass(frozen=True, slots=True)
class CompromiseRebuildPlan:
    domain_id: str
    backend: BackupBackend
    backup_id: str
    manifest_sha256: str
    audit_checkpoint_digest: str
    ordered_actions: tuple[RebuildAction, ...]
    credential_rotations: tuple[CredentialRotationRequirement, ...]
    state: Literal["plan_only_not_executed"] = "plan_only_not_executed"
    completed_actions: tuple[RebuildAction, ...] = ()
    restore_completed: Literal[False] = False
    rotations_completed: Literal[False] = False
    service_safe_to_resume: Literal[False] = False
    ha_proven: Literal[False] = False
    pitr_proven: Literal[False] = False

    def __post_init__(self) -> None:
        if self.ordered_actions != _REBUILD_ACTIONS or self.completed_actions != ():
            raise ValidationError("compromise rebuild actions are not the required unexecuted sequence")
        if tuple(item.credential_class for item in self.credential_rotations) != tuple(CredentialClass):
            raise ValidationError("compromise rebuild requires every credential class exactly once")
        if any(item.state != "required_not_executed" for item in self.credential_rotations):
            raise ValidationError("compromise credential rotations must remain unexecuted")
        if (
            self.state != "plan_only_not_executed"
            or self.restore_completed is not False
            or self.rotations_completed is not False
            or self.service_safe_to_resume is not False
            or self.ha_proven is not False
            or self.pitr_proven is not False
        ):
            raise ValidationError("compromise rebuild plan overclaims executed or recovery state")


@dataclass(frozen=True, slots=True)
class _FileFacts:
    path: Path
    sha256: str
    size: int
    header: bytes
    content: bytes | None = None


@dataclass(frozen=True, slots=True)
class _InstalledFile:
    path: Path
    parent_device: int
    parent_inode: int
    device: int
    inode: int
    sha256: str
    size: int


@dataclass(slots=True)
class _PublicationState:
    temporary_present: bool = True
    installed: _InstalledFile | None = None
    directory_durable: bool = False


_BACKUP_EXECUTION_CAPABILITY = object()
_RESTORE_EXECUTION_CAPABILITY = object()


class PublicationOutcomeUnknown(GateBlocked):
    """A final name exists but its directory durability could not be proven."""

    def __init__(self, installed: _InstalledFile) -> None:
        super().__init__(
            "filesystem_publication",
            "published bytes have unknown durability and were retained for operator quarantine",
        )
        self.installed = installed


@dataclass(frozen=True, slots=True, init=False)
class SQLiteBackupExecution:
    manifest: BackupManifest
    _archive: _InstalledFile = field(repr=False, compare=False)
    _manifest: _InstalledFile = field(repr=False, compare=False)
    _capability: object = field(repr=False, compare=False)

    def __init__(
        self,
        *,
        manifest: BackupManifest,
        archive: _InstalledFile,
        manifest_install: _InstalledFile,
        _capability: object,
    ) -> None:
        if _capability is not _BACKUP_EXECUTION_CAPABILITY:
            raise AuthenticationError("backup execution receipt was not produced by execution")
        object.__setattr__(self, "manifest", manifest)
        object.__setattr__(self, "_archive", archive)
        object.__setattr__(self, "_manifest", manifest_install)
        object.__setattr__(self, "_capability", _capability)


@dataclass(frozen=True, slots=True, init=False)
class SQLiteRestoreExecution:
    path: Path
    _installed: _InstalledFile = field(repr=False, compare=False)
    _capability: object = field(repr=False, compare=False)

    def __init__(self, *, path: Path, installed: _InstalledFile, _capability: object) -> None:
        if _capability is not _RESTORE_EXECUTION_CAPABILITY:
            raise AuthenticationError("restore execution receipt was not produced by execution")
        object.__setattr__(self, "path", path)
        object.__setattr__(self, "_installed", installed)
        object.__setattr__(self, "_capability", _capability)


def validate_owner_only_directory(path: Path) -> Path:
    """Return an absolute directory only when it is non-symlink and owner-only."""

    normalized = _require_path(path, "directory")
    try:
        metadata = normalized.lstat()
    except OSError as exc:
        raise ValidationError("owner-only directory is unavailable") from exc
    if (
        not stat.S_ISDIR(metadata.st_mode)
        or stat.S_ISLNK(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_mode & 0o077
        or metadata.st_mode & 0o300 != 0o300
    ):
        raise ValidationError("directory must be owned by the current user with no group/other access")
    return normalized


def _stable_file_facts(
    path: Path,
    *,
    header_size: int,
    expected_header: bytes | None = None,
    capture_content: bool = False,
    maximum_size: int | None = None,
) -> _FileFacts:
    normalized = _require_path(path, "file")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(normalized, flags)
    except OSError as exc:
        raise ValidationError("backup file is unavailable or unsafe") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or not before.st_mode & stat.S_IRUSR
        ):
            raise ValidationError("backup file must be an owner-only regular file")
        if maximum_size is not None and before.st_size > maximum_size:
            raise ValidationError("backup file exceeds the allowed size")
        digest = hashlib.sha256()
        header = b""
        content = bytearray() if capture_content else None
        while True:
            chunk = os.read(descriptor, 1_048_576)
            if not chunk:
                break
            digest.update(chunk)
            if len(header) < header_size:
                header += chunk[: header_size - len(header)]
            if content is not None:
                content.extend(chunk)
        after = os.fstat(descriptor)
        stable_before = (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        )
        stable_after = (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        )
        if stable_before != stable_after:
            raise ConflictError("backup file changed while it was being inspected")
        if expected_header is not None and header != expected_header:
            raise ValidationError("backup archive header does not match its declared format")
        return _FileFacts(
            path=normalized,
            sha256=digest.hexdigest(),
            size=after.st_size,
            header=header,
            content=bytes(content) if content is not None else None,
        )
    finally:
        os.close(descriptor)


def _require_absent(path: Path, name: str) -> Path:
    normalized = _require_path(path, name)
    validate_owner_only_directory(normalized.parent)
    try:
        normalized.lstat()
    except FileNotFoundError:
        return normalized
    except OSError as exc:
        raise ValidationError(f"{name} could not be inspected") from exc
    raise ConflictError(f"{name} must not already exist")


def _parent_fingerprint(path: Path, *, role: str) -> str:
    normalized = _require_path(path, role)
    parent = validate_owner_only_directory(normalized.parent)
    metadata = parent.lstat()
    return canonical_digest(
        {
            "role": role,
            "path": str(normalized),
            "parent_device": metadata.st_dev,
            "parent_inode": metadata.st_ino,
        }
    )


def _require_sqlite_offline_sidecars_absent(source: Path) -> None:
    for suffix in ("-wal", "-shm", "-journal"):
        candidate = source.with_name(source.name + suffix)
        try:
            candidate.lstat()
        except FileNotFoundError:
            continue
        except OSError as exc:
            raise ValidationError("SQLite sidecar state could not be inspected") from exc
        raise GateBlocked("sqlite_offline_backup", "SQLite WAL/journal sidecars must be absent")


def _verify_audit_rows(rows: list[Any]) -> tuple[int, str]:
    previous_hash = "0" * 64
    expected_sequence = 1
    for row in rows:
        try:
            sequence = int(row["sequence"])
            occurred_at = int(row["occurred_at"])
            record_json = str(row["record_json"])
            stored_previous = str(row["previous_hash"])
            stored_hash = str(row["record_hash"])
        except Exception as exc:
            raise GateBlocked("audit_chain", "audit chain row is malformed") from exc
        if sequence != expected_sequence:
            raise GateBlocked("audit_chain", "audit sequence is not contiguous")
        expected_hash = hashlib.sha256(
            previous_hash.encode("ascii")
            + b"\x00"
            + str(occurred_at).encode("ascii")
            + b"\x00"
            + record_json.encode("utf-8")
        ).hexdigest()
        if stored_previous != previous_hash or stored_hash != expected_hash:
            raise GateBlocked("audit_chain", "audit hash chain verification failed")
        previous_hash = stored_hash
        expected_sequence += 1
    return expected_sequence - 1, previous_hash


def capture_backup_binding(
    store: StoreBackend,
    *,
    domain_id: str,
    checkpoint: AuditCheckpointBinding | Mapping[str, object],
    audit_public_key_pem: str,
) -> BackupBinding:
    """Capture domain/schema/audit head coherently without writing store state."""

    domain = _require_string(domain_id, "backup domain id", maximum=256)
    if _SAFE_ID.fullmatch(domain) is None:
        raise ValidationError("backup domain id is invalid")
    bound_checkpoint = (
        checkpoint if type(checkpoint) is AuditCheckpointBinding else AuditCheckpointBinding.parse(checkpoint)
    )
    bound_checkpoint.verify(audit_public_key_pem)
    try:
        with store.transaction(immediate=False) as connection:
            rows = list(
                connection.execute(
                    "SELECT sequence,occurred_at,record_json,previous_hash,record_hash "
                    "FROM audit_log ORDER BY sequence"
                ).fetchall()
            )
            last_sequence, last_hash = _verify_audit_rows(rows)
            domain_row = connection.execute(
                "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
                (domain,),
            ).fetchone()
            metadata = connection.execute(
                "SELECT value FROM metadata WHERE key='schema_version'"
            ).fetchone()
            if domain_row is None:
                raise ValidationError("backup domain does not exist")
            if metadata is None:
                raise GateBlocked("schema_version", "backup schema metadata is missing")
            schema_version = int(metadata["value"])
            status = str(domain_row["status"])
            policy_revision = int(domain_row["policy_revision"])
            revocation_epoch = int(domain_row["revocation_epoch"])
    except (AuthenticationError, ConflictError, GateBlocked, ValidationError):
        raise
    except Exception as exc:
        raise GateBlocked("backup_binding", "current backup binding could not be captured") from exc
    if schema_version != CURRENT_SCHEMA_VERSION:
        raise GateBlocked("schema_version", "backup requires the exact current schema version")
    if (last_sequence, last_hash) != (bound_checkpoint.last_sequence, bound_checkpoint.last_hash):
        raise ConflictError("audit checkpoint is not the exact current audit head")
    return BackupBinding(
        schema_version=schema_version,
        domain_id=domain,
        domain_status=cast(Any, status),
        policy_revision=policy_revision,
        revocation_epoch=revocation_epoch,
        audit_checkpoint=bound_checkpoint,
    )


def _require_sqlite_archive_binding(path: Path, expected: BackupBinding) -> None:
    """Verify copied SQLite bytes carry the exact captured domain/audit snapshot.

    The archive is opened through a stable owner-only descriptor with immutable
    read-only SQLite semantics, so this check cannot create journals or change
    the bytes that will be signed.
    """

    if type(expected) is not BackupBinding:
        raise ValidationError("SQLite archive binding requires an exact backup binding")
    archive = _require_path(path, "SQLite backup archive")
    descriptor: int | None = None
    connection: sqlite3.Connection | None = None
    try:
        descriptor = os.open(archive, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        before = os.fstat(descriptor)
        current = archive.lstat()
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or before.st_nlink != 1
            or (before.st_dev, before.st_ino) != (current.st_dev, current.st_ino)
        ):
            raise ValidationError("SQLite backup archive must be one stable owner-only file")
        connection = sqlite3.connect(
            f"file:/proc/self/fd/{descriptor}?mode=ro&immutable=1",
            uri=True,
            isolation_level=None,
        )
        connection.row_factory = sqlite3.Row
        rows = list(
            connection.execute(
                "SELECT sequence,occurred_at,record_json,previous_hash,record_hash "
                "FROM audit_log ORDER BY sequence"
            ).fetchall()
        )
        last_sequence, last_hash = _verify_audit_rows(rows)
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (expected.domain_id,),
        ).fetchone()
        metadata = connection.execute(
            "SELECT value FROM metadata WHERE key='schema_version'"
        ).fetchone()
        catalog = connection.execute(
            "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
        ).fetchall()
        if domain is None or metadata is None:
            raise ConflictError("SQLite archive does not contain the bound domain/schema")
        if int(metadata["value"]) != CURRENT_SCHEMA_VERSION:
            raise ConflictError("SQLite archive schema differs from the current release")
        if [tuple(row) for row in catalog] != [
            (migration.version, migration.name, migration.checksum) for migration in MIGRATIONS
        ]:
            raise ConflictError("SQLite archive migration catalog differs from the current release")
        actual = BackupBinding(
            schema_version=int(metadata["value"]),
            domain_id=expected.domain_id,
            domain_status=cast(Any, str(domain["status"])),
            policy_revision=int(domain["policy_revision"]),
            revocation_epoch=int(domain["revocation_epoch"]),
            audit_checkpoint=expected.audit_checkpoint,
        )
        if (
            actual != expected
            or last_sequence != expected.audit_checkpoint.last_sequence
            or last_hash != expected.audit_checkpoint.last_hash
        ):
            raise ConflictError("SQLite archive is not the exact captured authority/audit snapshot")
        after = os.fstat(descriptor)
        current_after = archive.lstat()
        if (
            (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns)
            != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns)
            or (after.st_dev, after.st_ino) != (current_after.st_dev, current_after.st_ino)
        ):
            raise ConflictError("SQLite archive changed during binding verification")
    except (AuthenticationError, ConflictError, GateBlocked, ValidationError):
        raise
    except Exception as exc:
        raise GateBlocked(
            "sqlite_backup_binding",
            "SQLite archive binding could not be verified without mutation",
        ) from exc
    finally:
        if connection is not None:
            connection.close()
        if descriptor is not None:
            os.close(descriptor)


_ALLOWED_CONNINFO_KEYS = frozenset(
    {
        "application_name",
        "channel_binding",
        "connect_timeout",
        "dbname",
        "host",
        "hostaddr",
        "port",
        "sslcert",
        "sslcrl",
        "sslcrldir",
        "sslkey",
        "sslmode",
        "sslrootcert",
        "target_session_attrs",
        "user",
    }
)


def _safe_postgres_conninfo(database_url: str) -> tuple[str, dict[str, str], str]:
    dsn = _require_string(database_url, "PostgreSQL connection information", maximum=4096)
    if "\r" in dsn or "\n" in dsn or not dsn.startswith(("postgresql://", "postgres://")):
        raise ValidationError("PostgreSQL connection information must be a safe URI")
    try:
        parsed = {str(key): str(value) for key, value in conninfo_to_dict(dsn).items()}
    except Exception as exc:
        raise ValidationError("PostgreSQL connection information is invalid") from exc
    if "password" in parsed or "passfile" in parsed or "service" in parsed or "servicefile" in parsed:
        raise ValidationError("PostgreSQL credentials and service files must not be embedded in a plan")
    if not parsed or set(parsed) - _ALLOWED_CONNINFO_KEYS:
        raise ValidationError("PostgreSQL connection information contains unsupported parameters")
    if not parsed.get("host") or not parsed.get("dbname"):
        raise ValidationError("PostgreSQL connection information requires an exact host and database")
    fingerprint_fields = {
        key: parsed[key]
        for key in sorted(parsed)
        if key not in {"application_name", "connect_timeout", "sslcert", "sslkey"}
    }
    fingerprint = canonical_digest(
        {"backend": BackupBackend.POSTGRESQL.value, "connection": fingerprint_fields}
    )
    return dsn, parsed, fingerprint


def build_postgresql_backup_plan(
    *,
    database_url: str,
    archive_path: Path,
    manifest_path: Path,
    binding: BackupBinding,
) -> PostgreSQLBackupPlan:
    dsn, _parsed, source_fingerprint = _safe_postgres_conninfo(database_url)
    archive = _require_absent(archive_path, "PostgreSQL backup archive")
    manifest = _require_absent(manifest_path, "PostgreSQL backup manifest")
    if archive == manifest:
        raise ValidationError("backup archive and manifest paths must differ")
    command = SubprocessCommand(
        argv=(
            "pg_dump",
            "--format=custom",
            "--no-owner",
            "--no-privileges",
            "--file",
            str(archive),
            "--dbname",
            dsn,
        ),
        required_environment=("PGPASSFILE",),
    )
    return PostgreSQLBackupPlan(
        archive_path=archive,
        manifest_path=manifest,
        source_fingerprint=source_fingerprint,
        binding=binding,
        command=command,
    )


def build_sqlite_backup_plan(
    *,
    source_path: Path,
    archive_path: Path,
    manifest_path: Path,
    binding: BackupBinding,
    source_offline: bool,
) -> SQLiteBackupPlan:
    if source_offline is not True:
        raise GateBlocked("sqlite_offline_backup", "SQLite backup requires an independently stopped application")
    source = _require_path(source_path, "SQLite source")
    validate_owner_only_directory(source.parent)
    _require_sqlite_offline_sidecars_absent(source)
    facts = _stable_file_facts(
        source,
        header_size=len(_SQLITE_HEADER),
        expected_header=_SQLITE_HEADER,
    )
    archive = _require_absent(archive_path, "SQLite backup archive")
    manifest = _require_absent(manifest_path, "SQLite backup manifest")
    if len({source, archive, manifest}) != 3:
        raise ValidationError("SQLite source, archive, and manifest paths must differ")
    live_sidecars = {
        source.with_name(source.name + suffix)
        for suffix in ("-wal", "-shm", "-journal")
    }
    if archive in live_sidecars or manifest in live_sidecars:
        raise ValidationError("SQLite backup destinations must not masquerade as live sidecars")
    source_fingerprint = canonical_digest(
        {
            "backend": BackupBackend.SQLITE.value,
            "path": str(source),
            "sha256": facts.sha256,
            "size": facts.size,
        }
    )
    return SQLiteBackupPlan(
        source_path=source,
        archive_path=archive,
        manifest_path=manifest,
        source_fingerprint=source_fingerprint,
        source_sha256=facts.sha256,
        source_size=facts.size,
        archive_parent_fingerprint=_parent_fingerprint(
            archive,
            role="SQLite backup archive",
        ),
        manifest_parent_fingerprint=_parent_fingerprint(
            manifest,
            role="SQLite backup manifest",
        ),
        binding=binding,
    )


def create_backup_manifest(
    plan: BackupPlan,
    *,
    backup_id: str,
    created_at: datetime,
) -> BackupManifest:
    """Validate stable archive bytes and produce a canonical manifest object."""

    when = _require_utc_seconds(created_at, "backup creation time")
    if type(plan) is PostgreSQLBackupPlan:
        facts = _stable_file_facts(
            plan.archive_path,
            header_size=len(_POSTGRES_CUSTOM_HEADER),
            expected_header=_POSTGRES_CUSTOM_HEADER,
        )
        backend = BackupBackend.POSTGRESQL
    elif type(plan) is SQLiteBackupPlan:
        _require_sqlite_offline_sidecars_absent(plan.source_path)
        source = _stable_file_facts(
            plan.source_path,
            header_size=len(_SQLITE_HEADER),
            expected_header=_SQLITE_HEADER,
        )
        if source.sha256 != plan.source_sha256 or source.size != plan.source_size:
            raise ConflictError("SQLite source changed after backup planning")
        facts = _stable_file_facts(
            plan.archive_path,
            header_size=len(_SQLITE_HEADER),
            expected_header=_SQLITE_HEADER,
        )
        if facts.sha256 != source.sha256 or facts.size != source.size:
            raise ConflictError("SQLite archive is not the exact planned offline byte copy")
        backend = BackupBackend.SQLITE
    else:
        raise ValidationError("backup plan type is unsupported")
    if facts.path.name != plan.archive_path.name:
        raise ConflictError("backup archive path changed after planning")
    return BackupManifest(
        backup_id=backup_id,
        backend=backend,
        archive_format=plan.archive_format,
        filename=facts.path.name,
        sha256=facts.sha256,
        size=facts.size,
        created_at=when,
        source_fingerprint=plan.source_fingerprint,
        binding=plan.binding,
    )


def _open_pinned_parent(
    destination: Path,
    *,
    role: str,
    expected_parent_fingerprint: str | None,
) -> tuple[Path, int, os.stat_result]:
    path = _require_path(destination, role)
    validate_owner_only_directory(path.parent)
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        directory = os.open(path.parent, flags)
    except OSError as exc:
        raise ValidationError(f"{role} custody directory is unavailable or unsafe") from exc
    metadata = os.fstat(directory)
    current = path.parent.lstat()
    if (
        not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_mode & 0o077
        or (metadata.st_dev, metadata.st_ino) != (current.st_dev, current.st_ino)
    ):
        os.close(directory)
        raise ConflictError(f"{role} custody directory changed")
    if expected_parent_fingerprint is not None:
        actual = canonical_digest(
            {
                "role": role,
                "path": str(path),
                "parent_device": metadata.st_dev,
                "parent_inode": metadata.st_ino,
            }
        )
        if actual != expected_parent_fingerprint:
            os.close(directory)
            raise ConflictError(f"{role} custody directory changed after planning")
    try:
        os.stat(path.name, dir_fd=directory, follow_symlinks=False)
    except FileNotFoundError:
        pass
    except OSError as exc:
        os.close(directory)
        raise ValidationError(f"{role} destination could not be inspected") from exc
    else:
        os.close(directory)
        raise ConflictError(f"{role} must not already exist")
    return path, directory, metadata


def _publish_staged_descriptor(
    *,
    path: Path,
    directory: int,
    parent_metadata: os.stat_result,
    temporary_name: str,
    descriptor: int,
    digest: str,
    size: int,
    state: _PublicationState,
) -> _InstalledFile:
    os.fchmod(descriptor, 0o600)
    os.fsync(descriptor)
    installed_metadata = os.fstat(descriptor)
    if (
        not stat.S_ISREG(installed_metadata.st_mode)
        or installed_metadata.st_uid != os.geteuid()
        or stat.S_IMODE(installed_metadata.st_mode) != 0o600
        or installed_metadata.st_size != size
    ):
        raise ValidationError("staged owner-only file changed before publication")
    current_parent = path.parent.lstat()
    if (current_parent.st_dev, current_parent.st_ino) != (
        parent_metadata.st_dev,
        parent_metadata.st_ino,
    ):
        raise ConflictError("custody directory changed before publication")
    installed = _InstalledFile(
        path=path,
        parent_device=parent_metadata.st_dev,
        parent_inode=parent_metadata.st_ino,
        device=installed_metadata.st_dev,
        inode=installed_metadata.st_ino,
        sha256=digest,
        size=size,
    )
    os.link(
        temporary_name,
        path.name,
        src_dir_fd=directory,
        dst_dir_fd=directory,
        follow_symlinks=False,
    )
    state.installed = installed
    try:
        os.unlink(temporary_name, dir_fd=directory)
    except Exception:
        try:
            _unlink_installed_file(installed, expected_links=2)
            state.installed = None
        except Exception as cleanup_error:
            raise PublicationOutcomeUnknown(installed) from cleanup_error
        raise
    state.temporary_present = False
    try:
        os.fsync(directory)
    except Exception as exc:
        raise PublicationOutcomeUnknown(installed) from exc
    state.directory_durable = True
    return installed


def _close_publication_resources(
    *,
    state: _PublicationState,
    temporary_name: str,
    directory: int,
    descriptors: tuple[int | None, ...],
) -> None:
    """Close staging resources without turning a committed write into definite failure."""

    active_error = sys.exception()
    cleanup_error: BaseException | None = None
    for descriptor in descriptors:
        if descriptor is None:
            continue
        try:
            os.close(descriptor)
        except BaseException as exc:  # pragma: no cover - platform fault injection
            cleanup_error = cleanup_error or exc
    if state.temporary_present:
        try:
            os.unlink(temporary_name, dir_fd=directory)
            state.temporary_present = False
        except FileNotFoundError:
            state.temporary_present = False
        except BaseException as exc:  # pragma: no cover - platform fault injection
            cleanup_error = cleanup_error or exc
    try:
        os.close(directory)
    except BaseException as exc:  # pragma: no cover - platform fault injection
        cleanup_error = cleanup_error or exc
    if cleanup_error is None or active_error is not None:
        return
    if state.installed is not None:
        raise PublicationOutcomeUnknown(state.installed) from cleanup_error
    raise cleanup_error


def _exclusive_owner_bytes(
    payload: bytes,
    destination: Path,
    *,
    role: str,
    expected_parent_fingerprint: str | None = None,
) -> _InstalledFile:
    path, directory, parent_metadata = _open_pinned_parent(
        destination,
        role=role,
        expected_parent_fingerprint=expected_parent_fingerprint,
    )
    temporary_name = f".agentnet-{path.name}-{os.urandom(16).hex()}.tmp"
    descriptor: int | None = None
    state = _PublicationState()
    try:
        descriptor = os.open(
            temporary_name,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
            0o600,
            dir_fd=directory,
        )
        offset = 0
        while offset < len(payload):
            written = os.write(descriptor, payload[offset:])
            if written <= 0:
                raise OSError("staged owner-only write made no progress")
            offset += written
        return _publish_staged_descriptor(
            path=path,
            directory=directory,
            parent_metadata=parent_metadata,
            temporary_name=temporary_name,
            descriptor=descriptor,
            digest=hashlib.sha256(payload).hexdigest(),
            size=len(payload),
            state=state,
        )
    finally:
        _close_publication_resources(
            state=state,
            temporary_name=temporary_name,
            directory=directory,
            descriptors=(descriptor,),
        )


def write_backup_manifest(
    path: Path,
    manifest: BackupManifest,
    *,
    expected_parent_fingerprint: str | None = None,
) -> Path:
    """Atomically publish one canonical manifest after complete staged validation."""

    if type(manifest) is not BackupManifest:
        raise ValidationError("backup manifest object is invalid")
    return _exclusive_owner_bytes(
        canonical_json(manifest.as_dict()),
        path,
        role="SQLite backup manifest" if expected_parent_fingerprint is not None else "backup manifest",
        expected_parent_fingerprint=expected_parent_fingerprint,
    ).path


def _exclusive_owner_copy(
    source: Path,
    destination: Path,
    *,
    role: str,
    expected_sha256: str,
    expected_size: int,
    expected_header: bytes,
    expected_parent_fingerprint: str | None = None,
) -> _InstalledFile:
    """Stage, validate, and atomically publish one exact owner-only file copy."""

    source_path = _require_path(source, "copy source")
    path, directory, parent_metadata = _open_pinned_parent(
        destination,
        role=role,
        expected_parent_fingerprint=expected_parent_fingerprint,
    )
    temporary_name = f".agentnet-{path.name}-{os.urandom(16).hex()}.tmp"
    source_descriptor: int | None = None
    destination_descriptor: int | None = None
    state = _PublicationState()
    try:
        source_descriptor = os.open(source_path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        before = os.fstat(source_descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
        ):
            raise ValidationError("copy source must be an owner-only regular file")
        destination_descriptor = os.open(
            temporary_name,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
            0o600,
            dir_fd=directory,
        )
        digest = hashlib.sha256()
        copied = 0
        header = b""
        while True:
            chunk = os.read(source_descriptor, 1_048_576)
            if not chunk:
                break
            digest.update(chunk)
            copied += len(chunk)
            if len(header) < len(expected_header):
                header += chunk[: len(expected_header) - len(header)]
            offset = 0
            while offset < len(chunk):
                written = os.write(destination_descriptor, chunk[offset:])
                if written <= 0:
                    raise OSError("backup copy made no progress")
                offset += written
        after = os.fstat(source_descriptor)
        if (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        ) != (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        ):
            raise ConflictError("copy source changed while bytes were copied")
        actual_digest = digest.hexdigest()
        if (
            actual_digest != expected_sha256
            or copied != expected_size
            or header != expected_header
        ):
            raise ConflictError("staged copy differs from its exact planned bytes")
        return _publish_staged_descriptor(
            path=path,
            directory=directory,
            parent_metadata=parent_metadata,
            temporary_name=temporary_name,
            descriptor=destination_descriptor,
            digest=actual_digest,
            size=copied,
            state=state,
        )
    finally:
        _close_publication_resources(
            state=state,
            temporary_name=temporary_name,
            directory=directory,
            descriptors=(source_descriptor, destination_descriptor),
        )


def _verify_installed_name(
    *,
    directory: int,
    name: str,
    installed: _InstalledFile,
    expected_links: int = 1,
) -> None:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(name, flags, dir_fd=directory)
    except OSError as exc:
        raise ConflictError("installed file is missing or unsafe") from exc
    try:
        before = os.fstat(descriptor)
        if (
            (before.st_dev, before.st_ino) != (installed.device, installed.inode)
            or not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or before.st_size != installed.size
            or before.st_nlink != expected_links
        ):
            raise ConflictError("installed file identity changed")
        digest = hashlib.sha256()
        while True:
            chunk = os.read(descriptor, 1_048_576)
            if not chunk:
                break
            digest.update(chunk)
        after = os.fstat(descriptor)
        stable_before = (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_nlink,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        )
        stable_after = (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_nlink,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        )
        if stable_before != stable_after:
            raise ConflictError("installed file changed while it was verified")
        if digest.hexdigest() != installed.sha256:
            raise ConflictError("installed file digest changed")
    finally:
        os.close(descriptor)


def _open_installed_parent(installed: _InstalledFile) -> int:
    path = installed.path
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        directory = os.open(path.parent, flags)
    except OSError as exc:
        raise ConflictError("installed-file custody directory is unavailable") from exc
    try:
        parent = os.fstat(directory)
        current_parent = path.parent.lstat()
        if (
            (parent.st_dev, parent.st_ino)
            != (installed.parent_device, installed.parent_inode)
            or (current_parent.st_dev, current_parent.st_ino)
            != (installed.parent_device, installed.parent_inode)
        ):
            raise ConflictError("installed-file custody directory changed")
    except Exception:
        os.close(directory)
        raise
    return directory


def _verify_installed_file(installed: _InstalledFile) -> None:
    directory = _open_installed_parent(installed)
    try:
        _verify_installed_name(
            directory=directory,
            name=installed.path.name,
            installed=installed,
        )
    finally:
        os.close(directory)


def _unlink_installed_file(installed: _InstalledFile, *, expected_links: int = 1) -> None:
    """Remove the product-visible name and retain exact bytes under quarantine.

    POSIX has no portable unlink-by-open-descriptor operation.  Deleting the
    quarantine pathname after validation would therefore reintroduce a
    name-swap race.  Retention is deliberate: an authenticated operator may
    inspect and remove the random owner-only quarantine file out of band.
    """

    directory = _open_installed_parent(installed)
    quarantine_name = f".agentnet-quarantine-{os.urandom(16).hex()}"
    try:
        os.rename(
            installed.path.name,
            quarantine_name,
            src_dir_fd=directory,
            dst_dir_fd=directory,
        )
        os.fsync(directory)
        _verify_installed_name(
            directory=directory,
            name=quarantine_name,
            installed=installed,
            expected_links=expected_links,
        )
    except FileNotFoundError as exc:
        raise ConflictError("installed file changed before quarantine") from exc
    finally:
        os.close(directory)


def discard_unsealed_sqlite_backup(execution: SQLiteBackupExecution) -> None:
    """Quarantine only exact just-created outputs when seal publication fails."""

    if type(execution) is not SQLiteBackupExecution:
        raise ValidationError("unsealed backup rollback requires an exact execution receipt")
    _verify_installed_file(execution._manifest)
    _verify_installed_file(execution._archive)
    _unlink_installed_file(execution._manifest)
    _unlink_installed_file(execution._archive)


def discard_failed_sqlite_restore(execution: SQLiteRestoreExecution) -> None:
    """Quarantine only unchanged restored bytes after post-copy verification fails."""

    if type(execution) is not SQLiteRestoreExecution:
        raise ValidationError("failed restore rollback requires an exact execution receipt")
    _unlink_installed_file(execution._installed)


def execute_sqlite_backup_plan(
    plan: SQLiteBackupPlan,
    *,
    backup_id: str,
    created_at: datetime | None = None,
) -> SQLiteBackupExecution:
    """Execute the exact offline SQLite copy and durably write its manifest."""

    if type(plan) is not SQLiteBackupPlan:
        raise ValidationError("SQLite backup execution requires an exact SQLite plan")
    _require_sqlite_offline_sidecars_absent(plan.source_path)
    when = created_at or datetime.now(UTC).replace(microsecond=0)
    archive_installed: _InstalledFile | None = None
    try:
        archive_installed = _exclusive_owner_copy(
            plan.source_path,
            plan.archive_path,
            role="SQLite backup archive",
            expected_sha256=plan.source_sha256,
            expected_size=plan.source_size,
            expected_header=_SQLITE_HEADER,
            expected_parent_fingerprint=plan.archive_parent_fingerprint,
        )
        _require_sqlite_archive_binding(archive_installed.path, plan.binding)
        manifest = create_backup_manifest(plan, backup_id=backup_id, created_at=when)
        manifest_bytes = canonical_json(manifest.as_dict())
        manifest_installed = _exclusive_owner_bytes(
            manifest_bytes,
            plan.manifest_path,
            role="SQLite backup manifest",
            expected_parent_fingerprint=plan.manifest_parent_fingerprint,
        )
        return SQLiteBackupExecution(
            manifest=manifest,
            archive=archive_installed,
            manifest_install=manifest_installed,
            _capability=_BACKUP_EXECUTION_CAPABILITY,
        )
    except PublicationOutcomeUnknown:
        raise
    except Exception:
        if archive_installed is not None:
            _unlink_installed_file(archive_installed)
        raise


def _reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValidationError("backup manifest contains duplicate keys")
        result[key] = value
    return result


def _read_canonical_manifest(path: Path) -> tuple[Path, BackupManifest, str]:
    facts = _stable_file_facts(
        path,
        header_size=0,
        capture_content=True,
        maximum_size=_MAX_MANIFEST_BYTES,
    )
    assert facts.content is not None
    try:
        decoded = json.loads(facts.content.decode("utf-8"), object_pairs_hook=_reject_duplicate_keys)
    except ValidationError:
        raise
    except Exception as exc:
        raise ValidationError("backup manifest is not valid UTF-8 JSON") from exc
    if not isinstance(decoded, Mapping):
        raise ValidationError("backup manifest must be a JSON object")
    manifest = BackupManifest.parse(decoded)
    if facts.content != canonical_json(manifest.as_dict()):
        raise ValidationError("backup manifest is not exact canonical JSON")
    return facts.path, manifest, facts.sha256


def read_manifest_seal(path: Path) -> ManifestSeal:
    """Read one bounded, owner-only, non-symlink, exact canonical signed seal."""

    facts = _stable_file_facts(
        path,
        header_size=0,
        capture_content=True,
        maximum_size=_MAX_SEAL_BYTES,
    )
    assert facts.content is not None
    try:
        decoded = json.loads(
            facts.content.decode("utf-8"),
            object_pairs_hook=_reject_duplicate_keys,
        )
    except ValidationError:
        raise
    except Exception as exc:
        raise ValidationError("backup manifest seal is not valid UTF-8 JSON") from exc
    if not isinstance(decoded, Mapping):
        raise ValidationError("backup manifest seal must be a JSON object")
    seal = ManifestSeal.parse(decoded)
    if facts.content != canonical_json(seal.as_dict()):
        raise ValidationError("backup manifest seal is not exact canonical JSON")
    return seal


def write_manifest_seal(path: Path, seal: ManifestSeal) -> Path:
    """Atomically publish one owner-only canonical signed seal."""

    if type(seal) is not ManifestSeal:
        raise ValidationError("backup manifest seal object is invalid")
    return _exclusive_owner_bytes(
        canonical_json(seal.as_dict()),
        path,
        role="backup manifest seal",
    ).path


def _archive_facts_for_manifest(archive_path: Path, manifest: BackupManifest) -> _FileFacts:
    header = (
        _POSTGRES_CUSTOM_HEADER
        if manifest.archive_format is ArchiveFormat.POSTGRESQL_CUSTOM
        else _SQLITE_HEADER
    )
    facts = _stable_file_facts(
        archive_path,
        header_size=len(header),
        expected_header=header,
    )
    if facts.path.name != manifest.filename or facts.sha256 != manifest.sha256 or facts.size != manifest.size:
        raise AuthenticationError("backup archive does not match the sealed manifest")
    return facts


def verify_backup_for_restore(
    *,
    archive_path: Path,
    manifest_path: Path,
    seal: ManifestSeal,
    audit_public_key_pem: str,
    seal_public_key_pem: str,
    trusted_signer_key_epoch: int,
    expected_trust_root_revision: int,
    signer_not_before: int,
    signer_retired_at: int | None = None,
    signer_revoked_at: int | None = None,
    verified_at: datetime | None = None,
) -> VerifiedBackup:
    """Return a hidden verified capability only for exact archive/manifest/seal bytes."""

    if type(seal) is not ManifestSeal:
        raise ValidationError("backup manifest seal is invalid")
    when = verified_at or datetime.now(UTC).replace(microsecond=0)
    when = _require_utc_seconds(when, "backup verification time")
    manifest_file, manifest, manifest_file_sha256 = _read_canonical_manifest(manifest_path)
    archive = _archive_facts_for_manifest(archive_path, manifest)
    if manifest_file.parent != archive.path.parent:
        raise ValidationError("backup archive and manifest must share one owner-only custody directory")
    validate_owner_only_directory(archive.path.parent)
    checkpoint = manifest.binding.audit_checkpoint
    expected_manifest_fields = {
        "backup_id": manifest.backup_id,
        "backend": manifest.backend,
        "archive_format": manifest.archive_format,
        "archive_filename": manifest.filename,
        "archive_sha256": manifest.sha256,
        "archive_size": manifest.size,
        "manifest_sha256": manifest.digest,
        "source_fingerprint": manifest.source_fingerprint,
        "domain_id": manifest.binding.domain_id,
        "schema_version": manifest.binding.schema_version,
        "domain_status": manifest.binding.domain_status,
        "policy_revision": manifest.binding.policy_revision,
        "revocation_epoch": manifest.binding.revocation_epoch,
        "audit_checkpoint_digest": checkpoint.digest,
        "audit_checkpoint_signer_key_id": checkpoint.signer_key_id,
        "audit_last_sequence": checkpoint.last_sequence,
        "audit_last_hash": checkpoint.last_hash,
    }
    presented_manifest_fields = {
        name: getattr(seal, name) for name in expected_manifest_fields
    }
    if manifest_file_sha256 != manifest.digest or presented_manifest_fields != expected_manifest_fields:
        raise AuthenticationError("backup manifest does not match its separate custody seal")
    if (
        not _is_exact_int(trusted_signer_key_epoch)
        or trusted_signer_key_epoch < 1
        or not _is_exact_int(expected_trust_root_revision)
        or expected_trust_root_revision < 1
        or not _is_exact_int(signer_not_before)
        or signer_not_before < 1
        or (signer_retired_at is not None and not _is_exact_int(signer_retired_at))
        or (signer_revoked_at is not None and not _is_exact_int(signer_revoked_at))
    ):
        raise ValidationError("backup seal trust lifecycle is invalid")
    seal_key = load_public_key(seal_public_key_pem)
    pinned_key_id = b64url_encode(
        hashlib.sha256(
            seal_key.public_bytes(
                serialization.Encoding.DER,
                serialization.PublicFormat.SubjectPublicKeyInfo,
            )
        ).digest()
    )
    sealed_timestamp = int(seal.sealed_at.timestamp())
    if (
        pinned_key_id != seal.signer_key_id
        or seal.signer_key_epoch != trusted_signer_key_epoch
        or seal.trust_root_revision != expected_trust_root_revision
        or sealed_timestamp < signer_not_before
        or manifest.created_at > seal.sealed_at
        or seal.sealed_at > when
        or signer_revoked_at is not None
        or signer_retired_at is not None
    ):
        raise AuthenticationError("backup seal signer is not current in the pinned trust root")
    verify_signature(
        seal_public_key_pem,
        _BACKUP_SEAL_PURPOSE,
        seal.signed_fields(),
        seal.signature,
    )
    manifest.binding.audit_checkpoint.verify(audit_public_key_pem)
    return VerifiedBackup._create(
        archive_path=archive.path,
        manifest_path=manifest_file,
        manifest=manifest,
        seal=seal,
        verified_at=when,
    )


def inspect_sqlite_restore_target(
    *,
    target_path: Path,
    application_offline: bool,
    inspected_at: datetime | None = None,
) -> RestoreTargetInspection:
    if application_offline is not True:
        raise GateBlocked("restore_target", "SQLite restore target application must be stopped")
    target = _require_absent(target_path, "SQLite restore target")
    for suffix in ("-wal", "-shm", "-journal"):
        _require_absent(target.with_name(target.name + suffix), "SQLite restore target sidecar")
    fingerprint = _parent_fingerprint(target, role="SQLite restore target")
    when = inspected_at or datetime.now(UTC).replace(microsecond=0)
    return RestoreTargetInspection._create(
        backend=BackupBackend.SQLITE,
        target_fingerprint=fingerprint,
        inspected_at=_require_utc_seconds(when, "target inspection time"),
    )


def inspect_postgresql_restore_target(
    *,
    database_url: str,
    application_offline: bool,
    non_system_object_count: int,
    inspected_at: datetime | None = None,
) -> RestoreTargetInspection:
    if application_offline is not True:
        raise GateBlocked("restore_target", "PostgreSQL target application must be stopped")
    if not _is_exact_int(non_system_object_count) or non_system_object_count < 0:
        raise ValidationError("PostgreSQL target object count is invalid")
    _safe_postgres_conninfo(database_url)
    if inspected_at is not None:
        _require_utc_seconds(inspected_at, "target inspection time")
    raise GateBlocked(
        "postgres_restore_catalog_inspection",
        "caller-asserted PostgreSQL object counts cannot prove an empty locked restore target; "
        "use an authenticated operator runner that holds the catalog lock",
    )


def _require_fresh_target(target: RestoreTargetInspection, when: datetime) -> None:
    if type(target) is not RestoreTargetInspection or target._capability is not _INSPECTED_TARGET_CAPABILITY:
        raise AuthenticationError("restore target was not produced by inspection")
    now = _require_utc_seconds(when, "restore planning time")
    age = now - target.inspected_at
    if age < timedelta(0) or age > _TARGET_INSPECTION_MAX_AGE:
        raise ConflictError("restore target inspection is stale or from the future")


def _require_backup_binding(
    backup: VerifiedBackup,
    *,
    expected_domain_id: str,
    expected_schema_version: int,
) -> None:
    if type(backup) is not VerifiedBackup or backup._capability is not _VERIFIED_BACKUP_CAPABILITY:
        raise AuthenticationError("backup was not produced by restore verification")
    if (
        backup.manifest.binding.domain_id != expected_domain_id
        or backup.seal.domain_id != expected_domain_id
        or backup.manifest.binding.schema_version != expected_schema_version
        or backup.seal.schema_version != expected_schema_version
    ):
        raise AuthenticationError("backup domain or schema binding does not match the restore target")
    manifest_file, manifest, digest = _read_canonical_manifest(backup.manifest_path)
    if (
        manifest_file != backup.manifest_path
        or manifest != backup.manifest
        or digest != backup.seal.manifest_sha256
    ):
        raise ConflictError("verified backup manifest changed before restore planning")
    _archive_facts_for_manifest(backup.archive_path, backup.manifest)


def build_postgresql_restore_plan(
    *,
    backup: VerifiedBackup,
    target: RestoreTargetInspection,
    target_database_url: str,
    expected_domain_id: str,
    expected_schema_version: int,
    planned_at: datetime | None = None,
) -> PostgreSQLRestorePlan:
    when = planned_at or datetime.now(UTC).replace(microsecond=0)
    _require_fresh_target(target, when)
    _require_backup_binding(
        backup,
        expected_domain_id=expected_domain_id,
        expected_schema_version=expected_schema_version,
    )
    if backup.backend is not BackupBackend.POSTGRESQL or target.backend is not BackupBackend.POSTGRESQL:
        raise ValidationError("PostgreSQL restore requires matching backup and target backends")
    dsn, _parsed, fingerprint = _safe_postgres_conninfo(target_database_url)
    if fingerprint != target.target_fingerprint:
        raise AuthenticationError("PostgreSQL restore target differs from the inspected target")
    command = SubprocessCommand(
        argv=(
            "pg_restore",
            "--exit-on-error",
            "--single-transaction",
            "--no-owner",
            "--no-privileges",
            "--dbname",
            dsn,
            str(backup.archive_path),
        ),
        required_environment=("PGPASSFILE",),
    )
    forbidden = {"--clean", "--create", "-c", "-C"}
    if any(argument in forbidden for argument in command.argv):
        raise ValidationError("restore command must never clean or create a target database")
    return PostgreSQLRestorePlan(backup=backup, target=target, command=command)


def build_sqlite_restore_plan(
    *,
    backup: VerifiedBackup,
    target: RestoreTargetInspection,
    target_path: Path,
    expected_domain_id: str,
    expected_schema_version: int,
    planned_at: datetime | None = None,
) -> SQLiteRestorePlan:
    when = planned_at or datetime.now(UTC).replace(microsecond=0)
    _require_fresh_target(target, when)
    _require_backup_binding(
        backup,
        expected_domain_id=expected_domain_id,
        expected_schema_version=expected_schema_version,
    )
    if backup.backend is not BackupBackend.SQLITE or target.backend is not BackupBackend.SQLITE:
        raise ValidationError("SQLite restore requires matching backup and target backends")
    inspected_again = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=target.inspected_at,
    )
    if inspected_again.target_fingerprint != target.target_fingerprint:
        raise ConflictError("SQLite restore target changed after inspection")
    return SQLiteRestorePlan(
        backup=backup,
        target=target,
        target_path=_require_path(target_path, "SQLite restore target"),
    )


def execute_sqlite_restore_plan(
    plan: SQLiteRestorePlan,
    *,
    executed_at: datetime | None = None,
) -> SQLiteRestoreExecution:
    """Restore verified SQLite bytes to the exact inspected absent target."""

    if type(plan) is not SQLiteRestorePlan:
        raise ValidationError("SQLite restore execution requires an exact SQLite plan")
    when = executed_at or datetime.now(UTC).replace(microsecond=0)
    _require_fresh_target(plan.target, when)
    _require_backup_binding(
        plan.backup,
        expected_domain_id=plan.backup.manifest.binding.domain_id,
        expected_schema_version=plan.backup.manifest.binding.schema_version,
    )
    inspected = inspect_sqlite_restore_target(
        target_path=plan.target_path,
        application_offline=True,
        inspected_at=plan.target.inspected_at,
    )
    if inspected.target_fingerprint != plan.target.target_fingerprint:
        raise ConflictError("SQLite restore target changed after planning")
    installed = _exclusive_owner_copy(
        plan.backup.archive_path,
        plan.target_path,
        role="SQLite restore target",
        expected_sha256=plan.backup.manifest.sha256,
        expected_size=plan.backup.manifest.size,
        expected_header=_SQLITE_HEADER,
        expected_parent_fingerprint=plan.target.target_fingerprint,
    )
    restored = installed.path
    try:
        facts = _stable_file_facts(
            restored,
            header_size=len(_SQLITE_HEADER),
            expected_header=_SQLITE_HEADER,
        )
        if (
            facts.sha256 != plan.backup.manifest.sha256
            or facts.size != plan.backup.manifest.size
        ):
            raise ConflictError("restored SQLite bytes differ from the verified backup")
    except Exception:
        _unlink_installed_file(installed)
        raise
    return SQLiteRestoreExecution(
        path=restored,
        installed=installed,
        _capability=_RESTORE_EXECUTION_CAPABILITY,
    )


def build_compromise_rebuild_plan(
    *,
    domain_id: str,
    restore_plan: RestorePlan,
) -> CompromiseRebuildPlan:
    if type(restore_plan) not in {PostgreSQLRestorePlan, SQLiteRestorePlan}:
        raise ValidationError("compromise rebuild requires a validated restore plan")
    backup = restore_plan.backup
    if (
        domain_id != backup.manifest.binding.domain_id
        or domain_id != backup.seal.domain_id
        or restore_plan.restore_completed is not False
    ):
        raise AuthenticationError("compromise rebuild domain or restore-state binding mismatch")
    rotations = tuple(
        CredentialRotationRequirement(credential_class=credential_class)
        for credential_class in CredentialClass
    )
    return CompromiseRebuildPlan(
        domain_id=domain_id,
        backend=backup.backend,
        backup_id=backup.manifest.backup_id,
        manifest_sha256=backup.seal.manifest_sha256,
        audit_checkpoint_digest=backup.seal.audit_checkpoint_digest,
        ordered_actions=_REBUILD_ACTIONS,
        credential_rotations=rotations,
    )


__all__ = [
    "ArchiveFormat",
    "AuditCheckpointBinding",
    "BackupBackend",
    "BackupBinding",
    "BackupManifest",
    "CompromiseRebuildPlan",
    "CredentialClass",
    "CredentialRotationRequirement",
    "ManifestSeal",
    "PostgreSQLBackupPlan",
    "PostgreSQLRestorePlan",
    "PublicationOutcomeUnknown",
    "RebuildAction",
    "RestoreTargetInspection",
    "SQLiteBackupPlan",
    "SQLiteBackupExecution",
    "SQLiteRestorePlan",
    "SQLiteRestoreExecution",
    "SubprocessCommand",
    "VerifiedBackup",
    "build_compromise_rebuild_plan",
    "build_postgresql_backup_plan",
    "build_postgresql_restore_plan",
    "build_sqlite_backup_plan",
    "build_sqlite_restore_plan",
    "capture_backup_binding",
    "create_backup_manifest",
    "discard_unsealed_sqlite_backup",
    "discard_failed_sqlite_restore",
    "execute_sqlite_backup_plan",
    "execute_sqlite_restore_plan",
    "inspect_postgresql_restore_target",
    "inspect_sqlite_restore_target",
    "read_manifest_seal",
    "validate_owner_only_directory",
    "verify_backup_for_restore",
    "write_backup_manifest",
    "write_manifest_seal",
]
