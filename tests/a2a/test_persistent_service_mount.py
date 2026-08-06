from __future__ import annotations

import asyncio
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from a2a.types import Message, Part, Role, SendMessageRequest
from google.protobuf.json_format import MessageToDict
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest, HumanEntitlement
from agentnet.client import proof_headers
from agentnet.cli import _require_safe_serve_binding
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.gateways.a2a import SSRFPolicy, corporate_peer_namespace
from agentnet.gateways.a2a_client import (
    CorporateA2AClientIdentity,
    OutboundA2AJournal,
    create_native_a2a_client,
)
from agentnet.gateways.a2a_runtime import corporate_input_source, corporate_output_sink
from agentnet.gateways.a2a_service import _load_signing_identity
from agentnet.http_api import create_app
from agentnet.operations.config import (
    A2AAgentCardConfig,
    A2AServiceConfig,
    A2ASigningCredentialConfig,
    A2ASigningIdentityConfig,
    A2AStandingGrantConfig,
    ExtensionConfig,
    FeatureFlags,
)
from agentnet.identity.credentials import (
    CREDENTIAL_ROTATION_POP_PURPOSE,
    CredentialRotationRequest,
    CredentialRotationService,
)
from agentnet.protocol.a2a_mapping import A2AMappedKind
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import canonical_json
from agentnet.storage.a2a_schema import A2A_REQUIRED_TABLES, A2A_SCHEMA_VERSION
from agentnet.storage.migrations import (
    CURRENT_SCHEMA_VERSION,
    MIGRATIONS,
    validate_migration_catalog,
)


TENANT = "P" * 43


def _write_private_key(path: Path, key) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_bytes(key.private_pem)
    path.chmod(0o600)


def _service_config(
    tmp_path: Path,
    recipient,
    *,
    key_path: Path,
    peer_namespaces: frozenset[str],
) -> ExtensionConfig:
    return ExtensionConfig(
        domain_id=recipient.domain_id,
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
        features=FeatureFlags(public_a2a=True),
        server_agent_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.A2A_GATEWAY,
            }
        ),
        a2a=A2AServiceConfig(
            route_token=TENANT,
            recipient_harness_id=recipient.harness_id,
            card=A2AAgentCardConfig(
                name="Persistent ordinary agent",
                description="self-hosted native A2A test endpoint",
                version="1",
                streaming=False,
                push_notifications=False,
            ),
            standing_grant=A2AStandingGrantConfig(
                grant_id="persistent-a2a-standing-1",
                allowed_actions=frozenset({"a2a.message.send", "a2a.task.get"}),
                allowed_peer_namespaces=peer_namespaces,
                allowed_output_sinks=frozenset({"public-response"}),
                expires_at=datetime.now(UTC) + timedelta(hours=1),
            ),
            signing_identity=A2ASigningIdentityConfig(
                harness_id=recipient.harness_id,
                credential_id=recipient.credential_id,
                private_key_path=key_path,
            ),
        ),
    )


def _signed_headers(key, actor, *, method: str, path: str, body: bytes) -> dict[str, str]:
    proof = create_request_proof(
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
    return proof_headers(proof)


def _seed_task_authority(core: CommunicationCore, sender, recipient_id: str) -> TaskGrant:
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="a2a.task.submit",
            resource_pattern=recipient_id,
            revision=1,
        )
    )
    grant = TaskGrant(
        domain_id=sender.domain_id,
        principal_id=sender.principal_id,
        harness_id=sender.harness_id,
        actions=frozenset({"a2a.task.submit"}),
        resources=frozenset({recipient_id}),
        input_sources=frozenset({corporate_input_source(sender)}),
        output_sinks=frozenset({corporate_output_sink(recipient_id)}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=3,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    with core.store.transaction() as connection:
        return core.policy.grants._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "persistent_app_focused_test"},
        )


