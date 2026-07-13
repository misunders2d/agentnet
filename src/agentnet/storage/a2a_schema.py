"""Canonical native A2A tables and fail-closed runtime schema verification."""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


A2A_SCHEMA_VERSION = 1

A2A_SCHEMA = """
CREATE TABLE IF NOT EXISTS a2a_ingress_keys (
    tenant TEXT NOT NULL,
    owner_namespace TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    response_kind TEXT NOT NULL CHECK (response_kind IN ('task','message')),
    response_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(tenant,owner_namespace,idempotency_key)
);
CREATE TABLE IF NOT EXISTS a2a_tasks (
    task_id TEXT PRIMARY KEY,
    context_id TEXT NOT NULL,
    tenant TEXT NOT NULL,
    owner_namespace TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    source_message_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    executable INTEGER NOT NULL CHECK (executable IN (0,1)),
    recipient_id TEXT,
    task_grant_id TEXT,
    corporate_event_id TEXT,
    policy_revision INTEGER NOT NULL,
    state INTEGER NOT NULL,
    task_encrypted TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_a2a_tasks_owner
    ON a2a_tasks(tenant,owner_namespace,updated_at,task_id);
CREATE TABLE IF NOT EXISTS a2a_task_events (
    task_id TEXT NOT NULL REFERENCES a2a_tasks(task_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    event_encrypted TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(task_id,sequence)
);
CREATE TABLE IF NOT EXISTS a2a_messages (
    response_message_id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    owner_namespace TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    source_message_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    corporate_event_id TEXT NOT NULL,
    response_encrypted TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS a2a_callbacks (
    task_id TEXT NOT NULL REFERENCES a2a_tasks(task_id) ON DELETE CASCADE,
    config_id TEXT NOT NULL,
    owner_namespace TEXT NOT NULL,
    url_hash TEXT NOT NULL,
    config_encrypted TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(task_id,config_id)
);
CREATE TABLE IF NOT EXISTS a2a_outbound_exchanges (
    exchange_id TEXT PRIMARY KEY,
    peer_namespace TEXT NOT NULL,
    tenant TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    request_encrypted TEXT NOT NULL,
    state TEXT NOT NULL,
    remote_task_id TEXT,
    last_variant TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(peer_namespace,tenant,idempotency_key)
);
CREATE TABLE IF NOT EXISTS a2a_outbound_events (
    exchange_id TEXT NOT NULL REFERENCES a2a_outbound_exchanges(exchange_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    response_encrypted TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(exchange_id,sequence)
);
"""

A2A_REQUIRED_TABLES = frozenset(
    {
        "a2a_ingress_keys",
        "a2a_tasks",
        "a2a_task_events",
        "a2a_messages",
        "a2a_callbacks",
        "a2a_outbound_exchanges",
        "a2a_outbound_events",
    }
)
A2A_REQUIRED_INDEXES = frozenset({"idx_a2a_tasks_owner"})


def require_a2a_schema(store: Any) -> None:
    """Verify the canonical A2A migration without creating service-owned DDL."""

    backend = getattr(store, "backend_name", "")
    try:
        if backend == "sqlite":
            missing_tables = {
                name
                for name in A2A_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
                    (name,),
                )
                is None
            }
            missing_indexes = {
                name
                for name in A2A_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?",
                    (name,),
                )
                is None
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            if metadata is None or int(metadata["value"]) < A2A_SCHEMA_VERSION:
                raise GateBlocked("a2a_schema", "native A2A SQLite schema version is not current")
        elif backend == "postgresql":
            missing_tables = {
                name
                for name in A2A_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in A2A_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            migrations = store.fetch_one("SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations")
            from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION

            if (
                metadata is None
                or int(metadata["value"]) != CURRENT_SCHEMA_VERSION
                or migrations is None
                or int(migrations["version"]) != CURRENT_SCHEMA_VERSION
                or CURRENT_SCHEMA_VERSION < A2A_SCHEMA_VERSION
            ):
                raise GateBlocked("a2a_schema", "native A2A numbered migration is not current")
        else:
            raise GateBlocked("a2a_schema", "native A2A storage backend is unsupported")
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("a2a_schema", "native A2A schema could not be verified") from exc
    if missing_tables or missing_indexes:
        raise GateBlocked("a2a_schema", "native A2A schema is missing required relations")


__all__ = [
    "A2A_REQUIRED_INDEXES",
    "A2A_REQUIRED_TABLES",
    "A2A_SCHEMA",
    "A2A_SCHEMA_VERSION",
    "require_a2a_schema",
]
