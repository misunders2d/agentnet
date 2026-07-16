"""Dedicated SQLite custody for the independent approval service."""

from __future__ import annotations

import hashlib
import os
import sqlite3
import stat
import threading
import time
from contextlib import contextmanager
from functools import lru_cache
from pathlib import Path
from typing import Any, Iterator

from agentnet.errors import GateBlocked
from agentnet.security.envelope import LocalEnvelopeCipher


APPROVAL_STORE_SCHEMA_VERSION = 2
APPROVAL_STORE_SCHEMA_V1 = """
CREATE TABLE approval_store_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE approval_webauthn_credentials (
    credential_id_b64 TEXT PRIMARY KEY,
    approver_principal_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    user_handle_b64 TEXT NOT NULL,
    credential_public_key_b64 TEXT NOT NULL,
    sign_count INTEGER NOT NULL CHECK(sign_count >= 0),
    device_type TEXT NOT NULL,
    backed_up INTEGER NOT NULL CHECK(backed_up IN (0,1)),
    status TEXT NOT NULL CHECK(status IN ('active','revoked')),
    created_at INTEGER NOT NULL,
    revoked_at INTEGER,
    revocation_reason TEXT,
    UNIQUE(approver_principal_id,domain_id,user_handle_b64,credential_id_b64)
);
CREATE INDEX idx_approval_credentials_owner
    ON approval_webauthn_credentials(approver_principal_id,domain_id,status);
CREATE TABLE approval_registration_sessions (
    session_id TEXT PRIMARY KEY,
    approver_principal_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    capability_hash TEXT NOT NULL UNIQUE CHECK(length(capability_hash)=64),
    user_handle_b64 TEXT NOT NULL,
    challenge_encrypted TEXT,
    challenge_expires_at INTEGER,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER,
    failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts BETWEEN 0 AND 10)
);
CREATE INDEX idx_approval_registration_expiry
    ON approval_registration_sessions(expires_at,consumed_at);
CREATE TABLE approval_requests (
    request_id TEXT PRIMARY KEY,
    approver_principal_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    approval_purpose TEXT NOT NULL,
    capability_hash TEXT NOT NULL UNIQUE CHECK(length(capability_hash)=64),
    canonical_transaction_encrypted TEXT NOT NULL,
    transaction_digest TEXT NOT NULL CHECK(length(transaction_digest)=64),
    challenge_encrypted TEXT,
    challenge_expires_at INTEGER,
    state TEXT NOT NULL CHECK(state IN ('pending','issued','rejected','expired')),
    active_fingerprint TEXT UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    rejected_at INTEGER,
    expired_at INTEGER,
    failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts BETWEEN 0 AND 10)
);
CREATE INDEX idx_approval_request_expiry ON approval_requests(state,expires_at);
CREATE TABLE approval_issued_receipts (
    request_id TEXT PRIMARY KEY REFERENCES approval_requests(request_id) ON DELETE RESTRICT,
    credential_id_b64 TEXT NOT NULL REFERENCES approval_webauthn_credentials(credential_id_b64),
    authenticated_at INTEGER NOT NULL,
    issued_at INTEGER NOT NULL,
    receipt_expires_at INTEGER NOT NULL,
    receipt_encrypted TEXT NOT NULL,
    receipt_digest TEXT NOT NULL CHECK(length(receipt_digest)=64)
);
CREATE TABLE approval_audit (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    action TEXT NOT NULL,
    request_id TEXT,
    approver_principal_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    approval_purpose TEXT,
    transaction_digest TEXT,
    occurred_at INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    detail_code TEXT NOT NULL
);
CREATE INDEX idx_approval_audit_subject
    ON approval_audit(approver_principal_id,domain_id,occurred_at);
"""