def _issue_persistent_scope(core: CommunicationCore, sender, recipient) -> str:
    revision = core.policy.current_policy_revision(sender)
    domain = core.store.fetch_one(
        "SELECT revocation_epoch FROM domains WHERE domain_id=?",
        (sender.domain_id,),
    )
    assert domain is not None
    proposal = CollaborationScopeProposal(
        scope_id=f"scope:persistent-a2a:{uuid4()}",
        scope_kind="direct",
        member_harness_ids=tuple(sorted((sender.harness_id, recipient.harness_id))),
        allowed_actions=("message.send", "task.accept", "task.propose"),
        allowed_resource_prefixes=("conversation:", "task:"),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=revision,
        domain_revocation_epoch=int(domain["revocation_epoch"]),
    )
    resource = f"scope:{proposal.scope_id}"
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=resource,
            revision=revision,
        )
    )
    decision = core.policy.require(
        AuthorizationRequest(
            actor=sender,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=resource,
            policy_revision=revision,
            context=core.collaboration_scopes.issuance_request(
                actor=sender,
                proposal=proposal,
            ),
        )
    )
    return core.collaboration_scopes.issue(
        actor=sender,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=sender,
            policy_decision_id=decision.decision_id,
        ),
    ).scope_id


@pytest.mark.anyio
async def test_persistent_app_mounts_native_a2a_alongside_core_routes(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    sender, sender_key = identity_factory()
    recipient, recipient_key = identity_factory(kind="pi")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id IN (?,?)",
            (sender.harness_id, recipient.harness_id),
        )
    sender = sender.model_copy(update={"binding_assurance": "os_bound"})
    recipient = recipient.model_copy(update={"binding_assurance": "os_bound"})
    key_path = tmp_path / "data" / "secrets" / "a2a-signing.pem"
    _write_private_key(key_path, recipient_key)
    config = _service_config(
        tmp_path,
        recipient,
        key_path=Path("secrets/a2a-signing.pem"),
        peer_namespaces=frozenset({corporate_peer_namespace(sender)}),
    )
    assert "PRIVATE KEY" not in str(config.redacted_export())
    core = CommunicationCore(config, store)
    collaboration_scope_id = _issue_persistent_scope(core, sender, recipient)
    grant = _seed_task_authority(core, sender, recipient.harness_id)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action="message.send",
            resource_pattern="direct",
            revision=1,
        )
    )
    app = create_app(core)
    assert app.state.a2a_service is not None
    path = f"/a2a/{TENANT}/message:send"
    a2a_request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="persistent-a2a-message-1",
            role=Role.ROLE_USER,
            parts=[Part(text="durable task through persistent app")],
        ),
        metadata={
            "agentnetIntent": "task",
            "agentnetIdempotencyKey": "persistent-a2a-idempotency-0001",
            "agentnetTaskGrantId": grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": collaboration_scope_id,
        },
    )
    a2a_body = canonical_json(MessageToDict(a2a_request))
    transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1") as client:
        health_before = await client.get("/healthz")
        readiness = await client.get("/readyz")
        card_response = await client.get(f"/a2a/{TENANT}/.well-known/agent-card.json")
        rpc_path = f"/a2a/{TENANT}/rpc/"
        missing_task_id = "persistent-jsonrpc-missing-task"
        missing_task_body = canonical_json(
            {
                "jsonrpc": "2.0",
                "id": "persistent-jsonrpc-get-missing-1",
                "method": "GetTask",
                "params": {"id": missing_task_id},
            }
        )
        missing_counts_before = {
            table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
            for table in ("events", "a2a_tasks", "a2a_task_events")
        }
        missing_task = await client.post(
            rpc_path,
            content=missing_task_body,
            headers={
                "A2A-Version": "1.0",
                "Content-Type": "application/json",
                **_signed_headers(
                    sender_key,
                    sender,
                    method="POST",
                    path=rpc_path,
                    body=missing_task_body,
                ),
            },
        )
        missing_counts_after = {
            table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
            for table in ("events", "a2a_tasks", "a2a_task_events")
        }
        rejected_counts_before = {
            table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
            for table in ("events", "a2a_tasks", "a2a_task_events")
        }
        conflicting_task_id = "persistent-jsonrpc-conflicting-tenant-task"
        conflicting_tenant_responses = []
        for conflicting_rpc_path in (f"/a2a/{TENANT}/rpc", rpc_path):
            conflicting_body = canonical_json(
                {
                    "jsonrpc": "2.0",
                    "id": f"conflicting-tenant-{conflicting_rpc_path.endswith('/')}",
                    "method": "GetTask",
                    "params": {"tenant": "T" * 43, "id": conflicting_task_id},
                }
            )
            conflicting_tenant_responses.append(
                await client.post(
                    conflicting_rpc_path,
                    content=conflicting_body,
                    headers={
                        "A2A-Version": "1.0",
                        "Content-Type": "application/json",
                        **_signed_headers(
                            sender_key,
                            sender,
                            method="POST",
                            path=conflicting_rpc_path,
                            body=conflicting_body,
                        ),
                    },
                )
            )
        rejected_counts_after = {
            table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
            for table in ("events", "a2a_tasks", "a2a_task_events")
        }
        idempotent_rpc_request = SendMessageRequest(
            tenant=TENANT,
            message=Message(
                message_id="persistent-jsonrpc-idempotent-message",
                role=Role.ROLE_USER,
                parts=[Part(text="same signed request through both JSON-RPC spellings")],
            ),
            metadata={
                "agentnetIntent": "task",
                "agentnetIdempotencyKey": "persistent-jsonrpc-idempotency-0003",
                "agentnetTaskGrantId": grant.grant_id,
                "agentnetDataClass": "C1",
                "agentnetCollaborationScopeId": collaboration_scope_id,
            },
        )
        idempotent_rpc_params = MessageToDict(idempotent_rpc_request)
        idempotent_rpc_responses = []
        idempotent_counts = []
        for request_id, idempotent_rpc_path in (
            ("persistent-rpc-idempotent-1", f"/a2a/{TENANT}/rpc"),
            ("persistent-rpc-idempotent-2", rpc_path),
        ):
            idempotent_body = canonical_json(
                {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": "SendMessage",
                    "params": idempotent_rpc_params,
                }
            )
            idempotent_rpc_responses.append(
                await client.post(
                    idempotent_rpc_path,
                    content=idempotent_body,
                    headers={
                        "A2A-Version": "1.0",
                        "Content-Type": "application/json",
                        **_signed_headers(
                            sender_key,
                            sender,
                            method="POST",
                            path=idempotent_rpc_path,
                            body=idempotent_body,
                        ),
                    },
                )
            )
            idempotent_counts.append(
                {
                    table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
                    for table in ("events", "a2a_tasks", "a2a_task_events")
                }
            )
        accepted = await client.post(
            path,
            content=a2a_body,
            headers={
                "A2A-Version": "1.0",
                "Content-Type": "application/json",
                **_signed_headers(sender_key, sender, method="POST", path=path, body=a2a_body),
            },
        )

        core_request = {
            "collaboration_scope_id": collaboration_scope_id,
            "recipients": [recipient.harness_id],
            "payload": {"text": "same core and mailbox"},
            "idempotency_key": "persistent-core-idempotency-0001",
            "classification": "C1",
        }
        core_body = canonical_json(core_request)
        core_response = await client.post(
            "/v1/messages",
            content=core_body,
            headers={
                "Content-Type": "application/json",
                **_signed_headers(
                    sender_key,
                    sender,
                    method="POST",
                    path="/v1/messages",
                    body=core_body,
                ),
            },
        )
        health_after = await client.get("/healthz")

    native = create_native_a2a_client(
        app.state.a2a_service.runtime.agent_card,
        store=store,
        identity=CorporateA2AClientIdentity(
            key=sender_key,
            domain_id=sender.domain_id,
            harness_id=sender.harness_id,
            credential_id=sender.credential_id,
            audience=config.effective_service_audience,
        ),
        peer_id="127.0.0.1:persistent-ordinary-agent",
        tenant=TENANT,
        policy=SSRFPolicy(
            allowed_hosts=frozenset({"127.0.0.1"}),
            allowed_ports=frozenset({80}),
            allow_loopback_http_lab=True,
        ),
        resolver=lambda host, port: ("127.0.0.1",),
        inner_transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        call_timeout_seconds=0.5,
        total_timeout_seconds=2,
        poll_interval_seconds=0.01,
    )
    native_request = SendMessageRequest(
        tenant=TENANT,
        message=Message(
            message_id="persistent-native-client-message-2",
            role=Role.ROLE_USER,
            parts=[Part(text="official SDK through persistent mount")],
        ),
        metadata={
            "agentnetIntent": "task",
            "agentnetIdempotencyKey": "persistent-native-idempotency-0002",
            "agentnetTaskGrantId": grant.grant_id,
            "agentnetDataClass": "C1",
            "agentnetCollaborationScopeId": collaboration_scope_id,
        },
    )
    try:
        async with asyncio.timeout(3):
            native_facts = await native.send(native_request, wait_for_terminal=False)
    finally:
        await native.close()

    proposals = core.task_proposals(
        actor=recipient,
        collaboration_scope_id=collaboration_scope_id,
    )
    assert len(proposals) == 3
    for proposal in proposals:
        core.approve_task_proposal(
            actor=recipient,
            proposal_id=proposal["proposal_id"],
            request_digest=proposal["request_digest"],
            revision=proposal["revision"],
        )

    assert health_before.status_code == 200
    assert health_after.status_code == 200
    assert readiness.json()["a2a_schema"] == {"ready": True, "required": True}
    assert card_response.status_code == 200
    assert missing_task.status_code == 200
    missing_task_error = missing_task.json()["error"]
    assert missing_task_error["code"] == -32001
    assert missing_task_error["data"][0]["reason"] == "TASK_NOT_FOUND"
    assert missing_counts_after == missing_counts_before
    assert store.fetch_one(
        "SELECT task_id FROM a2a_tasks WHERE task_id=?",
        (missing_task_id,),
    ) is None
    for conflicting_response in conflicting_tenant_responses:
        assert conflicting_response.status_code == 200
        conflicting_error = conflicting_response.json()["error"]
        assert conflicting_error["code"] == -32004
        assert conflicting_error["data"][0]["reason"] == "UNSUPPORTED_OPERATION"
    assert rejected_counts_after == rejected_counts_before
    assert store.fetch_one(
        "SELECT task_id FROM a2a_tasks WHERE task_id=?",
        (conflicting_task_id,),
    ) is None
    assert [response.status_code for response in idempotent_rpc_responses] == [200, 200]
    idempotent_task_ids = [
        response.json()["result"]["task"]["id"] for response in idempotent_rpc_responses
    ]
    assert idempotent_task_ids[0] == idempotent_task_ids[1]
    assert idempotent_counts[0] == idempotent_counts[1]
    card_response_payload = card_response.json()
    assert card_response_payload["name"] == "Persistent ordinary agent"
    assert "agentNetCorporateProof" in card_response_payload["securitySchemes"]
    assert any(
        not requirement.get("schemes")
        for requirement in card_response_payload["securityRequirements"]
    )
    assert accepted.status_code == 200, accepted.text
    assert accepted.json()["task"]["metadata"]["agentnetExecutable"] is False
    assert accepted.json()["task"]["metadata"]["agentnetDisposition"] == "directional_approval_pending"
    assert core_response.status_code == 202, core_response.text
    assert native_facts[-1].kind is A2AMappedKind.TASK
    assert native_facts[-1].task_state == "submitted"
    assert core.mailboxes is app.state.a2a_service.runtime.mailbox
    assert core.policy is app.state.a2a_service.runtime.policy
    assert core.store is app.state.a2a_service.runtime.store
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 4


