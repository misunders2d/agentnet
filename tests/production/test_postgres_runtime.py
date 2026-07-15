from __future__ import annotations

import hashlib
import os
import sqlite3
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest
import httpx
import psycopg

from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.http_api import create_app
from agentnet.messaging.events import new_event
from agentnet.mailbox.service import MailboxService
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.operations.quotas import QuotaService
from agentnet.organization.conflicts import (
    TaskAccessMode,
    TaskConflictAdjudication,
    TaskConflictService,
    TaskExecutionIntent,
    TaskExclusivity,
    TaskResourceIntent,
)
from agentnet.protocol.models import Classification, DeliveryFact, EventType
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS, validate_migration_catalog
from agentnet.storage.relationship_governance_schema import (
    RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES,
    RELATIONSHIP_GOVERNANCE_SCHEMA_VERSION,
    RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA,
    require_relationship_governance_schema,
)
from agentnet.storage.post_audit_schema import (
    POST_AUDIT_REQUIRED_INDEXES,
    POST_AUDIT_REQUIRED_TABLES,
    POST_AUDIT_SCHEMA_VERSION,
    require_post_audit_schema,
)
from agentnet.storage.postgres import (
    PostgreSQLStore,
    apply_postgres_migrations,
    translate_qmark_sql,
    validate_applied_migrations,
    validate_postgres_recovery_dsn,
)
from agentnet.storage.recovery import probe_filesystem_artifact_recovery
from agentnet.storage.sqlite import SQLiteStore


def server_config(tmp_path: Path, *, instance_id: str = "server-agent-test") -> ExtensionConfig:
    return ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id="server-agent.example",
        data_dir=tmp_path / "data",
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        artifact_dir=tmp_path / "artifacts",
        runtime_instance_id=instance_id,
        enrolled_harness_id=f"enrolled-{instance_id}",
        enrolled_credential_id=f"credential-{instance_id}",
    )


def test_numbered_migration_catalog_preserves_v1_and_adds_protected_release(
    tmp_path: Path,
) -> None:
    validate_migration_catalog()
    assert CURRENT_SCHEMA_VERSION == 2
    assert [(migration.version, migration.name) for migration in MIGRATIONS] == [
        (1, "agentnet_first_release_schema"),
        (2, "protected_task_payload_release"),
    ]
    assert (
        MIGRATIONS[0].checksum
        == "c472c4442fce9195580bd55d6f01d831f9ef34cb8cc34b8389b72b1c572d484f"
    )
    schema = "\n".join(migration.sql for migration in MIGRATIONS)
    assert "AUTOINCREMENT" not in schema
    assert " INTEGER" not in schema
    assert "BIGSERIAL" in schema
    assert schema.index("CREATE TABLE IF NOT EXISTS policy_decisions") < schema.index(
        "CREATE TABLE IF NOT EXISTS effect_reservations"
    )

    # Every table and explicit index in the local schema has a PostgreSQL
    # counterpart in the immutable numbered production migrations.
    sqlite = SQLiteStore(tmp_path / "catalog-shape.sqlite3", LocalEnvelopeCipher(b"s" * 32))
    try:
        objects = sqlite.fetch_all(
            "SELECT type,name FROM sqlite_master "
            "WHERE type IN ('table','index') AND sql IS NOT NULL "
            "AND name NOT LIKE 'sqlite_%' ORDER BY type,name"
        )
    finally:
        sqlite.close()
    for row in objects:
        if row["type"] == "table":
            assert f"CREATE TABLE IF NOT EXISTS {row['name']}" in schema
        else:
            assert f"INDEX IF NOT EXISTS {row['name']}" in schema
    for required in (
        "actor_json TEXT NOT NULL",
        "policy_decision_id TEXT NOT NULL REFERENCES policy_decisions(decision_id)",
        "CREATE TABLE IF NOT EXISTS audit_intents",
        "CREATE TABLE IF NOT EXISTS artifact_release_outbox",
        "CREATE TABLE IF NOT EXISTS server_agent_relay_outbox",
        "CREATE TABLE IF NOT EXISTS server_agent_relay_inbox",
        "CREATE TABLE IF NOT EXISTS telemetry_counters",
        "CREATE TABLE IF NOT EXISTS runtime_leases",
        "CREATE INDEX IF NOT EXISTS runtime_leases_expiry_idx",
        "CREATE TABLE IF NOT EXISTS artifact_recovery_observations",
        "CREATE TABLE IF NOT EXISTS artifact_byte_accounts",
        "CREATE TABLE IF NOT EXISTS artifact_byte_charges",
        "CREATE INDEX IF NOT EXISTS idx_artifact_reservations_expiry_state",
        "CREATE TABLE IF NOT EXISTS task_payload_releases",
        "CREATE INDEX IF NOT EXISTS idx_task_payload_releases_recipient",
    ):
        assert required in schema
    assert "ON CONFLICT(reservation_id) DO NOTHING" in schema

    assert RELATIONSHIP_GOVERNANCE_SCHEMA_VERSION == 1
    for table in (
        "relationship_governance_lineages",
        "relationship_governance_transactions",
        "relationship_policy_exceptions",
    ):
        assert f"CREATE TABLE IF NOT EXISTS {table}" in schema
    for index in RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES:
        assert f"INDEX IF NOT EXISTS {index}" in schema
    assert "CREATE TABLE IF NOT EXISTS relationships" not in schema
    assert "relationship_legacy_quarantine" not in schema
    assert "legacy_unilateral_relationship" not in schema
    assert "UPDATE relationships" not in schema

    assert POST_AUDIT_SCHEMA_VERSION == 1
    for table in POST_AUDIT_REQUIRED_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in schema
    for index in POST_AUDIT_REQUIRED_INDEXES:
        assert f"INDEX IF NOT EXISTS {index}" in schema


def _sqlite_logical_snapshot(path: Path) -> tuple[str, ...]:
    connection = sqlite3.connect(path)
    try:
        return tuple(connection.iterdump())
    finally:
        connection.close()


def _sqlite_physical_state(path: Path) -> tuple[str, int, str, tuple[tuple[str, bool, str], ...]]:
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    mode = path.stat().st_mode & 0o777
    connection = sqlite3.connect(f"file:{path}?mode=ro&immutable=1", uri=True)
    try:
        journal_mode = str(connection.execute("PRAGMA journal_mode").fetchone()[0])
    finally:
        connection.close()
    sidecars = tuple(
        (
            suffix,
            candidate.exists(),
            hashlib.sha256(candidate.read_bytes()).hexdigest() if candidate.is_file() else "",
        )
        for suffix in ("-wal", "-shm", "-journal")
        if (candidate := Path(str(path) + suffix))
    )
    return digest, mode, journal_mode, sidecars


def test_sqlite_fresh_database_records_exact_first_release_catalog(tmp_path: Path) -> None:
    path = tmp_path / "fresh.sqlite3"
    store = SQLiteStore(path, LocalEnvelopeCipher(b"g" * 32))
    try:
        assert store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")["value"] == str(
            CURRENT_SCHEMA_VERSION
        )
        assert [tuple(row) for row in store.fetch_all(
            "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
        )] == [
            (migration.version, migration.name, migration.checksum)
            for migration in MIGRATIONS
        ]
        require_relationship_governance_schema(store)
        require_post_audit_schema(store)
        for table in POST_AUDIT_REQUIRED_TABLES | {
            "relationship_governance_lineages",
            "relationship_governance_transactions",
            "relationship_policy_exceptions",
        }:
            assert store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"] == 0
        assert store.fetch_one(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='relationships'"
        ) is None
        assert store.fetch_one(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='relationship_legacy_quarantine'"
        ) is None
    finally:
        store.close()


def _make_exact_v1_sqlite(path: Path, *, key: bytes) -> None:
    store = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        with store.transaction() as connection:
            connection.execute(
                "INSERT INTO metadata(key,value) VALUES('preserved-sentinel','exact-v1-data')"
            )
    finally:
        store.close()

    v1 = sqlite3.connect(path)
    try:
        v1.execute("DROP INDEX idx_task_payload_releases_recipient")
        v1.execute("DROP TABLE task_payload_releases")
        v1.execute("DELETE FROM installed_migration_catalog WHERE version=2")
        v1.execute("DROP TRIGGER trg_relationship_governance_schema_floor_update")
        v1.execute("DROP TRIGGER trg_relationship_governance_schema_floor_insert")
        v1.execute("UPDATE metadata SET value='1' WHERE key='schema_version'")
        v1.executescript(RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA)
        v1.commit()
    finally:
        v1.close()


