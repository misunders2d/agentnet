"""Strict authenticated sponsorship and public candidate invitation routes."""

from __future__ import annotations

import base64
import binascii
from collections.abc import Awaitable, Callable
from dataclasses import asdict
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import OperationClass
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ValidationError
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_ISSUE_ACTION,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRequest,
    InternalInvitationTransaction,
)
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.product_http import RELATIONSHIP_RESPONSE_HEADERS
from agentnet.security.signatures import canonical_digest, canonical_json


BodyAndActor = Callable[[Request, CommunicationCore], Awaitable[tuple[bytes, Any]]]


class InvitationIssueBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    invitation: InternalInvitationRequest
    grant_use: GrantUse | None = None


class InvitationRevokeBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    command: SignedAuthorityCommand
    grant_use: GrantUse | None = None


class InvitationOIDCBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    canonical_invitation_b64: str = Field(min_length=128, max_length=90_000)


class InvitationOIDCCompleteBody(InvitationOIDCBeginBody):
    state: str = Field(min_length=32, max_length=256)
    code: str = Field(min_length=8, max_length=4_096)


class InvitationAcceptBody(InvitationOIDCBeginBody):
    oidc_transaction_id: str = Field(min_length=16, max_length=128)
    oidc_acceptance_token: str = Field(min_length=32, max_length=256)
    candidate_possession_signature: str = Field(min_length=1, max_length=2_048)


def _canonical_invitation(value: str) -> bytes:
    try:
        decoded = base64.b64decode(value.encode("ascii"), validate=True)
    except (UnicodeEncodeError, binascii.Error, ValueError) as exc:
        raise ValidationError("canonical invitation is not canonical base64") from exc
    if len(decoded) < 128 or len(decoded) > 65_536:
        raise ValidationError("canonical invitation is outside the supported size")
    try:
        transaction = InternalInvitationTransaction.model_validate_json(decoded, strict=True)
    except Exception as exc:
        raise ValidationError("canonical invitation does not match the strict schema") from exc
    if canonical_json(transaction.model_dump(mode="json")) != decoded:
        raise ValidationError("invitation bytes are not exactly canonical")
    return decoded


async def _public_json(request: Request, core: CommunicationCore, model: type[BaseModel]) -> BaseModel:
    content_type = request.headers.get("content-type", "").split(";", 1)[0].strip().casefold()
    if content_type != "application/json":
        raise ValidationError("invitation candidate requests require application/json")
    body = await request.body()
    if not body or len(body) > core.config.max_request_bytes:
        raise ValidationError("invitation candidate body is empty or exceeds the configured limit")
    return model.model_validate_json(body, strict=True)


def _source_fingerprint(request: Request) -> str:
    """Hash only transport-derived peer facts; body/header claims are ignored."""

    if request.client is None or not request.client.host:
        raise AuthenticationError("invitation source transport is unavailable")
    return canonical_digest(
        {
            "schema": "agentnet.internal-invitation-source.v1",
            "peer_host": request.client.host,
            "transport_scheme": request.scope.get("scheme", ""),
        }
    )


