from __future__ import annotations

import json
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest

from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import HumanEntitlement
from agentnet.core.app import CommunicationCore
from agentnet.effects.reservations import (
    EffectExecutionEvidence,
    EffectReconciliationEvidence,
    EffectState,
    EffectTerminalEvidence,
    EffectTransitionProof,
    EffectUncertaintyEvidence,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.messaging.events import new_event
from agentnet.operations.config import ExtensionConfig, FeatureFlags
from agentnet.protocol.models import Classification, EventType, TaskGrant
from agentnet.provenance import ProvenanceObjectType, TransformationKind
from agentnet.security.signatures import canonical_digest


class InjectedCrash(RuntimeError):
    pass


def prepare_effect(store, identity_factory, tmp_path: Path):
    # Contract-level OS binding exercises the protected-effect path.  This is
    # not transport/platform evidence and is never cited as such.
    actor, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory()
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=actor.domain_id,
            data_dir=tmp_path / "data",
            artifact_dir=tmp_path / "artifacts",
            features=FeatureFlags(protected_effects=True),
        ),
        store,
    )
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="tool.invoke",
            resource_pattern="record:1",
            revision=1,
        )
    )
    event = new_event(
        domain_id=actor.domain_id,
        actor=actor,
        event_type=EventType.TASK_ASSIGNMENT,
        classification=Classification.C2_RESTRICTED,
        payload={"task": "synthetic"},
        idempotency_key=f"effect-parent-{uuid4()}",
        recipients=(recipient.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=30),
    )
    core.mailboxes.accept(event)
    grant = TaskGrant(
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        harness_id=actor.harness_id,
        actions=frozenset({"tool.invoke"}),
        resources=frozenset({"record:1"}),
        input_sources=frozenset({"event"}),
        output_sinks=frozenset({"tool:synthetic"}),
        data_classes=frozenset({Classification.C2_RESTRICTED}),
        max_uses=1,
        expires_at=datetime.now(UTC) + timedelta(minutes=5),
    )
    issuance_resource, _issuance_context = core.grants.issuance_binding(grant)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="authorization.task_grant.issue",
            resource_pattern=issuance_resource,
            revision=1,
        )
    )
    core.issue_task_grant(actor=actor, grant=grant)
    use = GrantUse(
        grant_id=grant.grant_id,
        action="tool.invoke",
        resource="record:1",
        input_source="event",
        output_sink="tool:synthetic",
        data_class=Classification.C2_RESTRICTED,
    )
    return core, actor, event, grant, use


def execution_authority(workload_factory, *, actor, event, grant):
    return workload_factory(
        domain=actor.domain_id,
        role="effect_authority",
        recipient_scope=actor.harness_id,
        parent_event_id=event.event_id,
        task_grant_id=grant.grant_id,
    )


def start_effect(core, executor, signer, prepared, event, grant, *, request: dict[str, object]):
    now = int(time.time())
    evidence = EffectExecutionEvidence(
        attempt_id=f"effect-attempt-{uuid4()}",
        executor_instance_id=f"effect-executor-{uuid4()}",
        request_digest=canonical_digest(request),
        dispatched_at=now,
    )
    proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.PREPARED,
        to_state=EffectState.EXECUTING,
        evidence=evidence,
    )
    result = core.effects.start_execution(
        prepared["effect_id"],
        actor=executor,
        proof=proof,
        evidence=evidence,
    )
    return result, evidence


def test_exact_grant_consumed_once_and_unknown_is_never_blind_retried(
    store, identity_factory, workload_factory, tmp_path: Path
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"value": 1}
    prepared = core.reserve_effect(actor=actor, event_id=event.event_id, grant_use=use, request=request)
    assert prepared["state"] == "effect_prepared"
    assert core.grants.uses_for_local_conformance(grant.grant_id) == 1

    duplicate = core.reserve_effect(actor=actor, event_id=event.event_id, grant_use=use, request={"value": 1})
    assert duplicate["duplicate"] is True
    assert duplicate["effect_id"] == prepared["effect_id"]
    assert core.grants.uses_for_local_conformance(grant.grant_id) == 1

    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    _executing, execution = start_effect(
        core,
        executor,
        signer,
        prepared,
        event,
        grant,
        request=request,
    )
    uncertain = EffectUncertaintyEvidence(
        attempt_id=execution.attempt_id,
        reason="timeout",
        observation_digest=canonical_digest({"transport": "synthetic-timeout"}),
        observed_at=int(time.time()),
    )
    unknown_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.UNKNOWN,
        evidence=uncertain,
    )
    core.effects.mark_unknown(
        prepared["effect_id"],
        actor=executor,
        proof=unknown_proof,
        evidence=uncertain,
    )
    with pytest.raises(ConflictError):
        core.effects.retry(prepared["effect_id"])


