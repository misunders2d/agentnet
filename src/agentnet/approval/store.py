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

from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json


APPROVAL_STORE_SCHEMA_VERSION = 4
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
_APPROVAL_STORE_MIGRATION_V2_NAME = "guided approval handoff"
_APPROVAL_STORE_MIGRATION_V2_CHECKSUM = hashlib.sha256(
    "\n".join(_APPROVAL_STORE_MIGRATION_V2_STATEMENTS).encode("utf-8")
).hexdigest()

_APPROVAL_STORE_MIGRATION_V3_STATEMENTS = (
    """CREATE TABLE approval_internal_broker_replay (
        key_id TEXT NOT NULL,
        nonce_hash TEXT NOT NULL CHECK(length(nonce_hash)=64),
        purpose TEXT NOT NULL,
        audience TEXT NOT NULL,
        method TEXT NOT NULL CHECK(method='POST'),
        path TEXT NOT NULL,
        body_sha256 TEXT NOT NULL CHECK(length(body_sha256)=64),
        issued_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL CHECK(expires_at > issued_at),
        consumed_at INTEGER NOT NULL,
        PRIMARY KEY(key_id,nonce_hash)
    )""",
    """CREATE INDEX idx_approval_internal_broker_replay_expiry
       ON approval_internal_broker_replay(expires_at)""",
)
APPROVAL_STORE_SCHEMA_V3 = (
    APPROVAL_STORE_SCHEMA_V2
    + "\n"
    + ";\n".join(_APPROVAL_STORE_MIGRATION_V3_STATEMENTS)
    + ";\n"
)
_APPROVAL_STORE_MIGRATION_V3_NAME = "signed approval broker replay custody"
_APPROVAL_STORE_MIGRATION_V3_CHECKSUM = hashlib.sha256(
    "\n".join(_APPROVAL_STORE_MIGRATION_V3_STATEMENTS).encode("utf-8")
).hexdigest()

