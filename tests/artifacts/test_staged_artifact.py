from __future__ import annotations

import hashlib
import json
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest

from agentnet.artifacts.scanner import ArtifactDerivationV1, ScannerTrustPolicy
from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
    PolicyEngine,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.protocol.models import Classification, EventType, ReleasedArtifactBinding
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceObjectType,
    ProvenanceOrigin,
    SinkSet,
    TransformationKind,
    TransformationStep,
)
from agentnet.security.signatures import P256KeyPair, canonical_digest


class InjectedReleaseCrash(RuntimeError):
    pass


def test_disabled_artifact_service_rejects_every_public_surface_before_state_access(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, _ = identity_factory()
    recipient, _ = identity_factory()
    objects_root = tmp_path / "objects"
    service = ArtifactService(store, None, enabled=False)
    binding = ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id=actor.domain_id,
        object_version="a" * 64,
        size=1,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )
    event = new_event(
        domain_id=actor.domain_id,
        actor=actor,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"kind": "disabled-artifact-surface-matrix"},
        idempotency_key="disabled-artifact-surface-0001",
        recipients=(recipient.harness_id,),
        released_artifacts=(binding,),
    )
    operations = {
        "require_enabled": lambda: service.require_enabled(),
        "reconcile_quota_accounting": lambda: service.reconcile_quota_accounting(),
        "reserve": lambda: service.reserve(
            actor=actor,
            idempotency_key="communication-only-artifact-reservation",
            expected_digest="0" * 64,
            expected_size=0,
            media_type="text/plain",
            classification=Classification.C1_INTERNAL,
            required_attachment=False,
            policy_decision_id="unused-because-disabled",
        ),
        "abort_reservation": lambda: service.abort_reservation(
            "reservation-disabled",
            actor=actor,
            policy_decision_id="unused-because-disabled",
        ),
        "process_reservation_release": lambda: service.process_reservation_release(
            "reservation-disabled"
        ),
        "recover_expired_reservations": lambda: service.recover_expired_reservations(),
        "resolve_released_binding": lambda: service.resolve_released_binding(binding.artifact_id),
        "require_released_binding": lambda: service.require_released_binding(
            binding,
            domain_id=actor.domain_id,
            event_classification=Classification.C1_INTERNAL,
        ),
        "require_event_artifacts": lambda: service.require_event_artifacts(event),
        "upload": lambda: service.upload(
            "reservation-disabled",
            b"must-not-be-read",
            actor=actor,
            policy_decision_id="unused-because-disabled",
        ),
        "promote_manifest": lambda: service.promote_manifest(
            reservation_id="reservation-disabled",
            object_version="a" * 64,
            provenance={},
            actor=actor,
            policy_decision_id="unused-because-disabled",
        ),
        "record_scan": lambda: service.record_scan(binding.artifact_id, {}),
        "release": lambda: service.release(
            binding.artifact_id,
            actor=actor,
            policy_decision_id="unused-because-disabled",
        ),
        "process_release_outbox": lambda: service.process_release_outbox(binding.artifact_id),
        "recover_release_outbox": lambda: service.recover_release_outbox(),
        "lifecycle_status": lambda: service.lifecycle_status(binding.artifact_id, actor=actor),
        "set_legal_hold": lambda: service.set_legal_hold(
            binding.artifact_id,
            actor=actor,
            policy_decision_id="unused-because-disabled",
            expected_revision=1,
            reason="disabled",
            enabled=True,
        ),
        "delete": lambda: service.delete(
            binding.artifact_id,
            actor=actor,
            policy_decision_id="unused-because-disabled",
            expected_revision=1,
            reason="disabled",
        ),
        "process_deletion_outbox": lambda: service.process_deletion_outbox(binding.artifact_id),
        "recover_deletion_outbox": lambda: service.recover_deletion_outbox(),
        "issue_download_capability": lambda: service.issue_download_capability(
            binding.artifact_id,
            actor=actor,
            audience_harness_id=actor.harness_id,
            policy_decision_id="unused-because-disabled",
        ),
        "consume_download": lambda: service.consume_download("disabled-token", actor=actor),
    }
    public_surfaces = {
        name
        for name, value in ArtifactService.__dict__.items()
        if callable(value) and not name.startswith("_")
    }
    assert set(operations) == public_surfaces
    artifact_tables = (
        "artifact_reservations",
        "artifact_manifests",
        "artifact_release_outbox",
        "artifact_byte_accounts",
        "artifact_byte_charges",
        "artifact_lifecycle",
        "artifact_deletion_outbox",
        "download_capabilities",
    )
    before = {
        table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
        for table in artifact_tables
    }

    for name, operation in operations.items():
        with pytest.raises(GateBlocked, match="artifact operations are disabled") as denied:
            operation()
        assert denied.value.gate == "artifacts_disabled", name

    after = {
        table: int(store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"])
        for table in artifact_tables
    }
    assert after == before
    assert not objects_root.exists()
    assert store.fetch_one("SELECT reservation_id FROM artifact_reservations LIMIT 1") is None


def authorize(
    policy: PolicyEngine,
    actor,
    *,
    action: str,
    resource: str,
    context: dict[str, object] | None = None,
    add_entitlement: bool = True,
) -> str:
    if add_entitlement:
        LocalConformancePolicyEngine(policy.store).bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
            )
        )
    return policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=1,
            context=context or {},
        )
    ).decision_id


