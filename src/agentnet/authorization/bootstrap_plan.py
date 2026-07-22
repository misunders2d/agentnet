"""Strict contract for the bounded same-principal two-harness C0 plan."""

from __future__ import annotations

import base64
import hashlib
from copy import deepcopy
from types import MappingProxyType
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.errors import ConflictError
from agentnet.security.signatures import canonical_json


BOOTSTRAP_PLAN_PROFILE = "ordinary-two-harness-c0:v1"
BOOTSTRAP_PLAN_APPROVAL_PURPOSE = "authorization.bootstrap_plan.approve"
BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT = "pending"
COMMUNICATION_ENTITLEMENT_COUNT = 5
REVOCATION_ENTITLEMENT_COUNT = 5
TOTAL_ENTITLEMENT_COUNT = COMMUNICATION_ENTITLEMENT_COUNT + REVOCATION_ENTITLEMENT_COUNT
BOOTSTRAP_PLAN_ACTIONS = frozenset(
    {
        "message.send",
        "mailbox.read",
        "mailbox.acknowledge",
        "authorization.entitlement.revoke",
    }
)
BOOTSTRAP_PLAN_RESOURCES = frozenset(
    {
        "direct",
        "harness:<resolved-owner>",
        "harness:<resolved-fresh>",
        "entitlement:<resolved-communication-entitlement>",
    }
)

_STRICT = ConfigDict(extra="forbid", frozen=True, strict=True)
_IDEMPOTENCY = Field(min_length=16, max_length=256)


class BootstrapPlanBeginRequest(BaseModel):
    """Caller selects only retry identity; Core owns profile, peers, IDs, and TTL."""

    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.begin.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY


class BootstrapPlanStatusRequest(BaseModel):
    """Read the state of the exact caller-bound begin reservation."""

    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.status.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY


class BootstrapPlanCompletionRequest(BaseModel):
    """Complete an existing server-owned plan with a private Approval claim code."""

    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.complete.v1"] = Field(alias="schema")
    begin_idempotency_key: str = _IDEMPOTENCY
    completion_idempotency_key: str = _IDEMPOTENCY
    claim_code: str = Field(pattern=r"^[0-9A-Fa-f]{4}(?:-[0-9A-Fa-f]{4}){7}$")


class BootstrapPlanBeginResult(BaseModel):
    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.begin-result.v1"] = Field(alias="schema")
    status: Literal["approval_pending"]
    approval_url: str
    expires_at: int = Field(gt=0)


class BootstrapPlanStatusResult(BaseModel):
    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.status-result.v1"] = Field(alias="schema")
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
    next_action: Literal["enter_claim_code_in_masked_local_tty"] | None = None

    @model_validator(mode="after")
    def require_exact_state_fields(self) -> "BootstrapPlanStatusResult":
        if self.status == "approval_pending":
            if self.approval_url is None or self.expires_at is None or self.next_action is not None:
                raise ValueError("pending bootstrap status fields are invalid")
        elif self.status == "approval_ready":
            if (
                self.approval_url is None
                or self.expires_at is None
                or self.next_action != "enter_claim_code_in_masked_local_tty"
            ):
                raise ValueError("ready bootstrap status fields are invalid")
        elif self.approval_url is not None or self.expires_at is not None or self.next_action is not None:
            raise ValueError("terminal bootstrap status fields are invalid")
        return self


class BootstrapPlanCompleteResult(BaseModel):
    """Only public/model-visible S4 completion output."""

    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.complete-result.v1"] = Field(alias="schema")
    status: Literal["prepared_unusable"]
    authority_granted: Literal[False]
    communication_usable: Literal[False]


class BootstrapPlanErrorResult(BaseModel):
    model_config = _STRICT

    schema_id: Literal["agentnet.bootstrap-plan.error.v1"] = Field(alias="schema")
    code: Literal[
        "invalid_request",
        "authentication_denied",
        "bootstrap_plan_denied",
        "bootstrap_plan_conflict",
        "bootstrap_plan_terminal",
        "bootstrap_plan_unavailable",
    ]
    message: Literal["request denied"]
    retryable: bool


