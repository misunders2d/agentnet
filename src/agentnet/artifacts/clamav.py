"""Bounded ClamAV INSTREAM adapter and signed artifact attestations."""

from __future__ import annotations

import hashlib
import ipaddress
import math
import secrets
import socket
import struct
from collections.abc import Buffer, Callable
from dataclasses import dataclass
from typing import Literal, Protocol
from urllib.parse import urlsplit

from agentnet.artifacts.scanner import ArtifactScanAttestationV1, ScannerTrustPolicy
from agentnet.errors import ValidationError
from agentnet.security.signatures import P256KeyPair, canonical_digest


CLAMAV_INSTREAM_CHUNK_BYTES = 65_536
DEFAULT_MAX_ARTIFACT_BYTES = 16_777_216
DEFAULT_MAX_RESPONSE_BYTES = 4096
DEFAULT_MAX_SIGNATURE_AGE_SECONDS = 86_400
_ATTESTATION_PURPOSE = "agentnet.artifact.attestation.v1"
_FOUND_PREFIX = b"stream: "
_FOUND_SUFFIX = b" FOUND"


class ClamAVScanError(RuntimeError):
    """A fail-closed result for which no release-authoritative evidence exists."""


class _SocketLike(Protocol):
    def settimeout(self, timeout: float, /) -> None: ...

    def sendall(self, data: Buffer, /) -> None: ...

    def recv(self, size: int, /) -> bytes: ...

    def close(self) -> None: ...


@dataclass(frozen=True, slots=True)
class ScannerEndpoint:
    """One local ClamAV endpoint with no DNS or non-loopback TCP ambiguity."""

    unix_socket: str | None = None
    host: str | None = None
    port: int | None = None

    def __post_init__(self) -> None:
        has_unix = self.unix_socket is not None
        has_tcp = self.host is not None or self.port is not None
        if has_unix == has_tcp:
            raise ValueError("scanner endpoint must select exactly one local transport")
        if has_unix:
            path = self.unix_socket
            if (
                not isinstance(path, str)
                or not path.startswith("/")
                or not 1 < len(path) <= 256
                or "\x00" in path
            ):
                raise ValueError("scanner Unix socket path is invalid")
            return
        if not isinstance(self.host, str) or type(self.port) is not int:
            raise ValueError("scanner TCP endpoint is invalid")
        try:
            address = ipaddress.ip_address(self.host)
        except ValueError as exc:
            raise ValueError("scanner TCP host must be a numeric loopback address") from exc
        if not address.is_loopback or address.is_unspecified or not 1 <= self.port <= 65_535:
            raise ValueError("scanner TCP endpoint must use a bounded loopback address")
        object.__setattr__(self, "host", address.compressed)

    @classmethod
    def from_uri(cls, value: str) -> "ScannerEndpoint":
        if not isinstance(value, str) or not value or value != value.strip():
            raise ValueError("scanner endpoint URI is invalid")
        parsed = urlsplit(value)
        if parsed.scheme == "unix":
            if parsed.netloc or parsed.query or parsed.fragment or not parsed.path.startswith("/"):
                raise ValueError("scanner Unix endpoint URI is invalid")
            endpoint = cls(unix_socket=parsed.path)
            if value != endpoint.uri:
                raise ValueError("scanner Unix endpoint URI is not canonical")
            return endpoint
        if parsed.scheme == "tcp":
            if (
                parsed.username is not None
                or parsed.password is not None
                or parsed.path
                or parsed.query
                or parsed.fragment
                or parsed.hostname is None
            ):
                raise ValueError("scanner TCP endpoint URI is invalid")
            try:
                port = parsed.port
            except ValueError as exc:
                raise ValueError("scanner TCP endpoint port is invalid") from exc
            endpoint = cls(host=parsed.hostname, port=port)
            if value != endpoint.uri:
                raise ValueError("scanner TCP endpoint URI is not canonical")
            return endpoint
        raise ValueError("scanner endpoint must use unix or tcp")

    @property
    def uri(self) -> str:
        if self.unix_socket is not None:
            return f"unix://{self.unix_socket}"
        assert self.host is not None and self.port is not None
        rendered_host = f"[{self.host}]" if ":" in self.host else self.host
        return f"tcp://{rendered_host}:{self.port}"


