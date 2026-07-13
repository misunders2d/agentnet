from __future__ import annotations

import base64
import hashlib
import io
import json
import urllib.error
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from urllib.parse import parse_qs, urlsplit

import pytest
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature

from agentnet.approval import (
    IndependentApprovalVerifier,
    LocalLabApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ReplayError
from agentnet.identity.domains import DomainRegistry
from agentnet.identity.enrollment import (
    ENROLLMENT_APPROVAL_PURPOSE,
    EnrollmentService,
    VerifiedOIDCIdentity,
)
from agentnet.identity.oidc import (
    OIDCEnrollmentCoordinator,
    OIDCHTTPResponse,
    OIDCProvider,
    OIDCProviderConfig,
    UrllibOIDCHTTPTransport,
)
from agentnet.operations.config import RuntimeProfile
from agentnet.security.signatures import P256KeyPair, canonical_json


ISSUER = "https://issuer.example"
AUTHORIZATION_ENDPOINT = f"{ISSUER}/authorize"
TOKEN_ENDPOINT = f"{ISSUER}/token"
JWKS_URI = f"{ISSUER}/jwks"


def b64(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


class MutableClock:
    def __init__(self, value: int = 2_000_000_000) -> None:
        self.value = value

    def __call__(self) -> int:
        return self.value


class FakeOIDCTransport:
    def __init__(self, clock: MutableClock) -> None:
        self.clock = clock
        self.signer = ec.generate_private_key(ec.SECP256R1())
        self.jwks_signer = self.signer
        self.kid = "issuer-key-1"
        self.discovery_issuer = ISSUER
        self.authorization_endpoint = AUTHORIZATION_ENDPOINT
        self.token_endpoint = TOKEN_ENDPOINT
        self.jwks_uri = JWKS_URI
        self.expected_code_challenge: str | None = None
        self.nonce: str | None = None
        self.claim_overrides: dict[str, object] = {}
        self.header_algorithm = "ES256"
        self.forced_id_token: str | None = None
        self.last_id_token: str | None = None
        self.token_posts = 0

    def bind_authorization_request(self, authorization_url: str) -> None:
        query = parse_qs(urlsplit(authorization_url).query)
        self.nonce = query["nonce"][0]
        self.expected_code_challenge = query["code_challenge"][0]
        assert query["code_challenge_method"] == ["S256"]
        assert query["response_type"] == ["code"]
        assert query["client_id"] == ["client-1"]

    def request(self, *, method, url, headers, body, timeout_seconds):
        assert timeout_seconds == 2
        if method == "GET" and url.endswith("/.well-known/openid-configuration"):
            return self._json(
                {
                    "issuer": self.discovery_issuer,
                    "authorization_endpoint": self.authorization_endpoint,
                    "token_endpoint": self.token_endpoint,
                    "jwks_uri": self.jwks_uri,
                    "response_types_supported": ["code"],
                    "code_challenge_methods_supported": ["S256"],
                    "id_token_signing_alg_values_supported": ["ES256"],
                }
            )
        if method == "GET" and url == self.jwks_uri:
            public = self.jwks_signer.public_key().public_numbers()
            return self._json(
                {
                    "keys": [
                        {
                            "alg": "ES256",
                            "crv": "P-256",
                            "kid": self.kid,
                            "kty": "EC",
                            "use": "sig",
                            "x": b64(public.x.to_bytes(32, "big")),
                            "y": b64(public.y.to_bytes(32, "big")),
                        }
                    ]
                }
            )
        if method == "POST" and url == self.token_endpoint:
            self.token_posts += 1
            fields = parse_qs(body.decode("ascii"), strict_parsing=True)
            verifier = fields["code_verifier"][0]
            actual_challenge = b64(hashlib.sha256(verifier.encode("ascii")).digest())
            assert actual_challenge == self.expected_code_challenge
            assert fields["redirect_uri"] == ["https://agent.example/oidc/callback"]
            assert fields["grant_type"] == ["authorization_code"]
            token = self.forced_id_token or self._id_token()
            self.last_id_token = token
            return self._json({"id_token": token, "token_type": "Bearer"})
        raise AssertionError(f"unexpected OIDC request: {method} {url}")

    def _id_token(self) -> str:
        claims: dict[str, object] = {
            "aud": "client-1",
            "email": "person@corp.example",
            "email_verified": True,
            "exp": self.clock() + 120,
            "iat": self.clock(),
            "iss": ISSUER,
            "nonce": self.nonce,
            "sub": "workforce-subject-1",
        }
        claims.update(self.claim_overrides)
        header = {"alg": self.header_algorithm, "kid": self.kid, "typ": "JWT"}
        encoded_header = b64(json.dumps(header, separators=(",", ":"), sort_keys=True).encode())
        encoded_claims = b64(json.dumps(claims, separators=(",", ":"), sort_keys=True).encode())
        signing_input = f"{encoded_header}.{encoded_claims}".encode("ascii")
        der = self.signer.sign(signing_input, ec.ECDSA(hashes.SHA256()))
        r, s = decode_dss_signature(der)
        signature = b64(r.to_bytes(32, "big") + s.to_bytes(32, "big"))
        return f"{encoded_header}.{encoded_claims}.{signature}"

    @staticmethod
    def _json(value: object) -> OIDCHTTPResponse:
        return OIDCHTTPResponse(200, {"content-type": "application/json"}, json.dumps(value).encode())


@dataclass
class OIDCStack:
    store: object
    clock: MutableClock
    transport: FakeOIDCTransport
    provider: OIDCProvider
    enrollment: EnrollmentService
    coordinator: OIDCEnrollmentCoordinator
    approver: TrustedApprover
    approver_key: P256KeyPair
    approval_verifier: IndependentApprovalVerifier

    def begin(self, key: P256KeyPair, *, name: str = "production workstation"):
        request = self.coordinator.begin_authorization(
            domain_id="corp.example",
            harness_kind="codex",
            harness_name=name,
            public_key_pem=key.public_pem,
        )
        self.transport.bind_authorization_request(request.authorization_url)
        return request

    def approval(self, challenge, *, transaction: bytes | None = None, purpose: str = ENROLLMENT_APPROVAL_PURPOSE, approver=None):
        return create_independent_approval_receipt(
            self.approver_key,
            approver=approver or self.approver,
            verifier_id=self.approval_verifier.verifier_id,
            approval_purpose=purpose,
            canonical_transaction=transaction or challenge.canonical_transaction,
            issued_at=self.clock(),
            expires_at=self.clock() + 60,
        )

    def complete_binding(self, key: P256KeyPair, challenge, *, approval=None):
        return self.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=approval or self.approval(challenge),
        )


@pytest.fixture
def oidc_stack(store) -> OIDCStack:
    clock = MutableClock()
    DomainRegistry(store).register("corp.example", now=clock())
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="independent-security-approver",
        domain_id="corp.example",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({ENROLLMENT_APPROVAL_PURPOSE}),
    )
    approval_verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver},
        verifier_id="webauthn-approval.corp.example",
    )
    enrollment = EnrollmentService(
        store,
        approval_verifier,
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        binding_assurance="os_bound",
        clock=clock,
    )
    transport = FakeOIDCTransport(clock)
    provider = OIDCProvider(
        OIDCProviderConfig(
            issuer=ISSUER,
            client_id="client-1",
            redirect_uri="https://agent.example/oidc/callback",
            allowed_signing_algorithms=("ES256",),
            http_timeout_seconds=2,
        ),
        transport=transport,
        clock=clock,
        resolver=lambda _host, _port: ("8.8.8.8",),
    )
    return OIDCStack(
        store,
        clock,
        transport,
        provider,
        enrollment,
        OIDCEnrollmentCoordinator(store, provider, enrollment),
        approver,
        approver_key,
        approval_verifier,
    )


