"""Authenticated current-credential lifecycle HTTP routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable

from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.context import ExpiredCredentialTransportContext
from agentnet.identity.credentials import (
    CredentialRenewalRequest,
    CredentialRotationRequest,
    LaptopCredentialReauthorizationPendingResult,
    LaptopCredentialReauthorizationPrepareRequest,
    LaptopCredentialReauthorizationProgressRequest,
)


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
ExpiredBodyAndContext = Callable[
    [Request, CommunicationCore, bool],
    Awaitable[tuple[bytes, ExpiredCredentialTransportContext]],
]


def create_credential_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    expired_body_and_context: ExpiredBodyAndContext,
) -> list[Route]:
    """Mount only current credential renewal, rotation, and reauthorization."""

    async def renew_current_credential(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = CredentialRenewalRequest.model_validate_json(body)
        result = core.renew_current_credential(actor=actor, request=parsed)
        return JSONResponse(result.model_dump(mode="json", by_alias=True))

    async def rotate_current_credential(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = CredentialRotationRequest.model_validate_json(body)
        result = core.rotate_credential(actor=actor, request=parsed)
        return JSONResponse(
            {"credential": result.model_dump(mode="json")},
            status_code=201,
        )

    async def prepare_expired_credential_reauthorization(
        request: Request,
    ) -> Response:
        body, context = await expired_body_and_context(request, core, False)
        parsed = LaptopCredentialReauthorizationPrepareRequest.model_validate_json(
            body,
            strict=True,
        )
        result = core.prepare_expired_credential_reauthorization(
            presented_credential_id=context.binding.credential_id,
            request=parsed,
        )
        return JSONResponse(result.model_dump(mode="json", by_alias=True))

    async def progress_expired_credential_reauthorization(
        request: Request,
    ) -> Response:
        body, context = await expired_body_and_context(request, core, True)
        parsed = LaptopCredentialReauthorizationProgressRequest.model_validate_json(
            body,
            strict=True,
        )
        result = core.progress_expired_credential_reauthorization(
            presented_credential_id=context.binding.credential_id,
            request=parsed,
        )
        return JSONResponse(
            result.model_dump(mode="json", by_alias=True),
            status_code=(
                202
                if isinstance(result, LaptopCredentialReauthorizationPendingResult)
                else 200
            ),
        )

    return [
        Route("/v1/credentials/current/renew", renew_current_credential, methods=["POST"]),
        Route("/v1/credentials/current/rotate", rotate_current_credential, methods=["POST"]),
        Route(
            "/v1/credentials/current/reauthorize-expired/prepare",
            prepare_expired_credential_reauthorization,
            methods=["POST"],
        ),
        Route(
            "/v1/credentials/current/reauthorize-expired",
            progress_expired_credential_reauthorization,
            methods=["POST"],
        ),
    ]


__all__ = ["ExpiredBodyAndContext", "create_credential_routes"]
