from __future__ import annotations

import hashlib
import json
import time
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    CommunicationScopeBeginRequest,
    CommunicationScopeCompleteRequest,
    CommunicationScopeStatusRequest,
)
from agentnet.authorization.communication_scope_service import CommunicationScopeService
from agentnet.bindings.tools import CanonicalToolDispatcher
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import AuthorizationError, GateBlocked
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.endpoint_lifecycle import EndpointLifecycleService
from agentnet.protocol.models import Classification, DeliveryFact, ReleasedArtifactBinding
from agentnet.security.signatures import P256KeyPair


class _IssuedApprovalBoundary:
    """In-process signed Approval boundary; authorization and Core stay production-real."""

    def __init__(
        self,
        *,
        key: P256KeyPair,
        approver: TrustedApprover,
        verifier: IndependentApprovalVerifier,
        now: int,
    ) -> None:
        self.key = key
        self.approver = approver
        self.verifier = verifier
        self.now = now
        self.canonical_transaction: bytes | None = None
        self.transaction_digest: str | None = None
        self.possession_hash: str | None = None
        self.request_expires_at: int | None = None
        self.request_id = "approval-request-persistent-communication"

    def create_request(self, **values: Any) -> dict[str, Any]:
        self.canonical_transaction = values["canonical_transaction"]
        self.transaction_digest = values["transaction_digest"]
        self.possession_hash = values["possession_hash"]
        self.request_expires_at = values["request_expires_at"]
        return {
            "request_id": self.request_id,
            "transaction_digest": self.transaction_digest,
            "expires_at": self.request_expires_at,
            "state": "pending",
        }

    def request_status(self, **values: Any) -> dict[str, Any]:
        assert values == {
            "request_id": self.request_id,
            "transaction_digest": self.transaction_digest,
        }
        return {
            "request_id": self.request_id,
            "transaction_digest": self.transaction_digest,
            "expires_at": self.request_expires_at,
            "state": "issued",
        }

    def retrieve_receipt(self, **values: Any) -> dict[str, Any]:
        assert self.canonical_transaction is not None
        assert self.transaction_digest is not None
        assert self.possession_hash is not None
        assert self.request_expires_at is not None
        assert values["request_id"] == self.request_id
        assert values["domain_id"] == self.approver.domain_id
        assert values["approval_purpose"] == COMMUNICATION_SCOPE_APPROVAL_PURPOSE
        assert values["transaction_digest"] == self.transaction_digest
        assert (
            hashlib.sha256(values["possession_secret"].encode("ascii")).hexdigest()
            == self.possession_hash
        )
        return create_independent_approval_receipt(
            self.key,
            approver=self.approver,
            verifier_id=self.verifier.verifier_id,
            approval_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            canonical_transaction=self.canonical_transaction,
            issued_at=self.now,
            authenticated_at=self.now,
            expires_at=min(self.request_expires_at, self.now + 300),
        )


def _scope_resolver(owner: VerifiedActor, server: VerifiedActor):
    def resolve(connection: Any, actor: VerifiedActor, now: int) -> dict[str, Any]:
        if actor != server:
            raise AuthorizationError("communication scope fixture requires the enrolled server actor")
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        principal = connection.execute(
            "SELECT status FROM principals WHERE domain_id=? AND principal_id=?",
            (actor.domain_id, actor.principal_id),
        ).fetchone()
        if domain is None or domain["status"] != "active" or principal is None or principal["status"] != "active":
            raise AuthorizationError("communication scope fixture identity is not current")

        def harness(role: str, current: VerifiedActor) -> tuple[dict[str, Any], dict[str, Any]]:
            row = connection.execute(
                """SELECT h.kind,h.display_name,h.status,h.binding_assurance,h.credential_epoch,
                          c.credential_id,c.status AS credential_status,c.epoch,c.not_before,c.expires_at
                     FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                    WHERE h.domain_id=? AND h.principal_id=? AND h.harness_id=?
                      AND c.credential_id=?""",
                (
                    current.domain_id,
                    current.principal_id,
                    current.harness_id,
                    current.credential_id,
                ),
            ).fetchone()
            if (
                row is None
                or row["status"] != "active"
                or row["credential_status"] != "active"
                or int(row["credential_epoch"]) != current.credential_epoch
                or int(row["epoch"]) != current.credential_epoch
                or int(row["not_before"]) > now
                or now >= int(row["expires_at"])
            ):
                raise AuthorizationError("communication scope fixture credential is not current")
            return (
                {
                    "harness_id": current.harness_id,
                    "credential_id": current.credential_id,
                    "credential_epoch": current.credential_epoch,
                    "binding_assurance": row["binding_assurance"],
                    "display_name": row["display_name"],
                    "kind": row["kind"],
                },
                {
                    "schema": "agentnet.integration.enrollment-evidence.v1",
                    "role": role,
                    "harness_id": current.harness_id,
                    "credential_id": current.credential_id,
                },
            )

        owner_binding, owner_evidence = harness("owner", owner)
        server_binding, server_evidence = harness("fresh", server)
        return {
            "domain": {
                "domain_id": actor.domain_id,
                "policy_revision": int(domain["policy_revision"]),
                "revocation_epoch": int(domain["revocation_epoch"]),
            },
            "principal": {"principal_id": actor.principal_id},
            "harnesses": {"owner": owner_binding, "fresh": server_binding},
            "enrollment_evidence": {
                "owner": owner_evidence,
                "fresh": server_evidence,
            },
        }

    return resolve


