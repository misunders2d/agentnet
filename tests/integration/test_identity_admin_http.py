from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import httpx
import pytest
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse

from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.authorization.evidence import AUTHORITY_COMMAND_PURPOSE, SignedAuthorityCommand
from agentnet.authorization.policy import HumanEntitlement
from agentnet.client import proof_headers
from agentnet.core.app import CommunicationCore
from agentnet.errors import ExtensionError
from agentnet.http_api import _body_and_actor
from agentnet.identity_admin_http import create_identity_admin_routes
from agentnet.identity.workload import WorkloadTransitionProof
from agentnet.messaging.events import new_event
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.models import Classification, DeliveryFact, EventType
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json


def _signed(key, actor, path: str, body: bytes) -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
            method="POST",
            scheme="http",
            authority="127.0.0.1",
            path=path,
            query="",
            body=body,
        )
    )


def _command(
    key,
    actor,
    *,
    action: str,
    resource: str,
    mutation: dict[str, object],
    entity_revision: int,
    reason: str,
) -> SignedAuthorityCommand:
    now = datetime.now(UTC)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=canonical_digest(mutation),
        expected_policy_revision=1,
        expected_entity_revision=entity_revision,
        reason=reason,
        issued_at=now,
        expires_at=now + timedelta(minutes=2),
    )
    return SignedAuthorityCommand(
        **fields,
        signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
    )


def _admin_app(core: CommunicationCore, verifier: IndependentApprovalVerifier) -> Starlette:
    async def handle(_request: Request, exc: Exception):
        if isinstance(exc, ExtensionError):
            return JSONResponse(exc.public_detail(), status_code=exc.http_status)
        return JSONResponse({"code": "invalid_request"}, status_code=422)

    return Starlette(
        routes=create_identity_admin_routes(core, _body_and_actor, verifier),
        exception_handlers={Exception: handle},
    )


@pytest.mark.anyio
async def test_entitlement_admin_http_uses_transport_actor_and_fences_command_replay(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    sibling, sibling_key = identity_factory(binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=sibling.principal_id,
        domain_id=administrator.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({"authorization.elevation.approve"}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="independent-approval.example",
    )
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=administrator.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    requested = HumanEntitlement(
        domain_id=administrator.domain_id,
        principal_id=sibling.principal_id,
        action="data.read",
        resource_pattern="dataset:quarterly",
        revision=1,
        expires_at=datetime.now(UTC) + timedelta(minutes=15),
    )
    reason = "issue bounded quarterly read"
    resource, mutation = core.policy.entitlement_issuance_binding(requested, reason=reason)
    for actor in (administrator, sibling):
        core.policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action="authorization.entitlement.issue",
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.now(UTC) + timedelta(minutes=15),
            )
        )
    command = _command(
        administrator_key,
        administrator,
        action="authorization.entitlement.issue",
        resource=resource,
        mutation=mutation,
        entity_revision=0,
        reason=reason,
    )
    payload = {
        "entitlement": requested.model_dump(mode="json"),
        "command": command.model_dump(mode="json"),
    }
    body = canonical_json(payload)
    app = _admin_app(core, verifier)
    transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1") as client:
        path = "/v1/admin/entitlements"
        response = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **_signed(administrator_key, administrator, path, body)},
        )
        assert response.status_code == 201
        assert response.json()["entitlement_id"] == requested.entitlement_id

        replay = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **_signed(administrator_key, administrator, path, body)},
        )
        assert replay.status_code == 409

        cross_actor = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **_signed(sibling_key, sibling, path, body)},
        )
        assert cross_actor.status_code == 401

        revoke_reason = "remove bounded quarterly read"
        revoke_resource, revoke_mutation = core.policy.entitlement_revocation_binding(
            requested.entitlement_id,
            expected_entity_revision=1,
            reason=revoke_reason,
        )
        core.policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=administrator.domain_id,
                principal_id=administrator.principal_id,
                action="authorization.entitlement.revoke",
                resource_pattern=revoke_resource,
                revision=1,
                expires_at=datetime.now(UTC) + timedelta(minutes=15),
            )
        )
        revoke_command = _command(
            administrator_key,
            administrator,
            action="authorization.entitlement.revoke",
            resource=revoke_resource,
            mutation=revoke_mutation,
            entity_revision=1,
            reason=revoke_reason,
        )
        revoke_body = canonical_json(
            {"command": revoke_command.model_dump(mode="json")}
        )
        revoke_path = f"/v1/admin/entitlements/{requested.entitlement_id}/revoke"
        revoked = await client.post(
            revoke_path,
            content=revoke_body,
            headers={
                "Content-Type": "application/json",
                **_signed(administrator_key, administrator, revoke_path, revoke_body),
            },
        )
        assert revoked.status_code == 200
        assert revoked.json() == {
            "entitlement_id": requested.entitlement_id,
            "revoked": True,
        }

    assert store.fetch_one(
        "SELECT state FROM audit_intents WHERE intent_id=?",
        (command.command_id,),
    )["state"] == "completed"
    assert store.fetch_one(
        "SELECT revoked_at FROM entitlements WHERE entitlement_id=?",
        (requested.entitlement_id,),
    )["revoked_at"] is not None


