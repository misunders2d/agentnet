from __future__ import annotations

from datetime import UTC, datetime

import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.errors import ValidationError
from agentnet.security.signatures import P256KeyPair


def test_independent_verifier_strict_parses_the_complete_receipt_before_trust() -> None:
    signer = P256KeyPair.generate()
    purpose = "test.exact.approval"
    approver = TrustedApprover(
        principal_id="approver-principal",
        domain_id="corp.example",
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({purpose}),
    )
    verifier = IndependentApprovalVerifier(
        {signer.thumbprint: approver},
        verifier_id="strict-approval-verifier",
    )
    transaction = b'{"exact":"transaction"}'
    receipt = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=purpose,
        canonical_transaction=transaction,
        issued_at=1_800_000_000,
        expires_at=1_800_000_300,
    )

    verified = verifier.verify(
        canonical_transaction=transaction,
        approval=receipt,
        expected_purpose=purpose,
        expected_domain_id=approver.domain_id,
        when=datetime.fromtimestamp(1_800_000_001, UTC),
    )
    assert verified.signer_key_id == signer.thumbprint

    for field, value in (
        ("issued_at", "1800000000"),
        ("approved", 1),
        ("authentication_method", ["webauthn_uv"]),
        ("unexpected", "ignored"),
    ):
        malformed = dict(receipt)
        malformed[field] = value
        with pytest.raises(ValidationError, match="exact schema"):
            verifier.verify(
                canonical_transaction=transaction,
                approval=malformed,
                expected_purpose=purpose,
                expected_domain_id=approver.domain_id,
                when=datetime.fromtimestamp(1_800_000_001, UTC),
            )
