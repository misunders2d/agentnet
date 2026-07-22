from __future__ import annotations

from importlib import import_module

import pytest
from pydantic import ValidationError


def test_owner_session_contract_uses_stable_origin_and_host_cookie() -> None:
    contract = import_module("agentnet.approval.owner_session")

    assert contract.STABLE_APPROVAL_PATH == "/approval"
    assert contract.OWNER_SESSION_COOKIE_NAME.startswith("__Host-")
    assert contract.OWNER_SESSION_COOKIE_SECURE is True
    assert contract.OWNER_SESSION_COOKIE_HTTP_ONLY is True
    assert contract.OWNER_SESSION_COOKIE_SAME_SITE in {"strict", "lax"}
    assert "token" not in contract.STABLE_APPROVAL_PATH
    assert "capability" not in contract.STABLE_APPROVAL_PATH


def test_owner_oidc_requests_require_csrf_and_reject_caller_identity_claims() -> None:
    contract = import_module("agentnet.approval.owner_session")
    start = {
        "schema": "agentnet.approval.owner-oidc-start.v1",
        "csrf_token": "c" * 32,
    }
    assert contract.OwnerOIDCStartRequest.model_validate(start).csrf_token == "c" * 32
    for forbidden in ("issuer", "subject", "verified_email", "redirect_uri"):
        with pytest.raises(ValidationError):
            contract.OwnerOIDCStartRequest.model_validate({**start, forbidden: "caller-value"})
    with pytest.raises(ValidationError):
        contract.OwnerOIDCStartRequest.model_validate(
            {"schema": "agentnet.approval.owner-oidc-start.v1"}
        )


def test_owner_approval_selection_accepts_only_session_csrf_and_request_id() -> None:
    contract = import_module("agentnet.approval.owner_session")
    value = {
        "schema": "agentnet.approval.owner-request-select.v1",
        "csrf_token": "c" * 32,
        "request_id": "request-123456789",
    }
    assert contract.OwnerApprovalSelectRequest.model_validate(value).request_id == (
        "request-123456789"
    )
    for forbidden in (
        "approver_principal_id",
        "domain_id",
        "approval_purpose",
        "transaction_digest",
        "claim_code",
        "token",
    ):
        with pytest.raises(ValidationError):
            contract.OwnerApprovalSelectRequest.model_validate(
                {**value, forbidden: "caller-value"}
            )


def test_owner_session_state_machines_reject_backward_or_implicit_edges() -> None:
    contract = import_module("agentnet.approval.owner_session")

    assert contract.ALLOWED_OIDC_LOGIN_TRANSITIONS == {
        "pending": {"callback_claimed", "failed", "expired", "canceled"},
        "callback_claimed": {"callback_consumed", "failed", "expired"},
        "callback_consumed": set(),
        "failed": set(),
        "expired": set(),
        "canceled": set(),
    }
    assert contract.ALLOWED_REGISTRATION_TRANSITIONS == {
        "pending": {"verified", "failed", "expired", "canceled"},
        "verified": set(),
        "failed": set(),
        "expired": set(),
        "canceled": set(),
    }
