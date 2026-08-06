from __future__ import annotations

import hashlib
import time
from datetime import UTC, datetime
from pathlib import Path
from types import SimpleNamespace
from uuid import NAMESPACE_URL, uuid5

import pytest

from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.artifacts.transfer import ArtifactTransferService
from agentnet.errors import (
    AuthorizationError,
    ConflictError,
    GateBlocked,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.mailbox.service import MailboxService
from agentnet.protocol.models import Classification, ReleasedArtifactBinding
from agentnet.security.signatures import canonical_digest, canonical_json


class InjectedTransferCrash(RuntimeError):
    pass


class FakeScopes:
    def __init__(self, store, sender, recipients) -> None:
        self.store = store
        self.scope_id = "collaboration-scope-artifact-tests"
        self.members = tuple(sorted((sender.harness_id, *(actor.harness_id for actor in recipients))))
        now = int(time.time())
        self.scope_digest = canonical_digest(
            {"scope_id": self.scope_id, "members": self.members}
        )
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO collaboration_scopes(
                    scope_id,schema_version,domain_id,scope_kind,owner_principal_id,owner_harness_id,
                    source_communication_scope_id,state,state_reason,allowed_actions_json,
                    allowed_resource_prefixes_json,allowed_classifications_json,canonical_references_json,
                    policy_floor,policy_revision,domain_revocation_epoch,control_sequence,membership_sequence,
                    proposal_digest,scope_digest,audit_record_hash,revision,created_at,updated_at
                ) VALUES(?,1,?,'shared',?,?,NULL,'active','test_active',?,?,?,?,1,1,1,1,1,?,?,?,1,?,?)""",
                (
                    self.scope_id,
                    sender.domain_id,
                    sender.principal_id,
                    sender.harness_id,
                    canonical_json(
                        ["artifact.download", "artifact.send", "message.read"]
                    ).decode(),
                    canonical_json(["artifact:", "conversation:"]).decode(),
                    canonical_json([Classification.C1_INTERNAL.value]).decode(),
                    canonical_json({}).decode(),
                    "a" * 64,
                    self.scope_digest,
                    "b" * 64,
                    now,
                    now,
                ),
            )
            for ordinal, actor in enumerate((sender, *recipients), start=1):
                connection.execute(
                    """INSERT INTO collaboration_scope_members(
                        scope_id,authority_kind,authority_id,harness_id,role,state,
                        joined_sequence,removed_sequence,member_digest,joined_at,removed_at
                    ) VALUES(?,'principal',?,?,?,'active',?,NULL,?, ?,NULL)""",
                    (
                        self.scope_id,
                        actor.principal_id,
                        actor.harness_id,
                        "owner" if actor.harness_id == sender.harness_id else "member",
                        ordinal,
                        canonical_digest({"scope_id": self.scope_id, "harness_id": actor.harness_id}),
                        now,
                    ),
                )

    def _scope(self):
        authorization_context = {
            "collaboration_scope_id": self.scope_id,
            "collaboration_scope_revision": 1,
            "collaboration_scope_policy_revision": 1,
            "collaboration_scope_domain_revocation_epoch": 1,
            "collaboration_scope_member_harness_ids": list(self.members),
            "collaboration_scope_digest": self.scope_digest,
        }
        return SimpleNamespace(
            scope_id=self.scope_id,
            domain_id="corp.example",
            member_harness_ids=self.members,
            revision=1,
            policy_revision=1,
            domain_revocation_epoch=1,
            scope_digest=self.scope_digest,
            state="active",
            allowed_actions=("artifact.download", "artifact.send", "message.read"),
            allowed_resource_prefixes=("artifact:", "conversation:"),
            allowed_classifications=(Classification.C1_INTERNAL.value,),
            authorization_context=lambda: authorization_context,
        )

    def require(
        self,
        *,
        actor,
        scope_id,
        action,
        resource,
        target_harness_ids,
        classification,
        when=None,
    ):
        del resource, classification, when
        if scope_id != self.scope_id or action not in {
            "artifact.send",
            "artifact.download",
            "message.read",
        }:
            raise AuthorizationError("collaboration scope is unavailable")
        if actor.harness_id not in self.members or not set(target_harness_ids).issubset(self.members):
            raise AuthorizationError("collaboration scope is unavailable")
        return self._scope()

    def get_for_actor(self, *, actor, scope_id, when=None):
        del when
        if scope_id != self.scope_id or actor.harness_id not in self.members:
            raise AuthorizationError("collaboration scope is unavailable")
        return self._scope()

    def require_in_transaction(self, connection, **request):
        del connection
        return self.require(**request)


class FakeArtifacts:
    def __init__(self, store, operations: list[str]) -> None:
        self.store = store
        self.operations = operations
        self.contents: dict[str, bytes] = {}
        self.tokens: dict[str, tuple[str, str, bool]] = {}

    def reserve(
        self,
        *,
        actor,
        idempotency_key,
        expected_digest,
        expected_size,
        media_type,
        classification,
        required_attachment,
        policy_decision_id,
        ttl_seconds=3600,
    ):
        del policy_decision_id
        existing = self.store.fetch_one(
            "SELECT * FROM artifact_reservations WHERE domain_id=? AND actor_id=? AND idempotency_key=?",
            (actor.domain_id, actor.principal_id, idempotency_key),
        )
        request_digest = canonical_digest(
            {
                "expected_digest": expected_digest,
                "expected_size": expected_size,
                "media_type": media_type,
                "classification": classification.value,
                "required_attachment": required_attachment,
            }
        )
        if existing is not None:
            if existing["request_digest"] != request_digest:
                raise IdempotencyConflict("artifact reservation changed")
            return dict(existing) | {"duplicate": True}
        reservation_id = str(uuid5(NAMESPACE_URL, f"test-reservation:{actor.harness_id}:{idempotency_key}"))
        object_key = hashlib.sha256(reservation_id.encode()).hexdigest()[:32]
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO artifact_reservations(
                    reservation_id,domain_id,actor_id,actor_json,idempotency_key,request_digest,object_key,
                    expected_digest,expected_size,media_type,classification,object_version,
                    required_attachment,state,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?, 'upload_reserved',?)""",
                (
                    reservation_id,
                    actor.domain_id,
                    actor.principal_id,
                    canonical_json(actor.audit_view()).decode(),
                    idempotency_key,
                    request_digest,
                    object_key,
                    expected_digest,
                    expected_size,
                    media_type,
                    classification.value,
                    None,
                    int(required_attachment),
                    int(time.time()) + ttl_seconds,
                ),
            )
        self.operations.append("reserve")
        return {
            "reservation_id": reservation_id,
            "object_key": object_key,
            "request_digest": request_digest,
            "state": "upload_reserved",
            "duplicate": False,
        }

    def upload(self, reservation_id, content, *, actor, policy_decision_id):
        del actor, policy_decision_id
        row = self.store.fetch_one(
            "SELECT * FROM artifact_reservations WHERE reservation_id=?", (reservation_id,)
        )
        assert hashlib.sha256(content).hexdigest() == row["expected_digest"]
        version = hashlib.sha256(b"encrypted:" + content).hexdigest()
        if row["state"] == "upload_reserved":
            with self.store.transaction() as connection:
                connection.execute(
                    "UPDATE artifact_reservations SET state='object_verified',object_version=? WHERE reservation_id=?",
                    (version, reservation_id),
                )
            self.contents[reservation_id] = content
            self.operations.append("upload")
            return {"reservation_id": reservation_id, "version": version, "state": "object_verified"}
        return {"reservation_id": reservation_id, "version": row["object_version"], "state": row["state"], "duplicate": True}

    def promote_manifest(
        self,
        *,
        reservation_id,
        object_version,
        provenance,
        actor,
        policy_decision_id,
        derivation=None,
    ):
        del provenance, actor, policy_decision_id, derivation
        existing = self.store.fetch_one(
            "SELECT * FROM artifact_manifests WHERE reservation_id=?", (reservation_id,)
        )
        if existing is not None:
            return dict(existing) | {"duplicate": True}
        reservation = self.store.fetch_one(
            "SELECT * FROM artifact_reservations WHERE reservation_id=?", (reservation_id,)
        )
        artifact_id = str(uuid5(NAMESPACE_URL, f"test-artifact:{reservation_id}"))
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO artifact_manifests(
                    artifact_id,reservation_id,domain_id,object_key,object_version,ciphertext_digest,
                    plaintext_digest_encrypted,size,media_type,classification,state,provenance_json,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,'quarantined','{}',?)""",
                (
                    artifact_id,
                    reservation_id,
                    reservation["domain_id"],
                    reservation["object_key"],
                    object_version,
                    object_version,
                    self.store.cipher.encrypt_json(
                        reservation["expected_digest"], purpose=f"artifact-digest:{artifact_id}"
                    ),
                    reservation["expected_size"],
                    reservation["media_type"],
                    reservation["classification"],
                    int(time.time()),
                ),
            )
            connection.execute(
                """INSERT INTO artifact_lifecycle(artifact_id,revision,status,updated_at)
                     VALUES(?,1,'active',?)""",
                (artifact_id, int(time.time())),
            )
            connection.execute(
                "UPDATE artifact_reservations SET state='manifest_committed' WHERE reservation_id=?",
                (reservation_id,),
            )
        self.operations.append("manifest")
        return {"artifact_id": artifact_id, "state": "quarantined", "duplicate": False}

    def release(self, artifact_id, *, actor, policy_decision_id, phase_hook=None):
        del phase_hook
        row = self.store.fetch_one(
            "SELECT state FROM artifact_manifests WHERE artifact_id=?",
            (artifact_id,),
        )
        if row["state"] == "released":
            return {"artifact_id": artifact_id, "state": "released", "duplicate": True}
        if row["state"] != "scan_passed":
            raise AuthorizationError("artifact release requirements are not satisfied")
        now = int(time.time())
        intent_id = str(uuid5(NAMESPACE_URL, f"test-release:{artifact_id}"))
        outbox_id = str(uuid5(NAMESPACE_URL, f"test-release-outbox:{artifact_id}"))
        actor_json = canonical_json(actor.audit_view()).decode()
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO audit_intents(
                    intent_id,action,resource_id,actor_json,policy_decision_id,
                    request_digest,state,created_at,completed_at
                ) VALUES(?,?,?,?,?,?,'completed',?,?)""",
                (
                    intent_id,
                    "artifact.release",
                    artifact_id,
                    actor_json,
                    policy_decision_id,
                    canonical_digest({"artifact_id": artifact_id}),
                    now,
                    now,
                ),
            )
            manifest = connection.execute(
                "SELECT object_key,object_version FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            connection.execute(
                """INSERT INTO artifact_release_outbox(
                    outbox_id,artifact_id,intent_id,object_key,object_version,actor_json,
                    policy_decision_id,state,attempts,last_error,created_at,updated_at,completed_at
                ) VALUES(?,?,?,?,?,?,?,'completed',1,NULL,?,?,?)""",
                (
                    outbox_id,
                    artifact_id,
                    intent_id,
                    manifest["object_key"],
                    manifest["object_version"],
                    actor_json,
                    policy_decision_id,
                    now,
                    now,
                    now,
                ),
            )
            connection.execute(
                "UPDATE artifact_manifests SET state='released' WHERE artifact_id=?",
                (artifact_id,),
            )
        self.operations.append("release")
        return {"artifact_id": artifact_id, "state": "released", "duplicate": False}

    def process_release_outbox(self, artifact_id):
        return self.release(artifact_id, actor=None, policy_decision_id="reconcile")

    def resolve_released_binding(self, artifact_id):
        row = self.store.fetch_one(
            """SELECT m.*,r.expected_digest,o.intent_id AS release_intent_id,
                      o.completed_at AS released_at
                 FROM artifact_manifests m
                 JOIN artifact_reservations r ON r.reservation_id=m.reservation_id
                 JOIN artifact_release_outbox o ON o.artifact_id=m.artifact_id
                WHERE m.artifact_id=? AND o.state='completed'""",
            (artifact_id,),
        )
        if row is None or row["state"] != "released":
            raise AuthorizationError("artifact is unavailable")
        self.operations.append("resolve")
        return ReleasedArtifactBinding(
            artifact_id=artifact_id,
            domain_id=row["domain_id"],
            object_version=row["object_version"],
            size=int(row["size"]),
            media_type=row["media_type"],
            classification=Classification(row["classification"]),
            release_intent_id=row["release_intent_id"],
            released_at=datetime.fromtimestamp(int(row["released_at"]), UTC),
        )

    def issue_download_capability(
        self,
        artifact_id,
        *,
        actor,
        audience_harness_id,
        policy_decision_id,
        ttl_seconds=60,
    ):
        del policy_decision_id, ttl_seconds
        if actor.harness_id != audience_harness_id:
            raise AuthorizationError("download audience mismatch")
        token = f"token-{len(self.tokens):016d}"
        self.tokens[token] = (artifact_id, audience_harness_id, False)
        return token

    def consume_download(self, token, *, actor):
        artifact_id, audience, consumed = self.tokens[token]
        if consumed or audience != actor.harness_id:
            raise AuthorizationError("download capability is invalid")
        self.tokens[token] = (artifact_id, audience, True)
        row = self.store.fetch_one(
            "SELECT reservation_id FROM artifact_manifests WHERE artifact_id=?", (artifact_id,)
        )
        return self.contents[row["reservation_id"]]


