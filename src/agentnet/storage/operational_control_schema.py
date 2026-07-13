"""Persistent privacy-safe telemetry and admission-control state."""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


OPERATIONAL_CONTROL_SCHEMA_VERSION = 1

OPERATIONAL_CONTROL_BASE_SCHEMA = """
CREATE TABLE IF NOT EXISTS telemetry_histograms (
    metric TEXT NOT NULL,
    outcome TEXT NOT NULL,
    bucket_upper_ms INTEGER NOT NULL,
    count INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(metric,outcome,bucket_upper_ms)
);
CREATE TABLE IF NOT EXISTS telemetry_gauges (
    metric TEXT PRIMARY KEY,
    value INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS admission_fairness (
    dimension TEXT NOT NULL,
    scope_hash TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    virtual_finish INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(dimension,scope_hash,window_start)
);
CREATE INDEX IF NOT EXISTS idx_admission_fairness_window
    ON admission_fairness(dimension,window_start,virtual_finish,scope_hash);
CREATE TABLE IF NOT EXISTS circuit_breakers (
    breaker_key TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('closed','open','half_open')),
    failure_count INTEGER NOT NULL,
    opened_at INTEGER,
    retry_after INTEGER,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS operation_loop_fences (
    operation_id_hash TEXT PRIMARY KEY,
    highest_hop INTEGER NOT NULL,
    max_hops INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_operation_loop_fences_expiry
    ON operation_loop_fences(expires_at,operation_id_hash);
"""

OPERATIONAL_WORK_SCHEMA = """
CREATE TABLE IF NOT EXISTS installed_migration_catalog (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS operational_work_reservations (
    work_kind TEXT NOT NULL CHECK (work_kind IN ('relay_outbound','relay_inbound','protected_effect')),
    source_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','terminal')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(work_kind,source_id)
);
CREATE INDEX IF NOT EXISTS idx_operational_work_pending
    ON operational_work_reservations(domain_id,state,work_kind,source_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_version_rollouts_target_migration
    ON version_rollouts(to_schema_version);
"""

OPERATIONAL_CONTROL_SCHEMA = OPERATIONAL_CONTROL_BASE_SCHEMA + OPERATIONAL_WORK_SCHEMA

OPERATIONAL_CONTROL_REQUIRED_TABLES = frozenset(
    {
        "telemetry_histograms",
        "telemetry_counters",
        "telemetry_gauges",
        "quota_counters",
        "admission_fairness",
        "circuit_breakers",
        "operation_loop_fences",
        "operational_work_reservations",
        "installed_migration_catalog",
    }
)
OPERATIONAL_CONTROL_REQUIRED_INDEXES = frozenset(
    {
        "idx_admission_fairness_window",
        "idx_operation_loop_fences_expiry",
        "idx_operational_work_pending",
        "idx_version_rollouts_target_migration",
    }
)


def require_operational_control_schema(store: Any) -> None:
    """Fail closed when the admission/telemetry migration is unavailable."""

    try:
        backend = getattr(store, "backend_name", "")
        if backend == "sqlite":
            missing_tables = {
                name
                for name in OPERATIONAL_CONTROL_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
                    (name,),
                )
                is None
            }
            missing_indexes = {
                name
                for name in OPERATIONAL_CONTROL_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?",
                    (name,),
                )
                is None
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            if metadata is None or int(metadata["value"]) < OPERATIONAL_CONTROL_SCHEMA_VERSION:
                raise GateBlocked(
                    "operational_control_schema",
                    "operational control SQLite migration is not current",
                )
        elif backend == "postgresql":
            missing_tables = {
                name
                for name in OPERATIONAL_CONTROL_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in OPERATIONAL_CONTROL_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            migrations = store.fetch_one(
                "SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations"
            )
            from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION

            if (
                CURRENT_SCHEMA_VERSION < OPERATIONAL_CONTROL_SCHEMA_VERSION
                or metadata is None
                or int(metadata["value"]) != CURRENT_SCHEMA_VERSION
                or migrations is None
                or int(migrations["version"]) != CURRENT_SCHEMA_VERSION
            ):
                raise GateBlocked(
                    "operational_control_schema",
                    "operational control PostgreSQL migration is not current",
                )
        else:
            raise GateBlocked(
                "operational_control_schema",
                "operational control storage backend is unsupported",
            )
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked(
            "operational_control_schema",
            "operational control schema could not be verified",
        ) from exc
    if missing_tables or missing_indexes:
        raise GateBlocked(
            "operational_control_schema",
            "operational control schema is missing required relations",
        )


__all__ = [
    "OPERATIONAL_CONTROL_REQUIRED_INDEXES",
    "OPERATIONAL_CONTROL_REQUIRED_TABLES",
    "OPERATIONAL_CONTROL_SCHEMA",
    "OPERATIONAL_CONTROL_BASE_SCHEMA",
    "OPERATIONAL_WORK_SCHEMA",
    "OPERATIONAL_CONTROL_SCHEMA_VERSION",
    "require_operational_control_schema",
]
