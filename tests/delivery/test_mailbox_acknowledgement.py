from __future__ import annotations

import json
import threading
from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScope,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.protocol.models import Classification, DeliveryFact, EventType


def _mailbox_scope(store, *, owner, recipient) -> tuple[CollaborationScopeService, CollaborationScope]:
    scopes = CollaborationScopeService(store)
    policy = LocalConformancePolicyEngine(store)
    domain = store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope:delivery-acknowledgement:{uuid4()}",
        scope_kind="direct",
        member_harness_ids=tuple(sorted((owner.harness_id, recipient.harness_id))),
        allowed_actions=("message.acknowledge", "message.read", "message.send"),
        allowed_resource_prefixes=("conversation:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=int(domain["policy_revision"]),
        domain_revocation_epoch=int(domain["revocation_epoch"]),
    )
    resource = f"scope:{proposal.scope_id}"
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=owner.domain_id,
            principal_id=owner.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=resource,
            revision=proposal.policy_revision,
        )
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=owner,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=resource,
            policy_revision=proposal.policy_revision,
            context=scopes.issuance_request(actor=owner, proposal=proposal),
        )
    )
    scope = scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=owner,
            policy_decision_id=decision.decision_id,
        ),
    )
    return scopes, scope


def _event(sender, recipient, scope, *, delivery_expires_at=None):
    return new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={
            "text": "acknowledge exact custody",
            "authorization_context": scope.authorization_context(),
        },
        idempotency_key=f"mailbox-ack-{uuid4()}",
        recipients=(recipient.harness_id,),
        delivery_expires_at=delivery_expires_at,
        policy_revision=scope.policy_revision,
    )


def _count(store, table: str) -> int:
    return int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])


def test_exact_recipient_acknowledgement_is_single_write_and_never_downgrades(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    scopes, scope = _mailbox_scope(store, owner=sender, recipient=recipient)
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    event = _event(sender, recipient, scope)
    accepted = mailbox.accept(event)

    first = mailbox.acknowledge(
        event_id=event.event_id,
        collaboration_scope_id=scope.scope_id,
        recipient_id=recipient.harness_id,
        envelope_digest_value=accepted["envelope_digest"],
        owner_actor=recipient,
    )
    receipt_count = _count(store, "receipts")
    audit_count = _count(store, "audit_log")

    assert first == {
        "schema": "agentnet.mailbox-acknowledgement.v1",
        "event_id": event.event_id,
        "recipient_id": recipient.harness_id,
        "fact": "recipient_committed",
        "current_fact": "recipient_committed",
        "duplicate": False,
        "receipt_id": first["receipt_id"],
        "envelope_digest": accepted["envelope_digest"],
        "audit_hash": first["audit_hash"],
    }
    receipt = store.fetch_one(
        "SELECT * FROM receipts WHERE receipt_id=?",
        (first["receipt_id"],),
    )
    assert json.loads(receipt["detail_json"]) == {
        "acknowledgement": "durable_recipient_custody"
    }
    assert json.loads(receipt["owner_actor_json"])["harness_id"] == recipient.harness_id

    duplicate = MailboxService(
        store,
        collaboration_scopes=scopes,
    ).acknowledge(
        event_id=event.event_id,
        collaboration_scope_id=scope.scope_id,
        recipient_id=recipient.harness_id,
        envelope_digest_value=accepted["envelope_digest"],
        owner_actor=recipient,
    )
    assert duplicate["duplicate"] is True
    assert duplicate["receipt_id"] == first["receipt_id"]
    assert duplicate["current_fact"] == "recipient_committed"
    assert _count(store, "receipts") == receipt_count
    assert _count(store, "audit_log") == audit_count

    mailbox.transition(
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed=DeliveryFact.PRESENTED,
        owner_actor=recipient,
    )
    advanced_retry = mailbox.acknowledge(
        event_id=event.event_id,
        collaboration_scope_id=scope.scope_id,
        recipient_id=recipient.harness_id,
        envelope_digest_value=accepted["envelope_digest"],
        owner_actor=recipient,
    )
    assert advanced_retry["duplicate"] is True
    assert advanced_retry["receipt_id"] == first["receipt_id"]
    assert advanced_retry["current_fact"] == "presented"
    assert store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (event.event_id, recipient.harness_id),
    )["current_fact"] == "presented"