def test_derived_artifact_binds_exact_parent_chain_and_authenticated_executor(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, _ = identity_factory()
    service = ArtifactService(
        store,
        FilesystemArtifactStore(
            tmp_path / "derived-objects",
            tmp_path / "derived-secrets" / "artifacts.key",
        ),
    )
    policy = PolicyEngine(store)
    now = datetime.now(UTC).replace(microsecond=0)
    parent_content = b"exact parent content"
    parent = service.provenance.register_origin(
        OriginRegistration(
            object_type=ProvenanceObjectType.EVENT,
            object_id=f"artifact-parent-{uuid4()}",
            domain_id=actor.domain_id,
            origin=ProvenanceOrigin(
                kind=OriginKind.HUMAN_INPUT,
                source_id=f"artifact-parent-input:{uuid4()}",
                source_digest=hashlib.sha256(parent_content).hexdigest(),
                principal_id=actor.principal_id,
                harness_id=actor.harness_id,
                observed_at=now,
            ),
            classification=Classification.C1_INTERNAL,
            allowed_sinks=SinkSet(sinks=()),
            policy_revision=1,
            recorded_at=now,
        ),
        when=now,
    )
    output = b"derived artifact bytes"
    output_digest = hashlib.sha256(output).hexdigest()
    reserve_context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": output_digest,
        "expected_size": len(output),
        "media_type": "text/plain",
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"derived-artifact-{uuid4()}",
        expected_digest=output_digest,
        expected_size=len(output),
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=reserve_context,
        ),
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        output,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": output_digest, "expected_size": len(output)},
        ),
    )
    step = TransformationStep(
        kind=TransformationKind.PARSER,
        operation_id=f"artifact-transform:{uuid4()}",
        implementation_id="agentnet.test.exact-artifact-transform",
        implementation_version="1",
        executor_harness_id=actor.harness_id,
        input_digests=(parent.content_digest,),
        output_digest=output_digest,
        started_at=now,
        completed_at=now,
    )
    derivation = ArtifactDerivationV1(
        parent_references=(parent.reference(),),
        transformations=(step,),
    )
    decision_context = {
        "object_version": uploaded["version"],
        "request_digest": reservation["request_digest"],
        "derivation_digest": canonical_digest(derivation.model_dump(mode="json")),
    }

    manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "derived-test"},
        derivation=derivation,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.manifest.promote",
            resource=reservation["reservation_id"],
            context=decision_context,
        ),
    )
    record = service.provenance.get_by_digest(manifest["provenance"]["provenance_digest"])
    assert record.origin.kind is OriginKind.DERIVED
    assert record.parent_digests.digests == (parent.provenance_digest,)
    assert record.transformations.steps == (step,)
    assert record.content_digest == output_digest
    assert record.tainted is True

    duplicate = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "derived-test"},
        derivation=derivation,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.manifest.promote",
            resource=reservation["reservation_id"],
            context=decision_context,
            add_entitlement=False,
        ),
    )
    assert duplicate["duplicate"] is True
    assert duplicate["provenance"] == manifest["provenance"]

    spoofed = derivation.model_copy(
        update={
            "transformations": (
                step.model_copy(update={"executor_harness_id": "untrusted-harness"}),
            )
        }
    )
    with pytest.raises(AuthorizationError, match="authenticated promoting harness"):
        service.promote_manifest(
            reservation_id=reservation["reservation_id"],
            object_version=uploaded["version"],
            provenance={"origin": "derived-test"},
            derivation=spoofed,
            actor=actor,
            policy_decision_id="not-consulted",
        )


def test_quarantine_scan_release_and_single_use_download(store, identity_factory, tmp_path: Path) -> None:
    actor, _ = identity_factory()
    intruder, _ = identity_factory()
    objects = FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secrets" / "artifacts.key")
    scanner_key = P256KeyPair.generate()
    service = ArtifactService(store, objects, trusted_scanner_keys={"synthetic-test-scanner": scanner_key.public_pem})
    policy = PolicyEngine(store)
    content = b"synthetic safe artifact"
    expected_digest = hashlib.sha256(content).hexdigest()
    reserve_context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": expected_digest,
        "expected_size": len(content),
        "media_type": "text/plain",
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"artifact-{uuid4()}",
        expected_digest=expected_digest,
        expected_size=len(content),
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=reserve_context,
        ),
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": expected_digest, "expected_size": len(content)},
        ),
    )
    repeated_upload = service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": expected_digest, "expected_size": len(content)},
            add_entitlement=False,
        ),
    )
    assert repeated_upload["duplicate"] is True
    assert repeated_upload["version"] == uploaded["version"]
    manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "test"},
        actor=actor,
        policy_decision_id=authorize(
            policy,
            action="artifact.manifest.promote",
            actor=actor,
            resource=reservation["reservation_id"],
            context={"object_version": uploaded["version"], "request_digest": reservation["request_digest"]},
        ),
    )
    assert manifest["provenance"]["object_type"] == "artifact"
    assert manifest["provenance"]["content_digest"] == expected_digest
    assert manifest["provenance"]["tainted"] is True
    assert manifest["provenance"]["authority_effect"] == "none"
    recorded = service.provenance.get_by_digest(
        manifest["provenance"]["provenance_digest"]
    )
    assert recorded.origin.harness_id == actor.harness_id
    assert recorded.allowed_sinks.sinks == ()
    duplicate_manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "test"},
        actor=actor,
        policy_decision_id=authorize(
            policy,
            action="artifact.manifest.promote",
            actor=actor,
            resource=reservation["reservation_id"],
            context={
                "object_version": uploaded["version"],
                "request_digest": reservation["request_digest"],
            },
            add_entitlement=False,
        ),
    )
    assert duplicate_manifest["duplicate"] is True
    assert duplicate_manifest["provenance"] == manifest["provenance"]
    with pytest.raises(IdempotencyConflict, match="attribution changed"):
        service.promote_manifest(
            reservation_id=reservation["reservation_id"],
            object_version=uploaded["version"],
            provenance={"origin": "substituted"},
            actor=actor,
            policy_decision_id=authorize(
                policy,
                action="artifact.manifest.promote",
                actor=actor,
                resource=reservation["reservation_id"],
                context={
                    "object_version": uploaded["version"],
                    "request_digest": reservation["request_digest"],
                },
                add_entitlement=False,
            ),
        )
    scan_fields = {
        "artifact_id": manifest["artifact_id"],
        "classification": Classification.C1_INTERNAL.value,
        "ciphertext_digest": uploaded["version"],
        "expires_at": int(time.time()) + 300,
        "issued_at": int(time.time()),
        "object_key": reservation["object_key"],
        "object_version": uploaded["version"],
        "plaintext_digest": expected_digest,
        "policy_revision": 1,
        "profile_digest": "c" * 64,
        "scanner_engine": "synthetic-test-engine",
        "scanner_id": "synthetic-test-scanner",
        "scanner_key_epoch": 1,
        "scanner_version": "1",
        "rules_digest": "a" * 64,
        "result": "allow",
    }
    signed_scan = scan_fields | {"signature": scanner_key.sign("agentnet.artifact.attestation.v1", scan_fields)}
    service.record_scan(manifest["artifact_id"], signed_scan)
    assert service.record_scan(
        manifest["artifact_id"],
        signed_scan,
    )["duplicate"] is True
    with pytest.raises(AuthorizationError):
        service.release(manifest["artifact_id"], actor=actor, policy_decision_id="missing")
    service.release(
        manifest["artifact_id"],
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.release",
            resource=manifest["artifact_id"],
        ),
    )
    lifecycle = service.lifecycle_status(manifest["artifact_id"], actor=actor)
    assert lifecycle["manifest_state"] == "released"
    assert lifecycle["lifecycle_state"] == "active"
    assert lifecycle["object_version"] == uploaded["version"]
    token = service.issue_download_capability(
        manifest["artifact_id"],
        actor=actor,
        audience_harness_id=actor.harness_id,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.download",
            resource=manifest["artifact_id"],
            context={"audience_harness_id": actor.harness_id},
        ),
    )
    with pytest.raises(AuthorizationError):
        service.consume_download(token, actor=intruder)
    assert service.consume_download(token, actor=actor) == content
    with pytest.raises(AuthorizationError):
        service.consume_download(token, actor=actor)


