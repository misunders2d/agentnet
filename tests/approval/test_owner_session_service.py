from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

import pytest
from webauthn.helpers.structs import CredentialDeviceType

from agentnet.approval.owner_session import OwnerSessionService
from agentnet.approval.store import ApprovalStore
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.security.envelope import LocalEnvelopeCipher


NOW = 1_800_000_000


class FakeApprovalRequests:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, object]]] = []

    def actionable_requests_for_owner(self, **kwargs):
        self.calls.append(("actionable", kwargs))
        return [
            {
                "request_id": "request-123456789",
                "approval_purpose": "identity.enrollment.approve",
                "state": "pending",
                "created_at": NOW,
                "expires_at": NOW + 300,
            }
        ]

    def request_options_for_owner(self, **kwargs):
        self.calls.append(("options", kwargs))
        transaction = {
            "candidate_key": {"algorithm": "ES256/P-256", "thumbprint": "k" * 64},
            "challenge_id": "challenge-123456789",
            "domain_id": "corp.example",
            "expires_at": NOW + 300,
            "harness": {
                "binding_assurance": "protected",
                "display_name": "Owner laptop",
                "kind": "pi",
                "requested_class": "protected_business",
            },
            "human": {
                "oidc_issuer": "https://idp.example",
                "oidc_subject": "owner-subject",
                "verified_email": "owner@example.test",
            },
            "issued_at": NOW,
            "nonce": "n" * 43,
            "purpose": "human_harness_credential_binding",
            "schema": "agentnet.enrollment.challenge.v1",
        }
        from agentnet.security.signatures import canonical_json

        return {
            "request_id": kwargs["request_id"],
            "approval_purpose": "identity.enrollment.approve",
            "transaction_digest": "a" * 64,
            "canonical_transaction_text": canonical_json(transaction).decode("utf-8"),
            "expires_at": NOW + 300,
            "challenge_expires_at": NOW + 180,
            "summary": {
                "title": "Enroll a laptop identity",
                "statements": [
                    "Laptop: Owner laptop (pi)",
                    "Verified account: owner@example.test",
                    "Corporate domain: corp.example",
                    "Authority granted: none",
                ],
                "advanced_digest": "a" * 64,
            },
            "publicKey": {"challenge": "AA", "allowCredentials": []},
        }

    def approve_request_for_owner(self, **kwargs):
        self.calls.append(("approve", kwargs))
        return {
            "schema": "agentnet.approval.claim-code.v1",
            "claim_code": "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111",
            "expires_at": NOW + 300,
        }

    def reject_request_for_owner(self, **kwargs):
        self.calls.append(("reject", kwargs))
        return {"status": "rejected"}

    def regenerate_claim_code(self, **kwargs):
        self.calls.append(("regenerate", kwargs))
        return {
            "claim_code": "1111-2222-3333-4444-5555-6666-7777-8888",
            "expires_at": NOW + 300,
        }


class BootstrapApprovalRequests(FakeApprovalRequests):
    def actionable_requests_for_owner(self, **kwargs):
        self.calls.append(("actionable", kwargs))
        return [
            {
                "request_id": "request-bootstrap-123",
                "approval_purpose": "authorization.bootstrap_plan.approve",
                "state": "pending",
                "created_at": NOW,
                "expires_at": NOW + 300,
            }
        ]

    def request_options_for_owner(self, **kwargs):
        self.calls.append(("options", kwargs))
        return {
            "request_id": kwargs["request_id"],
            "approval_purpose": "authorization.bootstrap_plan.approve",
            "expires_at": NOW + 300,
            "challenge_expires_at": NOW + 180,
            "summary": {
                "title": "Approve a bounded C0 laptop communication plan",
                "statements": ["Safety: communication remains unusable until the exact C0 guard ships"],
            },
            "publicKey": {"challenge": "AA", "allowCredentials": []},
        }


