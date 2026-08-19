"""Command-line entry point for local self-hosting and conformance."""

from __future__ import annotations

import argparse
import asyncio
import base64
import getpass
import html
import hashlib
import ipaddress
import json
import socket
import os
import secrets
import signal
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.parse import urlencode, urlsplit
from typing import Any
from uuid import NAMESPACE_URL, UUID, uuid4, uuid5

try:
    import fcntl
except ModuleNotFoundError:  # pragma: no cover - exercised on Windows CI
    fcntl = None  # type: ignore[assignment]

import uvicorn
import httpx
from a2a.types import AgentCapabilities, AgentCard, Message, Part, Role, SendMessageRequest
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette

from agentnet import __version__
from agentnet._terminal_handoff import (
    TerminalHandoffError,
    handoff_private_url,
    require_private_terminal,
)
from agentnet.approval.cli_commands import configure_approval_parser
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.audit.service import AuditService
from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    HumanEntitlement,
    PolicyEngine,
    SignedAuthorityCommand,
)
from agentnet.authorization.bootstrap_plan import (
    BootstrapPlanBeginResult,
    BootstrapPlanCompleteResult,
    BootstrapPlanStatusResult,
)
from agentnet.authorization.communication_scope import (
    CommunicationScopeBeginRequest,
    CommunicationScopeBeginResult,
    CommunicationScopeCompleteRequest,
    CommunicationScopeCompleteResult,
    CommunicationScopeStatusRequest,
    CommunicationScopeStatusResult,
)
from agentnet.authorization.c0_pilot import C0PilotResult
from agentnet.bindings.remote_manager import (
    resolve_packaged_manager_extension,
    run_manager_gateway,
    validate_manager_command,
)
from agentnet.client import MAX_ARTIFACT_BYTES, AgentNetClient
from agentnet.console.http import create_console_app
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked, ValidationError
from agentnet.http_api import create_app
from agentnet.host import host_platform
from agentnet.gateways.a2a import (
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    create_tainted_proposal_handler,
    generate_opaque_route,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import (
    LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    LaptopCredentialReauthorizationRequest,
    ManagedServerCredentialReauthorizationRequestV2,
    ManagedServerCredentialReauthorizationService,
    load_credential_binding,
    public_key_thumbprint,
)
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_POP_PURPOSE,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRecord,
    InternalInvitationRequest,
    InternalInvitationService,
)
from agentnet.identity.recovery import CredentialRecoveryRequest
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
from agentnet.operations.server_reset import ServerSetupResetError, reset_server_setup
from agentnet.operations.server_setup import (
    C0_RESPONDER_TERMINAL,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_ENV,
    CORE_USER,
    SERVER_AGENT_IDENTITY,
    CREDENTIAL_SUPERSESSION_JOURNAL,
    SERVER_AGENT_KEY,
    SETUP_ROOT,
    ServerSetupError,
    _parse_environment_file,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
)
from agentnet.operations.client_setup import (
    ClientIdentityProfile,
    ClientSetupContinuationStore,
    ClientSetupCoordinator,
    ClientSetupError,
    ClientSetupResult,
    EnrollmentProgress,
    SetupNextAction,
)
from agentnet.operations.incident import (
    DomainIncidentService,
    IncidentMode,
    IncidentModeChange,
)
from agentnet.operations.outage import OutageGate
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
from agentnet.supervisor.c0_responder import (
    check_c0_responder,
    load_c0_responder_config,
    run_c0_responder,
)
from agentnet.supervisor.daemon import (
    load_supervisor_config,
    redacted_supervisor_status,
    run_supervisor_daemon,
)


from . import helpers
from .helpers import (
    _canonical_server_origin,
    _identity_client_json_call,
    _load_config,
    _load_identity_client,
    _load_identity_profile,
    _owner_only_directory,
    _owner_only_file,
    _prepare_artifact_output,
    _private_state_lock,
    _public_json_request,
    _read_artifact_file,
    _read_json_object,
    _remove_private_state,
    _required_artifact_open_flags,
    _validate_http_json_response,
    _write_artifact_output,
    _write_owner_json,
    _write_owner_only,
    _write_private_config,
)
from agentnet.cli.commands.messaging import (
    _artifact_json_response,
    _obligation_request,
    command_artifact_abort,
    command_artifact_download,
    command_artifact_lifecycle,
    command_artifact_upload,
    command_authority_explain,
    command_authority_inventory,
    command_message_acknowledge,
    command_message_inbox,
    command_message_send,
    command_obligation_cancel,
    command_obligation_inbox,
    command_obligation_list,
    command_obligation_reconcile,
    command_obligation_show,
    command_obligation_transition,
    command_relationship_accept,
    command_relationship_propose,
)
from agentnet.cli.commands.diagnostics import (
    _provision_owner_only_key,
    _provision_owner_only_signing_key,
    _verification_package_root,
    command_a2a_demo,
    command_bootstrap_server_agent,
    command_demo,
    command_harness_demo,
    command_harness_live_gate,
    command_harness_probe,
    command_incident_set,
    command_incident_status,
    command_status,
    command_verify,
)
from agentnet.cli.commands.services import (
    _UnavailableGuidedClientEnrollment,
    _build_client_setup_coordinator,
    _canonical_console_origin,
    _client_setup_identity_profiles,
    _configure_client_setup_arguments,
    _console_json_response,
    _open_console_handoff_page,
    _print_client_setup_result,
    _require_safe_serve_binding,
    _run_client_setup,
    _serve_one_shot_loopback_page,
    command_client_setup,
    command_client_setup_continue,
    command_client_setup_status,
    command_console_open,
    command_console_serve,
    command_manager_run,
    command_serve,
    command_supervisor_run,
)




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



def _detect_guided_harness() -> str:
    supported = {"omp", "pi", "claude", "codex", "antigravity"}
    configured = os.environ.get("AGENTNET_HARNESS_KIND")
    if configured is not None:
        if configured not in supported:
            raise SystemExit("AGENTNET_HARNESS_KIND is not a supported laptop harness")
        return configured

    executable_kinds = {
        "omp": "omp",
        "pi": "pi",
        "claude": "claude",
        "codex": "codex",
        "agy": "antigravity",
        "antigravity": "antigravity",
    }
    try:
        import psutil

        process = psutil.Process(os.getppid())
        for _ in range(12):
            name = process.name().casefold()
            if name.endswith(".exe"):
                name = name[:-4]
            detected = executable_kinds.get(name)
            if detected is not None:
                return detected
            parent = process.parent()
            if parent is None:
                break
            process = parent
    except (OSError, ValueError, psutil.Error):
        pass

    environment_kinds = {
        kind
        for variable, kind in (
            ("OMPCODE", "omp"),
            ("CLAUDECODE", "claude"),
            ("CODEX_HOME", "codex"),
            ("ANTIGRAVITY_HOME", "antigravity"),
        )
        if os.environ.get(variable)
    }
    if len(environment_kinds) == 1:
        return environment_kinds.pop()
    raise SystemExit(
        "current AI harness could not be identified unambiguously; use --harness"
    )