def test_authorization_code_pkce_to_independently_approved_binding(oidc_stack: OIDCStack) -> None:
    key = P256KeyPair.generate()
    authorization = oidc_stack.begin(key)
    challenge = oidc_stack.coordinator.complete_authorization(state=authorization.state, code="code-value-0001")
    result = oidc_stack.complete_binding(key, challenge)

    transaction = oidc_stack.store.fetch_one(
        "SELECT * FROM oidc_enrollment_transactions WHERE transaction_id=?", (authorization.transaction_id,)
    )
    assert transaction["status"] == "consumed"
    assert transaction["enrollment_challenge_id"] == challenge.challenge_id
    assert result.harness_status == "active"
    assert dict(
        oidc_stack.store.fetch_one(
            "SELECT oidc_issuer,oidc_subject,verified_email FROM principals WHERE principal_id=?",
            (result.principal_id,),
        )
    ) == {
        "oidc_issuer": ISSUER,
        "oidc_subject": "workforce-subject-1",
        "verified_email": "person@corp.example",
    }
    assert oidc_stack.store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 3


def test_state_code_and_token_are_single_use(oidc_stack: OIDCStack) -> None:
    key = P256KeyPair.generate()
    first = oidc_stack.begin(key)
    original_nonce = oidc_stack.transport.nonce
    challenge = oidc_stack.coordinator.complete_authorization(state=first.state, code="code-value-replay")
    first_token = oidc_stack.transport.last_id_token
    with pytest.raises(ReplayError):
        oidc_stack.coordinator.complete_authorization(state=first.state, code="code-value-replay")

    second = oidc_stack.begin(P256KeyPair.generate())
    with pytest.raises(ReplayError, match="code"):
        oidc_stack.coordinator.complete_authorization(state=second.state, code="code-value-replay")

    third = oidc_stack.begin(P256KeyPair.generate())
    oidc_stack.transport.forced_id_token = first_token
    with oidc_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE oidc_enrollment_transactions SET nonce_hash=? WHERE transaction_id=?",
            (hashlib.sha256(original_nonce.encode()).hexdigest(), third.transaction_id),
        )
    with pytest.raises(ReplayError, match="token"):
        oidc_stack.coordinator.complete_authorization(state=third.state, code="code-value-new-token-replay")
    assert challenge.challenge_id


