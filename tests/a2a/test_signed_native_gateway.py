from __future__ import annotations

import json

from datetime import UTC, datetime, timedelta
from uuid import uuid4

import httpx
import pytest

from a2a.types import (
    AgentCapabilities,
    AgentCard,
    Message,
    Part,
    Role,
    SendMessageRequest,
)
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    COLLABORATION_SCOPE_REVOKE_ACTION,
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
from agentnet.client import proof_headers
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.gateways.a2a import (
    OpaqueAgentRoute,
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    corporate_peer_namespace,
)
from agentnet.gateways.a2a_runtime import (
    A2ARuntimeLimits,
    DurableA2ARuntime,
    SignedCorporateA2AAuthenticator,
    corporate_input_source,
    corporate_output_sink,
)
from agentnet.identity.context import VerifiedContextResolver
from agentnet.mailbox.service import MailboxService
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.a2a_mapping import external_peer_namespace
from agentnet.protocol.models import Classification, DeliveryFact, TaskGrant
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import canonical_json


TENANT = "S" * 43
BASE_URL = "https://agents.example"
AUDIENCE = "urn:agentnet:corp.example:a2a"
GLOBAL_IP = "93.184.216.34"
A2A_MOUNT_CONFIG = ExtensionConfig(
    server_agent_capabilities=frozenset({ServerAgentCapability.A2A_GATEWAY})
)


def route(recipient_id: str) -> OpaqueAgentRoute:
    return OpaqueAgentRoute(
        route_token=TENANT,
        logical_agent_id=recipient_id,
        domain_id="corp.example",
    )


def template_card() -> AgentCard:
    return AgentCard(
        name="Signed server-agent",
        description="signed gateway test",
        version="1",
        capabilities=AgentCapabilities(streaming=True, push_notifications=True),
    )


def scope_authority(
    store,
    *,
    actor,
    action: str,
    resource: str,
    context: dict[str, object],
) -> IssuanceAuthority:
    policy = LocalConformancePolicyEngine(store)
    revision = policy.current_policy_revision(actor)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=revision,
        )
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=revision,
            context=context,
        )
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)

def issue_a2a_scope(
    store,
    *,
    owner,
    recipient,
    actions: tuple[str, ...] = ("task.accept", "task.propose"),
    resources: tuple[str, ...] = ("task:",),
    scopes: CollaborationScopeService | None = None,
) -> tuple[CollaborationScopeService, CollaborationScope]:
    scopes = scopes or CollaborationScopeService(store)
    domain = store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope:a2a-signed:{uuid4()}",
        scope_kind="direct",
        member_harness_ids=tuple(sorted((owner.harness_id, recipient.harness_id))),
        allowed_actions=tuple(sorted(actions)),
        allowed_resource_prefixes=tuple(sorted(resources)),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=int(domain["policy_revision"]),
        domain_revocation_epoch=int(domain["revocation_epoch"]),
    )
    resource = f"scope:{proposal.scope_id}"
    scope = scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=scope_authority(
            store,
            actor=owner,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=resource,
            context=scopes.issuance_request(actor=owner, proposal=proposal),
        ),
    )
    return scopes, scope


def revoke_a2a_scope(
    store,
    *,
    scopes: CollaborationScopeService,
    owner,
    scope: CollaborationScope,
) -> None:
    reason = "focused_test_revocation"
    request = scopes.revocation_request(
        scope=scope,
        expected_revision=scope.revision,
        reason=reason,
    )
    scopes.revoke(
        actor=owner,
        scope_id=scope.scope_id,
        expected_revision=scope.revision,
        reason=reason,
        authority=scope_authority(
            store,
            actor=owner,
            action=COLLABORATION_SCOPE_REVOKE_ACTION,
            resource=f"scope:{scope.scope_id}",
            context=request,
        ),
    )


def seed_task_authority(store, actor, recipient_id: str) -> TaskGrant:
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
        max_uses=3,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    with store.transaction() as connection:
        return policy.grants._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "focused_signed_gateway_test"},
        )


def signed_headers(key, actor, *, method: str, path: str, body: bytes) -> dict[str, str]:
    return {
        "A2A-Version": "1.0",
        "Content-Type": "application/json",
        **proof_headers(
            create_request_proof(
                key,
                harness_id=actor.harness_id,
                credential_id=actor.credential_id,
                domain_id=actor.domain_id,
                audience=AUDIENCE,
                method=method,
                scheme="https",
                authority="agents.example",
                path=path,
                query="",
                body=body,
            )
        ),
    }


