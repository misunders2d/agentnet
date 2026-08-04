from __future__ import annotations

import hashlib
import time
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

import pytest
from starlette.testclient import TestClient

import agentnet.console.http as console_http
from agentnet.console.http import PREAUTH_COOKIE, SESSION_COOKIE, create_console_app
from agentnet.console.mutations import ConsoleMutationService
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.session import ConsoleOIDCCoordinator, ConsoleSessionService
from agentnet.errors import AuthenticationError, AuthorizationError
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc import OIDCVerificationResult


class _CurrentConsoleAuthority:
    def __init__(self) -> None:
        self.allowed = True
        self.calls: list[tuple[str, str]] = []

    def require(self, *, actor, action: str, resource: str, context=None):
        del actor, context
        self.calls.append((action, resource))
        if not self.allowed:
            raise AuthorizationError("console authority revoked")


class _Provider:
    def __init__(self, *, identity: VerifiedOIDCIdentity, clock: list[int]) -> None:
        self.identity = identity
        self.clock = clock
        self.exchange_hook = lambda: None
        self.exchanges = 0

    def authorization_url(self, *, state: str, nonce: str, code_challenge: str) -> str:
        return "https://idp.example/authorize?" f"state={state}&nonce={nonce}&code_challenge={code_challenge}"

    def exchange_and_verify(self, *, code: str, code_verifier: str, expected_nonce_hash: str) -> OIDCVerificationResult:
        assert code == "authorization-code"
        assert code_verifier
        assert len(expected_nonce_hash) == 64
        self.exchanges += 1
        self.exchange_hook()
        return OIDCVerificationResult(
            identity=self.identity,
            id_token_hash=hashlib.sha256(b"id-token").hexdigest(),
            expires_at=self.clock[0] + 300,
        )


class _ApprovalRecorder:
    config = SimpleNamespace(public_origin="https://approval.example")

    def create_request(self, **request):
        return {
            "request_id": "approval-request-0001",
            "transaction_digest": request["transaction_digest"],
            "expires_at": int(time.time()) + 300,
        }


class _ConsoleConfig:
    public_origin = "https://console.example"


def _stack(store, identity_factory):
    actor, _ = identity_factory(domain="corp.example", binding_assurance="hardware_bound")
    principal = store.fetch_one(
        "SELECT oidc_issuer,oidc_subject,verified_email FROM principals WHERE principal_id=?",
        (actor.principal_id,),
    )
    clock = [int(time.time())]
    authority = _CurrentConsoleAuthority()
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=900,
        challenge_ttl_seconds=300,
        mutation_ttl_seconds=120,
        require=authority.require,
        clock=lambda: clock[0],
    )
    provider = _Provider(
        identity=VerifiedOIDCIdentity(
            issuer=str(principal["oidc_issuer"]),
            subject=str(principal["oidc_subject"]),
            verified_email=str(principal["verified_email"]),
        ),
        clock=clock,
    )
    oidc = ConsoleOIDCCoordinator(sessions=sessions, provider=provider, preauth_ttl_seconds=300)
    return actor, authority, clock, sessions, provider, oidc


def _completed_handoff(sessions: ConsoleSessionService, actor):
    challenge = sessions.begin_challenge(actor=actor)
    return sessions.complete_challenge(
        actor=actor,
        challenge_id=challenge.challenge_id,
        transaction_digest=challenge.transaction_digest,
    )


def test_signed_completion_returns_only_an_opaque_short_lived_handoff(store, identity_factory) -> None:
    actor, _, _, sessions, _, _ = _stack(store, identity_factory)
    challenge = sessions.begin_challenge(actor=actor)

    completed = sessions.complete_challenge(
        actor=actor,
        challenge_id=challenge.challenge_id,
        transaction_digest=challenge.transaction_digest,
    )

    assert completed.handoff_token
    assert challenge.challenge_id not in completed.handoff_token
    assert completed.expires_at <= challenge.expires_at
    assert not hasattr(completed, "launch_url")
    row = store.fetch_one(
        "SELECT handoff_hash,handoff_expires_at,handoff_consumed_at FROM console_session_challenges WHERE challenge_id=?",
        (challenge.challenge_id,),
    )
    assert row["handoff_hash"] == hashlib.sha256(completed.handoff_token.encode("ascii")).hexdigest()
    assert row["handoff_expires_at"] == completed.expires_at
    assert row["handoff_consumed_at"] is None
    with pytest.raises((AuthenticationError, AuthorizationError)):
        sessions.complete_challenge(
            actor=actor,
            challenge_id=challenge.challenge_id,
            transaction_digest=challenge.transaction_digest,
        )