@pytest.mark.anyio
async def test_workload_admin_http_rejects_json_claims_without_server_transport_scope(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=administrator.principal_id,
        domain_id=administrator.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({"authorization.elevation.approve"}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="independent-approval.example",
    )
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=administrator.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    body = canonical_json(
        {
            "registration_id": f"workload-registration-{uuid4().hex}",
            "workload_id": "attacker-selected",
            "workload_role": "effect_authority",
            "recipient_scope": "*",
            "public_key_pem": P256KeyPair.generate().public_pem,
            "key_id": "0" * 64,
            "credential_epoch": 1,
            "revocation_epoch": 1,
            "issued_at": int(datetime.now(UTC).timestamp()),
            "expires_at": int(datetime.now(UTC).timestamp()) + 300,
            "possession_signature": "claimed",
            "command": {
                "command_version": 1,
                "command_id": "claimed-command",
                "actor": administrator.model_dump(mode="json"),
                "action": "identity.workload.register",
                "resource": "workload:claimed",
                "request_digest": "0" * 64,
                "expected_policy_revision": 1,
                "expected_entity_revision": 0,
                "reason": "attempt JSON workload fabrication",
                "issued_at": datetime.now(UTC).isoformat(),
                "expires_at": (datetime.now(UTC) + timedelta(minutes=1)).isoformat(),
                "approval_threshold": 1,
                "signature": "claimed",
            },
        }
    )
    path = "/v1/admin/workloads"
    transport = httpx.ASGITransport(app=_admin_app(core, verifier), raise_app_exceptions=False)
    async with httpx.AsyncClient(transport=transport, base_url="http://127.0.0.1") as client:
        response = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json", **_signed(administrator_key, administrator, path, body)},
        )
    assert response.status_code == 401
    assert store.fetch_one("SELECT COUNT(*) AS count FROM workload_registrations")["count"] == 0


@pytest.mark.anyio
async def test_workload_registration_http_uses_only_server_injected_transport_binding(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=administrator.principal_id,
        domain_id=administrator.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({"authorization.elevation.approve"}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="independent-approval.example",
    )
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=administrator.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    now = int(datetime.now(UTC).timestamp())
    registration_id = f"workload-registration-{uuid4().hex}"
    workload_key = P256KeyPair.generate()
    workload_transport_facts = {
        "schema_version": "1.0",
        "spiffe_id": f"spiffe://{administrator.domain_id}/mailbox/http-worker",
        "trust_domain": administrator.domain_id,
        "workload_role": "mailbox_dispatcher",
        "certificate_serial": "http-worker-serial",
        "process_id": 8112,
        "process_start_time": now - 10,
        "session_id": f"workload-session-{uuid4().hex}",
    }
    workload_transport = core.workloads.spiffe.transport_authority.bind_verified_peer(
        workload_transport_facts
    )
    identity = core.workloads.spiffe.resolve(workload_transport)
    mutation = core.workloads.registration_request(
        registration_id=registration_id,
        domain_id=administrator.domain_id,
        workload_id="mailbox.http-worker",
        workload_role="mailbox_dispatcher",
        recipient_scope="*",
        process_id=workload_transport.facts.process_id,
        process_start_time=workload_transport.facts.process_start_time,
        session_id=workload_transport.facts.session_id,
        identity=identity,
        public_key_pem=workload_key.public_pem,
        key_id=workload_key.thumbprint,
        credential_epoch=1,
        revocation_epoch=1,
        parent_event_id=None,
        task_grant_id=None,
        issued_at=now,
        expires_at=now + 600,
    )
    resource = f"workload:{registration_id}"
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=administrator.domain_id,
            principal_id=administrator.principal_id,
            action="identity.workload.register",
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.now(UTC) + timedelta(minutes=15),
        )
    )
    command = _command(
        administrator_key,
        administrator,
        action="identity.workload.register",
        resource=resource,
        mutation=mutation,
        entity_revision=0,
        reason="register transport-bound HTTP worker",
    )
    body = canonical_json(
        {
            "registration_id": registration_id,
            "workload_id": "mailbox.http-worker",
            "workload_role": "mailbox_dispatcher",
            "recipient_scope": "*",
            "public_key_pem": workload_key.public_pem,
            "key_id": workload_key.thumbprint,
            "credential_epoch": 1,
            "revocation_epoch": 1,
            "issued_at": now,
            "expires_at": now + 600,
            "possession_signature": workload_key.sign(
                "agentnet.workload.registration.pop.v1",
                mutation,
            ),
            "command": command.model_dump(mode="json"),
        }
    )
    base_app = _admin_app(core, verifier)

    async def transport_bound_app(scope, receive, send):
        forwarded = dict(scope)
        forwarded["agentnet.workload_transport"] = workload_transport
        await base_app(forwarded, receive, send)

    path = "/v1/admin/workloads"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=transport_bound_app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        response = await client.post(
            path,
            content=body,
            headers={
                "Content-Type": "application/json",
                **_signed(administrator_key, administrator, path, body),
            },
        )
    assert response.status_code == 201, response.text
    assert response.json()["workload_registration_id"] == registration_id
    stored = store.fetch_one(
        "SELECT spiffe_id,process_id,session_id,status FROM workload_registrations WHERE registration_id=?",
        (registration_id,),
    )
    assert dict(stored) == {
        "spiffe_id": workload_transport.facts.spiffe_id,
        "process_id": workload_transport.facts.process_id,
        "session_id": workload_transport.facts.session_id,
        "status": "active",
    }


