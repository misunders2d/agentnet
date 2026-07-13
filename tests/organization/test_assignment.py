from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import timedelta

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
    OperationClass,
    PolicyEngine,
)
from agentnet.organization import (
    AssignmentRequest,
    AssignmentScope,
    AssignmentService,
    RelationshipService,
)
from agentnet.organization.relationships import RELATIONSHIP_CONSENT_PURPOSE
from agentnet.protocol.models import Classification, DeliveryFact, Relationship
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_digest,
    canonical_json,
)


@dataclass(frozen=True, slots=True)
class _RelationshipApprovalAuthority:
    signers: dict[str, P256KeyPair]
    approvers: dict[str, TrustedApprover]
    verifier: IndependentApprovalVerifier


@pytest.fixture(scope="module")
def relationship_approval_authority() -> _RelationshipApprovalAuthority:
    signers = {
        principal_id: P256KeyPair.generate()
        for principal_id in ("admin-human", "sub-human", "peer-human")
    }
    approvers = {
        principal_id: TrustedApprover(
            principal_id=principal_id,
            domain_id="domain-a",
            signer_key_id=signer.thumbprint,
            public_key_pem=signer.public_pem,
            allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        )
        for principal_id, signer in signers.items()
    }
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver for approver in approvers.values()},
        verifier_id="assignment-test-independent-approval",
    )
    return _RelationshipApprovalAuthority(
        signers=signers,
        approvers=approvers,
        verifier=verifier,
    )


def scope() -> AssignmentScope:
    return AssignmentScope(
        task_types=frozenset({"research"}),
        resources=frozenset({"catalog:alpha"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        tools=frozenset({"search"}),
        max_budget=100,
        max_duration_seconds=3600,
        max_concurrency=1,
    )


def issue_edge(
    store,
    now,
    *,
    actor,
    approval_authority: _RelationshipApprovalAuthority,
    administrator: str = "admin-harness",
    subordinate: str = "sub-harness",
    revision: int = 1,
    expires_in: timedelta = timedelta(hours=4),
    may_assign: bool = True,
    activate: bool = True,
):
    relationship = Relationship(
        domain_id="domain-a",
        administrator_harness_id=administrator,
        subordinate_harness_id=subordinate,
        may_assign=may_assign,
        assignment_scope=scope().model_dump(mode="json") if may_assign else {},
        revision=revision,
        expires_at=now + expires_in,
    )
    proposal_expires_at = min(
        now + timedelta(minutes=10),
        relationship.expires_at - timedelta(seconds=1),
    )
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
        when=now,
    )
    subordinate_row = store.fetch_one(
        "SELECT principal_id,guest_id FROM harnesses WHERE harness_id=?",
        (subordinate,),
    )
    owner_id = subordinate_row["principal_id"] or subordinate_row["guest_id"]
    approval_signer = approval_authority.signers[owner_id]
    approver = approval_authority.approvers[owner_id]
    service = RelationshipService(
        store,
        approval_verifier=approval_authority.verifier,
    )
    proposal = service.propose(
        relationship,
        authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        proposal_expires_at=proposal_expires_at,
        when=now,
    )
    if not activate:
        return proposal
    approval = create_independent_approval_receipt(
        approval_signer,
        approver=approver,
        verifier_id=approval_authority.verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(
            proposal.consent_transaction.model_dump(mode="json")
        ),
        issued_at=int(now.timestamp()),
        expires_at=int((now + timedelta(minutes=5)).timestamp()),
    )
    return service.accept(
        relationship.relationship_id,
        actor=actor,
        approval=approval,
        expected_transaction_digest=proposal.transaction_digest,
        expected_relationship_revision=proposal.revision,
        expected_lifecycle_revision=proposal.lifecycle_revision,
        when=now,
    )


def assignment_service(
    store,
    approval_authority: _RelationshipApprovalAuthority,
) -> AssignmentService:
    return AssignmentService(
        store,
        approval_verifier=approval_authority.verifier,
    )


def assignment(actor, recipient: str = "sub-harness", **updates) -> AssignmentRequest:
    values = {
        "actor": actor,
        "recipient_harness_id": recipient,
        "task_type": "research",
        "resources": frozenset({"catalog:alpha"}),
        "data_classes": frozenset({Classification.C1_INTERNAL}),
        "tools": frozenset({"search"}),
        "budget": 50,
        "concurrency": 1,
        "policy_revision": 1,
    }
    values.update(updates)
    return AssignmentRequest(**values)


def test_active_downward_assignment_auto_queues_custody_only(
    store, admin_actor, now, relationship_approval_authority
):
    edge = issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    service = assignment_service(store, relationship_approval_authority)

    decision = service.decide(assignment(admin_actor), when=now)

    assert decision.fact is DeliveryFact.ACCEPTED_QUEUED
    assert decision.relationship_id == edge.relationship_id
    assert decision.data_access_authorized is False
    assert decision.effect_authorized is False
    persisted = service.recorder.get(decision.policy_decision_id)
    assert persisted.allowed is True
    assert persisted.context["delivery_fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert persisted.context["data_access_authorized"] is False
    assert persisted.context["effect_authorized"] is False


def test_administrator_proposal_alone_has_zero_assignment_authority(
    store, admin_actor, now, relationship_approval_authority
):
    proposal = issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
        activate=False,
    )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )

    assert proposal.lifecycle_state == "proposed"
    assert proposal.activated_at is None
    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "no_active_directed_assignment_relationship"
    assert decision.relationship_id is None


