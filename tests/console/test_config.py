from __future__ import annotations

import pytest
from pydantic import ValidationError

from agentnet.operations.config import AdminConsoleConfig, AdminConsoleOIDCConfig, ExtensionConfig


def _oidc() -> AdminConsoleOIDCConfig:
    return AdminConsoleOIDCConfig(
        issuer="https://idp.example",
        client_id="agentnet-console",
        redirect_uri="https://console.example/v1/console/oidc/callback",
        verifier_id="console-oidc",
    )


def test_console_configuration_is_explicit_and_inert_by_default() -> None:
    assert ExtensionConfig().features.admin_console is False
    assert ExtensionConfig().admin_console is None
    with pytest.raises(ValidationError, match="admin_console requires"):
        ExtensionConfig.model_validate({"features": {"admin_console": True}})


def test_console_requires_exact_origin_callback_and_safe_time_bounds() -> None:
    config = AdminConsoleConfig(
        public_origin="https://console.example",
        service_audience="urn:agentnet:corp.example:console",
        oidc=_oidc(),
    )
    assert config.oidc.redirect_uri == "https://console.example/v1/console/oidc/callback"

    with pytest.raises(ValidationError):
        AdminConsoleConfig(
            public_origin="http://console.example",
            service_audience="urn:agentnet:corp.example:console",
            oidc=_oidc(),
        )
    with pytest.raises(ValidationError, match="callback"):
        AdminConsoleConfig(
            public_origin="https://other.example",
            service_audience="urn:agentnet:corp.example:console",
            oidc=_oidc(),
        )
    with pytest.raises(ValidationError):
        AdminConsoleConfig(
            public_origin="https://console.example",
            service_audience="urn:agentnet:corp.example:console",
            oidc=_oidc(),
            session_ttl_seconds=30,
        )