def clamav_rules_digest(*, signature_version: str, signature_updated_at: int) -> str:
    """Commit the exact malware-signature database version and publication time."""

    _require_version(signature_version, label="signature")
    if type(signature_updated_at) is not int or signature_updated_at <= 0:
        raise ValueError("ClamAV signature update time is invalid")
    return canonical_digest(
        {
            "provider": "clamav",
            "signature_updated_at": signature_updated_at,
            "signature_version": signature_version,
        }
    )


def clamav_profile_digest(
    *,
    endpoint: ScannerEndpoint,
    engine_version: str,
    timeout_seconds: float,
    max_bytes: int = DEFAULT_MAX_ARTIFACT_BYTES,
    max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
    max_signature_age_seconds: int = DEFAULT_MAX_SIGNATURE_AGE_SECONDS,
) -> str:
    """Commit the maintained engine and every security-relevant protocol bound."""

    if not isinstance(endpoint, ScannerEndpoint):
        raise TypeError("scanner endpoint must be parsed before profile construction")
    _require_version(engine_version, label="engine")
    _require_transport_bounds(
        timeout_seconds=timeout_seconds,
        max_bytes=max_bytes,
        max_response_bytes=max_response_bytes,
    )
    _require_signature_age_bound(max_signature_age_seconds)
    return canonical_digest(
        {
            "chunk_bytes": CLAMAV_INSTREAM_CHUNK_BYTES,
            "endpoint": endpoint.uri,
            "engine_version": engine_version,
            "max_bytes": max_bytes,
            "max_response_bytes": max_response_bytes,
            "max_signature_age_seconds": max_signature_age_seconds,
            "protocol": "clamav.zINSTREAM.v1",
            "provider": "clamav",
            "timeout_seconds": float(timeout_seconds),
        }
    )


def _require_version(value: str, *, label: str) -> None:
    if (
        not isinstance(value, str)
        or not 1 <= len(value) <= 128
        or value != value.strip()
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in value)
    ):
        raise ValueError(f"ClamAV {label} version is invalid")


def _require_transport_bounds(*, timeout_seconds: float, max_bytes: int, max_response_bytes: int) -> None:
    if (
        isinstance(timeout_seconds, bool)
        or not isinstance(timeout_seconds, (int, float))
        or not math.isfinite(timeout_seconds)
        or not 0 < timeout_seconds <= 300
    ):
        raise ValueError("ClamAV timeout is outside the bounded profile")
    if type(max_bytes) is not int or not 1 <= max_bytes <= DEFAULT_MAX_ARTIFACT_BYTES:
        raise ValueError("ClamAV stream size is outside the bounded profile")
    if type(max_response_bytes) is not int or not 16 <= max_response_bytes <= 65_536:
        raise ValueError("ClamAV response size is outside the bounded profile")


def _require_signature_age_bound(value: int) -> None:
    if type(value) is not int or not 1 <= value <= 604_800:
        raise ValueError("ClamAV signature age is outside the bounded profile")


def _connect(endpoint: ScannerEndpoint, timeout_seconds: float) -> _SocketLike:
    if endpoint.unix_socket is not None:
        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.settimeout(timeout_seconds)
        try:
            connection.connect(endpoint.unix_socket)
        except Exception:
            connection.close()
            raise
        return connection
    assert endpoint.host is not None and endpoint.port is not None
    connection = socket.create_connection((endpoint.host, endpoint.port), timeout=timeout_seconds)
    connection.settimeout(timeout_seconds)
    return connection


