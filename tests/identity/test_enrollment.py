from __future__ import annotations

import json

import pytest

from agentnet.errors import AuthenticationError, GateBlocked, ReplayError, ValidationError
from agentnet.identity.enrollment import EnrollmentService
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.policy_defaults import EnrollmentApprovalPolicy
from agentnet.security.signatures import P256KeyPair, canonical_json


def test_exact_transcript_and_atomic_binding(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    challenge = identity_stack.begin(key)

    assert canonical_json(challenge.signed_fields()) == challenge.canonical_transaction
    assert challenge.signed_fields()["schema"] == "agentnet.enrollment.challenge.v1"
    assert challenge.signed_fields()["candidate_key"]["thumbprint"] == key.thumbprint
    assert challenge.signed_fields()["harness"]["binding_assurance"] == "lab"
    assert challenge.signed_fields()["harness"]["requested_capabilities"] == []

    result = identity_stack.complete(key, challenge)
    assert result.actor.principal_id == result.principal_id
    assert result.actor.harness_id == result.harness_id
    assert result.actor.credential_id == result.credential_id
    assert result.harness_status == "deterministic_only"

    challenge_row = identity_stack.store.fetch_one(
        "SELECT * FROM enrollment_challenges WHERE challenge_id=?", (challenge.challenge_id,)
    )
    assert challenge_row is not None and challenge_row["consumed_at"] == identity_stack.clock()
    assert identity_stack.store.fetch_one(
        "SELECT principal_id FROM harnesses WHERE harness_id=?", (result.harness_id,)
    )["principal_id"] == result.principal_id
    assert identity_stack.store.fetch_one(
        "SELECT harness_id FROM credentials WHERE credential_id=?", (result.credential_id,)
    )["harness_id"] == result.harness_id
    assert identity_stack.store.fetch_one(
        "SELECT verified_email FROM principal_aliases WHERE principal_id=?",
        (result.principal_id,),
    )["verified_email"] == identity_stack.identity.verified_email

    with pytest.raises(ReplayError):
        identity_stack.complete(key, challenge)
    assert identity_stack.store.fetch_one("SELECT COUNT(*) AS count FROM harnesses")["count"] == 1
    assert identity_stack.store.fetch_one("SELECT COUNT(*) AS count FROM credentials")["count"] == 1


def test_tampered_or_noncanonical_transcript_is_rejected(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    challenge = identity_stack.begin(key)
    tampered = challenge.signed_fields()
    tampered["harness"]["display_name"] = "Impostor"
    tampered_bytes = canonical_json(tampered)
    approval = identity_stack.verifier.approve(canonical_transaction=tampered_bytes)
    possession = key.sign("agentnet.enrollment.pop.v1", tampered)

    with pytest.raises(AuthenticationError):
        identity_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=tampered_bytes,
            possession_signature=possession,
            approval=approval,
        )

    noncanonical = json.dumps(challenge.signed_fields(), indent=2).encode()
    with pytest.raises(ValidationError):
        identity_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=noncanonical,
            possession_signature=key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=identity_stack.verifier.approve(canonical_transaction=noncanonical),
        )


def test_nonce_possession_and_expiry_fail_closed(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    challenge = identity_stack.begin(key)
    approval = identity_stack.verifier.approve(canonical_transaction=challenge.canonical_transaction)

    with pytest.raises(AuthenticationError):
        identity_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce="wrong-nonce-value-that-is-long-enough",
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=approval,
        )

    wrong_key = P256KeyPair.generate()
    with pytest.raises(AuthenticationError):
        identity_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=wrong_key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=approval,
        )

    identity_stack.clock.value = challenge.expires_at
    with pytest.raises(AuthenticationError):
        identity_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=approval,
        )


def test_server_agent_mode_refuses_local_lab_approval(identity_stack: object) -> None:
    with pytest.raises(GateBlocked, match="server-agent mode refuses the local lab approval verifier"):
        EnrollmentService(
            identity_stack.store,
            identity_stack.verifier,
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            binding_assurance="os_bound",
        )

    with pytest.raises(GateBlocked, match="server-agent mode refuses the local lab approval verifier"):
        EnrollmentService(
            identity_stack.store,
            identity_stack.verifier,
            profile="always_on_server_agent",
            binding_assurance="os_bound",
        )

    with pytest.raises(GateBlocked, match="local conformance cannot claim"):
        EnrollmentService(
            identity_stack.store,
            identity_stack.verifier,
            profile=RuntimeProfile.LOCAL_CONFORMANCE,
            binding_assurance="hardware_bound",
        )


def test_stricter_enrollment_policy_controls_ttl_attempts_and_entropy(identity_stack: object) -> None:
    service = EnrollmentService(
        identity_stack.store,
        identity_stack.verifier,
        approval_policy=EnrollmentApprovalPolicy(
            transaction_ttl_seconds=60,
            maximum_attempts=2,
        ),
        clock=identity_stack.clock,
    )
    key = P256KeyPair.generate()
    challenge = service.begin(
        domain_id="corp.example",
        identity=identity_stack.identity,
        harness_kind="codex",
        harness_name="strict enrollment",
        public_key_pem=key.public_pem,
    )
    assert challenge.expires_at - identity_stack.clock() == 60
    approval = identity_stack.verifier.approve(canonical_transaction=challenge.canonical_transaction)
    possession = key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields())
    for index in range(2):
        with pytest.raises(AuthenticationError):
            service.complete(
                challenge_id=challenge.challenge_id,
                nonce=("x" if index == 0 else "y") * 32,
                canonical_transaction=challenge.canonical_transaction,
                possession_signature=possession,
                approval=approval,
            )
    row = identity_stack.store.fetch_one(
        "SELECT failed_attempts FROM enrollment_challenges WHERE challenge_id=?",
        (challenge.challenge_id,),
    )
    assert row["failed_attempts"] == 2
    with pytest.raises(AuthenticationError, match="attempt ceiling"):
        service.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=possession,
            approval=approval,
        )

    entropy_service = EnrollmentService(
        identity_stack.store,
        identity_stack.verifier,
        approval_policy=EnrollmentApprovalPolicy(out_of_band_min_entropy_bits=256),
        clock=identity_stack.clock,
    )
    entropy_key = P256KeyPair.generate()
    entropy_challenge = entropy_service.begin(
        domain_id="corp.example",
        identity=identity_stack.identity,
        harness_kind="codex",
        harness_name="entropy floor",
        public_key_pem=entropy_key.public_pem,
    )
    with pytest.raises(AuthenticationError, match="entropy floor"):
        entropy_service.complete(
            challenge_id=entropy_challenge.challenge_id,
            nonce=entropy_challenge.nonce,
            canonical_transaction=entropy_challenge.canonical_transaction,
            possession_signature=entropy_key.sign(
                "agentnet.enrollment.pop.v1", entropy_challenge.signed_fields()
            ),
            approval=identity_stack.verifier.approve(
                canonical_transaction=entropy_challenge.canonical_transaction
            ),
        )
