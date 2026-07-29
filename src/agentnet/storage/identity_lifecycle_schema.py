"""Forward-only v5 identity idempotency and credential-renewal schema."""

from __future__ import annotations


IDENTITY_LIFECYCLE_SCHEMA_VERSION = 5

IDENTITY_LIFECYCLE_SCHEMA = """
ALTER TABLE oidc_enrollment_transactions
    ADD COLUMN begin_idempotency_key_hash TEXT;
ALTER TABLE oidc_enrollment_transactions
    ADD COLUMN begin_request_digest TEXT CHECK(
        begin_request_digest IS NULL OR length(begin_request_digest)=64
    );
ALTER TABLE oidc_enrollment_transactions
    ADD COLUMN begin_response_encrypted TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_oidc_enrollment_begin_idempotency
    ON oidc_enrollment_transactions(begin_idempotency_key_hash)
    WHERE begin_idempotency_key_hash IS NOT NULL;
CREATE TABLE IF NOT EXISTS credential_renewal_requests (
    request_id TEXT PRIMARY KEY,
    request_digest TEXT NOT NULL UNIQUE CHECK(length(request_digest)=64),
    credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    result_status TEXT NOT NULL CHECK(result_status IN ('current','renewed')),
    old_expires_at INTEGER NOT NULL,
    new_expires_at INTEGER NOT NULL,
    committed_at INTEGER NOT NULL,
    CHECK(
        (result_status='current' AND new_expires_at=old_expires_at)
        OR (result_status='renewed' AND new_expires_at>old_expires_at)
    )
);
CREATE INDEX IF NOT EXISTS idx_credential_renewal_credential
    ON credential_renewal_requests(credential_id,committed_at,request_id);
"""

IDENTITY_LIFECYCLE_REQUIRED_TABLES = frozenset({"credential_renewal_requests"})
IDENTITY_LIFECYCLE_REQUIRED_INDEXES = frozenset(
    {
        "idx_oidc_enrollment_begin_idempotency",
        "idx_credential_renewal_credential",
    }
)


__all__ = [
    "IDENTITY_LIFECYCLE_REQUIRED_INDEXES",
    "IDENTITY_LIFECYCLE_REQUIRED_TABLES",
    "IDENTITY_LIFECYCLE_SCHEMA",
    "IDENTITY_LIFECYCLE_SCHEMA_VERSION",
]
