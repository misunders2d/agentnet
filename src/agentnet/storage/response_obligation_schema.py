"""Durable response-obligation schema.

A response obligation is request/answer ownership, not delivery custody.  The
row binds the originating request event and its exact payload digest to one
responsible recipient harness; delivery facts stay in ``recipients`` and are
only ever mirrored into the obligation through the durable mailbox record, so
the two tables can never disagree about who proved what.
"""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


RESPONSE_OBLIGATION_SCHEMA_VERSION = 2

RESPONSE_OBLIGATION_SCHEMA = """
CREATE TABLE IF NOT EXISTS response_obligations (
    obligation_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    thread_id TEXT NOT NULL,
    request_event_id TEXT NOT NULL REFERENCES events(event_id),
    request_payload_digest TEXT NOT NULL,
    request_envelope_digest TEXT NOT NULL,
    requester_authority_id TEXT NOT NULL,
    requester_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    responsible_authority_id TEXT NOT NULL,
    responsible_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    response_required INTEGER NOT NULL CHECK (response_required IN (0,1)),
    response_schema_id TEXT,
    state TEXT NOT NULL CHECK (state IN (
        'created','recipient_committed','acknowledged','in_progress',
        'pending_human','blocked','completed','failed','canceled','expired'
    )),
    state_reason TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    deadline_at INTEGER,
    policy_revision INTEGER NOT NULL,
    response_event_id TEXT REFERENCES events(event_id),
    response_payload_digest TEXT,
    response_outcome TEXT CHECK (response_outcome IN ('completed','failed')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    closed_at INTEGER,
    UNIQUE(request_event_id,responsible_harness_id)
);
CREATE INDEX IF NOT EXISTS idx_response_obligations_responsible
    ON response_obligations(domain_id,responsible_harness_id,state,deadline_at);
CREATE INDEX IF NOT EXISTS idx_response_obligations_requester
    ON response_obligations(domain_id,requester_harness_id,state,deadline_at);
CREATE TABLE IF NOT EXISTS response_obligation_transitions (
    obligation_id TEXT NOT NULL REFERENCES response_obligations(obligation_id),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    detail_json TEXT NOT NULL,
    response_event_id TEXT,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(obligation_id,revision)
);
"""

RESPONSE_OBLIGATION_REQUIRED_TABLES = frozenset(
    {"response_obligations", "response_obligation_transitions"}
)
RESPONSE_OBLIGATION_REQUIRED_INDEXES = frozenset(
    {"idx_response_obligations_responsible", "idx_response_obligations_requester"}
)


def require_response_obligation_schema(store: Any) -> None:
    """Fail closed unless migration 2 and every obligation relation are present."""

    backend = getattr(store, "backend_name", "")
    try:
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if metadata is None or int(metadata["value"]) < RESPONSE_OBLIGATION_SCHEMA_VERSION:
            raise GateBlocked("response_obligation_schema", "response-obligation schema is not current")
        if backend == "sqlite":
            missing_tables = {
                name
                for name in RESPONSE_OBLIGATION_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
                )
                is None
            }
            missing_indexes = {
                name
                for name in RESPONSE_OBLIGATION_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?", (name,)
                )
                is None
            }
        elif backend == "postgresql":
            migrations = store.fetch_one("SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations")
            if migrations is None or int(migrations["version"]) < RESPONSE_OBLIGATION_SCHEMA_VERSION:
                raise GateBlocked("response_obligation_schema", "response-obligation migration is not current")
            missing_tables = {
                name
                for name in RESPONSE_OBLIGATION_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in RESPONSE_OBLIGATION_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
        else:
            raise GateBlocked("response_obligation_schema", "response-obligation backend is unsupported")
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("response_obligation_schema", "response-obligation schema could not be verified") from exc
    if missing_tables or missing_indexes:
        raise GateBlocked("response_obligation_schema", "response-obligation relations are missing")


__all__ = [
    "RESPONSE_OBLIGATION_REQUIRED_INDEXES",
    "RESPONSE_OBLIGATION_REQUIRED_TABLES",
    "RESPONSE_OBLIGATION_SCHEMA",
    "RESPONSE_OBLIGATION_SCHEMA_VERSION",
    "require_response_obligation_schema",
]
