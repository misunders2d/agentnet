"""Harness-scoped revocation without sibling authority changes."""

from __future__ import annotations

import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_approver_entitlement,
    require_current_authority_decision,
)
from agentnet.authorization.grants import TaskGrantService
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.domains import validate_domain_id
from agentnet.organization.relationships import RelationshipService
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.sqlite import SQLiteStore


@dataclass(frozen=True, slots=True)
class HarnessRevocationResult:
    domain_id: str
    harness_id: str
    credential_epoch: int
    domain_revocation_epoch: int
    revoked_credentials: int
    already_revoked: bool


class HarnessRevocationRequest(BaseModel):
    """Exact, state-fenced high-impact harness revocation transaction."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str = Field(min_length=1)
    harness_id: str = Field(min_length=1)
    expected_credential_epoch: int = Field(ge=1)
    expected_domain_revocation_epoch: int = Field(ge=1)
    reason: str = Field(min_length=1, max_length=512)

    def canonical_transaction(self) -> dict[str, object]:
        return {
            "type": "harness_revocation",
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "harness_id": self.harness_id,
            "expected_credential_epoch": self.expected_credential_epoch,
            "expected_domain_revocation_epoch": self.expected_domain_revocation_epoch,
            "reason": self.reason,
        }

    @property
    def transaction_digest(self) -> str:
        return canonical_digest(self.canonical_transaction())


class HarnessRevocationService:
    APPROVAL_PURPOSE = "identity.harness.revoke.approve"

    def __init__(
        self,
        store: SQLiteStore,
        approval_verifier: IndependentApprovalVerifier | None = None,
        *,
        relationships: RelationshipService | None = None,
        task_grants: TaskGrantService | None = None,
    ) -> None:
        self.store = store
        self.approval_verifier = approval_verifier
        self.relationships = relationships or RelationshipService(store)
        self.task_grants = task_grants or TaskGrantService(store)

    def prepare(self, *, domain_id: str, harness_id: str, reason: str) -> HarnessRevocationRequest:
        """Read current fencing epochs for an independently approved request."""

        validate_domain_id(domain_id)
        if not reason or len(reason) > 512:
            raise ValidationError("revocation reason is required")
        harness = self.store.fetch_one(
            "SELECT * FROM harnesses WHERE domain_id=? AND harness_id=?",
            (domain_id, harness_id),
        )
        domain = self.store.fetch_one("SELECT * FROM domains WHERE domain_id=?", (domain_id,))
        if harness is None:
            raise AuthenticationError("harness binding is unavailable")
        if domain is None:
            raise AuthenticationError("trust domain is unavailable")
        return HarnessRevocationRequest(
            domain_id=domain_id,
            harness_id=harness_id,
            expected_credential_epoch=int(harness["credential_epoch"]),
            expected_domain_revocation_epoch=int(domain["revocation_epoch"]),
            reason=reason,
        )

    @staticmethod
    def authority_binding(request: HarnessRevocationRequest) -> tuple[str, dict[str, str]]:
        return (
            f"harness:{request.harness_id}",
            {"request_digest": request.transaction_digest},
        )

    def revoke(
        self,
        *,
        request: HarnessRevocationRequest | None = None,
        authority: IssuanceAuthority | None = None,
        approval: Mapping[str, Any] | None = None,
        # Legacy named-target inputs remain accepted only to produce an
        # explicit fail-closed error instead of silently bypassing evidence.
        domain_id: str | None = None,
        harness_id: str | None = None,
        reason: str | None = None,
        now: int | None = None,
        commit_callback: Callable[[Any, HarnessRevocationResult], None] | None = None,
    ) -> HarnessRevocationResult:
        if request is None:
            if domain_id is not None or harness_id is not None or reason is not None:
                raise AuthorizationError(
                    "unguarded named-harness revocation is disabled; use a structured approved request"
                )
            raise AuthorizationError("structured harness revocation request is required")
        if any(value is not None for value in (domain_id, harness_id, reason)):
            raise ValidationError("structured and legacy revocation inputs cannot be mixed")
        validate_domain_id(request.domain_id)
        if self.approval_verifier is None:
            raise AuthorizationError("independent approval verifier is required for harness revocation")
        if approval is None:
            raise AuthorizationError("independent signed approval is required for harness revocation")
        revoked_at = int(time.time()) if now is None else now
        when = datetime.fromtimestamp(revoked_at, UTC)
        verified_approval = self.approval_verifier.verify(
            canonical_transaction=canonical_json(request.canonical_transaction()),
            approval=approval,
            expected_purpose=self.APPROVAL_PURPOSE,
            expected_domain_id=request.domain_id,
            when=when,
        )
        if authority is None:
            raise AuthorizationError("authenticated privileged revocation actor is required")
        if verified_approval.approver_principal_id == authority.actor.positive_authority_id:
            raise AuthorizationError("revocation actor cannot supply its own independent approval")

        with self.store.transaction() as connection:
            resource, expected_request = self.authority_binding(request)
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="identity.harness.revoke",
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if authority.actor.domain_id != request.domain_id:
                raise AuthorizationError("revocation actor domain binding mismatch")
            require_current_approver_entitlement(
                connection,
                domain_id=request.domain_id,
                approver_principal_id=verified_approval.approver_principal_id,
                action=self.APPROVAL_PURPOSE,
                resource=resource,
                policy_revision=policy_revision,
                when=when,
            )

            harness = connection.execute(
                "SELECT * FROM harnesses WHERE domain_id=? AND harness_id=?",
                (request.domain_id, request.harness_id),
            ).fetchone()
            if harness is None:
                raise AuthenticationError("harness binding is unavailable")
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?",
                (request.domain_id,),
            ).fetchone()
            if domain is None:
                raise AuthenticationError("trust domain is unavailable")
            if (
                int(harness["credential_epoch"]) != request.expected_credential_epoch
                or int(domain["revocation_epoch"]) != request.expected_domain_revocation_epoch
            ):
                raise ConflictError("harness revocation fencing epoch changed before commit")

            consume_independent_approval(connection, receipt=verified_approval)
            if harness["status"] == "revoked":
                self.relationships._cascade_revoke_for_harness_in_transaction(
                    connection,
                    harness_id=request.harness_id,
                    when=when,
                    reason=f"harness_revocation_reconfirmed:{request.request_id}",
                )
                self.task_grants._cascade_revoke_for_harness_in_transaction(
                    connection,
                    harness_id=request.harness_id,
                    when=when,
                    reason=f"harness_revocation_reconfirmed:{request.request_id}",
                )
                revoked_credentials = connection.execute(
                    "SELECT COUNT(*) AS count FROM credentials WHERE harness_id=? AND status='revoked'",
                    (request.harness_id,),
                ).fetchone()["count"]
                self.store.append_audit(
                    connection,
                    {
                        "action": "harness.revocation_reconfirmed",
                        "domain_id": request.domain_id,
                        "harness_id": request.harness_id,
                        "request_id": request.request_id,
                        "policy_decision_id": authority.policy_decision_id,
                        "approval_receipt_id": verified_approval.receipt_id,
                        "revoked_at": revoked_at,
                    },
                )
                result = HarnessRevocationResult(
                    request.domain_id,
                    request.harness_id,
                    harness["credential_epoch"],
                    domain["revocation_epoch"],
                    revoked_credentials,
                    True,
                )
                if commit_callback is not None:
                    commit_callback(connection, result)
                return result

            next_credential_epoch = harness["credential_epoch"] + 1
            connection.execute(
                "UPDATE harnesses SET status='revoked',credential_epoch=? WHERE harness_id=?",
                (next_credential_epoch, request.harness_id),
            )
            credentials = connection.execute(
                "UPDATE credentials SET status='revoked' WHERE harness_id=? AND status!='revoked'",
                (request.harness_id,),
            )
            self.relationships._cascade_revoke_for_harness_in_transaction(
                connection,
                harness_id=request.harness_id,
                when=when,
                reason=f"harness_revoked:{request.request_id}",
            )
            self.task_grants._cascade_revoke_for_harness_in_transaction(
                connection,
                harness_id=request.harness_id,
                when=when,
                reason=f"harness_revoked:{request.request_id}",
            )
            connection.execute(
                "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
                (request.domain_id,),
            )
            next_domain_epoch = domain["revocation_epoch"] + 1
            self.store.append_audit(
                connection,
                {
                    "action": "harness.revoked",
                    "domain_id": request.domain_id,
                    "domain_revocation_epoch": next_domain_epoch,
                    "harness_id": request.harness_id,
                    "new_credential_epoch": next_credential_epoch,
                    "reason": request.reason,
                    "request_id": request.request_id,
                    "policy_decision_id": authority.policy_decision_id,
                    "approval_receipt_id": verified_approval.receipt_id,
                    "approver_principal_id": verified_approval.approver_principal_id,
                    "revoked_at": revoked_at,
                },
            )
            result = HarnessRevocationResult(
                request.domain_id,
                request.harness_id,
                next_credential_epoch,
                next_domain_epoch,
                credentials.rowcount,
                False,
            )
            if commit_callback is not None:
                commit_callback(connection, result)
        return result