def _guided_join_inputs(
    args: argparse.Namespace,
    *,
    server: str,
    retained_state: dict[str, object] | None,
) -> tuple[str, str, str]:
    retained_domain = retained_state.get("domain_id") if retained_state else None
    retained_harness = retained_state.get("harness_kind") if retained_state else None
    retained_name = retained_state.get("harness_name") if retained_state else None

    domain = args.domain if args.domain is not None else retained_domain
    if domain is None:
        discovery = _public_json_request(
            server=server,
            method="GET",
            path="/v1/enrollment/discovery",
            body={},
        )
        if (
            set(discovery) != {"schema", "domain_id", "profile"}
            or discovery.get("schema") != "agentnet.enrollment.discovery.v1"
            or discovery.get("profile") != "guided_oidc_passkey"
        ):
            raise SystemExit("AgentNet enrollment discovery response is invalid")
        domain = discovery.get("domain_id")
    if (
        not isinstance(domain, str)
        or not 3 <= len(domain) <= 128
        or domain[0] not in "abcdefghijklmnopqrstuvwxyz0123456789"
        or any(character not in "abcdefghijklmnopqrstuvwxyz0123456789.-" for character in domain)
    ):
        raise SystemExit("AgentNet enrollment discovery domain is invalid")

    harness = args.harness if args.harness is not None else retained_harness
    if harness is None:
        harness = _detect_guided_harness()
    if (
        not isinstance(harness, str)
        or not 1 <= len(harness) <= 64
        or any(not (character.isascii() and (character.isalnum() or character in "._-")) for character in harness)
    ):
        raise SystemExit("guided join harness is invalid")

    name = args.name if args.name is not None else retained_name
    if name is None:
        name = socket.gethostname()
    if (
        not isinstance(name, str)
        or not 1 <= len(name) <= 128
        or name != name.strip()
        or any(ord(character) < 0x20 or ord(character) == 0x7F for character in name)
    ):
        raise SystemExit("guided join display name is invalid; use --name")
    return domain, harness, name




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
    _owner_only_directory(data_dir.absolute())
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
                _owner_only_file(
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
        _write_private_config(state_path, pending)

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
            _write_private_config(state_path, pending, force=True)
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
            _write_private_config(state_path, pending, force=True)
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
            _write_private_config(state_path, pending, force=True)
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
    _remove_private_state(state_path)
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


def _guided_join_state(path: Path) -> dict[str, object]:
    try:
        value = json.loads(_owner_only_file(path, label="guided join state"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("guided join state is not readable JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("guided join state must be one JSON object")
    return value


def _validate_guided_authorization(value: object) -> dict[str, object]:
    if not isinstance(value, dict) or set(value) != {
        "transaction_id",
        "authorization_url",
        "state",
        "expires_at",
        "continuation_token",
    }:
        raise SystemExit("guided enrollment authorization response is invalid")
    transaction_id = value.get("transaction_id")
    state = value.get("state")
    continuation = value.get("continuation_token")
    expires_at = value.get("expires_at")
    authorization_url = value.get("authorization_url")
    if (
        not isinstance(transaction_id, str)
        or not 16 <= len(transaction_id) <= 128
        or not isinstance(state, str)
        or not 32 <= len(state) <= 256
        or not isinstance(continuation, str)
        or not 32 <= len(continuation) <= 128
        or type(expires_at) is not int
        or not isinstance(authorization_url, str)
    ):
        raise SystemExit("guided enrollment authorization response is invalid")
    try:
        parsed = urlsplit(authorization_url)
    except ValueError as exc:
        raise SystemExit("guided enrollment authorization URL is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
    ):
        raise SystemExit("guided enrollment authorization URL is invalid")
    return value


def _validate_stable_approval_url(value: object) -> str:
    if not isinstance(value, str):
        raise SystemExit("guided enrollment approval entrypoint is invalid")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise SystemExit("guided enrollment approval entrypoint is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path != "/approval"
        or parsed.query
        or parsed.fragment
        or (port is not None and not 1 <= port <= 65_535)
    ):
        raise SystemExit("guided enrollment approval entrypoint is invalid")
    return value


def _validate_guided_challenge(value: object) -> tuple[dict[str, object], dict[str, object]]:
    if not isinstance(value, dict) or set(value) != {
        "challenge_id",
        "nonce",
        "canonical_transaction_b64",
    }:
        raise SystemExit("guided enrollment challenge is invalid")
    try:
        transaction = base64.b64decode(
            str(value["canonical_transaction_b64"]).encode("ascii"),
            validate=True,
        )
        decoded = json.loads(transaction)
    except (UnicodeError, ValueError, TypeError) as exc:
        raise SystemExit("guided enrollment challenge is invalid") from exc
    if (
        not isinstance(decoded, dict)
        or canonical_json(decoded) != transaction
        or not isinstance(value["challenge_id"], str)
        or not isinstance(value["nonce"], str)
    ):
        raise SystemExit("guided enrollment challenge is invalid")
    return value, decoded


def _guided_identity_result(
    *,
    result: dict[str, object],
    expected_domain_id: str,
    key: P256KeyPair,
) -> VerifiedActor:
    try:
        actor = VerifiedActor.model_validate(result["actor"])
    except Exception as exc:
        raise SystemExit("enrollment response lacks an exact verified actor") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.domain_id != expected_domain_id
        or result.get("key_id") != key.thumbprint
    ):
        raise SystemExit("enrollment response does not match the requested human/device binding")
    return actor


def _guided_success_output(
    *,
    identity_path: Path,
    actor: VerifiedActor,
    idempotent_repeat: bool,
) -> dict[str, object]:
    _ = identity_path, actor
    return {
        "status": "enrolled_identity_only",
        "idempotent_repeat": idempotent_repeat,
        "identity_saved_locally": True,
        "approval_delivery": "automatic_possession_bound_signed_broker",
        "authority_granted": False,
        "first_message_status": "first_message_blocked_explicit_authority_required",
        "next": "continue only with an explicitly approved bounded authority plan",
    }


def _require_private_terminal_or_exit() -> None:
    try:
        require_private_terminal()
    except TerminalHandoffError as exc:
        raise SystemExit(str(exc)) from None


def _handoff_guided_authorization(
    url: str,
    *,
    browser: str,
    purpose: str = "owner OIDC enrollment",
) -> None:
    if browser == "system":
        if not webbrowser.open(url, new=1):
            raise SystemExit(
                "system browser could not be opened; guided join state is retained"
            )
        return
    if browser == "remote":
        return
    if browser != "terminal":
        raise SystemExit("guided join browser mode is invalid")
    try:
        handoff_private_url(
            url,
            purpose=purpose,
            require_ack=True,
        )
    except TerminalHandoffError as exc:
        raise SystemExit(str(exc)) from None


def _poll_guided_authorization(
    *,
    server: str,
    authorization: dict[str, object],
) -> tuple[dict[str, object], object, int]:
    polled = _public_json_request(
        server=server,
        method="POST",
        path="/v1/enrollment/oidc/poll",
        body={
            "transaction_id": authorization["transaction_id"],
            "continuation_token": authorization["continuation_token"],
        },
    )
    status = polled.get("status")
    interval = polled.get("interval_seconds")
    if type(interval) is not int or not 2 <= interval <= 10:
        raise SystemExit("guided enrollment poll response is invalid")
    return polled, status, interval


def command_join_guided(args: argparse.Namespace) -> int:
    """Run resumable browser OIDC and Core-brokered independent approval."""
    started = time.monotonic()
    deadline = started + float(args.timeout)
    _setup_progress("discover", started, "fetch strict server enrollment metadata")

    state_path = Path(os.path.abspath(args.state))
    identity_path = Path(os.path.abspath(args.identity))
    server = _canonical_server_origin(args.server)
    authorization_url_disclosed = False
    approval_url_disclosed = False
    state_exists = os.path.lexists(state_path)
    pending = _guided_join_state(state_path) if state_exists else None
    args.domain, args.harness, args.name = _guided_join_inputs(
        args,
        server=server,
        retained_state=pending,
    )
    _setup_progress("prepare", started, "validate resumable owner-only identity state")
    replace_terminal_state = False
    if state_exists:
        assert pending is not None
        if pending.get("schema") == "agentnet.guided-join-complete.v1":
            if args.replace_terminal_state:
                raise SystemExit("completed guided join state cannot be replaced")
            expected = {
                "schema",
                "server_base_url",
                "domain_id",
                "harness_kind",
                "harness_name",
                "private_key_path",
                "public_key_pem",
                "identity_path",
                "actor",
            }
            if set(pending) != expected:
                raise SystemExit("completed guided join state does not match the exact schema")
            if (
                pending["server_base_url"] != server
                or pending["domain_id"] != args.domain
                or pending["harness_kind"] != args.harness
                or pending["harness_name"] != args.name
                or pending["identity_path"] != str(identity_path)
            ):
                raise SystemExit("guided join resume arguments do not match completed state")
            try:
                actor = VerifiedActor.model_validate(pending["actor"])
            except Exception as exc:
                raise SystemExit("completed guided join actor is invalid") from exc
            key_path = Path(str(pending["private_key_path"]))
            key = P256KeyPair.from_private_pem(
                _owner_only_file(key_path, label="guided join private key")
            )
            if key.public_pem != pending["public_key_pem"]:
                raise SystemExit("completed guided join key does not match state")
            expected_identity = {
                "schema": "agentnet.identity-profile.v1",
                "server_base_url": server,
                "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
                "actor": actor.model_dump(mode="json"),
                "private_key_path": str(key_path),
            }
            if _guided_join_state(identity_path) != expected_identity:
                raise SystemExit("completed guided join identity file does not match state")
            _setup_progress("enroll", started, "validate retained exact identity binding")
            _setup_progress("verify", started, "confirm identity-only terminal state")
            print(
                json.dumps(
                    _guided_success_output(
                        identity_path=identity_path,
                        actor=actor,
                        idempotent_repeat=True,
                    ),
                    indent=2,
                    sort_keys=True,
                )
            )
            return 0
        expected = {
            "schema",
            "server_base_url",
            "domain_id",
            "harness_kind",
            "harness_name",
            "private_key_path",
            "public_key_pem",
            "authorization",
            "challenge",
            "approval_url",
        }
        pending_schema = pending.get("schema")
        if pending_schema == "agentnet.guided-join.v3":
            expected |= {
                "identity_path",
                "browser_mode",
                "begin_idempotency_key",
                "replaced_authorization",
            }
        elif pending_schema == "agentnet.guided-join.v2":
            expected |= {"identity_path", "browser_mode"}
        elif pending_schema != "agentnet.guided-join.v1":
            raise SystemExit("guided join state does not match the exact schema")
        if set(pending) != expected:
            raise SystemExit("guided join state does not match the exact schema")
        if (
            pending["server_base_url"] != server
            or pending["domain_id"] != args.domain
            or pending["harness_kind"] != args.harness
            or pending["harness_name"] != args.name
            or (
                pending_schema in {"agentnet.guided-join.v2", "agentnet.guided-join.v3"}
                and (
                    pending["identity_path"] != str(identity_path)
                    or pending["browser_mode"] != args.browser
                )
            )
        ):
            raise SystemExit("guided join resume arguments do not match pending state")
        authorization = (
            None
            if pending["authorization"] is None
            else _validate_guided_authorization(pending["authorization"])
        )
        if pending_schema != "agentnet.guided-join.v3" and authorization is None:
            raise SystemExit("guided join authorization is missing")
        if pending_schema == "agentnet.guided-join.v3":
            begin_key = pending["begin_idempotency_key"]
            if (
                not isinstance(begin_key, str)
                or len(begin_key) != 43
                or any(character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for character in begin_key)
            ):
                raise SystemExit("guided join begin idempotency state is invalid")
            if authorization is None and (
                pending["challenge"] is not None or pending["approval_url"] is not None
            ):
                raise SystemExit("guided join pre-begin state is inconsistent")
        key_path = Path(str(pending["private_key_path"]))
        key = P256KeyPair.from_private_pem(
            _owner_only_file(key_path, label="guided join private key")
        )
        if key.public_pem != pending["public_key_pem"]:
            raise SystemExit("guided join private key no longer matches pending state")
        if args.replace_terminal_state:
            if pending_schema not in {"agentnet.guided-join.v2", "agentnet.guided-join.v3"}:
                raise SystemExit(
                    "guided join terminal replacement requires argument-bound state"
                )
            if authorization is None:
                raise SystemExit("guided join terminal replacement has no prior authorization")
            _polled, terminal_status, _interval = _poll_guided_authorization(
                server=server,
                authorization=authorization,
            )
            if terminal_status not in {"expired", "failed"}:
                raise SystemExit(
                    "guided join state is not terminal; rerun without --replace-terminal-state"
                )
            replace_terminal_state = True
    elif args.replace_terminal_state:
        raise SystemExit("guided join terminal replacement requires existing pending state")

    begin_required = (
        not state_exists
        or replace_terminal_state
        or (
            state_exists
            and pending.get("schema") == "agentnet.guided-join.v3"
            and pending.get("authorization") is None
        )
    )
    if begin_required:
        if args.browser == "terminal":
            _require_private_terminal_or_exit()
        if not state_exists:
            key_path = (
                Path(os.path.abspath(args.private_key))
                if args.private_key
                else state_path.with_suffix(".key.pem")
            )
            if os.path.lexists(key_path):
                key = P256KeyPair.from_private_pem(
                    _owner_only_file(key_path, label="guided join private key")
                )
            else:
                key = P256KeyPair.generate()
                _write_owner_only(key_path, key.private_pem)
        if not state_exists or replace_terminal_state:
            begin_key = secrets.token_urlsafe(32)
            pending = {
                "schema": "agentnet.guided-join.v3",
                "server_base_url": server,
                "domain_id": args.domain,
                "harness_kind": args.harness,
                "harness_name": args.name,
                "private_key_path": str(key_path),
                "public_key_pem": key.public_pem,
                "identity_path": str(identity_path),
                "browser_mode": args.browser,
                "begin_idempotency_key": begin_key,
                "authorization": None,
                "replaced_authorization": authorization if replace_terminal_state else None,
                "challenge": None,
                "approval_url": None,
            }
            if state_exists:
                _write_private_config(state_path, pending, force=True)
            else:
                _write_owner_json(state_path, pending)
            state_exists = True
        _setup_progress("authenticate", started, "start exact owner OIDC authorization")
        authorization = _validate_guided_authorization(
            _public_json_request(
                server=server,
                method="POST",
                path="/v1/enrollment/oidc/begin",
                body={
                    "idempotency_key": pending["begin_idempotency_key"],
                    "harness_kind": args.harness,
                    "harness_name": args.name,
                    "public_key_pem": key.public_pem,
                    **(
                        {"activation_mode": "remote_browser"}
                        if args.browser == "remote"
                        else {}
                    ),
                },
            )
        )
        pending = {
            **pending,
            "authorization": authorization,
            "replaced_authorization": None,
        }
        _write_private_config(state_path, pending, force=True)
        _setup_progress("approve", started, "request independent owner passkey approval")
        _handoff_guided_authorization(
            str(authorization["authorization_url"]),
            browser=args.browser,
        )
        authorization_url_disclosed = True

    challenge_value = pending.get("challenge")
    while True:
        if time.monotonic() >= deadline:
            raise SystemExit("guided enrollment timed out; pending state is retained")
        polled, status, interval = _poll_guided_authorization(
            server=server,
            authorization=authorization,
        )
        if status in {"approval_pending", "approval_ready"}:
            challenge_value = {
                "challenge_id": polled.get("challenge_id"),
                "nonce": polled.get("nonce"),
                "canonical_transaction_b64": polled.get("canonical_transaction_b64"),
            }
            _validate_guided_challenge(challenge_value)
            pending["challenge"] = challenge_value
            pending["approval_url"] = _validate_stable_approval_url(
                polled.get("approval_url")
            )
            _write_private_config(state_path, pending, force=True)
            if not approval_url_disclosed:
                _handoff_guided_authorization(
                    str(pending["approval_url"]),
                    browser=args.browser,
                    purpose="stable owner approval",
                )
                approval_url_disclosed = True
            if status == "approval_ready":
                break
        elif status not in {"authorization_pending", "slow_down"}:
            raise SystemExit(f"guided enrollment stopped in terminal state: {status}")
        if status == "authorization_pending" and not authorization_url_disclosed:
            if args.browser == "terminal":
                _require_private_terminal_or_exit()
            _handoff_guided_authorization(
                str(authorization["authorization_url"]),
                browser=args.browser,
            )
            authorization_url_disclosed = True
        time.sleep(min(interval, max(0.0, deadline - time.monotonic())))

    _setup_progress("enroll", started, "complete exact human and harness binding")
    _challenge, decoded = _validate_guided_challenge(challenge_value)
    result = _public_json_request(
        server=server,
        method="POST",
        path="/v1/enrollment/oidc/complete",
        body={
            "transaction_id": authorization["transaction_id"],
            "continuation_token": authorization["continuation_token"],
            "possession_signature": key.sign("agentnet.enrollment.pop.v1", decoded),
        },
    )
    actor = _guided_identity_result(
        result=result,
        expected_domain_id=args.domain,
        key=key,
    )
    identity = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": server,
        "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(key_path),
    }
    identity_repeat = False
    if os.path.lexists(identity_path):
        existing = _guided_join_state(identity_path)
        if existing != identity:
            raise SystemExit("existing identity file conflicts with completed enrollment")
        identity_repeat = True
    else:
        _write_owner_json(identity_path, identity)
    _setup_progress("verify", started, "confirm identity-only terminal state")
    completed_state = {
        "schema": "agentnet.guided-join-complete.v1",
        "server_base_url": server,
        "domain_id": args.domain,
        "harness_kind": args.harness,
        "harness_name": args.name,
        "private_key_path": str(key_path),
        "public_key_pem": key.public_pem,
        "identity_path": str(identity_path),
        "actor": actor.model_dump(mode="json"),
    }
    _write_private_config(state_path, completed_state, force=True)
    print(
        json.dumps(
            _guided_success_output(
                identity_path=identity_path,
                actor=actor,
                idempotent_repeat=identity_repeat,
            ),
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
            "idempotency_key": secrets.token_urlsafe(32),
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
                    (
                        "after server-agent activation and exact second guided enrollment, run "
                        "agentnet bootstrap-plan begin from the fresh identity"
                    ),
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def _invitation_record(path: Path) -> InternalInvitationRecord:
    try:
        value = json.loads(
            _owner_only_file(path.absolute(), label="internal invitation")
        )
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("internal invitation is not readable JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("internal invitation must be one JSON object")
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
    try:
        state = json.loads(
            _owner_only_file(path.absolute(), label="invitation candidate state")
        )
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("invitation candidate state is not readable JSON") from exc
    if not isinstance(state, dict):
        raise SystemExit("invitation candidate state must be one JSON object")
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
    try:
        key = P256KeyPair.from_private_pem(
            _owner_only_file(key_path, label="invitation candidate key")
        )
    except Exception as exc:
        raise SystemExit("invitation candidate key is invalid") from exc
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


def command_invitation_join_sponsored(args: argparse.Namespace) -> int:
    """Create candidate-owned proof state, discover one intent, and enter normal acceptance."""
    state_path = Path(args.state).resolve()
    invitation_path = Path(args.invitation).resolve()
    if os.path.lexists(state_path):
        state_bytes = _owner_only_file(state_path, label="sponsored enrollment state")
        try:
            value = json.loads(state_bytes)
        except (UnicodeError, json.JSONDecodeError) as exc:
            raise SystemExit("sponsored enrollment state is not readable JSON") from exc
        if not isinstance(value, dict):
            raise SystemExit("sponsored enrollment state must be one JSON object")
        if value.get("schema") == "agentnet.invitation-candidate.v1":
            if args.callback:
                return command_invitation_complete(
                    argparse.Namespace(
                        state=str(state_path),
                        invitation=str(invitation_path),
                        callback=args.callback,
                        identity=args.identity,
                        force=args.force,
                    )
            )
            return command_invitation_oidc_begin(
                argparse.Namespace(state=str(state_path), invitation=str(invitation_path))
            )
        if (
            set(value)
            != {
                "schema",
                "server_base_url",
                "private_key_path",
                "continuation_token",
            }
            or value.get("schema") != "agentnet.sponsored-enrollment-candidate.v1"
        ):
            raise SystemExit("sponsored enrollment state does not match the exact schema")
        if os.path.lexists(invitation_path):
            raise SystemExit(f"refusing to overwrite {invitation_path}; choose a new invitation path")
        status = _public_json_request(
            server=str(value["server_base_url"]),
            method="POST",
            path="/v1/sponsored-enrollment/candidate/status",
            body={"continuation_token": value["continuation_token"]},
        )
        invitation_value = status.get("invitation")
        if not isinstance(invitation_value, dict):
            print(json.dumps({"state": status.get("state"), "next": "retry after sponsor approval"}, indent=2))
            return 0
        record = InternalInvitationRecord.model_validate(invitation_value)
        transaction = record.transaction
        key_path = Path(str(value["private_key_path"]))
        try:
            candidate_key = P256KeyPair.from_private_pem(
                _owner_only_file(key_path, label="sponsored enrollment candidate key")
            )
        except Exception as exc:
            raise SystemExit("sponsored enrollment candidate key is invalid") from exc
        if (
            candidate_key.thumbprint != transaction.candidate_key_id
            or candidate_key.public_pem != transaction.candidate_public_key_pem
        ):
            raise SystemExit("issued invitation does not match the sponsored candidate key")
        request = InternalInvitationRequest(
            invitation_id=transaction.invitation_id,
            domain_id=transaction.domain_id,
            invited_oidc_issuer=transaction.invited_oidc_issuer,
            invited_oidc_subject=transaction.invited_oidc_subject,
            invited_verified_email=transaction.invited_verified_email,
            candidate_harness_id=transaction.candidate_harness_id,
            candidate_harness_kind=transaction.candidate_harness_kind,
            candidate_harness_display_name=transaction.candidate_harness_display_name,
            candidate_binding_assurance=transaction.candidate_binding_assurance,
            candidate_key_id=transaction.candidate_key_id,
            candidate_public_key_pem=transaction.candidate_public_key_pem,
            requested_capabilities=transaction.requested_capabilities,
            expires_at=transaction.expires_at,
            predecessor_invitation_id=transaction.predecessor_invitation_id,
            reason=transaction.reason,
        )
        invitation_output = {
            "invitation": record.model_dump(mode="json"),
            "zero_authority_proposal": True,
        }
        candidate_state = {
            "schema": "agentnet.invitation-candidate.v1",
            "server_base_url": value["server_base_url"],
            "private_key_path": value["private_key_path"],
            "request": request.model_dump(mode="json"),
        }
        _write_owner_json(invitation_path, invitation_output)
        _owner_only_directory(state_path.parent)
        with _private_state_lock(state_path):
            _write_private_config(
                state_path,
                candidate_state,
                force=True,
                expected_content=state_bytes,
            )
        return command_invitation_oidc_begin(
            argparse.Namespace(state=str(state_path), invitation=str(invitation_path))
        )

    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    result = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/sponsored-enrollment/candidate/begin",
        body={
            "candidate_harness_id": args.harness_id or str(uuid4()),
            "harness_kind": args.harness,
            "harness_name": args.name,
            "binding_assurance": args.binding_assurance,
            "public_key_pem": key.public_pem,
            "idempotency_key": secrets.token_urlsafe(24),
        },
    )
    required = {"authorization_url", "continuation_token"}
    if not required <= result.keys() or any(not isinstance(result[key], str) for key in required):
        raise SystemExit("sponsored enrollment begin response is invalid")
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.sponsored-enrollment-candidate.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "continuation_token": result["continuation_token"],
        },
    )
    print(json.dumps({"authorization_url": result["authorization_url"], "state": str(state_path),
                      "next": "open authorization_url, then rerun this command"}, indent=2))
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


from agentnet.cli.commands.scope import (
    _BOOTSTRAP_PLAN_CLI_STATE_KEYS,
    _BOOTSTRAP_PLAN_CLI_STATE_SCHEMA,
    _COMMUNICATION_SCOPE_CLI_STATE_KEYS,
    _COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
    _load_or_create_scoped_cli_state,
    _load_scoped_cli_state,
    _require_scoped_approval_url,
    _scoped_request_result,
    _validate_scoped_cli_state,
    command_bootstrap_plan_begin,
    command_bootstrap_plan_complete,
    command_bootstrap_plan_status,
    command_communication_scope_begin,
    command_communication_scope_complete,
    command_communication_scope_status,
)




def _c0_pilot_cli_result(response, *, expected_status: int) -> dict[str, str]:
    if response.status_code != expected_status:
        raise SystemExit(f"C0 pilot request was rejected with HTTP {response.status_code}")
    try:
        result = C0PilotResult.model_validate(response.json())
    except Exception as exc:
        raise SystemExit("C0 pilot response is invalid") from exc
    value = result.model_dump(mode="json", by_alias=True)
    if set(value) != {"schema", "status"}:
        raise SystemExit("C0 pilot response is invalid")
    return value


def command_c0_pilot(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        if args.c0_pilot_command == "start":
            response = client.c0_pilot_start()
            expected = 201
        elif args.c0_pilot_command == "complete":
            response = client.c0_pilot_complete()
            expected = 200
        elif args.c0_pilot_command == "status":
            response = client.c0_pilot_status()
            expected = 200
        else:
            raise SystemExit("unknown C0 pilot operation")
    finally:
        client.close()
    print(
        json.dumps(
            _c0_pilot_cli_result(response, expected_status=expected),
            indent=2,
            sort_keys=True,
        )
    )
    return 0


_CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA = "agentnet.credential-renewal-cli-state.v1"


def _credential_renewal_cli_state(path: Path) -> dict[str, str]:
    resolved = path.resolve()
    if not os.path.lexists(resolved):
        value = {
            "schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA,
            "request_id": str(uuid4()),
        }
        _write_private_config(resolved, value, force=False)
        return value
    try:
        value = json.loads(_owner_only_file(resolved, label="credential renewal state"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("credential renewal state is not readable JSON") from exc
    if not isinstance(value, dict) or set(value) != {"schema", "request_id"}:
        raise SystemExit("credential renewal state does not match the exact schema")
    request_id = value.get("request_id")
    try:
        parsed = UUID(str(request_id))
    except (TypeError, ValueError, AttributeError) as exc:
        raise SystemExit("credential renewal state does not match the exact schema") from exc
    if value.get("schema") != _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA or str(parsed) != request_id:
        raise SystemExit("credential renewal state does not match the exact schema")
    return {"schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA, "request_id": str(request_id)}


def command_credential_renew(args: argparse.Namespace) -> int:
    state_path = Path(args.state)
    state = _credential_renewal_cli_state(state_path)
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.renew_current_credential(request_id=state["request_id"])
    finally:
        client.close()
    if response.status_code != 200:
        print(json.dumps({"schema": "agentnet.credential-renewal-cli-result.v1", "status": "blocked"}, indent=2, sort_keys=True))
        return 1
    try:
        result = response.json()
    except Exception as exc:
        raise SystemExit("credential renewal response is invalid") from exc
    if (
        not isinstance(result, dict)
        or set(result) != {"schema", "status", "expires_at"}
        or result.get("schema") != "agentnet.credential-renewal-result.v1"
        or result.get("status") not in {"current", "renewed"}
        or not isinstance(result.get("expires_at"), int)
    ):
        raise SystemExit("credential renewal response is invalid")
    # Rotate only after exact response. If this local write fails, retry retains
    # the old ID and receives the exact stored result without another mutation.
    _write_private_config(
        state_path.resolve(),
        {"schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA, "request_id": str(uuid4())},
        force=True,
    )
    print(json.dumps({"schema": "agentnet.credential-renewal-cli-result.v1", "status": result["status"]}, indent=2, sort_keys=True))
    return 0

_LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA = (
    "agentnet.laptop-credential-reauthorization-cli-state.v1"
)
_LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_KEYS = {
    "schema",
    "identity_path",
    "identity_profile_sha256",
    "private_key_sha256",
    "request_id",
    "possession_secret",
    "transaction",
    "old_key_possession_signature",
    "approval_url_sha256",
    "approval_url_opened",
    "result",
    "successor_identity_sha256",
}
_LAPTOP_CREDENTIAL_REAUTHORIZATION_RESULT_KEYS = {
    "schema",
    "status",
    "request_id",
    "domain_id",
    "principal_id",
    "harness_id",
    "previous_credential_id",
    "credential_id",
    "key_id",
    "credential_epoch",
    "not_before",
    "expires_at",
    "idempotent_repeat",
    "key_preserved",
    "authority_granted",
}


def _private_json_bytes(value: dict[str, object]) -> bytes:
    return json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n"


def _load_laptop_reauthorization_identity(
    path: Path,
) -> tuple[
    Path,
    bytes,
    dict[str, object],
    VerifiedActor,
    Path,
    bytes,
    P256KeyPair,
]:
    identity_path = path.resolve()
    identity_raw = _owner_only_file(
        identity_path,
        label="AgentNet identity profile",
    )
    try:
        identity = json.loads(identity_raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("AgentNet identity profile is not readable JSON") from exc
    if (
        not isinstance(identity, dict)
        or set(identity)
        != {
            "schema",
            "server_base_url",
            "audience",
            "actor",
            "private_key_path",
        }
        or identity.get("schema") != "agentnet.identity-profile.v1"
    ):
        raise SystemExit("AgentNet identity profile does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(identity["actor"])
    except Exception as exc:
        raise SystemExit("AgentNet identity profile actor is invalid") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.principal_id is None
        or actor.harness_id is None
        or actor.credential_id is None
        or actor.binding_assurance not in {"os_bound", "hardware_bound"}
    ):
        raise SystemExit(
            "expired credential reauthorization requires an exact bound laptop identity"
        )
    key_path = Path(str(identity["private_key_path"]))
    if not key_path.is_absolute():
        raise SystemExit("AgentNet identity private key path must be absolute")
    key_raw = _owner_only_file(
        key_path,
        label="AgentNet identity private key",
    )
    try:
        key = P256KeyPair.from_private_pem(key_raw)
    except Exception as exc:
        raise SystemExit("AgentNet identity private key is invalid") from exc
    return (
        identity_path,
        identity_raw,
        identity,
        actor,
        key_path,
        key_raw,
        key,
    )


def _laptop_reauthorization_state(
    *,
    path: Path,
    identity_path: Path,
    identity_raw: bytes,
    key_raw: bytes,
) -> tuple[dict[str, object], bytes]:
    identity_digest = hashlib.sha256(identity_raw).hexdigest()
    key_digest = hashlib.sha256(key_raw).hexdigest()
    if not os.path.lexists(path):
        state: dict[str, object] = {
            "schema": _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA,
            "identity_path": str(identity_path),
            "identity_profile_sha256": identity_digest,
            "private_key_sha256": key_digest,
            "request_id": str(uuid4()),
            "possession_secret": secrets.token_urlsafe(32),
            "transaction": None,
            "old_key_possession_signature": None,
            "approval_url_sha256": None,
            "approval_url_opened": False,
            "result": None,
            "successor_identity_sha256": None,
        }
        raw = _private_json_bytes(state)
        _write_private_config(path, state)
        return state, raw
    try:
        raw = _owner_only_file(
            path,
            label="laptop credential reauthorization state",
        )
        value = json.loads(raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(
            "laptop credential reauthorization state is not readable JSON"
        ) from exc
    if (
        not isinstance(value, dict)
        or set(value) != _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_KEYS
        or value.get("schema")
        != _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA
        or value.get("identity_path") != str(identity_path)
        or value.get("private_key_sha256") != key_digest
        or not isinstance(value.get("identity_profile_sha256"), str)
        or len(str(value.get("identity_profile_sha256"))) != 64
        or any(
            character not in "0123456789abcdef"
            for character in str(value.get("identity_profile_sha256"))
        )
        or not isinstance(value.get("approval_url_opened"), bool)
    ):
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        )
    try:
        request_id = UUID(str(value["request_id"]))
    except (TypeError, ValueError, AttributeError) as exc:
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        ) from exc
    possession_secret = value.get("possession_secret")
    transaction = value.get("transaction")
    signature = value.get("old_key_possession_signature")
    approval_url_sha256 = value.get("approval_url_sha256")
    result = value.get("result")
    successor_digest = value.get("successor_identity_sha256")
    if (
        str(request_id) != value["request_id"]
        or not isinstance(possession_secret, str)
        or len(possession_secret) != 43
        or (transaction is None) != (signature is None)
        or (
            signature is not None
            and (
                not isinstance(signature, str)
                or not 1 <= len(signature) <= 2_048
            )
        )
        or (
            approval_url_sha256 is not None
            and (
                not isinstance(approval_url_sha256, str)
                or len(approval_url_sha256) != 64
                or any(character not in "0123456789abcdef" for character in approval_url_sha256)
            )
        )
        or (value["approval_url_opened"] and approval_url_sha256 is None)
        or (result is None) != (successor_digest is None)
        or (result is not None and transaction is None)
        or (
            successor_digest is not None
            and (
                not isinstance(successor_digest, str)
                or len(successor_digest) != 64
                or any(character not in "0123456789abcdef" for character in successor_digest)
            )
        )
    ):
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        )
    if transaction is not None:
        try:
            parsed = LaptopCredentialReauthorizationRequest.model_validate(transaction)
        except Exception as exc:
            raise SystemExit(
                "laptop credential reauthorization state transaction is invalid"
            ) from exc
        if parsed.request_id != value["request_id"]:
            raise SystemExit(
                "laptop credential reauthorization state transaction binding changed"
            )
    return value, raw


def _replace_laptop_reauthorization_state(
    path: Path,
    state: dict[str, object],
    *,
    expected_raw: bytes,
) -> bytes:
    replacement = _private_json_bytes(state)
    _write_private_config(
        path,
        state,
        force=True,
        expected_content=expected_raw,
    )
    return replacement


def _laptop_reauthorization_transaction(
    value: object,
    *,
    state: dict[str, object],
    actor: VerifiedActor,
    key: P256KeyPair,
    now: int,
    require_active: bool = True,
) -> LaptopCredentialReauthorizationRequest:
    try:
        transaction = LaptopCredentialReauthorizationRequest.model_validate(value)
    except Exception as exc:
        raise SystemExit(
            "laptop credential reauthorization transaction is invalid"
        ) from exc
    public_key_sha256 = hashlib.sha256(key.public_pem.encode("utf-8")).hexdigest()
    if (
        transaction.request_id != state["request_id"]
        or transaction.domain_id != actor.domain_id
        or transaction.principal_id != actor.principal_id
        or transaction.harness_id != actor.harness_id
        or transaction.expired_credential_id != actor.credential_id
        or transaction.expected_credential_epoch != actor.credential_epoch
        or transaction.successor_credential_epoch != actor.credential_epoch + 1
        or transaction.expected_key_id != key.thumbprint
        or transaction.expected_public_key_sha256 != public_key_sha256
        or transaction.expected_binding_assurance != actor.binding_assurance
        or transaction.identity_profile_sha256
        != state["identity_profile_sha256"]
        or (
            require_active
            and (
                transaction.prepared_at > now
                or transaction.expires_at <= now
            )
        )
    ):
        raise SystemExit(
            "laptop credential reauthorization transaction binding changed"
        )
    return transaction


def _laptop_reauthorization_result(
    value: object,
    *,
    transaction: LaptopCredentialReauthorizationRequest,
) -> dict[str, object]:
    if (
        not isinstance(value, dict)
        or set(value) != _LAPTOP_CREDENTIAL_REAUTHORIZATION_RESULT_KEYS
        or value.get("schema")
        != "agentnet.laptop-credential-reauthorization-result.v1"
        or value.get("status") != "current"
        or value.get("request_id") != transaction.request_id
        or value.get("domain_id") != transaction.domain_id
        or value.get("principal_id") != transaction.principal_id
        or value.get("harness_id") != transaction.harness_id
        or value.get("previous_credential_id")
        != transaction.expired_credential_id
        or value.get("key_id") != transaction.expected_key_id
        or value.get("credential_epoch")
        != transaction.successor_credential_epoch
        or not isinstance(value.get("credential_id"), str)
        or not 1 <= len(str(value["credential_id"])) <= 256
        or value.get("credential_id") == transaction.expired_credential_id
        or type(value.get("not_before")) is not int
        or type(value.get("expires_at")) is not int
        or int(value["expires_at"]) <= int(value["not_before"])
        or type(value.get("idempotent_repeat")) is not bool
        or value.get("key_preserved") is not True
        or value.get("authority_granted") is not False
    ):
        raise SystemExit(
            "laptop credential reauthorization completion response is invalid"
        )
    return value


def _laptop_reauthorization_output(
    *,
    status: str,
    credential_epoch: int | None = None,
) -> dict[str, object]:
    output: dict[str, object] = {
        "schema": "agentnet.laptop-credential-reauthorization-cli-result.v1",
        "status": status,
    }
    if status == "current":
        output.update(
            {
                "credential_epoch": credential_epoch,
                "identity_saved_locally": True,
                "key_preserved": True,
                "authority_granted": False,
            }
        )
    return output


def _print_laptop_reauthorization_output(
    *,
    status: str,
    credential_epoch: int | None = None,
) -> None:
    print(
        json.dumps(
            _laptop_reauthorization_output(
                status=status,
                credential_epoch=credential_epoch,
            ),
            indent=2,
            sort_keys=True,
        )
    )


def _remove_laptop_reauthorization_state(path: Path) -> None:
    _remove_private_state(path, label="laptop credential reauthorization state")

def _complete_laptop_identity_reauthorization(
    *,
    identity_path: Path,
    identity_raw: bytes,
    identity: dict[str, object],
    actor: VerifiedActor,
    key_path: Path,
    key_raw: bytes,
    state_path: Path,
    state: dict[str, object],
    state_raw: bytes,
    result: dict[str, object],
) -> int:
    updated_actor = actor.model_copy(
        update={
            "credential_id": result["credential_id"],
            "credential_epoch": result["credential_epoch"],
        }
    )
    updated_identity = {
        **identity,
        "actor": updated_actor.model_dump(mode="json"),
    }
    updated_raw = _private_json_bytes(updated_identity)
    successor_digest = hashlib.sha256(updated_raw).hexdigest()
    state["result"] = result
    state["successor_identity_sha256"] = successor_digest
    _replace_laptop_reauthorization_state(
        state_path,
        state,
        expected_raw=state_raw,
    )
    if not secrets.compare_digest(
        _owner_only_file(key_path, label="AgentNet identity private key"),
        key_raw,
    ):
        raise SystemExit(
            "AgentNet identity private key changed before credential replacement"
        )
    _write_private_config(
        identity_path,
        updated_identity,
        force=True,
        expected_content=identity_raw,
    )
    replaced_identity = _owner_only_file(
        identity_path,
        label="AgentNet identity profile",
    )
    if (
        not secrets.compare_digest(replaced_identity, updated_raw)
        or hashlib.sha256(replaced_identity).hexdigest()
        != state["successor_identity_sha256"]
        or not secrets.compare_digest(
            _owner_only_file(key_path, label="AgentNet identity private key"),
            key_raw,
        )
    ):
        raise SystemExit(
            "AgentNet identity replacement could not be verified exactly"
        )
    _remove_laptop_reauthorization_state(state_path)
    _print_laptop_reauthorization_output(
        status="current",
        credential_epoch=updated_actor.credential_epoch,
    )
    return 0


def command_credential_reauthorize_expired(args: argparse.Namespace) -> int:
    if type(args.timeout) is not int or not 30 <= args.timeout <= 600:
        raise SystemExit(
            "expired credential reauthorization timeout must be between 30 and 600 seconds"
        )
    if args.browser not in {"system", "manual"}:
        raise SystemExit("expired credential reauthorization browser mode is invalid")
    (
        identity_path,
        identity_raw,
        identity,
        actor,
        key_path,
        key_raw,
        key,
    ) = _load_laptop_reauthorization_identity(Path(args.identity))
    state_path = Path(args.state).resolve()
    if state_path in {identity_path, key_path}:
        raise SystemExit(
            "credential reauthorization state must be separate from identity and key files"
        )
    state, state_raw = _laptop_reauthorization_state(
        path=state_path,
        identity_path=identity_path,
        identity_raw=identity_raw,
        key_raw=key_raw,
    )
    identity_digest = hashlib.sha256(identity_raw).hexdigest()
    if identity_digest != state["identity_profile_sha256"]:
        if (
            state["result"] is None
            or identity_digest != state["successor_identity_sha256"]
        ):
            raise SystemExit(
                "AgentNet identity profile changed during credential reauthorization"
            )
        try:
            transaction = LaptopCredentialReauthorizationRequest.model_validate(
                state["transaction"]
            )
        except Exception as exc:
            raise SystemExit(
                "completed credential reauthorization state is invalid"
            ) from exc
        result = _laptop_reauthorization_result(
            state["result"],
            transaction=transaction,
        )
        if (
            actor.domain_id != transaction.domain_id
            or actor.principal_id != transaction.principal_id
            or actor.harness_id != transaction.harness_id
            or actor.binding_assurance != transaction.expected_binding_assurance
            or actor.credential_id != result["credential_id"]
            or actor.credential_epoch != result["credential_epoch"]
            or key.thumbprint != transaction.expected_key_id
        ):
            raise SystemExit(
                "completed credential reauthorization identity binding changed"
            )
        _remove_laptop_reauthorization_state(state_path)
        _print_laptop_reauthorization_output(
            status="current",
            credential_epoch=actor.credential_epoch,
        )
        return 0

    now = int(time.time())
    if state["result"] is not None:
        transaction = _laptop_reauthorization_transaction(
            state["transaction"],
            state=state,
            actor=actor,
            key=key,
            now=now,
            require_active=False,
        )
        result = _laptop_reauthorization_result(
            state["result"],
            transaction=transaction,
        )
        return _complete_laptop_identity_reauthorization(
            identity_path=identity_path,
            identity_raw=identity_raw,
            identity=identity,
            actor=actor,
            key_path=key_path,
            key_raw=key_raw,
            state_path=state_path,
            state=state,
            state_raw=state_raw,
            result=result,
        )
    transaction: LaptopCredentialReauthorizationRequest | None = None
    if state["transaction"] is not None:
        transaction = _laptop_reauthorization_transaction(
            state["transaction"],
            state=state,
            actor=actor,
            key=key,
            now=now,
        )
    client = AgentNetClient(
        base_url=_canonical_server_origin(str(identity["server_base_url"])),
        key=key,
        domain_id=actor.domain_id,
        harness_id=actor.harness_id or "",
        credential_id=actor.credential_id or "",
        audience=str(identity["audience"]),
    )
    deadline = time.monotonic() + float(args.timeout)
    try:
        if transaction is None:
            response = client.prepare_expired_current_credential_reauthorization(
                request_id=str(state["request_id"]),
                identity_profile_sha256=str(
                    state["identity_profile_sha256"]
                ),
            )
            if response.status_code != 200:
                _print_laptop_reauthorization_output(status="blocked")
                return 1
            try:
                prepared_value = response.json()
            except Exception as exc:
                raise SystemExit(
                    "laptop credential reauthorization prepare response is invalid"
                ) from exc
            transaction = _laptop_reauthorization_transaction(
                prepared_value,
                state=state,
                actor=actor,
                key=key,
                now=now,
            )
            transaction_value = transaction.model_dump(
                mode="json",
                by_alias=True,
            )
            signature = key.sign(
                LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                transaction.possession_fields(),
            )
            state["transaction"] = transaction_value
            state["old_key_possession_signature"] = signature
            state_raw = _replace_laptop_reauthorization_state(
                state_path,
                state,
                expected_raw=state_raw,
            )

        while True:
            response = client.progress_expired_current_credential_reauthorization(
                transaction=transaction.model_dump(
                    mode="json",
                    by_alias=True,
                ),
                old_key_possession_signature=str(
                    state["old_key_possession_signature"]
                ),
                possession_secret=str(state["possession_secret"]),
            )
            if response.status_code == 200:
                try:
                    completed_value = response.json()
                except Exception as exc:
                    raise SystemExit(
                        "laptop credential reauthorization completion response is invalid"
                    ) from exc
                result = _laptop_reauthorization_result(
                    completed_value,
                    transaction=transaction,
                )
                break
            if response.status_code != 202:
                _print_laptop_reauthorization_output(status="blocked")
                return 1
            try:
                pending = response.json()
            except Exception as exc:
                raise SystemExit(
                    "laptop credential reauthorization pending response is invalid"
                ) from exc
            if (
                not isinstance(pending, dict)
                or set(pending)
                != {"schema", "status", "approval_url", "expires_at"}
                or pending.get("schema")
                != "agentnet.laptop-credential-reauthorization-pending.v1"
                or pending.get("status") != "approval_pending"
                or type(pending.get("expires_at")) is not int
                or pending["expires_at"] != transaction.expires_at
            ):
                raise SystemExit(
                    "laptop credential reauthorization pending response is invalid"
                )
            approval_url = _validate_stable_approval_url(
                pending["approval_url"]
            )
            approval_url_sha256 = hashlib.sha256(
                approval_url.encode("utf-8")
            ).hexdigest()
            if (
                state["approval_url_sha256"] is not None
                and state["approval_url_sha256"] != approval_url_sha256
            ):
                raise SystemExit(
                    "laptop credential reauthorization approval entrypoint changed"
                )
            if not state["approval_url_opened"]:
                if args.browser == "manual":
                    _require_private_terminal_or_exit()
                state["approval_url_sha256"] = approval_url_sha256
                state_raw = _replace_laptop_reauthorization_state(
                    state_path,
                    state,
                    expected_raw=state_raw,
                )
                _handoff_guided_authorization(
                    approval_url,
                    browser=(
                        "system"
                        if args.browser == "system"
                        else "terminal"
                    ),
                    purpose="stable owner approval",
                )
                state["approval_url_opened"] = True
                state_raw = _replace_laptop_reauthorization_state(
                    state_path,
                    state,
                    expected_raw=state_raw,
                )
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                _print_laptop_reauthorization_output(
                    status="approval_pending"
                )
                return 2
            time.sleep(min(2.0, remaining))
    finally:
        client.close()

    return _complete_laptop_identity_reauthorization(
        identity_path=identity_path,
        identity_raw=identity_raw,
        identity=identity,
        actor=actor,
        key_path=key_path,
        key_raw=key_raw,
        state_path=state_path,
        state=state,
        state_raw=state_raw,
        result=result,
    )


def command_c0_pilot_responder(args: argparse.Namespace) -> int:
    config_path = Path(args.config)
    credential_path = Path(args.credential)
    config = load_c0_responder_config(config_path)
    if args.check:
        try:
            value = check_c0_responder(config, credential_path)
        except GateBlocked:
            print(
                json.dumps(
                    {
                        "schema": "agentnet.c0-pilot-responder.check.v1",
                        "status": "blocked",
                    },
                    indent=2,
                    sort_keys=True,
                )
            )
            return 1
    else:
        value = run_c0_responder(config, credential_path, config_path)
    print(json.dumps(value, indent=2, sort_keys=True))
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
    if args.beneficiary_identity is not None:
        beneficiary_client, beneficiary, _beneficiary_key = _load_identity_client(
            Path(args.beneficiary_identity)
        )
        beneficiary_client.close()
        if beneficiary.domain_id != actor.domain_id or beneficiary.principal_id is None:
            client.close()
            raise SystemExit("beneficiary identity is not a human in the issuer's exact domain")
        beneficiary_principal_id = beneficiary.principal_id
    else:
        beneficiary_principal_id = args.beneficiary_principal_id
        if (
            not beneficiary_principal_id
            or beneficiary_principal_id.strip() != beneficiary_principal_id
        ):
            client.close()
            raise SystemExit("beneficiary principal id must be a non-empty exact value")
    entitlement = HumanEntitlement(
        entitlement_id=args.entitlement_id or str(uuid4()),
        domain_id=actor.domain_id,
        principal_id=beneficiary_principal_id,
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
                "beneficiary_principal_id": beneficiary_principal_id,
                "authority_is_human_only": True,
                "next": "verify the exact bounded entitlement and retain its audit evidence",
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






def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agentnet", description="AgentNet")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    commands = parser.add_subparsers(dest="command", required=True)
    configure_approval_parser(commands)
    setup = commands.add_parser(
        "setup",
        help="begin or resume package-owned user-level AgentNet setup",
    )
    _configure_client_setup_arguments(setup)
    setup.set_defaults(func=command_client_setup)
    setup_commands = setup.add_subparsers(dest="setup_command", required=False)
    setup_status = setup_commands.add_parser(
        "status",
        help="show the exact resumable setup state",
    )
    _configure_client_setup_arguments(setup_status, defaults=False)
    setup_status.set_defaults(func=command_client_setup_status)
    setup_continue = setup_commands.add_parser(
        "continue",
        help="continue enrollment or activation without restarting the harness",
    )
    _configure_client_setup_arguments(setup_continue, defaults=False)
    setup_continue.set_defaults(func=command_client_setup_continue)


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
    network_create.add_argument(
        "--database-url-from-env",
        action="store_true",
        help="resolve the DSN only from --database-url-env so it never appears in process arguments",
    )
    network_create.add_argument("--public-base-url", required=True)
    network_create.add_argument("--oidc-config", required=True)
    network_create.add_argument(
        "--artifact-mode",
        choices=("enabled", "disabled"),
        default="enabled",
        help="enabled keeps scanner-backed artifacts; disabled permits communication only",
    )
    network_create.add_argument(
        "--scanner-trust-config",
        help="public maintained-scanner trust configuration required when artifacts are enabled",
    )
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
    server_agent_setup = server_agent_commands.add_parser(
        "setup",
        help="plan or apply the fixed product-owned ordinary Linux server profile",
    )
    server_agent_setup.add_argument("--request")
    server_agent_setup.add_argument(
        "--apply",
        action="store_true",
        help="apply the frozen setup request; omitted means no-managed-host-write plan",
    )
    server_agent_setup.add_argument(
        "--start",
        action="store_true",
        help="start and health-check only the managed AgentNet units after apply",
    )
    server_agent_setup.add_argument(
        "--expected-request-digest",
        help="exact digest from the human-approved no-managed-host-write plan; required with --apply",
    )
    server_agent_setup.set_defaults(func=command_server_agent_setup)
    server_agent_reset = server_agent_commands.add_parser(
        "reset",
        help="remove only package-owned server state while retaining every external prerequisite",
    )
    server_agent_reset.add_argument(
        "--retain-external-prerequisites",
        action="store_true",
        required=True,
        help="required acknowledgment that PostgreSQL, Node.js, uv, proxy, TLS, and operator config are retained",
    )
    server_agent_reset.add_argument(
        "--confirm-package-state-removal",
        action="store_true",
        required=True,
        help="required explicit confirmation to stop managed units and remove package-owned AgentNet state",
    )
    server_agent_reset.set_defaults(func=command_server_agent_reset)
    server_agent_activate = server_agent_commands.add_parser(
        "activate",
        help="bind an offline server config to one exact enrolled identity without granting authority",
    )
    server_agent_activate.add_argument("--config", default="agentnet.json")
    server_agent_activate.add_argument("--identity", default=".agentnet/identity.json")
    server_agent_activate.set_defaults(func=command_server_agent_activate)
    server_agent_reauthorize = server_agent_commands.add_parser(
        "reauthorize-expired-credential",
        help="reauthorize one exact expired managed-server credential through Approval",
    )
    server_agent_reauthorize.add_argument("--config", default=str(CORE_CONFIG))
    server_agent_reauthorize.add_argument("--identity", default=str(SERVER_AGENT_IDENTITY))
    server_agent_reauthorize.add_argument(
        "--state",
        default="/var/lib/agentnet-setup/credential-reauthorization.json",
    )
    server_agent_reauthorize.add_argument(
        "--replace-terminal-state",
        action="store_true",
        help="replace only a broker-proven rejected or expired pending ceremony",
    )
    server_agent_reauthorize.set_defaults(
        func=command_server_agent_reauthorize_expired_credential
    )

    join = commands.add_parser("join", help="enroll this person and device into an AgentNet")
    join_commands = join.add_subparsers(dest="join_command", required=True)
    join_guided = join_commands.add_parser(
        "guided",
        help="run resumable browser OIDC and Core-brokered independent approval",
    )
    join_guided.add_argument("--server", required=True)
    join_guided.add_argument("--domain")
    join_guided.add_argument("--harness")
    join_guided.add_argument("--name")
    join_guided.add_argument("--state", default=".agentnet/guided-join.json")
    join_guided.add_argument("--private-key")
    join_guided.add_argument("--identity", default=".agentnet/identity.json")
    join_guided.add_argument(
        "--browser",
        choices=("system", "terminal", "remote"),
        default="system",
        help="open locally, use fixed Core /activate remotely, or disclose through a private terminal",
    )
    join_guided.add_argument("--timeout", type=int, choices=range(30, 601), default=300)
    join_guided.add_argument(
        "--replace-terminal-state",
        action="store_true",
        help=(
            "replace one exact pending local state only after Core proves its "
            "continuation expired or failed; the candidate key is reused"
        ),
    )
    join_guided.set_defaults(func=command_join_guided)
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
    invitation_sponsored = invitation_commands.add_parser("join-sponsored")
    invitation_sponsored.add_argument("--server", required=True)
    invitation_sponsored.add_argument("--harness-id")
    invitation_sponsored.add_argument("--harness", default="laptop")
    invitation_sponsored.add_argument("--name", required=True)
    invitation_sponsored.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        default="os_bound",
    )
    invitation_sponsored.add_argument("--state", default=".agentnet/sponsored-enrollment.json")
    invitation_sponsored.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_sponsored.add_argument("--private-key")
    invitation_sponsored.add_argument("--callback")
    invitation_sponsored.add_argument("--identity", default=".agentnet/identity.json")
    invitation_sponsored.add_argument("--force", action="store_true")
    invitation_sponsored.set_defaults(func=command_invitation_join_sponsored)
    invitation_revoke = invitation_commands.add_parser("revoke")
    invitation_revoke.add_argument("--identity", default=".agentnet/identity.json")
    invitation_revoke.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_revoke.add_argument("--reason", required=True)
    invitation_revoke.set_defaults(func=command_invitation_revoke)

    bootstrap_plan = commands.add_parser(
        "bootstrap-plan",
        help="prepare the bounded same-principal two-harness C0 plan",
    )
    bootstrap_plan_commands = bootstrap_plan.add_subparsers(
        dest="bootstrap_plan_command",
        required=True,
    )
    for name, function in (
        ("begin", command_bootstrap_plan_begin),
        ("status", command_bootstrap_plan_status),
        ("complete", command_bootstrap_plan_complete),
    ):
        operation = bootstrap_plan_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.add_argument("--state", default=".agentnet/bootstrap-plan-state.json")
        operation.set_defaults(func=function)

    communication_scope = commands.add_parser(
        "communication-scope",
        help="approve or inspect the persistent same-principal communication scope",
    )
    communication_scope_commands = communication_scope.add_subparsers(
        dest="communication_scope_command",
        required=True,
    )
    for name, function in (
        ("begin", command_communication_scope_begin),
        ("status", command_communication_scope_status),
        ("complete", command_communication_scope_complete),
    ):
        operation = communication_scope_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.add_argument(
            "--state",
            default=".agentnet/communication-scope-state.json",
        )
        if name == "begin":
            operation.add_argument(
                "--replace-terminal-state",
                action="store_true",
                help="replace retry keys only after Core proves the old scope terminal",
            )
        operation.set_defaults(func=function)

    credential = commands.add_parser(
        "credential",
        help="operate the exact current signed credential",
    )
    credential_commands = credential.add_subparsers(dest="credential_command", required=True)
    credential_renew = credential_commands.add_parser(
        "renew",
        help="renew the exact configured always-on credential within policy window",
    )
    credential_renew.add_argument("--identity", default=".agentnet/identity.json")
    credential_renew.add_argument("--state", default=".agentnet/credential-renewal-state.json")
    credential_renew.set_defaults(func=command_credential_renew)
    credential_reauthorize = credential_commands.add_parser(
        "reauthorize-expired",
        help="reauthorize the exact expired laptop credential without replacing its key or identity",
    )
    credential_reauthorize.add_argument(
        "--identity",
        default=".agentnet/identity.json",
    )
    credential_reauthorize.add_argument(
        "--state",
        default=".agentnet/credential-reauthorization-state.json",
    )
    credential_reauthorize.add_argument(
        "--browser",
        choices=("system", "manual"),
        default="system",
        help="open the stable Approval entrypoint or disclose it through a private terminal",
    )
    credential_reauthorize.add_argument(
        "--timeout",
        type=int,
        choices=range(30, 601),
        default=300,
    )
    credential_reauthorize.set_defaults(
        func=command_credential_reauthorize_expired
    )

    c0_pilot = commands.add_parser(
        "c0-pilot",
        help="run or inspect the fixed same-principal two-harness C0 proof",
    )
    c0_pilot_commands = c0_pilot.add_subparsers(
        dest="c0_pilot_command", required=True
    )
    for name in ("start", "status", "complete"):
        operation = c0_pilot_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.set_defaults(func=command_c0_pilot)
    c0_responder = c0_pilot_commands.add_parser(
        "responder",
        help="check or run dedicated package-owned C0 responder",
    )
    c0_responder.add_argument("--config", required=True)
    c0_responder.add_argument("--credential", required=True)
    c0_responder_mode = c0_responder.add_mutually_exclusive_group(required=True)
    c0_responder_mode.add_argument("--check", action="store_true")
    c0_responder_mode.add_argument("--run", action="store_true")
    c0_responder.set_defaults(func=command_c0_pilot_responder)

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

    artifact = commands.add_parser(
        "artifact",
        help="upload quarantined artifacts and download released bytes",
    )
    artifact_commands = artifact.add_subparsers(dest="artifact_command", required=True)
    artifact_upload = artifact_commands.add_parser(
        "upload",
        help="reserve, upload, and promote one exact file into quarantine",
    )
    artifact_upload.add_argument("path")
    artifact_upload.add_argument("--identity", default=".agentnet/identity.json")
    artifact_upload.add_argument("--idempotency-key", required=True)
    artifact_upload.add_argument("--media-type", required=True)
    artifact_upload.add_argument("--origin", required=True)
    artifact_upload.add_argument(
        "--classification",
        choices=tuple(item.value for item in Classification),
        default=Classification.C1_INTERNAL.value,
    )
    artifact_upload.add_argument("--ttl-seconds", type=int, default=3600)
    artifact_upload.add_argument("--optional-attachment", action="store_true")
    artifact_upload.set_defaults(func=command_artifact_upload)
    artifact_abort = artifact_commands.add_parser(
        "abort",
        help="abort one caller-owned unpromoted reservation",
    )
    artifact_abort.add_argument("reservation_id")
    artifact_abort.add_argument("--identity", default=".agentnet/identity.json")
    artifact_abort.set_defaults(func=command_artifact_abort)
    artifact_lifecycle = artifact_commands.add_parser(
        "lifecycle",
        help="read content-free lifecycle state for one artifact",
    )
    artifact_lifecycle.add_argument("artifact_id")
    artifact_lifecycle.add_argument("--identity", default=".agentnet/identity.json")
    artifact_lifecycle.set_defaults(func=command_artifact_lifecycle)
    artifact_download = artifact_commands.add_parser(
        "download",
        help="consume a current single-use capability into a new private file",
    )
    artifact_download.add_argument("artifact_id")
    artifact_download.add_argument("--output", required=True)
    artifact_download.add_argument("--identity", default=".agentnet/identity.json")
    artifact_download.add_argument("--ttl-seconds", type=int, default=60)
    artifact_download.set_defaults(func=command_artifact_download)

    message = commands.add_parser("message", help="send and receive authenticated messages")
    message_commands = message.add_subparsers(dest="message_command", required=True)
    message_send = message_commands.add_parser("send")
    message_send.add_argument("--identity", default=".agentnet/identity.json")
    message_send.add_argument("--collaboration-scope-id", required=True)
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
    message_inbox.add_argument("--collaboration-scope-id", required=True)
    message_inbox.add_argument("--after", type=int, default=0)
    message_inbox.add_argument("--limit", type=int, default=100)
    message_inbox.set_defaults(func=command_message_inbox)
    message_acknowledge = message_commands.add_parser(
        "acknowledge",
        help="record durable custody for one exact mailbox event",
    )
    message_acknowledge.add_argument("event_id")
    message_acknowledge.add_argument("--envelope-digest", required=True)
    message_acknowledge.add_argument("--identity", default=".agentnet/identity.json")
    message_acknowledge.add_argument("--collaboration-scope-id", required=True)
    message_acknowledge.set_defaults(func=command_message_acknowledge)

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
    beneficiary = entitlement_issue.add_mutually_exclusive_group(required=True)
    beneficiary.add_argument("--beneficiary-identity")
    beneficiary.add_argument("--beneficiary-principal-id")
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
    console = commands.add_parser(
        "console",
        help="operate the private AgentNet administration console",
    )
    console_commands = console.add_subparsers(dest="console_command", required=True)
    console_serve = console_commands.add_parser(
        "serve",
        help="serve only the private administration console on loopback",
    )
    console_serve.add_argument("--config", default="agentnet.json")
    console_serve.add_argument("--host", default="127.0.0.1")
    console_serve.add_argument("--port", type=int, default=8090)
    console_serve.add_argument("--log-level", default="info")
    console_serve.set_defaults(func=command_console_serve)
    console_open = console_commands.add_parser(
        "open",
        help="open a signed private console session without disclosing credentials in a URL",
    )
    console_open.add_argument("--identity", default=".agentnet/identity.json")
    console_open.add_argument("--handoff-timeout", type=float, default=10.0)
    console_open.set_defaults(func=command_console_open)


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

    manager_run = commands.add_parser(
        "manager-run",
        help="run interactive Pi or OMP with the packaged local signed Manager extension",
    )
    manager_run.add_argument("--identity", required=True)
    manager_run.add_argument(
        "--state-dir",
        help="owner-only local Manager gateway state directory",
    )
    manager_run.add_argument(
        "manager_command",
        nargs="+",
        metavar="COMMAND",
        help="required Pi or OMP command and arguments after --; AgentNet owns extension/tool flags",
    )
    manager_run.set_defaults(func=command_manager_run)

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
