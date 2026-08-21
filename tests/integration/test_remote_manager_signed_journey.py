from __future__ import annotations

import hashlib
import json
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit
from uuid import uuid4

import pytest
from starlette.testclient import TestClient

from agentnet.approval import IndependentApprovalVerifier, TrustedApprover, create_independent_approval_receipt
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    CommunicationScopeBeginRequest,
    CommunicationScopeCompleteRequest,
    CommunicationScopeStatusRequest,
)
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
    CommunicationScopeService,
)
from agentnet.authorization.evidence import AUTHORITY_COMMAND_PURPOSE, IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.policy import AuthorizationRequest, HumanEntitlement, LocalConformancePolicyEngine
from agentnet.bindings.remote_manager import RemoteManagerDispatcher, RemoteManagerRequestError
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.http_api import create_app
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.endpoint_lifecycle import EndpointLifecycleService
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.protocol.models import Classification, DeliveryFact
from agentnet.security.dpop import canonical_request_target, create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json


class _IssuedApproval:
    def __init__(self, *, key: P256KeyPair, approver: TrustedApprover, verifier: IndependentApprovalVerifier, now: int, suffix: str) -> None:
        self.key = key
        self.approver = approver
        self.verifier = verifier
        self.now = now
        self.request_id = f"approval-request-{suffix}"
        self.canonical_transaction: bytes | None = None
        self.transaction_digest: str | None = None
        self.possession_hash: str | None = None
        self.expires_at: int | None = None

    def create_request(self, **values: Any) -> dict[str, Any]:
        self.canonical_transaction = values["canonical_transaction"]
        self.transaction_digest = values["transaction_digest"]
        self.possession_hash = values["possession_hash"]
        self.expires_at = values["request_expires_at"]
        return {"request_id": self.request_id, "transaction_digest": self.transaction_digest, "expires_at": self.expires_at, "state": "pending"}

    def request_status(self, **values: Any) -> dict[str, Any]:
        assert values == {"request_id": self.request_id, "transaction_digest": self.transaction_digest}
        return {"request_id": self.request_id, "transaction_digest": self.transaction_digest, "expires_at": self.expires_at, "state": "issued"}

    def retrieve_receipt(self, **values: Any) -> dict[str, Any]:
        assert self.canonical_transaction is not None
        assert self.transaction_digest is not None
        assert self.possession_hash is not None
        assert self.expires_at is not None
        assert values["request_id"] == self.request_id
        assert values["domain_id"] == self.approver.domain_id
        assert values["approval_purpose"] == COMMUNICATION_SCOPE_APPROVAL_PURPOSE
        assert values["transaction_digest"] == self.transaction_digest
        assert hashlib.sha256(values["possession_secret"].encode("ascii")).hexdigest() == self.possession_hash
        return create_independent_approval_receipt(
            self.key,
            approver=self.approver,
            verifier_id=self.verifier.verifier_id,
            approval_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            canonical_transaction=self.canonical_transaction,
            issued_at=self.now,
            authenticated_at=self.now,
            expires_at=min(self.expires_at, self.now + 300),
        )


