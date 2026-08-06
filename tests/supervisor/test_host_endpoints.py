from __future__ import annotations

from contextlib import suppress
from collections.abc import Callable
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from agentnet.bindings.endpoint import EndpointBinding
from agentnet.errors import AuthenticationError
from agentnet.operations.endpoint_lifecycle import EndpointActivationState
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.host import HostEndpointSupervisor
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.service import DeviceSupervisor


class FakeEndpointBindingRepository:
    def __init__(self, *bindings: EndpointBinding) -> None:
        self.current = {
            (binding.domain_id, binding.harness_id): binding for binding in bindings
        }
        self.revoked: set[tuple[str, str]] = set()
        self.loads: list[tuple[str, str]] = []

    def load_current(self, *, domain_id: str, harness_id: str) -> EndpointBinding:
        key = (domain_id, harness_id)
        self.loads.append(key)
        if key in self.revoked:
            raise AuthenticationError("exact endpoint credential is revoked")
        return self.current[key]


class FakeEndpointLifecycle:
    def __init__(self, *bindings: EndpointBinding) -> None:
        self.states = {
            binding.harness_id: EndpointActivationState.CONNECTED for binding in bindings
        }
        self.reconciled: list[str] = []

    def reconcile(self, *, endpoint_id: str) -> SimpleNamespace:
        self.reconciled.append(endpoint_id)
        return SimpleNamespace(state=self.states[endpoint_id])


