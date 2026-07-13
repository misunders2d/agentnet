"""Registered SPIFFE workload identity and per-transition proof verification."""

from __future__ import annotations

import json
import secrets
import time
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, Literal
from urllib.parse import urlsplit
from uuid import uuid4

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
    model_validator,
)

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    SignedAuthorityCommand,
    begin_authority_mutation_intent,
    complete_authority_mutation_intent,
    require_signed_authority_command,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ReplayError,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.protocol.models import DeliveryFact
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_digest,
    canonical_json,
    verify_signature,
)


@dataclass(frozen=True, slots=True)
class WorkloadIdentity:
    spiffe_id: str
    trust_domain: str
    workload_role: str
    certificate_serial: str


class SPIFFETransportFactsV1(BaseModel):
    """Exact facts emitted by the trusted mTLS/SVID termination boundary."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"]
    spiffe_id: str = Field(min_length=12, max_length=2048)
    trust_domain: str = Field(
        min_length=1,
        max_length=253,
        pattern=r"^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$",
    )
    workload_role: str = Field(
        min_length=1,
        max_length=128,
        pattern=r"^[a-z][a-z0-9_.-]{0,127}$",
    )
    certificate_serial: str = Field(min_length=1, max_length=256)
    process_id: int = Field(gt=0)
    process_start_time: int = Field(gt=0)
    session_id: str = Field(min_length=16, max_length=256)

    @model_validator(mode="after")
    def exact_spiffe_uri(self) -> "SPIFFETransportFactsV1":
        parsed = urlsplit(self.spiffe_id)
        if (
            parsed.scheme != "spiffe"
            or parsed.netloc != self.trust_domain
            or not parsed.path.startswith("/")
            or parsed.path == "/"
            or parsed.query
            or parsed.fragment
            or parsed.username is not None
            or parsed.password is not None
            or parsed.port is not None
        ):
            raise ValueError("SPIFFE identifier does not bind the exact trust domain and workload path")
        return self

    @classmethod
    def parse_boundary(cls, value: object) -> "SPIFFETransportFactsV1":
        try:
            return cls.model_validate(value, strict=True)
        except PydanticValidationError as exc:
            raise AuthenticationError("workload mTLS transport facts are malformed") from exc


@dataclass(frozen=True, slots=True)
class AuthenticatedSPIFFETransport:
    """Opaque capability minted only by the configured mTLS termination seam."""

    facts: SPIFFETransportFactsV1
    _authority_id: bytes = field(repr=False, compare=False)


class SPIFFETransportAuthority:
    """Capability held by the server-side mTLS/SVID verification component.

    Calling :meth:`bind_verified_peer` is the trust-boundary operation after
    the hosting transport has independently authenticated the peer certificate
    and SVID.  No request JSON field, including a ``verified`` boolean/string,
    can invoke this capability or substitute for that authentication.
    """

    def __init__(self) -> None:
        self.__authority_id = secrets.token_bytes(32)

    def bind_verified_peer(self, facts: object) -> AuthenticatedSPIFFETransport:
        return AuthenticatedSPIFFETransport(
            facts=SPIFFETransportFactsV1.parse_boundary(facts),
            _authority_id=self.__authority_id,
        )

    def owns(self, transport: AuthenticatedSPIFFETransport) -> bool:
        return secrets.compare_digest(transport._authority_id, self.__authority_id)


class SPIFFEAdapter:
    """Consume only opaque facts minted by this server's mTLS authority."""

    def __init__(self, authority: SPIFFETransportAuthority | None = None) -> None:
        self.transport_authority = authority or SPIFFETransportAuthority()

    def resolve(self, transport: AuthenticatedSPIFFETransport) -> WorkloadIdentity:
        if not isinstance(transport, AuthenticatedSPIFFETransport):
            raise AuthenticationError(
                "workload identity requires an authenticated mTLS transport capability"
            )
        if not self.transport_authority.owns(transport):
            raise AuthenticationError("workload mTLS transport was minted by another authority")
        facts = transport.facts
        return WorkloadIdentity(
            spiffe_id=facts.spiffe_id,
            trust_domain=facts.trust_domain,
            workload_role=facts.workload_role,
            certificate_serial=facts.certificate_serial,
        )


