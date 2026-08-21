"""Public OIDC credential-recovery HTTP routes."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import asdict
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.errors import ValidationError
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator


class RecoveryBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    old_harness_id: str = Field(min_length=1, max_length=256)
    new_harness_kind: str = Field(min_length=1, max_length=64)
    new_harness_name: str = Field(min_length=1, max_length=128)
    new_binding_assurance: Literal["os_bound", "hardware_bound"]
    new_public_key_pem: str = Field(min_length=128, max_length=16_384)


class RecoveryCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recovery_transaction_id: str = Field(min_length=16, max_length=128)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    independent_approvals: tuple[dict[str, Any], ...] = Field(min_length=1, max_length=5)


def create_credential_recovery_routes(
    core: CommunicationCore,
    recovery_coordinator: OIDCCredentialRecoveryCoordinator,
    response_headers: Mapping[str, str],
) -> list[Route]:
    """Mount public recovery routes only when recovery is configured."""

    async def begin_recovery(request: Request) -> Response:
        peer = "unavailable" if request.client is None else request.client.host
        core.quotas.consume(
            scope=f"public-credential-recovery:{peer}",
            metric="recovery_attempts",
            amount=1,
            limit=20,
        )
        body = await request.body()
        if len(body) > core.config.max_request_bytes:
            raise ValidationError("credential recovery request exceeds the configured limit")
        parsed = RecoveryBeginBody.model_validate_json(body)
        authorization = recovery_coordinator.begin_authorization(
            domain_id=core.config.domain_id,
            old_harness_id=parsed.old_harness_id,
            new_harness_kind=parsed.new_harness_kind,
            new_harness_name=parsed.new_harness_name,
            new_binding_assurance=parsed.new_binding_assurance,
            new_public_key_pem=parsed.new_public_key_pem,
        )
        return JSONResponse(
            asdict(authorization),
            status_code=201,
            headers=response_headers,
        )

    async def complete_recovery(request: Request) -> Response:
        peer = "unavailable" if request.client is None else request.client.host
        core.quotas.consume(
            scope=f"public-credential-recovery:{peer}",
            metric="recovery_attempts",
            amount=1,
            limit=20,
        )
        body = await request.body()
        if len(body) > core.config.max_request_bytes:
            raise ValidationError("credential recovery request exceeds the configured limit")
        parsed = RecoveryCompleteBody.model_validate_json(body)
        result = recovery_coordinator.complete_recovery(
            transaction_id=parsed.recovery_transaction_id,
            possession_signature=parsed.possession_signature,
            approvals=parsed.independent_approvals,
        )
        return JSONResponse(
            result.model_dump(mode="json"),
            status_code=201,
            headers=response_headers,
        )

    return [
        Route("/v1/credential-recovery/oidc/begin", begin_recovery, methods=["POST"]),
        Route("/v1/credential-recovery/complete", complete_recovery, methods=["POST"]),
    ]


__all__ = ["create_credential_recovery_routes"]
