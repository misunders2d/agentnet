from __future__ import annotations

import base64
import hashlib
import json
from importlib import import_module
from pathlib import Path

import pytest
from pydantic import ValidationError

from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS
from agentnet.storage.sqlite import SQLiteStore


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


def test_sqlite_clean_start_contains_final_bounded_plan_schema(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "plan-schema.sqlite3", LocalEnvelopeCipher(b"p" * 32))
    try:
        assert store.readiness()["schema_version"] == 5
        tables = {
            str(row["name"])
            for row in store.fetch_all(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
        assert REQUIRED_PLAN_TABLES <= tables
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


def test_postgres_catalog_preserves_v4_plan_and_adds_v5_identity_lifecycle() -> None:
    assert CURRENT_SCHEMA_VERSION == 5
    migration = MIGRATIONS[3]
    assert migration.version == 4
    assert migration.name == "bounded_c0_bootstrap_plan"
    for table in REQUIRED_PLAN_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in migration.sql
    assert "canonical_plan_preimage_json" in migration.sql
    assert "transaction_digest" in migration.sql
    assert "issuer_kind TEXT NOT NULL CHECK (issuer_kind IN ('accepting_core','harness'))" in migration.sql
    assert "storage_fact TEXT CHECK (storage_fact IS NULL OR storage_fact IN ('accepted_local','accepted_durable'))" in migration.sql
    assert "issuer_kind='accepting_core' AND issuer_harness_id IS NULL" in migration.sql
    assert "issuer_kind='harness' AND issuer_harness_id IS NOT NULL" in migration.sql
    assert " INTEGER" not in migration.sql
    lifecycle = MIGRATIONS[-1]
    assert lifecycle.version == 5
    assert lifecycle.name == "identity_begin_idempotency_and_credential_renewal"
    assert "begin_idempotency_key_hash" in lifecycle.sql
    assert "credential_renewal_requests" in lifecycle.sql
    assert " INTEGER" not in lifecycle.sql
