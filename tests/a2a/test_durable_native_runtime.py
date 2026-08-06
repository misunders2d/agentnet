from __future__ import annotations

import asyncio
import json

from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest

from a2a.server.context import ServerCallContext
from a2a.types import (
    AgentCapabilities,
    AgentCard,
    Artifact,
    GetTaskRequest,
    Message,
    Part,
    Role,
    SendMessageRequest,
    Task,
    TaskPushNotificationConfig,
    TaskState,
)

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
from agentnet.errors import AuthorizationError, IdempotencyConflict, ValidationError
from agentnet.gateways.a2a import corporate_peer_namespace
from agentnet.gateways.a2a_runtime import (
    A2ARuntimeLimits,
    DurableA2ARuntime,
    corporate_input_source,
    corporate_output_sink,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.protocol.a2a_mapping import external_peer_namespace
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    ReleasedArtifactBinding,
    TaskGrant,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.sqlite import SQLiteStore


TENANT = "R" * 43


def card() -> AgentCard:
    return AgentCard(
        name="Durable server-agent",
        description="focused runtime test",
        version="1",
        capabilities=AgentCapabilities(streaming=True, push_notifications=True),
    )


def external_context() -> ServerCallContext:
    namespace = external_peer_namespace("public-peer-1")
    return ServerCallContext(
        tenant=TENANT,
        state={
            "verified_actor": VerifiedActor(
                kind=ActorKind.EXTERNAL_A2A,
                domain_id="corp.example",
                external_peer_id=namespace,
                binding_assurance="external",
            ),
            "a2a_peer_namespace": namespace,
            "a2a_identity_mode": "external_unverified",
        },
    )

def scope_authority(
    store: SQLiteStore,
    *,
    actor: VerifiedActor,
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
    store: SQLiteStore,
    *,
    owner: VerifiedActor,
    recipient: VerifiedActor,
    actions: tuple[str, ...],
    resources: tuple[str, ...],
) -> tuple[CollaborationScopeService, CollaborationScope]:
    scopes = CollaborationScopeService(store)
    domain = store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope:a2a:{uuid4()}",
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
    store: SQLiteStore,
    *,
    scopes: CollaborationScopeService,
    owner: VerifiedActor,
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




def runtime(
    store: SQLiteStore,
    *,
    recipient_id: str = "server-agent-1",
    collaboration_scopes: CollaborationScopeService | None = None,
    artifact_binding_resolver=None,
) -> DurableA2ARuntime:
    scopes = collaboration_scopes or CollaborationScopeService(store)
    return DurableA2ARuntime(
        store=store,
        mailbox=MailboxService(
            store,
            collaboration_scopes=scopes,
            acceptance_fact=DeliveryFact.ACCEPTED_LOCAL,
        ),
        collaboration_scopes=scopes,
        policy=LocalConformancePolicyEngine(store),
        agent_card=card(),
        recipient_id=recipient_id,
        url_validator=lambda url: url if url == "https://files.example/object/1" else (_ for _ in ()).throw(ValueError()),
        artifact_binding_resolver=artifact_binding_resolver,
        limits=A2ARuntimeLimits(stream_window_seconds=0.05, stream_poll_seconds=0.005),
    )


@pytest.mark.anyio
async def test_external_proposal_is_durable_encrypted_idempotent_and_never_executable(tmp_path: Path) -> None:
    key = b"x" * 32
    path = tmp_path / "a2a.sqlite3"
    first_store = SQLiteStore(path, LocalEnvelopeCipher(key))
    first_runtime = runtime(first_store)
    request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="public-message-1",
            role=Role.ROLE_USER,
            parts=[
                Part(text="untrusted instructions stay inert"),
                Part(
                    url="https://files.example/object/1",
                    filename="proposal.txt",
                    media_type="text/plain",
                ),
            ],
        ),
    )
    context = external_context()

    accepted = await first_runtime.on_message_send(request, context)
    duplicate = await first_runtime.on_message_send(request, context)

    assert isinstance(accepted, Task)
    assert duplicate.id == accepted.id
    assert accepted.status.state == TaskState.TASK_STATE_SUBMITTED
    assert accepted.metadata["agentnetDisposition"] == "tainted_non_executable_proposal"
    assert accepted.metadata["agentnetAuthorityEligible"] is False
    assert accepted.metadata["agentnetExecutable"] is False
    assert first_store.fetch_one("SELECT COUNT(*) AS count FROM a2a_tasks")["count"] == 1
    encrypted = first_store.fetch_one("SELECT task_encrypted FROM a2a_tasks")["task_encrypted"]
    assert "untrusted instructions" not in encrypted
    assert "files.example" not in encrypted

    conflicting = SendMessageRequest()
    conflicting.CopyFrom(request)
    conflicting.message.parts[0].text = "different exact bytes"
    with pytest.raises(IdempotencyConflict):
        await first_runtime.on_message_send(conflicting, context)
    with pytest.raises(AuthorizationError, match="cannot execute"):
        await first_runtime.transition_task(
            accepted.id,
            state=TaskState.TASK_STATE_WORKING,
            owner_actor=VerifiedActor(
                kind=ActorKind.WORKLOAD,
                domain_id="corp.example",
                workload_id="recipient.processor:server-agent-1",
                parent_event_id="not-an-event",
                task_grant_id="not-a-grant",
                binding_assurance="internal_process",
            ),
        )
    with pytest.raises(AuthorizationError, match="tainted proposals"):
        await first_runtime.on_create_task_push_notification_config(
            TaskPushNotificationConfig(
                task_id=accepted.id,
                id="external-callback-denied",
                url="https://files.example/object/1",
            ),
            context,
        )

    raw_proposal = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="public-raw-file-rejected",
            role=Role.ROLE_USER,
            parts=[Part(raw=b"PK\x03\x04", media_type="application/zip")],
        ),
    )
    with pytest.raises(ValidationError, match="staged quarantine"):
        await first_runtime.on_message_send(raw_proposal, context)
    assert first_store.fetch_one("SELECT COUNT(*) AS count FROM a2a_tasks")["count"] == 1

    async with asyncio.timeout(0.5):
        streamed = [item async for item in first_runtime.on_message_send_stream(request, context)]
    assert len(streamed) == 1
    first_store.close()

    reopened_store = SQLiteStore(path, LocalEnvelopeCipher(key))
    try:
        reopened_runtime = runtime(reopened_store)
        recovered = await reopened_runtime.on_get_task(
            GetTaskRequest(tenant=TENANT, id=accepted.id),
            context,
        )
        assert recovered is not None
        assert recovered.id == accepted.id
        assert recovered.history[0].parts[0].text == "untrusted instructions stay inert"
    finally:
        reopened_store.close()


