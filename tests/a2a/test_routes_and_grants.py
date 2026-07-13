from __future__ import annotations

import asyncio
import hashlib
import json

from datetime import UTC, datetime, timedelta
from typing import Any, cast

import pytest
import httpx

from a2a.server.request_handlers import RequestHandler
from a2a.types import (
    AgentCapabilities,
    AgentCard,
    GetTaskRequest,
    Message,
    Part,
    Role,
    SendMessageRequest,
    Task,
    TaskState,
    TaskStatus,
)
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.routing import Mount, Route

from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import (
    AuthorizationError,
    GateBlocked,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.gateways.a2a import (
    A2AGatewayContextBuilder,
    BoundedA2ARequestHandler,
    OpaqueAgentRoute,
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    create_tainted_proposal_handler,
    require_a2a_gateway_mount_capability,
    require_standing_grant,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.a2a_mapping import external_peer_namespace


NOW = datetime(2026, 7, 12, 12, 0, tzinfo=UTC)
ROUTE_TOKEN = "R" * 43
PEER_ID = "authenticated-peer-key-1"
GLOBAL_ADDRESS = "93.184.216.34"
A2A_MOUNT_CONFIG = ExtensionConfig(
    server_agent_capabilities=frozenset({ServerAgentCapability.A2A_GATEWAY})
)

ALL_ACTIONS = frozenset(
    {
        "a2a.task.get",
        "a2a.task.list",
        "a2a.task.cancel",
        "a2a.message.send",
        "a2a.message.stream",
        "a2a.push.create",
        "a2a.push.get",
        "a2a.push.list",
        "a2a.push.delete",
        "a2a.task.subscribe",
        "a2a.card.extended",
    }
)


def route() -> OpaqueAgentRoute:
    return OpaqueAgentRoute(
        route_token=ROUTE_TOKEN,
        logical_agent_id="public-agent-1",
        domain_id="corp.example",
    )


def grant(**updates: Any) -> StandingA2AGrant:
    value = StandingA2AGrant(
        grant_id="grant-1",
        route_token=ROUTE_TOKEN,
        logical_agent_id="public-agent-1",
        allowed_actions=ALL_ACTIONS,
        allowed_resources=frozenset({"public-agent-1"}),
        allowed_output_sinks=frozenset({"public-response"}),
        allowed_peer_namespaces=frozenset({external_peer_namespace(PEER_ID)}),
        expires_at=NOW + timedelta(hours=1),
    )
    return value.model_copy(update=updates)


def template_card() -> AgentCard:
    return AgentCard(
        name="Public agent",
        description="route test",
        version="1",
        capabilities=AgentCapabilities(streaming=True),
    )


def request() -> Request:
    return Request(
        {
            "type": "http",
            "http_version": "1.1",
            "method": "POST",
            "scheme": "https",
            "path": f"/a2a/{ROUTE_TOKEN}/message:send",
            "raw_path": f"/a2a/{ROUTE_TOKEN}/message:send".encode(),
            "query_string": b"",
            "headers": [(b"a2a-version", b"1.0")],
            "client": ("203.0.113.10", 50000),
            "server": ("agents.example", 443),
        }
    )


def test_standing_grant_fails_closed_on_revoke_expiry_and_scope() -> None:
    active = grant()
    assert require_standing_grant(
        active,
        route=route(),
        now=NOW,
        peer_namespace=external_peer_namespace(PEER_ID),
        action="a2a.message.send",
        resource="public-agent-1",
    ).grant_id == "grant-1"

    with pytest.raises(AuthorizationError):
        require_standing_grant(active.model_copy(update={"revoked_at": NOW}), route=route(), now=NOW)
    with pytest.raises(AuthorizationError):
        require_standing_grant(active, route=route(), now=NOW + timedelta(hours=2))
    with pytest.raises(AuthorizationError):
        require_standing_grant(
            active,
            route=route(),
            now=NOW,
            action="a2a.not.allowed",
        )
    with pytest.raises(AuthorizationError):
        require_standing_grant(
            active,
            route=route(),
            now=NOW,
            peer_namespace=external_peer_namespace("different-peer"),
        )


def test_context_builder_can_only_create_external_unverified_actor() -> None:
    state = {"grant": grant()}
    builder = A2AGatewayContextBuilder(
        route=route(),
        grant_lookup=lambda token: state["grant"] if token == ROUTE_TOKEN else None,
        peer_resolver=lambda incoming: PEER_ID,
        clock=lambda: NOW,
    )
    context = builder.build(request())
    actor = context.state["verified_actor"]
    assert isinstance(actor, VerifiedActor)
    assert actor.kind is ActorKind.EXTERNAL_A2A
    assert actor.external_peer_id == external_peer_namespace(PEER_ID)
    assert actor.principal_id is None
    assert actor.harness_id is None
    assert actor.credential_id is None
    assert actor.positive_authority_id is None
    assert context.tenant == ROUTE_TOKEN

    state["grant"] = grant(revoked_at=NOW)
    with pytest.raises(AuthorizationError):
        builder.build(request())


class _TaskDelegate:
    async def on_get_task(self, params: GetTaskRequest, context: Any) -> Task:
        return Task(
            id=params.id,
            context_id="context-1",
            status=TaskStatus(state=TaskState.TASK_STATE_WORKING),
        )


def test_handler_rechecks_revocation_for_each_operation() -> None:
    state = {"grant": grant()}
    lookup = lambda token: state["grant"] if token == ROUTE_TOKEN else None
    builder = A2AGatewayContextBuilder(
        route=route(),
        grant_lookup=lookup,
        peer_resolver=lambda incoming: PEER_ID,
        clock=lambda: NOW,
    )
    context = builder.build(request())
    handler = BoundedA2ARequestHandler(
        cast("RequestHandler", _TaskDelegate()),
        route=route(),
        grant_lookup=lookup,
        url_validator=lambda url: url,
        clock=lambda: NOW,
    )
    params = GetTaskRequest(tenant=ROUTE_TOKEN, id="task-1")
    result = asyncio.run(handler.on_get_task(params, context))
    assert result is not None and result.id == "task-1"

    state["grant"] = grant(revoked_at=NOW)
    with pytest.raises(AuthorizationError):
        asyncio.run(handler.on_get_task(params, context))


def test_sdk_route_builder_is_fixed_v1_and_card_revocation_hides_route() -> None:
    current = {"grant": grant()}
    lookup = lambda token: current["grant"] if token == ROUTE_TOKEN else None
    exported_card = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    proposal_handler = create_tainted_proposal_handler(exported_card)
    routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=proposal_handler,
        agent_card=exported_card,
        route=route(),
        grant_lookup=lookup,
        peer_resolver=lambda incoming: PEER_ID,
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_ADDRESS,),
        clock=lambda: NOW,
    )

    assert routes
    assert not any(isinstance(candidate, Mount) for candidate in routes)
    paths = {candidate.path for candidate in routes if isinstance(candidate, Route)}
    assert f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json" in paths
    assert f"/a2a/{ROUTE_TOKEN}/message:send" in paths
    assert f"/a2a/{ROUTE_TOKEN}/message:stream" in paths
    assert f"/a2a/{ROUTE_TOKEN}/rpc" in paths
    assert all("{tenant}" not in path and "0.3" not in path for path in paths)

    async def exercise_routes() -> None:
        transport = httpx.ASGITransport(
            app=Starlette(routes=routes),
            raise_app_exceptions=False,
        )
        async with httpx.AsyncClient(transport=transport, base_url="https://agents.example") as client:
            card_response = await client.get(f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json")
            assert card_response.status_code == 200
            card_payload = card_response.json()
            assert card_payload["defaultInputModes"] == ["text/plain"]
            assert card_payload["defaultOutputModes"] == ["text/plain"]
            assert card_payload["skills"][0]["id"] == "agentnet-tainted-proposal-ingress"
            assert card_response.headers["cache-control"] == (
                "public, max-age=300, must-revalidate, no-transform"
            )
            etag = card_response.headers["etag"]
            assert etag == f'"{hashlib.sha256(card_response.content).hexdigest()}"'
            assert card_response.headers["last-modified"] == "Sun, 12 Jul 2026 12:00:00 GMT"

            unchanged_by_etag = await client.get(
                f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json",
                headers={"If-None-Match": etag},
            )
            assert unchanged_by_etag.status_code == 304
            assert unchanged_by_etag.content == b""
            assert unchanged_by_etag.headers["etag"] == etag
            assert unchanged_by_etag.headers["cache-control"] == (
                "public, max-age=300, must-revalidate, no-transform"
            )
            assert unchanged_by_etag.headers["last-modified"] == (
                "Sun, 12 Jul 2026 12:00:00 GMT"
            )

            unchanged_by_weak_etag = await client.get(
                f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json",
                headers={"If-None-Match": f'"unrelated", W/{etag}'},
            )
            assert unchanged_by_weak_etag.status_code == 304

            unchanged_by_date = await client.get(
                f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json",
                headers={"If-Modified-Since": card_response.headers["last-modified"]},
            )
            assert unchanged_by_date.status_code == 304

            etag_precedes_date = await client.get(
                f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json",
                headers={
                    "If-None-Match": '"different-representation"',
                    "If-Modified-Since": card_response.headers["last-modified"],
                },
            )
            assert etag_precedes_date.status_code == 200
            assert etag_precedes_date.headers["etag"] == etag

            missing = await client.post(f"/a2a/{ROUTE_TOKEN}/message:send", json={})
            legacy = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                json={},
                headers={"A2A-Version": "0.3"},
            )
            non_exact = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                json={},
                headers={"A2A-Version": "1.1"},
            )
            wrong_media = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                content=b"{}",
                headers={"A2A-Version": "1.0", "Content-Type": "text/plain"},
            )
            assert missing.status_code == 400
            assert legacy.status_code == 400
            assert non_exact.status_code == 400
            assert wrong_media.status_code == 415
            assert missing.json()["error"] == {
                "code": 400,
                "status": "FAILED_PRECONDITION",
                "message": "A2A-Version must be exactly 1.0",
                "details": [
                    {
                        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                        "reason": "VERSION_NOT_SUPPORTED",
                        "domain": "a2a-protocol.org",
                        "metadata": {},
                    }
                ],
            }
            assert wrong_media.json()["error"]["code"] == 415
            assert wrong_media.json()["error"]["details"][0]["reason"] == (
                "CONTENT_TYPE_NOT_SUPPORTED"
            )

            rpc_envelope = {
                "jsonrpc": "2.0",
                "id": "rpc-boundary-1",
                "method": "SendMessage",
                "params": {},
            }
            rpc_version = await client.post(
                f"/a2a/{ROUTE_TOKEN}/rpc",
                json=rpc_envelope,
                headers={"A2A-Version": "1.1"},
            )
            rpc_media = await client.post(
                f"/a2a/{ROUTE_TOKEN}/rpc",
                content=json.dumps(rpc_envelope),
                headers={"A2A-Version": "1.0", "Content-Type": "text/plain"},
            )
            assert rpc_version.status_code == 200
            assert rpc_version.json()["id"] == "rpc-boundary-1"
            assert rpc_version.json()["error"]["code"] == -32009
            assert rpc_media.status_code == 200
            assert rpc_media.json()["id"] == "rpc-boundary-1"
            assert rpc_media.json()["error"]["code"] == -32005

            inbound = SendMessageRequest(
                tenant=ROUTE_TOKEN,
                message=Message(
                    message_id="external-message-1",
                    context_id="external-context-1",
                    role=Role.ROLE_USER,
                    parts=[Part(text="hostile content remains tainted")],
                ),
            )
            accepted_as_proposal = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                json=MessageToDict(inbound),
                headers={"A2A-Version": "1.0"},
            )
            assert accepted_as_proposal.status_code == 200
            task_payload = accepted_as_proposal.json()["task"]
            assert task_payload["status"]["state"] == "TASK_STATE_SUBMITTED"
            assert task_payload["metadata"]["agentnetDisposition"] == "tainted_non_executable_proposal"
            assert task_payload["metadata"]["agentnetAuthorityEligible"] is False
            assert task_payload["metadata"]["agentnetEffectAuthorized"] is False

            unsupported_raw = SendMessageRequest(
                tenant=ROUTE_TOKEN,
                message=Message(
                    message_id="external-raw-message-1",
                    context_id="external-raw-context-1",
                    role=Role.ROLE_USER,
                    parts=[Part(raw=b"PK\x03\x04", media_type="application/zip")],
                ),
            )
            unsupported_response = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                json=MessageToDict(unsupported_raw),
                headers={"A2A-Version": "1.0"},
            )
            assert unsupported_response.status_code == 415
            assert unsupported_response.json()["error"]["code"] == 415
            assert unsupported_response.json()["error"]["details"][0]["reason"] == (
                "CONTENT_TYPE_NOT_SUPPORTED"
            )

            fetched = await client.get(
                f"/a2a/{ROUTE_TOKEN}/tasks/{task_payload['id']}",
                headers={"A2A-Version": "1.0"},
            )
            assert fetched.status_code == 200
            assert fetched.json()["id"] == task_payload["id"]

            jsonrpc_response = await client.post(
                f"/a2a/{ROUTE_TOKEN}/rpc",
                json={
                    "jsonrpc": "2.0",
                    "id": "rpc-1",
                    "method": "SendMessage",
                    "params": MessageToDict(inbound),
                },
                headers={"A2A-Version": "1.0"},
            )
            assert jsonrpc_response.status_code == 200
            jsonrpc_payload = jsonrpc_response.json()
            assert jsonrpc_payload["jsonrpc"] == "2.0"
            assert jsonrpc_payload["id"] == "rpc-1"
            assert jsonrpc_payload["result"]["task"]["status"]["state"] == "TASK_STATE_SUBMITTED"
            assert (
                jsonrpc_payload["result"]["task"]["metadata"]["agentnetDisposition"]
                == "tainted_non_executable_proposal"
            )

            current["grant"] = grant(revoked_at=NOW)
            hidden = await client.get(
                f"/a2a/{ROUTE_TOKEN}/.well-known/agent-card.json",
                headers={"If-None-Match": etag},
            )
            assert hidden.status_code == 404
            assert hidden.headers["cache-control"] == "no-store"
            assert "etag" not in hidden.headers
            assert "last-modified" not in hidden.headers

    asyncio.run(exercise_routes())


