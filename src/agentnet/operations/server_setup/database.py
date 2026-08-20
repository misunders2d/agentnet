"""PostgreSQL validation and lifecycle migration for server setup."""

from __future__ import annotations

import hashlib
import json
import os
import re
import select
import time
from typing import Any, Callable, Literal, Mapping

if os.name == "posix":
    import pwd

from agentnet.errors import GateBlocked
from agentnet.operations.c0_credential_supersession import (
    load_audited_supersession_journal,
    load_supersession_journal,
)
from agentnet.storage.migrations import MIGRATIONS
from agentnet.storage.postgres import (
    MIGRATION_LOCK_ID,
    ORDINARY_SERVER_POSTGRES_DATABASE,
    ORDINARY_SERVER_POSTGRES_SOCKET,
    ORDINARY_SERVER_POSTGRES_USER,
    apply_postgres_migrations,
    inspect_ordinary_server_postgres_auth,
    probe_ordinary_server_postgres_connection,
    validate_applied_migrations,
)
from agentnet.storage.postgres_catalog import require_exact_postgres_catalog

from .custody import _drop_identity
from .preflight import _strict_json_bytes
from .models import ServerSetupError
from .systemd import CORE_USER, _SYSTEM_PATH

_LIFECYCLE_SETUP_UPGRADE = ("0.1.44", "0.1.45")
_LIFECYCLE_SOURCE_SCHEMA = 6
_LIFECYCLE_TARGET_SCHEMA = 7
_LIFECYCLE_UPGRADE_JOURNAL_SCHEMA = "agentnet.server-setup.upgrade-journal.v4"
_LIFECYCLE_RELEASE_TABLES = (
    "invitation_link_failures",
    "invitation_links",
    "artifact_transfer_recipients",
    "artifact_transfers",
    "collaboration_scope_members",
    "collaboration_scopes",
    "endpoint_lifecycle",
)
_LIFECYCLE_PRESERVED_TABLES = (
    "domains",
    "principals",
    "principal_aliases",
    "harnesses",
    "credentials",
    "entitlements",
    "events",
    "recipients",
    "communication_scopes",
)


def _run_postgres_probe_as(
    account: pwd.struct_passwd,
    probe: Callable[[], dict[str, Any]],
    *,
    stage: str,
) -> dict[str, Any]:
    read_descriptor, write_descriptor = os.pipe()
    try:
        child = os.fork()
    except OSError as exc:
        os.close(read_descriptor)
        os.close(write_descriptor)
        raise ServerSetupError("postgres_preflight", f"{stage} could not start") from exc
    if child == 0:
        try:
            os.close(read_descriptor)
            try:
                _drop_identity(account)()
                os.environ.clear()
                os.environ.update(
                    {
                        "PATH": _SYSTEM_PATH,
                        "HOME": account.pw_dir,
                        "LANG": "C.UTF-8",
                    }
                )
                evidence = probe()
            except BaseException as exc:
                evidence = {"ready": False, "reason": type(exc).__name__}
            payload = json.dumps(evidence, sort_keys=True, separators=(",", ":")).encode()
            if len(payload) > 16_384:
                payload = b'{"ready":false,"reason":"OversizedEvidence"}'
            os.write(write_descriptor, payload)
        finally:
            os.close(write_descriptor)
            os._exit(0)
    os.close(write_descriptor)
    try:
        readable, _, _ = select.select([read_descriptor], [], [], 10)
        if not readable:
            os.kill(child, 9)
            os.waitpid(child, 0)
            raise ServerSetupError("postgres_preflight", f"{stage} timed out")
        payload = os.read(read_descriptor, 16_385)
        _, wait_status = os.waitpid(child, 0)
    finally:
        os.close(read_descriptor)
    if not payload or len(payload) > 16_384 or os.waitstatus_to_exitcode(wait_status) != 0:
        raise ServerSetupError("postgres_preflight", f"{stage} returned invalid evidence")
    try:
        evidence = _strict_json_bytes(payload, label=f"{stage} evidence")
    except ServerSetupError as exc:
        raise ServerSetupError("postgres_preflight", f"{stage} returned invalid evidence") from exc
    if evidence.get("ready") is not True:
        reason = evidence.get("reason")
        reason_class = reason if isinstance(reason, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", reason) else "Unavailable"
        raise ServerSetupError(
            "postgres_auth_not_ready",
            f"{stage} failed ({reason_class}); apply the exact operator-owned PostgreSQL peer rule, reload PostgreSQL, and retry the same approved digest",
        )
    return evidence


def _postgres_relation_digest(connection: Any, relation: str) -> str:
    """Hash one preserved relation without exporting its protected row values."""

    if relation not in _LIFECYCLE_PRESERVED_TABLES:
        raise ServerSetupError("setup_upgrade_conflict", "upgrade digest relation is invalid")
    digest = hashlib.sha256()
    cursor = connection.cursor()
    try:
        cursor.execute(
            f"""
            SELECT row_json
              FROM (
                    SELECT to_jsonb(snapshot_row)::text AS row_json
                      FROM "{relation}" AS snapshot_row
                   ) AS serialized_rows
             ORDER BY row_json
            """
        )
        while True:
            rows = cursor.fetchmany(256)
            if not rows:
                break
            for row in rows:
                value = row["row_json"]
                if not isinstance(value, str):
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "preserved PostgreSQL relation could not be serialized",
                    )
                digest.update(value.encode("utf-8"))
                digest.update(b"\n")
    finally:
        cursor.close()
    return digest.hexdigest()


