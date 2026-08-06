from __future__ import annotations

import base64
import hashlib
import json
import sqlite3
from importlib import import_module
from pathlib import Path

import pytest
from pydantic import ValidationError

from agentnet.errors import GateBlocked
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS
from agentnet.storage.sqlite import SCHEMA_V5, SCHEMA_V6, SQLiteStore


FIXTURE = Path(__file__).parents[1] / "fixtures" / "bootstrap_plan_golden_vector.json"
FIXTURE_SHA256 = "1b75c19539c518b8bc9842bdad58519c02375c78de2b9e8b48279eadc1b67b59"
REQUIRED_PLAN_TABLES = {
    "bootstrap_grant_plans",
    "bootstrap_grant_plan_items",
    "c0_plan_guards",
    "c0_plan_guard_entitlements",
    "c0_pilot_attempts",
    "c0_pilot_facts",
}

PRESERVED_V6_TABLES = {
    "communication_scopes",
    "communication_scope_items",
    "console_session_challenges",
    "console_oidc_transactions",
    "console_browser_sessions",
    "console_mutation_authorizations",
    "console_server_status",
    "console_enrollment_intents",
    "console_enrollment_reviews",
    "console_enrollment_candidates",
    "console_mutations",
}
REQUIRED_V7_TABLES = {
    "endpoint_lifecycle",
    "collaboration_scopes",
    "collaboration_scope_members",
    "artifact_transfers",
    "artifact_transfer_recipients",
    "invitation_links",
    "invitation_link_failures",
}


def _canonical(value: object) -> bytes:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        allow_nan=False,
    ).encode("utf-8")


def _digest(value: object) -> str:
    return hashlib.sha256(_canonical(value)).hexdigest()


def _derived_id(plan_digest: str, kind: str, ordinal: int | None = None) -> str:
    value: dict[str, object] = {
        "schema": "agentnet.bootstrap-plan.derived-id.v1",
        "plan_digest": plan_digest,
        "kind": kind,
    }
    if ordinal is not None:
        value["ordinal"] = ordinal
    prefix = {"plan": "bp1", "guard": "c0g1", "item": "bpi1", "entitlement": "ent1"}[kind]
    token = base64.urlsafe_b64encode(hashlib.sha256(_canonical(value)).digest()).rstrip(b"=").decode()
    return f"{prefix}_{token}"


def test_checked_in_bootstrap_plan_vector_is_exact_and_self_consistent() -> None:
    raw = FIXTURE.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == FIXTURE_SHA256
    vector = json.loads(raw)

    preimage = vector["canonical_plan_preimage"]
    assert _canonical(preimage["value"]).decode() == preimage["canonical_text"]
    assert _digest(preimage["value"]) == preimage["plan_digest"]

    transaction = vector["final_approval_transaction"]
    assert _canonical(transaction["value"]).decode() == transaction["canonical_text"]
    assert _digest(transaction["value"]) == transaction["transaction_digest"]
    assert transaction["value"]["plan_digest"] == preimage["plan_digest"]

    ids = vector["derived_ids"]
    assert ids["plan_id"] == _derived_id(preimage["plan_digest"], "plan")
    assert ids["guard_id"] == _derived_id(preimage["plan_digest"], "guard")
    assert ids["item_ids"] == [
        _derived_id(preimage["plan_digest"], "item", ordinal) for ordinal in range(1, 11)
    ]
    assert ids["entitlement_ids"] == [
        _derived_id(preimage["plan_digest"], "entitlement", ordinal)
        for ordinal in range(1, 11)
    ]

    for key in ("approval_create", "completion_reservation", "retrieval"):
        assert _digest(vector[key]["value"]) == vector[key]["digest"]
    assert "claim_code" not in vector["completion_reservation"]["value"]
    assert "claim_code_sha256" not in vector["completion_reservation"]["value"]
    assert vector["final_approval_transaction"]["value"]["guard"]["state_at_commit"] == "pending"
    assert len(vector["final_approval_transaction"]["value"]["items"]) == 10


