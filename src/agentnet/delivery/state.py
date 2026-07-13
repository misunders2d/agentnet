"""Declarative per-recipient delivery state machine."""

from __future__ import annotations

from agentnet.errors import ConflictError
from agentnet.protocol.models import DeliveryFact


ALLOWED_TRANSITIONS: dict[DeliveryFact, frozenset[DeliveryFact]] = {
    DeliveryFact.CREATED_LOCAL: frozenset({DeliveryFact.SUBMITTED, DeliveryFact.EXPIRED}),
    DeliveryFact.SUBMITTED: frozenset(
        {
            DeliveryFact.REJECTED_BEFORE_ACCEPT,
            DeliveryFact.AUTHORIZATION_HOLD,
            DeliveryFact.ACCEPTED_LOCAL,
            DeliveryFact.ACCEPTED_DURABLE,
        }
    ),
    DeliveryFact.AUTHORIZATION_HOLD: frozenset(
        {DeliveryFact.ACCEPTED_LOCAL, DeliveryFact.ACCEPTED_DURABLE, DeliveryFact.REJECTED_BEFORE_ACCEPT, DeliveryFact.EXPIRED}
    ),
    DeliveryFact.ACCEPTED_LOCAL: frozenset(
        {
            DeliveryFact.QUEUED,
            DeliveryFact.CONFLICT_PENDING,
            DeliveryFact.RECIPIENT_COMMITTED,
            DeliveryFact.CANCEL_REQUESTED,
            DeliveryFact.EXPIRED,
            DeliveryFact.QUARANTINED,
        }
    ),
    DeliveryFact.ACCEPTED_DURABLE: frozenset(
        {
            DeliveryFact.QUEUED,
            DeliveryFact.CONFLICT_PENDING,
            DeliveryFact.RECIPIENT_COMMITTED,
            DeliveryFact.CANCEL_REQUESTED,
            DeliveryFact.EXPIRED,
            DeliveryFact.QUARANTINED,
        }
    ),
    DeliveryFact.ACCEPTED_QUEUED: frozenset(
        {
            DeliveryFact.QUEUED,
            DeliveryFact.CONFLICT_PENDING,
            DeliveryFact.RECIPIENT_COMMITTED,
            DeliveryFact.CANCEL_REQUESTED,
            DeliveryFact.EXPIRED,
            DeliveryFact.QUARANTINED,
        }
    ),
    DeliveryFact.PENDING_HUMAN: frozenset(
        {DeliveryFact.ACCEPTED_QUEUED, DeliveryFact.REJECTED_BEFORE_ACCEPT, DeliveryFact.EXPIRED}
    ),
    DeliveryFact.QUEUED: frozenset(
        {
            DeliveryFact.CONFLICT_PENDING,
            DeliveryFact.DISPATCH_ATTEMPTED,
            DeliveryFact.RETRY_SCHEDULED,
            DeliveryFact.CANCEL_REQUESTED,
            DeliveryFact.EXPIRED,
            DeliveryFact.DEAD_LETTERED,
            DeliveryFact.QUARANTINED,
        }
    ),
    DeliveryFact.RETRY_SCHEDULED: frozenset(
        {DeliveryFact.DISPATCH_ATTEMPTED, DeliveryFact.CANCEL_REQUESTED, DeliveryFact.EXPIRED, DeliveryFact.DEAD_LETTERED}
    ),
    DeliveryFact.DISPATCH_ATTEMPTED: frozenset(
        {
            DeliveryFact.REMOTE_ACCEPTED,
            DeliveryFact.REMOTE_REJECTED,
            DeliveryFact.REMOTE_DELAYED,
            DeliveryFact.RECIPIENT_COMMITTED,
            DeliveryFact.RETRY_SCHEDULED,
            DeliveryFact.FAILED_RETRYABLE,
            DeliveryFact.FAILED_TERMINAL,
            DeliveryFact.EXPIRED,
        }
    ),
    DeliveryFact.REMOTE_ACCEPTED: frozenset(
        {DeliveryFact.RECIPIENT_COMMITTED, DeliveryFact.RETRY_SCHEDULED, DeliveryFact.EXPIRED, DeliveryFact.QUARANTINED}
    ),
    DeliveryFact.REMOTE_DELAYED: frozenset({DeliveryFact.RETRY_SCHEDULED, DeliveryFact.EXPIRED}),
    DeliveryFact.RECIPIENT_COMMITTED: frozenset(
        {DeliveryFact.PRESENTED, DeliveryFact.PROCESSING, DeliveryFact.EXPIRED, DeliveryFact.CANCEL_REQUESTED}
    ),
    DeliveryFact.PRESENTED: frozenset({DeliveryFact.PROCESSING, DeliveryFact.CANCEL_REQUESTED, DeliveryFact.EXPIRED}),
    DeliveryFact.PROCESSING: frozenset(
        {
            DeliveryFact.EFFECT_PREPARED,
            DeliveryFact.COMPLETED,
            DeliveryFact.FAILED_RETRYABLE,
            DeliveryFact.FAILED_TERMINAL,
            DeliveryFact.CANCEL_REQUESTED,
            DeliveryFact.QUARANTINED,
        }
    ),
    DeliveryFact.EFFECT_PREPARED: frozenset(
        {
            DeliveryFact.COMPLETED,
            DeliveryFact.EFFECT_UNKNOWN,
            DeliveryFact.CANCELED,
            DeliveryFact.TOO_LATE,
            DeliveryFact.FAILED_TERMINAL,
        }
    ),
    DeliveryFact.EFFECT_UNKNOWN: frozenset(
        {DeliveryFact.RECONCILING, DeliveryFact.COMPLETED, DeliveryFact.FAILED_TERMINAL, DeliveryFact.ADJUDICATION_REQUIRED}
    ),
    DeliveryFact.RECONCILING: frozenset(
        {DeliveryFact.COMPLETED, DeliveryFact.COMPENSATED, DeliveryFact.FAILED_TERMINAL, DeliveryFact.ADJUDICATION_REQUIRED}
    ),
    DeliveryFact.CANCEL_REQUESTED: frozenset(
        {DeliveryFact.CANCELED, DeliveryFact.TOO_LATE, DeliveryFact.EFFECT_UNKNOWN, DeliveryFact.ADJUDICATION_REQUIRED}
    ),
    DeliveryFact.FAILED_RETRYABLE: frozenset(
        {DeliveryFact.RETRY_SCHEDULED, DeliveryFact.EFFECT_UNKNOWN, DeliveryFact.FAILED_TERMINAL}
    ),
    DeliveryFact.CONFLICT_PENDING: frozenset(
        {DeliveryFact.QUEUED, DeliveryFact.REJECTED_BEFORE_ACCEPT, DeliveryFact.ADJUDICATION_REQUIRED}
    ),
    DeliveryFact.QUARANTINED: frozenset(
        {DeliveryFact.QUEUED, DeliveryFact.REJECTED_BEFORE_ACCEPT, DeliveryFact.DEAD_LETTERED, DeliveryFact.ADJUDICATION_REQUIRED}
    ),
}

TERMINAL_FACTS = frozenset(
    {
        DeliveryFact.REJECTED_BEFORE_ACCEPT,
        DeliveryFact.REMOTE_REJECTED,
        DeliveryFact.COMPLETED,
        DeliveryFact.FAILED_TERMINAL,
        DeliveryFact.EXPIRED,
        DeliveryFact.CANCELED,
        DeliveryFact.TOO_LATE,
        DeliveryFact.COMPENSATED,
        DeliveryFact.DEAD_LETTERED,
    }
)


def require_transition(current: DeliveryFact, proposed: DeliveryFact) -> None:
    if current == proposed:
        return
    if current in TERMINAL_FACTS or proposed not in ALLOWED_TRANSITIONS.get(current, frozenset()):
        raise ConflictError(f"illegal delivery transition {current.value} -> {proposed.value}")


def partial_delivery(facts: list[DeliveryFact]) -> bool:
    if not facts:
        return False
    delivered = {DeliveryFact.RECIPIENT_COMMITTED, DeliveryFact.PRESENTED, DeliveryFact.PROCESSING, DeliveryFact.COMPLETED}
    return any(fact in delivered for fact in facts) and not all(fact in delivered for fact in facts)
