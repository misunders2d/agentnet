from __future__ import annotations

import base64
import hashlib
import io
import json
import ssl
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
from agentnet.identity import oidc as oidc_module
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
        self.resolved_requests: list[tuple[str, tuple[str, ...]]] = []

    def bind_authorization_request(self, authorization_url: str) -> None:
        query = parse_qs(urlsplit(authorization_url).query)
        self.nonce = query["nonce"][0]
        self.expected_code_challenge = query["code_challenge"][0]
        assert query["code_challenge_method"] == ["S256"]
        assert query["response_type"] == ["code"]
        assert query["client_id"] == ["client-1"]

    def request(self, *, method, url, resolved_addresses, headers, body, timeout_seconds):
        assert timeout_seconds == 2
        self.resolved_requests.append((url, resolved_addresses))
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


def test_private_oidc_requires_explicit_origin_network_and_jwk_pins() -> None:
    common = {
        "issuer": ISSUER,
        "client_id": "client-1",
        "redirect_uri": "https://agent.example/oidc/callback",
        "allowed_signing_algorithms": ("ES256",),
    }
    with pytest.raises(ValueError, match="explicit endpoint origins"):
        OIDCProviderConfig(
            **common,
            allowed_private_endpoint_cidrs=("10.20.0.0/24",),
            pinned_jwk_thumbprints=(("issuer-key-1", "a" * 64),),
        )
    with pytest.raises(ValueError, match="JWK thumbprint"):
        OIDCProviderConfig(
            **common,
            allowed_endpoint_origins=(ISSUER,),
            allowed_private_endpoint_cidrs=("10.20.0.0/24",),
        )
    with pytest.raises(ValueError, match="canonical private networks"):
        OIDCProviderConfig(
            **common,
            allowed_endpoint_origins=(ISSUER,),
            allowed_private_endpoint_cidrs=("10.20.0.1/24",),
            pinned_jwk_thumbprints=(("issuer-key-1", "a" * 64),),
        )


def test_private_oidc_resolves_once_per_request_and_passes_only_validated_snapshot(
    oidc_stack: OIDCStack,
) -> None:
    config = OIDCProviderConfig(
        issuer=ISSUER,
        client_id="client-1",
        redirect_uri="https://agent.example/oidc/callback",
        allowed_signing_algorithms=("ES256",),
        allowed_endpoint_origins=(ISSUER,),
        allowed_private_endpoint_cidrs=("10.20.0.0/24",),
        pinned_jwk_thumbprints=(("issuer-key-1", "a" * 64),),
        http_timeout_seconds=2,
    )
    provider = OIDCProvider(
        config,
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: ("10.20.0.8",),
    )

    provider.discover()

    assert oidc_stack.transport.resolved_requests == [
        (provider.discovery_url, ("10.20.0.8",))
    ]

    outside = OIDCProvider(
        config,
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: ("10.21.0.8",),
    )
    with pytest.raises(GateBlocked, match="not explicitly pinned"):
        outside.discover()


def test_exact_oidc_address_pins_reject_dns_address_substitution(oidc_stack: OIDCStack) -> None:
    config = OIDCProviderConfig(
        issuer=ISSUER,
        client_id="client-1",
        redirect_uri="https://agent.example/oidc/callback",
        allowed_signing_algorithms=("ES256",),
        pinned_endpoint_addresses=("8.8.8.8",),
        http_timeout_seconds=2,
    )
    provider = OIDCProvider(
        config,
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: ("8.8.4.4",),
    )
    with pytest.raises(GateBlocked, match="not exactly pinned"):
        provider.discover()