def _postgres_migration_catalog(connection: Any) -> list[dict[str, Any]]:
    rows = connection.execute(
        "SELECT version,name,checksum,applied_at FROM schema_migrations ORDER BY version"
    ).fetchall()
    return [
        {
            "version": int(row["version"]),
            "name": str(row["name"]),
            "checksum": str(row["checksum"]),
            "applied_at": int(row["applied_at"]),
        }
        for row in rows
    ]


def _postgres_schema_version(connection: Any) -> int:
    row = connection.execute(
        "SELECT value FROM metadata WHERE key='schema_version'"
    ).fetchone()
    if row is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL schema version metadata is absent",
        )
    try:
        value = int(row["value"])
    except (KeyError, TypeError, ValueError) as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL schema version metadata is invalid",
        ) from exc
    return value


def _postgres_v0145_identity(
    connection: Any,
    *,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, str]:
    row = connection.execute(
        """
        SELECT harness.domain_id,harness.harness_id,harness.principal_id,
               harness.kind,harness.status AS harness_status,
               harness.credential_epoch,credential.credential_id,
               credential.status AS credential_status,credential.epoch
          FROM harnesses AS harness
          JOIN credentials AS credential
            ON credential.harness_id=harness.harness_id
         WHERE harness.domain_id=%s AND harness.harness_id=%s
           AND credential.credential_id=%s
        """,
        (domain_id, harness_id, credential_id),
    ).fetchone()
    if (
        row is None
        or not isinstance(row["principal_id"], str)
        or row["harness_status"] != "active"
        or row["credential_status"] != "active"
        or int(row["credential_epoch"]) != int(row["epoch"])
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.44 enrolled server identity is not the exact active binding",
        )
    return {
        "domain_id": str(row["domain_id"]),
        "harness_id": str(row["harness_id"]),
        "principal_id": str(row["principal_id"]),
        "credential_id": str(row["credential_id"]),
        "source_harness_kind": str(row["kind"]),
        "harness_kind": "server",
        "profile_key": profile_key,
    }


def _postgres_v0145_source_snapshot(
    connection: Any,
    *,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    catalog_rows = connection.execute(
        "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
    ).fetchall()
    if (
        _postgres_schema_version(connection) != _LIFECYCLE_SOURCE_SCHEMA
        or validate_applied_migrations(catalog_rows, migrations=MIGRATIONS[:6])
        != _LIFECYCLE_SOURCE_SCHEMA
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 requires the exact schema-v6 source",
        )
    require_exact_postgres_catalog(connection, migrations=MIGRATIONS[:6])
    lifecycle_relation = connection.execute(
        "SELECT to_regclass('endpoint_lifecycle') AS relation"
    ).fetchone()
    if lifecycle_relation is None or lifecycle_relation["relation"] is not None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "schema-v6 source contains endpoint lifecycle state",
        )
    identity = _postgres_v0145_identity(
        connection,
        domain_id=domain_id,
        harness_id=harness_id,
        credential_id=credential_id,
        profile_key=profile_key,
    )
    cursor_row = connection.execute(
        "SELECT COALESCE(MAX(cursor),0) AS cursor FROM recipients WHERE recipient_id=%s",
        (identity["harness_id"],),
    ).fetchone()
    committed_scopes = connection.execute(
        "SELECT scope_id,owner_harness_id,fresh_harness_id FROM communication_scopes "
        "WHERE state='committed' ORDER BY domain_id,principal_id,scope_id"
    ).fetchall()
    return {
        "schema_version": _LIFECYCLE_SOURCE_SCHEMA,
        "migration_catalog": _postgres_migration_catalog(connection),
        "endpoint_lifecycle_absent": True,
        "endpoint_mailbox_cursor": (
            int(cursor_row["cursor"]) if cursor_row is not None else 0
        ),
        "identity": identity,
        "migrated_collaboration": _expected_migrated_collaboration(committed_scopes),
        "preserved_relation_digests": {
            relation: _postgres_relation_digest(connection, relation)
            for relation in _LIFECYCLE_PRESERVED_TABLES
        },
    }


