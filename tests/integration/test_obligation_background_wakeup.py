from __future__ import annotations

import json
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.service import DeviceSupervisor


@dataclass(frozen=True, slots=True)
class _RuntimeSpec:
    harness_id: str
    semantic_mode: str


class _Runtime:
    def __init__(self, harness_id: str, *, semantic_mode: str = "clean_worker") -> None:
        self.spec = _RuntimeSpec(harness_id=harness_id, semantic_mode=semantic_mode)
        self.prompts: list[dict[str, Any]] = []

    def require_ready(self) -> None:
        return None

    def submit_background(self, prompt: str, *, authorization: object) -> dict[str, Any]:
        parsed = json.loads(prompt)
        self.prompts.append(parsed)
        return {
            "native_session_id": f"background-{self.spec.harness_id}",
            "output": "processed through the isolated background session",
            "terminal_event": "completed",
        }

    def stop(self) -> None:
        return None

    def content_free_status(self) -> dict[str, Any]:
        return {"phase": "ready"}


class _CoreClient:
    def __init__(self, harness_id: str, items: list[dict[str, Any]]) -> None:
        self.harness_id = harness_id
        self.items = items
        self.authorized: list[str] = []
        self.acknowledged: list[tuple[str, str]] = []
        self.uploaded: list[dict[str, Any]] = []

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        return any(item["cursor"] > after_cursor for item in self.items)

    def reconcile(self, *, after_cursor: int, limit: int) -> list[dict[str, Any]]:
        return [item for item in self.items if item["cursor"] > after_cursor][:limit]

    def reconcile_obligations(self, *, limit: int) -> dict[str, list[str]]:
        return {"recipient_committed": [], "expired": []}

    def obligation_inbox(self) -> dict[str, int]:
        return {
            "unread_information": 1,
            "action_required": 1,
            "awaiting_peer": 0,
            "awaiting_human": 0,
            "overdue": 0,
            "failed": 0,
        }

    def authorize_background(self, obligation_id: str) -> dict[str, Any]:
        self.authorized.append(obligation_id)
        return {
            "decision_id": f"decision-{obligation_id}",
            "harness_id": self.harness_id,
            "event_id": "event-request",
            "envelope_digest": "b" * 64,
            "event_type": "message",
            "classification": "C1",
            "policy_revision": 1,
            "expires_at": int(time.time()) + 60,
            "task_grant_id": "grant-request",
        }

    def acknowledge_custody(
        self,
        obligation_id: str,
        authorization: object,
        *,
        local_queue_id: str,
    ) -> None:
        self.acknowledged.append((obligation_id, local_queue_id))

    def upload_result(self, result: dict[str, Any]) -> None:
        self.uploaded.append(dict(result))

    def c0_pilot_respond(self) -> dict[str, str]:
        raise AssertionError("not used")

    def c0_pilot_status(self) -> dict[str, str]:
        raise AssertionError("not used")


def _mailbox_items(responsible_harness_id: str) -> list[dict[str, Any]]:
    """One quiet message, one non-task request, and one task assignment.

    Only the task assignment can complete the corporate worker lifecycle, so
    only it may wake a semantic worker.  The ordinary request stays durable
    and passively counted for the recipient's own authorized session.
    """

    return [
        {
            "cursor": 1,
            "envelope_digest": "a" * 64,
            "event": {"event_id": "event-information", "event_type": "message"},
            "fact": "accepted_local",
            "payload": {"kind": "post", "body": "quiet information"},
            "payload_available": True,
            "response_obligation": None,
        },
        {
            "cursor": 2,
            "envelope_digest": "b" * 64,
            "event": {"event_id": "event-request", "event_type": "message"},
            "fact": "recipient_committed",
            "payload": {"kind": "structured_request", "arguments": {"secret": "not queued"}},
            "payload_available": True,
            "response_obligation": {
                "obligation_id": "obligation-request",
                "responsible_harness_id": responsible_harness_id,
                "state": "created",
            },
        },
        {
            "cursor": 3,
            "envelope_digest": "c" * 64,
            "event": {"event_id": "event-task", "event_type": "task_assignment"},
            "fact": "recipient_committed",
            "payload": None,
            "payload_available": False,
            "payload_access": "task_grant_required",
            "payload_withheld_reason": "exact_task_grant_required",
            "custody_reference": {
                "schema": "agentnet.custody-payload-reference.v1",
                "event_id": "event-task",
                "envelope_digest": "c" * 64,
                "payload_access": "task_grant_required",
                "payload_digest": "d" * 64,
            },
            "response_obligation": {
                "obligation_id": "obligation-task",
                "responsible_harness_id": responsible_harness_id,
                "state": "created",
            },
        },
    ]