def _scope_resolver(owner: VerifiedActor, server: VerifiedActor):
    def resolve(connection: Any, actor: VerifiedActor, now: int) -> dict[str, Any]:
        assert actor == server
        domain = connection.execute("SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?", (actor.domain_id,)).fetchone()
        principal = connection.execute("SELECT status FROM principals WHERE domain_id=? AND principal_id=?", (actor.domain_id, actor.principal_id)).fetchone()
        assert domain is not None and domain["status"] == "active"
        assert principal is not None and principal["status"] == "active"

        def current(role: str, binding: VerifiedActor) -> tuple[dict[str, Any], dict[str, Any]]:
            row = connection.execute(
                """SELECT h.kind,h.display_name,h.status,h.binding_assurance,h.credential_epoch,
                          c.status AS credential_status,c.epoch,c.not_before,c.expires_at
                     FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                    WHERE h.domain_id=? AND h.principal_id=? AND h.harness_id=? AND c.credential_id=?""",
                (binding.domain_id, binding.principal_id, binding.harness_id, binding.credential_id),
            ).fetchone()
            assert row is not None
            assert row["status"] == row["credential_status"] == "active"
            assert int(row["credential_epoch"]) == int(row["epoch"]) == binding.credential_epoch
            assert int(row["not_before"]) <= now < int(row["expires_at"])
            return (
                {"harness_id": binding.harness_id, "credential_id": binding.credential_id, "credential_epoch": binding.credential_epoch, "binding_assurance": row["binding_assurance"], "display_name": row["display_name"], "kind": row["kind"]},
                {"schema": "agentnet.integration.enrollment-evidence.v1", "role": role, "harness_id": binding.harness_id, "credential_id": binding.credential_id},
            )

        owner_binding, owner_evidence = current("owner", owner)
        server_binding, server_evidence = current("fresh", server)
        return {
            "domain": {"domain_id": actor.domain_id, "policy_revision": int(domain["policy_revision"]), "revocation_epoch": int(domain["revocation_epoch"])},
            "principal": {"principal_id": actor.principal_id},
            "harnesses": {"owner": owner_binding, "fresh": server_binding},
            "enrollment_evidence": {"owner": owner_evidence, "fresh": server_evidence},
        }

    return resolve


def _activate_scope(store: Any, *, owner: VerifiedActor, server: VerifiedActor, now: int) -> dict[str, Any]:
    key = P256KeyPair.generate()
    approver = TrustedApprover(principal_id=server.principal_id, domain_id=server.domain_id, signer_key_id=key.thumbprint, public_key_pem=key.public_pem, allowed_purposes=frozenset({COMMUNICATION_SCOPE_APPROVAL_PURPOSE}))
    verifier = IndependentApprovalVerifier({key.thumbprint: approver}, verifier_id=f"manager-journey-approval-{server.harness_id}")
    approval = _IssuedApproval(key=key, approver=approver, verifier=verifier, now=now, suffix=server.harness_id)
    endpoint_lifecycle = EndpointLifecycleService(store, clock=lambda: now)
    for actor, profile_key in ((owner, "owner-profile"), (server, "server-profile")):
        harness_kind = str(
            store.fetch_one(
                "SELECT kind FROM harnesses WHERE harness_id=?",
                (actor.harness_id,),
            )["kind"]
        )
        endpoint_lifecycle.register_existing(
            actor=actor,
            harness_kind=harness_kind,
            profile_key=profile_key,
        )
    service = CommunicationScopeService(store, approval, verifier, resolver=_scope_resolver(owner, server), public_approval_url="https://approval.example/approval", approver_principal_id=approver.principal_id, endpoint_lifecycle=endpoint_lifecycle, clock=lambda: now)
    begin_key = f"manager-journey-begin-{server.harness_id}"
    begin = service.begin(actor=server, request=CommunicationScopeBeginRequest(schema="agentnet.communication-scope.begin.v1", begin_idempotency_key=begin_key))
    assert begin["status"] == "approval_pending"
    ready = service.status(actor=server, request=CommunicationScopeStatusRequest(schema="agentnet.communication-scope.status.v1", begin_idempotency_key=begin_key))
    assert ready["status"] == "approval_ready"
    completed = service.complete(actor=server, request=CommunicationScopeCompleteRequest(schema="agentnet.communication-scope.complete.v1", begin_idempotency_key=begin_key, completion_idempotency_key=f"manager-journey-complete-{server.harness_id}"))
    assert completed["status"] == "communication_active"
    scope = store.fetch_one("SELECT * FROM communication_scopes WHERE begin_idempotency_key_sha256=?", (hashlib.sha256(begin_key.encode()).hexdigest(),))
    assert scope is not None
    return scope


