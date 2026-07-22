"""Strict selector-free contract for the deterministic C0 pilot."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


_STRICT = ConfigDict(extra="forbid", frozen=True, strict=True)
C0_PILOT_SUCCESS = "COMPLETED_C0_ROUND_TRIP"
C0_PILOT_STAGES = frozenset(
    {
        "prepared_unusable",
        "waiting_owner",
        "waiting_fresh",
        "expired",
        "invalidated",
        C0_PILOT_SUCCESS,
    }
)


class C0PilotStartRequest(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.c0-pilot.start.v1"] = Field(alias="schema")


class C0PilotRespondRequest(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.c0-pilot.respond.v1"] = Field(alias="schema")


class C0PilotCompleteRequest(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.c0-pilot.complete.v1"] = Field(alias="schema")


class C0PilotStatusRequest(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.c0-pilot.status.v1"] = Field(alias="schema")


class C0PilotResult(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.c0-pilot.result.v1"] = Field(alias="schema")
    status: Literal[
        "prepared_unusable",
        "waiting_owner",
        "waiting_fresh",
        "expired",
        "invalidated",
        "COMPLETED_C0_ROUND_TRIP",
    ]


def c0_result(status: str) -> dict[str, str]:
    return C0PilotResult.model_validate(
        {"schema": "agentnet.c0-pilot.result.v1", "status": status}
    ).model_dump(by_alias=True)


__all__ = [
    "C0_PILOT_STAGES",
    "C0_PILOT_SUCCESS",
    "C0PilotCompleteRequest",
    "C0PilotRespondRequest",
    "C0PilotResult",
    "C0PilotStartRequest",
    "C0PilotStatusRequest",
    "c0_result",
]
