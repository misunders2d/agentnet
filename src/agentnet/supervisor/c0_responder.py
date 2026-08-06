"""Dedicated package-owned C0 responder with no worker or queue coupling."""

from __future__ import annotations

import json
import os
import signal
import stat
import threading
from pathlib import Path
from typing import Literal

import httpx
from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError

from agentnet.client import AgentNetClient
from agentnet.errors import GateBlocked, ValidationError
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.client import AgentNetC0PilotCoreClient


class C0PilotResponderConfig(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.c0-pilot-responder.config.v1"] = Field(alias="schema")
    core_base_url: str = Field(pattern=r"^https://", max_length=512)
    audience: str = Field(min_length=3, max_length=512)
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    harness_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    poll_seconds: float = Field(default=2.0, ge=0.25, le=30)
    reconnect_initial_seconds: float = Field(default=0.25, ge=0.05, le=5)
    reconnect_max_seconds: float = Field(default=5.0, ge=0.1, le=30)
    max_consecutive_errors: int = Field(default=5, ge=1, le=100)


def _owner_file(path: Path, *, label: str) -> bytes:
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise ValidationError(f"{label} is unavailable") from exc
    try:
        info = os.fstat(descriptor)
        if (
            not stat.S_ISREG(info.st_mode)
            or info.st_nlink != 1
            or info.st_uid != os.geteuid()
            or info.st_mode & 0o077
            or info.st_size > 65_536
        ):
            raise ValidationError(f"{label} custody is invalid")
        value = os.read(descriptor, info.st_size + 1)
        if len(value) != info.st_size:
            raise ValidationError(f"{label} changed while reading")
        return value
    finally:
        os.close(descriptor)


def _credential_file(path: Path, *, label: str) -> bytes:
    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_NONBLOCK", 0),
        )
    except OSError as exc:
        raise ValidationError(f"{label} is unavailable") from exc
    try:
        info = os.fstat(descriptor)
        credential_root = os.environ.get("CREDENTIALS_DIRECTORY")
        systemd_path = (
            credential_root is not None
            and Path(credential_root).is_absolute()
            and path.parent == Path(credential_root)
            and path.name == "signing-key.pem"
        )
        owner_custody = info.st_uid == os.geteuid() and not info.st_mode & 0o077
        systemd_custody = (
            systemd_path
            and info.st_uid == 0
            and info.st_gid == 0
            and stat.S_IMODE(info.st_mode) in {0o400, 0o440}
        )
        if (
            not stat.S_ISREG(info.st_mode)
            or info.st_nlink != 1
            or not (owner_custody or systemd_custody)
            or info.st_size > 65_536
        ):
            raise ValidationError(f"{label} custody is invalid")
        value = os.read(descriptor, info.st_size + 1)
        if len(value) != info.st_size:
            raise ValidationError(f"{label} changed while reading")
        return value
    finally:
        os.close(descriptor)


def load_c0_responder_config(path: Path) -> C0PilotResponderConfig:
    try:
        return C0PilotResponderConfig.model_validate_json(
            _owner_file(path, label="C0 responder config")
        )
    except PydanticValidationError as exc:
        raise ValidationError("C0 responder config is invalid") from exc


