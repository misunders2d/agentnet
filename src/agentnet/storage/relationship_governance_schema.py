"""First-release bilateral relationship-governance storage.

AgentNet is a clean-start product: no unilateral relationship table or retrofit
path exists. Every relationship begins as a zero-authority canonical proposal
and can become active only through the exact consent/exception evidence stored
in these relations. Metadata triggers prevent schema-marker rollback.
"""

from __future__ import annotations

import hashlib
import re
import sqlite3
from functools import lru_cache
from typing import Any

from agentnet.errors import GateBlocked


RELATIONSHIP_GOVERNANCE_SCHEMA_VERSION = 1

RELATIONSHIP_GOVERNANCE_BASE_SCHEMA = """
CREATE TABLE IF NOT EXISTS relationship_governance_lineages (
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    administrator_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    subordinate_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    revocation_epoch INTEGER NOT NULL CHECK (revocation_epoch >= 0),
    lifecycle_revision INTEGER NOT NULL CHECK (lifecycle_revision >= 1),
    last_revoked_at INTEGER,
    last_revocation_command_id TEXT UNIQUE,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(domain_id,administrator_harness_id,subordinate_harness_id),
    CHECK (administrator_harness_id <> subordinate_harness_id),
    CHECK (
        (last_revoked_at IS NULL AND last_revocation_command_id IS NULL)
        OR (last_revoked_at IS NOT NULL AND last_revocation_command_id IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS relationship_policy_exceptions (
    policy_exception_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    relationship_transaction_digest TEXT NOT NULL,
    relationship_revision INTEGER NOT NULL CHECK (relationship_revision >= 1),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    administrator_credential_epoch INTEGER NOT NULL CHECK (administrator_credential_epoch >= 1),
    subordinate_credential_epoch INTEGER NOT NULL CHECK (subordinate_credential_epoch >= 1),
    signer_authority_kind TEXT NOT NULL CHECK (signer_authority_kind IN ('human','guest')),
    signer_authority_id TEXT NOT NULL,
    signer_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    signer_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    signer_credential_epoch INTEGER NOT NULL CHECK (signer_credential_epoch >= 1),
    command_id TEXT NOT NULL UNIQUE,
    policy_decision_id TEXT NOT NULL UNIQUE REFERENCES policy_decisions(decision_id),
    command_json TEXT NOT NULL,
    exception_json TEXT NOT NULL,
    exception_digest TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL,
    consumed_at INTEGER,
    revoked_at INTEGER,
    lifecycle_revision INTEGER NOT NULL CHECK (lifecycle_revision >= 1),
    CHECK (expires_at > recorded_at),
    CHECK (consumed_at IS NULL OR (consumed_at >= recorded_at AND consumed_at < expires_at)),
    CHECK (revoked_at IS NULL OR revoked_at >= recorded_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_relationship_policy_exception_transaction
    ON relationship_policy_exceptions(domain_id,relationship_transaction_digest)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_relationship_policy_exception_expiry
    ON relationship_policy_exceptions(domain_id,expires_at,revoked_at,policy_exception_id);

CREATE TABLE IF NOT EXISTS relationship_governance_transactions (
    transaction_id TEXT PRIMARY KEY,
    relationship_id TEXT NOT NULL UNIQUE,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    administrator_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    subordinate_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    administrator_owner_kind TEXT NOT NULL CHECK (administrator_owner_kind IN ('human','guest')),
    administrator_owner_id TEXT NOT NULL,
    subordinate_owner_kind TEXT NOT NULL CHECK (subordinate_owner_kind IN ('human','guest')),
    subordinate_owner_id TEXT NOT NULL,
    may_assign INTEGER NOT NULL CHECK (may_assign IN (0,1)),
    assignment_scope_json TEXT NOT NULL,
    relationship_revision INTEGER NOT NULL CHECK (relationship_revision >= 1),
    relationship_expires_at INTEGER NOT NULL,
    proposal_expires_at INTEGER NOT NULL,
    canonical_transaction_json TEXT NOT NULL,
    transaction_digest TEXT NOT NULL UNIQUE,
    proposal_policy_revision INTEGER NOT NULL CHECK (proposal_policy_revision >= 1),
    proposal_domain_revocation_epoch INTEGER NOT NULL CHECK (proposal_domain_revocation_epoch >= 1),
    proposal_administrator_credential_epoch INTEGER NOT NULL
        CHECK (proposal_administrator_credential_epoch >= 1),
    proposal_subordinate_credential_epoch INTEGER NOT NULL
        CHECK (proposal_subordinate_credential_epoch >= 1),
    proposal_lineage_revocation_epoch INTEGER NOT NULL
        CHECK (proposal_lineage_revocation_epoch >= 0),
    proposer_authority_kind TEXT NOT NULL CHECK (proposer_authority_kind IN ('human','guest')),
    proposer_authority_id TEXT NOT NULL,
    proposer_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    proposer_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    proposer_credential_epoch INTEGER NOT NULL CHECK (proposer_credential_epoch >= 1),
    state TEXT NOT NULL CHECK (state IN (
        'proposed','active','rejected','expired','revoked','superseded'
    )),
    lifecycle_revision INTEGER NOT NULL CHECK (lifecycle_revision >= 1),
    activation_basis TEXT CHECK (activation_basis IN (
        'subordinate_owner_consent','domain_policy_exception'
    )),
    approval_receipt_id TEXT UNIQUE,
    approval_receipt_digest TEXT,
    approval_receipt_json TEXT,
    approval_approver_authority_kind TEXT CHECK (
        approval_approver_authority_kind IN ('human','guest')
    ),
    approval_approver_authority_id TEXT,
    approval_verifier_id TEXT,
    approval_signer_key_id TEXT,
    approval_expires_at INTEGER,
    policy_exception_id TEXT REFERENCES relationship_policy_exceptions(policy_exception_id),
    superseded_by_relationship_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    activated_at INTEGER,
    revoked_at INTEGER,
    CHECK (administrator_harness_id <> subordinate_harness_id),
    FOREIGN KEY(domain_id,administrator_harness_id,subordinate_harness_id)
        REFERENCES relationship_governance_lineages(
            domain_id,administrator_harness_id,subordinate_harness_id
        ),
    CHECK (relationship_expires_at > created_at),
    CHECK (proposal_expires_at > created_at),
    CHECK (proposal_expires_at <= relationship_expires_at),
    CHECK (updated_at >= created_at),
    CHECK (activated_at IS NULL OR activated_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CHECK (
        (activated_at IS NULL
         AND state IN ('proposed','rejected','expired','revoked')
         AND activation_basis IS NULL
         AND approval_receipt_id IS NULL
         AND approval_receipt_digest IS NULL
         AND approval_receipt_json IS NULL
         AND approval_approver_authority_kind IS NULL
         AND approval_approver_authority_id IS NULL
         AND approval_verifier_id IS NULL
         AND approval_signer_key_id IS NULL
         AND approval_expires_at IS NULL
         AND policy_exception_id IS NULL)
        OR
        (activated_at IS NOT NULL
         AND state IN ('active','expired','revoked','superseded')
         AND activation_basis = 'subordinate_owner_consent'
         AND approval_receipt_id IS NOT NULL
         AND approval_receipt_digest IS NOT NULL
         AND approval_receipt_json IS NOT NULL
         AND approval_approver_authority_kind IS NOT NULL
         AND approval_approver_authority_id IS NOT NULL
         AND approval_verifier_id IS NOT NULL
         AND approval_signer_key_id IS NOT NULL
         AND approval_expires_at IS NOT NULL
         AND approval_expires_at > activated_at
         AND policy_exception_id IS NULL)
        OR
        (activated_at IS NOT NULL
         AND state IN ('active','expired','revoked','superseded')
         AND activation_basis = 'domain_policy_exception'
         AND approval_receipt_id IS NULL
         AND approval_receipt_digest IS NULL
         AND approval_receipt_json IS NULL
         AND approval_approver_authority_kind IS NULL
         AND approval_approver_authority_id IS NULL
         AND approval_verifier_id IS NULL
         AND approval_signer_key_id IS NULL
         AND approval_expires_at IS NULL
         AND policy_exception_id IS NOT NULL)
    ),
    CHECK (
        (state = 'superseded' AND superseded_by_relationship_id IS NOT NULL)
        OR (state <> 'superseded' AND superseded_by_relationship_id IS NULL)
    ),
    CHECK (
        (state IN ('revoked','superseded') AND revoked_at IS NOT NULL)
        OR (state NOT IN ('revoked','superseded'))
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_relationship_governance_open_revision
    ON relationship_governance_transactions(
        domain_id,administrator_harness_id,subordinate_harness_id,relationship_revision
    ) WHERE state IN ('proposed','active');
CREATE UNIQUE INDEX IF NOT EXISTS idx_relationship_governance_active_pair
    ON relationship_governance_transactions(
        domain_id,administrator_harness_id,subordinate_harness_id
    ) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS idx_relationship_governance_pending_owner
    ON relationship_governance_transactions(
        domain_id,subordinate_owner_kind,subordinate_owner_id,state,
        proposal_expires_at,created_at,transaction_id
    );
CREATE INDEX IF NOT EXISTS idx_relationship_governance_expiry
    ON relationship_governance_transactions(
        domain_id,state,relationship_expires_at,relationship_id
    );

"""

RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA = (
    RELATIONSHIP_GOVERNANCE_BASE_SCHEMA
    + """
CREATE TRIGGER IF NOT EXISTS trg_relationship_governance_schema_floor_update
BEFORE UPDATE OF value ON metadata
WHEN OLD.key='schema_version' AND (
    CAST(NEW.value AS INTEGER) < 1
    OR CAST(NEW.value AS INTEGER) < CAST(OLD.value AS INTEGER)
)
BEGIN
    SELECT RAISE(ABORT, 'AgentNet relationship governance schema cannot be downgraded');
END;
CREATE TRIGGER IF NOT EXISTS trg_relationship_governance_schema_floor_insert
BEFORE INSERT ON metadata
WHEN NEW.key='schema_version' AND CAST(NEW.value AS INTEGER) < 1
BEGIN
    SELECT RAISE(ABORT, 'AgentNet relationship governance schema cannot be downgraded');
END;
"""
)
RELATIONSHIP_GOVERNANCE_SQLITE_MIGRATION = (
    "BEGIN IMMEDIATE;\n" + RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA + "COMMIT;\n"
)

RELATIONSHIP_GOVERNANCE_POSTGRES_MIGRATION = (
    RELATIONSHIP_GOVERNANCE_BASE_SCHEMA.replace(" INTEGER", " BIGINT")
)

