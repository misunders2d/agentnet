from __future__ import annotations

from concurrent.futures import CancelledError as FutureCancelledError
from datetime import UTC, datetime, timedelta

import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import AuthorizationRequest, IssuanceAuthority
from agentnet.authorization.policy import HumanEntitlement, LocalConformancePolicyEngine
from agentnet.errors import AuthenticationError, AuthorizationError, ReplayError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.identity.recovery import (
    CredentialRecoveryService,
    OIDCCredentialRecoveryCoordinator,
)
from agentnet.operations.policy_defaults import EnrollmentApprovalPolicy
from agentnet.organization import (
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipService,
)
from agentnet.protocol.models import Relationship
from agentnet.security.signatures import P256KeyPair, canonical_json


def _activate_test_relationship(
    store,
    *,
    relationship_id: str,
    administrator_harness_id: str,
    subordinate_harness_id: str,
    when: datetime,
) -> None:
    administrator = store.fetch_one(
        "SELECT * FROM harnesses WHERE harness_id=?", (administrator_harness_id,)
    )
    subordinate = store.fetch_one(
        "SELECT * FROM harnesses WHERE harness_id=?", (subordinate_harness_id,)
    )
    credential = store.fetch_one(
        "SELECT credential_id FROM credentials WHERE harness_id=? AND epoch=? "
        "AND status='active' ORDER BY credential_id LIMIT 1",
        (administrator_harness_id, administrator["credential_epoch"]),
    )
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=administrator["domain_id"],
        principal_id=administrator["principal_id"],
        harness_id=administrator_harness_id,
        credential_id=credential["credential_id"],
        credential_epoch=int(administrator["credential_epoch"]),
        binding_assurance=administrator["binding_assurance"],
    )
    relationship = Relationship(
        relationship_id=relationship_id,
        domain_id=administrator["domain_id"],
        administrator_harness_id=administrator_harness_id,
        subordinate_harness_id=subordinate_harness_id,
        may_assign=False,
        assignment_scope={},
        revision=1,
        expires_at=when + timedelta(hours=1),
    )
    proposal_expires_at = when + timedelta(minutes=10)
    resource, context = RelationshipService.proposal_binding(
        relationship,
        proposal_expires_at=proposal_expires_at,
    )
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="organization.relationship.propose",
            resource_pattern=resource,
            revision=1,
            expires_at=relationship.expires_at,
        )
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="organization.relationship.propose",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=when,
    )
    signer = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate["principal_id"],
        domain_id=actor.domain_id,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver},
        verifier_id=f"identity-cascade-{relationship_id}",
    )
    service = RelationshipService(store, approval_verifier=verifier)
    proposal = service.propose(
        relationship,
        proposal_expires_at=proposal_expires_at,
        authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        when=when,
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(
            proposal.consent_transaction.model_dump(mode="json")
        ),
        issued_at=int(when.timestamp()),
        expires_at=int((when + timedelta(minutes=5)).timestamp()),
    )
    service.accept(
        relationship_id,
        actor=actor,
        approval=receipt,
        expected_transaction_digest=proposal.transaction_digest,
        expected_relationship_revision=proposal.revision,
        expected_lifecycle_revision=proposal.lifecycle_revision,
        when=when,
    )


class FakeRecoveryProvider:
    def __init__(self, identity: VerifiedOIDCIdentity, now: int) -> None:
        self.identity = identity
        self.value = now
        self.nonce: str | None = None
        self.config = type(
            "Config",
            (),
            {
                "issuer": identity.issuer,
                "client_id": "recovery-client",
                "audience": "recovery-client",
                "redirect_uri": "https://agent.example/v1/enrollment/oidc/callback",
                "authorization_ttl_seconds": 120,
            },
        )()

    def clock(self) -> int:
        return self.value

    def authorization_url(self, *, state: str, nonce: str, code_challenge: str) -> str:
        assert state and code_challenge
        self.nonce = nonce
        return "https://idp.example/authorize?opaque=recovery"

    def exchange_and_verify(self, *, code: str, code_verifier: str, expected_nonce_hash: str):
        import hashlib

        assert code == "recovery-code-0001"
        assert len(code_verifier) >= 64
        assert self.nonce is not None
        assert hashlib.sha256(self.nonce.encode("ascii")).hexdigest() == expected_nonce_hash
        return OIDCVerificationResult(
            identity=self.identity,
            id_token_hash="a" * 64,
            expires_at=self.value + 120,
        )


