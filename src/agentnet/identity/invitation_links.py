"""Sponsor-authorized opaque links for bounded collaboration onboarding.

The bearer value is only a high-entropy lookup secret.  It is never stored and
never establishes identity or authority; all authority is re-established from
verified OIDC, key possession, independent approval, and current durable policy
at redemption time.
"""

from __future__ import annotations

import hashlib
import io
import json
import re
import secrets
from datetime import UTC, datetime
from typing import Any, Literal
from urllib.parse import quote, urlsplit, urlunsplit
from uuid import NAMESPACE_URL, uuid5

from pydantic import AnyHttpUrl, BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.authorization.communication_scope_service import CollaborationScopeProposal
from agentnet.authorization.evidence import IssuanceAuthority, require_current_authority_decision
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.security.signatures import canonical_digest
from agentnet.storage.backend import StoreBackend

INVITATION_LINK_ISSUE_ACTION = "identity.invitation_link.issue"
INVITATION_LINK_REVOKE_ACTION = "identity.invitation_link.revoke"
INVITATION_LINK_TTL_SECONDS = 86_400
_EMAIL = re.compile(r"^[^\s@]+@[^\s@]+\.[^\s@]+$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")


class InvitationUnavailable(AuthenticationError):
    """One public error for unknown, terminal, expired, or abusive links."""

    code = "invitation_unavailable"
    http_status = 410

    def __init__(self) -> None:
        super().__init__("invitation is unavailable")

    def public_detail(self) -> dict[str, str]:
        return {"code": self.code, "message": "invitation is unavailable"}


class InvitationOffer(BaseModel):
    """Exact sponsor-approved email, trust domain, scope, and action set."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.invitation-offer.v1"] = "agentnet.invitation-offer.v1"
    invitation_id: str = Field(min_length=16, max_length=128)
    invited_verified_email: str = Field(min_length=3, max_length=320)
    domain_id: str = Field(min_length=1, max_length=256)
    collaboration_scope_template: CollaborationScopeProposal
    permission_actions: tuple[str, ...] = Field(min_length=1, max_length=64)
    expires_at: int
    max_uses: Literal[1] = 1

    @field_validator("invitation_id", "domain_id")
    @classmethod
    def canonical_identifier(cls, value: str) -> str:
        if value != value.strip() or any(ord(character) < 0x21 or ord(character) > 0x7E for character in value):
            raise ValueError("invitation identifier is not canonical")
        return value

    @field_validator("invited_verified_email")
    @classmethod
    def canonical_email(cls, value: str) -> str:
        if value != value.strip().casefold() or not _EMAIL.fullmatch(value):
            raise ValueError("invited verified email is not canonical")
        return value

    @field_validator("permission_actions")
    @classmethod
    def canonical_actions(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if tuple(sorted(set(value))) != value:
            raise ValueError("invitation permissions must be unique and canonically sorted")
        if any(
            not action
            or len(action) > 128
            or any(not (character.isalnum() or character in "._:-") for character in action)
            for action in value
        ):
            raise ValueError("invitation permission is outside the supported profile")
        return value

    @model_validator(mode="after")
    def coherent_binding(self) -> "InvitationOffer":
        email_domain = self.invited_verified_email.rsplit("@", 1)[1]
        if not secrets.compare_digest(email_domain, self.domain_id.casefold()):
            raise ValueError("invited email is outside the invitation trust domain")
        if self.permission_actions != self.collaboration_scope_template.allowed_actions:
            raise ValueError(
                "invitation permissions must equal the enforceable collaboration scope"
            )
        return self

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))


class IssuedInvitationLink(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    invitation_id: str
    public_url: AnyHttpUrl
    qr_svg: str
    expires_at: int


class PublicInvitationSummary(BaseModel):
    """Non-sensitive copy support; deliberately excludes email and identifiers."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    scope_kind: Literal["personal", "direct", "shared"]
    permission_actions: tuple[str, ...]
    expires_at: int


