"""Psycopg-backed single-primary commit authority with lease fencing.

This adapter verifies a writable primary, local WAL settings, exact schema,
and a current runtime fence. Those facts do not prove the replicated RPO
boundary required to emit ``accepted_durable``; this build therefore emits
the weaker and honest ``accepted_local`` fact.
"""

from __future__ import annotations

import hashlib
import sqlite3
import threading
import time
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any, Callable

import psycopg
from psycopg.conninfo import conninfo_to_dict
from psycopg.rows import dict_row

from agentnet.errors import GateBlocked, IdempotencyConflict, ReplayError, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS, Migration
from agentnet.storage.postgres_catalog import require_exact_postgres_catalog


MIGRATION_LOCK_ID = 0x41474E544D494752  # "AGNTMIGR", inside PostgreSQL signed-bigint range.
WRITE_LOCK_ID = 0x41474E5457524954  # "AGNTWRIT", serializes canonical chain/cursor writes.
_VERIFIED_POSTGRESQL_SEAL = object()


def validate_postgres_recovery_dsn(database_url: str) -> tuple[str, ...]:
    """Validate the libpq recovery-selection contract without resolving secrets.

    A recovery topology must give libpq at least two ordered candidates and ask
    it to select a read/write server.  Authentication may come from ``pgpass``
    or another process-external libpq mechanism, but a password must never be
    embedded in the serialized extension configuration.
    """

    try:
        parameters = conninfo_to_dict(database_url)
    except Exception as exc:
        raise ValidationError("PostgreSQL recovery DSN is not valid libpq conninfo") from exc
    if "password" in parameters:
        raise ValidationError("PostgreSQL recovery DSN must not embed a password")
    hosts = tuple(host.strip() for host in parameters.get("host", "").split(","))
    if len(hosts) < 2 or any(not host for host in hosts) or len(set(hosts)) != len(hosts):
        raise ValidationError("PostgreSQL recovery DSN requires at least two distinct hosts")
    if parameters.get("target_session_attrs") != "read-write":
        raise ValidationError("PostgreSQL recovery DSN requires target_session_attrs=read-write")
    return hosts


def translate_qmark_sql(query: str) -> str:
    """Translate SQLite-style qmarks outside quoted SQL literals."""

    rendered: list[str] = []
    single = False
    double = False
    index = 0
    while index < len(query):
        character = query[index]
        if character == "'" and not double:
            rendered.append(character)
            if single and index + 1 < len(query) and query[index + 1] == "'":
                rendered.append("'")
                index += 2
                continue
            single = not single
        elif character == '"' and not single:
            rendered.append(character)
            if double and index + 1 < len(query) and query[index + 1] == '"':
                rendered.append('"')
                index += 2
                continue
            double = not double
        elif character == "?" and not single and not double:
            rendered.append("%s")
        else:
            rendered.append(character)
        index += 1
    if single or double:
        raise ValidationError("unterminated quoted SQL rejected")
    return "".join(rendered)


def split_migration_statements(sql: str) -> tuple[str, ...]:
    """Split the controlled migration catalog into individual statements."""

    statements = tuple(statement.strip() for statement in sql.split(";") if statement.strip())
    if not statements:
        raise ValueError("migration contains no SQL statements")
    return statements


def validate_applied_migrations(
    applied: Sequence[Mapping[str, Any]],
    migrations: tuple[Migration, ...] = MIGRATIONS,
) -> int:
    """Require an untampered prefix; unknown/future history fails closed."""

    expected_by_version = {migration.version: migration for migration in migrations}
    versions = [int(row["version"]) for row in applied]
    if versions != list(range(1, len(versions) + 1)):
        raise GateBlocked("schema_history", "PostgreSQL migration history is not a contiguous prefix")
    for row in applied:
        version = int(row["version"])
        expected = expected_by_version.get(version)
        if expected is None:
            raise GateBlocked("schema_future", "database schema is newer than this extension")
        if row["name"] != expected.name or row["checksum"] != expected.checksum:
            raise GateBlocked("schema_tamper", f"PostgreSQL migration {version} checksum/name mismatch")
    current = versions[-1] if versions else 0
    minimum_supported = migrations[-1].version - 1
    if current and current < minimum_supported:
        raise GateBlocked(
            "schema_migration_window",
            "PostgreSQL schema is outside the exact N/N-1 migration window",
        )
    return current


