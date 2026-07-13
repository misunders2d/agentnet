"""Registered workload credentials and transition-proof schema."""

from __future__ import annotations


WORKLOAD_SCHEMA_VERSION = 1

WORKLOAD_SCHEMA = """
CREATE TABLE IF NOT EXISTS workload_registrations (
    registration_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    workload_id TEXT NOT NULL,
    workload_role TEXT NOT NULL,
    recipient_scope TEXT NOT NULL,
    process_id INTEGER NOT NULL CHECK (process_id > 0),
    process_start_time INTEGER NOT NULL CHECK (process_start_time > 0),
    session_id TEXT NOT NULL,
    spiffe_id TEXT NOT NULL,
    certificate_serial TEXT NOT NULL,
    key_id TEXT NOT NULL,
    public_key_pem TEXT NOT NULL,
    credential_epoch INTEGER NOT NULL CHECK (credential_epoch > 0),
    revocation_epoch INTEGER NOT NULL CHECK (revocation_epoch > 0),
    parent_event_id TEXT,
    task_grant_id TEXT,
    status TEXT NOT NULL CHECK (status IN ('active','revoked','expired')),
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK ((parent_event_id IS NULL) = (task_grant_id IS NULL)),
    UNIQUE(domain_id,workload_id,process_id,process_start_time,session_id,credential_epoch)
);
CREATE INDEX IF NOT EXISTS idx_workload_registration_current
    ON workload_registrations(domain_id,workload_role,recipient_scope,status,expires_at);
CREATE TABLE IF NOT EXISTS recipient_address_snapshots (
    event_id TEXT NOT NULL REFERENCES events(event_id),
    recipient_id TEXT NOT NULL,
    snapshot_digest TEXT NOT NULL,
    snapshot_encrypted TEXT NOT NULL,
    resolved_at INTEGER NOT NULL,
    PRIMARY KEY(event_id,recipient_id),
    FOREIGN KEY(event_id,recipient_id) REFERENCES recipients(event_id,recipient_id)
);
"""

WORKLOAD_REQUIRED_TABLES = frozenset(
    {"workload_registrations", "recipient_address_snapshots"}
)
WORKLOAD_REQUIRED_INDEXES = frozenset({"idx_workload_registration_current"})


__all__ = [
    "WORKLOAD_REQUIRED_INDEXES",
    "WORKLOAD_REQUIRED_TABLES",
    "WORKLOAD_SCHEMA",
    "WORKLOAD_SCHEMA_VERSION",
]