def test_runtime_builder_reproduces_the_complete_golden_transaction() -> None:
    contract = import_module("agentnet.authorization.bootstrap_plan")
    vector = json.loads(FIXTURE.read_text())
    built = contract.build_bootstrap_plan_transaction(
        vector["canonical_plan_preimage"]["value"]
    )
    assert built == vector["final_approval_transaction"]["value"]
    assert _digest(built) == vector["final_approval_transaction"]["transaction_digest"]


def test_bounded_c0_plan_contract_is_explicit_and_non_wildcard() -> None:
    contract = import_module("agentnet.authorization.bootstrap_plan")

    assert contract.BOOTSTRAP_PLAN_PROFILE == "ordinary-two-harness-c0:v1"
    assert contract.BOOTSTRAP_PLAN_APPROVAL_PURPOSE == "authorization.bootstrap_plan.approve"
    assert contract.COMMUNICATION_ENTITLEMENT_COUNT == 5
    assert contract.REVOCATION_ENTITLEMENT_COUNT == 5
    assert contract.TOTAL_ENTITLEMENT_COUNT == 10
    assert "authorization.entitlement.issue" not in contract.BOOTSTRAP_PLAN_ACTIONS
    assert "*" not in contract.BOOTSTRAP_PLAN_RESOURCES


def test_plan_begin_status_and_complete_requests_expose_only_retry_keys() -> None:
    contract = import_module("agentnet.authorization.bootstrap_plan")
    begin = {
        "schema": "agentnet.bootstrap-plan.begin.v1",
        "begin_idempotency_key": "fixed-bootstrap-begin-key-0001",
    }
    parsed = contract.BootstrapPlanBeginRequest.model_validate(begin)
    assert parsed.begin_idempotency_key == begin["begin_idempotency_key"]

    status = contract.BootstrapPlanStatusRequest.model_validate(
        {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin["begin_idempotency_key"]}
    )
    assert status.begin_idempotency_key == begin["begin_idempotency_key"]

    complete = contract.BootstrapPlanCompletionRequest.model_validate(
        {
            "schema": "agentnet.bootstrap-plan.complete.v2",
            "begin_idempotency_key": begin["begin_idempotency_key"],
            "completion_idempotency_key": "fixed-bootstrap-complete-key-0001",
        }
    )
    assert complete.completion_idempotency_key.endswith("0001")

    forbidden = {
        "profile",
        "peer_harness_id",
        "principal_id",
        "domain_id",
        "items",
        "ttl_seconds",
        "plan_id",
        "completion_request_digest",
        "transaction_digest",
    }
    for field in forbidden:
        with pytest.raises(ValidationError):
            contract.BootstrapPlanBeginRequest.model_validate({**begin, field: "caller-value"})
        with pytest.raises(ValidationError):
            contract.BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin["begin_idempotency_key"],
                    "completion_idempotency_key": "fixed-bootstrap-complete-key-0001",
                    field: "caller-value",
                }
            )


def test_public_s4_results_are_prepared_but_unusable_and_identity_free() -> None:
    contract = import_module("agentnet.authorization.bootstrap_plan")
    result = contract.BootstrapPlanCompleteResult.model_validate(
        {
            "schema": "agentnet.bootstrap-plan.complete-result.v1",
            "status": "prepared_unusable",
            "authority_granted": False,
            "communication_usable": False,
        }
    )
    assert result.authority_granted is False
    assert result.communication_usable is False
    for forbidden in (
        "principal_id",
        "harness_id",
        "display_name",
        "event_id",
        "envelope_digest",
        "payload",
        "receipt",
        "transaction_digest",
    ):
        with pytest.raises(ValidationError):
            contract.BootstrapPlanCompleteResult.model_validate(
                {**result.model_dump(by_alias=True), forbidden: "sensitive"}
            )