_APPROVAL_STORE_MIGRATION_V2_STATEMENTS = (
    """ALTER TABLE approval_requests
       ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'direct_receipt'
       CHECK(delivery_mode IN ('direct_receipt','core_claim_code'))""",
    "ALTER TABLE approval_requests ADD COLUMN capability_encrypted TEXT",
    """CREATE TABLE approval_request_idempotency (
        idempotency_key TEXT PRIMARY KEY,
        request_id TEXT NOT NULL UNIQUE REFERENCES approval_requests(request_id) ON DELETE RESTRICT,
        request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
        created_at INTEGER NOT NULL
    )""",
    """CREATE TABLE approval_claim_codes (
        request_id TEXT PRIMARY KEY REFERENCES approval_requests(request_id) ON DELETE RESTRICT,
        claim_code_hash TEXT NOT NULL UNIQUE CHECK(length(claim_code_hash)=64),
        issued_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts BETWEEN 0 AND 5),
        first_retrieved_at INTEGER,
        last_retrieved_at INTEGER,
        last_retrieval_digest TEXT CHECK(
            last_retrieval_digest IS NULL OR length(last_retrieval_digest)=64
        )
    )""",
    """CREATE TABLE approval_store_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL,
        checksum TEXT NOT NULL CHECK(length(checksum)=64),
        applied_at INTEGER NOT NULL
    )""",
)

APPROVAL_STORE_SCHEMA_V2 = (
    APPROVAL_STORE_SCHEMA_V1
    + "\n"
    + ";\n".join(_APPROVAL_STORE_MIGRATION_V2_STATEMENTS)
    + ";\n"
)
# Current schema alias retained for callers that imported the original name.
APPROVAL_STORE_SCHEMA = APPROVAL_STORE_SCHEMA_V2
_APPROVAL_STORE_MIGRATION_V2_NAME = "guided approval handoff"
_APPROVAL_STORE_MIGRATION_V2_CHECKSUM = hashlib.sha256(
    "\n".join(_APPROVAL_STORE_MIGRATION_V2_STATEMENTS).encode("utf-8")
).hexdigest()

_CATALOG_QUERY = (
    "SELECT type,name,tbl_name,sql FROM sqlite_master "
    "WHERE type IN ('table','index','trigger') AND sql IS NOT NULL "
    "AND name NOT LIKE 'sqlite_%' ORDER BY type,name"
)


def _catalog(connection: sqlite3.Connection) -> tuple[tuple[str, str, str, str], ...]:
    return tuple(tuple(str(value) for value in row) for row in connection.execute(_CATALOG_QUERY))


@lru_cache(maxsize=2)
def expected_catalog(
    version: int = APPROVAL_STORE_SCHEMA_VERSION,
) -> tuple[tuple[str, str, str, str], ...]:
    schemas = {
        1: APPROVAL_STORE_SCHEMA_V1,
        2: APPROVAL_STORE_SCHEMA_V2,
    }
    schema = schemas.get(version)
    if schema is None:
        raise GateBlocked("approval_store", "approval schema version is unsupported")
    reference = sqlite3.connect(":memory:", isolation_level=None)
    try:
        reference.executescript(schema)
        return _catalog(reference)
    finally:
        reference.close()


@lru_cache(maxsize=2)
def expected_catalog_digest(version: int = APPROVAL_STORE_SCHEMA_VERSION) -> str:
    payload = "\n".join("\x1f".join(row) for row in expected_catalog(version)).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _expected_metadata(version: int) -> dict[str, str]:
    return {
        "schema_version": str(version),
        "schema_catalog_sha256": expected_catalog_digest(version),
    }


def _enable_wal(connection: sqlite3.Connection) -> None:
    """Converge concurrent openers on WAL without an unbounded startup race."""

    for attempt in range(100):
        try:
            current = connection.execute("PRAGMA journal_mode").fetchone()
            if current is not None and str(current[0]).lower() == "wal":
                return
            selected = connection.execute("PRAGMA journal_mode=WAL").fetchone()
            if selected is not None and str(selected[0]).lower() == "wal":
                return
        except sqlite3.OperationalError as exc:
            code = getattr(exc, "sqlite_errorcode", None)
            if code is None or code & 0xFF not in {sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED}:
                raise GateBlocked("approval_store", "approval WAL mode is unavailable") from exc
        if attempt < 99:
            time.sleep(0.05)
    raise GateBlocked("approval_store_busy", "approval store is busy")


