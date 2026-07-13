"""Sponsor-authorized, one-use invitations for internal human harnesses.

An invitation is a zero-authority enrollment proposal.  The service derives the
sponsor exclusively from a current :class:`IssuanceAuthority`, calls its
configured OIDC verifier instead of accepting identity claims, and atomically
creates the principal/harness/credential binding while consuming the exact
canonical invitation.  Requested capabilities are descriptive/attenuating
harness facts; this service never creates an entitlement or task grant.
"""

from __future__ import annotations

import hashlib
import json
import re
import secrets
import sqlite3
from collections.abc import Mapping
from datetime import UTC, datetime
from typing import Any, Literal, Protocol
from uuid import NAMESPACE_URL, uuid4, uuid5

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_current_authority_decision,
    require_signed_authority_command,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ExtensionError,
    GateBlocked,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import (
    load_credential_binding_from_connection,
    public_key_thumbprint,
)
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.security.signatures import (
    canonical_digest,
    canonical_json,
    verify_signature,
)
from agentnet.storage.backend import StoreBackend


INTERNAL_INVITATION_ISSUE_ACTION = "identity.internal_invitation.issue"
INTERNAL_INVITATION_REVOKE_ACTION = "identity.internal_invitation.revoke"
INTERNAL_INVITATION_POP_PURPOSE = "agentnet.internal-invitation.acceptance-pop.v1"
_EMAIL = re.compile(r"^[^\s@]+@[^\s@]+\.[^\s@]+$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_SAFE_TOKEN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$")


def _is_integrity_constraint_error(exc: Exception) -> bool:
    """Recognize the backend adapter's normalized constraint failure.

    ``PostgreSQLConnectionAdapter`` deliberately converts psycopg integrity
    exceptions to ``sqlite3.IntegrityError`` so services have one contract.
    The class-name fallback also keeps this boundary safe for a future backend
    that exposes its native DB-API ``IntegrityError``/``UniqueViolation``.
    """

    return isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ in {
        "IntegrityError",
        "UniqueViolation",
    }


def _epoch_seconds(value: datetime) -> int:
    if value.tzinfo is None:
        raise ValidationError("security timestamps must be timezone-aware")
    return int(value.timestamp())


def _require_safe_token(value: str, *, label: str, maximum: int) -> str:
    if not value or len(value) > maximum or not _SAFE_TOKEN.fullmatch(value):
        raise ValueError(f"{label} is outside the invitation profile")
    return value


class InternalInvitationOIDCVerifier(Protocol):
    """Configured trust boundary that independently verifies candidate OIDC.

    Implementations are expected to bind their authorization-code state/nonce
    to ``canonical_invitation``.  Returning an :class:`OIDCVerificationResult`
    is the only way candidate identity enters this service; request payload
    issuer, subject, and email fields are never treated as verified facts.
    """

    verifier_id: str

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation: bytes,
        evidence: Mapping[str, Any],
        expected_issuer: str,
        when: datetime,
    ) -> OIDCVerificationResult: ...