def test_public_a2a_config_and_owner_provisioned_key_fail_closed(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    recipient, _recipient_key = identity_factory(kind="pi")
    with pytest.raises(PydanticValidationError, match="route, card, standing grant"):
        ExtensionConfig(
            domain_id=recipient.domain_id,
            features=FeatureFlags(public_a2a=True),
            server_agent_capabilities=frozenset({ServerAgentCapability.A2A_GATEWAY}),
        )


def test_a2a_restart_selects_explicit_current_key_only_within_rotation_lineage(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    recipient, original_key = identity_factory(kind="pi", binding_assurance="os_bound")
    sibling, sibling_key = identity_factory(kind="pi", binding_assurance="os_bound")
    original_path = tmp_path / "data" / "secrets" / "a2a-original.pem"
    _write_private_key(original_path, original_key)
    initial = _service_config(
        tmp_path,
        recipient,
        key_path=Path("secrets/a2a-original.pem"),
        peer_namespaces=frozenset(),
    )
    assert _load_signing_identity(CommunicationCore(initial, store)).credential_id == recipient.credential_id

    rotated_key = type(original_key).generate()
    request_id = str(uuid4())
    possession = CredentialRotationRequest.possession_fields(
        request_id=request_id,
        actor=recipient,
        expected_credential_epoch=1,
        new_key_id=rotated_key.thumbprint,
    )
    rotation = CredentialRotationService(store).rotate(
        actor=recipient,
        request=CredentialRotationRequest(
            request_id=request_id,
            expected_credential_epoch=1,
            new_public_key_pem=rotated_key.public_pem,
            new_key_possession_signature=rotated_key.sign(
                CREDENTIAL_ROTATION_POP_PURPOSE,
                possession,
            ),
        ),
    )
    rotated_path = tmp_path / "data" / "secrets" / "a2a-rotated.pem"
    _write_private_key(rotated_path, rotated_key)
    signing = initial.a2a.signing_identity.model_copy(
        update={
            "successors": (
                A2ASigningCredentialConfig(
                    credential_id=rotation.credential_id,
                    private_key_path=Path("secrets/a2a-rotated.pem"),
                ),
            )
        }
    )
    restarted_config = initial.model_copy(
        update={"a2a": initial.a2a.model_copy(update={"signing_identity": signing})}
    )
    restarted_core = CommunicationCore(restarted_config, store)
    restarted_app = create_app(restarted_core)
    current = _load_signing_identity(restarted_core)
    assert restarted_app.state.a2a_service is not None
    assert current.credential_id == rotation.credential_id
    assert current.key.thumbprint == rotated_key.thumbprint

    with pytest.raises(GateBlocked, match="not current"):
        _load_signing_identity(CommunicationCore(initial, store))

    sibling_path = tmp_path / "data" / "secrets" / "a2a-sibling.pem"
    _write_private_key(sibling_path, sibling_key)
    crossed = signing.model_copy(
        update={
            "successors": (
                A2ASigningCredentialConfig(
                    credential_id=sibling.credential_id,
                    private_key_path=Path("secrets/a2a-sibling.pem"),
                ),
            )
        }
    )
    crossed_config = initial.model_copy(
        update={"a2a": initial.a2a.model_copy(update={"signing_identity": crossed})}
    )
    with pytest.raises(GateBlocked, match="crossed its enrolled harness"):
        _load_signing_identity(CommunicationCore(crossed_config, store))

    config = _service_config(
        tmp_path,
        recipient,
        key_path=Path("secrets/missing-a2a-signing.pem"),
        peer_namespaces=frozenset(),
    )
    core = CommunicationCore(config, store)
    with pytest.raises(GateBlocked) as blocked:
        create_app(core)
    assert blocked.value.gate == "a2a_signing_key"

    with pytest.raises(GateBlocked) as remote_bind:
        _require_safe_serve_binding(config, host="0.0.0.0", port=80)
    assert remote_bind.value.gate == "remote_plaintext_bind"
    with pytest.raises(GateBlocked) as wrong_loopback:
        _require_safe_serve_binding(config, host="127.0.0.1", port=8080)
    assert wrong_loopback.value.gate == "loopback_origin"
    _require_safe_serve_binding(config, host="127.0.0.1", port=80)

    with pytest.raises(PydanticValidationError, match="non-secret filesystem reference"):
        A2ASigningIdentityConfig(
            harness_id=recipient.harness_id,
            credential_id=recipient.credential_id,
            private_key_path="-----BEGIN PRIVATE KEY-----\nsecret",
        )


def test_a2a_schema_is_numbered_and_missing_relation_is_not_runtime_created(store) -> None:
    validate_migration_catalog()
    assert [item.version for item in MIGRATIONS] == list(range(1, CURRENT_SCHEMA_VERSION + 1))
    migration = MIGRATIONS[0]
    assert migration.name == "agentnet_first_release_schema"
    assert migration.version == A2A_SCHEMA_VERSION
    assert A2A_SCHEMA_VERSION == 1
    for table in A2A_REQUIRED_TABLES:
        assert f"CREATE TABLE IF NOT EXISTS {table}" in migration.sql
    assert store.readiness()["schema_version"] == CURRENT_SCHEMA_VERSION

    with store.transaction() as connection:
        connection.execute("DROP TABLE a2a_outbound_events")
    with pytest.raises(GateBlocked) as blocked:
        OutboundA2AJournal(store)
    assert blocked.value.gate == "a2a_schema"
    assert store.fetch_one(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='a2a_outbound_events'"
    ) is None
