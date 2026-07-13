"""Cryptographically bilateral, host-local guest admission and revocation."""

from __future__ import annotations

import json
import re
import secrets
import time
from collections.abc import Callable, Iterable, Mapping
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import uuid4

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
    field_validator,
    model_validator,
)

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_authority_decision,
)
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import (
    AttenuationPolicy,
    FederationAssurancePolicy,
)
from agentnet.organization.relationships import RelationshipService
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.signatures import canonical_digest, canonical_json, verify_signature
from agentnet.storage.backend import StoreBackend
from agentnet.federation.trust import require_direct_bilateral


def _canonical_tuple(value: tuple[str, ...], *, label: str) -> tuple[str, ...]:
    if not value or value != tuple(sorted(set(value))):
        raise ValueError(f"{label} must be a nonempty sorted unique tuple")
    return value


class HomeFederationAssertion(BaseModel):
    """Home-domain metadata signed by a host-pinned home-domain key."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    assertion_type: Literal["home_federation_metadata"] = "home_federation_metadata"
    host_domain_id: str = Field(min_length=1)
    home_domain_id: str = Field(min_length=1)
    home_key_id: str = Field(min_length=1)
    endpoints: tuple[str, ...]
    algorithms: tuple[str, ...]
    allowed_data_classes: tuple[str, ...]
    assurance_profile: Literal["lab", "os_bound", "hardware_bound"]
    revocation_endpoint: str = Field(min_length=1)
    incident_contact: str = Field(min_length=1)
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    @field_validator("endpoints", "algorithms", "allowed_data_classes")
    @classmethod
    def canonical_sets(cls, value: tuple[str, ...], info: Any) -> tuple[str, ...]:
        return _canonical_tuple(value, label=info.field_name)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class HostTrustAcceptance(BaseModel):
    """Independent host-policy signature accepting one exact home assertion."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    assertion_type: Literal["host_trust_acceptance"] = "host_trust_acceptance"
    host_domain_id: str = Field(min_length=1)
    home_domain_id: str = Field(min_length=1)
    host_key_id: str = Field(min_length=1)
    home_key_id: str = Field(min_length=1)
    home_assertion_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    accepted_endpoints: tuple[str, ...]
    accepted_data_classes: tuple[str, ...]
    assurance_profile: Literal["lab", "os_bound", "hardware_bound"]
    non_transitive: Literal[True]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    @field_validator("accepted_endpoints", "accepted_data_classes")
    @classmethod
    def canonical_sets(cls, value: tuple[str, ...], info: Any) -> tuple[str, ...]:
        return _canonical_tuple(value, label=info.field_name)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class GuestIdentityAssertion(BaseModel):
    """Fresh home assertion for one invitation and one per-host guest key."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    assertion_type: Literal["guest_identity"] = "guest_identity"
    invitation_id: str = Field(min_length=1)
    invitation_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    host_domain_id: str = Field(min_length=1)
    home_domain_id: str = Field(min_length=1)
    home_key_id: str = Field(min_length=1)
    pairwise_subject: str = Field(min_length=16, max_length=512)
    guest_harness_key_id: str = Field(min_length=1)
    guest_harness_key_thumbprint: str = Field(min_length=1)
    assurance_profile: Literal["lab", "os_bound", "hardware_bound"]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")


class FederationInvitationGrant(BaseModel):
    """Strict host-local grant embedded in one invitation transaction."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    action: str = Field(min_length=1, max_length=256)
    resource_pattern: str = Field(min_length=1, max_length=1_024)
    data_class: Classification
    input_source: str = Field(min_length=1, max_length=256)
    output_sink: str = Field(min_length=1, max_length=1_024)
    max_uses: int = Field(ge=1, le=1_000_000)
    expires_at: int = Field(ge=1)

    @field_validator("max_uses", "expires_at", mode="before")
    @classmethod
    def exact_integers(cls, value: Any) -> Any:
        if type(value) is not int:
            raise ValueError("federation invitation grant integers must be exact")
        return value

    @field_validator(
        "action",
        "resource_pattern",
        "data_class",
        "input_source",
        "output_sink",
        mode="before",
    )
    @classmethod
    def exact_strings(cls, value: Any) -> Any:
        if not isinstance(value, str):
            raise ValueError("federation invitation grant strings must be exact")
        return value


