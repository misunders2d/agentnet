from __future__ import annotations

import json
import threading
from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict, ValidationError
from agentnet.authorization.grants import TaskGrantService
from agentnet.identity.workload import RegisteredWorkloadCredential, WorkloadTransitionProof
from agentnet.mailbox.service import ExpiryAuthorization, MailboxService
from agentnet.messaging.events import new_event
from agentnet.operations.policy_defaults import RevocationPolicy
from agentnet.protocol.models import (
    AcceptingStorageBoundary,
    Classification,
    DeliveryFact,
    EventType,
    Receipt,
    TaskGrant,
)
from agentnet.provenance import OriginKind, ProvenanceService, ReviewState, ScanState


def test_offline_store_forward_and_exact_duplicate(store, identity_factory) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "hello"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    accepted = mailbox.accept(event)
    assert accepted["fact"] == "accepted_local"
    assert accepted["provenance"]["authority_effect"] == "none"
    assert accepted["provenance"]["tainted"] is True
    assert mailbox.accept(event)["provenance"] == accepted["provenance"]
    assert mailbox.accept(event)["duplicate"] is True
    link = store.fetch_one(
        "SELECT provenance_digest,object_type FROM event_provenance WHERE event_id=?",
        (event.event_id,),
    )
    assert link is not None
    assert link["provenance_digest"] == accepted["provenance"]["provenance_digest"]
    assert link["object_type"] == "event"
    rows = mailbox.reconcile(recipient.harness_id)
    assert rows[0]["payload"] == {"text": "hello"}
    assert rows[0]["fact"] == "accepted_local"
    assert rows[0]["provenance"] == accepted["provenance"]


def test_causal_child_uses_exact_server_resolved_parent_provenance(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store)
    parent = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "root"},
        idempotency_key=f"causal-root-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    parent_result = mailbox.accept(parent)
    child = new_event(
        domain_id=sender.domain_id,
        actor=recipient,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "reply"},
        idempotency_key=f"causal-child-{uuid4()}",
        recipients=(sender.harness_id,),
        causal_parent_ids=(parent.event_id,),
    )

    accepted = mailbox.accept(child)
    record = ProvenanceService(store).get_by_digest(
        accepted["provenance"]["provenance_digest"]
    )

    assert record.origin.kind is OriginKind.DERIVED
    assert record.parent_digests.digests == (
        parent_result["provenance"]["provenance_digest"],
    )
    assert record.transformations.steps == ()
    assert record.content_digest == child.payload_digest
    assert record.review_state is ReviewState.UNREVIEWED
    assert record.scan_state is ScanState.PENDING
    assert record.tainted is True
    assert record.allowed_sinks.sinks == tuple(sorted((sender.harness_id, recipient.harness_id)))
    assert mailbox.accept(child)["provenance"] == accepted["provenance"]

    other_parent = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "other root"},
        idempotency_key=f"causal-other-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(other_parent)
    changed_retry = new_event(
        domain_id=sender.domain_id,
        actor=recipient,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "reply"},
        idempotency_key=child.idempotency_key,
        recipients=(sender.harness_id,),
        causal_parent_ids=(other_parent.event_id,),
    )
    with pytest.raises(IdempotencyConflict, match="canonical intent"):
        mailbox.accept(changed_retry)


