"""Durable response obligations: request/answer ownership for conversations.

An obligation records that an exact accepted request event demands an answer
from one responsible recipient harness.  It never competes with delivery
custody: ``recipient_committed`` is only ever mirrored from the durable
mailbox recipient record, and a terminal ``completed``/``failed`` state is
only ever written atomically with the typed response event that binds the
original request identifier and payload digest.  Prose replies and unrelated
events can never close an obligation.
"""

from __future__ import annotations

import json
import re
import time
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import NAMESPACE_URL, uuid5

from jsonschema import Draft202012Validator
from jsonschema.exceptions import SchemaError as JsonSchemaError
from jsonschema.exceptions import ValidationError as JsonSchemaValidationError
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification, DeliveryFact
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.response_obligation_schema import require_response_obligation_schema


IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$")

ObligationState = Literal[
    "created",
    "recipient_committed",
    "acknowledged",
    "in_progress",
    "pending_human",
    "blocked",
    "completed",
    "failed",
    "canceled",
    "expired",
]

OBLIGATION_TERMINAL_STATES: frozenset[str] = frozenset(
    {"completed", "failed", "canceled", "expired"}
)

OBLIGATION_TRANSITIONS: dict[str, frozenset[str]] = {
    "created": frozenset(
        {"recipient_committed", "acknowledged", "completed", "failed", "canceled", "expired"}
    ),
    "recipient_committed": frozenset(
        {"acknowledged", "completed", "failed", "canceled", "expired"}
    ),
    "acknowledged": frozenset(
        {"in_progress", "pending_human", "blocked", "completed", "failed", "canceled", "expired"}
    ),
    "in_progress": frozenset(
        {"pending_human", "blocked", "completed", "failed", "canceled", "expired"}
    ),
    "pending_human": frozenset(
        {"acknowledged", "in_progress", "completed", "failed", "canceled", "expired"}
    ),
    "blocked": frozenset(
        {"acknowledged", "in_progress", "completed", "failed", "canceled", "expired"}
    ),
}

# States the responsible recipient may assert directly.  Terminal response
# outcomes are excluded: they only exist through the typed response event.
RECIPIENT_ASSERTABLE_STATES: frozenset[str] = frozenset(
    {"recipient_committed", "acknowledged", "in_progress", "pending_human", "blocked"}
)

# Durable mailbox facts that prove the recipient committed request custody.
_COMMITTED_DELIVERY_FACTS: frozenset[str] = frozenset(
    {
        DeliveryFact.RECIPIENT_COMMITTED.value,
        DeliveryFact.PRESENTED.value,
        DeliveryFact.PROCESSING.value,
        DeliveryFact.EFFECT_PREPARED.value,
        DeliveryFact.COMPLETED.value,
    }
)

# Mailbox facts that mean the recipient has not observed the entry yet.
_UNSEEN_DELIVERY_FACTS: frozenset[str] = frozenset(
    {
        DeliveryFact.ACCEPTED_LOCAL.value,
        DeliveryFact.ACCEPTED_DURABLE.value,
        DeliveryFact.ACCEPTED_QUEUED.value,
        DeliveryFact.QUEUED.value,
        DeliveryFact.RETRY_SCHEDULED.value,
        DeliveryFact.DISPATCH_ATTEMPTED.value,
        DeliveryFact.REMOTE_ACCEPTED.value,
        DeliveryFact.RECIPIENT_COMMITTED.value,
    }
)


def require_obligation_transition(current: str, proposed: str) -> None:
    if current in OBLIGATION_TERMINAL_STATES:
        raise ConflictError(
            f"response obligation is terminal; illegal transition {current} -> {proposed}"
        )
    if proposed not in OBLIGATION_TRANSITIONS.get(current, frozenset()):
        raise ConflictError(f"illegal response-obligation transition {current} -> {proposed}")


