from __future__ import annotations

import hashlib
import json
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.artifacts.scanner import ArtifactDerivationV1
from agentnet.artifacts.service import MAX_ARTIFACT_BYTES
from agentnet.automation import (
    AUTOMATION_CHARTER_APPROVAL_PURPOSE,
    AutomationCharter,
    AutomationInvocation,
    AutomationInvocationCompletion,
)
from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    HumanEntitlement,
    SignedAuthorityCommand,
)
from agentnet.authorization.decision import AuthorizationDecision, DecisionRecorder
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.discovery.directory import DirectoryRecord
from agentnet.effects.reservations import (
    EffectExecutionEvidence,
    EffectReconciliationEvidence,
    EffectState,
    EffectTerminalEvidence,
    EffectTransitionProof,
    EffectUncertaintyEvidence,
)
from agentnet.errors import GateBlocked
from agentnet.http_api import create_app
from agentnet.identity.workload import AuthenticatedSPIFFETransport
from agentnet.messaging.events import new_event
from agentnet.organization import (
    AssignmentRequest,
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipPolicyException,
    TaskConflictAdjudication,
)
from agentnet.operations.config import ExtensionConfig, FeatureFlags, RuntimeProfile
from agentnet.operations.incident import IncidentMode, IncidentModeChange
from agentnet.operations.versioning import (
    CompatibilityRequirement,
    VersioningService,
    VersionWindow,
)
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    EventType,
    PresenceLease,
    Relationship,
    TaskGrant,
)
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ParentDigestSet,
    ProvenanceDerivation,
    ProvenanceObjectType,
    ProvenanceOrigin,
    SinkSet,
    TransformationKind,
    TransformationStep,
)
from agentnet.rooms.governance import (
    RoomTransferSnapshot,
    SourceTransferProposal,
    TargetTransferAcceptance,
)
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.client import proof_headers


def _core(
    store,
    tmp_path: Path,
    *,
    domain: str,
    approval_verifier: IndependentApprovalVerifier | None = None,
) -> CommunicationCore:
    return CommunicationCore(
        ExtensionConfig(
            domain_id=domain,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
            features=FeatureFlags(protected_effects=True),
        ),
        store,
        approval_verifier=approval_verifier,
    )


def _headers(
    key,
    actor,
    method: str,
    path: str,
    body: bytes,
    *,
    query: str = "",
    audience_domain: str | None = None,
) -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{audience_domain or actor.domain_id}:corporate-api",
            method=method,
            scheme="http",
            authority="127.0.0.1",
            path=path,
            query=query,
            body=body,
        )
    )


async def _request(
    client,
    key,
    actor,
    method: str,
    path: str,
    value=None,
    *,
    query: str = "",
    audience_domain: str | None = None,
):
    body = canonical_json(value) if value is not None else b""
    target = f"{path}?{query}" if query else path
    return await client.request(
        method,
        target,
        content=body,
        headers={
            "Content-Type": "application/json",
            **_headers(
                key,
                actor,
                method,
                path,
                body,
                query=query,
                audience_domain=audience_domain,
            ),
        },
    )


async def _raw_request(
    client,
    key,
    actor,
    method: str,
    path: str,
    content: bytes,
    *,
    content_type: str,
    audience_domain: str | None = None,
):
    return await client.request(
        method,
        path,
        content=content,
        headers={
            "Content-Type": content_type,
            **_headers(
                key,
                actor,
                method,
                path,
                content,
                audience_domain=audience_domain,
            ),
        },
    )


def _allow(core: CommunicationCore, actor, action: str, resource: str) -> None:
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.now(UTC) + timedelta(hours=1),
        )
    )


def _command(*, key, actor, action: str, resource: str, request: dict[str, object], revision: int, reason: str):
    now = datetime.now(UTC)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=canonical_digest(request),
        expected_policy_revision=1,
        expected_entity_revision=revision,
        reason=reason,
        issued_at=now,
        expires_at=now + timedelta(minutes=2),
    )
    return SignedAuthorityCommand(
        **fields,
        signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
    )


@pytest.mark.anyio
async def test_communication_only_artifact_routes_fail_before_body_auth_metadata_or_audit(
    store,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr("agentnet.core.app.is_verified_postgresql_store", lambda _store: True)
    artifact_dir = tmp_path / "artifacts-must-not-exist"
    core = CommunicationCore(
        ExtensionConfig(
            profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            domain_id="corp.example",
            data_dir=tmp_path / "data",
            database_url="postgresql://agentnet@postgres/agentnet",
            artifact_backend="postgres-manifest",
            artifact_mode="disabled",
            artifact_dir=artifact_dir,
            public_base_url="https://core.corp.example",
            enrolled_harness_id="server-harness",
            enrolled_credential_id="server-credential",
            server_agent_capabilities={ServerAgentCapability.OFFLINE_CUSTODY},
        ),
        store,
    )
    routes = (
        ("POST", "/v1/artifacts/reservations", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/reservations/missing/bytes", b"secret-bytes", "application/octet-stream"),
        ("POST", "/v1/artifacts/reservations/missing/abort", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/reservations/missing/promote", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/missing/scan", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/missing/release", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/missing/download-capabilities", b"not-json", "application/json"),
        ("GET", "/v1/artifacts/missing/lifecycle", b"", "application/json"),
        ("POST", "/v1/artifacts/missing/legal-hold", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/missing/legal-hold/clear", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/missing/delete", b"not-json", "application/json"),
        ("POST", "/v1/artifacts/download", b"not-json", "application/json"),
    )
    observed_tables = (
        "artifact_reservations",
        "artifact_manifests",
        "artifact_release_outbox",
        "artifact_lifecycle",
        "artifact_deletion_outbox",
        "download_capabilities",
        "audit_intents",
        "audit_log",
    )
    before = {
        table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
        for table in observed_tables
    }

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        for method, path, body, content_type in routes:
            response = await client.request(
                method,
                path,
                content=body,
                headers={"Content-Type": content_type},
            )
            assert response.status_code == 503, (method, path, response.text)
            assert response.json()["gate"] == "artifacts_disabled"

    after = {
        table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
        for table in observed_tables
    }
    assert after == before
    assert not artifact_dir.exists()


@pytest.mark.anyio
async def test_raw_artifact_upload_reaches_exact_advertised_ceiling(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, actor_key = identity_factory(binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=actor.domain_id)
    content = b"x" * MAX_ARTIFACT_BYTES
    _allow(core, actor, "artifact.upload.reserve", "artifact:new")
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        reserved = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/artifacts/reservations",
            {
                "idempotency_key": "artifact-http-exact-max-0001",
                "expected_digest": hashlib.sha256(content).hexdigest(),
                "expected_size": len(content),
                "media_type": "application/octet-stream",
                "classification": "C1",
                "required_attachment": True,
            },
        )
        assert reserved.status_code == 201, reserved.text
        reservation_id = reserved.json()["reservation_id"]
        _allow(core, actor, "artifact.upload.bytes", reservation_id)
        uploaded = await _raw_request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/bytes",
            content,
            content_type="application/octet-stream",
        )
        assert uploaded.status_code == 200, uploaded.text
        assert uploaded.json()["size"] == MAX_ARTIFACT_BYTES

        too_large = await _raw_request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/bytes",
            content + b"x",
            content_type="application/octet-stream",
        )
        assert too_large.status_code == 422


@pytest.mark.anyio
async def test_artifact_reservation_abort_http_is_actor_scoped_and_releases_quota(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, actor_key = identity_factory(binding_assurance="os_bound")
    intruder, intruder_key = identity_factory(
        domain=actor.domain_id, binding_assurance="os_bound"
    )
    core = _core(store, tmp_path, domain=actor.domain_id)
    content = b"abort-http"
    _allow(core, actor, "artifact.upload.reserve", "artifact:new")
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        reserved = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/artifacts/reservations",
            {
                "idempotency_key": "artifact-http-abort-0001",
                "expected_digest": hashlib.sha256(content).hexdigest(),
                "expected_size": len(content),
                "media_type": "application/octet-stream",
                "classification": "C1",
                "required_attachment": True,
            },
        )
        assert reserved.status_code == 201, reserved.text
        reservation_id = reserved.json()["reservation_id"]
        _allow(core, intruder, "artifact.upload.abort", reservation_id)
        denied = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/abort",
            {},
        )
        assert denied.status_code == 404
        assert reserved.json()["request_digest"] not in denied.text

        _allow(core, actor, "artifact.upload.abort", reservation_id)
        aborted = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/abort",
            {},
        )
        assert aborted.status_code == 200, aborted.text
        assert aborted.json()["state"] == "aborted"
        repeated = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/abort",
            {},
        )
        assert repeated.status_code == 200, repeated.text
        assert repeated.json()["duplicate"] is True
    assert store.fetch_one(
        "SELECT state FROM artifact_byte_charges WHERE reservation_id=?", (reservation_id,)
    )["state"] == "released"
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (actor.principal_id,),
    )["used_bytes"] == 0


