from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.approval.service import independent_approval_replay_binding
from agentnet.authorization import (
    AuthorizationRequest,
    GrantUse,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ValidationError,
)
from agentnet.organization.relationships import (
    RELATIONSHIP_CONSENT_PURPOSE,
    AssignmentScope,
    RelationshipGovernanceRecord,
    RelationshipPolicyException,
    RelationshipService,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification, Relationship, TaskGrant
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json


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


def relationship(
    now: datetime,
    *,
    revision: int = 1,
    relationship_id: str = "relationship-v1",
    administrator_harness_id: str = "admin-harness",
    subordinate_harness_id: str = "sub-harness",
    expires_in: timedelta = timedelta(hours=4),
) -> Relationship:
    return Relationship(
        relationship_id=relationship_id,
        domain_id="domain-a",
        administrator_harness_id=administrator_harness_id,
        subordinate_harness_id=subordinate_harness_id,
        may_assign=True,
        assignment_scope=scope().model_dump(mode="json"),
        revision=revision,
        expires_at=now + expires_in,
    )


def proposal_authority(
    store,
    edge: Relationship,
    actor,
    now: datetime,
    *,
    proposal_expires_at: datetime,
) -> IssuanceAuthority:
    engine = LocalConformancePolicyEngine(store)
    resource, context = RelationshipService.proposal_binding(
        edge,
        proposal_expires_at=proposal_expires_at,
    )
    action = "organization.relationship.propose"
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=edge.expires_at,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def propose(
    service: RelationshipService,
    store,
    edge: Relationship,
    actor,
    now: datetime,
    *,
    proposal_expires_at: datetime | None = None,
) -> RelationshipGovernanceRecord:
    proposal_expires_at = proposal_expires_at or now + timedelta(minutes=30)
    return service.propose(
        edge,
        proposal_expires_at=proposal_expires_at,
        authority=proposal_authority(
            store,
            edge,
            actor,
            now,
            proposal_expires_at=proposal_expires_at,
        ),
        when=now,
    )


def approval_receipt(
    proposal: RelationshipGovernanceRecord,
    *,
    principal_id: str,
    signer: P256KeyPair,
    verifier: IndependentApprovalVerifier,
    now: datetime,
    canonical_transaction: bytes | None = None,
    purpose: str = RELATIONSHIP_CONSENT_PURPOSE,
    domain_id: str = "domain-a",
    issued_at: int | None = None,
    expires_at: int | None = None,
    receipt_id: str | None = None,
) -> dict[str, object]:
    issued = int(now.timestamp()) if issued_at is None else issued_at
    trusted = TrustedApprover(
        principal_id=principal_id,
        domain_id=domain_id,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        authority_kind="guest" if principal_id == "guest-owner" else "human",
    )
    return create_independent_approval_receipt(
        signer,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=purpose,
        canonical_transaction=(
            canonical_transaction
            or canonical_json(proposal.consent_transaction.model_dump(mode="json"))
        ),
        issued_at=issued,
        expires_at=issued + 300 if expires_at is None else expires_at,
        receipt_id=receipt_id,
    )


def accept(
    service: RelationshipService,
    proposal: RelationshipGovernanceRecord,
    actor,
    receipt: dict[str, object],
    now: datetime,
) -> RelationshipGovernanceRecord:
    return service.accept(
        proposal.relationship_id,
        actor=actor,
        approval=receipt,
        expected_transaction_digest=proposal.transaction_digest,
        expected_relationship_revision=proposal.revision,
        expected_lifecycle_revision=proposal.lifecycle_revision,
        when=now,
    )


def read_authority(
    store,
    edge: Relationship,
    actor,
    now: datetime,
    *,
    entitlement_expires_at: datetime | None = None,
) -> IssuanceAuthority:
    engine = LocalConformancePolicyEngine(store)
    resource, context = RelationshipService.read_binding(edge.relationship_id)
    action = "organization.relationship.read"
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=entitlement_expires_at or edge.expires_at,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def revocation_evidence(
    store,
    service: RelationshipService,
    edge: RelationshipGovernanceRecord,
    actor,
    key: P256KeyPair,
    signed_command,
    now: datetime,
    *,
    action: str = "organization.relationship.revoke",
    reason: str = "end exact relationship",
):
    resource, request = service.revocation_binding(
        edge.relationship_id,
        expected_relationship_revision=edge.revision,
        expected_lifecycle_revision=edge.lifecycle_revision,
        reason=reason,
    )
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=edge.expires_at,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(request)},
        ),
        when=now,
    )
    command = signed_command(
        key=key,
        actor=actor,
        action=action,
        resource=resource,
        request=request,
        now=now,
        entity_revision=edge.lifecycle_revision,
        reason=reason,
    )
    return command, IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def record_policy_exception(
    service: RelationshipService,
    store,
    pending: RelationshipGovernanceRecord,
    *,
    signer_actor,
    signer_key: P256KeyPair,
    signed_command,
    now: datetime,
    policy_exception_id: str,
):
    exception = RelationshipPolicyException(
        policy_exception_id=policy_exception_id,
        domain_id=pending.domain_id,
        relationship_id=pending.relationship_id,
        relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        relationship_transaction_digest=pending.transaction_digest,
        reason="recorded exact domain governance policy exception",
        expires_at=now + timedelta(minutes=10),
    )
    resource, request = service.policy_exception_binding(exception)
    action = "organization.relationship.policy_exception.record"
    engine = LocalConformancePolicyEngine(store)
    grant_use = None
    if signer_actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=signer_actor.domain_id,
                principal_id=signer_actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=pending.expires_at,
            ),
            when=now,
        )
    elif signer_actor.kind is ActorKind.HOST_GUEST_HARNESS:
        grant = TaskGrant(
            grant_id=f"{policy_exception_id}-guest-authority",
            domain_id=signer_actor.domain_id,
            principal_id=signer_actor.guest_id,
            harness_id=signer_actor.harness_id,
            actions=frozenset({action}),
            resources=frozenset({resource}),
            input_sources=frozenset({f"relationship:{pending.relationship_id}"}),
            output_sinks=frozenset({"governance:policy-exception-record"}),
            data_classes=frozenset({Classification.C1_INTERNAL}),
            max_uses=1,
            expires_at=now + timedelta(minutes=20),
        )
        with store.transaction() as connection:
            grant = engine.grants._insert_in_transaction(
                connection,
                grant=grant,
                when=now,
                issuance_evidence={"kind": "synthetic_test_host_guest_grant"},
            )
        grant_use = GrantUse(
            grant_id=grant.grant_id,
            action=action,
            resource=resource,
            input_source=f"relationship:{pending.relationship_id}",
            output_sink="governance:policy-exception-record",
            data_class=Classification.C1_INTERNAL,
        )
    else:  # pragma: no cover - helper guard
        raise AssertionError("policy exception test signer lacks positive authority")
    decision = engine.require(
        AuthorizationRequest(
            actor=signer_actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(request)},
            grant_use=grant_use,
        ),
        when=now,
    )
    command = signed_command(
        key=signer_key,
        actor=signer_actor,
        action=action,
        resource=resource,
        request=request,
        now=now,
        entity_revision=pending.lifecycle_revision,
        reason=exception.reason,
    )
    recorded = service.record_policy_exception(
        exception,
        command=command,
        authority=IssuanceAuthority(
            actor=signer_actor,
            policy_decision_id=decision.decision_id,
        ),
        when=now,
    )
    return exception, recorded


