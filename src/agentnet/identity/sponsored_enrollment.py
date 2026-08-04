"""Proof-bound sponsored enrollment coordinated by the administration console."""
from __future__ import annotations

import base64
import hashlib
import json
import secrets
import time
from collections.abc import Callable
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from typing import Any, Literal, Protocol
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.approval.service import IndependentApprovalVerifier, consume_independent_approval
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.invitations import InternalInvitationRequest, InternalInvitationService
from agentnet.identity.oidc import OIDCProvider
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


class ApprovalClient(Protocol):
    def create_request(self, **request: Any) -> dict[str, Any]: ...
    def request_status(self, **request: Any) -> dict[str, Any]: ...
    def retrieve_receipt(self, **request: Any) -> dict[str, Any]: ...


class SponsoredEnrollmentIntentRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)
    intent_id: str = Field(min_length=16, max_length=128)
    target_kind: Literal["existing_person", "new_person"]
    target_principal_id: str | None = None
    invited_verified_email: str | None = None
    harness_kind: str = Field(min_length=1, max_length=64)
    harness_display_name: str = Field(min_length=1, max_length=128)
    requested_capabilities: tuple[str, ...] = Field(default=(), max_length=64)
    expires_at: datetime
    reason: str = Field(min_length=1, max_length=512)

    @model_validator(mode="after")
    def exact_target(self) -> "SponsoredEnrollmentIntentRequest":
        if (self.target_principal_id is None) == (self.invited_verified_email is None):
            raise ValueError("exactly one sponsored enrollment target is required")
        if self.target_kind == "existing_person" and self.target_principal_id is None:
            raise ValueError("existing-person enrollment requires a principal")
        if self.target_kind == "new_person" and self.invited_verified_email is None:
            raise ValueError("new-person enrollment requires a verified email alias")
        if self.expires_at.tzinfo is None:
            raise ValueError("sponsored enrollment expiry must be timezone-aware")
        if tuple(sorted(set(self.requested_capabilities))) != self.requested_capabilities:
            raise ValueError("requested capabilities must be canonical")
        return self


@dataclass(frozen=True, slots=True)
class CandidateBegin:
    transaction_id: str
    authorization_url: str
    state: str
    continuation_token: str
    expires_at: int