def _expected_migrated_collaboration(rows: Any) -> list[dict[str, str]]:
    """Project committed v6 communication authority into its exact v7 image."""

    expectation: list[dict[str, str]] = []
    for row in rows:
        expectation.append(
            {
                "scope_id": str(row["scope_id"]),
                "owner_harness_id": str(row["owner_harness_id"]),
                "member_harness_id": str(row["fresh_harness_id"]),
            }
        )
    expectation.sort(key=lambda entry: entry["scope_id"])
    return expectation


def _require_migrated_collaboration_state(
    *,
    expected: Any,
    scope_rows: Any,
    member_rows: Any,
) -> None:
    """Admit exactly the migrated authority and nothing else.

    An upgraded deployment legitimately carries one v7 collaboration scope per
    committed v6 communication scope, so emptiness is not the invariant.  Any
    extra scope, missing scope, foreign member, or changed role means new
    v0.1.45 activity that rollback would silently discard.
    """

    expectation = sorted(
        (
            str(entry["scope_id"]),
            str(entry["owner_harness_id"]),
            str(entry["member_harness_id"]),
        )
        for entry in expected
    )
    observed_scopes = sorted(
        (
            str(row["scope_id"]),
            str(row["owner_harness_id"]),
            str(row["source_communication_scope_id"]),
            str(row["state"]),
            str(row["state_reason"]),
        )
        for row in scope_rows
    )
    if observed_scopes != [
        (scope_id, owner, scope_id, "active", "migrated_v6_communication_scope")
        for scope_id, owner, _member in expectation
    ]:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 release state changed before rollback",
        )
    expected_members = sorted(
        [(scope_id, owner, "owner") for scope_id, owner, _member in expectation]
        + [(scope_id, member, "member") for scope_id, _owner, member in expectation]
    )
    observed_members = sorted(
        (str(row["scope_id"]), str(row["harness_id"]), str(row["role"]))
        for row in member_rows
    )
    if expected_members != observed_members:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 release state changed before rollback",
        )


def _require_v0145_source_snapshot(
    actual: Mapping[str, Any],
    expected: Mapping[str, Any],
) -> None:
    if dict(actual) != dict(expected):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL source changed after the upgrade journal was committed",
        )


def _postgres_v0145_target_endpoint(
    connection: Any,
    source: Mapping[str, Any],
) -> dict[str, Any]:
    identity = dict(source["identity"])
    endpoint_rows = connection.execute(
        "SELECT * FROM endpoint_lifecycle ORDER BY domain_id,harness_id"
    ).fetchall()
    if len(endpoint_rows) != 1:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target is not exact",
        )
    row = dict(endpoint_rows[0])
    expected = {
        "domain_id": identity["domain_id"],
        "harness_id": identity["harness_id"],
        "principal_id": identity["principal_id"],
        "current_credential_id": identity["credential_id"],
        "harness_kind": identity["harness_kind"],
        "profile_key": identity["profile_key"],
        "state": "restart_required",
        "adapter_generation": 1,
        "mailbox_cursor": source["endpoint_mailbox_cursor"],
        "capability_root_digest": None,
        "process_measurement": None,
        "state_reason": "explicit_user_restart_required",
        "revision": 2,
    }
    if any(row.get(key) != value for key, value in expected.items()):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target changed unexpectedly",
        )
    if (
        not isinstance(row.get("mailbox_cursor"), int)
        or isinstance(row.get("mailbox_cursor"), bool)
        or int(row["mailbox_cursor"]) < 0
        or not isinstance(row.get("created_at"), int)
        or not isinstance(row.get("updated_at"), int)
        or row["updated_at"] < row["created_at"]
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 endpoint lifecycle target metadata is invalid",
        )
    return row


