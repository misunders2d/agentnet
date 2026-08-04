"""One durable directional task-custody state machine for every ingress.

An accepted result means mailbox custody only.  It never grants protected data,
tool, or effect authority.  Peer/upward/lateral task bytes are encrypted in a
proposal relation that is intentionally disjoint from the executable mailbox.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.approval.service import IndependentApprovalVerifier
from agentnet.authorization.decision import AuthorizationDecision, DecisionRecorder
from agentnet.authorization.grants import epoch_seconds
from agentnet.authorization.policy import PolicyEngine, validate_actor_state
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import envelope_digest, validate_event_digest
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import AttenuationPolicy
from agentnet.organization.conflicts import (
    TaskConflictAdjudication,
    TaskConflictOutcome,
    TaskConflictService,
    TaskExecutionIntent,
)
from agentnet.organization.relationships import AssignmentScope, RelationshipService
from agentnet.protocol.models import Classification, DeliveryFact, EventEnvelope, EventType, TaskGrant
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.relationship_governance_schema import (
    require_relationship_governance_schema,
)
from agentnet.storage.task_custody_schema import require_task_custody_schema


class TaskIngressKind(StrEnum):
    DIRECT = "direct"
    CONVERSATION_TASK = "conversation_task"
    CONVERSATION_HANDOFF = "conversation_handoff"
    A2A_TASK = "a2a_task"
    RELAY_TASK = "relay_task"
    ROOM_TASK = "room_task"
    FEDERATION_TASK = "federation_task"
    SEMANTIC_WORKER = "semantic_worker"


class TaskProposalState(StrEnum):
    PENDING = "pending"
    RESUMED = "resumed"
    DENIED = "denied"
    EXPIRED = "expired"
    INVALIDATED = "invalidated"


class AssignmentRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    actor: VerifiedActor
    recipient_harness_id: str = Field(min_length=1)
    task_type: str = Field(min_length=1)
    resources: frozenset[str] = Field(min_length=1)
    data_classes: frozenset[Classification] = Field(min_length=1)
    tools: frozenset[str] = frozenset()
    budget: int = Field(default=0, ge=0)
    concurrency: int = Field(default=1, ge=1)
    deadline: datetime | None = None
    expected_relationship_revision: int | None = Field(default=None, ge=1)
    policy_revision: int = Field(ge=1)
    intent: TaskExecutionIntent | None = None
    context: dict[str, Any] = Field(default_factory=dict)

    @field_validator("deadline")
    @classmethod
    def deadline_is_aware(cls, value: datetime | None) -> datetime | None:
        if value is not None and (value.tzinfo is None or value.utcoffset() is None):
            raise ValueError("assignment deadline must be timezone-aware")
        return value

    @model_validator(mode="after")
    def complete_intent_scope(self) -> "AssignmentRequest":
        if self.intent is not None and frozenset(
            item.resource for item in self.intent.resources
        ) != self.resources:
            raise ValueError("task intent must cover the complete exact assignment resource set")
        return self


class AssignmentDecision(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    policy_decision_id: str
    fact: Literal[DeliveryFact.ACCEPTED_QUEUED, DeliveryFact.PENDING_HUMAN]
    reason: str
    relationship_id: str | None = None
    relationship_revision: int | None = None
    effective_deadline: datetime | None = None
    proposal_id: str | None = None
    request_digest: str | None = None
    proposal_revision: int | None = None
    recipient_authority_id: str | None = None
    data_access_authorized: Literal[False] = False
    effect_authorized: Literal[False] = False


class TaskProposalOutcome(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    proposal_id: str
    state: TaskProposalState
    reason: str
    request_digest: str
    revision: int
    resumed_event_id: str | None = None
    fact: DeliveryFact = DeliveryFact.PENDING_HUMAN
    data_access_authorized: Literal[False] = False
    effect_authorized: Literal[False] = False


class _Evaluation(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, arbitrary_types_allowed=True)

    decision: AssignmentDecision
    recipient_authority_id: str | None = None
    recipient_credential_epoch: int = 0
    domain_revocation_epoch: int = 0
    relationship_digest: str
    effective_deadline: datetime | None = None


_FATAL_PROPOSAL_REASONS = frozenset(
    {
        "domain_not_active",
        "stale_policy_revision",
        "actor_kind_has_no_positive_authority",
        "missing_harness_state",
        "harness_domain_mismatch",
        "harness_not_active",
        "binding_assurance_mismatch",
        "stale_harness_credential_epoch",
        "missing_principal_state",
        "principal_binding_mismatch",
        "principal_not_active",
        "missing_guest_state",
        "guest_binding_mismatch",
        "guest_not_active",
        "missing_credential_state",
        "credential_binding_mismatch",
        "credential_not_active",
        "credential_outside_validity",
        "recipient_harness_not_active",
    }
)


class AssignmentService:
    """Evaluate, hold, approve, and resume exact task bytes atomically."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        mailbox: MailboxService | None = None,
        policy: PolicyEngine | None = None,
        approval_verifier: IndependentApprovalVerifier | None = None,
        attenuation_policy: AttenuationPolicy | None = None,
        outage_gate: OutageGate | None = None,
    ) -> None:
        self.store = store
        self.mailbox = mailbox
        self.policy = policy
        self.approval_verifier = approval_verifier
        self.recorder = DecisionRecorder(store)
        self.attenuation_policy = attenuation_policy
        self.outage_gate = outage_gate
        require_task_custody_schema(store)
        require_relationship_governance_schema(store)
        self.conflicts = TaskConflictService(
            store,
            release_callback=self._release_conflicted_continuation,
        )

    @staticmethod
    def _canonical_request(request: AssignmentRequest) -> AssignmentRequest:
        if request.intent is not None:
            return request
        return request.model_copy(
            update={
                "intent": TaskExecutionIntent.conservative(
                    task_type=request.task_type,
                    resources=request.resources,
                )
            }
        )

    @staticmethod
    def _relationship_digest(row: Any | None) -> str:
        if row is None:
            return canonical_digest({"relationship": None})
        return canonical_digest(
            {
                "schema": "agentnet.relationship-governance-binding.v1",
                "transaction_id": row["transaction_id"],
                "transaction_digest": row["transaction_digest"],
                "relationship_id": row["relationship_id"],
                "domain_id": row["domain_id"],
                "administrator_harness_id": row["administrator_harness_id"],
                "subordinate_harness_id": row["subordinate_harness_id"],
                "administrator_owner_kind": row["administrator_owner_kind"],
                "administrator_owner_id": row["administrator_owner_id"],
                "subordinate_owner_kind": row["subordinate_owner_kind"],
                "subordinate_owner_id": row["subordinate_owner_id"],
                "may_assign": bool(row["may_assign"]),
                "assignment_scope": json.loads(row["assignment_scope_json"]),
                "relationship_revision": int(row["relationship_revision"]),
                "relationship_expires_at": int(row["relationship_expires_at"]),
                "lifecycle_revision": int(row["lifecycle_revision"]),
                "state": row["state"],
                "activation_basis": row["activation_basis"],
                "proposal_policy_revision": int(row["proposal_policy_revision"]),
                "proposal_domain_revocation_epoch": int(
                    row["proposal_domain_revocation_epoch"]
                ),
                "proposal_administrator_credential_epoch": int(
                    row["proposal_administrator_credential_epoch"]
                ),
                "proposal_subordinate_credential_epoch": int(
                    row["proposal_subordinate_credential_epoch"]
                ),
                "proposal_lineage_revocation_epoch": int(
                    row["proposal_lineage_revocation_epoch"]
                ),
                "approval_receipt_id": row["approval_receipt_id"],
                "approval_receipt_digest": row["approval_receipt_digest"],
                "approval_approver_authority_kind": row[
                    "approval_approver_authority_kind"
                ],
                "policy_exception_id": row["policy_exception_id"],
                "activated_at": int(row["activated_at"]),
                "revoked_at": int(row["revoked_at"]) if row["revoked_at"] is not None else None,
            }
        )

    @staticmethod
    def _latest_relationship(connection: Any, request: AssignmentRequest) -> Any | None:
        return connection.execute(
            """
            SELECT * FROM relationship_governance_transactions
             WHERE domain_id=? AND administrator_harness_id=? AND subordinate_harness_id=?
               AND state='active'
             ORDER BY relationship_revision DESC LIMIT 1
            """,
            (request.actor.domain_id, request.actor.harness_id, request.recipient_harness_id),
        ).fetchone()

    @staticmethod
    def _scope_reason(
        row: Any,
        request: AssignmentRequest,
        *,
        when: datetime,
        deadline_anchor: datetime,
    ) -> tuple[bool, str, datetime | None]:
        if request.expected_relationship_revision is not None and (
            request.expected_relationship_revision != int(row["relationship_revision"])
        ):
            return False, "stale_relationship_revision", request.deadline
        if row["state"] != "active" or row["activated_at"] is None:
            return False, "missing_relationship_acceptance", request.deadline
        if not bool(row["may_assign"]):
            return False, "relationship_does_not_allow_assignment", request.deadline
        if row["revoked_at"] is not None:
            return False, "relationship_revoked", request.deadline
        if int(row["relationship_expires_at"]) <= epoch_seconds(when):
            return False, "relationship_expired", request.deadline
        try:
            scope = AssignmentScope.model_validate(json.loads(row["assignment_scope_json"]))
        except Exception:
            return False, "invalid_assignment_scope", request.deadline
        effective_deadline = request.deadline
        if effective_deadline is None:
            # Assignment events are extension-owned immutable records.  Their
            # normalized creation instant is therefore a stable retry anchor,
            # unlike the wall clock at the time a duplicate is received.
            # Whole seconds match both SQLite and PostgreSQL persistence.  One
            # second is reserved before relationship expiry so the established
            # strict explicit-deadline rule remains unchanged.
            anchor = min(epoch_seconds(deadline_anchor), epoch_seconds(when))
            effective_epoch = min(
                anchor + scope.max_duration_seconds,
                int(row["relationship_expires_at"]) - 1,
            )
            effective_deadline = datetime.fromtimestamp(effective_epoch, UTC)
        allowed, reason = scope.allows(
            task_type=request.task_type,
            resources=request.resources,
            data_classes=request.data_classes,
            tools=request.tools,
            budget=request.budget,
            concurrency=request.concurrency,
            deadline=effective_deadline,
            when=when,
            relationship_expires_at=datetime.fromtimestamp(row["relationship_expires_at"], UTC),
        )
        return allowed, reason, effective_deadline

    def _evaluate_in_transaction(
        self,
        connection: Any,
        request: AssignmentRequest,
        *,
        when: datetime,
        deadline_anchor: datetime | None = None,
    ) -> _Evaluation:
        if self.outage_gate is not None:
            self.outage_gate.require_low_risk_continuity()
        denial, policy_revision = validate_actor_state(
            connection,
            actor=request.actor,
            expected_policy_revision=request.policy_revision,
            when=when,
        )
        reason = denial
        if reason is None and self.attenuation_policy is not None:
            reason = self.attenuation_policy.denial_reason(request.actor.binding_assurance)
        domain = connection.execute(
            "SELECT revocation_epoch FROM domains WHERE domain_id=?", (request.actor.domain_id,)
        ).fetchone()
        recipient = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?", (request.recipient_harness_id,)
        ).fetchone()
        recipient_authority_id: str | None = None
        recipient_credential_epoch = 0
        if recipient is not None:
            recipient_authority_id = recipient["principal_id"] or recipient["guest_id"]
            recipient_credential_epoch = int(recipient["credential_epoch"])
        if reason is None and (
            recipient is None
            or recipient["domain_id"] != request.actor.domain_id
            or recipient["status"] != "active"
            or not recipient_authority_id
        ):
            reason = "recipient_harness_not_active"

        row = self._latest_relationship(connection, request) if reason is None else None
        if row is not None:
            row = RelationshipService.expire_active_in_transaction(
                self.store,
                connection,
                row,
                when=when,
            )
        relationship_id = str(row["relationship_id"]) if row is not None else None
        relationship_revision = int(row["relationship_revision"]) if row is not None else None
        accepted = False
        effective_deadline = request.deadline
        if reason is None and row is None:
            reason = "no_active_directed_assignment_relationship"
        elif reason is None and row is not None:
            reason = RelationshipService.authority_binding_denial(
                connection,
                row,
                current_policy_revision=policy_revision,
                approval_verifier=self.approval_verifier,
                when=when,
            )
            if reason is None:
                accepted, reason, effective_deadline = self._scope_reason(
                    row,
                    request,
                    when=when,
                    deadline_anchor=deadline_anchor or when,
                )

        fact = DeliveryFact.ACCEPTED_QUEUED if accepted else DeliveryFact.PENDING_HUMAN
        decision = AuthorizationDecision(
            occurred_at=when,
            actor=request.actor,
            action="organization.assign_task_custody",
            resource={
                "recipient_harness_id": request.recipient_harness_id,
                "task_type": request.task_type,
                "resources": sorted(request.resources),
            },
            context={
                "request": request.context,
                "data_classes": sorted(value.value for value in request.data_classes),
                "tools": sorted(request.tools),
                "budget": request.budget,
                "concurrency": request.concurrency,
                "requested_deadline": request.deadline.isoformat() if request.deadline else None,
                "effective_deadline": (
                    effective_deadline.isoformat() if effective_deadline else None
                ),
                "relationship_id": relationship_id,
                "relationship_revision": relationship_revision,
                "delivery_fact": fact.value,
                "data_access_authorized": False,
                "effect_authorized": False,
            },
            allowed=accepted,
            reason=reason or "assignment_within_custody_scope",
            policy_revision=policy_revision,
        )
        recorded = self.recorder.record(connection, decision)
        return _Evaluation(
            decision=AssignmentDecision(
                policy_decision_id=recorded.decision_id,
                fact=fact,
                reason=recorded.reason,
                relationship_id=relationship_id,
                relationship_revision=relationship_revision,
                effective_deadline=effective_deadline,
                recipient_authority_id=recipient_authority_id,
            ),
            recipient_authority_id=recipient_authority_id,
            recipient_credential_epoch=recipient_credential_epoch,
            domain_revocation_epoch=int(domain["revocation_epoch"]) if domain is not None else 0,
            relationship_digest=self._relationship_digest(row),
            effective_deadline=effective_deadline,
        )

    def decide(self, request: AssignmentRequest, *, when: datetime | None = None) -> AssignmentDecision:
        """Evaluate custody direction without accepting any task bytes.

        Real ingress must call :meth:`submit_event`; retaining this pure decision
        operation keeps administrative previews from accidentally becoming work.
        """

        when = when or datetime.now(UTC)
        request = self._canonical_request(request)
        with self.store.transaction() as connection:
            return self._evaluate_in_transaction(connection, request, when=when).decision

    @staticmethod
    def _validate_submission(request: AssignmentRequest, event: EventEnvelope, ingress: TaskIngressKind) -> None:
        validate_event_digest(event)
        if event.actor.audit_view() != request.actor.audit_view():
            raise AuthorizationError("task event actor does not bind the assignment requester")
        if event.domain_id != request.actor.domain_id or event.recipients != (request.recipient_harness_id,):
            raise AuthorizationError("task event does not bind the exact domain and recipient")
        if event.policy_revision != request.policy_revision:
            raise AuthorizationError("task event policy revision does not bind the assignment request")
        if event.classification not in request.data_classes:
            raise AuthorizationError("task event classification is outside the exact assignment request")
        if event.effect_deadline != request.deadline:
            raise AuthorizationError(
                "task event effect deadline does not bind the exact assignment request"
            )
        expected_type = EventType.CONTROL if ingress is TaskIngressKind.CONVERSATION_HANDOFF else EventType.TASK_ASSIGNMENT
        if event.event_type is not expected_type:
            raise ValidationError("typed task ingress has the wrong immutable event type")

    @staticmethod
    def _proposal_expiry(
        request: AssignmentRequest,
        event: EventEnvelope,
        *,
        when: datetime,
        proposal_expires_at: datetime | None,
    ) -> datetime:
        candidates = [event.created_at + timedelta(hours=24)]
        for value in (proposal_expires_at, event.delivery_expires_at, request.deadline):
            if value is not None:
                candidates.append(value)
        expires_at = min(candidates)
        if expires_at <= when:
            raise ValidationError("task proposal must expire in the future")
        return expires_at

    @staticmethod
    def _normalize_event_clock(event: EventEnvelope, *, when: datetime) -> EventEnvelope:
        """Replace a future/untrusted ingress timestamp with receipt time.

        Local constructors already create events before calling the service, so
        this is normally a no-op.  It also makes deterministic/frozen-clock
        ingresses and remote signed timestamps safe: a future timestamp can
        never extend the consented task duration beyond the accepting clock.
        """

        if when.tzinfo is None or when.utcoffset() is None:
            raise ValidationError("assignment evaluation time must be timezone-aware")
        created_at = min(event.created_at, when)
        return event if created_at == event.created_at else event.model_copy(
            update={"created_at": created_at}
        )

    @staticmethod
    def _bind_custody_deadline(
        request: AssignmentRequest,
        event: EventEnvelope,
        *,
        effective_deadline: datetime,
        when: datetime,
    ) -> tuple[AssignmentRequest, EventEnvelope]:
        if effective_deadline <= when:
            raise ValidationError("task custody deadline must be in the future")
        delivery_expires_at = event.delivery_expires_at
        if delivery_expires_at is None or delivery_expires_at > effective_deadline:
            delivery_expires_at = effective_deadline
        if delivery_expires_at <= when:
            raise ValidationError("task delivery lifetime is already expired")
        return (
            request.model_copy(update={"deadline": effective_deadline}),
            event.model_copy(
                update={
                    "effect_deadline": effective_deadline,
                    "delivery_expires_at": delivery_expires_at,
                    "payload_access": "task_grant_required",
                }
            ),
        )

    @staticmethod
    def _redacted_summary(
        *,
        proposal_id: str,
        request: AssignmentRequest,
        ingress: TaskIngressKind,
        request_digest: str,
        expires_at: datetime,
    ) -> dict[str, Any]:
        # No task summary, payload, resource identifier, artifact name, URL,
        # tool name, or conversation content is released at this boundary.
        return {
            "proposal_id": proposal_id,
            "ingress_kind": ingress.value,
            "sender_harness_id": request.actor.harness_id,
            "recipient_harness_id": request.recipient_harness_id,
            "task_type_present": bool(request.task_type),
            "resource_count": len(request.resources),
            "data_classes": sorted(item.value for item in request.data_classes),
            "tool_count": len(request.tools),
            "request_digest": request_digest,
            "expires_at": expires_at.isoformat(),
        }

    def _request_digest(
        self,
        *,
        request: AssignmentRequest,
        event: EventEnvelope,
        ingress: TaskIngressKind,
        continuation: dict[str, Any],
        relationship_digest: str,
    ) -> str:
        event_binding = event.model_dump(mode="json", exclude_none=True)
        event_binding.pop("payload", None)
        # These are extension-owned storage/retention timestamps, not caller
        # bytes.  Excluding them keeps retries stable while payload_digest,
        # task/effect constraints, endpoints, and every authority epoch remain
        # exact.  The complete immutable event has its own event_digest column.
        event_binding.pop("created_at", None)
        event_binding.pop("retention_delete_at", None)
        return canonical_digest(
            {
                "request": request.model_dump(mode="json", exclude_none=True),
                "event_binding": event_binding,
                "ingress_kind": ingress.value,
                "continuation_digest": canonical_digest(continuation),
                "relationship_digest": relationship_digest,
            }
        )

    def _apply_continuation(
        self,
        connection: Any,
        *,
        continuation: dict[str, Any],
        event: EventEnvelope,
        now: int,
        initial: bool,
    ) -> None:
        kind = continuation.get("kind")
        if not kind or (initial and not continuation.get("apply_on_initial", False)):
            return
        if kind in {"conversation_task", "conversation_handoff"}:
            conversation_id = str(continuation["conversation_id"])
            task_id = str(continuation["task_id"])
            conversation = connection.execute(
                "SELECT state FROM conversations WHERE conversation_id=? AND domain_id=?",
                (conversation_id, event.domain_id),
            ).fetchone()
            active_sender = connection.execute(
                """SELECT 1 FROM conversation_members
                     WHERE conversation_id=? AND authority_id=? AND status='active'""",
                (conversation_id, continuation["actor_authority_id"]),
            ).fetchone()
            active_recipient = connection.execute(
                """SELECT 1 FROM conversation_members
                     WHERE conversation_id=? AND harness_id=? AND status='active'""",
                (conversation_id, event.recipients[0]),
            ).fetchone()
            if conversation is None or conversation["state"] != "active" or not active_sender or not active_recipient:
                raise AuthorizationError("conversation task authority changed before custody resume")
            if kind == "conversation_task":
                existing = connection.execute(
                    "SELECT 1 FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, task_id),
                ).fetchone()
                if existing is not None:
                    raise ConflictError("conversation task identifier changed before custody resume")
            else:
                existing = connection.execute(
                    "SELECT * FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, task_id),
                ).fetchone()
                if (
                    existing is None
                    or existing["assignee_harness_id"] != continuation["from_harness_id"]
                    or existing["latest_event_id"] != continuation["source_event_id"]
                    or existing["state"] in {"completed", "failed_terminal", "canceled", "effect_unknown"}
                ):
                    raise AuthorizationError("conversation handoff state drifted before custody resume")
            connection.execute(
                """INSERT INTO conversation_actions(
                    event_id,conversation_id,thread_id,action_kind,parent_event_id,task_id,
                    actor_authority_id,actor_harness_id,action_digest,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    event.event_id,
                    conversation_id,
                    continuation["thread_id"],
                    "task" if kind == "conversation_task" else "handoff",
                    continuation.get("parent_event_id"),
                    task_id,
                    continuation["actor_authority_id"],
                    continuation["actor_harness_id"],
                    event.payload_digest,
                    now,
                ),
            )
            if kind == "conversation_task":
                connection.execute(
                    """INSERT INTO conversation_tasks(
                        conversation_id,task_id,creator_authority_id,assignee_harness_id,
                        source_event_id,latest_event_id,state,result_digest,updated_at
                    ) VALUES(?,?,?,?,?,?,'requested',NULL,?)""",
                    (
                        conversation_id,
                        task_id,
                        continuation["actor_authority_id"],
                        event.recipients[0],
                        event.event_id,
                        event.event_id,
                        now,
                    ),
                )
            else:
                connection.execute(
                    """UPDATE conversation_tasks
                          SET assignee_harness_id=?,latest_event_id=?,state='handed_off',updated_at=?
                        WHERE conversation_id=? AND task_id=?""",
                    (event.recipients[0], event.event_id, now, conversation_id, task_id),
                )
            connection.execute(
                "UPDATE conversations SET updated_at=? WHERE conversation_id=?", (now, conversation_id)
            )
        elif kind == "a2a_task":
            row = connection.execute(
                "SELECT task_encrypted FROM a2a_tasks WHERE task_id=?", (continuation["task_id"],)
            ).fetchone()
            if row is None:
                raise ConflictError("A2A proposal continuation is unavailable")
            task_value = self.store.cipher.decrypt_json(
                row["task_encrypted"], purpose=f"a2a-task:{continuation['task_id']}"
            )
            metadata = task_value.setdefault("metadata", {})
            metadata.update(
                {
                    "agentnetDisposition": "corporate_task_queued",
                    "agentnetExecutable": True,
                    "agentnetEventId": event.event_id,
                    "agentnetAssignmentFact": DeliveryFact.ACCEPTED_QUEUED.value,
                }
            )
            connection.execute(
                """UPDATE a2a_tasks
                      SET executable=1,corporate_event_id=?,task_encrypted=?,updated_at=?
                    WHERE task_id=? AND executable=0""",
                (
                    event.event_id,
                    self.store.cipher.encrypt_json(task_value, purpose=f"a2a-task:{continuation['task_id']}"),
                    now,
                    continuation["task_id"],
                ),
            )
        elif kind == "relay_task":
            cursor = connection.execute(
                """UPDATE server_agent_relay_inbox
                      SET state='recipient_committed',local_event_id=?,updated_at=?
                    WHERE packet_id=? AND state='authorized_pending'""",
                (event.event_id, now, continuation["packet_id"]),
            )
            if cursor.rowcount != 1:
                raise ConflictError("relay proposal continuation changed before custody resume")
            if self.mailbox is not None and self.mailbox.admission is not None:
                terminalized = self.mailbox.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="relay_inbound",
                    source_id=continuation["packet_id"],
                    now=now,
                )
                if not terminalized:
                    raise ConflictError("relay task work reservation was not pending")
        else:
            raise ValidationError("unknown task-custody continuation kind")

    def _release_conflicted_continuation(
        self,
        connection: Any,
        event_id: str,
        continuation: dict[str, Any],
        now: int,
    ) -> None:
        if self.mailbox is None:  # pragma: no cover - constructor composition invariant
            raise AuthorizationError("task conflict continuation requires the composed mailbox")
        row = connection.execute("SELECT * FROM events WHERE event_id=?", (event_id,)).fetchone()
        if row is None:
            raise ConflictError("task conflict continuation event is unavailable")
        event, _payload = self.mailbox._validated_event_and_payload(row)
        self._apply_continuation(
            connection,
            continuation=continuation,
            event=event,
            now=now,
            initial=False,
        )

    def _submit_in_transaction(
        self,
        connection: Any,
        request: AssignmentRequest,
        event: EventEnvelope,
        *,
        ingress: TaskIngressKind,
        continuation: dict[str, Any],
        proposal_expires_at: datetime | None,
        when: datetime,
    ) -> dict[str, Any]:
        if self.mailbox is None:
            raise AuthorizationError("task custody requires the composed mailbox service")
        request = self._canonical_request(request)
        event = self._normalize_event_clock(event, when=when)
        self._validate_submission(request, event, ingress)
        if request.deadline is not None and request.deadline <= when:
            raise ValidationError("task custody deadline must be in the future")
        now = epoch_seconds(when)
        existing = connection.execute(
            """SELECT * FROM task_custody_proposals
                 WHERE domain_id=? AND sender_harness_id=? AND idempotency_key=?""",
            (request.actor.domain_id, request.actor.harness_id, event.idempotency_key),
        ).fetchone()
        evaluation = self._evaluate_in_transaction(
            connection,
            request,
            when=when,
            deadline_anchor=event.created_at,
        )
        decision = evaluation.decision
        relationship_digest = evaluation.relationship_digest

        restored_conversation_retry = (
            existing is not None
            and ingress in {
                TaskIngressKind.CONVERSATION_TASK,
                TaskIngressKind.CONVERSATION_HANDOFF,
            }
            and request.deadline is None
            and event.delivery_expires_at is None
        )
        if restored_conversation_retry:
            proposal_id = str(existing["proposal_id"])
            stored_request = AssignmentRequest.model_validate_json(
                canonical_json(
                    self.store.cipher.decrypt_json(
                        existing["request_encrypted"],
                        purpose=f"task-proposal-request:{proposal_id}",
                    )
                )
            )
            stored_event = EventEnvelope.model_validate(
                self.store.cipher.decrypt_json(
                    existing["event_encrypted"],
                    purpose=f"task-proposal-event:{proposal_id}",
                )
            )
            effective_deadline = stored_request.deadline
            if (
                effective_deadline is None
                or stored_event.effect_deadline != effective_deadline
                or stored_event.delivery_expires_at is None
            ):
                raise ConflictError("stored conversation task deadline binding is incomplete")
            request = request.model_copy(update={"deadline": effective_deadline})
            event = event.model_copy(
                update={
                    "effect_deadline": stored_event.effect_deadline,
                    "delivery_expires_at": stored_event.delivery_expires_at,
                    "payload_access": "task_grant_required",
                }
            )
        else:
            if decision.fact is DeliveryFact.ACCEPTED_QUEUED:
                effective_deadline = evaluation.effective_deadline
                if effective_deadline is None:  # pragma: no cover - defensive invariant
                    raise AuthorizationError("automatic task custody lacks an exact deadline")
            else:
                proposal_deadline = self._proposal_expiry(
                    request,
                    event,
                    when=when,
                    proposal_expires_at=proposal_expires_at,
                )
                # A proposal has no relationship scope from which to derive a task
                # duration.  Keep omitted-deadline bytes non-executable and bound
                # them to a conservative one-hour custody window.  A later edge
                # may resume only when that exact stored bound fits its scope.
                effective_deadline = request.deadline or min(
                    proposal_deadline,
                    event.created_at + timedelta(hours=1),
                )
            request, event = self._bind_custody_deadline(
                request,
                event,
                effective_deadline=effective_deadline,
                when=when,
            )
        decision = decision.model_copy(update={"effective_deadline": effective_deadline})
        request_digest = self._request_digest(
            request=request,
            event=event,
            ingress=ingress,
            continuation=continuation,
            relationship_digest=relationship_digest,
        )

        if existing is not None:
            if (
                existing["request_digest"] != request_digest
                or existing["ingress_kind"] != ingress.value
            ):
                raise IdempotencyConflict("task proposal idempotency key names different exact bytes")
            return {
                **decision.model_dump(mode="json"),
                "fact": DeliveryFact.PENDING_HUMAN.value,
                "proposal_id": existing["proposal_id"],
                "request_digest": existing["request_digest"],
                "proposal_revision": int(existing["revision"]),
                "state": existing["state"],
                "duplicate": True,
            }

        if decision.fact is DeliveryFact.ACCEPTED_QUEUED:
            accepted = self.mailbox._accept_in_transaction(
                connection,
                event,
                now=now,
                pending_cost=0 if ingress is TaskIngressKind.RELAY_TASK else None,
            )
            if not accepted["duplicate"]:
                connection.execute(
                    "UPDATE recipients SET current_fact=?,updated_at=? WHERE event_id=? AND recipient_id=?",
                    (
                        DeliveryFact.ACCEPTED_QUEUED.value,
                        now,
                        accepted["event_id"],
                        request.recipient_harness_id,
                    ),
                )
            admission = self.conflicts.record_accepted_in_transaction(
                connection,
                event_id=str(accepted["event_id"]),
                domain_id=request.actor.domain_id,
                recipient_harness_id=request.recipient_harness_id,
                sender_harness_id=request.actor.harness_id or "",
                sender_authority_id=request.actor.positive_authority_id or "",
                authority_basis="directed_relationship",
                relationship_id=decision.relationship_id,
                relationship_revision=decision.relationship_revision or 0,
                intent=request.intent,
                continuation=continuation,
                deadline=effective_deadline,
                when=when,
            )
            if not accepted["duplicate"] and admission.fact is DeliveryFact.ACCEPTED_QUEUED:
                self._apply_continuation(
                    connection, continuation=continuation, event=event, now=now, initial=True
                )
                self.conflicts.mark_continuation_applied_in_transaction(
                    connection,
                    event_id=str(accepted["event_id"]),
                )
            self.store.append_audit(
                connection,
                {
                    "action": "task_custody.accepted_queued",
                    "event_id": accepted["event_id"],
                    "policy_decision_id": decision.policy_decision_id,
                    "relationship_id": decision.relationship_id,
                    "relationship_revision": decision.relationship_revision,
                    "request_digest": request_digest,
                    "task_conflict_ids": list(admission.conflict_ids),
                    "task_custody_fact": admission.fact.value,
                },
            )
            return accepted | decision.model_dump(mode="json") | {
                "fact": admission.fact.value,
                "conflict_ids": list(admission.conflict_ids),
                "request_digest": request_digest,
            }

        if decision.reason in _FATAL_PROPOSAL_REASONS or evaluation.recipient_authority_id is None:
            return decision.model_dump(mode="json") | {
                "fact": DeliveryFact.PENDING_HUMAN.value,
                "request_digest": request_digest,
                "state": TaskProposalState.INVALIDATED.value,
                "duplicate": False,
            }

        expires_at = min(proposal_deadline, effective_deadline)
        proposal_id = str(uuid4())
        redacted = self._redacted_summary(
            proposal_id=proposal_id,
            request=request,
            ingress=ingress,
            request_digest=request_digest,
            expires_at=expires_at,
        )
        connection.execute(
            """INSERT INTO task_custody_proposals(
                proposal_id,domain_id,sender_harness_id,sender_authority_id,
                recipient_harness_id,recipient_authority_id,ingress_kind,idempotency_key,
                request_digest,event_digest,request_encrypted,event_encrypted,
                continuation_encrypted,redacted_summary_json,policy_revision,
                domain_revocation_epoch,sender_credential_epoch,recipient_credential_epoch,
                relationship_id,relationship_revision,relationship_digest,state,state_reason,
                revision,expires_at,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending',?,1,?,?,?)""",
            (
                proposal_id,
                request.actor.domain_id,
                request.actor.harness_id,
                request.actor.positive_authority_id,
                request.recipient_harness_id,
                evaluation.recipient_authority_id,
                ingress.value,
                event.idempotency_key,
                request_digest,
                envelope_digest(event),
                self.store.cipher.encrypt_json(
                    request.model_dump(mode="json"), purpose=f"task-proposal-request:{proposal_id}"
                ),
                self.store.cipher.encrypt_json(
                    event.model_dump(mode="json"), purpose=f"task-proposal-event:{proposal_id}"
                ),
                self.store.cipher.encrypt_json(continuation, purpose=f"task-proposal-continuation:{proposal_id}"),
                canonical_json(redacted).decode("utf-8"),
                request.policy_revision,
                evaluation.domain_revocation_epoch,
                request.actor.credential_epoch,
                evaluation.recipient_credential_epoch,
                decision.relationship_id,
                decision.relationship_revision or 0,
                relationship_digest,
                decision.reason,
                epoch_seconds(expires_at),
                now,
                now,
            ),
        )
        self.store.append_audit(
            connection,
            {
                "action": "task_custody.proposal_created",
                "ingress_kind": ingress.value,
                "policy_decision_id": decision.policy_decision_id,
                "proposal_id": proposal_id,
                "recipient_authority_id": evaluation.recipient_authority_id,
                "request_digest": request_digest,
            },
        )
        return decision.model_dump(mode="json") | {
            "fact": DeliveryFact.PENDING_HUMAN.value,
            "proposal_id": proposal_id,
            "request_digest": request_digest,
            "proposal_revision": 1,
            "state": TaskProposalState.PENDING.value,
            "duplicate": False,
        }

    def submit_event(
        self,
        request: AssignmentRequest,
        event: EventEnvelope,
        *,
        ingress: TaskIngressKind = TaskIngressKind.DIRECT,
        continuation: dict[str, Any] | None = None,
        proposal_expires_at: datetime | None = None,
        when: datetime | None = None,
        connection: Any | None = None,
    ) -> dict[str, Any]:
        """Accept downward custody or atomically create an invisible proposal."""

        when = when or datetime.now(UTC)
        continuation = dict(continuation or {})
        if connection is not None:
            return self._submit_in_transaction(
                connection,
                request,
                event,
                ingress=ingress,
                continuation=continuation,
                proposal_expires_at=proposal_expires_at,
                when=when,
            )
        with self.store.transaction() as owned_connection:
            return self._submit_in_transaction(
                owned_connection,
                request,
                event,
                ingress=ingress,
                continuation=continuation,
                proposal_expires_at=proposal_expires_at,
                when=when,
            )

    def _load_exact(self, connection: Any, proposal_id: str) -> tuple[Any, AssignmentRequest, EventEnvelope, dict[str, Any]]:
        row = connection.execute(
            "SELECT * FROM task_custody_proposals WHERE proposal_id=?", (proposal_id,)
        ).fetchone()
        if row is None:
            raise AuthorizationError("task proposal is unavailable")
        request = AssignmentRequest.model_validate_json(
            canonical_json(
                self.store.cipher.decrypt_json(
                    row["request_encrypted"], purpose=f"task-proposal-request:{proposal_id}"
                )
            )
        )
        event = EventEnvelope.model_validate(
            self.store.cipher.decrypt_json(
                row["event_encrypted"], purpose=f"task-proposal-event:{proposal_id}"
            )
        )
        continuation = self.store.cipher.decrypt_json(
            row["continuation_encrypted"], purpose=f"task-proposal-continuation:{proposal_id}"
        )
        self._validate_submission(request, event, TaskIngressKind(row["ingress_kind"]))
        expected = self._request_digest(
            request=request,
            event=event,
            ingress=TaskIngressKind(row["ingress_kind"]),
            continuation=continuation,
            relationship_digest=row["relationship_digest"],
        )
        if expected != row["request_digest"] or envelope_digest(event) != row["event_digest"]:
            raise ConflictError("task proposal immutable digest verification failed")
        return row, request, event, continuation

    def _invalidate(
        self,
        connection: Any,
        *,
        row: Any,
        reason: str,
        now: int,
    ) -> TaskProposalOutcome:
        cursor = connection.execute(
            """UPDATE task_custody_proposals
                  SET state='invalidated',state_reason=?,revision=revision+1,updated_at=?,decided_at=?
                WHERE proposal_id=? AND state='pending' AND revision=?""",
            (reason, now, now, row["proposal_id"], row["revision"]),
        )
        if cursor.rowcount != 1:
            raise ConflictError("task proposal decision raced with another terminal decision")
        self.store.append_audit(
            connection,
            {
                "action": "task_custody.invalidated",
                "proposal_id": row["proposal_id"],
                "reason": reason,
                "request_digest": row["request_digest"],
            },
        )
        return TaskProposalOutcome(
            proposal_id=row["proposal_id"],
            state=TaskProposalState.INVALIDATED,
            reason=reason,
            request_digest=row["request_digest"],
            revision=int(row["revision"]) + 1,
        )

    def _freshness_reason(
        self,
        connection: Any,
        *,
        row: Any,
        request: AssignmentRequest,
        when: datetime,
        permit_relationship_reauthorization: bool,
    ) -> str | None:
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?", (row["domain_id"],)
        ).fetchone()
        if (
            domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != int(row["policy_revision"])
            or int(domain["revocation_epoch"]) != int(row["domain_revocation_epoch"])
        ):
            return "task_proposal_policy_or_domain_revision_drift"
        sender_denial, _ = validate_actor_state(
            connection,
            actor=request.actor,
            expected_policy_revision=int(row["policy_revision"]),
            when=when,
        )
        if sender_denial is not None or request.actor.credential_epoch != int(row["sender_credential_epoch"]):
            return "task_proposal_sender_identity_drift"
        recipient = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?", (row["recipient_harness_id"],)
        ).fetchone()
        if (
            recipient is None
            or recipient["status"] != "active"
            or recipient["domain_id"] != row["domain_id"]
            or (recipient["principal_id"] or recipient["guest_id"]) != row["recipient_authority_id"]
            or int(recipient["credential_epoch"]) != int(row["recipient_credential_epoch"])
        ):
            return "task_proposal_recipient_identity_drift"
        latest = self._latest_relationship(connection, request)
        if latest is not None:
            binding_denial = RelationshipService.authority_binding_denial(
                connection,
                latest,
                current_policy_revision=int(row["policy_revision"]),
                approval_verifier=self.approval_verifier,
                when=when,
            )
            if binding_denial is not None:
                return "task_proposal_relationship_authority_drift"
        if not permit_relationship_reauthorization and (
            self._relationship_digest(latest) != row["relationship_digest"]
            or (
                latest is not None
                and int(latest["relationship_expires_at"]) <= epoch_seconds(when)
            )
        ):
            return "task_proposal_relationship_revision_drift"
        return self._reauthorization_reason(connection, row=row, request=request, when=when)

    def _reauthorization_reason(
        self,
        connection: Any,
        *,
        row: Any,
        request: AssignmentRequest,
        when: datetime,
    ) -> str | None:
        continuation = self.store.cipher.decrypt_json(
            row["continuation_encrypted"], purpose=f"task-proposal-continuation:{row['proposal_id']}"
        )
        authorization = continuation.get("authorization")
        if not isinstance(authorization, dict) or not authorization:
            return None
        action = authorization.get("action")
        resource = authorization.get("resource")
        if not isinstance(action, str) or not isinstance(resource, str):
            return "task_proposal_reauthorization_binding_invalid"
        now = epoch_seconds(when)
        if request.actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
            entitlement_id, _reason, _scope_peers = PolicyEngine._current_entitlement(
                connection,
                domain_id=request.actor.domain_id,
                principal_id=request.actor.principal_id or "",
                harness_id=request.actor.harness_id or "",
                action=action,
                resource=resource,
                revision=int(row["policy_revision"]),
                now=now,
            )
            if entitlement_id is None:
                return "task_proposal_positive_authority_revoked"
        grant_id = authorization.get("grant_id")
        if grant_id is None:
            return None
        grant_row = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (grant_id,)).fetchone()
        if grant_row is None:
            return "task_proposal_grant_missing"
        try:
            grant = TaskGrant.model_validate_json(grant_row["grant_json"])
            data_class = Classification(str(authorization["data_class"]))
        except Exception:
            return "task_proposal_grant_binding_invalid"
        if (
            grant_row["domain_id"] != request.actor.domain_id
            or grant_row["principal_id"] != request.actor.positive_authority_id
            or grant_row["harness_id"] != request.actor.harness_id
            or grant_row["revoked_at"] is not None
            or int(grant_row["expires_at"]) <= now
            or int(grant_row["uses"]) < 1
            or int(grant_row["uses"]) > int(grant_row["max_uses"])
            or action not in grant.actions
            or resource not in grant.resources
            or authorization.get("input_source") not in grant.input_sources
            or authorization.get("output_sink") not in grant.output_sinks
            or data_class not in grant.data_classes
        ):
            return "task_proposal_grant_no_longer_current"
        return None

    def _resume(
        self,
        connection: Any,
        *,
        row: Any,
        request: AssignmentRequest,
        event: EventEnvelope,
        continuation: dict[str, Any],
        decision_actor: VerifiedActor,
        decision_method: str,
        authority_basis: Literal["directed_relationship", "recipient_owner_approval"],
        relationship_id: str | None,
        relationship_revision: int,
        expected_request_digest: str,
        expected_revision: int,
        when: datetime,
    ) -> TaskProposalOutcome:
        if self.mailbox is None:
            raise AuthorizationError("task custody requires the composed mailbox service")
        request = self._canonical_request(request)
        if row["state"] != TaskProposalState.PENDING.value or int(row["revision"]) != expected_revision:
            raise ConflictError("task proposal is no longer pending at the expected revision")
        if row["request_digest"] != expected_request_digest:
            raise AuthorizationError("task proposal decision does not bind the exact request digest")
        now = epoch_seconds(when)
        if int(row["expires_at"]) <= now:
            cursor = connection.execute(
                """UPDATE task_custody_proposals
                      SET state='expired',state_reason='task_proposal_expired',revision=revision+1,
                          updated_at=?,decided_at=?
                    WHERE proposal_id=? AND state='pending' AND revision=?""",
                (now, now, row["proposal_id"], expected_revision),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task proposal expiry raced with another decision")
            return TaskProposalOutcome(
                proposal_id=row["proposal_id"],
                state=TaskProposalState.EXPIRED,
                reason="task_proposal_expired",
                request_digest=row["request_digest"],
                revision=expected_revision + 1,
            )
        accepted = self.mailbox._accept_in_transaction(
            connection,
            event,
            now=now,
            pending_cost=0 if continuation.get("kind") == "relay_task" else None,
        )
        connection.execute(
            "UPDATE recipients SET current_fact=?,updated_at=? WHERE event_id=? AND recipient_id=?",
            (DeliveryFact.ACCEPTED_QUEUED.value, now, accepted["event_id"], event.recipients[0]),
        )
        effective_deadline = request.deadline or event.effect_deadline
        if effective_deadline is None:  # pragma: no cover - persisted proposals bind a deadline
            raise AuthorizationError("resumed task custody lacks an exact deadline")
        admission = self.conflicts.record_accepted_in_transaction(
            connection,
            event_id=str(accepted["event_id"]),
            domain_id=request.actor.domain_id,
            recipient_harness_id=request.recipient_harness_id,
            sender_harness_id=request.actor.harness_id or "",
            sender_authority_id=request.actor.positive_authority_id or "",
            authority_basis=authority_basis,
            relationship_id=relationship_id,
            relationship_revision=relationship_revision,
            intent=request.intent,
            continuation=continuation,
            deadline=effective_deadline,
            when=when,
        )
        if admission.fact is DeliveryFact.ACCEPTED_QUEUED:
            self._apply_continuation(
                connection, continuation=continuation, event=event, now=now, initial=False
            )
            self.conflicts.mark_continuation_applied_in_transaction(
                connection,
                event_id=str(accepted["event_id"]),
            )
        approval_digest = canonical_digest(
            {
                "actor": decision_actor.audit_view(),
                "method": decision_method,
                "proposal_id": row["proposal_id"],
                "proposal_revision": expected_revision,
                "request_digest": expected_request_digest,
            }
        )
        cursor = connection.execute(
            """UPDATE task_custody_proposals
                  SET state='resumed',state_reason=?,revision=revision+1,updated_at=?,decided_at=?,
                      approval_actor_json=?,approval_digest=?,resumed_event_id=?
                WHERE proposal_id=? AND state='pending' AND revision=?""",
            (
                decision_method,
                now,
                now,
                canonical_json(decision_actor.audit_view()).decode("utf-8"),
                approval_digest,
                accepted["event_id"],
                row["proposal_id"],
                expected_revision,
            ),
        )
        if cursor.rowcount != 1:
            raise ConflictError("task proposal approval raced with another decision")
        self.store.append_audit(
            connection,
            {
                "action": "task_custody.resumed",
                "approval_digest": approval_digest,
                "decision_method": decision_method,
                "event_id": accepted["event_id"],
                "proposal_id": row["proposal_id"],
                "request_digest": expected_request_digest,
                "task_conflict_ids": list(admission.conflict_ids),
                "task_custody_fact": admission.fact.value,
            },
        )
        return TaskProposalOutcome(
            proposal_id=row["proposal_id"],
            state=TaskProposalState.RESUMED,
            reason=decision_method,
            request_digest=row["request_digest"],
            revision=expected_revision + 1,
            resumed_event_id=accepted["event_id"],
            fact=admission.fact,
        )

    def approve(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        expected_request_digest: str,
        expected_revision: int,
        when: datetime | None = None,
    ) -> TaskProposalOutcome:
        """Resume once after fresh, exact, non-self recipient-owner approval."""

        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            row, request, event, continuation = self._load_exact(connection, proposal_id)
            if (
                actor.domain_id != row["domain_id"]
                or actor.positive_authority_id != row["recipient_authority_id"]
            ):
                raise AuthorizationError("task proposal approval is unavailable")
            if actor.positive_authority_id == row["sender_authority_id"]:
                raise AuthorizationError("task proposal approval must be non-self")
            freshness = self._freshness_reason(
                connection,
                row=row,
                request=request,
                when=when,
                permit_relationship_reauthorization=False,
            )
            if freshness is not None:
                return self._invalidate(connection, row=row, reason=freshness, now=epoch_seconds(when))
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(row["policy_revision"]),
                when=when,
            )
            if denial is not None:
                raise AuthorizationError("task proposal approver is not current")
            return self._resume(
                connection,
                row=row,
                request=request,
                event=event,
                continuation=continuation,
                decision_actor=actor,
                decision_method="recipient_owner_approval",
                authority_basis="recipient_owner_approval",
                relationship_id=None,
                relationship_revision=0,
                expected_request_digest=expected_request_digest,
                expected_revision=expected_revision,
                when=when,
            )

    def reauthorize_with_current_edge(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        expected_request_digest: str,
        expected_revision: int,
        expected_relationship_revision: int,
        when: datetime | None = None,
    ) -> TaskProposalOutcome:
        """Resume once after an explicitly bound newly-current directed edge."""

        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            row, request, event, continuation = self._load_exact(connection, proposal_id)
            if actor.audit_view() != request.actor.audit_view():
                raise AuthorizationError("relationship reauthorization requires the exact original sender")
            freshness = self._freshness_reason(
                connection,
                row=row,
                request=request,
                when=when,
                permit_relationship_reauthorization=True,
            )
            if freshness is not None:
                return self._invalidate(connection, row=row, reason=freshness, now=epoch_seconds(when))
            latest = self._latest_relationship(connection, request)
            if (
                latest is None
                or int(latest["relationship_revision"]) != expected_relationship_revision
            ):
                raise AuthorizationError("task proposal does not bind the exact current relationship revision")
            reauthorized_request = request.model_copy(
                update={"expected_relationship_revision": expected_relationship_revision}
            )
            allowed, reason, _effective_deadline = self._scope_reason(
                latest,
                reauthorized_request,
                when=when,
                deadline_anchor=event.created_at,
            )
            if not allowed:
                raise AuthorizationError(reason)
            return self._resume(
                connection,
                row=row,
                request=request,
                event=event,
                continuation=continuation,
                decision_actor=actor,
                decision_method="current_directed_edge_reauthorization",
                authority_basis="directed_relationship",
                relationship_id=str(latest["relationship_id"]),
                relationship_revision=int(latest["relationship_revision"]),
                expected_request_digest=expected_request_digest,
                expected_revision=expected_revision,
                when=when,
            )

    def deny(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        expected_request_digest: str,
        expected_revision: int,
        reason_code: str,
        when: datetime | None = None,
    ) -> TaskProposalOutcome:
        when = when or datetime.now(UTC)
        if not reason_code or len(reason_code) > 128 or not reason_code.replace("_", "").isalnum():
            raise ValidationError("task proposal denial reason must be a bounded code")
        with self.store.transaction() as connection:
            row, _request, _event, _continuation = self._load_exact(connection, proposal_id)
            if (
                actor.domain_id != row["domain_id"]
                or actor.positive_authority_id != row["recipient_authority_id"]
            ):
                raise AuthorizationError("task proposal denial is unavailable")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(row["policy_revision"]),
                when=when,
            )
            if denial is not None:
                raise AuthorizationError("task proposal owner is not current")
            if row["state"] != "pending" or int(row["revision"]) != expected_revision:
                raise ConflictError("task proposal is no longer pending at the expected revision")
            if row["request_digest"] != expected_request_digest:
                raise AuthorizationError("task proposal denial does not bind the exact request digest")
            now = epoch_seconds(when)
            cursor = connection.execute(
                """UPDATE task_custody_proposals
                      SET state='denied',state_reason=?,revision=revision+1,updated_at=?,decided_at=?,
                          approval_actor_json=?
                    WHERE proposal_id=? AND state='pending' AND revision=?""",
                (
                    reason_code,
                    now,
                    now,
                    canonical_json(actor.audit_view()).decode("utf-8"),
                    proposal_id,
                    expected_revision,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task proposal denial raced with another decision")
            self.store.append_audit(
                connection,
                {
                    "action": "task_custody.denied",
                    "proposal_id": proposal_id,
                    "reason_code": reason_code,
                    "request_digest": expected_request_digest,
                },
            )
            return TaskProposalOutcome(
                proposal_id=proposal_id,
                state=TaskProposalState.DENIED,
                reason=reason_code,
                request_digest=expected_request_digest,
                revision=expected_revision + 1,
            )

    def pending_for_owner(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
        when: datetime | None = None,
    ) -> list[dict[str, Any]]:
        """Return only redacted summaries to the exact recipient authority."""

        if not 1 <= limit <= 1_000:
            raise ValidationError("task proposal limit is invalid")
        authority_id = actor.positive_authority_id
        if authority_id is None:
            raise AuthorizationError("task proposal approval requires a human authority")
        now = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?", (actor.domain_id,)
            ).fetchone()
            if domain is None:
                raise AuthorizationError("task proposal approval is unavailable")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=now,
            )
            if denial is not None:
                raise AuthorizationError("task proposal owner is not current")
            rows = connection.execute(
                """SELECT proposal_id,request_digest,revision,expires_at,redacted_summary_json
                     FROM task_custody_proposals
                    WHERE domain_id=? AND recipient_authority_id=? AND state='pending'
                      AND expires_at>?
                    ORDER BY created_at,proposal_id LIMIT ?""",
                (actor.domain_id, authority_id, epoch_seconds(now), limit),
            ).fetchall()
            return [
                {
                    "proposal_id": row["proposal_id"],
                    "request_digest": row["request_digest"],
                    "revision": int(row["revision"]),
                    "expires_at": datetime.fromtimestamp(row["expires_at"], UTC).isoformat(),
                    "summary": json.loads(row["redacted_summary_json"]),
                }
                for row in rows
            ]

    def pending_conflicts_for_owner(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
        when: datetime | None = None,
    ) -> list[dict[str, Any]]:
        return self.conflicts.pending_for_owner(actor=actor, limit=limit, when=when)

    def adjudicate_conflict(
        self,
        *,
        actor: VerifiedActor,
        decision: TaskConflictAdjudication,
        when: datetime | None = None,
    ) -> TaskConflictOutcome:
        return self.conflicts.adjudicate(actor=actor, decision=decision, when=when)

    def expire_due(self, *, authoritative_now: datetime | None = None) -> int:
        now = epoch_seconds(authoritative_now or datetime.now(UTC))
        with self.store.transaction() as connection:
            cursor = connection.execute(
                """UPDATE task_custody_proposals
                      SET state='expired',state_reason='task_proposal_expired',revision=revision+1,
                          updated_at=?,decided_at=?
                    WHERE state='pending' AND expires_at<=?""",
                (now, now, now),
            )
            if cursor.rowcount:
                self.store.append_audit(
                    connection,
                    {"action": "task_custody.expired", "count": int(cursor.rowcount), "clock": now},
                )
            return int(cursor.rowcount)


__all__ = [
    "AssignmentDecision",
    "AssignmentRequest",
    "AssignmentService",
    "TaskIngressKind",
    "TaskConflictAdjudication",
    "TaskConflictOutcome",
    "TaskExecutionIntent",
    "TaskProposalOutcome",
    "TaskProposalState",
]
