"""Stable OIDC-authenticated owner session and first-passkey registration."""

from __future__ import annotations

import hashlib
import secrets
import sqlite3
import time
from dataclasses import dataclass
from types import MappingProxyType
from typing import Any, Literal, Mapping
from uuid import NAMESPACE_URL, uuid4, uuid5

from pydantic import BaseModel, ConfigDict, Field
from webauthn import generate_registration_options, verify_registration_response
from webauthn.helpers import options_to_json_dict
from webauthn.helpers.structs import (
    AuthenticatorSelectionCriteria,
    PublicKeyCredentialDescriptor,
    ResidentKeyRequirement,
    UserVerificationRequirement,
)

from agentnet.approval.store import ApprovalStore
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc_callback import OIDCCallbackSuccess
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_json


STABLE_APPROVAL_PATH = "/approval"
OWNER_SESSION_COOKIE_NAME = "__Host-agentnet-approval"
OWNER_PREAUTH_COOKIE_NAME = "__Host-agentnet-approval-preauth"
OWNER_CSRF_COOKIE_NAME = "__Host-agentnet-approval-csrf"
OWNER_SESSION_COOKIE_SECURE = True
OWNER_SESSION_COOKIE_HTTP_ONLY = True
OWNER_SESSION_COOKIE_SAME_SITE = "strict"
_MAX_REGISTRATION_FAILURES = 20
_MAX_REGISTRATION_ROTATIONS = 10

ALLOWED_OIDC_LOGIN_TRANSITIONS = MappingProxyType(
    {
        "pending": frozenset({"callback_claimed", "failed", "expired", "canceled"}),
        "callback_claimed": frozenset({"callback_consumed", "failed", "expired"}),
        "callback_consumed": frozenset(),
        "failed": frozenset(),
        "expired": frozenset(),
        "canceled": frozenset(),
    }
)


