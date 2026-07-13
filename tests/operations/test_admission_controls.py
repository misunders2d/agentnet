from __future__ import annotations

import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta

import pytest

from agentnet.errors import GateBlocked
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.operations.quotas import QuotaService
from agentnet.operations.telemetry import Telemetry
from agentnet.protocol.models import Classification, EventType
from agentnet.storage.sqlite import SQLiteStore


def _policy(**overrides) -> OperationsPolicy:
    return OperationsPolicy(
        per_actor_requests_per_minute=overrides.pop("per_actor_requests_per_minute", 100),
        per_domain_requests_per_minute=overrides.pop("per_domain_requests_per_minute", 100),
        global_requests_per_minute=overrides.pop("global_requests_per_minute", 100),
        pending_delivery_backpressure_limit=overrides.pop(
            "pending_delivery_backpressure_limit", 100
        ),
        fairness_burst_limit=overrides.pop("fairness_burst_limit", 100),
        max_operation_hops=overrides.pop("max_operation_hops", 2),
        circuit_breaker_failure_threshold=overrides.pop(
            "circuit_breaker_failure_threshold", 2
        ),
        circuit_breaker_reset_seconds=overrides.pop("circuit_breaker_reset_seconds", 10),
        **overrides,
    )


def _admit(service: QuotaService, *, actor: str, domain: str, operation_id: str, **values):
    return service.admit_operation(
        actor_scope=actor,
        domain_scope=domain,
        operation="relay",
        operation_id=operation_id,
        cost=values.pop("cost", 1),
        hop_count=values.pop("hop_count", 0),
        **values,
    )


def test_multidimensional_quotas_are_atomic_persistent_and_identity_free(store) -> None:
    clock = [60_000]
    policy = _policy(
        per_actor_requests_per_minute=10,
        per_domain_requests_per_minute=10,
        global_requests_per_minute=10,
    )
    first = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=Telemetry(store),
        clock=lambda: clock[0],
    )
    _admit(first, actor="actor-a", domain="domain-a", operation_id="operation-a", cost=6)
    second_process = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=Telemetry(store),
        clock=lambda: clock[0],
    )
    _admit(second_process, actor="actor-b", domain="domain-a", operation_id="operation-b", cost=4)
    with pytest.raises(GateBlocked, match="domain budget exhausted"):
        _admit(
            second_process,
            actor="actor-c",
            domain="domain-a",
            operation_id="operation-c",
        )

    rows = store.fetch_all("SELECT scope,metric,used FROM quota_counters ORDER BY metric,scope")
    assert {row["used"] for row in rows if row["metric"] == "relay_domain"} == {10}
    serialized = repr([dict(row) for row in rows])
    assert "actor-a" not in serialized
    assert "actor-b" not in serialized
    assert "domain-a" not in serialized


def test_backpressure_fair_share_and_loop_fences_survive_service_instances(store) -> None:
    clock = [70_000]
    telemetry = Telemetry(store)
    policy = _policy(
        pending_delivery_backpressure_limit=10,
        fairness_burst_limit=1,
    )
    service = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=telemetry,
        clock=lambda: clock[0],
    )
    _admit(service, actor="actor-a", domain="fair.example", operation_id="fair-a-1")
    _admit(service, actor="actor-b", domain="fair.example", operation_id="fair-b-1")
    _admit(service, actor="actor-a", domain="fair.example", operation_id="fair-a-2")
    _admit(service, actor="actor-a", domain="fair.example", operation_id="fair-a-3")
    with pytest.raises(GateBlocked, match="fair-share"):
        _admit(service, actor="actor-a", domain="fair.example", operation_id="fair-a-4")

    _admit(
        service,
        actor="loop-actor",
        domain="loop.example",
        operation_id="loop-operation",
        hop_count=2,
    )
    restarted = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=telemetry,
        clock=lambda: clock[0],
    )
    with pytest.raises(GateBlocked, match="regressed"):
        _admit(
            restarted,
            actor="loop-actor",
            domain="loop.example",
            operation_id="loop-operation",
            hop_count=1,
        )
    with pytest.raises(GateBlocked, match="maximum hop"):
        _admit(
            restarted,
            actor="loop-actor",
            domain="loop.example",
            operation_id="new-loop-operation",
            hop_count=3,
        )
    assert restarted.content_free_status()["active_loop_fences"] >= 4


