from __future__ import annotations

import time

import pytest

from agentnet.errors import AuthenticationError, AuthorizationError, ReplayError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.workload import WorkloadRegistry, WorkloadTransitionProof
from agentnet.protocol.models import DeliveryFact


def verify(store, actor, proof, *, role: str = "mailbox_dispatcher") -> None:
    with store.transaction() as connection:
        WorkloadRegistry(store).verify_transition(
            connection,
            actor=actor,
            proof=proof,
            allowed_roles={role},
            event_id="event-workload-proof",
            recipient_id="recipient-workload-proof",
            proposed_fact=DeliveryFact.DISPATCH_ATTEMPTED,
            detail=None,
            now=int(time.time()),
        )


def proof(key, actor, *, timestamp: int | None = None):
    return WorkloadTransitionProof.create(
        key,
        actor=actor,
        event_id="event-workload-proof",
        recipient_id="recipient-workload-proof",
        proposed_fact=DeliveryFact.DISPATCH_ATTEMPTED,
        timestamp=timestamp,
    )


def test_magic_workload_id_without_registered_proof_has_no_fact_authority(
    store, identity_factory
) -> None:
    identity_factory()
    forged = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="corp.example",
        workload_id="mailbox.dispatcher",
        binding_assurance="internal_process",
    )
    with pytest.raises(AuthorizationError, match="authenticated workload"):
        verify(store, forged, None)


def test_registered_transition_proof_is_one_time_and_restart_persistent(
    store, identity_factory, workload_factory
) -> None:
    owner, _ = identity_factory()
    actor, key = workload_factory(domain=owner.domain_id, role="mailbox_dispatcher")
    signed = proof(key, actor)
    verify(store, actor, signed)
    with pytest.raises(ReplayError, match="already consumed"):
        verify(store, actor, signed)


@pytest.mark.parametrize(
    "mutation",
    ["process", "session", "credential_epoch", "revocation_epoch", "role", "stale_proof"],
)
def test_process_session_epoch_role_and_freshness_are_exact(
    store, identity_factory, workload_factory, mutation: str
) -> None:
    owner, _ = identity_factory()
    actor, key = workload_factory(domain=owner.domain_id, role="mailbox_dispatcher")
    changed = actor
    timestamp = None
    expected_error = (AuthorizationError, AuthenticationError)
    if mutation == "process":
        changed = actor.model_copy(update={"workload_process_id": actor.workload_process_id + 1})
    elif mutation == "session":
        changed = actor.model_copy(update={"workload_session_id": "wrong-session-00000001"})
    elif mutation == "credential_epoch":
        changed = actor.model_copy(update={"credential_epoch": actor.credential_epoch + 1})
    elif mutation == "revocation_epoch":
        changed = actor.model_copy(
            update={"workload_revocation_epoch": actor.workload_revocation_epoch + 1}
        )
    elif mutation == "role":
        changed = actor.model_copy(update={"workload_role": "security_authority"})
    else:
        timestamp = int(time.time()) - 301
    with pytest.raises(expected_error):
        verify(store, changed, proof(key, changed, timestamp=timestamp))


@pytest.mark.parametrize("revocation", ["registration", "domain"])
def test_registration_and_domain_revocation_block_next_transition(
    store, identity_factory, workload_factory, revocation: str
) -> None:
    owner, _ = identity_factory()
    actor, key = workload_factory(domain=owner.domain_id, role="mailbox_dispatcher")
    with store.transaction() as connection:
        if revocation == "registration":
            connection.execute(
                "UPDATE workload_registrations SET status='revoked',revocation_epoch=revocation_epoch+1 WHERE registration_id=?",
                (actor.workload_registration_id,),
            )
        else:
            connection.execute(
                "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
                (actor.domain_id,),
            )
    with pytest.raises(AuthorizationError, match="not current"):
        verify(store, actor, proof(key, actor))


def test_sibling_registered_process_cannot_reuse_another_process_proof(
    store, identity_factory, workload_factory
) -> None:
    owner, _ = identity_factory()
    first, first_key = workload_factory(domain=owner.domain_id, role="mailbox_dispatcher")
    sibling, _sibling_key = workload_factory(domain=owner.domain_id, role="mailbox_dispatcher")
    with pytest.raises((AuthorizationError, AuthenticationError)):
        verify(store, sibling, proof(first_key, first))


def test_processing_role_without_parent_event_and_grant_is_non_authoritative(
    store, identity_factory, workload_factory
) -> None:
    owner, _ = identity_factory()
    processor, key = workload_factory(
        domain=owner.domain_id,
        role="recipient_processor",
        recipient_scope="recipient-workload-proof",
    )
    with pytest.raises(AuthorizationError, match="parent event"):
        verify(store, processor, proof(key, processor), role="recipient_processor")
