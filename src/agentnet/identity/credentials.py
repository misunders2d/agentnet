"""Credential records and exact public-key binding checks."""

from __future__ import annotations

import hashlib
import json
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Callable, Literal, Mapping
from uuid import NAMESPACE_URL, uuid4, uuid5

from cryptography.hazmat.primitives import serialization
from pydantic import BaseModel, ConfigDict, Field

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.operations.c0_credential_supersession import verify_recovery_provenance
from agentnet.security.signatures import (
    b64url_encode,
    canonical_json,
    canonical_digest,
    load_public_key,
    verify_signature,
)
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

MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE = (
    "identity.credential.recover.approve"
)
MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE = (
    "agentnet.managed-server-credential-reauthorization.pop.v1"
)
MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_SCHEMA = (
    "agentnet.managed-server-credential-reauthorization.v1"
)
MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_SCHEMA_V2 = (
    "agentnet.managed-server-credential-reauthorization.v2"
)


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


class ManagedServerCredentialReauthorizationRequest(BaseModel):
    """Owner-approved recovery of one lapsed, still-possessed managed-server key.

    This is deliberately not generic lost-key recovery.  The exact expired
    binding, managed config/identity bytes, and old-key possession are all
    frozen into the independently reviewed transaction.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.managed-server-credential-reauthorization.v1"] = Field(
        default=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_SCHEMA,
        alias="schema",
    )
    request_id: str = Field(pattern=r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    principal_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    expired_credential_id: str = Field(min_length=1, max_length=256)
    expected_credential_epoch: int = Field(ge=1)
    expected_expired_at: int = Field(ge=1)
    expected_key_id: str = Field(min_length=16, max_length=256)
    expected_binding_assurance: Literal["os_bound", "hardware_bound"]
    managed_config_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    managed_identity_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    maximum_new_credential_ttl_seconds: int = Field(ge=3_600, le=604_800)
    old_key_possession_signature: str = Field(min_length=1, max_length=2_048)

    def transaction_fields(self) -> dict[str, object]:
        return {
            "schema": self.schema_version,
            "approval_purpose": MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "principal_id": self.principal_id,
            "harness_id": self.harness_id,
            "expired_credential_id": self.expired_credential_id,
            "expected_credential_epoch": self.expected_credential_epoch,
            "expected_expired_at": self.expected_expired_at,
            "expected_key_id": self.expected_key_id,
            "expected_binding_assurance": self.expected_binding_assurance,
            "managed_config_sha256": self.managed_config_sha256,
            "managed_identity_sha256": self.managed_identity_sha256,
            "maximum_new_credential_ttl_seconds": self.maximum_new_credential_ttl_seconds,
            "managed_profile": "always_on_server_agent",
            "key_binding": "same_managed_key_with_fresh_possession_proof",
            "old_credential_action": "retire_without_extension",
            "authority_granted": False,
        }

    @property
    def canonical_transaction(self) -> bytes:
        return canonical_json(self.transaction_fields())

    def possession_fields(self) -> dict[str, object]:
        return {
            "schema": MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "principal_id": self.principal_id,
            "harness_id": self.harness_id,
            "expired_credential_id": self.expired_credential_id,
            "expected_credential_epoch": self.expected_credential_epoch,
            "expected_key_id": self.expected_key_id,
            "transaction_sha256": hashlib.sha256(self.canonical_transaction).hexdigest(),
        }


class ManagedServerCredentialReauthorizationRequestV2(BaseModel):
    """Post-C0 recovery bound to immutable terminal and journal provenance."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal[
        "agentnet.managed-server-credential-reauthorization.v2"
    ] = Field(
        default=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_SCHEMA_V2,
        alias="schema",
    )
    request_id: str = Field(pattern=r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    principal_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    expired_credential_id: str = Field(min_length=1, max_length=256)
    expected_credential_epoch: int = Field(ge=1)
    expected_expired_at: int = Field(ge=1)
    expected_key_id: str = Field(min_length=16, max_length=256)
    expected_binding_assurance: Literal["os_bound", "hardware_bound"]
    managed_config_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    managed_identity_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    maximum_new_credential_ttl_seconds: int = Field(ge=3_600, le=604_800)
    c0_terminal_credential_epoch: int = Field(ge=1)
    c0_terminal_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    prior_supersession_journal_sha256: str | None = Field(
        default=None,
        pattern=r"^[a-f0-9]{64}$",
    )
    old_key_possession_signature: str = Field(min_length=1, max_length=2_048)

    def transaction_fields(self) -> dict[str, object]:
        return {
            "schema": self.schema_version,
            "approval_purpose": MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "principal_id": self.principal_id,
            "harness_id": self.harness_id,
            "expired_credential_id": self.expired_credential_id,
            "expected_credential_epoch": self.expected_credential_epoch,
            "expected_expired_at": self.expected_expired_at,
            "expected_key_id": self.expected_key_id,
            "c0_terminal_credential_epoch": self.c0_terminal_credential_epoch,
            "expected_binding_assurance": self.expected_binding_assurance,
            "managed_config_sha256": self.managed_config_sha256,
            "managed_identity_sha256": self.managed_identity_sha256,
            "maximum_new_credential_ttl_seconds": self.maximum_new_credential_ttl_seconds,
            "c0_terminal_sha256": self.c0_terminal_sha256,
            "prior_supersession_journal_sha256": self.prior_supersession_journal_sha256,
            "managed_profile": "always_on_server_agent",
            "key_binding": "same_managed_key_with_fresh_possession_proof",
            "old_credential_action": "retire_without_extension",
            "authority_granted": False,
        }

    @property
    def canonical_transaction(self) -> bytes:
        return canonical_json(self.transaction_fields())

    def possession_fields(self) -> dict[str, object]:
        return {
            "schema": MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
            "request_id": self.request_id,
            "domain_id": self.domain_id,
            "principal_id": self.principal_id,
            "harness_id": self.harness_id,
            "expired_credential_id": self.expired_credential_id,
            "expected_credential_epoch": self.expected_credential_epoch,
            "expected_key_id": self.expected_key_id,
            "transaction_sha256": hashlib.sha256(self.canonical_transaction).hexdigest(),
        }


class ManagedServerCredentialReauthorizationResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.managed-server-credential-reauthorization-result.v1"] = Field(
        default="agentnet.managed-server-credential-reauthorization-result.v1",
        alias="schema",
    )
    request_id: str
    domain_id: str
    principal_id: str
    harness_id: str
    previous_credential_id: str
    credential_id: str
    key_id: str
    credential_epoch: int = Field(ge=2)
    not_before: int
    expires_at: int
    idempotent_repeat: bool
    authority_granted: Literal[False] = False


