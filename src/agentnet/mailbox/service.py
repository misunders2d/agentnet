"""Transactional mailbox with exact typed owners for every receipt fact."""

from __future__ import annotations

import json
import sqlite3
import threading
import time
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Callable
from uuid import uuid4

from agentnet.authorization.policy import validate_actor_state
from agentnet.delivery.state import require_transition
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.recipients import RecipientResolver
from agentnet.identity.workload import (
    RegisteredWorkloadCredential,
    WorkloadRegistry,
    WorkloadTransitionProof,
)
from agentnet.messaging.events import envelope_digest, envelope_metadata, validate_event_digest
from agentnet.operations.policy_defaults import RevocationPolicy
from agentnet.operations.quotas import QuotaService
from agentnet.protocol.models import (
    AcceptingStorageBoundary,
    DeliveryFact,
    EventEnvelope,
    EventType,
)
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceObjectType,
    ProvenanceOrigin,
    ProvenanceReferenceV1,
    ProvenanceService,
    SinkSet,
)
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


FACT_OWNER_MATRIX: dict[DeliveryFact, str] = {
    DeliveryFact.CREATED_LOCAL: "origin_supervisor",
    DeliveryFact.SUBMITTED: "origin_supervisor",
    DeliveryFact.REJECTED_BEFORE_ACCEPT: "accepting_authority",
    DeliveryFact.AUTHORIZATION_HOLD: "accepting_authority",
    DeliveryFact.ACCEPTED_LOCAL: "accepting_authority",
    DeliveryFact.ACCEPTED_DURABLE: "accepting_authority",
    DeliveryFact.ACCEPTED_QUEUED: "accepting_authority",
    DeliveryFact.PENDING_HUMAN: "accepting_authority",
    DeliveryFact.QUEUED: "mailbox_dispatcher",
    DeliveryFact.RETRY_SCHEDULED: "mailbox_dispatcher",
    DeliveryFact.DISPATCH_ATTEMPTED: "mailbox_dispatcher",
    DeliveryFact.REMOTE_ACCEPTED: "remote_domain",
    DeliveryFact.REMOTE_REJECTED: "remote_domain",
    DeliveryFact.REMOTE_DELAYED: "remote_domain",
    DeliveryFact.RECIPIENT_COMMITTED: "recipient_custodian",
    DeliveryFact.PRESENTED: "recipient_presentation",
    DeliveryFact.PROCESSING: "recipient_processor",
    DeliveryFact.EFFECT_PREPARED: "effect_authority",
    DeliveryFact.COMPLETED: "effect_authority",
    DeliveryFact.FAILED_RETRYABLE: "failing_workload",
    DeliveryFact.FAILED_TERMINAL: "failing_workload",
    DeliveryFact.EXPIRED: "mailbox_dispatcher",
    DeliveryFact.CANCEL_REQUESTED: "control_authority",
    DeliveryFact.CANCELED: "effect_authority",
    DeliveryFact.TOO_LATE: "effect_authority",
    DeliveryFact.EFFECT_UNKNOWN: "effect_authority",
    DeliveryFact.RECONCILING: "effect_authority",
    DeliveryFact.COMPENSATED: "effect_authority",
    DeliveryFact.ADJUDICATION_REQUIRED: "governance_authority",
    DeliveryFact.QUARANTINED: "security_authority",
    DeliveryFact.DEAD_LETTERED: "mailbox_dispatcher",
    DeliveryFact.CONFLICT_PENDING: "governance_authority",
}


@dataclass(frozen=True, slots=True)
class ExpiryAuthorization:
    """Exact registered-dispatcher authorization for one mailbox entry."""

    proof: WorkloadTransitionProof
    credential: RegisteredWorkloadCredential | None = None
    actor: VerifiedActor | None = None

    def __post_init__(self) -> None:
        if (self.credential is None) == (self.actor is None):
            raise ValueError("expiry authorization requires exactly one registered workload actor source")

    @property
    def resolved_actor(self) -> VerifiedActor:
        if self.actor is not None:
            return self.actor
        if self.credential is None:  # Defensive narrowing; __post_init__ rejects this state.
            raise ValueError("expiry authorization lacks a registered workload actor")
        return self.credential.actor


