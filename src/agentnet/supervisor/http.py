"""Strict HTTP request models for exact-endpoint supervisor operations."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from agentnet.protocol.models import SupervisorBackgroundAuthorization


class EligibilityBody(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    obligation_id: str = Field(min_length=1, max_length=256)


class CustodyBody(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    authorization: SupervisorBackgroundAuthorization
    obligation_id: str = Field(min_length=1, max_length=256)
    local_queue_id: str = Field(min_length=1, max_length=256)


class PayloadReleaseBody(CustodyBody):
    pass


class ResultBody(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    authorization: SupervisorBackgroundAuthorization
    source_queue_id: str = Field(min_length=1, max_length=256)
    native_result: dict[str, Any]


class LocalBindingChildBody(BaseModel):
    """Exact post-spawn child identity; corporate identity comes from the proof."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    pid: int = Field(gt=0)
    session_id: str = Field(min_length=16, max_length=256, pattern=r"^[A-Za-z0-9_-]+$")
    process_start_time: str = Field(pattern=r"^[0-9]{1,128}$")
    process_measurement: str = Field(pattern=r"^sha256:[0-9a-f]{64}$")


BackgroundAuthorizationBody = SupervisorBackgroundAuthorization


__all__ = [
    "BackgroundAuthorizationBody",
    "CustodyBody",
    "EligibilityBody",
    "LocalBindingChildBody",
    "PayloadReleaseBody",
    "ResultBody",
]
