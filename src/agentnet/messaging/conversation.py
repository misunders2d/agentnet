"""Exact, inert conversation action schemas.

Construction never implies task acceptance, cancellation success, completion,
or effect execution.  Those facts remain owned by the authorization and
delivery state machines.
"""

from __future__ import annotations

import json
import re
import time
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from typing import Annotated, Any, Literal
from uuid import NAMESPACE_URL, uuid5

from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, field_validator, model_validator

from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.messaging.obligation import (
    ResponseObligationService,
    ResponseObligationSpec,
)
from agentnet.protocol.models import (
    Classification,
    EventEnvelope,
    EventType,
    ReleasedArtifactBinding,
)
from agentnet.organization.assignment import (
    AssignmentRequest,
    AssignmentService,
    TaskIngressKind,
)
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$")
DIGEST = re.compile(r"^[a-f0-9]{64}$")


class _ConversationAction(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()


class PostAction(_ConversationAction):
    kind: Literal["post"]
    body: str = Field(min_length=1, max_length=65_536)
    mentions: tuple[str, ...] = ()
    response_obligation: ResponseObligationSpec | None = None


class ReplyAction(_ConversationAction):
    kind: Literal["reply"]
    reply_to_event_id: str = Field(min_length=1, max_length=256)
    body: str = Field(min_length=1, max_length=65_536)
    mentions: tuple[str, ...] = ()


class TaskAction(_ConversationAction):
    kind: Literal["task"]
    task_id: str = Field(min_length=1, max_length=256)
    summary: str = Field(min_length=1, max_length=4_096)
    structured_request: dict[str, Any] = Field(default_factory=dict)
    effect_deadline: datetime | None = None


class HandoffAction(_ConversationAction):
    kind: Literal["handoff"]
    task_id: str = Field(min_length=1, max_length=256)
    source_event_id: str = Field(min_length=1, max_length=256)
    from_harness_id: str = Field(min_length=1, max_length=256)
    to_harness_id: str = Field(min_length=1, max_length=256)
    state_digest: str

    @field_validator("state_digest")
    @classmethod
    def exact_digest(cls, value: str) -> str:
        if not DIGEST.fullmatch(value):
            raise ValueError("handoff state digest must be an exact SHA-256 value")
        return value


class StructuredRequestAction(_ConversationAction):
    kind: Literal["structured_request"]
    request_type: str
    arguments: dict[str, Any]
    response_schema_id: str | None = None
    response_obligation: ResponseObligationSpec | None = None

    @field_validator("request_type", "response_schema_id")
    @classmethod
    def bounded_identifiers(cls, value: str | None) -> str | None:
        if value is not None and not IDENTIFIER.fullmatch(value):
            raise ValueError("structured request identifiers are invalid")
        return value


class ObligationResponseAction(_ConversationAction):
    """Typed terminal answer bound to one exact open response obligation.

    Only this action can close an obligation.  It must repeat the original
    request event identifier and payload digest so a prose reply, a reply to
    the wrong request, or a tampered digest can never silently satisfy the
    obligation.
    """

    kind: Literal["obligation_response"]
    obligation_id: str = Field(min_length=1, max_length=256)
    request_event_id: str = Field(min_length=1, max_length=256)
    request_digest: str
    outcome: Literal["completed", "failed"]
    body: str = Field(min_length=1, max_length=65_536)
    structured_response: dict[str, Any] = Field(default_factory=dict)
    response_schema_id: str | None = None

    @field_validator("request_digest")
    @classmethod
    def exact_request_digest(cls, value: str) -> str:
        if not DIGEST.fullmatch(value):
            raise ValueError("obligation response must bind an exact SHA-256 request digest")
        return value

    @field_validator("response_schema_id")
    @classmethod
    def bounded_schema_id(cls, value: str | None) -> str | None:
        if value is not None and not IDENTIFIER.fullmatch(value):
            raise ValueError("obligation response schema identifier is invalid")
        return value


class CancellationAction(_ConversationAction):
    kind: Literal["cancellation"]
    target_event_id: str = Field(min_length=1, max_length=256)
    task_id: str | None = Field(default=None, max_length=256)
    reason_code: str

    @field_validator("reason_code")
    @classmethod
    def reason_is_code(cls, value: str) -> str:
        if not IDENTIFIER.fullmatch(value):
            raise ValueError("cancellation reason must be a bounded code, not content")
        return value


class CompletionAcknowledgementAction(_ConversationAction):
    kind: Literal["completion_ack"]
    target_event_id: str = Field(min_length=1, max_length=256)
    task_id: str | None = Field(default=None, max_length=256)
    outcome: Literal["completed", "failed_terminal", "canceled", "effect_unknown"]
    result_digest: str | None = None

    @field_validator("result_digest")
    @classmethod
    def result_is_digest(cls, value: str | None) -> str | None:
        if value is not None and not DIGEST.fullmatch(value):
            raise ValueError("completion result must be referenced by exact digest")
        return value

    @model_validator(mode="after")
    def completed_result_is_bound(self) -> "CompletionAcknowledgementAction":
        if self.outcome == "completed" and self.result_digest is None:
            raise ValueError("completed acknowledgement requires an exact result digest")
        return self


ConversationAction = Annotated[
    PostAction
    | ReplyAction
    | TaskAction
    | HandoffAction
    | StructuredRequestAction
    | ObligationResponseAction
    | CancellationAction
    | CompletionAcknowledgementAction,
    Field(discriminator="kind"),
]
ACTION_ADAPTER = TypeAdapter(ConversationAction)


def _action_payload(parsed: ConversationAction) -> dict[str, Any]:
    """Keep released file metadata in the typed envelope, never the free-form payload."""

    return parsed.model_dump(
        mode="json",
        exclude_none=True,
        exclude={"released_artifacts"},
    )


def build_conversation_event(
    *,
    actor: VerifiedActor,
    recipients: tuple[str, ...],
    conversation_id: str,
    thread_id: str,
    action: dict[str, Any],
    idempotency_key: str,
    classification: Classification,
    policy_revision: int = 1,
    retention_delete_at: datetime | None = None,
) -> EventEnvelope:
    """Validate a conversation action and construct an immutable event.

    This function does not enqueue the event and therefore cannot bypass the
    policy, assignment, cancellation, or effect state machines.
    """

    if not IDENTIFIER.fullmatch(conversation_id) or not IDENTIFIER.fullmatch(thread_id):
        raise ValidationError("conversation and thread identifiers are invalid")
    parsed = ACTION_ADAPTER.validate_python(action)
    mentions = getattr(parsed, "mentions", ())
    if len(set(mentions)) != len(mentions) or not set(mentions).issubset(recipients):
        raise ValidationError("mentions must be unique recipients of the exact event")
    if isinstance(parsed, HandoffAction):
        if actor.harness_id != parsed.from_harness_id or parsed.to_harness_id not in recipients:
            raise AuthorizationError("handoff endpoints do not bind the exact actor and recipient")

    event_type = EventType.MESSAGE
    if isinstance(parsed, TaskAction):
        event_type = EventType.TASK_ASSIGNMENT
    elif isinstance(parsed, (HandoffAction, CancellationAction, CompletionAcknowledgementAction)):
        event_type = EventType.CONTROL

    parent_id = None
    if isinstance(parsed, ReplyAction):
        parent_id = parsed.reply_to_event_id
    elif isinstance(parsed, HandoffAction):
        parent_id = parsed.source_event_id
    elif isinstance(parsed, ObligationResponseAction):
        parent_id = parsed.request_event_id
    elif isinstance(parsed, (CancellationAction, CompletionAcknowledgementAction)):
        parent_id = parsed.target_event_id

    event_id = str(
        uuid5(
            NAMESPACE_URL,
            f"agentnet:conversation:{actor.domain_id}:{actor.harness_id}:{conversation_id}:{idempotency_key}",
        )
    )
    return new_event(
        event_id=event_id,
        domain_id=actor.domain_id,
        actor=actor,
        event_type=event_type,
        classification=classification,
        payload=_action_payload(parsed),
        idempotency_key=idempotency_key,
        recipients=recipients,
        released_artifacts=parsed.released_artifacts,
        conversation_id=conversation_id,
        thread_id=thread_id,
        task_id=getattr(parsed, "task_id", None),
        causal_parent_ids=(parent_id,) if parent_id else (),
        effect_deadline=getattr(parsed, "effect_deadline", None),
        policy_revision=policy_revision,
        retention_delete_at=retention_delete_at,
    )


class ConversationService:
    """Atomic conversation state, authorization, and durable delivery.

    Conversation membership is positive-human-authority scoped.  The exact
    harness remains on every event and recipient row for attribution,
    revocation, and directional task ownership.
    """

    def __init__(
        self,
        store: StoreBackend,
        policy: PolicyEngine,
        mailbox: MailboxService,
        *,
        assignments: AssignmentService | None = None,
        obligations: ResponseObligationService | None = None,
        artifact_binding_validator: Callable[..., ReleasedArtifactBinding] | None = None,
        retention_days: int = 30,
    ) -> None:
        if not 1 <= retention_days <= 3_650:
            raise ValueError("conversation retention is outside the bounded policy")
        self.store = store
        self.policy = policy
        self.mailbox = mailbox
        self.assignments = assignments or AssignmentService(
            store,
            mailbox=mailbox,
            policy=policy,
        )
        self.obligations = obligations or ResponseObligationService(store, policy)
        self.artifact_binding_validator = artifact_binding_validator
        self.retention_days = retention_days

    @staticmethod
    def _authority_id(actor: VerifiedActor) -> str:
        authority_id = actor.positive_authority_id
        if (
            authority_id is None
            or actor.harness_id is None
            or actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}
        ):
            raise AuthorizationError("conversation access requires a verified human or host guest plus harness")
        return authority_id

    @staticmethod
    def _current_revision(connection: Any, domain_id: str) -> int:
        row = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?",
            (domain_id,),
        ).fetchone()
        if row is None or row["status"] != "active":
            raise AuthorizationError("conversation trust domain is unavailable")
        return int(row["policy_revision"])

    def _require_current_actor(
        self,
        connection: Any,
        actor: VerifiedActor,
        *,
        now: int,
        classification: Classification,
    ) -> tuple[str, int]:
        authority_id = self._authority_id(actor)
        revision = self._current_revision(connection, actor.domain_id)
        local_lab_allowed = self.policy.allows_local_conformance_conversation_harness(
            binding_assurance=actor.binding_assurance,
            classification=classification,
        )
        if actor.binding_assurance == "lab" and not local_lab_allowed:
            raise AuthorizationError("conversation actor is not current: synthetic_lab_harness_not_admitted")
        denial, _current = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=datetime.fromtimestamp(now, UTC),
            allow_deterministic_only=local_lab_allowed,
        )
        if denial is not None:
            raise AuthorizationError(f"conversation actor is not current: {denial}")
        return authority_id, revision

    def _require_member(
        self,
        connection: Any,
        actor: VerifiedActor,
        conversation_id: str,
        *,
        now: int,
    ) -> tuple[Any, str, int]:
        conversation = connection.execute(
            "SELECT * FROM conversations WHERE conversation_id=?",
            (conversation_id,),
        ).fetchone()
        if (
            conversation is None
            or conversation["state"] != "active"
            or conversation["domain_id"] != actor.domain_id
        ):
            raise AuthorizationError("conversation is unavailable")
        authority_id, revision = self._require_current_actor(
            connection,
            actor,
            now=now,
            classification=Classification(conversation["classification"]),
        )
        member = connection.execute(
            """SELECT role FROM conversation_members
                 WHERE conversation_id=? AND authority_id=? AND status='active' LIMIT 1""",
            (conversation_id, authority_id),
        ).fetchone()
        if member is None:
            raise AuthorizationError("verified principal is not an active conversation member")
        return conversation, authority_id, revision

    def _recipient_authorities(
        self,
        connection: Any,
        domain_id: str,
        recipients: tuple[str, ...],
        *,
        classification: Classification,
    ) -> dict[str, str]:
        if not recipients or len(recipients) != len(set(recipients)) or len(recipients) > 1_000:
            raise ValidationError("conversation recipients must be a bounded unique tuple")
        placeholders = ",".join("?" for _ in recipients)
        rows = connection.execute(
            f"""SELECT harness_id,principal_id,guest_id,status,domain_id,binding_assurance FROM harnesses
                  WHERE harness_id IN ({placeholders})""",
            recipients,
        ).fetchall()
        mapped: dict[str, str] = {}
        for row in rows:
            authority_id = row["principal_id"] or row["guest_id"]
            local_lab_allowed = self.policy.allows_local_conformance_conversation_harness(
                binding_assurance=str(row["binding_assurance"]),
                classification=classification,
            )
            deterministic_allowed = bool(
                row["status"] == "deterministic_only"
                and local_lab_allowed
            )
            if (
                (row["status"] != "active" and not deterministic_allowed)
                or (row["binding_assurance"] == "lab" and not local_lab_allowed)
                or row["domain_id"] != domain_id
                or not authority_id
            ):
                raise AuthorizationError("conversation recipient is not a current domain harness")
            mapped[row["harness_id"]] = authority_id
        if set(mapped) != set(recipients):
            raise AuthorizationError("conversation recipient is unavailable")
        return mapped

    def create(
        self,
        *,
        actor: VerifiedActor,
        conversation_id: str,
        member_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        if not IDENTIFIER.fullmatch(conversation_id):
            raise ValidationError("conversation identifier is invalid")
        if classification is Classification.C3_SEALED:
            raise AuthorizationError("C3 content requires a validated MLS room, not a direct conversation")
        members = tuple(dict.fromkeys((actor.harness_id or "", *member_harness_ids)))
        now = int(time.time())
        with self.store.transaction() as connection:
            authority_id, revision = self._require_current_actor(
                connection,
                actor,
                now=now,
                classification=classification,
            )
            mapped = self._recipient_authorities(
                connection,
                actor.domain_id,
                members,
                classification=classification,
            )
            context = {
                "classification": classification.value,
                "member_digest": canonical_digest({"members": sorted(mapped.items())}),
            }
            decision = self.policy._decide_in_transaction(
                connection,
                AuthorizationRequest(
                    actor=actor,
                    action="conversation.create",
                    resource=f"conversation:{conversation_id}",
                    operation_class=OperationClass.BUSINESS,
                    policy_revision=revision,
                    context=context,
                    classification=classification,
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            if not decision.allowed:
                raise AuthorizationError(decision.reason)
            existing = connection.execute(
                "SELECT * FROM conversations WHERE conversation_id=?",
                (conversation_id,),
            ).fetchone()
            if existing is not None:
                rows = connection.execute(
                    """SELECT harness_id,authority_id,role,status FROM conversation_members
                         WHERE conversation_id=? ORDER BY harness_id""",
                    (conversation_id,),
                ).fetchall()
                expected = sorted(
                    (harness_id, mapped[harness_id], "owner" if mapped[harness_id] == authority_id else "member", "active")
                    for harness_id in mapped
                )
                stored = sorted((row["harness_id"], row["authority_id"], row["role"], row["status"]) for row in rows)
                if (
                    existing["domain_id"] != actor.domain_id
                    or existing["created_by_authority_id"] != authority_id
                    or existing["classification"] != classification.value
                    or stored != expected
                ):
                    raise AuthorizationError("conversation identifier already names different authority or membership")
                return {"conversation_id": conversation_id, "duplicate": True, "policy_decision_id": decision.decision_id}
            connection.execute(
                """INSERT INTO conversations(
                    conversation_id,domain_id,created_by_authority_id,classification,state,created_at,updated_at
                ) VALUES(?,?,?,?,'active',?,?)""",
                (conversation_id, actor.domain_id, authority_id, classification.value, now, now),
            )
            for harness_id, member_authority in mapped.items():
                connection.execute(
                    """INSERT INTO conversation_members(
                        conversation_id,authority_id,harness_id,role,status,joined_at
                    ) VALUES(?,?,?,?, 'active',?)""",
                    (
                        conversation_id,
                        member_authority,
                        harness_id,
                        "owner" if member_authority == authority_id else "member",
                        now,
                    ),
                )
            self.store.append_audit(
                connection,
                {
                    "action": "conversation.created",
                    "actor": actor.audit_view(),
                    "classification": classification.value,
                    "conversation_id": conversation_id,
                    "member_authority_digest": context["member_digest"],
                    "policy_decision_id": decision.decision_id,
                },
            )
            if phase_hook is not None:
                phase_hook("before_conversation_commit")
        return {"conversation_id": conversation_id, "duplicate": False, "policy_decision_id": decision.decision_id}

    def post(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        conversation_id: str,
        thread_id: str,
        action: dict[str, Any],
        idempotency_key: str,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        parsed = ACTION_ADAPTER.validate_python(action)
        now = int(time.time())
        with self.store.transaction() as connection:
            conversation, authority_id, revision = self._require_member(
                connection,
                actor,
                conversation_id,
                now=now,
            )
            classification = Classification(conversation["classification"])
            if parsed.released_artifacts:
                if self.artifact_binding_validator is None:
                    raise AuthorizationError(
                        "conversation artifact delivery requires the authoritative release validator"
                    )
                for binding in parsed.released_artifacts:
                    self.artifact_binding_validator(
                        binding,
                        domain_id=actor.domain_id,
                        event_classification=classification,
                    )
            recipient_authorities = self._recipient_authorities(
                connection,
                actor.domain_id,
                recipients,
                classification=classification,
            )
            active_rows = connection.execute(
                """SELECT harness_id FROM conversation_members
                     WHERE conversation_id=? AND status='active'""",
                (conversation_id,),
            ).fetchall()
            if not set(recipients).issubset({row["harness_id"] for row in active_rows}):
                raise AuthorizationError("conversation delivery targets a nonmember harness")

            existing = connection.execute(
                """SELECT e.event_id,e.acceptance_fact,e.envelope_digest,e.envelope_json,e.payload_encrypted,
                          a.action_kind
                     FROM events e JOIN conversation_actions a ON a.event_id=e.event_id
                    WHERE e.domain_id=? AND e.actor_json=? AND e.idempotency_key=?""",
                (
                    actor.domain_id,
                    canonical_json(actor.audit_view()).decode("utf-8"),
                    idempotency_key,
                ),
            ).fetchone()
            if existing is not None:
                envelope = json.loads(existing["envelope_json"])
                exact_payload = self.store.decrypted_payload(existing["payload_encrypted"], existing["event_id"])
                expected_payload = _action_payload(parsed)
                expected_artifacts = [
                    binding.model_dump(mode="json") for binding in parsed.released_artifacts
                ]
                if (
                    existing["action_kind"] != parsed.kind
                    or envelope.get("conversation_id") != conversation_id
                    or envelope.get("thread_id") != thread_id
                    or tuple(envelope.get("recipients", ())) != recipients
                    or envelope.get("released_artifacts", []) != expected_artifacts
                    or exact_payload != expected_payload
                ):
                    raise AuthorizationError("conversation idempotency key names different exact action bytes")
                result: dict[str, Any] = {
                    "event_id": existing["event_id"],
                    "fact": existing["acceptance_fact"],
                    "duplicate": True,
                    "envelope_digest": existing["envelope_digest"],
                    "action_kind": parsed.kind,
                    "conversation_id": conversation_id,
                }
                obligation = connection.execute(
                    """SELECT obligation_id,state,revision FROM response_obligations
                         WHERE request_event_id=? OR response_event_id=?
                         ORDER BY obligation_id LIMIT 1""",
                    (existing["event_id"], existing["event_id"]),
                ).fetchone()
                if obligation is not None:
                    result["response_obligation"] = {
                        "obligation_id": obligation["obligation_id"],
                        "state": obligation["state"],
                        "revision": int(obligation["revision"]),
                    }
                return result

            parent_event_id: str | None = None
            task_row: Any | None = None
            policy_action = "conversation.message.send"
            if isinstance(parsed, ReplyAction):
                parent_event_id = parsed.reply_to_event_id
                parent = connection.execute(
                    """SELECT event_id FROM conversation_actions
                         WHERE event_id=? AND conversation_id=? AND thread_id=?""",
                    (parent_event_id, conversation_id, thread_id),
                ).fetchone()
                if parent is None:
                    raise AuthorizationError("reply parent is outside the exact conversation thread")
            elif isinstance(parsed, TaskAction):
                policy_action = "conversation.task.request"
                if len(recipients) != 1:
                    raise ValidationError("a conversation task requires one exact assignee harness")
                task_row = connection.execute(
                    "SELECT task_id FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, parsed.task_id),
                ).fetchone()
                if task_row is not None:
                    raise AuthorizationError("conversation task identifier is already in use")
            elif isinstance(parsed, HandoffAction):
                policy_action = "conversation.task.handoff"
                parent_event_id = parsed.source_event_id
                task_row = connection.execute(
                    "SELECT * FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, parsed.task_id),
                ).fetchone()
                if (
                    task_row is None
                    or task_row["state"] in {"completed", "failed_terminal", "canceled", "effect_unknown"}
                    or task_row["assignee_harness_id"] != actor.harness_id
                    or task_row["latest_event_id"] != parsed.source_event_id
                    or parsed.to_harness_id not in recipients
                ):
                    raise AuthorizationError("handoff does not bind the current task owner and state")
            elif isinstance(parsed, CancellationAction):
                policy_action = "conversation.task.cancel_request"
                parent_event_id = parsed.target_event_id
                if parsed.task_id is None:
                    raise ValidationError("task cancellation requires the exact task identifier")
                task_row = connection.execute(
                    "SELECT * FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, parsed.task_id),
                ).fetchone()
                if (
                    task_row is None
                    or task_row["creator_authority_id"] != authority_id
                    or task_row["latest_event_id"] != parsed.target_event_id
                    or task_row["state"] in {"completed", "failed_terminal", "canceled", "effect_unknown"}
                ):
                    raise AuthorizationError("cancellation does not bind the current task creator and state")
            elif isinstance(parsed, CompletionAcknowledgementAction):
                policy_action = "conversation.task.complete"
                parent_event_id = parsed.target_event_id
                if parsed.task_id is None:
                    raise ValidationError("completion acknowledgement requires the exact task identifier")
                task_row = connection.execute(
                    "SELECT * FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                    (conversation_id, parsed.task_id),
                ).fetchone()
                if (
                    task_row is None
                    or task_row["assignee_harness_id"] != actor.harness_id
                    or task_row["latest_event_id"] != parsed.target_event_id
                    or task_row["state"] in {"completed", "failed_terminal", "canceled", "effect_unknown"}
                ):
                    raise AuthorizationError("completion does not bind the current exact task assignee and state")
            elif isinstance(parsed, StructuredRequestAction):
                policy_action = "conversation.structured_request.send"

            obligation_row: Any | None = None
            if isinstance(parsed, ObligationResponseAction):
                policy_action = "conversation.response_obligation.respond"
                parent_event_id = parsed.request_event_id
                obligation_row = self.obligations.require_open_for_response_in_transaction(
                    connection,
                    actor=actor,
                    responder_authority_id=authority_id,
                    obligation_id=parsed.obligation_id,
                    request_event_id=parsed.request_event_id,
                    request_digest=parsed.request_digest,
                    conversation_id=conversation_id,
                    thread_id=thread_id,
                )
                if (
                    obligation_row["response_schema_id"] is not None
                    and parsed.response_schema_id != obligation_row["response_schema_id"]
                ):
                    raise ValidationError(
                        "obligation response must declare the exact demanded response schema"
                    )

            obligation_spec: ResponseObligationSpec | None = getattr(
                parsed, "response_obligation", None
            )
            responsible_harness_id: str | None = None
            if obligation_spec is not None:
                responsible_harness_id = obligation_spec.responsible_harness_id or (
                    recipients[0] if len(recipients) == 1 else None
                )
                if responsible_harness_id is None or responsible_harness_id not in recipients:
                    raise ValidationError(
                        "a response obligation requires one exact responsible recipient harness"
                    )

            event = build_conversation_event(
                actor=actor,
                recipients=recipients,
                conversation_id=conversation_id,
                thread_id=thread_id,
                action=(
                    _action_payload(parsed)
                    | {
                        "released_artifacts": [
                            binding.model_dump(mode="json")
                            for binding in parsed.released_artifacts
                        ]
                    }
                    if parsed.released_artifacts
                    else _action_payload(parsed)
                ),
                idempotency_key=idempotency_key,
                classification=classification,
                policy_revision=revision,
                retention_delete_at=datetime.fromtimestamp(now, UTC) + timedelta(days=self.retention_days),
            )
            context = {
                "action_digest": event.payload_digest,
                "recipient_authority_digest": canonical_digest({"recipients": sorted(recipient_authorities.items())}),
                "thread_id": thread_id,
            }
            decision = self.policy._decide_in_transaction(
                connection,
                AuthorizationRequest(
                    actor=actor,
                    action=policy_action,
                    resource=f"conversation:{conversation_id}",
                    operation_class=OperationClass.BUSINESS,
                    policy_revision=revision,
                    context=context,
                    classification=classification,
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            if not decision.allowed:
                raise AuthorizationError(decision.reason)

            if isinstance(parsed, (TaskAction, HandoffAction)):
                task_type = "conversation.task" if isinstance(parsed, TaskAction) else "conversation.handoff"
                resources = {f"conversation:{conversation_id}"}
                tools: frozenset[str] = frozenset()
                budget = 0
                concurrency = 1
                deadline = getattr(parsed, "effect_deadline", None)
                if isinstance(parsed, TaskAction) and parsed.structured_request:
                    structured = parsed.structured_request
                    declared_type = structured.get("task_type")
                    declared_resources = structured.get("resources")
                    declared_tools = structured.get("tools")
                    declared_budget = structured.get("budget")
                    declared_concurrency = structured.get("concurrency")
                    if isinstance(declared_type, str) and declared_type:
                        task_type = declared_type
                    if isinstance(declared_resources, list) and declared_resources and all(
                        isinstance(value, str) and value for value in declared_resources
                    ):
                        resources = set(declared_resources)
                    if isinstance(declared_tools, list) and all(
                        isinstance(value, str) and value for value in declared_tools
                    ):
                        tools = frozenset(declared_tools)
                    if isinstance(declared_budget, int) and declared_budget >= 0:
                        budget = declared_budget
                    if isinstance(declared_concurrency, int) and declared_concurrency >= 1:
                        concurrency = declared_concurrency
                assignment = AssignmentRequest(
                    actor=actor,
                    recipient_harness_id=recipients[0],
                    task_type=task_type,
                    resources=frozenset(resources),
                    data_classes=frozenset({Classification(conversation["classification"])}),
                    tools=tools,
                    budget=budget,
                    concurrency=concurrency,
                    deadline=deadline,
                    policy_revision=revision,
                    context={
                        "conversation_id": conversation_id,
                        "thread_id": thread_id,
                        "action_digest": event.payload_digest,
                    },
                )
                continuation = {
                    "kind": "conversation_task" if isinstance(parsed, TaskAction) else "conversation_handoff",
                    "apply_on_initial": True,
                    "conversation_id": conversation_id,
                    "thread_id": thread_id,
                    "task_id": parsed.task_id,
                    "parent_event_id": parent_event_id,
                    "actor_authority_id": authority_id,
                    "actor_harness_id": actor.harness_id,
                    "authorization": {
                        "action": policy_action,
                        "resource": f"conversation:{conversation_id}",
                    },
                }
                if isinstance(parsed, HandoffAction):
                    continuation.update(
                        {
                            "from_harness_id": parsed.from_harness_id,
                            "source_event_id": parsed.source_event_id,
                        }
                    )
                custody = self.assignments.submit_event(
                    assignment,
                    event,
                    ingress=(
                        TaskIngressKind.CONVERSATION_TASK
                        if isinstance(parsed, TaskAction)
                        else TaskIngressKind.CONVERSATION_HANDOFF
                    ),
                    continuation=continuation,
                    proposal_expires_at=deadline,
                    when=datetime.fromtimestamp(now, UTC),
                    connection=connection,
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "conversation.task_custody_decided",
                        "action_digest": event.payload_digest,
                        "action_kind": parsed.kind,
                        "conversation_id": conversation_id,
                        "event_id": event.event_id,
                        "fact": custody["fact"],
                        "policy_decision_id": decision.decision_id,
                        "proposal_id": custody.get("proposal_id"),
                        "request_digest": custody["request_digest"],
                    },
                )
                if phase_hook is not None:
                    phase_hook("before_conversation_action_commit")
                return custody | {
                    "action_kind": parsed.kind,
                    "conversation_id": conversation_id,
                    "policy_decision_id": decision.decision_id,
                }
            accepted = self.mailbox._accept_in_transaction(connection, event, now=now)
            if accepted["duplicate"]:
                existing_action = connection.execute(
                    "SELECT action_kind FROM conversation_actions WHERE event_id=? AND conversation_id=?",
                    (accepted["event_id"], conversation_id),
                ).fetchone()
                if existing_action is None or existing_action["action_kind"] != parsed.kind:
                    raise AuthorizationError("conversation idempotency record is incomplete or contradictory")
                return accepted | {
                    "action_kind": parsed.kind,
                    "conversation_id": conversation_id,
                    "policy_decision_id": decision.decision_id,
                }
            connection.execute(
                """INSERT INTO conversation_actions(
                    event_id,conversation_id,thread_id,action_kind,parent_event_id,task_id,
                    actor_authority_id,actor_harness_id,action_digest,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    event.event_id,
                    conversation_id,
                    thread_id,
                    parsed.kind,
                    parent_event_id,
                    getattr(parsed, "task_id", None),
                    authority_id,
                    actor.harness_id,
                    event.payload_digest,
                    now,
                ),
            )
            if isinstance(parsed, TaskAction):
                connection.execute(
                    """INSERT INTO conversation_tasks(
                        conversation_id,task_id,creator_authority_id,assignee_harness_id,
                        source_event_id,latest_event_id,state,result_digest,updated_at
                    ) VALUES(?,?,?,?,?,?,'requested',NULL,?)""",
                    (
                        conversation_id,
                        parsed.task_id,
                        authority_id,
                        recipients[0],
                        event.event_id,
                        event.event_id,
                        now,
                    ),
                )
            elif isinstance(parsed, HandoffAction):
                connection.execute(
                    """UPDATE conversation_tasks SET assignee_harness_id=?,latest_event_id=?,state='handed_off',updated_at=?
                         WHERE conversation_id=? AND task_id=?""",
                    (parsed.to_harness_id, event.event_id, now, conversation_id, parsed.task_id),
                )
            elif isinstance(parsed, CancellationAction):
                connection.execute(
                    """UPDATE conversation_tasks SET latest_event_id=?,state='cancel_requested',updated_at=?
                         WHERE conversation_id=? AND task_id=?""",
                    (event.event_id, now, conversation_id, parsed.task_id),
                )
            elif isinstance(parsed, CompletionAcknowledgementAction):
                connection.execute(
                    """UPDATE conversation_tasks SET latest_event_id=?,state=?,result_digest=?,updated_at=?
                         WHERE conversation_id=? AND task_id=?""",
                    (
                        event.event_id,
                        parsed.outcome,
                        parsed.result_digest,
                        now,
                        conversation_id,
                        parsed.task_id,
                    ),
                )
            obligation_result: dict[str, Any] | None = None
            if obligation_spec is not None and responsible_harness_id is not None:
                obligation_result = self.obligations.create_in_transaction(
                    connection,
                    actor=actor,
                    requester_authority_id=authority_id,
                    spec=obligation_spec,
                    request_event=event,
                    request_envelope_digest=accepted["envelope_digest"],
                    responsible_harness_id=responsible_harness_id,
                    responsible_authority_id=recipient_authorities[responsible_harness_id],
                    classification=classification,
                    policy_revision=revision,
                    now=now,
                )
            elif isinstance(parsed, ObligationResponseAction) and obligation_row is not None:
                # Atomic linkage: the accepted typed response and the terminal
                # obligation state commit or fail together, so the system can
                # never report "awaiting peer" once this response is durable.
                obligation_result = self.obligations.close_with_response_in_transaction(
                    connection,
                    row=obligation_row,
                    actor=actor,
                    outcome=parsed.outcome,
                    response_event_id=event.event_id,
                    response_payload_digest=event.payload_digest,
                    policy_decision_id=decision.decision_id,
                    now=now,
                )
            connection.execute(
                "UPDATE conversations SET updated_at=? WHERE conversation_id=?",
                (now, conversation_id),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "conversation.action_accepted",
                    "action_digest": event.payload_digest,
                    "action_kind": parsed.kind,
                    "actor": actor.audit_view(),
                    "conversation_id": conversation_id,
                    "event_id": event.event_id,
                    "policy_decision_id": decision.decision_id,
                },
            )
            if phase_hook is not None:
                phase_hook("before_conversation_action_commit")
        result = accepted | {
            "action_kind": parsed.kind,
            "conversation_id": conversation_id,
            "policy_decision_id": decision.decision_id,
        }
        if obligation_result is not None:
            result["response_obligation"] = obligation_result
        return result

    def thread(
        self,
        *,
        actor: VerifiedActor,
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        if not 1 <= limit <= 1_000:
            raise ValidationError("conversation thread limit is invalid")
        now = int(time.time())
        with self.store.transaction(immediate=False) as connection:
            self._require_member(connection, actor, conversation_id, now=now)
            rows = connection.execute(
                """SELECT a.*,e.payload_encrypted,e.envelope_json,e.envelope_digest,
                          e.retention_delete_at,e.legal_hold
                     FROM conversation_actions a JOIN events e ON e.event_id=a.event_id
                    WHERE a.conversation_id=? AND a.thread_id=?
                    ORDER BY a.created_at,a.event_id LIMIT ?""",
                (conversation_id, thread_id, limit),
            ).fetchall()
            result: list[dict[str, Any]] = []
            for row in rows:
                result.append(
                    {
                        "event": json.loads(row["envelope_json"]),
                        "envelope_digest": row["envelope_digest"],
                        **self.mailbox.generic_payload_view(row, now=now),
                    }
                )
            return result

    def task_state(self, *, actor: VerifiedActor, conversation_id: str, task_id: str) -> dict[str, Any]:
        now = int(time.time())
        with self.store.transaction(immediate=False) as connection:
            self._require_member(connection, actor, conversation_id, now=now)
            row = connection.execute(
                "SELECT * FROM conversation_tasks WHERE conversation_id=? AND task_id=?",
                (conversation_id, task_id),
            ).fetchone()
            if row is None:
                raise AuthorizationError("conversation task is unavailable")
            return dict(row)
