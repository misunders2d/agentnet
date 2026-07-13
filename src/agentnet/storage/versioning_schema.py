"""Canonical operational-versioning tables and fail-closed verification."""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


VERSIONING_SCHEMA_VERSION = 1

VERSIONING_SCHEMA = """
CREATE TABLE IF NOT EXISTS profile_offers (
    offer_id TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    peer_namespace TEXT NOT NULL,
    host_domain_id TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    credential_epoch INTEGER NOT NULL,
    domain_revocation_epoch INTEGER NOT NULL,
    status_epoch INTEGER NOT NULL,
    offer_digest TEXT NOT NULL,
    offer_encrypted TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('issued','verified','rejected')),
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY(offer_id,direction)
);
CREATE INDEX IF NOT EXISTS idx_profile_offers_peer
    ON profile_offers(peer_namespace,direction,recorded_at,offer_id);
CREATE TABLE IF NOT EXISTS profile_peer_state (
    peer_namespace TEXT PRIMARY KEY,
    host_domain_id TEXT NOT NULL,
    actor_encrypted TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    credential_epoch INTEGER NOT NULL,
    domain_revocation_epoch INTEGER NOT NULL,
    remote_status_epoch INTEGER NOT NULL,
    protocol_version TEXT NOT NULL,
    schema_profile TEXT NOT NULL,
    schema_hash TEXT NOT NULL,
    config_schema_version TEXT NOT NULL,
    storage_schema_version INTEGER NOT NULL,
    adapter_id TEXT NOT NULL,
    adapter_version TEXT NOT NULL,
    features_json TEXT NOT NULL,
    missing_optional_features_json TEXT NOT NULL,
    offer_digest TEXT NOT NULL,
    negotiated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS unsupported_event_quarantine (
    quarantine_id TEXT PRIMARY KEY,
    peer_namespace TEXT NOT NULL,
    event_type TEXT NOT NULL,
    required_protocol_version TEXT NOT NULL,
    required_schema_profile TEXT NOT NULL,
    required_schema_hash TEXT NOT NULL,
    required_features_json TEXT NOT NULL,
    event_digest TEXT NOT NULL,
    event_encrypted TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','replayed','rejected')),
    reason_code TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    replayed_at INTEGER,
    UNIQUE(peer_namespace,event_digest)
);
CREATE INDEX IF NOT EXISTS idx_unsupported_event_replay
    ON unsupported_event_quarantine(peer_namespace,state,received_at,quarantine_id);
CREATE TABLE IF NOT EXISTS version_rollouts (
    rollout_id TEXT PRIMARY KEY,
    host_domain_id TEXT NOT NULL,
    from_protocol_version TEXT NOT NULL,
    to_protocol_version TEXT NOT NULL,
    from_schema_version INTEGER NOT NULL,
    to_schema_version INTEGER NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN (
        'expanded','migrated_backfilled','verified','contracted','rolled_back'
    )),
    compatibility_deadline INTEGER NOT NULL,
    policy_revision_at_start INTEGER NOT NULL,
    revocation_epoch_at_start INTEGER NOT NULL,
    verification_digest TEXT,
    deprecated_protocol_version TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_version_rollouts_domain
    ON version_rollouts(host_domain_id,created_at,rollout_id);
"""

VERSIONING_REQUIRED_TABLES = frozenset(
    {
        "profile_offers",
        "profile_peer_state",
        "unsupported_event_quarantine",
        "version_rollouts",
    }
)
VERSIONING_REQUIRED_INDEXES = frozenset(
    {
        "idx_profile_offers_peer",
        "idx_unsupported_event_replay",
        "idx_version_rollouts_domain",
    }
)


def require_versioning_schema(store: Any) -> None:
    """Require migration 5 and every relation before versioning state is used."""

    backend = getattr(store, "backend_name", "")
    try:
        if backend == "sqlite":
            missing_tables = {
                name
                for name in VERSIONING_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
                    (name,),
                )
                is None
            }
            missing_indexes = {
                name
                for name in VERSIONING_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?",
                    (name,),
                )
                is None
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            if metadata is None or int(metadata["value"]) < VERSIONING_SCHEMA_VERSION:
                raise GateBlocked("versioning_schema", "operational versioning SQLite schema is not current")
        elif backend == "postgresql":
            missing_tables = {
                name
                for name in VERSIONING_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in VERSIONING_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            migrations = store.fetch_one("SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations")
            from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION

            if (
                CURRENT_SCHEMA_VERSION < VERSIONING_SCHEMA_VERSION
                or metadata is None
                or int(metadata["value"]) != CURRENT_SCHEMA_VERSION
                or migrations is None
                or int(migrations["version"]) != CURRENT_SCHEMA_VERSION
            ):
                raise GateBlocked("versioning_schema", "operational versioning migration is not current")
        else:
            raise GateBlocked("versioning_schema", "operational versioning storage backend is unsupported")
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("versioning_schema", "operational versioning schema could not be verified") from exc
    if missing_tables or missing_indexes:
        raise GateBlocked("versioning_schema", "operational versioning schema is missing required relations")


__all__ = [
    "VERSIONING_REQUIRED_INDEXES",
    "VERSIONING_REQUIRED_TABLES",
    "VERSIONING_SCHEMA",
    "VERSIONING_SCHEMA_VERSION",
    "require_versioning_schema",
]
