"""Crash-safe local conformance store.

SQLite is never advertised as the production ``accepted_durable`` boundary.
It supports the local supervisor/pilot label ``accepted_local`` only.
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import stat
import threading
import time
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from functools import lru_cache
from pathlib import Path
from typing import Any

from agentnet.errors import GateBlocked, IdempotencyConflict, ReplayError
from agentnet.host import host_platform
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json
from agentnet.storage.a2a_schema import A2A_SCHEMA, A2A_SCHEMA_VERSION
from agentnet.storage.authority_bootstrap_schema import (
    AUTHORITY_BOOTSTRAP_SCHEMA,
)
from agentnet.storage.artifact_lifecycle_schema import (
    ARTIFACT_LIFECYCLE_SCHEMA,
)
from agentnet.storage.artifact_quota_schema import (
    ARTIFACT_QUOTA_SCHEMA,
    ARTIFACT_QUOTA_SCHEMA_VERSION,
)
from agentnet.storage.bootstrap_plan_schema import (
    BOOTSTRAP_PLAN_SCHEMA,
    BOOTSTRAP_PLAN_SCHEMA_VERSION,
)
from agentnet.storage.credential_recovery_schema import (
    CREDENTIAL_RECOVERY_SCHEMA,
    CREDENTIAL_RECOVERY_SCHEMA_VERSION,
)
from agentnet.storage.effect_lifecycle_schema import (
    EFFECT_LIFECYCLE_SCHEMA,
    EFFECT_LIFECYCLE_SCHEMA_VERSION,
)
from agentnet.storage.guided_enrollment_schema import (
    GUIDED_ENROLLMENT_SCHEMA,
    GUIDED_ENROLLMENT_SCHEMA_VERSION,
)
from agentnet.storage.identity_lifecycle_schema import (
    IDENTITY_LIFECYCLE_SCHEMA,
    IDENTITY_LIFECYCLE_SCHEMA_VERSION,
)
from agentnet.storage.identity_schema import IDENTITY_SCHEMA, IDENTITY_SCHEMA_VERSION
from agentnet.storage.ipc_schema import IPC_SCHEMA, IPC_SCHEMA_VERSION
from agentnet.storage.operational_control_schema import (
    OPERATIONAL_CONTROL_SCHEMA,
)
from agentnet.storage.post_audit_schema import POST_AUDIT_SCHEMA
from agentnet.storage.relationship_governance_schema import (
    RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA,
)
from agentnet.storage.response_obligation_schema import (
    RESPONSE_OBLIGATION_SCHEMA,
)
from agentnet.storage.supervisor_schema import SUPERVISOR_SCHEMA, SUPERVISOR_SCHEMA_VERSION
from agentnet.storage.task_custody_schema import (
    TASK_CUSTODY_SCHEMA,
    TASK_CUSTODY_SCHEMA_VERSION,
)
from agentnet.storage.task_payload_release_schema import (
    TASK_PAYLOAD_RELEASE_SCHEMA,
    TASK_PAYLOAD_RELEASE_SCHEMA_VERSION,
)
from agentnet.storage.versioning_schema import VERSIONING_SCHEMA, VERSIONING_SCHEMA_VERSION
from agentnet.storage.workload_schema import WORKLOAD_SCHEMA, WORKLOAD_SCHEMA_VERSION


BASE_SCHEMA = """
CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS domains (
    domain_id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('active','quarantined','revoked')),
    policy_revision INTEGER NOT NULL DEFAULT 1,
    revocation_epoch INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS principals (
    principal_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    oidc_issuer TEXT NOT NULL,
    oidc_subject TEXT NOT NULL,
    verified_email TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','quarantined','revoked')),
    created_at INTEGER NOT NULL,
    UNIQUE(domain_id, oidc_issuer, oidc_subject)
);
CREATE TABLE IF NOT EXISTS principal_aliases (
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    verified_email TEXT NOT NULL,
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY(principal_id, verified_email)
);
CREATE TABLE IF NOT EXISTS harnesses (
    harness_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT REFERENCES principals(principal_id),
    guest_id TEXT,
    kind TEXT NOT NULL,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','active','deterministic_only','quarantined','revoked')),
    binding_assurance TEXT NOT NULL,
    capabilities_json TEXT NOT NULL,
    credential_epoch INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    CHECK ((principal_id IS NULL) != (guest_id IS NULL))
);
CREATE TABLE IF NOT EXISTS credentials (
    credential_id TEXT PRIMARY KEY,
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    key_id TEXT NOT NULL,
    public_key_pem TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','retired','compromised','revoked')),
    epoch INTEGER NOT NULL,
    not_before INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE(harness_id, key_id, epoch)
);
CREATE TABLE IF NOT EXISTS entitlements (
    entitlement_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    action TEXT NOT NULL,
    resource_pattern TEXT NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER,
    revision INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS enrollment_challenges (
    challenge_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    oidc_issuer TEXT NOT NULL,
    oidc_subject TEXT NOT NULL,
    verified_email TEXT NOT NULL,
    harness_kind TEXT NOT NULL,
    harness_name TEXT NOT NULL,
    public_key_pem TEXT NOT NULL,
    key_id TEXT NOT NULL,
    nonce_hash TEXT NOT NULL,
    transaction_digest TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    failed_attempts INTEGER NOT NULL DEFAULT 0,
    approved_receipt TEXT,
    consumed_at INTEGER
);
CREATE TABLE IF NOT EXISTS task_grants (
    grant_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    harness_id TEXT NOT NULL,
    grant_json TEXT NOT NULL,
    max_uses INTEGER NOT NULL,
    uses INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS events (
    event_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    actor_json TEXT NOT NULL,
    event_type TEXT NOT NULL,
    classification TEXT NOT NULL,
    payload_encrypted TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    envelope_digest TEXT NOT NULL,
    envelope_json TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    acceptance_fact TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    delivery_expires_at INTEGER,
    effect_deadline INTEGER,
    retention_delete_at INTEGER,
    legal_hold INTEGER NOT NULL DEFAULT 0,
    policy_revision INTEGER NOT NULL,
    credential_epoch INTEGER NOT NULL,
    UNIQUE(domain_id, actor_json, idempotency_key)
);
CREATE TABLE IF NOT EXISTS recipients (
    event_id TEXT NOT NULL REFERENCES events(event_id),
    recipient_id TEXT NOT NULL,
    cursor INTEGER NOT NULL UNIQUE,
    current_fact TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(event_id, recipient_id)
);
CREATE TABLE IF NOT EXISTS receipts (
    receipt_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(event_id),
    recipient_id TEXT,
    fact TEXT NOT NULL,
    owner_actor_json TEXT NOT NULL,
    event_digest TEXT NOT NULL,
    detail_json TEXT NOT NULL,
    signature TEXT,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS replay_nonces (
    actor_id TEXT NOT NULL,
    nonce_hash TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(actor_id, nonce_hash)
);
CREATE TABLE IF NOT EXISTS rooms (
    room_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    owner_domain_id TEXT NOT NULL,
    owner_epoch INTEGER NOT NULL,
    control_sequence INTEGER NOT NULL,
    state TEXT NOT NULL,
    classification TEXT NOT NULL,
    history_mode TEXT NOT NULL,
    expires_at INTEGER,
    legal_hold INTEGER NOT NULL DEFAULT 0,
    application_epoch INTEGER NOT NULL DEFAULT 1,
    mls_epoch INTEGER NOT NULL DEFAULT 0,
    file_key_epoch INTEGER NOT NULL DEFAULT 1,
    mls_group_id TEXT,
    mls_provider_id TEXT,
    policy_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS room_members (
    room_id TEXT NOT NULL REFERENCES rooms(room_id),
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    role TEXT NOT NULL,
    joined_sequence INTEGER NOT NULL,
    removed_sequence INTEGER,
    PRIMARY KEY(room_id, harness_id, joined_sequence)
);
CREATE TABLE IF NOT EXISTS room_transfers (
    transfer_id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL REFERENCES rooms(room_id),
    source_domain_id TEXT NOT NULL,
    target_domain_id TEXT NOT NULL,
    cutoff_sequence INTEGER NOT NULL,
    cutoff_event_sequence INTEGER NOT NULL,
    source_owner_epoch INTEGER NOT NULL,
    application_epoch INTEGER NOT NULL,
    mls_epoch INTEGER NOT NULL,
    file_key_epoch INTEGER NOT NULL,
    snapshot_digest TEXT NOT NULL,
    source_proposal_json TEXT NOT NULL,
    source_signatures_json TEXT NOT NULL,
    target_acceptance_digest TEXT,
    target_acceptance_json TEXT,
    target_signature TEXT,
    target_credential_id TEXT,
    state TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    committed_at INTEGER
);
CREATE TABLE IF NOT EXISTS artifact_reservations (
    reservation_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    object_key TEXT NOT NULL,
    expected_digest TEXT NOT NULL,
    expected_size INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    classification TEXT NOT NULL,
    object_version TEXT,
    required_attachment INTEGER NOT NULL,
    state TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE(domain_id, actor_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS artifact_manifests (
    artifact_id TEXT PRIMARY KEY,
    reservation_id TEXT NOT NULL UNIQUE REFERENCES artifact_reservations(reservation_id),
    domain_id TEXT NOT NULL,
    object_key TEXT NOT NULL,
    object_version TEXT NOT NULL,
    ciphertext_digest TEXT NOT NULL,
    plaintext_digest_encrypted TEXT NOT NULL,
    size INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    classification TEXT NOT NULL,
    state TEXT NOT NULL,
    provenance_json TEXT NOT NULL,
    scanner_attestation_json TEXT,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_intents (
    intent_id TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','completed')),
    created_at INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE TABLE IF NOT EXISTS artifact_release_outbox (
    outbox_id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL UNIQUE REFERENCES artifact_manifests(artifact_id),
    intent_id TEXT NOT NULL UNIQUE REFERENCES audit_intents(intent_id),
    object_key TEXT NOT NULL,
    object_version TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','completed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE TABLE IF NOT EXISTS server_agent_relay_outbox (
    packet_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(event_id),
    target_domain_id TEXT NOT NULL,
    target_recipient_id TEXT NOT NULL,
    packet_json TEXT NOT NULL,
    packet_digest TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('staged','remote_accepted','recipient_committed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    receipt_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(event_id,target_domain_id,target_recipient_id)
);
CREATE TABLE IF NOT EXISTS server_agent_relay_inbox (
    packet_id TEXT PRIMARY KEY,
    source_domain_id TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    target_recipient_id TEXT NOT NULL,
    guest_actor_json TEXT NOT NULL,
    packet_json TEXT NOT NULL,
    packet_digest TEXT NOT NULL,
    local_event_encrypted TEXT NOT NULL,
    target_grant_id TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('authorized_pending','recipient_committed','failed')),
    local_event_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS download_capabilities (
    capability_hash TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL REFERENCES artifact_manifests(artifact_id),
    audience_harness_id TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    issued_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS presence_leases (
    harness_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    lease_json TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS federation_invitations (
    invitation_id TEXT PRIMARY KEY,
    host_domain_id TEXT NOT NULL,
    home_domain_id TEXT NOT NULL,
    sponsor_principal_id TEXT NOT NULL,
    invitation_digest TEXT NOT NULL,
    grant_json TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS federation_trusts (
    host_domain_id TEXT NOT NULL,
    home_domain_id TEXT NOT NULL,
    assurance_profile TEXT NOT NULL,
    home_key_id TEXT NOT NULL,
    home_public_key_pem TEXT NOT NULL,
    host_key_id TEXT NOT NULL,
    home_assertion_digest TEXT NOT NULL,
    host_acceptance_digest TEXT NOT NULL,
    metadata_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','quarantined','revoked','expired')),
    expires_at INTEGER NOT NULL,
    revocation_epoch INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(host_domain_id,home_domain_id)
);
CREATE TABLE IF NOT EXISTS guests (
    guest_id TEXT PRIMARY KEY,
    host_domain_id TEXT NOT NULL,
    home_domain_id TEXT NOT NULL,
    pairwise_subject TEXT NOT NULL,
    sponsor_principal_id TEXT NOT NULL,
    status TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE(host_domain_id, home_domain_id, pairwise_subject)
);
CREATE TABLE IF NOT EXISTS guest_entitlements (
    grant_id TEXT PRIMARY KEY,
    guest_id TEXT NOT NULL REFERENCES guests(guest_id),
    action TEXT NOT NULL,
    resource_pattern TEXT NOT NULL,
    data_class TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS effect_reservations (
    effect_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(event_id),
    grant_id TEXT NOT NULL REFERENCES task_grants(grant_id),
    actor_json TEXT NOT NULL,
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    input_source TEXT NOT NULL,
    sink TEXT NOT NULL,
    data_class TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL REFERENCES policy_decisions(decision_id),
    state TEXT NOT NULL,
    fence INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(event_id, sink, request_digest)
);
CREATE TABLE IF NOT EXISTS audit_log (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at INTEGER NOT NULL,
    record_json TEXT NOT NULL,
    previous_hash TEXT NOT NULL,
    record_hash TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS policy_decisions (
    decision_id TEXT PRIMARY KEY,
    occurred_at INTEGER NOT NULL,
    actor_json TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_json TEXT NOT NULL,
    context_json TEXT NOT NULL,
    allowed INTEGER NOT NULL,
    reason TEXT NOT NULL,
    policy_revision INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS quota_counters (
    scope TEXT NOT NULL,
    metric TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    used INTEGER NOT NULL,
    limit_value INTEGER NOT NULL,
    PRIMARY KEY(scope,metric,window_start)
);
CREATE TABLE IF NOT EXISTS telemetry_counters (
    metric TEXT NOT NULL,
    outcome TEXT NOT NULL,
    count INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(metric,outcome)
);
CREATE TABLE IF NOT EXISTS directory_records (
    record_id TEXT PRIMARY KEY,
    record_type TEXT NOT NULL CHECK (record_type IN ('agent','room','domain','endpoint')),
    domain_id TEXT NOT NULL,
    epoch INTEGER NOT NULL,
    record_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','revoked')),
    expires_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS conversations (
    conversation_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    created_by_authority_id TEXT NOT NULL,
    classification TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active','archived')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    authority_id TEXT NOT NULL,
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    role TEXT NOT NULL CHECK (role IN ('owner','member')),
    status TEXT NOT NULL CHECK (status IN ('active','left','revoked')),
    joined_at INTEGER NOT NULL,
    PRIMARY KEY(conversation_id,harness_id)
);
CREATE TABLE IF NOT EXISTS conversation_actions (
    event_id TEXT PRIMARY KEY REFERENCES events(event_id),
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    thread_id TEXT NOT NULL,
    action_kind TEXT NOT NULL,
    parent_event_id TEXT,
    task_id TEXT,
    actor_authority_id TEXT NOT NULL,
    actor_harness_id TEXT NOT NULL,
    action_digest TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_actions_thread
    ON conversation_actions(conversation_id,thread_id,created_at,event_id);
CREATE TABLE IF NOT EXISTS conversation_tasks (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    task_id TEXT NOT NULL,
    creator_authority_id TEXT NOT NULL,
    assignee_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    source_event_id TEXT NOT NULL REFERENCES events(event_id),
    latest_event_id TEXT NOT NULL REFERENCES events(event_id),
    state TEXT NOT NULL CHECK (state IN (
        'requested','handed_off','cancel_requested','completed','failed_terminal','canceled','effect_unknown'
    )),
    result_digest TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(conversation_id,task_id)
);
"""
SCHEMA_V1 = (
    BASE_SCHEMA
    + A2A_SCHEMA
    + VERSIONING_SCHEMA
    + TASK_CUSTODY_SCHEMA
    + WORKLOAD_SCHEMA
    + IDENTITY_SCHEMA
    + SUPERVISOR_SCHEMA
    + AUTHORITY_BOOTSTRAP_SCHEMA
    + IPC_SCHEMA
    + EFFECT_LIFECYCLE_SCHEMA
    + CREDENTIAL_RECOVERY_SCHEMA
    + ARTIFACT_LIFECYCLE_SCHEMA
    + ARTIFACT_QUOTA_SCHEMA
    + OPERATIONAL_CONTROL_SCHEMA
    + RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA
    + POST_AUDIT_SCHEMA
    + RESPONSE_OBLIGATION_SCHEMA
)
SCHEMA_V2 = SCHEMA_V1 + TASK_PAYLOAD_RELEASE_SCHEMA
SCHEMA_V3 = SCHEMA_V2 + GUIDED_ENROLLMENT_SCHEMA
SCHEMA_V4 = SCHEMA_V3 + BOOTSTRAP_PLAN_SCHEMA
SCHEMA = SCHEMA_V4 + IDENTITY_LIFECYCLE_SCHEMA
_SQLITE_MIGRATION_SQL = {
    TASK_PAYLOAD_RELEASE_SCHEMA_VERSION: TASK_PAYLOAD_RELEASE_SCHEMA,
    GUIDED_ENROLLMENT_SCHEMA_VERSION: GUIDED_ENROLLMENT_SCHEMA,
    BOOTSTRAP_PLAN_SCHEMA_VERSION: BOOTSTRAP_PLAN_SCHEMA,
    IDENTITY_LIFECYCLE_SCHEMA_VERSION: IDENTITY_LIFECYCLE_SCHEMA,
}

_SCHEMA_CATALOG_QUERY = (
    "SELECT type,name,tbl_name,sql FROM sqlite_master "
    "WHERE type IN ('table','index','trigger') AND sql IS NOT NULL "
    "AND name NOT LIKE 'sqlite_%' ORDER BY type,name"
)


def _schema_catalog(connection: sqlite3.Connection) -> tuple[tuple[str, str, str, str], ...]:
    return tuple(
        (str(row[0]), str(row[1]), str(row[2]), str(row[3]))
        for row in connection.execute(_SCHEMA_CATALOG_QUERY).fetchall()
    )


@lru_cache(maxsize=5)
def _expected_schema_catalog(
    schema_version: int = BOOTSTRAP_PLAN_SCHEMA_VERSION,
) -> tuple[tuple[str, str, str, str], ...]:
    schemas = {
        1: SCHEMA_V1,
        TASK_PAYLOAD_RELEASE_SCHEMA_VERSION: SCHEMA_V2,
        GUIDED_ENROLLMENT_SCHEMA_VERSION: SCHEMA_V3,
        BOOTSTRAP_PLAN_SCHEMA_VERSION: SCHEMA_V4,
        IDENTITY_LIFECYCLE_SCHEMA_VERSION: SCHEMA,
    }
    schema = schemas.get(schema_version)
    if schema is None:
        raise GateBlocked("schema_version", "SQLite schema version is unsupported")
    reference = sqlite3.connect(":memory:", isolation_level=None)
    try:
        reference.executescript(schema)
        return _schema_catalog(reference)
    finally:
        reference.close()


class SQLiteStore:
    backend_name = "sqlite"
    def __init__(self, path: Path, cipher: LocalEnvelopeCipher) -> None:
        from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS

        path = path.absolute()
        platform_name = host_platform()
        directory: int | None = None
        if platform_name == "windows":
            from agentnet.windows_security import ensure_private_directory

            ensure_private_directory(path.parent)
        else:
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            directory_flags = (
                os.O_RDONLY
                | getattr(os, "O_DIRECTORY", 0)
                | getattr(os, "O_NOFOLLOW", 0)
            )
            try:
                directory = os.open(path.parent, directory_flags)
            except OSError as exc:
                raise GateBlocked(
                    "sqlite_path",
                    "SQLite state directory must be owner-only and not a symlink",
                ) from exc
            parent = os.fstat(directory)
            current_parent = path.parent.lstat()
            if (
                not stat.S_ISDIR(parent.st_mode)
                or parent.st_uid != os.geteuid()
                or parent.st_mode & 0o077
                or (parent.st_dev, parent.st_ino)
                != (current_parent.st_dev, current_parent.st_ino)
            ):
                os.close(directory)
                raise GateBlocked(
                    "sqlite_path",
                    "SQLite state directory must be owner-only and not a symlink",
                )

        def stat_entry(name: str) -> os.stat_result:
            if directory is not None:
                return os.stat(name, dir_fd=directory, follow_symlinks=False)
            candidate = path.parent / name
            if candidate.is_symlink():
                raise GateBlocked("sqlite_path", "SQLite state path cannot be a link")
            return candidate.lstat()

        def unlink_entry(name: str) -> None:
            if directory is not None:
                os.unlink(name, dir_fd=directory)
            else:
                (path.parent / name).unlink()

        def open_entry(name: str, flags: int, mode: int = 0o600) -> int:
            if directory is not None:
                return os.open(name, flags | getattr(os, "O_NOFOLLOW", 0), mode, dir_fd=directory)
            candidate = path.parent / name
            if os.path.lexists(candidate) and candidate.is_symlink():
                raise GateBlocked("sqlite_path", "SQLite state path cannot be a link")
            return os.open(candidate, flags | getattr(os, "O_BINARY", 0), mode)

        def sqlite_uri(*, mode: str, immutable: bool = False) -> str:
            if platform_name == "linux":
                assert descriptor is not None
                base = f"file:/proc/self/fd/{descriptor}"
            else:
                base = path.as_uri()
            suffix = f"?mode={mode}"
            if immutable:
                suffix += "&immutable=1"
            return base + suffix

        def secure_entry(name: str, *, missing_ok: bool = False) -> None:
            candidate = path.parent / name
            if platform_name == "windows":
                from agentnet.windows_security import apply_private_dacl, require_private_path

                if missing_ok and not candidate.exists():
                    # WAL/SHM creation is lazy. Any later sidecar inherits the
                    # already-protected parent DACL.
                    return
                apply_private_dacl(candidate, directory=False)
                require_private_path(candidate, directory=False)
            else:
                try:
                    metadata = stat_entry(name)
                except FileNotFoundError:
                    if missing_ok:
                        return
                    raise
                if (
                    not stat.S_ISREG(metadata.st_mode)
                    or metadata.st_uid != os.geteuid()
                    or metadata.st_nlink != 1
                    or metadata.st_mode & 0o077
                ):
                    raise GateBlocked(
                        "sqlite_path",
                        "existing SQLite database must already be owner-only",
                    )

        descriptor: int | None = None
        connection: sqlite3.Connection | None = None
        created_identity: tuple[int, int] | None = None
        created = False

        def cleanup_created_file() -> None:
            if created_identity is None:
                return
            try:
                current = stat_entry(path.name)
            except FileNotFoundError:
                return
            if (current.st_dev, current.st_ino) != created_identity:
                return
            for suffix in ("-wal", "-shm", "-journal"):
                name = path.name + suffix
                try:
                    sidecar = stat_entry(name)
                except FileNotFoundError:
                    continue
                if stat.S_ISREG(sidecar.st_mode) and sidecar.st_nlink == 1:
                    current_sidecar = stat_entry(name)
                    if (current_sidecar.st_dev, current_sidecar.st_ino) == (
                        sidecar.st_dev,
                        sidecar.st_ino,
                    ):
                        unlink_entry(name)
            current = stat_entry(path.name)
            if (current.st_dev, current.st_ino) == created_identity:
                unlink_entry(path.name)

        try:
            try:
                descriptor = open_entry(path.name, os.O_RDWR)
            except FileNotFoundError:
                descriptor = open_entry(path.name, os.O_RDWR | os.O_CREAT | os.O_EXCL)
                created = True
            existing = os.fstat(descriptor)
            if created:
                created_identity = (existing.st_dev, existing.st_ino)
                if platform_name == "windows":
                    secure_entry(path.name)
            if not stat.S_ISREG(existing.st_mode) or existing.st_nlink != 1:
                raise GateBlocked(
                    "sqlite_path",
                    "SQLite database must be a singly linked owner-owned regular file",
                )
            secure_entry(path.name)

            # Inspect an existing file through the pinned descriptor in
            # immutable read-only mode. Unknown/prototype bytes must be rejected
            # before chmod, journal-mode changes, sidecars, DDL, or metadata
            # writes can occur.
            initialize = False
            preflight: sqlite3.Connection | None = None
            try:
                preflight = sqlite3.connect(
                    sqlite_uri(mode="ro", immutable=True),
                    uri=True,
                    isolation_level=None,
                    check_same_thread=False,
                )
                preflight.row_factory = sqlite3.Row
                existing_tables = {
                    str(row["name"])
                    for row in preflight.execute(
                        "SELECT name FROM sqlite_master "
                        "WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                    ).fetchall()
                }
                try:
                    metadata = preflight.execute(
                        "SELECT value FROM metadata WHERE key='schema_version'"
                    ).fetchone()
                except sqlite3.OperationalError as exc:
                    if "no such table" not in str(exc):
                        raise
                    metadata = None
                try:
                    catalog_rows = preflight.execute(
                        "SELECT version,name,checksum FROM installed_migration_catalog "
                        "ORDER BY version"
                    ).fetchall()
                except sqlite3.OperationalError as exc:
                    if "no such table" not in str(exc):
                        raise
                    catalog_rows = []
                try:
                    existing_version = int(metadata["value"]) if metadata is not None else 0
                    catalog_versions = [int(row["version"]) for row in catalog_rows]
                except (TypeError, ValueError) as exc:
                    raise GateBlocked(
                        "schema_preflight",
                        "SQLite schema version is malformed",
                    ) from exc
                if existing_version > CURRENT_SCHEMA_VERSION or any(
                    version > CURRENT_SCHEMA_VERSION for version in catalog_versions
                ):
                    raise GateBlocked(
                        "schema_future",
                        "database schema is newer than this extension",
                    )
                if existing_version < 0:
                    raise GateBlocked("schema_preflight", "SQLite schema version is invalid")
                if existing_version == 0:
                    if existing_tables or catalog_rows:
                        raise GateBlocked(
                            "schema_legacy",
                            "pre-release SQLite databases require explicit export and clean reinitialization",
                        )
                    initialize = True
                elif existing_version not in {
                    CURRENT_SCHEMA_VERSION,
                    CURRENT_SCHEMA_VERSION - 1,
                }:
                    raise GateBlocked(
                        "schema_legacy",
                        "SQLite database is outside the exact supported N/N-1 migration window",
                    )
                if not initialize:
                    if len(catalog_rows) != existing_version:
                        raise GateBlocked(
                            "schema_migration_history",
                            "SQLite metadata and migration history are inconsistent",
                        )
                    for offset, (row, version) in enumerate(
                        zip(catalog_rows, catalog_versions, strict=True),
                        start=1,
                    ):
                        if version != offset:
                            raise GateBlocked(
                                "schema_migration_history",
                                "SQLite migration history is not contiguous",
                            )
                        expected = MIGRATIONS[offset - 1]
                        if row["name"] != expected.name or row["checksum"] != expected.checksum:
                            raise GateBlocked(
                                "schema_migration_history",
                                "SQLite migration history checksum is invalid",
                            )
                    if _schema_catalog(preflight) != _expected_schema_catalog(existing_version):
                        raise GateBlocked(
                            "schema_preflight",
                            "SQLite schema objects differ from the immutable versioned catalog",
                        )
            except GateBlocked:
                raise
            except (sqlite3.DatabaseError, OSError) as exc:
                raise GateBlocked(
                    "schema_preflight",
                    "SQLite schema could not be verified without mutation",
                ) from exc
            finally:
                if preflight is not None:
                    preflight.close()

            current_file = stat_entry(path.name)
            pinned_file = os.fstat(descriptor)
            if (
                (current_file.st_dev, current_file.st_ino)
                != (existing.st_dev, existing.st_ino)
                or (pinned_file.st_dev, pinned_file.st_ino)
                != (existing.st_dev, existing.st_ino)
                or current_file.st_nlink != 1
                or pinned_file.st_nlink != 1
            ):
                raise GateBlocked(
                    "sqlite_path",
                    "SQLite database path changed while it was opened",
                )
            connection = sqlite3.connect(
                sqlite_uri(mode="rw"),
                uri=True,
                isolation_level=None,
                check_same_thread=False,
            )
            connection.row_factory = sqlite3.Row
            current_file = stat_entry(path.name)
            connected = os.fstat(descriptor)
            if (
                (current_file.st_dev, current_file.st_ino)
                != (existing.st_dev, existing.st_ino)
                or (connected.st_dev, connected.st_ino)
                != (existing.st_dev, existing.st_ino)
                or current_file.st_nlink != 1
                or connected.st_nlink != 1
            ):
                raise GateBlocked(
                    "sqlite_path",
                    "SQLite database path changed while it was opened",
                )
            connection.execute("PRAGMA journal_mode=WAL")
            for suffix in ("-wal", "-shm"):
                secure_entry(path.name + suffix, missing_ok=True)
            connection.execute("PRAGMA synchronous=FULL")
            connection.execute("PRAGMA foreign_keys=ON")
            connection.execute("PRAGMA busy_timeout=5000")
            if initialize:
                # executescript only commits implicitly when no transaction is
                # active. Starting the transaction inside the script leaves it
                # open for parameterized catalog finalization.
                connection.executescript("BEGIN IMMEDIATE;\n" + SCHEMA)
                connection.execute(
                    """INSERT INTO metadata(key,value) VALUES('schema_version',?)
                       ON CONFLICT(key) DO UPDATE SET value=excluded.value""",
                    (str(CURRENT_SCHEMA_VERSION),),
                )
                connection.executemany(
                    """INSERT INTO installed_migration_catalog(version,name,checksum)
                       VALUES(?,?,?) ON CONFLICT(version) DO NOTHING""",
                    tuple(
                        (migration.version, migration.name, migration.checksum)
                        for migration in MIGRATIONS
                    ),
                )
                connection.execute("COMMIT")
                connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            elif existing_version < CURRENT_SCHEMA_VERSION:
                pending = MIGRATIONS[existing_version:]
                try:
                    migration_sql = "\n".join(
                        _SQLITE_MIGRATION_SQL[migration.version]
                        for migration in pending
                    )
                except KeyError as exc:
                    raise GateBlocked(
                        "schema_migration",
                        "SQLite migration SQL is unavailable for the exact supported upgrade",
                    ) from exc
                connection.executescript("BEGIN IMMEDIATE;\n" + migration_sql)
                connection.execute(
                    "UPDATE metadata SET value=? WHERE key='schema_version'",
                    (str(CURRENT_SCHEMA_VERSION),),
                )
                connection.executemany(
                    """INSERT INTO installed_migration_catalog(version,name,checksum)
                       VALUES(?,?,?)""",
                    tuple(
                        (migration.version, migration.name, migration.checksum)
                        for migration in pending
                    ),
                )
                if _schema_catalog(connection) != _expected_schema_catalog(
                    CURRENT_SCHEMA_VERSION
                ):
                    raise GateBlocked(
                        "schema_migration",
                        "SQLite migration did not produce the exact current catalog",
                    )
                connection.execute("COMMIT")
                connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            if platform_name == "windows":
                # Initialization/migration may create sidecars after WAL mode
                # is selected. Re-apply and verify any sidecar that now exists.
                for suffix in ("-wal", "-shm"):
                    secure_entry(path.name + suffix, missing_ok=True)
            self.path = path
            self.cipher = cipher
            self._connection = connection
            self._lock = threading.RLock()
        except Exception:
            if connection is not None:
                if connection.in_transaction:
                    connection.execute("ROLLBACK")
                connection.close()
                connection = None
            if descriptor is not None:
                os.close(descriptor)
                descriptor = None
            cleanup_created_file()
            if directory is not None:
                os.close(directory)
            raise
        assert descriptor is not None and connection is not None
        os.close(descriptor)
        if directory is not None:
            os.close(directory)

    @contextmanager
    def transaction(self, *, immediate: bool = True) -> Iterator[sqlite3.Connection]:
        with self._lock:
            self._connection.execute("BEGIN IMMEDIATE" if immediate else "BEGIN")
            try:
                yield self._connection
            except Exception:
                self._connection.execute("ROLLBACK")
                raise
            else:
                self._connection.execute("COMMIT")

    def close(self) -> None:
        with self._lock:
            self._connection.close()

    def fetch_one(self, query: str, parameters: tuple[Any, ...] = ()) -> sqlite3.Row | None:
        with self._lock:
            return self._connection.execute(query, parameters).fetchone()

    def fetch_all(self, query: str, parameters: tuple[Any, ...] = ()) -> list[sqlite3.Row]:
        with self._lock:
            return list(self._connection.execute(query, parameters).fetchall())

    def consume_once(self, actor_id: str, nonce: str, *, expires_at: int) -> None:
        nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        now = int(time.time())
        with self.transaction() as connection:
            connection.execute("DELETE FROM replay_nonces WHERE expires_at < ?", (now,))
            try:
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (actor_id, nonce_hash, expires_at),
                )
            except sqlite3.IntegrityError as exc:
                raise ReplayError("proof nonce was already consumed") from exc

    def enforce_idempotency(self, *, domain_id: str, actor_json: str, idempotency_key: str, digest: str) -> sqlite3.Row | None:
        row = self.fetch_one(
            "SELECT * FROM events WHERE domain_id=? AND actor_json=? AND idempotency_key=?",
            (domain_id, actor_json, idempotency_key),
        )
        if row is not None and row["envelope_digest"] != digest:
            raise IdempotencyConflict("same idempotency key was used with different bytes")
        return row

    def append_audit(self, connection: sqlite3.Connection, record: Mapping[str, Any]) -> str:
        previous = connection.execute("SELECT record_hash FROM audit_log ORDER BY sequence DESC LIMIT 1").fetchone()
        previous_hash = previous["record_hash"] if previous else "0" * 64
        occurred_at = int(time.time())
        serialized = canonical_json(dict(record)).decode("utf-8")
        preimage = previous_hash.encode("ascii") + b"\x00" + str(occurred_at).encode("ascii") + b"\x00" + serialized.encode("utf-8")
        record_hash = hashlib.sha256(preimage).hexdigest()
        connection.execute(
            "INSERT INTO audit_log(occurred_at,record_json,previous_hash,record_hash) VALUES(?,?,?,?)",
            (occurred_at, serialized, previous_hash, record_hash),
        )
        return record_hash

    def verify_audit_chain(self) -> tuple[bool, int]:
        rows = self.fetch_all("SELECT * FROM audit_log ORDER BY sequence")
        previous_hash = "0" * 64
        for row in rows:
            preimage = (
                previous_hash.encode("ascii")
                + b"\x00"
                + str(row["occurred_at"]).encode("ascii")
                + b"\x00"
                + row["record_json"].encode("utf-8")
            )
            expected = hashlib.sha256(preimage).hexdigest()
            if row["previous_hash"] != previous_hash or row["record_hash"] != expected:
                return False, row["sequence"]
            previous_hash = row["record_hash"]
        return True, len(rows)

    def encrypted_payload(self, payload: Mapping[str, Any], event_id: str) -> str:
        return self.cipher.encrypt_json(dict(payload), purpose=f"event:{event_id}")

    def decrypted_payload(self, token: str, event_id: str) -> dict[str, Any]:
        value = self.cipher.decrypt_json(token, purpose=f"event:{event_id}")
        if not isinstance(value, dict):
            raise TypeError("event payload is not an object")
        return value

    def readiness(self) -> dict[str, Any]:
        try:
            row = self.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            integrity = self.fetch_one("PRAGMA integrity_check")
        except Exception as exc:
            return {"ready": False, "backend": self.backend_name, "reason": type(exc).__name__}
        return {
            "ready": bool(row and integrity and integrity[0] == "ok"),
            "backend": self.backend_name,
            "schema_version": int(row["value"]) if row else 0,
            "durability_claim": "local_conformance_only",
            "ha_claimed": False,
        }
