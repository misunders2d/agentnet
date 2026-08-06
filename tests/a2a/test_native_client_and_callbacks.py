from __future__ import annotations

import asyncio
import time

from datetime import UTC, datetime, timedelta
from typing import Any
from uuid import uuid4

import httpx
import pytest

from a2a.client import ClientCallContext
from a2a.server.context import ServerCallContext
from a2a.types import (
    AgentCapabilities,
    AgentCard,
    GetTaskRequest,
    Message,
    Part,
    Role,
    SendMessageRequest,
    StreamResponse,
    Task,
    TaskPushNotificationConfig,
    TaskState,
    TaskStatus,
    TaskStatusUpdateEvent,
)
from starlette.applications import Starlette

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScope,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import ValidationError
from agentnet.gateways.a2a import (
    OpaqueAgentRoute,
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    corporate_peer_namespace,
    validate_outbound_url,
)
from agentnet.gateways.a2a_client import (
    CorporateA2AClientIdentity,
    NativeA2AClient,
    create_native_a2a_client,
    create_pinned_callback_sender,
)
from agentnet.gateways.a2a_runtime import (
    DurableA2ARuntime,
    SignedCorporateA2AAuthenticator,
    corporate_input_source,
    corporate_output_sink,
)
from agentnet.identity.context import VerifiedContextResolver
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.a2a_mapping import A2AMappedKind
from agentnet.protocol.models import Classification, DeliveryFact, TaskGrant
from agentnet.security.dpop import proof_from_headers, verify_request_proof


TENANT = "T" * 43
GLOBAL_IP = "93.184.216.34"
AUDIENCE = "urn:agentnet:corp.example:a2a"
A2A_MOUNT_CONFIG = ExtensionConfig(
    server_agent_capabilities=frozenset({ServerAgentCapability.A2A_GATEWAY})
)


class PollingSDK:
    def __init__(self) -> None:
        self.polls = 0
        self.sends = 0

    async def send_message(
        self,
        request: SendMessageRequest,
        *,
        context: ClientCallContext | None = None,
    ):
        del context
        self.sends += 1
        yield StreamResponse(
            task=Task(
                id="remote-task-1",
                context_id=request.message.context_id,
                status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED),
                history=[request.message],
            )
        )

    async def get_task(
        self,
        request: GetTaskRequest,
        *,
        context: ClientCallContext | None = None,
    ) -> Task:
        del context
        self.polls += 1
        state = TaskState.TASK_STATE_WORKING if self.polls == 1 else TaskState.TASK_STATE_COMPLETED
        return Task(
            id=request.id,
            context_id="remote-context-1",
            status=TaskStatus(state=state),
        )

    async def close(self) -> None:
        return None


def test_remote_endpoints_require_https_and_http_is_explicit_loopback_lab_only() -> None:
    with pytest.raises(ValidationError, match="HTTPS"):
        validate_outbound_url("http://127.0.0.1:18080/a2a")

    lab_policy = SSRFPolicy(
        allowed_ports=frozenset({18080}),
        allow_loopback_http_lab=True,
    )
    validated = validate_outbound_url(
        "http://127.0.0.1:18080/a2a",
        policy=lab_policy,
    )
    assert validated.scheme == "http"
    assert validated.addresses == ("127.0.0.1",)

    with pytest.raises(ValidationError, match="loopback"):
        validate_outbound_url(
            "http://93.184.216.34:18080/a2a",
            policy=lab_policy,
        )

    local_route = OpaqueAgentRoute(
        route_token=TENANT,
        logical_agent_id="lab-agent",
        domain_id="lab.example",
    )
    template = AgentCard(
        name="Loopback lab agent",
        description="explicit local-only HTTP profile",
        version="1",
        capabilities=AgentCapabilities(streaming=True),
    )
    with pytest.raises(ValidationError, match="loopback"):
        build_exported_agent_card(
            template,
            route=local_route,
            public_base_url="http://127.0.0.1:18080",
        )
    lab_card = build_exported_agent_card(
        template,
        route=local_route,
        public_base_url="http://127.0.0.1:18080",
        allow_loopback_http_lab=True,
    )
    assert all(
        interface.url.startswith("http://127.0.0.1:18080/")
        for interface in lab_card.supported_interfaces
    )


