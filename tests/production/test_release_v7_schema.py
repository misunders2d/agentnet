from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from agentnet.authorization.communication_scope import COMMUNICATION_SCOPE_ACTIONS
from agentnet.authorization.communication_scope_service import CollaborationScopeService
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.errors import GateBlocked
from agentnet.operations import server_setup
from agentnet.operations.server_setup import ServerSetupError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.relationship_governance_schema import (
    RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA,
)
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS
from agentnet.storage.sqlite import SQLiteStore


_REQUIRED_RELATIONS = {
    "endpoint_lifecycle",
    "collaboration_scopes",
    "collaboration_scope_members",
    "artifact_transfers",
    "artifact_transfer_recipients",
    "invitation_link_failures",
    "invitation_links",
}


def _relation_names(store: SQLiteStore) -> set[str]:
    return {
        str(row["name"])
        for row in store.fetch_all(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        )
    }


def _assert_collaboration_member_authority_schema(store: SQLiteStore) -> None:
    columns = {
        str(row["name"])
        for row in store.fetch_all("PRAGMA table_info(collaboration_scope_members)")
    }
    assert {"authority_kind", "authority_id", "harness_id"} <= columns
    assert "principal_id" not in columns
    foreign_tables = {
        str(row["table"])
        for row in store.fetch_all(
            "PRAGMA foreign_key_list(collaboration_scope_members)"
        )
    }
    assert foreign_tables == {"collaboration_scopes", "harnesses"}


def _insert_identity(store: SQLiteStore) -> None:
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,?,?)",
            ("release-v7.example", "active", 1_000),
        )
        connection.execute(
            "INSERT INTO principals(principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at) "
            "VALUES(?,?,?,?,?,?,?)",
            (
                "principal-release-v7",
                "release-v7.example",
                "https://idp.example",
                "subject-release-v7",
                "owner@release-v7.example",
                "active",
                1_000,
            ),
        )
        connection.execute(
            "INSERT INTO harnesses(harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,capabilities_json,created_at) "
            "VALUES(?,?,?,?,?,?,?,?,?)",
            (
                "harness-release-v7",
                "release-v7.example",
                "principal-release-v7",
                "omp",
                "Owner OMP",
                "active",
                "os_bound",
                "[]",
                1_000,
            ),
        )
        connection.execute(
            "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) "
            "VALUES(?,?,?,?,?,?,?,?)",
            (
                "credential-release-v7",
                "harness-release-v7",
                "key-release-v7",
                "synthetic-public-key",
                "active",
                1,
                900,
                9_000,
            ),
        )


def _downgrade_fresh_store_to_exact_v6(path: Path, *, key: bytes) -> None:
    store = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        with store.transaction() as connection:
            connection.execute(
                "INSERT INTO metadata(key,value) VALUES('preserved-v6-sentinel','present')"
            )
    finally:
        store.close()

    connection = sqlite3.connect(path)
    try:
        for relation in (
            "artifact_transfer_recipients",
            "invitation_link_failures",
            "collaboration_scope_members",
            "invitation_links",
            "artifact_transfers",
            "collaboration_scopes",
            "endpoint_lifecycle",
        ):
            connection.execute(f"DROP TABLE IF EXISTS {relation}")
        connection.execute("DROP TRIGGER trg_relationship_governance_schema_floor_update")
        connection.execute("DROP TRIGGER trg_relationship_governance_schema_floor_insert")
        connection.execute("DELETE FROM installed_migration_catalog WHERE version=7")
        connection.execute("UPDATE metadata SET value='6' WHERE key='schema_version'")
        connection.executescript(RELATIONSHIP_GOVERNANCE_SQLITE_SCHEMA)
        connection.commit()
    finally:
        connection.close()


