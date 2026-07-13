"""Clock and epoch checks shared by protected operations."""

from __future__ import annotations

from datetime import UTC, datetime

from agentnet.errors import AuthenticationError


def require_current_epoch(*, presented: int, current: int, name: str) -> None:
    if presented != current:
        raise AuthenticationError(f"stale or unknown {name} epoch")


def require_unexpired(expires_at: datetime, *, now: datetime | None = None) -> None:
    current = now or datetime.now(UTC)
    if expires_at.tzinfo is None or expires_at <= current:
        raise AuthenticationError("credential or authority is expired")