def _authority_command(key: P256KeyPair, actor: VerifiedActor, *, resource: str, mutation: dict[str, Any], reason: str, revision: int) -> SignedAuthorityCommand:
    now = datetime.now(UTC)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()), actor=actor, action="authorization.entitlement.issue", resource=resource,
        request_digest=canonical_digest(mutation), expected_policy_revision=revision, expected_entity_revision=0,
        reason=reason, issued_at=now, expires_at=now + timedelta(minutes=2),
    )
    return SignedAuthorityCommand(**fields, signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields))


def _issue_manager_authority(core: CommunicationCore, *, administrator: VerifiedActor, administrator_key: P256KeyPair, manager: VerifiedActor) -> None:
    revision = core.policy.current_policy_revision(administrator)
    fixture_policy = LocalConformancePolicyEngine(core.store)
    fixture_policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(entitlement_id="manager-journey-root-entitlement-issuer", domain_id=administrator.domain_id, principal_id=administrator.principal_id, action="authorization.entitlement.issue", resource_pattern="*", revision=revision, expires_at=datetime.now(UTC) + timedelta(minutes=15))
    )
    for action in sorted(COMMUNICATION_SCOPE_ACTIONS):
        entitlement = HumanEntitlement(entitlement_id=f"manager-b-{action.replace('.', '-')}", domain_id=manager.domain_id, principal_id=manager.principal_id, action=action, resource_pattern="*", revision=revision, expires_at=None)
        reason = f"grant enrolled Manager B canonical {action} authority"
        resource, mutation = core.policy.entitlement_issuance_binding(entitlement, reason=reason)
        decision = core.policy.require(AuthorizationRequest(actor=administrator, action="authorization.entitlement.issue", resource=resource, policy_revision=revision, context={"request_digest": canonical_digest(mutation)}))
        core.policy.add_entitlement(
            entitlement,
            command=_authority_command(administrator_key, administrator, resource=resource, mutation=mutation, reason=reason, revision=revision),
            authority=IssuanceAuthority(actor=administrator, policy_decision_id=decision.decision_id),
        )
    outbound = HumanEntitlement(
        entitlement_id="manager-a-cross-principal-direct-send",
        domain_id=administrator.domain_id,
        principal_id=administrator.principal_id,
        action="message.send",
        resource_pattern="direct",
        revision=revision,
        expires_at=None,
    )
    reason = "grant Manager A explicit cross-principal direct-send authority"
    resource, mutation = core.policy.entitlement_issuance_binding(outbound, reason=reason)
    decision = core.policy.require(
        AuthorizationRequest(
            actor=administrator,
            action="authorization.entitlement.issue",
            resource=resource,
            policy_revision=revision,
            context={"request_digest": canonical_digest(mutation)},
        )
    )
    core.policy.add_entitlement(
        outbound,
        command=_authority_command(
            administrator_key,
            administrator,
            resource=resource,
            mutation=mutation,
            reason=reason,
            revision=revision,
        ),
        authority=IssuanceAuthority(
            actor=administrator,
            policy_decision_id=decision.decision_id,
        ),
    )