class ClamAVScanner:
    """Issue P-256 evidence only for exact, current, bounded ClamAV results."""

    def __init__(
        self,
        endpoint: ScannerEndpoint,
        key: P256KeyPair,
        *,
        scanner_id: str,
        scanner_key_epoch: int,
        engine_version: str,
        signature_version: str,
        signature_updated_at: int,
        policy_revision: int,
        trust_policy: ScannerTrustPolicy,
        timeout_seconds: float = 30.0,
        max_bytes: int = DEFAULT_MAX_ARTIFACT_BYTES,
        max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
        max_signature_age_seconds: int = DEFAULT_MAX_SIGNATURE_AGE_SECONDS,
        connector: Callable[[ScannerEndpoint, float], _SocketLike] | None = None,
    ) -> None:
        if not isinstance(endpoint, ScannerEndpoint):
            raise TypeError("scanner endpoint must be a ScannerEndpoint")
        if not isinstance(key, P256KeyPair):
            raise TypeError("scanner key must be a P-256 key pair")
        if (
            not isinstance(scanner_id, str)
            or not 1 <= len(scanner_id) <= 256
            or scanner_id != scanner_id.strip()
            or any(ord(character) < 0x21 for character in scanner_id)
        ):
            raise ValueError("scanner identifier is invalid")
        if type(scanner_key_epoch) is not int or scanner_key_epoch < 1:
            raise ValueError("scanner key epoch is invalid")
        if type(policy_revision) is not int or policy_revision < 1:
            raise ValueError("scanner policy revision is invalid")
        if not isinstance(trust_policy, ScannerTrustPolicy):
            raise TypeError("scanner trust policy is invalid")
        _require_version(engine_version, label="engine")
        _require_version(signature_version, label="signature")
        _require_transport_bounds(
            timeout_seconds=timeout_seconds,
            max_bytes=max_bytes,
            max_response_bytes=max_response_bytes,
        )
        _require_signature_age_bound(max_signature_age_seconds)
        self.endpoint = endpoint
        self.key = key
        self.scanner_id = scanner_id
        self.scanner_key_epoch = scanner_key_epoch
        self.engine_version = engine_version
        self.signature_version = signature_version
        self.signature_updated_at = signature_updated_at
        self.policy_revision = policy_revision
        self.trust_policy = trust_policy
        self.timeout_seconds = float(timeout_seconds)
        self.max_bytes = max_bytes
        self.max_response_bytes = max_response_bytes
        self.max_signature_age_seconds = max_signature_age_seconds
        self.rules_digest = clamav_rules_digest(
            signature_version=signature_version,
            signature_updated_at=signature_updated_at,
        )
        self.profile_digest = clamav_profile_digest(
            endpoint=endpoint,
            engine_version=engine_version,
            timeout_seconds=timeout_seconds,
            max_bytes=max_bytes,
            max_response_bytes=max_response_bytes,
            max_signature_age_seconds=max_signature_age_seconds,
        )
        self._connector = connector or _connect
        self._require_current_profile()

    def _require_current_profile(self) -> None:
        if (self.scanner_id, self.scanner_key_epoch) in self.trust_policy.revoked_key_epochs:
            raise ValidationError("scanner key epoch is revoked")
        if self.trust_policy.required_engine not in {None, "clamav"}:
            raise ValidationError("scanner engine does not match current policy")
        if self.trust_policy.required_rules_digest not in {None, self.rules_digest}:
            raise ValidationError("scanner rules do not match current policy")
        if self.trust_policy.required_profile_digest not in {None, self.profile_digest}:
            raise ValidationError("scanner profile does not match current policy")

    def _require_fresh_signature_database(self, issued_at: int) -> None:
        if self.signature_updated_at > issued_at + self.trust_policy.allowed_future_skew_seconds:
            raise ClamAVScanError("ClamAV signature database time is in the future")
        if issued_at - self.signature_updated_at > self.max_signature_age_seconds:
            raise ClamAVScanError("ClamAV signature database is stale")

    def scan(
        self,
        *,
        artifact_id: str,
        classification: Literal["C0", "C1", "C2", "C3"],
        ciphertext_digest: str,
        object_key: str,
        object_version: str,
        plaintext_digest: str,
        policy_revision: int,
        content: bytes,
        issued_at: int,
        expires_at: int,
    ) -> ArtifactScanAttestationV1:
        if type(content) is not bytes:
            raise ValidationError("ClamAV content must be immutable bytes")
        if len(content) > self.max_bytes:
            raise ClamAVScanError("artifact exceeds the ClamAV stream size boundary")
        actual_digest = hashlib.sha256(content).hexdigest()
        if not isinstance(plaintext_digest, str) or not secrets.compare_digest(
            actual_digest, plaintext_digest
        ):
            raise ValidationError("artifact plaintext digest does not match scanner bytes")
        if policy_revision != self.policy_revision:
            raise ValidationError("scanner policy revision does not match the artifact decision")
        if (
            type(issued_at) is not int
            or type(expires_at) is not int
            or issued_at <= 0
            or expires_at <= issued_at
            or expires_at - issued_at > self.trust_policy.max_attestation_age_seconds
        ):
            raise ValidationError("scanner attestation time window is invalid")
        self._require_current_profile()
        self._require_fresh_signature_database(issued_at)
        common_fields: dict[str, object] = {
            "artifact_id": artifact_id,
            "classification": classification,
            "ciphertext_digest": ciphertext_digest,
            "expires_at": expires_at,
            "issued_at": issued_at,
            "object_key": object_key,
            "object_version": object_version,
            "plaintext_digest": plaintext_digest,
            "policy_revision": policy_revision,
            "profile_digest": self.profile_digest,
            "rules_digest": self.rules_digest,
            "scanner_engine": "clamav",
            "scanner_id": self.scanner_id,
            "scanner_key_epoch": self.scanner_key_epoch,
            "scanner_version": self.engine_version,
        }
        ArtifactScanAttestationV1.parse_boundary(
            common_fields | {"result": "indeterminate", "signature": "untrusted"}
        )
        result = self._scan_content(content)
        signed_fields = common_fields | {"result": result}
        signature = self.key.sign(_ATTESTATION_PURPOSE, signed_fields)
        return ArtifactScanAttestationV1.parse_boundary(signed_fields | {"signature": signature})

    def _scan_content(self, content: bytes) -> Literal["allow", "deny"]:
        connection: _SocketLike | None = None
        try:
            connection = self._connector(self.endpoint, self.timeout_seconds)
            connection.settimeout(self.timeout_seconds)
            connection.sendall(b"zINSTREAM\x00")
            view = memoryview(content)
            for offset in range(0, len(view), CLAMAV_INSTREAM_CHUNK_BYTES):
                chunk = view[offset : offset + CLAMAV_INSTREAM_CHUNK_BYTES]
                connection.sendall(struct.pack("!I", len(chunk)))
                connection.sendall(chunk)
            connection.sendall(b"\x00\x00\x00\x00")
            response = self._receive_response(connection)
        except (OSError, TimeoutError) as exc:
            raise ClamAVScanError("ClamAV scan transport failed") from exc
        finally:
            if connection is not None:
                try:
                    connection.close()
                except OSError:
                    pass
        if response == b"stream: OK":
            return "allow"
        if response.startswith(_FOUND_PREFIX) and response.endswith(_FOUND_SUFFIX):
            signature = response[len(_FOUND_PREFIX) : -len(_FOUND_SUFFIX)]
            if signature and len(signature) <= 256 and all(0x21 <= value <= 0x7E for value in signature):
                return "deny"
        raise ClamAVScanError("ClamAV returned an unknown or malformed result")

    def _receive_response(self, connection: _SocketLike) -> bytes:
        response = bytearray()
        while True:
            remaining = self.max_response_bytes + 1 - len(response)
            if remaining <= 0:
                raise ClamAVScanError("ClamAV response exceeds the response boundary")
            chunk = connection.recv(min(4096, remaining))
            if not chunk:
                raise ClamAVScanError("ClamAV response ended without a terminator")
            terminator = chunk.find(b"\x00")
            if terminator >= 0:
                response.extend(chunk[:terminator])
                if chunk[terminator + 1 :] or len(response) > self.max_response_bytes:
                    raise ClamAVScanError("ClamAV response exceeds the response boundary")
                return bytes(response)
            response.extend(chunk)
            if len(response) > self.max_response_bytes:
                raise ClamAVScanError("ClamAV response exceeds the response boundary")


__all__ = [
    "CLAMAV_INSTREAM_CHUNK_BYTES",
    "ClamAVScanError",
    "ClamAVScanner",
    "ScannerEndpoint",
    "clamav_profile_digest",
    "clamav_rules_digest",
]