class FakeOIDCProvider:
    def __init__(self, identity: VerifiedOIDCIdentity) -> None:
        self.identity = identity
        self.config = SimpleNamespace(
            issuer="https://idp.example",
            client_id="approval-client",
            redirect_uri="https://approval.corp.example/v1/approval/owner/oidc/callback",
            audience="approval-audience",
            authorization_ttl_seconds=300,
        )
        self.exchanges: list[dict[str, str]] = []

    def authorization_url(self, *, state: str, nonce: str, code_challenge: str) -> str:
        return (
            "https://idp.example/authorize?"
            f"state={state}&nonce={nonce}&code_challenge={code_challenge}"
        )

    def exchange_and_verify(
        self,
        *,
        code: str,
        code_verifier: str,
        expected_nonce_hash: str,
    ) -> OIDCVerificationResult:
        self.exchanges.append(
            {
                "code": code,
                "code_verifier": code_verifier,
                "expected_nonce_hash": expected_nonce_hash,
            }
        )
        return OIDCVerificationResult(
            identity=self.identity,
            id_token_hash="a" * 64,
            expires_at=NOW + 300,
        )


def _service(
    tmp_path: Path,
    *,
    identity: VerifiedOIDCIdentity | None = None,
    exact_subject: bool = True,
    clock: list[int] | None = None,
    approval_service: FakeApprovalRequests | None = None,
):
    root = tmp_path / "approval"
    root.mkdir(mode=0o700, parents=True)
    root.chmod(0o700)
    database = root / "approval.sqlite3"
    database.touch(mode=0o600)
    cipher = LocalEnvelopeCipher(b"o" * 32)
    store = ApprovalStore(database, cipher, initialize=True)
    owner = SimpleNamespace(
        principal_id="security-owner",
        domain_id="corp.example",
        oidc_issuer="https://idp.example",
        oidc_subject="owner-subject" if exact_subject else None,
        verified_email_alias=None if exact_subject else "owner@example.test",
    )
    config = SimpleNamespace(
        public_origin="https://approval.corp.example",
        rp_id="approval.corp.example",
        rp_name="AgentNet Approval",
        verifier_id="approval.corp.example",
        challenge_ttl_seconds=180,
        registration_ttl_seconds=600,
        approver=lambda principal_id, domain_id=None: owner,
        approvers=(owner,),
    )
    provider = FakeOIDCProvider(
        identity
        or VerifiedOIDCIdentity(
            issuer="https://idp.example",
            subject="owner-subject",
            verified_email="owner@example.test",
        )
    )
    current = clock if clock is not None else [NOW]
    service = OwnerSessionService(
        config,
        store,
        cipher,
        provider,
        approval_service=approval_service,
        clock=lambda: current[0],
    )
    return SimpleNamespace(store=store, service=service, provider=provider)


def _login(stack):
    preauth = stack.service.create_preauth()
    started = stack.service.begin_oidc_login(
        preauth_cookie=preauth.session_token,
        csrf_cookie=preauth.csrf_token,
        csrf_token=preauth.csrf_token,
    )
    state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]
    completed = stack.service.complete_oidc_login(
        preauth_cookie=preauth.session_token,
        state=state,
        code="authorization-code",
    )
    return preauth, completed


def test_owner_session_accepts_server_validated_bootstrap_plan_summary(tmp_path: Path) -> None:
    approvals = BootstrapApprovalRequests()
    stack = _service(tmp_path, approval_service=approvals)
    try:
        _preauth, completed = _login(stack)
        result = stack.service.begin_approval(
            session_token=completed.session_token,
            csrf_token=completed.csrf_token,
            request_id="request-bootstrap-123",
        )
        assert result["summary"] == {
            "title": "Approve a bounded C0 laptop communication plan",
            "statements": ["Safety: communication remains unusable until the exact C0 guard ships"],
        }
        assert "canonical_transaction_text" not in result
    finally:
        stack.store.close()


def test_exact_owner_login_pins_identity_and_rotates_browser_session(tmp_path: Path) -> None:
    stack = _service(tmp_path)
    try:
        preauth, completed = _login(stack)
        assert completed.session_token != preauth.session_token
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.session_status(preauth.session_token)
        status = stack.service.session_status(completed.session_token)
        assert status.authenticated is True
        assert status.csrf_token == completed.csrf_token
        row = stack.store.fetch_one("SELECT * FROM approval_owner_bindings")
        assert row is not None
        assert row["oidc_issuer"] == "https://idp.example"
        assert row["oidc_subject"] == "owner-subject"
        assert row["verified_email"] == "owner@example.test"
        assert stack.provider.exchanges[0]["code"] == "authorization-code"
    finally:
        stack.store.close()


