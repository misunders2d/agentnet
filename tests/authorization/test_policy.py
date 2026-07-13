from __future__ import annotations

import pytest

from agentnet.authorization import (
    AuthorizationRequest,
    DenyOnlyEligibility,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
    OperationClass,
    PolicyEngine,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.policy_defaults import AttenuationPolicy
from agentnet.protocol.models import Classification
from agentnet.security.signatures import P256KeyPair, canonical_digest


def entitlement(actor, future, *, action: str = "message.send", resource: str = "room:alpha") -> HumanEntitlement:
    return HumanEntitlement(
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        action=action,
        resource_pattern=resource,
        revision=1,
        expires_at=future,
    )


def request(actor, *, eligibility: DenyOnlyEligibility | None = None, revision: int = 1) -> AuthorizationRequest:
    return AuthorizationRequest(
        actor=actor,
        action="message.send",
        resource="room:alpha",
        operation_class=OperationClass.BUSINESS,
        policy_revision=revision,
        eligibility=eligibility or DenyOnlyEligibility(),
    )


def as_lab_actor(store, actor):
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='lab' WHERE harness_id=?",
            (actor.harness_id,),
        )
    return actor.model_copy(update={"binding_assurance": "lab"})


def test_human_entitlement_allows_and_persists_decision(store, actor, now, future):
    engine = LocalConformancePolicyEngine(store)
    granted = engine.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)

    decision = engine.decide(request(actor), when=now)

    assert decision.allowed is True
    assert decision.context["entitlement_id"] == granted.entitlement_id
    assert engine.recorder.get(decision.decision_id) == decision
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 1
    assert store.verify_audit_chain()[0] is True


def test_eligible_harness_cannot_create_positive_authority(store, actor, now):
    engine = PolicyEngine(store)

    decision = engine.decide(request(actor), when=now)

    assert decision.allowed is False
    assert decision.reason == "no_positive_human_entitlement"
    assert decision.context["eligibility"]["harness_eligible"] is True
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 1


def test_harness_session_and_device_are_deny_only(store, actor, now, future):
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)

    for field, reason in (
        ("harness_eligible", "harness_ineligible"),
        ("session_eligible", "session_ineligible"),
        ("device_eligible", "device_ineligible"),
    ):
        eligibility = DenyOnlyEligibility(**{field: False})
        decision = engine.decide(request(actor, eligibility=eligibility), when=now)
        assert decision.allowed is False
        assert decision.reason == reason

    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 3


def test_missing_stale_and_revoked_state_denies_and_persists(store, actor, now, future):
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)

    stale = engine.decide(request(actor, revision=2), when=now)
    assert stale.allowed is False
    assert stale.reason == "stale_policy_revision"

    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (actor.harness_id,))
    revoked = engine.decide(request(actor), when=now)
    assert revoked.allowed is False
    assert revoked.reason == "harness_not_active"

    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='active' WHERE harness_id=?", (actor.harness_id,))
    missing_actor = actor.model_copy(update={"principal_id": "missing-human"})
    missing = engine.decide(request(missing_actor), when=now)
    assert missing.allowed is False
    assert missing.reason == "missing_principal_state"

    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == 3


def test_workload_cannot_gain_positive_authority(store, now):
    engine = PolicyEngine(store)
    workload = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="domain-a",
        workload_id="effect-worker",
        parent_event_id="event-1",
        task_grant_id="grant-1",
        binding_assurance="internal_process",
    )
    decision = engine.decide(
        AuthorizationRequest(
            actor=workload,
            action="message.send",
            resource="room:alpha",
            policy_revision=1,
        ),
        when=now,
    )
    assert decision.allowed is False
    assert decision.reason == "actor_kind_has_no_positive_authority"


def test_revoked_positive_entitlement_denies_and_records(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    granted = engine.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)
    reason = "operator removed obsolete message authority"
    resource, revoke_request = engine.entitlement_revocation_binding(
        granted.entitlement_id,
        expected_entity_revision=granted.revision,
        reason=reason,
    )
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.entitlement.revoke",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.entitlement.revoke",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(revoke_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.entitlement.revoke",
        resource=resource,
        request=revoke_request,
        now=now,
        entity_revision=granted.revision,
        reason=reason,
    )
    assert engine.revoke_entitlement(
        granted.entitlement_id,
        command=command,
        authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        when=now,
    ) is True

    decision = engine.decide(request(actor), when=now)

    assert decision.allowed is False
    assert decision.reason == "no_current_positive_entitlement"
    assert engine.recorder.get(decision.decision_id) == decision


def test_stricter_binding_assurance_policy_only_attenuates_existing_human_authority(
    store, actor, now, future
):
    default = LocalConformancePolicyEngine(store, attenuation_policy=AttenuationPolicy())
    default.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)
    assert default.decide(request(actor), when=now).allowed is True

    strict = PolicyEngine(
        store,
        attenuation_policy=AttenuationPolicy(minimum_binding_assurance="hardware_bound"),
    )
    denied = strict.decide(request(actor), when=now)
    assert denied.allowed is False
    assert denied.reason == "binding_assurance_below_policy_floor"


