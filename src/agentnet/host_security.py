"""Cross-platform host process identity with PID-reuse fencing.

Linux hashes the live ``/proc/<pid>/exe`` object. macOS and Windows hash a
resolved executable path while holding one file descriptor and checking stable
file identity plus repeated process creation time. The latter fails closed but
is intentionally documented as lower assurance against privileged path
replacement until stronger target-host evidence exists.
"""

from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Protocol

import psutil

from agentnet.errors import AuthenticationError
from agentnet.host import HostPlatform, host_platform


class ProcessLike(Protocol):
    pid: int

    def create_time(self) -> float: ...
    def exe(self) -> str: ...
    def ppid(self) -> int: ...
    def is_running(self) -> bool: ...
    def uids(self): ...


@dataclass(frozen=True, slots=True)
class HostProcessIdentity:
    platform: HostPlatform
    account_id: str
    pid: int
    parent_pid: int
    start_time: str
    executable_path: str
    executable_measurement: str


def _windows_process_sid(pid: int) -> str:
    """Resolve the process-token SID through pywin32 without ambient names."""

    try:
        import win32api
        import win32security

        process = win32api.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
        try:
            token = win32security.OpenProcessToken(process, 0x0008)  # TOKEN_QUERY
            try:
                sid = win32security.GetTokenInformation(token, win32security.TokenUser)[0]
                rendered = win32security.ConvertSidToStringSid(sid)
            finally:
                token.Close()
        finally:
            process.Close()
    except Exception as exc:  # pywin32 raises several platform-specific types
        raise AuthenticationError("host process account SID could not be measured") from exc
    if not isinstance(rendered, str) or not rendered.startswith("S-"):
        raise AuthenticationError("host process account SID is invalid")
    return f"sid:{rendered}"


def _process_account_id(process: ProcessLike, platform_name: HostPlatform) -> str:
    if platform_name == "windows":
        return _windows_process_sid(process.pid)
    try:
        uid = int(process.uids().effective)
    except (AttributeError, OSError, psutil.Error, ValueError) as exc:
        raise AuthenticationError("host process account UID could not be measured") from exc
    if uid < 0:
        raise AuthenticationError("host process account UID is invalid")
    return f"uid:{uid}"


def _hash_executable(
    path: str,
    *,
    pid: int,
    platform_name: HostPlatform,
) -> tuple[str, str]:
    """Hash live Linux inode directly; other hosts recheck opened-file identity."""

    try:
        resolved = str(Path(path).resolve(strict=True))
        source = (
            f"/proc/{pid}/exe"
            if platform_name == "linux" and os.path.isdir("/proc/self")
            else resolved
        )
        digest = hashlib.sha256()
        with open(source, "rb") as executable:
            before = os.fstat(executable.fileno())
            for chunk in iter(lambda: executable.read(1024 * 1024), b""):
                digest.update(chunk)
            after = os.fstat(executable.fileno())
        if (
            before.st_dev,
            before.st_ino,
            before.st_size,
            before.st_mtime_ns,
        ) != (
            after.st_dev,
            after.st_ino,
            after.st_size,
            after.st_mtime_ns,
        ):
            raise AuthenticationError("host process executable changed during measurement")
    except AuthenticationError:
        raise
    except (OSError, RuntimeError, ValueError) as exc:
        raise AuthenticationError("host process executable could not be measured") from exc
    return resolved, f"sha256:{digest.hexdigest()}"


def measure_process_identity(
    pid: int,
    *,
    process_factory: Callable[[int], ProcessLike] = psutil.Process,
    platform_name: HostPlatform | None = None,
    account_resolver: Callable[[ProcessLike, HostPlatform], str] = _process_account_id,
) -> HostProcessIdentity:
    """Measure one live process twice so PID reuse or exec drift fails closed."""

    if type(pid) is not int or pid <= 0:
        raise AuthenticationError("host process identifier is invalid")
    platform_value = host_platform() if platform_name is None else platform_name
    try:
        process = process_factory(pid)
        before = float(process.create_time())
        executable_path, executable_measurement = _hash_executable(
            process.exe(),
            pid=pid,
            platform_name=platform_value,
        )
        parent_pid = int(process.ppid())
        account_id = account_resolver(process, platform_value)
        after = float(process.create_time())
        running = bool(process.is_running())
    except AuthenticationError:
        raise
    except (OSError, psutil.Error, TypeError, ValueError) as exc:
        raise AuthenticationError("host process identity could not be measured") from exc
    if (
        not running
        or before <= 0
        or before != after
        or parent_pid < 0
        or not account_id
    ):
        raise AuthenticationError("host process identity changed during measurement")
    return HostProcessIdentity(
        platform=platform_value,
        account_id=account_id,
        pid=pid,
        parent_pid=parent_pid,
        start_time=str(round(before * 1_000_000_000)),
        executable_path=executable_path,
        executable_measurement=executable_measurement,
    )


def current_account_id() -> str:
    return measure_process_identity(os.getpid()).account_id


__all__ = [
    "HostProcessIdentity",
    "current_account_id",
    "measure_process_identity",
]