def _postgres_v0145_target_is_rollback_safe(
    connection: Any,
    source: Mapping[str, Any],
) -> dict[str, Any]:
    catalog_rows = connection.execute(
        "SELECT version,name,checksum FROM schema_migrations ORDER BY version"
    ).fetchall()
    if (
        _postgres_schema_version(connection) != _LIFECYCLE_TARGET_SCHEMA
        or validate_applied_migrations(catalog_rows) != _LIFECYCLE_TARGET_SCHEMA
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 PostgreSQL target changed before rollback",
        )
    require_exact_postgres_catalog(connection, migrations=MIGRATIONS)
    source_catalog = list(source["migration_catalog"])
    target_catalog = _postgres_migration_catalog(connection)
    if target_catalog[:6] != source_catalog or len(target_catalog) != 7:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "PostgreSQL migration catalog changed before rollback",
        )
    preserved = dict(source["preserved_relation_digests"])
    if {
        relation: _postgres_relation_digest(connection, relation)
        for relation in _LIFECYCLE_PRESERVED_TABLES
    } != preserved:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "protected identity, access, or message state changed before rollback",
        )
    endpoint = _postgres_v0145_target_endpoint(connection, source)
    expected_migration = source.get("migrated_collaboration")
    if not isinstance(expected_migration, list):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 upgrade journal lacks its exact migrated authority expectation",
        )
    _require_migrated_collaboration_state(
        expected=expected_migration,
        scope_rows=connection.execute(
            "SELECT scope_id,owner_harness_id,source_communication_scope_id,state,"
            "state_reason FROM collaboration_scopes"
        ).fetchall(),
        member_rows=connection.execute(
            "SELECT scope_id,harness_id,role FROM collaboration_scope_members "
            "WHERE state='active'"
        ).fetchall(),
    )
    for relation in _LIFECYCLE_RELEASE_TABLES:
        if relation in {
            "endpoint_lifecycle",
            "collaboration_scopes",
            "collaboration_scope_members",
        }:
            continue
        row = connection.execute(
            f'SELECT COUNT(*) AS count FROM "{relation}"'
        ).fetchone()
        if row is None or int(row["count"]) != 0:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.45 release state changed before rollback",
            )
    return endpoint