def test_artifact_idempotency_digest_conflict(store, identity_factory, tmp_path: Path) -> None:
    actor, _ = identity_factory()
    service = ArtifactService(store, FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secret.key"))
    policy = PolicyEngine(store)
    key = f"artifact-{uuid4()}"
    kwargs = dict(
        actor=actor,
        idempotency_key=key,
        expected_digest="a" * 64,
        expected_size=1,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
    )
    first_context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": "a" * 64,
        "expected_size": 1,
        "media_type": "text/plain",
        "required_attachment": True,
    }
    service.reserve(
        **kwargs,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=first_context,
        ),
    )
    with pytest.raises(IdempotencyConflict):
        service.reserve(
            **(kwargs | {"expected_size": 2}),
            policy_decision_id=authorize(
                policy,
                actor,
                action="artifact.upload.reserve",
                resource="artifact:new",
                context=first_context | {"expected_size": 2},
                add_entitlement=False,
            ),
        )


@pytest.mark.parametrize(
    ("content", "media_type", "reason"),
    [
        (b"\x7fELF synthetic executable", "application/octet-stream", "executable_content"),
        (b"#!/bin/sh\necho unsafe", "text/plain", "executable_content"),
        (b"\x00asm synthetic wasm", "application/octet-stream", "executable_content"),
        (b"\xcf\xfa\xed\xfe synthetic Mach-O", "application/octet-stream", "executable_content"),
        (b"PK\x03\x04 synthetic archive", "application/octet-stream", "uninspected_container"),
        (b"\x1f\x8b synthetic compressed stream", "application/octet-stream", "uninspected_container"),
        (
            b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1 synthetic macro container",
            "application/octet-stream",
            "uninspected_container",
        ),
        (
            b"X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*",
            "application/octet-stream",
            "known_malware_test_signature",
        ),
        (b"api_key=super-secret-value-123", "text/plain", "secret_pattern"),
        (b"-----BEGIN PRIVATE KEY-----\nsynthetic", "text/plain", "secret_pattern"),
        (b"not a png", "image/png", "media_type_mismatch"),
        (b"\xff\xfeinvalid utf-8", "text/plain", "media_type_mismatch"),
    ],
)
def test_local_content_prefilter_persists_content_free_denial_without_storing_bytes(
    store,
    identity_factory,
    tmp_path: Path,
    content: bytes,
    media_type: str,
    reason: str,
) -> None:
    actor, _ = identity_factory()
    objects = FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secret.key")
    service = ArtifactService(store, objects)
    policy = PolicyEngine(store)
    digest = hashlib.sha256(content).hexdigest()
    context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": digest,
        "expected_size": len(content),
        "media_type": media_type,
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"prefilter-{uuid4()}",
        expected_digest=digest,
        expected_size=len(content),
        media_type=media_type,
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=context,
        ),
    )

    with pytest.raises(AuthorizationError, match=reason):
        service.upload(
            reservation["reservation_id"],
            content,
            actor=actor,
            policy_decision_id=authorize(
                policy,
                actor,
                action="artifact.upload.bytes",
                resource=reservation["reservation_id"],
                context={"expected_digest": digest, "expected_size": len(content)},
            ),
        )

    row = store.fetch_one(
        "SELECT state,object_key,object_version FROM artifact_reservations WHERE reservation_id=?",
        (reservation["reservation_id"],),
    )
    assert row["state"] == "prefilter_denied"
    assert row["object_version"] is None
    charge = store.fetch_one(
        "SELECT state,release_reason FROM artifact_byte_charges WHERE reservation_id=?",
        (reservation["reservation_id"],),
    )
    assert (charge["state"], charge["release_reason"]) == ("released", "prefilter_denied")
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (actor.principal_id,),
    )["used_bytes"] == 0
    assert not any(objects.quarantine.rglob("*"))
    records = [json.loads(item["record_json"]) for item in store.fetch_all("SELECT record_json FROM audit_log")]
    denied = next(item for item in records if item.get("action") == "artifact.prefilter_denied")
    assert denied["reason_code"] == reason
    assert content.decode("latin1") not in json.dumps(denied)
    assert service.recover_expired_reservations() == []


def prepare_scanned_artifact(store, identity_factory, tmp_path: Path):
    actor, _ = identity_factory()
    scanner_key = P256KeyPair.generate()
    objects = FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secrets" / "artifact.key")
    service = ArtifactService(store, objects, trusted_scanner_keys={"scanner": scanner_key.public_pem})
    policy = PolicyEngine(store)
    content = b"crash-safe synthetic artifact"
    digest = hashlib.sha256(content).hexdigest()
    reserve_context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": digest,
        "expected_size": len(content),
        "media_type": "application/octet-stream",
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"crash-artifact-{uuid4()}",
        expected_digest=digest,
        expected_size=len(content),
        media_type="application/octet-stream",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=reserve_context,
        ),
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": digest, "expected_size": len(content)},
        ),
    )
    manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "crash-test"},
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.manifest.promote",
            resource=reservation["reservation_id"],
            context={"object_version": uploaded["version"], "request_digest": reservation["request_digest"]},
        ),
    )
    scan = {
        "artifact_id": manifest["artifact_id"],
        "classification": Classification.C1_INTERNAL.value,
        "ciphertext_digest": uploaded["version"],
        "expires_at": int(time.time()) + 300,
        "issued_at": int(time.time()),
        "object_key": reservation["object_key"],
        "object_version": uploaded["version"],
        "plaintext_digest": digest,
        "policy_revision": 1,
        "profile_digest": "c" * 64,
        "scanner_engine": "synthetic-test-engine",
        "scanner_id": "scanner",
        "scanner_key_epoch": 1,
        "scanner_version": "1",
        "rules_digest": "b" * 64,
        "result": "allow",
    }
    service.record_scan(
        manifest["artifact_id"],
        scan | {"signature": scanner_key.sign("agentnet.artifact.attestation.v1", scan)},
    )
    release_decision = authorize(
        policy,
        actor,
        action="artifact.release",
        resource=manifest["artifact_id"],
    )
    return service, actor, manifest["artifact_id"], release_decision


