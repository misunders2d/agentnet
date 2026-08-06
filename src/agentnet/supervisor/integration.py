"""Autonomous durable mailbox-to-clean-worker supervisor integration."""

from __future__ import annotations

import json
import threading
import time
from collections.abc import Callable, Mapping
from dataclasses import asdict
from typing import Any, Protocol

from agentnet.adapters.status import status_indicator
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.supervisor.live_gate import should_wake
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

    def authorize_background(self, obligation_id: str) -> dict[str, Any]: ...

    def acknowledge_custody(
        self,
        obligation_id: str,
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> None: ...

    def upload_result(self, result: Mapping[str, Any]) -> None: ...

    def c0_pilot_respond(self) -> dict[str, str]: ...

    def c0_pilot_status(self) -> dict[str, str]: ...


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
        binding_guard: Callable[[str], object] | None = None,
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
        self._binding_guard = binding_guard
        self._runtimes: dict[str, BackgroundAdapterRuntime] = {}
        self._daemon_stops: dict[str, threading.Event] = {}
        self._daemon_threads: dict[str, threading.Thread] = {}
        self._daemon_errors: dict[str, int] = {}
        self._c0_daemon_stops: dict[str, threading.Event] = {}
        self._c0_daemon_threads: dict[str, threading.Thread] = {}
        self._last_daemon_error_at: dict[str, int] = {}
        self._last_daemon_error_type: dict[str, str] = {}
        self._last_cycle_at: dict[str, int] = {}
        self._cycle_started_at: dict[str, float] = {}
        self._daemon_started_at: dict[str, float] = {}
        self._last_reconciliation_at: dict[str, int] = {}
        self._last_wake_at: dict[str, int] = {}
        self.supervisor.recover()

    def _reload_binding(self, harness_id: str) -> None:
        if self._binding_guard is None:
            return
        try:
            self._binding_guard(harness_id)
        except Exception:
            runtime = self._runtimes.get(harness_id)
            if runtime is not None:
                runtime.stop()
            raise

    def register(self, runtime: BackgroundAdapterRuntime) -> None:
        harness_id = runtime.spec.harness_id
        if harness_id in self._runtimes:
            raise ConflictError("background harness runtime is already registered")
        self._runtimes[harness_id] = runtime
        self._daemon_errors[harness_id] = 0

    def run_c0_pilot_responder_once(self) -> dict[str, str]:
        """Run one deterministic owner response cycle with no worker/runtime path."""

        if self.core_client is None:
            raise ValidationError("C0 pilot responder requires an authenticated corporate client")
        status = self.core_client.c0_pilot_status()
        if status.get("status") == "waiting_owner":
            return self.core_client.c0_pilot_respond()
        return status

    def start_c0_pilot_responder_daemon(self, harness_id: str) -> dict[str, str]:
        """Start no-model C0 responder; never starts or registers a harness runtime."""

        if self.core_client is None:
            raise ValidationError("C0 pilot responder requires an authenticated corporate client")
        current = self._c0_daemon_threads.get(harness_id)
        if current is not None and current.is_alive():
            return {"daemon": "running", "mode": "c0_pilot_responder"}
        stop = threading.Event()
        self._c0_daemon_stops[harness_id] = stop
        thread = threading.Thread(
            target=self._c0_pilot_responder_loop,
            args=(harness_id, stop),
            name=f"agentnet-c0-responder-{harness_id[:24]}",
            daemon=True,
        )
        self._c0_daemon_threads[harness_id] = thread
        thread.start()
        return {"daemon": "running", "mode": "c0_pilot_responder"}

    def _c0_pilot_responder_loop(self, harness_id: str, stop: threading.Event) -> None:
        delay = self.reconnect_initial_seconds
        while not stop.is_set():
            try:
                result = self.run_c0_pilot_responder_once()
                self._last_cycle_at[harness_id] = int(time.time())
                self._daemon_errors[harness_id] = 0
                delay = self.reconnect_initial_seconds
                if result.get("status") in {
                    "waiting_fresh",
                    "COMPLETED_C0_ROUND_TRIP",
                    "expired",
                    "invalidated",
                }:
                    return
                stop.wait(self.watch_wait_seconds)
            except Exception as exc:
                self._daemon_errors[harness_id] = self._daemon_errors.get(harness_id, 0) + 1
                self._last_daemon_error_at[harness_id] = int(time.time())
                self._last_daemon_error_type[harness_id] = type(exc).__name__
                stop.wait(delay)
                delay = min(self.reconnect_max_seconds, delay * 2)

    def start(self, harness_id: str) -> dict[str, Any]:
        return asdict(self._runtime(harness_id).start())

    def stop(self, harness_id: str) -> None:
        c0_stop = self._c0_daemon_stops.get(harness_id)
        if c0_stop is not None:
            c0_stop.set()
        c0_thread = self._c0_daemon_threads.get(harness_id)
        if c0_thread is not None and c0_thread is not threading.current_thread():
            c0_thread.join(timeout=self.watch_wait_seconds + 3.0)
            if c0_thread.is_alive():
                raise ConflictError("C0 pilot responder did not stop within its bound")
        stop = self._daemon_stops.get(harness_id)
        if stop is not None:
            stop.set()
        thread = self._daemon_threads.get(harness_id)
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout=self.watch_wait_seconds + 3.0)
            if thread.is_alive():
                raise ConflictError("authenticated mailbox watch did not stop within its bound")
        runtime = self._runtimes.get(harness_id)
        if runtime is not None:
            runtime.stop()

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
        """Reconcile content-free obligation wakes, then authorize at launch."""

        if self.core_client is None:
            raise ValidationError("autonomous supervisor requires an authenticated corporate client")
        if not 1 <= limit <= 100:
            raise ValidationError("autonomous supervisor limit is invalid")
        runtime = self._runtime(harness_id)
        runtime.require_ready()
        after_cursor = self.supervisor.local_queue.cursor(harness_id)
        obligations_reconciled = 0
        fetched: list[dict[str, Any]] = []
        if reconcile_remote:
            obligation_result = self.core_client.reconcile_obligations(limit=limit)
            obligations_reconciled = sum(len(items) for items in obligation_result.values())
            self.supervisor.update_obligation_status(
                harness_id,
                self.core_client.obligation_inbox(),
            )
            fetched = self.core_client.reconcile(after_cursor=after_cursor, limit=limit)

        enqueued = 0
        highest_cursor = after_cursor
        for item in fetched:
            _event_id, cursor, _envelope_digest = self._mailbox_item(item)
            highest_cursor = max(highest_cursor, cursor)
            if not should_wake(item, endpoint_harness_id=harness_id):
                continue
            reference = item["response_obligation"]
            obligation_id = str(reference["obligation_id"])
            result = self.supervisor.queue_background_obligation(
                harness_id=harness_id,
                obligation_id=obligation_id,
            )
            if not result["duplicate"]:
                enqueued += 1
        if highest_cursor > after_cursor:
            self.supervisor.advance_mailbox_cursor(
                harness_id=harness_id,
                cursor=highest_cursor,
            )

        dispatched = 0
        self._reload_binding(harness_id)
        if runtime.spec.semantic_mode == "clean_worker":
            claimed = self.supervisor.local_queue.claim(
                harness_id=harness_id,
                direction="inbox",
                limit=limit,
            )
            for queued in claimed:
                try:
                    wake = queued["payload"]
                    if (
                        not isinstance(wake, dict)
                        or set(wake) != {"schema", "obligation_id"}
                        or wake["schema"] != "agentnet.background-obligation.v1"
                        or not isinstance(wake["obligation_id"], str)
                        or not wake["obligation_id"]
                    ):
                        raise AuthorizationError("background obligation queue binding is invalid")
                    obligation_id = wake["obligation_id"]
                    self._reload_binding(harness_id)
                    authorization = BackgroundTurnAuthorization.from_mapping(
                        self.core_client.authorize_background(obligation_id)
                    )
                    if authorization.harness_id != harness_id:
                        raise AuthorizationError(
                            "background eligibility crossed its exact harness binding"
                        )
                    self.core_client.acknowledge_custody(
                        obligation_id,
                        authorization,
                        local_queue_id=queued["queue_id"],
                    )
                    self._reload_binding(harness_id)
                    response = runtime.submit_background(
                        json.dumps(
                            {
                                "authorization": asdict(authorization),
                                "obligation_id": obligation_id,
                            },
                            allow_nan=False,
                            separators=(",", ":"),
                            sort_keys=True,
                        ),
                        authorization=authorization,
                    )
                    self._reload_binding(harness_id)
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
        self._reload_binding(harness_id)
        for queued in self.supervisor.local_queue.claim(
            harness_id=harness_id,
            direction="outbox",
            limit=limit,
        ):
            try:
                self.core_client.upload_result(queued["payload"])
                self._reload_binding(harness_id)
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
        self._reload_binding(harness_id)
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
                    self._reload_binding(harness_id)
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
        """Recover queue custody only; a harness requires an explicit restart."""

        return self.supervisor.recover()

    def close(self) -> None:
        failures: list[Exception] = []
        for harness_id in sorted(set(self._runtimes) | set(self._c0_daemon_threads)):
            try:
                self.stop(harness_id)
            except Exception as exc:
                failures.append(exc)
                runtime = self._runtimes.get(harness_id)
                if runtime is not None:
                    try:
                        runtime.stop()
                    except Exception as runtime_exc:
                        failures.append(runtime_exc)
        self._runtimes.clear()
        self._daemon_stops.clear()
        self._daemon_threads.clear()
        self._daemon_errors.clear()
        self._c0_daemon_stops.clear()
        self._c0_daemon_threads.clear()
        self._last_daemon_error_at.clear()
        self._last_daemon_error_type.clear()
        self._last_cycle_at.clear()
        self._cycle_started_at.clear()
        self._daemon_started_at.clear()
        self._last_reconciliation_at.clear()
        self._last_wake_at.clear()
        if failures:
            raise failures[0]

    def _runtime(self, harness_id: str) -> BackgroundAdapterRuntime:
        try:
            return self._runtimes[harness_id]
        except KeyError as exc:
            raise ValidationError("background harness runtime is not registered") from exc


__all__ = ["BackgroundHarnessIntegration", "SupervisorCoreClient"]
