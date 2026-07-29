from __future__ import annotations

import asyncio
import hashlib
import json
from types import SimpleNamespace

import httpx
import pytest

from agentnet.approval.http import SECURITY_HEADERS, create_approval_app
from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    build_internal_broker_proof,
)
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.security.signatures import b64url_encode, canonical_json


TOKEN = "agcap1." + "A" * 43
NOW = 1_800_000_000


class FakeReplayStore:
    def __init__(self) -> None:
        self.seen: set[tuple[str, str]] = set()
        self.calls: list[dict[str, object]] = []

    def consume_internal_broker_replay(self, **kwargs: object) -> None:
        key = (str(kwargs["key_id"]), str(kwargs["nonce"]))
        if key in self.seen:
            raise AuthenticationError("approval request denied")
        self.seen.add(key)
        self.calls.append(dict(kwargs))


class FakeOwnerSessions:
    def __init__(self) -> None:
        self.calls: list[tuple[str, object]] = []
        self.provider = SimpleNamespace(
            config=SimpleNamespace(
                authorization_ttl_seconds=300,
                issuer="https://idp.example",
            )
        )

    def create_preauth(self):
        self.calls.append(("create_preauth", None))
        return SimpleNamespace(session_token="P" * 43, csrf_token="C" * 43)

    def begin_oidc_login(self, **kwargs):
        self.calls.append(("begin_oidc_login", kwargs))
        return SimpleNamespace(
            authorization_url="https://idp.example/authorize?state=browser-confined",
            expires_at=NOW + 300,
        )

    def complete_oidc_login(self, **kwargs):
        self.calls.append(("complete_oidc_login", kwargs))
        return SimpleNamespace(
            session_token="S" * 43,
            csrf_token="N" * 43,
            expires_at=NOW + 900,
        )

    def fail_oidc_login(self, **kwargs):
        self.calls.append(("fail_oidc_login", kwargs))

    def session_status(self, session_token: str):
        self.calls.append(("session_status", session_token))
        if session_token != "S" * 43:
            raise AuthenticationError("owner session denied")
        return SimpleNamespace(
            authenticated=True,
            csrf_token="N" * 43,
            expires_at=NOW + 900,
            credential_registered=False,
        )

    def begin_registration(self, **kwargs):
        self.calls.append(("begin_registration", kwargs))
        return SimpleNamespace(
            ceremony_id="ceremony-123456789",
            expires_at=NOW + 180,
            public_key={"challenge": "AA", "user": {"id": "AA"}},
        )

    def complete_registration(self, **kwargs):
        self.calls.append(("complete_owner_registration", kwargs))
        return {
            "schema": "agentnet.approval.owner-registration-result.v1",
            "registered": True,
        }

    def pending_approvals(self, **kwargs):
        self.calls.append(("pending_approvals", kwargs))
        return [
            {
                "request_id": "request-123456789",
                "approval_purpose": "identity.enrollment.approve",
                "state": "pending",
                "created_at": NOW,
                "expires_at": NOW + 300,
            }
        ]

    def begin_approval(self, **kwargs):
        self.calls.append(("begin_approval", kwargs))
        return {
            "schema": "agentnet.approval.owner-request-options.v1",
            "request_id": kwargs["request_id"],
            "expires_at": NOW + 300,
            "challenge_expires_at": NOW + 180,
            "summary": {
                "title": "Enroll a laptop identity",
                "statements": ["Authority granted: none"],
                "advanced_digest": "a" * 64,
            },
            "publicKey": {"challenge": "AA", "allowCredentials": []},
        }

    def complete_approval(self, **kwargs):
        self.calls.append(("complete_approval", kwargs))
        return {
            "schema": "agentnet.approval.owner-request-result.v1",
            "approved": True,
            "claim_code": "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111",
            "claim_code_expires_at": NOW + 300,
        }

    def reject_approval(self, **kwargs):
        self.calls.append(("reject_approval", kwargs))
        return {
            "schema": "agentnet.approval.owner-request-rejection.v1",
            "rejected": True,
        }

    def regenerate_approval_code(self, **kwargs):
        self.calls.append(("regenerate_approval_code", kwargs))
        return self.complete_approval(**kwargs, credential={})