def _issue_manager_collaboration_scope(
    core: CommunicationCore,
    *,
    administrator: VerifiedActor,
    administrator_key: P256KeyPair,
    manager: VerifiedActor,
) -> str:
    revision = core.policy.current_policy_revision(administrator)
    scope_id = f"scope:remote-manager:{uuid4()}"
    entitlement = HumanEntitlement(
        entitlement_id="manager-a-collaboration-scope-issuer",
        domain_id=administrator.domain_id,
        principal_id=administrator.principal_id,
        action=COLLABORATION_SCOPE_ISSUE_ACTION,
        resource_pattern=f"scope:{scope_id}",
        revision=revision,
        expires_at=None,
    )
    reason = "grant Manager A authority to issue the exact Manager collaboration scope"
    resource, mutation = core.policy.entitlement_issuance_binding(
        entitlement,
        reason=reason,
    )
    decision = core.policy.require(
        AuthorizationRequest(
            actor=administrator,
            action="authorization.entitlement.issue",
            resource=resource,
            policy_revision=revision,
            context={"request_digest": canonical_digest(mutation)},
        )
    )
    core.policy.add_entitlement(
        entitlement,
        command=_authority_command(
            administrator_key,
            administrator,
            resource=resource,
            mutation=mutation,
            reason=reason,
            revision=revision,
        ),
        authority=IssuanceAuthority(
            actor=administrator,
            policy_decision_id=decision.decision_id,
        ),
    )
    domain = core.store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (administrator.domain_id,),
    )
    assert domain is not None
    proposal = CollaborationScopeProposal(
        scope_id=scope_id,
        scope_kind="direct",
        member_harness_ids=tuple(
            sorted((administrator.harness_id, manager.harness_id))
        ),
        allowed_actions=(
            "message.acknowledge",
            "message.read",
            "message.send",
        ),
        allowed_resource_prefixes=("conversation:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=int(domain["policy_revision"]),
        domain_revocation_epoch=int(domain["revocation_epoch"]),
    )
    scope_request = core.collaboration_scopes.issuance_request(
        actor=administrator,
        proposal=proposal,
    )
    scope_decision = core.policy.require(
        AuthorizationRequest(
            actor=administrator,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=f"scope:{scope_id}",
            policy_revision=revision,
            context=scope_request,
        )
    )
    return core.collaboration_scopes.issue(
        actor=administrator,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=administrator,
            policy_decision_id=scope_decision.decision_id,
        ),
    ).scope_id


class _SignedInProcessClient:
    """Test-local transport with the same purpose-bound proof as AgentNetClient."""

    def __init__(self, transport: TestClient, *, actor: VerifiedActor, key: P256KeyPair) -> None:
        self.transport = transport
        self.key = key
        self.domain_id = actor.domain_id
        self.harness_id = actor.harness_id
        self.credential_id = actor.credential_id
        self.audience = f"urn:agentnet:{actor.domain_id}:corporate-api"
        self.requests: list[dict[str, Any]] = []

    def request(self, method: str, path: str, *, json_body: dict[str, Any] | None = None, timeout_seconds: float | None = None) -> Any:
        del timeout_seconds
        relative = urlsplit(path)
        target = canonical_request_target(scheme="http", authority="127.0.0.1", path=relative.path, query=relative.query)
        body = canonical_json(json_body) if json_body is not None else b""
        headers = {"Content-Type": "application/json", **proof_headers(create_request_proof(self.key, harness_id=self.harness_id, credential_id=self.credential_id, domain_id=self.domain_id, audience=self.audience, method=method, scheme=target.scheme, authority=target.authority, path=target.path, query=target.query, body=body))}
        self.requests.append({"method": method, "path": path, "json_body": json_body, "headers": headers})
        return self.transport.request(method, path, content=body, headers=headers)


def _all_keys(value: Any) -> set[str]:
    if isinstance(value, dict):
        return set(value) | {key for nested in value.values() for key in _all_keys(nested)}
    if isinstance(value, (list, tuple)):
        return {key for nested in value for key in _all_keys(nested)}
    return set()


