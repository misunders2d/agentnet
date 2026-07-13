"""Production OIDC authorization-code/PKCE verification for enrollment.

The adapter owns discovery, token exchange, JWT/JWKS verification, and durable
single-use state.  Callers never submit identity claims to the enrollment API.
"""

from __future__ import annotations

import base64
import hashlib
import ipaddress
import json
import re
import secrets
import socket
import sqlite3
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any, Protocol
from urllib.parse import urlsplit
from uuid import uuid4

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec, padding, rsa
from cryptography.hazmat.primitives.asymmetric.utils import encode_dss_signature

from agentnet.errors import (
    AuthenticationError,
    ExtensionError,
    GateBlocked,
    ReplayError,
)
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.enrollment import EnrollmentChallenge, EnrollmentService, VerifiedOIDCIdentity
from agentnet.operations.config import RuntimeProfile


_B64URL = re.compile(r"^[A-Za-z0-9_-]+$")
_OIDC_ALGORITHMS = frozenset({"RS256", "ES256"})


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
        headers: Mapping[str, str],
        body: bytes | None,
        timeout_seconds: float,
    ) -> OIDCHTTPResponse: ...


class UrllibOIDCHTTPTransport:
    """Real TLS HTTP transport with bounded response bodies."""

    def __init__(self, *, maximum_response_bytes: int = 1_048_576, opener: Any | None = None) -> None:
        if maximum_response_bytes < 1_024 or maximum_response_bytes > 8_388_608:
            raise ValueError("OIDC response limit is outside the supported range")
        self.maximum_response_bytes = maximum_response_bytes
        self._opener = opener or urllib.request.build_opener(_NoOIDCRedirectHandler())

    def request(
        self,
        *,
        method: str,
        url: str,
        headers: Mapping[str, str],
        body: bytes | None,
        timeout_seconds: float,
    ) -> OIDCHTTPResponse:
        request = urllib.request.Request(url, data=body, headers=dict(headers), method=method)
        try:
            with self._opener.open(request, timeout=timeout_seconds) as response:  # noqa: S310 - URLs are validated/pinned.
                payload = response.read(self.maximum_response_bytes + 1)
                if len(payload) > self.maximum_response_bytes:
                    raise GateBlocked("oidc_provider", "OIDC provider response exceeds the configured bound")
                return OIDCHTTPResponse(
                    status=int(response.status),
                    headers={key.casefold(): value for key, value in response.headers.items()},
                    body=payload,
                )
        except urllib.error.HTTPError as exc:
            if 300 <= int(exc.code) < 400:
                raise GateBlocked("oidc_provider", "OIDC provider redirects are forbidden") from exc
            payload = exc.read(self.maximum_response_bytes + 1)
            return OIDCHTTPResponse(
                status=int(exc.code),
                headers={key.casefold(): value for key, value in exc.headers.items()},
                body=payload[: self.maximum_response_bytes],
            )
        except (OSError, urllib.error.URLError) as exc:
            raise GateBlocked("oidc_provider", "OIDC provider is unavailable") from exc


class _NoOIDCRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Return redirect responses to the caller; never follow a new target."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        return None


