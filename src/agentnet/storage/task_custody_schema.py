"""Durable directional task-custody proposal schema.

Pending proposals deliberately live outside ``events`` and ``recipients``.
Consequently neither mailbox reconciliation nor a background model worker can
observe task content before the recipient owner approves the exact digest.
"""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


TASK_CUSTODY_SCHEMA_VERSION = 1

TASK_CUSTODY_SCHEMA = """
CREATE TABLE IF NOT EXISTS task_custody_proposals (
    proposal_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    sender_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    sender_authority_id TEXT NOT NULL,
    recipient_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    recipient_authority_id TEXT NOT NULL,
    ingress_kind TEXT NOT NULL CHECK (ingress_kind IN (
        'direct','conversation_task','conversation_handoff','a2a_task',
        'relay_task','room_task','federation_task','semantic_worker'
    )),
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    event_digest TEXT NOT NULL,
    request_encrypted TEXT NOT NULL,
    event_encrypted TEXT NOT NULL,
    continuation_encrypted TEXT NOT NULL,
    redacted_summary_json TEXT NOT NULL,
    policy_revision INTEGER NOT NULL,
    domain_revocation_epoch INTEGER NOT NULL,
    sender_credential_epoch INTEGER NOT NULL,
    recipient_credential_epoch INTEGER NOT NULL,
    relationship_id TEXT,
    relationship_revision INTEGER NOT NULL,
    relationship_digest TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN (
        'pending','resumed','denied','expired','invalidated'
    )),
    state_reason TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    decided_at INTEGER,
    approval_actor_json TEXT,
    approval_digest TEXT,
    resumed_event_id TEXT,
    UNIQUE(domain_id,sender_harness_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_task_custody_owner_pending
    ON task_custody_proposals(
        domain_id,recipient_authority_id,state,expires_at,created_at,proposal_id
    );
CREATE INDEX IF NOT EXISTS idx_task_custody_event_digest
    ON task_custody_proposals(event_digest);
"""

TASK_CUSTODY_REQUIRED_TABLES = frozenset({"task_custody_proposals"})
TASK_CUSTODY_REQUIRED_INDEXES = frozenset(
    {"idx_task_custody_owner_pending", "idx_task_custody_event_digest"}
)


def require_task_custody_schema(store: Any) -> None:
    """Fail closed unless migration 6 and every custody relation are present."""

    backend = getattr(store, "backend_name", "")
    try:
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if metadata is None or int(metadata["value"]) < TASK_CUSTODY_SCHEMA_VERSION:
            raise GateBlocked("task_custody_schema", "directional task-custody schema is not current")
        if backend == "sqlite":
            missing_tables = {
                name
                for name in TASK_CUSTODY_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
                )
                is None
            }
            missing_indexes = {
                name
                for name in TASK_CUSTODY_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?", (name,)
                )
                is None
            }
        elif backend == "postgresql":
            migrations = store.fetch_one("SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations")
            if migrations is None or int(migrations["version"]) < TASK_CUSTODY_SCHEMA_VERSION:
                raise GateBlocked("task_custody_schema", "directional task-custody migration is not current")
            missing_tables = {
                name
                for name in TASK_CUSTODY_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in TASK_CUSTODY_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
        else:
            raise GateBlocked("task_custody_schema", "directional task-custody backend is unsupported")
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("task_custody_schema", "directional task-custody schema could not be verified") from exc
    if missing_tables or missing_indexes:
        raise GateBlocked("task_custody_schema", "directional task-custody relations are missing")


__all__ = [
    "TASK_CUSTODY_REQUIRED_INDEXES",
    "TASK_CUSTODY_REQUIRED_TABLES",
    "TASK_CUSTODY_SCHEMA",
    "TASK_CUSTODY_SCHEMA_VERSION",
    "require_task_custody_schema",
]
