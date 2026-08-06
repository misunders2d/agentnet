from __future__ import annotations

from datetime import UTC, datetime, timedelta
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
from agentnet.messaging.events import new_event
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.models import Classification, EventType
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import canonical_json


def _signed(key, actor, method: str, path: str, body: bytes) -> dict[str, str]:
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
            query="",
            body=body,
        )
    )


def _count(store, table: str) -> int:
    return int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])

def _collaboration_scope(
    core: CommunicationCore,
    *,
    owner,
    members,
    actions: tuple[str, ...],
    resources: tuple[str, ...],
    classifications: tuple[Classification, ...],
):
    revision = core.policy.current_policy_revision(owner)
    domain = core.store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=f"scope-http-ack-{uuid4()}",
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
    )


@pytest.mark.anyio
async def test_signed_mailbox_acknowledgement_is_exact_replay_safe_and_non_escalating(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    sender, _ = identity_factory(kind="codex", binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="pi", binding_assurance="os_bound")
    outsider, outsider_key = identity_factory(kind="claude")
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
            domain_id=recipient.domain_id,
            principal_id=recipient.principal_id,
            action="mailbox.acknowledge",
            resource_pattern=recipient.harness_id,
            revision=1,
        )
    )
    scope = _collaboration_scope(
        core,
        owner=sender,
        members=(sender, recipient),
        actions=("message.acknowledge",),
        resources=("conversation:direct",),
        classifications=(Classification.C1_INTERNAL,),
    )
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={
            "text": "persist before acknowledging",
            "authorization_context": scope.authorization_context(),
        },
        idempotency_key=f"http-mailbox-ack-{uuid4()}",
        recipients=(recipient.harness_id,),
        policy_revision=scope.policy_revision,
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
    )
    accepted = core.mailboxes.accept(event)
    path = f"/v1/mailbox/{event.event_id}/acknowledge"
    body = canonical_json(
        {
            "collaboration_scope_id": scope.scope_id,
            "envelope_digest": accepted["envelope_digest"],
        }
    )
    app = create_app(core)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        first_headers = _signed(recipient_key, recipient, "POST", path, body)
        first = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **first_headers},
        )
        assert first.status_code == 200
        assert first.json()["fact"] == "recipient_committed"
        assert first.json()["duplicate"] is False
        assert set(first.json()).isdisjoint({"payload", "processing", "effect"})
        receipt_count = _count(store, "receipts")

        replay = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **first_headers},
        )
        assert replay.status_code == 401
        assert _count(store, "receipts") == receipt_count

        duplicate = await client.post(
            path,
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(recipient_key, recipient, "POST", path, body),
            },
        )
        assert duplicate.status_code == 200
        assert duplicate.json()["duplicate"] is True
        assert duplicate.json()["receipt_id"] == first.json()["receipt_id"]
        assert _count(store, "receipts") == receipt_count

        spoofed_body = canonical_json(
            {
                "collaboration_scope_id": scope.scope_id,
                "envelope_digest": accepted["envelope_digest"],
                "recipient_id": outsider.harness_id,
            }
        )
        spoofed = await client.post(
            path,
            content=spoofed_body,
            headers={
                "Content-Type": "application/json",
                **_signed(recipient_key, recipient, "POST", path, spoofed_body),
            },
        )
        assert spoofed.status_code == 422

        wrong_actor = await client.post(
            path,
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(outsider_key, outsider, "POST", path, body),
            },
        )
        assert wrong_actor.status_code in {403, 404}

        altered = canonical_json(
            {
                "collaboration_scope_id": scope.scope_id,
                "envelope_digest": "0" * 64,
            }
        )
        body_substitution = await client.post(
            path,
            content=altered,
            headers={
                "Content-Type": "application/json",
                **_signed(recipient_key, recipient, "POST", path, body),
            },
        )
        assert body_substitution.status_code == 401

        unsafe_path = "/v1/mailbox/event%25bad/acknowledge"
        unsafe_event_id = await client.post(
            unsafe_path,
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(recipient_key, recipient, "POST", unsafe_path, body),
            },
        )
        assert unsafe_event_id.status_code == 422

        with store.transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (recipient.credential_id,),
            )
        revoked = await client.post(
            path,
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(recipient_key, recipient, "POST", path, body),
            },
        )
        assert revoked.status_code == 401

    row = store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (event.event_id, recipient.harness_id),
    )
    assert row["current_fact"] == "recipient_committed"
    assert store.fetch_all("SELECT * FROM effect_reservations", ()) == []
