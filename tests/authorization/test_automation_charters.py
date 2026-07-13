from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.approval import (
    IndependentApprovalReceipt,
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.automation import (
    AUTOMATION_CHARTER_APPROVAL_PURPOSE,
    AutomationCharter,
    AutomationCharterService,
    AutomationInvocation,
    AutomationInvocationCompletion,
)
from agentnet.authorization import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict
from agentnet.protocol.models import Classification
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json


def _authority(
    store,
    *,
    actor,
    action: str,
    resource: str,
    request: dict[str, object],
    when: datetime,
) -> IssuanceAuthority:
    policy = LocalConformancePolicyEngine(store)
    revision = policy.current_policy_revision(actor, when=when)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=revision,
            expires_at=when + timedelta(minutes=30),
        ),
        when=when,
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=revision,
            context=request,
        ),
        when=when,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def _fixture(store, identity_factory, workload_factory, execution_grant_factory):
    owner, _owner_key = identity_factory(binding_assurance="os_bound")
    approver, _approver_harness_key = identity_factory(
        domain=owner.domain_id,
        binding_assurance="hardware_bound",
    )
    event_id = f"event-{uuid4()}"
    grant = execution_grant_factory(
        recipient=owner,
        event_id=event_id,
        actions=frozenset({"message.process"}),
        max_uses=4,
    )
    workload, _workload_key = workload_factory(
        domain=owner.domain_id,
        role="recipient_processor",
        parent_event_id=event_id,
        task_grant_id=grant.grant_id,
    )
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=approver.principal_id,
        domain_id=owner.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({AUTOMATION_CHARTER_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="automation-independent-verifier",
    )
    service = AutomationCharterService(store, approval_verifier=verifier)
    now = datetime.now(UTC)
    charter = AutomationCharter(
        domain_id=owner.domain_id,
        accountable_principal_id=owner.principal_id,
        accountable_harness_id=owner.harness_id,
        workload_registration_id=workload.workload_registration_id,
        workload_id=workload.workload_id,
        triggers=frozenset({"mailbox"}),
        actions=frozenset({"message.process"}),
        resources=frozenset({f"event:{event_id}"}),
        output_sinks=frozenset({"receipt"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_runtime_seconds=300,
        max_fanout=2,
        max_spend_micros=500,
        use_limit=2,
        approval_threshold=1,
        expires_at=now + timedelta(minutes=20),
        reason="Process this exact mailbox event unattended",
    )
    resource, request = service.authority_binding(charter)
    proposed = service.propose(
        charter,
        authority=_authority(
            store,
            actor=owner,
            action="automation.charter.propose",
            resource=resource,
            request=request,
            when=now,
        ),
        when=now,
    )
    approval_resource = f"automation-charter:{charter.charter_id}"
    approval_policy = LocalConformancePolicyEngine(store)
    approval_policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=owner.domain_id,
            principal_id=approver.principal_id,
            action=AUTOMATION_CHARTER_APPROVAL_PURPOSE,
            resource_pattern=approval_resource,
            revision=1,
            expires_at=now + timedelta(minutes=20),
        ),
        when=now,
    )
    receipt = IndependentApprovalReceipt.model_validate(
        create_independent_approval_receipt(
            approval_key,
            approver=trusted,
            verifier_id=verifier.verifier_id,
            approval_purpose=AUTOMATION_CHARTER_APPROVAL_PURPOSE,
            canonical_transaction=canonical_json(charter.canonical_transaction()),
            issued_at=int(now.timestamp()),
            expires_at=int(now.timestamp()) + 300,
        )
    )
    active = service.activate(
        actor=owner,
        charter_id=charter.charter_id,
        expected_charter_digest=charter.digest,
        expected_revision=proposed.revision,
        approvals=[receipt],
        when=now,
    )
    return service, owner, approver, workload, grant, event_id, charter, active, now


def _invocation(*, charter, active, workload, grant, event_id, invocation_id=None):
    return AutomationInvocation(
        invocation_id=invocation_id or f"automation-invocation-{uuid4()}",
        charter_id=charter.charter_id,
        workload_registration_id=workload.workload_registration_id,
        expected_charter_revision=active.revision,
        expected_charter_digest=charter.digest,
        trigger="mailbox",
        action="message.process",
        resource=f"event:{event_id}",
        output_sink="receipt",
        data_class=Classification.C1_INTERNAL,
        fanout=1,
        spend_micros=100,
        requested_runtime_seconds=60,
        parent_event_id=event_id,
        task_grant_id=grant.grant_id,
        policy_revision=active.policy_revision,
    )


def test_charter_threshold_activation_reservation_retry_and_terminal_are_exact(
    store,
    identity_factory,
    workload_factory,
    execution_grant_factory,
) -> None:
    service, _owner, _approver, workload, grant, event_id, charter, active, now = _fixture(
        store, identity_factory, workload_factory, execution_grant_factory
    )
    assert active.state == "active"
    assert active.revision == 2
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM automation_charter_approvals WHERE charter_id=?",
        (charter.charter_id,),
    )["count"] == 1

    invocation = _invocation(
        charter=charter,
        active=active,
        workload=workload,
        grant=grant,
        event_id=event_id,
    )
    reserved = service.reserve_invocation(actor=workload, invocation=invocation, when=now)
    duplicate = service.reserve_invocation(actor=workload, invocation=invocation, when=now)
    assert reserved.use_id == duplicate.use_id
    assert duplicate.duplicate is True
    assert reserved.task_grant_still_required is True
    assert reserved.data_access_authorized is False
    assert reserved.effect_authorized is False

    changed = invocation.model_copy(update={"fanout": 2})
    with pytest.raises(IdempotencyConflict):
        service.reserve_invocation(actor=workload, invocation=changed, when=now)

    completion = AutomationInvocationCompletion(
        invocation_id=invocation.invocation_id,
        charter_id=charter.charter_id,
        workload_registration_id=workload.workload_registration_id,
        expected_intent_digest=invocation.digest,
        terminal_state="committed",
        result_digest=canonical_digest({"receipt": "recorded"}),
    )
    finished = service.finish_invocation(actor=workload, completion=completion, when=now)
    replayed = service.finish_invocation(actor=workload, completion=completion, when=now)
    assert finished.state == "committed"
    assert replayed.duplicate is True
    with pytest.raises(ConflictError):
        service.finish_invocation(
            actor=workload,
            completion=completion.model_copy(
                update={"result_digest": canonical_digest({"receipt": "different"})}
            ),
            when=now,
        )


