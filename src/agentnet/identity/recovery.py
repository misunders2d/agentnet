"""Exact, independently approved credential recovery into a new harness binding."""

from __future__ import annotations

import base64
import hashlib
import secrets
from asyncio import CancelledError as AsyncCancelledError
from collections.abc import Mapping
from concurrent.futures import CancelledError as FutureCancelledError
from dataclasses import asdict, dataclass
from datetime import UTC, datetime, timedelta
from typing import Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.authorization.grants import TaskGrantService
from agentnet.authorization.evidence import require_current_approver_entitlement
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ExtensionError,
    ReplayError,
)
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc import OIDCAuthorizationRequest, OIDCProvider
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import EnrollmentApprovalPolicy
from agentnet.organization.relationships import RelationshipService
from agentnet.security.signatures import canonical_json, verify_signature
from agentnet.storage.backend import StoreBackend


class CredentialRecoveryRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str = Field(default_factory=lambda: str(uuid4()))
    domain_id: str = Field(min_length=1)
    principal_id: str = Field(min_length=1)
    old_harness_id: str = Field(min_length=1)
    expected_credential_epoch: int = Field(ge=1)
    oidc_issuer: str = Field(min_length=1)
    oidc_subject: str = Field(min_length=1)
    verified_email: str = Field(min_length=3)
    new_harness_kind: str = Field(min_length=1, max_length=64)
    new_harness_name: str = Field(min_length=1, max_length=128)
    new_binding_assurance: Literal["os_bound", "hardware_bound"]
    new_public_key_pem: str = Field(min_length=1)
    new_key_id: str = Field(min_length=1)
    issued_at: datetime
    expires_at: datetime

    @model_validator(mode="after")
    def valid_time_window(self) -> "CredentialRecoveryRequest":
        if self.issued_at.tzinfo is None or self.expires_at.tzinfo is None:
            raise ValueError("credential recovery timestamps must be timezone-aware")
        if self.expires_at <= self.issued_at:
            raise ValueError("credential recovery expiry must follow issuance")
        return self

    def signed_fields(self) -> dict[str, object]:
        return self.model_dump(mode="json")


class CredentialRecoveryResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    principal_id: str
    revoked_harness_id: str
    harness_id: str
    credential_id: str
    credential_epoch: Literal[1] = 1
    approval_receipt_ids: tuple[str, ...]


