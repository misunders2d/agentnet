from __future__ import annotations

import hashlib
import re
import secrets
import time

from starlette.testclient import TestClient

from agentnet.console.http import create_console_app
from agentnet.console.mutations import ConsoleMutationService
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.session import ConsoleSessionService
from agentnet.identity.invitation_links import InvitationLinkService


class _AllowConsoleReads:
    def __init__(self) -> None:
        self.requests: list[dict[str, object]] = []

    def require(self, *, actor, action: str, resource: str, context=None):
        self.requests.append(
            {"actor": actor, "action": action, "resource": resource, "context": context}
        )
        if action.startswith("console."):
            assert resource == f"console-domain:{actor.domain_id}"
        elif action == "identity.harness.revoke":
            assert resource.startswith("harness:")
            assert isinstance(context, dict)
            assert set(context) == {"request_digest"}
            assert re.fullmatch(r"[a-f0-9]{64}", str(context["request_digest"]))
        else:
            assert action == "identity.enrollment.propose"
            assert resource.startswith(("principal:", "domain:"))
            assert context is not None


class _ApprovalRecorder:
    def __init__(self) -> None:
        self.requests: list[dict[str, object]] = []

    def create_request(self, **request):
        self.requests.append(request)
        return {
            "request_id": "approval-1",
            "state": "pending",
            "approval_purpose": request["approval_purpose"],
            "transaction_digest": request["transaction_digest"],
            "expires_at": request["request_expires_at"],
            "duplicate": False,
        }


def _client(store, identity_factory):
    actor, _ = identity_factory(domain="corp.example", binding_assurance="hardware_bound")
    authority = _AllowConsoleReads()
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=900,
        require=authority.require,
    )
    issued = sessions.issue_for_verified_actor(actor=actor)
    reader = ConsoleReadService(store=store, require=authority.require)
    approvals = _ApprovalRecorder()
    invitation_links = InvitationLinkService(
        store,
        public_base_url="https://console.example/join",
    )
    mutations = ConsoleMutationService(
        store=store,
        approval_client=approvals,
        invitation_links=invitation_links,
        require=authority.require,
    )
    app = create_console_app(
        sessions=sessions,
        read_service=reader,
        mutation_service=mutations,
        invitation_links=invitation_links,
        public_origin="https://console.example",
    )
    client = TestClient(app, base_url="https://console.example")
    client.cookies.set("__Host-agentnet_console", issued.session_token, path="/")
    return client, actor, issued, approvals, authority


def test_console_routes_are_narrow_and_server_fleet_is_read_only(store, identity_factory) -> None:
    client, _, _, _, _ = _client(store, identity_factory)

    assert client.get("/").status_code == 200
    assert client.get("/servers").status_code == 200
    assert client.get("/people").status_code == 200
    assert client.get("/approvals").status_code == 200
    assert client.get("/security").status_code == 200
    assert client.get("/activity").status_code == 200
    assert client.post("/servers/server-1/drain").status_code == 404
    assert client.post("/servers/server-1/revoke").status_code == 404
    assert client.get("/messages").status_code == 404
    assert client.get("/files").status_code == 404


