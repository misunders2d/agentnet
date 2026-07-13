from __future__ import annotations

from datetime import UTC, datetime
from uuid import uuid4

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.policy import HumanEntitlement, LocalConformancePolicyEngine, PolicyEngine
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.conversation import ConversationService, build_conversation_event
from agentnet.messaging.events import new_event
from agentnet.protocol.models import (
    Classification,
    EventType,
    ReleasedArtifactBinding,
)


class InjectedConversationCrash(RuntimeError):
    pass


def build(actor, recipient, action):
    return build_conversation_event(
        actor=actor,
        recipients=(recipient.harness_id,),
        conversation_id="conversation:synthetic",
        thread_id="thread:synthetic",
        action=action,
        idempotency_key=f"conversation-{uuid4()}",
        classification=Classification.C1_INTERNAL,
    )


def released_binding(actor, *, classification=Classification.C1_INTERNAL):
    return ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id=actor.domain_id,
        object_version="d" * 64,
        size=123,
        media_type="application/pdf",
        classification=classification,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )


def test_direct_and_conversation_events_bind_released_versions_outside_free_form_payload(
    identity_factory,
) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    binding = released_binding(actor)
    direct = new_event(
        domain_id=actor.domain_id,
        actor=actor,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"body": "see the typed attachment"},
        idempotency_key=f"direct-artifact-{uuid4()}",
        recipients=(recipient.harness_id,),
        released_artifacts=(binding,),
    )
    assert direct.released_artifacts == (binding,)
    assert "released_artifacts" not in direct.payload

    conversation = build(
        actor,
        recipient,
        {
            "kind": "post",
            "body": "see the typed attachment",
            "released_artifacts": [binding.model_dump(mode="json")],
        },
    )
    assert conversation.released_artifacts == (binding,)
    assert "released_artifacts" not in conversation.payload


def test_event_artifact_binding_rejects_classification_domain_duplicate_and_malformed_metadata(
    identity_factory,
) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    restricted = released_binding(actor, classification=Classification.C2_RESTRICTED)
    common = {
        "domain_id": actor.domain_id,
        "actor": actor,
        "event_type": EventType.MESSAGE,
        "classification": Classification.C1_INTERNAL,
        "payload": {"body": "synthetic"},
        "idempotency_key": f"direct-artifact-{uuid4()}",
        "recipients": (recipient.harness_id,),
    }
    with pytest.raises(PydanticValidationError, match="classification is lower"):
        new_event(**common, released_artifacts=(restricted,))

    binding = released_binding(actor)
    with pytest.raises(PydanticValidationError, match="unique"):
        new_event(**common, released_artifacts=(binding, binding))
    with pytest.raises(PydanticValidationError):
        ReleasedArtifactBinding.model_validate(
            binding.model_dump(mode="json") | {"media_type": "Text/Plain; charset=utf-8"}
        )
    with pytest.raises(PydanticValidationError):
        ReleasedArtifactBinding.model_validate(
            binding.model_dump(mode="json") | {"object_version": "not-a-digest"}
        )
    with pytest.raises(PydanticValidationError, match="trust domain"):
        new_event(
            **common,
            released_artifacts=(binding.model_copy(update={"domain_id": "other.example"}),),
        )


