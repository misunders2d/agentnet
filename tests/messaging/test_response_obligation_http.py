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
            authority="127.0.0.1:19102",
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
        scope_id=f"scope-obligation-http-{uuid4()}",
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
async def test_signed_http_response_obligation_lifecycle(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    requester, requester_key = identity_factory(binding_assurance="os_bound")
    responder, responder_key = identity_factory(kind="pi", binding_assurance="os_bound")
    conversation_id = "conversation:obligation-http"
    config = ExtensionConfig(
        domain_id=requester.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1:19102",
    )
    core = CommunicationCore(config, store)
    for actor in (requester, responder):
        for action in (
            "conversation.create",
            "conversation.message.send",
            "conversation.response_obligation.respond",
            "conversation.response_obligation.create",
            "conversation.response_obligation.read",
            "conversation.response_obligation.transition",
        ):
            core.policy.bootstrap_entitlement_for_local_conformance(
                HumanEntitlement(
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id,
                    action=action,
                    resource_pattern=(
                        "*" if action == "conversation.response_obligation.read"
                        else f"conversation:{conversation_id}"
                    ),
                    revision=1,
                )
            )
    scope_id = _collaboration_scope(
        core,
        owner=requester,
        members=(requester, responder),
        actions=(
            "message.read",
            "message.send",
            "obligation.create",
            "obligation.respond",
        ),
        resources=(f"conversation:{conversation_id}",),
        classifications=(Classification.C0_PUBLIC,),
    )
    app = create_app(core)
    transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1:19102") as client:
        create_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "conversation_id": conversation_id,
                "member_harness_ids": [responder.harness_id],
                "classification": "C0",
            }
        )
        response = await client.post(
            "/v1/conversations",
            content=create_body,
            headers={
                "Content-Type": "application/json",
                **signed(requester_key, requester, "POST", "/v1/conversations", create_body),
            },
        )
        assert response.status_code == 201

        action_path = f"/v1/conversations/{conversation_id}/actions"
        request_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "recipients": [responder.harness_id],
                "thread_id": "thread:obligation-http",
                "action": {
                    "kind": "post",
                    "body": "please answer over http",
                    "response_obligation": {},
                },
                "idempotency_key": "obligation-http-request-0001",
            }
        )
        posted = await client.post(
            action_path,
            content=request_body,
            headers={
                "Content-Type": "application/json",
                **signed(requester_key, requester, "POST", action_path, request_body),
            },
        )
        assert posted.status_code == 202, posted.text
        obligation = posted.json()["response_obligation"]
        obligation_id = obligation["obligation_id"]
        request_event_id = posted.json()["event_id"]
        assert obligation["state"] == "created"

        inbox_query = f"collaboration_scope_id={scope_id}"
        inbox = await client.get(
            f"/v1/response-obligations/inbox?{inbox_query}",
            headers=signed(
                responder_key,
                responder,
                "GET",
                "/v1/response-obligations/inbox",
                b"",
                query=inbox_query,
            ),
        )
        assert inbox.status_code == 200
        assert inbox.json()["action_required"] == 1

        show_path = f"/v1/response-obligations/{obligation_id}"
        show_query = f"collaboration_scope_id={scope_id}"
        shown = await client.get(
            f"{show_path}?{show_query}",
            headers=signed(
                requester_key,
                requester,
                "GET",
                show_path,
                b"",
                query=show_query,
            ),
        )
        assert shown.status_code == 200
        request_digest = shown.json()["request_payload_digest"]

        transition_path = f"/v1/response-obligations/{obligation_id}/transition"
        transition_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "to_state": "acknowledged",
            }
        )
        acked = await client.post(
            transition_path,
            content=transition_body,
            headers={
                "Content-Type": "application/json",
                **signed(responder_key, responder, "POST", transition_path, transition_body),
            },
        )
        assert acked.status_code == 200
        assert acked.json()["state"] == "acknowledged"

        # The requester cannot assert recipient progress over HTTP either;
        # authorization failures are non-disclosing (404).
        denied = await client.post(
            transition_path,
            content=transition_body,
            headers={
                "Content-Type": "application/json",
                **signed(requester_key, requester, "POST", transition_path, transition_body),
            },
        )
        assert denied.status_code == 404

        response_body = canonical_json(
            {
                "collaboration_scope_id": scope_id,
                "recipients": [requester.harness_id],
                "thread_id": "thread:obligation-http",
                "action": {
                    "kind": "obligation_response",
                    "obligation_id": obligation_id,
                    "request_event_id": request_event_id,
                    "request_digest": request_digest,
                    "outcome": "completed",
                    "body": "the http answer",
                },
                "idempotency_key": "obligation-http-response-0001",
            }
        )
        closed = await client.post(
            action_path,
            content=response_body,
            headers={
                "Content-Type": "application/json",
                **signed(responder_key, responder, "POST", action_path, response_body),
            },
        )
        assert closed.status_code == 202
        assert closed.json()["response_obligation"]["state"] == "completed"

        list_query = (
            f"role=requester&limit=10&collaboration_scope_id={scope_id}"
        )
        listed = await client.get(
            f"/v1/response-obligations?{list_query}",
            headers=signed(
                requester_key,
                requester,
                "GET",
                "/v1/response-obligations",
                b"",
                query=list_query,
            ),
        )
        assert listed.status_code == 200
        items = listed.json()["items"]
        assert len(items) == 1 and items[0]["state"] == "completed"
        assert items[0]["response_event_id"] == closed.json()["event_id"]