@pytest.mark.parametrize("failure", ("missing", "lower_classification", "new_sink"))
def test_causal_provenance_failures_leave_no_child_custody(
    store,
    identity_factory,
    failure: str,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    outsider, _ = identity_factory(kind="claude")
    mailbox = MailboxService(store)
    parent = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=(
            Classification.C2_RESTRICTED
            if failure == "lower_classification"
            else Classification.C1_INTERNAL
        ),
        payload={"text": "bounded parent"},
        idempotency_key=f"bounded-parent-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(parent)
    child = new_event(
        domain_id=sender.domain_id,
        actor=recipient,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "must fail atomically"},
        idempotency_key=f"bounded-child-{uuid4()}",
        recipients=(outsider.harness_id if failure == "new_sink" else sender.harness_id,),
        causal_parent_ids=("missing-parent" if failure == "missing" else parent.event_id,),
    )

    with pytest.raises((AuthorizationError, ConflictError, ValidationError, ValueError)):
        mailbox.accept(child)

    assert store.fetch_one("SELECT 1 FROM events WHERE event_id=?", (child.event_id,)) is None
    assert store.fetch_one(
        "SELECT 1 FROM content_provenance WHERE object_id=?", (child.event_id,)
    ) is None
    assert store.fetch_one(
        "SELECT 1 FROM event_provenance WHERE event_id=?", (child.event_id,)
    ) is None


def test_lost_response_retry_converges_on_client_stable_canonical_intent(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    key = f"message-{uuid4()}"
    first = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "stable retry"},
        idempotency_key=key,
        recipients=(recipient.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
    )
    accepted = mailbox.accept(first)
    retry = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "stable retry"},
        idempotency_key=key,
        recipients=(recipient.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=2),
    )

    duplicate = mailbox.accept(retry)

    assert retry.event_id != first.event_id
    assert retry.created_at != first.created_at
    assert retry.retention_delete_at != first.retention_delete_at
    assert duplicate["duplicate"] is True
    assert duplicate["event_id"] == accepted["event_id"]
    assert duplicate["envelope_digest"] == accepted["envelope_digest"]
    assert duplicate["provenance"] == accepted["provenance"]
    assert len(mailbox.reconcile(recipient.harness_id)) == 1


def test_event_and_provenance_roll_back_together_and_missing_link_fails_closed(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store)
    rolled_back = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "rollback provenance"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    with pytest.raises(RuntimeError, match="rollback"):
        with store.transaction() as connection:
            mailbox._accept_in_transaction(connection, rolled_back)
            raise RuntimeError("rollback")
    assert store.fetch_one(
        "SELECT event_id FROM events WHERE event_id=?",
        (rolled_back.event_id,),
    ) is None
    assert store.fetch_one(
        "SELECT object_id FROM content_provenance WHERE object_id=?",
        (rolled_back.event_id,),
    ) is None

    accepted = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "linked provenance"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(accepted)
    with store.transaction() as connection:
        connection.execute(
            "DELETE FROM event_provenance WHERE event_id=?",
            (accepted.event_id,),
        )
    with pytest.raises(ConflictError, match="mandatory provenance"):
        mailbox.reconcile(recipient.harness_id)


def test_content_free_watch_wakes_only_for_a_committed_durable_cursor(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    unrelated, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"secret": "never part of the wake"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    woke = threading.Event()
    unrelated_woke = threading.Event()
    subscription_id = mailbox.subscribe_content_free_wake(recipient.harness_id, woke.set)
    unrelated_subscription_id = mailbox.subscribe_content_free_wake(
        unrelated.harness_id,
        unrelated_woke.set,
    )
    mailbox.accept(event)
    mailbox.unsubscribe_content_free_wake(subscription_id)
    mailbox.unsubscribe_content_free_wake(unrelated_subscription_id)

    assert woke.wait(timeout=1)
    assert unrelated_woke.is_set() is False
    assert mailbox.reconcile(recipient.harness_id)[0]["cursor"] == 1


def test_rolled_back_acceptance_can_only_cause_a_false_wake_not_cursor_advance(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"secret": "rolled back"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    woke = threading.Event()
    subscription_id = mailbox.subscribe_content_free_wake(recipient.harness_id, woke.set)
    with pytest.raises(RuntimeError, match="rollback"):
        with store.transaction() as connection:
            mailbox._accept_in_transaction(connection, event)
            raise RuntimeError("rollback")
    mailbox.unsubscribe_content_free_wake(subscription_id)

    assert woke.wait(timeout=1)
    assert mailbox.reconcile(recipient.harness_id) == []


def test_same_key_different_digest_is_security_conflict(store, identity_factory) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    key = f"message-{uuid4()}"
    first = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "one"},
        idempotency_key=key,
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(first)
    second = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "two"},
        idempotency_key=key,
        recipients=(recipient.harness_id,),
    )
    with pytest.raises(IdempotencyConflict, match="different canonical intent"):
        mailbox.accept(second)


