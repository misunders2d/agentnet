"""Production OIDC authorization-code coordinator for internal invitations.

The coordinator is deliberately separate from invitation issuance and
acceptance.  An invitation has zero authority, and verified OIDC identity is
returned only after a state/nonce/PKCE transaction has been bound to the exact
canonical invitation already stored by :mod:`identity.invitations`.
"""

from __future__ import annotations

import hashlib
import json
import re
import secrets
import sqlite3
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

from agentnet.errors import (
    AuthenticationError,
    ExtensionError,
    ReplayError,
    ValidationError,
)
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitations import InternalInvitationTransaction
from agentnet.identity.oidc import (
    OIDCAuthorizationRequest,
    OIDCProvider,
    OIDCVerificationResult,
)
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend


_SHA256 = re.compile(r"^[0-9a-f]{64}$")


def _is_integrity_constraint_error(exc: Exception) -> bool:
    return isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ in {
        "IntegrityError",
        "UniqueViolation",
    }


def _epoch_seconds(value: datetime) -> int:
    if value.tzinfo is None:
        raise ValidationError("security timestamps must be timezone-aware")
    return int(value.timestamp())


@dataclass(frozen=True, slots=True)
class InternalInvitationOIDCChallenge:
    """Public, bounded material needed to sign the exact candidate PoP."""

    transaction_id: str
    invitation_digest: str
    identity: VerifiedOIDCIdentity
    id_token_hash: str
    expires_at: int
    acceptance_token: str

    def __post_init__(self) -> None:
        if not self.transaction_id or len(self.transaction_id) > 128:
            raise ValidationError("OIDC invitation transaction identifier is invalid")
        if not _SHA256.fullmatch(self.invitation_digest):
            raise ValidationError("OIDC invitation digest is invalid")
        if not isinstance(self.identity, VerifiedOIDCIdentity):
            raise ValidationError("OIDC invitation verified identity is invalid")
        if not _SHA256.fullmatch(self.id_token_hash) or self.expires_at <= 0:
            raise ValidationError("OIDC invitation verification result is invalid")
        if len(self.acceptance_token) < 32 or len(self.acceptance_token) > 256:
            raise ValidationError("OIDC invitation acceptance token is invalid")


