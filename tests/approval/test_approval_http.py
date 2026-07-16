from __future__ import annotations

import asyncio
import hashlib
from types import SimpleNamespace

import httpx
import pytest

from agentnet.approval.http import SECURITY_HEADERS, create_approval_app
from agentnet.security.signatures import b64url_encode


TOKEN = "agcap1." + "A" * 43


class FakeApprovalService:
    def __init__(self, *, max_body: int = 4096) -> None:
        self.config = SimpleNamespace(max_http_body_bytes=max_body)
        self.calls: list[tuple[str, object]] = []

    def registration_options(self, token: str):
        self.calls.append(("registration_options", token))
        return {"publicKey": {"challenge": "AA", "user": {"id": "AA"}}}

    def complete_registration(self, token: str, credential):
        self.calls.append(("complete_registration", token))
        return {"registered": True}

    def request_options(self, token: str):
        self.calls.append(("request_options", token))
        return {
            "approval_purpose": "identity.enrollment.approve",
            "domain_id": "corp.example",
            "transaction_digest": "a" * 64,
            "canonical_transaction_text": "{}",
            "publicKey": {"challenge": "AA", "allowCredentials": []},
        }

    def approve_request(self, token: str, credential, *, approved: bool):
        self.calls.append(("approve_request", approved))
        return {"approved": True}

    def reject_request(self, token: str):
        self.calls.append(("reject_request", token))
        return {"status": "rejected"}


def test_browser_page_uses_fragment_cleanup_external_script_and_security_headers() -> None:
    async def exercise() -> None:
        app = create_approval_app(FakeApprovalService())
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            page = await client.get("/approval")
            assert page.status_code == 200
            assert "#token=" not in page.text
            assert '<script src="/approval.js" defer></script>' in page.text
            assert "<script>" not in page.text
            for name, value in SECURITY_HEADERS.items():
                assert page.headers[name] == value
            script = await client.get("/approval.js")
            assert "history.replaceState" in script.text
            assert "navigator.credentials.create" in script.text
            assert "navigator.credentials.get" in script.text
            assert "options.delivery_mode === 'core_claim_code'" in script.text
            assert "result.claim_code" in script.text
            assert script.headers["cache-control"] == "no-store"

    asyncio.run(exercise())


def test_strict_json_duplicate_keys_and_generic_errors_fail_closed() -> None:
    async def exercise() -> None:
        service = FakeApprovalService()
        app = create_approval_app(service)
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            ok = await client.post(
                "/v1/approval/requests/options",
                json={"token": TOKEN},
            )
            assert ok.status_code == 200
            assert service.calls == [("request_options", TOKEN)]
            duplicate = await client.post(
                "/v1/approval/requests/options",
                content=(f'{{"token":"{TOKEN}","token":"{TOKEN}"}}').encode(),
                headers={"Content-Type": "application/json"},
            )
            assert duplicate.status_code == 400
            assert duplicate.json() == {"error": "request_denied"}
            malformed = await client.post(
                "/v1/approval/requests/options",
                content=b'{"token":NaN}',
                headers={"Content-Type": "application/json"},
            )
            assert malformed.status_code == 400
            assert malformed.json() == {"error": "request_denied"}
            assert service.calls == [("request_options", TOKEN)]

    asyncio.run(exercise())


