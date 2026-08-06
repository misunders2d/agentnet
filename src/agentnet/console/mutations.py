"""Fail-closed console mutation preparation and fresh Approval requests."""

from __future__ import annotations

import hashlib
import json
import secrets
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, Protocol

from agentnet.authorization.communication_scope_service import CollaborationScopeProposal
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.console.models import (
    INVITATION_PERMISSION_ACTIONS,
    InvitationCreationForm,
    InvitationDetail,
    InvitationScopeChoice,
)
from agentnet.errors import AuthenticationError, AuthorizationError, IdempotencyConflict, ValidationError
from agentnet.identity.invitation_links import (
    INVITATION_LINK_ISSUE_ACTION,
    INVITATION_LINK_REVOKE_ACTION,
    INVITATION_LINK_TTL_SECONDS,
    InvitationLinkService,
    InvitationOffer,
)
from agentnet.identity.revocation import (
    HarnessRevocationRequest,
    HarnessRevocationService,
)
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend

ALLOWED_ENROLLMENT_CAPABILITIES = frozenset(
    {"message_delivery", "offline_delivery"}
)
_ALLOWED_ENROLLMENT_HARNESS_KINDS = frozenset({"laptop"})


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

@dataclass(frozen=True, slots=True)
class EnrollmentReview:
    review_token: str
    person: str
    harness_kind: str
    harness_name: str
    capabilities: tuple[str, ...]
    reason: str
    consequence: str
    expires_at: int

@dataclass(frozen=True, slots=True)
class _CachedInvitation:
    actor_principal_id: str
    actor_harness_id: str
    scope_id: str
    offer: InvitationOffer
    detail: InvitationDetail




