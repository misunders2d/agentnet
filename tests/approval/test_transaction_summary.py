from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path

import pytest

from agentnet.approval.transaction_summary import validate_and_summarize_approval_transaction
from agentnet.authorization.bootstrap_plan import build_bootstrap_plan_transaction
from agentnet.errors import AuthenticationError
from agentnet.identity.credentials import (
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    ManagedServerCredentialReauthorizationRequest,
)


FIXTURE = Path(__file__).parents[1] / "fixtures" / "bootstrap_plan_golden_vector.json"
PURPOSE = "authorization.bootstrap_plan.approve"


def _canonical(value: object) -> bytes:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        allow_nan=False,
    ).encode()


def _summary(value: dict[str, object]) -> dict[str, object]:
    canonical = _canonical(value)
    return validate_and_summarize_approval_transaction(
        PURPOSE,
        canonical,
        hashlib.sha256(canonical).hexdigest(),
    )


def test_bounded_plan_summary_validates_exact_transaction_and_leaks_no_identifiers() -> None:
    vector = json.loads(FIXTURE.read_text())
    transaction = vector["final_approval_transaction"]["value"]
    summary = _summary(transaction)

    assert summary == {
        "title": "Approve a bounded C0 laptop communication plan",
        "statements": [
            "Same verified person: two enrolled laptop harnesses",
            "Owner laptop: Owner laptop (pi)",
            "Fresh laptop: Fresh laptop (pi)",
            "Classification: C0",
            "Communication: one fixed harmless request and one fixed harmless reply",
            "Authority: five communication permissions and five exact cleanup permissions",
            "Expiry: communication and cleanup authority ends within one hour",
            "Safety: communication remains unusable until the exact C0 guard ships",
            "Independent approval boundary: not proven",
        ],
    }
    rendered = json.dumps(summary, sort_keys=True)
    for forbidden in (
        vector["derived_ids"]["plan_id"],
        vector["derived_ids"]["guard_id"],
        vector["canonical_plan_preimage"]["plan_digest"],
        vector["final_approval_transaction"]["transaction_digest"],
        vector["fixture"]["principal_id"],
        vector["fixture"]["owner_harness_id"],
        vector["fixture"]["fresh_harness_id"],
        "owner@example.test",
    ):
        assert forbidden not in rendered


@pytest.mark.parametrize(
    "mutate",
    [
        lambda value: value.update({"unexpected": True}),
        lambda value: value["guard"].update({"state_at_commit": "active"}),
        lambda value: value["items"].pop(),
        lambda value: value["items"][0]["entitlement"].update({"resource_pattern": "*"}),
        lambda value: value["guard"]["request_payload"].update({"message": "changed"}),
        lambda value: value.update({"item_count": 9}),
    ],
)
def test_bounded_plan_summary_rejects_any_transaction_mutation(mutate) -> None:
    value = copy.deepcopy(json.loads(FIXTURE.read_text())["final_approval_transaction"]["value"])
    mutate(value)
    with pytest.raises(AuthenticationError, match="approval request denied"):
        _summary(value)


@pytest.mark.parametrize(
    "mutate",
    [
        lambda value: value.update({"profile_version": True}),
        lambda value: value["domain"].update({"policy_revision": "1"}),
        lambda value: value["domain"].update({"revocation_epoch": 0}),
        lambda value: value["harnesses"]["owner"].update({"credential_epoch": True}),
        lambda value: value["harnesses"]["fresh"].update({"binding_assurance": "lab"}),
        lambda value: value["enrollment_evidence"]["owner"].update(
            {"approval_receipt_digest": "not-a-sha256"}
        ),
        lambda value: value["enrollment_evidence"]["fresh"].update(
            {"oidc_consumed_at": "1800000000"}
        ),
        lambda value: value["enrollment_evidence"]["owner"].update(
            {"approval_authenticated_at": 1_800_000_100, "approval_issued_at": 1_800_000_000}
        ),
    ],
)
def test_bounded_plan_summary_rejects_self_consistent_malformed_preimage(mutate) -> None:
    preimage = copy.deepcopy(
        json.loads(FIXTURE.read_text())["canonical_plan_preimage"]["value"]
    )
    mutate(preimage)
    transaction = build_bootstrap_plan_transaction(preimage)
    with pytest.raises(AuthenticationError, match="approval request denied"):
        _summary(transaction)