class ResponseObligationSpec(BaseModel):
    """Opt-in request marker carried inside the exact request payload digest."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    response_required: bool = True
    responsible_harness_id: str | None = Field(default=None, min_length=1, max_length=256)
    deadline_at: datetime | None = None
    response_schema_id: str | None = None
    response_schema: dict[str, Any] | None = None

    @model_validator(mode="after")
    def response_is_actually_required(self) -> "ResponseObligationSpec":
        if not self.response_required:
            raise ValueError(
                "response_obligation is only valid when response_required is true"
            )
        if (self.response_schema_id is None) != (self.response_schema is None):
            raise ValueError(
                "response schema identifier and inline schema must be supplied together"
            )
        if self.response_schema is not None:
            encoded = canonical_json(self.response_schema)
            if len(encoded) > 65_536:
                raise ValueError("response schema exceeds the bounded size")
            try:
                Draft202012Validator.check_schema(self.response_schema)
            except JsonSchemaError as exc:
                raise ValueError("response schema is not valid JSON Schema 2020-12") from exc
            self._require_local_references(self.response_schema)
        return self

    @classmethod
    def _require_local_references(cls, value: Any) -> None:
        pending = [value]
        while pending:
            current = pending.pop()
            if isinstance(current, dict):
                for keyword in ("$ref", "$dynamicRef"):
                    reference = current.get(keyword)
                    if (
                        isinstance(reference, str)
                        and reference != ""
                        and not reference.startswith("#")
                    ):
                        raise ValueError("response schema references must be self-contained")
                pending.extend(current.values())
            elif isinstance(current, list):
                pending.extend(current)

    @field_validator("response_schema_id")
    @classmethod
    def bounded_schema_id(cls, value: str | None) -> str | None:
        if value is not None and not IDENTIFIER.fullmatch(value):
            raise ValueError("response schema identifier is invalid")
        return value

    @field_validator("deadline_at")
    @classmethod
    def deadline_is_aware(cls, value: datetime | None) -> datetime | None:
        if value is not None and (value.tzinfo is None or value.utcoffset() is None):
            raise ValueError("response deadline must be timezone-aware")
        return value


def obligation_id_for(request_event_id: str, responsible_harness_id: str) -> str:
    return str(
        uuid5(
            NAMESPACE_URL,
            f"agentnet:response-obligation:{request_event_id}:{responsible_harness_id}",
        )
    )


def _row_view(row: Any) -> dict[str, Any]:
    return {
        "obligation_id": row["obligation_id"],
        "domain_id": row["domain_id"],
        "conversation_id": row["conversation_id"],
        "thread_id": row["thread_id"],
        "request_event_id": row["request_event_id"],
        "request_payload_digest": row["request_payload_digest"],
        "request_envelope_digest": row["request_envelope_digest"],
        "requester_authority_id": row["requester_authority_id"],
        "requester_harness_id": row["requester_harness_id"],
        "responsible_authority_id": row["responsible_authority_id"],
        "responsible_harness_id": row["responsible_harness_id"],
        "response_required": bool(row["response_required"]),
        "response_schema_id": row["response_schema_id"],
        "response_schema_digest": row["response_schema_digest"],
        "state": row["state"],
        "state_reason": row["state_reason"],
        "revision": int(row["revision"]),
        "deadline_at": row["deadline_at"],
        "policy_revision": int(row["policy_revision"]),
        "response_event_id": row["response_event_id"],
        "response_payload_digest": row["response_payload_digest"],
        "response_outcome": row["response_outcome"],
        "created_at": int(row["created_at"]),
        "updated_at": int(row["updated_at"]),
        "closed_at": row["closed_at"],
    }


class ResponseObligationService:
    """Durable request/answer ownership with fail-closed typed closure."""

    def __init__(self, store: StoreBackend, policy: PolicyEngine) -> None:
        require_response_obligation_schema(store)
        self.store = store
        self.policy = policy

    # -- shared validation -------------------------------------------------

    @staticmethod
    def _authority_id(actor: VerifiedActor) -> str:
        authority_id = actor.positive_authority_id
        if (
            authority_id is None
            or actor.harness_id is None
            or actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}
        ):
            raise AuthorizationError(
                "response obligations require a verified human or host guest plus harness"
            )
        return authority_id

    def _require_current_actor(
        self,
        connection: Any,
        actor: VerifiedActor,
        *,
        now: int,
        classification: Classification,
    ) -> tuple[str, int]:
        authority_id = self._authority_id(actor)
        domain = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None or domain["status"] != "active":
            raise AuthorizationError("response obligation trust domain is unavailable")
        revision = int(domain["policy_revision"])
        local_lab_allowed = self.policy.allows_local_conformance_conversation_harness(
            binding_assurance=actor.binding_assurance,
            classification=classification,
        )
        if actor.binding_assurance == "lab" and not local_lab_allowed:
            raise AuthorizationError(
                "response obligation actor is not current: synthetic_lab_harness_not_admitted"
            )
        denial, _current = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=datetime.fromtimestamp(now, UTC),
            allow_deterministic_only=local_lab_allowed,
        )
        if denial is not None:
            raise AuthorizationError(f"response obligation actor is not current: {denial}")
        return authority_id, revision

    def _load_for_update(self, connection: Any, obligation_id: str) -> Any:
        row = connection.execute(
            "SELECT * FROM response_obligations WHERE obligation_id=?",
            (obligation_id,),
        ).fetchone()
        if row is None:
            raise AuthorizationError("response obligation is unavailable")
        return row

    def _decide(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        revision: int,
        classification: Classification,
        context: dict[str, Any],
        now: int,
    ) -> Any:
        decision = self.policy._decide_in_transaction(
            connection,
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=resource,
                operation_class=OperationClass.BUSINESS,
                policy_revision=revision,
                context=context,
                classification=classification,
            ),
            when=datetime.fromtimestamp(now, UTC),
        )
        if not decision.allowed:
            raise AuthorizationError(decision.reason)
        return decision

    def _classification(self, connection: Any, conversation_id: str) -> Classification:
        row = connection.execute(
            "SELECT classification FROM conversations WHERE conversation_id=?",
            (conversation_id,),
        ).fetchone()
        if row is None:
            raise AuthorizationError("response obligation conversation is unavailable")
        return Classification(row["classification"])

    def _record_transition(
        self,
        connection: Any,
        *,
        row: Any,
        to_state: str,
        actor_view: dict[str, Any],
        detail: dict[str, Any],
        now: int,
        response_event_id: str | None = None,
        response_payload_digest: str | None = None,
        response_outcome: str | None = None,
        state_reason: str,
    ) -> dict[str, Any]:
        require_obligation_transition(str(row["state"]), to_state)
        next_revision = int(row["revision"]) + 1
        closed_at = now if to_state in OBLIGATION_TERMINAL_STATES else None
        updated = connection.execute(
            """UPDATE response_obligations
                  SET state=?,state_reason=?,revision=?,updated_at=?,closed_at=?,
                      response_event_id=COALESCE(?,response_event_id),
                      response_payload_digest=COALESCE(?,response_payload_digest),
                      response_outcome=COALESCE(?,response_outcome)
                WHERE obligation_id=? AND revision=?""",
            (
                to_state,
                state_reason,
                next_revision,
                now,
                closed_at,
                response_event_id,
                response_payload_digest,
                response_outcome,
                row["obligation_id"],
                int(row["revision"]),
            ),
        )
        if updated.rowcount != 1:
            raise ConflictError("response obligation transition raced with another mutation")
        connection.execute(
            """INSERT INTO response_obligation_transitions(
                obligation_id,revision,from_state,to_state,actor_json,detail_json,
                response_event_id,created_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                row["obligation_id"],
                next_revision,
                row["state"],
                to_state,
                canonical_json(actor_view).decode("utf-8"),
                canonical_json(detail).decode("utf-8"),
                response_event_id,
                now,
            ),
        )
        audit_entry = {
            "action": "response_obligation.transition",
            "obligation_id": row["obligation_id"],
            "conversation_id": row["conversation_id"],
            "request_event_id": row["request_event_id"],
            "from": row["state"],
            "to": to_state,
            "revision": next_revision,
            "actor": actor_view,
            "detail": detail,
        }
        if response_event_id is not None:
            audit_entry["response_event_id"] = response_event_id
        self.store.append_audit(connection, audit_entry)
        return {
            "obligation_id": row["obligation_id"],
            "state": to_state,
            "revision": next_revision,
        }

    # -- creation (composed by ConversationService) ------------------------

    def create_in_transaction(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        requester_authority_id: str,
        spec: ResponseObligationSpec,
        request_event: Any,
        request_envelope_digest: str,
        responsible_harness_id: str,
        responsible_authority_id: str,
        classification: Classification,
        policy_revision: int,
        now: int,
    ) -> dict[str, Any]:
        """Create the obligation on the exact transaction that accepted the request."""

        if actor.harness_id is None or actor.harness_id != request_event.actor.harness_id:
            raise AuthorizationError("response obligation requester must be the request author")
        if responsible_harness_id == actor.harness_id:
            raise ValidationError("a response obligation cannot name its requester as responsible")
        if spec.deadline_at is not None and int(spec.deadline_at.timestamp()) <= now:
            raise ValidationError("response obligation deadline must be in the future")
        self._decide(
            connection,
            actor=actor,
            action="conversation.response_obligation.create",
            resource=f"conversation:{request_event.conversation_id}",
            revision=policy_revision,
            classification=classification,
            context={
                "request_event_id": request_event.event_id,
                "request_digest": request_event.payload_digest,
                "responsible_harness_id": responsible_harness_id,
            },
            now=now,
        )
        obligation_id = obligation_id_for(request_event.event_id, responsible_harness_id)
        connection.execute(
            """INSERT INTO response_obligations(
                obligation_id,domain_id,conversation_id,thread_id,request_event_id,
                request_payload_digest,request_envelope_digest,requester_authority_id,
                requester_harness_id,responsible_authority_id,responsible_harness_id,
                response_required,response_schema_id,response_schema_json,response_schema_digest,
                state,state_reason,revision,
                deadline_at,policy_revision,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'created','requested',1,?,?,?,?)""",
            (
                obligation_id,
                request_event.domain_id,
                request_event.conversation_id,
                request_event.thread_id,
                request_event.event_id,
                request_event.payload_digest,
                request_envelope_digest,
                requester_authority_id,
                actor.harness_id,
                responsible_authority_id,
                responsible_harness_id,
                int(spec.response_required),
                spec.response_schema_id,
                (
                    canonical_json(spec.response_schema).decode("utf-8")
                    if spec.response_schema is not None
                    else None
                ),
                canonical_digest(spec.response_schema) if spec.response_schema is not None else None,
                int(spec.deadline_at.timestamp()) if spec.deadline_at else None,
                policy_revision,
                now,
                now,
            ),
        )
        self.store.append_audit(
            connection,
            {
                "action": "response_obligation.created",
                "obligation_id": obligation_id,
                "conversation_id": request_event.conversation_id,
                "request_event_id": request_event.event_id,
                "request_digest": request_event.payload_digest,
                "requester": actor.audit_view(),
                "responsible_harness_id": responsible_harness_id,
                "response_required": spec.response_required,
                "deadline_at": int(spec.deadline_at.timestamp()) if spec.deadline_at else None,
            },
        )
        return {"obligation_id": obligation_id, "state": "created", "revision": 1}

    # -- typed terminal response (composed by ConversationService) ---------

    def require_open_for_response_in_transaction(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        responder_authority_id: str,
        obligation_id: str,
        request_event_id: str,
        request_digest: str,
        conversation_id: str,
        thread_id: str,
    ) -> Any:
        """Bind the response to the exact open obligation before acceptance."""

        row = self._load_for_update(connection, obligation_id)
        if (
            row["conversation_id"] != conversation_id
            or row["thread_id"] != thread_id
            or row["domain_id"] != actor.domain_id
        ):
            raise AuthorizationError("response does not bind the obligation's exact conversation")
        if (
            row["request_event_id"] != request_event_id
            or row["request_payload_digest"] != request_digest
        ):
            raise AuthorizationError(
                "response does not bind the exact original request identifier and digest"
            )
        if (
            row["responsible_harness_id"] != actor.harness_id
            or row["responsible_authority_id"] != responder_authority_id
        ):
            raise AuthorizationError(
                "response must come from the exact responsible recipient harness"
            )
        if row["state"] in OBLIGATION_TERMINAL_STATES:
            raise ConflictError("response obligation already has a terminal outcome")
        return row

    @staticmethod
    def validate_structured_response(row: Any, structured_response: dict[str, Any]) -> None:
        """Validate against the exact immutable schema stored with the request."""

        schema_json = row["response_schema_json"]
        schema_digest = row["response_schema_digest"]
        schema_id = row["response_schema_id"]
        if schema_json is None:
            if schema_id is not None or schema_digest is not None:
                raise ValidationError("response obligation schema binding is incomplete")
            return
        if schema_id is None or schema_digest is None:
            raise ValidationError("response obligation schema binding is incomplete")
        try:
            schema = json.loads(str(schema_json))
        except (TypeError, json.JSONDecodeError) as exc:
            raise ValidationError("response obligation schema binding is invalid") from exc
        if canonical_digest(schema) != schema_digest:
            raise ValidationError("response obligation schema digest does not match")
        try:
            Draft202012Validator(schema).validate(structured_response)
        except JsonSchemaValidationError as exc:
            raise ValidationError("structured response does not satisfy the demanded schema") from exc

    def close_with_response_in_transaction(
        self,
        connection: Any,
        *,
        row: Any,
        actor: VerifiedActor,
        outcome: Literal["completed", "failed"],
        response_event_id: str,
        response_payload_digest: str,
        policy_decision_id: str,
        now: int,
    ) -> dict[str, Any]:
        classification = self._classification(connection, row["conversation_id"])
        authority_id, revision = self._require_current_actor(
            connection,
            actor,
            now=now,
            classification=classification,
        )
        if (
            row["responsible_harness_id"] != actor.harness_id
            or row["responsible_authority_id"] != authority_id
            or row["domain_id"] != actor.domain_id
        ):
            raise AuthorizationError(
                "response must come from the exact responsible recipient harness"
            )
        transition_decision = self._decide(
            connection,
            actor=actor,
            action="conversation.response_obligation.transition",
            resource=f"conversation:{row['conversation_id']}",
            revision=revision,
            classification=classification,
            context={
                "obligation_id": row["obligation_id"],
                "to_state": outcome,
                "response_event_id": response_event_id,
                "response_payload_digest": response_payload_digest,
            },
            now=now,
        )
        return self._record_transition(
            connection,
            row=row,
            to_state=outcome,
            actor_view=actor.audit_view(),
            detail={
                "kind": "typed_response",
                "policy_decision_id": transition_decision.decision_id,
                "conversation_policy_decision_id": policy_decision_id,
                "request_event_id": row["request_event_id"],
                "request_digest": row["request_payload_digest"],
            },
            now=now,
            response_event_id=response_event_id,
            response_payload_digest=response_payload_digest,
            response_outcome=outcome,
            state_reason=f"typed_response_{outcome}",
        )

    # -- recipient-progress transitions -------------------------------------

    def transition(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        if to_state not in RECIPIENT_ASSERTABLE_STATES:
            raise ValidationError(
                "only recipient progress states may be asserted directly; "
                "terminal outcomes require a typed response, cancellation, or expiry"
            )
        if not IDENTIFIER.fullmatch(reason):
            raise ValidationError("response obligation reason must be a bounded code")
        now = int(time.time())
        with self.store.transaction() as connection:
            row = self._load_for_update(connection, obligation_id)
            classification = self._classification(connection, row["conversation_id"])
            authority_id, revision = self._require_current_actor(
                connection, actor, now=now, classification=classification
            )
            if (
                row["responsible_harness_id"] != actor.harness_id
                or row["responsible_authority_id"] != authority_id
                or row["domain_id"] != actor.domain_id
            ):
                raise AuthorizationError(
                    "obligation progress must come from the exact responsible recipient harness"
                )
            if expected_revision is not None and int(row["revision"]) != expected_revision:
                raise ConflictError("response obligation revision fence does not match")
            if to_state == "recipient_committed":
                self._require_committed_delivery(connection, row)
            decision = self._decide(
                connection,
                actor=actor,
                action="conversation.response_obligation.transition",
                resource=f"conversation:{row['conversation_id']}",
                revision=revision,
                classification=classification,
                context={"obligation_id": obligation_id, "to_state": to_state},
                now=now,
            )
            return self._record_transition(
                connection,
                row=row,
                to_state=to_state,
                actor_view=actor.audit_view(),
                detail={
                    "kind": "recipient_progress",
                    "policy_decision_id": decision.decision_id,
                    "reason": reason,
                },
                now=now,
                state_reason=reason,
            )

    @staticmethod
    def _require_committed_delivery(connection: Any, row: Any) -> None:
        """``recipient_committed`` mirrors the durable mailbox fact, never asserts it."""

        delivery = connection.execute(
            "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
            (row["request_event_id"], row["responsible_harness_id"]),
        ).fetchone()
        if delivery is None or str(delivery["current_fact"]) not in _COMMITTED_DELIVERY_FACTS:
            raise ConflictError(
                "recipient commitment requires the durable mailbox recipient fact"
            )

    # -- requester cancellation ---------------------------------------------

    def cancel(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        if not IDENTIFIER.fullmatch(reason_code):
            raise ValidationError("cancellation reason must be a bounded code, not content")
        now = int(time.time())
        with self.store.transaction() as connection:
            row = self._load_for_update(connection, obligation_id)
            classification = self._classification(connection, row["conversation_id"])
            authority_id, revision = self._require_current_actor(
                connection, actor, now=now, classification=classification
            )
            if (
                row["requester_authority_id"] != authority_id
                or row["requester_harness_id"] != actor.harness_id
                or row["domain_id"] != actor.domain_id
            ):
                raise AuthorizationError(
                    "obligation cancellation must come from the exact accountable requester"
                )
            if expected_revision is not None and int(row["revision"]) != expected_revision:
                raise ConflictError("response obligation revision fence does not match")
            decision = self._decide(
                connection,
                actor=actor,
                action="conversation.response_obligation.cancel",
                resource=f"conversation:{row['conversation_id']}",
                revision=revision,
                classification=classification,
                context={"obligation_id": obligation_id, "reason_code": reason_code},
                now=now,
            )
            return self._record_transition(
                connection,
                row=row,
                to_state="canceled",
                actor_view=actor.audit_view(),
                detail={
                    "kind": "requester_cancel",
                    "policy_decision_id": decision.decision_id,
                    "reason_code": reason_code,
                },
                now=now,
                state_reason=reason_code,
            )

    # -- durable reconciliation ----------------------------------------------

    def reconcile(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
        authoritative_now: datetime | None = None,
    ) -> dict[str, Any]:
        """Restart/offline-safe derived reconciliation for one verified party.

        Two derived mutations only, both re-executing already-authorized facts:

        - obligations awaiting the responsible party's commitment move to
          ``recipient_committed`` exactly when the durable mailbox recipient
          record already proves it;
        - the requester's own overdue obligations move to ``expired`` exactly
          when the deadline bound at authorized creation has passed.

        Each derived mutation crosses the canonical transition PEP after actor
        currency and exact requester/responsible harness ownership are revalidated.
        """

        if not 1 <= limit <= 1000:
            raise ValidationError("reconcile limit must be between 1 and 1000")
        now = int((authoritative_now or datetime.now(UTC)).timestamp())
        committed: list[str] = []
        expired: list[str] = []
        with self.store.transaction() as connection:
            # Reconciliation mutates no content and mints no authority, so the
            # actor-currency reference classification is the C0 floor.
            authority_id, _revision = self._require_current_actor(
                connection,
                actor,
                now=now,
                classification=Classification.C0_PUBLIC,
            )
            pending_commit = connection.execute(
                """SELECT o.* FROM response_obligations o
                    JOIN recipients r
                      ON r.event_id=o.request_event_id
                     AND r.recipient_id=o.responsible_harness_id
                   WHERE o.domain_id=? AND o.state='created'
                     AND (
                         (o.responsible_authority_id=? AND o.responsible_harness_id=?)
                         OR (o.requester_authority_id=? AND o.requester_harness_id=?)
                     )
                   ORDER BY o.created_at,o.obligation_id LIMIT ?""",
                (
                    actor.domain_id,
                    authority_id,
                    actor.harness_id,
                    authority_id,
                    actor.harness_id,
                    limit,
                ),
            ).fetchall()
            for row in pending_commit:
                delivery = connection.execute(
                    "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
                    (row["request_event_id"], row["responsible_harness_id"]),
                ).fetchone()
                if (
                    delivery is None
                    or str(delivery["current_fact"]) not in _COMMITTED_DELIVERY_FACTS
                ):
                    continue
                classification = self._classification(connection, row["conversation_id"])
                _current_authority_id, revision = self._require_current_actor(
                    connection,
                    actor,
                    now=now,
                    classification=classification,
                )
                decision = self._decide(
                    connection,
                    actor=actor,
                    action="conversation.response_obligation.transition",
                    resource=f"conversation:{row['conversation_id']}",
                    revision=revision,
                    classification=classification,
                    context={
                        "obligation_id": row["obligation_id"],
                        "to_state": "recipient_committed",
                        "source": "reconcile_delivery_fact",
                    },
                    now=now,
                )
                self._record_transition(
                    connection,
                    row=row,
                    to_state="recipient_committed",
                    actor_view=actor.audit_view(),
                    detail={
                        "kind": "derived_from_delivery_fact",
                        "delivery_fact": str(delivery["current_fact"]),
                        "policy_decision_id": decision.decision_id,
                    },
                    now=now,
                    state_reason="delivery_fact_mirrored",
                )
                committed.append(str(row["obligation_id"]))
            overdue = connection.execute(
                """SELECT * FROM response_obligations
                   WHERE domain_id=? AND requester_authority_id=? AND requester_harness_id=?
                     AND deadline_at IS NOT NULL AND deadline_at<=?
                     AND state NOT IN ('completed','failed','canceled','expired')
                   ORDER BY deadline_at,obligation_id LIMIT ?""",
                (actor.domain_id, authority_id, actor.harness_id, now, limit),
            ).fetchall()
            for row in overdue:
                classification = self._classification(connection, row["conversation_id"])
                _current_authority_id, revision = self._require_current_actor(
                    connection,
                    actor,
                    now=now,
                    classification=classification,
                )
                decision = self._decide(
                    connection,
                    actor=actor,
                    action="conversation.response_obligation.transition",
                    resource=f"conversation:{row['conversation_id']}",
                    revision=revision,
                    classification=classification,
                    context={
                        "obligation_id": row["obligation_id"],
                        "to_state": "expired",
                        "source": "reconcile_deadline",
                    },
                    now=now,
                )
                self._record_transition(
                    connection,
                    row=row,
                    to_state="expired",
                    actor_view=actor.audit_view(),
                    detail={
                        "kind": "deadline_expiry",
                        "deadline_at": int(row["deadline_at"]),
                        "authoritative_clock": now,
                        "policy_decision_id": decision.decision_id,
                    },
                    now=now,
                    state_reason="deadline_expired",
                )
                expired.append(str(row["obligation_id"]))
            if committed or expired:
                self.store.append_audit(
                    connection,
                    {
                        "action": "response_obligation.reconciled",
                        "actor": actor.audit_view(),
                        "recipient_committed": committed,
                        "expired": expired,
                        "clock": now,
                    },
                )
        return {"recipient_committed": committed, "expired": expired}

    # -- exact-fetch and inbox visibility -------------------------------------

    def _require_party(
        self,
        connection: Any,
        actor: VerifiedActor,
        row: Any,
    ) -> tuple[str, int, Classification]:
        classification = self._classification(connection, row["conversation_id"])
        authority_id, revision = self._require_current_actor(
            connection,
            actor,
            now=int(time.time()),
            classification=classification,
        )
        if row["domain_id"] != actor.domain_id:
            raise AuthorizationError("response obligation is unavailable")
        if (
            row["requester_authority_id"] == authority_id
            and row["requester_harness_id"] == actor.harness_id
        ):
            return "requester", revision, classification
        if (
            row["responsible_authority_id"] == authority_id
            and row["responsible_harness_id"] == actor.harness_id
        ):
            return "responsible", revision, classification
        raise AuthorizationError("response obligation is unavailable")

    def get(self, *, actor: VerifiedActor, obligation_id: str) -> dict[str, Any]:
        with self.store.transaction(immediate=False) as connection:
            row = connection.execute(
                "SELECT * FROM response_obligations WHERE obligation_id=?",
                (obligation_id,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("response obligation is unavailable")
            role, revision, classification = self._require_party(connection, actor, row)
            self._decide(
                connection,
                actor=actor,
                action="conversation.response_obligation.read",
                resource=f"conversation:{row['conversation_id']}",
                revision=revision,
                classification=classification,
                context={"obligation_id": obligation_id, "exposure": "get"},
                now=int(time.time()),
            )
            transitions = connection.execute(
                """SELECT revision,from_state,to_state,detail_json,response_event_id,created_at
                     FROM response_obligation_transitions
                    WHERE obligation_id=? ORDER BY revision""",
                (obligation_id,),
            ).fetchall()
            return _row_view(row) | {
                "viewer_role": role,
                "transitions": [
                    {
                        "revision": int(item["revision"]),
                        "from_state": item["from_state"],
                        "to_state": item["to_state"],
                        "detail": json.loads(item["detail_json"]),
                        "response_event_id": item["response_event_id"],
                        "created_at": int(item["created_at"]),
                    }
                    for item in transitions
                ],
            }

    def list_for(
        self,
        *,
        actor: VerifiedActor,
        role: Literal["requester", "responsible", "any"] = "any",
        states: tuple[str, ...] = (),
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        if not 1 <= limit <= 1000:
            raise ValidationError("obligation list limit must be between 1 and 1000")
        known_states = set(OBLIGATION_TRANSITIONS) | OBLIGATION_TERMINAL_STATES
        if any(state not in known_states for state in states):
            raise ValidationError("obligation list names an unknown state")
        with self.store.transaction(immediate=False) as connection:
            authority_id, _revision = self._require_current_actor(
                connection,
                actor,
                now=int(time.time()),
                classification=Classification.C0_PUBLIC,
            )
            clauses = ["domain_id=?"]
            parameters: list[Any] = [actor.domain_id]
            if role == "requester":
                clauses.extend(("requester_authority_id=?", "requester_harness_id=?"))
                parameters.extend((authority_id, actor.harness_id))
            elif role == "responsible":
                clauses.extend(("responsible_authority_id=?", "responsible_harness_id=?"))
                parameters.extend((authority_id, actor.harness_id))
            else:
                clauses.append(
                    "((requester_authority_id=? AND requester_harness_id=?) "
                    "OR (responsible_authority_id=? AND responsible_harness_id=?))"
                )
                parameters.extend(
                    (authority_id, actor.harness_id, authority_id, actor.harness_id)
                )
            if states:
                clauses.append(f"state IN ({','.join('?' for _ in states)})")
                parameters.extend(states)
            parameters.append(limit)
            rows = connection.execute(
                f"""SELECT * FROM response_obligations WHERE {' AND '.join(clauses)}
                    ORDER BY created_at,obligation_id LIMIT ?""",
                tuple(parameters),
            ).fetchall()
            for conversation_id in dict.fromkeys(
                str(row["conversation_id"]) for row in rows
            ):
                classification = self._classification(connection, conversation_id)
                _current_authority_id, revision = self._require_current_actor(
                    connection,
                    actor,
                    now=int(time.time()),
                    classification=classification,
                )
                self._decide(
                    connection,
                    actor=actor,
                    action="conversation.response_obligation.read",
                    resource=f"conversation:{conversation_id}",
                    revision=revision,
                    classification=classification,
                    context={
                        "exposure": "list",
                        "role": role,
                        "states": list(states),
                        "limit": limit,
                    },
                    now=int(time.time()),
                )
            return [_row_view(row) for row in rows]

    def inbox(self, *, actor: VerifiedActor, now: int | None = None) -> dict[str, int]:
        """Privacy-safe counters distinguishing why attention is needed.

        Counters are derived, content-free, and never mutate state.  ``overdue``
        intentionally overlaps ``action_required``/``awaiting_peer``: an overdue
        item stays owned until it is answered, canceled, or expired.
        """

        now = int(time.time()) if now is None else now
        open_states = ("created", "recipient_committed", "acknowledged", "in_progress", "blocked")
        with self.store.transaction(immediate=False) as connection:
            authority_id, _revision = self._require_current_actor(
                connection,
                actor,
                now=now,
                classification=Classification.C0_PUBLIC,
            )
            obligation_conversations = connection.execute(
                """SELECT DISTINCT conversation_id FROM response_obligations
                    WHERE domain_id=?
                      AND (
                          (requester_authority_id=? AND requester_harness_id=?)
                          OR (responsible_authority_id=? AND responsible_harness_id=?)
                      )
                    ORDER BY conversation_id""",
                (
                    actor.domain_id,
                    authority_id,
                    actor.harness_id,
                    authority_id,
                    actor.harness_id,
                ),
            ).fetchall()
            for conversation_row in obligation_conversations:
                conversation_id = str(conversation_row["conversation_id"])
                classification = self._classification(connection, conversation_id)
                _current_authority_id, revision = self._require_current_actor(
                    connection,
                    actor,
                    now=now,
                    classification=classification,
                )
                self._decide(
                    connection,
                    actor=actor,
                    action="conversation.response_obligation.read",
                    resource=f"conversation:{conversation_id}",
                    revision=revision,
                    classification=classification,
                    context={"exposure": "inbox"},
                    now=now,
                )
            def count(sql: str, parameters: tuple[Any, ...]) -> int:
                row = connection.execute(sql, parameters).fetchone()
                return int(row["total"]) if row is not None else 0

            open_marks = ",".join("?" for _ in open_states)
            unread_information = count(
                f"""SELECT COUNT(*) AS total FROM recipients r
                     JOIN events e ON e.event_id=r.event_id
                    WHERE r.recipient_id=? AND e.domain_id=?
                      AND r.current_fact IN ({','.join('?' for _ in _UNSEEN_DELIVERY_FACTS)})
                      AND NOT EXISTS (
                          SELECT 1 FROM response_obligations o
                           WHERE o.request_event_id=r.event_id
                             AND o.responsible_harness_id=r.recipient_id
                      )""",
                (actor.harness_id, actor.domain_id, *sorted(_UNSEEN_DELIVERY_FACTS)),
            )
            action_required = count(
                f"""SELECT COUNT(*) AS total FROM response_obligations
                    WHERE domain_id=? AND responsible_authority_id=?
                      AND responsible_harness_id=? AND response_required=1
                      AND state IN ({open_marks})""",
                (actor.domain_id, authority_id, actor.harness_id, *open_states),
            )
            awaiting_peer = count(
                f"""SELECT COUNT(*) AS total FROM response_obligations
                    WHERE domain_id=? AND requester_authority_id=? AND requester_harness_id=?
                      AND response_required=1
                      AND state IN ({open_marks})""",
                (actor.domain_id, authority_id, actor.harness_id, *open_states),
            )
            awaiting_human = count(
                """SELECT COUNT(*) AS total FROM response_obligations
                   WHERE domain_id=? AND state='pending_human'
                     AND (
                         (requester_authority_id=? AND requester_harness_id=?)
                         OR (responsible_authority_id=? AND responsible_harness_id=?)
                     )""",
                (
                    actor.domain_id,
                    authority_id,
                    actor.harness_id,
                    authority_id,
                    actor.harness_id,
                ),
            )
            overdue = count(
                f"""SELECT COUNT(*) AS total FROM response_obligations
                    WHERE domain_id=?
                      AND (
                          (requester_authority_id=? AND requester_harness_id=?)
                          OR (responsible_authority_id=? AND responsible_harness_id=?)
                      )
                      AND deadline_at IS NOT NULL AND deadline_at<=?
                      AND state IN ({open_marks},'pending_human')""",
                (
                    actor.domain_id,
                    authority_id,
                    actor.harness_id,
                    authority_id,
                    actor.harness_id,
                    now,
                    *open_states,
                ),
            )
            failed = count(
                """SELECT COUNT(*) AS total FROM response_obligations
                   WHERE domain_id=? AND requester_authority_id=? AND requester_harness_id=?
                     AND state='failed'""",
                (actor.domain_id, authority_id, actor.harness_id),
            )
        return {
            "unread_information": unread_information,
            "action_required": action_required,
            "awaiting_peer": awaiting_peer,
            "awaiting_human": awaiting_human,
            "overdue": overdue,
            "failed": failed,
        }


__all__ = [
    "OBLIGATION_TERMINAL_STATES",
    "OBLIGATION_TRANSITIONS",
    "RECIPIENT_ASSERTABLE_STATES",
    "ResponseObligationService",
    "ResponseObligationSpec",
    "obligation_id_for",
    "require_obligation_transition",
]
