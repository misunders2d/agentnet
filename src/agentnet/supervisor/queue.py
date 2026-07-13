"""Encrypted crash-safe local supervisor inbox/outbox."""

from __future__ import annotations

import os
import sqlite3
import threading
import time
from pathlib import Path
from typing import Any
from uuid import uuid4

from agentnet.errors import ConflictError, IdempotencyConflict, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_digest


QUEUE_SCHEMA = """
CREATE TABLE IF NOT EXISTS queue (
    queue_id TEXT PRIMARY KEY,
    harness_id TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('inbox','outbox')),
    idempotency_key TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    payload_encrypted TEXT NOT NULL,
    state TEXT NOT NULL,
    available_at INTEGER NOT NULL,
    expires_at INTEGER,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(harness_id,direction,idempotency_key)
);
CREATE TABLE IF NOT EXISTS cursors (
    harness_id TEXT PRIMARY KEY,
    core_cursor INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS obligation_snapshots (
    harness_id TEXT PRIMARY KEY,
    snapshot_digest TEXT NOT NULL,
    snapshot_encrypted TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
"""

OBLIGATION_COUNTER_KEYS = frozenset(
    {
        "unread_information",
        "action_required",
        "awaiting_peer",
        "awaiting_human",
        "overdue",
        "failed",
    }
)


