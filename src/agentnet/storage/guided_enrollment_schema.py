"""Durable continuation state for guided OIDC enrollment."""

from __future__ import annotations


GUIDED_ENROLLMENT_SCHEMA_VERSION = 3

GUIDED_ENROLLMENT_SCHEMA = """
CREATE TABLE IF NOT EXISTS oidc_enrollment_continuations (
    transaction_id TEXT PRIMARY KEY
        REFERENCES oidc_enrollment_transactions(transaction_id) ON DELETE RESTRICT,
    continuation_hash TEXT NOT NULL UNIQUE CHECK(length(continuation_hash)=64),
    status TEXT NOT NULL CHECK(status IN (
        'awaiting_oidc','callback_ready','approval_pending','enrolled','expired','failed'
    )),
    challenge_encrypted TEXT,
    approval_request_id TEXT,
    approval_transaction_digest TEXT CHECK(
        approval_transaction_digest IS NULL OR length(approval_transaction_digest)=64
    ),
    approval_request_expires_at INTEGER,
    completion_request_digest TEXT CHECK(
        completion_request_digest IS NULL OR length(completion_request_digest)=64
    ),
    completion_response_encrypted TEXT,
    poll_after_at INTEGER NOT NULL,
    poll_interval_seconds INTEGER NOT NULL CHECK(
        poll_interval_seconds BETWEEN 2 AND 10
    ),
    poll_count INTEGER NOT NULL DEFAULT 0 CHECK(poll_count BETWEEN 0 AND 60),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_continuation_status_expiry
    ON oidc_enrollment_continuations(status,expires_at,transaction_id);
"""

GUIDED_ENROLLMENT_REQUIRED_TABLES = frozenset({"oidc_enrollment_continuations"})
GUIDED_ENROLLMENT_REQUIRED_INDEXES = frozenset({"idx_oidc_continuation_status_expiry"})


__all__ = [
    "GUIDED_ENROLLMENT_REQUIRED_INDEXES",
    "GUIDED_ENROLLMENT_REQUIRED_TABLES",
    "GUIDED_ENROLLMENT_SCHEMA",
    "GUIDED_ENROLLMENT_SCHEMA_VERSION",
]
