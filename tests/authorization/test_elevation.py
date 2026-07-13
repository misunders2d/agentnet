from __future__ import annotations

from datetime import timedelta

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import (
    AuthorizationRequest,
    ElevationRequest,
    ElevationService,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
    PolicyEngine,
    VerifiedElevationApproval,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ReplayError, ValidationError
from agentnet.protocol.models import Classification
from agentnet.operations.policy_defaults import ElevationPolicy
from agentnet.security.signatures import P256KeyPair, canonical_json


def elevation_request(actor, future, *, threshold: int = 1, risk_class: str = "ordinary") -> ElevationRequest:
    return ElevationRequest(
        domain_id=actor.domain_id,
        beneficiary_authority_id=actor.positive_authority_id,
        harness_id=actor.harness_id,
        actions=frozenset({"data.export"}),
        resources=frozenset({"dataset:alpha"}),
        input_sources=frozenset({"mailbox:event-1"}),
        output_sinks=frozenset({"external:partner"}),
        data_classes=frozenset({Classification.C2_RESTRICTED}),
        max_uses=1,
        approval_threshold=threshold,
        risk_class=risk_class,
        expires_at=future,
        reason="time-bounded incident response",
    )


def add_entitlement(engine, *, principal_id, action, resource, future):
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id="domain-a",
            principal_id=principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        )
    )


def authorize_request(engine, request, actor, now, future) -> IssuanceAuthority:
    resource, context = ElevationService.authority_binding(request)
    add_entitlement(
        engine,
        principal_id=actor.principal_id,
        action="authorization.elevation.request",
        resource=resource,
        future=future,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.elevation.request",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def approve(request, approver, signer, verifier, now, *, transaction=None, expires_in=300, nonce=None):
    return create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=ElevationService.APPROVAL_PURPOSE,
        canonical_transaction=canonical_json(transaction or request.canonical_transaction()),
        issued_at=int(now.timestamp()),
        expires_at=int(now.timestamp()) + expires_in,
        nonce=nonce,
    )


def authorize_approvers(engine, request, approvers, future):
    resource, _ = ElevationService.authority_binding(request)
    for approver in approvers:
        add_entitlement(
            engine,
            principal_id=approver.principal_id,
            action=ElevationService.APPROVAL_PURPOSE,
            resource=resource,
            future=future,
        )


def test_independent_exact_signed_approval_issues_bounded_grant(
    store, actor, now, future, approval_verifier, trusted_approvers, approval_signers
):
    request = elevation_request(actor, future)
    engine = LocalConformancePolicyEngine(store)
    authority = authorize_request(engine, request, actor, now, future)
    approver = trusted_approvers["admin-key-1"]
    authorize_approvers(engine, request, (approver,), future)
    receipt = approve(request, approver, approval_signers["admin-key-1"], approval_verifier, now)
    service = ElevationService(engine.grants, approval_verifier)

    grant = service.issue(
        request,
        beneficiary=actor,
        authority=authority,
        approvals=(receipt,),
        when=now,
    )

    assert grant.grant_id == request.request_id
    assert grant.principal_id == actor.principal_id
    assert grant.max_uses == 1
    assert service.grants.uses_for_local_conformance(grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 1


def test_caller_constructed_verified_bool_is_rejected(
    store, actor, now, future, approval_verifier
):
    request = elevation_request(actor, future)
    engine = LocalConformancePolicyEngine(store)
    authority = authorize_request(engine, request, actor, now, future)
    claim = VerifiedElevationApproval(
        approver_principal_id="admin-human",
        transaction_digest=request.transaction_digest,
        verified=True,
        verified_at=now,
        expires_at=now + timedelta(minutes=5),
    )
    service = ElevationService(engine.grants, approval_verifier)

    with pytest.raises(AuthorizationError, match="caller-asserted verified"):
        service.issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(claim,),
            when=now,
        )