class ManagedServerCredentialReauthorizationResultV2(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal[
        "agentnet.managed-server-credential-reauthorization-result.v2"
    ] = Field(
        default="agentnet.managed-server-credential-reauthorization-result.v2",
        alias="schema",
    )
    request_id: str
    domain_id: str
    principal_id: str
    harness_id: str
    previous_credential_id: str
    credential_id: str
    key_id: str
    credential_epoch: int = Field(ge=2)
    not_before: int
    expires_at: int
    audit_record_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    idempotent_repeat: bool
    authority_granted: Literal[False] = False


ManagedServerCredentialReauthorizationRequestAny = (
    ManagedServerCredentialReauthorizationRequest
    | ManagedServerCredentialReauthorizationRequestV2
)
ManagedServerCredentialReauthorizationResultAny = (
    ManagedServerCredentialReauthorizationResult
    | ManagedServerCredentialReauthorizationResultV2
)


class ManagedServerCredentialReauthorizationService:
    """Atomically reauthorize one expired managed-server binding.

    The caller must hold the PostgreSQL runtime lease while Core is offline.
    ``PostgreSQLStore.transaction`` enforces that lease at the commit boundary;
    a test-only/non-production backend may be used by unit tests.
    """

    def __init__(
        self,
        store: StoreBackend,
        approval_verifier: IndependentApprovalVerifier,
        *,
        credential_ttl_seconds: int = 86_400,
        outage_gate: OutageGate | None = None,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not isinstance(approval_verifier, IndependentApprovalVerifier):
            raise ValueError("managed-server credential reauthorization requires independent approval")
        if not 3_600 <= credential_ttl_seconds <= 604_800:
            raise ValueError("managed-server credential TTL is outside the supported range")
        self.store = store
        self.approval_verifier = approval_verifier
        self.credential_ttl_seconds = credential_ttl_seconds
        self.outage_gate = outage_gate
        self.clock = clock or (lambda: int(time.time()))

    @staticmethod
    def _new_credential_id(request: ManagedServerCredentialReauthorizationRequestAny) -> str:
        transaction_sha256 = hashlib.sha256(request.canonical_transaction).hexdigest()
        return str(
            uuid5(
                NAMESPACE_URL,
                "agentnet:managed-server-credential-reauthorization:"
                f"{request.domain_id}:{request.harness_id}:{request.request_id}:{transaction_sha256}",
            )
        )

    def _idempotent_result(
        self,
        connection: Any,
        *,
        request: ManagedServerCredentialReauthorizationRequestAny,
        credential_id: str,
    ) -> ManagedServerCredentialReauthorizationResultAny | None:
        row = connection.execute(
            """SELECT c.credential_id,c.harness_id,c.key_id,c.public_key_pem,c.status,c.epoch,
                      c.not_before,c.expires_at,h.domain_id,h.principal_id,h.status AS harness_status,
                      h.binding_assurance,h.credential_epoch
                 FROM credentials c JOIN harnesses h ON h.harness_id=c.harness_id
                WHERE c.credential_id=?""",
            (credential_id,),
        ).fetchone()
        if row is None:
            return None
        old = connection.execute(
            "SELECT status,epoch,key_id,public_key_pem,expires_at FROM credentials WHERE credential_id=?",
            (request.expired_credential_id,),
        ).fetchone()
        if (
            old is None
            or old["status"] != "retired"
            or int(old["epoch"]) != request.expected_credential_epoch
            or old["key_id"] != request.expected_key_id
            or int(old["expires_at"]) != request.expected_expired_at
            or row["harness_id"] != request.harness_id
            or row["domain_id"] != request.domain_id
            or row["principal_id"] != request.principal_id
            or row["harness_status"] != "active"
            or row["binding_assurance"] != request.expected_binding_assurance
            or row["status"] != "active"
            or row["key_id"] != request.expected_key_id
            or row["public_key_pem"] != old["public_key_pem"]
            or public_key_thumbprint(str(row["public_key_pem"])) != request.expected_key_id
            or int(row["epoch"]) != request.expected_credential_epoch + 1
            or int(row["credential_epoch"]) != request.expected_credential_epoch + 1
            or int(row["expires_at"]) - int(row["not_before"]) != self.credential_ttl_seconds
        ):
            raise ConflictError("managed-server reauthorization request identifier conflicts")
        verify_signature(
            str(old["public_key_pem"]),
            MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
            request.possession_fields(),
            request.old_key_possession_signature,
        )
        result_fields = {
            "request_id": request.request_id,
            "domain_id": request.domain_id,
            "principal_id": request.principal_id,
            "harness_id": request.harness_id,
            "previous_credential_id": request.expired_credential_id,
            "credential_id": credential_id,
            "key_id": request.expected_key_id,
            "credential_epoch": request.expected_credential_epoch + 1,
            "not_before": int(row["not_before"]),
            "expires_at": int(row["expires_at"]),
            "idempotent_repeat": True,
        }
        if isinstance(request, ManagedServerCredentialReauthorizationRequestV2):
            transaction_digest = hashlib.sha256(request.canonical_transaction).hexdigest()
            audit_hash: str | None = None
            audit_rows = connection.execute(
                "SELECT record_hash,record_json FROM audit_log ORDER BY sequence"
            ).fetchall()
            for audit_row in audit_rows:
                try:
                    record = json.loads(str(audit_row["record_json"]))
                except (TypeError, ValueError):
                    continue
                if (
                    isinstance(record, dict)
                    and record.get("action") == "credential.managed_server_reauthorized"
                    and record.get("request_id") == request.request_id
                    and record.get("transaction_digest") == transaction_digest
                    and record.get("c0_terminal_sha256") == request.c0_terminal_sha256
                    and record.get("c0_supersession_sha256")
                    == request.prior_supersession_journal_sha256
                ):
                    audit_hash = str(audit_row["record_hash"])
                    break
            if audit_hash is None:
                raise ConflictError("managed-server reauthorization audit record is missing")
            return ManagedServerCredentialReauthorizationResultV2(
                **result_fields,
                audit_record_hash=audit_hash,
            )
        return ManagedServerCredentialReauthorizationResult(**result_fields)

    def reauthorize(
        self,
        *,
        request: ManagedServerCredentialReauthorizationRequestAny,
        approval: Mapping[str, Any],
        c0_terminal_raw: bytes | None = None,
        c0_supersession_journal_raw: bytes | None = None,
    ) -> ManagedServerCredentialReauthorizationResultAny:
        now = self.clock()
        if now < 0:
            raise ValidationError("managed-server credential reauthorization time is invalid")
        if request.maximum_new_credential_ttl_seconds != self.credential_ttl_seconds:
            raise ConflictError("approved managed-server credential TTL changed")
        credential_id = self._new_credential_id(request)
        if isinstance(request, ManagedServerCredentialReauthorizationRequestV2):
            if c0_terminal_raw is None:
                raise ValidationError("managed-server reauthorization C0 terminal is missing")
            verify_recovery_provenance(
                self.store,
                terminal_raw=c0_terminal_raw,
                journal_raw=c0_supersession_journal_raw,
                domain_id=request.domain_id,
                principal_id=request.principal_id,
                harness_id=request.harness_id,
                expected_previous_credential_id=request.expired_credential_id,
                expected_previous_credential_epoch=request.expected_credential_epoch,
                c0_terminal_credential_epoch=request.c0_terminal_credential_epoch,
                c0_terminal_sha256=request.c0_terminal_sha256,
                prior_journal_sha256=request.prior_supersession_journal_sha256,
            )

        with self.store.transaction() as connection:
            repeated = self._idempotent_result(
                connection,
                request=request,
                credential_id=credential_id,
            )
            if repeated is not None:
                return repeated

            current = load_credential_binding_from_connection(
                connection,
                request.expired_credential_id,
            )
            if (
                current.domain_id != request.domain_id
                or current.principal_id != request.principal_id
                or current.guest_id is not None
                or current.harness_id != request.harness_id
                or current.credential_id != request.expired_credential_id
                or current.credential_epoch != request.expected_credential_epoch
                or current.harness_credential_epoch != request.expected_credential_epoch
                or current.expires_at != request.expected_expired_at
                or current.key_id != request.expected_key_id
                or current.binding_assurance != request.expected_binding_assurance
            ):
                raise ConflictError("managed-server expired binding changed")
            if (
                current.domain_status != "active"
                or current.principal_status != "active"
                or current.harness_status != "active"
                or current.credential_status != "active"
            ):
                raise AuthorizationError("managed-server expired binding is not eligible for reauthorization")
            if now < current.expires_at:
                raise AuthorizationError("managed-server credential is not expired")
            if public_key_thumbprint(current.public_key_pem) != request.expected_key_id:
                raise AuthenticationError("managed-server credential key binding is invalid")
            verify_signature(
                current.public_key_pem,
                MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                request.possession_fields(),
                request.old_key_possession_signature,
            )
            verified = self.approval_verifier.verify(
                canonical_transaction=request.canonical_transaction,
                approval=approval,
                expected_purpose=MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
                expected_domain_id=request.domain_id,
                when=datetime.fromtimestamp(now, UTC),
            )
            if (
                verified.approver_authority_kind != "human"
                or verified.approver_principal_id != request.principal_id
            ):
                raise AuthorizationError("managed-server reauthorization requires the configured owner")
            if self.outage_gate is not None:
                self.outage_gate.require_issuance()

            next_epoch = request.expected_credential_epoch + 1
            expires_at = now + self.credential_ttl_seconds
            harness_update = connection.execute(
                """UPDATE harnesses SET credential_epoch=?
                     WHERE harness_id=? AND domain_id=? AND principal_id=?
                       AND credential_epoch=? AND status='active'""",
                (
                    next_epoch,
                    request.harness_id,
                    request.domain_id,
                    request.principal_id,
                    request.expected_credential_epoch,
                ),
            )
            if harness_update.rowcount != 1:
                raise ConflictError("managed-server harness epoch changed before commit")
            retired = connection.execute(
                """UPDATE credentials SET status='retired'
                     WHERE credential_id=? AND harness_id=? AND epoch=?
                       AND status='active' AND expires_at=?""",
                (
                    request.expired_credential_id,
                    request.harness_id,
                    request.expected_credential_epoch,
                    request.expected_expired_at,
                ),
            )
            if retired.rowcount != 1:
                raise ConflictError("managed-server expired credential changed before commit")
            connection.execute(
                """INSERT INTO credentials(
                       credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                   ) VALUES(?,?,?,?,'active',?,?,?)""",
                (
                    credential_id,
                    request.harness_id,
                    request.expected_key_id,
                    current.public_key_pem,
                    next_epoch,
                    now,
                    expires_at,
                ),
            )
            consume_independent_approval(connection, receipt=verified)
            if isinstance(request, ManagedServerCredentialReauthorizationRequestV2):
                audit_record = {
                    "action": "credential.managed_server_reauthorized",
                    "request_id": request.request_id,
                    "domain_id": request.domain_id,
                    "principal_id": request.principal_id,
                    "harness_id": request.harness_id,
                    "old_credential_id": request.expired_credential_id,
                    "new_credential_id": credential_id,
                    "key_id": request.expected_key_id,
                    "previous_credential_epoch": request.expected_credential_epoch,
                    "new_credential_epoch": next_epoch,
                    "not_before": now,
                    "expires_at": expires_at,
                    "approval_receipt_id": verified.receipt_id,
                    "approval_receipt_digest": hashlib.sha256(
                        canonical_json(dict(approval))
                    ).hexdigest(),
                    "transaction_digest": verified.transaction_digest,
                    "c0_terminal_sha256": request.c0_terminal_sha256,
                    "terminal_credential_epoch": request.c0_terminal_credential_epoch,
                    "c0_supersession_sha256": request.prior_supersession_journal_sha256,
                }
            else:
                audit_record = {
                    "action": "credential.managed_server_reauthorized",
                    "domain_id": request.domain_id,
                    "principal_id": request.principal_id,
                    "harness_id": request.harness_id,
                    "request_id": request.request_id,
                    "approval_receipt_id": verified.receipt_id,
                    "old_credential_id": request.expired_credential_id,
                    "new_credential_id": credential_id,
                    "key_id": request.expected_key_id,
                    "previous_credential_epoch": request.expected_credential_epoch,
                    "new_credential_epoch": next_epoch,
                    "old_expires_at": request.expected_expired_at,
                    "not_before": now,
                    "expires_at": expires_at,
                    "managed_config_sha256": request.managed_config_sha256,
                    "managed_identity_sha256": request.managed_identity_sha256,
                    "authority_granted": False,
                }
            audit_record_hash = self.store.append_audit(connection, audit_record)

        result_fields = {
            "request_id": request.request_id,
            "domain_id": request.domain_id,
            "principal_id": request.principal_id,
            "harness_id": request.harness_id,
            "previous_credential_id": request.expired_credential_id,
            "credential_id": credential_id,
            "key_id": request.expected_key_id,
            "credential_epoch": next_epoch,
            "not_before": now,
            "expires_at": expires_at,
            "idempotent_repeat": False,
        }
        if isinstance(request, ManagedServerCredentialReauthorizationRequestV2):
            return ManagedServerCredentialReauthorizationResultV2(
                **result_fields,
                audit_record_hash=audit_record_hash,
            )
        return ManagedServerCredentialReauthorizationResult(**result_fields)


class CredentialRenewalRequest(BaseModel):
    """Selector-free idempotent renewal request for current signed actor."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.credential-renewal.v1"] = Field(
        default="agentnet.credential-renewal.v1", alias="schema"
    )
    request_id: str = Field(default_factory=lambda: str(uuid4()), min_length=36, max_length=36)


class CredentialRenewalResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.credential-renewal-result.v1"] = Field(
        default="agentnet.credential-renewal-result.v1", alias="schema"
    )
    status: Literal["current", "renewed"]
    expires_at: int


class CredentialRenewalService:
    """Extend one exact still-current server credential within bounded window."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        credential_ttl_seconds: int = 86_400,
        renewal_window_seconds: int = 21_600,
        outage_gate: OutageGate | None = None,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not 3_600 <= credential_ttl_seconds <= 604_800:
            raise ValueError("always-on credential TTL is outside the supported range")
        if not 300 <= renewal_window_seconds < credential_ttl_seconds:
            raise ValueError("credential renewal window is outside the supported range")
        self.store = store
        self.credential_ttl_seconds = credential_ttl_seconds
        self.renewal_window_seconds = renewal_window_seconds
        self.outage_gate = outage_gate
        self.clock = clock or (lambda: int(time.time()))

    def renew(
        self,
        *,
        actor: VerifiedActor,
        request: CredentialRenewalRequest,
    ) -> CredentialRenewalResult:
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise AuthorizationError("credential renewal requires current human harness actor")
        if actor.harness_id is None or actor.credential_id is None or actor.credential_epoch is None:
            raise AuthenticationError("credential renewal requires current credential binding")
        now = self.clock()
        if now < 0:
            raise ValidationError("credential renewal time is invalid")
        request_digest = canonical_digest(
            {
                "schema": request.schema_version,
                "request_id": request.request_id,
                "domain_id": actor.domain_id,
                "principal_id": actor.principal_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
                "credential_epoch": actor.credential_epoch,
            }
        )
        with self.store.transaction() as connection:
            current = load_credential_binding_from_connection(connection, actor.credential_id)
            current.require_active(now=now)
            if (
                current.domain_id != actor.domain_id
                or current.principal_id != actor.principal_id
                or current.harness_id != actor.harness_id
                or current.credential_id != actor.credential_id
                or current.credential_epoch != actor.credential_epoch
                or current.harness_credential_epoch != actor.credential_epoch
            ):
                raise ConflictError("credential renewal authenticated binding changed")
            previous = connection.execute(
                """SELECT request_digest,result_status,new_expires_at
                     FROM credential_renewal_requests WHERE request_id=?""",
                (request.request_id,),
            ).fetchone()
            if previous is not None:
                if previous["request_digest"] != request_digest:
                    raise ConflictError("credential renewal request identifier conflicts")
                return CredentialRenewalResult(
                    status=previous["result_status"],
                    expires_at=int(previous["new_expires_at"]),
                )
            if self.outage_gate is not None:
                self.outage_gate.require_issuance()
            old_expires_at = int(current.expires_at)
            in_window = old_expires_at - now <= self.renewal_window_seconds
            status = "renewed" if in_window else "current"
            new_expires_at = now + self.credential_ttl_seconds if in_window else old_expires_at
            if in_window:
                updated = connection.execute(
                    """UPDATE credentials SET expires_at=?
                         WHERE credential_id=? AND status='active' AND epoch=? AND expires_at=?""",
                    (
                        new_expires_at,
                        current.credential_id,
                        current.credential_epoch,
                        old_expires_at,
                    ),
                )
                if updated.rowcount != 1:
                    raise ConflictError("credential renewal validity changed concurrently")
            connection.execute(
                """INSERT INTO credential_renewal_requests(
                       request_id,request_digest,credential_id,result_status,
                       old_expires_at,new_expires_at,committed_at
                   ) VALUES(?,?,?,?,?,?,?)""",
                (
                    request.request_id,
                    request_digest,
                    current.credential_id,
                    status,
                    old_expires_at,
                    new_expires_at,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": f"credential.renewal.{status}",
                    "domain_id": current.domain_id,
                    "harness_id": current.harness_id,
                    "credential_id": current.credential_id,
                    "credential_epoch": current.credential_epoch,
                    "request_digest": request_digest,
                    "old_expires_at": old_expires_at,
                    "new_expires_at": new_expires_at,
                },
            )
        return CredentialRenewalResult(status=status, expires_at=new_expires_at)


__all__ = [
    "CREDENTIAL_ROTATION_POP_PURPOSE",
    "CredentialBinding",
    "CredentialRenewalRequest",
    "CredentialRenewalResult",
    "CredentialRenewalService",
    "CredentialRotationRequest",
    "CredentialRotationResult",
    "CredentialRotationService",
    "credential_binding_from_row",
    "load_credential_binding",
    "load_credential_binding_from_connection",
    "public_key_thumbprint",
]