def test_bootstrap_state_machine_keeps_s4_guard_pending_until_s5() -> None:
    contract = import_module("agentnet.authorization.bootstrap_plan")
    assert contract.ALLOWED_BOOTSTRAP_PLAN_TRANSITIONS == {
        "reserved": {"pending_approval", "expired", "invalidated"},
        "pending_approval": {"approval_issued", "rejected", "canceled", "expired", "invalidated"},
        "approval_issued": {"completion_reserved", "expired", "invalidated"},
        "completion_reserved": {"committed", "expired", "invalidated"},
        "committed": set(),
        "rejected": set(),
        "canceled": set(),
        "expired": set(),
        "invalidated": set(),
    }
    assert contract.BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT == "pending"
    assert contract.C0_REQUIRED_FACTS == (
        "request_durable_custody",
        "request_retrieved",
        "request_recipient_acknowledged",
        "reply_sent",
        "reply_durable_custody",
        "reply_retrieved",
        "reply_final_acknowledged",
    )


def _initialize_schema_fixture(
    path: Path,
    *,
    schema: str,
    version: int,
    catalog: list[tuple[int, str, str]],
) -> None:
    connection = sqlite3.connect(path)
    try:
        connection.executescript(schema)
        connection.execute(
            "INSERT INTO metadata(key,value) VALUES('schema_version',?)",
            (str(version),),
        )
        connection.executemany(
            "INSERT INTO installed_migration_catalog(version,name,checksum) VALUES(?,?,?)",
            catalog,
        )
        connection.commit()
    finally:
        connection.close()
    path.chmod(0o600)


def _migration_catalog(
    count: int,
) -> list[tuple[int, str, str]]:
    return [
        (migration.version, migration.name, migration.checksum)
        for migration in MIGRATIONS[:count]
    ]


def _assert_rejected_without_mutation(path: Path, *, match: str) -> None:
    before = path.read_bytes()
    with pytest.raises(GateBlocked, match=match):
        SQLiteStore(path, LocalEnvelopeCipher(b"r" * 32))
    assert path.read_bytes() == before


