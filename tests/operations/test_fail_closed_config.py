from __future__ import annotations

import pytest
from pydantic import ValidationError

from agentnet.components.bakeoff import adoption_ready
from agentnet.components.registry import BASELINE_COMPONENTS
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.operations.config import (
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    FeatureFlags,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
)
from agentnet.security.signatures import P256KeyPair


def test_backup_trust_is_public_only_domain_bound_epoch_monotonic_and_revocation_safe() -> None:
    key = P256KeyPair.generate()
    pin = BackupSealKeyConfig(
        key_id=key.thumbprint,
        key_epoch=3,
        public_key_pem=key.public_pem,
        not_before=1_700_000_000,
    )
    trust = BackupTrustConfig(
        domain_id="corp.example",
        trust_root_revision=4,
        minimum_key_epoch=3,
        active_signer_key_id=key.thumbprint,
        keys=(pin,),
    )
    config = ExtensionConfig(domain_id="corp.example", backup_trust=trust)
    exported = config.redacted_export()
    assert exported["backup_trust"]["keys"][0]["public_key_pem"] == key.public_pem
    assert "PRIVATE KEY" not in str(exported)

    with pytest.raises(ValidationError, match="exact local domain"):
        ExtensionConfig(domain_id="other.example", backup_trust=trust)
    with pytest.raises(ValidationError, match="public-key pin"):
        BackupSealKeyConfig(
            key_id="wrong-key-identifier",
            key_epoch=3,
            public_key_pem=key.public_pem,
            not_before=1_700_000_000,
        )
    with pytest.raises(ValidationError, match="non-revoked active"):
        BackupTrustConfig(
            domain_id="corp.example",
            trust_root_revision=5,
            minimum_key_epoch=3,
            active_signer_key_id=key.thumbprint,
            keys=(pin.model_copy(update={"revoked_at": 1_700_000_001}),),
        )
    successor = P256KeyPair.generate()
    with pytest.raises(ValidationError, match="epochs must be unique"):
        BackupTrustConfig(
            domain_id="corp.example",
            trust_root_revision=5,
            minimum_key_epoch=3,
            active_signer_key_id=successor.thumbprint,
            keys=(
                pin,
                BackupSealKeyConfig(
                    key_id=successor.thumbprint,
                    key_epoch=3,
                    public_key_pem=successor.public_pem,
                    not_before=1_700_000_001,
                ),
            ),
        )
    retired = pin.model_copy(update={"retired_at": 1_700_000_010})
    current = BackupSealKeyConfig(
        key_id=successor.thumbprint,
        key_epoch=4,
        public_key_pem=successor.public_pem,
        not_before=1_700_000_011,
    )
    rotated = BackupTrustConfig(
        domain_id="corp.example",
        trust_root_revision=5,
        minimum_key_epoch=3,
        active_signer_key_id=successor.thumbprint,
        keys=(retired, current),
    )
    assert rotated.key_by_id(successor.thumbprint) == current
    with pytest.raises(ValidationError, match="highest-epoch"):
        BackupTrustConfig(
            domain_id="corp.example",
            trust_root_revision=5,
            minimum_key_epoch=3,
            active_signer_key_id=key.thumbprint,
            keys=(pin, current.model_copy(update={"retired_at": 1_700_000_020})),
        )
    with pytest.raises(ValidationError, match="non-active"):
        BackupTrustConfig(
            domain_id="corp.example",
            trust_root_revision=5,
            minimum_key_epoch=3,
            active_signer_key_id=successor.thumbprint,
            keys=(pin, current),
        )
    with pytest.raises(ValidationError, match="cannot precede retirement"):
        BackupSealKeyConfig(
            **(
                pin.model_dump()
                | {"retired_at": 1_700_000_020, "revoked_at": 1_700_000_019}
            )
        )


def test_server_agent_requires_postgres_enrollment_and_durable_capabilities() -> None:
    with pytest.raises(ValidationError):
        ExtensionConfig(profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT)


def test_high_risk_features_require_evidence() -> None:
    with pytest.raises(ValidationError):
        ExtensionConfig(features=FeatureFlags(sealed_rooms=True))


def test_absent_component_is_not_a_passed_bakeoff() -> None:
    postgres = next(item for item in BASELINE_COMPONENTS if item.name == "PostgreSQL")
    ready, missing = adoption_ready(postgres, {})
    assert ready is False
    assert "decision=not_available" in missing


def test_non_loopback_http_public_origin_is_rejected_but_https_is_allowed() -> None:
    with pytest.raises(ValidationError, match="loopback"):
        ExtensionConfig(public_base_url="http://api.example")
    assert ExtensionConfig(public_base_url="https://api.example").service_authority == "api.example"


def test_server_agent_capabilities_use_one_enum_and_are_attenuating_prerequisites() -> None:
    base = {
        "profile": RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        "database_url": "postgresql://agentnet@postgres/agentnet",
        "artifact_backend": "postgres-manifest",
        "enrolled_harness_id": "harness-1",
        "enrolled_credential_id": "credential-1",
        "public_base_url": "https://agent.example",
    }
    config = ExtensionConfig(**base)
    assert config.server_agent_capabilities == {
        ServerAgentCapability.OFFLINE_CUSTODY,
        ServerAgentCapability.ARTIFACT_STORAGE,
    }
    with pytest.raises(ValidationError, match="a2a_gateway"):
        ExtensionConfig(**base, features=FeatureFlags(public_a2a=True))
    with pytest.raises(ValidationError):
        ExtensionConfig(**base, owner_decisions={"PD-001": "legacy-placeholder"})


