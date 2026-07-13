from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.authorization.policy import HumanEntitlement, LocalConformancePolicyEngine
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.conversation import ConversationService
from agentnet.messaging.obligation import (
    OBLIGATION_TERMINAL_STATES,
    OBLIGATION_TRANSITIONS,
    ResponseObligationService,
    require_obligation_transition,
)
from agentnet.protocol.models import Classification, DeliveryFact


class InjectedObligationCrash(RuntimeError):
    pass


OBLIGATION_ACTIONS = (
    "conversation.create",
    "conversation.message.send",
    "conversation.structured_request.send",
    "conversation.response_obligation.create",
    "conversation.response_obligation.respond",
    "conversation.response_obligation.update",
    "conversation.response_obligation.cancel",
)


def grant_actions(policy, actor, conversation_id: str) -> None:
    for action in OBLIGATION_ACTIONS:
        policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=f"conversation:{conversation_id}",
                revision=1,
            )
        )


@pytest.fixture
def stack(store, identity_factory):
    requester, _ = identity_factory()
    responder, _ = identity_factory(kind="pi")
    observer, _ = identity_factory(kind="claude")
    policy = LocalConformancePolicyEngine(store)
    mailbox = MailboxService(store)
    service = ConversationService(store, policy, mailbox)
    conversation_id = f"conversation:obligation-{uuid4().hex[:8]}"
    for actor in (requester, responder, observer):
        grant_actions(policy, actor, conversation_id)
    service.create(
        actor=requester,
        conversation_id=conversation_id,
        member_harness_ids=(responder.harness_id, observer.harness_id),
        classification=Classification.C0_PUBLIC,
    )
    return {
        "store": store,
        "policy": policy,
        "mailbox": mailbox,
        "service": service,
        "obligations": service.obligations,
        "conversation_id": conversation_id,
        "requester": requester,
        "responder": responder,
        "observer": observer,
    }


def post_request(stack, *, deadline=None, idempotency_key=None, responsible=None, schema=None):
    spec: dict[str, object] = {}
    if deadline is not None:
        spec["deadline_at"] = deadline.isoformat()
    if responsible is not None:
        spec["responsible_harness_id"] = responsible
    if schema is not None:
        spec["response_schema_id"] = schema
    return stack["service"].post(
        actor=stack["requester"],
        recipients=(stack["responder"].harness_id,),
        conversation_id=stack["conversation_id"],
        thread_id="thread:obligation",
        action={"kind": "post", "body": "please answer", "response_obligation": spec},
        idempotency_key=idempotency_key or f"request-{uuid4()}",
    )


def request_digest(stack, obligation_id: str) -> str:
    row = stack["store"].fetch_one(
        "SELECT request_payload_digest FROM response_obligations WHERE obligation_id=?",
        (obligation_id,),
    )
    return str(row["request_payload_digest"])


def post_response(stack, *, obligation_id, request_event_id, digest=None, outcome="completed",
                  actor=None, idempotency_key=None, schema=None):
    action = {
        "kind": "obligation_response",
        "obligation_id": obligation_id,
        "request_event_id": request_event_id,
        "request_digest": digest or request_digest(stack, obligation_id),
        "outcome": outcome,
        "body": "the exact answer",
    }
    if schema is not None:
        action["response_schema_id"] = schema
    return stack["service"].post(
        actor=actor or stack["responder"],
        recipients=(stack["requester"].harness_id,),
        conversation_id=stack["conversation_id"],
        thread_id="thread:obligation",
        action=action,
        idempotency_key=idempotency_key or f"response-{uuid4()}",
    )


def test_state_machine_is_explicit_and_terminal_states_are_final() -> None:
    for current, allowed in OBLIGATION_TRANSITIONS.items():
        assert current not in OBLIGATION_TERMINAL_STATES
        for proposed in allowed:
            require_obligation_transition(current, proposed)
    for terminal in OBLIGATION_TERMINAL_STATES:
        with pytest.raises(ConflictError):
            require_obligation_transition(terminal, "acknowledged")
    with pytest.raises(ConflictError):
        require_obligation_transition("created", "in_progress")
    with pytest.raises(ConflictError):
        require_obligation_transition("recipient_committed", "pending_human")