def test_internal_routes_require_runtime_secret_and_never_return_approval_url(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class InternalService(FakeApprovalService):
        def __init__(self) -> None:
            super().__init__()
            self.config = SimpleNamespace(
                max_http_body_bytes=4096,
                internal_core_credential_env="AGENTNET_TEST_APPROVAL_CORE_TOKEN",
            )

        def create_request(self, **kwargs):
            self.calls.append(("create_request", kwargs))
            return SimpleNamespace(
                identifier="request-1",
                state="pending",
                transaction_digest=kwargs["canonical_transaction"].hex()[:64].ljust(64, "0"),
                expires_at=1_800_000_300,
                duplicate=False,
            )

        def request_status(self, **kwargs):
            self.calls.append(("request_status", kwargs))
            return {
                "schema": "agentnet.approval.internal-request-status-result.v1",
                "request_id": kwargs["request_id"],
                "state": "issued",
                "transaction_digest": kwargs["transaction_digest"],
                "expires_at": 1_800_000_300,
            }

        def retrieve_core_receipt(self, **kwargs):
            self.calls.append(("retrieve_core_receipt", kwargs))
            return {"schema": "agentnet.independent-approval.receipt.v1", "value": "safe"}

    async def exercise() -> None:
        secret = "S" * 43
        monkeypatch.setenv("AGENTNET_TEST_APPROVAL_CORE_TOKEN", secret)
        service = InternalService()
        app = create_approval_app(service)
        canonical = b'{"schema":"agentnet.test.v1"}'
        digest = hashlib.sha256(canonical).hexdigest()
        create_body = {
            "schema": "agentnet.approval.internal-request-create.v1",
            "idempotency_key": "core:enrollment:test-1",
            "approver_principal_id": "security-owner",
            "domain_id": "corp.example",
            "approval_purpose": "identity.enrollment.approve",
            "canonical_transaction_b64": b64url_encode(canonical),
            "transaction_digest": digest,
        }
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            denied = await client.post("/v1/approval/internal/requests", json=create_body)
            assert denied.status_code == 400
            assert denied.json() == {"error": "request_denied"}
            assert service.calls == []

            headers = {"Authorization": f"Bearer {secret}"}
            created = await client.post(
                "/v1/approval/internal/requests",
                json=create_body,
                headers=headers,
            )
            assert created.status_code == 201
            created_text = created.text
            assert "approval_url" not in created_text
            assert "agcap1." not in created_text
            assert secret not in created_text

            status = await client.post(
                "/v1/approval/internal/requests/status",
                json={
                    "schema": "agentnet.approval.internal-request-status.v1",
                    "request_id": "request-1",
                    "transaction_digest": digest,
                },
                headers=headers,
            )
            assert status.status_code == 200
            assert status.json()["state"] == "issued"

            retrieved = await client.post(
                "/v1/approval/internal/receipts/retrieve",
                json={
                    "schema": "agentnet.approval.internal-receipt-retrieve.v1",
                    "request_id": "request-1",
                    "claim_code": "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111",
                    "domain_id": "corp.example",
                    "approval_purpose": "identity.enrollment.approve",
                    "transaction_digest": digest,
                    "idempotency_key": "core:enrollment-complete:test-1",
                },
                headers=headers,
            )
            assert retrieved.status_code == 200
            assert retrieved.json()["receipt"]["value"] == "safe"
            assert secret not in retrieved.text

    asyncio.run(exercise())


def test_internal_routes_are_absent_without_runtime_reference() -> None:
    async def exercise() -> None:
        app = create_approval_app(FakeApprovalService())
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            response = await client.post("/v1/approval/internal/requests", json={})
            assert response.status_code == 404

    asyncio.run(exercise())


def test_body_limit_applies_without_content_length() -> None:
    async def exercise() -> None:
        service = FakeApprovalService(max_body=64)
        app = create_approval_app(service)
        body = (b'{"token":"' + TOKEN.encode() + b'","padding":"' + b"x" * 100 + b'"}')
        messages = iter(
            [
                {"type": "http.request", "body": body[:50], "more_body": True},
                {"type": "http.request", "body": body[50:], "more_body": False},
            ]
        )
        sent: list[dict[str, object]] = []

        async def receive():
            return next(messages)

        async def send(message):
            sent.append(message)

        await app(
            {
                "type": "http",
                "asgi": {"version": "3.0"},
                "http_version": "1.1",
                "method": "POST",
                "scheme": "https",
                "path": "/v1/approval/requests/options",
                "raw_path": b"/v1/approval/requests/options",
                "query_string": b"",
                "headers": [(b"content-type", b"application/json")],
                "client": ("127.0.0.1", 12345),
                "server": ("approval.corp.example", 443),
            },
            receive,
            send,
        )
        start = next(item for item in sent if item["type"] == "http.response.start")
        assert start["status"] == 400
        response_body = b"".join(
            item.get("body", b"") for item in sent if item["type"] == "http.response.body"
        )
        assert response_body == b'{"error":"request_denied"}'
        assert service.calls == []

    asyncio.run(exercise())
