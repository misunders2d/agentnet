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


class _AllowConsoleReads:
    def require(self, *, actor, action: str, resource: str, context=None):
        if action.startswith("console."):
            assert resource == f"console-domain:{actor.domain_id}"
        elif action == "identity.harness.revoke":
            assert resource.startswith("harness:")
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
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=900,
        require=_AllowConsoleReads().require,
    )
    issued = sessions.issue_for_verified_actor(actor=actor)
    reader = ConsoleReadService(store=store, require=_AllowConsoleReads().require)
    approvals = _ApprovalRecorder()
    mutations = ConsoleMutationService(
        store=store,
        approval_client=approvals,
        require=_AllowConsoleReads().require,
    )
    app = create_console_app(
        sessions=sessions,
        read_service=reader,
        mutation_service=mutations,
        public_origin="https://console.example",
    )
    client = TestClient(app, base_url="https://console.example")
    client.cookies.set("__Host-agentnet_console", issued.session_token, path="/")
    return client, actor, issued, approvals


def test_console_routes_are_narrow_and_server_fleet_is_read_only(store, identity_factory) -> None:
    client, _, _, _ = _client(store, identity_factory)

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
    client, actor, _, approvals = _client(store, identity_factory)
    sibling, _ = identity_factory(
        domain="corp.example",
        principal_id=actor.principal_id,
        binding_assurance="hardware_bound",
    )
    path = f"/harnesses/{sibling.harness_id}/revoke"
    review_path = f"{path}/review"

    missing = client.post(review_path, data={"reason": "Lost laptop"})
    assert missing.status_code == 403

    wrong = client.post(
        review_path,
        headers={"Origin": "https://console.example"},
        data={
            "reason": "Lost laptop",
            "confirmation": "Confirm",
            "idempotency_key": secrets.token_urlsafe(24),
        },
    )
    assert wrong.status_code == 400

    idempotency_key = secrets.token_urlsafe(24)
    review = client.post(
        review_path,
        headers={"Origin": "https://console.example"},
        data={
            "reason": "Lost laptop",
            "confirmation": f"Remove access for {sibling.harness_id}",
            "idempotency_key": idempotency_key,
        },
    )
    assert review.status_code == 200
    match = re.search(r'name=\"mutation_token\" value=\"([^\"]+)\"', review.text)
    assert match is not None

    accepted = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={
            "mutation_token": match.group(1),
            "reason": "Lost laptop",
            "confirmation": f"Remove access for {sibling.harness_id}",
            "idempotency_key": idempotency_key,
        },
        follow_redirects=False,
    )
    assert accepted.status_code == 303
    assert len(approvals.requests) == 1
    assert approvals.requests[0]["approval_purpose"] == "identity.harness.revoke.approve"
    assert len(str(approvals.requests[0]["transaction_digest"])) == 64
    row = store.fetch_one("SELECT status FROM harnesses WHERE harness_id=?", (sibling.harness_id,))
    assert row["status"] == "active"


def test_static_assets_are_local_and_session_is_not_exposed_to_javascript(store, identity_factory) -> None:
    client, _, issued, _ = _client(store, identity_factory)
    css = client.get("/assets/console.css")
    js = client.get("/assets/console.js")

    assert css.status_code == 200
    assert js.status_code == 200
    assert "localStorage" not in js.text
    assert "sessionStorage" not in js.text
    assert "document.cookie" not in js.text
    assert "innerHTML" not in js.text
    assert issued.session_token not in client.get("/").text
    assert hashlib.sha256(css.content).hexdigest() in client.get("/").text


def test_initial_enrollment_submit_only_renders_an_exact_review(
    store, identity_factory
) -> None:
    client, _, _, _ = _client(store, identity_factory)

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
    client, _, _, _ = _client(store, identity_factory)

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