class _StoredInvitationTranscript(BaseModel):
    """Authenticated-at-rest representation; parsing never grants authority."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    assurance_profile: Literal["lab", "os_bound", "hardware_bound"]
    grants: tuple[FederationInvitationGrant, ...] = Field(min_length=1, max_length=256)
    guest_key_id: str = Field(min_length=16, max_length=256)
    guest_public_key_thumbprint: str = Field(min_length=16, max_length=256)
    guest_public_key_pem: str = Field(min_length=128, max_length=16_384)
    home_assertion_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    home_domain_id: str = Field(min_length=1, max_length=256)
    host_domain_id: str = Field(min_length=1, max_length=256)
    invitation_id: str = Field(min_length=1, max_length=256)
    pairwise_subject: str = Field(min_length=16, max_length=512)
    sponsor_principal_id: str = Field(min_length=1, max_length=256)
    expires_at: int = Field(ge=1)
    secret_hash: str = Field(pattern=r"^[0-9a-f]{64}$")

    @model_validator(mode="after")
    def coherent(self) -> "_StoredInvitationTranscript":
        if self.guest_key_id != self.guest_public_key_thumbprint:
            raise ValueError("stored invitation key binding is inconsistent")
        if any(grant.expires_at > self.expires_at for grant in self.grants):
            raise ValueError("stored invitation grant outlives its invitation")
        return self

    def transaction_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"guest_public_key_pem"})


class HomeRevocationSignal(BaseModel):
    """Home-signed emergency revocation for one bilateral trust relationship.

    A home-domain revocation conservatively invalidates every guest admitted
    from that home domain.  The host never imports home authority; it merely
    treats the signed signal as a deny-only containment input.
    """

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    assertion_type: Literal["home_revocation"] = "home_revocation"
    host_domain_id: str = Field(min_length=1)
    home_domain_id: str = Field(min_length=1)
    home_key_id: str = Field(min_length=1)
    revocation_epoch: int = Field(ge=2)
    reason_code: Literal["identity_revoked", "credential_compromised", "domain_emergency"]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class FederationService:
    INVITATION_FAILURE_METRIC = "federation_invitation_accept_failures"

    def __init__(
        self,
        store: StoreBackend,
        *,
        enabled: bool = False,
        runtime_capabilities: frozenset[ServerAgentCapability] = frozenset(),
        policy_engine: PolicyEngine | None = None,
        trusted_domain_keys: Mapping[tuple[str, str], str] | None = None,
        host_policy_keys: Mapping[tuple[str, str], str] | None = None,
        assurance_policy: FederationAssurancePolicy | None = None,
        attenuation_policy: AttenuationPolicy | None = None,
        outage_gate: OutageGate | None = None,
        relationships: RelationshipService | None = None,
        clock: Any = time.time,
        invitation_failure_limit: int = 5,
    ) -> None:
        if type(invitation_failure_limit) is not int or not 1 <= invitation_failure_limit <= 20:
            raise ValueError("federation invitation failure limit is outside the bounded profile")
        self.store = store
        self.enabled = enabled
        self.runtime_capabilities = frozenset(runtime_capabilities)
        self.policy_engine = policy_engine
        self.trusted_domain_keys = dict(trusted_domain_keys or {})
        self.host_policy_keys = dict(host_policy_keys or {})
        self.assurance_policy = assurance_policy or FederationAssurancePolicy()
        self.attenuation_policy = attenuation_policy
        self.outage_gate = outage_gate
        self.relationships = relationships or RelationshipService(store)
        self.clock = clock
        self.invitation_failure_limit = invitation_failure_limit

    @staticmethod
    def _invitation_failure_scope(invitation_id: str) -> str:
        return f"federation-invitation:{invitation_id}"

    def _invitation_failure_count(self, connection: Any, invitation_id: str) -> int:
        row = connection.execute(
            """SELECT used FROM quota_counters
                WHERE scope=? AND metric=? AND window_start=0""",
            (self._invitation_failure_scope(invitation_id), self.INVITATION_FAILURE_METRIC),
        ).fetchone()
        return 0 if row is None else int(row["used"])

    def _record_invitation_failure(
        self,
        connection: Any,
        *,
        invitation_id: str,
        host_domain_id: str,
        now: int,
    ) -> int:
        connection.execute(
            """INSERT INTO quota_counters(scope,metric,window_start,used,limit_value)
                 VALUES(?,?,0,1,?)
                 ON CONFLICT(scope,metric,window_start) DO UPDATE SET
                    used=quota_counters.used+1,
                    limit_value=excluded.limit_value""",
            (
                self._invitation_failure_scope(invitation_id),
                self.INVITATION_FAILURE_METRIC,
                self.invitation_failure_limit,
            ),
        )
        count = self._invitation_failure_count(connection, invitation_id)
        self.store.append_audit(
            connection,
            {
                "action": (
                    "federation.invitation_locked"
                    if count >= self.invitation_failure_limit
                    else "federation.invitation_proof_rejected"
                ),
                "attempt_count": min(count, self.invitation_failure_limit),
                "host_domain_id": host_domain_id,
                "invitation_id": invitation_id,
                "occurred_at": now,
            },
        )
        return count

    @staticmethod
    def _stored_invitation(value: object) -> _StoredInvitationTranscript:
        if not isinstance(value, str):
            raise AuthorizationError("federation invitation state is invalid")
        try:
            return _StoredInvitationTranscript.model_validate_json(value)
        except PydanticValidationError as exc:
            raise AuthorizationError("federation invitation state is invalid") from exc

    def _require_enabled(self) -> None:
        if not self.enabled:
            raise AuthorizationError("federation is disabled until bilateral and owner gates pass")
        if ServerAgentCapability.FEDERATION not in self.runtime_capabilities:
            raise AuthorizationError("this ordinary server-agent process is not configured for federation")

    def admit_bilateral_trust(
        self,
        *,
        home_assertion: HomeFederationAssertion,
        home_signature: str,
        host_acceptance: HostTrustAcceptance,
        host_signature: str,
    ) -> dict[str, Any]:
        """Persist trust only after independent exact signatures from both domains."""

        self._require_enabled()
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        now = int(self.clock())
        if home_assertion.host_domain_id == home_assertion.home_domain_id:
            raise ConflictError("federation trust must cross distinct domains")
        home_key = self.trusted_domain_keys.get((home_assertion.home_domain_id, home_assertion.home_key_id))
        host_key = self.host_policy_keys.get((host_acceptance.host_domain_id, host_acceptance.host_key_id))
        if home_key is None or host_key is None:
            raise AuthenticationError("bilateral assertion key is not pinned by host policy")
        if not (home_assertion.issued_at <= now < home_assertion.expires_at):
            raise AuthenticationError("home federation assertion is outside its validity interval")
        if not (host_acceptance.issued_at <= now < host_acceptance.expires_at):
            raise AuthenticationError("host federation acceptance is outside its validity interval")
        verify_signature(
            home_key,
            "agentnet.federation.assertion.v1",
            home_assertion.signed_fields(),
            home_signature,
        )
        verify_signature(
            host_key,
            "agentnet.federation.assertion.v1",
            host_acceptance.signed_fields(),
            host_signature,
        )
        if (
            host_acceptance.host_domain_id != home_assertion.host_domain_id
            or host_acceptance.home_domain_id != home_assertion.home_domain_id
            or host_acceptance.home_key_id != home_assertion.home_key_id
            or host_acceptance.home_assertion_digest != home_assertion.digest
            or host_acceptance.assurance_profile != home_assertion.assurance_profile
            or not set(host_acceptance.accepted_endpoints).issubset(home_assertion.endpoints)
            or not set(host_acceptance.accepted_data_classes).issubset(home_assertion.allowed_data_classes)
            or host_acceptance.expires_at > home_assertion.expires_at
        ):
            raise AuthenticationError("host acceptance does not bind an exact subset of the home assertion")
        if "ES256" not in home_assertion.algorithms:
            raise AuthenticationError("federation assertion profile lacks the required signature algorithm")
        if not self.assurance_policy.permits_assurance(host_acceptance.assurance_profile):
            raise AuthorizationError("federation assurance is below the configured host floor")
        if any(
            not self.assurance_policy.permits_data_class(value)
            for value in host_acceptance.accepted_data_classes
        ):
            raise AuthorizationError("federation data class exceeds the configured host ceiling")
        metadata = {
            "home_assertion": home_assertion.signed_fields(),
            "host_acceptance": host_acceptance.signed_fields(),
        }
        with self.store.transaction() as connection:
            host_domain = connection.execute(
                "SELECT status FROM domains WHERE domain_id=?", (home_assertion.host_domain_id,)
            ).fetchone()
            if host_domain is None or host_domain["status"] != "active":
                raise AuthorizationError("host trust domain is unavailable")
            existing = connection.execute(
                "SELECT * FROM federation_trusts WHERE host_domain_id=? AND home_domain_id=?",
                (home_assertion.host_domain_id, home_assertion.home_domain_id),
            ).fetchone()
            exact = (
                home_assertion.digest,
                host_acceptance.digest,
                home_assertion.home_key_id,
                host_acceptance.host_key_id,
            )
            if existing is not None:
                stored = (
                    existing["home_assertion_digest"],
                    existing["host_acceptance_digest"],
                    existing["home_key_id"],
                    existing["host_key_id"],
                )
                if stored != exact:
                    raise ConflictError("bilateral trust already names different signed bytes")
                return {
                    "host_domain_id": home_assertion.host_domain_id,
                    "home_domain_id": home_assertion.home_domain_id,
                    "status": existing["status"],
                    "duplicate": True,
                }
            connection.execute(
                """INSERT INTO federation_trusts(
                    host_domain_id,home_domain_id,assurance_profile,home_key_id,home_public_key_pem,
                    host_key_id,home_assertion_digest,host_acceptance_digest,metadata_json,status,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,'active',?)""",
                (
                    home_assertion.host_domain_id,
                    home_assertion.home_domain_id,
                    host_acceptance.assurance_profile,
                    home_assertion.home_key_id,
                    home_key,
                    host_acceptance.host_key_id,
                    home_assertion.digest,
                    host_acceptance.digest,
                    canonical_json(metadata).decode("utf-8"),
                    host_acceptance.expires_at,
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "federation.bilateral_trust_admitted",
                    "home_assertion_digest": home_assertion.digest,
                    "home_domain_id": home_assertion.home_domain_id,
                    "host_acceptance_digest": host_acceptance.digest,
                    "host_domain_id": home_assertion.host_domain_id,
                    "non_transitive": True,
                },
            )
        return {
            "host_domain_id": home_assertion.host_domain_id,
            "home_domain_id": home_assertion.home_domain_id,
            "status": "active",
            "duplicate": False,
            "audit_hash": audit_hash,
        }

    def _current_trust(self, connection: Any, *, host_domain_id: str, home_domain_id: str, now: int) -> Any:
        trust = connection.execute(
            "SELECT * FROM federation_trusts WHERE host_domain_id=? AND home_domain_id=?",
            (host_domain_id, home_domain_id),
        ).fetchone()
        if trust is None or trust["status"] != "active" or int(trust["expires_at"]) <= now:
            raise AuthorizationError("no current cryptographically bilateral trust exists")
        return trust

    def create_invitation(
        self,
        *,
        sponsor: VerifiedActor,
        home_domain_id: str,
        pairwise_subject: str,
        guest_public_key_pem: str,
        guest_key_id: str,
        grants: Iterable[Mapping[str, Any]],
        expires_at: int,
    ) -> dict[str, Any]:
        self._require_enabled()
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        now = int(self.clock())
        if sponsor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or sponsor.principal_id is None or sponsor.harness_id is None:
            raise AuthorizationError("guest sponsorship requires a verified host human plus harness")
        if self.attenuation_policy is not None:
            attenuation_denial = self.attenuation_policy.denial_reason(sponsor.binding_assurance)
            if attenuation_denial is not None:
                raise AuthorizationError(attenuation_denial)
        if home_domain_id == sponsor.domain_id:
            raise ConflictError("federation invitation must target another trust domain")
        if len(pairwise_subject) < 16:
            raise ConflictError("federation invitation requires a pairwise, non-email subject")
        if public_key_thumbprint(guest_public_key_pem) != guest_key_id:
            raise AuthenticationError("guest invitation key identifier does not match the per-host public key")
        try:
            grant_models = tuple(
                grant
                if isinstance(grant, FederationInvitationGrant)
                else FederationInvitationGrant.model_validate(grant)
                for grant in grants
            )
        except PydanticValidationError as exc:
            raise ConflictError("federation grant is not exact") from exc
        if not grant_models:
            raise ConflictError("federation invitation requires at least one exact grant")
        if len(grant_models) > 256:
            raise ConflictError("federation invitation grant count exceeds the bounded profile")
        for grant in grant_models:
            if grant.expires_at > expires_at:
                raise ConflictError("federation grant is not exact or exceeds invitation expiry")
        grant_list = [grant.model_dump(mode="json") for grant in grant_models]
        invitation_id = str(uuid4())
        secret = secrets.token_urlsafe(32)
        with self.store.transaction() as connection:
            domain = connection.execute("SELECT policy_revision FROM domains WHERE domain_id=?", (sponsor.domain_id,)).fetchone()
            if domain is None:
                raise AuthorizationError("host domain is unavailable")
            denial, _revision = validate_actor_state(
                connection,
                actor=sponsor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if denial is not None:
                raise AuthorizationError(f"guest sponsor is not current: {denial}")
            trust = self._current_trust(
                connection,
                host_domain_id=sponsor.domain_id,
                home_domain_id=home_domain_id,
                now=now,
            )
            metadata = json.loads(trust["metadata_json"])
            accepted_classes = set(metadata["host_acceptance"]["accepted_data_classes"])
            if expires_at <= now or expires_at > int(trust["expires_at"]):
                raise ConflictError("federation invitation expiry exceeds bilateral trust")
            if any(grant["data_class"] not in accepted_classes for grant in grant_list):
                raise AuthorizationError("federation grant data class exceeds bilateral trust")
            stored = _StoredInvitationTranscript(
                assurance_profile=trust["assurance_profile"],
                grants=grant_models,
                guest_key_id=guest_key_id,
                guest_public_key_thumbprint=guest_key_id,
                guest_public_key_pem=guest_public_key_pem,
                home_assertion_digest=trust["home_assertion_digest"],
                home_domain_id=home_domain_id,
                host_domain_id=sponsor.domain_id,
                invitation_id=invitation_id,
                pairwise_subject=pairwise_subject,
                sponsor_principal_id=sponsor.principal_id,
                expires_at=expires_at,
                secret_hash=canonical_digest({"secret": secret}),
            )
            transcript = stored.transaction_fields()
            digest = canonical_digest(transcript)
            connection.execute(
                """INSERT INTO federation_invitations(
                    invitation_id,host_domain_id,home_domain_id,sponsor_principal_id,invitation_digest,grant_json,expires_at
                ) VALUES(?,?,?,?,?,?,?)""",
                (
                    invitation_id,
                    sponsor.domain_id,
                    home_domain_id,
                    sponsor.principal_id,
                    digest,
                    canonical_json(stored.model_dump(mode="json")).decode("utf-8"),
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "federation.invitation_created",
                    "digest": digest,
                    "home_domain_id": home_domain_id,
                    "invitation_id": invitation_id,
                    "sponsor": sponsor.audit_view(),
                },
            )
        return {"invitation_id": invitation_id, "secret": secret, "transaction_digest": digest, "expires_at": expires_at}

    def _admit_invitation_in_transaction(
        self,
        connection: Any,
        *,
        invitation: Any,
        transcript: _StoredInvitationTranscript,
        assertion: GuestIdentityAssertion,
        trust: Any,
        now: int,
    ) -> dict[str, Any]:
        invitation_id = str(invitation["invitation_id"])
        guest_id = str(uuid4())
        harness_id = str(uuid4())
        credential_id = str(uuid4())
        connection.execute(
            """INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,'active',?)""",
            (
                guest_id,
                invitation["host_domain_id"],
                invitation["home_domain_id"],
                assertion.pairwise_subject,
                invitation["sponsor_principal_id"],
                invitation["expires_at"],
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,guest_id,kind,display_name,status,binding_assurance,
                capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,'federated_guest',?,'active',?,?,1,?)""",
            (
                harness_id,
                invitation["host_domain_id"],
                guest_id,
                f"guest:{assertion.pairwise_subject[:24]}",
                assertion.assurance_profile,
                canonical_json({"foreign_authority_imported": False}).decode("utf-8"),
                now,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)""",
            (
                credential_id,
                harness_id,
                assertion.guest_harness_key_id,
                transcript.guest_public_key_pem,
                now,
                min(assertion.expires_at, invitation["expires_at"]),
            ),
        )
        grant_ids: list[str] = []
        if transcript.grants and self.policy_engine is None:
            raise AuthorizationError("guest admission requires the corporate policy/grant engine")
        for grant in transcript.grants:
            grant_id = str(uuid4())
            grant_ids.append(grant_id)
            connection.execute(
                """INSERT INTO guest_entitlements(
                    grant_id,guest_id,action,resource_pattern,data_class,expires_at
                ) VALUES(?,?,?,?,?,?)""",
                (
                    grant_id,
                    guest_id,
                    grant.action,
                    grant.resource_pattern,
                    grant.data_class.value,
                    grant.expires_at,
                ),
            )
            grant_json = {
                "grant_id": grant_id,
                "domain_id": invitation["host_domain_id"],
                "principal_id": guest_id,
                "harness_id": harness_id,
                "actions": [grant.action],
                "resources": [grant.resource_pattern],
                "input_sources": [grant.input_source],
                "output_sinks": [grant.output_sink],
                "data_classes": [grant.data_class.value],
                "max_uses": grant.max_uses,
                "expires_at": datetime.fromtimestamp(grant.expires_at, UTC).isoformat(),
                "revoked_at": None,
            }
            assert self.policy_engine is not None
            self.policy_engine.grants._insert_in_transaction(
                connection,
                grant=TaskGrant.model_validate(grant_json),
                when=datetime.fromtimestamp(now, UTC),
                issuance_evidence={
                    "kind": "bilateral_guest_admission",
                    "invitation_id": invitation_id,
                    "invitation_digest": invitation["invitation_digest"],
                    "home_assertion_digest": trust["home_assertion_digest"],
                },
            )
        consumed = connection.execute(
            """UPDATE federation_invitations SET consumed_at=?
                 WHERE invitation_id=? AND consumed_at IS NULL AND revoked_at IS NULL""",
            (now, invitation_id),
        )
        if consumed.rowcount != 1:
            raise ConflictError("federation invitation lifecycle changed before admission")
        actor = VerifiedActor(
            kind=ActorKind.HOST_GUEST_HARNESS,
            domain_id=invitation["host_domain_id"],
            guest_id=guest_id,
            harness_id=harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance=assertion.assurance_profile,
        )
        audit_hash = self.store.append_audit(
            connection,
            {
                "action": "federation.guest_created",
                "actor": actor.audit_view(),
                "assurance_profile": assertion.assurance_profile,
                "guest_id": guest_id,
                "home_assertion_digest": trust["home_assertion_digest"],
                "home_domain_id": invitation["home_domain_id"],
                "host_domain_id": invitation["host_domain_id"],
                "invitation_id": invitation_id,
            },
        )
        return {
            "guest_id": guest_id,
            "harness_id": harness_id,
            "credential_id": credential_id,
            "host_domain_id": invitation["host_domain_id"],
            "home_domain_id": invitation["home_domain_id"],
            "pairwise_subject": assertion.pairwise_subject,
            "status": "active",
            "grant_ids": grant_ids,
            "actor": actor,
            "audit_hash": audit_hash,
        }

    def accept_invitation(
        self,
        *,
        invitation_id: str,
        secret: str,
        assertion: GuestIdentityAssertion,
        home_signature: str,
    ) -> dict[str, Any]:
        self._require_enabled()
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        now = int(self.clock())
        proof_failure: AuthenticationError | None = None
        result: dict[str, Any] | None = None
        with self.store.transaction() as connection:
            lock = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
            invitation = connection.execute(
                "SELECT * FROM federation_invitations WHERE invitation_id=?" + lock,
                (invitation_id,),
            ).fetchone()
            if (
                invitation is None
                or invitation["consumed_at"] is not None
                or invitation["revoked_at"] is not None
                or invitation["expires_at"] <= now
            ):
                raise AuthorizationError("federation invitation is invalid")
            if self._invitation_failure_count(connection, invitation_id) >= self.invitation_failure_limit:
                raise AuthenticationError("federation invitation proof is invalid")
            trust = self._current_trust(
                connection,
                host_domain_id=invitation["host_domain_id"],
                home_domain_id=invitation["home_domain_id"],
                now=now,
            )
            transcript = self._stored_invitation(invitation["grant_json"])
            if (
                transcript.invitation_id != invitation_id
                or transcript.host_domain_id != invitation["host_domain_id"]
                or transcript.home_domain_id != invitation["home_domain_id"]
                or transcript.sponsor_principal_id != invitation["sponsor_principal_id"]
                or transcript.expires_at != invitation["expires_at"]
                or canonical_digest(transcript.transaction_fields())
                != invitation["invitation_digest"]
            ):
                raise AuthorizationError("federation invitation state is invalid")
            try:
                if assertion.invitation_id != invitation_id:
                    raise AuthenticationError("guest assertion invitation binding mismatch")
                if not (assertion.issued_at <= now < assertion.expires_at):
                    raise AuthenticationError("guest assertion is outside its validity interval")
                expected = (
                    invitation["invitation_digest"],
                    invitation["host_domain_id"],
                    invitation["home_domain_id"],
                    trust["home_key_id"],
                    transcript.pairwise_subject,
                    transcript.guest_key_id,
                    transcript.guest_public_key_thumbprint,
                    trust["assurance_profile"],
                )
                presented = (
                    assertion.invitation_digest,
                    assertion.host_domain_id,
                    assertion.home_domain_id,
                    assertion.home_key_id,
                    assertion.pairwise_subject,
                    assertion.guest_harness_key_id,
                    assertion.guest_harness_key_thumbprint,
                    assertion.assurance_profile,
                )
                if presented != expected or assertion.expires_at > invitation["expires_at"]:
                    raise AuthenticationError(
                        "guest assertion does not bind the exact invitation/trust/key"
                    )
                verify_signature(
                    trust["home_public_key_pem"],
                    "agentnet.federation.assertion.v1",
                    assertion.signed_fields(),
                    home_signature,
                )
                if not secrets.compare_digest(
                    transcript.secret_hash,
                    canonical_digest({"secret": secret}),
                ):
                    raise AuthenticationError("federation invitation proof is invalid")
            except AuthenticationError as exc:
                self._record_invitation_failure(
                    connection,
                    invitation_id=invitation_id,
                    host_domain_id=invitation["host_domain_id"],
                    now=now,
                )
                proof_failure = exc
            if proof_failure is None:
                result = self._admit_invitation_in_transaction(
                    connection,
                    invitation=invitation,
                    transcript=transcript,
                    assertion=assertion,
                    trust=trust,
                    now=now,
                )
        if proof_failure is not None:
            raise AuthenticationError("federation invitation proof is invalid") from proof_failure
        if result is None:  # defensive fail-close
            raise AuthorizationError("federation invitation could not be admitted")
        return result

    def reissue_locked_invitation(
        self,
        *,
        sponsor: VerifiedActor,
        invitation_id: str,
        expected_invitation_digest: str,
    ) -> dict[str, Any]:
        """Atomically revoke one locked invitation and mint exact fresh proof."""

        self._require_enabled()
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        now = int(self.clock())
        if (
            sponsor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or sponsor.principal_id is None
            or sponsor.harness_id is None
        ):
            raise AuthorizationError("invitation reissue requires the exact verified sponsor")
        with self.store.transaction() as connection:
            lock = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
            invitation = connection.execute(
                "SELECT * FROM federation_invitations WHERE invitation_id=?" + lock,
                (invitation_id,),
            ).fetchone()
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (sponsor.domain_id,),
            ).fetchone()
            if domain is None:
                raise AuthorizationError("host domain is unavailable")
            denial, _revision = validate_actor_state(
                connection,
                actor=sponsor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if (
                denial is not None
                or invitation is None
                or invitation["host_domain_id"] != sponsor.domain_id
                or invitation["sponsor_principal_id"] != sponsor.principal_id
                or invitation["consumed_at"] is not None
                or invitation["revoked_at"] is not None
                or int(invitation["expires_at"]) <= now
                or invitation["invitation_digest"] != expected_invitation_digest
            ):
                raise AuthorizationError("federation invitation is invalid")
            failure_count = self._invitation_failure_count(connection, invitation_id)
            if failure_count < self.invitation_failure_limit:
                raise ConflictError("federation invitation is not locked")
            old = self._stored_invitation(invitation["grant_json"])
            if canonical_digest(old.transaction_fields()) != expected_invitation_digest:
                raise AuthorizationError("federation invitation state is invalid")
            self._current_trust(
                connection,
                host_domain_id=old.host_domain_id,
                home_domain_id=old.home_domain_id,
                now=now,
            )
            new_invitation_id = str(uuid4())
            secret = secrets.token_urlsafe(32)
            updated_value = old.model_dump(mode="python") | {
                "invitation_id": new_invitation_id,
                "secret_hash": canonical_digest({"secret": secret}),
            }
            replacement = _StoredInvitationTranscript.model_validate(updated_value)
            digest = canonical_digest(replacement.transaction_fields())
            revoked = connection.execute(
                """UPDATE federation_invitations SET revoked_at=?
                     WHERE invitation_id=? AND consumed_at IS NULL AND revoked_at IS NULL""",
                (now, invitation_id),
            )
            if revoked.rowcount != 1:
                raise ConflictError("federation invitation lifecycle changed before reissue")
            connection.execute(
                """INSERT INTO federation_invitations(
                    invitation_id,host_domain_id,home_domain_id,sponsor_principal_id,
                    invitation_digest,grant_json,expires_at
                ) VALUES(?,?,?,?,?,?,?)""",
                (
                    new_invitation_id,
                    invitation["host_domain_id"],
                    invitation["home_domain_id"],
                    invitation["sponsor_principal_id"],
                    digest,
                    canonical_json(replacement.model_dump(mode="json")).decode("utf-8"),
                    invitation["expires_at"],
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "federation.invitation_reissued",
                    "failed_attempts": failure_count,
                    "new_invitation_digest": digest,
                    "new_invitation_id": new_invitation_id,
                    "old_invitation_digest": expected_invitation_digest,
                    "old_invitation_id": invitation_id,
                    "sponsor": sponsor.audit_view(),
                },
            )
        return {
            "invitation_id": new_invitation_id,
            "secret": secret,
            "transaction_digest": digest,
            "expires_at": int(invitation["expires_at"]),
            "reissued_from": invitation_id,
            "audit_hash": audit_hash,
        }

    def authorize_guest_operation(
        self,
        *,
        actor: VerifiedActor,
        asserted_host_domain_id: str,
        asserted_home_domain_id: str,
        grant_use: GrantUse,
        classification: Classification,
        operation_class: OperationClass = OperationClass.BUSINESS,
        when: datetime | None = None,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        """Authorize one host-local guest operation in one transaction.

        The host/home domain context, current bilateral trust, current guest
        binding, exact use-counted grant, policy revision, decision record and
        audit record share one transaction.  The configured federation
        capability is a deny-only process limit and is never an authority
        source.
        """

        self._require_enabled()
        if self.policy_engine is None:
            raise AuthorizationError("federated operations require the corporate policy engine")
        if actor.kind is not ActorKind.HOST_GUEST_HARNESS or actor.guest_id is None:
            raise AuthorizationError("federated operation requires a host-local guest actor")
        if actor.domain_id != asserted_host_domain_id:
            raise AuthorizationError("federated operation host-domain context mismatch")
        require_direct_bilateral(
            host_domain_id=asserted_host_domain_id,
            admitted_home_domain_id=asserted_home_domain_id,
            asserted_home_domain_id=asserted_home_domain_id,
        )
        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        with self.store.transaction() as connection:
            guest = connection.execute("SELECT * FROM guests WHERE guest_id=?", (actor.guest_id,)).fetchone()
            if guest is None:
                raise AuthorizationError("federated guest state is missing")
            require_direct_bilateral(
                host_domain_id=asserted_host_domain_id,
                admitted_home_domain_id=str(guest["home_domain_id"]),
                asserted_home_domain_id=asserted_home_domain_id,
            )
            if guest["host_domain_id"] != asserted_host_domain_id:
                raise AuthorizationError("federated operation crossed its host domain")
            trust = self._current_trust(
                connection,
                host_domain_id=asserted_host_domain_id,
                home_domain_id=asserted_home_domain_id,
                now=now,
            )
            metadata = json.loads(trust["metadata_json"])
            accepted_classes = set(metadata["host_acceptance"]["accepted_data_classes"])
            if classification.value not in accepted_classes:
                raise AuthorizationError("federated operation data class exceeds bilateral trust")
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?", (asserted_host_domain_id,)
            ).fetchone()
            if domain is None:
                raise AuthorizationError("federated operation host domain is unavailable")
            request = AuthorizationRequest(
                actor=actor,
                action=grant_use.action,
                resource=grant_use.resource,
                operation_class=operation_class,
                classification=classification,
                policy_revision=int(domain["policy_revision"]),
                context={
                    "host_domain_id": asserted_host_domain_id,
                    "home_domain_id": asserted_home_domain_id,
                    "bilateral_trust_digest": trust["host_acceptance_digest"],
                },
                grant_use=grant_use,
            )
            decision = self.policy_engine._decide_in_transaction(
                connection,
                request,
                when=when,
                phase_hook=phase_hook,
            )
            if decision.allowed:
                audit_hash = self.store.append_audit(
                    connection,
                    {
                        "action": "federation.guest_operation_authorized",
                        "decision_id": decision.decision_id,
                        "grant_id": grant_use.grant_id,
                        "guest_id": actor.guest_id,
                        "home_domain_id": asserted_home_domain_id,
                        "host_domain_id": asserted_host_domain_id,
                    },
                )
            else:
                audit_hash = None
        if not decision.allowed:
            raise AuthorizationError(decision.reason)
        return {
            "allowed": True,
            "decision_id": decision.decision_id,
            "grant_id": grant_use.grant_id,
            "host_domain_id": asserted_host_domain_id,
            "home_domain_id": asserted_home_domain_id,
            "audit_hash": audit_hash,
        }

    def accept_home_revocation(
        self,
        *,
        signal: HomeRevocationSignal,
        home_signature: str,
    ) -> dict[str, Any]:
        """Apply a fresh home-domain signal as immediate deny-only containment."""

        self._require_enabled()
        now = int(self.clock())
        if not (signal.issued_at <= now < signal.expires_at) or now - signal.issued_at > 300:
            raise AuthenticationError("home revocation signal is stale or outside its validity interval")
        with self.store.transaction() as connection:
            trust = connection.execute(
                "SELECT * FROM federation_trusts WHERE host_domain_id=? AND home_domain_id=?",
                (signal.host_domain_id, signal.home_domain_id),
            ).fetchone()
            if trust is None or trust["home_key_id"] != signal.home_key_id:
                raise AuthenticationError("home revocation signal key is not pinned by bilateral trust")
            verify_signature(
                trust["home_public_key_pem"],
                "agentnet.federation.revocation.v1",
                signal.signed_fields(),
                home_signature,
            )
            metadata = json.loads(trust["metadata_json"])
            current_epoch = int(trust["revocation_epoch"])
            previous_digest = metadata.get("last_revocation_signal_digest")
            if signal.revocation_epoch == current_epoch and previous_digest == signal.digest:
                return {
                    "host_domain_id": signal.host_domain_id,
                    "home_domain_id": signal.home_domain_id,
                    "status": "revoked",
                    "duplicate": True,
                    "revocation_epoch": current_epoch,
                }
            if signal.revocation_epoch <= current_epoch:
                raise AuthenticationError("home revocation signal is stale or conflicts at the current epoch")
            metadata["last_revocation_signal_digest"] = signal.digest
            metadata["last_revocation_reason_code"] = signal.reason_code
            connection.execute(
                """UPDATE federation_trusts
                      SET status='revoked',revocation_epoch=?,metadata_json=?
                    WHERE host_domain_id=? AND home_domain_id=?""",
                (
                    signal.revocation_epoch,
                    canonical_json(metadata).decode("utf-8"),
                    signal.host_domain_id,
                    signal.home_domain_id,
                ),
            )
            guest_rows = connection.execute(
                "SELECT guest_id FROM guests WHERE host_domain_id=? AND home_domain_id=? AND status='active'",
                (signal.host_domain_id, signal.home_domain_id),
            ).fetchall()
            guest_ids = [str(row["guest_id"]) for row in guest_rows]
            for guest_id in guest_ids:
                self._revoke_guest_rows(
                    connection,
                    guest_id=guest_id,
                    now=now,
                    reason=f"home_revocation:{signal.reason_code}",
                )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "federation.home_revocation_applied",
                    "home_domain_id": signal.home_domain_id,
                    "host_domain_id": signal.host_domain_id,
                    "reason_code": signal.reason_code,
                    "revocation_epoch": signal.revocation_epoch,
                    "signal_digest": signal.digest,
                    "revoked_guest_count": len(guest_ids),
                },
            )
        return {
            "host_domain_id": signal.host_domain_id,
            "home_domain_id": signal.home_domain_id,
            "status": "revoked",
            "duplicate": False,
            "revocation_epoch": signal.revocation_epoch,
            "revoked_guest_count": len(guest_ids),
            "audit_hash": audit_hash,
        }

    def _revoke_guest_rows(
        self,
        connection: Any,
        *,
        guest_id: str,
        now: int,
        reason: str,
    ) -> None:
        harnesses = connection.execute(
            "SELECT harness_id FROM harnesses WHERE guest_id=?",
            (guest_id,),
        ).fetchall()
        when = datetime.fromtimestamp(now, UTC)
        for harness in harnesses:
            self.relationships._cascade_revoke_for_harness_in_transaction(
                connection,
                harness_id=str(harness["harness_id"]),
                when=when,
                reason=reason,
            )
        connection.execute("UPDATE guests SET status='revoked' WHERE guest_id=?", (guest_id,))
        connection.execute(
            "UPDATE guest_entitlements SET revoked_at=? WHERE guest_id=? AND revoked_at IS NULL",
            (now, guest_id),
        )
        connection.execute(
            "UPDATE task_grants SET revoked_at=? WHERE principal_id=? AND revoked_at IS NULL",
            (now, guest_id),
        )
        connection.execute(
            "UPDATE harnesses SET status='revoked',credential_epoch=credential_epoch+1 WHERE guest_id=?",
            (guest_id,),
        )
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE harness_id IN (SELECT harness_id FROM harnesses WHERE guest_id=?)",
            (guest_id,),
        )

    @staticmethod
    def security_guest_revocation_binding(
        *,
        guest_id: str,
        reason: str,
    ) -> tuple[str, dict[str, str]]:
        """Return the exact policy binding for the deny-only security kill.

        This is intentionally a separate action from sponsorship.  Holding it
        grants no invitation, guest-data, renewal, reassignment, or general
        sponsor capability; it authorizes only revocation of the named
        host-local guest.
        """

        if not guest_id:
            raise AuthorizationError("security guest revocation requires an exact guest identifier")
        if not re.fullmatch(r"[a-z][a-z0-9_.-]{0,63}", reason):
            raise AuthorizationError("guest revocation requires a bounded reason code")
        return f"guest:{guest_id}", {
            "schema": "agentnet.federation.security-guest-revocation.v1",
            "guest_id": guest_id,
            "reason": reason,
        }

    def security_revoke_guest(
        self,
        *,
        authority: IssuanceAuthority,
        guest_id: str,
        reason: str,
    ) -> dict[str, Any]:
        """Apply a sponsor-independent, narrowly scoped host security kill.

        The transport-resolved human actor and exact current positive
        entitlement are reloaded inside the mutation transaction.  A caller
        cannot assert a verified role, reuse a sponsor decision, cross a
        domain, or turn this deny-only capability into guest/sponsor authority.
        """

        self._require_enabled()
        actor = authority.actor
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or actor.principal_id is None:
            raise AuthorizationError("security guest revocation requires current host-human authority")
        resource, request = self.security_guest_revocation_binding(
            guest_id=guest_id,
            reason=reason,
        )
        now = int(self.clock())
        with self.store.transaction() as connection:
            policy_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="federation.guest.security_revoke",
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            guest = connection.execute(
                "SELECT * FROM guests WHERE guest_id=?",
                (guest_id,),
            ).fetchone()
            if guest is None or guest["host_domain_id"] != actor.domain_id:
                raise AuthorizationError("guest is not visible in this host domain")
            if guest["status"] != "active":
                raise ConflictError("security guest revocation is stale or replayed")
            self._revoke_guest_rows(
                connection,
                guest_id=guest_id,
                now=now,
                reason=f"security_guest_revoked:{reason}",
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "federation.guest_security_revoked",
                    "actor": actor.audit_view(),
                    "guest_id": guest_id,
                    "host_domain_id": actor.domain_id,
                    "policy_decision_id": authority.policy_decision_id,
                    "policy_revision": policy_revision,
                    "reason": reason,
                    "sponsor_independent": guest["sponsor_principal_id"] != actor.principal_id,
                    "authority_scope": "deny_only_exact_guest",
                },
            )
        return {
            "guest_id": guest_id,
            "status": "revoked",
            "revocation_basis": "domain_security_admin",
            "audit_hash": audit_hash,
        }

    def revoke_guest(self, *, host_actor: VerifiedActor, guest_id: str, reason: str) -> dict[str, Any]:
        self._require_enabled()
        if host_actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or host_actor.principal_id is None:
            raise AuthorizationError("guest revocation requires verified host authority")
        if not re.fullmatch(r"[a-z][a-z0-9_.-]{0,63}", reason):
            raise AuthorizationError("guest revocation requires a bounded reason code")
        now = int(self.clock())
        with self.store.transaction() as connection:
            domain = connection.execute("SELECT policy_revision FROM domains WHERE domain_id=?", (host_actor.domain_id,)).fetchone()
            if domain is None:
                raise AuthorizationError("host domain is unavailable")
            denial, _revision = validate_actor_state(
                connection,
                actor=host_actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if denial is not None:
                raise AuthorizationError(f"guest revocation actor is not current: {denial}")
            guest = connection.execute("SELECT * FROM guests WHERE guest_id=?", (guest_id,)).fetchone()
            if guest is None or guest["host_domain_id"] != host_actor.domain_id:
                raise AuthorizationError("guest is not visible in this host domain")
            if guest["sponsor_principal_id"] != host_actor.principal_id:
                raise AuthorizationError("secure default permits only the current guest sponsor to revoke")
            self._revoke_guest_rows(
                connection,
                guest_id=guest_id,
                now=now,
                reason=f"guest_revoked:{reason}",
            )
            audit_hash = self.store.append_audit(
                connection,
                {"action": "federation.guest_revoked", "actor": host_actor.audit_view(), "guest_id": guest_id, "reason": reason},
            )
        return {"guest_id": guest_id, "status": "revoked", "audit_hash": audit_hash}
