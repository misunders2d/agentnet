"""Runnable laptop/server-side ordinary harness supervisor daemon."""

from __future__ import annotations

import json
import os
import signal
import stat
import threading
import time
from pathlib import Path
from typing import Literal

import httpx
from pydantic import (
    AliasChoices,
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
    model_validator,
)

from agentnet.adapters.auth import (
    EphemeralBrokerEnvironment,
    HarnessAuthInjection,
    PreprovisionedPrivateAuth,
)
from agentnet.adapters.base import HarnessKind
from agentnet.adapters.specs import build_launch_spec
from agentnet.client import AgentNetClient
from agentnet.errors import GateBlocked, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.client import AgentNetSupervisorCoreClient
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.service import DeviceSupervisor
from agentnet.supervisor.workers import CleanWorkerLauncher


class SupervisorDaemonConfig(BaseModel):
    """Non-secret references for one enrolled ordinary background harness."""

    model_config = ConfigDict(extra="forbid", frozen=True, populate_by_name=True)

    schema_version: Literal["1.0"] = "1.0"
    core_base_url: str
    audience: str = Field(min_length=3, max_length=512)
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    harness_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    signing_key_path: Path
    harness: HarnessKind
    executable: str | None = Field(default=None, min_length=1, max_length=4_096)
    runtime_root: Path
    queue_database_path: Path
    queue_key_path: Path
    evidence_dir: Path
    trusted_evidence_keys: dict[str, str] = Field(min_length=1, max_length=32)
    sandbox_launcher: str = Field(default="bwrap", min_length=1, max_length=4_096)
    auth_environment_names: tuple[str, ...] = ()
    private_auth_source: Path | None = None
    private_auth_broker_origin: str | None = None
    codex_model: Literal["gpt-5.6-sol"] = "gpt-5.6-sol"
    codex_reasoning_effort: Literal["ultra"] = "ultra"
    watch_wait_seconds: float = Field(default=5.0, ge=0.25, le=30)
    reconciliation_interval_seconds: float = Field(
        default=30.0,
        ge=5,
        le=300,
        validation_alias=AliasChoices(
            "reconciliation_interval_seconds",
            "poll_interval_seconds",
        ),
    )
    reconnect_initial_seconds: float = Field(default=0.25, ge=0.05, le=5)
    reconnect_max_seconds: float = Field(default=5.0, ge=0.1, le=30)
    request_timeout_seconds: float = Field(default=30.0, ge=0.25, le=60)
    heartbeat_interval_seconds: float = Field(default=1.0, ge=0.05, le=60)
    max_restart_attempts: int = Field(default=3, ge=0, le=20)
    max_consecutive_cycle_errors: int = Field(default=5, ge=1, le=100)
    max_cycle_staleness_seconds: float = Field(default=90.0, ge=1.0, le=300.0)
    local_bindings_required: bool = False

    @model_validator(mode="after")
    def exact_auth_profile(self) -> "SupervisorDaemonConfig":
        if self.local_bindings_required and self.harness == "antigravity":
            raise ValueError("Antigravity has no approved local binding and remains deterministic-only")
        if bool(self.auth_environment_names) == bool(self.private_auth_source):
            raise ValueError("supervisor requires exactly one explicit worker authentication source")
        if len(set(self.auth_environment_names)) != len(self.auth_environment_names):
            raise ValueError("supervisor authentication environment names must be unique")
        if any(
            "PRIVATE KEY" in key or "PUBLIC KEY" not in key
            for key in self.trusted_evidence_keys.values()
        ):
            raise ValueError("clean-worker evidence trust must contain public keys only")
        for path in (
            self.signing_key_path,
            self.runtime_root,
            self.queue_database_path,
            self.queue_key_path,
            self.evidence_dir,
        ):
            if len(str(path)) > 4_096 or "PRIVATE KEY" in str(path).upper():
                raise ValueError("supervisor path reference is invalid")
            if not path.is_absolute():
                raise ValueError("supervisor path references must be absolute")
        if self.reconnect_max_seconds < self.reconnect_initial_seconds:
            raise ValueError("supervisor reconnect ceiling must not precede its initial delay")
        minimum_cycle_ceiling = (
            self.request_timeout_seconds
            + self.watch_wait_seconds
            + self.heartbeat_interval_seconds
            + self.reconnect_max_seconds
        )
        if self.max_cycle_staleness_seconds <= minimum_cycle_ceiling:
            raise ValueError(
                "supervisor cycle staleness ceiling must exceed request, watch, reconnect, and heartbeat bounds"
            )
        return self


