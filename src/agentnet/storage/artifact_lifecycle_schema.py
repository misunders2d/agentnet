"""Revisioned legal-hold and crash-recoverable artifact deletion state."""

from __future__ import annotations


ARTIFACT_LIFECYCLE_SCHEMA_VERSION = 1

ARTIFACT_LIFECYCLE_SCHEMA = """
CREATE TABLE IF NOT EXISTS artifact_lifecycle (
    artifact_id TEXT PRIMARY KEY REFERENCES artifact_manifests(artifact_id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    status TEXT NOT NULL CHECK (status IN ('active','deletion_pending','deleted')),
    legal_hold_at INTEGER,
    legal_hold_reason_encrypted TEXT,
    legal_hold_actor_json TEXT,
    deleted_at INTEGER,
    deletion_reason_encrypted TEXT,
    deletion_actor_json TEXT,
    updated_at INTEGER NOT NULL,
    CHECK (
        (legal_hold_at IS NULL AND legal_hold_reason_encrypted IS NULL AND legal_hold_actor_json IS NULL)
        OR
        (legal_hold_at IS NOT NULL AND legal_hold_reason_encrypted IS NOT NULL AND legal_hold_actor_json IS NOT NULL)
    )
);
CREATE TABLE IF NOT EXISTS artifact_deletion_outbox (
    outbox_id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL UNIQUE REFERENCES artifact_manifests(artifact_id),
    intent_id TEXT NOT NULL UNIQUE REFERENCES audit_intents(intent_id),
    object_key TEXT NOT NULL,
    object_version TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL,
    expected_revision INTEGER NOT NULL CHECK (expected_revision > 0),
    state TEXT NOT NULL CHECK (state IN ('pending','completed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_artifact_deletion_pending
    ON artifact_deletion_outbox(state,created_at,artifact_id);
"""

ARTIFACT_LIFECYCLE_REQUIRED_TABLES = frozenset(
    {"artifact_lifecycle", "artifact_deletion_outbox"}
)
ARTIFACT_LIFECYCLE_REQUIRED_INDEXES = frozenset({"idx_artifact_deletion_pending"})


__all__ = [
    "ARTIFACT_LIFECYCLE_REQUIRED_INDEXES",
    "ARTIFACT_LIFECYCLE_REQUIRED_TABLES",
    "ARTIFACT_LIFECYCLE_SCHEMA",
    "ARTIFACT_LIFECYCLE_SCHEMA_VERSION",
]
