"""Opaque single-use invitation links for bounded collaboration onboarding."""

from __future__ import annotations


INVITATION_LINK_SCHEMA_VERSION = 7

INVITATION_LINK_SCHEMA = """
CREATE TABLE IF NOT EXISTS invitation_links (
    invitation_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    destination_scope_id TEXT NOT NULL REFERENCES collaboration_scopes(scope_id),
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    encrypted_offer TEXT NOT NULL,
    offer_digest TEXT NOT NULL UNIQUE CHECK (length(offer_digest) = 64),
    invited_email_encrypted TEXT NOT NULL,
    invited_email_sha256 TEXT NOT NULL CHECK (length(invited_email_sha256) = 64),
    sponsor_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    sponsor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    sponsor_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    sponsor_credential_epoch INTEGER NOT NULL CHECK (sponsor_credential_epoch >= 1),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    state TEXT NOT NULL CHECK (state IN ('issued','reserved','consumed','revoked','expired')),
    state_reason TEXT NOT NULL CHECK (length(state_reason) BETWEEN 1 AND 128),
    max_uses INTEGER NOT NULL CHECK (max_uses = 1),
    use_count INTEGER NOT NULL CHECK (use_count IN (0,1)),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    reservation_id TEXT UNIQUE,
    reservation_digest TEXT UNIQUE CHECK (
        reservation_digest IS NULL OR length(reservation_digest) = 64
    ),
    reserved_at INTEGER,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    consumed_at INTEGER,
    revoked_at INTEGER,
    reissued_from_invitation_id TEXT UNIQUE REFERENCES invitation_links(invitation_id),
    audit_record_hash TEXT NOT NULL CHECK (length(audit_record_hash) = 64),
    CHECK (expires_at > created_at),
    CHECK (updated_at >= created_at),
    CHECK (
        (state = 'issued' AND use_count = 0 AND reservation_id IS NULL
            AND reservation_digest IS NULL AND reserved_at IS NULL
            AND consumed_at IS NULL AND revoked_at IS NULL)
        OR
        (state = 'reserved' AND use_count = 0 AND reservation_id IS NOT NULL
            AND reservation_digest IS NOT NULL AND reserved_at IS NOT NULL
            AND consumed_at IS NULL AND revoked_at IS NULL)
        OR
        (state = 'consumed' AND use_count = 1 AND reservation_id IS NOT NULL
            AND reservation_digest IS NOT NULL AND reserved_at IS NOT NULL
            AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR
        (state = 'revoked' AND use_count = 0 AND consumed_at IS NULL
            AND revoked_at IS NOT NULL)
        OR
        (state = 'expired' AND use_count = 0 AND consumed_at IS NULL
            AND revoked_at IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_invitation_links_sponsor
    ON invitation_links(domain_id,sponsor_principal_id,state,created_at,invitation_id);
CREATE INDEX IF NOT EXISTS idx_invitation_links_expiry
    ON invitation_links(state,expires_at,invitation_id);

CREATE TABLE IF NOT EXISTS invitation_link_failures (
    invitation_id TEXT NOT NULL REFERENCES invitation_links(invitation_id) ON DELETE RESTRICT,
    source_fingerprint TEXT NOT NULL CHECK (length(source_fingerprint) = 64),
    window_started_at INTEGER NOT NULL,
    failure_count INTEGER NOT NULL CHECK (failure_count >= 0),
    locked_until INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (invitation_id,source_fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_invitation_link_failures_lock
    ON invitation_link_failures(locked_until,updated_at,invitation_id);
"""

INVITATION_LINK_REQUIRED_TABLES = frozenset(
    {"invitation_links", "invitation_link_failures"}
)
INVITATION_LINK_REQUIRED_INDEXES = frozenset(
    {
        "idx_invitation_links_sponsor",
        "idx_invitation_links_expiry",
        "idx_invitation_link_failures_lock",
    }
)


__all__ = [
    "INVITATION_LINK_REQUIRED_INDEXES",
    "INVITATION_LINK_REQUIRED_TABLES",
    "INVITATION_LINK_SCHEMA",
    "INVITATION_LINK_SCHEMA_VERSION",
]
