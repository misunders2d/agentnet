"""Small signed client for the owned internal HTTP API."""

from __future__ import annotations

from typing import Any
from urllib.parse import quote, urlsplit

import httpx

from agentnet.errors import ValidationError
from agentnet.security.dpop import (
    RequestProof,
    canonical_request_target,
    canonical_service_audience,
    create_request_proof,
)
from agentnet.security.signatures import P256KeyPair, canonical_json


MAX_ARTIFACT_BYTES = 16_777_216
_ROUTE_IDENTIFIER_CHARS = frozenset(
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-"
)


def _route_identifier(value: str, *, label: str) -> str:
    if (
        not isinstance(value, str)
        or not 1 <= len(value) <= 256
        or value != value.strip()
        or any(character not in _ROUTE_IDENTIFIER_CHARS for character in value)
    ):
        raise ValidationError(f"{label} is invalid")
    return quote(value, safe="")


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

    def _request_body(
        self,
        method: str,
        path: str,
        *,
        body: bytes,
        content_type: str,
        timeout_seconds: float | None = None,
        stream: bool = False,
    ) -> httpx.Response:
        if timeout_seconds is not None and not 0.25 <= timeout_seconds <= 65:
            raise ValidationError("AgentNet client request timeout is outside the bounded profile")
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
        request = self._client.build_request(
            method,
            path,
            content=body,
            headers={"Content-Type": content_type, **proof_headers(proof)},
            timeout=self._client.timeout if timeout_seconds is None else timeout_seconds,
        )
        return self._client.send(request, stream=stream)

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        body = canonical_json(json_body) if json_body is not None else b""
        return self._request_body(
            method,
            path,
            body=body,
            content_type="application/json",
            timeout_seconds=timeout_seconds,
        )

    def renew_current_credential(self, *, request_id: str) -> httpx.Response:
        return self.request(
            "POST",
            "/v1/credentials/current/renew",
            json_body={
                "schema": "agentnet.credential-renewal.v1",
                "request_id": request_id,
            },
        )

    def c0_pilot_readiness(self) -> httpx.Response:
        return self.request(
            "POST", "/v1/c0-pilot/readiness",
            json_body={"schema": "agentnet.c0-pilot.readiness.v1"},
        )

    def c0_pilot_start(self) -> httpx.Response:
        return self.request(
            "POST", "/v1/c0-pilot/start",
            json_body={"schema": "agentnet.c0-pilot.start.v1"},
        )

    def c0_pilot_respond(self) -> httpx.Response:
        return self.request(
            "POST", "/v1/c0-pilot/respond",
            json_body={"schema": "agentnet.c0-pilot.respond.v1"},
        )

    def c0_pilot_complete(self) -> httpx.Response:
        return self.request(
            "POST", "/v1/c0-pilot/complete",
            json_body={"schema": "agentnet.c0-pilot.complete.v1"},
        )

    def c0_pilot_status(self) -> httpx.Response:
        return self.request(
            "POST", "/v1/c0-pilot/status",
            json_body={"schema": "agentnet.c0-pilot.status.v1"},
        )

    def request_bytes(
        self,
        method: str,
        path: str,
        *,
        content: bytes,
        content_type: str = "application/octet-stream",
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        if not isinstance(content, bytes):
            raise ValidationError("AgentNet binary request content must be bytes")
        if len(content) > MAX_ARTIFACT_BYTES:
            raise ValidationError("AgentNet binary request exceeds the artifact size limit")
        if content_type != "application/octet-stream":
            raise ValidationError("AgentNet binary requests require application/octet-stream")
        return self._request_body(
            method,
            path,
            body=content,
            content_type=content_type,
            timeout_seconds=timeout_seconds,
        )

    def reserve_artifact(
        self,
        *,
        idempotency_key: str,
        expected_digest: str,
        expected_size: int,
        media_type: str,
        classification: str = "C1",
        required_attachment: bool = True,
        ttl_seconds: int = 3600,
    ) -> httpx.Response:
        return self.request(
            "POST",
            "/v1/artifacts/reservations",
            json_body={
                "idempotency_key": idempotency_key,
                "expected_digest": expected_digest,
                "expected_size": expected_size,
                "media_type": media_type,
                "classification": classification,
                "required_attachment": required_attachment,
                "ttl_seconds": ttl_seconds,
            },
        )

    def upload_artifact_bytes(
        self,
        *,
        reservation_id: str,
        content: bytes,
    ) -> httpx.Response:
        encoded_reservation_id = _route_identifier(
            reservation_id,
            label="artifact reservation_id",
        )
        return self.request_bytes(
            "POST",
            f"/v1/artifacts/reservations/{encoded_reservation_id}/bytes",
            content=content,
        )

    def promote_artifact(
        self,
        *,
        reservation_id: str,
        object_version: str,
        provenance: dict[str, Any],
        derivation: dict[str, Any] | None = None,
    ) -> httpx.Response:
        encoded_reservation_id = _route_identifier(
            reservation_id,
            label="artifact reservation_id",
        )
        body: dict[str, Any] = {
            "object_version": object_version,
            "provenance": provenance,
        }
        if derivation is not None:
            body["derivation"] = derivation
        return self.request(
            "POST",
            f"/v1/artifacts/reservations/{encoded_reservation_id}/promote",
            json_body=body,
        )

    def abort_artifact_reservation(self, *, reservation_id: str) -> httpx.Response:
        encoded_reservation_id = _route_identifier(
            reservation_id,
            label="artifact reservation_id",
        )
        return self.request(
            "POST",
            f"/v1/artifacts/reservations/{encoded_reservation_id}/abort",
        )

    def artifact_lifecycle(self, *, artifact_id: str) -> httpx.Response:
        encoded_artifact_id = _route_identifier(artifact_id, label="artifact_id")
        return self.request(
            "GET",
            f"/v1/artifacts/{encoded_artifact_id}/lifecycle",
        )

    def issue_artifact_download_capability(
        self,
        *,
        artifact_id: str,
        ttl_seconds: int = 60,
    ) -> httpx.Response:
        encoded_artifact_id = _route_identifier(artifact_id, label="artifact_id")
        return self.request(
            "POST",
            f"/v1/artifacts/{encoded_artifact_id}/download-capabilities",
            json_body={"ttl_seconds": ttl_seconds},
        )

    def consume_artifact_download(
        self,
        *,
        capability: str,
        stream: bool = False,
    ) -> httpx.Response:
        if (
            not isinstance(capability, str)
            or not 32 <= len(capability) <= 512
            or capability != capability.strip()
        ):
            raise ValidationError("artifact download capability is invalid")
        body = canonical_json({"token": capability})
        return self._request_body(
            "POST",
            "/v1/artifacts/download",
            body=body,
            content_type="application/json",
            stream=stream,
        )

    def download_artifact(
        self,
        *,
        artifact_id: str,
        ttl_seconds: int = 60,
    ) -> httpx.Response:
        issued = self.issue_artifact_download_capability(
            artifact_id=artifact_id,
            ttl_seconds=ttl_seconds,
        )
        if issued.status_code != 200:
            return issued
        try:
            value = issued.json()
        except ValueError as exc:
            raise ValidationError(
                "artifact download capability response is invalid"
            ) from exc
        if (
            not isinstance(value, dict)
            or set(value) != {"artifact_id", "capability"}
            or value["artifact_id"] != artifact_id
            or not isinstance(value["capability"], str)
        ):
            raise ValidationError("artifact download capability response is invalid")
        consumed = self.consume_artifact_download(
            capability=value["capability"],
            stream=True,
        )
        content = bytearray()
        try:
            content_length = consumed.headers.get("content-length")
            if content_length is not None:
                try:
                    declared_length = int(content_length)
                except ValueError as exc:
                    raise ValidationError(
                        "artifact download content length is invalid"
                    ) from exc
                if declared_length < 0 or declared_length > MAX_ARTIFACT_BYTES:
                    raise ValidationError("artifact download exceeds the artifact size limit")
            for chunk in consumed.iter_bytes(chunk_size=65_536):
                content.extend(chunk)
                if len(content) > MAX_ARTIFACT_BYTES:
                    raise ValidationError("artifact download exceeds the artifact size limit")
        finally:
            consumed.close()
        return httpx.Response(
            consumed.status_code,
            headers=consumed.headers,
            content=bytes(content),
            extensions=consumed.extensions,
        )

    def acknowledge_mailbox(
        self,
        *,
        event_id: str,
        envelope_digest: str,
    ) -> httpx.Response:
        if (
            not 1 <= len(event_id) <= 256
            or event_id != event_id.strip()
            or any(
                character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-"
                for character in event_id
            )
        ):
            raise ValidationError("mailbox acknowledgement event_id is invalid")
        if len(envelope_digest) != 64 or any(
            character not in "0123456789abcdef" for character in envelope_digest
        ):
            raise ValidationError("mailbox acknowledgement envelope digest is invalid")
        encoded_event_id = quote(event_id, safe="")
        return self.request(
            "POST",
            f"/v1/mailbox/{encoded_event_id}/acknowledge",
            json_body={"envelope_digest": envelope_digest},
        )

    def close(self) -> None:
        self._client.close()