def test_manifest_and_provenance_commit_atomically_and_corruption_blocks_release(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch,
) -> None:
    actor, _ = identity_factory()
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "atomic-objects", tmp_path / "atomic.key"),
    )
    policy = PolicyEngine(store)
    content = b"atomic provenance artifact"
    digest = hashlib.sha256(content).hexdigest()
    context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": digest,
        "expected_size": len(content),
        "media_type": "text/plain",
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"atomic-{uuid4()}",
        expected_digest=digest,
        expected_size=len(content),
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=context,
        ),
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": digest, "expected_size": len(content)},
        ),
    )
    original = service.provenance.register_origin_in_transaction

    def fail_after_provenance(connection, registration, *, when=None):
        original(connection, registration, when=when)
        raise InjectedReleaseCrash("after provenance append")

    monkeypatch.setattr(
        service.provenance,
        "register_origin_in_transaction",
        fail_after_provenance,
    )
    with pytest.raises(InjectedReleaseCrash, match="after provenance append"):
        service.promote_manifest(
            reservation_id=reservation["reservation_id"],
            object_version=uploaded["version"],
            provenance={"origin": "atomic-test"},
            actor=actor,
            policy_decision_id=authorize(
                policy,
                actor,
                action="artifact.manifest.promote",
                resource=reservation["reservation_id"],
                context={
                    "object_version": uploaded["version"],
                    "request_digest": reservation["request_digest"],
                },
            ),
        )
    assert store.fetch_one(
        "SELECT COUNT(*) AS total FROM content_provenance WHERE object_type='artifact'"
    )["total"] == 0
    assert store.fetch_one(
        "SELECT COUNT(*) AS total FROM artifact_manifests WHERE reservation_id=?",
        (reservation["reservation_id"],),
    )["total"] == 0
    assert store.fetch_one(
        "SELECT state FROM artifact_reservations WHERE reservation_id=?",
        (reservation["reservation_id"],),
    )["state"] == "object_verified"

    scanned, owner, artifact_id, release_decision = prepare_scanned_artifact(
        store,
        identity_factory,
        tmp_path / "corrupt",
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE artifact_manifests SET provenance_json='{}' WHERE artifact_id=?",
            (artifact_id,),
        )
    with pytest.raises(ConflictError, match="provenance"):
        scanned.release(
            artifact_id,
            actor=owner,
            policy_decision_id=release_decision,
        )
    assert store.fetch_one(
        "SELECT state FROM artifact_manifests WHERE artifact_id=?",
        (artifact_id,),
    )["state"] == "held"
    assert store.fetch_one(
        "SELECT COUNT(*) AS total FROM artifact_release_outbox WHERE artifact_id=?",
        (artifact_id,),
    )["total"] == 0


@pytest.mark.parametrize(
    "crash_phase",
    [
        "after_release_intent_inserted",
        "after_release_intent_committed",
        "after_release_object_promoted",
        "before_release_commit",
    ],
)
def test_release_outbox_recovers_every_authorize_intent_promote_commit_crash(
    store,
    identity_factory,
    tmp_path: Path,
    crash_phase: str,
) -> None:
    service, actor, artifact_id, release_decision = prepare_scanned_artifact(
        store,
        identity_factory,
        tmp_path,
    )

    def crash(current: str) -> None:
        if current == crash_phase:
            raise InjectedReleaseCrash(current)

    with pytest.raises(InjectedReleaseCrash, match=crash_phase):
        service.release(
            artifact_id,
            actor=actor,
            policy_decision_id=release_decision,
            phase_hook=crash,
        )

    manifest_state = store.fetch_one(
        "SELECT state FROM artifact_manifests WHERE artifact_id=?",
        (artifact_id,),
    )["state"]
    if crash_phase == "after_release_intent_inserted":
        assert manifest_state == "scan_passed"
        assert store.fetch_one("SELECT COUNT(*) AS count FROM audit_intents")["count"] == 0
        assert store.fetch_one("SELECT COUNT(*) AS count FROM artifact_release_outbox")["count"] == 0
        recovered = service.release(
            artifact_id,
            actor=actor,
            policy_decision_id=release_decision,
        )
    else:
        assert manifest_state == "release_pending"
        assert store.fetch_one("SELECT state FROM audit_intents")["state"] == "pending"
        assert store.fetch_one("SELECT state FROM artifact_release_outbox")["state"] == "pending"
        recovered_items = service.recover_release_outbox()
        assert len(recovered_items) == 1
        recovered = recovered_items[0]

    assert recovered["state"] == "released"
    assert store.fetch_one("SELECT state FROM artifact_manifests WHERE artifact_id=?", (artifact_id,))["state"] == "released"
    assert store.fetch_one("SELECT state FROM audit_intents")["state"] == "completed"
    assert store.fetch_one("SELECT state FROM artifact_release_outbox")["state"] == "completed"
    actions = [
        json.loads(row["record_json"]).get("action")
        for row in store.fetch_all("SELECT record_json FROM audit_log ORDER BY sequence")
    ]
    assert actions.index("artifact.release_intent_committed") < actions.index("artifact.released")
    assert store.verify_audit_chain()[0] is True


class ScannerClock:
    def __init__(self, value: int = 1_000_000) -> None:
        self.value = value

    def __call__(self) -> int:
        return self.value