def _record_terminal_and_remove_config(
    path: Path,
    *,
    config: C0PilotResponderConfig,
    status: str,
) -> None:
    parent = path.parent
    marker = parent / "terminal.json"
    temporary = parent / f".terminal.{os.getpid()}.{threading.get_ident()}.tmp"
    payload = (
        json.dumps(
            {
                "schema": "agentnet.c0-pilot-responder.terminal.v1",
                "status": status,
                "domain_id": config.domain_id,
                "harness_id": config.harness_id,
                "credential_id": config.credential_id,
            },
            sort_keys=True,
        ).encode("utf-8")
        + b"\n"
    )
    try:
        descriptor = os.open(
            temporary,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_EXCL
            | getattr(os, "O_NOFOLLOW", 0),
            0o600,
        )
        try:
            os.write(descriptor, payload)
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        os.replace(temporary, marker)
        os.unlink(path)
        descriptor = os.open(parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
    except OSError as exc:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise GateBlocked("c0_pilot_responder", "terminal responder cleanup failed") from exc


def _client(
    config: C0PilotResponderConfig,
    credential_path: Path,
    *,
    transport: httpx.BaseTransport | None,
) -> tuple[AgentNetClient, AgentNetC0PilotCoreClient]:
    try:
        key = P256KeyPair.from_private_pem(
            _credential_file(credential_path, label="C0 responder credential")
        )
    except (UnicodeError, ValueError) as exc:
        raise ValidationError("C0 responder credential is invalid") from exc
    client = AgentNetClient(
        base_url=config.core_base_url,
        key=key,
        domain_id=config.domain_id,
        harness_id=config.harness_id,
        credential_id=config.credential_id,
        audience=config.audience,
        transport=transport,
    )
    return client, AgentNetC0PilotCoreClient(client)


def check_c0_responder(
    config: C0PilotResponderConfig,
    credential_path: Path,
    *,
    transport: httpx.BaseTransport | None = None,
) -> dict[str, str]:
    client, core = _client(config, credential_path, transport=transport)
    try:
        readiness = core.c0_pilot_readiness()
        return {
            "schema": "agentnet.c0-pilot-responder.check.v1",
            "status": readiness["status"],
        }
    finally:
        client.close()


def run_c0_responder(
    config: C0PilotResponderConfig,
    credential_path: Path,
    config_path: Path,
    *,
    stop_event: threading.Event | None = None,
    transport: httpx.BaseTransport | None = None,
) -> dict[str, object]:
    client, core = _client(config, credential_path, transport=transport)
    requested_stop = stop_event or threading.Event()
    previous_handlers: dict[int, object] = {}
    try:
        if threading.current_thread() is threading.main_thread():
            for signum in (signal.SIGINT, signal.SIGTERM):
                previous_handlers[signum] = signal.getsignal(signum)
                signal.signal(signum, lambda _signum, _frame: requested_stop.set())
        errors = 0
        delay = config.reconnect_initial_seconds
        while not requested_stop.is_set():
            try:
                readiness = core.c0_pilot_readiness()
                if readiness["status"] == "waiting_plan":
                    status = "waiting_plan"
                else:
                    result = core.c0_pilot_status()
                    if result["status"] == "waiting_owner":
                        result = core.c0_pilot_respond()
                    status = result["status"]
                if status in {
                    "COMPLETED_C0_ROUND_TRIP",
                    "expired",
                    "invalidated",
                    "failed",
                }:
                    _record_terminal_and_remove_config(
                        config_path,
                        config=config,
                        status=status,
                    )
                    return {
                        "schema": "agentnet.c0-pilot-responder.exit.v1",
                        "status": status,
                        "stopped": True,
                    }
                if status not in {
                    "waiting_plan",
                    "prepared_unusable",
                    "waiting_owner",
                    "waiting_fresh",
                }:
                    raise ValidationError("C0 responder status is invalid")
                errors = 0
                delay = config.reconnect_initial_seconds
                requested_stop.wait(config.poll_seconds)
            except Exception:
                errors += 1
                if errors >= config.max_consecutive_errors:
                    raise GateBlocked(
                        "c0_pilot_responder",
                        "C0 responder reached its consecutive error ceiling",
                    )
                requested_stop.wait(delay)
                delay = min(config.reconnect_max_seconds, delay * 2)
        return {
            "schema": "agentnet.c0-pilot-responder.exit.v1",
            "status": "stopped",
            "stopped": True,
        }
    finally:
        client.close()
        for signum, handler in previous_handlers.items():
            signal.signal(signum, handler)


__all__ = [
    "C0PilotResponderConfig",
    "check_c0_responder",
    "load_c0_responder_config",
    "run_c0_responder",
]
