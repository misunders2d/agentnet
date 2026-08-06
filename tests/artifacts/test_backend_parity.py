from __future__ import annotations

import hashlib
import os
import time
from collections.abc import Iterator
from pathlib import Path
from uuid import uuid4

import psycopg
import pytest

from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
    PolicyEngine,
)
from agentnet.errors import AuthorizationError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.storage.postgres import PostgreSQLStore
from agentnet.storage.sqlite import SQLiteStore


_POSTGRES_PREREQUISITE = (
    "requires AGENTNET_TEST_POSTGRES_URL and "
    "AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1 for a dedicated PostgreSQL test database"
)


@pytest.fixture(params=("sqlite", "postgresql"), ids=("sqlite-real", "postgresql-dedicated"))
def backend_store(request: pytest.FixtureRequest, tmp_path: Path) -> Iterator[StoreBackend]:
    cipher = LocalEnvelopeCipher(b"b" * 32)
    if request.param == "sqlite":
        store = SQLiteStore(tmp_path / "artifact-parity.sqlite3", cipher)
        try:
            yield store
        finally:
            store.close()
        return

    database_url = os.environ.get("AGENTNET_TEST_POSTGRES_URL")
    if not database_url or os.environ.get("AGENTNET_TEST_POSTGRES_ALLOW_MUTATION") != "1":
        pytest.skip(_POSTGRES_PREREQUISITE)
    schema = f"agentnet_artifact_parity_{uuid4().hex}"
    administrator = psycopg.connect(database_url, autocommit=True)
    administrator.execute(
        psycopg.sql.SQL("CREATE SCHEMA {}").format(psycopg.sql.Identifier(schema))
    )
    separator = "&" if "?" in database_url else "?"
    isolated_url = (
        f"{database_url}{separator}options="
        f"-csearch_path%3D{schema}%20-cclient_encoding%3DUTF8"
    )
    store = None
    try:
        store = PostgreSQLStore(
            isolated_url,
            cipher,
            instance_id=f"artifact-parity-{uuid4().hex}",
            lease_ttl_seconds=300,
            start_lease_keeper=False,
        )
        yield store
    finally:
        if store is not None:
            store.close()
        administrator.execute(
            psycopg.sql.SQL("DROP SCHEMA {} CASCADE").format(
                psycopg.sql.Identifier(schema)
            )
        )
        administrator.close()


