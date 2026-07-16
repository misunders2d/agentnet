"""Bounded Core client for the independently operated approval broker."""

from __future__ import annotations

import json
from typing import Any

import httpx

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
        self._client = httpx.Client(
            base_url=config.origin.rstrip("/"),
            timeout=config.request_timeout_seconds,
            follow_redirects=False,
            trust_env=False,
            transport=transport,
        )

    def _post(self, path: str, body: dict[str, Any], *, expected: set[int]) -> dict[str, Any]:
        headers = {
            "Authorization": f"Bearer {self._credential}",
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Cache-Control": "no-store",
        }
        try:
            with self._client.stream(
                "POST",
                path,
                content=canonical_json(body),
                headers=headers,
            ) as response:
                if response.is_redirect or response.status_code not in expected:
                    raise AuthenticationError("approval service request denied")
                chunks: list[bytes] = []
                size = 0
                for chunk in response.iter_bytes():
                    size += len(chunk)
                    if size > self.config.maximum_response_bytes:
                        raise AuthenticationError("approval service response denied")
                    chunks.append(chunk)
        except AuthenticationError:
            raise
        except httpx.HTTPError as exc:
            raise GateBlocked("approval_service", "approval service is unavailable") from exc
        return _strict_object(b"".join(chunks))

    def create_request(
        self,
        *,
        idempotency_key: str,
        domain_id: str,
        approval_purpose: str,
        canonical_transaction: bytes,
        transaction_digest: str,
    ) -> dict[str, Any]:
        return self._post(
            "/v1/approval/internal/requests",
            {
                "schema": "agentnet.approval.internal-request-create.v1",
                "idempotency_key": idempotency_key,
                "approver_principal_id": self.config.approver_principal_id,
                "domain_id": domain_id,
                "approval_purpose": approval_purpose,
                "canonical_transaction_b64": b64url_encode(canonical_transaction),
                "transaction_digest": transaction_digest,
            },
            expected={200, 201},
        )

    def request_status(self, *, request_id: str, transaction_digest: str) -> dict[str, Any]:
        return self._post(
            "/v1/approval/internal/requests/status",
            {
                "schema": "agentnet.approval.internal-request-status.v1",
                "request_id": request_id,
                "transaction_digest": transaction_digest,
            },
            expected={200},
        )

    def retrieve_receipt(
        self,
        *,
        request_id: str,
        claim_code: str,
        domain_id: str,
        approval_purpose: str,
        transaction_digest: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        result = self._post(
            "/v1/approval/internal/receipts/retrieve",
            {
                "schema": "agentnet.approval.internal-receipt-retrieve.v1",
                "request_id": request_id,
                "claim_code": claim_code,
                "domain_id": domain_id,
                "approval_purpose": approval_purpose,
                "transaction_digest": transaction_digest,
                "idempotency_key": idempotency_key,
            },
            expected={200},
        )
        receipt = result.get("receipt")
        if not isinstance(receipt, dict):
            raise AuthenticationError("approval service response denied")
        return receipt

    def close(self) -> None:
        self._client.close()


__all__ = ["ApprovalServiceClient"]
