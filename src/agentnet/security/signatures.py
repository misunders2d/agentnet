"""Purpose-bound P-256 signatures over deterministic typed JSON.

Cryptographic primitives come from ``cryptography``.  This module owns only
domain separation, exact canonicalization, and schema restrictions.
"""

from __future__ import annotations

import base64
import hashlib
import json
from collections.abc import Mapping
from typing import Any

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec

from agentnet.errors import AuthenticationError, ValidationError


ALLOWED_PURPOSES = frozenset(
    {
        "agentnet.local.request.v1",
        "agentnet.enrollment.pop.v1",
        "agentnet.internal-invitation.acceptance-pop.v1",
        "agentnet.credential.rotation.pop.v1",
        "agentnet.managed-server-credential-reauthorization.pop.v1",
        "agentnet.recovery.pop.v1",
        "agentnet.approval.receipt.v1",
        "agentnet.event.origin.v1",
        "agentnet.receipt.v1",
        "agentnet.artifact.attestation.v1",
        "agentnet.federation.assertion.v1",
        "agentnet.federation.revocation.v1",
        "agentnet.mls.adoption.v1",
        "agentnet.room.control.v1",
        "agentnet.room.recovery.v1",
        "agentnet.audit.checkpoint.v1",
        "agentnet.backup.manifest-seal.v1",
        "agentnet.update.manifest.v1",
        "agentnet.presence.lease.v1",
        "agentnet.component.adoption.v1",
        "agentnet.component.bakeoff.review.v1",
        "agentnet.component.reviewer.root.v1",
        "agentnet.server-relay.packet.v1",
        "agentnet.server-relay.receipt.v1",
        "agentnet.server-relay.key-rotation.v1",
        "agentnet.server-relay.key-revocation.v1",
        "agentnet.profile.offer.v1",
        "agentnet.authority.command.v1",
        "agentnet.workload.registration.pop.v1",
        "agentnet.workload.renewal.pop.v1",
        "agentnet.workload.transition.v1",
        "agentnet.effect.transition.v1",
    }
)


def b64url_encode(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def b64url_decode(value: str) -> bytes:
    if not value or any(character.isspace() for character in value):
        raise ValidationError("invalid base64url encoding")
    padding = "=" * (-len(value) % 4)
    try:
        return base64.urlsafe_b64decode(value + padding)
    except Exception as exc:  # pragma: no cover - exact backend error is irrelevant
        raise ValidationError("invalid base64url encoding") from exc


def canonical_json(value: Mapping[str, Any]) -> bytes:
    try:
        rendered = json.dumps(
            value,
            ensure_ascii=False,
            allow_nan=False,
            sort_keys=True,
            separators=(",", ":"),
        )
    except (TypeError, ValueError) as exc:
        raise ValidationError("object is not canonical-JSON compatible") from exc
    return rendered.encode("utf-8")


def canonical_digest(value: Mapping[str, Any]) -> str:
    return hashlib.sha256(canonical_json(value)).hexdigest()


def purpose_preimage(purpose: str, value: Mapping[str, Any]) -> bytes:
    if purpose not in ALLOWED_PURPOSES:
        raise ValidationError("unknown signing purpose")
    body = canonical_json(value)
    return b"AgentNet-SIGNATURE\x00" + purpose.encode("ascii") + b"\x00" + len(body).to_bytes(8, "big") + body


class P256KeyPair:
    """P-256 key wrapper used by the conformance profile.

    Production private-key custody is provided by the selected platform/KMS
    adapter; exporting a PEM is intentionally a local-test convenience.
    """

    def __init__(self, private_key: ec.EllipticCurvePrivateKey) -> None:
        self._private_key = private_key

    @classmethod
    def generate(cls) -> "P256KeyPair":
        return cls(ec.generate_private_key(ec.SECP256R1()))

    @classmethod
    def from_private_pem(cls, pem: bytes, password: bytes | None = None) -> "P256KeyPair":
        key = serialization.load_pem_private_key(pem, password=password)
        if not isinstance(key, ec.EllipticCurvePrivateKey) or not isinstance(key.curve, ec.SECP256R1):
            raise ValidationError("only P-256 private keys are accepted")
        return cls(key)

    @property
    def public_pem(self) -> str:
        return self._private_key.public_key().public_bytes(
            serialization.Encoding.PEM,
            serialization.PublicFormat.SubjectPublicKeyInfo,
        ).decode("ascii")

    @property
    def private_pem(self) -> bytes:
        return self._private_key.private_bytes(
            serialization.Encoding.PEM,
            serialization.PrivateFormat.PKCS8,
            serialization.NoEncryption(),
        )

    @property
    def thumbprint(self) -> str:
        der = self._private_key.public_key().public_bytes(
            serialization.Encoding.DER,
            serialization.PublicFormat.SubjectPublicKeyInfo,
        )
        return b64url_encode(hashlib.sha256(der).digest())

    def sign(self, purpose: str, value: Mapping[str, Any]) -> str:
        signature = self._private_key.sign(purpose_preimage(purpose, value), ec.ECDSA(hashes.SHA256()))
        return b64url_encode(signature)


def load_public_key(pem: str) -> ec.EllipticCurvePublicKey:
    try:
        key = serialization.load_pem_public_key(pem.encode("ascii"))
    except (ValueError, TypeError) as exc:
        raise ValidationError("invalid public key") from exc
    if not isinstance(key, ec.EllipticCurvePublicKey) or not isinstance(key.curve, ec.SECP256R1):
        raise ValidationError("only P-256 public keys are accepted")
    return key


def verify_signature(public_key_pem: str, purpose: str, value: Mapping[str, Any], signature: str) -> None:
    key = load_public_key(public_key_pem)
    try:
        key.verify(b64url_decode(signature), purpose_preimage(purpose, value), ec.ECDSA(hashes.SHA256()))
    except InvalidSignature as exc:
        raise AuthenticationError("signature verification failed") from exc
