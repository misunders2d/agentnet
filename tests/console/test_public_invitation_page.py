from __future__ import annotations

from html.parser import HTMLParser
from importlib.metadata import version as package_version

import pytest
from starlette.testclient import TestClient

from agentnet.console.http import create_console_app
from agentnet.identity.invitation_links import (
    InvitationUnavailable,
    PublicInvitationSummary,
)
from agentnet.identity.onboarding_prompt import build_onboarding_prompt


_UNAVAILABLE_MESSAGE = "This invitation is unavailable. Ask the sender for a new link."


class _TextCollector(HTMLParser):
    def __init__(self, *, element_id: str | None = None) -> None:
        super().__init__(convert_charrefs=True)
        self.element_id = element_id
        self.depth = 0
        self.ignored_depth = 0
        self.parts: list[str] = []

    def handle_starttag(
        self,
        tag: str,
        attrs: list[tuple[str, str | None]],
    ) -> None:
        if self.element_id is None and tag in {"script", "style", "title"}:
            self.ignored_depth += 1
        if self.element_id is not None and dict(attrs).get("id") == self.element_id:
            self.depth = 1
        elif self.depth:
            self.depth += 1

    def handle_endtag(self, tag: str) -> None:
        if self.element_id is None and tag in {"script", "style", "title"}:
            self.ignored_depth -= 1
        if self.depth:
            self.depth -= 1

    def handle_data(self, data: str) -> None:
        if not self.ignored_depth and (self.element_id is None or self.depth):
            self.parts.append(data)

    @property
    def text(self) -> str:
        return " ".join(" ".join(self.parts).split())


def _visible_text(document: str) -> str:
    parser = _TextCollector()
    parser.feed(document)
    return parser.text


def _element_text(document: str, element_id: str) -> str:
    parser = _TextCollector(element_id=element_id)
    parser.feed(document)
    return "".join(parser.parts).strip()


def _summary(*, scope_kind: str = "shared") -> PublicInvitationSummary:
    return PublicInvitationSummary(
        scope_kind=scope_kind,
        permission_actions=tuple(
            sorted(
                {
                    "artifact.download",
                    "artifact.send",
                    "database.admin",
                    "domain_id.read",
                    "harness_id.read",
                    "invitation_id.read",
                    "message.read",
                    "message.send",
                    "oidc.login",
                    "principal_id.read",
                    "protocol.debug",
                    "receipt.read",
                    "scope_id.read",
                    "secret.export",
                }
            )
        ),
        expires_at=1_800_000_000,
    )


class _PublicInvitationLinks:
    def __init__(self, token: str, summary: PublicInvitationSummary) -> None:
        self.token = token
        self.summary = summary

    def inspect_public(self, *, opaque_token: str) -> PublicInvitationSummary:
        if opaque_token != self.token:
            raise InvitationUnavailable()
        return self.summary


class _UnusedConsoleService:
    approval_public_origin = "https://approval.example"


def _public_client(
    *,
    token: str,
    summary: PublicInvitationSummary,
) -> TestClient:
    unused = _UnusedConsoleService()
    app = create_console_app(
        sessions=unused,
        read_service=unused,
        mutation_service=unused,
        invitation_links=_PublicInvitationLinks(token, summary),
        public_origin="https://console.example",
    )
    return TestClient(app, base_url="https://console.example")


def _assert_public_headers(response) -> None:
    assert response.headers["cache-control"] == "no-store"
    assert "default-src 'none'" in response.headers["content-security-policy"]
    assert "frame-ancestors 'none'" in response.headers["content-security-policy"]
    assert response.headers["x-frame-options"] == "DENY"
    assert response.headers["referrer-policy"] == "no-referrer"


def test_onboarding_prompt_is_complete_current_and_plain_language() -> None:
    summary = _summary()
    prompt = build_onboarding_prompt(summary)
    current_version = package_version("agentnet")

    assert prompt.package_version == current_version
    assert f"npm install -g @misunders2d/agentnet@{current_version}" in prompt.install_text
    assert "Install AgentNet without sudo" in prompt.install_text
    assert "shared collaboration space" in prompt.copyable_text
    assert "send and receive messages" in prompt.copyable_text
    assert "send and receive files" in prompt.copyable_text
    assert "preserve your existing AgentNet state" in prompt.copyable_text
    assert "Continue with work account" in prompt.copyable_text
    assert "sign in with your work account" in prompt.copyable_text
    assert "approve" in prompt.copyable_text.casefold()
    assert "passkey" in prompt.copyable_text.casefold()
    assert "You will be asked before your agent restarts" in prompt.restart_text
    assert "restart your agent yourself" in prompt.restart_text.casefold()
    assert "safely resume" in prompt.recovery_text
    assert "ask the sender for a new link" in prompt.recovery_text


@pytest.mark.parametrize(
    ("scope_kind", "display_context"),
    [
        ("personal", "private AgentNet space"),
        ("direct", "direct conversation space"),
        ("shared", "shared collaboration space"),
    ],
)
def test_onboarding_prompt_uses_only_safe_display_context(
    scope_kind: str,
    display_context: str,
) -> None:
    prompt = build_onboarding_prompt(_summary(scope_kind=scope_kind))
    lowered = prompt.copyable_text.casefold()

    assert f"You are joining a {display_context}" in prompt.copyable_text
    for technical_value in (
        "database",
        "domain_id",
        "harness_id",
        "invitation_id",
        "oidc",
        "principal_id",
        "protocol",
        "receipt",
        "scope_id",
        "secret",
        "/home/",
        "/var/",
        ".agentnet",
    ):
        assert technical_value not in lowered
    assert "agentnet client" not in lowered
    assert "pip install" not in lowered
    assert "npm install" in lowered


def test_public_invitation_page_contains_complete_copyable_prompt() -> None:
    token = "public-token-must-not-be-copied"
    summary = _summary()
    expected = build_onboarding_prompt(summary)
    client = _public_client(token=token, summary=summary)

    response = client.get(f"/join/{token}")

    assert response.status_code == 200
    _assert_public_headers(response)
    assert _element_text(response.text, "onboarding-prompt") == expected.copyable_text
    visible = _visible_text(response.text)
    assert "Continue with work account" in visible
    assert "Install AgentNet without sudo" in visible
    assert "You will be asked before your agent restarts" in visible
    assert token not in _element_text(response.text, "onboarding-prompt")


@pytest.mark.parametrize(
    "unavailable_path",
    [
        "/join/unknown-invitation",
        "/join/expired-invitation",
        "/join/revoked-invitation",
        "/join/%3Cscript%3Ealert%281%29",
    ],
)
def test_unavailable_invitation_is_generic_escaped_and_protected(
    unavailable_path: str,
) -> None:
    client = _public_client(token="one-active-token", summary=_summary())

    response = client.get(unavailable_path)

    assert response.status_code == 410
    _assert_public_headers(response)
    assert _visible_text(response.text) == _UNAVAILABLE_MESSAGE
    assert "<script>" not in response.text.casefold()