def _identity(store: StoreBackend, *, suffix: str) -> VerifiedActor:
    now = int(time.time())
    principal_id = f"principal-{suffix}"
    harness_id = f"harness-{suffix}"
    credential_id = f"credential-{suffix}"
    key = P256KeyPair.generate()
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO domains(domain_id,status,created_at)
               VALUES(?,'active',?) ON CONFLICT(domain_id) DO NOTHING""",
            ("corp.example", now),
        )
        connection.execute(
            """INSERT INTO principals(
                   principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
               ) VALUES(?,?,?,?,?,'active',?)""",
            (
                principal_id,
                "corp.example",
                "https://idp.example",
                f"subject-{suffix}",
                f"{suffix}@example.test",
                now,
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,kind,display_name,status,
                   binding_assurance,capabilities_json,created_at
               ) VALUES(?,?,?,?,?,'active','lab',?,?)""",
            (
                harness_id,
                "corp.example",
                principal_id,
                "codex",
                f"codex-{suffix}",
                canonical_json({}).decode("utf-8"),
                now,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,'active',1,?,?)""",
            (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 3_600),
        )
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id=principal_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="lab",
    )


def _authorize(
    policy: PolicyEngine,
    actor: VerifiedActor,
    *,
    action: str,
    resource: str,
    context: dict[str, object] | None = None,
) -> str:
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


def _release_bytes(
    store: StoreBackend,
    tmp_path: Path,
    *,
    content: bytes,
) -> tuple[ArtifactService, VerifiedActor, VerifiedActor, str]:
    sender = _identity(store, suffix=f"sender-{uuid4().hex[:10]}")
    recipient = _identity(store, suffix=f"recipient-{uuid4().hex[:10]}")
    scanner_key = P256KeyPair.generate()
    service = ArtifactService(
        store,
        FilesystemArtifactStore(
            tmp_path / f"objects-{uuid4().hex}",
            tmp_path / f"artifact-key-{uuid4().hex}",
        ),
        trusted_scanner_keys={"backend-parity-scanner": scanner_key.public_pem},
    )
    policy = PolicyEngine(store)
    expected_digest = hashlib.sha256(content).hexdigest()
    reserve_context = {
        "actor": sender.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": expected_digest,
        "expected_size": len(content),
        "media_type": "application/octet-stream",
        "required_attachment": True,
    }
    reserve_decision = _authorize(
        policy,
        sender,
        action="artifact.upload.reserve",
        resource="artifact:new",
        context=reserve_context,
    )
    idempotency_key = f"artifact-parity-{uuid4()}"
    reservation = service.reserve(
        actor=sender,
        idempotency_key=idempotency_key,
        expected_digest=expected_digest,
        expected_size=len(content),
        media_type="application/octet-stream",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=reserve_decision,
    )
    duplicate_reservation = service.reserve(
        actor=sender,
        idempotency_key=idempotency_key,
        expected_digest=expected_digest,
        expected_size=len(content),
        media_type="application/octet-stream",
        classification=Classification.C1_INTERNAL,
        required_attachment=True,
        policy_decision_id=reserve_decision,
    )
    assert duplicate_reservation["duplicate"] is True
    assert duplicate_reservation["reservation_id"] == reservation["reservation_id"]

    upload_decision = _authorize(
        policy,
        sender,
        action="artifact.upload.bytes",
        resource=reservation["reservation_id"],
        context={"expected_digest": expected_digest, "expected_size": len(content)},
    )
    uploaded = service.upload(
        reservation["reservation_id"],
        content,
        actor=sender,
        policy_decision_id=upload_decision,
    )
    duplicate_upload = service.upload(
        reservation["reservation_id"],
        content,
        actor=sender,
        policy_decision_id=upload_decision,
    )
    assert duplicate_upload["duplicate"] is True
    assert duplicate_upload["version"] == uploaded["version"]

    manifest = service.promote_manifest(
        reservation_id=reservation["reservation_id"],
        object_version=uploaded["version"],
        provenance={"origin": "backend-parity"},
        actor=sender,
        policy_decision_id=_authorize(
            policy,
            sender,
            action="artifact.manifest.promote",
            resource=reservation["reservation_id"],
            context={
                "object_version": uploaded["version"],
                "request_digest": reservation["request_digest"],
            },
        ),
    )
    now = int(time.time())
    scan_fields = {
        "artifact_id": manifest["artifact_id"],
        "classification": Classification.C1_INTERNAL.value,
        "ciphertext_digest": uploaded["version"],
        "expires_at": now + 300,
        "issued_at": now,
        "object_key": reservation["object_key"],
        "object_version": uploaded["version"],
        "plaintext_digest": expected_digest,
        "policy_revision": 1,
        "profile_digest": "c" * 64,
        "scanner_engine": "backend-parity-engine",
        "scanner_id": "backend-parity-scanner",
        "scanner_key_epoch": 1,
        "scanner_version": "1",
        "rules_digest": "a" * 64,
        "result": "allow",
    }
    service.record_scan(
        manifest["artifact_id"],
        scan_fields
        | {
            "signature": scanner_key.sign(
                "agentnet.artifact.attestation.v1",
                scan_fields,
            )
        },
    )
    released = service.release(
        manifest["artifact_id"],
        actor=sender,
        policy_decision_id=_authorize(
            policy,
            sender,
            action="artifact.release",
            resource=manifest["artifact_id"],
        ),
    )
    assert released["state"] == "released"
    download_decision = _authorize(
        policy,
        recipient,
        action="artifact.download",
        resource=manifest["artifact_id"],
        context={"audience_harness_id": recipient.harness_id},
    )
    with pytest.raises(
        AuthorizationError,
        match="download audience must be the verified caller harness",
    ):
        service.issue_download_capability(
            manifest["artifact_id"],
            actor=sender,
            audience_harness_id=recipient.harness_id,
            policy_decision_id=download_decision,
        )
    token = service.issue_download_capability(
        manifest["artifact_id"],
        actor=recipient,
        audience_harness_id=recipient.harness_id,
        policy_decision_id=download_decision,
    )
    return service, sender, recipient, token


def test_reserve_upload_scan_release_download_is_backend_neutral(
    backend_store: StoreBackend,
    tmp_path: Path,
) -> None:
    content = b"\x00backend-neutral artifact\xff\n"
    service, sender, recipient, token = _release_bytes(
        backend_store,
        tmp_path,
        content=content,
    )

    with pytest.raises(AuthorizationError):
        service.consume_download(token, actor=sender)
    assert service.consume_download(token, actor=recipient) == content
    with pytest.raises(AuthorizationError):
        service.consume_download(token, actor=recipient)


def test_reservation_transaction_rolls_back_on_audit_failure(
    backend_store: StoreBackend,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    actor = _identity(backend_store, suffix=f"rollback-{uuid4().hex[:10]}")
    service = ArtifactService(
        backend_store,
        FilesystemArtifactStore(tmp_path / "rollback-objects", tmp_path / "rollback.key"),
    )
    policy = PolicyEngine(backend_store)
    content = b"rollback"
    digest = hashlib.sha256(content).hexdigest()
    context = {
        "actor": actor.audit_view(),
        "classification": Classification.C1_INTERNAL.value,
        "expected_digest": digest,
        "expected_size": len(content),
        "media_type": "application/octet-stream",
        "required_attachment": True,
    }
    decision = _authorize(
        policy,
        actor,
        action="artifact.upload.reserve",
        resource="artifact:new",
        context=context,
    )

    def fail_audit(_connection: object, _record: object) -> str:
        raise RuntimeError("injected audit failure")

    monkeypatch.setattr(backend_store, "append_audit", fail_audit)
    with pytest.raises(RuntimeError, match="injected audit failure"):
        service.reserve(
            actor=actor,
            idempotency_key=f"rollback-{uuid4()}",
            expected_digest=digest,
            expected_size=len(content),
            media_type="application/octet-stream",
            classification=Classification.C1_INTERNAL,
            required_attachment=True,
            policy_decision_id=decision,
        )
    assert backend_store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_reservations"
    )["count"] == 0
    assert backend_store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_byte_charges"
    )["count"] == 0
    assert backend_store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_byte_accounts"
    )["count"] == 0


@pytest.mark.parametrize(
    "catalog_mutation",
    ("malformed-version", "wrong-checksum", "missing-artifact-index"),
)
def test_malformed_schema_or_catalog_fails_before_artifact_access(
    backend_store: StoreBackend,
    tmp_path: Path,
    catalog_mutation: str,
) -> None:
    with backend_store.transaction() as connection:
        if catalog_mutation == "malformed-version":
            # Preserve the governance floor while making the catalog value
            # non-canonical for ArtifactService's exact schema-v7 check.
            connection.execute(
                "UPDATE metadata SET value=? WHERE key='schema_version'",
                (f"{CURRENT_SCHEMA_VERSION}-malformed",),
            )
        elif catalog_mutation == "wrong-checksum":
            catalog_table = (
                "installed_migration_catalog"
                if backend_store.backend_name == "sqlite"
                else "schema_migrations"
            )
            connection.execute(
                f"UPDATE {catalog_table} SET checksum=? WHERE version=?",
                ("0" * 64, CURRENT_SCHEMA_VERSION),
            )
        else:
            connection.execute("DROP INDEX idx_artifact_transfers_recovery")

    with pytest.raises(GateBlocked, match="schema/catalog"):
        ArtifactService(
            backend_store,
            FilesystemArtifactStore(
                tmp_path / f"blocked-objects-{catalog_mutation}",
                tmp_path / f"blocked-key-{catalog_mutation}",
            ),
        )


def test_unknown_store_backend_fails_closed_before_queries(
    store: SQLiteStore,
    tmp_path: Path,
) -> None:
    class UnknownBackend:
        backend_name = "unknown"
        cipher = store.cipher

        def fetch_one(self, _query: str, _parameters: tuple[object, ...] = ()) -> object:
            raise AssertionError("unknown backend must fail before catalog access")

        def fetch_all(self, _query: str, _parameters: tuple[object, ...] = ()) -> list[object]:
            raise AssertionError("unknown backend must fail before catalog access")

    with pytest.raises(GateBlocked, match="unsupported"):
        ArtifactService(
            UnknownBackend(),  # type: ignore[arg-type]
            FilesystemArtifactStore(tmp_path / "unknown-objects", tmp_path / "unknown.key"),
        )
