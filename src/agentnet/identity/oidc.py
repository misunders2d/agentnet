"""Production OIDC authorization-code/PKCE verification for enrollment.

The adapter owns discovery, token exchange, JWT/JWKS verification, and durable
single-use state.  Callers never submit identity claims to the enrollment API.
"""

from __future__ import annotations

import base64
import hashlib
import http.client
import ipaddress
import json
import re
import secrets
import socket
import sqlite3
import ssl
import time
import urllib.parse
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from typing import Any, Protocol
from urllib.parse import urlsplit
from uuid import NAMESPACE_URL, uuid4, uuid5

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec, padding, rsa
from cryptography.hazmat.primitives.asymmetric.utils import encode_dss_signature
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

from agentnet.errors import (
    AuthenticationError,
    ExtensionError,
    GateBlocked,
    ReplayError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.endpoint_policy import (
    canonical_endpoint_address as _canonical_endpoint_address,
    canonical_private_endpoint_network as _canonical_private_endpoint_network,
)
from agentnet.identity.enrollment import (
    EnrollmentChallenge,
    EnrollmentResult,
    EnrollmentService,
    VerifiedOIDCIdentity,
)
from agentnet.operations.config import OIDCTokenEndpointAuthMethod, RuntimeProfile
from agentnet.security.signatures import canonical_json, verify_signature


_B64URL = re.compile(r"^[A-Za-z0-9_-]+$")
_OIDC_ALGORITHMS = frozenset({"RS256", "ES256"})


class RemoteActivationIdentityMismatch(AuthenticationError):
    """Verified OIDC identity does not match the server-staged owner policy."""

    code = "activation_wrong_account"
    http_status = 403


@dataclass(frozen=True, slots=True)
class OIDCHTTPResponse:
    status: int
    headers: Mapping[str, str]
    body: bytes


class OIDCHTTPTransport(Protocol):
    def request(
        self,
        *,
        method: str,
        url: str,
        resolved_addresses: tuple[str, ...],
        headers: Mapping[str, str],
        body: bytes | None,
        timeout_seconds: float,
    ) -> OIDCHTTPResponse: ...


class _PinnedHTTPSConnection(http.client.HTTPSConnection):
    """HTTPS connection whose TCP peer is fixed while TLS verifies the URL host."""

    def __init__(
        self,
        server_hostname: str,
        port: int,
        connect_address: str,
        *,
        timeout: float,
        context: ssl.SSLContext,
    ) -> None:
        super().__init__(server_hostname, port=port, timeout=timeout, context=context)
        self._connect_address = connect_address

    def connect(self) -> None:
        if self._tunnel_host is not None:
            raise GateBlocked("oidc_provider", "OIDC provider proxy tunnels are forbidden")
        raw_socket = self._create_connection(
            (self._connect_address, self.port),
            self.timeout,
            self.source_address,
        )
        try:
            self.sock = self._context.wrap_socket(raw_socket, server_hostname=self.host)
        except BaseException:
            raw_socket.close()
            raise


class PinnedOIDCHTTPTransport:
    """Direct TLS transport bound to a validated DNS/address snapshot.

    The transport never consults environment proxies and never resolves the URL
    hostname.  The provider resolves and validates an exact address tuple once;
    this class connects only to those addresses while certificate verification,
    SNI, and the HTTP Host header retain the configured URL hostname.
    """

    def __init__(
        self,
        *,
        maximum_response_bytes: int = 1_048_576,
        ssl_context: ssl.SSLContext | None = None,
        connection_factory: Callable[..., http.client.HTTPSConnection] | None = None,
    ) -> None:
        if maximum_response_bytes < 1_024 or maximum_response_bytes > 8_388_608:
            raise ValueError("OIDC response limit is outside the supported range")
        context = ssl_context or ssl.create_default_context()
        if not context.check_hostname or context.verify_mode != ssl.CERT_REQUIRED:
            raise ValueError("OIDC TLS context must require certificate and hostname verification")
        if ssl_context is not None and context.minimum_version < ssl.TLSVersion.TLSv1_2:
            raise ValueError("OIDC TLS context must require TLS 1.2 or newer")
        if ssl_context is None:
            context.minimum_version = ssl.TLSVersion.TLSv1_2
            context.set_alpn_protocols(["http/1.1"])
        self.maximum_response_bytes = maximum_response_bytes
        self._ssl_context = context
        self._connection_factory = connection_factory or _PinnedHTTPSConnection

    def request(
        self,
        *,
        method: str,
        url: str,
        resolved_addresses: tuple[str, ...],
        headers: Mapping[str, str],
        body: bytes | None,
        timeout_seconds: float,
    ) -> OIDCHTTPResponse:
        _require_discovered_https_url(url, label="provider endpoint")
        if method not in {"GET", "POST"}:
            raise GateBlocked("oidc_provider", "OIDC provider HTTP method is unsupported")
        if any(key.casefold() == "host" for key in headers):
            raise GateBlocked("oidc_provider", "OIDC provider Host header is transport-owned")
        parsed = urlsplit(url)
        if parsed.hostname is None:
            raise GateBlocked("oidc_provider", "OIDC provider URL has no hostname")
        try:
            addresses = tuple(str(ipaddress.ip_address(value)) for value in resolved_addresses)
        except ValueError as exc:
            raise GateBlocked("oidc_provider", "OIDC provider validated address is invalid") from exc
        if not addresses or len(addresses) > 32 or len(set(addresses)) != len(addresses):
            raise GateBlocked(
                "oidc_provider",
                "OIDC provider validated addresses are empty, excessive, or ambiguous",
            )
        target = parsed.path or "/"
        last_error: OSError | None = None
        for address in addresses:
            connection = self._connection_factory(
                parsed.hostname,
                parsed.port or 443,
                address,
                timeout=timeout_seconds,
                context=self._ssl_context,
            )
            try:
                connection.request(method, target, body=body, headers=dict(headers))
                response = connection.getresponse()
                if 300 <= int(response.status) < 400:
                    raise GateBlocked("oidc_provider", "OIDC provider redirects are forbidden")
                payload = response.read(self.maximum_response_bytes + 1)
                if len(payload) > self.maximum_response_bytes:
                    raise GateBlocked("oidc_provider", "OIDC provider response exceeds the configured bound")
                return OIDCHTTPResponse(
                    status=int(response.status),
                    headers={key.casefold(): value for key, value in response.getheaders()},
                    body=payload,
                )
            except ssl.SSLCertVerificationError as exc:
                raise GateBlocked("oidc_provider", "OIDC provider TLS identity verification failed") from exc
            except ssl.SSLError as exc:
                raise GateBlocked("oidc_provider", "OIDC provider TLS negotiation failed") from exc
            except http.client.HTTPException as exc:
                raise GateBlocked("oidc_provider", "OIDC provider returned an invalid HTTP response") from exc
            except OSError as exc:
                last_error = exc
            finally:
                connection.close()
        raise GateBlocked("oidc_provider", "OIDC provider is unavailable") from last_error


class UrllibOIDCHTTPTransport(PinnedOIDCHTTPTransport):
    """Compatibility name for the direct pinned transport; urllib is no longer used."""


@dataclass(frozen=True, slots=True)
class OIDCProviderConfig:
    issuer: str
    client_id: str
    redirect_uri: str
    audience: str | None = None
    token_endpoint_auth_method: OIDCTokenEndpointAuthMethod = OIDCTokenEndpointAuthMethod.NONE
    # Runtime-only value. Never serialize this dataclass with asdict()/astuple().
    client_secret: str | None = field(default=None, repr=False)
    allowed_signing_algorithms: tuple[str, ...] = ("RS256",)
    pinned_jwk_thumbprints: tuple[tuple[str, str], ...] = ()
    allowed_endpoint_origins: tuple[str, ...] = ()
    allowed_private_endpoint_cidrs: tuple[str, ...] = ()
    pinned_endpoint_addresses: tuple[str, ...] = ()
    remote_activation_oidc_subject: str | None = None
    remote_activation_verified_email_alias: str | None = None
    authorization_ttl_seconds: int = 300
    maximum_id_token_age_seconds: int = 300
    allowed_clock_skew_seconds: int = 30
    http_timeout_seconds: float = 5.0

    def __post_init__(self) -> None:
        _require_https_url(self.issuer, label="issuer", allow_query=False)
        _require_https_url(self.redirect_uri, label="redirect URI", allow_query=True)
        if self.issuer.endswith("/"):
            raise ValueError("OIDC issuer must use its canonical value without a trailing slash")
        if not self.client_id or len(self.client_id) > 512:
            raise ValueError("OIDC client identifier is invalid")
        audience = self.client_id if self.audience is None else self.audience
        if not audience or len(audience) > 512:
            raise ValueError("OIDC audience is invalid")
        object.__setattr__(self, "audience", audience)
        algorithms = tuple(dict.fromkeys(self.allowed_signing_algorithms))
        if not algorithms or not set(algorithms) <= _OIDC_ALGORITHMS:
            raise ValueError("OIDC signing algorithms must be an explicit RS256/ES256 subset")
        object.__setattr__(self, "allowed_signing_algorithms", algorithms)
        try:
            auth_method = OIDCTokenEndpointAuthMethod(self.token_endpoint_auth_method)
        except ValueError as exc:
            raise ValueError("OIDC token endpoint authentication method is invalid") from exc
        object.__setattr__(self, "token_endpoint_auth_method", auth_method)
        confidential = auth_method is not OIDCTokenEndpointAuthMethod.NONE
        if not confidential and self.client_secret is not None:
            raise ValueError("public OIDC authentication cannot configure a client secret")
        if confidential and (
            self.client_secret is None
            or not self.client_secret
            or len(self.client_secret) > 4_096
            or any(ord(character) < 0x20 or ord(character) == 0x7F for character in self.client_secret)
        ):
            raise ValueError("OIDC client secret is invalid")
        if self.authorization_ttl_seconds < 60 or self.authorization_ttl_seconds > 600:
            raise ValueError("OIDC authorization lifetime must be between 60 and 600 seconds")
        if self.maximum_id_token_age_seconds < 30 or self.maximum_id_token_age_seconds > 900:
            raise ValueError("OIDC ID-token age bound must be between 30 and 900 seconds")
        if self.allowed_clock_skew_seconds < 0 or self.allowed_clock_skew_seconds > 120:
            raise ValueError("OIDC clock skew is outside the supported range")
        if self.http_timeout_seconds <= 0 or self.http_timeout_seconds > 30:
            raise ValueError("OIDC HTTP timeout is outside the supported range")
        if self.remote_activation_oidc_subject is not None and any(
            ord(character) < 0x20 for character in self.remote_activation_oidc_subject
        ):
            raise ValueError("remote activation owner OIDC subject is invalid")
        if self.remote_activation_verified_email_alias is not None:
            normalized = self.remote_activation_verified_email_alias.strip().casefold()
            local, separator, domain = normalized.partition("@")
            try:
                normalized.encode("ascii")
            except UnicodeEncodeError as exc:
                raise ValueError("remote activation verified-email alias must be normalized") from exc
            if (
                separator != "@"
                or not local
                or not domain
                or "@" in domain
                or normalized != self.remote_activation_verified_email_alias
                or any(ord(character) <= 0x20 or ord(character) == 0x7F for character in normalized)
            ):
                raise ValueError("remote activation verified-email alias must be normalized")
        if self.remote_activation_oidc_subject is not None and self.remote_activation_verified_email_alias is not None:
            raise ValueError("remote activation owner identity must use subject or verified-email alias")
        pins = dict(self.pinned_jwk_thumbprints)
        if len(pins) != len(self.pinned_jwk_thumbprints) or any(
            not key_id or not _is_sha256(value) for key_id, value in pins.items()
        ):
            raise ValueError("OIDC JWK thumbprint pins are invalid")
        explicit_origins = self.allowed_endpoint_origins
        origins = explicit_origins or (_canonical_https_origin(self.issuer),)
        canonical_origins = tuple(_canonical_https_origin(value, require_origin=True) for value in origins)
        if len(set(canonical_origins)) != len(canonical_origins):
            raise ValueError("OIDC endpoint origins must be unique")
        private_networks = tuple(
            _canonical_private_endpoint_network(value) for value in self.allowed_private_endpoint_cidrs
        )
        endpoint_addresses = tuple(
            _canonical_endpoint_address(value) for value in self.pinned_endpoint_addresses
        )
        if len(set(private_networks)) != len(private_networks):
            raise ValueError("OIDC private endpoint CIDR pins must be unique")
        if len(set(endpoint_addresses)) != len(endpoint_addresses):
            raise ValueError("OIDC endpoint address pins must be unique")
        private_addresses = tuple(
            value for value in endpoint_addresses if not ipaddress.ip_address(value).is_global
        )
        if private_networks or private_addresses:
            if not explicit_origins:
                raise ValueError("private OIDC endpoints require explicit endpoint origins")
            if not pins:
                raise ValueError("private OIDC endpoints require exact JWK thumbprint pins")
        object.__setattr__(self, "allowed_endpoint_origins", canonical_origins)
        object.__setattr__(self, "allowed_private_endpoint_cidrs", private_networks)
        object.__setattr__(self, "pinned_endpoint_addresses", endpoint_addresses)


@dataclass(frozen=True, slots=True)
class OIDCDiscoveryDocument:
    authorization_endpoint: str
    token_endpoint: str
    jwks_uri: str


@dataclass(frozen=True, slots=True)
class OIDCVerificationResult:
    identity: VerifiedOIDCIdentity
    id_token_hash: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class OIDCAuthorizationRequest:
    transaction_id: str
    authorization_url: str
    state: str
    expires_at: int


@dataclass(frozen=True, slots=True)
class OIDCGuidedAuthorizationRequest:
    transaction_id: str
    authorization_url: str
    state: str
    expires_at: int
    continuation_token: str = field(repr=False)


@dataclass(frozen=True, slots=True)
class OIDCPollResult:
    status: str
    interval_seconds: int
    expires_at: int
    challenge_id: str | None = None
    nonce: str | None = None
    canonical_transaction_b64: str | None = None
    approval_url: str | None = None


class OIDCProvider:
    """Pinned issuer verifier with injectable HTTP and production TLS defaults."""

    def __init__(
        self,
        config: OIDCProviderConfig,
        *,
        transport: OIDCHTTPTransport | None = None,
        clock: Callable[[], int] | None = None,
        resolver: Callable[[str, int], tuple[str, ...]] | None = None,
    ) -> None:
        self.config = config
        self.transport = transport or PinnedOIDCHTTPTransport()
        self.clock = clock or (lambda: int(time.time()))
        self.resolver = resolver or _system_address_resolver

    @property
    def discovery_url(self) -> str:
        return f"{self.config.issuer}/.well-known/openid-configuration"

    def discover(self) -> OIDCDiscoveryDocument:
        document = self._get_json(self.discovery_url, label="OIDC discovery")
        if document.get("issuer") != self.config.issuer:
            raise AuthenticationError("OIDC discovery issuer mismatch")
        authorization_endpoint = _mapping_string(document, "authorization_endpoint")
        token_endpoint = _mapping_string(document, "token_endpoint")
        jwks_uri = _mapping_string(document, "jwks_uri")
        for label, value in (
            ("authorization endpoint", authorization_endpoint),
            ("token endpoint", token_endpoint),
            ("JWKS endpoint", jwks_uri),
        ):
            # Discovery authenticates endpoint origins. Address resolution is
            # performed exactly when the server connects to each endpoint so a
            # validated snapshot is never discarded and later re-resolved.
            self._require_pinned_endpoint_origin(value, label=label)
        response_types = document.get("response_types_supported")
        if not isinstance(response_types, list) or "code" not in response_types:
            raise AuthenticationError("OIDC provider does not advertise authorization code support")
        pkce_methods = document.get("code_challenge_methods_supported")
        if not isinstance(pkce_methods, list) or "S256" not in pkce_methods:
            raise AuthenticationError("OIDC provider does not advertise PKCE S256")
        advertised_algorithms = document.get("id_token_signing_alg_values_supported")
        if not isinstance(advertised_algorithms, list) or not set(self.config.allowed_signing_algorithms) <= set(
            advertised_algorithms
        ):
            raise AuthenticationError("OIDC provider does not advertise the pinned signing algorithms")
        if self.config.token_endpoint_auth_method is not OIDCTokenEndpointAuthMethod.NONE:
            advertised_auth_methods = document.get("token_endpoint_auth_methods_supported")
            if (
                not isinstance(advertised_auth_methods, list)
                or self.config.token_endpoint_auth_method.value not in advertised_auth_methods
            ):
                raise AuthenticationError(
                    "OIDC provider does not advertise the configured token endpoint authentication method"
                )
        return OIDCDiscoveryDocument(authorization_endpoint, token_endpoint, jwks_uri)

    def authorization_url(self, *, state: str, nonce: str, code_challenge: str) -> str:
        discovery = self.discover()
        query = urllib.parse.urlencode(
            {
                "client_id": self.config.client_id,
                "code_challenge": code_challenge,
                "code_challenge_method": "S256",
                "nonce": nonce,
                "redirect_uri": self.config.redirect_uri,
                "response_type": "code",
                "scope": "openid email",
                "state": state,
            }
        )
        separator = "&" if urlsplit(discovery.authorization_endpoint).query else "?"
        return f"{discovery.authorization_endpoint}{separator}{query}"

    def exchange_and_verify(
        self,
        *,
        code: str,
        code_verifier: str,
        expected_nonce_hash: str,
    ) -> OIDCVerificationResult:
        discovery = self.discover()
        fields = {
            "client_id": self.config.client_id,
            "code": code,
            "code_verifier": code_verifier,
            "grant_type": "authorization_code",
            "redirect_uri": self.config.redirect_uri,
        }
        headers = {"accept": "application/json", "content-type": "application/x-www-form-urlencoded"}
        if self.config.token_endpoint_auth_method is OIDCTokenEndpointAuthMethod.CLIENT_SECRET_POST:
            if self.config.client_secret is None:  # pragma: no cover - dataclass invariant
                raise AuthenticationError("OIDC client authentication is unavailable")
            fields["client_secret"] = self.config.client_secret
        elif self.config.token_endpoint_auth_method is OIDCTokenEndpointAuthMethod.CLIENT_SECRET_BASIC:
            if self.config.client_secret is None:  # pragma: no cover - dataclass invariant
                raise AuthenticationError("OIDC client authentication is unavailable")
            client = urllib.parse.quote_plus(self.config.client_id)
            secret = urllib.parse.quote_plus(self.config.client_secret)
            credentials = base64.b64encode(f"{client}:{secret}".encode("utf-8")).decode("ascii")
            headers["authorization"] = f"Basic {credentials}"
            fields.pop("client_id")
        token_addresses = self._resolve_and_validate_endpoint_addresses(
            discovery.token_endpoint,
            label="token endpoint",
        )
        response = self.transport.request(
            method="POST",
            url=discovery.token_endpoint,
            resolved_addresses=token_addresses,
            headers=headers,
            body=urllib.parse.urlencode(fields).encode("ascii"),
            timeout_seconds=self.config.http_timeout_seconds,
        )
        if response.status != 200:
            raise AuthenticationError("OIDC authorization code exchange failed")
        token_response = _load_json_object(response.body, label="OIDC token response")
        id_token = token_response.get("id_token")
        if not isinstance(id_token, str) or len(id_token) < 32 or len(id_token) > 131_072:
            raise AuthenticationError("OIDC token response lacks a valid ID token")
        return self.verify_id_token(
            id_token,
            jwks_uri=discovery.jwks_uri,
            expected_nonce_hash=expected_nonce_hash,
        )

    def verify_id_token(
        self,
        id_token: str,
        *,
        jwks_uri: str,
        expected_nonce_hash: str,
    ) -> OIDCVerificationResult:
        if len(id_token) < 32 or len(id_token) > 131_072:
            raise AuthenticationError("OIDC ID token is outside the supported size bound")
        parts = id_token.split(".")
        if len(parts) != 3 or any(not part or not _B64URL.fullmatch(part) for part in parts):
            raise AuthenticationError("OIDC ID token is malformed")
        header = _load_json_object(_b64url_decode(parts[0]), label="OIDC JWT header")
        claims = _load_json_object(_b64url_decode(parts[1]), label="OIDC JWT claims")
        if any(field in header for field in ("jku", "jwk", "x5u", "crit")):
            raise AuthenticationError("OIDC ID token contains an unsupported key-selection header")
        algorithm = header.get("alg")
        key_id = header.get("kid")
        if algorithm not in self.config.allowed_signing_algorithms or algorithm == "none":
            raise AuthenticationError("OIDC ID token signing algorithm is not pinned")
        if not isinstance(key_id, str) or not key_id or len(key_id) > 512:
            raise AuthenticationError("OIDC ID token key identifier is invalid")
        jwks = self._get_json(jwks_uri, label="OIDC JWKS")
        keys = jwks.get("keys")
        if not isinstance(keys, list) or len(keys) < 1 or len(keys) > 128:
            raise AuthenticationError("OIDC JWKS key set is invalid")
        matches = [key for key in keys if isinstance(key, dict) and key.get("kid") == key_id]
        if len(matches) != 1:
            raise AuthenticationError("OIDC signing key is unavailable or ambiguous")
        jwk = matches[0]
        if jwk.get("alg") != algorithm or jwk.get("use", "sig") != "sig":
            raise AuthenticationError("OIDC signing key profile mismatch")
        key_operations = jwk.get("key_ops")
        if key_operations is not None and (
            not isinstance(key_operations, list) or "verify" not in key_operations
        ):
            raise AuthenticationError("OIDC signing key cannot verify signatures")
        pins = dict(self.config.pinned_jwk_thumbprints)
        if pins and (key_id not in pins or not secrets.compare_digest(pins[key_id], _jwk_thumbprint(jwk))):
            raise AuthenticationError("OIDC signing key does not match its configured pin")
        signature = _b64url_decode(parts[2])
        signing_input = f"{parts[0]}.{parts[1]}".encode("ascii")
        _verify_jwt_signature(jwk, algorithm=algorithm, signing_input=signing_input, signature=signature)
        return self._validate_claims(claims, id_token=id_token, expected_nonce_hash=expected_nonce_hash)

    def _validate_claims(
        self,
        claims: Mapping[str, Any],
        *,
        id_token: str,
        expected_nonce_hash: str,
    ) -> OIDCVerificationResult:
        if claims.get("iss") != self.config.issuer:
            raise AuthenticationError("OIDC ID token issuer mismatch")
        audience = claims.get("aud")
        if audience != self.config.audience and audience != [self.config.audience]:
            raise AuthenticationError("OIDC ID token audience mismatch")
        now = self.clock()
        issued_at = claims.get("iat")
        expires_at = claims.get("exp")
        not_before = claims.get("nbf")
        if type(issued_at) is not int or type(expires_at) is not int:
            raise AuthenticationError("OIDC ID token timestamps are invalid")
        if issued_at > now + self.config.allowed_clock_skew_seconds:
            raise AuthenticationError("OIDC ID token issuance time is in the future")
        if now - issued_at > self.config.maximum_id_token_age_seconds:
            raise AuthenticationError("OIDC ID token is stale")
        if expires_at <= issued_at or now >= expires_at:
            raise AuthenticationError("OIDC ID token is expired")
        if not_before is not None and (
            type(not_before) is not int or not_before > now + self.config.allowed_clock_skew_seconds
        ):
            raise AuthenticationError("OIDC ID token is not yet valid")
        nonce = claims.get("nonce")
        if not isinstance(nonce, str) or len(nonce) < 32 or len(nonce) > 256:
            raise AuthenticationError("OIDC ID token nonce is invalid")
        actual_nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        if not secrets.compare_digest(actual_nonce_hash, expected_nonce_hash):
            raise AuthenticationError("OIDC ID token nonce mismatch")
        if claims.get("email_verified") is not True:
            raise AuthenticationError("OIDC email is not verified")
        subject = claims.get("sub")
        email = claims.get("email")
        if not isinstance(subject, str) or not isinstance(email, str):
            raise AuthenticationError("OIDC subject identity is incomplete")
        identity = VerifiedOIDCIdentity(
            issuer=self.config.issuer,
            subject=subject,
            verified_email=email,
        )
        try:
            token_bytes = id_token.encode("ascii")
        except UnicodeEncodeError as exc:  # defensive; base64url validation should reject this first.
            raise AuthenticationError("OIDC ID token is malformed") from exc
        return OIDCVerificationResult(
            identity=identity,
            id_token_hash=hashlib.sha256(token_bytes).hexdigest(),
            expires_at=expires_at,
        )

    def _get_json(self, url: str, *, label: str) -> dict[str, Any]:
        addresses = self._resolve_and_validate_endpoint_addresses(url, label=label)
        response = self.transport.request(
            method="GET",
            url=url,
            resolved_addresses=addresses,
            headers={"accept": "application/json"},
            body=None,
            timeout_seconds=self.config.http_timeout_seconds,
        )
        if response.status != 200:
            raise GateBlocked("oidc_provider", f"{label} is unavailable")
        if len(response.body) > 1_048_576:
            raise GateBlocked("oidc_provider", f"{label} exceeds the supported response bound")
        return _load_json_object(response.body, label=label)

    def _require_pinned_endpoint_origin(self, value: str, *, label: str) -> None:
        _require_discovered_https_url(value, label=label)
        origin = _canonical_https_origin(value)
        if origin not in self.config.allowed_endpoint_origins:
            raise AuthenticationError(f"OIDC {label} origin is not pinned")

    def _resolve_and_validate_endpoint_addresses(
        self,
        value: str,
        *,
        label: str,
    ) -> tuple[str, ...]:
        self._require_pinned_endpoint_origin(value, label=label)
        parsed = urlsplit(value)
        if parsed.hostname is None:
            raise GateBlocked("oidc_provider", f"OIDC {label} URL has no hostname")
        try:
            addresses = self.resolver(parsed.hostname, parsed.port or 443)
        except Exception as exc:
            raise GateBlocked("oidc_provider", f"OIDC {label} address resolution failed") from exc
        if not addresses:
            raise GateBlocked("oidc_provider", f"OIDC {label} address resolution was empty")
        try:
            parsed_addresses = tuple(ipaddress.ip_address(address) for address in addresses)
        except (TypeError, ValueError) as exc:
            raise GateBlocked("oidc_provider", f"OIDC {label} resolved to an invalid address") from exc
        canonical_addresses = tuple(str(address) for address in parsed_addresses)
        if len(canonical_addresses) > 32 or len(set(canonical_addresses)) != len(canonical_addresses):
            raise GateBlocked("oidc_provider", f"OIDC {label} address resolution was excessive or ambiguous")
        exact_pins = frozenset(self.config.pinned_endpoint_addresses)
        private_networks = tuple(
            ipaddress.ip_network(value, strict=True)
            for value in self.config.allowed_private_endpoint_cidrs
        )
        for address, canonical in zip(parsed_addresses, canonical_addresses, strict=True):
            if exact_pins and canonical not in exact_pins:
                raise GateBlocked("oidc_provider", f"OIDC {label} address is not exactly pinned")
            if address.is_global:
                continue
            if canonical in exact_pins or any(address in network for network in private_networks):
                continue
            raise GateBlocked(
                "oidc_provider",
                f"OIDC {label} resolved to a non-public address that is not explicitly pinned",
            )
        return canonical_addresses


class OIDCEnrollmentCoordinator:
    """Compose verified OIDC identity with enrollment challenge creation."""

    def __init__(
        self,
        store: Any,
        provider: OIDCProvider,
        enrollment: EnrollmentService,
        *,
        approval_client: Any | None = None,
    ) -> None:
        if enrollment.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            raise GateBlocked("oidc_enrollment", "production OIDC enrollment requires the server-agent profile")
        if enrollment.binding_assurance == "lab":
            raise GateBlocked("oidc_enrollment", "production OIDC enrollment refuses lab identity binding")
        if store is not enrollment.store:
            raise ValueError("OIDC coordinator and enrollment service must share one transaction store")
        self.store = store
        self.provider = provider
        self.enrollment = enrollment
        self.approval_client = approval_client

    def begin_authorization(
        self,
        *,
        domain_id: str,
        harness_kind: str,
        harness_name: str,
        public_key_pem: str,
        remote_activation: bool = False,
    ) -> OIDCGuidedAuthorizationRequest:
        if self.enrollment.outage_gate is not None:
            self.enrollment.outage_gate.require_issuance()
        key_id = self.enrollment.validate_begin_request(
            domain_id=domain_id,
            harness_kind=harness_kind,
            harness_name=harness_name,
            public_key_pem=public_key_pem,
        )
        transaction_id = str(uuid4())
        state = secrets.token_urlsafe(32)
        continuation_token = secrets.token_urlsafe(32)
        continuation_hash = hashlib.sha256(continuation_token.encode("ascii")).hexdigest()
        nonce = secrets.token_urlsafe(32)
        code_verifier = secrets.token_urlsafe(64)
        code_challenge = _b64url_encode(hashlib.sha256(code_verifier.encode("ascii")).digest())
        authorization_url = self.provider.authorization_url(
            state=state,
            nonce=nonce,
            code_challenge=code_challenge,
        )
        now = self.provider.clock()
        expires_at = now + self.provider.config.authorization_ttl_seconds
        verifier_encrypted = self.store.cipher.encrypt_json(
            {"code_verifier": code_verifier},
            purpose=f"oidc-pkce:{transaction_id}",
        )
        expected_subject = self.provider.config.remote_activation_oidc_subject
        expected_email = self.provider.config.remote_activation_verified_email_alias
        if remote_activation and (expected_subject is None) == (expected_email is None):
            raise GateBlocked(
                "remote_activation_identity_policy",
                "remote server activation requires one exact approved owner identity",
            )
        activation_encrypted = (
            self.store.cipher.encrypt_json(
                {
                    "schema": "agentnet.oidc.remote-activation.v1",
                    "transaction_id": transaction_id,
                    "authorization_url": authorization_url,
                    "expected_oidc_issuer": self.provider.config.issuer,
                    "expected_oidc_subject": expected_subject,
                    "expected_verified_email_alias": expected_email,
                },
                purpose=f"oidc-guided-activation:{transaction_id}",
            )
            if remote_activation
            else None
        )
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO oidc_enrollment_transactions(
                    transaction_id,domain_id,issuer,client_id,audience,redirect_uri,state_hash,nonce_hash,
                    code_verifier_encrypted,harness_kind,harness_name,public_key_pem,key_id,binding_assurance,
                    status,created_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?)""",
                (
                    transaction_id,
                    domain_id,
                    self.provider.config.issuer,
                    self.provider.config.client_id,
                    self.provider.config.audience,
                    self.provider.config.redirect_uri,
                    hashlib.sha256(state.encode("ascii")).hexdigest(),
                    hashlib.sha256(nonce.encode("ascii")).hexdigest(),
                    verifier_encrypted,
                    harness_kind,
                    harness_name,
                    public_key_pem,
                    key_id,
                    self.enrollment.binding_assurance,
                    "pending",
                    now,
                    expires_at,
                ),
            )
            connection.execute(
                """INSERT INTO oidc_enrollment_continuations(
                       transaction_id,continuation_hash,status,challenge_encrypted,poll_after_at,
                       poll_interval_seconds,poll_count,created_at,updated_at,expires_at
                   ) VALUES(?,?,'awaiting_oidc',?,?,2,0,?,?,?)""",
                (
                    transaction_id,
                    continuation_hash,
                    activation_encrypted,
                    now + 2,
                    now,
                    now,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "oidc.authorization.created",
                    "domain_id": domain_id,
                    "expires_at": expires_at,
                    "issuer": self.provider.config.issuer,
                    "transaction_id": transaction_id,
                },
            )
        return OIDCGuidedAuthorizationRequest(
            transaction_id,
            authorization_url,
            state,
            expires_at,
            continuation_token,
        )

    def remote_activation_authorization_url(self) -> str:
        """Return one server-staged browser authorization without exposing its state elsewhere."""

        now = self.provider.clock()
        rows = self.store.fetch_all(
            """SELECT c.transaction_id,c.challenge_encrypted,c.expires_at,o.status AS oidc_status
                 FROM oidc_enrollment_continuations c
                 JOIN oidc_enrollment_transactions o ON o.transaction_id=c.transaction_id
                WHERE c.status='awaiting_oidc' AND c.challenge_encrypted IS NOT NULL
                  AND c.expires_at>? AND o.status='pending'
                ORDER BY c.created_at,c.transaction_id""",
            (now,),
        )
        if len(rows) != 1:
            raise GateBlocked(
                "remote_activation_unavailable",
                "exactly one remote server activation must be waiting",
            )
        row = rows[0]
        transaction_id = str(row["transaction_id"])
        value = self.store.cipher.decrypt_json(
            row["challenge_encrypted"],
            purpose=f"oidc-guided-activation:{transaction_id}",
        )
        if (
            not isinstance(value, dict)
            or set(value)
            != {
                "schema",
                "transaction_id",
                "authorization_url",
                "expected_oidc_issuer",
                "expected_oidc_subject",
                "expected_verified_email_alias",
            }
            or value.get("schema") != "agentnet.oidc.remote-activation.v1"
            or value.get("transaction_id") != transaction_id
            or not isinstance(value.get("authorization_url"), str)
            or value.get("expected_oidc_issuer") != self.provider.config.issuer
            or value.get("expected_oidc_subject")
            != self.provider.config.remote_activation_oidc_subject
            or value.get("expected_verified_email_alias")
            != self.provider.config.remote_activation_verified_email_alias
            or (value.get("expected_oidc_subject") is None)
            == (value.get("expected_verified_email_alias") is None)
        ):
            raise AuthenticationError("OIDC remote activation is unavailable")
        authorization_url = str(value["authorization_url"])
        _require_https_url(authorization_url, label="authorization URL", allow_query=True)
        return authorization_url

    def remote_activation_for_challenge(self, challenge_id: str) -> bool:
        """Report whether a verified callback belongs to browser-only server activation."""

        row = self.store.fetch_one(
            """SELECT c.transaction_id,c.challenge_encrypted
                 FROM oidc_enrollment_continuations c
                 JOIN oidc_enrollment_transactions o ON o.transaction_id=c.transaction_id
                WHERE o.enrollment_challenge_id=? AND c.status='callback_ready'""",
            (challenge_id,),
        )
        if row is None or not row["challenge_encrypted"]:
            return False
        transaction_id = str(row["transaction_id"])
        value = self.store.cipher.decrypt_json(
            row["challenge_encrypted"],
            purpose=f"oidc-guided-challenge:{transaction_id}",
        )
        if (
            not isinstance(value, dict)
            or set(value)
            != {
                "challenge_id",
                "nonce",
                "canonical_transaction_b64",
                "activation_mode",
            }
            or value.get("challenge_id") != challenge_id
            or value.get("activation_mode") != "remote_browser"
        ):
            raise AuthenticationError("OIDC remote activation is unavailable")
        return True

    def poll_continuation(
        self,
        *,
        transaction_id: str,
        continuation_token: str,
    ) -> OIDCPollResult:
        if (
            not isinstance(transaction_id, str)
            or not 16 <= len(transaction_id) <= 128
            or not isinstance(continuation_token, str)
            or not 32 <= len(continuation_token) <= 128
        ):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        supplied_hash = hashlib.sha256(continuation_token.encode("utf-8")).hexdigest()
        approval_possession_secret = _approval_possession_secret(
            continuation_token,
            transaction_id=transaction_id,
        )
        approval_possession_hash = hashlib.sha256(
            approval_possession_secret.encode("ascii")
        ).hexdigest()
        now = self.provider.clock()
        challenge_value: dict[str, Any] | None = None
        should_stage = False
        approval_request_id: str | None = None
        approval_transaction_digest: str | None = None
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_enrollment_continuations WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if row is None or not secrets.compare_digest(
                str(row["continuation_hash"]), supplied_hash
            ):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            status = str(row["status"])
            expires_at = int(row["expires_at"])
            interval = int(row["poll_interval_seconds"])
            if now >= expires_at:
                if status not in {"enrolled", "expired", "failed"}:
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET status='expired',updated_at=? WHERE transaction_id=?""",
                        (now, transaction_id),
                    )
                return OIDCPollResult("expired", interval, expires_at)
            if status not in {"enrolled", "expired", "failed"}:
                # The finite poll budget protects the browser-authorization wait only.
                # Callback creates a fresh enrollment challenge and resets ``expires_at``;
                # the human Approval ceremony is bounded by that expiry, not by polling
                # already spent before the callback.
                awaiting_oidc = status == "awaiting_oidc"
                if awaiting_oidc and int(row["poll_count"]) >= 60:
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET status='failed',updated_at=? WHERE transaction_id=?""",
                        (now, transaction_id),
                    )
                    status = "failed"
                elif now < int(row["poll_after_at"]):
                    interval = min(10, interval + 2)
                    if awaiting_oidc:
                        connection.execute(
                            """UPDATE oidc_enrollment_continuations
                                  SET poll_interval_seconds=?,poll_after_at=?,
                                      poll_count=poll_count+1,updated_at=?
                                WHERE transaction_id=?""",
                            (interval, now + interval, now, transaction_id),
                        )
                    else:
                        connection.execute(
                            """UPDATE oidc_enrollment_continuations
                                  SET poll_interval_seconds=?,poll_after_at=?,updated_at=?
                                WHERE transaction_id=?""",
                            (interval, now + interval, now, transaction_id),
                        )
                    return OIDCPollResult("slow_down", interval, expires_at)
                elif awaiting_oidc:
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET poll_after_at=?,poll_count=poll_count+1,updated_at=?
                            WHERE transaction_id=?""",
                        (now + interval, now, transaction_id),
                    )
                else:
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET poll_after_at=?,updated_at=? WHERE transaction_id=?""",
                        (now + interval, now, transaction_id),
                    )
            if status in {"callback_ready", "approval_pending"}:
                encrypted = row["challenge_encrypted"]
                if not encrypted:
                    raise AuthenticationError("OIDC enrollment continuation is unavailable")
                value = self.store.cipher.decrypt_json(
                    encrypted,
                    purpose=f"oidc-guided-challenge:{transaction_id}",
                )
                if not isinstance(value, dict):
                    raise AuthenticationError("OIDC enrollment continuation is unavailable")
                challenge_value = value
                should_stage = status == "callback_ready"
                if status == "approval_pending":
                    approval_request_id = str(row["approval_request_id"] or "")
                    approval_transaction_digest = str(
                        row["approval_transaction_digest"] or ""
                    )
            else:
                rendered = "authorization_pending" if status == "awaiting_oidc" else status
                return OIDCPollResult(rendered, interval, expires_at)

        if challenge_value is None:  # pragma: no cover - guarded above
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        if should_stage:
            if self.approval_client is None:
                raise GateBlocked(
                    "guided_enrollment_unavailable",
                    "guided enrollment approval service is unavailable",
                )
            try:
                canonical = base64.b64decode(
                    str(challenge_value["canonical_transaction_b64"]).encode("ascii"),
                    validate=True,
                )
                canonical_value = json.loads(canonical)
                challenge_expires_at = canonical_value["expires_at"]
            except Exception as exc:
                raise AuthenticationError("OIDC enrollment continuation is unavailable") from exc
            if (
                not isinstance(canonical_value, dict)
                or canonical_json(canonical_value) != canonical
                or type(challenge_expires_at) is not int
                or not now < challenge_expires_at <= expires_at
            ):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            transaction_digest = hashlib.sha256(canonical).hexdigest()
            identity = self.store.fetch_one(
                "SELECT domain_id FROM oidc_enrollment_transactions WHERE transaction_id=?",
                (transaction_id,),
            )
            if identity is None:
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            created = self.approval_client.create_request(
                idempotency_key=f"core:identity.enrollment.approve:{transaction_id}",
                domain_id=str(identity["domain_id"]),
                approval_purpose="identity.enrollment.approve",
                canonical_transaction=canonical,
                transaction_digest=transaction_digest,
                possession_hash=approval_possession_hash,
                request_expires_at=challenge_expires_at,
            )
            request_id = created.get("request_id")
            response_digest = created.get("transaction_digest")
            request_expires_at = created.get("expires_at")
            if (
                not isinstance(request_id, str)
                or not 16 <= len(request_id) <= 128
                or response_digest != transaction_digest
                or not isinstance(request_expires_at, int)
                or request_expires_at != challenge_expires_at
            ):
                raise AuthenticationError("approval service response denied")
            with self.store.transaction() as connection:
                current = connection.execute(
                    "SELECT * FROM oidc_enrollment_continuations WHERE transaction_id=?",
                    (transaction_id,),
                ).fetchone()
                if current is None or not secrets.compare_digest(
                    str(current["continuation_hash"]), supplied_hash
                ):
                    raise AuthenticationError("OIDC enrollment continuation is unavailable")
                if current["status"] == "callback_ready":
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET status='approval_pending',approval_request_id=?,
                                  approval_transaction_digest=?,approval_request_expires_at=?,
                                  updated_at=? WHERE transaction_id=? AND status='callback_ready'""",
                        (
                            request_id,
                            transaction_digest,
                            request_expires_at,
                            now,
                            transaction_id,
                        ),
                    )
                    self.store.append_audit(
                        connection,
                        {
                            "action": "oidc.approval.staged",
                            "approval_request_id": request_id,
                            "transaction_digest": transaction_digest,
                            "transaction_id": transaction_id,
                        },
                    )
                elif (
                    current["status"] != "approval_pending"
                    or current["approval_request_id"] != request_id
                    or current["approval_transaction_digest"] != transaction_digest
                ):
                    raise ReplayError("OIDC approval staging conflicted")
            approval_request_id = request_id
            approval_transaction_digest = transaction_digest
        if (
            self.approval_client is None
            or not approval_request_id
            or not approval_transaction_digest
        ):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        approval_status = self.approval_client.request_status(
            request_id=approval_request_id,
            transaction_digest=approval_transaction_digest,
        )
        remote_state = approval_status.get("state")
        if remote_state in {"rejected", "expired"}:
            terminal_status = "expired" if remote_state == "expired" else "failed"
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE oidc_enrollment_continuations
                          SET status=?,updated_at=?
                        WHERE transaction_id=? AND status='approval_pending'""",
                    (terminal_status, now, transaction_id),
                )
            return OIDCPollResult(terminal_status, interval, expires_at)
        if remote_state not in {"pending", "issued"}:
            raise AuthenticationError("approval service response denied")
        approval_config = getattr(self.approval_client, "config", None)
        approval_origin = getattr(approval_config, "origin", None)
        if not isinstance(approval_origin, str) or not approval_origin.startswith("https://"):
            raise GateBlocked(
                "guided_enrollment_unavailable",
                "guided enrollment approval entrypoint is unavailable",
            )
        return OIDCPollResult(
            "approval_ready" if remote_state == "issued" else "approval_pending",
            interval,
            expires_at,
            challenge_id=str(challenge_value["challenge_id"]),
            nonce=str(challenge_value["nonce"]),
            canonical_transaction_b64=str(challenge_value["canonical_transaction_b64"]),
            approval_url=f"{approval_origin.rstrip('/')}/approval",
        )

    def complete_guided_enrollment(
        self,
        *,
        transaction_id: str,
        continuation_token: str,
        possession_signature: str,
    ) -> EnrollmentResult:
        """Complete one brokered enrollment without exposing approval receipt."""

        if self.approval_client is None:
            raise GateBlocked(
                "guided_enrollment_unavailable",
                "guided enrollment approval service is unavailable",
            )
        if (
            not isinstance(transaction_id, str)
            or not 16 <= len(transaction_id) <= 128
            or not isinstance(continuation_token, str)
            or not 32 <= len(continuation_token) <= 128
            or not isinstance(possession_signature, str)
            or not 16 <= len(possession_signature) <= 2_048
            or _B64URL.fullmatch(possession_signature) is None
        ):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        supplied_hash = hashlib.sha256(continuation_token.encode("utf-8")).hexdigest()
        request_digest = hashlib.sha256(
            json.dumps(
                {
                    "challenge_signature_hash": hashlib.sha256(
                        possession_signature.encode("ascii")
                    ).hexdigest(),
                    "continuation_hash": supplied_hash,
                    "transaction_id": transaction_id,
                },
                sort_keys=True,
                separators=(",", ":"),
            ).encode("utf-8")
        ).hexdigest()
        now = self.provider.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_enrollment_continuations WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if row is None or not secrets.compare_digest(
                str(row["continuation_hash"]), supplied_hash
            ):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            if now >= int(row["expires_at"]):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            if row["status"] == "enrolled":
                return self._stored_guided_result(row, request_digest=request_digest)
            if row["status"] != "approval_pending":
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            if (
                row["approval_request_expires_at"] is None
                or now >= int(row["approval_request_expires_at"])
                or not row["approval_request_id"]
                or not row["approval_transaction_digest"]
                or not row["challenge_encrypted"]
            ):
                raise AuthenticationError("approval request denied")
            if row["completion_request_digest"] is not None and not secrets.compare_digest(
                str(row["completion_request_digest"]), request_digest
            ):
                raise ReplayError("guided enrollment completion conflicted")
            challenge_value = self.store.cipher.decrypt_json(
                row["challenge_encrypted"],
                purpose=f"oidc-guided-challenge:{transaction_id}",
            )
            if not isinstance(challenge_value, dict):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            request_id = str(row["approval_request_id"])
            approval_transaction_digest = str(row["approval_transaction_digest"])
            reserved_request_digest = row["completion_request_digest"]

        try:
            canonical_transaction = base64.b64decode(
                str(challenge_value["canonical_transaction_b64"]).encode("ascii"),
                validate=True,
            )
        except Exception as exc:
            raise AuthenticationError("OIDC enrollment continuation is unavailable") from exc
        if (
            not canonical_transaction
            or len(canonical_transaction) > 98_304
            or hashlib.sha256(canonical_transaction).hexdigest()
            != approval_transaction_digest
        ):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        identity = self.store.fetch_one(
            """SELECT o.domain_id,e.public_key_pem,e.challenge_id
                 FROM oidc_enrollment_transactions o
                 JOIN enrollment_challenges e
                   ON e.challenge_id=o.enrollment_challenge_id
                WHERE o.transaction_id=?""",
            (transaction_id,),
        )
        if identity is None or identity["challenge_id"] != challenge_value.get("challenge_id"):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        try:
            transcript = json.loads(canonical_transaction)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise AuthenticationError("OIDC enrollment continuation is unavailable") from exc
        if not isinstance(transcript, dict):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        verify_signature(
            str(identity["public_key_pem"]),
            "agentnet.enrollment.pop.v1",
            transcript,
            possession_signature,
        )

        challenge_id = str(challenge_value.get("challenge_id", ""))
        if reserved_request_digest is not None:
            consumed = self.store.fetch_one(
                "SELECT consumed_at FROM enrollment_challenges WHERE challenge_id=?",
                (challenge_id,),
            )
            if consumed is not None and consumed["consumed_at"] is not None:
                return self._commit_guided_result(
                    transaction_id=transaction_id,
                    continuation_hash=supplied_hash,
                    request_digest=request_digest,
                    result=self._recover_guided_result(challenge_id),
                    now=now,
                )

        # This idempotency key participates in the approval host's versioned
        # retrieval digest. Exact response-loss retries must preserve this body
        # shape or the host correctly rejects them as a conflicting retrieval.
        receipt = self.approval_client.retrieve_receipt(
            request_id=request_id,
            possession_secret=_approval_possession_secret(
                continuation_token,
                transaction_id=transaction_id,
            ),
            domain_id=str(identity["domain_id"]),
            approval_purpose="identity.enrollment.approve",
            transaction_digest=approval_transaction_digest,
            idempotency_key=(
                f"core:identity.enrollment.complete:{transaction_id}:{request_digest}"
            ),
        )

        with self.store.transaction() as connection:
            current = connection.execute(
                "SELECT * FROM oidc_enrollment_continuations WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if current is None or not secrets.compare_digest(
                str(current["continuation_hash"]), supplied_hash
            ):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            if current["status"] == "enrolled":
                return self._stored_guided_result(current, request_digest=request_digest)
            if current["status"] != "approval_pending":
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            reserved = current["completion_request_digest"]
            if reserved is None:
                update = connection.execute(
                    """UPDATE oidc_enrollment_continuations
                          SET completion_request_digest=?,updated_at=?
                        WHERE transaction_id=? AND status='approval_pending'
                          AND completion_request_digest IS NULL""",
                    (request_digest, now, transaction_id),
                )
                if update.rowcount != 1:
                    raise ReplayError("guided enrollment completion conflicted")
            elif not secrets.compare_digest(str(reserved), request_digest):
                raise ReplayError("guided enrollment completion conflicted")

        consumed = self.store.fetch_one(
            "SELECT consumed_at FROM enrollment_challenges WHERE challenge_id=?",
            (challenge_id,),
        )
        if consumed is not None and consumed["consumed_at"] is not None:
            result = self._recover_guided_result(challenge_id)
        else:
            result = self.enrollment.complete(
                challenge_id=challenge_id,
                nonce=str(challenge_value.get("nonce", "")),
                canonical_transaction=canonical_transaction,
                possession_signature=possession_signature,
                approval=receipt,
            )
        return self._commit_guided_result(
            transaction_id=transaction_id,
            continuation_hash=supplied_hash,
            request_digest=request_digest,
            result=result,
            now=now,
        )

    @staticmethod
    def _guided_result_payload(result: EnrollmentResult) -> dict[str, Any]:
        return {
            "principal_id": result.principal_id,
            "harness_id": result.harness_id,
            "credential_id": result.credential_id,
            "key_id": result.key_id,
            "credential_epoch": result.credential_epoch,
            "harness_status": result.harness_status,
            "actor": result.actor.model_dump(mode="json"),
        }

    @staticmethod
    def _guided_result_from_payload(value: Any) -> EnrollmentResult:
        if not isinstance(value, dict):
            raise AuthenticationError("OIDC enrollment continuation is unavailable")
        try:
            actor_value = dict(value["actor"])
            actor_value["kind"] = ActorKind(actor_value["kind"])
            actor = VerifiedActor.model_validate(actor_value, strict=True)
            return EnrollmentResult(
                principal_id=str(value["principal_id"]),
                harness_id=str(value["harness_id"]),
                credential_id=str(value["credential_id"]),
                key_id=str(value["key_id"]),
                credential_epoch=int(value["credential_epoch"]),
                harness_status=str(value["harness_status"]),
                actor=actor,
            )
        except (KeyError, TypeError, ValueError) as exc:
            raise AuthenticationError("OIDC enrollment continuation is unavailable") from exc

    def _stored_guided_result(self, row: Any, *, request_digest: str) -> EnrollmentResult:
        if (
            row["completion_request_digest"] is None
            or not secrets.compare_digest(
                str(row["completion_request_digest"]), request_digest
            )
            or not row["completion_response_encrypted"]
        ):
            raise ReplayError("guided enrollment completion conflicted")
        value = self.store.cipher.decrypt_json(
            row["completion_response_encrypted"],
            purpose=f"oidc-guided-result:{row['transaction_id']}",
        )
        return self._guided_result_from_payload(value)

    def _recover_guided_result(self, challenge_id: str) -> EnrollmentResult:
        harness_id = str(uuid5(NAMESPACE_URL, f"agentnet:harness:{challenge_id}"))
        credential_id = str(uuid5(NAMESPACE_URL, f"agentnet:credential:{challenge_id}"))
        row = self.store.fetch_one(
            """SELECT h.domain_id,h.principal_id,h.status AS harness_status,
                      h.binding_assurance,h.credential_epoch,
                      c.credential_id,c.key_id,c.status AS credential_status,
                      e.consumed_at,e.key_id AS challenge_key_id
                 FROM harnesses h
                 JOIN credentials c ON c.harness_id=h.harness_id
                 JOIN enrollment_challenges e ON e.challenge_id=?
                WHERE h.harness_id=? AND c.credential_id=?
                  AND c.epoch=h.credential_epoch""",
            (challenge_id, harness_id, credential_id),
        )
        if (
            row is None
            or row["credential_status"] != "active"
            or row["consumed_at"] is None
            or row["key_id"] != row["challenge_key_id"]
        ):
            raise AuthenticationError("OIDC enrollment completion recovery is unavailable")
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=str(row["domain_id"]),
            principal_id=str(row["principal_id"]),
            harness_id=harness_id,
            credential_id=str(row["credential_id"]),
            credential_epoch=int(row["credential_epoch"]),
            binding_assurance=str(row["binding_assurance"]),
        )
        return EnrollmentResult(
            principal_id=str(row["principal_id"]),
            harness_id=harness_id,
            credential_id=str(row["credential_id"]),
            key_id=str(row["key_id"]),
            credential_epoch=int(row["credential_epoch"]),
            harness_status=str(row["harness_status"]),
            actor=actor,
        )

    def _commit_guided_result(
        self,
        *,
        transaction_id: str,
        continuation_hash: str,
        request_digest: str,
        result: EnrollmentResult,
        now: int,
    ) -> EnrollmentResult:
        encrypted = self.store.cipher.encrypt_json(
            self._guided_result_payload(result),
            purpose=f"oidc-guided-result:{transaction_id}",
        )
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_enrollment_continuations WHERE transaction_id=?",
                (transaction_id,),
            ).fetchone()
            if row is None or not secrets.compare_digest(
                str(row["continuation_hash"]), continuation_hash
            ):
                raise AuthenticationError("OIDC enrollment continuation is unavailable")
            if row["status"] == "enrolled":
                return self._stored_guided_result(row, request_digest=request_digest)
            if (
                row["status"] != "approval_pending"
                or row["completion_request_digest"] is None
                or not secrets.compare_digest(
                    str(row["completion_request_digest"]), request_digest
                )
            ):
                raise ReplayError("guided enrollment completion conflicted")
            updated = connection.execute(
                """UPDATE oidc_enrollment_continuations
                      SET status='enrolled',completion_response_encrypted=?,updated_at=?
                    WHERE transaction_id=? AND status='approval_pending'
                      AND completion_request_digest=?""",
                (encrypted, now, transaction_id, request_digest),
            )
            if updated.rowcount != 1:
                raise ReplayError("guided enrollment completion conflicted")
            self.store.append_audit(
                connection,
                {
                    "action": "oidc.enrollment.guided.completed",
                    "credential_id": result.credential_id,
                    "harness_id": result.harness_id,
                    "transaction_id": transaction_id,
                },
            )
        return result

    def fail_authorization(self, *, state: str) -> None:
        """Consume one exact pending authorization after a provider error response."""

        if not isinstance(state, str) or len(state) < 32 or len(state) > 256:
            raise AuthenticationError("OIDC authorization state is invalid")
        now = self.provider.clock()
        state_hash = hashlib.sha256(state.encode("utf-8")).hexdigest()
        expired = False
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_enrollment_transactions WHERE state_hash=?",
                (state_hash,),
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC authorization state is unavailable")
            if row["status"] != "pending":
                raise ReplayError("OIDC authorization state was already consumed")
            self._require_provider_binding(row)
            expired = now >= int(row["expires_at"])
            updated = connection.execute(
                """UPDATE oidc_enrollment_transactions SET status='failed',consumed_at=?
                   WHERE transaction_id=? AND status='pending'""",
                (now, row["transaction_id"]),
            )
            if updated.rowcount != 1:
                raise ReplayError("OIDC authorization state was already consumed")
            connection.execute(
                """UPDATE oidc_enrollment_continuations
                      SET status=?,updated_at=?
                    WHERE transaction_id=? AND status='awaiting_oidc'""",
                ("expired" if expired else "failed", now, row["transaction_id"]),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "oidc.authorization.failed",
                    "transaction_id": row["transaction_id"],
                },
            )
        if expired:
            raise AuthenticationError("OIDC authorization state is expired")

    def complete_authorization(self, *, state: str, code: str) -> EnrollmentChallenge:
        if self.enrollment.outage_gate is not None:
            self.enrollment.outage_gate.require_issuance()
        if not isinstance(state, str) or len(state) < 32 or len(state) > 256:
            raise AuthenticationError("OIDC authorization state is invalid")
        if not isinstance(code, str) or len(code) < 8 or len(code) > 4_096:
            raise AuthenticationError("OIDC authorization code is invalid")
        now = self.provider.clock()
        state_hash = hashlib.sha256(state.encode("utf-8")).hexdigest()
        code_hash = hashlib.sha256(code.encode("utf-8")).hexdigest()
        row = self._claim_transaction(state_hash=state_hash, now=now)
        transaction_id = row["transaction_id"]
        try:
            self._require_provider_binding(row)
            if public_key_thumbprint(row["public_key_pem"]) != row["key_id"]:
                raise AuthenticationError("OIDC enrollment candidate key binding is corrupt")
            replay = self.store.fetch_one(
                "SELECT 1 AS present FROM replay_nonces WHERE actor_id=? AND nonce_hash=?",
                (self._code_replay_actor, code_hash),
            )
            if replay is not None:
                raise ReplayError("OIDC authorization code was already consumed")
            encrypted = self.store.cipher.decrypt_json(
                row["code_verifier_encrypted"],
                purpose=f"oidc-pkce:{transaction_id}",
            )
            if not isinstance(encrypted, dict) or not isinstance(encrypted.get("code_verifier"), str):
                raise AuthenticationError("OIDC PKCE state is unavailable")
            result = self.provider.exchange_and_verify(
                code=code,
                code_verifier=encrypted["code_verifier"],
                expected_nonce_hash=row["nonce_hash"],
            )
            return self._commit_verified_identity(
                row=row,
                code_hash=code_hash,
                result=result,
                now=now,
            )
        except RemoteActivationIdentityMismatch:
            self._restore_remote_activation_pending(transaction_id, claimed_at=now)
            raise
        except Exception as exc:
            self._mark_failed(transaction_id, now=now)
            if isinstance(exc, ExtensionError):
                raise
            raise AuthenticationError("OIDC authorization could not be verified") from exc

    @property
    def _code_replay_actor(self) -> str:
        return f"oidc-code:{self.provider.config.issuer}:{self.provider.config.client_id}"

    @property
    def _token_replay_actor(self) -> str:
        return f"oidc-token:{self.provider.config.issuer}:{self.provider.config.client_id}"

    def _claim_transaction(self, *, state_hash: str, now: int) -> Any:
        expired = False
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM oidc_enrollment_transactions WHERE state_hash=?", (state_hash,)
            ).fetchone()
            if row is None:
                raise AuthenticationError("OIDC authorization state is unavailable")
            if row["status"] != "pending":
                raise ReplayError("OIDC authorization state was already consumed")
            if now >= int(row["expires_at"]):
                connection.execute(
                    "UPDATE oidc_enrollment_transactions SET status='failed' WHERE transaction_id=? AND status='pending'",
                    (row["transaction_id"],),
                )
                connection.execute(
                    """UPDATE oidc_enrollment_continuations
                          SET status='expired',updated_at=?
                        WHERE transaction_id=? AND status='awaiting_oidc'""",
                    (now, row["transaction_id"]),
                )
                expired = True
            else:
                updated = connection.execute(
                    """UPDATE oidc_enrollment_transactions SET status='exchanging',claimed_at=?
                       WHERE transaction_id=? AND status='pending'""",
                    (now, row["transaction_id"]),
                )
                if updated.rowcount != 1:
                    raise ReplayError("OIDC authorization state was already consumed")
        if expired:
            raise AuthenticationError("OIDC authorization state is expired")
        return row

    def _commit_verified_identity(
        self,
        *,
        row: Any,
        code_hash: str,
        result: OIDCVerificationResult,
        now: int,
    ) -> EnrollmentChallenge:
        replay_expires_at = max(result.expires_at, now + 86_400)
        try:
            with self.store.transaction() as connection:
                current = connection.execute(
                    "SELECT * FROM oidc_enrollment_transactions WHERE transaction_id=?",
                    (row["transaction_id"],),
                ).fetchone()
                if current is None or current["status"] != "exchanging" or current["claimed_at"] != now:
                    raise ReplayError("OIDC authorization transaction is no longer current")
                self._require_provider_binding(current)
                # This transaction must keep the current-state check, replay
                # claims, challenge creation, and terminal update atomic. Both
                # supported stores provide that transaction boundary; unique
                # constraints convert a concurrent loser into ReplayError.
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._code_replay_actor, code_hash, replay_expires_at),
                )
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._token_replay_actor, result.id_token_hash, replay_expires_at),
                )
                continuation_current = connection.execute(
                    """SELECT challenge_encrypted FROM oidc_enrollment_continuations
                        WHERE transaction_id=? AND status='awaiting_oidc'""",
                    (current["transaction_id"],),
                ).fetchone()
                if continuation_current is None:
                    raise ReplayError("OIDC enrollment continuation is no longer current")
                remote_activation = False
                if continuation_current["challenge_encrypted"]:
                    activation = self.store.cipher.decrypt_json(
                        continuation_current["challenge_encrypted"],
                        purpose=f"oidc-guided-activation:{current['transaction_id']}",
                    )
                    if (
                        not isinstance(activation, dict)
                        or set(activation)
                        != {
                            "schema",
                            "transaction_id",
                            "authorization_url",
                            "expected_oidc_issuer",
                            "expected_oidc_subject",
                            "expected_verified_email_alias",
                        }
                        or activation.get("schema") != "agentnet.oidc.remote-activation.v1"
                        or activation.get("transaction_id") != current["transaction_id"]
                        or activation.get("expected_oidc_issuer") != current["issuer"]
                        or activation.get("expected_oidc_subject")
                        != self.provider.config.remote_activation_oidc_subject
                        or activation.get("expected_verified_email_alias")
                        != self.provider.config.remote_activation_verified_email_alias
                        or (activation.get("expected_oidc_subject") is None)
                        == (activation.get("expected_verified_email_alias") is None)
                    ):
                        raise AuthenticationError("OIDC remote activation is unavailable")
                    subject_matches = activation.get("expected_oidc_subject") == result.identity.subject
                    email_matches = (
                        activation.get("expected_verified_email_alias")
                        == result.identity.verified_email
                    )
                    if not (subject_matches or email_matches):
                        raise RemoteActivationIdentityMismatch(
                            "verified OIDC account is not approved for this server activation"
                        )
                    remote_activation = True
                challenge = self.enrollment._begin_in_transaction(
                    connection,
                    domain_id=current["domain_id"],
                    identity=result.identity,
                    harness_kind=current["harness_kind"],
                    harness_name=current["harness_name"],
                    public_key_pem=current["public_key_pem"],
                    now=now,
                )
                challenge_payload = {
                    "challenge_id": challenge.challenge_id,
                    "nonce": challenge.nonce,
                    "canonical_transaction_b64": base64.b64encode(
                        challenge.canonical_transaction
                    ).decode("ascii"),
                }
                if remote_activation:
                    challenge_payload["activation_mode"] = "remote_browser"
                challenge_encrypted = self.store.cipher.encrypt_json(
                    challenge_payload,
                    purpose=f"oidc-guided-challenge:{current['transaction_id']}",
                )
                continuation = connection.execute(
                    """UPDATE oidc_enrollment_continuations
                          SET status='callback_ready',challenge_encrypted=?,updated_at=?,
                              expires_at=?
                        WHERE transaction_id=? AND status='awaiting_oidc'""",
                    (
                        challenge_encrypted,
                        now,
                        challenge.expires_at,
                        current["transaction_id"],
                    ),
                )
                if continuation.rowcount != 1:
                    raise ReplayError("OIDC enrollment continuation is no longer current")
                updated = connection.execute(
                    """UPDATE oidc_enrollment_transactions
                       SET status='consumed',consumed_at=?,authorization_code_hash=?,id_token_hash=?,
                           enrollment_challenge_id=?
                       WHERE transaction_id=? AND status='exchanging'""",
                    (
                        now,
                        code_hash,
                        result.id_token_hash,
                        challenge.challenge_id,
                        current["transaction_id"],
                    ),
                )
                if updated.rowcount != 1:
                    raise ReplayError("OIDC authorization transaction was concurrently consumed")
                self.store.append_audit(
                    connection,
                    {
                        "action": "oidc.authorization.verified",
                        "challenge_id": challenge.challenge_id,
                        "domain_id": current["domain_id"],
                        "issuer": current["issuer"],
                        "transaction_id": current["transaction_id"],
                    },
                )
                return challenge
        except Exception as exc:
            if isinstance(exc, (sqlite3.IntegrityError, ReplayError)) or exc.__class__.__name__ == "UniqueViolation":
                raise ReplayError("OIDC authorization code or ID token was already consumed") from exc
            raise

    def _require_provider_binding(self, row: Any) -> None:
        expected = {
            "issuer": self.provider.config.issuer,
            "client_id": self.provider.config.client_id,
            "audience": self.provider.config.audience,
            "redirect_uri": self.provider.config.redirect_uri,
            "binding_assurance": self.enrollment.binding_assurance,
        }
        if any(row[field] != value for field, value in expected.items()):
            raise AuthenticationError("OIDC authorization provider binding mismatch")

    def _restore_remote_activation_pending(self, transaction_id: str, *, claimed_at: int) -> None:
        """Restore one wrong-account remote callback without consuming its state."""

        try:
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """UPDATE oidc_enrollment_transactions
                          SET status='pending',claimed_at=NULL
                        WHERE transaction_id=? AND status='exchanging' AND claimed_at=?""",
                    (transaction_id, claimed_at),
                )
                if updated.rowcount != 1:
                    return
                self.store.append_audit(
                    connection,
                    {
                        "action": "oidc.authorization.wrong_account_rejected",
                        "transaction_id": transaction_id,
                    },
                )
        except Exception:
            # Failure to restore leaves ``exchanging`` unusable and therefore
            # fails closed without staging or enrolling the wrong identity.
            return

    def _mark_failed(self, transaction_id: str, *, now: int) -> None:
        try:
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """UPDATE oidc_enrollment_transactions SET status='failed',consumed_at=?
                       WHERE transaction_id=? AND status='exchanging'""",
                    (now, transaction_id),
                )
                if updated.rowcount:
                    connection.execute(
                        """UPDATE oidc_enrollment_continuations
                              SET status='failed',updated_at=?
                            WHERE transaction_id=?
                              AND status IN ('awaiting_oidc','callback_ready','approval_pending')""",
                        (now, transaction_id),
                    )
                    self.store.append_audit(
                        connection,
                        {
                            "action": "oidc.authorization.failed",
                            "transaction_id": transaction_id,
                        },
                    )
        except Exception:
            # Preserve the original verification failure.  A transaction left in
            # ``exchanging`` remains unusable and therefore fails closed.
            return


def _load_json_object(value: bytes, *, label: str) -> dict[str, Any]:
    def reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, item in pairs:
            if key in result:
                raise ValueError("duplicate JSON member")
            result[key] = item
        return result

    try:
        decoded = json.loads(
            value.decode("utf-8"),
            object_pairs_hook=reject_duplicates,
            parse_constant=lambda _value: (_ for _ in ()).throw(ValueError("non-finite JSON number")),
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise AuthenticationError(f"{label} is not a valid JSON object") from exc
    if not isinstance(decoded, dict):
        raise AuthenticationError(f"{label} is not a valid JSON object")
    return decoded


def _mapping_string(value: Mapping[str, Any], field: str) -> str:
    result = value.get(field)
    if not isinstance(result, str) or not result or len(result) > 4_096:
        raise AuthenticationError(f"OIDC discovery {field} is invalid")
    return result


def _require_https_url(value: str, *, label: str, allow_query: bool) -> None:
    if not isinstance(value, str) or not value or len(value) > 4_096:
        raise ValueError(f"OIDC {label} must be an exact HTTPS URL")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise ValueError(f"OIDC {label} must be an exact HTTPS URL") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
        or (not allow_query and parsed.query)
        or (port is not None and not 1 <= port <= 65_535)
    ):
        raise ValueError(f"OIDC {label} must be an exact HTTPS URL")


def _require_discovered_https_url(value: str, *, label: str) -> None:
    try:
        _require_https_url(value, label=label, allow_query=False)
    except ValueError as exc:
        raise AuthenticationError(f"OIDC {label} is invalid") from exc


def _canonical_https_origin(value: str, *, require_origin: bool = False) -> str:
    try:
        _require_https_url(value, label="endpoint origin", allow_query=not require_origin)
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise ValueError("OIDC endpoint origin is invalid") from exc
    if require_origin and (parsed.path not in {"", "/"} or parsed.query):
        raise ValueError("OIDC endpoint origin must not contain a path or query")
    if parsed.hostname is None:
        raise ValueError("OIDC endpoint origin is invalid")
    hostname = parsed.hostname.lower()
    rendered_host = f"[{hostname}]" if ":" in hostname else hostname
    origin = f"https://{rendered_host}"
    if port not in {None, 443}:
        origin += f":{port}"
    if require_origin and value.rstrip("/") != origin:
        raise ValueError("OIDC endpoint origin must use canonical spelling")
    return origin


def _system_address_resolver(host: str, port: int) -> tuple[str, ...]:
    # This is host NSS/hosts/DNS policy, not a trust source. Its complete result
    # is validated once and handed to the direct-address transport unchanged.
    records = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    return tuple(sorted({str(record[4][0]) for record in records}))


def _b64url_decode(value: str) -> bytes:
    if not value or not _B64URL.fullmatch(value):
        raise AuthenticationError("OIDC base64url value is invalid")
    try:
        return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
    except (ValueError, base64.binascii.Error) as exc:
        raise AuthenticationError("OIDC base64url value is invalid") from exc


def _b64url_encode(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def _approval_possession_secret(
    continuation_token: str,
    *,
    transaction_id: str,
) -> str:
    """Purpose-separate Approval possession from Core continuation bearer use."""

    derived = HKDF(
        algorithm=hashes.SHA256(),
        length=32,
        salt=None,
        info=(
            "agentnet.approval.possession.v1:" + transaction_id
        ).encode("ascii"),
    ).derive(continuation_token.encode("ascii"))
    return _b64url_encode(derived)


def _b64url_uint(value: Any, *, field: str) -> int:
    if not isinstance(value, str):
        raise AuthenticationError(f"OIDC JWK {field} is invalid")
    decoded = _b64url_decode(value)
    if not decoded:
        raise AuthenticationError(f"OIDC JWK {field} is empty")
    return int.from_bytes(decoded, "big")


def _verify_jwt_signature(
    jwk: Mapping[str, Any],
    *,
    algorithm: str,
    signing_input: bytes,
    signature: bytes,
) -> None:
    try:
        if algorithm == "RS256":
            if jwk.get("kty") != "RSA":
                raise AuthenticationError("OIDC RSA signing key type mismatch")
            modulus = _b64url_uint(jwk.get("n"), field="modulus")
            exponent = _b64url_uint(jwk.get("e"), field="exponent")
            key = rsa.RSAPublicNumbers(exponent, modulus).public_key()
            if key.key_size < 2048:
                raise AuthenticationError("OIDC RSA signing key is too small")
            key.verify(signature, signing_input, padding.PKCS1v15(), hashes.SHA256())
        elif algorithm == "ES256":
            if jwk.get("kty") != "EC" or jwk.get("crv") != "P-256" or len(signature) != 64:
                raise AuthenticationError("OIDC EC signing key profile mismatch")
            x = _b64url_uint(jwk.get("x"), field="x")
            y = _b64url_uint(jwk.get("y"), field="y")
            key = ec.EllipticCurvePublicNumbers(x, y, ec.SECP256R1()).public_key()
            der_signature = encode_dss_signature(
                int.from_bytes(signature[:32], "big"),
                int.from_bytes(signature[32:], "big"),
            )
            key.verify(der_signature, signing_input, ec.ECDSA(hashes.SHA256()))
        else:  # pragma: no cover - configuration and header checks pin this.
            raise AuthenticationError("OIDC ID token signing algorithm is unsupported")
    except InvalidSignature as exc:
        raise AuthenticationError("OIDC ID token signature is invalid") from exc
    except (TypeError, ValueError) as exc:
        raise AuthenticationError("OIDC signing key is invalid") from exc


def _jwk_thumbprint(jwk: Mapping[str, Any]) -> str:
    if jwk.get("kty") == "RSA":
        fields = {"e": jwk.get("e"), "kty": "RSA", "n": jwk.get("n")}
    elif jwk.get("kty") == "EC":
        fields = {"crv": jwk.get("crv"), "kty": "EC", "x": jwk.get("x"), "y": jwk.get("y")}
    else:
        raise AuthenticationError("OIDC JWK type is unsupported")
    if any(not isinstance(value, str) or not value for value in fields.values()):
        raise AuthenticationError("OIDC JWK thumbprint fields are invalid")
    canonical = json.dumps(fields, separators=(",", ":"), sort_keys=True).encode("utf-8")
    return hashlib.sha256(canonical).hexdigest()


def _is_sha256(value: str) -> bool:
    return len(value) == 64 and all(character in "0123456789abcdef" for character in value)


__all__ = [
    "OIDCAuthorizationRequest",
    "OIDCEnrollmentCoordinator",
    "OIDCHTTPResponse",
    "OIDCHTTPTransport",
    "OIDCProvider",
    "OIDCProviderConfig",
    "OIDCVerificationResult",
    "RemoteActivationIdentityMismatch",
    "PinnedOIDCHTTPTransport",
    "UrllibOIDCHTTPTransport",
]