class ConsoleMutationService:
    def __init__(
        self,
        *,
        store: StoreBackend,
        approval_client: ApprovalRequestClient,
        invitation_links: InvitationLinkService,
        require: Callable[..., Any],
        harness_revocations: HarnessRevocationService | None = None,
        approval_public_origin: str = "/approvals",
        clock: Callable[[], int] | None = None,
        enrollment_review_ttl_seconds: int = 600,
    ) -> None:
        self.store = store
        self.approval_client = approval_client
        self.require = require
        self.invitation_links = invitation_links
        self.harness_revocations = harness_revocations
        self.approval_public_origin = approval_public_origin
        self.enrollment_review_ttl_seconds = enrollment_review_ttl_seconds
        self.clock = clock or (lambda: int(time.time()))
        self._invitation_cache: dict[str, _CachedInvitation] = {}
        self._invitation_cache_lock = threading.RLock()

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
            f"Remove access for {target['display_name']} only. Other harnesses and the person remain active."
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
        now = self.clock()
        if expires_at <= now:
            raise AuthorizationError("independent approval request expired")
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
        now = self.clock()
        status_state = str(status.get("state", "unknown"))
        if int(row["expires_at"]) <= now:
            status_state = "expired"
        if status_state == "pending":
            return PendingConsoleAction(
                mutation_id,
                "waiting_approval",
                request_id,
                int(row["expires_at"]),
            )
        if status_state in {"rejected", "expired"}:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE console_mutations SET state=?,revision=revision+1,
                       updated_at=?,terminal_at=?
                       WHERE mutation_id=? AND state='waiting_approval'""",
                    (status_state, now, now, mutation_id),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "console.mutation.not_approved",
                        "domain_id": actor.domain_id,
                        "mutation_id": mutation_id,
                        "approval_request_id": request_id,
                        "outcome": status_state,
                        "occurred_at": now,
                    },
                )
            return PendingConsoleAction(
                mutation_id,
                status_state,
                request_id,
                int(row["expires_at"]),
            )
        if status_state != "issued":
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
        now = self.clock()
        if int(row["expires_at"]) <= now:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE console_mutations SET state='expired',revision=revision+1,
                       updated_at=?,terminal_at=?
                       WHERE mutation_id=? AND state='waiting_approval'""",
                    (now, now, mutation_id),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "console.mutation.not_approved",
                        "domain_id": actor.domain_id,
                        "mutation_id": mutation_id,
                        "approval_request_id": request_id,
                        "outcome": "expired",
                        "occurred_at": now,
                    },
                )
            return PendingConsoleAction(
                mutation_id,
                "expired",
                request_id,
                int(row["expires_at"]),
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


    def prepare_enrollment_review(
        self,
        *,
        actor: VerifiedActor,
        target_kind: str,
        target_principal_id: str | None,
        invited_email_alias: str | None,
        harness_kind: str,
        harness_name: str,
        capabilities: tuple[str, ...],
        reason: str,
        idempotency_key: str,
    ) -> EnrollmentReview:
        if actor.principal_id is None or actor.harness_id is None:
            raise AuthenticationError("console enrollment denied")
        if target_kind not in {"existing_person", "new_person"}:
            raise ValidationError("Choose who will use this laptop")
        if target_kind == "existing_person":
            invited_email_alias = None
        elif target_kind == "new_person":
            target_principal_id = None
        if (target_principal_id is None) == (invited_email_alias is None):
            raise ValidationError("Choose exactly one existing or new person")
        if harness_kind not in _ALLOWED_ENROLLMENT_HARNESS_KINDS:
            raise AuthorizationError("Requested harness kind is not allowed")
        normalized_harness_name = harness_name.strip()
        if not normalized_harness_name or len(normalized_harness_name) > 128:
            raise ValidationError("Enter a laptop name")
        normalized_reason = reason.strip()
        if not normalized_reason or len(normalized_reason) > 512:
            raise ValidationError("A reason is required")
        if not 16 <= len(idempotency_key) <= 128:
            raise ValidationError("The enrollment identifier is invalid")
        if (
            not isinstance(capabilities, tuple)
            or len(capabilities) > len(ALLOWED_ENROLLMENT_CAPABILITIES)
            or any(
                not isinstance(capability, str)
                or capability not in ALLOWED_ENROLLMENT_CAPABILITIES
                for capability in capabilities
            )
        ):
            raise AuthorizationError("Requested service is not allowed")
        canonical_capabilities = tuple(sorted(capabilities))
        if len(set(canonical_capabilities)) != len(canonical_capabilities):
            raise ValidationError("Choose each requested service only once")

        target_identity: dict[str, str]
        normalized_alias: str | None = None
        if target_principal_id is not None:
            target = self.store.fetch_one(
                """SELECT principal_id,oidc_issuer,oidc_subject,verified_email,status
                   FROM principals WHERE domain_id=? AND principal_id=?""",
                (actor.domain_id, target_principal_id),
            )
            if (
                target is None
                or target["status"] != "active"
                or not target["oidc_issuer"]
                or not target["oidc_subject"]
                or not target["verified_email"]
            ):
                raise AuthenticationError("console enrollment denied")
            person_label = str(target["verified_email"]).strip().casefold()
            target_identity = {
                "principal_id": str(target["principal_id"]),
                "oidc_issuer": str(target["oidc_issuer"]),
                "oidc_subject": str(target["oidc_subject"]),
                "verified_email": person_label,
            }
            resource = f"principal:{target_principal_id}"
        else:
            normalized_alias = (invited_email_alias or "").strip().casefold()
            local, separator, domain = normalized_alias.partition("@")
            if (
                separator != "@"
                or not local
                or not domain
                or "@" in domain
                or len(normalized_alias) > 320
                or any(ord(character) < 0x20 or ord(character) == 0x7F for character in normalized_alias)
            ):
                raise ValidationError("Enter a valid verified email address")
            person_label = normalized_alias
            target_identity = {"verified_email": normalized_alias}
            resource = f"domain:{actor.domain_id}:new-person"

        now = self.clock()
        expires_at = now + 86_400
        consequence = (
            "No access is created until the target proves possession and fresh "
            "passkey approval completes."
        )
        request = {
            "schema": "agentnet.console.enrollment-intent.v1",
            "intent_id": idempotency_key,
            "target_kind": target_kind,
            "target_identity": target_identity,
            "target_principal_id": target_principal_id,
            "invited_email_alias": normalized_alias,
            "person": person_label,
            "harness_kind": harness_kind,
            "harness_name": normalized_harness_name,
            "capabilities": list(canonical_capabilities),
            "reason": normalized_reason,
            "consequence": consequence,
            "expires_at": expires_at,
        }
        context = {
            "target_kind": target_kind,
            "target_identity": target_identity,
            "harness_kind": harness_kind,
            "harness_name": normalized_harness_name,
            "capabilities": canonical_capabilities,
            "expires_at": expires_at,
        }
        self.require(
            actor=actor,
            action="identity.enrollment.propose",
            resource=resource,
            context=context,
        )
        request_json = canonical_json(request).decode("utf-8")
        request_digest = canonical_digest(request)
        review_token = secrets.token_urlsafe(32)
        review_token_hash = hashlib.sha256(review_token.encode("ascii")).hexdigest()
        review_expires_at = now + self.enrollment_review_ttl_seconds
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO console_enrollment_reviews(
                    review_token_hash,domain_id,sponsor_principal_id,sponsor_harness_id,
                    request_json,request_digest,state,created_at,expires_at
                ) VALUES(?,?,?,?,?,?,'pending',?,?)""",
                (
                    review_token_hash,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    request_json,
                    request_digest,
                    now,
                    review_expires_at,
                ),
            )
        return EnrollmentReview(
            review_token=review_token,
            person=person_label,
            harness_kind=harness_kind,
            harness_name=normalized_harness_name,
            capabilities=canonical_capabilities,
            reason=normalized_reason,
            consequence=consequence,
            expires_at=expires_at,
        )

    def create_enrollment_intent(
        self,
        *,
        actor: VerifiedActor,
        review_token: str,
    ) -> EnrollmentIntentResult:
        if actor.principal_id is None or actor.harness_id is None:
            raise AuthenticationError("console enrollment denied")
        if (
            not isinstance(review_token, str)
            or not 32 <= len(review_token) <= 128
            or not review_token.isascii()
        ):
            raise AuthenticationError("console enrollment review denied")
        review_hash = hashlib.sha256(review_token.encode("ascii")).hexdigest()
        now = self.clock()
        preview = self.store.fetch_one(
            """SELECT * FROM console_enrollment_reviews
               WHERE review_token_hash=? AND domain_id=?""",
            (review_hash, actor.domain_id),
        )
        if (
            preview is None
            or preview["sponsor_principal_id"] != actor.principal_id
            or preview["sponsor_harness_id"] != actor.harness_id
            or preview["state"] != "pending"
            or int(preview["expires_at"]) <= now
        ):
            raise AuthenticationError("console enrollment review denied")
        try:
            preview_request = json.loads(str(preview["request_json"]))
        except (json.JSONDecodeError, UnicodeError) as exc:
            raise AuthenticationError("console enrollment review denied") from exc
        if (
            not isinstance(preview_request, dict)
            or not secrets.compare_digest(
                str(preview["request_digest"]), canonical_digest(preview_request)
            )
        ):
            raise AuthenticationError("console enrollment review denied")
        target_kind = preview_request.get("target_kind")
        target_identity = preview_request.get("target_identity")
        harness_kind = preview_request.get("harness_kind")
        harness_name = preview_request.get("harness_name")
        capabilities = preview_request.get("capabilities")
        expires_at = preview_request.get("expires_at")
        target_principal_id = preview_request.get("target_principal_id")
        if (
            target_kind not in {"existing_person", "new_person"}
            or not isinstance(target_identity, dict)
            or harness_kind not in _ALLOWED_ENROLLMENT_HARNESS_KINDS
            or not isinstance(harness_name, str)
            or not isinstance(capabilities, list)
            or tuple(sorted(capabilities)) != tuple(capabilities)
            or any(
                not isinstance(capability, str)
                or capability not in ALLOWED_ENROLLMENT_CAPABILITIES
                for capability in capabilities
            )
            or not isinstance(expires_at, int)
            or expires_at <= now
        ):
            raise AuthenticationError("console enrollment review denied")
        resource = (
            f"principal:{target_principal_id}"
            if target_kind == "existing_person"
            and isinstance(target_principal_id, str)
            else f"domain:{actor.domain_id}:new-person"
        )
        self.require(
            actor=actor,
            action="identity.enrollment.propose",
            resource=resource,
            context={
                "target_kind": target_kind,
                "target_identity": target_identity,
                "harness_kind": harness_kind,
                "harness_name": harness_name,
                "capabilities": tuple(capabilities),
                "expires_at": expires_at,
            },
        )
        with self.store.transaction() as connection:
            review = connection.execute(
                """SELECT * FROM console_enrollment_reviews
                   WHERE review_token_hash=? AND domain_id=?""",
                (review_hash, actor.domain_id),
            ).fetchone()
            if (
                review is None
                or review["sponsor_principal_id"] != actor.principal_id
                or review["sponsor_harness_id"] != actor.harness_id
                or review["state"] != "pending"
                or int(review["expires_at"]) <= now
            ):
                raise AuthenticationError("console enrollment review denied")
            try:
                request = json.loads(str(review["request_json"]))
            except (json.JSONDecodeError, UnicodeError) as exc:
                raise AuthenticationError("console enrollment review denied") from exc
            if (
                not isinstance(request, dict)
                or not secrets.compare_digest(
                    str(review["request_digest"]), canonical_digest(request)
                )
            ):
                raise AuthenticationError("console enrollment review denied")
            intent_id = request.get("intent_id")
            target_kind = request.get("target_kind")
            target_principal_id = request.get("target_principal_id")
            invited_email_alias = request.get("invited_email_alias")
            expires_at = request.get("expires_at")
            if (
                not isinstance(intent_id, str)
                or not isinstance(target_kind, str)
                or target_kind not in {"existing_person", "new_person"}
                or not isinstance(expires_at, int)
                or expires_at <= now
                or (target_principal_id is None) == (invited_email_alias is None)
                or (
                    target_principal_id is not None
                    and not isinstance(target_principal_id, str)
                )
                or (
                    invited_email_alias is not None
                    and not isinstance(invited_email_alias, str)
                )
            ):
                raise AuthenticationError("console enrollment review denied")
            duplicate = connection.execute(
                """SELECT intent_id,state,expires_at,request_digest
                   FROM console_enrollment_intents WHERE intent_id=?""",
                (intent_id,),
            ).fetchone()
            if duplicate is not None:
                if not secrets.compare_digest(
                    str(duplicate["request_digest"]), str(review["request_digest"])
                ):
                    raise IdempotencyConflict(
                        "enrollment identifier was reused for different details"
                    )
                result = EnrollmentIntentResult(
                    str(duplicate["intent_id"]),
                    str(duplicate["state"]),
                    int(duplicate["expires_at"]),
                )
            else:
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
                        str(review["request_json"]),
                        str(review["request_digest"]),
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
                        "request_digest": str(review["request_digest"]),
                        "outcome": "waiting_target",
                        "occurred_at": now,
                    },
                )
                result = EnrollmentIntentResult(
                    intent_id, "waiting_target", expires_at
                )
            consumed = connection.execute(
                """UPDATE console_enrollment_reviews
                   SET state='consumed',consumed_at=?
                   WHERE review_token_hash=? AND state='pending' AND expires_at>?""",
                (now, review_hash, now),
            )
            if consumed.rowcount != 1:
                raise IdempotencyConflict("enrollment review changed before commit")
        return result

    @staticmethod
    def _require_invitation_actor(actor: VerifiedActor) -> tuple[str, str]:
        if actor.principal_id is None or actor.harness_id is None or actor.credential_id is None:
            raise AuthenticationError("console invitation denied")
        return actor.principal_id, actor.harness_id

    @staticmethod
    def _json_string_tuple(row: Any, field: str) -> tuple[str, ...]:
        try:
            value = json.loads(str(row[field]))
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            raise AuthorizationError("console invitation denied") from exc
        if (
            not isinstance(value, list)
            or any(not isinstance(item, str) or not item for item in value)
            or tuple(sorted(set(value))) != tuple(value)
        ):
            raise AuthorizationError("console invitation denied")
        return tuple(value)

    @staticmethod
    def _scope_display_name(
        _canonical_references: tuple[str, ...],
        scope_kind: str,
    ) -> str:
        return {
            "personal": "Personal space",
            "direct": "Direct space",
            "shared": "Shared space",
        }.get(scope_kind, "Collaboration space")

    def _current_invitation_scope(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str,
    ) -> tuple[Any, tuple[str, ...]]:
        principal_id, harness_id = self._require_invitation_actor(actor)
        now = self.clock()
        with self.store.transaction(immediate=False) as connection:
            row = connection.execute(
                """SELECT s.*,d.policy_revision AS current_policy_revision,
                          d.revocation_epoch AS current_domain_revocation_epoch
                     FROM collaboration_scopes s
                     JOIN collaboration_scope_members m
                       ON m.scope_id=s.scope_id
                     JOIN domains d ON d.domain_id=s.domain_id
                    WHERE s.scope_id=? AND s.domain_id=?
                      AND s.state='active'
                      AND (s.expires_at IS NULL OR s.expires_at>?)
                      AND s.policy_revision=d.policy_revision
                      AND s.domain_revocation_epoch=d.revocation_epoch
                      AND m.authority_kind='principal' AND m.authority_id=?
                      AND m.harness_id=? AND m.role IN ('owner','administrator')
                      AND m.state='active'""",
                (scope_id, actor.domain_id, now, principal_id, harness_id),
            ).fetchone()
            if row is None:
                raise AuthorizationError("console invitation denied")
            members = connection.execute(
                """SELECT harness_id FROM collaboration_scope_members
                    WHERE scope_id=? AND state='active'
                    ORDER BY harness_id""",
                (scope_id,),
            ).fetchall()
        member_harness_ids = tuple(str(member["harness_id"]) for member in members)
        if not member_harness_ids or len(member_harness_ids) != len(set(member_harness_ids)):
            raise AuthorizationError("console invitation denied")
        return row, member_harness_ids

    def invitation_scopes(self, *, actor: VerifiedActor) -> tuple[InvitationScopeChoice, ...]:
        principal_id, harness_id = self._require_invitation_actor(actor)
        now = self.clock()
        rows = self.store.fetch_all(
            """SELECT s.scope_id,s.scope_kind,s.canonical_references_json,
                      s.allowed_actions_json
                 FROM collaboration_scopes s
                 JOIN collaboration_scope_members m ON m.scope_id=s.scope_id
                 JOIN domains d ON d.domain_id=s.domain_id
                WHERE s.domain_id=? AND s.state='active'
                  AND (s.expires_at IS NULL OR s.expires_at>?)
                  AND s.policy_revision=d.policy_revision
                  AND s.domain_revocation_epoch=d.revocation_epoch
                  AND m.authority_kind='principal' AND m.authority_id=?
                  AND m.harness_id=? AND m.role IN ('owner','administrator')
                  AND m.state='active'
                ORDER BY s.updated_at DESC,s.scope_id""",
            (actor.domain_id, now, principal_id, harness_id),
        )
        result: list[InvitationScopeChoice] = []
        used_names: dict[str, int] = {}
        for row in rows:
            references = self._json_string_tuple(row, "canonical_references_json")
            allowed_actions = self._json_string_tuple(row, "allowed_actions_json")
            if (
                not allowed_actions
                or not set(allowed_actions).issubset(INVITATION_PERMISSION_ACTIONS)
            ):
                continue
            base_name = self._scope_display_name(references, str(row["scope_kind"]))
            used_names[base_name] = used_names.get(base_name, 0) + 1
            suffix = used_names[base_name]
            display_name = (
                base_name if suffix == 1 else f"{base_name[:120]} ({suffix})"
            )
            result.append(
                InvitationScopeChoice(
                    scope_id=str(row["scope_id"]),
                    display_name=display_name,
                )
            )
        if not result:
            raise AuthorizationError("console invitation denied")
        return tuple(result)

    def issue_invitation(
        self,
        *,
        actor: VerifiedActor,
        form: InvitationCreationForm,
        submission_id: str,
    ) -> tuple[str, InvitationDetail]:
        principal_id, harness_id = self._require_invitation_actor(actor)
        if not submission_id or len(submission_id) > 256:
            raise AuthenticationError("console invitation denied")
        email_domain = form.email.rsplit("@", 1)[-1]
        if email_domain != actor.domain_id.casefold():
            raise ValidationError("Enter a work email in this network")
        row, member_harness_ids = self._current_invitation_scope(
            actor=actor,
            scope_id=form.scope_id,
        )
        allowed_actions = self._json_string_tuple(row, "allowed_actions_json")
        if form.permissions != allowed_actions:
            raise ValidationError(
                "Choose exactly the message and file actions available in this space"
            )
        classifications = self._json_string_tuple(row, "allowed_classifications_json")
        try:
            proposal = CollaborationScopeProposal(
                scope_id=form.scope_id,
                scope_kind=str(row["scope_kind"]),
                member_harness_ids=member_harness_ids,
                allowed_actions=allowed_actions,
                allowed_resource_prefixes=self._json_string_tuple(
                    row, "allowed_resource_prefixes_json"
                ),
                allowed_classifications=tuple(
                    Classification(value) for value in classifications
                ),
                canonical_references=self._json_string_tuple(
                    row, "canonical_references_json"
                ),
                policy_revision=int(row["policy_revision"]),
                domain_revocation_epoch=int(row["domain_revocation_epoch"]),
                expires_at=int(row["expires_at"]) if row["expires_at"] is not None else None,
            )
        except (TypeError, ValueError) as exc:
            raise AuthorizationError("console invitation denied") from exc
        issued_at = int(self.invitation_links.clock())
        invitation_id = "console-" + canonical_digest(
            {
                "schema": "agentnet.console-invitation-submission.v1",
                "session_id": submission_id,
                "actor_principal_id": principal_id,
                "actor_harness_id": harness_id,
                "email": form.email,
                "scope_id": form.scope_id,
                "permissions": list(form.permissions),
            }
        )
        offer = InvitationOffer(
            invitation_id=invitation_id,
            invited_verified_email=form.email,
            domain_id=actor.domain_id,
            collaboration_scope_template=proposal,
            permission_actions=form.permissions,
            expires_at=issued_at + INVITATION_LINK_TTL_SECONDS,
        )
        resource, context = self.invitation_links.authority_binding(
            offer,
            action=INVITATION_LINK_ISSUE_ACTION,
            expected_revision=1,
        )
        decision = self.require(
            actor=actor,
            action=INVITATION_LINK_ISSUE_ACTION,
            resource=resource,
            context=context,
        )
        decision_id = getattr(decision, "decision_id", None)
        if not isinstance(decision_id, str) or not decision_id:
            raise AuthorizationError("console invitation denied")
        issued = self.invitation_links.issue(
            actor=actor,
            offer=offer,
            authority=IssuanceAuthority(
                actor=actor,
                policy_decision_id=decision_id,
            ),
        )
        public_url = str(issued.public_url)
        if not public_url.startswith("https://"):
            raise AuthorizationError("console invitation denied")
        detail = InvitationDetail(
            work_email=form.email,
            space=self._scope_display_name(
                tuple(proposal.canonical_references),
                proposal.scope_kind,
            ),
            permissions=form.permissions,
            invitation_url=public_url,
            qr_svg=issued.qr_svg,
            expires_at=issued.expires_at,
        )
        with self._invitation_cache_lock:
            if invitation_id in self._invitation_cache:
                raise IdempotencyConflict("invitation submission was already used")
            self._invitation_cache[invitation_id] = _CachedInvitation(
                actor_principal_id=principal_id,
                actor_harness_id=harness_id,
                scope_id=form.scope_id,
                offer=offer,
                detail=detail,
            )
        return invitation_id, detail

    def invitation_detail(
        self,
        *,
        actor: VerifiedActor,
        invitation_id: str,
    ) -> InvitationDetail:
        principal_id, harness_id = self._require_invitation_actor(actor)
        with self._invitation_cache_lock:
            cached = self._invitation_cache.get(invitation_id)
        if (
            cached is None
            or cached.actor_principal_id != principal_id
            or cached.actor_harness_id != harness_id
            or cached.offer.domain_id != actor.domain_id
        ):
            raise AuthenticationError("console invitation denied")
        row = self.store.fetch_one(
            """SELECT state,sponsor_principal_id,sponsor_harness_id
                 FROM invitation_links
                WHERE invitation_id=? AND domain_id=?""",
            (invitation_id, actor.domain_id),
        )
        if (
            row is None
            or row["sponsor_principal_id"] != principal_id
            or row["sponsor_harness_id"] != harness_id
        ):
            raise AuthenticationError("console invitation denied")
        state = str(row["state"])
        scope_current = True
        try:
            self._current_invitation_scope(actor=actor, scope_id=cached.scope_id)
        except AuthorizationError:
            scope_current = False
        unavailable = (
            state not in {"issued", "reserved"}
            or not scope_current
            or cached.detail.expires_at <= self.clock()
        )
        if unavailable:
            return cached.detail.model_copy(
                update={
                    "invitation_url": "",
                    "qr_svg": "",
                    "revoked": state in {"revoked", "consumed"} or not scope_current,
                }
            )
        return cached.detail

    def revoke_invitation(
        self,
        *,
        actor: VerifiedActor,
        invitation_id: str,
    ) -> InvitationDetail:
        principal_id, harness_id = self._require_invitation_actor(actor)
        with self._invitation_cache_lock:
            cached = self._invitation_cache.get(invitation_id)
        if (
            cached is None
            or cached.actor_principal_id != principal_id
            or cached.actor_harness_id != harness_id
            or cached.offer.domain_id != actor.domain_id
        ):
            raise AuthenticationError("console invitation denied")
        row = self.store.fetch_one(
            """SELECT state,revision,sponsor_principal_id,sponsor_harness_id
                 FROM invitation_links
                WHERE invitation_id=? AND domain_id=?""",
            (invitation_id, actor.domain_id),
        )
        if (
            row is None
            or row["sponsor_principal_id"] != principal_id
            or row["sponsor_harness_id"] != harness_id
        ):
            raise AuthenticationError("console invitation denied")
        if row["state"] not in {"issued", "reserved"}:
            raise ValidationError("This invitation is no longer available")
        expected_revision = int(row["revision"])
        resource, context = self.invitation_links.authority_binding(
            cached.offer,
            action=INVITATION_LINK_REVOKE_ACTION,
            expected_revision=expected_revision,
        )
        decision = self.require(
            actor=actor,
            action=INVITATION_LINK_REVOKE_ACTION,
            resource=resource,
            context=context,
        )
        decision_id = getattr(decision, "decision_id", None)
        if not isinstance(decision_id, str) or not decision_id:
            raise AuthorizationError("console invitation denied")
        self.invitation_links.revoke(
            actor=actor,
            invitation_id=invitation_id,
            expected_revision=expected_revision,
            authority=IssuanceAuthority(
                actor=actor,
                policy_decision_id=decision_id,
            ),
        )
        detail = cached.detail.model_copy(
            update={"invitation_url": "", "qr_svg": "", "revoked": True}
        )
        with self._invitation_cache_lock:
            self._invitation_cache[invitation_id] = _CachedInvitation(
                actor_principal_id=cached.actor_principal_id,
                actor_harness_id=cached.actor_harness_id,
                scope_id=cached.scope_id,
                offer=cached.offer,
                detail=detail,
            )
        return detail


__all__ = [
    "ALLOWED_ENROLLMENT_CAPABILITIES",
    "ConsoleMutationService",
    "EnrollmentIntentResult",
    "EnrollmentReview",
    "PendingConsoleAction",
]
