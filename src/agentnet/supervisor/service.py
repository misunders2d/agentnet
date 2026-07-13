"""Per-device supervisor orchestration without foreground injection."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from agentnet.supervisor.queue import LocalQueue


class DeviceSupervisor:
    def __init__(self, local_queue: LocalQueue) -> None:
        self.local_queue = local_queue

    def receive_from_core(self, *, harness_id: str, event: dict[str, Any], cursor: int) -> dict[str, Any]:
        return self.local_queue.enqueue_inbox_with_cursor(
            harness_id=harness_id,
            idempotency_key=f"core-delivery:{event['event']['event_id']}:{cursor}",
            payload=event,
            cursor=cursor,
        )

    def submit_local(self, *, harness_id: str, request: dict[str, Any], idempotency_key: str) -> dict[str, Any]:
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
        return self.local_queue.complete_with_outbox(
            queue_id=source_queue_id,
            harness_id=harness_id,
            idempotency_key=idempotency_key,
            payload=request,
        )

    def explicit_open(self, harness_id: str, *, limit: int = 25) -> list[dict[str, Any]]:
        """Only a human-initiated call returns message content."""
        return self.local_queue.claim(harness_id=harness_id, direction="inbox", limit=limit)

    def passive_status(self, harness_id: str) -> dict[str, int]:
        """Content-free, noninteractive counts only."""
        return self.local_queue.content_free_counts(harness_id)

    def recover(self) -> int:
        return self.local_queue.recover_processing()
