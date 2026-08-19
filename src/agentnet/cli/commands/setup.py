"""CLI commands for local profile init, network create, SQLite backup/restore, and server-agent setup/reset/activation."""

from __future__ import annotations

import argparse
import base64
import getpass
import hashlib
import json
import os
import secrets
import signal
import socket
import stat
import subprocess
import sys
import time
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from uuid import NAMESPACE_URL, UUID, uuid4, uuid5

try:
    import pwd as posix_pwd
except ModuleNotFoundError:
    posix_pwd = None

try:
    import fcntl
except ModuleNotFoundError:
    fcntl = None

import httpx

from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.audit.service import AuditService
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import (
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    ManagedServerCredentialReauthorizationRequestV2,
    ManagedServerCredentialReauthorizationService,
    load_credential_binding,
    public_key_thumbprint,
)
from agentnet.operations.backup import (
    BackupBinding,
    ManifestSeal,
    PublicationOutcomeUnknown,
    VerifiedBackup,
    build_compromise_rebuild_plan,
    build_sqlite_backup_plan,
    build_sqlite_restore_plan,
    capture_backup_binding,
    discard_failed_sqlite_restore,
    discard_unsealed_sqlite_backup,
    execute_sqlite_backup_plan,
    execute_sqlite_restore_plan,
    inspect_sqlite_restore_target,
    read_manifest_seal,
    verify_backup_for_restore,
    write_manifest_seal,
)
from agentnet.operations.c0_credential_supersession import (
    append_supersession,
    canonical_supersession_journal,
    completed_c0_terminal_credential,
    load_audited_supersession_journal,
)
from agentnet.operations.config import (
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
    ScannerTrustConfig,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.operations.incident import DomainIncidentService
from agentnet.operations.outage import OutageGate
from agentnet.operations.server_reset import ServerSetupResetError, reset_server_setup
from agentnet.operations.server_setup import (
    C0_RESPONDER_TERMINAL,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_ENV,
    CORE_USER,
    CREDENTIAL_SUPERSESSION_JOURNAL,
    SERVER_AGENT_IDENTITY,
    SERVER_AGENT_KEY,
    SETUP_ROOT,
    ServerSetupError,
    _parse_environment_file,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
)
from agentnet.security.dpop import canonical_service_audience
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.storage.postgres import PostgreSQLStore
from agentnet.storage.sqlite import SQLiteStore
from agentnet.cli import helpers


def _provision_owner_only_key(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.parent.is_symlink() or path.parent.stat().st_mode & 0o077:
        raise SystemExit(f"key directory must be an owner-only real directory: {path.parent}")
    if os.path.lexists(path):
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077 or path.stat().st_size != 32:
            raise SystemExit(f"existing key is not an owner-only 32-byte file: {path}")
        return
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        remaining = memoryview(os.urandom(32))
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("key provisioning write made no progress")
            remaining = remaining[written:]
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _provision_owner_only_signing_key(path: Path) -> P256KeyPair:
    """Create or reload one exact owner-only P-256 software key."""

    path = path.absolute()
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if (
        path.parent.is_symlink()
        or path.parent.stat().st_uid != os.geteuid()
        or path.parent.stat().st_mode & 0o077
    ):
        raise SystemExit(f"signing-key directory must be an owner-only real directory: {path.parent}")
    if os.path.lexists(path):
        return P256KeyPair.from_private_pem(
            helpers._owner_only_file(path, label="existing backup seal private key")
        )
    key = P256KeyPair.generate()
    helpers._write_owner_only(path, key.private_pem)
    return key


def _local_sqlite_path(config: ExtensionConfig) -> Path:
    if config.profile is not RuntimeProfile.LOCAL_CONFORMANCE:
        raise SystemExit("SQLite backup and restore commands require the local-conformance profile")
    if not config.database_url.startswith("sqlite:///"):
        raise SystemExit("local-conformance configuration does not name an exact SQLite database")
    configured = Path(config.database_url.removeprefix("sqlite:///"))
    return configured if configured.is_absolute() else config.data_dir.parent / configured


def _parse_manifest_seal(path: Path) -> ManifestSeal:
    try:
        return read_manifest_seal(path)
    except Exception as exc:
        raise SystemExit("backup manifest seal is invalid") from exc


def _seal_json(seal: ManifestSeal) -> dict[str, object]:
    return seal.as_dict()


def _backup_seal_pin(config: ExtensionConfig, seal: ManifestSeal | None = None) -> BackupSealKeyConfig:
    trust = config.backup_trust
    if trust is None or trust.domain_id != config.domain_id:
        raise SystemExit("configuration lacks an exact backup-seal trust root")
    key_id = trust.active_signer_key_id if seal is None else seal.signer_key_id
    pin = trust.key_by_id(key_id)
    if pin is None:
        raise SystemExit("backup seal signer is not pinned by the selected configuration")
    if (
        pin.key_epoch < trust.minimum_key_epoch
        or pin.revoked_at is not None
        or pin.retired_at is not None
    ):
        raise SystemExit("backup seal signer is revoked or below the minimum trusted epoch")
    if seal is None and (pin.retired_at is not None or pin.key_id != trust.active_signer_key_id):
        raise SystemExit("backup requires the current configured seal signer")
    return pin


def _open_server_agent_activation_store(
    config: ExtensionConfig,
    *,
    database_url_override: str | None = None,
) -> PostgreSQLStore:
    """Open the exact runtime lease without migrations or background work.

    Reusing the configured runtime instance lease proves the ordinary server
    agent is offline and keeps it fenced until the config replacement finishes.
    """

    if config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        raise SystemExit("server-agent activation requires always_on_server_agent profile")
    cipher = LocalEnvelopeCipher.from_key_file(
        config.data_dir / "secrets" / "records.key",
        create=False,
    )
    return PostgreSQLStore(
        database_url_override or config.resolved_database_url(),
        cipher,
        instance_id=config.runtime_instance_id,
        lease_owner_id=f"activation-{uuid4().hex}",
        connect_timeout=config.postgres_connect_timeout_seconds,
        statement_timeout_ms=config.postgres_statement_timeout_ms,
        lock_timeout_ms=config.postgres_lock_timeout_ms,
        lease_ttl_seconds=config.postgres_lease_ttl_seconds,
        run_migrations=False,
        start_lease_keeper=False,
        require_recovery_topology=config.postgres_recovery_topology,
    )


def _open_server_agent_activation_store_as_core_peer(
    config: ExtensionConfig,
    *,
    database_url_override: str,
    core_account: Any,
) -> PostgreSQLStore:
    """Open PostgreSQL under the fixed Core peer identity, then restore root."""

    original_euid = os.geteuid()
    original_egid = os.getegid()
    original_groups = os.getgroups()
    groups_changed = False
    egid_changed = False
    euid_changed = False
    try:
        os.setgroups([core_account.pw_gid])
        groups_changed = True
        os.setegid(core_account.pw_gid)
        egid_changed = True
        os.seteuid(core_account.pw_uid)
        euid_changed = True
        return _open_server_agent_activation_store(
            config,
            database_url_override=database_url_override,
        )
    finally:
        if euid_changed:
            os.seteuid(original_euid)
        if egid_changed:
            os.setegid(original_egid)
        if groups_changed:
            os.setgroups(original_groups)


def _require_server_agent_activation_binding(
    store: PostgreSQLStore,
    *,
    config: ExtensionConfig,
    actor: VerifiedActor,
    key: P256KeyPair,
) -> None:
    if actor.principal_id is None or actor.harness_id is None or actor.credential_id is None:
        raise SystemExit("server-agent activation requires an exact human-owned harness identity")
    binding = load_credential_binding(store, actor.credential_id)
    binding.require_active(now=int(datetime.now(UTC).timestamp()))
    if (
        binding.credential_id != actor.credential_id
        or binding.domain_id != config.domain_id
        or binding.domain_id != actor.domain_id
        or binding.harness_id != actor.harness_id
        or binding.principal_id != actor.principal_id
        or binding.credential_epoch != actor.credential_epoch
        or binding.binding_assurance != actor.binding_assurance
        or binding.binding_assurance == "lab"
        or binding.public_key_pem != key.public_pem
        or binding.key_id != public_key_thumbprint(key.public_pem)
    ):
        raise SystemExit("server-agent identity does not match its current stored credential binding")


def command_backup_sqlite(args: argparse.Namespace) -> int:
    """Create an exact offline local-profile backup plus separate-custody seal."""

    if args.application_offline is not True:
        raise SystemExit("SQLite backup requires explicit --application-offline confirmation")
    config = helpers._load_config(Path(args.config))
    source = _local_sqlite_path(config).absolute()
    archive = Path(args.archive).absolute()
    manifest_path = Path(args.manifest).absolute()
    seal_path = Path(args.seal).absolute()
    if seal_path.parent == archive.parent:
        raise SystemExit("backup seal must use a separate custody directory")
    audit_key_path = Path(args.audit_private_key).absolute()
    audit_key = P256KeyPair.from_private_pem(
        helpers._owner_only_file(audit_key_path, label="audit checkpoint private key")
    )
    seal_key = P256KeyPair.from_private_pem(
        helpers._owner_only_file(
            Path(args.seal_private_key).absolute(),
            label="backup seal private key",
        )
    )
    seal_pin = _backup_seal_pin(config)
    if public_key_thumbprint(seal_key.public_pem) != seal_pin.key_id:
        raise SystemExit("backup seal private key does not match the active configured public pin")
    cipher = LocalEnvelopeCipher.from_key_file(
        config.data_dir / "secrets" / "records.key",
        create=False,
    )
    store = SQLiteStore(source, cipher)
    try:
        checkpoint = AuditService(store).checkpoint(audit_key)
        binding = capture_backup_binding(
            store,
            domain_id=config.domain_id,
            checkpoint=checkpoint,
            audit_public_key_pem=audit_key.public_pem,
        )
    finally:
        store.close()
    plan = build_sqlite_backup_plan(
        source_path=source,
        archive_path=archive,
        manifest_path=manifest_path,
        binding=binding,
        source_offline=True,
    )
    execution = execute_sqlite_backup_plan(plan, backup_id=args.backup_id)
    manifest = execution.manifest
    assert config.backup_trust is not None
    try:
        seal = ManifestSeal.create(
            manifest=manifest,
            signer=seal_key,
            signer_key_epoch=seal_pin.key_epoch,
            trust_root_revision=config.backup_trust.trust_root_revision,
        )
        write_manifest_seal(seal_path, seal)
    except PublicationOutcomeUnknown:
        raise
    except Exception:
        discard_unsealed_sqlite_backup(execution)
        raise
    print(
        json.dumps(
            {
                "archive": str(plan.archive_path),
                "manifest": str(plan.manifest_path),
                "seal": str(seal_path),
                "backup_id": manifest.backup_id,
                "domain_id": binding.domain_id,
                "schema_version": binding.schema_version,
                "sha256": manifest.sha256,
                "ha_proven": False,
                "pitr_proven": False,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def _verified_sqlite_backup_from_args(
    args: argparse.Namespace,
    *,
    config: ExtensionConfig,
) -> tuple[VerifiedBackup, str]:
    seal = _parse_manifest_seal(Path(args.seal).absolute())
    seal_pin = _backup_seal_pin(config, seal)
    assert config.backup_trust is not None
    public_key = helpers._owner_only_file(
        Path(args.audit_public_key).absolute(),
        label="audit checkpoint public key",
    ).decode("ascii")
    verified = verify_backup_for_restore(
        archive_path=Path(args.archive).absolute(),
        manifest_path=Path(args.manifest).absolute(),
        seal=seal,
        audit_public_key_pem=public_key,
        seal_public_key_pem=seal_pin.public_key_pem,
        trusted_signer_key_epoch=seal_pin.key_epoch,
        expected_trust_root_revision=config.backup_trust.trust_root_revision,
        signer_not_before=seal_pin.not_before,
        signer_retired_at=seal_pin.retired_at,
        signer_revoked_at=seal_pin.revoked_at,
    )
    if verified.manifest.binding.domain_id != config.domain_id:
        raise SystemExit("backup domain does not match the selected AgentNet configuration")
    return verified, public_key


def command_restore_sqlite(args: argparse.Namespace) -> int:
    """Restore exact verified local-profile bytes to an absent offline target."""

    if args.application_offline is not True:
        raise SystemExit("SQLite restore requires explicit --application-offline confirmation")
    config = helpers._load_config(Path(args.config))
    _local_sqlite_path(config)
    verified, public_key = _verified_sqlite_backup_from_args(args, config=config)
    target_path = Path(args.target).absolute()
    helpers._owner_only_directory(target_path.parent)
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
    )
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=config.domain_id,
        expected_schema_version=CURRENT_SCHEMA_VERSION,
    )
    restore_execution = None
    try:
        restore_execution = execute_sqlite_restore_plan(restore_plan)
        cipher = LocalEnvelopeCipher.from_key_file(
            config.data_dir / "secrets" / "records.key",
            create=False,
        )
        restored_store = SQLiteStore(target_path, cipher)
        try:
            restored_binding = capture_backup_binding(
                restored_store,
                domain_id=config.domain_id,
                checkpoint=verified.manifest.binding.audit_checkpoint,
                audit_public_key_pem=public_key,
            )
            if restored_binding != verified.manifest.binding:
                raise GateBlocked("restore_binding", "restored SQLite authority binding differs")
        finally:
            restored_store.close()
    except PublicationOutcomeUnknown:
        raise
    except Exception as exc:
        if restore_execution is None:
            raise
        try:
            discard_failed_sqlite_restore(restore_execution)
        except Exception as cleanup_exc:
            raise GateBlocked(
                "restore_cleanup",
                "failed restore bytes changed after installation and were retained for quarantine",
            ) from cleanup_exc
        raise exc
    print(
        json.dumps(
            {
                "target": str(target_path),
                "backup_id": verified.manifest.backup_id,
                "domain_id": config.domain_id,
                "schema_version": verified.manifest.binding.schema_version,
                "restore_completed": True,
                "signed_manifest_seal_verified": True,
                "audit_checkpoint_signature_verified": True,
                "restored_archive_digest_verified": True,
                "restored_domain_snapshot_matches_manifest": True,
                "service_safe_to_resume": False,
                "resume_requirement": "independent recovery approval and required credential rotations",
                "ha_proven": False,
                "pitr_proven": False,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_compromise_rebuild_plan(args: argparse.Namespace) -> int:
    """Emit the exact unexecuted compromise-rebuild sequence for a verified backup."""

    if args.application_offline is not True:
        raise SystemExit("compromise rebuild planning requires explicit --application-offline confirmation")
    config = helpers._load_config(Path(args.config))
    _local_sqlite_path(config)
    verified, _public_key = _verified_sqlite_backup_from_args(args, config=config)
    target_path = Path(args.target).absolute()
    helpers._owner_only_directory(target_path.parent)
    target = inspect_sqlite_restore_target(
        target_path=target_path,
        application_offline=True,
    )
    restore_plan = build_sqlite_restore_plan(
        backup=verified,
        target=target,
        target_path=target_path,
        expected_domain_id=config.domain_id,
        expected_schema_version=verified.manifest.binding.schema_version,
    )
    plan = build_compromise_rebuild_plan(
        domain_id=config.domain_id,
        restore_plan=restore_plan,
    )
    output = {
        "state": plan.state,
        "domain_id": plan.domain_id,
        "backend": plan.backend.value,
        "backup_id": plan.backup_id,
        "manifest_sha256": plan.manifest_sha256,
        "audit_checkpoint_digest": plan.audit_checkpoint_digest,
        "ordered_actions": [item.value for item in plan.ordered_actions],
        "credential_rotations": [
            {
                "credential_class": item.credential_class.value,
                "state": item.state,
            }
            for item in plan.credential_rotations
        ],
        "restore_completed": False,
        "rotations_completed": False,
        "service_safe_to_resume": False,
        "ha_proven": False,
        "pitr_proven": False,
    }
    if args.output:
        helpers._write_owner_json(Path(args.output).absolute(), output)
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


def command_init(args: argparse.Namespace) -> int:
    path = Path(args.config)
    data_dir = Path(args.data_dir)
    helpers._owner_only_directory(data_dir.absolute())
    seal_key_path = (data_dir / "secrets" / "backup-seal.key.pem").absolute()
    seal_key = _provision_owner_only_signing_key(seal_key_path)
    activated_at = int(datetime.now(UTC).replace(microsecond=0).timestamp())
    config = ExtensionConfig(
        domain_id=args.domain,
        data_dir=data_dir,
        database_url=f"sqlite:///{data_dir / 'core.sqlite3'}",
        artifact_dir=data_dir / "artifacts",
        public_base_url=args.public_base_url,
        backup_trust=BackupTrustConfig(
            domain_id=args.domain,
            trust_root_revision=1,
            minimum_key_epoch=1,
            active_signer_key_id=public_key_thumbprint(seal_key.public_pem),
            keys=(
                BackupSealKeyConfig(
                    key_id=public_key_thumbprint(seal_key.public_pem),
                    key_epoch=1,
                    public_key_pem=seal_key.public_pem,
                    not_before=activated_at,
                ),
            ),
        ),
    )
    helpers._write_private_config(path, config.redacted_export(), force=args.force)
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
    finally:
        core.close()
    print(
        json.dumps(
            {
                "config": str(path),
                "data_dir": str(data_dir),
                "profile": config.profile.value,
                "backup_seal_private_key": str(seal_key_path),
                "backup_seal_key_custody": "local_software_key_not_production_kms",
            }
        )
    )
    return 0


def command_network_create(args: argparse.Namespace) -> int:
    """Create one production server-agent namespace without inventing a founder."""

    path = Path(args.config)
    if args.artifact_mode == "enabled" and not args.scanner_trust_config:
        raise SystemExit("artifact_mode=enabled requires scanner trust configuration")
    if args.artifact_mode == "disabled" and args.scanner_trust_config:
        raise SystemExit("artifact_mode=disabled forbids scanner trust configuration")
    oidc_value = Path(args.oidc_config).read_text(encoding="utf-8")
    try:
        oidc = OIDCEnrollmentConfig.model_validate_json(oidc_value)
    except Exception as exc:
        raise SystemExit("OIDC/independent-approval configuration is invalid") from exc
    scanner_trust = None
    if args.scanner_trust_config:
        try:
            scanner_trust = ScannerTrustConfig.model_validate_json(
                Path(args.scanner_trust_config).read_text(encoding="utf-8")
            )
        except Exception as exc:
            raise SystemExit("scanner trust configuration is invalid") from exc
    domain_id = args.domain or f"network-{uuid4().hex}.agentnet"
    data_dir = Path(args.data_dir)
    helpers._owner_only_directory(data_dir.absolute())
    seal_key_path = (data_dir / "secrets" / "backup-seal.key.pem").absolute()
    seal_key = _provision_owner_only_signing_key(seal_key_path)
    activated_at = int(datetime.now(UTC).replace(microsecond=0).timestamp())
    database_url = args.database_url
    if args.database_url_from_env:
        database_url = os.environ.get(args.database_url_env)
        if not database_url:
            raise SystemExit("network create database URL environment reference is absent")
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=domain_id,
        data_dir=data_dir,
        database_url=database_url,
        database_url_env=args.database_url_env,
        artifact_mode=args.artifact_mode,
        artifact_backend="postgres-manifest",
        artifact_dir=data_dir / "artifacts",
        public_base_url=args.public_base_url,
        runtime_instance_id=args.runtime_instance_id,
        oidc_enrollment=oidc,
        scanner_trust=scanner_trust,
        server_agent_capabilities=(
            {ServerAgentCapability.OFFLINE_CUSTODY}
            if args.artifact_mode == "disabled"
            else {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
            }
        ),
        postgres_recovery_topology=args.postgres_recovery_topology,
        backup_trust=BackupTrustConfig(
            domain_id=domain_id,
            trust_root_revision=1,
            minimum_key_epoch=1,
            active_signer_key_id=public_key_thumbprint(seal_key.public_pem),
            keys=(
                BackupSealKeyConfig(
                    key_id=public_key_thumbprint(seal_key.public_pem),
                    key_epoch=1,
                    public_key_pem=seal_key.public_pem,
                    not_before=activated_at,
                ),
            ),
        ),
    )
    _provision_owner_only_key(data_dir / "secrets" / "records.key")
    if args.artifact_mode == "enabled":
        _provision_owner_only_key(data_dir / "secrets" / "artifact.key")
    helpers._write_private_config(path, config.redacted_export(), force=args.force)
    core = CommunicationCore.open(config, validate_deployment_identity=False)
    try:
        domain = core.bootstrap_domain()
        local_readiness = core.readiness()
    finally:
        core.close()
    print(
        json.dumps(
            {
                "config": str(path),
                "data_dir": str(data_dir),
                "domain": domain,
                "local_readiness": local_readiness,
                "namespace_semantics": (
                    "domain_id is an opaque private AgentNet namespace; it is not proof of DNS ownership"
                ),
                "backup_seal_private_key": str(seal_key_path),
                "backup_seal_key_custody": "local_software_key_not_production_kms",
                "next": [
                    (
                        "run agentnet join guided with --server "
                        + config.public_base_url
                        + ", --domain "
                        + config.domain_id
                        + ", the exact supported --harness, an approved --name, "
                        "--state .agentnet/guided-join.json, and "
                        "--identity .agentnet/server-agent-identity.json"
                    ),
                    "agentnet server-agent activate --config "
                    + str(path)
                    + " --identity .agentnet/server-agent-identity.json",
                    "agentnet serve --config " + str(path),
                    (
                        "after exactly two guided same-principal harnesses enroll, run agentnet "
                        "bootstrap-plan begin --identity <fresh-identity.json>"
                    ),
                    "the fixed C0 service alone activates the pending guard after exact plan approval",
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if bool(local_readiness.get("ready")) else 1


@contextmanager
def _server_setup_deadline(seconds: int = 600):
    if not hasattr(signal, "SIGALRM") or not hasattr(signal, "setitimer"):
        yield
        return

    def exceeded(_signum, _frame) -> None:
        raise ServerSetupError(
            "setup_deadline_exceeded",
            f"guided server setup exceeded its {seconds}-second deadline; rerun resumes exact state",
        )

    started = time.monotonic()
    prior_handler = signal.getsignal(signal.SIGALRM)
    prior_timer = signal.setitimer(signal.ITIMER_REAL, 0.0)
    signal.signal(signal.SIGALRM, exceeded)
    signal.setitimer(signal.ITIMER_REAL, float(seconds))
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0.0)
        signal.signal(signal.SIGALRM, prior_handler)
        prior_delay, prior_interval = prior_timer
        if prior_delay > 0.0 or prior_interval > 0.0:
            elapsed = time.monotonic() - started
            signal.setitimer(
                signal.ITIMER_REAL,
                max(0.000001, prior_delay - elapsed),
                prior_interval,
            )


def _setup_progress(phase: str, started: float, action: str) -> None:
    elapsed = max(0.0, time.monotonic() - started)
    print(
        f"phase={phase} elapsed={elapsed:.1f}s action={action}",
        file=sys.stderr,
        flush=True,
    )


def command_server_agent_setup(args: argparse.Namespace) -> int:
    """Plan or apply the fixed product-owned ordinary Linux server profile."""

    started = time.monotonic()
    phase = "discover"
    try:
        with _server_setup_deadline():
            guided = args.request is None
            if args.start and not args.apply:
                raise ServerSetupError("invalid_action", "--start requires --apply")
            if not guided and args.apply and not args.expected_request_digest:
                raise ServerSetupError(
                    "approval_digest_required",
                    "--apply requires --expected-request-digest from the frozen no-managed-host-write plan",
                )
            if not args.apply and args.expected_request_digest:
                raise ServerSetupError(
                    "invalid_action",
                    "--expected-request-digest requires --apply",
                )
            if guided and args.expected_request_digest:
                raise ServerSetupError(
                    "invalid_action",
                    "guided setup derives the approved request digest itself",
                )
            request_path = (
                SETUP_ROOT / "server-setup.json"
                if guided
                else Path(str(args.request))
            )
            if guided:
                _setup_progress("discover", started, "load standard owner-only prerequisites")
            try:
                request = load_server_setup_request(request_path)
            except ServerSetupError as exc:
                if guided and exc.blocker == "missing_input":
                    raise ServerSetupError(
                        "missing_external_prerequisite",
                        "create the owner-only standard prerequisite file at "
                        f"{request_path}",
                    ) from exc
                raise

            phase = "plan"
            if guided:
                _setup_progress("plan", started, "validate and freeze exact setup plan")
            plan = plan_server_setup(request)
            if guided and args.apply:
                phase = "approve"
                _setup_progress("approve", started, "await one exact terminal approval")
                if not sys.stdin.isatty():
                    raise ServerSetupError(
                        "plan_approval_required",
                        "guided server setup requires one interactive terminal approval",
                    )
                summary = {
                    key: plan[key]
                    for key in (
                        "schema",
                        "status",
                        "package_version",
                        "profile",
                        "request_digest",
                        "managed_units",
                        "public_core_origin",
                    )
                    if key in plan
                }
                print(json.dumps(summary, indent=2, sort_keys=True), file=sys.stderr)
                print(
                    "Apply this exact AgentNet server setup plan? [yes/no]",
                    file=sys.stderr,
                    flush=True,
                )
                if sys.stdin.readline().strip().lower() != "yes":
                    raise ServerSetupError(
                        "approval_declined",
                        "guided server setup plan was not approved",
                    )
                phase = "apply"
                _setup_progress("apply", started, "apply exact plan and managed services")
                result = apply_server_setup(
                    request,
                    start=bool(args.start),
                    expected_request_digest=str(plan["request_digest"]),
                )
                phase = "verify"
                _setup_progress("verify", started, "confirm exact setup result")
            elif guided:
                result = plan
            else:
                phase = "apply" if args.apply else "plan"
                result = (
                    apply_server_setup(
                        request,
                        start=bool(args.start),
                        expected_request_digest=str(args.expected_request_digest),
                    )
                    if args.apply
                    else plan
                )
    except ServerSetupError as exc:
        blocker = exc.blocker
        message = str(exc)
        identity_enrolled = exc.identity_enrolled
    except Exception:
        blocker = "internal_setup_failure"
        message = "server setup failed before producing verified evidence"
        identity_enrolled = False
    else:
        result = {
            "phase": phase,
            **result,
            "setup_elapsed_seconds": round(time.monotonic() - started, 3),
        }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    print(
        json.dumps(
            {
                "schema": "agentnet.server-setup.evidence.v1",
                "status": "blocked",
                "phase": phase,
                "blocker": blocker,
                "message": message,
                "responsible_component": "agentnet-server-setup",
                "safe_action": "retained exact resumable setup state",
                "rerun_resumes": True,
                "human_action": (
                    "supply the named prerequisite or approval, then rerun the same command"
                ),
                "authority_granted": False,
                "identity_enrolled": identity_enrolled,
                "production_durability_proven": False,
                "setup_elapsed_seconds": round(time.monotonic() - started, 3),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 1


def command_server_agent_reset(args: argparse.Namespace) -> int:
    """Remove only package-owned ordinary server state after explicit confirmation."""

    try:
        if not bool(args.confirm_package_state_removal):
            raise ServerSetupResetError(
                "reset_confirmation_required",
                "server setup reset requires explicit package-state removal confirmation",
            )
        result = reset_server_setup(
            retain_external_prerequisites=bool(args.retain_external_prerequisites),
        )
    except ServerSetupResetError as exc:
        result = {
            "schema": "agentnet.server-setup.reset-evidence.v1",
            "state": "blocked",
            "blocker": exc.blocker,
            "message": str(exc),
            "external_prerequisites": "retained",
            "authority_granted": False,
            "identity_enrolled": False,
            "production_durability_proven": False,
        }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 1
    except Exception:
        print(
            json.dumps(
                {
                    "schema": "agentnet.server-setup.reset-evidence.v1",
                    "state": "blocked",
                    "blocker": "internal_reset_failure",
                    "message": "server setup reset failed before producing verified evidence",
                    "external_prerequisites": "retained",
                    "authority_granted": False,
                    "identity_enrolled": False,
                    "production_durability_proven": False,
                },
                indent=2,
                sort_keys=True,
            )
        )
        return 1
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def _managed_server_reauthorization_verifier(
    config: ExtensionConfig,
) -> tuple[IndependentApprovalVerifier, object]:
    oidc = config.oidc_enrollment
    if oidc is None or oidc.approval_service is None:
        raise SystemExit("managed-server credential reauthorization requires configured Approval")
    approval = oidc.approval_service
    if not any(
        item.principal_id == approval.approver_principal_id
        and MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE
        in item.allowed_purposes
        for item in oidc.trusted_approvers
    ):
        raise SystemExit("configured Approval owner cannot authorize credential recovery")
    trusted: dict[str, TrustedApprover] = {}
    for item in oidc.trusted_approvers:
        if public_key_thumbprint(item.public_key_pem) != item.signer_key_id:
            raise SystemExit("configured Approval trust key is invalid")
        trusted[item.signer_key_id] = TrustedApprover(
            principal_id=item.principal_id,
            domain_id=config.domain_id,
            signer_key_id=item.signer_key_id,
            public_key_pem=item.public_key_pem,
            allowed_purposes=item.allowed_purposes,
            authority_kind=item.authority_kind,
        )
    return IndependentApprovalVerifier(trusted, verifier_id=oidc.verifier_id), approval


def _managed_server_reauthorization_client(
    config: ExtensionConfig,
    *,
    broker_credential: str,
) -> ApprovalServiceClient:
    _verifier, approval = _managed_server_reauthorization_verifier(config)
    if not 43 <= len(broker_credential) <= 512:
        raise SystemExit("Approval broker credential is unavailable")
    return ApprovalServiceClient(approval, broker_credential)


def _require_managed_server_reauthorization_topology(config: ExtensionConfig) -> None:
    """Keep the corrective path inside the exact communication-only topology.

    The recovery command separately validates immutable C0 terminal provenance
    and every post-C0 supersession before it can replace a credential.
    """

    if config.a2a is not None or config.relay is not None:
        raise SystemExit(
            "managed-server credential reauthorization requires the communication-only topology"
        )


def _managed_private_file(
    path: Path,
    *,
    label: str,
    expected_uid: int | None = None,
) -> tuple[bytes, os.stat_result]:
    if not path.is_absolute():
        raise SystemExit(f"{label} must be an absolute managed private file")
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise SystemExit(f"{label} is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or stat.S_IMODE(before.st_mode) != 0o600
            or before.st_size < 1
            or before.st_size > 65_536
            or (expected_uid is not None and before.st_uid != expected_uid)
            or (expected_uid is None and os.geteuid() != 0 and before.st_uid != os.geteuid())
        ):
            raise SystemExit(f"{label} custody is unsafe")
        content = bytearray()
        while True:
            chunk = os.read(descriptor, 16_384)
            if not chunk:
                break
            content.extend(chunk)
        after = os.fstat(descriptor)
        if (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_gid,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        ) != (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_gid,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        ):
            raise SystemExit(f"{label} changed while it was read")
        return bytes(content), before
    finally:
        os.close(descriptor)


def _cas_managed_private_json(
    path: Path,
    *,
    expected_sha256: str,
    replacement: dict[str, object],
    label: str,
    expected_uid: int,
) -> str:
    raw, metadata = _managed_private_file(path, label=label, expected_uid=expected_uid)
    try:
        current = json.loads(raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(f"{label} is not readable JSON") from exc
    if current == replacement:
        return "reconciled"
    if hashlib.sha256(raw).hexdigest() != expected_sha256:
        raise SystemExit(f"{label} changed outside the approved reauthorization transaction")
    parent = path.parent
    directory = os.open(
        parent,
        os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
    )
    temporary_name = f".{path.name}.reauthorize-{uuid4().hex}"
    descriptor: int | None = None
    installed = False
    try:
        current_metadata = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        if (current_metadata.st_dev, current_metadata.st_ino) != (metadata.st_dev, metadata.st_ino):
            raise SystemExit(f"{label} changed before replacement")
        descriptor = os.open(
            temporary_name,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
            0o600,
            dir_fd=directory,
        )
        payload = json.dumps(replacement, indent=2, sort_keys=True).encode() + b"\n"
        remaining = memoryview(payload)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("managed private replacement made no progress")
            remaining = remaining[written:]
        os.fchmod(descriptor, 0o600)
        os.fchown(descriptor, metadata.st_uid, metadata.st_gid)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        os.replace(temporary_name, path.name, src_dir_fd=directory, dst_dir_fd=directory)
        installed = True
        os.fsync(directory)
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if not installed:
            try:
                os.unlink(temporary_name, dir_fd=directory)
            except FileNotFoundError:
                pass
        os.close(directory)
    verified, verified_metadata = _managed_private_file(
        path,
        label=label,
        expected_uid=expected_uid,
    )
    if verified_metadata.st_gid != metadata.st_gid or json.loads(verified) != replacement:
        raise SystemExit(f"{label} replacement could not be verified")
    return "updated"


def _replace_managed_private_bytes(
    path: Path,
    *,
    expected: bytes | None,
    replacement: bytes,
    uid: int,
    gid: int,
) -> str:
    if os.path.lexists(path):
        current, _ = _managed_private_file(
            path,
            label="managed private state",
            expected_uid=uid,
        )
        if expected is None or not secrets.compare_digest(current, expected):
            raise SystemExit("managed private state changed before replacement")
        if secrets.compare_digest(current, replacement):
            return "already_current"
    elif expected is not None:
        raise SystemExit("managed private state disappeared before replacement")
    directory = os.open(
        path.parent,
        os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
    )
    temporary = f".{path.name}.replace-{uuid4().hex}"
    descriptor: int | None = None
    try:
        descriptor = os.open(
            temporary,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_EXCL
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
            0o600,
            dir_fd=directory,
        )
        remaining = memoryview(replacement)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("managed private state write made no progress")
            remaining = remaining[written:]
        os.fchown(descriptor, uid, gid)
        os.fchmod(descriptor, 0o600)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        if expected is None and os.path.lexists(path):
            raise SystemExit("managed private state appeared before replacement")
        os.replace(temporary, path.name, src_dir_fd=directory, dst_dir_fd=directory)
        os.fsync(directory)
        return "updated"
    finally:
        if descriptor is not None:
            os.close(descriptor)
        try:
            os.unlink(temporary, dir_fd=directory)
        except FileNotFoundError:
            pass
        os.close(directory)


def _managed_server_reauthorization_provenance(
    *,
    store: Any,
    actor: VerifiedActor,
    key: P256KeyPair,
    core_account: Any,
    c0_account: Any,
    request: ManagedServerCredentialReauthorizationRequestV2 | None = None,
) -> tuple[bytes, bytes | None, Any, tuple[str, int]]:
    terminal_raw, terminal_metadata = _managed_private_file(
        C0_RESPONDER_TERMINAL,
        label="C0 responder terminal evidence",
        expected_uid=c0_account.pw_uid,
    )
    if terminal_metadata.st_gid != c0_account.pw_gid:
        raise SystemExit("C0 responder terminal evidence group custody is unsafe")
    try:
        terminal = json.loads(terminal_raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("C0 responder terminal evidence is invalid") from exc
    if (
        not isinstance(terminal, dict)
        or set(terminal)
        != {"schema", "status", "domain_id", "harness_id", "credential_id"}
        or terminal.get("schema") != "agentnet.c0-pilot-responder.terminal.v1"
        or terminal.get("status") != "COMPLETED_C0_ROUND_TRIP"
        or terminal.get("domain_id") != actor.domain_id
        or terminal.get("harness_id") != actor.harness_id
    ):
        raise SystemExit("C0 responder terminal evidence conflicts with managed identity")
    terminal_credential = completed_c0_terminal_credential(
        store,
        domain_id=actor.domain_id,
        principal_id=str(actor.principal_id),
        harness_id=str(actor.harness_id),
    )
    if (
        terminal_credential is None
        or terminal_credential[0] != terminal.get("credential_id")
    ):
        raise SystemExit("C0 terminal evidence conflicts with authoritative PostgreSQL state")
    if not os.path.lexists(CREDENTIAL_SUPERSESSION_JOURNAL):
        if (actor.credential_id, actor.credential_epoch) != terminal_credential:
            raise SystemExit("managed replacement credential lacks a supersession journal")
        return terminal_raw, None, None, terminal_credential
    journal_raw, journal_metadata = _managed_private_file(
        CREDENTIAL_SUPERSESSION_JOURNAL,
        label="managed credential supersession journal",
        expected_uid=core_account.pw_uid,
    )
    if journal_metadata.st_gid != core_account.pw_gid:
        raise SystemExit("managed credential supersession journal group custody is unsafe")
    try:
        journal = load_audited_supersession_journal(
            journal_raw,
            store,
            domain_id=actor.domain_id,
            principal_id=str(actor.principal_id),
            harness_id=str(actor.harness_id),
        )
    except GateBlocked as exc:
        raise SystemExit("managed credential supersession journal is invalid") from exc
    if (
        (journal.terminal_credential_id, journal.terminal_credential_epoch)
        != terminal_credential
        or journal.entries[-1].key_id != key.thumbprint
    ):
        raise SystemExit("managed credential supersession journal lineage changed")
    actor_is_current = journal.current_credential == (
        actor.credential_id,
        actor.credential_epoch,
    )
    replay_is_current = (
        request is not None
        and journal.entries[-1].request_id == request.request_id
        and journal.entries[-1].previous_credential_id
        == request.expired_credential_id
        and journal.entries[-1].previous_credential_epoch
        == request.expected_credential_epoch
    )
    if not actor_is_current and not replay_is_current:
        raise SystemExit("managed identity and supersession journal current credential differ")
    return terminal_raw, journal_raw, journal, terminal_credential


@contextmanager
def _managed_server_reauthorization_lock():
    """Serialize recovery with package-owned setup across the full invocation."""

    lock_path = SETUP_ROOT / "setup.lock"
    try:
        descriptor = os.open(
            lock_path,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
            0o600,
        )
    except OSError as exc:
        raise SystemExit("managed-server recovery lock custody is unsafe") from exc
    locked = False
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_nlink != 1
            or metadata.st_uid != 0
            or metadata.st_gid != 0
            or stat.S_IMODE(metadata.st_mode) != 0o600
        ):
            raise SystemExit("managed-server recovery lock custody conflicts")
        if fcntl is None:
            raise SystemExit("managed-server credential recovery requires POSIX file locking")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise SystemExit("another AgentNet setup or recovery operation is active") from exc
        locked = True
        yield
    finally:
        if locked and fcntl is not None:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)


def command_server_agent_reauthorize_expired_credential(args: argparse.Namespace) -> int:
    if os.geteuid() != 0:
        raise SystemExit("managed-server credential reauthorization requires root")
    with _managed_server_reauthorization_lock():
        return _command_server_agent_reauthorize_expired_credential_locked(args)


def _command_server_agent_reauthorize_expired_credential_locked(
    args: argparse.Namespace,
) -> int:
    """Rebind one expired managed-server credential after exact owner approval."""

    if os.geteuid() != 0:
        raise SystemExit("managed-server credential reauthorization requires root")
    config_path = Path(os.path.abspath(args.config))
    identity_path = Path(os.path.abspath(args.identity))
    state_path = Path(os.path.abspath(args.state))
    if (
        config_path != CORE_CONFIG
        or identity_path != SERVER_AGENT_IDENTITY
        or state_path != SETUP_ROOT / "credential-reauthorization.json"
    ):
        raise SystemExit("root managed-server reauthorization requires exact package-owned paths")
    try:
        import pwd as posix_pwd

        core_account = posix_pwd.getpwnam(CORE_USER)
        c0_account = posix_pwd.getpwnam(C0_RESPONDER_USER)
        core_root = CORE_CONFIG.parent.lstat()
        setup_root = SETUP_ROOT.lstat()
    except (KeyError, OSError) as exc:
        raise SystemExit("managed Core service custody is unavailable") from exc
    if (
        CORE_CONFIG.parent.is_symlink()
        or not stat.S_ISDIR(core_root.st_mode)
        or core_root.st_uid != core_account.pw_uid
        or core_root.st_gid != core_account.pw_gid
        or stat.S_IMODE(core_root.st_mode) != 0o700
        or SETUP_ROOT.is_symlink()
        or not stat.S_ISDIR(setup_root.st_mode)
        or setup_root.st_uid != 0
        or setup_root.st_gid != 0
        or stat.S_IMODE(setup_root.st_mode) != 0o700
    ):
        raise SystemExit("managed Core service directory custody is unsafe")
    config_raw, config_metadata = _managed_private_file(
        config_path,
        label="managed server configuration",
        expected_uid=core_account.pw_uid,
    )
    identity_raw, identity_metadata = _managed_private_file(
        identity_path,
        label="managed server identity",
        expected_uid=config_metadata.st_uid,
    )
    if (
        config_metadata.st_gid != core_account.pw_gid
        or identity_metadata.st_gid != core_account.pw_gid
    ):
        raise SystemExit("managed server file group custody is unsafe")
    try:
        config = load_config_json(config_raw.decode())
        identity = json.loads(identity_raw)
    except (UnicodeError, json.JSONDecodeError, ValueError) as exc:
        raise SystemExit("managed-server config or identity is invalid") from exc
    if not isinstance(identity, dict) or set(identity) != {
        "schema", "server_base_url", "audience", "actor", "private_key_path"
    } or identity.get("schema") != "agentnet.identity-profile.v1":
        raise SystemExit("managed server identity does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(identity["actor"])
    except Exception as exc:
        raise SystemExit("managed server identity actor is invalid") from exc
    key_raw, _key_metadata = _managed_private_file(
        Path(str(identity["private_key_path"])),
        label="managed server identity key",
        expected_uid=config_metadata.st_uid,
    )
    if (
        Path(str(identity["private_key_path"])) != SERVER_AGENT_KEY
        or _key_metadata.st_gid != core_account.pw_gid
    ):
        raise SystemExit("managed server identity key group custody is unsafe")
    key = P256KeyPair.from_private_pem(key_raw)
    if (
        config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT
        or actor.principal_id is None
        or actor.harness_id is None
        or actor.credential_id is None
        or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        or actor.domain_id != config.domain_id
        or actor.harness_id != config.enrolled_harness_id
    ):
        raise SystemExit("managed-server identity lineage is not eligible for reauthorization")
    _require_managed_server_reauthorization_topology(config)
    verifier, approval_config = _managed_server_reauthorization_verifier(config)
    if approval_config.approver_principal_id != actor.principal_id:
        raise SystemExit("configured Approval owner does not match the managed-server principal")
    core_environment = _parse_environment_file(CORE_ENV, label="managed Core environment")
    if any(name in os.environ for name in ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE")):
        raise SystemExit("ambient TLS trust overrides are forbidden for managed reauthorization")
    if config.database_url_env is None or config.database_url_env not in core_environment:
        raise SystemExit("managed Core database credential is unavailable")
    broker_name = approval_config.service_credential_env
    if broker_name not in core_environment:
        raise SystemExit("managed Approval broker credential is unavailable")
    database_url = core_environment[config.database_url_env]
    broker_credential = core_environment[broker_name]
    ttl_seconds = config.policies.identity.always_on_credential_ttl_seconds
    now = int(time.time())
    pending: dict[str, object]
    state_preexisting = os.path.lexists(state_path)
    if state_preexisting:
        try:
            pending_value = json.loads(
                helpers._owner_only_file(
                    state_path,
                    label="managed-server reauthorization state",
                )
            )
        except (UnicodeError, json.JSONDecodeError) as exc:
            raise SystemExit("managed-server reauthorization state is invalid") from exc
        if not isinstance(pending_value, dict):
            raise SystemExit("managed-server reauthorization state must be one object")
        pending = pending_value
        expected_state_keys = {
            "schema",
            "config_path",
            "identity_path",
            "request",
            "possession_secret",
            "approval_request_id",
            "transaction_digest",
            "request_expires_at",
        }
        if pending.get("schema") == "agentnet.managed-server-credential-reauthorization-state.v1":
            raise SystemExit(
                "legacy reauthorization state lacks C0 provenance and cannot be resumed; "
                "retain it as evidence and follow the package recovery procedure"
            )
        if (
            set(pending) != expected_state_keys
            or pending.get("schema")
            != "agentnet.managed-server-credential-reauthorization-state.v2"
        ):
            raise SystemExit("managed-server reauthorization state does not match the exact schema")
        if pending["config_path"] != str(config_path) or pending["identity_path"] != str(identity_path):
            raise SystemExit("managed-server reauthorization resume paths changed")
        try:
            request = ManagedServerCredentialReauthorizationRequestV2.model_validate(
                pending["request"]
            )
        except Exception as exc:
            raise SystemExit("managed-server reauthorization request state is invalid") from exc
        store = _open_server_agent_activation_store_as_core_peer(
            config,
            database_url_override=database_url,
            core_account=core_account,
        )
        try:
            (
                c0_terminal_raw,
                prior_supersession_journal_raw,
                _prior_supersession_journal,
                _terminal_credential,
            ) = _managed_server_reauthorization_provenance(
                store=store,
                actor=actor,
                key=key,
                core_account=core_account,
                c0_account=c0_account,
                request=request,
            )
        finally:
            store.close()
        if (
            request.domain_id != actor.domain_id
            or request.principal_id != actor.principal_id
            or request.harness_id != actor.harness_id
            or request.expected_key_id != key.thumbprint
            or request.maximum_new_credential_ttl_seconds != ttl_seconds
            or pending["transaction_digest"] != hashlib.sha256(request.canonical_transaction).hexdigest()
            or not isinstance(pending["possession_secret"], str)
            or len(pending["possession_secret"]) != 43
            or type(pending["request_expires_at"]) is not int
            or request.c0_terminal_sha256
            != hashlib.sha256(c0_terminal_raw).hexdigest()
            or (
                request.prior_supersession_journal_sha256
                != (
                    hashlib.sha256(prior_supersession_journal_raw).hexdigest()
                    if prior_supersession_journal_raw is not None
                    else None
                )
                and not (
                    _prior_supersession_journal is not None
                    and _prior_supersession_journal.entries[-1].request_id
                    == request.request_id
                    and _prior_supersession_journal.entries[-1].prior_journal_sha256
                    == request.prior_supersession_journal_sha256
                )
            )
        ):
            raise SystemExit("managed-server reauthorization state binding changed")
    else:
        if args.replace_terminal_state:
            raise SystemExit("terminal replacement requires existing reauthorization state")
        if config.enrolled_credential_id != actor.credential_id:
            raise SystemExit("managed-server config and identity credential labels differ")
        store = _open_server_agent_activation_store_as_core_peer(
            config,
            database_url_override=database_url,
            core_account=core_account,
        )
        try:
            binding = load_credential_binding(store, actor.credential_id)
            if (
                binding.domain_id != actor.domain_id
                or binding.principal_id != actor.principal_id
                or binding.harness_id != actor.harness_id
                or binding.credential_epoch != actor.credential_epoch
                or binding.harness_credential_epoch != actor.credential_epoch
                or binding.credential_status != "active"
                or binding.harness_status != "active"
                or binding.principal_status != "active"
                or binding.domain_status != "active"
                or binding.binding_assurance != actor.binding_assurance
                or binding.public_key_pem != key.public_pem
                or binding.key_id != key.thumbprint
            ):
                raise SystemExit("managed-server expired credential binding changed")
            if now < binding.expires_at:
                raise SystemExit("managed-server credential is not expired")
            (
                c0_terminal_raw,
                prior_supersession_journal_raw,
                _prior_supersession_journal,
                terminal_credential,
            ) = _managed_server_reauthorization_provenance(
                store=store,
                actor=actor,
                key=key,
                core_account=core_account,
                c0_account=c0_account,
            )
        finally:
            store.close()
        config_sha256 = hashlib.sha256(config_raw).hexdigest()
        identity_sha256 = hashlib.sha256(identity_raw).hexdigest()
        request_id = str(
            uuid5(
                NAMESPACE_URL,
                "agentnet:managed-server-credential-reauthorization-request:"
                f"{actor.domain_id}:{actor.harness_id}:{actor.credential_id}:"
                f"{actor.credential_epoch}:{binding.expires_at}:{config_sha256}:"
                f"{identity_sha256}:{hashlib.sha256(c0_terminal_raw).hexdigest()}:"
                f"{hashlib.sha256(prior_supersession_journal_raw).hexdigest() if prior_supersession_journal_raw is not None else '0' * 64}",
            )
        )
        values = {
            "request_id": request_id,
            "domain_id": actor.domain_id,
            "principal_id": actor.principal_id,
            "harness_id": actor.harness_id,
            "expired_credential_id": actor.credential_id,
            "expected_credential_epoch": actor.credential_epoch,
            "expected_expired_at": binding.expires_at,
            "expected_key_id": key.thumbprint,
            "expected_binding_assurance": actor.binding_assurance,
            "managed_config_sha256": config_sha256,
            "managed_identity_sha256": identity_sha256,
            "c0_terminal_credential_epoch": terminal_credential[1],
            "c0_terminal_sha256": hashlib.sha256(c0_terminal_raw).hexdigest(),
            "prior_supersession_journal_sha256": (
                hashlib.sha256(prior_supersession_journal_raw).hexdigest()
                if prior_supersession_journal_raw is not None
                else None
            ),
            "maximum_new_credential_ttl_seconds": ttl_seconds,
        }
        unsigned = ManagedServerCredentialReauthorizationRequestV2(
            **values,
            old_key_possession_signature="pending",
        )
        request = ManagedServerCredentialReauthorizationRequestV2(
            **values,
            old_key_possession_signature=key.sign(
                MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                unsigned.possession_fields(),
            ),
        )
        possession_secret = secrets.token_urlsafe(32)
        request_expires_at = now + 300
        pending = {
            "schema": "agentnet.managed-server-credential-reauthorization-state.v2",
            "config_path": str(config_path),
            "identity_path": str(identity_path),
            "request": request.model_dump(mode="json", by_alias=True),
            "possession_secret": possession_secret,
            "approval_request_id": None,
            "transaction_digest": hashlib.sha256(request.canonical_transaction).hexdigest(),
            "request_expires_at": request_expires_at,
        }
        helpers._write_private_config(state_path, pending)

    client = _managed_server_reauthorization_client(
        config,
        broker_credential=broker_credential,
    )
    try:
        approval_request_id = pending["approval_request_id"]
        if approval_request_id is None:
            created = client.create_request(
                idempotency_key=f"managed-server-credential-reauthorization:{request.request_id}",
                domain_id=request.domain_id,
                approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
                canonical_transaction=request.canonical_transaction,
                transaction_digest=str(pending["transaction_digest"]),
                possession_hash=hashlib.sha256(str(pending["possession_secret"]).encode()).hexdigest(),
                request_expires_at=int(pending["request_expires_at"]),
            )
            approval_request_id = created["request_id"]
            pending["approval_request_id"] = approval_request_id
            helpers._write_private_config(state_path, pending, force=True)
        status = client.request_status(
            request_id=str(approval_request_id),
            transaction_digest=str(pending["transaction_digest"]),
        )
        if args.replace_terminal_state and status["state"] not in {"rejected", "expired"}:
            raise SystemExit("terminal replacement requires broker-proven rejected or expired state")
        if status["state"] in {"rejected", "expired"}:
            if not args.replace_terminal_state:
                raise SystemExit(
                    "managed-server credential reauthorization is terminal; rerun with --replace-terminal-state"
                )
            if (
                hashlib.sha256(config_raw).hexdigest() != request.managed_config_sha256
                or hashlib.sha256(identity_raw).hexdigest() != request.managed_identity_sha256
            ):
                raise SystemExit("terminal reauthorization state cannot be replaced after managed-file drift")
            replacement_values = request.model_dump(mode="python", by_alias=True)
            replacement_values["request_id"] = str(uuid4())
            replacement_values["old_key_possession_signature"] = "pending"
            replacement_unsigned = ManagedServerCredentialReauthorizationRequestV2.model_validate(
                replacement_values
            )
            replacement_values["old_key_possession_signature"] = key.sign(
                MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                replacement_unsigned.possession_fields(),
            )
            request = ManagedServerCredentialReauthorizationRequestV2.model_validate(
                replacement_values
            )
            possession_secret = secrets.token_urlsafe(32)
            pending = {
                "schema": "agentnet.managed-server-credential-reauthorization-state.v2",
                "config_path": str(config_path),
                "identity_path": str(identity_path),
                "request": request.model_dump(mode="json", by_alias=True),
                "possession_secret": possession_secret,
                "approval_request_id": None,
                "transaction_digest": hashlib.sha256(request.canonical_transaction).hexdigest(),
                "request_expires_at": int(time.time()) + 300,
            }
            helpers._write_private_config(state_path, pending, force=True)
            created = client.create_request(
                idempotency_key=f"managed-server-credential-reauthorization:{request.request_id}",
                domain_id=request.domain_id,
                approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
                canonical_transaction=request.canonical_transaction,
                transaction_digest=str(pending["transaction_digest"]),
                possession_hash=hashlib.sha256(possession_secret.encode()).hexdigest(),
                request_expires_at=int(pending["request_expires_at"]),
            )
            pending["approval_request_id"] = created["request_id"]
            approval_request_id = created["request_id"]
            helpers._write_private_config(state_path, pending, force=True)
            status = client.request_status(
                request_id=str(created["request_id"]),
                transaction_digest=str(pending["transaction_digest"]),
            )
        if status["state"] == "pending":
            print(
                json.dumps(
                    {
                        "schema": "agentnet.managed-server-credential-reauthorization-cli.v1",
                        "status": "waiting_owner_approval",
                        "owner_action": f"Open {approval_config.public_origin}/approval and approve the expired server-agent credential reauthorization, then rerun this exact command.",
                        "authority_granted": False,
                        "service_restart": "not_performed",
                    },
                    indent=2,
                    sort_keys=True,
                )
            )
            return 2
        if status["state"] != "issued":
            raise SystemExit("managed-server credential reauthorization was rejected or expired")
        receipt = client.retrieve_receipt(
            request_id=str(approval_request_id),
            possession_secret=str(pending["possession_secret"]),
            domain_id=request.domain_id,
            approval_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
            transaction_digest=str(pending["transaction_digest"]),
            idempotency_key=f"managed-server-credential-reauthorization-retrieve:{request.request_id}",
        )
    finally:
        client.close()

    store = _open_server_agent_activation_store_as_core_peer(
        config,
        database_url_override=database_url,
        core_account=core_account,
    )
    try:
        (
            c0_terminal_raw,
            current_supersession_journal_raw,
            current_supersession_journal,
            _terminal_credential,
        ) = _managed_server_reauthorization_provenance(
            store=store,
            actor=actor,
            key=key,
            core_account=core_account,
            c0_account=c0_account,
            request=request,
        )
        request_bound_journal_raw = current_supersession_journal_raw
        if (
            current_supersession_journal is not None
            and current_supersession_journal.entries[-1].request_id
            == request.request_id
        ):
            request_bound_journal_raw = (
                canonical_supersession_journal(
                    current_supersession_journal.model_copy(
                        update={
                            "entries": current_supersession_journal.entries[:-1]
                        }
                    )
                )
                if len(current_supersession_journal.entries) > 1
                else None
            )
        incidents = DomainIncidentService(store)
        outage_gate = OutageGate(
            config.policies.outage,
            incident_mode_provider=lambda: incidents.current_mode(config.domain_id),
        )
        result = ManagedServerCredentialReauthorizationService(
            store,
            verifier,
            credential_ttl_seconds=ttl_seconds,
            outage_gate=outage_gate,
        ).reauthorize(
            request=request,
            approval=receipt,
            c0_terminal_raw=c0_terminal_raw,
            c0_supersession_journal_raw=request_bound_journal_raw,
        )
        supersession_journal = append_supersession(
            terminal_raw=c0_terminal_raw,
            existing=current_supersession_journal,
            domain_id=request.domain_id,
            principal_id=request.principal_id,
            terminal_credential_epoch=request.c0_terminal_credential_epoch,
            harness_id=request.harness_id,
            request_id=request.request_id,
            transaction_sha256=hashlib.sha256(request.canonical_transaction).hexdigest(),
            approval_receipt_id=str(receipt["receipt_id"]),
            approval_receipt_sha256=hashlib.sha256(
                canonical_json(dict(receipt))
            ).hexdigest(),
            audit_record_hash=result.audit_record_hash,
            prior_journal_sha256=request.prior_supersession_journal_sha256,
            previous_credential_id=request.expired_credential_id,
            credential_id=result.credential_id,
            previous_credential_epoch=request.expected_credential_epoch,
            credential_epoch=result.credential_epoch,
            key_id=request.expected_key_id,
            not_before=result.not_before,
            expires_at=result.expires_at,
        )
        load_audited_supersession_journal(
            canonical_supersession_journal(supersession_journal),
            store,
            domain_id=request.domain_id,
            principal_id=request.principal_id,
            harness_id=request.harness_id,
        )
        journal_status = _replace_managed_private_bytes(
            CREDENTIAL_SUPERSESSION_JOURNAL,
            expected=current_supersession_journal_raw,
            replacement=canonical_supersession_journal(supersession_journal),
            uid=core_account.pw_uid,
            gid=core_account.pw_gid,
        )
        config_value = config.model_dump(mode="python")
        config_value["enrolled_credential_id"] = result.credential_id
        candidate = ExtensionConfig.model_validate(config_value)
        config_replacement = json.loads(config_raw)
        if not isinstance(config_replacement, dict):
            raise SystemExit("managed-server config must be one object")
        # Preserve every validated field exactly as installed.  In particular,
        # frozenset-backed config fields have no semantic order, so re-dumping
        # the model here would make crash recovery depend on PYTHONHASHSEED.
        config_replacement["enrolled_credential_id"] = candidate.enrolled_credential_id
        updated_actor = actor.model_copy(
            update={
                "credential_id": result.credential_id,
                "credential_epoch": result.credential_epoch,
            }
        )
        identity_value = dict(identity)
        identity_value["actor"] = updated_actor.model_dump(mode="json")
        config_status = _cas_managed_private_json(
            config_path,
            expected_sha256=request.managed_config_sha256,
            replacement=config_replacement,
            label="managed server configuration",
            expected_uid=config_metadata.st_uid,
        )
        identity_status = _cas_managed_private_json(
            identity_path,
            expected_sha256=request.managed_identity_sha256,
            replacement=identity_value,
            label="managed server identity",
            expected_uid=identity_metadata.st_uid,
        )
    finally:
        store.close()
    helpers._remove_private_state(state_path)
    print(
        json.dumps(
            {
                "schema": "agentnet.managed-server-credential-reauthorization-cli.v1",
                "status": "completed",
                "idempotent_database_repeat": result.idempotent_repeat,
                "config": config_status,
                "identity": identity_status,
                "credential_supersession_journal": journal_status,
                "credential_epoch": result.credential_epoch,
                "authority_granted": False,
                "service_restart": "not_performed",
                "next": "rerun the exact package-owned server-agent setup --apply --start command",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_server_agent_activate(args: argparse.Namespace) -> int:
    """Bind an offline server config to one exact enrolled harness credential.

    Activation narrows deployment identity only. It grants no entitlement,
    relationship, data, tool, effect, or additional server capability.
    """

    config_path = Path(args.config)
    config = helpers._load_config(config_path)
    if config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        raise SystemExit("server-agent activation requires always_on_server_agent profile")

    identity_path = Path(args.identity)
    identity, actor, key = helpers._load_identity_profile(identity_path)
    if (
        actor.principal_id is None
        or actor.harness_id is None
        or actor.credential_id is None
        or actor.binding_assurance == "lab"
    ):
        raise SystemExit("server-agent activation requires an exact non-lab human harness identity")
    if actor.domain_id != config.domain_id:
        raise SystemExit("server-agent identity belongs to a different AgentNet domain")
    if helpers._canonical_server_origin(str(identity["server_base_url"])) != config.public_base_url:
        raise SystemExit("server-agent identity names a different AgentNet service origin")
    try:
        identity_audience = canonical_service_audience(str(identity["audience"]))
    except ValidationError as exc:
        raise SystemExit("server-agent identity audience is not canonical") from exc
    if identity_audience != config.effective_service_audience:
        raise SystemExit("server-agent identity names a different AgentNet service audience")

    existing = (config.enrolled_harness_id, config.enrolled_credential_id)
    requested = (actor.harness_id, actor.credential_id)
    if any(existing) and existing != requested:
        raise SystemExit(
            "server-agent configuration is already bound to a different identity; "
            "use the explicit credential rotation/recovery flow"
        )

    candidate = ExtensionConfig.model_validate(
        {
            **config.model_dump(mode="python"),
            "enrolled_harness_id": actor.harness_id,
            "enrolled_credential_id": actor.credential_id,
        }
    )
    duplicate = existing == requested
    store = _open_server_agent_activation_store(candidate)
    try:
        _require_server_agent_activation_binding(
            store,
            config=candidate,
            actor=actor,
            key=key,
        )
        if not duplicate:
            helpers._write_private_config(config_path, candidate.redacted_export(), force=True)
    finally:
        store.close()

    print(
        json.dumps(
            {
                "activated": not duplicate,
                "idempotent_repeat": duplicate,
                "config": str(config_path),
                "domain_id": actor.domain_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
                "authority_granted": False,
                "service_restart": "not_performed",
                "server_agent_capabilities": sorted(
                    capability.value for capability in candidate.server_agent_capabilities
                ),
                "next": [
                    "agentnet serve --config " + str(config_path),
                    "agentnet status --config " + str(config_path) + " --live",
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0

