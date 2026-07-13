from __future__ import annotations

import hashlib
import json
import os
import shutil
import sqlite3
import stat
from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from agentnet.audit.service import AuditService
from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ValidationError
from agentnet.operations.backup import (
    ArchiveFormat,
    AuditCheckpointBinding,
    BackupBackend,
    BackupBinding,
    BackupManifest,
    CredentialClass,
    ManifestSeal,
    PublicationOutcomeUnknown,
    RebuildAction,
    RestoreTargetInspection,
    VerifiedBackup,
    build_compromise_rebuild_plan,
    build_postgresql_backup_plan,
    build_postgresql_restore_plan,
    build_sqlite_backup_plan,
    build_sqlite_restore_plan,
    capture_backup_binding,
    create_backup_manifest,
    discard_failed_sqlite_restore,
    discard_unsealed_sqlite_backup,
    execute_sqlite_backup_plan,
    execute_sqlite_restore_plan,
    inspect_postgresql_restore_target,
    inspect_sqlite_restore_target,
    read_manifest_seal,
    validate_owner_only_directory,
    verify_backup_for_restore,
    write_backup_manifest,
    write_manifest_seal,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 13, 12, 0, tzinfo=UTC)
DOMAIN = "corp.example"
PG_SOURCE = "postgresql://backup@db1.example/corporate?sslmode=verify-full"
PG_TARGET = "postgresql://restore@db2.example/rebuild?sslmode=verify-full"


def _owner_directory(path: Path) -> Path:
    path.mkdir(mode=0o700)
    path.chmod(0o700)
    return path


def _owner_file(path: Path, content: bytes) -> Path:
    path.write_bytes(content)
    path.chmod(0o600)
    return path


def _signed_seal(manifest, key: P256KeyPair) -> ManifestSeal:
    return ManifestSeal.create(
        manifest=manifest,
        signer=key,
        signer_key_epoch=1,
        trust_root_revision=1,
        sealed_at=NOW,
    )


def _verify_backup(
    *,
    archive_path: Path,
    manifest_path: Path,
    seal: ManifestSeal,
    audit_key: P256KeyPair,
    seal_key: P256KeyPair | None = None,
) -> VerifiedBackup:
    trusted = seal_key or audit_key
    return verify_backup_for_restore(
        archive_path=archive_path,
        manifest_path=manifest_path,
        seal=seal,
        audit_public_key_pem=audit_key.public_pem,
        seal_public_key_pem=trusted.public_pem,
        trusted_signer_key_epoch=1,
        expected_trust_root_revision=1,
        signer_not_before=int(NOW.timestamp()),
        verified_at=NOW,
    )


@pytest.fixture
def binding_material(tmp_path: Path) -> tuple[BackupBinding, P256KeyPair, dict[str, object]]:
    database = tmp_path / "binding.sqlite3"
    store = SQLiteStore(database, LocalEnvelopeCipher(b"b" * 32))
    key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
            "VALUES(?,'active',7,4,?)",
            (DOMAIN, int(NOW.timestamp())),
        )
        store.append_audit(
            connection,
            {"action": "operator.backup.requested", "domain_id": DOMAIN, "revision": 7},
        )
    checkpoint = AuditService(store).checkpoint(key)
    binding = capture_backup_binding(
        store,
        domain_id=DOMAIN,
        checkpoint=checkpoint,
        audit_public_key_pem=key.public_pem,
    )
    store.close()
    return binding, key, checkpoint


def _sqlite_verified_backup(
    tmp_path: Path,
    binding: BackupBinding,
    key: P256KeyPair,
) -> tuple[VerifiedBackup, ManifestSeal, Path]:
    custody = _owner_directory(tmp_path / "custody")
    source = _owner_file(custody / "source.sqlite3", b"SQLite format 3\x00" + b"source-body")
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "backup.sqlite3",
        manifest_path=custody / "backup.manifest.json",
        binding=binding,
        source_offline=True,
    )
    shutil.copyfile(source, plan.archive_path)
    plan.archive_path.chmod(0o600)
    manifest = create_backup_manifest(plan, backup_id="backup-20260713", created_at=NOW)
    write_backup_manifest(plan.manifest_path, manifest)
    seal = _signed_seal(manifest, key)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=seal,
        audit_key=key,
    )
    return verified, seal, custody


def _postgres_verified_backup(
    tmp_path: Path,
    binding: BackupBinding,
    key: P256KeyPair,
) -> VerifiedBackup:
    custody = _owner_directory(tmp_path / "pg-custody")
    plan = build_postgresql_backup_plan(
        database_url=PG_SOURCE,
        archive_path=custody / "backup.dump",
        manifest_path=custody / "backup.manifest.json",
        binding=binding,
    )
    _owner_file(plan.archive_path, b"PGDMP" + b"custom-archive")
    manifest = create_backup_manifest(plan, backup_id="pg-backup-20260713", created_at=NOW)
    write_backup_manifest(plan.manifest_path, manifest)
    seal = _signed_seal(manifest, key)
    return _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=seal,
        audit_key=key,
    )


