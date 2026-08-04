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
    HarnessSummary,
    PersonSummary,
    VisibleState,
)
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.render import ConsoleRenderer
from agentnet.core.app import CommunicationCore
from agentnet.console.session import ConsoleSessionService
from agentnet.errors import AuthenticationError, AuthorizationError
from agentnet.security.signatures import canonical_json


class _Authorizer:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str]] = []

    def require(self, *, actor, action: str, resource: str, context=None) -> None:
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
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=900,
        require=lambda **_kwargs: None,
    )
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

    authorize = lambda _path, _form: "one-use-mutation-token"
    document = renderer.home(home=home, authorize_mutation=authorize)
    server_document = renderer.servers(page=servers, authorize_mutation=authorize)

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
    authorize = lambda _path, _form: "one-use-mutation-token"
    pages = (
        renderer.people(PersonPage(people=(), relationships=(), fresh_at=now), authorize_mutation=authorize),
        renderer.approvals(ApprovalPage(approvals=(), fresh_at=now), authorize_mutation=authorize),
        renderer.security(SecurityPage(issues=(), incident_mode="Normal", audit_healthy=True, fresh_at=now), authorize_mutation=authorize),
        renderer.activity(ActivityPage(events=(ActivitySummary(event_id="event-1", occurred_at=now, actor="Administrator", action="Signed in", resource="Administration console", result="Completed", server=None, technical=None),), fresh_at=now), authorize_mutation=authorize),
    )
    forbidden = ("private key", "claim code", "credential_epoch", "DPoP", "database")
    for page in pages:
        assert page.count("<h1") == 1
        assert all(word not in page for word in forbidden)



def test_home_with_no_enrolled_servers_waits_for_server(store, identity_factory) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    service = ConsoleReadService(store=store, require=lambda **_request: None)

    home = service.home(actor=actor)

    assert home.server_total == 0
    assert home.server_online == 0
    assert home.state is VisibleState.WAITING_SERVER


