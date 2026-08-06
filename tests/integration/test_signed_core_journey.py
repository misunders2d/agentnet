from __future__ import annotations

import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import NAMESPACE_URL, uuid4, uuid5

import httpx
import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.http_api import create_app
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.organization import RELATIONSHIP_CONSENT_PURPOSE
from agentnet.protocol.models import Classification, Relationship
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json


def _signed(key, actor, method: str, path: str, body: bytes, *, query: str = "") -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
            method=method,
            scheme="http",
            authority="127.0.0.1",
            path=path,
            query=query,
            body=body,
        )
    )


def _allow(core: CommunicationCore, actor, action: str, resource: str) -> None:
    policy = (
        core.policy
        if isinstance(core.policy, LocalConformancePolicyEngine)
        else LocalConformancePolicyEngine(core.store)
    )
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
        )
    )

def _collaboration_scope(
    core: CommunicationCore,
    *,
    owner,
    members,
    actions: tuple[str, ...],
    resources: tuple[str, ...],
    classifications: tuple[Classification, ...],
) -> str:
    policy = LocalConformancePolicyEngine(core.store)
    revision = policy.current_policy_revision(owner)
    domain = core.store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope-signed-journey-{uuid4()}",
        scope_kind="direct",
        member_harness_ids=tuple(sorted(member.harness_id for member in members)),
        allowed_actions=tuple(sorted(actions)),
        allowed_resource_prefixes=tuple(sorted(resources)),
        allowed_classifications=tuple(
            sorted(classifications, key=lambda value: value.value)
        ),
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
            context=core.collaboration_scopes.issuance_request(
                actor=owner,
                proposal=proposal,
            ),
        )
    )
    return core.collaboration_scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=owner,
            policy_decision_id=decision.decision_id,
        ),
    ).scope_id