def _postgres_v0145_database_operation(
    database_url: str,
    *,
    operation: Literal["snapshot", "migrate", "rollback"],
    source: Mapping[str, Any] | None,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    """Run one exact schema-6/7 transition under the PostgreSQL peer identity."""

    import psycopg
    from psycopg.rows import dict_row

    connection = psycopg.connect(
        database_url,
        autocommit=True,
        row_factory=dict_row,
        connect_timeout=5,
        application_name=f"agentnet:server-setup-{operation}",
    )
    try:
        with connection.transaction():
            connection.execute("SELECT pg_advisory_xact_lock(%s)", (MIGRATION_LOCK_ID,))
            if operation == "snapshot":
                snapshot = _postgres_v0145_source_snapshot(
                    connection,
                    domain_id=domain_id,
                    harness_id=harness_id,
                    credential_id=credential_id,
                    profile_key=profile_key,
                )
                return {"ready": True, "source": snapshot}
            if source is None:
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "v0.1.45 database journal is absent",
                )
            if operation == "migrate":
                if _postgres_schema_version(connection) == _LIFECYCLE_SOURCE_SCHEMA:
                    actual = _postgres_v0145_source_snapshot(
                        connection,
                        domain_id=domain_id,
                        harness_id=harness_id,
                        credential_id=credential_id,
                        profile_key=profile_key,
                    )
                    _require_v0145_source_snapshot(actual, source)
                    apply_postgres_migrations(connection)
                    identity = dict(source["identity"])
                    now = int(time.time())
                    connection.execute(
                        """
                        INSERT INTO endpoint_lifecycle(
                            domain_id,harness_id,principal_id,current_credential_id,
                            harness_kind,profile_key,state,adapter_generation,
                            mailbox_cursor,capability_root_digest,process_measurement,
                            state_reason,revision,created_at,updated_at
                        ) VALUES(%s,%s,%s,%s,%s,%s,'restart_required',1,%s,NULL,NULL,
                                 'explicit_user_restart_required',2,%s,%s)
                        """,
                        (
                            identity["domain_id"],
                            identity["harness_id"],
                            identity["principal_id"],
                            identity["credential_id"],
                            identity["harness_kind"],
                            identity["profile_key"],
                            int(source["endpoint_mailbox_cursor"]),
                            now,
                            now,
                        ),
                    )
                endpoint = _postgres_v0145_target_is_rollback_safe(connection, source)
                return {"ready": True, "endpoint_lifecycle": endpoint}
            if operation == "rollback":
                if _postgres_schema_version(connection) == _LIFECYCLE_SOURCE_SCHEMA:
                    actual = _postgres_v0145_source_snapshot(
                        connection,
                        domain_id=domain_id,
                        harness_id=harness_id,
                        credential_id=credential_id,
                        profile_key=profile_key,
                    )
                    _require_v0145_source_snapshot(actual, source)
                    return {"ready": True, "rolled_back": "already_source"}
                _postgres_v0145_target_is_rollback_safe(connection, source)
                for relation in _LIFECYCLE_RELEASE_TABLES:
                    connection.execute(f'DROP TABLE "{relation}"')
                migration = MIGRATIONS[6]
                deleted = connection.execute(
                    """
                    DELETE FROM schema_migrations
                     WHERE version=%s AND name=%s AND checksum=%s
                    """,
                    (migration.version, migration.name, migration.checksum),
                )
                if deleted.rowcount != 1:
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "v0.1.45 migration catalog changed before rollback",
                    )
                updated = connection.execute(
                    """
                    UPDATE metadata SET value=%s
                     WHERE key='schema_version' AND value=%s
                    """,
                    (str(_LIFECYCLE_SOURCE_SCHEMA), str(_LIFECYCLE_TARGET_SCHEMA)),
                )
                if updated.rowcount != 1:
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "v0.1.45 schema metadata changed before rollback",
                    )
                require_exact_postgres_catalog(connection, migrations=MIGRATIONS[:6])
                return {"ready": True, "rolled_back": "schema_v6_restored"}
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.45 database operation is invalid",
            )
    finally:
        connection.close()


def _run_v0145_database_operation_as(
    account: pwd.struct_passwd,
    database_url: str,
    *,
    operation: Literal["snapshot", "migrate", "rollback"],
    source: Mapping[str, Any] | None,
    domain_id: str,
    harness_id: str,
    credential_id: str,
    profile_key: str,
) -> dict[str, Any]:
    try:
        return _run_postgres_probe_as(
            account,
            lambda: _postgres_v0145_database_operation(
                database_url,
                operation=operation,
                source=source,
                domain_id=domain_id,
                harness_id=harness_id,
                credential_id=credential_id,
                profile_key=profile_key,
            ),
            stage=f"v0145_database_{operation}",
        )
    except ServerSetupError as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            f"v0.1.45 PostgreSQL {operation} could not be proven exact",
        ) from exc