def test_recovery_requires_configured_independent_threshold_and_creates_a_new_binding(
    store, identity_factory
) -> None:
    beneficiary, _old_key = identity_factory(email="owner@corp.example")
    sibling, _sibling_key = identity_factory(email="sibling@corp.example")
    first_admin, first_signer = identity_factory(email="admin1@corp.example")
    second_admin, second_signer = identity_factory(email="admin2@corp.example")
    now = datetime.now(UTC)
    for relationship_id, administrator_harness_id in (
        ("recovery-target-relationship", beneficiary.harness_id),
        ("recovery-sibling-relationship", sibling.harness_id),
    ):
        _activate_test_relationship(
            store,
            relationship_id=relationship_id,
            administrator_harness_id=administrator_harness_id,
            subordinate_harness_id=first_admin.harness_id,
            when=now,
        )
    with store.transaction() as connection:
        for grant_id, actor in (
            ("recovery-target-grant", beneficiary),
            ("recovery-sibling-grant", sibling),
        ):
            connection.execute(
                """INSERT INTO task_grants(
                       grant_id,domain_id,principal_id,harness_id,grant_json,max_uses,uses,
                       expires_at,revoked_at
                   ) VALUES(?,?,?,?,'{}',1,0,?,NULL)""",
                (
                    grant_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    int(now.timestamp()) + 3_600,
                ),
            )
    identity = VerifiedOIDCIdentity(
        issuer="https://idp.example",
        subject=store.fetch_one(
            "SELECT oidc_subject FROM principals WHERE principal_id=?",
            (beneficiary.principal_id,),
        )["oidc_subject"],
        verified_email="owner@corp.example",
    )
    purpose = CredentialRecoveryService.APPROVAL_PURPOSE
    approvers = {
        first_signer.thumbprint: TrustedApprover(
            principal_id=first_admin.principal_id,
            domain_id=first_admin.domain_id,
            signer_key_id=first_signer.thumbprint,
            public_key_pem=first_signer.public_pem,
            allowed_purposes=frozenset({purpose}),
        ),
        second_signer.thumbprint: TrustedApprover(
            principal_id=second_admin.principal_id,
            domain_id=second_admin.domain_id,
            signer_key_id=second_signer.thumbprint,
            public_key_pem=second_signer.public_pem,
            allowed_purposes=frozenset({purpose}),
        ),
    }
    verifier = IndependentApprovalVerifier(approvers, verifier_id="recovery-approval.example")
    service = CredentialRecoveryService(
        store,
        verifier,
        policy=EnrollmentApprovalPolicy(
            transaction_ttl_seconds=120,
            recovery_approver_threshold=2,
        ),
        recovered_credential_ttl_seconds=900,
    )
    new_key = P256KeyPair.generate()
    request = service.prepare(
        identity=identity,
        domain_id=beneficiary.domain_id,
        old_harness_id=beneficiary.harness_id,
        new_harness_kind="codex",
        new_harness_name="replacement device",
        new_binding_assurance="os_bound",
        new_public_key_pem=new_key.public_pem,
        when=now,
    )
    assert request.expires_at - request.issued_at == timedelta(seconds=120)
    resource = f"credential-recovery:{request.request_id}"
    engine = LocalConformancePolicyEngine(store)
    for admin in (first_admin, second_admin):
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=admin.domain_id,
                principal_id=admin.principal_id,
                action=purpose,
                resource_pattern=resource,
                revision=1,
                expires_at=now + timedelta(minutes=10),
            )
        )

    def approval(admin, signer):
        trusted = approvers[signer.thumbprint]
        return create_independent_approval_receipt(
            signer,
            approver=trusted,
            verifier_id=verifier.verifier_id,
            approval_purpose=purpose,
            canonical_transaction=canonical_json(request.signed_fields()),
            issued_at=int(now.timestamp()),
            expires_at=int(now.timestamp()) + 60,
        )

    first = approval(first_admin, first_signer)
    second = approval(second_admin, second_signer)
    possession = new_key.sign("agentnet.recovery.pop.v1", request.signed_fields())
    with pytest.raises(AuthorizationError, match="threshold"):
        service.recover(
            request,
            identity=identity,
            possession_signature=possession,
            approvals=(first,),
            when=now,
        )

    result = service.recover(
        request,
        identity=identity,
        possession_signature=possession,
        approvals=(first, second),
        when=now,
    )
    assert result.harness_id != beneficiary.harness_id
    assert store.fetch_one(
        "SELECT status FROM harnesses WHERE harness_id=?", (beneficiary.harness_id,)
    )["status"] == "revoked"
    replacement = store.fetch_one(
        "SELECT status,binding_assurance,principal_id FROM harnesses WHERE harness_id=?",
        (result.harness_id,),
    )
    assert dict(replacement) == {
        "status": "active",
        "binding_assurance": "os_bound",
        "principal_id": beneficiary.principal_id,
    }
    replacement_credential = store.fetch_one(
        "SELECT not_before,expires_at FROM credentials WHERE credential_id=?",
        (result.credential_id,),
    )
    assert replacement_credential["expires_at"] - replacement_credential["not_before"] == 900
    assert store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?", (beneficiary.domain_id,)
    )["revocation_epoch"] == 2
    assert store.fetch_one(
        "SELECT revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id='recovery-target-relationship'"
    )["revoked_at"] is not None
    assert store.fetch_one(
        "SELECT revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id='recovery-sibling-relationship'"
    )["revoked_at"] is None
    assert store.fetch_one(
        "SELECT revoked_at FROM task_grants WHERE grant_id='recovery-target-grant'"
    )["revoked_at"] is not None
    assert store.fetch_one(
        "SELECT revoked_at FROM task_grants WHERE grant_id='recovery-sibling-grant'"
    )["revoked_at"] is None
    assert dict(
        store.fetch_one(
            "SELECT status,credential_epoch FROM harnesses WHERE harness_id=?",
            (sibling.harness_id,),
        )
    ) == {"status": "active", "credential_epoch": 1}


