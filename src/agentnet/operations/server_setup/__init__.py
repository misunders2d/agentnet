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
from typing import Any, Callable, Literal, Mapping, Sequence
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
from agentnet.operations.c0_credential_supersession import (
    load_audited_supersession_journal,
    load_supersession_journal,
)
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
from . import upgrade as _upgrade
from .upgrade import render_units
from .upgrade_state import SETUP_ATTEMPT, SETUP_MARKER, SETUP_UPGRADE_JOURNAL
from . import systemd as _systemd
from .systemd import (
    APPROVAL_CONFIG,
    APPROVAL_DATA,
    APPROVAL_ENV,
    APPROVAL_PORT,
    APPROVAL_UNIT,
    APPROVAL_USER,
    C0_RESPONDER_CONFIG,
    C0_RESPONDER_DATA,
    C0_RESPONDER_UNIT,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_DATA,
    CORE_ENV,
    CORE_PORT,
    CORE_UNIT,
    CORE_USER,
    CREDENTIAL_RENEW_STATE,
    CREDENTIAL_RENEW_TIMER,
    CREDENTIAL_RENEW_UNIT,
    LEGACY_COMMUNICATION_ONLY_UNITS,
    MANAGED_UNITS,
    SECRET_ROOT,
    SERVER_AGENT_IDENTITY,
    SERVER_AGENT_KEY,
    UnitRenderError,
    _managed_service_runtime as _rendered_service_runtime,
    render_managed_units,
)
from .models import (
    ScannerSetupSpec,
    ServerSetupError,
    ServerSetupPreflight,
    ServerSetupRequest,
    SetupApprover,
    SetupLayout,
    SetupOIDCProvider,
    SetupRuntimeIdentity,
    SETUP_ROOT,
    SYSTEMD_UNIT_ROOT,
)
from .preflight import (
    SCANNER_SIGNING_KEY,
    _BROKER_CREDENTIAL_NAME,
    _MAX_CONFIG_BYTES,
    _SYSTEM_PATH,
    _allowed_input_owners,
    _input_fields,
    _legacy_request_digest,
    _package_owned_executable,
    _parse_environment,
    _parse_environment_file,
    _planned_setup_evidence,
    _read_bounded_snapshot,
    _read_input_bundle,
    _read_private_input,
    _reject_duplicates,
    _request_digest,
    _request_references,
    _require_root_owned_executable,
    _require_root_owned_tree,
    _require_scanner_readiness,
    _require_service_visible_path,
    _resolve_executable,
    _resolve_host_tool,
    _resolve_node_executable,
    _resolve_scanner_setup,
    _resolve_setup_runtime,
    _resolve_uv_executable,
    _scanner_integer,
    _server_setup_preflight,
    _sha256_stable_file,
    _sha256_stable_tree,
    _strict_json_bytes,
    _validate_broker_credential,
    _validate_inputs,
    load_server_setup_request,
    plan_server_setup,
)
from .custody import (
    _BoundedCommandResult,
    _account_fact,
    _atomic_replace_exact,
    _atomic_write,
    _drop_identity,
    _ensure_account,
    _ensure_private_root,
    _ensure_root_private_directory,
    _kill_product_process_tree,
    _managed_config_digest,
    _prepare_managed_service_runtime,
    _private_entry_exists,
    _read_managed_exact,
    _read_managed_unit,
    _read_private_managed_file,
    _read_setup_marker,
    _remove_managed_unit_exact,
    _require_communication_only_artifact_absence,
    _require_private_directory,
    _require_private_file,
    _require_private_tree,
    _run_as,
    _run_bounded_product_process,
    _service_environment,
    _validate_account,
    _write_managed_unit,
)
from .database import (
    _LIFECYCLE_PRESERVED_TABLES,
    _LIFECYCLE_RELEASE_TABLES,
    _LIFECYCLE_SETUP_UPGRADE,
    _LIFECYCLE_SOURCE_SCHEMA,
    _LIFECYCLE_TARGET_SCHEMA,
    _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA,
    _expected_migrated_collaboration,
    _postgres_migration_catalog,
    _postgres_peer_gate,
    _postgres_relation_digest,
    _postgres_schema_version,
    _postgres_supersession_audit_evidence,
    _postgres_v0145_database_operation,
    _postgres_v0145_identity,
    _postgres_v0145_source_snapshot,
    _postgres_v0145_target_endpoint,
    _postgres_v0145_target_is_rollback_safe,
    _require_migrated_collaboration_state,
    _require_v0145_source_snapshot,
    _run_postgres_probe_as,
    _run_supersession_audit_as,
    _run_v0145_database_operation_as,
)
from .provisioning import (
    APPROVAL_STATE,
    CORE_OIDC_CONFIG,
    SCANNER_WORKER_CONFIG,
    _approval_trust,
    _build_core_oidc_config,
    _configure_scanner_worker,
    _core_create_arguments,
    _ensure_c0_responder_runtime,
    _legacy_remote_activation_oidc,
    _load_upgrade_compatible_core_config,
    _load_validated_core_config,
    _provision_approval_service,
    _provision_core_service,
    _require_core_config_matches,
    _require_core_create_evidence,
    _require_exact_approval_policy,
)
from .activation import (
    C0_RESPONDER_TERMINAL,
    CREDENTIAL_SUPERSESSION_JOURNAL,
    _START_HEALTH_ATTEMPTS,
    _health,
    _prepare_c0_responder_activation,
    _start_managed_server_services,
    _validated_c0_terminal_marker,
    _validated_managed_identity_profile,
)
from .apply import apply_server_setup


SETUP_RUNTIME_ROOT = SETUP_ROOT / "npm-runtime"







































































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