class FakeScanner:
    def __init__(self, store, operations: list[str], *, outcome: str = "allow") -> None:
        self.store = store
        self.operations = operations
        self.outcome = outcome

    def process_once(self, *, limit=25):
        del limit
        row = self.store.fetch_one(
            "SELECT artifact_id FROM artifact_manifests WHERE state='quarantined' ORDER BY artifact_id LIMIT 1"
        )
        if row is None:
            return ()
        if self.outcome == "failure":
            raise GateBlocked("scanner_unavailable", "scanner is unavailable")
        if self.outcome == "stale":
            raise GateBlocked("scanner_stale", "scanner evidence is stale")
        state = "scan_passed" if self.outcome == "allow" else "held"
        with self.store.transaction() as connection:
            connection.execute(
                "UPDATE artifact_manifests SET state=? WHERE artifact_id=?",
                (state, row["artifact_id"]),
            )
        self.operations.append("scan")
        return (row["artifact_id"],)


class RecordingMailbox:
    def __init__(self, mailbox, operations: list[str]) -> None:
        self.mailbox = mailbox
        self.store = mailbox.store
        self.operations = operations

    def accept(self, event):
        result = self.mailbox.accept(event)
        if not result["duplicate"]:
            self.operations.append("event")
        return result


class CrashOnce:
    def __init__(self, phase: str) -> None:
        self.phase = phase
        self.triggered = False

    def __call__(self, phase: str) -> None:
        if phase == self.phase and not self.triggered:
            self.triggered = True
            raise InjectedTransferCrash(phase)


