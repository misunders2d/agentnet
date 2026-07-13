from __future__ import annotations

from datetime import timedelta

import pytest

from agentnet.authorization import (
    AuthorizationRequest,
    GrantUse,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
    OperationClass,
    PolicyEngine,
    TaskGrantService,
)
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.signatures import P256KeyPair, canonical_digest


def seed_entitlement(engine: PolicyEngine, actor: VerifiedActor, now, future):
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="data.read",
            resource_pattern="dataset:alpha",
            revision=1,
            expires_at=future,
        ),
        when=now,
    )


def task_grant(actor: VerifiedActor, future, *, max_uses: int = 1) -> TaskGrant:
    return TaskGrant(
        domain_id=actor.domain_id,
        principal_id=actor.positive_authority_id,
        harness_id=actor.harness_id,
        actions=frozenset({"data.read"}),
        resources=frozenset({"dataset:alpha"}),
        input_sources=frozenset({"mailbox:event-1"}),
        output_sinks=frozenset({"worker:clean-1"}),
        data_classes=frozenset({Classification.C2_RESTRICTED}),
        max_uses=max_uses,
        expires_at=future,
    )


def grant_use(grant: TaskGrant) -> GrantUse:
    return GrantUse(
        grant_id=grant.grant_id,
        action="data.read",
        resource="dataset:alpha",
        input_source="mailbox:event-1",
        output_sink="worker:clean-1",
        data_class=Classification.C2_RESTRICTED,
    )


def protected_request(actor: VerifiedActor, use: GrantUse) -> AuthorizationRequest:
    return AuthorizationRequest(
        actor=actor,
        action="data.read",
        resource="dataset:alpha",
        operation_class=OperationClass.PROTECTED_READ,
        policy_revision=1,
        grant_use=use,
    )


def authorize_grant_issue(engine: PolicyEngine, grant: TaskGrant, actor: VerifiedActor, now, future) -> IssuanceAuthority:
    resource, context = engine.grants.issuance_binding(grant)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.issue",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.issue",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def test_exact_grant_intersection_consumes_once_transactionally(store, actor, now, future):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )

    allowed = engine.decide(protected_request(actor, grant_use(grant)), when=now)
    exhausted = engine.decide(protected_request(actor, grant_use(grant)), when=now)

    assert allowed.allowed is True
    assert allowed.context["task_grant_consumed"] is True
    assert exhausted.allowed is False
    assert exhausted.reason == "task_grant_exhausted"
    assert engine.grants.uses_for_local_conformance(grant.grant_id) == 1
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 3


@pytest.mark.parametrize(
    ("field", "value", "reason"),
    [
        ("action", "data.export", "task_grant_request_mismatch"),
        ("resource", "dataset:beta", "task_grant_request_mismatch"),
        ("input_source", "mailbox:event-2", "task_grant_source_mismatch"),
        ("output_sink", "external:web", "task_grant_sink_mismatch"),
        ("data_class", Classification.C1_INTERNAL, "task_grant_class_mismatch"),
    ],
)
def test_every_grant_dimension_is_enforced_without_consumption(store, actor, now, future, field, value, reason):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )
    use = grant_use(grant).model_copy(update={field: value})

    decision = engine.decide(protected_request(actor, use), when=now)

    assert decision.allowed is False
    assert decision.reason == reason
    assert engine.grants.uses_for_local_conformance(grant.grant_id) == 0


