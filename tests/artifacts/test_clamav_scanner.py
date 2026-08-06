from __future__ import annotations

import hashlib
import socket
import struct
from collections.abc import Buffer, Callable

import pytest

from agentnet.artifacts.clamav import (
    ClamAVScanError,
    ClamAVScanner,
    ScannerEndpoint,
    clamav_profile_digest,
    clamav_rules_digest,
)
from agentnet.artifacts.scanner import ScannerTrustPolicy
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import P256KeyPair, verify_signature


class ScriptedSocket:
    def __init__(self, *replies: bytes | BaseException) -> None:
        self.replies = list(replies)
        self.sent = bytearray()
        self.timeout: float | None = None
        self.closed = False

    def settimeout(self, timeout: float) -> None:
        self.timeout = timeout

    def sendall(self, value: Buffer) -> None:
        self.sent.extend(memoryview(value))

    def recv(self, size: int) -> bytes:
        if not self.replies:
            return b""
        value = self.replies.pop(0)
        if isinstance(value, BaseException):
            raise value
        if len(value) <= size:
            return value
        self.replies.insert(0, value[size:])
        return value[:size]

    def close(self) -> None:
        self.closed = True


class SocketFactory:
    def __init__(self, *replies: bytes | BaseException) -> None:
        self.socket = ScriptedSocket(*replies)
        self.calls: list[tuple[ScannerEndpoint, float]] = []

    def __call__(self, endpoint: ScannerEndpoint, timeout: float) -> ScriptedSocket:
        self.calls.append((endpoint, timeout))
        return self.socket


def _scanner(
    factory: Callable[[ScannerEndpoint, float], ScriptedSocket],
    *,
    key: P256KeyPair | None = None,
    signature_updated_at: int = 90,
    max_signature_age_seconds: int = 300,
    max_bytes: int = 16_777_216,
    max_response_bytes: int = 4096,
) -> ClamAVScanner:
    endpoint = ScannerEndpoint.from_uri("unix:///run/agentnet-clamav/clamd.sock")
    engine_version = "1.4.3"
    signature_version = "27890"
    profile_digest = clamav_profile_digest(
        endpoint=endpoint,
        engine_version=engine_version,
        timeout_seconds=2.5,
        max_bytes=max_bytes,
        max_response_bytes=max_response_bytes,
        max_signature_age_seconds=max_signature_age_seconds,
    )
    rules_digest = clamav_rules_digest(
        signature_version=signature_version,
        signature_updated_at=signature_updated_at,
    )
    return ClamAVScanner(
        endpoint,
        key or P256KeyPair.generate(),
        scanner_id="scanner:clamav",
        scanner_key_epoch=7,
        engine_version=engine_version,
        signature_version=signature_version,
        signature_updated_at=signature_updated_at,
        policy_revision=4,
        trust_policy=ScannerTrustPolicy(
            required_engine="clamav",
            required_rules_digest=rules_digest,
            required_profile_digest=profile_digest,
        ),
        timeout_seconds=2.5,
        max_bytes=max_bytes,
        max_response_bytes=max_response_bytes,
        max_signature_age_seconds=max_signature_age_seconds,
        connector=factory,
    )


def _scan(scanner: ClamAVScanner, content: bytes = b"hello"):
    digest = hashlib.sha256(content).hexdigest()
    return scanner.scan(
        artifact_id="artifact-00000001",
        classification="C1",
        ciphertext_digest="a" * 64,
        object_key="b" * 32,
        object_version="c" * 64,
        plaintext_digest=digest,
        policy_revision=4,
        content=content,
        issued_at=100,
        expires_at=160,
    )


def test_clean_result_uses_bounded_instream_framing_and_signed_attestation() -> None:
    factory = SocketFactory(b"stream: OK\x00")
    key = P256KeyPair.generate()
    scanner = _scanner(factory, key=key)
    content = b"x" * 65_537

    attestation = _scan(scanner, content)

    first = content[:65_536]
    second = content[65_536:]
    expected = (
        b"zINSTREAM\x00"
        + struct.pack("!I", len(first))
        + first
        + struct.pack("!I", len(second))
        + second
        + b"\x00\x00\x00\x00"
    )
    assert bytes(factory.socket.sent) == expected
    assert factory.socket.timeout == 2.5
    assert factory.socket.closed is True
    assert attestation.result == "allow"
    assert attestation.scanner_engine == "clamav"
    assert attestation.scanner_version == "1.4.3"
    assert attestation.rules_digest == clamav_rules_digest(
        signature_version="27890",
        signature_updated_at=90,
    )
    verify_signature(
        key.public_pem,
        "agentnet.artifact.attestation.v1",
        attestation.signed_fields(),
        attestation.signature,
    )


