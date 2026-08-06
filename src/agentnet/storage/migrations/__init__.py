"""Numbered PostgreSQL migrations with immutable checksums."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass

from agentnet.storage.a2a_schema import A2A_SCHEMA
from agentnet.storage.admin_console_schema import ADMIN_CONSOLE_SCHEMA
from agentnet.storage.communication_scope_schema import COMMUNICATION_SCOPE_TABLE_DDL
from agentnet.storage.artifact_lifecycle_schema import ARTIFACT_LIFECYCLE_SCHEMA
from agentnet.storage.artifact_quota_schema import ARTIFACT_QUOTA_SCHEMA
from agentnet.storage.authority_bootstrap_schema import AUTHORITY_BOOTSTRAP_SCHEMA
from agentnet.storage.bootstrap_plan_schema import BOOTSTRAP_PLAN_SCHEMA
from agentnet.storage.credential_recovery_schema import CREDENTIAL_RECOVERY_SCHEMA
from agentnet.storage.effect_lifecycle_schema import EFFECT_LIFECYCLE_SCHEMA
from agentnet.storage.guided_enrollment_schema import GUIDED_ENROLLMENT_SCHEMA
from agentnet.storage.identity_lifecycle_schema import IDENTITY_LIFECYCLE_SCHEMA
from agentnet.storage.identity_schema import IDENTITY_SCHEMA
from agentnet.storage.ipc_schema import IPC_SCHEMA
from agentnet.storage.operational_control_schema import OPERATIONAL_CONTROL_SCHEMA
from agentnet.storage.post_audit_schema import POST_AUDIT_SCHEMA
from agentnet.storage.relationship_governance_schema import (
    RELATIONSHIP_GOVERNANCE_POSTGRES_MIGRATION,
)
from agentnet.storage.response_obligation_schema import RESPONSE_OBLIGATION_SCHEMA
from agentnet.storage.supervisor_schema import SUPERVISOR_SCHEMA
from agentnet.storage.release_v7_schema import RELEASE_V7_SCHEMA
from agentnet.storage.sqlite import BASE_SCHEMA as SQLITE_BASE_SCHEMA
from agentnet.storage.task_custody_schema import TASK_CUSTODY_SCHEMA
from agentnet.storage.task_payload_release_schema import TASK_PAYLOAD_RELEASE_SCHEMA
from agentnet.storage.versioning_schema import VERSIONING_SCHEMA
from agentnet.storage.workload_schema import WORKLOAD_SCHEMA


@dataclass(frozen=True, slots=True)
class Migration:
    version: int
    name: str
    sql: str

    @property
    def checksum(self) -> str:
        return hashlib.sha256(self.sql.encode("utf-8")).hexdigest()


def _postgres_base_schema() -> str:
    # The canonical table model is shared with the local conformance store;
    # only primitive dialect spellings differ.  DML translation is handled by
    # the PostgreSQL connection adapter, not by migrations.
    value = SQLITE_BASE_SCHEMA.replace(
        "sequence INTEGER PRIMARY KEY AUTOINCREMENT",
        "sequence BIGSERIAL PRIMARY KEY",
    )
    value = value.replace(" INTEGER", " BIGINT")
    # SQLite permits a foreign key to name a table declared later; PostgreSQL
    # requires the referenced table to exist when the statement is executed.
    policy_start = value.index("CREATE TABLE IF NOT EXISTS policy_decisions")
    policy_end = value.index(";", policy_start) + 1
    policy_statement = value[policy_start:policy_end]
    value = value[:policy_start] + value[policy_end:]
    effect_start = value.index("CREATE TABLE IF NOT EXISTS effect_reservations")
    value = value[:effect_start] + policy_statement + "\n" + value[effect_start:]
    return value


_RUNTIME_LEASE_SCHEMA = """
CREATE TABLE IF NOT EXISTS runtime_leases (
    lease_name TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    fence BIGINT NOT NULL CHECK (fence > 0),
    acquired_at BIGINT NOT NULL,
    heartbeat_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_leases_expiry_idx ON runtime_leases(expires_at);
"""

_ARTIFACT_RECOVERY_SCHEMA = """
CREATE TABLE IF NOT EXISTS artifact_recovery_observations (
    instance_id TEXT PRIMARY KEY,
    checked_at BIGINT NOT NULL,
    manifest_count BIGINT NOT NULL,
    verified_count BIGINT NOT NULL,
    missing_count BIGINT NOT NULL,
    corrupt_count BIGINT NOT NULL,
    root_fingerprint TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ready','degraded'))
);
"""


def _postgres_first_release_schema() -> str:
    """Return the complete clean-start PostgreSQL schema in dependency order."""

    fragments = (
        _postgres_base_schema(),
        _RUNTIME_LEASE_SCHEMA,
        _ARTIFACT_RECOVERY_SCHEMA,
        A2A_SCHEMA,
        VERSIONING_SCHEMA,
        TASK_CUSTODY_SCHEMA,
        WORKLOAD_SCHEMA,
        IDENTITY_SCHEMA,
        SUPERVISOR_SCHEMA,
        AUTHORITY_BOOTSTRAP_SCHEMA,
        IPC_SCHEMA,
        EFFECT_LIFECYCLE_SCHEMA,
        CREDENTIAL_RECOVERY_SCHEMA,
        ARTIFACT_LIFECYCLE_SCHEMA,
        ARTIFACT_QUOTA_SCHEMA,
        OPERATIONAL_CONTROL_SCHEMA,
        RELATIONSHIP_GOVERNANCE_POSTGRES_MIGRATION,
        POST_AUDIT_SCHEMA,
        RESPONSE_OBLIGATION_SCHEMA,
    )
    return "\n".join(fragment.replace(" INTEGER", " BIGINT") for fragment in fragments)


MIGRATIONS: tuple[Migration, ...] = (
    Migration(1, "agentnet_first_release_schema", _postgres_first_release_schema()),
    Migration(
        2,
        "protected_task_payload_release",
        TASK_PAYLOAD_RELEASE_SCHEMA.replace(" INTEGER", " BIGINT"),
    ),
    Migration(
        3,
        "guided_oidc_enrollment_continuation",
        GUIDED_ENROLLMENT_SCHEMA.replace(" INTEGER", " BIGINT"),
    ),
    Migration(
        4,
        "bounded_c0_bootstrap_plan",
        BOOTSTRAP_PLAN_SCHEMA.replace(" INTEGER", " BIGINT"),
    ),
    Migration(
        5,
        "identity_begin_idempotency_and_credential_renewal",
        IDENTITY_LIFECYCLE_SCHEMA.replace(" INTEGER", " BIGINT"),
    ),
    Migration(
        6,
        "communication_scope_and_private_administration",
        (COMMUNICATION_SCOPE_TABLE_DDL + ADMIN_CONSOLE_SCHEMA).replace(
            " INTEGER", " BIGINT"
        ),
    ),
    Migration(
        7,
        "communication_collaboration_release",
        RELEASE_V7_SCHEMA.replace(" INTEGER", " BIGINT"),
    ),
)

CURRENT_SCHEMA_VERSION = MIGRATIONS[-1].version


def validate_migration_catalog(migrations: tuple[Migration, ...] = MIGRATIONS) -> None:
    expected = list(range(1, len(migrations) + 1))
    actual = [migration.version for migration in migrations]
    if actual != expected:
        raise ValueError("PostgreSQL migrations must be contiguous and start at version 1")
    if len({migration.name for migration in migrations}) != len(migrations):
        raise ValueError("PostgreSQL migration names must be unique")
    if any(not migration.sql.strip() or len(migration.checksum) != 64 for migration in migrations):
        raise ValueError("PostgreSQL migration is empty or unhashable")


validate_migration_catalog()


__all__ = ["CURRENT_SCHEMA_VERSION", "MIGRATIONS", "Migration", "validate_migration_catalog"]