def test_sqlite_exact_v1_database_upgrades_to_v2_without_data_loss(
    tmp_path: Path,
) -> None:
    path = tmp_path / "v1-upgrade.sqlite3"
    key = b"u" * 32
    _make_exact_v1_sqlite(path, key=key)

    upgraded = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        assert upgraded.fetch_one(
            "SELECT value FROM metadata WHERE key='schema_version'"
        )["value"] == "2"
        assert upgraded.fetch_one(
            "SELECT value FROM metadata WHERE key='preserved-sentinel'"
        )["value"] == "exact-v1-data"
        assert [tuple(row) for row in upgraded.fetch_all(
            "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
        )] == [
            (migration.version, migration.name, migration.checksum)
            for migration in MIGRATIONS
        ]
        assert upgraded.fetch_one(
            "SELECT COUNT(*) AS count FROM task_payload_releases"
        )["count"] == 0
    finally:
        upgraded.close()


def test_sqlite_v1_to_v2_migration_failure_rolls_back_without_partial_schema(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = tmp_path / "v1-upgrade-rollback.sqlite3"
    key = b"v" * 32
    _make_exact_v1_sqlite(path, key=key)
    before = _sqlite_logical_snapshot(path)
    monkeypatch.setitem(
        __import__("agentnet.storage.sqlite", fromlist=["_SQLITE_MIGRATION_SQL"])
        ._SQLITE_MIGRATION_SQL,
        2,
        "CREATE TABLE migration_partial(value TEXT); SELECT missing_function();",
    )

    with pytest.raises(sqlite3.OperationalError, match="missing_function"):
        SQLiteStore(path, LocalEnvelopeCipher(key))

    assert _sqlite_logical_snapshot(path) == before
    raw = sqlite3.connect(path)
    try:
        assert raw.execute(
            "SELECT name FROM sqlite_master WHERE name='migration_partial'"
        ).fetchone() is None
        assert raw.execute(
            "SELECT value FROM metadata WHERE key='schema_version'"
        ).fetchone()[0] == "1"
        assert raw.execute(
            "SELECT COUNT(*) FROM installed_migration_catalog"
        ).fetchone()[0] == 1
    finally:
        raw.close()


def test_sqlite_nonempty_unversioned_prototype_fails_closed_without_mutation(
    tmp_path: Path,
) -> None:
    path = tmp_path / "prototype.sqlite3"
    prototype = sqlite3.connect(path)
    prototype.execute("CREATE TABLE prototype_events(event_id TEXT PRIMARY KEY,payload TEXT NOT NULL)")
    prototype.execute("INSERT INTO prototype_events VALUES('event-1','must-remain')")
    prototype.commit()
    prototype.close()
    path.chmod(0o600)
    before = _sqlite_logical_snapshot(path)
    physical_before = _sqlite_physical_state(path)

    with pytest.raises(GateBlocked, match="pre-release SQLite databases"):
        SQLiteStore(path, LocalEnvelopeCipher(b"l" * 32))

    assert _sqlite_logical_snapshot(path) == before
    assert _sqlite_physical_state(path) == physical_before


def test_sqlite_pre_release_multiversion_catalog_fails_closed_without_mutation(
    tmp_path: Path,
) -> None:
    path = tmp_path / "pre-release-catalog.sqlite3"
    old = sqlite3.connect(path)
    old.execute("CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
    old.execute(
        "CREATE TABLE installed_migration_catalog("
        "version INTEGER PRIMARY KEY,name TEXT NOT NULL,checksum TEXT NOT NULL)"
    )
    old.execute("INSERT INTO metadata VALUES('schema_version','19')")
    old.executemany(
        "INSERT INTO installed_migration_catalog VALUES(?,?,?)",
        [(version, f"prototype_{version}", str(version).zfill(64)) for version in range(1, 20)],
    )
    old.commit()
    old.close()
    path.chmod(0o600)
    before = _sqlite_logical_snapshot(path)
    physical_before = _sqlite_physical_state(path)

    with pytest.raises(GateBlocked, match="newer than this extension"):
        SQLiteStore(path, LocalEnvelopeCipher(b"l" * 32))

    assert _sqlite_logical_snapshot(path) == before
    assert _sqlite_physical_state(path) == physical_before


def test_sqlite_existing_database_requires_owner_only_mode_without_chmod(
    tmp_path: Path,
) -> None:
    path = tmp_path / "unsafe-mode.sqlite3"
    connection = sqlite3.connect(path)
    connection.execute("CREATE TABLE prototype(value TEXT NOT NULL)")
    connection.commit()
    connection.close()
    path.chmod(0o644)
    before = _sqlite_physical_state(path)

    with pytest.raises(GateBlocked, match="already be owner-only"):
        SQLiteStore(path, LocalEnvelopeCipher(b"m" * 32))

    assert _sqlite_physical_state(path) == before


def test_sqlite_rejects_hardlinked_database_without_sidecar_or_content_mutation(
    tmp_path: Path,
) -> None:
    path = tmp_path / "primary.sqlite3"
    store = SQLiteStore(path, LocalEnvelopeCipher(b"n" * 32))
    store.close()
    alias = tmp_path / "alias.sqlite3"
    os.link(path, alias)
    before = _sqlite_physical_state(alias)

    with pytest.raises(GateBlocked, match="singly linked"):
        SQLiteStore(alias, LocalEnvelopeCipher(b"n" * 32))

    assert _sqlite_physical_state(alias) == before
    assert not Path(str(alias) + "-wal").exists()
    assert not Path(str(alias) + "-shm").exists()


def test_sqlite_failed_new_database_initialization_removes_exact_file_and_sidecars(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = tmp_path / "failed-new.sqlite3"
    real_connect = sqlite3.connect

    class FailingPragmaConnection(sqlite3.Connection):
        def execute(self, sql, parameters=()):
            if str(sql).strip().upper() == "PRAGMA SYNCHRONOUS=FULL":
                raise sqlite3.OperationalError("injected synchronous pragma failure")
            return super().execute(sql, parameters)

    def failing_connect(*args, **kwargs):
        kwargs["factory"] = FailingPragmaConnection
        return real_connect(*args, **kwargs)

    monkeypatch.setattr("agentnet.storage.sqlite.sqlite3.connect", failing_connect)
    with pytest.raises(sqlite3.OperationalError, match="injected"):
        SQLiteStore(path, LocalEnvelopeCipher(b"o" * 32))

    assert not path.exists()
    for suffix in ("-wal", "-shm", "-journal"):
        assert not Path(str(path) + suffix).exists()


def test_relationship_governance_schema_verifier_fails_closed_on_missing_index(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "governance-schema.sqlite3", LocalEnvelopeCipher(b"h" * 32))
    try:
        require_relationship_governance_schema(store)
        with store.transaction() as connection:
            connection.execute("DROP INDEX idx_relationship_governance_active_pair")
        with pytest.raises(GateBlocked, match="relations are missing or altered"):
            require_relationship_governance_schema(store)
    finally:
        store.close()


def test_relationship_governance_schema_verifier_rejects_same_name_tampering(
    tmp_path: Path,
) -> None:
    store = SQLiteStore(tmp_path / "governance-tamper.sqlite3", LocalEnvelopeCipher(b"j" * 32))
    try:
        with store.transaction() as connection:
            connection.execute("DROP INDEX idx_relationship_governance_active_pair")
            connection.execute(
                "CREATE UNIQUE INDEX idx_relationship_governance_active_pair "
                "ON relationship_governance_transactions(transaction_id)"
            )
            connection.execute("DROP TRIGGER trg_relationship_governance_schema_floor_update")
            connection.execute(
                "CREATE TRIGGER trg_relationship_governance_schema_floor_update "
                "BEFORE UPDATE OF value ON metadata BEGIN SELECT 1; END"
            )
        with pytest.raises(GateBlocked, match="relations are missing or altered"):
            require_relationship_governance_schema(store)
    finally:
        store.close()


def test_sqlite_first_release_schema_verifiers_accept_only_complete_fresh_schema(
    tmp_path: Path,
) -> None:
    path = tmp_path / "complete-first-release.sqlite3"
    store = SQLiteStore(path, LocalEnvelopeCipher(b"p" * 32))
    try:
        require_relationship_governance_schema(store)
        require_post_audit_schema(store)
        assert store.readiness()["schema_version"] == CURRENT_SCHEMA_VERSION
        assert {
            row["name"]
            for row in store.fetch_all(
                "SELECT name FROM sqlite_master WHERE type='index' AND sql IS NOT NULL"
            )
        } >= RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES | POST_AUDIT_REQUIRED_INDEXES
    finally:
        store.close()


def test_post_audit_schema_verifier_fails_closed_on_missing_index(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "post-audit-schema.sqlite3", LocalEnvelopeCipher(b"q" * 32))
    try:
        require_post_audit_schema(store)
        index = sorted(POST_AUDIT_REQUIRED_INDEXES)[0]
        with store.transaction() as connection:
            connection.execute(f"DROP INDEX {index}")
        with pytest.raises(GateBlocked, match="relations are missing"):
            require_post_audit_schema(store)
    finally:
        store.close()


def test_sqlite_future_schema_preflight_never_rewrites_metadata(tmp_path: Path) -> None:
    path = tmp_path / "future-schema.sqlite3"
    store = SQLiteStore(path, LocalEnvelopeCipher(b"i" * 32))
    store.close()
    raw = sqlite3.connect(path)
    future_version = CURRENT_SCHEMA_VERSION + 1
    raw.execute("UPDATE metadata SET value=?", (str(future_version),))
    raw.commit()
    raw.close()
    before = _sqlite_logical_snapshot(path)

    with pytest.raises(GateBlocked, match="newer than this extension"):
        SQLiteStore(path, LocalEnvelopeCipher(b"i" * 32))

    assert _sqlite_logical_snapshot(path) == before


@pytest.mark.parametrize("tamper", ["checksum", "name", "missing", "future_tail"])
def test_sqlite_migration_catalog_tamper_is_never_silently_repaired(
    tmp_path: Path,
    tamper: str,
) -> None:
    path = tmp_path / f"catalog-{tamper}.sqlite3"
    store = SQLiteStore(path, LocalEnvelopeCipher(b"k" * 32))
    store.close()
    raw = sqlite3.connect(path)
    if tamper == "checksum":
        raw.execute(
            "UPDATE installed_migration_catalog SET checksum=? WHERE version=?",
            ("0" * 64, CURRENT_SCHEMA_VERSION),
        )
    elif tamper == "name":
        raw.execute(
            "UPDATE installed_migration_catalog SET name='prototype_first_release' WHERE version=1"
        )
    elif tamper == "missing":
        raw.execute(
            "DELETE FROM installed_migration_catalog WHERE version=?",
            (CURRENT_SCHEMA_VERSION,),
        )
    else:
        raw.execute(
            "INSERT INTO installed_migration_catalog(version,name,checksum) VALUES(?,'future',?)",
            (CURRENT_SCHEMA_VERSION + 1, "f" * 64),
        )
    raw.commit()
    raw.close()
    before = _sqlite_logical_snapshot(path)

    with pytest.raises(GateBlocked, match="migration history|newer than this extension"):
        SQLiteStore(path, LocalEnvelopeCipher(b"k" * 32))

    assert _sqlite_logical_snapshot(path) == before


def test_qmark_translation_ignores_exact_quoted_literals() -> None:
    query = "SELECT '?' AS literal, \"?\" AS identifier FROM events WHERE event_id=? AND actor_json=?"
    assert translate_qmark_sql(query) == (
        "SELECT '?' AS literal, \"?\" AS identifier FROM events WHERE event_id=%s AND actor_json=%s"
    )


def test_migration_history_rejects_gaps_future_and_checksum_tamper() -> None:
    first = MIGRATIONS[0]
    applied_prefix = [{"version": 1, "name": first.name, "checksum": first.checksum}]
    assert validate_applied_migrations(applied_prefix) == 1
    complete = [
        {"version": migration.version, "name": migration.name, "checksum": migration.checksum}
        for migration in MIGRATIONS
    ]
    assert validate_applied_migrations(complete) == CURRENT_SCHEMA_VERSION
    with pytest.raises(GateBlocked, match="contiguous"):
        validate_applied_migrations(
            [{"version": 2, "name": first.name, "checksum": first.checksum}]
        )
    with pytest.raises(GateBlocked, match="newer"):
        validate_applied_migrations(
            complete
            + [
                {
                    "version": CURRENT_SCHEMA_VERSION + 1,
                    "name": "future_schema",
                    "checksum": "f" * 64,
                }
            ]
        )
    with pytest.raises(GateBlocked, match="checksum"):
        validate_applied_migrations([{"version": 1, "name": first.name, "checksum": "0" * 64}])


def test_recovery_dsn_requires_distinct_hosts_read_write_selection_and_no_password() -> None:
    assert validate_postgres_recovery_dsn(
        "postgresql://agentnet@postgres-a:5432,postgres-b:5432/agentnet?target_session_attrs=read-write"
    ) == ("postgres-a", "postgres-b")
    rejected = (
        "postgresql://agentnet@postgres-a:5432/agentnet?target_session_attrs=read-write",
        "postgresql://agentnet@postgres-a:5432,postgres-b:5432/agentnet",
        "postgresql://agentnet@postgres-a:5432,postgres-b:5432/agentnet?target_session_attrs=any",
        "postgresql://agentnet:embedded@postgres-a:5432,postgres-b:5432/agentnet?target_session_attrs=read-write",
        "postgresql://agentnet@postgres-a:5432,postgres-a:5432/agentnet?target_session_attrs=read-write",
    )
    for database_url in rejected:
        with pytest.raises(ValidationError, match="hosts|read-write|password"):
            validate_postgres_recovery_dsn(database_url)


def test_server_profile_can_require_exact_recovery_topology(tmp_path: Path) -> None:
    config = server_config(tmp_path).model_copy(
        update={
            "database_url": (
                "postgresql://agentnet@postgres-a:5432,postgres-b:5432/agentnet"
                "?target_session_attrs=read-write"
            ),
            "postgres_recovery_topology": True,
        }
    )
    assert ExtensionConfig.model_validate(config.model_dump()).postgres_recovery_topology is True
    invalid = config.model_dump()
    invalid["database_url"] = "postgresql://agentnet@postgres-a:5432/agentnet"
    with pytest.raises(ValueError, match="multi-host DSN"):
        ExtensionConfig.model_validate(invalid)


class _Cursor:
    def __init__(self, rows=()):
        self.rows = list(rows)

    def fetchall(self):
        return list(self.rows)


class _Transaction:
    def __init__(self, connection):
        self.connection = connection

    def __enter__(self):
        self.connection.entered += 1

    def __exit__(self, exc_type, exc, traceback):
        self.connection.exited += 1


class _MigrationConnection:
    def __init__(self, *, relations=()):
        self.statements: list[tuple[str, tuple[object, ...]]] = []
        self.entered = 0
        self.exited = 0
        self.relations = list(relations)

    def transaction(self):
        return _Transaction(self)

    def execute(self, query, parameters=()):
        self.statements.append((str(query).strip(), tuple(parameters)))
        if "FROM pg_catalog.pg_class relation" in str(query):
            return _Cursor(self.relations)
        if "SELECT version,name,checksum FROM schema_migrations" in str(query):
            return _Cursor()
        return _Cursor()


def test_migration_application_is_one_crash_atomic_transaction() -> None:
    connection = _MigrationConnection()
    assert apply_postgres_migrations(connection) == CURRENT_SCHEMA_VERSION
    assert (connection.entered, connection.exited) == (1, 1)
    inserted_versions = [parameters[0] for query, parameters in connection.statements if query.startswith("INSERT INTO schema_migrations")]
    assert inserted_versions == list(range(1, CURRENT_SCHEMA_VERSION + 1))


@pytest.mark.parametrize("relation", ("relationships", "relationship_legacy_quarantine", "foreign_table"))
def test_postgres_clean_start_preflight_rejects_nonempty_unversioned_schema_without_mutation(
    relation: str,
) -> None:
    connection = _MigrationConnection(relations=({"name": relation, "kind": "r"},))

    with pytest.raises(GateBlocked, match="pre-release|nonempty unversioned"):
        apply_postgres_migrations(connection)

    assert not any(
        statement.startswith("CREATE TABLE schema_migrations")
        for statement, _parameters in connection.statements
    )
    assert not any(
        statement.startswith("INSERT INTO schema_migrations")
        for statement, _parameters in connection.statements
    )


class _LeaseCursor:
    def __init__(self, row=None, *, rowcount=0):
        self.row = row
        self.rowcount = rowcount

    def fetchone(self):
        return self.row


class _LeaseTransaction:
    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, traceback):
        return False


class _LeaseDatabase:
    def __init__(self, *, fsync: str = "on"):
        self.leases: dict[str, dict[str, object]] = {}
        self.fsync = fsync
        self.connect_count = 0
        self.connections: list[_LeaseConnection] = []
        self.fail_next_probe = False
        self.probe_attempts = 0
        self.recovery_generations: set[int] = set()

    def connect(self, *_args, **_kwargs):
        self.connect_count += 1
        connection = _LeaseConnection(self, generation=self.connect_count)
        self.connections.append(connection)
        return connection


class _LeaseConnection:
    def __init__(self, database: _LeaseDatabase, *, generation: int):
        self.database = database
        self.generation = generation
        self.closed = False

    def transaction(self):
        return _LeaseTransaction()

    def close(self):
        self.closed = True

    def execute(self, query, parameters=()):
        sql = " ".join(str(query).split())
        if sql.startswith("SELECT set_config") or "pg_advisory_xact_lock" in sql:
            return _LeaseCursor()
        if "current_setting('synchronous_commit') AS synchronous_commit" in sql:
            return _LeaseCursor(
                {
                    "synchronous_commit": "on",
                    "fsync": self.database.fsync,
                    "full_page_writes": "on",
                    "in_recovery": self.generation in self.database.recovery_generations,
                }
            )
        if sql == "SELECT recovery_probe":
            self.database.probe_attempts += 1
            if self.database.fail_next_probe:
                self.database.fail_next_probe = False
                raise psycopg.OperationalError("simulated connection loss after dispatch")
            return _LeaseCursor({"generation": self.generation})
        if sql.startswith("INSERT INTO runtime_leases"):
            lease_name, owner_id, acquired_at, heartbeat_at, expires_at = parameters
            current = self.database.leases.get(lease_name)
            if current is not None and int(current["expires_at"]) > acquired_at and current["owner_id"] != owner_id:
                return _LeaseCursor()
            fence = int(current["fence"]) + 1 if current else 1
            row = {
                "lease_name": lease_name,
                "owner_id": owner_id,
                "fence": fence,
                "expires_at": expires_at,
                "heartbeat_at": heartbeat_at,
            }
            self.database.leases[lease_name] = row
            return _LeaseCursor(dict(row), rowcount=1)
        if sql.startswith("UPDATE runtime_leases SET heartbeat_at"):
            now, expires_at, lease_name, owner_id, fence, must_exceed = parameters
            current = self.database.leases.get(lease_name)
            if (
                current
                and current["owner_id"] == owner_id
                and int(current["fence"]) == fence
                and int(current["expires_at"]) > must_exceed
            ):
                current.update({"heartbeat_at": now, "expires_at": expires_at})
                return _LeaseCursor(dict(current), rowcount=1)
            return _LeaseCursor()
        if sql.startswith("SELECT 1 AS current FROM runtime_leases"):
            lease_name, owner_id, fence, now = parameters
            current = self.database.leases.get(lease_name)
            valid = bool(
                current
                and current["owner_id"] == owner_id
                and int(current["fence"]) == fence
                and int(current["expires_at"]) > now
            )
            return _LeaseCursor({"current": 1} if valid else None)
        if sql.startswith("UPDATE runtime_leases SET expires_at"):
            expires_at, heartbeat_at, lease_name, owner_id, fence = parameters
            current = self.database.leases.get(lease_name)
            if current and current["owner_id"] == owner_id and int(current["fence"]) == fence:
                current.update({"expires_at": expires_at, "heartbeat_at": heartbeat_at})
                return _LeaseCursor(rowcount=1)
            return _LeaseCursor()
        raise AssertionError(f"unexpected fake PostgreSQL statement: {sql}")


class _Clock:
    def __init__(self, value: int):
        self.value = value

    def __call__(self):
        return self.value


def _lease_store(database: _LeaseDatabase, clock: _Clock, instance_id: str) -> PostgreSQLStore:
    return PostgreSQLStore(
        "postgresql://agentnet@postgres/agentnet",
        LocalEnvelopeCipher(b"v" * 32),
        instance_id=instance_id,
        run_migrations=False,
        verify_schema=False,
        connector=database.connect,
        clock=clock,
        start_lease_keeper=False,
    )


def test_store_recovery_topology_validation_happens_before_connect() -> None:
    database = _LeaseDatabase()
    with pytest.raises(ValidationError, match="at least two distinct hosts"):
        PostgreSQLStore(
            "postgresql://agentnet@postgres-a:5432/agentnet?target_session_attrs=read-write",
            LocalEnvelopeCipher(b"v" * 32),
            instance_id="server-agent-a",
            run_migrations=False,
            verify_schema=False,
            connector=database.connect,
            start_lease_keeper=False,
            require_recovery_topology=True,
        )
    assert database.connect_count == 0

    store = PostgreSQLStore(
        "postgresql://agentnet@postgres-a:5432,postgres-b:5432/agentnet?target_session_attrs=read-write",
        LocalEnvelopeCipher(b"v" * 32),
        instance_id="server-agent-a",
        run_migrations=False,
        verify_schema=False,
        connector=database.connect,
        start_lease_keeper=False,
        require_recovery_topology=True,
    )
    try:
        assert database.connect_count == 1
    finally:
        store.close()


def test_runtime_fence_invalidates_an_older_same_instance_process() -> None:
    database = _LeaseDatabase()
    first = _lease_store(database, _Clock(100), "server-agent-a")
    second = _lease_store(database, _Clock(101), "server-agent-a")
    try:
        with pytest.raises(GateBlocked, match="lease"):
            with first.transaction():
                pass
        with second.transaction():
            pass
    finally:
        first.close()
        second.close()


def test_distinct_lease_owner_cannot_take_over_live_same_instance_runtime() -> None:
    database = _LeaseDatabase()
    first = _lease_store(database, _Clock(100), "server-agent-a")
    try:
        with pytest.raises(GateBlocked, match="held by another owner"):
            PostgreSQLStore(
                "postgresql://agentnet@postgres/agentnet",
                LocalEnvelopeCipher(b"v" * 32),
                instance_id="server-agent-a",
                lease_owner_id="activation-attempt",
                run_migrations=False,
                verify_schema=False,
                connector=database.connect,
                clock=_Clock(101),
                start_lease_keeper=False,
            )
        assert database.connections[-1].closed is True
    finally:
        first.close()

    activation = PostgreSQLStore(
        "postgresql://agentnet@postgres/agentnet",
        LocalEnvelopeCipher(b"v" * 32),
        instance_id="server-agent-a",
        lease_owner_id="activation-attempt",
        run_migrations=False,
        verify_schema=False,
        connector=database.connect,
        clock=_Clock(102),
        start_lease_keeper=False,
    )
    try:
        assert activation._lease.owner_id == "activation-attempt"
    finally:
        activation.close()


def test_postgres_backend_rejects_fsync_disabled_even_with_synchronous_commit() -> None:
    database = _LeaseDatabase(fsync="off")
    with pytest.raises(GateBlocked, match="fsync"):
        _lease_store(database, _Clock(100), "server-agent-a")


def test_singleton_lease_requires_expiry_before_different_owner_takeover() -> None:
    database = _LeaseDatabase()
    first_clock = _Clock(100)
    second_clock = _Clock(101)
    first = _lease_store(database, first_clock, "server-agent-a")
    second = _lease_store(database, second_clock, "server-agent-b")
    try:
        token = first.acquire_lease("artifact-recovery", owner_id="server-agent-a", ttl_seconds=30)
        with pytest.raises(GateBlocked, match="held by another owner"):
            second.acquire_lease("artifact-recovery", owner_id="server-agent-b", ttl_seconds=30)
        second_clock.value = 131
        takeover = second.acquire_lease("artifact-recovery", owner_id="server-agent-b", ttl_seconds=30)
        assert takeover.fence == token.fence + 1
    finally:
        first.close()
        second.close()


def test_connection_loss_is_not_retried_and_next_operation_reconnects_with_higher_fence() -> None:
    database = _LeaseDatabase()
    store = _lease_store(database, _Clock(100), "server-agent-a")
    initial_fence = store._lease.fence
    try:
        database.fail_next_probe = True
        with pytest.raises(GateBlocked, match="outcome is unknown and was not retried"):
            store.fetch_one("SELECT recovery_probe")
        assert database.probe_attempts == 1
        assert database.connect_count == 1
        assert store._reconnect_required is True

        result = store.fetch_one("SELECT recovery_probe")
        assert result == {"generation": 2}
        assert database.probe_attempts == 2
        assert database.connect_count == 2
        assert store._lease.fence > initial_fence
        assert store._reconnect_required is False
        assert store._unknown_operation_count == 1
    finally:
        store.close()


def test_reconnect_rejects_standby_and_does_not_retry_the_subsequent_operation() -> None:
    database = _LeaseDatabase()
    database.recovery_generations.add(2)
    store = _lease_store(database, _Clock(100), "server-agent-a")
    try:
        database.fail_next_probe = True
        with pytest.raises(GateBlocked, match="outcome is unknown"):
            store.fetch_one("SELECT recovery_probe")
        with pytest.raises(GateBlocked, match="writable primary"):
            store.fetch_one("SELECT recovery_probe")
        assert database.probe_attempts == 1
        assert database.connect_count == 2
        assert store._reconnect_required is True
    finally:
        store.close()


def test_reconnect_rejects_nonincreasing_fence_as_divergent_primary() -> None:
    database = _LeaseDatabase()
    store = _lease_store(database, _Clock(100), "server-agent-a")
    try:
        database.fail_next_probe = True
        with pytest.raises(GateBlocked, match="outcome is unknown"):
            store.fetch_one("SELECT recovery_probe")
        database.leases.clear()
        with pytest.raises(GateBlocked, match="strictly higher runtime fence"):
            store.fetch_one("SELECT recovery_probe")
        assert database.probe_attempts == 1
        assert store._reconnect_required is True
    finally:
        store.close()


def test_reconnect_rejects_future_schema_before_reacquiring_fence(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    database = _LeaseDatabase()

    def exact_schema(connection: _LeaseConnection) -> None:
        if connection.generation > 1:
            raise GateBlocked("schema_future", "database schema is newer than this extension")

    monkeypatch.setattr(
        "agentnet.storage.postgres.require_current_postgres_schema",
        exact_schema,
    )
    store = PostgreSQLStore(
        "postgresql://agentnet@postgres/agentnet",
        LocalEnvelopeCipher(b"w" * 32),
        instance_id="server-agent-a",
        run_migrations=False,
        verify_schema=True,
        connector=database.connect,
        clock=_Clock(100),
        start_lease_keeper=False,
    )
    initial_fence = store._lease.fence
    try:
        database.fail_next_probe = True
        with pytest.raises(GateBlocked, match="outcome is unknown"):
            store.fetch_one("SELECT recovery_probe")
        with pytest.raises(GateBlocked, match="newer than this extension"):
            store.fetch_one("SELECT recovery_probe")
        assert database.leases[store._lease.lease_name]["fence"] == initial_fence
        assert store._reconnect_required is True
    finally:
        store.close()


class _CallerDeclaredDurableSQLite(SQLiteStore):
    backend_name = "test-postgresql-commit"
    durable_commit = True


def _event() -> object:
    return new_event(
        domain_id="server-agent.example",
        actor=VerifiedActor(
            kind=ActorKind.WORKLOAD,
            domain_id="server-agent.example",
            workload_id="mailbox.acceptance-test",
            workload_registration_id="registration-mailbox-acceptance-test",
            workload_role="mailbox.acceptance",
            workload_process_id=4242,
            workload_process_start_time=1,
            workload_session_id="session-mailbox-acceptance-test",
            workload_revocation_epoch=1,
            credential_id="registration-mailbox-acceptance-test",
            credential_epoch=1,
            binding_assurance="workload_mtls",
        ),
        event_type=EventType.MESSAGE,
        classification=Classification.C0_PUBLIC,
        payload={"test": True},
        idempotency_key=f"durable-test-{uuid4()}",
        recipients=("offline-recipient",),
    )


def test_caller_declared_or_constructor_selected_durability_is_rejected(tmp_path: Path) -> None:
    store = _CallerDeclaredDurableSQLite(tmp_path / "core.sqlite3", LocalEnvelopeCipher(b"r" * 32))
    try:
        with pytest.raises(GateBlocked, match="durable-commit"):
            CommunicationCore(server_config(tmp_path), store)
        with pytest.raises(ValueError, match="verified storage boundary"):
            MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_DURABLE)
        with pytest.raises(GateBlocked, match="post-audit backend is unsupported"):
            MailboxService(store)
    finally:
        store.close()


def test_server_agent_profile_rejects_nondurable_store(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "core.sqlite3", LocalEnvelopeCipher(b"s" * 32))
    try:
        with pytest.raises(GateBlocked, match="durable-commit"):
            CommunicationCore(server_config(tmp_path), store)
    finally:
        store.close()


def test_filesystem_recovery_cross_checks_manifest_bytes(tmp_path: Path) -> None:
    root = tmp_path / "artifacts"
    root.mkdir(mode=0o700)
    store = SQLiteStore(tmp_path / "core.sqlite3", LocalEnvelopeCipher(b"t" * 32))
    ciphertext = b"immutable encrypted object"
    version = hashlib.sha256(ciphertext).hexdigest()
    object_key = "a" * 32
    target = root / "quarantine" / object_key[:2] / object_key / version
    target.parent.mkdir(parents=True, mode=0o700)
    target.write_bytes(ciphertext)
    target.chmod(0o600)
    now = 1_800_000_000
    try:
        with store.transaction() as connection:
            connection.execute(
                "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
                ("server-agent.example", now),
            )
            connection.execute(
                """INSERT INTO artifact_reservations(
                    reservation_id,domain_id,actor_id,actor_json,idempotency_key,request_digest,
                    object_key,expected_digest,expected_size,media_type,classification,
                    required_attachment,state,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    "reservation-1",
                    "server-agent.example",
                    "actor-1",
                    "{}",
                    "artifact-idempotency-0001",
                    "b" * 64,
                    object_key,
                    "c" * 64,
                    1,
                    "application/octet-stream",
                    "C1",
                    1,
                    "manifest_committed",
                    now + 300,
                ),
            )
            connection.execute(
                """INSERT INTO artifact_manifests(
                    artifact_id,reservation_id,domain_id,object_key,object_version,ciphertext_digest,
                    plaintext_digest_encrypted,size,media_type,classification,state,provenance_json,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    "artifact-1",
                    "reservation-1",
                    "server-agent.example",
                    object_key,
                    version,
                    version,
                    "encrypted",
                    len(ciphertext),
                    "application/octet-stream",
                    "C1",
                    "quarantined",
                    "{}",
                    now,
                ),
            )
        status = probe_filesystem_artifact_recovery(
            store,
            root,
            instance_id="server-agent-test",
            scan_limit=100,
        )
        assert status["ready"] is True
        target.unlink()
        assert probe_filesystem_artifact_recovery(
            store,
            root,
            instance_id="server-agent-test",
            scan_limit=100,
        )["ready"] is False
    finally:
        store.close()


@pytest.mark.anyio
async def test_liveness_is_dependency_independent_while_readiness_and_recovery_fail_closed(tmp_path: Path) -> None:
    config = ExtensionConfig(
        domain_id="probe.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
    )
    core = CommunicationCore.open(config)
    transport = httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1") as client:
        assert (await client.get("/healthz")).status_code == 200
        assert (await client.get("/readyz")).status_code == 200
        assert (await client.get("/recoveryz")).status_code == 200
        core.close()
        assert (await client.get("/healthz")).status_code == 200
        assert (await client.get("/readyz")).status_code == 503
        assert (await client.get("/recoveryz")).status_code == 503


def test_server_agent_startup_rejects_retired_exact_configured_credential(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    config = server_config(tmp_path, instance_id="retired-binding")
    core = object.__new__(CommunicationCore)
    core.config = config
    core.store = object()

    class RetiredBinding:
        credential_id = config.enrolled_credential_id
        domain_id = config.domain_id
        harness_id = config.enrolled_harness_id
        binding_assurance = "os_bound"

        @staticmethod
        def require_active(*, now: int) -> None:
            assert now > 0
            raise AuthenticationError("credential is unavailable")

    monkeypatch.setattr(
        "agentnet.core.app.load_credential_binding",
        lambda _store, _credential_id: RetiredBinding(),
    )

    with pytest.raises(GateBlocked, match="configured server-agent enrollment is not current"):
        core._require_enrolled_server_agent_binding()


def test_server_agent_probe_status_does_not_enumerate_enrollment_identifiers(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    config = server_config(tmp_path, instance_id="private-runtime-label")
    core = object.__new__(CommunicationCore)
    core.config = config
    monkeypatch.setattr(core, "_require_enrolled_server_agent_binding", lambda: None)

    status = core.server_agent_binding_status()

    assert status == {"ready": True, "required": True}
    assert "harness_id" not in status
    assert "credential_id" not in status


@pytest.mark.skipif(
    not (
        os.environ.get("AGENTNET_TEST_POSTGRES_URL")
        and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"
    ),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_real_postgres_pre_release_schema_fails_closed_without_catalog_mutation() -> None:
    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    schema = f"agentnet_pre_release_{uuid4().hex}"
    administrator = psycopg.connect(database_url, autocommit=True)
    administrator.execute(
        psycopg.sql.SQL("CREATE SCHEMA {}").format(psycopg.sql.Identifier(schema))
    )
    administrator.execute(
        psycopg.sql.SQL("CREATE TABLE {}.relationships (relationship_id TEXT PRIMARY KEY)").format(
            psycopg.sql.Identifier(schema)
        )
    )
    separator = "&" if "?" in database_url else "?"
    isolated_url = (
        f"{database_url}{separator}options="
        f"-csearch_path%3D{schema}%20-cclient_encoding%3DUTF8"
    )
    try:
        with pytest.raises(GateBlocked, match="pre-release PostgreSQL relationship schemas"):
            PostgreSQLStore(
                isolated_url,
                LocalEnvelopeCipher(b"l" * 32),
                instance_id=f"pre-release-reject-{uuid4().hex}",
                start_lease_keeper=False,
            )
        assert administrator.execute(
            "SELECT to_regclass(%s) AS relation",
            (f"{schema}.schema_migrations",),
        ).fetchone()[0] is None
        assert administrator.execute(
            "SELECT to_regclass(%s) AS relation",
            (f"{schema}.relationships",),
        ).fetchone()[0] == f"{schema}.relationships"
    finally:
        administrator.execute(
            psycopg.sql.SQL("DROP SCHEMA {} CASCADE").format(
                psycopg.sql.Identifier(schema)
            )
        )
        administrator.close()


@pytest.mark.skipif(
    not (
        os.environ.get("AGENTNET_TEST_POSTGRES_URL")
        and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"
    ),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_real_postgres_fresh_schema_installs_current_migration_catalog() -> None:
    """Install the complete current migration catalog in an isolated PostgreSQL schema."""

    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    schema = f"agentnet_current_catalog_{uuid4().hex}"
    administrator = psycopg.connect(database_url, autocommit=True)
    administrator.execute(
        psycopg.sql.SQL("CREATE SCHEMA {}").format(psycopg.sql.Identifier(schema))
    )
    separator = "&" if "?" in database_url else "?"
    isolated_url = (
        f"{database_url}{separator}options="
        f"-csearch_path%3D{schema}%20-cclient_encoding%3DUTF8"
    )
    store = None
    reopened = None
    try:
        store = PostgreSQLStore(
            isolated_url,
            LocalEnvelopeCipher(b"v" * 32),
            instance_id=f"current-catalog-{uuid4().hex}",
            start_lease_keeper=False,
        )
        assert store.fetch_all(
            "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
        ) == [
            {
                "version": migration.version,
                "name": migration.name,
                "checksum": migration.checksum,
            }
            for migration in MIGRATIONS
        ]
        assert store.fetch_one(
            "SELECT value FROM metadata WHERE key='schema_version'"
        )["value"] == str(CURRENT_SCHEMA_VERSION)
        require_relationship_governance_schema(store)
        require_post_audit_schema(store)
        for relation in (
            RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES
            | POST_AUDIT_REQUIRED_TABLES
            | POST_AUDIT_REQUIRED_INDEXES
            | {
                "relationship_governance_lineages",
                "relationship_governance_transactions",
                "relationship_policy_exceptions",
                "runtime_leases",
                "artifact_recovery_observations",
            }
        ):
            assert store.fetch_one("SELECT to_regclass(?) AS relation", (relation,))[
                "relation"
            ] is not None
        assert store.fetch_one("SELECT to_regclass('relationships') AS relation")[
            "relation"
        ] is None
        assert store.fetch_one(
            "SELECT to_regclass('relationship_legacy_quarantine') AS relation"
        )["relation"] is None
        store.close()
        store = None

        reopened = PostgreSQLStore(
            isolated_url,
            LocalEnvelopeCipher(b"v" * 32),
            instance_id=f"current-catalog-reopen-{uuid4().hex}",
            start_lease_keeper=False,
        )
        assert reopened.fetch_all(
            "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
        ) == [
            {
                "version": migration.version,
                "name": migration.name,
                "checksum": migration.checksum,
            }
            for migration in MIGRATIONS
        ]
    finally:
        if store is not None:
            store.close()
        if reopened is not None:
            reopened.close()
        administrator.execute(
            psycopg.sql.SQL("DROP SCHEMA {} CASCADE").format(
                psycopg.sql.Identifier(schema)
            )
        )
        administrator.close()


@pytest.mark.skipif(
    not (os.environ.get("AGENTNET_TEST_POSTGRES_URL") and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_two_server_agent_instances_share_durable_mailbox(tmp_path: Path, monkeypatch) -> None:
    # This is the conditional live lane; unit tests above remain hermetic.
    from agentnet.storage.postgres import PostgreSQLStore

    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    cipher = LocalEnvelopeCipher(b"u" * 32)
    first = PostgreSQLStore(database_url, cipher, instance_id=f"test-a-{uuid4().hex}")
    second = PostgreSQLStore(database_url, cipher, instance_id=f"test-b-{uuid4().hex}")
    try:
        from agentnet.identity.domains import DomainRegistry
        domain = f"test-{uuid4().hex}.example"
        DomainRegistry(first).register(domain)
        recipient_key = P256KeyPair.generate()
        recipient_id = f"offline-recipient-{uuid4().hex}"
        recipient_principal_id = f"principal-{recipient_id}"
        recipient_credential_id = f"credential-{recipient_id}"
        now = int(time.time())
        with first.transaction() as connection:
            connection.execute(
                "INSERT INTO principals(principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at) "
                "VALUES(?,?,?,?,?,'active',?)",
                (
                    recipient_principal_id,
                    domain,
                    "https://idp.postgres-test.example",
                    f"subject-{uuid4().hex}",
                    f"{uuid4().hex}@postgres-test.example",
                    now,
                ),
            )
            connection.execute(
                "INSERT INTO harnesses(harness_id,domain_id,principal_id,kind,display_name,status,"
                "binding_assurance,capabilities_json,credential_epoch,created_at) "
                "VALUES(?,?,?,'codex','offline-recipient','active','hardware_bound','[]',1,?)",
                (recipient_id, domain, recipient_principal_id, now),
            )
            connection.execute(
                "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) "
                "VALUES(?,?,?,?,'active',1,?,?)",
                (
                    recipient_credential_id,
                    recipient_id,
                    recipient_key.thumbprint,
                    recipient_key.public_pem,
                    now - 1,
                    now + 3600,
                ),
            )
        event = new_event(
            domain_id=domain,
            actor=VerifiedActor(
                kind=ActorKind.EXTERNAL_A2A,
                domain_id=domain,
                external_peer_id="conditional-postgres-durable-mailbox-peer",
                binding_assurance="external",
            ),
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload={"durable": True},
            idempotency_key=f"postgres-live-{uuid4()}",
            recipients=(recipient_id,),
        )
        accepted = MailboxService(first).accept(event)
        assert accepted["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
        assert MailboxService(second).reconcile(recipient_id)[0][
            "payload"
        ] == {"durable": True}
        assert first.readiness()["ready"] is True
        assert second.readiness()["ready"] is True
        assert first.readiness()["accepted_durable_enabled"] is False
    finally:
        first.close()
        second.close()


@pytest.mark.skipif(
    not (os.environ.get("AGENTNET_TEST_POSTGRES_URL") and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_real_postgres_closed_connection_recovers_only_on_subsequent_operation() -> None:
    """Exercise the reconnect/fence path without claiming a standby failover."""

    store = PostgreSQLStore(
        os.environ["AGENTNET_TEST_POSTGRES_URL"],
        LocalEnvelopeCipher(b"x" * 32),
        instance_id=f"reconnect-{uuid4().hex}",
        start_lease_keeper=False,
    )
    initial_fence = store._lease.fence
    try:
        store._connection.close()
        with pytest.raises(GateBlocked, match="outcome is unknown and was not retried"):
            store.fetch_one("SELECT 1 AS value")
        assert store._reconnect_required is True
        assert store.fetch_one("SELECT 1 AS value") == {"value": 1}
        assert store._lease.fence > initial_fence
        assert store._unknown_operation_count == 1
    finally:
        store.close()


@pytest.mark.skipif(
    not (os.environ.get("AGENTNET_TEST_POSTGRES_URL") and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_postgres_cross_instance_artifact_quota_admission_is_atomic(tmp_path: Path) -> None:
    """Two ordinary extension instances cannot oversubscribe one domain."""

    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    cipher = LocalEnvelopeCipher(b"q" * 32)
    first = PostgreSQLStore(database_url, cipher, instance_id=f"quota-a-{uuid4().hex}")
    second = PostgreSQLStore(database_url, cipher, instance_id=f"quota-b-{uuid4().hex}")
    domain = f"quota-{uuid4().hex}.example"
    reservation_ids = (f"reservation-{uuid4()}", f"reservation-{uuid4()}")
    actor_ids = (f"actor-{uuid4()}", f"actor-{uuid4()}")
    now = int(time.time())
    try:
        with first.transaction() as connection:
            connection.execute(
                "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
                (domain, now),
            )
            for index, (reservation_id, actor_id) in enumerate(
                zip(reservation_ids, actor_ids, strict=True)
            ):
                connection.execute(
                    """INSERT INTO artifact_reservations(
                           reservation_id,domain_id,actor_id,actor_json,idempotency_key,
                           request_digest,object_key,expected_digest,expected_size,media_type,
                           classification,required_attachment,state,expires_at
                       ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                    (
                        reservation_id,
                        domain,
                        actor_id,
                        "{}",
                        f"postgres-quota-{index}-{uuid4()}",
                        hashlib.sha256(reservation_id.encode()).hexdigest(),
                        uuid4().hex,
                        hashlib.sha256(actor_id.encode()).hexdigest(),
                        10,
                        "application/octet-stream",
                        "C1",
                        1,
                        "upload_reserved",
                        now + 300,
                    ),
                )
        operations = OperationsPolicy(
            per_actor_artifact_bytes=10,
            per_domain_artifact_bytes=10,
        )
        objects = FilesystemArtifactStore(tmp_path / "objects", tmp_path / "artifact.key")
        services = (
            ArtifactService(first, objects, operations_policy=operations),
            ArtifactService(second, objects, operations_policy=operations),
        )
        barrier = threading.Barrier(2)

        def charge(index: int) -> str:
            barrier.wait(timeout=5)
            try:
                with services[index].store.transaction() as connection:
                    services[index]._charge_artifact_bytes_in_transaction(
                        connection,
                        reservation_id=reservation_ids[index],
                        domain_id=domain,
                        actor_id=actor_ids[index],
                        amount=10,
                        now=now,
                    )
            except AuthorizationError as exc:
                return str(exc)
            return "admitted"

        with ThreadPoolExecutor(max_workers=2) as executor:
            outcomes = sorted(executor.map(charge, range(2)))
        assert outcomes == ["admitted", "artifact byte quota exceeded"]
        accounts = first.fetch_all(
            "SELECT scope_type,scope_id,used_bytes FROM artifact_byte_accounts WHERE scope_id=? OR scope_id IN (?,?)",
            (domain, *actor_ids),
        )
        assert sorted((row["scope_type"], int(row["used_bytes"])) for row in accounts) == [
            ("actor", 10),
            ("domain", 10),
        ]
        assert first.fetch_one(
            "SELECT COUNT(*) AS count FROM artifact_byte_charges WHERE reservation_id IN (?,?)",
            reservation_ids,
        )["count"] == 1
    finally:
        first.close()
        second.close()


@pytest.mark.skipif(
    not (os.environ.get("AGENTNET_TEST_POSTGRES_URL") and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_postgres_cross_instance_half_open_breaker_has_one_cas_winner() -> None:
    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    schema = f"agentnet_breaker_{uuid4().hex}"
    administrator = psycopg.connect(database_url, autocommit=True)
    administrator.execute(psycopg.sql.SQL("CREATE SCHEMA {}").format(psycopg.sql.Identifier(schema)))
    separator = "&" if "?" in database_url else "?"
    isolated_url = (
        f"{database_url}{separator}options="
        f"-csearch_path%3D{schema}%20-cclient_encoding%3DUTF8"
    )
    cipher = LocalEnvelopeCipher(b"b" * 32)
    first = None
    second = None
    try:
        first = PostgreSQLStore(isolated_url, cipher, instance_id=f"breaker-a-{uuid4().hex}")
        second = PostgreSQLStore(isolated_url, cipher, instance_id=f"breaker-b-{uuid4().hex}")
        domain = f"breaker-{uuid4().hex}.example"
        clock = [100_000]
        policy = OperationsPolicy(
            per_actor_requests_per_minute=100,
            per_domain_requests_per_minute=100,
            global_requests_per_minute=100,
            pending_delivery_backpressure_limit=100,
            fairness_burst_limit=100,
            circuit_breaker_failure_threshold=2,
            circuit_breaker_reset_seconds=10,
        )
        services = (
            QuotaService(first, policy=policy, safety_reserve_fraction=0, clock=lambda: clock[0]),
            QuotaService(second, policy=policy, safety_reserve_fraction=0, clock=lambda: clock[0]),
        )
        services[0].record_failure(operation="relay", domain_scope=domain)
        services[0].record_failure(operation="relay", domain_scope=domain)
        clock[0] += 11
        barrier = threading.Barrier(2)

        def probe(index: int) -> str:
            barrier.wait(timeout=5)
            try:
                services[index].admit_operation(
                    actor_scope=f"actor-{index}",
                    domain_scope=domain,
                    operation="relay",
                    operation_id=f"probe-{uuid4()}",
                )
            except GateBlocked:
                return "denied"
            return "admitted"

        with ThreadPoolExecutor(max_workers=2) as executor:
            assert sorted(executor.map(probe, range(2))) == ["admitted", "denied"]

        pressure_domain = f"pressure-{uuid4().hex}.example"
        pressure_policy = policy.model_copy(
            update={"pending_delivery_backpressure_limit": 1}
        )
        pressure_services = (
            QuotaService(first, policy=pressure_policy, safety_reserve_fraction=0),
            QuotaService(second, policy=pressure_policy, safety_reserve_fraction=0),
        )
        pressure_barrier = threading.Barrier(2)

        def reserve_pressure(index: int) -> str:
            source_id = f"pressure-work-{uuid4()}"
            pressure_barrier.wait(timeout=5)
            try:
                with pressure_services[index].store.transaction() as connection:
                    pressure_services[index]._admit_operation_in_transaction(
                        connection,
                        actor_scope=f"pressure-actor-{index}",
                        domain_scope=pressure_domain,
                        operation="relay_outbound",
                        operation_id=source_id,
                    )
                    pressure_services[index]._reserve_work_in_transaction(
                        connection,
                        work_kind="relay_outbound",
                        source_id=source_id,
                        domain_id=pressure_domain,
                        now=int(time.time()),
                    )
            except GateBlocked:
                return "denied"
            return "admitted"

        with ThreadPoolExecutor(max_workers=2) as executor:
            assert sorted(executor.map(reserve_pressure, range(2))) == ["admitted", "denied"]
        assert first.fetch_one(
            """SELECT COUNT(*) AS count FROM operational_work_reservations
                 WHERE domain_id=? AND state='pending'""",
            (pressure_domain,),
        )["count"] == 1
    finally:
        if first is not None:
            first.close()
        if second is not None:
            second.close()
        administrator.execute(
            psycopg.sql.SQL("DROP SCHEMA {} CASCADE").format(psycopg.sql.Identifier(schema))
        )
        administrator.close()


@pytest.mark.skipif(
    not (
        os.environ.get("AGENTNET_TEST_POSTGRES_URL")
        and os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") == "1"
    ),
    reason="requires an explicitly mutation-authorized dedicated PostgreSQL test database",
)
def test_postgres_cross_instance_task_conflict_race_and_owner_revision_fence() -> None:
    """Concurrent incompatible admissions hold both; one exact owner decision wins."""

    database_url = os.environ["AGENTNET_TEST_POSTGRES_URL"]
    schema = f"agentnet_task_conflict_{uuid4().hex}"
    administrator = psycopg.connect(database_url, autocommit=True)
    administrator.execute(
        psycopg.sql.SQL("CREATE SCHEMA {}").format(psycopg.sql.Identifier(schema))
    )
    separator = "&" if "?" in database_url else "?"
    isolated_url = (
        f"{database_url}{separator}options="
        f"-csearch_path%3D{schema}%20-cclient_encoding%3DUTF8"
    )
    cipher = LocalEnvelopeCipher(b"c" * 32)
    first = None
    second = None
    try:
        first = PostgreSQLStore(
            isolated_url,
            cipher,
            instance_id=f"task-conflict-a-{uuid4().hex}",
            start_lease_keeper=False,
        )
        second = PostgreSQLStore(
            isolated_url,
            cipher,
            instance_id=f"task-conflict-b-{uuid4().hex}",
            start_lease_keeper=False,
        )
        domain = f"task-conflict-{uuid4().hex}.example"
        now = datetime.now(UTC)
        now_epoch = int(now.timestamp())
        deadline = now + timedelta(hours=1)
        identity_rows: list[tuple[VerifiedActor, P256KeyPair]] = []
        with first.transaction() as connection:
            connection.execute(
                "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
                (domain, now_epoch),
            )
            for label in ("owner", "sender-a", "sender-b"):
                suffix = uuid4().hex
                principal_id = f"{label}-principal-{suffix}"
                harness_id = f"{label}-harness-{suffix}"
                credential_id = f"{label}-credential-{suffix}"
                key = P256KeyPair.generate()
                connection.execute(
                    """INSERT INTO principals(
                           principal_id,domain_id,oidc_issuer,oidc_subject,
                           verified_email,status,created_at
                       ) VALUES(?,?,?,?,?,'active',?)""",
                    (
                        principal_id,
                        domain,
                        "https://idp.postgres-conflict.example",
                        f"subject-{suffix}",
                        f"{suffix}@postgres-conflict.example",
                        now_epoch,
                    ),
                )
                connection.execute(
                    """INSERT INTO harnesses(
                           harness_id,domain_id,principal_id,kind,display_name,status,
                           binding_assurance,capabilities_json,credential_epoch,created_at
                       ) VALUES(?,?,?,'codex',?,'active','hardware_bound','{}',1,?)""",
                    (harness_id, domain, principal_id, label, now_epoch),
                )
                connection.execute(
                    """INSERT INTO credentials(
                           credential_id,harness_id,key_id,public_key_pem,status,epoch,
                           not_before,expires_at
                       ) VALUES(?,?,?,?,'active',1,?,?)""",
                    (
                        credential_id,
                        harness_id,
                        key.thumbprint,
                        key.public_pem,
                        now_epoch - 1,
                        now_epoch + 3_600,
                    ),
                )
                identity_rows.append(
                    (
                        VerifiedActor(
                            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
                            domain_id=domain,
                            principal_id=principal_id,
                            harness_id=harness_id,
                            credential_id=credential_id,
                            credential_epoch=1,
                            binding_assurance="hardware_bound",
                        ),
                        key,
                    )
                )

        owner = identity_rows[0][0]
        senders = (identity_rows[1][0], identity_rows[2][0])
        intent = TaskExecutionIntent(
            resources=(
                TaskResourceIntent(
                    resource="catalog:postgres-race",
                    operation="research",
                    access=TaskAccessMode.WRITE,
                    exclusivity=TaskExclusivity.EXCLUSIVE,
                ),
            )
        )
        events = tuple(
            new_event(
                domain_id=domain,
                actor=sender,
                event_type=EventType.TASK_ASSIGNMENT,
                classification=Classification.C1_INTERNAL,
                payload={"instruction": f"exclusive rewrite {index}"},
                idempotency_key=f"postgres-task-conflict-{index}-{uuid4()}",
                recipients=(owner.harness_id,),
                task_id=str(uuid4()),
                effect_deadline=deadline,
                policy_revision=1,
            )
            for index, sender in enumerate(senders)
        )
        stores = (first, second)
        mailboxes = (MailboxService(first), MailboxService(second))
        conflicts = (TaskConflictService(first), TaskConflictService(second))
        admission_barrier = threading.Barrier(2)

        def admit(index: int) -> tuple[str, tuple[str, ...]]:
            admission_barrier.wait(timeout=5)
            with stores[index].transaction() as connection:
                accepted = mailboxes[index]._accept_in_transaction(
                    connection,
                    events[index],
                    now=now_epoch,
                )
                assert accepted["duplicate"] is False
                connection.execute(
                    """UPDATE recipients SET current_fact=?,updated_at=?
                         WHERE event_id=? AND recipient_id=?""",
                    (
                        DeliveryFact.ACCEPTED_QUEUED.value,
                        now_epoch,
                        events[index].event_id,
                        owner.harness_id,
                    ),
                )
                admission = conflicts[index].record_accepted_in_transaction(
                    connection,
                    event_id=events[index].event_id,
                    domain_id=domain,
                    recipient_harness_id=owner.harness_id or "",
                    sender_harness_id=senders[index].harness_id or "",
                    sender_authority_id=senders[index].positive_authority_id or "",
                    authority_basis="recipient_owner_approval",
                    relationship_id=None,
                    relationship_revision=0,
                    intent=intent,
                    continuation={},
                    deadline=deadline,
                    when=now,
                )
                return admission.fact.value, admission.conflict_ids

        with ThreadPoolExecutor(max_workers=2) as executor:
            admissions = list(executor.map(admit, range(2)))
        assert sorted(fact for fact, _conflict_ids in admissions) == [
            DeliveryFact.ACCEPTED_QUEUED.value,
            DeliveryFact.CONFLICT_PENDING.value,
        ]
        pending = conflicts[0].pending_for_owner(actor=owner, when=now)
        assert len(pending) == 1
        conflict = pending[0]
        member_ids = frozenset(str(member["event_id"]) for member in conflict["members"])
        assert member_ids == frozenset(event.event_id for event in events)
        decisions = tuple(
            TaskConflictAdjudication(
                conflict_id=str(conflict["conflict_id"]),
                expected_revision=int(conflict["revision"]),
                expected_policy_revision=int(conflict["policy_revision"]),
                expected_domain_revocation_epoch=int(conflict["domain_revocation_epoch"]),
                expected_recipient_credential_epoch=int(
                    conflict["recipient_credential_epoch"]
                ),
                expected_member_event_ids=member_ids,
                release_event_ids=frozenset({events[index].event_id}),
                reject_event_ids=frozenset({events[1 - index].event_id}),
                reason_code=f"prefer_sender_{index}",
            )
            for index in range(2)
        )
        decision_barrier = threading.Barrier(2)

        def decide(index: int) -> tuple[str, tuple[str, ...]]:
            decision_barrier.wait(timeout=5)
            try:
                result = conflicts[index].adjudicate(
                    actor=owner,
                    decision=decisions[index],
                    when=now,
                )
            except ConflictError:
                return "revision_fenced", ()
            return "resolved", result.released_event_ids

        with ThreadPoolExecutor(max_workers=2) as executor:
            outcomes = list(executor.map(decide, range(2)))
        assert sorted(state for state, _released in outcomes) == [
            "resolved",
            "revision_fenced",
        ]
        released_ids = next(released for state, released in outcomes if state == "resolved")
        assert len(released_ids) == 1
        released_id = released_ids[0]
        rejected_id = next(event.event_id for event in events if event.event_id != released_id)
        assert {
            str(row["event_id"]): str(row["current_fact"])
            for row in first.fetch_all(
                "SELECT event_id,current_fact FROM recipients WHERE recipient_id=?",
                (owner.harness_id,),
            )
        } == {
            released_id: DeliveryFact.QUEUED.value,
            rejected_id: DeliveryFact.REJECTED_BEFORE_ACCEPT.value,
        }
        persisted = first.fetch_one(
            "SELECT state,revision FROM task_conflicts WHERE conflict_id=?",
            (conflict["conflict_id"],),
        )
        assert persisted == {
            "state": "resolved",
            "revision": int(conflict["revision"]) + 1,
        }
        assert first.verify_audit_chain()[0] is True
    finally:
        if first is not None:
            first.close()
        if second is not None:
            second.close()
        administrator.execute(
            psycopg.sql.SQL("DROP SCHEMA {} CASCADE").format(
                psycopg.sql.Identifier(schema)
            )
        )
        administrator.close()