@pytest.mark.parametrize(
    "terminal_state",
    [EffectState.SUCCEEDED, EffectState.FAILED, EffectState.CANCELLED],
)
def test_registered_executor_moves_prepared_through_exact_idempotent_terminal_state(
    store,
    identity_factory,
    workload_factory,
    tmp_path: Path,
    terminal_state: EffectState,
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"operation": "terminal-path", "state": terminal_state.value}
    prepared = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )
    assert store.fetch_one(
        """SELECT state FROM operational_work_reservations
             WHERE work_kind='protected_effect' AND source_id=?""",
        (prepared["effect_id"],),
    )["state"] == "pending"
    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    executing, execution = start_effect(
        core,
        executor,
        signer,
        prepared,
        event,
        grant,
        request=request,
    )
    assert executing["state"] == EffectState.EXECUTING.value
    assert core.effects.status(prepared["effect_id"], actor=actor)["state"] == EffectState.EXECUTING.value

    terminal = EffectTerminalEvidence(
        attempt_id=execution.attempt_id,
        external_receipt_id=f"receipt-{uuid4()}",
        external_receipt_digest=canonical_digest({"receipt": terminal_state.value}),
        observed_at=int(time.time()),
    )
    terminal_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=terminal_state,
        evidence=terminal,
    )
    completed = core.effects.acknowledge_terminal(
        prepared["effect_id"],
        actor=executor,
        proof=terminal_proof,
        terminal_state=terminal_state,
        evidence=terminal,
    )
    assert completed["state"] == terminal_state.value
    assert completed["duplicate"] is False
    tool_output = core.provenance.get_by_digest(
        completed["provenance"]["provenance_digest"]
    )
    assert tool_output.object_type is ProvenanceObjectType.TOOL_OUTPUT
    assert tool_output.content_digest == terminal.external_receipt_digest
    assert tool_output.transformations.steps[-1].kind is TransformationKind.TOOL
    assert tool_output.tainted is True
    assert store.fetch_one(
        """SELECT state FROM operational_work_reservations
             WHERE work_kind='protected_effect' AND source_id=?""",
        (prepared["effect_id"],),
    )["state"] == "terminal"

    retry_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=terminal_state,
        evidence=terminal,
    )
    duplicate = core.effects.acknowledge_terminal(
        prepared["effect_id"],
        actor=executor,
        proof=retry_proof,
        terminal_state=terminal_state,
        evidence=terminal,
    )
    assert duplicate["duplicate"] is True
    assert duplicate["provenance"] == completed["provenance"]
    assert core.effects.status(prepared["effect_id"], actor=actor)["state"] == terminal_state.value
    lifecycle = store.fetch_one(
        "SELECT * FROM effect_lifecycle WHERE effect_id=?",
        (prepared["effect_id"],),
    )
    assert lifecycle["fence"] == prepared["fence"]
    assert lifecycle["executor_registration_id"] == executor.workload_registration_id
    assert lifecycle["terminal_source"] == "executor_ack"
    records = [json.loads(row["record_json"]) for row in store.fetch_all("SELECT record_json FROM audit_log")]
    actions = [record.get("action") for record in records]
    assert actions.count("effect.executing") == 1
    assert actions.count(f"effect.{terminal_state.value.removeprefix('effect_')}") == 1
    assert store.verify_audit_chain()[0] is True


