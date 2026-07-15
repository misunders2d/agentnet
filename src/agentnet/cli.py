"""Command-line entry point for local self-hosting and conformance."""

from __future__ import annotations

import argparse
import asyncio
import base64
import ipaddress
import json
import os
import stat
import subprocess
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.parse import urlsplit
from uuid import uuid4

import uvicorn
import httpx
from a2a.types import AgentCapabilities, AgentCard, Message, Part, Role, SendMessageRequest
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette

from agentnet import __version__
from agentnet.audit.service import AuditService
from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    HumanEntitlement,
    PolicyEngine,
    SignedAuthorityCommand,
)
from agentnet.client import AgentNetClient
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked, ValidationError
from agentnet.http_api import create_app
from agentnet.gateways.a2a import (
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    create_tainted_proposal_handler,
    generate_opaque_route,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding, public_key_thumbprint
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_POP_PURPOSE,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRecord,
    InternalInvitationRequest,
    InternalInvitationService,
)
from agentnet.identity.recovery import CredentialRecoveryRequest
from agentnet.operations.config import (
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.operations.incident import (
    DomainIncidentService,
    IncidentMode,
    IncidentModeChange,
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
    discard_unsealed_sqlite_backup,
    discard_failed_sqlite_restore,
    execute_sqlite_backup_plan,
    execute_sqlite_restore_plan,
    inspect_sqlite_restore_target,
    read_manifest_seal,
    verify_backup_for_restore,
    write_manifest_seal,
)
from agentnet.organization.relationships import RelationshipGovernanceRecord
from agentnet.protocol.models import Classification, Relationship
from agentnet.storage.postgres import PostgreSQLReadiness, PostgreSQLStore
from agentnet.storage.sqlite import SQLiteStore
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.dpop import canonical_service_audience
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.supervisor.demos import (
    content_free_demo_summary,
    run_deterministic_harness_demo,
)
from agentnet.supervisor.live_gate import (
    assert_installed_probe_report,
    installed_probe_report,
    run_live_harness_gate,
)
from agentnet.supervisor.daemon import (
    load_supervisor_config,
    redacted_supervisor_status,
    run_supervisor_daemon,
)


def _load_config(path: Path) -> ExtensionConfig:
    if not path.exists():
        raise SystemExit(f"configuration not found: {path}")
    return load_config_json(path.read_text(encoding="utf-8"))


def _owner_only_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink() or path.stat().st_mode & 0o077:
        raise SystemExit(f"private state directory must be owner-only and not a symlink: {path}")


def _write_owner_only(path: Path, content: bytes, *, force: bool = False) -> None:
    _owner_only_directory(path.parent)
    if os.path.lexists(path):
        if not force:
            raise SystemExit(f"refusing to overwrite {path}; use --force where supported")
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077:
            raise SystemExit(f"existing private state is unsafe: {path}")
        descriptor = os.open(path, os.O_WRONLY | os.O_TRUNC)
    else:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        remaining = memoryview(content)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("private state write made no progress")
            remaining = remaining[written:]
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _write_owner_json(path: Path, value: dict[str, object], *, force: bool = False) -> None:
    _write_owner_only(
        path,
        json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n",
        force=force,
    )


def _write_private_config(path: Path, value: dict[str, object], *, force: bool = False) -> None:
    """Atomically write a 0600 config without following or replacing a raced link.

    Config files commonly live in an ordinary 0755 project directory, unlike
    credential state.  The containing directory is therefore not required to
    be private, but the final directory and destination must be real objects
    owned by this process.  A directory descriptor pins the replacement seam.
    """

    parent = path.parent
    parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if parent.is_symlink() or not parent.is_dir() or parent.stat().st_uid != os.geteuid():
        raise SystemExit(f"configuration directory is unsafe: {parent}")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    directory = os.open(parent, flags)
    temporary_name = f".{path.name}.tmp-{uuid4().hex}"
    descriptor: int | None = None
    installed = False
    try:
        existing: os.stat_result | None
        try:
            existing = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            existing = None
        if existing is not None:
            if not force:
                raise SystemExit(f"refusing to overwrite {path}; use --force")
            if not stat.S_ISREG(existing.st_mode) or existing.st_uid != os.geteuid():
                raise SystemExit(f"existing configuration is unsafe: {path}")
        create_flags = (
            os.O_WRONLY
            | os.O_CREAT
            | os.O_EXCL
            | getattr(os, "O_NOFOLLOW", 0)
        )
        descriptor = os.open(temporary_name, create_flags, 0o600, dir_fd=directory)
        content = json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n"
        remaining = memoryview(content)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("configuration write made no progress")
            remaining = remaining[written:]
        os.fchmod(descriptor, 0o600)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None

        try:
            current = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            current = None
        if existing is None:
            if current is not None:
                raise SystemExit(f"configuration destination raced during creation: {path}")
        elif current is None or (current.st_dev, current.st_ino) != (existing.st_dev, existing.st_ino):
            raise SystemExit(f"configuration destination raced during replacement: {path}")
        os.replace(
            temporary_name,
            path.name,
            src_dir_fd=directory,
            dst_dir_fd=directory,
        )
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


def _read_json_object(path: Path, *, label: str) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(f"{label} is not readable JSON: {path}") from exc
    if not isinstance(value, dict):
        raise SystemExit(f"{label} must be one JSON object: {path}")
    return value


def _owner_only_file(path: Path, *, label: str) -> bytes:
    if not path.is_absolute():
        raise SystemExit(f"{label} must be an owner-only absolute regular file")
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise SystemExit(f"{label} must be an owner-only absolute regular file") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or before.st_size > 65_536
        ):
            raise SystemExit(f"{label} must be an owner-only bounded regular file")
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
            raise SystemExit(f"{label} changed while it was read")
        return bytes(content)
    finally:
        os.close(descriptor)


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


def _canonical_server_origin(value: str) -> str:
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise SystemExit("server URL is invalid") from exc
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise SystemExit("server URL must be one canonical HTTP(S) origin without credentials")
    if parsed.scheme == "http":
        try:
            if not ipaddress.ip_address(parsed.hostname).is_loopback:
                raise SystemExit("plaintext server URL must be an explicit loopback address")
        except ValueError as exc:
            raise SystemExit("plaintext server URL must be an explicit loopback address") from exc
    host = f"[{parsed.hostname.lower()}]" if ":" in parsed.hostname else parsed.hostname.lower()
    default_port = 80 if parsed.scheme == "http" else 443
    rendered = f"{parsed.scheme}://{host}"
    if port not in {None, default_port}:
        rendered += f":{port}"
    if value.rstrip("/") != rendered:
        raise SystemExit("server URL must use canonical origin spelling")
    return rendered


