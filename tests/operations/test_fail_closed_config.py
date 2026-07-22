from __future__ import annotations

import json

import pytest
from pydantic import ValidationError

from agentnet.components.bakeoff import adoption_ready
from agentnet.components.registry import BASELINE_COMPONENTS
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.http_api import create_app
from agentnet.operations.config import (
    ApprovalServiceClientConfig,
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    FeatureFlags,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    OIDCTokenEndpointAuthMethod,
    RuntimeProfile,
)
from agentnet.operations.config_migration import load_config_json
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


def _confidential_oidc_config(
    *,
    method: OIDCTokenEndpointAuthMethod = OIDCTokenEndpointAuthMethod.CLIENT_SECRET_POST,
    client_secret_env: str | None = "AGENTNET_TEST_OIDC_CLIENT_SECRET",
) -> OIDCEnrollmentConfig:
    approver_key = P256KeyPair.generate()
    return OIDCEnrollmentConfig(
        issuer="https://idp.example",
        client_id="agentnet-ordinary-extension",
        redirect_uri="https://agent.example/v1/enrollment/oidc/callback",
        token_endpoint_auth_method=method,
        client_secret_env=client_secret_env,
        verifier_id="independent-webauthn-service",
        trusted_approvers=(
            IndependentApproverConfig(
                principal_id="security-owner",
                signer_key_id=approver_key.thumbprint,
                public_key_pem=approver_key.public_pem,
                allowed_purposes=frozenset(
                    {
                        "authorization.bootstrap_plan.approve",
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


def test_oidc_public_config_requires_exact_auth_method_secret_reference_pair() -> None:
    public = _confidential_oidc_config(
        method=OIDCTokenEndpointAuthMethod.NONE,
        client_secret_env=None,
    )
    assert public.token_endpoint_auth_method is OIDCTokenEndpointAuthMethod.NONE
    assert public.client_secret_env is None

    with pytest.raises(ValidationError, match="requires client_secret_env"):
        _confidential_oidc_config(client_secret_env=None)
    with pytest.raises(ValidationError, match="cannot configure client_secret_env"):
        _confidential_oidc_config(
            method=OIDCTokenEndpointAuthMethod.NONE,
            client_secret_env="AGENTNET_TEST_OIDC_CLIENT_SECRET",
        )
    with pytest.raises(ValidationError):
        _confidential_oidc_config(client_secret_env="not-an-env-name")


def test_guided_approval_client_config_is_reference_only_and_fail_closed(
    store,
    tmp_path,
    monkeypatch,
) -> None:
    approval = ApprovalServiceClientConfig(
        origin="https://approval-internal.example",
        public_origin="https://approval.example",
        service_credential_env="AGENTNET_TEST_APPROVAL_CORE_TOKEN",
        approver_principal_id="security-owner",
    )
    enrollment = _confidential_oidc_config().model_copy(
        update={"approval_service": approval}
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
    exported = config.redacted_export()
    assert exported["oidc_enrollment"]["approval_service"] == {
        "origin": "https://approval-internal.example",
        "public_origin": "https://approval.example",
        "service_credential_env": "AGENTNET_TEST_APPROVAL_CORE_TOKEN",
        "approver_principal_id": "security-owner",
        "request_timeout_seconds": 5.0,
        "maximum_response_bytes": 262144,
    }
    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    monkeypatch.setenv("AGENTNET_TEST_OIDC_CLIENT_SECRET", "O" * 43)
    with pytest.raises(GateBlocked, match="credential is unavailable"):
        CommunicationCore(config, store)
    monkeypatch.setenv("AGENTNET_TEST_APPROVAL_CORE_TOKEN", "A" * 43)
    core = CommunicationCore(config, store)
    try:
        assert core.approval_service_client is not None
        assert core.bootstrap_plan_service is not None
        assert core.bootstrap_plan_service.public_approval_url == "https://approval.example/approval"
        paths = {route.path for route in create_app(core).routes}
        assert {
            "/v1/bootstrap-plan/begin",
            "/v1/bootstrap-plan/status",
            "/v1/bootstrap-plan/complete",
        } <= paths
        assert all(not path.startswith("/v1/authority-bootstrap") for path in paths)
        rendered = repr(core.approval_service_client) + json.dumps(
            config.redacted_export(), sort_keys=True
        )
        assert "A" * 43 not in rendered
    finally:
        core.close()

    for origin in (
        "http://approval.example",
        "https://user@approval.example",
        "https://approval.example/path",
        "https://APPROVAL.example",
    ):
        with pytest.raises(ValidationError, match="approval service origin"):
            ApprovalServiceClientConfig(
                origin=origin,
                public_origin="https://approval.example",
                service_credential_env="AGENTNET_TEST_APPROVAL_CORE_TOKEN",
                approver_principal_id="security-owner",
            )

    for public_origin in (
        "http://approval.example",
        "https://user@approval.example",
        "https://approval.example/path",
        "https://APPROVAL.example",
    ):
        with pytest.raises(ValidationError, match="public approval origin"):
            ApprovalServiceClientConfig(
                origin="https://approval-internal.example",
                public_origin=public_origin,
                service_credential_env="AGENTNET_TEST_APPROVAL_CORE_TOKEN",
                approver_principal_id="security-owner",
            )


def test_oidc_secret_reference_is_exported_but_runtime_value_is_not(
    store,
    tmp_path,
    monkeypatch,
) -> None:
    enrollment = _confidential_oidc_config()
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        public_base_url="https://agent.example",
        data_dir=tmp_path / "data",
        artifact_dir=tmp_path / "artifacts",
        oidc_enrollment=enrollment,
    )
    exported = config.redacted_export()
    assert exported["oidc_enrollment"]["client_secret_env"] == "AGENTNET_TEST_OIDC_CLIENT_SECRET"
    assert exported["oidc_enrollment"]["token_endpoint_auth_method"] == "client_secret_post"

    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    with pytest.raises(GateBlocked, match="client secret environment variable"):
        CommunicationCore(config, store)

    monkeypatch.setenv("AGENTNET_TEST_OIDC_CLIENT_SECRET", "runtime-secret-sentinel-one")
    first = CommunicationCore(config, store)
    assert first.oidc_enrollment is not None
    first_provider = first.oidc_enrollment.provider
    assert "runtime-secret-sentinel-one" not in repr(first_provider.config)
    assert "runtime-secret-sentinel-one" not in json.dumps(config.redacted_export(), sort_keys=True)

    monkeypatch.setenv("AGENTNET_TEST_OIDC_CLIENT_SECRET", "runtime-secret-sentinel-two")
    second = CommunicationCore(config, store)
    assert second.oidc_enrollment is not None
    assert second.oidc_enrollment.provider.config.client_secret == "runtime-secret-sentinel-two"
    assert first_provider.config.client_secret == "runtime-secret-sentinel-one"


def test_config_migration_rejects_embedded_client_secret_but_accepts_env_reference() -> None:
    with pytest.raises(Exception, match="embedded secret material"):
        load_config_json(json.dumps({"schema_version": "1.0", "client_secret": "forbidden"}))

    enrollment = _confidential_oidc_config()
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_backend="postgres-manifest",
        public_base_url="https://agent.example",
        oidc_enrollment=enrollment,
    )
    loaded = load_config_json(config.model_dump_json())
    assert loaded.oidc_enrollment is not None
    assert loaded.oidc_enrollment.client_secret_env == "AGENTNET_TEST_OIDC_CLIENT_SECRET"


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
                        "authorization.bootstrap_plan.approve",
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
                        "authorization.bootstrap_plan.approve",
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
