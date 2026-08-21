"""Signed and browser-OIDC console session establishment routes."""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, RedirectResponse, Response
from starlette.routing import Route

from agentnet.authorization.policy import OperationClass
from agentnet.console.headers import protected_headers
from agentnet.console.session import ConsoleOIDCCoordinator, ConsoleSessionService
from agentnet.core.app import CommunicationCore
from agentnet.errors import GateBlocked, ValidationError
from agentnet.identity.sponsored_enrollment import SponsoredEnrollmentService


SESSION_COOKIE = "__Host-agentnet_console"
PREAUTH_COOKIE = "__Host-agentnet_console_preauth"

AuthenticateRequest = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, Any]],
]
HostGuard = Callable[[Request], None]
ParsedForm = Callable[..., Awaitable[dict[str, list[str]]]]
SingleValue = Callable[..., str]


class _ChallengeComplete(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    transaction_digest: str = Field(pattern=r"^[a-f0-9]{64}$")


def create_console_session_routes(
    *,
    core: CommunicationCore | None,
    sessions: ConsoleSessionService,
    oidc: ConsoleOIDCCoordinator | None,
    sponsored_enrollment: SponsoredEnrollmentService | None,
    origin: str,
    authenticate_request: AuthenticateRequest,
    require_host: HostGuard,
    parsed_form: ParsedForm,
    single_value: SingleValue,
) -> list[Route]:
    """Mount signed handoff and browser OIDC session-establishment routes."""

    async def begin_challenge(request: Request) -> Response:
        if core is None:
            raise GateBlocked("admin_console", "Signed console launch is unavailable")
        _, context = await authenticate_request(request, core)
        core._require(
            actor=context.actor,
            action="console.session.open",
            resource=f"console-domain:{context.actor.domain_id}",
            operation_class=OperationClass.PROTECTED_READ,
        )
        challenge = sessions.begin_challenge(actor=context.actor)
        return JSONResponse(
            {
                "schema": "agentnet.console.session-challenge-result.v1",
                "challenge_id": challenge.challenge_id,
                "transaction": challenge.transaction,
                "transaction_digest": challenge.transaction_digest,
                "expires_at": challenge.expires_at,
                "console_origin": origin,
            },
            status_code=201,
            headers=protected_headers(),
        )

    async def complete_challenge(request: Request) -> Response:
        if core is None:
            raise GateBlocked("admin_console", "Signed console launch is unavailable")
        raw, context = await authenticate_request(request, core)
        try:
            parsed = _ChallengeComplete.model_validate_json(raw)
        except PydanticValidationError as exc:
            raise ValidationError("console challenge completion is invalid") from exc
        completed = sessions.complete_challenge(
            actor=context.actor,
            challenge_id=request.path_params["challenge_id"],
            transaction_digest=parsed.transaction_digest,
        )
        return JSONResponse(
            {
                "schema": "agentnet.console.session-handoff.v1",
                "handoff_token": completed.handoff_token,
                "expires_at": completed.expires_at,
            },
            headers=protected_headers(),
        )

    async def open_console(request: Request) -> Response:
        require_host(request)
        if oidc is None:
            raise GateBlocked("admin_console_oidc", "Sign in is unavailable")
        form = await parsed_form(request, same_origin=False)
        begun = oidc.begin(handoff_token=single_value(form, "handoff_token"))
        response = RedirectResponse(
            begun.authorization_url,
            status_code=303,
            headers=protected_headers(),
        )
        response.set_cookie(
            PREAUTH_COOKIE,
            begun.preauth_token,
            max_age=max(1, begun.expires_at - sessions.clock()),
            secure=True,
            httponly=True,
            samesite="lax",
            path="/",
        )
        return response

    async def oidc_callback(request: Request) -> Response:
        require_host(request)
        if oidc is None:
            raise GateBlocked("admin_console_oidc", "Sign in is unavailable")
        state = request.query_params.get("state", "")
        code = request.query_params.get("code", "")
        if sponsored_enrollment is not None and sponsored_enrollment.owns_state(state):
            sponsored_enrollment.complete_candidate_oidc(state=state, code=code)
            return HTMLResponse(
                "<!doctype html><html><head><meta charset=utf-8><title>Identity verified</title></head>"
                "<body><main><h1>Identity verified</h1><p>Return to AgentNet on this device. "
                "The sponsor must approve the exact enrollment before access is created.</p></main></body></html>",
                headers=protected_headers(),
            )
        issued = oidc.complete(
            state=state,
            code=code,
            preauth_token=request.cookies.get(PREAUTH_COOKIE, ""),
        )
        response = RedirectResponse("/", status_code=303, headers=protected_headers())
        response.set_cookie(
            SESSION_COOKIE,
            issued.session_token,
            max_age=max(1, issued.expires_at - sessions.clock()),
            secure=True,
            httponly=True,
            samesite="strict",
            path="/",
        )
        response.delete_cookie(
            PREAUTH_COOKIE,
            path="/",
            secure=True,
            httponly=True,
            samesite="lax",
        )
        return response

    return [
        Route("/v1/console/session-challenges", begin_challenge, methods=["POST"]),
        Route(
            "/v1/console/session-challenges/{challenge_id}/complete",
            complete_challenge,
            methods=["POST"],
        ),
        Route("/v1/console/open", open_console, methods=["POST"]),
        Route("/v1/console/oidc/callback", oidc_callback, methods=["GET"]),
    ]


__all__ = [
    "PREAUTH_COOKIE",
    "SESSION_COOKIE",
    "create_console_session_routes",
]