def test_sqlite_clean_start_contains_complete_schema_v7_release(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "plan-schema.sqlite3", LocalEnvelopeCipher(b"p" * 32))
    try:
        assert store.readiness()["schema_version"] == 7
        tables = {
            str(row["name"])
            for row in store.fetch_all(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
        assert REQUIRED_PLAN_TABLES | PRESERVED_V6_TABLES | REQUIRED_V7_TABLES <= tables
        columns = {
            str(row["name"])
            for row in store.fetch_all("PRAGMA table_info(bootstrap_grant_plans)")
        }
        assert {
            "canonical_plan_preimage_json",
            "final_approval_transaction_json",
            "plan_digest",
            "transaction_digest",
            "begin_idempotency_key_sha256",
            "completion_idempotency_key_sha256",
            "approval_request_id",
            "approval_create_request_digest",
            "committed_result_digest",
        } <= columns
        guard_columns = {
            str(row["name"])
            for row in store.fetch_all("PRAGMA table_info(c0_plan_guards)")
        }
        assert {"request_payload_schema_digest", "reply_payload_schema_digest"} <= guard_columns
        fact_columns = {
            str(row["name"])
            for row in store.fetch_all("PRAGMA table_info(c0_pilot_facts)")
        }
        assert {
            "issuer_kind", "issuer_harness_id", "event_id", "receipt_id",
            "envelope_digest", "storage_fact", "evidence_json",
        } <= fact_columns
    finally:
        store.close()


def test_sqlite_v6_migrates_to_v7_and_preserves_existing_rows_and_catalog(
    tmp_path: Path,
) -> None:
    path = tmp_path / "v6.sqlite3"
    _initialize_schema_fixture(
        path,
        schema=SCHEMA_V6,
        version=6,
        catalog=_migration_catalog(6),
    )
    connection = sqlite3.connect(path)
    try:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,?,?)",
            ("domain-preserved", "active", 100),
        )
        connection.execute(
            """INSERT INTO principals(
                   principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
               ) VALUES(?,?,?,?,?,?,?)""",
            (
                "principal-preserved",
                "domain-preserved",
                "https://issuer.example",
                "subject-preserved",
                "person@example.test",
                "active",
                101,
            ),
        )
        connection.executemany(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,kind,display_name,status,
                   binding_assurance,capabilities_json,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            [
                (
                    "harness-owner",
                    "domain-preserved",
                    "principal-preserved",
                    "omp",
                    "Owner",
                    "active",
                    "os_bound",
                    "{}",
                    102,
                ),
                (
                    "harness-fresh",
                    "domain-preserved",
                    "principal-preserved",
                    "pi",
                    "Fresh",
                    "active",
                    "os_bound",
                    "{}",
                    103,
                ),
            ],
        )
        connection.executemany(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,?,?,?,?)""",
            [
                ("credential-owner", "harness-owner", "key-owner", "pem", "active", 1, 100, 999),
                ("credential-fresh", "harness-fresh", "key-fresh", "pem", "active", 1, 100, 999),
            ],
        )
        connection.execute(
            """INSERT INTO communication_scopes(
                   scope_id,profile,profile_version,domain_id,principal_id,
                   owner_harness_id,fresh_harness_id,owner_credential_id,fresh_credential_id,
                   owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                   policy_revision,actor_binding_json,canonical_scope_preimage_json,
                   final_approval_transaction_json,scope_digest,transaction_digest,
                   begin_idempotency_key_sha256,state,created_at,approval_expires_at,
                   approval_create_idempotency_key,approval_create_request_digest
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "scope-preserved",
                "same-principal-full-communication:v1",
                1,
                "domain-preserved",
                "principal-preserved",
                "harness-owner",
                "harness-fresh",
                "credential-owner",
                "credential-fresh",
                1,
                1,
                1,
                1,
                "{}",
                "{}",
                "{}",
                "a" * 64,
                "b" * 64,
                "c" * 64,
                "reserved",
                110,
                200,
                "approval-key-preserved",
                "d" * 64,
            ),
        )
        connection.execute(
            """INSERT INTO console_enrollment_candidates(
                   transaction_id,begin_idempotency_hash,begin_request_digest,state_hash,
                   nonce_hash,continuation_hash,begin_response_encrypted,code_verifier_encrypted,
                   candidate_harness_id,candidate_harness_kind,candidate_harness_name,
                   candidate_binding_assurance,candidate_public_key_pem,candidate_key_id,
                   state,created_at,updated_at,expires_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "candidate-preserved",
                "e" * 64,
                "f" * 64,
                "1" * 64,
                "2" * 64,
                "3" * 64,
                "encrypted-response",
                "encrypted-verifier",
                "candidate-harness",
                "pi",
                "Candidate",
                "os_bound",
                "public-key",
                "k" * 43,
                "waiting_oidc",
                120,
                120,
                220,
            ),
        )
        connection.commit()
    finally:
        connection.close()

    store = SQLiteStore(path, LocalEnvelopeCipher(b"m" * 32))
    try:
        assert store.readiness()["schema_version"] == 7
        scope = store.fetch_one(
            """SELECT scope_id,profile,state,owner_harness_id,fresh_harness_id
               FROM communication_scopes WHERE scope_id=?""",
            ("scope-preserved",),
        )
        assert scope is not None
        assert dict(scope) == {
            "scope_id": "scope-preserved",
            "profile": "same-principal-full-communication:v1",
            "state": "reserved",
            "owner_harness_id": "harness-owner",
            "fresh_harness_id": "harness-fresh",
        }
        candidate = store.fetch_one(
            """SELECT transaction_id,candidate_harness_id,state
               FROM console_enrollment_candidates WHERE transaction_id=?""",
            ("candidate-preserved",),
        )
        assert candidate is not None
        assert dict(candidate) == {
            "transaction_id": "candidate-preserved",
            "candidate_harness_id": "candidate-harness",
            "state": "waiting_oidc",
        }
        assert [
            (int(row["version"]), str(row["name"]), str(row["checksum"]))
            for row in store.fetch_all(
                "SELECT version,name,checksum FROM installed_migration_catalog ORDER BY version"
            )
        ] == _migration_catalog(7)
        tables = {
            str(row["name"])
            for row in store.fetch_all(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
        assert PRESERVED_V6_TABLES | REQUIRED_V7_TABLES <= tables
    finally:
        store.close()


def test_sqlite_v5_catalog_is_outside_v6_v7_window_and_fails_closed(
    tmp_path: Path,
) -> None:
    path = tmp_path / "v5.sqlite3"
    _initialize_schema_fixture(
        path,
        schema=SCHEMA_V5,
        version=5,
        catalog=_migration_catalog(5),
    )
    _assert_rejected_without_mutation(path, match="exact supported N/N-1 migration window")


def test_sqlite_future_catalog_fails_closed(tmp_path: Path) -> None:
    path = tmp_path / "future.sqlite3"
    _initialize_schema_fixture(
        path,
        schema=SCHEMA_V6,
        version=8,
        catalog=[
            *_migration_catalog(7),
            (8, "future_schema", "8" * 64),
        ],
    )
    _assert_rejected_without_mutation(path, match="newer than this extension")


def test_sqlite_v6_tampered_catalog_fails_closed(tmp_path: Path) -> None:
    path = tmp_path / "tampered-v6.sqlite3"
    catalog = _migration_catalog(6)
    version, name, _checksum = catalog[-1]
    catalog[-1] = (version, name, "0" * 64)
    _initialize_schema_fixture(
        path,
        schema=SCHEMA_V6,
        version=6,
        catalog=catalog,
    )
    _assert_rejected_without_mutation(path, match="migration history checksum is invalid")


def test_postgres_catalog_preserves_v1_to_v6_and_appends_immutable_v7() -> None:
    assert CURRENT_SCHEMA_VERSION == 7
    assert [(migration.version, migration.name) for migration in MIGRATIONS] == [
        (1, "agentnet_first_release_schema"),
        (2, "protected_task_payload_release"),
        (3, "guided_oidc_enrollment_continuation"),
        (4, "bounded_c0_bootstrap_plan"),
        (5, "identity_begin_idempotency_and_credential_renewal"),
        (6, "communication_scope_and_private_administration"),
        (7, "communication_collaboration_release"),
    ]
    assert MIGRATIONS[0].checksum == (
        "c472c4442fce9195580bd55d6f01d831f9ef34cb8cc34b8389b72b1c572d484f"
    )
    bootstrap_plan = MIGRATIONS[3]
    for table in REQUIRED_PLAN_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in bootstrap_plan.sql
    assert "canonical_plan_preimage_json" in bootstrap_plan.sql
    assert "transaction_digest" in bootstrap_plan.sql
    assert (
        "issuer_kind TEXT NOT NULL CHECK (issuer_kind IN ('accepting_core','harness'))"
        in bootstrap_plan.sql
    )
    assert (
        "storage_fact TEXT CHECK (storage_fact IS NULL OR storage_fact IN "
        "('accepted_local','accepted_durable'))"
        in bootstrap_plan.sql
    )
    assert "issuer_kind='accepting_core' AND issuer_harness_id IS NULL" in bootstrap_plan.sql
    assert "issuer_kind='harness' AND issuer_harness_id IS NOT NULL" in bootstrap_plan.sql
    lifecycle = MIGRATIONS[4]
    assert "begin_idempotency_key_hash" in lifecycle.sql
    assert "credential_renewal_requests" in lifecycle.sql
    preserved_v6 = MIGRATIONS[5]
    for table in PRESERVED_V6_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in preserved_v6.sql
    release_v7 = MIGRATIONS[6]
    assert (
        release_v7.checksum
        == "cf8b758dd1f1ba5f674bfe7aa6de6966ddc7e5b2032c7381fa5c3a2faa54eb35"
    )
    for table in REQUIRED_V7_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in release_v7.sql
        assert all(table not in migration.sql for migration in MIGRATIONS[:6])
    assert all(" INTEGER" not in migration.sql for migration in MIGRATIONS)
