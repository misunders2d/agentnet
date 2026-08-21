from __future__ import annotations

import pytest
from pydantic import ValidationError

from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    COMMUNICATION_SCOPE_PROFILE,
    COMMUNICATION_SCOPE_RESTRICTIONS,
    CommunicationScopeBeginRequest,
    CommunicationScopeCompleteResult,
    CommunicationScopeCompleteRequest,
    CommunicationScopeStatusResult,
    build_communication_scope_transaction,
    require_communication_scope_transition,
)
from agentnet.errors import ConflictError


def _preimage() -> dict[str, object]:
    return {
        "schema": "agentnet.communication-scope.preimage.v1",
        "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
        "profile": COMMUNICATION_SCOPE_PROFILE,
        "profile_version": 1,
        "independent_boundary_proven": False,
        "max_uses": 1,
        "begin_idempotency_key_sha256": "a" * 64,
        "domain": {
            "domain_id": "domain-a",
            "policy_revision": 7,
            "revocation_epoch": 3,
        },
        "principal": {"principal_id": "human-a"},
        "harnesses": {
            "owner": {
                "harness_id": "harness-owner",
                "credential_id": "credential-owner",
                "credential_epoch": 2,
                "binding_assurance": "hardware_bound",
                "display_name": "Owner",
                "kind": "pi",
            },
            "fresh": {
                "harness_id": "harness-fresh",
                "credential_id": "credential-fresh",
                "credential_epoch": 1,
                "binding_assurance": "os_bound",
                "display_name": "Fresh",
                "kind": "codex",
            },
        },
        "enrollment_evidence": {
            "owner": {"schema": "evidence.v1", "role": "owner"},
            "fresh": {"schema": "evidence.v1", "role": "fresh"},
        },
        "actions": sorted(COMMUNICATION_SCOPE_ACTIONS),
        "restrictions": dict(COMMUNICATION_SCOPE_RESTRICTIONS),
        "issued_at": 1_800_000_000,
        "approval_expires_at": 1_800_003_600,
        "authority_expires_at": None,
    }


def test_models_are_strict_and_callers_supply_only_idempotency_keys() -> None:
    begin = CommunicationScopeBeginRequest.model_validate(
        {
            "schema": "agentnet.communication-scope.begin.v1",
            "begin_idempotency_key": "b" * 32,
        }
    )
    completion = CommunicationScopeCompleteRequest.model_validate(
        {
            "schema": "agentnet.communication-scope.complete.v1",
            "begin_idempotency_key": "b" * 32,
            "completion_idempotency_key": "c" * 32,
        }
    )

    assert begin.model_dump(by_alias=True) == {
        "schema": "agentnet.communication-scope.begin.v1",
        "begin_idempotency_key": "b" * 32,
    }
    assert completion.completion_idempotency_key == "c" * 32
    with pytest.raises(ValidationError):
        CommunicationScopeBeginRequest.model_validate(
            {
                "schema": "agentnet.communication-scope.begin.v1",
                "begin_idempotency_key": "b" * 32,
                "harness_id": "caller-selected-harness",
            }
        )


def test_transaction_is_deterministic_fixed_action_and_persistently_restricted() -> None:
    first = build_communication_scope_transaction(_preimage())
    second = build_communication_scope_transaction(_preimage())

    assert first == second
    assert set(first) == {
        "schema",
        "approval_purpose",
        "scope_digest",
        "canonical_scope_preimage",
        "scope_id",
        "items",
        "item_count",
        "restrictions",
    }
    assert first["restrictions"] == {
        "artifacts_enabled": False,
        "business_effects_enabled": False,
        "federation_enabled": False,
        "public_a2a_enabled": False,
        "authority_expires_at": None,
    }
    assert first["item_count"] == 2 * len(COMMUNICATION_SCOPE_ACTIONS) == 38
    assert {item["action"] for item in first["items"]} == COMMUNICATION_SCOPE_ACTIONS
    assert all(item["action"] != "*" for item in first["items"])
    assert all(item["entitlement"]["expires_at"] is None for item in first["items"])
    assert len({item["entitlement"]["entitlement_id"] for item in first["items"]}) == 38
    assert {
        (item["harness_id"], item["action"]) for item in first["items"]
    } == {
        (harness_id, action)
        for harness_id in ("harness-owner", "harness-fresh")
        for action in COMMUNICATION_SCOPE_ACTIONS
    }


def test_transaction_rejects_weakened_or_extended_scope() -> None:
    weakened = _preimage()
    weakened["restrictions"] = {
        **dict(COMMUNICATION_SCOPE_RESTRICTIONS),
        "federation_enabled": True,
    }
    with pytest.raises(ValueError, match="preimage is invalid"):
        build_communication_scope_transaction(weakened)

    extended = _preimage()
    extended["actions"] = [*sorted(COMMUNICATION_SCOPE_ACTIONS), "artifact.read"]
    with pytest.raises(ValueError, match="preimage is invalid"):
        build_communication_scope_transaction(extended)

    shortened = _preimage()
    shortened["approval_expires_at"] = 1_800_000_300
    with pytest.raises(ValueError, match="preimage is invalid"):
        build_communication_scope_transaction(shortened)


@pytest.mark.parametrize("display_name", ["Owner\napprove everything", "x" * 129])
def test_transaction_rejects_unsafe_approval_summary_text(display_name: str) -> None:
    unsafe = _preimage()
    harnesses = unsafe["harnesses"]
    assert isinstance(harnesses, dict)
    owner = harnesses["owner"]
    assert isinstance(owner, dict)
    owner["display_name"] = display_name

    with pytest.raises(ValueError, match="preimage is invalid"):
        build_communication_scope_transaction(unsafe)


def test_status_and_complete_results_enforce_exact_state_shapes() -> None:
    with pytest.raises(ValidationError):
        CommunicationScopeStatusResult.model_validate(
            {
                "schema": "agentnet.communication-scope.status-result.v1",
                "status": "approval_ready",
                "approval_url": "https://approval.example/approval",
                "expires_at": 1_800_000_300,
            }
        )

    result = CommunicationScopeCompleteResult.model_validate(
        {
            "schema": "agentnet.communication-scope.complete-result.v2",
            "status": "communication_active",
            "authority_granted": True,
            "communication_usable": True,
            "collaboration_scope_id": "scope:communication-contract",
            **dict(COMMUNICATION_SCOPE_RESTRICTIONS),
        }
    )
    assert result.authority_expires_at is None


def test_transition_rules_reject_skips_backwards_and_terminal_replays() -> None:
    require_communication_scope_transition("reserved", "pending_approval")
    require_communication_scope_transition("pending_approval", "approval_issued")
    require_communication_scope_transition("approval_issued", "completion_reserved")
    require_communication_scope_transition("completion_reserved", "committed")

    for current, target in (
        ("reserved", "committed"),
        ("committed", "completion_reserved"),
        ("rejected", "pending_approval"),
        ("unknown", "reserved"),
    ):
        with pytest.raises(ConflictError):
            require_communication_scope_transition(current, target)
