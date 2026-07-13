from __future__ import annotations

import time
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta

import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import (
    AuthorizationRequest,
    HumanEntitlement,
    IssuanceAuthority,
)
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import LocalConformancePolicyEngine, OperationClass
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.federation.service import (
    FederationService,
    GuestIdentityAssertion,
    HomeFederationAssertion,
    HomeRevocationSignal,
    HostTrustAcceptance,
)
from agentnet.identity.actors import ActorKind
from agentnet.identity.context import VerifiedContextResolver
from agentnet.identity.credentials import load_credential_binding
from agentnet.operations.policy_defaults import FederationAssurancePolicy
from agentnet.organization import (
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipService,
)
from agentnet.protocol.models import Classification, Relationship
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json


def _trust(service: FederationService, home_key: P256KeyPair, host_key: P256KeyPair, now: int):
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
        nonce="home-federation-metadata-nonce-0001",
    )
    acceptance = HostTrustAcceptance(
        host_domain_id=home.host_domain_id,
        home_domain_id=home.home_domain_id,
        host_key_id=host_key.thumbprint,
        home_key_id=home.home_key_id,
        home_assertion_digest=home.digest,
        accepted_endpoints=home.endpoints,
        accepted_data_classes=home.allowed_data_classes,
        assurance_profile=home.assurance_profile,
        non_transitive=True,
        issued_at=now - 1,
        expires_at=now + 1800,
        nonce="host-federation-acceptance-nonce-0001",
    )
    return home, acceptance


def _service(
    store,
    home_key: P256KeyPair,
    host_key: P256KeyPair,
    now: int,
    *,
    invitation_failure_limit: int = 5,
) -> FederationService:
    return FederationService(
        store,
        enabled=True,
        runtime_capabilities=frozenset({ServerAgentCapability.FEDERATION}),
        policy_engine=LocalConformancePolicyEngine(store),
        trusted_domain_keys={("partner.example", home_key.thumbprint): home_key.public_pem},
        host_policy_keys={("corp.example", host_key.thumbprint): host_key.public_pem},
        clock=lambda: now,
        invitation_failure_limit=invitation_failure_limit,
    )


def _admit_guest(
    service: FederationService,
    *,
    sponsor,
    home_key: P256KeyPair,
    host_key: P256KeyPair,
    guest_key: P256KeyPair,
    now: int,
):
    home, acceptance = _trust(service, home_key, host_key, now)
    service.admit_bilateral_trust(
        home_assertion=home,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        host_acceptance=acceptance,
        host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    )
    invitation = service.create_invitation(
        sponsor=sponsor,
        home_domain_id="partner.example",
        pairwise_subject="pairwise-partner-subject-runtime-91f4",
        guest_public_key_pem=guest_key.public_pem,
        guest_key_id=guest_key.thumbprint,
        grants=(
            {
                "action": "message.send",
                "resource_pattern": "room:contract-1",
                "data_class": "C1",
                "input_source": "guest.request",
                "output_sink": "room:contract-1",
                "max_uses": 3,
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
        pairwise_subject="pairwise-partner-subject-runtime-91f4",
        guest_harness_key_id=guest_key.thumbprint,
        guest_harness_key_thumbprint=guest_key.thumbprint,
        assurance_profile="os_bound",
        issued_at=now - 1,
        expires_at=now + 600,
        nonce="fresh-runtime-guest-assertion-nonce-0001",
    )
    result = service.accept_invitation(
        invitation_id=invitation["invitation_id"],
        secret=invitation["secret"],
        assertion=assertion,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", assertion.signed_fields()),
    )
    use = GrantUse(
        grant_id=result["grant_ids"][0],
        action="message.send",
        resource="room:contract-1",
        input_source="guest.request",
        output_sink="room:contract-1",
        data_class=Classification.C1_INTERNAL,
    )
    return result, use


def _activate_guest_subordinate_relationship(
    store,
    *,
    sponsor,
    guest_actor,
    now: int,
) -> str:
    when = datetime.fromtimestamp(now, UTC)
    relationship = Relationship(
        relationship_id="federated-guest-subordinate-edge",
        domain_id=sponsor.domain_id,
        administrator_harness_id=sponsor.harness_id,
        subordinate_harness_id=guest_actor.harness_id,
        expires_at=when + timedelta(hours=1),
    )
    proposal_expires_at = when + timedelta(minutes=10)
    resource, context = RelationshipService.proposal_binding(
        relationship,
        proposal_expires_at=proposal_expires_at,
    )
    policy = LocalConformancePolicyEngine(store)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sponsor.domain_id,
            principal_id=sponsor.principal_id,
            action="organization.relationship.propose",
            resource_pattern=resource,
            revision=1,
            expires_at=relationship.expires_at,
        )
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=sponsor,
            action="organization.relationship.propose",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=when,
    )
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=guest_actor.guest_id,
        domain_id=sponsor.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        authority_kind="guest",
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="independent-federated-guest-owner-approver",
    )
    relationships = RelationshipService(store, approval_verifier=verifier)
    pending = relationships.propose(
        relationship,
        proposal_expires_at=proposal_expires_at,
        authority=IssuanceAuthority(
            actor=sponsor,
            policy_decision_id=decision.decision_id,
        ),
        when=when,
    )
    receipt = create_independent_approval_receipt(
        approval_key,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(
            pending.consent_transaction.model_dump(mode="json")
        ),
        issued_at=now,
        expires_at=now + 300,
    )
    active = relationships.accept(
        relationship.relationship_id,
        actor=sponsor,
        approval=receipt,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=when,
    )
    assert active.lifecycle_state == "active"
    return active.relationship_id


