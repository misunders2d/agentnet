from __future__ import annotations

import sqlite3
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

import agentnet.approval.store as store_module
from agentnet.approval.store import (
    APPROVAL_STORE_SCHEMA_V1,
    APPROVAL_STORE_SCHEMA_VERSION,
    ApprovalStore,
    expected_catalog,
    expected_catalog_digest,
)
from agentnet.errors import GateBlocked
from agentnet.security.envelope import LocalEnvelopeCipher


def _v1_database(tmp_path: Path, *, with_request: bool = False) -> Path:
    root = tmp_path / "approval"
    root.mkdir(mode=0o700)
    root.chmod(0o700)
    path = root / "approval.sqlite3"
    path.touch(mode=0o600)
    path.chmod(0o600)
    connection = sqlite3.connect(path, isolation_level=None)
    try:
        connection.executescript("BEGIN IMMEDIATE;\n" + APPROVAL_STORE_SCHEMA_V1)
        connection.executemany(
            "INSERT INTO approval_store_meta(key,value) VALUES(?,?)",
            (
                ("schema_version", "1"),
                ("schema_catalog_sha256", expected_catalog_digest(1)),
            ),
        )
        if with_request:
            connection.execute(
                """INSERT INTO approval_requests(
                       request_id,approver_principal_id,domain_id,approval_purpose,
                       capability_hash,canonical_transaction_encrypted,transaction_digest,
                       state,active_fingerprint,created_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    "request-1",
                    "security-owner",
                    "corp.example",
                    "identity.enrollment.approve",
                    "a" * 64,
                    "encrypted-transaction",
                    "b" * 64,
                    "pending",
                    "c" * 64,
                    1_800_000_000,
                    1_800_000_300,
                ),
            )
        connection.execute("COMMIT")
    finally:
        connection.close()
    return path


def _metadata(path: Path) -> dict[str, str]:
    connection = sqlite3.connect(path)
    try:
        return {
            str(key): str(value)
            for key, value in connection.execute(
                "SELECT key,value FROM approval_store_meta"
            ).fetchall()
        }
    finally:
        connection.close()


def test_clean_initialize_creates_exact_v2_catalog(tmp_path: Path) -> None:
    path = _v1_database(tmp_path)
    path.unlink()
    path.touch(mode=0o600)
    path.chmod(0o600)
    store = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32), initialize=True)
    try:
        assert store.readiness()["schema_version"] == APPROVAL_STORE_SCHEMA_VERSION
        assert store.fetch_one(
            "SELECT name,checksum FROM approval_store_migrations WHERE version=2"
        ) is not None
        assert store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_claim_codes"
        )["n"] == 0
    finally:
        store.close()


def test_v1_database_migrates_atomically_and_preserves_existing_request(tmp_path: Path) -> None:
    path = _v1_database(tmp_path, with_request=True)
    store = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32))
    try:
        assert store.readiness()["schema_version"] == 2
        row = store.fetch_one(
            "SELECT delivery_mode,capability_encrypted FROM approval_requests WHERE request_id=?",
            ("request-1",),
        )
        assert row is not None
        assert dict(row) == {
            "delivery_mode": "direct_receipt",
            "capability_encrypted": None,
        }
        assert store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_store_migrations WHERE version=2"
        )["n"] == 1
    finally:
        store.close()


def test_v1_catalog_tamper_is_rejected_before_migration(tmp_path: Path) -> None:
    path = _v1_database(tmp_path)
    connection = sqlite3.connect(path, isolation_level=None)
    try:
        connection.execute("CREATE TABLE rogue_object(value TEXT)")
    finally:
        connection.close()

    with pytest.raises(GateBlocked, match="catalog mismatches before migration"):
        ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32))
    assert _metadata(path)["schema_version"] == "1"


def test_failed_v1_migration_rolls_back_every_schema_change(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = _v1_database(tmp_path)
    original = store_module._APPROVAL_STORE_MIGRATION_V2_STATEMENTS
    monkeypatch.setattr(
        store_module,
        "_APPROVAL_STORE_MIGRATION_V2_STATEMENTS",
        (original[0], "CREATE TABLE broken_migration("),
    )
    with pytest.raises(GateBlocked, match="migration failed"):
        ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32))

    connection = sqlite3.connect(path)
    try:
        columns = {
            str(row[1]) for row in connection.execute("PRAGMA table_info(approval_requests)")
        }
        assert "delivery_mode" not in columns
        assert store_module._catalog(connection) == expected_catalog(1)
    finally:
        connection.close()
    assert _metadata(path)["schema_version"] == "1"

    monkeypatch.setattr(
        store_module,
        "_APPROVAL_STORE_MIGRATION_V2_STATEMENTS",
        original,
    )
    migrated = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32))
    migrated.close()


def test_concurrent_v1_open_converges_on_one_v2_catalog(tmp_path: Path) -> None:
    path = _v1_database(tmp_path)

    def open_and_check() -> int:
        store = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32))
        try:
            return int(store.readiness()["schema_version"])
        finally:
            store.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        versions = list(pool.map(lambda _index: open_and_check(), range(2)))
    assert versions == [2, 2]
    assert _metadata(path)["schema_version"] == "2"