@pytest.mark.anyio
async def test_registered_dispatcher_http_expires_due_mail_atomically_with_server_bounded_clock(
    store,
    identity_factory,
    workload_factory,
    tmp_path: Path,
) -> None:
    sender, _sender_key = identity_factory(binding_assurance="os_bound")
    recipient, _recipient_key = identity_factory(binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=sender.principal_id,
        domain_id=sender.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({"authorization.elevation.approve"}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="independent-approval.example",
    )
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=sender.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    event_clock = datetime.now(UTC)
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"text": "already expired"},
        idempotency_key=f"expiry-http-{uuid4()}",
        recipients=(recipient.harness_id,),
        delivery_expires_at=event_clock + timedelta(minutes=1),
        retention_delete_at=event_clock + timedelta(days=1),
    ).model_copy(
        update={
            "created_at": event_clock - timedelta(seconds=10),
            "delivery_expires_at": event_clock - timedelta(seconds=1),
        }
    )
    core.mailboxes.accept(event)
    dispatcher, dispatcher_key = workload_factory(
        domain=sender.domain_id,
        role="mailbox_dispatcher",
        recipient_scope=recipient.harness_id,
    )
    row = store.fetch_one(
        "SELECT spiffe_id,certificate_serial FROM workload_registrations WHERE registration_id=?",
        (dispatcher.workload_registration_id,),
    )
    authoritative_clock = int(datetime.now(UTC).timestamp())
    detail = {"authoritative_clock": authoritative_clock}
    proof = WorkloadTransitionProof.create(
        dispatcher_key,
        actor=dispatcher,
        event_id=event.event_id,
        recipient_id=recipient.harness_id,
        proposed_fact=DeliveryFact.EXPIRED,
        detail=detail,
        timestamp=authoritative_clock,
    )
    workload_transport = core.workloads.spiffe.transport_authority.bind_verified_peer({
        "schema_version": "1.0",
        "spiffe_id": row["spiffe_id"],
        "trust_domain": sender.domain_id,
        "workload_role": "mailbox_dispatcher",
        "certificate_serial": row["certificate_serial"],
        "process_id": dispatcher.workload_process_id,
        "process_start_time": dispatcher.workload_process_start_time,
        "session_id": dispatcher.workload_session_id,
    })
    base_app = _admin_app(core, verifier)

    async def transport_bound_app(scope, receive, send):
        forwarded = dict(scope)
        forwarded["agentnet.workload_transport"] = workload_transport
        await base_app(forwarded, receive, send)

    body = canonical_json(
        {
            "authoritative_clock": authoritative_clock,
            "proofs": [proof.model_dump(mode="json")],
        }
    )
    path = "/v1/workloads/mailbox/expire-due"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=transport_bound_app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        response = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json"},
        )
    assert response.status_code == 200, response.text
    assert response.json() == {"expired": 1}
    assert core.mailboxes.reconcile(recipient.harness_id)[0]["fact"] == DeliveryFact.EXPIRED.value

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=base_app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        missing_transport = await client.post(
            path,
            content=body,
            headers={"Content-Type": "application/json"},
        )
    assert missing_transport.status_code == 401
