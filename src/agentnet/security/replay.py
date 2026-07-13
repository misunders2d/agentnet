"""Persistent replay-cache abstraction."""

from __future__ import annotations

from typing import Protocol


class ReplayCache(Protocol):
    def consume_once(self, actor_id: str, nonce: str, *, expires_at: int) -> None: ...

