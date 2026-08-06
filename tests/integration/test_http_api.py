from __future__ import annotations

import asyncio
from concurrent.futures import CancelledError
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


def _signed(
    key,
    actor,
    method: str,
    path: str,
    body: bytes,
    *,
    query: str = "",
    audience: str | None = None,
    scheme: str = "http",
    authority: str = "127.0.0.1",
) -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=audience or f"urn:agentnet:{actor.domain_id}:corporate-api",
            method=method,
            scheme=scheme,
            authority=authority,
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
        scope_id=f"scope-http-{uuid4()}",
        scope_kind="personal" if len(members) == 1 else "direct" if len(members) == 2 else "shared",
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
async def test_signed_http_round_trip_and_payload_identity_spoof_is_ignored(store, identity_factory, tmp_path: Path) -> None:
    sender, sender_key = identity_factory(binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="pi")
    config = ExtensionConfig(
        domain_id=sender.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
    )
    core = CommunicationCore(config, store)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="*",
            revision=1,
        )
    )
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=recipient.domain_id,
            principal_id=recipient.principal_id,
            action="mailbox.read",
            resource_pattern=recipient.harness_id,
            revision=1,
        )
    )
    scope_id = _collaboration_scope(
        core,
        owner=sender,
        members=(sender, recipient),
        actions=("message.read", "message.send"),
        resources=("conversation:direct",),
        classifications=(Classification.C0_PUBLIC,),
    )
    app = create_app(core)
    transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
    request = {
        "collaboration_scope_id": scope_id,
        "recipients": [recipient.harness_id],
        "payload": {"text": "hello", "caller_email": "attacker@example.invalid"},
        "idempotency_key": f"http-message-{uuid4()}",
        "classification": "C0",
    }
    body = canonical_json(request)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1") as client:
        response = await client.post(
            "/v1/messages",
            content=body,
            headers={"Content-Type": "application/json", **_signed(sender_key, sender, "POST", "/v1/messages", body)},
        )
        assert response.status_code == 202
        accepted = response.json()

        retry = await client.post(
            "/v1/messages",
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(sender_key, sender, "POST", "/v1/messages", body),
            },
        )
        assert retry.status_code == 202
        assert retry.json()["duplicate"] is True
        assert retry.json()["event_id"] == accepted["event_id"]
        assert retry.json()["envelope_digest"] == accepted["envelope_digest"]

        changed = canonical_json(request | {"payload": {"text": "different intent"}})
        conflict = await client.post(
            "/v1/messages",
            content=changed,
            headers={
                "Content-Type": "application/json",
                **_signed(sender_key, sender, "POST", "/v1/messages", changed),
            },
        )
        assert conflict.status_code == 409

        watch_query = (
            f"after=0&wait_ms=50&collaboration_scope_id={scope_id}"
        )
        watch = await asyncio.wait_for(
            client.get(
                f"/v1/mailbox/watch?{watch_query}",
                headers=_signed(
                    recipient_key,
                    recipient,
                    "GET",
                    "/v1/mailbox/watch",
                    b"",
                    query=watch_query,
                ),
            ),
            timeout=2,
        )
        assert watch.status_code == 200
        assert watch.headers["cache-control"] == "no-store"
        wake_value = watch.json()
        assert set(wake_value) == {"schema", "kind", "cursor_hint"}
        assert wake_value["schema"] == "agentnet.mailbox-wake.v1"
        assert wake_value["kind"] == "wake"
        assert type(wake_value["cursor_hint"]) is int
        assert "hello" not in watch.text
        assert accepted["event_id"] not in watch.text

        inbox_query = f"collaboration_scope_id={scope_id}"
        headers = _signed(
            recipient_key,
            recipient,
            "GET",
            "/v1/mailbox",
            b"",
            query=inbox_query,
        )
        inbox = await client.get(f"/v1/mailbox?{inbox_query}", headers=headers)
        assert inbox.status_code == 200
        actor = inbox.json()["items"][0]["event"]["actor"]
        assert actor["principal_id"] == sender.principal_id
        assert actor["harness_id"] == sender.harness_id
        assert actor.get("verified_email") is None

        cursor = inbox.json()["items"][0]["cursor"]
        assert wake_value["cursor_hint"] == cursor
        idle_query = (
            f"after={cursor}&wait_ms=50&collaboration_scope_id={scope_id}"
        )
        idle = await asyncio.wait_for(
            client.get(
                f"/v1/mailbox/watch?{idle_query}",
                headers=_signed(
                    recipient_key,
                    recipient,
                    "GET",
                    "/v1/mailbox/watch",
                    b"",
                    query=idle_query,
                ),
            ),
            timeout=2,
        )
        assert idle.json() == {
            "schema": "agentnet.mailbox-wake.v1",
            "kind": "idle",
            "cursor_hint": cursor,
        }

        live_query = (
            f"after={cursor}&wait_ms=1000&collaboration_scope_id={scope_id}"
        )
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
            base_url="http://127.0.0.1",
        ) as watch_client:
            live_watch = asyncio.create_task(
                watch_client.get(
                    f"/v1/mailbox/watch?{live_query}",
                    headers=_signed(
                        recipient_key,
                        recipient,
                        "GET",
                        "/v1/mailbox/watch",
                        b"",
                        query=live_query,
                    ),
                )
            )
            await asyncio.sleep(0.05)
            later_request = request | {
                "payload": {"text": "arrived while watched"},
                "idempotency_key": f"http-message-{uuid4()}",
            }
            later_body = canonical_json(later_request)
            later = await asyncio.wait_for(
                client.post(
                    "/v1/messages",
                    content=later_body,
                    headers={
                        "Content-Type": "application/json",
                        **_signed(sender_key, sender, "POST", "/v1/messages", later_body),
                    },
                ),
                timeout=2,
            )
            assert later.status_code == 202
            live_value = (await asyncio.wait_for(live_watch, timeout=2)).json()
        assert live_value["kind"] == "wake"
        assert live_value["cursor_hint"] > cursor
        assert "arrived while watched" not in canonical_json(live_value).decode("utf-8")

        unsigned_watch = await client.get(
            f"/v1/mailbox/watch?after=0&wait_ms=50&collaboration_scope_id={scope_id}"
        )
        assert unsigned_watch.status_code == 401

        for invalid_watch_query in (
            f"after=0&wait_ms=50&collaboration_scope_id={scope_id}&extra=1",
            f"after=0&after=0&wait_ms=50&collaboration_scope_id={scope_id}",
            f"after=01&wait_ms=50&collaboration_scope_id={scope_id}",
        ):
            invalid_watch = await client.get(
                f"/v1/mailbox/watch?{invalid_watch_query}",
                headers=_signed(
                    recipient_key,
                    recipient,
                    "GET",
                    "/v1/mailbox/watch",
                    b"",
                    query=invalid_watch_query,
                ),
            )
            assert invalid_watch.status_code == 422
            assert invalid_watch.headers["cache-control"] == "no-store"

        replay = await client.get("/v1/mailbox", headers=headers)
        assert replay.status_code == 401

        query = f"after=0&limit=1&collaboration_scope_id={scope_id}"
        query_headers = _signed(
            recipient_key,
            recipient,
            "GET",
            "/v1/mailbox",
            b"",
            query=query,
        )
        query_response = await client.get(
            f"/v1/mailbox?{query}",
            headers=query_headers,
        )
        assert query_response.status_code == 200

        wrong_query_value = f"after=0&limit=2&collaboration_scope_id={scope_id}"
        wrong_query = _signed(
            recipient_key,
            recipient,
            "GET",
            "/v1/mailbox",
            b"",
            query=wrong_query_value,
        )
        assert (
            await client.get(f"/v1/mailbox?{query}", headers=wrong_query)
        ).status_code == 401

        wrong_audience = _signed(
            recipient_key,
            recipient,
            "GET",
            "/v1/mailbox",
            b"",
            query=inbox_query,
            audience="urn:agentnet:other-service",
        )
        assert (
            await client.get(f"/v1/mailbox?{inbox_query}", headers=wrong_audience)
        ).status_code == 401

        revoked_query = (
            f"after={live_value['cursor_hint']}&wait_ms=100"
            f"&collaboration_scope_id={scope_id}"
        )
        revoked_watch = asyncio.create_task(
            client.get(
                f"/v1/mailbox/watch?{revoked_query}",
                headers=_signed(
                    recipient_key,
                    recipient,
                    "GET",
                    "/v1/mailbox/watch",
                    b"",
                    query=revoked_query,
                ),
            )
        )
        await asyncio.sleep(0.02)
        with store.transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (recipient.credential_id,),
            )
        revoked_response = await asyncio.wait_for(revoked_watch, timeout=2)
        assert revoked_response.status_code in {401, 403, 404}
        assert revoked_response.headers["cache-control"] == "no-store"
        assert "arrived while watched" not in revoked_response.text
        assert core.mailboxes._wake_subscribers == {}


