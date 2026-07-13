"""Durable OIDC reauthentication transactions for credential recovery."""

from __future__ import annotations


CREDENTIAL_RECOVERY_SCHEMA_VERSION = 1

CREDENTIAL_RECOVERY_SCHEMA = """
CREATE TABLE IF NOT EXISTS oidc_recovery_transactions (
    transaction_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    audience TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    state_hash TEXT NOT NULL UNIQUE,
    nonce_hash TEXT NOT NULL,
    code_verifier_encrypted TEXT NOT NULL,
    old_harness_id TEXT NOT NULL,
    new_harness_kind TEXT NOT NULL,
    new_harness_name TEXT NOT NULL,
    new_binding_assurance TEXT NOT NULL CHECK (new_binding_assurance IN ('os_bound','hardware_bound')),
    new_public_key_pem TEXT NOT NULL,
    new_key_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','exchanging','verified','recovering','consumed','failed')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    claimed_at INTEGER,
    verified_at INTEGER,
    consumed_at INTEGER,
    authorization_code_hash TEXT UNIQUE,
    id_token_hash TEXT UNIQUE,
    recovery_request_encrypted TEXT
);
CREATE INDEX IF NOT EXISTS idx_oidc_recovery_pending
    ON oidc_recovery_transactions(status,expires_at,transaction_id);
"""

CREDENTIAL_RECOVERY_REQUIRED_TABLES = frozenset({"oidc_recovery_transactions"})
CREDENTIAL_RECOVERY_REQUIRED_INDEXES = frozenset({"idx_oidc_recovery_pending"})


__all__ = [
    "CREDENTIAL_RECOVERY_REQUIRED_INDEXES",
    "CREDENTIAL_RECOVERY_REQUIRED_TABLES",
    "CREDENTIAL_RECOVERY_SCHEMA",
    "CREDENTIAL_RECOVERY_SCHEMA_VERSION",
]