RELATIONSHIP_GOVERNANCE_REQUIRED_TABLES = frozenset(
    {
        "relationship_governance_transactions",
        "relationship_governance_lineages",
        "relationship_policy_exceptions",
    }
)
RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES = frozenset(
    {
        "idx_relationship_governance_active_pair",
        "idx_relationship_governance_expiry",
        "idx_relationship_governance_open_revision",
        "idx_relationship_governance_pending_owner",
        "idx_relationship_policy_exception_expiry",
        "idx_relationship_policy_exception_transaction",
    }
)
RELATIONSHIP_GOVERNANCE_REQUIRED_SQLITE_TRIGGERS = frozenset(
    {
        "trg_relationship_governance_schema_floor_insert",
        "trg_relationship_governance_schema_floor_update",
    }
)
RELATIONSHIP_GOVERNANCE_REQUIRED_POSTGRES_CONSTRAINTS: frozenset[str] = frozenset()

_POSTGRES_INDEX_SHAPES: dict[str, tuple[str, bool, str, str]] = {
    "idx_relationship_governance_open_revision": (
        "relationship_governance_transactions",
        True,
        "domain_id,administrator_harness_id,subordinate_harness_id,relationship_revision",
        "state = any(array['proposed','active'])",
    ),
    "idx_relationship_governance_active_pair": (
        "relationship_governance_transactions",
        True,
        "domain_id,administrator_harness_id,subordinate_harness_id",
        "state = 'active'",
    ),
    "idx_relationship_governance_pending_owner": (
        "relationship_governance_transactions",
        False,
        "domain_id,subordinate_owner_kind,subordinate_owner_id,state,proposal_expires_at,created_at,transaction_id",
        "",
    ),
    "idx_relationship_governance_expiry": (
        "relationship_governance_transactions",
        False,
        "domain_id,state,relationship_expires_at,relationship_id",
        "",
    ),
    "idx_relationship_policy_exception_transaction": (
        "relationship_policy_exceptions",
        True,
        "domain_id,relationship_transaction_digest",
        "revoked_at is null",
    ),
    "idx_relationship_policy_exception_expiry": (
        "relationship_policy_exceptions",
        False,
        "domain_id,expires_at,revoked_at,policy_exception_id",
        "",
    ),
}

