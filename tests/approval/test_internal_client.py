from __future__ import annotations

import hashlib
import json
import ssl
import threading
import traceback
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import httpx
import pytest
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_READINESS,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    verify_internal_broker_proof,
)
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.operations.config import ApprovalServiceClientConfig


@pytest.fixture(autouse=True)
def _clear_tls_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    for name in ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"):
        monkeypatch.delenv(name, raising=False)


def _config() -> ApprovalServiceClientConfig:
    return ApprovalServiceClientConfig(
        origin="https://approval.corp.example",
        public_origin="https://approval-public.corp.example",
        service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        approver_principal_id="security-owner",
        remote_activation_oidc_subject="approved-owner-subject",
    )


def test_internal_client_uses_explicit_system_tls_context_without_environment_trust(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    captured: dict[str, object] = {}
    real_client = httpx.Client

    def create_context(*_args: object, **_kwargs: object) -> ssl.SSLContext:
        return context

    def capture_client(*args: object, **kwargs: object) -> httpx.Client:
        captured.update(kwargs)
        return real_client(*args, **kwargs)

    monkeypatch.setattr(ssl, "create_default_context", create_context)
    monkeypatch.setattr(httpx, "Client", capture_client)
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(lambda _request: httpx.Response(200)),
    )
    try:
        assert captured["verify"] is context
        assert captured["trust_env"] is False
        assert captured["follow_redirects"] is False
        assert str(captured["base_url"]) == "https://approval-public.corp.example"
        assert context.check_hostname is True
        assert context.verify_mode == ssl.CERT_REQUIRED
        assert context.keylog_filename is None
    finally:
        client.close()


@pytest.mark.parametrize("variable", ["SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"])
def test_internal_client_rejects_ambient_tls_trust_override(
    monkeypatch: pytest.MonkeyPatch,
    variable: str,
) -> None:
    monkeypatch.setenv(variable, "/private/hostile-ca.pem")
    with pytest.raises(GateBlocked) as exc_info:
        ApprovalServiceClient(_config(), "S" * 43)
    assert exc_info.value.gate == "approval_broker_auth"
    assert "hostile-ca" not in str(exc_info.value)


def test_internal_client_sanitizes_system_tls_context_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("SSL_CERT_FILE", raising=False)
    monkeypatch.delenv("SSL_CERT_DIR", raising=False)

    def unavailable_context(*_args: object, **_kwargs: object) -> ssl.SSLContext:
        raise OSError("private trust-store detail")

    monkeypatch.setattr(ssl, "create_default_context", unavailable_context)
    with pytest.raises(GateBlocked) as exc_info:
        ApprovalServiceClient(_config(), "S" * 43)
    assert exc_info.value.gate == "approval_broker_unavailable"
    rendered = "".join(traceback.format_exception(exc_info.value))
    assert "private trust-store detail" not in rendered
    assert exc_info.value.__cause__ is None


class _ReadinessHandler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        payload = (
            b'{"schema":"agentnet.approval.internal-readiness-result.v1",'
            b'"status":"ready"}'
        )
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, _format: str, *args: object) -> None:
        del args


