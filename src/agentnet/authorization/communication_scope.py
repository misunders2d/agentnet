"""Strict persistent same-principal two-harness communication-scope contract."""

from __future__ import annotations

import base64
import hashlib
from copy import deepcopy
from types import MappingProxyType
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.errors import ConflictError
from agentnet.security.signatures import canonical_json


COMMUNICATION_SCOPE_PROFILE = "same-principal-full-communication:v1"
COMMUNICATION_SCOPE_APPROVAL_PURPOSE = "authorization.communication_scope.approve"
COMMUNICATION_SCOPE_ACTIONS = frozenset(
    {
        "message.send",
        "mailbox.read",
        "mailbox.acknowledge",
        "conversation.create",
        "conversation.message.send",
        "conversation.task.request",
        "conversation.task.handoff",
        "conversation.task.cancel_request",
        "conversation.task.complete",
        "conversation.structured_request.send",
        "conversation.response_obligation.respond",
        "conversation.thread",
        "conversation.response_obligation.create",
        "conversation.response_obligation.read",
        "conversation.response_obligation.transition",
        "conversation.response_obligation.cancel",
        "room.create",
        "room.action",
        "room.read",
    }
)
COMMUNICATION_SCOPE_RESTRICTIONS = MappingProxyType(
    {
        "artifacts_enabled": False,
        "business_effects_enabled": False,
        "federation_enabled": False,
        "public_a2a_enabled": False,
        "authority_expires_at": None,
    }
)

_STRICT = ConfigDict(
    extra="forbid",
    frozen=True,
    populate_by_name=True,
    serialize_by_alias=True,
    strict=True,
)
_IDEMPOTENCY = Field(min_length=16, max_length=256)


class CommunicationScopeBeginRequest(BaseModel):
    """Begin the server-resolved fixed scope; callers select only retry identity."""

    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.begin.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY


class CommunicationScopeStatusRequest(BaseModel):
    """Read a caller-bound scope reservation."""

    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.status.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY


class CommunicationScopeCompleteRequest(BaseModel):
    """Complete the exact independently approved scope reservation."""

    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.complete.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY
    completion_idempotency_key: str = _IDEMPOTENCY


