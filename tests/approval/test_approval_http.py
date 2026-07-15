from __future__ import annotations

import asyncio
from types import SimpleNamespace

import httpx

from agentnet.approval.http import SECURITY_HEADERS, create_approval_app


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