def test_wrong_state_does_not_consume_the_real_transaction(oidc_stack: OIDCStack) -> None:
    authorization = oidc_stack.begin(P256KeyPair.generate())
    with pytest.raises(AuthenticationError, match="state"):
        oidc_stack.coordinator.complete_authorization(state="x" * 43, code="code-value-0002")
    row = oidc_stack.store.fetch_one(
        "SELECT status FROM oidc_enrollment_transactions WHERE transaction_id=?", (authorization.transaction_id,)
    )
    assert row["status"] == "pending"
    assert oidc_stack.coordinator.complete_authorization(
        state=authorization.state, code="code-value-0002"
    ).challenge_id


@pytest.mark.parametrize("attack", ["fake_issuer", "wrong_algorithm", "wrong_key", "nonce", "stale", "email_unverified"])
def test_discovery_jwks_and_claim_attacks_fail_closed(oidc_stack: OIDCStack, attack: str) -> None:
    if attack == "fake_issuer":
        oidc_stack.transport.discovery_issuer = "https://attacker.example"
        with pytest.raises(AuthenticationError, match="issuer"):
            oidc_stack.begin(P256KeyPair.generate())
        return
    authorization = oidc_stack.begin(P256KeyPair.generate())
    if attack == "wrong_algorithm":
        oidc_stack.transport.header_algorithm = "RS256"
    elif attack == "wrong_key":
        oidc_stack.transport.jwks_signer = ec.generate_private_key(ec.SECP256R1())
    elif attack == "nonce":
        oidc_stack.transport.claim_overrides["nonce"] = "attacker-nonce-value-that-is-long-enough"
    elif attack == "stale":
        oidc_stack.transport.claim_overrides["iat"] = oidc_stack.clock() - 301
    elif attack == "email_unverified":
        oidc_stack.transport.claim_overrides["email_verified"] = False
    with pytest.raises(AuthenticationError):
        oidc_stack.coordinator.complete_authorization(state=authorization.state, code=f"code-{attack}-000")
    assert oidc_stack.store.fetch_one(
        "SELECT status FROM oidc_enrollment_transactions WHERE transaction_id=?", (authorization.transaction_id,)
    )["status"] == "failed"


