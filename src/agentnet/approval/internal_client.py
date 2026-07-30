"""Bounded Core client for the independently operated approval broker."""

from __future__ import annotations

import hashlib
import json
import os
import secrets
import ssl
from typing import Any

import httpx

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    INTERNAL_BROKER_PURPOSE_READINESS,
    INTERNAL_BROKER_PURPOSE_RETRIEVE,
    INTERNAL_BROKER_PURPOSE_STATUS,
    build_internal_broker_proof,
)
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.operations.config import ApprovalServiceClientConfig
from agentnet.security.signatures import b64url_encode, canonical_json


def _reject_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


_APPROVAL_STATES = frozenset({"pending", "issued", "rejected", "expired"})
_TLS_TRUST_ENVIRONMENT = ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE")


def require_approval_tls_environment() -> None:
    if any(name in os.environ for name in _TLS_TRUST_ENVIRONMENT):
        raise GateBlocked(
            "approval_broker_auth",
            "approval broker TLS trust configuration is unavailable",
        )


def _system_tls_context() -> ssl.SSLContext:
    require_approval_tls_environment()
    try:
        context = ssl.create_default_context()
        context.keylog_filename = None
        if context.minimum_version < ssl.TLSVersion.TLSv1_2:
            context.minimum_version = ssl.TLSVersion.TLSv1_2
    except (OSError, TypeError, ValueError):
        raise GateBlocked(
            "approval_broker_unavailable",
            "approval broker TLS trust is unavailable",
        ) from None
    if (
        not context.check_hostname
        or context.verify_mode != ssl.CERT_REQUIRED
        or context.keylog_filename is not None
    ):
        raise GateBlocked(
            "approval_broker_auth",
            "approval broker TLS trust configuration is unavailable",
        )
    return context


def _require_exact_keys(value: dict[str, Any], expected: frozenset[str]) -> None:
    if set(value) != expected:
        raise AuthenticationError("approval service response denied")


def _require_identifier(value: object) -> str:
    if not isinstance(value, str) or not 1 <= len(value) <= 256:
        raise AuthenticationError("approval service response denied")
    return value