def test_fence_executor_and_terminal_evidence_are_exact_and_fail_closed(
    store, identity_factory, workload_factory, tmp_path: Path
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"operation": "exact-fence"}
    prepared = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )
    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    now = int(time.time())
    execution = EffectExecutionEvidence(
        attempt_id=f"effect-attempt-{uuid4()}",
        executor_instance_id=f"effect-executor-{uuid4()}",
        request_digest=canonical_digest(request),
        dispatched_at=now,
    )
    wrong_fence = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"] + 1,
        from_state=EffectState.PREPARED,
        to_state=EffectState.EXECUTING,
        evidence=execution,
    )
    with pytest.raises(AuthenticationError, match="binding"):
        core.effects.start_execution(
            prepared["effect_id"],
            actor=executor,
            proof=wrong_fence,
            evidence=execution,
        )

    valid_start = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.PREPARED,
        to_state=EffectState.EXECUTING,
        evidence=execution,
    )
    core.effects.start_execution(
        prepared["effect_id"],
        actor=executor,
        proof=valid_start,
        evidence=execution,
    )
    sibling, sibling_signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    terminal = EffectTerminalEvidence(
        attempt_id=execution.attempt_id,
        external_receipt_id=f"receipt-{uuid4()}",
        external_receipt_digest=canonical_digest({"receipt": "succeeded"}),
        observed_at=int(time.time()),
    )
    sibling_proof = EffectTransitionProof.create(
        sibling_signer,
        actor=sibling,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.SUCCEEDED,
        evidence=terminal,
    )
    with pytest.raises(ConflictError, match="different durable"):
        core.effects.acknowledge_terminal(
            prepared["effect_id"],
            actor=sibling,
            proof=sibling_proof,
            terminal_state=EffectState.SUCCEEDED,
            evidence=terminal,
        )

    with store.transaction() as connection:
        connection.execute(
            "UPDATE workload_registrations SET status='revoked' WHERE registration_id=?",
            (executor.workload_registration_id,),
        )
    terminal_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.SUCCEEDED,
        evidence=terminal,
    )
    with pytest.raises(AuthorizationError, match="not current"):
        core.effects.acknowledge_terminal(
            prepared["effect_id"],
            actor=executor,
            proof=terminal_proof,
            terminal_state=EffectState.SUCCEEDED,
            evidence=terminal,
        )


def test_terminal_tool_output_and_state_roll_back_when_parent_provenance_is_missing(
    store,
    identity_factory,
    workload_factory,
    tmp_path: Path,
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"operation": "missing-parent-provenance"}
    prepared = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )
    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    _executing, execution = start_effect(
        core,
        executor,
        signer,
        prepared,
        event,
        grant,
        request=request,
    )
    terminal = EffectTerminalEvidence(
        attempt_id=execution.attempt_id,
        external_receipt_id=f"receipt-{uuid4()}",
        external_receipt_digest=canonical_digest({"receipt": "committed"}),
        observed_at=int(time.time()),
    )
    proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.SUCCEEDED,
        evidence=terminal,
    )
    with store.transaction() as connection:
        connection.execute(
            "DELETE FROM event_provenance WHERE event_id=?",
            (event.event_id,),
        )
    with pytest.raises(AuthorizationError, match="mandatory event provenance"):
        core.effects.acknowledge_terminal(
            prepared["effect_id"],
            actor=executor,
            proof=proof,
            terminal_state=EffectState.SUCCEEDED,
            evidence=terminal,
        )
    assert store.fetch_one(
        "SELECT state FROM effect_reservations WHERE effect_id=?",
        (prepared["effect_id"],),
    )["state"] == EffectState.EXECUTING.value
    assert store.fetch_one(
        """SELECT COUNT(*) AS total FROM content_provenance
             WHERE object_type='tool_output' AND object_id=?""",
        (f"effect-output:{prepared['effect_id']}",),
    )["total"] == 0

