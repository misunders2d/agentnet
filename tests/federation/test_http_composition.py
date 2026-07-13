from __future__ import annotations

import json
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path

import httpx
import pytest
from pydantic import ValidationError as PydanticValidationError
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse

from agentnet.authorization.policy import HumanEntitlement
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import ExtensionError
from agentnet.federation.service import (
    FederationService,
    GuestIdentityAssertion,
    HomeFederationAssertion,
    HomeRevocationSignal,
    HostTrustAcceptance,
)
from agentnet.federation_http import create_federation_routes
from agentnet.http_api import _body_and_actor
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.config import (
    ExtensionConfig,
    FeatureFlags,
    FederationPublicKeyPin,
    FederationTrustConfig,
)
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.client import proof_headers


def _trust_config(home_key: P256KeyPair, host_key: P256KeyPair) -> FederationTrustConfig:
    return FederationTrustConfig(
        home_domain_keys=(
            FederationPublicKeyPin(
                domain_id="partner.example",
                key_id=home_key.thumbprint,
                public_key_pem=home_key.public_pem,
            ),
        ),
        host_policy_keys=(
            FederationPublicKeyPin(
                domain_id="corp.example",
                key_id=host_key.thumbprint,
                public_key_pem=host_key.public_pem,
            ),
        ),
    )


def _config(tmp_path: Path, trust: FederationTrustConfig) -> ExtensionConfig:
    return ExtensionConfig(
        domain_id="corp.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
        features=FeatureFlags(federation=True),
        federation_trust=trust,
        server_agent_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.FEDERATION,
            }
        ),
        component_evidence={"federation": "focused-bilateral-test"},
    )


def _allow(core: CommunicationCore, actor: VerifiedActor, action: str, resource: str) -> None:
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id or "",
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.now(UTC) + timedelta(hours=1),
        )
    )


def _headers(key, actor: VerifiedActor, method: str, path: str, body: bytes) -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id or "",
            credential_id=actor.credential_id or "",
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
            method=method,
            scheme="http",
            authority="127.0.0.1",
            path=path,
            query="",
            body=body,
        )
    )


async def _post(client, key, actor: VerifiedActor, path: str, value: dict):
    body = canonical_json(value)
    return await client.post(
        path,
        content=body,
        headers={"Content-Type": "application/json", **_headers(key, actor, "POST", path, body)},
    )


def _app(core: CommunicationCore) -> Starlette:
    async def errors(_request: Request, exc: Exception):
        if isinstance(exc, ExtensionError):
            return JSONResponse(exc.public_detail(), status_code=exc.http_status)
        if isinstance(exc, (PydanticValidationError, json.JSONDecodeError)):
            return JSONResponse({"code": "invalid_request"}, status_code=422)
        return JSONResponse({"code": "internal_error"}, status_code=500)

    return Starlette(
        routes=create_federation_routes(core, _body_and_actor),
        exception_handlers={Exception: errors},
    )


def test_federation_trust_config_is_required_exact_and_never_inert(tmp_path: Path) -> None:
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    trust = _trust_config(home_key, host_key)
    base = {
        "domain_id": "corp.example",
        "data_dir": tmp_path / "data",
        "database_url": f"sqlite:///{tmp_path / 'core.sqlite3'}",
        "artifact_dir": tmp_path / "artifacts",
        "public_base_url": "http://127.0.0.1",
        "server_agent_capabilities": frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.FEDERATION,
            }
        ),
        "component_evidence": {"federation": "focused-bilateral-test"},
    }
    with pytest.raises(PydanticValidationError, match="explicit home-domain"):
        ExtensionConfig(**base, features=FeatureFlags(federation=True))
    with pytest.raises(PydanticValidationError, match="inert"):
        ExtensionConfig(**base, federation_trust=trust)
    with pytest.raises(PydanticValidationError, match="exact local host"):
        ExtensionConfig(
            **base,
            features=FeatureFlags(federation=True),
            federation_trust=trust.model_copy(
                update={
                    "host_policy_keys": (
                        trust.host_policy_keys[0].model_copy(update={"domain_id": "other.example"}),
                    )
                }
            ),
        )