def test_concurrent_acknowledgements_converge_on_one_receipt(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    scopes, scope = _mailbox_scope(store, owner=sender, recipient=recipient)
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    event = _event(sender, recipient, scope)
    accepted = mailbox.accept(event)
    baseline_receipts = _count(store, "receipts")
    barrier = threading.Barrier(8)
    results: list[dict[str, object]] = []
    errors: list[BaseException] = []

    def acknowledge() -> None:
        try:
            barrier.wait(timeout=5)
            results.append(
                MailboxService(
                    store,
                    collaboration_scopes=scopes,
                ).acknowledge(
                    event_id=event.event_id,
                    collaboration_scope_id=scope.scope_id,
                    recipient_id=recipient.harness_id,
                    envelope_digest_value=accepted["envelope_digest"],
                    owner_actor=recipient,
                )
            )
        except BaseException as exc:  # pragma: no cover - asserted below
            errors.append(exc)

    threads = [threading.Thread(target=acknowledge) for _ in range(8)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=10)

    assert not errors
    assert len(results) == 8
    assert sum(result["duplicate"] is False for result in results) == 1
    assert {result["receipt_id"] for result in results} == {results[0]["receipt_id"]}
    assert _count(store, "receipts") == baseline_receipts + 1


def test_acknowledgement_rejects_wrong_recipient_digest_and_revoked_credential(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    outsider, _ = identity_factory(kind="claude")
    scopes, scope = _mailbox_scope(store, owner=sender, recipient=recipient)
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    event = _event(sender, recipient, scope)
    accepted = mailbox.accept(event)

    with pytest.raises(AuthorizationError, match="exact authenticated recipient"):
        mailbox.acknowledge(
            event_id=event.event_id,
            collaboration_scope_id=scope.scope_id,
            recipient_id=recipient.harness_id,
            envelope_digest_value=accepted["envelope_digest"],
            owner_actor=outsider,
        )
    with pytest.raises(AuthorizationError, match="not visible"):
        mailbox.acknowledge(
            event_id=event.event_id,
            collaboration_scope_id=scope.scope_id,
            recipient_id=recipient.harness_id,
            envelope_digest_value="0" * 64,
            owner_actor=recipient,
        )

    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id=?",
            (recipient.credential_id,),
        )
    with pytest.raises(AuthorizationError, match="credential_not_active"):
        mailbox.acknowledge(
            event_id=event.event_id,
            collaboration_scope_id=scope.scope_id,
            recipient_id=recipient.harness_id,
            envelope_digest_value=accepted["envelope_digest"],
            owner_actor=recipient,
        )
    assert store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (event.event_id, recipient.harness_id),
    )["current_fact"] == "accepted_local"


def test_existing_receipt_with_backward_state_fails_closed_as_inconsistent(
    store,
    identity_factory,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    scopes, scope = _mailbox_scope(store, owner=sender, recipient=recipient)
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    event = _event(sender, recipient, scope)
    accepted = mailbox.accept(event)
    mailbox.acknowledge(
        event_id=event.event_id,
        collaboration_scope_id=scope.scope_id,
        recipient_id=recipient.harness_id,
        envelope_digest_value=accepted["envelope_digest"],
        owner_actor=recipient,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE recipients SET current_fact='accepted_local' WHERE event_id=? AND recipient_id=?",
            (event.event_id, recipient.harness_id),
        )

    with pytest.raises(ConflictError, match="history is inconsistent"):
        mailbox.acknowledge(
            event_id=event.event_id,
            collaboration_scope_id=scope.scope_id,
            recipient_id=recipient.harness_id,
            envelope_digest_value=accepted["envelope_digest"],
            owner_actor=recipient,
        )


def test_late_acknowledgement_cannot_reopen_expired_delivery(
    store,
    identity_factory,
    monkeypatch,
) -> None:
    sender, _ = identity_factory(kind="codex")
    recipient, _ = identity_factory(kind="pi")
    expiry = datetime.now(UTC) + timedelta(minutes=1)
    scopes, scope = _mailbox_scope(store, owner=sender, recipient=recipient)
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    event = _event(sender, recipient, scope, delivery_expires_at=expiry)
    accepted = mailbox.accept(event)
    monkeypatch.setattr("agentnet.mailbox.service.time.time", lambda: expiry.timestamp())

    with pytest.raises(ConflictError, match="no longer legal"):
        mailbox.acknowledge(
            event_id=event.event_id,
            collaboration_scope_id=scope.scope_id,
            recipient_id=recipient.harness_id,
            envelope_digest_value=accepted["envelope_digest"],
            owner_actor=recipient,
        )
    assert store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (event.event_id, recipient.harness_id),
    )["current_fact"] == "accepted_local"