def test_canonical_mailbox_accept_atomically_reserves_and_terminal_rows_release_pressure(
    store,
    identity_factory,
) -> None:
    sender, _key = identity_factory()
    recipients = tuple(identity_factory(kind="pi")[0] for _ in range(11))
    policy = _policy(
        pending_delivery_backpressure_limit=10,
        fairness_burst_limit=100,
        per_actor_requests_per_minute=1000,
        per_domain_requests_per_minute=1000,
        global_requests_per_minute=1000,
    )
    admission = QuotaService(store, policy=policy, safety_reserve_fraction=0)
    mailbox = MailboxService(store, admission=admission)

    first = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"kind": "pressure-fill"},
        idempotency_key="pressure-fill-event-0001",
        recipients=tuple(recipient.harness_id for recipient in recipients[:10]),
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
    )
    mailbox.accept(first)
    for _ in range(policy.circuit_breaker_failure_threshold):
        admission.record_failure(operation="mailbox_accept", domain_scope=sender.domain_id)
    quota_before = store.fetch_one("SELECT COALESCE(SUM(used),0) AS used FROM quota_counters")["used"]
    assert mailbox.accept(first)["duplicate"] is True
    assert admission.content_free_status()["open_breakers"] == 1
    assert store.fetch_one("SELECT COALESCE(SUM(used),0) AS used FROM quota_counters")["used"] == quota_before
    admission.record_success(operation="mailbox_accept", domain_scope=sender.domain_id)
    blocked = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"kind": "pressure-blocked"},
        idempotency_key="pressure-block-event-001",
        recipients=(recipients[10].harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
    )
    with pytest.raises(GateBlocked, match="pressure"):
        mailbox.accept(blocked)
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM events WHERE event_id=?", (blocked.event_id,)
    )["count"] == 0

    # Terminal lifecycle state is the authoritative release; there is no
    # separately drifting pending-depth counter.
    with store.transaction() as connection:
        connection.execute(
            "UPDATE recipients SET current_fact='completed' WHERE event_id=?",
            (first.event_id,),
        )
    assert mailbox.accept(blocked)["duplicate"] is False


def test_concurrent_sqlite_half_open_probe_has_exactly_one_cas_winner(store) -> None:
    clock = [90_000]
    policy = _policy()
    opener = QuotaService(store, policy=policy, safety_reserve_fraction=0, clock=lambda: clock[0])
    opener.record_failure(operation="relay", domain_scope="sqlite-race.example")
    opener.record_failure(operation="relay", domain_scope="sqlite-race.example")
    clock[0] += 11

    peers = (
        SQLiteStore(store.path, store.cipher),
        SQLiteStore(store.path, store.cipher),
    )
    barrier = threading.Barrier(2)

    def probe(index: int) -> str:
        service = QuotaService(
            peers[index], policy=policy, safety_reserve_fraction=0, clock=lambda: clock[0]
        )
        barrier.wait(timeout=5)
        try:
            _admit(
                service,
                actor=f"probe-{index}",
                domain="sqlite-race.example",
                operation_id=f"probe-{index}",
            )
        except GateBlocked:
            return "denied"
        return "admitted"

    try:
        with ThreadPoolExecutor(max_workers=2) as executor:
            assert sorted(executor.map(probe, range(2))) == ["admitted", "denied"]
    finally:
        for peer in peers:
            peer.close()