class InternalInvitationOIDCCoordinator:
    """Verify invited workforce identity without trusting request claims.

    The returned :class:`OIDCVerificationResult` is evidence for the invitation
    service; it does not consume the invitation or create a principal, harness,
    entitlement, or other authority.  Candidate key proof and final invitation
    consumption remain one atomic operation in ``InternalInvitationService``.
    """

    def __init__(self, store: StoreBackend, provider: OIDCProvider) -> None:
        if not isinstance(provider, OIDCProvider):
            raise TypeError("internal invitation OIDC requires an OIDCProvider")
        self.store = store
        self.provider = provider
        provider_binding = {
            "schema": "agentnet.internal-invitation-oidc-verifier.v1",
            "issuer": provider.config.issuer,
            "client_id": provider.config.client_id,
            "audience": provider.config.audience,
            "redirect_uri": provider.config.redirect_uri,
            "allowed_signing_algorithms": list(provider.config.allowed_signing_algorithms),
            "pinned_jwk_thumbprints": [list(item) for item in provider.config.pinned_jwk_thumbprints],
            "allowed_endpoint_origins": list(provider.config.allowed_endpoint_origins),
        }
        binding_digest = hashlib.sha256(canonical_json(provider_binding)).hexdigest()
        self.verifier_id = f"agentnet-internal-invitation-oidc-v1:{binding_digest}"

    def begin_authorization(
        self,
        invitation_id: str,
        canonical_invitation: bytes,
    ) -> OIDCAuthorizationRequest:
        transaction = self._parse_exact_invitation(canonical_invitation)
        if transaction.invitation_id != invitation_id:
            raise AuthenticationError("internal invitation identifier binding mismatch")
        if transaction.invited_oidc_issuer != self.provider.config.issuer:
            raise AuthenticationError("internal invitation OIDC provider binding mismatch")

        initial_now = int(self.provider.clock())
        invitation_digest = hashlib.sha256(canonical_invitation).hexdigest()
        with self.store.transaction() as connection:
            invitation = self._require_active_invitation(
                connection,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                now=initial_now,
            )
            invitation_revision = int(invitation["revision"])

        transaction_id = str(uuid4())
        state = secrets.token_urlsafe(32)
        nonce = secrets.token_urlsafe(32)
        code_verifier = secrets.token_urlsafe(64)
        code_challenge = self._b64url_sha256(code_verifier)
        authorization_url = self.provider.authorization_url(
            state=state,
            nonce=nonce,
            code_challenge=code_challenge,
        )
        now = int(self.provider.clock())

        with self.store.transaction() as connection:
            invitation = self._require_active_invitation(
                connection,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                now=now,
                expected_revision=invitation_revision,
            )
            expires_at = min(
                now + self.provider.config.authorization_ttl_seconds,
                int(invitation["expires_at"]),
            )
            if expires_at <= now:
                raise AuthenticationError("internal invitation is expired")
            encrypted_verifier = self.store.cipher.encrypt_json(
                {"code_verifier": code_verifier},
                purpose=f"internal-invitation-oidc-pkce:{transaction_id}",
            )
            connection.execute(
                """
                INSERT INTO internal_invitation_oidc_transactions(
                    transaction_id,invitation_id,invitation_digest,invitation_revision,
                    verifier_id,issuer,client_id,audience,redirect_uri,state_hash,nonce_hash,
                    code_verifier_encrypted,status,created_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?, 'pending',?,?)
                """,
                (
                    transaction_id,
                    invitation_id,
                    invitation_digest,
                    invitation_revision,
                    self.verifier_id,
                    self.provider.config.issuer,
                    self.provider.config.client_id,
                    self.provider.config.audience,
                    self.provider.config.redirect_uri,
                    hashlib.sha256(state.encode("ascii")).hexdigest(),
                    hashlib.sha256(nonce.encode("ascii")).hexdigest(),
                    encrypted_verifier,
                    now,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.oidc_authorization.created",
                    "transaction_id": transaction_id,
                    "invitation_id": invitation_id,
                    "invitation_digest": invitation_digest,
                    "verifier_id": self.verifier_id,
                    "issuer": self.provider.config.issuer,
                    "expires_at": expires_at,
                },
            )
        return OIDCAuthorizationRequest(transaction_id, authorization_url, state, expires_at)

    def complete_authorization(
        self,
        *,
        canonical_invitation: bytes,
        evidence: Mapping[str, Any],
    ) -> InternalInvitationOIDCChallenge:
        """Exchange one exact callback for candidate-PoP challenge material."""

        transaction = self._parse_exact_invitation(canonical_invitation)
        if (
            not isinstance(evidence, Mapping)
            or set(evidence.keys()) != {"state", "code"}
        ):
            raise AuthenticationError("OIDC invitation evidence must contain only state and code")
        state = evidence.get("state")
        code = evidence.get("code")
        if not isinstance(state, str) or len(state) < 32 or len(state) > 256:
            raise AuthenticationError("OIDC invitation authorization state is invalid")
        if not isinstance(code, str) or len(code) < 8 or len(code) > 4_096:
            raise AuthenticationError("OIDC invitation authorization code is invalid")
        if transaction.invited_oidc_issuer != self.provider.config.issuer:
            raise AuthenticationError("OIDC invitation provider binding mismatch")

        now = int(self.provider.clock())
        state_hash = hashlib.sha256(state.encode("utf-8")).hexdigest()
        code_hash = hashlib.sha256(code.encode("utf-8")).hexdigest()
        row, already_verified = self._claim_transaction(
            state_hash=state_hash,
            code_hash=code_hash,
            transaction=transaction,
            canonical_invitation=canonical_invitation,
            now=now,
        )
        transaction_id = str(row["transaction_id"])
        if already_verified:
            return self._restore_challenge(
                row=row,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                now=now,
            )
        try:
            if self.store.fetch_one(
                "SELECT 1 AS present FROM replay_nonces WHERE actor_id=? AND nonce_hash=?",
                (self._code_replay_actor, code_hash),
            ) is not None:
                raise ReplayError("OIDC invitation authorization code was already consumed")
            encrypted = self.store.cipher.decrypt_json(
                row["code_verifier_encrypted"],
                purpose=f"internal-invitation-oidc-pkce:{transaction_id}",
            )
            if (
                not isinstance(encrypted, dict)
                or set(encrypted.keys()) != {"code_verifier"}
                or not isinstance(encrypted.get("code_verifier"), str)
                or len(encrypted["code_verifier"]) < 43
                or len(encrypted["code_verifier"]) > 128
            ):
                raise AuthenticationError("OIDC invitation PKCE state is unavailable")
            result = self.provider.exchange_and_verify(
                code=code,
                code_verifier=encrypted["code_verifier"],
                expected_nonce_hash=row["nonce_hash"],
            )
            commit_now = max(now, int(self.provider.clock()))
            self._validate_result(transaction, result, now=commit_now)
            return self._commit_verified_challenge(
                row=row,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                code_hash=code_hash,
                result=result,
                claimed_at=now,
                now=commit_now,
            )
        except Exception as exc:
            failure_now = now
            try:
                failure_now = max(now, int(self.provider.clock()))
            except Exception:
                pass
            self._mark_failed(transaction_id, now=failure_now)
            if isinstance(exc, ExtensionError):
                raise
            raise AuthenticationError("OIDC invitation authorization could not be verified") from exc

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation: bytes,
        evidence: Mapping[str, Any],
        expected_issuer: str,
        when: datetime,
    ) -> OIDCVerificationResult:
        """Consume an exact completed OIDC challenge for invitation acceptance."""

        transaction = self._parse_exact_invitation(canonical_invitation)
        if (
            not isinstance(evidence, Mapping)
            or set(evidence.keys()) != {"transaction_id", "acceptance_token"}
        ):
            raise AuthenticationError(
                "OIDC invitation acceptance evidence must contain only transaction_id and acceptance_token"
            )
        transaction_id = evidence.get("transaction_id")
        acceptance_token = evidence.get("acceptance_token")
        if (
            not isinstance(transaction_id, str)
            or len(transaction_id) < 16
            or len(transaction_id) > 128
        ):
            raise AuthenticationError("OIDC invitation transaction identifier is invalid")
        if (
            not isinstance(acceptance_token, str)
            or len(acceptance_token) < 32
            or len(acceptance_token) > 256
        ):
            raise AuthenticationError("OIDC invitation acceptance token is invalid")
        if (
            not isinstance(expected_issuer, str)
            or expected_issuer != transaction.invited_oidc_issuer
            or expected_issuer != self.provider.config.issuer
        ):
            raise AuthenticationError("OIDC invitation expected issuer binding mismatch")

        now = _epoch_seconds(when)
        acceptance_token_hash = hashlib.sha256(acceptance_token.encode("utf-8")).hexdigest()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC invitation acceptance transaction is unavailable")
            stored_token_hash = row["acceptance_token_hash"]
            if (
                not isinstance(stored_token_hash, str)
                or not secrets.compare_digest(stored_token_hash, acceptance_token_hash)
            ):
                # Never burn or reveal the state of a real challenge to a
                # caller that does not possess its random acceptance secret.
                raise AuthenticationError("OIDC invitation acceptance token is invalid")
            if row["status"] == "consumed":
                raise ReplayError("OIDC invitation acceptance was already consumed")
            if row["status"] != "verified":
                raise AuthenticationError("OIDC invitation acceptance is unavailable")
            self._require_transaction_invitation_binding(
                row,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
            )
            self._require_provider_binding(row)
            if now >= int(row["expires_at"]):
                raise AuthenticationError("OIDC invitation acceptance is expired")
            self._require_active_invitation(
                connection,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                now=now,
                expected_revision=int(row["invitation_revision"]),
            )
            result, restored_token = self._decrypt_verified_payload(row)
            if not secrets.compare_digest(restored_token, acceptance_token):
                raise AuthenticationError("OIDC invitation acceptance token binding changed")
            self._validate_result(transaction, result, now=now)
            if (
                row["id_token_hash"] != result.id_token_hash
                or connection.execute(
                    "SELECT 1 AS present FROM replay_nonces WHERE actor_id=? AND nonce_hash=?",
                    (self._code_replay_actor, row["authorization_code_hash"]),
                ).fetchone()
                is None
                or connection.execute(
                    "SELECT 1 AS present FROM replay_nonces WHERE actor_id=? AND nonce_hash=?",
                    (self._token_replay_actor, result.id_token_hash),
                ).fetchone()
                is None
            ):
                raise AuthenticationError("OIDC invitation replay evidence is unavailable")
            try:
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (
                        self._acceptance_replay_actor,
                        acceptance_token_hash,
                        max(result.expires_at, now + 86_400),
                    ),
                )
            except Exception as exc:
                if _is_integrity_constraint_error(exc):
                    raise ReplayError("OIDC invitation acceptance was already consumed") from exc
                raise
            updated = connection.execute(
                """
                UPDATE internal_invitation_oidc_transactions
                   SET status='consumed',consumed_at=?
                 WHERE transaction_id=? AND status='verified' AND acceptance_token_hash=?
                """,
                (now, transaction_id, acceptance_token_hash),
            )
            if updated.rowcount != 1:
                raise ReplayError("OIDC invitation acceptance was concurrently consumed")
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.oidc_acceptance.consumed",
                    "transaction_id": transaction_id,
                    "invitation_id": transaction.invitation_id,
                    "invitation_digest": row["invitation_digest"],
                    "verifier_id": self.verifier_id,
                    "id_token_hash": result.id_token_hash,
                },
            )
            return result

    @property
    def _code_replay_actor(self) -> str:
        return f"internal-invitation-oidc-code:{self.verifier_id}"

    @property
    def _token_replay_actor(self) -> str:
        return f"internal-invitation-oidc-token:{self.verifier_id}"

    @property
    def _acceptance_replay_actor(self) -> str:
        return f"internal-invitation-oidc-acceptance:{self.verifier_id}"

    def _claim_transaction(
        self,
        *,
        state_hash: str,
        code_hash: str,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
        now: int,
    ) -> tuple[Any, bool]:
        expired = False
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM internal_invitation_oidc_transactions WHERE state_hash=?",
                (state_hash,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC invitation authorization state is unavailable")
            if row["status"] == "verified":
                if not secrets.compare_digest(
                    str(row["authorization_code_hash"]), code_hash
                ):
                    raise ReplayError("OIDC invitation authorization state was already consumed")
                self._require_transaction_invitation_binding(
                    row,
                    transaction=transaction,
                    canonical_invitation=canonical_invitation,
                )
                self._require_provider_binding(row)
                self._require_active_invitation(
                    connection,
                    transaction=transaction,
                    canonical_invitation=canonical_invitation,
                    now=now,
                    expected_revision=int(row["invitation_revision"]),
                )
                if now >= int(row["expires_at"]):
                    raise AuthenticationError("OIDC invitation authorization result is expired")
                return row, True
            if row["status"] != "pending":
                raise ReplayError("OIDC invitation authorization state was already consumed")
            try:
                self._require_transaction_invitation_binding(
                    row,
                    transaction=transaction,
                    canonical_invitation=canonical_invitation,
                )
            except AuthenticationError:
                # A wrong canonical invitation must not let an attacker burn a
                # legitimate state value observed through its own browser.
                raise
            self._require_provider_binding(row)
            self._require_active_invitation(
                connection,
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                now=now,
                expected_revision=int(row["invitation_revision"]),
            )
            if now >= int(row["expires_at"]):
                connection.execute(
                    """
                    UPDATE internal_invitation_oidc_transactions
                       SET status='failed',consumed_at=?
                     WHERE transaction_id=? AND status='pending'
                    """,
                    (now, row["transaction_id"]),
                )
                expired = True
            else:
                updated = connection.execute(
                    """
                    UPDATE internal_invitation_oidc_transactions
                       SET status='exchanging',claimed_at=?
                     WHERE transaction_id=? AND status='pending'
                    """,
                    (now, row["transaction_id"]),
                )
                if updated.rowcount != 1:
                    raise ReplayError("OIDC invitation authorization state was already consumed")
                self.store.append_audit(
                    connection,
                    {
                        "action": "internal_invitation.oidc_authorization.claimed",
                        "transaction_id": row["transaction_id"],
                        "invitation_id": transaction.invitation_id,
                    },
                )
        if expired:
            raise AuthenticationError("OIDC invitation authorization state is expired")
        return row, False

    def _commit_verified_challenge(
        self,
        *,
        row: Any,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
        code_hash: str,
        result: OIDCVerificationResult,
        claimed_at: int,
        now: int,
    ) -> InternalInvitationOIDCChallenge:
        replay_expires_at = max(result.expires_at, now + 86_400)
        acceptance_token = secrets.token_urlsafe(32)
        acceptance_token_hash = hashlib.sha256(acceptance_token.encode("ascii")).hexdigest()
        try:
            with self.store.transaction() as connection:
                current = connection.execute(
                    "SELECT * FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
                    (row["transaction_id"],),
                ).fetchone()
                if (
                    current is None
                    or current["status"] != "exchanging"
                    or int(current["claimed_at"]) != claimed_at
                ):
                    raise ReplayError("OIDC invitation transaction is no longer current")
                self._require_provider_binding(current)
                self._require_transaction_invitation_binding(
                    current,
                    transaction=transaction,
                    canonical_invitation=canonical_invitation,
                )
                if now >= int(current["expires_at"]):
                    raise AuthenticationError("OIDC invitation authorization state is expired")
                self._require_active_invitation(
                    connection,
                    transaction=transaction,
                    canonical_invitation=canonical_invitation,
                    now=now,
                    expected_revision=int(current["invitation_revision"]),
                )
                self._validate_result(transaction, result, now=now)
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._code_replay_actor, code_hash, replay_expires_at),
                )
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._token_replay_actor, result.id_token_hash, replay_expires_at),
                )
                encrypted_result = self.store.cipher.encrypt_json(
                    {
                        "schema": "agentnet.internal-invitation-oidc-result.v1",
                        "identity": {
                            "issuer": result.identity.issuer,
                            "subject": result.identity.subject,
                            "verified_email": result.identity.verified_email,
                        },
                        "id_token_hash": result.id_token_hash,
                        "expires_at": result.expires_at,
                        # Encrypted recovery makes an exact callback retry
                        # idempotent after response loss; only its digest is in
                        # queryable columns.
                        "acceptance_token": acceptance_token,
                    },
                    purpose=f"internal-invitation-oidc-verification:{current['transaction_id']}",
                )
                acceptance_expires_at = min(
                    result.expires_at,
                    _epoch_seconds(transaction.expires_at),
                )
                updated = connection.execute(
                    """
                    UPDATE internal_invitation_oidc_transactions
                       SET status='verified',authorization_code_hash=?,id_token_hash=?,
                           verification_result_encrypted=?,acceptance_token_hash=?,expires_at=?
                     WHERE transaction_id=? AND status='exchanging' AND claimed_at=?
                    """,
                    (
                        code_hash,
                        result.id_token_hash,
                        encrypted_result,
                        acceptance_token_hash,
                        acceptance_expires_at,
                        current["transaction_id"],
                        claimed_at,
                    ),
                )
                if updated.rowcount != 1:
                    raise ReplayError("OIDC invitation transaction was concurrently consumed")
                self.store.append_audit(
                    connection,
                    {
                        "action": "internal_invitation.oidc_authorization.verified",
                        "transaction_id": current["transaction_id"],
                        "invitation_id": transaction.invitation_id,
                        "invitation_digest": current["invitation_digest"],
                        "verifier_id": self.verifier_id,
                        "issuer": self.provider.config.issuer,
                        "id_token_hash": result.id_token_hash,
                        "acceptance_expires_at": acceptance_expires_at,
                    },
                )
            return InternalInvitationOIDCChallenge(
                transaction_id=str(row["transaction_id"]),
                invitation_digest=hashlib.sha256(canonical_invitation).hexdigest(),
                identity=result.identity,
                id_token_hash=result.id_token_hash,
                expires_at=result.expires_at,
                acceptance_token=acceptance_token,
            )
        except Exception as exc:
            if _is_integrity_constraint_error(exc):
                raise ReplayError(
                    "OIDC invitation authorization code or ID token was already consumed"
                ) from exc
            raise

    def _restore_challenge(
        self,
        *,
        row: Any,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
        now: int,
    ) -> InternalInvitationOIDCChallenge:
        result, acceptance_token = self._decrypt_verified_payload(row)
        self._validate_result(transaction, result, now=now)
        return InternalInvitationOIDCChallenge(
            transaction_id=str(row["transaction_id"]),
            invitation_digest=hashlib.sha256(canonical_invitation).hexdigest(),
            identity=result.identity,
            id_token_hash=result.id_token_hash,
            expires_at=result.expires_at,
            acceptance_token=acceptance_token,
        )

    def _decrypt_verified_payload(
        self,
        row: Any,
    ) -> tuple[OIDCVerificationResult, str]:
        encrypted = row["verification_result_encrypted"]
        if not isinstance(encrypted, str) or not encrypted:
            raise AuthenticationError("OIDC invitation verification result is unavailable")
        payload = self.store.cipher.decrypt_json(
            encrypted,
            purpose=f"internal-invitation-oidc-verification:{row['transaction_id']}",
        )
        if not isinstance(payload, dict) or set(payload.keys()) != {
            "schema",
            "identity",
            "id_token_hash",
            "expires_at",
            "acceptance_token",
        }:
            raise AuthenticationError("OIDC invitation verification result is invalid")
        identity = payload.get("identity")
        if (
            payload.get("schema") != "agentnet.internal-invitation-oidc-result.v1"
            or not isinstance(identity, dict)
            or set(identity.keys()) != {"issuer", "subject", "verified_email"}
            or not isinstance(payload.get("id_token_hash"), str)
            or type(payload.get("expires_at")) is not int
            or not isinstance(payload.get("acceptance_token"), str)
        ):
            raise AuthenticationError("OIDC invitation verification result is invalid")
        try:
            result = OIDCVerificationResult(
                identity=VerifiedOIDCIdentity(
                    issuer=identity["issuer"],
                    subject=identity["subject"],
                    verified_email=identity["verified_email"],
                ),
                id_token_hash=payload["id_token_hash"],
                expires_at=payload["expires_at"],
            )
        except Exception as exc:
            raise AuthenticationError("OIDC invitation verification result is invalid") from exc
        acceptance_token = payload["acceptance_token"]
        if (
            len(acceptance_token) < 32
            or len(acceptance_token) > 256
            or not secrets.compare_digest(
                hashlib.sha256(acceptance_token.encode("utf-8")).hexdigest(),
                str(row["acceptance_token_hash"]),
            )
            or result.id_token_hash != row["id_token_hash"]
        ):
            raise AuthenticationError("OIDC invitation verification result binding changed")
        return result, acceptance_token

    @staticmethod
    def _require_transaction_invitation_binding(
        row: Any,
        *,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
    ) -> None:
        invitation_digest = hashlib.sha256(canonical_invitation).hexdigest()
        if (
            row["invitation_id"] != transaction.invitation_id
            or not secrets.compare_digest(str(row["invitation_digest"]), invitation_digest)
        ):
            raise AuthenticationError("OIDC invitation canonical state binding mismatch")

    def _require_provider_binding(self, row: Any) -> None:
        expected = {
            "verifier_id": self.verifier_id,
            "issuer": self.provider.config.issuer,
            "client_id": self.provider.config.client_id,
            "audience": self.provider.config.audience,
            "redirect_uri": self.provider.config.redirect_uri,
        }
        if any(row[field] != value for field, value in expected.items()):
            raise AuthenticationError("OIDC invitation authorization provider binding mismatch")

    def _require_active_invitation(
        self,
        connection: Any,
        *,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
        now: int,
        expected_revision: int | None = None,
    ) -> Any:
        row = connection.execute(
            "SELECT * FROM internal_invitations WHERE invitation_id=?",
            (transaction.invitation_id,),
        ).fetchone()
        if row is None:
            raise AuthenticationError("internal invitation is unavailable")
        digest = hashlib.sha256(canonical_invitation).hexdigest()
        try:
            capabilities = json.loads(row["requested_capabilities_json"])
        except (TypeError, json.JSONDecodeError) as exc:
            raise AuthenticationError("stored internal invitation binding is corrupt") from exc
        if (
            row["schema_version"] != "1.0"
            or row["canonical_invitation_json"] != canonical_invitation.decode("utf-8")
            or not secrets.compare_digest(str(row["invitation_digest"]), digest)
            or row["domain_id"] != transaction.domain_id
            or row["invited_oidc_issuer"] != transaction.invited_oidc_issuer
            or row["invited_oidc_subject"] != transaction.invited_oidc_subject
            or row["invited_verified_email"] != transaction.invited_verified_email
            or row["sponsor_authority_kind"] != transaction.sponsor_authority_kind
            or row["sponsor_authority_id"] != transaction.sponsor_authority_id
            or row["sponsor_harness_id"] != transaction.sponsor_harness_id
            or row["sponsor_credential_id"] != transaction.sponsor_credential_id
            or int(row["sponsor_credential_epoch"]) != transaction.sponsor_credential_epoch
            or row["candidate_harness_id"] != transaction.candidate_harness_id
            or row["candidate_harness_kind"] != transaction.candidate_harness_kind
            or row["candidate_key_id"] != transaction.candidate_key_id
            or row["candidate_public_key_pem"] != transaction.candidate_public_key_pem
            or capabilities != list(transaction.requested_capabilities)
            or int(row["policy_revision"]) != transaction.policy_revision
            or int(row["domain_revocation_epoch"]) != transaction.domain_revocation_epoch
            or int(row["max_uses"]) != 1
            or int(row["expires_at"]) != _epoch_seconds(transaction.expires_at)
        ):
            raise AuthenticationError("stored internal invitation canonical binding mismatch")
        revision = int(row["revision"])
        if expected_revision is not None and revision != expected_revision:
            raise AuthenticationError("internal invitation revision changed")
        if (
            row["state"] != "active"
            or int(row["use_count"]) != 0
            or now >= int(row["expires_at"])
        ):
            raise AuthenticationError("internal invitation is not active")
        if transaction.invited_oidc_issuer != self.provider.config.issuer:
            raise AuthenticationError("internal invitation OIDC provider binding mismatch")
        domain = connection.execute(
            "SELECT * FROM domains WHERE domain_id=?",
            (transaction.domain_id,),
        ).fetchone()
        if (
            domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != transaction.policy_revision
            or int(domain["revocation_epoch"]) != transaction.domain_revocation_epoch
        ):
            raise AuthenticationError("internal invitation domain or policy binding changed")
        sponsor = load_credential_binding_from_connection(
            connection,
            transaction.sponsor_credential_id,
        )
        sponsor.require_active(now=now)
        sponsor_authority_id = (
            sponsor.principal_id
            if transaction.sponsor_authority_kind == "human"
            else sponsor.guest_id
        )
        if (
            sponsor.domain_id != transaction.domain_id
            or sponsor.harness_id != transaction.sponsor_harness_id
            or sponsor.credential_epoch != transaction.sponsor_credential_epoch
            or sponsor_authority_id != transaction.sponsor_authority_id
        ):
            raise AuthenticationError("internal invitation sponsor authority changed")
        return row

    def _mark_failed(self, transaction_id: str, *, now: int) -> None:
        try:
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """
                    UPDATE internal_invitation_oidc_transactions
                       SET status='failed',consumed_at=?
                     WHERE transaction_id=? AND status='exchanging'
                    """,
                    (now, transaction_id),
                )
                if updated.rowcount:
                    self.store.append_audit(
                        connection,
                        {
                            "action": "internal_invitation.oidc_authorization.failed",
                            "transaction_id": transaction_id,
                        },
                    )
        except Exception:
            # Preserve the verification exception.  A transaction stranded in
            # ``exchanging`` cannot be retried and therefore still fails closed.
            return

    @staticmethod
    def _parse_exact_invitation(value: bytes) -> InternalInvitationTransaction:
        if not isinstance(value, bytes) or len(value) < 128 or len(value) > 65_536:
            raise ValidationError("canonical invitation bytes are outside the supported size")

        def reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
            result: dict[str, Any] = {}
            for key, item in pairs:
                if key in result:
                    raise ValueError("duplicate JSON member")
                result[key] = item
            return result

        try:
            decoded = json.loads(
                value.decode("utf-8"),
                object_pairs_hook=reject_duplicates,
                parse_constant=lambda _value: (_ for _ in ()).throw(
                    ValueError("non-finite JSON number")
                ),
            )
            if not isinstance(decoded, dict):
                raise ValueError("not an object")
            transaction = InternalInvitationTransaction.model_validate(decoded)
        except Exception as exc:
            raise ValidationError("canonical invitation does not match the strict schema") from exc
        if canonical_json(transaction.model_dump(mode="json")) != value:
            raise ValidationError("invitation bytes are not exactly canonical")
        return transaction

    @staticmethod
    def _validate_result(
        transaction: InternalInvitationTransaction,
        result: OIDCVerificationResult,
        *,
        now: int,
    ) -> None:
        if not isinstance(result, OIDCVerificationResult):
            raise AuthenticationError("OIDC invitation verifier returned an invalid result")
        identity = result.identity
        if (
            identity.issuer != transaction.invited_oidc_issuer
            or identity.subject != transaction.invited_oidc_subject
            or identity.verified_email != transaction.invited_verified_email
            or not _SHA256.fullmatch(result.id_token_hash)
            or now >= result.expires_at
        ):
            raise AuthenticationError("verified OIDC identity does not match the invitation")

    @staticmethod
    def _b64url_sha256(value: str) -> str:
        import base64

        digest = hashlib.sha256(value.encode("ascii")).digest()
        return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


__all__ = [
    "InternalInvitationOIDCChallenge",
    "InternalInvitationOIDCCoordinator",
]
