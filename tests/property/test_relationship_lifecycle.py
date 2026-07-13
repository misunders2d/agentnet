from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor

import pytest

from agentnet.organization.relationships import RelationshipService
from tests.organization import conftest as organization_fixtures
from tests.organization.test_relationships import (
    accept,
    approval_receipt,
    propose,
    record_policy_exception,
    relationship,
    revocation_evidence,
)

pytest_plugins = ("tests.organization.conftest",)


@pytest.fixture
def governance_store(tmp_path, now, identity_keys):
    yield from organization_fixtures.store.__wrapped__(tmp_path, now, identity_keys)


def test_two_exact_acceptances_have_one_atomic_winner(
    governance_store,
    admin_actor,
    subordinate_actor,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
):
    store = governance_store
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipts = [
        approval_receipt(
            pending,
            principal_id="sub-human",
            signer=relationship_approval_keys["sub-human"],
            verifier=relationship_approval_verifier,
            now=now,
            receipt_id=f"concurrent-owner-consent-{index:04d}",
        )
        for index in range(2)
    ]

    def activate_once(receipt):
        try:
            return accept(service, pending, subordinate_actor, receipt, now).lifecycle_state
        except Exception as exc:
            return exc.__class__.__name__

    with ThreadPoolExecutor(max_workers=2) as pool:
        outcomes = list(pool.map(activate_once, receipts))

    assert outcomes.count("active") == 1
    assert outcomes.count("ConflictError") == 1
    row = store.fetch_one(
        "SELECT state,lifecycle_revision FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )
    assert row["state"] == "active"
    assert row["lifecycle_revision"] == 2
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 1
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_log WHERE record_json LIKE '%relationship_activated%'"
    )["count"] == 1


@pytest.mark.parametrize("_attempt", range(20))
def test_acceptance_and_signed_revocation_race_has_one_versioned_winner(
    governance_store,
    admin_actor,
    subordinate_actor,
    identity_keys,
    signed_command,
    relationship_approval_keys,
    relationship_approval_verifier,
    now,
    _attempt,
):
    store = governance_store
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    receipt = approval_receipt(
        pending,
        principal_id="sub-human",
        signer=relationship_approval_keys["sub-human"],
        verifier=relationship_approval_verifier,
        now=now,
    )
    command, authority = revocation_evidence(
        store,
        service,
        pending,
        subordinate_actor,
        identity_keys["sub-credential"],
        signed_command,
        now,
        reason="concurrent subject exit",
    )

    def activate_once():
        try:
            return accept(service, pending, subordinate_actor, receipt, now).lifecycle_state
        except Exception as exc:
            return exc.__class__.__name__

    def revoke_once():
        try:
            service.revoke(
                pending.relationship_id,
                command=command,
                authority=authority,
                when=now,
            )
            return "revoked"
        except Exception as exc:
            return exc.__class__.__name__

    with ThreadPoolExecutor(max_workers=2) as pool:
        activation = pool.submit(activate_once)
        revocation = pool.submit(revoke_once)
        outcomes = [activation.result(), revocation.result()]

    assert sum(value in {"active", "revoked"} for value in outcomes) == 1
    assert outcomes.count("ConflictError") == 1
    row = store.fetch_one(
        "SELECT state,lifecycle_revision FROM relationship_governance_transactions WHERE relationship_id=?",
        (pending.relationship_id,),
    )
    assert row["state"] in {"active", "revoked"}
    assert row["lifecycle_revision"] == 2
    lineage_epoch = store.fetch_one(
        "SELECT revocation_epoch FROM relationship_governance_lineages"
    )["revocation_epoch"]
    assert lineage_epoch == (1 if row["state"] == "revoked" else 0)
    assert store.verify_audit_chain()[0] is True


@pytest.mark.parametrize("_attempt", range(10))
def test_policy_exception_activation_and_signed_revocation_have_one_atomic_winner(
    governance_store,
    admin_actor,
    subordinate_actor,
    peer_actor,
    identity_keys,
    signed_command,
    relationship_approval_verifier,
    now,
    _attempt,
):
    store = governance_store
    service = RelationshipService(store, approval_verifier=relationship_approval_verifier)
    pending = propose(service, store, relationship(now), admin_actor, now)
    _exception, recorded = record_policy_exception(
        service,
        store,
        pending,
        signer_actor=peer_actor,
        signer_key=identity_keys["peer-credential"],
        signed_command=signed_command,
        now=now,
        policy_exception_id=f"policy-exception-revoke-race-{_attempt}",
    )
    command, authority = revocation_evidence(
        store,
        service,
        pending,
        subordinate_actor,
        identity_keys["sub-credential"],
        signed_command,
        now,
        reason="concurrent subject exit against policy exception",
    )

    def activate_once():
        try:
            return service.activate_with_policy_exception(
                pending.relationship_id,
                policy_exception_id=recorded.policy_exception_id,
                actor=subordinate_actor,
                expected_transaction_digest=pending.transaction_digest,
                expected_relationship_revision=pending.revision,
                expected_lifecycle_revision=pending.lifecycle_revision,
                when=now,
            ).lifecycle_state
        except Exception as exc:
            return exc.__class__.__name__

    def revoke_once():
        try:
            service.revoke(
                pending.relationship_id,
                command=command,
                authority=authority,
                when=now,
            )
            return "revoked"
        except Exception as exc:
            return exc.__class__.__name__

    with ThreadPoolExecutor(max_workers=2) as pool:
        if _attempt % 2:
            futures = (pool.submit(revoke_once), pool.submit(activate_once))
        else:
            futures = (pool.submit(activate_once), pool.submit(revoke_once))
        outcomes = [future.result() for future in futures]

    assert sum(value in {"active", "revoked"} for value in outcomes) == 1
    assert outcomes.count("ConflictError") == 1
    row = store.fetch_one(
        "SELECT state,lifecycle_revision FROM relationship_governance_transactions "
        "WHERE relationship_id=?",
        (pending.relationship_id,),
    )
    exception_row = store.fetch_one(
        "SELECT consumed_at,revoked_at FROM relationship_policy_exceptions "
        "WHERE policy_exception_id=?",
        (recorded.policy_exception_id,),
    )
    activation_intents = store.fetch_one(
        "SELECT COUNT(*) AS count FROM audit_intents "
        "WHERE action='organization.relationship.activate'"
    )["count"]
    assert row["lifecycle_revision"] == 2
    if row["state"] == "active":
        assert exception_row["consumed_at"] is not None
        assert exception_row["revoked_at"] is None
        assert activation_intents == 1
    else:
        assert row["state"] == "revoked"
        assert exception_row["consumed_at"] is None
        assert exception_row["revoked_at"] is not None
        assert activation_intents == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS count FROM relationship_governance_transactions "
        "WHERE state='active'"
    )["count"] == (1 if row["state"] == "active" else 0)
    assert store.verify_audit_chain()[0] is True