def _seed_committed_v6_communication_scope(path: Path) -> None:
    connection = sqlite3.connect(path)
    try:
        connection.execute(
            "INSERT INTO harnesses(harness_id,domain_id,principal_id,kind,display_name,status,"
            "binding_assurance,capabilities_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)",
            (
                "harness-release-v7-fresh",
                "release-v7.example",
                "principal-release-v7",
                "pi",
                "Fresh Pi",
                "active",
                "os_bound",
                "[]",
                1_001,
            ),
        )
        connection.execute(
            "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,"
            "not_before,expires_at) VALUES(?,?,?,?,?,?,?,?)",
            (
                "credential-release-v7-fresh",
                "harness-release-v7-fresh",
                "key-release-v7-fresh",
                "synthetic-public-key",
                "active",
                1,
                900,
                9_000,
            ),
        )
        digest = "a" * 64
        connection.execute(
            """INSERT INTO communication_scopes(
                scope_id,profile,profile_version,domain_id,principal_id,owner_harness_id,
                fresh_harness_id,owner_credential_id,fresh_credential_id,
                owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                policy_revision,actor_binding_json,canonical_scope_preimage_json,
                final_approval_transaction_json,scope_digest,transaction_digest,
                begin_idempotency_key_sha256,state,created_at,approval_expires_at,
                approval_create_idempotency_key,approval_create_request_digest,
                approval_request_id,approval_issued_at,completion_reserved_at,
                completion_idempotency_key_sha256,completion_request_digest,
                approval_receipt_id,approval_receipt_digest,committed_at,
                committed_result_encrypted,committed_result_digest,audit_record_hash
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "scope-release-v6",
                "same-principal-full-communication:v1",
                1,
                "release-v7.example",
                "principal-release-v7",
                "harness-release-v7",
                "harness-release-v7-fresh",
                "credential-release-v7",
                "credential-release-v7-fresh",
                1,
                1,
                1,
                1,
                "{}",
                "{}",
                "{}",
                "b" * 64,
                "c" * 64,
                "d" * 64,
                "committed",
                1_100,
                2_000,
                "approval-create",
                "e" * 64,
                "approval-request",
                1_200,
                1_300,
                "f" * 64,
                "1" * 64,
                "approval-receipt",
                "2" * 64,
                1_400,
                "encrypted-result",
                "3" * 64,
                "4" * 64,
            ),
        )
        ordinal = 0
        for harness_id in ("harness-release-v7", "harness-release-v7-fresh"):
            for action in sorted(COMMUNICATION_SCOPE_ACTIONS):
                ordinal += 1
                entitlement_id = f"entitlement-release-v6-{ordinal}"
                connection.execute(
                    "INSERT INTO entitlements(entitlement_id,domain_id,principal_id,action,"
                    "resource_pattern,expires_at,revoked_at,revision) VALUES(?,?,?,?,?,NULL,NULL,?)",
                    (
                        entitlement_id,
                        "release-v7.example",
                        "principal-release-v7",
                        action,
                        "*",
                        1,
                    ),
                )
                connection.execute(
                    """INSERT INTO communication_scope_items(
                        scope_id,item_ordinal,item_id,entitlement_id,harness_id,action,
                        resource_pattern,item_json,expires_at
                    ) VALUES(?,?,?,?,?,?,?,'{}',NULL)""",
                    (
                        "scope-release-v6",
                        ordinal,
                        f"item-release-v6-{ordinal}",
                        entitlement_id,
                        harness_id,
                        action,
                        "*",
                    ),
                )
        connection.commit()
    finally:
        connection.close()


def test_sqlite_v6_upgrade_preserves_committed_communication_authority(
    tmp_path: Path,
) -> None:
    path = tmp_path / "release-v6-with-scope.sqlite3"
    key = b"m" * 32
    initial = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        _insert_identity(initial)
    finally:
        initial.close()
    _downgrade_fresh_store_to_exact_v6(path, key=key)
    _seed_committed_v6_communication_scope(path)

    upgraded = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        service = CollaborationScopeService(upgraded, clock=lambda: 1_500)
        scope = service.get_for_actor(
            actor=VerifiedActor(
                kind=ActorKind.VERIFIED_HUMAN_HARNESS,
                domain_id="release-v7.example",
                principal_id="principal-release-v7",
                harness_id="harness-release-v7",
                credential_id="credential-release-v7",
                credential_epoch=1,
                binding_assurance="os_bound",
            ),
            scope_id="scope-release-v6",
        )
        migrated_row = upgraded.fetch_one(
            "SELECT source_communication_scope_id FROM collaboration_scopes WHERE scope_id=?",
            ("scope-release-v6",),
        )
        assert migrated_row is not None
        assert migrated_row["source_communication_scope_id"] == "scope-release-v6"
        assert scope.scope_kind == "direct"
        assert scope.member_harness_ids == (
            "harness-release-v7",
            "harness-release-v7-fresh",
        )
        assert "message.send" in scope.allowed_actions
        assert "room.send" in scope.allowed_actions
        assert scope.allowed_classifications == ("C1",)
    finally:
        upgraded.close()

def test_sqlite_v6_upgrade_rejects_incomplete_communication_authority(
    tmp_path: Path,
) -> None:
    path = tmp_path / "release-v6-incomplete-scope.sqlite3"
    key = b"n" * 32
    initial = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        _insert_identity(initial)
    finally:
        initial.close()
    _downgrade_fresh_store_to_exact_v6(path, key=key)
    _seed_committed_v6_communication_scope(path)
    connection = sqlite3.connect(path)
    try:
        connection.execute(
            "DELETE FROM communication_scope_items WHERE scope_id=? AND item_ordinal=?",
            ("scope-release-v6", 1),
        )
        connection.commit()
    finally:
        connection.close()

    with pytest.raises(
        GateBlocked,
        match="v6 communication scope authority items are incomplete or not current",
    ):
        SQLiteStore(path, LocalEnvelopeCipher(key))
    connection = sqlite3.connect(path)
    try:
        assert connection.execute(
            "SELECT value FROM metadata WHERE key='schema_version'"
        ).fetchone() == ("6",)
    finally:
        connection.close()





def test_release_v7_is_one_contiguous_immutable_migration() -> None:
    assert CURRENT_SCHEMA_VERSION == 7
    assert MIGRATIONS[-1].version == 7
    assert MIGRATIONS[-1].name == "communication_collaboration_release"
    assert " INTEGER" not in MIGRATIONS[-1].sql
    for relation in _REQUIRED_RELATIONS:
        assert f"CREATE TABLE IF NOT EXISTS {relation}" in MIGRATIONS[-1].sql
    member_declaration = MIGRATIONS[-1].sql.split(
        "CREATE TABLE IF NOT EXISTS collaboration_scope_members",
        1,
    )[1].split(";", 1)[0]
    assert "authority_kind TEXT NOT NULL CHECK (authority_kind IN ('principal','guest'))" in member_declaration
    assert "authority_id TEXT NOT NULL" in member_declaration
    assert "principal_id" not in member_declaration
    assert "REFERENCES principals" not in member_declaration


def test_fresh_sqlite_v7_uses_kind_aware_collaboration_members(tmp_path: Path) -> None:
    store = SQLiteStore(
        tmp_path / "fresh-release-v7.sqlite3",
        LocalEnvelopeCipher(b"f" * 32),
    )
    try:
        _assert_collaboration_member_authority_schema(store)
    finally:
        store.close()


def test_sqlite_v6_upgrades_to_v7_without_losing_existing_rows(tmp_path: Path) -> None:
    path = tmp_path / "release-v6.sqlite3"
    key = b"7" * 32
    _downgrade_fresh_store_to_exact_v6(path, key=key)

    upgraded = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        assert upgraded.fetch_one(
            "SELECT value FROM metadata WHERE key='schema_version'"
        )["value"] == "7"
        assert upgraded.fetch_one(
            "SELECT value FROM metadata WHERE key='preserved-v6-sentinel'"
        )["value"] == "present"
        assert _REQUIRED_RELATIONS <= _relation_names(upgraded)
        _assert_collaboration_member_authority_schema(upgraded)
        assert [tuple(row) for row in upgraded.fetch_all(
            "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
        )] == [
            (migration.version, migration.name, migration.checksum)
            for migration in MIGRATIONS
        ]
    finally:
        upgraded.close()


def test_endpoint_lifecycle_rejects_duplicate_profile_binding(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "release-v7.sqlite3", LocalEnvelopeCipher(b"8" * 32))
    try:
        _insert_identity(store)
        with store.transaction() as connection:
            connection.execute(
                "INSERT INTO harnesses(harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,capabilities_json,created_at) "
                "VALUES(?,?,?,?,?,?,?,?,?)",
                (
                    "harness-release-v7-duplicate",
                    "release-v7.example",
                    "principal-release-v7",
                    "omp",
                    "Second OMP",
                    "active",
                    "verified",
                    "[]",
                    1_000,
                ),
            )
            connection.execute(
                "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) "
                "VALUES(?,?,?,?,?,?,?,?)",
                (
                    "credential-release-v7-duplicate",
                    "harness-release-v7-duplicate",
                    "key-release-v7-duplicate",
                    "synthetic-public-key",
                    "active",
                    1,
                    900,
                    9_000,
                ),
            )
        values = (
            "release-v7.example",
            "harness-release-v7",
            "principal-release-v7",
            "credential-release-v7",
            "omp",
            "omp:default",
            "enrolled",
            1,
            0,
            "enrolled",
            1,
            1_000,
            1_000,
        )
        with store.transaction() as connection:
            connection.execute(
                "INSERT INTO endpoint_lifecycle(domain_id,harness_id,principal_id,current_credential_id,harness_kind,profile_key,state,adapter_generation,mailbox_cursor,state_reason,revision,created_at,updated_at) "
                "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
                values,
            )
        with pytest.raises(sqlite3.IntegrityError):
            with store.transaction() as connection:
                connection.execute(
                    "INSERT INTO endpoint_lifecycle(domain_id,harness_id,principal_id,current_credential_id,harness_kind,profile_key,state,adapter_generation,mailbox_cursor,state_reason,revision,created_at,updated_at) "
                    "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (
                        "release-v7.example",
                        "harness-release-v7-duplicate",
                        "principal-release-v7",
                        "credential-release-v7-duplicate",
                        "omp",
                        "omp:default",
                        "enrolled",
                        1,
                        0,
                        "enrolled",
                        1,
                        1_000,
                        1_000,
                    ),
                )
    finally:
        store.close()



def test_upgraded_server_admits_exactly_its_migrated_communication_authority() -> None:
    """A deployment holding v6 authority must still be rollback-safe after upgrade."""

    expected = [
        {
            "scope_id": "scope-release-v6",
            "owner_harness_id": "harness-release-v7",
            "member_harness_id": "harness-release-v7-fresh",
        }
    ]
    migrated_scope = {
        "scope_id": "scope-release-v6",
        "owner_harness_id": "harness-release-v7",
        "source_communication_scope_id": "scope-release-v6",
        "state": "active",
        "state_reason": "migrated_v6_communication_scope",
    }
    migrated_members = [
        {"scope_id": "scope-release-v6", "harness_id": "harness-release-v7", "role": "owner"},
        {
            "scope_id": "scope-release-v6",
            "harness_id": "harness-release-v7-fresh",
            "role": "member",
        },
    ]

    server_setup._require_migrated_collaboration_state(
        expected=expected,
        scope_rows=[migrated_scope],
        member_rows=migrated_members,
    )

    with pytest.raises(ServerSetupError, match="release state changed before rollback"):
        server_setup._require_migrated_collaboration_state(
            expected=expected,
            scope_rows=[],
            member_rows=[],
        )

    with pytest.raises(ServerSetupError, match="release state changed before rollback"):
        server_setup._require_migrated_collaboration_state(
            expected=expected,
            scope_rows=[
                migrated_scope,
                {
                    "scope_id": "scope-issued-after-upgrade",
                    "owner_harness_id": "harness-release-v7",
                    "source_communication_scope_id": "scope-issued-after-upgrade",
                    "state": "active",
                    "state_reason": "issued",
                },
            ],
            member_rows=migrated_members,
        )

    with pytest.raises(ServerSetupError, match="release state changed before rollback"):
        server_setup._require_migrated_collaboration_state(
            expected=expected,
            scope_rows=[migrated_scope],
            member_rows=[
                migrated_members[0],
                {
                    "scope_id": "scope-release-v6",
                    "harness_id": "harness-joined-after-upgrade",
                    "role": "member",
                },
            ],
        )


def test_unenrolled_v6_source_expects_no_migrated_collaboration_state() -> None:
    """A deployment without committed v6 authority still requires empty v7 tables."""

    server_setup._require_migrated_collaboration_state(
        expected=[],
        scope_rows=[],
        member_rows=[],
    )
    with pytest.raises(ServerSetupError, match="release state changed before rollback"):
        server_setup._require_migrated_collaboration_state(
            expected=[],
            scope_rows=[
                {
                    "scope_id": "scope-issued-after-upgrade",
                    "owner_harness_id": "harness-release-v7",
                    "source_communication_scope_id": "scope-issued-after-upgrade",
                    "state": "active",
                    "state_reason": "issued",
                }
            ],
            member_rows=[],
        )