def test_obligation_binds_request_and_idempotent_retry_creates_one(stack) -> None:
    key = f"request-{uuid4()}"
    result = post_request(stack, idempotency_key=key)
    obligation = result["response_obligation"]
    assert obligation["state"] == "created" and obligation["revision"] == 1

    record = stack["obligations"].get(
        actor=stack["requester"], obligation_id=obligation["obligation_id"]
    )
    assert record["request_event_id"] == result["event_id"]
    assert record["request_envelope_digest"] == result["envelope_digest"]
    assert record["requester_harness_id"] == stack["requester"].harness_id
    assert record["responsible_harness_id"] == stack["responder"].harness_id
    assert record["response_required"] is True

    retry = post_request(stack, idempotency_key=key)
    assert retry["duplicate"] is True
    rows = stack["store"].fetch_all(
        "SELECT obligation_id FROM response_obligations WHERE request_event_id=?",
        (result["event_id"],),
    )
    assert len(rows) == 1


def test_multi_recipient_requires_exact_responsible_recipient(stack) -> None:
    recipients = (stack["responder"].harness_id, stack["observer"].harness_id)
    with pytest.raises(ValidationError, match="one exact responsible recipient"):
        stack["service"].post(
            actor=stack["requester"],
            recipients=recipients,
            conversation_id=stack["conversation_id"],
            thread_id="thread:obligation",
            action={"kind": "post", "body": "who answers?", "response_obligation": {}},
            idempotency_key=f"request-{uuid4()}",
        )
    with pytest.raises(ValidationError, match="one exact responsible recipient"):
        stack["service"].post(
            actor=stack["requester"],
            recipients=(stack["responder"].harness_id,),
            conversation_id=stack["conversation_id"],
            thread_id="thread:obligation",
            action={
                "kind": "post",
                "body": "wrong responsible",
                "response_obligation": {"responsible_harness_id": "harness-unrelated"},
            },
            idempotency_key=f"request-{uuid4()}",
        )
    result = stack["service"].post(
        actor=stack["requester"],
        recipients=recipients,
        conversation_id=stack["conversation_id"],
        thread_id="thread:obligation",
        action={
            "kind": "post",
            "body": "observer answers",
            "response_obligation": {"responsible_harness_id": stack["observer"].harness_id},
        },
        idempotency_key=f"request-{uuid4()}",
    )
    record = stack["obligations"].get(
        actor=stack["requester"],
        obligation_id=result["response_obligation"]["obligation_id"],
    )
    assert record["responsible_harness_id"] == stack["observer"].harness_id


def test_past_deadline_and_self_obligation_fail_closed(stack) -> None:
    with pytest.raises(ValidationError, match="deadline must be in the future"):
        post_request(stack, deadline=datetime.now(UTC) - timedelta(seconds=5))
    with pytest.raises(ValidationError, match="cannot name its requester"):
        stack["service"].post(
            actor=stack["requester"],
            recipients=(stack["requester"].harness_id,),
            conversation_id=stack["conversation_id"],
            thread_id="thread:obligation",
            action={"kind": "post", "body": "note to self", "response_obligation": {}},
            idempotency_key=f"request-{uuid4()}",
        )


def test_prose_reply_and_wrong_binding_never_close(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    request_event_id = result["event_id"]

    stack["service"].post(
        actor=stack["responder"],
        recipients=(stack["requester"].harness_id,),
        conversation_id=stack["conversation_id"],
        thread_id="thread:obligation",
        action={"kind": "reply", "reply_to_event_id": request_event_id, "body": "prose only"},
        idempotency_key=f"reply-{uuid4()}",
    )
    assert (
        stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)["state"]
        == "created"
    )

    with pytest.raises(AuthorizationError, match="exact original request identifier and digest"):
        post_response(stack, obligation_id=obligation_id, request_event_id=request_event_id, digest="0" * 64)

    other = post_request(stack)
    with pytest.raises(AuthorizationError, match="exact original request identifier and digest"):
        post_response(
            stack,
            obligation_id=obligation_id,
            request_event_id=other["event_id"],
            digest=request_digest(stack, obligation_id),
        )
    assert (
        stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)["state"]
        == "created"
    )