def _executed_sqlite_backup(tmp_path: Path):
    custody = _owner_directory(tmp_path / "executed-receipt-custody")
    source = custody / "source.sqlite3"
    key = P256KeyPair.generate()
    store = SQLiteStore(source, LocalEnvelopeCipher(b"z" * 32))
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
            "VALUES(?,'active',1,1,?)",
            (DOMAIN, int(NOW.timestamp())),
        )
        store.append_audit(connection, {"action": "receipt-bound-backup"})
    checkpoint = AuditService(store).checkpoint(key)
    binding = capture_backup_binding(
        store,
        domain_id=DOMAIN,
        checkpoint=checkpoint,
        audit_public_key_pem=key.public_pem,
    )
    store.close()
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "backup.sqlite3",
        manifest_path=custody / "backup.manifest.json",
        binding=binding,
        source_offline=True,
    )
    execution = execute_sqlite_backup_plan(
        plan,
        backup_id="receipt-bound-backup",
        created_at=NOW,
    )
    return plan, execution, key


def test_checkpoint_and_binding_are_strict_canonical_types(
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, _key, checkpoint = binding_material
    assert AuditCheckpointBinding.parse(checkpoint).as_dict() == checkpoint
    assert BackupBinding.parse(binding.as_dict()) == binding
    assert binding.schema_version == CURRENT_SCHEMA_VERSION
    assert binding.policy_revision == 7
    assert binding.revocation_epoch == 4

    with pytest.raises(ValidationError):
        AuditCheckpointBinding.parse(checkpoint | {"caller_verified": True})
    with pytest.raises(ValidationError):
        BackupBinding.parse(binding.as_dict() | {"ha_proven": True})
    malformed = binding.as_dict()
    malformed["policy_revision"] = True
    with pytest.raises(ValidationError):
        BackupBinding.parse(malformed)
    with pytest.raises(FrozenInstanceError):
        binding.domain_id = "other.example"  # type: ignore[misc]


def test_capture_binding_rejects_wrong_signer_stale_head_and_tampered_chain(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "capture.sqlite3", LocalEnvelopeCipher(b"c" * 32))
    key = P256KeyPair.generate()
    other_key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
            "VALUES(?,'active',1,1,?)",
            (DOMAIN, int(NOW.timestamp())),
        )
        store.append_audit(connection, {"action": "first"})
    checkpoint = AuditService(store).checkpoint(key)

    with pytest.raises(AuthenticationError):
        capture_backup_binding(
            store,
            domain_id=DOMAIN,
            checkpoint=checkpoint,
            audit_public_key_pem=other_key.public_pem,
        )

    with store.transaction() as connection:
        store.append_audit(connection, {"action": "second"})
    with pytest.raises(ConflictError, match="exact current audit head"):
        capture_backup_binding(
            store,
            domain_id=DOMAIN,
            checkpoint=checkpoint,
            audit_public_key_pem=key.public_pem,
        )

    current = AuditService(store).checkpoint(key)
    with store.transaction() as connection:
        connection.execute("UPDATE audit_log SET record_hash=? WHERE sequence=1", ("f" * 64,))
    with pytest.raises(GateBlocked, match="audit hash chain"):
        capture_backup_binding(
            store,
            domain_id=DOMAIN,
            checkpoint=current,
            audit_public_key_pem=key.public_pem,
        )
    store.close()