@pytest.mark.anyio
async def test_relationship_and_task_grant_http_use_exact_authority_and_non_enumerating_reads(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    subordinate, subordinate_key = identity_factory(kind="pi", binding_assurance="os_bound")
    intruder, intruder_key = identity_factory(kind="claude", binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate.principal_id,
        domain_id=owner.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="relationship-owner-consent",
    )
    core = _core(
        store,
        tmp_path,
        domain=owner.domain_id,
        approval_verifier=verifier,
    )
    assert core.approval_verifier is verifier
    assert core.relationships.approval_verifier is verifier
    relationship = Relationship(
        domain_id=owner.domain_id,
        administrator_harness_id=owner.harness_id,
        subordinate_harness_id=subordinate.harness_id,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    proposal_expires_at = datetime.now(UTC) + timedelta(minutes=5)
    relationship_resource = f"relationship:{relationship.relationship_id}"
    for action in ("organization.relationship.propose", "organization.relationship.read"):
        _allow(core, owner, action, relationship_resource)
    _allow(core, subordinate, "organization.relationship.revoke", relationship_resource)
    _allow(core, intruder, "organization.relationship.read", relationship_resource)

    grant = TaskGrant(
        domain_id=owner.domain_id,
        principal_id=owner.principal_id,
        harness_id=owner.harness_id,
        actions=frozenset({"dataset.read"}),
        resources=frozenset({"dataset:alpha"}),
        input_sources=frozenset({"mailbox:event-1"}),
        output_sinks=frozenset({"worker:clean"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    grant_resource = f"task-grant:{grant.grant_id}"
    for action in (
        "authorization.task_grant.issue",
        "authorization.task_grant.read",
        "authorization.task_grant.revoke",
    ):
        _allow(core, owner, action, grant_resource)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        unmatched = await client.get("/v1/relationships/private/unknown-subresource")
        wrong_method = await client.put("/v1/relationships")
        assert unmatched.status_code == 404
        assert wrong_method.status_code == 405
        for response in (unmatched, wrong_method):
            assert response.headers["cache-control"] == "no-store"
            assert response.headers["pragma"] == "no-cache"
            assert response.headers["referrer-policy"] == "no-referrer"
            assert response.headers["x-content-type-options"] == "nosniff"

        for field, value in (("may_assign", 0), ("revision", "1")):
            malformed_relationship = relationship.model_dump(mode="json")
            malformed_relationship[field] = value
            malformed = await _request(
                client,
                owner_key,
                owner,
                "POST",
                "/v1/relationships",
                {
                    "relationship": malformed_relationship,
                    "proposal_expires_at": proposal_expires_at.isoformat(),
                },
            )
            assert malformed.status_code == 422
            assert malformed.headers["cache-control"] == "no-store"

        asserted_actor = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/relationships",
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
                "actor": subordinate.audit_view(),
            },
        )
        assert asserted_actor.status_code == 422
        assert asserted_actor.headers["cache-control"] == "no-store"

        proposed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/relationships",
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            },
        )
        assert proposed.status_code == 201, proposed.text
        assert proposed.headers["cache-control"] == "no-store"
        proposal = proposed.json()["proposal"]
        assert proposal["lifecycle_state"] == "proposed"
        assert proposal["activation_basis"] is None

        issued_at = int(time.time())
        approval = create_independent_approval_receipt(
            approval_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
            canonical_transaction=canonical_json(proposal["consent_transaction"]),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )
        strict_acceptance = {
            "approval": approval,
            "expected_transaction_digest": proposal["transaction_digest"],
            "expected_relationship_revision": proposal["revision"],
            "expected_lifecycle_revision": proposal["lifecycle_revision"],
        }
        malformed_acceptance_bodies = []
        for field, value in (
            ("expected_relationship_revision", str(proposal["revision"])),
            ("expected_lifecycle_revision", float(proposal["lifecycle_revision"])),
        ):
            malformed = dict(strict_acceptance)
            malformed[field] = value
            malformed_acceptance_bodies.append(malformed)
        for field, value in (("approved", 1), ("issued_at", str(approval["issued_at"]))):
            malformed = dict(strict_acceptance)
            malformed["approval"] = {**approval, field: value}
            malformed_acceptance_bodies.append(malformed)
        for malformed in malformed_acceptance_bodies:
            rejected = await _request(
                client,
                subordinate_key,
                subordinate,
                "POST",
                f"/v1/relationships/{relationship.relationship_id}/accept",
                malformed,
            )
            assert rejected.status_code == 422
            assert rejected.headers["cache-control"] == "no-store"

        asserted_verification = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/accept",
            {
                "approval": {**approval, "verified": True},
                "expected_transaction_digest": proposal["transaction_digest"],
                "expected_relationship_revision": proposal["revision"],
                "expected_lifecycle_revision": proposal["lifecycle_revision"],
            },
        )
        assert asserted_verification.status_code == 422
        assert asserted_verification.headers["cache-control"] == "no-store"

        nonparticipant_body = {
            "approval": approval,
            "expected_transaction_digest": "0" * 64,
            "expected_relationship_revision": 999,
            "expected_lifecycle_revision": 999,
        }
        hidden_existing_accept = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/accept",
            nonparticipant_body,
        )
        hidden_missing_accept = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            "/v1/relationships/nonexistent-relationship/accept",
            nonparticipant_body,
        )
        assert hidden_existing_accept.status_code == hidden_missing_accept.status_code == 404
        assert hidden_existing_accept.json() == hidden_missing_accept.json()
        assert hidden_existing_accept.headers["cache-control"] == "no-store"
        assert hidden_missing_accept.headers["cache-control"] == "no-store"

        accepted = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/accept",
            {
                "approval": approval,
                "expected_transaction_digest": proposal["transaction_digest"],
                "expected_relationship_revision": proposal["revision"],
                "expected_lifecycle_revision": proposal["lifecycle_revision"],
            },
        )
        assert accepted.status_code == 200, accepted.text
        assert accepted.headers["cache-control"] == "no-store"
        assert accepted.json()["relationship"]["lifecycle_state"] == "active"
        assert (
            accepted.json()["relationship"]["activation_basis"]
            == "subordinate_owner_consent"
        )
        fetched = await _request(
            client,
            owner_key,
            owner,
            "GET",
            f"/v1/relationships/{relationship.relationship_id}",
        )
        assert fetched.status_code == 200, fetched.text
        assert fetched.headers["cache-control"] == "no-store"
        hidden = await _request(
            client,
            intruder_key,
            intruder,
            "GET",
            f"/v1/relationships/{relationship.relationship_id}",
        )
        assert hidden.status_code == 404
        assert hidden.headers["cache-control"] == "no-store"

        relationship_reason = "subordinate owner exited exact edge"
        _, relationship_revoke_request = core.relationships.revocation_binding(
            relationship.relationship_id,
            expected_relationship_revision=1,
            expected_lifecycle_revision=2,
            reason=relationship_reason,
        )
        relationship_command = _command(
            key=subordinate_key,
            actor=subordinate,
            action="organization.relationship.revoke",
            resource=relationship_resource,
            request=relationship_revoke_request,
            revision=2,
            reason=relationship_reason,
        )
        malformed_revoke_command = relationship_command.model_dump(mode="json")
        malformed_revoke_command["expected_entity_revision"] = str(
            malformed_revoke_command["expected_entity_revision"]
        )
        malformed_revoke = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/revoke",
            {"command": malformed_revoke_command},
        )
        assert malformed_revoke.status_code == 422
        assert malformed_revoke.headers["cache-control"] == "no-store"

        revoked = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/revoke",
            {"command": relationship_command.model_dump(mode="json")},
        )
        assert revoked.status_code == 200, revoked.text
        assert revoked.headers["cache-control"] == "no-store"

        grant_issued = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/task-grants",
            {"grant": grant.model_dump(mode="json")},
        )
        assert grant_issued.status_code == 201, grant_issued.text
        grant_fetched = await _request(
            client,
            owner_key,
            owner,
            "GET",
            f"/v1/task-grants/{grant.grant_id}",
        )
        assert grant_fetched.status_code == 200

        grant_reason = "beneficiary ended exact grant"
        _, grant_revoke_request = core.grants.revocation_binding(
            grant.grant_id,
            expected_entity_revision=1,
            reason=grant_reason,
        )
        grant_command = _command(
            key=owner_key,
            actor=owner,
            action="authorization.task_grant.revoke",
            resource=grant_resource,
            request=grant_revoke_request,
            revision=1,
            reason=grant_reason,
        )
        grant_revoked = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/task-grants/{grant.grant_id}/revoke",
            {"command": grant_command.model_dump(mode="json")},
        )
        assert grant_revoked.status_code == 200, grant_revoked.text


@pytest.mark.anyio
@pytest.mark.parametrize(
    "malformed_case",
    (
        "relationship_revision_string",
        "lifecycle_revision_float",
        "approval_boolean_integer",
        "approval_issued_at_string",
    ),
)
async def test_relationship_acceptance_malformed_schema_is_exact_and_fixture_isolated(
    store,
    identity_factory,
    tmp_path: Path,
    malformed_case: str,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    subordinate, subordinate_key = identity_factory(kind="pi", binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate.principal_id,
        domain_id=administrator.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id=f"isolated-malformed-{malformed_case}",
    )
    core = _core(
        store,
        tmp_path,
        domain=administrator.domain_id,
        approval_verifier=verifier,
    )
    relationship = Relationship(
        domain_id=administrator.domain_id,
        administrator_harness_id=administrator.harness_id,
        subordinate_harness_id=subordinate.harness_id,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    _allow(
        core,
        administrator,
        "organization.relationship.propose",
        f"relationship:{relationship.relationship_id}",
    )
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        proposed = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            "/v1/relationships",
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": (datetime.now(UTC) + timedelta(minutes=5)).isoformat(),
            },
        )
        assert proposed.status_code == 201, proposed.text
        proposal = proposed.json()["proposal"]
        issued_at = int(time.time())
        approval = create_independent_approval_receipt(
            approval_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
            canonical_transaction=canonical_json(proposal["consent_transaction"]),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )
        body = {
            "approval": approval,
            "expected_transaction_digest": proposal["transaction_digest"],
            "expected_relationship_revision": proposal["revision"],
            "expected_lifecycle_revision": proposal["lifecycle_revision"],
        }
        if malformed_case == "relationship_revision_string":
            body["expected_relationship_revision"] = str(proposal["revision"])
        elif malformed_case == "lifecycle_revision_float":
            body["expected_lifecycle_revision"] = float(proposal["lifecycle_revision"])
        elif malformed_case == "approval_boolean_integer":
            body["approval"] = {**approval, "approved": 1}
        else:
            body["approval"] = {**approval, "issued_at": str(approval["issued_at"])}

        rejected = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/accept",
            body,
        )

    assert rejected.status_code == 422
    assert rejected.json() == {
        "code": "invalid_request",
        "message": "request validation failed",
    }
    assert rejected.headers["cache-control"] == "no-store"