def test_fabricated_proposed_row_activation_never_auto_queues(
    store, admin_actor, now, relationship_approval_authority
):
    proposal = issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
        activate=False,
    )
    with store.transaction() as connection:
        connection.execute(
            """
            UPDATE relationship_governance_transactions
               SET state='active',lifecycle_revision=lifecycle_revision+1,
                   activation_basis='subordinate_owner_consent',
                   approval_receipt_id='fabricated-receipt-id',
                   approval_receipt_digest=?,approval_receipt_json='{}',
                   approval_approver_authority_kind='human',
                   approval_approver_authority_id='sub-human',
                   approval_verifier_id=?,approval_signer_key_id=?,
                   approval_expires_at=?,activated_at=?,updated_at=?
             WHERE relationship_id=?
            """,
            (
                canonical_digest({}),
                relationship_approval_authority.verifier.verifier_id,
                relationship_approval_authority.approvers["sub-human"].signer_key_id,
                int((now + timedelta(minutes=5)).timestamp()),
                int(now.timestamp()),
                int(now.timestamp()),
                proposal.relationship_id,
            ),
        )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "missing_relationship_acceptance"
    assert decision.data_access_authorized is False
    assert decision.effect_authorized is False


def test_signed_consent_remains_valid_after_receipt_expiry_while_edge_is_active(
    store, admin_actor, now, relationship_approval_authority
):
    edge = issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    after_receipt_expiry = now + timedelta(minutes=6)
    persisted = store.fetch_one(
        "SELECT state,approval_expires_at,relationship_expires_at "
        "FROM relationship_governance_transactions WHERE relationship_id=?",
        (edge.relationship_id,),
    )
    assert persisted["approval_expires_at"] < int(after_receipt_expiry.timestamp())
    assert int(after_receipt_expiry.timestamp()) < persisted["relationship_expires_at"]

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor),
        when=after_receipt_expiry,
    )

    assert persisted["state"] == "active"
    assert decision.fact is DeliveryFact.ACCEPTED_QUEUED
    assert decision.relationship_id == edge.relationship_id


def test_active_owner_consent_without_configured_verifier_fails_closed(
    store, admin_actor, now, relationship_approval_authority
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )

    decision = AssignmentService(store).decide(assignment(admin_actor), when=now)

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "missing_relationship_acceptance"
    assert decision.data_access_authorized is False
    assert decision.effect_authorized is False


