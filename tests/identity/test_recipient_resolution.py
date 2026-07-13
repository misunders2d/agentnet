from __future__ import annotations

import time

import pytest

from agentnet.errors import AuthorizationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.recipients import RecipientResolver


def resolve(store, actor, recipient_id: str):
    with store.transaction(immediate=False) as connection:
        return RecipientResolver.resolve_in_transaction(
            connection,
            event_actor=actor,
            event_domain_id=actor.domain_id,
            recipient_id=recipient_id,
            now=int(time.time()),
        )


def test_active_offline_recipient_resolves_without_presence(store, identity_factory) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    snapshot = resolve(store, sender, recipient.harness_id)
    assert snapshot["harness_id"] == recipient.harness_id
    assert snapshot["owner_id"] == recipient.principal_id
    assert store.fetch_one(
        "SELECT 1 FROM presence_leases WHERE harness_id=?", (recipient.harness_id,)
    ) is None


@pytest.mark.parametrize("mutation", ["unknown", "revoked", "expired_key", "stale_epoch", "cross_domain"])
def test_unknown_revoked_cross_domain_and_stale_recipient_fail_closed(
    store, identity_factory, mutation: str
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    recipient_id = recipient.harness_id
    if mutation == "unknown":
        recipient_id = "unknown-enrolled-looking-harness"
    elif mutation == "revoked":
        with store.transaction() as connection:
            connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (recipient_id,))
    elif mutation == "expired_key":
        with store.transaction() as connection:
            connection.execute("UPDATE credentials SET expires_at=? WHERE credential_id=?", (1, recipient.credential_id))
    elif mutation == "stale_epoch":
        with store.transaction() as connection:
            connection.execute("UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?", (recipient_id,))
    else:
        other, _ = identity_factory(domain="other.example")
        recipient_id = other.harness_id
    with pytest.raises(AuthorizationError, match="current enrolled"):
        resolve(store, sender, recipient_id)


def test_deterministic_only_recipient_is_restricted_to_explicit_synthetic_lane(
    store, identity_factory
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET status='deterministic_only' WHERE harness_id=?",
            (recipient.harness_id,),
        )
    with pytest.raises(AuthorizationError):
        resolve(store, sender, recipient.harness_id)
    synthetic = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id=sender.domain_id,
        workload_id="synthetic-lab-recipient-test",
        binding_assurance="synthetic_lab",
    )
    assert resolve(store, synthetic, recipient.harness_id)["harness_id"] == recipient.harness_id