def test_handoff_is_post_only_atomic_and_one_use(store, identity_factory) -> None:
    actor, _, _, sessions, _, oidc = _stack(store, identity_factory)
    handoff = _completed_handoff(sessions, actor)
    reads = ConsoleReadService(store=store, require=_CurrentConsoleAuthority().require)
    mutations = ConsoleMutationService(store=store, approval_client=_ApprovalRecorder(), require=_CurrentConsoleAuthority().require)
    app = create_console_app(
        sessions=sessions,
        read_service=reads,
        mutation_service=mutations,
        public_origin="https://console.example",
        oidc=oidc,
    )
    client = TestClient(app, base_url="https://console.example")

    assert client.get("/v1/console/open", follow_redirects=False).status_code == 405
    opened = client.post(
        "/v1/console/open",
        data={"handoff_token": handoff.handoff_token},
        follow_redirects=False,
    )
    assert opened.status_code == 303
    assert opened.headers["location"].startswith("https://idp.example/")
    replay = client.post(
        "/v1/console/open",
        data={"handoff_token": handoff.handoff_token},
        follow_redirects=False,
    )
    assert replay.status_code == 401


def test_external_oidc_callback_uses_lax_preauth_and_strict_final_cookie(store, identity_factory) -> None:
    actor, _, _, sessions, _, oidc = _stack(store, identity_factory)
    handoff = _completed_handoff(sessions, actor)
    reads = ConsoleReadService(store=store, require=_CurrentConsoleAuthority().require)
    mutations = ConsoleMutationService(store=store, approval_client=_ApprovalRecorder(), require=_CurrentConsoleAuthority().require)
    client = TestClient(
        create_console_app(
            sessions=sessions,
            read_service=reads,
            mutation_service=mutations,
            public_origin="https://console.example",
            oidc=oidc,
        ),
        base_url="https://console.example",
    )

    opened = client.post(
        "/v1/console/open",
        data={"handoff_token": handoff.handoff_token},
        follow_redirects=False,
    )
    preauth_cookie = opened.headers["set-cookie"]
    assert PREAUTH_COOKIE in preauth_cookie
    assert "HttpOnly" in preauth_cookie
    assert "Secure" in preauth_cookie
    assert "SameSite=lax" in preauth_cookie
    state = parse_qs(urlsplit(opened.headers["location"]).query)["state"][0]

    callback = client.get(
        "/v1/console/oidc/callback",
        params={"state": state, "code": "authorization-code"},
        follow_redirects=False,
    )
    cookie_headers = callback.headers.get_list("set-cookie")
    assert callback.status_code == 303
    assert any(
        SESSION_COOKIE in value and "HttpOnly" in value and "Secure" in value and "SameSite=strict" in value
        for value in cookie_headers
    )
    replay = client.get(
        "/v1/console/oidc/callback",
        params={"state": state, "code": "authorization-code"},
        follow_redirects=False,
    )
    assert replay.status_code == 401


def test_oidc_completion_refreshes_clock_after_provider_exchange(store, identity_factory) -> None:
    actor, _, clock, sessions, provider, oidc = _stack(store, identity_factory)
    handoff = _completed_handoff(sessions, actor)
    begun = oidc.begin(handoff_token=handoff.handoff_token)
    provider.exchange_hook = lambda: clock.__setitem__(0, begun.expires_at)

    with pytest.raises(AuthenticationError, match="console session denied"):
        oidc.complete(state=begun.state, code="authorization-code", preauth_token=begun.preauth_token)
    with pytest.raises(AuthenticationError, match="console session denied"):
        oidc.complete(state=begun.state, code="authorization-code", preauth_token=begun.preauth_token)
    assert provider.exchanges == 1
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_browser_sessions")["n"] == 0