def test_oidc_discovery_rejects_unpinned_origins_and_nonpublic_resolution(
    oidc_stack: OIDCStack,
) -> None:
    oidc_stack.transport.token_endpoint = "https://attacker.example/token"
    with pytest.raises(AuthenticationError, match="origin is not pinned"):
        oidc_stack.begin(P256KeyPair.generate())

    provider = OIDCProvider(
        oidc_stack.provider.config,
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: ("127.0.0.1",),
    )
    with pytest.raises(GateBlocked, match="non-public"):
        provider.discover()


def test_production_oidc_transport_never_follows_redirects() -> None:
    class RedirectingOpener:
        def __init__(self) -> None:
            self.calls = 0

        def open(self, request, *, timeout):
            self.calls += 1
            raise urllib.error.HTTPError(
                request.full_url,
                302,
                "redirect",
                {"location": "https://attacker.example/token"},
                io.BytesIO(b""),
            )

    opener = RedirectingOpener()
    transport = UrllibOIDCHTTPTransport(opener=opener)
    with pytest.raises(GateBlocked, match="redirects are forbidden"):
        transport.request(
            method="GET",
            url="https://issuer.example/.well-known/openid-configuration",
            headers={"accept": "application/json"},
            body=None,
            timeout_seconds=2,
        )
    assert opener.calls == 1


def test_subject_binding_allows_safe_alias_change_but_rejects_email_collision(oidc_stack: OIDCStack) -> None:
    first_key = P256KeyPair.generate()
    first_auth = oidc_stack.begin(first_key)
    first_challenge = oidc_stack.coordinator.complete_authorization(
        state=first_auth.state, code="code-alias-first"
    )
    first = oidc_stack.complete_binding(first_key, first_challenge)

    oidc_stack.transport.claim_overrides = {"email": "new.alias@corp.example"}
    alias_key = P256KeyPair.generate()
    alias_auth = oidc_stack.begin(alias_key)
    alias_challenge = oidc_stack.coordinator.complete_authorization(
        state=alias_auth.state, code="code-alias-second"
    )
    alias = oidc_stack.complete_binding(alias_key, alias_challenge)
    assert alias.principal_id == first.principal_id
    assert oidc_stack.store.fetch_one(
        "SELECT verified_email FROM principals WHERE principal_id=?", (first.principal_id,)
    )["verified_email"] == "new.alias@corp.example"
    assert oidc_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM principal_aliases WHERE principal_id=?", (first.principal_id,)
    )["count"] == 2

    oidc_stack.transport.claim_overrides = {
        "email": "person@corp.example",
        "sub": "attacker-subject-2",
    }
    collision_key = P256KeyPair.generate()
    collision_auth = oidc_stack.begin(collision_key)
    collision_challenge = oidc_stack.coordinator.complete_authorization(
        state=collision_auth.state, code="code-email-collision"
    )
    with pytest.raises(ConflictError, match="different OIDC subject"):
        oidc_stack.complete_binding(collision_key, collision_challenge)
    assert oidc_stack.store.fetch_one("SELECT COUNT(*) AS count FROM principals")["count"] == 1


