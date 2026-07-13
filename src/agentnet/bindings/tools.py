"""Canonical local tool surface shared by direct, MCP, and Unix IPC bindings.

Identity is deliberately absent from every argument model.  A dispatcher is
constructed with a server-side actor provider and resolves that provider for
every call, so credential rotation or revocation fences an already-created
binding without accepting replacement identity claims from tool arguments.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field

from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import Classification


CanonicalToolName = Literal["agentnet.inbox", "agentnet.send"]
CANONICAL_TOOL_NAMES: tuple[CanonicalToolName, ...] = ("agentnet.inbox", "agentnet.send")


class BoundCore(Protocol):
    def send_message(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        payload: dict[str, Any],
        idempotency_key: str,
        classification: Classification = Classification.C1_INTERNAL,
    ) -> dict[str, Any]: ...

    def mailbox(
        self,
        *,
        actor: VerifiedActor,
        after_cursor: int,
        limit: int,
    ) -> list[dict[str, Any]]: ...


ActorProvider = Callable[[], VerifiedActor]


class CanonicalToolRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    method: CanonicalToolName
    arguments: dict[str, Any]


class SendArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL


class InboxArguments(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    after_cursor: int = Field(default=0, ge=0)
    limit: int = Field(default=25, ge=1, le=100)


class CanonicalToolDispatcher:
    """Dispatch exact local tools under a fresh server-derived actor."""

    def __init__(self, core: BoundCore, actor_provider: ActorProvider) -> None:
        self.core = core
        self.actor_provider = actor_provider

    def call(self, method: CanonicalToolName, arguments: dict[str, Any]) -> Any:
        request = CanonicalToolRequest(method=method, arguments=arguments)
        actor = self.actor_provider()
        if request.method == "agentnet.send":
            parsed = SendArguments.model_validate(request.arguments)
            return self.core.send_message(
                actor=actor,
                recipients=parsed.recipients,
                payload=parsed.payload,
                idempotency_key=parsed.idempotency_key,
                classification=parsed.classification,
            )
        parsed = InboxArguments.model_validate(request.arguments)
        return self.core.mailbox(
            actor=actor,
            after_cursor=parsed.after_cursor,
            limit=parsed.limit,
        )


__all__ = [
    "CANONICAL_TOOL_NAMES",
    "ActorProvider",
    "BoundCore",
    "CanonicalToolDispatcher",
    "CanonicalToolName",
    "CanonicalToolRequest",
    "InboxArguments",
    "SendArguments",
]
