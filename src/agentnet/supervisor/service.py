"""Per-device supervisor orchestration without foreground injection."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from agentnet.supervisor.queue import LocalQueue


class DeviceSupervisor:
    def __init__(
        self,
        local_queue: LocalQueue,
        *,
        harness_id: str | None = None,
        binding_guard: Callable[[str], object] | None = None,
    ) -> None:
        if harness_id is not None and not harness_id:
            raise ValueError("device supervisor exact harness binding is invalid")
        self.local_queue = local_queue
        self._harness_id = harness_id
        self._binding_guard = binding_guard

    def _require_harness(self, harness_id: str, *, reload_binding: bool = False) -> None:
        if self._harness_id is not None and harness_id != self._harness_id:
            raise ValueError("device supervisor crossed its exact harness binding")
        if reload_binding and self._binding_guard is not None:
            self._binding_guard(harness_id)

    def receive_from_core(self, *, harness_id: str, event: dict[str, Any], cursor: int) -> dict[str, Any]:
        self._require_harness(harness_id)
        return self.local_queue.enqueue_inbox_with_cursor(
            harness_id=harness_id,
            idempotency_key=f"core-delivery:{event['event']['event_id']}:{cursor}",
            payload=event,
            cursor=cursor,
        )
    def queue_background_obligation(
        self,
        *,
        harness_id: str,
        obligation_id: str,
    ) -> dict[str, Any]:
        """Persist only one exact content-free obligation wake reference."""

        self._require_harness(harness_id)
        if not obligation_id or len(obligation_id) > 256:
            raise ValueError("background obligation identifier is invalid")
        return self.local_queue.enqueue(
            harness_id=harness_id,
            direction="inbox",
            idempotency_key=f"background-obligation:{obligation_id}",
            payload={
                "schema": "agentnet.background-obligation.v1",
                "obligation_id": obligation_id,
            },
        )

    def advance_mailbox_cursor(self, *, harness_id: str, cursor: int) -> None:
        """Advance only after every wake reference through the cursor is durable."""

        self._require_harness(harness_id)
        self.local_queue.set_cursor(harness_id, cursor)


    def submit_local(self, *, harness_id: str, request: dict[str, Any], idempotency_key: str) -> dict[str, Any]:
        self._require_harness(harness_id)
        return self.local_queue.enqueue(
            harness_id=harness_id,
            direction="outbox",
            idempotency_key=idempotency_key,
            payload=request,
        )

    def acknowledge_with_local_output(
        self,
        *,
        harness_id: str,
        source_queue_id: str,
        request: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._require_harness(harness_id, reload_binding=True)
        return self.local_queue.complete_with_outbox(
            queue_id=source_queue_id,
            harness_id=harness_id,
            idempotency_key=idempotency_key,
            payload=request,
        )

    def explicit_open(self, harness_id: str, *, limit: int = 25) -> list[dict[str, Any]]:
        """Only a human-initiated call returns message content."""
        self._require_harness(harness_id, reload_binding=True)
        return self.local_queue.claim(harness_id=harness_id, direction="inbox", limit=limit)

    def update_obligation_status(self, harness_id: str, counters: dict[str, int]) -> None:
        self._require_harness(harness_id)
        self.local_queue.store_obligation_snapshot(
            harness_id=harness_id,
            counters=counters,
        )

    def passive_status(self, harness_id: str) -> dict[str, int]:
        """Content-free, noninteractive counts only."""
        self._require_harness(harness_id)
        queue_counts = self.local_queue.content_free_counts(harness_id)
        obligation_counts = self.local_queue.obligation_snapshot(harness_id)
        return queue_counts | {
            f"obligation_{key}": value for key, value in obligation_counts.items()
        }

    def recover(self) -> int:
        return self.local_queue.recover_processing()
