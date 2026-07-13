"""Runnable, honest four-harness lifecycle demonstrations.

The deterministic demo proves installed executable/version discovery, private
background startup, offline local custody, explicit human open, content-free
status, and bounded shutdown.  It intentionally performs no model inference.
Semantic inference is a separate signed-evidence gate in ``live_gate``.
"""

from __future__ import annotations

from pathlib import Path
from typing import Mapping
from uuid import uuid4

from agentnet.adapters.base import HarnessKind
from agentnet.adapters.specs import PINNED_VERSIONS, build_launch_spec
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.runtime import BackgroundAdapterRuntime
from agentnet.supervisor.service import DeviceSupervisor


def run_deterministic_harness_demo(
    root: Path,
    *,
    executables: Mapping[HarnessKind, str] | None = None,
    request_timeout_seconds: float = 5.0,
) -> dict[str, object]:
    """Exercise all four ordinary private runtimes without semantic content.

    Every subprocess has a bounded native request timeout and is stopped in a
    ``finally`` block.  A missing or mismatched binary fails closed through the
    runtime's pinned probe; the function never substitutes a fake harness or a
    different model.
    """

    root = root.resolve()
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    run_id = uuid4().hex
    queue = LocalQueue(
        root / f"demo-queue-{run_id}.sqlite3",
        LocalEnvelopeCipher.from_key_file(root / f"demo-queue-{run_id}.key"),
    )
    integration = BackgroundHarnessIntegration(DeviceSupervisor(queue))
    results: dict[str, object] = {}
    try:
        harnesses: tuple[HarnessKind, ...] = ("claude", "codex", "pi", "antigravity")
        for harness in harnesses:
            harness_id = f"installed-deterministic-{harness}-{run_id}"
            spec = build_launch_spec(
                harness,
                harness_id=harness_id,
                root=root / "private-runtimes",
                executable=(executables or {}).get(harness),
            )
            runtime = BackgroundAdapterRuntime(
                spec,
                request_timeout_seconds=request_timeout_seconds,
                heartbeat_interval_seconds=0.25,
                max_restart_attempts=1,
            )
            integration.register(runtime)
            event_id = f"synthetic-explicit-open-{harness}-{run_id}"
            integration.receive_from_core(
                harness_id=harness_id,
                event={
                    "event": {"event_id": event_id},
                    "payload": {
                        "synthetic": True,
                        "purpose": "deterministic installed-harness lifecycle demo",
                    },
                },
                cursor=1,
            )
            before = integration.passive_status(harness_id)["activity"]
            started = integration.start(harness_id)
            health = runtime.healthcheck()
            opened = integration.explicit_pull(harness_id, limit=1)
            after = integration.passive_status(harness_id)["activity"]
            if len(opened) != 1 or opened[0].get("disposition") != "explicit_human_open":
                raise RuntimeError(f"{harness} deterministic explicit-open lifecycle did not complete")
            results[harness] = {
                "foreground_session_id": spec.foreground_session_id,
                "health": health,
                "native_surface": spec.transport,
                "passive_activity_after": after,
                "passive_activity_before": before,
                "pinned_version": PINNED_VERSIONS[harness],
                "runtime": started,
                "semantic_content_sent": False,
            }
            integration.stop(harness_id)
        return {
            "schema": "agentnet.four-harness-deterministic-demo.v1",
            "warning": (
                "Installed deterministic lifecycle evidence only: no model inference, "
                "corporate enrollment, foreground injection, or external conformance claim."
            ),
            "harnesses": results,
        }
    finally:
        integration.close()
        queue.close()


def content_free_demo_summary(result: Mapping[str, object]) -> dict[str, object]:
    """Return a stable summary suitable for CLI output and operator logs."""

    harnesses = result.get("harnesses")
    if not isinstance(harnesses, Mapping):
        raise ValueError("four-harness demo result is malformed")
    summary: dict[str, object] = {}
    for harness, value in harnesses.items():
        if not isinstance(harness, str) or not isinstance(value, Mapping):
            raise ValueError("four-harness demo result is malformed")
        runtime = value.get("runtime")
        if not isinstance(runtime, Mapping):
            raise ValueError("four-harness runtime result is malformed")
        summary[harness] = {
            "clean_worker_admitted": runtime.get("clean_worker_admitted"),
            "native_surface": value.get("native_surface"),
            "phase_at_probe": runtime.get("phase"),
            "pinned_version": value.get("pinned_version"),
            "semantic_content_sent": value.get("semantic_content_sent"),
            "version_match": runtime.get("version_match"),
        }
    return {
        "schema": result.get("schema"),
        "warning": result.get("warning"),
        "harnesses": summary,
    }


__all__ = ["content_free_demo_summary", "run_deterministic_harness_demo"]