class FakeApprovalService:
    def __init__(self, *, max_body: int = 4096, owner_sessions=None) -> None:
        self.config = SimpleNamespace(
            max_http_body_bytes=max_body,
            public_origin="https://approval.corp.example",
            verifier_id="approval.corp.example",
            owner_oidc=SimpleNamespace() if owner_sessions is not None else None,
        )
        self.owner_sessions = owner_sessions
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


def test_health_identifies_exact_approval_service() -> None:
    async def exercise() -> None:
        app = create_approval_app(FakeApprovalService())
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            response = await client.get("/healthz")
        assert response.status_code == 200
        assert response.json()["schema"] == "agentnet.approval.health.v1"
        assert response.json()["service"] == "agentnet-approval"
        assert response.json()["status"] == "alive"
        assert response.json()["public_origin"] == "https://approval.corp.example"
        assert response.json()["verifier_id"] == "approval.corp.example"

    asyncio.run(exercise())


def test_owner_oidc_app_requires_matching_owner_session_service() -> None:
    service = FakeApprovalService()
    service.config.owner_oidc = SimpleNamespace()
    with pytest.raises(ValueError, match="owner OIDC requires owner session service"):
        create_approval_app(service)

    service = FakeApprovalService(owner_sessions=FakeOwnerSessions())
    service.config.owner_oidc = None
    with pytest.raises(ValueError, match="owner session service requires owner OIDC"):
        create_approval_app(service)


def test_owner_oidc_callback_accepts_unique_extensions_and_rejects_ambiguous_shapes() -> None:
    async def exercise() -> None:
        success_owner = FakeOwnerSessions()
        success_app = create_approval_app(FakeApprovalService(owner_sessions=success_owner))
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=success_app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            await client.get("/v1/approval/owner/session")
            success = await client.get(
                "/v1/approval/owner/oidc/callback",
                params={
                    "state": "T" * 43,
                    "code": "authorization-code",
                    "scope": "openid email",
                    "authuser": "0",
                    "prompt": "consent",
                },
                follow_redirects=False,
            )
            assert success.status_code == 303
            assert success_owner.calls[-1] == (
                "complete_oidc_login",
                {
                    "preauth_cookie": "P" * 43,
                    "state": "T" * 43,
                    "code": "authorization-code",
                },
            )

        error_owner = FakeOwnerSessions()
        error_app = create_approval_app(FakeApprovalService(owner_sessions=error_owner))
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=error_app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            await client.get("/v1/approval/owner/session")
            malformed_queries = (
                [
                    ("state", "T" * 43),
                    ("code", "authorization-code"),
                    ("scope", "openid"),
                    ("scope", "email"),
                ],
                [
                    ("state", "T" * 43),
                    ("code", "authorization-code"),
                    ("error", "access_denied"),
                ],
                [
                    ("state", "T" * 43),
                    ("code", "authorization-code"),
                    ("error_description", "denied"),
                ],
                [
                    ("state", "T" * 43),
                    ("error", "access_denied"),
                    ("error", "server_error"),
                ],
            )
            for query in malformed_queries:
                denied = await client.get(
                    "/v1/approval/owner/oidc/callback",
                    params=query,
                    follow_redirects=False,
                )
                assert denied.status_code == 400
            assert not any(call[0] == "complete_oidc_login" for call in error_owner.calls)
            assert not any(call[0] == "fail_oidc_login" for call in error_owner.calls)

            provider_error = await client.get(
                "/v1/approval/owner/oidc/callback",
                params={
                    "state": "T" * 43,
                    "error": "access_denied_sensitive",
                    "error_description": "owner-canceled-sensitive",
                    "error_uri": "https://idp.example/errors/private-sensitive",
                    "authuser": "0",
                },
                follow_redirects=False,
            )
            assert provider_error.status_code == 400
            assert "access_denied_sensitive" not in provider_error.text
            assert "owner-canceled-sensitive" not in provider_error.text
            assert "private-sensitive" not in provider_error.text
            assert error_owner.calls[-1] == (
                "fail_oidc_login",
                {"preauth_cookie": "P" * 43, "state": "T" * 43},
            )
            assert not any(call[0] == "complete_oidc_login" for call in error_owner.calls)

    asyncio.run(exercise())


