"""Durable production OIDC enrollment transaction schema."""

from __future__ import annotations


IDENTITY_SCHEMA_VERSION = 1

IDENTITY_SCHEMA = """
CREATE TABLE IF NOT EXISTS oidc_enrollment_transactions (
    transaction_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    audience TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    state_hash TEXT NOT NULL UNIQUE,
    nonce_hash TEXT NOT NULL,
    code_verifier_encrypted TEXT NOT NULL,
    harness_kind TEXT NOT NULL,
    harness_name TEXT NOT NULL,
    public_key_pem TEXT NOT NULL,
    key_id TEXT NOT NULL,
    binding_assurance TEXT NOT NULL CHECK (binding_assurance IN ('os_bound','hardware_bound')),
    status TEXT NOT NULL CHECK (status IN ('pending','exchanging','consumed','failed')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    claimed_at INTEGER,
    consumed_at INTEGER,
    authorization_code_hash TEXT UNIQUE,
    id_token_hash TEXT UNIQUE,
    enrollment_challenge_id TEXT UNIQUE REFERENCES enrollment_challenges(challenge_id)
);
CREATE INDEX IF NOT EXISTS idx_oidc_enrollment_pending
    ON oidc_enrollment_transactions(status,expires_at,transaction_id);
"""

IDENTITY_REQUIRED_TABLES = frozenset({"oidc_enrollment_transactions"})
IDENTITY_REQUIRED_INDEXES = frozenset({"idx_oidc_enrollment_pending"})


__all__ = [
    "IDENTITY_REQUIRED_INDEXES",
    "IDENTITY_REQUIRED_TABLES",
    "IDENTITY_SCHEMA",
    "IDENTITY_SCHEMA_VERSION",
]
