from __future__ import annotations

import time

import pytest
from starlette.testclient import TestClient

from agentnet.console.headers import protected_headers
from agentnet.console.models import (
    ActivityPage,
    ActivitySummary,
    ApprovalPage,
    HomeSummary,
    PersonPage,
    SecurityPage,
    ServerPage,
    ServerSummary,
    VisibleState,
)
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.render import ConsoleRenderer
from agentnet.console.session import ConsoleSessionService
from agentnet.errors import AuthenticationError, AuthorizationError


class _Authorizer:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str]] = []

    def require(self, *, actor, action: str, resource: str) -> None:
        self.calls.append((action, resource))
        if action == "console.security.read":
            raise AuthorizationError("denied")


def test_protected_headers_are_private_and_frame_denied() -> None:
    headers = protected_headers()
    assert headers["Cache-Control"] == "no-store"
    assert headers["X-Frame-Options"] == "DENY"
    assert headers["X-Content-Type-Options"] == "nosniff"
    assert "frame-ancestors 'none'" in headers["Content-Security-Policy"]
    assert "default-src 'self'" in headers["Content-Security-Policy"]


def test_read_service_authorizes_before_query_and_keeps_domains_separate(store, identity_factory) -> None:
    actor, _ = identity_factory(domain="corp.example", binding_assurance="hardware_bound")
    identity_factory(domain="other.example", binding_assurance="hardware_bound")
    auth = _Authorizer()
    service = ConsoleReadService(store=store, require=auth.require)

    people = service.people(actor=actor)

    assert auth.calls[0] == ("console.people.read", "console-domain:corp.example")
    assert {person.domain_id for person in people.people} == {"corp.example"}
    with pytest.raises(AuthorizationError):
        service.security(actor=actor)


def test_session_revalidates_exact_harness_on_every_use(store, identity_factory) -> None:
    actor, _ = identity_factory(domain="corp.example", binding_assurance="hardware_bound")
    sessions = ConsoleSessionService(store=store, audience="https://console.example", ttl_seconds=900)
    issued = sessions.issue_for_verified_actor(actor=actor)

    assert sessions.authenticate(issued.session_token).actor.harness_id == actor.harness_id
    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (actor.harness_id,))
    with pytest.raises(AuthenticationError, match="console session denied"):
        sessions.authenticate(issued.session_token)


def test_renderer_uses_semantic_plain_language_and_escapes_names() -> None:
    renderer = ConsoleRenderer(asset_version="test")
    home = HomeSummary(
        state=VisibleState.ONLINE,
        server_total=2,
        server_online=1,
        people_total=1,
        agent_total=3,
        approvals_waiting=1,
        security_issues=0,
        fresh_at=int(time.time()),
    )
    servers = ServerPage(
        servers=(
            ServerSummary(
                harness_id="server-1",
                friendly_name="<script>alert(1)</script>",
                kind="Server agent",
                state=VisibleState.ONLINE,
                last_checked_at=int(time.time()),
                capabilities=("Message delivery",),
                blockers=(),
                access_state="Active",
                technical=None,
            ),
        ),
        fresh_at=int(time.time()),
    )

    document = renderer.home(home=home, csrf_token="csrf")
    server_document = renderer.servers(page=servers, csrf_token="csrf")

    assert document.count("<h1") == 1
    assert '<nav aria-label="Primary">' in document
    assert 'aria-live="polite"' in document
    assert "Hub" not in document
    assert "&lt;script&gt;alert(1)&lt;/script&gt;" in server_document
    assert "<script>alert(1)</script>" not in server_document
    assert "Revoke server" not in server_document


def test_renderer_exposes_all_approved_pages_without_protected_content() -> None:
    renderer = ConsoleRenderer(asset_version="test")
    now = int(time.time())
    pages = (
        renderer.people(PersonPage(people=(), relationships=(), fresh_at=now), csrf_token="csrf"),
        renderer.approvals(ApprovalPage(approvals=(), fresh_at=now), csrf_token="csrf"),
        renderer.security(SecurityPage(issues=(), incident_mode="Normal", audit_healthy=True, fresh_at=now), csrf_token="csrf"),
        renderer.activity(ActivityPage(events=(ActivitySummary(event_id="event-1", occurred_at=now, actor="Administrator", action="Signed in", resource="Administration console", result="Completed", server=None, technical=None),), fresh_at=now), csrf_token="csrf"),
    )
    forbidden = ("private key", "claim code", "credential_epoch", "DPoP", "database")
    for page in pages:
        assert page.count("<h1") == 1
        assert all(word not in page for word in forbidden)
