"""Purpose-bound Core→Approval request proofs with one-use replay metadata."""

from __future__ import annotations

import hashlib
import hmac
import json
import re
import secrets
import time
from dataclasses import dataclass
from typing import Any

from agentnet.errors import AuthenticationError
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_json


INTERNAL_BROKER_PROOF_HEADER = "X-AgentNet-Approval-Broker-Proof"
INTERNAL_BROKER_SCHEMA = "agentnet.approval.internal-broker-proof.v1"
INTERNAL_BROKER_ALGORITHM = "hmac-sha256-v1"
INTERNAL_BROKER_PURPOSE_CREATE = "agentnet.approval.internal-broker.create.v1"
INTERNAL_BROKER_PURPOSE_STATUS = "agentnet.approval.internal-broker.status.v1"
INTERNAL_BROKER_PURPOSE_RETRIEVE = "agentnet.approval.internal-broker.retrieve.v1"
INTERNAL_BROKER_PURPOSES = frozenset(
    {
        INTERNAL_BROKER_PURPOSE_CREATE,
        INTERNAL_BROKER_PURPOSE_STATUS,
        INTERNAL_BROKER_PURPOSE_RETRIEVE,
    }
)
INTERNAL_BROKER_TTL_SECONDS = 30
INTERNAL_BROKER_MAX_FUTURE_SKEW_SECONDS = 5
INTERNAL_BROKER_MAX_HEADER_BYTES = 4096

_B64URL = re.compile(r"^[A-Za-z0-9_-]+$")
_HEX_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_FIELDS = frozenset(
    {
        "alg",
        "audience",
        "body_sha256",
        "expires_at",
        "issued_at",
        "key_id",
        "method",
        "nonce",
        "path",
        "purpose",
        "schema",
        "signature",
    }
)
_KEY_DERIVATION_LABEL = b"AgentNet-Approval-Broker-Key\x00v1"
_KEY_ID_LABEL = b"AgentNet-Approval-Broker-Key-ID\x00v1"
_SIGNATURE_LABEL = b"AgentNet-Approval-Broker-Proof\x00v1"
_DENIAL = "approval request denied"


@dataclass(frozen=True, slots=True)
class VerifiedInternalBrokerProof:
    key_id: str
    nonce: str
    issued_at: int
    expires_at: int
    purpose: str
    audience: str
    method: str
    path: str
    body_sha256: str


def _deny(exc: Exception | None = None) -> AuthenticationError:
    error = AuthenticationError(_DENIAL)
    if exc is not None:
        error.__cause__ = exc
    return error


def _validate_credential(credential: str) -> bytes:
    if (
        not isinstance(credential, str)
        or not 43 <= len(credential) <= 512
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in credential)
    ):
        raise _deny()
    return credential.encode("ascii")


def _derive_key(credential: str) -> tuple[bytes, str]:
    root = _validate_credential(credential)
    key = hmac.new(root, _KEY_DERIVATION_LABEL, hashlib.sha256).digest()
    key_id = b64url_encode(hmac.new(key, _KEY_ID_LABEL, hashlib.sha256).digest()[:16])
    return key, key_id


def _strict_b64url(value: str, *, decoded_bytes: int | None = None) -> bytes:
    if (
        not isinstance(value, str)
        or not value
        or len(value) > INTERNAL_BROKER_MAX_HEADER_BYTES
        or _B64URL.fullmatch(value) is None
    ):
        raise _deny()
    try:
        decoded = b64url_decode(value)
    except Exception as exc:
        raise _deny(exc) from exc
    if b64url_encode(decoded) != value or (
        decoded_bytes is not None and len(decoded) != decoded_bytes
    ):
        raise _deny()
    return decoded


def _reject_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _reject_nonfinite(_value: str) -> object:
    raise ValueError("non-finite JSON number")


def _proof_preimage(unsigned: dict[str, Any]) -> bytes:
    body = canonical_json(unsigned)
    return _SIGNATURE_LABEL + len(body).to_bytes(8, "big") + body


def _validate_request_binding(
    *,
    audience: str,
    method: str,
    path: str,
    purpose: str,
    raw_body: bytes,
) -> None:
    if (
        not isinstance(audience, str)
        or not audience.startswith("https://")
        or audience.endswith("/")
        or not 8 <= len(audience) <= 512
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in audience)
        or method != "POST"
        or not isinstance(path, str)
        or not path.startswith("/v1/approval/internal/")
        or "?" in path
        or "#" in path
        or not 1 <= len(path) <= 512
        or purpose not in INTERNAL_BROKER_PURPOSES
        or not isinstance(raw_body, bytes)
    ):
        raise _deny()