@pytest.mark.parametrize(
    "mutation",
    [
        "receipt_json",
        "receipt_digest",
        "receipt_signature",
        "receipt_id",
        "verifier_id",
        "signer_key_id",
        "receipt_expiry",
        "replay_fence",
    ],
)
def test_mutated_or_unfenced_persisted_consent_denies_automatic_custody(
    store,
    admin_actor,
    now,
    relationship_approval_authority,
    mutation,
):
    edge = issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    row = store.fetch_one(
        "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
        (edge.relationship_id,),
    )
    with store.transaction() as connection:
        if mutation == "receipt_json":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_receipt_json=? WHERE relationship_id=?",
                (f" {row['approval_receipt_json']}", edge.relationship_id),
            )
        elif mutation == "receipt_digest":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_receipt_digest=? WHERE relationship_id=?",
                ("0" * 64, edge.relationship_id),
            )
        elif mutation == "receipt_signature":
            receipt = json.loads(row["approval_receipt_json"])
            receipt["signature"] = "tampered-signature"
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_receipt_json=?,approval_receipt_digest=? "
                "WHERE relationship_id=?",
                (
                    canonical_json(receipt).decode("utf-8"),
                    canonical_digest(receipt),
                    edge.relationship_id,
                ),
            )
        elif mutation == "receipt_id":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_receipt_id='tampered-receipt-id' WHERE relationship_id=?",
                (edge.relationship_id,),
            )
        elif mutation == "verifier_id":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_verifier_id='tampered-verifier' WHERE relationship_id=?",
                (edge.relationship_id,),
            )
        elif mutation == "signer_key_id":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_signer_key_id='tampered-signer' WHERE relationship_id=?",
                (edge.relationship_id,),
            )
        elif mutation == "receipt_expiry":
            connection.execute(
                "UPDATE relationship_governance_transactions "
                "SET approval_expires_at=approval_expires_at+1 WHERE relationship_id=?",
                (edge.relationship_id,),
            )
        else:
            connection.execute(
                "DELETE FROM replay_nonces WHERE actor_id=?",
                (f"approval:{row['approval_verifier_id']}",),
            )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "missing_relationship_acceptance"
    assert decision.data_access_authorized is False
    assert decision.effect_authorized is False


@pytest.mark.parametrize("direction", ["reverse", "lateral"])
def test_reverse_and_lateral_assignment_remain_pending_human(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    now,
    direction,
    relationship_approval_authority,
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    service = assignment_service(store, relationship_approval_authority)
    request = (
        assignment(subordinate_actor, recipient="admin-harness")
        if direction == "reverse"
        else assignment(peer_actor, recipient="sub-harness")
    )

    decision = service.decide(request, when=now)

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "no_active_directed_assignment_relationship"
    assert service.recorder.get(decision.policy_decision_id).allowed is False


def test_separate_exact_reverse_edge_can_authorize_that_direction(
    store, subordinate_actor, now, relationship_approval_authority
):
    # The reverse edge is separately issued by the authenticated subordinate
    # endpoint acting as administrator for this exact direction.
    reverse = issue_edge(
        store,
        now,
        actor=subordinate_actor,
        approval_authority=relationship_approval_authority,
        administrator="sub-harness",
        subordinate="admin-harness",
    )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(subordinate_actor, recipient="admin-harness"),
        when=now,
    )

    assert decision.fact is DeliveryFact.ACCEPTED_QUEUED
    assert decision.relationship_id == reverse.relationship_id


def test_expired_revoked_stale_and_out_of_scope_edges_fail_closed(
    store, admin_actor, now, relationship_approval_authority
):
    service = assignment_service(store, relationship_approval_authority)
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
        expires_in=timedelta(minutes=30),
    )

    expired = service.decide(assignment(admin_actor), when=now + timedelta(hours=1))
    assert expired.fact is DeliveryFact.PENDING_HUMAN
    assert expired.reason == "relationship_expired"

    # A new current revision fences the old edge.
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
        revision=2,
    )
    stale = service.decide(assignment(admin_actor, expected_relationship_revision=1), when=now)
    assert stale.fact is DeliveryFact.PENDING_HUMAN
    assert stale.reason == "stale_relationship_revision"

    out_of_scope = service.decide(
        assignment(admin_actor, resources=frozenset({"catalog:secret"})),
        when=now,
    )
    assert out_of_scope.fact is DeliveryFact.PENDING_HUMAN
    assert out_of_scope.reason == "assignment_resource_out_of_scope"

    # The local fixture simulates an already-authorized offboarding cascade;
    # public revocation itself is covered by signed-command lifecycle tests.
    with store.transaction() as connection:
        RelationshipService(store)._cascade_revoke_for_harness_in_transaction(
            connection,
            harness_id="sub-harness",
            when=now,
            reason="focused assignment revocation fixture",
        )
    revoked = service.decide(assignment(admin_actor), when=now)
    assert revoked.fact is DeliveryFact.PENDING_HUMAN
    assert revoked.reason == "no_active_directed_assignment_relationship"


