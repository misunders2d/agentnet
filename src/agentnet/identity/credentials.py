"""Credential records and exact public-key binding checks."""

from __future__ import annotations

import hashlib
import time
from dataclasses import dataclass
from typing import Any, Callable
from uuid import NAMESPACE_URL, uuid4, uuid5

from cryptography.hazmat.primitives import serialization
from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.security.signatures import b64url_encode, load_public_key, verify_signature
from agentnet.storage.backend import StoreBackend


_BINDING_QUERY = """
SELECT
  c.credential_id, c.key_id, c.public_key_pem, c.status AS credential_status,
  c.epoch AS credential_epoch, c.not_before, c.expires_at,
  h.harness_id, h.domain_id, h.principal_id, h.guest_id, h.status AS harness_status,
  h.binding_assurance, h.credential_epoch AS harness_credential_epoch,
  p.status AS principal_status,
  g.status AS guest_status, g.host_domain_id AS guest_host_domain_id,
  g.expires_at AS guest_expires_at,
  d.status AS domain_status, d.revocation_epoch AS domain_revocation_epoch
FROM credentials AS c
JOIN harnesses AS h ON h.harness_id = c.harness_id
LEFT JOIN principals AS p ON p.principal_id = h.principal_id
LEFT JOIN guests AS g ON g.guest_id = h.guest_id
JOIN domains AS d ON d.domain_id = h.domain_id
WHERE c.credential_id = ?
"""


@dataclass(frozen=True, slots=True)
class CredentialBinding:
    credential_id: str
    key_id: str
    public_key_pem: str
    credential_status: str
    credential_epoch: int
    not_before: int
    expires_at: int
    harness_id: str
    domain_id: str
    principal_id: str | None
    guest_id: str | None
    harness_status: str
    binding_assurance: str
    harness_credential_epoch: int
    principal_status: str | None
    guest_status: str | None
    guest_host_domain_id: str | None
    guest_expires_at: int | None
    domain_status: str
    domain_revocation_epoch: int

    def require_active(self, *, now: int) -> None:
        if self.domain_status != "active":
            raise AuthenticationError("credential authority is unavailable")
        if self.guest_id is None:
            if self.principal_id is None or self.principal_status != "active":
                raise AuthenticationError("credential principal authority is unavailable")
        else:
            if self.principal_id is not None:
                raise AuthenticationError("guest credential cannot inherit a host principal")
            if (
                self.guest_status != "active"
                or self.guest_host_domain_id != self.domain_id
                or self.guest_expires_at is None
                or now >= self.guest_expires_at
            ):
                raise AuthenticationError("guest credential authority is unavailable")
        if self.harness_status not in {"active", "deterministic_only"}:
            raise AuthenticationError("harness credential is unavailable")
        if self.credential_status != "active":
            raise AuthenticationError("credential is unavailable")
        if self.credential_epoch != self.harness_credential_epoch:
            raise AuthenticationError("credential epoch is stale")
        if now < self.not_before or now >= self.expires_at:
            raise AuthenticationError("credential is outside its validity interval")


def public_key_thumbprint(public_key_pem: str) -> str:
    key = load_public_key(public_key_pem)
    der = key.public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)
    return b64url_encode(hashlib.sha256(der).digest())


def load_credential_binding(store: StoreBackend, credential_id: str) -> CredentialBinding:
    row = store.fetch_one(_BINDING_QUERY, (credential_id,))
    if row is None:
        raise AuthenticationError("credential binding is unavailable")
    return credential_binding_from_row(row)


def load_credential_binding_from_connection(connection: Any, credential_id: str) -> CredentialBinding:
    row = connection.execute(_BINDING_QUERY, (credential_id,)).fetchone()
    if row is None:
        raise AuthenticationError("credential binding is unavailable")
    return credential_binding_from_row(row)