def build_internal_broker_proof(
    *,
    credential: str,
    audience: str,
    method: str,
    path: str,
    purpose: str,
    raw_body: bytes,
    now: int | None = None,
    nonce: bytes | None = None,
) -> str:
    """Build one bounded proof over exact request bytes."""

    _validate_request_binding(
        audience=audience,
        method=method,
        path=path,
        purpose=purpose,
        raw_body=raw_body,
    )
    issued_at = int(time.time()) if now is None else now
    nonce_bytes = secrets.token_bytes(32) if nonce is None else nonce
    if (
        not isinstance(issued_at, int)
        or isinstance(issued_at, bool)
        or issued_at < 0
        or not isinstance(nonce_bytes, bytes)
        or len(nonce_bytes) != 32
    ):
        raise _deny()
    key, key_id = _derive_key(credential)
    unsigned: dict[str, Any] = {
        "alg": INTERNAL_BROKER_ALGORITHM,
        "audience": audience,
        "body_sha256": hashlib.sha256(raw_body).hexdigest(),
        "expires_at": issued_at + INTERNAL_BROKER_TTL_SECONDS,
        "issued_at": issued_at,
        "key_id": key_id,
        "method": method,
        "nonce": b64url_encode(nonce_bytes),
        "path": path,
        "purpose": purpose,
        "schema": INTERNAL_BROKER_SCHEMA,
    }
    signature = b64url_encode(hmac.new(key, _proof_preimage(unsigned), hashlib.sha256).digest())
    proof = canonical_json({**unsigned, "signature": signature})
    encoded = b64url_encode(proof)
    if len(encoded) > INTERNAL_BROKER_MAX_HEADER_BYTES:
        raise _deny()
    return encoded


def verify_internal_broker_proof(
    *,
    credential: str,
    header_value: str,
    audience: str,
    method: str,
    path: str,
    purpose: str,
    raw_body: bytes,
    now: int | None = None,
) -> VerifiedInternalBrokerProof:
    """Verify exact proof, but do not consume replay state."""

    try:
        _validate_request_binding(
            audience=audience,
            method=method,
            path=path,
            purpose=purpose,
            raw_body=raw_body,
        )
        checked_now = int(time.time()) if now is None else now
        if not isinstance(checked_now, int) or isinstance(checked_now, bool) or checked_now < 0:
            raise _deny()
        if not isinstance(header_value, str) or len(header_value) > INTERNAL_BROKER_MAX_HEADER_BYTES:
            raise _deny()
        encoded = _strict_b64url(header_value)
        value = json.loads(
            encoded.decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=_reject_nonfinite,
        )
        if not isinstance(value, dict) or set(value) != _FIELDS or canonical_json(value) != encoded:
            raise _deny()

        strings = (
            "alg",
            "audience",
            "body_sha256",
            "key_id",
            "method",
            "nonce",
            "path",
            "purpose",
            "schema",
            "signature",
        )
        if any(not isinstance(value[field], str) for field in strings):
            raise _deny()
        issued_at = value["issued_at"]
        expires_at = value["expires_at"]
        if (
            not isinstance(issued_at, int)
            or isinstance(issued_at, bool)
            or not isinstance(expires_at, int)
            or isinstance(expires_at, bool)
            or issued_at < 0
            or expires_at < 0
        ):
            raise _deny()

        nonce_value = str(value["nonce"])
        key_id_value = str(value["key_id"])
        signature_value = str(value["signature"])
        _strict_b64url(nonce_value, decoded_bytes=32)
        _strict_b64url(key_id_value, decoded_bytes=16)
        signature = _strict_b64url(signature_value, decoded_bytes=32)

        key, expected_key_id = _derive_key(credential)
        body_sha256 = hashlib.sha256(raw_body).hexdigest()
        if (
            value["schema"] != INTERNAL_BROKER_SCHEMA
            or value["alg"] != INTERNAL_BROKER_ALGORITHM
            or value["audience"] != audience
            or value["method"] != method
            or value["path"] != path
            or value["purpose"] != purpose
            or not isinstance(value["body_sha256"], str)
            or _HEX_SHA256.fullmatch(str(value["body_sha256"])) is None
            or not secrets.compare_digest(str(value["body_sha256"]), body_sha256)
            or not secrets.compare_digest(key_id_value, expected_key_id)
            or expires_at - issued_at <= 0
            or expires_at - issued_at > INTERNAL_BROKER_TTL_SECONDS
            or issued_at > checked_now + INTERNAL_BROKER_MAX_FUTURE_SKEW_SECONDS
            or checked_now >= expires_at
        ):
            raise _deny()

        unsigned = {key_name: value[key_name] for key_name in value if key_name != "signature"}
        expected_signature = hmac.new(
            key,
            _proof_preimage(unsigned),
            hashlib.sha256,
        ).digest()
        if not hmac.compare_digest(signature, expected_signature):
            raise _deny()

        return VerifiedInternalBrokerProof(
            key_id=key_id_value,
            nonce=nonce_value,
            issued_at=issued_at,
            expires_at=expires_at,
            purpose=str(value["purpose"]),
            audience=str(value["audience"]),
            method=str(value["method"]),
            path=str(value["path"]),
            body_sha256=str(value["body_sha256"]),
        )
    except AuthenticationError:
        raise
    except Exception as exc:
        raise _deny(exc) from exc


__all__ = [
    "INTERNAL_BROKER_ALGORITHM",
    "INTERNAL_BROKER_MAX_FUTURE_SKEW_SECONDS",
    "INTERNAL_BROKER_PROOF_HEADER",
    "INTERNAL_BROKER_PURPOSE_CREATE",
    "INTERNAL_BROKER_PURPOSE_RETRIEVE",
    "INTERNAL_BROKER_PURPOSE_STATUS",
    "INTERNAL_BROKER_SCHEMA",
    "INTERNAL_BROKER_TTL_SECONDS",
    "VerifiedInternalBrokerProof",
    "build_internal_broker_proof",
    "verify_internal_broker_proof",
]
