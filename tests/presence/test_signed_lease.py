from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.presence.service import PresenceService
from agentnet.protocol.models import PresenceLease


def signed_lease(actor, key, *, issued_at: datetime) -> tuple[PresenceLease, str]:
    lease = PresenceLease(
        harness_id=actor.harness_id,
        domain_id=actor.domain_id,
        issued_at=issued_at,
        expires_at=issued_at + timedelta(seconds=60),
        capability_hints=frozenset({"background_queue"}),
    )
    return lease, key.sign("agentnet.presence.lease.v1", lease.model_dump(mode="json"))


def test_presence_requires_current_exact_harness_signature(store, identity_factory) -> None:
    actor, key = identity_factory()
    other, other_key = identity_factory()
    service = PresenceService(store)
    issued = datetime.now(UTC)
    lease, signature = signed_lease(actor, key, issued_at=issued)
    service.update(lease, actor=actor, signature=signature)
    assert service.state(actor.harness_id) == "live"

    replay, replay_signature = signed_lease(actor, key, issued_at=issued)
    with pytest.raises(ConflictError):
        service.update(replay, actor=actor, signature=replay_signature)

    advanced, _ = signed_lease(actor, key, issued_at=issued + timedelta(seconds=1))
    bad_signature = other_key.sign("agentnet.presence.lease.v1", advanced.model_dump(mode="json"))
    with pytest.raises(AuthenticationError):
        service.update(advanced, actor=actor, signature=bad_signature)

    with pytest.raises(AuthorizationError):
        service.update(lease, actor=other, signature=signature)

    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (actor.harness_id,))
    revoked, revoked_signature = signed_lease(actor, key, issued_at=issued + timedelta(seconds=2))
    with pytest.raises(AuthorizationError):
        service.update(revoked, actor=actor, signature=revoked_signature)


def test_presence_exposes_live_recent_stale_unknown_with_bounded_freshness(
    store,
    identity_factory,
    monkeypatch,
) -> None:
    actor, key = identity_factory()
    base = int(datetime.now(UTC).timestamp())
    monkeypatch.setattr("agentnet.presence.service.time.time", lambda: base)
    service = PresenceService(store, max_ttl_seconds=120, max_clock_skew_seconds=30)
    assert service.state("never-enrolled") == "unknown"
    lease, signature = signed_lease(actor, key, issued_at=datetime.fromtimestamp(base, UTC))
    service.update(lease, actor=actor, signature=signature)
    assert service.state(actor.harness_id) == "live"

    monkeypatch.setattr("agentnet.presence.service.time.time", lambda: base + 61)
    assert service.state(actor.harness_id, recent_window_seconds=300) == "recent"
    monkeypatch.setattr("agentnet.presence.service.time.time", lambda: base + 400)
    assert service.state(actor.harness_id, recent_window_seconds=300) == "stale"

    overlong = lease.model_copy(update={"expires_at": lease.issued_at + timedelta(seconds=121)})
    overlong_signature = key.sign("agentnet.presence.lease.v1", overlong.model_dump(mode="json"))
    monkeypatch.setattr("agentnet.presence.service.time.time", lambda: base)
    with pytest.raises(ValidationError, match="lifetime"):
        service.update(overlong, actor=actor, signature=overlong_signature)