def test_bound_owner_session_lists_reviews_and_approves_without_browser_capability(
    tmp_path: Path,
) -> None:
    approvals = FakeApprovalRequests()
    stack = _service(tmp_path, approval_service=approvals)
    try:
        _preauth, completed = _login(stack)
        pending = stack.service.pending_approvals(session_token=completed.session_token)
        assert pending == [
            {
                "request_id": "request-123456789",
                "approval_purpose": "identity.enrollment.approve",
                "state": "pending",
                "created_at": NOW,
                "expires_at": NOW + 300,
            }
        ]
        options = stack.service.begin_approval(
            session_token=completed.session_token,
            csrf_token=completed.csrf_token,
            request_id="request-123456789",
        )
        assert options["summary"] == {
            "title": "Enroll a laptop identity",
            "statements": [
                "Laptop: Owner laptop (pi)",
                "Verified account: owner@example.test",
                "Corporate domain: corp.example",
                "Authority granted: none",
            ],
            "advanced_digest": "a" * 64,
        }
        assert "canonical_transaction_text" not in options
        approved = stack.service.complete_approval(
            session_token=completed.session_token,
            csrf_token=completed.csrf_token,
            request_id="request-123456789",
            credential={"id": "credential"},
        )
        assert approved["claim_code"] == "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111"
        regenerated = stack.service.regenerate_approval_code(
            session_token=completed.session_token,
            csrf_token=completed.csrf_token,
            request_id="request-123456789",
        )
        assert regenerated["claim_code"].startswith("1111-")
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.begin_approval(
                session_token=completed.session_token,
                csrf_token="X" * 43,
                request_id="request-123456789",
            )
        expected_session_hash = hashlib.sha256(
            completed.session_token.encode("ascii")
        ).hexdigest()
        for name, call in approvals.calls:
            assert call.get("principal_id", "security-owner") == "security-owner"
            assert call.get("domain_id", "corp.example") == "corp.example"
            assert "token" not in call
            if name in {"options", "approve", "regenerate"}:
                assert call["owner_session_hash"] == expected_session_hash
    finally:
        stack.store.close()


def test_stable_owner_denies_unsupported_purpose_without_webauthn_options(
    tmp_path: Path,
) -> None:
    class UnsupportedApproval(FakeApprovalRequests):
        def actionable_requests_for_owner(self, **kwargs):
            self.calls.append(("actionable", kwargs))
            return [
                {
                    "request_id": "request-123456789",
                    "approval_purpose": "authorization.elevation.approve",
                    "state": "pending",
                    "created_at": NOW,
                    "expires_at": NOW + 300,
                }
            ]

        def request_options_for_owner(self, **kwargs):
            raise AssertionError("unsupported purpose must not create WebAuthn options")
            return {
                "request_id": kwargs["request_id"],
                "approval_purpose": "authorization.elevation.approve",
                "transaction_digest": "a" * 64,
                "canonical_transaction_text": '{"schema":"agentnet.elevation.v1"}',
                "expires_at": NOW + 300,
                "challenge_expires_at": NOW + 180,
                "publicKey": {"challenge": "AA", "allowCredentials": []},
            }

    approvals = UnsupportedApproval()
    stack = _service(tmp_path, approval_service=approvals)
    try:
        _preauth, completed = _login(stack)
        with pytest.raises(AuthenticationError, match="approval request denied"):
            stack.service.begin_approval(
                session_token=completed.session_token,
                csrf_token=completed.csrf_token,
                request_id="request-123456789",
            )
        assert [name for name, _call in approvals.calls] == ["actionable"]
    finally:
        stack.store.close()


