"""Authenticated self-hosted HTTP API for the corporate core."""

from __future__ import annotations

import asyncio
import json
from asyncio import CancelledError as AsyncCancelledError
from concurrent.futures import CancelledError as FutureCancelledError
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError
from starlette.applications import Starlette
from starlette.datastructures import MutableHeaders
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authority_bootstrap_http import create_authority_bootstrap_routes
from agentnet.bindings.composition import (
    LocalBindingService,
    create_local_binding_service,
)
from agentnet.core.app import CommunicationCore
from agentnet.enrollment_http import create_enrollment_routes
from agentnet.errors import ExtensionError, ValidationError
from agentnet.federation_http import create_federation_routes
from agentnet.gateways.a2a_service import (
    PersistentA2AService,
    create_persistent_a2a_service,
)
from agentnet.identity_admin_http import create_identity_admin_routes
from agentnet.invitation_http import create_internal_invitation_routes
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator
from agentnet.organization.assignment import AssignmentRequest
from agentnet.organization.conflicts import TaskExecutionIntent
from agentnet.protocol.models import Classification, ReleasedArtifactBinding
from agentnet.product_http import RELATIONSHIP_RESPONSE_HEADERS, create_product_routes
from agentnet.relay.http import create_relay_routes
from agentnet.security.dpop import proof_from_headers
from agentnet.supervisor_http import create_supervisor_routes


PROTECTED_RESPONSE_HEADERS = {
    **RELATIONSHIP_RESPONSE_HEADERS,
    "Cross-Origin-Resource-Policy": "same-origin",
    "X-Frame-Options": "DENY",
}


class ProtectedResponseHeadersMiddleware:
    """Apply privacy headers without buffering request or response streams.

    Starlette's ``BaseHTTPMiddleware`` runs the downstream application in a
    task group and proxies the body through an in-memory stream. That is a
    poor fit for cancellation-sensitive endpoints and long-lived response
    bodies. This small ASGI middleware only edits ``http.response.start`` and
    otherwise preserves the server's native cancellation semantics.
    """

    def __init__(self, app: Any) -> None:
        self.app = app

    async def __call__(self, scope: dict[str, Any], receive: Any, send: Any) -> None:
        if scope.get("type") != "http" or not scope.get("path", "").startswith("/v1/"):
            await self.app(scope, receive, send)
            return

        async def protected_send(message: dict[str, Any]) -> None:
            if message.get("type") == "http.response.start":
                headers = MutableHeaders(scope=message)
                for name, value in PROTECTED_RESPONSE_HEADERS.items():
                    headers[name] = value
            await send(message)

        await self.app(scope, receive, protected_send)


class MessageBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()
    conversation_id: str | None = None
    room_id: str | None = None
    expected_room_control_sequence: int | None = Field(default=None, ge=1)


class AssignmentBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    recipient_harness_id: str
    task_type: str
    resources: frozenset[str]
    data_classes: frozenset[Classification]
    tools: frozenset[str] = frozenset()
    budget: int = 0
    concurrency: int = 1
    deadline: datetime | None = None
    expected_relationship_revision: int | None = None
    intent: TaskExecutionIntent | None = None
    task_payload: dict[str, Any]
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()
    idempotency_key: str = Field(min_length=16, max_length=256)


class ConversationCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    conversation_id: str = Field(min_length=1, max_length=256)
    member_harness_ids: tuple[str, ...] = Field(min_length=1, max_length=999)
    classification: Classification = Classification.C1_INTERNAL


class ConversationActionBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    thread_id: str = Field(min_length=1, max_length=256)
    action: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)


class ObligationTransitionBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    to_state: str = Field(min_length=1, max_length=64)
    reason: str = Field(default="recipient_update", min_length=1, max_length=128)
    expected_revision: int | None = Field(default=None, ge=1)


class ObligationCancelBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    reason_code: str = Field(default="requester_canceled", min_length=1, max_length=128)
    expected_revision: int | None = Field(default=None, ge=1)


class ObligationReconcileBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    limit: int = Field(default=100, ge=1, le=1000)


class TaskProposalDecisionBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    request_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    revision: int = Field(ge=1)


class TaskProposalDenialBody(TaskProposalDecisionBody):
    reason_code: str = Field(min_length=1, max_length=128, pattern=r"^[A-Za-z0-9_]+$")


class TaskProposalReauthorizationBody(TaskProposalDecisionBody):
    relationship_revision: int = Field(ge=1)


async def _body_and_actor(request: Request, core: CommunicationCore) -> tuple[bytes, Any]:
    body = await request.body()
    if len(body) > core.config.max_request_bytes:
        raise ValidationError("request body exceeds configured limit")
    proof = proof_from_headers(dict(request.headers))
    try:
        raw_path = request.scope.get("raw_path", request.url.path.encode("ascii")).decode("ascii")
        raw_query = request.scope.get("query_string", b"").decode("ascii")
    except UnicodeError as exc:
        raise ValidationError("request target must use canonical ASCII encoding") from exc
    authority = request.headers.get("host")
    if not authority:
        raise ValidationError("request authority is required")
    context = core.authenticate(
        proof,
        method=request.method,
        scheme=request.scope.get("scheme", ""),
        authority=authority,
        path=raw_path,
        query=raw_query,
        body=body,
        caller_claims=None,
    )
    # Private ASGI state: only the proof verifier may populate this value.
    # Inspection handlers consume the full transport binding so request JSON
    # can never select another principal, guest, harness, or domain.
    request.scope["agentnet.trusted_transport"] = context
    return body, context.actor


