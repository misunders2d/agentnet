from __future__ import annotations

from pathlib import Path

import pytest

from agentnet.adapters.status import status_indicator
from agentnet.errors import ConflictError, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.service import DeviceSupervisor


def test_routine_receive_never_returns_content_to_status(tmp_path: Path) -> None:
    cipher = LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key")
    supervisor = DeviceSupervisor(LocalQueue(tmp_path / "queue.sqlite3", cipher))
    event = {"event": {"event_id": "e1"}, "payload": {"secret": "canary"}}
    supervisor.receive_from_core(harness_id="h1", event=event, cursor=1)
    status = status_indicator(supervisor.passive_status("h1"))
    assert status == {"kind": "agentnet_count", "count": 1}
    assert "canary" not in repr(status)
    opened = supervisor.explicit_open("h1")
    assert opened[0]["payload"] == event


def test_recovery_requeues_processing(tmp_path: Path) -> None:
    queue = LocalQueue(tmp_path / "queue.sqlite3", LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key"))
    queue.enqueue(harness_id="h1", direction="inbox", idempotency_key="key-1234567890123456", payload={"x": 1})
    assert queue.claim(harness_id="h1", direction="inbox")
    assert queue.recover_processing() == 1
    assert queue.claim(harness_id="h1", direction="inbox")


def test_queue_transitions_fail_closed_and_cursor_never_rolls_back(tmp_path: Path) -> None:
    queue = LocalQueue(tmp_path / "queue.sqlite3", LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key"))
    queued = queue.enqueue(
        harness_id="h1",
        direction="outbox",
        idempotency_key="key-1234567890123456",
        payload={"synthetic": True},
    )
    with pytest.raises(ConflictError):
        queue.complete(queued["queue_id"])
    claimed = queue.claim(harness_id="h1", direction="outbox")
    queue.complete(claimed[0]["queue_id"])
    with pytest.raises(ConflictError):
        queue.retry(claimed[0]["queue_id"], delay_seconds=0)
    queue.set_cursor("h1", 5)
    queue.set_cursor("h1", 3)
    assert queue.cursor("h1") == 5
    with pytest.raises(ValidationError):
        queue.claim(harness_id="h1", direction="inbox", limit=0)


def test_native_output_and_inbox_ack_are_one_durable_transaction(tmp_path: Path) -> None:
    queue = LocalQueue(
        tmp_path / "queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key"),
    )
    inbox = queue.enqueue(
        harness_id="h1",
        direction="inbox",
        idempotency_key="input-1234567890123456",
        payload={"native": "input"},
    )
    with pytest.raises(ConflictError, match="claimed inbox"):
        queue.complete_with_outbox(
            queue_id=inbox["queue_id"],
            harness_id="h1",
            idempotency_key="output-1234567890123456",
            payload={"native": "output"},
        )
    assert queue.claim(harness_id="h1", direction="outbox") == []

    claimed = queue.claim(harness_id="h1", direction="inbox")
    outbox = queue.complete_with_outbox(
        queue_id=claimed[0]["queue_id"],
        harness_id="h1",
        idempotency_key="output-1234567890123456",
        payload={"native": "output"},
    )
    assert queue.content_free_counts("h1") == {}
    opened = queue.claim(harness_id="h1", direction="outbox")
    assert opened == [
        {"queue_id": outbox["queue_id"], "payload": {"native": "output"}, "attempt": 1}
    ]