def test_wrong_subject_and_alias_subject_change_fail_without_new_session(tmp_path: Path) -> None:
    wrong = _service(
        tmp_path / "wrong",
        identity=VerifiedOIDCIdentity(
            issuer="https://idp.example",
            subject="attacker-subject",
            verified_email="owner@example.test",
        ),
    )
    try:
        with pytest.raises(AuthenticationError, match="owner identity denied"):
            _login(wrong)
        assert wrong.store.fetch_one("SELECT COUNT(*) AS n FROM approval_owner_bindings")["n"] == 0
        assert wrong.store.fetch_one("SELECT COUNT(*) AS n FROM approval_browser_sessions")["n"] == 0
    finally:
        wrong.store.close()

    alias = _service(tmp_path / "alias", exact_subject=False)
    try:
        _preauth, first = _login(alias)
        alias.provider.identity = VerifiedOIDCIdentity(
            issuer="https://idp.example",
            subject="different-subject",
            verified_email="owner@example.test",
        )
        preauth = alias.service.create_preauth()
        started = alias.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]
        with pytest.raises(AuthenticationError, match="owner identity denied"):
            alias.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
                code="second-code",
            )
        assert alias.service.session_status(first.session_token).authenticated is True
        assert alias.store.fetch_one("SELECT COUNT(*) AS n FROM approval_owner_bindings")["n"] == 1
        denied = alias.store.fetch_one(
            """SELECT state,failure_code FROM approval_oidc_login_transactions
                WHERE state_hash=?""",
            (hashlib.sha256(state.encode("ascii")).hexdigest(),),
        )
        assert denied is not None
        assert (denied["state"], denied["failure_code"]) == (
            "failed",
            "owner_binding_mismatch",
        )
    finally:
        alias.store.close()


def test_oidc_csrf_state_and_preauth_mixups_fail_without_consuming_valid_login(
    tmp_path: Path,
) -> None:
    stack = _service(tmp_path)
    try:
        preauth = stack.service.create_preauth()
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.begin_oidc_login(
                preauth_cookie=preauth.session_token,
                csrf_cookie=preauth.csrf_token,
                csrf_token="X" * 43,
            )
        started = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.complete_oidc_login(
                preauth_cookie="W" * 43,
                state=state,
                code="wrong-browser-code",
            )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state="Z" * 43,
                code="wrong-state-code",
            )
        completed = stack.service.complete_oidc_login(
            preauth_cookie=preauth.session_token,
            state=state,
            code="valid-code",
        )
        assert stack.service.session_status(completed.session_token).authenticated is True
        assert len(stack.provider.exchanges) == 1
    finally:
        stack.store.close()


def test_provider_error_fails_only_matching_pending_oidc_login_without_exchange(
    tmp_path: Path,
) -> None:
    stack = _service(tmp_path)
    try:
        preauth = stack.service.create_preauth()
        started = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]

        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.fail_oidc_login(
                preauth_cookie="W" * 43,
                state=state,
            )
        assert stack.store.fetch_one(
            "SELECT state FROM approval_oidc_login_transactions WHERE state_hash=?",
            (hashlib.sha256(state.encode("ascii")).hexdigest(),),
        )["state"] == "pending"

        stack.service.fail_oidc_login(
            preauth_cookie=preauth.session_token,
            state=state,
        )
        failed = stack.store.fetch_one(
            "SELECT state,failure_code,callback_claimed_at "
            "FROM approval_oidc_login_transactions WHERE state_hash=?",
            (hashlib.sha256(state.encode("ascii")).hexdigest(),),
        )
        assert (failed["state"], failed["failure_code"]) == (
            "failed",
            "provider_denied",
        )
        assert failed["callback_claimed_at"] is not None
        assert stack.provider.exchanges == []

        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.fail_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
            )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
                code="replayed-code",
            )
        assert stack.provider.exchanges == []
    finally:
        stack.store.close()


