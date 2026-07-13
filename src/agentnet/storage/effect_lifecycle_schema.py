"""Durable protected-effect execution, uncertainty, and reconciliation state."""

from __future__ import annotations


EFFECT_LIFECYCLE_SCHEMA_VERSION = 1

EFFECT_LIFECYCLE_SCHEMA = """
CREATE TABLE IF NOT EXISTS effect_lifecycle (
    effect_id TEXT PRIMARY KEY REFERENCES effect_reservations(effect_id),
    event_id TEXT NOT NULL REFERENCES events(event_id),
    grant_id TEXT NOT NULL REFERENCES task_grants(grant_id),
    fence INTEGER NOT NULL CHECK (fence > 0),
    attempt_id TEXT NOT NULL,
    executor_registration_id TEXT NOT NULL REFERENCES workload_registrations(registration_id),
    executor_actor_json TEXT NOT NULL,
    execution_evidence_digest TEXT NOT NULL,
    execution_started_at INTEGER NOT NULL,
    current_state TEXT NOT NULL CHECK (current_state IN (
        'effect_executing','effect_unknown','effect_succeeded','effect_failed','effect_cancelled'
    )),
    uncertainty_evidence_digest TEXT,
    uncertainty_recorded_at INTEGER,
    terminal_evidence_digest TEXT,
    terminal_source TEXT CHECK (terminal_source IN ('executor_ack','reconciliation')),
    terminal_recorded_at INTEGER,
    reconciliation_evidence_digest TEXT,
    updated_at INTEGER NOT NULL,
    UNIQUE(event_id,grant_id,fence),
    CHECK (
        (current_state='effect_executing'
            AND uncertainty_evidence_digest IS NULL AND uncertainty_recorded_at IS NULL
            AND terminal_evidence_digest IS NULL AND terminal_source IS NULL
            AND terminal_recorded_at IS NULL AND reconciliation_evidence_digest IS NULL)
        OR
        (current_state='effect_unknown'
            AND uncertainty_evidence_digest IS NOT NULL AND uncertainty_recorded_at IS NOT NULL
            AND terminal_evidence_digest IS NULL AND terminal_source IS NULL
            AND terminal_recorded_at IS NULL AND reconciliation_evidence_digest IS NULL)
        OR
        (current_state IN ('effect_succeeded','effect_failed','effect_cancelled')
            AND terminal_evidence_digest IS NOT NULL AND terminal_source IS NOT NULL
            AND terminal_recorded_at IS NOT NULL
            AND (
                (terminal_source='executor_ack' AND uncertainty_evidence_digest IS NULL
                    AND uncertainty_recorded_at IS NULL AND reconciliation_evidence_digest IS NULL)
                OR
                (terminal_source='reconciliation' AND uncertainty_evidence_digest IS NOT NULL
                    AND uncertainty_recorded_at IS NOT NULL
                    AND reconciliation_evidence_digest IS NOT NULL)
            ))
    )
);
CREATE INDEX IF NOT EXISTS idx_effect_lifecycle_executor_state
    ON effect_lifecycle(executor_registration_id,current_state,updated_at,effect_id);
"""

EFFECT_LIFECYCLE_REQUIRED_TABLES = frozenset({"effect_lifecycle"})
EFFECT_LIFECYCLE_REQUIRED_INDEXES = frozenset({"idx_effect_lifecycle_executor_state"})


__all__ = [
    "EFFECT_LIFECYCLE_REQUIRED_INDEXES",
    "EFFECT_LIFECYCLE_REQUIRED_TABLES",
    "EFFECT_LIFECYCLE_SCHEMA",
    "EFFECT_LIFECYCLE_SCHEMA_VERSION",
]