class OwnerOIDCStartRequest(BaseModel):
    """Identity/provider claims are server-owned and never accepted from browser JSON."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_id: Literal["agentnet.approval.owner-oidc-start.v1"] = Field(alias="schema")
    csrf_token: str = Field(min_length=32, max_length=256)


# Backward-compatible name for the strict recognized success projection.
OwnerOIDCCallbackQuery = OIDCCallbackSuccess


class OwnerRegistrationBeginRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_id: Literal["agentnet.approval.owner-registration-begin.v1"] = Field(alias="schema")
    csrf_token: str = Field(min_length=32, max_length=256)


class OwnerRegistrationCompleteRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_id: Literal["agentnet.approval.owner-registration-complete.v1"] = Field(alias="schema")
    csrf_token: str = Field(min_length=32, max_length=256)
    ceremony_id: str = Field(min_length=16, max_length=128)
    credential: dict[str, Any]


class OwnerApprovalSelectRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_id: Literal["agentnet.approval.owner-request-select.v1"] = Field(alias="schema")
    csrf_token: str = Field(min_length=32, max_length=256)
    request_id: str = Field(min_length=16, max_length=128)


class OwnerApprovalCompleteRequest(OwnerApprovalSelectRequest):
    schema_id: Literal["agentnet.approval.owner-request-complete.v1"] = Field(alias="schema")
    credential: dict[str, Any]


ALLOWED_REGISTRATION_TRANSITIONS = MappingProxyType(
    {
        "pending": frozenset({"verified", "failed", "expired", "canceled"}),
        "verified": frozenset(),
        "failed": frozenset(),
        "expired": frozenset(),
        "canceled": frozenset(),
    }
)


@dataclass(frozen=True, slots=True)
class OwnerPreauthSession:
    session_token: str
    csrf_token: str


@dataclass(frozen=True, slots=True)
class OwnerOIDCStart:
    authorization_url: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class OwnerAuthenticatedSession:
    session_token: str
    csrf_token: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class OwnerSessionStatus:
    authenticated: bool
    csrf_token: str
    expires_at: int
    credential_registered: bool


@dataclass(frozen=True, slots=True)
class OwnerRegistrationCeremony:
    ceremony_id: str
    expires_at: int
    public_key: dict[str, Any]


def require_owner_session_transition(
    current: str,
    target: str,
    *,
    registration: bool = False,
) -> None:
    """Reject unknown, terminal, backward, or implicit state changes."""

    transitions = (
        ALLOWED_REGISTRATION_TRANSITIONS if registration else ALLOWED_OIDC_LOGIN_TRANSITIONS
    )
    if current not in transitions or target not in transitions[current]:
        raise ConflictError("owner ceremony state transition rejected")


def _browser_secret() -> str:
    return b64url_encode(secrets.token_bytes(32))


def _secret_hash(value: str) -> str:
    if not isinstance(value, str) or not 32 <= len(value) <= 256:
        raise AuthenticationError("owner session denied")
    try:
        value.encode("ascii")
    except UnicodeEncodeError as exc:
        raise AuthenticationError("owner session denied") from exc
    return hashlib.sha256(value.encode("ascii")).hexdigest()


def _user_handle(config: Any, principal_id: str, domain_id: str) -> bytes:
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


class OwnerSessionService:
    """Approval-local OIDC/session owner for stable first-passkey registration."""

    def __init__(
        self,
        config: Any,
        store: ApprovalStore,
        cipher: LocalEnvelopeCipher,
        provider: Any,
        *,
        approval_service: Any | None = None,
        clock: Any | None = None,
        session_ttl_seconds: int = 900,
    ) -> None:
        if session_ttl_seconds < 60 or session_ttl_seconds > 3_600:
            raise ValueError("owner session TTL must be between 60 and 3600 seconds")
        self.config = config
        self.store = store
        self.cipher = cipher
        self.provider = provider
        self.approval_service = approval_service
        self.clock = clock or (lambda: int(time.time()))
        self.session_ttl_seconds = session_ttl_seconds

    def create_preauth(self) -> OwnerPreauthSession:
        return OwnerPreauthSession(_browser_secret(), _browser_secret())

    def begin_oidc_login(
        self,
        *,
        preauth_cookie: str,
        csrf_cookie: str,
        csrf_token: str,
    ) -> OwnerOIDCStart:
        if not secrets.compare_digest(csrf_cookie, csrf_token):
            raise AuthenticationError("owner session denied")
        preauth_hash = _secret_hash(preauth_cookie)
        csrf_hash = _secret_hash(csrf_token)
        now = self.clock()
        state = _browser_secret()
        nonce = _browser_secret()
        code_verifier = b64url_encode(secrets.token_bytes(64))
        code_challenge = b64url_encode(hashlib.sha256(code_verifier.encode("ascii")).digest())
        login_id = str(uuid4())
        expires_at = now + int(self.provider.config.authorization_ttl_seconds)
        encrypted = self.cipher.encrypt_json(
            {
                "code_verifier": code_verifier,
                "provider_audience": self.provider.config.audience,
            },
            purpose=f"approval-owner-oidc:{login_id}",
        )
        try:
            with self.store.transaction() as connection:
                self._expire(connection, at=now)
                connection.execute(
                    """INSERT INTO approval_oidc_login_transactions(
                           login_id,preauth_session_hash,preauth_csrf_hash,oidc_issuer,
                           client_id,redirect_uri,state_hash,nonce_hash,code_verifier_encrypted,
                           state,created_at,expires_at
                       ) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?)""",
                    (
                        login_id,
                        preauth_hash,
                        csrf_hash,
                        self.provider.config.issuer,
                        self.provider.config.client_id,
                        self.provider.config.redirect_uri,
                        hashlib.sha256(state.encode("ascii")).hexdigest(),
                        hashlib.sha256(nonce.encode("ascii")).hexdigest(),
                        encrypted,
                        now,
                        expires_at,
                    ),
                )
        except sqlite3.IntegrityError as exc:
            raise AuthenticationError("owner session denied") from exc
        return OwnerOIDCStart(
            self.provider.authorization_url(
                state=state,
                nonce=nonce,
                code_challenge=code_challenge,
            ),
            expires_at,
        )

    def fail_oidc_login(
        self,
        *,
        preauth_cookie: str,
        state: str,
    ) -> None:
        """Atomically terminate one browser-bound pending login after provider denial."""

        preauth_hash = _secret_hash(preauth_cookie)
        state_hash = _secret_hash(state)
        now = self.clock()
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT * FROM approval_oidc_login_transactions
                    WHERE state_hash=? AND preauth_session_hash=?""",
                (state_hash, preauth_hash),
            ).fetchone()
            if row is None or row["state"] != "pending" or now >= int(row["expires_at"]):
                raise AuthenticationError("owner session denied")
            if (
                row["oidc_issuer"] != self.provider.config.issuer
                or row["client_id"] != self.provider.config.client_id
                or row["redirect_uri"] != self.provider.config.redirect_uri
            ):
                raise AuthenticationError("owner session denied")
            protected = self._decrypt_oidc_login(row)
            if protected.get("provider_audience") != self.provider.config.audience:
                raise AuthenticationError("owner session denied")
            updated = connection.execute(
                """UPDATE approval_oidc_login_transactions
                      SET state='failed',callback_claimed_at=?,failure_code='provider_denied'
                    WHERE login_id=? AND state='pending'""",
                (now, row["login_id"]),
            )
            if updated.rowcount != 1:
                raise AuthenticationError("owner session denied")

    def complete_oidc_login(
        self,
        *,
        preauth_cookie: str,
        state: str,
        code: str,
    ) -> OwnerAuthenticatedSession:
        preauth_hash = _secret_hash(preauth_cookie)
        state_hash = _secret_hash(state)
        if not isinstance(code, str) or not 1 <= len(code) <= 4_096:
            raise AuthenticationError("owner session denied")
        now = self.clock()
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT * FROM approval_oidc_login_transactions
                    WHERE state_hash=? AND preauth_session_hash=?""",
                (state_hash, preauth_hash),
            ).fetchone()
            if row is None or row["state"] != "pending" or now >= int(row["expires_at"]):
                raise AuthenticationError("owner session denied")
            if (
                row["oidc_issuer"] != self.provider.config.issuer
                or row["client_id"] != self.provider.config.client_id
                or row["redirect_uri"] != self.provider.config.redirect_uri
            ):
                raise AuthenticationError("owner session denied")
            protected = self._decrypt_oidc_login(row)
            if protected.get("provider_audience") != self.provider.config.audience:
                raise AuthenticationError("owner session denied")
            updated = connection.execute(
                """UPDATE approval_oidc_login_transactions
                      SET state='callback_claimed',callback_claimed_at=?
                    WHERE login_id=? AND state='pending'""",
                (now, row["login_id"]),
            )
            if updated.rowcount != 1:
                raise AuthenticationError("owner session denied")
        try:
            code_verifier = protected["code_verifier"]
            if not isinstance(code_verifier, str):
                raise TypeError("missing verifier")
            verified = self.provider.exchange_and_verify(
                code=code,
                code_verifier=code_verifier,
                expected_nonce_hash=row["nonce_hash"],
            )
            owner, pin_source = self._resolve_owner(verified.identity)
        except Exception as exc:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE approval_oidc_login_transactions
                          SET state='failed',failure_code='verification_denied'
                        WHERE login_id=? AND state='callback_claimed'""",
                    (row["login_id"],),
                )
            raise AuthenticationError("owner identity denied") from exc

        session_token = _browser_secret()
        csrf_token = _browser_secret()
        session_hash = _secret_hash(session_token)
        expires_at = now + self.session_ttl_seconds
        binding_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:approval-owner:{owner.domain_id}:{verified.identity.issuer}:"
                f"{verified.identity.subject}",
            )
        )
        binding_mismatch = False
        with self.store.transaction() as connection:
            current = connection.execute(
                """SELECT * FROM approval_owner_bindings
                    WHERE domain_id=? AND approver_principal_id=?""",
                (owner.domain_id, owner.principal_id),
            ).fetchone()
            binding_mismatch = current is not None and (
                current["status"] != "active"
                or current["oidc_issuer"] != verified.identity.issuer
                or current["oidc_subject"] != verified.identity.subject
                or current["verified_email"] != verified.identity.verified_email
            )
            if binding_mismatch:
                connection.execute(
                    """UPDATE approval_oidc_login_transactions
                          SET state='failed',failure_code='owner_binding_mismatch'
                        WHERE login_id=? AND state='callback_claimed'""",
                    (row["login_id"],),
                )
            else:
                if current is None:
                    connection.execute(
                        """INSERT INTO approval_owner_bindings(
                               binding_id,domain_id,approver_principal_id,oidc_issuer,oidc_subject,
                               verified_email,pin_source,status,pinned_at
                           ) VALUES(?,?,?,?,?,?,?,'active',?)""",
                        (
                            binding_id,
                            owner.domain_id,
                            owner.principal_id,
                            verified.identity.issuer,
                            verified.identity.subject,
                            verified.identity.verified_email,
                            pin_source,
                            now,
                        ),
                    )
                else:
                    binding_id = str(current["binding_id"])
                connection.execute(
                    """INSERT INTO approval_registration_budgets(
                           owner_binding_id,failed_attempts_total,challenge_rotations,updated_at
                       ) VALUES(?,0,0,?) ON CONFLICT(owner_binding_id) DO NOTHING""",
                    (binding_id, now),
                )
                previous = connection.execute(
                    """SELECT session_hash FROM approval_browser_sessions
                        WHERE owner_binding_id=? AND revoked_at IS NULL
                        ORDER BY authenticated_at DESC LIMIT 1""",
                    (binding_id,),
                ).fetchone()
                connection.execute(
                    """UPDATE approval_browser_sessions
                          SET revoked_at=?,revocation_reason='session_rotated'
                        WHERE owner_binding_id=? AND revoked_at IS NULL""",
                    (now, binding_id),
                )
                connection.execute(
                    """INSERT INTO approval_browser_sessions(
                           session_hash,owner_binding_id,csrf_secret_encrypted,
                           rp_id,public_origin,verifier_id,rotated_from_hash,
                           created_at,authenticated_at,expires_at
                       ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                    (
                        session_hash,
                        binding_id,
                        self.cipher.encrypt_json(
                            {"csrf_token": csrf_token},
                            purpose=f"approval-owner-csrf:{session_hash}",
                        ),
                        self.config.rp_id,
                        self.config.public_origin,
                        self.config.verifier_id,
                        str(previous["session_hash"]) if previous is not None else None,
                        now,
                        now,
                        expires_at,
                    ),
                )
                changed = connection.execute(
                    """UPDATE approval_oidc_login_transactions
                          SET state='callback_consumed',completed_session_hash=?,
                              callback_consumed_at=?,owner_binding_id=?
                        WHERE login_id=? AND state='callback_claimed'""",
                    (session_hash, now, binding_id, row["login_id"]),
                ).rowcount
                if changed != 1:
                    raise AuthenticationError("owner session denied")
                self._audit(
                    connection,
                    action="owner.login.completed",
                    owner=owner,
                    request_id=row["login_id"],
                    occurred_at=now,
                    outcome="completed",
                    detail="oidc_pkce_session_rotated",
                )
        if binding_mismatch:
            raise AuthenticationError("owner identity denied")
        return OwnerAuthenticatedSession(session_token, csrf_token, expires_at)

    def session_status(self, session_token: str) -> OwnerSessionStatus:
        now = self.clock()
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            session, binding, csrf = self._require_session(
                connection,
                session_token=session_token,
                csrf_token=None,
                at=now,
            )
            registered = connection.execute(
                """SELECT 1 AS present FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
                (binding["approver_principal_id"], binding["domain_id"]),
            ).fetchone()
        return OwnerSessionStatus(
            True,
            csrf,
            int(session["expires_at"]),
            registered is not None,
        )

    def pending_approvals(self, *, session_token: str) -> list[dict[str, Any]]:
        service = self._require_approval_service()
        principal_id, domain_id = self._owner_for_session(
            session_token=session_token,
            csrf_token=None,
        )
        return service.actionable_requests_for_owner(
            principal_id=principal_id,
            domain_id=domain_id,
        )

    def begin_approval(
        self,
        *,
        session_token: str,
        csrf_token: str,
        request_id: str,
    ) -> dict[str, Any]:
        service = self._require_approval_service()
        principal_id, domain_id = self._owner_for_session(
            session_token=session_token,
            csrf_token=csrf_token,
        )
        matches = [
            item
            for item in service.actionable_requests_for_owner(
                principal_id=principal_id,
                domain_id=domain_id,
            )
            if item.get("request_id") == request_id
        ]
        if (
            len(matches) != 1
            or matches[0].get("state") != "pending"
            or matches[0].get("approval_purpose")
            not in {"identity.enrollment.approve", "authorization.bootstrap_plan.approve"}
        ):
            raise AuthenticationError("approval request denied")
        options = service.request_options_for_owner(
            request_id=request_id,
            principal_id=principal_id,
            domain_id=domain_id,
            owner_session_hash=_secret_hash(session_token),
        )
        return {
            "schema": "agentnet.approval.owner-request-options.v1",
            "request_id": options["request_id"],
            "expires_at": options["expires_at"],
            "challenge_expires_at": options["challenge_expires_at"],
            "summary": options["summary"],
            "publicKey": options["publicKey"],
        }

    def complete_approval(
        self,
        *,
        session_token: str,
        csrf_token: str,
        request_id: str,
        credential: Mapping[str, Any],
    ) -> dict[str, Any]:
        service = self._require_approval_service()
        principal_id, domain_id = self._owner_for_session(
            session_token=session_token,
            csrf_token=csrf_token,
        )
        result = service.approve_request_for_owner(
            request_id=request_id,
            principal_id=principal_id,
            domain_id=domain_id,
            credential=credential,
            owner_session_hash=_secret_hash(session_token),
        )
        if result.get("schema") == "agentnet.approval.possession-status.v1":
            delivery_status = result.get("delivery_status")
            if delivery_status not in {"waiting_agent", "retrieved"}:
                raise AuthenticationError("approval request denied")
            return {
                "schema": "agentnet.approval.owner-request-result.v2",
                "approved": True,
                "delivery_status": delivery_status,
                "expires_at": result["expires_at"],
            }
        code = result.get("claim_code")
        if result.get("schema") == "agentnet.approval.claim-code.v1" and isinstance(
            code, str
        ):
            return {
                "schema": "agentnet.approval.owner-request-result.v1",
                "approved": True,
                "claim_code": code,
                "claim_code_expires_at": result["expires_at"],
            }
        if result.get("schema") == "agentnet.approval.claim-code-status.v1":
            return {
                "schema": "agentnet.approval.owner-request-result.v1",
                "approved": True,
                "claim_code": None,
                "claim_code_expires_at": result["expires_at"],
            }
        raise AuthenticationError("approval request denied")

    def reject_approval(
        self,
        *,
        session_token: str,
        csrf_token: str,
        request_id: str,
    ) -> dict[str, Any]:
        service = self._require_approval_service()
        principal_id, domain_id = self._owner_for_session(
            session_token=session_token,
            csrf_token=csrf_token,
        )
        service.reject_request_for_owner(
            request_id=request_id,
            principal_id=principal_id,
            domain_id=domain_id,
            owner_session_hash=_secret_hash(session_token),
        )
        return {
            "schema": "agentnet.approval.owner-request-rejection.v1",
            "rejected": True,
        }

    def regenerate_approval_code(
        self,
        *,
        session_token: str,
        csrf_token: str,
        request_id: str,
    ) -> dict[str, Any]:
        service = self._require_approval_service()
        principal_id, domain_id = self._owner_for_session(
            session_token=session_token,
            csrf_token=csrf_token,
        )
        result = service.regenerate_claim_code(
            request_id=request_id,
            principal_id=principal_id,
            domain_id=domain_id,
            owner_session_hash=_secret_hash(session_token),
        )
        code = result.get("claim_code")
        if not isinstance(code, str):
            raise AuthenticationError("approval request denied")
        return {
            "schema": "agentnet.approval.owner-request-result.v1",
            "approved": True,
            "claim_code": code,
            "claim_code_expires_at": result["expires_at"],
        }

    def begin_registration(
        self,
        *,
        session_token: str,
        csrf_token: str,
    ) -> OwnerRegistrationCeremony:
        now = self.clock()
        session_hash = _secret_hash(session_token)
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            session, binding, _csrf = self._require_session(
                connection,
                session_token=session_token,
                csrf_token=csrf_token,
                at=now,
            )
            active = connection.execute(
                """SELECT ceremony_id FROM approval_registration_ceremonies
                    WHERE owner_binding_id=? AND state='pending' AND expires_at>?""",
                (binding["binding_id"], now),
            ).fetchone()
            if active is not None:
                raise ConflictError("registration ceremony already active")
            credential = connection.execute(
                """SELECT credential_id_b64 FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
                (binding["approver_principal_id"], binding["domain_id"]),
            ).fetchone()
            if credential is not None:
                raise ConflictError("owner credential already registered")
            budget = connection.execute(
                "SELECT * FROM approval_registration_budgets WHERE owner_binding_id=?",
                (binding["binding_id"],),
            ).fetchone()
            if budget is None or int(budget["failed_attempts_total"]) >= _MAX_REGISTRATION_FAILURES:
                raise AuthenticationError("registration denied")
            if int(budget["challenge_rotations"]) >= _MAX_REGISTRATION_ROTATIONS:
                raise AuthenticationError("registration denied")
            ceremony_id = str(uuid4())
            challenge = secrets.token_bytes(32)
            challenge_expires_at = min(
                int(session["expires_at"]),
                now + int(self.config.challenge_ttl_seconds),
            )
            connection.execute(
                """INSERT INTO approval_registration_ceremonies(
                       ceremony_id,owner_binding_id,session_hash,challenge_encrypted,
                       challenge_hash,state,created_at,expires_at
                   ) VALUES(?,?,?,?,?,'pending',?,?)""",
                (
                    ceremony_id,
                    binding["binding_id"],
                    session_hash,
                    self.cipher.encrypt_json(
                        {"challenge_b64": b64url_encode(challenge)},
                        purpose=f"approval-owner-registration:{ceremony_id}",
                    ),
                    hashlib.sha256(challenge).hexdigest(),
                    now,
                    challenge_expires_at,
                ),
            )
            connection.execute(
                """UPDATE approval_registration_budgets
                      SET challenge_rotations=challenge_rotations+1,updated_at=?
                    WHERE owner_binding_id=?""",
                (now, binding["binding_id"]),
            )
            existing = connection.execute(
                """SELECT credential_id_b64 FROM approval_webauthn_credentials
                    WHERE approver_principal_id=? AND domain_id=?""",
                (binding["approver_principal_id"], binding["domain_id"]),
            ).fetchall()
            options = generate_registration_options(
                rp_id=self.config.rp_id,
                rp_name=self.config.rp_name,
                user_name=binding["approver_principal_id"],
                user_id=_user_handle(
                    self.config,
                    binding["approver_principal_id"],
                    binding["domain_id"],
                ),
                user_display_name=binding["approver_principal_id"],
                challenge=challenge,
                timeout=int(self.config.challenge_ttl_seconds) * 1000,
                authenticator_selection=AuthenticatorSelectionCriteria(
                    resident_key=ResidentKeyRequirement.PREFERRED,
                    user_verification=UserVerificationRequirement.REQUIRED,
                ),
                exclude_credentials=[
                    PublicKeyCredentialDescriptor(id=b64url_decode(item["credential_id_b64"]))
                    for item in existing
                ],
            )
        return OwnerRegistrationCeremony(
            ceremony_id,
            challenge_expires_at,
            options_to_json_dict(options),
        )

    def complete_registration(
        self,
        *,
        session_token: str,
        csrf_token: str,
        ceremony_id: str,
        credential: Mapping[str, Any],
    ) -> dict[str, Any]:
        now = self.clock()
        failure: Exception | None = None
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            session, binding, _csrf = self._require_session(
                connection,
                session_token=session_token,
                csrf_token=csrf_token,
                at=now,
            )
            ceremony = connection.execute(
                """SELECT * FROM approval_registration_ceremonies
                    WHERE ceremony_id=? AND owner_binding_id=? AND session_hash=?""",
                (ceremony_id, binding["binding_id"], session["session_hash"]),
            ).fetchone()
            if (
                ceremony is None
                or ceremony["state"] != "pending"
                or now >= int(ceremony["expires_at"])
            ):
                raise AuthenticationError("registration denied")
            try:
                protected = self.cipher.decrypt_json(
                    ceremony["challenge_encrypted"],
                    purpose=f"approval-owner-registration:{ceremony_id}",
                )
                verified = verify_registration_response(
                    credential=dict(credential),
                    expected_challenge=b64url_decode(protected["challenge_b64"]),
                    expected_rp_id=self.config.rp_id,
                    expected_origin=self.config.public_origin,
                    require_user_verification=True,
                )
                credential_id = b64url_encode(verified.credential_id)
                connection.execute(
                    """INSERT INTO approval_webauthn_credentials(
                           credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                           credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
                       ) VALUES(?,?,?,?,?,?,?,?,'active',?)""",
                    (
                        credential_id,
                        binding["approver_principal_id"],
                        binding["domain_id"],
                        b64url_encode(
                            _user_handle(
                                self.config,
                                binding["approver_principal_id"],
                                binding["domain_id"],
                            )
                        ),
                        b64url_encode(verified.credential_public_key),
                        verified.sign_count,
                        str(verified.credential_device_type.value),
                        int(verified.credential_backed_up),
                        now,
                    ),
                )
            except Exception as exc:
                failure = exc
                connection.execute(
                    """UPDATE approval_registration_ceremonies
                          SET state='failed',completed_at=?,failed_attempts=failed_attempts+1
                        WHERE ceremony_id=? AND state='pending'""",
                    (now, ceremony_id),
                )
                connection.execute(
                    """UPDATE approval_registration_budgets
                          SET failed_attempts_total=MIN(?,failed_attempts_total+1),updated_at=?
                        WHERE owner_binding_id=?""",
                    (_MAX_REGISTRATION_FAILURES, now, binding["binding_id"]),
                )
                self._audit(
                    connection,
                    action="owner.registration.denied",
                    owner=binding,
                    request_id=ceremony_id,
                    occurred_at=now,
                    outcome="denied",
                    detail="webauthn_verification_failed",
                )
            if failure is None:
                connection.execute(
                    """UPDATE approval_registration_ceremonies
                          SET state='verified',completed_at=?
                        WHERE ceremony_id=? AND state='pending'""",
                    (now, ceremony_id),
                )
                self._audit(
                    connection,
                    action="owner.registration.completed",
                    owner=binding,
                    request_id=ceremony_id,
                    occurred_at=now,
                    outcome="completed",
                    detail="webauthn_uv",
                )
        if failure is not None:
            raise AuthenticationError("registration denied") from failure
        return {
            "schema": "agentnet.approval.owner-registration-result.v1",
            "registered": True,
        }

    def cancel_registration(
        self,
        *,
        session_token: str,
        csrf_token: str,
        ceremony_id: str,
    ) -> None:
        now = self.clock()
        with self.store.transaction() as connection:
            session, binding, _csrf = self._require_session(
                connection,
                session_token=session_token,
                csrf_token=csrf_token,
                at=now,
            )
            changed = connection.execute(
                """UPDATE approval_registration_ceremonies
                      SET state='canceled',completed_at=?
                    WHERE ceremony_id=? AND owner_binding_id=? AND session_hash=?
                      AND state='pending'""",
                (now, ceremony_id, binding["binding_id"], session["session_hash"]),
            ).rowcount
            if changed != 1:
                raise AuthenticationError("registration denied")

    def _require_approval_service(self) -> Any:
        if self.approval_service is None:
            raise AuthenticationError("approval request denied")
        return self.approval_service

    def _owner_for_session(
        self,
        *,
        session_token: str,
        csrf_token: str | None,
    ) -> tuple[str, str]:
        now = self.clock()
        self._commit_expirations(at=now)
        with self.store.transaction() as connection:
            _session, binding, _csrf = self._require_session(
                connection,
                session_token=session_token,
                csrf_token=csrf_token,
                at=now,
            )
            return str(binding["approver_principal_id"]), str(binding["domain_id"])

    def _resolve_owner(self, identity: VerifiedOIDCIdentity) -> tuple[Any, str]:
        matches: list[tuple[Any, str]] = []
        for owner in self.config.approvers:
            if owner.oidc_issuer != identity.issuer:
                continue
            if owner.oidc_subject is not None and owner.oidc_subject == identity.subject:
                matches.append((owner, "exact_subject"))
            elif (
                owner.oidc_subject is None
                and owner.verified_email_alias == identity.verified_email
            ):
                matches.append((owner, "verified_alias"))
        if len(matches) != 1:
            raise AuthenticationError("owner identity denied")
        return matches[0]

    def _require_session(
        self,
        connection: sqlite3.Connection,
        *,
        session_token: str,
        csrf_token: str | None,
        at: int,
    ) -> tuple[sqlite3.Row, sqlite3.Row, str]:
        session_hash = _secret_hash(session_token)
        session = connection.execute(
            "SELECT * FROM approval_browser_sessions WHERE session_hash=?",
            (session_hash,),
        ).fetchone()
        if (
            session is None
            or session["revoked_at"] is not None
            or at >= int(session["expires_at"])
            or session["rp_id"] != self.config.rp_id
            or session["public_origin"] != self.config.public_origin
            or session["verifier_id"] != self.config.verifier_id
        ):
            raise AuthenticationError("owner session denied")
        binding = connection.execute(
            "SELECT * FROM approval_owner_bindings WHERE binding_id=?",
            (session["owner_binding_id"],),
        ).fetchone()
        if binding is None or binding["status"] != "active":
            raise AuthenticationError("owner session denied")
        protected = self.cipher.decrypt_json(
            session["csrf_secret_encrypted"],
            purpose=f"approval-owner-csrf:{session_hash}",
        )
        expected_csrf = protected.get("csrf_token") if isinstance(protected, dict) else None
        if not isinstance(expected_csrf, str):
            raise AuthenticationError("owner session denied")
        if csrf_token is not None and not secrets.compare_digest(expected_csrf, csrf_token):
            raise AuthenticationError("owner session denied")
        return session, binding, expected_csrf

    def _commit_expirations(self, *, at: int) -> None:
        with self.store.transaction() as connection:
            self._expire(connection, at=at)

    def _decrypt_oidc_login(self, row: sqlite3.Row) -> dict[str, object]:
        try:
            protected = self.cipher.decrypt_json(
                row["code_verifier_encrypted"],
                purpose=f"approval-owner-oidc:{row['login_id']}",
            )
        except Exception as exc:
            raise AuthenticationError("owner session denied") from exc
        if not isinstance(protected, dict):
            raise AuthenticationError("owner session denied")
        return protected

    def _expire(self, connection: sqlite3.Connection, *, at: int) -> None:
        connection.execute(
            """UPDATE approval_oidc_login_transactions
                  SET state='expired',failure_code='expired'
                WHERE state IN ('pending','callback_claimed') AND expires_at<=?""",
            (at,),
        )
        connection.execute(
            """UPDATE approval_registration_ceremonies
                  SET state='expired',completed_at=?
                WHERE state='pending' AND expires_at<=?""",
            (at, at),
        )
        connection.execute(
            """UPDATE approval_browser_sessions
                  SET revoked_at=?,revocation_reason='expired'
                WHERE revoked_at IS NULL AND expires_at<=?""",
            (at, at),
        )

    @staticmethod
    def _audit(
        connection: sqlite3.Connection,
        *,
        action: str,
        owner: Any,
        request_id: str,
        occurred_at: int,
        outcome: str,
        detail: str,
    ) -> None:
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES(?,?,?,?,NULL,NULL,?,?,?)""",
            (
                action,
                request_id,
                owner["approver_principal_id"]
                if isinstance(owner, sqlite3.Row)
                else owner.principal_id,
                owner["domain_id"] if isinstance(owner, sqlite3.Row) else owner.domain_id,
                occurred_at,
                outcome,
                detail,
            ),
        )


__all__ = [
    "ALLOWED_OIDC_LOGIN_TRANSITIONS",
    "ALLOWED_REGISTRATION_TRANSITIONS",
    "OwnerAuthenticatedSession",
    "OwnerOIDCCallbackQuery",
    "OwnerOIDCStart",
    "OwnerOIDCStartRequest",
    "OwnerPreauthSession",
    "OwnerRegistrationBeginRequest",
    "OwnerRegistrationCeremony",
    "OwnerRegistrationCompleteRequest",
    "OwnerSessionService",
    "OwnerSessionStatus",
    "OWNER_CSRF_COOKIE_NAME",
    "OWNER_PREAUTH_COOKIE_NAME",
    "OWNER_SESSION_COOKIE_HTTP_ONLY",
    "OWNER_SESSION_COOKIE_NAME",
    "OWNER_SESSION_COOKIE_SAME_SITE",
    "OWNER_SESSION_COOKIE_SECURE",
    "STABLE_APPROVAL_PATH",
    "require_owner_session_transition",
]
