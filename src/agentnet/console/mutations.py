"""Fail-closed console mutation preparation and fresh Approval requests."""

from __future__ import annotations

import hashlib
import json
import secrets
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, Protocol

from agentnet.errors import AuthenticationError, AuthorizationError, IdempotencyConflict, ValidationError
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.identity.revocation import (
    HarnessRevocationRequest,
    HarnessRevocationService,
)
from agentnet.identity.actors import VerifiedActor
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


class ApprovalRequestClient(Protocol):
    def create_request(self, **request: Any) -> dict[str, Any]: ...
    def request_status(self, **request: Any) -> dict[str, Any]: ...
    def retrieve_receipt(self, **request: Any) -> dict[str, Any]: ...


@dataclass(frozen=True, slots=True)
class PendingConsoleAction:
    mutation_id: str
    state: str
    approval_request_id: str | None
    expires_at: int


@dataclass(frozen=True, slots=True)
class EnrollmentIntentResult:
    intent_id: str
    state: str
    expires_at: int


class ConsoleMutationService:
    def __init__(
        self,
        *,
        store: StoreBackend,
        approval_client: ApprovalRequestClient,
        require: Callable[..., Any],
        harness_revocations: HarnessRevocationService | None = None,
        approval_public_origin: str = "/approvals",
        clock: Callable[[], int] | None = None,
    ) -> None:
        self.store = store
        self.approval_client = approval_client
        self.require = require
        self.harness_revocations = harness_revocations
        self.approval_public_origin = approval_public_origin
        self.clock = clock or (lambda: int(time.time()))

    def request_harness_revocation(
        self,
        *,
        actor: VerifiedActor,
        target_harness_id: str,
        reason: str,
        idempotency_key: str,
    ) -> PendingConsoleAction:
        if actor.principal_id is None or actor.harness_id is None:
            raise AuthenticationError("console mutation denied")
        reason = reason.strip()
        if not reason or len(reason) > 512:
            raise ValidationError("A reason is required")
        if not 16 <= len(idempotency_key) <= 128:
            raise ValidationError("The action identifier is invalid")
        if target_harness_id == actor.harness_id:
            raise AuthorizationError("The current administration session cannot remove itself")
        now = self.clock()
        target = self.store.fetch_one(
            """SELECT h.*,p.verified_email FROM harnesses h
               LEFT JOIN principals p ON p.principal_id=h.principal_id
               WHERE h.domain_id=? AND h.harness_id=?""",
            (actor.domain_id, target_harness_id),
        )
        if target is None or target["kind"] == "server-agent":
            raise AuthenticationError("console mutation denied")
        if target["status"] == "revoked":
            raise ValidationError("Access is already removed")
        mutation_id = idempotency_key
        expires_at = now + 600
        consequence = (
            f"Remove access for {target['display_name']} only. Other laptops and the person remain active."
        )
        if self.harness_revocations is not None:
            prepared = self.harness_revocations.prepare(
                domain_id=actor.domain_id,
                harness_id=target_harness_id,
                reason=reason,
            )
            revocation_request = prepared.model_copy(update={"request_id": mutation_id})
        else:
            domain = self.store.fetch_one(
                "SELECT revocation_epoch FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            )
            if domain is None:
                raise AuthenticationError("console mutation denied")
            revocation_request = HarnessRevocationRequest(
                request_id=mutation_id,
                domain_id=actor.domain_id,
                harness_id=target_harness_id,
                expected_credential_epoch=int(target["credential_epoch"]),
                expected_domain_revocation_epoch=int(domain["revocation_epoch"]),
                reason=reason,
            )
        transaction = revocation_request.canonical_transaction()
        digest = revocation_request.transaction_digest
        resource, request_context = HarnessRevocationService.authority_binding(revocation_request)
        decision = self.require(
            actor=actor,
            action="identity.harness.revoke",
            resource=resource,
            context=request_context,
        )
        policy_decision_id = getattr(decision, "decision_id", None)
        request_view = {
            "person": str(target["verified_email"] or "Enrolled person"),
            "harness_name": str(target["display_name"]),
            "consequence": consequence,
            "reason": reason,
            "revocation_request": revocation_request.model_dump(mode="json"),
        }
        possession_secret = secrets.token_urlsafe(32)
        encrypted = self.store.encrypted_payload(
            {"possession_secret": possession_secret}, mutation_id
        )
        with self.store.transaction() as connection:
            duplicate = connection.execute(
                "SELECT * FROM console_mutations WHERE idempotency_key=?", (idempotency_key,)
            ).fetchone()
            if duplicate is not None:
                if not secrets.compare_digest(str(duplicate["request_digest"]), digest):
                    raise IdempotencyConflict("action identifier was reused for different details")
                if duplicate["state"] != "prepared":
                    return PendingConsoleAction(
                        str(duplicate["mutation_id"]),
                        str(duplicate["state"]),
                        str(duplicate["approval_request_id"])
                        if duplicate["approval_request_id"]
                        else None,
                        int(duplicate["expires_at"]),
                    )
                payload = self.store.decrypted_payload(
                    str(duplicate["possession_secret_encrypted"]), mutation_id
                )
                possession_secret = payload.get("possession_secret")
                if not isinstance(possession_secret, str):
                    raise AuthenticationError("console mutation denied")
                expires_at = int(duplicate["expires_at"])
            else:
                connection.execute(
                    """INSERT INTO console_mutations(
                        mutation_id,domain_id,actor_principal_id,actor_harness_id,mutation_kind,
                        resource,request_json,request_digest,idempotency_key,state,revision,
                        policy_decision_id,approval_transaction_digest,possession_secret_encrypted,
                        created_at,updated_at,expires_at
                    ) VALUES(?,?,?,?,?,?,?,?,?,'prepared',1,?,?,?,?,?,?)""",
                    (
                        mutation_id,
                        actor.domain_id,
                        actor.principal_id,
                        actor.harness_id,
                        "harness_revoke",
                        resource,
                        canonical_json(request_view).decode("utf-8"),
                        digest,
                        idempotency_key,
                        policy_decision_id,
                        digest,
                        encrypted,
                        now,
                        now,
                        expires_at,
                    ),
                )
        approval = self.approval_client.create_request(
            idempotency_key=f"console:{idempotency_key}",
            domain_id=actor.domain_id,
            approval_purpose=HarnessRevocationService.APPROVAL_PURPOSE,
            canonical_transaction=canonical_json(transaction),
            transaction_digest=digest,
            possession_hash=hashlib.sha256(possession_secret.encode("ascii")).hexdigest(),
            request_expires_at=expires_at,
        )
        request_id = approval.get("request_id")
        if approval.get("state") not in {"pending", "issued"} or not isinstance(request_id, str):
            raise AuthorizationError("independent approval request was not accepted")
        with self.store.transaction() as connection:
            updated = connection.execute(
                """UPDATE console_mutations SET state='waiting_approval',revision=revision+1,
                   approval_request_id=?,updated_at=?
                   WHERE mutation_id=? AND state='prepared'""",
                (request_id, now, mutation_id),
            )
            if updated.rowcount != 1:
                current = connection.execute(
                    "SELECT state,approval_request_id,expires_at FROM console_mutations WHERE mutation_id=?",
                    (mutation_id,),
                ).fetchone()
                if current is None or current["approval_request_id"] != request_id:
                    raise IdempotencyConflict(
                        "administrator action changed while approval was requested"
                    )
                return PendingConsoleAction(
                    mutation_id,
                    str(current["state"]),
                    request_id,
                    int(current["expires_at"]),
                )
            self.store.append_audit(
                connection,
                {
                    "action": "console.mutation.requested",
                    "domain_id": actor.domain_id,
                    "actor_principal_id": actor.principal_id,
                    "actor_harness_id": actor.harness_id,
                    "mutation_id": mutation_id,
                    "mutation_kind": "harness_revoke",
                    "resource": resource,
                    "request_digest": digest,
                    "approval_request_id": request_id,
                    "outcome": "waiting_approval",
                    "occurred_at": now,
                },
            )
        return PendingConsoleAction(mutation_id, "waiting_approval", request_id, expires_at)
    def reconcile_harness_revocation(
        self,
        *,
        actor: VerifiedActor,
        mutation_id: str,
    ) -> PendingConsoleAction:
        if self.harness_revocations is None:
            raise AuthorizationError("harness revocation service is unavailable")
        now = self.clock()
        row = self.store.fetch_one(
            """SELECT * FROM console_mutations
               WHERE mutation_id=? AND domain_id=? AND mutation_kind='harness_revoke'""",
            (mutation_id, actor.domain_id),
        )
        if (
            row is None
            or row["actor_principal_id"] != actor.principal_id
            or row["actor_harness_id"] != actor.harness_id
        ):
            raise AuthenticationError("console mutation denied")
        if row["state"] == "completed":
            return PendingConsoleAction(
                mutation_id,
                "completed",
                str(row["approval_request_id"]),
                int(row["expires_at"]),
            )
        if row["state"] != "waiting_approval":
            raise ValidationError("The approved action is not ready to apply")
        request_id = str(row["approval_request_id"])
        transaction_digest = str(row["approval_transaction_digest"])
        status = self.approval_client.request_status(
            request_id=request_id,
            transaction_digest=transaction_digest,
        )
        if status["state"] == "pending":
            return PendingConsoleAction(
                mutation_id,
                "waiting_approval",
                request_id,
                int(row["expires_at"]),
            )
        if status["state"] in {"rejected", "expired"}:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE console_mutations SET state='failed',revision=revision+1,updated_at=?
                       WHERE mutation_id=? AND state='waiting_approval'""",
                    (now, mutation_id),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "console.mutation.not_approved",
                        "domain_id": actor.domain_id,
                        "mutation_id": mutation_id,
                        "approval_request_id": request_id,
                        "outcome": str(status["state"]),
                        "occurred_at": now,
                    },
                )
            return PendingConsoleAction(
                mutation_id,
                "failed",
                request_id,
                int(row["expires_at"]),
            )
        if status["state"] != "issued":
            raise AuthenticationError("approval service response denied")
        payload = self.store.decrypted_payload(
            str(row["possession_secret_encrypted"]), mutation_id
        )
        possession_secret = payload.get("possession_secret")
        if not isinstance(possession_secret, str):
            raise AuthenticationError("console mutation denied")
        receipt = self.approval_client.retrieve_receipt(
            request_id=request_id,
            possession_secret=possession_secret,
            domain_id=actor.domain_id,
            approval_purpose=HarnessRevocationService.APPROVAL_PURPOSE,
            transaction_digest=transaction_digest,
            idempotency_key=f"console-reconcile:{mutation_id}",
        )
        request_view = json.loads(str(row["request_json"]))
        if not isinstance(request_view, dict):
            raise AuthenticationError("console mutation denied")
        revocation_request = HarnessRevocationRequest.model_validate(
            request_view.get("revocation_request")
        )
        if not secrets.compare_digest(
            revocation_request.transaction_digest, transaction_digest
        ):
            raise AuthenticationError("console mutation denied")
        policy_decision_id = row["policy_decision_id"]
        if not isinstance(policy_decision_id, str) or not policy_decision_id:
            raise AuthorizationError("recorded authorization decision is unavailable")
        authority = IssuanceAuthority(
            actor=actor,
            policy_decision_id=policy_decision_id,
        )
        receipt_digest = hashlib.sha256(canonical_json(receipt)).hexdigest()

        def complete_in_transaction(connection, result) -> None:
            result_json = canonical_json(
                {
                    "domain_id": result.domain_id,
                    "harness_id": result.harness_id,
                    "credential_epoch": result.credential_epoch,
                    "domain_revocation_epoch": result.domain_revocation_epoch,
                    "revoked_credentials": result.revoked_credentials,
                    "already_revoked": result.already_revoked,
                }
            ).decode("utf-8")
            updated = connection.execute(
                """UPDATE console_mutations SET state='completed',revision=revision+1,
                   result_json=?,approval_receipt_digest=?,updated_at=?
                   WHERE mutation_id=? AND state='waiting_approval'""",
                (result_json, receipt_digest, now, mutation_id),
            )
            if updated.rowcount != 1:
                raise IdempotencyConflict("administrator action changed before commit")
            self.store.append_audit(
                connection,
                {
                    "action": "console.mutation.completed",
                    "domain_id": actor.domain_id,
                    "actor_principal_id": actor.principal_id,
                    "actor_harness_id": actor.harness_id,
                    "mutation_id": mutation_id,
                    "mutation_kind": "harness_revoke",
                    "resource": str(row["resource"]),
                    "approval_request_id": request_id,
                    "approval_receipt_digest": receipt_digest,
                    "outcome": "completed",
                    "occurred_at": now,
                },
            )

        self.harness_revocations.revoke(
            request=revocation_request,
            authority=authority,
            approval=receipt,
            now=now,
            commit_callback=complete_in_transaction,
        )
        return PendingConsoleAction(
            mutation_id,
            "completed",
            request_id,
            int(row["expires_at"]),
        )


    def create_enrollment_intent(
        self,
        *,
        actor: VerifiedActor,
        target_kind: str,
        target_principal_id: str | None,
        invited_email_alias: str | None,
        harness_name: str,
        capabilities: tuple[str, ...],
        reason: str,
        idempotency_key: str,
    ) -> EnrollmentIntentResult:
        if actor.principal_id is None or actor.harness_id is None:
            raise AuthenticationError("console enrollment denied")
        if target_kind not in {"existing_person", "new_person"}:
            raise ValidationError("Choose who will use this laptop")
        if (target_principal_id is None) == (invited_email_alias is None):
            raise ValidationError("Choose exactly one existing or new person")
        if not harness_name.strip() or len(harness_name.strip()) > 128:
            raise ValidationError("Enter a laptop name")
        if not reason.strip() or len(reason.strip()) > 512:
            raise ValidationError("A reason is required")
        if not 16 <= len(idempotency_key) <= 128:
            raise ValidationError("The enrollment identifier is invalid")
        resource = (
            f"principal:{target_principal_id}"
            if target_principal_id is not None
            else f"domain:{actor.domain_id}:new-person"
        )
        self.require(actor=actor, action="identity.enrollment.propose", resource=resource)
        person_label = "Invited person"
        if target_principal_id is not None:
            target = self.store.fetch_one(
                "SELECT principal_id,verified_email,status FROM principals WHERE domain_id=? AND principal_id=?",
                (actor.domain_id, target_principal_id),
            )
            if target is None or target["status"] != "active":
                raise AuthenticationError("console enrollment denied")
            person_label = str(target["verified_email"])
        else:
            alias = (invited_email_alias or "").strip().casefold()
            if alias != invited_email_alias or alias.count("@") != 1 or len(alias) > 320:
                raise ValidationError("Enter a normalized verified email address")
        now = self.clock()
        expires_at = now + 86_400
        intent_id = idempotency_key
        request = {
            "schema": "agentnet.console.enrollment-intent.v1",
            "intent_id": intent_id,
            "target_kind": target_kind,
            "person": person_label,
            "harness_name": harness_name.strip(),
            "capabilities": sorted(set(capabilities)),
            "reason": reason.strip(),
            "consequence": "No access is created until the target proves possession and fresh passkey approval completes.",
            "expires_at": expires_at,
        }
        digest = canonical_digest(request)
        with self.store.transaction() as connection:
            duplicate = connection.execute(
                "SELECT intent_id,state,expires_at,request_digest FROM console_enrollment_intents WHERE intent_id=?",
                (idempotency_key,),
            ).fetchone()
            if duplicate is not None:
                if not secrets.compare_digest(str(duplicate["request_digest"]), digest):
                    raise IdempotencyConflict("enrollment identifier was reused for different details")
                return EnrollmentIntentResult(
                    str(duplicate["intent_id"]), str(duplicate["state"]), int(duplicate["expires_at"])
                )
            connection.execute(
                """INSERT INTO console_enrollment_intents(
                    intent_id,domain_id,sponsor_principal_id,sponsor_harness_id,target_kind,
                    target_principal_id,invited_email_alias,request_json,request_digest,state,
                    revision,created_at,updated_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,'waiting_target',1,?,?,?)""",
                (
                    intent_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    target_kind,
                    target_principal_id,
                    invited_email_alias,
                    canonical_json(request).decode("utf-8"),
                    digest,
                    now,
                    now,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "console.enrollment.requested",
                    "domain_id": actor.domain_id,
                    "actor_principal_id": actor.principal_id,
                    "actor_harness_id": actor.harness_id,
                    "intent_id": intent_id,
                    "target_kind": target_kind,
                    "request_digest": digest,
                    "outcome": "waiting_target",
                    "occurred_at": now,
                },
            )
        return EnrollmentIntentResult(intent_id, "waiting_target", expires_at)


__all__ = [
    "ConsoleMutationService",
    "EnrollmentIntentResult",
    "PendingConsoleAction",
]
