from __future__ import annotations

from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.authorization.policy import HumanEntitlement
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.http_api import create_app
from agentnet.operations.config import ExtensionConfig
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import canonical_json


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
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
        )
    )


@pytest.mark.anyio
async def test_signed_http_core_journey_composes_delivery_ack_and_response_obligation(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    """Compose signed in-process HTTP boundaries without claiming native evidence."""

    laptop, laptop_key = identity_factory(kind="pi")
    server, server_key = identity_factory(kind="codex")
    conversation_id = "conversation:native-journey"
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=laptop.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
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
            "conversation.response_obligation.update",
        ):
            _allow(core, actor, action, f"conversation:{conversation_id}")

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        message_request = {
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
            "/v1/mailbox",
            headers=_signed(server_key, server, "GET", "/v1/mailbox", b""),
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
        ack_body = canonical_json({"envelope_digest": accepted_value["envelope_digest"]})
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

        create_body = canonical_json(
            {
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
            "/v1/mailbox",
            headers=_signed(server_key, server, "GET", "/v1/mailbox", b""),
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
            {"envelope_digest": requested.json()["envelope_digest"]}
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

        obligation_inbox = await client.get(
            "/v1/response-obligations/inbox",
            headers=_signed(
                server_key,
                server,
                "GET",
                "/v1/response-obligations/inbox",
                b"",
            ),
        )
        assert obligation_inbox.status_code == 200, obligation_inbox.text
        assert obligation_inbox.json()["action_required"] == 1

        show_path = f"/v1/response-obligations/{obligation_id}"
        shown = await client.get(
            show_path,
            headers=_signed(laptop_key, laptop, "GET", show_path, b""),
        )
        assert shown.status_code == 200, shown.text
        request_digest = shown.json()["request_payload_digest"]

        transition_path = f"/v1/response-obligations/{obligation_id}/transition"
        transition_body = canonical_json({"to_state": "acknowledged"})
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

        query = "role=requester&limit=10"
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
