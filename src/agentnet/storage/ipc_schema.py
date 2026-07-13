"""Persistent replay fences for authenticated Unix IPC frames."""

from __future__ import annotations


IPC_SCHEMA_VERSION = 1

IPC_SCHEMA = """
CREATE TABLE IF NOT EXISTS ipc_replay_fences (
    context_digest TEXT NOT NULL,
    root_key_id TEXT NOT NULL,
    capability_id TEXT NOT NULL,
    peer_uid INTEGER NOT NULL CHECK (peer_uid >= 0),
    peer_pid INTEGER NOT NULL CHECK (peer_pid > 0),
    process_start_time TEXT NOT NULL,
    process_measurement TEXT NOT NULL,
    session_id TEXT NOT NULL,
    nonce_hash TEXT NOT NULL,
    consumed_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(context_digest,nonce_hash),
    CHECK (expires_at > consumed_at)
);
CREATE INDEX IF NOT EXISTS idx_ipc_replay_fences_expiry
    ON ipc_replay_fences(expires_at);
"""

IPC_REQUIRED_TABLES = frozenset({"ipc_replay_fences"})
IPC_REQUIRED_INDEXES = frozenset({"idx_ipc_replay_fences_expiry"})


__all__ = [
    "IPC_REQUIRED_INDEXES",
    "IPC_REQUIRED_TABLES",
    "IPC_SCHEMA",
    "IPC_SCHEMA_VERSION",
]