def prepare_policy_bound_scan(store, identity_factory, tmp_path: Path):
    actor, _ = identity_factory()
    scanner_key = P256KeyPair.generate()
    clock = ScannerClock()
    rules = "d" * 64
    profile = "e" * 64
    objects = FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secrets" / "fresh.key")
    scanner_policy = ScannerTrustPolicy(
        max_attestation_age_seconds=300,
        required_engine="maintained-scanner",
        required_rules_digest=rules,
        required_profile_digest=profile,
    )
    service = ArtifactService(
        store,
        objects,
        trusted_scanner_keys={"scanner:7": scanner_key.public_pem},
        scanner_policy=scanner_policy,
        clock=clock,
    )
    policy = PolicyEngine(store)
    content = b"policy-bound scanner artifact"
    digest = hashlib.sha256(content).hexdigest()
    reserve_context = {
        "actor": actor.audit_view(),
        "classification": Classification.C2_RESTRICTED.value,
        "expected_digest": digest,
        "expected_size": len(content),
        "media_type": "application/octet-stream",
        "required_attachment": True,
    }
    reservation = service.reserve(
        actor=actor,
        idempotency_key=f"fresh-scan-{uuid4()}",
        expected_digest=digest,
        expected_size=len(content),
        media_type="application/octet-stream",
        classification=Classification.C2_RESTRICTED,
        required_attachment=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=reserve_context,
        ),
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": digest, "expected_size": len(content)},
        ),
    )
    manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "freshness-test"},
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.manifest.promote",
            resource=reservation["reservation_id"],
            context={"object_version": uploaded["version"], "request_digest": reservation["request_digest"]},
        ),
    )
    scan = {
        "artifact_id": manifest["artifact_id"],
        "classification": Classification.C2_RESTRICTED.value,
        "ciphertext_digest": uploaded["version"],
        "expires_at": clock.value + 300,
        "issued_at": clock.value,
        "object_key": reservation["object_key"],
        "object_version": uploaded["version"],
        "plaintext_digest": digest,
        "policy_revision": 1,
        "profile_digest": profile,
        "result": "allow",
        "rules_digest": rules,
        "scanner_engine": "maintained-scanner",
        "scanner_id": "scanner",
        "scanner_key_epoch": 7,
        "scanner_version": "2026.07",
    }
    service.record_scan(
        manifest["artifact_id"],
        scan | {"signature": scanner_key.sign("agentnet.artifact.attestation.v1", scan)},
    )
    return service, objects, scanner_key, scanner_policy, clock, actor, manifest["artifact_id"]


def test_scan_evidence_is_encrypted_and_expiry_holds_release(
    store, identity_factory, tmp_path: Path
) -> None:
    service, _objects, _key, _policy, clock, actor, artifact_id = prepare_policy_bound_scan(
        store, identity_factory, tmp_path
    )
    stored = store.fetch_one(
        "SELECT scanner_attestation_json FROM artifact_manifests WHERE artifact_id=?",
        (artifact_id,),
    )["scanner_attestation_json"]
    assert "plaintext_digest" not in stored
    assert "maintained-scanner" not in stored

    clock.value += 300
    with pytest.raises(AuthorizationError, match="expired"):
        service.release(artifact_id, actor=actor, policy_decision_id="not-reached")
    assert store.fetch_one(
        "SELECT state FROM artifact_manifests WHERE artifact_id=?", (artifact_id,)
    )["state"] == "held"


@pytest.mark.parametrize("drift", ["rules", "key", "revoked", "policy"])
def test_scan_rules_key_and_policy_drift_hold_release(
    store, identity_factory, tmp_path: Path, drift: str
) -> None:
    service, objects, key, policy, clock, actor, artifact_id = prepare_policy_bound_scan(
        store, identity_factory, tmp_path
    )
    trusted = {"scanner:7": key.public_pem}
    replacement_policy = policy
    if drift == "rules":
        replacement_policy = ScannerTrustPolicy(
            required_engine="maintained-scanner",
            required_rules_digest="f" * 64,
            required_profile_digest="e" * 64,
        )
    elif drift == "key":
        trusted = {"scanner:8": P256KeyPair.generate().public_pem}
    elif drift == "revoked":
        replacement_policy = ScannerTrustPolicy(
            required_engine="maintained-scanner",
            required_rules_digest="d" * 64,
            required_profile_digest="e" * 64,
            revoked_key_epochs=frozenset({("scanner", 7)}),
        )
    else:
        with store.transaction() as connection:
            connection.execute(
                "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id=?",
                (actor.domain_id,),
            )
    replacement = ArtifactService(
        store,
        objects,
        trusted_scanner_keys=trusted,
        scanner_policy=replacement_policy,
        clock=clock,
    )
    with pytest.raises((AuthorizationError, ValidationError)):
        replacement.release(artifact_id, actor=actor, policy_decision_id="not-reached")
    assert store.fetch_one(
        "SELECT state FROM artifact_manifests WHERE artifact_id=?", (artifact_id,)
    )["state"] == "held"


def test_scanner_substitution_cannot_record_allow(store, identity_factory, tmp_path: Path) -> None:
    service, _objects, _key, _policy, _clock, _actor, artifact_id = prepare_policy_bound_scan(
        store, identity_factory, tmp_path
    )
    stored = store.fetch_one(
        "SELECT scanner_attestation_json FROM artifact_manifests WHERE artifact_id=?",
        (artifact_id,),
    )["scanner_attestation_json"]
    attestation = service._decode_scan_attestation(artifact_id, stored)
    attestation["scanner_version"] = "substituted"
    with pytest.raises(AuthenticationError, match="signature verification failed"):
        service.record_scan(artifact_id, attestation)


def test_only_current_completed_release_can_mint_or_validate_an_event_binding(
    store, identity_factory, tmp_path: Path
) -> None:
    service, actor, artifact_id, release_decision = prepare_scanned_artifact(
        store, identity_factory, tmp_path
    )
    recipient, _ = identity_factory(kind="pi")
    with pytest.raises(AuthorizationError, match="completed corporate release"):
        service.resolve_released_binding(artifact_id)

    service.release(
        artifact_id,
        actor=actor,
        policy_decision_id=release_decision,
    )
    binding = service.resolve_released_binding(artifact_id)
    event = new_event(
        domain_id=actor.domain_id,
        actor=actor,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"body": "artifact binding validation"},
        idempotency_key=f"artifact-event-{uuid4()}",
        recipients=(recipient.harness_id,),
        released_artifacts=(binding,),
    )
    assert service.require_event_artifacts(event) == (binding,)
    with pytest.raises(AuthorizationError, match="stale or substituted"):
        service.require_released_binding(
            binding.model_copy(update={"size": binding.size + 1}),
            domain_id=actor.domain_id,
            event_classification=Classification.C1_INTERNAL,
        )


@pytest.mark.parametrize(
    ("expected_digest", "expected_size", "media_type", "message"),
    [
        ("A" * 64, 1, "text/plain", "lowercase SHA-256"),
        ("a" * 64, 16_777_217, "text/plain", "supported boundary"),
        ("a" * 64, 1, "Text/Plain; charset=utf-8", "canonical lowercase"),
    ],
)
def test_artifact_reservation_rejects_ambiguous_digest_size_and_media_type_before_policy(
    store,
    identity_factory,
    tmp_path: Path,
    expected_digest: str,
    expected_size: int,
    media_type: str,
    message: str,
) -> None:
    actor, _ = identity_factory()
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "objects", tmp_path / "artifact.key"),
    )
    with pytest.raises(ValidationError, match=message):
        service.reserve(
            actor=actor,
            idempotency_key=f"invalid-artifact-{uuid4()}",
            expected_digest=expected_digest,
            expected_size=expected_size,
            media_type=media_type,
            classification=Classification.C1_INTERNAL,
            required_attachment=True,
            policy_decision_id="must-not-be-reached",
        )


