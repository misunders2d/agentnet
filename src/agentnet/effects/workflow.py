"""Strict workflow-result seam; workflow state cannot fabricate effects."""

from __future__ import annotations

import secrets
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
)

from agentnet.effects.reservations import EffectState
from agentnet.errors import ConflictError, ValidationError


class WorkflowState(StrEnum):
    PENDING = "pending"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELED = "canceled"
    TIMED_OUT = "timed_out"


class WorkflowTerminalReceiptV1(BaseModel):
    """Exact terminal-effect receipt emitted through a workflow adapter.

    This is only a wire model.  Neither its ``fact`` nor its signature field is
    trusted until a configured adapter verifier checks the exact workflow,
    effect, workload credential/epoch, signature, freshness, and external
    evidence digest.  There is deliberately no caller-supplied ``verified``
    flag.
    """

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"]
    workflow_id: str = Field(min_length=1, max_length=256)
    workflow_run_id: str = Field(min_length=1, max_length=256)
    effect_id: str = Field(min_length=1, max_length=256)
    attempt_id: str = Field(min_length=16, max_length=256)
    fact: Literal[
        EffectState.SUCCEEDED,
        EffectState.FAILED,
        EffectState.CANCELLED,
    ]
    external_receipt_id: str = Field(min_length=8, max_length=512)
    external_receipt_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    issuer_registration_id: str = Field(min_length=16, max_length=128)
    issuer_credential_epoch: int = Field(ge=1)
    issuer_revocation_epoch: int = Field(ge=1)
    observed_at: int = Field(gt=0)
    nonce: str = Field(min_length=24, max_length=256)
    signature: str = Field(min_length=1, max_length=4096)

    @classmethod
    def parse_boundary(cls, value: object) -> "WorkflowTerminalReceiptV1":
        try:
            return cls.model_validate(value, strict=True)
        except PydanticValidationError as exc:
            raise ValidationError(
                "workflow terminal receipt does not match the exact v1 schema"
            ) from exc


_VERIFIED_RECEIPT_SEAL = object()


@dataclass(frozen=True, slots=True, init=False)
class VerifiedWorkflowTerminalReceipt:
    """Opaque result of the configured workflow-receipt verifier."""

    receipt: WorkflowTerminalReceiptV1
    _seal: object = field(repr=False, compare=False)

    def __init__(self, receipt: WorkflowTerminalReceiptV1, *, _seal: object) -> None:
        if _seal is not _VERIFIED_RECEIPT_SEAL:
            raise TypeError("verified workflow receipt can only be minted by the verifier boundary")
        object.__setattr__(self, "receipt", receipt)
        object.__setattr__(self, "_seal", _seal)


class WorkflowReceiptVerifier(ABC):
    """Configured adapter that authenticates a parsed terminal receipt."""

    @abstractmethod
    def verify(self, receipt: WorkflowTerminalReceiptV1) -> None:
        """Authenticate current issuer/effect evidence or raise."""


def verify_workflow_terminal_receipt(
    value: object,
    *,
    expected_workflow_id: str,
    expected_effect_id: str,
    verifier: WorkflowReceiptVerifier,
) -> VerifiedWorkflowTerminalReceipt:
    """Parse, bind, then authenticate one terminal receipt.

    ``verifier`` is a configured trust-boundary implementation.  It must raise
    unless the issuer signature, current workload credential/revocation epoch,
    freshness, and external evidence are valid.  Returning a boolean is not
    supported, preventing a caller-asserted value from becoming trust.
    """

    if not isinstance(verifier, WorkflowReceiptVerifier):
        raise ValidationError("workflow terminal receipt verifier is required")
    parsed = WorkflowTerminalReceiptV1.parse_boundary(value)
    if not secrets.compare_digest(parsed.workflow_id, expected_workflow_id):
        raise ConflictError("workflow terminal receipt binds another workflow")
    if not secrets.compare_digest(parsed.effect_id, expected_effect_id):
        raise ConflictError("workflow terminal receipt binds another effect")
    result = verifier.verify(parsed)
    if result is not None:
        raise ValidationError("workflow receipt verifier must authenticate by success or raise")
    return VerifiedWorkflowTerminalReceipt(parsed, _seal=_VERIFIED_RECEIPT_SEAL)


def terminal_effect_from_workflow(
    workflow_state: WorkflowState | str,
    effect_receipt: VerifiedWorkflowTerminalReceipt | None,
) -> str:
    """Map workflow state without treating workflow completion as effect proof."""

    try:
        state = WorkflowState(workflow_state)
    except (TypeError, ValueError) as exc:
        raise ValidationError("workflow state is unsupported") from exc
    if effect_receipt is not None and not isinstance(
        effect_receipt, VerifiedWorkflowTerminalReceipt
    ):
        raise ValidationError("effect receipt has not crossed the authenticated verifier boundary")
    if state is WorkflowState.COMPLETED and effect_receipt is None:
        raise ConflictError("workflow completion cannot fabricate external effect evidence")
    if state is not WorkflowState.COMPLETED and effect_receipt is not None:
        raise ConflictError("terminal effect receipt contradicts non-completed workflow state")
    return (
        effect_receipt.receipt.fact.value
        if effect_receipt is not None
        else EffectState.UNKNOWN.value
    )


__all__ = [
    "VerifiedWorkflowTerminalReceipt",
    "WorkflowReceiptVerifier",
    "WorkflowState",
    "WorkflowTerminalReceiptV1",
    "terminal_effect_from_workflow",
    "verify_workflow_terminal_receipt",
]
