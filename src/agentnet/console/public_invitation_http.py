"""Public invitation display and work-account continuation routes."""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from typing import Protocol
from urllib.parse import quote, urlsplit

from pydantic import ValidationError as PydanticValidationError
from starlette.requests import Request
from starlette.responses import HTMLResponse, RedirectResponse, Response
from starlette.routing import Route

from agentnet.console.headers import protected_headers
from agentnet.console.models import InvitationContinuationResult
from agentnet.console.render import ConsoleRenderer
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    ValidationError,
)
from agentnet.identity.invitation_links import (
    InvitationLinkService,
    InvitationUnavailable,
)
from agentnet.identity.onboarding_prompt import build_onboarding_prompt


FormParser = Callable[[bytes], dict[str, list[str]]]


class InvitationContinuationService(Protocol):
    """Package-owned boundary that performs verified invitation continuation."""

    def continue_with_work_account(
        self,
        *,
        opaque_token: str,
        source_fingerprint: str,
    ) -> InvitationContinuationResult: ...


def create_public_invitation_routes(
    *,
    invitation_links: InvitationLinkService,
    invitation_continuation: InvitationContinuationService | None,
    invitation_authorization_origins: frozenset[str],
    renderer: ConsoleRenderer,
    expected_host: str,
    origin: str,
    parse_form: FormParser,
) -> list[Route]:
    """Mount public invitation routes without console-session authority."""

    def require_host(request: Request) -> None:
        if request.headers.get("host", "").casefold() != expected_host.casefold():
            raise AuthenticationError("console request denied")

    def require_public_invitation_origin(request: Request) -> None:
        supplied = request.headers.get("origin", "")
        if supplied not in {origin, "null"}:
            raise AuthorizationError("invitation continuation denied")

    async def parsed_form(request: Request) -> dict[str, list[str]]:
        require_host(request)
        content_type = (
            request.headers.get("content-type", "")
            .split(";", 1)[0]
            .strip()
            .casefold()
        )
        if content_type != "application/x-www-form-urlencoded":
            raise ValidationError("form encoding is required")
        return parse_form(await request.body())

    def public_headers() -> dict[str, str]:
        return protected_headers(
            {
                "Content-Security-Policy": "; ".join(
                    (
                        "default-src 'none'",
                        "base-uri 'none'",
                        "form-action 'self'",
                        "frame-ancestors 'none'",
                        "script-src 'self'",
                        "style-src 'self'",
                    )
                )
            }
        )

    def public_page_response(
        document: str,
        *,
        status_code: int = 200,
    ) -> HTMLResponse:
        return HTMLResponse(
            document,
            status_code=status_code,
            headers=public_headers(),
        )

    def unavailable_invitation_response() -> HTMLResponse:
        return public_page_response(
            renderer.public_invitation_unavailable(),
            status_code=410,
        )

    def source_fingerprint(request: Request) -> str:
        if request.client is None or not request.client.host:
            raise AuthenticationError("invitation source transport is unavailable")
        return hashlib.sha256(
            request.client.host.casefold().encode("utf-8")
        ).hexdigest()

    async def public_invitation(request: Request) -> Response:
        require_host(request)
        opaque_token = request.path_params["opaque_token"]
        try:
            summary = invitation_links.inspect_public(opaque_token=opaque_token)
        except InvitationUnavailable:
            return unavailable_invitation_response()
        prompt = build_onboarding_prompt(summary)
        continue_path = f"/join/{quote(opaque_token, safe='')}/continue"
        return public_page_response(
            renderer.public_invitation(
                prompt=prompt,
                continue_path=continue_path,
            )
        )

    async def continue_public_invitation(request: Request) -> Response:
        opaque_token = request.path_params["opaque_token"]
        try:
            require_public_invitation_origin(request)
            form = await parsed_form(request)
            if form:
                raise ValidationError("invitation continuation accepts no browser fields")
            invitation_links.inspect_public(opaque_token=opaque_token)
            if invitation_continuation is None:
                raise InvitationUnavailable()
            result = InvitationContinuationResult.model_validate(
                invitation_continuation.continue_with_work_account(
                    opaque_token=opaque_token,
                    source_fingerprint=source_fingerprint(request),
                )
            )
        except (
            InvitationUnavailable,
            AuthenticationError,
            AuthorizationError,
            ConflictError,
            PydanticValidationError,
        ):
            return unavailable_invitation_response()
        if result.state == "authorization_required":
            authorization_url = str(result.authorization_url)
            parsed = urlsplit(authorization_url)
            authorization_origin = f"{parsed.scheme}://{parsed.netloc}".casefold()
            allowed_origins = {
                value.rstrip("/").casefold()
                for value in invitation_authorization_origins
            }
            if authorization_origin not in allowed_origins:
                return unavailable_invitation_response()
            return RedirectResponse(
                authorization_url,
                status_code=303,
                headers=public_headers(),
            )
        return public_page_response(
            renderer.public_invitation_status(state=result.state)
        )

    return [
        Route("/join/{opaque_token}", public_invitation, methods=["GET"]),
        Route(
            "/join/{opaque_token}/continue",
            continue_public_invitation,
            methods=["POST"],
        ),
    ]


__all__ = ["InvitationContinuationService", "create_public_invitation_routes"]