def seed_corporate_grant(
    store: SQLiteStore,
    actor: VerifiedActor,
    recipient_id: str,
    *,
    action: str,
) -> TaskGrant:
    policy = LocalConformancePolicyEngine(store)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=recipient_id,
            revision=1,
        )
    )
    grant = TaskGrant(
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        harness_id=actor.harness_id,
        actions=frozenset({action}),
        resources=frozenset({recipient_id}),
        input_sources=frozenset({corporate_input_source(actor)}),
        output_sinks=frozenset({corporate_output_sink(recipient_id)}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=4,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    with store.transaction() as connection:
        return policy.grants._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "focused_test_fixture"},
        )


def corporate_context(actor: VerifiedActor) -> ServerCallContext:
    return ServerCallContext(
        tenant=TENANT,
        state={
            "verified_actor": actor,
            "a2a_peer_namespace": corporate_peer_namespace(actor),
            "a2a_identity_mode": "corporate_verified",
        },
    )


def corporate_request(
    *,
    intent: str,
    grant: TaskGrant,
    scope: CollaborationScope,
    suffix: str,
) -> SendMessageRequest:
    return SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id=f"corporate-message-{suffix}",
            context_id=f"corporate-context-{suffix}",
            role=Role.ROLE_USER,
            parts=[Part(text=f"corporate {intent}")],
            metadata={"claimedPrincipal": "attacker-controlled-and-ignored"},
        ),
        metadata={
            "agentnetIntent": intent,
            "agentnetIdempotencyKey": f"corporate-idempotency-{suffix}",
            "agentnetTaskGrantId": grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": scope.scope_id,
        },
    )


