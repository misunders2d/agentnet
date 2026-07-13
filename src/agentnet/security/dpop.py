"""Sender-constrained local/API proof profile.

This is not a replacement for RFC 9449 at an OAuth boundary.  It is the exact
local/internal request proof consumed by the supervisor and core; OAuth DPoP is
provided by the selected issuer adapter.
"""

from __future__ import annotations

import hashlib
import re
import secrets
import time
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any
from urllib.parse import unquote_to_bytes, urlsplit

from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import P256KeyPair, verify_signature


@dataclass(frozen=True, slots=True)
class RequestProof:
    harness_id: str
    credential_id: str
    key_id: str
    domain_id: str
    audience: str
    method: str
    scheme: str
    authority: str
    path: str
    query: str
    body_digest: str
    timestamp: int
    nonce: str
    signature: str

    def signed_fields(self) -> dict[str, Any]:
        return {
            "body_digest": self.body_digest,
            "credential_id": self.credential_id,
            "domain_id": self.domain_id,
            "harness_id": self.harness_id,
            "key_id": self.key_id,
            "audience": self.audience,
            "method": self.method.upper(),
            "nonce": self.nonce,
            "authority": self.authority,
            "path": self.path,
            "query": self.query,
            "scheme": self.scheme,
            "timestamp": self.timestamp,
        }

    @property
    def proof_id(self) -> str:
        return hashlib.sha256((self.signature + self.nonce).encode("ascii")).hexdigest()


def create_request_proof(
    key: P256KeyPair,
    *,
    harness_id: str,
    credential_id: str,
    domain_id: str,
    audience: str,
    method: str,
    scheme: str,
    authority: str,
    path: str,
    query: str,
    body: bytes,
    timestamp: int | None = None,
    nonce: str | None = None,
) -> RequestProof:
    canonical = canonical_request_target(scheme=scheme, authority=authority, path=path, query=query)
    canonical_audience = canonical_service_audience(audience)
    fields = {
        "audience": canonical_audience,
        "authority": canonical.authority,
        "body_digest": hashlib.sha256(body).hexdigest(),
        "credential_id": credential_id,
        "domain_id": domain_id,
        "harness_id": harness_id,
        "key_id": key.thumbprint,
        "method": method.upper(),
        "nonce": nonce or secrets.token_urlsafe(24),
        "path": canonical.path,
        "query": canonical.query,
        "scheme": canonical.scheme,
        "timestamp": timestamp if timestamp is not None else int(time.time()),
    }
    return RequestProof(**fields, signature=key.sign("agentnet.local.request.v1", fields))


def verify_request_proof(
    proof: RequestProof,
    *,
    public_key_pem: str,
    expected_method: str,
    expected_audience: str,
    expected_scheme: str,
    expected_authority: str,
    expected_path: str,
    expected_query: str,
    body: bytes,
    now: int,
    max_age: int,
    future_skew: int,
) -> None:
    expected = canonical_request_target(
        scheme=expected_scheme,
        authority=expected_authority,
        path=expected_path,
        query=expected_query,
    )
    if not secrets.compare_digest(proof.audience, canonical_service_audience(expected_audience)):
        raise AuthenticationError("request proof audience mismatch")
    presented = canonical_request_target(
        scheme=proof.scheme,
        authority=proof.authority,
        path=proof.path,
        query=proof.query,
    )
    if proof.method != expected_method.upper() or presented != expected:
        raise AuthenticationError("request proof target mismatch")
    expected_digest = hashlib.sha256(body).hexdigest()
    if not secrets.compare_digest(proof.body_digest, expected_digest):
        raise AuthenticationError("request body digest mismatch")
    if proof.timestamp > now + future_skew or proof.timestamp < now - max_age:
        raise AuthenticationError("request proof outside freshness window")
    if len(proof.nonce) < 24 or len(proof.nonce) > 256:
        raise ValidationError("nonce length outside profile")
    verify_signature(public_key_pem, "agentnet.local.request.v1", proof.signed_fields(), proof.signature)


