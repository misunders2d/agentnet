"""First-positive-authority bootstrap challenge and singleton schema."""

from __future__ import annotations


AUTHORITY_BOOTSTRAP_SCHEMA_VERSION = 1

AUTHORITY_BOOTSTRAP_SCHEMA = """
CREATE TABLE IF NOT EXISTS authority_bootstrap_challenges (
    challenge_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    credential_epoch INTEGER NOT NULL CHECK (credential_epoch > 0),
    credential_key_id TEXT NOT NULL,
    binding_assurance TEXT NOT NULL CHECK (binding_assurance IN ('os_bound','hardware_bound')),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch > 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    candidate_entitlement_id TEXT NOT NULL UNIQUE,
    candidate_entitlement_json TEXT NOT NULL,
    nonce_hash TEXT NOT NULL,
    transaction_digest TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    approval_receipt_id TEXT UNIQUE,
    approval_receipt_digest TEXT UNIQUE,
    CHECK (
        (consumed_at IS NULL AND approval_receipt_id IS NULL AND approval_receipt_digest IS NULL)
        OR
        (consumed_at IS NOT NULL AND approval_receipt_id IS NOT NULL AND approval_receipt_digest IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_authority_bootstrap_pending
    ON authority_bootstrap_challenges(domain_id,consumed_at,expires_at,challenge_id);
CREATE TABLE IF NOT EXISTS authority_bootstrap_slots (
    domain_id TEXT PRIMARY KEY REFERENCES domains(domain_id),
    current_entitlement_id TEXT NOT NULL UNIQUE REFERENCES entitlements(entitlement_id),
    generation INTEGER NOT NULL CHECK (generation > 0),
    updated_at INTEGER NOT NULL
);
"""

AUTHORITY_BOOTSTRAP_REQUIRED_TABLES = frozenset(
    {"authority_bootstrap_challenges", "authority_bootstrap_slots"}
)
AUTHORITY_BOOTSTRAP_REQUIRED_INDEXES = frozenset({"idx_authority_bootstrap_pending"})


__all__ = [
    "AUTHORITY_BOOTSTRAP_REQUIRED_INDEXES",
    "AUTHORITY_BOOTSTRAP_REQUIRED_TABLES",
    "AUTHORITY_BOOTSTRAP_SCHEMA",
    "AUTHORITY_BOOTSTRAP_SCHEMA_VERSION",
]
