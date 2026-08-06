from __future__ import annotations

import time

import pytest

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.operations.endpoint_lifecycle import (
    EndpointActivationState,
    EndpointLifecycleService,
)
from agentnet.storage.sqlite import SQLiteStore


def _service(store: SQLiteStore, now: int) -> EndpointLifecycleService:
    return EndpointLifecycleService(store, clock=lambda: now)


def test_register_existing_rejects_duplicate_profile_address_binding(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor_a, _ = identity_factory(kind="pi", principal_id="shared-principal")
    actor_b, _ = identity_factory(kind="pi", principal_id="shared-principal")
    service = _service(store, now)

    registered = service.register_existing(
        actor=actor_a,
        harness_kind="pi",
        profile_key="default",
    )
    assert registered.state is EndpointActivationState.ACCESS_READY

    with pytest.raises(ConflictError, match="endpoint binding"):
        service.register_existing(
            actor=actor_b,
            harness_kind="pi",
            profile_key="default",
        )
    with pytest.raises(ConflictError, match="endpoint binding"):
        service.register_existing(
            actor=actor_a,
            harness_kind="pi",
            profile_key="other-profile",
        )


def test_status_resolves_stable_exact_address_or_harness_id(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor, _ = identity_factory(kind="codex")
    service = _service(store, now)
    registered = service.register_existing(
        actor=actor,
        harness_kind="codex",
        profile_key="work",
    )

    assert service.status(endpoint_id=registered.harness_id) == registered
    assert service.status(endpoint_id=registered.endpoint_address) == registered
    with pytest.raises(AuthorizationError, match="unavailable"):
        service.status(endpoint_id="agentnet:corp.example:codex:missing")


def test_reconnect_preserves_generation_process_and_mailbox_cursor_across_reopen(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor, _ = identity_factory(kind="omp")
    service = _service(store, now)
    registered = service.register_existing(
        actor=actor,
        harness_kind="omp",
        profile_key="default",
    )
    requested = service.request_activation(
        actor=actor,
        expected_revision=registered.revision,
    )
    connected = service.record_user_restart(
        actor=actor,
        expected_generation=requested.adapter_generation,
        process_measurement="a" * 64,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE endpoint_lifecycle SET mailbox_cursor=? WHERE domain_id=? AND harness_id=?",
            (37, actor.domain_id, actor.harness_id),
        )

    reopened = SQLiteStore(store.path, store.cipher)
    try:
        restored = _service(reopened, now).status(endpoint_id=actor.harness_id or "")
    finally:
        reopened.close()

    assert restored.state is EndpointActivationState.CONNECTED
    assert restored.adapter_generation == connected.adapter_generation
    assert restored.process_measurement == "a" * 64
    assert restored.mailbox_cursor == 37


def test_registration_activation_and_restart_retries_are_idempotent(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor, _ = identity_factory(kind="claude")
    service = _service(store, now)

    registered = service.register_existing(
        actor=actor,
        harness_kind="claude",
        profile_key="default",
    )
    assert service.register_existing(
        actor=actor,
        harness_kind="claude",
        profile_key="default",
    ) == registered

    requested = service.request_activation(
        actor=actor,
        expected_revision=registered.revision,
    )
    assert service.request_activation(
        actor=actor,
        expected_revision=registered.revision,
    ) == requested

    connected = service.record_user_restart(
        actor=actor,
        expected_generation=requested.adapter_generation,
        process_measurement="b" * 64,
    )
    assert service.record_user_restart(
        actor=actor,
        expected_generation=requested.adapter_generation,
        process_measurement="b" * 64,
    ) == connected


def test_request_activation_rejects_stale_revision(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor, _ = identity_factory(kind="antigravity")
    service = _service(store, now)
    registered = service.register_existing(
        actor=actor,
        harness_kind="antigravity",
        profile_key="default",
    )

    with pytest.raises(ConflictError, match="revision"):
        service.request_activation(
            actor=actor,
            expected_revision=registered.revision + 1,
        )


def test_revoked_or_expired_credential_denies_mutation_and_reconcile_blocks(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor, _ = identity_factory(kind="codex")
    service = _service(store, now)
    registered = service.register_existing(
        actor=actor,
        harness_kind="codex",
        profile_key="default",
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked',expires_at=? WHERE credential_id=?",
            (now, actor.credential_id),
        )

    with pytest.raises(AuthenticationError, match="unavailable"):
        service.request_activation(actor=actor, expected_revision=registered.revision)
    blocked = service.reconcile(endpoint_id=actor.harness_id or "")
    assert blocked.state is EndpointActivationState.BLOCKED
    assert blocked.adapter_generation == registered.adapter_generation + 1
    assert service.reconcile(endpoint_id=registered.endpoint_address) == blocked


def test_sibling_harness_cannot_observe_or_transition_exact_endpoint(
    store: SQLiteStore,
    identity_factory,
) -> None:
    now = int(time.time())
    actor_a, _ = identity_factory(kind="pi", principal_id="same-human")
    actor_b, _ = identity_factory(kind="pi", principal_id="same-human")
    service = _service(store, now)
    status_a = service.register_existing(
        actor=actor_a,
        harness_kind="pi",
        profile_key="a",
    )
    status_b = service.register_existing(
        actor=actor_b,
        harness_kind="pi",
        profile_key="b",
    )

    requested_a = service.request_activation(
        actor=actor_a,
        expected_revision=status_a.revision,
    )
    with pytest.raises(ConflictError, match="restart"):
        service.record_user_restart(
            actor=actor_b,
            expected_generation=requested_a.adapter_generation,
            process_measurement="c" * 64,
        )

    assert service.status(endpoint_id=actor_a.harness_id or "").state is EndpointActivationState.RESTART_REQUIRED
    assert service.status(endpoint_id=actor_b.harness_id or "") == status_b
