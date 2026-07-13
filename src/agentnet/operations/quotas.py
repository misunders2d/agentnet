"""Persistent multidimensional admission, fairness, loop, and breaker controls."""

from __future__ import annotations

import hashlib
import re
import time
from collections.abc import Callable, Iterable
from dataclasses import dataclass
from typing import Any, Protocol

from agentnet.errors import GateBlocked
from agentnet.delivery.state import TERMINAL_FACTS
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.storage.backend import StoreBackend
from agentnet.storage.operational_control_schema import (
    require_operational_control_schema,
)


SAFE_METRIC = re.compile(r"^[a-z][a-z0-9_]{0,63}$")


class AdmissionTelemetry(Protocol):
    def increment(self, metric: str, *, outcome: str = "ok", amount: int = 1) -> None: ...

    def set_gauge(self, metric: str, value: int) -> None: ...


@dataclass(frozen=True, slots=True)
class QuotaDimension:
    scope: str
    metric: str
    amount: int
    limit: int


@dataclass(frozen=True, slots=True)
class AdmissionDecision:
    admitted: bool
    fair_sequence: int
    window_start: int
    dimensions: tuple[str, ...]


class QuotaService:
    def __init__(
        self,
        store: StoreBackend,
        *,
        policy: OperationsPolicy | None = None,
        window_seconds: int = 60,
        safety_reserve_fraction: float = 0.1,
        clock: Callable[[], int] = lambda: int(time.time()),
        telemetry: AdmissionTelemetry | None = None,
    ) -> None:
        if window_seconds <= 0 or not 0 <= safety_reserve_fraction < 1:
            raise ValueError("quota window/reserve configuration is invalid")
        self.store = store
        self.policy = policy
        self.window_seconds = window_seconds
        self.safety_reserve_fraction = safety_reserve_fraction
        self.clock = clock
        self.telemetry = telemetry
        require_operational_control_schema(store)
        self._reconcile_operational_work()

    def _reconcile_operational_work(self) -> None:
        """Backfill durable work state after upgrade before admitting new work."""

        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO operational_work_reservations(
                       work_kind,source_id,domain_id,state,created_at,updated_at)
                   SELECT 'relay_outbound',o.packet_id,e.domain_id,
                          CASE WHEN o.state IN ('staged','remote_accepted') THEN 'pending' ELSE 'terminal' END,
                          o.created_at,o.updated_at
                     FROM server_agent_relay_outbox o JOIN events e ON e.event_id=o.event_id
                   ON CONFLICT(work_kind,source_id) DO NOTHING"""
            )
            connection.execute(
                """INSERT INTO operational_work_reservations(
                       work_kind,source_id,domain_id,state,created_at,updated_at)
                   SELECT 'relay_inbound',i.packet_id,h.domain_id,
                          CASE WHEN i.state='authorized_pending' THEN 'pending' ELSE 'terminal' END,
                          i.created_at,i.updated_at
                     FROM server_agent_relay_inbox i JOIN harnesses h ON h.harness_id=i.target_recipient_id
                   ON CONFLICT(work_kind,source_id) DO NOTHING"""
            )
            connection.execute(
                """INSERT INTO operational_work_reservations(
                       work_kind,source_id,domain_id,state,created_at,updated_at)
                   SELECT 'protected_effect',x.effect_id,e.domain_id,
                          CASE WHEN x.state IN ('effect_prepared','effect_executing','effect_unknown')
                               THEN 'pending' ELSE 'terminal' END,
                          x.created_at,x.updated_at
                     FROM effect_reservations x JOIN events e ON e.event_id=x.event_id
                   ON CONFLICT(work_kind,source_id) DO NOTHING"""
            )

    @staticmethod
    def _scope_hash(scope: str) -> str:
        if not scope or len(scope) > 512 or scope != scope.strip():
            raise ValueError("quota scope is invalid")
        return hashlib.sha256(("agentnet.quota.scope.v1\x00" + scope).encode("utf-8")).hexdigest()

    @staticmethod
    def _operation_key(operation: str, domain_scope: str) -> str:
        if not SAFE_METRIC.fullmatch(operation):
            raise ValueError("operation name is outside the bounded catalog shape")
        return hashlib.sha256(
            (f"agentnet.breaker.v1\x00{operation}\x00{domain_scope}").encode("utf-8")
        ).hexdigest()

    def _denied(self, gate: str, reason: str) -> None:
        raise GateBlocked(gate, reason)

    def consume_actor_request(
        self,
        *,
        actor_scope: str,
        amount: int = 1,
        safety_lane: bool = False,
    ) -> dict[str, int]:
        if self.policy is None:
            raise ValueError("policy-bound quota operation requires an OperationsPolicy")
        return self.consume(
            scope=actor_scope,
            metric="requests",
            amount=amount,
            limit=self.policy.per_actor_requests_per_minute,
            safety_lane=safety_lane,
        )

    def _consume_many_in_transaction(
        self,
        connection: Any,
        dimensions: Iterable[QuotaDimension],
        *,
        window: int,
        safety_lane: bool,
    ) -> dict[str, int]:
        checked: list[tuple[QuotaDimension, str, int]] = []
        for dimension in dimensions:
            if (
                type(dimension.amount) is not int
                or type(dimension.limit) is not int
                or dimension.amount <= 0
                or dimension.limit <= 0
                or not SAFE_METRIC.fullmatch(dimension.metric)
            ):
                raise ValueError("quota dimension is invalid")
            scope_hash = self._scope_hash(dimension.scope)
            row = connection.execute(
                "SELECT used,limit_value FROM quota_counters WHERE scope=? AND metric=? AND window_start=?",
                (scope_hash, dimension.metric, window),
            ).fetchone()
            used = int(row["used"]) if row else 0
            effective_limit = (
                dimension.limit
                if safety_lane
                else int(dimension.limit * (1 - self.safety_reserve_fraction))
            )
            if used + dimension.amount > effective_limit:
                self._denied("budget_hold", f"{dimension.metric} budget exhausted")
            checked.append((dimension, scope_hash, used))
        results: dict[str, int] = {}
        for dimension, scope_hash, used in checked:
            connection.execute(
                """INSERT INTO quota_counters(scope,metric,window_start,used,limit_value)
                   VALUES(?,?,?,?,?)
                   ON CONFLICT(scope,metric,window_start) DO UPDATE SET
                   used=quota_counters.used+excluded.used,limit_value=excluded.limit_value""",
                (
                    scope_hash,
                    dimension.metric,
                    window,
                    dimension.amount,
                    dimension.limit,
                ),
            )
            results[dimension.metric] = used + dimension.amount
        return results

    def consume(
        self,
        *,
        scope: str,
        metric: str,
        amount: int,
        limit: int,
        safety_lane: bool = False,
    ) -> dict[str, int]:
        now = self.clock()
        window = now - (now % self.window_seconds)
        dimension = QuotaDimension(scope=scope, metric=metric, amount=amount, limit=limit)
        try:
            with self.store.transaction() as connection:
                result = self._consume_many_in_transaction(
                    connection,
                    (dimension,),
                    window=window,
                    safety_lane=safety_lane,
                )
        except GateBlocked:
            if self.telemetry is not None:
                self.telemetry.increment("quota_result", outcome="denied")
            raise
        return {"used": result[metric], "limit": limit, "window_start": window}

    def _require_breaker_in_transaction(
        self,
        connection: Any,
        *,
        breaker_key: str,
        now: int,
    ) -> None:
        row = connection.execute(
            "SELECT * FROM circuit_breakers WHERE breaker_key=?",
            (breaker_key,),
        ).fetchone()
        if row is None or row["state"] == "closed":
            return
        if row["state"] == "half_open":
            claimed_at = int(row["updated_at"])
            if now < claimed_at + self.policy.circuit_breaker_reset_seconds:
                self._denied("circuit_open", "operation circuit breaker probe is already in flight")
            updated = connection.execute(
                """UPDATE circuit_breakers SET updated_at=?
                     WHERE breaker_key=? AND state='half_open' AND updated_at=?""",
                (now, breaker_key, claimed_at),
            )
            if updated.rowcount != 1:
                self._denied("circuit_open", "operation circuit breaker probe was reclaimed concurrently")
            return
        retry_after = int(row["retry_after"] or 0)
        if now < retry_after:
            self._denied("circuit_open", "operation circuit breaker is open")
        updated = connection.execute(
            "UPDATE circuit_breakers SET state='half_open',updated_at=? WHERE breaker_key=? AND state='open'",
            (now, breaker_key),
        )
        if updated.rowcount != 1:
            self._denied("circuit_open", "operation circuit breaker probe was claimed concurrently")

    @staticmethod
    def _pending_delivery_depth_in_transaction(connection: Any, *, domain_scope: str) -> int:
        terminal = tuple(fact.value for fact in sorted(TERMINAL_FACTS, key=lambda item: item.value))
        placeholders = ",".join("?" for _ in terminal)
        mailbox = connection.execute(
            f"""SELECT COUNT(*) AS count
                   FROM recipients r JOIN events e ON e.event_id=r.event_id
                  WHERE e.domain_id=? AND r.current_fact NOT IN ({placeholders})""",
            (domain_scope, *terminal),
        ).fetchone()
        work = connection.execute(
            """SELECT COUNT(*) AS count FROM operational_work_reservations
                 WHERE domain_id=? AND state='pending'""",
            (domain_scope,),
        ).fetchone()
        return int(mailbox["count"] if mailbox else 0) + int(work["count"] if work else 0)

    @staticmethod
    def _reserve_work_in_transaction(
        connection: Any,
        *,
        work_kind: str,
        source_id: str,
        domain_id: str,
        now: int,
    ) -> None:
        inserted = connection.execute(
            """INSERT INTO operational_work_reservations(
                   work_kind,source_id,domain_id,state,created_at,updated_at)
               VALUES(?,?,?,'pending',?,?) ON CONFLICT(work_kind,source_id) DO NOTHING""",
            (work_kind, source_id, domain_id, now, now),
        )
        if inserted.rowcount == 1:
            return
        row = connection.execute(
            """SELECT domain_id,state FROM operational_work_reservations
                 WHERE work_kind=? AND source_id=?""",
            (work_kind, source_id),
        ).fetchone()
        if row is None or row["domain_id"] != domain_id or row["state"] != "pending":
            raise GateBlocked("backpressure_hold", "operational work reservation identity conflicted")

    @staticmethod
    def _terminalize_work_in_transaction(
        connection: Any,
        *,
        work_kind: str,
        source_id: str,
        now: int,
    ) -> bool:
        updated = connection.execute(
            """UPDATE operational_work_reservations SET state='terminal',updated_at=?
                 WHERE work_kind=? AND source_id=? AND state='pending'""",
            (now, work_kind, source_id),
        )
        return updated.rowcount == 1

    def _fence_loop_in_transaction(
        self,
        connection: Any,
        *,
        operation_id: str,
        hop_count: int,
        max_hops: int,
        now: int,
    ) -> None:
        if not operation_id or len(operation_id) > 512:
            raise ValueError("operation loop identifier is invalid")
        if type(hop_count) is not int or hop_count < 0 or hop_count > max_hops:
            self._denied("loop_hold", "operation exceeded its maximum hop count")
        operation_hash = hashlib.sha256(
            ("agentnet.operation.loop.v1\x00" + operation_id).encode("utf-8")
        ).hexdigest()
        connection.execute("DELETE FROM operation_loop_fences WHERE expires_at<=?", (now,))
        row = connection.execute(
            "SELECT highest_hop,max_hops FROM operation_loop_fences WHERE operation_id_hash=?",
            (operation_hash,),
        ).fetchone()
        if row is not None:
            if int(row["max_hops"]) != max_hops or hop_count < int(row["highest_hop"]):
                self._denied("loop_hold", "operation loop fence regressed or changed bounds")
            connection.execute(
                "UPDATE operation_loop_fences SET highest_hop=?,expires_at=?,updated_at=? WHERE operation_id_hash=?",
                (max(hop_count, int(row["highest_hop"])), now + 86_400, now, operation_hash),
            )
            return
        connection.execute(
            """INSERT INTO operation_loop_fences(
                   operation_id_hash,highest_hop,max_hops,expires_at,updated_at
               ) VALUES(?,?,?,?,?)""",
            (operation_hash, hop_count, max_hops, now + 86_400, now),
        )

    def _fair_admission_in_transaction(
        self,
        connection: Any,
        *,
        operation: str,
        actor_scope: str,
        cost: int,
        window: int,
        now: int,
    ) -> int:
        if self.policy is None:
            raise ValueError("fair admission requires an OperationsPolicy")
        scope_hash = self._scope_hash(actor_scope)
        connection.execute(
            "DELETE FROM admission_fairness WHERE window_start<?",
            (window - self.window_seconds * 10,),
        )
        minimum_row = connection.execute(
            "SELECT MIN(virtual_finish) AS minimum FROM admission_fairness WHERE dimension=? AND window_start=?",
            (operation, window),
        ).fetchone()
        current_row = connection.execute(
            """SELECT virtual_finish FROM admission_fairness
                 WHERE dimension=? AND scope_hash=? AND window_start=?""",
            (operation, scope_hash, window),
        ).fetchone()
        minimum = int(minimum_row["minimum"] or 0)
        current = int(current_row["virtual_finish"]) if current_row else minimum
        finish = max(current, minimum) + cost
        if finish > minimum + self.policy.fairness_burst_limit:
            self._denied("fairness_hold", "actor exceeded the persistent fair-share burst")
        connection.execute(
            """INSERT INTO admission_fairness(
                   dimension,scope_hash,window_start,virtual_finish,updated_at
               ) VALUES(?,?,?,?,?)
               ON CONFLICT(dimension,scope_hash,window_start) DO UPDATE SET
               virtual_finish=excluded.virtual_finish,updated_at=excluded.updated_at""",
            (operation, scope_hash, window, finish, now),
        )
        return finish

    def _admit_operation_in_transaction(
        self,
        connection: Any,
        *,
        actor_scope: str,
        domain_scope: str,
        operation: str,
        operation_id: str,
        cost: int = 1,
        pending_cost: int | None = None,
        hop_count: int = 0,
        safety_lane: bool = False,
    ) -> AdmissionDecision:
        """Reserve admission in the caller's mailbox transaction.

        The pending depth is read from authoritative recipient lifecycle rows.
        Callers must add their recipient rows on this same transaction so the
        pressure check and reservation cannot be separated by a TOCTOU race.
        """

        if self.policy is None:
            raise ValueError("admission control requires an OperationsPolicy")
        if type(cost) is not int or cost <= 0:
            raise ValueError("admission cost is invalid")
        if pending_cost is None:
            pending_cost = cost
        if type(pending_cost) is not int or pending_cost < 0:
            raise ValueError("pending admission cost is invalid")
        pending_depth = self._pending_delivery_depth_in_transaction(
            connection,
            domain_scope=domain_scope,
        )
        if pending_depth + pending_cost > self.policy.pending_delivery_backpressure_limit:
            self._denied("backpressure_hold", "pending delivery pressure exceeded the secure ceiling")
        now = self.clock()
        window = now - (now % self.window_seconds)
        breaker_key = self._operation_key(operation, domain_scope)
        dimensions = (
            QuotaDimension(
                scope=f"actor:{actor_scope}",
                metric=f"{operation}_actor",
                amount=cost,
                limit=self.policy.per_actor_requests_per_minute,
            ),
            QuotaDimension(
                scope=f"domain:{domain_scope}",
                metric=f"{operation}_domain",
                amount=cost,
                limit=self.policy.per_domain_requests_per_minute,
            ),
            QuotaDimension(
                scope="global",
                metric=f"{operation}_global",
                amount=cost,
                limit=self.policy.global_requests_per_minute,
            ),
        )
        self._require_breaker_in_transaction(connection, breaker_key=breaker_key, now=now)
        self._fence_loop_in_transaction(
            connection,
            operation_id=operation_id,
            hop_count=hop_count,
            max_hops=self.policy.max_operation_hops,
            now=now,
        )
        self._consume_many_in_transaction(
            connection,
            dimensions,
            window=window,
            safety_lane=safety_lane,
        )
        fair_sequence = self._fair_admission_in_transaction(
            connection,
            operation=operation,
            actor_scope=actor_scope,
            cost=cost,
            window=window,
            now=now,
        )
        return AdmissionDecision(
            admitted=True,
            fair_sequence=fair_sequence,
            window_start=window,
            dimensions=tuple(dimension.metric for dimension in dimensions),
        )

    def admit_operation(
        self,
        *,
        actor_scope: str,
        domain_scope: str,
        operation: str,
        operation_id: str,
        cost: int = 1,
        pending_cost: int | None = None,
        hop_count: int = 0,
        safety_lane: bool = False,
    ) -> AdmissionDecision:
        """Atomically admit against authoritative pending mailbox lifecycle state."""

        try:
            with self.store.transaction() as connection:
                decision = self._admit_operation_in_transaction(
                    connection,
                    actor_scope=actor_scope,
                    domain_scope=domain_scope,
                    operation=operation,
                    operation_id=operation_id,
                    cost=cost,
                    pending_cost=pending_cost,
                    hop_count=hop_count,
                    safety_lane=safety_lane,
                )
                pending_depth = self._pending_delivery_depth_in_transaction(
                    connection,
                    domain_scope=domain_scope,
                )
        except GateBlocked:
            if self.telemetry is not None:
                self.telemetry.increment("quota_result", outcome="denied")
            raise
        if self.telemetry is not None:
            self.telemetry.increment("quota_result", outcome="ok")
            reserved = pending_depth + (cost if pending_cost is None else pending_cost)
            self.telemetry.set_gauge("queue_depth", min(reserved, 1_000_000_000))
        return decision

    def record_failure(self, *, operation: str, domain_scope: str) -> dict[str, Any]:
        if self.policy is None:
            raise ValueError("circuit breaker requires an OperationsPolicy")
        now = self.clock()
        key = self._operation_key(operation, domain_scope)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT failure_count,state FROM circuit_breakers WHERE breaker_key=?",
                (key,),
            ).fetchone()
            failures = int(row["failure_count"]) + 1 if row else 1
            opened = failures >= self.policy.circuit_breaker_failure_threshold
            state = "open" if opened else "closed"
            connection.execute(
                """INSERT INTO circuit_breakers(
                       breaker_key,state,failure_count,opened_at,retry_after,updated_at
                   ) VALUES(?,?,?,?,?,?)
                   ON CONFLICT(breaker_key) DO UPDATE SET
                   state=excluded.state,failure_count=excluded.failure_count,
                   opened_at=excluded.opened_at,retry_after=excluded.retry_after,
                   updated_at=excluded.updated_at""",
                (
                    key,
                    state,
                    failures,
                    now if opened else None,
                    now + self.policy.circuit_breaker_reset_seconds if opened else None,
                    now,
                ),
            )
        if self.telemetry is not None and opened:
            self.telemetry.increment("circuit_breaker", outcome="open")
        return {"state": state, "failure_count": failures}

    def record_success(self, *, operation: str, domain_scope: str) -> None:
        key = self._operation_key(operation, domain_scope)
        with self.store.transaction() as connection:
            self._record_success_in_transaction(connection, breaker_key=key, now=self.clock())
        if self.telemetry is not None:
            self.telemetry.increment("circuit_breaker", outcome="closed")

    @staticmethod
    def _record_success_in_transaction(
        connection: Any,
        *,
        breaker_key: str,
        now: int,
    ) -> None:
        connection.execute(
            """INSERT INTO circuit_breakers(
                   breaker_key,state,failure_count,opened_at,retry_after,updated_at
               ) VALUES(?,'closed',0,NULL,NULL,?)
               ON CONFLICT(breaker_key) DO UPDATE SET
               state='closed',failure_count=0,opened_at=NULL,retry_after=NULL,updated_at=excluded.updated_at""",
            (breaker_key, now),
        )

    def content_free_status(self) -> dict[str, int]:
        now = self.clock()
        open_row = self.store.fetch_one(
            "SELECT COUNT(*) AS count FROM circuit_breakers WHERE state<>'closed'"
        )
        loop_row = self.store.fetch_one(
            "SELECT COUNT(*) AS count FROM operation_loop_fences WHERE expires_at>?",
            (now,),
        )
        return {
            "open_breakers": int(open_row["count"] if open_row else 0),
            "active_loop_fences": int(loop_row["count"] if loop_row else 0),
        }


__all__ = [
    "AdmissionDecision",
    "QuotaDimension",
    "QuotaService",
]