def test_terminal_state_cannot_reopen(store, identity_factory, workload_factory) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "short lived"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
        delivery_expires_at=datetime.now(UTC) + timedelta(seconds=30),
    )
    mailbox.accept(event)
    expiry_now = datetime.now(UTC) + timedelta(seconds=60)
    dispatcher, dispatcher_key = workload_factory(
        domain=sender.domain_id,
        role="mailbox_dispatcher",
        recipient_scope=recipient.harness_id,
    )
    detail = {"authoritative_clock": int(expiry_now.timestamp())}
    mailbox.expire_due(
        authoritative_now=expiry_now,
        authorizations={
            (event.event_id, recipient.harness_id): ExpiryAuthorization(
                credential=RegisteredWorkloadCredential(actor=dispatcher, signer=dispatcher_key),
                proof=WorkloadTransitionProof.create(
                    dispatcher_key,
                    actor=dispatcher,
                    event_id=event.event_id,
                    recipient_id=recipient.harness_id,
                    proposed_fact=DeliveryFact.EXPIRED,
                    detail=detail,
                    timestamp=int(expiry_now.timestamp()),
                ),
            )
        },
    )
    with pytest.raises(ConflictError):
        mailbox.transition(
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed=DeliveryFact.RECIPIENT_COMMITTED,
            owner_actor=recipient,
        )


def test_acceptance_receipt_is_owned_by_typed_storage_boundary_not_fabricated_actor(
    store, identity_factory
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "storage boundary owner"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )

    accepted = mailbox.accept(event)
    row = store.fetch_one("SELECT * FROM receipts WHERE receipt_id=?", (accepted["receipt_id"],))
    owner = json.loads(row["owner_actor_json"])
    parsed = Receipt(
        receipt_id=row["receipt_id"],
        event_id=row["event_id"],
        recipient_id=row["recipient_id"],
        fact=row["fact"],
        owner=owner,
        event_digest=row["event_digest"],
        detail=json.loads(row["detail_json"]),
        created_at=datetime.fromtimestamp(row["created_at"], UTC),
    )

    assert isinstance(parsed.owner, AcceptingStorageBoundary)
    assert parsed.owner.kind == "accepting_storage_boundary"
    assert parsed.owner.storage_profile == "local_transactional"
    assert "workload_id" not in owner
    assert "binding_assurance" not in owner


def test_expire_due_fails_closed_without_every_exact_dispatcher_proof(
    store, identity_factory, workload_factory
) -> None:
    sender, _ = identity_factory()
    recipients = [identity_factory()[0], identity_factory()[0]]
    expiry_now = datetime.now(UTC) + timedelta(seconds=2)
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "all expiry proofs are required"},
        idempotency_key=f"message-{uuid4()}",
        recipients=tuple(recipient.harness_id for recipient in recipients),
        delivery_expires_at=expiry_now - timedelta(seconds=1),
    )
    mailbox.accept(event)
    dispatcher, dispatcher_key = workload_factory(
        domain=sender.domain_id,
        role="mailbox_dispatcher",
        recipient_scope="*",
    )
    detail = {"authoritative_clock": int(expiry_now.timestamp())}
    first = recipients[0].harness_id
    one_authorization = {
        (event.event_id, first): ExpiryAuthorization(
            credential=RegisteredWorkloadCredential(actor=dispatcher, signer=dispatcher_key),
            proof=WorkloadTransitionProof.create(
                dispatcher_key,
                actor=dispatcher,
                event_id=event.event_id,
                recipient_id=first,
                proposed_fact=DeliveryFact.EXPIRED,
                detail=detail,
                timestamp=int(expiry_now.timestamp()),
            ),
        )
    }

    with pytest.raises(AuthorizationError, match="exact event and recipient"):
        mailbox.expire_due(authoritative_now=expiry_now, authorizations=one_authorization)

    assert [row["fact"] for row in mailbox.reconcile(first)] == [DeliveryFact.ACCEPTED_LOCAL.value]
    assert [row["fact"] for row in mailbox.reconcile(recipients[1].harness_id)] == [
        DeliveryFact.ACCEPTED_LOCAL.value
    ]