def test_unilateral_or_unpinned_federation_assertions_fail_closed(store, identity_factory) -> None:
    identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    attacker_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    home, acceptance = _trust(service, home_key, host_key, now)
    with pytest.raises(AuthenticationError):
        service.admit_bilateral_trust(
            home_assertion=home,
            home_signature=attacker_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
            host_acceptance=acceptance,
            host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
        )
    with pytest.raises(AuthenticationError):
        service.admit_bilateral_trust(
            home_assertion=home,
            home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
            host_acceptance=acceptance,
            host_signature=attacker_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
        )
    assert store.fetch_one("SELECT 1 FROM federation_trusts") is None


def test_configured_federation_ceiling_and_assurance_floor_are_enforced(store, identity_factory) -> None:
    identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    home, acceptance = _trust(_service(store, home_key, host_key, now), home_key, host_key, now)

    def service(policy: FederationAssurancePolicy) -> FederationService:
        return FederationService(
            store,
            enabled=True,
            runtime_capabilities=frozenset({ServerAgentCapability.FEDERATION}),
            policy_engine=LocalConformancePolicyEngine(store),
            trusted_domain_keys={("partner.example", home_key.thumbprint): home_key.public_pem},
            host_policy_keys={("corp.example", host_key.thumbprint): host_key.public_pem},
            assurance_policy=policy,
            clock=lambda: now,
        )

    arguments = {
        "home_assertion": home,
        "home_signature": home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        "host_acceptance": acceptance,
        "host_signature": host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    }
    with pytest.raises(AuthorizationError, match="data class"):
        service(FederationAssurancePolicy(default_maximum_data_class="C0")).admit_bilateral_trust(
            **arguments
        )
    with pytest.raises(AuthorizationError, match="assurance"):
        service(FederationAssurancePolicy(minimum_home_assurance="hardware_bound")).admit_bilateral_trust(
            **arguments
        )


