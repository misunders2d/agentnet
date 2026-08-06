from __future__ import annotations

from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest, HumanEntitlement
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.http_api import create_app
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.models import Classification
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import canonical_json


def signed(key, actor, method: str, path: str, body: bytes, *, query: str = "") -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
            method=method,
            scheme="http",
            authority="127.0.0.1:19101",
            path=path,
            query=query,
            body=body,
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
    revision = core.policy.current_policy_revision(owner)
    domain = core.store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope-conversation-http-{uuid4()}",
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
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=owner.domain_id,
            principal_id=owner.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=resource,
            revision=revision,
        )
    )
    decision = core.policy.require(
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
async def test_signed_http_conversation_create_post_and_thread_round_trip(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    creator, creator_key = identity_factory(binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="pi", binding_assurance="os_bound")
    conversation_id = "conversation:http-e2e"
    config = ExtensionConfig(
        domain_id=creator.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1:19101",
    )
    core = CommunicationCore(config, store)
    for action in ("conversation.create", "conversation.message.send"):
        core.policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=creator.domain_id,
                principal_id=creator.principal_id,
                action=action,
                resource_pattern=f"conversation:{conversation_id}",
                revision=1,
            )
        )
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=recipient.domain_id,
            principal_id=recipient.principal_id,
            action="conversation.thread",
            resource_pattern="*",
            revision=1,
        )
    )
    scope_id = _collaboration_scope(
        core,
        owner=creator,
        members=(creator, recipient),
        actions=("message.read", "message.send"),
        resources=(f"conversation:{conversation_id}",),
        classifications=(Classification.C0_PUBLIC,),
    )
    app = create_app(core)
    transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1:19101") as client:
        create_value = {
            "collaboration_scope_id": scope_id,
            "conversation_id": conversation_id,
            "member_harness_ids": [recipient.harness_id],
            "classification": "C0",
        }
        create_body = canonical_json(create_value)
        response = await client.post(
            "/v1/conversations",
            content=create_body,
            headers={"Content-Type": "application/json", **signed(creator_key, creator, "POST", "/v1/conversations", create_body)},
        )
        assert response.status_code == 201

        action_path = f"/v1/conversations/{conversation_id}/actions"
        action_value = {
            "collaboration_scope_id": scope_id,
            "recipients": [recipient.harness_id],
            "thread_id": "thread:http-e2e",
            "action": {"kind": "post", "body": "durable background hello"},
            "idempotency_key": "conversation-http-action-0001",
        }
        action_body = canonical_json(action_value)
        posted = await client.post(
            action_path,
            content=action_body,
            headers={"Content-Type": "application/json", **signed(creator_key, creator, "POST", action_path, action_body)},
        )
        assert posted.status_code == 202, posted.text
        assert posted.json()["fact"] == "accepted_local"

        thread_path = f"/v1/conversations/{conversation_id}/threads/thread:http-e2e"
        query = f"limit=10&collaboration_scope_id={scope_id}"
        fetched = await client.get(
            f"{thread_path}?{query}",
            headers=signed(recipient_key, recipient, "GET", thread_path, b"", query=query),
        )
        assert fetched.status_code == 200
        assert fetched.json()["items"][0]["payload"] == {
            "authorization_context": core.collaboration_scopes.get_for_actor(
                actor=recipient,
                scope_id=scope_id,
            ).authorization_context(),
            "kind": "post",
            "body": "durable background hello",
            "mentions": [],
        }