class InternalInvitationRequest(BaseModel):
    """Strict sponsor request for one candidate human/harness binding."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    invitation_id: str = Field(default_factory=lambda: str(uuid4()), min_length=16, max_length=128)
    domain_id: str = Field(min_length=1, max_length=256)
    invited_oidc_issuer: str = Field(min_length=1, max_length=512)
    invited_oidc_subject: str = Field(min_length=1, max_length=512)
    invited_verified_email: str = Field(min_length=3, max_length=320)
    candidate_harness_id: str = Field(min_length=1, max_length=256)
    candidate_harness_kind: str = Field(min_length=1, max_length=64)
    candidate_harness_display_name: str = Field(min_length=1, max_length=128)
    candidate_binding_assurance: Literal["lab", "os_bound", "hardware_bound"]
    candidate_key_id: str = Field(min_length=16, max_length=256)
    candidate_public_key_pem: str = Field(min_length=128, max_length=16_384)
    requested_capabilities: tuple[str, ...] = Field(default=(), max_length=64)
    expires_at: datetime
    predecessor_invitation_id: str | None = Field(default=None, min_length=16, max_length=128)
    reason: str = Field(min_length=1, max_length=512)

    @field_validator("domain_id", "candidate_harness_id", "candidate_harness_kind")
    @classmethod
    def safe_identifiers(cls, value: str, info: Any) -> str:
        return _require_safe_token(value, label=info.field_name, maximum=256)

    @field_validator("invited_oidc_issuer")
    @classmethod
    def canonical_issuer(cls, value: str) -> str:
        if not value.startswith("https://") or value.endswith("/") or any(ord(char) < 0x20 for char in value):
            raise ValueError("invited OIDC issuer is not canonical HTTPS")
        return value

    @field_validator("invited_oidc_subject")
    @classmethod
    def valid_subject(cls, value: str) -> str:
        if any(ord(char) < 0x20 for char in value):
            raise ValueError("invited OIDC subject contains control characters")
        return value

    @field_validator("invited_verified_email")
    @classmethod
    def canonical_email(cls, value: str) -> str:
        normalized = value.strip().casefold()
        if normalized != value or not _EMAIL.fullmatch(normalized):
            raise ValueError("invited verified email is not canonical")
        return normalized

    @field_validator("candidate_harness_display_name", "reason")
    @classmethod
    def nonblank_text(cls, value: str) -> str:
        if value != value.strip() or any(ord(char) < 0x20 for char in value) or not value:
            raise ValueError("invitation text must be nonblank canonical text")
        return value

    @field_validator("requested_capabilities")
    @classmethod
    def canonical_capabilities(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if tuple(sorted(set(value))) != value:
            raise ValueError("requested capabilities must be unique and canonically sorted")
        for capability in value:
            _require_safe_token(capability, label="requested capability", maximum=128)
        return value

    @model_validator(mode="after")
    def coherent_request(self) -> "InternalInvitationRequest":
        if self.expires_at.tzinfo is None:
            raise ValueError("invitation expiry must be timezone-aware")
        if public_key_thumbprint(self.candidate_public_key_pem) != self.candidate_key_id:
            raise ValueError("candidate key identifier does not match its public key")
        if self.predecessor_invitation_id == self.invitation_id:
            raise ValueError("an invitation cannot reissue itself")
        return self


class InternalInvitationTransaction(BaseModel):
    """The exact canonical bytes authorized by the sponsor and candidate."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    purpose: Literal["internal_human_harness_invitation"] = "internal_human_harness_invitation"
    invitation_id: str = Field(min_length=16, max_length=128)
    domain_id: str = Field(min_length=1, max_length=256)
    sponsor_authority_kind: Literal["human", "guest"]
    sponsor_authority_id: str = Field(min_length=1, max_length=256)
    sponsor_harness_id: str = Field(min_length=1, max_length=256)
    sponsor_credential_id: str = Field(min_length=1, max_length=256)
    sponsor_credential_epoch: int = Field(ge=1)
    invited_oidc_issuer: str = Field(min_length=1, max_length=512)
    invited_oidc_subject: str = Field(min_length=1, max_length=512)
    invited_verified_email: str = Field(min_length=3, max_length=320)
    candidate_harness_id: str = Field(min_length=1, max_length=256)
    candidate_harness_kind: str = Field(min_length=1, max_length=64)
    candidate_harness_display_name: str = Field(min_length=1, max_length=128)
    candidate_binding_assurance: Literal["lab", "os_bound", "hardware_bound"]
    candidate_key_id: str = Field(min_length=16, max_length=256)
    candidate_public_key_pem: str = Field(min_length=128, max_length=16_384)
    requested_capabilities: tuple[str, ...] = Field(max_length=64)
    policy_revision: int = Field(ge=1)
    domain_revocation_epoch: int = Field(ge=1)
    max_uses: Literal[1] = 1
    expires_at: datetime
    predecessor_invitation_id: str | None = Field(default=None, min_length=16, max_length=128)
    predecessor_invitation_digest: str | None = Field(default=None, pattern=r"^[0-9a-f]{64}$")
    predecessor_revision: int | None = Field(default=None, ge=1)
    reason: str = Field(min_length=1, max_length=512)

    @field_validator(
        "domain_id",
        "sponsor_authority_id",
        "sponsor_harness_id",
        "sponsor_credential_id",
        "candidate_harness_id",
        "candidate_harness_kind",
    )
    @classmethod
    def safe_identifiers(cls, value: str, info: Any) -> str:
        return _require_safe_token(value, label=info.field_name, maximum=256)

    @field_validator("invited_oidc_issuer")
    @classmethod
    def canonical_issuer(cls, value: str) -> str:
        if not value.startswith("https://") or value.endswith("/") or any(ord(char) < 0x20 for char in value):
            raise ValueError("invited OIDC issuer is not canonical HTTPS")
        return value

    @field_validator("invited_oidc_subject")
    @classmethod
    def valid_subject(cls, value: str) -> str:
        if any(ord(char) < 0x20 for char in value):
            raise ValueError("invited OIDC subject contains control characters")
        return value

    @field_validator("invited_verified_email")
    @classmethod
    def canonical_email(cls, value: str) -> str:
        if value != value.strip().casefold() or not _EMAIL.fullmatch(value):
            raise ValueError("invited verified email is not canonical")
        return value

    @field_validator("candidate_harness_display_name", "reason")
    @classmethod
    def nonblank_text(cls, value: str) -> str:
        if value != value.strip() or any(ord(char) < 0x20 for char in value) or not value:
            raise ValueError("invitation text must be nonblank canonical text")
        return value

    @field_validator("requested_capabilities")
    @classmethod
    def canonical_capabilities(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if tuple(sorted(set(value))) != value:
            raise ValueError("requested capabilities must be unique and canonically sorted")
        for capability in value:
            _require_safe_token(capability, label="requested capability", maximum=128)
        return value

    @model_validator(mode="after")
    def coherent_lineage(self) -> "InternalInvitationTransaction":
        lineage = (
            self.predecessor_invitation_id,
            self.predecessor_invitation_digest,
            self.predecessor_revision,
        )
        if any(value is None for value in lineage) != all(value is None for value in lineage):
            raise ValueError("invitation predecessor binding is incomplete")
        if self.expires_at.tzinfo is None:
            raise ValueError("invitation expiry must be timezone-aware")
        if public_key_thumbprint(self.candidate_public_key_pem) != self.candidate_key_id:
            raise ValueError("candidate key identifier does not match its public key")
        if self.predecessor_invitation_id == self.invitation_id:
            raise ValueError("an invitation cannot reissue itself")
        return self

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))


