"""Autonomous durable mailbox-to-clean-worker supervisor integration."""

from __future__ import annotations

import json
import threading
import time
from collections.abc import Mapping
from dataclasses import asdict
from typing import Any, Protocol

from agentnet.adapters.status import status_indicator
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.supervisor.runtime import (
    BackgroundAdapterRuntime,
    BackgroundTurnAuthorization,
)
from agentnet.supervisor.service import DeviceSupervisor


class SupervisorCoreClient(Protocol):
    """Authenticated corporate API surface required by the local daemon."""

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool: ...

    def reconcile(self, *, after_cursor: int, limit: int) -> list[dict[str, Any]]: ...

    def reconcile_obligations(self, *, limit: int) -> dict[str, list[str]]: ...

    def obligation_inbox(self) -> dict[str, int]: ...

    def authorize_background(self, item: Mapping[str, Any]) -> dict[str, Any]: ...

    def acknowledge_custody(
        self,
        item: Mapping[str, Any],
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> None: ...

    def release_task_payload(
        self,
        item: Mapping[str, Any],
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> dict[str, Any]: ...

    def upload_result(self, result: Mapping[str, Any]) -> None: ...


class BackgroundHarnessIntegration:
    """Join corporate mailboxes to separately managed background processes.

    Remote cursor advancement follows durable local enqueue.  Only a fresh
    server-side eligibility decision can trigger a semantic worker.  Manual
    explicit-open remains available for human inspection and deterministic
    adapters, but is not the cooperation path.
    """

    def __init__(
        self,
        supervisor: DeviceSupervisor,
        *,
        core_client: SupervisorCoreClient | None = None,
        watch_wait_seconds: float = 5.0,
        reconciliation_interval_seconds: float = 30.0,
        reconnect_initial_seconds: float = 0.25,
        reconnect_max_seconds: float = 5.0,
    ) -> None:
        if not 0.05 <= watch_wait_seconds <= 30:
            raise ValueError("supervisor watch wait is outside the bounded profile")
        if not 0.05 <= reconciliation_interval_seconds <= 300:
            raise ValueError("supervisor reconciliation interval is outside the bounded profile")
        if not 0.01 <= reconnect_initial_seconds <= 5:
            raise ValueError("supervisor reconnect initial delay is outside the bounded profile")
        if not reconnect_initial_seconds <= reconnect_max_seconds <= 30:
            raise ValueError("supervisor reconnect ceiling is outside the bounded profile")
        self.supervisor = supervisor
        self.core_client = core_client
        self.watch_wait_seconds = watch_wait_seconds
        self.reconciliation_interval_seconds = reconciliation_interval_seconds
        self.reconnect_initial_seconds = reconnect_initial_seconds
        self.reconnect_max_seconds = reconnect_max_seconds
        self._runtimes: dict[str, BackgroundAdapterRuntime] = {}
        self._daemon_stops: dict[str, threading.Event] = {}
        self._daemon_threads: dict[str, threading.Thread] = {}
        self._daemon_errors: dict[str, int] = {}
        self._last_daemon_error_at: dict[str, int] = {}
        self._last_daemon_error_type: dict[str, str] = {}
        self._last_cycle_at: dict[str, int] = {}
        self._cycle_started_at: dict[str, float] = {}
        self._daemon_started_at: dict[str, float] = {}
        self._last_reconciliation_at: dict[str, int] = {}
        self._last_wake_at: dict[str, int] = {}
        self.supervisor.recover()

    def register(self, runtime: BackgroundAdapterRuntime) -> None:
        harness_id = runtime.spec.harness_id
        if harness_id in self._runtimes:
            raise ConflictError("background harness runtime is already registered")
        self._runtimes[harness_id] = runtime
        self._daemon_errors[harness_id] = 0

    def start(self, harness_id: str) -> dict[str, Any]:
        return asdict(self._runtime(harness_id).start())

    def stop(self, harness_id: str) -> None:
        stop = self._daemon_stops.get(harness_id)
        if stop is not None:
            stop.set()
        thread = self._daemon_threads.get(harness_id)
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout=self.watch_wait_seconds + 3.0)
            if thread.is_alive():
                raise ConflictError("authenticated mailbox watch did not stop within its bound")
        self._runtime(harness_id).stop()

    def start_daemon(self, harness_id: str) -> dict[str, Any]:
        if self.core_client is None:
            raise ValidationError("autonomous supervisor requires an authenticated corporate client")
        runtime_status = self.start(harness_id)
        current = self._daemon_threads.get(harness_id)
        if current is not None and current.is_alive():
            return {"daemon": "running", "runtime": runtime_status}
        stop = threading.Event()
        self._daemon_stops[harness_id] = stop
        thread = threading.Thread(
            target=self._daemon_loop,
            args=(harness_id, stop),
            name=f"agentnet-mailbox-daemon-{harness_id[:24]}",
            daemon=True,
        )
        self._daemon_threads[harness_id] = thread
        self._daemon_started_at[harness_id] = time.time()
        thread.start()
        return {"daemon": "running", "runtime": runtime_status}

    def _daemon_loop(self, harness_id: str, stop: threading.Event) -> None:
        initial_reconciliation_complete = False
        last_reconciliation_monotonic = 0.0
        reconnect_attempt = 0
        while not stop.is_set():
            self._cycle_started_at[harness_id] = time.time()
            retry_delay = 0.0
            try:
                reconcile_remote = not initial_reconciliation_complete
                if initial_reconciliation_complete:
                    after_cursor = self.supervisor.local_queue.cursor(harness_id)
                    woke = self.core_client.watch(
                        after_cursor=after_cursor,
                        wait_seconds=self.watch_wait_seconds,
                    )
                    if woke:
                        self._last_wake_at[harness_id] = int(time.time())
                    reconcile_remote = woke or (
                        time.monotonic() - last_reconciliation_monotonic
                        >= self.reconciliation_interval_seconds
                    )
                self.run_once(harness_id, reconcile_remote=reconcile_remote)
                if reconcile_remote:
                    initial_reconciliation_complete = True
                    last_reconciliation_monotonic = time.monotonic()
                    self._last_reconciliation_at[harness_id] = int(time.time())
            except Exception as exc:
                self._daemon_errors[harness_id] = self._daemon_errors.get(harness_id, 0) + 1
                self._last_daemon_error_at[harness_id] = int(time.time())
                self._last_daemon_error_type[harness_id] = type(exc).__name__
                retry_delay = min(
                    self.reconnect_max_seconds,
                    self.reconnect_initial_seconds * (2**min(reconnect_attempt, 16)),
                )
                reconnect_attempt += 1
            else:
                # This is a consecutive-error fence.  A recovered cycle clears
                # it while the last content-free error metadata remains visible.
                self._daemon_errors[harness_id] = 0
                reconnect_attempt = 0
            self._last_cycle_at[harness_id] = int(time.time())
            self._cycle_started_at.pop(harness_id, None)
            if retry_delay:
                stop.wait(retry_delay)

    @staticmethod
    def _mailbox_item(item: Mapping[str, Any]) -> tuple[str, int, str]:
        event = item.get("event")
        cursor = item.get("cursor")
        envelope_digest = item.get("envelope_digest")
        if (
            not isinstance(event, dict)
            or not isinstance(event.get("event_id"), str)
            or type(cursor) is not int
            or cursor < 0
            or not isinstance(envelope_digest, str)
            or len(envelope_digest) != 64
        ):
            raise AuthorizationError("corporate mailbox item binding is invalid")
        if item.get("fact") in {
            "pending_human",
            "authorization_hold",
            "rejected_before_accept",
            "conflict_pending",
        }:
            raise AuthorizationError("non-executable custody cannot enter a background worker")
        if event.get("event_type") == "task_assignment":
            reference = item.get("custody_reference")
            if (
                item.get("payload") is not None
                or item.get("payload_available") is not False
                or item.get("payload_access") != "task_grant_required"
                or item.get("payload_withheld_reason") != "exact_task_grant_required"
                or not isinstance(reference, dict)
                or set(reference)
                != {
                    "schema",
                    "event_id",
                    "payload_digest",
                    "envelope_digest",
                    "payload_access",
                }
                or reference.get("schema") != "agentnet.custody-payload-reference.v1"
                or reference.get("event_id") != event["event_id"]
                or reference.get("envelope_digest") != envelope_digest
                or reference.get("payload_access") != "task_grant_required"
                or not isinstance(reference.get("payload_digest"), str)
                or len(reference["payload_digest"]) != 64
            ):
                raise AuthorizationError("protected task custody reference is invalid")
        elif item.get("payload_available") is not True or not isinstance(
            item.get("payload"), dict
        ):
            raise AuthorizationError("corporate mailbox item payload is unavailable")
        return event["event_id"], cursor, envelope_digest

    def receive_from_core(self, *, harness_id: str, event: dict[str, Any], cursor: int) -> dict[str, Any]:
        """Manual/test custody path; never triggers a harness by itself."""

        if harness_id not in self._runtimes:
            raise ValidationError("background harness runtime is not registered")
        return self.supervisor.receive_from_core(harness_id=harness_id, event=event, cursor=cursor)

    def run_once(
        self,
        harness_id: str,
        *,
        limit: int = 25,
        reconcile_remote: bool = True,
    ) -> dict[str, int]:
        """Reconcile when requested, then dispatch durable eligible local work."""

        if self.core_client is None:
            raise ValidationError("autonomous supervisor requires an authenticated corporate client")
        if not 1 <= limit <= 100:
            raise ValidationError("autonomous supervisor limit is invalid")
        runtime = self._runtime(harness_id)
        runtime.require_ready()
        after_cursor = self.supervisor.local_queue.cursor(harness_id)
        fetched = (
            self.core_client.reconcile(after_cursor=after_cursor, limit=limit)
            if reconcile_remote
            else []
        )
        obligations_reconciled = 0
        if reconcile_remote:
            obligation_result = self.core_client.reconcile_obligations(limit=limit)
            obligations_reconciled = sum(len(items) for items in obligation_result.values())
            self.supervisor.update_obligation_status(
                harness_id,
                self.core_client.obligation_inbox(),
            )
        enqueued = 0
        for item in fetched:
            event_id, cursor, envelope_digest = self._mailbox_item(item)
            authorization = BackgroundTurnAuthorization.from_mapping(
                self.core_client.authorize_background(item)
            )
            if (
                authorization.harness_id != harness_id
                or authorization.event_id != event_id
                or authorization.envelope_digest != envelope_digest
            ):
                raise AuthorizationError("background eligibility crossed its exact mailbox binding")
            stored = {
                "authorization": asdict(authorization),
                "mailbox_item": dict(item),
            }
            result = self.supervisor.receive_from_core(
                harness_id=harness_id,
                event={"event": {"event_id": event_id}, "eligible_delivery": stored},
                cursor=cursor,
            )
            if not result["duplicate"]:
                enqueued += 1

        dispatched = 0
        if runtime.spec.semantic_mode == "clean_worker":
            claimed = self.supervisor.local_queue.claim(
                harness_id=harness_id,
                direction="inbox",
                limit=limit,
            )
            for queued in claimed:
                try:
                    delivery = queued["payload"]["eligible_delivery"]
                    item = delivery["mailbox_item"]
                    authorization = BackgroundTurnAuthorization.from_mapping(
                        delivery["authorization"]
                    )
                    self.core_client.acknowledge_custody(
                        item,
                        authorization,
                        local_queue_id=queued["queue_id"],
                    )
                    released = self.core_client.release_task_payload(
                        item,
                        authorization,
                        local_queue_id=queued["queue_id"],
                    )
                    response = runtime.submit_background(
                        json.dumps(
                            {
                                "authority": {
                                    "effect_authorized": released["effect_authorized"],
                                    "payload_access_authorized": released[
                                        "payload_access_authorized"
                                    ],
                                    "release_receipt_id": released["release_receipt_id"],
                                    "semantic_processing_authorized": released[
                                        "semantic_processing_authorized"
                                    ],
                                    "tool_authorized": released["tool_authorized"],
                                },
                                "event": item["event"],
                                "intent": released["intent"],
                                "payload": released["payload"],
                                "provenance": released["provenance"],
                                "task_grant_id": authorization.task_grant_id,
                            },
                            allow_nan=False,
                            separators=(",", ":"),
                            sort_keys=True,
                        ),
                        authorization=authorization,
                    )
                    self.supervisor.acknowledge_with_local_output(
                        harness_id=harness_id,
                        source_queue_id=queued["queue_id"],
                        request={
                            "authorization": asdict(authorization),
                            "native_result": response,
                            "source_queue_id": queued["queue_id"],
                        },
                        idempotency_key=f"native-output:{queued['queue_id']}",
                    )
                    dispatched += 1
                except Exception:
                    self.supervisor.local_queue.retry(queued["queue_id"], delay_seconds=1)
                    raise

        uploaded = 0
        for queued in self.supervisor.local_queue.claim(
            harness_id=harness_id,
            direction="outbox",
            limit=limit,
        ):
            try:
                self.core_client.upload_result(queued["payload"])
                self.supervisor.local_queue.complete(queued["queue_id"])
                uploaded += 1
            except Exception:
                self.supervisor.local_queue.retry(queued["queue_id"], delay_seconds=1)
                raise
        self._last_cycle_at[harness_id] = int(time.time())
        return {
            "fetched": len(fetched),
            "enqueued": enqueued,
            "dispatched": dispatched,
            "uploaded": uploaded,
            "obligations_reconciled": obligations_reconciled,
        }

    def explicit_pull(self, harness_id: str, *, limit: int = 25) -> list[dict[str, Any]]:
        """Human-initiated inspection path, separate from autonomous dispatch."""

        runtime = self._runtime(harness_id)
        runtime.require_ready()
        claimed = self.supervisor.explicit_open(harness_id, limit=limit)
        results: list[dict[str, Any]] = []
        for item in claimed:
            try:
                if runtime.spec.semantic_mode == "deterministic_only":
                    result = {
                        "queue_id": item["queue_id"],
                        "disposition": "explicit_human_open",
                        "payload": item["payload"],
                    }
                else:
                    response = runtime.submit(
                        json.dumps(
                            {"queue_id": item["queue_id"], "payload": item["payload"]},
                            allow_nan=False,
                            separators=(",", ":"),
                            sort_keys=True,
                        ),
                        explicit=True,
                    )
                    durable_output = self.supervisor.acknowledge_with_local_output(
                        harness_id=harness_id,
                        source_queue_id=item["queue_id"],
                        request={"source_queue_id": item["queue_id"], "native_result": response},
                        idempotency_key=f"native-output:{item['queue_id']}",
                    )
                    result = {
                        "queue_id": item["queue_id"],
                        "disposition": "clean_worker_response",
                        "output_queue_id": durable_output["queue_id"],
                    }
                if runtime.spec.semantic_mode == "deterministic_only":
                    self.supervisor.local_queue.complete(item["queue_id"])
                results.append(result)
            except Exception:
                self.supervisor.local_queue.retry(item["queue_id"], delay_seconds=0)
                raise
        return results

    def passive_status(self, harness_id: str) -> dict[str, Any]:
        runtime = self._runtime(harness_id)
        thread = self._daemon_threads.get(harness_id)
        counts = self.supervisor.passive_status(harness_id)
        queue_counts = {
            key: value for key, value in counts.items() if not key.startswith("obligation_")
        }
        obligation_counts = [
            value for key, value in counts.items() if key.startswith("obligation_")
        ]
        activity_counts = {
            **queue_counts,
            # Obligation counters deliberately overlap (for example, an item can
            # be both action-required and overdue).  Use their maximum as the
            # content-free attention floor instead of inflating the indicator.
            "obligation_attention": max(obligation_counts, default=0),
        }
        return {
            "activity": status_indicator(activity_counts),
            "obligations": {
                key.removeprefix("obligation_"): value
                for key, value in counts.items()
                if key.startswith("obligation_")
            },
            "daemon": {
                "errors": self._daemon_errors.get(harness_id, 0),
                "last_error_at": self._last_daemon_error_at.get(harness_id),
                "last_error_type": self._last_daemon_error_type.get(harness_id),
                "last_cycle_at": self._last_cycle_at.get(harness_id),
                "cycle_started_at": self._cycle_started_at.get(harness_id),
                "daemon_started_at": self._daemon_started_at.get(harness_id),
                "delivery_mode": "authenticated_watch_with_cursor_reconciliation",
                "last_reconciliation_at": self._last_reconciliation_at.get(harness_id),
                "last_wake_at": self._last_wake_at.get(harness_id),
                "running": bool(thread and thread.is_alive()),
            },
            "runtime": runtime.content_free_status(),
        }

    def recover(self) -> int:
        recovered = self.supervisor.recover()
        for runtime in self._runtimes.values():
            if runtime.status().phase in {"offline", "degraded"}:
                try:
                    runtime.start()
                except Exception:
                    continue
        return recovered

    def close(self) -> None:
        for harness_id in tuple(self._runtimes):
            self.stop(harness_id)

    def _runtime(self, harness_id: str) -> BackgroundAdapterRuntime:
        try:
            return self._runtimes[harness_id]
        except KeyError as exc:
            raise ValidationError("background harness runtime is not registered") from exc


__all__ = ["BackgroundHarnessIntegration", "SupervisorCoreClient"]