def test_activity_requires_exact_domain_and_filters_before_its_result_limit(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    with store.transaction() as connection:
        store.append_audit(
            connection,
            {
                "action": "console.mutation.completed",
                "domain_id": actor.domain_id,
                "resource": "visible-domain-event",
                "outcome": "completed",
            },
        )
        for index in range(101):
            store.append_audit(
                connection,
                {
                    "action": "console.mutation.completed",
                    "domain_id": "other.example",
                    "resource": f"other-event-{index}",
                    "outcome": "completed",
                },
            )
        store.append_audit(
            connection,
            {
                "action": "console.mutation.completed",
                "resource": "domainless-event",
                "outcome": "completed",
            },
        )
    service = ConsoleReadService(store=store, require=lambda **_request: None)

    activity = service.activity(actor=actor)

    assert [event.resource for event in activity.events] == ["visible-domain-event"]


def test_activity_maps_non_success_outcomes_without_claiming_completion(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    expected = {
        "waiting": "Waiting",
        "unknown": "Unknown — needs reconciliation",
        "rejected": "Rejected",
        "expired": "Expired",
        "canceled": "Canceled",
        "denied": "Denied",
        "failed": "Failed",
    }
    with store.transaction() as connection:
        for outcome in expected:
            store.append_audit(
                connection,
                {
                    "action": "console.mutation.completed",
                    "domain_id": actor.domain_id,
                    "resource": f"outcome-{outcome}",
                    "outcome": outcome,
                },
            )
    service = ConsoleReadService(store=store, require=lambda **_request: None)

    activity = service.activity(actor=actor)

    observed = {
        event.resource.removeprefix("outcome-"): event.result
        for event in activity.events
    }
    assert observed == expected


def test_approval_projection_retains_enrollment_lifecycle_and_terminal_mutations(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    now = int(time.time())
    enrollment_states = {
        "invitation_issued": VisibleState.WAITING_SERVER,
        "waiting_possession": VisibleState.WAITING_SERVER,
        "enrolled": VisibleState.COMPLETED,
        "expired": VisibleState.EXPIRED,
        "canceled": VisibleState.CANCELED,
        "blocked": VisibleState.BLOCKED,
        "failed": VisibleState.FAILED,
        "unknown": VisibleState.UNKNOWN,
    }
    mutation_states = {
        "completed": VisibleState.COMPLETED,
        "failed": VisibleState.FAILED,
        "rejected": VisibleState.FAILED,
        "expired": VisibleState.EXPIRED,
    }
    request_json = canonical_json(
        {
            "person": "redacted@example.test",
            "harness_name": "Field laptop",
            "capabilities": ["message_delivery"],
            "consequence": "Only the reviewed laptop enrollment can proceed.",
        }
    ).decode()
    with store.transaction() as connection:
        for index, state in enumerate(enrollment_states):
            intent_id = f"lifecycle-enrollment-{index:02d}"
            connection.execute(
                """INSERT INTO console_enrollment_intents(
                    intent_id,domain_id,sponsor_principal_id,sponsor_harness_id,target_kind,
                    target_principal_id,invited_email_alias,request_json,request_digest,state,
                    revision,created_at,updated_at,expires_at
                ) VALUES(?,?,?,?,? ,?,?,?, ?,?,1,?,?,?)""",
                (
                    intent_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    "existing_person",
                    actor.principal_id,
                    None,
                    request_json,
                    f"{index + 1:064x}",
                    state,
                    now,
                    now,
                    now + 600,
                ),
            )
        for index, state in enumerate(mutation_states):
            mutation_id = f"lifecycle-mutation-{index:02d}"
            connection.execute(
                """INSERT INTO console_mutations(
                    mutation_id,domain_id,actor_principal_id,actor_harness_id,mutation_kind,
                    resource,request_json,request_digest,idempotency_key,state,revision,
                    created_at,updated_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?,?)""",
                (
                    mutation_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    "harness_revoke",
                    "harness:redacted",
                    request_json,
                    f"{index + 101:064x}",
                    f"lifecycle-idempotency-{index:02d}",
                    state,
                    now,
                    now,
                    now + 600,
                ),
            )
    service = ConsoleReadService(
        store=store, require=lambda **_request: None, clock=lambda: now
    )

    page = service.approvals(actor=actor)
    visible = {item.request_id: item.state for item in page.approvals}

    for index, expected_state in enumerate(enrollment_states.values()):
        assert visible[f"lifecycle-enrollment-{index:02d}"] is expected_state
    for index, expected_state in enumerate(mutation_states.values()):
        assert visible[f"lifecycle-mutation-{index:02d}"] is expected_state


def test_people_renderer_stages_enrollment_review_and_uses_harness_wording() -> None:
    now = int(time.time())
    renderer = ConsoleRenderer(asset_version="test")
    page = PersonPage(
        people=(
            PersonSummary(
                principal_id="principal-1",
                domain_id="corp.example",
                display_name="person@example.test",
                access_state="Active",
                harnesses=(
                    HarnessSummary(
                        harness_id="harness-1",
                        friendly_name="Field agent",
                        kind="Agent",
                        access_state="Active",
                        credential_state="Active",
                        credential_expires_at=now + 600,
                        can_remove=True,
                    ),
                ),
            ),
        ),
        relationships=(),
        fresh_at=now,
    )

    document = renderer.people(
        page,
        lambda _path, _form: "mutation-token",
        enrollment_values={
            "target_kind": "new_person",
            "invited_email_alias": "person@example.test",
            "harness_name": "<Field agent>",
            "capabilities": ("message_delivery",),
            "reason": "Field access",
        },
        enrollment_error="Requested service is not allowed",
    )

    assert 'action="/enrollments/review"' in document
    assert "Review enrollment request" in document
    assert "Start this enrollment request with these exact details" not in document
    assert "Remove this harness’s access" in document
    assert "Remove this laptop’s access" not in document
    assert "&lt;Field agent&gt;" in document
    assert "Requested service is not allowed" in document
    assert 'data-enrollment-existing hidden' in document
    assert 'data-enrollment-new>' in document
    assert 'data-enrollment-new hidden' not in document


def test_core_console_authority_forwards_exact_context(
    monkeypatch, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    captured: dict[str, object] = {}

    def capture(self, **request):
        del self
        captured.update(request)
        return "allowed"

    monkeypatch.setattr(CommunicationCore, "_require", capture)
    core = object.__new__(CommunicationCore)
    context = {"capabilities": ("message_delivery",)}

    result = core._require_console_authority(
        actor=actor,
        action="identity.enrollment.propose",
        resource="domain:corp.example:new-person",
        context=context,
    )

    assert result == "allowed"
    assert captured["context"] is context