@pytest.mark.parametrize(
    ("updates", "reason"),
    [
        ({"task_type": "effect"}, "assignment_task_type_out_of_scope"),
        ({"resources": frozenset({"catalog:secret"})}, "assignment_resource_out_of_scope"),
        ({"data_classes": frozenset({Classification.C2_RESTRICTED})}, "assignment_data_class_out_of_scope"),
        ({"tools": frozenset({"shell"})}, "assignment_tool_out_of_scope"),
        ({"budget": 101}, "assignment_budget_out_of_scope"),
        ({"concurrency": 2}, "assignment_concurrency_out_of_scope"),
    ],
)
def test_every_assignment_scope_dimension_is_denying(
    store,
    admin_actor,
    now,
    updates,
    reason,
    relationship_approval_authority,
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor, **updates), when=now
    )

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == reason


def test_assignment_deadline_is_bounded_by_scope_and_relationship(
    store, admin_actor, now, relationship_approval_authority
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    service = assignment_service(store, relationship_approval_authority)

    beyond_scope = service.decide(
        assignment(admin_actor, deadline=now + timedelta(hours=2)),
        when=now,
    )
    at_relationship_end = service.decide(
        assignment(admin_actor, deadline=now + timedelta(hours=4)),
        when=now,
    )

    assert beyond_scope.fact is DeliveryFact.PENDING_HUMAN
    assert beyond_scope.reason == "assignment_deadline_out_of_scope"
    # The four-hour relationship is also outside the one-hour assignment scope.
    assert at_relationship_end.fact is DeliveryFact.PENDING_HUMAN
    assert at_relationship_end.reason == "assignment_deadline_out_of_scope"


def test_active_edge_without_may_assign_remains_pending(
    store, admin_actor, now, relationship_approval_authority
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
        expires_in=timedelta(hours=1),
        may_assign=False,
    )

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "relationship_does_not_allow_assignment"


def test_relationship_acceptance_never_authorizes_protected_read_or_effect(
    store, admin_actor, now, relationship_approval_authority
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    custody = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )
    assert custody.fact is DeliveryFact.ACCEPTED_QUEUED

    protected = PolicyEngine(store).decide(
        AuthorizationRequest(
            actor=admin_actor,
            action="data.read",
            resource="catalog:alpha",
            operation_class=OperationClass.PROTECTED_READ,
            policy_revision=1,
        ),
        when=now,
    )
    assert protected.allowed is False
    assert protected.reason == "no_positive_human_entitlement"


def test_revoked_sender_state_yields_pending_and_persists(
    store, admin_actor, now, relationship_approval_authority
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id='admin-harness'")

    service = assignment_service(store, relationship_approval_authority)
    decision = service.decide(assignment(admin_actor), when=now)

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "harness_not_active"
    assert service.recorder.get(decision.policy_decision_id).allowed is False


@pytest.mark.parametrize(
    "mutation",
    [
        "UPDATE principals SET status='revoked' WHERE principal_id='sub-human'",
        "UPDATE harnesses SET principal_id='peer-human' WHERE harness_id='sub-harness'",
        "UPDATE harnesses SET credential_epoch=credential_epoch+1 "
        "WHERE harness_id='sub-harness'",
    ],
)
def test_current_subordinate_owner_and_credential_drift_deny_automatic_custody(
    store,
    admin_actor,
    now,
    mutation,
    relationship_approval_authority,
):
    issue_edge(
        store,
        now,
        actor=admin_actor,
        approval_authority=relationship_approval_authority,
    )
    with store.transaction() as connection:
        connection.execute(mutation)

    decision = assignment_service(store, relationship_approval_authority).decide(
        assignment(admin_actor), when=now
    )

    assert decision.fact is DeliveryFact.PENDING_HUMAN
    assert decision.reason == "stale_relationship_authority_binding"
    assert decision.data_access_authorized is False
    assert decision.effect_authorized is False