def test_owner_oidc_audience_drift_and_legacy_payload_fail_without_consumption(
    tmp_path: Path,
) -> None:
    stack = _service(tmp_path)
    try:
        preauth = stack.service.create_preauth()
        started = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]
        state_hash = hashlib.sha256(state.encode("ascii")).hexdigest()

        stack.provider.config.audience = "changed-audience"
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.fail_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
            )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
                code="drifted-code",
            )
        assert stack.store.fetch_one(
            "SELECT state FROM approval_oidc_login_transactions WHERE state_hash=?",
            (state_hash,),
        )["state"] == "pending"
        assert stack.provider.exchanges == []

        stack.provider.config.audience = "approval-audience"
        row = stack.store.fetch_one(
            "SELECT * FROM approval_oidc_login_transactions WHERE state_hash=?",
            (state_hash,),
        )
        legacy_payload = stack.service.cipher.encrypt_json(
            {"code_verifier": "legacy-verifier"},
            purpose=f"approval-owner-oidc:{row['login_id']}",
        )
        with stack.store.transaction() as connection:
            connection.execute(
                "UPDATE approval_oidc_login_transactions SET code_verifier_encrypted=? "
                "WHERE login_id=?",
                (legacy_payload, row["login_id"]),
            )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.fail_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
            )
        assert stack.store.fetch_one(
            "SELECT state FROM approval_oidc_login_transactions WHERE state_hash=?",
            (state_hash,),
        )["state"] == "pending"
        assert stack.provider.exchanges == []
    finally:
        stack.store.close()


def test_provider_error_and_success_race_has_one_terminal_winner(tmp_path: Path) -> None:
    stack = _service(tmp_path)
    try:
        preauth = stack.service.create_preauth()
        started = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]

        def succeed():
            return stack.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
                code="racing-code",
            )

        def deny():
            return stack.service.fail_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
            )

        with ThreadPoolExecutor(max_workers=2) as executor:
            futures = [executor.submit(succeed), executor.submit(deny)]
            outcomes: list[object] = []
            for future in futures:
                try:
                    outcomes.append(future.result())
                except AuthenticationError:
                    outcomes.append("denied")

        row = stack.store.fetch_one(
            "SELECT state,failure_code FROM approval_oidc_login_transactions"
        )
        assert row["state"] in {"callback_consumed", "failed"}
        assert sum(outcome != "denied" for outcome in outcomes) == 1
        if row["state"] == "callback_consumed":
            assert len(stack.provider.exchanges) == 1
            assert stack.store.fetch_one(
                "SELECT COUNT(*) AS n FROM approval_browser_sessions"
            )["n"] == 1
        else:
            assert row["failure_code"] == "provider_denied"
            assert stack.provider.exchanges == []
            assert stack.store.fetch_one(
                "SELECT COUNT(*) AS n FROM approval_browser_sessions"
            )["n"] == 0
    finally:
        stack.store.close()


def test_expired_owner_provider_error_fails_closed_without_exchange(tmp_path: Path) -> None:
    clock = [NOW]
    stack = _service(tmp_path, clock=clock)
    try:
        preauth = stack.service.create_preauth()
        started = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        state = parse_qs(urlsplit(started.authorization_url).query)["state"][0]
        clock[0] = started.expires_at
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.fail_oidc_login(
                preauth_cookie=preauth.session_token,
                state=state,
            )
        row = stack.store.fetch_one(
            "SELECT state,failure_code FROM approval_oidc_login_transactions"
        )
        assert (row["state"], row["failure_code"]) == ("expired", "expired")
        assert stack.provider.exchanges == []
    finally:
        stack.store.close()


def test_parallel_oidc_tabs_do_not_overwrite_state_and_callback_replay_fails(
    tmp_path: Path,
) -> None:
    stack = _service(tmp_path)
    try:
        preauth = stack.service.create_preauth()
        first = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        second = stack.service.begin_oidc_login(
            preauth_cookie=preauth.session_token,
            csrf_cookie=preauth.csrf_token,
            csrf_token=preauth.csrf_token,
        )
        first_state = parse_qs(urlsplit(first.authorization_url).query)["state"][0]
        second_state = parse_qs(urlsplit(second.authorization_url).query)["state"][0]
        assert first_state != second_state
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_oidc_login_transactions WHERE state='pending'"
        )["n"] == 2

        first_session = stack.service.complete_oidc_login(
            preauth_cookie=preauth.session_token,
            state=first_state,
            code="first-code",
        )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.complete_oidc_login(
                preauth_cookie=preauth.session_token,
                state=first_state,
                code="replayed-code",
            )
        second_session = stack.service.complete_oidc_login(
            preauth_cookie=preauth.session_token,
            state=second_state,
            code="second-code",
        )
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.session_status(first_session.session_token)
        assert stack.service.session_status(second_session.session_token).authenticated is True
        second_row = stack.store.fetch_one(
            "SELECT rotated_from_hash FROM approval_browser_sessions WHERE session_hash=?",
            (hashlib.sha256(second_session.session_token.encode("ascii")).hexdigest(),),
        )
        assert second_row is not None
        assert second_row["rotated_from_hash"] == hashlib.sha256(
            first_session.session_token.encode("ascii")
        ).hexdigest()
    finally:
        stack.store.close()


