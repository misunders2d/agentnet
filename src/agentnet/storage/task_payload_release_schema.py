"""Durable protected task-payload disclosure receipts."""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


TASK_PAYLOAD_RELEASE_SCHEMA_VERSION = 2

TASK_PAYLOAD_RELEASE_SCHEMA = """
CREATE TABLE IF NOT EXISTS task_payload_releases (
    event_id TEXT NOT NULL,
    recipient_harness_id TEXT NOT NULL,
    release_receipt_id TEXT NOT NULL UNIQUE,
    release_request_digest TEXT NOT NULL,
    authorization_digest TEXT NOT NULL,
    local_queue_id TEXT NOT NULL,
    task_grant_id TEXT NOT NULL REFERENCES task_grants(grant_id),
    policy_decision_id TEXT NOT NULL REFERENCES policy_decisions(decision_id),
    intent_digest TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    envelope_digest TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    recipient_credential_epoch INTEGER NOT NULL CHECK (recipient_credential_epoch > 0),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch > 0),
    release_expires_at INTEGER NOT NULL,
    released_at INTEGER NOT NULL,
    PRIMARY KEY(event_id,recipient_harness_id),
    FOREIGN KEY(event_id,recipient_harness_id)
        REFERENCES supervisor_executions(event_id,recipient_harness_id),
    CHECK (release_expires_at > released_at)
);
CREATE INDEX IF NOT EXISTS idx_task_payload_releases_recipient
    ON task_payload_releases(recipient_harness_id,released_at,event_id);
"""

TASK_PAYLOAD_RELEASE_REQUIRED_TABLES = frozenset({"task_payload_releases"})
TASK_PAYLOAD_RELEASE_REQUIRED_INDEXES = frozenset(
    {"idx_task_payload_releases_recipient"}
)


def require_task_payload_release_schema(store: Any) -> None:
    """Fail closed unless the exact protected-release relations are installed."""

    backend = getattr(store, "backend_name", "")
    try:
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if metadata is None or int(metadata["value"]) < TASK_PAYLOAD_RELEASE_SCHEMA_VERSION:
            raise GateBlocked(
                "task_payload_release_schema",
                "protected task-payload release schema is not current",
            )
        if backend == "sqlite":
            missing_tables = {
                name
                for name in TASK_PAYLOAD_RELEASE_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
                    (name,),
                )
                is None
            }
            missing_indexes = {
                name
                for name in TASK_PAYLOAD_RELEASE_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?",
                    (name,),
                )
                is None
            }
        elif backend == "postgresql":
            migrations = store.fetch_one(
                "SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations"
            )
            if (
                migrations is None
                or int(migrations["version"]) < TASK_PAYLOAD_RELEASE_SCHEMA_VERSION
            ):
                raise GateBlocked(
                    "task_payload_release_schema",
                    "protected task-payload release migration is not current",
                )
            missing_tables = {
                name
                for name in TASK_PAYLOAD_RELEASE_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in TASK_PAYLOAD_RELEASE_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
        else:
            raise GateBlocked(
                "task_payload_release_schema",
                "protected task-payload release backend is unsupported",
            )
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked(
            "task_payload_release_schema",
            "protected task-payload release schema could not be verified",
        ) from exc
    if missing_tables or missing_indexes:
        raise GateBlocked(
            "task_payload_release_schema",
            "protected task-payload release relations are missing",
        )


__all__ = [
    "TASK_PAYLOAD_RELEASE_REQUIRED_INDEXES",
    "TASK_PAYLOAD_RELEASE_REQUIRED_TABLES",
    "TASK_PAYLOAD_RELEASE_SCHEMA",
    "TASK_PAYLOAD_RELEASE_SCHEMA_VERSION",
    "require_task_payload_release_schema",
]