def make_guest_policy_signer(store, signer_key: P256KeyPair, now: datetime) -> VerifiedActor:
    """Turn the otherwise-unused peer endpoint into an exact host-local guest."""

    epoch = int(now.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,
                sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,?,?)
            """,
            (
                "policy-signer-guest",
                "domain-a",
                "partner.example",
                "policy-signer-pairwise",
                "admin-human",
                "active",
                epoch + 3600,
            ),
        )
        connection.execute(
            """
            UPDATE harnesses SET principal_id=NULL,guest_id='policy-signer-guest'
             WHERE harness_id='peer-harness'
            """
        )
        credential = connection.execute(
            "SELECT key_id FROM credentials WHERE credential_id='peer-credential'"
        ).fetchone()
        assert credential["key_id"] == signer_key.thumbprint
    return VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id="domain-a",
        guest_id="policy-signer-guest",
        harness_id="peer-harness",
        credential_id="peer-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )


def test_proposal_has_zero_authority_until_exact_owner_consent(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)

    assert pending.lifecycle_state == "proposed"
    assert pending.activation_basis is None
    assert isinstance(pending.assignment_scope, AssignmentScope)
    assert pending.assignment_scope.authority_effect == "custody_only"
    assert store.fetch_one(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='relationships'"
    ) is None
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now,
        ) == "missing_relationship_acceptance"

    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    active = accept(service, pending, subordinate_actor, receipt, now)

    assert active.lifecycle_state == "active"
    assert active.activation_basis == "subordinate_owner_consent"
    assert active.approval_approver_authority_id == "sub-human"
    assert active.lifecycle_revision == 2
    assert active.active_at(now) is True
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            approval_verifier=relationship_approval_verifier,
            when=now,
        ) is None
    assert store.verify_audit_chain()[0] is True


def test_proposal_retry_rejects_a_committed_lineage_revocation(
    store,
    admin_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    edge = relationship(now)
    proposal_expires_at = now + timedelta(minutes=30)
    pending = propose(
        service,
        store,
        edge,
        admin_actor,
        now,
        proposal_expires_at=proposal_expires_at,
    )

    command, authority = revocation_evidence(
        store,
        service,
        pending,
        admin_actor,
        identity_keys["admin-credential"],
        signed_command,
        now,
    )
    assert service.revoke(
        pending.relationship_id,
        command=command,
        authority=authority,
        when=now,
    ) is True
    with pytest.raises(ConflictError, match="different proposal bytes"):
        propose(
            service,
            store,
            edge,
            admin_actor,
            now,
            proposal_expires_at=proposal_expires_at,
        )


def test_prompt_text_or_caller_assertions_can_never_accept(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    with pytest.raises(ValidationError, match="exact schema"):
        accept(
            service,
            pending,
            subordinate_actor,
            {"prompt": "the agent says its owner approved", "verified": True},
            now,
        )
    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"


def test_exact_proposal_retry_is_idempotent_only_while_snapshot_is_current(
    store,
    admin_actor,
    now,
):
    service = RelationshipService(store)
    edge = relationship(now)
    proposal_expires_at = now + timedelta(minutes=30, microseconds=123456)
    first = service.propose(
        edge,
        proposal_expires_at=proposal_expires_at,
        authority=proposal_authority(
            store,
            edge,
            admin_actor,
            now,
            proposal_expires_at=proposal_expires_at,
        ),
        when=now,
    )
    retried = service.propose(
        edge,
        proposal_expires_at=proposal_expires_at,
        authority=proposal_authority(
            store,
            edge,
            admin_actor,
            now,
            proposal_expires_at=proposal_expires_at,
        ),
        when=now,
    )
    assert retried.transaction_id == first.transaction_id
    assert retried.transaction_digest == first.transaction_digest


@pytest.mark.parametrize("changed_expiry", ["relationship", "proposal"])
def test_same_id_fractional_expiry_substitution_is_not_idempotent(
    store,
    admin_actor,
    now,
    changed_expiry,
):
    service = RelationshipService(store)
    edge = relationship(now)
    proposal_expires_at = now + timedelta(minutes=30, microseconds=123456)
    service.propose(
        edge,
        proposal_expires_at=proposal_expires_at,
        authority=proposal_authority(
            store,
            edge,
            admin_actor,
            now,
            proposal_expires_at=proposal_expires_at,
        ),
        when=now,
    )
    retry_edge = edge
    retry_proposal_expiry = proposal_expires_at
    if changed_expiry == "relationship":
        retry_edge = edge.model_copy(
            update={"expires_at": edge.expires_at + timedelta(microseconds=1)}
        )
    else:
        retry_proposal_expiry += timedelta(microseconds=1)
    with pytest.raises(ConflictError, match="different proposal bytes"):
        service.propose(
            retry_edge,
            proposal_expires_at=retry_proposal_expiry,
            authority=proposal_authority(
                store,
                retry_edge,
                admin_actor,
                now,
                proposal_expires_at=retry_proposal_expiry,
            ),
            when=now,
        )


def test_exact_proposal_retry_rejects_stale_subordinate_credential_snapshot(
    store,
    admin_actor,
    now,
):
    service = RelationshipService(store)
    edge = relationship(now)
    proposal_expires_at = now + timedelta(minutes=30)
    service.propose(
        edge,
        proposal_expires_at=proposal_expires_at,
        authority=proposal_authority(
            store,
            edge,
            admin_actor,
            now,
            proposal_expires_at=proposal_expires_at,
        ),
        when=now,
    )
    replacement = P256KeyPair.generate()
    epoch = int(now.timestamp())
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET credential_epoch=2 WHERE harness_id='sub-harness'"
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "sub-credential-idempotency-2",
                "sub-harness",
                replacement.thumbprint,
                replacement.public_pem,
                "active",
                2,
                epoch - 1,
                epoch + 3600,
            ),
        )
    with pytest.raises(ConflictError, match="different proposal bytes"):
        service.propose(
            edge,
            proposal_expires_at=proposal_expires_at,
            authority=proposal_authority(
                store,
                edge,
                admin_actor,
                now,
                proposal_expires_at=proposal_expires_at,
            ),
            when=now,
        )


def test_consent_rejects_wrong_owner_transaction_purpose_and_expiry(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)

    wrong_owner = approval_receipt(
        pending,
        principal_id="peer-human",
        signer=relationship_approval_keys["peer-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    with pytest.raises(AuthorizationError, match="current owner"):
        accept(service, pending, subordinate_actor, wrong_owner, now)

    wrong_transaction = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        canonical_transaction=canonical_json({"different": "relationship"}),
    )
    with pytest.raises(AuthenticationError, match="transaction binding"):
        accept(service, pending, subordinate_actor, wrong_transaction, now)

    wrong_purpose = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        purpose="organization.relationship.prompt_accept",
    )
    with pytest.raises(AuthenticationError, match="purpose or domain"):
        accept(service, pending, subordinate_actor, wrong_purpose, now)

    wrong_domain = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        domain_id="different-domain",
    )
    with pytest.raises(AuthenticationError, match="purpose or domain"):
        accept(service, pending, subordinate_actor, wrong_domain, now)

    expired = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        issued_at=int(now.timestamp()) - 301,
        expires_at=int(now.timestamp()) - 1,
    )
    with pytest.raises(AuthenticationError, match="expired or overlong"):
        accept(service, pending, subordinate_actor, expired, now)
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 0


def test_exact_receipt_is_replay_safe(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        receipt_id="receipt-exact-replay-0001",
    )
    active = accept(service, pending, subordinate_actor, receipt, now)
    with pytest.raises(ConflictError, match="stale proposal state"):
        accept(service, pending, subordinate_actor, receipt, now)
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 1
    persisted = store.fetch_one(
        "SELECT lifecycle_revision,state FROM relationship_governance_transactions WHERE relationship_id=?",
        (active.relationship_id,),
    )
    assert persisted["lifecycle_revision"] == 2
    assert persisted["state"] == "active"


def test_owner_activation_failure_after_pending_intent_rolls_back_receipt_and_edge(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
    monkeypatch,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        receipt_id="rollback-owner-consent-receipt-0001",
    )

    def fail_after_intent_and_receipt(connection, **_kwargs):
        assert connection.execute(
            "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()["state"] == "proposed"
        assert connection.execute(
            "SELECT COUNT(*) AS count FROM replay_nonces"
        ).fetchone()["count"] == 1
        intent = connection.execute(
            "SELECT state,completed_at FROM audit_intents "
            "WHERE action='organization.relationship.activate'"
        ).fetchone()
        assert intent["state"] == "pending"
        assert intent["completed_at"] is None
        raise RuntimeError("injected failure after activation intent and receipt consumption")

    with monkeypatch.context() as patch:
        patch.setattr(service, "_activate", fail_after_intent_and_receipt)
        with pytest.raises(RuntimeError, match="receipt consumption"):
            accept(service, pending, subordinate_actor, receipt, now)

    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_intents "
        "WHERE action='organization.relationship.activate'"
    )["count"] == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_log "
        "WHERE record_json LIKE '%relationship_activated%'"
    )["count"] == 0

    assert accept(service, pending, subordinate_actor, receipt, now).lifecycle_state == "active"


def test_policy_exception_failure_after_edge_cas_rolls_back_exception_and_intent(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    monkeypatch,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="rollback-policy-exception-after-cas",
    )

    def fail_after_relationship_cas(connection, intent):
        assert connection.execute(
            "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()["state"] == "active"
        assert connection.execute(
            "SELECT consumed_at FROM relationship_policy_exceptions "
            "WHERE policy_exception_id=?",
            (recorded.policy_exception_id,),
        ).fetchone()["consumed_at"] is not None
        persisted_intent = connection.execute(
            "SELECT state,completed_at FROM audit_intents WHERE intent_id=?",
            (intent.intent_id,),
        ).fetchone()
        assert persisted_intent["state"] == "pending"
        assert persisted_intent["completed_at"] is None
        raise RuntimeError("injected failure after relationship activation CAS")

    with monkeypatch.context() as patch:
        patch.setattr(service, "_complete_activation_intent", fail_after_relationship_cas)
        with pytest.raises(RuntimeError, match="activation CAS"):
            service.activate_with_policy_exception(
                pending.relationship_id,
                policy_exception_id=recorded.policy_exception_id,
                actor=subordinate_actor,
                expected_transaction_digest=pending.transaction_digest,
                expected_relationship_revision=pending.revision,
                expected_lifecycle_revision=pending.lifecycle_revision,
                when=now,
            )

    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"
    assert store.fetch_one(
        "SELECT consumed_at FROM relationship_policy_exceptions WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )["consumed_at"] is None
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_intents "
        "WHERE action='organization.relationship.activate'"
    )["count"] == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_log "
        "WHERE record_json LIKE '%relationship_activated%'"
    )["count"] == 0

    assert service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=subordinate_actor,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=now,
    ).lifecycle_state == "active"


def test_expired_unused_receipt_cannot_be_backdated_into_active_authority(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
        receipt_id="unused-expired-receipt-0001",
    )
    verified = relationship_approval_verifier.verify(
        canonical_transaction=canonical_json(
            pending.consent_transaction.model_dump(mode="json")
        ),
        approval=receipt,
        expected_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        expected_domain_id=pending.domain_id,
        when=now,
    )
    retain_until = max(int(pending.expires_at.timestamp()), verified.expires_at)
    actor_id, nonce_hash, durable_until = independent_approval_replay_binding(
        verified,
        retain_until=retain_until,
    )
    activated_at = int(now.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """
            UPDATE relationship_governance_transactions
               SET state='active',lifecycle_revision=2,
                   activation_basis='subordinate_owner_consent',
                   activated_at=?,updated_at=?,approval_receipt_id=?,
                   approval_receipt_digest=?,approval_receipt_json=?,
                   approval_approver_authority_id=?,
                   approval_approver_authority_kind=?,approval_verifier_id=?,
                   approval_signer_key_id=?,approval_expires_at=?
             WHERE relationship_id=?
            """,
            (
                activated_at,
                activated_at,
                verified.receipt_id,
                canonical_digest(receipt),
                canonical_json(receipt).decode("utf-8"),
                verified.approver_principal_id,
                verified.approver_authority_kind,
                verified.verifier_id,
                verified.signer_key_id,
                verified.expires_at,
                pending.relationship_id,
            ),
        )
        connection.execute(
            "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
            (actor_id, nonce_hash, durable_until),
        )
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            approval_verifier=relationship_approval_verifier,
            when=now + timedelta(minutes=6),
        ) == "missing_relationship_acceptance"
        assert connection.execute(
            "SELECT COUNT(*) AS count FROM audit_intents "
            "WHERE action='organization.relationship.activate'"
        ).fetchone()["count"] == 0


@pytest.mark.parametrize(
    "tamper_sql",
    (
        "DELETE FROM audit_intents WHERE action='organization.relationship.activate'",
        "UPDATE audit_intents SET request_digest=printf('%064d',0) "
        "WHERE action='organization.relationship.activate'",
        "UPDATE audit_intents SET actor_json='{}' "
        "WHERE action='organization.relationship.activate'",
        "UPDATE audit_intents SET state='pending',completed_at=NULL "
        "WHERE action='organization.relationship.activate'",
        "UPDATE audit_intents SET completed_at=completed_at+1 "
        "WHERE action='organization.relationship.activate'",
    ),
)
def test_active_owner_consent_requires_exact_completed_activation_intent(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
    tamper_sql,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    active = accept(
        service,
        pending,
        subordinate_actor,
        approval_receipt(
            pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    with store.transaction() as connection:
        connection.execute(tamper_sql)
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            approval_verifier=relationship_approval_verifier,
            when=now,
        ) == "missing_relationship_acceptance"


def test_persisted_transaction_rejects_float_to_integer_type_smuggling(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT canonical_transaction_json FROM relationship_governance_transactions "
            "WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()
        smuggled = json.loads(row["canonical_transaction_json"])
        smuggled["relationship"]["revision"] = 1.0
        smuggled_json = canonical_json(smuggled).decode("utf-8")
        smuggled_digest = canonical_digest(smuggled)
        connection.execute(
            """
            UPDATE relationship_governance_transactions
               SET canonical_transaction_json=?,transaction_digest=?
             WHERE relationship_id=?
            """,
            (smuggled_json, smuggled_digest, pending.relationship_id),
        )
    with pytest.raises(AuthenticationError, match="consent transaction is malformed"):
        service.accept(
            pending.relationship_id,
            actor=subordinate_actor,
            approval=receipt,
            expected_transaction_digest=smuggled_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"


@pytest.mark.parametrize("drift", ["policy", "credential", "owner_status"])
def test_acceptance_fails_closed_on_authority_drift(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
    drift,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    with store.transaction() as connection:
        if drift == "policy":
            connection.execute(
                "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id='domain-a'"
            )
        elif drift == "credential":
            replacement = P256KeyPair.generate()
            connection.execute(
                "UPDATE harnesses SET credential_epoch=2 WHERE harness_id='sub-harness'"
            )
            connection.execute(
                """
                INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,?,?,?,?)
                """,
                (
                    "sub-credential-2",
                    "sub-harness",
                    replacement.thumbprint,
                    replacement.public_pem,
                    "active",
                    2,
                    int(now.timestamp()) - 1,
                    int(now.timestamp()) + 3600,
                ),
            )
        else:
            connection.execute("UPDATE principals SET status='revoked' WHERE principal_id='sub-human'")

    with pytest.raises((AuthenticationError, AuthorizationError, ValidationError)):
        accept(service, pending, subordinate_actor, receipt, now)
    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 0


def test_renewal_requires_fresh_exact_consent_and_atomically_supersedes(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    first_pending = propose(service, store, relationship(now), admin_actor, now)
    first_receipt = approval_receipt(
        first_pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    first = accept(service, first_pending, subordinate_actor, first_receipt, now)

    second_pending = propose(
        service,
        store,
        relationship(now, revision=2, relationship_id="relationship-v2"),
        admin_actor,
        now,
    )
    assert second_pending.lifecycle_state == "proposed"
    assert service.get(
        first.relationship_id,
        authority=read_authority(store, first, admin_actor, now),
        when=now,
    ).lifecycle_state == "active"
    with pytest.raises(AuthenticationError, match="transaction binding"):
        accept(service, second_pending, subordinate_actor, first_receipt, now)

    second_receipt = approval_receipt(
        second_pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    second = accept(service, second_pending, subordinate_actor, second_receipt, now)
    previous = service.get(
        first.relationship_id,
        authority=read_authority(store, first, admin_actor, now),
        when=now,
    )
    assert second.lifecycle_state == "active"
    assert previous.lifecycle_state == "superseded"
    assert previous.superseded_by_relationship_id == second.relationship_id
    assert previous.revoked_at == now


def test_multiple_administrators_and_subordinates_form_exact_independent_edges(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    edge_specs = (
        (
            relationship(now, relationship_id="admin-to-sub"),
            admin_actor,
            subordinate_actor,
            "sub-human",
        ),
        (
            relationship(
                now,
                relationship_id="peer-to-sub",
                administrator_harness_id="peer-harness",
            ),
            peer_actor,
            subordinate_actor,
            "sub-human",
        ),
        (
            relationship(
                now,
                relationship_id="sub-to-peer",
                administrator_harness_id="sub-harness",
                subordinate_harness_id="peer-harness",
            ),
            subordinate_actor,
            peer_actor,
            "peer-human",
        ),
    )
    active = []
    for requested, proposer, activation_actor, owner_id in edge_specs:
        pending = propose(service, store, requested, proposer, now)
        active.append(
            accept(
                service,
                pending,
                activation_actor,
                approval_receipt(
                    pending,
                    principal_id=owner_id,
                    signer=relationship_approval_keys[owner_id],
                    verifier=relationship_approval_verifier,
                    now=now,
                ),
                now,
            )
        )

    assert {edge.lifecycle_state for edge in active} == {"active"}
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM relationship_governance_transactions WHERE state='active'"
    )["count"] == 3
    assert store.fetch_one(
        """
        SELECT COUNT(*) AS count FROM relationship_governance_transactions
         WHERE subordinate_harness_id='sub-harness' AND state='active'
        """
    )["count"] == 2


def test_renewal_acceptance_loses_to_signed_predecessor_revocation(
    store,
    admin_actor,
    subordinate_actor,
    identity_keys,
    signed_command,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    first_pending = propose(service, store, relationship(now), admin_actor, now)
    first = accept(
        service,
        first_pending,
        subordinate_actor,
        approval_receipt(
            first_pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    renewal = propose(
        service,
        store,
        relationship(now, revision=2, relationship_id="relationship-race-v2"),
        admin_actor,
        now,
    )
    renewal_receipt = approval_receipt(
        renewal,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    command, authority = revocation_evidence(
        store,
        service,
        first,
        subordinate_actor,
        identity_keys["sub-credential"],
        signed_command,
        now,
        reason="subordinate exits while renewal is pending",
    )
    assert service.revoke(first.relationship_id, command=command, authority=authority, when=now)
    with pytest.raises(ConflictError, match="stale proposal state"):
        accept(service, renewal, subordinate_actor, renewal_receipt, now)
    states = store.fetch_all(
        """
        SELECT relationship_id,state FROM relationship_governance_transactions
         WHERE domain_id='domain-a' AND administrator_harness_id='admin-harness'
           AND subordinate_harness_id='sub-harness'
        """
    )
    assert {row["relationship_id"]: row["state"] for row in states} == {
        first.relationship_id: "revoked",
        renewal.relationship_id: "revoked",
    }
    assert store.fetch_one(
        """
        SELECT COUNT(*) AS count FROM relationship_governance_transactions
         WHERE domain_id='domain-a' AND administrator_harness_id='admin-harness'
           AND subordinate_harness_id='sub-harness' AND state IN ('proposed','active')
        """
    )["count"] == 0


def test_prepared_signed_revocation_conflicts_after_renewal_activation(
    store,
    admin_actor,
    subordinate_actor,
    identity_keys,
    signed_command,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    first_pending = propose(service, store, relationship(now), admin_actor, now)
    first = accept(
        service,
        first_pending,
        subordinate_actor,
        approval_receipt(
            first_pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    renewal = propose(
        service,
        store,
        relationship(now, revision=2, relationship_id="relationship-renewed-before-revoke"),
        admin_actor,
        now,
    )
    command, authority = revocation_evidence(
        store,
        service,
        first,
        subordinate_actor,
        identity_keys["sub-credential"],
        signed_command,
        now,
        reason="prepared subordinate exit is exact-revision fenced",
    )
    renewed = accept(
        service,
        renewal,
        subordinate_actor,
        approval_receipt(
            renewal,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    assert renewed.lifecycle_state == "active"

    with pytest.raises(ConflictError, match="lifecycle changed before revocation"):
        service.revoke(first.relationship_id, command=command, authority=authority, when=now)

    records = store.fetch_all(
        """
        SELECT relationship_id,state FROM relationship_governance_transactions
         WHERE domain_id='domain-a' AND administrator_harness_id='admin-harness'
           AND subordinate_harness_id='sub-harness'
        """
    )
    assert {row["relationship_id"]: row["state"] for row in records} == {
        first.relationship_id: "superseded",
        renewal.relationship_id: "active",
    }
    assert store.fetch_one(
        """
        SELECT COUNT(*) AS count FROM relationship_governance_transactions
         WHERE domain_id='domain-a' AND administrator_harness_id='admin-harness'
           AND subordinate_harness_id='sub-harness' AND state IN ('proposed','active')
        """
    )["count"] == 1
    lineage = store.fetch_one(
        """
        SELECT revocation_epoch,last_revocation_command_id
          FROM relationship_governance_lineages
         WHERE domain_id='domain-a' AND administrator_harness_id='admin-harness'
           AND subordinate_harness_id='sub-harness'
        """
    )
    assert lineage["revocation_epoch"] == 0
    assert lineage["last_revocation_command_id"] is None


def test_subject_exit_and_security_admin_override_are_signed_and_fenced(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    active = accept(
        service,
        pending,
        subordinate_actor,
        approval_receipt(
            pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    command, authority = revocation_evidence(
        store,
        service,
        active,
        subordinate_actor,
        identity_keys["sub-credential"],
        signed_command,
        now,
        reason="subordinate owner exits",
    )
    assert service.revoke(active.relationship_id, command=command, authority=authority, when=now)
    audit = store.fetch_one("SELECT record_json FROM audit_log ORDER BY sequence DESC LIMIT 1")
    assert '"actor_role":"subject_exit"' in audit["record_json"]

    other_pending = propose(
        service,
        store,
        relationship(
            now,
            relationship_id="peer-relationship-v1",
            administrator_harness_id="sub-harness",
            subordinate_harness_id="peer-harness",
        ),
        subordinate_actor,
        now,
    )
    other_active = accept(
        service,
        other_pending,
        peer_actor,
        approval_receipt(
            other_pending,
            principal_id="peer-human",
            signer=relationship_approval_keys["peer-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    override, override_authority = revocation_evidence(
        store,
        service,
        other_active,
        admin_actor,
        identity_keys["admin-credential"],
        signed_command,
        now,
        action="organization.relationship.admin_revoke",
        reason="security administrator emergency override",
    )
    assert service.revoke(
        other_active.relationship_id,
        command=override,
        authority=override_authority,
        when=now,
    )
    audit = store.fetch_one("SELECT record_json FROM audit_log ORDER BY sequence DESC LIMIT 1")
    assert '"actor_role":"authorized_administrator"' in audit["record_json"]


def test_policy_exception_outsider_is_rejected_before_expensive_proof_validation(
    store,
    admin_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    monkeypatch,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=admin_actor,
        signer_key=identity_keys["admin-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="policy-exception-outsider-short-circuit",
    )
    proof_validation_called = False

    def fail_if_called(*_args, **_kwargs):
        nonlocal proof_validation_called
        proof_validation_called = True
        raise AssertionError("outsider path reached expensive persisted-proof validation")

    monkeypatch.setattr(service, "_validate_persisted_policy_exception", fail_if_called)
    with pytest.raises(AuthorizationError, match="current exact participant"):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=peer_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    assert proof_validation_called is False
    assert store.fetch_one(
        "SELECT consumed_at FROM relationship_policy_exceptions WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )["consumed_at"] is None


def test_exact_recorded_policy_exception_signer_can_activate_and_remain_authoritative(
    store,
    admin_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="policy-exception-signer-activation-positive",
    )
    active = service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=peer_actor,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=now,
    )
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now,
        ) is None
        intents = connection.execute(
            "SELECT intent_id,action,policy_decision_id,state,created_at,completed_at "
            "FROM audit_intents ORDER BY action"
        ).fetchall()
        assert len(intents) == 2
        activation_intent = next(
            item
            for item in intents
            if item["action"] == "organization.relationship.activate"
        )
        assert activation_intent["intent_id"].startswith("relationship-activation:")
        assert activation_intent["policy_decision_id"] == (
            f"policy-exception:{recorded.policy_exception_id}"
        )
        assert activation_intent["state"] == "completed"
        assert activation_intent["created_at"] == activation_intent["completed_at"]


@pytest.mark.parametrize("revoked_binding", ("guest", "grant", "credential"))
def test_exact_guest_policy_exception_signer_activates_then_revocation_fails_closed(
    store,
    admin_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    revoked_binding,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    guest_signer = make_guest_policy_signer(
        store,
        identity_keys["peer-credential"],
        now,
    )
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=guest_signer,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"guest-policy-exception-{revoked_binding}",
    )

    active = service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=guest_signer,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=now,
    )
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now,
        ) is None

        if revoked_binding == "guest":
            connection.execute(
                "UPDATE guests SET status='revoked' WHERE guest_id=?",
                (guest_signer.guest_id,),
            )
        elif revoked_binding == "grant":
            connection.execute(
                "UPDATE task_grants SET revoked_at=? WHERE grant_id=?",
                (
                    int(now.timestamp()),
                    f"{recorded.policy_exception_id}-guest-authority",
                ),
            )
        else:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (guest_signer.credential_id,),
            )

        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now,
        ) == "stale_relationship_policy_exception"


def test_expired_unused_policy_exception_cannot_be_backdated_into_active_authority(
    store,
    admin_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="unused-expired-policy-exception-0001",
    )
    activated_at = int(now.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """
            UPDATE relationship_policy_exceptions
               SET consumed_at=?,lifecycle_revision=2
             WHERE policy_exception_id=?
            """,
            (activated_at, recorded.policy_exception_id),
        )
        connection.execute(
            """
            UPDATE relationship_governance_transactions
               SET state='active',lifecycle_revision=2,
                   activation_basis='domain_policy_exception',
                   policy_exception_id=?,activated_at=?,updated_at=?
             WHERE relationship_id=?
            """,
            (
                recorded.policy_exception_id,
                activated_at,
                activated_at,
                pending.relationship_id,
            ),
        )
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (pending.relationship_id,),
        ).fetchone()
        assert connection.execute(
            "SELECT COUNT(*) AS count FROM audit_intents "
            "WHERE action='organization.relationship.policy_exception.record'"
        ).fetchone()["count"] == 1
        assert connection.execute(
            "SELECT COUNT(*) AS count FROM audit_intents "
            "WHERE action='organization.relationship.activate'"
        ).fetchone()["count"] == 0
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now + timedelta(minutes=11),
        ) == "stale_relationship_policy_exception"


def test_exact_signed_domain_policy_exception_can_activate_once(
    store,
    admin_actor,
    signed_command,
    identity_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    exception = RelationshipPolicyException(
        policy_exception_id="relationship-policy-exception-0001",
        domain_id=pending.domain_id,
        relationship_id=pending.relationship_id,
        relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        relationship_transaction_digest=pending.transaction_digest,
        reason="recorded legal employment policy exception",
        expires_at=now + timedelta(minutes=10),
    )
    resource, request = service.policy_exception_binding(exception)
    action = "organization.relationship.policy_exception.record"
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=admin_actor.domain_id,
            principal_id=admin_actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=pending.expires_at,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=admin_actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(request)},
        ),
        when=now,
    )
    command = signed_command(
        key=identity_keys["admin-credential"],
        actor=admin_actor,
        action=action,
        resource=resource,
        request=request,
        now=now,
        entity_revision=pending.lifecycle_revision,
        reason=exception.reason,
    )
    recorded = service.record_policy_exception(
        exception,
        command=command,
        authority=IssuanceAuthority(
            actor=admin_actor,
            policy_decision_id=decision.decision_id,
        ),
        when=now,
    )
    active = service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=admin_actor,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=now,
    )
    assert active.lifecycle_state == "active"
    assert active.activation_basis == "domain_policy_exception"
    assert active.policy_exception_id == exception.policy_exception_id
    assert active.approval_receipt_id is None
    with pytest.raises(ConflictError, match="stale proposal state"):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=admin_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )


@pytest.mark.parametrize(
    "drift",
    ["credential_revoked", "credential_rotated", "owner_kind_changed"],
)
def test_policy_exception_activation_rejects_current_signer_authority_drift(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    drift,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"policy-exception-signer-drift-{drift}",
    )
    epoch = int(now.timestamp())
    with store.transaction() as connection:
        if drift == "credential_revoked":
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id='peer-credential'"
            )
        elif drift == "credential_rotated":
            replacement = P256KeyPair.generate()
            connection.execute(
                "UPDATE harnesses SET credential_epoch=2 WHERE harness_id='peer-harness'"
            )
            connection.execute(
                """
                INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,?,?,?,?)
                """,
                (
                    "peer-credential-2",
                    "peer-harness",
                    replacement.thumbprint,
                    replacement.public_pem,
                    "active",
                    2,
                    epoch - 1,
                    epoch + 3600,
                ),
            )
        else:
            connection.execute(
                """
                INSERT INTO guests(
                    guest_id,host_domain_id,home_domain_id,pairwise_subject,
                    sponsor_principal_id,status,expires_at
                ) VALUES(?,?,?,?,?,?,?)
                """,
                (
                    "policy-signer-guest",
                    "domain-a",
                    "partner.example",
                    "policy-signer-pairwise",
                    "admin-human",
                    "active",
                    epoch + 3600,
                ),
            )
            connection.execute(
                """
                UPDATE harnesses SET principal_id=NULL,guest_id='policy-signer-guest'
                 WHERE harness_id='peer-harness'
                """
            )

    with pytest.raises(AuthenticationError, match="signer authority is stale"):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=subordinate_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    assert store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )["state"] == "proposed"
    exception_row = store.fetch_one(
        "SELECT consumed_at FROM relationship_policy_exceptions WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )
    assert exception_row["consumed_at"] is None


def test_active_policy_exception_edge_fails_closed_after_signer_credential_revocation(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="policy-exception-post-activation-signer-revocation",
    )
    active = service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=subordinate_actor,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=now,
    )
    assert active.lifecycle_state == "active"
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id='peer-credential'"
        )
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=now,
        ) == "stale_relationship_policy_exception"


@pytest.mark.parametrize(
    "tamper_sql",
    (
        "UPDATE relationship_policy_exceptions "
        "SET relationship_revision=relationship_revision+1 WHERE policy_exception_id=?",
        "UPDATE relationship_policy_exceptions "
        "SET signer_authority_id='retargeted-authority' WHERE policy_exception_id=?",
        "UPDATE relationship_policy_exceptions "
        "SET exception_json='{}' WHERE policy_exception_id=?",
        "UPDATE relationship_policy_exceptions "
        "SET command_json='{}' WHERE policy_exception_id=?",
    ),
)
def test_policy_exception_activation_rejects_mutable_projection_or_signed_byte_tampering(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    tamper_sql,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"policy-exception-tamper-{abs(hash(tamper_sql))}",
    )
    with store.transaction() as connection:
        connection.execute(tamper_sql, (recorded.policy_exception_id,))

    with pytest.raises(AuthorizationError, match="not visible"):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=subordinate_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    persisted = store.fetch_one(
        "SELECT state FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )
    assert persisted["state"] == "proposed"


@pytest.mark.parametrize("tamper", ["decision_context", "audit_intent"])
def test_policy_exception_activation_requires_exact_decision_and_completed_intent(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    tamper,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"policy-exception-ceremony-{tamper}",
    )
    exception_row = store.fetch_one(
        """
        SELECT policy_decision_id,command_id FROM relationship_policy_exceptions
         WHERE policy_exception_id=?
        """,
        (recorded.policy_exception_id,),
    )
    with store.transaction() as connection:
        if tamper == "decision_context":
            connection.execute(
                "UPDATE policy_decisions SET context_json='{}' WHERE decision_id=?",
                (exception_row["policy_decision_id"],),
            )
        else:
            connection.execute(
                "UPDATE audit_intents SET state='pending',completed_at=NULL WHERE intent_id=?",
                (exception_row["command_id"],),
            )

    with pytest.raises(AuthorizationError, match="not visible"):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=subordinate_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    assert store.fetch_one(
        "SELECT consumed_at FROM relationship_policy_exceptions WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )["consumed_at"] is None


@pytest.mark.parametrize("drift", ["original_entitlement_revoked", "policy_revision"])
def test_policy_exception_activation_rejects_signer_positive_authority_drift(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    drift,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"policy-exception-positive-authority-{drift}",
    )
    exception_row = store.fetch_one(
        """
        SELECT policy_decision_id FROM relationship_policy_exceptions
         WHERE policy_exception_id=?
        """,
        (recorded.policy_exception_id,),
    )
    decision = store.fetch_one(
        "SELECT context_json FROM policy_decisions WHERE decision_id=?",
        (exception_row["policy_decision_id"],),
    )
    entitlement_id = json.loads(decision["context_json"])["entitlement_id"]
    with store.transaction() as connection:
        if drift == "original_entitlement_revoked":
            connection.execute(
                "UPDATE entitlements SET revoked_at=? WHERE entitlement_id=?",
                (int(now.timestamp()), entitlement_id),
            )
        else:
            connection.execute(
                "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id='domain-a'"
            )

    with pytest.raises((AuthenticationError, AuthorizationError, ConflictError)):
        service.activate_with_policy_exception(
            pending.relationship_id,
            policy_exception_id=recorded.policy_exception_id,
            actor=subordinate_actor,
            expected_transaction_digest=pending.transaction_digest,
            expected_relationship_revision=pending.revision,
            expected_lifecycle_revision=pending.lifecycle_revision,
            when=now,
        )
    assert store.fetch_one(
        "SELECT consumed_at FROM relationship_policy_exceptions WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )["consumed_at"] is None


def test_recorded_policy_exception_survives_command_and_exception_deadline_after_activation(
    store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id="policy-exception-historical-command-validity",
    )
    activation_time = now + timedelta(minutes=6)
    active = service.activate_with_policy_exception(
        pending.relationship_id,
        policy_exception_id=recorded.policy_exception_id,
        actor=subordinate_actor,
        expected_transaction_digest=pending.transaction_digest,
        expected_relationship_revision=pending.revision,
        expected_lifecycle_revision=pending.lifecycle_revision,
        when=activation_time,
    )
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            approval_verifier=relationship_approval_verifier,
            when=now + timedelta(minutes=11),
        ) is None


def test_guest_positive_authority_owner_can_independently_consent(
    store,
    admin_actor,
    now,
):
    epoch = int(now.timestamp())
    endpoint_key = P256KeyPair.generate()
    approval_key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,
                sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,?,?)
            """,
            ("guest-owner", "domain-a", "partner.example", "pairwise-owner", "admin-human", "active", epoch + 3600),
        )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """,
            ("guest-harness", "domain-a", None, "guest-owner", "codex", "Guest", "active", "os_bound", "{}", 1, epoch - 1),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "guest-credential",
                "guest-harness",
                endpoint_key.thumbprint,
                endpoint_key.public_pem,
                "active",
                1,
                epoch - 1,
                epoch + 3600,
            ),
        )
    trusted = TrustedApprover(
        principal_id="guest-owner",
        domain_id="domain-a",
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        authority_kind="guest",
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="guest-owner-approval.example",
    )
    service = RelationshipService(store, approval_verifier=verifier)
    pending = propose(
        service,
        store,
        relationship(now, subordinate_harness_id="guest-harness"),
        admin_actor,
        now,
    )
    wrong_kind_trust = TrustedApprover(
        principal_id="guest-owner",
        domain_id="domain-a",
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        authority_kind="human",
    )
    wrong_kind_verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: wrong_kind_trust},
        verifier_id="guest-owner-approval.example",
    )
    wrong_kind_receipt = create_independent_approval_receipt(
        approval_key,
        approver=wrong_kind_trust,
        verifier_id=wrong_kind_verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(pending.consent_transaction.model_dump(mode="json")),
        issued_at=epoch,
        expires_at=epoch + 300,
    )
    service.approval_verifier = wrong_kind_verifier
    with pytest.raises(AuthorizationError, match="current owner"):
        accept(service, pending, admin_actor, wrong_kind_receipt, now)
    service.approval_verifier = verifier
    receipt = create_independent_approval_receipt(
        approval_key,
        approver=trusted,
        verifier_id=verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(pending.consent_transaction.model_dump(mode="json")),
        issued_at=epoch,
        expires_at=epoch + 300,
    )
    active = accept(service, pending, admin_actor, receipt, now)
    assert active.lifecycle_state == "active"
    assert active.consent_transaction.subordinate_owner_kind == "guest"
    assert active.approval_approver_authority_id == "guest-owner"


def test_expiry_is_automatic_and_never_extends_consent_scope(
    store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(
        service,
        store,
        relationship(now, expires_in=timedelta(minutes=20)),
        admin_actor,
        now,
        proposal_expires_at=now + timedelta(minutes=10),
    )
    active = accept(
        service,
        pending,
        subordinate_actor,
        approval_receipt(
            pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
        ),
        now,
    )
    after_expiry = now + timedelta(minutes=21)
    with store.transaction() as connection:
        row = connection.execute(
            "SELECT * FROM relationship_governance_transactions WHERE relationship_id=?",
            (active.relationship_id,),
        ).fetchone()
        assert RelationshipService.authority_binding_denial(
            connection,
            row,
            current_policy_revision=1,
            when=after_expiry,
        ) == "relationship_expired"
    expired = service.get(
        active.relationship_id,
        authority=read_authority(
            store,
            active,
            admin_actor,
            after_expiry,
            entitlement_expires_at=after_expiry + timedelta(hours=1),
        ),
        when=after_expiry,
    )
    assert expired.lifecycle_state == "expired"
    persisted = store.fetch_one(
        "SELECT state,lifecycle_revision FROM relationship_governance_transactions WHERE relationship_id=?",
        (active.relationship_id,),
    )
    assert persisted["state"] == "expired"
    assert persisted["lifecycle_revision"] == active.lifecycle_revision + 1
    audit = store.fetch_one("SELECT record_json FROM audit_log ORDER BY sequence DESC LIMIT 1")
    assert '"type":"relationship_expired"' in audit["record_json"]


def test_scope_rejects_authority_smuggling_and_nonassigning_scope(store, now):
    smuggled = relationship(now).model_copy(
        update={
            "assignment_scope": {
                **scope().model_dump(mode="json"),
                "data_permissions": ["read:any"],
            }
        }
    )
    with pytest.raises(PydanticValidationError):
        RelationshipService(store).issue(smuggled, when=now)

    nonassigning = relationship(now).model_copy(update={"may_assign": False})
    with pytest.raises(ValidationError, match="empty assignment scope"):
        RelationshipService(store).issue(nonassigning, when=now)
