from __future__ import annotations

import hashlib

import pytest

from agentnet.approval.transaction_summary import (
    validate_and_summarize_approval_transaction,
)
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    COMMUNICATION_SCOPE_PROFILE,
    COMMUNICATION_SCOPE_RESTRICTIONS,
    build_communication_scope_transaction,
)
from agentnet.errors import AuthenticationError
from agentnet.security.signatures import canonical_json


def _transaction() -> dict[str, object]:
    preimage = {
        "schema": "agentnet.communication-scope.preimage.v1",
        "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
        "profile": COMMUNICATION_SCOPE_PROFILE,
        "profile_version": 1,
        "independent_boundary_proven": False,
        "max_uses": 1,
        "begin_idempotency_key_sha256": "a" * 64,
        "domain": {
            "domain_id": "corp.example",
            "policy_revision": 7,
            "revocation_epoch": 3,
        },
        "principal": {"principal_id": "principal-owner"},
        "harnesses": {
            "owner": {
                "harness_id": "owner-omp",
                "credential_id": "credential-owner",
                "credential_epoch": 2,
                "binding_assurance": "os_bound",
                "display_name": "Owner OMP laptop",
                "kind": "pi",
            },
            "fresh": {
                "harness_id": "ordinary-server",
                "credential_id": "credential-server",
                "credential_epoch": 4,
                "binding_assurance": "hardware_bound",
                "display_name": "Ordinary server agent",
                "kind": "server",
            },
        },
        "enrollment_evidence": {
            "owner": {"role": "owner"},
            "fresh": {"role": "fresh"},
        },
        "actions": sorted(COMMUNICATION_SCOPE_ACTIONS),
        "restrictions": dict(COMMUNICATION_SCOPE_RESTRICTIONS),
        "issued_at": 1_800_000_000,
        "approval_expires_at": 1_800_003_600,
        "authority_expires_at": None,
    }
    return build_communication_scope_transaction(preimage)


def _summary(transaction: dict[str, object]) -> dict[str, object]:
    canonical = canonical_json(transaction)
    return validate_and_summarize_approval_transaction(
        COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
        canonical,
        hashlib.sha256(canonical).hexdigest(),
    )


def test_communication_scope_summary_is_plain_and_explicit_about_permanent_boundaries() -> None:
    summary = _summary(_transaction())

    assert summary == {
        "title": "Approve persistent communication",
        "statements": [
            "Same verified person: one owner OMP laptop and one ordinary server agent",
            "Owner laptop: Owner OMP laptop (pi)",
            "Server agent: Ordinary server agent (server)",
            "Communication: messages, mailbox delivery, conversations, rooms, and response obligations",
            "Authority: 19 communication permissions for each exact enrolled harness",
            "Expiry: this communication authority does not expire automatically",
            "Revocation: either exact harness or any individual permission can be revoked",
            "Excluded: files, artifacts, tools, business effects, federation, and public A2A",
            "Independent approval boundary: not proven",
        ],
    }


@pytest.mark.parametrize(
    "mutate",
    [
        lambda value: value["items"][0].__setitem__("harness_id", "third-harness"),
        lambda value: value["items"][0]["entitlement"].__setitem__("expires_at", 1_900_000_000),
        lambda value: value["canonical_scope_preimage"]["restrictions"].__setitem__(
            "artifacts_enabled", True
        ),
    ],
)
def test_communication_scope_summary_rejects_scope_or_boundary_substitution(mutate) -> None:
    transaction = _transaction()
    mutate(transaction)

    with pytest.raises(AuthenticationError, match="approval request denied"):
        _summary(transaction)