def test_exact_approval_domain_purpose_transaction_key_and_harness_bindings(oidc_stack: OIDCStack) -> None:
    key = P256KeyPair.generate()
    authorization = oidc_stack.begin(key)
    challenge = oidc_stack.coordinator.complete_authorization(state=authorization.state, code="code-bindings-001")

    wrong_domain_approver = TrustedApprover(
        principal_id=oidc_stack.approver.principal_id,
        domain_id="other.example",
        signer_key_id=oidc_stack.approver.signer_key_id,
        public_key_pem=oidc_stack.approver.public_key_pem,
        allowed_purposes=frozenset({ENROLLMENT_APPROVAL_PURPOSE}),
    )
    invalid_approvals = (
        oidc_stack.approval(challenge, transaction=canonical_json({"wrong": "transaction"})),
        oidc_stack.approval(challenge, purpose="other.approval.purpose"),
        oidc_stack.approval(challenge, approver=wrong_domain_approver),
        create_independent_approval_receipt(
            P256KeyPair.generate(),
            approver=oidc_stack.approver,
            verifier_id=oidc_stack.approval_verifier.verifier_id,
            approval_purpose=ENROLLMENT_APPROVAL_PURPOSE,
            canonical_transaction=challenge.canonical_transaction,
            issued_at=oidc_stack.clock(),
            expires_at=oidc_stack.clock() + 60,
        ),
    )
    for approval in invalid_approvals:
        with pytest.raises(AuthenticationError):
            oidc_stack.complete_binding(key, challenge, approval=approval)

    with pytest.raises(AuthenticationError):
        oidc_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=P256KeyPair.generate().sign("agentnet.enrollment.pop.v1", challenge.signed_fields()),
            approval=oidc_stack.approval(challenge),
        )
    tampered = challenge.signed_fields()
    tampered["harness"]["display_name"] = "substituted harness"
    tampered_bytes = canonical_json(tampered)
    with pytest.raises(AuthenticationError):
        oidc_stack.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=tampered_bytes,
            possession_signature=key.sign("agentnet.enrollment.pop.v1", tampered),
            approval=oidc_stack.approval(challenge, transaction=tampered_bytes),
        )


def test_independent_receipt_and_enrollment_challenge_consume_atomically_under_race(oidc_stack: OIDCStack) -> None:
    key = P256KeyPair.generate()
    authorization = oidc_stack.begin(key)
    challenge = oidc_stack.coordinator.complete_authorization(state=authorization.state, code="code-race-0001")
    approval = oidc_stack.approval(challenge)

    def consume():
        return oidc_stack.complete_binding(key, challenge, approval=approval)

    with ThreadPoolExecutor(max_workers=2) as pool:
        outcomes = [future.exception() or future.result() for future in (pool.submit(consume), pool.submit(consume))]
    assert sum(not isinstance(outcome, Exception) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, ReplayError) for outcome in outcomes) == 1
    assert oidc_stack.store.fetch_one("SELECT COUNT(*) AS count FROM harnesses")["count"] == 1
    assert oidc_stack.store.fetch_one("SELECT COUNT(*) AS count FROM credentials")["count"] == 1


def test_oidc_callback_state_claim_is_atomic_under_race(oidc_stack: OIDCStack) -> None:
    authorization = oidc_stack.begin(P256KeyPair.generate())

    def consume():
        return oidc_stack.coordinator.complete_authorization(
            state=authorization.state,
            code="code-callback-race",
        )

    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = (pool.submit(consume), pool.submit(consume))
        outcomes = [future.exception() or future.result() for future in futures]
    assert sum(not isinstance(outcome, Exception) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, ReplayError) for outcome in outcomes) == 1
    assert oidc_stack.transport.token_posts == 1
    assert oidc_stack.store.fetch_one("SELECT COUNT(*) AS count FROM enrollment_challenges")["count"] == 1


def test_production_refuses_lab_or_caller_injected_identity(oidc_stack: OIDCStack) -> None:
    with pytest.raises(GateBlocked, match="authorization-code verifier"):
        oidc_stack.enrollment.begin(
            domain_id="corp.example",
            identity=VerifiedOIDCIdentity(
                issuer=ISSUER,
                subject="caller-asserted-subject",
                verified_email="person@corp.example",
            ),
            harness_kind="codex",
            harness_name="injected",
            public_key_pem=P256KeyPair.generate().public_pem,
        )
    with pytest.raises(GateBlocked, match="local lab approval verifier"):
        EnrollmentService(
            oidc_stack.store,
            LocalLabApprovalVerifier(P256KeyPair.generate(), clock=oidc_stack.clock),
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            binding_assurance="os_bound",
        )