@pytest.mark.anyio
async def test_authenticated_relationship_renewal_requires_fresh_exact_consent_and_fences_assignment_revisions(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    subordinate, subordinate_key = identity_factory(kind="pi", binding_assurance="os_bound")
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate.principal_id,
        domain_id=administrator.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="relationship-http-renewal-owner-consent",
    )
    core = _core(
        store,
        tmp_path,
        domain=administrator.domain_id,
        approval_verifier=verifier,
    )
    now = datetime.now(UTC)
    resources = [
        "catalog:before-activation",
        "catalog:revision-one",
        "catalog:renewal-proposed",
        "catalog:premature-revision-two",
        "catalog:stale-revision-one",
        "catalog:revision-two",
    ]
    scope = {
        "task_types": ["research"],
        "resources": resources,
        "data_classes": ["C1"],
        "tools": [],
        "max_budget": 10,
        "max_duration_seconds": 1800,
        "max_concurrency": 1,
        "authority_effect": "custody_only",
    }
    first = Relationship(
        relationship_id="relationship-http-renewal-v1",
        revision=1,
        domain_id=administrator.domain_id,
        administrator_harness_id=administrator.harness_id,
        subordinate_harness_id=subordinate.harness_id,
        may_assign=True,
        assignment_scope=scope,
        expires_at=now + timedelta(hours=1),
    )
    second = first.model_copy(
        update={
            "relationship_id": "relationship-http-renewal-v2",
            "revision": 2,
            "expires_at": now + timedelta(hours=2),
        }
    )
    for relationship in (first, second):
        resource = f"relationship:{relationship.relationship_id}"
        _allow(core, administrator, "organization.relationship.propose", resource)
        _allow(core, administrator, "organization.relationship.read", resource)

    def approval_for(proposal: dict[str, object]) -> dict[str, object]:
        issued_at = int(time.time())
        return create_independent_approval_receipt(
            approval_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
            canonical_transaction=canonical_json(proposal["consent_transaction"]),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )

    async def propose(client: httpx.AsyncClient, relationship: Relationship) -> dict[str, object]:
        response = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            "/v1/relationships",
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": (datetime.now(UTC) + timedelta(minutes=5)).isoformat(),
            },
        )
        assert response.status_code == 201, response.text
        return response.json()["proposal"]

    async def assign(
        client: httpx.AsyncClient,
        *,
        key: str,
        resource: str,
        expected_revision: int,
    ) -> httpx.Response:
        return await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            "/v1/tasks/assign",
            {
                "recipient_harness_id": subordinate.harness_id,
                "task_type": "research",
                "resources": [resource],
                "data_classes": ["C1"],
                "tools": [],
                "budget": 0,
                "concurrency": 1,
                "expected_relationship_revision": expected_revision,
                "intent": {
                    "schema_version": "1.0",
                    "resources": [
                        {
                            "resource": resource,
                            "operation": "research",
                            "access": "write",
                            "exclusivity": "exclusive",
                        }
                    ],
                },
                "task_payload": {"instruction": key},
                "released_artifacts": [],
                "idempotency_key": key,
            },
        )

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        first_proposal = await propose(client, first)
        before_activation = await assign(
            client,
            key="relationship-renewal-before-activation-0001",
            resource=resources[0],
            expected_revision=1,
        )
        assert before_activation.status_code == 202, before_activation.text
        assert before_activation.json()["fact"] == DeliveryFact.PENDING_HUMAN.value
        assert before_activation.json()["reason"] == "no_active_directed_assignment_relationship"

        first_approval = approval_for(first_proposal)
        unused_old_approval = approval_for(first_proposal)
        first_activation_body = {
            "approval": first_approval,
            "expected_transaction_digest": first_proposal["transaction_digest"],
            "expected_relationship_revision": first_proposal["revision"],
            "expected_lifecycle_revision": first_proposal["lifecycle_revision"],
        }
        activated_first = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{first.relationship_id}/accept",
            first_activation_body,
        )
        assert activated_first.status_code == 200, activated_first.text
        assert activated_first.json()["relationship"]["lifecycle_state"] == "active"
        first_assignment = await assign(
            client,
            key="relationship-renewal-revision-one-0001",
            resource=resources[1],
            expected_revision=1,
        )
        assert first_assignment.status_code == 202, first_assignment.text
        assert first_assignment.json()["fact"] == DeliveryFact.ACCEPTED_QUEUED.value

        second_proposal = await propose(client, second)
        still_first = await assign(
            client,
            key="relationship-renewal-proposal-no-authority-0001",
            resource=resources[2],
            expected_revision=1,
        )
        assert still_first.status_code == 202, still_first.text
        assert still_first.json()["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
        premature_second = await assign(
            client,
            key="relationship-renewal-premature-revision-two-0001",
            resource=resources[3],
            expected_revision=2,
        )
        assert premature_second.status_code == 202, premature_second.text
        assert premature_second.json()["fact"] == DeliveryFact.PENDING_HUMAN.value
        assert premature_second.json()["reason"] == "stale_relationship_revision"

        old_consent = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{second.relationship_id}/accept",
            {
                "approval": unused_old_approval,
                "expected_transaction_digest": second_proposal["transaction_digest"],
                "expected_relationship_revision": second_proposal["revision"],
                "expected_lifecycle_revision": second_proposal["lifecycle_revision"],
            },
        )
        assert old_consent.status_code == 401

        second_approval = approval_for(second_proposal)
        second_activation_body = {
            "approval": second_approval,
            "expected_transaction_digest": second_proposal["transaction_digest"],
            "expected_relationship_revision": second_proposal["revision"],
            "expected_lifecycle_revision": second_proposal["lifecycle_revision"],
        }
        activated_second = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{second.relationship_id}/accept",
            second_activation_body,
        )
        assert activated_second.status_code == 200, activated_second.text
        assert activated_second.json()["relationship"]["lifecycle_state"] == "active"
        replay = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            f"/v1/relationships/{second.relationship_id}/accept",
            second_activation_body,
        )
        assert replay.status_code == 409

        predecessor = await _request(
            client,
            administrator_key,
            administrator,
            "GET",
            f"/v1/relationships/{first.relationship_id}",
        )
        assert predecessor.status_code == 200, predecessor.text
        predecessor_record = predecessor.json()["relationship"]
        assert predecessor_record["lifecycle_state"] == "superseded"
        assert predecessor_record["superseded_by_relationship_id"] == second.relationship_id

        stale_first = await assign(
            client,
            key="relationship-renewal-stale-revision-one-0001",
            resource=resources[4],
            expected_revision=1,
        )
        assert stale_first.status_code == 202, stale_first.text
        assert stale_first.json()["fact"] == DeliveryFact.PENDING_HUMAN.value
        assert stale_first.json()["reason"] == "stale_relationship_revision"
        current_second = await assign(
            client,
            key="relationship-renewal-current-revision-two-0001",
            resource=resources[5],
            expected_revision=2,
        )
        assert current_second.status_code == 202, current_second.text
        assert current_second.json()["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
        assert current_second.json()["relationship_revision"] == 2


@pytest.mark.anyio
async def test_authenticated_task_conflict_http_is_exact_owner_scoped_strict_and_replay_fenced(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    first_admin, first_key = identity_factory(binding_assurance="os_bound")
    second_admin, second_key = identity_factory(
        domain=first_admin.domain_id,
        kind="claude",
        binding_assurance="os_bound",
    )
    subordinate, subordinate_key = identity_factory(
        domain=first_admin.domain_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate.principal_id,
        domain_id=first_admin.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="task-conflict-http-owner-consent",
    )
    core = _core(
        store,
        tmp_path,
        domain=first_admin.domain_id,
        approval_verifier=verifier,
    )
    now = datetime.now(UTC)
    for administrator in (first_admin, second_admin):
        relationship = Relationship(
            domain_id=administrator.domain_id,
            administrator_harness_id=administrator.harness_id,
            subordinate_harness_id=subordinate.harness_id,
            may_assign=True,
            assignment_scope={
                "task_types": ["research"],
                "resources": ["catalog:alpha"],
                "data_classes": ["C1"],
                "tools": [],
                "max_budget": 10,
                "max_duration_seconds": 1800,
                "max_concurrency": 1,
                "authority_effect": "custody_only",
            },
            expires_at=now + timedelta(hours=1),
        )
        _allow(
            core,
            administrator,
            "organization.relationship.propose",
            f"relationship:{relationship.relationship_id}",
        )
        proposal = core.propose_relationship(
            actor=administrator,
            relationship=relationship,
            proposal_expires_at=now + timedelta(minutes=5),
        )
        issued_at = int(time.time())
        approval = create_independent_approval_receipt(
            approval_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
            canonical_transaction=canonical_json(
                proposal.consent_transaction.model_dump(mode="json")
            ),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )
        core.accept_relationship(
            actor=subordinate,
            relationship_id=relationship.relationship_id,
            approval=approval,
            expected_transaction_digest=proposal.transaction_digest,
            expected_relationship_revision=proposal.revision,
            expected_lifecycle_revision=proposal.lifecycle_revision,
        )
    accepted: list[dict[str, object]] = []
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as assignment_client:
        for index, (administrator, key) in enumerate(
            ((first_admin, first_key), (second_admin, second_key))
        ):
            response = await _request(
                assignment_client,
                key,
                administrator,
                "POST",
                "/v1/tasks/assign",
                {
                    "recipient_harness_id": subordinate.harness_id,
                    "task_type": "research",
                    "resources": ["catalog:alpha"],
                    "data_classes": [Classification.C1_INTERNAL.value],
                    "tools": [],
                    "budget": 0,
                    "concurrency": 1,
                    "expected_relationship_revision": 1,
                    "intent": {
                        "schema_version": "1.0",
                        "resources": [
                            {
                                "resource": "catalog:alpha",
                                "operation": "research",
                                "access": "write",
                                "exclusivity": "exclusive",
                            }
                        ],
                    },
                    "task_payload": {"instruction": f"conflicting instruction {index}"},
                    "released_artifacts": [],
                    "idempotency_key": f"task-conflict-http-{index}-0001",
                },
            )
            assert response.status_code == 202, response.text
            accepted.append(response.json())
    assert accepted[0]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert accepted[1]["fact"] == DeliveryFact.CONFLICT_PENDING.value

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        hidden = await _request(
            client,
            first_key,
            first_admin,
            "GET",
            "/v1/task-conflicts",
        )
        assert hidden.status_code == 200
        assert hidden.json() == {"conflicts": []}
        listed = await _request(
            client,
            subordinate_key,
            subordinate,
            "GET",
            "/v1/task-conflicts",
        )
        assert listed.status_code == 200, listed.text
        assert listed.headers["cache-control"] == "no-store"
        conflict = listed.json()["conflicts"][0]
        member_ids = [member["event_id"] for member in conflict["members"]]
        strict_decision = TaskConflictAdjudication(
            conflict_id=conflict["conflict_id"],
            expected_revision=conflict["revision"],
            expected_policy_revision=conflict["policy_revision"],
            expected_domain_revocation_epoch=conflict["domain_revocation_epoch"],
            expected_recipient_credential_epoch=conflict["recipient_credential_epoch"],
            expected_member_event_ids=frozenset(member_ids),
            release_event_ids=frozenset({member_ids[0]}),
            reject_event_ids=frozenset({member_ids[1]}),
            reason_code="http_owner_partition",
        )
        path = f"/v1/task-conflicts/{conflict['conflict_id']}/adjudicate"
        malformed = strict_decision.model_dump(mode="json")
        malformed["expected_revision"] = str(malformed["expected_revision"])
        strict_rejection = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            path,
            {"decision": malformed},
        )
        assert strict_rejection.status_code == 422
        wrong_owner = await _request(
            client,
            first_key,
            first_admin,
            "POST",
            path,
            {"decision": strict_decision.model_dump(mode="json")},
        )
        assert wrong_owner.status_code == 404
        resolved = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            path,
            {"decision": strict_decision.model_dump(mode="json")},
        )
        assert resolved.status_code == 200, resolved.text
        assert resolved.headers["cache-control"] == "no-store"
        assert resolved.json()["conflict"]["data_access_authorized"] is False
        replay = await _request(
            client,
            subordinate_key,
            subordinate,
            "POST",
            path,
            {"decision": strict_decision.model_dump(mode="json")},
        )
        assert replay.status_code == 409