def test_unauthorized_closure_is_rejected(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    for wrong_actor in (stack["requester"], stack["observer"]):
        with pytest.raises(AuthorizationError, match="exact responsible recipient harness"):
            post_response(
                stack,
                obligation_id=obligation_id,
                request_event_id=result["event_id"],
                actor=wrong_actor,
            )
    assert (
        stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)["state"]
        == "created"
    )


def test_typed_response_closes_atomically_with_exact_linkage(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    assert stack["obligations"].inbox(actor=stack["requester"])["awaiting_peer"] == 1
    assert stack["obligations"].inbox(actor=stack["responder"])["action_required"] == 1

    closed = post_response(
        stack, obligation_id=obligation_id, request_event_id=result["event_id"]
    )
    assert closed["response_obligation"]["state"] == "completed"
    record = stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)
    assert record["state"] == "completed"
    assert record["response_event_id"] == closed["event_id"]
    assert record["response_outcome"] == "completed"
    assert record["closed_at"] is not None
    assert record["transitions"][-1]["to_state"] == "completed"
    assert stack["obligations"].inbox(actor=stack["requester"])["awaiting_peer"] == 0
    assert stack["obligations"].inbox(actor=stack["responder"])["action_required"] == 0


def test_duplicate_and_conflicting_terminal_responses(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    key = f"response-{uuid4()}"
    closed = post_response(
        stack, obligation_id=obligation_id, request_event_id=result["event_id"], idempotency_key=key
    )

    retry = post_response(
        stack, obligation_id=obligation_id, request_event_id=result["event_id"], idempotency_key=key
    )
    assert retry["duplicate"] is True and retry["event_id"] == closed["event_id"]
    record = stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)
    assert record["state"] == "completed" and record["response_event_id"] == closed["event_id"]

    with pytest.raises(ConflictError, match="already has a terminal outcome"):
        post_response(
            stack, obligation_id=obligation_id, request_event_id=result["event_id"], outcome="failed"
        )


def test_failed_outcome_and_failed_counter(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    post_response(
        stack, obligation_id=obligation_id, request_event_id=result["event_id"], outcome="failed"
    )
    record = stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)
    assert record["state"] == "failed" and record["response_outcome"] == "failed"
    assert stack["obligations"].inbox(actor=stack["requester"])["failed"] == 1


def test_demanded_response_schema_must_be_declared(stack) -> None:
    result = post_request(stack, schema="inventory.lookup.result")
    obligation_id = result["response_obligation"]["obligation_id"]
    with pytest.raises(ValidationError, match="exact demanded response schema"):
        post_response(stack, obligation_id=obligation_id, request_event_id=result["event_id"])
    closed = post_response(
        stack,
        obligation_id=obligation_id,
        request_event_id=result["event_id"],
        schema="inventory.lookup.result",
    )
    assert closed["response_obligation"]["state"] == "completed"


