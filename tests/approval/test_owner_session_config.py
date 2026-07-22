from __future__ import annotations

from pathlib import Path

import pytest
from pydantic import ValidationError

from agentnet.approval.config import (
    MANDATORY_APPROVAL_PURPOSES,
    ApprovalOwnerOIDCConfig,
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
)
from agentnet.operations.config import OIDCTokenEndpointAuthMethod


def _approver(tmp_path: Path, **updates: object) -> ApprovalServiceApproverConfig:
    values: dict[str, object] = {
        "principal_id": "security-owner",
        "domain_id": "corp.example",
        "signer_key_id": "signer-key-identifier-1234",
        "signer_private_key_path": (tmp_path / "owner.pem").absolute(),
        "allowed_purposes": MANDATORY_APPROVAL_PURPOSES,
        "oidc_issuer": "https://idp.example",
        "oidc_subject": "owner-subject",
    }
    values.update(updates)
    return ApprovalServiceApproverConfig(**values)


def _provider(**updates: object) -> ApprovalOwnerOIDCConfig:
    values: dict[str, object] = {
        "issuer": "https://idp.example",
        "client_id": "agentnet-approval",
        "redirect_uri": "https://approval.corp.example/v1/approval/owner/oidc/callback",
        "allowed_endpoint_origins": ("https://idp.example",),
    }
    values.update(updates)
    return ApprovalOwnerOIDCConfig(**values)


def _config(tmp_path: Path, **updates: object) -> ApprovalServiceConfig:
    data = (tmp_path / "approval").absolute()
    values: dict[str, object] = {
        "public_origin": "https://approval.corp.example",
        "rp_id": "approval.corp.example",
        "verifier_id": "approval.corp.example",
        "data_dir": data,
        "database_path": data / "approval.sqlite3",
        "record_key_path": data / "secrets" / "records.key",
        "owner_oidc": _provider(),
        "approvers": (_approver(tmp_path),),
    }
    values.update(updates)
    return ApprovalServiceConfig(**values)


def test_owner_oidc_policy_binds_exact_callback_issuer_and_preapproved_subject(
    tmp_path: Path,
) -> None:
    config = _config(tmp_path)
    assert config.owner_oidc is not None
    assert config.owner_oidc.redirect_uri.endswith("/v1/approval/owner/oidc/callback")
    assert config.approvers[0].oidc_subject == "owner-subject"
    assert config.approvers[0].verified_email_alias is None

    with pytest.raises(ValidationError, match="stable Approval callback"):
        _config(
            tmp_path,
            owner_oidc=_provider(redirect_uri="https://approval.corp.example/wrong"),
        )
    with pytest.raises(ValidationError, match="configured OIDC issuer"):
        _config(
            tmp_path,
            approvers=(_approver(tmp_path, oidc_issuer="https://other.example"),),
        )
    with pytest.raises(ValidationError, match="exact subject or verified-email alias"):
        _approver(
            tmp_path,
            oidc_subject="owner-subject",
            verified_email_alias="owner@example.test",
        )
    with pytest.raises(ValidationError, match="normalized"):
        _approver(
            tmp_path,
            oidc_subject=None,
            verified_email_alias="Owner@Example.Test",
        )


def test_owner_oidc_confidential_secret_is_reference_only(tmp_path: Path) -> None:
    provider = _provider(
        token_endpoint_auth_method=OIDCTokenEndpointAuthMethod.CLIENT_SECRET_POST,
        client_secret_env="AGENTNET_APPROVAL_OIDC_CLIENT_SECRET",
    )
    config = _config(tmp_path, owner_oidc=provider)
    rendered = config.model_dump_json()
    assert "AGENTNET_APPROVAL_OIDC_CLIENT_SECRET" in rendered
    assert "client_secret\"" not in rendered

    with pytest.raises(ValidationError, match="environment policy is inconsistent"):
        _provider(
            token_endpoint_auth_method=OIDCTokenEndpointAuthMethod.CLIENT_SECRET_POST,
            client_secret_env=None,
        )
    with pytest.raises(ValidationError, match="environment policy is inconsistent"):
        _provider(client_secret_env="AGENTNET_APPROVAL_OIDC_CLIENT_SECRET")
    with pytest.raises(ValidationError, match="canonical HTTPS issuer"):
        _provider(issuer="http://idp.example")
    with pytest.raises(ValidationError, match="canonical HTTPS issuer"):
        _provider(issuer="https://idp.example/")