class InternalInvitationRecord(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    transaction: InternalInvitationTransaction
    invitation_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    state: Literal["active", "consumed", "revoked", "expired"]
    revision: int = Field(ge=1)
    use_count: Literal[0, 1]
    created_at: datetime
    updated_at: datetime
    consumed_at: datetime | None = None
    revoked_at: datetime | None = None
    accepted_principal_id: str | None = None
    accepted_harness_id: str | None = None


class InternalInvitationAcceptance(BaseModel):
    """Created identity binding; it deliberately contains no entitlement."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    invitation_id: str
    principal_id: str
    harness_id: str
    credential_id: str
    key_id: str
    credential_epoch: Literal[1] = 1
    credential_expires_at: datetime
    actor: VerifiedActor
    requested_capabilities: tuple[str, ...]
    positive_entitlements_issued: Literal[0] = 0


class InternalInvitationService:
    """Issue, consume, revoke, expire, and safely reissue internal invites."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        oidc_verifier: InternalInvitationOIDCVerifier,
        credential_ttl_seconds: int = 3_600,
        maximum_invitation_ttl_seconds: int = 604_800,
        failure_window_seconds: int = 300,
        maximum_failures_per_source: int = 5,
        lockout_seconds: int = 300,
        clock: Any | None = None,
    ) -> None:
        if not getattr(oidc_verifier, "verifier_id", ""):
            raise ValueError("internal invitation OIDC verifier must have an identifier")
        if credential_ttl_seconds < 60 or credential_ttl_seconds > 86_400:
            raise ValueError("invitation credential TTL must be between 60 seconds and 24 hours")
        if maximum_invitation_ttl_seconds < 60 or maximum_invitation_ttl_seconds > 2_592_000:
            raise ValueError("maximum invitation TTL must be between one minute and 30 days")
        if failure_window_seconds < 1 or failure_window_seconds > 3_600:
            raise ValueError("invitation failure window is outside the supported range")
        if maximum_failures_per_source < 1 or maximum_failures_per_source > 100:
            raise ValueError("invitation failure ceiling is outside the supported range")
        if lockout_seconds < 1 or lockout_seconds > 86_400:
            raise ValueError("invitation lockout is outside the supported range")
        self.store = store
        self.oidc_verifier = oidc_verifier
        self.credential_ttl_seconds = credential_ttl_seconds
        self.maximum_invitation_ttl_seconds = maximum_invitation_ttl_seconds
        self.failure_window_seconds = failure_window_seconds
        self.maximum_failures_per_source = maximum_failures_per_source
        self.lockout_seconds = lockout_seconds
        self.clock = clock or (lambda: int(datetime.now(UTC).timestamp()))

    @staticmethod
    def issuance_binding(request: InternalInvitationRequest) -> tuple[str, dict[str, str]]:
        return (
            f"internal-invitation:{request.invitation_id}",
            {
                "schema": "agentnet.internal-invitation.issue.v1",
                "request_digest": canonical_digest(request.model_dump(mode="json")),
            },
        )

    @staticmethod
    def revocation_binding(
        invitation_id: str,
        *,
        expected_revision: int,
        reason: str,
    ) -> tuple[str, dict[str, object]]:
        return (
            f"internal-invitation:{invitation_id}",
            {
                "schema": "agentnet.internal-invitation.revoke.v1",
                "invitation_id": invitation_id,
                "expected_revision": expected_revision,
                "reason": reason,
            },
        )

    @staticmethod
    def candidate_possession_fields(
        transaction: InternalInvitationTransaction,
        verification: OIDCVerificationResult,
    ) -> dict[str, object]:
        return {
            "schema": "agentnet.internal-invitation.acceptance-pop.v1",
            "purpose": "accept_internal_human_harness_invitation",
            "invitation_id": transaction.invitation_id,
            "invitation_digest": transaction.digest,
            "domain_id": transaction.domain_id,
            "candidate_harness_id": transaction.candidate_harness_id,
            "candidate_key_id": transaction.candidate_key_id,
            "oidc_identity": {
                "issuer": verification.identity.issuer,
                "subject": verification.identity.subject,
                "verified_email": verification.identity.verified_email,
            },
            "oidc_token_hash": verification.id_token_hash,
            "oidc_token_expires_at": verification.expires_at,
        }

    def issue(
        self,
        request: InternalInvitationRequest,
        *,
        authority: IssuanceAuthority | None,
        when: datetime | None = None,
    ) -> InternalInvitationRecord:
        # Pydantic's generic ``model_copy(update=...)`` does not re-run model
        # validators.  Re-parse at this trust boundary so a Python caller
        # cannot smuggle a substituted key or noncanonical scope around the
        # same strict checks used by HTTP JSON parsing.
        request = InternalInvitationRequest.model_validate(
            request.model_dump(mode="python")
        )
        when = when or datetime.fromtimestamp(int(self.clock()), UTC)
        now = _epoch_seconds(when)
        expires_at = _epoch_seconds(request.expires_at)
        if request.domain_id != (authority.actor.domain_id if authority is not None else None):
            raise AuthorizationError("internal invitation sponsor domain mismatch")
        if expires_at <= now or expires_at - now > self.maximum_invitation_ttl_seconds:
            raise ValidationError("internal invitation expiry is outside the configured bound")

        with self.store.transaction() as connection:
            resource, expected_request = self.issuance_binding(request)
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=INTERNAL_INVITATION_ISSUE_ACTION,
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if authority is None:  # narrowed by the authority verifier
                raise AuthorizationError("authenticated invitation sponsor is required")
            actor = authority.actor
            if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
                raise AuthorizationError("internal invitations require current positive human authority")
            if actor.harness_id is None or actor.credential_id is None or actor.positive_authority_id is None:
                raise AuthorizationError("internal invitation sponsor binding is incomplete")
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?", (request.domain_id,)
            ).fetchone()
            if domain is None or domain["status"] != "active":
                raise AuthorizationError("internal invitation domain is unavailable")
            binding = load_credential_binding_from_connection(connection, actor.credential_id)
            binding.require_active(now=now)
            if (
                binding.domain_id != request.domain_id
                or binding.harness_id != actor.harness_id
                or binding.credential_epoch != actor.credential_epoch
                or binding.key_id == request.candidate_key_id
            ):
                raise AuthorizationError("internal invitation sponsor binding is invalid")

            existing = connection.execute(
                "SELECT * FROM internal_invitations WHERE invitation_id=?",
                (request.invitation_id,),
            ).fetchone()
            if existing is not None:
                existing_record = self._from_row(existing)
                if not self._request_matches_transaction(
                    request,
                    existing_record.transaction,
                    sponsor_actor=actor,
                    policy_revision=policy_revision,
                    domain_revocation_epoch=int(domain["revocation_epoch"]),
                ):
                    raise ConflictError("invitation identifier already names different canonical bytes")
                return existing_record

            predecessor = self._require_safe_predecessor(
                connection,
                request=request,
                sponsor_actor=actor,
            )
            sponsor_kind: Literal["human", "guest"] = (
                "human" if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS else "guest"
            )
            transaction = InternalInvitationTransaction(
                invitation_id=request.invitation_id,
                domain_id=request.domain_id,
                sponsor_authority_kind=sponsor_kind,
                sponsor_authority_id=actor.positive_authority_id,
                sponsor_harness_id=actor.harness_id,
                sponsor_credential_id=actor.credential_id,
                sponsor_credential_epoch=actor.credential_epoch,
                invited_oidc_issuer=request.invited_oidc_issuer,
                invited_oidc_subject=request.invited_oidc_subject,
                invited_verified_email=request.invited_verified_email,
                candidate_harness_id=request.candidate_harness_id,
                candidate_harness_kind=request.candidate_harness_kind,
                candidate_harness_display_name=request.candidate_harness_display_name,
                candidate_binding_assurance=request.candidate_binding_assurance,
                candidate_key_id=request.candidate_key_id,
                candidate_public_key_pem=request.candidate_public_key_pem,
                requested_capabilities=request.requested_capabilities,
                policy_revision=policy_revision,
                domain_revocation_epoch=int(domain["revocation_epoch"]),
                expires_at=request.expires_at,
                predecessor_invitation_id=(
                    predecessor.transaction.invitation_id if predecessor is not None else None
                ),
                predecessor_invitation_digest=(
                    predecessor.invitation_digest if predecessor is not None else None
                ),
                predecessor_revision=predecessor.revision if predecessor is not None else None,
                reason=request.reason,
            )
            serialized = canonical_json(transaction.model_dump(mode="json")).decode("utf-8")
            digest = hashlib.sha256(serialized.encode("utf-8")).hexdigest()
            if connection.execute(
                "SELECT harness_id FROM harnesses WHERE harness_id=?", (request.candidate_harness_id,)
            ).fetchone() is not None:
                raise ConflictError("candidate harness identifier is already enrolled")
            if connection.execute(
                "SELECT credential_id FROM credentials WHERE key_id=? LIMIT 1", (request.candidate_key_id,)
            ).fetchone() is not None:
                raise ConflictError("candidate key is already enrolled")

            connection.execute(
                """
                INSERT INTO internal_invitations(
                    invitation_id,schema_version,domain_id,sponsor_authority_kind,
                    sponsor_authority_id,sponsor_harness_id,sponsor_credential_id,
                    sponsor_credential_epoch,invited_oidc_issuer,invited_oidc_subject,
                    invited_verified_email,candidate_harness_id,candidate_harness_kind,
                    candidate_key_id,candidate_public_key_pem,requested_capabilities_json,
                    policy_revision,domain_revocation_epoch,max_uses,use_count,state,revision,
                    canonical_invitation_json,invitation_digest,expires_at,created_at,updated_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,0,'active',1,?,?,?,?,?)
                """,
                (
                    request.invitation_id,
                    "1.0",
                    request.domain_id,
                    sponsor_kind,
                    actor.positive_authority_id,
                    actor.harness_id,
                    actor.credential_id,
                    actor.credential_epoch,
                    request.invited_oidc_issuer,
                    request.invited_oidc_subject,
                    request.invited_verified_email,
                    request.candidate_harness_id,
                    request.candidate_harness_kind,
                    request.candidate_key_id,
                    request.candidate_public_key_pem,
                    canonical_json(list(request.requested_capabilities)).decode("utf-8"),
                    policy_revision,
                    int(domain["revocation_epoch"]),
                    serialized,
                    digest,
                    expires_at,
                    now,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.issued",
                    "invitation_id": request.invitation_id,
                    "invitation_digest": digest,
                    "domain_id": request.domain_id,
                    "sponsor": actor.audit_view(),
                    "candidate_harness_id": request.candidate_harness_id,
                    "policy_revision": policy_revision,
                    "domain_revocation_epoch": int(domain["revocation_epoch"]),
                    "expires_at": expires_at,
                    "zero_authority_proposal": True,
                },
            )
            row = connection.execute(
                "SELECT * FROM internal_invitations WHERE invitation_id=?",
                (request.invitation_id,),
            ).fetchone()
            if row is None:  # pragma: no cover - same authoritative transaction
                raise RuntimeError("invitation insert was not observable")
            return self._from_row(row)

    def accept(
        self,
        *,
        invitation_id: str,
        canonical_invitation: bytes,
        oidc_evidence: Mapping[str, Any],
        candidate_possession_signature: str,
        source_fingerprint: str,
        when: datetime | None = None,
    ) -> InternalInvitationAcceptance:
        """Consume one invitation after verifier-derived OIDC and exact key PoP.

        ``source_fingerprint`` must be a privacy-minimized digest supplied by a
        trusted HTTP/transport adapter, never a request-body identity claim.
        """

        when = when or datetime.fromtimestamp(int(self.clock()), UTC)
        now = _epoch_seconds(when)
        if not _SHA256.fullmatch(source_fingerprint):
            raise ValidationError("invitation source fingerprint must be a SHA-256 digest")
        self.expire_due(when=when)
        try:
            transaction = self._parse_exact_transaction(canonical_invitation)
            if transaction.invitation_id != invitation_id:
                raise AuthenticationError("internal invitation binding mismatch")
            row = self.store.fetch_one(
                "SELECT * FROM internal_invitations WHERE invitation_id=?", (invitation_id,)
            )
            if row is None:
                raise AuthenticationError("internal invitation is unavailable")
            self._require_row_matches_bytes(row, transaction, canonical_invitation)
            self._require_not_locked(row, source_fingerprint=source_fingerprint, now=now)
            try:
                verification = self.oidc_verifier.verify_invitation_identity(
                    canonical_invitation=canonical_invitation,
                    evidence=oidc_evidence,
                    expected_issuer=transaction.invited_oidc_issuer,
                    when=when,
                )
            except ExtensionError:
                raise
            except Exception as exc:
                raise GateBlocked(
                    "oidc_verifier",
                    "internal invitation identity verifier failed closed",
                ) from exc
            self._validate_oidc_verification(transaction, verification, now=now)
            possession_fields = self.candidate_possession_fields(transaction, verification)
            verify_signature(
                transaction.candidate_public_key_pem,
                INTERNAL_INVITATION_POP_PURPOSE,
                possession_fields,
                candidate_possession_signature,
            )
            return self._consume(
                transaction=transaction,
                canonical_invitation=canonical_invitation,
                verification=verification,
                source_fingerprint=source_fingerprint,
                when=when,
            )
        except GateBlocked:
            raise
        except ExtensionError as exc:
            self._record_failed_attempt(
                invitation_id=invitation_id,
                source_fingerprint=source_fingerprint,
                now=now,
            )
            raise AuthenticationError("internal invitation is unavailable or invalid") from exc

    def _consume(
        self,
        *,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
        verification: OIDCVerificationResult,
        source_fingerprint: str,
        when: datetime,
    ) -> InternalInvitationAcceptance:
        now = _epoch_seconds(when)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM internal_invitations WHERE invitation_id=?",
                (transaction.invitation_id,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("internal invitation is unavailable")
            self._require_row_matches_bytes(row, transaction, canonical_invitation)
            self._require_not_locked_in_connection(
                connection,
                invitation_id=transaction.invitation_id,
                source_fingerprint=source_fingerprint,
                now=now,
            )
            if row["state"] != "active" or int(row["use_count"]) != 0 or int(row["revision"]) != 1:
                raise AuthenticationError("internal invitation is no longer active")
            if now >= int(row["expires_at"]):
                raise AuthenticationError("internal invitation is expired")
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?", (transaction.domain_id,)
            ).fetchone()
            if (
                domain is None
                or domain["status"] != "active"
                or int(domain["policy_revision"]) != transaction.policy_revision
                or int(domain["revocation_epoch"]) != transaction.domain_revocation_epoch
            ):
                raise AuthorizationError("internal invitation domain or policy epoch changed")
            sponsor = load_credential_binding_from_connection(
                connection, transaction.sponsor_credential_id
            )
            sponsor.require_active(now=now)
            sponsor_authority_id = (
                sponsor.principal_id if transaction.sponsor_authority_kind == "human" else sponsor.guest_id
            )
            if (
                sponsor.domain_id != transaction.domain_id
                or sponsor.harness_id != transaction.sponsor_harness_id
                or sponsor.credential_epoch != transaction.sponsor_credential_epoch
                or sponsor_authority_id != transaction.sponsor_authority_id
            ):
                raise AuthorizationError("internal invitation sponsor authority changed")
            self._validate_oidc_verification(transaction, verification, now=now)
            try:
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (
                        f"oidc-invitation:{self.oidc_verifier.verifier_id}",
                        verification.id_token_hash,
                        verification.expires_at,
                    ),
                )
            except Exception as exc:
                if _is_integrity_constraint_error(exc):
                    raise AuthenticationError("verified OIDC proof was already consumed") from exc
                raise
            if connection.execute(
                "SELECT harness_id FROM harnesses WHERE harness_id=?",
                (transaction.candidate_harness_id,),
            ).fetchone() is not None:
                raise ConflictError("candidate harness identifier is already enrolled")
            if connection.execute(
                "SELECT credential_id FROM credentials WHERE key_id=? LIMIT 1",
                (transaction.candidate_key_id,),
            ).fetchone() is not None:
                raise ConflictError("candidate key is already enrolled")

            identity = verification.identity
            principal = connection.execute(
                "SELECT * FROM principals WHERE domain_id=? AND oidc_issuer=? AND oidc_subject=?",
                (transaction.domain_id, identity.issuer, identity.subject),
            ).fetchone()
            email_owners = connection.execute(
                """
                SELECT DISTINCT p.principal_id,p.oidc_issuer,p.oidc_subject
                  FROM principals p LEFT JOIN principal_aliases a ON a.principal_id=p.principal_id
                 WHERE p.domain_id=? AND (p.verified_email=? OR a.verified_email=?)
                """,
                (transaction.domain_id, identity.verified_email, identity.verified_email),
            ).fetchall()
            if any(
                owner["oidc_issuer"] != identity.issuer or owner["oidc_subject"] != identity.subject
                for owner in email_owners
            ):
                raise ConflictError("verified email is bound to a different OIDC subject")
            if principal is None:
                principal_id = str(uuid4())
                connection.execute(
                    """
                    INSERT INTO principals(
                        principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                    ) VALUES(?,?,?,?,?,'active',?)
                    """,
                    (
                        principal_id,
                        transaction.domain_id,
                        identity.issuer,
                        identity.subject,
                        identity.verified_email,
                        now,
                    ),
                )
            else:
                if principal["status"] != "active":
                    raise ConflictError("existing principal requires explicit recovery")
                principal_id = str(principal["principal_id"])
                if principal["verified_email"] != identity.verified_email:
                    connection.execute(
                        "UPDATE principals SET verified_email=? WHERE principal_id=? AND status='active'",
                        (identity.verified_email, principal_id),
                    )
            connection.execute(
                """
                INSERT INTO principal_aliases(principal_id,verified_email,first_seen_at,last_seen_at)
                VALUES(?,?,?,?)
                ON CONFLICT(principal_id,verified_email)
                DO UPDATE SET last_seen_at=excluded.last_seen_at
                """,
                (principal_id, identity.verified_email, now, now),
            )

            credential_id = str(
                uuid5(
                    NAMESPACE_URL,
                    f"agentnet:internal-invitation-credential:{transaction.invitation_id}:"
                    f"{transaction.candidate_harness_id}:{transaction.candidate_key_id}",
                )
            )
            harness_status = (
                "deterministic_only"
                if transaction.candidate_binding_assurance == "lab"
                else "active"
            )
            credential_expires_at = now + self.credential_ttl_seconds
            connection.execute(
                """
                INSERT INTO harnesses(
                    harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                    binding_assurance,capabilities_json,credential_epoch,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,1,?)
                """,
                (
                    transaction.candidate_harness_id,
                    transaction.domain_id,
                    principal_id,
                    None,
                    transaction.candidate_harness_kind,
                    transaction.candidate_harness_display_name,
                    harness_status,
                    transaction.candidate_binding_assurance,
                    canonical_json(list(transaction.requested_capabilities)).decode("utf-8"),
                    now,
                ),
            )
            connection.execute(
                """
                INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,'active',1,?,?)
                """,
                (
                    credential_id,
                    transaction.candidate_harness_id,
                    transaction.candidate_key_id,
                    transaction.candidate_public_key_pem,
                    now,
                    credential_expires_at,
                ),
            )
            consume = connection.execute(
                """
                UPDATE internal_invitations
                   SET state='consumed',use_count=1,revision=2,updated_at=?,consumed_at=?,
                       accepted_principal_id=?,accepted_harness_id=?
                 WHERE invitation_id=? AND state='active' AND use_count=0 AND revision=1
                   AND expires_at>?
                """,
                (
                    now,
                    now,
                    principal_id,
                    transaction.candidate_harness_id,
                    transaction.invitation_id,
                    now,
                ),
            )
            if consume.rowcount != 1:
                raise ConflictError("internal invitation lifecycle changed before commit")
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.consumed",
                    "invitation_id": transaction.invitation_id,
                    "invitation_digest": transaction.digest,
                    "domain_id": transaction.domain_id,
                    "principal_id": principal_id,
                    "harness_id": transaction.candidate_harness_id,
                    "credential_id": credential_id,
                    "oidc_verifier_id": self.oidc_verifier.verifier_id,
                    "oidc_token_hash": verification.id_token_hash,
                    "positive_entitlements_issued": 0,
                },
            )

        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=transaction.domain_id,
            principal_id=principal_id,
            harness_id=transaction.candidate_harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance=transaction.candidate_binding_assurance,
        )
        return InternalInvitationAcceptance(
            invitation_id=transaction.invitation_id,
            principal_id=principal_id,
            harness_id=transaction.candidate_harness_id,
            credential_id=credential_id,
            key_id=transaction.candidate_key_id,
            credential_expires_at=datetime.fromtimestamp(credential_expires_at, UTC),
            actor=actor,
            requested_capabilities=transaction.requested_capabilities,
        )

    def revoke(
        self,
        invitation_id: str,
        *,
        command: SignedAuthorityCommand | None,
        authority: IssuanceAuthority | None,
        when: datetime | None = None,
    ) -> InternalInvitationRecord:
        when = when or datetime.fromtimestamp(int(self.clock()), UTC)
        now = _epoch_seconds(when)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM internal_invitations WHERE invitation_id=?", (invitation_id,)
            ).fetchone()
            if row is None:
                raise AuthorizationError("internal invitation is unavailable")
            if command is None:
                raise AuthorizationError("signed invitation revocation command is required")
            resource, expected_request = self.revocation_binding(
                invitation_id,
                expected_revision=command.expected_entity_revision,
                reason=command.reason,
            )
            require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action=INTERNAL_INVITATION_REVOKE_ACTION,
                expected_resource=resource,
                expected_request=expected_request,
                when=when,
            )
            if authority is None:  # narrowed by the verifier
                raise AuthorizationError("authenticated invitation sponsor is required")
            actor = authority.actor
            expected_kind = (
                ActorKind.VERIFIED_HUMAN_HARNESS
                if row["sponsor_authority_kind"] == "human"
                else ActorKind.HOST_GUEST_HARNESS
            )
            if (
                actor.kind is not expected_kind
                or actor.domain_id != row["domain_id"]
                or actor.positive_authority_id != row["sponsor_authority_id"]
                or actor.harness_id != row["sponsor_harness_id"]
            ):
                raise AuthorizationError("only the exact current invitation sponsor may revoke")
            if command.expected_entity_revision != int(row["revision"]):
                raise ConflictError("invitation revocation revision is stale")
            if row["state"] != "active" or int(row["use_count"]) != 0:
                raise ConflictError("only an active unconsumed invitation may be revoked")
            begin_authority_mutation_intent(
                connection,
                command=command,
                authority=authority,
                when=when,
            )
            update = connection.execute(
                """
                UPDATE internal_invitations
                   SET state='revoked',revision=revision+1,updated_at=?,revoked_at=?
                 WHERE invitation_id=? AND state='active' AND use_count=0 AND revision=?
                """,
                (now, now, invitation_id, command.expected_entity_revision),
            )
            if update.rowcount != 1:
                raise ConflictError("invitation lifecycle changed before revocation")
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.revoked",
                    "invitation_id": invitation_id,
                    "invitation_digest": row["invitation_digest"],
                    "domain_id": row["domain_id"],
                    "actor": actor.audit_view(),
                    "reason": command.reason,
                    "previous_revision": command.expected_entity_revision,
                    "new_revision": command.expected_entity_revision + 1,
                },
            )
            complete_authority_mutation_intent(
                connection,
                command_id=command.command_id,
                when=when,
            )
            updated = connection.execute(
                "SELECT * FROM internal_invitations WHERE invitation_id=?", (invitation_id,)
            ).fetchone()
            if updated is None:  # pragma: no cover - same transaction
                raise RuntimeError("revoked invitation disappeared")
            return self._from_row(updated)

    @staticmethod
    def _request_matches_transaction(
        request: InternalInvitationRequest,
        transaction: InternalInvitationTransaction,
        *,
        sponsor_actor: VerifiedActor,
        policy_revision: int,
        domain_revocation_epoch: int,
    ) -> bool:
        sponsor_kind = (
            "human"
            if sponsor_actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
            else "guest"
        )
        transaction_request = InternalInvitationRequest(
            invitation_id=transaction.invitation_id,
            domain_id=transaction.domain_id,
            invited_oidc_issuer=transaction.invited_oidc_issuer,
            invited_oidc_subject=transaction.invited_oidc_subject,
            invited_verified_email=transaction.invited_verified_email,
            candidate_harness_id=transaction.candidate_harness_id,
            candidate_harness_kind=transaction.candidate_harness_kind,
            candidate_harness_display_name=transaction.candidate_harness_display_name,
            candidate_binding_assurance=transaction.candidate_binding_assurance,
            candidate_key_id=transaction.candidate_key_id,
            candidate_public_key_pem=transaction.candidate_public_key_pem,
            requested_capabilities=transaction.requested_capabilities,
            expires_at=transaction.expires_at,
            predecessor_invitation_id=transaction.predecessor_invitation_id,
            reason=transaction.reason,
        )
        request_bytes_match = secrets.compare_digest(
            canonical_json(request.model_dump(mode="json")),
            canonical_json(transaction_request.model_dump(mode="json")),
        )
        return bool(
            request_bytes_match
            and transaction.sponsor_authority_kind == sponsor_kind
            and transaction.sponsor_authority_id == sponsor_actor.positive_authority_id
            and transaction.sponsor_harness_id == sponsor_actor.harness_id
            and transaction.sponsor_credential_id == sponsor_actor.credential_id
            and transaction.sponsor_credential_epoch == sponsor_actor.credential_epoch
            and transaction.policy_revision == policy_revision
            and transaction.domain_revocation_epoch == domain_revocation_epoch
        )

    def expire_due(self, *, when: datetime | None = None) -> int:
        when = when or datetime.fromtimestamp(int(self.clock()), UTC)
        now = _epoch_seconds(when)
        with self.store.transaction() as connection:
            rows = connection.execute(
                """
                SELECT invitation_id,invitation_digest,domain_id,revision
                  FROM internal_invitations
                 WHERE state='active' AND use_count=0 AND expires_at<=?
                 ORDER BY invitation_id
                """,
                (now,),
            ).fetchall()
            for row in rows:
                update = connection.execute(
                    """
                    UPDATE internal_invitations
                       SET state='expired',revision=revision+1,updated_at=?,revoked_at=?
                     WHERE invitation_id=? AND state='active' AND use_count=0
                       AND revision=? AND expires_at<=?
                    """,
                    (now, now, row["invitation_id"], row["revision"], now),
                )
                if update.rowcount != 1:
                    continue
                self.store.append_audit(
                    connection,
                    {
                        "action": "internal_invitation.expired",
                        "invitation_id": row["invitation_id"],
                        "invitation_digest": row["invitation_digest"],
                        "domain_id": row["domain_id"],
                        "previous_revision": int(row["revision"]),
                        "new_revision": int(row["revision"]) + 1,
                    },
                )
            return len(rows)

    def _require_safe_predecessor(
        self,
        connection: Any,
        *,
        request: InternalInvitationRequest,
        sponsor_actor: VerifiedActor,
    ) -> InternalInvitationRecord | None:
        rows = connection.execute(
            """
            SELECT * FROM internal_invitations
             WHERE domain_id=? AND candidate_harness_id=?
             ORDER BY invitation_id
            """,
            (request.domain_id, request.candidate_harness_id),
        ).fetchall()
        if not rows:
            if request.predecessor_invitation_id is not None:
                raise ConflictError("invitation predecessor is unavailable")
            return None
        if request.predecessor_invitation_id is None:
            raise ConflictError("invitation reissue must bind the latest predecessor")
        records = [self._from_row(row) for row in rows]
        matches = [
            record
            for record in records
            if record.transaction.invitation_id == request.predecessor_invitation_id
        ]
        if len(matches) != 1:
            raise ConflictError("invitation predecessor is unavailable or ambiguous")
        predecessor = matches[0]
        if any(
            record.transaction.predecessor_invitation_id == request.predecessor_invitation_id
            for record in records
        ):
            raise ConflictError("invitation predecessor was already reissued")
        if any(record.state == "active" for record in records):
            raise ConflictError("an active invitation already exists for the candidate harness")
        previous = predecessor.transaction
        if predecessor.state not in {"revoked", "expired"}:
            raise ConflictError("only a revoked or expired invitation may be reissued")
        if (
            previous.domain_id != request.domain_id
            or previous.sponsor_authority_id != sponsor_actor.positive_authority_id
            or previous.sponsor_harness_id != sponsor_actor.harness_id
            or previous.invited_oidc_issuer != request.invited_oidc_issuer
            or previous.invited_oidc_subject != request.invited_oidc_subject
            or previous.invited_verified_email != request.invited_verified_email
            or previous.candidate_harness_id != request.candidate_harness_id
            or previous.candidate_harness_kind != request.candidate_harness_kind
        ):
            raise ConflictError("invitation reissue changes predecessor identity or sponsor binding")
        return predecessor

    @staticmethod
    def _parse_exact_transaction(value: bytes) -> InternalInvitationTransaction:
        if not isinstance(value, bytes) or len(value) < 128 or len(value) > 65_536:
            raise ValidationError("canonical invitation bytes are outside the supported size")
        try:
            decoded = json.loads(value)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("canonical invitation is invalid JSON") from exc
        if not isinstance(decoded, dict):
            raise ValidationError("canonical invitation must be an object")
        try:
            transaction = InternalInvitationTransaction.model_validate(decoded)
        except Exception as exc:
            raise ValidationError("canonical invitation does not match the strict schema") from exc
        if canonical_json(transaction.model_dump(mode="json")) != value:
            raise ValidationError("invitation bytes are not exactly canonical")
        return transaction

    def _require_row_matches_bytes(
        self,
        row: Any,
        transaction: InternalInvitationTransaction,
        canonical_invitation: bytes,
    ) -> None:
        serialized = canonical_invitation.decode("utf-8")
        digest = hashlib.sha256(canonical_invitation).hexdigest()
        if (
            row["schema_version"] != "1.0"
            or row["canonical_invitation_json"] != serialized
            or not secrets.compare_digest(str(row["invitation_digest"]), digest)
            or not secrets.compare_digest(transaction.digest, digest)
        ):
            raise AuthenticationError("internal invitation canonical binding mismatch")

    @staticmethod
    def _validate_oidc_verification(
        transaction: InternalInvitationTransaction,
        verification: OIDCVerificationResult,
        *,
        now: int,
    ) -> None:
        if not isinstance(verification, OIDCVerificationResult):
            raise AuthenticationError("OIDC verifier returned an invalid result")
        identity = verification.identity
        if not isinstance(identity, VerifiedOIDCIdentity):
            raise AuthenticationError("OIDC verifier returned an invalid identity")
        if (
            identity.issuer != transaction.invited_oidc_issuer
            or identity.subject != transaction.invited_oidc_subject
            or identity.verified_email != transaction.invited_verified_email
        ):
            raise AuthenticationError("verified OIDC identity does not match the invitation")
        if not _SHA256.fullmatch(verification.id_token_hash) or now >= verification.expires_at:
            raise AuthenticationError("verified OIDC proof is expired or malformed")

    def _require_not_locked(self, row: Any, *, source_fingerprint: str, now: int) -> None:
        if row["state"] != "active" or int(row["use_count"]) != 0:
            raise AuthenticationError("internal invitation is not active")
        abuse = self.store.fetch_one(
            "SELECT * FROM internal_invitation_abuse WHERE invitation_id=? AND source_fingerprint=?",
            (row["invitation_id"], source_fingerprint),
        )
        if abuse is not None and abuse["locked_until"] is not None and now < int(abuse["locked_until"]):
            raise AuthenticationError("internal invitation attempts are temporarily locked")

    @staticmethod
    def _require_not_locked_in_connection(
        connection: Any,
        *,
        invitation_id: str,
        source_fingerprint: str,
        now: int,
    ) -> None:
        abuse = connection.execute(
            "SELECT locked_until FROM internal_invitation_abuse WHERE invitation_id=? AND source_fingerprint=?",
            (invitation_id, source_fingerprint),
        ).fetchone()
        if abuse is not None and abuse["locked_until"] is not None and now < int(abuse["locked_until"]):
            raise AuthenticationError("internal invitation attempts are temporarily locked")

    def _record_failed_attempt(
        self,
        *,
        invitation_id: str,
        source_fingerprint: str,
        now: int,
    ) -> None:
        with self.store.transaction() as connection:
            invitation = connection.execute(
                "SELECT state,use_count,expires_at FROM internal_invitations WHERE invitation_id=?",
                (invitation_id,),
            ).fetchone()
            if (
                invitation is None
                or invitation["state"] != "active"
                or int(invitation["use_count"]) != 0
                or now >= int(invitation["expires_at"])
            ):
                return
            prior = connection.execute(
                "SELECT * FROM internal_invitation_abuse WHERE invitation_id=? AND source_fingerprint=?",
                (invitation_id, source_fingerprint),
            ).fetchone()
            if prior is not None and prior["locked_until"] is not None and now < int(prior["locked_until"]):
                return
            if prior is None or now >= int(prior["window_started_at"]) + self.failure_window_seconds:
                window_started_at = now
                failure_count = 1
            else:
                window_started_at = int(prior["window_started_at"])
                failure_count = int(prior["failure_count"]) + 1
            locked_until = (
                now + self.lockout_seconds
                if failure_count >= self.maximum_failures_per_source
                else None
            )
            connection.execute(
                """
                INSERT INTO internal_invitation_abuse(
                    invitation_id,source_fingerprint,window_started_at,
                    failure_count,locked_until,updated_at
                ) VALUES(?,?,?,?,?,?)
                ON CONFLICT(invitation_id,source_fingerprint) DO UPDATE SET
                    window_started_at=excluded.window_started_at,
                    failure_count=excluded.failure_count,
                    locked_until=excluded.locked_until,
                    updated_at=excluded.updated_at
                """,
                (
                    invitation_id,
                    source_fingerprint,
                    window_started_at,
                    failure_count,
                    locked_until,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "internal_invitation.failed_attempt",
                    "invitation_id": invitation_id,
                    "source_fingerprint": source_fingerprint,
                    "failure_count": failure_count,
                    "locked": locked_until is not None,
                },
            )

    @staticmethod
    def _from_row(row: Any) -> InternalInvitationRecord:
        try:
            decoded = json.loads(row["canonical_invitation_json"])
            transaction = InternalInvitationTransaction.model_validate(decoded)
            canonical = canonical_json(transaction.model_dump(mode="json")).decode("utf-8")
            digest = hashlib.sha256(canonical.encode("utf-8")).hexdigest()
            capabilities = json.loads(row["requested_capabilities_json"])
            if (
                canonical != row["canonical_invitation_json"]
                or digest != row["invitation_digest"]
                or transaction.invitation_id != row["invitation_id"]
                or transaction.domain_id != row["domain_id"]
                or transaction.sponsor_authority_kind != row["sponsor_authority_kind"]
                or transaction.sponsor_authority_id != row["sponsor_authority_id"]
                or transaction.sponsor_harness_id != row["sponsor_harness_id"]
                or transaction.sponsor_credential_id != row["sponsor_credential_id"]
                or transaction.sponsor_credential_epoch != int(row["sponsor_credential_epoch"])
                or transaction.invited_oidc_issuer != row["invited_oidc_issuer"]
                or transaction.invited_oidc_subject != row["invited_oidc_subject"]
                or transaction.invited_verified_email != row["invited_verified_email"]
                or transaction.candidate_harness_id != row["candidate_harness_id"]
                or transaction.candidate_harness_kind != row["candidate_harness_kind"]
                or transaction.candidate_key_id != row["candidate_key_id"]
                or transaction.candidate_public_key_pem != row["candidate_public_key_pem"]
                or list(transaction.requested_capabilities) != capabilities
                or transaction.policy_revision != int(row["policy_revision"])
                or transaction.domain_revocation_epoch != int(row["domain_revocation_epoch"])
                or int(row["max_uses"]) != 1
                or _epoch_seconds(transaction.expires_at) != int(row["expires_at"])
                or (
                    row["accepted_harness_id"] is not None
                    and row["accepted_harness_id"] != transaction.candidate_harness_id
                )
            ):
                raise ValueError("stored invitation columns disagree with canonical bytes")
        except Exception as exc:
            raise GateBlocked(
                "internal_invitation_integrity",
                "stored internal invitation failed canonical integrity checks",
            ) from exc
        return InternalInvitationRecord(
            transaction=transaction,
            invitation_digest=digest,
            state=row["state"],
            revision=int(row["revision"]),
            use_count=int(row["use_count"]),
            created_at=datetime.fromtimestamp(int(row["created_at"]), UTC),
            updated_at=datetime.fromtimestamp(int(row["updated_at"]), UTC),
            consumed_at=(
                datetime.fromtimestamp(int(row["consumed_at"]), UTC)
                if row["consumed_at"] is not None
                else None
            ),
            revoked_at=(
                datetime.fromtimestamp(int(row["revoked_at"]), UTC)
                if row["revoked_at"] is not None
                else None
            ),
            accepted_principal_id=row["accepted_principal_id"],
            accepted_harness_id=row["accepted_harness_id"],
        )


__all__ = [
    "INTERNAL_INVITATION_ISSUE_ACTION",
    "INTERNAL_INVITATION_POP_PURPOSE",
    "INTERNAL_INVITATION_REVOKE_ACTION",
    "InternalInvitationAcceptance",
    "InternalInvitationOIDCVerifier",
    "InternalInvitationRecord",
    "InternalInvitationRequest",
    "InternalInvitationService",
    "InternalInvitationTransaction",
]
