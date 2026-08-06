from __future__ import annotations

from datetime import UTC, datetime
import json
from uuid import uuid4

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.communication_scope_service import CollaborationScope
from agentnet.authorization.policy import HumanEntitlement, LocalConformancePolicyEngine, PolicyEngine
from agentnet.errors import AuthorizationError, IdempotencyConflict, ValidationError
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

def synthetic_scope(
    actor,
    *members,
    scope_id="scope:test-conversation",
    extra_actions=(),
    extra_resource_prefixes=(),
):
    member_harness_ids = tuple(
        sorted({actor.harness_id, *(member.harness_id for member in members)})
    )
    now = int(datetime.now(UTC).timestamp())
    return CollaborationScope(
        scope_id=scope_id,
        scope_kind=(
            "personal"
            if len(member_harness_ids) == 1
            else "direct"
            if len(member_harness_ids) == 2
            else "shared"
        ),
        domain_id=actor.domain_id,
        owner_principal_id=actor.principal_id,
        owner_harness_id=actor.harness_id,
        member_harness_ids=member_harness_ids,
        allowed_actions=tuple(
            sorted(
                {
                    "message.acknowledge",
                    "message.read",
                    "message.send",
                    "obligation.create",
                    "obligation.respond",
                    "task.accept",
                    "task.cancel",
                    "task.handoff",
                    "task.propose",
                    *extra_actions,
                }
            )
        ),
        allowed_resource_prefixes=tuple(
            sorted({"conversation:", "task:", *extra_resource_prefixes})
        ),
        allowed_classifications=(
            Classification.C0_PUBLIC,
            Classification.C1_INTERNAL,
            Classification.C2_RESTRICTED,
        ),
        canonical_references=(),
        policy_revision=1,
        domain_revocation_epoch=1,
        control_sequence=1,
        membership_sequence=1,
        proposal_digest="a" * 64,
        scope_digest="b" * 64,
        revision=1,
        state="active",
        state_reason="test.fixture",
        created_at=now,
        updated_at=now,
    )


class StaticCollaborationScopes:
    def __init__(self):
        self.scopes = {}
        self.revoked = False

    def require_in_transaction(
        self,
        _connection,
        *,
        actor,
        scope_id,
        action,
        resource,
        target_harness_ids,
        classification,
        when=None,
    ):
        scope = self.scopes.get(scope_id)
        if scope is None:
            members = tuple(
                type("ExactMember", (), {"harness_id": harness_id})
                for harness_id in target_harness_ids
                if harness_id != actor.harness_id
            )
            scope = synthetic_scope(actor, *members, scope_id=scope_id)
            self.scopes[scope_id] = scope
        if self.revoked:
            raise AuthorizationError("test collaboration scope is revoked")
        if (
            actor.harness_id not in scope.member_harness_ids
            or not set(target_harness_ids).issubset(scope.member_harness_ids)
            or action not in scope.allowed_actions
            or classification not in scope.allowed_classifications
            or not any(resource.startswith(prefix) for prefix in scope.allowed_resource_prefixes)
        ):
            raise AuthorizationError("test collaboration scope denied the operation")
        return scope

    def get_for_actor(self, *, actor, scope_id, when=None):
        scope = self.scopes.get(scope_id)
        if (
            self.revoked
            or scope is None
            or scope.domain_id != actor.domain_id
            or actor.harness_id not in scope.member_harness_ids
        ):
            raise AuthorizationError("test collaboration scope is unavailable")
        return scope


_ConversationService = ConversationService
_MailboxService = MailboxService


def mailbox_for(store, collaboration_scopes=None):
    collaboration_scopes = collaboration_scopes or StaticCollaborationScopes()
    return _MailboxService(
        store,
        collaboration_scopes=collaboration_scopes,
    )


def service_for(store, policy, mailbox, **kwargs):
    return _ConversationService(
        store,
        policy,
        mailbox,
        collaboration_scopes=mailbox.collaboration_scopes,
        **kwargs,
    )