_POSTGRES_LINEAGE_COLUMNS: dict[str, tuple[str, bool]] = {
    "domain_id": ("text", True),
    "administrator_harness_id": ("text", True),
    "subordinate_harness_id": ("text", True),
    "revocation_epoch": ("bigint", True),
    "lifecycle_revision": ("bigint", True),
    "last_revoked_at": ("bigint", False),
    "last_revocation_command_id": ("text", False),
    "updated_at": ("bigint", True),
}

# Fingerprints cover every live column (type and nullability) plus every
# CHECK/FK/PK/UNIQUE semantic definition. PostgreSQL 18 exposes NOT NULL as
# separate ``contype='n'`` rows; those are excluded because ``attnotnull`` is
# already covered and earlier supported PostgreSQL versions do not expose them.
_POSTGRES_TABLE_FINGERPRINTS = {
    "relationship_governance_lineages": (
        "32f2ee3fcd4300ec22b9af6f7f555aae0ed8a4ef0810364acdad2fbf0e2546c6"
    ),
    "relationship_policy_exceptions": (
        "c44b6383d41d67ba1b74e372ed54b007e981d8a3729e766e5c0040c00dbe24c7"
    ),
    "relationship_governance_transactions": (
        "657ded996a318ab068c91cd5f65097f89981d24bc865ad074eaaaad903f8623e"
    ),
}


def _normalize_sql(value: str | None) -> str:
    return re.sub(r"\s+", " ", value or "").strip().lower()


def _normalize_postgres_expression(value: str | None) -> str:
    normalized = _normalize_sql(value).replace("::text", "")
    normalized = re.sub(r"\s*([(),\[\]])\s*", r"\1", normalized)
    while normalized.startswith("(") and normalized.endswith(")"):
        depth = 0
        encloses_all = True
        for index, character in enumerate(normalized):
            if character == "(":
                depth += 1
            elif character == ")":
                depth -= 1
                if depth == 0 and index != len(normalized) - 1:
                    encloses_all = False
                    break
        if not encloses_all or depth != 0:
            break
        normalized = normalized[1:-1].strip()
    return normalized


def _postgres_table_fingerprint(store: Any, table_name: str) -> str:
    columns = store.fetch_all(
        """
        SELECT attribute.attname,
               format_type(attribute.atttypid,attribute.atttypmod) AS type_name,
               attribute.attnotnull
          FROM pg_attribute attribute
         WHERE attribute.attrelid=to_regclass(?)
           AND attribute.attnum>0 AND NOT attribute.attisdropped
        """,
        (table_name,),
    )
    constraints = store.fetch_all(
        """
        SELECT constraint_definition.contype,
               constraint_definition.convalidated,
               pg_get_constraintdef(constraint_definition.oid,TRUE) AS definition
          FROM pg_constraint constraint_definition
         WHERE constraint_definition.conrelid=to_regclass(?)
           AND constraint_definition.contype<>'n'
        """,
        (table_name,),
    )
    lines = [
        "column|"
        + "|".join(
            (
                str(row["attname"]),
                str(row["type_name"]),
                "1" if bool(row["attnotnull"]) else "0",
            )
        )
        for row in columns
    ]
    lines.extend(
        "constraint|"
        + "|".join(
            (
                str(row["contype"]),
                "1" if bool(row["convalidated"]) else "0",
                _normalize_postgres_expression(row["definition"]),
            )
        )
        for row in constraints
    )
    return hashlib.sha256("\n".join(sorted(lines)).encode("utf-8")).hexdigest()


@lru_cache(maxsize=1)
def _expected_sqlite_object_sql() -> dict[str, str]:
    """Build exact trusted object definitions from the canonical migration."""

    connection = sqlite3.connect(":memory:", isolation_level=None)
    try:
        connection.executescript(
            """
            CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
            """
            + RELATIONSHIP_GOVERNANCE_SQLITE_MIGRATION
        )
        required = (
            RELATIONSHIP_GOVERNANCE_REQUIRED_TABLES
            | RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES
            | RELATIONSHIP_GOVERNANCE_REQUIRED_SQLITE_TRIGGERS
        )
        placeholders = ",".join("?" for _ in required)
        rows = connection.execute(
            f"SELECT name,sql FROM sqlite_master WHERE name IN ({placeholders})",
            tuple(sorted(required)),
        ).fetchall()
        definitions = {str(name): _normalize_sql(sql) for name, sql in rows}
        if set(definitions) != required or any(not value for value in definitions.values()):
            raise RuntimeError("canonical relationship-governance SQLite schema is incomplete")
        return definitions
    finally:
        connection.close()