def test_effect_unknown_requires_signed_reconciliation_and_never_accepts_blind_terminal_ack(
    store, identity_factory, workload_factory, tmp_path: Path
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"operation": "uncertain-commit"}
    prepared = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )
    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    _executing, execution = start_effect(
        core,
        executor,
        signer,
        prepared,
        event,
        grant,
        request=request,
    )
    unknown = EffectUncertaintyEvidence(
        attempt_id=execution.attempt_id,
        reason="commit_response_lost",
        observation_digest=canonical_digest({"observation": "connection-closed-after-write"}),
        observed_at=int(time.time()),
    )
    unknown_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.UNKNOWN,
        evidence=unknown,
    )
    transition = core.effects.mark_unknown(
        prepared["effect_id"],
        actor=executor,
        proof=unknown_proof,
        evidence=unknown,
    )
    assert transition["state"] == EffectState.UNKNOWN.value
    assert store.fetch_one(
        """SELECT COUNT(*) AS total FROM content_provenance
             WHERE object_type='tool_output' AND object_id=?""",
        (f"effect-output:{prepared['effect_id']}",),
    )["total"] == 0

    blind_terminal = EffectTerminalEvidence(
        attempt_id=execution.attempt_id,
        external_receipt_id=f"receipt-{uuid4()}",
        external_receipt_digest=canonical_digest({"claim": "success-without-readback"}),
        observed_at=int(time.time()),
    )
    blind_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.EXECUTING,
        to_state=EffectState.SUCCEEDED,
        evidence=blind_terminal,
    )
    with pytest.raises(ConflictError, match="reconcile explicitly"):
        core.effects.acknowledge_terminal(
            prepared["effect_id"],
            actor=executor,
            proof=blind_proof,
            terminal_state=EffectState.SUCCEEDED,
            evidence=blind_terminal,
        )

    reconciliation = EffectReconciliationEvidence(
        attempt_id=execution.attempt_id,
        authority_system_id="synthetic-system-of-record",
        query_id=f"query-{uuid4()}",
        query_response_digest=canonical_digest({"authoritative_state": "committed"}),
        observed_at=int(time.time()),
        terminal_state=EffectState.SUCCEEDED,
    )
    executor_reconcile_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.UNKNOWN,
        to_state=EffectState.SUCCEEDED,
        evidence=reconciliation,
    )
    with pytest.raises(AuthorizationError, match="independent"):
        core.effects.reconcile(
            prepared["effect_id"],
            actor=executor,
            proof=executor_reconcile_proof,
            evidence=reconciliation,
        )
    reconciler, reconciler_signer = workload_factory(
        domain=actor.domain_id,
        role="effect_reconciler",
        workload_id=f"effect-system:{reconciliation.authority_system_id}",
        recipient_scope=actor.harness_id,
        parent_event_id=event.event_id,
        task_grant_id=grant.grant_id,
    )
    reconcile_proof = EffectTransitionProof.create(
        reconciler_signer,
        actor=reconciler,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.UNKNOWN,
        to_state=EffectState.SUCCEEDED,
        evidence=reconciliation,
    )
    reconciled = core.effects.reconcile(
        prepared["effect_id"],
        actor=reconciler,
        proof=reconcile_proof,
        evidence=reconciliation,
    )
    assert reconciled["state"] == EffectState.SUCCEEDED.value
    assert reconciled["duplicate"] is False
    tool_output = core.provenance.get_by_digest(
        reconciled["provenance"]["provenance_digest"]
    )
    assert tool_output.content_digest == reconciliation.query_response_digest
    assert tool_output.transformations.steps[-1].kind is TransformationKind.TOOL

    duplicate_proof = EffectTransitionProof.create(
        reconciler_signer,
        actor=reconciler,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.UNKNOWN,
        to_state=EffectState.SUCCEEDED,
        evidence=reconciliation,
    )
    duplicate = core.effects.reconcile(
        prepared["effect_id"],
        actor=reconciler,
        proof=duplicate_proof,
        evidence=reconciliation,
    )
    assert duplicate["duplicate"] is True
    assert duplicate["provenance"] == reconciled["provenance"]
    lifecycle = store.fetch_one(
        "SELECT * FROM effect_lifecycle WHERE effect_id=?",
        (prepared["effect_id"],),
    )
    reconciliation_digest = canonical_digest(reconciliation.model_dump(mode="json"))
    assert lifecycle["terminal_source"] == "reconciliation"
    assert lifecycle["uncertainty_evidence_digest"] == canonical_digest(unknown.model_dump(mode="json"))
    assert lifecycle["reconciliation_evidence_digest"] == reconciliation_digest
    assert lifecycle["terminal_evidence_digest"] == reconciliation_digest

    conflicting = reconciliation.model_copy(
        update={
            "query_id": f"query-{uuid4()}",
            "query_response_digest": canonical_digest({"authoritative_state": "failed"}),
            "terminal_state": EffectState.FAILED,
        }
    )
    conflict_proof = EffectTransitionProof.create(
        reconciler_signer,
        actor=reconciler,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.UNKNOWN,
        to_state=EffectState.FAILED,
        evidence=conflicting,
    )
    with pytest.raises(ConflictError, match="only effect_unknown"):
        core.effects.reconcile(
            prepared["effect_id"],
            actor=reconciler,
            proof=conflict_proof,
            evidence=conflicting,
        )