def test_browser_page_uses_stable_owner_session_without_fragment_capability() -> None:
    async def exercise() -> None:
        owner = FakeOwnerSessions()
        app = create_approval_app(FakeApprovalService(owner_sessions=owner))
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
            for forbidden in (
                "location.hash",
                "fragment.get('token')",
                "history.replaceState",
                "agcap1.",
            ):
                assert forbidden not in script.text
            assert "navigator.credentials.create" in script.text
            assert "/v1/approval/owner/session" in script.text
            assert "/v1/approval/owner/oidc/start" in script.text
            assert "/v1/approval/owner/registration/begin" in script.text
            assert "/v1/approval/owner/requests/options" in script.text
            assert "navigator.credentials.get" in script.text
            assert "Waiting for the AgentNet request" in script.text
            assert "window.setTimeout(() => loadRequests().catch(deny), 500)" in script.text
            assert "One-time approval code" in page.text
            assert script.headers["cache-control"] == "no-store"

            initial = await client.get("/v1/approval/owner/session")
            assert initial.status_code == 200
            assert initial.json() == {
                "schema": "agentnet.approval.owner-session-status.v1",
                "authenticated": False,
                "csrf_token": "C" * 43,
            }
            set_cookies = initial.headers.get_list("set-cookie")
            cookies = "\n".join(set_cookies)
            preauth_cookie = next(
                value
                for value in set_cookies
                if value.startswith("__Host-agentnet-approval-preauth=")
            )
            csrf_cookie = next(
                value
                for value in set_cookies
                if value.startswith("__Host-agentnet-approval-csrf=")
            )
            assert "Secure" in preauth_cookie and "HttpOnly" in preauth_cookie
            assert "SameSite=lax" in preauth_cookie
            assert "Secure" in csrf_cookie and "HttpOnly" not in csrf_cookie
            assert "SameSite=strict" in csrf_cookie

            missing_origin = await client.post(
                "/v1/approval/owner/oidc/start",
                json={
                    "schema": "agentnet.approval.owner-oidc-start.v1",
                    "csrf_token": "C" * 43,
                },
            )
            assert missing_origin.status_code == 400

            started = await client.post(
                "/v1/approval/owner/oidc/start",
                headers={"Origin": "https://approval.corp.example"},
                json={
                    "schema": "agentnet.approval.owner-oidc-start.v1",
                    "csrf_token": "C" * 43,
                },
            )
            assert started.status_code == 200
            assert started.json()["authorization_url"].startswith("https://idp.example/")

            callback = await client.get(
                "/v1/approval/owner/oidc/callback",
                params={
                    "state": "T" * 43,
                    "code": "authorization-code",
                    "scope": "openid email",
                    "authuser": "0",
                },
                follow_redirects=False,
            )
            assert callback.status_code == 303
            assert callback.headers["location"] == "/approval"
            callback_cookies = "\n".join(callback.headers.get_list("set-cookie"))
            assert "__Host-agentnet-approval=" in callback_cookies
            assert "HttpOnly" in callback_cookies
            assert "__Host-agentnet-approval-preauth=\"\"" in callback_cookies

            status = await client.get("/v1/approval/owner/session")
            assert status.status_code == 200
            assert status.json()["authenticated"] is True
            assert status.json()["credential_registered"] is False

            wrong_origin = await client.post(
                "/v1/approval/owner/registration/begin",
                headers={"Origin": "https://attacker.example"},
                json={
                    "schema": "agentnet.approval.owner-registration-begin.v1",
                    "csrf_token": "N" * 43,
                },
            )
            assert wrong_origin.status_code == 400

            begun = await client.post(
                "/v1/approval/owner/registration/begin",
                headers={"Origin": "https://approval.corp.example"},
                json={
                    "schema": "agentnet.approval.owner-registration-begin.v1",
                    "csrf_token": "N" * 43,
                },
            )
            assert begun.status_code == 200
            assert begun.json()["ceremony_id"] == "ceremony-123456789"

            completed = await client.post(
                "/v1/approval/owner/registration/complete",
                headers={"Origin": "https://approval.corp.example"},
                json={
                    "schema": "agentnet.approval.owner-registration-complete.v1",
                    "csrf_token": "N" * 43,
                    "ceremony_id": "ceremony-123456789",
                    "credential": {"id": "credential"},
                },
            )
            assert completed.status_code == 200
            assert completed.json()["registered"] is True

            pending = await client.get("/v1/approval/owner/requests")
            assert pending.status_code == 200
            assert pending.json()["requests"][0]["state"] == "pending"

            owner_options = await client.post(
                "/v1/approval/owner/requests/options",
                headers={"Origin": "https://approval.corp.example"},
                json={
                    "schema": "agentnet.approval.owner-request-select.v1",
                    "csrf_token": "N" * 43,
                    "request_id": "request-123456789",
                },
            )
            assert owner_options.status_code == 200
            assert owner_options.json()["summary"]["title"] == "Enroll a laptop identity"
            assert "canonical_transaction_text" not in owner_options.text

            owner_completed = await client.post(
                "/v1/approval/owner/requests/complete",
                headers={"Origin": "https://approval.corp.example"},
                json={
                    "schema": "agentnet.approval.owner-request-complete.v1",
                    "csrf_token": "N" * 43,
                    "request_id": "request-123456789",
                    "credential": {"id": "credential"},
                },
            )
            assert owner_completed.status_code == 200
            assert owner_completed.json()["approved"] is True
            assert owner_completed.json()["claim_code"].startswith("AAAA-")

            wrong_owner_origin = await client.post(
                "/v1/approval/owner/requests/reject",
                headers={"Origin": "https://attacker.example"},
                json={
                    "schema": "agentnet.approval.owner-request-select.v1",
                    "csrf_token": "N" * 43,
                    "request_id": "request-123456789",
                },
            )
            assert wrong_owner_origin.status_code == 400

            for legacy_path in (
                "/v1/approval/registration/options",
                "/v1/approval/requests/options",
                "/v1/approval/requests/verify",
                "/v1/approval/requests/reject",
            ):
                legacy = await client.post(legacy_path, json={"token": TOKEN})
                assert legacy.status_code == 404

    asyncio.run(exercise())