_S4_TABLES = frozenset(
    {
        "bootstrap_grant_plans",
        "c0_plan_guards",
        "bootstrap_grant_plan_items",
        "c0_plan_guard_entitlements",
        "c0_pilot_attempts",
        "c0_pilot_facts",
    }
)
_S4_INDEX_COLUMNS = {
    "idx_bootstrap_grant_plans_active": (
        "bootstrap_grant_plans",
        ("domain_id", "principal_id", "profile", "state", "authority_expires_at"),
    ),
    "idx_c0_plan_guards_active": (
        "c0_plan_guards",
        ("domain_id", "principal_id", "state", "expires_at"),
    ),
    "idx_bootstrap_plan_items_entitlement": (
        "bootstrap_grant_plan_items",
        ("entitlement_id", "plan_id"),
    ),
    "idx_c0_guard_entitlements_lookup": (
        "c0_plan_guard_entitlements",
        ("entitlement_id", "guard_id"),
    ),
    "idx_c0_pilot_attempts_state": ("c0_pilot_attempts", ("state", "expires_at")),
    "idx_c0_pilot_facts_event": ("c0_pilot_facts", ("event_id", "attempt_id")),
}
_S4_CONSTRAINT_COUNTS = {
    "bootstrap_grant_plans": (22, 6, 7, 1),
    "c0_plan_guards": (14, 5, 1, 1),
    "bootstrap_grant_plan_items": (5, 2, 3, 1),
    "c0_plan_guard_entitlements": (1, 4, 0, 1),
    "c0_pilot_attempts": (5, 2, 4, 1),
    "c0_pilot_facts": (5, 2, 0, 1),
}


def require_s4_postgres_catalog(connection: Any) -> None:
    """Fail closed unless the live PostgreSQL S4 catalog matches the released shape."""

    relations = connection.execute(
        """SELECT relation.relname AS name,relation.relkind AS kind
             FROM pg_catalog.pg_class relation
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
            WHERE namespace.nspname=current_schema() AND relation.relname=ANY(%s)
            ORDER BY relation.relname""",
        (sorted(_S4_TABLES | frozenset(_S4_INDEX_COLUMNS)),),
    ).fetchall()
    expected_relations = {
        **{name: "r" for name in _S4_TABLES},
        **{name: "i" for name in _S4_INDEX_COLUMNS},
    }
    actual_relations = {str(row["name"]): str(row["kind"]) for row in relations}
    if actual_relations != expected_relations:
        raise GateBlocked("schema_s4_catalog", "PostgreSQL S4 relation catalog is not exact")

    constraint_rows = connection.execute(
        """SELECT relation.relname AS table_name,
                  COUNT(*) FILTER (WHERE con.contype='c') AS check_count,
                  COUNT(*) FILTER (WHERE con.contype='f') AS foreign_count,
                  COUNT(*) FILTER (WHERE con.contype='u') AS unique_count,
                  COUNT(*) FILTER (WHERE con.contype='p') AS primary_count
             FROM pg_catalog.pg_class relation
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=relation.relnamespace
             LEFT JOIN pg_catalog.pg_constraint con ON con.conrelid=relation.oid
            WHERE namespace.nspname=current_schema() AND relation.relname=ANY(%s)
            GROUP BY relation.relname ORDER BY relation.relname""",
        (sorted(_S4_TABLES),),
    ).fetchall()
    actual_constraints = {
        str(row["table_name"]): (
            int(row["check_count"]),
            int(row["foreign_count"]),
            int(row["unique_count"]),
            int(row["primary_count"]),
        )
        for row in constraint_rows
    }
    if actual_constraints != _S4_CONSTRAINT_COUNTS:
        raise GateBlocked("schema_s4_constraints", "PostgreSQL S4 constraints are not exact")

    index_rows = connection.execute(
        """SELECT index_relation.relname AS index_name,table_relation.relname AS table_name,
                  ARRAY_AGG(attribute.attname ORDER BY key_column.ordinality) AS columns
             FROM pg_catalog.pg_index index_catalog
             JOIN pg_catalog.pg_class index_relation ON index_relation.oid=index_catalog.indexrelid
             JOIN pg_catalog.pg_class table_relation ON table_relation.oid=index_catalog.indrelid
             JOIN pg_catalog.pg_namespace namespace ON namespace.oid=table_relation.relnamespace
             JOIN LATERAL UNNEST(index_catalog.indkey) WITH ORDINALITY AS key_column(attnum,ordinality)
               ON TRUE
             JOIN pg_catalog.pg_attribute attribute
               ON attribute.attrelid=table_relation.oid AND attribute.attnum=key_column.attnum
            WHERE namespace.nspname=current_schema() AND index_relation.relname=ANY(%s)
            GROUP BY index_relation.relname,table_relation.relname ORDER BY index_relation.relname""",
        (sorted(_S4_INDEX_COLUMNS),),
    ).fetchall()
    actual_indexes = {
        str(row["index_name"]): (
            str(row["table_name"]),
            tuple(str(column) for column in row["columns"]),
        )
        for row in index_rows
    }
    if actual_indexes != _S4_INDEX_COLUMNS:
        raise GateBlocked("schema_s4_indexes", "PostgreSQL S4 indexes are not exact")


