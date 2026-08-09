"""Bounded recovery from the v0.1.50 placeholder Approval owner."""

from __future__ import annotations

import hashlib
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.approval.store import ApprovalStore, approval_user_handle
from agentnet.errors import GateBlocked
from agentnet.security.signatures import b64url_encode, canonical_json


_RECOVERY_ID = r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
_MAX_OWNER_APPROVAL_AGE_SECONDS = 31 * 24 * 60 * 60


class CanonicalOwnerAdoptionRequest(BaseModel):
    """Exact owner-approved placeholder-to-canonical adoption input."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-adoption.v1"] = Field(alias="schema")
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    domain_id: str = Field(min_length=1, max_length=256)
    source_principal_id: str = Field(min_length=1, max_length=256)
    target_principal_id: str = Field(min_length=1, max_length=256)
    oidc_issuer: str = Field(min_length=1, max_length=512)
    oidc_subject: str = Field(min_length=1, max_length=512)
    verified_email: str = Field(min_length=3, max_length=320)
    verifier_id: str = Field(min_length=1, max_length=128)
    approved_at: int = Field(ge=0)

    @model_validator(mode="after")
    def _different_principals(self) -> "CanonicalOwnerAdoptionRequest":
        if self.source_principal_id == self.target_principal_id:
            raise ValueError("source and target principals must differ")
        return self


class CanonicalOwnerAdoptionResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-adoption-result.v1"] = Field(alias="schema")
    status: Literal["adopted", "already_exact"]
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    migrated_active_credentials: int = Field(ge=0)
    revoked_browser_sessions: int = Field(ge=0)
    canceled_registration_ceremonies: int = Field(ge=0)


def _request_digest(request: CanonicalOwnerAdoptionRequest) -> str:
    return hashlib.sha256(
        canonical_json(request.model_dump(by_alias=True, mode="json"))
    ).hexdigest()


def _result(
    request: CanonicalOwnerAdoptionRequest,
    *,
    status: Literal["adopted", "already_exact"],
    migrated_active_credentials: int,
    revoked_browser_sessions: int,
    canceled_registration_ceremonies: int,
) -> dict[str, Any]:
    return CanonicalOwnerAdoptionResult(
        schema="agentnet.canonical-owner-adoption-result.v1",
        status=status,
        recovery_id=request.recovery_id,
        migrated_active_credentials=migrated_active_credentials,
        revoked_browser_sessions=revoked_browser_sessions,
        canceled_registration_ceremonies=canceled_registration_ceremonies,
    ).model_dump(by_alias=True)


def _matches_binding(row: Any, request: CanonicalOwnerAdoptionRequest, principal_id: str) -> bool:
    return (
        row["domain_id"] == request.domain_id
        and row["approver_principal_id"] == principal_id
        and row["oidc_issuer"] == request.oidc_issuer
        and row["oidc_subject"] == request.oidc_subject
        and row["verified_email"] == request.verified_email
        and row["status"] == "active"
        and row["revoked_at"] is None
    )


def _active_credentials(connection: Any, principal_id: str, domain_id: str) -> list[Any]:
    return list(
        connection.execute(
            """SELECT * FROM approval_webauthn_credentials
                 WHERE approver_principal_id=? AND domain_id=? AND status='active'
                 ORDER BY credential_id_b64""",
            (principal_id, domain_id),
        ).fetchall()
    )


def _require_exact_target(
    connection: Any,
    *,
    request: CanonicalOwnerAdoptionRequest,
    binding: Any,
    request_digest: str,
) -> None:
    if not _matches_binding(binding, request, request.target_principal_id):
        raise GateBlocked("canonical_owner_recovery", "target authority already exists")
    if connection.execute(
        """SELECT 1 FROM approval_owner_bindings
             WHERE domain_id=? AND approver_principal_id=? AND status='active'""",
        (request.domain_id, request.source_principal_id),
    ).fetchone() is not None:
        raise GateBlocked("canonical_owner_recovery", "source authority remains active")
    if _active_credentials(connection, request.source_principal_id, request.domain_id):
        raise GateBlocked("canonical_owner_recovery", "source authority remains active")
    credentials = _active_credentials(connection, request.target_principal_id, request.domain_id)
    expected_handle = b64url_encode(
        approval_user_handle(
            verifier_id=request.verifier_id,
            principal_id=request.target_principal_id,
            domain_id=request.domain_id,
        )
    )
    if not credentials or any(row["user_handle_b64"] != expected_handle for row in credentials):
        raise GateBlocked("canonical_owner_recovery", "target authority is incomplete")
    audits = connection.execute(
        """SELECT COUNT(*) FROM approval_audit
             WHERE action='owner.canonical_adoption' AND approver_principal_id=?
               AND domain_id=? AND approval_purpose='owner.canonical_adoption'
               AND transaction_digest=? AND outcome='adopted'
               AND detail_code='canonical_owner_adopted'""",
        (request.target_principal_id, request.domain_id, request_digest),
    ).fetchone()
    if audits is None or int(audits[0]) != 1:
        raise GateBlocked("canonical_owner_recovery", "target recovery evidence is incomplete")


def adopt_canonical_approval_owner(
    store: ApprovalStore,
    *,
    request: CanonicalOwnerAdoptionRequest,
    now: int,
) -> dict[str, Any]:
    """Atomically move only live Approval authority to the enrolled principal."""

    if (
        isinstance(now, bool)
        or not isinstance(now, int)
        or request.approved_at > now
        or now - request.approved_at > _MAX_OWNER_APPROVAL_AGE_SECONDS
    ):
        raise GateBlocked("canonical_owner_recovery", "owner approval is not current")

    digest = _request_digest(request)
    with store.transaction() as connection:
        active_bindings = list(
            connection.execute(
                """SELECT * FROM approval_owner_bindings
                     WHERE domain_id=? AND status='active'
                     ORDER BY binding_id""",
                (request.domain_id,),
            ).fetchall()
        )
        if len(active_bindings) != 1:
            raise GateBlocked("canonical_owner_recovery", "approval owner state is ambiguous")
        binding = active_bindings[0]

        if binding["approver_principal_id"] == request.target_principal_id:
            _require_exact_target(
                connection,
                request=request,
                binding=binding,
                request_digest=digest,
            )
            return _result(
                request,
                status="already_exact",
                migrated_active_credentials=0,
                revoked_browser_sessions=0,
                canceled_registration_ceremonies=0,
            )

        if not _matches_binding(binding, request, request.source_principal_id):
            raise GateBlocked("canonical_owner_recovery", "source state does not match")

        target_binding = connection.execute(
            """SELECT 1 FROM approval_owner_bindings
                 WHERE domain_id=? AND approver_principal_id=?""",
            (request.domain_id, request.target_principal_id),
        ).fetchone()
        target_credential = connection.execute(
            """SELECT 1 FROM approval_webauthn_credentials
                 WHERE domain_id=? AND approver_principal_id=?""",
            (request.domain_id, request.target_principal_id),
        ).fetchone()
        if target_binding is not None or target_credential is not None:
            raise GateBlocked("canonical_owner_recovery", "target authority already exists")

        pending_request = connection.execute(
            """SELECT 1 FROM approval_requests
                 WHERE approver_principal_id=? AND domain_id=? AND state='pending' LIMIT 1""",
            (request.source_principal_id, request.domain_id),
        ).fetchone()
        pending_registration = connection.execute(
            """SELECT 1 FROM approval_registration_sessions
                 WHERE approver_principal_id=? AND domain_id=?
                   AND consumed_at IS NULL AND expires_at>? LIMIT 1""",
            (request.source_principal_id, request.domain_id, now),
        ).fetchone()
        pending_oidc = connection.execute(
            """SELECT 1 FROM approval_oidc_login_transactions
                 WHERE state IN ('pending','callback_claimed') LIMIT 1"""
        ).fetchone()
        if pending_request is not None or pending_registration is not None or pending_oidc is not None:
            raise GateBlocked("canonical_owner_recovery", "nonterminal approval state exists")

        credentials = _active_credentials(
            connection, request.source_principal_id, request.domain_id
        )
        source_handle = b64url_encode(
            approval_user_handle(
                verifier_id=request.verifier_id,
                principal_id=request.source_principal_id,
                domain_id=request.domain_id,
            )
        )
        if not credentials or any(row["user_handle_b64"] != source_handle for row in credentials):
            raise GateBlocked("canonical_owner_recovery", "source credential state does not match")

        ceremonies = connection.execute(
            """UPDATE approval_registration_ceremonies
                  SET state='canceled'
                WHERE owner_binding_id=? AND state='pending'""",
            (binding["binding_id"],),
        )
        sessions = connection.execute(
            """UPDATE approval_browser_sessions
                  SET revoked_at=?,revocation_reason='canonical_owner_adoption'
                WHERE owner_binding_id=? AND revoked_at IS NULL AND expires_at>?""",
            (now, binding["binding_id"], now),
        )
        connection.execute(
            """UPDATE approval_owner_bindings
                  SET approver_principal_id=?
                WHERE binding_id=? AND approver_principal_id=? AND status='active'""",
            (
                request.target_principal_id,
                binding["binding_id"],
                request.source_principal_id,
            ),
        )
        target_handle = b64url_encode(
            approval_user_handle(
                verifier_id=request.verifier_id,
                principal_id=request.target_principal_id,
                domain_id=request.domain_id,
            )
        )
        migrated = connection.execute(
            """UPDATE approval_webauthn_credentials
                  SET approver_principal_id=?,user_handle_b64=?
                WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
            (
                request.target_principal_id,
                target_handle,
                request.source_principal_id,
                request.domain_id,
            ),
        )
        if migrated.rowcount != len(credentials):
            raise GateBlocked("canonical_owner_recovery", "approval owner adoption raced")
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES('owner.canonical_adoption',NULL,?,?,
                        'owner.canonical_adoption',?,?,'adopted','canonical_owner_adopted')""",
            (request.target_principal_id, request.domain_id, digest, now),
        )
        _require_exact_target(
            connection,
            request=request,
            binding=connection.execute(
                "SELECT * FROM approval_owner_bindings WHERE binding_id=?",
                (binding["binding_id"],),
            ).fetchone(),
            request_digest=digest,
        )
        foreign_keys = list(connection.execute("PRAGMA foreign_key_check").fetchall())
        if foreign_keys:
            raise GateBlocked("canonical_owner_recovery", "approval owner adoption is inconsistent")

        return _result(
            request,
            status="adopted",
            migrated_active_credentials=len(credentials),
            revoked_browser_sessions=int(sessions.rowcount),
            canceled_registration_ceremonies=int(ceremonies.rowcount),
        )


__all__ = [
    "CanonicalOwnerAdoptionRequest",
    "CanonicalOwnerAdoptionResult",
    "adopt_canonical_approval_owner",
]