@pytest.mark.anyio
async def test_corporate_message_and_task_use_exact_grants_mailbox_and_task_state_owners(
    store: SQLiteStore,
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
    direct_grant = seed_corporate_grant(store, sender, recipient.harness_id, action="a2a.message.submit")
    task_grant = seed_corporate_grant(store, sender, recipient.harness_id, action="a2a.task.submit")
    scopes, scope = issue_a2a_scope(
        store,
        owner=sender,
        recipient=recipient,
        actions=("message.read", "message.send", "task.accept", "task.propose"),
        resources=("conversation:", "task:"),
    )
    released = ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id=sender.domain_id,
        object_version="a" * 64,
        size=6,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )
    handler = runtime(
        store,
        recipient_id=recipient.harness_id,
        collaboration_scopes=scopes,
        artifact_binding_resolver=lambda artifact_id: released
        if artifact_id == released.artifact_id
        else (_ for _ in ()).throw(AuthorizationError("unknown released artifact")),
    )
    context = corporate_context(sender)

    direct_request = corporate_request(
        intent="message",
        grant=direct_grant,
        scope=scope,
        suffix="direct-0001",
    )
    direct = await handler.on_message_send(direct_request, context)
    direct_duplicate = await handler.on_message_send(direct_request, context)
    assert isinstance(direct, Message)
    assert direct_duplicate.message_id == direct.message_id
    assert direct.task_id == ""
    assert direct.metadata["agentnetDisposition"] == "corporate_message_accepted"
    assert handler.policy.grants.uses_for_local_conformance(direct_grant.grant_id) == 1

    mailbox_rows = handler.mailbox.reconcile(
        actor=recipient,
        collaboration_scope_id=scope.scope_id,
    )
    assert len(mailbox_rows) == 1
    assert mailbox_rows[0]["event"]["actor"]["principal_id"] == sender.principal_id
    assert "claimedPrincipal" in mailbox_rows[0]["payload"]["a2a_message"]["metadata"]
    assert mailbox_rows[0]["payload"]["authorization_context"] == scope.authorization_context()

    task_request = corporate_request(
        intent="task",
        grant=task_grant,
        scope=scope,
        suffix="task-0000001",
    )
    task = await handler.on_message_send(task_request, context)
    assert isinstance(task, Task)
    assert task.metadata["agentnetExecutable"] is False
    assert task.metadata["agentnetDisposition"] == "directional_approval_pending"
    assert len(
        handler.mailbox.reconcile(
            actor=recipient,
            collaboration_scope_id=scope.scope_id,
        )
    ) == 1
    approval = handler.assignments.approve(
        actor=recipient,
        proposal_id=str(task.metadata["agentnetProposalId"]),
        expected_request_digest=str(task.metadata["agentnetAssignmentDigest"]),
        expected_revision=1,
    )
    event_id = str(approval.resumed_event_id)
    task = await handler.on_get_task(GetTaskRequest(tenant=TENANT, id=task.id), context)
    assert task is not None
    assert task.metadata["agentnetExecutable"] is True
    assert task.metadata["agentnetEventId"] == event_id
    execution_grant = execution_grant_factory(recipient=recipient, event_id=event_id)
    workload_credentials = workload_credentials_factory(
        domain=sender.domain_id,
        recipient_id=recipient.harness_id,
        event_id=event_id,
        task_grant_id=execution_grant.grant_id,
        roles=(
            "mailbox_dispatcher",
            "recipient_custodian",
            "recipient_processor",
            "effect_authority",
        ),
    )
    working = await handler.transition_task(
        task.id,
        state=TaskState.TASK_STATE_WORKING,
        owner_actor=workload_credentials["recipient_processor"].actor,
        workload_credentials=workload_credentials,
    )
    assert working.status.state == TaskState.TASK_STATE_WORKING
    completed = await handler.transition_task(
        task.id,
        state=TaskState.TASK_STATE_COMPLETED,
        owner_actor=workload_credentials["effect_authority"].actor,
        workload_credentials=workload_credentials,
        artifacts=[
            Artifact(
                artifact_id="artifact-1",
                name="result",
                parts=[
                    Part(
                        url="https://files.example/object/1",
                        media_type="text/plain",
                        metadata={"agentnetReleasedArtifact": released.model_dump(mode="json")},
                    )
                ],
            )
        ],
    )
    assert completed.status.state == TaskState.TASK_STATE_COMPLETED
    assert completed.artifacts[0].artifact_id == "artifact-1"
    assert completed.artifacts[0].parts[0].WhichOneof("content") == "text"
    assert released.artifact_id in completed.artifacts[0].parts[0].text
    recipient_fact = store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (event_id, recipient.harness_id),
    )
    assert recipient_fact["current_fact"] == DeliveryFact.COMPLETED.value
    assert store.fetch_one("SELECT COUNT(*) AS count FROM a2a_task_events WHERE task_id=?", (task.id,))["count"] == 4

    with store.transaction() as connection:
        connection.execute(
            "UPDATE task_grants SET revoked_at=? WHERE grant_id=?",
            (int(datetime.now(UTC).timestamp()), task_grant.grant_id),
        )
    with pytest.raises(AuthorizationError, match="grant is no longer current"):
        await handler.on_get_task(GetTaskRequest(tenant=TENANT, id=task.id), context)
    revoke_a2a_scope(store, scopes=scopes, owner=sender, scope=scope)
    with pytest.raises(AuthorizationError, match="not visible"):
        handler.mailbox.reconcile(
            actor=recipient,
            collaboration_scope_id=scope.scope_id,
        )