def test_half_open_conditional_update_rowcount_zero_denies_fake_postgres_loser() -> None:
    class Cursor:
        rowcount = 0

    class Connection:
        def execute(self, query, _parameters=()):
            if query.startswith("SELECT"):
                return type("Row", (), {"fetchone": lambda _self: {"state": "open", "retry_after": 1}})()
            return Cursor()

    service = object.__new__(QuotaService)
    with pytest.raises(GateBlocked, match="claimed concurrently"):
        service._require_breaker_in_transaction(
            Connection(), breaker_key="f" * 64, now=2
        )


def test_durable_operational_work_limit_and_terminal_release_are_atomic(store) -> None:
    clock = [110_000]
    policy = _policy().model_copy(update={"pending_delivery_backpressure_limit": 2})
    service = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        clock=lambda: clock[0],
    )

    def reserve(source_id: str) -> None:
        with store.transaction() as connection:
            service._admit_operation_in_transaction(
                connection,
                actor_scope="relay-agent",
                domain_scope="work.example",
                operation="relay_outbound",
                operation_id=source_id,
            )
            service._reserve_work_in_transaction(
                connection,
                work_kind="relay_outbound",
                source_id=source_id,
                domain_id="work.example",
                now=clock[0],
            )

    reserve("packet-1")
    reserve("packet-2")
    with pytest.raises(GateBlocked, match="pressure"):
        reserve("packet-3")
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM operational_work_reservations WHERE state='pending'"
    )["count"] == 2
    with store.transaction() as connection:
        assert service._terminalize_work_in_transaction(
            connection,
            work_kind="relay_outbound",
            source_id="packet-1",
            now=clock[0],
        )
        assert not service._terminalize_work_in_transaction(
            connection,
            work_kind="relay_outbound",
            source_id="packet-1",
            now=clock[0],
        )
    reserve("packet-3")


def test_abandoned_half_open_probe_is_reclaimed_once_after_lease(store) -> None:
    clock = [120_000]
    policy = _policy(circuit_breaker_reset_seconds=10)
    service = QuotaService(store, policy=policy, safety_reserve_fraction=0, clock=lambda: clock[0])
    service.record_failure(operation="relay", domain_scope="reclaim.example")
    service.record_failure(operation="relay", domain_scope="reclaim.example")
    clock[0] += 11
    _admit(service, actor="first", domain="reclaim.example", operation_id="first-probe")
    with pytest.raises(GateBlocked, match="in flight"):
        _admit(service, actor="second", domain="reclaim.example", operation_id="second-probe")
    clock[0] += 11
    _admit(service, actor="reclaimed", domain="reclaim.example", operation_id="reclaimed-probe")
    with pytest.raises(GateBlocked, match="in flight"):
        _admit(service, actor="loser", domain="reclaim.example", operation_id="reclaim-loser")


def test_persistent_circuit_breaker_allows_one_half_open_probe(store) -> None:
    clock = [80_000]
    policy = _policy()
    first = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=Telemetry(store),
        clock=lambda: clock[0],
    )
    assert first.record_failure(operation="relay", domain_scope="breaker.example")["state"] == "closed"
    assert first.record_failure(operation="relay", domain_scope="breaker.example")["state"] == "open"
    restarted = QuotaService(
        store,
        policy=policy,
        safety_reserve_fraction=0,
        telemetry=Telemetry(store),
        clock=lambda: clock[0],
    )
    with pytest.raises(GateBlocked, match="open"):
        _admit(
            restarted,
            actor="probe-actor",
            domain="breaker.example",
            operation_id="probe-before-reset",
        )
    clock[0] += 11
    assert _admit(
        restarted,
        actor="probe-actor",
        domain="breaker.example",
        operation_id="probe-after-reset",
    ).admitted
    with pytest.raises(GateBlocked, match="probe"):
        _admit(
            first,
            actor="other-actor",
            domain="breaker.example",
            operation_id="second-probe",
        )
    restarted.record_success(operation="relay", domain_scope="breaker.example")
    assert restarted.content_free_status()["open_breakers"] == 0
