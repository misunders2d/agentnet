"""Authenticated autonomous supervisor routes and durable execution receipts.

The server never trusts payload fields as identity or as execution authority.
Every operation is rebound to the transport-authenticated recipient, immutable
mailbox metadata, a current recipient-owned task grant, and the current domain
and credential epochs.
"""

from __future__ import annotations

import json
import time
from collections.abc import Awaitable, Callable, Mapping
from datetime import UTC, datetime
from typing import TYPE_CHECKING, Any
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    validate_actor_state,
)
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import Classification, DeliveryFact, EventType, TaskGrant
from agentnet.provenance import (
    ProvenanceObjectType,
    ProvenanceReferenceV1,
    TransformationKind,
    TransformationStep,
)
from agentnet.security.signatures import canonical_digest, canonical_json

if TYPE_CHECKING:
    from agentnet.bindings.composition import LocalBindingService
    from agentnet.core.app import CommunicationCore


BodyAndActor = Callable[[Request, "CommunicationCore"], Awaitable[tuple[bytes, VerifiedActor]]]


class EligibilityBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    event_id: str = Field(min_length=1, max_length=256)
    cursor: int = Field(ge=1)
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")


class BackgroundAuthorizationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    decision_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    event_id: str = Field(min_length=1, max_length=256)
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    event_type: str = Field(min_length=1, max_length=64)
    classification: str = Field(pattern=r"^C[0-3]$")
    policy_revision: int = Field(ge=1)
    expires_at: int = Field(gt=0)
    task_grant_id: str = Field(min_length=1, max_length=256)

class CustodyBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    authorization: BackgroundAuthorizationBody
    cursor: int = Field(ge=1)
    local_queue_id: str = Field(min_length=1, max_length=256)


class ResultBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    authorization: BackgroundAuthorizationBody
    source_queue_id: str = Field(min_length=1, max_length=256)
    native_result: dict[str, Any]


class LocalBindingChildBody(BaseModel):
    """Exact post-spawn child identity; corporate identity comes from the proof."""

    model_config = ConfigDict(extra="forbid")

    pid: int = Field(gt=0)
    session_id: str = Field(min_length=16, max_length=256, pattern=r"^[A-Za-z0-9_-]+$")
    process_start_time: str = Field(pattern=r"^[0-9]{1,128}$")
    process_measurement: str = Field(pattern=r"^sha256:[0-9a-f]{64}$")