@dataclass(frozen=True, slots=True)
class OIDCProviderConfig:
    issuer: str
    client_id: str
    redirect_uri: str
    audience: str | None = None
    client_secret: str | None = None
    allowed_signing_algorithms: tuple[str, ...] = ("RS256",)
    pinned_jwk_thumbprints: tuple[tuple[str, str], ...] = ()
    allowed_endpoint_origins: tuple[str, ...] = ()
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
        if self.client_secret is not None and (not self.client_secret or len(self.client_secret) > 4_096):
            raise ValueError("OIDC client secret is invalid")
        if self.authorization_ttl_seconds < 60 or self.authorization_ttl_seconds > 600:
            raise ValueError("OIDC authorization lifetime must be between 60 and 600 seconds")
        if self.maximum_id_token_age_seconds < 30 or self.maximum_id_token_age_seconds > 900:
            raise ValueError("OIDC ID-token age bound must be between 30 and 900 seconds")
        if self.allowed_clock_skew_seconds < 0 or self.allowed_clock_skew_seconds > 120:
            raise ValueError("OIDC clock skew is outside the supported range")
        if self.http_timeout_seconds <= 0 or self.http_timeout_seconds > 30:
            raise ValueError("OIDC HTTP timeout is outside the supported range")
        pins = dict(self.pinned_jwk_thumbprints)
        if len(pins) != len(self.pinned_jwk_thumbprints) or any(
            not key_id or not _is_sha256(value) for key_id, value in pins.items()
        ):
            raise ValueError("OIDC JWK thumbprint pins are invalid")
        origins = self.allowed_endpoint_origins or (_canonical_https_origin(self.issuer),)
        canonical_origins = tuple(_canonical_https_origin(value, require_origin=True) for value in origins)
        if len(set(canonical_origins)) != len(canonical_origins):
            raise ValueError("OIDC endpoint origins must be unique")
        object.__setattr__(self, "allowed_endpoint_origins", canonical_origins)


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
        self.transport = transport or UrllibOIDCHTTPTransport()
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
            self._require_pinned_public_endpoint(value, label=label)
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
        if self.config.client_secret is not None:
            client = urllib.parse.quote_plus(self.config.client_id)
            secret = urllib.parse.quote_plus(self.config.client_secret)
            credentials = base64.b64encode(f"{client}:{secret}".encode("utf-8")).decode("ascii")
            headers["authorization"] = f"Basic {credentials}"
            fields.pop("client_id")
        self._require_pinned_public_endpoint(
            discovery.token_endpoint,
            label="token endpoint",
        )
        response = self.transport.request(
            method="POST",
            url=discovery.token_endpoint,
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
        self._require_pinned_public_endpoint(jwks_uri, label="JWKS endpoint")
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
        return OIDCVerificationResult(
            identity=identity,
            id_token_hash=hashlib.sha256(id_token.encode("ascii")).hexdigest(),
            expires_at=expires_at,
        )

    def _get_json(self, url: str, *, label: str) -> dict[str, Any]:
        self._require_pinned_public_endpoint(url, label=label)
        response = self.transport.request(
            method="GET",
            url=url,
            headers={"accept": "application/json"},
            body=None,
            timeout_seconds=self.config.http_timeout_seconds,
        )
        if response.status != 200:
            raise GateBlocked("oidc_provider", f"{label} is unavailable")
        if len(response.body) > 1_048_576:
            raise GateBlocked("oidc_provider", f"{label} exceeds the supported response bound")
        return _load_json_object(response.body, label=label)

    def _require_pinned_public_endpoint(self, value: str, *, label: str) -> None:
        _require_discovered_https_url(value, label=label)
        origin = _canonical_https_origin(value)
        if origin not in self.config.allowed_endpoint_origins:
            raise AuthenticationError(f"OIDC {label} origin is not pinned")
        parsed = urlsplit(value)
        assert parsed.hostname is not None  # established by URL validation above
        try:
            addresses = self.resolver(parsed.hostname, parsed.port or 443)
        except Exception as exc:
            raise GateBlocked("oidc_provider", f"OIDC {label} address resolution failed") from exc
        if not addresses:
            raise GateBlocked("oidc_provider", f"OIDC {label} address resolution was empty")
        try:
            parsed_addresses = tuple(ipaddress.ip_address(address) for address in addresses)
        except ValueError as exc:
            raise GateBlocked("oidc_provider", f"OIDC {label} resolved to an invalid address") from exc
        if any(not address.is_global for address in parsed_addresses):
            raise GateBlocked("oidc_provider", f"OIDC {label} resolved to a non-public address")


class OIDCEnrollmentCoordinator:
    """Compose verified OIDC identity with enrollment challenge creation."""

    def __init__(self, store: Any, provider: OIDCProvider, enrollment: EnrollmentService) -> None:
        if enrollment.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            raise GateBlocked("oidc_enrollment", "production OIDC enrollment requires the server-agent profile")
        if enrollment.binding_assurance == "lab":
            raise GateBlocked("oidc_enrollment", "production OIDC enrollment refuses lab identity binding")
        if store is not enrollment.store:
            raise ValueError("OIDC coordinator and enrollment service must share one transaction store")
        self.store = store
        self.provider = provider
        self.enrollment = enrollment

    def begin_authorization(
        self,
        *,
        domain_id: str,
        harness_kind: str,
        harness_name: str,
        public_key_pem: str,
    ) -> OIDCAuthorizationRequest:
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
        return OIDCAuthorizationRequest(transaction_id, authorization_url, state, expires_at)

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
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._code_replay_actor, code_hash, replay_expires_at),
                )
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (self._token_replay_actor, result.id_token_hash, replay_expires_at),
                )
                challenge = self.enrollment._begin_in_transaction(
                    connection,
                    domain_id=current["domain_id"],
                    identity=result.identity,
                    harness_kind=current["harness_kind"],
                    harness_name=current["harness_name"],
                    public_key_pem=current["public_key_pem"],
                    now=now,
                )
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

    def _mark_failed(self, transaction_id: str, *, now: int) -> None:
        try:
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """UPDATE oidc_enrollment_transactions SET status='failed',consumed_at=?
                       WHERE transaction_id=? AND status='exchanging'""",
                    (now, transaction_id),
                )
                if updated.rowcount:
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
    assert parsed.hostname is not None
    hostname = parsed.hostname.lower()
    rendered_host = f"[{hostname}]" if ":" in hostname else hostname
    origin = f"https://{rendered_host}"
    if port not in {None, 443}:
        origin += f":{port}"
    if require_origin and value.rstrip("/") != origin:
        raise ValueError("OIDC endpoint origin must use canonical spelling")
    return origin


def _system_address_resolver(host: str, port: int) -> tuple[str, ...]:
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
    "UrllibOIDCHTTPTransport",
]
