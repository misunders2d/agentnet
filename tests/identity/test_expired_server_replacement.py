from __future__ import annotations

from uuid import uuid4

import pytest

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.expired_server_replacement import (
    EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE,
    ExpiredServerReplacementRequest,
    ExpiredServerReplacementService,
)
from agentnet.security.signatures import P256KeyPair


def _request(actor, key: P256KeyPair, *, expires_at: int, request_id: str | None = None):
    request_id = request_id or str(uuid4())
    fields = ExpiredServerReplacementRequest.possession_fields(
        request_id=request_id,
        actor=actor,
        setup_request_digest="a" * 64,
        expected_config_digest="b" * 64,
        expected_expires_at=expires_at,
    )
    return ExpiredServerReplacementRequest(
        request_id=request_id,
        setup_request_digest="a" * 64,
        expected_config_digest="b" * 64,
        expected_expires_at=expires_at,
        possession_signature=key.sign(EXPIRED_SERVER_REPLACEMENT_POP_PURPOSE, fields),
    )


def _expire(store, actor, *, expires_at: int) -> None:
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (expires_at - 600, expires_at, actor.credential_id),
        )


def test_recent_expiry_replacement_preserves_harness_and_key_and_replays(store, identity_factory):
    actor, key = identity_factory(binding_assurance="hardware_bound")
    sibling, _ = identity_factory(binding_assurance="hardware_bound")
    now = 1_900_000_000
    _expire(store, actor, expires_at=now - 60)
    request = _request(actor, key, expires_at=now - 60)
    service = ExpiredServerReplacementService(store, clock=lambda: now)

    result = service.replace(actor=actor, request=request)
    replay = service.replace(actor=actor, request=request)

    assert replay == result
    assert result.credential_epoch == actor.credential_epoch + 1
    assert result.expires_at == now + 86_400
    assert dict(
        store.fetch_one(
            "SELECT status,epoch,key_id FROM credentials WHERE credential_id=?",
            (actor.credential_id,),
        )
    ) == {"status": "retired", "epoch": actor.credential_epoch, "key_id": key.thumbprint}
    assert dict(
        store.fetch_one(
            "SELECT status,epoch,key_id,harness_id FROM credentials WHERE credential_id=?",
            (result.credential_id,),
        )
    ) == {
        "status": "active",
        "epoch": actor.credential_epoch + 1,
        "key_id": key.thumbprint,
        "harness_id": actor.harness_id,
    }
    assert store.fetch_one(
        "SELECT credential_epoch FROM harnesses WHERE harness_id=?", (actor.harness_id,)
    )["credential_epoch"] == actor.credential_epoch + 1
    assert store.fetch_one(
        "SELECT credential_epoch FROM harnesses WHERE harness_id=?", (sibling.harness_id,)
    )["credential_epoch"] == sibling.credential_epoch
    assert store.fetch_one(
        "SELECT COUNT(*) AS n FROM expired_server_credential_replacements"
    )["n"] == 1


def test_replacement_rejects_current_too_old_and_lab_credentials(store, identity_factory):
    now = 1_900_000_000
    current, current_key = identity_factory(binding_assurance="hardware_bound")
    current_expires_at = int(
        store.fetch_one(
            "SELECT expires_at FROM credentials WHERE credential_id=?",
            (current.credential_id,),
        )["expires_at"]
    )
    with pytest.raises(ValidationError, match="refuses a current"):
        ExpiredServerReplacementService(
            store, clock=lambda: current_expires_at - 1
        ).replace(
            actor=current,
            request=_request(current, current_key, expires_at=current_expires_at),
        )

    old, old_key = identity_factory(binding_assurance="os_bound")
    _expire(store, old, expires_at=now - 86_401)
    with pytest.raises(AuthenticationError, match="outside the replacement window"):
        ExpiredServerReplacementService(store, clock=lambda: now).replace(
            actor=old, request=_request(old, old_key, expires_at=now - 86_401)
        )

    lab, lab_key = identity_factory(binding_assurance="lab")
    _expire(store, lab, expires_at=now - 1)
    with pytest.raises(AuthenticationError, match="non-lab"):
        ExpiredServerReplacementService(store, clock=lambda: now).replace(
            actor=lab, request=_request(lab, lab_key, expires_at=now - 1)
        )


def test_replacement_cannot_chain_a_second_transition_on_same_harness(
    store,
    identity_factory,
):
    actor, key = identity_factory(binding_assurance="hardware_bound")
    now = 1_900_000_000
    _expire(store, actor, expires_at=now - 1)
    first = ExpiredServerReplacementService(store, clock=lambda: now).replace(
        actor=actor,
        request=_request(actor, key, expires_at=now - 1),
    )
    successor = actor.model_copy(
        update={
            "credential_id": first.credential_id,
            "credential_epoch": first.credential_epoch,
        }
    )
    second_now = first.expires_at + 1
    _expire(store, successor, expires_at=first.expires_at)

    with pytest.raises(AuthorizationError, match="one transition per harness"):
        ExpiredServerReplacementService(store, clock=lambda: second_now).replace(
            actor=successor,
            request=_request(successor, key, expires_at=first.expires_at),
        )


def test_replacement_rejects_wrong_key_and_changed_validity(store, identity_factory):
    actor, key = identity_factory(binding_assurance="hardware_bound")
    now = 1_900_000_000
    _expire(store, actor, expires_at=now - 5)
    wrong = _request(actor, P256KeyPair.generate(), expires_at=now - 5)
    with pytest.raises(AuthenticationError, match="signature verification failed"):
        ExpiredServerReplacementService(store, clock=lambda: now).replace(
            actor=actor, request=wrong
        )

    with pytest.raises(ConflictError, match="validity changed"):
        ExpiredServerReplacementService(store, clock=lambda: now).replace(
            actor=actor, request=_request(actor, key, expires_at=now - 6)
        )