class SupervisorExecutionService:
    """Atomic recipient-owned eligibility, custody, and result lifecycle."""

    AUTHORIZATION_TTL_SECONDS = 300
    ACTION = "task.process"
    INPUT_SOURCE = "mailbox"
    OUTPUT_SINK = "receipt"

    def __init__(self, core: "CommunicationCore") -> None:
        self.core = core
        self.store = core.store

    @staticmethod
    def _authorization(row: Mapping[str, Any]) -> dict[str, Any]:
        return {
            "decision_id": str(row["policy_decision_id"]),
            "harness_id": str(row["recipient_harness_id"]),
            "event_id": str(row["event_id"]),
            "envelope_digest": str(row["envelope_digest"]),
            "event_type": str(row["event_type"]),
            "classification": str(row["classification"]),
            "policy_revision": int(row["policy_revision"]),
            "expires_at": int(row["authorization_expires_at"]),
            "task_grant_id": str(row["task_grant_id"]),
        }

    @staticmethod
    def _event_row(connection: Any, *, actor: VerifiedActor, body: EligibilityBody) -> Any:
        if actor.harness_id is None:
            raise AuthorizationError("supervisor execution requires exact recipient attribution")
        row = connection.execute(
            """
            SELECT e.event_id,e.domain_id,e.event_type,e.classification,e.payload_digest,
                   e.envelope_digest,e.policy_revision,e.delivery_expires_at,e.effect_deadline,
                   e.retention_delete_at,r.cursor,r.current_fact,h.credential_epoch,
                   d.revocation_epoch,d.status AS domain_status
              FROM events AS e
              JOIN recipients AS r ON r.event_id=e.event_id
              JOIN harnesses AS h ON h.harness_id=r.recipient_id
              JOIN domains AS d ON d.domain_id=e.domain_id
             WHERE e.event_id=? AND r.recipient_id=?
            """,
            (body.event_id, actor.harness_id),
        ).fetchone()
        if row is None:
            raise AuthorizationError("supervisor execution is not visible")
        if (
            str(row["domain_id"]) != actor.domain_id
            or int(row["cursor"]) != body.cursor
            or str(row["envelope_digest"]) != body.envelope_digest
        ):
            raise AuthorizationError("supervisor execution binding is not visible")
        return row

    @staticmethod
    def _grant_matches(
        grant: TaskGrant,
        *,
        actor: VerifiedActor,
        event_id: str,
        classification: Classification,
    ) -> bool:
        return (
            grant.domain_id == actor.domain_id
            and grant.principal_id == actor.positive_authority_id
            and grant.harness_id == actor.harness_id
            and SupervisorExecutionService.ACTION in grant.actions
            and f"event:{event_id}" in grant.resources
            and SupervisorExecutionService.INPUT_SOURCE in grant.input_sources
            and SupervisorExecutionService.OUTPUT_SINK in grant.output_sinks
            and classification in grant.data_classes
        )

    def _select_grant(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        event_id: str,
        classification: Classification,
        now: int,
    ) -> TaskGrant:
        rows = connection.execute(
            """
            SELECT * FROM task_grants
             WHERE domain_id=? AND harness_id=? AND principal_id=?
               AND revoked_at IS NULL AND expires_at>? AND uses<max_uses
             ORDER BY expires_at,grant_id
            """,
            (actor.domain_id, actor.harness_id, actor.positive_authority_id, now),
        ).fetchall()
        for row in rows:
            try:
                grant = TaskGrant.model_validate(json.loads(row["grant_json"]))
            except Exception:
                continue
            if self._grant_matches(
                grant,
                actor=actor,
                event_id=event_id,
                classification=classification,
            ):
                return grant
        raise AuthorizationError("no current exact execution grant is available")

    @staticmethod
    def _require_current_execution(
        connection: Any,
        *,
        actor: VerifiedActor,
        row: Mapping[str, Any],
        now: int,
        require_unexpired_authorization: bool,
    ) -> None:
        if actor.harness_id != row["recipient_harness_id"]:
            raise AuthorizationError("supervisor execution is not visible")
        denial, revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(row["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        domain = connection.execute(
            "SELECT revocation_epoch FROM domains WHERE domain_id=?", (actor.domain_id,)
        ).fetchone()
        grant_row = connection.execute(
            "SELECT * FROM task_grants WHERE grant_id=?",
            (row["task_grant_id"],),
        ).fetchone()
        event = connection.execute(
            """
            SELECT e.envelope_digest,e.payload_digest,e.policy_revision,r.recipient_id
              FROM events AS e JOIN recipients AS r ON r.event_id=e.event_id
             WHERE e.event_id=? AND r.recipient_id=?
            """,
            (row["event_id"], row["recipient_harness_id"]),
        ).fetchone()
        binding_row = connection.execute(
            "SELECT value FROM metadata WHERE key=?",
            (f"authority-binding:task-grant:{row['task_grant_id']}",),
        ).fetchone()
        try:
            binding = json.loads(binding_row["value"]) if binding_row is not None else None
            grant = TaskGrant.model_validate(json.loads(grant_row["grant_json"])) if grant_row is not None else None
            binding_valid = (
                isinstance(binding, dict)
                and binding.get("schema") == "agentnet.task-grant.authority-binding.v1"
                and binding.get("domain_id") == actor.domain_id
                and binding.get("principal_id") == actor.positive_authority_id
                and binding.get("harness_id") == actor.harness_id
                and int(binding.get("policy_revision", 0)) == int(row["policy_revision"])
                and int(binding.get("harness_credential_epoch", 0)) == actor.credential_epoch
            )
        except Exception:
            binding = None
            grant = None
            binding_valid = False
        classification = Classification(str(row["classification"]))
        if (
            denial is not None
            or revision != int(row["policy_revision"])
            or actor.credential_epoch != int(row["recipient_credential_epoch"])
            or domain is None
            or int(domain["revocation_epoch"]) != int(row["domain_revocation_epoch"])
            or event is None
            or event["envelope_digest"] != row["envelope_digest"]
            or event["payload_digest"] != row["payload_digest"]
            or int(event["policy_revision"]) != int(row["policy_revision"])
            or grant_row is None
            or grant is None
            or grant_row["revoked_at"] is not None
            or int(grant_row["expires_at"]) <= now
            or not SupervisorExecutionService._grant_matches(
                grant,
                actor=actor,
                event_id=str(row["event_id"]),
                classification=classification,
            )
            or not binding_valid
            or (
                require_unexpired_authorization
                and int(row["authorization_expires_at"]) <= now
            )
        ):
            raise AuthorizationError("supervisor execution authority is no longer current")

    @staticmethod
    def _execution_row(connection: Any, *, event_id: str, recipient_id: str) -> Any:
        return connection.execute(
            "SELECT * FROM supervisor_executions WHERE event_id=? AND recipient_harness_id=?",
            (event_id, recipient_id),
        ).fetchone()

    def _parent_event_provenance(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        row: Mapping[str, Any],
    ):
        link = connection.execute(
            "SELECT * FROM event_provenance WHERE event_id=?",
            (row["event_id"],),
        ).fetchone()
        if link is None:
            raise ConflictError("supervisor input lacks mandatory event provenance")
        try:
            raw_reference = str(link["reference_json"])
            reference = ProvenanceReferenceV1.model_validate_json(
                raw_reference,
                strict=True,
            )
            if (
                str(link["provenance_digest"]) != reference.provenance_digest
                or str(link["object_type"]) != ProvenanceObjectType.TASK.value
                or canonical_json(reference.model_dump(mode="json")).decode("utf-8")
                != raw_reference
            ):
                raise ValueError("event provenance link fields disagree")
        except Exception as exc:
            raise ConflictError("supervisor input provenance link is invalid") from exc
        return self.core.provenance.require_reference_in_transaction(
            connection,
            reference,
            expected_domain_id=actor.domain_id,
            expected_content_digest=str(row["payload_digest"]),
            expected_object_type=ProvenanceObjectType.TASK,
            expected_classification=Classification(str(row["classification"])),
            required_sinks=(str(actor.harness_id),),
            expected_policy_revision=int(row["policy_revision"]),
        )

    def _result_provenance(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        row: Mapping[str, Any],
    ) -> ProvenanceReferenceV1:
        try:
            raw_reference = str(row["result_provenance_json"])
            reference = ProvenanceReferenceV1.model_validate_json(
                raw_reference,
                strict=True,
            )
            if (
                str(row["result_provenance_digest"]) != reference.provenance_digest
                or canonical_json(reference.model_dump(mode="json")).decode("utf-8")
                != raw_reference
            ):
                raise ValueError("result provenance fields disagree")
        except Exception as exc:
            raise ConflictError("supervisor result provenance link is invalid") from exc
        self.core.provenance.require_reference_in_transaction(
            connection,
            reference,
            expected_domain_id=actor.domain_id,
            expected_content_digest=str(row["result_digest"]),
            expected_object_type=ProvenanceObjectType.PARSER_OUTPUT,
            expected_classification=Classification(str(row["classification"])),
            required_sinks=(str(actor.harness_id),),
            expected_policy_revision=int(row["policy_revision"]),
        )
        return reference

    def authorize(self, *, actor: VerifiedActor, body: EligibilityBody) -> dict[str, Any]:
        now = int(time.time())
        self.core.outage.require_privileged()
        if actor.harness_id is None:
            raise AuthorizationError("supervisor execution requires exact recipient attribution")
        with self.store.transaction() as connection:
            event = self._event_row(connection, actor=actor, body=body)
            existing = self._execution_row(
                connection, event_id=body.event_id, recipient_id=actor.harness_id
            )
            if existing is not None:
                if (
                    existing["envelope_digest"] != event["envelope_digest"]
                    or existing["payload_digest"] != event["payload_digest"]
                    or int(existing["policy_revision"]) != int(event["policy_revision"])
                ):
                    raise ConflictError("supervisor execution immutable binding changed")
                self._require_current_execution(
                    connection,
                    actor=actor,
                    row=existing,
                    now=now,
                    require_unexpired_authorization=True,
                )
                return self._authorization(existing)

            if event["event_type"] != EventType.TASK_ASSIGNMENT.value:
                raise AuthorizationError("only typed task assignments may enter a semantic worker")
            if event["current_fact"] not in {
                DeliveryFact.ACCEPTED_LOCAL.value,
                DeliveryFact.ACCEPTED_DURABLE.value,
                DeliveryFact.ACCEPTED_QUEUED.value,
                DeliveryFact.QUEUED.value,
            }:
                raise AuthorizationError("mailbox custody is not eligible for background execution")
            for boundary in (
                event["delivery_expires_at"],
                event["effect_deadline"],
                event["retention_delete_at"],
            ):
                if boundary is not None and int(boundary) <= now:
                    raise AuthorizationError("mailbox execution boundary has expired")
            classification = Classification(str(event["classification"]))
            grant = self._select_grant(
                connection,
                actor=actor,
                event_id=body.event_id,
                classification=classification,
                now=now,
            )
            decision = self.core.policy._decide_in_transaction(
                connection,
                AuthorizationRequest(
                    actor=actor,
                    action=self.ACTION,
                    resource=f"event:{body.event_id}",
                    operation_class=OperationClass.PROTECTED_READ,
                    classification=classification,
                    policy_revision=int(event["policy_revision"]),
                    grant_use=GrantUse(
                        grant_id=grant.grant_id,
                        action=self.ACTION,
                        resource=f"event:{body.event_id}",
                        input_source=self.INPUT_SOURCE,
                        output_sink=self.OUTPUT_SINK,
                        data_class=classification,
                    ),
                    context={
                        "schema": "agentnet.supervisor.eligibility.v1",
                        "cursor": int(event["cursor"]),
                        "envelope_digest": str(event["envelope_digest"]),
                        "event_id": body.event_id,
                        "event_type": str(event["event_type"]),
                        "payload_digest": str(event["payload_digest"]),
                        "recipient_harness_id": actor.harness_id,
                    },
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            if not decision.allowed:
                raise AuthorizationError(decision.reason)
            domain = connection.execute(
                "SELECT revocation_epoch FROM domains WHERE domain_id=?", (actor.domain_id,)
            ).fetchone()
            if domain is None:
                raise AuthorizationError("supervisor execution domain is unavailable")
            expires_at = min(now + self.AUTHORIZATION_TTL_SECONDS, int(grant.expires_at.timestamp()))
            for boundary in (event["delivery_expires_at"], event["effect_deadline"]):
                if boundary is not None:
                    expires_at = min(expires_at, int(boundary))
            authorization = {
                "decision_id": decision.decision_id,
                "harness_id": actor.harness_id,
                "event_id": body.event_id,
                "envelope_digest": str(event["envelope_digest"]),
                "event_type": str(event["event_type"]),
                "classification": classification.value,
                "policy_revision": decision.policy_revision,
                "expires_at": expires_at,
                "task_grant_id": grant.grant_id,
            }
            if expires_at <= now:
                raise AuthorizationError("supervisor authorization has no executable lifetime")
            authorization_digest = canonical_digest(authorization)
            connection.execute(
                """
                INSERT INTO supervisor_executions(
                    event_id,recipient_harness_id,envelope_digest,payload_digest,event_type,
                    classification,task_grant_id,policy_decision_id,policy_revision,
                    recipient_credential_epoch,domain_revocation_epoch,authorization_digest,
                    authorization_expires_at,state,created_at,updated_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'eligible',?,?)
                """,
                (
                    body.event_id,
                    actor.harness_id,
                    event["envelope_digest"],
                    event["payload_digest"],
                    event["event_type"],
                    classification.value,
                    grant.grant_id,
                    decision.decision_id,
                    decision.policy_revision,
                    actor.credential_epoch,
                    int(domain["revocation_epoch"]),
                    authorization_digest,
                    expires_at,
                    now,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "supervisor.background.authorized",
                    "authorization_digest": authorization_digest,
                    "event_id": body.event_id,
                    "policy_decision_id": decision.decision_id,
                    "recipient_harness_id": actor.harness_id,
                    "task_grant_id": grant.grant_id,
                },
            )
            return authorization

    @staticmethod
    def _require_authorization_binding(
        row: Mapping[str, Any],
        authorization: BackgroundAuthorizationBody,
    ) -> None:
        expected = SupervisorExecutionService._authorization(row)
        if authorization.model_dump(mode="json") != expected:
            raise AuthorizationError("supervisor authorization binding is not visible")
        if canonical_digest(authorization.model_dump(mode="json")) != row["authorization_digest"]:
            raise AuthorizationError("supervisor authorization digest changed")

    def acknowledge_custody(
        self,
        *,
        actor: VerifiedActor,
        body: CustodyBody,
    ) -> dict[str, Any]:
        now = int(time.time())
        authorization = body.authorization
        if actor.harness_id is None:
            raise AuthorizationError("supervisor custody requires exact recipient attribution")
        assertion = {
            "schema": "agentnet.supervisor.local-custody.v1",
            "authorization": authorization.model_dump(mode="json"),
            "cursor": body.cursor,
            "local_queue_id": body.local_queue_id,
        }
        assertion_digest = canonical_digest(assertion)
        with self.store.transaction() as connection:
            row = self._execution_row(
                connection,
                event_id=authorization.event_id,
                recipient_id=actor.harness_id,
            )
            if row is None:
                raise AuthorizationError("supervisor execution is not visible")
            self._require_current_execution(
                connection,
                actor=actor,
                row=row,
                now=now,
                require_unexpired_authorization=True,
            )
            self._require_authorization_binding(row, authorization)
            recipient = connection.execute(
                "SELECT cursor FROM recipients WHERE event_id=? AND recipient_id=?",
                (authorization.event_id, actor.harness_id),
            ).fetchone()
            if recipient is None or int(recipient["cursor"]) != body.cursor:
                raise AuthorizationError("local custody cursor changed")
            if row["state"] in {"local_custody", "result_uploaded"}:
                if (
                    row["custody_assertion_digest"] != assertion_digest
                    or row["local_queue_id"] != body.local_queue_id
                ):
                    raise ConflictError("local custody was already asserted with different bytes")
                return {
                    "custody_receipt_id": row["custody_receipt_id"],
                    "duplicate": True,
                    "event_id": authorization.event_id,
                    "state": row["state"],
                }
            custody_receipt_id = str(uuid4())
            delivery_receipt = self.core.mailboxes._transition_in_transaction(
                connection,
                event_id=authorization.event_id,
                recipient_id=actor.harness_id,
                proposed=DeliveryFact.RECIPIENT_COMMITTED,
                owner_actor=actor,
                detail={
                    "authorization_digest": row["authorization_digest"],
                    "cursor": body.cursor,
                    "local_queue_id": body.local_queue_id,
                    "schema": "agentnet.supervisor.local-custody.v1",
                },
                now=now,
            )
            cursor = connection.execute(
                """
                UPDATE supervisor_executions
                   SET state='local_custody',custody_receipt_id=?,local_queue_id=?,
                       custody_assertion_digest=?,custody_recorded_at=?,updated_at=?
                 WHERE event_id=? AND recipient_harness_id=? AND state='eligible'
                """,
                (
                    custody_receipt_id,
                    body.local_queue_id,
                    assertion_digest,
                    now,
                    now,
                    authorization.event_id,
                    actor.harness_id,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("supervisor custody state changed concurrently")
            self.store.append_audit(
                connection,
                {
                    "action": "supervisor.local_custody.acknowledged",
                    "actor": actor.audit_view(),
                    "assertion_digest": assertion_digest,
                    "custody_receipt_id": custody_receipt_id,
                    "delivery_receipt_id": delivery_receipt["receipt_id"],
                    "event_id": authorization.event_id,
                },
            )
            return {
                "custody_receipt_id": custody_receipt_id,
                "duplicate": False,
                "event_id": authorization.event_id,
                "state": "local_custody",
            }

    def upload_result(self, *, actor: VerifiedActor, body: ResultBody) -> dict[str, Any]:
        now = int(time.time())
        authorization = body.authorization
        if actor.harness_id is None:
            raise AuthorizationError("supervisor result requires exact recipient attribution")
        result_value = body.model_dump(mode="json")
        result_digest = canonical_digest(result_value)
        with self.store.transaction() as connection:
            row = self._execution_row(
                connection,
                event_id=authorization.event_id,
                recipient_id=actor.harness_id,
            )
            if row is None:
                raise AuthorizationError("supervisor execution is not visible")
            self._require_current_execution(
                connection,
                actor=actor,
                row=row,
                now=now,
                require_unexpired_authorization=False,
            )
            self._require_authorization_binding(row, authorization)
            if row["local_queue_id"] != body.source_queue_id:
                raise AuthorizationError("result does not bind the acknowledged local custody")
            if row["state"] == "result_uploaded":
                if row["result_digest"] != result_digest:
                    raise ConflictError("supervisor result was already uploaded with different bytes")
                provenance = self._result_provenance(
                    connection,
                    actor=actor,
                    row=row,
                )
                return {
                    "duplicate": True,
                    "event_id": authorization.event_id,
                    "provenance": provenance.model_dump(mode="json"),
                    "result_digest": result_digest,
                    "result_receipt_id": row["result_receipt_id"],
                    "state": "result_uploaded",
                }
            if row["state"] != "local_custody":
                raise ConflictError("result upload requires acknowledged local custody")
            result_receipt_id = str(uuid4())
            recorded_at = datetime.fromtimestamp(now, UTC)
            parent = self._parent_event_provenance(
                connection,
                actor=actor,
                row=row,
            )
            operation_binding = canonical_digest(
                {
                    "event_id": authorization.event_id,
                    "recipient_harness_id": actor.harness_id,
                    "source_queue_id": body.source_queue_id,
                }
            )
            step = TransformationStep(
                kind=TransformationKind.PARSER,
                operation_id=f"supervisor-result:{operation_binding}",
                implementation_id=f"native-result-parser:{canonical_digest({'schema': 'agentnet.supervisor-result.v1'})}",
                implementation_version="agentnet.supervisor-result.v1",
                executor_harness_id=actor.harness_id,
                input_digests=(parent.content_digest,),
                output_digest=result_digest,
                started_at=recorded_at,
                completed_at=recorded_at,
            )
            result_provenance = self.core.provenance.record_tainted_derivation_in_transaction(
                connection,
                object_type=ProvenanceObjectType.PARSER_OUTPUT,
                object_id=f"supervisor-result:{operation_binding}",
                domain_id=parent.domain_id,
                expected_previous_version=0,
                parent_provenance_digests=(parent.provenance_digest,),
                transformations=(step,),
                output_digest=result_digest,
                classification=parent.classification,
                allowed_sinks=parent.allowed_sinks.sinks,
                policy_revision=parent.policy_revision,
                recorded_at=recorded_at,
                when=recorded_at,
            )
            provenance_reference = result_provenance.reference()
            encrypted = self.store.cipher.encrypt_json(
                result_value,
                purpose=f"supervisor-result:{authorization.event_id}:{actor.harness_id}",
            )
            cursor = connection.execute(
                """
                UPDATE supervisor_executions
                   SET state='result_uploaded',result_receipt_id=?,result_digest=?,
                       result_encrypted=?,result_provenance_digest=?,
                       result_provenance_json=?,result_recorded_at=?,updated_at=?
                 WHERE event_id=? AND recipient_harness_id=? AND state='local_custody'
                """,
                (
                    result_receipt_id,
                    result_digest,
                    encrypted,
                    result_provenance.provenance_digest,
                    canonical_json(provenance_reference.model_dump(mode="json")).decode("utf-8"),
                    now,
                    now,
                    authorization.event_id,
                    actor.harness_id,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("supervisor result state changed concurrently")
            self.store.append_audit(
                connection,
                {
                    "action": "supervisor.result.uploaded",
                    "actor": actor.audit_view(),
                    "event_id": authorization.event_id,
                    "result_digest": result_digest,
                    "result_provenance_digest": result_provenance.provenance_digest,
                    "result_receipt_id": result_receipt_id,
                },
            )
            return {
                "duplicate": False,
                "event_id": authorization.event_id,
                "provenance": provenance_reference.model_dump(mode="json"),
                "result_digest": result_digest,
                "result_receipt_id": result_receipt_id,
                "state": "result_uploaded",
            }

    def status(self, *, actor: VerifiedActor, event_id: str) -> dict[str, Any]:
        if actor.harness_id is None:
            raise AuthorizationError("supervisor status requires exact recipient attribution")
        now = int(time.time())
        with self.store.transaction(immediate=False) as connection:
            row = self._execution_row(
                connection, event_id=event_id, recipient_id=actor.harness_id
            )
            if row is None:
                raise AuthorizationError("supervisor execution is not visible")
            self._require_current_execution(
                connection,
                actor=actor,
                row=row,
                now=now,
                require_unexpired_authorization=False,
            )
            provenance = (
                self._result_provenance(connection, actor=actor, row=row)
                if row["state"] == "result_uploaded"
                else None
            )
            return {
                "authorization_expires_at": int(row["authorization_expires_at"]),
                "custody_receipt_id": row["custody_receipt_id"],
                "event_id": event_id,
                "provenance": (
                    provenance.model_dump(mode="json") if provenance is not None else None
                ),
                "result_digest": row["result_digest"],
                "result_receipt_id": row["result_receipt_id"],
                "schema": "agentnet.supervisor.execution-status.v1",
                "state": row["state"],
                "updated_at": int(row["updated_at"]),
            }


def create_supervisor_routes(
    core: "CommunicationCore",
    body_and_actor: BodyAndActor,
    *,
    local_binding_service: "LocalBindingService | None" = None,
) -> list[Route]:
    service = SupervisorExecutionService(core)

    async def authorize(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = EligibilityBody.model_validate_json(body)
        return JSONResponse(service.authorize(actor=actor, body=parsed))

    async def custody(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = CustodyBody.model_validate_json(body)
        result = service.acknowledge_custody(actor=actor, body=parsed)
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def result(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = ResultBody.model_validate_json(body)
        value = service.upload_result(actor=actor, body=parsed)
        return JSONResponse(value, status_code=200 if value["duplicate"] else 201)

    async def status(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        return JSONResponse(
            service.status(actor=actor, event_id=request.path_params["event_id"])
        )

    async def bind_child(request: Request) -> Response:
        if local_binding_service is None:
            raise AuthorizationError("local harness binding is disabled")
        body, actor = await body_and_actor(request, core)
        if actor.harness_id is None:
            raise AuthorizationError("local harness binding requires an exact harness actor")
        parsed = LocalBindingChildBody.model_validate_json(body)
        issued = local_binding_service.register_or_issue_child(
            harness_id=actor.harness_id,
            pid=parsed.pid,
            session_id=parsed.session_id,
            expected_process_start_time=parsed.process_start_time,
            expected_process_measurement=parsed.process_measurement,
        )
        response = issued.redacted()
        capability = getattr(issued, "capability", None)
        if capability is not None:
            response["capability"] = capability
        return JSONResponse(response, status_code=201)

    return [
        Route("/v1/supervisor/executions/authorize", authorize, methods=["POST"]),
        Route("/v1/supervisor/executions/custody", custody, methods=["POST"]),
        Route("/v1/supervisor/executions/result", result, methods=["POST"]),
        Route("/v1/supervisor/executions/{event_id}/status", status, methods=["GET"]),
        Route("/v1/supervisor/local-binding/children", bind_child, methods=["POST"]),
    ]


__all__ = [
    "BackgroundAuthorizationBody",
    "CustodyBody",
    "EligibilityBody",
    "LocalBindingChildBody",
    "ResultBody",
    "SupervisorExecutionService",
    "create_supervisor_routes",
]