class InvitationRedemptionReservation(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    invitation_id: str
    reservation_id: str
    reservation_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    offer_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    destination_scope_id: str
    permission_actions: tuple[str, ...]
    expires_at: int
    reserved_at: int
    revision: int = Field(ge=2)


class InvitationRecord(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    offer: InvitationOffer
    state: Literal["issued", "reserved", "consumed", "revoked", "expired"]
    state_reason: str
    revision: int = Field(ge=1)
    use_count: Literal[0, 1]
    reservation_id: str | None = None
    reservation_digest: str | None = None
    reserved_at: int | None = None
    created_at: int
    updated_at: int
    consumed_at: int | None = None
    revoked_at: int | None = None


class InvitationLinkService:
    """Issue, inspect, reserve, revoke, and atomically consume opaque links."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        public_base_url: str,
        clock: Any | None = None,
        failure_window_seconds: int = 300,
        maximum_failures_per_source: int = 5,
        lockout_seconds: int = 300,
    ) -> None:
        parsed = urlsplit(public_base_url)
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.query
            or parsed.fragment
            or parsed.path.rstrip("/") != "/join"
        ):
            raise ValueError("invitation public base URL must be canonical HTTPS")
        if failure_window_seconds < 1 or failure_window_seconds > 3_600:
            raise ValueError("invitation failure window is outside the supported range")
        if maximum_failures_per_source < 1 or maximum_failures_per_source > 100:
            raise ValueError("invitation failure ceiling is outside the supported range")
        if lockout_seconds < 1 or lockout_seconds > 86_400:
            raise ValueError("invitation lockout is outside the supported range")
        self.store = store
        self.public_base_url = urlunsplit(
            (parsed.scheme, parsed.netloc, parsed.path.rstrip("/"), "", "")
        )
        self.clock = clock or (lambda: int(datetime.now(UTC).timestamp()))
        self.failure_window_seconds = failure_window_seconds
        self.maximum_failures_per_source = maximum_failures_per_source
        self.lockout_seconds = lockout_seconds

    @staticmethod
    def authority_binding(
        offer: InvitationOffer,
        *,
        action: str,
        expected_revision: int = 1,
    ) -> tuple[str, dict[str, object]]:
        if action not in {INVITATION_LINK_ISSUE_ACTION, INVITATION_LINK_REVOKE_ACTION}:
            raise ValueError("unsupported invitation-link authority action")
        return (
            f"invitation-link:{offer.invitation_id}",
            {
                "schema": "agentnet.invitation-link.authority.v1",
                "action": action,
                "invitation_id": offer.invitation_id,
                "offer_digest": offer.digest,
                "domain_id": offer.domain_id,
                "destination_scope_id": offer.collaboration_scope_template.scope_id,
                "expected_revision": expected_revision,
            },
        )

    def issue(
        self,
        *,
        actor: VerifiedActor,
        offer: InvitationOffer,
        authority: IssuanceAuthority | None,
    ) -> IssuedInvitationLink:
        offer = InvitationOffer.model_validate(offer.model_dump(mode="python"))
        now = int(self.clock())
        if offer.expires_at != now + INVITATION_LINK_TTL_SECONDS:
            raise ValidationError("invitation expiry must be exactly 24 hours after issuance")
        opaque_token = secrets.token_urlsafe(32)
        token_hash = _token_hash(opaque_token)
        public_url = f"{self.public_base_url}/{quote(opaque_token, safe='')}"
        qr_svg = invitation_qr_svg(public_url)
        with self.store.transaction() as connection:
            resource, request = self.authority_binding(
                offer, action=INVITATION_LINK_ISSUE_ACTION
            )
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=INVITATION_LINK_ISSUE_ACTION,
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            if authority is None or authority.actor != actor:
                raise AuthorizationError("authenticated invitation sponsor mismatch")
            self._require_sponsor(connection, actor=actor, domain_id=offer.domain_id, now=now)
            domain = connection.execute(
                "SELECT * FROM domains WHERE domain_id=?", (offer.domain_id,)
            ).fetchone()
            if (
                domain is None
                or domain["status"] != "active"
                or int(domain["policy_revision"]) != policy_revision
                or offer.collaboration_scope_template.policy_revision != policy_revision
                or int(domain["revocation_epoch"])
                != offer.collaboration_scope_template.domain_revocation_epoch
            ):
                raise AuthorizationError("invitation trust domain is unavailable")
            scope = self._require_exact_scope(
                connection,
                offer=offer,
                actor=actor,
                now=now,
                require_administrator=True,
            )
            if connection.execute(
                "SELECT invitation_id FROM invitation_links WHERE invitation_id=? OR offer_digest=?",
                (offer.invitation_id, offer.digest),
            ).fetchone() is not None:
                raise ConflictError("invitation identifier or canonical offer already exists")
            encrypted_offer = self.store.cipher.encrypt_json(
                offer.model_dump(mode="json", exclude={"invited_verified_email"}),
                purpose=f"invitation-link-offer:{offer.invitation_id}",
            )
            encrypted_email = self.store.cipher.encrypt_json(
                offer.invited_verified_email,
                purpose=f"invitation-link-email:{offer.invitation_id}",
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "invitation_link.issued",
                    "invitation_id": offer.invitation_id,
                    "offer_digest": offer.digest,
                    "domain_id": offer.domain_id,
                    "destination_scope_id": scope["scope_id"],
                    "sponsor": actor.audit_view(),
                    "permission_actions": list(offer.permission_actions),
                    "expires_at": offer.expires_at,
                    "max_uses": 1,
                    "opaque_token_stored": False,
                },
            )
            connection.execute(
                """INSERT INTO invitation_links(
                    invitation_id,domain_id,destination_scope_id,token_hash,encrypted_offer,
                    offer_digest,invited_email_encrypted,invited_email_sha256,
                    sponsor_principal_id,sponsor_harness_id,sponsor_credential_id,
                    sponsor_credential_epoch,policy_revision,domain_revocation_epoch,state,
                    state_reason,max_uses,use_count,revision,reservation_id,reservation_digest,
                    reserved_at,expires_at,created_at,updated_at,consumed_at,revoked_at,
                    reissued_from_invitation_id,audit_record_hash
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'issued','issued',1,0,1,NULL,NULL,NULL,?,?,?,NULL,NULL,NULL,?)""",
                (
                    offer.invitation_id,
                    offer.domain_id,
                    scope["scope_id"],
                    token_hash,
                    encrypted_offer,
                    offer.digest,
                    encrypted_email,
                    hashlib.sha256(offer.invited_verified_email.encode("utf-8")).hexdigest(),
                    actor.principal_id,
                    actor.harness_id,
                    actor.credential_id,
                    actor.credential_epoch,
                    policy_revision,
                    int(domain["revocation_epoch"]),
                    offer.expires_at,
                    now,
                    now,
                    audit_hash,
                ),
            )
        return IssuedInvitationLink(
            invitation_id=offer.invitation_id,
            public_url=public_url,
            qr_svg=qr_svg,
            expires_at=offer.expires_at,
        )

    def inspect_public(self, *, opaque_token: str) -> PublicInvitationSummary:
        token_hash = _public_token_hash(opaque_token)
        if token_hash is None:
            raise InvitationUnavailable()
        now = int(self.clock())
        self._expire_due(now=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM invitation_links WHERE token_hash=?", (token_hash,)
            ).fetchone()
            if row is None or row["state"] != "issued" or int(row["expires_at"]) <= now:
                raise InvitationUnavailable()
            offer = self._offer_from_row(row)
            try:
                self._require_exact_scope(
                    connection,
                    offer=offer,
                    actor=None,
                    now=now,
                    require_administrator=False,
                )
            except AuthorizationError as exc:
                raise InvitationUnavailable() from exc
            return PublicInvitationSummary(
                scope_kind=offer.collaboration_scope_template.scope_kind,
                permission_actions=offer.permission_actions,
                expires_at=offer.expires_at,
            )

    def reserve_redemption(
        self,
        *,
        opaque_token: str,
        source_fingerprint: str,
    ) -> InvitationRedemptionReservation:
        token_hash = _public_token_hash(opaque_token)
        self._validate_source(source_fingerprint)
        if token_hash is None:
            raise InvitationUnavailable()
        now = int(self.clock())
        reservation_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:invitation-link-reservation:{token_hash}:{source_fingerprint}",
            )
        )
        self._expire_due(now=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM invitation_links WHERE token_hash=?", (token_hash,)
            ).fetchone()
            if row is None:
                raise InvitationUnavailable()
            self._require_not_locked_connection(
                connection,
                invitation_id=str(row["invitation_id"]),
                source_fingerprint=source_fingerprint,
                now=now,
            )
            offer = self._offer_from_row(row)
            try:
                self._require_exact_scope(
                    connection,
                    offer=offer,
                    actor=None,
                    now=now,
                    require_administrator=False,
                )
            except AuthorizationError as exc:
                raise InvitationUnavailable() from exc
            if row["state"] == "reserved" and row["reservation_id"] == reservation_id:
                return self._reservation_from_row(row, offer)
            if row["state"] != "issued" or int(row["use_count"]) != 0 or int(row["revision"]) != 1:
                raise InvitationUnavailable()
            reservation_digest = self._reservation_digest(
                invitation_id=offer.invitation_id,
                reservation_id=reservation_id,
                offer_digest=offer.digest,
                source_fingerprint=source_fingerprint,
                reserved_at=now,
                expires_at=offer.expires_at,
            )
            updated = connection.execute(
                """UPDATE invitation_links
                   SET state='reserved',state_reason='redemption_reserved',revision=2,
                       reservation_id=?,reservation_digest=?,reserved_at=?,updated_at=?
                 WHERE invitation_id=? AND token_hash=? AND state='issued' AND use_count=0
                   AND revision=1 AND expires_at>?""",
                (
                    reservation_id,
                    reservation_digest,
                    now,
                    now,
                    offer.invitation_id,
                    token_hash,
                    now,
                ),
            )
            if updated.rowcount != 1:
                raise InvitationUnavailable()
            self.store.append_audit(
                connection,
                {
                    "action": "invitation_link.redemption_reserved",
                    "invitation_id": offer.invitation_id,
                    "offer_digest": offer.digest,
                    "reservation_digest": reservation_digest,
                    "source_fingerprint": source_fingerprint,
                    "reserved_at": now,
                },
            )
            reserved = connection.execute(
                "SELECT * FROM invitation_links WHERE invitation_id=?", (offer.invitation_id,)
            ).fetchone()
            if reserved is None:
                raise GateBlocked("invitation_link", "invitation reservation is unavailable")
            return self._reservation_from_row(reserved, offer)

    def validate_reserved(
        self,
        *,
        reservation: InvitationRedemptionReservation,
        source_fingerprint: str,
    ) -> InvitationOffer:
        reservation = InvitationRedemptionReservation.model_validate(
            reservation.model_dump(mode="python")
        )
        self._validate_source(source_fingerprint)
        now = int(self.clock())
        self._expire_due(now=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM invitation_links WHERE invitation_id=?",
                (reservation.invitation_id,),
            ).fetchone()
            if row is None:
                raise InvitationUnavailable()
            self._require_not_locked_connection(
                connection,
                invitation_id=reservation.invitation_id,
                source_fingerprint=source_fingerprint,
                now=now,
            )
            offer = self._offer_from_row(row)
            expected = self._reservation_from_row(row, offer) if row["state"] == "reserved" else None
            if expected is None:
                raise InvitationUnavailable()
            if expected != reservation:
                raise ConflictError("invitation redemption reservation binding mismatch")
            expected_digest = self._reservation_digest(
                invitation_id=offer.invitation_id,
                reservation_id=reservation.reservation_id,
                offer_digest=offer.digest,
                source_fingerprint=source_fingerprint,
                reserved_at=reservation.reserved_at,
                expires_at=reservation.expires_at,
            )
            if not secrets.compare_digest(expected_digest, reservation.reservation_digest):
                raise AuthenticationError("invitation redemption source binding mismatch")
            self._require_exact_scope(
                connection,
                offer=offer,
                actor=None,
                now=now,
                require_administrator=False,
            )
            return offer

    def require_internal_sponsor_binding(
        self,
        *,
        reservation: InvitationRedemptionReservation,
        domain_id: str,
        sponsor_principal_id: str,
        sponsor_harness_id: str,
        sponsor_credential_id: str,
        sponsor_credential_epoch: int,
    ) -> None:
        """Require the internal enrollment sponsor to be the exact link sponsor."""

        row = self.store.fetch_one(
            """SELECT domain_id,sponsor_principal_id,sponsor_harness_id,
                      sponsor_credential_id,sponsor_credential_epoch,state,
                      reservation_id,reservation_digest
                 FROM invitation_links WHERE invitation_id=?""",
            (reservation.invitation_id,),
        )
        if (
            row is None
            or row["state"] != "reserved"
            or row["reservation_id"] != reservation.reservation_id
            or row["reservation_digest"] != reservation.reservation_digest
            or not secrets.compare_digest(str(row["domain_id"]), domain_id)
            or not secrets.compare_digest(
                str(row["sponsor_principal_id"]), sponsor_principal_id
            )
            or not secrets.compare_digest(
                str(row["sponsor_harness_id"]), sponsor_harness_id
            )
            or not secrets.compare_digest(
                str(row["sponsor_credential_id"]), sponsor_credential_id
            )
            or int(row["sponsor_credential_epoch"]) != sponsor_credential_epoch
        ):
            raise AuthorizationError("invitation sponsor binding mismatch")

    def note_redemption_failure(
        self,
        *,
        reservation: InvitationRedemptionReservation,
        source_fingerprint: str,
    ) -> None:
        self._validate_source(source_fingerprint)
        now = int(self.clock())
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT reservation_id,reservation_digest FROM invitation_links WHERE invitation_id=?",
                (reservation.invitation_id,),
            ).fetchone()
            if (
                row is None
                or row["reservation_id"] != reservation.reservation_id
                or row["reservation_digest"] != reservation.reservation_digest
            ):
                return
            failure = connection.execute(
                "SELECT * FROM invitation_link_failures WHERE invitation_id=? AND source_fingerprint=?",
                (reservation.invitation_id, source_fingerprint),
            ).fetchone()
            if (
                failure is not None
                and failure["locked_until"] is not None
                and now < int(failure["locked_until"])
            ):
                return
            if failure is None or now - int(failure["window_started_at"]) >= self.failure_window_seconds:
                count = 1
                window_started_at = now
            else:
                count = int(failure["failure_count"]) + 1
                window_started_at = int(failure["window_started_at"])
            locked_until = now + self.lockout_seconds if count >= self.maximum_failures_per_source else None
            connection.execute(
                """INSERT INTO invitation_link_failures(
                    invitation_id,source_fingerprint,window_started_at,failure_count,locked_until,updated_at
                ) VALUES(?,?,?,?,?,?)
                ON CONFLICT(invitation_id,source_fingerprint) DO UPDATE SET
                    window_started_at=excluded.window_started_at,
                    failure_count=excluded.failure_count,
                    locked_until=excluded.locked_until,
                    updated_at=excluded.updated_at""",
                (
                    reservation.invitation_id,
                    source_fingerprint,
                    window_started_at,
                    count,
                    locked_until,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "invitation_link.redemption_failed",
                    "invitation_id": reservation.invitation_id,
                    "source_fingerprint": source_fingerprint,
                    "failure_count": count,
                    "locked_until": locked_until,
                },
            )

    def revoke(
        self,
        *,
        actor: VerifiedActor,
        invitation_id: str,
        expected_revision: int,
        authority: IssuanceAuthority | None,
    ) -> InvitationRecord:
        now = int(self.clock())
        self._expire_due(now=now)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM invitation_links WHERE invitation_id=?", (invitation_id,)
            ).fetchone()
            if row is None:
                raise AuthorizationError("invitation is unavailable")
            offer = self._offer_from_row(row)
            resource, request = self.authority_binding(
                offer,
                action=INVITATION_LINK_REVOKE_ACTION,
                expected_revision=expected_revision,
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=INVITATION_LINK_REVOKE_ACTION,
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            if authority is None or authority.actor != actor:
                raise AuthorizationError("authenticated invitation sponsor mismatch")
            if (
                actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
                or actor.domain_id != row["domain_id"]
                or actor.principal_id != row["sponsor_principal_id"]
                or actor.harness_id != row["sponsor_harness_id"]
                or actor.credential_id != row["sponsor_credential_id"]
                or actor.credential_epoch != int(row["sponsor_credential_epoch"])
            ):
                raise AuthorizationError("only the exact current sponsor may revoke")
            self._require_sponsor(connection, actor=actor, domain_id=offer.domain_id, now=now)
            if int(row["revision"]) != expected_revision:
                raise ConflictError("invitation revocation revision is stale")
            if row["state"] not in {"issued", "reserved"} or int(row["use_count"]) != 0:
                raise ConflictError("only an active unconsumed invitation may be revoked")
            updated = connection.execute(
                """UPDATE invitation_links
                   SET state='revoked',state_reason='sponsor_revoked',revision=revision+1,
                       updated_at=?,revoked_at=?
                 WHERE invitation_id=? AND revision=? AND state IN ('issued','reserved')
                   AND use_count=0""",
                (now, now, invitation_id, expected_revision),
            )
            if updated.rowcount != 1:
                raise ConflictError("invitation lifecycle changed before revocation")
            self.store.append_audit(
                connection,
                {
                    "action": "invitation_link.revoked",
                    "invitation_id": invitation_id,
                    "offer_digest": offer.digest,
                    "sponsor": actor.audit_view(),
                    "expected_revision": expected_revision,
                },
            )
            final = connection.execute(
                "SELECT * FROM invitation_links WHERE invitation_id=?", (invitation_id,)
            ).fetchone()
            if final is None:
                raise GateBlocked("invitation_link", "revoked invitation is unavailable")
            return self._record_from_row(final)

    def consume_reserved_in_connection(
        self,
        connection: Any,
        *,
        reservation: InvitationRedemptionReservation,
        source_fingerprint: str,
        principal_id: str,
        harness_id: str,
        now: int,
    ) -> InvitationRecord:
        """Consume one validated reservation inside the caller's atomic mutation."""

        self._validate_source(source_fingerprint)
        row = connection.execute(
            "SELECT * FROM invitation_links WHERE invitation_id=?", (reservation.invitation_id,)
        ).fetchone()
        if row is None:
            raise AuthenticationError("invitation is unavailable")
        offer = self._offer_from_row(row)
        expected = self._reservation_from_row(row, offer) if row["state"] == "reserved" else None
        if expected != reservation or now >= offer.expires_at:
            raise ConflictError("invitation redemption reservation changed before commit")
        source_digest = self._reservation_digest(
            invitation_id=offer.invitation_id,
            reservation_id=reservation.reservation_id,
            offer_digest=offer.digest,
            source_fingerprint=source_fingerprint,
            reserved_at=reservation.reserved_at,
            expires_at=reservation.expires_at,
        )
        if not secrets.compare_digest(source_digest, reservation.reservation_digest):
            raise AuthenticationError("invitation redemption source binding mismatch")
        updated = connection.execute(
            """UPDATE invitation_links
               SET state='consumed',state_reason='redemption_completed',use_count=1,
                   revision=revision+1,updated_at=?,consumed_at=?
             WHERE invitation_id=? AND state='reserved' AND use_count=0 AND revision=?
               AND reservation_id=? AND reservation_digest=? AND expires_at>?""",
            (
                now,
                now,
                reservation.invitation_id,
                reservation.revision,
                reservation.reservation_id,
                reservation.reservation_digest,
                now,
            ),
        )
        if updated.rowcount != 1:
            raise ConflictError("invitation redemption lost its single-use fence")
        self.store.append_audit(
            connection,
            {
                "action": "invitation_link.consumed",
                "invitation_id": reservation.invitation_id,
                "offer_digest": offer.digest,
                "reservation_digest": reservation.reservation_digest,
                "principal_id": principal_id,
                "harness_id": harness_id,
                "use_count": 1,
            },
        )
        final = connection.execute(
            "SELECT * FROM invitation_links WHERE invitation_id=?",
            (reservation.invitation_id,),
        ).fetchone()
        if final is None:
            raise GateBlocked("invitation_link", "consumed invitation is unavailable")
        return self._record_from_row(final)

    def _require_sponsor(self, connection: Any, *, actor: VerifiedActor, domain_id: str, now: int) -> None:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.domain_id != domain_id
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.credential_epoch < 1
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise AuthorizationError("invitation sponsorship requires a verified human harness")
        binding = load_credential_binding_from_connection(connection, actor.credential_id)
        binding.require_active(now=now)
        if (
            binding.domain_id != actor.domain_id
            or binding.principal_id != actor.principal_id
            or binding.harness_id != actor.harness_id
            or binding.credential_epoch != actor.credential_epoch
        ):
            raise AuthorizationError("invitation sponsor credential binding mismatch")

    def _require_exact_scope(
        self,
        connection: Any,
        *,
        offer: InvitationOffer,
        actor: VerifiedActor | None,
        now: int,
        require_administrator: bool,
    ) -> Any:
        proposal = offer.collaboration_scope_template
        row = connection.execute(
            "SELECT * FROM collaboration_scopes WHERE scope_id=?", (proposal.scope_id,)
        ).fetchone()
        if (
            row is None
            or row["domain_id"] != offer.domain_id
            or row["state"] != "active"
            or (row["expires_at"] is not None and now >= int(row["expires_at"]))
            or (
                row["expires_at"] is not None
                and offer.expires_at > int(row["expires_at"])
            )
            or int(row["policy_revision"]) != proposal.policy_revision
            or int(row["domain_revocation_epoch"]) != proposal.domain_revocation_epoch
            or row["scope_kind"] != proposal.scope_kind
            or (
                None if row["expires_at"] is None else int(row["expires_at"])
            ) != proposal.expires_at
        ):
            raise AuthorizationError("invitation collaboration scope is unavailable")
        expected_json = (
            ("allowed_actions_json", list(proposal.allowed_actions)),
            ("allowed_resource_prefixes_json", list(proposal.allowed_resource_prefixes)),
            (
                "allowed_classifications_json",
                [item.value if hasattr(item, "value") else item for item in proposal.allowed_classifications],
            ),
            ("canonical_references_json", list(proposal.canonical_references)),
        )
        for column, expected in expected_json:
            try:
                actual = json.loads(row[column])
            except Exception as exc:
                raise GateBlocked("invitation_link", "collaboration scope record is invalid") from exc
            if actual != expected:
                raise AuthorizationError("invitation collaboration scope changed")
        members = connection.execute(
            "SELECT harness_id FROM collaboration_scope_members WHERE scope_id=? AND state='active' ORDER BY harness_id",
            (proposal.scope_id,),
        ).fetchall()
        if tuple(str(member["harness_id"]) for member in members) != proposal.member_harness_ids:
            raise AuthorizationError("invitation collaboration membership changed")
        if require_administrator:
            if actor is None:
                raise AuthorizationError(
                    "invitation scope administrator binding is unavailable"
                )
            membership = connection.execute(
                """SELECT role FROM collaboration_scope_members
                   WHERE scope_id=? AND authority_kind='principal'
                     AND authority_id=? AND harness_id=? AND state='active'""",
                (proposal.scope_id, actor.principal_id, actor.harness_id),
            ).fetchone()
            if membership is None or membership["role"] not in {"owner", "administrator"}:
                raise AuthorizationError("invitation sponsor is not a scope administrator")
        return row

    def _offer_from_row(self, row: Any) -> InvitationOffer:
        invitation_id = str(row["invitation_id"])
        value = self.store.cipher.decrypt_json(
            str(row["encrypted_offer"]), purpose=f"invitation-link-offer:{invitation_id}"
        )
        email = self.store.cipher.decrypt_json(
            str(row["invited_email_encrypted"]), purpose=f"invitation-link-email:{invitation_id}"
        )
        if not isinstance(value, dict) or not isinstance(email, str):
            raise GateBlocked("invitation_link", "encrypted invitation offer is invalid")
        try:
            offer = InvitationOffer.model_validate({**value, "invited_verified_email": email})
        except Exception as exc:
            raise GateBlocked("invitation_link", "encrypted invitation offer is invalid") from exc
        if (
            offer.invitation_id != invitation_id
            or offer.domain_id != row["domain_id"]
            or offer.collaboration_scope_template.scope_id != row["destination_scope_id"]
            or not secrets.compare_digest(offer.digest, str(row["offer_digest"]))
            or not secrets.compare_digest(
                hashlib.sha256(email.encode("utf-8")).hexdigest(),
                str(row["invited_email_sha256"]),
            )
        ):
            raise GateBlocked("invitation_link", "invitation offer authentication failed")
        return offer

    def _record_from_row(self, row: Any) -> InvitationRecord:
        return InvitationRecord(
            offer=self._offer_from_row(row),
            state=row["state"],
            state_reason=row["state_reason"],
            revision=int(row["revision"]),
            use_count=int(row["use_count"]),
            reservation_id=row["reservation_id"],
            reservation_digest=row["reservation_digest"],
            reserved_at=row["reserved_at"],
            created_at=int(row["created_at"]),
            updated_at=int(row["updated_at"]),
            consumed_at=row["consumed_at"],
            revoked_at=row["revoked_at"],
        )

    @staticmethod
    def _reservation_digest(
        *,
        invitation_id: str,
        reservation_id: str,
        offer_digest: str,
        source_fingerprint: str,
        reserved_at: int,
        expires_at: int,
    ) -> str:
        return canonical_digest(
            {
                "schema": "agentnet.invitation-redemption-reservation.v1",
                "invitation_id": invitation_id,
                "reservation_id": reservation_id,
                "offer_digest": offer_digest,
                "source_fingerprint": source_fingerprint,
                "reserved_at": reserved_at,
                "expires_at": expires_at,
            }
        )

    @staticmethod
    def _reservation_from_row(row: Any, offer: InvitationOffer) -> InvitationRedemptionReservation:
        if row["reservation_id"] is None or row["reservation_digest"] is None or row["reserved_at"] is None:
            raise GateBlocked("invitation_link", "invitation reservation is incomplete")
        return InvitationRedemptionReservation(
            invitation_id=offer.invitation_id,
            reservation_id=row["reservation_id"],
            reservation_digest=row["reservation_digest"],
            offer_digest=offer.digest,
            destination_scope_id=offer.collaboration_scope_template.scope_id,
            permission_actions=offer.permission_actions,
            expires_at=offer.expires_at,
            reserved_at=int(row["reserved_at"]),
            revision=int(row["revision"]),
        )

    def _expire_due(self, *, now: int) -> None:
        with self.store.transaction() as connection:
            self._expire_due_connection(connection, now=now)

    def _expire_due_connection(self, connection: Any, *, now: int) -> None:
        due = connection.execute(
            "SELECT invitation_id,offer_digest FROM invitation_links WHERE state IN ('issued','reserved') AND use_count=0 AND expires_at<=?",
            (now,),
        ).fetchall()
        for row in due:
            updated = connection.execute(
                """UPDATE invitation_links
                   SET state='expired',state_reason='expired',revision=revision+1,updated_at=?
                 WHERE invitation_id=? AND state IN ('issued','reserved') AND use_count=0 AND expires_at<=?""",
                (now, row["invitation_id"], now),
            )
            if updated.rowcount == 1:
                self.store.append_audit(
                    connection,
                    {
                        "action": "invitation_link.expired",
                        "invitation_id": row["invitation_id"],
                        "offer_digest": row["offer_digest"],
                        "expired_at": now,
                    },
                )

    def _require_not_locked_connection(
        self,
        connection: Any,
        *,
        invitation_id: str,
        source_fingerprint: str,
        now: int,
    ) -> None:
        failure = connection.execute(
            "SELECT locked_until FROM invitation_link_failures WHERE invitation_id=? AND source_fingerprint=?",
            (invitation_id, source_fingerprint),
        ).fetchone()
        if failure is not None and failure["locked_until"] is not None and now < int(failure["locked_until"]):
            raise InvitationUnavailable()

    @staticmethod
    def _validate_source(source_fingerprint: str) -> None:
        if not _SHA256.fullmatch(source_fingerprint):
            raise ValidationError("invitation source fingerprint must be a SHA-256 digest")


def _token_hash(token: str) -> str:
    return hashlib.sha256(token.encode("ascii")).hexdigest()


def _public_token_hash(token: str) -> str | None:
    if not isinstance(token, str) or not token or len(token) > 512:
        return None
    try:
        encoded = token.encode("ascii")
    except UnicodeEncodeError:
        return None
    return hashlib.sha256(encoded).hexdigest()


def invitation_qr_svg(public_url: str) -> str:
    """Render locally; absence of the pinned renderer fails closed."""

    try:
        import segno
    except ImportError as exc:  # pragma: no cover - packaging gate owns dependency closure
        raise GateBlocked(
            "invitation_qr",
            "the configured local QR renderer is unavailable",
        ) from exc
    output = io.BytesIO()
    segno.make(public_url, error="m").save(
        output,
        kind="svg",
        scale=4,
        border=4,
        xmldecl=False,
        svgns=True,
    )
    svg = output.getvalue().decode("utf-8")
    if "<svg" not in svg or "<script" in svg.casefold():
        raise GateBlocked("invitation_qr", "the local QR renderer returned invalid SVG")
    return svg


__all__ = [
    "INVITATION_LINK_ISSUE_ACTION",
    "INVITATION_LINK_REVOKE_ACTION",
    "INVITATION_LINK_TTL_SECONDS",
    "InvitationLinkService",
    "InvitationOffer",
    "InvitationRecord",
    "InvitationRedemptionReservation",
    "InvitationUnavailable",
    "IssuedInvitationLink",
    "PublicInvitationSummary",
    "invitation_qr_svg",
]