@pytest.mark.anyio
async def test_outbound_journal_uses_bounded_polling_fallback_until_terminal(store) -> None:
    sdk = PollingSDK()
    client = NativeA2AClient(
        sdk_client=sdk,  # type: ignore[arg-type]
        store=store,
        peer_id="ordinary-server-agent-peer",
        tenant=TENANT,
        url_validator=lambda value: value,
        call_timeout_seconds=0.2,
        total_timeout_seconds=1.0,
        poll_interval_seconds=0.005,
    )
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="outbound-message-1",
            context_id="remote-context-1",
            role=Role.ROLE_USER,
            parts=[Part(text="poll if the bounded stream ends")],
        ),
        metadata={"agentnetIdempotencyKey": "outbound-idempotency-0001"},
    )

    async with asyncio.timeout(2):
        facts = await client.send(request)

    assert sdk.polls == 2
    assert facts[-1].terminal_remote_state is True
    assert facts[-1].task_state == "completed"
    exchange = store.fetch_one("SELECT * FROM a2a_outbound_exchanges")
    assert exchange["state"] == "terminal_remote_fact"
    assert exchange["remote_task_id"] == "remote-task-1"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_outbound_events")["count"] == 3
    assert "poll if" not in exchange["request_encrypted"]

    replayed = await client.send(request)
    assert sdk.sends == 1
    assert sdk.polls == 2
    assert replayed[-1].task_state == "completed"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_outbound_events")["count"] == 3


@pytest.mark.anyio
async def test_callback_delivery_is_signed_ip_pinned_token_bound_and_ssrf_closed(identity_factory) -> None:
    sender, sender_key = identity_factory()
    captured: dict[str, Any] = {}

    async def handle(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["host"] = request.headers["host"]
        captured["token"] = request.headers["x-a2a-notification-token"]
        captured["version"] = request.headers["a2a-version"]
        captured["sni"] = request.extensions["sni_hostname"]
        captured["body"] = await request.aread()
        captured["proof"] = proof_from_headers(dict(request.headers))
        return httpx.Response(204)

    identity = CorporateA2AClientIdentity(
        key=sender_key,
        domain_id=sender.domain_id,
        harness_id=sender.harness_id,
        credential_id=sender.credential_id,
        audience=AUDIENCE,
    )
    callback = create_pinned_callback_sender(
        identity=identity,
        policy=SSRFPolicy(allowed_hosts=frozenset({"callbacks.example"})),
        resolver=lambda host, port: (GLOBAL_IP,),
        inner_transport=httpx.MockTransport(handle),
        timeout_seconds=0.5,
    )
    event = StreamResponse(
        status_update=TaskStatusUpdateEvent(
            task_id="task-1",
            context_id="context-1",
            status=TaskStatus(state=TaskState.TASK_STATE_COMPLETED),
        )
    )
    config = TaskPushNotificationConfig(
        id="callback-1",
        task_id="task-1",
        url="https://callbacks.example/a2a/callback",
        token="opaque-notification-token",
    )
    try:
        await callback.send(config, event)
        proof = captured["proof"]
        verify_request_proof(
            proof,
            public_key_pem=sender_key.public_pem,
            expected_method="POST",
            expected_audience=AUDIENCE,
            expected_scheme="https",
            expected_authority="callbacks.example",
            expected_path="/a2a/callback",
            expected_query="",
            body=captured["body"],
            now=int(time.time()),
            max_age=300,
            future_skew=60,
        )
        assert captured["url"] == f"https://{GLOBAL_IP}/a2a/callback"
        assert captured["host"] == "callbacks.example"
        assert captured["sni"] == "callbacks.example"
        assert captured["token"] == "opaque-notification-token"
        assert captured["version"] == "1.0"

        with pytest.raises(ValidationError):
            await callback.send(
                TaskPushNotificationConfig(
                    id="callback-2",
                    task_id="task-1",
                    url="https://127.0.0.1/internal",
                ),
                event,
            )
    finally:
        await callback.close()


class CapturingCallbackSender:
    def __init__(self) -> None:
        self.deliveries: list[tuple[TaskPushNotificationConfig, StreamResponse]] = []

    async def send(self, config: TaskPushNotificationConfig, event: StreamResponse) -> None:
        copied_config = TaskPushNotificationConfig()
        copied_config.CopyFrom(config)
        copied_event = StreamResponse()
        copied_event.CopyFrom(event)
        self.deliveries.append((copied_config, copied_event))


def issue_task_scope(
    store,
    *,
    owner: VerifiedActor,
    recipient: VerifiedActor,
) -> tuple[CollaborationScopeService, CollaborationScope]:
    scopes = CollaborationScopeService(store)
    policy = LocalConformancePolicyEngine(store)
    revision = policy.current_policy_revision(owner)
    domain = store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope:a2a-native:{uuid4()}",
        scope_kind="direct",
        member_harness_ids=tuple(sorted((owner.harness_id, recipient.harness_id))),
        allowed_actions=("task.accept", "task.propose"),
        allowed_resource_prefixes=("task:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=revision,
        domain_revocation_epoch=int(domain["revocation_epoch"]),
    )
    resource = f"scope:{proposal.scope_id}"
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=owner.domain_id,
            principal_id=owner.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=resource,
            revision=revision,
        )
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=owner,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=resource,
            policy_revision=revision,
            context=scopes.issuance_request(actor=owner, proposal=proposal),
        )
    )
    scope = scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=owner,
            policy_decision_id=decision.decision_id,
        ),
    )
    return scopes, scope


