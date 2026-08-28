"""Narrow offline replacement for one recently expired server credential."""

from __future__ import annotations

import time
from typing import Callable, Literal
from uuid import NAMESPACE_URL, uuid5

from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.operations.outage import OutageGate
from agentnet.security.signatures import canonical_digest, verify_signature
from agentnet.storage.backend import StoreBackend


EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE = "agentnet.credential.expired-server-replacement.pop.v1"


class ExpiredServerReplacementRequest(BaseModel):
    """Frozen setup-bound retained-key proof for one expired server binding."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    request_id: str = Field(min_length=36, max_length=36)
    setup_request_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    expected_config_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    expected_expires_at: int = Field(ge=0)
    possession_signature: str = Field(min_length=1, max_length=2_048)

    @staticmethod
    def possession_fields(
        *,
        request_id: str,
        actor: VerifiedActor,
        setup_request_digest: str,
        expected_config_digest: str,
        expected_expires_at: int,
    ) -> dict[str, object]:
        if actor.principal_id is None or actor.harness_id is None or actor.credential_id is None:
            raise AuthenticationError("expired server replacement requires one exact human harness")
        return {
            "schema": EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE,
            "request_id": request_id,
            "domain_id": actor.domain_id,
            "principal_id": actor.principal_id,
            "harness_id": actor.harness_id,
            "credential_id": actor.credential_id,
            "credential_epoch": actor.credential_epoch,
            "setup_request_digest": setup_request_digest,
            "expected_config_digest": expected_config_digest,
            "expected_expires_at": expected_expires_at,
        }

    def signed_fields(self, actor: VerifiedActor) -> dict[str, object]:
        return self.possession_fields(
            request_id=self.request_id,
            actor=actor,
            setup_request_digest=self.setup_request_digest,
            expected_config_digest=self.expected_config_digest,
            expected_expires_at=self.expected_expires_at,
        )


class ExpiredServerReplacementResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["agentnet.credential.expired-server-replacement-result.v1"] = Field(
        default="agentnet.credential.expired-server-replacement-result.v1", alias="schema"
    )
    request_id: str
    old_credential_id: str
    credential_id: str
    credential_epoch: int = Field(ge=2)
    not_before: int
    expires_at: int


class ExpiredServerReplacementService:
    """Replace one active-but-expired non-lab server binding on the same harness/key."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        credential_ttl_seconds: int = 86_400,
        max_expiry_age_seconds: int = 86_400,
        outage_gate: OutageGate | None = None,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not 3_600 <= credential_ttl_seconds <= 86_400:
            raise ValueError("replacement credential TTL is outside the supported range")
        if not 300 <= max_expiry_age_seconds <= 86_400:
            raise ValueError("replacement expiry window is outside the supported range")
        self.store = store
        self.credential_ttl_seconds = credential_ttl_seconds
        self.max_expiry_age_seconds = max_expiry_age_seconds
        self.outage_gate = outage_gate
        self.clock = clock or (lambda: int(time.time()))

    def replace(
        self,
        *,
        actor: VerifiedActor,
        request: ExpiredServerReplacementRequest,
    ) -> ExpiredServerReplacementResult:
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise AuthorizationError("expired server replacement requires a human harness")
        if (
            actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise AuthenticationError("expired server replacement requires an exact non-lab binding")
        now = self.clock()
        if now < 0:
            raise ValidationError("expired server replacement time is invalid")
        signed_fields = request.signed_fields(actor)
        request_digest = canonical_digest(signed_fields)

        with self.store.transaction() as connection:
            current = load_credential_binding_from_connection(connection, actor.credential_id)
            verify_signature(
                current.public_key_pem,
                EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE,
                signed_fields,
                request.possession_signature,
            )
            previous = connection.execute(
                """SELECT request_digest,old_credential_id,new_credential_id,new_epoch,
                          not_before,new_expires_at
                     FROM expired_server_credential_replacements WHERE request_id=?""",
                (request.request_id,),
            ).fetchone()
            if previous is not None:
                if previous["request_digest"] != request_digest:
                    raise ConflictError("expired server replacement request identifier conflicts")
                successor = load_credential_binding_from_connection(
                    connection, str(previous["new_credential_id"])
                )
                if (
                    previous["old_credential_id"] != actor.credential_id
                    or successor.domain_id != actor.domain_id
                    or successor.principal_id != actor.principal_id
                    or successor.harness_id != actor.harness_id
                    or successor.key_id != current.key_id
                    or successor.public_key_pem != current.public_key_pem
                    or successor.credential_status != "active"
                    or successor.credential_epoch != int(previous["new_epoch"])
                    or successor.harness_credential_epoch != successor.credential_epoch
                ):
                    raise ConflictError("expired server replacement result changed after commit")
                return ExpiredServerReplacementResult(
                    request_id=request.request_id,
                    old_credential_id=actor.credential_id,
                    credential_id=successor.credential_id,
                    credential_epoch=successor.credential_epoch,
                    not_before=int(previous["not_before"]),
                    expires_at=int(previous["new_expires_at"]),
                )
            prior_harness_replacement = connection.execute(
                """SELECT replacement.request_id
                     FROM expired_server_credential_replacements AS replacement
                     JOIN credentials AS old_binding
                       ON old_binding.credential_id=replacement.old_credential_id
                    WHERE old_binding.harness_id=?
                    LIMIT 1""",
                (current.harness_id,),
            ).fetchone()
            if prior_harness_replacement is not None:
                raise AuthorizationError(
                    "expired server replacement is limited to one transition per harness"
                )
            if self.outage_gate is not None:
                self.outage_gate.require_issuance()
            if (
                current.domain_status != "active"
                or current.principal_id != actor.principal_id
                or current.principal_status != "active"
                or current.harness_status != "active"
                or current.credential_status != "active"
                or current.domain_id != actor.domain_id
                or current.harness_id != actor.harness_id
                or current.credential_epoch != actor.credential_epoch
                or current.harness_credential_epoch != actor.credential_epoch
                or current.binding_assurance != actor.binding_assurance
                or current.binding_assurance not in {"os_bound", "hardware_bound"}
            ):
                raise ConflictError("expired server replacement binding changed before commit")
            if request.expected_expires_at != current.expires_at:
                raise ConflictError("expired server replacement validity changed before commit")
            if now < current.not_before:
                raise AuthenticationError("expired server credential is not yet valid")
            if now < current.expires_at:
                raise ValidationError("expired server replacement refuses a current credential")
            if now - current.expires_at > self.max_expiry_age_seconds:
                raise AuthenticationError("expired server credential is outside the replacement window")

            next_epoch = current.credential_epoch + 1
            credential_id = str(
                uuid5(
                    NAMESPACE_URL,
                    "agentnet:expired-server-replacement:"
                    f"{current.domain_id}:{current.harness_id}:{request.request_id}:"
                    f"{next_epoch}:{current.key_id}",
                )
            )
            expires_at = now + self.credential_ttl_seconds
            updated = connection.execute(
                """UPDATE harnesses SET credential_epoch=?
                     WHERE harness_id=? AND domain_id=? AND principal_id=?
                       AND credential_epoch=? AND status='active'""",
                (
                    next_epoch,
                    current.harness_id,
                    current.domain_id,
                    current.principal_id,
                    current.credential_epoch,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("expired server replacement harness epoch changed before commit")
            retired = connection.execute(
                """UPDATE credentials SET status='retired'
                     WHERE credential_id=? AND harness_id=? AND epoch=?
                       AND status='active' AND expires_at=?""",
                (
                    current.credential_id,
                    current.harness_id,
                    current.credential_epoch,
                    current.expires_at,
                ),
            )
            if retired.rowcount != 1:
                raise ConflictError("expired server replacement lost its exact expired row")
            connection.execute(
                """INSERT INTO credentials(
                       credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                   ) VALUES(?,?,?,?,'active',?,?,?)""",
                (
                    credential_id,
                    current.harness_id,
                    current.key_id,
                    current.public_key_pem,
                    next_epoch,
                    now,
                    expires_at,
                ),
            )
            connection.execute(
                """INSERT INTO expired_server_credential_replacements(
                       request_id,request_digest,setup_request_digest,expected_config_digest,
                       old_credential_id,new_credential_id,new_epoch,not_before,new_expires_at,committed_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    request.request_id,
                    request_digest,
                    request.setup_request_digest,
                    request.expected_config_digest,
                    current.credential_id,
                    credential_id,
                    next_epoch,
                    now,
                    expires_at,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "credential.expired_server_replaced",
                    "domain_id": current.domain_id,
                    "principal_id": current.principal_id,
                    "harness_id": current.harness_id,
                    "old_credential_id": current.credential_id,
                    "new_credential_id": credential_id,
                    "credential_epoch": next_epoch,
                    "request_digest": request_digest,
                    "setup_request_digest": request.setup_request_digest,
                    "expected_config_digest": request.expected_config_digest,
                    "old_expires_at": current.expires_at,
                    "new_expires_at": expires_at,
                },
            )
        return ExpiredServerReplacementResult(
            request_id=request.request_id,
            old_credential_id=current.credential_id,
            credential_id=credential_id,
            credential_epoch=next_epoch,
            not_before=now,
            expires_at=expires_at,
        )


__all__ = [
    "EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE",
    "ExpiredServerReplacementRequest",
    "ExpiredServerReplacementResult",
    "ExpiredServerReplacementService",
]