class SponsoredEnrollmentService:
    APPROVAL_PURPOSE = "identity.enrollment.approve"

    def __init__(self, *, store: StoreBackend, provider: OIDCProvider,
                 invitations: InternalInvitationService, approval_client: ApprovalClient,
                 approval_verifier: IndependentApprovalVerifier, require: Callable[..., Any],
                 clock: Callable[[], int] | None = None) -> None:
        self.store = store
        self.provider = provider
        self.invitations = invitations
        self.approval_client = approval_client
        self.approval_verifier = approval_verifier
        self.require = require
        self.clock = clock or (lambda: int(time.time()))

    @staticmethod
    def _hash(value: str) -> str:
        return hashlib.sha256(value.encode()).hexdigest()

    @staticmethod
    def _token() -> str:
        return secrets.token_urlsafe(32)

    @staticmethod
    def _challenge(verifier: str) -> str:
        return base64.urlsafe_b64encode(hashlib.sha256(verifier.encode("ascii")).digest()).rstrip(b"=").decode()

    def begin_candidate(self, *, candidate_harness_id: str, harness_kind: str, harness_name: str,
                        binding_assurance: Literal["os_bound", "hardware_bound"],
                        public_key_pem: str, idempotency_key: str) -> CandidateBegin:
        if not 16 <= len(idempotency_key) <= 128:
            raise ValidationError("candidate idempotency key is invalid")
        key_id = public_key_thumbprint(public_key_pem)
        request = {"schema": "agentnet.sponsored-enrollment.candidate-begin.v1",
                   "candidate_harness_id": candidate_harness_id, "harness_kind": harness_kind,
                   "harness_name": harness_name, "binding_assurance": binding_assurance,
                   "public_key_pem": public_key_pem, "candidate_key_id": key_id}
        digest = canonical_digest(request)
        key_hash = self._hash(idempotency_key)
        existing = self.store.fetch_one("SELECT * FROM console_enrollment_candidates WHERE begin_idempotency_hash=?", (key_hash,))
        if existing is not None:
            if existing["begin_request_digest"] != digest:
                raise ConflictError("candidate begin identifier was reused for different details")
            return CandidateBegin(**self.store.decrypted_payload(str(existing["begin_response_encrypted"]), str(existing["transaction_id"])))
        now, transaction_id = self.clock(), str(uuid4())
        state, nonce, verifier, continuation = self._token(), self._token(), self._token(), self._token()
        response = CandidateBegin(transaction_id, self.provider.authorization_url(state=state, nonce=nonce, code_challenge=self._challenge(verifier)), state, continuation, now + 600)
        with self.store.transaction() as connection:
            connection.execute("""INSERT INTO console_enrollment_candidates(
                transaction_id,begin_idempotency_hash,begin_request_digest,state_hash,nonce_hash,
                continuation_hash,begin_response_encrypted,code_verifier_encrypted,candidate_harness_id,
                candidate_harness_kind,candidate_harness_name,candidate_binding_assurance,
                candidate_public_key_pem,candidate_key_id,state,created_at,updated_at,expires_at)
                VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'waiting_oidc',?,?,?)""",
                (transaction_id,key_hash,digest,self._hash(state),self._hash(nonce),self._hash(continuation),
                 self.store.encrypted_payload(asdict(response), transaction_id),
                 self.store.encrypted_payload({"code_verifier": verifier}, transaction_id),
                 candidate_harness_id,harness_kind,harness_name,binding_assurance,public_key_pem,key_id,
                 now,now,now+600))
        return response

    def owns_state(self, state: str) -> bool:
        return self.store.fetch_one("SELECT transaction_id FROM console_enrollment_candidates WHERE state_hash=?", (self._hash(state),)) is not None

    def complete_candidate_oidc(self, *, state: str, code: str) -> str:
        now = self.clock()
        row = self.store.fetch_one("SELECT * FROM console_enrollment_candidates WHERE state_hash=?", (self._hash(state),))
        if row is None or row["state"] != "waiting_oidc" or int(row["expires_at"]) <= now:
            raise AuthenticationError("sponsored enrollment is unavailable")
        verifier = self.store.decrypted_payload(str(row["code_verifier_encrypted"]), str(row["transaction_id"])).get("code_verifier")
        if not isinstance(verifier, str):
            raise AuthenticationError("sponsored enrollment is unavailable")
        verified = self.provider.exchange_and_verify(code=code, code_verifier=verifier, expected_nonce_hash=str(row["nonce_hash"]))
        matches = []
        for intent in self.store.fetch_all("SELECT * FROM console_enrollment_intents WHERE state='waiting_target' AND expires_at>?", (now,)):
            request = json.loads(str(intent["request_json"]))
            if request.get("harness_kind") != row["candidate_harness_kind"] or request.get("harness_name") != row["candidate_harness_name"]:
                continue
            if intent["target_kind"] == "existing_person":
                principal = self.store.fetch_one("SELECT oidc_issuer,oidc_subject FROM principals WHERE principal_id=? AND status='active'", (intent["target_principal_id"],))
                if principal is None or principal["oidc_issuer"] != verified.identity.issuer or principal["oidc_subject"] != verified.identity.subject:
                    continue
            elif str(intent["invited_email_alias"]).casefold() != verified.identity.verified_email.casefold():
                continue
            matches.append((intent, request))
        if len(matches) != 1:
            raise AuthenticationError("sponsored enrollment is unavailable")
        intent, request = matches[0]
        invitation = InternalInvitationRequest(
            invitation_id=str(intent["intent_id"]), domain_id=str(intent["domain_id"]),
            invited_oidc_issuer=verified.identity.issuer, invited_oidc_subject=verified.identity.subject,
            invited_verified_email=verified.identity.verified_email.casefold(),
            candidate_harness_id=str(row["candidate_harness_id"]), candidate_harness_kind=str(row["candidate_harness_kind"]),
            candidate_harness_display_name=str(row["candidate_harness_name"]), candidate_binding_assurance=str(row["candidate_binding_assurance"]),
            candidate_key_id=str(row["candidate_key_id"]), candidate_public_key_pem=str(row["candidate_public_key_pem"]),
            requested_capabilities=tuple(request.get("capabilities", ())), expires_at=datetime.fromtimestamp(int(intent["expires_at"]), UTC),
            reason=str(request["reason"]))
        with self.store.transaction() as connection:
            one = connection.execute("""UPDATE console_enrollment_candidates SET state='candidate_verified',intent_id=?,oidc_issuer=?,oidc_subject=?,verified_email=?,updated_at=? WHERE transaction_id=? AND state='waiting_oidc'""",
                (intent["intent_id"],verified.identity.issuer,verified.identity.subject,verified.identity.verified_email.casefold(),now,row["transaction_id"]))
            two = connection.execute("""UPDATE console_enrollment_intents SET state='candidate_verified',revision=revision+1,candidate_transaction_id=?,canonical_invitation_json=?,invitation_id=?,updated_at=? WHERE intent_id=? AND state='waiting_target'""",
                (row["transaction_id"],canonical_json(invitation.model_dump(mode="json")).decode(),invitation.invitation_id,now,intent["intent_id"]))
            if one.rowcount != 1 or two.rowcount != 1:
                raise ConflictError("sponsored enrollment changed during identity verification")
        return str(intent["intent_id"])

    def request_approval(self, *, actor: VerifiedActor, intent_id: str) -> str:
        now = self.clock()
        row = self.store.fetch_one("SELECT * FROM console_enrollment_intents WHERE intent_id=? AND domain_id=?", (intent_id,actor.domain_id))
        if row is None or row["sponsor_principal_id"] != actor.principal_id or row["sponsor_harness_id"] != actor.harness_id or row["state"] not in {"candidate_verified","waiting_approval"}:
            raise AuthenticationError("sponsored enrollment is unavailable")
        invitation = InternalInvitationRequest.model_validate_json(str(row["canonical_invitation_json"]))
        resource, context = InternalInvitationService.issuance_binding(invitation)
        decision = self.require(actor=actor, action="identity.internal_invitation.issue", resource=resource, context=context)
        if row["state"] == "waiting_approval":
            return str(row["approval_request_id"])
        candidate = self.store.fetch_one("SELECT * FROM console_enrollment_candidates WHERE transaction_id=?", (row["candidate_transaction_id"],))
        if candidate is None:
            raise AuthenticationError("sponsored enrollment is unavailable")
        expires = min(int(row["expires_at"]), now+600)
        transaction = {"schema":"agentnet.enrollment.challenge.v1","purpose":"human_harness_credential_binding","challenge_id":intent_id,"domain_id":actor.domain_id,
            "nonce":str(candidate["transaction_id"]),"candidate_key":{"algorithm":"ES256/P-256","thumbprint":invitation.candidate_key_id},
            "harness":{"binding_assurance":invitation.candidate_binding_assurance,"display_name":invitation.candidate_harness_display_name,"kind":invitation.candidate_harness_kind,
                       "requested_capabilities":list(invitation.requested_capabilities),"requested_class":"protected_business"},
            "human":{"oidc_issuer":invitation.invited_oidc_issuer,"oidc_subject":invitation.invited_oidc_subject,"verified_email":invitation.invited_verified_email},"issued_at":now,"expires_at":expires}
        digest, possession = canonical_digest(transaction), self._token()
        created = self.approval_client.create_request(idempotency_key=f"sponsored:{intent_id}",domain_id=actor.domain_id,approval_purpose=self.APPROVAL_PURPOSE,
            canonical_transaction=canonical_json(transaction),transaction_digest=digest,possession_hash=hashlib.sha256(possession.encode("ascii")).hexdigest(),request_expires_at=expires)
        request_id = created.get("request_id")
        if not isinstance(request_id,str) or created.get("state") not in {"pending","issued"}:
            raise AuthorizationError("enrollment approval request was not accepted")
        with self.store.transaction() as connection:
            updated=connection.execute("""UPDATE console_enrollment_intents SET state='waiting_approval',revision=revision+1,policy_decision_id=?,approval_request_id=?,approval_transaction_digest=?,approval_transaction_json=?,possession_secret_encrypted=?,updated_at=? WHERE intent_id=? AND state='candidate_verified'""",
                (decision.decision_id,request_id,digest,canonical_json(transaction).decode(),self.store.encrypted_payload({"possession_secret":possession},intent_id),now,intent_id))
            if updated.rowcount != 1: raise ConflictError("enrollment changed while approval was requested")
        return request_id

    def reconcile(self, *, actor: VerifiedActor, intent_id: str) -> str:
        row = self.store.fetch_one(
            "SELECT * FROM console_enrollment_intents WHERE intent_id=? AND domain_id=?",
            (intent_id, actor.domain_id),
        )
        if (
            row is None
            or row["sponsor_principal_id"] != actor.principal_id
            or row["sponsor_harness_id"] != actor.harness_id
        ):
            raise AuthenticationError("sponsored enrollment is unavailable")
        if row["state"] in {"invitation_issued", "waiting_possession", "enrolled"}:
            return str(row["state"])
        if row["state"] != "waiting_approval":
            raise ValidationError("enrollment is not waiting for approval")
        try:
            transaction = json.loads(str(row["approval_transaction_json"]))
            approval_expires_at = transaction["expires_at"]
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            raise AuthenticationError("sponsored enrollment is unavailable") from exc
        if type(approval_expires_at) is not int:
            raise AuthenticationError("sponsored enrollment is unavailable")

        def fail_closed(when: int) -> str:
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """UPDATE console_enrollment_intents
                       SET state='failed',revision=revision+1,terminal_at=?,updated_at=?
                       WHERE intent_id=? AND state='waiting_approval'""",
                    (when, when, intent_id),
                )
                if updated.rowcount != 1:
                    current = connection.execute(
                        "SELECT state FROM console_enrollment_intents WHERE intent_id=?",
                        (intent_id,),
                    ).fetchone()
                    if current is not None and current["state"] in {
                        "invitation_issued",
                        "waiting_possession",
                        "enrolled",
                    }:
                        return str(current["state"])
                    raise ConflictError("enrollment changed while approval was reconciled")
            return "failed"

        request_id = str(row["approval_request_id"])
        digest = str(row["approval_transaction_digest"])
        status = self.approval_client.request_status(
            request_id=request_id,
            transaction_digest=digest,
        )
        now = self.clock()
        if min(int(row["expires_at"]), approval_expires_at) <= now:
            return fail_closed(now)
        if status["state"] == "pending":
            return "waiting_approval"
        if status["state"] in {"rejected", "expired"}:
            return fail_closed(now)
        secret = self.store.decrypted_payload(
            str(row["possession_secret_encrypted"]), intent_id
        ).get("possession_secret")
        if not isinstance(secret, str):
            raise AuthenticationError("sponsored enrollment is unavailable")
        receipt = self.approval_client.retrieve_receipt(
            request_id=request_id,
            possession_secret=secret,
            domain_id=actor.domain_id,
            approval_purpose=self.APPROVAL_PURPOSE,
            transaction_digest=digest,
            idempotency_key=f"sponsored-reconcile:{intent_id}",
        )
        now = self.clock()
        if min(int(row["expires_at"]), approval_expires_at) <= now:
            return fail_closed(now)
        verified = self.approval_verifier.verify(
            canonical_transaction=str(row["approval_transaction_json"]).encode(),
            approval=receipt,
            expected_purpose=self.APPROVAL_PURPOSE,
            expected_domain_id=actor.domain_id,
            when=datetime.fromtimestamp(now, UTC),
        )
        invitation = InternalInvitationRequest.model_validate_json(
            str(row["canonical_invitation_json"])
        )
        authority = IssuanceAuthority(
            actor=actor,
            policy_decision_id=str(row["policy_decision_id"]),
        )

        def complete(connection, record) -> None:
            consume_independent_approval(connection, receipt=verified)
            updated = connection.execute(
                """UPDATE console_enrollment_intents
                   SET state='invitation_issued',revision=revision+1,result_json=?,updated_at=?
                   WHERE intent_id=? AND state='waiting_approval'""",
                (
                    canonical_json(
                        {
                            "invitation_id": record.transaction.invitation_id,
                            "transaction_digest": record.transaction.digest,
                        }
                    ).decode(),
                    now,
                    intent_id,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("enrollment changed before invitation commit")
            connection.execute(
                "UPDATE console_enrollment_candidates SET state='invitation_issued',updated_at=? WHERE intent_id=?",
                (now, intent_id),
            )

        self.invitations.issue(
            invitation,
            authority=authority,
            when=datetime.fromtimestamp(now, UTC),
            commit_callback=complete,
        )
        return "invitation_issued"

    def candidate_status(self, *, continuation_token: str) -> dict[str, Any]:
        now = self.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM console_enrollment_candidates WHERE continuation_hash=?",
                (self._hash(continuation_token),),
            ).fetchone()
            if (
                row is None
                or row["consumed_at"] is not None
                or int(row["expires_at"]) <= now
            ):
                raise AuthenticationError("sponsored enrollment is unavailable")
            result: dict[str, Any] = {
                "state": str(row["state"]),
                "expires_at": int(row["expires_at"]),
            }
            if row["state"] in {"invitation_issued", "waiting_possession", "enrolled"} and row["intent_id"]:
                invitation = connection.execute(
                    "SELECT * FROM internal_invitations WHERE invitation_id=?",
                    (row["intent_id"],),
                ).fetchone()
                if invitation is not None:
                    consumed = connection.execute(
                        """UPDATE console_enrollment_candidates
                           SET consumed_at=?,updated_at=?
                           WHERE transaction_id=? AND consumed_at IS NULL AND expires_at>?""",
                        (now, now, row["transaction_id"], now),
                    )
                    if consumed.rowcount != 1:
                        raise AuthenticationError("sponsored enrollment is unavailable")
                    result["invitation"] = self.invitations._from_row(invitation).model_dump(
                        mode="json"
                    )
            return result


__all__=["CandidateBegin","SponsoredEnrollmentIntentRequest","SponsoredEnrollmentService"]
