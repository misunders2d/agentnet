"""Mounted HTTPS transport for ordinary server-agent relay packets.

The transport adds no identity class or positive authority.  Packet and
receipt signatures are verified by :class:`ServerAgentRelayService`; this
module only supplies bounded HTTP movement and restart-safe worker calls.
"""

from __future__ import annotations

import ipaddress
import json
from typing import Any, Literal
from urllib.parse import urlsplit

import httpx
from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    StrictBool,
    ValidationError as PydanticValidationError,
)
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import BaseRoute, Route

from agentnet.errors import ExtensionError, GateBlocked, ValidationError
from agentnet.relay.service import (
    RelayPacket,
    RelayPeerKeyRevocation,
    RelayPeerKeyRotation,
    ServerAgentRelayService,
    ServerRelayReceipt,
)
from agentnet.security.signatures import canonical_json


class RelayReceiptAcknowledgement(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    packet_id: str = Field(min_length=16, max_length=128)
    state: Literal["remote_accepted", "recipient_committed"]
    advanced: StrictBool


class RelayPeerKeyRotationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    rotation: RelayPeerKeyRotation
    local_signature: str = Field(min_length=1, max_length=2_048)
    peer_signature: str = Field(min_length=1, max_length=2_048)
    policy_decision_id: str = Field(min_length=1, max_length=256)


class RelayPeerKeyRevocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    revocation: RelayPeerKeyRevocation
    local_signature: str = Field(min_length=1, max_length=2_048)
    policy_decision_id: str = Field(min_length=1, max_length=256)


def _canonical_relay_origin(value: str, *, allow_loopback_http_lab: bool) -> str:
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise ValidationError("relay endpoint origin is invalid") from exc
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise ValidationError("relay endpoint must be a credential-free canonical HTTP(S) origin")
    hostname = parsed.hostname.casefold()
    if parsed.scheme == "http":
        try:
            loopback = ipaddress.ip_address(hostname).is_loopback
        except ValueError:
            loopback = hostname == "localhost"
        if not (allow_loopback_http_lab and loopback and port is not None):
            raise ValidationError("remote server-agent relay endpoints require HTTPS")
    default_port = 80 if parsed.scheme == "http" else 443
    rendered_host = f"[{hostname}]" if ":" in hostname else hostname
    canonical = f"{parsed.scheme}://{rendered_host}"
    if port not in {None, default_port}:
        canonical += f":{port}"
    if value.rstrip("/") != canonical:
        raise ValidationError("relay endpoint origin is not canonical")
    return canonical


def create_relay_routes(
    service: ServerAgentRelayService,
    *,
    max_request_bytes: int = 16_777_216,
) -> list[BaseRoute]:
    if not 1_024 <= max_request_bytes <= 67_108_864:
        raise ValueError("relay request bound is invalid")

    async def bounded_json(request: Request) -> bytes:
        if request.headers.get("content-type", "").split(";", 1)[0].strip().casefold() != "application/json":
            raise ValidationError("relay transport requires application/json")
        body = await request.body()
        if not body or len(body) > max_request_bytes:
            raise ValidationError("relay request body is empty or exceeds the configured bound")
        return body

    async def receive_packet(request: Request) -> Response:
        packet = RelayPacket.model_validate_json(await bounded_json(request))
        receipt = service.accept(packet)
        return JSONResponse(receipt.model_dump(mode="json"), status_code=202)

    async def receive_receipt(request: Request) -> Response:
        receipt = ServerRelayReceipt.model_validate_json(await bounded_json(request))
        return JSONResponse(service.record_receipt(receipt), status_code=202)

    async def rotate_peer_key(request: Request) -> Response:
        body = RelayPeerKeyRotationBody.model_validate_json(await bounded_json(request))
        result = service.rotate_peer_key(
            peer_domain_id=request.path_params["peer_domain_id"],
            rotation=body.rotation,
            local_signature=body.local_signature,
            peer_signature=body.peer_signature,
            policy_decision_id=body.policy_decision_id,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def revoke_peer_key(request: Request) -> Response:
        body = RelayPeerKeyRevocationBody.model_validate_json(await bounded_json(request))
        result = service.revoke_peer_key(
            peer_domain_id=request.path_params["peer_domain_id"],
            revocation=body.revocation,
            local_signature=body.local_signature,
            policy_decision_id=body.policy_decision_id,
        )
        return JSONResponse(result, status_code=200)

    return [
        Route("/v1/server-agent-relay/packets", receive_packet, methods=["POST"]),
        Route("/v1/server-agent-relay/receipts", receive_receipt, methods=["POST"]),
        Route(
            "/v1/server-agent-relay/peers/{peer_domain_id}/key-rotations",
            rotate_peer_key,
            methods=["POST"],
        ),
        Route(
            "/v1/server-agent-relay/peers/{peer_domain_id}/key-revocations",
            revoke_peer_key,
            methods=["POST"],
        ),
    ]


def create_relay_app(
    service: ServerAgentRelayService,
    *,
    max_request_bytes: int = 16_777_216,
) -> Starlette:
    async def exception_handler(_request: Request, exc: Exception) -> Response:
        if isinstance(exc, ExtensionError):
            return JSONResponse(exc.public_detail(), status_code=exc.http_status)
        if isinstance(exc, (PydanticValidationError, json.JSONDecodeError)):
            return JSONResponse({"code": "invalid_request", "message": "request validation failed"}, status_code=422)
        return JSONResponse({"code": "internal_error", "message": "request could not be processed"}, status_code=500)

    return Starlette(
        debug=False,
        routes=create_relay_routes(service, max_request_bytes=max_request_bytes),
        exception_handlers={Exception: exception_handler},
    )


class ServerAgentRelayClient:
    """Bounded outbound worker for one explicitly pinned peer origin."""

    def __init__(
        self,
        *,
        base_url: str,
        service: ServerAgentRelayService | None = None,
        allow_loopback_http_lab: bool = False,
        transport: httpx.AsyncBaseTransport | None = None,
        timeout_seconds: float = 10.0,
    ) -> None:
        if not 0.1 <= timeout_seconds <= 120:
            raise ValueError("relay client timeout is invalid")
        self.base_url = _canonical_relay_origin(
            base_url,
            allow_loopback_http_lab=allow_loopback_http_lab,
        )
        self.service = service
        self._client = httpx.AsyncClient(
            base_url=self.base_url,
            transport=transport,
            timeout=timeout_seconds,
            follow_redirects=False,
        )

    async def send_packet(self, packet: RelayPacket) -> ServerRelayReceipt:
        if self.service is None or self.service.admission is None:
            raise GateBlocked("relay_transport", "relay transport lacks durable admission controls")
        self.service.admission.admit_operation(
            actor_scope=packet.source_relay_harness_id,
            domain_scope=packet.source_domain_id,
            operation="relay_transport",
            operation_id=f"relay-transport:{packet.packet_id}",
            pending_cost=0,
        )
        try:
            response = await self._client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(packet.model_dump(mode="json")),
                headers={"Content-Type": "application/json"},
            )
            response.raise_for_status()
            receipt = ServerRelayReceipt.model_validate(response.json())
            self.service.record_receipt(receipt)
        except Exception:
            self.service.admission.record_failure(
                operation="relay_transport",
                domain_scope=packet.source_domain_id,
            )
            raise
        self.service.admission.record_success(
            operation="relay_transport",
            domain_scope=packet.source_domain_id,
        )
        return receipt

    async def send_receipt(self, receipt: ServerRelayReceipt) -> dict[str, Any]:
        if self.service is None or self.service.admission is None:
            raise GateBlocked("relay_transport", "relay receipt transport lacks durable admission controls")
        self.service.admission.admit_operation(
            actor_scope=receipt.target_relay_harness_id,
            domain_scope=receipt.target_domain_id,
            operation="relay_receipt_transport",
            operation_id=f"relay-receipt-transport:{receipt.packet_id}",
            pending_cost=0,
        )
        try:
            response = await self._client.post(
                "/v1/server-agent-relay/receipts",
                content=canonical_json(receipt.model_dump(mode="json")),
                headers={"Content-Type": "application/json"},
            )
            response.raise_for_status()
            acknowledgement = RelayReceiptAcknowledgement.model_validate(response.json())
            allowed_states = (
                {"recipient_committed"}
                if receipt.fact == "recipient_committed"
                else {"remote_accepted", "recipient_committed"}
            )
            if (
                acknowledgement.packet_id != receipt.packet_id
                or acknowledgement.state not in allowed_states
            ):
                raise ValidationError("relay receipt acknowledgement binding is invalid")
        except Exception:
            self.service.admission.record_failure(
                operation="relay_receipt_transport",
                domain_scope=receipt.target_domain_id,
            )
            raise
        self.service.admission.record_success(
            operation="relay_receipt_transport",
            domain_scope=receipt.target_domain_id,
        )
        return acknowledgement.model_dump(mode="json")

    async def send_key_rotation(
        self,
        *,
        peer_domain_id: str,
        rotation: RelayPeerKeyRotation,
        local_signature: str,
        peer_signature: str,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        response = await self._client.post(
            f"/v1/server-agent-relay/peers/{peer_domain_id}/key-rotations",
            content=canonical_json(
                {
                    "rotation": rotation.model_dump(mode="json"),
                    "local_signature": local_signature,
                    "peer_signature": peer_signature,
                    "policy_decision_id": policy_decision_id,
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        response.raise_for_status()
        value = response.json()
        if (
            not isinstance(value, dict)
            or value.get("peer_domain_id") != peer_domain_id
            or value.get("active_key_id") != rotation.to_key_id
            or value.get("active_key_epoch") != rotation.to_key_epoch
        ):
            raise ValidationError("relay key rotation acknowledgement binding is invalid")
        return value

    async def send_key_revocation(
        self,
        *,
        peer_domain_id: str,
        revocation: RelayPeerKeyRevocation,
        local_signature: str,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        response = await self._client.post(
            f"/v1/server-agent-relay/peers/{peer_domain_id}/key-revocations",
            content=canonical_json(
                {
                    "revocation": revocation.model_dump(mode="json"),
                    "local_signature": local_signature,
                    "policy_decision_id": policy_decision_id,
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        response.raise_for_status()
        value = response.json()
        if (
            not isinstance(value, dict)
            or value.get("peer_domain_id") != peer_domain_id
            or value.get("key_id") != revocation.key_id
            or value.get("key_epoch") != revocation.key_epoch
            or value.get("status") != "revoked"
        ):
            raise ValidationError("relay key revocation acknowledgement binding is invalid")
        return value

    async def aclose(self) -> None:
        await self._client.aclose()

    async def __aenter__(self) -> "ServerAgentRelayClient":
        return self

    async def __aexit__(self, *_args: object) -> None:
        await self.aclose()