def require_relationship_governance_schema(store: Any) -> None:
    """Fail closed unless the bilateral governance migration is exact."""

    try:
        from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION

        backend = getattr(store, "backend_name", "")
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if (
            CURRENT_SCHEMA_VERSION < RELATIONSHIP_GOVERNANCE_SCHEMA_VERSION
            or metadata is None
            or int(metadata["value"]) != CURRENT_SCHEMA_VERSION
        ):
            raise GateBlocked(
                "relationship_governance_schema",
                "bilateral relationship-governance schema is not current",
            )
        if backend == "sqlite":
            expected_definitions = _expected_sqlite_object_sql()
            required_objects = tuple(sorted(expected_definitions))
            placeholders = ",".join("?" for _ in required_objects)
            actual_rows = store.fetch_all(
                f"SELECT name,sql FROM sqlite_master WHERE name IN ({placeholders})",
                required_objects,
            )
            actual_definitions = {
                str(row["name"]): _normalize_sql(row["sql"]) for row in actual_rows
            }
            missing_tables = {
                name
                for name in RELATIONSHIP_GOVERNANCE_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
                )
                is None
            }
            missing_indexes = {
                name
                for name in RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?", (name,)
                )
                is None
            }
            missing_triggers = {
                name
                for name in RELATIONSHIP_GOVERNANCE_REQUIRED_SQLITE_TRIGGERS
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='trigger' AND name=?", (name,)
                )
                is None
            }
            invalid_objects = {
                name
                for name, expected in expected_definitions.items()
                if actual_definitions.get(name) != expected
            }
        elif backend == "postgresql":
            from agentnet.storage.migrations import MIGRATIONS

            migration = store.fetch_one(
                "SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations"
            )
            if migration is None or int(migration["version"]) != CURRENT_SCHEMA_VERSION:
                raise GateBlocked(
                    "relationship_governance_schema",
                    "bilateral relationship-governance PostgreSQL migration is not current",
                )
            expected_migration = MIGRATIONS[-1]
            applied = store.fetch_one(
                "SELECT name,checksum FROM schema_migrations WHERE version=?",
                (CURRENT_SCHEMA_VERSION,),
            )
            if (
                applied is None
                or applied["name"] != expected_migration.name
                or applied["checksum"] != expected_migration.checksum
            ):
                raise GateBlocked(
                    "relationship_governance_schema",
                    "bilateral relationship-governance migration checksum is invalid",
                )
            table_catalog = {
                name: store.fetch_one(
                    """
                    SELECT relation.oid AS relation,relation.relkind
                      FROM pg_class relation
                     WHERE relation.oid=to_regclass(?)
                    """,
                    (name,),
                )
                for name in RELATIONSHIP_GOVERNANCE_REQUIRED_TABLES
            }
            missing_tables = {
                name for name, row in table_catalog.items() if row is None
            }
            invalid_objects: set[str] = {
                name
                for name, row in table_catalog.items()
                if row is not None and row["relkind"] != "r"
            }
            for name, expected_fingerprint in _POSTGRES_TABLE_FINGERPRINTS.items():
                if table_catalog.get(name) is not None and (
                    _postgres_table_fingerprint(store, name) != expected_fingerprint
                ):
                    invalid_objects.add(name)
            index_catalog = {
                name: store.fetch_one(
                    """
                    SELECT table_relation.relname AS table_name,
                           index_relation.relname AS index_name,
                           index_definition.indisunique,
                           index_definition.indisvalid,
                           index_definition.indisready,
                           pg_get_expr(
                               index_definition.indpred,index_definition.indrelid
                           ) AS predicate,
                           (
                               SELECT string_agg(attribute.attname,',' ORDER BY key_part.ordinality)
                                 FROM unnest(index_definition.indkey) WITH ORDINALITY
                                      AS key_part(attnum,ordinality)
                                 JOIN pg_attribute attribute
                                   ON attribute.attrelid=index_definition.indrelid
                                  AND attribute.attnum=key_part.attnum
                                WHERE key_part.attnum>0
                           ) AS columns
                      FROM pg_index index_definition
                      JOIN pg_class index_relation
                        ON index_relation.oid=index_definition.indexrelid
                      JOIN pg_class table_relation
                        ON table_relation.oid=index_definition.indrelid
                     WHERE index_definition.indexrelid=to_regclass(?)
                    """,
                    (name,),
                )
                for name in RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES
            }
            missing_indexes = {
                name for name, row in index_catalog.items() if row is None
            }
            for name, expected in _POSTGRES_INDEX_SHAPES.items():
                row = index_catalog.get(name)
                if row is None:
                    continue
                expected_table, expected_unique, expected_columns, expected_predicate = expected
                if (
                    row["table_name"] != expected_table
                    or bool(row["indisunique"]) is not expected_unique
                    or not bool(row["indisvalid"])
                    or not bool(row["indisready"])
                    or row["columns"] != expected_columns
                    or _normalize_postgres_expression(row["predicate"])
                    != expected_predicate
                ):
                    invalid_objects.add(name)

            lineage_columns = store.fetch_all(
                """
                SELECT attribute.attname,
                       format_type(attribute.atttypid,attribute.atttypmod) AS type_name,
                       attribute.attnotnull
                  FROM pg_attribute attribute
                 WHERE attribute.attrelid=to_regclass('relationship_governance_lineages')
                   AND attribute.attnum>0 AND NOT attribute.attisdropped
                """
            )
            actual_lineage_columns = {
                row["attname"]: (row["type_name"], bool(row["attnotnull"]))
                for row in lineage_columns
            }
            if actual_lineage_columns != _POSTGRES_LINEAGE_COLUMNS:
                invalid_objects.add("relationship_governance_lineages")
            lineage_epoch_column = store.fetch_one(
                """
                SELECT format_type(attribute.atttypid,attribute.atttypmod) AS type_name,
                       attribute.attnotnull
                  FROM pg_attribute attribute
                 WHERE attribute.attrelid=to_regclass('relationship_governance_transactions')
                   AND attribute.attname='proposal_lineage_revocation_epoch'
                   AND attribute.attnum>0 AND NOT attribute.attisdropped
                """
            )
            if (
                lineage_epoch_column is None
                or lineage_epoch_column["type_name"] != "bigint"
                or not bool(lineage_epoch_column["attnotnull"])
            ):
                invalid_objects.add("proposal_lineage_revocation_epoch")
            lineage_primary_key = store.fetch_one(
                """
                SELECT constraint_definition.convalidated,
                       pg_get_constraintdef(constraint_definition.oid,TRUE) AS definition
                  FROM pg_constraint constraint_definition
                 WHERE constraint_definition.conrelid=
                       to_regclass('relationship_governance_lineages')
                   AND constraint_definition.contype='p'
                """
            )
            if (
                lineage_primary_key is None
                or not bool(lineage_primary_key["convalidated"])
                or _normalize_postgres_expression(lineage_primary_key["definition"])
                != "primary key(domain_id,administrator_harness_id,subordinate_harness_id)"
            ):
                invalid_objects.add("relationship_governance_lineages_primary_key")
            missing_triggers: set[str] = set()
            missing_constraints: set[str] = set()
        else:
            raise GateBlocked(
                "relationship_governance_schema",
                "bilateral relationship-governance backend is unsupported",
            )
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked(
            "relationship_governance_schema",
            "bilateral relationship-governance schema could not be verified",
        ) from exc
    if backend == "sqlite":
        missing_constraints: set[str] = set()
    if (
        missing_tables
        or missing_indexes
        or missing_triggers
        or missing_constraints
        or invalid_objects
    ):
        raise GateBlocked(
            "relationship_governance_schema",
            "bilateral relationship-governance relations are missing or altered",
        )


__all__ = [
    "RELATIONSHIP_GOVERNANCE_BASE_SCHEMA",
    "RELATIONSHIP_GOVERNANCE_POSTGRES_MIGRATION",
    "RELATIONSHIP_GOVERNANCE_REQUIRED_POSTGRES_CONSTRAINTS",
    "RELATIONSHIP_GOVERNANCE_REQUIRED_INDEXES",
    "RELATIONSHIP_GOVERNANCE_REQUIRED_SQLITE_TRIGGERS",
    "RELATIONSHIP_GOVERNANCE_REQUIRED_TABLES",
    "RELATIONSHIP_GOVERNANCE_SCHEMA_VERSION",
    "RELATIONSHIP_GOVERNANCE_SQLITE_MIGRATION",
    "RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA",
    "require_relationship_governance_schema",
]