def test_reply_and_structured_request_have_exact_thread_and_parent_semantics(identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    reply = build(
        actor,
        recipient,
        {
            "kind": "reply",
            "reply_to_event_id": "event:parent",
            "body": "synthetic reply",
            "mentions": [recipient.harness_id],
        },
    )
    assert reply.event_type is EventType.MESSAGE
    assert reply.causal_parent_ids == ("event:parent",)
    assert reply.thread_id == "thread:synthetic"

    request = build(
        actor,
        recipient,
        {
            "kind": "structured_request",
            "request_type": "record.lookup",
            "arguments": {"synthetic_id": 1},
            "response_schema_id": "schema.result.v1",
        },
    )
    assert request.payload["kind"] == "structured_request"
    assert request.causal_parent_ids == ()


def test_handoff_cancellation_and_completion_are_inert_control_facts(identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    handoff = build(
        actor,
        recipient,
        {
            "kind": "handoff",
            "task_id": "task:synthetic",
            "source_event_id": "event:source",
            "from_harness_id": actor.harness_id,
            "to_harness_id": recipient.harness_id,
            "state_digest": "a" * 64,
        },
    )
    assert handoff.event_type is EventType.CONTROL
    assert handoff.causal_parent_ids == ("event:source",)

    cancel = build(
        actor,
        recipient,
        {
            "kind": "cancellation",
            "target_event_id": "event:source",
            "task_id": "task:synthetic",
            "reason_code": "owner.requested",
        },
    )
    assert cancel.payload["kind"] == "cancellation"
    assert "canceled" not in cancel.payload

    completion = build(
        actor,
        recipient,
        {
            "kind": "completion_ack",
            "target_event_id": "event:source",
            "task_id": "task:synthetic",
            "outcome": "effect_unknown",
        },
    )
    assert completion.payload["outcome"] == "effect_unknown"


def test_conversation_schema_rejects_spoofed_handoff_mentions_and_extras(identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    with pytest.raises(AuthorizationError):
        build(
            actor,
            recipient,
            {
                "kind": "handoff",
                "task_id": "task:synthetic",
                "source_event_id": "event:source",
                "from_harness_id": "spoofed-harness",
                "to_harness_id": recipient.harness_id,
                "state_digest": "a" * 64,
            },
        )
    with pytest.raises(ValidationError):
        build(
            actor,
            recipient,
            {"kind": "post", "body": "synthetic", "mentions": ["hidden-harness"]},
        )
    with pytest.raises(PydanticValidationError):
        build(
            actor,
            recipient,
            {"kind": "post", "body": "synthetic", "mentions": [], "authority": True},
        )


def grant_actions(policy: PolicyEngine, actor, conversation_id: str, actions: tuple[str, ...]) -> None:
    for action in actions:
        policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=f"conversation:{conversation_id}",
                revision=1,
            )
        )


def set_deterministic_only(store, *actors) -> None:
    with store.transaction() as connection:
        for actor in actors:
            connection.execute(
                "UPDATE harnesses SET status='deterministic_only' WHERE harness_id=?",
                (actor.harness_id,),
            )


def test_deterministic_conversation_actor_requires_explicit_local_policy_and_c0(
    store,
    identity_factory,
) -> None:
    creator, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    local = LocalConformancePolicyEngine(store)
    mailbox = MailboxService(store)
    for conversation_id in ("conversation:production-deny", "conversation:c1-deny", "conversation:local-c0"):
        grant_actions(local, creator, conversation_id, ("conversation.create",))
    set_deterministic_only(store, creator, recipient)

    with pytest.raises(AuthorizationError, match="synthetic_lab_harness_not_admitted"):
        ConversationService(store, PolicyEngine(store), mailbox).create(
            actor=creator,
            conversation_id="conversation:production-deny",
            member_harness_ids=(recipient.harness_id,),
            classification=Classification.C0_PUBLIC,
        )
    with pytest.raises(AuthorizationError, match="synthetic_lab_harness_not_admitted"):
        ConversationService(store, local, mailbox).create(
            actor=creator,
            conversation_id="conversation:c1-deny",
            member_harness_ids=(recipient.harness_id,),
            classification=Classification.C1_INTERNAL,
        )

    created = ConversationService(store, local, mailbox).create(
        actor=creator,
        conversation_id="conversation:local-c0",
        member_harness_ids=(recipient.harness_id,),
        classification=Classification.C0_PUBLIC,
    )
    assert created["duplicate"] is False


def test_production_conversation_rejects_deterministic_recipient_even_for_c0(
    store,
    identity_factory,
) -> None:
    creator, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    conversation_id = "conversation:deterministic-recipient"
    local = LocalConformancePolicyEngine(store)
    grant_actions(local, creator, conversation_id, ("conversation.create",))
    set_deterministic_only(store, recipient)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id=?",
            (creator.harness_id,),
        )
    creator = creator.model_copy(update={"binding_assurance": "os_bound"})

    with pytest.raises(AuthorizationError, match="recipient is not a current domain harness"):
        ConversationService(store, PolicyEngine(store), MailboxService(store)).create(
            actor=creator,
            conversation_id=conversation_id,
            member_harness_ids=(recipient.harness_id,),
            classification=Classification.C0_PUBLIC,
        )


