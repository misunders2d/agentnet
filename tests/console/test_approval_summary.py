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