def create_internal_invitation_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
) -> list[Route]:
    service = core.internal_invitations
    coordinator = core.internal_invitation_oidc
    if service is None or coordinator is None:
        return []

    def authority(
        *,
        actor: Any,
        action: str,
        resource: str,
        context: dict[str, Any],
        grant_use: GrantUse | None,
    ) -> IssuanceAuthority:
        decision = core._require(
            actor=actor,
            action=action,
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            context=context,
            grant_use=grant_use,
        )
        return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)

    async def issue_invitation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = InvitationIssueBody.model_validate_json(body, strict=True)
        resource, context = service.issuance_binding(parsed.invitation)
        issued = service.issue(
            parsed.invitation,
            authority=authority(
                actor=actor,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource=resource,
                context=context,
                grant_use=parsed.grant_use,
            ),
        )
        return JSONResponse(
            {
                "invitation": issued.model_dump(mode="json"),
                "zero_authority_proposal": True,
            },
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def revoke_invitation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = InvitationRevokeBody.model_validate_json(body, strict=True)
        invitation_id = request.path_params["invitation_id"]
        resource = f"internal-invitation:{invitation_id}"
        record = service.revoke(
            invitation_id,
            command=parsed.command,
            authority=authority(
                actor=actor,
                action=INTERNAL_INVITATION_REVOKE_ACTION,
                resource=resource,
                context={"request_digest": parsed.command.request_digest},
                grant_use=parsed.grant_use,
            ),
        )
        return JSONResponse(
            {"invitation": record.model_dump(mode="json")},
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def begin_candidate_oidc(request: Request) -> Response:
        parsed = await _public_json(request, core, InvitationOIDCBeginBody)
        if not isinstance(parsed, InvitationOIDCBeginBody):  # pragma: no cover - type narrowing
            raise RuntimeError("invalid invitation begin model")
        canonical = _canonical_invitation(parsed.canonical_invitation_b64)
        transaction = InternalInvitationTransaction.model_validate_json(canonical, strict=True)
        authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
        return JSONResponse(
            {"authorization": asdict(authorization)},
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def complete_candidate_oidc(request: Request) -> Response:
        parsed = await _public_json(request, core, InvitationOIDCCompleteBody)
        if not isinstance(parsed, InvitationOIDCCompleteBody):  # pragma: no cover - type narrowing
            raise RuntimeError("invalid invitation completion model")
        canonical = _canonical_invitation(parsed.canonical_invitation_b64)
        transaction = InternalInvitationTransaction.model_validate_json(canonical, strict=True)
        challenge = coordinator.complete_authorization(
            canonical_invitation=canonical,
            evidence={"state": parsed.state, "code": parsed.code},
        )
        verification = OIDCVerificationResult(
            identity=challenge.identity,
            id_token_hash=challenge.id_token_hash,
            expires_at=challenge.expires_at,
        )
        possession_fields = service.candidate_possession_fields(transaction, verification)
        return JSONResponse(
            {
                "oidc_transaction_id": challenge.transaction_id,
                "oidc_acceptance_token": challenge.acceptance_token,
                "candidate_possession_fields": possession_fields,
                "expires_at": challenge.expires_at,
            },
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    async def accept_invitation(request: Request) -> Response:
        parsed = await _public_json(request, core, InvitationAcceptBody)
        if not isinstance(parsed, InvitationAcceptBody):  # pragma: no cover - type narrowing
            raise RuntimeError("invalid invitation acceptance model")
        canonical = _canonical_invitation(parsed.canonical_invitation_b64)
        transaction = InternalInvitationTransaction.model_validate_json(canonical, strict=True)
        accepted = service.accept(
            invitation_id=transaction.invitation_id,
            canonical_invitation=canonical,
            oidc_evidence={
                "transaction_id": parsed.oidc_transaction_id,
                "acceptance_token": parsed.oidc_acceptance_token,
            },
            candidate_possession_signature=parsed.candidate_possession_signature,
            source_fingerprint=_source_fingerprint(request),
        )
        return JSONResponse(
            {"acceptance": accepted.model_dump(mode="json")},
            status_code=201,
            headers=RELATIONSHIP_RESPONSE_HEADERS,
        )

    return [
        Route("/v1/internal-invitations", issue_invitation, methods=["POST"]),
        Route(
            "/v1/internal-invitations/{invitation_id}/revoke",
            revoke_invitation,
            methods=["POST"],
        ),
        Route(
            "/v1/internal-invitations/oidc/begin",
            begin_candidate_oidc,
            methods=["POST"],
        ),
        Route(
            "/v1/internal-invitations/oidc/complete",
            complete_candidate_oidc,
            methods=["POST"],
        ),
        Route("/v1/internal-invitations/accept", accept_invitation, methods=["POST"]),
    ]


__all__ = ["create_internal_invitation_routes"]
