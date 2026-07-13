"""Privacy-safe fixed-cardinality operational metrics.

The catalog is deliberately closed.  Callers cannot turn identities, resource
names, exception text, or content into a metric label.  Counters, fixed latency
buckets, and bounded gauges are persisted so an ordinary server-agent restart
does not erase outage or pressure evidence.
"""

from __future__ import annotations

from collections import Counter
import time
from threading import Lock

from agentnet.storage.backend import StoreBackend


COUNTER_METRICS = frozenset(
    {
        "adapter_result",
        "audit_check",
        "auth_result",
        "circuit_breaker",
        "config_migration",
        "cost_usage",
        "effect_result",
        "mailbox_accept",
        "operator_request",
        "outage_gate",
        "policy_result",
        "queue_accept",
        "quota_result",
        "relay_accept",
        "scanner_result",
        "unsupported_event",
        "version_rollout",
    }
)
HISTOGRAM_METRICS = frozenset(
    {
        "adapter_latency",
        "auth_latency",
        "mailbox_latency",
        "scanner_latency",
    }
)
GAUGE_METRICS = frozenset(
    {
        "artifact_ready",
        "audit_backlog",
        "audit_valid",
        "queue_depth",
        "storage_ready",
        "unsupported_event_depth",
    }
)
OUTCOMES = frozenset(
    {
        "authority",
        "closed",
        "continuity",
        "degraded",
        "denied",
        "error",
        "held",
        "invalid",
        "ok",
        "open",
        "privileged",
        "queued",
        "rejected",
        "replayed",
        "timeout",
    }
)
LATENCY_BUCKETS_MS = (1, 5, 10, 25, 50, 100, 250, 500, 1_000, 2_500, 5_000, 10_000, 30_000)
MAX_GAUGE_VALUE = 1_000_000_000


class Telemetry:
    def __init__(self, store: StoreBackend | None = None) -> None:
        self.store = store
        self._counts: Counter[tuple[str, str]] = Counter()
        self._lock = Lock()

    def increment(self, metric: str, *, outcome: str = "ok", amount: int = 1) -> None:
        if type(amount) is not int or amount <= 0:
            raise ValueError("telemetry amount must be positive")
        if metric not in COUNTER_METRICS or outcome not in OUTCOMES:
            raise ValueError("telemetry metric/outcome is outside the fixed privacy-safe catalog")
        if self.store is None:
            with self._lock:
                self._counts[(metric, outcome)] += amount
            return
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO telemetry_counters(metric,outcome,count,updated_at) VALUES(?,?,?,?)
                   ON CONFLICT(metric,outcome) DO UPDATE SET
                   count=telemetry_counters.count+excluded.count,updated_at=excluded.updated_at""",
                (metric, outcome, amount, int(time.time())),
            )

    def observe_latency(self, metric: str, milliseconds: int, *, outcome: str = "ok") -> None:
        """Record one latency using cumulative fixed buckets only."""

        if metric not in HISTOGRAM_METRICS or outcome not in OUTCOMES:
            raise ValueError("latency metric/outcome is outside the fixed privacy-safe catalog")
        if type(milliseconds) is not int or milliseconds < 0 or milliseconds > LATENCY_BUCKETS_MS[-1]:
            raise ValueError("latency observation is outside the bounded profile")
        buckets = tuple(bucket for bucket in LATENCY_BUCKETS_MS if milliseconds <= bucket)
        now = int(time.time())
        if self.store is None:
            with self._lock:
                for bucket in buckets:
                    self._counts[(f"{metric}.le_{bucket}", outcome)] += 1
            return
        with self.store.transaction() as connection:
            for bucket in buckets:
                connection.execute(
                    """INSERT INTO telemetry_histograms(
                           metric,outcome,bucket_upper_ms,count,updated_at
                       ) VALUES(?,?,?,?,?)
                       ON CONFLICT(metric,outcome,bucket_upper_ms) DO UPDATE SET
                       count=telemetry_histograms.count+1,updated_at=excluded.updated_at""",
                    (metric, outcome, bucket, 1, now),
                )

    def set_gauge(self, metric: str, value: int) -> None:
        if metric not in GAUGE_METRICS:
            raise ValueError("gauge metric is outside the fixed privacy-safe catalog")
        if type(value) is not int or not 0 <= value <= MAX_GAUGE_VALUE:
            raise ValueError("telemetry gauge is outside the bounded profile")
        if self.store is None:
            with self._lock:
                self._counts[(f"gauge.{metric}", "value")] = value
            return
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO telemetry_gauges(metric,value,updated_at) VALUES(?,?,?)
                   ON CONFLICT(metric) DO UPDATE SET
                   value=excluded.value,updated_at=excluded.updated_at""",
                (metric, value, int(time.time())),
            )

    def record_outage_denial(self, boundary: str) -> None:
        mapping = {
            "authority_outage": "authority",
            "privileged_hold": "privileged",
            "continuity_expired": "continuity",
            "audit_ceiling": "held",
            "dependency_health": "invalid",
        }
        outcome = mapping.get(boundary)
        if outcome is None:
            raise ValueError("outage boundary is outside the fixed catalog")
        self.increment("outage_gate", outcome=outcome)

    def snapshot(self) -> dict[str, int]:
        if self.store is not None:
            rows = self.store.fetch_all("SELECT metric,outcome,count FROM telemetry_counters ORDER BY metric,outcome")
            return {f"{row['metric']}:{row['outcome']}": int(row["count"]) for row in rows}
        with self._lock:
            return {f"{metric}:{outcome}": count for (metric, outcome), count in sorted(self._counts.items())}

    def operational_snapshot(self) -> dict[str, dict[str, int]]:
        """Return content-free counters, latency buckets, and gauges."""

        counters = self.snapshot()
        if self.store is None:
            with self._lock:
                latency_buckets = {
                    f"{metric}:{outcome}": count
                    for (metric, outcome), count in sorted(self._counts.items())
                    if ".le_" in metric
                }
                gauges = {
                    metric.removeprefix("gauge."): count
                    for (metric, outcome), count in sorted(self._counts.items())
                    if metric.startswith("gauge.") and outcome == "value"
                }
            counter_values = {
                key: value
                for key, value in counters.items()
                if ".le_" not in key and not key.startswith("gauge.")
            }
            return {
                "counters": counter_values,
                "latency_buckets": latency_buckets,
                "gauges": gauges,
            }
        histogram_rows = self.store.fetch_all(
            """SELECT metric,outcome,bucket_upper_ms,count
                 FROM telemetry_histograms ORDER BY metric,outcome,bucket_upper_ms"""
        )
        gauge_rows = self.store.fetch_all(
            "SELECT metric,value FROM telemetry_gauges ORDER BY metric"
        )
        return {
            "counters": counters,
            "latency_buckets": {
                f"{row['metric']}:{row['outcome']}:le_{int(row['bucket_upper_ms'])}": int(row["count"])
                for row in histogram_rows
            },
            "gauges": {row["metric"]: int(row["value"]) for row in gauge_rows},
        }


__all__ = [
    "COUNTER_METRICS",
    "GAUGE_METRICS",
    "HISTOGRAM_METRICS",
    "LATENCY_BUCKETS_MS",
    "OUTCOMES",
    "Telemetry",
]