def _queue(path: Path, key_path: Path, harness_id: str) -> LocalQueue:
    return LocalQueue(
        path,
        LocalEnvelopeCipher.from_key_file(key_path),
        harness_id=harness_id,
    )


def test_request_wakes_only_exact_responsible_endpoint_and_information_stays_passive(
    tmp_path: Path,
) -> None:
    responsible_id = "responsible-harness"
    sibling_id = "sibling-harness"
    items = _mailbox_items(responsible_id)
    responsible_queue = _queue(
        tmp_path / "responsible.sqlite3",
        tmp_path / "responsible.key",
        responsible_id,
    )
    sibling_queue = _queue(
        tmp_path / "sibling.sqlite3",
        tmp_path / "sibling.key",
        sibling_id,
    )
    responsible_client = _CoreClient(responsible_id, items)
    sibling_client = _CoreClient(sibling_id, items)
    responsible_runtime = _Runtime(responsible_id)
    sibling_runtime = _Runtime(sibling_id)
    responsible = BackgroundHarnessIntegration(
        DeviceSupervisor(responsible_queue, harness_id=responsible_id),
        core_client=responsible_client,
    )
    sibling = BackgroundHarnessIntegration(
        DeviceSupervisor(sibling_queue, harness_id=sibling_id),
        core_client=sibling_client,
    )
    responsible.register(responsible_runtime)  # type: ignore[arg-type]
    sibling.register(sibling_runtime)  # type: ignore[arg-type]
    try:
        assert responsible.run_once(responsible_id) == {
            "fetched": 3,
            "enqueued": 1,
            "dispatched": 1,
            "uploaded": 1,
            "obligations_reconciled": 0,
        }
        assert sibling.run_once(sibling_id) == {
            "fetched": 3,
            "enqueued": 0,
            "dispatched": 0,
            "uploaded": 0,
            "obligations_reconciled": 0,
        }
        assert responsible_client.authorized == ["obligation-task"]
        assert sibling_client.authorized == []
        prompt = responsible_runtime.prompts[0]
        assert prompt["obligation_id"] == "obligation-task"
        assert prompt["authorization"]["harness_id"] == responsible_id
        assert "quiet information" not in json.dumps(responsible_runtime.prompts)
        assert "not queued" not in json.dumps(responsible_runtime.prompts)
        assert responsible.passive_status(responsible_id)["obligations"]["action_required"] == 1
        assert sibling.passive_status(sibling_id)["obligations"]["unread_information"] == 1
    finally:
        responsible.close()
        sibling.close()
        responsible_queue.close()
        sibling_queue.close()


def test_obligation_id_queue_replays_after_manager_restart_without_mailbox_content(
    tmp_path: Path,
) -> None:
    harness_id = "restart-harness"
    database = tmp_path / "restart.sqlite3"
    key_path = tmp_path / "restart.key"
    items = _mailbox_items(harness_id)[2:]
    first_queue = _queue(database, key_path, harness_id)
    first_client = _CoreClient(harness_id, items)
    first = BackgroundHarnessIntegration(
        DeviceSupervisor(first_queue, harness_id=harness_id),
        core_client=first_client,
    )
    first.register(_Runtime(harness_id, semantic_mode="deterministic_only"))  # type: ignore[arg-type]
    try:
        result = first.run_once(harness_id)
        assert result["enqueued"] == 1
        assert result["dispatched"] == 0
        queued = first_queue.claim(harness_id=harness_id, direction="inbox")
        assert queued[0]["payload"] == {
            "schema": "agentnet.background-obligation.v1",
            "obligation_id": "obligation-task",
        }
        first_queue.retry(queued[0]["queue_id"], delay_seconds=0)
    finally:
        first.close()
        first_queue.close()

    restarted_queue = _queue(database, key_path, harness_id)
    restarted_client = _CoreClient(harness_id, items)
    restarted_runtime = _Runtime(harness_id)
    restarted = BackgroundHarnessIntegration(
        DeviceSupervisor(restarted_queue, harness_id=harness_id),
        core_client=restarted_client,
    )
    restarted.register(restarted_runtime)  # type: ignore[arg-type]
    try:
        result = restarted.run_once(harness_id)
        assert result["fetched"] == 0
        assert result["dispatched"] == 1
        assert restarted_client.authorized == ["obligation-task"]
        assert restarted_runtime.prompts[0]["obligation_id"] == "obligation-task"
    finally:
        restarted.close()
        restarted_queue.close()