@pytest.mark.parametrize(
    ("failure", "reason"),
    [
        (ValidationError("invalid protocol projection"), "INVALID_PARAMS"),
        (IdempotencyConflict("digest mismatch"), "INVALID_REQUEST"),
    ],
)
def test_internal_validation_and_idempotency_failures_are_native_a2a_errors(
    failure: Exception,
    reason: str,
) -> None:
    class FailingDelegate:
        async def on_message_send(self, params: Any, context: Any) -> Any:
            del params, context
            raise failure

    exported_card = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=cast("RequestHandler", FailingDelegate()),
        agent_card=exported_card,
        route=route(),
        grant_lookup=lambda token: grant() if token == ROUTE_TOKEN else None,
        peer_resolver=lambda incoming: PEER_ID,
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_ADDRESS,),
        clock=lambda: NOW,
    )
    inbound = SendMessageRequest(
        tenant=ROUTE_TOKEN,
        message=Message(
            message_id="failing-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="proposal")],
        ),
    )

    async def exercise() -> None:
        transport = httpx.ASGITransport(
            app=Starlette(routes=routes),
            raise_app_exceptions=False,
        )
        async with httpx.AsyncClient(
            transport=transport,
            base_url="https://agents.example",
        ) as client:
            response = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:send",
                json=MessageToDict(inbound),
                headers={"A2A-Version": "1.0"},
            )
        assert response.status_code == 400, response.text
        assert response.json()["error"]["code"] == 400
        assert response.json()["error"]["details"][0]["reason"] == reason

    asyncio.run(exercise())