def test_legal_hold_blocks_deletion_and_crash_recovery_finishes_exact_version(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    service, actor, artifact_id, _release_decision = prepare_scanned_artifact(
        store,
        identity_factory,
        tmp_path,
    )
    policy = PolicyEngine(store)
    assert service.lifecycle_status(artifact_id, actor=actor)["lifecycle_revision"] == 1
    hold_reason = "litigation preservation order 2026-07"
    hold = service.set_legal_hold(
        artifact_id,
        actor=actor,
        expected_revision=1,
        reason=hold_reason,
        enabled=True,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.legal_hold.set",
            resource=artifact_id,
            context={"enabled": True, "expected_revision": 1, "reason": hold_reason},
        ),
    )
    assert (hold["legal_hold"], hold["lifecycle_revision"]) == (True, 2)
    delete_reason = "retention elapsed and owner approved disposal"
    blocked_decision = authorize(
        policy,
        actor,
        action="artifact.delete",
        resource=artifact_id,
        context={"expected_revision": 2, "reason": delete_reason},
    )
    with pytest.raises(ConflictError, match="legal hold"):
        service.delete(
            artifact_id,
            actor=actor,
            policy_decision_id=blocked_decision,
            expected_revision=2,
            reason=delete_reason,
        )

    clear_reason = "counsel released preservation order"
    cleared = service.set_legal_hold(
        artifact_id,
        actor=actor,
        expected_revision=2,
        reason=clear_reason,
        enabled=False,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.legal_hold.clear",
            resource=artifact_id,
            context={"enabled": False, "expected_revision": 2, "reason": clear_reason},
        ),
    )
    assert (cleared["legal_hold"], cleared["lifecycle_revision"]) == (False, 3)
    deletion_decision = authorize(
        policy,
        actor,
        action="artifact.delete",
        resource=artifact_id,
        context={"expected_revision": 3, "reason": delete_reason},
        add_entitlement=False,
    )

    def crash(phase: str) -> None:
        if phase == "after_deletion_object_removed":
            raise InjectedReleaseCrash(phase)

    with pytest.raises(InjectedReleaseCrash, match="after_deletion_object_removed"):
        service.delete(
            artifact_id,
            actor=actor,
            policy_decision_id=deletion_decision,
            expected_revision=3,
            reason=delete_reason,
            phase_hook=crash,
        )
    pending = service.lifecycle_status(artifact_id, actor=actor)
    assert (pending["lifecycle_state"], pending["lifecycle_revision"]) == (
        "deletion_pending",
        4,
    )
    recovered = service.recover_deletion_outbox()
    assert recovered[0]["state"] == "deleted"
    deleted = service.lifecycle_status(artifact_id, actor=actor)
    assert (deleted["lifecycle_state"], deleted["manifest_state"]) == ("deleted", "deleted")
    row = store.fetch_one(
        """SELECT l.deletion_reason_encrypted,m.scanner_attestation_json
             FROM artifact_lifecycle l JOIN artifact_manifests m ON m.artifact_id=l.artifact_id
            WHERE l.artifact_id=?""",
        (artifact_id,),
    )
    assert delete_reason not in row["deletion_reason_encrypted"]
    assert row["scanner_attestation_json"] is None
    with pytest.raises(AuthorizationError, match="completed corporate release"):
        service.resolve_released_binding(artifact_id)
    assert store.verify_audit_chain()[0] is True


def test_retained_and_legally_held_event_references_block_artifact_deletion(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    service, actor, artifact_id, release_decision = prepare_scanned_artifact(
        store,
        identity_factory,
        tmp_path,
    )
    service.release(artifact_id, actor=actor, policy_decision_id=release_decision)
    binding = service.resolve_released_binding(artifact_id)
    policy = PolicyEngine(store)
    token = service.issue_download_capability(
        artifact_id,
        actor=actor,
        audience_harness_id=actor.harness_id,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.download",
            resource=artifact_id,
            context={"audience_harness_id": actor.harness_id},
        ),
    )
    recipient, _ = identity_factory(kind="pi")
    scopes = CollaborationScopeService(store)
    scope_id = f"scope:retained-artifact:{uuid4()}"
    scope_proposal = CollaborationScopeProposal(
        scope_id=scope_id,
        scope_kind="direct",
        member_harness_ids=tuple(sorted((actor.harness_id, recipient.harness_id))),
        allowed_actions=("message.send",),
        allowed_resource_prefixes=("conversation:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=1,
        domain_revocation_epoch=1,
    )
    scope_decision = authorize(
        policy,
        actor,
        action=COLLABORATION_SCOPE_ISSUE_ACTION,
        resource=f"scope:{scope_id}",
        context=scopes.issuance_request(actor=actor, proposal=scope_proposal),
    )
    scope = scopes.issue(
        actor=actor,
        proposal=scope_proposal,
        authority=IssuanceAuthority(
            actor=actor,
            policy_decision_id=scope_decision,
        ),
    )
    event = new_event(
        domain_id=actor.domain_id,
        actor=actor,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={
            "text": "retained artifact reference",
            "authorization_context": scope.authorization_context(),
        },
        idempotency_key=f"retained-artifact-{uuid4()}",
        recipients=(recipient.harness_id,),
        released_artifacts=(binding,),
        retention_delete_at=datetime.now(UTC) + timedelta(hours=1),
    )
    MailboxService(store, collaboration_scopes=scopes).accept(event)
    reason = "delete after retained history expires"
    decision = authorize(
        policy,
        actor,
        action="artifact.delete",
        resource=artifact_id,
        context={"expected_revision": 1, "reason": reason},
    )
    with pytest.raises(ConflictError, match="retained corporate history"):
        service.delete(
            artifact_id,
            actor=actor,
            policy_decision_id=decision,
            expected_revision=1,
            reason=reason,
        )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE events SET retention_delete_at=?,legal_hold=1 WHERE event_id=?",
            (int(time.time()) - 1, event.event_id),
        )
    with pytest.raises(ConflictError, match="legally held event"):
        service.delete(
            artifact_id,
            actor=actor,
            policy_decision_id=decision,
            expected_revision=1,
            reason=reason,
        )
    with store.transaction() as connection:
        connection.execute("UPDATE events SET legal_hold=0 WHERE event_id=?", (event.event_id,))
    assert service.delete(
        artifact_id,
        actor=actor,
        policy_decision_id=decision,
        expected_revision=1,
        reason=reason,
    )["state"] == "deleted"
    with pytest.raises(AuthorizationError, match="download capability is invalid"):
        service.consume_download(token, actor=actor)