@pytest.mark.anyio
async def test_relationship_policy_exception_activation_and_admin_override_are_exact(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    subordinate, _subordinate_key = identity_factory(kind="pi", binding_assurance="os_bound")
    administrator, administrator_key = identity_factory(
        kind="claude",
        binding_assurance="os_bound",
    )
    intruder, intruder_key = identity_factory(
        kind="codex",
        binding_assurance="os_bound",
    )
    core = _core(store, tmp_path, domain=owner.domain_id)
    relationship = Relationship(
        domain_id=owner.domain_id,
        administrator_harness_id=owner.harness_id,
        subordinate_harness_id=subordinate.harness_id,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    proposal_expires_at = datetime.now(UTC) + timedelta(minutes=5)
    resource = f"relationship:{relationship.relationship_id}"
    _allow(core, owner, "organization.relationship.propose", resource)
    unrelated_relationship = Relationship(
        domain_id=owner.domain_id,
        administrator_harness_id=owner.harness_id,
        subordinate_harness_id=intruder.harness_id,
        expires_at=datetime.now(UTC) + timedelta(hours=1),
    )
    unrelated_resource = f"relationship:{unrelated_relationship.relationship_id}"
    _allow(core, owner, "organization.relationship.propose", unrelated_resource)
    _allow(
        core,
        administrator,
        "organization.relationship.policy_exception.record",
        resource,
    )
    _allow(core, administrator, "organization.relationship.admin_revoke", resource)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        response = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/relationships",
            {
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            },
        )
        assert response.status_code == 201, response.text
        proposal = response.json()["proposal"]
        unrelated_response = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/relationships",
            {
                "relationship": unrelated_relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            },
        )
        assert unrelated_response.status_code == 201, unrelated_response.text
        unrelated_proposal = unrelated_response.json()["proposal"]

        exception = RelationshipPolicyException(
            domain_id=owner.domain_id,
            relationship_id=relationship.relationship_id,
            relationship_revision=proposal["revision"],
            expected_lifecycle_revision=proposal["lifecycle_revision"],
            relationship_transaction_digest=proposal["transaction_digest"],
            reason="exact domain governance exception",
            expires_at=datetime.now(UTC) + timedelta(minutes=4),
        )
        _, exception_request = core.relationships.policy_exception_binding(exception)
        command = _command(
            key=administrator_key,
            actor=administrator,
            action="organization.relationship.policy_exception.record",
            resource=resource,
            request=exception_request,
            revision=proposal["lifecycle_revision"],
            reason=exception.reason,
        )
        malformed_exception = exception.model_dump(mode="json")
        malformed_exception["relationship_revision"] = str(
            malformed_exception["relationship_revision"]
        )
        malformed_exception_response = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions",
            {
                "exception": malformed_exception,
                "command": command.model_dump(mode="json"),
            },
        )
        assert malformed_exception_response.status_code == 422
        assert malformed_exception_response.headers["cache-control"] == "no-store"

        malformed_command = command.model_dump(mode="json")
        malformed_command["expected_entity_revision"] = str(
            malformed_command["expected_entity_revision"]
        )
        malformed_command_response = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions",
            {
                "exception": exception.model_dump(mode="json"),
                "command": malformed_command,
            },
        )
        assert malformed_command_response.status_code == 422
        assert malformed_command_response.headers["cache-control"] == "no-store"

        hidden_path = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            "/v1/relationships/not-the-target/policy-exceptions",
            {
                "exception": exception.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        assert hidden_path.status_code == 404
        assert hidden_path.headers["cache-control"] == "no-store"

        hidden_existing_record = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions",
            {
                "exception": exception.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        absent_exception = exception.model_copy(
            update={"relationship_id": "nonexistent-relationship"}
        )
        hidden_missing_record = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            "/v1/relationships/nonexistent-relationship/policy-exceptions",
            {
                "exception": absent_exception.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        assert hidden_existing_record.status_code == hidden_missing_record.status_code == 404
        assert hidden_existing_record.json() == hidden_missing_record.json()
        assert hidden_existing_record.headers["cache-control"] == "no-store"
        assert hidden_missing_record.headers["cache-control"] == "no-store"

        recorded = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions",
            {
                "exception": exception.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        assert recorded.status_code == 201, recorded.text
        assert recorded.headers["cache-control"] == "no-store"
        assert (
            recorded.json()["policy_exception"]["policy_exception_id"]
            == exception.policy_exception_id
        )

        signer_unrelated_probe = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            f"/v1/relationships/{unrelated_relationship.relationship_id}/policy-exceptions/activate",
            {
                "policy_exception_id": exception.policy_exception_id,
                "expected_transaction_digest": unrelated_proposal["transaction_digest"],
                "expected_relationship_revision": unrelated_proposal["revision"],
                "expected_lifecycle_revision": unrelated_proposal["lifecycle_revision"],
            },
        )
        signer_missing_probe = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            "/v1/relationships/nonexistent-relationship/policy-exceptions/activate",
            {
                "policy_exception_id": exception.policy_exception_id,
                "expected_transaction_digest": unrelated_proposal["transaction_digest"],
                "expected_relationship_revision": unrelated_proposal["revision"],
                "expected_lifecycle_revision": unrelated_proposal["lifecycle_revision"],
            },
        )
        assert signer_unrelated_probe.status_code == signer_missing_probe.status_code == 404
        assert signer_unrelated_probe.json() == signer_missing_probe.json()

        activation_body = {
            "policy_exception_id": exception.policy_exception_id,
            "expected_transaction_digest": "0" * 64,
            "expected_relationship_revision": 999,
            "expected_lifecycle_revision": 999,
        }
        hidden_existing_activation = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions/activate",
            activation_body,
        )
        hidden_missing_activation = await _request(
            client,
            intruder_key,
            intruder,
            "POST",
            "/v1/relationships/nonexistent-relationship/policy-exceptions/activate",
            activation_body,
        )
        assert hidden_existing_activation.status_code == hidden_missing_activation.status_code == 404
        assert hidden_existing_activation.json() == hidden_missing_activation.json()
        assert hidden_existing_activation.headers["cache-control"] == "no-store"
        assert hidden_missing_activation.headers["cache-control"] == "no-store"

        malformed_activation = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions/activate",
            {
                "policy_exception_id": exception.policy_exception_id,
                "expected_transaction_digest": proposal["transaction_digest"],
                "expected_relationship_revision": str(proposal["revision"]),
                "expected_lifecycle_revision": proposal["lifecycle_revision"],
            },
        )
        assert malformed_activation.status_code == 422
        assert malformed_activation.headers["cache-control"] == "no-store"

        activated = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/policy-exceptions/activate",
            {
                "policy_exception_id": exception.policy_exception_id,
                "expected_transaction_digest": proposal["transaction_digest"],
                "expected_relationship_revision": proposal["revision"],
                "expected_lifecycle_revision": proposal["lifecycle_revision"],
            },
        )
        assert activated.status_code == 200, activated.text
        assert activated.headers["cache-control"] == "no-store"
        assert activated.json()["relationship"]["activation_basis"] == "domain_policy_exception"

        reason = "domain administrator revoked exact active edge"
        _, revoke_request = core.relationships.revocation_binding(
            relationship.relationship_id,
            expected_relationship_revision=proposal["revision"],
            expected_lifecycle_revision=2,
            reason=reason,
        )
        revoke_command = _command(
            key=administrator_key,
            actor=administrator,
            action="organization.relationship.admin_revoke",
            resource=resource,
            request=revoke_request,
            revision=2,
            reason=reason,
        )
        revoked = await _request(
            client,
            administrator_key,
            administrator,
            "POST",
            f"/v1/relationships/{relationship.relationship_id}/revoke",
            {"command": revoke_command.model_dump(mode="json")},
        )
        assert revoked.status_code == 200, revoked.text
        assert revoked.headers["cache-control"] == "no-store"


@pytest.mark.anyio
async def test_room_presence_directory_and_content_free_operator_http(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    member, member_key = identity_factory(kind="pi", binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=owner.domain_id)
    _allow(core, owner, "room.create", "room:new")

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        created = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/rooms",
            {"classification": "C1", "persistent": True},
        )
        assert created.status_code == 201, created.text
        room_id = created.json()["room_id"]
        meeting = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/meetings",
            {
                "classification": "C1",
                "expires_at": (datetime.now(UTC) + timedelta(minutes=30)).isoformat(),
            },
        )
        assert meeting.status_code == 201, meeting.text
        meeting_row = store.fetch_one(
            "SELECT policy_json,expires_at FROM rooms WHERE room_id=?",
            (meeting.json()["room_id"],),
        )
        assert json.loads(meeting_row["policy_json"])["persistent"] is False
        assert meeting_row["expires_at"] is not None
        for action in ("room.action", "message.send"):
            _allow(core, owner, action, room_id)
        _allow(core, member, "room.read", room_id)

        added = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/rooms/{room_id}/members",
            {"harness_id": member.harness_id, "role": "member"},
        )
        assert added.status_code == 201, added.text
        assert added.json()["control_sequence"] == 2

        described = await _request(client, member_key, member, "GET", f"/v1/rooms/{room_id}")
        assert described.status_code == 200
        assert described.json()["member_count"] == 2
        assert "members" not in described.json()

        recipients = sorted((owner.harness_id, member.harness_id))
        missing_epoch = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/messages",
            {
                "recipients": recipients,
                "payload": {"text": "missing epoch"},
                "idempotency_key": "room-http-message-missing-0001",
                "classification": "C1",
                "room_id": room_id,
            },
        )
        assert missing_epoch.status_code == 404
        stale = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/rooms/{room_id}/messages",
            {
                "recipients": recipients,
                "payload": {"text": "stale"},
                "idempotency_key": "room-http-message-stale-0001",
                "classification": "C1",
                "expected_control_sequence": 1,
            },
        )
        assert stale.status_code == 404
        sent = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/rooms/{room_id}/messages",
            {
                "recipients": recipients,
                "payload": {"text": "current epoch"},
                "idempotency_key": "room-http-message-current-0001",
                "classification": "C1",
                "expected_control_sequence": 2,
            },
        )
        assert sent.status_code == 202, sent.text
        stored_room_event = store.fetch_one(
            "SELECT envelope_json FROM events WHERE event_id=?", (sent.json()["event_id"],)
        )
        room_envelope = json.loads(stored_room_event["envelope_json"])
        assert room_envelope["room_control_sequence"] == 2
        assert room_envelope["room_application_epoch"] == 1
        assert room_envelope["room_file_key_epoch"] == 1
        persisted_room_event = json.loads(
            store.fetch_one(
                "SELECT envelope_json FROM events WHERE event_id=?",
                (sent.json()["event_id"],),
            )["envelope_json"]
        )
        assert persisted_room_event["room_control_sequence"] == 2
        assert persisted_room_event["room_application_epoch"] == 1
        assert persisted_room_event["room_file_key_epoch"] == 1
        assert persisted_room_event["room_mls_epoch"] == 0

        _allow(core, owner, "presence.update", owner.harness_id)
        _allow(core, member, "presence.read", owner.harness_id)
        lease = PresenceLease(
            harness_id=owner.harness_id,
            domain_id=owner.domain_id,
            issued_at=datetime.now(UTC),
            expires_at=datetime.now(UTC) + timedelta(seconds=60),
            capability_hints=frozenset({"background"}),
        )
        lease_signature = owner_key.sign("agentnet.presence.lease.v1", lease.model_dump(mode="json"))
        presence = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/presence",
            {"lease": lease.model_dump(mode="json"), "signature": lease_signature},
        )
        assert presence.status_code == 200, presence.text
        state = await _request(
            client,
            member_key,
            member,
            "GET",
            f"/v1/presence/{owner.harness_id}",
        )
        assert state.json()["state"] == "live"

        record = DirectoryRecord(
            record_id="agent:owned-visible",
            record_type="agent",
            domain_id=owner.domain_id,
            epoch=1,
            attributes={"display_name": "Visible agent"},
            visible_to_principal_ids=(member.principal_id,),
            expires_at=int(time.time()) + 600,
        )
        directory_resource, _directory_context = core.directory.publication_binding(record)
        _allow(core, owner, "directory.publish", directory_resource)
        published = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/directory",
            {"record": record.model_dump(mode="json")},
        )
        assert published.status_code == 201, published.text
        assert published.json()["record_id"] == record.record_id
        _allow(core, member, "directory.read", record.record_id)
        _allow(core, member, "directory.list", "directory:self")
        fetched_record = await _request(
            client,
            member_key,
            member,
            "GET",
            f"/v1/directory/{record.record_id}",
        )
        assert fetched_record.status_code == 200
        listed = await _request(
            client,
            member_key,
            member,
            "GET",
            "/v1/directory",
            query="types=agent&limit=10",
        )
        assert [item["record_id"] for item in listed.json()["records"]] == [record.record_id]

        _allow(core, owner, "operator.status.read", "operator:self")
        operator = await _request(client, owner_key, owner, "GET", "/v1/operator/status")
        assert operator.status_code == 200
        assert set(operator.json()) == {
            "status",
            "profile",
            "acceptance_fact",
            "storage_ready",
            "artifacts_ready",
            "audit_valid",
            "deployment_binding_ready",
            "a2a_ready",
            "scanner_trust_ready",
            "telemetry",
            "admission_controls",
            "versioning",
        }
        assert owner.harness_id not in operator.text
        assert operator.json()["telemetry"]["available"] is True
        assert operator.json()["admission_controls"]["available"] is True
        assert operator.json()["admission_controls"]["open_breakers"] == 0
        assert operator.json()["admission_controls"]["active_loop_fences"] >= 1
        assert operator.json()["versioning"] == {
            "available": True,
            "active_rollouts": 0,
            "queued_unsupported_events": 0,
            "protocol_current": "1.1",
            "protocol_previous": "1.0",
            "rollout_phase": "bootstrap",
        }

        removed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/rooms/{room_id}/members/remove",
            {"harness_id": member.harness_id},
        )
        assert removed.status_code == 200
        hidden_after_remove = await _request(client, member_key, member, "GET", f"/v1/rooms/{room_id}")
        assert hidden_after_remove.status_code == 404
        room_policy_rows = store.fetch_all(
            """SELECT action,context_json FROM policy_decisions
                 WHERE action LIKE 'room.%'"""
        )
        room_policy_actions = {row["action"] for row in room_policy_rows}
        assert {"room.create", "room.action", "room.read"} <= room_policy_actions
        assert room_policy_actions.isdisjoint({"room.member.add", "room.member.remove"})
        room_action_operations = {
            json.loads(row["context_json"]).get("request", {}).get("operation")
            for row in room_policy_rows
            if row["action"] == "room.action"
        }
        assert {"member.add", "member.remove", "message.send"} <= room_action_operations


