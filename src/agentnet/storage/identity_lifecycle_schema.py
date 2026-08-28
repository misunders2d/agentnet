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
CREATE TABLE IF NOT EXISTS expired_server_credential_replacements (
    request_id TEXT PRIMARY KEY,
    request_digest TEXT NOT NULL UNIQUE CHECK(length(request_digest)=64),
    setup_request_digest TEXT NOT NULL CHECK(length(setup_request_digest)=64),
    expected_config_digest TEXT NOT NULL CHECK(length(expected_config_digest)=64),
    old_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    new_credential_id TEXT NOT NULL UNIQUE REFERENCES credentials(credential_id),
    new_epoch INTEGER NOT NULL CHECK(new_epoch>=2),
    not_before INTEGER NOT NULL,
    new_expires_at INTEGER NOT NULL,
    committed_at INTEGER NOT NULL,
    CHECK(new_credential_id<>old_credential_id),
    CHECK(new_expires_at>not_before)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_expired_server_replacement_old
    ON expired_server_credential_replacements(old_credential_id);
"""

IDENTITY_LIFECYCLE_REQUIRED_TABLES = frozenset(
    {"credential_renewal_requests", "expired_server_credential_replacements"}
)
IDENTITY_LIFECYCLE_REQUIRED_INDEXES = frozenset(
    {
        "idx_oidc_enrollment_begin_idempotency",
        "idx_credential_renewal_credential",
        "idx_expired_server_replacement_old",
    }
)


__all__ = [
    "IDENTITY_LIFECYCLE_REQUIRED_INDEXES",
    "IDENTITY_LIFECYCLE_REQUIRED_TABLES",
    "IDENTITY_LIFECYCLE_SCHEMA",
    "IDENTITY_LIFECYCLE_SCHEMA_VERSION",
]
