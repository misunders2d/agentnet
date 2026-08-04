"""Strict redacted read models for the administration console."""

from __future__ import annotations

from enum import StrEnum

from pydantic import BaseModel, ConfigDict, Field


class VisibleState(StrEnum):
    ONLINE = "Online"
    OFFLINE = "Offline"
    RECENT = "Recent"
    STALE = "Stale"
    WAITING_SERVER = "Waiting for server"
    WAITING_APPROVAL = "Waiting for approval"
    COMPLETED = "Completed"
    FAILED = "Failed"
    EXPIRED = "Expired"
    CANCELED = "Canceled"
    BLOCKED = "Blocked"
    ACCESS_REMOVED = "Access removed"
    EXPIRES_SOON = "Expires soon"
    UNKNOWN = "Unknown — needs reconciliation"


class _ConsoleModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class HomeSummary(_ConsoleModel):
    state: VisibleState
    server_total: int = Field(ge=0)
    server_online: int = Field(ge=0)
    people_total: int = Field(ge=0)
    agent_total: int = Field(ge=0)
    approvals_waiting: int = Field(ge=0)
    security_issues: int = Field(ge=0)
    fresh_at: int


class ServerSummary(_ConsoleModel):
    harness_id: str
    friendly_name: str
    kind: str
    state: VisibleState
    last_checked_at: int | None
    capabilities: tuple[str, ...]
    blockers: tuple[str, ...]
    access_state: str
    technical: dict[str, str] | None = None


class ServerPage(_ConsoleModel):
    servers: tuple[ServerSummary, ...]
    fresh_at: int


class HarnessSummary(_ConsoleModel):
    harness_id: str
    friendly_name: str
    kind: str
    access_state: str
    credential_state: str
    credential_expires_at: int | None
    can_remove: bool = False
    technical: dict[str, str] | None = None


class PersonSummary(_ConsoleModel):
    principal_id: str
    domain_id: str
    display_name: str
    access_state: str
    harnesses: tuple[HarnessSummary, ...]


class RelationshipSummary(_ConsoleModel):
    relationship_id: str
    direction: str
    person: str
    scope: str
    state: str
    expires_at: int | None


class PersonPage(_ConsoleModel):
    people: tuple[PersonSummary, ...]
    relationships: tuple[RelationshipSummary, ...]
    fresh_at: int


class ApprovalSummary(_ConsoleModel):
    request_id: str
    title: str
    person: str
    harness: str | None
    capabilities: tuple[str, ...]
    consequence: str
    state: VisibleState
    expires_at: int
    action_path: str | None = None
    action_confirmation: str | None = None
    action_label: str | None = None


class ApprovalPage(_ConsoleModel):
    approvals: tuple[ApprovalSummary, ...]
    fresh_at: int


class SecurityIssue(_ConsoleModel):
    issue_id: str
    title: str
    description: str
    state: VisibleState
    occurred_at: int | None
    action_path: str | None = None


class SecurityPage(_ConsoleModel):
    issues: tuple[SecurityIssue, ...]
    incident_mode: str
    audit_healthy: bool
    fresh_at: int


class ActivitySummary(_ConsoleModel):
    event_id: str
    occurred_at: int
    actor: str
    action: str
    resource: str
    result: str
    server: str | None
    technical: dict[str, str] | None = None


class ActivityPage(_ConsoleModel):
    events: tuple[ActivitySummary, ...]
    fresh_at: int


__all__ = [
    "ActivityPage",
    "ActivitySummary",
    "ApprovalPage",
    "ApprovalSummary",
    "HarnessSummary",
    "HomeSummary",
    "PersonPage",
    "PersonSummary",
    "RelationshipSummary",
    "SecurityIssue",
    "SecurityPage",
    "ServerPage",
    "ServerSummary",
    "VisibleState",
]
