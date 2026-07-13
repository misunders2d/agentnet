"""Small signed client for the owned internal HTTP API."""

from __future__ import annotations

from typing import Any
from urllib.parse import urlsplit

import httpx

from agentnet.errors import ValidationError
from agentnet.security.dpop import (
    RequestProof,
    canonical_request_target,
    canonical_service_audience,
    create_request_proof,
)
from agentnet.security.signatures import P256KeyPair, canonical_json


def proof_headers(proof: RequestProof) -> dict[str, str]:
    return {
        "X-AgentNet-Harness": proof.harness_id,
        "X-AgentNet-Credential": proof.credential_id,
        "X-AgentNet-Key": proof.key_id,
        "X-AgentNet-Domain": proof.domain_id,
        "X-AgentNet-Audience": proof.audience,
        "X-AgentNet-Method": proof.method,
        "X-AgentNet-Scheme": proof.scheme,
        "X-AgentNet-Authority": proof.authority,
        "X-AgentNet-Path": proof.path,
        "X-AgentNet-Query": proof.query,
        "X-AgentNet-Body-Digest": proof.body_digest,
        "X-AgentNet-Timestamp": str(proof.timestamp),
        "X-AgentNet-Nonce": proof.nonce,
        "X-AgentNet-Signature": proof.signature,
    }


class AgentNetClient:
    def __init__(
        self,
        *,
        base_url: str,
        key: P256KeyPair,
        domain_id: str,
        harness_id: str,
        credential_id: str,
        audience: str,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.key = key
        self.domain_id = domain_id
        self.harness_id = harness_id
        self.credential_id = credential_id
        self.audience = canonical_service_audience(audience)
        origin = urlsplit(self.base_url)
        if origin.path or origin.query or origin.fragment or origin.username is not None or origin.password is not None:
            raise ValidationError("AgentNet client base_url must be a canonical origin")
        try:
            port = origin.port
        except ValueError as exc:
            raise ValidationError("AgentNet client base_url has an invalid port") from exc
        host = origin.hostname or ""
        rendered_host = f"[{host}]" if ":" in host else host
        default_port = 80 if origin.scheme == "http" else 443
        authority = rendered_host if port in {None, default_port} else f"{rendered_host}:{port}"
        target = canonical_request_target(scheme=origin.scheme, authority=authority, path="/", query="")
        if self.base_url != f"{target.scheme}://{target.authority}":
            raise ValidationError("AgentNet client base_url must use its canonical origin spelling")
        self._scheme = target.scheme
        self._authority = target.authority
        self._client = httpx.Client(base_url=self.base_url, transport=transport, timeout=10)

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        if timeout_seconds is not None and not 0.25 <= timeout_seconds <= 65:
            raise ValidationError("AgentNet client request timeout is outside the bounded profile")
        body = canonical_json(json_body) if json_body is not None else b""
        relative = urlsplit(path)
        if relative.scheme or relative.netloc or relative.fragment:
            raise ValidationError("AgentNet client request target must be relative and fragment-free")
        target = canonical_request_target(
            scheme=self._scheme,
            authority=self._authority,
            path=relative.path,
            query=relative.query,
        )
        proof = create_request_proof(
            self.key,
            harness_id=self.harness_id,
            credential_id=self.credential_id,
            domain_id=self.domain_id,
            audience=self.audience,
            method=method,
            scheme=target.scheme,
            authority=target.authority,
            path=target.path,
            query=target.query,
            body=body,
        )
        return self._client.request(
            method,
            path,
            content=body,
            headers={"Content-Type": "application/json", **proof_headers(proof)},
            timeout=self._client.timeout if timeout_seconds is None else timeout_seconds,
        )

    def close(self) -> None:
        self._client.close()