class RecordingDeviceSupervisor(DeviceSupervisor):
    def __init__(self, local_queue: LocalQueue) -> None:
        super().__init__(local_queue)
        self.acknowledged: list[tuple[str, str, dict[str, Any], str]] = []
        self.lose_next_ack_response = False

    def acknowledge_with_local_output(
        self,
        *,
        harness_id: str,
        source_queue_id: str,
        request: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        result = super().acknowledge_with_local_output(
            harness_id=harness_id,
            source_queue_id=source_queue_id,
            request=request,
            idempotency_key=idempotency_key,
        )
        self.acknowledged.append(
            (harness_id, source_queue_id, request, idempotency_key)
        )
        if self.lose_next_ack_response:
            self.lose_next_ack_response = False
            raise ConnectionError("synthetic acknowledgement response loss")
        return result


class FakeChild:
    def __init__(self) -> None:
        self.alive = True

    def close(self) -> None:
        self.alive = False


class FakeEndpointWorker:
    def __init__(
        self,
        binding: EndpointBinding,
        device: RecordingDeviceSupervisor,
        *,
        terminal_responses: dict[str, dict[str, Any]] | None = None,
        lose_first_response: bool = False,
        before_response: Callable[[], None] | None = None,
    ) -> None:
        self.binding = binding
        self.device = device
        self.terminal_responses = (
            terminal_responses if terminal_responses is not None else {}
        )
        self.lose_first_response = lose_first_response
        self.before_response = before_response
        self.deliveries: list[str] = []
        self.launch_cursor: int | None = None
        self.closed_reasons: list[str] = []
        self.child: FakeChild | None = None
        self.socket_path = binding.capability_root_path / "manager.sock"
        self.capability_path = binding.capability_root_path / "endpoint.capability"

    def launch(self, binding: EndpointBinding) -> FakeChild:
        assert binding == self.binding
        self.launch_cursor = self.device.local_queue.cursor(binding.harness_id)
        binding.capability_root_path.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.socket_path.touch(mode=0o600)
        self.capability_path.touch(mode=0o600)
        self.child = FakeChild()
        return self.child

    def deliver(self, item: dict[str, Any]) -> dict[str, Any] | None:
        event_id = item["payload"]["event"]["event_id"]
        self.deliveries.append(event_id)
        response = self.terminal_responses.setdefault(
            event_id,
            {
                "event_id": event_id,
                "disposition": "processed_by_exact_endpoint",
            },
        )
        if self.before_response is not None:
            self.before_response()
        if self.lose_first_response and self.deliveries.count(event_id) == 1:
            return None
        return dict(response)

    def close(self, reason: str) -> None:
        self.closed_reasons.append(reason)
        if self.child is not None:
            self.child.close()
        self.socket_path.unlink(missing_ok=True)
        self.capability_path.unlink(missing_ok=True)


class EndpointHarness:
    def __init__(self, tmp_path: Path, *bindings: EndpointBinding) -> None:
        self.tmp_path = tmp_path
        self.bindings = {binding.harness_id: binding for binding in bindings}
        self.repository = FakeEndpointBindingRepository(*bindings)
        self.lifecycle = FakeEndpointLifecycle(*bindings)
        self.devices: dict[str, RecordingDeviceSupervisor] = {}
        self.workers: dict[str, list[FakeEndpointWorker]] = {
            binding.harness_id: [] for binding in bindings
        }
        self.terminal_responses: dict[str, dict[str, Any]] = {}
        self.lose_first_response_for: set[str] = set()
        self.before_response_hooks: dict[str, Callable[[], None]] = {}
        self.hosts: list[HostEndpointSupervisor] = []

    def queue_path(self, harness_id: str) -> Path:
        return self.tmp_path / harness_id / "queue.sqlite3"

    def queue_key_path(self, harness_id: str) -> Path:
        return self.tmp_path / harness_id / "queue.key"

    def open_queue(self, binding: EndpointBinding) -> LocalQueue:
        return LocalQueue(
            self.queue_path(binding.harness_id),
            LocalEnvelopeCipher.from_key_file(
                self.queue_key_path(binding.harness_id)
            ),
        )

    def queue_factory(self, binding: EndpointBinding) -> LocalQueue:
        return self.open_queue(binding)

    def device_factory(self, queue: LocalQueue) -> RecordingDeviceSupervisor:
        return RecordingDeviceSupervisor(queue)

    def worker_factory(
        self,
        binding: EndpointBinding,
        device: RecordingDeviceSupervisor,
    ) -> FakeEndpointWorker:
        self.devices[binding.harness_id] = device
        worker = FakeEndpointWorker(
            binding,
            device,
            terminal_responses=self.terminal_responses,
            lose_first_response=binding.harness_id in self.lose_first_response_for,
            before_response=self.before_response_hooks.get(binding.harness_id),
        )
        self.workers[binding.harness_id].append(worker)
        return worker

    def new_host(self) -> HostEndpointSupervisor:
        host = HostEndpointSupervisor(
            self.repository,
            self.lifecycle,
            queue_factory=self.queue_factory,
            device_factory=self.device_factory,
            worker_factory=self.worker_factory,
            clock=lambda: 1_800_000_000,
        )
        self.hosts.append(host)
        return host

    def enqueue(self, harness_id: str, event_id: str, *, cursor: int) -> None:
        self.devices[harness_id].receive_from_core(
            harness_id=harness_id,
            event={"event": {"event_id": event_id}},
            cursor=cursor,
        )

    def close(self) -> None:
        for host in self.hosts:
            for harness_id in self.bindings:
                with suppress(Exception):
                    host.deactivate(harness_id, reason="test fixture close")


@pytest.fixture
def endpoint_a(tmp_path: Path) -> EndpointBinding:
    return EndpointBinding(
        domain_id="corp.example",
        principal_id="principal-shared",
        harness_id="pi-endpoint-a",
        harness_kind="pi",
        credential_id="credential-a",
        credential_epoch=3,
        adapter_generation=7,
        mailbox_cursor=0,
        profile_key="work-a",
        capability_root_path=tmp_path / "capabilities" / "a",
        process_measurement="pid:101:start:700",
    )


@pytest.fixture
def endpoint_b(tmp_path: Path) -> EndpointBinding:
    return EndpointBinding(
        domain_id="corp.example",
        principal_id="principal-shared",
        harness_id="pi-endpoint-b",
        harness_kind="pi",
        credential_id="credential-b",
        credential_epoch=5,
        adapter_generation=11,
        mailbox_cursor=0,
        profile_key="work-b",
        capability_root_path=tmp_path / "capabilities" / "b",
        process_measurement="pid:202:start:900",
    )


@pytest.fixture
def endpoints(
    tmp_path: Path,
    endpoint_a: EndpointBinding,
    endpoint_b: EndpointBinding,
):
    harness = EndpointHarness(tmp_path, endpoint_a, endpoint_b)
    try:
        yield harness
    finally:
        harness.close()


def test_only_exact_target_dequeues_and_acknowledges(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
    endpoint_b: EndpointBinding,
) -> None:
    host = endpoints.new_host()
    host.activate(endpoint_a)
    host.activate(endpoint_b)
    endpoints.enqueue(endpoint_a.harness_id, "event-target-a", cursor=9)

    host.reconcile_once()

    worker_a = endpoints.workers[endpoint_a.harness_id][0]
    worker_b = endpoints.workers[endpoint_b.harness_id][0]
    assert worker_a.deliveries == ["event-target-a"]
    assert worker_b.deliveries == []
    assert [ack[0] for ack in endpoints.devices[endpoint_a.harness_id].acknowledged] == [
        endpoint_a.harness_id
    ]
    assert endpoints.devices[endpoint_b.harness_id].acknowledged == []
    assert endpoints.devices[endpoint_a.harness_id].local_queue.content_free_counts(
        endpoint_a.harness_id
    ) == {}
    assert endpoints.devices[endpoint_b.harness_id].local_queue.content_free_counts(
        endpoint_b.harness_id
    ) == {}


def test_offline_target_remains_exclusively_queued_and_sibling_never_observes_it(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
    endpoint_b: EndpointBinding,
) -> None:
    offline_queue = endpoints.open_queue(endpoint_a)
    offline_queue.enqueue_inbox_with_cursor(
        harness_id=endpoint_a.harness_id,
        idempotency_key="offline-event-delivery-0001",
        payload={"event": {"event_id": "event-offline-a"}},
        cursor=12,
    )
    offline_queue.close()

    host = endpoints.new_host()
    host.activate(endpoint_b)
    host.reconcile_once()

    assert endpoints.workers[endpoint_b.harness_id][0].deliveries == []
    assert endpoints.devices[endpoint_b.harness_id].acknowledged == []
    reopened = endpoints.open_queue(endpoint_a)
    try:
        assert reopened.content_free_counts(endpoint_a.harness_id) == {"queued": 1}
        assert reopened.cursor(endpoint_a.harness_id) == 12
    finally:
        reopened.close()


@pytest.mark.parametrize("fence", ["generation", "revocation"])
def test_stale_generation_or_revocation_closes_only_original_runtime_without_losing_event(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
    endpoint_b: EndpointBinding,
    fence: str,
) -> None:
    host = endpoints.new_host()
    host.activate(endpoint_a)
    host.activate(endpoint_b)
    endpoints.enqueue(endpoint_a.harness_id, f"event-{fence}", cursor=15)

    if fence == "generation":
        endpoints.repository.current[(endpoint_a.domain_id, endpoint_a.harness_id)] = replace(
            endpoint_a,
            adapter_generation=endpoint_a.adapter_generation + 1,
            process_measurement="pid:303:start:1200",
        )
    else:
        endpoints.lifecycle.states[endpoint_a.harness_id] = EndpointActivationState.BLOCKED

    statuses = {
        status.harness_id: status for status in host.reconcile_once()
    }

    fenced_worker = endpoints.workers[endpoint_a.harness_id][0]
    assert fenced_worker.closed_reasons
    assert fenced_worker.deliveries == []
    assert endpoints.workers[endpoint_b.harness_id][0].deliveries == []
    assert endpoints.devices[endpoint_b.harness_id].acknowledged == []
    assert statuses[endpoint_a.harness_id].phase == "closed"
    assert statuses[endpoint_b.harness_id].phase == "active"
    reopened = endpoints.open_queue(endpoint_a)
    try:
        assert reopened.content_free_counts(endpoint_a.harness_id) == {"queued": 1}
        assert reopened.cursor(endpoint_a.harness_id) == 15
    finally:
        reopened.close()


def test_generation_change_between_dequeue_and_ack_fences_response_and_retains_event(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
    endpoint_b: EndpointBinding,
) -> None:
    def rotate_while_worker_has_claim() -> None:
        endpoints.repository.current[
            (endpoint_a.domain_id, endpoint_a.harness_id)
        ] = replace(
            endpoint_a,
            adapter_generation=endpoint_a.adapter_generation + 1,
            process_measurement="pid:404:start:1500",
        )

    endpoints.before_response_hooks[endpoint_a.harness_id] = (
        rotate_while_worker_has_claim
    )
    host = endpoints.new_host()
    host.activate(endpoint_a)
    host.activate(endpoint_b)
    endpoints.enqueue(endpoint_a.harness_id, "event-generation-race", cursor=18)

    host.reconcile_once()

    target_worker = endpoints.workers[endpoint_a.harness_id][0]
    assert target_worker.deliveries == ["event-generation-race"]
    assert target_worker.closed_reasons
    assert endpoints.devices[endpoint_a.harness_id].acknowledged == []
    assert endpoints.workers[endpoint_b.harness_id][0].deliveries == []
    assert endpoints.devices[endpoint_b.harness_id].acknowledged == []
    reopened = endpoints.open_queue(endpoint_a)
    try:
        retained = reopened.content_free_counts(endpoint_a.harness_id)
        assert sum(retained.values()) == 1
        assert set(retained) <= {"queued", "retry_scheduled"}
        assert reopened.cursor(endpoint_a.harness_id) == 18
    finally:
        reopened.close()


def test_response_loss_is_idempotent_and_acknowledges_one_terminal_response(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
) -> None:
    endpoints.lose_first_response_for.add(endpoint_a.harness_id)
    host = endpoints.new_host()
    host.activate(endpoint_a)
    endpoints.enqueue(endpoint_a.harness_id, "event-response-loss", cursor=21)

    host.reconcile_once()
    host.reconcile_once()

    worker = endpoints.workers[endpoint_a.harness_id][0]
    assert worker.deliveries == ["event-response-loss", "event-response-loss"]
    assert endpoints.terminal_responses == {
        "event-response-loss": {
            "event_id": "event-response-loss",
            "disposition": "processed_by_exact_endpoint",
        }
    }
    acknowledgements = endpoints.devices[endpoint_a.harness_id].acknowledged
    assert len(acknowledgements) == 1
    assert acknowledgements[0][0] == endpoint_a.harness_id
    assert acknowledgements[0][2] == endpoints.terminal_responses["event-response-loss"]
    assert endpoints.devices[endpoint_a.harness_id].local_queue.content_free_counts(
        endpoint_a.harness_id
    ) == {}


def test_acknowledgement_response_loss_does_not_duplicate_terminal_output(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
) -> None:
    host = endpoints.new_host()
    host.activate(endpoint_a)
    endpoints.enqueue(endpoint_a.harness_id, "event-ack-response-loss", cursor=27)
    device = endpoints.devices[endpoint_a.harness_id]
    device.lose_next_ack_response = True

    with pytest.raises(
        ConnectionError,
        match="acknowledgement response loss",
    ):
        host.reconcile_once()
    host.reconcile_once()

    worker = endpoints.workers[endpoint_a.harness_id][0]
    assert worker.deliveries == ["event-ack-response-loss"]
    assert len(device.acknowledged) == 1
    assert device.local_queue.content_free_counts(endpoint_a.harness_id) == {}
    durable_outputs = device.local_queue.claim(
        harness_id=endpoint_a.harness_id,
        direction="outbox",
    )
    assert len(durable_outputs) == 1
    assert durable_outputs[0]["payload"] == endpoints.terminal_responses[
        "event-ack-response-loss"
    ]


def test_explicit_restart_resumes_exact_endpoint_cursor_and_pending_event(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
) -> None:
    endpoints.lose_first_response_for.add(endpoint_a.harness_id)
    first_host = endpoints.new_host()
    first_host.activate(endpoint_a)
    endpoints.enqueue(endpoint_a.harness_id, "event-before-restart", cursor=34)
    first_host.reconcile_once()
    first_host.deactivate(endpoint_a.harness_id, reason="explicit user restart")

    endpoints.lose_first_response_for.discard(endpoint_a.harness_id)
    restarted_host = endpoints.new_host()
    restarted_host.activate(endpoint_a)
    restarted_worker = endpoints.workers[endpoint_a.harness_id][-1]

    assert restarted_worker.launch_cursor == 34
    restarted_host.reconcile_once()
    assert restarted_worker.deliveries == ["event-before-restart"]
    assert len(endpoints.devices[endpoint_a.harness_id].acknowledged) == 1
    assert endpoints.devices[endpoint_a.harness_id].local_queue.cursor(
        endpoint_a.harness_id
    ) == 34
    assert endpoints.devices[endpoint_a.harness_id].local_queue.content_free_counts(
        endpoint_a.harness_id
    ) == {}


def test_deactivate_closes_child_and_removes_socket_and_capability_resources(
    endpoints: EndpointHarness,
    endpoint_a: EndpointBinding,
) -> None:
    host = endpoints.new_host()
    active = host.activate(endpoint_a)
    worker = endpoints.workers[endpoint_a.harness_id][0]
    child = worker.child

    assert active.phase == "active"
    assert child is not None and child.alive
    assert worker.socket_path.exists()
    assert worker.capability_path.exists()

    closed = host.deactivate(endpoint_a.harness_id, reason="fixture close")

    assert closed.phase == "closed"
    assert closed.reason == "fixture close"
    assert child.alive is False
    assert not worker.socket_path.exists()
    assert not worker.capability_path.exists()