@pytest.mark.anyio
async def test_protected_rollout_http_replays_mailbox_and_survives_restart_with_downgrade_fence(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    recipient, _recipient_key = identity_factory(kind="pi", binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=owner.domain_id)
    clock = [int(time.time())]
    core.versioning.clock = lambda: clock[0]
    peer = "upgrade-peer.example"
    queued_event = new_event(
        domain_id=owner.domain_id,
        actor=owner,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"kind": "queued-before-upgrade"},
        idempotency_key="version-replay-envelope-0001",
        recipients=(recipient.harness_id,),
        retention_delete_at=datetime.now(UTC) + timedelta(days=1),
    )
    requirement = CompatibilityRequirement(
        event_type="mailbox.future",
        protocol_version="1.2",
        schema_profile=core.versioning.schema_profile,
        schema_hash=core.versioning.schema_hash,
        required_features=frozenset(),
    )
    assert core.quarantine_unsupported_event(
        peer_namespace=peer,
        event=queued_event.model_dump(mode="json"),
        requirement=requirement,
    )["queued"] is True

    begin_path = "/v1/operator/version-rollouts"
    begin_body = {
        "from_protocol_version": "1.1",
        "to_protocol_version": "1.2",
        "from_schema_version": CURRENT_SCHEMA_VERSION,
        "to_schema_version": CURRENT_SCHEMA_VERSION,
        "compatibility_deadline": clock[0] + 10,
    }
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        denied = await _request(client, owner_key, owner, "POST", begin_path, begin_body)
        assert denied.status_code == 404
        _allow(core, owner, "operator.version.rollout", f"operator-domain:{owner.domain_id}")
        begun = await _request(client, owner_key, owner, "POST", begin_path, begin_body)
        assert begun.status_code == 201, begun.text
        first_rollout = begun.json()["rollout_id"]

        _allow(core, owner, "operator.version.replay", f"version-replay:{peer}")
        replayed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/operator/version-replay",
            {"peer_namespace": peer, "limit": 10},
        )
        assert replayed.status_code == 503
        _allow(core, owner, "operator.version.rollout", f"version-rollout:{first_rollout}")
        migrated = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/operator/version-rollouts/{first_rollout}/advance",
            {
                "expected_phase": "expanded",
                "target_phase": "migrated_backfilled",
                "verification_digest": None,
            },
        )
        assert migrated.status_code == 200, migrated.text
        verification_digest = migrated.json()["verification_digest"]
        replayed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/operator/version-replay",
            {"peer_namespace": peer, "limit": 10},
        )
        assert replayed.status_code == 503
        advanced = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/operator/version-rollouts/{first_rollout}/advance",
            {
                "expected_phase": "migrated_backfilled",
                "target_phase": "verified",
                "verification_digest": verification_digest,
            },
        )
        assert advanced.status_code == 200, advanced.text
        core.versioning = VersioningService(
            store,
            protocol_window=VersionWindow(current="1.2", previous="1.1"),
            host_domain_id=owner.domain_id,
            schema_profile=core.versioning.schema_profile,
            schema_hash=core.versioning.schema_hash,
            clock=lambda: clock[0],
        )
        replayed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/operator/version-replay",
            {"peer_namespace": peer, "limit": 10},
        )
        assert replayed.status_code == 200, replayed.text
        assert replayed.json() == {"replayed": 1, "still_queued": 0}
        assert core.mailboxes.reconcile(recipient.harness_id)[0]["payload"] == {
            "kind": "queued-before-upgrade"
        }
        clock[0] = begin_body["compatibility_deadline"] + 1
        contracted = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/operator/version-rollouts/{first_rollout}/advance",
            {
                "expected_phase": "verified",
                "target_phase": "contracted",
                "verification_digest": verification_digest,
            },
        )
        assert contracted.status_code == 200, contracted.text
        assert contracted.json()["phase"] == "contracted"

    with pytest.raises(GateBlocked, match="incompatible with the durable rollout phase"):
        CommunicationCore(core.config, store)
    upgraded = VersioningService(
        store,
        protocol_window=VersionWindow(current="1.2", previous="1.1"),
        host_domain_id=owner.domain_id,
        schema_profile=core.versioning.schema_profile,
        schema_hash=core.versioning.schema_hash,
    )
    assert upgraded.content_free_status()["rollout_phase"] == "contracted"


@pytest.mark.anyio
async def test_room_governance_transfer_http_is_signed_fenced_and_target_bound(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    target, target_key = identity_factory(domain="partner.example", binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=owner.domain_id)
    _allow(core, owner, "room.create", "room:new")
    app = create_app(core)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        created = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/rooms",
            {"classification": "C1", "persistent": True},
        )
        assert created.status_code == 201, created.text
        room_id = created.json()["room_id"]
        snapshot = RoomTransferSnapshot(
            room_id=room_id,
            cutoff_control_sequence=1,
            cutoff_event_sequence=0,
            owner_epoch=1,
            application_epoch=1,
            mls_epoch=0,
            file_key_epoch=1,
            state_root=canonical_digest({"root": "state"}),
            history_root=canonical_digest({"root": "history"}),
            recipient_rows_digest=canonical_digest({"root": "recipients"}),
            pending_effects_digest=canonical_digest({"root": "effects"}),
            artifact_manifests_digest=canonical_digest({"root": "artifacts"}),
            retention_legal_hold_digest=canonical_digest({"root": "retention"}),
            guest_roster_digest=canonical_digest({"root": "guests"}),
            audit_custody_digest=canonical_digest({"root": "audit"}),
            destination_key_id="destination-key-http-1",
            reconciliation_closed=True,
        )
        now = int(time.time())
        proposal = SourceTransferProposal(
            transfer_id=str(uuid4()),
            room_id=room_id,
            source_domain_id=owner.domain_id,
            target_domain_id=target.domain_id,
            source_harness_id=owner.harness_id,
            source_credential_id=owner.credential_id,
            source_owner_epoch=1,
            cutoff_control_sequence=1,
            cutoff_event_sequence=0,
            application_epoch=1,
            mls_epoch=0,
            file_key_epoch=1,
            destination_key_id=snapshot.destination_key_id,
            snapshot_digest=snapshot.digest,
            issued_at=now - 1,
            expires_at=now + 240,
            nonce="source-http-transfer-nonce-with-entropy-0001",
        )
        _allow(core, owner, "room.transfer.propose", room_id)
        proposed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/rooms/{room_id}/transfers",
            {
                "proposal": proposal.model_dump(mode="json"),
                "snapshot": snapshot.model_dump(mode="json"),
                "signature": owner_key.sign("agentnet.room.control.v1", proposal.signed_fields()),
            },
        )
        assert proposed.status_code == 202, proposed.text
        acceptance = TargetTransferAcceptance(
            transfer_id=proposal.transfer_id,
            room_id=room_id,
            source_domain_id=owner.domain_id,
            target_domain_id=target.domain_id,
            source_proposal_digest=proposal.digest,
            snapshot_digest=snapshot.digest,
            source_owner_epoch=1,
            cutoff_control_sequence=1,
            cutoff_event_sequence=0,
            destination_owner_epoch=2,
            destination_application_epoch=2,
            destination_mls_epoch=0,
            destination_file_key_epoch=2,
            destination_key_id=snapshot.destination_key_id,
            target_harness_id=target.harness_id,
            target_credential_id=target.credential_id,
            objects_verified=True,
            reconciliation_closed=True,
            issued_at=now - 1,
            expires_at=now + 240,
            nonce="target-http-acceptance-nonce-with-entropy-001",
        )
        transfer_resource = f"room-transfer:{proposal.transfer_id}"
        _allow(core, target, "room.transfer.accept", transfer_resource)
        accepted = await _request(
            client,
            target_key,
            target,
            "POST",
            f"/v1/room-transfers/{proposal.transfer_id}/accept",
            {
                "acceptance": acceptance.model_dump(mode="json"),
                "signature": target_key.sign("agentnet.room.control.v1", acceptance.signed_fields()),
            },
            audience_domain=owner.domain_id,
        )
        assert accepted.status_code == 200, accepted.text
        _allow(core, target, "room.transfer.commit", transfer_resource)
        committed = await _request(
            client,
            target_key,
            target,
            "POST",
            f"/v1/room-transfers/{proposal.transfer_id}/commit",
            audience_domain=owner.domain_id,
        )
        assert committed.status_code == 200, committed.text
        room = store.fetch_one("SELECT * FROM rooms WHERE room_id=?", (room_id,))
        assert (room["state"], room["owner_domain_id"], room["owner_epoch"]) == (
            "active",
            target.domain_id,
            2,
        )


