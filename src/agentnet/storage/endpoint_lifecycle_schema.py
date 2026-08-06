"""Durable lifecycle state for one exact enrolled AgentNet endpoint."""

from __future__ import annotations


ENDPOINT_LIFECYCLE_SCHEMA_VERSION = 7

ENDPOINT_LIFECYCLE_SCHEMA = """
CREATE TABLE IF NOT EXISTS endpoint_lifecycle (
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    current_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    harness_kind TEXT NOT NULL CHECK (harness_kind IN (
        'omp','pi','claude','codex','antigravity','server'
    )),
    profile_key TEXT NOT NULL CHECK (length(profile_key) BETWEEN 1 AND 256),
    state TEXT NOT NULL CHECK (state IN (
        'ready_to_connect','waiting_for_approval','enrolled','access_ready',
        'restart_required','connected','blocked'
    )),
    adapter_generation INTEGER NOT NULL CHECK (adapter_generation >= 1),
    mailbox_cursor INTEGER NOT NULL DEFAULT 0 CHECK (mailbox_cursor >= 0),
    capability_root_digest TEXT CHECK (
        capability_root_digest IS NULL OR length(capability_root_digest) = 64
    ),
    process_measurement TEXT CHECK (
        process_measurement IS NULL OR length(process_measurement) = 64
    ),
    state_reason TEXT NOT NULL CHECK (length(state_reason) BETWEEN 1 AND 128),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (domain_id,harness_id),
    UNIQUE (domain_id,harness_kind,profile_key),
    CHECK (updated_at >= created_at)
);
CREATE INDEX IF NOT EXISTS idx_endpoint_lifecycle_principal
    ON endpoint_lifecycle(domain_id,principal_id,state,harness_id);
CREATE INDEX IF NOT EXISTS idx_endpoint_lifecycle_restart
    ON endpoint_lifecycle(domain_id,state,adapter_generation,harness_id);
"""

ENDPOINT_LIFECYCLE_REQUIRED_TABLES = frozenset({"endpoint_lifecycle"})
ENDPOINT_LIFECYCLE_REQUIRED_INDEXES = frozenset(
    {"idx_endpoint_lifecycle_principal", "idx_endpoint_lifecycle_restart"}
)


__all__ = [
    "ENDPOINT_LIFECYCLE_REQUIRED_INDEXES",
    "ENDPOINT_LIFECYCLE_REQUIRED_TABLES",
    "ENDPOINT_LIFECYCLE_SCHEMA",
    "ENDPOINT_LIFECYCLE_SCHEMA_VERSION",
]