def test_same_plaintext_across_actors_has_no_dedup_identity_or_version_oracle(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    first, _ = identity_factory()
    second, _ = identity_factory()
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "objects", tmp_path / "secrets" / "dedup.key"),
    )
    policy = PolicyEngine(store)
    content = b"same bytes must not disclose another actor's object"
    digest = hashlib.sha256(content).hexdigest()
    results = []
    for index, actor in enumerate((first, second), start=1):
        context = {
            "actor": actor.audit_view(),
            "classification": Classification.C1_INTERNAL.value,
            "expected_digest": digest,
            "expected_size": len(content),
            "media_type": "application/octet-stream",
            "required_attachment": True,
        }
        reservation = service.reserve(
            actor=actor,
            idempotency_key=f"dedup-nondisclosure-{index}-{uuid4()}",
            expected_digest=digest,
            expected_size=len(content),
            media_type="application/octet-stream",
            classification=Classification.C1_INTERNAL,
            required_attachment=True,
            policy_decision_id=authorize(
                policy,
                actor,
                action="artifact.upload.reserve",
                resource="artifact:new",
                context=context,
            ),
        )
        uploaded = service.upload(
            reservation["reservation_id"],
            content,
            actor=actor,
            policy_decision_id=authorize(
                policy,
                actor,
                action="artifact.upload.bytes",
                resource=reservation["reservation_id"],
                context={"expected_digest": digest, "expected_size": len(content)},
            ),
        )
        results.append((reservation, uploaded))
    assert results[0][0]["object_key"] != results[1][0]["object_key"]
    assert results[0][1]["version"] != results[1][1]["version"]
    assert results[0][0]["reservation_id"] not in json.dumps(results[1])
    assert results[1][0]["reservation_id"] not in json.dumps(results[0])


def test_legal_hold_delete_race_has_one_revision_fenced_winner(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    service, actor, artifact_id, _release_decision = prepare_scanned_artifact(
        store,
        identity_factory,
        tmp_path,
    )
    policy = PolicyEngine(store)
    hold_reason = "race preservation"
    delete_reason = "race disposal"
    hold_decision = authorize(
        policy,
        actor,
        action="artifact.legal_hold.set",
        resource=artifact_id,
        context={"enabled": True, "expected_revision": 1, "reason": hold_reason},
    )
    delete_decision = authorize(
        policy,
        actor,
        action="artifact.delete",
        resource=artifact_id,
        context={"expected_revision": 1, "reason": delete_reason},
    )

    def hold():
        return service.set_legal_hold(
            artifact_id,
            actor=actor,
            policy_decision_id=hold_decision,
            expected_revision=1,
            reason=hold_reason,
            enabled=True,
        )

    def delete():
        return service.delete(
            artifact_id,
            actor=actor,
            policy_decision_id=delete_decision,
            expected_revision=1,
            reason=delete_reason,
        )

    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = (pool.submit(hold), pool.submit(delete))
        outcomes = [future.exception() or future.result() for future in futures]
    assert sum(isinstance(outcome, dict) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, ConflictError) for outcome in outcomes) == 1
    status = service.lifecycle_status(artifact_id, actor=actor)
    assert status["lifecycle_revision"] == 2
    assert (status["legal_hold"], status["lifecycle_state"]) in {
        (True, "active"),
        (False, "deleted"),
    }


def _reserve_for_quota_test(
    service: ArtifactService,
    policy: PolicyEngine,
    actor,
    *,
    key: str,
    size: int,
    digest: str | None = None,
    ttl_seconds: int = 3600,
):
    expected_digest = digest or hashlib.sha256(f"{actor.harness_id}:{key}".encode()).hexdigest()
    context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": expected_digest,
        "expected_size": size,
        "media_type": "application/octet-stream",
        "required_attachment": True,
    }
    return service.reserve(
        actor=actor,
        idempotency_key=key,
        expected_digest=expected_digest,
        expected_size=size,
        media_type="application/octet-stream",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        ttl_seconds=ttl_seconds,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.reserve",
            resource="artifact:new",
            context=context,
            add_entitlement=not bool(
                service.store.fetch_one(
                    """SELECT 1 FROM entitlements
                        WHERE domain_id=? AND principal_id=? AND action='artifact.upload.reserve'
                          AND resource_pattern='artifact:new'""",
                    (actor.domain_id, actor.principal_id),
                )
            ),
        ),
    )


def test_cumulative_actor_and_domain_artifact_quota_is_atomic_idempotent_and_nondisclosing(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    first, _ = identity_factory()
    second, _ = identity_factory(domain=first.domain_id)
    operations = OperationsPolicy(per_actor_artifact_bytes=10, per_domain_artifact_bytes=15)
    assert operations.artifact_deduplication == "disabled"
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "quota-objects", tmp_path / "quota.key"),
        operations_policy=operations,
    )
    policy = PolicyEngine(store)
    key = f"quota-idempotency-{uuid4()}"
    first_reservation = _reserve_for_quota_test(service, policy, first, key=key, size=10)
    duplicate = _reserve_for_quota_test(service, policy, first, key=key, size=10)
    assert duplicate["duplicate"] is True
    assert duplicate["reservation_id"] == first_reservation["reservation_id"]
    assert store.fetch_one("SELECT COUNT(*) AS count FROM artifact_byte_charges")["count"] == 1

    for actor, size in ((first, 1), (second, 6)):
        with pytest.raises(AuthorizationError) as denied:
            _reserve_for_quota_test(
                service,
                policy,
                actor,
                key=f"quota-denied-{uuid4()}",
                size=size,
            )
        assert str(denied.value) == "artifact byte quota exceeded"
    assert store.fetch_one(
        "SELECT 1 FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (second.principal_id,),
    ) is None

    _reserve_for_quota_test(service, policy, second, key=f"quota-ok-{uuid4()}", size=5)
    accounts = {
        (row["scope_type"], row["scope_id"]): int(row["used_bytes"])
        for row in store.fetch_all("SELECT scope_type,scope_id,used_bytes FROM artifact_byte_accounts")
    }
    assert accounts[("actor", first.principal_id)] == 10
    assert accounts[("actor", second.principal_id)] == 5
    assert accounts[("domain", first.domain_id)] == 15