def test_charter_fails_closed_on_scope_policy_credential_and_emergency_stop(
    store,
    identity_factory,
    workload_factory,
    execution_grant_factory,
) -> None:
    service, owner, _approver, workload, grant, event_id, charter, active, now = _fixture(
        store, identity_factory, workload_factory, execution_grant_factory
    )
    invocation = _invocation(
        charter=charter,
        active=active,
        workload=workload,
        grant=grant,
        event_id=event_id,
    )
    with pytest.raises(AuthorizationError):
        service.reserve_invocation(
            actor=workload,
            invocation=invocation.model_copy(update={"output_sink": "external"}),
            when=now,
        )

    resource, request = service.mutation_binding(
        charter_id=charter.charter_id,
        expected_revision=active.revision,
        expected_charter_digest=charter.digest,
        reason="Immediate security containment",
        emergency=True,
    )
    security_admin, _key = identity_factory(
        domain=owner.domain_id,
        binding_assurance="hardware_bound",
    )
    stopped = service.stop(
        authority=_authority(
            store,
            actor=security_admin,
            action="automation.charter.emergency_stop",
            resource=resource,
            request=request,
            when=now,
        ),
        charter_id=charter.charter_id,
        expected_revision=active.revision,
        expected_charter_digest=charter.digest,
        reason="Immediate security containment",
        emergency=True,
        when=now,
    )
    assert stopped.state == "emergency_stopped"
    with pytest.raises((AuthorizationError, ConflictError)):
        service.reserve_invocation(actor=workload, invocation=invocation, when=now)


def test_charter_use_limit_and_drift_are_atomic(
    store,
    identity_factory,
    workload_factory,
    execution_grant_factory,
) -> None:
    service, _owner, _approver, workload, grant, event_id, charter, active, now = _fixture(
        store, identity_factory, workload_factory, execution_grant_factory
    )
    for index in range(2):
        invocation = _invocation(
            charter=charter,
            active=active,
            workload=workload,
            grant=grant,
            event_id=event_id,
            invocation_id=f"automation-invocation-{index}-{uuid4()}",
        )
        service.reserve_invocation(actor=workload, invocation=invocation, when=now)
    third = _invocation(
        charter=charter,
        active=active,
        workload=workload,
        grant=grant,
        event_id=event_id,
    )
    with pytest.raises(ConflictError):
        service.reserve_invocation(actor=workload, invocation=third, when=now)
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM automation_charter_uses WHERE charter_id=?",
        (charter.charter_id,),
    )["count"] == 2

    with store.transaction() as connection:
        connection.execute(
            "UPDATE workload_registrations SET credential_epoch=credential_epoch+1 WHERE registration_id=?",
            (workload.workload_registration_id,),
        )
    with pytest.raises(AuthorizationError):
        service.reserve_invocation(actor=workload, invocation=third, when=now)


def test_expiry_releases_reserved_use_and_owner_reads_only_own_charter(
    store,
    identity_factory,
    workload_factory,
    execution_grant_factory,
) -> None:
    service, owner, _approver, workload, grant, event_id, charter, active, now = _fixture(
        store, identity_factory, workload_factory, execution_grant_factory
    )
    invocation = _invocation(
        charter=charter,
        active=active,
        workload=workload,
        grant=grant,
        event_id=event_id,
    )
    service.reserve_invocation(actor=workload, invocation=invocation, when=now)
    assert service.get_for_owner(actor=owner, charter_id=charter.charter_id, when=now).state == "active"
    assert len(service.list_for_owner(actor=owner, when=now)) == 1
    stranger, _key = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    with pytest.raises(AuthorizationError):
        service.get_for_owner(actor=stranger, charter_id=charter.charter_id, when=now)

    assert service.expire_due(when=charter.expires_at + timedelta(seconds=1)) == 1
    use = store.fetch_one(
        "SELECT state,result_digest FROM automation_charter_uses WHERE invocation_id=?",
        (invocation.invocation_id,),
    )
    assert use["state"] == "released"
    assert len(use["result_digest"]) == 64