class WorkloadTransitionProof(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    registration_id: str = Field(min_length=16, max_length=128)
    workload_id: str = Field(min_length=1, max_length=256)
    workload_role: str = Field(min_length=1, max_length=128)
    process_id: int = Field(gt=0)
    process_start_time: int = Field(gt=0)
    session_id: str = Field(min_length=16, max_length=256)
    credential_epoch: int = Field(gt=0)
    revocation_epoch: int = Field(gt=0)
    parent_event_id: str | None = None
    task_grant_id: str | None = None
    event_id: str = Field(min_length=1)
    recipient_id: str = Field(min_length=1)
    proposed_fact: DeliveryFact
    detail_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    timestamp: int = Field(gt=0)
    nonce: str = Field(min_length=24, max_length=256)
    signature: str = Field(min_length=1, max_length=2048)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"signature"}, exclude_none=True)

    @classmethod
    def create(
        cls,
        signer: P256KeyPair,
        *,
        actor: VerifiedActor,
        event_id: str,
        recipient_id: str,
        proposed_fact: DeliveryFact,
        detail: dict[str, Any] | None = None,
        timestamp: int | None = None,
        nonce: str | None = None,
    ) -> "WorkloadTransitionProof":
        if actor.binding_assurance != "workload_mtls":
            raise AuthenticationError("transition proof requires a registered mTLS workload")
        fields = {
            "registration_id": actor.workload_registration_id,
            "workload_id": actor.workload_id,
            "workload_role": actor.workload_role,
            "process_id": actor.workload_process_id,
            "process_start_time": actor.workload_process_start_time,
            "session_id": actor.workload_session_id,
            "credential_epoch": actor.credential_epoch,
            "revocation_epoch": actor.workload_revocation_epoch,
            "parent_event_id": actor.parent_event_id,
            "task_grant_id": actor.task_grant_id,
            "event_id": event_id,
            "recipient_id": recipient_id,
            "proposed_fact": proposed_fact.value,
            "detail_digest": canonical_digest(detail or {}),
            "timestamp": int(time.time()) if timestamp is None else timestamp,
            "nonce": nonce or f"workload-proof-{uuid4()}-{uuid4()}",
        }
        signed = {key: value for key, value in fields.items() if value is not None}
        return cls(**fields, signature=signer.sign("agentnet.workload.transition.v1", signed))


@dataclass(frozen=True, slots=True)
class RegisteredWorkloadCredential:
    """In-process handle for one already-registered workload credential.

    The actor is the public registration projection and ``signer`` is the
    process-held proof-of-possession key.  This object is deliberately neither
    serializable nor persisted by the core; process launch/custody code must
    supply it through an authenticated local boundary.
    """

    actor: VerifiedActor
    signer: P256KeyPair

    def proof(
        self,
        *,
        event_id: str,
        recipient_id: str,
        proposed_fact: DeliveryFact,
        detail: dict[str, Any] | None = None,
    ) -> WorkloadTransitionProof:
        return WorkloadTransitionProof.create(
            self.signer,
            actor=self.actor,
            event_id=event_id,
            recipient_id=recipient_id,
            proposed_fact=proposed_fact,
            detail=detail,
        )