def credential_binding_from_row(row: Any) -> CredentialBinding:
    return CredentialBinding(
        credential_id=row["credential_id"],
        key_id=row["key_id"],
        public_key_pem=row["public_key_pem"],
        credential_status=row["credential_status"],
        credential_epoch=row["credential_epoch"],
        not_before=row["not_before"],
        expires_at=row["expires_at"],
        harness_id=row["harness_id"],
        domain_id=row["domain_id"],
        principal_id=row["principal_id"],
        guest_id=row["guest_id"],
        harness_status=row["harness_status"],
        binding_assurance=row["binding_assurance"],
        harness_credential_epoch=row["harness_credential_epoch"],
        principal_status=row["principal_status"],
        guest_status=row["guest_status"],
        guest_host_domain_id=row["guest_host_domain_id"],
        guest_expires_at=row["guest_expires_at"],
        domain_status=row["domain_status"],
        domain_revocation_epoch=row["domain_revocation_epoch"],
    )


CREDENTIAL_ROTATION_POP_PURPOSE = "agentnet.credential.rotation.pop.v1"


class CredentialRotationRequest(BaseModel):
    """New key material for the already authenticated current harness.

    Domain, principal, harness, and current credential identifiers are
    deliberately absent.  They come only from the verified request proof.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str = Field(default_factory=lambda: str(uuid4()), min_length=36, max_length=36)
    expected_credential_epoch: int = Field(ge=1)
    new_public_key_pem: str = Field(min_length=128, max_length=16_384)
    new_key_possession_signature: str = Field(min_length=1, max_length=2_048)

    @staticmethod
    def possession_fields(
        *,
        request_id: str,
        actor: VerifiedActor,
        expected_credential_epoch: int,
        new_key_id: str,
    ) -> dict[str, object]:
        if actor.harness_id is None or actor.credential_id is None:
            raise AuthenticationError("credential rotation requires a credential-bound harness actor")
        return {
            "schema": "agentnet.credential.rotation.pop.v1",
            "request_id": request_id,
            "domain_id": actor.domain_id,
            "harness_id": actor.harness_id,
            "current_credential_id": actor.credential_id,
            "expected_credential_epoch": expected_credential_epoch,
            "new_key_id": new_key_id,
        }

    def signed_fields(self, *, actor: VerifiedActor) -> dict[str, object]:
        return self.possession_fields(
            request_id=self.request_id,
            actor=actor,
            expected_credential_epoch=self.expected_credential_epoch,
            new_key_id=public_key_thumbprint(self.new_public_key_pem),
        )


class CredentialRotationResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str
    harness_id: str
    credential_id: str
    key_id: str
    credential_epoch: int = Field(ge=2)
    not_before: int
    expires_at: int


class CredentialRotationService:
    """Rotate one current harness credential without expanding its authority."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        credential_ttl_seconds: int = 3_600,
        outage_gate: OutageGate | None = None,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if credential_ttl_seconds < 60 or credential_ttl_seconds > 86_400:
            raise ValueError("credential rotation TTL must be between 60 seconds and 24 hours")
        self.store = store
        self.credential_ttl_seconds = credential_ttl_seconds
        self.outage_gate = outage_gate
        self.clock = clock or (lambda: int(time.time()))

    def rotate(
        self,
        *,
        actor: VerifiedActor,
        request: CredentialRotationRequest,
        now: int | None = None,
    ) -> CredentialRotationResult:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError("only an authenticated current harness may rotate its credential")
        if actor.harness_id is None or actor.credential_id is None or actor.credential_epoch is None:
            raise AuthenticationError("credential rotation requires a credential-bound harness actor")
        current_time = self.clock() if now is None else now
        if current_time < 0:
            raise ValidationError("credential rotation time is invalid")

        new_key_id = public_key_thumbprint(request.new_public_key_pem)
        signed_fields = request.possession_fields(
            request_id=request.request_id,
            actor=actor,
            expected_credential_epoch=request.expected_credential_epoch,
            new_key_id=new_key_id,
        )
        verify_signature(
            request.new_public_key_pem,
            CREDENTIAL_ROTATION_POP_PURPOSE,
            signed_fields,
            request.new_key_possession_signature,
        )

        with self.store.transaction() as connection:
            current = load_credential_binding_from_connection(connection, actor.credential_id)
            current.require_active(now=current_time)
            if (
                current.domain_id != actor.domain_id
                or current.harness_id != actor.harness_id
                or current.credential_id != actor.credential_id
                or current.credential_epoch != actor.credential_epoch
                or current.harness_credential_epoch != request.expected_credential_epoch
                or actor.credential_epoch != request.expected_credential_epoch
            ):
                raise ConflictError("credential rotation fencing epoch or authenticated binding changed")
            if new_key_id == current.key_id:
                raise ValidationError("credential rotation requires a distinct new key")
            reused_key = connection.execute(
                "SELECT credential_id FROM credentials WHERE key_id=? LIMIT 1",
                (new_key_id,),
            ).fetchone()
            if reused_key is not None:
                raise ConflictError("credential rotation key is already registered")

            next_epoch = request.expected_credential_epoch + 1
            expires_at = current_time + self.credential_ttl_seconds
            if current.guest_expires_at is not None:
                expires_at = min(expires_at, int(current.guest_expires_at))
            if expires_at - current_time < 60:
                raise AuthorizationError("credential authority expires too soon to rotate")
            credential_id = str(
                uuid5(
                    NAMESPACE_URL,
                    f"agentnet:credential-rotation:{current.harness_id}:{request.request_id}:{next_epoch}:{new_key_id}",
                )
            )
            if connection.execute(
                "SELECT credential_id FROM credentials WHERE credential_id=?",
                (credential_id,),
            ).fetchone() is not None:
                raise ConflictError("credential rotation request was already committed")

            harness_update = connection.execute(
                """UPDATE harnesses SET credential_epoch=?
                     WHERE harness_id=? AND domain_id=? AND credential_epoch=?
                       AND status IN ('active','deterministic_only')""",
                (
                    next_epoch,
                    current.harness_id,
                    current.domain_id,
                    request.expected_credential_epoch,
                ),
            )
            if harness_update.rowcount != 1:
                raise ConflictError("credential rotation harness epoch changed before commit")
            retired = connection.execute(
                """UPDATE credentials SET status='retired'
                     WHERE credential_id=? AND harness_id=? AND epoch=? AND status='active'""",
                (
                    current.credential_id,
                    current.harness_id,
                    request.expected_credential_epoch,
                ),
            )
            if retired.rowcount != 1:
                raise ConflictError("credential rotation current key changed before commit")
            connection.execute(
                """INSERT INTO credentials(
                       credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                   ) VALUES(?,?,?,?,'active',?,?,?)""",
                (
                    credential_id,
                    current.harness_id,
                    new_key_id,
                    request.new_public_key_pem,
                    next_epoch,
                    current_time,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "credential.rotated",
                    "domain_id": current.domain_id,
                    "harness_id": current.harness_id,
                    "principal_id": current.principal_id,
                    "guest_id": current.guest_id,
                    "request_id": request.request_id,
                    "old_credential_id": current.credential_id,
                    "new_credential_id": credential_id,
                    "new_key_id": new_key_id,
                    "previous_credential_epoch": request.expected_credential_epoch,
                    "new_credential_epoch": next_epoch,
                    "not_before": current_time,
                    "expires_at": expires_at,
                },
            )

        return CredentialRotationResult(
            request_id=request.request_id,
            harness_id=current.harness_id,
            credential_id=credential_id,
            key_id=new_key_id,
            credential_epoch=next_epoch,
            not_before=current_time,
            expires_at=expires_at,
        )


__all__ = [
    "CREDENTIAL_ROTATION_POP_PURPOSE",
    "CredentialBinding",
    "CredentialRotationRequest",
    "CredentialRotationResult",
    "CredentialRotationService",
    "credential_binding_from_row",
    "load_credential_binding",
    "load_credential_binding_from_connection",
    "public_key_thumbprint",
]