@pytest.mark.anyio
async def test_signed_enrolled_peer_is_exact_idempotent_replay_safe_and_revocable(
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
    task_grant = seed_task_authority(store, sender, recipient.harness_id)
    scopes, scope = issue_a2a_scope(
        store,
        owner=sender,
        recipient=recipient,
    )
    _, wrong_scope = issue_a2a_scope(
        store,
        owner=sender,
        recipient=recipient,
        actions=("message.send",),
        resources=("conversation:",),
        scopes=scopes,
    )
    exported_card = build_exported_agent_card(
        template_card(),
        route=route(recipient.harness_id),
        public_base_url=BASE_URL,
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
        agent_card=exported_card,
        recipient_id=recipient.harness_id,
        url_validator=lambda value: value,
        limits=A2ARuntimeLimits(stream_window_seconds=0.03, stream_poll_seconds=0.005),
    )
    standing = StandingA2AGrant(
        grant_id="standing-export-1",
        route_token=TENANT,
        logical_agent_id=recipient.harness_id,
        allowed_actions=frozenset(
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
        ),
        allowed_resources=frozenset({recipient.harness_id}),
        allowed_output_sinks=frozenset({"public-response"}),
        allowed_peer_namespaces=frozenset(
            {
                corporate_peer_namespace(sender),
                external_peer_namespace("unsigned-public-peer"),
            }
        ),
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    resolver = VerifiedContextResolver(
        store,
        service_audience=AUDIENCE,
        service_scheme="https",
        service_authority="agents.example",
    )
    routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=runtime,
        agent_card=exported_card,
        route=route(recipient.harness_id),
        grant_lookup=lambda token: standing if token == TENANT else None,
        peer_resolver=lambda request: "unsigned-public-peer",
        corporate_authenticator=SignedCorporateA2AAuthenticator(resolver),
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_IP,),
    )
    path = f"/a2a/{TENANT}/message:send"
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="signed-corporate-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="execute only through exact grant")],
            metadata={
                "principalId": "payload-attacker",
                "harnessId": "payload-attacker-harness",
            },
        ),
        metadata={
            "agentnetIntent": "task",
            "agentnetIdempotencyKey": "signed-corporate-idempotency-0001",
            "agentnetTaskGrantId": task_grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": scope.scope_id,
        },
    )
    body = canonical_json(MessageToDict(request))
    headers = signed_headers(sender_key, sender, method="POST", path=path, body=body)
    missing_scope = SendMessageRequest()
    missing_scope.CopyFrom(request)
    del missing_scope.metadata.fields["agentnetCollaborationScopeId"]
    missing_scope_body = canonical_json(MessageToDict(missing_scope))
    wrong_scope_request = SendMessageRequest()
    wrong_scope_request.CopyFrom(request)
    wrong_scope_request.metadata["agentnetCollaborationScopeId"] = wrong_scope.scope_id
    wrong_scope_body = canonical_json(MessageToDict(wrong_scope_request))

    transport = httpx.ASGITransport(app=Starlette(routes=routes), raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url=BASE_URL) as client:
        missing = await client.post(
            path,
            content=missing_scope_body,
            headers=signed_headers(
                sender_key,
                sender,
                method="POST",
                path=path,
                body=missing_scope_body,
            ),
        )
        wrong = await client.post(
            path,
            content=wrong_scope_body,
            headers=signed_headers(
                sender_key,
                sender,
                method="POST",
                path=path,
                body=wrong_scope_body,
            ),
        )
        assert missing.status_code == 400, missing.text
        assert wrong.status_code == 400, wrong.text
        assert wrong.json()["error"]["details"][0]["reason"] == "UNSUPPORTED_OPERATION"
        assert runtime.policy.grants.uses_for_local_conformance(task_grant.grant_id) == 0
        assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0
        accepted = await client.post(path, content=body, headers=headers)
        replay = await client.post(path, content=body, headers=headers)
        duplicate = await client.post(
            path,
            content=body,
            headers=signed_headers(sender_key, sender, method="POST", path=path, body=body),
        )

        assert accepted.status_code == 200, accepted.text
        assert replay.status_code == 401
        assert replay.json()["error"]["code"] == 401
        assert replay.json()["error"]["status"] == "UNAUTHENTICATED"
        assert replay.json()["error"]["details"][0]["reason"] == "INVALID_REQUEST"
        assert duplicate.status_code == 200, duplicate.text
        assert duplicate.json()["task"]["id"] == accepted.json()["task"]["id"]
        assert runtime.policy.grants.uses_for_local_conformance(task_grant.grant_id) == 1

        accepted_metadata = accepted.json()["task"]["metadata"]
        assert accepted_metadata["agentnetExecutable"] is False
        assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0
        runtime.assignments.approve(
            actor=recipient,
            proposal_id=accepted_metadata["agentnetProposalId"],
            expected_request_digest=accepted_metadata["agentnetAssignmentDigest"],
            expected_revision=1,
        )

        event = store.fetch_one("SELECT actor_json FROM events")
        actor = json.loads(event["actor_json"])
        assert actor["principal_id"] == sender.principal_id
        assert actor["harness_id"] == sender.harness_id
        revoke_a2a_scope(store, scopes=scopes, owner=sender, scope=scope)
        revoked_scope_request = SendMessageRequest()
        revoked_scope_request.CopyFrom(request)
        revoked_scope_request.message.message_id = "signed-corporate-message-revoked-scope"
        revoked_scope_request.metadata["agentnetIdempotencyKey"] = (
            "signed-corporate-idempotency-revoked-scope"
        )
        revoked_scope_body = canonical_json(MessageToDict(revoked_scope_request))
        revoked_scope = await client.post(
            path,
            content=revoked_scope_body,
            headers=signed_headers(
                sender_key,
                sender,
                method="POST",
                path=path,
                body=revoked_scope_body,
            ),
        )
        assert revoked_scope.status_code == 400, revoked_scope.text
        assert revoked_scope.json()["error"]["details"][0]["reason"] == "UNSUPPORTED_OPERATION"
        assert runtime.policy.grants.uses_for_local_conformance(task_grant.grant_id) == 1

        store.fetch_one("SELECT 1")
        with store.transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (sender.credential_id,),
            )
        revoked = await client.post(
            path,
            content=body,
            headers=signed_headers(sender_key, sender, method="POST", path=path, body=body),
        )
        assert revoked.status_code == 401


