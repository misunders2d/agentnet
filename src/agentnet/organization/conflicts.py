"""Typed, atomic task-intent conflict detection and owner adjudication.

Conflict custody is deliberately narrower than authorization.  A released task
returns to the mailbox queue; it receives no data, tool, semantic-processing,
or business-effect authority from this service.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime
from enum import StrEnum
from itertools import combinations
from collections.abc import Callable
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.authorization.grants import epoch_seconds
from agentnet.authorization.policy import validate_actor_state
from agentnet.delivery.state import TERMINAL_FACTS, require_transition
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import DeliveryFact
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.post_audit_schema import require_post_audit_schema


class TaskAccessMode(StrEnum):
    READ = "read"
    WRITE = "write"


class TaskExclusivity(StrEnum):
    SHARED = "shared"
    EXCLUSIVE = "exclusive"


class TaskResourceIntent(BaseModel):
    """One exact operation over one canonical assignment resource."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    resource: str = Field(min_length=1, max_length=1024)
    operation: str = Field(min_length=1, max_length=256)
    access: TaskAccessMode
    exclusivity: TaskExclusivity

    @field_validator("resource", "operation")
    @classmethod
    def bounded_canonical_text(cls, value: str) -> str:
        if value != value.strip() or any(ord(character) < 32 for character in value):
            raise ValueError("task intent text must be canonical printable text")
        return value


class TaskExecutionIntent(BaseModel):
    """Complete, typed intent bound into an accepted assignment transaction."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    resources: tuple[TaskResourceIntent, ...] = Field(min_length=1, max_length=256)

    @model_validator(mode="after")
    def unique_operations(self) -> "TaskExecutionIntent":
        identities = [(item.resource, item.operation) for item in self.resources]
        if len(identities) != len(set(identities)):
            raise ValueError("task intent contains a duplicate resource operation")
        if tuple(sorted(identities)) != tuple(identities):
            raise ValueError("task intent resource operations must use canonical order")
        return self

    @classmethod
    def conservative(cls, *, task_type: str, resources: frozenset[str]) -> "TaskExecutionIntent":
        """Derive a fail-closed intent for legacy callers that omit typed intent."""

        return cls(
            resources=tuple(
                TaskResourceIntent(
                    resource=resource,
                    operation=task_type,
                    access=TaskAccessMode.WRITE,
                    exclusivity=TaskExclusivity.EXCLUSIVE,
                )
                for resource in sorted(resources)
            )
        )


class TaskConflictAdjudication(BaseModel):
    """Exact, replay-fenced partition of every member at one conflict revision."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    conflict_id: str = Field(min_length=1, max_length=256)
    expected_revision: int = Field(ge=1)
    expected_policy_revision: int = Field(ge=1)
    expected_domain_revocation_epoch: int = Field(ge=1)
    expected_recipient_credential_epoch: int = Field(ge=1)
    expected_member_event_ids: frozenset[str] = Field(min_length=2, max_length=1000)
    release_event_ids: frozenset[str] = Field(default_factory=frozenset, max_length=1000)
    reject_event_ids: frozenset[str] = Field(default_factory=frozenset, max_length=1000)
    reason_code: str = Field(min_length=1, max_length=128)

    @field_validator("reason_code")
    @classmethod
    def bounded_reason_code(cls, value: str) -> str:
        if not value.replace("_", "").replace("-", "").isalnum():
            raise ValueError("conflict reason must be a bounded code")
        return value

    @model_validator(mode="after")
    def complete_partition(self) -> "TaskConflictAdjudication":
        if self.release_event_ids & self.reject_event_ids:
            raise ValueError("conflict release and reject sets must be disjoint")
        if self.release_event_ids | self.reject_event_ids != self.expected_member_event_ids:
            raise ValueError("conflict decision must partition every exact member")
        return self


