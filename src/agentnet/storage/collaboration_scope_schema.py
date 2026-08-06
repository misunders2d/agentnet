"""Versioned authorization boundary shared by communication and collaboration."""

from __future__ import annotations


COLLABORATION_SCOPE_SCHEMA_VERSION = 7

COLLABORATION_SCOPE_SCHEMA = """
CREATE TABLE IF NOT EXISTS collaboration_scopes (
    scope_id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('personal','direct','shared')),
    owner_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    owner_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    source_communication_scope_id TEXT UNIQUE REFERENCES communication_scopes(scope_id),
    state TEXT NOT NULL CHECK (state IN (
        'active','revoked','expired','archived','deleted','blocked'
    )),
    state_reason TEXT NOT NULL CHECK (length(state_reason) BETWEEN 1 AND 128),
    allowed_actions_json TEXT NOT NULL,
    allowed_resource_prefixes_json TEXT NOT NULL,
    allowed_classifications_json TEXT NOT NULL,
    canonical_references_json TEXT NOT NULL,
    policy_floor INTEGER NOT NULL CHECK (policy_floor >= 1),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= policy_floor),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    control_sequence INTEGER NOT NULL CHECK (control_sequence >= 1),
    membership_sequence INTEGER NOT NULL CHECK (membership_sequence >= 1),
    proposal_digest TEXT NOT NULL CHECK (length(proposal_digest) = 64),
    scope_digest TEXT NOT NULL UNIQUE CHECK (length(scope_digest) = 64),
    audit_record_hash TEXT NOT NULL CHECK (length(audit_record_hash) = 64),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER,
    archived_at INTEGER,
    deleted_at INTEGER,
    CHECK (updated_at >= created_at),
    CHECK (expires_at IS NULL OR expires_at > created_at),
    CHECK ((state = 'revoked') = (revoked_at IS NOT NULL)),
    CHECK ((state = 'archived') = (archived_at IS NOT NULL)),
    CHECK ((state = 'deleted') = (deleted_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_collaboration_scopes_owner
    ON collaboration_scopes(domain_id,owner_principal_id,state,updated_at,scope_id);
CREATE INDEX IF NOT EXISTS idx_collaboration_scopes_current
    ON collaboration_scopes(domain_id,state,expires_at,scope_id);

CREATE TABLE IF NOT EXISTS collaboration_scope_members (
    scope_id TEXT NOT NULL REFERENCES collaboration_scopes(scope_id) ON DELETE RESTRICT,
    authority_kind TEXT NOT NULL CHECK (authority_kind IN ('principal','guest')),
    authority_id TEXT NOT NULL,
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    role TEXT NOT NULL CHECK (role IN ('owner','administrator','member','guest')),
    state TEXT NOT NULL CHECK (state IN ('active','removed')),
    joined_sequence INTEGER NOT NULL CHECK (joined_sequence >= 1),
    removed_sequence INTEGER,
    member_digest TEXT NOT NULL CHECK (length(member_digest) = 64),
    joined_at INTEGER NOT NULL,
    removed_at INTEGER,
    PRIMARY KEY (scope_id,harness_id),
    CHECK (
        (state = 'active' AND removed_sequence IS NULL AND removed_at IS NULL)
        OR
        (state = 'removed' AND removed_sequence IS NOT NULL AND removed_at IS NOT NULL
            AND removed_sequence >= joined_sequence)
    )
);
CREATE INDEX IF NOT EXISTS idx_collaboration_scope_members_actor
    ON collaboration_scope_members(authority_kind,authority_id,harness_id,state,scope_id);
"""

COLLABORATION_SCOPE_REQUIRED_TABLES = frozenset(
    {"collaboration_scopes", "collaboration_scope_members"}
)
COLLABORATION_SCOPE_REQUIRED_INDEXES = frozenset(
    {
        "idx_collaboration_scopes_owner",
        "idx_collaboration_scopes_current",
        "idx_collaboration_scope_members_actor",
    }
)


__all__ = [
    "COLLABORATION_SCOPE_REQUIRED_INDEXES",
    "COLLABORATION_SCOPE_REQUIRED_TABLES",
    "COLLABORATION_SCOPE_SCHEMA",
    "COLLABORATION_SCOPE_SCHEMA_VERSION",
]