@pytest.mark.anyio
async def test_artifact_and_effect_http_lifecycles_mint_server_side_decisions(
    store,
    identity_factory,
    workload_factory,
    tmp_path: Path,
) -> None:
    actor, actor_key = identity_factory(binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=actor.domain_id)
    scanner_key = P256KeyPair.generate()
    core.artifacts.trusted_scanner_keys["scanner:http:1"] = scanner_key.public_pem
    content = b"safe authenticated artifact"
    digest = hashlib.sha256(content).hexdigest()
    parent_content = b"authenticated human source for derived artifact"
    parent_digest = hashlib.sha256(parent_content).hexdigest()
    parent_resource = "provenance:artifact:http-artifact-parent"
    _allow(core, actor, "provenance.origin.register", parent_resource)
    _allow(core, actor, "artifact.upload.reserve", "artifact:new")
    workload_transport: list[AuthenticatedSPIFFETransport] = []
    product_app = create_app(core)

    async def transport_bound_app(scope, receive, send):
        forwarded_scope = dict(scope)
        if scope["type"] == "http" and workload_transport:
            forwarded_scope["agentnet.workload_transport"] = workload_transport[0]
        await product_app(forwarded_scope, receive, send)

    def bind_workload_transport(workload) -> None:
        row = store.fetch_one(
            "SELECT * FROM workload_registrations WHERE registration_id=?",
            (workload.workload_registration_id,),
        )
        workload_transport[:] = [
            core.workloads.spiffe.transport_authority.bind_verified_peer(
                {
                    "schema_version": "1.0",
                    "spiffe_id": row["spiffe_id"],
                    "trust_domain": workload.domain_id,
                    "workload_role": workload.workload_role,
                    "certificate_serial": row["certificate_serial"],
                    "process_id": workload.workload_process_id,
                    "process_start_time": workload.workload_process_start_time,
                    "session_id": workload.workload_session_id,
                }
            )
        ]

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=transport_bound_app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        observed_at = datetime.now(UTC).replace(microsecond=0)
        parent_registration = OriginRegistration(
            object_type=ProvenanceObjectType.ARTIFACT,
            object_id="http-artifact-parent",
            domain_id=actor.domain_id,
            origin=ProvenanceOrigin(
                kind=OriginKind.HUMAN_INPUT,
                source_id="http-artifact-parent-input",
                source_digest=parent_digest,
                principal_id=actor.principal_id,
                harness_id=actor.harness_id,
                observed_at=observed_at,
            ),
            classification=Classification.C1_INTERNAL,
            allowed_sinks=SinkSet(sinks=()),
            policy_revision=1,
            recorded_at=observed_at,
        )
        parent_response = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/origins",
            {"registration": parent_registration.model_dump(mode="json")},
        )
        assert parent_response.status_code == 201, parent_response.text
        parent = parent_response.json()["provenance"]
        reserved = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/artifacts/reservations",
            {
                "idempotency_key": "artifact-http-reservation-0001",
                "expected_digest": digest,
                "expected_size": len(content),
                "media_type": "text/plain",
                "classification": "C1",
                "required_attachment": True,
            },
        )
        assert reserved.status_code == 201, reserved.text
        reservation = reserved.json()
        reservation_id = reservation["reservation_id"]
        _allow(core, actor, "artifact.upload.bytes", reservation_id)
        uploaded = await _raw_request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/bytes",
            content,
            content_type="application/octet-stream",
        )
        assert uploaded.status_code == 200, uploaded.text
        object_version = uploaded.json()["version"]
        parent_reference = {
            "schema_version": "1.0",
            "object_type": parent["object_type"],
            "object_id": parent["object_id"],
            "version": parent["version"],
            "domain_id": parent["domain_id"],
            "provenance_digest": parent["provenance_digest"],
            "content_digest": parent["transformations"]["output_digest"],
            "classification": parent["classification"],
            "allowed_sinks": parent["allowed_sinks"],
            "policy_revision": parent["policy_revision"],
            "review_state": parent["review_state"],
            "scan_state": parent["scan_state"],
            "tainted": parent["tainted"],
            "authority_effect": "none",
        }
        transform = TransformationStep(
            kind=TransformationKind.PARSER,
            operation_id="http-derived-artifact-transform-1",
            implementation_id="agentnet.http.derived-artifact-test",
            implementation_version="1",
            executor_harness_id=actor.harness_id,
            input_digests=(parent_digest,),
            output_digest=digest,
            started_at=observed_at,
            completed_at=observed_at,
        )
        derivation = ArtifactDerivationV1.model_validate_json(
            canonical_json(
                {
                    "parent_references": [parent_reference],
                    "transformations": [transform.model_dump(mode="json")],
                }
            ),
            strict=True,
        )
        _allow(core, actor, "artifact.manifest.promote", reservation_id)
        promote_body = {
            "object_version": object_version,
            "provenance": {"origin": "http-derived-test"},
            "derivation": derivation.model_dump(mode="json"),
        }
        promoted = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/promote",
            promote_body,
        )
        assert promoted.status_code == 201, promoted.text
        promoted_reference = promoted.json()["provenance"]
        artifact_id = promoted.json()["artifact_id"]
        _allow(core, actor, "provenance.read", f"provenance:artifact:{artifact_id}")
        exact_provenance_response = await _request(
            client,
            actor_key,
            actor,
            "GET",
            f"/v1/provenance/artifact/{artifact_id}/{promoted_reference['version']}",
        )
        assert exact_provenance_response.status_code == 200, exact_provenance_response.text
        promoted_provenance = exact_provenance_response.json()["provenance"]
        assert promoted_provenance["origin"]["kind"] == OriginKind.DERIVED.value
        assert promoted_provenance["parent_digests"]["digests"] == [
            parent["provenance_digest"]
        ]
        assert promoted_provenance["transformations"]["steps"] == [
            transform.model_dump(mode="json")
        ]
        assert promoted_provenance["tainted"] is True
        duplicate_promotion = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/reservations/{reservation_id}/promote",
            promote_body,
        )
        assert duplicate_promotion.status_code == 200, duplicate_promotion.text
        assert duplicate_promotion.json()["duplicate"] is True
        assert duplicate_promotion.json()["provenance"] == promoted_reference
        scan_fields = {
            "artifact_id": artifact_id,
            "classification": "C1",
            "ciphertext_digest": uploaded.json()["ciphertext_digest"],
            "expires_at": int(time.time()) + 240,
            "issued_at": int(time.time()),
            "object_key": reservation["object_key"],
            "object_version": object_version,
            "plaintext_digest": digest,
            "policy_revision": 1,
            "profile_digest": "c" * 64,
            "result": "allow",
            "rules_digest": "a" * 64,
            "scanner_engine": "maintained-http-test-engine",
            "scanner_id": "scanner:http",
            "scanner_key_epoch": 1,
            "scanner_version": "1",
        }
        attestation = scan_fields | {
            "signature": scanner_key.sign("agentnet.artifact.attestation.v1", scan_fields)
        }
        _allow(core, actor, "artifact.scan.record", artifact_id)
        scanned = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/scan",
            {"attestation": attestation},
        )
        assert scanned.status_code == 200, scanned.text
        _allow(core, actor, "artifact.release", artifact_id)
        released = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/release",
        )
        assert released.status_code == 200, released.text
        _allow(core, actor, "artifact.download", artifact_id)
        capability = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/download-capabilities",
            {"ttl_seconds": 60},
        )
        assert capability.status_code == 200, capability.text
        downloaded = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/artifacts/download",
            {"token": capability.json()["capability"]},
        )
        assert downloaded.content == content

        released_binding = core.artifacts.resolve_released_binding(artifact_id)
        _allow(core, actor, "message.send", "direct")
        message = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/messages",
            {
                "recipients": [actor.harness_id],
                "payload": {"task": "prepare effect"},
                "idempotency_key": "effect-parent-message-0001",
                "classification": "C2",
                "released_artifacts": [released_binding.model_dump(mode="json")],
            },
        )
        assert message.status_code == 202, message.text
        event_id = message.json()["event_id"]
        stored_event = json.loads(
            store.fetch_one(
                "SELECT envelope_json FROM events WHERE event_id=?",
                (event_id,),
            )["envelope_json"]
        )
        assert stored_event["released_artifacts"] == [released_binding.model_dump(mode="json")]
        assert "payload" not in stored_event

        _allow(core, actor, "artifact.lifecycle.read", artifact_id)
        lifecycle = await _request(
            client,
            actor_key,
            actor,
            "GET",
            f"/v1/artifacts/{artifact_id}/lifecycle",
        )
        assert lifecycle.status_code == 200, lifecycle.text
        assert lifecycle.json()["lifecycle_revision"] == 1
        _allow(core, actor, "artifact.legal_hold.set", artifact_id)
        held = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/legal-hold",
            {"expected_revision": 1, "reason": "HTTP preservation test"},
        )
        assert held.status_code == 200, held.text
        assert held.json()["legal_hold"] is True
        _allow(core, actor, "artifact.legal_hold.clear", artifact_id)
        cleared = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/legal-hold/clear",
            {"expected_revision": 2, "reason": "HTTP preservation released"},
        )
        assert cleared.status_code == 200, cleared.text
        assert cleared.json()["legal_hold"] is False
        _allow(core, actor, "artifact.delete", artifact_id)
        retained_delete = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/artifacts/{artifact_id}/delete",
            {"expected_revision": 3, "reason": "HTTP retained history test"},
        )
        assert retained_delete.status_code == 409
        assert core.artifacts.resolve_released_binding(artifact_id) == released_binding

        grant = TaskGrant(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            harness_id=actor.harness_id,
            actions=frozenset({"dataset.write"}),
            resources=frozenset({"dataset:alpha"}),
            input_sources=frozenset({f"mailbox:{event_id}"}),
            output_sinks=frozenset({"connector:synthetic"}),
            data_classes=frozenset({Classification.C2_RESTRICTED}),
            max_uses=3,
            expires_at=datetime.now(UTC) + timedelta(minutes=20),
        )
        _allow(core, actor, "authorization.task_grant.issue", f"task-grant:{grant.grant_id}")
        grant_response = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/task-grants",
            {"grant": grant.model_dump(mode="json")},
        )
        assert grant_response.status_code == 201, grant_response.text
        _allow(core, actor, "dataset.write", "dataset:alpha")
        first_effect_request = {"operation": "write", "value": 1}
        effect = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/effects",
            {
                "event_id": event_id,
                "grant_use": {
                    "grant_id": grant.grant_id,
                    "action": "dataset.write",
                    "resource": "dataset:alpha",
                    "input_source": f"mailbox:{event_id}",
                    "output_sink": "connector:synthetic",
                    "data_class": "C2",
                },
                "request": first_effect_request,
            },
        )
        assert effect.status_code == 201, effect.text
        effect_id = effect.json()["effect_id"]
        _allow(core, actor, "effect.status", effect_id)
        status = await _request(client, actor_key, actor, "GET", f"/v1/effects/{effect_id}")
        assert status.status_code == 200
        assert status.json()["state"] == "effect_prepared"
        assert "request" not in status.json()

        executor, executor_key = workload_factory(
            domain=actor.domain_id,
            role="effect_authority",
            recipient_scope=actor.harness_id,
            parent_event_id=event_id,
            task_grant_id=grant.grant_id,
        )
        execution = EffectExecutionEvidence(
            attempt_id=f"http-effect-attempt-{uuid4()}",
            executor_instance_id=f"http-effect-executor-{uuid4()}",
            request_digest=canonical_digest(first_effect_request),
            dispatched_at=int(time.time()),
        )
        start_proof = EffectTransitionProof.create(
            executor_key,
            actor=executor,
            effect_id=effect_id,
            fence=effect.json()["fence"],
            from_state=EffectState.PREPARED,
            to_state=EffectState.EXECUTING,
            evidence=execution,
        )
        start_body = canonical_json(
            {
                "proof": start_proof.model_dump(mode="json"),
                "evidence": execution.model_dump(mode="json"),
            }
        )
        proof_only = await client.post(
            f"/v1/effects/{effect_id}/start",
            content=start_body,
            headers={
                "Content-Type": "application/json",
                "X-AgentNet-Workload-Verified": "true",
                "X-AgentNet-Workload-Registration": executor.workload_registration_id,
            },
        )
        assert proof_only.status_code == 401
        bind_workload_transport(executor)
        started = await client.post(
            f"/v1/effects/{effect_id}/start",
            content=start_body,
            headers={"Content-Type": "application/json"},
        )
        assert started.status_code == 200, started.text
        assert started.json()["state"] == EffectState.EXECUTING.value

        uncertainty = EffectUncertaintyEvidence(
            attempt_id=execution.attempt_id,
            reason="commit_response_lost",
            observation_digest=canonical_digest({"transport": "http-test"}),
            observed_at=int(time.time()),
        )
        unknown_proof = EffectTransitionProof.create(
            executor_key,
            actor=executor,
            effect_id=effect_id,
            fence=effect.json()["fence"],
            from_state=EffectState.EXECUTING,
            to_state=EffectState.UNKNOWN,
            evidence=uncertainty,
        )
        unknown = await client.post(
            f"/v1/effects/{effect_id}/unknown",
            content=canonical_json(
                {
                    "proof": unknown_proof.model_dump(mode="json"),
                    "evidence": uncertainty.model_dump(mode="json"),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert unknown.status_code == 200, unknown.text
        assert unknown.json()["state"] == EffectState.UNKNOWN.value

        reconciliation = EffectReconciliationEvidence(
            attempt_id=execution.attempt_id,
            authority_system_id="synthetic-connector",
            query_id=f"query-{uuid4()}",
            query_response_digest=canonical_digest({"committed": True}),
            observed_at=int(time.time()),
            terminal_state=EffectState.SUCCEEDED,
        )
        executor_reconcile_proof = EffectTransitionProof.create(
            executor_key,
            actor=executor,
            effect_id=effect_id,
            fence=effect.json()["fence"],
            from_state=EffectState.UNKNOWN,
            to_state=EffectState.SUCCEEDED,
            evidence=reconciliation,
        )
        executor_reconcile = await client.post(
            f"/v1/effects/{effect_id}/reconcile",
            content=canonical_json(
                {
                    "proof": executor_reconcile_proof.model_dump(mode="json"),
                    "evidence": reconciliation.model_dump(mode="json"),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert executor_reconcile.status_code == 404
        reconciler, reconciler_key = workload_factory(
            domain=actor.domain_id,
            role="effect_reconciler",
            workload_id=f"effect-system:{reconciliation.authority_system_id}",
            recipient_scope=actor.harness_id,
            parent_event_id=event_id,
            task_grant_id=grant.grant_id,
        )
        bind_workload_transport(reconciler)
        reconcile_proof = EffectTransitionProof.create(
            reconciler_key,
            actor=reconciler,
            effect_id=effect_id,
            fence=effect.json()["fence"],
            from_state=EffectState.UNKNOWN,
            to_state=EffectState.SUCCEEDED,
            evidence=reconciliation,
        )
        reconciled = await client.post(
            f"/v1/effects/{effect_id}/reconcile",
            content=canonical_json(
                {
                    "proof": reconcile_proof.model_dump(mode="json"),
                    "evidence": reconciliation.model_dump(mode="json"),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert reconciled.status_code == 200, reconciled.text
        assert reconciled.json()["state"] == EffectState.SUCCEEDED.value

        second_effect_request = {"operation": "write", "value": 2}
        bind_workload_transport(executor)
        second_effect = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/effects",
            {
                "event_id": event_id,
                "grant_use": {
                    "grant_id": grant.grant_id,
                    "action": "dataset.write",
                    "resource": "dataset:alpha",
                    "input_source": f"mailbox:{event_id}",
                    "output_sink": "connector:synthetic",
                    "data_class": "C2",
                },
                "request": second_effect_request,
            },
        )
        assert second_effect.status_code == 201, second_effect.text
        second_id = second_effect.json()["effect_id"]
        second_execution = EffectExecutionEvidence(
            attempt_id=f"http-effect-attempt-{uuid4()}",
            executor_instance_id=execution.executor_instance_id,
            request_digest=canonical_digest(second_effect_request),
            dispatched_at=int(time.time()),
        )
        second_start_proof = EffectTransitionProof.create(
            executor_key,
            actor=executor,
            effect_id=second_id,
            fence=second_effect.json()["fence"],
            from_state=EffectState.PREPARED,
            to_state=EffectState.EXECUTING,
            evidence=second_execution,
        )
        second_started = await client.post(
            f"/v1/effects/{second_id}/start",
            content=canonical_json(
                {
                    "proof": second_start_proof.model_dump(mode="json"),
                    "evidence": second_execution.model_dump(mode="json"),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert second_started.status_code == 200, second_started.text
        _allow(core, actor, "effect.cancel", second_id)
        unsafe_cancel = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/effects/{second_id}/cancel",
        )
        assert unsafe_cancel.status_code == 409
        terminal = EffectTerminalEvidence(
            attempt_id=second_execution.attempt_id,
            external_receipt_id=f"receipt-{uuid4()}",
            external_receipt_digest=canonical_digest({"receipt": "committed"}),
            observed_at=int(time.time()),
        )
        terminal_proof = EffectTransitionProof.create(
            executor_key,
            actor=executor,
            effect_id=second_id,
            fence=second_effect.json()["fence"],
            from_state=EffectState.EXECUTING,
            to_state=EffectState.SUCCEEDED,
            evidence=terminal,
        )
        terminal_response = await client.post(
            f"/v1/effects/{second_id}/terminal",
            content=canonical_json(
                {
                    "proof": terminal_proof.model_dump(mode="json"),
                    "terminal_state": EffectState.SUCCEEDED.value,
                    "evidence": terminal.model_dump(mode="json"),
                }
            ),
            headers={"Content-Type": "application/json"},
        )
        assert terminal_response.status_code == 200, terminal_response.text
        assert terminal_response.json()["state"] == EffectState.SUCCEEDED.value

        third_effect = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/effects",
            {
                "event_id": event_id,
                "grant_use": {
                    "grant_id": grant.grant_id,
                    "action": "dataset.write",
                    "resource": "dataset:alpha",
                    "input_source": f"mailbox:{event_id}",
                    "output_sink": "connector:synthetic",
                    "data_class": "C2",
                },
                "request": {"operation": "write", "value": 3},
            },
        )
        assert third_effect.status_code == 201, third_effect.text
        third_id = third_effect.json()["effect_id"]
        _allow(core, actor, "effect.cancel", third_id)
        cancelled = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/effects/{third_id}/cancel",
        )
        assert cancelled.status_code == 200, cancelled.text
        assert cancelled.json()["state"] == EffectState.CANCELLED.value
        assert cancelled.json()["duplicate"] is False
        duplicate_cancel = await _request(
            client,
            actor_key,
            actor,
            "POST",
            f"/v1/effects/{third_id}/cancel",
        )
        assert duplicate_cancel.status_code == 200, duplicate_cancel.text
        assert duplicate_cancel.json()["duplicate"] is True


@pytest.mark.anyio
async def test_automation_charter_http_is_strict_threshold_bound_and_workload_authenticated(
    store,
    identity_factory,
    workload_factory,
    execution_grant_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    approver, _approver_harness_key = identity_factory(
        domain=owner.domain_id,
        binding_assurance="hardware_bound",
    )
    event_id = f"automation-http-event-{uuid4()}"
    grant = execution_grant_factory(
        recipient=owner,
        event_id=event_id,
        actions=frozenset({"message.process"}),
        max_uses=4,
    )
    workload, _workload_key = workload_factory(
        domain=owner.domain_id,
        role="recipient_processor",
        parent_event_id=event_id,
        task_grant_id=grant.grant_id,
    )
    approval_key = P256KeyPair.generate()
    trusted = TrustedApprover(
        principal_id=approver.principal_id,
        domain_id=owner.domain_id,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({AUTOMATION_CHARTER_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: trusted},
        verifier_id="automation-http-independent-verifier",
    )
    core = _core(
        store,
        tmp_path,
        domain=owner.domain_id,
        approval_verifier=verifier,
    )
    charter = AutomationCharter(
        domain_id=owner.domain_id,
        accountable_principal_id=owner.principal_id,
        accountable_harness_id=owner.harness_id,
        workload_registration_id=workload.workload_registration_id,
        workload_id=workload.workload_id,
        triggers=frozenset({"mailbox"}),
        actions=frozenset({"message.process"}),
        resources=frozenset({f"event:{event_id}"}),
        output_sinks=frozenset({"receipt"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_runtime_seconds=300,
        max_fanout=2,
        max_spend_micros=500,
        use_limit=2,
        approval_threshold=1,
        expires_at=datetime.now(UTC) + timedelta(minutes=20),
        reason="Process the exact HTTP mailbox trigger unattended",
    )
    resource = f"automation-charter:{charter.charter_id}"
    _allow(core, owner, "automation.charter.propose", resource)
    _allow(core, approver, AUTOMATION_CHARTER_APPROVAL_PURPOSE, resource)

    workload_transport: list[AuthenticatedSPIFFETransport] = []
    product_app = create_app(core)

    async def transport_bound_app(scope, receive, send):
        forwarded_scope = dict(scope)
        if scope["type"] == "http" and workload_transport:
            forwarded_scope["agentnet.workload_transport"] = workload_transport[0]
        await product_app(forwarded_scope, receive, send)

    row = store.fetch_one(
        "SELECT * FROM workload_registrations WHERE registration_id=?",
        (workload.workload_registration_id,),
    )
    exact_workload_transport = core.workloads.spiffe.transport_authority.bind_verified_peer(
        {
            "schema_version": "1.0",
            "spiffe_id": row["spiffe_id"],
            "trust_domain": workload.domain_id,
            "workload_role": workload.workload_role,
            "certificate_serial": row["certificate_serial"],
            "process_id": workload.workload_process_id,
            "process_start_time": workload.workload_process_start_time,
            "session_id": workload.workload_session_id,
        }
    )

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=transport_bound_app, raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        malformed_charter = charter.model_dump(mode="json")
        malformed_charter["max_fanout"] = "2"
        strict = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/automation-charters",
            {"charter": malformed_charter},
        )
        assert strict.status_code == 422

        proposed = await _request(
            client,
            owner_key,
            owner,
            "POST",
            "/v1/automation-charters",
            {"charter": charter.model_dump(mode="json")},
        )
        assert proposed.status_code == 201, proposed.text
        proposal = proposed.json()["charter"]
        assert proposal["state"] == "proposed"
        assert proposed.headers["cache-control"] == "no-store"

        issued_at = int(time.time())
        approval = create_independent_approval_receipt(
            approval_key,
            approver=trusted,
            verifier_id=verifier.verifier_id,
            approval_purpose=AUTOMATION_CHARTER_APPROVAL_PURPOSE,
            canonical_transaction=canonical_json(charter.canonical_transaction()),
            issued_at=issued_at,
            expires_at=issued_at + 120,
        )
        activated = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/automation-charters/{charter.charter_id}/activate",
            {
                "expected_charter_digest": charter.digest,
                "expected_revision": proposal["revision"],
                "approvals": [approval],
            },
        )
        assert activated.status_code == 200, activated.text
        active = activated.json()["charter"]
        assert active["state"] == "active"

        listed = await _request(
            client,
            owner_key,
            owner,
            "GET",
            "/v1/automation-charters",
            query="limit=10",
        )
        assert listed.status_code == 200, listed.text
        assert [item["charter"]["charter_id"] for item in listed.json()["charters"]] == [
            charter.charter_id
        ]

        invocation = AutomationInvocation(
            invocation_id=f"automation-http-invocation-{uuid4()}",
            charter_id=charter.charter_id,
            workload_registration_id=workload.workload_registration_id,
            expected_charter_revision=active["revision"],
            expected_charter_digest=charter.digest,
            trigger="mailbox",
            action="message.process",
            resource=f"event:{event_id}",
            output_sink="receipt",
            data_class=Classification.C1_INTERNAL,
            fanout=1,
            spend_micros=100,
            requested_runtime_seconds=60,
            parent_event_id=event_id,
            task_grant_id=grant.grant_id,
            policy_revision=active["policy_revision"],
        )
        invocation_path = f"/v1/automation-charters/{charter.charter_id}/invocations"
        unauthenticated = await client.post(
            invocation_path,
            content=canonical_json({"invocation": invocation.model_dump(mode="json")}),
            headers={"Content-Type": "application/json"},
        )
        assert unauthenticated.status_code == 401

        workload_transport[:] = [exact_workload_transport]
        reserved = await client.post(
            invocation_path,
            content=canonical_json({"invocation": invocation.model_dump(mode="json")}),
            headers={"Content-Type": "application/json"},
        )
        assert reserved.status_code == 201, reserved.text
        reservation = reserved.json()["reservation"]
        assert reservation["task_grant_still_required"] is True
        assert reservation["data_access_authorized"] is False
        assert reservation["effect_authorized"] is False
        retry = await client.post(
            invocation_path,
            content=canonical_json({"invocation": invocation.model_dump(mode="json")}),
            headers={"Content-Type": "application/json"},
        )
        assert retry.status_code == 200, retry.text
        assert retry.json()["reservation"]["use_id"] == reservation["use_id"]

        completion = AutomationInvocationCompletion(
            invocation_id=invocation.invocation_id,
            charter_id=charter.charter_id,
            workload_registration_id=workload.workload_registration_id,
            expected_intent_digest=invocation.digest,
            terminal_state="committed",
            result_digest=canonical_digest({"receipt": "http-recorded"}),
        )
        terminal = await client.post(
            f"{invocation_path}/{invocation.invocation_id}/terminal",
            content=canonical_json({"completion": completion.model_dump(mode="json")}),
            headers={"Content-Type": "application/json"},
        )
        assert terminal.status_code == 200, terminal.text
        assert terminal.json()["reservation"]["state"] == "committed"

        _allow(core, owner, "automation.charter.revoke", resource)
        revoked = await _request(
            client,
            owner_key,
            owner,
            "POST",
            f"/v1/automation-charters/{charter.charter_id}/revoke",
            {
                "expected_charter_digest": charter.digest,
                "expected_revision": active["revision"],
                "reason": "Owner ended unattended execution",
            },
        )
        assert revoked.status_code == 200, revoked.text
        assert revoked.json()["charter"]["state"] == "revoked"


@pytest.mark.anyio
async def test_provenance_http_registers_tainted_origin_and_enforces_exact_human_binding(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, actor_key = identity_factory(binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=actor.domain_id)
    recorded_at = datetime.now(UTC).replace(microsecond=0)
    registration = OriginRegistration(
        object_type=ProvenanceObjectType.ARTIFACT,
        object_id="http-origin-artifact",
        domain_id=actor.domain_id,
        origin=ProvenanceOrigin(
            kind=OriginKind.HUMAN_INPUT,
            source_id="upload:http-origin-artifact",
            source_digest=hashlib.sha256(b"exact provenance input").hexdigest(),
            principal_id=actor.principal_id,
            harness_id=actor.harness_id,
            observed_at=recorded_at,
        ),
        classification=Classification.C1_INTERNAL,
        allowed_sinks=SinkSet(sinks=("artifact-store",)),
        policy_revision=1,
        recorded_at=recorded_at,
    )
    resource = "provenance:artifact:http-origin-artifact"
    _allow(core, actor, "provenance.origin.register", resource)
    _allow(core, actor, "provenance.read", resource)
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        created = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/origins",
            {"registration": registration.model_dump(mode="json")},
        )
        assert created.status_code == 201, created.text
        assert created.json()["provenance"]["tainted"] is True

        replay = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/origins",
            {"registration": registration.model_dump(mode="json")},
        )
        assert replay.status_code == 201, replay.text
        assert (
            replay.json()["provenance"]["provenance_digest"]
            == created.json()["provenance"]["provenance_digest"]
        )

        listed = await _request(
            client,
            actor_key,
            actor,
            "GET",
            "/v1/provenance/artifact/http-origin-artifact",
        )
        assert listed.status_code == 200, listed.text
        assert [item["version"] for item in listed.json()["versions"]] == [1]

        derived_at = datetime.now(UTC).replace(microsecond=0)
        output_digest = hashlib.sha256(b"exact derived provenance output").hexdigest()
        derivation = ProvenanceDerivation(
            object_type=ProvenanceObjectType.ARTIFACT,
            object_id="http-origin-artifact",
            domain_id=actor.domain_id,
            expected_previous_version=1,
            parent_digests=ParentDigestSet(
                digests=(created.json()["provenance"]["provenance_digest"],)
            ),
            transformations=(
                TransformationStep(
                    kind=TransformationKind.PARSER,
                    operation_id="http-provenance-transform-1",
                    implementation_id="agentnet.http.provenance-test",
                    implementation_version="1",
                    executor_harness_id=actor.harness_id,
                    input_digests=(registration.origin.source_digest,),
                    output_digest=output_digest,
                    started_at=derived_at,
                    completed_at=derived_at,
                ),
            ),
            output_digest=output_digest,
            classification=Classification.C1_INTERNAL,
            allowed_sinks=SinkSet(sinks=("artifact-store",)),
            policy_revision=1,
            recorded_at=derived_at,
        )
        _allow(core, actor, "provenance.derive", resource)
        derived = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/derivations",
            {"derivation": derivation.model_dump(mode="json")},
        )
        assert derived.status_code == 201, derived.text
        assert derived.json()["provenance"]["version"] == 2
        assert derived.json()["provenance"]["parent_digests"]["digests"] == [
            created.json()["provenance"]["provenance_digest"]
        ]
        replayed_derivation = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/derivations",
            {"derivation": derivation.model_dump(mode="json")},
        )
        assert replayed_derivation.status_code == 201
        assert (
            replayed_derivation.json()["provenance"]["provenance_digest"]
            == derived.json()["provenance"]["provenance_digest"]
        )
        exact_version = await _request(
            client,
            actor_key,
            actor,
            "GET",
            "/v1/provenance/artifact/http-origin-artifact/2",
        )
        assert exact_version.status_code == 200, exact_version.text
        assert exact_version.json()["provenance"] == derived.json()["provenance"]

        spoofed_executor = derivation.model_copy(
            update={
                "transformations": (
                    derivation.transformations[0].model_copy(
                        update={"executor_harness_id": "spoofed-transformer"}
                    ),
                )
            }
        )
        denied_executor = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/derivations",
            {"derivation": spoofed_executor.model_dump(mode="json")},
        )
        assert denied_executor.status_code == 404

        spoofed = registration.model_copy(
            update={
                "object_id": "http-spoofed-origin",
                "origin": registration.origin.model_copy(
                    update={"principal_id": "someone-else"}
                ),
            }
        )
        _allow(
            core,
            actor,
            "provenance.origin.register",
            "provenance:artifact:http-spoofed-origin",
        )
        denied = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/origins",
            {"registration": spoofed.model_dump(mode="json")},
        )
        assert denied.status_code == 404

        server_origin = OriginRegistration(
            object_type=ProvenanceObjectType.ARTIFACT,
            object_id="http-server-origin-spoof",
            domain_id=actor.domain_id,
            origin=ProvenanceOrigin(
                kind=OriginKind.ARTIFACT,
                source_id="artifact-reservation:spoofed",
                source_digest=hashlib.sha256(b"spoofed server origin").hexdigest(),
                harness_id=actor.harness_id,
                observed_at=recorded_at,
            ),
            classification=Classification.C1_INTERNAL,
            allowed_sinks=SinkSet(sinks=()),
            policy_revision=1,
            recorded_at=recorded_at,
        )
        _allow(
            core,
            actor,
            "provenance.origin.register",
            "provenance:artifact:http-server-origin-spoof",
        )
        denied_server_origin = await _request(
            client,
            actor_key,
            actor,
            "POST",
            "/v1/provenance/origins",
            {"registration": server_origin.model_dump(mode="json")},
        )
        assert denied_server_origin.status_code == 404


@pytest.mark.anyio
async def test_domain_incident_http_is_signed_revisioned_and_actually_freezes_authority(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, actor_key = identity_factory(binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=actor.domain_id)
    resource = f"operator-domain:{actor.domain_id}"
    _allow(core, actor, "operator.incident.set", resource)
    _allow(core, actor, "operator.incident.read", resource)
    freeze = IncidentModeChange(
        domain_id=actor.domain_id,
        expected_revision=0,
        target_mode=IncidentMode.FREEZE_NEW_AUTHORITY,
        reason="suspected authority issuer compromise",
    )
    _resource, exact = core.incidents.authority_binding(freeze)
    command = _command(
        key=actor_key,
        actor=actor,
        action=core.incidents.ACTION,
        resource=resource,
        request=exact,
        revision=0,
        reason=freeze.reason,
    )
    path = "/v1/operator/incident"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        frozen = await _request(
            client,
            actor_key,
            actor,
            "POST",
            path,
            {
                "change": freeze.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        assert frozen.status_code == 200, frozen.text
        assert frozen.json()["incident"]["mode"] == "freeze_new_authority"
        with pytest.raises(GateBlocked, match="new authority is frozen"):
            core.outage.require_issuance()
        core.outage.require_privileged()

        replay = await _request(
            client,
            actor_key,
            actor,
            "POST",
            path,
            {
                "change": freeze.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
        assert replay.status_code == 409

        status = await _request(client, actor_key, actor, "GET", path)
        assert status.status_code == 200, status.text
        assert status.json()["incident"]["revision"] == 1

        clear = IncidentModeChange(
            domain_id=actor.domain_id,
            expected_revision=1,
            target_mode=IncidentMode.NORMAL,
            reason="independent investigation cleared the issuer",
        )
        _resource, clear_exact = core.incidents.authority_binding(clear)
        clear_command = _command(
            key=actor_key,
            actor=actor,
            action=core.incidents.ACTION,
            resource=resource,
            request=clear_exact,
            revision=1,
            reason=clear.reason,
        )
        cleared = await _request(
            client,
            actor_key,
            actor,
            "POST",
            path,
            {
                "change": clear.model_dump(mode="json"),
                "command": clear_command.model_dump(mode="json"),
            },
        )
        assert cleared.status_code == 200, cleared.text
        assert cleared.json()["incident"]["mode"] == "normal"
        core.outage.require_issuance()

        current = await _request(client, actor_key, actor, "GET", path)
        assert current.status_code == 200, current.text
        assert current.json()["incident"]["revision"] == 2


@pytest.mark.anyio
async def test_authority_inspection_http_is_transport_owner_bound_and_non_enumerating(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, owner_key = identity_factory(binding_assurance="os_bound")
    other, _other_key = identity_factory(binding_assurance="os_bound")
    core = _core(store, tmp_path, domain=owner.domain_id)
    _allow(core, owner, "message.send", "resource:owned")
    owner_denial = AuthorizationDecision(
        decision_id="owner-http-denial",
        actor=owner,
        action="protected.secret.read",
        resource={"secret": "must-not-leak"},
        context={"principal_id": other.principal_id},
        allowed=False,
        reason="task_grant_expired",
        policy_revision=1,
    )
    other_denial = owner_denial.model_copy(
        update={"decision_id": "other-http-denial", "actor": other}
    )
    with store.transaction() as connection:
        recorder = DecisionRecorder(store)
        recorder.record(connection, owner_denial)
        recorder.record(connection, other_denial)

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app(core), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        inventory = await _request(client, owner_key, owner, "GET", "/v1/authority")
        assert inventory.status_code == 200, inventory.text
        authority = inventory.json()["authority"]
        assert authority["authority_id"] == owner.principal_id
        assert authority["authenticated_harness_id"] == owner.harness_id
        assert any(item["actions"] == ["message.send"] for item in authority["bases"])
        assert authority["descriptive_only"] is True
        assert authority["grants_no_new_authority"] is True

        explained = await _request(
            client,
            owner_key,
            owner,
            "GET",
            "/v1/authority/denials/owner-http-denial",
        )
        assert explained.status_code == 200, explained.text
        explanation = explained.json()["explanation"]
        assert explanation["reason_code"] == "task_grant_expired"
        assert "must-not-leak" not in explained.text
        assert other.principal_id not in explained.text

        unavailable_codes = []
        for decision_id in ("missing-http-denial", "other-http-denial"):
            unavailable = await _request(
                client,
                owner_key,
                owner,
                "GET",
                f"/v1/authority/denials/{decision_id}",
            )
            unavailable_codes.append(unavailable.status_code)
        assert unavailable_codes == [404, 404]

        caller_selected = await _request(
            client,
            owner_key,
            owner,
            "GET",
            "/v1/authority",
            query=f"principal_id={other.principal_id}",
        )
        assert caller_selected.status_code == 422