def _transfer_fixture(store, identity_factory, tmp_path: Path, *, scanner_outcome="allow", phase_hook=None):
    sender, _ = identity_factory()
    recipient, _ = identity_factory()
    sibling, _ = identity_factory(principal_id=recipient.principal_id)
    operations: list[str] = []
    artifacts = FakeArtifacts(store, operations)
    scopes = FakeScopes(store, sender, (recipient,))
    scanner = FakeScanner(store, operations, outcome=scanner_outcome)
    mailbox = RecordingMailbox(
        MailboxService(store, collaboration_scopes=scopes),
        operations,
    )
    conversations = SimpleNamespace(store=store, mailbox=mailbox)
    root = tmp_path / "downloads"
    root.mkdir(mode=0o700)
    service = ArtifactTransferService(
        store,
        artifacts,
        scanner,
        scopes,
        conversations,
        authorize=lambda **request: SimpleNamespace(
            decision_id=canonical_digest(
                {
                    "action": request["action"],
                    "resource": request["resource"],
                    "context": request.get("context") or {},
                }
            )
        ),
        destination=SafeDownloadDestination(root),
        phase_hook=phase_hook,
    )
    source = tmp_path / "source.txt"
    source.write_bytes(b"immutable transfer bytes")
    source.chmod(0o600)
    return SimpleNamespace(
        service=service,
        sender=sender,
        recipient=recipient,
        sibling=sibling,
        source=source,
        root=root,
        artifacts=artifacts,
        scanner=scanner,
        scopes=scopes,
        conversations=conversations,
        operations=operations,
    )