def build(actor, recipient, action):
    released_artifact_ids = tuple(
        str(binding["artifact_id"])
        for binding in action.get("released_artifacts", ())
    )
    collaboration_scope = synthetic_scope(
        actor,
        recipient,
        extra_actions=("artifact.send",) if released_artifact_ids else (),
        extra_resource_prefixes=tuple(
            f"artifact:{artifact_id}" for artifact_id in released_artifact_ids
        ),
    )
    return build_conversation_event(
        actor=actor,
        recipients=(recipient.harness_id,),
        conversation_id="conversation:synthetic",
        thread_id="thread:synthetic",
        action=action,
        idempotency_key=f"conversation-{uuid4()}",
        classification=Classification.C1_INTERNAL,
        collaboration_scope=collaboration_scope,
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
    assert reply.payload["authorization_context"] == {
        "collaboration_scope_id": "scope:test-conversation",
        "collaboration_scope_revision": 1,
        "collaboration_scope_policy_revision": 1,
        "collaboration_scope_domain_revocation_epoch": 1,
        "collaboration_scope_member_harness_ids": sorted(
            (actor.harness_id, recipient.harness_id)
        ),
        "collaboration_scope_digest": "b" * 64,
    }

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
                resource_pattern=(
                    "*" if action == "conversation.thread" else f"conversation:{conversation_id}"
                ),
                revision=1,
            )
        )