def test_postgresql_backup_plan_is_inert_secret_free_and_shell_free(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, _key, _checkpoint = binding_material
    custody = _owner_directory(tmp_path / "pg-plan")
    plan = build_postgresql_backup_plan(
        database_url=PG_SOURCE,
        archive_path=custody / "backup.dump",
        manifest_path=custody / "manifest.json",
        binding=binding,
    )
    assert plan.command.argv[0] == "pg_dump"
    assert plan.command.shell is False
    assert plan.command.required_umask == 0o077
    assert plan.command.required_environment == ("PGPASSFILE",)
    assert plan.consistent_snapshot_requested is True
    assert plan.ha_proven is False and plan.pitr_proven is False
    assert not plan.archive_path.exists()

    with pytest.raises(ValidationError, match="credentials"):
        build_postgresql_backup_plan(
            database_url="postgresql://backup:secret@db1.example/corporate",
            archive_path=custody / "unsafe.dump",
            manifest_path=custody / "unsafe.json",
            binding=binding,
        )
    _owner_file(custody / "occupied.dump", b"PGDMP")
    with pytest.raises(ConflictError, match="must not already exist"):
        build_postgresql_backup_plan(
            database_url=PG_SOURCE,
            archive_path=custody / "occupied.dump",
            manifest_path=custody / "other.json",
            binding=binding,
        )


@pytest.mark.parametrize(
    ("offline", "content", "mode"),
    [
        (False, b"SQLite format 3\x00body", 0o600),
        (True, b"not-a-sqlite-file", 0o600),
        (True, b"SQLite format 3\x00body", 0o644),
    ],
)
def test_sqlite_backup_requires_offline_valid_owner_only_source(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    offline: bool,
    content: bytes,
    mode: int,
) -> None:
    binding, _key, _checkpoint = binding_material
    custody = _owner_directory(tmp_path / f"sqlite-{offline}-{mode}")
    source = _owner_file(custody / "source.sqlite3", content)
    source.chmod(mode)
    expected = GateBlocked if not offline else ValidationError
    with pytest.raises(expected):
        build_sqlite_backup_plan(
            source_path=source,
            archive_path=custody / "archive.sqlite3",
            manifest_path=custody / "manifest.json",
            binding=binding,
            source_offline=offline,
        )


def test_sqlite_sidecar_and_source_drift_fail_closed(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, _key, _checkpoint = binding_material
    custody = _owner_directory(tmp_path / "sqlite-race")
    source = _owner_file(custody / "source.sqlite3", b"SQLite format 3\x00one")
    sidecar = _owner_file(custody / "source.sqlite3-wal", b"pending")
    with pytest.raises(GateBlocked, match="sidecars"):
        build_sqlite_backup_plan(
            source_path=source,
            archive_path=custody / "archive.sqlite3",
            manifest_path=custody / "manifest.json",
            binding=binding,
            source_offline=True,
        )
    sidecar.unlink()
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "archive.sqlite3",
        manifest_path=custody / "manifest.json",
        binding=binding,
        source_offline=True,
    )
    shutil.copyfile(source, plan.archive_path)
    plan.archive_path.chmod(0o600)
    _owner_file(source, b"SQLite format 3\x00two")
    with pytest.raises(ConflictError, match="source changed"):
        create_backup_manifest(plan, backup_id="raced-backup", created_at=NOW)


@pytest.mark.parametrize("suffix", ["-wal", "-shm", "-journal"])
@pytest.mark.parametrize("destination", ["archive", "manifest"])
def test_sqlite_backup_destinations_cannot_masquerade_as_live_sidecars(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    suffix: str,
    destination: str,
) -> None:
    binding, _key, _checkpoint = binding_material
    custody = _owner_directory(tmp_path / f"sidecar-destination-{suffix[1:]}-{destination}")
    source = _owner_file(custody / "source.sqlite3", b"SQLite format 3\x00exact")
    archive = custody / "backup.sqlite3"
    manifest = custody / "backup.manifest.json"
    if destination == "archive":
        archive = source.with_name(source.name + suffix)
    else:
        manifest = source.with_name(source.name + suffix)
    with pytest.raises(ValidationError, match="live sidecars"):
        build_sqlite_backup_plan(
            source_path=source,
            archive_path=archive,
            manifest_path=manifest,
            binding=binding,
            source_offline=True,
        )


def test_sqlite_full_verify_restore_and_compromise_rebuild_are_plan_only(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    target_directory = _owner_directory(tmp_path / "target")
    target_path = target_directory / "restored.sqlite3"
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    restore = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    rebuild = build_compromise_rebuild_plan(domain_id=DOMAIN, restore_plan=restore)

    assert restore.restore_completed is False
    assert not target_path.exists()
    assert rebuild.state == "plan_only_not_executed"
    assert rebuild.completed_actions == ()
    assert rebuild.restore_completed is False
    assert rebuild.rotations_completed is False
    assert rebuild.service_safe_to_resume is False
    assert rebuild.manifest_sha256 == seal.manifest_sha256
    assert tuple(item.credential_class for item in rebuild.credential_rotations) == tuple(CredentialClass)
    assert all(item.state == "required_not_executed" for item in rebuild.credential_rotations)
    assert rebuild.ordered_actions == (
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
    assert rebuild.ha_proven is False and rebuild.pitr_proven is False


def test_sqlite_execute_backup_and_restore_round_trip_is_exact_and_no_overwrite(
    tmp_path: Path,
) -> None:
    custody = _owner_directory(tmp_path / "executed-custody")
    source = custody / "source.sqlite3"
    key = P256KeyPair.generate()
    source_store = SQLiteStore(source, LocalEnvelopeCipher(b"x" * 32))
    with source_store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
            "VALUES(?,'active',7,4,?)",
            (DOMAIN, int(NOW.timestamp())),
        )
        source_store.append_audit(
            connection,
            {"action": "operator.backup.requested", "domain_id": DOMAIN, "revision": 7},
        )
    checkpoint = AuditService(source_store).checkpoint(key)
    binding = capture_backup_binding(
        source_store,
        domain_id=DOMAIN,
        checkpoint=checkpoint,
        audit_public_key_pem=key.public_pem,
    )
    source_store.close()
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "backup.sqlite3",
        manifest_path=custody / "backup.manifest.json",
        binding=binding,
        source_offline=True,
    )

    backup_execution = execute_sqlite_backup_plan(
        plan,
        backup_id="executed-backup-20260713",
        created_at=NOW,
    )
    manifest = backup_execution.manifest
    assert plan.archive_path.read_bytes() == source.read_bytes()
    assert manifest.sha256 == hashlib.sha256(source.read_bytes()).hexdigest()
    assert plan.archive_path.stat().st_mode & 0o777 == 0o600
    assert plan.manifest_path.stat().st_mode & 0o777 == 0o600
    with pytest.raises((ConflictError, ValidationError)):
        execute_sqlite_backup_plan(
            plan,
            backup_id="executed-backup-replay",
            created_at=NOW,
        )

    seal = _signed_seal(manifest, key)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=seal,
        audit_key=key,
    )
    restore_directory = _owner_directory(tmp_path / "executed-restore")
    target_path = restore_directory / "restored.sqlite3"
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )

    restore_execution = execute_sqlite_restore_plan(restore_plan, executed_at=NOW)
    assert restore_execution.path == target_path
    assert target_path.read_bytes() == source.read_bytes()
    assert target_path.stat().st_mode & 0o777 == 0o600
    with pytest.raises((ConflictError, ValidationError)):
        execute_sqlite_restore_plan(restore_plan, executed_at=NOW)