class CommunicationScopeBeginResult(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.begin-result.v1"] = Field(alias="schema")
    status: Literal["approval_pending"]
    approval_url: str = Field(min_length=1)
    expires_at: int = Field(gt=0)


class CommunicationScopeStatusResult(BaseModel):
    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.status-result.v1"] = Field(alias="schema")
    status: Literal[
        "approval_pending",
        "approval_ready",
        "rejected",
        "canceled",
        "expired",
        "invalidated",
    ]
    approval_url: str | None = None
    expires_at: int | None = Field(default=None, gt=0)
    next_action: Literal["complete_automatically"] | None = None

    @model_validator(mode="after")
    def require_exact_state_fields(self) -> "CommunicationScopeStatusResult":
        if self.status == "approval_pending":
            if self.approval_url is None or self.expires_at is None or self.next_action is not None:
                raise ValueError("pending communication-scope status fields are invalid")
        elif self.status == "approval_ready":
            if (
                self.approval_url is None
                or self.expires_at is None
                or self.next_action != "complete_automatically"
            ):
                raise ValueError("ready communication-scope status fields are invalid")
        elif self.approval_url is not None or self.expires_at is not None or self.next_action is not None:
            raise ValueError("terminal communication-scope status fields are invalid")
        return self


class LegacyCommunicationScopeCompleteResult(BaseModel):
    """Previously stored v1 result accepted only for exact upgrade replay."""

    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.complete-result.v1"] = Field(alias="schema")
    status: Literal["communication_active"]
    authority_granted: Literal[True]
    communication_usable: Literal[True]
    authority_expires_at: None
    artifacts_enabled: Literal[False]
    business_effects_enabled: Literal[False]
    federation_enabled: Literal[False]
    public_a2a_enabled: Literal[False]


class CommunicationScopeCompleteResult(BaseModel):
    """Successful authority completion with its exact operational scope."""

    model_config = _STRICT
    schema_id: Literal["agentnet.communication-scope.complete-result.v2"] = Field(alias="schema")
    status: Literal["communication_active"]
    authority_granted: Literal[True]
    communication_usable: Literal[True]
    authority_expires_at: None
    artifacts_enabled: Literal[False]
    business_effects_enabled: Literal[False]
    federation_enabled: Literal[False]
    public_a2a_enabled: Literal[False]
    collaboration_scope_id: str = Field(min_length=16, max_length=256)


ALLOWED_COMMUNICATION_SCOPE_TRANSITIONS = MappingProxyType(
    {
        "reserved": frozenset({"pending_approval", "expired", "invalidated"}),
        "pending_approval": frozenset(
            {"approval_issued", "rejected", "canceled", "expired", "invalidated"}
        ),
        "approval_issued": frozenset({"completion_reserved", "expired", "invalidated"}),
        "completion_reserved": frozenset({"committed", "expired", "invalidated"}),
        "committed": frozenset(),
        "rejected": frozenset(),
        "canceled": frozenset(),
        "expired": frozenset(),
        "invalidated": frozenset(),
    }
)

_PREIMAGE_KEYS = frozenset(
    {
        "schema",
        "approval_purpose",
        "profile",
        "profile_version",
        "independent_boundary_proven",
        "max_uses",
        "begin_idempotency_key_sha256",
        "domain",
        "principal",
        "harnesses",
        "enrollment_evidence",
        "actions",
        "restrictions",
        "issued_at",
        "approval_expires_at",
        "authority_expires_at",
    }
)
_HARNESS_KEYS = frozenset(
    {
        "harness_id",
        "credential_id",
        "credential_epoch",
        "binding_assurance",
        "display_name",
        "kind",
    }
)


def digest_canonical(value: dict[str, object]) -> str:
    """Return the lowercase SHA-256 digest of canonical JSON."""

    return hashlib.sha256(canonical_json(value)).hexdigest()


def _derived_id(
    scope_digest: str,
    kind: Literal["scope", "item", "entitlement"],
    ordinal: int | None = None,
) -> str:
    value: dict[str, object] = {
        "schema": "agentnet.communication-scope.derived-id.v1",
        "scope_digest": scope_digest,
        "kind": kind,
    }
    if ordinal is not None:
        value["ordinal"] = ordinal
    prefix = {"scope": "cs1", "item": "csi1", "entitlement": "ent1"}[kind]
    token = base64.urlsafe_b64encode(hashlib.sha256(canonical_json(value)).digest()).rstrip(b"=")
    return f"{prefix}_{token.decode('ascii')}"


def _positive_int(value: object) -> bool:
    return type(value) is int and value > 0


def _safe_identifier(value: object) -> bool:
    allowed = frozenset(
        "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._:@/"
    )
    return (
        isinstance(value, str)
        and 1 <= len(value) <= 256
        and value[0].isalnum()
        and all(character in allowed for character in value)
    )


def _safe_display_text(value: object) -> bool:
    return (
        isinstance(value, str)
        and 1 <= len(value) <= 128
        and all(ord(character) >= 0x20 and ord(character) != 0x7F for character in value)
    )


def _valid_resolution(preimage: dict[str, Any]) -> bool:
    domain = preimage.get("domain")
    principal = preimage.get("principal")
    harnesses = preimage.get("harnesses")
    evidence = preimage.get("enrollment_evidence")
    if (
        not isinstance(domain, dict)
        or set(domain) != {"domain_id", "policy_revision", "revocation_epoch"}
        or not _safe_identifier(domain["domain_id"])
        or not _positive_int(domain["policy_revision"])
        or not _positive_int(domain["revocation_epoch"])
        or not isinstance(principal, dict)
        or set(principal) != {"principal_id"}
        or not _safe_identifier(principal["principal_id"])
        or not isinstance(harnesses, dict)
        or set(harnesses) != {"owner", "fresh"}
        or not isinstance(evidence, dict)
        or set(evidence) != {"owner", "fresh"}
    ):
        return False
    for role in ("owner", "fresh"):
        harness = harnesses.get(role)
        role_evidence = evidence.get(role)
        if (
            not isinstance(harness, dict)
            or frozenset(harness) != _HARNESS_KEYS
            or not _safe_identifier(harness["harness_id"])
            or not _safe_identifier(harness["credential_id"])
            or not _safe_identifier(harness["kind"])
            or not _safe_display_text(harness["display_name"])
            or not _positive_int(harness["credential_epoch"])
            or harness["binding_assurance"] not in {"os_bound", "hardware_bound"}
            or not isinstance(role_evidence, dict)
            or role_evidence.get("role") != role
        ):
            return False
    return (
        harnesses["owner"]["harness_id"] != harnesses["fresh"]["harness_id"]
        and harnesses["owner"]["credential_id"] != harnesses["fresh"]["credential_id"]
    )


def build_communication_scope_transaction(
    canonical_scope_preimage: dict[str, Any],
) -> dict[str, Any]:
    """Build the exact deterministic fixed-action, exact-harness Approval transaction."""

    preimage = deepcopy(canonical_scope_preimage)
    issued_at = preimage.get("issued_at")
    approval_expires_at = preimage.get("approval_expires_at")
    valid = (
        frozenset(preimage) == _PREIMAGE_KEYS
        and preimage.get("schema") == "agentnet.communication-scope.preimage.v1"
        and preimage.get("approval_purpose") == COMMUNICATION_SCOPE_APPROVAL_PURPOSE
        and preimage.get("profile") == COMMUNICATION_SCOPE_PROFILE
        and type(preimage.get("profile_version")) is int
        and preimage.get("profile_version") == 1
        and type(preimage.get("independent_boundary_proven")) is bool
        and preimage.get("independent_boundary_proven") is False
        and type(preimage.get("max_uses")) is int
        and preimage.get("max_uses") == 1
        and isinstance(preimage.get("begin_idempotency_key_sha256"), str)
        and len(preimage["begin_idempotency_key_sha256"]) == 64
        and all(
            character in "0123456789abcdef"
            for character in preimage["begin_idempotency_key_sha256"]
        )
        and preimage.get("actions") == sorted(COMMUNICATION_SCOPE_ACTIONS)
        and preimage.get("restrictions") == dict(COMMUNICATION_SCOPE_RESTRICTIONS)
        and preimage.get("authority_expires_at") is None
        and _positive_int(issued_at)
        and _positive_int(approval_expires_at)
        and approval_expires_at == issued_at + 3_600
        and _valid_resolution(preimage)
    )
    if not valid:
        raise ValueError("communication scope preimage is invalid")

    scope_digest = digest_canonical(preimage)
    scope_id = _derived_id(scope_digest, "scope")
    domain = preimage["domain"]
    principal = preimage["principal"]
    items: list[dict[str, Any]] = []
    ordinal = 0
    for role in ("owner", "fresh"):
        harness_id = preimage["harnesses"][role]["harness_id"]
        for action in sorted(COMMUNICATION_SCOPE_ACTIONS):
            ordinal += 1
            entitlement = {
                "entitlement_id": _derived_id(scope_digest, "entitlement", ordinal),
                "domain_id": domain["domain_id"],
                "principal_id": principal["principal_id"],
                "action": action,
                "resource_pattern": "*",
                "revision": domain["policy_revision"],
                "expires_at": None,
                "revoked_at": None,
            }
            items.append(
                {
                    "item_ordinal": ordinal,
                    "item_id": _derived_id(scope_digest, "item", ordinal),
                    "scope_id": scope_id,
                    "harness_id": harness_id,
                    "action": action,
                    "resource_pattern": "*",
                    "entitlement": entitlement,
                }
            )
    return {
        "schema": "agentnet.communication-scope.transaction.v1",
        "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
        "scope_digest": scope_digest,
        "canonical_scope_preimage": preimage,
        "scope_id": scope_id,
        "items": items,
        "item_count": len(items),
        "restrictions": dict(COMMUNICATION_SCOPE_RESTRICTIONS),
    }


def require_communication_scope_transition(current: str, target: str) -> None:
    """Reject unknown, terminal, backward, or skipped state changes."""

    if current not in ALLOWED_COMMUNICATION_SCOPE_TRANSITIONS:
        raise ConflictError("communication scope state transition rejected")
    if target not in ALLOWED_COMMUNICATION_SCOPE_TRANSITIONS[current]:
        raise ConflictError("communication scope state transition rejected")


__all__ = [
    "ALLOWED_COMMUNICATION_SCOPE_TRANSITIONS",
    "COMMUNICATION_SCOPE_ACTIONS",
    "COMMUNICATION_SCOPE_APPROVAL_PURPOSE",
    "COMMUNICATION_SCOPE_PROFILE",
    "COMMUNICATION_SCOPE_RESTRICTIONS",
    "CommunicationScopeBeginRequest",
    "CommunicationScopeBeginResult",
    "CommunicationScopeCompleteResult",
    "LegacyCommunicationScopeCompleteResult",
    "CommunicationScopeCompleteRequest",
    "CommunicationScopeStatusRequest",
    "CommunicationScopeStatusResult",
    "build_communication_scope_transaction",
    "digest_canonical",
    "require_communication_scope_transition",
]