def test_browser_session_is_bound_to_exact_rp_origin_and_verifier(tmp_path: Path) -> None:
    stack = _service(tmp_path)
    try:
        _preauth, session = _login(stack)
        assert stack.service.session_status(session.session_token).authenticated is True
        original = stack.service.config.rp_id
        stack.service.config.rp_id = "different.example"
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.session_status(session.session_token)
        stack.service.config.rp_id = original
        assert stack.service.session_status(session.session_token).authenticated is True
    finally:
        stack.store.close()


def test_expired_owner_session_and_registration_ceremony_fail_closed(tmp_path: Path) -> None:
    clock = [NOW]
    stack = _service(tmp_path, clock=clock)
    try:
        _preauth, session = _login(stack)
        ceremony = stack.service.begin_registration(
            session_token=session.session_token,
            csrf_token=session.csrf_token,
        )
        clock[0] = ceremony.expires_at
        with pytest.raises(AuthenticationError, match="registration denied"):
            stack.service.complete_registration(
                session_token=session.session_token,
                csrf_token=session.csrf_token,
                ceremony_id=ceremony.ceremony_id,
                credential={"id": "stale"},
            )
        row = stack.store.fetch_one(
            "SELECT state FROM approval_registration_ceremonies WHERE ceremony_id=?",
            (ceremony.ceremony_id,),
        )
        assert row is not None and row["state"] == "expired"

        clock[0] = session.expires_at
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.session_status(session.session_token)
    finally:
        stack.store.close()


def test_csrf_multi_tab_and_cumulative_registration_budget(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    stack = _service(tmp_path)
    try:
        _preauth, session = _login(stack)
        with pytest.raises(AuthenticationError, match="owner session denied"):
            stack.service.begin_registration(
                session_token=session.session_token,
                csrf_token="wrong" * 8,
            )
        first = stack.service.begin_registration(
            session_token=session.session_token,
            csrf_token=session.csrf_token,
        )
        with pytest.raises(ConflictError, match="ceremony already active"):
            stack.service.begin_registration(
                session_token=session.session_token,
                csrf_token=session.csrf_token,
            )
        assert stack.store.fetch_one(
            "SELECT COUNT(*) AS n FROM approval_registration_ceremonies WHERE state='pending'"
        )["n"] == 1

        monkeypatch.setattr(
            "agentnet.approval.owner_session.verify_registration_response",
            lambda **_kwargs: (_ for _ in ()).throw(ValueError("invalid credential")),
        )
        with pytest.raises(AuthenticationError, match="registration denied"):
            stack.service.complete_registration(
                session_token=session.session_token,
                csrf_token=session.csrf_token,
                ceremony_id=first.ceremony_id,
                credential={"id": "invalid"},
            )
        second = stack.service.begin_registration(
            session_token=session.session_token,
            csrf_token=session.csrf_token,
        )
        monkeypatch.setattr(
            "agentnet.approval.owner_session.verify_registration_response",
            lambda **_kwargs: SimpleNamespace(
                credential_id=b"credential-1",
                credential_public_key=b"credential-public-key",
                sign_count=0,
                credential_device_type=CredentialDeviceType.SINGLE_DEVICE,
                credential_backed_up=False,
                user_verified=True,
            ),
        )
        result = stack.service.complete_registration(
            session_token=session.session_token,
            csrf_token=session.csrf_token,
            ceremony_id=second.ceremony_id,
            credential={"id": "valid"},
        )
        assert result == {"schema": "agentnet.approval.owner-registration-result.v1", "registered": True}
        budget = stack.store.fetch_one("SELECT * FROM approval_registration_budgets")
        assert budget is not None
        assert budget["failed_attempts_total"] == 1
        assert budget["challenge_rotations"] == 2
    finally:
        stack.store.close()
