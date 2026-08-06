#!/usr/bin/env python3
"""Deterministic exact-endpoint routing and offline-custody smoke scenario."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from importlib.metadata import distribution
from pathlib import Path
from typing import Any
from urllib.parse import urlparse
from urllib.request import url2pathname

import agentnet
from agentnet.adapters.catalog import BUILTIN_ADAPTERS
from agentnet.bindings.endpoint import EndpointBinding
from agentnet.errors import AuthenticationError
from agentnet.operations.endpoint_lifecycle import (
    EndpointActivationState,
    EndpointLifecycleStatus,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.host import HostEndpointSupervisor
from agentnet.supervisor.queue import LocalQueue


DOMAIN_ID = "routing-smoke.example"
PRINCIPAL_ID = "principal:exact-routing-smoke"
ONLINE_EVENT_ID = "event:exact-endpoint-routing:online"
OFFLINE_EVENT_ID = "event:exact-endpoint-routing:offline"
TARGET_HARNESS_ID = "harness:pi:target"
_ENDPOINTS = (
    (TARGET_HARNESS_ID, "pi", "target-pi"),
    ("harness:pi:sibling", "pi", "sibling-pi"),
    ("harness:omp:sibling", "omp", "sibling-omp"),
    ("harness:claude:sibling", "claude", "sibling-claude"),
)
_REQUIRED_ADAPTERS = frozenset({"omp", "pi", "claude", "codex", "antigravity"})


class _BindingRepository:
    def __init__(self, bindings: tuple[EndpointBinding, ...]) -> None:
        self._bindings = {
            (binding.domain_id, binding.harness_id): binding for binding in bindings
        }

    def load_current(self, *, domain_id: str, harness_id: str) -> EndpointBinding:
        try:
            return self._bindings[(domain_id, harness_id)]
        except KeyError as exc:
            raise AuthenticationError("exact endpoint binding is unavailable") from exc


class _Lifecycle:
    def __init__(self, statuses: dict[str, EndpointLifecycleStatus]) -> None:
        self._statuses = statuses

    def reconcile(self, *, endpoint_id: str) -> EndpointLifecycleStatus:
        try:
            return self._statuses[endpoint_id]
        except KeyError as exc:
            raise AuthenticationError("exact endpoint lifecycle is unavailable") from exc


@dataclass(slots=True)
class _ObservationLedger:
    by_event: dict[str, list[str]]

    def record(self, *, event_id: str, harness_id: str) -> None:
        self.by_event.setdefault(event_id, []).append(harness_id)

    def processed_harnesses(self, event_id: str) -> list[str]:
        return list(self.by_event.get(event_id, ()))


class _DeterministicEndpointWorker:
    """A no-inference endpoint process whose parent records exact deliveries."""

    def __init__(
        self,
        binding: EndpointBinding,
        ledger: _ObservationLedger,
    ) -> None:
        self.binding = binding
        self.ledger = ledger
        self.process: subprocess.Popen[bytes] | None = None
        self._processed = 0

    def launch(self, binding: EndpointBinding) -> None:
        if binding != self.binding or self.process is not None:
            raise RuntimeError("worker launch crossed its exact endpoint binding")
        environment = {
            "PYTHONDONTWRITEBYTECODE": "1",
            "PYTHONNOUSERSITE": "1",
        }
        for name in ("SYSTEMROOT", "TEMP", "TMP", "WINDIR"):
            if name in os.environ:
                environment[name] = os.environ[name]
        self.process = subprocess.Popen(
            [sys.executable, "-B", "-I", "-c", "import time; time.sleep(3600)"],
            cwd=self.binding.capability_root_path.parent,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=os.name != "nt",
        )

    def deliver(self, claimed_item: dict[str, Any]) -> dict[str, object]:
        process = self.process
        if process is None or process.poll() is not None:
            raise RuntimeError("delivery reached an inactive endpoint process")
        payload = claimed_item.get("payload")
        if not isinstance(payload, dict):
            raise RuntimeError("claimed endpoint event payload is invalid")
        event = payload.get("event")
        if not isinstance(event, dict) or not isinstance(event.get("event_id"), str):
            raise RuntimeError("claimed endpoint event identity is invalid")
        if event.get("recipient_harness_id") != self.binding.harness_id:
            raise RuntimeError("worker observed an event addressed to a sibling endpoint")
        event_id = str(event["event_id"])
        self.ledger.record(event_id=event_id, harness_id=self.binding.harness_id)
        self._processed += 1
        return {
            "event_id": event_id,
            "processed_harness_id": self.binding.harness_id,
        }

    def close(self, reason: str) -> None:
        del reason
        process = self.process
        if process is None:
            return
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)

    def content_free_status(self) -> dict[str, int]:
        return {"processed": self._processed}


@dataclass(frozen=True, slots=True)
class _ScenarioState:
    report: dict[str, object]
    workers: tuple[_DeterministicEndpointWorker, ...]


def _assert_installed_package(package_root: Path) -> None:
    package_root = package_root.resolve()
    expected = package_root / "src" / "agentnet"
    module_file = Path(agentnet.__file__).resolve()
    if module_file.is_relative_to(expected):
        return
    direct_url_text = distribution("agentnet").read_text("direct_url.json")
    if direct_url_text is None:
        raise RuntimeError("routing smoke package provenance is unavailable")
    direct_url = json.loads(direct_url_text).get("url")
    if not isinstance(direct_url, str):
        raise RuntimeError("routing smoke package provenance is invalid")
    parsed = urlparse(direct_url)
    if parsed.scheme != "file" or parsed.netloc not in {"", "localhost"}:
        raise RuntimeError("routing smoke package provenance is not local")
    installed_from = Path(url2pathname(parsed.path)).resolve()
    if installed_from != package_root:
        raise RuntimeError("routing smoke did not execute from the selected package bytes")


def _prepare_runtime_root(runtime_root: Path) -> Path:
    if runtime_root.is_symlink() or runtime_root.exists():
        raise RuntimeError("routing smoke runtime root must be a new real directory")
    runtime_root.mkdir(parents=True, mode=0o700)
    if os.name != "nt":
        runtime_root.chmod(0o700)
    return runtime_root.resolve()


def _private_write(path: Path, value: bytes) -> None:
    path.write_bytes(value)
    if os.name != "nt":
        path.chmod(0o600)


def _binding(runtime_root: Path, harness_id: str, harness_kind: str, profile: str) -> EndpointBinding:
    generation = 1
    opaque = hashlib.sha256(
        f"{DOMAIN_ID}\0{harness_id}\0{generation}".encode("utf-8")
    ).hexdigest()
    capability_directory = runtime_root / "capability-roots" / opaque
    capability_directory.mkdir(parents=True, mode=0o700)
    if os.name != "nt":
        capability_directory.chmod(0o700)
    capability_file = capability_directory / "capability-root.key"
    _private_write(capability_file, hashlib.sha256(f"capability:{harness_id}".encode()).digest())
    return EndpointBinding(
        domain_id=DOMAIN_ID,
        principal_id=PRINCIPAL_ID,
        harness_id=harness_id,
        harness_kind=harness_kind,
        credential_id=f"credential:{harness_id}",
        credential_epoch=1,
        adapter_generation=generation,
        mailbox_cursor=0,
        profile_key=profile,
        capability_root_path=capability_file,
        process_measurement=hashlib.sha256(
            f"process:{harness_id}".encode("utf-8")
        ).hexdigest(),
    )


def _lifecycle_status(binding: EndpointBinding) -> EndpointLifecycleStatus:
    now = 1_786_000_000
    return EndpointLifecycleStatus(
        endpoint_id=binding.harness_id,
        endpoint_address=f"agentnet:{binding.domain_id}:{binding.harness_id}",
        domain_id=binding.domain_id,
        principal_id=binding.principal_id,
        harness_id=binding.harness_id,
        current_credential_id=binding.credential_id,
        harness_kind=binding.harness_kind,
        profile_key=binding.profile_key,
        state=EndpointActivationState.CONNECTED,
        adapter_generation=binding.adapter_generation,
        mailbox_cursor=binding.mailbox_cursor,
        capability_root_digest=hashlib.sha256(
            binding.capability_root_path.read_bytes()
        ).hexdigest(),
        process_measurement=binding.process_measurement,
        state_reason="exact_routing_smoke",
        revision=1,
        created_at=now,
        updated_at=now,
    )


def _queue_for_binding(binding: EndpointBinding) -> LocalQueue:
    root = binding.capability_root_path.parent
    cipher = LocalEnvelopeCipher.from_key_file(root / "endpoint-queue.key")
    return LocalQueue(
        root / "endpoint-queue.sqlite3",
        cipher,
        harness_id=binding.harness_id,
    )


def _event(event_id: str, recipient_harness_id: str, question: str) -> dict[str, object]:
    return {
        "event": {
            "event_id": event_id,
            "recipient_harness_id": recipient_harness_id,
        },
        "payload": {"question": question},
    }


def _queue_owner(queue_path: Path, queue_id: str) -> tuple[str, str]:
    with sqlite3.connect(queue_path) as connection:
        row = connection.execute(
            "SELECT harness_id,state FROM queue WHERE queue_id=?",
            (queue_id,),
        ).fetchone()
    if row is None:
        raise RuntimeError("offline event lost exact endpoint custody")
    return str(row[0]), str(row[1])


def _queue_row_count(queue_path: Path) -> int:
    with sqlite3.connect(queue_path) as connection:
        row = connection.execute("SELECT COUNT(*) FROM queue").fetchone()
    return int(row[0]) if row is not None else 0


def _execute(runtime_root: Path) -> _ScenarioState:
    if set(BUILTIN_ADAPTERS) != _REQUIRED_ADAPTERS:
        raise RuntimeError("installed package does not register all five exact harness adapters")

    bindings = tuple(_binding(runtime_root, *endpoint) for endpoint in _ENDPOINTS)
    target = bindings[0]
    repository = _BindingRepository(bindings)
    lifecycle = _Lifecycle(
        {binding.harness_id: _lifecycle_status(binding) for binding in bindings}
    )
    ledger = _ObservationLedger(by_event={})
    queues: dict[str, LocalQueue] = {}
    queue_paths = {
        binding.harness_id: binding.capability_root_path.parent / "endpoint-queue.sqlite3"
        for binding in bindings
    }
    workers: list[_DeterministicEndpointWorker] = []

    def queue_factory(binding: EndpointBinding) -> LocalQueue:
        queue = _queue_for_binding(binding)
        queues[binding.harness_id] = queue
        return queue

    def worker_factory(binding: EndpointBinding, _device: object) -> _DeterministicEndpointWorker:
        worker = _DeterministicEndpointWorker(binding, ledger)
        workers.append(worker)
        return worker

    host = HostEndpointSupervisor(
        repository,
        lifecycle,
        queue_factory=queue_factory,
        worker_factory=worker_factory,
        clock=lambda: 1_786_000_000,
    )
    active: list[str] = []
    try:
        for binding in bindings:
            host.activate(binding)
            active.append(binding.harness_id)

        queues[target.harness_id].enqueue(
            harness_id=target.harness_id,
            direction="inbox",
            idempotency_key="exact-routing-online-0001",
            payload=_event(ONLINE_EVENT_ID, target.harness_id, "reply with 7"),
        )
        host.reconcile_once()
        processed = ledger.processed_harnesses(ONLINE_EVENT_ID)
        if processed != [target.harness_id]:
            raise RuntimeError("online event was not processed only by its exact endpoint")
        if any(_queue_row_count(queue_paths[binding.harness_id]) for binding in bindings[1:]):
            raise RuntimeError("online event entered a sibling endpoint queue")

        host.deactivate(target.harness_id, reason="offline-test")
        active.remove(target.harness_id)
        offline_queue = _queue_for_binding(target)
        try:
            queued = offline_queue.enqueue(
                harness_id=target.harness_id,
                direction="inbox",
                idempotency_key="exact-routing-offline-0001",
                payload=_event(OFFLINE_EVENT_ID, target.harness_id, "reply with 8"),
            )
        finally:
            offline_queue.close()

        host.reconcile_once()
        offline_processed = ledger.processed_harnesses(OFFLINE_EVENT_ID)
        if offline_processed:
            raise RuntimeError("offline target event was processed by another endpoint")
        owner, state = _queue_owner(queue_paths[target.harness_id], str(queued["queue_id"]))
        if owner != target.harness_id or state != "queued":
            raise RuntimeError("offline event did not retain exclusive exact-endpoint custody")
        if any(_queue_row_count(queue_paths[binding.harness_id]) for binding in bindings[1:]):
            raise RuntimeError("offline event entered a sibling endpoint queue")

        sibling_ids = {binding.harness_id for binding in bindings[1:]}
        sibling_reactions = sum(
            harness_id in sibling_ids
            for harnesses in ledger.by_event.values()
            for harness_id in harnesses
        )
        if sibling_reactions != 0:
            raise RuntimeError("a sibling endpoint reacted to exact-target work")

        return _ScenarioState(
            report={
                "event_id": ONLINE_EVENT_ID,
                "target_harness_id": target.harness_id,
                "processing_harness_id": processed[0],
                "sibling_reactions": sibling_reactions,
                "offline_queue_owner": owner,
                "offline_processing_harness_ids": offline_processed,
                "workspace_fallback_used": False,
            },
            workers=tuple(workers),
        )
    finally:
        for harness_id in reversed(active):
            try:
                host.deactivate(harness_id, reason="smoke-cleanup")
            except Exception:
                pass
        for worker in workers:
            worker.close("smoke-cleanup")
        for queue in queues.values():
            try:
                queue.close()
            except Exception:
                pass


def run_scenario(
    *,
    package_root: Path,
    runtime_root: Path,
    workspace: Path,
) -> dict[str, object]:
    package_root = package_root.resolve()
    workspace = workspace.resolve()
    _assert_installed_package(package_root)
    prepared_root = _prepare_runtime_root(runtime_root)
    state: _ScenarioState | None = None
    try:
        state = _execute(prepared_root)
    finally:
        shutil.rmtree(prepared_root, ignore_errors=False)

    assert state is not None
    process_count = sum(
        worker.process is not None and worker.process.poll() is None for worker in state.workers
    )
    capability_count = (
        sum(1 for _ in (prepared_root / "capability-roots").glob("*"))
        if prepared_root.exists()
        else 0
    )
    if process_count != 0 or capability_count != 0 or prepared_root.exists():
        raise RuntimeError("exact endpoint process or capability-root cleanup failed")
    return state.report | {
        "endpoint_processes_remaining": process_count,
        "capability_roots_remaining": capability_count,
        "workspace_fallback_used": False,
    }


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    parser.add_argument("action", nargs="?", choices=("run",), default="run")
    parser.add_argument(
        "--package-root",
        type=Path,
        default=Path(__file__).resolve().parents[2],
    )
    parser.add_argument("--runtime-root", type=Path)
    parser.add_argument("--workspace", type=Path, default=Path.cwd())
    return parser


def main() -> int:
    args = _parser().parse_args()
    runtime_root = args.runtime_root
    if runtime_root is None:
        runtime_root = Path(tempfile.mkdtemp(prefix="agentnet-exact-endpoint-routing-"))
        runtime_root.rmdir()
    report = run_scenario(
        package_root=args.package_root,
        runtime_root=runtime_root,
        workspace=args.workspace,
    )
    print(json.dumps(report, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