def test_disabled_streaming_and_push_fail_before_standing_grant_scope_checks() -> None:
    disabled_card = AgentCard(
        name="Public agent",
        description="disabled capability test",
        version="1",
        capabilities=AgentCapabilities(streaming=False, push_notifications=False),
    )
    exported_card = build_exported_agent_card(
        disabled_card,
        route=route(),
        public_base_url="https://agents.example",
    )
    scoped_grant = grant(
        allowed_actions=frozenset({"a2a.message.send"}),
    )
    routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=create_tainted_proposal_handler(exported_card),
        agent_card=exported_card,
        route=route(),
        grant_lookup=lambda token: scoped_grant if token == ROUTE_TOKEN else None,
        peer_resolver=lambda incoming: PEER_ID,
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_ADDRESS,),
        clock=lambda: NOW,
    )
    inbound = SendMessageRequest(
        tenant=ROUTE_TOKEN,
        message=Message(
            message_id="disabled-capability-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="proposal")],
        ),
    )

    async def exercise() -> None:
        transport = httpx.ASGITransport(
            app=Starlette(routes=routes),
            raise_app_exceptions=False,
        )
        async with httpx.AsyncClient(
            transport=transport,
            base_url="https://agents.example",
        ) as client:
            stream = await client.post(
                f"/a2a/{ROUTE_TOKEN}/message:stream",
                json=MessageToDict(inbound),
                headers={"A2A-Version": "1.0"},
            )
            push = await client.post(
                f"/a2a/{ROUTE_TOKEN}/tasks/task-1/pushNotificationConfigs",
                json={"url": "https://callbacks.example/notify"},
                headers={"A2A-Version": "1.0"},
            )

        assert stream.status_code == 400, stream.text
        assert stream.json()["error"]["details"][0]["reason"] == "UNSUPPORTED_OPERATION"
        assert push.status_code == 400, push.text
        assert push.json()["error"]["details"][0]["reason"] == (
            "PUSH_NOTIFICATION_NOT_SUPPORTED"
        )

    asyncio.run(exercise())