def test_infected_result_produces_a_signed_deny_attestation() -> None:
    factory = SocketFactory(b"stream: Win.Test.EICAR_HDB-1 FOUND\x00")
    key = P256KeyPair.generate()
    scanner = _scanner(factory, key=key)

    attestation = _scan(scanner)

    assert attestation.result == "deny"
    verify_signature(
        key.public_pem,
        "agentnet.artifact.attestation.v1",
        attestation.signed_fields(),
        attestation.signature,
    )


@pytest.mark.parametrize(
    "reply",
    [
        socket.timeout("timed out"),
        b"stream: OK",
        b"stream: UNKNOWN\x00",
        b"stream: Virus FOUND trailing\x00",
        b"stream:  FOUND\x00",
        b"stream: OK\x00trailing",
    ],
)
def test_timeout_malformed_and_unknown_results_are_indeterminate_failures(
    reply: bytes | BaseException,
) -> None:
    factory = SocketFactory(reply)
    scanner = _scanner(factory)

    with pytest.raises(ClamAVScanError):
        _scan(scanner)

    assert factory.socket.closed is True


def test_stale_signature_database_is_rejected_before_bytes_are_sent() -> None:
    factory = SocketFactory(b"stream: OK\x00")
    scanner = _scanner(factory, signature_updated_at=1, max_signature_age_seconds=30)

    with pytest.raises(ClamAVScanError, match="signature database is stale"):
        _scan(scanner)

    assert factory.calls == []


def test_oversize_content_is_denied_before_connecting() -> None:
    factory = SocketFactory(b"stream: OK\x00")
    scanner = _scanner(factory, max_bytes=4)

    with pytest.raises(ClamAVScanError, match="size boundary"):
        _scan(scanner, b"12345")

    assert factory.calls == []


def test_response_is_bounded_even_when_clamd_never_terminates_it() -> None:
    factory = SocketFactory(b"x" * 65)
    scanner = _scanner(factory, max_response_bytes=64)

    with pytest.raises(ClamAVScanError, match="response boundary"):
        _scan(scanner)


def test_only_unix_sockets_and_numeric_loopback_tcp_endpoints_are_accepted() -> None:
    assert ScannerEndpoint.from_uri("unix:///run/clamav/clamd.sock").unix_socket == "/run/clamav/clamd.sock"
    assert ScannerEndpoint.from_uri("tcp://127.0.0.1:3310").host == "127.0.0.1"
    assert ScannerEndpoint.from_uri("tcp://[::1]:3310").host == "::1"

    for endpoint in (
        "tcp://localhost:3310",
        "tcp://10.0.0.4:3310",
        "tcp://0.0.0.0:3310",
        "tcp://clamav:3310",
        "unix://relative.sock",
    ):
        with pytest.raises(ValueError):
            ScannerEndpoint.from_uri(endpoint)


def test_signature_substitution_of_any_artifact_binding_is_rejected() -> None:
    factory = SocketFactory(b"stream: OK\x00")
    key = P256KeyPair.generate()
    attestation = _scan(_scanner(factory, key=key))
    substituted = attestation.signed_fields()
    substituted["object_version"] = "d" * 64

    with pytest.raises(AuthenticationError, match="signature verification failed"):
        verify_signature(
            key.public_pem,
            "agentnet.artifact.attestation.v1",
            substituted,
            attestation.signature,
        )


def test_digest_substitution_is_rejected_before_connecting() -> None:
    factory = SocketFactory(b"stream: OK\x00")
    scanner = _scanner(factory)

    with pytest.raises(ValidationError, match="plaintext digest"):
        scanner.scan(
            artifact_id="artifact-00000001",
            classification="C1",
            ciphertext_digest="a" * 64,
            object_key="b" * 32,
            object_version="c" * 64,
            plaintext_digest="d" * 64,
            policy_revision=4,
            content=b"hello",
            issued_at=100,
            expires_at=160,
        )

    assert factory.calls == []