def proof_from_headers(headers: Mapping[str, str]) -> RequestProof:
    normalized = {key.lower(): value for key, value in headers.items()}
    required = {
        "x-agentnet-harness": "harness_id",
        "x-agentnet-credential": "credential_id",
        "x-agentnet-key": "key_id",
        "x-agentnet-domain": "domain_id",
        "x-agentnet-audience": "audience",
        "x-agentnet-method": "method",
        "x-agentnet-scheme": "scheme",
        "x-agentnet-authority": "authority",
        "x-agentnet-path": "path",
        "x-agentnet-query": "query",
        "x-agentnet-body-digest": "body_digest",
        "x-agentnet-timestamp": "timestamp",
        "x-agentnet-nonce": "nonce",
        "x-agentnet-signature": "signature",
    }
    missing = [header for header in required if header not in normalized]
    if missing:
        raise AuthenticationError("request proof is incomplete")
    values: dict[str, Any] = {field: normalized[header] for header, field in required.items()}
    try:
        values["timestamp"] = int(values["timestamp"])
    except ValueError as exc:
        raise AuthenticationError("invalid proof timestamp") from exc
    return RequestProof(**values)


@dataclass(frozen=True, slots=True)
class CanonicalRequestTarget:
    scheme: str
    authority: str
    path: str
    query: str


_PERCENT_ESCAPE = re.compile(r"%[0-9A-F]{2}")
_ANY_PERCENT_ESCAPE = re.compile(r"%[0-9A-Fa-f]{2}")


def canonical_service_audience(value: str) -> str:
    """Validate an opaque configured audience without normalizing it."""

    if not isinstance(value, str) or not (3 <= len(value) <= 512):
        raise ValidationError("service audience length outside profile")
    if value != value.strip() or any(ord(character) < 0x21 for character in value):
        raise ValidationError("service audience is not canonical")
    return value


def canonical_request_target(*, scheme: str, authority: str, path: str, query: str) -> CanonicalRequestTarget:
    """Return the sole accepted HTTP target spelling.

    Ambiguous encodings are rejected rather than silently rewritten.  The
    caller must pass raw ASGI path/query bytes decoded as ASCII so the client
    and service sign the same wire target.
    """

    if not all(isinstance(item, str) for item in (scheme, authority, path, query)):
        raise ValidationError("request target components must be strings")
    canonical_scheme = scheme.lower()
    if scheme != canonical_scheme or canonical_scheme not in {"http", "https"}:
        raise ValidationError("request target scheme is not canonical")
    if not authority or authority != authority.strip() or any(ord(character) > 127 for character in authority):
        raise ValidationError("request target authority is not canonical")
    try:
        parsed = urlsplit(f"{canonical_scheme}://{authority}")
        port = parsed.port
    except ValueError as exc:
        raise ValidationError("request target authority is invalid") from exc
    if parsed.username is not None or parsed.password is not None or not parsed.hostname:
        raise ValidationError("request target authority is invalid")
    host = parsed.hostname.lower()
    if parsed.hostname != host:
        raise ValidationError("request target host must be lowercase")
    if ":" in host:
        host = f"[{host}]"
    default_port = 80 if canonical_scheme == "http" else 443
    canonical_authority = host if port in {None, default_port} else f"{host}:{port}"
    if authority != canonical_authority:
        raise ValidationError("request target authority is not canonical")
    if not path.startswith("/") or "?" in path or "#" in path or "\\" in path:
        raise ValidationError("request target path is not canonical")
    if "#" in query or query.startswith("?"):
        raise ValidationError("request target query is not canonical")
    for component in (path, query):
        if any(ord(character) < 0x20 or ord(character) > 0x7E for character in component):
            raise ValidationError("request target contains non-ASCII or control bytes")
        escapes = _ANY_PERCENT_ESCAPE.findall(component)
        if escapes != _PERCENT_ESCAPE.findall(component):
            raise ValidationError("request target percent escapes must use uppercase hex")
        if "%" in _ANY_PERCENT_ESCAPE.sub("", component):
            raise ValidationError("request target contains an invalid percent escape")
    segments = path.split("/")
    for segment in segments:
        decoded = unquote_to_bytes(segment)
        if decoded in {b".", b".."}:
            raise ValidationError("request target contains a dot segment")
        if b"/" in decoded or b"\\" in decoded:
            raise ValidationError("request target contains an encoded path separator")
    return CanonicalRequestTarget(canonical_scheme, canonical_authority, path, query)
