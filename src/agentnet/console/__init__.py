"""Private AgentNet administration console."""

from __future__ import annotations

from typing import Any


def create_console_app(*args: Any, **kwargs: Any):
    from agentnet.console.http import create_console_app as factory

    return factory(*args, **kwargs)


__all__ = ["create_console_app"]