def seed_task_grant(store, actor, recipient_id: str) -> TaskGrant:
    policy = LocalConformancePolicyEngine(store)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action="a2a.task.submit",
            resource_pattern=recipient_id,
            revision=1,
        )
    )
    grant = TaskGrant(
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        harness_id=actor.harness_id,
        actions=frozenset({"a2a.task.submit"}),
        resources=frozenset({recipient_id}),
        input_sources=frozenset({corporate_input_source(actor)}),
        output_sinks=frozenset({corporate_output_sink(recipient_id)}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=2,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    with store.transaction() as connection:
        return policy.grants._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "focused_native_client_test"},
        )


@pytest.mark.anyio
async def test_runtime_persists_encrypted_callback_and_delivers_transition_event(
    store,
    identity_factory,
    workload_credentials_factory,
    execution_grant_factory,
) -> None:
    sender, _sender_key = identity_factory()
    recipient, _recipient_key = identity_factory(kind="pi")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id IN (?,?)",
            (sender.harness_id, recipient.harness_id),
        )
    sender = sender.model_copy(update={"binding_assurance": "os_bound"})
    recipient = recipient.model_copy(update={"binding_assurance": "os_bound"})
    grant = seed_task_grant(store, sender, recipient.harness_id)
    scopes, scope = issue_task_scope(store, owner=sender, recipient=recipient)
    callback_sender = CapturingCallbackSender()
    runtime = DurableA2ARuntime(
        store=store,
        mailbox=MailboxService(
            store,
            collaboration_scopes=scopes,
            acceptance_fact=DeliveryFact.ACCEPTED_LOCAL,
        ),
        collaboration_scopes=scopes,
        policy=LocalConformancePolicyEngine(store),
        agent_card=AgentCard(
            name="Callback server-agent",
            description="callback persistence test",
            version="1",
            capabilities=AgentCapabilities(streaming=True, push_notifications=True),
        ),
        recipient_id=recipient.harness_id,
        url_validator=lambda url: url
        if url == "https://callbacks.example/a2a/callback"
        else (_ for _ in ()).throw(ValidationError("callback host denied")),
        callback_sender=callback_sender,
    )
    context = ServerCallContext(
        tenant=TENANT,
        state={
            "verified_actor": sender,
            "a2a_peer_namespace": corporate_peer_namespace(sender),
            "a2a_identity_mode": "corporate_verified",
        },
    )
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="callback-task-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="task with callback")],
        ),
        metadata={
            "agentnetIntent": "task",
            "agentnetIdempotencyKey": "callback-task-idempotency-0001",
            "agentnetTaskGrantId": grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": scope.scope_id,
        },
    )
    task = await runtime.on_message_send(request, context)
    assert isinstance(task, Task)
    assert task.metadata["agentnetExecutable"] is False
    approval = runtime.assignments.approve(
        actor=recipient,
        proposal_id=str(task.metadata["agentnetProposalId"]),
        expected_request_digest=str(task.metadata["agentnetAssignmentDigest"]),
        expected_revision=1,
    )
    event_id = str(approval.resumed_event_id)
    task = await runtime.on_get_task(GetTaskRequest(tenant=TENANT, id=task.id), context)
    assert task is not None and task.metadata["agentnetExecutable"] is True
    config = await runtime.on_create_task_push_notification_config(
        TaskPushNotificationConfig(
            task_id=task.id,
            id="callback-config-1",
            url="https://callbacks.example/a2a/callback",
            token="secret-callback-token",
        ),
        context,
    )
    assert config.id == "callback-config-1"
    stored = store.fetch_one("SELECT config_encrypted,url_hash FROM a2a_callbacks")
    assert "secret-callback-token" not in stored["config_encrypted"]
    assert "callbacks.example" not in stored["config_encrypted"]

    assert str(task.metadata["agentnetEventId"]) == event_id
    execution_grant = execution_grant_factory(recipient=recipient, event_id=event_id)
    workload_credentials = workload_credentials_factory(
        domain=sender.domain_id,
        recipient_id=recipient.harness_id,
        event_id=event_id,
        task_grant_id=execution_grant.grant_id,
        roles=("mailbox_dispatcher", "recipient_custodian", "recipient_processor"),
    )
    await runtime.transition_task(
        task.id,
        state=TaskState.TASK_STATE_WORKING,
        owner_actor=workload_credentials["recipient_processor"].actor,
        workload_credentials=workload_credentials,
    )
    assert len(callback_sender.deliveries) == 1
    delivered_config, delivered_event = callback_sender.deliveries[0]
    assert delivered_config.token == "secret-callback-token"
    assert delivered_event.status_update.status.state == TaskState.TASK_STATE_WORKING
    attempt = store.fetch_one("SELECT attempts,last_error FROM a2a_callbacks")
    assert attempt["attempts"] == 1
    assert attempt["last_error"] is None

    with pytest.raises(ValidationError, match="callback host denied"):
        await runtime.on_create_task_push_notification_config(
            TaskPushNotificationConfig(
                task_id=task.id,
                id="callback-config-2",
                url="https://127.0.0.1/internal",
            ),
            context,
        )


