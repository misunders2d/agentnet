"""Persistent cumulative artifact-byte quota accounts and exact charge ledger."""

from __future__ import annotations


ARTIFACT_QUOTA_SCHEMA_VERSION = 1

ARTIFACT_QUOTA_SCHEMA = """
CREATE TABLE IF NOT EXISTS artifact_byte_accounts (
    scope_type TEXT NOT NULL CHECK (scope_type IN ('actor','domain')),
    scope_id TEXT NOT NULL,
    used_bytes INTEGER NOT NULL CHECK (used_bytes >= 0),
    limit_bytes INTEGER NOT NULL CHECK (limit_bytes > 0),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(scope_type,scope_id)
);
CREATE TABLE IF NOT EXISTS artifact_byte_charges (
    reservation_id TEXT PRIMARY KEY REFERENCES artifact_reservations(reservation_id),
    domain_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    charged_bytes INTEGER NOT NULL CHECK (charged_bytes >= 0),
    state TEXT NOT NULL CHECK (state IN ('charged','release_pending','released')),
    release_reason TEXT CHECK (release_reason IN ('aborted','expired','deleted','prefilter_denied')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    released_at INTEGER,
    CHECK (
        (state='charged' AND release_reason IS NULL AND released_at IS NULL)
        OR
        (state='release_pending' AND release_reason IS NOT NULL AND released_at IS NULL)
        OR
        (state='released' AND release_reason IS NOT NULL AND released_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_artifact_byte_charges_state
    ON artifact_byte_charges(state,updated_at,reservation_id);
CREATE INDEX IF NOT EXISTS idx_artifact_reservations_expiry_state
    ON artifact_reservations(state,expires_at,reservation_id);

-- Existing live reservations become charged exactly once. Deleted manifests
-- and already-terminal reservations are intentionally excluded. The maximum
-- placeholder limit is replaced by runtime policy on the next reconciliation.
INSERT INTO artifact_byte_charges(
    reservation_id,domain_id,actor_id,charged_bytes,state,created_at,updated_at
)
SELECT r.reservation_id,r.domain_id,r.actor_id,r.expected_size,'charged',r.expires_at,r.expires_at
  FROM artifact_reservations r
  LEFT JOIN artifact_manifests m ON m.reservation_id=r.reservation_id
 WHERE r.state NOT IN ('aborted','expired','prefilter_denied')
   AND (m.artifact_id IS NULL OR m.state <> 'deleted')
ON CONFLICT(reservation_id) DO NOTHING;

UPDATE artifact_byte_accounts SET used_bytes=0;
INSERT INTO artifact_byte_accounts(scope_type,scope_id,used_bytes,limit_bytes,updated_at)
SELECT scope_type,scope_id,SUM(charged_bytes),9223372036854775807,MAX(updated_at)
  FROM (
      SELECT 'actor' AS scope_type,actor_id AS scope_id,charged_bytes,updated_at
        FROM artifact_byte_charges WHERE state IN ('charged','release_pending')
      UNION ALL
      SELECT 'domain' AS scope_type,domain_id AS scope_id,charged_bytes,updated_at
        FROM artifact_byte_charges WHERE state IN ('charged','release_pending')
  ) charged_scopes
 GROUP BY scope_type,scope_id
ON CONFLICT(scope_type,scope_id) DO UPDATE SET
    used_bytes=excluded.used_bytes,
    updated_at=excluded.updated_at;
"""

ARTIFACT_QUOTA_REQUIRED_TABLES = frozenset(
    {"artifact_byte_accounts", "artifact_byte_charges"}
)
ARTIFACT_QUOTA_REQUIRED_INDEXES = frozenset(
    {"idx_artifact_byte_charges_state", "idx_artifact_reservations_expiry_state"}
)


__all__ = [
    "ARTIFACT_QUOTA_REQUIRED_INDEXES",
    "ARTIFACT_QUOTA_REQUIRED_TABLES",
    "ARTIFACT_QUOTA_SCHEMA",
    "ARTIFACT_QUOTA_SCHEMA_VERSION",
]
