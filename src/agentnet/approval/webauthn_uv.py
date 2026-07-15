"""WebAuthn-UV ceremony state for the separately operated approval service."""

from __future__ import annotations

import hashlib
import secrets
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Mapping
from uuid import uuid4

from webauthn import (
    generate_authentication_options,
    generate_registration_options,
    verify_authentication_response,
    verify_registration_response,
)
from webauthn.helpers import options_to_json_dict
from webauthn.helpers.structs import (
    AuthenticatorSelectionCriteria,
    PublicKeyCredentialDescriptor,
    ResidentKeyRequirement,
    UserVerificationRequirement,
)

from agentnet.approval.config import ApprovalServiceConfig, require_owner_only_file
from agentnet.approval.service import TrustedApprover, create_independent_approval_receipt
from agentnet.approval.store import ApprovalStore
from agentnet.errors import AuthenticationError, ConflictError, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import (
    P256KeyPair,
    b64url_decode,
    b64url_encode,
    canonical_json,
)


_CAPABILITY_PREFIX = "agcap1."
_CAPABILITY_BYTES = 32
_MAX_FAILED_ATTEMPTS = 10


@dataclass(frozen=True, slots=True)
class ApprovalURL:
    url: str
    expires_at: int
    identifier: str
    transaction_digest: str | None = None


def _capability() -> str:
    return _CAPABILITY_PREFIX + b64url_encode(secrets.token_bytes(_CAPABILITY_BYTES))


def _capability_hash(token: str) -> str:
    if not token.startswith(_CAPABILITY_PREFIX):
        raise AuthenticationError("approval request denied")
    encoded = token.removeprefix(_CAPABILITY_PREFIX)
    try:
        decoded = b64url_decode(encoded)
    except Exception as exc:
        raise AuthenticationError("approval request denied") from exc
    if len(decoded) != _CAPABILITY_BYTES or b64url_encode(decoded) != encoded:
        raise AuthenticationError("approval request denied")
    return hashlib.sha256(token.encode("ascii")).hexdigest()


def _user_handle(config: ApprovalServiceConfig, principal_id: str, domain_id: str) -> bytes:
    return hashlib.sha256(
        canonical_json(
            {
                "schema": "agentnet.approval.webauthn-user.v1",
                "verifier_id": config.verifier_id,
                "domain_id": domain_id,
                "approver_principal_id": principal_id,
            }
        )
    ).digest()


def _active_fingerprint(
    *, approver_principal_id: str, domain_id: str, approval_purpose: str, transaction_digest: str
) -> str:
    return hashlib.sha256(
        canonical_json(
            {
                "schema": "agentnet.approval.active-request.v1",
                "approver_principal_id": approver_principal_id,
                "domain_id": domain_id,
                "approval_purpose": approval_purpose,
                "transaction_digest": transaction_digest,
            }
        )
    ).hexdigest()


