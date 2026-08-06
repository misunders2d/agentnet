"""Strict redacted read models for the administration console."""

from __future__ import annotations

import ipaddress
from enum import StrEnum
import re
from typing import Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator


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

_WORK_EMAIL = re.compile(
    r"^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@"
    r"(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+"
    r"[A-Za-z]{2,63}$"
)
INVITATION_PERMISSION_ACTIONS = frozenset(
    {
        "artifact.download",
        "artifact.send",
        "message.read",
        "message.send",
    }
)


class InvitationCreationForm(_ConsoleModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    email: str = Field(min_length=3, max_length=320)
    scope_id: str = Field(min_length=1, max_length=256)
    permissions: tuple[str, ...] = Field(min_length=1, max_length=4)

    @field_validator("email")
    @classmethod
    def normalize_work_email(cls, value: str) -> str:
        normalized = value.strip().casefold()
        local_part = normalized.partition("@")[0]
        if (
            normalized != value.strip().lower()
            or not _WORK_EMAIL.fullmatch(normalized)
            or local_part.startswith(".")
            or local_part.endswith(".")
            or ".." in local_part
        ):
            raise ValueError("Enter a valid work email")
        return normalized

    @field_validator("scope_id")
    @classmethod
    def validate_scope_id(cls, value: str) -> str:
        if value != value.strip() or any(ord(character) < 33 for character in value):
            raise ValueError("Choose an available space")
        return value

    @field_validator("permissions")
    @classmethod
    def validate_permissions(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if (
            len(set(value)) != len(value)
            or not set(value).issubset(INVITATION_PERMISSION_ACTIONS)
        ):
            raise ValueError("Choose only the available message and file actions")
        return tuple(sorted(value))


class InvitationScopeChoice(_ConsoleModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    scope_id: str = Field(min_length=1, max_length=256)
    display_name: str = Field(min_length=1, max_length=128)


class InvitationDetail(_ConsoleModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    work_email: str = Field(min_length=3, max_length=320)
    space: str = Field(min_length=1, max_length=128)
    permissions: tuple[str, ...] = Field(min_length=1, max_length=4)
    invitation_url: str = Field(max_length=4_096)
    qr_svg: str = Field(max_length=2_000_000)
    expires_at: int = Field(ge=1)
    revoked: bool = False

    @field_validator("invitation_url")
    @classmethod
    def require_https_url(cls, value: str) -> str:
        if value:
            parsed = urlsplit(value)
            if (
                parsed.scheme != "https"
                or not parsed.hostname
                or parsed.username is not None
                or parsed.password is not None
                or parsed.query
                or parsed.fragment
            ):
                raise ValueError("Invitation links must use canonical HTTPS")
        return value


class InvitationContinuationResult(_ConsoleModel):
    """Safe browser transition returned by the package-owned invitation flow."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    state: Literal[
        "authorization_required",
        "waiting_approval",
        "restart_required",
        "active",
    ]
    authorization_url: str | None = Field(default=None, max_length=4_096)

    @model_validator(mode="after")
    def require_exact_transition(self) -> "InvitationContinuationResult":
        if self.state == "authorization_required":
            if self.authorization_url is None:
                raise ValueError("Work-account authorization URL is required")
            parsed = urlsplit(self.authorization_url)
            try:
                literal_address = ipaddress.ip_address(parsed.hostname or "")
            except ValueError:
                literal_address = None
            if (
                parsed.scheme != "https"
                or not parsed.hostname
                or parsed.username is not None
                or parsed.password is not None
                or parsed.fragment
                or parsed.hostname.casefold() == "localhost"
                or (
                    literal_address is not None
                    and (
                        literal_address.is_loopback
                        or literal_address.is_private
                        or literal_address.is_unspecified
                    )
                )
            ):
                raise ValueError("Work-account authorization must use canonical public HTTPS")
        elif self.authorization_url is not None:
            raise ValueError("Terminal invitation state cannot carry an authorization URL")
        return self



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
    "INVITATION_PERMISSION_ACTIONS",
    "InvitationCreationForm",
    "InvitationDetail",
    "InvitationContinuationResult",
    "InvitationScopeChoice",
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