class LocalQueue:
    def __init__(self, path: Path, cipher: LocalEnvelopeCipher) -> None:
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if path.is_symlink():
            raise ValidationError("local queue database cannot be a symbolic link")
        self._connection = sqlite3.connect(path, isolation_level=None, check_same_thread=False)
        os.chmod(path, 0o600)
        self._connection.row_factory = sqlite3.Row
        self._lock = threading.RLock()
        self.cipher = cipher
        self._connection.execute("PRAGMA journal_mode=WAL")
        self._connection.execute("PRAGMA synchronous=FULL")
        self._connection.execute("PRAGMA foreign_keys=ON")
        self._connection.executescript(QUEUE_SCHEMA)

    def enqueue(
        self,
        *,
        harness_id: str,
        direction: str,
        idempotency_key: str,
        payload: dict[str, Any],
        expires_at: int | None = None,
    ) -> dict[str, Any]:
        if not harness_id or direction not in {"inbox", "outbox"}:
            raise ValidationError("local queue binding is invalid")
        if not 16 <= len(idempotency_key) <= 256:
            raise ValidationError("local queue idempotency key length is invalid")
        if expires_at is not None and expires_at <= int(time.time()):
            raise ValidationError("local queue expiry must be in the future")
        digest = canonical_digest(payload)
        now = int(time.time())
        with self._lock:
            self._connection.execute("BEGIN IMMEDIATE")
            try:
                existing = self._connection.execute(
                    "SELECT * FROM queue WHERE harness_id=? AND direction=? AND idempotency_key=?",
                    (harness_id, direction, idempotency_key),
                ).fetchone()
                if existing:
                    if existing["payload_digest"] != digest:
                        raise IdempotencyConflict("local queue idempotency key has different payload")
                    self._connection.execute("COMMIT")
                    return {"queue_id": existing["queue_id"], "state": existing["state"], "duplicate": True}
                queue_id = str(uuid4())
                encrypted = self.cipher.encrypt_json(payload, purpose=f"local-queue:{queue_id}")
                self._connection.execute(
                    """INSERT INTO queue(
                        queue_id,harness_id,direction,idempotency_key,payload_digest,payload_encrypted,
                        state,available_at,expires_at,created_at,updated_at
                    ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                    (queue_id, harness_id, direction, idempotency_key, digest, encrypted, "queued", now, expires_at, now, now),
                )
                self._connection.execute("COMMIT")
            except Exception:
                if self._connection.in_transaction:
                    self._connection.execute("ROLLBACK")
                raise
        return {"queue_id": queue_id, "state": "queued", "duplicate": False}

    def enqueue_inbox_with_cursor(
        self,
        *,
        harness_id: str,
        idempotency_key: str,
        payload: dict[str, Any],
        cursor: int,
        expires_at: int | None = None,
    ) -> dict[str, Any]:
        """Atomically take local custody and advance the remote cursor."""

        if not harness_id or cursor < 0 or not 16 <= len(idempotency_key) <= 256:
            raise ValidationError("atomic inbox custody binding is invalid")
        if expires_at is not None and expires_at <= int(time.time()):
            raise ValidationError("local queue expiry must be in the future")
        digest = canonical_digest(payload)
        now = int(time.time())
        with self._lock:
            self._connection.execute("BEGIN IMMEDIATE")
            try:
                existing = self._connection.execute(
                    "SELECT * FROM queue WHERE harness_id=? AND direction='inbox' AND idempotency_key=?",
                    (harness_id, idempotency_key),
                ).fetchone()
                if existing is not None:
                    if existing["payload_digest"] != digest:
                        raise IdempotencyConflict("local queue idempotency key has different payload")
                    result = {
                        "queue_id": existing["queue_id"],
                        "state": existing["state"],
                        "duplicate": True,
                    }
                else:
                    queue_id = str(uuid4())
                    encrypted = self.cipher.encrypt_json(payload, purpose=f"local-queue:{queue_id}")
                    self._connection.execute(
                        """INSERT INTO queue(
                            queue_id,harness_id,direction,idempotency_key,payload_digest,payload_encrypted,
                            state,available_at,expires_at,created_at,updated_at
                        ) VALUES(?,?,'inbox',?,?,?,?,?,?,?,?)""",
                        (
                            queue_id,
                            harness_id,
                            idempotency_key,
                            digest,
                            encrypted,
                            "queued",
                            now,
                            expires_at,
                            now,
                            now,
                        ),
                    )
                    result = {"queue_id": queue_id, "state": "queued", "duplicate": False}
                self._connection.execute(
                    """INSERT INTO cursors(harness_id,core_cursor) VALUES(?,?)
                       ON CONFLICT(harness_id) DO UPDATE SET core_cursor=MAX(core_cursor,excluded.core_cursor)""",
                    (harness_id, cursor),
                )
                self._connection.execute("COMMIT")
                return result
            except Exception:
                if self._connection.in_transaction:
                    self._connection.execute("ROLLBACK")
                raise

    def claim(self, *, harness_id: str, direction: str, limit: int = 25) -> list[dict[str, Any]]:
        if not harness_id or direction not in {"inbox", "outbox"} or not 1 <= limit <= 100:
            raise ValidationError("local queue claim binding or limit is invalid")
        now = int(time.time())
        with self._lock:
            self._connection.execute("BEGIN IMMEDIATE")
            try:
                self._connection.execute(
                    """UPDATE queue SET state='expired',updated_at=?
                       WHERE harness_id=? AND direction=? AND state IN ('queued','retry_scheduled')
                         AND expires_at IS NOT NULL AND expires_at<=?""",
                    (now, harness_id, direction, now),
                )
                rows = self._connection.execute(
                    """SELECT * FROM queue WHERE harness_id=? AND direction=? AND state IN ('queued','retry_scheduled')
                       AND available_at<=? AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at LIMIT ?""",
                    (harness_id, direction, now, now, limit),
                ).fetchall()
                result: list[dict[str, Any]] = []
                for row in rows:
                    self._connection.execute(
                        "UPDATE queue SET state='processing',attempts=attempts+1,updated_at=? WHERE queue_id=?",
                        (now, row["queue_id"]),
                    )
                    result.append(
                        {
                            "queue_id": row["queue_id"],
                            "payload": self.cipher.decrypt_json(row["payload_encrypted"], purpose=f"local-queue:{row['queue_id']}"),
                            "attempt": row["attempts"] + 1,
                        }
                    )
                self._connection.execute("COMMIT")
                return result
            except Exception:
                self._connection.execute("ROLLBACK")
                raise

    def complete(self, queue_id: str) -> None:
        with self._lock:
            cursor = self._connection.execute(
                "UPDATE queue SET state='completed',updated_at=? WHERE queue_id=? AND state='processing'",
                (int(time.time()), queue_id),
            )
            if cursor.rowcount != 1:
                raise ConflictError("only a processing queue item can complete")

    def complete_with_outbox(
        self,
        *,
        queue_id: str,
        harness_id: str,
        idempotency_key: str,
        payload: dict[str, Any],
    ) -> dict[str, Any]:
        """Atomically persist native output and acknowledge its claimed input."""

        if not queue_id or not harness_id or not 16 <= len(idempotency_key) <= 256:
            raise ValidationError("atomic queue acknowledgement binding is invalid")
        digest = canonical_digest(payload)
        now = int(time.time())
        outbox_id = str(uuid4())
        encrypted = self.cipher.encrypt_json(payload, purpose=f"local-queue:{outbox_id}")
        with self._lock:
            self._connection.execute("BEGIN IMMEDIATE")
            try:
                existing = self._connection.execute(
                    "SELECT * FROM queue WHERE harness_id=? AND direction='outbox' AND idempotency_key=?",
                    (harness_id, idempotency_key),
                ).fetchone()
                if existing:
                    if existing["payload_digest"] != digest:
                        raise IdempotencyConflict("local queue idempotency key has different payload")
                    outbox_id = existing["queue_id"]
                    result = {
                        "queue_id": outbox_id,
                        "state": existing["state"],
                        "duplicate": True,
                    }
                else:
                    self._connection.execute(
                        """INSERT INTO queue(
                            queue_id,harness_id,direction,idempotency_key,payload_digest,payload_encrypted,
                            state,available_at,expires_at,created_at,updated_at
                        ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                        (
                            outbox_id,
                            harness_id,
                            "outbox",
                            idempotency_key,
                            digest,
                            encrypted,
                            "queued",
                            now,
                            None,
                            now,
                            now,
                        ),
                    )
                    result = {"queue_id": outbox_id, "state": "queued", "duplicate": False}
                cursor = self._connection.execute(
                    """UPDATE queue SET state='completed',updated_at=?
                       WHERE queue_id=? AND harness_id=? AND direction='inbox' AND state='processing'""",
                    (now, queue_id, harness_id),
                )
                if cursor.rowcount != 1:
                    raise ConflictError("only the claimed inbox item can acknowledge native output")
                self._connection.execute("COMMIT")
                return result
            except Exception:
                if self._connection.in_transaction:
                    self._connection.execute("ROLLBACK")
                raise

    def retry(self, queue_id: str, *, delay_seconds: int) -> None:
        if delay_seconds < 0 or delay_seconds > 86_400:
            raise ValidationError("local retry delay is outside the bounded range")
        now = int(time.time())
        with self._lock:
            cursor = self._connection.execute(
                "UPDATE queue SET state='retry_scheduled',available_at=?,updated_at=? WHERE queue_id=? AND state='processing'",
                (now + delay_seconds, now, queue_id),
            )
            if cursor.rowcount != 1:
                raise ConflictError("only a processing queue item can be retried")

    def recover_processing(self) -> int:
        with self._lock:
            cursor = self._connection.execute(
                "UPDATE queue SET state='retry_scheduled',available_at=?,updated_at=? WHERE state='processing'",
                (int(time.time()), int(time.time())),
            )
            return cursor.rowcount

    def content_free_counts(self, harness_id: str) -> dict[str, int]:
        with self._lock:
            rows = self._connection.execute(
                """SELECT state,COUNT(*) AS count FROM queue
                    WHERE harness_id=? AND direction='inbox'
                      AND state IN ('queued','retry_scheduled','processing')
                    GROUP BY state""",
                (harness_id,),
            ).fetchall()
        return {row["state"]: row["count"] for row in rows}

    def store_obligation_snapshot(
        self,
        *,
        harness_id: str,
        counters: dict[str, int],
    ) -> None:
        """Durably retain only privacy-safe obligation attention counters."""

        if (
            not harness_id
            or set(counters) != OBLIGATION_COUNTER_KEYS
            or any(type(value) is not int or value < 0 for value in counters.values())
        ):
            raise ValidationError("obligation counter snapshot schema is invalid")
        digest = canonical_digest(counters)
        encrypted = self.cipher.encrypt_json(
            counters,
            purpose=f"obligation-snapshot:{harness_id}",
        )
        with self._lock:
            self._connection.execute(
                """INSERT INTO obligation_snapshots(
                       harness_id,snapshot_digest,snapshot_encrypted,updated_at
                   ) VALUES(?,?,?,?)
                   ON CONFLICT(harness_id) DO UPDATE SET
                       snapshot_digest=excluded.snapshot_digest,
                       snapshot_encrypted=excluded.snapshot_encrypted,
                       updated_at=excluded.updated_at""",
                (harness_id, digest, encrypted, int(time.time())),
            )

    def obligation_snapshot(self, harness_id: str) -> dict[str, int]:
        with self._lock:
            row = self._connection.execute(
                "SELECT snapshot_digest,snapshot_encrypted FROM obligation_snapshots WHERE harness_id=?",
                (harness_id,),
            ).fetchone()
        if row is None:
            return {key: 0 for key in OBLIGATION_COUNTER_KEYS}
        value = self.cipher.decrypt_json(
            row["snapshot_encrypted"],
            purpose=f"obligation-snapshot:{harness_id}",
        )
        if (
            not isinstance(value, dict)
            or set(value) != OBLIGATION_COUNTER_KEYS
            or any(type(item) is not int or item < 0 for item in value.values())
            or canonical_digest(value) != row["snapshot_digest"]
        ):
            raise ValidationError("durable obligation counter snapshot is invalid")
        return {key: int(value[key]) for key in OBLIGATION_COUNTER_KEYS}

    def cursor(self, harness_id: str) -> int:
        with self._lock:
            row = self._connection.execute("SELECT core_cursor FROM cursors WHERE harness_id=?", (harness_id,)).fetchone()
            return int(row["core_cursor"]) if row else 0

    def set_cursor(self, harness_id: str, cursor: int) -> None:
        if not harness_id or cursor < 0:
            raise ValidationError("local queue cursor binding is invalid")
        with self._lock:
            self._connection.execute(
                """INSERT INTO cursors(harness_id,core_cursor) VALUES(?,?)
                   ON CONFLICT(harness_id) DO UPDATE SET core_cursor=MAX(core_cursor,excluded.core_cursor)""",
                (harness_id, cursor),
            )

    def close(self) -> None:
        with self._lock:
            self._connection.close()