def _tls_material(tmp_path: Path, *, server_name: str) -> tuple[Path, Path, Path]:
    now = datetime.now(UTC)
    root_key = ec.generate_private_key(ec.SECP256R1())
    root_name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "AgentNet test root")])
    root = (
        x509.CertificateBuilder()
        .subject_name(root_name)
        .issuer_name(root_name)
        .public_key(root_key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
        .add_extension(
            x509.KeyUsage(
                digital_signature=False,
                content_commitment=False,
                key_encipherment=False,
                data_encipherment=False,
                key_agreement=False,
                key_cert_sign=True,
                crl_sign=True,
                encipher_only=None,
                decipher_only=None,
            ),
            critical=True,
        )
        .add_extension(
            x509.SubjectKeyIdentifier.from_public_key(root_key.public_key()),
            critical=False,
        )
        .add_extension(
            x509.AuthorityKeyIdentifier.from_issuer_public_key(root_key.public_key()),
            critical=False,
        )
        .sign(root_key, hashes.SHA256())
    )
    server_key = ec.generate_private_key(ec.SECP256R1())
    server = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, server_name)]))
        .issuer_name(root.subject)
        .public_key(server_key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(server_name)]), critical=False)
        .add_extension(
            x509.SubjectKeyIdentifier.from_public_key(server_key.public_key()),
            critical=False,
        )
        .add_extension(
            x509.AuthorityKeyIdentifier.from_issuer_public_key(root_key.public_key()),
            critical=False,
        )
        .add_extension(
            x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]),
            critical=False,
        )
        .sign(root_key, hashes.SHA256())
    )
    root_path = tmp_path / "root.pem"
    cert_path = tmp_path / "server.pem"
    key_path = tmp_path / "server-key.pem"
    root_path.write_bytes(root.public_bytes(serialization.Encoding.PEM))
    cert_path.write_bytes(server.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(
        server_key.private_bytes(
            serialization.Encoding.PEM,
            serialization.PrivateFormat.PKCS8,
            serialization.NoEncryption(),
        )
    )
    return root_path, cert_path, key_path


@pytest.mark.parametrize(
    ("server_name", "trust_root", "expected_gate"),
    [
        ("wrong-host.example", True, "approval_broker_unavailable"),
        ("localhost", False, "approval_broker_unavailable"),
        ("localhost", True, None),
    ],
)
def test_internal_client_enforces_trusted_matching_tls(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    server_name: str,
    trust_root: bool,
    expected_gate: str | None,
) -> None:
    root_path, cert_path, key_path = _tls_material(tmp_path, server_name=server_name)
    server = ThreadingHTTPServer(("127.0.0.1", 0), _ReadinessHandler)
    server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server_context.load_cert_chain(cert_path, key_path)
    server.socket = server_context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    client_context = (
        ssl.create_default_context(cafile=root_path)
        if trust_root
        else ssl.create_default_context()
    )
    monkeypatch.setattr(
        "agentnet.approval.internal_client._system_tls_context",
        lambda: client_context,
    )
    origin = f"https://localhost:{server.server_port}"
    config = ApprovalServiceClientConfig(
        origin=origin,
        public_origin=origin,
        service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
        approver_principal_id="security-owner",
        request_timeout_seconds=1.0,
    )
    client = ApprovalServiceClient(config, "S" * 43)
    try:
        if expected_gate is None:
            assert client.readiness() == {
                "schema": "agentnet.approval.internal-readiness-result.v1",
                "status": "ready",
            }
        else:
            with pytest.raises(GateBlocked) as exc_info:
                client.readiness()
            assert exc_info.value.gate == expected_gate
            assert server_name not in str(exc_info.value)
    finally:
        client.close()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        assert not thread.is_alive()


def test_internal_client_sanitizes_transport_failure() -> None:
    def handler(_request: httpx.Request) -> httpx.Response:
        raise httpx.TransportError("private endpoint and certificate detail")

    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(handler),
    )
    try:
        with pytest.raises(GateBlocked) as exc_info:
            client.readiness()
        assert exc_info.value.gate == "approval_broker_unavailable"
        rendered = "".join(traceback.format_exception(exc_info.value))
        assert "private endpoint and certificate detail" not in rendered
        assert exc_info.value.__cause__ is None
    finally:
        client.close()


