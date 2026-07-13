from __future__ import annotations

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
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthenticationError, AuthorizationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.context import VerifiedContextResolver
from agentnet.identity.revocation import HarnessRevocationService
from agentnet.organization import (
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipService,
)
from agentnet.protocol.models import Relationship
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json

AUDIENCE = "urn:agentnet:corp.example:corporate-api"
SCHEME = "https"
AUTHORITY = "api.corp.example"


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
    subordinate_owner_id = subordinate["principal_id"]
    approver = TrustedApprover(
        principal_id=subordinate_owner_id,
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


def _proof(identity_stack: object, key: P256KeyPair, enrolled: object, nonce: str) -> object:
    return create_request_proof(
        key,
        harness_id=enrolled.harness_id,
        credential_id=enrolled.credential_id,
        domain_id="corp.example",
        audience=AUDIENCE,
        method="GET",
        scheme=SCHEME,
        authority=AUTHORITY,
        path="/v1/inbox",
        query="",
        body=b"",
        timestamp=identity_stack.clock(),
        nonce=nonce,
    )


def _administration_stack(identity_stack):
    now = identity_stack.clock()
    signer = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="security-approver",
        domain_id="corp.example",
        signer_key_id="security-approval-key",
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({HarnessRevocationService.APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver},
        verifier_id="independent-security-approval",
    )
    with identity_stack.store.transaction() as connection:
        for principal_id in ("security-admin", "security-approver"):
            connection.execute(
                """
                INSERT INTO principals(
                    principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                ) VALUES(?,?,?,?,?,?,?)
                """,
                (
                    principal_id,
                    "corp.example",
                    "https://id.corp.example",
                    principal_id,
                    f"{principal_id}@corp.example",
                    "active",
                    now - 10,
                ),
            )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """,
            (
                "security-admin-harness",
                "corp.example",
                "security-admin",
                None,
                "codex",
                "Security admin",
                "active",
                "os_bound",
                "{}",
                1,
                now - 10,
            ),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "security-admin-credential",
                "security-admin-harness",
                "security-admin-key",
                "test",
                "active",
                1,
                now - 10,
                now + 3600,
            ),
        )
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="security-admin",
        harness_id="security-admin-harness",
        credential_id="security-admin-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    return actor, approver, signer, verifier


def _authorize_and_approve(identity_stack, service, request, actor, approver, signer, verifier):
    now = datetime.fromtimestamp(identity_stack.clock(), UTC)
    future = now + timedelta(minutes=30)
    resource, context = service.authority_binding(request)
    engine = LocalConformancePolicyEngine(identity_stack.store)
    for principal_id, action in (
        (actor.principal_id, "identity.harness.revoke"),
        (approver.principal_id, service.APPROVAL_PURPOSE),
    ):
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id="corp.example",
                principal_id=principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=future,
            )
        )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="identity.harness.revoke",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=service.APPROVAL_PURPOSE,
        canonical_transaction=canonical_json(request.canonical_transaction()),
        issued_at=identity_stack.clock(),
        expires_at=identity_stack.clock() + 300,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id), receipt


def test_individual_harness_revoke_requires_current_actor_exact_decision_and_independent_approval(
    identity_stack: object,
) -> None:
    first_key = P256KeyPair.generate()
    second_key = P256KeyPair.generate()
    third_key = P256KeyPair.generate()
    first = identity_stack.enroll(first_key, name="Codex one")
    second = identity_stack.enroll(second_key, name="Codex two")
    third = identity_stack.enroll(third_key, name="Codex three")
    assert first.principal_id == second.principal_id
    # This cascade case exercises production-eligible governance edges.  The
    # identity fixture enrolls lab harnesses as deterministic-only by default.
    with identity_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET status='active',binding_assurance='os_bound' "
            "WHERE harness_id IN (?,?,?)",
            (first.harness_id, second.harness_id, third.harness_id),
        )
    relationship_when = datetime.fromtimestamp(identity_stack.clock(), UTC)
    for relationship_id, administrator_harness_id in (
        ("target-relationship", first.harness_id),
        ("sibling-relationship", second.harness_id),
    ):
        _activate_test_relationship(
            identity_stack.store,
            relationship_id=relationship_id,
            administrator_harness_id=administrator_harness_id,
            subordinate_harness_id=third.harness_id,
            when=relationship_when,
        )
    with identity_stack.store.transaction() as connection:
        for grant_id, harness_id in (
            ("target-grant", first.harness_id),
            ("sibling-grant", second.harness_id),
        ):
            connection.execute(
                """INSERT INTO task_grants(
                       grant_id,domain_id,principal_id,harness_id,grant_json,max_uses,uses,
                       expires_at,revoked_at
                   ) VALUES(?,?,?,?,'{}',1,0,?,NULL)""",
                (
                    grant_id,
                    "corp.example",
                    first.principal_id,
                    harness_id,
                    identity_stack.clock() + 3_600,
                ),
            )

    actor, approver, signer, verifier = _administration_stack(identity_stack)
    revocations = HarnessRevocationService(identity_stack.store, verifier)
    request = revocations.prepare(
        domain_id="corp.example",
        harness_id=first.harness_id,
        reason="device lost",
    )
    authority, receipt = _authorize_and_approve(
        identity_stack,
        revocations,
        request,
        actor,
        approver,
        signer,
        verifier,
    )
    result = revocations.revoke(
        request=request,
        authority=authority,
        approval=receipt,
        now=identity_stack.clock(),
    )
    assert result.revoked_credentials == 1
    assert not result.already_revoked

    resolver = VerifiedContextResolver(
        identity_stack.store,
        service_audience=AUDIENCE,
        service_scheme=SCHEME,
        service_authority=AUTHORITY,
    )
    with pytest.raises(AuthenticationError):
        resolver.resolve(
            _proof(identity_stack, first_key, first, "revoked-harness-proof-nonce-with-entropy"),
            expected_method="GET",
            expected_scheme=SCHEME,
            expected_authority=AUTHORITY,
            expected_path="/v1/inbox",
            expected_query="",
            body=b"",
            now=identity_stack.clock(),
        )

    sibling_context = resolver.resolve(
        _proof(identity_stack, second_key, second, "active-sibling-proof-nonce-with-entropy"),
        expected_method="GET",
        expected_scheme=SCHEME,
        expected_authority=AUTHORITY,
        expected_path="/v1/inbox",
        expected_query="",
        body=b"",
        now=identity_stack.clock(),
    )
    assert sibling_context.actor.harness_id == second.harness_id
    assert identity_stack.store.fetch_one(
        "SELECT status FROM credentials WHERE credential_id=?", (second.credential_id,)
    )["status"] == "active"
    assert identity_stack.store.fetch_one(
        "SELECT revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id='target-relationship'"
    )["revoked_at"] is not None
    assert identity_stack.store.fetch_one(
        "SELECT revoked_at FROM relationship_governance_transactions "
        "WHERE relationship_id='sibling-relationship'"
    )["revoked_at"] is None
    assert identity_stack.store.fetch_one(
        "SELECT revoked_at FROM task_grants WHERE grant_id='target-grant'"
    )["revoked_at"] is not None
    assert identity_stack.store.fetch_one(
        "SELECT revoked_at FROM task_grants WHERE grant_id='sibling-grant'"
    )["revoked_at"] is None


def test_unguarded_named_harness_revoke_is_disabled(identity_stack: object) -> None:
    enrolled = identity_stack.enroll(P256KeyPair.generate())
    service = HarnessRevocationService(identity_stack.store)

    with pytest.raises(AuthorizationError, match="unguarded named-harness"):
        service.revoke(
            domain_id="corp.example",
            harness_id=enrolled.harness_id,
            reason="payload-provided target",
            now=identity_stack.clock(),
        )
    assert identity_stack.store.fetch_one(
        "SELECT status FROM harnesses WHERE harness_id=?", (enrolled.harness_id,)
    )["status"] == "deterministic_only"


def test_revocation_rechecks_actor_current_state_before_mutation(identity_stack: object) -> None:
    target = identity_stack.enroll(P256KeyPair.generate())
    actor, approver, signer, verifier = _administration_stack(identity_stack)
    service = HarnessRevocationService(identity_stack.store, verifier)
    request = service.prepare(
        domain_id="corp.example",
        harness_id=target.harness_id,
        reason="security response",
    )
    authority, receipt = _authorize_and_approve(
        identity_stack,
        service,
        request,
        actor,
        approver,
        signer,
        verifier,
    )
    with identity_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id=?",
            (actor.credential_id,),
        )

    with pytest.raises(AuthorizationError, match="credential_not_active"):
        service.revoke(
            request=request,
            authority=authority,
            approval=receipt,
            now=identity_stack.clock(),
        )
    assert identity_stack.store.fetch_one(
        "SELECT status FROM harnesses WHERE harness_id=?", (target.harness_id,)
    )["status"] == "deterministic_only"