def test_sqlite_backup_rejects_a_copy_newer_than_its_captured_audit_binding(
    tmp_path: Path,
) -> None:
    custody = _owner_directory(tmp_path / "drifted-snapshot")
    source = custody / "source.sqlite3"
    key = P256KeyPair.generate()
    store = SQLiteStore(source, LocalEnvelopeCipher(b"y" * 32))
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
            "VALUES(?,'active',1,1,?)",
            (DOMAIN, int(NOW.timestamp())),
        )
        store.append_audit(connection, {"action": "captured"})
    checkpoint = AuditService(store).checkpoint(key)
    binding = capture_backup_binding(
        store,
        domain_id=DOMAIN,
        checkpoint=checkpoint,
        audit_public_key_pem=key.public_pem,
    )
    with store.transaction() as connection:
        store.append_audit(connection, {"action": "raced_after_binding"})
    store.close()
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "backup.sqlite3",
        manifest_path=custody / "backup.manifest.json",
        binding=binding,
        source_offline=True,
    )

    with pytest.raises(ConflictError, match="exact captured authority/audit snapshot"):
        execute_sqlite_backup_plan(plan, backup_id="drifted-binding", created_at=NOW)

    assert not plan.archive_path.exists()
    assert not plan.manifest_path.exists()


@pytest.mark.parametrize("replaced", ["manifest", "archive"])
def test_unsealed_backup_cleanup_refuses_same_byte_inode_replacement(
    tmp_path: Path,
    replaced: str,
) -> None:
    plan, execution, _key = _executed_sqlite_backup(tmp_path)
    path = plan.manifest_path if replaced == "manifest" else plan.archive_path
    content = path.read_bytes()
    path.rename(path.with_name(path.name + ".original"))
    _owner_file(path, content)

    with pytest.raises(ConflictError, match="identity changed"):
        discard_unsealed_sqlite_backup(execution)

    assert path.read_bytes() == content
    assert plan.archive_path.exists()
    assert plan.manifest_path.exists()


def test_failed_restore_cleanup_refuses_same_byte_inode_replacement(tmp_path: Path) -> None:
    plan, backup_execution, key = _executed_sqlite_backup(tmp_path)
    seal = _signed_seal(backup_execution.manifest, key)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=seal,
        audit_key=key,
    )
    restore_dir = _owner_directory(tmp_path / "receipt-bound-restore")
    target_path = restore_dir / "restored.sqlite3"
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    execution = execute_sqlite_restore_plan(restore_plan, executed_at=NOW)
    content = target_path.read_bytes()
    target_path.rename(target_path.with_name("original-restored.sqlite3"))
    _owner_file(target_path, content)

    with pytest.raises(ConflictError, match="identity changed"):
        discard_failed_sqlite_restore(execution)

    assert not target_path.exists()
    quarantined = list(restore_dir.glob(".agentnet-quarantine-*"))
    assert len(quarantined) == 1
    assert quarantined[0].read_bytes() == content


@pytest.mark.parametrize("changed", ["manifest", "archive"])
def test_unsealed_backup_cleanup_refuses_same_inode_digest_drift(
    tmp_path: Path,
    changed: str,
) -> None:
    plan, execution, _key = _executed_sqlite_backup(tmp_path)
    path = plan.manifest_path if changed == "manifest" else plan.archive_path
    original = path.read_bytes()
    replacement = bytes([original[0] ^ 1]) + original[1:]
    path.write_bytes(replacement)

    with pytest.raises(ConflictError, match="digest changed"):
        discard_unsealed_sqlite_backup(execution)

    assert path.read_bytes() == replacement
    assert plan.archive_path.exists() and plan.manifest_path.exists()


