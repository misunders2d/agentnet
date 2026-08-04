from __future__ import annotations

import hashlib

from agentnet.approval.transaction_summary import validate_and_summarize_approval_transaction
from agentnet.security.signatures import canonical_json


def test_harness_revocation_summary_names_exact_target_and_consequence() -> None:
    transaction = {
        "type": "harness_revocation",
        "request_id": "mutation-id-123456",
        "domain_id": "corp.example",
        "harness_id": "laptop-lost",
        "expected_credential_epoch": 3,
        "expected_domain_revocation_epoch": 7,
        "reason": "Device was lost",
    }
    canonical = canonical_json(transaction)
    digest = hashlib.sha256(canonical).hexdigest()

    summary = validate_and_summarize_approval_transaction(
        "identity.harness.revoke.approve", canonical, digest
    )

    assert summary["title"] == "Remove one laptop or agent"
    assert "Exact device: laptop-lost" in summary["statements"]
    assert "Other devices and the person remain active" in summary["statements"]
    assert summary["advanced_digest"] == digest


def test_enrollment_summary_binds_and_names_exact_requested_capabilities() -> None:
    transaction = {
        "schema": "agentnet.enrollment.challenge.v1",
        "purpose": "human_harness_credential_binding",
        "challenge_id": "intent-id-1234567890",
        "domain_id": "corp.example",
        "nonce": "candidate-transaction-123",
        "candidate_key": {"algorithm": "ES256/P-256", "thumbprint": "a" * 64},
        "harness": {
            "binding_assurance": "hardware_bound",
            "display_name": "Finance laptop",
            "kind": "laptop",
            "requested_class": "protected_business",
            "requested_capabilities": ["artifact.read", "message.send"],
        },
        "human": {
            "oidc_issuer": "https://idp.example",
            "oidc_subject": "subject-123",
            "verified_email": "person@example.test",
        },
        "issued_at": 1_900_000_000,
        "expires_at": 1_900_000_600,
    }
    canonical = canonical_json(transaction)
    digest = hashlib.sha256(canonical).hexdigest()

    summary = validate_and_summarize_approval_transaction(
        "identity.enrollment.approve", canonical, digest
    )

    assert summary["title"] == "Enroll a laptop identity"
    assert "Verified person: person@example.test" in summary["statements"]
    assert (
        "Requested capabilities (not granted by enrollment): artifact.read, message.send"
        in summary["statements"]
    )
    assert summary["advanced_digest"] == digest