_APPROVAL_STORE_MIGRATION_V4_STATEMENTS = (
    """CREATE TABLE approval_owner_bindings (
        binding_id TEXT PRIMARY KEY,
        domain_id TEXT NOT NULL,
        approver_principal_id TEXT NOT NULL,
        oidc_issuer TEXT NOT NULL,
        oidc_subject TEXT NOT NULL,
        verified_email TEXT NOT NULL,
        pin_source TEXT NOT NULL CHECK(pin_source IN ('exact_subject','verified_alias')),
        status TEXT NOT NULL CHECK(status IN ('active','revoked')),
        pinned_at INTEGER NOT NULL,
        revoked_at INTEGER,
        revocation_reason TEXT,
        UNIQUE(domain_id,approver_principal_id),
        UNIQUE(domain_id,oidc_issuer,oidc_subject),
        CHECK((status='active' AND revoked_at IS NULL) OR (status='revoked' AND revoked_at IS NOT NULL))
    )""",
    """CREATE INDEX idx_approval_owner_bindings_active
       ON approval_owner_bindings(domain_id,status,pinned_at)""",
    """CREATE TABLE approval_oidc_login_transactions (
        login_id TEXT PRIMARY KEY,
        preauth_session_hash TEXT NOT NULL CHECK(length(preauth_session_hash)=64),
        preauth_csrf_hash TEXT NOT NULL CHECK(length(preauth_csrf_hash)=64),
        completed_session_hash TEXT UNIQUE CHECK(
            completed_session_hash IS NULL OR length(completed_session_hash)=64
        ),
        oidc_issuer TEXT NOT NULL,
        client_id TEXT NOT NULL,
        redirect_uri TEXT NOT NULL,
        state_hash TEXT NOT NULL UNIQUE CHECK(length(state_hash)=64),
        nonce_hash TEXT NOT NULL UNIQUE CHECK(length(nonce_hash)=64),
        code_verifier_encrypted TEXT NOT NULL,
        state TEXT NOT NULL CHECK(state IN (
            'pending','callback_claimed','callback_consumed','failed','expired','canceled'
        )),
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL CHECK(expires_at > created_at),
        callback_claimed_at INTEGER,
        callback_consumed_at INTEGER,
        owner_binding_id TEXT REFERENCES approval_owner_bindings(binding_id),
        failure_code TEXT,
        CHECK(
            (state='pending' AND callback_claimed_at IS NULL
                AND callback_consumed_at IS NULL AND completed_session_hash IS NULL)
            OR
            (state='callback_claimed' AND callback_claimed_at IS NOT NULL
                AND callback_consumed_at IS NULL AND completed_session_hash IS NULL)
            OR
            (state='callback_consumed' AND callback_claimed_at IS NOT NULL
                AND callback_consumed_at IS NOT NULL
                AND completed_session_hash IS NOT NULL AND owner_binding_id IS NOT NULL)
            OR
            (state IN ('failed','expired','canceled'))
        )
    )""",
    """CREATE INDEX idx_approval_oidc_login_expiry
       ON approval_oidc_login_transactions(state,expires_at)""",
    """CREATE TABLE approval_browser_sessions (
        session_hash TEXT PRIMARY KEY CHECK(length(session_hash)=64),
        owner_binding_id TEXT NOT NULL REFERENCES approval_owner_bindings(binding_id) ON DELETE RESTRICT,
        csrf_secret_encrypted TEXT NOT NULL,
        rp_id TEXT NOT NULL,
        public_origin TEXT NOT NULL,
        verifier_id TEXT NOT NULL,
        rotated_from_hash TEXT UNIQUE CHECK(
            rotated_from_hash IS NULL OR length(rotated_from_hash)=64
        ),
        created_at INTEGER NOT NULL,
        authenticated_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL CHECK(expires_at > authenticated_at),
        revoked_at INTEGER,
        revocation_reason TEXT
    )""",
    """CREATE INDEX idx_approval_browser_sessions_active
       ON approval_browser_sessions(owner_binding_id,expires_at,revoked_at)""",
    """CREATE TABLE approval_registration_budgets (
        owner_binding_id TEXT PRIMARY KEY REFERENCES approval_owner_bindings(binding_id) ON DELETE RESTRICT,
        failed_attempts_total INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts_total BETWEEN 0 AND 20),
        challenge_rotations INTEGER NOT NULL DEFAULT 0 CHECK(challenge_rotations BETWEEN 0 AND 10),
        updated_at INTEGER NOT NULL
    )""",
    """CREATE TABLE approval_registration_ceremonies (
        ceremony_id TEXT PRIMARY KEY,
        owner_binding_id TEXT NOT NULL REFERENCES approval_owner_bindings(binding_id) ON DELETE RESTRICT,
        session_hash TEXT NOT NULL REFERENCES approval_browser_sessions(session_hash) ON DELETE RESTRICT,
        challenge_encrypted TEXT NOT NULL,
        challenge_hash TEXT NOT NULL UNIQUE CHECK(length(challenge_hash)=64),
        state TEXT NOT NULL CHECK(state IN ('pending','verified','failed','expired','canceled')),
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL CHECK(expires_at > created_at),
        completed_at INTEGER,
        failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts BETWEEN 0 AND 10),
        CHECK(
            (state='pending' AND completed_at IS NULL)
            OR (state='verified' AND completed_at IS NOT NULL)
            OR (state IN ('failed','expired','canceled'))
        )
    )""",
    """CREATE INDEX idx_approval_registration_ceremonies_active
       ON approval_registration_ceremonies(owner_binding_id,state,expires_at)""",
    """ALTER TABLE approval_requests
       ADD COLUMN claim_code_failed_attempts_total INTEGER NOT NULL DEFAULT 0
       CHECK(claim_code_failed_attempts_total BETWEEN 0 AND 20)""",
    """ALTER TABLE approval_requests
       ADD COLUMN claim_code_rotations INTEGER NOT NULL DEFAULT 0
       CHECK(claim_code_rotations BETWEEN 0 AND 10)""",
    """ALTER TABLE approval_requests
       ADD COLUMN challenge_owner_session_hash TEXT
       REFERENCES approval_browser_sessions(session_hash) ON DELETE RESTRICT
       CHECK(challenge_owner_session_hash IS NULL OR length(challenge_owner_session_hash)=64)""",
)
APPROVAL_STORE_SCHEMA_V4 = (
    APPROVAL_STORE_SCHEMA_V3
    + "\n"
    + ";\n".join(_APPROVAL_STORE_MIGRATION_V4_STATEMENTS)
    + ";\n"
)
# Current schema alias retained for callers that imported the original name.
APPROVAL_STORE_SCHEMA = APPROVAL_STORE_SCHEMA_V4
_APPROVAL_STORE_MIGRATION_V4_NAME = "stable owner session and registration ceremonies"
_APPROVAL_STORE_MIGRATION_V4_CHECKSUM = hashlib.sha256(
    "\n".join(_APPROVAL_STORE_MIGRATION_V4_STATEMENTS).encode("utf-8")
).hexdigest()
_MIGRATION_RECORDS = {
    2: (_APPROVAL_STORE_MIGRATION_V2_NAME, _APPROVAL_STORE_MIGRATION_V2_CHECKSUM),
    3: (_APPROVAL_STORE_MIGRATION_V3_NAME, _APPROVAL_STORE_MIGRATION_V3_CHECKSUM),
    4: (_APPROVAL_STORE_MIGRATION_V4_NAME, _APPROVAL_STORE_MIGRATION_V4_CHECKSUM),
}

