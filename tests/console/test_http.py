from __future__ import annotations

import hashlib
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
        else:
            assert action == "identity.harness.revoke"
            assert resource.startswith("harness:")


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
    sessions = ConsoleSessionService(store=store, audience="https://console.example", ttl_seconds=900)
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


def test_sensitive_action_requires_same_origin_csrf_and_exact_confirmation(store, identity_factory) -> None:
    client, actor, issued, approvals = _client(store, identity_factory)
    sibling, _ = identity_factory(
        domain="corp.example",
        principal_id=actor.principal_id,
        binding_assurance="hardware_bound",
    )
    path = f"/harnesses/{sibling.harness_id}/revoke"

    missing = client.post(path, data={"reason": "Lost laptop"})
    assert missing.status_code == 403

    wrong = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={
            "csrf_token": issued.csrf_token,
            "reason": "Lost laptop",
            "confirmation": "Confirm",
            "idempotency_key": secrets.token_urlsafe(24),
        },
    )
    assert wrong.status_code == 400

    accepted = client.post(
        path,
        headers={"Origin": "https://console.example"},
        data={
            "csrf_token": issued.csrf_token,
            "reason": "Lost laptop",
            "confirmation": f"Remove access for {sibling.harness_id}",
            "idempotency_key": secrets.token_urlsafe(24),
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