def test_production_oidc_transport_connects_only_to_validated_address_and_rejects_redirects(
    monkeypatch,
) -> None:
    calls: list[tuple[str, int, str, float]] = []
    monkeypatch.setenv("HTTPS_PROXY", "http://127.0.0.1:8888")

    class RedirectResponse:
        status = 302

        @staticmethod
        def getheaders():
            return [("location", "https://attacker.example/token")]

        @staticmethod
        def read(_limit):
            raise AssertionError("redirect bodies must not be consumed")

    class RecordingConnection:
        def __init__(self, host, port, address, *, timeout, context) -> None:
            del context
            calls.append((host, port, address, timeout))
            self.requested: tuple[str, str] | None = None
            self.closed = False

        def request(self, method, target, *, body, headers) -> None:
            del body, headers
            self.requested = (method, target)

        @staticmethod
        def getresponse():
            return RedirectResponse()

        def close(self) -> None:
            self.closed = True

    transport = UrllibOIDCHTTPTransport(connection_factory=RecordingConnection)
    with pytest.raises(GateBlocked, match="redirects are forbidden"):
        transport.request(
            method="GET",
            url="https://issuer.example/.well-known/openid-configuration",
            resolved_addresses=("8.8.8.8",),
            headers={"accept": "application/json"},
            body=None,
            timeout_seconds=2,
        )
    assert calls == [("issuer.example", 443, "8.8.8.8", 2)]


def test_pinned_connection_binds_to_snapshot_address_not_re_resolved_hostname(
    monkeypatch,
) -> None:
    resolver_calls: list[tuple[str, int]] = []
    socket_calls: list[tuple[tuple[str, int], float, object]] = []
    tls_hosts: list[str] = []
    request_bytes: list[bytes] = []
    monkeypatch.setenv("HTTPS_PROXY", "http://127.0.0.1:8888")

    class RebindingResolver:
        def __call__(self, host: str, port: int) -> tuple[str, ...]:
            resolver_calls.append((host, port))
            if len(resolver_calls) == 1:
                return ("8.8.8.8",)
            return ("127.0.0.1",)

    discovery_body = json.dumps(
        {
            "issuer": ISSUER,
            "authorization_endpoint": AUTHORIZATION_ENDPOINT,
            "token_endpoint": TOKEN_ENDPOINT,
            "jwks_uri": JWKS_URI,
            "response_types_supported": ["code"],
            "code_challenge_methods_supported": ["S256"],
            "id_token_signing_alg_values_supported": ["ES256"],
        }
    ).encode()
    wire_response = (
        b"HTTP/1.1 200 OK\r\n"
        b"Content-Type: application/json\r\n"
        + f"Content-Length: {len(discovery_body)}\r\n".encode("ascii")
        + b"Connection: close\r\n\r\n"
        + discovery_body
    )

    class FakeSocket:
        def __init__(self) -> None:
            self._response = io.BytesIO(wire_response)
            self.closed = False

        def sendall(self, value: bytes) -> None:
            request_bytes.append(value)

        def makefile(self, _mode: str, _buffering: int | None = None):
            return self._response

        def close(self) -> None:
            self.closed = True

    def create_connection(address, timeout, source_address):
        socket_calls.append((address, timeout, source_address))
        return FakeSocket()

    class VerifiedFakeTLSContext:
        check_hostname = True
        verify_mode = ssl.CERT_REQUIRED
        minimum_version = ssl.TLSVersion.TLSv1_2

        @staticmethod
        def wrap_socket(raw_socket, *, server_hostname):
            tls_hosts.append(server_hostname)
            return raw_socket

    monkeypatch.setattr(oidc_module.socket, "create_connection", create_connection)
    provider = OIDCProvider(
        OIDCProviderConfig(
            issuer=ISSUER,
            client_id="client-1",
            redirect_uri="https://agent.example/oidc/callback",
            allowed_signing_algorithms=("ES256",),
            http_timeout_seconds=2,
        ),
        transport=UrllibOIDCHTTPTransport(ssl_context=VerifiedFakeTLSContext()),
        resolver=RebindingResolver(),
    )

    discovery = provider.discover()

    assert discovery.authorization_endpoint == AUTHORIZATION_ENDPOINT
    assert resolver_calls == [("issuer.example", 443)]
    assert socket_calls == [(('8.8.8.8', 443), 2, None)]
    assert tls_hosts == ["issuer.example"]
    assert b"Host: issuer.example\r\n" in b"".join(request_bytes)

    # A second resolution would now return loopback. The transport never makes
    # that second lookup; it connected to the first validated snapshot above.
    assert provider.resolver("issuer.example", 443) == ("127.0.0.1",)


