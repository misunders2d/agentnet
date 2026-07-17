"""Canonical host-platform detection independent of launcher input."""

from __future__ import annotations

import sys
from typing import Literal

from agentnet.errors import GateBlocked


HostPlatform = Literal["linux", "macos", "windows"]
_HOST_PLATFORM_BY_PYTHON = {
    "linux": "linux",
    "darwin": "macos",
    "win32": "windows",
}


def host_platform(value: str | None = None) -> HostPlatform:
    """Return the canonical host platform or fail closed for unknown hosts."""

    detected = sys.platform if value is None else value
    try:
        return _HOST_PLATFORM_BY_PYTHON[detected]  # type: ignore[return-value]
    except KeyError as exc:
        raise GateBlocked("host_platform", f"unsupported host platform: {detected}") from exc


__all__ = ["HostPlatform", "host_platform"]