def test_same_ordinary_server_can_bootstrap_only_through_exact_oidc_and_independent_approval(
    store,
    tmp_path,
    monkeypatch,
) -> None:
    approver_key = P256KeyPair.generate()
    enrollment = OIDCEnrollmentConfig(
        issuer="https://idp.example",
        client_id="agentnet-ordinary-extension",
        redirect_uri="https://agent.example/v1/enrollment/oidc/callback",
        verifier_id="independent-webauthn-service",
        trusted_approvers=(
            IndependentApproverConfig(
                principal_id="security-owner",
                signer_key_id=approver_key.thumbprint,
                public_key_pem=approver_key.public_pem,
                allowed_purposes=frozenset(
                    {
                        "authorization.entitlement.bootstrap.approve",
                        "authorization.elevation.approve",
                        "identity.credential.recover.approve",
                        "identity.enrollment.approve",
                        "identity.harness.revoke.approve",
                        "organization.relationship.accept",
                    }
                ),
            ),
        ),
    )
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        public_base_url="https://agent.example",
        data_dir=tmp_path / "data",
        artifact_dir=tmp_path / "artifacts",
        oidc_enrollment=enrollment,
    )
    assert config.enrolled_harness_id is None
    assert config.oidc_enrollment is enrollment
    monkeypatch.setattr(
        "agentnet.core.app.is_verified_postgresql_store",
        lambda _store: True,
    )
    core = CommunicationCore(config, store)
    assert core.approval_verifier is core.relationships.approval_verifier
    assert core.oidc_enrollment is not None
    assert core.oidc_enrollment.enrollment.approval_verifier is core.approval_verifier
    with pytest.raises(GateBlocked, match="ambiguously"):
        CommunicationCore(config, store, approval_verifier=core.approval_verifier)
    enrollment_only = enrollment.model_copy(
        update={
            "trusted_approvers": (
                enrollment.trusted_approvers[0].model_copy(
                    update={"allowed_purposes": frozenset({"identity.enrollment.approve"})}
                ),
            )
        }
    )
    with pytest.raises(ValidationError, match="high-impact ceremony"):
        ExtensionConfig(
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            database_url="postgresql://agentnet@postgres/agentnet",
            artifact_backend="postgres-manifest",
            public_base_url="https://agent.example",
            oidc_enrollment=enrollment_only,
        )
    without_relationship_consent = enrollment.model_copy(
        update={
            "trusted_approvers": (
                enrollment.trusted_approvers[0].model_copy(
                    update={
                        "allowed_purposes": enrollment.trusted_approvers[0].allowed_purposes
                        - {"organization.relationship.accept"}
                    }
                ),
            )
        }
    )
    with pytest.raises(ValidationError, match="organization.relationship.accept"):
        ExtensionConfig(
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            database_url="postgresql://agentnet@postgres/agentnet",
            artifact_backend="postgres-manifest",
            public_base_url="https://agent.example",
            oidc_enrollment=without_relationship_consent,
        )
    with pytest.raises(ValidationError, match="redirect URI"):
        ExtensionConfig(
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            database_url="postgresql://agentnet@postgres/agentnet",
            artifact_backend="postgres-manifest",
            public_base_url="https://agent.example",
            oidc_enrollment=enrollment.model_copy(
                update={"redirect_uri": "https://attacker.example/callback"}
            ),
        )
    with pytest.raises(ValidationError, match="only on the always-on"):
        ExtensionConfig(oidc_enrollment=enrollment)


def test_private_oidc_config_requires_explicit_canonical_network_origin_and_jwk_pins() -> None:
    approver_key = P256KeyPair.generate()
    base = {
        "issuer": "https://idp.corp.example",
        "client_id": "agentnet-ordinary-extension",
        "redirect_uri": "https://agent.example/v1/enrollment/oidc/callback",
        "verifier_id": "independent-webauthn-service",
        "trusted_approvers": (
            IndependentApproverConfig(
                principal_id="security-owner",
                signer_key_id=approver_key.thumbprint,
                public_key_pem=approver_key.public_pem,
                allowed_purposes=frozenset(
                    {
                        "authorization.entitlement.bootstrap.approve",
                        "authorization.elevation.approve",
                        "identity.credential.recover.approve",
                        "identity.enrollment.approve",
                        "identity.harness.revoke.approve",
                        "organization.relationship.accept",
                    }
                ),
            ),
        ),
    }
    with pytest.raises(ValidationError, match="explicit endpoint origins"):
        OIDCEnrollmentConfig(
            **base,
            allowed_private_endpoint_cidrs=("10.20.0.0/24",),
            pinned_jwk_thumbprints={"idp-key-1": "a" * 64},
        )
    with pytest.raises(ValidationError, match="JWK thumbprint"):
        OIDCEnrollmentConfig(
            **base,
            allowed_endpoint_origins=("https://idp.corp.example",),
            allowed_private_endpoint_cidrs=("10.20.0.0/24",),
        )
    with pytest.raises(ValidationError, match="canonical private networks"):
        OIDCEnrollmentConfig(
            **base,
            allowed_endpoint_origins=("https://idp.corp.example",),
            allowed_private_endpoint_cidrs=("10.20.0.1/24",),
            pinned_jwk_thumbprints={"idp-key-1": "a" * 64},
        )
    configured = OIDCEnrollmentConfig(
        **base,
        allowed_endpoint_origins=("https://idp.corp.example",),
        allowed_private_endpoint_cidrs=("10.20.0.0/24",),
        pinned_endpoint_addresses=("10.20.0.8",),
        pinned_jwk_thumbprints={"idp-key-1": "a" * 64},
    )
    assert configured.allowed_private_endpoint_cidrs == ("10.20.0.0/24",)
    assert configured.pinned_endpoint_addresses == ("10.20.0.8",)