def test_receipt_fact_owner_is_derived_from_exact_recipient_or_fixed_workload(
    store, identity_factory, workload_factory
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    outsider, _ = identity_factory()
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "fact ownership"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(event)
    dispatcher, dispatcher_key = workload_factory(
        domain=sender.domain_id, role="mailbox_dispatcher"
    )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.QUEUED,
        owner_actor=dispatcher,
        workload_proof=WorkloadTransitionProof.create(
            dispatcher_key,
            actor=dispatcher,
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed_fact=DeliveryFact.QUEUED,
        ),
    )
    with pytest.raises(AuthorizationError):
        mailbox.transition(
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed=DeliveryFact.DISPATCH_ATTEMPTED,
            owner_actor=outsider,
        )
    dispatcher_proof = WorkloadTransitionProof.create(
        dispatcher_key,
        actor=dispatcher,
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed_fact=DeliveryFact.DISPATCH_ATTEMPTED,
    )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.DISPATCH_ATTEMPTED,
        owner_actor=dispatcher,
        workload_proof=dispatcher_proof,
    )
    with pytest.raises(AuthorizationError):
        mailbox.transition(
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed=DeliveryFact.RECIPIENT_COMMITTED,
            owner_actor=outsider,
        )
    committed = mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.RECIPIENT_COMMITTED,
        owner_actor=recipient,
    )
    receipt = store.fetch_one("SELECT * FROM receipts WHERE receipt_id=?", (committed["receipt_id"],))
    assert recipient.harness_id in receipt["owner_actor_json"]
    assert outsider.harness_id not in receipt["owner_actor_json"]


def test_recipient_cannot_fabricate_effect_completion_and_remote_facts_fail_closed(
    store, identity_factory, workload_factory
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    mailbox = MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "effect"},
        idempotency_key=f"message-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    mailbox.accept(event)
    dispatcher, dispatcher_key = workload_factory(
        domain=sender.domain_id, role="mailbox_dispatcher"
    )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.QUEUED,
        owner_actor=dispatcher,
        workload_proof=WorkloadTransitionProof.create(
            dispatcher_key,
            actor=dispatcher,
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed_fact=DeliveryFact.QUEUED,
        ),
    )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.DISPATCH_ATTEMPTED,
        owner_actor=dispatcher,
        workload_proof=WorkloadTransitionProof.create(
            dispatcher_key,
            actor=dispatcher,
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed_fact=DeliveryFact.DISPATCH_ATTEMPTED,
        ),
    )
    with pytest.raises(AuthorizationError):
        mailbox.transition(
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed=DeliveryFact.REMOTE_ACCEPTED,
            owner_actor=dispatcher,
            workload_proof=WorkloadTransitionProof.create(
                dispatcher_key,
                actor=dispatcher,
                event_id=event.event_id,
                recipient_id=recipient.harness_id,
                proposed_fact=DeliveryFact.REMOTE_ACCEPTED,
            ),
        )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.RECIPIENT_COMMITTED,
        owner_actor=recipient,
    )
    grant = TaskGrant(
        domain_id=sender.domain_id,
        principal_id=recipient.principal_id,
        harness_id=recipient.harness_id,
        actions=frozenset({"message.process", "effect.execute"}),
        resources=frozenset({f"event:{event.event_id}"}),
        input_sources=frozenset({"mailbox"}),
        output_sinks=frozenset({"receipt"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=2,
        expires_at=datetime.now(UTC) + timedelta(minutes=5),
    )
    with store.transaction() as connection:
        TaskGrantService(store)._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "workload-transition-test"},
        )
    processor, processor_key = workload_factory(
        domain=sender.domain_id,
        role="recipient_processor",
        recipient_scope=recipient.harness_id,
        parent_event_id=event.event_id,
        task_grant_id=grant.grant_id,
    )
    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.PROCESSING,
        owner_actor=processor,
        workload_proof=WorkloadTransitionProof.create(
            processor_key,
            actor=processor,
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed_fact=DeliveryFact.PROCESSING,
        ),
    )
    with pytest.raises(AuthorizationError):
        mailbox.transition(
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed=DeliveryFact.COMPLETED,
            owner_actor=recipient,
        )
    effect, effect_key = workload_factory(
        domain=sender.domain_id,
        role="effect_authority",
        recipient_scope=recipient.harness_id,
        parent_event_id=event.event_id,
        task_grant_id=grant.grant_id,
    )
    result = mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.COMPLETED,
        owner_actor=effect,
        workload_proof=WorkloadTransitionProof.create(
            effect_key,
            actor=effect,
            event_id=event.event_id,
            recipient_id=recipient.harness_id,
            proposed_fact=DeliveryFact.COMPLETED,
        ),
    )
    assert result["fact"] == "completed"