@pytest.mark.anyio
async def test_official_sdk_client_sends_signed_corporate_task_through_pinned_in_process_transport(
    store,
    identity_factory,
) -> None:
    sender, sender_key = identity_factory()
    recipient, _recipient_key = identity_factory(kind="pi")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id IN (?,?)",
            (sender.harness_id, recipient.harness_id),
        )
    sender = sender.model_copy(update={"binding_assurance": "os_bound"})
    recipient = recipient.model_copy(update={"binding_assurance": "os_bound"})
    grant = seed_task_grant(store, sender, recipient.harness_id)
    scopes, scope = issue_task_scope(store, owner=sender, recipient=recipient)
    route = OpaqueAgentRoute(
        route_token=TENANT,
        logical_agent_id=recipient.harness_id,
        domain_id=sender.domain_id,
    )
    card = build_exported_agent_card(
        AgentCard(
            name="Ordinary server-agent",
            description="native SDK client test",
            version="1",
            capabilities=AgentCapabilities(streaming=False),
        ),
        route=route,
        public_base_url="https://agents.example",
    )
    runtime = DurableA2ARuntime(
        store=store,
        mailbox=MailboxService(
            store,
            collaboration_scopes=scopes,
            acceptance_fact=DeliveryFact.ACCEPTED_LOCAL,
        ),
        collaboration_scopes=scopes,
        policy=LocalConformancePolicyEngine(store),
        agent_card=card,
        recipient_id=recipient.harness_id,
        url_validator=lambda value: value,
    )
    standing = StandingA2AGrant(
        grant_id="native-client-standing-1",
        route_token=TENANT,
        logical_agent_id=recipient.harness_id,
        allowed_actions=frozenset({"a2a.message.send", "a2a.task.get"}),
        allowed_resources=frozenset({recipient.harness_id}),
        allowed_output_sinks=frozenset({"public-response"}),
        allowed_peer_namespaces=frozenset({corporate_peer_namespace(sender)}),
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    identity_resolver = VerifiedContextResolver(
        store,
        service_audience=AUDIENCE,
        service_scheme="https",
        service_authority="agents.example",
    )
    server_routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=runtime,
        agent_card=card,
        route=route,
        grant_lookup=lambda token: standing if token == TENANT else None,
        peer_resolver=lambda request: "unsigned-peer-not-used",
        corporate_authenticator=SignedCorporateA2AAuthenticator(identity_resolver),
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_IP,),
    )
    native = create_native_a2a_client(
        card,
        store=store,
        identity=CorporateA2AClientIdentity(
            key=sender_key,
            domain_id=sender.domain_id,
            harness_id=sender.harness_id,
            credential_id=sender.credential_id,
            audience=AUDIENCE,
        ),
        peer_id="agents.example:ordinary-server-agent",
        tenant=TENANT,
        policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_IP,),
        inner_transport=httpx.ASGITransport(
            app=Starlette(routes=server_routes),
            raise_app_exceptions=False,
        ),
        call_timeout_seconds=0.5,
        total_timeout_seconds=2,
        poll_interval_seconds=0.01,
    )
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="native-sdk-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="signed native SDK task")],
        ),
        metadata={
            "agentnetIntent": "task",
            "agentnetIdempotencyKey": "native-sdk-idempotency-0001",
            "agentnetTaskGrantId": grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": scope.scope_id,
        },
    )
    try:
        async with asyncio.timeout(3):
            facts = await native.send(request, wait_for_terminal=False)
    finally:
        await native.close()

    assert facts[-1].kind is A2AMappedKind.TASK
    assert facts[-1].task_state == "submitted"
    assert facts[-1].authority_eligible is False
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_tasks")["count"] == 1
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_outbound_exchanges")["count"] == 1
    assert LocalConformancePolicyEngine(store).grants.uses_for_local_conformance(grant.grant_id) == 1
