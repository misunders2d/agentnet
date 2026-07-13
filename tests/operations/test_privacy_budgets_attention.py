from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest

from agentnet.attention.policy import exceptional_notice
from agentnet.errors import GateBlocked
from agentnet.operations.quotas import QuotaService
from agentnet.operations.telemetry import Telemetry
from agentnet.operations.outage import OperationalHealth, OutageGate
from agentnet.operations.policy_defaults import OutagePolicy


def test_telemetry_is_aggregate_bounded_and_rejects_sensitive_labels() -> None:
    telemetry = Telemetry()
    telemetry.increment("queue_accept", outcome="ok", amount=2)
    assert telemetry.snapshot() == {"queue_accept:ok": 2}
    for label in ("message_count", "principal.id", "contains space", "x" * 65):
        with pytest.raises(ValueError):
            telemetry.increment(label)
    with pytest.raises(ValueError):
        telemetry.increment("queue_accept", amount=0)


def test_telemetry_persists_across_server_agent_process_instances(store) -> None:
    Telemetry(store).increment("relay_accept", outcome="ok", amount=2)
    second_process = Telemetry(store)
    second_process.increment("relay_accept", outcome="ok", amount=1)
    assert second_process.snapshot() == {"relay_accept:ok": 3}


def test_telemetry_uses_fixed_buckets_gauges_and_cost_without_identity_labels(store) -> None:
    telemetry = Telemetry(store)
    telemetry.increment("auth_result", outcome="denied")
    telemetry.increment("scanner_result", outcome="error")
    telemetry.increment("adapter_result", outcome="timeout")
    telemetry.increment("audit_check", outcome="invalid")
    telemetry.increment("cost_usage", amount=17)
    telemetry.observe_latency("auth_latency", 7, outcome="denied")
    telemetry.observe_latency("scanner_latency", 31, outcome="error")
    telemetry.observe_latency("adapter_latency", 11, outcome="timeout")
    telemetry.set_gauge("audit_backlog", 9)

    snapshot = telemetry.operational_snapshot()
    assert snapshot["counters"]["cost_usage:ok"] == 17
    assert snapshot["latency_buckets"]["auth_latency:denied:le_10"] == 1
    assert snapshot["gauges"]["audit_backlog"] == 9
    assert "principal" not in repr(snapshot)
    with pytest.raises(ValueError):
        telemetry.observe_latency("auth_latency", 30_001)
    with pytest.raises(ValueError):
        telemetry.set_gauge("customer.queue", 1)


def test_outage_fail_closed_paths_emit_only_fixed_content_free_outcomes(store) -> None:
    now = datetime(2026, 7, 13, 12, 0, tzinfo=UTC)
    health = OperationalHealth(
        revocation_current=False,
        policy_current=False,
        audit_backlog_records=0,
        last_confirmed_current_at=now - timedelta(minutes=10),
    )
    telemetry = Telemetry(store)
    gate = OutageGate(
        OutagePolicy(low_risk_continuity_max_seconds=60),
        health_provider=lambda: health,
        clock=lambda: now,
        telemetry=telemetry,
    )
    with pytest.raises(GateBlocked):
        gate.require_issuance()
    with pytest.raises(GateBlocked):
        gate.require_privileged()
    with pytest.raises(GateBlocked):
        gate.require_low_risk_continuity()

    gate_with_failed_controller = OutageGate(
        OutagePolicy(),
        health_provider=lambda: (_ for _ in ()).throw(RuntimeError("sensitive controller text")),
        telemetry=telemetry,
    )
    with pytest.raises(GateBlocked, match="provider is unavailable"):
        gate_with_failed_controller.require_issuance()
    counters = telemetry.snapshot()
    assert counters == {
        "outage_gate:authority": 1,
        "outage_gate:continuity": 1,
        "outage_gate:invalid": 1,
        "outage_gate:privileged": 1,
    }
    assert "sensitive controller text" not in repr(counters)


def test_outage_decision_survives_unavailable_telemetry_sink() -> None:
    class BrokenTelemetry:
        def record_outage_denial(self, boundary: str) -> None:
            raise RuntimeError(f"sink failed for {boundary}")

    now = datetime(2026, 7, 13, 12, 0, tzinfo=UTC)
    gate = OutageGate(
        OutagePolicy(),
        health_provider=lambda: OperationalHealth(
            revocation_current=False,
            policy_current=False,
            last_confirmed_current_at=now,
        ),
        clock=lambda: now,
        telemetry=BrokenTelemetry(),
    )
    with pytest.raises(GateBlocked) as blocked:
        gate.require_issuance()
    assert blocked.value.gate == "authority_outage"


def test_quota_reserves_capacity_for_safety_lane(store) -> None:
    quotas = QuotaService(store, safety_reserve_fraction=0.1)
    assert quotas.consume(scope="synthetic", metric="requests", amount=90, limit=100)["used"] == 90
    with pytest.raises(GateBlocked):
        quotas.consume(scope="synthetic", metric="requests", amount=1, limit=100)
    assert quotas.consume(scope="synthetic", metric="requests", amount=10, limit=100, safety_lane=True)["used"] == 100
    with pytest.raises(GateBlocked):
        quotas.consume(scope="synthetic", metric="requests", amount=1, limit=100, safety_lane=True)


def test_attention_is_silent_by_default_and_exception_is_content_free() -> None:
    assert exceptional_notice("routine_message", opaque_reference="incident:" + "a" * 32) is None
    assert exceptional_notice(
        "confirmed_security_incident", opaque_reference="incident:" + "a" * 32
    ) == {
        "type": "confirmed_security_incident",
        "reference": "incident:" + "a" * 32,
        "content": "redacted",
    }
    with pytest.raises(ValueError):
        exceptional_notice("confirmed_security_incident", opaque_reference="customer secret in reference")
