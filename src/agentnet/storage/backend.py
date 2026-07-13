"""Backend-neutral transaction contract used by the corporate core."""

from __future__ import annotations

from collections.abc import Iterator, Mapping
from contextlib import AbstractContextManager
from typing import Any, Protocol, runtime_checkable

from agentnet.security.envelope import LocalEnvelopeCipher


@runtime_checkable
class StoreBackend(Protocol):
    backend_name: str
    cipher: LocalEnvelopeCipher

    def transaction(self, *, immediate: bool = True) -> AbstractContextManager[Any]: ...

    def fetch_one(self, query: str, parameters: tuple[Any, ...] = ()) -> Any | None: ...

    def fetch_all(self, query: str, parameters: tuple[Any, ...] = ()) -> list[Any]: ...

    def append_audit(self, connection: Any, record: Mapping[str, Any]) -> str: ...

    def verify_audit_chain(self) -> tuple[bool, int]: ...

    def encrypted_payload(self, payload: Mapping[str, Any], event_id: str) -> str: ...

    def decrypted_payload(self, token: str, event_id: str) -> dict[str, Any]: ...

    def readiness(self) -> dict[str, Any]: ...

    def close(self) -> None: ...
