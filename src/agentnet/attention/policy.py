"""Routine traffic is silent; exceptional notices are content-free."""

from __future__ import annotations

import re
from collections.abc import Callable
from datetime import UTC, datetime

from agentnet.operations.policy_defaults import (
    ATTENTION_EXCEPTION_TYPES,
    AttentionPolicy,
)

ALLOWED_EXCEPTION_TYPES = ATTENTION_EXCEPTION_TYPES

OPAQUE_REFERENCE = re.compile(r"^(?:approval|incident|elevation|failure):[a-f0-9]{32,64}$")


def exceptional_notice(
    event_type: str,
    *,
    opaque_reference: str,
    policy: AttentionPolicy | None = None,
    when: datetime | None = None,
) -> dict[str, str] | None:
    configured = policy or AttentionPolicy()
    if event_type not in ALLOWED_EXCEPTION_TYPES or not configured.allows(event_type):
        return None
    if not OPAQUE_REFERENCE.fullmatch(opaque_reference):
        raise ValueError("exception reference must be an opaque bounded identifier")
    result = {"type": event_type, "reference": opaque_reference, "content": "redacted"}
    if when is not None:
        if when.tzinfo is None:
            raise ValueError("attention decision time must be timezone-aware")
        if configured.is_quiet_hour(when):
            result["delivery"] = "deferred_quiet_hours"
    return result


class AttentionService:
    def __init__(
        self,
        policy: AttentionPolicy,
        *,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        self.policy = policy
        self.clock = clock

    def exceptional_notice(
        self,
        event_type: str,
        *,
        opaque_reference: str,
        when: datetime | None = None,
    ) -> dict[str, str] | None:
        return exceptional_notice(
            event_type,
            opaque_reference=opaque_reference,
            policy=self.policy,
            when=when or self.clock(),
        )