def test_a2a_mount_requires_explicit_server_agent_attenuation() -> None:
    with pytest.raises(GateBlocked, match="not attenuated") as blocked:
        require_a2a_gateway_mount_capability(
            ExtensionConfig(server_agent_capabilities=frozenset())
        )

    assert blocked.value.gate == "a2a_gateway_capability"
    require_a2a_gateway_mount_capability(A2A_MOUNT_CONFIG)


def test_inert_handler_can_return_direct_message_without_synthesizing_task() -> None:
    state = {"grant": grant()}
    builder = A2AGatewayContextBuilder(
        route=route(),
        grant_lookup=lambda token: state["grant"] if token == ROUTE_TOKEN else None,
        peer_resolver=lambda incoming: PEER_ID,
        clock=lambda: NOW,
    )
    context = builder.build(request())
    exported_card = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    handler = create_tainted_proposal_handler(exported_card, response_mode="message")
    response = asyncio.run(
        handler.on_message_send(
            SendMessageRequest(
                tenant=ROUTE_TOKEN,
                message=Message(
                    message_id="external-message-2",
                    role=Role.ROLE_USER,
                    parts=[Part(text="proposal")],
                ),
            ),
            context,
        )
    )
    assert isinstance(response, Message)
    assert response.task_id == ""
    assert response.role == Role.ROLE_AGENT
    assert response.metadata["agentnetDisposition"] == "tainted_non_executable_proposal"