@pytest.mark.anyio
async def test_signed_http_core_journey_composes_delivery_ack_and_response_obligation(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Compose signed in-process HTTP boundaries without claiming native evidence."""

    laptop, laptop_key = identity_factory(kind="pi", binding_assurance="os_bound")
    server, server_key = identity_factory(kind="codex", binding_assurance="os_bound")
    conversation_id = "conversation:native-journey"
    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    artifact_dir = tmp_path / "artifacts"
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=laptop.domain_id,
        data_dir=tmp_path / "data",
        database_url="postgresql://agentnet@postgres/agentnet",
        artifact_mode="disabled",
        artifact_backend="postgres-manifest",
        artifact_dir=artifact_dir,
        public_base_url="http://127.0.0.1",
        enrolled_harness_id=server.harness_id,
        enrolled_credential_id=server.credential_id,
        server_agent_capabilities={ServerAgentCapability.OFFLINE_CUSTODY},
    )
    relationship_key = P256KeyPair.generate()
    relationship_approver = TrustedApprover(
        principal_id=server.principal_id,
        domain_id=server.domain_id,
        signer_key_id=relationship_key.thumbprint,
        public_key_pem=relationship_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    relationship_verifier = IndependentApprovalVerifier(
        {relationship_key.thumbprint: relationship_approver},
        verifier_id="signed-communication-only-relationship-consent",
    )
    core = CommunicationCore(
        config,
        store,
        approval_verifier=relationship_verifier,
    )

    _allow(core, laptop, "message.send", "*")
    _allow(core, server, "mailbox.read", server.harness_id)
    _allow(core, server, "mailbox.acknowledge", server.harness_id)
    for actor in (laptop, server):
        for action in (
            "conversation.create",
            "conversation.message.send",
            "conversation.response_obligation.create",
            "conversation.response_obligation.respond",
            "conversation.response_obligation.read",
            "conversation.response_obligation.transition",
        ):
            _allow(core, actor, action, f"conversation:{conversation_id}")
    denied_task_idempotency = "signed-disabled-artifact-task-0001"
    task_idempotency = "signed-communication-only-task-0001"
    task_resources = tuple(
        sorted(
            f"task:{uuid5(NAMESPACE_URL, f'agentnet:task:{laptop.domain_id}:{laptop.harness_id}:{idempotency_key}')}"
            for idempotency_key in (
                denied_task_idempotency,
                task_idempotency,
            )
        )
    )
    scope_id = _collaboration_scope(
        core,
        owner=laptop,
        members=(laptop, server),
        actions=(
            "message.acknowledge",
            "message.read",
            "message.send",
            "obligation.create",
            "obligation.respond",
            "task.accept",
            "task.propose",
        ),
        resources=(
            "conversation:direct",
            f"conversation:{conversation_id}",
            *task_resources,
        ),
        classifications=(Classification.C0_PUBLIC,),
    )
    mailbox_query = f"collaboration_scope_id={scope_id}"
    relationship = Relationship(
        relationship_id="signed-communication-only",
        revision=1,
        domain_id=laptop.domain_id,
        administrator_harness_id=laptop.harness_id,
        subordinate_harness_id=server.harness_id,
        may_assign=True,
        assignment_scope={
            "task_types": ["research"],
            "resources": ["catalog:communication-only"],
            "data_classes": ["C0"],
            "tools": [],
            "max_budget": 0,
            "max_duration_seconds": 1800,
            "max_concurrency": 1,
            "authority_effect": "custody_only",
        },
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    relationship_resource = f"relationship:{relationship.relationship_id}"
    _allow(core, laptop, "organization.relationship.propose", relationship_resource)
    _allow(core, laptop, "organization.relationship.read", relationship_resource)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        denied_artifact_request = {
            "collaboration_scope_id": scope_id,
            "recipients": [server.harness_id],
            "payload": {"text": "must never reach custody"},
            "idempotency_key": f"disabled-artifact-message-{uuid4()}",
            "classification": "C0",
            "released_artifacts": [
                {
                    "artifact_id": str(uuid4()),
                    "domain_id": laptop.domain_id,
                    "object_version": "a" * 64,
                    "size": 1,
                    "media_type": "text/plain",
                    "classification": "C0",
                    "release_intent_id": str(uuid4()),
                    "released_at": datetime.now(UTC).isoformat(),
                }
            ],
        }
        denied_artifact_body = canonical_json(denied_artifact_request)
        denied_artifact = await client.post(
            "/v1/messages",
            content=denied_artifact_body,
            headers={
                "Content-Type": "application/json",
                **_signed(
                    laptop_key,
                    laptop,
                    "POST",
                    "/v1/messages",
                    denied_artifact_body,
                ),
            },
        )
        assert denied_artifact.status_code == 503, denied_artifact.text
        assert denied_artifact.json()["gate"] == "artifacts_disabled"
        empty_mailbox = await client.get(
            f"/v1/mailbox?{mailbox_query}",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/mailbox",
                b"",
                query=mailbox_query,
            ),
        )
        assert empty_mailbox.status_code == 200, empty_mailbox.text
        assert empty_mailbox.json()["items"] == []

        message_request = {
            "collaboration_scope_id": scope_id,
            "recipients": [server.harness_id],
            "payload": {
                "text": "native journey delivery",
                "principal_id": "payload-spoof.invalid",
            },
            "idempotency_key": f"native-journey-message-{uuid4()}",
            "classification": "C0",
        }
        message_body = canonical_json(message_request)
        accepted = await client.post(
            "/v1/messages",
            content=message_body,
            headers={
                "Content-Type": "application/json",
                **_signed(laptop_key, laptop, "POST", "/v1/messages", message_body),
            },
        )
        assert accepted.status_code == 202, accepted.text
        accepted_value = accepted.json()

        duplicate = await client.post(
            "/v1/messages",
            content=message_body,
            headers={
                "Content-Type": "application/json",
                **_signed(laptop_key, laptop, "POST", "/v1/messages", message_body),
            },
        )
        assert duplicate.status_code == 202, duplicate.text
        assert duplicate.json()["duplicate"] is True
        assert duplicate.json()["event_id"] == accepted_value["event_id"]
        assert duplicate.json()["envelope_digest"] == accepted_value["envelope_digest"]

        mailbox = await client.get(
            f"/v1/mailbox?{mailbox_query}",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/mailbox",
                b"",
                query=mailbox_query,
            ),
        )
        assert mailbox.status_code == 200, mailbox.text
        items = mailbox.json()["items"]
        assert len(items) == 1
        event = items[0]["event"]
        assert event["event_id"] == accepted_value["event_id"]
        assert event["actor"]["principal_id"] == laptop.principal_id
        assert event["actor"]["harness_id"] == laptop.harness_id
        assert event["actor"]["principal_id"] != message_request["payload"]["principal_id"]

        ack_path = f"/v1/mailbox/{accepted_value['event_id']}/acknowledge"
        ack_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "envelope_digest": accepted_value["envelope_digest"],
            }
        )
        acknowledged = await client.post(
            ack_path,
            content=ack_body,
            headers={
                "Content-Type": "application/json",
                **_signed(server_key, server, "POST", ack_path, ack_body),
            },
        )
        assert acknowledged.status_code == 200, acknowledged.text
        assert acknowledged.json()["fact"] == "recipient_committed"
        assert acknowledged.json()["duplicate"] is False
        assert set(acknowledged.json()).isdisjoint({"processing", "effect"})

        duplicate_ack = await client.post(
            ack_path,
            content=ack_body,
            headers={
                "Content-Type": "application/json",
                **_signed(server_key, server, "POST", ack_path, ack_body),
            },
        )
        assert duplicate_ack.status_code == 200, duplicate_ack.text
        assert duplicate_ack.json()["duplicate"] is True
        assert duplicate_ack.json()["receipt_id"] == acknowledged.json()["receipt_id"]

        proposal_expires_at = datetime.now(UTC) + timedelta(minutes=5)
        relationship_body = canonical_json(
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            }
        )
        proposed = await client.post(
            "/v1/relationships",
            content=relationship_body,
            headers={
                "Content-Type": "application/json",
                **_signed(
                    laptop_key,
                    laptop,
                    "POST",
                    "/v1/relationships",
                    relationship_body,
                ),
            },
        )
        assert proposed.status_code == 201, proposed.text
        proposal = proposed.json()["proposal"]
        issued_at = int(time.time())
        relationship_approval = create_independent_approval_receipt(
            relationship_key,
            approver=relationship_approver,
            verifier_id=relationship_verifier.verifier_id,
            approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
            canonical_transaction=canonical_json(proposal["consent_transaction"]),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )
        accept_path = f"/v1/relationships/{relationship.relationship_id}/accept"
        accept_body = canonical_json(
            {
                "approval": relationship_approval,
                "expected_transaction_digest": proposal["transaction_digest"],
                "expected_relationship_revision": proposal["revision"],
                "expected_lifecycle_revision": proposal["lifecycle_revision"],
            }
        )
        activated = await client.post(
            accept_path,
            content=accept_body,
            headers={
                "Content-Type": "application/json",
                **_signed(server_key, server, "POST", accept_path, accept_body),
            },
        )
        assert activated.status_code == 200, activated.text
        assert activated.json()["relationship"]["lifecycle_state"] == "active"

        task_template = {
            "collaboration_scope_id": scope_id,
            "recipient_harness_id": server.harness_id,
            "task_type": "research",
            "resources": ["catalog:communication-only"],
            "data_classes": ["C0"],
            "tools": [],
            "budget": 0,
            "concurrency": 1,
            "expected_relationship_revision": 1,
            "task_payload": {"instruction": "record custody only; do not execute"},
        }
        denied_task_request = {
            **task_template,
            "released_artifacts": denied_artifact_request["released_artifacts"],
            "idempotency_key": denied_task_idempotency,
        }
        denied_task_body = canonical_json(denied_task_request)
        denied_task = await client.post(
            "/v1/tasks/assign",
            content=denied_task_body,
            headers={
                "Content-Type": "application/json",
                **_signed(
                    laptop_key,
                    laptop,
                    "POST",
                    "/v1/tasks/assign",
                    denied_task_body,
                ),
            },
        )
        assert denied_task.status_code == 503, denied_task.text
        assert denied_task.json()["gate"] == "artifacts_disabled"

        task_request = {
            **task_template,
            "released_artifacts": [],
            "idempotency_key": task_idempotency,
        }
        task_body = canonical_json(task_request)
        assigned = await client.post(
            "/v1/tasks/assign",
            content=task_body,
            headers={
                "Content-Type": "application/json",
                **_signed(
                    laptop_key,
                    laptop,
                    "POST",
                    "/v1/tasks/assign",
                    task_body,
                ),
            },
        )
        assert assigned.status_code == 202, assigned.text
        assert assigned.json()["fact"] == "accepted_queued"
        assert assigned.json()["data_access_authorized"] is False
        assert assigned.json()["effect_authorized"] is False
        task_event_id = assigned.json()["event_id"]
        task_mailbox = await client.get(
            f"/v1/mailbox?{mailbox_query}",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/mailbox",
                b"",
                query=mailbox_query,
            ),
        )
        task_items = [
            item
            for item in task_mailbox.json()["items"]
            if item["event"]["event_id"] == task_event_id
        ]
        assert len(task_items) == 1
        assert task_items[0]["fact"] == "accepted_queued"
        assert task_items[0]["payload_available"] is False
        assert task_items[0]["payload_access"] == "task_grant_required"

        create_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "conversation_id": conversation_id,
                "member_harness_ids": [server.harness_id],
                "classification": "C0",
            }
        )
        created = await client.post(
            "/v1/conversations",
            content=create_body,
            headers={
                "Content-Type": "application/json",
                **_signed(laptop_key, laptop, "POST", "/v1/conversations", create_body),
            },
        )
        assert created.status_code == 201, created.text

        action_path = f"/v1/conversations/{conversation_id}/actions"
        request_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "recipients": [server.harness_id],
                "thread_id": "thread:native-journey",
                "action": {
                    "kind": "post",
                    "body": "return exact terminal proof",
                    "response_obligation": {},
                },
                "idempotency_key": "native-journey-obligation-request-0001",
            }
        )
        requested = await client.post(
            action_path,
            content=request_body,
            headers={
                "Content-Type": "application/json",
                **_signed(laptop_key, laptop, "POST", action_path, request_body),
            },
        )
        assert requested.status_code == 202, requested.text
        obligation = requested.json()["response_obligation"]
        obligation_id = obligation["obligation_id"]
        request_event_id = requested.json()["event_id"]
        assert obligation["state"] == "created"

        request_mailbox = await client.get(
            f"/v1/mailbox?{mailbox_query}",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/mailbox",
                b"",
                query=mailbox_query,
            ),
        )
        assert request_mailbox.status_code == 200, request_mailbox.text
        request_items = [
            item
            for item in request_mailbox.json()["items"]
            if item["event"]["event_id"] == request_event_id
        ]
        assert len(request_items) == 1
        assert request_items[0]["envelope_digest"] == requested.json()["envelope_digest"]

        request_ack_path = f"/v1/mailbox/{request_event_id}/acknowledge"
        request_ack_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "envelope_digest": requested.json()["envelope_digest"],
            }
        )
        request_ack = await client.post(
            request_ack_path,
            content=request_ack_body,
            headers={
                "Content-Type": "application/json",
                **_signed(
                    server_key,
                    server,
                    "POST",
                    request_ack_path,
                    request_ack_body,
                ),
            },
        )
        assert request_ack.status_code == 200, request_ack.text
        assert request_ack.json()["fact"] == "recipient_committed"
        assert request_ack.json()["duplicate"] is False

        obligation_inbox_query = f"collaboration_scope_id={scope_id}"
        obligation_inbox = await client.get(
            f"/v1/response-obligations/inbox?{obligation_inbox_query}",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/response-obligations/inbox",
                b"",
                query=obligation_inbox_query,
            ),
        )
        assert obligation_inbox.status_code == 200, obligation_inbox.text
        assert obligation_inbox.json()["action_required"] == 1

        show_path = f"/v1/response-obligations/{obligation_id}"
        show_query = f"collaboration_scope_id={scope_id}"
        shown = await client.get(
            f"{show_path}?{show_query}",
            headers=_signed(
                laptop_key,
                laptop,
                "GET",
                show_path,
                b"",
                query=show_query,
            ),
        )
        assert shown.status_code == 200, shown.text
        request_digest = shown.json()["request_payload_digest"]

        transition_path = f"/v1/response-obligations/{obligation_id}/transition"
        transition_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "to_state": "acknowledged",
            }
        )
        progressed = await client.post(
            transition_path,
            content=transition_body,
            headers={
                "Content-Type": "application/json",
                **_signed(server_key, server, "POST", transition_path, transition_body),
            },
        )
        assert progressed.status_code == 200, progressed.text
        assert progressed.json()["state"] == "acknowledged"

        response_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "recipients": [laptop.harness_id],
                "thread_id": "thread:native-journey",
                "action": {
                    "kind": "obligation_response",
                    "obligation_id": obligation_id,
                    "request_event_id": request_event_id,
                    "request_digest": request_digest,
                    "outcome": "completed",
                    "body": "terminal proof returned",
                },
                "idempotency_key": "native-journey-obligation-response-0001",
            }
        )
        completed = await client.post(
            action_path,
            content=response_body,
            headers={
                "Content-Type": "application/json",
                **_signed(server_key, server, "POST", action_path, response_body),
            },
        )
        assert completed.status_code == 202, completed.text
        assert completed.json()["response_obligation"]["state"] == "completed"

        query = (
            f"role=requester&limit=10&collaboration_scope_id={scope_id}"
        )
        listed = await client.get(
            f"/v1/response-obligations?{query}",
            headers=_signed(
                laptop_key,
                laptop,
                "GET",
                "/v1/response-obligations",
                b"",
                query=query,
            ),
        )
        assert listed.status_code == 200, listed.text
        listed_items = listed.json()["items"]
        assert len(listed_items) == 1
        assert listed_items[0]["state"] == "completed"
        assert listed_items[0]["response_event_id"] == completed.json()["event_id"]

    assert not artifact_dir.exists()
    assert not (config.data_dir / "secrets" / "artifact.key").exists()