def test_enrollment_summary_rejects_hidden_or_malformed_transaction_fields() -> None:
    enrollment = {
        "candidate_key": {"algorithm": "ES256/P-256", "thumbprint": "key-thumbprint"},
        "challenge_id": "challenge-1",
        "domain_id": "corp.example",
        "expires_at": 1_800_000_300,
        "harness": {
            "binding_assurance": "os_bound",
            "display_name": "Fresh laptop",
            "kind": "pi",
            "requested_capabilities": ["message.send"],
            "requested_class": "protected_business",
        },
        "human": {
            "oidc_issuer": "https://idp.example",
            "oidc_subject": "subject-1",
            "verified_email": "owner@example.test",
        },
        "issued_at": 1_800_000_000,
        "nonce": "nonce-1",
        "purpose": "human_harness_credential_binding",
        "schema": "agentnet.enrollment.challenge.v1",
    }
    canonical = _canonical(enrollment)
    summary = validate_and_summarize_approval_transaction(
        "identity.enrollment.approve",
        canonical,
        hashlib.sha256(canonical).hexdigest(),
    )
    assert summary["title"] == "Enroll a laptop identity"

    mutations = [
        lambda value: value.update({"hidden_authority": "authorization.entitlement.issue"}),
        lambda value: value["human"].update({"hidden": "value"}),
        lambda value: value["harness"].update({"requested_class": "unbounded"}),
        lambda value: value["harness"].update(
            {"requested_capabilities": ["message.send", "artifact.read"]}
        ),
        lambda value: value["candidate_key"].update({"algorithm": "unknown"}),
        lambda value: value.update({"expires_at": value["issued_at"]}),
    ]
    for mutate in mutations:
        changed = copy.deepcopy(enrollment)
        mutate(changed)
        changed_canonical = _canonical(changed)
        with pytest.raises(AuthenticationError, match="approval request denied"):
            validate_and_summarize_approval_transaction(
                "identity.enrollment.approve",
                changed_canonical,
                hashlib.sha256(changed_canonical).hexdigest(),
            )


def test_bounded_plan_summary_rejects_wrong_digest_and_unknown_privileged_purpose() -> None:
    value = json.loads(FIXTURE.read_text())["final_approval_transaction"]["value"]
    canonical = _canonical(value)
    with pytest.raises(AuthenticationError, match="approval request denied"):
        validate_and_summarize_approval_transaction(PURPOSE, canonical, "0" * 64)
    with pytest.raises(AuthenticationError, match="approval request denied"):
        validate_and_summarize_approval_transaction("authorization.unknown.approve", canonical, hashlib.sha256(canonical).hexdigest())

    reauthorization = ManagedServerCredentialReauthorizationRequest(
        request_id="12345678-1234-4234-8234-123456789abc",
        domain_id="corp.example",
        principal_id="owner-principal",
        harness_id="managed-server-harness",
        expired_credential_id="expired-credential",
        expected_credential_epoch=1,
        expected_expired_at=1_800_000_000,
        expected_key_id="managed-server-key-thumbprint",
        expected_binding_assurance="os_bound",
        managed_config_sha256="a" * 64,
        managed_identity_sha256="b" * 64,
        maximum_new_credential_ttl_seconds=86_400,
        old_key_possession_signature="not-presented-to-browser",
    )
    reauthorization_canonical = reauthorization.canonical_transaction
    recovery_summary = validate_and_summarize_approval_transaction(
        MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        reauthorization_canonical,
        hashlib.sha256(reauthorization_canonical).hexdigest(),
    )
    assert recovery_summary["title"] == "Reauthorize an expired server-agent credential"
    rendered = json.dumps(recovery_summary, sort_keys=True)
    assert "managed-server-harness" not in rendered
    assert "expired-credential" not in rendered
    changed = json.loads(reauthorization_canonical)
    changed["old_credential_action"] = "extend"
    changed_canonical = _canonical(changed)
    with pytest.raises(AuthenticationError, match="approval request denied"):
        validate_and_summarize_approval_transaction(
            MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
            changed_canonical,
            hashlib.sha256(changed_canonical).hexdigest(),
        )