def test_failed_restore_cleanup_refuses_same_inode_digest_drift(tmp_path: Path) -> None:
    plan, backup_execution, key = _executed_sqlite_backup(tmp_path)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=_signed_seal(backup_execution.manifest, key),
        audit_key=key,
    )
    restore_dir = _owner_directory(tmp_path / "digest-drift-restore")
    target_path = restore_dir / "restored.sqlite3"
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=inspect_sqlite_restore_target(
            target_path=target_path,
            application_offline=True,
            inspected_at=NOW,
        ),
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    execution = execute_sqlite_restore_plan(restore_plan, executed_at=NOW)
    original = target_path.read_bytes()
    replacement = bytes([original[0] ^ 1]) + original[1:]
    target_path.write_bytes(replacement)

    with pytest.raises(ConflictError, match="digest changed"):
        discard_failed_sqlite_restore(execution)

    assert not target_path.exists()
    quarantined = list(restore_dir.glob(".agentnet-quarantine-*"))
    assert len(quarantined) == 1
    assert quarantined[0].read_bytes() == replacement


def test_failed_restore_cleanup_quarantines_path_replacement_race(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    plan, backup_execution, key = _executed_sqlite_backup(tmp_path)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=_signed_seal(backup_execution.manifest, key),
        audit_key=key,
    )
    restore_dir = _owner_directory(tmp_path / "rename-race-restore")
    target_path = restore_dir / "restored.sqlite3"
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=inspect_sqlite_restore_target(
            target_path=target_path,
            application_offline=True,
            inspected_at=NOW,
        ),
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    execution = execute_sqlite_restore_plan(restore_plan, executed_at=NOW)
    original = target_path.read_bytes()
    victim = b"v" * len(original)
    real_rename = os.rename
    raced = False

    def replace_before_quarantine(source, destination, *args, **kwargs):
        nonlocal raced
        if not raced and source == target_path.name:
            raced = True
            real_rename(source, ".raced-installed", *args, **kwargs)
            descriptor = os.open(
                source,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL,
                0o600,
                dir_fd=kwargs["src_dir_fd"],
            )
            try:
                os.write(descriptor, victim)
            finally:
                os.close(descriptor)
        return real_rename(source, destination, *args, **kwargs)

    monkeypatch.setattr("agentnet.operations.backup.os.rename", replace_before_quarantine)
    with pytest.raises(ConflictError, match="identity changed"):
        discard_failed_sqlite_restore(execution)

    assert (restore_dir / ".raced-installed").read_bytes() == original
    quarantined = list(restore_dir.glob(".agentnet-quarantine-*"))
    assert len(quarantined) == 1
    assert quarantined[0].read_bytes() == victim