def test_console_open_authority_is_rechecked_after_oidc_and_on_every_session_use(store, identity_factory) -> None:
    actor, authority, _, sessions, provider, oidc = _stack(store, identity_factory)
    handoff = _completed_handoff(sessions, actor)
    begun = oidc.begin(handoff_token=handoff.handoff_token)
    provider.exchange_hook = lambda: setattr(authority, "allowed", False)

    with pytest.raises(AuthenticationError, match="console session denied"):
        oidc.complete(state=begun.state, code="authorization-code", preauth_token=begun.preauth_token)

    authority.allowed = True
    issued = sessions.issue_for_verified_actor(actor=actor)
    assert sessions.authenticate(issued.session_token).actor == actor
    authority.allowed = False
    with pytest.raises(AuthenticationError, match="console session denied"):
        sessions.authenticate(issued.session_token)
    assert authority.calls[-1] == ("console.session.open", "console-domain:corp.example")


def test_mutation_authorization_is_session_method_path_body_bound_and_one_use(store, identity_factory) -> None:
    actor, _, _, sessions, _, _ = _stack(store, identity_factory)
    issued = sessions.issue_for_verified_actor(actor=actor)
    exact_form = {"reason": ["canonical non-token body"]}
    token = sessions.issue_mutation_authorization(
        session_token=issued.session_token,
        method="POST",
        path="/harnesses/harness-1/revoke",
        form=exact_form,
    )

    with pytest.raises(AuthorizationError, match="console mutation denied"):
        sessions.require_mutation(
            session_token=issued.session_token,
            authorization_token=token,
            method="POST",
            path="/enrollments",
            form={**exact_form, "mutation_token": [token]},
        )
    with pytest.raises(AuthorizationError, match="console mutation denied"):
        sessions.require_mutation(
            session_token=issued.session_token,
            authorization_token=token,
            method="POST",
            path="/harnesses/harness-1/revoke",
            form={"reason": ["different body"], "mutation_token": [token]},
        )

    status = sessions.require_mutation(
        session_token=issued.session_token,
        authorization_token=token,
        method="POST",
        path="/harnesses/harness-1/revoke",
        form={**exact_form, "mutation_token": [token]},
    )
    assert status.session_id == issued.session_id
    with pytest.raises(AuthorizationError, match="console mutation denied"):
        sessions.require_mutation(
            session_token=issued.session_token,
            authorization_token=token,
            method="POST",
            path="/harnesses/harness-1/revoke",
            form={**exact_form, "mutation_token": [token]},
        )


def test_http_signed_completion_response_contains_no_credential_url(store, identity_factory, monkeypatch) -> None:
    actor, authority, _, sessions, _, oidc = _stack(store, identity_factory)
    reads = ConsoleReadService(store=store, require=authority.require)
    mutations = ConsoleMutationService(store=store, approval_client=_ApprovalRecorder(), require=authority.require)
    core = SimpleNamespace(
        config=SimpleNamespace(admin_console=_ConsoleConfig()),
        console_sessions=sessions,
        console_reads=reads,
        console_mutations=mutations,
        console_oidc=oidc,
        sponsored_enrollment=None,
        _require=lambda **kwargs: authority.require(
            actor=kwargs["actor"],
            action=kwargs["action"],
            resource=kwargs["resource"],
        ),
    )

    async def authenticated(request, authenticated_core):
        assert authenticated_core is core
        return await request.body(), SimpleNamespace(actor=actor)

    monkeypatch.setattr(console_http, "authenticate_proof_request", authenticated)
    client = TestClient(create_console_app(core), base_url="https://console.example")
    begun = client.post("/v1/console/session-challenges").json()
    completed = client.post(
        f"/v1/console/session-challenges/{begun['challenge_id']}/complete",
        json={"transaction_digest": begun["transaction_digest"]},
    )

    assert completed.status_code == 200
    assert set(completed.json()) == {"schema", "handoff_token", "expires_at"}
    assert completed.json()["schema"] == "agentnet.console.session-handoff.v1"
    assert "url" not in completed.text.casefold()
    assert begun["challenge_id"] not in completed.text