@pytest.mark.parametrize("ttl", [299, 86_401, True])
def test_recovery_rejects_credential_ttl_outside_the_policy_profile(
    store,
    ttl,
) -> None:
    signer = P256KeyPair.generate()
    purpose = CredentialRecoveryService.APPROVAL_PURPOSE
    approver = TrustedApprover(
        principal_id="ttl-policy-approver",
        domain_id="corp.example",
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    verifier = IndependentApprovalVerifier(
        {signer.thumbprint: approver},
        verifier_id="ttl-policy-verifier",
    )
    with pytest.raises(ValueError, match="credential TTL"):
        CredentialRecoveryService(
            store,
            verifier,
            policy=EnrollmentApprovalPolicy(),
            recovered_credential_ttl_seconds=ttl,
        )


def test_oidc_recovery_coordinator_never_accepts_identity_claims_and_consumes_once(
    store,
    identity_factory,
) -> None:
    beneficiary, _old_key = identity_factory(
        email="owner@corp.example",
        binding_assurance="os_bound",
    )
    approver_actor, approver_key = identity_factory(
        email="security@corp.example",
        binding_assurance="hardware_bound",
    )
    second_approver, second_approver_key = identity_factory(
        email="security2@corp.example",
        binding_assurance="hardware_bound",
    )
    principal = store.fetch_one(
        "SELECT oidc_issuer,oidc_subject,verified_email FROM principals WHERE principal_id=?",
        (beneficiary.principal_id,),
    )
    identity = VerifiedOIDCIdentity(
        issuer=principal["oidc_issuer"],
        subject=principal["oidc_subject"],
        verified_email=principal["verified_email"],
    )
    purpose = CredentialRecoveryService.APPROVAL_PURPOSE
    trusted = TrustedApprover(
        principal_id=approver_actor.principal_id,
        domain_id=beneficiary.domain_id,
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    second_trusted = TrustedApprover(
        principal_id=second_approver.principal_id,
        domain_id=beneficiary.domain_id,
        signer_key_id=second_approver_key.thumbprint,
        public_key_pem=second_approver_key.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    verifier = IndependentApprovalVerifier(
        {
            approver_key.thumbprint: trusted,
            second_approver_key.thumbprint: second_trusted,
        },
        verifier_id="recovery-approval.example",
    )
    recovery = CredentialRecoveryService(
        store,
        verifier,
        policy=EnrollmentApprovalPolicy(
            transaction_ttl_seconds=120,
            recovery_approver_threshold=2,
        ),
    )
    now = int(datetime.now(UTC).timestamp())
    provider = FakeRecoveryProvider(identity, now)
    coordinator = OIDCCredentialRecoveryCoordinator(store, provider, recovery)
    new_key = P256KeyPair.generate()
    authorization = coordinator.begin_authorization(
        domain_id=beneficiary.domain_id,
        old_harness_id=beneficiary.harness_id,
        new_harness_kind="codex",
        new_harness_name="recovered workstation",
        new_binding_assurance="hardware_bound",
        new_public_key_pem=new_key.public_pem,
    )
    assert coordinator.has_state(authorization.state)
    verified = coordinator.complete_authorization(
        state=authorization.state,
        code="recovery-code-0001",
    )
    assert verified.request.principal_id == beneficiary.principal_id
    assert verified.request.oidc_subject == identity.subject
    encrypted = store.fetch_one(
        "SELECT recovery_request_encrypted,status FROM oidc_recovery_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )
    assert encrypted["status"] == "verified"
    assert identity.subject not in encrypted["recovery_request_encrypted"]

    resource = f"credential-recovery:{verified.request.request_id}"
    engine = LocalConformancePolicyEngine(store)
    for approved_actor in (approver_actor, second_approver):
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=approved_actor.domain_id,
                principal_id=approved_actor.principal_id,
                action=purpose,
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.fromtimestamp(now, UTC) + timedelta(minutes=10),
            )
        )
    receipt = create_independent_approval_receipt(
        approver_key,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=purpose,
        canonical_transaction=canonical_json(verified.request.signed_fields()),
        issued_at=now,
        expires_at=now + 60,
    )
    second_receipt = create_independent_approval_receipt(
        second_approver_key,
        approver=second_trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=purpose,
        canonical_transaction=canonical_json(verified.request.signed_fields()),
        issued_at=now,
        expires_at=now + 60,
    )
    result = coordinator.complete_recovery(
        transaction_id=authorization.transaction_id,
        possession_signature=new_key.sign(
            "agentnet.recovery.pop.v1",
            verified.request.signed_fields(),
        ),
        approvals=(receipt, second_receipt),
    )
    assert result.revoked_harness_id == beneficiary.harness_id
    assert store.fetch_one(
        "SELECT status FROM oidc_recovery_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "consumed"
    with pytest.raises(ReplayError):
        coordinator.complete_recovery(
            transaction_id=authorization.transaction_id,
            possession_signature=new_key.sign(
                "agentnet.recovery.pop.v1",
                verified.request.signed_fields(),
            ),
            approvals=(receipt, second_receipt),
        )


def test_oidc_recovery_identity_mismatch_fails_before_request_creation(store, identity_factory) -> None:
    beneficiary, _old_key = identity_factory(
        email="owner@corp.example",
        binding_assurance="os_bound",
    )
    approver_actor, approver_key = identity_factory(binding_assurance="hardware_bound")
    purpose = CredentialRecoveryService.APPROVAL_PURPOSE
    trusted = TrustedApprover(
        principal_id=approver_actor.principal_id,
        domain_id=beneficiary.domain_id,
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    verifier = IndependentApprovalVerifier(
        {approver_key.thumbprint: trusted},
        verifier_id="recovery-approval.example",
    )
    recovery = CredentialRecoveryService(
        store,
        verifier,
        policy=EnrollmentApprovalPolicy(recovery_approver_threshold=2),
    )
    now = int(datetime.now(UTC).timestamp())
    provider = FakeRecoveryProvider(
        VerifiedOIDCIdentity(
            issuer="https://idp.example",
            subject="attacker-subject",
            verified_email="owner@corp.example",
        ),
        now,
    )
    coordinator = OIDCCredentialRecoveryCoordinator(store, provider, recovery)
    authorization = coordinator.begin_authorization(
        domain_id=beneficiary.domain_id,
        old_harness_id=beneficiary.harness_id,
        new_harness_kind="codex",
        new_harness_name="attacker binding",
        new_binding_assurance="hardware_bound",
        new_public_key_pem=P256KeyPair.generate().public_pem,
    )
    with pytest.raises(AuthenticationError, match="does not match"):
        coordinator.complete_authorization(
            state=authorization.state,
            code="recovery-code-0001",
        )
    assert store.fetch_one(
        "SELECT status FROM oidc_recovery_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "failed"


def test_oidc_recovery_exchange_cancellation_propagates_and_consumes_state(
    store,
    identity_factory,
    monkeypatch,
) -> None:
    beneficiary, _old_key = identity_factory(
        email="owner@corp.example",
        binding_assurance="os_bound",
    )
    approver_actor, approver_key = identity_factory(binding_assurance="hardware_bound")
    purpose = CredentialRecoveryService.APPROVAL_PURPOSE
    trusted = TrustedApprover(
        principal_id=approver_actor.principal_id,
        domain_id=beneficiary.domain_id,
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    verifier = IndependentApprovalVerifier(
        {approver_key.thumbprint: trusted},
        verifier_id="recovery-approval.example",
    )
    recovery = CredentialRecoveryService(
        store,
        verifier,
        policy=EnrollmentApprovalPolicy(recovery_approver_threshold=2),
    )
    principal = store.fetch_one(
        "SELECT oidc_issuer,oidc_subject,verified_email FROM principals WHERE principal_id=?",
        (beneficiary.principal_id,),
    )
    identity = VerifiedOIDCIdentity(
        issuer=principal["oidc_issuer"],
        subject=principal["oidc_subject"],
        verified_email=principal["verified_email"],
    )
    provider = FakeRecoveryProvider(identity, int(datetime.now(UTC).timestamp()))
    coordinator = OIDCCredentialRecoveryCoordinator(store, provider, recovery)
    authorization = coordinator.begin_authorization(
        domain_id=beneficiary.domain_id,
        old_harness_id=beneficiary.harness_id,
        new_harness_kind="codex",
        new_harness_name="cancelled recovery binding",
        new_binding_assurance="hardware_bound",
        new_public_key_pem=P256KeyPair.generate().public_pem,
    )

    def cancel_exchange(**_arguments):
        raise FutureCancelledError()

    monkeypatch.setattr(provider, "exchange_and_verify", cancel_exchange)
    with pytest.raises(FutureCancelledError):
        coordinator.complete_authorization(
            state=authorization.state,
            code="recovery-code-0001",
        )

    row = store.fetch_one(
        "SELECT status FROM oidc_recovery_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )
    assert row["status"] == "failed"
    with pytest.raises(ReplayError):
        coordinator.complete_authorization(
            state=authorization.state,
            code="recovery-code-0001",
        )