_CATALOG_QUERY = (
    "SELECT type,name,tbl_name,sql FROM sqlite_master "
    "WHERE type IN ('table','index','trigger') AND sql IS NOT NULL "
    "AND name NOT LIKE 'sqlite_%' ORDER BY type,name"
)


def _catalog(connection: sqlite3.Connection) -> tuple[tuple[str, str, str, str], ...]:
    return tuple(tuple(str(value) for value in row) for row in connection.execute(_CATALOG_QUERY))


@lru_cache(maxsize=4)
def expected_catalog(
    version: int = APPROVAL_STORE_SCHEMA_VERSION,
) -> tuple[tuple[str, str, str, str], ...]:
    schemas = {
        1: APPROVAL_STORE_SCHEMA_V1,
        2: APPROVAL_STORE_SCHEMA_V2,
        3: APPROVAL_STORE_SCHEMA_V3,
        4: APPROVAL_STORE_SCHEMA_V4,
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


@lru_cache(maxsize=4)
def expected_catalog_digest(version: int = APPROVAL_STORE_SCHEMA_VERSION) -> str:
    payload = "\n".join("\x1f".join(row) for row in expected_catalog(version)).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _expected_metadata(version: int) -> dict[str, str]:
    return {
        "schema_version": str(version),
        "schema_catalog_sha256": expected_catalog_digest(version),
    }


def _verify_migration_history(connection: sqlite3.Connection, version: int) -> None:
    expected = [
        (migration_version, name, checksum)
        for migration_version, (name, checksum) in sorted(_MIGRATION_RECORDS.items())
        if migration_version <= version
    ]
    try:
        observed = [
            (int(row[0]), str(row[1]), str(row[2]))
            for row in connection.execute(
                "SELECT version,name,checksum FROM approval_store_migrations ORDER BY version"
            ).fetchall()
        ]
    except sqlite3.DatabaseError as exc:
        raise GateBlocked("approval_store", "approval migration history is unavailable") from exc
    if observed != expected:
        raise GateBlocked("approval_store", "approval migration history mismatches")


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
def approval_user_handle(*, verifier_id: str, principal_id: str, domain_id: str) -> bytes:
    """Derive the stable WebAuthn user handle for one exact approval principal."""

    return hashlib.sha256(
        canonical_json(
            {
                "schema": "agentnet.approval.webauthn-user.v1",
                "verifier_id": verifier_id,
                "domain_id": domain_id,
                "approver_principal_id": principal_id,
            }
        )
    ).digest()




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
            applied_at = int(time.time())
            self._connection.executemany(
                """INSERT INTO approval_store_migrations(version,name,checksum,applied_at)
                   VALUES(?,?,?,?)""",
                tuple(
                    (version, name, checksum, applied_at)
                    for version, (name, checksum) in sorted(_MIGRATION_RECORDS.items())
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
        if metadata not in (
            _expected_metadata(1),
            _expected_metadata(2),
            _expected_metadata(3),
        ):
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
            if metadata not in (
                _expected_metadata(1),
                _expected_metadata(2),
                _expected_metadata(3),
            ):
                raise GateBlocked("approval_store", "approval schema metadata mismatches")

            source_version = next(
                version
                for version in (1, 2, 3)
                if metadata == _expected_metadata(version)
            )
            if _catalog(self._connection) != expected_catalog(source_version):
                raise GateBlocked(
                    "approval_store",
                    "approval schema catalog mismatches before migration",
                )
            if source_version >= 2:
                _verify_migration_history(self._connection, source_version)

            applied_at = int(time.time())

            def apply_migration(
                version: int,
                statements: tuple[str, ...],
                name: str,
                checksum: str,
            ) -> None:
                for statement in statements:
                    self._connection.execute(statement)
                self._connection.execute(
                    """INSERT INTO approval_store_migrations(version,name,checksum,applied_at)
                       VALUES(?,?,?,?)""",
                    (version, name, checksum, applied_at),
                )
                if _catalog(self._connection) != expected_catalog(version):
                    raise GateBlocked(
                        "approval_store",
                        f"approval migration did not produce the exact v{version} catalog",
                    )
                _verify_migration_history(self._connection, version)

            if source_version < 2:
                apply_migration(
                    2,
                    _APPROVAL_STORE_MIGRATION_V2_STATEMENTS,
                    _APPROVAL_STORE_MIGRATION_V2_NAME,
                    _APPROVAL_STORE_MIGRATION_V2_CHECKSUM,
                )
            if source_version < 3:
                apply_migration(
                    3,
                    _APPROVAL_STORE_MIGRATION_V3_STATEMENTS,
                    _APPROVAL_STORE_MIGRATION_V3_NAME,
                    _APPROVAL_STORE_MIGRATION_V3_CHECKSUM,
                )
            apply_migration(
                4,
                _APPROVAL_STORE_MIGRATION_V4_STATEMENTS,
                _APPROVAL_STORE_MIGRATION_V4_NAME,
                _APPROVAL_STORE_MIGRATION_V4_CHECKSUM,
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
            _verify_migration_history(self._connection, APPROVAL_STORE_SCHEMA_VERSION)
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
        _verify_migration_history(self._connection, APPROVAL_STORE_SCHEMA_VERSION)
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

    def consume_internal_broker_replay(
        self,
        *,
        key_id: str,
        nonce: str,
        purpose: str,
        audience: str,
        method: str,
        path: str,
        body_sha256: str,
        issued_at: int,
        expires_at: int,
        consumed_at: int | None = None,
    ) -> None:
        """Persist one-use broker proof custody before route action."""

        checked_at = int(time.time()) if consumed_at is None else consumed_at
        valid = (
            isinstance(key_id, str)
            and 1 <= len(key_id) <= 128
            and isinstance(nonce, str)
            and 1 <= len(nonce) <= 128
            and isinstance(purpose, str)
            and 1 <= len(purpose) <= 256
            and isinstance(audience, str)
            and 1 <= len(audience) <= 512
            and method == "POST"
            and isinstance(path, str)
            and path.startswith("/v1/approval/internal/")
            and isinstance(body_sha256, str)
            and len(body_sha256) == 64
            and all(character in "0123456789abcdef" for character in body_sha256)
            and isinstance(issued_at, int)
            and not isinstance(issued_at, bool)
            and isinstance(expires_at, int)
            and not isinstance(expires_at, bool)
            and isinstance(checked_at, int)
            and not isinstance(checked_at, bool)
            and issued_at <= checked_at < expires_at
        )
        if not valid:
            raise AuthenticationError("approval request denied")
        try:
            nonce_hash = hashlib.sha256(nonce.encode("ascii")).hexdigest()
        except (UnicodeEncodeError, AttributeError) as exc:
            raise AuthenticationError("approval request denied") from exc

        with self.transaction() as connection:
            try:
                connection.execute(
                    "DELETE FROM approval_internal_broker_replay WHERE expires_at <= ?",
                    (checked_at,),
                )
                connection.execute(
                    """INSERT INTO approval_internal_broker_replay(
                           key_id,nonce_hash,purpose,audience,method,path,body_sha256,
                           issued_at,expires_at,consumed_at
                       ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                    (
                        key_id,
                        nonce_hash,
                        purpose,
                        audience,
                        method,
                        path,
                        body_sha256,
                        issued_at,
                        expires_at,
                        checked_at,
                    ),
                )
            except sqlite3.IntegrityError as exc:
                extended = getattr(exc, "sqlite_errorcode", None)
                if extended in {
                    getattr(sqlite3, "SQLITE_CONSTRAINT_PRIMARYKEY", -1),
                    getattr(sqlite3, "SQLITE_CONSTRAINT_UNIQUE", -1),
                }:
                    raise AuthenticationError("approval request denied") from exc
                raise GateBlocked("approval_store", "approval replay custody failed") from exc
            except sqlite3.DatabaseError as exc:
                raise GateBlocked("approval_store", "approval replay custody failed") from exc

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
    "APPROVAL_STORE_SCHEMA_V3",
    "APPROVAL_STORE_SCHEMA_V4",
    "APPROVAL_STORE_SCHEMA_VERSION",
    "approval_user_handle",
    "ApprovalStore",
    "expected_catalog",
    "expected_catalog_digest",
]
