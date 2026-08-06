"""Exact-endpoint host supervision without foreground-session interaction."""

from __future__ import annotations

import subprocess
import threading
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from hashlib import sha256
from pathlib import Path
from typing import Any, Literal, Protocol

from agentnet.adapters.specs import build_launch_spec
from agentnet.bindings.endpoint import EndpointBinding, EndpointBindingRepository
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.service import DeviceSupervisor
from agentnet.supervisor.workers import CleanWorkerLauncher


EndpointRuntimePhase = Literal["active", "closed"]


@dataclass(frozen=True, slots=True)
class EndpointRuntimeStatus:
    """Content-free status for one exact enrolled harness runtime."""

    domain_id: str
    harness_id: str
    harness_kind: str
    credential_id: str
    credential_epoch: int
    adapter_generation: int
    mailbox_cursor: int
    phase: EndpointRuntimePhase
    reason: str


class _EndpointLifecycle(Protocol):
    def reconcile(self, *, endpoint_id: str) -> Any: ...


class _EndpointWorker(Protocol):
    def launch(self, binding: EndpointBinding) -> Any: ...

    def deliver(self, item: dict[str, Any]) -> Mapping[str, Any] | None: ...

    def close(self, reason: str) -> None: ...


class _PreparedEndpointWorker:
    """Default endpoint preparation with no foreground or automatic restart path.

    The host prepares an exact private launch profile and clean-worker verifier.
    A harness process is attached by the endpoint-specific adapter after an
    explicit user restart; this fallback intentionally never starts or wakes an
    ordinary conversation and therefore never manufactures semantic authority.
    """

    def __init__(self, binding: EndpointBinding, _device: DeviceSupervisor) -> None:
        root = binding.capability_root_path.parent
        self.launch_spec = (
            None
            if binding.harness_kind == "omp"
            else build_launch_spec(
                binding.harness_kind,
                harness_id=binding.harness_id,
                root=root / "runtime",
                local_bindings=True,
            )
        )
        self.launcher = CleanWorkerLauncher(evidence_dir=root / "evidence")
        self.process: subprocess.Popen[bytes] | None = None
        self.socket_path = root / "agentnet.sock"
        self.capability_path = binding.capability_root_path
        self._launched = False

    def launch(self, _binding: EndpointBinding) -> None:
        self._launched = True

    def deliver(self, _item: dict[str, Any]) -> None:
        # No generic worker may reinterpret custody as permission to inject a
        # turn. Endpoint adapters replace this worker only after exact launch
        # admission and an explicit user restart.
        return None

    def close(self, _reason: str) -> None:
        process = self.process
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        self.socket_path.unlink(missing_ok=True)
        self.capability_path.unlink(missing_ok=True)
        self._launched = False


@dataclass(slots=True)
class _EndpointRuntime:
    binding: EndpointBinding
    supervisor: DeviceSupervisor
    queue: LocalQueue
    worker: _EndpointWorker
    phase: EndpointRuntimePhase = "active"
    reason: str = "activated"
    last_reconciled_at: float | None = None


QueueFactory = Callable[[EndpointBinding], LocalQueue]
DeviceFactory = Callable[[LocalQueue], DeviceSupervisor]
WorkerFactory = Callable[[EndpointBinding, DeviceSupervisor], _EndpointWorker]


def _default_queue_factory(binding: EndpointBinding) -> LocalQueue:
    capability_base = binding.capability_root_path.parent.parent.parent
    opaque_endpoint = sha256(
        f"{binding.domain_id}\0{binding.harness_id}".encode()
    ).hexdigest()
    root = capability_base / "queues" / opaque_endpoint
    return LocalQueue(
        root / "endpoint-queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(root / "endpoint-queue.key"),
        harness_id=binding.harness_id,
    )


def _default_device_factory(queue: LocalQueue) -> DeviceSupervisor:
    return DeviceSupervisor(queue)


def _default_worker_factory(
    binding: EndpointBinding,
    device: DeviceSupervisor,
) -> _EndpointWorker:
    return _PreparedEndpointWorker(binding, device)