def test_sensitive_action_requires_same_origin_one_use_token_and_exact_confirmation(store, identity_factory) -> None:
    client, actor, _, approvals, authority = _client(store, identity_factory)
    sibling, _ = identity_factory(
        domain="corp.example",
        principal_id=actor.principal_id,
        binding_assurance="hardware_bound",
    )
    path = f"/harnesses/{sibling.harness_id}/revoke"
    review_path = f"{path}/review"

    missing_origin = client.post(review_path, data={"reason": "Lost laptop"})
    assert missing_origin.status_code == 403

    wrong_confirmation = client.post(
        review_path,
        headers={"Origin": "https://console.example"},
        data={
            "reason": "Lost laptop",
            "confirmation": "Confirm",
            "idempotency_key": secrets.token_urlsafe(24),
        },
    )
    assert wrong_confirmation.status_code == 400

    idempotency_key = secrets.token_urlsafe(24)
    confirmation = f"Remove access for {sibling.harness_id}"
    exact_request = {
        "reason": "Lost laptop",
        "confirmation": confirmation,
        "idempotency_key": idempotency_key,
    }
    review = client.post(
        review_path,
        headers={"Origin": "https://console.example"},
        data=exact_request,
    )
    assert review.status_code == 200
    match = re.search(
        rf'<form method="post" action="{re.escape(path)}">.*?'
        r'name="mutation_token" value="([^"]+)"',
        review.text,
        re.DOTALL,
    )
    assert match is not None
    mutation_token = match.group(1)
    wrong_mutation_token = (
        ("A" if mutation_token[0] != "A" else "B") + mutation_token[1:]
    )

    missing_token = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data=exact_request,
        follow_redirects=False,
    )
    assert missing_token.status_code == 403

    wrong_token = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={**exact_request, "mutation_token": wrong_mutation_token},
        follow_redirects=False,
    )
    assert wrong_token.status_code == 403

    drifted = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={
            **exact_request,
            "mutation_token": mutation_token,
            "reason": "Different reason",
        },
        follow_redirects=False,
    )
    assert drifted.status_code == 403

    accepted = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={**exact_request, "mutation_token": mutation_token},
        follow_redirects=False,
    )
    assert accepted.status_code == 303

    replayed = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={**exact_request, "mutation_token": mutation_token},
        follow_redirects=False,
    )
    assert replayed.status_code == 403

    assert len(approvals.requests) == 1
    approval = approvals.requests[0]
    assert approval["approval_purpose"] == "identity.harness.revoke.approve"
    assert len(str(approval["transaction_digest"])) == 64
    revocation_checks = [
        request
        for request in authority.requests
        if request["action"] == "identity.harness.revoke"
    ]
    assert revocation_checks == [
        {
            "actor": actor,
            "action": "identity.harness.revoke",
            "resource": f"harness:{sibling.harness_id}",
            "context": {"request_digest": approval["transaction_digest"]},
        }
    ]
    row = store.fetch_one("SELECT status FROM harnesses WHERE harness_id=?", (sibling.harness_id,))
    assert row["status"] == "active"


def test_static_assets_are_local_and_session_is_not_exposed_to_javascript(store, identity_factory) -> None:
    client, _, issued, _, _ = _client(store, identity_factory)
    css = client.get("/assets/console.css")
    js = client.get("/assets/console.js")

    assert css.status_code == 200
    assert js.status_code == 200
    assert "localStorage" not in js.text
    assert "sessionStorage" not in js.text
    assert "document.cookie" not in js.text
    assert "innerHTML" not in js.text
    assert issued.session_token not in client.get("/").text
    assert hashlib.sha256(css.content + b"\0" + js.content).hexdigest() in client.get(
        "/"
    ).text


def test_initial_enrollment_submit_only_renders_an_exact_review(
    store, identity_factory
) -> None:
    client, _, _, _, _ = _client(store, identity_factory)

    response = client.post(
        "/enrollments/review",
        headers={"Origin": "https://console.example"},
        data={
            "target_kind": "new_person",
            "target_principal_id": "",
            "invited_email_alias": " Person@Example.Test ",
            "harness_name": "Field laptop",
            "capabilities": ["offline_delivery", "message_delivery"],
            "reason": "Provide field access",
            "idempotency_key": "enrollment-http-review-0001",
        },
    )

    assert response.status_code == 200
    assert "Review enrollment request" in response.text
    assert "person@example.test" in response.text
    assert "Field laptop" in response.text
    assert "Message delivery" in response.text
    assert "Offline delivery" in response.text
    assert "No access is created" in response.text
    assert 'action="/enrollments"' in response.text
    assert 'name="review_token"' in response.text
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_reviews")["n"] == 1
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_intents")["n"] == 0


def test_enrollment_review_validation_rerenders_non_sensitive_values_inline(
    store, identity_factory
) -> None:
    client, _, _, _, _ = _client(store, identity_factory)

    response = client.post(
        "/enrollments/review",
        headers={"Origin": "https://console.example"},
        data={
            "target_kind": "new_person",
            "target_principal_id": "",
            "invited_email_alias": "not-an-email",
            "harness_name": "<Field agent>",
            "capabilities": ["message_delivery"],
            "reason": "Provide field access",
            "idempotency_key": "enrollment-http-review-0002",
        },
    )

    assert response.status_code == 400
    assert "Enter a valid verified email address" in response.text
    assert "&lt;Field agent&gt;" in response.text
    assert "not-an-email" in response.text
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_reviews")["n"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_intents")["n"] == 0