def _send(fixture):
    return fixture.service.send_file(
        collaboration_scope_id=fixture.scopes.scope_id,
        actor=fixture.sender,
        recipients=(fixture.recipient.harness_id,),
        source=fixture.source,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        idempotency_key="artifact-transfer-test-0001",
    )


def test_file_sent_only_after_policy_release_and_exact_recipient_custody(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)

    result = _send(fixture)

    assert result["state"] == "recipient_committed"
    assert result["recipient_harness_ids"] == (fixture.recipient.harness_id,)
    assert result["recipient_states"] == {fixture.recipient.harness_id: "recipient_committed"}
    assert fixture.operations == ["reserve", "upload", "manifest", "scan", "release", "resolve", "event"]
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1


def test_duplicate_send_is_idempotent_and_never_commits_a_second_event(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)

    first = _send(fixture)
    second = _send(fixture)

    assert second["duplicate"] is True
    assert second["transfer_id"] == first["transfer_id"]
    assert second["artifact_id"] == first["artifact_id"]
    assert second["event_id"] == first["event_id"]
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1


def test_send_rejects_symlink_source_before_reservation(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    real_source = tmp_path / "real-source.txt"
    fixture.source.rename(real_source)
    fixture.source.symlink_to(real_source)

    with pytest.raises(ValidationError, match="source"):
        _send(fixture)

    assert store.fetch_one("SELECT COUNT(*) AS count FROM artifact_transfers")["count"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0


def test_same_send_key_with_changed_source_is_rejected(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    _send(fixture)
    fixture.source.write_bytes(b"different bytes")

    with pytest.raises(IdempotencyConflict):
        _send(fixture)


@pytest.mark.parametrize(
    "phase",
    ("after_reserve", "after_upload", "after_manifest", "after_scan", "after_release", "after_event"),
)
def test_crash_restart_reconciles_without_duplicate_event(
    store,
    identity_factory,
    tmp_path: Path,
    phase: str,
) -> None:
    crash = CrashOnce(phase)
    fixture = _transfer_fixture(store, identity_factory, tmp_path, phase_hook=crash)

    with pytest.raises(InjectedTransferCrash, match=phase):
        _send(fixture)

    restarted = ArtifactTransferService(
        store,
        fixture.artifacts,
        fixture.scanner,
        fixture.scopes,
        fixture.conversations,
        authorize=fixture.service.authorize,
        destination=SafeDownloadDestination(fixture.root),
    )
    result = restarted.send_file(
        collaboration_scope_id=fixture.scopes.scope_id,
        actor=fixture.sender,
        recipients=(fixture.recipient.harness_id,),
        source=fixture.source,
        media_type="text/plain",
        classification=Classification.C1_INTERNAL,
        idempotency_key="artifact-transfer-test-0001",
    )

    assert result["state"] == "recipient_committed"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1


@pytest.mark.parametrize("scanner_outcome", ("failure", "stale", "deny"))
def test_scanner_failure_or_staleness_never_releases_or_commits_custody(
    store,
    identity_factory,
    tmp_path: Path,
    scanner_outcome: str,
) -> None:
    fixture = _transfer_fixture(
        store,
        identity_factory,
        tmp_path,
        scanner_outcome=scanner_outcome,
    )

    with pytest.raises((AuthorizationError, GateBlocked)):
        _send(fixture)

    assert "release" not in fixture.operations
    assert "event" not in fixture.operations
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0


def test_transfer_status_is_exact_harness_scoped_and_non_enumerating(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    sent = _send(fixture)

    recipient_status = fixture.service.status(
        collaboration_scope_id=fixture.scopes.scope_id,
        actor=fixture.recipient,
        transfer_id=sent["transfer_id"],
    )

    with pytest.raises(AuthorizationError, match="unavailable"):
        fixture.service.status(
            actor=fixture.recipient,
            collaboration_scope_id="scope:substituted-transfer",
            transfer_id=sent["transfer_id"],
        )
    assert recipient_status["artifact_id"] == sent["artifact_id"]

    with pytest.raises(AuthorizationError, match="unavailable"):
        fixture.service.status(
            actor=fixture.sibling,
            collaboration_scope_id=fixture.scopes.scope_id,
            transfer_id=sent["transfer_id"],
        )


def test_download_rechecks_exact_recipient_and_consumes_a_bounded_capability_once(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    sent = _send(fixture)
    destination = fixture.root / "received.txt"

    with pytest.raises(AuthorizationError, match="unavailable"):
        fixture.service.download_file(
            actor=fixture.recipient,
            collaboration_scope_id="scope:substituted-transfer",
            artifact_id=sent["artifact_id"],
            destination=destination,
            idempotency_key="artifact-download-wrong-scope-0001",
        )

    with pytest.raises(AuthorizationError, match="unavailable"):
        fixture.service.download_file(
            collaboration_scope_id=fixture.scopes.scope_id,
            actor=fixture.sibling,
            artifact_id=sent["artifact_id"],
            destination=destination,
            idempotency_key="artifact-download-test-0001",
        )

    downloaded = fixture.service.download_file(
        collaboration_scope_id=fixture.scopes.scope_id,
        actor=fixture.recipient,
        artifact_id=sent["artifact_id"],
        destination=destination,
        idempotency_key="artifact-download-test-0001",
    )
    assert downloaded["state"] == "materialized"
    assert downloaded["plaintext_digest"] == hashlib.sha256(destination.read_bytes()).hexdigest()
    assert all(consumed for _artifact, _audience, consumed in fixture.artifacts.tokens.values())

    replayed = fixture.service.download_file(
        collaboration_scope_id=fixture.scopes.scope_id,
        actor=fixture.recipient,
        artifact_id=sent["artifact_id"],
        destination=destination,
        idempotency_key="artifact-download-test-0001",
    )
    assert replayed | {"duplicate": False} == downloaded
    assert replayed["duplicate"] is True
    assert len(fixture.artifacts.tokens) == 1


def test_download_digest_mismatch_consumes_no_bytes_at_destination(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    sent = _send(fixture)
    manifest = store.fetch_one(
        "SELECT reservation_id FROM artifact_manifests WHERE artifact_id=?",
        (sent["artifact_id"],),
    )
    fixture.artifacts.contents[manifest["reservation_id"]] = b"substituted bytes"
    destination = fixture.root / "received.txt"

    with pytest.raises(ConflictError, match="immutable transfer"):
        fixture.service.download_file(
            collaboration_scope_id=fixture.scopes.scope_id,
            actor=fixture.recipient,
            artifact_id=sent["artifact_id"],
            destination=destination,
            idempotency_key="artifact-download-digest-0001",
        )

    assert not destination.exists()
    assert all(consumed for _artifact, _audience, consumed in fixture.artifacts.tokens.values())


def test_non_active_artifact_lifecycle_blocks_download_before_capability(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    fixture = _transfer_fixture(store, identity_factory, tmp_path)
    sent = _send(fixture)
    with store.transaction() as connection:
        connection.execute(
            """UPDATE artifact_lifecycle
                  SET status='deletion_pending',revision=revision+1,updated_at=?
                WHERE artifact_id=?""",
            (int(time.time()), sent["artifact_id"]),
        )

    with pytest.raises(AuthorizationError, match="unavailable"):
        fixture.service.download_file(
            collaboration_scope_id=fixture.scopes.scope_id,
            actor=fixture.recipient,
            artifact_id=sent["artifact_id"],
            destination=fixture.root / "received.txt",
            idempotency_key="artifact-download-lifecycle-0001",
        )

    assert fixture.artifacts.tokens == {}