def load_supervisor_config(path: Path) -> SupervisorDaemonConfig:
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise ValidationError("supervisor config is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or metadata.st_size < 2
            or metadata.st_size > 1_048_576
        ):
            raise ValidationError("supervisor config must be an owner-only bounded regular file")
        value = os.read(descriptor, 1_048_577)
        if len(value) != metadata.st_size:
            raise ValidationError("supervisor config changed during bounded read")
    finally:
        os.close(descriptor)
    try:
        return SupervisorDaemonConfig.model_validate_json(value)
    except PydanticValidationError as exc:
        first = exc.errors(include_url=False, include_input=False)[0]
        location = ".".join(str(part) for part in first.get("loc", ())) or "document"
        reason = str(first.get("msg", "invalid value"))
        raise ValidationError(
            f"supervisor daemon config is invalid at {location}: {reason}; "
            "expected agentnet-supervisor.json, not the core agentnet.json"
        ) from exc


def _owner_private_key(path: Path) -> P256KeyPair:
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("supervisor_signing_key", "supervisor signing key is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or metadata.st_size < 128
            or metadata.st_size > 16_384
        ):
            raise GateBlocked(
                "supervisor_signing_key",
                "supervisor signing key must be one owner-only bounded file",
            )
        value = os.read(descriptor, 16_385)
        if len(value) != metadata.st_size:
            raise GateBlocked("supervisor_signing_key", "supervisor signing key changed during read")
    finally:
        os.close(descriptor)
    try:
        return P256KeyPair.from_private_pem(value)
    except Exception as exc:
        raise GateBlocked("supervisor_signing_key", "supervisor signing key is invalid") from exc


def _owner_queue_cipher(path: Path) -> LocalEnvelopeCipher:
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("supervisor_queue_key", "supervisor queue key is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or metadata.st_size != 32
        ):
            raise GateBlocked(
                "supervisor_queue_key",
                "supervisor queue key must be one owner-only 256-bit file",
            )
        value = os.read(descriptor, 33)
        if len(value) != 32:
            raise GateBlocked("supervisor_queue_key", "supervisor queue key changed during read")
    finally:
        os.close(descriptor)
    return LocalEnvelopeCipher(value)


def _worker_auth(config: SupervisorDaemonConfig) -> HarnessAuthInjection:
    if config.private_auth_source is not None:
        return PreprovisionedPrivateAuth(
            config.harness,
            config.private_auth_source,
            broker_origin=config.private_auth_broker_origin,
        )
    values: dict[str, str] = {}
    for name in config.auth_environment_names:
        value = os.environ.get(name)
        if value is None:
            raise GateBlocked("G03", f"clean-worker authentication environment {name} is absent")
        values[name] = value
    return EphemeralBrokerEnvironment(config.harness, values)