def _require_private_database(path: Path) -> os.stat_result:
    if not path.is_absolute() or path.is_symlink() or path.parent.is_symlink():
        raise GateBlocked("approval_store", "approval database must be an absolute non-symlink file")
    try:
        parent = path.parent.stat()
        metadata = path.stat(follow_symlinks=False)
    except OSError as exc:
        raise GateBlocked("approval_store", "approval database is unavailable") from exc
    if (
        parent.st_uid != os.geteuid()
        or parent.st_mode & 0o077
        or not stat.S_ISREG(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_nlink != 1
        or metadata.st_mode & 0o077
    ):
        raise GateBlocked("approval_store", "approval database must be owner-only")
    return metadata


class ApprovalStore:
    """One independently rooted approval database; never shares core state."""

    def __init__(self, path: Path, cipher: LocalEnvelopeCipher, *, initialize: bool = False) -> None:
        self.path = path.absolute()
        before = _require_private_database(self.path)
        self.cipher = cipher
        self._lock = threading.RLock()
        self._connection = sqlite3.connect(
            self.path,
            timeout=5.0,
            isolation_level=None,
            check_same_thread=False,
        )
        self._connection.row_factory = sqlite3.Row
        self._connection.execute("PRAGMA busy_timeout=5000")
        self._connection.execute("PRAGMA foreign_keys=ON")
        _enable_wal(self._connection)
        self._connection.execute("PRAGMA synchronous=FULL")
        after = self.path.stat(follow_symlinks=False)
        if (before.st_dev, before.st_ino) != (after.st_dev, after.st_ino):
            self._connection.close()
            raise GateBlocked("approval_store", "approval database path changed while opening")
        try:
            if initialize:
                self._initialize()
            else:
                self._maybe_migrate()
            self._verify_schema()
        except Exception:
            self._connection.close()
            raise

    def _read_metadata(self) -> dict[str, str]:
        try:
            return {
                str(row["key"]): str(row["value"])
                for row in self._connection.execute(
                    "SELECT key,value FROM approval_store_meta"
                ).fetchall()
            }
        except sqlite3.DatabaseError as exc:
            raise GateBlocked("approval_store", "approval schema metadata is unavailable") from exc

    def _initialize(self) -> None:
        existing = self._connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
        ).fetchall()
        if existing:
            raise GateBlocked("approval_store", "approval database is already initialized")
        try:
            self._connection.executescript("BEGIN IMMEDIATE;\n" + APPROVAL_STORE_SCHEMA)
            self._connection.executemany(
                "INSERT INTO approval_store_meta(key,value) VALUES(?,?)",
                tuple(_expected_metadata(APPROVAL_STORE_SCHEMA_VERSION).items()),
            )
            self._connection.execute(
                """INSERT INTO approval_store_migrations(version,name,checksum,applied_at)
                   VALUES(?,?,?,?)""",
                (
                    APPROVAL_STORE_SCHEMA_VERSION,
                    _APPROVAL_STORE_MIGRATION_V2_NAME,
                    _APPROVAL_STORE_MIGRATION_V2_CHECKSUM,
                    int(time.time()),
                ),
            )
            self._connection.execute("COMMIT")
            self._connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        except Exception:
            if self._connection.in_transaction:
                self._connection.execute("ROLLBACK")
            raise

    def _maybe_migrate(self) -> None:
        metadata = self._read_metadata()
        if metadata == _expected_metadata(APPROVAL_STORE_SCHEMA_VERSION):
            return
        if metadata != _expected_metadata(1):
            raise GateBlocked("approval_store", "approval schema metadata mismatches")

        try:
            self._connection.execute("BEGIN IMMEDIATE")
        except sqlite3.OperationalError as exc:
            raise GateBlocked("approval_store_busy", "approval store is busy") from exc
        try:
            # Another exact process may have migrated while this connection
            # waited for the write lock. Re-read only after acquiring custody.
            metadata = self._read_metadata()
            if metadata == _expected_metadata(APPROVAL_STORE_SCHEMA_VERSION):
                self._connection.execute("COMMIT")
                return
            if metadata != _expected_metadata(1):
                raise GateBlocked("approval_store", "approval schema metadata mismatches")
            if _catalog(self._connection) != expected_catalog(1):
                raise GateBlocked(
                    "approval_store",
                    "approval schema catalog mismatches before migration",
                )

            for statement in _APPROVAL_STORE_MIGRATION_V2_STATEMENTS:
                self._connection.execute(statement)
            self._connection.execute(
                """INSERT INTO approval_store_migrations(version,name,checksum,applied_at)
                   VALUES(?,?,?,?)""",
                (
                    APPROVAL_STORE_SCHEMA_VERSION,
                    _APPROVAL_STORE_MIGRATION_V2_NAME,
                    _APPROVAL_STORE_MIGRATION_V2_CHECKSUM,
                    int(time.time()),
                ),
            )
            self._connection.executemany(
                "UPDATE approval_store_meta SET value=? WHERE key=?",
                tuple(
                    (value, key)
                    for key, value in _expected_metadata(APPROVAL_STORE_SCHEMA_VERSION).items()
                ),
            )
            if self._read_metadata() != _expected_metadata(APPROVAL_STORE_SCHEMA_VERSION):
                raise GateBlocked("approval_store", "approval migration metadata mismatches")
            if _catalog(self._connection) != expected_catalog(APPROVAL_STORE_SCHEMA_VERSION):
                raise GateBlocked(
                    "approval_store",
                    "approval migration did not produce the exact catalog",
                )
            self._connection.execute("COMMIT")
            self._connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        except Exception as exc:
            if self._connection.in_transaction:
                self._connection.execute("ROLLBACK")
            if isinstance(exc, GateBlocked):
                raise
            raise GateBlocked("approval_store", "approval schema migration failed") from exc

    def _verify_schema(self) -> None:
        metadata = self._read_metadata()
        if metadata != _expected_metadata(APPROVAL_STORE_SCHEMA_VERSION):
            raise GateBlocked("approval_store", "approval schema metadata mismatches")
        if _catalog(self._connection) != expected_catalog(APPROVAL_STORE_SCHEMA_VERSION):
            raise GateBlocked("approval_store", "approval schema catalog mismatches")
        integrity = self._connection.execute("PRAGMA quick_check").fetchone()
        if integrity is None or str(integrity[0]) != "ok":
            raise GateBlocked("approval_store", "approval database integrity check failed")

    @contextmanager
    def transaction(self) -> Iterator[sqlite3.Connection]:
        with self._lock:
            try:
                self._connection.execute("BEGIN IMMEDIATE")
            except sqlite3.OperationalError as exc:
                raise GateBlocked("approval_store_busy", "approval store is busy") from exc
            try:
                yield self._connection
                self._connection.execute("COMMIT")
            except Exception:
                if self._connection.in_transaction:
                    self._connection.execute("ROLLBACK")
                raise

    def fetch_one(self, sql: str, params: tuple[Any, ...] = ()) -> sqlite3.Row | None:
        with self._lock:
            return self._connection.execute(sql, params).fetchone()

    def fetch_all(self, sql: str, params: tuple[Any, ...] = ()) -> list[sqlite3.Row]:
        with self._lock:
            return list(self._connection.execute(sql, params).fetchall())

    def readiness(self) -> dict[str, Any]:
        try:
            self._verify_schema()
            counts = {
                "active_credentials": int(
                    self._connection.execute(
                        "SELECT COUNT(*) FROM approval_webauthn_credentials WHERE status='active'"
                    ).fetchone()[0]
                ),
                "pending_requests": int(
                    self._connection.execute(
                        "SELECT COUNT(*) FROM approval_requests WHERE state='pending'"
                    ).fetchone()[0]
                ),
            }
        except Exception as exc:
            return {"ready": False, "reason": type(exc).__name__}
        return {
            "ready": True,
            "schema_version": APPROVAL_STORE_SCHEMA_VERSION,
            "durability_claim": "single_host_local_only",
            **counts,
        }

    def close(self) -> None:
        with self._lock:
            self._connection.close()


__all__ = [
    "APPROVAL_STORE_SCHEMA",
    "APPROVAL_STORE_SCHEMA_V1",
    "APPROVAL_STORE_SCHEMA_V2",
    "APPROVAL_STORE_SCHEMA_VERSION",
    "ApprovalStore",
    "expected_catalog",
    "expected_catalog_digest",
]