def test_legacy_generic_conversation_action_does_not_authorize_exact_operation(
    store,
    identity_factory,
) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    conversation_id = "conversation:generic-action-denied"
    policy = LocalConformancePolicyEngine(store)
    service = service_for(store, policy, mailbox_for(store))
    grant_actions(
        policy,
        actor,
        conversation_id,
        ("conversation.create", "conversation.action"),
    )
    service.create(actor=actor, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
    classification=Classification.C0_PUBLIC,)

    with pytest.raises(AuthorizationError, match="no_positive_human_entitlement"):
        service.post(actor=actor, recipients=(recipient.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:generic-action-denied",
        action={"kind": "post", "body": "must not use a generic entitlement"},
        idempotency_key="generic-conversation-action-denied-0001",)


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
    mailbox = mailbox_for(store)
    for conversation_id in ("conversation:production-deny", "conversation:c1-deny", "conversation:local-c0"):
        grant_actions(local, creator, conversation_id, ("conversation.create",))
    set_deterministic_only(store, creator, recipient)

    with pytest.raises(AuthorizationError, match="synthetic_lab_harness_not_admitted"):
        service_for(store, PolicyEngine(store), mailbox).create(actor=creator, conversation_id="conversation:production-deny", collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
        classification=Classification.C0_PUBLIC,)
    with pytest.raises(AuthorizationError, match="synthetic_lab_harness_not_admitted"):
        service_for(store, local, mailbox).create(actor=creator, conversation_id="conversation:c1-deny", collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
        classification=Classification.C1_INTERNAL,)

    created = service_for(store, local, mailbox).create(actor=creator, conversation_id="conversation:local-c0", collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
    classification=Classification.C0_PUBLIC,)
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
        service_for(store, PolicyEngine(store), mailbox_for(store)).create(actor=creator, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
        classification=Classification.C0_PUBLIC,)


def test_operational_conversation_task_handoff_cancel_completion_and_receipts(
    store,
    identity_factory,
    monkeypatch,
) -> None:
    test_clock = {"value": int(datetime.now(UTC).timestamp())}
    monkeypatch.setattr(
        "agentnet.messaging.conversation.time.time",
        lambda: test_clock["value"],
    )
    creator, _ = identity_factory()
    first_assignee, _ = identity_factory(kind="claude")
    second_assignee, _ = identity_factory(kind="pi")
    outsider, _ = identity_factory(kind="antigravity")
    conversation_id = "conversation:operational"
    policy = LocalConformancePolicyEngine(store)
    mailbox = mailbox_for(store)
    service = service_for(store, policy, mailbox)
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
        ("conversation.task.complete", "conversation.thread"),
    )
    created = service.create(actor=creator, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", member_harness_ids=(first_assignee.harness_id, second_assignee.harness_id),
    classification=Classification.C0_PUBLIC,)
    assert created["duplicate"] is False

    root = service.post(actor=creator, recipients=(first_assignee.harness_id, second_assignee.harness_id), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={"kind": "post", "body": "begin", "mentions": [first_assignee.harness_id]},
    idempotency_key="conversation-operational-root",)
    reply = service.post(actor=first_assignee, recipients=(creator.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={"kind": "reply", "reply_to_event_id": root["event_id"], "body": "ack"},
    idempotency_key="conversation-operational-reply",)
    assert reply["fact"] == "accepted_local"

    task = service.post(actor=creator, recipients=(first_assignee.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={"kind": "task", "task_id": "task:work", "summary": "do exact work"},
    idempotency_key="conversation-operational-task",)
    test_clock["value"] += 1
    duplicate_task = service.post(actor=creator, recipients=(first_assignee.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={"kind": "task", "task_id": "task:work", "summary": "do exact work"},
    idempotency_key="conversation-operational-task",)
    assert duplicate_task["duplicate"] is True
    assert duplicate_task["collaboration_scope_id"] == "scope:test-conversation"
    assert duplicate_task["collaboration_scope_revision"] == 1
    assert duplicate_task["proposal_id"] == task["proposal_id"]
    assert duplicate_task["request_digest"] == task["request_digest"]
    with pytest.raises(IdempotencyConflict):
        service.post(actor=creator, recipients=(first_assignee.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
        action={
            "kind": "task",
            "task_id": "task:work",
            "summary": "do exact work",
            "effect_deadline": datetime.fromtimestamp(
                test_clock["value"] + 7_200,
                UTC,
            ),
        },
        idempotency_key="conversation-operational-task",)
    assert all(
        item["event"]["event_type"] != "task_assignment"
        for item in mailbox.reconcile(actor=first_assignee, collaboration_scope_id="scope:test-conversation")
    )
    task_approval = service.assignments.approve(
        actor=first_assignee,
        proposal_id=task["proposal_id"],
        expected_request_digest=task["request_digest"],
        expected_revision=task["proposal_revision"],
    )
    task = task | {"event_id": task_approval.resumed_event_id}

    handoff = service.post(actor=first_assignee, recipients=(second_assignee.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={
        "kind": "handoff",
        "task_id": "task:work",
        "source_event_id": task["event_id"],
        "from_harness_id": first_assignee.harness_id,
        "to_harness_id": second_assignee.harness_id,
        "state_digest": "a" * 64,
    },
    idempotency_key="conversation-operational-handoff",)
    assert all(
        item["event"].get("task_id") != "task:work"
        for item in mailbox.reconcile(actor=second_assignee, collaboration_scope_id="scope:test-conversation")
    )
    handoff_approval = service.assignments.approve(
        actor=second_assignee,
        proposal_id=handoff["proposal_id"],
        expected_request_digest=handoff["request_digest"],
        expected_revision=handoff["proposal_revision"],
    )
    handoff = handoff | {"event_id": handoff_approval.resumed_event_id}
    with pytest.raises(AuthorizationError, match="assignee"):
        service.post(actor=first_assignee, recipients=(creator.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
        action={
            "kind": "completion_ack",
            "target_event_id": handoff["event_id"],
            "task_id": "task:work",
            "outcome": "effect_unknown",
        },
        idempotency_key="conversation-old-assignee-complete",)

    cancel = service.post(actor=creator, recipients=(second_assignee.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={
        "kind": "cancellation",
        "target_event_id": handoff["event_id"],
        "task_id": "task:work",
        "reason_code": "owner.requested",
    },
    idempotency_key="conversation-operational-cancel",)
    completed = service.post(actor=second_assignee, recipients=(creator.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",
    action={
        "kind": "completion_ack",
        "target_event_id": cancel["event_id"],
        "task_id": "task:work",
        "outcome": "canceled",
    },
    idempotency_key="conversation-operational-completion",)
    assert completed["fact"] == "accepted_local"
    state = service.task_state(actor=creator, collaboration_scope_id="scope:test-conversation", conversation_id=conversation_id, task_id="task:work")
    assert state["state"] == "canceled"
    with pytest.raises(AuthorizationError):
        service.thread(actor=creator, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",)
    assert state["assignee_harness_id"] == second_assignee.harness_id
    thread_items = service.thread(actor=second_assignee, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work",)
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
    decisions = store.fetch_all(
        """SELECT action,resource_json,context_json
             FROM policy_decisions ORDER BY occurred_at,decision_id"""
    )
    requested_actions = {row["action"] for row in decisions}
    assert {
        "conversation.create",
        "conversation.message.send",
        "conversation.task.request",
        "conversation.task.handoff",
        "conversation.task.cancel_request",
        "conversation.task.complete",
        "conversation.thread",
    } <= requested_actions
    assert requested_actions.isdisjoint(
        {
            "conversation.action",
            "conversation.structured_request.send",
            "conversation.response_obligation.respond",
        }
    )
    create_decision = next(row for row in decisions if row["action"] == "conversation.create")
    assert json.loads(create_decision["context_json"])["request"]["member_harness_ids"] == sorted(
        (creator.harness_id, first_assignee.harness_id, second_assignee.harness_id)
    )
    thread_decision = next(row for row in decisions if row["action"] == "conversation.thread")
    assert json.loads(thread_decision["resource_json"]) == {"id": f"conversation:{conversation_id}"}
    assert json.loads(thread_decision["context_json"])["request"]["thread_id"] == "thread:work"
    assert mailbox.reconcile(actor=first_assignee, collaboration_scope_id="scope:test-conversation")
    assert mailbox.reconcile(actor=second_assignee, collaboration_scope_id="scope:test-conversation")
    with pytest.raises(AuthorizationError, match="member"):
        service.thread(actor=outsider, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:work")


def test_conversation_policy_and_mailbox_projection_rollback_together(store, identity_factory) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    conversation_id = "conversation:atomic"
    policy = LocalConformancePolicyEngine(store)
    mailbox = mailbox_for(store)
    service = service_for(store, policy, mailbox)
    grant_actions(
        policy,
        actor,
        conversation_id,
        ("conversation.create", "conversation.message.send"),
    )
    service.create(actor=actor, conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", member_harness_ids=(recipient.harness_id,),
    classification=Classification.C0_PUBLIC,)
    before_events = store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"]
    before_decisions = store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"]

    def crash(phase: str) -> None:
        if phase == "before_conversation_action_commit":
            raise InjectedConversationCrash(phase)

    with pytest.raises(InjectedConversationCrash):
        service.post(actor=actor, recipients=(recipient.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:atomic",
        action={"kind": "post", "body": "must rollback"},
        idempotency_key="conversation-atomic-crash",
        phase_hook=crash,)
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == before_events
    assert store.fetch_one("SELECT COUNT(*) AS count FROM policy_decisions")["count"] == before_decisions
    assert store.fetch_one("SELECT COUNT(*) AS count FROM conversation_actions")["count"] == 0
    assert mailbox.reconcile(actor=recipient, collaboration_scope_id="scope:test-conversation") == []

    recovered = service.post(actor=actor, recipients=(recipient.harness_id,), conversation_id=conversation_id, collaboration_scope_id="scope:test-conversation", thread_id="thread:atomic",
    action={"kind": "post", "body": "must rollback"},
    idempotency_key="conversation-atomic-crash",)
    assert recovered["duplicate"] is False
    assert len(mailbox.reconcile(actor=recipient, collaboration_scope_id="scope:test-conversation")) == 1


def test_revoked_scope_denies_conversation_idempotency_replay(
    store,
    identity_factory,
) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    conversation_id = "conversation:revoked-replay"
    policy = LocalConformancePolicyEngine(store)
    scope_gate = StaticCollaborationScopes()
    mailbox = mailbox_for(store, scope_gate)
    service = _ConversationService(
        store,
        policy,
        mailbox,
        collaboration_scopes=scope_gate,
    )
    grant_actions(
        policy,
        actor,
        conversation_id,
        ("conversation.create", "conversation.message.send"),
    )
    service.create(
        actor=actor,
        conversation_id=conversation_id,
        collaboration_scope_id="scope:test-conversation",
        member_harness_ids=(recipient.harness_id,),
        classification=Classification.C0_PUBLIC,
    )
    service.post(
        actor=actor,
        recipients=(recipient.harness_id,),
        conversation_id=conversation_id,
        collaboration_scope_id="scope:test-conversation",
        thread_id="thread:revoked",
        action={"kind": "post", "body": "accepted once"},
        idempotency_key="conversation-revoked-replay",
    )
    with pytest.raises(AuthorizationError, match="idempotency key"):
        service.post(
            actor=actor,
            recipients=(recipient.harness_id,),
            conversation_id=conversation_id,
            collaboration_scope_id="scope:different-conversation",
            thread_id="thread:revoked",
            action={"kind": "post", "body": "accepted once"},
            idempotency_key="conversation-revoked-replay",
        )
    scope_gate.revoked = True

    with pytest.raises(AuthorizationError, match="revoked"):
        service.post(
            actor=actor,
            recipients=(recipient.harness_id,),
            conversation_id=conversation_id,
            collaboration_scope_id="scope:test-conversation",
            thread_id="thread:revoked",
            action={"kind": "post", "body": "accepted once"},
            idempotency_key="conversation-revoked-replay",
        )


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