def test_entitlement_issue_requires_signed_current_one_time_authority(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    requested = entitlement(actor, future, action="data.read", resource="dataset:alpha")
    reason = "grant bounded dataset access"
    resource, issue_request = engine.entitlement_issuance_binding(requested, reason=reason)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.entitlement.issue",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.entitlement.issue",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(issue_request)},
        ),
        when=now,
    )
    authority = IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.entitlement.issue",
        resource=resource,
        request=issue_request,
        now=now,
        entity_revision=0,
        reason=reason,
    )

    with pytest.raises(AuthorizationError, match="signed authority command"):
        engine.add_entitlement(requested, when=now)
    assert engine.add_entitlement(requested, command=command, authority=authority, when=now) == requested
    with pytest.raises(ConflictError, match="already consumed"):
        engine.add_entitlement(requested, command=command, authority=authority, when=now)
    assert store.fetch_one(
        "SELECT state FROM audit_intents WHERE intent_id=?", (command.command_id,)
    )["state"] == "completed"


def test_entitlement_issue_rechecks_authorizing_entitlement_and_policy_revision(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    requested = entitlement(actor, future, action="data.read", resource="dataset:beta")
    reason = "issue exact beta access"
    resource, issue_request = engine.entitlement_issuance_binding(requested, reason=reason)
    issuer_entitlement = engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.entitlement.issue",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.entitlement.issue",
            resource=resource,
            policy_revision=engine.current_policy_revision(actor, when=now),
            context={"request_digest": canonical_digest(issue_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.entitlement.issue",
        resource=resource,
        request=issue_request,
        now=now,
        entity_revision=0,
        reason=reason,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE entitlements SET revoked_at=? WHERE entitlement_id=?",
            (int(now.timestamp()), issuer_entitlement.entitlement_id),
        )
    with pytest.raises(AuthorizationError, match="no longer current"):
        engine.add_entitlement(
            requested,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
            when=now,
        )

    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id=?", (actor.domain_id,))
    assert engine.current_policy_revision(actor, when=now) == 2
    with pytest.raises(AuthorizationError, match="stale_policy_revision"):
        engine.add_entitlement(
            requested,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
            when=now,
        )


def test_entitlement_command_is_fenced_by_recovery_credential_epoch(
    store, actor, actor_key, signed_command, now, future
):
    engine = LocalConformancePolicyEngine(store)
    requested = entitlement(actor, future, action="data.read", resource="dataset:recovery")
    reason = "pre-recovery command must not survive rotation"
    resource, issue_request = engine.entitlement_issuance_binding(requested, reason=reason)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.entitlement.issue",
            resource_pattern=resource,
            revision=1,
            expires_at=future,
        ),
        when=now,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="authorization.entitlement.issue",
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(issue_request)},
        ),
        when=now,
    )
    command = signed_command(
        key=actor_key,
        actor=actor,
        action="authorization.entitlement.issue",
        resource=resource,
        request=issue_request,
        now=now,
        entity_revision=0,
        reason=reason,
    )
    recovered_key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='retired' WHERE credential_id=?",
            (actor.credential_id,),
        )
        connection.execute(
            "UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?",
            (actor.harness_id,),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "credential-recovered",
                actor.harness_id,
                recovered_key.thumbprint,
                recovered_key.public_pem,
                "active",
                2,
                int(now.timestamp()),
                int(future.timestamp()),
            ),
        )
    with pytest.raises(AuthenticationError, match="credential"):
        engine.add_entitlement(
            requested,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
            when=now,
        )


@pytest.mark.parametrize(
    "profile",
    [RuntimeProfile.LOCAL_CONFORMANCE, RuntimeProfile.ALWAYS_ON_SERVER_AGENT],
)
def test_production_policy_has_no_lab_bootstrap_for_any_runtime_profile(store, profile):
    engine = PolicyEngine(store, runtime_profile=profile)

    assert not hasattr(engine, "bootstrap_entitlement_for_local_conformance")


def test_production_policy_never_applies_local_c0_allowance(store, actor, now, future):
    actor = as_lab_actor(store, actor)
    local = LocalConformancePolicyEngine(
        store,
        attenuation_policy=AttenuationPolicy(minimum_binding_assurance="hardware_bound"),
    )
    local.bootstrap_entitlement_for_local_conformance(entitlement(actor, future), when=now)
    candidate = request(actor).model_copy(update={"classification": Classification.C0_PUBLIC})

    production = PolicyEngine(
        store,
        runtime_profile=RuntimeProfile.LOCAL_CONFORMANCE,
        attenuation_policy=AttenuationPolicy(minimum_binding_assurance="hardware_bound"),
    )

    assert local.decide(candidate, when=now).allowed is True
    denied = production.decide(candidate, when=now)
    assert denied.allowed is False
    assert denied.reason == "binding_assurance_below_policy_floor"


@pytest.mark.parametrize(
    ("action", "operation_class", "classification"),
    [
        ("data.read", OperationClass.BUSINESS, "C0"),
        ("effect.execute", OperationClass.PROTECTED_EFFECT, "C0"),
        ("semantic.process", OperationClass.BUSINESS, "C0"),
        ("message.send", OperationClass.BUSINESS, "C1"),
    ],
)
def test_local_allowance_never_expands_to_data_effect_semantic_or_non_c0(
    store,
    actor,
    now,
    future,
    action,
    operation_class,
    classification,
):
    actor = as_lab_actor(store, actor)
    engine = LocalConformancePolicyEngine(
        store,
        attenuation_policy=AttenuationPolicy(minimum_binding_assurance="hardware_bound"),
    )
    engine.bootstrap_entitlement_for_local_conformance(
        entitlement(actor, future, action=action),
        when=now,
    )
    decision = engine.decide(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource="room:alpha",
            operation_class=operation_class,
            classification=classification,
            policy_revision=1,
        ),
        when=now,
    )

    assert decision.allowed is False
    assert decision.reason == "binding_assurance_below_policy_floor"