def test_lab_browser_page_preserves_legacy_fragment_ceremony() -> None:
    async def exercise() -> None:
        app = create_approval_app(FakeApprovalService())
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            page = await client.get("/approval")
            script = await client.get("/approval.js")
            assert page.status_code == script.status_code == 200
            assert "AgentNet Independent Approval" in page.text
            for expected in (
                "location.hash",
                "fragment.get('token')",
                "history.replaceState",
                "/v1/approval/registration/options",
                "/v1/approval/requests/options",
                "/v1/approval/requests/verify",
                "/v1/approval/requests/reject",
            ):
                assert expected in script.text
            assert "/v1/approval/owner/session" not in script.text
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
                public_origin="https://approval.corp.example",
                owner_oidc=object(),
            )
            self.owner_sessions = object()
            self.clock = lambda: NOW
            self.store = FakeReplayStore()

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
            if getattr(self, "failure", None) is not None:
                raise self.failure
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
        possession_secret = "P" * 43
        possession_hash = hashlib.sha256(possession_secret.encode("ascii")).hexdigest()
        create_body = {
            "schema": "agentnet.approval.internal-request-create.v2",
            "idempotency_key": "core:enrollment:test-1",
            "approver_principal_id": "security-owner",
            "domain_id": "corp.example",
            "approval_purpose": "identity.enrollment.approve",
            "canonical_transaction_b64": b64url_encode(canonical),
            "transaction_digest": digest,
            "possession_hash": possession_hash,
            "request_expires_at": 1_800_000_300,
        }
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="https://approval.corp.example",
        ) as client:
            denied = await client.post("/v1/approval/internal/requests", json=create_body)
            assert denied.status_code == 400
            assert denied.json() == {"error": "request_denied"}
            assert service.calls == []

            bearer = {"Authorization": f"Bearer {secret}"}
            bearer_only = await client.post(
                "/v1/approval/internal/requests",
                content=canonical_json(create_body),
                headers={**bearer, "Content-Type": "application/json"},
            )
            assert bearer_only.status_code == 400
            assert service.calls == []
            assert service.store.calls == []

            def signed_headers(
                path: str,
                purpose: str,
                body: dict[str, object],
                *,
                nonce: bytes,
            ) -> dict[str, str]:
                raw = canonical_json(body)
                return {
                    **bearer,
                    "Content-Type": "application/json",
                    INTERNAL_BROKER_PROOF_HEADER: build_internal_broker_proof(
                        credential=secret,
                        audience="https://approval.corp.example",
                        method="POST",
                        path=path,
                        purpose=purpose,
                        raw_body=raw,
                        now=NOW,
                        nonce=nonce,
                    ),
                }

            create_raw = canonical_json(create_body)
            create_headers = signed_headers(
                "/v1/approval/internal/requests",
                INTERNAL_BROKER_PURPOSE_CREATE,
                create_body,
                nonce=b"c" * 32,
            )
            created = await client.post(
                "/v1/approval/internal/requests",
                content=create_raw,
                headers=create_headers,
            )
            assert created.status_code == 201
            assert service.calls[0][1]["request_expires_at"] == 1_800_000_300
            created_text = created.text
            assert "approval_url" not in created_text
            assert "agcap1." not in created_text
            assert secret not in created_text

            replay = await client.post(
                "/v1/approval/internal/requests",
                content=create_raw,
                headers=create_headers,
            )
            assert replay.status_code == 400
            assert [name for name, _value in service.calls] == ["create_request"]

            status_body = {
                "schema": "agentnet.approval.internal-request-status.v1",
                "request_id": "request-1",
                "transaction_digest": digest,
            }
            status_path = "/v1/approval/internal/requests/status"
            status = await client.post(
                status_path,
                content=canonical_json(status_body),
                headers=signed_headers(
                    status_path,
                    INTERNAL_BROKER_PURPOSE_STATUS,
                    status_body,
                    nonce=b"s" * 32,
                ),
            )
            assert status.status_code == 200
            assert status.json()["state"] == "issued"

            retrieve_body = {
                "schema": "agentnet.approval.internal-receipt-retrieve.v2",
                "request_id": "request-1",
                "possession_secret": possession_secret,
                "domain_id": "corp.example",
                "approval_purpose": "identity.enrollment.approve",
                "transaction_digest": digest,
                "idempotency_key": "core:enrollment-complete:test-1",
            }
            retrieve_path = "/v1/approval/internal/receipts/retrieve"
            retrieved = await client.post(
                retrieve_path,
                content=canonical_json(retrieve_body),
                headers=signed_headers(
                    retrieve_path,
                    INTERNAL_BROKER_PURPOSE_RETRIEVE,
                    retrieve_body,
                    nonce=b"r" * 32,
                ),
            )
            assert retrieved.status_code == 200
            assert retrieved.json()["receipt"]["value"] == "safe"
            assert secret not in retrieved.text
            assert len(service.store.calls) == 3

            non_ascii_retrieve = {
                **retrieve_body,
                "possession_secret": "é" * 16,
                "idempotency_key": "core:enrollment-complete:non-ascii",
            }
            calls_before_invalid = list(service.calls)
            invalid_retrieve = await client.post(
                retrieve_path,
                content=canonical_json(non_ascii_retrieve),
                headers=signed_headers(
                    retrieve_path,
                    INTERNAL_BROKER_PURPOSE_RETRIEVE,
                    non_ascii_retrieve,
                    nonce=b"v" * 32,
                ),
            )
            assert invalid_retrieve.status_code == 400
            assert invalid_retrieve.json() == {"error": "request_denied"}
            assert service.calls == calls_before_invalid

            noncanonical_raw = json.dumps(status_body, indent=1).encode("utf-8")
            noncanonical = await client.post(
                status_path,
                content=noncanonical_raw,
                headers={
                    **bearer,
                    "Content-Type": "application/json",
                    INTERNAL_BROKER_PROOF_HEADER: build_internal_broker_proof(
                        credential=secret,
                        audience="https://approval.corp.example",
                        method="POST",
                        path=status_path,
                        purpose=INTERNAL_BROKER_PURPOSE_STATUS,
                        raw_body=noncanonical_raw,
                        now=NOW,
                        nonce=b"n" * 32,
                    ),
                },
            )
            assert noncanonical.status_code == 400
            assert len(service.store.calls) == 5
            assert [name for name, _value in service.calls] == [
                "create_request",
                "request_status",
                "retrieve_core_receipt",
            ]

            query_denied = await client.post(
                status_path + "?unexpected=1",
                content=canonical_json(status_body),
                headers=signed_headers(
                    status_path,
                    INTERNAL_BROKER_PURPOSE_STATUS,
                    status_body,
                    nonce=b"q" * 32,
                ),
            )
            assert query_denied.status_code == 400
            assert len(service.store.calls) == 5

            duplicate_proof = signed_headers(
                status_path,
                INTERNAL_BROKER_PURPOSE_STATUS,
                status_body,
                nonce=b"d" * 32,
            )[INTERNAL_BROKER_PROOF_HEADER]
            duplicate_header_denied = await client.post(
                status_path,
                content=canonical_json(status_body),
                headers=[
                    ("Authorization", f"Bearer {secret}"),
                    ("Content-Type", "application/json"),
                    (INTERNAL_BROKER_PROOF_HEADER, duplicate_proof),
                    (INTERNAL_BROKER_PROOF_HEADER, duplicate_proof),
                ],
            )
            assert duplicate_header_denied.status_code == 400
            assert len(service.store.calls) == 5

            malformed = await client.post(
                status_path,
                content=canonical_json(status_body),
                headers={
                    **bearer,
                    "Content-Type": "application/json",
                    INTERNAL_BROKER_PROOF_HEADER: "not-a-valid-proof",
                },
            )
            assert malformed.status_code == 400
            assert len(service.store.calls) == 5

            for nonce, failure in (
                (b"u" * 32, GateBlocked("approval_store", "private outage detail")),
                (b"x" * 32, RuntimeError("private exception detail")),
            ):
                service.failure = failure
                unavailable = await client.post(
                    status_path,
                    content=canonical_json(status_body),
                    headers=signed_headers(
                        status_path,
                        INTERNAL_BROKER_PURPOSE_STATUS,
                        status_body,
                        nonce=nonce,
                    ),
                )
                assert unavailable.status_code == 503
                assert unavailable.json() == {"error": "request_unavailable"}
                assert "private" not in unavailable.text

    asyncio.run(exercise())


@pytest.mark.parametrize("runtime_reference", (None, "AGENTNET_TEST_APPROVAL_CORE_TOKEN"))
def test_internal_routes_require_both_runtime_reference_and_stable_owner_profile(
    runtime_reference: str | None,
) -> None:
    class LegacyService(FakeApprovalService):
        def __init__(self) -> None:
            super().__init__()
            self.config.internal_core_credential_env = runtime_reference

    async def exercise() -> None:
        app = create_approval_app(LegacyService())
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