def apply_postgres_migrations(connection: Any) -> int:
    """Apply every pending numbered migration in one crash-atomic transaction."""

    with connection.transaction():
        connection.execute("SELECT pg_advisory_xact_lock(%s)", (MIGRATION_LOCK_ID,))
        # Clean-start first release: inspect before creating any catalog object.
        # A nonempty schema without the exact released catalog is a prototype or
        # foreign schema and must not be silently stamped as AgentNet v1.  The
        # advisory lock and surrounding transaction make the inspection and
        # first install one atomic decision.
        relations = connection.execute(
            """
            SELECT relation.relname AS name, relation.relkind AS kind
              FROM pg_catalog.pg_class relation
              JOIN pg_catalog.pg_namespace namespace
                ON namespace.oid=relation.relnamespace
             WHERE namespace.nspname=current_schema()
               AND relation.relkind IN ('r','p','v','m','S','f')
             ORDER BY relation.relname
            """
        ).fetchall()
        relation_names = {str(row["name"]) for row in relations}
        forbidden_pre_release = {
            "relationships",
            "relationship_legacy_quarantine",
        }
        if relation_names & forbidden_pre_release:
            raise GateBlocked(
                "schema_legacy",
                "pre-release PostgreSQL relationship schemas require explicit export and clean reinitialization",
            )
        catalog_present = "schema_migrations" in relation_names
        if relation_names and not catalog_present:
            raise GateBlocked(
                "schema_legacy",
                "nonempty unversioned PostgreSQL schemas are not accepted by the first release",
            )
        if catalog_present:
            applied = connection.execute(
                "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
            ).fetchall()
            current = validate_applied_migrations(applied)
            if current == 0:
                raise GateBlocked(
                    "schema_migration_history",
                    "an empty PostgreSQL migration catalog is not a released schema",
                )
            if current == CURRENT_SCHEMA_VERSION - 1 and relation_names & _S4_TABLES:
                raise GateBlocked(
                    "schema_s4_partial",
                    "PostgreSQL v3 contains unsupported partial S4 relations",
                )
            require_exact_postgres_catalog(connection, migrations=MIGRATIONS[:current])
        else:
            connection.execute(
                """
                CREATE TABLE schema_migrations (
                    version BIGINT PRIMARY KEY,
                    name TEXT NOT NULL,
                    checksum TEXT NOT NULL,
                    applied_at BIGINT NOT NULL
                )
                """
            )
            current = 0
        for migration in MIGRATIONS:
            if migration.version <= current:
                continue
            for statement in split_migration_statements(migration.sql):
                connection.execute(statement)
            connection.execute(
                "INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(%s,%s,%s,%s)",
                (migration.version, migration.name, migration.checksum, int(time.time())),
            )
        connection.execute(
            """INSERT INTO metadata(key,value) VALUES('schema_version',%s)
               ON CONFLICT(key) DO UPDATE SET value=excluded.value""",
            (str(CURRENT_SCHEMA_VERSION),),
        )
        require_exact_postgres_catalog(connection, migrations=MIGRATIONS)
        require_s4_postgres_catalog(connection)
    return CURRENT_SCHEMA_VERSION


def require_current_postgres_schema(connection: Any) -> None:
    try:
        applied = connection.execute(
            "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
        ).fetchall()
        current = validate_applied_migrations(applied)
        metadata = connection.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("schema_missing", "PostgreSQL schema metadata is unavailable") from exc
    if current != CURRENT_SCHEMA_VERSION or metadata is None or int(metadata["value"]) != CURRENT_SCHEMA_VERSION:
        raise GateBlocked("schema_version", "PostgreSQL schema is not at the exact runtime version")
    require_exact_postgres_catalog(connection, migrations=MIGRATIONS)
    require_s4_postgres_catalog(connection)


@dataclass(frozen=True, slots=True)
class LeaseToken:
    lease_name: str
    owner_id: str
    fence: int
    expires_at: int


class PostgreSQLConnectionAdapter:
    """Present the narrow sqlite-shaped cursor API used by existing services."""

    def __init__(self, connection: Any) -> None:
        self.raw = connection

    def execute(self, query: str, parameters: tuple[Any, ...] = ()) -> Any:
        translated = translate_qmark_sql(query)
        try:
            return self.raw.execute(translated, parameters)
        except psycopg.IntegrityError as exc:
            # Existing services deliberately catch sqlite3.IntegrityError at
            # replay/idempotency boundaries.  Preserve that backend-neutral
            # contract without leaking PostgreSQL details.
            raise sqlite3.IntegrityError("database integrity constraint rejected") from exc