@pytest.mark.parametrize(
    "crash_phase",
    ["after_grant_consumed", "after_decision_recorded", "after_reservation_inserted", "before_commit"],
)
def test_effect_transaction_rolls_back_every_crash_boundary(
    store,
    identity_factory,
    tmp_path: Path,
    crash_phase: str,
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    decisions_before = store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"]
    audit_before = store.fetch_one("SELECT COUNT(*) AS count FROM audit_log")["count"]

    def crash(current: str) -> None:
        if current == crash_phase:
            raise InjectedCrash(current)

    with pytest.raises(InjectedCrash, match=crash_phase):
        core.effects.reserve(
            policy=core.policy,
            actor=actor,
            event_id=event.event_id,
            grant_use=use,
            request={"value": 1},
            phase_hook=crash,
        )

    assert core.grants.uses_for_local_conformance(grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM effect_reservations")["count"] == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM operational_work_reservations WHERE work_kind='protected_effect'"
    )["count"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == decisions_before
    assert store.fetch_one("SELECT COUNT(*) AS count FROM audit_log")["count"] == audit_before
    assert store.verify_audit_chain()[0] is True

    recovered = core.reserve_effect(actor=actor, event_id=event.event_id, grant_use=use, request={"value": 1})
    assert recovered["state"] == "effect_prepared"
    assert core.grants.uses_for_local_conformance(grant.grant_id) == 1


def test_effect_crash_rolls_back_half_open_probe_and_next_probe_closes_it(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core, actor, event, _grant, use = prepare_effect(store, identity_factory, tmp_path)
    clock = [130_000]
    core.quotas.clock = lambda: clock[0]
    for _ in range(core.config.policies.operations.circuit_breaker_failure_threshold):
        core.quotas.record_failure(operation="effect_reserve", domain_scope=actor.domain_id)
    clock[0] += core.config.policies.operations.circuit_breaker_reset_seconds + 1

    def crash(phase: str) -> None:
        if phase == "after_reservation_inserted":
            raise InjectedCrash(phase)

    with pytest.raises(InjectedCrash):
        core.effects.reserve(
            policy=core.policy,
            actor=actor,
            event_id=event.event_id,
            grant_use=use,
            request={"value": 1},
            phase_hook=crash,
        )
    breaker = store.fetch_one("SELECT state FROM circuit_breakers WHERE state<>'closed'")
    assert breaker["state"] == "open"
    recovered = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request={"value": 1},
    )
    assert recovered["state"] == "effect_prepared"
    assert core.quotas.content_free_status()["open_breakers"] == 0


def test_effect_duplicates_do_not_close_open_breakers(
    store,
    identity_factory,
    workload_factory,
    tmp_path: Path,
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    request = {"operation": "duplicate-breaker-neutral"}
    prepared = core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )
    for _ in range(core.config.policies.operations.circuit_breaker_failure_threshold):
        core.quotas.record_failure(operation="effect_reserve", domain_scope=actor.domain_id)
    assert core.reserve_effect(
        actor=actor,
        event_id=event.event_id,
        grant_use=use,
        request=request,
    )["duplicate"] is True
    assert core.quotas.content_free_status()["open_breakers"] == 1
    core.quotas.record_success(operation="effect_reserve", domain_scope=actor.domain_id)

    executor, signer = execution_authority(
        workload_factory,
        actor=actor,
        event=event,
        grant=grant,
    )
    now = int(time.time())
    evidence = EffectExecutionEvidence(
        attempt_id=f"effect-attempt-{uuid4()}",
        executor_instance_id=f"effect-executor-{uuid4()}",
        request_digest=canonical_digest(request),
        dispatched_at=now,
    )
    proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.PREPARED,
        to_state=EffectState.EXECUTING,
        evidence=evidence,
    )
    core.start_effect_execution(
        actor=executor,
        effect_id=prepared["effect_id"],
        proof=proof,
        evidence=evidence,
    )
    for _ in range(core.config.policies.operations.circuit_breaker_failure_threshold):
        core.quotas.record_failure(operation="effect_execute", domain_scope=actor.domain_id)
    duplicate_proof = EffectTransitionProof.create(
        signer,
        actor=executor,
        effect_id=prepared["effect_id"],
        fence=prepared["fence"],
        from_state=EffectState.PREPARED,
        to_state=EffectState.EXECUTING,
        evidence=evidence,
    )
    assert core.start_effect_execution(
        actor=executor,
        effect_id=prepared["effect_id"],
        proof=duplicate_proof,
        evidence=evidence,
    )["duplicate"] is True
    assert core.quotas.content_free_status()["open_breakers"] == 1


def test_effect_rejects_parent_event_after_domain_policy_revision_changes(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core, actor, event, grant, use = prepare_effect(store, identity_factory, tmp_path)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id=?",
            (actor.domain_id,),
        )

    from agentnet.errors import AuthorizationError

    with pytest.raises(AuthorizationError, match="stale_policy_revision"):
        core.reserve_effect(actor=actor, event_id=event.event_id, grant_use=use, request={"value": 1})
    assert core.grants.uses_for_local_conformance(grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM effect_reservations")["count"] == 0