@pytest.mark.anyio
async def test_corporate_a2a_raw_and_unreleased_url_parts_fail_before_grant_or_mailbox_use(
    store: SQLiteStore,
    identity_factory,
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id IN (?,?)",
            (sender.harness_id, recipient.harness_id),
        )
    sender = sender.model_copy(update={"binding_assurance": "os_bound"})
    recipient = recipient.model_copy(update={"binding_assurance": "os_bound"})
    scopes, scope = issue_a2a_scope(
        store,
        owner=sender,
        recipient=recipient,
        actions=("message.read", "message.send"),
        resources=("conversation:",),
    )
    released = ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id=sender.domain_id,
        object_version="b" * 64,
        size=4,
        media_type="application/octet-stream",
        classification=Classification.C1_INTERNAL,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )
    handler = runtime(
        store,
        recipient_id=recipient.harness_id,
        collaboration_scopes=scopes,
        artifact_binding_resolver=lambda _artifact_id: released,
    )
    context = corporate_context(sender)

    raw_grant = seed_corporate_grant(
        store, sender, recipient.harness_id, action="a2a.message.submit"
    )
    raw = corporate_request(
        intent="message",
        grant=raw_grant,
        scope=scope,
        suffix="raw-rejected-0001",
    )
    raw.message.parts.append(
        Part(raw=b"MZ\x00\x00", media_type="application/octet-stream", filename="payload.exe")
    )
    with pytest.raises(ValidationError, match="staged quarantine"):
        await handler.on_message_send(raw, context)
    assert handler.policy.grants.uses_for_local_conformance(raw_grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0

    url_grant = seed_corporate_grant(
        store, sender, recipient.harness_id, action="a2a.message.submit"
    )
    unbound = corporate_request(
        intent="message",
        grant=url_grant,
        scope=scope,
        suffix="url-rejected-0001",
    )
    unbound.message.parts.append(
        Part(url="https://files.example/object/1", media_type="application/octet-stream")
    )
    with pytest.raises(AuthorizationError, match="released artifact binding"):
        await handler.on_message_send(unbound, context)
    assert handler.policy.grants.uses_for_local_conformance(url_grant.grant_id) == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0


@pytest.mark.anyio
async def test_corporate_a2a_released_url_is_exactly_bound_and_non_fetchable_in_custody(
    store: SQLiteStore,
    identity_factory,
) -> None:
    sender, _ = identity_factory()
    recipient, _ = identity_factory(kind="pi")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id IN (?,?)",
            (sender.harness_id, recipient.harness_id),
        )
    sender = sender.model_copy(update={"binding_assurance": "os_bound"})
    recipient = recipient.model_copy(update={"binding_assurance": "os_bound"})
    scopes, scope = issue_a2a_scope(
        store,
        owner=sender,
        recipient=recipient,
        actions=("message.read", "message.send"),
        resources=("conversation:",),
    )
    released = ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id=sender.domain_id,
        object_version="c" * 64,
        size=12,
        media_type="application/pdf",
        classification=Classification.C1_INTERNAL,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )
    handler = runtime(
        store,
        recipient_id=recipient.harness_id,
        collaboration_scopes=scopes,
        artifact_binding_resolver=lambda artifact_id: released
        if artifact_id == released.artifact_id
        else (_ for _ in ()).throw(AuthorizationError("unknown released artifact")),
    )
    grant = seed_corporate_grant(
        store, sender, recipient.harness_id, action="a2a.message.submit"
    )
    request = corporate_request(
        intent="message",
        grant=grant,
        scope=scope,
        suffix="released-url-0001",
    )
    request.message.parts.append(
        Part(
            url="https://files.example/object/1",
            media_type="application/pdf",
            filename="report.pdf",
            metadata={"agentnetReleasedArtifact": released.model_dump(mode="json")},
        )
    )
    accepted = await handler.on_message_send(request, corporate_context(sender))
    assert isinstance(accepted, Message)
    item = handler.mailbox.reconcile(
        actor=recipient,
        collaboration_scope_id=scope.scope_id,
    )[0]
    stored_part = item["payload"]["a2a_message"]["parts"][1]
    assert "url" not in stored_part
    assert stored_part["text"] == f"[AgentNet released artifact {released.artifact_id}]"
    assert item["event"]["released_artifacts"] == [released.model_dump(mode="json")]
    assert item["payload"]["artifact_references"][0]["fetch_allowed"] is False