def test_operational_conversation_task_handoff_cancel_completion_and_receipts(store, identity_factory) -> None:
    creator, _ = identity_factory()
    first_assignee, _ = identity_factory(kind="claude")
    second_assignee, _ = identity_factory(kind="pi")
    outsider, _ = identity_factory(kind="antigravity")
    conversation_id = "conversation:operational"
    policy = LocalConformancePolicyEngine(store)
    mailbox = MailboxService(store)
    service = ConversationService(store, policy, mailbox)
    grant_actions(
        policy,
        creator,
        conversation_id,
        (
            "conversation.create",
            "conversation.message.send",
            "conversation.task.request",
            "conversation.task.cancel_request",
        ),
    )
    grant_actions(
        policy,
        first_assignee,
        conversation_id,
        ("conversation.message.send", "conversation.task.handoff"),
    )
    grant_actions(
        policy,
        second_assignee,
        conversation_id,
        ("conversation.message.send", "conversation.task.complete"),
    )
    created = service.create(
        actor=creator,
        conversation_id=conversation_id,
        member_harness_ids=(first_assignee.harness_id, second_assignee.harness_id),
        classification=Classification.C0_PUBLIC,
    )
    assert created["duplicate"] is False

    root = service.post(
        actor=creator,
        recipients=(first_assignee.harness_id, second_assignee.harness_id),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={"kind": "post", "body": "begin", "mentions": [first_assignee.harness_id]},
        idempotency_key="conversation-operational-root",
    )
    reply = service.post(
        actor=first_assignee,
        recipients=(creator.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={"kind": "reply", "reply_to_event_id": root["event_id"], "body": "ack"},
        idempotency_key="conversation-operational-reply",
    )
    assert reply["fact"] == "accepted_local"

    task = service.post(
        actor=creator,
        recipients=(first_assignee.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={"kind": "task", "task_id": "task:work", "summary": "do exact work"},
        idempotency_key="conversation-operational-task",
    )
    duplicate_task = service.post(
        actor=creator,
        recipients=(first_assignee.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={"kind": "task", "task_id": "task:work", "summary": "do exact work"},
        idempotency_key="conversation-operational-task",
    )
    assert duplicate_task["duplicate"] is True
    assert duplicate_task["proposal_id"] == task["proposal_id"]
    assert duplicate_task["request_digest"] == task["request_digest"]
    assert all(
        item["event"]["event_type"] != "task_assignment"
        for item in mailbox.reconcile(first_assignee.harness_id)
    )
    task_approval = service.assignments.approve(
        actor=first_assignee,
        proposal_id=task["proposal_id"],
        expected_request_digest=task["request_digest"],
        expected_revision=task["proposal_revision"],
    )
    task = task | {"event_id": task_approval.resumed_event_id}

    handoff = service.post(
        actor=first_assignee,
        recipients=(second_assignee.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={
            "kind": "handoff",
            "task_id": "task:work",
            "source_event_id": task["event_id"],
            "from_harness_id": first_assignee.harness_id,
            "to_harness_id": second_assignee.harness_id,
            "state_digest": "a" * 64,
        },
        idempotency_key="conversation-operational-handoff",
    )
    assert all(
        item["event"].get("task_id") != "task:work"
        for item in mailbox.reconcile(second_assignee.harness_id)
    )
    handoff_approval = service.assignments.approve(
        actor=second_assignee,
        proposal_id=handoff["proposal_id"],
        expected_request_digest=handoff["request_digest"],
        expected_revision=handoff["proposal_revision"],
    )
    handoff = handoff | {"event_id": handoff_approval.resumed_event_id}
    with pytest.raises(AuthorizationError, match="assignee"):
        service.post(
            actor=first_assignee,
            recipients=(creator.harness_id,),
            conversation_id=conversation_id,
            thread_id="thread:work",
            action={
                "kind": "completion_ack",
                "target_event_id": handoff["event_id"],
                "task_id": "task:work",
                "outcome": "effect_unknown",
            },
            idempotency_key="conversation-old-assignee-complete",
        )

    cancel = service.post(
        actor=creator,
        recipients=(second_assignee.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={
            "kind": "cancellation",
            "target_event_id": handoff["event_id"],
            "task_id": "task:work",
            "reason_code": "owner.requested",
        },
        idempotency_key="conversation-operational-cancel",
    )
    completed = service.post(
        actor=second_assignee,
        recipients=(creator.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:work",
        action={
            "kind": "completion_ack",
            "target_event_id": cancel["event_id"],
            "task_id": "task:work",
            "outcome": "canceled",
        },
        idempotency_key="conversation-operational-completion",
    )
    assert completed["fact"] == "accepted_local"
    state = service.task_state(actor=creator, conversation_id=conversation_id, task_id="task:work")
    assert state["state"] == "canceled"
    assert state["assignee_harness_id"] == second_assignee.harness_id
    thread_items = service.thread(
        actor=second_assignee,
        conversation_id=conversation_id,
        thread_id="thread:work",
    )
    assert len(thread_items) == 6
    ordinary = [item for item in thread_items if item["event"]["event_type"] == "message"]
    task_controlled = [
        item
        for item in thread_items
        if item["event"]["event_type"] == "task_assignment"
        or (
            item["event"]["event_type"] == "control"
            and item["event"].get("task_id") is not None
        )
    ]
    assert ordinary and all(item["payload_available"] is True for item in ordinary)
    assert task_controlled
    assert all(item["payload"] is None for item in task_controlled)
    assert all(item["payload_access"] == "task_grant_required" for item in task_controlled)
    assert "do exact work" not in str(task_controlled)
    assert mailbox.reconcile(first_assignee.harness_id)
    assert mailbox.reconcile(second_assignee.harness_id)
    with pytest.raises(AuthorizationError, match="member"):
        service.thread(actor=outsider, conversation_id=conversation_id, thread_id="thread:work")


def test_conversation_policy_and_mailbox_projection_rollback_together(store, identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    conversation_id = "conversation:atomic"
    policy = LocalConformancePolicyEngine(store)
    mailbox = MailboxService(store)
    service = ConversationService(store, policy, mailbox)
    grant_actions(policy, actor, conversation_id, ("conversation.create", "conversation.message.send"))
    service.create(
        actor=actor,
        conversation_id=conversation_id,
        member_harness_ids=(recipient.harness_id,),
        classification=Classification.C0_PUBLIC,
    )
    before_events = store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"]
    before_decisions = store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"]

    def crash(phase: str) -> None:
        if phase == "before_conversation_action_commit":
            raise InjectedConversationCrash(phase)

    with pytest.raises(InjectedConversationCrash):
        service.post(
            actor=actor,
            recipients=(recipient.harness_id,),
            conversation_id=conversation_id,
            thread_id="thread:atomic",
            action={"kind": "post", "body": "must rollback"},
            idempotency_key="conversation-atomic-crash",
            phase_hook=crash,
        )
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == before_events
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == before_decisions
    assert store.fetch_one("SELECT COUNT(*) AS count FROM conversation_actions")["count"] == 0
    assert mailbox.reconcile(recipient.harness_id) == []

    recovered = service.post(
        actor=actor,
        recipients=(recipient.harness_id,),
        conversation_id=conversation_id,
        thread_id="thread:atomic",
        action={"kind": "post", "body": "must rollback"},
        idempotency_key="conversation-atomic-crash",
    )
    assert recovered["duplicate"] is False
    assert len(mailbox.reconcile(recipient.harness_id)) == 1


def test_completed_conversation_ack_requires_result_digest(identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    with pytest.raises(PydanticValidationError, match="result digest"):
        build(
            actor,
            recipient,
            {
                "kind": "completion_ack",
                "target_event_id": "event:source",
                "task_id": "task:synthetic",
                "outcome": "completed",
            },
        )