def _postgres_supersession_audit_evidence(
    database_url: str,
    *,
    journal_raw: bytes,
    terminal_raw: bytes,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> dict[str, Any]:
    """Validate one canonical supersession chain through read-only PostgreSQL."""

    import psycopg
    from psycopg.rows import dict_row

    connection = psycopg.connect(
        database_url,
        autocommit=True,
        row_factory=dict_row,
        connect_timeout=5,
        application_name="agentnet:server-setup-supersession-audit",
    )

    class AuditView:
        def fetch_all(
            self,
            query: str,
            parameters: tuple[Any, ...] = (),
        ) -> list[dict[str, object]]:
            return [
                dict(row)
                for row in connection.execute(query, parameters).fetchall()
            ]

        def fetch_one(
            self,
            query: str,
            parameters: tuple[Any, ...] = (),
        ) -> dict[str, object] | None:
            row = connection.execute(query, parameters).fetchone()
            return None if row is None else dict(row)

        def verify_audit_chain(self) -> tuple[bool, int]:
            rows = self.fetch_all(
                "SELECT sequence,occurred_at,record_json,previous_hash,record_hash "
                "FROM audit_log ORDER BY sequence"
            )
            previous_hash = "0" * 64
            for row in rows:
                sequence = row["sequence"]
                occurred_at = row["occurred_at"]
                record_json = row["record_json"]
                stored_previous = row["previous_hash"]
                stored_hash = row["record_hash"]
                if (
                    not isinstance(sequence, int)
                    or not isinstance(occurred_at, int)
                    or not isinstance(record_json, str)
                    or not isinstance(stored_previous, str)
                    or not isinstance(stored_hash, str)
                ):
                    return False, 0
                preimage = (
                    previous_hash.encode("ascii")
                    + b"\x00"
                    + str(occurred_at).encode("ascii")
                    + b"\x00"
                    + record_json.encode("utf-8")
                )
                expected = hashlib.sha256(preimage).hexdigest()
                if stored_previous != previous_hash or stored_hash != expected:
                    return False, sequence
                previous_hash = stored_hash
            return True, len(rows)

    try:
        connection.execute("SET default_transaction_read_only = on")
        journal = load_supersession_journal(
            journal_raw,
            terminal_raw=terminal_raw,
            domain_id=domain_id,
            principal_id=principal_id,
            harness_id=harness_id,
        )
        audited = load_audited_supersession_journal(
            journal_raw,
            AuditView(),
            domain_id=domain_id,
            principal_id=principal_id,
            harness_id=harness_id,
        )
        if audited != journal:
            raise GateBlocked(
                "c0_credential_supersession",
                "audited credential supersession journal changed during validation",
            )
        return {
            "ready": True,
            "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
            "transition_count": len(journal.entries),
            "audit_records_verified": len(journal.entries),
            "credential_id": journal.current_credential[0],
            "credential_epoch": journal.current_credential[1],
        }
    finally:
        connection.close()


def _run_supersession_audit_as(
    account: pwd.struct_passwd,
    database_url: str,
    *,
    journal_raw: bytes,
    terminal_raw: bytes,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> dict[str, Any]:
    try:
        return _run_postgres_probe_as(
            account,
            lambda: _postgres_supersession_audit_evidence(
                database_url,
                journal_raw=journal_raw,
                terminal_raw=terminal_raw,
                domain_id=domain_id,
                principal_id=principal_id,
                harness_id=harness_id,
            ),
            stage="credential_supersession_audit",
        )
    except ServerSetupError as exc:
        raise ServerSetupError(
            "c0_credential_supersession",
            "credential supersession audit could not be proven exact",
        ) from exc


def _postgres_peer_gate(core_account: pwd.struct_passwd, database_url: str) -> dict[str, Any]:
    service = _run_postgres_probe_as(
        core_account,
        lambda: probe_ordinary_server_postgres_connection(database_url),
        stage="postgres_service_identity_canary",
    )
    try:
        postgres_account = pwd.getpwnam("postgres")
    except KeyError as exc:
        raise ServerSetupError(
            "postgres_admin_identity",
            "local PostgreSQL administrator identity is unavailable for read-only auth-rule inspection",
        ) from exc
    if postgres_account.pw_uid == 0 or postgres_account.pw_name != "postgres":
        raise ServerSetupError(
            "postgres_admin_identity",
            "local PostgreSQL administrator identity conflicts with fixed profile",
        )
    auth = _run_postgres_probe_as(
        postgres_account,
        inspect_ordinary_server_postgres_auth,
        stage="postgres_auth_rule_inspection",
    )
    if (
        service.get("current_user") != ORDINARY_SERVER_POSTGRES_USER
        or service.get("current_database") != ORDINARY_SERVER_POSTGRES_DATABASE
        or service.get("transport") != "unix_socket"
        or service.get("writable_primary") is not True
        or auth.get("auth_method") != "peer"
        or auth.get("ident_map") != "none_exact_name_match"
    ):
        raise ServerSetupError(
            "postgres_auth_not_ready",
            "PostgreSQL service identity or exact peer rule does not match the fixed profile",
        )
    return {
        "status": "validated_exact_local_peer",
        "database": ORDINARY_SERVER_POSTGRES_DATABASE,
        "os_user": CORE_USER,
        "role": ORDINARY_SERVER_POSTGRES_USER,
        "socket": ORDINARY_SERVER_POSTGRES_SOCKET,
        "auth_method": "peer",
        "ident_map": "none_exact_name_match",
    }