def run_supervisor_daemon(
    config: SupervisorDaemonConfig,
    *,
    stop_event: threading.Event | None = None,
    transport: httpx.BaseTransport | None = None,
) -> dict[str, object]:
    """Run until SIGINT/SIGTERM; every failure closes workers and durable stores."""

    signing_key = _owner_private_key(config.signing_key_path)
    queue: LocalQueue | None = None
    client: AgentNetClient | None = None
    integration: BackgroundHarnessIntegration | None = None
    requested_stop = stop_event or threading.Event()
    previous_handlers: dict[int, object] = {}
    try:
        queue = LocalQueue(
            config.queue_database_path,
            _owner_queue_cipher(config.queue_key_path),
        )
        client = AgentNetClient(
            base_url=config.core_base_url,
            key=signing_key,
            domain_id=config.domain_id,
            harness_id=config.harness_id,
            credential_id=config.credential_id,
            audience=config.audience,
            transport=transport,
        )
        core_client = AgentNetSupervisorCoreClient(client)
        integration = BackgroundHarnessIntegration(
            DeviceSupervisor(queue),
            core_client=core_client,
            watch_wait_seconds=config.watch_wait_seconds,
            reconciliation_interval_seconds=config.reconciliation_interval_seconds,
            reconnect_initial_seconds=config.reconnect_initial_seconds,
            reconnect_max_seconds=config.reconnect_max_seconds,
        )
        if threading.current_thread() is threading.main_thread():
            for signum in (signal.SIGINT, signal.SIGTERM):
                previous_handlers[signum] = signal.getsignal(signum)
                signal.signal(signum, lambda _signum, _frame: requested_stop.set())
        base_spec = build_launch_spec(
            config.harness,
            harness_id=config.harness_id,
            root=config.runtime_root,
            executable=config.executable,
            local_bindings=config.local_bindings_required,
        )
        runtime = CleanWorkerLauncher(
            evidence_dir=config.evidence_dir,
            trusted_evidence_keys=config.trusted_evidence_keys,
        ).create_adapter_runtime(
            base_spec,
            _worker_auth(config),
            sandbox_launcher=config.sandbox_launcher,
            request_timeout_seconds=config.request_timeout_seconds,
            heartbeat_interval_seconds=config.heartbeat_interval_seconds,
            max_restart_attempts=config.max_restart_attempts,
            local_binding_issuer=(
                core_client.issue_local_binding if config.local_bindings_required else None
            ),
        )
        integration.register(runtime)
        integration.start_daemon(config.harness_id)
        nonready_since: float | None = None
        while not requested_stop.wait(1.0):
            status = integration.passive_status(config.harness_id)
            daemon = status["daemon"]
            runtime_status = status["runtime"]
            if daemon["running"] is not True:
                raise GateBlocked("supervisor_daemon", "supervisor mailbox watch thread stopped")
            if int(daemon["errors"]) >= config.max_consecutive_cycle_errors:
                raise GateBlocked(
                    "supervisor_daemon",
                    "supervisor reached its consecutive corporate-cycle error ceiling",
                )
            cycle_reference = (
                daemon["cycle_started_at"]
                or daemon["last_cycle_at"]
                or daemon["daemon_started_at"]
            )
            if (
                cycle_reference is not None
                and time.time() - float(cycle_reference) > config.max_cycle_staleness_seconds
            ):
                raise GateBlocked("supervisor_daemon", "supervisor mailbox watch cycle stalled")
            phase = runtime_status["phase"]
            if phase in {"ready", "starting"}:
                nonready_since = None
            elif phase == "offline":
                nonready_since = nonready_since or time.monotonic()
                restart_grace = max(
                    5.0,
                    config.request_timeout_seconds
                    + config.heartbeat_interval_seconds * (config.max_restart_attempts + 1),
                )
                if time.monotonic() - nonready_since > restart_grace:
                    raise GateBlocked(
                        "supervisor_daemon",
                        "background harness did not recover within its restart ceiling",
                    )
            else:
                raise GateBlocked(
                    "supervisor_daemon",
                    f"background harness became {phase}",
                )
        return {
            "schema": "agentnet.supervisor-daemon.exit.v1",
            "harness": config.harness,
            "stopped": True,
        }
    finally:
        if integration is not None:
            integration.close()
        if client is not None:
            client.close()
        if queue is not None:
            queue.close()
        for signum, handler in previous_handlers.items():
            signal.signal(signum, handler)


def redacted_supervisor_status(config: SupervisorDaemonConfig) -> dict[str, object]:
    return {
        "schema": "agentnet.supervisor-daemon.config.v1",
        "core_base_url": config.core_base_url,
        "domain_id": config.domain_id,
        "harness": config.harness,
        "harness_id": config.harness_id,
        "credential_id": config.credential_id,
        "auth_environment_names": list(config.auth_environment_names),
        "private_auth_configured": config.private_auth_source is not None,
        "trusted_evidence_key_count": len(config.trusted_evidence_keys),
        "local_bindings_required": config.local_bindings_required,
        "watch_wait_seconds": config.watch_wait_seconds,
        "reconciliation_interval_seconds": config.reconciliation_interval_seconds,
        "reconnect_max_seconds": config.reconnect_max_seconds,
    }


__all__ = [
    "SupervisorDaemonConfig",
    "load_supervisor_config",
    "redacted_supervisor_status",
    "run_supervisor_daemon",
]