def test_decision_persistence_failure_rolls_back_grant_consumption(store, actor, now, future, monkeypatch):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )

    def fail_record(_connection, _decision):
        raise RuntimeError("simulated decision store failure")

    monkeypatch.setattr(engine.recorder, "record", fail_record)
    with pytest.raises(RuntimeError, match="decision store failure"):
        engine.decide(protected_request(actor, grant_use(grant)), when=now)

    assert engine.grants.uses_for_local_conformance(grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 1


def test_revoked_grant_denies_without_consumption(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )
    reason = "beneficiary ended the task"
    resource, revoke_request = engine.grants.revocation_binding(
        grant.grant_id,
        expected_entity_revision=1,
        reason=reason,
    )
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.revoke",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(revoke_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.task_grant.revoke",
        resource=resource,
        request=revoke_request,
        now=now,
        entity_revision=1,
        reason=reason,
    )
    assert engine.grants.revoke(
        grant.grant_id,
        command=command,
        authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        when=now,
    ) is True

    decision = engine.decide(protected_request(actor, grant_use(grant)), when=now)

    assert decision.allowed is False
    assert decision.reason == "task_grant_revoked"
    assert engine.grants.uses_for_local_conformance(grant.grant_id) == 0
    assert engine.grants.get_for_local_conformance(grant.grant_id).revoked_at == now


def test_host_guest_positive_authority_requires_exact_host_grant(store, now, future):
    epoch = int(now.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,
                sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,?,?)
            """,
            ("guest-a", "domain-a", "partner-b", "pairwise-a", "human-a", "active", epoch + 86400),
        )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """,
            ("guest-harness", "domain-a", None, "guest-a", "codex", "Guest", "active", "os_bound", "{}", 1, epoch - 1),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            ("guest-credential", "guest-harness", "guest-key", "test", "active", 1, epoch - 1, epoch + 86400),
        )
    guest = VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id="domain-a",
        guest_id="guest-a",
        harness_id="guest-harness",
        credential_id="guest-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    engine = LocalConformancePolicyEngine(store)
    requested = task_grant(guest, future)
    with store.transaction() as connection:
        grant = engine.grants._insert_in_transaction(
            connection,
            grant=requested,
            when=now,
            issuance_evidence={"kind": "test_host_admission_fixture"},
        )

    no_grant = engine.decide(
        AuthorizationRequest(
            actor=guest,
            action="data.read",
            resource="dataset:alpha",
            operation_class=OperationClass.PROTECTED_READ,
            policy_revision=1,
        ),
        when=now,
    )
    allowed = engine.decide(protected_request(guest, grant_use(grant)), when=now)

    assert no_grant.allowed is False
    assert no_grant.reason == "exact_task_grant_required"
    assert allowed.allowed is True
    assert allowed.context["positive_authority_id"] == "guest-a"


def test_task_grant_issue_fails_closed_without_exact_issuer_evidence(store, actor, now, future):
    service = TaskGrantService(store)
    requested = task_grant(actor, future)

    with pytest.raises(AuthorizationError, match="authority evidence"):
        service.issue(requested, when=now)
    assert service.get_for_local_conformance(requested.grant_id) is None


def test_task_grant_issue_rejects_wrong_request_digest_and_cross_human_beneficiary(
    store, actor, now, future
):
    engine = LocalConformancePolicyEngine(store)
    requested = task_grant(actor, future)
    resource, _ = engine.grants.issuance_binding(requested)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.issue",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    wrong = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.issue",
            resource=resource,
            policy_revision=1,
            context={"request_digest": "0" * 64},
        ),
        when=now,
    )
    authority = IssuanceAuthority(actor=actor, policy_decision_id=wrong.decision_id)
    with pytest.raises(AuthorizationError, match="request binding mismatch"):
        engine.grants.issue(requested, authority=authority, when=now)

    cross_human = requested.model_copy(update={"principal_id": "admin-human"})
    cross_authority = authorize_grant_issue(engine, cross_human, actor, now, future)
    with pytest.raises(AuthorizationError, match="exact beneficiary"):
        engine.grants.issue(cross_human, authority=cross_authority, when=now)


