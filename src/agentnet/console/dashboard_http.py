"""Authenticated read-only administration console page routes."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from starlette.requests import Request
from starlette.responses import HTMLResponse, Response
from starlette.routing import Route

from agentnet.console.read_service import ConsoleReadService
from agentnet.console.render import ConsoleRenderer
from agentnet.console.session import ConsoleSessionStatus


SessionResolver = Callable[[Request], ConsoleSessionStatus]
MutationAuthorizer = Callable[[Request], Any]
PageResponse = Callable[..., HTMLResponse]


def create_console_dashboard_routes(
    *,
    read_service: ConsoleReadService,
    renderer: ConsoleRenderer,
    session_for: SessionResolver,
    mutation_authorizer: MutationAuthorizer,
    page_response: PageResponse,
) -> list[Route]:
    """Mount authenticated read-only console pages."""

    async def home(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.home(
                read_service.home(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    async def servers_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.servers(
                read_service.servers(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    async def people_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.people(
                read_service.people(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    async def approvals_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.approvals(
                read_service.approvals(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    async def security_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.security(
                read_service.security(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    async def activity_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(
            renderer.activity(
                read_service.activity(actor=status.actor),
                mutation_authorizer(request),
            )
        )

    return [
        Route("/", home, methods=["GET"]),
        Route("/servers", servers_page, methods=["GET"]),
        Route("/people", people_page, methods=["GET"]),
        Route("/approvals", approvals_page, methods=["GET"]),
        Route("/security", security_page, methods=["GET"]),
        Route("/activity", activity_page, methods=["GET"]),
    ]


__all__ = ["create_console_dashboard_routes"]
