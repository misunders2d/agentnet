"""Crash-recoverable exact-recipient file-transfer coordination state."""

from __future__ import annotations

from agentnet.errors import GateBlocked
from agentnet.storage.artifact_lifecycle_schema import (
    ARTIFACT_LIFECYCLE_REQUIRED_INDEXES,
    ARTIFACT_LIFECYCLE_REQUIRED_TABLES,
)
from agentnet.storage.artifact_quota_schema import (
    ARTIFACT_QUOTA_REQUIRED_INDEXES,
    ARTIFACT_QUOTA_REQUIRED_TABLES,
)
from agentnet.storage.backend import StoreBackend


ARTIFACT_TRANSFER_SCHEMA_VERSION = 7

ARTIFACT_TRANSFER_SCHEMA = """
CREATE TABLE IF NOT EXISTS artifact_transfers (
    transfer_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    collaboration_scope_id TEXT NOT NULL REFERENCES collaboration_scopes(scope_id),
    sender_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    sender_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    sender_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    sender_credential_epoch INTEGER NOT NULL CHECK (sender_credential_epoch >= 1),
    reservation_id TEXT NOT NULL UNIQUE REFERENCES artifact_reservations(reservation_id),
    artifact_id TEXT UNIQUE REFERENCES artifact_manifests(artifact_id),
    event_id TEXT UNIQUE REFERENCES events(event_id),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 16 AND 256),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    expected_digest TEXT NOT NULL CHECK (length(expected_digest) = 64),
    expected_size INTEGER NOT NULL CHECK (expected_size >= 0),
    media_type TEXT NOT NULL CHECK (length(media_type) BETWEEN 1 AND 255),
    classification TEXT NOT NULL CHECK (classification IN ('C0','C1','C2','C3')),
    recipient_count INTEGER NOT NULL CHECK (recipient_count >= 1),
    source_name_encrypted TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN (
        'reserved','quarantined','scanning','released','event_committed',
        'recipient_committed','failed','canceled'
    )),
    state_reason TEXT NOT NULL CHECK (length(state_reason) BETWEEN 1 AND 128),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    terminal_at INTEGER,
    UNIQUE (domain_id,sender_harness_id,idempotency_key),
    CHECK (updated_at >= created_at),
    CHECK (
        (state IN ('recipient_committed','failed','canceled') AND terminal_at IS NOT NULL)
        OR
        (state NOT IN ('recipient_committed','failed','canceled') AND terminal_at IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_artifact_transfers_sender
    ON artifact_transfers(domain_id,sender_harness_id,state,updated_at,transfer_id);
CREATE INDEX IF NOT EXISTS idx_artifact_transfers_recovery
    ON artifact_transfers(state,updated_at,transfer_id);

CREATE TABLE IF NOT EXISTS artifact_transfer_recipients (
    transfer_id TEXT NOT NULL REFERENCES artifact_transfers(transfer_id) ON DELETE RESTRICT,
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    custody_state TEXT NOT NULL CHECK (custody_state IN (
        'pending','event_committed','recipient_committed','acknowledged','failed'
    )),
    event_id TEXT REFERENCES events(event_id),
    state_reason TEXT NOT NULL CHECK (length(state_reason) BETWEEN 1 AND 128),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    updated_at INTEGER NOT NULL,
    committed_at INTEGER,
    acknowledged_at INTEGER,
    PRIMARY KEY (transfer_id,harness_id),
    CHECK (
        (custody_state = 'pending' AND event_id IS NULL AND committed_at IS NULL)
        OR
        (custody_state IN ('event_committed','recipient_committed','acknowledged','failed')
            AND event_id IS NOT NULL)
    ),
    CHECK ((custody_state = 'acknowledged') = (acknowledged_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_artifact_transfer_recipients_custody
    ON artifact_transfer_recipients(harness_id,custody_state,updated_at,transfer_id);
"""

ARTIFACT_TRANSFER_REQUIRED_TABLES = frozenset(
    {"artifact_transfers", "artifact_transfer_recipients"}
)
ARTIFACT_TRANSFER_REQUIRED_INDEXES = frozenset(
    {
        "idx_artifact_transfers_sender",
        "idx_artifact_transfers_recovery",
        "idx_artifact_transfer_recipients_custody",
    }
)

_ARTIFACT_ACCESS_REQUIRED_TABLES = frozenset(
    {
        "artifact_manifests",
        "artifact_release_outbox",
        "artifact_reservations",
        "audit_intents",
        "download_capabilities",
    }
).union(
    ARTIFACT_LIFECYCLE_REQUIRED_TABLES,
    ARTIFACT_QUOTA_REQUIRED_TABLES,
    ARTIFACT_TRANSFER_REQUIRED_TABLES,
)
_ARTIFACT_ACCESS_REQUIRED_INDEXES = frozenset().union(
    ARTIFACT_LIFECYCLE_REQUIRED_INDEXES,
    ARTIFACT_QUOTA_REQUIRED_INDEXES,
    ARTIFACT_TRANSFER_REQUIRED_INDEXES,
)



def require_current_artifact_schema(store: StoreBackend) -> None:
    """Require the exact released schema and artifact-transfer relations."""

    from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS

    try:
        backend = store.backend_name
        if backend == "sqlite":
            missing_tables = {
                name
                for name in _ARTIFACT_ACCESS_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
                    (name,),
                )
                is None
            }
            missing_indexes = {
                name
                for name in _ARTIFACT_ACCESS_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?",
                    (name,),
                )
                is None
            }
            catalog_rows = store.fetch_all(
                "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
            )
        elif backend == "postgresql":
            missing_tables = {
                name
                for name in _ARTIFACT_ACCESS_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in _ARTIFACT_ACCESS_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            catalog_rows = store.fetch_all(
                "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
            )
        else:
            raise GateBlocked(
                "artifact_schema",
                "artifact storage backend is unsupported",
            )
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        expected_catalog = [
            (migration.version, migration.name, migration.checksum) for migration in MIGRATIONS
        ]
        actual_catalog = [
            (int(row["version"]), str(row["name"]), str(row["checksum"]))
            for row in catalog_rows
        ]
        if (
            CURRENT_SCHEMA_VERSION != ARTIFACT_TRANSFER_SCHEMA_VERSION
            or metadata is None
            or int(metadata["value"]) != CURRENT_SCHEMA_VERSION
            or actual_catalog != expected_catalog
        ):
            raise GateBlocked(
                "artifact_schema",
                "artifact storage schema/catalog is not the exact current release",
            )
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked(
            "artifact_schema",
            "artifact storage schema/catalog could not be verified",
        ) from exc
    if missing_tables or missing_indexes:
        raise GateBlocked(
            "artifact_schema",
            "artifact storage schema/catalog is missing required relations",
        )


__all__ = [
    "ARTIFACT_TRANSFER_REQUIRED_INDEXES",
    "ARTIFACT_TRANSFER_REQUIRED_TABLES",
    "ARTIFACT_TRANSFER_SCHEMA",
    "ARTIFACT_TRANSFER_SCHEMA_VERSION",
    "require_current_artifact_schema",
]