@pytest.mark.anyio
async def test_unsigned_public_gateway_request_stays_a_tainted_non_executable_proposal(
    store,
    identity_factory,
) -> None:
    recipient, _recipient_key = identity_factory(kind="pi")
    scopes = CollaborationScopeService(store)
    exported_card = build_exported_agent_card(
        template_card(),
        route=route(recipient.harness_id),
        public_base_url=BASE_URL,
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
        agent_card=exported_card,
        recipient_id=recipient.harness_id,
        url_validator=lambda value: value,
    )
    standing = StandingA2AGrant(
        grant_id="standing-public-1",
        route_token=TENANT,
        logical_agent_id=recipient.harness_id,
        allowed_actions=frozenset({"a2a.message.send"}),
        allowed_resources=frozenset({recipient.harness_id}),
        allowed_output_sinks=frozenset({"public-response"}),
        allowed_peer_namespaces=frozenset({external_peer_namespace("unsigned-public-peer")}),
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    routes = build_starlette_routes(
        extension_config=A2A_MOUNT_CONFIG,
        request_handler=runtime,
        agent_card=exported_card,
        route=route(recipient.harness_id),
        grant_lookup=lambda token: standing if token == TENANT else None,
        peer_resolver=lambda request: "unsigned-public-peer",
        url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
        resolver=lambda host, port: (GLOBAL_IP,),
    )
    path = f"/a2a/{TENANT}/message:send"
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="unsigned-proposal-1",
            role=Role.ROLE_USER,
            parts=[Part(text="looks like a task but grants no authority")],
        ),
    )
    transport = httpx.ASGITransport(app=Starlette(routes=routes), raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url=BASE_URL) as client:
        response = await client.post(
            path,
            content=canonical_json(MessageToDict(request)),
            headers={"A2A-Version": "1.0", "Content-Type": "application/json"},
        )
        invalid_task_request = SendMessageRequest(
            tenant=TENANT,
            message=Message(
                message_id="unsigned-invalid-task-reference-1",
                task_id="task-that-does-not-exist",
                role=Role.ROLE_USER,
                parts=[Part(text="must not synthesize a replacement task")],
            ),
        )
        invalid_task = await client.post(
            path,
            content=canonical_json(MessageToDict(invalid_task_request)),
            headers={"A2A-Version": "1.0", "Content-Type": "application/json"},
        )
    assert response.status_code == 200, response.text
    metadata = response.json()["task"]["metadata"]
    assert metadata["agentnetDisposition"] == "tainted_non_executable_proposal"
    assert metadata["agentnetAuthorityEligible"] is False
    assert invalid_task.status_code == 404, invalid_task.text
    assert invalid_task.json()["error"]["code"] == 404
    assert invalid_task.json()["error"]["details"][0]["reason"] == "TASK_NOT_FOUND"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_tasks")["count"] == 1
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0