ALLOWED_BOOTSTRAP_PLAN_TRANSITIONS = MappingProxyType(
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
C0_REQUIRED_FACTS = (
    "request_durable_custody",
    "request_retrieved",
    "request_recipient_acknowledged",
    "reply_sent",
    "reply_durable_custody",
    "reply_retrieved",
    "reply_final_acknowledged",
)
SANITIZED_C0_SUCCESS = "COMPLETED_C0_ROUND_TRIP"


def digest_canonical(value: dict[str, object]) -> str:
    """Return the plan protocol's lowercase SHA-256 canonical digest."""

    return hashlib.sha256(canonical_json(value)).hexdigest()


def _derived_id(plan_digest: str, kind: str, ordinal: int | None = None) -> str:
    value: dict[str, Any] = {
        "schema": "agentnet.bootstrap-plan.derived-id.v1",
        "plan_digest": plan_digest,
        "kind": kind,
    }
    if ordinal is not None:
        value["ordinal"] = ordinal
    prefix = {"plan": "bp1", "guard": "c0g1", "item": "bpi1", "entitlement": "ent1"}[kind]
    token = base64.urlsafe_b64encode(hashlib.sha256(canonical_json(value)).digest()).rstrip(b"=")
    return f"{prefix}_{token.decode('ascii')}"


def _payload_schema(role: str, message: str) -> dict[str, Any]:
    schema = f"agentnet.c0-pilot.{role}-payload.v1"
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": schema,
        "type": "object",
        "additionalProperties": False,
        "required": ["schema", "profile", "role", "message"],
        "properties": {
            "schema": {"const": schema},
            "profile": {"const": BOOTSTRAP_PLAN_PROFILE},
            "role": {"const": role},
            "message": {"const": message},
        },
    }


def _payload(role: str, message: str) -> dict[str, Any]:
    return {
        "schema": f"agentnet.c0-pilot.{role}-payload.v1",
        "profile": BOOTSTRAP_PLAN_PROFILE,
        "role": role,
        "message": message,
    }


def bootstrap_plan_c0_binding() -> dict[str, str]:
    request_message = "AgentNet C0 pilot request: fixed harmless transport check."
    reply_message = "AgentNet C0 pilot reply: fixed harmless transport check."
    request_schema = _payload_schema("request", request_message)
    reply_schema = _payload_schema("reply", reply_message)
    request_payload = _payload("request", request_message)
    reply_payload = _payload("reply", reply_message)
    return {
        "classification": "C0",
        "request_payload_schema_digest": digest_canonical(request_schema),
        "request_payload_digest": digest_canonical(request_payload),
        "reply_payload_schema_digest": digest_canonical(reply_schema),
        "reply_payload_digest": digest_canonical(reply_payload),
    }


