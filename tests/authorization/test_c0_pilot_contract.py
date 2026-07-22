from __future__ import annotations

from pathlib import Path

import pytest
from pydantic import ValidationError

from agentnet.authorization.bootstrap_plan import C0_REQUIRED_FACTS
from agentnet.authorization.c0_pilot import (
    C0PilotCompleteRequest,
    C0PilotRespondRequest,
    C0PilotStartRequest,
    C0PilotStatusRequest,
    c0_result,
)
from agentnet.storage.bootstrap_plan_schema import BOOTSTRAP_PLAN_SCHEMA


ROOT = Path(__file__).resolve().parents[2]


def test_c0_requests_are_schema_only_and_reject_all_selectors() -> None:
    models = (
        (C0PilotStartRequest, "agentnet.c0-pilot.start.v1"),
        (C0PilotRespondRequest, "agentnet.c0-pilot.respond.v1"),
        (C0PilotCompleteRequest, "agentnet.c0-pilot.complete.v1"),
        (C0PilotStatusRequest, "agentnet.c0-pilot.status.v1"),
    )
    for model, schema in models:
        assert model.model_validate({"schema": schema}).model_dump(by_alias=True) == {
            "schema": schema
        }
        for selector in (
            "plan_id", "guard_id", "peer_harness_id", "direction", "classification",
            "payload", "use_count", "event_id", "envelope_digest", "entitlement_id",
        ):
            with pytest.raises(ValidationError):
                model.model_validate({"schema": schema, selector: "caller-value"})


def test_c0_result_is_sanitized_and_fact_set_is_exact() -> None:
    assert c0_result("COMPLETED_C0_ROUND_TRIP") == {
        "schema": "agentnet.c0-pilot.result.v1",
        "status": "COMPLETED_C0_ROUND_TRIP",
    }
    assert c0_result("invalidated") == {
        "schema": "agentnet.c0-pilot.result.v1",
        "status": "invalidated",
    }
    assert C0_REQUIRED_FACTS == (
        "request_durable_custody",
        "request_retrieved",
        "request_recipient_acknowledged",
        "reply_sent",
        "reply_durable_custody",
        "reply_retrieved",
        "reply_final_acknowledged",
    )


def test_c0_persisted_state_vocabulary_matches_authoritative_docs() -> None:
    guard_sql = BOOTSTRAP_PLAN_SCHEMA.split(
        "CREATE TABLE IF NOT EXISTS c0_plan_guards", 1
    )[1].split("CREATE TABLE IF NOT EXISTS bootstrap_grant_plan_items", 1)[0]
    attempt_sql = BOOTSTRAP_PLAN_SCHEMA.split(
        "CREATE TABLE IF NOT EXISTS c0_pilot_attempts", 1
    )[1].split("CREATE TABLE IF NOT EXISTS c0_pilot_facts", 1)[0]
    assert "state IN ('pending','active','revoked','expired','invalidated')" in guard_sql
    assert "consumed" not in guard_sql
    assert (
        "'active','evidence_complete','communication_revoked','failed','expired'"
        in attempt_sql
    )
    assert "'pending'" not in attempt_sql

    docs = (ROOT / "docs/SCHEMAS_INTERFACES.md").read_text(encoding="utf-8")
    assert "Persisted guard state is `pending | active | revoked | expired | invalidated`" in docs
    assert (
        "Persisted attempt state is `active |\n"
        "evidence_complete | communication_revoked | failed | expired`"
        in docs
    )

    service = (
        ROOT / "src/agentnet/authorization/c0_pilot_service.py"
    ).read_text(encoding="utf-8")
    assert "state IN ('pending','active','evidence_complete')" not in service
    assert '"pending", "active", "evidence_complete"' not in service
    assert '"pending",\n            "active",\n            "evidence_complete"' not in service