def test_reservation_abort_crash_and_restart_recovery_release_quota_exactly_once(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, _ = identity_factory()

    class Clock:
        value = int(time.time())

        def __call__(self) -> int:
            return self.value

    clock = Clock()
    operations = OperationsPolicy(per_actor_artifact_bytes=10, per_domain_artifact_bytes=10)
    objects = FilesystemArtifactStore(tmp_path / "abort-objects", tmp_path / "abort.key")
    service = ArtifactService(store, objects, operations_policy=operations, clock=clock)
    policy = PolicyEngine(store)
    content = b"0123456789"
    key = f"abort-idempotency-{uuid4()}"
    reservation = _reserve_for_quota_test(
        service,
        policy,
        actor,
        key=key,
        size=len(content),
        digest=hashlib.sha256(content).hexdigest(),
        ttl_seconds=30,
    )
    service.upload(
        reservation["reservation_id"],
        content,
        actor=actor,
        policy_decision_id=authorize(
            policy,
            actor,
            action="artifact.upload.bytes",
            resource=reservation["reservation_id"],
            context={"expected_digest": hashlib.sha256(content).hexdigest(), "expected_size": len(content)},
        ),
    )
    abort_decision = authorize(
        policy,
        actor,
        action="artifact.upload.abort",
        resource=reservation["reservation_id"],
        context={"request_digest": reservation["request_digest"]},
    )

    def crash_after_unlink(stage: str) -> None:
        if stage == "after_reservation_objects_removed":
            raise RuntimeError("synthetic abort crash")

    with pytest.raises(RuntimeError, match="synthetic abort crash"):
        service.abort_reservation(
            reservation["reservation_id"],
            actor=actor,
            policy_decision_id=abort_decision,
            phase_hook=crash_after_unlink,
        )
    assert store.fetch_one(
        "SELECT state FROM artifact_byte_charges WHERE reservation_id=?",
        (reservation["reservation_id"],),
    )["state"] == "release_pending"
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='domain' AND scope_id=?",
        (actor.domain_id,),
    )["used_bytes"] == 10

    restarted = ArtifactService(store, objects, operations_policy=operations, clock=clock)
    assert restarted.reconcile_quota_accounting() == {"actor_accounts": 1, "domain_accounts": 1}
    recovered = restarted.recover_expired_reservations()
    assert len(recovered) == 1
    assert recovered[0]["reservation_id"] == reservation["reservation_id"]
    assert recovered[0]["state"] == "aborted"
    assert recovered[0]["duplicate"] is False
    assert restarted.process_reservation_release(reservation["reservation_id"])["duplicate"] is True
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='domain' AND scope_id=?",
        (actor.domain_id,),
    )["used_bytes"] == 0
    assert _reserve_for_quota_test(
        service,
        policy,
        actor,
        key=key,
        size=10,
        digest=hashlib.sha256(content).hexdigest(),
    )["state"] == "aborted"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM artifact_byte_charges")["count"] == 1


def test_expired_reservation_and_deletion_recovery_do_not_leak_or_double_release_quota(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actor, _ = identity_factory()

    class Clock:
        value = int(time.time())

        def __call__(self) -> int:
            return self.value

    clock = Clock()
    operations = OperationsPolicy(per_actor_artifact_bytes=20, per_domain_artifact_bytes=20)
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "expiry-objects", tmp_path / "expiry.key"),
        operations_policy=operations,
        clock=clock,
    )
    policy = PolicyEngine(store)
    expired = _reserve_for_quota_test(
        service,
        policy,
        actor,
        key=f"expiry-{uuid4()}",
        size=7,
        ttl_seconds=30,
    )
    clock.value += 31
    assert service.recover_expired_reservations()[0]["state"] == "expired"
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (actor.principal_id,),
    )["used_bytes"] == 0
    assert service.recover_expired_reservations() == []
    assert service.process_reservation_release(expired["reservation_id"])["duplicate"] is True

    scanned, owner, artifact_id, release_decision = prepare_scanned_artifact(
        store, identity_factory, tmp_path / "deleted"
    )
    scanned.release(artifact_id, actor=owner, policy_decision_id=release_decision)
    manifest = store.fetch_one(
        """SELECT m.reservation_id,m.size,r.actor_id,r.domain_id
             FROM artifact_manifests m JOIN artifact_reservations r USING(reservation_id)
            WHERE m.artifact_id=?""",
        (artifact_id,),
    )
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (manifest["actor_id"],),
    )["used_bytes"] == manifest["size"]
    reason = "retention elapsed"
    delete_decision = authorize(
        PolicyEngine(store),
        owner,
        action="artifact.delete",
        resource=artifact_id,
        context={"expected_revision": 1, "reason": reason},
    )

    def crash_after_unlink(stage: str) -> None:
        if stage == "after_deletion_object_removed":
            raise RuntimeError("synthetic deletion crash")

    with pytest.raises(RuntimeError, match="synthetic deletion crash"):
        scanned.delete(
            artifact_id,
            actor=owner,
            policy_decision_id=delete_decision,
            expected_revision=1,
            reason=reason,
            phase_hook=crash_after_unlink,
        )
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (manifest["actor_id"],),
    )["used_bytes"] == manifest["size"]
    assert scanned.recover_deletion_outbox()[0]["state"] == "deleted"
    assert scanned.process_deletion_outbox(artifact_id)["duplicate"] is True
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='actor' AND scope_id=?",
        (manifest["actor_id"],),
    )["used_bytes"] == 0


def test_concurrent_domain_quota_race_has_one_charged_winner(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    actors = (identity_factory()[0], identity_factory()[0])
    operations = OperationsPolicy(per_actor_artifact_bytes=10, per_domain_artifact_bytes=10)
    service = ArtifactService(
        store,
        FilesystemArtifactStore(tmp_path / "race-objects", tmp_path / "race.key"),
        operations_policy=operations,
    )
    policy = PolicyEngine(store)

    def reserve(actor):
        return _reserve_for_quota_test(
            service,
            policy,
            actor,
            key=f"quota-race-{actor.harness_id}",
            size=10,
        )

    with ThreadPoolExecutor(max_workers=2) as executor:
        futures = [executor.submit(reserve, actor) for actor in actors]
        outcomes = []
        for future in futures:
            try:
                outcomes.append(future.result())
            except AuthorizationError as exc:
                outcomes.append(str(exc))
    assert sum(isinstance(outcome, dict) for outcome in outcomes) == 1
    assert outcomes.count("artifact byte quota exceeded") == 1
    assert store.fetch_one("SELECT COUNT(*) AS count FROM artifact_byte_charges")["count"] == 1
    assert store.fetch_one(
        "SELECT used_bytes FROM artifact_byte_accounts WHERE scope_type='domain' AND scope_id=?",
        (actors[0].domain_id,),
    )["used_bytes"] == 10