def test_task_grant_read_is_authenticated_non_enumerating_and_harness_scoped(
    store, actor, now, future
):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )
    resource, context = engine.grants.read_binding(grant.grant_id)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.read",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    owner_decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.read",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    owner_authority = IssuanceAuthority(actor=actor, policy_decision_id=owner_decision.decision_id)
    assert engine.grants.get(grant.grant_id, authority=owner_authority, when=now) == grant

    sibling_key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """,
            (
                "sibling-harness",
                actor.domain_id,
                actor.principal_id,
                None,
                "pi",
                "Sibling",
                "active",
                "os_bound",
                "{}",
                1,
                int(now.timestamp()) - 1,
            ),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "sibling-credential",
                "sibling-harness",
                sibling_key.thumbprint,
                sibling_key.public_pem,
                "active",
                1,
                int(now.timestamp()) - 1,
                int(future.timestamp()),
            ),
        )
    sibling = actor.model_copy(
        update={"harness_id": "sibling-harness", "credential_id": "sibling-credential"}
    )
    sibling_decision = engine.require(
        AuthorizationRequest(
            actor=sibling,
            action="authorization.task_grant.read",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    assert engine.grants.get(
        grant.grant_id,
        authority=IssuanceAuthority(actor=sibling, policy_decision_id=sibling_decision.decision_id),
        when=now,
    ) is None
    with pytest.raises(AuthorizationError, match="authority evidence"):
        engine.grants.get(grant.grant_id, when=now)


def test_task_grant_consume_revoke_race_and_policy_drift_fail_closed(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    seed_entitlement(engine, actor, now, future)
    requested = task_grant(actor, future, max_uses=2)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )
    reason = "cancel task before execution"
    resource, revoke_request = engine.grants.revocation_binding(
        grant.grant_id,
        expected_entity_revision=1,
        reason=reason,
    )
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    revoke_decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.revoke",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(revoke_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.task_grant.revoke",
        resource=resource,
        request=revoke_request,
        now=now,
        entity_revision=1,
        reason=reason,
    )

    assert engine.decide(protected_request(actor, grant_use(grant)), when=now).allowed is True
    with pytest.raises(ConflictError, match="revision changed"):
        engine.grants.revoke(
            grant.grant_id,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=revoke_decision.decision_id),
            when=now,
        )
    assert store.fetch_one(
        "SELECT intent_id FROM audit_intents WHERE intent_id=?", (command.command_id,)
    ) is None

    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id=?", (actor.domain_id,))
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="data.read",
            resource_pattern="dataset:alpha",
            revision=2,
            expires_at=future,
        ),
        when=now,
    )
    drifted = engine.decide(
        protected_request(actor, grant_use(grant)).model_copy(update={"policy_revision": 2}),
        when=now,
    )
    assert drifted.allowed is False
    assert drifted.reason == "stale_task_grant_policy_binding"


def test_task_grant_revoke_audit_failure_rolls_back_mutation(
    store, actor, actor_key, signed_command, now, future, monkeypatch
):
    engine = LocalConformancePolicyEngine(store)
    requested = task_grant(actor, future)
    grant = engine.grants.issue(
        requested,
        authority=authorize_grant_issue(engine, requested, actor, now, future),
        when=now,
    )
    reason = "rollback probe"
    resource, revoke_request = engine.grants.revocation_binding(
        grant.grant_id,
        expected_entity_revision=1,
        reason=reason,
    )
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.task_grant.revoke",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(revoke_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.task_grant.revoke",
        resource=resource,
        request=revoke_request,
        now=now,
        entity_revision=1,
        reason=reason,
    )

    def fail_audit(_store, _connection, _record):
        raise RuntimeError("simulated audit failure")

    monkeypatch.setattr(type(store), "append_audit", fail_audit)
    with pytest.raises(RuntimeError, match="audit failure"):
        engine.grants.revoke(
            grant.grant_id,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
            when=now,
        )
    assert store.fetch_one(
        "SELECT revoked_at FROM task_grants WHERE grant_id=?", (grant.grant_id,)
    )["revoked_at"] is None
    assert store.fetch_one(
        "SELECT intent_id FROM audit_intents WHERE intent_id=?", (command.command_id,)
    ) is None
