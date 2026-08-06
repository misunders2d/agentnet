"""Crash-recoverable exact-recipient artifact transfer coordination."""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import stat
import time
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from pathlib import Path
from typing import Any
from uuid import NAMESPACE_URL, uuid5

from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.artifacts.service import (
    CANONICAL_MEDIA_TYPE,
    MAX_ARTIFACT_BYTES,
    ArtifactService,
)
from agentnet.errors import (
    AuthorizationError,
    ConflictError,
    GateBlocked,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.identity.actors import VerifiedActor
from agentnet.messaging.events import envelope_digest, new_event, validate_event_digest
from agentnet.protocol.models import (
    Classification,
    EventEnvelope,
    EventType,
    ReleasedArtifactBinding,
)
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$")
_SHA256 = re.compile(r"^[a-f0-9]{64}$")
_TERMINAL_STATES = frozenset({"recipient_committed", "failed", "canceled"})


class TransferState(StrEnum):
    RESERVED = "reserved"
    QUARANTINED = "quarantined"
    SCANNING = "scanning"
    RELEASED = "released"
    EVENT_COMMITTED = "event_committed"
    RECIPIENT_COMMITTED = "recipient_committed"
    FAILED = "failed"
    CANCELED = "canceled"


class ArtifactTransferService:
    """Coordinate lifecycle gates and one immutable exact-recipient event.

    Every externally visible phase has a durable predecessor. Retries inspect
    authoritative lifecycle/event state rather than trusting a previous return
    value, and deterministic lifecycle/event idempotency keys converge after a
    lost response or process crash.
    """

    def __init__(
        self,
        store: StoreBackend,
        artifacts: ArtifactService,
        scanner: Any,
        collaboration_scopes: Any,
        conversations: Any,
        *,
        authorize: Callable[..., Any],
        destination: SafeDownloadDestination | None = None,
        clock: Callable[[], int] | None = None,
        phase_hook: Callable[[str], None] | None = None,
    ) -> None:
        if artifacts.store is not store:
            raise ValueError("artifact transfer and lifecycle services must share one store")
        if getattr(collaboration_scopes, "store", store) is not store:
            raise ValueError("artifact transfer and collaboration services must share one store")
        if getattr(conversations, "store", store) is not store:
            raise ValueError("artifact transfer and conversation services must share one store")
        mailbox = getattr(conversations, "mailbox", None)
        if mailbox is None or getattr(mailbox, "store", store) is not store:
            raise ValueError("artifact transfer requires the shared conversation mailbox")
        if getattr(scanner, "store", store) is not store:
            raise ValueError("artifact transfer and scanner worker must share one store")
        if not callable(authorize):
            raise ValueError("artifact transfer requires the controller authorization boundary")
        self.store = store
        self.artifacts = artifacts
        self.scanner = scanner
        self.collaboration_scopes = collaboration_scopes
        self.conversations = conversations
        self.authorize = authorize
        self.destination = destination
        self.clock = clock or (lambda: int(time.time()))
        self.phase_hook = phase_hook

    def _phase(self, name: str) -> None:
        if self.phase_hook is not None:
            self.phase_hook(name)

    @staticmethod
    def _require_actor(actor: VerifiedActor) -> None:
        if (
            actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.credential_epoch < 1
        ):
            raise AuthorizationError("artifact transfer requires a verified exact harness")

    @staticmethod
    def _require_idempotency_key(value: str) -> str:
        if (
            not isinstance(value, str)
            or not 16 <= len(value) <= 256
            or value != value.strip()
            or any(ord(character) < 0x21 or ord(character) == 0x7F for character in value)
        ):
            raise ValidationError("artifact transfer idempotency key is invalid")
        return value

    @staticmethod
    def _require_recipients(recipients: tuple[str, ...]) -> tuple[str, ...]:
        if (
            not isinstance(recipients, tuple)
            or not recipients
            or len(recipients) > 1_000
            or len(recipients) != len(set(recipients))
            or any(not isinstance(value, str) or not _IDENTIFIER.fullmatch(value) for value in recipients)
        ):
            raise ValidationError("artifact transfer recipients must be unique exact harness identifiers")
        return tuple(sorted(recipients))

    @staticmethod
    def _source_bytes(source: Path) -> tuple[bytes, str]:
        if not isinstance(source, Path):
            raise ValidationError("artifact source must be a filesystem path")
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
        try:
            descriptor = os.open(source, flags)
        except OSError as exc:
            raise ValidationError("artifact source is unavailable or unsafe") from exc
        try:
            before = os.fstat(descriptor)
            if not stat.S_ISREG(before.st_mode):
                raise ValidationError("artifact source must be a regular file")
            if hasattr(os, "geteuid") and before.st_uid != os.geteuid():
                raise ValidationError("artifact source must be owned by the current user")
            if before.st_size < 0 or before.st_size > MAX_ARTIFACT_BYTES:
                raise ValidationError("artifact source size is outside the supported boundary")
            remaining = int(before.st_size)
            chunks: list[bytes] = []
            while remaining:
                chunk = os.read(descriptor, min(65_536, remaining))
                if not chunk:
                    raise ValidationError("artifact source ended before its recorded size")
                chunks.append(chunk)
                remaining -= len(chunk)
            if os.read(descriptor, 1):
                raise ValidationError("artifact source exceeded its recorded size")
            after = os.fstat(descriptor)
            stable_fields = (
                "st_dev",
                "st_ino",
                "st_mode",
                "st_uid",
                "st_size",
                "st_mtime_ns",
                "st_ctime_ns",
            )
            if any(getattr(before, field) != getattr(after, field) for field in stable_fields):
                raise ValidationError("artifact source changed while it was read")
        finally:
            os.close(descriptor)
        name = source.name
        if (
            not name
            or len(name) > 255
            or name in {".", ".."}
            or any(ord(character) < 0x20 or ord(character) == 0x7F for character in name)
        ):
            raise ValidationError("artifact source name is invalid")
        return b"".join(chunks), name

    @staticmethod
    def _scope_snapshot(scope: Any) -> dict[str, Any]:
        try:
            members = tuple(scope.member_harness_ids)
            snapshot = {
                "scope_id": str(scope.scope_id),
                "scope_revision": int(scope.revision),
                "policy_revision": int(scope.policy_revision),
                "domain_revocation_epoch": int(scope.domain_revocation_epoch),
                "member_harness_ids": list(members),
                "scope_digest": str(scope.scope_digest),
            }
        except Exception as exc:
            raise AuthorizationError("collaboration scope is unavailable") from exc
        if (
            not _IDENTIFIER.fullmatch(snapshot["scope_id"])
            or snapshot["scope_revision"] < 1
            or snapshot["policy_revision"] < 1
            or not _SHA256.fullmatch(snapshot["scope_digest"])
            or snapshot["domain_revocation_epoch"] < 1
            or not members
            or tuple(sorted(set(members))) != members
            or any(not _IDENTIFIER.fullmatch(member) for member in members)
        ):
            raise AuthorizationError("collaboration scope is unavailable")
        return snapshot

    def _require_scope(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str | None,
        action: str,
        resource: str,
        recipients: tuple[str, ...],
        classification: Classification,
    ) -> tuple[Any, dict[str, Any]]:
        try:
            scope = self.collaboration_scopes.require(
                actor=actor,
                scope_id=scope_id,
                action=action,
                resource=resource,
                target_harness_ids=recipients,
                classification=classification,
                when=datetime.fromtimestamp(self.clock(), UTC),
            )
        except (AuthorizationError, ConflictError, GateBlocked):
            raise
        except Exception as exc:
            raise AuthorizationError("collaboration scope is unavailable") from exc
        snapshot = self._scope_snapshot(scope)
        if scope_id is not None and not secrets.compare_digest(snapshot["scope_id"], scope_id):
            raise AuthorizationError("collaboration scope is unavailable")
        if getattr(scope, "domain_id", None) != actor.domain_id:
            raise AuthorizationError("collaboration scope is unavailable")
        if actor.harness_id not in snapshot["member_harness_ids"] or not set(recipients).issubset(
            snapshot["member_harness_ids"]
        ):
            raise AuthorizationError("collaboration scope is unavailable")
        return scope, snapshot

    @staticmethod
    def _same_scope_snapshot(expected: dict[str, Any], current: dict[str, Any]) -> None:
        if not secrets.compare_digest(canonical_json(expected), canonical_json(current)):
            raise AuthorizationError("artifact transfer authorization changed; submit a new transfer")

    def _decision(
        self,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        classification: Classification,
        context: dict[str, Any],
    ) -> str:
        decision = self.authorize(
            actor=actor,
            action=action,
            resource=resource,
            classification=classification,
            context=context,
        )
        decision_id = (
            decision.get("decision_id")
            if isinstance(decision, dict)
            else getattr(decision, "decision_id", None)
        )
        if not isinstance(decision_id, str) or not decision_id:
            raise AuthorizationError("artifact transfer authorization did not record a decision")
        return decision_id

    def _private_binding(self, row: Any) -> dict[str, Any]:
        try:
            value = self.store.cipher.decrypt_json(
                row["source_name_encrypted"],
                purpose=f"artifact-transfer-private:{row['transfer_id']}",
            )
        except Exception as exc:
            raise ConflictError("artifact transfer private binding is invalid") from exc
        if not isinstance(value, dict):
            raise ConflictError("artifact transfer private binding is invalid")
        expected = {
            "source_name",
            "scope_id",
            "scope_revision",
            "policy_revision",
            "domain_revocation_epoch",
            "scope_digest",
            "member_harness_ids",
        }
        if set(value) != expected:
            raise ConflictError("artifact transfer private binding is invalid")
        try:
            snapshot = {
                "scope_id": value["scope_id"],
                "scope_revision": int(value["scope_revision"]),
                "policy_revision": int(value["policy_revision"]),
                "domain_revocation_epoch": int(value["domain_revocation_epoch"]),
                "scope_digest": str(value["scope_digest"]),
                "member_harness_ids": list(value["member_harness_ids"]),
            }
        except Exception as exc:
            raise ConflictError("artifact transfer private binding is invalid") from exc
        if (
            not isinstance(value["source_name"], str)
            or not value["source_name"]
            or not _SHA256.fullmatch(snapshot["scope_digest"])
            or snapshot["scope_id"] != row["collaboration_scope_id"]
            or snapshot["policy_revision"] != int(row["policy_revision"])
            or snapshot["domain_revocation_epoch"] != int(row["domain_revocation_epoch"])
        ):
            raise ConflictError("artifact transfer private binding is inconsistent")
        return {"source_name": value["source_name"], "scope_snapshot": snapshot}

    def _recipients(self, transfer_id: str) -> tuple[str, ...]:
        rows = self.store.fetch_all(
            "SELECT harness_id FROM artifact_transfer_recipients WHERE transfer_id=? ORDER BY harness_id",
            (transfer_id,),
        )
        return tuple(str(row["harness_id"]) for row in rows)

    def _recipient_states(self, transfer_id: str) -> dict[str, str]:
        rows = self.store.fetch_all(
            """SELECT harness_id,custody_state FROM artifact_transfer_recipients
                 WHERE transfer_id=? ORDER BY harness_id""",
            (transfer_id,),
        )
        return {str(row["harness_id"]): str(row["custody_state"]) for row in rows}

    def _result(self, row: Any, *, duplicate: bool) -> dict[str, Any]:
        recipients = self._recipients(str(row["transfer_id"]))
        if len(recipients) != int(row["recipient_count"]):
            raise ConflictError("artifact transfer recipient binding is incomplete")
        return {
            "transfer_id": str(row["transfer_id"]),
            "collaboration_scope_id": str(row["collaboration_scope_id"]),
            "state": str(row["state"]),
            "artifact_id": row["artifact_id"],
            "event_id": row["event_id"],
            "recipient_harness_ids": recipients,
            "recipient_states": self._recipient_states(str(row["transfer_id"])),
            "expected_digest": str(row["expected_digest"]),
            "expected_size": int(row["expected_size"]),
            "media_type": str(row["media_type"]),
            "classification": str(row["classification"]),
            "revision": int(row["revision"]),
            "created_at": int(row["created_at"]),
            "updated_at": int(row["updated_at"]),
            "terminal_at": row["terminal_at"],
            "duplicate": duplicate,
        }

    def _row(self, transfer_id: str) -> Any:
        row = self.store.fetch_one(
            "SELECT * FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
        )
        if row is None:
            raise AuthorizationError("artifact transfer is unavailable")
        self._private_binding(row)
        return row

    @staticmethod
    def _exact_sender(row: Any, actor: VerifiedActor) -> bool:
        return bool(
            actor.principal_id == row["sender_principal_id"]
            and actor.harness_id == row["sender_harness_id"]
            and actor.credential_id == row["sender_credential_id"]
            and actor.credential_epoch == int(row["sender_credential_epoch"])
            and actor.domain_id == row["domain_id"]
        )

    def _require_visible(self, *, actor: VerifiedActor, row: Any) -> None:
        self._require_actor(actor)
        recipients = self._recipients(str(row["transfer_id"]))
        if not self._exact_sender(row, actor) and actor.harness_id not in recipients:
            raise AuthorizationError("artifact transfer is unavailable")
        try:
            scope = self.collaboration_scopes.get_for_actor(
                actor=actor,
                scope_id=str(row["collaboration_scope_id"]),
            )
        except Exception as exc:
            raise AuthorizationError("artifact transfer is unavailable") from exc
        private = self._private_binding(row)
        self._same_scope_snapshot(
            private["scope_snapshot"],
            self._scope_snapshot(scope),
        )

    def _transition(
        self,
        transfer_id: str,
        *,
        expected: frozenset[str],
        state: TransferState,
        reason: str,
        artifact_id: str | None = None,
        event_id: str | None = None,
    ) -> Any:
        now = self.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
            ).fetchone()
            if row is None:
                raise ConflictError("artifact transfer disappeared")
            if row["state"] == state.value:
                if artifact_id is not None and row["artifact_id"] != artifact_id:
                    raise ConflictError("artifact transfer immutable artifact binding changed")
                if event_id is not None and row["event_id"] != event_id:
                    raise ConflictError("artifact transfer immutable event binding changed")
                return row
            if row["state"] not in expected:
                raise ConflictError("artifact transfer state changed concurrently")
            if artifact_id is not None and row["artifact_id"] not in {None, artifact_id}:
                raise ConflictError("artifact transfer immutable artifact binding changed")
            if event_id is not None and row["event_id"] not in {None, event_id}:
                raise ConflictError("artifact transfer immutable event binding changed")
            terminal_at = now if state.value in _TERMINAL_STATES else None
            connection.execute(
                """UPDATE artifact_transfers
                      SET state=?,state_reason=?,artifact_id=COALESCE(artifact_id,?),
                          event_id=COALESCE(event_id,?),revision=revision+1,updated_at=?,terminal_at=?
                    WHERE transfer_id=? AND state=?""",
                (
                    state.value,
                    reason,
                    artifact_id,
                    event_id,
                    now,
                    terminal_at,
                    transfer_id,
                    row["state"],
                ),
            )
            updated = connection.execute(
                "SELECT * FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
            ).fetchone()
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.transfer_state",
                    "artifact_id": updated["artifact_id"],
                    "event_id": updated["event_id"],
                    "from_state": row["state"],
                    "reason_code": reason,
                    "to_state": state.value,
                    "transfer_id": transfer_id,
                },
            )
            return updated

    def _mark_failed(self, transfer_id: str, *, reason: str) -> None:
        row = self.store.fetch_one(
            "SELECT state FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
        )
        if row is None or row["state"] in _TERMINAL_STATES:
            return
        self._transition(
            transfer_id,
            expected=frozenset({str(row["state"])}),
            state=TransferState.FAILED,
            reason=reason,
        )

    def _create_transfer(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        source_name: str,
        scope_snapshot: dict[str, Any],
        reservation: dict[str, Any],
        expected_digest: str,
        expected_size: int,
        media_type: str,
        classification: Classification,
        idempotency_key: str,
    ) -> tuple[Any, bool]:
        transfer_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:artifact-transfer:{actor.domain_id}:{actor.harness_id}:{idempotency_key}",
            )
        )
        request = {
            "classification": classification.value,
            "expected_digest": expected_digest,
            "expected_size": expected_size,
            "media_type": media_type,
            "recipients": list(recipients),
            "scope": scope_snapshot,
            "sender": actor.audit_view(),
            "source_name_digest": hashlib.sha256(source_name.encode("utf-8")).hexdigest(),
        }
        request_digest = canonical_digest(request)
        now = self.clock()
        private = {
            "source_name": source_name,
            **scope_snapshot,
        }
        encrypted = self.store.cipher.encrypt_json(
            private,
            purpose=f"artifact-transfer-private:{transfer_id}",
        )
        with self.store.transaction() as connection:
            existing = connection.execute(
                """SELECT * FROM artifact_transfers
                     WHERE domain_id=? AND sender_harness_id=? AND idempotency_key=?""",
                (actor.domain_id, actor.harness_id, idempotency_key),
            ).fetchone()
            if existing is not None:
                if not secrets.compare_digest(str(existing["request_digest"]), request_digest):
                    raise IdempotencyConflict(
                        "artifact transfer idempotency key names a different exact request"
                    )
                if existing["reservation_id"] != reservation["reservation_id"]:
                    raise ConflictError("artifact transfer reservation binding changed")
                self._private_binding(existing)
                return existing, True
            domain = connection.execute(
                "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            if (
                domain is None
                or domain["status"] != "active"
                or int(domain["policy_revision"]) != scope_snapshot["policy_revision"]
                or int(domain["revocation_epoch"]) != scope_snapshot["domain_revocation_epoch"]
            ):
                raise AuthorizationError("artifact transfer current authority is unavailable")
            inserted = connection.execute(
                """INSERT INTO artifact_transfers(
                    transfer_id,domain_id,collaboration_scope_id,sender_principal_id,
                    sender_harness_id,sender_credential_id,sender_credential_epoch,reservation_id,
                    artifact_id,event_id,idempotency_key,request_digest,expected_digest,expected_size,
                    media_type,classification,recipient_count,source_name_encrypted,state,state_reason,
                    policy_revision,domain_revocation_epoch,revision,created_at,updated_at,terminal_at
                ) VALUES(?,?,?,?,?,?,?,?,NULL,NULL,?,?,?,?,?,?,?,?,'reserved','reservation_committed',
                         ?,?,1,?,?,NULL) ON CONFLICT DO NOTHING""",
                (
                    transfer_id,
                    actor.domain_id,
                    scope_snapshot["scope_id"],
                    actor.principal_id,
                    actor.harness_id,
                    actor.credential_id,
                    actor.credential_epoch,
                    reservation["reservation_id"],
                    idempotency_key,
                    request_digest,
                    expected_digest,
                    expected_size,
                    media_type,
                    classification.value,
                    len(recipients),
                    encrypted,
                    scope_snapshot["policy_revision"],
                    scope_snapshot["domain_revocation_epoch"],
                    now,
                    now,
                ),
            )
            if inserted.rowcount == 0:
                existing = connection.execute(
                    """SELECT * FROM artifact_transfers
                         WHERE domain_id=? AND sender_harness_id=? AND idempotency_key=?""",
                    (actor.domain_id, actor.harness_id, idempotency_key),
                ).fetchone()
                if existing is None:
                    raise ConflictError("artifact transfer identifier collision")
                if not secrets.compare_digest(
                    str(existing["request_digest"]), request_digest
                ):
                    raise IdempotencyConflict(
                        "artifact transfer idempotency key names a different exact request"
                    )
                if existing["reservation_id"] != reservation["reservation_id"]:
                    raise ConflictError("artifact transfer reservation binding changed")
                self._private_binding(existing)
                return existing, True
            for recipient in recipients:
                connection.execute(
                    """INSERT INTO artifact_transfer_recipients(
                        transfer_id,harness_id,custody_state,event_id,state_reason,revision,
                        updated_at,committed_at,acknowledged_at
                    ) VALUES(?,?,'pending',NULL,'awaiting_release',1,?,NULL,NULL)""",
                    (transfer_id, recipient, now),
                )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.transfer_reserved",
                    "actor": actor.audit_view(),
                    "collaboration_scope_id": scope_snapshot["scope_id"],
                    "member_snapshot_digest": canonical_digest(
                        {"member_harness_ids": scope_snapshot["member_harness_ids"]}
                    ),
                    "recipient_digest": canonical_digest({"recipients": list(recipients)}),
                    "request_digest": request_digest,
                    "transfer_id": transfer_id,
                },
            )
            created = connection.execute(
                "SELECT * FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
            ).fetchone()
            if audit_hash is None or created is None:
                raise ConflictError("artifact transfer reservation did not commit")
            return created, False

    def send_file(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        source: Path,
        media_type: str,
        classification: Classification,
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._require_actor(actor)
        recipients = self._require_recipients(recipients)
        idempotency_key = self._require_idempotency_key(idempotency_key)
        if not isinstance(classification, Classification):
            raise ValidationError("artifact transfer classification is invalid")
        if not isinstance(media_type, str) or not CANONICAL_MEDIA_TYPE.fullmatch(media_type):
            raise ValidationError("artifact transfer media type is invalid")
        _scope, scope_snapshot = self._require_scope(
            actor=actor,
            scope_id=collaboration_scope_id,
            action="artifact.send",
            resource="artifact:new",
            recipients=recipients,
            classification=classification,
        )
        content, source_name = self._source_bytes(source)
        expected_digest = hashlib.sha256(content).hexdigest()
        reserve_context = {
            "actor": actor.audit_view(),
            "classification": classification.value,
            "expected_digest": expected_digest,
            "expected_size": len(content),
            "media_type": media_type,
            "required_attachment": True,
        }
        reservation = self.artifacts.reserve(
            actor=actor,
            idempotency_key=f"artifact-transfer-reserve:{canonical_digest({'key': idempotency_key})}",
            expected_digest=expected_digest,
            expected_size=len(content),
            media_type=media_type,
            classification=classification,
            required_attachment=True,
            policy_decision_id=self._decision(
                actor=actor,
                action="artifact.upload.reserve",
                resource="artifact:new",
                classification=classification,
                context=reserve_context,
            ),
        )
        row, duplicate = self._create_transfer(
            actor=actor,
            recipients=recipients,
            source_name=source_name,
            scope_snapshot=scope_snapshot,
            reservation=reservation,
            expected_digest=expected_digest,
            expected_size=len(content),
            media_type=media_type,
            classification=classification,
            idempotency_key=idempotency_key,
        )
        if not duplicate:
            self._phase("after_reserve")
        result = self._reconcile(row=row, actor=actor, content=content)
        result["duplicate"] = duplicate
        return result

    def _reconcile(self, *, row: Any, actor: VerifiedActor, content: bytes | None) -> dict[str, Any]:
        transfer_id = str(row["transfer_id"])
        while True:
            row = self._row(transfer_id)
            if not self._exact_sender(row, actor):
                raise AuthorizationError("artifact transfer is unavailable")
            state = str(row["state"])
            classification = Classification(str(row["classification"]))
            recipients = self._recipients(transfer_id)
            private = self._private_binding(row)
            _scope, current_scope = self._require_scope(
                actor=actor,
                scope_id=str(row["collaboration_scope_id"]),
                action="artifact.send",
                resource=(
                    f"artifact:{row['artifact_id']}" if row["artifact_id"] else "artifact:new"
                ),
                recipients=recipients,
                classification=classification,
            )
            self._same_scope_snapshot(private["scope_snapshot"], current_scope)
            if state in _TERMINAL_STATES:
                return self._result(row, duplicate=True)

            if state == TransferState.RESERVED.value:
                reservation = self.store.fetch_one(
                    "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                    (row["reservation_id"],),
                )
                if reservation is None:
                    raise ConflictError("artifact transfer reservation disappeared")
                if reservation["state"] == "upload_reserved":
                    if content is None:
                        raise GateBlocked(
                            "artifact_transfer_source_required",
                            "artifact transfer retry requires the exact source file",
                        )
                    if (
                        len(content) != int(row["expected_size"])
                        or not secrets.compare_digest(
                            hashlib.sha256(content).hexdigest(), str(row["expected_digest"])
                        )
                    ):
                        raise IdempotencyConflict("artifact transfer source bytes changed")
                    self.artifacts.upload(
                        row["reservation_id"],
                        content,
                        actor=actor,
                        policy_decision_id=self._decision(
                            actor=actor,
                            action="artifact.upload.bytes",
                            resource=str(row["reservation_id"]),
                            classification=classification,
                            context={
                                "expected_digest": str(row["expected_digest"]),
                                "expected_size": int(row["expected_size"]),
                            },
                        ),
                    )
                    self._phase("after_upload")
                    reservation = self.store.fetch_one(
                        "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                        (row["reservation_id"],),
                    )
                if reservation["state"] not in {"object_verified", "manifest_committed"}:
                    raise ConflictError("artifact transfer reservation is not upload-complete")
                self._transition(
                    transfer_id,
                    expected=frozenset({TransferState.RESERVED.value}),
                    state=TransferState.QUARANTINED,
                    reason="quarantine_verified",
                )
                continue

            if state == TransferState.QUARANTINED.value:
                manifest = self.store.fetch_one(
                    "SELECT * FROM artifact_manifests WHERE reservation_id=?",
                    (row["reservation_id"],),
                )
                if manifest is None:
                    reservation = self.store.fetch_one(
                        "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                        (row["reservation_id"],),
                    )
                    if reservation is None or reservation["state"] != "object_verified":
                        raise ConflictError("artifact transfer quarantine binding is incomplete")
                    promoted = self.artifacts.promote_manifest(
                        reservation_id=str(row["reservation_id"]),
                        object_version=str(reservation["object_version"]),
                        provenance={"origin": f"local-file-transfer:{transfer_id}"},
                        actor=actor,
                        policy_decision_id=self._decision(
                            actor=actor,
                            action="artifact.manifest.promote",
                            resource=str(row["reservation_id"]),
                            classification=classification,
                            context={
                                "object_version": str(reservation["object_version"]),
                                "request_digest": str(reservation["request_digest"]),
                            },
                        ),
                    )
                    self._phase("after_manifest")
                    artifact_id = str(promoted["artifact_id"])
                else:
                    artifact_id = str(manifest["artifact_id"])
                self._transition(
                    transfer_id,
                    expected=frozenset({TransferState.QUARANTINED.value}),
                    state=TransferState.SCANNING,
                    reason="manifest_committed",
                    artifact_id=artifact_id,
                )
                continue

            if state == TransferState.SCANNING.value:
                manifest = self.store.fetch_one(
                    "SELECT * FROM artifact_manifests WHERE artifact_id=?",
                    (row["artifact_id"],),
                )
                if manifest is None:
                    raise ConflictError("artifact transfer manifest disappeared")
                manifest_state = str(manifest["state"])
                if manifest_state == "quarantined":
                    self.scanner.process_once(limit=25)
                    manifest = self.store.fetch_one(
                        "SELECT * FROM artifact_manifests WHERE artifact_id=?",
                        (row["artifact_id"],),
                    )
                    manifest_state = "" if manifest is None else str(manifest["state"])
                    if manifest_state == "quarantined":
                        raise GateBlocked(
                            "artifact_scan_pending",
                            "artifact scan has not produced current trusted evidence",
                        )
                    self._phase("after_scan")
                if manifest_state == "held":
                    self._mark_failed(transfer_id, reason="scanner_rejected")
                    raise AuthorizationError("artifact transfer is unavailable")
                if manifest_state not in {"scan_passed", "release_pending", "released"}:
                    raise ConflictError("artifact transfer scanner state is invalid")
                became_released = manifest_state != "released"
                try:
                    if manifest_state == "release_pending":
                        self.artifacts.process_release_outbox(str(row["artifact_id"]))
                    elif manifest_state == "scan_passed":
                        self.artifacts.release(
                            str(row["artifact_id"]),
                            actor=actor,
                            policy_decision_id=self._decision(
                                actor=actor,
                                action="artifact.release",
                                resource=str(row["artifact_id"]),
                                classification=classification,
                                context={},
                            ),
                        )
                except Exception:
                    current = self.store.fetch_one(
                        "SELECT state FROM artifact_manifests WHERE artifact_id=?",
                        (row["artifact_id"],),
                    )
                    if current is not None and current["state"] == "held":
                        self._mark_failed(transfer_id, reason="scanner_evidence_invalid")
                    raise
                released = self.store.fetch_one(
                    "SELECT state FROM artifact_manifests WHERE artifact_id=?",
                    (row["artifact_id"],),
                )
                if released is None or released["state"] != "released":
                    raise ConflictError("artifact transfer release did not complete")
                if became_released:
                    self._phase("after_release")
                self._transition(
                    transfer_id,
                    expected=frozenset({TransferState.SCANNING.value}),
                    state=TransferState.RELEASED,
                    reason="policy_release_completed",
                    artifact_id=str(row["artifact_id"]),
                )
                continue

            if state == TransferState.RELEASED.value:
                binding = self.artifacts.resolve_released_binding(str(row["artifact_id"]))
                self._require_binding(row, binding)
                event_id = self._committed_event_id(
                    row=row,
                    actor=actor,
                    recipients=recipients,
                    binding=binding,
                )
                if event_id is None:
                    event = self._event(
                        row=row,
                        actor=actor,
                        recipients=recipients,
                        binding=binding,
                    )
                    accepted = self.conversations.mailbox.accept(event)
                    if str(accepted["event_id"]) != event.event_id:
                        raise ConflictError(
                            "artifact transfer event idempotency binding changed"
                        )
                    event_id = event.event_id
                    self._phase("after_event")
                self._commit_event_state(
                    transfer_id=transfer_id,
                    event_id=event_id,
                )
                continue

            if state == TransferState.EVENT_COMMITTED.value:
                self._finalize_recipient_custody(row)
                continue

            raise ConflictError("artifact transfer has an unknown state")

    @staticmethod
    def _require_binding(row: Any, binding: ReleasedArtifactBinding) -> None:
        if (
            binding.artifact_id != row["artifact_id"]
            or binding.domain_id != row["domain_id"]
            or binding.size != int(row["expected_size"])
            or binding.media_type != row["media_type"]
            or binding.classification.value != row["classification"]
        ):
            raise ConflictError("artifact transfer released binding changed")

    def _event_payload(self, row: Any) -> dict[str, Any]:
        scope_snapshot = self._private_binding(row)["scope_snapshot"]
        return {
            "kind": "artifact.transfer",
            "transfer_id": str(row["transfer_id"]),
            "authorization_context": {
                "collaboration_scope_id": scope_snapshot["scope_id"],
                "collaboration_scope_revision": scope_snapshot["scope_revision"],
                "collaboration_scope_policy_revision": scope_snapshot["policy_revision"],
                "collaboration_scope_domain_revocation_epoch": scope_snapshot[
                    "domain_revocation_epoch"
                ],
                "collaboration_scope_member_harness_ids": scope_snapshot[
                    "member_harness_ids"
                ],
                "collaboration_scope_digest": scope_snapshot["scope_digest"],
            },
        }

    def _event(
        self,
        *,
        row: Any,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        binding: ReleasedArtifactBinding,
    ):
        event_id = str(uuid5(NAMESPACE_URL, f"agentnet:artifact-transfer-event:{row['transfer_id']}"))
        retention_days = int(getattr(self.conversations, "retention_days", 30))
        if not 1 <= retention_days <= 3_650:
            raise ConflictError("artifact transfer retention policy is invalid")
        return new_event(
            domain_id=str(row["domain_id"]),
            actor=actor,
            event_id=event_id,
            event_type=EventType.MESSAGE,
            classification=Classification(str(row["classification"])),
            payload=self._event_payload(row),
            idempotency_key=f"artifact-transfer-event:{row['transfer_id']}",
            recipients=recipients,
            released_artifacts=(binding,),
            retention_delete_at=datetime.now(UTC) + timedelta(days=retention_days),
            policy_revision=int(row["policy_revision"]),
        )

    @staticmethod
    def _event_semantics(event: EventEnvelope) -> dict[str, Any]:
        return {
            "domain_id": event.domain_id,
            "actor": event.actor.audit_view(),
            "event_type": event.event_type.value,
            "classification": event.classification.value,
            "payload": event.payload,
            "idempotency_key": event.idempotency_key,
            "recipients": list(event.recipients),
            "released_artifacts": [
                artifact.model_dump(mode="json") for artifact in event.released_artifacts
            ],
            "policy_revision": event.policy_revision,
            "credential_epoch": event.credential_epoch,
            "legal_hold": event.legal_hold,
            "conversation_id": event.conversation_id,
            "room_id": event.room_id,
            "room_control_sequence": event.room_control_sequence,
            "room_application_epoch": event.room_application_epoch,
            "room_file_key_epoch": event.room_file_key_epoch,
            "room_mls_epoch": event.room_mls_epoch,
            "thread_id": event.thread_id,
            "task_id": event.task_id,
            "causal_parent_ids": list(event.causal_parent_ids),
        }

    def _committed_event_id(
        self,
        *,
        row: Any,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        binding: ReleasedArtifactBinding,
    ) -> str | None:
        expected_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:artifact-transfer-event:{row['transfer_id']}",
            )
        )
        stored = self.store.fetch_one(
            "SELECT * FROM events WHERE event_id=?",
            (expected_id,),
        )
        if stored is None:
            return None
        try:
            raw_metadata = str(stored["envelope_json"])
            metadata = json.loads(raw_metadata)
            if (
                not isinstance(metadata, dict)
                or canonical_json(metadata).decode("utf-8") != raw_metadata
            ):
                raise ValueError("event envelope metadata is not canonical")
            payload = self.store.decrypted_payload(
                str(stored["payload_encrypted"]),
                expected_id,
            )
            committed = EventEnvelope.model_validate_json(
                canonical_json(metadata | {"payload": payload}),
                strict=True,
            )
            validate_event_digest(committed)
        except Exception as exc:
            raise ConflictError("artifact transfer committed event is invalid") from exc
        expected_semantics = {
            "domain_id": str(row["domain_id"]),
            "actor": actor.audit_view(),
            "event_type": EventType.MESSAGE.value,
            "classification": str(row["classification"]),
            "payload": self._event_payload(row),
            "idempotency_key": f"artifact-transfer-event:{row['transfer_id']}",
            "recipients": list(recipients),
            "released_artifacts": [binding.model_dump(mode="json")],
            "policy_revision": int(row["policy_revision"]),
            "credential_epoch": actor.credential_epoch,
            "legal_hold": False,
            "conversation_id": None,
            "room_id": None,
            "room_control_sequence": None,
            "room_application_epoch": None,
            "room_file_key_epoch": None,
            "room_mls_epoch": None,
            "thread_id": None,
            "task_id": None,
            "causal_parent_ids": [],
        }
        actual_recipients = tuple(
            str(item["recipient_id"])
            for item in self.store.fetch_all(
                "SELECT recipient_id FROM recipients WHERE event_id=? ORDER BY recipient_id",
                (expected_id,),
            )
        )
        if (
            committed.event_id != expected_id
            or stored["envelope_digest"] != envelope_digest(committed)
            or stored["payload_digest"] != committed.payload_digest
            or self._event_semantics(committed) != expected_semantics
            or actual_recipients != recipients
        ):
            raise ConflictError("artifact transfer immutable event binding changed")
        return expected_id

    def _commit_event_state(self, *, transfer_id: str, event_id: str) -> None:
        now = self.clock()
        with self.store.transaction() as connection:
            current = connection.execute(
                "SELECT * FROM artifact_transfers WHERE transfer_id=?",
                (transfer_id,),
            ).fetchone()
            if current is None:
                raise ConflictError("artifact transfer disappeared")
            if current["state"] == TransferState.EVENT_COMMITTED.value:
                if current["event_id"] != event_id:
                    raise ConflictError("artifact transfer immutable event binding changed")
                return
            if current["state"] != TransferState.RELEASED.value:
                raise ConflictError("artifact transfer event state changed concurrently")
            connection.execute(
                """UPDATE artifact_transfers
                      SET state='event_committed',state_reason='event_custody_committed',
                          event_id=?,revision=revision+1,updated_at=?
                    WHERE transfer_id=? AND state='released'""",
                (event_id, now, transfer_id),
            )
            connection.execute(
                """UPDATE artifact_transfer_recipients
                      SET custody_state='event_committed',event_id=?,
                          state_reason='event_custody_committed',revision=revision+1,updated_at=?
                    WHERE transfer_id=? AND custody_state='pending'""",
                (event_id, now, transfer_id),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.transfer_event_committed",
                    "event_id": event_id,
                    "transfer_id": transfer_id,
                },
            )

    def _finalize_recipient_custody(self, row: Any) -> None:
        transfer_id = str(row["transfer_id"])
        event_id = str(row["event_id"])
        expected = self._recipients(transfer_id)
        now = self.clock()
        with self.store.transaction() as connection:
            event = connection.execute(
                "SELECT domain_id FROM events WHERE event_id=?", (event_id,)
            ).fetchone()
            actual_rows = connection.execute(
                "SELECT recipient_id FROM recipients WHERE event_id=? ORDER BY recipient_id",
                (event_id,),
            ).fetchall()
            actual = tuple(str(item["recipient_id"]) for item in actual_rows)
            if event is None or event["domain_id"] != row["domain_id"] or actual != expected:
                raise ConflictError("artifact transfer exact recipient custody is incomplete")
            current = connection.execute(
                "SELECT * FROM artifact_transfers WHERE transfer_id=?", (transfer_id,)
            ).fetchone()
            if current is None:
                raise ConflictError("artifact transfer disappeared")
            if current["state"] == TransferState.RECIPIENT_COMMITTED.value:
                return
            if current["state"] != TransferState.EVENT_COMMITTED.value:
                raise ConflictError("artifact transfer custody state changed concurrently")
            changed = connection.execute(
                """UPDATE artifact_transfer_recipients
                      SET custody_state='recipient_committed',state_reason='exact_recipient_custody',
                          revision=revision+1,updated_at=?,committed_at=?
                    WHERE transfer_id=? AND event_id=? AND custody_state='event_committed'""",
                (now, now, transfer_id, event_id),
            )
            if changed.rowcount != len(expected):
                raise ConflictError("artifact transfer exact recipient custody is incomplete")
            connection.execute(
                """UPDATE artifact_transfers
                      SET state='recipient_committed',state_reason='exact_recipient_custody',
                          revision=revision+1,updated_at=?,terminal_at=?
                    WHERE transfer_id=? AND state='event_committed'""",
                (now, now, transfer_id),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.transfer_recipient_custody",
                    "event_id": event_id,
                    "recipient_digest": canonical_digest({"recipients": list(expected)}),
                    "transfer_id": transfer_id,
                },
            )

    def reconcile(
        self,
        *,
        actor: VerifiedActor,
        transfer_id: str,
        source: Path | None = None,
    ) -> dict[str, Any]:
        self._require_actor(actor)
        if not isinstance(transfer_id, str) or not _IDENTIFIER.fullmatch(transfer_id):
            raise AuthorizationError("artifact transfer is unavailable")
        row = self._row(transfer_id)
        if not self._exact_sender(row, actor):
            raise AuthorizationError("artifact transfer is unavailable")
        content = None
        if source is not None:
            content, _source_name = self._source_bytes(source)
            if (
                len(content) != int(row["expected_size"])
                or not secrets.compare_digest(
                    hashlib.sha256(content).hexdigest(), str(row["expected_digest"])
                )
            ):
                raise IdempotencyConflict("artifact transfer source bytes changed")
        return self._reconcile(row=row, actor=actor, content=content)

    def recover(self, *, actor: VerifiedActor, limit: int = 100) -> tuple[dict[str, Any], ...]:
        self._require_actor(actor)
        if type(limit) is not int or not 1 <= limit <= 1_000:
            raise ValidationError("artifact transfer recovery limit is invalid")
        rows = self.store.fetch_all(
            """SELECT transfer_id FROM artifact_transfers
                 WHERE domain_id=? AND sender_harness_id=?
                   AND state NOT IN ('recipient_committed','failed','canceled')
                 ORDER BY updated_at,transfer_id LIMIT ?""",
            (actor.domain_id, actor.harness_id, limit),
        )
        results: list[dict[str, Any]] = []
        for item in rows:
            try:
                results.append(
                    self.reconcile(actor=actor, transfer_id=str(item["transfer_id"]))
                )
            except GateBlocked as exc:
                if exc.gate != "artifact_transfer_source_required":
                    raise
                row = self._row(str(item["transfer_id"]))
                results.append(self._result(row, duplicate=True))
        return tuple(results)

    def status(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        transfer_id: str,
    ) -> dict[str, Any]:
        if not isinstance(transfer_id, str) or not _IDENTIFIER.fullmatch(transfer_id):
            raise AuthorizationError("artifact transfer is unavailable")
        row = self._row(transfer_id)
        if (
            not isinstance(collaboration_scope_id, str)
            or not secrets.compare_digest(
                collaboration_scope_id,
                str(row["collaboration_scope_id"]),
            )
        ):
            raise AuthorizationError("artifact transfer is unavailable")
        self._require_visible(actor=actor, row=row)
        return self._result(row, duplicate=True)

    def _download_row(self, *, actor: VerifiedActor, artifact_id: str) -> Any:
        if not isinstance(artifact_id, str) or not _IDENTIFIER.fullmatch(artifact_id):
            raise AuthorizationError("artifact transfer is unavailable")
        row = self.store.fetch_one(
            """SELECT t.*,r.custody_state
                 FROM artifact_transfers t
                 JOIN artifact_transfer_recipients r ON r.transfer_id=t.transfer_id
                WHERE t.artifact_id=? AND r.harness_id=?""",
            (artifact_id, actor.harness_id),
        )
        if (
            row is None
            or row["state"] != TransferState.RECIPIENT_COMMITTED.value
            or row["custody_state"] not in {"recipient_committed", "acknowledged"}
        ):
            raise AuthorizationError("artifact transfer is unavailable")
        return row

    def _download_intent(
        self,
        *,
        destination_exists: bool,
        actor: VerifiedActor,
        row: Any,
        artifact_id: str,
        destination: Path,
        idempotency_key: str,
        policy_decision_id: str,
    ) -> tuple[str, str]:
        intent_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:artifact-download:{actor.domain_id}:{actor.harness_id}:{idempotency_key}",
            )
        )
        destination_digest = hashlib.sha256(
            os.fspath(destination).encode("utf-8")
        ).hexdigest()
        request_digest = canonical_digest(
            {
                "actor": actor.audit_view(),
                "artifact_id": artifact_id,
                "destination_digest": destination_digest,
                "expected_digest": str(row["expected_digest"]),
                "expected_size": int(row["expected_size"]),
                "idempotency_key_digest": hashlib.sha256(
                    idempotency_key.encode("utf-8")
                ).hexdigest(),
                "transfer_id": str(row["transfer_id"]),
            }
        )
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        with self.store.transaction() as connection:
            existing = connection.execute(
                "SELECT * FROM audit_intents WHERE intent_id=?", (intent_id,)
            ).fetchone()
            if existing is None and destination_exists:
                raise ConflictError("download destination already exists")
            if existing is None:
                inserted = connection.execute(
                    """INSERT INTO audit_intents(
                        intent_id,action,resource_id,actor_json,policy_decision_id,
                        request_digest,state,created_at,completed_at
                    ) VALUES(?,?,?,?,?,?,'pending',?,NULL) ON CONFLICT DO NOTHING""",
                    (
                        intent_id,
                        "artifact.download.materialize",
                        artifact_id,
                        actor_json,
                        policy_decision_id,
                        request_digest,
                        self.clock(),
                    ),
                )
                if inserted.rowcount != 0:
                    self.store.append_audit(
                        connection,
                        {
                            "action": "artifact.transfer_materialization_intent",
                            "artifact_id": artifact_id,
                            "destination_digest": destination_digest,
                            "intent_id": intent_id,
                            "request_digest": request_digest,
                            "transfer_id": row["transfer_id"],
                        },
                    )
                    return intent_id, "pending"
                existing = connection.execute(
                    "SELECT * FROM audit_intents WHERE intent_id=?", (intent_id,)
                ).fetchone()
                if existing is None:
                    raise ConflictError("artifact download intent identifier collision")
            if (
                existing["action"] != "artifact.download.materialize"
                or existing["resource_id"] != artifact_id
                or existing["actor_json"] != actor_json
                or not secrets.compare_digest(
                    str(existing["request_digest"]), request_digest
                )
            ):
                raise IdempotencyConflict(
                    "artifact download idempotency key names a different exact request"
                )
            return intent_id, str(existing["state"])

    def _complete_download_intent(
        self,
        *,
        intent_id: str,
        actor: VerifiedActor,
        row: Any,
        artifact_id: str,
        destination: Path,
    ) -> None:
        now = self.clock()
        with self.store.transaction() as connection:
            intent = connection.execute(
                "SELECT * FROM audit_intents WHERE intent_id=?", (intent_id,)
            ).fetchone()
            if intent is None:
                raise ConflictError("artifact download materialization intent disappeared")
            if intent["state"] == "completed":
                return
            if intent["state"] != "pending":
                raise ConflictError("artifact download materialization intent is invalid")
            connection.execute(
                """UPDATE audit_intents SET state='completed',completed_at=?
                     WHERE intent_id=? AND state='pending'""",
                (now, intent_id),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.transfer_materialized",
                    "actor": actor.audit_view(),
                    "artifact_id": artifact_id,
                    "destination_digest": hashlib.sha256(
                        os.fspath(destination).encode("utf-8")
                    ).hexdigest(),
                    "intent_id": intent_id,
                    "plaintext_digest": str(row["expected_digest"]),
                    "size": int(row["expected_size"]),
                    "transfer_id": row["transfer_id"],
                },
            )

    @staticmethod
    def _download_result(
        *,
        row: Any,
        artifact_id: str,
        destination: Path,
        duplicate: bool,
    ) -> dict[str, Any]:
        return {
            "transfer_id": str(row["transfer_id"]),
            "artifact_id": artifact_id,
            "state": "materialized",
            "destination": os.fspath(destination),
            "plaintext_digest": str(row["expected_digest"]),
            "size": int(row["expected_size"]),
            "duplicate": duplicate,
        }

    def download_file(
        self,
        *,
        actor: VerifiedActor,
        collaboration_scope_id: str,
        artifact_id: str,
        destination: Path,
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._require_actor(actor)
        self._require_idempotency_key(idempotency_key)
        if self.destination is None:
            raise GateBlocked(
                "artifact_download_destination",
                "no owner-approved artifact download root is configured",
            )
        row = self._download_row(actor=actor, artifact_id=artifact_id)
        if (
            not isinstance(collaboration_scope_id, str)
            or not secrets.compare_digest(
                collaboration_scope_id,
                str(row["collaboration_scope_id"]),
            )
        ):
            raise AuthorizationError("artifact transfer is unavailable")
        classification = Classification(str(row["classification"]))
        private = self._private_binding(row)
        _scope, current_scope = self._require_scope(
            actor=actor,
            scope_id=collaboration_scope_id,
            action="artifact.download",
            resource=f"artifact:{artifact_id}",
            recipients=(str(actor.harness_id),),
            classification=classification,
        )
        self._same_scope_snapshot(private["scope_snapshot"], current_scope)
        lifecycle = self.store.fetch_one(
            "SELECT status FROM artifact_lifecycle WHERE artifact_id=?", (artifact_id,)
        )
        if lifecycle is None or lifecycle["status"] != "active":
            raise AuthorizationError("artifact transfer is unavailable")
        decision_id = self._decision(
            actor=actor,
            action="artifact.download",
            resource=artifact_id,
            classification=classification,
            context={"audience_harness_id": actor.harness_id},
        )
        binding = self.artifacts.resolve_released_binding(artifact_id)
        self._require_binding(row, binding)
        existing = self.destination.verify_existing(
            destination=destination,
            expected_digest=str(row["expected_digest"]),
            expected_size=int(row["expected_size"]),
        )
        intent_id, intent_state = self._download_intent(
            destination_exists=existing is not None,
            actor=actor,
            row=row,
            artifact_id=artifact_id,
            destination=destination,
            idempotency_key=idempotency_key,
            policy_decision_id=decision_id,
        )
        if intent_state == "completed":
            if existing is None:
                raise ConflictError("recorded download destination is unavailable")
            return self._download_result(
                row=row,
                artifact_id=artifact_id,
                destination=existing,
                duplicate=True,
            )
        if existing is not None:
            self._complete_download_intent(
                intent_id=intent_id,
                actor=actor,
                row=row,
                artifact_id=artifact_id,
                destination=existing,
            )
            return self._download_result(
                row=row,
                artifact_id=artifact_id,
                destination=existing,
                duplicate=True,
            )
        token = self.artifacts.issue_download_capability(
            artifact_id,
            actor=actor,
            audience_harness_id=str(actor.harness_id),
            policy_decision_id=decision_id,
        )
        content = self.artifacts.consume_download(token, actor=actor)
        digest = hashlib.sha256(content).hexdigest()
        if (
            len(content) != int(row["expected_size"])
            or not secrets.compare_digest(digest, str(row["expected_digest"]))
        ):
            raise ConflictError("downloaded artifact bytes do not match the immutable transfer")
        written = self.destination.write(
            destination=destination,
            content=content,
            expected_digest=str(row["expected_digest"]),
        )
        self._complete_download_intent(
            intent_id=intent_id,
            actor=actor,
            row=row,
            artifact_id=artifact_id,
            destination=written,
        )
        return self._download_result(
            row=row,
            artifact_id=artifact_id,
            destination=written,
            duplicate=False,
        )


__all__ = ["ArtifactTransferService", "TransferState"]