class WorkloadRegistry:
    def __init__(self, store: Any, *, spiffe: SPIFFEAdapter | None = None) -> None:
        self.store = store
        self.spiffe = spiffe or SPIFFEAdapter()

    @staticmethod
    def registration_request(
        *,
        registration_id: str,
        domain_id: str,
        workload_id: str,
        workload_role: str,
        recipient_scope: str,
        process_id: int,
        process_start_time: int,
        session_id: str,
        identity: WorkloadIdentity,
        public_key_pem: str,
        key_id: str,
        credential_epoch: int,
        revocation_epoch: int,
        parent_event_id: str | None,
        task_grant_id: str | None,
        issued_at: int,
        expires_at: int,
    ) -> dict[str, Any]:
        return {
            "certificate_serial": identity.certificate_serial,
            "credential_epoch": credential_epoch,
            "domain_id": domain_id,
            "expires_at": expires_at,
            "issued_at": issued_at,
            "key_id": key_id,
            "parent_event_id": parent_event_id,
            "process_id": process_id,
            "process_start_time": process_start_time,
            "public_key_digest": canonical_digest({"public_key_pem": public_key_pem}),
            "recipient_scope": recipient_scope,
            "registration_id": registration_id,
            "revocation_epoch": revocation_epoch,
            "session_id": session_id,
            "spiffe_id": identity.spiffe_id,
            "task_grant_id": task_grant_id,
            "trust_domain": identity.trust_domain,
            "workload_id": workload_id,
            "workload_role": workload_role,
        }

    def register(
        self,
        *,
        authority: IssuanceAuthority,
        command: SignedAuthorityCommand,
        registration_id: str,
        domain_id: str,
        workload_id: str,
        workload_role: str,
        recipient_scope: str,
        process_id: int,
        process_start_time: int,
        session_id: str,
        identity: WorkloadIdentity,
        public_key_pem: str,
        key_id: str,
        credential_epoch: int,
        revocation_epoch: int,
        parent_event_id: str | None,
        task_grant_id: str | None,
        issued_at: int,
        expires_at: int,
        possession_signature: str,
    ) -> VerifiedActor:
        request = self.registration_request(
            registration_id=registration_id,
            domain_id=domain_id,
            workload_id=workload_id,
            workload_role=workload_role,
            recipient_scope=recipient_scope,
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=public_key_pem,
            key_id=key_id,
            credential_epoch=credential_epoch,
            revocation_epoch=revocation_epoch,
            parent_event_id=parent_event_id,
            task_grant_id=task_grant_id,
            issued_at=issued_at,
            expires_at=expires_at,
        )
        now = int(time.time())
        if (
            identity.trust_domain != domain_id
            or identity.workload_role != workload_role
            or (parent_event_id is None) != (task_grant_id is None)
            or process_id < 1
            or process_start_time < 1
            or process_start_time > now
            or credential_epoch != 1
            or revocation_epoch < 1
            or issued_at > now
            or issued_at < now - 300
            or expires_at <= now
            or expires_at > now + 3_600
        ):
            raise AuthenticationError("workload registration binding is invalid")
        if command.expected_entity_revision != 0:
            raise ConflictError("new workload expected entity revision must be zero")
        if public_key_thumbprint(public_key_pem) != key_id:
            raise AuthenticationError("workload registration key thumbprint mismatch")
        verify_signature(
            public_key_pem,
            "agentnet.workload.registration.pop.v1",
            request,
            possession_signature,
        )
        when = datetime.fromtimestamp(now, UTC)
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT status,revocation_epoch FROM domains WHERE domain_id=?",
                (domain_id,),
            ).fetchone()
            if (
                authority.actor.domain_id != domain_id
                or domain is None
                or domain["status"] != "active"
                or int(domain["revocation_epoch"]) != revocation_epoch
            ):
                raise AuthorizationError("workload registration domain authority is not current")
            require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="identity.workload.register",
                expected_resource=f"workload:{registration_id}",
                expected_request=request,
                when=when,
            )
            begin_authority_mutation_intent(
                connection,
                command=command,
                authority=authority,
                when=when,
            )
            connection.execute(
                """INSERT INTO workload_registrations(
                    registration_id,domain_id,workload_id,workload_role,recipient_scope,
                    process_id,process_start_time,session_id,spiffe_id,certificate_serial,key_id,
                    public_key_pem,credential_epoch,revocation_epoch,parent_event_id,task_grant_id,
                    status,issued_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    registration_id, domain_id, workload_id, workload_role, recipient_scope,
                    process_id, process_start_time, session_id, identity.spiffe_id,
                    identity.certificate_serial, key_id, public_key_pem, credential_epoch,
                    revocation_epoch, parent_event_id, task_grant_id, "active", issued_at, expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "workload.register",
                    "actor": authority.actor.audit_view(),
                    "command_id": command.command_id,
                    "request_digest": canonical_digest(request),
                    "registration_id": registration_id,
                },
            )
            complete_authority_mutation_intent(
                connection,
                command_id=command.command_id,
                when=when,
            )
        return self._actor_from_request(request)

    @staticmethod
    def renewal_request(
        *,
        registration_id: str,
        expected_credential_epoch: int,
        credential_epoch: int,
        revocation_epoch: int,
        process_id: int,
        process_start_time: int,
        session_id: str,
        identity: WorkloadIdentity,
        public_key_pem: str,
        key_id: str,
        issued_at: int,
        expires_at: int,
    ) -> dict[str, Any]:
        return {
            "certificate_serial": identity.certificate_serial,
            "credential_epoch": credential_epoch,
            "expected_credential_epoch": expected_credential_epoch,
            "expires_at": expires_at,
            "issued_at": issued_at,
            "key_id": key_id,
            "process_id": process_id,
            "process_start_time": process_start_time,
            "public_key_digest": canonical_digest({"public_key_pem": public_key_pem}),
            "registration_id": registration_id,
            "revocation_epoch": revocation_epoch,
            "session_id": session_id,
            "spiffe_id": identity.spiffe_id,
            "trust_domain": identity.trust_domain,
            "workload_role": identity.workload_role,
        }

    def renew(
        self,
        *,
        authority: IssuanceAuthority,
        command: SignedAuthorityCommand,
        registration_id: str,
        expected_credential_epoch: int,
        process_id: int,
        process_start_time: int,
        session_id: str,
        identity: WorkloadIdentity,
        public_key_pem: str,
        key_id: str,
        issued_at: int,
        expires_at: int,
        possession_signature: str,
    ) -> VerifiedActor:
        """Renew one exact workload binding without changing its delegated scope."""

        if expected_credential_epoch < 1 or command.expected_entity_revision != expected_credential_epoch:
            raise ConflictError("workload renewal expected credential revision mismatch")
        credential_epoch = expected_credential_epoch + 1
        now = int(time.time())
        if (
            process_id < 1
            or process_start_time < 1
            or process_start_time > now
            or issued_at > now
            or issued_at < now - 300
            or expires_at <= now
            or expires_at > now + 3_600
            or public_key_thumbprint(public_key_pem) != key_id
        ):
            raise AuthenticationError("workload renewal binding is invalid")
        request = self.renewal_request(
            registration_id=registration_id,
            expected_credential_epoch=expected_credential_epoch,
            credential_epoch=credential_epoch,
            revocation_epoch=0,  # Rebound to the authoritative domain epoch below.
            process_id=process_id,
            process_start_time=process_start_time,
            session_id=session_id,
            identity=identity,
            public_key_pem=public_key_pem,
            key_id=key_id,
            issued_at=issued_at,
            expires_at=expires_at,
        )
        when = datetime.fromtimestamp(now, UTC)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM workload_registrations WHERE registration_id=?",
                (registration_id,),
            ).fetchone()
            domain = None if row is None else connection.execute(
                "SELECT status,revocation_epoch FROM domains WHERE domain_id=?",
                (row["domain_id"],),
            ).fetchone()
            if (
                row is None
                or row["status"] != "active"
                or int(row["credential_epoch"]) != expected_credential_epoch
                or domain is None
                or domain["status"] != "active"
                or int(domain["revocation_epoch"]) != int(row["revocation_epoch"])
                or identity.trust_domain != row["domain_id"]
                or identity.spiffe_id != row["spiffe_id"]
                or identity.workload_role != row["workload_role"]
                or authority.actor.domain_id != row["domain_id"]
            ):
                raise ConflictError("workload renewal state changed or transport binding is stale")
            request["revocation_epoch"] = int(domain["revocation_epoch"])
            verify_signature(
                public_key_pem,
                "agentnet.workload.renewal.pop.v1",
                request,
                possession_signature,
            )
            require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="identity.workload.renew",
                expected_resource=f"workload:{registration_id}",
                expected_request=request,
                when=when,
            )
            begin_authority_mutation_intent(
                connection,
                command=command,
                authority=authority,
                when=when,
            )
            updated = connection.execute(
                """UPDATE workload_registrations
                      SET process_id=?,process_start_time=?,session_id=?,certificate_serial=?,
                          key_id=?,public_key_pem=?,credential_epoch=?,issued_at=?,expires_at=?
                    WHERE registration_id=? AND status='active' AND credential_epoch=?""",
                (
                    process_id,
                    process_start_time,
                    session_id,
                    identity.certificate_serial,
                    key_id,
                    public_key_pem,
                    credential_epoch,
                    issued_at,
                    expires_at,
                    registration_id,
                    expected_credential_epoch,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("workload renewal raced with another lifecycle mutation")
            self.store.append_audit(
                connection,
                {
                    "action": "workload.renew",
                    "actor": authority.actor.audit_view(),
                    "command_id": command.command_id,
                    "credential_epoch": credential_epoch,
                    "registration_id": registration_id,
                    "request_digest": canonical_digest(request),
                },
            )
            complete_authority_mutation_intent(
                connection,
                command_id=command.command_id,
                when=when,
            )
            current = dict(row)
            current.update(
                process_id=process_id,
                process_start_time=process_start_time,
                session_id=session_id,
                certificate_serial=identity.certificate_serial,
                key_id=key_id,
                public_key_pem=public_key_pem,
                credential_epoch=credential_epoch,
                issued_at=issued_at,
                expires_at=expires_at,
            )
            return self._actor_from_request(current)

    @staticmethod
    def revocation_request(
        *,
        registration_id: str,
        expected_credential_epoch: int,
        expected_revocation_epoch: int,
        reason: str,
    ) -> dict[str, Any]:
        if not reason.strip() or len(reason) > 512:
            raise ValidationError("workload revocation reason is required")
        return {
            "expected_credential_epoch": expected_credential_epoch,
            "expected_revocation_epoch": expected_revocation_epoch,
            "reason": reason,
            "registration_id": registration_id,
        }

    def revoke(
        self,
        *,
        authority: IssuanceAuthority,
        command: SignedAuthorityCommand,
        registration_id: str,
        expected_credential_epoch: int,
        expected_revocation_epoch: int,
        reason: str,
    ) -> dict[str, Any]:
        """Revoke one workload without rotating the domain-wide epoch."""

        if command.expected_entity_revision != expected_credential_epoch:
            raise ConflictError("workload revocation expected credential revision mismatch")
        request = self.revocation_request(
            registration_id=registration_id,
            expected_credential_epoch=expected_credential_epoch,
            expected_revocation_epoch=expected_revocation_epoch,
            reason=reason,
        )
        when = datetime.now(UTC)
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM workload_registrations WHERE registration_id=?",
                (registration_id,),
            ).fetchone()
            domain = None if row is None else connection.execute(
                "SELECT status,revocation_epoch FROM domains WHERE domain_id=?",
                (row["domain_id"],),
            ).fetchone()
            if (
                row is None
                or row["status"] != "active"
                or int(row["credential_epoch"]) != expected_credential_epoch
                or int(row["revocation_epoch"]) != expected_revocation_epoch
                or domain is None
                or domain["status"] != "active"
                or int(domain["revocation_epoch"]) != expected_revocation_epoch
                or authority.actor.domain_id != row["domain_id"]
            ):
                raise ConflictError("workload revocation state changed before commit")
            require_signed_authority_command(
                connection,
                command=command,
                authority=authority,
                expected_action="identity.workload.revoke",
                expected_resource=f"workload:{registration_id}",
                expected_request=request,
                when=when,
            )
            begin_authority_mutation_intent(
                connection,
                command=command,
                authority=authority,
                when=when,
            )
            updated = connection.execute(
                """UPDATE workload_registrations
                      SET status='revoked',credential_epoch=credential_epoch+1
                    WHERE registration_id=? AND status='active' AND credential_epoch=?""",
                (registration_id, expected_credential_epoch),
            )
            if updated.rowcount != 1:
                raise ConflictError("workload revocation raced with another lifecycle mutation")
            self.store.append_audit(
                connection,
                {
                    "action": "workload.revoke",
                    "actor": authority.actor.audit_view(),
                    "command_id": command.command_id,
                    "registration_id": registration_id,
                    "reason": reason,
                    "request_digest": canonical_digest(request),
                },
            )
            complete_authority_mutation_intent(
                connection,
                command_id=command.command_id,
                when=when,
            )
            return {
                "credential_epoch": expected_credential_epoch + 1,
                "registration_id": registration_id,
                "revocation_epoch": expected_revocation_epoch,
                "status": "revoked",
            }

    @staticmethod
    def _actor_from_request(request: dict[str, Any]) -> VerifiedActor:
        return VerifiedActor(
            kind=ActorKind.WORKLOAD,
            domain_id=request["domain_id"],
            workload_id=request["workload_id"],
            workload_registration_id=request["registration_id"],
            workload_role=request["workload_role"],
            workload_process_id=request["process_id"],
            workload_process_start_time=request["process_start_time"],
            workload_session_id=request["session_id"],
            workload_revocation_epoch=request["revocation_epoch"],
            parent_event_id=request["parent_event_id"],
            task_grant_id=request["task_grant_id"],
            credential_id=request["registration_id"],
            credential_epoch=request["credential_epoch"],
            binding_assurance="workload_mtls",
        )

    def resolve(
        self,
        *,
        transport: AuthenticatedSPIFFETransport,
        registration_id: str,
        process_id: int | None = None,
        process_start_time: int | None = None,
        session_id: str | None = None,
        now: int | None = None,
    ) -> VerifiedActor:
        identity = self.spiffe.resolve(transport)
        facts = transport.facts
        for supplied, bound, label in (
            (process_id, facts.process_id, "process identifier"),
            (process_start_time, facts.process_start_time, "process start time"),
            (session_id, facts.session_id, "session identifier"),
        ):
            if supplied is not None and supplied != bound:
                raise AuthenticationError(f"workload {label} differs from authenticated transport")
        process_id = facts.process_id
        process_start_time = facts.process_start_time
        session_id = facts.session_id
        now = int(time.time()) if now is None else now
        row = self.store.fetch_one(
            "SELECT * FROM workload_registrations WHERE registration_id=?",
            (registration_id,),
        )
        if (
            row is None
            or row["status"] != "active"
            or int(row["issued_at"]) > now
            or int(row["expires_at"]) <= now
            or row["spiffe_id"] != identity.spiffe_id
            or row["certificate_serial"] != identity.certificate_serial
            or row["workload_role"] != identity.workload_role
            or int(row["process_id"]) != process_id
            or int(row["process_start_time"]) != process_start_time
            or row["session_id"] != session_id
        ):
            raise AuthenticationError("workload registration is not current for this transport")
        domain = self.store.fetch_one(
            "SELECT status,revocation_epoch FROM domains WHERE domain_id=?", (row["domain_id"],)
        )
        if domain is None or domain["status"] != "active" or int(domain["revocation_epoch"]) != int(row["revocation_epoch"]):
            raise AuthenticationError("workload domain revocation epoch is stale")
        return self._actor_from_request(dict(row))

    def verify_transition(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        proof: WorkloadTransitionProof | None,
        allowed_roles: set[str],
        event_id: str,
        recipient_id: str,
        proposed_fact: DeliveryFact,
        detail: dict[str, Any] | None,
        now: int,
    ) -> None:
        if actor.kind is not ActorKind.WORKLOAD or actor.binding_assurance != "workload_mtls" or proof is None:
            raise AuthorizationError("receipt fact requires an authenticated workload transition proof")
        row = connection.execute(
            "SELECT * FROM workload_registrations WHERE registration_id=?",
            (actor.workload_registration_id,),
        ).fetchone()
        domain = connection.execute(
            "SELECT status,revocation_epoch FROM domains WHERE domain_id=?", (actor.domain_id,)
        ).fetchone()
        expected_actor = None if row is None else self._actor_from_request(dict(row))
        if (
            row is None
            or expected_actor.audit_view() != actor.audit_view()
            or row["status"] != "active"
            or int(row["issued_at"]) > now
            or int(row["expires_at"]) <= now
            or row["workload_role"] not in allowed_roles
            or row["recipient_scope"] not in {"*", recipient_id}
            or domain is None
            or domain["status"] != "active"
            or int(domain["revocation_epoch"]) != int(row["revocation_epoch"])
        ):
            raise AuthorizationError("registered workload authority is not current for this fact")
        expected_proof = {
            "registration_id": actor.workload_registration_id,
            "workload_id": actor.workload_id,
            "workload_role": actor.workload_role,
            "process_id": actor.workload_process_id,
            "process_start_time": actor.workload_process_start_time,
            "session_id": actor.workload_session_id,
            "credential_epoch": actor.credential_epoch,
            "revocation_epoch": actor.workload_revocation_epoch,
            "parent_event_id": actor.parent_event_id,
            "task_grant_id": actor.task_grant_id,
            "event_id": event_id,
            "recipient_id": recipient_id,
            "proposed_fact": proposed_fact.value,
            "detail_digest": canonical_digest(detail or {}),
        }
        actual = proof.model_dump(mode="json", exclude={"signature", "timestamp", "nonce"}, exclude_none=True)
        expected = {key: value for key, value in expected_proof.items() if value is not None}
        if actual != expected or proof.timestamp > now + 60 or proof.timestamp < now - 300:
            raise AuthenticationError("workload transition proof binding or freshness failed")
        if row["workload_role"] in {
            "recipient_processor",
            "effect_authority",
            "control_authority",
        }:
            if row["parent_event_id"] != event_id or row["task_grant_id"] is None:
                raise AuthorizationError("workload transition lacks its exact parent event and task grant")
            grant = connection.execute(
                "SELECT * FROM task_grants WHERE grant_id=?", (row["task_grant_id"],)
            ).fetchone()
            grant_document = {} if grant is None else json.loads(grant["grant_json"])
            recipient = connection.execute(
                "SELECT domain_id,principal_id,guest_id,status FROM harnesses WHERE harness_id=?",
                (recipient_id,),
            ).fetchone()
            required_actions = {
                "recipient_processor": {"message.process", "task.process"},
                "effect_authority": {"effect.execute"},
                "control_authority": {"task.cancel"},
            }[str(row["workload_role"])]
            if (
                grant is None
                or grant["domain_id"] != actor.domain_id
                or recipient is None
                or recipient["domain_id"] != actor.domain_id
                or recipient["status"] != "active"
                or grant["harness_id"] != recipient_id
                or grant["principal_id"] not in {recipient["principal_id"], recipient["guest_id"]}
                or f"event:{event_id}" not in set(grant_document.get("resources", ()))
                or not required_actions.intersection(grant_document.get("actions", ()))
                or "mailbox" not in set(grant_document.get("input_sources", ()))
                or "receipt" not in set(grant_document.get("output_sinks", ()))
                or grant["revoked_at"] is not None
                or int(grant["expires_at"]) <= now
                or int(grant["uses"]) >= int(grant["max_uses"])
            ):
                raise AuthorizationError("workload task grant is not current")
        verify_signature(row["public_key_pem"], "agentnet.workload.transition.v1", proof.signed_fields(), proof.signature)
        replay_key = canonical_digest({
            "nonce": proof.nonce,
            "registration_id": proof.registration_id,
            "event_id": event_id,
            "recipient_id": recipient_id,
            "fact": proposed_fact.value,
        })
        replay = connection.execute(
            """INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)
               ON CONFLICT(actor_id,nonce_hash) DO NOTHING""",
            (
                f"workload:{proof.registration_id}",
                replay_key,
                max(now + 300, int(row["expires_at"])),
            ),
        )
        if replay.rowcount != 1:
            raise ReplayError("workload transition proof was already consumed")
        if row["workload_role"] in {
            "recipient_processor",
            "effect_authority",
            "control_authority",
        }:
            cursor = connection.execute(
                """UPDATE task_grants SET uses=uses+1
                     WHERE grant_id=? AND revoked_at IS NULL AND expires_at>? AND uses<max_uses""",
                (row["task_grant_id"], now),
            )
            if cursor.rowcount != 1:
                raise AuthorizationError("workload task grant was consumed or revoked concurrently")


__all__ = [
    "AuthenticatedSPIFFETransport",
    "RegisteredWorkloadCredential",
    "SPIFFEAdapter",
    "SPIFFETransportAuthority",
    "SPIFFETransportFactsV1",
    "WorkloadIdentity",
    "WorkloadRegistry",
    "WorkloadTransitionProof",
]