class MailboxService:
    def __init__(
        self,
        store: StoreBackend,
        *,
        acceptance_fact: DeliveryFact | None = None,
        revocation_policy: RevocationPolicy | None = None,
        admission: QuotaService | None = None,
        provenance: ProvenanceService | None = None,
    ) -> None:
        # A single PostgreSQL primary is real local custody, but it is not the
        # independently evidenced replicated RPO boundary of ACCEPTED_DURABLE.
        derived = DeliveryFact.ACCEPTED_LOCAL
        if acceptance_fact is not None and acceptance_fact is not derived:
            raise ValueError("mailbox acceptance fact does not match the verified storage boundary")
        self.store = store
        self.acceptance_fact = derived
        self.revocation_policy = revocation_policy
        self.admission = admission
        self.provenance = provenance or ProvenanceService(store)
        self.workloads = WorkloadRegistry(store)
        self._wake_lock = threading.Lock()
        self._wake_subscribers: dict[int, tuple[str, Callable[[], None]]] = {}
        self._wake_subscription_sequence = 0

    @staticmethod
    def _canonical_submission_intent(event: EventEnvelope) -> str:
        """Digest the caller-stable intent rather than server-minted envelope facts.

        ``event_id``, ``created_at``, and ``retention_delete_at`` are assigned by
        the accepting server for ordinary messages.  A response can be lost
        after commit, so those values cannot participate in deciding whether
        the client's exact idempotent retry is the same operation.

        Every caller-controlled field and every authority/room/artifact epoch
        remains bound.  The stored full ``envelope_digest`` still protects the
        immutable accepted envelope; this digest is used only for convergence.
        """

        value = event.model_dump(mode="json", exclude_none=True)
        if event.event_type is EventType.MESSAGE:
            value.pop("event_id", None)
            value.pop("created_at", None)
            value.pop("retention_delete_at", None)
        return canonical_digest(value)

    @staticmethod
    def _provenance_sink_ceiling(connection: Any, event: EventEnvelope) -> tuple[str, ...]:
        """Derive a deny-only sink ceiling from authoritative local membership."""

        sinks = set(event.recipients)
        if event.actor.harness_id is not None:
            sinks.add(event.actor.harness_id)
        if event.conversation_id is not None:
            rows = connection.execute(
                """SELECT harness_id FROM conversation_members
                    WHERE conversation_id=? AND status='active'""",
                (event.conversation_id,),
            ).fetchall()
            sinks.update(str(row["harness_id"]) for row in rows)
        if event.room_id is not None and event.room_control_sequence is not None:
            rows = connection.execute(
                """SELECT harness_id FROM room_members
                    WHERE room_id=? AND joined_sequence<=?
                      AND (removed_sequence IS NULL OR removed_sequence>?)""",
                (event.room_id, event.room_control_sequence, event.room_control_sequence),
            ).fetchall()
            sinks.update(str(row["harness_id"]) for row in rows)
        return tuple(sorted(sinks))

    def _causal_parent_references(
        self,
        connection: Any,
        event: EventEnvelope,
    ) -> tuple[ProvenanceReferenceV1, ...]:
        references: list[ProvenanceReferenceV1] = []
        for parent_event_id in event.causal_parent_ids:
            row = connection.execute(
                "SELECT * FROM events WHERE event_id=?",
                (parent_event_id,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("causal parent event is unavailable")
            parent, _payload = self._validated_event_and_payload(row, connection=connection)
            if parent.domain_id != event.domain_id:
                raise AuthorizationError("causal parent crossed a trust domain")
            references.append(self._event_provenance_reference(parent, connection=connection))
        return tuple(references)

    def _signal_content_free_wake(self, recipient_ids: tuple[str, ...]) -> None:
        """Wake only exact-recipient watchers without content or authority."""

        recipients = frozenset(recipient_ids)
        with self._wake_lock:
            subscribers = tuple(
                callback
                for recipient_id, callback in self._wake_subscribers.values()
                if recipient_id in recipients
            )
        for callback in subscribers:
            try:
                callback()
            except Exception:
                # A hint is never part of acceptance authority or durability.
                # Broken subscribers reconcile by cursor after reconnect.
                continue

    def subscribe_content_free_wake(
        self,
        recipient_id: str,
        callback: Callable[[], None],
    ) -> int:
        """Register an in-process wake hint; returned IDs carry no authority."""

        if not recipient_id or not callable(callback):
            raise ValueError("mailbox wake subscriber binding is invalid")
        with self._wake_lock:
            self._wake_subscription_sequence += 1
            subscription_id = self._wake_subscription_sequence
            self._wake_subscribers[subscription_id] = (recipient_id, callback)
            return subscription_id

    def unsubscribe_content_free_wake(self, subscription_id: int) -> None:
        with self._wake_lock:
            self._wake_subscribers.pop(subscription_id, None)

    def _require_workload(
        self,
        connection: Any,
        actor: VerifiedActor,
        *,
        event_domain_id: str,
        allowed_roles: set[str],
        event_id: str,
        recipient_id: str,
        proposed: DeliveryFact,
        detail: dict[str, Any] | None,
        now: int,
        proof: WorkloadTransitionProof | None,
    ) -> None:
        if actor.domain_id != event_domain_id:
            raise AuthorizationError("workload transition crossed the event trust domain")
        self.workloads.verify_transition(
            connection,
            actor=actor,
            proof=proof,
            allowed_roles=allowed_roles,
            event_id=event_id,
            recipient_id=recipient_id,
            proposed_fact=proposed,
            detail=detail,
            now=now,
        )

    @staticmethod
    def _require_current_recipient(
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        recipient_id: str,
        event_domain_id: str,
        policy_revision: int,
        now: int,
    ) -> None:
        if actor.harness_id != recipient_id or actor.domain_id != event_domain_id:
            raise AuthorizationError("recipient receipt must come from the exact authenticated recipient harness")
        denial, _current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=policy_revision,
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None:
            raise AuthorizationError(f"recipient receipt actor is not current: {denial}")

    def _require_fact_owner(
        self,
        connection: sqlite3.Connection,
        *,
        actor: VerifiedActor,
        fact: DeliveryFact,
        recipient_id: str,
        event_domain_id: str,
        policy_revision: int,
        now: int,
        event_id: str,
        detail: dict[str, Any] | None,
        workload_proof: WorkloadTransitionProof | None,
    ) -> None:
        owner_class = FACT_OWNER_MATRIX[fact]
        if owner_class in {"recipient_custodian", "recipient_presentation"}:
            if actor.kind in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
                self._require_current_recipient(
                    connection,
                    actor=actor,
                    recipient_id=recipient_id,
                    event_domain_id=event_domain_id,
                    policy_revision=policy_revision,
                    now=now,
                )
                return
            self._require_workload(
                connection,
                actor,
                event_domain_id=event_domain_id,
                allowed_roles={owner_class},
                event_id=event_id,
                recipient_id=recipient_id,
                proposed=fact,
                detail=detail,
                now=now,
                proof=workload_proof,
            )
            return
        if owner_class == "recipient_processor":
            self._require_workload(
                connection,
                actor,
                event_domain_id=event_domain_id,
                allowed_roles={"recipient_processor"},
                event_id=event_id,
                recipient_id=recipient_id,
                proposed=fact,
                detail=detail,
                now=now,
                proof=workload_proof,
            )
            return
        workload_roles = {
            "origin_supervisor": {"origin_supervisor"},
            "accepting_authority": {"accepting_authority"},
            "mailbox_dispatcher": {"mailbox_dispatcher"},
            "effect_authority": {"effect_authority"},
            "control_authority": {"control_authority"},
            "governance_authority": {"governance_authority"},
            "security_authority": {"security_authority"},
            "failing_workload": {
                "mailbox_dispatcher",
                "recipient_processor",
                "effect_authority",
            },
        }
        if owner_class == "remote_domain":
            raise AuthorizationError("remote facts require a separately verified remote-domain receipt adapter")
        self._require_workload(
            connection,
            actor,
            event_domain_id=event_domain_id,
            allowed_roles=workload_roles[owner_class],
            event_id=event_id,
            recipient_id=recipient_id,
            proposed=fact,
            detail=detail,
            now=now,
            proof=workload_proof,
        )

    def _accept_in_transaction(
        self,
        connection: Any,
        event: EventEnvelope,
        *,
        now: int | None = None,
        pending_cost: int | None = None,
    ) -> dict[str, Any]:
        """Accept exact bytes on the caller's transaction.

        This is the atomic composition point for conversation, relay, and
        workflow services.  Callers must not publish the returned acceptance
        until their transaction context commits successfully.
        """

        validate_event_digest(event)
        if self.revocation_policy is not None and not event.legal_hold:
            if event.retention_delete_at is None:
                raise AuthorizationError("accepted history requires an explicit authorized retention boundary")
            retention_seconds = int((event.retention_delete_at - event.created_at).total_seconds())
            maximum_seconds = self.revocation_policy.accepted_history_max_retention_days * 86_400
            if retention_seconds < 0 or retention_seconds > maximum_seconds:
                raise AuthorizationError("accepted history retention exceeds the post-revocation policy")
        digest = envelope_digest(event)
        actor_json = canonical_json(event.actor.audit_view()).decode("utf-8")
        now = int(time.time()) if now is None else now
        existing = connection.execute(
            "SELECT * FROM events WHERE domain_id=? AND actor_json=? AND idempotency_key=?",
            (event.domain_id, actor_json, event.idempotency_key),
        ).fetchone()
        if existing is not None:
            existing_event, _existing_payload = self._validated_event_and_payload(
                existing,
                connection=connection,
            )
            existing_provenance = self._event_provenance_reference(
                existing_event,
                connection=connection,
            )
            if self._canonical_submission_intent(existing_event) != self._canonical_submission_intent(event):
                raise IdempotencyConflict(
                    "same idempotency key was used with different canonical intent"
                )
            return {
                "event_id": existing["event_id"],
                "fact": existing["acceptance_fact"],
                "duplicate": True,
                "envelope_digest": existing["envelope_digest"],
                "provenance": existing_provenance.model_dump(mode="json"),
            }
        if self.admission is not None:
            self.admission._admit_operation_in_transaction(
                connection,
                actor_scope=(
                    event.actor.harness_id
                    or event.actor.positive_authority_id
                    or "unattributed"
                ),
                domain_scope=event.domain_id,
                operation="mailbox_accept",
                operation_id=event.event_id,
                cost=len(event.recipients),
                pending_cost=pending_cost,
            )
        encrypted_payload = self.store.encrypted_payload(event.payload, event.event_id)
        recipient_snapshots = {
            recipient: RecipientResolver.resolve_in_transaction(
                connection,
                event_actor=event.actor,
                event_domain_id=event.domain_id,
                recipient_id=recipient,
                now=now,
            )
            for recipient in event.recipients
        }
        provenance_time = datetime.fromtimestamp(now, UTC)
        provenance_object_type = (
            ProvenanceObjectType.TASK
            if event.event_type is EventType.TASK_ASSIGNMENT
            else ProvenanceObjectType.EVENT
        )
        sink_ceiling = self._provenance_sink_ceiling(connection, event)
        parent_references = self._causal_parent_references(connection, event)
        if parent_references:
            provenance_record = self.provenance.record_tainted_causal_derivation_in_transaction(
                connection,
                object_type=provenance_object_type,
                object_id=event.event_id,
                domain_id=event.domain_id,
                parent_provenance_digests=tuple(
                    reference.provenance_digest for reference in parent_references
                ),
                output_digest=event.payload_digest,
                classification=event.classification,
                allowed_sinks=sink_ceiling,
                policy_revision=event.policy_revision,
                recorded_at=provenance_time,
                when=provenance_time,
            )
        else:
            provenance_record = self.provenance.register_origin_in_transaction(
                connection,
                OriginRegistration(
                    object_type=provenance_object_type,
                    object_id=event.event_id,
                    domain_id=event.domain_id,
                    origin=ProvenanceOrigin(
                        kind=(
                            OriginKind.EXTERNAL_INPUT
                            if event.actor.kind is ActorKind.EXTERNAL_A2A
                            else OriginKind.INTERNAL_EVENT
                        ),
                        source_id=f"submission:{event.event_id}",
                        source_digest=event.payload_digest,
                        harness_id=event.actor.harness_id,
                        observed_at=provenance_time,
                    ),
                    classification=event.classification,
                    allowed_sinks=SinkSet(sinks=sink_ceiling),
                    policy_revision=event.policy_revision,
                    recorded_at=provenance_time,
                ),
                when=provenance_time,
            )
        provenance_reference = provenance_record.reference()
        connection.execute(
            """INSERT INTO events(
                event_id,domain_id,actor_json,event_type,classification,payload_encrypted,payload_digest,
                envelope_digest,envelope_json,idempotency_key,acceptance_fact,created_at,delivery_expires_at,
                effect_deadline,retention_delete_at,legal_hold,policy_revision,credential_epoch
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                event.event_id,
                event.domain_id,
                actor_json,
                event.event_type.value,
                event.classification.value,
                encrypted_payload,
                event.payload_digest,
                digest,
                canonical_json(envelope_metadata(event)).decode("utf-8"),
                event.idempotency_key,
                self.acceptance_fact.value,
                int(event.created_at.timestamp()),
                int(event.delivery_expires_at.timestamp()) if event.delivery_expires_at else None,
                int(event.effect_deadline.timestamp()) if event.effect_deadline else None,
                int(event.retention_delete_at.timestamp()) if event.retention_delete_at else None,
                int(event.legal_hold),
                event.policy_revision,
                event.credential_epoch,
            ),
        )
        connection.execute(
            """INSERT INTO event_provenance(
                   event_id,provenance_digest,reference_json,object_type,created_at
               ) VALUES(?,?,?,?,?)""",
            (
                event.event_id,
                provenance_record.provenance_digest,
                canonical_json(provenance_reference.model_dump(mode="json")).decode("utf-8"),
                provenance_object_type.value,
                now,
            ),
        )
        cursor_row = connection.execute("SELECT COALESCE(MAX(cursor),0) AS cursor FROM recipients").fetchone()
        cursor = int(cursor_row["cursor"])
        for recipient in event.recipients:
            cursor += 1
            connection.execute(
                "INSERT INTO recipients(event_id,recipient_id,cursor,current_fact,updated_at) VALUES(?,?,?,?,?)",
                (event.event_id, recipient, cursor, self.acceptance_fact.value, now),
            )
            snapshot = recipient_snapshots[recipient]
            connection.execute(
                """INSERT INTO recipient_address_snapshots(
                    event_id,recipient_id,snapshot_digest,snapshot_encrypted,resolved_at
                ) VALUES(?,?,?,?,?)""",
                (
                    event.event_id,
                    recipient,
                    canonical_digest(snapshot),
                    self.store.cipher.encrypt_json(
                        snapshot,
                        purpose=f"recipient-snapshot:{event.event_id}:{recipient}",
                    ),
                    now,
                ),
            )
        receipt_id = str(uuid4())
        acceptance_owner = AcceptingStorageBoundary(
            domain_id=event.domain_id,
            storage_profile=(
                "verified_durable"
                if self.acceptance_fact is DeliveryFact.ACCEPTED_DURABLE
                else "local_transactional"
            ),
            acceptance_fact=self.acceptance_fact,
            event_digest=digest,
        )
        connection.execute(
            """INSERT INTO receipts(
                receipt_id,event_id,recipient_id,fact,owner_actor_json,event_digest,detail_json,created_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                receipt_id,
                event.event_id,
                None,
                self.acceptance_fact.value,
                canonical_json(acceptance_owner.model_dump(mode="json")).decode("utf-8"),
                digest,
                canonical_json({"durability_profile": self.acceptance_fact.value}).decode("utf-8"),
                now,
            ),
        )
        audit_hash = self.store.append_audit(
            connection,
            {
                "action": "mailbox.accept",
                "actor": event.actor.audit_view(),
                "event_digest": digest,
                "event_id": event.event_id,
                "fact": self.acceptance_fact.value,
                "owner": acceptance_owner.model_dump(mode="json"),
                "recipients": list(event.recipients),
                "recipient_snapshot_digests": {
                    recipient: canonical_digest(snapshot)
                    for recipient, snapshot in recipient_snapshots.items()
                },
                "provenance_digest": provenance_record.provenance_digest,
                "provenance_authority_effect": "none",
            },
        )
        if self.admission is not None:
            self.admission._record_success_in_transaction(
                connection,
                breaker_key=self.admission._operation_key("mailbox_accept", event.domain_id),
                now=now,
            )
        self._signal_content_free_wake(event.recipients)
        return {
            "event_id": event.event_id,
            "fact": self.acceptance_fact.value,
            "duplicate": False,
            "receipt_id": receipt_id,
            "envelope_digest": digest,
            "provenance": provenance_reference.model_dump(mode="json"),
            "audit_hash": audit_hash,
        }

    def accept(self, event: EventEnvelope) -> dict[str, Any]:
        with self.store.transaction() as connection:
            return self._accept_in_transaction(connection, event)

    def _validated_event_and_payload(
        self,
        row: Mapping[str, Any],
        *,
        connection: Any | None = None,
    ) -> tuple[EventEnvelope, dict[str, Any]]:
        """Reconstruct exact accepted bytes before making a visibility choice.

        The marker is not trusted as a loose database flag.  Payload digest and
        complete canonical envelope digest must both verify first, so changing
        or deleting the marker cannot turn protected task custody into an
        ordinary mailbox disclosure.
        """

        try:
            raw_metadata = str(row["envelope_json"])
            metadata = json.loads(raw_metadata)
            if (
                not isinstance(metadata, dict)
                or canonical_json(metadata).decode("utf-8") != raw_metadata
            ):
                raise ValueError("envelope metadata is not canonical JSON")
            payload = self.store.decrypted_payload(
                str(row["payload_encrypted"]),
                str(row["event_id"]),
            )
            event = EventEnvelope.model_validate_json(
                canonical_json(metadata | {"payload": payload}),
                strict=True,
            )
            validate_event_digest(event)
        except Exception as exc:
            raise ConflictError("mailbox event failed immutable payload validation") from exc
        if envelope_digest(event) != row["envelope_digest"]:
            raise ConflictError("mailbox event failed immutable envelope validation")
        self._event_provenance_reference(event, connection=connection)
        return event, payload

    def _event_provenance_reference(
        self,
        event: EventEnvelope,
        *,
        connection: Any | None = None,
    ) -> ProvenanceReferenceV1:
        """Resolve and validate the mandatory authority-neutral event link."""

        if connection is None:
            link = self.store.fetch_one(
                "SELECT * FROM event_provenance WHERE event_id=?",
                (event.event_id,),
            )
        else:
            link = connection.execute(
                "SELECT * FROM event_provenance WHERE event_id=?",
                (event.event_id,),
            ).fetchone()
        if link is None:
            raise ConflictError("mailbox event lacks mandatory provenance")
        try:
            raw_reference = str(link["reference_json"])
            reference = ProvenanceReferenceV1.model_validate_json(
                raw_reference,
                strict=True,
            )
            if canonical_json(reference.model_dump(mode="json")).decode("utf-8") != raw_reference:
                raise ValueError("event provenance reference is not canonical")
            expected_object_type = (
                ProvenanceObjectType.TASK
                if event.event_type is EventType.TASK_ASSIGNMENT
                else ProvenanceObjectType.EVENT
            )
            if (
                str(link["provenance_digest"]) != reference.provenance_digest
                or str(link["object_type"]) != expected_object_type.value
            ):
                raise ValueError("event provenance link fields disagree")
            if connection is None:
                self.provenance.require_reference(
                    reference,
                    expected_domain_id=event.domain_id,
                    expected_content_digest=event.payload_digest,
                    expected_object_type=expected_object_type,
                    expected_classification=event.classification,
                    required_sinks=event.recipients,
                    expected_policy_revision=event.policy_revision,
                )
            else:
                self.provenance.require_reference_in_transaction(
                    connection,
                    reference,
                    expected_domain_id=event.domain_id,
                    expected_content_digest=event.payload_digest,
                    expected_object_type=expected_object_type,
                    expected_classification=event.classification,
                    required_sinks=event.recipients,
                    expected_policy_revision=event.policy_revision,
                )
        except Exception as exc:
            if isinstance(exc, ConflictError):
                raise
            raise ConflictError("mailbox event provenance failed immutable validation") from exc
        return reference

    @staticmethod
    def _task_payload_requires_grant(event: EventEnvelope) -> bool:
        """Fail closed for marked, typed, and legacy task-custody events."""

        return (
            event.payload_access == "task_grant_required"
            or event.event_type is EventType.TASK_ASSIGNMENT
            or (event.event_type is EventType.CONTROL and event.task_id is not None)
        )

    @staticmethod
    def _custody_reference(
        event: EventEnvelope,
        *,
        envelope_digest_value: str,
    ) -> dict[str, Any]:
        return {
            "schema": "agentnet.custody-payload-reference.v1",
            "event_id": event.event_id,
            "payload_digest": event.payload_digest,
            "envelope_digest": envelope_digest_value,
            "payload_access": "task_grant_required",
        }

    def generic_payload_view(
        self,
        row: Mapping[str, Any],
        *,
        now: int | None = None,
    ) -> dict[str, Any]:
        """Return an ordinary payload or a permanent non-disclosing reference.

        This method intentionally has no "include protected" switch.  A future
        protected-release API must perform its own exact task-grant ceremony;
        generic mailbox or conversation authority can never opt into bytes.
        """

        event, payload = self._validated_event_and_payload(row)
        provenance = self._event_provenance_reference(event)
        if self._task_payload_requires_grant(event):
            return {
                "payload": None,
                "payload_available": False,
                "payload_access": "task_grant_required",
                "payload_withheld_reason": "exact_task_grant_required",
                "custody_reference": self._custody_reference(
                    event,
                    envelope_digest_value=str(row["envelope_digest"]),
                ),
                "provenance": provenance.model_dump(mode="json"),
            }
        now = int(time.time()) if now is None else now
        retained = (
            row["retention_delete_at"] is None
            or row["retention_delete_at"] > now
            or bool(row["legal_hold"])
        )
        return {
            "payload": payload if retained else None,
            "payload_available": retained,
            "provenance": provenance.model_dump(mode="json"),
        }

    def reconcile(self, recipient_id: str, *, after_cursor: int = 0, limit: int = 100) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1000:
            raise ValueError("mailbox limit must be between 1 and 1000")
        rows = self.store.fetch_all(
            """SELECT e.*,r.cursor,r.current_fact FROM recipients r
               JOIN events e ON e.event_id=r.event_id
               WHERE r.recipient_id=? AND r.cursor>? ORDER BY r.cursor LIMIT ?""",
            (recipient_id, after_cursor, limit),
        )
        result: list[dict[str, Any]] = []
        now = int(time.time())
        for row in rows:
            payload_view = self.generic_payload_view(row, now=now)
            result.append(
                {
                    "cursor": row["cursor"],
                    "fact": row["current_fact"],
                    "event": json.loads(row["envelope_json"]),
                    "envelope_digest": row["envelope_digest"],
                    **payload_view,
                }
            )
        return result

    def _transition_in_transaction(
        self,
        connection: Any,
        *,
        event_id: str,
        recipient_id: str,
        proposed: DeliveryFact,
        owner_actor: VerifiedActor,
        detail: dict[str, Any] | None = None,
        workload_proof: WorkloadTransitionProof | None = None,
        now: int | None = None,
    ) -> dict[str, Any]:
        now = int(time.time()) if now is None else now
        row = connection.execute(
            """SELECT r.current_fact,e.envelope_digest,e.delivery_expires_at,e.effect_deadline,
                      e.domain_id,e.policy_revision
               FROM recipients r JOIN events e ON e.event_id=r.event_id
               WHERE r.event_id=? AND r.recipient_id=?""",
            (event_id, recipient_id),
        ).fetchone()
        if row is None:
            raise AuthorizationError("mailbox entry is not visible")
        current = DeliveryFact(row["current_fact"])
        if row["delivery_expires_at"] is not None and now >= row["delivery_expires_at"]:
            if current not in {DeliveryFact.COMPLETED, DeliveryFact.CANCELED, DeliveryFact.TOO_LATE}:
                proposed = DeliveryFact.EXPIRED
        require_transition(current, proposed)
        self._require_fact_owner(
            connection,
            actor=owner_actor,
            fact=proposed,
            recipient_id=recipient_id,
            event_domain_id=row["domain_id"],
            policy_revision=int(row["policy_revision"]),
            now=now,
            event_id=event_id,
            detail=detail,
            workload_proof=workload_proof,
        )
        connection.execute(
            "UPDATE recipients SET current_fact=?,updated_at=? WHERE event_id=? AND recipient_id=?",
            (proposed.value, now, event_id, recipient_id),
        )
        receipt_id = str(uuid4())
        connection.execute(
            """INSERT INTO receipts(receipt_id,event_id,recipient_id,fact,owner_actor_json,event_digest,detail_json,created_at)
               VALUES(?,?,?,?,?,?,?,?)""",
            (
                receipt_id,
                event_id,
                recipient_id,
                proposed.value,
                canonical_json(owner_actor.audit_view()).decode("utf-8"),
                row["envelope_digest"],
                canonical_json(detail or {}).decode("utf-8"),
                now,
            ),
        )
        audit_hash = self.store.append_audit(
            connection,
            {
                "action": "mailbox.transition",
                "event_id": event_id,
                "from": current.value,
                "owner": owner_actor.audit_view(),
                "recipient_id": recipient_id,
                "to": proposed.value,
            },
        )
        return {"receipt_id": receipt_id, "fact": proposed.value, "audit_hash": audit_hash}

    def transition(
        self,
        *,
        event_id: str,
        recipient_id: str,
        proposed: DeliveryFact,
        owner_actor: VerifiedActor,
        detail: dict[str, Any] | None = None,
        workload_proof: WorkloadTransitionProof | None = None,
    ) -> dict[str, Any]:
        with self.store.transaction() as connection:
            return self._transition_in_transaction(
                connection,
                event_id=event_id,
                recipient_id=recipient_id,
                proposed=proposed,
                owner_actor=owner_actor,
                detail=detail,
                workload_proof=workload_proof,
            )

    def expire_due(
        self,
        *,
        authoritative_now: datetime | None = None,
        authorizations: Mapping[tuple[str, str], ExpiryAuthorization] | None = None,
    ) -> int:
        """Expire due entries only with exact registered dispatcher proofs.

        The entire batch is one transaction.  If any eligible entry lacks its
        event/recipient-bound credential and proof, the operation fails closed
        and no earlier entry in the batch can remain mutated.
        """

        now = int((authoritative_now or datetime.now(UTC)).timestamp())
        with self.store.transaction() as connection:
            rows = connection.execute(
                """SELECT r.event_id,r.recipient_id,r.current_fact,e.envelope_digest,e.domain_id
                   FROM recipients r JOIN events e ON e.event_id=r.event_id
                   WHERE e.delivery_expires_at IS NOT NULL AND e.delivery_expires_at<=?""",
                (now,),
            ).fetchall()
            eligible: list[Any] = []
            for row in rows:
                current = DeliveryFact(row["current_fact"])
                if current in {DeliveryFact.COMPLETED, DeliveryFact.CANCELED, DeliveryFact.TOO_LATE, DeliveryFact.EXPIRED}:
                    continue
                try:
                    require_transition(current, DeliveryFact.EXPIRED)
                except ConflictError:
                    continue
                eligible.append(row)
            if eligible and authorizations is None:
                raise AuthorizationError("due mailbox expiry requires exact registered dispatcher authorizations")
            for row in eligible:
                key = (str(row["event_id"]), str(row["recipient_id"]))
                authorization = None if authorizations is None else authorizations.get(key)
                if authorization is None:
                    raise AuthorizationError(
                        "due mailbox expiry lacks its exact event and recipient dispatcher authorization"
                    )
                detail = {"authoritative_clock": now}
                self._transition_in_transaction(
                    connection,
                    event_id=key[0],
                    recipient_id=key[1],
                    proposed=DeliveryFact.EXPIRED,
                    owner_actor=authorization.resolved_actor,
                    detail=detail,
                    workload_proof=authorization.proof,
                    now=now,
                )
            if eligible:
                self.store.append_audit(
                    connection,
                    {"action": "mailbox.expire", "count": len(eligible), "clock": now},
                )
        return len(eligible)