class WebAuthnApprovalService:
    """Issue existing AgentNet receipts only after exact WebAuthn UV."""

    def __init__(
        self,
        config: ApprovalServiceConfig,
        store: ApprovalStore,
        cipher: LocalEnvelopeCipher,
        *,
        clock: Any | None = None,
    ) -> None:
        self.config = config
        self.store = store
        self.cipher = cipher
        self.clock = clock or (lambda: int(time.time()))
        self._signers: dict[tuple[str, str], P256KeyPair] = {}
        self._trusted: dict[tuple[str, str], TrustedApprover] = {}
        for item in config.approvers:
            signer = P256KeyPair.from_private_pem(
                require_owner_only_file(
                    item.signer_private_key_path,
                    label=f"approval signer {item.principal_id}",
                    max_bytes=16_384,
                )
            )
            if signer.thumbprint != item.signer_key_id:
                raise AuthenticationError("approval signer key identifier mismatch")
            key = (item.domain_id, item.principal_id)
            self._signers[key] = signer
            self._trusted[key] = TrustedApprover(
                principal_id=item.principal_id,
                domain_id=item.domain_id,
                signer_key_id=item.signer_key_id,
                public_key_pem=signer.public_pem,
                allowed_purposes=item.allowed_purposes,
                authority_kind=item.authority_kind,
            )

    def _approver(self, principal_id: str, domain_id: str) -> tuple[TrustedApprover, P256KeyPair]:
        key = (domain_id, principal_id)
        trusted = self._trusted.get(key)
        signer = self._signers.get(key)
        if trusted is None or signer is None:
            raise AuthenticationError("approval request denied")
        return trusted, signer

    def _commit_registration_expirations(self, *, at: int) -> None:
        with self.store.transaction() as connection:
            self._expire_registrations(connection, at=at)

    def _commit_request_expirations(self, *, at: int) -> None:
        with self.store.transaction() as connection:
            self._expire_requests(connection, at=at)

    def begin_registration(self, principal_id: str, *, now: int | None = None) -> ApprovalURL:
        at = self.clock() if now is None else now
        self._commit_registration_expirations(at=at)
        item = self.config.approver(principal_id)
        token = _capability()
        session_id = str(uuid4())
        expires_at = at + self.config.registration_ttl_seconds
        with self.store.transaction() as connection:
            self._expire_registrations(connection, at=at)
            connection.execute(
                """INSERT INTO approval_registration_sessions(
                       session_id,approver_principal_id,domain_id,capability_hash,user_handle_b64,
                       created_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?)""",
                (
                    session_id,
                    item.principal_id,
                    item.domain_id,
                    _capability_hash(token),
                    b64url_encode(_user_handle(self.config, item.principal_id, item.domain_id)),
                    at,
                    expires_at,
                ),
            )
            self._audit(
                connection,
                action="registration.created",
                request_id=session_id,
                principal_id=item.principal_id,
                domain_id=item.domain_id,
                purpose=None,
                digest=None,
                occurred_at=at,
                outcome="pending",
                detail="host_admin_created",
            )
        return ApprovalURL(
            url=f"{self.config.public_origin}/approval#token={token}&kind=registration",
            expires_at=expires_at,
            identifier=session_id,
        )

    def registration_options(self, token: str, *, now: int | None = None) -> dict[str, Any]:
        at = self.clock() if now is None else now
        self._commit_registration_expirations(at=at)
        token_hash = _capability_hash(token)
        with self.store.transaction() as connection:
            self._expire_registrations(connection, at=at)
            row = connection.execute(
                "SELECT * FROM approval_registration_sessions WHERE capability_hash=?",
                (token_hash,),
            ).fetchone()
            self._require_registration_row(row, at=at)
            trusted, _signer = self._approver(row["approver_principal_id"], row["domain_id"])
            challenge = secrets.token_bytes(32)
            challenge_expires_at = min(
                int(row["expires_at"]), at + self.config.challenge_ttl_seconds
            )
            encrypted = self.cipher.encrypt_json(
                {"challenge_b64": b64url_encode(challenge)},
                purpose=f"approval-registration-challenge:{row['session_id']}",
            )
            connection.execute(
                """UPDATE approval_registration_sessions
                      SET challenge_encrypted=?,challenge_expires_at=?
                    WHERE session_id=?""",
                (encrypted, challenge_expires_at, row["session_id"]),
            )
            existing = connection.execute(
                """SELECT credential_id_b64 FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=?""",
                (trusted.principal_id, trusted.domain_id),
            ).fetchall()
            options = generate_registration_options(
                rp_id=self.config.rp_id,
                rp_name=self.config.rp_name,
                user_name=trusted.principal_id,
                user_id=b64url_decode(row["user_handle_b64"]),
                user_display_name=trusted.principal_id,
                challenge=challenge,
                timeout=self.config.challenge_ttl_seconds * 1000,
                authenticator_selection=AuthenticatorSelectionCriteria(
                    resident_key=ResidentKeyRequirement.PREFERRED,
                    user_verification=UserVerificationRequirement.REQUIRED,
                ),
                exclude_credentials=[
                    PublicKeyCredentialDescriptor(id=b64url_decode(item["credential_id_b64"]))
                    for item in existing
                ],
            )
        return {
            "schema": "agentnet.approval.registration-options.v1",
            "expires_at": challenge_expires_at,
            "publicKey": options_to_json_dict(options),
        }

    def complete_registration(
        self,
        token: str,
        credential: Mapping[str, Any],
        *,
        now: int | None = None,
    ) -> dict[str, Any]:
        at = self.clock() if now is None else now
        self._commit_registration_expirations(at=at)
        token_hash = _capability_hash(token)
        failure: Exception | None = None
        result: dict[str, Any] | None = None
        with self.store.transaction() as connection:
            self._expire_registrations(connection, at=at)
            row = connection.execute(
                "SELECT * FROM approval_registration_sessions WHERE capability_hash=?",
                (token_hash,),
            ).fetchone()
            self._require_registration_row(row, at=at, challenge=True)
            self._approver(row["approver_principal_id"], row["domain_id"])
            try:
                stored = self.cipher.decrypt_json(
                    row["challenge_encrypted"],
                    purpose=f"approval-registration-challenge:{row['session_id']}",
                )
                verified = verify_registration_response(
                    credential=dict(credential),
                    expected_challenge=b64url_decode(stored["challenge_b64"]),
                    expected_rp_id=self.config.rp_id,
                    expected_origin=self.config.public_origin,
                    require_user_verification=True,
                )
            except Exception as exc:
                failure = exc
                attempts = min(_MAX_FAILED_ATTEMPTS, int(row["failed_attempts"]) + 1)
                connection.execute(
                    """UPDATE approval_registration_sessions
                          SET challenge_encrypted=NULL,challenge_expires_at=NULL,failed_attempts=?,
                              consumed_at=CASE WHEN ? >= ? THEN ? ELSE consumed_at END
                        WHERE session_id=?""",
                    (attempts, attempts, _MAX_FAILED_ATTEMPTS, at, row["session_id"]),
                )
                self._audit(
                    connection,
                    action="registration.denied",
                    request_id=row["session_id"],
                    principal_id=row["approver_principal_id"],
                    domain_id=row["domain_id"],
                    purpose=None,
                    digest=None,
                    occurred_at=at,
                    outcome="denied",
                    detail="webauthn_verification_failed",
                )
            else:
                credential_id = b64url_encode(verified.credential_id)
                try:
                    connection.execute(
                        """INSERT INTO approval_webauthn_credentials(
                               credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                               credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
                           ) VALUES(?,?,?,?,?,?,?,?,'active',?)""",
                        (
                            credential_id,
                            row["approver_principal_id"],
                            row["domain_id"],
                            row["user_handle_b64"],
                            b64url_encode(verified.credential_public_key),
                            verified.sign_count,
                            str(verified.credential_device_type.value),
                            int(verified.credential_backed_up),
                            at,
                        ),
                    )
                except Exception as exc:
                    failure = exc
                    attempts = min(_MAX_FAILED_ATTEMPTS, int(row["failed_attempts"]) + 1)
                    connection.execute(
                        """UPDATE approval_registration_sessions
                              SET challenge_encrypted=NULL,challenge_expires_at=NULL,failed_attempts=?,
                                  consumed_at=CASE WHEN ? >= ? THEN ? ELSE consumed_at END
                            WHERE session_id=?""",
                        (attempts, attempts, _MAX_FAILED_ATTEMPTS, at, row["session_id"]),
                    )
                    self._audit(
                        connection,
                        action="registration.denied",
                        request_id=row["session_id"],
                        principal_id=row["approver_principal_id"],
                        domain_id=row["domain_id"],
                        purpose=None,
                        digest=None,
                        occurred_at=at,
                        outcome="denied",
                        detail="credential_registration_conflict",
                    )
                if failure is None:
                    connection.execute(
                        """UPDATE approval_registration_sessions
                              SET challenge_encrypted=NULL,challenge_expires_at=NULL,consumed_at=?
                            WHERE session_id=?""",
                        (at, row["session_id"]),
                    )
                    self._audit(
                        connection,
                        action="registration.completed",
                        request_id=row["session_id"],
                        principal_id=row["approver_principal_id"],
                        domain_id=row["domain_id"],
                        purpose=None,
                        digest=None,
                        occurred_at=at,
                        outcome="completed",
                        detail="webauthn_uv",
                    )
                    result = {
                        "schema": "agentnet.approval.registration-result.v1",
                        "registered": True,
                        "credential_id": credential_id,
                    }
        if failure is not None:
            raise AuthenticationError("approval request denied") from failure
        if result is None:  # pragma: no cover - defensive invariant
            raise AuthenticationError("approval request denied")
        return result

    def create_request(
        self,
        *,
        principal_id: str,
        approval_purpose: str,
        canonical_transaction: bytes,
        now: int | None = None,
    ) -> ApprovalURL:
        at = self.clock() if now is None else now
        self._commit_request_expirations(at=at)
        item = self.config.approver(principal_id)
        if approval_purpose not in item.allowed_purposes:
            raise AuthenticationError("approval request denied")
        if not canonical_transaction or len(canonical_transaction) > self.config.max_transaction_bytes:
            raise ValidationError("approval transaction size is invalid")
        transaction_digest = hashlib.sha256(canonical_transaction).hexdigest()
        token = _capability()
        request_id = str(uuid4())
        expires_at = at + self.config.request_ttl_seconds
        fingerprint = _active_fingerprint(
            approver_principal_id=item.principal_id,
            domain_id=item.domain_id,
            approval_purpose=approval_purpose,
            transaction_digest=transaction_digest,
        )
        with self.store.transaction() as connection:
            self._expire_requests(connection, at=at)
            active = connection.execute(
                "SELECT request_id FROM approval_requests WHERE active_fingerprint=?",
                (fingerprint,),
            ).fetchone()
            if active is not None:
                raise ConflictError("an active approval request already exists")
            credentials = connection.execute(
                """SELECT COUNT(*) FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
                (item.principal_id, item.domain_id),
            ).fetchone()[0]
            if not credentials:
                raise AuthenticationError("approval request denied")
            encrypted = self.cipher.encrypt_json(
                {"canonical_transaction": canonical_transaction.decode("utf-8")},
                purpose=f"approval-canonical-transaction:{request_id}",
            )
            connection.execute(
                """INSERT INTO approval_requests(
                       request_id,approver_principal_id,domain_id,approval_purpose,capability_hash,
                       canonical_transaction_encrypted,transaction_digest,state,active_fingerprint,
                       created_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?,'pending',?,?,?)""",
                (
                    request_id,
                    item.principal_id,
                    item.domain_id,
                    approval_purpose,
                    _capability_hash(token),
                    encrypted,
                    transaction_digest,
                    fingerprint,
                    at,
                    expires_at,
                ),
            )
            self._audit(
                connection,
                action="approval.created",
                request_id=request_id,
                principal_id=item.principal_id,
                domain_id=item.domain_id,
                purpose=approval_purpose,
                digest=transaction_digest,
                occurred_at=at,
                outcome="pending",
                detail="host_admin_created",
            )
        return ApprovalURL(
            url=f"{self.config.public_origin}/approval#token={token}&kind=approval",
            expires_at=expires_at,
            identifier=request_id,
            transaction_digest=transaction_digest,
        )

    def request_options(self, token: str, *, now: int | None = None) -> dict[str, Any]:
        at = self.clock() if now is None else now
        self._commit_request_expirations(at=at)
        token_hash = _capability_hash(token)
        with self.store.transaction() as connection:
            self._expire_requests(connection, at=at)
            row = connection.execute(
                "SELECT * FROM approval_requests WHERE capability_hash=?", (token_hash,)
            ).fetchone()
            self._require_request_row(row, at=at, state="pending")
            trusted, _signer = self._approver(row["approver_principal_id"], row["domain_id"])
            if row["approval_purpose"] not in trusted.allowed_purposes:
                raise AuthenticationError("approval request denied")
            canonical = self._canonical_transaction(row)
            if hashlib.sha256(canonical).hexdigest() != row["transaction_digest"]:
                raise AuthenticationError("approval request denied")
            credentials = connection.execute(
                """SELECT credential_id_b64 FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=? AND status='active'
                    ORDER BY credential_id_b64""",
                (trusted.principal_id, trusted.domain_id),
            ).fetchall()
            if not credentials:
                raise AuthenticationError("approval request denied")
            challenge = secrets.token_bytes(32)
            challenge_expires_at = min(
                int(row["expires_at"]), at + self.config.challenge_ttl_seconds
            )
            connection.execute(
                """UPDATE approval_requests SET challenge_encrypted=?,challenge_expires_at=?
                    WHERE request_id=?""",
                (
                    self.cipher.encrypt_json(
                        {"challenge_b64": b64url_encode(challenge)},
                        purpose=f"approval-authentication-challenge:{row['request_id']}",
                    ),
                    challenge_expires_at,
                    row["request_id"],
                ),
            )
            options = generate_authentication_options(
                rp_id=self.config.rp_id,
                challenge=challenge,
                timeout=self.config.challenge_ttl_seconds * 1000,
                allow_credentials=[
                    PublicKeyCredentialDescriptor(id=b64url_decode(item["credential_id_b64"]))
                    for item in credentials
                ],
                user_verification=UserVerificationRequirement.REQUIRED,
            )
        return {
            "schema": "agentnet.approval.request-options.v1",
            "request_id": row["request_id"],
            "approver_principal_id": row["approver_principal_id"],
            "domain_id": row["domain_id"],
            "approval_purpose": row["approval_purpose"],
            "transaction_digest": row["transaction_digest"],
            "canonical_transaction_text": canonical.decode("utf-8"),
            "expires_at": row["expires_at"],
            "challenge_expires_at": challenge_expires_at,
            "publicKey": options_to_json_dict(options),
        }

    def approve_request(
        self,
        token: str,
        credential: Mapping[str, Any],
        *,
        approved: bool,
        now: int | None = None,
    ) -> dict[str, Any]:
        if type(approved) is not bool or approved is not True:
            raise AuthenticationError("approval request denied")
        at = self.clock() if now is None else now
        self._commit_request_expirations(at=at)
        token_hash = _capability_hash(token)
        failure: Exception | None = None
        receipt: dict[str, Any] | None = None
        with self.store.transaction() as connection:
            self._expire_requests(connection, at=at)
            row = connection.execute(
                "SELECT * FROM approval_requests WHERE capability_hash=?", (token_hash,)
            ).fetchone()
            if row is not None and row["state"] == "issued":
                receipt = self._stored_receipt(connection, row, at=at)
            else:
                self._require_request_row(row, at=at, state="pending", challenge=True)
                trusted, signer = self._approver(
                    row["approver_principal_id"], row["domain_id"]
                )
                if row["approval_purpose"] not in trusted.allowed_purposes:
                    raise AuthenticationError("approval request denied")
                canonical = self._canonical_transaction(row)
                if hashlib.sha256(canonical).hexdigest() != row["transaction_digest"]:
                    raise AuthenticationError("approval request denied")
                try:
                    credential_id = str(credential.get("id", ""))
                    credential_row = connection.execute(
                        """SELECT * FROM approval_webauthn_credentials
                            WHERE credential_id_b64=? AND approver_principal_id=?
                              AND domain_id=? AND status='active'""",
                        (
                            credential_id,
                            trusted.principal_id,
                            trusted.domain_id,
                        ),
                    ).fetchone()
                    if credential_row is None:
                        raise AuthenticationError("approval request denied")
                    challenge = self.cipher.decrypt_json(
                        row["challenge_encrypted"],
                        purpose=f"approval-authentication-challenge:{row['request_id']}",
                    )
                    verified = verify_authentication_response(
                        credential=dict(credential),
                        expected_challenge=b64url_decode(challenge["challenge_b64"]),
                        expected_rp_id=self.config.rp_id,
                        expected_origin=self.config.public_origin,
                        credential_public_key=b64url_decode(
                            credential_row["credential_public_key_b64"]
                        ),
                        credential_current_sign_count=int(credential_row["sign_count"]),
                        require_user_verification=True,
                    )
                    if b64url_encode(verified.credential_id) != credential_row["credential_id_b64"]:
                        raise AuthenticationError("approval request denied")
                except Exception as exc:
                    failure = exc
                    attempts = min(_MAX_FAILED_ATTEMPTS, int(row["failed_attempts"]) + 1)
                    terminal = attempts >= _MAX_FAILED_ATTEMPTS
                    connection.execute(
                        """UPDATE approval_requests
                              SET challenge_encrypted=NULL,challenge_expires_at=NULL,failed_attempts=?,
                                  state=CASE WHEN ? THEN 'expired' ELSE state END,
                                  expired_at=CASE WHEN ? THEN ? ELSE expired_at END,
                                  active_fingerprint=CASE WHEN ? THEN NULL ELSE active_fingerprint END
                            WHERE request_id=?""",
                        (attempts, terminal, terminal, at, terminal, row["request_id"]),
                    )
                    self._audit(
                        connection,
                        action="approval.denied",
                        request_id=row["request_id"],
                        principal_id=row["approver_principal_id"],
                        domain_id=row["domain_id"],
                        purpose=row["approval_purpose"],
                        digest=row["transaction_digest"],
                        occurred_at=at,
                        outcome="denied",
                        detail="webauthn_verification_failed",
                    )
                else:
                    issued_at = at
                    expires_at = issued_at + self.config.receipt_ttl_seconds
                    receipt = create_independent_approval_receipt(
                        signer,
                        approver=trusted,
                        verifier_id=self.config.verifier_id,
                        approval_purpose=row["approval_purpose"],
                        canonical_transaction=canonical,
                        authenticated_at=at,
                        issued_at=issued_at,
                        expires_at=expires_at,
                    )
                    encrypted = self.cipher.encrypt_json(
                        receipt,
                        purpose=f"approval-issued-receipt:{row['request_id']}",
                    )
                    receipt_digest = hashlib.sha256(canonical_json(receipt)).hexdigest()
                    connection.execute(
                        "UPDATE approval_webauthn_credentials SET sign_count=? WHERE credential_id_b64=?",
                        (verified.new_sign_count, credential_row["credential_id_b64"]),
                    )
                    connection.execute(
                        """INSERT INTO approval_issued_receipts(
                               request_id,credential_id_b64,authenticated_at,issued_at,
                               receipt_expires_at,receipt_encrypted,receipt_digest
                           ) VALUES(?,?,?,?,?,?,?)""",
                        (
                            row["request_id"],
                            credential_row["credential_id_b64"],
                            at,
                            issued_at,
                            expires_at,
                            encrypted,
                            receipt_digest,
                        ),
                    )
                    self._audit(
                        connection,
                        action="approval.issued",
                        request_id=row["request_id"],
                        principal_id=row["approver_principal_id"],
                        domain_id=row["domain_id"],
                        purpose=row["approval_purpose"],
                        digest=row["transaction_digest"],
                        occurred_at=at,
                        outcome="issued",
                        detail="webauthn_uv",
                    )
                    connection.execute(
                        """UPDATE approval_requests
                              SET state='issued',challenge_encrypted=NULL,challenge_expires_at=NULL
                            WHERE request_id=?""",
                        (row["request_id"],),
                    )
        if failure is not None:
            raise AuthenticationError("approval request denied") from failure
        if receipt is None:
            raise AuthenticationError("approval request denied")
        return receipt

    def reject_request(self, token: str, *, now: int | None = None) -> dict[str, Any]:
        at = self.clock() if now is None else now
        self._commit_request_expirations(at=at)
        token_hash = _capability_hash(token)
        with self.store.transaction() as connection:
            self._expire_requests(connection, at=at)
            row = connection.execute(
                "SELECT * FROM approval_requests WHERE capability_hash=?", (token_hash,)
            ).fetchone()
            self._require_request_row(row, at=at, state="pending")
            self._approver(row["approver_principal_id"], row["domain_id"])
            connection.execute(
                """UPDATE approval_requests
                      SET state='rejected',rejected_at=?,active_fingerprint=NULL,
                          challenge_encrypted=NULL,challenge_expires_at=NULL
                    WHERE request_id=?""",
                (at, row["request_id"]),
            )
            self._audit(
                connection,
                action="approval.rejected",
                request_id=row["request_id"],
                principal_id=row["approver_principal_id"],
                domain_id=row["domain_id"],
                purpose=row["approval_purpose"],
                digest=row["transaction_digest"],
                occurred_at=at,
                outcome="rejected",
                detail="human_rejected",
            )
        return {"schema": "agentnet.approval.rejection.v1", "status": "rejected"}

    def revoke_credential(
        self,
        *,
        principal_id: str,
        credential_id: str,
        reason: str,
        now: int | None = None,
    ) -> dict[str, Any]:
        at = self.clock() if now is None else now
        item = self.config.approver(principal_id)
        if not reason or len(reason) > 512:
            raise ValidationError("credential revocation reason is invalid")
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT * FROM approval_webauthn_credentials
                    WHERE credential_id_b64=? AND approver_principal_id=? AND domain_id=?""",
                (credential_id, item.principal_id, item.domain_id),
            ).fetchone()
            if row is None or row["status"] != "active":
                raise AuthenticationError("approval credential is unavailable")
            connection.execute(
                """UPDATE approval_webauthn_credentials
                      SET status='revoked',revoked_at=?,revocation_reason=?
                    WHERE credential_id_b64=?""",
                (at, reason, credential_id),
            )
            remaining = connection.execute(
                """SELECT COUNT(*) FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
                (item.principal_id, item.domain_id),
            ).fetchone()[0]
            expired = 0
            if not remaining:
                pending = connection.execute(
                    """SELECT request_id,approval_purpose,transaction_digest
                          FROM approval_requests
                         WHERE approver_principal_id=? AND domain_id=? AND state='pending'""",
                    (item.principal_id, item.domain_id),
                ).fetchall()
                connection.execute(
                    """UPDATE approval_requests
                          SET state='expired',expired_at=?,active_fingerprint=NULL,
                              challenge_encrypted=NULL,challenge_expires_at=NULL
                        WHERE approver_principal_id=? AND domain_id=? AND state='pending'""",
                    (at, item.principal_id, item.domain_id),
                )
                expired = len(pending)
                for request in pending:
                    self._audit(
                        connection,
                        action="approval.expired",
                        request_id=request["request_id"],
                        principal_id=item.principal_id,
                        domain_id=item.domain_id,
                        purpose=request["approval_purpose"],
                        digest=request["transaction_digest"],
                        occurred_at=at,
                        outcome="expired",
                        detail="credential_unavailable",
                    )
            self._audit(
                connection,
                action="credential.revoked",
                request_id=None,
                principal_id=item.principal_id,
                domain_id=item.domain_id,
                purpose=None,
                digest=None,
                occurred_at=at,
                outcome="revoked",
                detail="operator_revocation",
            )
        return {"revoked": True, "expired_pending_requests": expired}

    def _canonical_transaction(self, row: Any) -> bytes:
        value = self.cipher.decrypt_json(
            row["canonical_transaction_encrypted"],
            purpose=f"approval-canonical-transaction:{row['request_id']}",
        )
        canonical = value.get("canonical_transaction") if isinstance(value, dict) else None
        if not isinstance(canonical, str):
            raise AuthenticationError("approval request denied")
        return canonical.encode("utf-8")

    def _stored_receipt(self, connection: Any, row: Any, *, at: int) -> dict[str, Any]:
        trusted, _signer = self._approver(row["approver_principal_id"], row["domain_id"])
        if row["approval_purpose"] not in trusted.allowed_purposes:
            raise AuthenticationError("approval request denied")
        issued = connection.execute(
            """SELECT r.*,c.status AS credential_status
                  FROM approval_issued_receipts AS r
                  JOIN approval_webauthn_credentials AS c
                    ON c.credential_id_b64=r.credential_id_b64
                 WHERE r.request_id=?""",
            (row["request_id"],),
        ).fetchone()
        if issued is None or issued["credential_status"] != "active" or int(issued["receipt_expires_at"]) <= at:
            raise AuthenticationError("approval request denied")
        receipt = self.cipher.decrypt_json(
            issued["receipt_encrypted"],
            purpose=f"approval-issued-receipt:{row['request_id']}",
        )
        if not isinstance(receipt, dict) or hashlib.sha256(canonical_json(receipt)).hexdigest() != issued["receipt_digest"]:
            raise AuthenticationError("approval request denied")
        return receipt

    @staticmethod
    def _require_registration_row(row: Any, *, at: int, challenge: bool = False) -> None:
        if (
            row is None
            or row["consumed_at"] is not None
            or int(row["expires_at"]) <= at
            or int(row["failed_attempts"]) >= _MAX_FAILED_ATTEMPTS
            or (challenge and (
                row["challenge_encrypted"] is None
                or row["challenge_expires_at"] is None
                or int(row["challenge_expires_at"]) <= at
            ))
        ):
            raise AuthenticationError("approval request denied")

    @staticmethod
    def _require_request_row(
        row: Any, *, at: int, state: str, challenge: bool = False
    ) -> None:
        if (
            row is None
            or row["state"] != state
            or int(row["expires_at"]) <= at
            or int(row["failed_attempts"]) >= _MAX_FAILED_ATTEMPTS
            or (challenge and (
                row["challenge_encrypted"] is None
                or row["challenge_expires_at"] is None
                or int(row["challenge_expires_at"]) <= at
            ))
        ):
            raise AuthenticationError("approval request denied")

    @classmethod
    def _expire_registrations(cls, connection: Any, *, at: int) -> None:
        rows = connection.execute(
            """SELECT session_id,approver_principal_id,domain_id
                  FROM approval_registration_sessions
                 WHERE consumed_at IS NULL AND expires_at<=?""",
            (at,),
        ).fetchall()
        connection.execute(
            """UPDATE approval_registration_sessions
                  SET consumed_at=?,challenge_encrypted=NULL,challenge_expires_at=NULL
                WHERE consumed_at IS NULL AND expires_at<=?""",
            (at, at),
        )
        for row in rows:
            cls._audit(
                connection,
                action="registration.expired",
                request_id=row["session_id"],
                principal_id=row["approver_principal_id"],
                domain_id=row["domain_id"],
                purpose=None,
                digest=None,
                occurred_at=at,
                outcome="expired",
                detail="session_ttl_expired",
            )

    @classmethod
    def _expire_requests(cls, connection: Any, *, at: int) -> None:
        pending = connection.execute(
            """SELECT request_id,approver_principal_id,domain_id,approval_purpose,transaction_digest
                  FROM approval_requests WHERE state='pending' AND expires_at<=?""",
            (at,),
        ).fetchall()
        issued = connection.execute(
            """SELECT q.request_id,q.approver_principal_id,q.domain_id,q.approval_purpose,
                       q.transaction_digest
                  FROM approval_requests AS q
                  JOIN approval_issued_receipts AS r ON r.request_id=q.request_id
                 WHERE q.state='issued' AND r.receipt_expires_at<=?""",
            (at,),
        ).fetchall()
        connection.execute(
            """UPDATE approval_requests
                  SET state='expired',expired_at=?,active_fingerprint=NULL,
                      challenge_encrypted=NULL,challenge_expires_at=NULL
                WHERE state='pending' AND expires_at<=?""",
            (at, at),
        )
        connection.execute(
            """UPDATE approval_requests
                  SET state='expired',expired_at=?,active_fingerprint=NULL
                WHERE state='issued' AND request_id IN (
                    SELECT request_id FROM approval_issued_receipts WHERE receipt_expires_at<=?
                )""",
            (at, at),
        )
        for row in pending:
            cls._audit(
                connection,
                action="approval.expired",
                request_id=row["request_id"],
                principal_id=row["approver_principal_id"],
                domain_id=row["domain_id"],
                purpose=row["approval_purpose"],
                digest=row["transaction_digest"],
                occurred_at=at,
                outcome="expired",
                detail="request_ttl_expired",
            )
        for row in issued:
            cls._audit(
                connection,
                action="approval.expired",
                request_id=row["request_id"],
                principal_id=row["approver_principal_id"],
                domain_id=row["domain_id"],
                purpose=row["approval_purpose"],
                digest=row["transaction_digest"],
                occurred_at=at,
                outcome="expired",
                detail="receipt_ttl_expired",
            )

    @staticmethod
    def _audit(
        connection: Any,
        *,
        action: str,
        request_id: str | None,
        principal_id: str,
        domain_id: str,
        purpose: str | None,
        digest: str | None,
        occurred_at: int,
        outcome: str,
        detail: str,
    ) -> None:
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            (
                action,
                request_id,
                principal_id,
                domain_id,
                purpose,
                digest,
                occurred_at,
                outcome,
                detail,
            ),
        )


__all__ = ["ApprovalURL", "WebAuthnApprovalService"]