class HostEndpointSupervisor:
    """Own isolated queue, worker, and lifecycle state per exact harness ID."""

    def __init__(
        self,
        repository: EndpointBindingRepository,
        lifecycle: _EndpointLifecycle | None = None,
        *,
        queue_factory: QueueFactory | None = None,
        device_factory: DeviceFactory | None = None,
        worker_factory: WorkerFactory | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._repository = repository
        self._lifecycle = lifecycle
        self._queue_factory = queue_factory or _default_queue_factory
        self._device_factory = device_factory or _default_device_factory
        self._worker_factory = worker_factory or _default_worker_factory
        self._clock = clock
        self._runtimes: dict[str, _EndpointRuntime] = {}
        self._closed: dict[str, EndpointRuntimeStatus] = {}
        self._lock = threading.RLock()

    def activate(self, binding: EndpointBinding) -> EndpointRuntimeStatus:
        """Activate one exact binding without waking or replacing a sibling."""

        with self._lock:
            self._verify_current(binding)
            existing = self._runtimes.get(binding.harness_id)
            if existing is not None:
                if existing.binding == binding:
                    return self._runtime_status(existing)
                self._close_runtime(existing, reason="exact endpoint binding changed")

            queue: LocalQueue | None = None
            worker: _EndpointWorker | None = None
            try:
                queue = self._queue_factory(binding)
                queue.set_cursor(binding.harness_id, binding.mailbox_cursor)
                supervisor = self._device_factory(queue)
                worker = self._worker_factory(binding, supervisor)
                worker.launch(binding)
            except Exception:
                if worker is not None:
                    try:
                        worker.close("activation failed")
                    except Exception:
                        pass
                    self._cleanup_worker_resources(worker)
                if queue is not None:
                    queue.recover_processing()
                    queue.close()
                raise

            runtime = _EndpointRuntime(
                binding=binding,
                supervisor=supervisor,
                queue=queue,
                worker=worker,
            )
            self._runtimes[binding.harness_id] = runtime
            self._closed.pop(binding.harness_id, None)
            return self._runtime_status(runtime)

    def deactivate(self, harness_id: str, *, reason: str) -> EndpointRuntimeStatus:
        """Close one exact runtime and retain its durable endpoint queue."""

        if not harness_id or not reason:
            raise ValidationError("endpoint deactivation binding is invalid")
        with self._lock:
            runtime = self._runtimes.get(harness_id)
            if runtime is not None:
                return self._close_runtime(runtime, reason=reason)
            try:
                status = self._closed[harness_id]
            except KeyError as exc:
                raise ValidationError("endpoint runtime is not registered") from exc
            if status.reason == reason:
                return status
            updated = EndpointRuntimeStatus(
                domain_id=status.domain_id,
                harness_id=status.harness_id,
                harness_kind=status.harness_kind,
                credential_id=status.credential_id,
                credential_epoch=status.credential_epoch,
                adapter_generation=status.adapter_generation,
                mailbox_cursor=status.mailbox_cursor,
                phase="closed",
                reason=reason,
            )
            self._closed[harness_id] = updated
            return updated

    def reconcile_once(self) -> tuple[EndpointRuntimeStatus, ...]:
        """Run one bounded pass for every active exact endpoint.

        Binding and lifecycle authority are reloaded immediately before the
        endpoint queue is claimed and again immediately before native output is
        durably acknowledged. A failed fence closes only that runtime. Its
        claimed item is restored to the same queue and is never offered to a
        sibling.
        """

        with self._lock:
            harness_ids = tuple(sorted(self._runtimes))
            for harness_id in harness_ids:
                runtime = self._runtimes.get(harness_id)
                if runtime is None:
                    continue
                try:
                    self._verify_current(runtime.binding)
                except Exception as exc:
                    self._close_runtime(runtime, reason=self._fence_reason(exc))
                    continue

                claimed = runtime.queue.claim(
                    harness_id=runtime.binding.harness_id,
                    direction="inbox",
                    limit=25,
                )
                for item in claimed:
                    queue_id = str(item["queue_id"])
                    try:
                        response = runtime.worker.deliver(item)
                        if response is None:
                            runtime.queue.retry(queue_id, delay_seconds=0)
                            continue
                        self._verify_current(runtime.binding)
                        runtime.supervisor.acknowledge_with_local_output(
                            harness_id=runtime.binding.harness_id,
                            source_queue_id=queue_id,
                            request=dict(response),
                            idempotency_key=f"native-output:{queue_id}",
                        )
                    except Exception as exc:
                        self._retry_if_processing(runtime.queue, queue_id)
                        if self._is_fence_failure(runtime.binding, exc):
                            self._close_runtime(runtime, reason=self._fence_reason(exc))
                            break
                        raise
                if harness_id in self._runtimes:
                    runtime.last_reconciled_at = self._clock()

            return self.status()

    def status(self) -> tuple[EndpointRuntimeStatus, ...]:
        """Return content-free endpoint status ordered by exact harness ID."""

        with self._lock:
            statuses = dict(self._closed)
            statuses.update(
                {
                    harness_id: self._runtime_status(runtime)
                    for harness_id, runtime in self._runtimes.items()
                }
            )
            return tuple(statuses[harness_id] for harness_id in sorted(statuses))

    def _verify_current(self, binding: EndpointBinding) -> EndpointBinding:
        verifier = getattr(self._repository, "require_current", None)
        if callable(verifier):
            current = verifier(binding, process_measurement=binding.process_measurement)
        else:
            current = self._repository.load_current(
                domain_id=binding.domain_id,
                harness_id=binding.harness_id,
            )
            if not self._same_runtime_binding(current, binding):
                raise AuthenticationError("exact endpoint binding changed")

        lifecycle = self._lifecycle
        if lifecycle is not None:
            state = lifecycle.reconcile(endpoint_id=binding.harness_id)
            state_value = getattr(getattr(state, "state", None), "value", getattr(state, "state", None))
            if state_value != "connected":
                raise AuthenticationError("exact endpoint lifecycle is not connected")
            self._require_optional_equal(state, "domain_id", binding.domain_id)
            self._require_optional_equal(state, "principal_id", binding.principal_id)
            self._require_optional_equal(state, "harness_id", binding.harness_id)
            self._require_optional_equal(state, "harness_kind", binding.harness_kind)
            self._require_optional_equal(state, "profile_key", binding.profile_key)
            self._require_optional_equal(state, "current_credential_id", binding.credential_id)
            self._require_optional_equal(
                state,
                "adapter_generation",
                binding.adapter_generation,
            )
            self._require_optional_equal(
                state,
                "process_measurement",
                binding.process_measurement,
            )
        return current

    @staticmethod
    def _require_optional_equal(value: Any, field: str, expected: object) -> None:
        if hasattr(value, field) and getattr(value, field) != expected:
            raise AuthenticationError(f"exact endpoint {field} changed")

    @staticmethod
    def _same_runtime_binding(left: EndpointBinding, right: EndpointBinding) -> bool:
        return (
            left.domain_id,
            left.principal_id,
            left.harness_id,
            left.harness_kind,
            left.credential_id,
            left.credential_epoch,
            left.adapter_generation,
            left.profile_key,
            left.capability_root_path,
            left.process_measurement,
        ) == (
            right.domain_id,
            right.principal_id,
            right.harness_id,
            right.harness_kind,
            right.credential_id,
            right.credential_epoch,
            right.adapter_generation,
            right.profile_key,
            right.capability_root_path,
            right.process_measurement,
        )

    def _runtime_status(self, runtime: _EndpointRuntime) -> EndpointRuntimeStatus:
        return EndpointRuntimeStatus(
            domain_id=runtime.binding.domain_id,
            harness_id=runtime.binding.harness_id,
            harness_kind=runtime.binding.harness_kind,
            credential_id=runtime.binding.credential_id,
            credential_epoch=runtime.binding.credential_epoch,
            adapter_generation=runtime.binding.adapter_generation,
            mailbox_cursor=runtime.queue.cursor(runtime.binding.harness_id),
            phase=runtime.phase,
            reason=runtime.reason,
        )

    def _close_runtime(
        self,
        runtime: _EndpointRuntime,
        *,
        reason: str,
    ) -> EndpointRuntimeStatus:
        harness_id = runtime.binding.harness_id
        self._runtimes.pop(harness_id, None)
        runtime.phase = "closed"
        runtime.reason = reason
        runtime.queue.recover_processing()
        cursor = runtime.queue.cursor(harness_id)
        worker_error: Exception | None = None
        try:
            runtime.worker.close(reason)
        except Exception as exc:
            worker_error = exc
        finally:
            self._cleanup_worker_resources(runtime.worker)
            runtime.queue.close()
        status = EndpointRuntimeStatus(
            domain_id=runtime.binding.domain_id,
            harness_id=harness_id,
            harness_kind=runtime.binding.harness_kind,
            credential_id=runtime.binding.credential_id,
            credential_epoch=runtime.binding.credential_epoch,
            adapter_generation=runtime.binding.adapter_generation,
            mailbox_cursor=cursor,
            phase="closed",
            reason=reason,
        )
        self._closed[harness_id] = status
        if worker_error is not None:
            raise worker_error
        return status

    @staticmethod
    def _cleanup_worker_resources(worker: _EndpointWorker) -> None:
        child = getattr(worker, "child", None)
        close_child = getattr(child, "close", None)
        if callable(close_child):
            try:
                close_child()
            except Exception:
                pass
        process = getattr(worker, "process", None)
        poll = getattr(process, "poll", None)
        if callable(poll) and poll() is None:
            try:
                process.terminate()
                process.wait(timeout=3)
            except Exception:
                try:
                    process.kill()
                    process.wait(timeout=3)
                except Exception:
                    pass
        for field in ("socket_path", "capability_path"):
            path = getattr(worker, field, None)
            if path is None:
                continue
            try:
                Path(path).unlink(missing_ok=True)
            except OSError:
                pass

    @staticmethod
    def _retry_if_processing(queue: LocalQueue, queue_id: str) -> None:
        try:
            queue.retry(queue_id, delay_seconds=0)
        except Exception:
            # The acknowledgement transaction may already have committed and
            # only its response was lost. In that case the inbox is completed
            # and the durable outbox record is authoritative.
            return

    def _is_fence_failure(self, binding: EndpointBinding, exc: Exception) -> bool:
        if isinstance(exc, AuthenticationError):
            return True
        try:
            self._verify_current(binding)
        except Exception:
            return True
        return False

    @staticmethod
    def _fence_reason(exc: Exception) -> str:
        message = str(exc).strip()
        return message[:128] if message else type(exc).__name__


__all__ = ["EndpointRuntimeStatus", "HostEndpointSupervisor"]