def test_internal_client_binds_runtime_secret_and_exact_bounded_routes() -> None:
    secret = "S" * 43
    observed: list[tuple[str, dict[str, object]]] = []
    proofs: list[str] = []
    purposes = {
        "/v1/approval/internal/requests": INTERNAL_BROKER_PURPOSE_CREATE,
        "/v1/approval/internal/requests/status": INTERNAL_BROKER_PURPOSE_STATUS,
        "/v1/approval/internal/receipts/retrieve": INTERNAL_BROKER_PURPOSE_RETRIEVE,
    }

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.headers["authorization"] == f"Bearer {secret}"
        assert request.url.host == "approval-public.corp.example"
        proof = request.headers[INTERNAL_BROKER_PROOF_HEADER]
        verified = verify_internal_broker_proof(
            credential=secret,
            header_value=proof,
            audience="https://approval-public.corp.example",
            method="POST",
            path=request.url.path,
            purpose=purposes[request.url.path],
            raw_body=request.content,
        )
        assert verified.path == request.url.path
        proofs.append(proof)
        body = json.loads(request.content)
        observed.append((request.url.path, body))
        transaction_digest = hashlib.sha256(b"{}").hexdigest()
        if request.url.path.endswith("/requests/status"):
            return httpx.Response(
                200,
                json={
                    "schema": "agentnet.approval.internal-request-status-result.v1",
                    "request_id": "request-1",
                    "state": "issued",
                    "transaction_digest": transaction_digest,
                    "expires_at": 1_800_000_300,
                },
            )
        if request.url.path.endswith("/receipts/retrieve"):
            receipt = {
                "schema": "agentnet.independent-approval.receipt.v1",
                "opaque": "signed",
            }
            return httpx.Response(
                200,
                json={
                    "schema": "agentnet.approval.internal-receipt-retrieve-result.v1",
                    "request_id": "request-1",
                    "receipt": receipt,
                    "receipt_digest": hashlib.sha256(
                        json.dumps(
                            receipt, sort_keys=True, separators=(",", ":")
                        ).encode("utf-8")
                    ).hexdigest(),
                },
            )
        return httpx.Response(
            201,
            json={
                "schema": "agentnet.approval.internal-request-created.v1",
                "request_id": "request-1",
                "state": "pending",
                "approval_purpose": "identity.enrollment.approve",
                "transaction_digest": transaction_digest,
                "expires_at": 1_800_000_300,
                "duplicate": False,
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        transaction_digest = hashlib.sha256(b"{}").hexdigest()
        possession_secret = "P" * 43
        possession_hash = hashlib.sha256(possession_secret.encode("ascii")).hexdigest()
        created = client.create_request(
            idempotency_key="core:enrollment:test-1",
            domain_id="corp.example",
            approval_purpose="identity.enrollment.approve",
            canonical_transaction=b"{}",
            transaction_digest=transaction_digest,
            possession_hash=possession_hash,
            request_expires_at=1_800_000_300,
        )
        assert created["request_id"] == "request-1"
        assert client.request_status(
            request_id="request-1",
            transaction_digest=transaction_digest,
        )["state"] == "issued"
        receipt = client.retrieve_receipt(
            request_id="request-1",
            possession_secret=possession_secret,
            domain_id="corp.example",
            approval_purpose="identity.enrollment.approve",
            transaction_digest=transaction_digest,
            idempotency_key="core:enrollment-complete:test-1",
        )
        assert receipt["opaque"] == "signed"
    finally:
        client.close()

    rendered = json.dumps(observed)
    assert secret not in rendered
    assert "approval_url" not in rendered
    assert [path for path, _body in observed] == [
        "/v1/approval/internal/requests",
        "/v1/approval/internal/requests/status",
        "/v1/approval/internal/receipts/retrieve",
    ]
    assert len(set(proofs)) == 3


def test_internal_client_readiness_uses_public_topology_and_exact_proof() -> None:
    secret = "S" * 43

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.host == "approval-public.corp.example"
        assert request.content == b'{"schema":"agentnet.approval.internal-readiness.v1"}'
        verify_internal_broker_proof(
            credential=secret,
            header_value=request.headers[INTERNAL_BROKER_PROOF_HEADER],
            audience="https://approval-public.corp.example",
            method="POST",
            path="/v1/approval/internal/readiness",
            purpose=INTERNAL_BROKER_PURPOSE_READINESS,
            raw_body=request.content,
        )
        return httpx.Response(
            200,
            json={
                "schema": "agentnet.approval.internal-readiness-result.v1",
                "status": "ready",
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        assert client.readiness() == {
            "schema": "agentnet.approval.internal-readiness-result.v1",
            "status": "ready",
        }
    finally:
        client.close()


@pytest.mark.parametrize(
    ("response", "gate"),
    [
        (httpx.Response(199), "approval_broker_auth"),
        (httpx.Response(201, json={}), "approval_broker_auth"),
        (httpx.Response(307), "approval_broker_auth"),
        (httpx.Response(401), "approval_broker_auth"),
        (httpx.Response(408), "approval_broker_unavailable"),
        (httpx.Response(425), "approval_broker_unavailable"),
        (httpx.Response(429), "approval_broker_unavailable"),
        (httpx.Response(503), "approval_broker_unavailable"),
    ],
)
def test_internal_client_readiness_has_total_sanitized_classification(
    response: httpx.Response,
    gate: str,
) -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(lambda _request: response),
    )
    try:
        with pytest.raises(GateBlocked) as exc_info:
            client.readiness()
        assert exc_info.value.gate == gate
    finally:
        client.close()


def test_internal_client_uses_fresh_proof_for_same_business_retry() -> None:
    secret = "S" * 43
    proofs: list[str] = []
    bodies: list[bytes] = []

    def handler(request: httpx.Request) -> httpx.Response:
        proofs.append(request.headers[INTERNAL_BROKER_PROOF_HEADER])
        bodies.append(request.content)
        return httpx.Response(
            200,
            json={
                "schema": "agentnet.approval.internal-request-created.v1",
                "request_id": "request-1",
                "state": "pending",
                "approval_purpose": "identity.enrollment.approve",
                "transaction_digest": hashlib.sha256(b"{}").hexdigest(),
                "expires_at": 1_800_000_300,
                "duplicate": True,
            },
        )

    client = ApprovalServiceClient(
        _config(),
        secret,
        transport=httpx.MockTransport(handler),
    )
    try:
        for _attempt in range(2):
            client.create_request(
                idempotency_key="core:enrollment:same-business-request",
                domain_id="corp.example",
                approval_purpose="identity.enrollment.approve",
                canonical_transaction=b"{}",
                transaction_digest=hashlib.sha256(b"{}").hexdigest(),
                possession_hash=hashlib.sha256(b"waiting-process-secret").hexdigest(),
                request_expires_at=1_800_000_300,
            )
    finally:
        client.close()

    assert bodies[0] == bodies[1]
    assert proofs[0] != proofs[1]


def test_internal_client_rejects_redirect_and_duplicate_json() -> None:
    for response in (
        httpx.Response(307, headers={"location": "https://attacker.example"}),
        httpx.Response(200, content=b'{"state":"issued","state":"pending"}'),
    ):
        client = ApprovalServiceClient(
            _config(),
            "S" * 43,
            transport=httpx.MockTransport(lambda _request, result=response: result),
        )
        try:
            with pytest.raises(AuthenticationError, match="approval service"):
                client.request_status(request_id="request-1", transaction_digest="a" * 64)
        finally:
            client.close()


@pytest.mark.parametrize(
    "response",
    [
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "request-1",
            "state": "unknown",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
        },
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "different-request",
            "state": "issued",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
        },
        {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": "request-1",
            "state": "issued",
            "transaction_digest": "a" * 64,
            "expires_at": 1_800_000_300,
            "private_detail": "must-be-rejected",
        },
    ],
)
def test_internal_client_rejects_non_exact_status_response(response) -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(lambda _request: httpx.Response(200, json=response)),
    )
    try:
        with pytest.raises(AuthenticationError, match="approval service response"):
            client.request_status(request_id="request-1", transaction_digest="a" * 64)
    finally:
        client.close()


def test_internal_client_maps_service_unavailable_to_retryable_gate() -> None:
    client = ApprovalServiceClient(
        _config(),
        "S" * 43,
        transport=httpx.MockTransport(
            lambda _request: httpx.Response(503, json={"error": "request_unavailable"})
        ),
    )
    try:
        with pytest.raises(GateBlocked, match="approval service is unavailable"):
            client.request_status(request_id="request-1", transaction_digest="a" * 64)
    finally:
        client.close()


def test_internal_client_rejects_short_runtime_secret() -> None:
    with pytest.raises(Exception, match="credential is unavailable"):
        ApprovalServiceClient(_config(), "short")