def test_self_duplicate_wrong_transaction_expiry_and_replay_fail_closed(
    store, actor, now, future, approval_verifier, trusted_approvers, approval_signers
):
    request = elevation_request(actor, future, threshold=2)
    engine = LocalConformancePolicyEngine(store)
    authority = authorize_request(engine, request, actor, now, future)
    first = trusted_approvers["admin-key-1"]
    second = trusted_approvers["admin-key-2"]
    authorize_approvers(engine, request, (first, second), future)
    service = ElevationService(engine.grants, approval_verifier)

    duplicate_one = approve(request, first, approval_signers["admin-key-1"], approval_verifier, now)
    duplicate_two = approve(request, first, approval_signers["admin-key-1"], approval_verifier, now)
    with pytest.raises(AuthorizationError, match="duplicate approver"):
        service.issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(duplicate_one, duplicate_two),
            when=now,
        )

    wrong = approve(
        request,
        second,
        approval_signers["admin-key-2"],
        approval_verifier,
        now,
        transaction={"different": "transaction"},
    )
    with pytest.raises(AuthenticationError, match="transaction binding mismatch"):
        service.issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(duplicate_one, wrong),
            when=now,
        )

    expired = approve(
        request,
        second,
        approval_signers["admin-key-2"],
        approval_verifier,
        now,
        expires_in=1,
    )
    with pytest.raises(AuthenticationError, match="expired"):
        service.issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(duplicate_one, expired),
            when=now + timedelta(seconds=1),
        )

    valid_second = approve(request, second, approval_signers["admin-key-2"], approval_verifier, now)
    service.issue(
        request,
        beneficiary=actor,
        authority=authority,
        approvals=(duplicate_one, valid_second),
        when=now,
    )
    with pytest.raises(ReplayError, match="already consumed"):
        service.issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(duplicate_one, valid_second),
            when=now,
        )


def test_beneficiary_signed_receipt_cannot_self_approve(store, actor, now, future):
    request = elevation_request(actor, future)
    engine = LocalConformancePolicyEngine(store)
    authority = authorize_request(engine, request, actor, now, future)
    signer = P256KeyPair.generate()
    self_approver = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id="self-approval-key",
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({ElevationService.APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {self_approver.signer_key_id: self_approver},
        verifier_id="independent-approval.example",
    )
    authorize_approvers(engine, request, (self_approver,), future)
    receipt = approve(request, self_approver, signer, verifier, now)

    with pytest.raises(AuthorizationError, match="own elevation"):
        ElevationService(engine.grants, verifier).issue(
            request,
            beneficiary=actor,
            authority=authority,
            approvals=(receipt,),
            when=now,
        )


def test_high_impact_cannot_reduce_independent_threshold(actor, future):
    with pytest.raises(PydanticValidationError, match="at least two approvers"):
        elevation_request(actor, future, threshold=1, risk_class="high_impact")


def test_configured_elevation_policy_constrains_threshold_ttl_uses_and_break_glass(
    store, actor, now, future, approval_verifier
):
    policy = ElevationPolicy(
        ordinary_approval_threshold=2,
        high_impact_approval_threshold=3,
        ordinary_ttl_seconds=600,
        high_impact_ttl_seconds=120,
        maximum_uses=1,
        break_glass_enabled=False,
    )
    service = ElevationService(
        PolicyEngine(store).grants,
        approval_verifier,
        policy=policy,
    )
    with pytest.raises(ValidationError, match="threshold"):
        service.issue(
            elevation_request(actor, future, threshold=1),
            beneficiary=actor,
            approvals=(),
            when=now,
        )
    with pytest.raises(ValidationError, match="lifetime"):
        service.issue(
            elevation_request(actor, future, threshold=2),
            beneficiary=actor,
            approvals=(),
            when=now,
        )
    overused = elevation_request(actor, now + timedelta(minutes=5), threshold=2).model_copy(
        update={"max_uses": 2}
    )
    with pytest.raises(ValidationError, match="use budget"):
        service.issue(overused, beneficiary=actor, approvals=(), when=now)
    break_glass = elevation_request(
        actor,
        now + timedelta(minutes=1),
        threshold=3,
        risk_class="break_glass",
    )
    with pytest.raises(AuthorizationError, match="disabled"):
        service.issue(break_glass, beneficiary=actor, approvals=(), when=now)