def test_post_revocation_history_policy_bounds_new_acceptance_retention(store, identity_factory) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    mailbox = MailboxService(
        store,
        revocation_policy=RevocationPolicy(accepted_history_max_retention_days=1),
    )
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C0_PUBLIC,
        payload={"synthetic": True},
        idempotency_key=f"retention-ceiling-{uuid4()}",
        recipients=(recipient.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=2),
    )
    with pytest.raises(AuthorizationError, match="post-revocation"):
        mailbox.accept(event)


@pytest.mark.parametrize("recipient_case", ["unknown", "cross_domain", "revoked", "stale_key"])
def test_acceptance_rejects_nonexistent_cross_domain_revoked_or_stale_recipient_before_custody(
    store, identity_factory, recipient_case: str
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory(
        domain="other.example" if recipient_case == "cross_domain" else sender.domain_id
    )
    recipient_id = recipient.harness_id
    if recipient_case == "unknown":
        recipient_id = "unknown-harness-address"
    elif recipient_case == "revoked":
        with store.transaction() as connection:
            connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (recipient_id,))
    elif recipient_case == "stale_key":
        with store.transaction() as connection:
            connection.execute("UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?", (recipient_id,))
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "must not become orphaned custody"},
        idempotency_key=f"invalid-recipient-{uuid4()}",
        recipients=(recipient_id,),
    )
    with pytest.raises(AuthorizationError, match="current enrolled"):
        MailboxService(store).accept(event)
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM recipients")["count"] == 0


def test_acceptance_encrypts_exact_recipient_snapshot_and_offline_is_valid(
    store, identity_factory
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "offline is normal"},
        idempotency_key=f"recipient-snapshot-{uuid4()}",
        recipients=(recipient.harness_id,),
    )
    accepted = MailboxService(store).accept(event)
    assert accepted["fact"] == "accepted_local"
    row = store.fetch_one(
        "SELECT * FROM recipient_address_snapshots WHERE event_id=? AND recipient_id=?",
        (event.event_id, recipient.harness_id),
    )
    assert row is not None
    assert recipient.credential_id not in row["snapshot_encrypted"]
    snapshot = store.cipher.decrypt_json(
        row["snapshot_encrypted"],
        purpose=f"recipient-snapshot:{event.event_id}:{recipient.harness_id}",
    )
    assert snapshot["credential_id"] == recipient.credential_id
    assert snapshot["credential_epoch"] == recipient.credential_epoch
    assert store.fetch_one(
        "SELECT 1 FROM presence_leases WHERE harness_id=?", (recipient.harness_id,)
    ) is None