def test_guest_admission_creates_distinct_host_harness_key_credential_and_context(store, identity_factory) -> None:
    sponsor, _sponsor_key = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    home, acceptance = _trust(service, home_key, host_key, now)
    admitted = service.admit_bilateral_trust(
        home_assertion=home,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        host_acceptance=acceptance,
        host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    )
    assert admitted["status"] == "active"

    invitation = service.create_invitation(
        sponsor=sponsor,
        home_domain_id="partner.example",
        pairwise_subject="pairwise-partner-subject-8f9ca0",
        guest_public_key_pem=guest_key.public_pem,
        guest_key_id=guest_key.thumbprint,
        grants=(
            {
                "action": "message.send",
                "resource_pattern": "room:contract-1",
                "data_class": "C1",
                "input_source": "guest.request",
                "output_sink": "room:contract-1",
                "max_uses": 3,
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
        pairwise_subject="pairwise-partner-subject-8f9ca0",
        guest_harness_key_id=guest_key.thumbprint,
        guest_harness_key_thumbprint=guest_key.thumbprint,
        assurance_profile="os_bound",
        issued_at=now - 1,
        expires_at=now + 600,
        nonce="fresh-guest-identity-assertion-nonce-0001",
    )
    attacker = P256KeyPair.generate()
    with pytest.raises(AuthenticationError):
        service.accept_invitation(
            invitation_id=invitation["invitation_id"],
            secret=invitation["secret"],
            assertion=assertion,
            home_signature=attacker.sign("agentnet.federation.assertion.v1", assertion.signed_fields()),
        )
    result = service.accept_invitation(
        invitation_id=invitation["invitation_id"],
        secret=invitation["secret"],
        assertion=assertion,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", assertion.signed_fields()),
    )
    actor = result["actor"]
    assert actor.kind is ActorKind.HOST_GUEST_HARNESS
    assert actor.domain_id == "corp.example"
    assert actor.guest_id == result["guest_id"]
    assert actor.principal_id is None
    assert actor.harness_id != sponsor.harness_id
    binding = load_credential_binding(store, result["credential_id"])
    binding.require_active(now=now)
    assert binding.guest_id == result["guest_id"]
    assert binding.principal_id is None
    assert binding.key_id == guest_key.thumbprint

    audience = "urn:agentnet:corp.example:corporate-api"
    proof = create_request_proof(
        guest_key,
        harness_id=result["harness_id"],
        credential_id=result["credential_id"],
        domain_id="corp.example",
        audience=audience,
        method="POST",
        scheme="https",
        authority="api.corp.example",
        path="/v1/guest/action",
        query="",
        body=b"{}",
        timestamp=now,
        nonce="guest-context-proof-nonce-with-enough-entropy-001",
    )
    context = VerifiedContextResolver(
        store,
        service_audience=audience,
        service_scheme="https",
        service_authority="api.corp.example",
    ).resolve(
        proof,
        expected_method="POST",
        expected_scheme="https",
        expected_authority="api.corp.example",
        expected_path="/v1/guest/action",
        expected_query="",
        body=b"{}",
        now=now,
    )
    assert context.actor == actor
    with pytest.raises(AuthorizationError):
        service.accept_invitation(
            invitation_id=invitation["invitation_id"],
            secret=invitation["secret"],
            assertion=assertion,
            home_signature=home_key.sign("agentnet.federation.assertion.v1", assertion.signed_fields()),
        )


def test_invitation_failures_lock_exact_invitation_and_sponsor_reissue_is_atomic(
    store,
    identity_factory,
) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(
        store,
        home_key,
        host_key,
        now,
        invitation_failure_limit=3,
    )
    home, acceptance = _trust(service, home_key, host_key, now)
    service.admit_bilateral_trust(
        home_assertion=home,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        host_acceptance=acceptance,
        host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    )
    grant = {
        "action": "message.send",
        "resource_pattern": "room:locked-invitation",
        "data_class": "C1",
        "input_source": "guest.request",
        "output_sink": "room:locked-invitation",
        "max_uses": 1,
        "expires_at": now + 900,
    }
    invitation = service.create_invitation(
        sponsor=sponsor,
        home_domain_id="partner.example",
        pairwise_subject="pairwise-lockout-subject-0001",
        guest_public_key_pem=guest_key.public_pem,
        guest_key_id=guest_key.thumbprint,
        grants=(grant,),
        expires_at=now + 900,
    )

    def assertion_for(value: dict[str, object]) -> GuestIdentityAssertion:
        return GuestIdentityAssertion(
            invitation_id=str(value["invitation_id"]),
            invitation_digest=str(value["transaction_digest"]),
            host_domain_id="corp.example",
            home_domain_id="partner.example",
            home_key_id=home_key.thumbprint,
            pairwise_subject="pairwise-lockout-subject-0001",
            guest_harness_key_id=guest_key.thumbprint,
            guest_harness_key_thumbprint=guest_key.thumbprint,
            assurance_profile="os_bound",
            issued_at=now - 1,
            expires_at=now + 600,
            nonce="fresh-lockout-guest-assertion-nonce-0001",
        )

    old_assertion = assertion_for(invitation)
    old_signature = home_key.sign(
        "agentnet.federation.assertion.v1",
        old_assertion.signed_fields(),
    )
    with ThreadPoolExecutor(max_workers=3) as executor:
        failed_attempts = [
            executor.submit(
                service.accept_invitation,
                invitation_id=str(invitation["invitation_id"]),
                secret=f"wrong-proof-{attempt}",
                assertion=old_assertion,
                home_signature=old_signature,
            )
            for attempt in range(3)
        ]
    for failed_attempt in failed_attempts:
        with pytest.raises(AuthenticationError, match="proof is invalid"):
            failed_attempt.result()
    counter = store.fetch_one(
        """SELECT used,limit_value FROM quota_counters
             WHERE scope=? AND metric=? AND window_start=0""",
        (
            f"federation-invitation:{invitation['invitation_id']}",
            FederationService.INVITATION_FAILURE_METRIC,
        ),
    )
    assert dict(counter) == {"used": 3, "limit_value": 3}
    with pytest.raises(AuthenticationError, match="proof is invalid"):
        service.accept_invitation(
            invitation_id=str(invitation["invitation_id"]),
            secret=str(invitation["secret"]),
            assertion=old_assertion,
            home_signature=old_signature,
        )

    with ThreadPoolExecutor(max_workers=2) as executor:
        reissues = [
            executor.submit(
                service.reissue_locked_invitation,
                sponsor=sponsor,
                invitation_id=str(invitation["invitation_id"]),
                expected_invitation_digest=str(invitation["transaction_digest"]),
            )
            for _attempt in range(2)
        ]
    replacements: list[dict[str, object]] = []
    denied_reissues = 0
    for reissue in reissues:
        try:
            replacements.append(reissue.result())
        except AuthorizationError:
            denied_reissues += 1
    assert len(replacements) == 1
    assert denied_reissues == 1
    replacement = replacements[0]
    assert replacement["invitation_id"] != invitation["invitation_id"]
    assert replacement["secret"] != invitation["secret"]
    assert store.fetch_one(
        "SELECT revoked_at FROM federation_invitations WHERE invitation_id=?",
        (invitation["invitation_id"],),
    )["revoked_at"] == now

    replacement_assertion = assertion_for(replacement)
    admitted = service.accept_invitation(
        invitation_id=str(replacement["invitation_id"]),
        secret=str(replacement["secret"]),
        assertion=replacement_assertion,
        home_signature=home_key.sign(
            "agentnet.federation.assertion.v1",
            replacement_assertion.signed_fields(),
        ),
    )
    assert admitted["status"] == "active"


@pytest.mark.parametrize(
    "mutation",
    [
        {"max_uses": True},
        {"expires_at": "2000000000"},
        {"unexpected": "ignored"},
        {"data_class": 1},
    ],
)
def test_invitation_grants_are_strict_parsed_before_policy_consumption(
    store,
    identity_factory,
    mutation,
) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    home, acceptance = _trust(service, home_key, host_key, now)
    service.admit_bilateral_trust(
        home_assertion=home,
        home_signature=home_key.sign("agentnet.federation.assertion.v1", home.signed_fields()),
        host_acceptance=acceptance,
        host_signature=host_key.sign("agentnet.federation.assertion.v1", acceptance.signed_fields()),
    )
    grant = {
        "action": "message.send",
        "resource_pattern": "room:strict-invitation",
        "data_class": "C1",
        "input_source": "guest.request",
        "output_sink": "room:strict-invitation",
        "max_uses": 1,
        "expires_at": now + 600,
    }
    grant.update(mutation)
    with pytest.raises(ConflictError, match="grant is not exact"):
        service.create_invitation(
            sponsor=sponsor,
            home_domain_id="partner.example",
            pairwise_subject="pairwise-strict-subject-0001",
            guest_public_key_pem=guest_key.public_pem,
            guest_key_id=guest_key.thumbprint,
            grants=(grant,),
            expires_at=now + 600,
        )


def test_invitation_without_persisted_bilateral_trust_and_revoked_guest_are_denied(store, identity_factory) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    with pytest.raises(AuthorizationError):
        service.create_invitation(
            sponsor=sponsor,
            home_domain_id="partner.example",
            pairwise_subject="pairwise-subject-without-trust",
            guest_public_key_pem=guest_key.public_pem,
            guest_key_id=guest_key.thumbprint,
            grants=(
                {
                    "action": "message.send",
                    "resource_pattern": "room:1",
                    "data_class": "C1",
                    "input_source": "guest.request",
                    "output_sink": "room:1",
                    "max_uses": 1,
                    "expires_at": now + 100,
                },
            ),
            expires_at=now + 100,
        )


def test_guest_operation_is_domain_bound_atomic_and_revocation_is_immediate(store, identity_factory) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    result, use = _admit_guest(
        service,
        sponsor=sponsor,
        home_key=home_key,
        host_key=host_key,
        guest_key=guest_key,
        now=now,
    )
    relationship_id = _activate_guest_subordinate_relationship(
        store,
        sponsor=sponsor,
        guest_actor=result["actor"],
        now=now,
    )

    authorized = service.authorize_guest_operation(
        actor=result["actor"],
        asserted_host_domain_id="corp.example",
        asserted_home_domain_id="partner.example",
        grant_use=use,
        classification=Classification.C1_INTERNAL,
    )
    assert authorized["allowed"] is True
    assert service.policy_engine.grants.uses_for_local_conformance(use.grant_id) == 1

    with pytest.raises(AuthorizationError, match="transitive|bilateral"):
        service.authorize_guest_operation(
            actor=result["actor"],
            asserted_host_domain_id="corp.example",
            asserted_home_domain_id="third.example",
            grant_use=use,
            classification=Classification.C1_INTERNAL,
        )
    assert service.policy_engine.grants.uses_for_local_conformance(use.grant_id) == 1

    def crash_after_grant(stage: str) -> None:
        if stage == "after_grant_consumed":
            raise RuntimeError("crash after federated grant consumption")

    with pytest.raises(RuntimeError, match="crash"):
        service.authorize_guest_operation(
            actor=result["actor"],
            asserted_host_domain_id="corp.example",
            asserted_home_domain_id="partner.example",
            grant_use=use,
            classification=Classification.C1_INTERNAL,
            phase_hook=crash_after_grant,
        )
    assert service.policy_engine.grants.uses_for_local_conformance(use.grant_id) == 1

    revoked = service.revoke_guest(host_actor=sponsor, guest_id=result["guest_id"], reason="contract_ended")
    assert revoked["status"] == "revoked"
    with pytest.raises(AuthorizationError):
        service.authorize_guest_operation(
            actor=result["actor"],
            asserted_host_domain_id="corp.example",
            asserted_home_domain_id="partner.example",
            grant_use=use,
            classification=Classification.C1_INTERNAL,
        )
    assert service.policy_engine.grants.uses_for_local_conformance(use.grant_id) == 1
    credential = store.fetch_one("SELECT status FROM credentials WHERE credential_id=?", (result["credential_id"],))
    assert credential["status"] == "revoked"
    relationship = store.fetch_one(
        "SELECT state,revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id=?",
        (relationship_id,),
    )
    assert relationship["state"] == "revoked"
    assert relationship["revoked_at"] == now
    relationship_audit = store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_log "
        "WHERE record_json LIKE '%relationships_offboarding_revoked%' "
        "AND record_json LIKE ?",
        (f'%"harness_id":"{result["actor"].harness_id}"%',),
    )
    assert relationship_audit["count"] == 1


def test_domain_security_admin_can_revoke_without_sponsor_authority_and_replay_fails_closed(
    store,
    identity_factory,
) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    security_admin, _ = identity_factory(domain="corp.example")
    outsider, _ = identity_factory(domain="other.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    result, _use = _admit_guest(
        service,
        sponsor=sponsor,
        home_key=home_key,
        host_key=host_key,
        guest_key=guest_key,
        now=now,
    )
    guest_id = result["guest_id"]
    resource, request = service.security_guest_revocation_binding(
        guest_id=guest_id,
        reason="sponsor_unavailable",
    )
    assert service.policy_engine is not None
    service.policy_engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=security_admin.domain_id,
            principal_id=security_admin.principal_id,
            action="federation.guest.security_revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.fromtimestamp(now, UTC) + timedelta(hours=1),
        )
    )
    decision = service.policy_engine.require(
        AuthorizationRequest(
            actor=security_admin,
            action="federation.guest.security_revoke",
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            policy_revision=1,
            context=request,
        ),
        when=datetime.fromtimestamp(now, UTC),
    )
    authority = IssuanceAuthority(
        actor=security_admin,
        policy_decision_id=decision.decision_id,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE principals SET status='revoked' WHERE principal_id=?",
            (sponsor.principal_id,),
        )
        connection.execute(
            "UPDATE harnesses SET status='revoked',credential_epoch=credential_epoch+1 WHERE harness_id=?",
            (sponsor.harness_id,),
        )
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id=?",
            (sponsor.credential_id,),
        )

    with pytest.raises(AuthorizationError, match="actor binding"):
        service.security_revoke_guest(
            authority=authority.model_copy(update={"actor": outsider}),
            guest_id=guest_id,
            reason="sponsor_unavailable",
        )
    assert store.fetch_one("SELECT status FROM guests WHERE guest_id=?", (guest_id,))["status"] == "active"

    revoked = service.security_revoke_guest(
        authority=authority,
        guest_id=guest_id,
        reason="sponsor_unavailable",
    )
    assert revoked["status"] == "revoked"
    assert revoked["revocation_basis"] == "domain_security_admin"
    assert security_admin.principal_id != sponsor.principal_id
    assert store.fetch_one(
        "SELECT status FROM credentials WHERE credential_id=?",
        (result["credential_id"],),
    )["status"] == "revoked"
    audit = store.fetch_one(
        "SELECT record_json FROM audit_log WHERE record_json LIKE '%federation.guest_security_revoked%'"
    )
    assert audit is not None
    assert '"authority_scope":"deny_only_exact_guest"' in audit["record_json"]
    assert '"sponsor_independent":true' in audit["record_json"]

    with pytest.raises(ConflictError, match="stale or replayed"):
        service.security_revoke_guest(
            authority=authority,
            guest_id=guest_id,
            reason="sponsor_unavailable",
        )