def _public_json_request(
    *,
    server: str,
    method: str,
    path: str,
    body: dict[str, object],
    timeout: float = 10.0,
) -> dict[str, object]:
    origin = _canonical_server_origin(server)
    try:
        response = httpx.request(
            method,
            f"{origin}{path}",
            content=canonical_json(body),
            headers={"Content-Type": "application/json"},
            timeout=timeout,
            follow_redirects=False,
        )
    except httpx.HTTPError as exc:
        raise SystemExit(f"AgentNet server request failed: {type(exc).__name__}") from exc
    if response.status_code < 200 or response.status_code >= 300:
        raise SystemExit(f"AgentNet server rejected the request with HTTP {response.status_code}")
    try:
        value = response.json()
    except ValueError as exc:
        raise SystemExit("AgentNet server returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("AgentNet server returned a non-object response")
    return value


def _load_identity_profile(path: Path) -> tuple[dict[str, object], VerifiedActor, P256KeyPair]:
    value = _read_json_object(path, label="AgentNet identity profile")
    if set(value) != {
        "schema",
        "server_base_url",
        "audience",
        "actor",
        "private_key_path",
    } or value.get("schema") != "agentnet.identity-profile.v1":
        raise SystemExit("AgentNet identity profile does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(value["actor"])
    except Exception as exc:
        raise SystemExit("AgentNet identity profile actor is invalid") from exc
    if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
        raise SystemExit("AgentNet owner commands require a verified human identity profile")
    key_path = Path(str(value["private_key_path"]))
    key = P256KeyPair.from_private_pem(
        _owner_only_file(key_path, label="AgentNet identity private key")
    )
    return value, actor, key


def _load_identity_client(path: Path) -> tuple[AgentNetClient, VerifiedActor, P256KeyPair]:
    value, actor, key = _load_identity_profile(path)
    client = AgentNetClient(
        base_url=_canonical_server_origin(str(value["server_base_url"])),
        key=key,
        domain_id=actor.domain_id,
        harness_id=actor.harness_id or "",
        credential_id=actor.credential_id or "",
        audience=str(value["audience"]),
    )
    return client, actor, key


def _open_server_agent_activation_store(config: ExtensionConfig) -> PostgreSQLStore:
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
        config.resolved_database_url(),
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
    config = _load_config(Path(args.config))
    source = _local_sqlite_path(config).absolute()
    archive = Path(args.archive).absolute()
    manifest_path = Path(args.manifest).absolute()
    seal_path = Path(args.seal).absolute()
    if seal_path.parent == archive.parent:
        raise SystemExit("backup seal must use a separate custody directory")
    audit_key_path = Path(args.audit_private_key).absolute()
    audit_key = P256KeyPair.from_private_pem(
        _owner_only_file(audit_key_path, label="audit checkpoint private key")
    )
    seal_key = P256KeyPair.from_private_pem(
        _owner_only_file(
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
    public_key = _owner_only_file(
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
    config = _load_config(Path(args.config))
    _local_sqlite_path(config)
    verified, public_key = _verified_sqlite_backup_from_args(args, config=config)
    target_path = Path(args.target).absolute()
    _owner_only_directory(target_path.parent)
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
    config = _load_config(Path(args.config))
    _local_sqlite_path(config)
    verified, _public_key = _verified_sqlite_backup_from_args(args, config=config)
    target_path = Path(args.target).absolute()
    _owner_only_directory(target_path.parent)
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
        _write_owner_json(Path(args.output).absolute(), output)
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


def command_init(args: argparse.Namespace) -> int:
    path = Path(args.config)
    data_dir = Path(args.data_dir)
    _owner_only_directory(data_dir.absolute())
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
    _write_private_config(path, config.redacted_export(), force=args.force)
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
    oidc_value = Path(args.oidc_config).read_text(encoding="utf-8")
    try:
        oidc = OIDCEnrollmentConfig.model_validate_json(oidc_value)
    except Exception as exc:
        raise SystemExit("OIDC/independent-approval configuration is invalid") from exc
    domain_id = args.domain or f"network-{uuid4().hex}.agentnet"
    data_dir = Path(args.data_dir)
    _owner_only_directory(data_dir.absolute())
    seal_key_path = (data_dir / "secrets" / "backup-seal.key.pem").absolute()
    seal_key = _provision_owner_only_signing_key(seal_key_path)
    activated_at = int(datetime.now(UTC).replace(microsecond=0).timestamp())
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=domain_id,
        data_dir=data_dir,
        database_url=args.database_url,
        database_url_env=args.database_url_env,
        artifact_backend="postgres-manifest",
        artifact_dir=data_dir / "artifacts",
        public_base_url=args.public_base_url,
        runtime_instance_id=args.runtime_instance_id,
        oidc_enrollment=oidc,
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
    _provision_owner_only_key(data_dir / "secrets" / "artifact.key")
    _write_private_config(path, config.redacted_export(), force=args.force)
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
                    "agentnet join begin --server " + config.public_base_url,
                    "agentnet join complete --identity .agentnet/server-agent-identity.json ...",
                    "agentnet server-agent activate --config "
                    + str(path)
                    + " --identity .agentnet/server-agent-identity.json",
                    "agentnet serve --config " + str(path),
                    "agentnet founder begin --identity <founder-identity.json>",
                    "enroll at least two independent recovery administrators before the initial root expires",
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if bool(local_readiness.get("ready")) else 1


def command_server_agent_activate(args: argparse.Namespace) -> int:
    """Bind an offline server config to one exact enrolled harness credential.

    Activation narrows deployment identity only. It grants no entitlement,
    relationship, data, tool, effect, or additional server capability.
    """

    config_path = Path(args.config)
    config = _load_config(config_path)
    if config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        raise SystemExit("server-agent activation requires always_on_server_agent profile")

    identity_path = Path(args.identity)
    identity, actor, key = _load_identity_profile(identity_path)
    if (
        actor.principal_id is None
        or actor.harness_id is None
        or actor.credential_id is None
        or actor.binding_assurance == "lab"
    ):
        raise SystemExit("server-agent activation requires an exact non-lab human harness identity")
    if actor.domain_id != config.domain_id:
        raise SystemExit("server-agent identity belongs to a different AgentNet domain")
    if _canonical_server_origin(str(identity["server_base_url"])) != config.public_base_url:
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
            _write_private_config(config_path, candidate.redacted_export(), force=True)
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


def command_join_begin(args: argparse.Namespace) -> int:
    """Generate a device key and start the public OIDC enrollment redirect."""

    state_path = Path(args.state)
    key_path = Path(args.private_key).resolve() if args.private_key else state_path.with_suffix(".key.pem").resolve()
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    response = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/enrollment/oidc/begin",
        body={
            "harness_kind": args.harness,
            "harness_name": args.name,
            "public_key_pem": key.public_pem,
        },
    )
    pending = {
        "schema": "agentnet.join-pending.v1",
        "server_base_url": _canonical_server_origin(args.server),
        "private_key_path": str(key_path),
        "public_key_pem": key.public_pem,
        "authorization": response,
    }
    _write_owner_json(state_path, pending)
    print(
        json.dumps(
            {
                "state": str(state_path),
                "authorization_url": response.get("authorization_url"),
                "expires_at": response.get("expires_at"),
                "next": (
                    "complete the OIDC redirect, save its JSON response, obtain exact independent approval, "
                    "then run agentnet join complete"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_join_complete(args: argparse.Namespace) -> int:
    """Prove the candidate key and consume one exact independent enrollment approval."""

    state = _read_json_object(Path(args.state), label="join pending state")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "public_key_pem",
        "authorization",
    } or state.get("schema") != "agentnet.join-pending.v1":
        raise SystemExit("join pending state does not match the exact schema")
    challenge = _read_json_object(Path(args.challenge), label="OIDC callback challenge")
    if set(challenge) != {
        "challenge_id",
        "nonce",
        "expires_at",
        "canonical_transaction_b64",
    }:
        raise SystemExit("OIDC callback challenge does not match the exact schema")
    approval = _read_json_object(Path(args.approval), label="independent enrollment approval")
    try:
        transaction = base64.b64decode(
            str(challenge["canonical_transaction_b64"]).encode("ascii"),
            validate=True,
        )
        decoded = json.loads(transaction)
    except (UnicodeError, ValueError, TypeError) as exc:
        raise SystemExit("OIDC callback transaction is invalid") from exc
    if not isinstance(decoded, dict) or canonical_json(decoded) != transaction:
        raise SystemExit("OIDC callback transaction is not exact canonical JSON")
    key_path = Path(str(state["private_key_path"]))
    key = P256KeyPair.from_private_pem(
        _owner_only_file(key_path, label="join private key")
    )
    if key.public_pem != state["public_key_pem"]:
        raise SystemExit("join private key no longer matches the pending candidate")
    result = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/enrollment/complete",
        body={
            "challenge_id": challenge["challenge_id"],
            "nonce": challenge["nonce"],
            "canonical_transaction_b64": challenge["canonical_transaction_b64"],
            "possession_signature": key.sign("agentnet.enrollment.pop.v1", decoded),
            "independent_approval": approval,
        },
    )
    try:
        actor = VerifiedActor.model_validate(result["actor"])
    except Exception as exc:
        raise SystemExit("enrollment response lacks an exact verified actor") from exc
    if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
        raise SystemExit("enrollment response did not create a human harness")
    identity_path = Path(args.identity)
    _write_owner_json(
        identity_path,
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": str(key_path.resolve()),
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": str(identity_path),
                "principal_id": actor.principal_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
                "next": [
                    "ordinary client devices may now reconnect with this exact identity",
                    "for an always-on config, run agentnet server-agent activate --config agentnet.json --identity "
                    + str(identity_path),
                    "after server-agent activation, start the service and run agentnet founder begin if applicable",
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def _invitation_record(path: Path) -> InternalInvitationRecord:
    value = _read_json_object(path, label="internal invitation")
    if set(value) != {"invitation", "zero_authority_proposal"}:
        raise SystemExit("internal invitation file does not match the exact schema")
    if value["zero_authority_proposal"] is not True:
        raise SystemExit("internal invitation file makes an invalid authority claim")
    try:
        return InternalInvitationRecord.model_validate_json(
            json.dumps(value["invitation"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("internal invitation record is invalid") from exc


def _invitation_candidate_state(path: Path) -> tuple[dict[str, object], InternalInvitationRequest, P256KeyPair]:
    state = _read_json_object(path, label="invitation candidate state")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "request",
    } or state.get("schema") != "agentnet.invitation-candidate.v1":
        raise SystemExit("invitation candidate state does not match the exact schema")
    try:
        request = InternalInvitationRequest.model_validate_json(
            json.dumps(state["request"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("invitation candidate request is invalid") from exc
    key_path = Path(str(state["private_key_path"]))
    if (
        not key_path.is_absolute()
        or key_path.is_symlink()
        or not key_path.is_file()
        or key_path.stat().st_mode & 0o077
    ):
        raise SystemExit("invitation candidate key is not an owner-only absolute file")
    key = P256KeyPair.from_private_pem(key_path.read_bytes())
    if key.thumbprint != request.candidate_key_id or key.public_pem != request.candidate_public_key_pem:
        raise SystemExit("invitation candidate key no longer matches the exact request")
    return state, request, key


def _canonical_invitation_for_candidate(
    record: InternalInvitationRecord,
    request: InternalInvitationRequest,
) -> bytes:
    transaction = record.transaction
    exact_fields = (
        "invitation_id",
        "domain_id",
        "invited_oidc_issuer",
        "invited_oidc_subject",
        "invited_verified_email",
        "candidate_harness_id",
        "candidate_harness_kind",
        "candidate_harness_display_name",
        "candidate_binding_assurance",
        "candidate_key_id",
        "candidate_public_key_pem",
        "requested_capabilities",
        "expires_at",
        "predecessor_invitation_id",
        "reason",
    )
    if any(getattr(transaction, field) != getattr(request, field) for field in exact_fields):
        raise SystemExit("issued invitation does not match the candidate's exact request")
    return canonical_json(transaction.model_dump(mode="json"))


def command_invitation_prepare(args: argparse.Namespace) -> int:
    """Generate candidate-owned key material and a public exact sponsor request."""

    if args.expires_in < 60 or args.expires_in > 2_592_000:
        raise SystemExit("invitation lifetime must be between one minute and thirty days")
    state_path = Path(args.state)
    request_path = Path(args.request)
    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    try:
        request = InternalInvitationRequest(
            invitation_id=args.invitation_id or str(uuid4()),
            domain_id=args.domain,
            invited_oidc_issuer=args.issuer,
            invited_oidc_subject=args.subject,
            invited_verified_email=args.email,
            candidate_harness_id=args.harness_id or str(uuid4()),
            candidate_harness_kind=args.harness,
            candidate_harness_display_name=args.name,
            candidate_binding_assurance=args.binding_assurance,
            candidate_key_id=key.thumbprint,
            candidate_public_key_pem=key.public_pem,
            requested_capabilities=tuple(sorted(set(args.capability or ()))),
            expires_at=datetime.now(UTC).replace(microsecond=0)
            + timedelta(seconds=args.expires_in),
            reason=args.reason,
        )
    except Exception as exc:
        raise SystemExit("internal invitation request is invalid") from exc
    public_request = request.model_dump(mode="json")
    _write_owner_json(request_path, public_request)
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.invitation-candidate.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "request": public_request,
        },
    )
    print(
        json.dumps(
            {
                "candidate_state": str(state_path),
                "sponsor_request": str(request_path),
                "invitation_id": request.invitation_id,
                "next": "send only the sponsor request file to a current authorized human sponsor",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_issue(args: argparse.Namespace) -> int:
    request_value = Path(args.request).read_text(encoding="utf-8")
    try:
        invitation = InternalInvitationRequest.model_validate_json(
            request_value,
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("internal invitation request does not match the exact schema") from exc
    client, actor, _key = _load_identity_client(Path(args.identity))
    if invitation.domain_id != actor.domain_id:
        client.close()
        raise SystemExit("internal invitation request crossed the sponsor's domain")
    try:
        response = client.request(
            "POST",
            "/v1/internal-invitations",
            json_body={"invitation": invitation.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"internal invitation issuance was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict):
        raise SystemExit("internal invitation issuance returned invalid JSON")
    _write_owner_json(Path(args.invitation), value, force=args.force)
    record = _invitation_record(Path(args.invitation))
    print(
        json.dumps(
            {
                "invitation": args.invitation,
                "invitation_id": record.transaction.invitation_id,
                "expires_at": record.transaction.expires_at.isoformat(),
                "positive_entitlements_issued": 0,
                "next": "return the invitation file to the exact candidate for OIDC verification",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_oidc_begin(args: argparse.Namespace) -> int:
    state, request, _key = _invitation_candidate_state(Path(args.state))
    record = _invitation_record(Path(args.invitation))
    canonical = _canonical_invitation_for_candidate(record, request)
    result = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/oidc/begin",
        body={"canonical_invitation_b64": base64.b64encode(canonical).decode("ascii")},
    )
    print(
        json.dumps(
            {
                "authorization": result.get("authorization"),
                "next": (
                    "complete the displayed OIDC redirect, save exact state/code JSON, "
                    "then run agentnet invitation complete"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_complete(args: argparse.Namespace) -> int:
    state, request, key = _invitation_candidate_state(Path(args.state))
    record = _invitation_record(Path(args.invitation))
    canonical = _canonical_invitation_for_candidate(record, request)
    callback = _read_json_object(Path(args.callback), label="invitation OIDC callback")
    if set(callback) != {"state", "code"}:
        raise SystemExit("invitation OIDC callback does not match the exact schema")
    encoded = base64.b64encode(canonical).decode("ascii")
    completed = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/oidc/complete",
        body={
            "canonical_invitation_b64": encoded,
            "state": callback["state"],
            "code": callback["code"],
        },
    )
    if set(completed) != {
        "oidc_transaction_id",
        "oidc_acceptance_token",
        "candidate_possession_fields",
        "expires_at",
    } or not isinstance(completed["candidate_possession_fields"], dict):
        raise SystemExit("invitation OIDC completion does not match the exact schema")
    acceptance = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/accept",
        body={
            "canonical_invitation_b64": encoded,
            "oidc_transaction_id": completed["oidc_transaction_id"],
            "oidc_acceptance_token": completed["oidc_acceptance_token"],
            "candidate_possession_signature": key.sign(
                INTERNAL_INVITATION_POP_PURPOSE,
                completed["candidate_possession_fields"],
            ),
        },
    )
    if set(acceptance) != {"acceptance"} or not isinstance(acceptance["acceptance"], dict):
        raise SystemExit("invitation acceptance response does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(acceptance["acceptance"]["actor"])
    except Exception as exc:
        raise SystemExit("invitation acceptance lacks an exact verified actor") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.domain_id != request.domain_id
        or actor.harness_id != request.candidate_harness_id
    ):
        raise SystemExit("invitation acceptance identity does not match the exact candidate")
    _write_owner_json(
        Path(args.identity),
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": state["private_key_path"],
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": args.identity,
                "acceptance": acceptance["acceptance"],
                "positive_entitlements_issued": 0,
                "next": "reconnect using the new identity; authority must be granted separately",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_revoke(args: argparse.Namespace) -> int:
    record = _invitation_record(Path(args.invitation))
    resource, mutation = InternalInvitationService.revocation_binding(
        record.transaction.invitation_id,
        expected_revision=record.revision,
        reason=args.reason,
    )
    client, actor, key = _load_identity_client(Path(args.identity))
    command = _authority_command(
        actor=actor,
        key=key,
        action=INTERNAL_INVITATION_REVOKE_ACTION,
        resource=resource,
        mutation=mutation,
        expected_policy_revision=record.transaction.policy_revision,
        expected_entity_revision=record.revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            f"/v1/internal-invitations/{record.transaction.invitation_id}/revoke",
            json_body={"command": command.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"internal invitation revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_founder_begin(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/authority-bootstrap/challenges",
            json_body={},
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"founder bootstrap begin was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict):
        raise SystemExit("founder bootstrap response is invalid")
    _write_owner_json(Path(args.challenge), value, force=args.force)
    print(
        json.dumps(
            {
                "challenge": args.challenge,
                "challenge_id": value.get("challenge_id"),
                "expires_at": value.get("expires_at"),
                "next": "obtain independent non-beneficiary approval, then run agentnet founder complete",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_founder_complete(args: argparse.Namespace) -> int:
    challenge = _read_json_object(Path(args.challenge), label="founder challenge")
    if set(challenge) != {
        "candidate_entitlement",
        "canonical_transaction_b64",
        "challenge_id",
        "expires_at",
        "nonce",
    }:
        raise SystemExit("founder challenge does not match the exact schema")
    approval = _read_json_object(Path(args.approval), label="founder independent approval")
    client, actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            f"/v1/authority-bootstrap/challenges/{challenge['challenge_id']}/complete",
            json_body={
                "nonce": challenge["nonce"],
                "canonical_transaction_b64": challenge["canonical_transaction_b64"],
                "independent_approval": approval,
            },
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"founder bootstrap completion was rejected with HTTP {response.status_code}")
    result = response.json()
    print(
        json.dumps(
            {
                "founder_principal_id": actor.principal_id,
                "result": result,
                "recovery_safe": False,
                "required_next": (
                    "invite and enroll at least two independent administrators, then issue their exact "
                    "recovery and revocation entitlements before the displayed root expiry"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_authority_inventory(args: argparse.Namespace) -> int:
    """Show authority derived only from the current signed transport identity."""

    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request("GET", "/v1/authority")
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"authority inventory was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_authority_explain(args: argparse.Namespace) -> int:
    """Explain one denial visible to the current signed transport identity."""

    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request("GET", f"/v1/authority/denials/{args.decision_id}")
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"denial explanation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_relationship_propose(args: argparse.Namespace) -> int:
    """Submit a zero-authority exact relationship proposal."""

    relationship_value = _read_json_object(
        Path(args.relationship),
        label="relationship proposal terms",
    )
    try:
        relationship = Relationship.model_validate_json(
            json.dumps(relationship_value),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal terms do not match the exact schema") from exc
    if args.proposal_expires_in < 60 or args.proposal_expires_in > 604_800:
        raise SystemExit("relationship proposal lifetime must be between one minute and seven days")
    proposal_expires_at = datetime.now(UTC).replace(microsecond=0) + timedelta(
        seconds=args.proposal_expires_in
    )
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/relationships",
            json_body={
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            },
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"relationship proposal was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict) or set(value) != {"proposal"}:
        raise SystemExit("relationship proposal response does not match the exact schema")
    try:
        record = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(value["proposal"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal response is invalid") from exc
    _write_owner_json(Path(args.proposal), value, force=args.force)
    print(
        json.dumps(
            {
                "proposal": args.proposal,
                "relationship_id": record.relationship_id,
                "transaction_digest": record.transaction_digest,
                "lifecycle_state": record.lifecycle_state,
                "authority_active": False,
                "next": (
                    "the exact current subordinate human or guest owner independently approves "
                    "the canonical transaction, then runs agentnet relationship accept"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_relationship_accept(args: argparse.Namespace) -> int:
    """Submit independent subordinate-owner consent for one exact proposal."""

    value = _read_json_object(Path(args.proposal), label="relationship proposal")
    if set(value) != {"proposal"} or not isinstance(value["proposal"], dict):
        raise SystemExit("relationship proposal file does not match the exact schema")
    try:
        proposal = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(value["proposal"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal file is invalid") from exc
    if proposal.lifecycle_state != "proposed" or proposal.activation_basis is not None:
        raise SystemExit("relationship proposal file is not a zero-authority pending proposal")
    approval = _read_json_object(Path(args.approval), label="independent relationship approval")
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            f"/v1/relationships/{proposal.relationship_id}/accept",
            json_body={
                "approval": approval,
                "expected_transaction_digest": proposal.transaction_digest,
                "expected_relationship_revision": proposal.revision,
                "expected_lifecycle_revision": proposal.lifecycle_revision,
            },
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"relationship acceptance was rejected with HTTP {response.status_code}")
    result = response.json()
    if not isinstance(result, dict) or set(result) != {"relationship"}:
        raise SystemExit("relationship acceptance response does not match the exact schema")
    try:
        active = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(result["relationship"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship acceptance response is invalid") from exc
    if active.lifecycle_state != "active":
        raise SystemExit("relationship acceptance did not activate the governance edge")
    print(
        json.dumps(
            {
                "relationship": active.model_dump(mode="json"),
                "authority_effect": "governance_edge_and_custody_only_assignment_scope",
                "data_access_granted": False,
                "semantic_processing_granted": False,
                "tools_granted": False,
                "business_effect_authority_granted": False,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_message_send(args: argparse.Namespace) -> int:
    payload = _read_json_object(Path(args.payload), label="message payload")
    idempotency_key = args.idempotency_key or f"agentnet-cli-{uuid4()}"
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/messages",
            json_body={
                "recipients": list(args.recipient),
                "payload": payload,
                "idempotency_key": idempotency_key,
                "classification": args.classification,
            },
        )
    finally:
        client.close()
    if response.status_code != 202:
        raise SystemExit(f"message send was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def _obligation_request(
    args: argparse.Namespace,
    method: str,
    path: str,
    *,
    json_body: dict[str, object] | None = None,
) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        if json_body is None:
            response = client.request(method, path)
        else:
            response = client.request(method, path, json_body=json_body)
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(
            f"response obligation call was rejected with HTTP {response.status_code}"
        )
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_obligation_list(args: argparse.Namespace) -> int:
    if args.limit < 1 or args.limit > 1000:
        raise SystemExit("obligation limit is outside the supported range")
    query = f"?role={args.role}&limit={args.limit}"
    for state in args.state or ():
        query += f"&state={state}"
    return _obligation_request(args, "GET", f"/v1/response-obligations{query}")


def command_obligation_show(args: argparse.Namespace) -> int:
    return _obligation_request(
        args, "GET", f"/v1/response-obligations/{args.obligation_id}"
    )


def command_obligation_inbox(args: argparse.Namespace) -> int:
    return _obligation_request(args, "GET", "/v1/response-obligations/inbox")


def command_obligation_transition(args: argparse.Namespace) -> int:
    body: dict[str, object] = {"to_state": args.to_state, "reason": args.reason}
    if args.expected_revision is not None:
        body["expected_revision"] = args.expected_revision
    return _obligation_request(
        args,
        "POST",
        f"/v1/response-obligations/{args.obligation_id}/transition",
        json_body=body,
    )


def command_obligation_cancel(args: argparse.Namespace) -> int:
    body: dict[str, object] = {"reason_code": args.reason_code}
    if args.expected_revision is not None:
        body["expected_revision"] = args.expected_revision
    return _obligation_request(
        args,
        "POST",
        f"/v1/response-obligations/{args.obligation_id}/cancel",
        json_body=body,
    )


def command_obligation_reconcile(args: argparse.Namespace) -> int:
    if args.limit < 1 or args.limit > 1000:
        raise SystemExit("obligation reconcile limit is outside the supported range")
    return _obligation_request(
        args,
        "POST",
        "/v1/response-obligations/reconcile",
        json_body={"limit": args.limit},
    )


def command_message_inbox(args: argparse.Namespace) -> int:
    if args.after < 0 or args.limit < 1 or args.limit > 1000:
        raise SystemExit("mailbox cursor or limit is outside the supported range")
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "GET",
            f"/v1/mailbox?after={args.after}&limit={args.limit}",
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"mailbox read was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def _authority_command(
    *,
    actor: VerifiedActor,
    key: P256KeyPair,
    action: str,
    resource: str,
    mutation: dict[str, object],
    expected_policy_revision: int,
    expected_entity_revision: int,
    reason: str,
) -> SignedAuthorityCommand:
    issued_at = datetime.now(UTC).replace(microsecond=0)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=canonical_digest(mutation),
        expected_policy_revision=expected_policy_revision,
        expected_entity_revision=expected_entity_revision,
        reason=reason,
        issued_at=issued_at,
        expires_at=issued_at + timedelta(minutes=5),
    )
    return SignedAuthorityCommand(
        **fields,
        signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
    )


def command_admin_entitlement_issue(args: argparse.Namespace) -> int:
    if args.expires_in < 300 or args.expires_in > 31_536_000:
        raise SystemExit("entitlement lifetime must be between five minutes and one year")
    client, actor, key = _load_identity_client(Path(args.identity))
    beneficiary_client, beneficiary, _beneficiary_key = _load_identity_client(
        Path(args.beneficiary_identity)
    )
    beneficiary_client.close()
    if beneficiary.domain_id != actor.domain_id or beneficiary.principal_id is None:
        client.close()
        raise SystemExit("beneficiary identity is not a human in the issuer's exact domain")
    entitlement = HumanEntitlement(
        entitlement_id=args.entitlement_id or str(uuid4()),
        domain_id=actor.domain_id,
        principal_id=beneficiary.principal_id,
        action=args.action,
        resource_pattern=args.resource,
        revision=args.revision,
        expires_at=datetime.now(UTC).replace(microsecond=0)
        + timedelta(seconds=args.expires_in),
    )
    resource, mutation = PolicyEngine.entitlement_issuance_binding(
        entitlement,
        reason=args.reason,
    )
    command = _authority_command(
        actor=actor,
        key=key,
        action="authorization.entitlement.issue",
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=0,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            "/v1/admin/entitlements",
            json_body={
                "entitlement": entitlement.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"entitlement issuance was rejected with HTTP {response.status_code}")
    print(
        json.dumps(
            {
                "entitlement": response.json(),
                "beneficiary_principal_id": beneficiary.principal_id,
                "authority_is_human_only": True,
                "next": (
                    "retain at least two independently operated recovery approvers; "
                    "issue only the exact approval actions and scopes they require"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_admin_entitlement_revoke(args: argparse.Namespace) -> int:
    client, actor, key = _load_identity_client(Path(args.identity))
    resource, mutation = PolicyEngine.entitlement_revocation_binding(
        args.entitlement_id,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    command = _authority_command(
        actor=actor,
        key=key,
        action="authorization.entitlement.revoke",
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            f"/v1/admin/entitlements/{args.entitlement_id}/revoke",
            json_body={"command": command.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"entitlement revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_admin_harness_revoke_prepare(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/admin/harness-revocations/prepare",
            json_body={"harness_id": args.harness_id, "reason": args.reason},
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"harness revocation preparation was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict):
        raise SystemExit("harness revocation preparation returned invalid JSON")
    _write_owner_json(Path(args.request), value, force=args.force)
    print(
        json.dumps(
            {
                "request": args.request,
                "transaction": value,
                "next": "obtain exact independent identity.harness.revoke.approve receipt, then commit",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_admin_harness_revoke_commit(args: argparse.Namespace) -> int:
    request_value = _read_json_object(Path(args.request), label="harness revocation request")
    approval = _read_json_object(Path(args.approval), label="independent harness revocation approval")
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/admin/harness-revocations/commit",
            json_body={"request": request_value, "independent_approval": approval},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"harness revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_recovery_begin(args: argparse.Namespace) -> int:
    state_path = Path(args.state)
    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    response = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/credential-recovery/oidc/begin",
        body={
            "old_harness_id": args.old_harness_id,
            "new_harness_kind": args.harness,
            "new_harness_name": args.name,
            "new_binding_assurance": args.binding_assurance,
            "new_public_key_pem": key.public_pem,
        },
    )
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.recovery-pending.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "public_key_pem": key.public_pem,
            "authorization": response,
        },
    )
    print(
        json.dumps(
            {
                "state": str(state_path),
                "authorization_url": response.get("authorization_url"),
                "expires_at": response.get("expires_at"),
                "next": "complete OIDC redirect, save callback JSON, collect recovery approvals, then run recovery complete",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_recovery_complete(args: argparse.Namespace) -> int:
    state = _read_json_object(Path(args.state), label="credential recovery pending state")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "public_key_pem",
        "authorization",
    } or state.get("schema") != "agentnet.recovery-pending.v1":
        raise SystemExit("credential recovery pending state does not match the exact schema")
    callback = _read_json_object(Path(args.callback), label="credential recovery OIDC callback")
    if set(callback) != {"recovery_request", "recovery_transaction_id"}:
        raise SystemExit("credential recovery callback does not match the exact schema")
    try:
        recovery_request = CredentialRecoveryRequest.model_validate(
            callback["recovery_request"],
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("credential recovery callback request is invalid") from exc
    transaction_id = callback["recovery_transaction_id"]
    if not isinstance(transaction_id, str) or not 16 <= len(transaction_id) <= 128:
        raise SystemExit("credential recovery callback transaction identifier is invalid")
    key_path = Path(str(state["private_key_path"]))
    if key_path.is_symlink() or not key_path.is_file() or key_path.stat().st_mode & 0o077:
        raise SystemExit("credential recovery private key is not an owner-only real file")
    key = P256KeyPair.from_private_pem(key_path.read_bytes())
    if (
        key.public_pem != state["public_key_pem"]
        or recovery_request.new_public_key_pem != key.public_pem
    ):
        raise SystemExit("credential recovery key no longer matches the exact OIDC transaction")
    approvals = tuple(
        _read_json_object(Path(path), label="independent credential recovery approval")
        for path in args.approval
    )
    response = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/credential-recovery/complete",
        body={
            "recovery_transaction_id": transaction_id,
            "possession_signature": key.sign(
                "agentnet.recovery.pop.v1",
                recovery_request.signed_fields(),
            ),
            "independent_approvals": approvals,
        },
    )
    try:
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=recovery_request.domain_id,
            principal_id=str(response["principal_id"]),
            harness_id=str(response["harness_id"]),
            credential_id=str(response["credential_id"]),
            credential_epoch=int(response["credential_epoch"]),
            binding_assurance=recovery_request.new_binding_assurance,
        )
    except Exception as exc:
        raise SystemExit("credential recovery response lacks an exact verified actor binding") from exc
    identity_path = Path(args.identity)
    _write_owner_json(
        identity_path,
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": str(key_path.resolve()),
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": str(identity_path),
                "recovery": response,
                "old_harness_revoked_atomically": True,
                "next": "reconnect with the new identity; rotate into a normal-lifetime credential as policy requires",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_incident_status(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request("GET", "/v1/operator/incident")
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"incident status was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_incident_set(args: argparse.Namespace) -> int:
    client, actor, key = _load_identity_client(Path(args.identity))
    change = IncidentModeChange(
        domain_id=actor.domain_id,
        expected_revision=args.expected_revision,
        target_mode=IncidentMode(args.mode),
        reason=args.reason,
    )
    resource, mutation = DomainIncidentService.authority_binding(change)
    command = _authority_command(
        actor=actor,
        key=key,
        action=DomainIncidentService.ACTION,
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            "/v1/operator/incident",
            json_body={
                "change": change.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"incident transition was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_serve(args: argparse.Namespace) -> int:
    config = _load_config(Path(args.config))
    _require_safe_serve_binding(config, host=args.host, port=args.port)
    enrollment_bootstrap = bool(
        config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT
        and config.oidc_enrollment is not None
        and (not config.enrolled_harness_id or not config.enrolled_credential_id)
    )
    core = CommunicationCore.open(
        config,
        validate_deployment_identity=not enrollment_bootstrap,
    )
    try:
        core.bootstrap_domain()
        uvicorn.run(create_app(core), host=args.host, port=args.port, log_level=args.log_level)
    finally:
        core.close()
    return 0


def command_supervisor_run(args: argparse.Namespace) -> int:
    """Validate or run one persistent ordinary-harness supervisor."""

    try:
        config = load_supervisor_config(Path(args.config))
    except ValidationError as exc:
        raise SystemExit(str(exc)) from None
    if args.check:
        print(json.dumps(redacted_supervisor_status(config), indent=2, sort_keys=True))
        return 0
    print(json.dumps(run_supervisor_daemon(config), indent=2, sort_keys=True))
    return 0


def _require_safe_serve_binding(config: ExtensionConfig, *, host: str, port: int) -> None:
    """The built-in Uvicorn command is plaintext; expose it only on loopback."""

    try:
        bind_address = ipaddress.ip_address(host)
    except ValueError as exc:
        raise GateBlocked(
            "remote_plaintext_bind",
            "agentnet serve requires an explicit loopback bind behind any remote HTTPS terminator",
        ) from exc
    if not bind_address.is_loopback:
        raise GateBlocked(
            "remote_plaintext_bind",
            "agentnet serve refuses a remotely reachable plaintext bind; use a loopback reverse-proxy upstream",
        )
    if config.service_scheme == "http":
        origin = urlsplit(config.public_base_url)
        try:
            origin_address = ipaddress.ip_address(origin.hostname or "")
        except ValueError as exc:
            raise GateBlocked("loopback_origin", "HTTP service origin must be a literal loopback address") from exc
        origin_port = origin.port or 80
        if bind_address != origin_address or port != origin_port:
            raise GateBlocked(
                "loopback_origin",
                "HTTP loopback bind must exactly match the configured public_base_url authority",
            )


def command_status(args: argparse.Namespace) -> int:
    if type(args.timeout) is not float or not 0.1 <= args.timeout <= 10.0:
        raise SystemExit("status --timeout must be between 0.1 and 10 seconds")
    config = _load_config(Path(args.config))
    if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        local_readiness = PostgreSQLReadiness(config.resolved_database_url()).probe()
        local_readiness.update(
            {
                "profile": config.profile.value,
                "instance_id": config.runtime_instance_id,
                "artifact_root_present": config.artifact_dir.is_dir(),
                "mutating_runtime_lease_acquired": False,
            }
        )
    else:
        core = CommunicationCore.open(config)
        try:
            local_readiness = core.readiness()
        finally:
            core.close()

    live_connectivity: dict[str, object]
    if args.local_only:
        live_connectivity = {
            "checked": False,
            "reachable": False,
            "ready": False,
            "reason": "live probe disabled by --local-only",
        }
    else:
        try:
            with httpx.Client(
                base_url=config.public_base_url,
                timeout=args.timeout,
                follow_redirects=False,
            ) as client:
                health = client.get("/healthz")
                readiness = client.get("/readyz")
            live_connectivity = {
                "checked": True,
                "reachable": True,
                "ready": health.status_code == 200 and readiness.status_code == 200,
                "health_status": health.status_code,
                "readiness_status": readiness.status_code,
            }
        except (httpx.HTTPError, OSError) as exc:
            live_connectivity = {
                "checked": True,
                "reachable": False,
                "ready": False,
                "error_type": type(exc).__name__,
            }

    local_ready = bool(local_readiness.get("ready"))
    ready = local_ready and (
        bool(live_connectivity["ready"]) if not args.local_only else True
    )
    print(
        json.dumps(
            {
                "ready": ready,
                "local_readiness": local_readiness,
                "live_connectivity": live_connectivity,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if ready else 1


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
            _owner_only_file(path, label="existing backup seal private key")
        )
    key = P256KeyPair.generate()
    _write_owner_only(path, key.private_pem)
    return key


def command_bootstrap_server_agent(args: argparse.Namespace) -> int:
    """Provision shared software keys, migrate PostgreSQL, and verify recovery.

    This is a self-hosted runtime bootstrap, not KMS/HA/restore evidence and
    does not enroll or grant authority to any agent.
    """

    config = _load_config(Path(args.config))
    if config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        raise SystemExit("bootstrap-server-agent requires always_on_server_agent profile")
    secrets_dir = config.data_dir / "secrets"
    _provision_owner_only_key(secrets_dir / "records.key")
    _provision_owner_only_key(secrets_dir / "artifact.key")
    core = CommunicationCore.open(config, validate_deployment_identity=False)
    try:
        domain = core.bootstrap_domain()
        recovery = core.recovery_status(record_observation=True)
        storage = core.store.readiness()
        audit = core.audit.verify()
        print(
            json.dumps(
                {
                    "domain": domain,
                    "recovery": recovery,
                    "storage": storage,
                    "audit": audit,
                    "deployment_binding": core.server_agent_binding_status(),
                    "warning": "software-key/single-PostgreSQL bootstrap; no HA, mTLS, KMS, or restore claim",
                },
                indent=2,
                sort_keys=True,
            )
        )
        return 0 if recovery["ready"] and storage["ready"] and audit["valid"] else 1
    finally:
        core.close()


def command_demo(args: argparse.Namespace) -> int:
    root = Path(args.data_dir)
    config = ExtensionConfig(
        domain_id="demo.example",
        data_dir=root,
        database_url=f"sqlite:///{root / 'core.sqlite3'}",
        artifact_dir=root / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
        sender, _sender_key = core.bootstrap_synthetic_identity(harness_kind="codex", display_name="demo-sender")
        recipient, _recipient_key = core.bootstrap_synthetic_identity(harness_kind="pi", display_name="demo-recipient")
        accepted = core.send_synthetic_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"synthetic": True, "text": "synthetic local conformance message"},
            idempotency_key=f"demo-message-{uuid4()}",
        )
        inbox = core.mailboxes.reconcile(recipient.harness_id)
        print(
            json.dumps(
                {
                    "warning": "synthetic local-conformance identity; not production enrollment or durability",
                    "accepted": accepted,
                    "recipient": recipient.harness_id,
                    "inbox": inbox,
                    "readiness": core.readiness(),
                },
                indent=2,
                sort_keys=True,
            )
        )
    finally:
        core.close()
    return 0


def _verification_package_root() -> Path:
    configured = os.environ.get("AGENTNET_PACKAGE_ROOT")
    package_root = (
        Path(configured).expanduser().resolve()
        if configured
        else Path(__file__).resolve().parents[2]
    )
    tests_root = package_root / "tests"
    if not tests_root.is_dir():
        raise SystemExit(
            "AgentNet packaged tests are unavailable; reinstall the complete npm package "
            "or run verification from a source checkout"
        )
    return package_root


def command_verify(args: argparse.Namespace) -> int:
    package_root = _verification_package_root()
    tests_root = package_root / "tests"
    host_specific = (
        tests_root / "adapters/test_installed_live_inference.py",
        tests_root / "adapters/test_subprocess_lifecycle.py",
        tests_root / "components/test_bakeoff_evidence.py",
    )
    return subprocess.call(
        [
            sys.executable,
            "-m",
            "pytest",
            "-q",
            str(tests_root),
            *(f"--ignore={path}" for path in host_specific),
            *args.pytest_args,
        ],
        cwd=package_root,
    )


def command_harness_probe(args: argparse.Namespace) -> int:
    root = Path(args.data_dir)
    if args.harness != "all":
        report = installed_probe_report(root, harnesses=(args.harness,))
        probe = report[args.harness]
        ready = bool(probe.get("matches_pin") and probe.get("resolved_path"))
        result: dict[str, object] = {
            "diagnostic_only": True,
            "harness": args.harness,
            "probe": probe,
            "ready": ready,
            "scope": "single_harness",
        }
        if not ready:
            result["error"] = {
                "code": "installed_harness_mismatch",
                "message": "requested installed harness is absent or version-mismatched",
            }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0 if ready else 1

    report = installed_probe_report(root)
    try:
        assert_installed_probe_report(report)
    except GateBlocked as exc:
        print(
            json.dumps(
                {"ready": False, "error": exc.public_detail(), "harnesses": report},
                indent=2,
                sort_keys=True,
            )
        )
        return 1
    print(json.dumps({"ready": True, "harnesses": report}, indent=2, sort_keys=True))
    return 0


def command_harness_demo(args: argparse.Namespace) -> int:
    result = run_deterministic_harness_demo(
        Path(args.data_dir),
        request_timeout_seconds=args.request_timeout,
    )
    print(json.dumps(content_free_demo_summary(result), indent=2, sort_keys=True))
    return 0


def command_harness_live_gate(args: argparse.Namespace) -> int:
    harnesses = (
        ("claude", "codex", "pi", "antigravity")
        if args.harness == "all"
        else (args.harness,)
    )
    results: dict[str, object] = {}
    for harness in harnesses:
        try:
            results[harness] = run_live_harness_gate(
                harness,
                root=Path(args.data_dir),
                request_timeout_seconds=args.request_timeout,
            )
        except GateBlocked as exc:
            print(
                json.dumps(
                    {
                        "ready": False,
                        "failed_harness": harness,
                        "error": exc.public_detail(),
                        "completed_harnesses": results,
                    },
                    indent=2,
                    sort_keys=True,
                ),
                file=sys.stderr,
            )
            return 1
    print(json.dumps({"ready": True, "harnesses": results}, indent=2, sort_keys=True))
    return 0


def command_a2a_demo(_args: argparse.Namespace) -> int:
    """Exercise the official SDK REST route with an inert external proposal."""

    async def run() -> dict[str, object]:
        route = generate_opaque_route(logical_agent_id="synthetic-public-agent", domain_id="demo.example")
        template = AgentCard(
            name="Synthetic public proposal agent",
            description="Local A2A v1 conformance route",
            version="0.1.5",
            capabilities=AgentCapabilities(streaming=False),
            default_input_modes=["text/plain"],
            default_output_modes=["text/plain"],
        )
        card = build_exported_agent_card(template, route=route, public_base_url="https://agents.example")
        grant = StandingA2AGrant(
            grant_id="synthetic-standing-grant",
            route_token=route.route_token,
            logical_agent_id=route.logical_agent_id,
            allowed_actions=frozenset({"a2a.message.send", "a2a.task.get"}),
            allowed_resources=frozenset({route.logical_agent_id}),
            allowed_output_sinks=frozenset({"inert-proposal"}),
            expires_at=datetime.now(UTC) + timedelta(minutes=5),
        )
        routes = build_starlette_routes(
            request_handler=create_tainted_proposal_handler(card),
            agent_card=card,
            route=route,
            grant_lookup=lambda token: grant if token == route.route_token else None,
            peer_resolver=lambda _request: "synthetic-peer",
            url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
            resolver=lambda _host, _port: ("93.184.216.34",),
            extension_config=ExtensionConfig(
                domain_id="demo.example",
                public_base_url="https://agents.example",
                server_agent_capabilities=frozenset(
                    {
                        ServerAgentCapability.A2A_GATEWAY,
                        ServerAgentCapability.ARTIFACT_STORAGE,
                        ServerAgentCapability.OFFLINE_CUSTODY,
                    }
                ),
            ),
        )
        app = Starlette(routes=routes)
        inbound = SendMessageRequest(
            tenant=route.tenant,
            message=Message(
                message_id="external-message-demo",
                context_id="external-context-demo",
                role=Role.ROLE_USER,
                parts=[Part(text="untrusted content remains tainted")],
            ),
        )
        transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
        async with httpx.AsyncClient(transport=transport, base_url="https://agents.example") as client:
            response = await client.post(
                f"{route.path_prefix}/message:send",
                json=MessageToDict(inbound),
                headers={"A2A-Version": "1.0"},
            )
        return {
            "status_code": response.status_code,
            "route": route.path_prefix,
            "response": response.json(),
            "warning": "synthetic local A2A proposal; no corporate identity, authority, execution, or durability",
        }

    print(json.dumps(asyncio.run(run()), indent=2, sort_keys=True))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agentnet", description="AgentNet")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    commands = parser.add_subparsers(dest="command", required=True)

    network = commands.add_parser("network", help="create and operate one AgentNet namespace")
    network_commands = network.add_subparsers(dest="network_command", required=True)
    network_create = network_commands.add_parser(
        "create",
        help="create a production server-agent domain and migrate its PostgreSQL store",
    )
    network_create.add_argument("--config", default="agentnet.json")
    network_create.add_argument("--data-dir", default=".agentnet/server")
    network_create.add_argument("--domain")
    network_create.add_argument(
        "--database-url",
        default="postgresql://agentnet@127.0.0.1/agentnet",
        help="password-free PostgreSQL DSN; credentials come from --database-url-env",
    )
    network_create.add_argument("--database-url-env", default="AGENTNET_DATABASE_URL")
    network_create.add_argument("--public-base-url", required=True)
    network_create.add_argument("--oidc-config", required=True)
    network_create.add_argument("--runtime-instance-id", default="agentnet-server-1")
    network_create.add_argument("--postgres-recovery-topology", action="store_true")
    network_create.add_argument("--force", action="store_true")
    network_create.set_defaults(func=command_network_create)

    server_agent = commands.add_parser(
        "server-agent",
        help="activate and operate one ordinary enrolled always-on AgentNet process",
    )
    server_agent_commands = server_agent.add_subparsers(
        dest="server_agent_command",
        required=True,
    )
    server_agent_activate = server_agent_commands.add_parser(
        "activate",
        help="bind an offline server config to one exact enrolled identity without granting authority",
    )
    server_agent_activate.add_argument("--config", default="agentnet.json")
    server_agent_activate.add_argument("--identity", default=".agentnet/identity.json")
    server_agent_activate.set_defaults(func=command_server_agent_activate)

    join = commands.add_parser("join", help="enroll this person and device into an AgentNet")
    join_commands = join.add_subparsers(dest="join_command", required=True)
    join_begin = join_commands.add_parser("begin", help="start exact OIDC/device enrollment")
    join_begin.add_argument("--server", required=True)
    join_begin.add_argument("--harness", required=True)
    join_begin.add_argument("--name", required=True)
    join_begin.add_argument("--state", default=".agentnet/join-pending.json")
    join_begin.add_argument("--private-key")
    join_begin.set_defaults(func=command_join_begin)
    join_complete = join_commands.add_parser(
        "complete",
        help="complete OIDC enrollment with exact key possession and independent approval",
    )
    join_complete.add_argument("--state", default=".agentnet/join-pending.json")
    join_complete.add_argument("--challenge", required=True)
    join_complete.add_argument("--approval", required=True)
    join_complete.add_argument("--identity", default=".agentnet/identity.json")
    join_complete.add_argument("--force", action="store_true")
    join_complete.set_defaults(func=command_join_complete)

    invitation = commands.add_parser(
        "invitation",
        help="prepare, sponsor, verify, accept, or revoke one exact internal invitation",
    )
    invitation_commands = invitation.add_subparsers(
        dest="invitation_command",
        required=True,
    )
    invitation_prepare = invitation_commands.add_parser("prepare")
    invitation_prepare.add_argument("--server", required=True)
    invitation_prepare.add_argument("--domain", required=True)
    invitation_prepare.add_argument("--issuer", required=True)
    invitation_prepare.add_argument("--subject", required=True)
    invitation_prepare.add_argument("--email", required=True)
    invitation_prepare.add_argument("--harness", required=True)
    invitation_prepare.add_argument("--harness-id")
    invitation_prepare.add_argument("--name", required=True)
    invitation_prepare.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        required=True,
    )
    invitation_prepare.add_argument("--capability", action="append")
    invitation_prepare.add_argument("--reason", required=True)
    invitation_prepare.add_argument("--expires-in", type=int, default=86_400)
    invitation_prepare.add_argument("--invitation-id")
    invitation_prepare.add_argument("--request", default=".agentnet/invitation-request.json")
    invitation_prepare.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_prepare.add_argument("--private-key")
    invitation_prepare.set_defaults(func=command_invitation_prepare)
    invitation_issue = invitation_commands.add_parser("issue")
    invitation_issue.add_argument("--identity", default=".agentnet/identity.json")
    invitation_issue.add_argument("--request", default=".agentnet/invitation-request.json")
    invitation_issue.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_issue.add_argument("--force", action="store_true")
    invitation_issue.set_defaults(func=command_invitation_issue)
    invitation_begin = invitation_commands.add_parser("oidc-begin")
    invitation_begin.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_begin.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_begin.set_defaults(func=command_invitation_oidc_begin)
    invitation_complete = invitation_commands.add_parser("complete")
    invitation_complete.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_complete.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_complete.add_argument("--callback", required=True)
    invitation_complete.add_argument("--identity", default=".agentnet/identity.json")
    invitation_complete.add_argument("--force", action="store_true")
    invitation_complete.set_defaults(func=command_invitation_complete)
    invitation_revoke = invitation_commands.add_parser("revoke")
    invitation_revoke.add_argument("--identity", default=".agentnet/identity.json")
    invitation_revoke.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_revoke.add_argument("--reason", required=True)
    invitation_revoke.set_defaults(func=command_invitation_revoke)

    founder = commands.add_parser(
        "founder",
        help="perform the independently approved first-positive-authority ceremony",
    )
    founder_commands = founder.add_subparsers(dest="founder_command", required=True)
    founder_begin = founder_commands.add_parser("begin")
    founder_begin.add_argument("--identity", default=".agentnet/identity.json")
    founder_begin.add_argument("--challenge", default=".agentnet/founder-challenge.json")
    founder_begin.add_argument("--force", action="store_true")
    founder_begin.set_defaults(func=command_founder_begin)
    founder_complete = founder_commands.add_parser("complete")
    founder_complete.add_argument("--identity", default=".agentnet/identity.json")
    founder_complete.add_argument("--challenge", default=".agentnet/founder-challenge.json")
    founder_complete.add_argument("--approval", required=True)
    founder_complete.set_defaults(func=command_founder_complete)

    authority = commands.add_parser(
        "authority",
        help="inspect authority bound to the current authenticated identity",
    )
    authority_commands = authority.add_subparsers(dest="authority_command", required=True)
    authority_inventory = authority_commands.add_parser("inventory")
    authority_inventory.add_argument("--identity", default=".agentnet/identity.json")
    authority_inventory.set_defaults(func=command_authority_inventory)
    authority_explain = authority_commands.add_parser("explain")
    authority_explain.add_argument("--identity", default=".agentnet/identity.json")
    authority_explain.add_argument("--decision-id", required=True)
    authority_explain.set_defaults(func=command_authority_explain)

    relationship = commands.add_parser(
        "relationship",
        help="propose and independently accept exact bilateral governance",
    )
    relationship_commands = relationship.add_subparsers(
        dest="relationship_command",
        required=True,
    )
    relationship_propose = relationship_commands.add_parser("propose")
    relationship_propose.add_argument("--identity", default=".agentnet/identity.json")
    relationship_propose.add_argument("--relationship", required=True)
    relationship_propose.add_argument("--proposal", default=".agentnet/relationship-proposal.json")
    relationship_propose.add_argument("--proposal-expires-in", type=int, default=86_400)
    relationship_propose.add_argument("--force", action="store_true")
    relationship_propose.set_defaults(func=command_relationship_propose)
    relationship_accept = relationship_commands.add_parser("accept")
    relationship_accept.add_argument("--identity", default=".agentnet/identity.json")
    relationship_accept.add_argument("--proposal", default=".agentnet/relationship-proposal.json")
    relationship_accept.add_argument("--approval", required=True)
    relationship_accept.set_defaults(func=command_relationship_accept)

    message = commands.add_parser("message", help="send and receive authenticated messages")
    message_commands = message.add_subparsers(dest="message_command", required=True)
    message_send = message_commands.add_parser("send")
    message_send.add_argument("--identity", default=".agentnet/identity.json")
    message_send.add_argument("--recipient", action="append", required=True)
    message_send.add_argument("--payload", required=True)
    message_send.add_argument("--idempotency-key")
    message_send.add_argument(
        "--classification",
        choices=tuple(item.value for item in Classification),
        default=Classification.C1_INTERNAL.value,
    )
    message_send.set_defaults(func=command_message_send)
    message_inbox = message_commands.add_parser("inbox")
    message_inbox.add_argument("--identity", default=".agentnet/identity.json")
    message_inbox.add_argument("--after", type=int, default=0)
    message_inbox.add_argument("--limit", type=int, default=100)
    message_inbox.set_defaults(func=command_message_inbox)

    obligation = commands.add_parser(
        "obligation",
        help="inspect and operate durable response obligations",
    )
    obligation_commands = obligation.add_subparsers(dest="obligation_command", required=True)
    obligation_list = obligation_commands.add_parser("list")
    obligation_list.add_argument("--identity", default=".agentnet/identity.json")
    obligation_list.add_argument(
        "--role", choices=("requester", "responsible", "any"), default="any"
    )
    obligation_list.add_argument("--state", action="append")
    obligation_list.add_argument("--limit", type=int, default=100)
    obligation_list.set_defaults(func=command_obligation_list)
    obligation_show = obligation_commands.add_parser("show")
    obligation_show.add_argument("obligation_id")
    obligation_show.add_argument("--identity", default=".agentnet/identity.json")
    obligation_show.set_defaults(func=command_obligation_show)
    obligation_inbox = obligation_commands.add_parser("inbox")
    obligation_inbox.add_argument("--identity", default=".agentnet/identity.json")
    obligation_inbox.set_defaults(func=command_obligation_inbox)
    obligation_transition = obligation_commands.add_parser("transition")
    obligation_transition.add_argument("obligation_id")
    obligation_transition.add_argument(
        "to_state",
        choices=(
            "recipient_committed",
            "acknowledged",
            "in_progress",
            "pending_human",
            "blocked",
        ),
    )
    obligation_transition.add_argument("--identity", default=".agentnet/identity.json")
    obligation_transition.add_argument("--reason", default="recipient_update")
    obligation_transition.add_argument("--expected-revision", type=int)
    obligation_transition.set_defaults(func=command_obligation_transition)
    obligation_cancel = obligation_commands.add_parser("cancel")
    obligation_cancel.add_argument("obligation_id")
    obligation_cancel.add_argument("--identity", default=".agentnet/identity.json")
    obligation_cancel.add_argument("--reason-code", default="requester_canceled")
    obligation_cancel.add_argument("--expected-revision", type=int)
    obligation_cancel.set_defaults(func=command_obligation_cancel)
    obligation_reconcile = obligation_commands.add_parser("reconcile")
    obligation_reconcile.add_argument("--identity", default=".agentnet/identity.json")
    obligation_reconcile.add_argument("--limit", type=int, default=100)
    obligation_reconcile.set_defaults(func=command_obligation_reconcile)

    admin = commands.add_parser("admin", help="perform signed, revision-fenced human administration")
    admin_commands = admin.add_subparsers(dest="admin_command", required=True)
    entitlement = admin_commands.add_parser("entitlement", help="issue or revoke human authority")
    entitlement_commands = entitlement.add_subparsers(
        dest="entitlement_command",
        required=True,
    )
    entitlement_issue = entitlement_commands.add_parser("issue")
    entitlement_issue.add_argument("--identity", default=".agentnet/identity.json")
    entitlement_issue.add_argument("--beneficiary-identity", required=True)
    entitlement_issue.add_argument("--entitlement-id")
    entitlement_issue.add_argument("--action", required=True)
    entitlement_issue.add_argument("--resource", required=True)
    entitlement_issue.add_argument("--revision", type=int, default=1)
    entitlement_issue.add_argument("--policy-revision", type=int, required=True)
    entitlement_issue.add_argument("--expires-in", type=int, default=86_400)
    entitlement_issue.add_argument("--reason", required=True)
    entitlement_issue.set_defaults(func=command_admin_entitlement_issue)
    entitlement_revoke = entitlement_commands.add_parser("revoke")
    entitlement_revoke.add_argument("--identity", default=".agentnet/identity.json")
    entitlement_revoke.add_argument("--entitlement-id", required=True)
    entitlement_revoke.add_argument("--expected-revision", type=int, required=True)
    entitlement_revoke.add_argument("--policy-revision", type=int, required=True)
    entitlement_revoke.add_argument("--reason", required=True)
    entitlement_revoke.set_defaults(func=command_admin_entitlement_revoke)

    harness_revocation = admin_commands.add_parser(
        "harness-revocation",
        help="prepare or commit an independently approved lost-device revocation",
    )
    harness_revocation_commands = harness_revocation.add_subparsers(
        dest="harness_revocation_command",
        required=True,
    )
    harness_prepare = harness_revocation_commands.add_parser("prepare")
    harness_prepare.add_argument("--identity", default=".agentnet/identity.json")
    harness_prepare.add_argument("--harness-id", required=True)
    harness_prepare.add_argument("--reason", required=True)
    harness_prepare.add_argument("--request", default=".agentnet/harness-revocation.json")
    harness_prepare.add_argument("--force", action="store_true")
    harness_prepare.set_defaults(func=command_admin_harness_revoke_prepare)
    harness_commit = harness_revocation_commands.add_parser("commit")
    harness_commit.add_argument("--identity", default=".agentnet/identity.json")
    harness_commit.add_argument("--request", default=".agentnet/harness-revocation.json")
    harness_commit.add_argument("--approval", required=True)
    harness_commit.set_defaults(func=command_admin_harness_revoke_commit)

    recovery = commands.add_parser("recovery", help="recover a lost device through exact OIDC and independent approval")
    recovery_commands = recovery.add_subparsers(dest="recovery_command", required=True)
    recovery_begin = recovery_commands.add_parser("begin")
    recovery_begin.add_argument("--server", required=True)
    recovery_begin.add_argument("--old-harness-id", required=True)
    recovery_begin.add_argument("--harness", required=True)
    recovery_begin.add_argument("--name", required=True)
    recovery_begin.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        required=True,
    )
    recovery_begin.add_argument("--state", default=".agentnet/recovery-pending.json")
    recovery_begin.add_argument("--private-key")
    recovery_begin.set_defaults(func=command_recovery_begin)
    recovery_complete = recovery_commands.add_parser("complete")
    recovery_complete.add_argument("--state", default=".agentnet/recovery-pending.json")
    recovery_complete.add_argument("--callback", required=True)
    recovery_complete.add_argument("--approval", action="append", required=True)
    recovery_complete.add_argument("--identity", default=".agentnet/identity.json")
    recovery_complete.add_argument("--force", action="store_true")
    recovery_complete.set_defaults(func=command_recovery_complete)

    incident = commands.add_parser("incident", help="inspect or change durable domain incident mode")
    incident_commands = incident.add_subparsers(dest="incident_command", required=True)
    incident_status = incident_commands.add_parser("status")
    incident_status.add_argument("--identity", default=".agentnet/identity.json")
    incident_status.set_defaults(func=command_incident_status)
    incident_set = incident_commands.add_parser("set")
    incident_set.add_argument("--identity", default=".agentnet/identity.json")
    incident_set.add_argument("--mode", choices=tuple(mode.value for mode in IncidentMode), required=True)
    incident_set.add_argument("--expected-revision", type=int, required=True)
    incident_set.add_argument("--policy-revision", type=int, required=True)
    incident_set.add_argument("--reason", required=True)
    incident_set.set_defaults(func=command_incident_set)

    backup = commands.add_parser(
        "backup",
        help="create a sealed offline backup without making HA or PITR claims",
    )
    backup_commands = backup.add_subparsers(dest="backup_command", required=True)
    backup_sqlite = backup_commands.add_parser(
        "sqlite",
        help="execute an owner-only offline local-profile backup",
    )
    backup_sqlite.add_argument("--config", default="agentnet.json")
    backup_sqlite.add_argument("--archive", required=True)
    backup_sqlite.add_argument("--manifest", required=True)
    backup_sqlite.add_argument("--seal", required=True)
    backup_sqlite.add_argument("--backup-id", required=True)
    backup_sqlite.add_argument("--audit-private-key", required=True)
    backup_sqlite.add_argument("--seal-private-key", required=True)
    backup_sqlite.add_argument("--application-offline", action="store_true", required=True)
    backup_sqlite.set_defaults(func=command_backup_sqlite)

    restore = commands.add_parser(
        "restore",
        help="restore a separately sealed backup to an absent offline target",
    )
    restore_commands = restore.add_subparsers(dest="restore_command", required=True)
    restore_sqlite = restore_commands.add_parser(
        "sqlite",
        help="execute an exact local-profile restore and verify its authority/audit binding",
    )
    restore_sqlite.add_argument("--config", default="agentnet.json")
    restore_sqlite.add_argument("--archive", required=True)
    restore_sqlite.add_argument("--manifest", required=True)
    restore_sqlite.add_argument("--seal", required=True)
    restore_sqlite.add_argument("--audit-public-key", required=True)
    restore_sqlite.add_argument("--target", required=True)
    restore_sqlite.add_argument("--application-offline", action="store_true", required=True)
    restore_sqlite.set_defaults(func=command_restore_sqlite)

    compromise = commands.add_parser(
        "compromise-rebuild",
        help="plan a fail-closed rebuild; never resumes service or claims rotations complete",
    )
    compromise_commands = compromise.add_subparsers(
        dest="compromise_rebuild_command",
        required=True,
    )
    compromise_plan = compromise_commands.add_parser("plan")
    compromise_plan.add_argument("--config", default="agentnet.json")
    compromise_plan.add_argument("--archive", required=True)
    compromise_plan.add_argument("--manifest", required=True)
    compromise_plan.add_argument("--seal", required=True)
    compromise_plan.add_argument("--audit-public-key", required=True)
    compromise_plan.add_argument("--target", required=True)
    compromise_plan.add_argument("--output")
    compromise_plan.add_argument("--application-offline", action="store_true", required=True)
    compromise_plan.set_defaults(func=command_compromise_rebuild_plan)

    init = commands.add_parser("init", help="initialize a local self-hosted conformance profile")
    init.add_argument("--config", default="agentnet.json")
    init.add_argument("--data-dir", default=".agentnet")
    init.add_argument("--domain", default="local.example")
    init.add_argument("--public-base-url", default="http://127.0.0.1:8080")
    init.add_argument("--force", action="store_true")
    init.set_defaults(func=command_init)

    serve = commands.add_parser("serve", help="run the self-hosted HTTP service")
    serve.add_argument("--config", default="agentnet.json")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8080)
    serve.add_argument("--log-level", default="info")
    serve.set_defaults(func=command_serve)

    supervisor_run = commands.add_parser(
        "supervisor-run",
        help="run an enrolled laptop/server harness supervisor until terminated",
    )
    supervisor_run.add_argument("--config", default="agentnet-supervisor.json")
    supervisor_run.add_argument(
        "--check",
        action="store_true",
        help="validate the owner-only configuration and print only non-secret fields",
    )
    supervisor_run.set_defaults(func=command_supervisor_run)

    status = commands.add_parser("status", help="show operational readiness without acquiring a runtime lease")
    status.add_argument("--config", default="agentnet.json")
    status.add_argument(
        "--local-only",
        action="store_true",
        help="inspect configuration/storage readiness without claiming the HTTP process is live",
    )
    status.add_argument("--timeout", type=float, default=2.0)
    status.set_defaults(func=command_status)

    bootstrap = commands.add_parser(
        "bootstrap-server-agent",
        help="provision and verify an always-on server-agent runtime without granting agent authority",
    )
    bootstrap.add_argument("--config", default="agentnet.json")
    bootstrap.set_defaults(func=command_bootstrap_server_agent)

    demo = commands.add_parser("demo", help="run an end-to-end synthetic local flow")
    demo.add_argument("--data-dir", default="/tmp/agentnet-demo")
    demo.set_defaults(func=command_demo)

    verify = commands.add_parser("verify", help="run the hermetic conformance tests")
    verify.add_argument("pytest_args", nargs="*")
    verify.set_defaults(func=command_verify)

    harness_probe = commands.add_parser(
        "harness-probe",
        help="probe exact installed harness versions without inference",
    )
    harness_probe.add_argument(
        "--harness",
        choices=("all", "claude", "codex", "pi", "antigravity"),
        default="all",
        help="use one diagnostic probe or the default four-harness G01 gate",
    )
    harness_probe.add_argument("--data-dir", default="/tmp/agentnet-harness-probes")
    harness_probe.set_defaults(func=command_harness_probe)

    harness_demo = commands.add_parser(
        "harness-demo",
        help="run the four installed deterministic background lifecycles without inference",
    )
    harness_demo.add_argument("--data-dir", default="/tmp/agentnet-harness-demo")
    harness_demo.add_argument("--request-timeout", type=float, default=5.0)
    harness_demo.set_defaults(func=command_harness_demo)

    harness_live = commands.add_parser(
        "harness-live-gate",
        help="run explicitly credentialed signed clean-worker inference evidence",
    )
    harness_live.add_argument(
        "--harness",
        choices=("all", "claude", "codex", "pi", "antigravity"),
        default="all",
    )
    harness_live.add_argument("--data-dir", default="/tmp/agentnet-harness-live")
    harness_live.add_argument("--request-timeout", type=float, default=60.0)
    harness_live.set_defaults(func=command_harness_live_gate)

    a2a_demo = commands.add_parser("a2a-demo", help="exercise the strict official-SDK A2A proposal route")
    a2a_demo.set_defaults(func=command_a2a_demo)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return int(args.func(args))