@pytest.mark.anyio
async def test_federation_http_composes_bilateral_invitation_guest_use_and_both_revocations(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    sponsor, sponsor_key = identity_factory(domain="corp.example", binding_assurance="os_bound")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    trust_config = _trust_config(home_key, host_key)
    core = CommunicationCore(_config(tmp_path, trust_config), store)
    core.federation = FederationService(
        store,
        enabled=True,
        runtime_capabilities=core.config.server_agent_capabilities,
        policy_engine=core.policy,
        trusted_domain_keys=trust_config.trusted_domain_key_map,
        host_policy_keys=trust_config.host_policy_key_map,
        assurance_policy=core.config.policies.federation,
        attenuation_policy=core.config.policies.attenuation,
        outage_gate=core.outage,
        clock=lambda: now,
    )

    home = HomeFederationAssertion(
        host_domain_id="corp.example",
        home_domain_id="partner.example",
        home_key_id=home_key.thumbprint,
        endpoints=("https://a2a.partner.example",),
        algorithms=("ES256",),
        allowed_data_classes=("C0", "C1"),
        assurance_profile="os_bound",
        revocation_endpoint="https://id.partner.example/revocations",
        incident_contact="security@partner.example",
        issued_at=now - 1,
        expires_at=now + 3600,
        nonce="home-federation-http-nonce-0001",
    )
    acceptance = HostTrustAcceptance(
        host_domain_id=home.host_domain_id,
        home_domain_id=home.home_domain_id,
        host_key_id=host_key.thumbprint,
        home_key_id=home.home_key_id,
        home_assertion_digest=home.digest,
        accepted_endpoints=home.endpoints,
        accepted_data_classes=home.allowed_data_classes,
        assurance_profile="os_bound",
        non_transitive=True,
        issued_at=now - 1,
        expires_at=now + 1800,
        nonce="host-federation-http-nonce-0001",
    )
    _allow(core, sponsor, "federation.trust.admit", "federation:partner.example")
    _allow(core, sponsor, "federation.invitation.create", "federation:partner.example")

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        admitted = await _post(
            client,
            sponsor_key,
            sponsor,
            "/v1/federation/trusts",
            {
                "home_assertion": home.model_dump(mode="json"),
                "home_signature": home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
                "host_acceptance": acceptance.model_dump(mode="json"),
                "host_signature": host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
            },
        )
        assert admitted.status_code == 201, admitted.text

        invitation_value = {
            "home_domain_id": "partner.example",
            "pairwise_subject": "pairwise-federation-http-subject",
            "guest_public_key_pem": guest_key.public_pem,
            "guest_key_id": guest_key.thumbprint,
            "grants": [
                {
                    "action": "message.send",
                    "resource_pattern": "room:contract-http",
                    "data_class": "C1",
                    "input_source": "guest.request",
                    "output_sink": "room:contract-http",
                    "max_uses": 2,
                    "expires_at": now + 900,
                }
            ],
            "expires_at": now + 900,
        }
        coerced_grant = await _post(
            client,
            sponsor_key,
            sponsor,
            "/v1/federation/invitations",
            invitation_value
            | {"grants": [invitation_value["grants"][0] | {"max_uses": "2"}]},
        )
        assert coerced_grant.status_code == 422

        invitation_response = await _post(
            client,
            sponsor_key,
            sponsor,
            "/v1/federation/invitations",
            invitation_value,
        )
        assert invitation_response.status_code == 201, invitation_response.text
        invitation = invitation_response.json()
        locked_assertion = GuestIdentityAssertion(
            invitation_id=invitation["invitation_id"],
            invitation_digest=invitation["transaction_digest"],
            host_domain_id="corp.example",
            home_domain_id="partner.example",
            home_key_id=home_key.thumbprint,
            pairwise_subject="pairwise-federation-http-subject",
            guest_harness_key_id=guest_key.thumbprint,
            guest_harness_key_thumbprint=guest_key.thumbprint,
            assurance_profile="os_bound",
            issued_at=now - 1,
            expires_at=now + 600,
            nonce="guest-federation-http-assertion-nonce-0001",
        )
        accept_path = f"/v1/federation/invitations/{invitation['invitation_id']}/accept"
        wrong_proof = canonical_json(
            {
                "secret": "x" * 43,
                "assertion": locked_assertion.model_dump(mode="json"),
                "home_signature": home_key.sign(
                    "agentnet.federation.assertion.v1",
                    locked_assertion.signed_fields(),
                ),
            }
        )
        for _attempt in range(5):
            denied_attempt = await client.post(
                accept_path,
                content=wrong_proof,
                headers={"Content-Type": "application/json"},
            )
            assert denied_attempt.status_code == 401

        old_locked = await client.post(
            accept_path,
            content=canonical_json(
                {
                    "secret": invitation["secret"],
                    "assertion": locked_assertion.model_dump(mode="json"),
                    "home_signature": home_key.sign(
                        "agentnet.federation.assertion.v1",
                        locked_assertion.signed_fields(),
                    ),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert old_locked.status_code == 401

        _allow(
            core,
            sponsor,
            "federation.invitation.reissue",
            f"federation-invitation:{invitation['invitation_id']}",
        )
        reissued = await _post(
            client,
            sponsor_key,
            sponsor,
            f"/v1/federation/invitations/{invitation['invitation_id']}/reissue",
            {"expected_invitation_digest": invitation["transaction_digest"]},
        )
        assert reissued.status_code == 201, reissued.text
        replacement = reissued.json()
        assert replacement["reissued_from"] == invitation["invitation_id"]
        invitation = replacement
        assertion = GuestIdentityAssertion(
            invitation_id=invitation["invitation_id"],
            invitation_digest=invitation["transaction_digest"],
            host_domain_id="corp.example",
            home_domain_id="partner.example",
            home_key_id=home_key.thumbprint,
            pairwise_subject="pairwise-federation-http-subject",
            guest_harness_key_id=guest_key.thumbprint,
            guest_harness_key_thumbprint=guest_key.thumbprint,
            assurance_profile="os_bound",
            issued_at=now - 1,
            expires_at=now + 600,
            nonce="guest-federation-http-assertion-nonce-0002",
        )
        accept_path = f"/v1/federation/invitations/{invitation['invitation_id']}/accept"
        accepted = await client.post(
            accept_path,
            content=canonical_json(
                {
                    "secret": invitation["secret"],
                    "assertion": assertion.model_dump(mode="json"),
                    "home_signature": home_key.sign(
                        "agentnet.federation.assertion.v1", assertion.signed_fields()
                    ),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert accepted.status_code == 201, accepted.text
        accepted_value = accepted.json()
        guest_actor = VerifiedActor.model_validate(accepted_value["actor"])
        use = {
            "grant_id": accepted_value["grant_ids"][0],
            "action": "message.send",
            "resource": "room:contract-http",
            "input_source": "guest.request",
            "output_sink": "room:contract-http",
            "data_class": "C1",
        }
        authorized = await _post(
            client,
            guest_key,
            guest_actor,
            "/v1/federation/guest-operations/authorize",
            {"grant_use": use, "classification": "C1"},
        )
        assert authorized.status_code == 200, authorized.text
        assert authorized.json()["allowed"] is True

        injected_identity = await client.post(
            accept_path,
            content=canonical_json(
                {
                    "secret": invitation["secret"],
                    "assertion": assertion.model_dump(mode="json"),
                    "home_signature": "not-a-signature",
                    "principal_id": sponsor.principal_id,
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert injected_identity.status_code == 422

        guest_id = accepted_value["guest_id"]
        _allow(core, sponsor, "federation.guest.revoke", f"guest:{guest_id}")
        revoked = await _post(
            client,
            sponsor_key,
            sponsor,
            f"/v1/federation/guests/{guest_id}/revoke",
            {"reason": "contract_ended"},
        )
        assert revoked.status_code == 200, revoked.text
        denied = await _post(
            client,
            guest_key,
            guest_actor,
            "/v1/federation/guest-operations/authorize",
            {"grant_use": use, "classification": "C1"},
        )
        assert denied.status_code == 401

        signal = HomeRevocationSignal(
            host_domain_id="corp.example",
            home_domain_id="partner.example",
            home_key_id=home_key.thumbprint,
            revocation_epoch=2,
            reason_code="domain_emergency",
            issued_at=now - 1,
            expires_at=now + 60,
            nonce="home-federation-http-revocation-nonce-0001",
        )
        home_revoked = await client.post(
            "/v1/federation/revocations/home",
            content=canonical_json(
                {
                    "signal": signal.model_dump(mode="json"),
                    "home_signature": home_key.sign(
                        "agentnet.federation.revocation.v1", signal.signed_fields()
                    ),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert home_revoked.status_code == 200, home_revoked.text
        assert home_revoked.json()["status"] == "revoked"


@pytest.mark.anyio
async def test_http_domain_security_revocation_is_exact_authenticated_and_deny_only(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    sponsor, sponsor_key = identity_factory(domain="corp.example", binding_assurance="os_bound")
    security_admin, security_key = identity_factory(
        domain="corp.example",
        binding_assurance="os_bound",
    )
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    trust_config = _trust_config(home_key, host_key)
    core = CommunicationCore(_config(tmp_path, trust_config), store)
    core.federation = FederationService(
        store,
        enabled=True,
        runtime_capabilities=core.config.server_agent_capabilities,
        policy_engine=core.policy,
        trusted_domain_keys=trust_config.trusted_domain_key_map,
        host_policy_keys=trust_config.host_policy_key_map,
        assurance_policy=core.config.policies.federation,
        attenuation_policy=core.config.policies.attenuation,
        outage_gate=core.outage,
        clock=lambda: now,
    )
    home = HomeFederationAssertion(
        host_domain_id="corp.example",
        home_domain_id="partner.example",
        home_key_id=home_key.thumbprint,
        endpoints=("https://a2a.partner.example",),
        algorithms=("ES256",),
        allowed_data_classes=("C1",),
        assurance_profile="os_bound",
        revocation_endpoint="https://id.partner.example/revocations",
        incident_contact="security@partner.example",
        issued_at=now - 1,
        expires_at=now + 3600,
        nonce="home-security-revoke-http-nonce-0001",
    )
    acceptance = HostTrustAcceptance(
        host_domain_id="corp.example",
        home_domain_id="partner.example",
        host_key_id=host_key.thumbprint,
        home_key_id=home_key.thumbprint,
        home_assertion_digest=home.digest,
        accepted_endpoints=home.endpoints,
        accepted_data_classes=home.allowed_data_classes,
        assurance_profile="os_bound",
        non_transitive=True,
        issued_at=now - 1,
        expires_at=now + 1800,
        nonce="host-security-revoke-http-nonce-0001",
    )
    core.federation.admit_bilateral_trust(
        home_assertion=home,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        host_acceptance=acceptance,
        host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    )
    invitation = core.federation.create_invitation(
        sponsor=sponsor,
        home_domain_id="partner.example",
        pairwise_subject="pairwise-security-revoke-http-subject",
        guest_public_key_pem=guest_key.public_pem,
        guest_key_id=guest_key.thumbprint,
        grants=(
            {
                "action": "message.send",
                "resource_pattern": "room:security-revoke-http",
                "data_class": "C1",
                "input_source": "guest.request",
                "output_sink": "room:security-revoke-http",
                "max_uses": 1,
                "expires_at": now + 900,
            },
        ),
        expires_at=now + 900,
    )
    assertion = GuestIdentityAssertion(
        invitation_id=invitation["invitation_id"],
        invitation_digest=invitation["transaction_digest"],
        host_domain_id="corp.example",
        home_domain_id="partner.example",
        home_key_id=home_key.thumbprint,
        pairwise_subject="pairwise-security-revoke-http-subject",
        guest_harness_key_id=guest_key.thumbprint,
        guest_harness_key_thumbprint=guest_key.thumbprint,
        assurance_profile="os_bound",
        issued_at=now - 1,
        expires_at=now + 600,
        nonce="guest-security-revoke-http-nonce-0001",
    )
    guest = core.federation.accept_invitation(
        invitation_id=invitation["invitation_id"],
        secret=invitation["secret"],
        assertion=assertion,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", assertion.signed_fields()),
    )
    guest_id = guest["guest_id"]
    _allow(
        core,
        security_admin,
        "federation.guest.security_revoke",
        f"guest:{guest_id}",
    )
    revoke_path = f"/v1/federation/guests/{guest_id}/security-revoke"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        sponsor_only = await _post(
            client,
            sponsor_key,
            sponsor,
            revoke_path,
            {"reason": "sponsor_offboarded"},
        )
        assert sponsor_only.status_code == 404, sponsor_only.text

        caller_role_injection = await _post(
            client,
            security_key,
            security_admin,
            revoke_path,
            {"reason": "sponsor_offboarded", "role": "domain-security-admin"},
        )
        assert caller_role_injection.status_code == 422

        revoked = await _post(
            client,
            security_key,
            security_admin,
            revoke_path,
            {"reason": "sponsor_offboarded"},
        )
        assert revoked.status_code == 200, revoked.text
        assert revoked.json()["revocation_basis"] == "domain_security_admin"

        replay = await _post(
            client,
            security_key,
            security_admin,
            revoke_path,
            {"reason": "sponsor_offboarded"},
        )
        assert replay.status_code == 409

        unauthorized_guest_key = P256KeyPair.generate()
        no_sponsor_expansion = await _post(
            client,
            security_key,
            security_admin,
            "/v1/federation/invitations",
            {
                "home_domain_id": "partner.example",
                "pairwise_subject": "pairwise-security-admin-cannot-sponsor",
                "guest_public_key_pem": unauthorized_guest_key.public_pem,
                "guest_key_id": unauthorized_guest_key.thumbprint,
                "grants": [
                    {
                        "action": "message.send",
                        "resource_pattern": "room:security-revoke-http",
                        "data_class": "C1",
                        "input_source": "guest.request",
                        "output_sink": "room:security-revoke-http",
                        "max_uses": 1,
                        "expires_at": now + 60,
                    }
                ],
                "expires_at": now + 60,
            },
        )
        assert no_sponsor_expansion.status_code == 404
