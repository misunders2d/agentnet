"""Strict operator-triggered installed-harness evidence gates.

Routine tests never pretend that missing credentials are a passing inference
run.  An operator must explicitly request this gate and supply signed clean
worker evidence, a separately evidenced broker-egress wrapper, and narrowly
scoped authentication.  Once requested, every missing or mismatched input is a
hard failure rather than a pytest skip.
"""

from __future__ import annotations

import hashlib
import os
import time
from collections.abc import Mapping
from dataclasses import asdict
from pathlib import Path
from typing import Any
from uuid import uuid4

from agentnet.adapters.auth import (
    EphemeralBrokerEnvironment,
    HarnessAuthInjection,
    PreprovisionedPrivateAuth,
)
from agentnet.adapters.base import HarnessKind
from agentnet.adapters.specs import build_launch_spec, detect_installed_harnesses
from agentnet.errors import GateBlocked
from agentnet.supervisor.runtime import BackgroundTurnAuthorization
from agentnet.supervisor.workers import CleanWorkerLauncher


LIVE_OPT_IN = "AGENTNET_RUN_LIVE_HARNESS_INFERENCE"
LIVE_MARKER = "AGENTNET_LIVE_OK"


def _required(environment: Mapping[str, str], name: str) -> str:
    value = environment.get(name)
    if not isinstance(value, str) or not value:
        raise GateBlocked("G03", f"explicit live harness gate is missing {name}")
    return value


def require_live_opt_in(environment: Mapping[str, str]) -> None:
    value = environment.get(LIVE_OPT_IN)
    if value != "1":
        raise GateBlocked(
            "G03",
            f"live harness inference was not explicitly requested with {LIVE_OPT_IN}=1",
        )


def _auth_for(harness: HarnessKind, environment: Mapping[str, str]) -> HarnessAuthInjection:
    if harness == "claude":
        return EphemeralBrokerEnvironment(
            "claude",
            {
                "ANTHROPIC_API_KEY": _required(environment, "AGENTNET_LIVE_CLAUDE_BROKER_KEY"),
                "ANTHROPIC_BASE_URL": _required(environment, "AGENTNET_LIVE_CLAUDE_BROKER_URL"),
            },
        )
    if harness == "codex":
        return EphemeralBrokerEnvironment(
            "codex",
            {
                "OPENAI_API_KEY": _required(environment, "AGENTNET_LIVE_CODEX_BROKER_KEY"),
                "OPENAI_BASE_URL": _required(environment, "AGENTNET_LIVE_CODEX_BROKER_URL"),
            },
        )
    if harness == "pi":
        return PreprovisionedPrivateAuth(
            "pi",
            Path(_required(environment, "AGENTNET_LIVE_PI_PRIVATE_AUTH_DIR")),
            broker_origin=_required(environment, "AGENTNET_LIVE_PI_BROKER_URL"),
        )
    return PreprovisionedPrivateAuth(
        "antigravity",
        Path(_required(environment, "AGENTNET_LIVE_ANTIGRAVITY_PRIVATE_AUTH_DIR")),
        broker_origin=_required(environment, "AGENTNET_LIVE_ANTIGRAVITY_BROKER_URL"),
    )


def installed_probe_report(
    root: Path,
    *,
    harnesses: tuple[HarnessKind, ...] = ("claude", "codex", "pi", "antigravity"),
) -> dict[str, dict[str, Any]]:
    """Evaluate requested installed binaries without making inference claims."""

    probes = detect_installed_harnesses(root, harnesses=harnesses)
    return {harness: asdict(probe) for harness, probe in probes.items()}


def assert_installed_probe_report(report: Mapping[str, Mapping[str, Any]]) -> None:
    expected = {"claude", "codex", "pi", "antigravity"}
    if set(report) != expected:
        raise GateBlocked("G01", "installed harness probe set is incomplete")
    failures = [
        harness
        for harness, probe in report.items()
        if probe.get("matches_pin") is not True or not probe.get("resolved_path")
    ]
    if failures:
        raise GateBlocked(
            "G01",
            "installed harnesses are absent or version-mismatched: " + ",".join(sorted(failures)),
        )


def run_live_harness_gate(
    harness: HarnessKind,
    *,
    root: Path,
    environment: Mapping[str, str] | None = None,
    request_timeout_seconds: float = 60.0,
) -> dict[str, Any]:
    """Run one exact native semantic round trip through signed admission."""

    supplied = dict(os.environ if environment is None else environment)
    require_live_opt_in(supplied)
    evidence_dir = Path(_required(supplied, "AGENTNET_LIVE_CLEAN_EVIDENCE_DIR"))
    evidence_key_id = _required(supplied, "AGENTNET_LIVE_CLEAN_EVIDENCE_KEY_ID")
    public_key_path = Path(_required(supplied, "AGENTNET_LIVE_CLEAN_EVIDENCE_PUBLIC_KEY"))
    sandbox_launcher = _required(supplied, "AGENTNET_LIVE_SANDBOX_EGRESS_WRAPPER")
    try:
        public_key = public_key_path.read_text(encoding="ascii")
    except OSError as exc:
        raise GateBlocked("G03", "live clean-worker evidence public key is unavailable") from exc

    auth = _auth_for(harness, supplied)
    run_id = uuid4().hex
    spec = build_launch_spec(
        harness,
        harness_id=f"installed-live-{harness}-{run_id}",
        root=root,
    )
    launcher = CleanWorkerLauncher(
        evidence_dir=evidence_dir,
        trusted_evidence_keys={evidence_key_id: public_key},
    )
    runtime = launcher.create_adapter_runtime(
        spec,
        auth,
        sandbox_launcher=sandbox_launcher,
        request_timeout_seconds=request_timeout_seconds,
        heartbeat_interval_seconds=1.0,
        max_restart_attempts=1,
    )
    prompt = f"Return only {LIVE_MARKER}."
    digest = hashlib.sha256(prompt.encode("utf-8")).hexdigest()
    authorization = BackgroundTurnAuthorization(
        decision_id=f"external-live-gate-{run_id}",
        harness_id=runtime.spec.harness_id,
        event_id=f"external-live-event-{run_id}",
        envelope_digest=digest,
        event_type="external_evidence_probe",
        classification="C0",
        policy_revision=1,
        expires_at=int(time.time()) + max(60, int(request_timeout_seconds) + 5),
        task_grant_id=None,
    )
    try:
        status = runtime.start()
        result = runtime.submit_background(prompt, authorization=authorization)
        output = result.get("output")
        if not isinstance(output, str) or LIVE_MARKER not in output:
            raise GateBlocked("G03", "live native harness response omitted the exact marker")
        return {
            "schema": "agentnet.live-harness-gate-result.v1",
            "harness": harness,
            "clean_worker_admitted": status.clean_worker_admitted,
            "native_surface": status.native_surface,
            "native_session_bound": bool(result.get("native_session_id")),
            "output_marker_verified": True,
            "pinned_version": status.pinned_version,
            "terminal_event": result.get("terminal_event"),
            "version_match": status.version_match,
            "warning": "Operator-triggered external evidence; output content is not retained.",
        }
    finally:
        runtime.stop()


__all__ = [
    "LIVE_MARKER",
    "LIVE_OPT_IN",
    "assert_installed_probe_report",
    "installed_probe_report",
    "require_live_opt_in",
    "run_live_harness_gate",
]
