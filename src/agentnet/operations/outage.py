"""Fail-closed dependency-outage decisions derived from trusted health state."""

from __future__ import annotations

from collections.abc import Callable
from datetime import UTC, datetime
from enum import StrEnum
from typing import Protocol

from pydantic import BaseModel, ConfigDict, Field, field_validator

from agentnet.errors import GateBlocked
from agentnet.operations.policy_defaults import OutagePolicy


class IncidentMode(StrEnum):
    NORMAL = "normal"
    FREEZE_NEW_AUTHORITY = "freeze_new_authority"
    FREEZE_PRIVILEGED = "freeze_privileged"
    FREEZE_ALL = "freeze_all"


class OperationalHealth(BaseModel):
    """Facts supplied by the deployment health controller, never request content."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    revocation_current: bool = True
    policy_current: bool = True
    audit_backlog_records: int = Field(default=0, ge=0)
    last_confirmed_current_at: datetime

    @field_validator("last_confirmed_current_at")
    @classmethod
    def timezone_aware(cls, value: datetime) -> datetime:
        if value.tzinfo is None:
            raise ValueError("operational health timestamp must be timezone-aware")
        return value


HealthProvider = Callable[[], OperationalHealth]
IncidentModeProvider = Callable[[], IncidentMode]


class OutageTelemetry(Protocol):
    def record_outage_denial(self, boundary: str) -> None: ...


def healthy_operational_state() -> OperationalHealth:
    return OperationalHealth(last_confirmed_current_at=datetime.now(UTC))


class OutageGate:
    """Apply PD-009 stop/continuity rules at service decision points."""

    def __init__(
        self,
        policy: OutagePolicy,
        *,
        health_provider: HealthProvider = healthy_operational_state,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
        telemetry: OutageTelemetry | None = None,
        incident_mode_provider: IncidentModeProvider = lambda: IncidentMode.NORMAL,
    ) -> None:
        self.policy = policy
        self._health_provider = health_provider
        self._clock = clock
        self._telemetry = telemetry
        self._incident_mode_provider = incident_mode_provider

    def _blocked(self, boundary: str, reason: str) -> None:
        if self._telemetry is not None:
            try:
                self._telemetry.record_outage_denial(boundary)
            except Exception:
                # Metrics are evidence, not authority. An unavailable telemetry
                # sink must never bypass or replace the fail-closed decision.
                pass
        raise GateBlocked(boundary, reason)

    def _state(self) -> tuple[OperationalHealth, datetime]:
        try:
            state = self._health_provider()
            now = self._clock()
        except Exception:
            self._blocked("dependency_health", "operational health provider is unavailable")
        if now.tzinfo is None:
            self._blocked("dependency_health", "operational clock is not timezone-aware")
        if state.last_confirmed_current_at > now:
            self._blocked("dependency_health", "operational health timestamp is in the future")
        if state.audit_backlog_records > self.policy.audit_backlog_max_records:
            self._blocked("audit_ceiling", "audit backlog exceeded the configured secure ceiling")
        return state, now

    def _incident_mode(self) -> IncidentMode:
        try:
            mode = IncidentMode(self._incident_mode_provider())
        except Exception:
            self._blocked("incident_control", "durable incident control is unavailable")
        return mode

    def require_issuance(self) -> None:
        if self._incident_mode() is not IncidentMode.NORMAL:
            self._blocked("incident_authority_freeze", "new authority is frozen by domain incident mode")
        state, _now = self._state()
        if not state.revocation_current or not state.policy_current:
            self._blocked("authority_outage", "new authority issuance is denied while policy/revocation is uncertain")

    def require_privileged(self) -> None:
        if self._incident_mode() in {IncidentMode.FREEZE_PRIVILEGED, IncidentMode.FREEZE_ALL}:
            self._blocked("incident_privileged_freeze", "privileged work is frozen by domain incident mode")
        state, _now = self._state()
        if not state.revocation_current or not state.policy_current:
            self._blocked("privileged_hold", "privileged work is held while policy/revocation is uncertain")

    def require_low_risk_continuity(self) -> None:
        if self._incident_mode() is IncidentMode.FREEZE_ALL:
            self._blocked("incident_full_freeze", "all processing is frozen by domain incident mode")
        state, now = self._state()
        if state.revocation_current and state.policy_current:
            return
        elapsed = int((now - state.last_confirmed_current_at).total_seconds())
        if elapsed < 0 or elapsed > self.policy.low_risk_continuity_max_seconds:
            self._blocked("continuity_expired", "bounded low-risk outage continuity has expired")


__all__ = [
    "HealthProvider",
    "IncidentMode",
    "IncidentModeProvider",
    "OperationalHealth",
    "OutageGate",
    "healthy_operational_state",
]