def test_security_guest_revocation_binds_exact_reason_guest_domain_and_current_policy(
    store,
    identity_factory,
) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    security_admin, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    first, _ = _admit_guest(
        service,
        sponsor=sponsor,
        home_key=home_key,
        host_key=host_key,
        guest_key=P256KeyPair.generate(),
        now=now,
    )
    resource, request = service.security_guest_revocation_binding(
        guest_id=first["guest_id"],
        reason="confirmed_incident",
    )
    assert service.policy_engine is not None
    service.policy_engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id="corp.example",
            principal_id=security_admin.principal_id,
            action="federation.guest.security_revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.fromtimestamp(now, UTC) + timedelta(hours=1),
        )
    )
    decision = service.policy_engine.require(
        AuthorizationRequest(
            actor=security_admin,
            action="federation.guest.security_revoke",
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            policy_revision=1,
            context=request,
        ),
        when=datetime.fromtimestamp(now, UTC),
    )
    authority = IssuanceAuthority(actor=security_admin, policy_decision_id=decision.decision_id)
    with pytest.raises(AuthorizationError, match="request binding"):
        service.security_revoke_guest(
            authority=authority,
            guest_id=first["guest_id"],
            reason="different_reason",
        )
    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='corp.example'")
    with pytest.raises(AuthorizationError, match="policy|current"):
        service.security_revoke_guest(
            authority=authority,
            guest_id=first["guest_id"],
            reason="confirmed_incident",
        )
    assert store.fetch_one(
        "SELECT status FROM guests WHERE guest_id=?",
        (first["guest_id"],),
    )["status"] == "active"