@pytest.mark.anyio
async def test_every_protected_v1_error_is_non_cacheable_and_router_errors_keep_privacy_headers(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, key = identity_factory()
    config = ExtensionConfig(
        domain_id=actor.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
    )
    app = create_app(CommunicationCore(config, store))
    malformed = b'{"recipients":[]}'
    protected_headers = {
        "cache-control": "no-store",
        "pragma": "no-cache",
        "referrer-policy": "no-referrer",
        "x-content-type-options": "nosniff",
        "cross-origin-resource-policy": "same-origin",
        "x-frame-options": "DENY",
    }
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        responses = (
            await client.get("/v1/private-unknown"),
            await client.put("/v1/messages"),
            await client.get("/v1/mailbox"),
            await client.post(
                "/v1/messages",
                content=malformed,
                headers={
                    "Content-Type": "application/json",
                    **_signed(key, actor, "POST", "/v1/messages", malformed),
                },
            ),
        )
        assert [response.status_code for response in responses] == [404, 405, 401, 422]
        for response in responses:
            assert {name: response.headers[name] for name in protected_headers} == protected_headers

        health = await client.get("/healthz")
        assert health.status_code == 200
        assert health.json()["schema"] == "agentnet.core.health.v1"
        assert health.json()["service"] == "agentnet-core"
        assert health.json()["status"] == "alive"
        assert health.json()["domain_id"] == actor.domain_id
        assert "cache-control" not in health.headers


@pytest.mark.anyio
async def test_request_cancellation_is_never_translated_into_an_internal_error(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch,
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(kind="pi")
    config = ExtensionConfig(
        domain_id=actor.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
    )
    core = CommunicationCore(config, store)
    scope_id = _collaboration_scope(
        core,
        owner=actor,
        members=(actor, recipient),
        actions=("message.send",),
        resources=("conversation:direct",),
        classifications=(Classification.C0_PUBLIC,),
    )

    def cancelled(**_kwargs):
        raise CancelledError("server shutdown")

    monkeypatch.setattr(core, "send_message", cancelled)
    request = {
        "collaboration_scope_id": scope_id,
        "recipients": [recipient.harness_id],
        "payload": {"text": "never processed"},
        "idempotency_key": f"cancelled-message-{uuid4()}",
        "classification": "C0",
    }
    body = canonical_json(request)
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=True),
        base_url="http://127.0.0.1",
    ) as client:
        with pytest.raises(CancelledError, match="shutdown"):
            await client.post(
                "/v1/messages",
                content=body,
                headers={
                    "Content-Type": "application/json",
                    **_signed(key, actor, "POST", "/v1/messages", body),
                },
            )