def _mailbox_item(
    core: CommunicationCore,
    actor: VerifiedActor,
    collaboration_scope_id: str,
    event_id: str,
) -> dict[str, Any]:
    return next(
        item
        for item in core.mailbox(
            actor=actor,
            collaboration_scope_id=collaboration_scope_id,
        )
        if item["event"]["event_id"] == event_id
    )


def test_persistent_same_principal_communication_is_exact_harness_scoped(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Prove local transactional custody and recovery, never production durability."""

    domain_id = "persistent-communication.example"
    principal_id = "principal-persistent-communication"
    owner, _owner_key = identity_factory(
        domain=domain_id,
        principal_id=principal_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    server, _server_key = identity_factory(
        domain=domain_id,
        principal_id=principal_id,
        kind="codex",
        binding_assurance="os_bound",
    )
    sibling, _sibling_key = identity_factory(
        domain=domain_id,
        principal_id=principal_id,
        kind="claude",
        binding_assurance="os_bound",
    )
    outsider, _outsider_key = identity_factory(
        domain=domain_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    assert owner.principal_id == server.principal_id == sibling.principal_id
    assert outsider.principal_id != owner.principal_id

    now = int(time.time())
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=principal_id,
        domain_id=domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({COMMUNICATION_SCOPE_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="persistent-communication-approval",
    )
    approval = _IssuedApprovalBoundary(
        key=approval_key,
        approver=approver,
        verifier=verifier,
        now=now,
    )
    endpoint_lifecycle = EndpointLifecycleService(store, clock=lambda: now)
    endpoint_lifecycle.register_existing(
        actor=owner,
        harness_kind="pi",
        profile_key="owner-profile",
    )
    endpoint_lifecycle.register_existing(
        actor=server,
        harness_kind="codex",
        profile_key="server-profile",
    )
    scope_service = CommunicationScopeService(
        store,
        approval,
        verifier,
        resolver=_scope_resolver(owner, server),
        public_approval_url="https://approval.example/approval",
        approver_principal_id=approver.principal_id,
        endpoint_lifecycle=endpoint_lifecycle,
        clock=lambda: now,
    )
    begin_key = "persistent-communication-begin-0001"
    begin = scope_service.begin(
        actor=server,
        request=CommunicationScopeBeginRequest.model_validate(
            {
                "schema": "agentnet.communication-scope.begin.v1",
                "begin_idempotency_key": begin_key,
            }
        ),
    )
    assert begin["status"] == "approval_pending"
    ready = scope_service.status(
        actor=server,
        request=CommunicationScopeStatusRequest.model_validate(
            {
                "schema": "agentnet.communication-scope.status.v1",
                "begin_idempotency_key": begin_key,
            }
        ),
    )
    assert ready["status"] == "approval_ready"
    completed = scope_service.complete(
        actor=server,
        request=CommunicationScopeCompleteRequest.model_validate(
            {
                "schema": "agentnet.communication-scope.complete.v1",
                "begin_idempotency_key": begin_key,
                "completion_idempotency_key": "persistent-communication-complete-0001",
            }
        ),
    )
    assert completed["schema"] == "agentnet.communication-scope.complete-result.v2"
    assert completed["status"] == "communication_active"
    assert completed["authority_granted"] is True
    assert completed["communication_usable"] is True
    assert completed["authority_expires_at"] is None
    assert completed["artifacts_enabled"] is False
    assert completed["business_effects_enabled"] is False
    assert completed["federation_enabled"] is False
    assert completed["public_a2a_enabled"] is False
    collaboration_scope_id = completed["collaboration_scope_id"]

    scope = store.fetch_one(
        "SELECT * FROM communication_scopes WHERE begin_idempotency_key_sha256=?",
        (hashlib.sha256(begin_key.encode("utf-8")).hexdigest(),),
    )
    assert scope is not None
    assert scope["state"] == "committed"
    assert scope["domain_id"] == domain_id
    assert scope["principal_id"] == principal_id
    assert scope["owner_harness_id"] == owner.harness_id
    assert scope["fresh_harness_id"] == server.harness_id
    assert scope["owner_credential_id"] == owner.credential_id
    assert scope["fresh_credential_id"] == server.credential_id
    assert scope["authority_expires_at"] is None

    expected_actions = {
        "message.send",
        "mailbox.read",
        "mailbox.acknowledge",
        "conversation.create",
        "conversation.message.send",
        "conversation.task.request",
        "conversation.task.handoff",
        "conversation.task.cancel_request",
        "conversation.task.complete",
        "conversation.structured_request.send",
        "conversation.response_obligation.respond",
        "conversation.thread",
        "conversation.response_obligation.create",
        "conversation.response_obligation.read",
        "conversation.response_obligation.transition",
        "conversation.response_obligation.cancel",
        "room.create",
        "room.action",
        "room.read",
    }
    assert set(COMMUNICATION_SCOPE_ACTIONS) == expected_actions
    items = store.fetch_all(
        """SELECT i.harness_id,i.action,i.resource_pattern,i.item_json,
                  e.entitlement_id,e.domain_id,e.principal_id,e.expires_at,e.revoked_at,e.revision
             FROM communication_scope_items i
             JOIN entitlements e ON e.entitlement_id=i.entitlement_id
            WHERE i.scope_id=? ORDER BY i.item_ordinal""",
        (scope["scope_id"],),
    )
    expected_pairs = {
        (harness_id, action)
        for harness_id in (owner.harness_id, server.harness_id)
        for action in expected_actions
    }
    assert {(row["harness_id"], row["action"]) for row in items} == expected_pairs
    assert len(items) == len(expected_pairs)
    assert all(row["resource_pattern"] == "*" for row in items)
    assert all(row["domain_id"] == domain_id for row in items)
    assert all(row["principal_id"] == principal_id for row in items)
    assert all(row["expires_at"] is None and row["revoked_at"] is None for row in items)
    assert all(int(row["revision"]) == int(scope["policy_revision"]) for row in items)
    assert all(json.loads(row["item_json"])["harness_id"] == row["harness_id"] for row in items)
    assert not any(
        row["action"].startswith(("artifact.", "effect.", "tool.", "federation.", "a2a."))
        for row in items
    )

    for actor in (owner, server):
        current = store.fetch_one(
            """SELECT d.status AS domain_status,d.policy_revision,
                      p.status AS principal_status,h.status AS harness_status,h.credential_epoch,
                      c.status AS credential_status,c.epoch,c.not_before,c.expires_at
                 FROM domains d JOIN principals p ON p.domain_id=d.domain_id
                 JOIN harnesses h ON h.principal_id=p.principal_id
                 JOIN credentials c ON c.harness_id=h.harness_id
                WHERE d.domain_id=? AND p.principal_id=? AND h.harness_id=?
                  AND c.credential_id=?""",
            (domain_id, principal_id, actor.harness_id, actor.credential_id),
        )
        assert current is not None
        assert current["domain_status"] == "active"
        assert current["principal_status"] == "active"
        assert current["harness_status"] == "active"
        assert current["credential_status"] == "active"
        assert int(current["policy_revision"]) == int(scope["policy_revision"])
        assert int(current["credential_epoch"]) == actor.credential_epoch
        assert int(current["epoch"]) == actor.credential_epoch
        assert int(current["not_before"]) <= now < int(current["expires_at"])

    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    artifact_dir = tmp_path / "artifacts"
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=domain_id,
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
    core = CommunicationCore(config, store)
    collaboration_scope = core.collaboration_scopes.require(
        actor=server,
        scope_id=collaboration_scope_id,
        action="message.send",
        resource="conversation:direct",
        target_harness_ids=(owner.harness_id,),
    )
    assert collaboration_scope.scope_id == collaboration_scope_id
    assert collaboration_scope.state == "active"
    assert collaboration_scope.revision == 1
    assert collaboration_scope.member_harness_ids == tuple(
        sorted((owner.harness_id, server.harness_id))
    )

    disabled_artifact = ReleasedArtifactBinding(
        artifact_id="00000000-0000-4000-8000-000000000001",
        domain_id=domain_id,
        object_version="a" * 64,
        size=1,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        release_intent_id="00000000-0000-4000-8000-000000000002",
        released_at=datetime(2026, 1, 1, tzinfo=UTC),
    )
    with pytest.raises(AuthorizationError, match="communication_scope_request_mismatch"):
        core.send_message(
            actor=owner,
            collaboration_scope_id=collaboration_scope.scope_id,
            recipients=(server.harness_id,),
            payload={"text": "artifact authority must stay disabled"},
            idempotency_key="persistent-artifact-denied-0001",
            released_artifacts=(disabled_artifact,),
        )
    assert config.artifact_mode == "disabled"

    for actor, key in (
        (sibling, "persistent-sibling-denied-0001"),
        (outsider, "persistent-outsider-denied-0001"),
    ):
        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=actor,
                collaboration_scope_id=collaboration_scope.scope_id,
                recipients=(server.harness_id,),
                payload={"text": "must not inherit communication authority"},
                idempotency_key=key,
            )

    for recipient, label in ((sibling, "sibling"), (outsider, "outsider")):
        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=owner,
                collaboration_scope_id=collaboration_scope.scope_id,
                recipients=(recipient.harness_id,),
                payload={"text": "approved scope must not reach an unbound same-domain harness"},
                idempotency_key=f"persistent-{label}-recipient-denied-0001",
            )
        with pytest.raises(AuthorizationError):
            core.create_conversation(
                actor=owner,
                collaboration_scope_id=collaboration_scope.scope_id,
                conversation_id=f"conversation:persistent-{label}-denied",
                member_harness_ids=(recipient.harness_id,),
                classification=Classification.C0_PUBLIC,
            )

    owner_to_server = core.send_message(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(server.harness_id,),
        payload={"text": "recover this after the server was offline"},
        idempotency_key="persistent-owner-server-0001",
    )
    assert owner_to_server["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    assert owner_to_server["fact"] != DeliveryFact.ACCEPTED_DURABLE.value
    assert set(owner_to_server).isdisjoint({"processing", "effect", "completed"})
    recovered = _mailbox_item(
        core,
        server,
        collaboration_scope.scope_id,
        owner_to_server["event_id"],
    )
    assert recovered["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    assert recovered["payload_available"] is True
    assert recovered["payload"]["text"] == "recover this after the server was offline"
    assert (
        recovered["payload"]["authorization_context"]["collaboration_scope_id"]
        == collaboration_scope.scope_id
    )
    assert recovered["event"]["actor"]["harness_id"] == owner.harness_id
    assert recovered["event"]["actor"]["principal_id"] == principal_id
    server_ack = core.acknowledge_mailbox(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        event_id=owner_to_server["event_id"],
        envelope_digest=owner_to_server["envelope_digest"],
    )
    assert server_ack["fact"] == DeliveryFact.RECIPIENT_COMMITTED.value
    assert set(server_ack).isdisjoint({"processing", "effect", "completed"})

    server_to_owner = core.send_message(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(owner.harness_id,),
        payload={"text": "server reply recovered by owner"},
        idempotency_key="persistent-server-owner-0001",
    )
    owner_recovered = _mailbox_item(
        core,
        owner,
        collaboration_scope.scope_id,
        server_to_owner["event_id"],
    )
    assert owner_recovered["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    assert owner_recovered["event"]["actor"]["harness_id"] == server.harness_id
    owner_ack = core.acknowledge_mailbox(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        event_id=server_to_owner["event_id"],
        envelope_digest=server_to_owner["envelope_digest"],
    )
    assert owner_ack["fact"] == DeliveryFact.RECIPIENT_COMMITTED.value
    assert set(owner_ack).isdisjoint({"processing", "effect", "completed"})
    owner_tools = CanonicalToolDispatcher(core, lambda: owner)
    server_tools = CanonicalToolDispatcher(core, lambda: server)
    room = owner_tools.call(
        "agentnet.room.create",
        {"collaboration_scope_id": collaboration_scope.scope_id},
    )
    member = owner_tools.call(
        "agentnet.room.member.add",
        {
            "collaboration_scope_id": collaboration_scope.scope_id,
            "room_id": room["room_id"],
            "harness_id": server.harness_id,
            "role": "member",
        },
    )
    assert member["control_sequence"] == 2
    server_room = server_tools.call(
        "agentnet.room.get",
        {
            "collaboration_scope_id": collaboration_scope.scope_id,
            "room_id": room["room_id"],
        },
    )
    for scope_bound_result in (room, member, server_room):
        assert (
            scope_bound_result["authorization_context"]["collaboration_scope_id"]
            == collaboration_scope.scope_id
        )
        assert (
            scope_bound_result["authorization_context"]["collaboration_scope_revision"]
            == collaboration_scope.revision
        )
    assert server_room["member_count"] == 2
    assert server_room["self_membership"]["role"] == "member"
    room_message = owner_tools.call(
        "agentnet.room.send",
        {
            "collaboration_scope_id": collaboration_scope.scope_id,
            "room_id": room["room_id"],
            "recipients": [owner.harness_id, server.harness_id],
            "payload": {"text": "persistent room message"},
            "idempotency_key": "persistent-room-message-0001",
            "expected_control_sequence": member["control_sequence"],
        },
    )
    assert room_message["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    room_delivery = _mailbox_item(
        core,
        server,
        collaboration_scope.scope_id,
        room_message["event_id"],
    )
    assert room_delivery["event"]["room_id"] == room["room_id"]
    assert room_delivery["payload"]["text"] == "persistent room message"
    assert (
        room_delivery["payload"]["authorization_context"]["collaboration_scope_id"]
        == collaboration_scope.scope_id
    )

    conversation_id = "conversation:persistent-communication"
    thread_id = "thread:persistent-communication"
    created = core.create_conversation(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        conversation_id=conversation_id,
        member_harness_ids=(server.harness_id,),
    )
    assert created["duplicate"] is False
    task_request = core.post_conversation_action(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(server.harness_id,),
        conversation_id=conversation_id,
        thread_id=thread_id,
        action={
            "kind": "task",
            "task_id": "task:persistent-communication",
            "summary": "prove exact task-request authority",
        },
        idempotency_key="persistent-conversation-task-request-0001",
    )
    assert task_request["fact"] == "pending_human"
    requested = core.post_conversation_action(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(server.harness_id,),
        conversation_id=conversation_id,
        thread_id=thread_id,
        action={
            "kind": "post",
            "body": "return an exact terminal response",
            "response_obligation": {"responsible_harness_id": server.harness_id},
        },
        idempotency_key="persistent-obligation-request-0001",
    )
    assert requested["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    obligation_id = requested["response_obligation"]["obligation_id"]
    assert requested["response_obligation"]["state"] == "created"

    owner_view = core.response_obligation(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        obligation_id=obligation_id,
    )
    server_view = core.response_obligation(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        obligation_id=obligation_id,
    )
    assert owner_view["state"] == server_view["state"] == "created"
    assert owner_view["viewer_role"] == "requester"
    assert server_view["viewer_role"] == "responsible"
    request_digest = owner_view["request_payload_digest"]

    server_thread = core.conversation_thread(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        conversation_id=conversation_id,
        thread_id=thread_id,
    )
    request_entry = next(
        entry for entry in server_thread if entry["event"]["event_id"] == requested["event_id"]
    )
    assert request_entry["payload"]["kind"] == "post"
    obligation_delivery = _mailbox_item(
        core,
        server,
        collaboration_scope.scope_id,
        requested["event_id"],
    )
    assert obligation_delivery["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    obligation_ack = core.acknowledge_mailbox(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        event_id=requested["event_id"],
        envelope_digest=requested["envelope_digest"],
    )
    assert obligation_ack["fact"] == DeliveryFact.RECIPIENT_COMMITTED.value

    reconciled = core.response_obligation_reconcile(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
    )
    assert reconciled["recipient_committed"] == [obligation_id]
    reconciled_view = core.response_obligation(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        obligation_id=obligation_id,
    )
    assert reconciled_view["state"] == "recipient_committed"
    progressed = core.response_obligation_transition(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        obligation_id=obligation_id,
        to_state="acknowledged",
        expected_revision=reconciled_view["revision"],
    )
    assert progressed["state"] == "acknowledged"

    replied = core.post_conversation_action(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(owner.harness_id,),
        conversation_id=conversation_id,
        thread_id=thread_id,
        action={
            "kind": "reply",
            "reply_to_event_id": requested["event_id"],
            "body": "thread reply before terminal response",
        },
        idempotency_key="persistent-thread-reply-0001",
    )
    assert replied["action_kind"] == "reply"
    owner_thread = core.conversation_thread(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        conversation_id=conversation_id,
        thread_id=thread_id,
    )
    reply_entry = next(
        entry for entry in owner_thread if entry["event"]["event_id"] == replied["event_id"]
    )
    assert reply_entry["payload"]["kind"] == "reply"
    assert reply_entry["event"]["causal_parent_ids"] == [requested["event_id"]]

    terminal = core.post_conversation_action(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(owner.harness_id,),
        conversation_id=conversation_id,
        thread_id=thread_id,
        action={
            "kind": "obligation_response",
            "obligation_id": obligation_id,
            "request_event_id": requested["event_id"],
            "request_digest": request_digest,
            "outcome": "completed",
            "body": "exact terminal response",
        },
        idempotency_key="persistent-obligation-response-0001",
    )
    assert terminal["response_obligation"]["state"] == "completed"
    final_view = core.response_obligation(
        actor=owner,
        collaboration_scope_id=collaboration_scope.scope_id,
        obligation_id=obligation_id,
    )
    assert final_view["state"] == "completed"
    assert final_view["response_event_id"] == terminal["event_id"]

    owner_message_entitlement = next(
        row["entitlement_id"]
        for row in items
        if row["harness_id"] == owner.harness_id and row["action"] == "message.send"
    )
    with store.transaction() as connection:
        changed = connection.execute(
            "UPDATE entitlements SET revoked_at=? WHERE entitlement_id=? AND revoked_at IS NULL",
            (now, owner_message_entitlement),
        )
        assert changed.rowcount == 1
    with pytest.raises(AuthorizationError):
        core.send_message(
            actor=owner,
            collaboration_scope_id=collaboration_scope.scope_id,
            recipients=(server.harness_id,),
            payload={"text": "revoked owner send must fail immediately"},
            idempotency_key="persistent-owner-revoked-0001",
        )
    assert isinstance(
        core.mailbox(
            actor=owner,
            collaboration_scope_id=collaboration_scope.scope_id,
        ),
        list,
    )
    unaffected_server_send = core.send_message(
        actor=server,
        collaboration_scope_id=collaboration_scope.scope_id,
        recipients=(owner.harness_id,),
        payload={"text": "server send remains independently authorized"},
        idempotency_key="persistent-server-unaffected-0001",
    )
    assert unaffected_server_send["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
            (domain_id,),
        )
    for actor, recipient, key in (
        (owner, server, "persistent-owner-domain-epoch-denied-0001"),
        (server, owner, "persistent-server-domain-epoch-denied-0001"),
    ):
        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=actor,
                collaboration_scope_id=collaboration_scope.scope_id,
                recipients=(recipient.harness_id,),
                payload={"text": "stale communication scope must not survive a domain epoch bump"},
                idempotency_key=key,
            )

    assert not artifact_dir.exists()
    assert not (config.data_dir / "secrets" / "artifact.key").exists()