def build_bootstrap_plan_transaction(canonical_plan_preimage: dict[str, Any]) -> dict[str, Any]:
    """Build the one exact ten-item Approval transaction from server-owned inputs."""

    preimage = deepcopy(canonical_plan_preimage)
    plan_digest = digest_canonical(preimage)
    plan_id = _derived_id(plan_digest, "plan")
    guard_id = _derived_id(plan_digest, "guard")
    item_ids = {ordinal: _derived_id(plan_digest, "item", ordinal) for ordinal in range(1, 11)}
    entitlement_ids = {
        ordinal: _derived_id(plan_digest, "entitlement", ordinal) for ordinal in range(1, 11)
    }
    domain = preimage["domain"]
    principal = preimage["principal"]
    harnesses = preimage["harnesses"]
    issued_at = int(preimage["issued_at"])
    approval_expires_at = int(preimage["approval_expires_at"])
    authority_expires_at = int(preimage["authority_expires_at"])
    if (
        preimage.get("schema") != "agentnet.bootstrap-plan.preimage.v1"
        or preimage.get("approval_purpose") != BOOTSTRAP_PLAN_APPROVAL_PURPOSE
        or preimage.get("profile") != BOOTSTRAP_PLAN_PROFILE
        or preimage.get("profile_version") != 1
        or preimage.get("independent_boundary_proven") is not False
        or preimage.get("max_uses") != 1
        or preimage.get("communication_ttl_seconds") != 3600
        or not issued_at < approval_expires_at <= issued_at + 300
        or not approval_expires_at < authority_expires_at <= issued_at + 3600
        or harnesses["owner"]["harness_id"] == harnesses["fresh"]["harness_id"]
    ):
        raise ValueError("bootstrap plan preimage is invalid")

    request_message = "AgentNet C0 pilot request: fixed harmless transport check."
    reply_message = "AgentNet C0 pilot reply: fixed harmless transport check."
    request_schema = _payload_schema("request", request_message)
    reply_schema = _payload_schema("reply", reply_message)
    request_payload = _payload("request", request_message)
    reply_payload = _payload("reply", reply_message)

    def entitlement(ordinal: int, action: str, resource: str) -> dict[str, Any]:
        return {
            "entitlement_id": entitlement_ids[ordinal],
            "domain_id": domain["domain_id"],
            "principal_id": principal["principal_id"],
            "action": action,
            "resource_pattern": resource,
            "revision": domain["policy_revision"],
            "expires_at": authority_expires_at,
            "revoked_at": None,
        }

    communication = (
        (1, "message.send", "direct", ["fresh_to_owner_send", "owner_to_fresh_send"]),
        (2, "mailbox.read", harnesses["owner"]["harness_id"], ["owner_mailbox_read"]),
        (3, "mailbox.acknowledge", harnesses["owner"]["harness_id"], ["owner_mailbox_acknowledge"]),
        (4, "mailbox.read", harnesses["fresh"]["harness_id"], ["fresh_mailbox_read"]),
        (5, "mailbox.acknowledge", harnesses["fresh"]["harness_id"], ["fresh_mailbox_acknowledge"]),
    )
    items: list[dict[str, Any]] = []
    for ordinal, action, resource, scopes in communication:
        items.append(
            {
                "item_ordinal": ordinal,
                "item_id": item_ids[ordinal],
                "plan_id": plan_id,
                "domain_id": domain["domain_id"],
                "principal_id": principal["principal_id"],
                "revision": domain["policy_revision"],
                "item_kind": "communication",
                "action": action,
                "resource_pattern": resource,
                "guard_id": guard_id,
                "operation_scopes": scopes,
                "expires_at": authority_expires_at,
                "entitlement": entitlement(ordinal, action, resource),
            }
        )
    for ordinal in range(6, 11):
        target_ordinal = ordinal - 5
        target = entitlement_ids[target_ordinal]
        resource = f"entitlement:{target}"
        items.append(
            {
                "item_ordinal": ordinal,
                "item_id": item_ids[ordinal],
                "plan_id": plan_id,
                "domain_id": domain["domain_id"],
                "principal_id": principal["principal_id"],
                "revision": domain["policy_revision"],
                "item_kind": "exact_revoke",
                "action": "authorization.entitlement.revoke",
                "resource_pattern": resource,
                "guard_id": guard_id,
                "target_communication_ordinal": target_ordinal,
                "target_entitlement_id": target,
                "expires_at": authority_expires_at,
                "entitlement": entitlement(
                    ordinal, "authorization.entitlement.revoke", resource
                ),
            }
        )
    guard = {
        "schema": "agentnet.c0-plan-guard.v1",
        "guard_id": guard_id,
        "classification": "C0",
        "owner_harness_id": harnesses["owner"]["harness_id"],
        "fresh_harness_id": harnesses["fresh"]["harness_id"],
        "request_remaining_uses": 1,
        "reply_remaining_uses": 1,
        "request_payload_schema": request_schema,
        "request_payload_schema_digest": digest_canonical(request_schema),
        "request_payload": request_payload,
        "request_payload_digest": digest_canonical(request_payload),
        "reply_payload_schema": reply_schema,
        "reply_payload_schema_digest": digest_canonical(reply_schema),
        "reply_payload": reply_payload,
        "reply_payload_digest": digest_canonical(reply_payload),
        "state_at_commit": BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT,
    }
    return {
        "schema": "agentnet.bootstrap-plan.transaction.v1",
        "approval_purpose": BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
        "plan_digest": plan_digest,
        "canonical_plan_preimage": preimage,
        "plan_id": plan_id,
        "guard": guard,
        "items": items,
        "item_count": 10,
        "entitlement_count": 10,
    }


def require_bootstrap_plan_transition(current: str, target: str) -> None:
    """Reject unknown, terminal, backward, or implicit plan state changes."""

    if current not in ALLOWED_BOOTSTRAP_PLAN_TRANSITIONS:
        raise ConflictError("bootstrap plan state transition rejected")
    if target not in ALLOWED_BOOTSTRAP_PLAN_TRANSITIONS[current]:
        raise ConflictError("bootstrap plan state transition rejected")


__all__ = [
    "ALLOWED_BOOTSTRAP_PLAN_TRANSITIONS",
    "BOOTSTRAP_PLAN_ACTIONS",
    "BOOTSTRAP_PLAN_APPROVAL_PURPOSE",
    "BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT",
    "BOOTSTRAP_PLAN_PROFILE",
    "BOOTSTRAP_PLAN_RESOURCES",
    "BootstrapPlanBeginRequest",
    "BootstrapPlanBeginResult",
    "BootstrapPlanCompleteResult",
    "BootstrapPlanCompletionRequest",
    "BootstrapPlanErrorResult",
    "BootstrapPlanStatusRequest",
    "BootstrapPlanStatusResult",
    "C0_REQUIRED_FACTS",
    "COMMUNICATION_ENTITLEMENT_COUNT",
    "REVOCATION_ENTITLEMENT_COUNT",
    "SANITIZED_C0_SUCCESS",
    "TOTAL_ENTITLEMENT_COUNT",
    "bootstrap_plan_c0_binding",
    "build_bootstrap_plan_transaction",
    "digest_canonical",
    "require_bootstrap_plan_transition",
]