def create_app(core: CommunicationCore) -> Starlette:
    a2a_service: PersistentA2AService | None = None
    if core.config.features.public_a2a:
        a2a_service = create_persistent_a2a_service(core)
    local_binding_service: LocalBindingService | None = None
    if core.config.features.local_bindings:
        local_binding_service = create_local_binding_service(core)

    async def health(_request: Request) -> Response:
        return JSONResponse(core.liveness())

    async def ready(_request: Request) -> Response:
        status = core.readiness()
        return JSONResponse(status, status_code=200 if status["ready"] else 503)

    async def recovery(_request: Request) -> Response:
        status = core.recovery_status(record_observation=False)
        return JSONResponse(status, status_code=200 if status["ready"] else 503)

    async def send_message(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = MessageBody.model_validate_json(body)
        result = core.send_message(
            actor=actor,
            recipients=parsed.recipients,
            payload=parsed.payload,
            idempotency_key=parsed.idempotency_key,
            classification=parsed.classification,
            released_artifacts=parsed.released_artifacts,
            conversation_id=parsed.conversation_id,
            room_id=parsed.room_id,
            expected_room_control_sequence=parsed.expected_room_control_sequence,
        )
        return JSONResponse(result, status_code=202)

    async def mailbox(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        try:
            after = int(request.query_params.get("after", "0"))
            limit = int(request.query_params.get("limit", "100"))
        except ValueError as exc:
            raise ValidationError("mailbox cursor/limit must be integers") from exc
        return JSONResponse({"items": core.mailbox(actor=actor, after_cursor=after, limit=limit)})

    async def mailbox_watch(request: Request) -> Response:
        """Authenticated resumable watch that emits authority-free wake hints.

        The hint intentionally contains no event identifier, sender, type, or
        payload.  It never authorizes delivery: clients must perform a fresh,
        signed cursor reconciliation and durably enqueue the returned item
        before advancing their local cursor.
        """

        _body, actor = await _body_and_actor(request, core)
        query_items = request.query_params.multi_items()
        query_keys = [key for key, _value in query_items]
        if (
            any(key not in {"after", "wait_ms"} for key in query_keys)
            or len(query_keys) != len(set(query_keys))
        ):
            raise ValidationError("mailbox watch query schema is invalid")
        raw_after = request.query_params.get("after", "0")
        raw_wait_ms = request.query_params.get("wait_ms", "5000")
        if any(
            not value.isascii()
            or not value.isdigit()
            or (len(value) > 1 and value.startswith("0"))
            for value in (raw_after, raw_wait_ms)
        ):
            raise ValidationError("mailbox watch cursor/wait must be canonical integers")
        try:
            after = int(raw_after)
            wait_ms = int(raw_wait_ms)
        except ValueError as exc:
            raise ValidationError("mailbox watch cursor/wait must be integers") from exc
        if after < 0 or not 50 <= wait_ms <= 30_000:
            raise ValidationError("mailbox watch cursor/wait is outside the bounded profile")

        recipient_id = actor.harness_id
        if not recipient_id:
            raise ValidationError("mailbox watch requires an exact recipient harness")

        loop = asyncio.get_running_loop()
        wake = asyncio.Event()
        subscription_id = core.mailboxes.subscribe_content_free_wake(
            recipient_id,
            lambda: loop.call_soon_threadsafe(wake.set)
        )
        try:
            # Register-before-read closes the query-vs-subscribe race.
            items = core.mailbox(actor=actor, after_cursor=after, limit=1)
            if not items:
                try:
                    await asyncio.wait_for(wake.wait(), timeout=wait_ms / 1_000)
                except TimeoutError:
                    pass
                # The wake is never authorization.  Re-run current policy and
                # credential checks, then derive the hint only from durable state.
                items = core.mailbox(actor=actor, after_cursor=after, limit=1)
        finally:
            core.mailboxes.unsubscribe_content_free_wake(subscription_id)

        kind = "wake" if items else "idle"
        cursor_hint = items[0].get("cursor") if items else after
        if type(cursor_hint) is not int or (kind == "wake" and cursor_hint <= after):
            raise ValidationError("mailbox watch observed an invalid cursor")
        return JSONResponse(
            {
                "schema": "agentnet.mailbox-wake.v1",
                "kind": kind,
                "cursor_hint": cursor_hint,
            },
            headers={"Cache-Control": "no-store", "Pragma": "no-cache"},
        )

    async def assign_task(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = AssignmentBody.model_validate_json(body)
        assignment = AssignmentRequest(
            actor=actor,
            recipient_harness_id=parsed.recipient_harness_id,
            task_type=parsed.task_type,
            resources=parsed.resources,
            data_classes=parsed.data_classes,
            tools=parsed.tools,
            budget=parsed.budget,
            concurrency=parsed.concurrency,
            deadline=parsed.deadline,
            expected_relationship_revision=parsed.expected_relationship_revision,
            policy_revision=core.policy.current_policy_revision(actor),
            intent=parsed.intent,
        )
        result = core.assign_task(
            assignment,
            payload=parsed.task_payload,
            idempotency_key=parsed.idempotency_key,
            released_artifacts=parsed.released_artifacts,
        )
        return JSONResponse(result, status_code=202)

    async def create_conversation(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = ConversationCreateBody.model_validate_json(body)
        result = core.create_conversation(
            actor=actor,
            conversation_id=parsed.conversation_id,
            member_harness_ids=parsed.member_harness_ids,
            classification=parsed.classification,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def post_conversation_action(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = ConversationActionBody.model_validate_json(body)
        result = core.post_conversation_action(
            actor=actor,
            recipients=parsed.recipients,
            conversation_id=request.path_params["conversation_id"],
            thread_id=parsed.thread_id,
            action=parsed.action,
            idempotency_key=parsed.idempotency_key,
        )
        return JSONResponse(result, status_code=202)

    async def conversation_thread(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        try:
            limit = int(request.query_params.get("limit", "100"))
        except ValueError as exc:
            raise ValidationError("conversation thread limit must be an integer") from exc
        return JSONResponse(
            {
                "items": core.conversation_thread(
                    actor=actor,
                    conversation_id=request.path_params["conversation_id"],
                    thread_id=request.path_params["thread_id"],
                    limit=limit,
                )
            }
        )

    async def response_obligation_inbox(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        return JSONResponse(core.response_obligation_inbox(actor=actor))

    async def response_obligation_reconcile(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = ObligationReconcileBody.model_validate_json(body or b"{}")
        return JSONResponse(core.response_obligation_reconcile(actor=actor, limit=parsed.limit))

    async def response_obligation_list(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        role = request.query_params.get("role", "any")
        states = tuple(
            value for value in request.query_params.getlist("state") if value
        )
        try:
            limit = int(request.query_params.get("limit", "100"))
        except ValueError as exc:
            raise ValidationError("obligation list limit must be an integer") from exc
        return JSONResponse(
            {
                "items": core.response_obligation_list(
                    actor=actor,
                    role=role,
                    states=states,
                    limit=limit,
                )
            }
        )

    async def response_obligation_get(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        return JSONResponse(
            core.response_obligation(
                actor=actor,
                obligation_id=request.path_params["obligation_id"],
            )
        )

    async def response_obligation_transition(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = ObligationTransitionBody.model_validate_json(body)
        return JSONResponse(
            core.response_obligation_transition(
                actor=actor,
                obligation_id=request.path_params["obligation_id"],
                to_state=parsed.to_state,
                reason=parsed.reason,
                expected_revision=parsed.expected_revision,
            )
        )

    async def response_obligation_cancel(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = ObligationCancelBody.model_validate_json(body)
        return JSONResponse(
            core.response_obligation_cancel(
                actor=actor,
                obligation_id=request.path_params["obligation_id"],
                reason_code=parsed.reason_code,
                expected_revision=parsed.expected_revision,
            )
        )

    async def task_proposals(request: Request) -> Response:
        _body, actor = await _body_and_actor(request, core)
        try:
            limit = int(request.query_params.get("limit", "100"))
        except ValueError as exc:
            raise ValidationError("task proposal limit must be an integer") from exc
        return JSONResponse({"items": core.task_proposals(actor=actor, limit=limit)})

    async def approve_task_proposal(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = TaskProposalDecisionBody.model_validate_json(body)
        return JSONResponse(
            core.approve_task_proposal(
                actor=actor,
                proposal_id=request.path_params["proposal_id"],
                request_digest=parsed.request_digest,
                revision=parsed.revision,
            )
        )

    async def deny_task_proposal(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = TaskProposalDenialBody.model_validate_json(body)
        return JSONResponse(
            core.deny_task_proposal(
                actor=actor,
                proposal_id=request.path_params["proposal_id"],
                request_digest=parsed.request_digest,
                revision=parsed.revision,
                reason_code=parsed.reason_code,
            )
        )

    async def reauthorize_task_proposal(request: Request) -> Response:
        body, actor = await _body_and_actor(request, core)
        parsed = TaskProposalReauthorizationBody.model_validate_json(body)
        return JSONResponse(
            core.reauthorize_task_proposal(
                actor=actor,
                proposal_id=request.path_params["proposal_id"],
                request_digest=parsed.request_digest,
                revision=parsed.revision,
                relationship_revision=parsed.relationship_revision,
            )
        )

    async def exception_handler(request: Request, exc: Exception) -> Response:
        if isinstance(exc, (AsyncCancelledError, FutureCancelledError)):
            # Shutdown and request cancellation are control flow, not a 500
            # response.  Re-raise so the server can cancel the task promptly.
            raise exc
        headers = (
            PROTECTED_RESPONSE_HEADERS
            if request.url.path.startswith("/v1/")
            else None
        )
        if isinstance(exc, ExtensionError):
            return JSONResponse(exc.public_detail(), status_code=exc.http_status, headers=headers)
        if isinstance(exc, (PydanticValidationError, json.JSONDecodeError)):
            return JSONResponse(
                {"code": "invalid_request", "message": "request validation failed"},
                status_code=422,
                headers=headers,
            )
        return JSONResponse(
            {"code": "internal_error", "message": "request could not be processed"},
            status_code=500,
            headers=headers,
        )

    routes = [
        Route("/healthz", health, methods=["GET"]),
        Route("/readyz", ready, methods=["GET"]),
        Route("/recoveryz", recovery, methods=["GET"]),
        Route("/v1/messages", send_message, methods=["POST"]),
        Route("/v1/mailbox", mailbox, methods=["GET"]),
        Route("/v1/mailbox/watch", mailbox_watch, methods=["GET"]),
        Route("/v1/tasks/assign", assign_task, methods=["POST"]),
        Route("/v1/task-proposals", task_proposals, methods=["GET"]),
        Route("/v1/task-proposals/{proposal_id}/approve", approve_task_proposal, methods=["POST"]),
        Route("/v1/task-proposals/{proposal_id}/deny", deny_task_proposal, methods=["POST"]),
        Route(
            "/v1/task-proposals/{proposal_id}/reauthorize",
            reauthorize_task_proposal,
            methods=["POST"],
        ),
        Route("/v1/response-obligations", response_obligation_list, methods=["GET"]),
        Route("/v1/response-obligations/inbox", response_obligation_inbox, methods=["GET"]),
        Route("/v1/response-obligations/reconcile", response_obligation_reconcile, methods=["POST"]),
        Route("/v1/response-obligations/{obligation_id}", response_obligation_get, methods=["GET"]),
        Route(
            "/v1/response-obligations/{obligation_id}/transition",
            response_obligation_transition,
            methods=["POST"],
        ),
        Route(
            "/v1/response-obligations/{obligation_id}/cancel",
            response_obligation_cancel,
            methods=["POST"],
        ),
        Route("/v1/conversations", create_conversation, methods=["POST"]),
        Route("/v1/conversations/{conversation_id}/actions", post_conversation_action, methods=["POST"]),
        Route(
            "/v1/conversations/{conversation_id}/threads/{thread_id}",
            conversation_thread,
            methods=["GET"],
        ),
    ]
    if core.oidc_enrollment is not None:
        enrollment_verifier = getattr(core.oidc_enrollment.enrollment, "approval_verifier", None)
        if isinstance(enrollment_verifier, IndependentApprovalVerifier):
            provider = getattr(core.oidc_enrollment, "provider", None)
            recovery_coordinator = (
                OIDCCredentialRecoveryCoordinator(
                    core.store,
                    provider,
                    core.create_recovery_service(enrollment_verifier),
                )
                if provider is not None
                else None
            )
            routes.extend(
                create_enrollment_routes(
                    core,
                    core.oidc_enrollment,
                    recovery_coordinator=recovery_coordinator,
                )
            )
            routes.extend(create_authority_bootstrap_routes(core, _body_and_actor))
            routes.extend(
                create_identity_admin_routes(
                    core,
                    _body_and_actor,
                    enrollment_verifier,
                    recovery_coordinator=recovery_coordinator,
                )
            )
        else:
            routes.extend(create_enrollment_routes(core, core.oidc_enrollment))
    routes.extend(create_product_routes(core, _body_and_actor))
    routes.extend(create_internal_invitation_routes(core, _body_and_actor))
    routes.extend(
        create_supervisor_routes(
            core,
            _body_and_actor,
            local_binding_service=local_binding_service,
        )
    )
    if core.config.features.federation:
        routes.extend(create_federation_routes(core, _body_and_actor))
    if core.relay_service is not None:
        routes.extend(
            create_relay_routes(
                core.relay_service,
                max_request_bytes=core.config.max_request_bytes,
            )
        )
    if a2a_service is not None:
        routes.extend(a2a_service.routes)

    @asynccontextmanager
    async def lifespan(_app: Starlette):
        try:
            if local_binding_service is not None:
                await local_binding_service.start()
            yield
        finally:
            if local_binding_service is not None:
                await local_binding_service.close()
            if a2a_service is not None:
                await a2a_service.close()

    app = Starlette(
        debug=False,
        routes=routes,
        exception_handlers={Exception: exception_handler},
        lifespan=lifespan,
    )

    app.add_middleware(ProtectedResponseHeadersMiddleware)

    app.state.a2a_service = a2a_service
    app.state.local_binding_service = local_binding_service
    app.state.relay_service = core.relay_service
    return app
