from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest
from pydantic import ValidationError

from agentnet.attention.policy import AttentionService
from agentnet.authorization.policy import HumanEntitlement
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, GateBlocked
from agentnet.operations.config import ExtensionConfig
from agentnet.operations.outage import OperationalHealth, OutageGate
from agentnet.operations.policy_defaults import (
    AttentionPolicy,
    ConfidentialityPolicy,
    OperationsPolicy,
    OutagePolicy,
    RevocationPolicy,
    SecurePolicyDefaults,
)
from agentnet.privacy.classes import ConfidentialityEnforcer
from agentnet.protocol.models import Classification


def _core(store, tmp_path: Path, *, recipient_limit: int = 1, retention_days: int = 7) -> CommunicationCore:
    config = ExtensionConfig(
        domain_id="corp.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        policies=SecurePolicyDefaults(
            operations={
                "per_message_recipient_limit": recipient_limit,
                "retention_days": retention_days,
            }
        ),
    )
    return CommunicationCore(config, store)


def test_runtime_enforces_policy_recipient_limit_and_retention(store, identity_factory, tmp_path: Path) -> None:
    sender, _ = identity_factory()
    first, _ = identity_factory()
    second, _ = identity_factory()
    core = _core(store, tmp_path)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="*",
            revision=1,
        )
    )
    with pytest.raises(AuthorizationError, match="fanout"):
        core.send_message(
            actor=sender,
            recipients=(first.harness_id, second.harness_id),
            payload={"text": "too many recipients"},
            idempotency_key=f"policy-fanout-{uuid4()}",
            classification=Classification.C0_PUBLIC,
        )
    result = core.send_message(
        actor=sender,
        recipients=(first.harness_id,),
        payload={"text": "bounded"},
        idempotency_key=f"policy-retention-{uuid4()}",
        classification=Classification.C0_PUBLIC,
    )
    row = store.fetch_one(
        "SELECT created_at,retention_delete_at FROM events WHERE event_id=?",
        (result["event_id"],),
    )
    assert 7 * 86_400 - 1 <= row["retention_delete_at"] - row["created_at"] <= 7 * 86_400 + 1


def test_capability_configuration_never_grants_caller_authority_or_direct_c3(store, identity_factory, tmp_path: Path) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    core = _core(store, tmp_path)
    with pytest.raises(AuthorizationError):
        core.send_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"text": "no entitlement"},
            idempotency_key=f"policy-no-grant-{uuid4()}",
            classification=Classification.C0_PUBLIC,
        )
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="*",
            revision=1,
        )
    )
    with pytest.raises(AuthorizationError):
        core.send_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"text": "direct sealed data forbidden"},
            classification=Classification.C3_SEALED,
            idempotency_key=f"policy-c3-{uuid4()}",
        )


def test_local_lab_lane_is_exactly_c0_and_does_not_open_c1(store, identity_factory, tmp_path: Path) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    core = _core(store, tmp_path)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="*",
            revision=1,
        )
    )
    core.send_message(
        actor=sender,
        recipients=(recipient.harness_id,),
        payload={"synthetic": True},
        classification=Classification.C0_PUBLIC,
        idempotency_key=f"local-c0-{uuid4()}",
    )
    with pytest.raises(AuthorizationError, match="binding_assurance"):
        core.send_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"not": "an inert class"},
            classification=Classification.C1_INTERNAL,
            idempotency_key=f"local-c1-{uuid4()}",
        )


def test_core_event_snapshots_current_policy_revision_not_revision_one(
    store, identity_factory, tmp_path: Path
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    core = _core(store, tmp_path)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET policy_revision=2 WHERE domain_id=?",
            (sender.domain_id,),
        )
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="direct",
            revision=2,
        )
    )

    result = core.send_message(
        actor=sender,
        recipients=(recipient.harness_id,),
        payload={"revision": 2},
        classification=Classification.C0_PUBLIC,
        idempotency_key=f"current-policy-{uuid4()}",
    )

    assert store.fetch_one(
        "SELECT policy_revision FROM events WHERE event_id=?",
        (result["event_id"],),
    )["policy_revision"] == 2