def test_remote_managers_cross_principals_over_signed_http_with_exact_scope_and_attribution(store: Any, identity_factory: Any, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    domain_id = "remote-manager-journey.example"
    person_a_manager, person_a_key = identity_factory(domain=domain_id, kind="pi", binding_assurance="os_bound")
    person_a_server, _server_key = identity_factory(domain=domain_id, principal_id=person_a_manager.principal_id, kind="server", binding_assurance="os_bound")
    unscoped_manager, unscoped_manager_key = identity_factory(domain=domain_id, kind="codex", binding_assurance="os_bound")
    person_b_manager, person_b_key = identity_factory(domain=domain_id, kind="claude", binding_assurance="os_bound")
    assert person_a_manager.principal_id != person_b_manager.principal_id

    scope = _activate_scope(store, owner=person_a_manager, server=person_a_server, now=int(time.time()))
    assert scope["state"] == "committed"
    assert scope["owner_harness_id"] == person_a_manager.harness_id
    assert scope["fresh_harness_id"] == person_a_server.harness_id

    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT, domain_id=domain_id, data_dir=tmp_path / "data",
        database_url="postgresql://agentnet@postgres/agentnet", artifact_mode="disabled",
        artifact_backend="postgres-manifest", artifact_dir=tmp_path / "artifacts", public_base_url="http://127.0.0.1",
        enrolled_harness_id=person_a_server.harness_id, enrolled_credential_id=person_a_server.credential_id,
        server_agent_capabilities={ServerAgentCapability.OFFLINE_CUSTODY},
    )
    core = CommunicationCore(config, store)
    _issue_manager_authority(core, administrator=person_a_manager, administrator_key=person_a_key, manager=person_b_manager)
    collaboration_scope_id = _issue_manager_collaboration_scope(
        core,
        administrator=person_a_manager,
        administrator_key=person_a_key,
        manager=person_b_manager,
    )
    collaboration_scope_context = core.collaboration_scopes.get_for_actor(
        actor=person_a_manager,
        scope_id=collaboration_scope_id,
    ).authorization_context()
    manager_b_actions = store.fetch_all("SELECT action,expires_at FROM entitlements WHERE principal_id=? AND entitlement_id LIKE 'manager-b-%'", (person_b_manager.principal_id,))
    assert {row["action"] for row in manager_b_actions} == set(COMMUNICATION_SCOPE_ACTIONS)
    assert all(row["expires_at"] is None for row in manager_b_actions)
    assert store.fetch_one("""SELECT 1 FROM communication_scope_items i JOIN entitlements e ON e.entitlement_id=i.entitlement_id WHERE e.principal_id=?""", (person_b_manager.principal_id,)) is None

    calls: list[dict[str, Any]] = []

    def invoke(dispatcher: RemoteManagerDispatcher, method: str, arguments: dict[str, Any]) -> Any:
        calls.append({"method": method, "arguments": arguments})
        return dispatcher.dispatch(method, arguments)

    with TestClient(create_app(core), base_url="http://127.0.0.1", raise_server_exceptions=False) as http:
        client_a = _SignedInProcessClient(http, actor=person_a_manager, key=person_a_key)
        client_b = _SignedInProcessClient(http, actor=person_b_manager, key=person_b_key)
        client_unscoped = _SignedInProcessClient(http, actor=unscoped_manager, key=unscoped_manager_key)
        manager_a = RemoteManagerDispatcher(client_a, lambda: person_a_manager)  # type: ignore[arg-type]
        manager_b = RemoteManagerDispatcher(client_b, lambda: person_b_manager)  # type: ignore[arg-type]
        unscoped = RemoteManagerDispatcher(client_unscoped, lambda: unscoped_manager)  # type: ignore[arg-type]

        with pytest.raises(RemoteManagerRequestError) as denied:
            invoke(unscoped, "agentnet.send", {"recipients": [person_b_manager.harness_id], "payload": {"text": "unscoped manager must not inherit Manager A authority"}, "idempotency_key": "unscoped-manager-send-0001", "classification": "C1"})
        assert denied.value.status_code == 404
        assert store.fetch_one("SELECT event_id FROM events WHERE idempotency_key=?", ("unscoped-manager-send-0001",)) is None

        sent = invoke(manager_a, "agentnet.send", {"recipients": [person_b_manager.harness_id], "payload": {"text": "signed Manager A to Manager B"}, "idempotency_key": "manager-a-to-manager-b-0001", "classification": "C1"})
        assert sent["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
        inbox_b = invoke(manager_b, "agentnet.inbox", {"collaboration_scope_id": collaboration_scope_id, "after_cursor": 0, "limit": 25})
        received_b = next(item for item in inbox_b if item["event"]["event_id"] == sent["event_id"])
        assert received_b["payload"] == {
            "authorization_context": collaboration_scope_context,
            "text": "signed Manager A to Manager B",
        }
        assert received_b["event"]["actor"]["principal_id"] == person_a_manager.principal_id
        assert received_b["event"]["actor"]["harness_id"] == person_a_manager.harness_id
        assert received_b["event"]["recipients"] == [person_b_manager.harness_id]
        ack_b = invoke(manager_b, "agentnet.inbox.acknowledge", {"collaboration_scope_id": collaboration_scope_id, "event_id": sent["event_id"], "envelope_digest": sent["envelope_digest"]})
        assert ack_b["fact"] == DeliveryFact.RECIPIENT_COMMITTED.value
        assert ack_b["recipient_id"] == person_b_manager.harness_id

        reply = invoke(manager_b, "agentnet.send", {"recipients": [person_a_manager.harness_id], "payload": {"text": "signed Manager B acknowledgement", "in_reply_to": sent["event_id"]}, "idempotency_key": "manager-b-to-manager-a-reply-0001", "classification": "C1"})
        inbox_a = invoke(manager_a, "agentnet.inbox", {"collaboration_scope_id": collaboration_scope_id, "after_cursor": 0, "limit": 25})
        received_a = next(item for item in inbox_a if item["event"]["event_id"] == reply["event_id"])
        assert received_a["payload"]["in_reply_to"] == sent["event_id"]
        assert received_a["event"]["actor"]["principal_id"] == person_b_manager.principal_id
        assert received_a["event"]["actor"]["harness_id"] == person_b_manager.harness_id
        assert received_a["event"]["recipients"] == [person_a_manager.harness_id]
        ack_a = invoke(manager_a, "agentnet.inbox.acknowledge", {"collaboration_scope_id": collaboration_scope_id, "event_id": reply["event_id"], "envelope_digest": reply["envelope_digest"]})
        assert ack_a["fact"] == DeliveryFact.RECIPIENT_COMMITTED.value
        assert ack_a["recipient_id"] == person_a_manager.harness_id

    forbidden_child_arguments = {"authorization", "bearer", "bearer_token", "private_key", "remote_bearer", "signing_key"}
    assert all(_all_keys(call["arguments"]).isdisjoint(forbidden_child_arguments) for call in calls)
    for client, actor in ((client_a, person_a_manager), (client_b, person_b_manager), (client_unscoped, unscoped_manager)):
        assert client.requests
        assert all("Authorization" not in request["headers"] for request in client.requests)
        assert all(request["headers"]["X-AgentNet-Harness"] == actor.harness_id for request in client.requests)
        assert all(request["headers"]["X-AgentNet-Domain"] == domain_id for request in client.requests)
        assert all(request["headers"]["X-AgentNet-Path"] == urlsplit(request["path"]).path for request in client.requests)

    for event_id, sender, recipient, envelope_digest in (
        (sent["event_id"], person_a_manager, person_b_manager, sent["envelope_digest"]),
        (reply["event_id"], person_b_manager, person_a_manager, reply["envelope_digest"]),
    ):
        event = store.fetch_one("SELECT actor_json,acceptance_fact,envelope_digest FROM events WHERE event_id=?", (event_id,))
        assert event is not None
        durable_actor = json.loads(event["actor_json"])
        assert durable_actor["principal_id"] == sender.principal_id
        assert durable_actor["harness_id"] == sender.harness_id
        assert event["acceptance_fact"] == DeliveryFact.ACCEPTED_LOCAL.value
        assert event["envelope_digest"] == envelope_digest
        recipient_fact = store.fetch_one("SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?", (event_id, recipient.harness_id))
        assert recipient_fact is not None and recipient_fact["current_fact"] == DeliveryFact.RECIPIENT_COMMITTED.value
        receipt = store.fetch_one("""SELECT fact,owner_actor_json,event_digest FROM receipts WHERE event_id=? AND recipient_id=? AND fact=?""", (event_id, recipient.harness_id, DeliveryFact.RECIPIENT_COMMITTED.value))
        assert receipt is not None
        receipt_actor = json.loads(receipt["owner_actor_json"])
        assert receipt_actor["principal_id"] == recipient.principal_id
        assert receipt_actor["harness_id"] == recipient.harness_id
        assert receipt["event_digest"] == envelope_digest

    assert not (config.data_dir / "secrets" / "artifact.key").exists()