class CredentialRecoveryService:
    APPROVAL_PURPOSE = "identity.credential.recover.approve"

    def __init__(
        self,
        store: StoreBackend,
        approval_verifier: IndependentApprovalVerifier,
        *,
        policy: EnrollmentApprovalPolicy,
        outage_gate: OutageGate | None = None,
        relationships: RelationshipService | None = None,
        task_grants: TaskGrantService | None = None,
        recovered_credential_ttl_seconds: int = 3_600,
    ) -> None:
        if (
            type(recovered_credential_ttl_seconds) is not int
            or not 300 <= recovered_credential_ttl_seconds <= 86_400
        ):
            raise ValueError("recovered credential TTL is outside the secure policy profile")
        self.store = store
        self.approval_verifier = approval_verifier
        self.policy = policy
        self.outage_gate = outage_gate
        self.relationships = relationships or RelationshipService(store)
        self.task_grants = task_grants or TaskGrantService(store)
        self.recovered_credential_ttl_seconds = recovered_credential_ttl_seconds

    def prepare(
        self,
        *,
        identity: VerifiedOIDCIdentity,
        domain_id: str,
        old_harness_id: str,
        new_harness_kind: str,
        new_harness_name: str,
        new_binding_assurance: Literal["os_bound", "hardware_bound"],
        new_public_key_pem: str,
        when: datetime | None = None,
    ) -> CredentialRecoveryRequest:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        when = when or datetime.now(UTC)
        if when.tzinfo is None:
            raise ValueError("recovery time must be timezone-aware")
        row = self.store.fetch_one(
            """SELECT p.principal_id,p.oidc_issuer,p.oidc_subject,p.verified_email,p.status,
                      h.status AS harness_status,h.credential_epoch,
                      h.principal_id AS harness_principal_id,h.domain_id
                 FROM harnesses h JOIN principals p ON p.principal_id=h.principal_id
                WHERE h.harness_id=?""",
            (old_harness_id,),
        )
        if (
            row is None
            or row["domain_id"] != domain_id
            or row["harness_principal_id"] != row["principal_id"]
            or row["status"] != "active"
            or row["harness_status"] not in {"active", "quarantined"}
            or (row["oidc_issuer"], row["oidc_subject"], row["verified_email"])
            != (identity.issuer, identity.subject, identity.verified_email)
        ):
            raise AuthenticationError("recovery identity does not match one current canonical principal")
        return CredentialRecoveryRequest(
            domain_id=domain_id,
            principal_id=row["principal_id"],
            old_harness_id=old_harness_id,
            expected_credential_epoch=int(row["credential_epoch"]),
            oidc_issuer=identity.issuer,
            oidc_subject=identity.subject,
            verified_email=identity.verified_email,
            new_harness_kind=new_harness_kind,
            new_harness_name=new_harness_name,
            new_binding_assurance=new_binding_assurance,
            new_public_key_pem=new_public_key_pem,
            new_key_id=public_key_thumbprint(new_public_key_pem),
            issued_at=when,
            expires_at=when + timedelta(seconds=self.policy.transaction_ttl_seconds),
        )

    def recover(
        self,
        request: CredentialRecoveryRequest,
        *,
        identity: VerifiedOIDCIdentity,
        possession_signature: str,
        approvals: tuple[Mapping[str, object], ...],
        when: datetime | None = None,
        oidc_transaction_id: str | None = None,
    ) -> CredentialRecoveryResult:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        when = when or datetime.now(UTC)
        if when.tzinfo is None:
            raise ValueError("recovery time must be timezone-aware")
        if not (request.issued_at <= when < request.expires_at):
            raise AuthenticationError("credential recovery transaction is expired")
        if request.expires_at - request.issued_at > timedelta(seconds=self.policy.transaction_ttl_seconds):
            raise AuthenticationError("credential recovery transaction exceeds the configured lifetime")
        if (request.oidc_issuer, request.oidc_subject, request.verified_email) != (
            identity.issuer,
            identity.subject,
            identity.verified_email,
        ):
            raise AuthenticationError("credential recovery OIDC binding mismatch")
        if public_key_thumbprint(request.new_public_key_pem) != request.new_key_id:
            raise AuthenticationError("credential recovery key thumbprint mismatch")
        verify_signature(
            request.new_public_key_pem,
            "agentnet.recovery.pop.v1",
            request.signed_fields(),
            possession_signature,
        )
        canonical_transaction = canonical_json(request.signed_fields())
        receipts = tuple(
            self.approval_verifier.verify(
                canonical_transaction=canonical_transaction,
                approval=approval,
                expected_purpose=self.APPROVAL_PURPOSE,
                expected_domain_id=request.domain_id,
                when=when,
            )
            for approval in approvals
        )
        approvers = [receipt.approver_principal_id for receipt in receipts]
        if request.principal_id in approvers:
            raise AuthorizationError("recovery beneficiary cannot approve its own recovery")
        if len(approvers) != len(set(approvers)):
            raise AuthorizationError("duplicate recovery approver cannot satisfy the threshold")
        if len(approvers) < self.policy.recovery_approver_threshold:
            raise AuthorizationError("credential recovery approval threshold was not met")

        new_harness_id = str(uuid4())
        new_credential_id = str(uuid4())
        now = int(when.timestamp())
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
                (request.domain_id,),
            ).fetchone()
            principal = connection.execute(
                "SELECT * FROM principals WHERE principal_id=?",
                (request.principal_id,),
            ).fetchone()
            old = connection.execute(
                "SELECT * FROM harnesses WHERE harness_id=?",
                (request.old_harness_id,),
            ).fetchone()
            if domain is None or domain["status"] != "active" or principal is None or principal["status"] != "active":
                raise AuthenticationError("credential recovery authority is unavailable")
            if (
                principal["domain_id"] != request.domain_id
                or (principal["oidc_issuer"], principal["oidc_subject"], principal["verified_email"])
                != (request.oidc_issuer, request.oidc_subject, request.verified_email)
                or old is None
                or old["domain_id"] != request.domain_id
                or old["principal_id"] != request.principal_id
                or int(old["credential_epoch"]) != request.expected_credential_epoch
                or old["status"] not in {"active", "quarantined"}
            ):
                raise ConflictError("credential recovery state changed before commit")
            resource = f"credential-recovery:{request.request_id}"
            for receipt in receipts:
                require_current_approver_entitlement(
                    connection,
                    domain_id=request.domain_id,
                    approver_principal_id=receipt.approver_principal_id,
                    action=self.APPROVAL_PURPOSE,
                    resource=resource,
                    policy_revision=int(domain["policy_revision"]),
                    when=when,
                )
                consume_independent_approval(connection, receipt=receipt)
            connection.execute(
                "UPDATE harnesses SET status='revoked',credential_epoch=credential_epoch+1 WHERE harness_id=?",
                (request.old_harness_id,),
            )
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE harness_id=? AND status!='revoked'",
                (request.old_harness_id,),
            )
            self.relationships._cascade_revoke_for_harness_in_transaction(
                connection,
                harness_id=request.old_harness_id,
                when=when,
                reason=f"credential_recovery:{request.request_id}",
            )
            self.task_grants._cascade_revoke_for_harness_in_transaction(
                connection,
                harness_id=request.old_harness_id,
                when=when,
                reason=f"credential_recovery:{request.request_id}",
            )
            connection.execute(
                "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
                (request.domain_id,),
            )
            connection.execute(
                """INSERT INTO harnesses(
                       harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,
                       capabilities_json,credential_epoch,created_at
                   ) VALUES(?,?,?,?,?,'active',?,'[]',1,?)""",
                (
                    new_harness_id,
                    request.domain_id,
                    request.principal_id,
                    request.new_harness_kind,
                    request.new_harness_name,
                    request.new_binding_assurance,
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO credentials(
                       credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                   ) VALUES(?,?,?,?,'active',1,?,?)""",
                (
                    new_credential_id,
                    new_harness_id,
                    request.new_key_id,
                    request.new_public_key_pem,
                    now,
                    now + self.recovered_credential_ttl_seconds,
                ),
            )
            connection.execute(
                """INSERT INTO principal_aliases(principal_id,verified_email,first_seen_at,last_seen_at)
                   VALUES(?,?,?,?)
                   ON CONFLICT(principal_id,verified_email) DO UPDATE SET last_seen_at=excluded.last_seen_at""",
                (request.principal_id, request.verified_email, now, now),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "credential.recovered_to_new_binding",
                    "approval_receipt_ids": [receipt.receipt_id for receipt in receipts],
                    "new_credential_id": new_credential_id,
                    "new_harness_id": new_harness_id,
                    "old_harness_id": request.old_harness_id,
                    "principal_id": request.principal_id,
                    "request_id": request.request_id,
                    "recovered_credential_expires_at": now
                    + self.recovered_credential_ttl_seconds,
                    "recovered_credential_ttl_seconds": self.recovered_credential_ttl_seconds,
                    "revocation_epoch": int(domain["revocation_epoch"]) + 1,
                },
            )
            if oidc_transaction_id is not None:
                completed = connection.execute(
                    """UPDATE oidc_recovery_transactions
                          SET status='consumed',consumed_at=?
                        WHERE transaction_id=? AND status='recovering'""",
                    (now, oidc_transaction_id),
                )
                if completed.rowcount != 1:
                    raise ReplayError("OIDC recovery transaction is no longer current")
        return CredentialRecoveryResult(
            principal_id=request.principal_id,
            revoked_harness_id=request.old_harness_id,
            harness_id=new_harness_id,
            credential_id=new_credential_id,
            approval_receipt_ids=tuple(receipt.receipt_id for receipt in receipts),
        )


@dataclass(frozen=True, slots=True)
class VerifiedRecoveryAuthorization:
    transaction_id: str
    request: CredentialRecoveryRequest


class OIDCCredentialRecoveryCoordinator:
    """One-time OIDC reauthentication feeding the approved recovery service.

    Candidate binding fields are stored before redirect. Human identity exists
    only as an encrypted result of the pinned provider verification and is
    never accepted back from the browser.
    """

    def __init__(
        self,
        store: StoreBackend,
        provider: OIDCProvider,
        recovery: CredentialRecoveryService,
    ) -> None:
        if store is not recovery.store:
            raise ValueError("OIDC recovery coordinator and recovery service must share one store")
        self.store = store
        self.provider = provider
        self.recovery = recovery

    def begin_authorization(
        self,
        *,
        domain_id: str,
        old_harness_id: str,
        new_harness_kind: str,
        new_harness_name: str,
        new_binding_assurance: Literal["os_bound", "hardware_bound"],
        new_public_key_pem: str,
    ) -> OIDCAuthorizationRequest:
        if self.recovery.outage_gate is not None:
            self.recovery.outage_gate.require_issuance()
        new_key_id = public_key_thumbprint(new_public_key_pem)
        transaction_id = str(uuid4())
        state = secrets.token_urlsafe(32)
        nonce = secrets.token_urlsafe(32)
        code_verifier = secrets.token_urlsafe(64)
        code_challenge = base64.urlsafe_b64encode(
            hashlib.sha256(code_verifier.encode("ascii")).digest()
        ).rstrip(b"=").decode("ascii")
        authorization_url = self.provider.authorization_url(
            state=state,
            nonce=nonce,
            code_challenge=code_challenge,
        )
        now = self.provider.clock()
        expires_at = now + self.provider.config.authorization_ttl_seconds
        verifier_encrypted = self.store.cipher.encrypt_json(
            {"code_verifier": code_verifier},
            purpose=f"oidc-recovery-pkce:{transaction_id}",
        )
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO oidc_recovery_transactions(
                       transaction_id,domain_id,issuer,client_id,audience,redirect_uri,
                       state_hash,nonce_hash,code_verifier_encrypted,old_harness_id,
                       new_harness_kind,new_harness_name,new_binding_assurance,
                       new_public_key_pem,new_key_id,status,created_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending',?,?)""",
                (
                    transaction_id,
                    domain_id,
                    self.provider.config.issuer,
                    self.provider.config.client_id,
                    self.provider.config.audience,
                    self.provider.config.redirect_uri,
                    hashlib.sha256(state.encode("ascii")).hexdigest(),
                    hashlib.sha256(nonce.encode("ascii")).hexdigest(),
                    verifier_encrypted,
                    old_harness_id,
                    new_harness_kind,
                    new_harness_name,
                    new_binding_assurance,
                    new_public_key_pem,
                    new_key_id,
                    now,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "oidc.recovery.authorization.created",
                    "domain_id": domain_id,
                    "expires_at": expires_at,
                    "issuer": self.provider.config.issuer,
                    "transaction_id": transaction_id,
                },
            )
        return OIDCAuthorizationRequest(transaction_id, authorization_url, state, expires_at)

    def has_state(self, state: str) -> bool:
        if not isinstance(state, str) or len(state) < 32 or len(state) > 256:
            return False
        return self.store.fetch_one(
            "SELECT 1 AS present FROM oidc_recovery_transactions WHERE state_hash=?",
            (hashlib.sha256(state.encode("utf-8")).hexdigest(),),
        ) is not None

    def complete_authorization(self, *, state: str, code: str) -> VerifiedRecoveryAuthorization:
        if not isinstance(state, str) or len(state) < 32 or len(state) > 256:
            raise AuthenticationError("OIDC recovery state is invalid")
        if not isinstance(code, str) or len(code) < 8 or len(code) > 4_096:
            raise AuthenticationError("OIDC recovery code is invalid")
        now = self.provider.clock()
        state_hash = hashlib.sha256(state.encode("utf-8")).hexdigest()
        code_hash = hashlib.sha256(code.encode("utf-8")).hexdigest()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_recovery_transactions WHERE state_hash=?",
                (state_hash,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC recovery state is unavailable")
            if row["status"] != "pending":
                raise ReplayError("OIDC recovery state was already consumed")
            if now >= int(row["expires_at"]):
                connection.execute(
                    "UPDATE oidc_recovery_transactions SET status='failed' WHERE transaction_id=?",
                    (row["transaction_id"],),
                )
                raise AuthenticationError("OIDC recovery state is expired")
            claimed = connection.execute(
                """UPDATE oidc_recovery_transactions SET status='exchanging',claimed_at=?
                     WHERE transaction_id=? AND status='pending'""",
                (now, row["transaction_id"]),
            )
            if claimed.rowcount != 1:
                raise ReplayError("OIDC recovery state was concurrently consumed")
        transaction_id = str(row["transaction_id"])
        try:
            self._require_provider_binding(row)
            if public_key_thumbprint(row["new_public_key_pem"]) != row["new_key_id"]:
                raise AuthenticationError("OIDC recovery candidate key binding is corrupt")
            encrypted = self.store.cipher.decrypt_json(
                row["code_verifier_encrypted"],
                purpose=f"oidc-recovery-pkce:{transaction_id}",
            )
            if not isinstance(encrypted, dict) or not isinstance(encrypted.get("code_verifier"), str):
                raise AuthenticationError("OIDC recovery PKCE state is unavailable")
            if self.store.fetch_one(
                "SELECT 1 AS present FROM replay_nonces WHERE actor_id=? AND nonce_hash=?",
                (self._code_replay_actor, code_hash),
            ) is not None:
                raise ReplayError("OIDC authorization code was already consumed")
            result = self.provider.exchange_and_verify(
                code=code,
                code_verifier=encrypted["code_verifier"],
                expected_nonce_hash=row["nonce_hash"],
            )
            when = datetime.fromtimestamp(now, UTC)
            recovery_request = self.recovery.prepare(
                identity=result.identity,
                domain_id=row["domain_id"],
                old_harness_id=row["old_harness_id"],
                new_harness_kind=row["new_harness_kind"],
                new_harness_name=row["new_harness_name"],
                new_binding_assurance=row["new_binding_assurance"],
                new_public_key_pem=row["new_public_key_pem"],
                when=when,
            )
            recovery_encrypted = self.store.cipher.encrypt_json(
                {
                    "identity": asdict(result.identity),
                    "request": recovery_request.model_dump(mode="json"),
                },
                purpose=f"oidc-recovery-result:{transaction_id}",
            )
            replay_expires_at = max(result.expires_at, now + 86_400)
            with self.store.transaction() as connection:
                current = connection.execute(
                    "SELECT status,claimed_at FROM oidc_recovery_transactions WHERE transaction_id=?",
                    (transaction_id,),
                ).fetchone()
                if current is None or current["status"] != "exchanging" or int(current["claimed_at"]) != now:
                    raise ReplayError("OIDC recovery transaction is no longer current")
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._code_replay_actor, code_hash, replay_expires_at),
                )
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._token_replay_actor, result.id_token_hash, replay_expires_at),
                )
                updated = connection.execute(
                    """UPDATE oidc_recovery_transactions
                          SET status='verified',verified_at=?,authorization_code_hash=?,
                              id_token_hash=?,recovery_request_encrypted=?
                        WHERE transaction_id=? AND status='exchanging'""",
                    (now, code_hash, result.id_token_hash, recovery_encrypted, transaction_id),
                )
                if updated.rowcount != 1:
                    raise ReplayError("OIDC recovery transaction was concurrently consumed")
                self.store.append_audit(
                    connection,
                    {
                        "action": "oidc.recovery.authorization.verified",
                        "domain_id": row["domain_id"],
                        "issuer": row["issuer"],
                        "request_id": recovery_request.request_id,
                        "transaction_id": transaction_id,
                    },
                )
            return VerifiedRecoveryAuthorization(transaction_id, recovery_request)
        except (AsyncCancelledError, FutureCancelledError):
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE oidc_recovery_transactions SET status='failed'
                         WHERE transaction_id=? AND status='exchanging'""",
                    (transaction_id,),
                )
            raise
        except Exception as exc:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE oidc_recovery_transactions SET status='failed'
                         WHERE transaction_id=? AND status='exchanging'""",
                    (transaction_id,),
                )
            if isinstance(exc, ExtensionError):
                raise
            raise AuthenticationError("OIDC recovery authorization could not be verified") from exc

    def complete_recovery(
        self,
        *,
        transaction_id: str,
        possession_signature: str,
        approvals: tuple[Mapping[str, object], ...],
    ) -> CredentialRecoveryResult:
        now = self.provider.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_recovery_transactions WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC recovery transaction is unavailable")
            if row["status"] != "verified":
                raise ReplayError("OIDC recovery transaction is not available for completion")
            claimed = connection.execute(
                """UPDATE oidc_recovery_transactions SET status='recovering',claimed_at=?
                     WHERE transaction_id=? AND status='verified'""",
                (now, transaction_id),
            )
            if claimed.rowcount != 1:
                raise ReplayError("OIDC recovery transaction was concurrently consumed")
        try:
            encrypted = self.store.cipher.decrypt_json(
                row["recovery_request_encrypted"],
                purpose=f"oidc-recovery-result:{transaction_id}",
            )
            if not isinstance(encrypted, dict):
                raise AuthenticationError("OIDC recovery verification result is unavailable")
            identity_value = encrypted.get("identity")
            if not isinstance(identity_value, dict):
                raise AuthenticationError("OIDC recovery identity result is unavailable")
            identity = VerifiedOIDCIdentity(**identity_value)
            request = CredentialRecoveryRequest.model_validate(encrypted.get("request"))
            return self.recovery.recover(
                request,
                identity=identity,
                possession_signature=possession_signature,
                approvals=approvals,
                when=datetime.fromtimestamp(now, UTC),
                oidc_transaction_id=transaction_id,
            )
        except (AsyncCancelledError, FutureCancelledError):
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE oidc_recovery_transactions SET status='verified'
                         WHERE transaction_id=? AND status='recovering'""",
                    (transaction_id,),
                )
            raise
        except Exception:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE oidc_recovery_transactions SET status='verified'
                         WHERE transaction_id=? AND status='recovering'""",
                    (transaction_id,),
                )
            raise

    @property
    def _code_replay_actor(self) -> str:
        return f"oidc-code:{self.provider.config.issuer}:{self.provider.config.client_id}"

    @property
    def _token_replay_actor(self) -> str:
        return f"oidc-token:{self.provider.config.issuer}:{self.provider.config.client_id}"

    def _require_provider_binding(self, row: Mapping[str, object]) -> None:
        expected = (
            self.provider.config.issuer,
            self.provider.config.client_id,
            self.provider.config.audience,
            self.provider.config.redirect_uri,
        )
        actual = (row["issuer"], row["client_id"], row["audience"], row["redirect_uri"])
        if actual != expected:
            raise AuthenticationError("OIDC recovery provider binding changed")


__all__ = [
    "CredentialRecoveryRequest",
    "CredentialRecoveryResult",
    "CredentialRecoveryService",
    "OIDCCredentialRecoveryCoordinator",
    "VerifiedRecoveryAuthorization",
]
