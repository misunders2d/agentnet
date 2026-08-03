from __future__ import annotations

from pathlib import Path
from uuid import uuid4

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.policy import LocalConformancePolicyEngine, PolicyEngine
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.messaging.events import new_event
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.protocol.models import Classification, EventType


def test_synthetic_identity_is_deterministic_only_and_lane_requires_marked_c0(tmp_path: Path) -> None:
    config = ExtensionConfig(
        domain_id="synthetic.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(harness_kind="codex", display_name="sender")
        recipient, _ = core.bootstrap_synthetic_identity(harness_kind="pi", display_name="recipient")
        row = core.store.fetch_one("SELECT status FROM harnesses WHERE harness_id=?", (sender.harness_id,))
        assert row["status"] == "deterministic_only"
        with pytest.raises(AuthorizationError):
            core.send_synthetic_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload={"text": "unmarked"},
                idempotency_key=f"synthetic-{uuid4()}",
            )
        accepted = core.send_synthetic_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"synthetic": True, "text": "marked"},
            idempotency_key=f"synthetic-{uuid4()}",
        )
        assert accepted["fact"] == "accepted_local"
        event = core.mailboxes.reconcile(recipient.harness_id)[0]["event"]
        assert event["actor"]["kind"] == "workload"
        assert event["actor"]["binding_assurance"] == "synthetic_lab"
        assert event["actor"]["binding_assurance"] != "workload_mtls"
        assert "principal_id" not in event["actor"]
    finally:
        core.close()


def test_local_lab_harness_uses_signed_c0_policy_without_becoming_active(tmp_path: Path) -> None:
    config = ExtensionConfig(
        domain_id="signed-synthetic.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(harness_kind="codex", display_name="sender")
        recipient, _ = core.bootstrap_synthetic_identity(harness_kind="pi", display_name="recipient")
        core.grant_local_entitlement(sender, action="message.send")
        core.grant_local_entitlement(recipient, action="mailbox.read", resource=recipient.harness_id)
        core.grant_local_entitlement(
            recipient,
            action="mailbox.acknowledge",
            resource=recipient.harness_id,
        )

        accepted = core.send_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"synthetic": True, "text": "signed local C0"},
            idempotency_key=f"signed-synthetic-{uuid4()}",
            classification=Classification.C0_PUBLIC,
        )
        assert accepted["fact"] == "accepted_local"
        item = core.mailbox(actor=recipient)[0]
        assert item["event"]["actor"] == sender.model_dump(mode="json")
        acknowledged = core.acknowledge_mailbox(
            actor=recipient,
            event_id=accepted["event_id"],
            envelope_digest=accepted["envelope_digest"],
        )
        assert acknowledged["fact"] == "recipient_committed"
        row = core.store.fetch_one(
            "SELECT status FROM harnesses WHERE harness_id=?",
            (sender.harness_id,),
        )
        assert row["status"] == "deterministic_only"
        with pytest.raises(AuthorizationError, match="harness_not_active"):
            PolicyEngine(
                core.store,
                runtime_profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            ).current_policy_revision(sender)
        non_lab_sender = sender.model_copy(update={"binding_assurance": "os_bound"})
        with pytest.raises(AuthorizationError, match="harness_not_active"):
            LocalConformancePolicyEngine(core.store).current_policy_revision(non_lab_sender)

        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload={"synthetic": True, "text": "non-C0 remains denied"},
                idempotency_key=f"signed-synthetic-{uuid4()}",
                classification=Classification.C1_INTERNAL,
            )

        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id=?",
                (recipient.harness_id,),
            )
        with pytest.raises(AuthorizationError, match="recipient is not a current enrolled address"):
            core.send_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload={"synthetic": True, "text": "non-lab deterministic recipient denied"},
                idempotency_key=f"signed-synthetic-{uuid4()}",
                classification=Classification.C0_PUBLIC,
            )
    finally:
        core.close()


def test_workload_event_causal_parent_binding_is_exact_and_synthetic_roots_have_none() -> None:
    synthetic = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="synthetic.example",
        workload_id="synthetic-lab-causal-root",
        binding_assurance="synthetic_lab",
    )
    with pytest.raises(PydanticValidationError, match="synthetic lab root"):
        new_event(
            domain_id=synthetic.domain_id,
            actor=synthetic,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload={"synthetic": True},
            idempotency_key=f"synthetic-causal-{uuid4()}",
            recipients=("synthetic-recipient",),
            causal_parent_ids=("forged-parent",),
        )

    exact_parent = "event-exact-parent"
    workload = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="workload.example",
        workload_id="registered-worker",
        workload_registration_id="registration-1",
        workload_role="mailbox_dispatcher",
        workload_process_id=123,
        workload_process_start_time=456,
        workload_session_id="session-1",
        workload_revocation_epoch=1,
        parent_event_id=exact_parent,
        task_grant_id="grant-1",
        credential_id="registration-1",
        credential_epoch=1,
        binding_assurance="workload_mtls",
    )
    accepted = new_event(
        domain_id=workload.domain_id,
        actor=workload,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"result": "bounded"},
        idempotency_key=f"workload-causal-{uuid4()}",
        recipients=("workload-recipient",),
        causal_parent_ids=(exact_parent,),
    )
    assert accepted.causal_parent_ids == (exact_parent,)

    for wrong_parents in ((), ("event-other-parent",)):
        with pytest.raises(PydanticValidationError, match="exact transport parent"):
            new_event(
                domain_id=workload.domain_id,
                actor=workload,
                event_type=EventType.MESSAGE,
                classification=Classification.C1_INTERNAL,
                payload={"result": "not-bound"},
                idempotency_key=f"workload-causal-{uuid4()}",
                recipients=("workload-recipient",),
                causal_parent_ids=wrong_parents,
            )
