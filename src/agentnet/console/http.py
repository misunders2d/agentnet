"""Dedicated server-hosted administration console application."""

from __future__ import annotations

import asyncio
import hashlib
import html
import json
from importlib.resources import files
from typing import Any
from urllib.parse import parse_qs, urlsplit

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, RedirectResponse, Response, StreamingResponse
from starlette.routing import Route
from agentnet.authorization.policy import OperationClass

from agentnet.console.headers import protected_headers
from agentnet.console.mutations import ConsoleMutationService
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.render import ConsoleRenderer
from agentnet.console.session import ConsoleOIDCCoordinator, ConsoleSessionService, ConsoleSessionStatus
from agentnet.identity.sponsored_enrollment import SponsoredEnrollmentService
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ExtensionError, GateBlocked, ValidationError
from agentnet.http_auth import authenticate_proof_request

SESSION_COOKIE = "__Host-agentnet_console"
PREAUTH_COOKIE = "__Host-agentnet_console_preauth"
_MAX_FORM_BYTES = 65_536


class _ChallengeComplete(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    transaction_digest: str = Field(pattern=r"^[a-f0-9]{64}$")


class _SponsoredCandidateBegin(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    candidate_harness_id: str = Field(min_length=1, max_length=256)
    harness_kind: str = Field(min_length=1, max_length=64)
    harness_name: str = Field(min_length=1, max_length=128)
    binding_assurance: str = Field(pattern=r"^(os_bound|hardware_bound)$")
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    idempotency_key: str = Field(min_length=16, max_length=128)

def _asset(name: str) -> bytes:
    return files("agentnet.console").joinpath("static", name).read_bytes()


def _parse_query_form(raw: bytes) -> dict[str, list[str]]:
    if len(raw) > _MAX_FORM_BYTES:
        raise ValidationError("form is too large")
    try:
        decoded = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ValidationError("form encoding is invalid") from exc
    return parse_qs(decoded, keep_blank_values=True, strict_parsing=True, max_num_fields=64)


def _single(form: dict[str, list[str]], name: str, *, required: bool = True) -> str:
    values = form.get(name, [])
    if len(values) != 1 or (required and not values[0]):
        raise ValidationError(f"{name.replace('_', ' ')} is required")
    return values[0]


def create_console_app(
    core: CommunicationCore | None = None,
    *,
    sessions: ConsoleSessionService | None = None,
    read_service: ConsoleReadService | None = None,
    mutation_service: ConsoleMutationService | None = None,
    public_origin: str | None = None,
    oidc: ConsoleOIDCCoordinator | None = None,
    sponsored_enrollment: SponsoredEnrollmentService | None = None,
) -> Starlette:
    if core is not None:
        console = core.config.admin_console
        sessions = core.console_sessions
        read_service = core.console_reads
        mutation_service = core.console_mutations
        oidc = core.console_oidc
        sponsored_enrollment = core.sponsored_enrollment
        public_origin = console.public_origin if console is not None else None
    if sessions is None or read_service is None or mutation_service is None or public_origin is None:
        raise GateBlocked("admin_console", "admin console services are not configured")
    origin = public_origin.rstrip("/")
    parsed_origin = urlsplit(origin)
    expected_host = parsed_origin.netloc
    css = _asset("console.css")
    javascript = _asset("console.js")
    asset_version = hashlib.sha256(css).hexdigest()
    renderer = ConsoleRenderer(
        asset_version=asset_version,
        approval_origin=mutation_service.approval_public_origin,
    )

    def require_origin(request: Request) -> None:
        supplied = request.headers.get("origin", "")
        if supplied != origin:
            raise AuthorizationError("console mutation denied")

    def require_host(request: Request) -> None:
        if request.headers.get("host", "").casefold() != expected_host.casefold():
            raise AuthenticationError("console request denied")

    def session_for(request: Request) -> ConsoleSessionStatus:
        require_host(request)
        token = request.cookies.get(SESSION_COOKIE, "")
        status = sessions.authenticate(token)
        request.scope["agentnet.console_session"] = status
        return status

    async def mutation_form(request: Request) -> tuple[ConsoleSessionStatus, dict[str, list[str]]]:
        require_host(request)
        require_origin(request)
        content_type = request.headers.get("content-type", "").split(";", 1)[0].strip().casefold()
        if content_type != "application/x-www-form-urlencoded":
            raise ValidationError("form encoding is required")
        raw_body = await request.body()
        form = _parse_query_form(raw_body)
        csrf_values = form.get("csrf_token", [])
        if len(csrf_values) != 1 or not csrf_values[0]:
            raise AuthorizationError("console mutation denied")
        status = sessions.require_mutation(
            session_token=request.cookies.get(SESSION_COOKIE, ""),
            csrf_token=csrf_values[0],
            method=request.method,
            path=request.url.path,
            body_sha256=hashlib.sha256(raw_body).hexdigest(),
        )
        request.scope["agentnet.console_session"] = status
        return status, form

    def page_response(document: str, *, status_code: int = 200) -> HTMLResponse:
        return HTMLResponse(document, status_code=status_code, headers=protected_headers())

    async def health(request: Request) -> Response:
        require_host(request)
        return JSONResponse(
            {"schema": "agentnet.console.health.v1", "status": "ready"},
            headers=protected_headers(),
        )

    async def stylesheet(_request: Request) -> Response:
        return Response(
            css,
            media_type="text/css; charset=utf-8",
            headers={
                "Cache-Control": "public,max-age=31536000,immutable",
                "Content-Security-Policy": "default-src 'none'",
                "X-Content-Type-Options": "nosniff",
            },
        )

    async def script(_request: Request) -> Response:
        return Response(
            javascript,
            media_type="text/javascript; charset=utf-8",
            headers={
                "Cache-Control": "public,max-age=31536000,immutable",
                "Content-Security-Policy": "default-src 'none'",
                "X-Content-Type-Options": "nosniff",
            },
        )

    async def home(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.home(read_service.home(actor=status.actor), status.csrf_token))

    async def servers_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.servers(read_service.servers(actor=status.actor), status.csrf_token))

    async def people_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.people(read_service.people(actor=status.actor), status.csrf_token))

    async def approvals_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.approvals(read_service.approvals(actor=status.actor), status.csrf_token))

    async def security_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.security(read_service.security(actor=status.actor), status.csrf_token))

    async def activity_page(request: Request) -> Response:
        status = session_for(request)
        return page_response(renderer.activity(read_service.activity(actor=status.actor), status.csrf_token))

    async def begin_challenge(request: Request) -> Response:
        if core is None:
            raise GateBlocked("admin_console", "Signed console launch is unavailable")
        _, context = await authenticate_proof_request(request, core)
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
            },
            status_code=201,
            headers=protected_headers(),
        )

    async def complete_challenge(request: Request) -> Response:
        if core is None:
            raise GateBlocked("admin_console", "Signed console launch is unavailable")
        raw, context = await authenticate_proof_request(request, core)
        try:
            parsed = _ChallengeComplete.model_validate_json(raw)
        except PydanticValidationError as exc:
            raise ValidationError("console challenge completion is invalid") from exc
        challenge_id = request.path_params["challenge_id"]
        sessions.complete_challenge(
            actor=context.actor,
            challenge_id=challenge_id,
            transaction_digest=parsed.transaction_digest,
        )
        return JSONResponse(
            {
                "schema": "agentnet.console.session-challenge-completed.v1",
                "launch_url": f"{origin}/v1/console/open/{challenge_id}",
            },
            headers=protected_headers(),
        )

    async def open_console(request: Request) -> Response:
        require_host(request)
        if oidc is None:
            raise GateBlocked("admin_console_oidc", "Sign in is unavailable")
        begun = oidc.begin(challenge_id=request.path_params["challenge_id"])
        response = RedirectResponse(begun.authorization_url, status_code=303, headers=protected_headers())
        response.set_cookie(
            PREAUTH_COOKIE,
            begun.preauth_token,
            max_age=max(1, begun.expires_at - sessions.clock()),
            secure=True,
            httponly=True,
            samesite="strict",
            path="/",
        )
        return response

    async def sponsored_candidate_begin(request: Request) -> Response:
        require_host(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        raw = await request.body()
        if len(raw) > _MAX_FORM_BYTES:
            raise ValidationError("request is too large")
        try:
            body = _SponsoredCandidateBegin.model_validate_json(raw)
        except PydanticValidationError as exc:
            raise ValidationError("candidate enrollment request is invalid") from exc
        result = sponsored_enrollment.begin_candidate(**body.model_dump())
        return JSONResponse(
            {
                "schema": "agentnet.sponsored-enrollment.candidate-begin-result.v1",
                "transaction_id": result.transaction_id,
                "authorization_url": result.authorization_url,
                "state": result.state,
                "continuation_token": result.continuation_token,
                "expires_at": result.expires_at,
            },
            status_code=201,
            headers=protected_headers(),
        )

    async def sponsored_candidate_status(request: Request) -> Response:
        require_host(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        raw = await request.body()
        if len(raw) > _MAX_FORM_BYTES:
            raise ValidationError("request is too large")
        try:
            body = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("candidate status request is invalid") from exc
        if (
            not isinstance(body, dict)
            or set(body) != {"continuation_token"}
            or not isinstance(body["continuation_token"], str)
        ):
            raise ValidationError("candidate status request is invalid")
        result = sponsored_enrollment.candidate_status(
            continuation_token=body["continuation_token"]
        )
        return JSONResponse(
            {"schema": "agentnet.sponsored-enrollment.candidate-status-result.v1", **result},
            headers=protected_headers(),
        )

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
        response.delete_cookie(PREAUTH_COOKIE, path="/", secure=True, httponly=True, samesite="strict")
        return response

    async def sign_out(request: Request) -> Response:
        status, _ = await mutation_form(request)
        token = request.cookies.get(SESSION_COOKIE, "")
        sessions.revoke(token)
        response = RedirectResponse("/", status_code=303, headers=protected_headers())
        response.delete_cookie(SESSION_COOKIE, path="/", secure=True, httponly=True, samesite="strict")
        del status
        return response

    async def create_enrollment(request: Request) -> Response:
        status, form = await mutation_form(request)
        target_kind = _single(form, "target_kind")
        target_principal = _single(form, "target_principal_id", required=False) or None
        invited_email = _single(form, "invited_email_alias", required=False) or None
        if target_kind == "existing_person":
            invited_email = None
        elif target_kind == "new_person":
            target_principal = None
        confirmation = _single(form, "confirmation")
        if confirmation != "Start this enrollment request":
            raise ValidationError("Review and acknowledge the exact enrollment consequence")
        mutation_service.create_enrollment_intent(
            actor=status.actor,
            target_kind=target_kind,
            target_principal_id=target_principal,
            invited_email_alias=invited_email,
            harness_name=_single(form, "harness_name"),
            capabilities=tuple(form.get("capabilities", [])),
            reason=_single(form, "reason"),
            idempotency_key=_single(form, "idempotency_key"),
        )
        return RedirectResponse("/approvals", status_code=303, headers=protected_headers())

    async def revoke_harness(request: Request) -> Response:
        status, form = await mutation_form(request)
        harness_id = request.path_params["harness_id"]
        required_confirmation = f"Remove access for {harness_id}"
        if _single(form, "confirmation") != required_confirmation:
            raise ValidationError(f"Type “{required_confirmation}” to continue")
        mutation_service.request_harness_revocation(
            actor=status.actor,
            target_harness_id=harness_id,
            reason=_single(form, "reason"),
            idempotency_key=_single(form, "idempotency_key"),
        )
        return RedirectResponse("/approvals", status_code=303, headers=protected_headers())
    async def reconcile_mutation(request: Request) -> Response:
        status, form = await mutation_form(request)
        mutation_id = request.path_params["mutation_id"]
        if not 16 <= len(mutation_id) <= 128:
            raise ValidationError("The action identifier is invalid")
        if _single(form, "confirmation") != "Apply this approved action":
            raise ValidationError("Confirm the approved action before applying it")
        mutation_service.reconcile_harness_revocation(
            actor=status.actor,
            mutation_id=mutation_id,
        )
        return RedirectResponse("/approvals", status_code=303, headers=protected_headers())
    async def request_enrollment_approval(request: Request) -> Response:
        status, form = await mutation_form(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        if _single(form, "confirmation") != "Request passkey approval":
            raise ValidationError("Confirm the exact enrollment before requesting approval")
        sponsored_enrollment.request_approval(
            actor=status.actor,
            intent_id=request.path_params["intent_id"],
        )
        return RedirectResponse("/approvals", status_code=303, headers=protected_headers())

    async def reconcile_enrollment(request: Request) -> Response:
        status, form = await mutation_form(request)
        if sponsored_enrollment is None:
            raise GateBlocked("admin_console", "sponsored enrollment is unavailable")
        if _single(form, "confirmation") != "Issue this approved invitation":
            raise ValidationError("Confirm the approved enrollment before issuing it")
        sponsored_enrollment.reconcile(
            actor=status.actor,
            intent_id=request.path_params["intent_id"],
        )
        return RedirectResponse("/approvals", status_code=303, headers=protected_headers())



    def revision_for(domain_id: str) -> int:
        row = sessions.store.fetch_one(
            """SELECT COALESCE(MAX(sequence),0) AS revision FROM audit_log
               WHERE record_json LIKE ?""",
            (f'%"domain_id":"{domain_id}"%',),
        )
        return int(row["revision"]) if row is not None else 0

    async def snapshot(request: Request) -> Response:
        status = session_for(request)
        revision = revision_for(status.actor.domain_id)
        return JSONResponse(
            {
                "schema": "agentnet.console.snapshot.v1",
                "revision": revision,
                "view": "network",
                "state": "changed" if revision > int(request.query_params.get("after", "0")) else "current",
                "changed_ids": [],
                "fresh_at": sessions.clock(),
            },
            headers=protected_headers(),
        )

    async def events(request: Request) -> Response:
        status = session_for(request)
        after_text = request.query_params.get("after", request.headers.get("last-event-id", "0"))
        try:
            after = max(0, int(after_text))
        except ValueError as exc:
            raise ValidationError("event cursor is invalid") from exc

        async def stream():
            revision = revision_for(status.actor.domain_id)
            payload = {
                "revision": revision,
                "view": "network",
                "state": "changed" if revision > after else "current",
                "changed_ids": [],
                "fresh_at": sessions.clock(),
            }
            yield f"id: {revision}\nevent: console\ndata: {json.dumps(payload, separators=(',', ':'))}\n\n"
            await asyncio.sleep(0)

        return StreamingResponse(
            stream(),
            media_type="text/event-stream",
            headers=protected_headers({"X-Accel-Buffering": "no"}),
        )

    async def extension_error(_request: Request, exc: Exception) -> Response:
        assert isinstance(exc, ExtensionError)
        status_code = (
            403
            if isinstance(exc, AuthorizationError)
            else 400
            if isinstance(exc, ValidationError)
            else exc.http_status
        )
        message = "The request could not be completed."
        if isinstance(exc, ValidationError):
            message = html.escape(str(exc))
        return HTMLResponse(
            f'<!doctype html><html lang="en"><meta charset="utf-8"><title>Could not complete</title><main><h1>Could not complete</h1><p>{message}</p><p><a href="/">Return to Home</a></p></main></html>',
            status_code=status_code,
            headers=protected_headers(),
        )

    routes = [
        Route("/health", health, methods=["GET"]),
        Route("/assets/console.css", stylesheet, methods=["GET"]),
        Route("/assets/console.js", script, methods=["GET"]),
        Route("/v1/console/session-challenges", begin_challenge, methods=["POST"]),
        Route(
            "/v1/console/session-challenges/{challenge_id}/complete",
            complete_challenge,
            methods=["POST"],
        ),
        Route("/v1/console/open/{challenge_id}", open_console, methods=["GET"]),
        Route("/v1/console/oidc/callback", oidc_callback, methods=["GET"]),
        Route("/v1/console/sign-out", sign_out, methods=["POST"]),
        Route("/", home, methods=["GET"]),
        Route("/servers", servers_page, methods=["GET"]),
        Route("/people", people_page, methods=["GET"]),
        Route("/approvals", approvals_page, methods=["GET"]),
        Route("/security", security_page, methods=["GET"]),
        Route("/activity", activity_page, methods=["GET"]),
        Route("/enrollments", create_enrollment, methods=["POST"]),
        Route("/harnesses/{harness_id}/revoke", revoke_harness, methods=["POST"]),
        Route("/mutations/{mutation_id}/reconcile", reconcile_mutation, methods=["POST"]),
        Route("/v1/sponsored-enrollment/candidate/begin", sponsored_candidate_begin, methods=["POST"]),
        Route("/v1/sponsored-enrollment/candidate/status", sponsored_candidate_status, methods=["POST"]),
        Route("/enrollments/{intent_id}/request-approval", request_enrollment_approval, methods=["POST"]),
        Route("/enrollments/{intent_id}/reconcile", reconcile_enrollment, methods=["POST"]),
        Route("/v1/console/events", events, methods=["GET"]),
        Route("/v1/console/snapshot", snapshot, methods=["GET"]),
    ]
    return Starlette(routes=routes, exception_handlers={ExtensionError: extension_error})


__all__ = ["PREAUTH_COOKIE", "SESSION_COOKIE", "create_console_app"]