class PostgreSQLStore:
    backend_name = "postgresql"

    def __init__(
        self,
        database_url: str,
        cipher: LocalEnvelopeCipher,
        *,
        instance_id: str,
        lease_owner_id: str | None = None,
        connect_timeout: int = 5,
        statement_timeout_ms: int = 15_000,
        lock_timeout_ms: int = 5_000,
        lease_ttl_seconds: int = 30,
        run_migrations: bool = True,
        verify_schema: bool = True,
        connector: Callable[..., Any] = psycopg.connect,
        clock: Callable[[], int] | None = None,
        start_lease_keeper: bool = True,
        require_recovery_topology: bool = False,
    ) -> None:
        if not database_url.startswith(("postgresql://", "postgres://")):
            raise ValidationError("PostgreSQL store requires a PostgreSQL DSN")
        if not instance_id or len(instance_id) > 128:
            raise ValidationError("PostgreSQL runtime instance_id is invalid")
        if lease_owner_id is not None and (not lease_owner_id or len(lease_owner_id) > 128):
            raise ValidationError("PostgreSQL runtime lease_owner_id is invalid")
        if not 10 <= lease_ttl_seconds <= 300:
            raise ValidationError("PostgreSQL lease TTL outside profile")
        if require_recovery_topology:
            validate_postgres_recovery_dsn(database_url)
        self.database_url = database_url
        self.cipher = cipher
        self.instance_id = instance_id
        self.lease_owner_id = lease_owner_id or instance_id
        self._clock = clock or (lambda: int(time.time()))
        self._lease_ttl = lease_ttl_seconds
        self._connector = connector
        self._connect_timeout = connect_timeout
        self._statement_timeout_ms = statement_timeout_ms
        self._lock_timeout_ms = lock_timeout_ms
        self._verify_schema = verify_schema
        self._start_lease_keeper = start_lease_keeper
        self._lock = threading.RLock()
        self._closed = False
        self._lease_lost_reason: str | None = None
        self._reconnect_required = False
        self._unknown_operation_count = 0
        self._stop = threading.Event()
        self._keeper: threading.Thread | None = None
        self._keeper_restart_needed = False
        self._postgresql_seal: object | None = None
        self._connection = connector(
            database_url,
            autocommit=True,
            row_factory=dict_row,
            connect_timeout=connect_timeout,
            application_name=f"agentnet:{instance_id}",
        )
        try:
            if run_migrations:
                apply_postgres_migrations(self._connection)
            if verify_schema:
                require_current_postgres_schema(self._connection)
        except Exception:
            self._connection.close()
            raise
        self._connection.execute("SELECT set_config('synchronous_commit','on',false)")
        self._connection.execute("SELECT set_config('statement_timeout',%s,false)", (str(statement_timeout_ms),))
        self._connection.execute("SELECT set_config('lock_timeout',%s,false)", (str(lock_timeout_ms),))
        self._connection.execute("SELECT set_config('timezone','UTC',false)")
        durability = self._connection.execute(
            """SELECT current_setting('synchronous_commit') AS synchronous_commit,
                      current_setting('fsync') AS fsync,
                      current_setting('full_page_writes') AS full_page_writes,
                      pg_is_in_recovery() AS in_recovery"""
        ).fetchone()
        if (
            durability is None
            or durability["synchronous_commit"] != "on"
            or durability["fsync"] != "on"
            or durability["full_page_writes"] != "on"
            or durability["in_recovery"]
        ):
            self._connection.close()
            raise GateBlocked(
                "postgres_durability",
                "PostgreSQL must be a writable primary with synchronous_commit, fsync, and full_page_writes enabled",
            )
        if connector is psycopg.connect and verify_schema:
            self._postgresql_seal = _VERIFIED_POSTGRESQL_SEAL
        self._adapter = PostgreSQLConnectionAdapter(self._connection)
        try:
            self._lease = self.acquire_lease(
                f"server-agent.instance:{instance_id}",
                owner_id=self.lease_owner_id,
                ttl_seconds=lease_ttl_seconds,
            )
        except Exception:
            self._closed = True
            self._connection.close()
            raise
        if start_lease_keeper:
            self._keeper = threading.Thread(
                target=self._keep_lease,
                name=f"agentnet-lease-{instance_id}",
                daemon=True,
            )
            self._keeper.start()

    def _open_connection(self) -> Any:
        return self._connector(
            self.database_url,
            autocommit=True,
            row_factory=dict_row,
            connect_timeout=self._connect_timeout,
            application_name=f"agentnet:{self.instance_id}",
        )

    def _configure_and_verify_connection(self, connection: Any, *, migrate: bool) -> None:
        if migrate:
            apply_postgres_migrations(connection)
        if self._verify_schema:
            require_current_postgres_schema(connection)
        connection.execute("SELECT set_config('synchronous_commit','on',false)")
        connection.execute(
            "SELECT set_config('statement_timeout',%s,false)",
            (str(self._statement_timeout_ms),),
        )
        connection.execute(
            "SELECT set_config('lock_timeout',%s,false)",
            (str(self._lock_timeout_ms),),
        )
        connection.execute("SELECT set_config('timezone','UTC',false)")
        durability = connection.execute(
            """SELECT current_setting('synchronous_commit') AS synchronous_commit,
                      current_setting('fsync') AS fsync,
                      current_setting('full_page_writes') AS full_page_writes,
                      pg_is_in_recovery() AS in_recovery"""
        ).fetchone()
        if (
            durability is None
            or durability["synchronous_commit"] != "on"
            or durability["fsync"] != "on"
            or durability["full_page_writes"] != "on"
            or durability["in_recovery"]
        ):
            raise GateBlocked(
                "postgres_durability",
                "PostgreSQL must be a writable primary with synchronous_commit, fsync, and full_page_writes enabled",
            )

    def _acquire_runtime_lease_on(
        self,
        connection: Any,
        *,
        prior_fence: int | None,
    ) -> LeaseToken:
        now = self._clock()
        expires_at = now + self._lease_ttl
        lease_name = f"server-agent.instance:{self.instance_id}"
        with connection.transaction():
            row = connection.execute(
                """
                INSERT INTO runtime_leases(lease_name,owner_id,fence,acquired_at,heartbeat_at,expires_at)
                VALUES(%s,%s,1,%s,%s,%s)
                ON CONFLICT(lease_name) DO UPDATE SET
                    owner_id=excluded.owner_id,
                    fence=runtime_leases.fence+1,
                    acquired_at=excluded.acquired_at,
                    heartbeat_at=excluded.heartbeat_at,
                    expires_at=excluded.expires_at
                WHERE runtime_leases.expires_at<=excluded.acquired_at OR runtime_leases.owner_id=excluded.owner_id
                RETURNING lease_name,owner_id,fence,expires_at
                """,
                (lease_name, self.lease_owner_id, now, now, expires_at),
            ).fetchone()
        if row is None:
            raise GateBlocked("lease_contended", "PostgreSQL runtime lease is held by another owner")
        token = LeaseToken(
            row["lease_name"],
            row["owner_id"],
            int(row["fence"]),
            int(row["expires_at"]),
        )
        if prior_fence is not None and token.fence <= prior_fence:
            raise GateBlocked(
                "postgres_fence_regression",
                "reconnected PostgreSQL primary did not issue a strictly higher runtime fence",
            )
        return token

    @staticmethod
    def _is_connection_failure(exc: BaseException, connection: Any) -> bool:
        return isinstance(exc, (psycopg.OperationalError, psycopg.InterfaceError)) or bool(
            getattr(connection, "broken", False)
        )

    def _mark_operation_unknown(self, connection: Any) -> None:
        self._unknown_operation_count += 1
        self._reconnect_required = True
        self._lease_lost_reason = "connection_lost_operation_unknown"
        if threading.current_thread() is self._keeper:
            self._keeper_restart_needed = True
        try:
            connection.close()
        except Exception:
            pass

    def _recover_for_subsequent_operation(self) -> None:
        if not self._reconnect_required:
            return
        if (
            self._keeper_restart_needed
            and self._keeper is not None
            and self._keeper is not threading.current_thread()
            and self._keeper.is_alive()
        ):
            self._keeper.join(timeout=1)
        prior_fence = self._lease.fence
        try:
            connection = self._open_connection()
            try:
                # A reconnect never mutates schema.  It accepts only the exact
                # version this process started with on a writable durable node.
                self._configure_and_verify_connection(connection, migrate=False)
                lease = self._acquire_runtime_lease_on(connection, prior_fence=prior_fence)
            except BaseException:
                connection.close()
                raise
        except BaseException as exc:
            self._reconnect_required = True
            self._lease_lost_reason = f"recovery_rejected:{type(exc).__name__}"
            if isinstance(exc, GateBlocked):
                raise
            raise GateBlocked(
                "postgres_reconnect_failed",
                "PostgreSQL recovery connection was not accepted",
            ) from exc
        self._connection = connection
        self._adapter = PostgreSQLConnectionAdapter(connection)
        self._lease = lease
        self._reconnect_required = False
        self._lease_lost_reason = None
        if self._start_lease_keeper and (
            self._keeper_restart_needed or self._keeper is None or not self._keeper.is_alive()
        ):
            self._keeper = threading.Thread(
                target=self._keep_lease,
                name=f"agentnet-lease-{self.instance_id}",
                daemon=True,
            )
            self._keeper_restart_needed = False
            self._keeper.start()

    @contextmanager
    def _raw_transaction(self) -> Iterator[PostgreSQLConnectionAdapter]:
        with self._lock:
            if self._closed:
                raise GateBlocked("storage_closed", "PostgreSQL storage is closed")
            self._recover_for_subsequent_operation()
            connection = self._connection
            try:
                with connection.transaction():
                    yield self._adapter
            except BaseException as exc:
                if self._is_connection_failure(exc, connection):
                    self._mark_operation_unknown(connection)
                    raise GateBlocked(
                        "postgres_operation_unknown",
                        "PostgreSQL connection was lost; current operation outcome is unknown and was not retried",
                    ) from exc
                raise

    def acquire_lease(self, lease_name: str, *, owner_id: str, ttl_seconds: int) -> LeaseToken:
        if not lease_name or not owner_id or not 10 <= ttl_seconds <= 300:
            raise ValidationError("runtime lease name, owner, or TTL is outside profile")
        now = self._clock()
        expires_at = now + ttl_seconds
        with self._raw_transaction() as connection:
            row = connection.execute(
                """
                INSERT INTO runtime_leases(lease_name,owner_id,fence,acquired_at,heartbeat_at,expires_at)
                VALUES(?,?,1,?,?,?)
                ON CONFLICT(lease_name) DO UPDATE SET
                    owner_id=excluded.owner_id,
                    fence=runtime_leases.fence+1,
                    acquired_at=excluded.acquired_at,
                    heartbeat_at=excluded.heartbeat_at,
                    expires_at=excluded.expires_at
                WHERE runtime_leases.expires_at<=excluded.acquired_at OR runtime_leases.owner_id=excluded.owner_id
                RETURNING lease_name,owner_id,fence,expires_at
                """,
                (lease_name, owner_id, now, now, expires_at),
            ).fetchone()
            if row is None:
                raise GateBlocked("lease_contended", f"runtime lease {lease_name} is held by another owner")
        return LeaseToken(row["lease_name"], row["owner_id"], int(row["fence"]), int(row["expires_at"]))

    def heartbeat_lease(self, token: LeaseToken) -> LeaseToken:
        now = self._clock()
        expires_at = now + self._lease_ttl
        with self._raw_transaction() as connection:
            row = connection.execute(
                """UPDATE runtime_leases SET heartbeat_at=?,expires_at=?
                   WHERE lease_name=? AND owner_id=? AND fence=? AND expires_at>?
                   RETURNING lease_name,owner_id,fence,expires_at""",
                (now, expires_at, token.lease_name, token.owner_id, token.fence, now),
            ).fetchone()
            if row is None:
                raise GateBlocked("lease_lost", "PostgreSQL runtime lease was fenced or expired")
        return LeaseToken(row["lease_name"], row["owner_id"], int(row["fence"]), int(row["expires_at"]))

    def _keep_lease(self) -> None:
        interval = max(1.0, self._lease_ttl / 3)
        while not self._stop.wait(interval):
            try:
                self._lease = self.heartbeat_lease(self._lease)
            except Exception as exc:  # fail closed until a later operation completes recovery
                if not self._keeper_restart_needed:
                    self._lease_lost_reason = type(exc).__name__
                return

    def _require_runtime_lease(self, connection: PostgreSQLConnectionAdapter) -> None:
        if self._lease_lost_reason is not None:
            raise GateBlocked("lease_lost", "PostgreSQL runtime lease is not current")
        now = self._clock()
        row = connection.execute(
            """SELECT 1 AS current FROM runtime_leases
               WHERE lease_name=? AND owner_id=? AND fence=? AND expires_at>?""",
            (self._lease.lease_name, self._lease.owner_id, self._lease.fence, now),
        ).fetchone()
        if row is None:
            self._lease_lost_reason = "fenced_or_expired"
            raise GateBlocked("lease_lost", "PostgreSQL runtime lease is not current")

    @contextmanager
    def transaction(self, *, immediate: bool = True) -> Iterator[PostgreSQLConnectionAdapter]:
        del immediate
        with self._raw_transaction() as connection:
            # One transaction owns the audit-chain tail and mailbox cursor at a
            # time across every always-on server-agent instance.  This is intentionally simple;
            # future measured scale can replace it without changing schemas.
            connection.execute("SELECT pg_advisory_xact_lock(?)", (WRITE_LOCK_ID,))
            self._require_runtime_lease(connection)
            yield connection

    def close(self) -> None:
        self._stop.set()
        if self._keeper is not None:
            self._keeper.join(timeout=5)
        with self._lock:
            if self._closed:
                return
            try:
                if not self._reconnect_required:
                    now = self._clock()
                    with self._connection.transaction():
                        self._adapter.execute(
                            """UPDATE runtime_leases SET expires_at=?,heartbeat_at=?
                               WHERE lease_name=? AND owner_id=? AND fence=?""",
                            (now, now, self._lease.lease_name, self._lease.owner_id, self._lease.fence),
                        )
            except Exception:
                # Shutdown never reconnects and never turns a failed lease
                # release into evidence that the previous operation committed.
                pass
            finally:
                self._closed = True
                self._connection.close()

    def fetch_one(self, query: str, parameters: tuple[Any, ...] = ()) -> Any | None:
        with self._lock:
            if self._closed:
                raise GateBlocked("storage_closed", "PostgreSQL storage is closed")
            self._recover_for_subsequent_operation()
            connection = self._connection
            try:
                return self._adapter.execute(query, parameters).fetchone()
            except BaseException as exc:
                if self._is_connection_failure(exc, connection):
                    self._mark_operation_unknown(connection)
                    raise GateBlocked(
                        "postgres_operation_unknown",
                        "PostgreSQL connection was lost; current operation outcome is unknown and was not retried",
                    ) from exc
                raise

    def fetch_all(self, query: str, parameters: tuple[Any, ...] = ()) -> list[Any]:
        with self._lock:
            if self._closed:
                raise GateBlocked("storage_closed", "PostgreSQL storage is closed")
            self._recover_for_subsequent_operation()
            connection = self._connection
            try:
                return list(self._adapter.execute(query, parameters).fetchall())
            except BaseException as exc:
                if self._is_connection_failure(exc, connection):
                    self._mark_operation_unknown(connection)
                    raise GateBlocked(
                        "postgres_operation_unknown",
                        "PostgreSQL connection was lost; current operation outcome is unknown and was not retried",
                    ) from exc
                raise

    def consume_once(self, actor_id: str, nonce: str, *, expires_at: int) -> None:
        nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        now = self._clock()
        with self.transaction() as connection:
            connection.execute("DELETE FROM replay_nonces WHERE expires_at < ?", (now,))
            try:
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (actor_id, nonce_hash, expires_at),
                )
            except sqlite3.IntegrityError as exc:
                raise ReplayError("proof nonce was already consumed") from exc

    def enforce_idempotency(
        self,
        *,
        domain_id: str,
        actor_json: str,
        idempotency_key: str,
        digest: str,
    ) -> Any | None:
        row = self.fetch_one(
            "SELECT * FROM events WHERE domain_id=? AND actor_json=? AND idempotency_key=?",
            (domain_id, actor_json, idempotency_key),
        )
        if row is not None and row["envelope_digest"] != digest:
            raise IdempotencyConflict("same idempotency key was used with different bytes")
        return row

    def append_audit(self, connection: PostgreSQLConnectionAdapter, record: Mapping[str, Any]) -> str:
        previous = connection.execute(
            "SELECT record_hash FROM audit_log ORDER BY sequence DESC LIMIT 1 FOR UPDATE"
        ).fetchone()
        previous_hash = previous["record_hash"] if previous else "0" * 64
        occurred_at = self._clock()
        serialized = canonical_json(dict(record)).decode("utf-8")
        preimage = (
            previous_hash.encode("ascii")
            + b"\x00"
            + str(occurred_at).encode("ascii")
            + b"\x00"
            + serialized.encode("utf-8")
        )
        record_hash = hashlib.sha256(preimage).hexdigest()
        connection.execute(
            "INSERT INTO audit_log(occurred_at,record_json,previous_hash,record_hash) VALUES(?,?,?,?)",
            (occurred_at, serialized, previous_hash, record_hash),
        )
        return record_hash

    def verify_audit_chain(self) -> tuple[bool, int]:
        rows = self.fetch_all("SELECT * FROM audit_log ORDER BY sequence")
        previous_hash = "0" * 64
        for row in rows:
            preimage = (
                previous_hash.encode("ascii")
                + b"\x00"
                + str(row["occurred_at"]).encode("ascii")
                + b"\x00"
                + row["record_json"].encode("utf-8")
            )
            expected = hashlib.sha256(preimage).hexdigest()
            if row["previous_hash"] != previous_hash or row["record_hash"] != expected:
                return False, int(row["sequence"])
            previous_hash = row["record_hash"]
        return True, len(rows)

    def encrypted_payload(self, payload: Mapping[str, Any], event_id: str) -> str:
        return self.cipher.encrypt_json(dict(payload), purpose=f"event:{event_id}")

    def decrypted_payload(self, token: str, event_id: str) -> dict[str, Any]:
        value = self.cipher.decrypt_json(token, purpose=f"event:{event_id}")
        if not isinstance(value, dict):
            raise TypeError("event payload is not an object")
        return value

    def lease_status(self) -> dict[str, Any]:
        now = self._clock()
        try:
            row = self.fetch_one(
                "SELECT owner_id,fence,expires_at FROM runtime_leases WHERE lease_name=?",
                (self._lease.lease_name,),
            )
        except Exception as exc:
            return {"ready": False, "reason": type(exc).__name__}
        current = bool(
            row
            and row["owner_id"] == self._lease.owner_id
            and int(row["fence"]) == self._lease.fence
            and int(row["expires_at"]) > now
            and self._lease_lost_reason is None
        )
        return {
            "ready": current,
            "fence": self._lease.fence,
            "expires_at": int(row["expires_at"]) if row else None,
            "reason": self._lease_lost_reason,
        }

    def readiness(self) -> dict[str, Any]:
        try:
            row = self.fetch_one(
                """SELECT current_setting('server_version') AS server_version,
                          current_setting('synchronous_commit') AS synchronous_commit,
                          current_setting('fsync') AS fsync,
                          current_setting('full_page_writes') AS full_page_writes,
                          pg_is_in_recovery() AS in_recovery"""
            )
            schema = self.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
            migrations = self.fetch_one("SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations")
        except Exception as exc:
            return {"ready": False, "backend": self.backend_name, "reason": type(exc).__name__}
        schema_version = int(schema["value"]) if schema else 0
        migration_version = int(migrations["version"]) if migrations else 0
        lease = self.lease_status()
        ready = bool(
            row
            and row["synchronous_commit"] == "on"
            and row["fsync"] == "on"
            and row["full_page_writes"] == "on"
            and not row["in_recovery"]
            and schema_version == CURRENT_SCHEMA_VERSION
            and migration_version == CURRENT_SCHEMA_VERSION
            and lease["ready"]
            and is_verified_postgresql_store(self)
        )
        return {
            "ready": ready,
            "backend": self.backend_name,
            "server_version": row["server_version"] if row else None,
            "synchronous_commit": row["synchronous_commit"] if row else None,
            "fsync": row["fsync"] if row else None,
            "full_page_writes": row["full_page_writes"] if row else None,
            "in_recovery": bool(row["in_recovery"]) if row else None,
            "schema_version": schema_version,
            "expected_schema_version": CURRENT_SCHEMA_VERSION,
            "lease": lease,
            "commit_boundary": "single_postgresql_primary_local_wal",
            "postgresql_commit_verified": is_verified_postgresql_store(self),
            "acceptance_fact": "accepted_local",
            "accepted_durable_enabled": False,
            "ha_claimed": False,
            "reconnect_required": self._reconnect_required,
            "unknown_operation_count": self._unknown_operation_count,
        }