def test_home_revocation_is_signed_monotonic_duplicate_safe_and_immediate(store, identity_factory) -> None:
    sponsor, _ = identity_factory(domain="corp.example")
    now = int(time.time())
    home_key = P256KeyPair.generate()
    host_key = P256KeyPair.generate()
    guest_key = P256KeyPair.generate()
    service = _service(store, home_key, host_key, now)
    result, use = _admit_guest(
        service,
        sponsor=sponsor,
        home_key=home_key,
        host_key=host_key,
        guest_key=guest_key,
        now=now,
    )
    relationship_id = _activate_guest_subordinate_relationship(
        store,
        sponsor=sponsor,
        guest_actor=result["actor"],
        now=now,
    )
    signal = HomeRevocationSignal(
        host_domain_id="corp.example",
        home_domain_id="partner.example",
        home_key_id=home_key.thumbprint,
        revocation_epoch=2,
        reason_code="credential_compromised",
        issued_at=now - 1,
        expires_at=now + 60,
        nonce="home-revocation-signal-nonce-0001",
    )
    attacker = P256KeyPair.generate()
    with pytest.raises(AuthenticationError):
        service.accept_home_revocation(
            signal=signal,
            home_signature=attacker.sign("agentnet.federation.revocation.v1", signal.signed_fields()),
        )
    assert store.fetch_one("SELECT status FROM guests WHERE guest_id=?", (result["guest_id"],))["status"] == "active"

    applied = service.accept_home_revocation(
        signal=signal,
        home_signature=home_key.sign("agentnet.federation.revocation.v1", signal.signed_fields()),
    )
    assert applied["status"] == "revoked"
    assert applied["revoked_guest_count"] == 1
    relationship = store.fetch_one(
        "SELECT state,revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id=?",
        (relationship_id,),
    )
    assert relationship["state"] == "revoked"
    assert relationship["revoked_at"] == now
    duplicate = service.accept_home_revocation(
        signal=signal,
        home_signature=home_key.sign("agentnet.federation.revocation.v1", signal.signed_fields()),
    )
    assert duplicate["duplicate"] is True

    conflicting = signal.model_copy(update={"nonce": "home-revocation-signal-nonce-conflict"})
    with pytest.raises(AuthenticationError, match="stale|conflicts"):
        service.accept_home_revocation(
            signal=conflicting,
            home_signature=home_key.sign("agentnet.federation.revocation.v1", conflicting.signed_fields()),
        )
    with pytest.raises(AuthorizationError):
        service.authorize_guest_operation(
            actor=result["actor"],
            asserted_host_domain_id="corp.example",
            asserted_home_domain_id="partner.example",
            grant_use=use,
            classification=Classification.C1_INTERNAL,
        )
