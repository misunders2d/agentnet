"""Durable autonomous-supervisor execution receipts."""

from __future__ import annotations


SUPERVISOR_SCHEMA_VERSION = 1

SUPERVISOR_SCHEMA = """
CREATE TABLE IF NOT EXISTS supervisor_executions (
    event_id TEXT NOT NULL REFERENCES events(event_id),
    recipient_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    envelope_digest TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    event_type TEXT NOT NULL,
    classification TEXT NOT NULL,
    task_grant_id TEXT NOT NULL REFERENCES task_grants(grant_id),
    policy_decision_id TEXT NOT NULL REFERENCES policy_decisions(decision_id),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    recipient_credential_epoch INTEGER NOT NULL CHECK (recipient_credential_epoch > 0),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch > 0),
    authorization_digest TEXT NOT NULL,
    authorization_expires_at INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('eligible','local_custody','result_uploaded')),
    custody_receipt_id TEXT,
    local_queue_id TEXT,
    custody_assertion_digest TEXT,
    custody_recorded_at INTEGER,
    result_receipt_id TEXT,
    result_digest TEXT,
    result_encrypted TEXT,
    result_provenance_digest TEXT,
    result_provenance_json TEXT,
    result_recorded_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(event_id,recipient_harness_id),
    UNIQUE(policy_decision_id),
    UNIQUE(custody_receipt_id),
    UNIQUE(result_receipt_id),
    CHECK (
        (state='eligible' AND custody_receipt_id IS NULL AND local_queue_id IS NULL
                          AND custody_assertion_digest IS NULL
                          AND custody_recorded_at IS NULL AND result_receipt_id IS NULL
                          AND result_digest IS NULL AND result_encrypted IS NULL
                          AND result_provenance_digest IS NULL
                          AND result_provenance_json IS NULL
                          AND result_recorded_at IS NULL)
        OR
        (state='local_custody' AND custody_receipt_id IS NOT NULL
                               AND local_queue_id IS NOT NULL
                               AND custody_assertion_digest IS NOT NULL
                               AND custody_recorded_at IS NOT NULL
                               AND result_receipt_id IS NULL AND result_digest IS NULL
                               AND result_encrypted IS NULL
                               AND result_provenance_digest IS NULL
                               AND result_provenance_json IS NULL
                               AND result_recorded_at IS NULL)
        OR
        (state='result_uploaded' AND custody_receipt_id IS NOT NULL
                                 AND local_queue_id IS NOT NULL
                                 AND custody_assertion_digest IS NOT NULL
                                 AND custody_recorded_at IS NOT NULL
                                 AND result_receipt_id IS NOT NULL
                                 AND result_digest IS NOT NULL
                                 AND result_encrypted IS NOT NULL
                                 AND result_provenance_digest IS NOT NULL
                                 AND result_provenance_json IS NOT NULL
                                 AND result_recorded_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_supervisor_execution_status
    ON supervisor_executions(recipient_harness_id,state,updated_at,event_id);
"""

SUPERVISOR_REQUIRED_TABLES = frozenset({"supervisor_executions"})
SUPERVISOR_REQUIRED_INDEXES = frozenset({"idx_supervisor_execution_status"})


__all__ = [
    "SUPERVISOR_REQUIRED_INDEXES",
    "SUPERVISOR_REQUIRED_TABLES",
    "SUPERVISOR_SCHEMA",
    "SUPERVISOR_SCHEMA_VERSION",
]