def test_outage_policy_changes_issuance_privileged_and_bounded_continuity() -> None:
    now = datetime(2026, 7, 12, 12, 0, tzinfo=UTC)
    health = OperationalHealth(
        revocation_current=False,
        policy_current=False,
        audit_backlog_records=0,
        last_confirmed_current_at=now - timedelta(seconds=30),
    )
    permissive_within_safe_bound = OutageGate(
        OutagePolicy(low_risk_continuity_max_seconds=60),
        health_provider=lambda: health,
        clock=lambda: now,
    )
    permissive_within_safe_bound.require_low_risk_continuity()
    with pytest.raises(GateBlocked, match="issuance"):
        permissive_within_safe_bound.require_issuance()
    with pytest.raises(GateBlocked, match="held"):
        permissive_within_safe_bound.require_privileged()

    stricter = OutageGate(
        OutagePolicy(low_risk_continuity_max_seconds=0),
        health_provider=lambda: health,
        clock=lambda: now,
    )
    with pytest.raises(GateBlocked, match="expired"):
        stricter.require_low_risk_continuity()


def test_confidentiality_and_attention_stricter_settings_change_runtime_behavior() -> None:
    confidentiality = ConfidentialityEnforcer(
        ConfidentialityPolicy(c1_processing="isolated_service")
    )
    assert confidentiality.processing_profile(Classification.C1_INTERNAL) == "isolated_service"
    assert confidentiality.permits(Classification.C1_INTERNAL, "managed_search") is False
    assert confidentiality.permits(Classification.C1_INTERNAL, "isolated_scanner") is True
    with pytest.raises(AuthorizationError, match="not an allowed C3"):
        confidentiality.require_processing(Classification.C3_SEALED, "scanner")

    attention = AttentionService(
        AttentionPolicy(exception_types=("confirmed_security_incident",))
    )
    assert attention.exceptional_notice(
        "high_risk_elevation_expiring",
        opaque_reference="elevation:" + "a" * 32,
        when=datetime(2026, 7, 12, 12, 0, tzinfo=UTC),
    ) is None
    notice = attention.exceptional_notice(
        "confirmed_security_incident",
        opaque_reference="incident:" + "b" * 32,
        when=datetime(2026, 7, 12, 23, 0, tzinfo=UTC),
    )
    assert notice == {
        "type": "confirmed_security_incident",
        "reference": "incident:" + "b" * 32,
        "content": "redacted",
        "delivery": "deferred_quiet_hours",
    }


def test_policy_hard_floors_reject_false_durability_multiregion_and_retention_weakening() -> None:
    with pytest.raises(ValidationError):
        OperationsPolicy(accepted_durable_enabled=True)
    with pytest.raises(ValidationError):
        OperationsPolicy(regions=2)
    with pytest.raises(ValidationError, match="post-revocation"):
        SecurePolicyDefaults(
            revocation=RevocationPolicy(accepted_history_max_retention_days=7),
            operations=OperationsPolicy(retention_days=30),
        )
    configured = SecurePolicyDefaults(
        revocation=RevocationPolicy(accepted_history_max_retention_days=7),
        operations=OperationsPolicy(retention_days=7),
    )
    assert configured.operations.retention_days == 7


def test_core_injects_typed_policy_objects_into_runtime_decision_services(store, tmp_path: Path) -> None:
    core = _core(store, tmp_path, recipient_limit=3, retention_days=5)
    policies = core.config.policies
    assert core.policy.attenuation_policy == policies.attenuation
    assert core.assignments.attenuation_policy == policies.attenuation
    assert core.rooms.governance_policy == policies.rooms
    assert core.room_governance.policy == policies.rooms
    assert core.federation.assurance_policy == policies.federation
    assert core.quotas.policy == policies.operations
    assert core.outage.policy == policies.outage
    assert core.attention.policy == policies.attention
    assert core.confidentiality.policy == policies.confidentiality
    assert core.mailboxes.revocation_policy == policies.revocation