def _require_positive_int(value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise AuthenticationError("approval service response denied")
    return value


def _strict_object(payload: bytes) -> dict[str, Any]:
    try:
        value = json.loads(
            payload.decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=lambda _value: (_ for _ in ()).throw(ValueError("non-finite")),
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise AuthenticationError("approval service response denied") from exc
    if not isinstance(value, dict):
        raise AuthenticationError("approval service response denied")
    return value


class ApprovalServiceClient:
    """Runtime-secret client; never exposes broker credential or capability URLs."""

    def __init__(
        self,
        config: ApprovalServiceClientConfig,
        credential: str,
        *,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        if (
            not 43 <= len(credential) <= 512
            or any(ord(character) < 0x21 or ord(character) > 0x7E for character in credential)
        ):
            raise GateBlocked("approval_service", "approval service credential is unavailable")
        self.config = config
        self._credential = credential
        self._audience = config.public_origin.rstrip("/")
        self._client = httpx.Client(
            base_url=self._audience,
            timeout=config.request_timeout_seconds,
            follow_redirects=False,
            trust_env=False,
            verify=_system_tls_context(),
            transport=transport,
        )

    def _post(
        self,
        path: str,
        body: dict[str, Any],
        *,
        purpose: str,
        expected: set[int],
    ) -> dict[str, Any]:
        raw_body = canonical_json(body)
        headers = {
            "Authorization": f"Bearer {self._credential}",
            INTERNAL_BROKER_PROOF_HEADER: build_internal_broker_proof(
                credential=self._credential,
                audience=self._audience,
                method="POST",
                path=path,
                purpose=purpose,
                raw_body=raw_body,
            ),
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Cache-Control": "no-store",
        }
        try:
            with self._client.stream(
                "POST",
                path,
                content=raw_body,
                headers=headers,
            ) as response:
                if response.status_code == 503:
                    raise GateBlocked("approval_service", "approval service is unavailable")
                if response.is_redirect or response.status_code not in expected:
                    raise AuthenticationError("approval service request denied")
                chunks: list[bytes] = []
                size = 0
                for chunk in response.iter_bytes():
                    size += len(chunk)
                    if size > self.config.maximum_response_bytes:
                        raise AuthenticationError("approval service response denied")
                    chunks.append(chunk)
        except (AuthenticationError, GateBlocked):
            raise
        except httpx.HTTPError:
            raise GateBlocked("approval_service", "approval service is unavailable") from None
        return _strict_object(b"".join(chunks))

    def readiness(self) -> dict[str, str]:
        """Prove the configured public broker path without business mutation."""

        path = "/v1/approval/internal/readiness"
        body = {"schema": "agentnet.approval.internal-readiness.v1"}
        raw_body = canonical_json(body)
        headers = {
            "Authorization": f"Bearer {self._credential}",
            INTERNAL_BROKER_PROOF_HEADER: build_internal_broker_proof(
                credential=self._credential,
                audience=self._audience,
                method="POST",
                path=path,
                purpose=INTERNAL_BROKER_PURPOSE_READINESS,
                raw_body=raw_body,
            ),
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Cache-Control": "no-store",
        }
        try:
            with self._client.stream("POST", path, content=raw_body, headers=headers) as response:
                status = response.status_code
                if status == 200:
                    chunks: list[bytes] = []
                    size = 0
                    for chunk in response.iter_bytes():
                        size += len(chunk)
                        if size > self.config.maximum_response_bytes:
                            raise GateBlocked(
                                "approval_broker_auth",
                                "Approval broker readiness response is invalid",
                            )
                        chunks.append(chunk)
                    try:
                        result = _strict_object(b"".join(chunks))
                    except AuthenticationError as exc:
                        raise GateBlocked(
                            "approval_broker_auth",
                            "Approval broker readiness response is invalid",
                        ) from exc
                    if result != {
                        "schema": "agentnet.approval.internal-readiness-result.v1",
                        "status": "ready",
                    }:
                        raise GateBlocked(
                            "approval_broker_auth",
                            "Approval broker readiness response is invalid",
                        )
                    return {"schema": str(result["schema"]), "status": str(result["status"])}
                if status in {408, 425, 429} or 500 <= status <= 599:
                    raise GateBlocked(
                        "approval_broker_unavailable",
                        "Approval broker readiness is unavailable",
                    )
                raise GateBlocked(
                    "approval_broker_auth",
                    "Approval broker readiness authentication failed",
                )
        except GateBlocked:
            raise
        except httpx.HTTPError:
            raise GateBlocked(
                "approval_broker_unavailable",
                "Approval broker readiness is unavailable",
            ) from None

    def create_request(
        self,
        *,
        idempotency_key: str,
        domain_id: str,
        approval_purpose: str,
        canonical_transaction: bytes,
        transaction_digest: str,
        possession_hash: str,
        request_expires_at: int,
    ) -> dict[str, Any]:
        result = self._post(
            "/v1/approval/internal/requests",
            {
                "schema": "agentnet.approval.internal-request-create.v2",
                "idempotency_key": idempotency_key,
                "approver_principal_id": self.config.approver_principal_id,
                "domain_id": domain_id,
                "approval_purpose": approval_purpose,
                "canonical_transaction_b64": b64url_encode(canonical_transaction),
                "transaction_digest": transaction_digest,
                "possession_hash": possession_hash,
                "request_expires_at": request_expires_at,
            },
            purpose=INTERNAL_BROKER_PURPOSE_CREATE,
            expected={200, 201},
        )
        _require_exact_keys(
            result,
            frozenset(
                {
                    "schema",
                    "request_id",
                    "state",
                    "approval_purpose",
                    "transaction_digest",
                    "expires_at",
                    "duplicate",
                }
            ),
        )
        if (
            result.get("schema") != "agentnet.approval.internal-request-created.v1"
            or result.get("state") not in _APPROVAL_STATES
            or result.get("approval_purpose") != approval_purpose
            or not isinstance(result.get("transaction_digest"), str)
            or not secrets.compare_digest(str(result["transaction_digest"]), transaction_digest)
            or _require_positive_int(result.get("expires_at")) != request_expires_at
            or type(result.get("duplicate")) is not bool
        ):
            raise AuthenticationError("approval service response denied")
        _require_identifier(result.get("request_id"))
        return result

    def request_status(self, *, request_id: str, transaction_digest: str) -> dict[str, Any]:
        result = self._post(
            "/v1/approval/internal/requests/status",
            {
                "schema": "agentnet.approval.internal-request-status.v1",
                "request_id": request_id,
                "transaction_digest": transaction_digest,
            },
            purpose=INTERNAL_BROKER_PURPOSE_STATUS,
            expected={200},
        )
        _require_exact_keys(
            result,
            frozenset({"schema", "request_id", "state", "transaction_digest", "expires_at"}),
        )
        if (
            result.get("schema") != "agentnet.approval.internal-request-status-result.v1"
            or result.get("request_id") != request_id
            or result.get("state") not in _APPROVAL_STATES
            or not isinstance(result.get("transaction_digest"), str)
            or not secrets.compare_digest(str(result["transaction_digest"]), transaction_digest)
        ):
            raise AuthenticationError("approval service response denied")
        _require_positive_int(result.get("expires_at"))
        return result

    def retrieve_receipt(
        self,
        *,
        request_id: str,
        possession_secret: str,
        domain_id: str,
        approval_purpose: str,
        transaction_digest: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        result = self._post(
            "/v1/approval/internal/receipts/retrieve",
            {
                "schema": "agentnet.approval.internal-receipt-retrieve.v2",
                "request_id": request_id,
                "possession_secret": possession_secret,
                "domain_id": domain_id,
                "approval_purpose": approval_purpose,
                "transaction_digest": transaction_digest,
                "idempotency_key": idempotency_key,
            },
            purpose=INTERNAL_BROKER_PURPOSE_RETRIEVE,
            expected={200},
        )
        _require_exact_keys(
            result,
            frozenset({"schema", "request_id", "receipt", "receipt_digest"}),
        )
        receipt = result.get("receipt")
        receipt_digest = result.get("receipt_digest")
        if (
            result.get("schema") != "agentnet.approval.internal-receipt-retrieve-result.v1"
            or result.get("request_id") != request_id
            or not isinstance(receipt, dict)
            or not isinstance(receipt_digest, str)
            or not secrets.compare_digest(
                receipt_digest,
                hashlib.sha256(canonical_json(receipt)).hexdigest(),
            )
        ):
            raise AuthenticationError("approval service response denied")
        return receipt

    def close(self) -> None:
        self._client.close()


__all__ = ["ApprovalServiceClient"]