def test_exact_restore_cleanup_retains_verified_random_quarantine_without_unlink(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    plan, backup_execution, key = _executed_sqlite_backup(tmp_path)
    verified = _verify_backup(
        archive_path=plan.archive_path,
        manifest_path=plan.manifest_path,
        seal=_signed_seal(backup_execution.manifest, key),
        audit_key=key,
    )
    restore_dir = _owner_directory(tmp_path / "retained-quarantine-restore")
    target_path = restore_dir / "restored.sqlite3"
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=inspect_sqlite_restore_target(
            target_path=target_path,
            application_offline=True,
            inspected_at=NOW,
        ),
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    execution = execute_sqlite_restore_plan(restore_plan, executed_at=NOW)
    original = target_path.read_bytes()
    real_unlink = os.unlink

    def forbid_quarantine_unlink(path, *args, **kwargs):
        if str(path).startswith(".agentnet-quarantine-"):
            raise AssertionError("verified quarantine must never be pathname-unlinked")
        return real_unlink(path, *args, **kwargs)

    monkeypatch.setattr("agentnet.operations.backup.os.unlink", forbid_quarantine_unlink)
    discard_failed_sqlite_restore(execution)

    assert not target_path.exists()
    quarantined = list(restore_dir.glob(".agentnet-quarantine-*"))
    assert len(quarantined) == 1
    assert quarantined[0].read_bytes() == original


def test_publication_directory_fsync_failure_retains_quarantined_final_name(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    seal_dir = _owner_directory(tmp_path / "ambiguous-seal-custody")
    seal_path = seal_dir / "backup.seal.json"
    real_fsync = os.fsync
    failed = False

    def fail_first_directory_fsync(descriptor: int) -> None:
        nonlocal failed
        if not failed and stat.S_ISDIR(os.fstat(descriptor).st_mode):
            failed = True
            raise OSError("injected directory fsync failure")
        real_fsync(descriptor)

    monkeypatch.setattr("agentnet.operations.backup.os.fsync", fail_first_directory_fsync)
    with pytest.raises(PublicationOutcomeUnknown, match="unknown durability"):
        write_manifest_seal(seal_path, seal)

    assert seal_path.exists()
    assert read_manifest_seal(seal_path) == seal
    assert verified.archive_path.exists() and verified.manifest_path.exists()


def test_publication_temporary_unlink_failure_removes_final_and_staged_names(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binding, key, _checkpoint = binding_material
    _verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    seal_dir = _owner_directory(tmp_path / "unlink-failure-seal-custody")
    seal_path = seal_dir / "backup.seal.json"
    real_unlink = os.unlink
    failed = False

    def fail_first_temporary_unlink(path, *args, **kwargs):
        nonlocal failed
        if not failed and str(path).startswith(".agentnet-"):
            failed = True
            raise OSError("injected temporary unlink failure")
        return real_unlink(path, *args, **kwargs)

    monkeypatch.setattr("agentnet.operations.backup.os.unlink", fail_first_temporary_unlink)
    with pytest.raises(OSError, match="injected"):
        write_manifest_seal(seal_path, seal)

    assert not seal_path.exists()
    quarantined = list(seal_dir.glob(".agentnet-quarantine-*"))
    assert len(quarantined) == 1
    assert read_manifest_seal(quarantined[0]) == seal


def test_successful_publication_never_retries_removed_temporary_name(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binding, key, _checkpoint = binding_material
    _verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    seal_dir = _owner_directory(tmp_path / "single-unlink-seal-custody")
    seal_path = seal_dir / "backup.seal.json"
    real_unlink = os.unlink
    temporary_unlinks = 0

    def fail_second_temporary_unlink(path, *args, **kwargs):
        nonlocal temporary_unlinks
        if str(path).startswith(".agentnet-") and not str(path).startswith(
            ".agentnet-quarantine-"
        ):
            temporary_unlinks += 1
            if temporary_unlinks == 2:
                raise OSError("injected duplicate temporary unlink")
        return real_unlink(path, *args, **kwargs)

    monkeypatch.setattr("agentnet.operations.backup.os.unlink", fail_second_temporary_unlink)
    write_manifest_seal(seal_path, seal)

    assert temporary_unlinks == 1
    assert read_manifest_seal(seal_path) == seal


def test_post_commit_close_failure_is_outcome_unknown_and_retains_final(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binding, key, _checkpoint = binding_material
    _verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    seal_dir = _owner_directory(tmp_path / "close-failure-seal-custody")
    seal_path = seal_dir / "backup.seal.json"
    real_close = os.close
    real_fsync = os.fsync
    committed = False
    failed = False

    def observe_commit(descriptor: int) -> None:
        nonlocal committed
        real_fsync(descriptor)
        if stat.S_ISDIR(os.fstat(descriptor).st_mode):
            committed = True

    def fail_regular_close_after_commit(descriptor: int) -> None:
        nonlocal failed
        is_regular = stat.S_ISREG(os.fstat(descriptor).st_mode)
        real_close(descriptor)
        if committed and is_regular and not failed:
            failed = True
            raise OSError("injected post-commit close failure")

    monkeypatch.setattr("agentnet.operations.backup.os.fsync", observe_commit)
    monkeypatch.setattr("agentnet.operations.backup.os.close", fail_regular_close_after_commit)
    with pytest.raises(PublicationOutcomeUnknown, match="unknown durability"):
        write_manifest_seal(seal_path, seal)

    assert read_manifest_seal(seal_path) == seal


def test_verified_backup_and_target_cannot_be_caller_constructed(
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    tmp_path: Path,
) -> None:
    binding, _key, _checkpoint = binding_material
    with pytest.raises(AuthenticationError):
        VerifiedBackup(
            backend=BackupBackend.SQLITE,
            archive_path=tmp_path / "archive",
            manifest_path=tmp_path / "manifest",
            manifest=None,  # type: ignore[arg-type]
            seal=None,  # type: ignore[arg-type]
            verified_at=NOW,
            _capability=object(),
        )
    with pytest.raises(AuthenticationError):
        RestoreTargetInspection(
            backend=BackupBackend.SQLITE,
            target_fingerprint="0" * 64,
            inspected_at=NOW,
            empty=True,
            application_offline=True,
            _capability=object(),
        )


def test_manifest_write_is_exclusive_nofollow_and_exact_mode(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, _seal, custody = _sqlite_verified_backup(tmp_path, binding, key)
    assert verified.manifest_path.stat().st_mode & 0o777 == 0o600
    assert verified.manifest_path.read_bytes() == canonical_json(verified.manifest.as_dict())
    with pytest.raises(ConflictError):
        write_backup_manifest(verified.manifest_path, verified.manifest)

    victim = _owner_file(custody / "victim", b"unchanged")
    symlink = custody / "manifest-symlink"
    symlink.symlink_to(victim)
    with pytest.raises(ConflictError):
        write_backup_manifest(symlink, verified.manifest)
    assert victim.read_bytes() == b"unchanged"


@pytest.mark.parametrize("tamper", ["archive", "manifest", "seal", "key"])
def test_restore_verification_rejects_every_tamper(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
    tamper: str,
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    archive = verified.archive_path
    manifest_path = verified.manifest_path
    supplied_seal = seal
    supplied_key = key.public_pem
    if tamper == "archive":
        _owner_file(archive, b"SQLite format 3\x00tampered")
    elif tamper == "manifest":
        value = json.loads(manifest_path.read_text())
        value["backup_id"] = "tampered-backup"
        _owner_file(manifest_path, canonical_json(value))
    elif tamper == "seal":
        supplied_seal = replace(seal, manifest_sha256="f" * 64)
    else:
        supplied_key = P256KeyPair.generate().public_pem
    with pytest.raises((AuthenticationError, ValidationError)):
        _verify_backup(
            archive_path=archive,
            manifest_path=manifest_path,
            seal=supplied_seal,
            audit_key=(key if tamper != "key" else P256KeyPair.generate()),
        )


def test_recomputed_authority_binding_without_trusted_seal_signature_is_rejected(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    forged_binding = replace(
        verified.manifest.binding,
        policy_revision=99,
        revocation_epoch=99,
    )
    forged_manifest = replace(verified.manifest, binding=forged_binding)
    _owner_file(
        verified.manifest_path,
        canonical_json(forged_manifest.as_dict()),
    )
    forged_seal = replace(
        seal,
        manifest_sha256=forged_manifest.digest,
        policy_revision=99,
        revocation_epoch=99,
    )
    with pytest.raises(AuthenticationError):
        _verify_backup(
            archive_path=verified.archive_path,
            manifest_path=verified.manifest_path,
            seal=forged_seal,
            audit_key=key,
        )


def test_signed_seal_rejects_unpinned_epoch_root_retirement_and_revocation(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, audit_key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, audit_key)
    other = P256KeyPair.generate()
    base = {
        "archive_path": verified.archive_path,
        "manifest_path": verified.manifest_path,
        "seal": seal,
        "audit_public_key_pem": audit_key.public_pem,
        "seal_public_key_pem": audit_key.public_pem,
        "trusted_signer_key_epoch": 1,
        "expected_trust_root_revision": 1,
        "signer_not_before": int(NOW.timestamp()),
        "verified_at": NOW,
    }
    variants = (
        {"seal_public_key_pem": other.public_pem},
        {"trusted_signer_key_epoch": 2},
        {"expected_trust_root_revision": 2},
        {"signer_retired_at": int(NOW.timestamp()) - 1},
        {"signer_retired_at": int(NOW.timestamp()) + 3600},
        {"signer_revoked_at": int(NOW.timestamp()) + 1},
    )
    for variant in variants:
        with pytest.raises(AuthenticationError):
            verify_backup_for_restore(**(base | variant))


def test_backup_manifest_and_seal_require_exact_integer_versions_and_ordered_time(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    manifest_value = verified.manifest.as_dict()
    manifest_value["version"] = True
    with pytest.raises(ValidationError, match="version"):
        BackupManifest.parse(manifest_value)

    seal_value = seal.as_dict()
    seal_value["version"] = True
    seal_fields = {name: value for name, value in seal_value.items() if name != "signature"}
    seal_value["signature"] = key.sign("agentnet.backup.manifest-seal.v1", seal_fields)
    with pytest.raises(ValidationError, match="version"):
        ManifestSeal.parse(seal_value)

    future_manifest = replace(verified.manifest, created_at=NOW + timedelta(days=1))
    _owner_file(verified.manifest_path, canonical_json(future_manifest.as_dict()))
    fields = seal.signed_fields() | {"manifest_sha256": future_manifest.digest}
    forged_time_seal = ManifestSeal.parse(
        fields | {"signature": key.sign("agentnet.backup.manifest-seal.v1", fields)}
    )
    with pytest.raises(AuthenticationError, match="current in the pinned trust root"):
        _verify_backup(
            archive_path=verified.archive_path,
            manifest_path=verified.manifest_path,
            seal=forged_time_seal,
            audit_key=key,
        )


def test_signed_seal_io_is_canonical_bounded_nofollow_and_duplicate_safe(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, custody = _sqlite_verified_backup(tmp_path, binding, key)
    seal_path = custody / "separate.seal.json"
    assert write_manifest_seal(seal_path, seal) == seal_path
    assert read_manifest_seal(seal_path) == seal

    _owner_file(seal_path, json.dumps(seal.as_dict(), indent=2).encode("utf-8"))
    with pytest.raises(ValidationError, match="canonical"):
        read_manifest_seal(seal_path)
    duplicate = canonical_json(seal.as_dict())[:-1] + b',"version":1}'
    _owner_file(seal_path, duplicate)
    with pytest.raises(ValidationError, match="duplicate"):
        read_manifest_seal(seal_path)
    victim = _owner_file(custody / "seal-victim", canonical_json(seal.as_dict()))
    seal_path.unlink()
    seal_path.symlink_to(victim)
    with pytest.raises(ValidationError):
        read_manifest_seal(seal_path)


def test_backup_and_restore_refuse_swapped_custody_directories(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    custody = _owner_directory(tmp_path / "planned-custody")
    source = _owner_file(custody / "source.sqlite3", b"SQLite format 3\x00planned")
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=custody / "backup.sqlite3",
        manifest_path=custody / "backup.json",
        binding=binding,
        source_offline=True,
    )
    moved = tmp_path / "moved-custody"
    custody.rename(moved)
    _owner_directory(custody)
    with pytest.raises(ConflictError, match="custody directory changed"):
        execute_sqlite_backup_plan(plan, backup_id="swapped-custody", created_at=NOW)
    assert not (custody / "backup.sqlite3").exists()

    verified, _seal, _backup_custody = _sqlite_verified_backup(tmp_path, binding, key)
    target_dir = _owner_directory(tmp_path / "planned-restore")
    target_path = target_dir / "restored.sqlite3"
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    restore = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=DOMAIN,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
        planned_at=NOW,
    )
    target_dir.rename(tmp_path / "moved-restore")
    _owner_directory(target_dir)
    with pytest.raises(ConflictError, match="changed"):
        execute_sqlite_restore_plan(restore, executed_at=NOW)
    assert not target_path.exists()


def test_manifest_rejects_noncanonical_and_extra_fields(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    value = verified.manifest.as_dict()
    value["caller_verified"] = True
    _owner_file(verified.manifest_path, json.dumps(value, indent=2).encode())
    with pytest.raises(ValidationError):
        _verify_backup(
            archive_path=verified.archive_path,
            manifest_path=verified.manifest_path,
            seal=seal,
            audit_key=key,
        )


def test_sqlite_target_must_remain_absent_offline_and_fresh(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, _seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    target_dir = _owner_directory(tmp_path / "restore-race")
    target_path = target_dir / "database.sqlite3"
    with pytest.raises(GateBlocked):
        inspect_sqlite_restore_target(target_path=target_path, application_offline=False)
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    _owner_file(target_path, b"raced")
    with pytest.raises(ConflictError):
        build_sqlite_restore_plan(
            backup=verified,
            target=target,
            target_path=target_path,
            expected_domain_id=DOMAIN,
            expected_schema_version=CURRENT_SCHEMA_VERSION,
            planned_at=NOW,
        )
    target_path.unlink()
    with pytest.raises(ConflictError, match="stale"):
        build_sqlite_restore_plan(
            backup=verified,
            target=target,
            target_path=target_path,
            expected_domain_id=DOMAIN,
            expected_schema_version=CURRENT_SCHEMA_VERSION,
            planned_at=NOW + timedelta(minutes=6),
        )


def test_postgresql_restore_refuses_caller_asserted_catalog_emptiness(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    _postgres_verified_backup(tmp_path, binding, key)
    with pytest.raises(GateBlocked, match="caller-asserted"):
        inspect_postgresql_restore_target(
            database_url=PG_TARGET,
            application_offline=True,
            non_system_object_count=1,
            inspected_at=NOW,
        )
    with pytest.raises(GateBlocked, match="caller-asserted"):
        inspect_postgresql_restore_target(
            database_url=PG_TARGET,
            application_offline=True,
            non_system_object_count=0,
            inspected_at=NOW,
        )


def test_owner_only_directory_rejects_permissions_and_symlink(tmp_path: Path) -> None:
    private = _owner_directory(tmp_path / "private")
    assert validate_owner_only_directory(private) == private.resolve()
    private.chmod(0o750)
    with pytest.raises(ValidationError):
        validate_owner_only_directory(private)
    private.chmod(0o700)
    symlink = tmp_path / "private-link"
    symlink.symlink_to(private, target_is_directory=True)
    with pytest.raises(ValidationError):
        validate_owner_only_directory(symlink)


def test_sqlite_store_rejects_database_path_swap_during_descriptor_open(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    directory = _owner_directory(tmp_path / "sqlite-open-race")
    database = directory / "core.sqlite3"
    moved = directory / "moved-original.sqlite3"
    real_connect = sqlite3.connect

    def race_connect(target, *args, **kwargs):
        database.rename(moved)
        _owner_file(database, b"")
        return real_connect(target, *args, **kwargs)

    monkeypatch.setattr(sqlite3, "connect", race_connect)
    with pytest.raises(GateBlocked, match="path changed"):
        SQLiteStore(database, LocalEnvelopeCipher(b"r" * 32))
    assert database.exists()
    assert database.stat().st_ino != moved.stat().st_ino


def test_verified_archive_race_after_verification_blocks_restore_plan(
    tmp_path: Path,
    binding_material: tuple[BackupBinding, P256KeyPair, dict[str, object]],
) -> None:
    binding, key, _checkpoint = binding_material
    verified, _seal, _custody = _sqlite_verified_backup(tmp_path, binding, key)
    target_dir = _owner_directory(tmp_path / "race-target")
    target_path = target_dir / "database.sqlite3"
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
        inspected_at=NOW,
    )
    _owner_file(verified.archive_path, b"SQLite format 3\x00changed-after-verify")
    with pytest.raises(AuthenticationError, match="sealed manifest"):
        build_sqlite_restore_plan(
            backup=verified,
            target=target,
            target_path=target_path,
            expected_domain_id=DOMAIN,
            expected_schema_version=CURRENT_SCHEMA_VERSION,
            planned_at=NOW,
        )


def test_module_never_exposes_execute_or_marks_recovery_complete() -> None:
    import agentnet.operations.backup as backup_module

    public_names = set(backup_module.__all__)
    assert not {"execute", "run_backup", "run_restore", "rotate_credentials", "resume_service"} & public_names
    assert ArchiveFormat.POSTGRESQL_CUSTOM.value == "postgresql_custom"
    assert BackupBackend.SQLITE.value == "sqlite"
