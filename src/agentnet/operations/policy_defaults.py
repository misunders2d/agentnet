"""Conservative configurable policy defaults for PD-001 through PD-011.

These defaults make behavior executable without inventing owner approval.  An
organization can version and replace them explicitly; external credentials and
human ceremonies authenticated independently of the requesting harness still have
to exist at runtime.
"""

from __future__ import annotations

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest


ATTENTION_EXCEPTION_TYPES = frozenset(
    {
        "confirmed_security_incident",
        "exact_approval_required",
        "high_risk_elevation_expiring",
        "terminal_unrecoverable_failure",
    }
)
_ASSURANCE_RANK = {"lab": 0, "os_bound": 1, "hardware_bound": 2}
_CLASSIFICATION_RANK = {
    Classification.C0_PUBLIC: 0,
    Classification.C1_INTERNAL: 1,
    Classification.C2_RESTRICTED: 2,
    Classification.C3_SEALED: 3,
}


class _Policy(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class IdentityPolicy(_Policy):
    principal_key: Literal["oidc_issuer_subject"] = "oidc_issuer_subject"
    verified_email_is_alias_only: Literal[True] = True
    preserve_alias_history: Literal[True] = True
    ambiguous_mapping_action: Literal["quarantine"] = "quarantine"
    credential_ttl_seconds: int = Field(default=3_600, ge=300, le=86_400)
    always_on_credential_ttl_seconds: int = Field(default=86_400, ge=3_600, le=604_800)
    credential_renewal_window_seconds: int = Field(default=21_600, ge=300, le=86_400)

    @model_validator(mode="after")
    def _renewal_window_precedes_expiry(self) -> "IdentityPolicy":
        if self.credential_renewal_window_seconds >= self.always_on_credential_ttl_seconds:
            raise ValueError("credential renewal window must be shorter than always-on TTL")
        return self


class EnrollmentApprovalPolicy(_Policy):
    primary_authentication: Literal["webauthn_uv"] = "webauthn_uv"
    out_of_band_required: Literal[True] = True
    out_of_band_min_entropy_bits: int = Field(default=80, ge=64, le=256)
    transaction_ttl_seconds: int = Field(default=300, ge=60, le=600)
    maximum_attempts: int = Field(default=5, ge=1, le=10)
    recovery_approver_threshold: int = Field(default=2, ge=2, le=5)


class AttenuationPolicy(_Policy):
    harness_session_device_are_deny_only: Literal[True] = True
    posture_grants_positive_authority: Literal[False] = False
    unapproved_posture_action: Literal["deny"] = "deny"
    minimum_binding_assurance: Literal["os_bound", "hardware_bound"] = "os_bound"

    def denial_reason(self, binding_assurance: str) -> str | None:
        rank = _ASSURANCE_RANK.get(binding_assurance)
        if rank is None or rank < _ASSURANCE_RANK[self.minimum_binding_assurance]:
            return "binding_assurance_below_policy_floor"
        return None


class ElevationPolicy(_Policy):
    ordinary_approval_threshold: int = Field(default=1, ge=1, le=5)
    high_impact_approval_threshold: int = Field(default=2, ge=2, le=5)
    ordinary_ttl_seconds: int = Field(default=900, ge=60, le=3_600)
    high_impact_ttl_seconds: int = Field(default=300, ge=60, le=900)
    maximum_uses: int = Field(default=1, ge=1, le=10)
    beneficiary_may_approve: Literal[False] = False
    break_glass_enabled: bool = False
    break_glass_ttl_seconds: int = Field(default=300, ge=60, le=900)

    def threshold_for(self, risk_class: str) -> int:
        return (
            self.ordinary_approval_threshold
            if risk_class == "ordinary"
            else self.high_impact_approval_threshold
        )

    def ttl_for(self, risk_class: str) -> int:
        if risk_class == "ordinary":
            return self.ordinary_ttl_seconds
        if risk_class == "break_glass":
            return self.break_glass_ttl_seconds
        return self.high_impact_ttl_seconds


class RevocationPolicy(_Policy):
    enforcement: Literal["next_decision"] = "next_decision"
    new_issuance_during_revocation_outage: Literal["deny"] = "deny"
    uncertain_compromise_window: Literal["quarantine"] = "quarantine"
    preserve_inert_accepted_history: Literal[True] = True
    accepted_history_max_retention_days: int = Field(default=30, ge=1, le=3_650)


class RoomGovernancePolicy(_Policy):
    history_mode: Literal["from_join", "no_prior_history"] = "from_join"
    guest_prior_history: Literal[False] = False
    sequencer: Literal["owner_domain"] = "owner_domain"
    governance_threshold: int = Field(default=1, ge=1, le=5)
    recovery_threshold: int = Field(default=1, ge=1, le=5)
    explicit_transfer_required: Literal[True] = True
    tombstone_on_unrecoverable_owner_loss: Literal[True] = True


class ConfidentialityPolicy(_Policy):
    c1_processing: Literal["managed", "isolated_service"] = "managed"
    c2_processing: Literal["isolated_service"] = "isolated_service"
    c3_enabled_by_default: Literal[False] = False
    c3_requires_validated_mls: Literal[True] = True
    key_holding_services_visible: Literal[True] = True
    model_training_allowed: Literal[False] = False

    def processing_profile(self, classification: Classification) -> str:
        if classification is Classification.C0_PUBLIC:
            return "gateway"
        if classification is Classification.C1_INTERNAL:
            return self.c1_processing
        if classification is Classification.C2_RESTRICTED:
            return self.c2_processing
        return "mls_members_only"


class FederationAssurancePolicy(_Policy):
    non_transitive: Literal[True] = True
    default_maximum_data_class: Literal["C0", "C1"] = "C1"
    minimum_home_assurance: Literal["os_bound", "hardware_bound"] = "os_bound"
    ordinary_operations_proof: Literal["fresh_home_assertion"] = "fresh_home_assertion"
    high_risk_operations_proof: Literal["fresh_host_reproof_and_admin_approval"] = (
        "fresh_host_reproof_and_admin_approval"
    )
    revocation_signal_failure: Literal["privileged_hold"] = "privileged_hold"

    def permits_data_class(self, value: str) -> bool:
        try:
            classification = Classification(value)
        except ValueError:
            return False
        ceiling = Classification(self.default_maximum_data_class)
        return _CLASSIFICATION_RANK[classification] <= _CLASSIFICATION_RANK[ceiling]

    def permits_assurance(self, value: str) -> bool:
        rank = _ASSURANCE_RANK.get(value)
        return rank is not None and rank >= _ASSURANCE_RANK[self.minimum_home_assurance]


class OutagePolicy(_Policy):
    new_credential_or_grant_issuance: Literal["deny"] = "deny"
    privileged_operations: Literal["hold"] = "hold"
    low_risk_continuity_max_seconds: int = Field(default=300, ge=0, le=900)
    audit_backlog_max_records: int = Field(default=10_000, ge=100, le=1_000_000)


class OperationsPolicy(_Policy):
    supported_os: tuple[Literal["linux", "macos", "windows"], ...] = (
        "linux",
        "macos",
        "windows",
    )
    supported_architectures: tuple[Literal["x86_64", "aarch64"], ...] = ("x86_64", "aarch64")
    regions: Literal[1] = 1
    accepted_durable_enabled: Literal[False] = False
    declared_rpo_seconds: None = None
    target_rto_seconds: None = None
    per_actor_requests_per_minute: int = Field(default=600, ge=10, le=100_000)
    per_domain_requests_per_minute: int = Field(default=10_000, ge=10, le=1_000_000)
    global_requests_per_minute: int = Field(default=50_000, ge=10, le=5_000_000)
    per_message_recipient_limit: int = Field(default=100, ge=1, le=1_000)
    pending_delivery_backpressure_limit: int = Field(default=10_000, ge=10, le=1_000_000)
    fairness_burst_limit: int = Field(default=25, ge=1, le=1_000)
    max_operation_hops: int = Field(default=8, ge=1, le=64)
    circuit_breaker_failure_threshold: int = Field(default=5, ge=2, le=100)
    circuit_breaker_reset_seconds: int = Field(default=60, ge=1, le=3_600)
    per_actor_artifact_bytes: int = Field(default=67_108_864, ge=1, le=1_099_511_627_776)
    per_domain_artifact_bytes: int = Field(default=1_073_741_824, ge=1, le=17_592_186_044_416)
    artifact_deduplication: Literal["disabled"] = "disabled"
    retention_days: int = Field(default=30, ge=1, le=3_650)

    @model_validator(mode="after")
    def coherent_artifact_limits(self) -> "OperationsPolicy":
        if self.per_domain_artifact_bytes < self.per_actor_artifact_bytes:
            raise ValueError("domain artifact-byte limit cannot be lower than the actor limit")
        if not (
            self.per_actor_requests_per_minute
            <= self.per_domain_requests_per_minute
            <= self.global_requests_per_minute
        ):
            raise ValueError("actor/domain/global request ceilings must be monotonically attenuated")
        return self


class AttentionPolicy(_Policy):
    routine_traffic: Literal["silent"] = "silent"
    exception_types: tuple[str, ...] = (
        "confirmed_security_incident",
        "exact_approval_required",
        "high_risk_elevation_expiring",
        "terminal_unrecoverable_failure",
    )
    content: Literal["redacted"] = "redacted"
    quiet_hours_start: int = Field(default=22, ge=0, le=23)
    quiet_hours_end: int = Field(default=7, ge=0, le=23)

    @field_validator("exception_types")
    @classmethod
    def bounded_exception_types(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if len(value) != len(set(value)) or not set(value).issubset(ATTENTION_EXCEPTION_TYPES):
            raise ValueError("attention exceptions must be a unique subset of the secure exception catalog")
        return value

    def allows(self, event_type: str) -> bool:
        return event_type in self.exception_types

    def is_quiet_hour(self, when: datetime) -> bool:
        hour = when.hour
        if self.quiet_hours_start < self.quiet_hours_end:
            return self.quiet_hours_start <= hour < self.quiet_hours_end
        return hour >= self.quiet_hours_start or hour < self.quiet_hours_end


class SecurePolicyDefaults(_Policy):
    schema_version: Literal["1.0"] = "1.0"
    revision: int = Field(default=1, ge=1)
    identity: IdentityPolicy = Field(default_factory=IdentityPolicy)
    enrollment_approval: EnrollmentApprovalPolicy = Field(default_factory=EnrollmentApprovalPolicy)
    attenuation: AttenuationPolicy = Field(default_factory=AttenuationPolicy)
    elevation: ElevationPolicy = Field(default_factory=ElevationPolicy)
    revocation: RevocationPolicy = Field(default_factory=RevocationPolicy)
    rooms: RoomGovernancePolicy = Field(default_factory=RoomGovernancePolicy)
    confidentiality: ConfidentialityPolicy = Field(default_factory=ConfidentialityPolicy)
    federation: FederationAssurancePolicy = Field(default_factory=FederationAssurancePolicy)
    outage: OutagePolicy = Field(default_factory=OutagePolicy)
    operations: OperationsPolicy = Field(default_factory=OperationsPolicy)
    attention: AttentionPolicy = Field(default_factory=AttentionPolicy)

    @model_validator(mode="after")
    def coherent_thresholds(self) -> "SecurePolicyDefaults":
        if self.elevation.high_impact_approval_threshold <= self.elevation.ordinary_approval_threshold:
            raise ValueError("high-impact elevation threshold must exceed ordinary elevation")
        if self.elevation.high_impact_ttl_seconds > self.elevation.ordinary_ttl_seconds:
            raise ValueError("high-impact elevation lifetime cannot exceed ordinary elevation")
        if self.attention.quiet_hours_start == self.attention.quiet_hours_end:
            raise ValueError("quiet hours must describe a nonempty interval")
        if self.operations.retention_days > self.revocation.accepted_history_max_retention_days:
            raise ValueError("retention exceeds the post-revocation inert-history ceiling")
        return self

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))