def is_verified_postgresql_store(store: Any) -> bool:
    """Return true only for the exact psycopg/schema/settings-verified backend.

    This is a backend/type seal, not HA, PITR, restore, or RPO evidence.
    """

    return (
        type(store) is PostgreSQLStore
        and getattr(store, "_postgresql_seal", None) is _VERIFIED_POSTGRESQL_SEAL
    )


class PostgreSQLReadiness:
    """Non-mutating preflight probe for operators and conditional tests."""

    def __init__(self, database_url: str) -> None:
        self.database_url = database_url

    def probe(self) -> dict[str, Any]:
        try:
            with psycopg.connect(self.database_url, connect_timeout=3) as connection:
                row = connection.execute(
                    """SELECT current_setting('server_version'), current_setting('synchronous_commit'),
                              current_setting('fsync'), current_setting('full_page_writes'), pg_is_in_recovery()"""
                ).fetchone()
                schema = connection.execute("SELECT value FROM metadata WHERE key='schema_version'").fetchone()
                migration = connection.execute("SELECT COALESCE(MAX(version),0) FROM schema_migrations").fetchone()
        except Exception as exc:
            return {"ready": False, "reason": type(exc).__name__}
        schema_version = int(schema[0]) if schema else 0
        migration_version = int(migration[0]) if migration else 0
        return {
            "ready": bool(
                row[1] == "on"
                and row[2] == "on"
                and row[3] == "on"
                and not row[4]
                and schema_version == CURRENT_SCHEMA_VERSION
                and migration_version == CURRENT_SCHEMA_VERSION
            ),
            "server_version": row[0],
            "synchronous_commit": row[1],
            "fsync": row[2],
            "full_page_writes": row[3],
            "in_recovery": bool(row[4]),
            "schema_version": schema_version,
            "expected_schema_version": CURRENT_SCHEMA_VERSION,
            "ha_claimed": False,
        }