class TaskConflictOutcome(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    conflict_id: str
    state: Literal["resolved"] = "resolved"
    revision: int
    decision_digest: str
    released_event_ids: tuple[str, ...]
    rejected_event_ids: tuple[str, ...]
    data_access_authorized: Literal[False] = False
    semantic_processing_authorized: Literal[False] = False
    tool_authorized: Literal[False] = False
    effect_authorized: Literal[False] = False


class TaskConflictAdmission(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    fact: Literal[DeliveryFact.ACCEPTED_QUEUED, DeliveryFact.CONFLICT_PENDING]
    conflict_ids: tuple[str, ...] = ()


_OPEN_INTENT_STATES = frozenset({"active", "conflict_pending", "released"})
_HOLDABLE_FACTS = frozenset(
    {
        DeliveryFact.ACCEPTED_LOCAL,
        DeliveryFact.ACCEPTED_DURABLE,
        DeliveryFact.ACCEPTED_QUEUED,
        DeliveryFact.QUEUED,
        DeliveryFact.RETRY_SCHEDULED,
        DeliveryFact.CONFLICT_PENDING,
    }
)


def _incompatible(left: TaskResourceIntent, right: TaskResourceIntent) -> bool:
    if left.resource != right.resource and "*" not in {left.resource, right.resource}:
        return False
    return (
        left.access is TaskAccessMode.WRITE
        or right.access is TaskAccessMode.WRITE
        or left.exclusivity is TaskExclusivity.EXCLUSIVE
        or right.exclusivity is TaskExclusivity.EXCLUSIVE
    )


def conflict_resource_keys(
    left: TaskExecutionIntent,
    right: TaskExecutionIntent,
) -> frozenset[str]:
    keys: set[str] = set()
    for left_resource in left.resources:
        for right_resource in right.resources:
            if _incompatible(left_resource, right_resource):
                keys.add(
                    "*"
                    if "*" in {left_resource.resource, right_resource.resource}
                    else left_resource.resource
                )
    return frozenset(keys)


class TaskConflictService:
    """Atomic admission hold and exact subordinate-owner release/reject service."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        release_callback: Callable[[Any, str, dict[str, Any], int], None] | None = None,
    ) -> None:
        self.store = store
        self.release_callback = release_callback
        require_post_audit_schema(store)

    @staticmethod
    def _intent_json(intent: TaskExecutionIntent) -> str:
        return canonical_json(intent.model_dump(mode="json")).decode("utf-8")

    @staticmethod
    def _intent_from_row(row: Any) -> TaskExecutionIntent:
        try:
            return TaskExecutionIntent.model_validate_json(str(row["intent_json"]), strict=True)
        except Exception as exc:
            raise AuthorizationError("stored task intent is invalid") from exc

    @staticmethod
    def _conflict_id(*, domain_id: str, recipient_harness_id: str, resource_key: str) -> str:
        digest = canonical_digest(
            {
                "schema": "agentnet.task-conflict.v1",
                "domain_id": domain_id,
                "recipient_harness_id": recipient_harness_id,
                "resource_key": resource_key,
            }
        )
        return f"task-conflict:{digest}"

    @staticmethod
    def _recipient_owner(connection: Any, recipient_harness_id: str) -> tuple[Any, str]:
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?", (recipient_harness_id,)
        ).fetchone()
        if harness is None or harness["status"] != "active":
            raise AuthorizationError("task conflict recipient is not active")
        authority_id = harness["principal_id"] or harness["guest_id"]
        if not authority_id:
            raise AuthorizationError("task conflict recipient has no positive-authority owner")
        return harness, str(authority_id)

    @staticmethod
    def _current_domain(connection: Any, domain_id: str) -> Any:
        domain = connection.execute(
            "SELECT * FROM domains WHERE domain_id=?", (domain_id,)
        ).fetchone()
        if domain is None or domain["status"] != "active":
            raise AuthorizationError("task conflict domain is not active")
        return domain

    @staticmethod
    def _record_fact(
        connection: Any,
        *,
        event_id: str,
        recipient_harness_id: str,
        proposed: DeliveryFact,
        owner: dict[str, Any],
        detail: dict[str, Any],
        now: int,
    ) -> bool:
        row = connection.execute(
            """SELECT r.current_fact,e.envelope_digest
                 FROM recipients AS r JOIN events AS e ON e.event_id=r.event_id
                WHERE r.event_id=? AND r.recipient_id=?""",
            (event_id, recipient_harness_id),
        ).fetchone()
        if row is None:
            raise ConflictError("task conflict member is no longer in recipient custody")
        current = DeliveryFact(str(row["current_fact"]))
        if current is proposed:
            return False
        require_transition(current, proposed)
        cursor = connection.execute(
            """UPDATE recipients SET current_fact=?,updated_at=?
                 WHERE event_id=? AND recipient_id=? AND current_fact=?""",
            (proposed.value, now, event_id, recipient_harness_id, current.value),
        )
        if cursor.rowcount != 1:
            raise ConflictError("task conflict fact transition raced with another owner")
        connection.execute(
            """INSERT INTO receipts(
                receipt_id,event_id,recipient_id,fact,owner_actor_json,event_digest,detail_json,created_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                str(uuid4()),
                event_id,
                recipient_harness_id,
                proposed.value,
                canonical_json(owner).decode("utf-8"),
                row["envelope_digest"],
                canonical_json(detail).decode("utf-8"),
                now,
            ),
        )
        return True

    def _open_conflict(
        self,
        connection: Any,
        *,
        domain: Any,
        recipient_harness: Any,
        recipient_authority_id: str,
        resource_key: str,
        event_ids: set[str],
        now: int,
    ) -> tuple[str, int]:
        conflict_id = self._conflict_id(
            domain_id=str(domain["domain_id"]),
            recipient_harness_id=str(recipient_harness["harness_id"]),
            resource_key=resource_key,
        )
        inserted = connection.execute(
            """INSERT INTO task_conflicts(
                conflict_id,domain_id,recipient_harness_id,recipient_authority_id,
                policy_revision,domain_revocation_epoch,recipient_credential_epoch,
                resource_key,state,revision,created_at,updated_at,decided_at,
                decision_actor_json,decision_digest,reason_code
            ) VALUES(?,?,?,?,?,?,?,?,'pending',1,?,?,NULL,NULL,NULL,NULL)
            ON CONFLICT(conflict_id) DO NOTHING""",
            (
                conflict_id,
                domain["domain_id"],
                recipient_harness["harness_id"],
                recipient_authority_id,
                int(domain["policy_revision"]),
                int(domain["revocation_epoch"]),
                int(recipient_harness["credential_epoch"]),
                resource_key,
                now,
                now,
            ),
        ).rowcount == 1
        lock_suffix = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
        row = connection.execute(
            "SELECT * FROM task_conflicts WHERE conflict_id=?" + lock_suffix,
            (conflict_id,),
        ).fetchone()
        if row is None:  # pragma: no cover - database invariant
            raise ConflictError("task conflict could not be opened")
        existing_members = {
            str(member["event_id"])
            for member in connection.execute(
                "SELECT event_id FROM task_conflict_memberships WHERE conflict_id=?",
                (conflict_id,),
            ).fetchall()
        }
        revision = int(row["revision"])
        if row["state"] == "resolved":
            connection.execute(
                "DELETE FROM task_conflict_memberships WHERE conflict_id=?", (conflict_id,)
            )
            existing_members.clear()
            cursor = connection.execute(
                """UPDATE task_conflicts
                      SET recipient_authority_id=?,policy_revision=?,domain_revocation_epoch=?,
                          recipient_credential_epoch=?,state='pending',revision=revision+1,
                          updated_at=?,decided_at=NULL,decision_actor_json=NULL,
                          decision_digest=NULL,reason_code=NULL
                    WHERE conflict_id=? AND state='resolved' AND revision=?""",
                (
                    recipient_authority_id,
                    int(domain["policy_revision"]),
                    int(domain["revocation_epoch"]),
                    int(recipient_harness["credential_epoch"]),
                    now,
                    conflict_id,
                    revision,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task conflict reopen raced with another assignment")
            revision += 1
        elif not inserted and event_ids != existing_members:
            cursor = connection.execute(
                """UPDATE task_conflicts
                      SET revision=revision+1,updated_at=?
                    WHERE conflict_id=? AND state='pending' AND revision=?""",
                (now, conflict_id, revision),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task conflict membership raced with another assignment")
            revision += 1
        for event_id in sorted(event_ids):
            connection.execute(
                """INSERT INTO task_conflict_memberships(
                    conflict_id,event_id,member_state,joined_at,decided_at
                ) VALUES(?,?,'pending',?,NULL)
                ON CONFLICT(conflict_id,event_id) DO UPDATE SET
                    member_state='pending',decided_at=NULL""",
                (conflict_id, event_id, now),
            )
            intent_row = connection.execute(
                "SELECT state FROM task_execution_intents WHERE event_id=?", (event_id,)
            ).fetchone()
            if intent_row is None:
                raise ConflictError("task conflict member lacks an exact execution intent")
            if intent_row["state"] != "conflict_pending":
                connection.execute(
                    """UPDATE task_execution_intents
                          SET state='conflict_pending',state_revision=state_revision+1,updated_at=?
                        WHERE event_id=?""",
                    (now, event_id),
                )
            fact_row = connection.execute(
                "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
                (event_id, recipient_harness["harness_id"]),
            ).fetchone()
            if fact_row is not None and DeliveryFact(str(fact_row["current_fact"])) in _HOLDABLE_FACTS:
                self._record_fact(
                    connection,
                    event_id=event_id,
                    recipient_harness_id=str(recipient_harness["harness_id"]),
                    proposed=DeliveryFact.CONFLICT_PENDING,
                    owner={"authority": "task_conflict_service", "domain_id": domain["domain_id"]},
                    detail={
                        "conflict_id": conflict_id,
                        "conflict_revision": revision,
                        "resource_key": resource_key,
                    },
                    now=now,
                )
        self.store.append_audit(
            connection,
            {
                "action": "task_conflict.opened" if inserted else "task_conflict.updated",
                "conflict_id": conflict_id,
                "conflict_revision": revision,
                "member_event_ids": sorted(event_ids),
                "recipient_authority_id": recipient_authority_id,
                "resource_key": resource_key,
            },
        )
        return conflict_id, revision

    def record_accepted_in_transaction(
        self,
        connection: Any,
        *,
        event_id: str,
        domain_id: str,
        recipient_harness_id: str,
        sender_harness_id: str,
        sender_authority_id: str,
        authority_basis: Literal["directed_relationship", "recipient_owner_approval"],
        relationship_id: str | None,
        relationship_revision: int,
        intent: TaskExecutionIntent,
        continuation: dict[str, Any],
        deadline: datetime,
        when: datetime,
    ) -> TaskConflictAdmission:
        now = epoch_seconds(when)
        deadline_epoch = epoch_seconds(deadline)
        if deadline_epoch <= now:
            raise ValidationError("task intent deadline must be in the future")
        domain = self._current_domain(connection, domain_id)
        recipient_harness, recipient_authority_id = self._recipient_owner(
            connection, recipient_harness_id
        )
        if recipient_harness["domain_id"] != domain_id:
            raise AuthorizationError("task conflict recipient crossed its trust domain")
        intent_json = self._intent_json(intent)
        intent_digest = canonical_digest(intent.model_dump(mode="json"))
        continuation_digest = canonical_digest(continuation)
        existing = connection.execute(
            "SELECT * FROM task_execution_intents WHERE event_id=?", (event_id,)
        ).fetchone()
        if existing is not None:
            if (
                existing["domain_id"] != domain_id
                or existing["recipient_harness_id"] != recipient_harness_id
                or existing["sender_harness_id"] != sender_harness_id
                or existing["sender_authority_id"] != sender_authority_id
                or existing["authority_basis"] != authority_basis
                or existing["relationship_id"] != relationship_id
                or int(existing["relationship_revision"]) != relationship_revision
                or existing["intent_digest"] != intent_digest
                or existing["continuation_digest"] != continuation_digest
                or int(existing["deadline"]) != deadline_epoch
            ):
                raise IdempotencyConflict("accepted task event names different exact execution intent")
            conflicts = connection.execute(
                """SELECT m.conflict_id FROM task_conflict_memberships AS m
                     JOIN task_conflicts AS c ON c.conflict_id=m.conflict_id
                    WHERE m.event_id=? AND m.member_state='pending' AND c.state='pending'
                    ORDER BY m.conflict_id""",
                (event_id,),
            ).fetchall()
            conflict_ids = tuple(str(row["conflict_id"]) for row in conflicts)
            return TaskConflictAdmission(
                fact=(
                    DeliveryFact.CONFLICT_PENDING
                    if conflict_ids
                    else DeliveryFact.ACCEPTED_QUEUED
                ),
                conflict_ids=conflict_ids,
            )
        connection.execute(
            """INSERT INTO task_execution_intents(
                event_id,domain_id,recipient_harness_id,recipient_authority_id,
                sender_harness_id,sender_authority_id,authority_basis,relationship_id,
                relationship_revision,intent_schema_version,intent_json,intent_digest,
                continuation_encrypted,continuation_digest,continuation_applied,
                state,state_revision,deadline,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,?,?,'1.0',?,?,?,?,0,'active',1,?,?,?)""",
            (
                event_id,
                domain_id,
                recipient_harness_id,
                recipient_authority_id,
                sender_harness_id,
                sender_authority_id,
                authority_basis,
                relationship_id,
                relationship_revision,
                intent_json,
                intent_digest,
                self.store.cipher.encrypt_json(
                    continuation, purpose=f"task-intent-continuation:{event_id}"
                ),
                continuation_digest,
                deadline_epoch,
                now,
                now,
            ),
        )
        rows = connection.execute(
            """SELECT i.*,r.current_fact
                 FROM task_execution_intents AS i
                 JOIN recipients AS r ON r.event_id=i.event_id
                  AND r.recipient_id=i.recipient_harness_id
                WHERE i.domain_id=? AND i.recipient_harness_id=? AND i.event_id<>?
                  AND i.state IN ('active','conflict_pending','released') AND i.deadline>? 
                ORDER BY i.event_id""",
            (domain_id, recipient_harness_id, event_id, now),
        ).fetchall()
        collisions: dict[str, set[str]] = {}
        for row in rows:
            fact = DeliveryFact(str(row["current_fact"]))
            if fact in TERMINAL_FACTS or fact not in _HOLDABLE_FACTS:
                continue
            for resource_key in conflict_resource_keys(intent, self._intent_from_row(row)):
                collisions.setdefault(resource_key, {event_id}).add(str(row["event_id"]))
        conflict_ids: list[str] = []
        for resource_key, event_ids in sorted(collisions.items()):
            conflict_id, _revision = self._open_conflict(
                connection,
                domain=domain,
                recipient_harness=recipient_harness,
                recipient_authority_id=recipient_authority_id,
                resource_key=resource_key,
                event_ids=event_ids,
                now=now,
            )
            conflict_ids.append(conflict_id)
        return TaskConflictAdmission(
            fact=(
                DeliveryFact.CONFLICT_PENDING
                if conflict_ids
                else DeliveryFact.ACCEPTED_QUEUED
            ),
            conflict_ids=tuple(conflict_ids),
        )

    def mark_continuation_applied_in_transaction(
        self,
        connection: Any,
        *,
        event_id: str,
    ) -> None:
        cursor = connection.execute(
            """UPDATE task_execution_intents SET continuation_applied=1
                 WHERE event_id=? AND continuation_applied=0""",
            (event_id,),
        )
        if cursor.rowcount not in {0, 1}:
            raise ConflictError("task continuation application state is invalid")

    def _apply_continuation_once(
        self,
        connection: Any,
        *,
        event_id: str,
        now: int,
    ) -> None:
        row = connection.execute(
            """SELECT continuation_encrypted,continuation_digest,continuation_applied
                 FROM task_execution_intents WHERE event_id=?""",
            (event_id,),
        ).fetchone()
        if row is None:
            raise ConflictError("released task lacks continuation state")
        if bool(row["continuation_applied"]):
            return
        try:
            continuation = self.store.cipher.decrypt_json(
                row["continuation_encrypted"], purpose=f"task-intent-continuation:{event_id}"
            )
        except Exception as exc:
            raise AuthorizationError("released task continuation is unavailable") from exc
        if not isinstance(continuation, dict) or canonical_digest(continuation) != row["continuation_digest"]:
            raise AuthorizationError("released task continuation binding is invalid")
        if continuation:
            if self.release_callback is None:
                raise AuthorizationError("task conflict release lacks its composed continuation handler")
            self.release_callback(connection, event_id, continuation, now)
        cursor = connection.execute(
            """UPDATE task_execution_intents SET continuation_applied=1
                 WHERE event_id=? AND continuation_applied=0""",
            (event_id,),
        )
        if cursor.rowcount != 1:
            raise ConflictError("task conflict continuation raced with another release")

    @staticmethod
    def _require_current_owner(
        connection: Any,
        *,
        actor: VerifiedActor,
        conflict: Any,
        when: datetime,
    ) -> tuple[Any, Any]:
        if actor.kind not in {
            ActorKind.VERIFIED_HUMAN_HARNESS,
            ActorKind.HOST_GUEST_HARNESS,
        }:
            raise AuthorizationError("task conflict decision requires a human or guest owner")
        domain = TaskConflictService._current_domain(connection, str(conflict["domain_id"]))
        harness, authority_id = TaskConflictService._recipient_owner(
            connection, str(conflict["recipient_harness_id"])
        )
        if (
            actor.domain_id != conflict["domain_id"]
            or actor.positive_authority_id != authority_id
            or conflict["recipient_authority_id"] != authority_id
        ):
            raise AuthorizationError("task conflict decision is unavailable")
        denial, _current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(domain["policy_revision"]),
            when=when,
        )
        if denial is not None:
            raise AuthorizationError("task conflict owner is not current")
        return domain, harness

    @staticmethod
    def _pairwise_compatible(intents: list[TaskExecutionIntent]) -> bool:
        return all(not conflict_resource_keys(left, right) for left, right in combinations(intents, 2))

    def _release_if_unblocked(
        self,
        connection: Any,
        *,
        event_id: str,
        recipient_harness_id: str,
        owner: dict[str, Any],
        detail: dict[str, Any],
        now: int,
    ) -> None:
        intent = connection.execute(
            "SELECT state FROM task_execution_intents WHERE event_id=?",
            (event_id,),
        ).fetchone()
        if intent is None:
            raise ConflictError("released task lacks an exact execution intent")
        if intent["state"] == "rejected":
            return
        other_pending = connection.execute(
            """SELECT 1 FROM task_conflict_memberships AS m
                 JOIN task_conflicts AS c ON c.conflict_id=m.conflict_id
                WHERE m.event_id=? AND m.member_state='pending' AND c.state='pending'
                LIMIT 1""",
            (event_id,),
        ).fetchone()
        if other_pending is not None:
            return
        self._apply_continuation_once(connection, event_id=event_id, now=now)
        if intent["state"] != "released":
            cursor = connection.execute(
                """UPDATE task_execution_intents
                      SET state='released',state_revision=state_revision+1,updated_at=?
                    WHERE event_id=? AND state<>'rejected'""",
                (now, event_id),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task release raced with terminal rejection")
        self._record_fact(
            connection,
            event_id=event_id,
            recipient_harness_id=recipient_harness_id,
            proposed=DeliveryFact.QUEUED,
            owner=owner,
            detail=detail,
            now=now,
        )

    def _propagate_terminal_rejection(
        self,
        connection: Any,
        *,
        event_id: str,
        actor: VerifiedActor,
        decided_conflict_id: str,
        now: int,
    ) -> None:
        """Fence every other conflict containing a now-terminal task."""

        lock_suffix = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
        conflict_ids = [
            str(row["conflict_id"])
            for row in connection.execute(
                """SELECT m.conflict_id FROM task_conflict_memberships AS m
                     JOIN task_conflicts AS c ON c.conflict_id=m.conflict_id
                    WHERE m.event_id=? AND m.member_state='pending'
                      AND c.state='pending' AND m.conflict_id<>?
                    ORDER BY m.conflict_id""",
                (event_id, decided_conflict_id),
            ).fetchall()
        ]
        for conflict_id in conflict_ids:
            conflict = connection.execute(
                "SELECT * FROM task_conflicts WHERE conflict_id=?" + lock_suffix,
                (conflict_id,),
            ).fetchone()
            if conflict is None or conflict["state"] != "pending":
                continue
            current_revision = int(conflict["revision"])
            cursor = connection.execute(
                """UPDATE task_conflict_memberships
                      SET member_state='rejected',decided_at=?
                    WHERE conflict_id=? AND event_id=? AND member_state='pending'""",
                (now, conflict_id, event_id),
            )
            if cursor.rowcount != 1:
                raise ConflictError("terminal task conflict membership raced with adjudication")
            remaining = connection.execute(
                """SELECT m.event_id,i.intent_json
                     FROM task_conflict_memberships AS m
                     JOIN task_execution_intents AS i ON i.event_id=m.event_id
                    WHERE m.conflict_id=? AND m.member_state='pending'
                      AND i.state='conflict_pending'
                    ORDER BY m.event_id""",
                (conflict_id,),
            ).fetchall()
            auto_resolve = len(remaining) < 2 or self._pairwise_compatible(
                [self._intent_from_row(row) for row in remaining]
            )
            next_revision = current_revision + 1
            decision_digest = canonical_digest(
                {
                    "schema": "agentnet.task-conflict-auto-settlement.v1",
                    "conflict_id": conflict_id,
                    "expected_revision": current_revision,
                    "terminal_event_id": event_id,
                    "remaining_event_ids": [str(row["event_id"]) for row in remaining],
                    "state": "resolved" if auto_resolve else "pending",
                }
            )
            if auto_resolve:
                cursor = connection.execute(
                    """UPDATE task_conflicts
                          SET state='resolved',revision=revision+1,updated_at=?,decided_at=?,
                              decision_actor_json=?,decision_digest=?,reason_code='member_terminal'
                        WHERE conflict_id=? AND state='pending' AND revision=?""",
                    (
                        now,
                        now,
                        canonical_json(actor.audit_view()).decode("utf-8"),
                        decision_digest,
                        conflict_id,
                        current_revision,
                    ),
                )
            else:
                cursor = connection.execute(
                    """UPDATE task_conflicts SET revision=revision+1,updated_at=?
                        WHERE conflict_id=? AND state='pending' AND revision=?""",
                    (now, conflict_id, current_revision),
                )
            if cursor.rowcount != 1:
                raise ConflictError("terminal task conflict propagation raced with adjudication")
            if auto_resolve:
                for row in remaining:
                    member_event_id = str(row["event_id"])
                    connection.execute(
                        """UPDATE task_conflict_memberships
                              SET member_state='released',decided_at=?
                            WHERE conflict_id=? AND event_id=? AND member_state='pending'""",
                        (now, conflict_id, member_event_id),
                    )
                    self._release_if_unblocked(
                        connection,
                        event_id=member_event_id,
                        recipient_harness_id=str(conflict["recipient_harness_id"]),
                        owner=actor.audit_view(),
                        detail={
                            "conflict_id": conflict_id,
                            "conflict_revision": next_revision,
                            "decision_digest": decision_digest,
                            "automatic_settlement": True,
                        },
                        now=now,
                    )
            self.store.append_audit(
                connection,
                {
                    "action": (
                        "task_conflict.auto_resolved"
                        if auto_resolve
                        else "task_conflict.member_terminal"
                    ),
                    "actor": actor.audit_view(),
                    "conflict_id": conflict_id,
                    "conflict_revision": next_revision,
                    "decision_digest": decision_digest,
                    "terminal_event_id": event_id,
                    "released_event_ids": (
                        [str(row["event_id"]) for row in remaining] if auto_resolve else []
                    ),
                    "authority_effect": "custody_only",
                },
            )

    def adjudicate(
        self,
        *,
        actor: VerifiedActor,
        decision: TaskConflictAdjudication,
        when: datetime | None = None,
    ) -> TaskConflictOutcome:
        when = when or datetime.now(UTC)
        now = epoch_seconds(when)
        with self.store.transaction() as connection:
            lock_suffix = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
            conflict = connection.execute(
                "SELECT * FROM task_conflicts WHERE conflict_id=?" + lock_suffix,
                (decision.conflict_id,),
            ).fetchone()
            if conflict is None:
                raise AuthorizationError("task conflict decision is unavailable")
            if conflict["state"] != "pending" or int(conflict["revision"]) != decision.expected_revision:
                raise ConflictError("task conflict is no longer pending at the expected revision")
            domain, recipient_harness = self._require_current_owner(
                connection, actor=actor, conflict=conflict, when=when
            )
            if (
                decision.expected_policy_revision != int(conflict["policy_revision"])
                or decision.expected_domain_revocation_epoch
                != int(conflict["domain_revocation_epoch"])
                or decision.expected_recipient_credential_epoch
                != int(conflict["recipient_credential_epoch"])
                or int(domain["policy_revision"]) != int(conflict["policy_revision"])
                or int(domain["revocation_epoch"]) != int(conflict["domain_revocation_epoch"])
                or int(recipient_harness["credential_epoch"])
                != int(conflict["recipient_credential_epoch"])
            ):
                raise ConflictError("task conflict authority epochs drifted; refresh before deciding")
            member_rows = connection.execute(
                """SELECT m.event_id,m.member_state,i.intent_json,i.state AS intent_state
                     FROM task_conflict_memberships AS m
                     JOIN task_execution_intents AS i ON i.event_id=m.event_id
                    WHERE m.conflict_id=? ORDER BY m.event_id""",
                (decision.conflict_id,),
            ).fetchall()
            member_ids = frozenset(str(row["event_id"]) for row in member_rows)
            if (
                member_ids != decision.expected_member_event_ids
                or any(
                    row["member_state"] != "pending"
                    or row["intent_state"] != "conflict_pending"
                    for row in member_rows
                )
            ):
                raise ConflictError("task conflict decision does not bind every current pending member")
            releases = [
                self._intent_from_row(row)
                for row in member_rows
                if row["event_id"] in decision.release_event_ids
            ]
            if not self._pairwise_compatible(releases):
                raise ValidationError("task conflict cannot release mutually incompatible intents")
            external_rows = connection.execute(
                """SELECT i.* FROM task_execution_intents AS i
                     JOIN recipients AS r ON r.event_id=i.event_id
                      AND r.recipient_id=i.recipient_harness_id
                    WHERE i.domain_id=? AND i.recipient_harness_id=?
                      AND i.event_id NOT IN (
                          SELECT event_id FROM task_conflict_memberships WHERE conflict_id=?
                      )
                      AND i.state IN ('active','released') AND i.deadline>?
                      AND r.current_fact NOT IN (
                          'rejected_before_accept','remote_rejected','completed','failed_terminal',
                          'expired','canceled','too_late','compensated','dead_lettered'
                      )""",
                (
                    conflict["domain_id"],
                    conflict["recipient_harness_id"],
                    decision.conflict_id,
                    now,
                ),
            ).fetchall()
            for released_intent in releases:
                if any(
                    conflict_resource_keys(released_intent, self._intent_from_row(row))
                    for row in external_rows
                ):
                    raise ConflictError("released task would conflict with another active exact intent")
            decision_digest = canonical_digest(
                {
                    "schema": "agentnet.task-conflict-decision.v1",
                    "actor": actor.audit_view(),
                    "decision": decision.model_dump(mode="json"),
                    "recipient_harness_id": conflict["recipient_harness_id"],
                    "recipient_authority_id": conflict["recipient_authority_id"],
                    "resource_key": conflict["resource_key"],
                }
            )
            cursor = connection.execute(
                """UPDATE task_conflicts
                      SET state='resolved',revision=revision+1,updated_at=?,decided_at=?,
                          decision_actor_json=?,decision_digest=?,reason_code=?
                    WHERE conflict_id=? AND state='pending' AND revision=?""",
                (
                    now,
                    now,
                    canonical_json(actor.audit_view()).decode("utf-8"),
                    decision_digest,
                    decision.reason_code,
                    decision.conflict_id,
                    decision.expected_revision,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("task conflict decision raced with another mutation")
            next_revision = decision.expected_revision + 1
            for event_id in sorted(decision.reject_event_ids):
                connection.execute(
                    """UPDATE task_conflict_memberships
                          SET member_state='rejected',decided_at=?
                        WHERE conflict_id=? AND event_id=? AND member_state='pending'""",
                    (now, decision.conflict_id, event_id),
                )
                connection.execute(
                    """UPDATE task_execution_intents
                          SET state='rejected',state_revision=state_revision+1,updated_at=?
                        WHERE event_id=?""",
                    (now, event_id),
                )
                self._record_fact(
                    connection,
                    event_id=event_id,
                    recipient_harness_id=str(conflict["recipient_harness_id"]),
                    proposed=DeliveryFact.REJECTED_BEFORE_ACCEPT,
                    owner=actor.audit_view(),
                    detail={
                        "conflict_id": decision.conflict_id,
                        "conflict_revision": next_revision,
                        "decision_digest": decision_digest,
                    },
                    now=now,
                )
                self._propagate_terminal_rejection(
                    connection,
                    event_id=event_id,
                    actor=actor,
                    decided_conflict_id=decision.conflict_id,
                    now=now,
                )
            for event_id in sorted(decision.release_event_ids):
                connection.execute(
                    """UPDATE task_conflict_memberships
                          SET member_state='released',decided_at=?
                        WHERE conflict_id=? AND event_id=? AND member_state='pending'""",
                    (now, decision.conflict_id, event_id),
                )
                self._release_if_unblocked(
                    connection,
                    event_id=event_id,
                    recipient_harness_id=str(conflict["recipient_harness_id"]),
                    owner=actor.audit_view(),
                    detail={
                        "conflict_id": decision.conflict_id,
                        "conflict_revision": next_revision,
                        "decision_digest": decision_digest,
                    },
                    now=now,
                )
            self.store.append_audit(
                connection,
                {
                    "action": "task_conflict.resolved",
                    "actor": actor.audit_view(),
                    "conflict_id": decision.conflict_id,
                    "conflict_revision": next_revision,
                    "decision_digest": decision_digest,
                    "reason_code": decision.reason_code,
                    "released_event_ids": sorted(decision.release_event_ids),
                    "rejected_event_ids": sorted(decision.reject_event_ids),
                    "data_access_authorized": False,
                    "semantic_processing_authorized": False,
                    "tool_authorized": False,
                    "effect_authorized": False,
                },
            )
            return TaskConflictOutcome(
                conflict_id=decision.conflict_id,
                revision=next_revision,
                decision_digest=decision_digest,
                released_event_ids=tuple(sorted(decision.release_event_ids)),
                rejected_event_ids=tuple(sorted(decision.reject_event_ids)),
            )

    def pending_for_owner(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
        when: datetime | None = None,
    ) -> list[dict[str, Any]]:
        if not 1 <= limit <= 1000:
            raise ValidationError("task conflict limit is invalid")
        if actor.positive_authority_id is None:
            raise AuthorizationError("task conflict listing requires a positive-authority owner")
        when = when or datetime.now(UTC)
        with self.store.transaction() as connection:
            domain = self._current_domain(connection, actor.domain_id)
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=when,
            )
            if denial is not None:
                raise AuthorizationError("task conflict owner is not current")
            rows = connection.execute(
                """SELECT * FROM task_conflicts
                    WHERE domain_id=? AND recipient_authority_id=? AND state='pending'
                    ORDER BY created_at,conflict_id LIMIT ?""",
                (actor.domain_id, actor.positive_authority_id, limit),
            ).fetchall()
            results: list[dict[str, Any]] = []
            for row in rows:
                harness, authority_id = self._recipient_owner(
                    connection, str(row["recipient_harness_id"])
                )
                if authority_id != actor.positive_authority_id:
                    continue
                revision = int(row["revision"])
                if (
                    int(row["policy_revision"]) != int(domain["policy_revision"])
                    or int(row["domain_revocation_epoch"]) != int(domain["revocation_epoch"])
                    or int(row["recipient_credential_epoch"]) != int(harness["credential_epoch"])
                ):
                    cursor = connection.execute(
                        """UPDATE task_conflicts
                              SET policy_revision=?,domain_revocation_epoch=?,
                                  recipient_credential_epoch=?,revision=revision+1,updated_at=?
                            WHERE conflict_id=? AND state='pending' AND revision=?""",
                        (
                            int(domain["policy_revision"]),
                            int(domain["revocation_epoch"]),
                            int(harness["credential_epoch"]),
                            epoch_seconds(when),
                            row["conflict_id"],
                            revision,
                        ),
                    )
                    if cursor.rowcount != 1:
                        raise ConflictError("task conflict refresh raced with another mutation")
                    revision += 1
                members = connection.execute(
                    """SELECT m.event_id,i.sender_harness_id,i.sender_authority_id,
                              i.authority_basis,i.relationship_id,i.relationship_revision,
                              i.intent_json,i.intent_digest,i.deadline
                         FROM task_conflict_memberships AS m
                         JOIN task_execution_intents AS i ON i.event_id=m.event_id
                        WHERE m.conflict_id=? AND m.member_state='pending'
                          AND i.state='conflict_pending'
                        ORDER BY m.event_id""",
                    (row["conflict_id"],),
                ).fetchall()
                results.append(
                    {
                        "conflict_id": row["conflict_id"],
                        "revision": revision,
                        "policy_revision": int(domain["policy_revision"]),
                        "domain_revocation_epoch": int(domain["revocation_epoch"]),
                        "recipient_credential_epoch": int(harness["credential_epoch"]),
                        "recipient_harness_id": row["recipient_harness_id"],
                        "resource_key": row["resource_key"],
                        "members": [
                            {
                                "event_id": member["event_id"],
                                "sender_harness_id": member["sender_harness_id"],
                                "sender_authority_id": member["sender_authority_id"],
                                "authority_basis": member["authority_basis"],
                                "relationship_id": member["relationship_id"],
                                "relationship_revision": int(member["relationship_revision"]),
                                "intent": json.loads(member["intent_json"]),
                                "intent_digest": member["intent_digest"],
                                "deadline": datetime.fromtimestamp(int(member["deadline"]), UTC).isoformat(),
                            }
                            for member in members
                        ],
                    }
                )
            return results


__all__ = [
    "TaskAccessMode",
    "TaskConflictAdjudication",
    "TaskConflictAdmission",
    "TaskConflictOutcome",
    "TaskConflictService",
    "TaskExecutionIntent",
    "TaskExclusivity",
    "TaskResourceIntent",
    "conflict_resource_keys",
]