def test_recipient_progress_transitions_and_revision_fencing(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    obligations = stack["obligations"]

    with pytest.raises(ConflictError, match="illegal response-obligation transition"):
        obligations.transition(
            actor=stack["responder"], obligation_id=obligation_id, to_state="in_progress"
        )
    with pytest.raises(ValidationError, match="terminal outcomes require"):
        obligations.transition(
            actor=stack["responder"], obligation_id=obligation_id, to_state="completed"
        )
    with pytest.raises(AuthorizationError, match="exact responsible recipient harness"):
        obligations.transition(
            actor=stack["requester"], obligation_id=obligation_id, to_state="acknowledged"
        )
    with pytest.raises(ConflictError, match="durable mailbox recipient fact"):
        obligations.transition(
            actor=stack["responder"], obligation_id=obligation_id, to_state="recipient_committed"
        )

    stack["mailbox"].transition(
        event_id=result["event_id"],
        recipient_id=stack["responder"].harness_id,
        proposed=DeliveryFact.RECIPIENT_COMMITTED,
        owner_actor=stack["responder"],
    )
    committed = obligations.transition(
        actor=stack["responder"], obligation_id=obligation_id, to_state="recipient_committed"
    )
    assert committed["state"] == "recipient_committed" and committed["revision"] == 2

    with pytest.raises(ConflictError, match="revision fence"):
        obligations.transition(
            actor=stack["responder"],
            obligation_id=obligation_id,
            to_state="acknowledged",
            expected_revision=1,
        )
    obligations.transition(
        actor=stack["responder"],
        obligation_id=obligation_id,
        to_state="acknowledged",
        expected_revision=2,
    )
    obligations.transition(
        actor=stack["responder"], obligation_id=obligation_id, to_state="in_progress"
    )
    obligations.transition(
        actor=stack["responder"], obligation_id=obligation_id, to_state="pending_human"
    )
    assert stack["obligations"].inbox(actor=stack["requester"])["awaiting_human"] == 1
    assert stack["obligations"].inbox(actor=stack["responder"])["awaiting_human"] == 1
    obligations.transition(
        actor=stack["responder"], obligation_id=obligation_id, to_state="in_progress"
    )
    obligations.transition(
        actor=stack["responder"], obligation_id=obligation_id, to_state="blocked"
    )
    record = obligations.get(actor=stack["responder"], obligation_id=obligation_id)
    assert [item["to_state"] for item in record["transitions"]] == [
        "recipient_committed",
        "acknowledged",
        "in_progress",
        "pending_human",
        "in_progress",
        "blocked",
    ]


def test_cancellation_requires_exact_requester(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    with pytest.raises(AuthorizationError, match="exact accountable requester"):
        stack["obligations"].cancel(actor=stack["responder"], obligation_id=obligation_id)
    canceled = stack["obligations"].cancel(
        actor=stack["requester"], obligation_id=obligation_id, reason_code="no_longer_needed"
    )
    assert canceled["state"] == "canceled"
    with pytest.raises(ConflictError):
        stack["obligations"].cancel(actor=stack["requester"], obligation_id=obligation_id)
    with pytest.raises(ConflictError, match="already has a terminal outcome"):
        post_response(stack, obligation_id=obligation_id, request_event_id=result["event_id"])


def test_overdue_visibility_and_deadline_expiry_reconciliation(stack) -> None:
    deadline = datetime.now(UTC) + timedelta(seconds=60)
    result = post_request(stack, deadline=deadline)
    obligation_id = result["response_obligation"]["obligation_id"]
    obligations = stack["obligations"]

    now = int(datetime.now(UTC).timestamp())
    assert obligations.inbox(actor=stack["requester"], now=now)["overdue"] == 0
    late = now + 120
    assert obligations.inbox(actor=stack["requester"], now=late)["overdue"] == 1
    assert obligations.inbox(actor=stack["responder"], now=late)["overdue"] == 1

    # The responsible party cannot expire its own duty to answer.
    outcome = obligations.reconcile(
        actor=stack["responder"], authoritative_now=datetime.fromtimestamp(late, UTC)
    )
    assert outcome["expired"] == []

    outcome = obligations.reconcile(
        actor=stack["requester"], authoritative_now=datetime.fromtimestamp(late, UTC)
    )
    assert outcome["expired"] == [obligation_id]
    record = obligations.get(actor=stack["requester"], obligation_id=obligation_id)
    assert record["state"] == "expired" and record["closed_at"] is not None

    repeat = obligations.reconcile(
        actor=stack["requester"], authoritative_now=datetime.fromtimestamp(late, UTC)
    )
    assert repeat["expired"] == []
    with pytest.raises(ConflictError, match="already has a terminal outcome"):
        post_response(stack, obligation_id=obligation_id, request_event_id=result["event_id"])


def test_reconcile_mirrors_durable_commitment_after_restart(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    stack["mailbox"].transition(
        event_id=result["event_id"],
        recipient_id=stack["responder"].harness_id,
        proposed=DeliveryFact.RECIPIENT_COMMITTED,
        owner_actor=stack["responder"],
    )
    # A freshly constructed service over the same store must see identical
    # durable state and reconcile idempotently (crash/restart safety).
    restarted = ResponseObligationService(stack["store"], stack["policy"])
    outcome = restarted.reconcile(actor=stack["responder"])
    assert outcome["recipient_committed"] == [obligation_id]
    assert (
        restarted.get(actor=stack["responder"], obligation_id=obligation_id)["state"]
        == "recipient_committed"
    )
    assert restarted.reconcile(actor=stack["responder"])["recipient_committed"] == []


def test_crash_before_commit_persists_neither_side(stack) -> None:
    key = f"request-{uuid4()}"
    with pytest.raises(InjectedObligationCrash):
        stack["service"].post(
            actor=stack["requester"],
            recipients=(stack["responder"].harness_id,),
            conversation_id=stack["conversation_id"],
            thread_id="thread:obligation",
            action={"kind": "post", "body": "please answer", "response_obligation": {}},
            idempotency_key=key,
            phase_hook=lambda phase: (_ for _ in ()).throw(InjectedObligationCrash(phase)),
        )
    assert stack["store"].fetch_all("SELECT * FROM response_obligations", ()) == []

    result = post_request(stack, idempotency_key=key)
    obligation_id = result["response_obligation"]["obligation_id"]

    response_key = f"response-{uuid4()}"
    with pytest.raises(InjectedObligationCrash):
        stack["service"].post(
            actor=stack["responder"],
            recipients=(stack["requester"].harness_id,),
            conversation_id=stack["conversation_id"],
            thread_id="thread:obligation",
            action={
                "kind": "obligation_response",
                "obligation_id": obligation_id,
                "request_event_id": result["event_id"],
                "request_digest": request_digest(stack, obligation_id),
                "outcome": "completed",
                "body": "the exact answer",
            },
            idempotency_key=response_key,
            phase_hook=lambda phase: (_ for _ in ()).throw(InjectedObligationCrash(phase)),
        )
    record = stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)
    assert record["state"] == "created" and record["response_event_id"] is None

    closed = post_response(
        stack,
        obligation_id=obligation_id,
        request_event_id=result["event_id"],
        idempotency_key=response_key,
    )
    assert closed["response_obligation"]["state"] == "completed"


def test_exact_fetch_and_list_visibility(stack) -> None:
    result = post_request(stack)
    obligation_id = result["response_obligation"]["obligation_id"]
    assert (
        stack["obligations"].get(actor=stack["requester"], obligation_id=obligation_id)[
            "viewer_role"
        ]
        == "requester"
    )
    assert (
        stack["obligations"].get(actor=stack["responder"], obligation_id=obligation_id)[
            "viewer_role"
        ]
        == "responsible"
    )
    with pytest.raises(AuthorizationError, match="unavailable"):
        stack["obligations"].get(actor=stack["observer"], obligation_id=obligation_id)
    with pytest.raises(AuthorizationError, match="unavailable"):
        stack["obligations"].get(actor=stack["requester"], obligation_id=str(uuid4()))

    assert stack["obligations"].list_for(actor=stack["observer"]) == []
    mine = stack["obligations"].list_for(actor=stack["requester"], role="requester")
    assert [item["obligation_id"] for item in mine] == [obligation_id]
    assert (
        stack["obligations"].list_for(
            actor=stack["responder"], role="responsible", states=("created",)
        )[0]["obligation_id"]
        == obligation_id
    )
    with pytest.raises(ValidationError, match="unknown state"):
        stack["obligations"].list_for(actor=stack["requester"], states=("bogus",))


def test_inbox_distinguishes_information_from_action(stack) -> None:
    # Plain informational message: responder sees unread information only.
    stack["service"].post(
        actor=stack["requester"],
        recipients=(stack["responder"].harness_id,),
        conversation_id=stack["conversation_id"],
        thread_id="thread:obligation",
        action={"kind": "post", "body": "fyi only"},
        idempotency_key=f"info-{uuid4()}",
    )
    responder_inbox = stack["obligations"].inbox(actor=stack["responder"])
    assert responder_inbox["unread_information"] == 1
    assert responder_inbox["action_required"] == 0

    post_request(stack)
    responder_inbox = stack["obligations"].inbox(actor=stack["responder"])
    assert responder_inbox["unread_information"] == 1
    assert responder_inbox["action_required"] == 1
    requester_inbox = stack["obligations"].inbox(actor=stack["requester"])
    assert requester_inbox["awaiting_peer"] == 1
    assert requester_inbox["action_required"] == 0