def test_pinned_connection_rejects_proxy_tunnel_before_socket_connect(monkeypatch) -> None:
    socket_called = False

    def create_connection(*_args, **_kwargs):
        nonlocal socket_called
        socket_called = True
        raise AssertionError("proxy tunnel rejection must precede socket creation")

    class VerifiedFakeTLSContext:
        check_hostname = True
        verify_mode = ssl.CERT_REQUIRED
        minimum_version = ssl.TLSVersion.TLSv1_2

    monkeypatch.setattr(oidc_module.socket, "create_connection", create_connection)
    connection = oidc_module._PinnedHTTPSConnection(
        "issuer.example",
        443,
        "8.8.8.8",
        timeout=2,
        context=VerifiedFakeTLSContext(),
    )
    connection.set_tunnel("proxy.example", port=443)

    with pytest.raises(GateBlocked, match="proxy tunnels are forbidden"):
        connection.connect()
    assert socket_called is False


def test_oidc_provider_converts_invalid_resolver_types_to_gate_block(oidc_stack: OIDCStack) -> None:
    provider = OIDCProvider(
        oidc_stack.provider.config,
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: (None,),  # type: ignore[return-value]
    )

    with pytest.raises(GateBlocked, match="resolved to an invalid address"):
        provider.discover()


def test_production_oidc_transport_rejects_oversized_response() -> None:
    class OversizedResponse:
        status = 200

        @staticmethod
        def getheaders():
            return []

        @staticmethod
        def read(limit):
            return b"x" * limit

    class OversizedConnection:
        def __init__(self, *_args, **_kwargs) -> None:
            self.closed = False

        @staticmethod
        def request(*_args, **_kwargs) -> None:
            return None

        @staticmethod
        def getresponse():
            return OversizedResponse()

        def close(self) -> None:
            self.closed = True

    transport = UrllibOIDCHTTPTransport(
        maximum_response_bytes=1_024,
        connection_factory=OversizedConnection,
    )
    with pytest.raises(GateBlocked, match="response exceeds"):
        transport.request(
            method="GET",
            url=f"{ISSUER}/oversized",
            resolved_addresses=("8.8.8.8",),
            headers={"accept": "application/json"},
            body=None,
            timeout_seconds=2,
        )


@pytest.mark.parametrize(
    "forbidden_address",
    (
        "127.0.0.1",
        "169.254.169.254",
        "::1",
        "::ffff:127.0.0.1",
        "2001:db8::1",
    ),
)
def test_oidc_endpoint_pins_reject_unsafe_address_classes(forbidden_address: str) -> None:
    with pytest.raises(ValueError, match="safe unicast"):
        OIDCProviderConfig(
            issuer=ISSUER,
            client_id="client-1",
            redirect_uri="https://agent.example/oidc/callback",
            allowed_signing_algorithms=("ES256",),
            allowed_endpoint_origins=(ISSUER,),
            pinned_endpoint_addresses=(forbidden_address,),
            pinned_jwk_thumbprints=(("issuer-key-1", "a" * 64),),
        )


def test_private_ipv6_oidc_address_requires_and_uses_explicit_pins(
    oidc_stack: OIDCStack,
) -> None:
    provider = OIDCProvider(
        OIDCProviderConfig(
            issuer=ISSUER,
            client_id="client-1",
            redirect_uri="https://agent.example/oidc/callback",
            allowed_signing_algorithms=("ES256",),
            allowed_endpoint_origins=(ISSUER,),
            allowed_private_endpoint_cidrs=("fd00::/8",),
            pinned_endpoint_addresses=("fd00::8",),
            pinned_jwk_thumbprints=(("issuer-key-1", "a" * 64),),
            http_timeout_seconds=2,
        ),
        transport=oidc_stack.transport,
        clock=oidc_stack.clock,
        resolver=lambda _host, _port: ("fd00::8",),
    )

    provider.discover()

    assert oidc_stack.transport.resolved_requests == [
        (provider.discovery_url, ("fd00::8",))
    ]


def test_production_oidc_transport_rejects_insecure_tls_context() -> None:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    with pytest.raises(ValueError, match="certificate and hostname verification"):
        UrllibOIDCHTTPTransport(ssl_context=context)


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
